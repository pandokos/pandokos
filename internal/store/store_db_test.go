package store_test

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

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/migrate"
	"github.com/pandokos/pandokos/internal/store/migrations"
	"github.com/pandokos/pandokos/internal/tenant"
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

// withExtra is the embedded set plus one synthetic migration.
func withExtra(t *testing.T) []migrate.Migration {
	t.Helper()
	dir := filepath.Join(testutil.RepoRoot(t), "internal", "store", "migrations", "sql")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		fsys["sql/"+e.Name()] = &fstest.MapFile{Data: b}
	}
	fsys[fmt.Sprintf("sql/%04d_extra.sql", len(entries)+1)] = &fstest.MapFile{Data: []byte("SELECT 1")}
	ms, err := migrate.Load(fsys, "sql")
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func openSized(t *testing.T, d *dbtest.Database, role store.Role, size int) *store.DB {
	t.Helper()
	db, err := store.Open(store.Options{Conn: d.ConnOptions(string(role)), Role: role, MaxConns: size, Migrations: realSet(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

// seed creates two active tenants with two service accounts each.
func seed(t *testing.T, d *dbtest.Database) (a, b tenant.ID) {
	t.Helper()
	a, b = d.SeedTenant(t, "tenant-a"), d.SeedTenant(t, "tenant-b")
	super := d.Super(t)
	for _, row := range []struct {
		id   tenant.ID
		name string
	}{{a, "a-one"}, {a, "a-two"}, {b, "b-one"}, {b, "b-two"}} {
		if _, err := super.Exec(ctx(t), "INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, $2)", row.id.String(), row.name); err != nil {
			t.Fatal(err)
		}
	}
	return a, b
}

func scalarInt(t *testing.T, c context.Context, tx store.Tx, q string, args ...any) int {
	t.Helper()
	var n int
	if err := tx.QueryRow(c, q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// TestCheckAcceptsMigratedDatabase is T-130.
func TestCheckAcceptsMigratedDatabase(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		for _, role := range []store.Role{store.RoleGateway, store.RoleAdmin, store.RoleWorker} {
			db := d.Open(t, role)
			if err := db.Check(ctx(t)); err != nil {
				t.Errorf("%s: Check: %v", role, err)
			}
			if err := db.Ready(ctx(t)); err != nil {
				t.Errorf("%s: Ready: %v", role, err)
			}
		}
	})
}

// TestCheckRefusesSchemaMismatch is T-131.
func TestCheckRefusesSchemaMismatch(t *testing.T) {
	check := func(t *testing.T, d *dbtest.Database, ms []migrate.Migration) error {
		t.Helper()
		db, err := store.Open(store.Options{Conn: d.ConnOptions("gateway"), Role: store.RoleGateway, MaxConns: 2, Migrations: ms})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(db.Close)
		return db.Check(ctx(t))
	}
	t.Run("not migrated", func(t *testing.T) {
		err := check(t, dbtest.New(t), realSet(t))
		want := `database (gateway): the database has no Pandokos schema; run "pandokos migrate"`
		if err == nil || err.Error() != want {
			t.Fatalf("got %v, want %s", err, want)
		}
		if !errors.Is(err, migrate.ErrNoSchema) {
			t.Error("errors.Is must find ErrNoSchema")
		}
	})
	t.Run("binary ahead", func(t *testing.T) {
		d := dbtest.NewMigrated(t)
		err := check(t, d, withExtra(t))
		want := `database (gateway): the database schema is older than this binary: schema version 1, this binary needs version 2; run "pandokos migrate"`
		if err == nil || err.Error() != want || !errors.Is(err, migrate.ErrSchemaOlder) {
			t.Fatalf("got %v, want %s", err, want)
		}
	})
	t.Run("database ahead", func(t *testing.T) {
		d := dbtest.NewMigrated(t)
		if _, err := d.Super(t).Exec(ctx(t), "INSERT INTO pandokos.schema_migration (version, name, checksum, pandokos_version) VALUES (2, 'future', repeat('0', 64), 'x')"); err != nil {
			t.Fatal(err)
		}
		err := check(t, d, realSet(t))
		want := "database (gateway): the database schema is newer than this binary: schema version 2, this binary knows version 1; upgrade Pandokos"
		if err == nil || err.Error() != want || !errors.Is(err, migrate.ErrSchemaNewer) {
			t.Fatalf("got %v, want %s", err, want)
		}
	})
	t.Run("checksum changed", func(t *testing.T) {
		d := dbtest.NewMigrated(t)
		if _, err := d.Super(t).Exec(ctx(t), "UPDATE pandokos.schema_migration SET checksum = repeat('0', 64) WHERE version = 1"); err != nil {
			t.Fatal(err)
		}
		err := check(t, d, realSet(t))
		want := "database (gateway): a migration recorded in the database differs from this binary: 0001_init"
		if err == nil || err.Error() != want || !errors.Is(err, migrate.ErrChecksum) {
			t.Fatalf("got %v, want %s", err, want)
		}
	})
}

// TestCheckRefusesWrongUser is T-132.
func TestCheckRefusesWrongUser(t *testing.T) {
	d := dbtest.NewMigrated(t)
	super := d.Super(t)
	check := func(o store.ConnOptions) error {
		db, err := store.Open(store.Options{Conn: o, Role: store.RoleGateway, MaxConns: 2, Migrations: realSet(t)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(db.Close)
		return db.Check(ctx(t))
	}
	su := d.CreateLogin(t, "su", "SUPERUSER")
	bypass := d.CreateLogin(t, "bypass", "BYPASSRLS", "pandokos_gateway")
	both := d.CreateLogin(t, "both", "", "pandokos_gateway", "pandokos_owner")
	two := d.CreateLogin(t, "two", "", "pandokos_gateway", "pandokos_admin")
	owns := d.CreateLogin(t, "owns", "", "pandokos_gateway")
	if _, err := super.Exec(ctx(t), "CREATE TABLE pandokos.zz_owned (i integer)"); err != nil {
		t.Fatal(err)
	}
	if _, err := super.Exec(ctx(t), "ALTER TABLE pandokos.zz_owned OWNER TO "+`"`+owns.User+`"`); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		opts store.ConnOptions
		want string
	}{
		{"superuser", su, `user "%s" is a superuser; a runtime role must not be`},
		{"BYPASSRLS", bypass, `user "%s" has BYPASSRLS; a runtime role must not`},
		{"another runtime role", d.ConnOptions("admin"), `user "%s" is not a member of pandokos_gateway`},
		{"owner member", both, `user "%s" is a member of pandokos_owner; a runtime role must not own the schema`},
		{"two runtime roles", two, `user "%s" is also a member of pandokos_admin; each role needs its own database user`},
		{"owns objects", owns, `user "%s" owns objects in schema pandokos; a runtime role must own nothing`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := check(c.opts)
			want := "database (gateway): " + fmt.Sprintf(c.want, c.opts.User)
			if err == nil || err.Error() != want {
				t.Fatalf("got %v, want %s", err, want)
			}
		})
	}
	t.Run("the proper user passes", func(t *testing.T) {
		if err := check(d.ConnOptions("gateway")); err != nil {
			t.Fatal(err)
		}
	})
}

// TestCheckCannotConnect is T-133.
func TestCheckCannotConnect(t *testing.T) {
	d := dbtest.NewMigrated(t)
	real := d.Passwords()[1]
	wrong := "wrong-" + fmt.Sprint(time.Now().UnixNano())
	for name, edit := range map[string]func(o *store.ConnOptions){
		"wrong password": func(o *store.ConnOptions) { o.Password = secret.New(wrong) },
		"closed port":    func(o *store.ConnOptions) { o.Port = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			o := d.ConnOptions("gateway")
			edit(&o)
			db, err := store.Open(store.Options{Conn: o, Role: store.RoleGateway, MaxConns: 2, Migrations: realSet(t)})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(db.Close)
			err = db.Check(ctx(t))
			if err == nil || !strings.Contains(err.Error(), "cannot connect") {
				t.Fatalf("got %v", err)
			}
			testutil.AssertAbsent(t, "error text", err.Error(), real, wrong)
		})
	}
}

// TestWithTenant is T-134.
func TestWithTenant(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		a, b := seed(t, d)
		gw := d.Open(t, store.RoleGateway)
		err := gw.WithTenant(ctx(t), a, func(c context.Context, tx store.Tx) error {
			var got string
			if err := tx.QueryRow(c, "SELECT pandokos.current_tenant_id()::text").Scan(&got); err != nil {
				return err
			}
			if got != a.String() {
				t.Errorf("current_tenant_id() = %s, want %s", got, a)
			}
			var total, foreign int
			if err := tx.QueryRow(c, "SELECT count(*), count(*) FILTER (WHERE tenant_id <> $1::uuid) FROM pandokos.service_account", a.String()).Scan(&total, &foreign); err != nil {
				return err
			}
			if total != 2 || foreign != 0 {
				t.Errorf("visible rows: %d, of other tenants: %d", total, foreign)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		adm := d.Open(t, store.RoleAdmin)
		if err := adm.WithTenant(ctx(t), a, func(c context.Context, tx store.Tx) error {
			_, err := tx.Exec(c, "INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, 'a-three')", a.String())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		super := d.Super(t)
		var na, nb int
		if err := super.QueryRow(ctx(t), "SELECT count(*) FILTER (WHERE tenant_id = $1::uuid), count(*) FILTER (WHERE tenant_id = $2::uuid) FROM pandokos.service_account", a.String(), b.String()).Scan(&na, &nb); err != nil {
			t.Fatal(err)
		}
		if na != 3 || nb != 2 {
			t.Errorf("rows after the write: A %d, B %d", na, nb)
		}

		// With a pool of one the next transaction reuses the connection.
		one := openSized(t, d, store.RoleAdmin, 1)
		if err := one.WithTenant(ctx(t), a, func(c context.Context, tx store.Tx) error {
			if n := scalarInt(t, c, tx, "SELECT count(*) FROM pandokos.service_account"); n != 3 {
				t.Errorf("tenant A sees %d rows", n)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := one.Platform(ctx(t), func(c context.Context, tx store.Tx) error {
			var setting string
			if err := tx.QueryRow(c, "SELECT COALESCE(pg_catalog.current_setting('app.tenant_id', true), '')").Scan(&setting); err != nil {
				return err
			}
			if setting != "" {
				t.Errorf("the tenant setting survived the transaction: %q", setting)
			}
			if n := scalarInt(t, c, tx, "SELECT count(*) FROM pandokos.service_account"); n != 0 {
				t.Errorf("a platform transaction sees %d service accounts", n)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// TestWithTenantRollsBack is T-135.
func TestWithTenantRollsBack(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a := d.SeedTenant(t, "tenant-a")
	db := openSized(t, d, store.RoleAdmin, 1)
	super := d.Super(t)
	rows := func() int {
		var n int
		if err := super.QueryRow(ctx(t), "SELECT count(*) FROM pandokos.service_account").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	insert := func(c context.Context, tx store.Tx) error {
		_, err := tx.Exec(c, "INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, 'x')", a.String())
		return err
	}

	t.Run("error", func(t *testing.T) {
		sentinel := errors.New("boom")
		err := db.WithTenant(ctx(t), a, func(c context.Context, tx store.Tx) error {
			if err := insert(c, tx); err != nil {
				return err
			}
			return sentinel
		})
		if err != sentinel { //nolint:errorlint // the error must be returned unchanged
			t.Fatalf("got %v, want the same error", err)
		}
		if n := rows(); n != 0 {
			t.Errorf("%d rows were written", n)
		}
	})
	t.Run("panic", func(t *testing.T) {
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			_ = db.WithTenant(ctx(t), a, func(c context.Context, tx store.Tx) error {
				if err := insert(c, tx); err != nil {
					return err
				}
				panic("kaboom")
			})
		}()
		if recovered != "kaboom" {
			t.Fatalf("recovered %v", recovered)
		}
		if n := rows(); n != 0 {
			t.Errorf("%d rows were written", n)
		}
	})
	t.Run("cancelled context", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx(t))
		err := db.WithTenant(cctx, a, func(c context.Context, tx store.Tx) error {
			if err := insert(c, tx); err != nil {
				return err
			}
			cancel()
			return insert(c, tx)
		})
		if err == nil {
			t.Fatal("expected an error")
		}
		if n := rows(); n != 0 {
			t.Errorf("%d rows were written", n)
		}
		// The pool has one connection: it must have been released.
		if err := db.WithTenant(ctx(t), a, insert); err != nil {
			t.Fatalf("the pool is not usable after cancellation: %v", err)
		}
		if n := rows(); n != 1 {
			t.Errorf("%d rows after the successful call, want 1", n)
		}
	})
}

// TestPlatform is T-136.
func TestPlatform(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		seed(t, d)
		adm := openSized(t, d, store.RoleAdmin, 1)
		if err := adm.Platform(ctx(t), func(c context.Context, tx store.Tx) error {
			if n := scalarInt(t, c, tx, "SELECT count(*) FROM pandokos.tenant"); n != 2 {
				t.Errorf("admin sees %d tenants, want 2", n)
			}
			if _, err := tx.Exec(c, "INSERT INTO pandokos.tenant (name) VALUES ('tenant-c')"); err != nil {
				t.Errorf("admin cannot create a tenant: %v", err)
			}
			if n := scalarInt(t, c, tx, "SELECT count(*) FROM pandokos.service_account"); n != 0 {
				t.Errorf("admin sees %d service accounts in platform scope", n)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// The scope is gone after the transaction: same connection, tenant transaction.
		a := d.SeedTenant(t, "tenant-d")
		if err := adm.WithTenant(ctx(t), a, func(c context.Context, tx store.Tx) error {
			var scope string
			if err := tx.QueryRow(c, "SELECT COALESCE(pg_catalog.current_setting('app.scope', true), '')").Scan(&scope); err != nil {
				return err
			}
			if scope != "" {
				t.Errorf("the platform scope survived the transaction: %q", scope)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		wk := d.Open(t, store.RoleWorker)
		if err := wk.Platform(ctx(t), func(c context.Context, tx store.Tx) error {
			if n := scalarInt(t, c, tx, "SELECT count(*) FROM pandokos.tenant"); n != 4 {
				t.Errorf("worker sees %d tenants, want 4", n)
			}
			_, err := tx.Exec(c, "INSERT INTO pandokos.tenant (name) VALUES ('tenant-e')")
			if err == nil {
				t.Error("the worker created a tenant")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// TestTenantContextDoesNotLeak is T-137.
func TestTenantContextDoesNotLeak(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		a, b := seed(t, d)
		db := openSized(t, d, store.RoleAdmin, 8)
		var wg sync.WaitGroup
		for i := 0; i < 200; i++ {
			id := a
			if i%2 == 1 {
				id = b
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				err := db.WithTenant(c, id, func(c context.Context, tx store.Tx) error {
					var total, foreign int
					if err := tx.QueryRow(c, "SELECT count(*), count(*) FILTER (WHERE tenant_id <> $1::uuid) FROM pandokos.service_account", id.String()).Scan(&total, &foreign); err != nil {
						return err
					}
					if total != 2 || foreign != 0 {
						t.Errorf("tenant %s saw %d rows, %d of them foreign", id, total, foreign)
					}
					_, err := tx.Exec(c, "SELECT pg_catalog.pg_sleep(0.005)")
					return err
				})
				if err != nil {
					t.Errorf("WithTenant: %v", err)
				}
			}()
		}
		wg.Wait()
		// Between transactions the pool shows nothing.
		if err := db.Platform(ctx(t), func(c context.Context, tx store.Tx) error {
			if n := scalarInt(t, c, tx, "SELECT count(*) FROM pandokos.service_account"); n != 0 {
				t.Errorf("a platform transaction sees %d service accounts", n)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// TestReady is T-138.
func TestReady(t *testing.T) {
	d := dbtest.NewMigrated(t)
	db := d.Open(t, store.RoleGateway)
	if err := db.Ready(ctx(t)); err != nil {
		t.Fatal(err)
	}
	d.SetAcceptingConnections(t, false)
	testutil.Eventually(t, 5*time.Second, "Ready to fail without connections", func() bool { return db.Ready(ctx(t)) != nil })
	d.SetAcceptingConnections(t, true)
	testutil.Eventually(t, 10*time.Second, "Ready to recover", func() bool { return db.Ready(ctx(t)) == nil })

	super := d.Super(t)
	if _, err := super.Exec(ctx(t), "INSERT INTO pandokos.schema_migration (version, name, checksum, pandokos_version) VALUES (99, 'future', repeat('0', 64), 'x')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Ready(ctx(t)); err == nil {
		t.Error("Ready must fail when the schema changed under the instance")
	}
	if _, err := super.Exec(ctx(t), "DELETE FROM pandokos.schema_migration WHERE version = 99"); err != nil {
		t.Fatal(err)
	}
	if err := db.Ready(ctx(t)); err != nil {
		t.Errorf("Ready after the schema is back: %v", err)
	}
}

// TestNoSessionState is T-139.
func TestNoSessionState(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		a, _ := seed(t, d)
		db := openSized(t, d, store.RoleAdmin, 1)
		moment := time.Date(2026, 11, 12, 10, 30, 15, 123456000, time.UTC)
		err := db.WithTenant(ctx(t), a, func(c context.Context, tx store.Tx) error {
			for i := 0; i < 100; i++ {
				text := fmt.Sprintf("text-%d", i)
				var (
					gs  string
					gi  int64
					gb  bool
					gt  time.Time
					gid string
				)
				if err := tx.QueryRow(c, "SELECT $1::text, $2::bigint, $3::boolean, $4::timestamptz, $5::uuid",
					text, int64(i), i%2 == 0, moment, a.String()).Scan(&gs, &gi, &gb, &gt, &gid); err != nil {
					return fmt.Errorf("iteration %d: %w", i, err)
				}
				if gs != text || gi != int64(i) || gb != (i%2 == 0) || !gt.Equal(moment) || gid != a.String() {
					t.Fatalf("iteration %d: got %q %d %v %v %q", i, gs, gi, gb, gt, gid)
				}
			}
			if strings.HasSuffix(t.Name(), "/direct") {
				if n := scalarInt(t, c, tx, "SELECT count(*) FROM pg_catalog.pg_prepared_statements"); n != 0 {
					t.Errorf("%d prepared statements exist on the connection", n)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}
