package isolation

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

// errRollBack ends a transaction of inSession without committing what it did.
var errRollBack = errors.New("roll back")

// constraintOf returns the constraint name of a database error, or "".
func constraintOf(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.ConstraintName
	}
	return ""
}

// tenantOpenGlobals are the global tables that name a tenant in a row written in platform scope:
// the guard on them is a row trigger (D-5).
var tenantOpenGlobals = map[string]bool{
	"app_user": true, "model_tenant_grant": true, "platform_issuer_tenant": true, "platform_limit_rule": true, "support_action": true,
}

// insertableTables lists, from the catalogue, the tables (partitions excluded) on which the role has
// an INSERT privilege on the table or on one of its columns, and says which have a tenant column.
func insertableTables(t *testing.T, d *dbtest.Database, role string) (withTenant, without []string) {
	t.Helper()
	rows, err := d.Super(t).Query(ctx(t), `SELECT c.relname,
			EXISTS (SELECT 1 FROM pg_catalog.pg_attribute a WHERE a.attrelid = c.oid AND a.attname = 'tenant_id' AND NOT a.attisdropped AND a.attnum > 0)
		FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'pandokos' AND c.relkind IN ('r', 'p') AND NOT c.relispartition
		  AND pg_catalog.has_any_column_privilege($1, c.oid, 'INSERT') ORDER BY 1`, "pandokos_"+role)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var has bool
		if err := rows.Scan(&name, &has); err != nil {
			t.Fatal(err)
		}
		if has {
			withTenant = append(withTenant, name)
		} else {
			without = append(without, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return withTenant, without
}

// attemptInsert runs one INSERT in a transaction of the session that is always rolled back, with the
// settings given, and returns the error of the INSERT. A statement that succeeds returns nil.
func attemptInsert(t *testing.T, conn *pgx.Conn, s session, settings map[string]string, sql string, args ...any) error {
	t.Helper()
	var got error
	err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
		for k, v := range settings {
			if _, err := tx.Exec(c, `SELECT pg_catalog.set_config($1, $2, true)`, k, v); err != nil {
				t.Fatalf("set %s: %v", k, err)
			}
		}
		_, got = tx.Exec(c, sql, args...)
		return errRollBack
	})
	if !errors.Is(err, errRollBack) {
		t.Fatalf("the transaction ended with %v", err)
	}
	return got
}

