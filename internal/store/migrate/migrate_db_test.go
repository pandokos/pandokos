package migrate_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/store/migrate"
	"github.com/pandokos/pandokos/internal/store/migrations"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

func realSet(t *testing.T) []migrate.Migration {
	t.Helper()
	ms, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

// withExtra loads the embedded migrations from disk plus one synthetic file per extra SQL text.
func withExtra(t *testing.T, extra ...string) []migrate.Migration {
	t.Helper()
	dir := filepath.Join(testutil.RepoRoot(t), "internal", "store", "migrations", "sql")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{}
	n := 0
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		fsys["sql/"+e.Name()] = &fstest.MapFile{Data: b}
		n++
	}
	for i, sql := range extra {
		fsys[fmt.Sprintf("sql/%04d_extra.sql", n+i+1)] = &fstest.MapFile{Data: []byte(sql)}
	}
	ms, err := migrate.Load(fsys, "sql")
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func count(t *testing.T, conn *pgx.Conn, query string, args ...any) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(ctx(t), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func exists(t *testing.T, conn *pgx.Conn, rel string) bool {
	t.Helper()
	var ok bool
	if err := conn.QueryRow(ctx(t), "SELECT pg_catalog.to_regclass($1) IS NOT NULL", rel).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

// TestApplyFromEmpty is T-120.
func TestApplyFromEmpty(t *testing.T) {
	for _, mode := range []string{"direct", "pooler"} {
		t.Run(mode, func(t *testing.T) {
			d := dbtest.New(t)
			if mode == "pooler" {
				d = d.Pooled(t)
			}
			ms := realSet(t)
			res, err := migrate.Apply(ctx(t), d.Connect(t, "owner"), ms, "v-test")
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Applied) != len(ms) || res.Version != len(ms) {
				t.Fatalf("result %+v", res)
			}
			super := d.Super(t)
			rows, err := super.Query(ctx(t), "SELECT version, name, checksum, pandokos_version, applied_by FROM pandokos.schema_migration ORDER BY version")
			if err != nil {
				t.Fatal(err)
			}
			i := 0
			for rows.Next() {
				var v int
				var name, sum, pv, by string
				if err := rows.Scan(&v, &name, &sum, &pv, &by); err != nil {
					t.Fatal(err)
				}
				if v != ms[i].Version || name != ms[i].Name || sum != ms[i].Checksum || pv != "v-test" || by != d.User("owner") {
					t.Errorf("ledger row %d: %d %s %s %s %s", i, v, name, sum, pv, by)
				}
				i++
			}
			rows.Close()
			if i != len(ms) {
				t.Fatalf("ledger has %d rows, want %d", i, len(ms))
			}
			for _, q := range []string{
				`SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'pandokos' AND pg_catalog.pg_get_userbyid(c.relowner) <> 'pandokos_owner'`,
				`SELECT count(*) FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'pandokos' AND pg_catalog.pg_get_userbyid(p.proowner) <> 'pandokos_owner'`,
			} {
				if n := count(t, super, q); n != 0 {
					t.Errorf("%d objects are not owned by pandokos_owner", n)
				}
			}
		})
	}
}

// TestApplyIsIdempotent is T-121.
func TestApplyIsIdempotent(t *testing.T) {
	d := dbtest.New(t)
	conn := d.Connect(t, "owner")
	ms := realSet(t)
	first, err := migrate.Apply(ctx(t), conn, ms, "v")
	if err != nil {
		t.Fatal(err)
	}
	second, err := migrate.Apply(ctx(t), conn, ms, "v")
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Applied) != 0 || second.Version != first.Version {
		t.Fatalf("second result %+v, first %+v", second, first)
	}
}

// TestApplyIncrementally is T-122.
func TestApplyIncrementally(t *testing.T) {
	d := dbtest.New(t)
	conn := d.Connect(t, "owner")
	if _, err := migrate.Apply(ctx(t), conn, realSet(t), "v"); err != nil {
		t.Fatal(err)
	}
	ms := withExtra(t, "CREATE TABLE pandokos.zz_one (i integer)")
	res, err := migrate.Apply(ctx(t), conn, ms, "v")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 || res.Applied[0].Name != "extra" || res.Version != len(ms) {
		t.Fatalf("result %+v", res)
	}
	if !exists(t, d.Super(t), "pandokos.zz_one") {
		t.Error("the table of the new migration does not exist")
	}
}

// TestFailedMigrationRollsBack is T-123.
func TestFailedMigrationRollsBack(t *testing.T) {
	d := dbtest.New(t)
	conn := d.Connect(t, "owner")
	ms := withExtra(t, "CREATE TABLE pandokos.zz_two (i integer); SELECT 1/0; CREATE TABLE pandokos.zz_three (i integer)")
	_, err := migrate.Apply(ctx(t), conn, ms, "v")
	if err == nil || !strings.Contains(err.Error(), "migration 0002_extra") {
		t.Fatalf("error %v", err)
	}
	super := d.Super(t)
	if exists(t, super, "pandokos.zz_two") || exists(t, super, "pandokos.zz_three") {
		t.Error("a failed migration left objects behind")
	}
	if n := count(t, super, "SELECT count(*) FROM pandokos.schema_migration"); n != len(ms)-1 {
		t.Errorf("the ledger has %d rows, want %d", n, len(ms)-1)
	}
}

// TestMismatchRefused is T-124.
func TestMismatchRefused(t *testing.T) {
	cases := []struct {
		name   string
		tamper string
		want   error
	}{
		{"checksum", "UPDATE pandokos.schema_migration SET checksum = repeat('0', 64) WHERE version = 1", migrate.ErrChecksum},
		{"newer", "INSERT INTO pandokos.schema_migration (version, name, checksum, pandokos_version) VALUES (2, 'future', repeat('0', 64), 'x')", migrate.ErrSchemaNewer},
		{"gap", "INSERT INTO pandokos.schema_migration (version, name, checksum, pandokos_version) VALUES (3, 'future', repeat('0', 64), 'x')", migrate.ErrLedger},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := dbtest.New(t)
			conn := d.Connect(t, "owner")
			ms := realSet(t)
			if _, err := migrate.Apply(ctx(t), conn, ms, "v"); err != nil {
				t.Fatal(err)
			}
			super := d.Super(t)
			if _, err := super.Exec(ctx(t), c.tamper); err != nil {
				t.Fatal(err)
			}
			before := count(t, super, "SELECT count(*) FROM pandokos.schema_migration")

			if _, err := migrate.ReadStatus(ctx(t), conn, ms); !errors.Is(err, c.want) {
				t.Errorf("ReadStatus: %v", err)
			}
			if err := migrate.Check(ctx(t), conn, ms); !errors.Is(err, c.want) {
				t.Errorf("Check: %v", err)
			}
			// A longer binary must not apply anything on top of a bad ledger.
			longer := withExtra(t, "CREATE TABLE pandokos.zz_never (i integer)")
			if _, err := migrate.Apply(ctx(t), conn, longer, "v"); !errors.Is(err, c.want) {
				t.Errorf("Apply: %v", err)
			}
			if exists(t, super, "pandokos.zz_never") {
				t.Error("Apply changed the schema")
			}
			if after := count(t, super, "SELECT count(*) FROM pandokos.schema_migration"); after != before {
				t.Errorf("Apply changed the ledger: %d rows, was %d", after, before)
			}
		})
	}
}

