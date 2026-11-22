package idem_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/idem"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

func ctx(t testing.TB) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return c
}

type rig struct {
	d   *dbtest.Database
	db  *store.DB
	s   *idem.Store
	tid tenant.ID
}

func newRig(t *testing.T) *rig {
	t.Helper()
	d := dbtest.NewMigrated(t)
	db := d.Open(t, store.RoleAdmin)
	return &rig{d: d, db: db, s: idem.NewStore(idem.Options{DB: db, Retention: 24 * time.Hour}), tid: d.SeedTenant(t, "acme")}
}

// commit plays the create operation of a claimed request: a transaction of the operation's scope
// that marks the claim, as the audit row of a create does.
func (r *rig) commit(t testing.TB, c context.Context, scope idem.Scope, typ, id string) error {
	t.Helper()
	run := func(c context.Context, tx store.Tx) error { return idem.Mark(c, tx, typ, id) }
	if scope.TenantID.IsZero() {
		return r.db.Platform(c, run)
	}
	return r.db.WithTenant(c, scope.TenantID, run)
}

func (r *rig) rows(t testing.TB) int {
	return r.d.Count(t, "SELECT count(*) FROM pandokos.idempotency_key")
}

// TestClaim is T-818.
func TestClaim(t *testing.T) {
	r := newRig(t)
	platform := idem.Scope{PrincipalKind: "platform", PrincipalID: "k1"}
	user := idem.Scope{TenantID: r.tid, PrincipalKind: "user", PrincipalID: "u1"}
	body := []byte(`{"name":"one"}`)
	response := []byte(`{"user":{"id":"0192f0f0-0000-7000-8000-000000000001"},"initial_password":"shown-once","secret":"also-once"}`)

	// A new key.
	c1, replay, err := r.s.Begin(ctx(t), user, "createMember", "/v1/tenants/x/members", "key-1", body)
	if err != nil || replay != nil {
		t.Fatalf("a new key: replay=%v err=%v", replay, err)
	}
	// The same key and request while the first holds the lock: 409.
	if _, replay, err = r.s.Begin(ctx(t), user, "createMember", "/v1/tenants/x/members", "key-1", body); !errors.Is(err, idem.ErrInProgress) || replay != nil {
		t.Errorf("the same request while locked: replay=%v err=%v, want ErrInProgress", replay, err)
	}
	// Another body or another operation or another path: 422.
	for name, args := range map[string][3]any{
		"another body":      {"createMember", "/v1/tenants/x/members", []byte(`{"name":"two"}`)},
		"another operation": {"createUser", "/v1/tenants/x/members", body},
		"another path":      {"createMember", "/v1/tenants/y/members", body},
	} {
		if _, _, err := r.s.Begin(ctx(t), user, args[0].(string), args[1].(string), "key-1", args[2].([]byte)); !errors.Is(err, idem.ErrKeyReused) {
			t.Errorf("%s: %v, want ErrKeyReused", name, err)
		}
	}
	// The create commits, the response is stored: a replay.
	if err := r.commit(t, c1, user, "user", "0192f0f0-0000-7000-8000-000000000001"); err != nil {
		t.Fatalf("Mark: %v", err)
	}
	if err := r.s.Finish(c1, 201, response); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	_, replay, err = r.s.Begin(ctx(t), user, "createMember", "/v1/tenants/x/members", "key-1", body)
	if err != nil || replay == nil || replay.Status != 201 {
		t.Fatalf("a replay: %+v %v", replay, err)
	}
	if strings.Contains(string(replay.Body), "shown-once") || strings.Contains(string(replay.Body), "also-once") || !strings.Contains(string(replay.Body), "0192f0f0-0000-7000-8000-000000000001") {
		t.Errorf("the replayed body %s: members shown once must be gone, the rest kept", replay.Body)
	}
	if _, _, err := r.s.Begin(ctx(t), user, "createMember", "/v1/tenants/x/members", "key-1", []byte(`{"name":"two"}`)); !errors.Is(err, idem.ErrKeyReused) {
		t.Errorf("another body after completion: %v", err)
	}
	// The stored row holds no secret and no body.
	var text string
	if err := r.d.Super(t).QueryRow(ctx(t), "SELECT string_agg(to_jsonb(i)::text, ' ') FROM pandokos.idempotency_key i").Scan(&text); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"shown-once", "also-once", `"name":"one"`} {
		if strings.Contains(text, v) {
			t.Errorf("the table holds %q", v)
		}
	}

	// Another principal, another tenant, the platform: independent.
	for name, sc := range map[string]idem.Scope{
		"another principal": {TenantID: r.tid, PrincipalKind: "user", PrincipalID: "u2"},
		"another kind":      {TenantID: r.tid, PrincipalKind: "service_account", PrincipalID: "u1"},
		"another tenant":    {TenantID: r.d.SeedTenant(t, "globex"), PrincipalKind: "user", PrincipalID: "u1"},
		"the platform":      {PrincipalKind: "user", PrincipalID: "u1"},
	} {
		if _, replay, err := r.s.Begin(ctx(t), sc, "createMember", "/v1/tenants/x/members", "key-1", body); err != nil || replay != nil {
			t.Errorf("%s: replay=%v err=%v, want an independent new claim", name, replay, err)
		}
	}

	// A stale lock is taken over; the attempt that lost it fails at the commit mark.
	old, _, err := r.s.Begin(ctx(t), platform, "createTenant", "/v1/tenants", "key-2", body)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.s.Begin(ctx(t), platform, "createTenant", "/v1/tenants", "key-2", body); !errors.Is(err, idem.ErrInProgress) {
		t.Fatalf("a live lock: %v", err)
	}
	r.d.Age(t, "idempotency_key", "key = 'key-2'", 2*time.Minute, "locked_until")
	taken, replay, err := r.s.Begin(ctx(t), platform, "createTenant", "/v1/tenants", "key-2", body)
	if err != nil || replay != nil {
		t.Fatalf("a stale lock: replay=%v err=%v, want a take-over", replay, err)
	}
	if n := r.d.Count(t, "SELECT attempt FROM pandokos.idempotency_key WHERE key = 'key-2'"); n != 2 {
		t.Errorf("attempt %d after the take-over, want 2", n)
	}
	if err := r.commit(t, old, platform, "tenant", "t-old"); err == nil {
		t.Error("the attempt that lost the lock committed its mark")
	}
	if err := r.commit(t, taken, platform, "tenant", "t-new"); err != nil {
		t.Errorf("the attempt that holds the lock: %v", err)
	}
	// A request with a commit mark and no stored response: the resource exists, the response is lost.
	r.d.Age(t, "idempotency_key", "key = 'key-2'", 2*time.Minute, "locked_until")
	if _, _, err := r.s.Begin(ctx(t), platform, "createTenant", "/v1/tenants", "key-2", body); !errors.Is(err, idem.ErrResponseLost) {
		t.Errorf("a committed request without a response: %v, want ErrResponseLost", err)
	}

	// Mark without a claim is not an error; a claim without Finish keeps no response.
	if err := r.db.Platform(ctx(t), func(c context.Context, tx store.Tx) error { return idem.Mark(c, tx, "tenant", "x") }); err != nil {
		t.Errorf("Mark without a claim: %v", err)
	}
	if r.rows(t) == 0 {
		t.Error("no row")
	}
}
