package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/account"
	"github.com/pandokos/pandokos/internal/idem"
	"github.com/pandokos/pandokos/internal/password"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/test/dbtest"
)

// TestPurge is T-797: what the purge ticker of the admin role removes, and what it leaves.
func TestPurge(t *testing.T) {
	d := dbtest.NewMigrated(t)
	ctx := context.Background()
	db := d.Open(t, store.RoleAdmin)
	clk := &clock{}
	sessions := newStore(t, d, clk, 100)
	hasher, err := password.New(dbtest.FloorParams, 2)
	if err != nil {
		t.Fatalf("password.New: %v", err)
	}
	accounts := account.NewService(account.Options{
		DB: db, Hasher: hasher, Sessions: sessions, MinLength: 12, GeneratedPasswordTTL: 72 * time.Hour,
		MaxFailures: 10, SourceMaxFailures: 50, FailureWindow: 15 * time.Minute, LockDuration: 15 * time.Minute,
	})
	keys := idem.NewStore(idem.Options{DB: db, Retention: 24 * time.Hour})

	u := d.PlatformUser(t, "ada.lovelace", []string{"platform-admin"})
	var ids = map[string]string{}
	for _, name := range []string{"live", "expired", "idle"} {
		_, sess := create(t, d, sessions, u.ID, "local")
		ids[name] = sess.ID
	}
	// Expired: created and used long ago, past the absolute lifetime. Idle: used 31 minutes ago.
	d.Age(t, "user_session", "id = '"+ids["expired"]+"'", 13*time.Hour, "created_at", "last_used_at", "expires_at")
	d.Age(t, "user_session", "id = '"+ids["idle"]+"'", 31*time.Minute, "last_used_at")

	// Throttle rows: one fresh, one last failed longer than window + lock ago.
	super := d.Super(t)
	for i, age := range []string{"1 minute", "31 minutes"} {
		if _, err := super.Exec(ctx, `INSERT INTO pandokos.signin_throttle (key, kind, failures, window_started_at, last_failure_at)
			VALUES (sha256(('k' || $1::int)::bytea), 'account', 3, now() - $2::interval, now() - $2::interval)`, i, age); err != nil {
			t.Fatal(err)
		}
	}
	// Sign-in transactions: one live, one expired.
	for i, ahead := range []string{"+ interval '5 minutes'", "- interval '1 minute'"} {
		if _, err := super.Exec(ctx, `INSERT INTO pandokos.oidc_login (state_hash, created_at, expires_at)
			VALUES (sha256(('s' || $1::int)::bytea), now() - interval '10 minutes', now() `+ahead+`)`, i); err != nil {
			t.Fatal(err)
		}
	}
	// Idempotency keys: one live, one past its retention.
	for i, ahead := range []string{"+ interval '1 hour'", "- interval '1 minute'"} {
		if _, err := super.Exec(ctx, `INSERT INTO pandokos.idempotency_key (tenant_id, principal_kind, principal_id, key, operation, salt, request_hash, locked_until, expires_at)
			VALUES (NULL, 'platform', 'p', 'key-' || $1::int, 'createTenant', substr(sha256('a'::bytea), 1, 16), sha256('b'::bytea), now(), now() `+ahead+`)`, i); err != nil {
			t.Fatal(err)
		}
	}

	if err := sessions.Purge(ctx); err != nil {
		t.Fatalf("session Purge: %v", err)
	}
	if err := accounts.Purge(ctx); err != nil {
		t.Fatalf("account Purge: %v", err)
	}
	if err := keys.Purge(ctx); err != nil {
		t.Fatalf("idem Purge: %v", err)
	}

	if n := d.Count(t, "SELECT count(*) FROM pandokos.user_session"); n != 1 {
		t.Errorf("%d sessions left, want only the live one", n)
	}
	if n := d.Count(t, "SELECT count(*) FROM pandokos.user_session WHERE id = $1::uuid", ids["live"]); n != 1 {
		t.Error("the live session was removed")
	}
	if n := d.Count(t, "SELECT count(*) FROM pandokos.signin_throttle"); n != 1 {
		t.Errorf("%d throttle rows left, want the fresh one only", n)
	}
	if n := d.Count(t, "SELECT count(*) FROM pandokos.oidc_login"); n != 1 {
		t.Errorf("%d sign-in transactions left, want the live one only", n)
	}
	if n := d.Count(t, "SELECT count(*) FROM pandokos.idempotency_key"); n != 1 {
		t.Errorf("%d idempotency keys left, want the live one only", n)
	}
}
