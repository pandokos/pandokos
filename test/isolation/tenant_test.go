package isolation

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

// TestTenantTablePolicies is T-145: the visibility table of sprint.md section 5.6.
func TestTenantTablePolicies(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		a := d.SeedTenant(t, "tenant-a")
		d.SeedTenant(t, "tenant-b")
		platform := session{"platform", "app.scope", "platform"}
		none := session{name: "none"}
		sessA := sessions(a)[0]

		visible := map[string]map[string]int{
			"gateway": {"tenant-a": 1, "platform": 0, "none": 0},
			"admin":   {"tenant-a": 1, "platform": 2, "none": 0},
			"worker":  {"tenant-a": 1, "platform": 2, "none": 0},
		}
		for _, role := range runtimeRoles {
			conn := d.Connect(t, role)
			for _, s := range []session{sessA, platform, none} {
				var n int
				if err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
					return tx.QueryRow(c, `SELECT count(*) FROM pandokos.tenant`).Scan(&n)
				}); err != nil {
					t.Fatalf("%s/%s: %v", role, s.name, err)
				}
				if want := visible[role][s.name]; n != want {
					t.Errorf("%s in session %s sees %d tenants, want %d", role, s.name, n, want)
				}
			}
		}

		admin := d.Connect(t, "admin")
		err := inSession(t, admin, platform, func(c context.Context, tx pgx.Tx) error {
			var id, state string
			if err := tx.QueryRow(c, `INSERT INTO pandokos.tenant (name, display_name) VALUES ('t3', 'T3') RETURNING id::text, state`).Scan(&id, &state); err != nil {
				t.Errorf("admin cannot create a tenant: %v", err)
				return nil
			}
			if state != "provisioning" {
				t.Errorf("a new tenant is %s, want provisioning", state)
			}
			tag, err := tx.Exec(c, `UPDATE pandokos.tenant SET state = 'active' WHERE name = 't3'`)
			if err != nil || tag.RowsAffected() != 1 {
				t.Errorf("admin cannot activate the tenant: %v, %d rows", err, tag.RowsAffected())
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		denied := func(role string, s session, sql string) {
			t.Helper()
			conn := d.Connect(t, role)
			err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(c, sql)
				return err
			})
			if sqlstate(err) != insufficientPrivilege {
				t.Errorf("%s/%s: %q gave %v, want SQLSTATE %s", role, s.name, sql, err, insufficientPrivilege)
			}
		}
		denied("admin", platform, `INSERT INTO pandokos.tenant (id, name) VALUES (pandokos.uuid_v7(), 't4')`)
		denied("admin", platform, `INSERT INTO pandokos.tenant (name, state) VALUES ('t5', 'active')`)
		denied("admin", platform, `DELETE FROM pandokos.tenant WHERE name = 't3'`)
		for _, role := range []string{"gateway", "worker"} {
			denied(role, platform, `INSERT INTO pandokos.tenant (name) VALUES ('t6')`)
			denied(role, platform, `UPDATE pandokos.tenant SET display_name = 'x'`)
			denied(role, sessA, `DELETE FROM pandokos.tenant`)
		}
		// In a tenant's own session the admin role may read the tenant row but not change it.
		err = inSession(t, admin, sessA, func(c context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(c, `UPDATE pandokos.tenant SET display_name = 'x'`)
			if err != nil || tag.RowsAffected() != 0 {
				t.Errorf("admin update in a tenant session: %v, %d rows, want 0", err, tag.RowsAffected())
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

// walk returns the updates that take a new tenant to the state.
func walk(to tenant.State) []tenant.State {
	switch to {
	case tenant.StateActive:
		return []tenant.State{tenant.StateActive}
	case tenant.StateSuspended:
		return []tenant.State{tenant.StateActive, tenant.StateSuspended}
	case tenant.StateDeleting:
		return []tenant.State{tenant.StateDeleting}
	case tenant.StateDeleted:
		return []tenant.State{tenant.StateDeleting, tenant.StateDeleted}
	}
	return nil
}

// TestTenantLifecycle is T-146: the database and tenant.CanTransition agree on all 25 pairs.
func TestTenantLifecycle(t *testing.T) {
	d := dbtest.NewMigrated(t)
	super := d.Super(t)
	n := 0
	for _, from := range tenant.States() {
		for _, to := range tenant.States() {
			n++
			t.Run(string(from)+"->"+string(to), func(t *testing.T) {
				var id string
				if err := super.QueryRow(ctx(t), `INSERT INTO pandokos.tenant (name) VALUES ($1) RETURNING id::text`, fmt.Sprintf("life-%d", n)).Scan(&id); err != nil {
					t.Fatal(err)
				}
				for _, step := range walk(from) {
					if _, err := super.Exec(ctx(t), `UPDATE pandokos.tenant SET state = $2 WHERE id = $1::uuid`, id, string(step)); err != nil {
						t.Fatalf("walking to %s: %v", step, err)
					}
				}
				_, err := super.Exec(ctx(t), `UPDATE pandokos.tenant SET state = $2 WHERE id = $1::uuid`, id, string(to))
				switch {
				case from == tenant.StateDeleted:
					// A deleted tenant never changes again, not even to the same state.
					if sqlstate(err) != checkViolation {
						t.Errorf("update of a deleted tenant: %v, want SQLSTATE %s", err, checkViolation)
					}
				case from == to:
					if err != nil {
						t.Errorf("an update that keeps the state must succeed: %v", err)
					}
				case tenant.CanTransition(from, to):
					if err != nil {
						t.Errorf("the database refuses an allowed transition: %v", err)
					}
				default:
					if sqlstate(err) != checkViolation {
						t.Errorf("the database accepts or mis-reports a forbidden transition: %v, want SQLSTATE %s", err, checkViolation)
					}
				}
			})
		}
	}
	if n != 25 {
		t.Fatalf("%d pairs", n)
	}

	exec := func(sql string, args ...any) error {
		_, err := super.Exec(ctx(t), sql, args...)
		return err
	}
	t.Run("insert state", func(t *testing.T) {
		if err := exec(`INSERT INTO pandokos.tenant (name, state) VALUES ('x', 'active')`); sqlstate(err) != checkViolation {
			t.Errorf("insert in state active: %v, want SQLSTATE %s", err, checkViolation)
		}
	})
	t.Run("rows are never deleted", func(t *testing.T) {
		if err := exec(`INSERT INTO pandokos.tenant (name) VALUES ('keep')`); err != nil {
			t.Fatal(err)
		}
		if err := exec(`DELETE FROM pandokos.tenant WHERE name = 'keep'`); sqlstate(err) != restrictViolation {
			t.Errorf("delete: %v, want SQLSTATE %s", err, restrictViolation)
		}
	})
	t.Run("id is immutable", func(t *testing.T) {
		if err := exec(`UPDATE pandokos.tenant SET id = pandokos.uuid_v7() WHERE name = 'keep'`); sqlstate(err) != checkViolation {
			t.Errorf("update of id: %v, want SQLSTATE %s", err, checkViolation)
		}
	})
	t.Run("a deleted tenant cannot be changed", func(t *testing.T) {
		if err := exec(`INSERT INTO pandokos.tenant (name) VALUES ('gone')`); err != nil {
			t.Fatal(err)
		}
		for _, s := range []string{"deleting", "deleted"} {
			if err := exec(`UPDATE pandokos.tenant SET state = $1 WHERE name = 'gone'`, s); err != nil {
				t.Fatal(err)
			}
		}
		if err := exec(`UPDATE pandokos.tenant SET display_name = 'x' WHERE name = 'gone'`); sqlstate(err) != checkViolation {
			t.Errorf("update of a deleted tenant: %v, want SQLSTATE %s", err, checkViolation)
		}
	})
	t.Run("state_changed_at", func(t *testing.T) {
		if err := exec(`INSERT INTO pandokos.tenant (name) VALUES ('clock')`); err != nil {
			t.Fatal(err)
		}
		read := func() time.Time {
			var at time.Time
			if err := super.QueryRow(ctx(t), `SELECT state_changed_at FROM pandokos.tenant WHERE name = 'clock'`).Scan(&at); err != nil {
				t.Fatal(err)
			}
			return at
		}
		tick := func() {
			if err := exec(`SELECT pg_catalog.pg_sleep(0.02)`); err != nil {
				t.Fatal(err)
			}
		}
		first := read()
		tick()
		if err := exec(`UPDATE pandokos.tenant SET display_name = 'changed' WHERE name = 'clock'`); err != nil {
			t.Fatal(err)
		}
		if got := read(); !got.Equal(first) {
			t.Errorf("state_changed_at moved without a transition: %v -> %v", first, got)
		}
		tick()
		if err := exec(`UPDATE pandokos.tenant SET state = 'active' WHERE name = 'clock'`); err != nil {
			t.Fatal(err)
		}
		if got := read(); !got.After(first) {
			t.Errorf("state_changed_at did not move on a transition: %v -> %v", first, got)
		}
	})
	t.Run("names", func(t *testing.T) {
		for _, name := range []string{"A", "-a", "a-", "a_b", strings.Repeat("a", 64), ""} {
			if err := exec(`INSERT INTO pandokos.tenant (name) VALUES ($1)`, name); sqlstate(err) != checkViolation {
				t.Errorf("name %q: %v, want SQLSTATE %s", name, err, checkViolation)
			}
		}
		if err := exec(`INSERT INTO pandokos.tenant (name) VALUES ($1)`, strings.Repeat("a", 63)); err != nil {
			t.Errorf("a name of 63 characters: %v", err)
		}
		if err := exec(`INSERT INTO pandokos.tenant (name) VALUES ('taken')`); err != nil {
			t.Fatal(err)
		}
		if err := exec(`INSERT INTO pandokos.tenant (name) VALUES ('taken')`); sqlstate(err) != uniqueViolation {
			t.Errorf("a live tenant's name: %v, want SQLSTATE %s", err, uniqueViolation)
		}
		for _, s := range []string{"deleting", "deleted"} {
			if err := exec(`UPDATE pandokos.tenant SET state = $1 WHERE name = 'taken'`, s); err != nil {
				t.Fatal(err)
			}
		}
		if err := exec(`INSERT INTO pandokos.tenant (name) VALUES ('taken')`); err != nil {
			t.Errorf("a deleted tenant's name must be reusable: %v", err)
		}
	})
}
