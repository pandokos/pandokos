package account

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/password"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// hashWait is how long a request waits for a slot of the hasher before it is answered 503.
const hashWait = 2 * time.Second

// afterCommit collects what must be written in a tenant's session once the transaction of the
// platform scope has committed: the audit rows of a user that a tenant manages go to that tenant's
// log, which the platform scope cannot write.
type afterCommit []func(ctx context.Context) error

// record writes e to the platform log inside tx for a user that the platform manages, and queues it
// for the tenant's log otherwise.
func (s *Service) record(ctx context.Context, tx store.Tx, later *afterCommit, managing tenant.ID, e audit.Event) error {
	if managing.IsZero() {
		return audit.RecordPlatform(ctx, tx, e)
	}
	*later = append(*later, func(ctx context.Context) error {
		return s.o.DB.WithTenant(ctx, managing, func(ctx context.Context, tx store.Tx) error {
			return audit.RecordTenant(ctx, tx, managing, e)
		})
	})
	return nil
}

func (a afterCommit) run(ctx context.Context) error {
	for _, f := range a {
		if err := f(ctx); err != nil {
			return err
		}
	}
	return nil
}

func parseManaging(raw string) (tenant.ID, error) {
	if raw == "" {
		return tenant.ID{}, nil
	}
	return tenant.ParseID(raw)
}

func userOf(a queries.GetAccountForSignInRow, managing tenant.ID) User {
	return User{
		ID: a.UID, SignIn: "local", DisplayName: a.DisplayName, State: a.State, Username: a.Username,
		TenantID: managing, MustChange: a.MustChange,
	}
}

func actorOf(u User) audit.Actor {
	return audit.Actor{Type: audit.ActorUser, ID: u.ID, Name: u.Username}
}

// SignIn checks a username and a password from a source address and, when presented holds a
// session of the same user, replaces it (section 5.5). It runs in three transactions of the
// platform scope: a read of the throttle rows and the account, then, after the hash has been
// computed outside any transaction, either the count of the failure or the writes of the success.
func (s *Service) SignIn(ctx context.Context, username string, pw secret.Secret, source netip.Addr, presented string) (SignInResult, error) {
	username = strings.ToLower(username)
	akey, skey := UsernameKey(username), sourceKey(source)

	var (
		rows  []queries.GetThrottleRow
		acct  queries.GetAccountForSignInRow
		found bool
	)
	err := s.o.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		var err error
		if rows, err = q.GetThrottle(ctx, [][]byte{akey, skey}); err != nil {
			return err
		}
		acct, err = q.GetAccountForSignIn(ctx, username)
		if errors.Is(err, store.ErrNoRows) {
			return nil
		}
		found = err == nil
		return err
	})
	if err != nil {
		return SignInResult{}, err
	}
	if wait := s.retryAfter(rows, akey); wait > 0 {
		return SignInResult{}, &Refusal{Reason: "throttled", RetryAfter: wait}
	}
	managing, err := parseManaging(acct.ManagingTenantID)
	if err != nil {
		return SignInResult{}, err
	}

	// One computation always runs, against the stored hash or a dummy one; it is outside any
	// transaction. A busy hasher is no attempt.
	hctx, cancel := context.WithTimeout(ctx, hashWait)
	defer cancel()
	var ok, outdated bool
	if found {
		ok, outdated, err = s.o.Hasher.Verify(hctx, acct.PasswordHash, pw)
	} else {
		err = s.o.Hasher.Dummy(hctx, pw)
	}
	if err != nil {
		return SignInResult{}, err
	}
	reason := ""
	switch {
	case !found:
		reason = "unknown_user"
	case acct.State != "active":
		reason = "user_disabled"
	case !ok:
		reason = "wrong_password"
	case acct.PasswordExpiresAt != nil && !s.o.Now().Before(*acct.PasswordExpiresAt):
		reason = "password_expired"
	}
	if reason != "" {
		return SignInResult{}, s.failSignIn(ctx, akey, skey, reason, acct, managing, found)
	}
	newHash := ""
	if outdated {
		if newHash, err = s.o.Hasher.Hash(hctx, pw); err != nil {
			newHash = "" // the hash is upgraded at a later sign-in
		}
	}

	u := userOf(acct, managing)
	var res SignInResult
	var later afterCommit
	err = s.o.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		later = nil
		q := queries.New(tx)
		if _, err := q.DeleteThrottle(ctx, akey); err != nil {
			return err
		}
		if newHash != "" {
			if _, err := q.UpgradePasswordHash(ctx, queries.UpgradePasswordHashParams{NewHash: newHash, UserID: u.ID, OldHash: acct.PasswordHash}); err != nil {
				return err
			}
		}
		if presented != "" {
			if err := s.o.Sessions.DeletePresented(ctx, tx, presented, u.ID); err != nil {
				return err
			}
		}
		tok, sess, err := s.o.Sessions.Create(ctx, tx, u.ID, "local")
		if err != nil {
			return err
		}
		res = SignInResult{User: u, Token: tok, Session: sess}
		return s.record(ctx, tx, &later, managing, audit.Event{Actor: actorOf(u), Action: "auth.sign_in", ResourceType: "user", ResourceID: u.ID,
			Outcome: "success", Detail: map[string]any{"method": "local"}})
	})
	if err != nil {
		return SignInResult{}, err
	}
	if err := later.run(ctx); err != nil {
		return SignInResult{}, err
	}
	return res, nil
}

