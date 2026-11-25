package vault

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
)

const (
	transitReauthEvery = 5 * time.Second // at most one new authentication after a 403 per this time
	transitBackoffMin  = time.Second
	transitBackoffMax  = 60 * time.Second
	transitPollEvery   = 20 * time.Millisecond // the lifecycle polls an injected clock
)

// errNotExtendable is the attempt of a token that nothing can extend: the lifecycle warns.
var errNotExtendable = errors.New("vault: the token of the key service cannot be extended")

// tokenKey carries, in a context, the token a request must use instead of the current one (a
// candidate that is looked up before it is adopted, and the token a request was sent with).
type tokenKey struct{}

// currentToken returns the token in use.
func (p *TransitProvider) currentToken() secret.Secret {
	p.tokMu.RLock()
	defer p.tokMu.RUnlock()
	return p.token
}

// rootToken reports whether the policies of the token include root.
func (p *TransitProvider) rootToken() bool {
	p.tokMu.RLock()
	defer p.tokMu.RUnlock()
	return slices.Contains(p.policies, "root")
}

// grant is what a lookup, a login or a renewal says about a token.
type grant struct {
	policies  []string
	lifetime  time.Duration // 0: the token does not expire
	renewable bool
}

// seconds converts a number of seconds of an answer to a duration; ok is false for a field that is
// missing or is not a sensible number: such an answer is rejected, never read as a token that does
// not expire (0 is that, and only when the service says it).
func seconds(n *json.Number) (d time.Duration, ok bool) {
	if n == nil {
		return 0, false
	}
	f, err := n.Float64()
	if err != nil || f < 0 || f > 1e9 {
		return 0, false
	}
	return time.Duration(f * float64(time.Second)), true
}

