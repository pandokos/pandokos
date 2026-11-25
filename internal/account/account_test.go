package account_test

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/account"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/password"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/session"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/test/dbtest"
)

var (
	src1 = netip.MustParseAddr("192.0.2.1")
	src2 = netip.MustParseAddr("192.0.2.2")
)

type env struct {
	d        *dbtest.Database
	db       *store.DB
	hasher   *password.Hasher
	sessions *session.Store
	svc      *account.Service
}

func newEnv(t *testing.T, mutate ...func(*account.Options)) *env {
	t.Helper()
	d := dbtest.NewMigrated(t)
	e := &env{d: d, db: d.Open(t, store.RoleAdmin)}
	var err error
	if e.hasher, err = password.New(dbtest.FloorParams, 4); err != nil {
		t.Fatalf("password.New: %v", err)
	}
	e.sessions = session.NewStore(session.Options{DB: e.db, Idle: 30 * time.Minute, Absolute: 12 * time.Hour, MaxPerUser: 10})
	o := account.Options{
		DB: e.db, Hasher: e.hasher, Sessions: e.sessions, MinLength: 12, GeneratedPasswordTTL: 72 * time.Hour,
		MaxFailures: 10, SourceMaxFailures: 50, FailureWindow: 15 * time.Minute, LockDuration: 15 * time.Minute,
	}
	for _, m := range mutate {
		m(&o)
	}
	e.svc = account.NewService(o)
	return e
}

func ctx(t testing.TB) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return c
}

func (e *env) signIn(t testing.TB, username, pw string, src netip.Addr, presented string) (account.SignInResult, error) {
	t.Helper()
	return e.svc.SignIn(ctx(t), username, secret.New(pw), src, presented)
}

func refusal(t testing.TB, what string, err error) *account.Refusal {
	t.Helper()
	var r *account.Refusal
	if !errors.As(err, &r) {
		t.Fatalf("%s: err = %v, want a *account.Refusal", what, err)
	}
	return r
}

