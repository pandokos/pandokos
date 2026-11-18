package migrations_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/test/dbtest"
)

// nineTables are the tables migration 0011 lifts forced row-level security on, and puts back.
var nineTables = []string{"limit_rule", "platform_limit_rule", "service_account", "virtual_key", "subject_mapping",
	"provider", "model", "audit_event", "platform_audit_event"}

// TestLimitIntegrityUpgrade is T-692: a database at 0010 with a rule of every row of the table of
// section 5.10, in two tenants, and ceilings, upgraded with the real migrator.
func TestLimitIntegrityUpgrade(t *testing.T) {
	d := dbtest.New(t)
	d.MigrateTo(t, 10)
	up := d.SeedRulesAt0010(t)
	super := d.Super(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	type stamp struct {
		created, updated time.Time
	}
	stamps := map[string]stamp{}
	for _, r := range up.Rules {
		tbl := "limit_rule"
		if r.Platform {
			tbl = "platform_limit_rule"
		}
		var s stamp
		if err := super.QueryRow(ctx, `SELECT created_at, updated_at FROM pandokos.`+tbl+` WHERE id = $1::uuid`, r.ID).Scan(&s.created, &s.updated); err != nil {
			t.Fatal(err)
		}
		stamps[r.ID] = s
	}
	time.Sleep(20 * time.Millisecond) // a migration that touched updated_at would show

	d.Migrate(t)

	t.Run("each surviving rule has its identifiers and its updated_at", func(t *testing.T) {
		for _, r := range up.Rules {
			if r.Cause != "" {
				continue
			}
			var cols map[string]*string
			var created, updated time.Time
			var err error
			if r.Platform {
				var prov, mod *string
				err = super.QueryRow(ctx, `SELECT provider_id::text, model_id::text, created_at, updated_at FROM pandokos.platform_limit_rule WHERE id = $1::uuid`, r.ID).
					Scan(&prov, &mod, &created, &updated)
				cols = map[string]*string{"provider_id": prov, "model_id": mod}
			} else {
				var sa, vk, sm, prov, mod *string
				err = super.QueryRow(ctx, `SELECT service_account_id::text, virtual_key_id::text, subject_mapping_id::text, provider_id::text, model_id::text,
						created_at, updated_at FROM pandokos.limit_rule WHERE id = $1::uuid`, r.ID).Scan(&sa, &vk, &sm, &prov, &mod, &created, &updated)
				cols = map[string]*string{"service_account_id": sa, "virtual_key_id": vk, "subject_mapping_id": sm, "provider_id": prov, "model_id": mod}
			}
			if err != nil {
				t.Errorf("%s: the rule is gone or unreadable: %v", r.Case, err)
				continue
			}
			for name, got := range cols {
				want, has := r.Kept[name]
				switch {
				case has && (got == nil || *got != want):
					t.Errorf("%s: %s is %v, want %s", r.Case, name, got, want)
				case !has && got != nil:
					t.Errorf("%s: %s is %s, want NULL", r.Case, name, *got)
				}
			}
			if !updated.Equal(stamps[r.ID].updated) || !created.Equal(stamps[r.ID].created) {
				t.Errorf("%s: created_at/updated_at are %v/%v, were %v/%v: the data migration touched the row", r.Case, created, updated, stamps[r.ID].created, stamps[r.ID].updated)
			}
		}
	})

	t.Run("each deleted rule has one audit row in the right log", func(t *testing.T) {
		for _, r := range up.Rules {
			if r.Cause == "" {
				continue
			}
			tbl, action, resource := "audit_event", "limit.delete", "limit_rule"
			if r.Platform {
				tbl, action, resource = "platform_audit_event", "platform_limit.delete", "platform_limit_rule"
			}
			var gone int
			table := "limit_rule"
			if r.Platform {
				table = "platform_limit_rule"
			}
			if err := super.QueryRow(ctx, `SELECT count(*) FROM pandokos.`+table+` WHERE id = $1::uuid`, r.ID).Scan(&gone); err != nil || gone != 0 {
				t.Errorf("%s: the rule is still there (%d, %v)", r.Case, gone, err)
			}
			rows, err := super.Query(ctx, `SELECT actor_type, actor_name, action, resource_type, outcome, detail FROM pandokos.`+tbl+` WHERE resource_id = $1`, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			type row struct {
				actorType, actorName, action, resource, outcome string
				detail                                          []byte
			}
			var got []row
			for rows.Next() {
				var x row
				if err := rows.Scan(&x.actorType, &x.actorName, &x.action, &x.resource, &x.outcome, &x.detail); err != nil {
					t.Fatal(err)
				}
				got = append(got, x)
			}
			rows.Close()
			if len(got) != 1 {
				t.Errorf("%s: %d audit rows in %s, want one", r.Case, len(got), tbl)
				continue
			}
			x := got[0]
			if x.actorType != "platform" || x.actorName != "migration 0011" || x.action != action || x.resource != resource || x.outcome != "success" {
				t.Errorf("%s: audit row %+v", r.Case, x)
			}
			var detail map[string]any
			if err := json.Unmarshal(x.detail, &detail); err != nil {
				t.Fatal(err)
			}
			if detail["cause"] != r.Cause {
				t.Errorf("%s: cause %v, want %s", r.Case, detail["cause"], r.Cause)
			}
			for k, want := range r.Fields {
				if !reflect.DeepEqual(detail[k], want) {
					t.Errorf("%s: detail %s is %#v, want %#v (detail %s)", r.Case, k, detail[k], want, x.detail)
				}
			}
			// The row is in this log only, and in the log of its own tenant.
			if !r.Platform {
				var tenantID string
				if err := super.QueryRow(ctx, `SELECT tenant_id::text FROM pandokos.audit_event WHERE resource_id = $1`, r.ID).Scan(&tenantID); err != nil || tenantID != r.Tenant.String() {
					t.Errorf("%s: logged for tenant %s (%v), want %s", r.Case, tenantID, err, r.Tenant)
				}
				var other int
				if err := super.QueryRow(ctx, `SELECT count(*) FROM pandokos.platform_audit_event WHERE resource_id = $1`, r.ID).Scan(&other); err != nil || other != 0 {
					t.Errorf("%s: also in the platform log", r.Case)
				}
			}
		}
		var total, deleted int
		for _, r := range up.Rules {
			total++
			if r.Cause != "" {
				deleted++
			}
		}
		var tenantRows, platformRows int
		if err := super.QueryRow(ctx, `SELECT count(*) FROM pandokos.audit_event WHERE action = 'limit.delete' AND actor_name = 'migration 0011'`).Scan(&tenantRows); err != nil {
			t.Fatal(err)
		}
		if err := super.QueryRow(ctx, `SELECT count(*) FROM pandokos.platform_audit_event WHERE action = 'platform_limit.delete' AND actor_name = 'migration 0011'`).Scan(&platformRows); err != nil {
			t.Fatal(err)
		}
		if tenantRows+platformRows != deleted {
			t.Errorf("%d audit rows by the migration for %d deleted rules of %d", tenantRows+platformRows, deleted, total)
		}
		var left int
		if err := super.QueryRow(ctx, `SELECT (SELECT count(*) FROM pandokos.limit_rule) + (SELECT count(*) FROM pandokos.platform_limit_rule)`).Scan(&left); err != nil || left != total-deleted {
			t.Errorf("%d rules left (%v), want %d", left, err, total-deleted)
		}
	})

	t.Run("forced row-level security and the triggers are back", func(t *testing.T) {
		for _, tbl := range nineTables {
			var enabled, forced bool
			if err := super.QueryRow(ctx, `SELECT relrowsecurity, relforcerowsecurity FROM pg_catalog.pg_class
				WHERE oid = ('pandokos.' || $1)::regclass`, tbl).Scan(&enabled, &forced); err != nil {
				t.Fatal(err)
			}
			if !enabled || !forced {
				t.Errorf("%s: row security enabled %v, forced %v", tbl, enabled, forced)
			}
		}
		for _, tbl := range []string{"limit_rule", "platform_limit_rule"} {
			rows, err := super.Query(ctx, `SELECT tgname, tgenabled::text FROM pg_catalog.pg_trigger WHERE tgrelid = ('pandokos.' || $1)::regclass AND NOT tgisinternal`, tbl)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for rows.Next() {
				var name, enabled string
				if err := rows.Scan(&name, &enabled); err != nil {
					t.Fatal(err)
				}
				n++
				if enabled != "O" {
					t.Errorf("%s: trigger %s is %q, want enabled (O)", tbl, name, enabled)
				}
			}
			rows.Close()
			if n < 2 {
				t.Errorf("%s has %d triggers", tbl, n)
			}
		}
		// A tenant session still sees only its rows: the policy is back in force for the owner too.
		var n int
		if err := pgx.BeginFunc(ctx, d.Connect(t, "owner"), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM pandokos.limit_rule`).Scan(&n)
		}); err != nil || n != 0 {
			t.Errorf("the owner without a session sees %d rules (%v): forced row-level security is not back", n, err)
		}
	})

	t.Run("the migration applied twice is refused by the ledger", func(t *testing.T) {
		res, err := store.Migrate(ctx, d.ConnOptions("owner"), embeddedMigrations(t), "test")
		if err != nil || len(res.Applied) != 0 {
			t.Errorf("a second Migrate applied %d migrations (%v), want none", len(res.Applied), err)
		}
		var n int
		if err := super.QueryRow(ctx, `SELECT count(*) FROM pandokos.schema_migration WHERE version = 11`).Scan(&n); err != nil || n != 1 {
			t.Errorf("%d ledger rows for version 11 (%v), want 1", n, err)
		}
		// The SQL of 0011 is not repeatable: running it again by hand fails, so it is the ledger that keeps it from running.
		if _, err := d.Connect(t, "owner").Exec(ctx, migrationSQL(t, 11)); err == nil {
			t.Error("the SQL of migration 0011 ran twice")
		}
	})

	t.Run("a database without rules migrates", func(t *testing.T) {
		e := dbtest.New(t)
		e.MigrateTo(t, 10)
		e.SeedTenant(t, "empty-upgrade")
		e.Migrate(t)
		var n int
		if err := e.Super(t).QueryRow(ctx, `SELECT count(*) FROM pandokos.audit_event WHERE actor_name = 'migration 0011'`).Scan(&n); err != nil || n != 0 {
			t.Errorf("%d audit rows by the migration without rules (%v)", n, err)
		}
	})
}