// TestTenantOpenGuard is T-977: a tenant that is being deleted or is deleted takes no new row, in
// any table that takes a tenant's rows, from any runtime role, whatever the session says about the
// erasure; a tenant that is only suspended with a scheduled deletion, and another tenant, do.
func TestTenantOpenGuard(t *testing.T) {
	d := dbtest.NewMigrated(t)
	deleting, deleted := closedTenants(t, d)
	scheduled, other := d.PopulatedTenant(t), d.PopulatedTenant(t)
	d.RequestDeletion(t, scheduled, time.Now().Add(time.Hour))
	if st := dbText(t, d, `SELECT state FROM pandokos.tenant WHERE id = $1::uuid`, scheduled.String()); st != "suspended" {
		t.Fatalf("the tenant with a scheduled deletion is %s", st)
	}
	if st := dbText(t, d, `SELECT state FROM pandokos.tenant WHERE id = $1::uuid`, deleting.String()); st != "deleting" {
		t.Fatalf("the tenant being deleted is %s", st)
	}
	if st := dbText(t, d, `SELECT state FROM pandokos.tenant WHERE id = $1::uuid`, deleted.String()); st != "deleted" {
		t.Fatalf("the deleted tenant is %s", st)
	}
	platform := session{"platform", "app.scope", "platform"}

	// A statement for each table of the catalogue: for a table guarded by a statement trigger,
	// DEFAULT VALUES in the tenant's session (the trigger fires before any row is looked at); for the
	// five global tables guarded by a row trigger, a row that names the tenant, in platform scope.
	// tenant_deletion has a tenant column and its own guard (T-978).
	attempt := func(conn *pgx.Conn, tbl string, id tenant.ID, settings map[string]string) error {
		if tenantOpenGlobals[tbl] {
			return attemptInsert(t, conn, platform, settings, `INSERT INTO `+table(tbl)+` (tenant_id) VALUES ($1::uuid)`, id.String())
		}
		return attemptInsert(t, conn, tenantSession(id), settings, `INSERT INTO `+table(tbl)+` DEFAULT VALUES`)
	}
	tested := map[string]bool{}
	for _, role := range runtimeRoles {
		conn := d.Connect(t, role)
		scoped, _ := insertableTables(t, d, role)
		for _, tbl := range scoped {
			if tbl == "tenant_deletion" {
				continue
			}
			tested[tbl] = true
			for _, closed := range []tenant.ID{deleting, deleted} {
				for _, settings := range []map[string]string{nil, {"pandokos.erasure": "on"}, {"pandokos.erasure": "on", "pandokos.retention": "on"}} {
					err := attempt(conn, tbl, closed, settings)
					if sqlstate(err) != restrictViolation || constraintOf(err) != "tenant_open" {
						t.Errorf("%s inserts into %s for a closed tenant (settings %v): %v, want SQLSTATE %s tenant_open", role, tbl, settings, err, restrictViolation)
					}
				}
			}
			for _, open := range []tenant.ID{scheduled, other} {
				if err := attempt(conn, tbl, open, nil); constraintOf(err) == "tenant_open" {
					t.Errorf("%s is refused by tenant_open in %s for an open tenant: %v", role, tbl, err)
				}
			}
		}
	}
	var names []string
	for n := range tested {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, must := range []string{"service_account", "virtual_key", "audit_event", "usage_record", "limit_event", "idempotency_key", "tenant_data_key",
		"provider_credential", "app_user", "model_tenant_grant", "platform_issuer_tenant", "platform_limit_rule", "support_action"} {
		if !tested[must] {
			t.Errorf("the catalogue shows no runtime INSERT grant on %s; tested: %v", must, names)
		}
	}

	// A real insert, not only the guard: the other tenant takes a row.
	admin := d.Connect(t, "admin")
	for _, open := range []tenant.ID{scheduled, other} {
		if err := attemptInsert(t, admin, tenantSession(open), nil, `INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, 'fresh')`, open.String()); err != nil {
			t.Errorf("the admin role cannot insert a service account for an open tenant: %v", err)
		}
	}
	if err := attemptInsert(t, admin, tenantSession(deleting), nil, `INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, 'late')`, deleting.String()); constraintOf(err) != "tenant_open" {
		t.Errorf("a service account for the tenant being deleted: %v", err)
	}

	// The gateway's own definer functions run as the owner without the setting: refused like anybody.
	gw := d.Connect(t, "gateway")
	for _, closed := range []tenant.ID{deleting, deleted} {
		err := attemptInsert(t, gw, tenantSession(closed), nil, `SELECT pandokos.record_auth_failure('tabcdefghijklmnop', 'bad_secret', '0123456789abcdef0123456789abcdef')`)
		if constraintOf(err) != "tenant_open" {
			t.Errorf("record_auth_failure for a closed tenant: %v, want tenant_open", err)
		}
		err = attemptInsert(t, gw, tenantSession(closed), nil, `SELECT pandokos.record_unlimited_traffic(current_date, 'UTC', 'openai', 'gpt-4o', 'fedcba9876543210fedcba9876543210')`)
		if constraintOf(err) != "tenant_open" {
			t.Errorf("record_unlimited_traffic for a closed tenant: %v, want tenant_open", err)
		}
	}
	for _, open := range []tenant.ID{scheduled, other} {
		if err := attemptInsert(t, gw, tenantSession(open), nil, `SELECT pandokos.record_auth_failure('tabcdefghijklmnop', 'bad_secret', '0123456789abcdef0123456789abcdef')`); err != nil {
			t.Errorf("record_auth_failure for an open tenant: %v", err)
		}
	}

	// tenant_closed: a session sees its own tenant's row, the platform scope all, nobody writes it.
	for _, role := range runtimeRoles {
		conn := d.Connect(t, role)
		for _, c := range []struct {
			sess session
			want int
		}{
			{tenantSession(deleting), 1}, {tenantSession(deleted), 1}, {tenantSession(other), 0}, {tenantSession(scheduled), 0},
			{platform, 2}, {session{name: "none"}, 0},
		} {
			var n int
			err := inSession(t, conn, c.sess, func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT count(*) FROM pandokos.tenant_closed`).Scan(&n)
			})
			if err != nil || n != c.want {
				t.Errorf("%s in session %s sees %d rows of tenant_closed (%v), want %d", role, c.sess.name, n, err, c.want)
			}
		}
		for _, sql := range []string{
			`INSERT INTO pandokos.tenant_closed (tenant_id) VALUES ('` + other.String() + `')`,
			`UPDATE pandokos.tenant_closed SET closed_at = now()`,
			`DELETE FROM pandokos.tenant_closed`,
			`TRUNCATE pandokos.tenant_closed`,
		} {
			if err := attemptInsert(t, conn, platform, nil, sql); sqlstate(err) != insufficientPrivilege {
				t.Errorf("%s: %s gave %v, want SQLSTATE %s", role, sql, err, insufficientPrivilege)
			}
		}
	}
	if n := dbNumber(t, d, `SELECT count(*) FROM pandokos.tenant_closed`); n != 2 {
		t.Errorf("%d rows in tenant_closed, want 2", n)
	}
}

// TestTenantOpenLintRule is the second half of T-977: the lint rule L-21 reports a table that takes
// a tenant's rows and has no tenant_open trigger, and one whose trigger is of the wrong kind.
func TestTenantOpenLintRule(t *testing.T) {
	for _, c := range []struct {
		name string
		ddl  string
	}{
		{"statement trigger dropped from a tenant table", `DROP TRIGGER service_account_tenant_open ON pandokos.service_account`},
		{"statement trigger dropped from idempotency_key", `DROP TRIGGER idempotency_key_tenant_open ON pandokos.idempotency_key`},
		{"row trigger dropped from a global table", `DROP TRIGGER model_tenant_grant_tenant_open ON pandokos.model_tenant_grant`},
		{"a trigger that does not guard", `DROP TRIGGER virtual_key_tenant_open ON pandokos.virtual_key;
			CREATE TRIGGER virtual_key_tenant_open BEFORE INSERT ON pandokos.virtual_key FOR EACH STATEMENT EXECUTE FUNCTION pandokos.row_guard()`},
		{"a row trigger where a statement trigger belongs", `DROP TRIGGER limit_rule_tenant_open ON pandokos.limit_rule;
			CREATE TRIGGER limit_rule_tenant_open BEFORE INSERT ON pandokos.limit_rule FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_open()`},
		{"a disabled trigger", `ALTER TABLE pandokos.audit_event DISABLE TRIGGER audit_event_tenant_open`},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := dbtest.NewMigrated(t)
			owned(t, d, c.ddl)
			findings, err := Lint(ctx(t), d.Super(t), DefaultSpec())
			if err != nil {
				t.Fatal(err)
			}
			var fired bool
			for _, f := range findings {
				fired = fired || f.Rule == "L-21"
			}
			if !fired {
				t.Errorf("L-21 did not fire; findings: %+v", findings)
			}
		})
	}
}

// definerCall is one valid call of a SECURITY DEFINER function that a runtime role may execute, the
// scope it is made in (a closed tenant's session, or the platform scope), and whether it must be
// refused by tenant_open in a closed tenant's session: the functions that write a row of the tenant.
type definerCall struct {
	sql      string
	platform bool
	refused  bool
}

// definerCalls holds one call for every definer function that the catalogue lets a runtime role
// execute; the test fails for a function that has none. $1 is the deleting tenant, $2 the deleted
// one, $3 the active key-encryption key version and $4 its provider: every call takes the four.
var definerCalls = map[string]definerCall{
	"record_auth_failure":       {sql: `SELECT pandokos.record_auth_failure('tabcdefghijklmnop', 'bad_secret', '0123456789abcdef0123456789abcdef'), $1::uuid, $2::uuid, $3, $4`, refused: true},
	"record_token_auth_failure": {sql: `SELECT pandokos.record_token_auth_failure('tenant', '0192f0f0-5555-7000-8000-000000000001'::uuid, 'bad_token', 'subject', '0123456789abcdef0123456789abcdef'), $1::uuid, $2::uuid, $3, $4`, refused: true},
	"record_unlimited_traffic":  {sql: `SELECT pandokos.record_unlimited_traffic(current_date, 'UTC', 'openai', 'gpt-4o', 'fedcba9876543210fedcba9876543210'), $1::uuid, $2::uuid, $3, $4`, refused: true},
	// With an audit retention the function makes sure the tenant's chain exists: an insert.
	"tenant_retention":              {sql: `SELECT r.*, $1::uuid, $2::uuid, $3, $4 FROM pandokos.tenant_retention(120, 1200) r`, refused: true},
	"deleted_tenant_retention":      {sql: `SELECT r.*, $1::uuid, $2::uuid, $3, $4 FROM pandokos.deleted_tenant_retention(120, 1200) r`},
	"tenant_erasure_step":           {sql: `SELECT pandokos.tenant_erasure_step(100), $1::uuid, $2::uuid, $3, $4`},
	"virtual_key_lookup":            {sql: `SELECT pandokos.virtual_key_lookup('tabcdefghijklmnop'), $1::uuid, $2::uuid, $3, $4`},
	"issuer_lookup":                 {sql: `SELECT r.*, $1::uuid, $2::uuid, $3, $4 FROM pandokos.issuer_lookup('https://idp.example.test', ARRAY['pandokos']) r`},
	"platform_issuer_tenant_lookup": {sql: `SELECT pandokos.platform_issuer_tenant_lookup('0192f0f0-5555-7000-8000-000000000002'::uuid, 'claim'), $1::uuid, $2::uuid, $3, $4`},
	"user_tenants":                  {sql: `SELECT r.*, $1::uuid, $2::uuid, $3, $4 FROM pandokos.user_tenants('0192f0f0-5555-7000-8000-000000000003'::uuid) r`, platform: true},
	"platform_user_lookup":          {sql: `SELECT pandokos.platform_user_lookup('0192f0f0-5555-7000-8000-000000000003'::uuid, 'someone'), $1::uuid, $2::uuid, $3, $4`, platform: true},
	"platform_issuer_mapping_count": {sql: `SELECT pandokos.platform_issuer_mapping_count('0192f0f0-5555-7000-8000-000000000002'::uuid, ARRAY[$1::uuid, $2::uuid]), $3, $4`, platform: true},
	"delete_platform_issuer_limit_rules": {sql: `SELECT pandokos.delete_platform_issuer_limit_rules('0192f0f0-5555-7000-8000-000000000002'::uuid, ARRAY[$1::uuid, $2::uuid],
		'pkeytest', 'test', 'pkeytest', '0123456789abcdef0123456789abcdef'), $3, $4`, platform: true},
	"ensure_usage_partitions": {sql: `SELECT r.*, $1::uuid, $2::uuid, $3, $4 FROM pandokos.ensure_usage_partitions(1) r`, platform: true},
	"platform_retention":      {sql: `SELECT r.*, $1::uuid, $2::uuid, $3, $4 FROM pandokos.platform_retention(120, 1200) r`, platform: true},
	"kek_activate":            {sql: `SELECT pandokos.kek_activate($3, $4), $1::uuid, $2::uuid`, platform: true},
	"kek_retire":              {sql: `SELECT pandokos.kek_retire(), $1::uuid, $2::uuid, $3, $4`, platform: true},
}

// TestTenantOpenDefinerFunctions is the part of T-977 about the definer functions: a runtime role
// that sets pandokos.erasure, pandokos.retention or both and then calls a definer function, which
// runs as the owner, writes no row for a closed tenant. Each function the catalogue lets a runtime
// role execute is called by every runtime role that may, under each setting, in the session of a
// tenant that is being deleted and of one that is deleted (or in platform scope for the functions
// of the platform); the functions that write a row of the tenant are refused by tenant_open, and no
// row of either tenant changes. tenant_erasure_step is called for the deleted tenant only: for the
// one being deleted it is the deletion itself.
func TestTenantOpenDefinerFunctions(t *testing.T) {
	d := dbtest.NewMigrated(t)
	deleting, deleted := closedTenants(t, d)
	kek := dbText(t, d, `SELECT id FROM pandokos.kek_version WHERE state = 'active'`)
	provider := dbText(t, d, `SELECT provider FROM pandokos.kek_version WHERE state = 'active'`)
	tenantTables := map[string]bool{}
	{
		rows, err := d.Super(t).Query(ctx(t), `SELECT c.relname FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'pandokos' AND c.relkind IN ('r', 'p') AND NOT c.relispartition
			  AND EXISTS (SELECT 1 FROM pg_catalog.pg_attribute a WHERE a.attrelid = c.oid AND a.attname = 'tenant_id' AND NOT a.attisdropped AND a.attnum > 0)`)
		if err != nil {
			t.Fatal(err)
		}
		names, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			tenantTables[n] = true
		}
	}
	digests := func() map[string]string {
		out := map[string]string{}
		for _, id := range []tenant.ID{deleting, deleted} {
			for tbl, dg := range d.TableDigests(t, id) {
				if tenantTables[tbl] {
					out[id.String()+" "+tbl] = dg
				}
			}
		}
		return out
	}
	before := digests()

	settings := []map[string]string{{"pandokos.erasure": "on"}, {"pandokos.retention": "on"}, {"pandokos.erasure": "on", "pandokos.retention": "on"}}
	called := map[string]bool{}
	for _, role := range runtimeRoles {
		rows, err := d.Super(t).Query(ctx(t), `SELECT p.proname::text FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname = 'pandokos' AND p.prosecdef AND pg_catalog.has_function_privilege($1, p.oid, 'EXECUTE') ORDER BY 1`, "pandokos_"+role)
		if err != nil {
			t.Fatal(err)
		}
		fns, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		conn := d.Connect(t, role)
		for _, fn := range fns {
			call, ok := definerCalls[fn]
			if !ok {
				t.Errorf("%s may execute the definer function %s, which has no call in definerCalls", role, fn)
				continue
			}
			called[fn] = true
			for _, closed := range []tenant.ID{deleting, deleted} {
				if fn == "tenant_erasure_step" && closed == deleting {
					continue
				}
				s := tenantSession(closed)
				if call.platform {
					s = platformSession
				}
				for _, set := range settings {
					var got error
					err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
						for k, v := range set {
							if _, err := tx.Exec(c, `SELECT pg_catalog.set_config($1, $2, true)`, k, v); err != nil {
								t.Fatalf("set %s: %v", k, err)
							}
						}
						_, got = tx.Exec(c, call.sql, deleting.String(), deleted.String(), kek, provider)
						return got // a call that succeeds is committed: its rows would show below
					})
					if err != nil && !errors.Is(err, got) {
						t.Fatalf("the transaction ended with %v", err)
					}
					if call.refused && (sqlstate(got) != restrictViolation || constraintOf(got) != "tenant_open") {
						t.Errorf("%s calls %s for a closed tenant under %v: %v, want SQLSTATE %s tenant_open", role, fn, set, got, restrictViolation)
					}
				}
			}
		}
	}
	for fn := range definerCalls {
		if !called[fn] {
			t.Errorf("definerCalls has a call of %s, which no runtime role may execute", fn)
		}
	}
	after := digests()
	for k, was := range before {
		if after[k] != was {
			t.Errorf("a definer function called under a setting changed the rows of %s", k)
		}
	}
}