// TestConcurrentApply is T-125.
func TestConcurrentApply(t *testing.T) {
	d := dbtest.New(t)
	ms := realSet(t)
	const n = 4
	conns := make([]*pgx.Conn, n)
	for i := range conns {
		conns[i] = d.Connect(t, "owner")
	}
	var wg sync.WaitGroup
	results := make([]migrate.Result, n)
	errs := make([]error, n)
	for i := range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			results[i], errs[i] = migrate.Apply(c, conns[i], ms, "v")
		}()
	}
	wg.Wait()
	applied := 0
	for i := range errs {
		if errs[i] != nil {
			t.Errorf("migrator %d: %v", i, errs[i])
		}
		applied += len(results[i].Applied)
	}
	if applied != len(ms) {
		t.Errorf("the migrators applied %d migrations in total, want %d", applied, len(ms))
	}
	super := d.Super(t)
	if got := count(t, super, "SELECT count(*) FROM pandokos.schema_migration"); got != len(ms) {
		t.Errorf("the ledger has %d rows, want %d", got, len(ms))
	}
	if got := count(t, super, "SELECT count(DISTINCT version) FROM pandokos.schema_migration"); got != len(ms) {
		t.Errorf("the ledger has %d distinct versions, want %d", got, len(ms))
	}
}

