package isolation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pandokos/pandokos/test/dbtest"
)

// TestCrossTenantNegative is T-143: with tenant A's session or with none, no runtime role can
// read or change tenant B's rows, in every tenant-scoped table, directly and through the pooler.
func TestCrossTenantNegative(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		a, b := d.SeedTenant(t, "tenant-a"), d.SeedTenant(t, "tenant-b")
		super := d.Super(t)
		spec := DefaultSpec()
		for _, tbl := range tenantScopedTables(t, super, spec) { // from the catalogue
			fx, ok := spec.Fixtures[tbl]
			if !ok {
				t.Fatalf("table %s has no fixture", tbl)
			}
			keyA, keyB := insertRow(t, super, fx, a), insertRow(t, super, fx, b)
			before := snapshot(t, super, tbl, b)
			if before == "[]" {
				t.Fatalf("tenant B has no row in %s: the test would prove nothing", tbl)
			}
			for _, role := range runtimeRoles {
				conn := d.Connect(t, role)
				for _, sess := range sessions(a) {
					for _, st := range statements(tbl, fx, a, b, keyB, sess) {
						t.Run(tbl+"/"+role+"/"+sess.name+"/"+st.name, func(t *testing.T) {
							ensureRow(t, super, tbl, fx, a, &keyA) // A may have deleted its own row
							res := runCommitted(t, conn, sess, st)
							st.check(t, res, b)
							if after := snapshot(t, super, tbl, b); after != before {
								t.Fatalf("tenant B's rows changed:\nbefore %s\nafter  %s", before, after)
							}
						})
					}
				}
			}
		}
	})
}