func (e *env) localRow(t testing.TB, userID string) (hash string, mustChange bool, expires *time.Time) {
	t.Helper()
	err := e.d.Super(t).QueryRow(ctx(t), "SELECT password_hash, must_change, password_expires_at FROM pandokos.local_account WHERE user_id = $1::uuid", userID).Scan(&hash, &mustChange, &expires)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func (e *env) sessionsOf(t testing.TB, userID string) int {
	return e.d.Count(t, "SELECT count(*) FROM pandokos.user_session WHERE user_id = $1::uuid", userID)
}

func (e *env) lookup(t testing.TB, token secret.Secret) error {
	t.Helper()
	_, _, err := e.sessions.Lookup(ctx(t), token.Reveal())
	return err
}

// TestSignIn is T-781.
func TestSignIn(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.d.PlatformUser(t, "ada.lovelace", []string{"platform-admin"}, dbtest.Display("Ada", "ada@example.test"))

	// Failures first, so that the throttle row exists when the password is right.
	for i := 0; i < 2; i++ {
		if _, err := e.signIn(t, "ada.lovelace", "wrong "+u.Password, src1, ""); err == nil {
			t.Fatal("a wrong password signed in")
		}
	}
	if rows := e.d.ThrottleRows(t); len(rows) == 0 {
		t.Fatal("no throttle row after two failures")
	}
	res, err := e.signIn(t, "Ada.Lovelace", u.Password, src1, "") // the username is lower-cased
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if res.User.ID != u.ID || res.User.Username != "ada.lovelace" || res.User.SignIn != "local" || res.User.State != "active" ||
		res.User.DisplayName != "Ada" || !res.User.TenantID.IsZero() || res.User.MustChange {
		t.Errorf("user %+v", res.User)
	}
	if !strings.HasPrefix(res.Token.Reveal(), "pds_") || res.Session.UserID != u.ID || res.Session.Method != "local" || res.Session.ExpiresAt.Before(time.Now().Add(11*time.Hour)) {
		t.Errorf("session %+v", res.Session)
	}
	if err := e.lookup(t, res.Token); err != nil {
		t.Errorf("the session that SignIn made does not look up: %v", err)
	}
	for _, r := range e.d.ThrottleRows(t) {
		if r.Kind == "account" {
			t.Errorf("the throttle row of the account is still there: %+v", r)
		}
	}

	t.Run("the hash is upgraded when the parameters changed", func(t *testing.T) {
		stronger := password.Params{MemoryKiB: 19456, Iterations: 3, Parallelism: 1}
		h, err := password.New(stronger, 4)
		if err != nil {
			t.Fatal(err)
		}
		svc := account.NewService(account.Options{DB: e.db, Hasher: h, Sessions: e.sessions, MinLength: 12, GeneratedPasswordTTL: time.Hour,
			MaxFailures: 10, SourceMaxFailures: 50, FailureWindow: time.Minute * 15, LockDuration: 15 * time.Minute})
		before, _, _ := e.localRow(t, u.ID)
		if !strings.Contains(before, "m=19456,t=2,p=1") {
			t.Fatalf("the seed: %s", before)
		}
		if _, err := svc.SignIn(ctx(t), "ada.lovelace", secret.New(u.Password), src1, ""); err != nil {
			t.Fatal(err)
		}
		after, _, _ := e.localRow(t, u.ID)
		if !strings.Contains(after, "m=19456,t=3,p=1") || after == before {
			t.Errorf("the hash was not replaced by one with the configured parameters: %s", after)
		}
		if _, err := svc.SignIn(ctx(t), "ada.lovelace", secret.New(u.Password), src1, ""); err != nil {
			t.Errorf("the upgraded hash does not verify: %v", err)
		}
		if h2, _, _ := e.localRow(t, u.ID); h2 != after {
			t.Error("a hash with the configured parameters was replaced again")
		}
	})

	t.Run("a presented session is deleted", func(t *testing.T) {
		old, err := e.signIn(t, "ada.lovelace", u.Password, src1, "")
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := e.signIn(t, "ada.lovelace", u.Password, src1, old.Token.Reveal())
		if err != nil {
			t.Fatal(err)
		}
		if err := e.lookup(t, old.Token); !errors.Is(err, session.ErrNoSession) {
			t.Errorf("the presented session still works: %v", err)
		}
		if err := e.lookup(t, fresh.Token); err != nil {
			t.Errorf("the new session: %v", err)
		}
		if fresh.Token.Reveal() == old.Token.Reveal() {
			t.Error("the token was not renewed")
		}
	})

	t.Run("the eleventh session removes the oldest", func(t *testing.T) {
		e := newEnv(t)
		u := e.d.PlatformUser(t, "grace.hopper", []string{"platform-admin"})
		var tokens []secret.Secret
		for i := 0; i < 11; i++ {
			res, err := e.signIn(t, "grace.hopper", u.Password, src1, "")
			if err != nil {
				t.Fatal(err)
			}
			tokens = append(tokens, res.Token)
		}
		if n := e.sessionsOf(t, u.ID); n != 10 {
			t.Fatalf("%d sessions, want 10", n)
		}
		if err := e.lookup(t, tokens[0]); !errors.Is(err, session.ErrNoSession) {
			t.Errorf("the oldest session survives: %v", err)
		}
		for _, tok := range tokens[1:] {
			if err := e.lookup(t, tok); err != nil {
				t.Errorf("a newer session was removed: %v", err)
			}
		}
	})
}

// TestSignInTenantUser checks the user a tenant manages: the sign-in is the same and the result names
// the managing tenant.
func TestSignInTenantUser(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tid := e.d.SeedTenant(t, "acme")
	u := e.d.TenantUser(t, tid, "tenant.person", []string{"tenant-admin"})
	res, err := e.signIn(t, "tenant.person", u.Password, src1, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.User.TenantID != tid || res.User.ID != u.ID {
		t.Errorf("user %+v, want it managed by %s", res.User, tid)
	}
}

// TestSignInUniform is T-782.
func TestSignInUniform(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	good := e.d.PlatformUser(t, "good.user", nil)
	disabled := e.d.PlatformUser(t, "disabled.user", nil, dbtest.Disabled())
	expired := e.d.PlatformUser(t, "expired.user", nil, dbtest.MustChange(time.Now().Add(-time.Hour)))
	cases := []struct {
		name, user, pw string
		reason         string
	}{
		{"unknown username", "nobody.here", "some password 123", "unknown_user"},
		{"wrong password", "good.user", "not the password 123", "wrong_password"},
		{"disabled user", "disabled.user", disabled.Password, "user_disabled"},
		{"expired generated password", "expired.user", expired.Password, "password_expired"},
	}
	_ = good
	for i, c := range cases {
		src := netip.MustParseAddr("198.51.100." + string(rune('1'+i)))
		before := e.hasher.Computations()
		_, err := e.signIn(t, c.user, c.pw, src, "")
		r := refusal(t, c.name, err)
		if !errors.Is(err, authz.ErrUnauthenticated) || errors.Is(err, account.ErrThrottled) || r.RetryAfter != 0 {
			t.Errorf("%s: %v (retry after %v)", c.name, err, r.RetryAfter)
		}
		if r.Reason != c.reason {
			t.Errorf("%s: reason %q, want %q", c.name, r.Reason, c.reason)
		}
		if n := e.hasher.Computations() - before; n != 1 {
			t.Errorf("%s: %d hash computations, want exactly 1", c.name, n)
		}
	}
	// The rows written: one account row and one source row per refusal, each with one failure,
	// whether or not an account has the username.
	rows := e.d.ThrottleRows(t)
	count := map[string]int{}
	for _, r := range rows {
		if r.Failures != 1 || r.LockedUntil != nil {
			t.Errorf("row %+v: want one failure and no lock", r)
		}
		count[r.Kind]++
	}
	if count["account"] != 4 || count["source"] != 4 {
		t.Errorf("rows by kind %v, want 4 account and 4 source rows", count)
	}
}

// delay is the delay after n failures in the window: none below three, then 1 s doubling to 30 s.
func delay(n int) time.Duration {
	if n < 3 {
		return 0
	}
	d := time.Second << (n - 3)
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// TestThrottle is T-783.
func TestThrottle(t *testing.T) {
	t.Parallel()
	for _, known := range []bool{true, false} {
		name := "unknown username"
		if known {
			name = "known username"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			username, right := "victim.user", "the right password 1"
			if known {
				right = e.d.PlatformUser(t, username, nil).Password
			}
			bad := func() error { _, err := e.signIn(t, username, "wrong password here", src1, ""); return err }
			wait := func(by time.Duration) { e.d.Age(t, "signin_throttle", "kind = 'account'", by, "last_failure_at") }

			for n := 1; n <= 9; n++ {
				// No delay for the first two failures: the next one follows at once.
				err := bad()
				if r := refusal(t, "failure", err); r.RetryAfter != 0 || errors.Is(err, account.ErrThrottled) {
					t.Fatalf("failure %d was throttled: %v", n, err)
				}
				if n < 3 {
					continue
				}
				// From the third on the next attempt is delayed, and no password is checked, the right one included.
				before := e.hasher.Computations()
				_, err = e.signIn(t, username, right, src1, "")
				r := refusal(t, "attempt after a failure", err)
				if want := delay(n); !errors.Is(err, account.ErrThrottled) || r.RetryAfter <= want-2*time.Second || r.RetryAfter > want {
					t.Errorf("after %d failures: %v (retry after %v), want a delay of %v", n, err, r.RetryAfter, want)
				}
				if got := e.hasher.Computations() - before; got != 0 {
					t.Errorf("after %d failures: a refused attempt computed %d hashes", n, got)
				}
				wait(31 * time.Second)
			}
			// The tenth failure locks the username.
			_ = refusal(t, "tenth failure", bad())
			for _, pw := range []string{right, "wrong password here"} {
				before := e.hasher.Computations()
				_, err := e.signIn(t, username, pw, src1, "")
				r := refusal(t, "locked", err)
				if !errors.Is(err, account.ErrThrottled) || r.RetryAfter < 14*time.Minute || r.RetryAfter > 15*time.Minute {
					t.Errorf("locked: %v (retry after %v), want about 15 minutes", err, r.RetryAfter)
				}
				if e.hasher.Computations() != before {
					t.Error("a locked username computed a hash")
				}
			}
			wait(31 * time.Second) // the delay passes, the lock does not
			if _, err := e.signIn(t, username, right, src1, ""); !errors.Is(err, account.ErrThrottled) {
				t.Errorf("a lock lifted by the passing of the delay: %v", err)
			}
			// The lock passes.
			e.d.Age(t, "signin_throttle", "kind = 'account'", 16*time.Minute, "window_started_at", "last_failure_at", "locked_until")
			_, err := e.signIn(t, username, right, src1, "")
			if known && err != nil {
				t.Errorf("the right password after the lock: %v", err)
			}
			if !known {
				_ = refusal(t, "unknown username after the lock", err)
			}
		})
	}

	t.Run("the window restarts the count", func(t *testing.T) {
		e := newEnv(t)
		for i := 0; i < 2; i++ {
			_, _ = e.signIn(t, "someone.else", "wrong password here", src1, "")
		}
		e.d.Age(t, "signin_throttle", "kind = 'account'", 16*time.Minute, "window_started_at", "last_failure_at")
		_, _ = e.signIn(t, "someone.else", "wrong password here", src1, "")
		for _, r := range e.d.ThrottleRows(t) {
			if r.Kind == "account" && r.Failures != 1 {
				t.Errorf("%d failures after the window passed, want the count restarted at 1", r.Failures)
			}
		}
	})
}

// TestSourceThrottle is T-784.
func TestSourceThrottle(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(o *account.Options) { o.SourceMaxFailures = 50 })
	u := e.d.PlatformUser(t, "innocent.user", nil)
	attacker := netip.MustParseAddr("203.0.113.9")
	for i := 0; i < 50; i++ {
		e.d.Age(t, "signin_throttle", "kind = 'source'", 31*time.Second, "last_failure_at") // the delay of the row is not the point
		_, err := e.signIn(t, "name"+strings.Repeat("x", i%5)+string(rune('a'+i%26))+string(rune('a'+i/26)), "wrong password here", attacker, "")
		if r := refusal(t, "failure", err); r.RetryAfter != 0 {
			t.Fatalf("failure %d from the source was throttled: %v", i+1, err)
		}
	}
	// The source is locked for every username, the right password included.
	before := e.hasher.Computations()
	_, err := e.signIn(t, "innocent.user", u.Password, attacker, "")
	if r := refusal(t, "locked source", err); !errors.Is(err, account.ErrThrottled) || r.RetryAfter <= 0 {
		t.Errorf("the source after 50 failures: %v", err)
	}
	if e.hasher.Computations() != before {
		t.Error("a locked source computed a hash")
	}
	// Another source is unaffected.
	if _, err := e.signIn(t, "innocent.user", u.Password, src2, ""); err != nil {
		t.Errorf("another source: %v", err)
	}

	t.Run("IPv6 addresses of one /64 share a row", func(t *testing.T) {
		e := newEnv(t)
		for _, a := range []string{"2001:db8:0:1::1", "2001:db8:0:1:ffff:ffff:ffff:ffff", "2001:db8:0:1:abcd::1"} {
			e.d.Age(t, "signin_throttle", "kind = 'source'", 31*time.Second, "last_failure_at")
			_, _ = e.signIn(t, "v6.user", "wrong password here", netip.MustParseAddr(a), "")
		}
		var rows []dbtest.Throttle
		for _, r := range e.d.ThrottleRows(t) {
			if r.Kind == "source" {
				rows = append(rows, r)
			}
		}
		if len(rows) != 1 || rows[0].Failures != 3 {
			t.Errorf("source rows %+v, want one row with 3 failures", rows)
		}
		e.d.Age(t, "signin_throttle", "", 31*time.Second, "last_failure_at") // the delay of the account row passes too
		_, _ = e.signIn(t, "v6.user", "wrong password here", netip.MustParseAddr("2001:db8:0:2::1"), "")
		n := 0
		for _, r := range e.d.ThrottleRows(t) {
			if r.Kind == "source" {
				n++
			}
		}
		if n != 2 {
			t.Errorf("%d source rows after an address of another /64, want 2", n)
		}
	})

	t.Run("IPv4 addresses are rows of their own", func(t *testing.T) {
		e := newEnv(t)
		_, _ = e.signIn(t, "v4.user", "wrong password here", src1, "")
		_, _ = e.signIn(t, "v4.user2", "wrong password here", src2, "")
		n := 0
		for _, r := range e.d.ThrottleRows(t) {
			if r.Kind == "source" {
				n++
			}
		}
		if n != 2 {
			t.Errorf("%d source rows for two /32 prefixes, want 2", n)
		}
	})
}

// TestGeneratedPasswordExpiry is T-786.
func TestGeneratedPasswordExpiry(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	live := e.d.PlatformUser(t, "live.generated", nil, dbtest.MustChange(time.Now().Add(time.Hour)))
	old := e.d.PlatformUser(t, "old.generated", nil, dbtest.MustChange(time.Now().Add(-time.Minute)))
	res, err := e.signIn(t, "live.generated", live.Password, src1, "")
	if err != nil {
		t.Fatalf("a generated password before its expiry: %v", err)
	}
	if !res.User.MustChange {
		t.Error("the account of a generated password does not carry must_change")
	}
	_, err = e.signIn(t, "old.generated", old.Password, src1, "")
	if r := refusal(t, "expired", err); r.Reason != "password_expired" || !errors.Is(err, authz.ErrUnauthenticated) {
		t.Errorf("after the expiry: %v", err)
	}
	_, wrong := e.signIn(t, "live.generated", "wrong password here", src2, "")
	if r := refusal(t, "wrong", wrong); !errors.Is(wrong, authz.ErrUnauthenticated) || r.RetryAfter != 0 {
		t.Errorf("a wrong password: %v", wrong)
	}
	if n := e.sessionsOf(t, old.ID); n != 0 {
		t.Errorf("%d sessions of the account whose password expired", n)
	}
}

// TestChangePassword is T-787.
func TestChangePassword(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.d.PlatformUser(t, "change.user.name", nil, dbtest.MustChange(time.Now().Add(time.Hour)))
	first, err := e.signIn(t, "change.user.name", u.Password, src1, "")
	if err != nil {
		t.Fatal(err)
	}
	other1, err := e.signIn(t, "change.user.name", u.Password, src1, "")
	if err != nil {
		t.Fatal(err)
	}
	other2, err := e.signIn(t, "change.user.name", u.Password, src2, "")
	if err != nil {
		t.Fatal(err)
	}
	change := func(cur, next string) (account.SignInResult, error) {
		return e.svc.ChangePassword(ctx(t), first.Session, secret.New(cur), secret.New(next), src1)
	}

	// A wrong current password is refused and counted.
	_, err = change("wrong current password", "a brand new password 1")
	var pe *account.PolicyError
	if !errors.As(err, &pe) || pe.Field != "current_password" || pe.Violation == "" {
		t.Errorf("wrong current password: %v, want a PolicyError on current_password", err)
	}
	counted := 0
	for _, r := range e.d.ThrottleRows(t) {
		if r.Kind == "account" {
			counted += r.Failures
		}
	}
	if counted != 1 {
		t.Errorf("%d failures counted for a wrong current password, want 1", counted)
	}
	// Policy violations.
	for name, c := range map[string]struct{ next, sentence string }{
		"too short": {"short", "must have at least 12 characters"},
		"too long":  {strings.Repeat("a", 257), "must have at most 256 characters"},
		"username":  {"change.user.name", "must not be the username"},
		"the same":  {u.Password, ""},
	} {
		_, err := change(u.Password, c.next)
		if !errors.As(err, &pe) || pe.Field != "new_password" || (c.sentence != "" && pe.Violation != c.sentence) || pe.Violation == "" {
			t.Errorf("%s: %v, want a PolicyError on new_password (%q)", name, err, c.sentence)
		}
	}
	if n := e.sessionsOf(t, u.ID); n != 3 {
		t.Fatalf("%d sessions after refused changes, want 3", n)
	}

	// Success.
	const next = "a brand new password 1"
	res, err := change(u.Password, next)
	if err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if res.User.MustChange || res.Token.IsZero() || res.Token.Reveal() == first.Token.Reveal() {
		t.Errorf("result %+v", res)
	}
	if _, must, expires := e.localRow(t, u.ID); must || expires != nil {
		t.Errorf("must_change=%v expires=%v after the change, want the flag and the expiry cleared", must, expires)
	}
	if err := e.lookup(t, first.Token); !errors.Is(err, session.ErrNoSession) {
		t.Errorf("the old token of the session in use works: %v", err)
	}
	if err := e.lookup(t, res.Token); err != nil {
		t.Errorf("the replacement session: %v", err)
	}
	for _, o := range []account.SignInResult{other1, other2} {
		if err := e.lookup(t, o.Token); !errors.Is(err, session.ErrNoSession) {
			t.Errorf("another session of the user survived the change: %v", err)
		}
	}
	if n := e.sessionsOf(t, u.ID); n != 1 {
		t.Errorf("%d sessions after the change, want 1", n)
	}
	if _, err := e.signIn(t, "change.user.name", u.Password, src1, ""); err == nil {
		t.Error("the old password still signs in")
	}
	if _, err := e.signIn(t, "change.user.name", next, src1, ""); err != nil {
		t.Errorf("the new password does not sign in: %v", err)
	}
}