// TestPreflight is T-126.
func TestPreflight(t *testing.T) {
	d := dbtest.New(t)
	super := d.Super(t)
	owner := d.Connect(t, "owner")

	t.Run("accepts the real setup", func(t *testing.T) {
		if err := migrate.Preflight(ctx(t), owner, migrate.OwnerRole, migrate.RuntimeRoles); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("missing role", func(t *testing.T) {
		missing := "pandokos_missing_" + strings.ReplaceAll(d.Name, "pdk_t_", "")
		err := migrate.Preflight(ctx(t), owner, migrate.OwnerRole, []string{"pandokos_gateway", missing})
		if !errors.Is(err, migrate.ErrRoleMissing) || !strings.Contains(err.Error(), missing) {
			t.Fatalf("error %v", err)
		}
	})
	t.Run("privileged role", func(t *testing.T) {
		su := d.CreateLogin(t, "su", "SUPERUSER")
		err := migrate.Preflight(ctx(t), owner, su.User, migrate.RuntimeRoles)
		if !errors.Is(err, migrate.ErrRolePrivileged) || !strings.Contains(err.Error(), su.User) {
			t.Fatalf("error %v", err)
		}
	})
	t.Run("login that cannot act as the owner", func(t *testing.T) {
		gw := d.Connect(t, "gateway")
		err := migrate.Preflight(ctx(t), gw, migrate.OwnerRole, migrate.RuntimeRoles)
		if !errors.Is(err, migrate.ErrNotOwner) || !strings.Contains(err.Error(), d.User("gateway")) {
			t.Fatalf("error %v", err)
		}
	})
	if exists(t, super, "pandokos.schema_migration") {
		t.Error("preflight created something")
	}
}

// TestStatus is T-127.
func TestStatus(t *testing.T) {
	d := dbtest.New(t)
	conn := d.Connect(t, "owner")
	ms := realSet(t)

	st, err := migrate.ReadStatus(ctx(t), conn, ms)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Applied) != 0 || len(st.Pending) != len(ms) || st.Version != 0 || st.Want != len(ms) {
		t.Fatalf("empty database: %+v", st)
	}
	if err := migrate.Check(ctx(t), conn, ms); !errors.Is(err, migrate.ErrNoSchema) || !strings.Contains(err.Error(), `run "pandokos migrate"`) {
		t.Errorf("Check on an empty database: %v", err)
	}

	if _, err := migrate.Apply(ctx(t), conn, ms, "v"); err != nil {
		t.Fatal(err)
	}
	st, err = migrate.ReadStatus(ctx(t), conn, ms)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Applied) != len(ms) || len(st.Pending) != 0 || st.Version != len(ms) || st.Want != len(ms) {
		t.Fatalf("migrated database: %+v", st)
	}
	for _, a := range st.Applied {
		if a.AppliedAt.IsZero() || a.Checksum == "" || a.Name == "" {
			t.Errorf("applied row %+v", a)
		}
	}
	if err := migrate.Check(ctx(t), conn, ms); err != nil {
		t.Errorf("Check on a migrated database: %v", err)
	}
	longer := withExtra(t, "SELECT 1")
	err = migrate.Check(ctx(t), conn, longer)
	if !errors.Is(err, migrate.ErrSchemaOlder) {
		t.Fatalf("Check with a longer binary: %v", err)
	}
	want := fmt.Sprintf("the database schema is older than this binary: schema version %d, this binary needs version %d; run \"pandokos migrate\"", len(ms), len(longer))
	if err.Error() != want {
		t.Errorf("message %q, want %q", err.Error(), want)
	}
}