// failSignIn counts the failure for both keys, writes the lock when this failure caused it, and
// returns the refusal that the caller reports; an error of the count is returned as it is.
func (s *Service) failSignIn(ctx context.Context, akey, skey []byte, reason string, acct queries.GetAccountForSignInRow, managing tenant.ID, found bool) error {
	var later afterCommit
	err := s.o.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		later = nil
		locked, failures, err := s.countFailure(ctx, tx, akey, skey)
		if err != nil {
			return err
		}
		if locked && found {
			return s.record(ctx, tx, &later, managing, audit.Event{Actor: audit.Actor{Type: audit.ActorAnonymous}, Action: "user.lock", ResourceType: "user",
				ResourceID: acct.UID, Outcome: "success", Detail: map[string]any{"failures": failures}})
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := later.run(ctx); err != nil {
		return err
	}
	r := &Refusal{Reason: reason, TenantID: managing}
	if found {
		r.UserID = acct.UID
	}
	return r
}

// ChangePassword changes the password of the user of sess and ends their other sessions; the session in
// use is replaced by a new one (section 5.4). A wrong current password counts in the throttle of the
// username, like a wrong password at sign-in.
func (s *Service) ChangePassword(ctx context.Context, sess Session, current, next secret.Secret, source netip.Addr) (SignInResult, error) {
	var (
		u    queries.GetUserRow
		acct queries.GetAccountForSignInRow
		rows []queries.GetThrottleRow
	)
	var akey []byte
	err := s.o.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		var err error
		if u, err = q.GetUser(ctx, sess.UserID); err != nil {
			return err
		}
		if u.SignIn != "local" {
			return ErrNotLocal
		}
		akey = UsernameKey(u.Username)
		if acct, err = q.GetAccountForSignIn(ctx, u.Username); err != nil {
			return err
		}
		rows, err = q.GetThrottle(ctx, [][]byte{akey})
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrNoRows) {
			return SignInResult{}, ErrNoUser
		}
		return SignInResult{}, err
	}
	if wait := s.retryAfter(rows, akey); wait > 0 {
		return SignInResult{}, &Refusal{Reason: "throttled", RetryAfter: wait}
	}
	managing, err := parseManaging(acct.ManagingTenantID)
	if err != nil {
		return SignInResult{}, err
	}
	hctx, cancel := context.WithTimeout(ctx, hashWait)
	defer cancel()
	ok, _, err := s.o.Hasher.Verify(hctx, acct.PasswordHash, current)
	if err != nil {
		return SignInResult{}, err
	}
	if !ok {
		var later afterCommit
		err := s.o.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
			later = nil
			locked, failures, err := s.countFailure(ctx, tx, akey, sourceKey(source))
			if err != nil {
				return err
			}
			if locked {
				return s.record(ctx, tx, &later, managing, audit.Event{Actor: audit.Actor{Type: audit.ActorAnonymous}, Action: "user.lock", ResourceType: "user",
					ResourceID: u.ID, Outcome: "success", Detail: map[string]any{"failures": failures}})
			}
			return nil
		})
		if err == nil {
			err = later.run(ctx)
		}
		if err != nil {
			return SignInResult{}, err
		}
		return SignInResult{}, &PolicyError{Field: "current_password", Violation: "is not the current password"}
	}
	switch v := password.Check(next, u.Username, s.o.MinLength); {
	case v != "":
		return SignInResult{}, &PolicyError{Field: "new_password", Violation: v}
	case next.Reveal() == current.Reveal():
		return SignInResult{}, &PolicyError{Field: "new_password", Violation: "must differ from the current password"}
	}
	hash, err := s.o.Hasher.Hash(hctx, next)
	if err != nil {
		return SignInResult{}, err
	}

	user := userOf(acct, managing)
	user.MustChange = false
	var res SignInResult
	var later afterCommit
	err = s.o.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		later = nil
		q := queries.New(tx)
		if _, err := q.SetPassword(ctx, queries.SetPasswordParams{PasswordHash: hash, MustChange: false, UserID: u.ID}); err != nil {
			return err
		}
		if _, err := q.DeleteThrottle(ctx, akey); err != nil {
			return err
		}
		if _, err := s.o.Sessions.DeleteOfUser(ctx, tx, u.ID, ""); err != nil {
			return err
		}
		tok, created, err := s.o.Sessions.Create(ctx, tx, u.ID, sess.Method)
		if err != nil {
			return err
		}
		res = SignInResult{User: user, Token: tok, Session: created}
		return s.record(ctx, tx, &later, managing, audit.Event{Actor: actorOf(user), Action: "user.password_change", ResourceType: "user", ResourceID: u.ID, Outcome: "success"})
	})
	if err != nil {
		return SignInResult{}, err
	}
	if err := later.run(ctx); err != nil {
		return SignInResult{}, err
	}
	return res, nil
}
