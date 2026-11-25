package account_test

import (
	"testing"

	"github.com/pandokos/pandokos/internal/account"
	"github.com/pandokos/pandokos/test/dbtest"
)

// signIns flattens Stats().Attempts (keyed by a value with the fields Method and Result) into
// "method|result" counts.
func signIns(svc *account.Service) map[string]int64 {
	out := map[string]int64{}
	for k, v := range svc.Stats().Attempts {
		out[k.Method+"|"+k.Result] += v
	}
	return out
}

// TestSignInStats is T-920 (sign-in part): every result of the closed list is provoked for
// passwords (ok, failed, locked, disabled, unavailable) and counted by method and result, the locks
// are counted, and the snapshot holds nothing of a user.
func TestSignInStats(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(o *account.Options) { o.MaxFailures = 3 })
	ada := e.d.PlatformUser(t, "ada.lovelace", []string{"platform-admin"})
	off := e.d.PlatformUser(t, "disabled.user", nil, dbtest.Disabled())
	lockme := e.d.PlatformUser(t, "lock.me", nil)

	want := map[string]int64{}
	check := func(what string) {
		t.Helper()
		got := signIns(e.svc)
		for k, v := range want {
			if got[k] != v {
				t.Errorf("after %s: %q counted %d, want %d", what, k, got[k], v)
			}
		}
		for k, v := range got {
			if want[k] == 0 {
				t.Errorf("after %s: %q counted %d, want nothing", what, k, v)
			}
		}
	}
	if got := signIns(e.svc); len(got) != 0 || e.svc.Stats().Locks != 0 {
		t.Fatalf("a new service has counted %v", got)
	}

	if _, err := e.signIn(t, "ada.lovelace", ada.Password, src1, ""); err != nil {
		t.Fatal(err)
	}
	want["password|"+account.SignInOK]++
	check("a good sign-in")

	if _, err := e.signIn(t, "ada.lovelace", "wrong "+ada.Password, src1, ""); err == nil {
		t.Fatal("a wrong password signed in")
	}
	want["password|"+account.SignInFailed]++
	if _, err := e.signIn(t, "nobody.at.all", "whatever password 123", src1, ""); err == nil {
		t.Fatal("an unknown user signed in")
	}
	want["password|"+account.SignInFailed]++
	check("two failures")

	if _, err := e.signIn(t, "disabled.user", off.Password, src1, ""); err == nil {
		t.Fatal("a disabled user signed in")
	}
	want["password|"+account.SignInDisabled]++
	check("a disabled user")

	// The third failure of an account locks it; the next attempt meets the lock.
	for i := 0; i < 3; i++ {
		if _, err := e.signIn(t, "lock.me", "wrong "+lockme.Password, src2, ""); err == nil {
			t.Fatal("a wrong password signed in")
		}
		want["password|"+account.SignInFailed]++
	}
	if got := e.svc.Stats().Locks; got != 1 {
		t.Errorf("locks = %d after the third failure, want 1", got)
	}
	if _, err := e.signIn(t, "lock.me", lockme.Password, src2, ""); err == nil {
		t.Fatal("a locked account signed in")
	}
	want["password|"+account.SignInLocked]++
	check("a locked account")
	if got := e.svc.Stats().Locks; got != 1 {
		t.Errorf("locks = %d after an attempt on a locked account: a lock is counted when it is made", got)
	}

	// With the database stopped the sign-in cannot be answered.
	e.d.SetAcceptingConnections(t, false)
	_, err := e.signIn(t, "ada.lovelace", ada.Password, src1, "")
	e.d.SetAcceptingConnections(t, true)
	if err == nil {
		t.Fatal("signed in with the database stopped")
	}
	want["password|"+account.SignInUnavailable]++
	check("an unavailable database")

	// Closed lists only, and no user.
	closed := map[string]bool{}
	for _, r := range account.SignInResults {
		closed[r] = true
	}
	for k := range e.svc.Stats().Attempts {
		if k.Method != "password" && k.Method != "oidc" || !closed[k.Result] {
			t.Errorf("a value outside the closed lists: %+v", k)
		}
	}
}
