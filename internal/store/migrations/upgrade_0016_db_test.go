package migrations_test

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/dbtest"
)

// TestUpgrade0016 is T-986: migration 0016 on a database at 0015 with two key-encryption key
// versions, data keys, credentials, audit rows and tenants in every state. The version registered
// last becomes active and the other retiring; no row of any table changes (the columns of 0015 are
// compared); the triggers of L-21 exist; row-level security is forced on the two new tables.
func TestUpgrade0016(t *testing.T) {
	d := dbtest.New(t)
	d.MigrateTo(t, 15)
	super := d.Super(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := super.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}

	// Two versions, the second registered later; the identifiers are plain strings (no key is here).
	exec(`INSERT INTO pandokos.kek_version (id, provider, created_at) VALUES ('local-1111111111111111', 'local', now() - interval '2 days')`)
	exec(`INSERT INTO pandokos.kek_version (id, provider, created_at) VALUES ('local-2222222222222222', 'local', now() - interval '1 day')`)

	// Tenants in every state the 0015 schema allows.
	var provisioningID string // stays in state provisioning
	if err := super.QueryRow(ctx, `INSERT INTO pandokos.tenant (name) VALUES ('up16-new') RETURNING id::text`).Scan(&provisioningID); err != nil {
		t.Fatal(err)
	}
	active := d.SeedTenant(t, "up16-active")
	suspended := d.SeedTenant(t, "up16-suspended")
	exec(`UPDATE pandokos.tenant SET state = 'suspended' WHERE id = $1::uuid`, suspended.String())
	deleting := d.SeedTenant(t, "up16-deleting")
	exec(`UPDATE pandokos.tenant SET state = 'deleting' WHERE id = $1::uuid`, deleting.String())
	deleted := d.SeedTenant(t, "up16-deleted")
	exec(`UPDATE pandokos.tenant SET state = 'deleting' WHERE id = $1::uuid`, deleted.String())
	exec(`UPDATE pandokos.tenant SET state = 'deleted' WHERE id = $1::uuid`, deleted.String())

	// Data keys under each version and credentials sealed with them (opaque bytes of a valid length).
	i := 0
	for _, c := range []struct{ tenant, version string }{
		{active.String(), "local-2222222222222222"},
		{suspended.String(), "local-1111111111111111"},
		{deleting.String(), "local-1111111111111111"},
	} {
		i++
		exec(`INSERT INTO pandokos.tenant_data_key (tenant_id, kek_version, wrapped_key) VALUES ($1::uuid, $2, decode(repeat($3, 61), 'hex'))`,
			c.tenant, c.version, fmt.Sprintf("%02x", i))
		exec(`INSERT INTO pandokos.provider_credential (tenant_id, provider, label, key_version, secret, fingerprint)
			VALUES ($1::uuid, 'openai', $2, 1, decode(repeat($3, 61), 'hex'), md5($2))`, c.tenant, fmt.Sprintf("c-%d", i), fmt.Sprintf("%02x", 0x80+i))
	}

	// Audit rows: of two tenants and the platform, and a host row.
	for _, tid := range []string{active.String(), deleted.String()} {
		for j := 0; j < 3; j++ {
			exec(`INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_name, action, resource_type, resource_id, outcome, request_id, detail)
				VALUES ($1::uuid, 'platform', 'p', 'member.create', 'member', $2, 'success', md5($2), '{"n": 1.50}'::jsonb)`, tid, fmt.Sprintf("u16-%s-%d", tid[:8], j))
		}
	}
	for j := 0; j < 3; j++ {
		exec(`INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, resource_id, outcome, detail)
			VALUES ('platform', 'p', 'tenant.create', 'tenant', $1, 'success', '{"x": 1}')`, fmt.Sprintf("u16-p-%d", j))
	}

	// Every row of every table, over the columns of 0015, before the migration.
	type table struct {
		name string
		cols []string
	}
	var tables []table
	rows, err := super.Query(ctx, `SELECT c.relname, array_agg(a.attname::text ORDER BY a.attnum)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		WHERE n.nspname = 'pandokos' AND c.relkind IN ('r', 'p') AND NOT c.relispartition GROUP BY c.relname ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var tb table
		if err := rows.Scan(&tb.name, &tb.cols); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, tb)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	snapshot := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		for _, tb := range tables {
			var parts []string
			for _, c := range tb.cols {
				parts = append(parts, fmt.Sprintf("'%s', x.%q", c, c))
			}
			var s string
			q := fmt.Sprintf(`SELECT COALESCE(string_agg(j, E'\n' ORDER BY j), '') FROM (SELECT jsonb_build_object(%s)::text AS j FROM pandokos.%q x) y`, strings.Join(parts, ", "), tb.name)
			if err := super.QueryRow(ctx, q).Scan(&s); err != nil {
				t.Fatalf("snapshot of %s: %v", tb.name, err)
			}
			out[tb.name] = s
		}
		return out
	}
	before := snapshot()
	for _, name := range []string{"kek_version", "tenant", "tenant_data_key", "provider_credential", "audit_event", "platform_audit_event"} {
		if before[name] == "" {
			t.Fatalf("the test needs rows in %s", name)
		}
	}

	d.Migrate(t)

	t.Run("no row of the earlier tables changes", func(t *testing.T) {
		after := snapshot()
		var changed []string
		for name, was := range before {
			if name == "schema_migration" { // the migrator's own record gains the row of 0016
				continue
			}
			if after[name] != was {
				changed = append(changed, name)
			}
		}
		sort.Strings(changed)
		if len(changed) > 0 {
			t.Errorf("rows of %v changed by the migration or by the activation of the test key", changed)
		}
	})

	t.Run("the last version is active and the other retiring", func(t *testing.T) {
		got := map[string][3]string{}
		r, err := super.Query(ctx, `SELECT id, state, (activated_at = created_at)::text, COALESCE(retired_at::text, '') FROM pandokos.kek_version`)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		var ids []string
		for r.Next() {
			var id, state, same, retired string
			if err := r.Scan(&id, &state, &same, &retired); err != nil {
				t.Fatal(err)
			}
			if retired != "" || same != "true" {
				t.Errorf("%s: retired_at %q, activated_at equals created_at %s", id, retired, same)
			}
			got[id] = [3]string{state}
			ids = append(ids, id)
		}
		// Migrate activates the test key's version afterwards when none is active: none may be missing.
		if got["local-2222222222222222"][0] != "active" || got["local-1111111111111111"][0] != "retiring" {
			t.Errorf("states %v, want the version registered last active and the other retiring", got)
		}
		if len(ids) != 2 {
			t.Errorf("versions %v: the migration or the harness registered one", ids)
		}
		var active int
		if err := super.QueryRow(ctx, `SELECT count(*) FROM pandokos.kek_version WHERE state = 'active'`).Scan(&active); err != nil || active != 1 {
			t.Errorf("%d active versions (%v), want 1", active, err)
		}
	})

	t.Run("the triggers of L-21 exist", func(t *testing.T) {
		names := regexp.MustCompile(`CREATE TRIGGER (\w+_tenant_open) `).FindAllStringSubmatch(migrationSQL(t, 16), -1)
		if len(names) < 25 {
			t.Fatalf("%d tenant_open triggers in the migration", len(names))
		}
		for _, m := range names {
			var state string
			if err := super.QueryRow(ctx, `SELECT tgenabled::text FROM pg_trigger WHERE tgname = $1 AND NOT tgisinternal`, m[1]).Scan(&state); err != nil || state != "O" {
				t.Errorf("trigger %s: state %q (%v), want O", m[1], state, err)
			}
		}
	})

	t.Run("row-level security is forced on the two new tables", func(t *testing.T) {
		for _, tbl := range []string{"tenant_closed", "tenant_deletion"} {
			var forced, enabled bool
			if err := super.QueryRow(ctx, `SELECT relforcerowsecurity, relrowsecurity FROM pg_class WHERE oid = ('pandokos.' || $1)::regclass`, tbl).Scan(&forced, &enabled); err != nil || !forced || !enabled {
				t.Errorf("%s: forced %v enabled %v (%v), want both", tbl, forced, enabled, err)
			}
		}
	})
}