// TestPositiveControls is T-144: the matrix above cannot pass because nothing works at all.
func TestPositiveControls(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a, b := d.SeedTenant(t, "tenant-a"), d.SeedTenant(t, "tenant-b")
	super := d.Super(t)
	fx := DefaultSpec().Fixtures["service_account"]
	keyA := insertRow(t, super, fx, a)
	insertRow(t, super, fx, b)
	sessA := sessions(a)[0]

	t.Run("admin", func(t *testing.T) {
		conn := d.Connect(t, "admin")
		err := inSession(t, conn, sessA, func(c context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(c, `SELECT tenant_id::text AS tenant_id FROM pandokos.service_account`)
			if err != nil {
				return err
			}
			got, err := pgx.CollectRows(rows, pgx.RowToMap)
			if err != nil {
				return err
			}
			if len(got) != 1 || got[0]["tenant_id"] != a.String() {
				t.Errorf("admin read %v, want exactly A's row", got)
			}
			var newKey string
			if err := tx.QueryRow(c, fx.Insert, a.String()).Scan(&newKey); err != nil {
				t.Errorf("admin insert for A: %v", err)
			}
			tag, err := tx.Exec(c, `UPDATE pandokos.service_account SET `+fx.Update)
			if err != nil || tag.RowsAffected() < 1 {
				t.Errorf("admin update: %v, %d rows", err, tag.RowsAffected())
			}
			tag, err = tx.Exec(c, `DELETE FROM pandokos.service_account WHERE id::text = $1`, keyA)
			if err != nil || tag.RowsAffected() != 1 {
				t.Errorf("admin delete: %v, %d rows", err, tag.RowsAffected())
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("gateway reads", func(t *testing.T) {
		conn := d.Connect(t, "gateway")
		err := inSession(t, conn, sessA, func(c context.Context, tx pgx.Tx) error {
			var n int
			if err := tx.QueryRow(c, `SELECT count(*) FROM pandokos.service_account`).Scan(&n); err != nil {
				return err
			}
			if n < 1 {
				t.Errorf("gateway sees %d rows of A", n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("worker has no grant", func(t *testing.T) {
		conn := d.Connect(t, "worker")
		err := inSession(t, conn, sessA, func(c context.Context, tx pgx.Tx) error {
			var n int
			return tx.QueryRow(c, `SELECT count(*) FROM pandokos.service_account`).Scan(&n)
		})
		if sqlstate(err) != insufficientPrivilege {
			t.Fatalf("worker read: %v, want SQLSTATE %s", err, insufficientPrivilege)
		}
	})
}

// TestRuntimeRolesCannotEscape is T-147.
func TestRuntimeRolesCannotEscape(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a := d.SeedTenant(t, "tenant-a")
	super := d.Super(t)
	insertRow(t, super, DefaultSpec().Fixtures["service_account"], a)
	attempts := []struct {
		name string
		sql  string
		then string // run after a successful sql, must fail
	}{
		{name: "set role to the owner", sql: `SET ROLE pandokos_owner`},
		{name: "row_security off", sql: `SET row_security = off`, then: `SELECT count(*) FROM pandokos.service_account`},
		{name: "disable row level security", sql: `ALTER TABLE pandokos.service_account DISABLE ROW LEVEL SECURITY`},
		{name: "no force row level security", sql: `ALTER TABLE pandokos.service_account NO FORCE ROW LEVEL SECURITY`},
		{name: "drop policy", sql: `DROP POLICY tenant_isolation ON pandokos.service_account`},
		{name: "create policy", sql: `CREATE POLICY zz ON pandokos.service_account USING (true)`},
		{name: "create table", sql: `CREATE TABLE pandokos.zz (i integer)`},
		{name: "change owner", sql: `ALTER TABLE pandokos.service_account OWNER TO CURRENT_USER`},
		{name: "grant", sql: `GRANT SELECT ON pandokos.service_account TO PUBLIC`},
		{name: "create function", sql: `CREATE FUNCTION pandokos.zz() RETURNS integer LANGUAGE sql RETURN 1`},
	}
	for _, role := range runtimeRoles {
		for _, at := range attempts {
			t.Run(role+"/"+at.name, func(t *testing.T) {
				conn := d.Connect(t, role)
				_, err := conn.Exec(ctx(t), at.sql)
				if at.then != "" {
					if err != nil {
						t.Fatalf("%s: %v", at.sql, err)
					}
					var n int
					err = conn.QueryRow(ctx(t), at.then).Scan(&n)
					_, _ = conn.Exec(ctx(t), `RESET row_security`)
				}
				switch {
				case err == nil && at.name == "grant":
					// A grant by a role that holds no grant option raises a warning, not an error;
					// what matters is that nothing was granted.
				case sqlstate(err) != insufficientPrivilege:
					t.Fatalf("error %v, want SQLSTATE %s", err, insufficientPrivilege)
				}
				var public bool
				if err := super.QueryRow(ctx(t), `SELECT pg_catalog.has_table_privilege('public', 'pandokos.service_account', 'SELECT')`).Scan(&public); err != nil {
					t.Fatal(err)
				}
				if public {
					t.Error("PUBLIC was granted SELECT on service_account")
				}
			})
		}
	}
	// The attempts changed nothing.
	var rls, forced bool
	if err := super.QueryRow(ctx(t), `SELECT relrowsecurity, relforcerowsecurity FROM pg_catalog.pg_class WHERE oid = 'pandokos.service_account'::regclass`).Scan(&rls, &forced); err != nil {
		t.Fatal(err)
	}
	if !rls || !forced {
		t.Errorf("row-level security: enabled %v, forced %v", rls, forced)
	}
}

// TestOwnerIsSubjectToPolicies is T-148: forced row-level security applies to the owner too.
func TestOwnerIsSubjectToPolicies(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a := d.SeedTenant(t, "tenant-a")
	super := d.Super(t)
	insertRow(t, super, DefaultSpec().Fixtures["service_account"], a)
	var existing int
	if err := super.QueryRow(ctx(t), `SELECT count(*) FROM pandokos.service_account`).Scan(&existing); err != nil || existing == 0 {
		t.Fatalf("no service account exists to be hidden: %d %v", existing, err)
	}
	conn := d.Connect(t, "owner")
	if _, err := conn.Exec(ctx(t), "SET ROLE pandokos_owner"); err != nil {
		t.Fatal(err)
	}
	count := func(s session, q string) int {
		t.Helper()
		var n int
		if err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
			return tx.QueryRow(c, q).Scan(&n)
		}); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	none, platform, sessA := session{name: "none"}, session{"platform", "app.scope", "platform"}, sessions(a)[0]
	if n := count(none, `SELECT count(*) FROM pandokos.service_account`); n != 0 {
		t.Errorf("the owner sees %d service accounts with no tenant: without FORCE it would see all", n)
	}
	if n := count(platform, `SELECT count(*) FROM pandokos.service_account`); n != 0 {
		t.Errorf("the owner sees %d service accounts in platform scope", n)
	}
	for _, s := range []session{none, platform, sessA} {
		if n := count(s, `SELECT count(*) FROM pandokos.tenant`); n != 0 {
			t.Errorf("the owner sees %d tenants in session %s: the tenant policies name only the runtime roles", n, s.name)
		}
	}
}

// TestMalformedTenantSetting is T-149.
func TestMalformedTenantSetting(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a := d.SeedTenant(t, "tenant-a")
	insertRow(t, d.Super(t), DefaultSpec().Fixtures["service_account"], a)
	conn := d.Connect(t, "admin")
	read := func(value string) (int, error) {
		var n int
		err := inSession(t, conn, session{"x", "app.tenant_id", value}, func(c context.Context, tx pgx.Tx) error {
			return tx.QueryRow(c, `SELECT count(*) FROM pandokos.service_account`).Scan(&n)
		})
		return n, err
	}
	if _, err := read("not-a-uuid"); sqlstate(err) != invalidText {
		t.Errorf("malformed setting: %v, want SQLSTATE %s", err, invalidText)
	}
	if n, err := read(""); err != nil || n != 0 {
		t.Errorf("empty setting: %d rows, error %v; want no rows and no error", n, err)
	}
	if n, err := read(a.String()); err != nil || n != 1 {
		t.Errorf("valid setting: %d rows, error %v; want the control row", n, err)
	}
}

// TestCompositeForeignKey is T-150.
func TestCompositeForeignKey(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a, b := d.SeedTenant(t, "tenant-a"), d.SeedTenant(t, "tenant-b")
	super := d.Super(t)
	fx := DefaultSpec().Fixtures["service_account"]
	saA, saB := insertRow(t, super, fx, a), insertRow(t, super, fx, b)
	owned(t, d, `
CREATE TABLE pandokos.zz_child (
    tenant_id uuid NOT NULL REFERENCES pandokos.tenant (id),
    id uuid NOT NULL DEFAULT pandokos.uuid_v7(),
    service_account_id uuid NOT NULL,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, service_account_id) REFERENCES pandokos.service_account (tenant_id, id)
);
ALTER TABLE pandokos.zz_child ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.zz_child FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pandokos.zz_child
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));
GRANT SELECT, INSERT ON pandokos.zz_child TO pandokos_admin;`)
	conn := d.Connect(t, "admin")
	insert := func(sa string) error {
		return inSession(t, conn, sessions(a)[0], func(c context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(c, `INSERT INTO pandokos.zz_child (tenant_id, service_account_id) VALUES ($1::uuid, $2::uuid)`, a.String(), sa)
			return err
		})
	}
	if err := insert(saA); err != nil {
		t.Fatalf("a child of A's own service account: %v", err)
	}
	var random string
	if err := super.QueryRow(ctx(t), `SELECT pandokos.uuid_v7()::text`).Scan(&random); err != nil {
		t.Fatal(err)
	}
	errB, errRandom := insert(saB), insert(random)
	var peB, peRandom *pgconn.PgError
	if sqlstate(errB) != foreignKey || sqlstate(errRandom) != foreignKey {
		t.Fatalf("errors: %v / %v, want SQLSTATE %s", errB, errRandom, foreignKey)
	}
	if !asPg(errB, &peB) || !asPg(errRandom, &peRandom) {
		t.Fatal("not database errors")
	}
	if peB.Code != peRandom.Code || peB.ConstraintName != peRandom.ConstraintName || peB.Message != peRandom.Message {
		t.Errorf("a service account of another tenant is distinguishable from one that does not exist:\n%+v\n%+v", peB, peRandom)
	}
}

func asPg(err error, target **pgconn.PgError) bool {
	pe, ok := err.(*pgconn.PgError) //nolint:errorlint // inSession returns the driver error itself
	if ok {
		*target = pe
	}
	return ok
}

// TestServiceAccountRules is T-151.
func TestServiceAccountRules(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a, b := d.SeedTenant(t, "tenant-a"), d.SeedTenant(t, "tenant-b")
	super := d.Super(t)
	insert := func(id, name string) error {
		_, err := super.Exec(ctx(t), `INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, $2)`, id, name)
		return err
	}
	if err := insert(a.String(), "same"); err != nil {
		t.Fatal(err)
	}
	if err := insert(b.String(), "same"); err != nil {
		t.Errorf("the same name in two tenants must be allowed: %v", err)
	}
	if err := insert(a.String(), "same"); sqlstate(err) != uniqueViolation {
		t.Errorf("the same name twice in one tenant: %v, want SQLSTATE %s", err, uniqueViolation)
	}
	for _, name := range []string{"A", "-a", "a-", "a_b", strings.Repeat("a", 64), ""} {
		if err := insert(a.String(), name); sqlstate(err) != checkViolation {
			t.Errorf("name %q: %v, want SQLSTATE %s", name, err, checkViolation)
		}
	}
	_, err := super.Exec(ctx(t), `INSERT INTO pandokos.service_account (tenant_id, name, valid_from, valid_until) VALUES ($1::uuid, 'dates', now(), now())`, a.String())
	if sqlstate(err) != checkViolation {
		t.Errorf("valid_from not before valid_until: %v, want SQLSTATE %s", err, checkViolation)
	}
	if _, err := super.Exec(ctx(t), `INSERT INTO pandokos.service_account (tenant_id, name, valid_from, valid_until) VALUES ($1::uuid, 'dates', now(), now() + interval '1 day')`, a.String()); err != nil {
		t.Errorf("a valid validity window: %v", err)
	}

	// id and tenant_id are immutable, even for a superuser (trigger).
	if _, err := super.Exec(ctx(t), `UPDATE pandokos.service_account SET id = pandokos.uuid_v7() WHERE name = 'same' AND tenant_id = $1::uuid`, a.String()); sqlstate(err) != checkViolation {
		t.Errorf("changing id: %v, want SQLSTATE %s", err, checkViolation)
	}
	if _, err := super.Exec(ctx(t), `UPDATE pandokos.service_account SET tenant_id = $2::uuid WHERE name = 'dates' AND tenant_id = $1::uuid`, a.String(), b.String()); sqlstate(err) != checkViolation {
		t.Errorf("changing tenant_id: %v, want SQLSTATE %s", err, checkViolation)
	}

	// updated_at moves, created_at does not.
	var created1, updated1, created2, updated2 time.Time
	read := func(c, u *time.Time) {
		t.Helper()
		if err := super.QueryRow(ctx(t), `SELECT created_at, updated_at FROM pandokos.service_account WHERE name = 'same' AND tenant_id = $1::uuid`, a.String()).Scan(c, u); err != nil {
			t.Fatal(err)
		}
	}
	read(&created1, &updated1)
	// The timestamps are the transaction's start time: let the clock move.
	if _, err := super.Exec(ctx(t), `SELECT pg_catalog.pg_sleep(0.02)`); err != nil {
		t.Fatal(err)
	}
	if _, err := super.Exec(ctx(t), `UPDATE pandokos.service_account SET description = 'changed' WHERE name = 'same' AND tenant_id = $1::uuid`, a.String()); err != nil {
		t.Fatal(err)
	}
	read(&created2, &updated2)
	if !created2.Equal(created1) {
		t.Errorf("created_at moved: %v -> %v", created1, created2)
	}
	if !updated2.After(updated1) {
		t.Errorf("updated_at did not move: %v -> %v", updated1, updated2)
	}
}