// adopt makes tok the token in use with the lifetime g says. fresh marks a token that is new (the
// lifetime of its first grant is then this one).
func (p *TransitProvider) adopt(tok secret.Secret, g grant, fresh bool) {
	now := p.opts.Now()
	p.tokMu.Lock()
	if fresh {
		p.token, p.first = tok, g.lifetime
	}
	if g.policies != nil {
		p.policies = g.policies
	}
	p.granted, p.obtained, p.renewable, p.expires = g.lifetime, now, g.renewable, g.lifetime > 0
	p.gen++
	p.tokMu.Unlock()
	p.lmu.Lock()
	p.authFails = 0
	p.lmu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// authenticate obtains the token at start: it reads it from its file and looks it up, or logs in
// with the AppRole.
func (p *TransitProvider) authenticate(ctx context.Context) error {
	if p.opts.AuthMethod == "approle" {
		return p.login(ctx)
	}
	tok, err := secret.ReadFile(p.opts.TokenFile)
	if err != nil {
		return err
	}
	return p.lookup(ctx, tok)
}

// lookup looks tok up with itself and adopts it as a new token.
func (p *TransitProvider) lookup(ctx context.Context, tok secret.Secret) error {
	var out lookupData
	lctx := context.WithValue(ctx, tokenKey{}, tok)
	if err := p.do(lctx, "lookup", http.MethodGet, "auth/token/lookup-self", nil, &out); err != nil {
		return err
	}
	g, err := lookupGrant(out)
	if err != nil {
		return err
	}
	p.adopt(tok, g, true)
	return nil
}

// renew extends the token in use.
func (p *TransitProvider) renew(ctx context.Context) error {
	var out renewData
	tok := p.currentToken()
	if err := p.do(context.WithValue(ctx, tokenKey{}, tok), "renew", http.MethodPost, "auth/token/renew-self", nil, &out); err != nil {
		return err
	}
	g, err := renewGrant(out)
	if err != nil {
		return err
	}
	p.adopt(tok, g, false)
	return nil
}

// login makes the AppRole login, reading both files again, and adopts the token it grants.
func (p *TransitProvider) login(ctx context.Context) error {
	roleID, err := secret.ReadFile(p.opts.RoleIDFile)
	if err != nil {
		return err
	}
	secretID, err := secret.ReadFile(p.opts.SecretIDFile)
	if err != nil {
		return err
	}
	in := struct {
		RoleID   string `json:"role_id"`
		SecretID string `json:"secret_id"`
	}{roleID.Reveal(), secretID.Reveal()}
	var out loginData
	if err := p.do(ctx, "login", http.MethodPost, "auth/"+escapePath(p.opts.AppRoleMount)+"/login", in, &out); err != nil {
		return err
	}
	defer clear(out.Raw)
	g, err := loginGrant(out)
	if err != nil {
		return err
	}
	p.adopt(secret.New(string(out.Raw)), g, true)
	return nil
}

// rawToken receives the text of a token without making a string of it on the way.
type rawToken []byte

// UnmarshalJSON keeps the bytes of a JSON string, without escapes, in a slice that can be cleared.
func (r *rawToken) UnmarshalJSON(b []byte) error {
	in, ok := transitUnquote(b)
	if !ok {
		return &TransitError{Operation: "login", Class: "rejected"}
	}
	*r = append((*r)[:0], in...)
	return nil
}

// refresh is one pass of the lifecycle at two thirds of the lifetime (§5.5).
func (p *TransitProvider) refresh(ctx context.Context) error {
	if p.opts.AuthMethod == "approle" {
		p.tokMu.RLock()
		renewable := p.renewable
		p.tokMu.RUnlock()
		if renewable {
			err := p.renew(ctx)
			if err == nil {
				p.tokMu.RLock()
				short := p.granted*3 <= p.first // a third of the first lifetime, or less
				p.tokMu.RUnlock()
				if !short {
					return nil
				}
			} else if !renewalRefused(ctx, err) {
				return err
			}
		}
		return p.login(ctx)
	}
	cand, err := secret.ReadFile(p.opts.TokenFile)
	if err != nil {
		return &TransitError{Operation: "lookup", Class: "rejected"}
	}
	if !p.currentToken().Equal(cand.Reveal()) {
		return p.lookup(ctx, cand)
	}
	p.tokMu.RLock()
	renewable := p.renewable
	p.tokMu.RUnlock()
	if renewable {
		left, _ := p.tokenLeft()
		if err := p.renew(ctx); err != nil {
			return err
		}
		p.tokMu.RLock()
		short := p.granted <= left+time.Second || p.granted*3 <= p.first
		p.tokMu.RUnlock()
		if short {
			// A token at its maximum lifetime is renewed for what it had left: nothing extends it.
			return errNotExtendable
		}
		return nil
	}
	return errNotExtendable
}

// renewalRefused reports whether a failed renewal is an answer of the service (a new login is
// then tried) and not a failure to reach it.
func renewalRefused(ctx context.Context, err error) bool {
	var te *TransitError
	if ctx.Err() != nil || !errors.As(err, &te) {
		return false
	}
	return te.Class != "unavailable" && te.Class != "refused"
}

// tokenLeft is the lifetime that is left to the token; ok is false for a token that does not expire.
func (p *TransitProvider) tokenLeft() (left time.Duration, ok bool) {
	p.tokMu.RLock()
	defer p.tokMu.RUnlock()
	if !p.expires {
		return 0, false
	}
	return max(p.granted-p.opts.Now().Sub(p.obtained), 0), true
}

// schedule returns the next moment of the lifecycle (not before notBefore: a pass is never followed
// by another within a second) and the generation of the state it was made
// from; the zero time means nothing is to be done.
func (p *TransitProvider) schedule(failures int, retryAt, notBefore time.Time) (time.Time, uint64) {
	p.tokMu.RLock()
	defer p.tokMu.RUnlock()
	if failures > 0 {
		return retryAt, p.gen
	}
	if !p.expires {
		return time.Time{}, p.gen
	}
	due := p.obtained.Add(p.granted * 2 / 3)
	return later(due, notBefore), p.gen
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// Run keeps the token alive until ctx ends (§5.5): at two thirds of its lifetime it is renewed, or
// obtained again; a failure is tried again after 1, 2, 4 ... 60 s.
func (p *TransitProvider) Run(ctx context.Context) {
	failures := 0
	var retryAt time.Time
	due, gen := p.schedule(failures, retryAt, time.Time{})
	for {
		now := p.opts.Now()
		if !due.IsZero() && !now.Before(due) {
			began := p.opts.Now() // the backoff counts from the start of the pass
			err := p.refresh(ctx)
			if ctx.Err() != nil {
				return
			}
			if err == nil {
				failures = 0
			} else {
				failures++
				retryAt = began.Add(min(transitBackoffMin<<min(failures-1, 6), transitBackoffMax))
				p.noteFailure(err)
			}
			due, gen = p.schedule(failures, retryAt, began.Add(time.Second))
			continue
		}
		var wait time.Duration
		switch {
		case p.polled:
			wait = transitPollEvery
		case due.IsZero():
			wait = time.Hour
		default:
			wait = due.Sub(now)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-p.wake:
			t.Stop()
		}
		p.tokMu.RLock()
		changed := p.gen != gen
		p.tokMu.RUnlock()
		if changed {
			// Another path (a new authentication after a 403) renewed the token: start over.
			failures = 0
			due, gen = p.schedule(failures, retryAt, time.Time{})
		}
	}
}

// noteFailure logs what a failed pass of the lifecycle means, at most every transitWarnEvery.
func (p *TransitProvider) noteFailure(err error) {
	now := p.opts.Now()
	if left, ok := p.tokenLeft(); ok {
		p.lmu.Lock()
		due := p.lastExpiring.IsZero() || now.Sub(p.lastExpiring) >= transitWarnEvery
		if due {
			p.lastExpiring = now
		}
		p.lmu.Unlock()
		if due {
			p.opts.Logger.Warn("key provider token expiring", "kek_provider", transitName,
				"auth_method", p.opts.AuthMethod, "expires_in_seconds", int64(left/time.Second))
		}
	}
	if !errors.Is(err, errNotExtendable) {
		p.logAuthFailure(err)
	}
}

// logAuthFailure logs a refused login, renewal or adopted token, at most every transitWarnEvery.
func (p *TransitProvider) logAuthFailure(err error) {
	class := "rejected"
	var te *TransitError
	if errors.As(err, &te) {
		class = te.Class
		if class == "unavailable" {
			p.mu.Lock()
			class = p.lastClass
			p.mu.Unlock()
		}
	}
	now := p.opts.Now()
	p.lmu.Lock()
	p.authFails++
	n := p.authFails
	due := p.lastAuthLog.IsZero() || now.Sub(p.lastAuthLog) >= transitWarnEvery
	if due {
		p.lastAuthLog = now
	}
	p.lmu.Unlock()
	if due {
		p.opts.Logger.Error("key provider authentication failing", "kek_provider", transitName,
			"auth_method", p.opts.AuthMethod, "error_class", class, "failures", n)
	}
}

// reauth makes the new authentication after a 403 (§5.5) and reports whether the token in use is
// no longer refused. It is coalesced: one runs, the others wait within their own contexts, and
// there is at most one per transitReauthEvery.
func (p *TransitProvider) reauth(ctx context.Context, refused secret.Secret) bool {
	differs := func() bool { return !p.currentToken().Equal(refused.Reveal()) }
	p.authMu.Lock()
	if differs() {
		p.authMu.Unlock()
		return true
	}
	if ch := p.reauthCh; ch != nil {
		p.authMu.Unlock()
		select {
		case <-ch:
			return differs()
		case <-ctx.Done():
			return false
		}
	}
	if !p.reauthAt.IsZero() && p.opts.Now().Sub(p.reauthAt) < transitReauthEvery {
		p.authMu.Unlock()
		return false
	}
	ch := make(chan struct{})
	p.reauthCh = ch
	p.authMu.Unlock()

	// The work does not end with the context of the caller that runs it: the others wait for it.
	err := p.reauthOnce(context.WithoutCancel(ctx), refused)

	p.authMu.Lock()
	if !errors.Is(err, errNotExtendable) {
		// Reading a file that holds the refused token again made no request: nothing to limit
		// (the limit of one per 5 s is for the requests to the key service, §5.5).
		p.reauthAt = p.opts.Now()
	}
	p.reauthCh = nil
	p.authMu.Unlock()
	close(ch)
	if err != nil && !errors.Is(err, errNotExtendable) {
		p.logAuthFailure(err)
	}
	return differs()
}

func (p *TransitProvider) reauthOnce(ctx context.Context, refused secret.Secret) error {
	if p.opts.AuthMethod == "approle" {
		return p.login(ctx)
	}
	cand, err := secret.ReadFile(p.opts.TokenFile)
	if err != nil {
		return &TransitError{Operation: "lookup", Class: "rejected"}
	}
	if refused.Equal(cand.Reveal()) {
		return errNotExtendable
	}
	return p.lookup(ctx, cand)
}
