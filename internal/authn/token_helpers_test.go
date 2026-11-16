package authn

import (
	"context"
	"crypto"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/jwks"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/fakeidp"
)

// tokenNow is the instant of every unit test of token validation.
var tokenNow = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

const (
	testSkew        = 60 * time.Second
	testMaxLifetime = 24 * time.Hour
	testAudience    = "pandokos-acme-eu"
	testRegID       = "0192f0f0-0000-7000-8000-0000000000a1"
	testTenantID    = "0192f0f0-0000-7000-8000-0000000000b1"
)

// serverKeys is a KeySource backed by the keys of a fake issuer, without HTTP. It records every
// call, so that a test can see which source and key ID the validation asked for.
type serverKeys struct {
	idp *fakeidp.Server

	mu    sync.Mutex
	calls []keyCall
	err   error            // returned by every call when set
	force crypto.PublicKey // returned for every call when set: a hostile or buggy source
}

type keyCall struct {
	Src      jwks.Source
	Kid, Alg string
}

func (k *serverKeys) Key(_ context.Context, src jwks.Source, kid, alg string) (crypto.PublicKey, error) {
	k.mu.Lock()
	k.calls = append(k.calls, keyCall{src, kid, alg})
	err, force := k.err, k.force
	k.mu.Unlock()
	switch {
	case err != nil:
		return nil, err
	case force != nil:
		return force, nil
	case kid == "":
		// The one key of the algorithm, as the key cache gives it.
		return k.idp.PublicKey(alg), nil
	}
	if pub := k.idp.PublicKeyByID(kid); pub != nil {
		return pub, nil
	}
	return nil, jwks.ErrKeyNotFound
}

func (k *serverKeys) callLog() []keyCall {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]keyCall(nil), k.calls...)
}

// tokenRig is an authenticator that validates tokens against a fake issuer with a fixed clock, no
// database and no cache.
type tokenRig struct {
	idp  *fakeidp.Server
	keys *serverKeys
	a    *Authenticator
	reg  registration
}

func newTokenRig(t testing.TB) *tokenRig {
	t.Helper()
	idp := fakeidp.Start(t, fakeidp.Options{})
	keys := &serverKeys{idp: idp}
	a := &Authenticator{
		kinds: map[Kind]bool{KindToken: true},
		now:   func() time.Time { return tokenNow },
		tok:   TokenOptions{Keys: keys, MaxBytes: 8192, ClockSkew: testSkew, MaxLifetime: testMaxLifetime},
	}
	tid, err := tenant.ParseID(testTenantID)
	if err != nil {
		t.Fatal(err)
	}
	return &tokenRig{idp: idp, keys: keys, a: a, reg: registration{
		Scope: "tenant", ID: testRegID, TenantID: tid, Issuer: idp.Issuer(), Audience: testAudience,
		Algorithms: append([]string(nil), fakeidp.Algorithms...), SubjectClaim: "sub", Enabled: true,
	}}
}

// claims returns the standard claims at tokenNow: a token that lives for ten minutes.
func (r *tokenRig) claims(sub string) fakeidp.Claims {
	return r.idp.StandardAt(tokenNow, sub, testAudience, 10*time.Minute)
}

// run does what Authenticate does before the database: peek, then validate against the
// registration of the rig. The error is that of the first step that refuses.
func (r *tokenRig) run(token string) (verified, error) { return r.runAgainst(token, r.reg) }

func (r *tokenRig) runAgainst(token string, reg registration) (verified, error) {
	// What Authenticate does before peek: a value that does not have the shape of a token is malformed.
	if !looksLikeToken(token, r.a.tok.MaxBytes) {
		return verified{}, &Failure{Reason: "malformed"}
	}
	alg, kid, _, _, err := peek(token)
	if err != nil {
		return verified{}, err
	}
	return r.a.validate(context.Background(), token, alg, kid, reg)
}

// refusal returns the *Failure of err, failing the test when err is nil or another kind of error.
func refusal(t testing.TB, what string, err error) *Failure {
	t.Helper()
	if err == nil {
		t.Errorf("%s: the token was accepted", what)
		return &Failure{}
	}
	var f *Failure
	if !errors.As(err, &f) {
		t.Errorf("%s: err = %v, want a *Failure", what, err)
		return &Failure{}
	}
	if !errors.Is(err, authz.ErrUnauthenticated) {
		t.Errorf("%s: %v does not wrap authz.ErrUnauthenticated", what, err)
	}
	return f
}

// wantReason checks that err is a refusal with one of the reasons.
func wantReason(t testing.TB, what string, err error, reasons ...string) *Failure {
	t.Helper()
	f := refusal(t, what, err)
	for _, r := range reasons {
		if f.Reason == r {
			return f
		}
	}
	if f.Reason != "" || err != nil {
		t.Errorf("%s: reason %q, want %s", what, f.Reason, strings.Join(reasons, " or "))
	}
	return f
}

func mustAccept(t testing.TB, what string, v verified, err error) verified {
	t.Helper()
	if err != nil {
		t.Errorf("%s: refused: %v", what, err)
	}
	return v
}

func sourceOf(r registration) jwks.Source {
	return jwks.Source{Issuer: r.Issuer, JWKSURI: r.JWKSURI}
}

func errorsIsUnavailable(err error) bool   { return errors.Is(err, authz.ErrUnavailable) }
func errorsAs(err error, f **Failure) bool { return errors.As(err, f) }
