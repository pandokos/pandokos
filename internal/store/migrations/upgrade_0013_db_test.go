package migrations_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/dbtest"
)

// TestUpgrade0013 is T-775: migration 0013 on a database with the data of 0012.
func TestUpgrade0013(t *testing.T) {
	d := dbtest.New(t)
	d.MigrateTo(t, 12)
	super := d.Super(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := super.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	var tid string
	if err := super.QueryRow(ctx, `INSERT INTO pandokos.tenant (name) VALUES ('upgrade-people') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	var sa string
	if err := super.QueryRow(ctx, `INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, 'bot') RETURNING id::text`, tid).Scan(&sa); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role) VALUES ($1::uuid, $2::uuid, 'tenant-admin')`, tid, sa)
	exec(`INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash) VALUES ($1::uuid, $2::uuid, 'tabcdefghijklmnop', sha256('k'::bytea))`, tid, sa)
	exec(`INSERT INTO pandokos.platform_key (key_id, secret_hash, description) VALUES ('pabcdefghijklmnop', sha256('p'::bytea), 'ops')`)
	exec(`INSERT INTO pandokos.audit_event (tenant_id, actor_type, action, resource_type, outcome) VALUES ($1::uuid, 'anonymous', 'auth.failure', 'virtual_key', 'failure')`, tid)
	exec(`INSERT INTO pandokos.platform_audit_event (actor_type, action, resource_type, outcome) VALUES ('platform', 'tenant.create', 'tenant', 'success')`)
	exec(`INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience) VALUES ($1::uuid, 'idp', 'https://idp.example.test', 'aud')`, tid)

	snapshot := func(tbl string) string {
		t.Helper()
		var s string
		// Migration 0014 (sprint 4.3) adds the column audit_event.support_action_id and gives the role
		// owner to whoever held tenant-admin by hand: neither is a change by migration 0013.
		where := ""
		if tbl == "role_assignment" {
			where = " WHERE t.role <> 'owner'"
		}
		if err := super.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(t) - 'support_action_id' ORDER BY to_jsonb(t)::text)::text, '[]') FROM pandokos.`+tbl+` t`+where).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	tables := []string{"tenant", "service_account", "role_assignment", "virtual_key", "platform_key", "audit_event", "platform_audit_event", "trusted_issuer"}
	before := map[string]string{}
	for _, tbl := range tables {
		before[tbl] = snapshot(tbl)
	}

	d.MigrateTo(t, 13)

	for _, tbl := range tables {
		if got := snapshot(tbl); got != before[tbl] {
			t.Errorf("%s changed by the migration:\n%s\n%s", tbl, before[tbl], got)
		}
	}

	// The audit constraints accept the actor host in both logs, and still refuse an unknown actor.
	exec(`INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_name, action, resource_type, outcome) VALUES ($1::uuid, 'host', 'pandokos user unlock', 'user.unlock', 'user', 'success')`, tid)
	exec(`INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, outcome) VALUES ('host', 'pandokos init', 'user.create', 'user', 'success')`)
	if _, err := super.Exec(ctx, `INSERT INTO pandokos.audit_event (tenant_id, actor_type, action, resource_type, outcome) VALUES ($1::uuid, 'robot', 'a.b', 'user', 'success')`, tid); err == nil {
		t.Error("an unknown actor type was accepted")
	}

	// The admin role executes the two issuer functions; the gateway role still does; the worker does not.
	for _, c := range []struct {
		role, fn string
		want     bool
	}{
		{"admin", "pandokos.issuer_lookup(text, text[])", true},
		{"admin", "pandokos.platform_issuer_tenant_lookup(uuid, text)", true},
		{"gateway", "pandokos.issuer_lookup(text, text[])", true},
		{"worker", "pandokos.issuer_lookup(text, text[])", false},
	} {
		var got bool
		if err := super.QueryRow(ctx, `SELECT has_function_privilege($1, $2, 'EXECUTE')`, d.User(c.role), c.fn).Scan(&got); err != nil || got != c.want {
			t.Errorf("EXECUTE on %s for %s: %v %v, want %v", c.fn, c.role, got, err, c.want)
		}
	}

	// No new table is visible to the gateway or the worker role.
	for _, tbl := range []string{"app_user", "local_account", "user_identity", "user_session", "platform_role_assignment", "membership", "membership_role", "membership_directory", "signin_throttle", "oidc_login", "idempotency_key"} {
		for _, role := range []string{"gateway", "worker"} {
			var got bool
			if err := super.QueryRow(ctx, `SELECT has_table_privilege($1, $2, 'SELECT') OR has_table_privilege($1, $2, 'INSERT') OR has_table_privilege($1, $2, 'UPDATE') OR has_table_privilege($1, $2, 'DELETE')`, d.User(role), "pandokos."+tbl).Scan(&got); err != nil || got {
				t.Errorf("%s has a privilege on %s (%v)", role, tbl, err)
			}
		}
	}

	// A second run is refused, and the ledger holds the migration once.
	if _, err := d.Connect(t, "owner").Exec(ctx, migrationSQL(t, 13)); err == nil {
		t.Error("migration 0013 ran twice")
	}
	var n int
	if err := super.QueryRow(ctx, `SELECT count(*) FROM pandokos.schema_migration WHERE version = 13`).Scan(&n); err != nil || n != 1 {
		t.Errorf("the ledger holds migration 13 %d times (%v)", n, err)
	}
	if !strings.Contains(migrationSQL(t, 13), "0013_users_sessions") {
		t.Error("migration 13 is not 0013_users_sessions")
	}
}
