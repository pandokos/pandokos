package account_test

import (
	"errors"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/account"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/test/dbtest"
)

// race holds the lock of a user row from a second connection, runs an operation that has verified a
// password before the lock was taken, and lets the test change the account while the operation waits
// for the lock (or, in a version that never locks, while it runs to the end). It returns the error of
// the operation once the change has been committed.
func (e *env) race(t *testing.T, userID string, op func() error, change func(exec func(sql string, args ...any))) error {
	t.Helper()
	c := ctx(t)
	conn := e.d.Super(t)
	tx, err := conn.Begin(c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(c) }()
	if _, err := tx.Exec(c, "SELECT 1 FROM pandokos.app_user WHERE id = $1::uuid FOR UPDATE", userID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- op() }()
	var res error
	finished := false
	deadline := time.Now().Add(15 * time.Second)
	for !finished && time.Now().Before(deadline) {
		var waiting int
		if err := tx.QueryRow(c, "SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case res = <-done:
			finished = true
		case <-time.After(50 * time.Millisecond):
		}
	}
	change(func(sql string, args ...any) {
		if _, err := tx.Exec(c, sql, args...); err != nil {
			t.Fatal(err)
		}
	})
	if err := tx.Commit(c); err != nil {
		t.Fatal(err)
	}
	if !finished {
		select {
		case res = <-done:
		case <-time.After(15 * time.Second):
			t.Fatal("the operation did not finish after the lock was released")
		}
	}
	return res
}

func (e *env) newHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := e.hasher.Hash(ctx(t), secret.New(pw))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestSignInRacesWithAccountChange: a sign-in whose password check overlaps a password change, a
// reset or a disable creates no session (design 5.4, 5.6).
func TestSignInRacesWithAccountChange(t *testing.T) {
	t.Run("password change", func(t *testing.T) {
		e := newEnv(t)
		u := e.d.PlatformUser(t, "race.change", nil)
		hash := e.newHash(t, "a brand new password 1")
		err := e.race(t, u.ID, func() error {
			_, err := e.signIn(t, "race.change", u.Password, src1, "")
			return err
		}, func(exec func(string, ...any)) {
			exec("UPDATE pandokos.local_account SET password_hash = $1 WHERE user_id = $2::uuid", hash, u.ID)
			exec("DELETE FROM pandokos.user_session WHERE user_id = $1::uuid", u.ID)
		})
		_ = refusal(t, "sign-in over a password change", err)
		if n := e.sessionsOf(t, u.ID); n != 0 {
			t.Errorf("%d sessions after the change, want 0", n)
		}
	})
	t.Run("disable", func(t *testing.T) {
		e := newEnv(t)
		u := e.d.PlatformUser(t, "race.disable", nil)
		err := e.race(t, u.ID, func() error {
			_, err := e.signIn(t, "race.disable", u.Password, src1, "")
			return err
		}, func(exec func(string, ...any)) {
			exec("UPDATE pandokos.app_user SET state = 'disabled' WHERE id = $1::uuid", u.ID)
			exec("DELETE FROM pandokos.user_session WHERE user_id = $1::uuid", u.ID)
		})
		_ = refusal(t, "sign-in over a disable", err)
		if n := e.sessionsOf(t, u.ID); n != 0 {
			t.Errorf("%d sessions after the disable, want 0", n)
		}
	})
	t.Run("federated disable", func(t *testing.T) {
		e := newEnv(t)
		u := e.d.FederatedUser(t, "https://idp.example.test", "race-subject", nil, dbtest.Display("Fed", "fed@example.test"))
		err := e.race(t, u.ID, func() error {
			_, err := e.svc.SignInFederated(ctx(t), account.FederatedInput{Issuer: "https://idp.example.test", Subject: "race-subject", DisplayName: "Fed", Email: "fed@example.test"})
			return err
		}, func(exec func(string, ...any)) {
			exec("UPDATE pandokos.app_user SET state = 'disabled' WHERE id = $1::uuid", u.ID)
		})
		if r := refusal(t, "federated sign-in over a disable", err); r.Reason != "user_disabled" {
			t.Errorf("reason %q, want user_disabled", r.Reason)
		}
		if n := e.sessionsOf(t, u.ID); n != 0 {
			t.Errorf("%d sessions after the disable, want 0", n)
		}
	})
}

// TestChangePasswordRacesWithReset: a change with the old password does not overwrite a reset that
// committed after the password was verified.
func TestChangePasswordRacesWithReset(t *testing.T) {
	e := newEnv(t)
	u := e.d.PlatformUser(t, "race.reset", nil)
	first, err := e.signIn(t, "race.reset", u.Password, src1, "")
	if err != nil {
		t.Fatal(err)
	}
	reset := e.newHash(t, "the password of the reset 1")
	err = e.race(t, u.ID, func() error {
		_, err := e.svc.ChangePassword(ctx(t), first.Session, secret.New(u.Password), secret.New("a brand new password 1"), src1)
		return err
	}, func(exec func(string, ...any)) {
		exec("UPDATE pandokos.local_account SET password_hash = $1, must_change = true WHERE user_id = $2::uuid", reset, u.ID)
		exec("DELETE FROM pandokos.user_session WHERE user_id = $1::uuid", u.ID)
	})
	var pe *account.PolicyError
	if !errors.As(err, &pe) || pe.Field != "current_password" {
		t.Fatalf("ChangePassword over a reset: %v, want a PolicyError on current_password", err)
	}
	hash, must, _ := e.localRow(t, u.ID)
	if hash != reset || !must {
		t.Error("the change overwrote the reset")
	}
	if n := e.sessionsOf(t, u.ID); n != 0 {
		t.Errorf("%d sessions after the reset, want 0", n)
	}
}
