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
		// In a tenant's own session the admin role may read the tenant row but not change it; since
		// sprint 5.3 the row of the tenant is updatable there for its time zone, so the guard trigger
		// refuses any other column.
		err = inSession(t, admin, sessA, func(c context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(c, `UPDATE pandokos.tenant SET display_name = 'x'`)
			return err
		})
		if sqlstate(err) != insufficientPrivilege {
			t.Errorf("admin update in a tenant session: %v, want SQLSTATE %s", err, insufficientPrivilege)
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
	t.Run("suspended to active is refused while a deletion is requested", func(t *testing.T) {
		// Sprint 6.3 (D-3): a trigger holds it, whoever updates; the cancel (the removal of the request) frees it.
		id := d.SeedTenant(t, "life-requested")
		d.RequestDeletion(t, id, time.Now().Add(time.Hour))
		err := exec(`UPDATE pandokos.tenant SET state = 'active' WHERE id = $1::uuid`, id.String())
		if sqlstate(err) != restrictViolation || constraintOf(err) != "tenant_deletion_holds" {
			t.Errorf("reactivation with a request: %v, want SQLSTATE %s tenant_deletion_holds", err, restrictViolation)
		}
		if err := exec(`DELETE FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, id.String()); err != nil {
			t.Fatal(err)
		}
		if err := exec(`UPDATE pandokos.tenant SET state = 'active' WHERE id = $1::uuid`, id.String()); err != nil {
			t.Errorf("reactivation after the cancel: %v", err)
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

// notifications returns the payloads received on conn until nothing arrives for quiet.
func notifications(conn *pgx.Conn, quiet time.Duration) []string {
	var got []string
	for {
		c, cancel := context.WithTimeout(context.Background(), quiet)
		n, err := conn.WaitForNotification(c)
		cancel()
		if err != nil {
			return got
		}
		got = append(got, n.Payload)
	}
}

// TestTenantTimeZonePolicies is T-697: the admin role in a tenant session changes the three time
// zone columns of its own row and nothing else; the trigger of section 5.7 (4) refuses every shape
// that would move a boundary that has not been reached; the platform scope changes any tenant; the
// gateway and worker roles cannot; a change notifies limits:<tenant>.
func TestTenantTimeZonePolicies(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		super := d.Super(t)
		a, b := d.SeedTenant(t, "zone-a"), d.SeedTenant(t, "zone-b")
		sessA := sessions(a)[0]
		platform := session{name: "platform", setting: "app.scope", value: "platform"}
		ad := d.Connect(t, "admin")
		listener := d.Super(t)
		if _, err := listener.Exec(ctx(t), "LISTEN pandokos_cache"); err != nil {
			t.Fatal(err)
		}
		notifications(listener, 200*time.Millisecond)

		set := func(s session, id tenant.ID, zone, previous string, from any) (int64, error) {
			return execIn(t, ad, s, `UPDATE pandokos.tenant SET time_zone = $2, time_zone_previous = $3, time_zone_effective_from = $4::timestamptz WHERE id = $1::uuid`,
				id.String(), zone, previous, from)
		}
		in := func(days float64) any { return time.Now().UTC().Add(time.Duration(days * 24 * float64(time.Hour))) }
		zone := func(id tenant.ID) string {
			return scalar(t, super, `SELECT time_zone || '|' || time_zone_previous || '|' || COALESCE(time_zone_effective_from::text, '') FROM pandokos.tenant WHERE id = $1::uuid`, id.String())
		}
		expect := func(what, want string, err error) {
			t.Helper()
			if got := sqlstate(err); got != want {
				t.Errorf("%s: SQLSTATE %q (%v), want %q", what, got, err, want)
			}
		}

		t.Run("a tenant session changes its own zone", func(t *testing.T) {
			n, err := set(sessA, a, "Europe/Rome", "", in(10))
			if err != nil || n != 1 {
				t.Fatalf("a pending change: %d %v", n, err)
			}
			if got := notifications(listener, 400*time.Millisecond); len(got) != 1 || got[0] != "limits:"+a.String() {
				t.Errorf("notifications %v, want limits:%s", got, a)
			}
			// Withdrawn: the zone in force is the previous one again.
			if n, err := set(sessA, a, "", "", nil); err != nil || n != 1 {
				t.Errorf("a pending change withdrawn: %d %v", n, err)
			}
			if got := zone(a); got != "||" {
				t.Errorf("zone after the withdrawal %q", got)
			}
		})

		t.Run("name, display name and state are refused; another tenant's row is not reached", func(t *testing.T) {
			for what, sql := range map[string]string{
				"name":         `UPDATE pandokos.tenant SET name = 'renamed' WHERE id = $1::uuid`,
				"display name": `UPDATE pandokos.tenant SET display_name = 'x' WHERE id = $1::uuid`,
				"state":        `UPDATE pandokos.tenant SET state = 'suspended' WHERE id = $1::uuid`,
			} {
				_, err := execIn(t, ad, sessA, sql, a.String())
				expect(what, insufficientPrivilege, err)
			}
			// A statement that changes the zone and the display name together is refused as a whole.
			_, err := execIn(t, ad, sessA, `UPDATE pandokos.tenant SET display_name = 'x', time_zone = 'Europe/Rome', time_zone_previous = '', time_zone_effective_from = $2::timestamptz WHERE id = $1::uuid`, a.String(), in(5))
			expect("zone and display name together", insufficientPrivilege, err)
			if got := zone(a); got != "||" {
				t.Errorf("zone %q after a refused statement", got)
			}
			if n, err := set(sessA, b, "Europe/Rome", "", in(10)); err != nil || n != 0 {
				t.Errorf("A's session changed B's row: %d %v", n, err)
			}
			if got := zone(b); got != "||" {
				t.Errorf("B's zone %q", got)
			}
			if n, err := execIn(t, ad, noSession, `UPDATE pandokos.tenant SET time_zone = 'Europe/Rome'`); err == nil && n != 0 {
				t.Errorf("a session without a tenant changed %d rows", n)
			}
		})

		t.Run("every refusal of the trigger", func(t *testing.T) {
			expect("a zone in force changed without a pending instant", checkViolation, errOf(set(sessA, a, "Europe/Rome", "", nil)))
			expect("a previous zone that is not the one in force", checkViolation, errOf(set(sessA, a, "Europe/Rome", "Asia/Tokyo", in(5))))
			expect("an instant in the past", checkViolation, errOf(set(sessA, a, "Europe/Rome", "", in(-0.5))))
			expect("an instant now", checkViolation, errOf(set(sessA, a, "Europe/Rome", "", time.Now().UTC())))
			expect("an instant 33 days away", checkViolation, errOf(set(sessA, a, "Europe/Rome", "", in(33))))
			expect("a pending change whose previous equals the zone", checkViolation, errOf(set(sessA, a, "", "", in(5))))
			expect("a malformed zone name", checkViolation, errOf(set(sessA, a, "Europe Rome", "", in(5))))
			if got := zone(a); got != "||" {
				t.Fatalf("zone %q after refusals", got)
			}
			if n, err := set(sessA, a, "Europe/Rome", "", in(31)); err != nil || n != 1 {
				t.Fatalf("an instant 31 days away: %d %v", n, err)
			}
			pending := scalar(t, super, `SELECT time_zone_effective_from::text FROM pandokos.tenant WHERE id = $1::uuid`, a.String())
			// A replacement keeps the instant; one that moves it is refused.
			expect("a replacement that moves the instant", checkViolation, errOf(set(sessA, a, "Asia/Tokyo", "", in(5))))
			expect("a replacement with another previous", checkViolation, errOf(set(sessA, a, "Asia/Tokyo", "Europe/Rome", pending)))
			if n, err := execIn(t, ad, sessA, `UPDATE pandokos.tenant SET time_zone = 'Asia/Tokyo' WHERE id = $1::uuid`, a.String()); err != nil || n != 1 {
				t.Errorf("a replacement that keeps the instant: %d %v", n, err)
			}
			if got := scalar(t, super, `SELECT time_zone || '|' || time_zone_previous || '|' || (time_zone_effective_from::text = $2)::text FROM pandokos.tenant WHERE id = $1::uuid`, a.String(), pending); got != "Asia/Tokyo||true" {
				t.Errorf("after the replacement: %q", got)
			}
			// A pending zone that is the previous one again withdraws the change.
			if n, err := set(sessA, a, "", "", nil); err != nil || n != 1 {
				t.Errorf("withdrawal: %d %v", n, err)
			}
			// A tenant is created with a zone and without a pending change.
			expect("a tenant inserted with a pending change", checkViolation,
				errOf(execIn(t, super0(t, d), noSession, `INSERT INTO pandokos.tenant (name, time_zone, time_zone_previous, time_zone_effective_from) VALUES ('zone-ins', 'Asia/Tokyo', 'Europe/Rome', now() + interval '1 day')`)))
			expect("a tenant inserted with a previous zone", checkViolation,
				errOf(execIn(t, super0(t, d), noSession, `INSERT INTO pandokos.tenant (name, time_zone_previous, time_zone_effective_from) VALUES ('zone-ins', 'Europe/Rome', now() + interval '1 day')`)))
		})

		t.Run("after the instant the next change is a new pending one", func(t *testing.T) {
			// A change that took effect: the instant lies in the past. Only a superuser with the triggers off can write it.
			c := d.SeedTenant(t, "zone-c")
			if _, err := super.Exec(ctx(t), `SET session_replication_role = replica`); err != nil {
				t.Fatal(err)
			}
			if _, err := super.Exec(ctx(t), `UPDATE pandokos.tenant SET time_zone = 'Europe/Rome', time_zone_previous = '', time_zone_effective_from = now() - interval '1 day' WHERE id = $1::uuid`, c.String()); err != nil {
				t.Fatal(err)
			}
			if _, err := super.Exec(ctx(t), `SET session_replication_role = origin`); err != nil {
				t.Fatal(err)
			}
			sessC := sessions(c)[0]
			expect("the zone in force is Europe/Rome: changing it without a pending instant", checkViolation, errOf(set(sessC, c, "Asia/Tokyo", "", nil)))
			expect("a previous zone that is the old one", checkViolation, errOf(set(sessC, c, "Asia/Tokyo", "", in(5))))
			if n, err := set(sessC, c, "Asia/Tokyo", "Europe/Rome", in(5)); err != nil || n != 1 {
				t.Errorf("a new pending change after the instant: %d %v", n, err)
			}
			if n, err := set(sessC, c, "Europe/Rome", "", nil); err == nil && n == 1 {
				t.Log("withdrawn to the zone in force")
			} else {
				t.Errorf("a withdrawal: %d %v", n, err)
			}
		})

		t.Run("the platform scope changes any tenant", func(t *testing.T) {
			notifications(listener, 300*time.Millisecond) // what the subtests above sent
			if n, err := set(platform, b, "America/New_York", "", in(7)); err != nil || n != 1 {
				t.Errorf("platform scope, tenant B: %d %v", n, err)
			}
			if n, err := execIn(t, ad, platform, `UPDATE pandokos.tenant SET display_name = 'Tenant B' WHERE id = $1::uuid`, b.String()); err != nil || n != 1 {
				t.Errorf("platform scope, display name: %d %v", n, err)
			}
			// Only a change of the zone notifies; the display name does not.
			got := notifications(listener, 400*time.Millisecond)
			if len(got) != 1 || got[0] != "limits:"+b.String() {
				t.Errorf("notifications %v, want limits:%s once", got, b)
			}
		})

		t.Run("the gateway and the worker cannot", func(t *testing.T) {
			for _, role := range []string{"gateway", "worker"} {
				for _, s := range []session{sessA, platform} {
					_, err := execIn(t, d.Connect(t, role), s, `UPDATE pandokos.tenant SET time_zone = 'Europe/Rome'`)
					expect(role+" in session "+s.name, insufficientPrivilege, err)
				}
			}
		})

		t.Run("a tenant is created with a zone and without a pending change", func(t *testing.T) {
			rows, err := query(t, ad, platform, `INSERT INTO pandokos.tenant (name, time_zone) VALUES ('zone-new', 'Europe/Rome') RETURNING time_zone, time_zone_previous, time_zone_effective_from::text AS at`)
			if err != nil || len(rows) != 1 || rows[0]["time_zone"] != "Europe/Rome" || rows[0]["time_zone_previous"] != "" || rows[0]["at"] != nil {
				t.Errorf("created: %v %v", rows, err)
			}
			_, err = execIn(t, ad, platform, `INSERT INTO pandokos.tenant (name, time_zone_previous, time_zone_effective_from) VALUES ('zone-new-2', 'Europe/Rome', now() + interval '1 day')`)
			if sqlstate(err) != insufficientPrivilege && sqlstate(err) != checkViolation {
				t.Errorf("created with a pending change: %v", err)
			}
		})
	})
}

// TestActiveCurrencyPolicies is T-743: the admin role in the tenant's session changes the three currency
// columns of its own row; the platform scope, another tenant's session, a session without scope and a
// superuser without scope are refused; the gateway and the worker cannot update; every refusal of the
// guard (no pending instant, an instant in the past or beyond 32 days, a replacement that moves the
// instant, a wrong previous currency); the format; a tenant is created with the default; the two
// notifications.
func TestActiveCurrencyPolicies(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		super := d.Super(t)
		a, b := d.SeedTenant(t, "cur-a"), d.SeedTenant(t, "cur-b")
		sessA := sessions(a)[0]
		platform := session{name: "platform", setting: "app.scope", value: "platform"}
		ad := d.Connect(t, "admin")
		listener := d.Super(t)
		if _, err := listener.Exec(ctx(t), "LISTEN pandokos_cache"); err != nil {
			t.Fatal(err)
		}
		notifications(listener, 200*time.Millisecond)

		set := func(s session, id tenant.ID, currency, previous string, from any) (int64, error) {
			return execIn(t, ad, s, `UPDATE pandokos.tenant SET active_currency = $2, active_currency_previous = $3, active_currency_effective_from = $4::timestamptz WHERE id = $1::uuid`,
				id.String(), currency, previous, from)
		}
		in := func(days float64) any { return time.Now().UTC().Add(time.Duration(days * 24 * float64(time.Hour))) }
		row := func(id tenant.ID) string {
			return scalar(t, super, `SELECT active_currency || '|' || active_currency_previous || '|' || COALESCE(active_currency_effective_from::text, '') FROM pandokos.tenant WHERE id = $1::uuid`, id.String())
		}
		expect := func(what, want string, err error) {
			t.Helper()
			if got := sqlstate(err); got != want {
				t.Errorf("%s: SQLSTATE %q (%v), want %q", what, got, err, want)
			}
		}
		sorted := func(ns []string) string {
			if len(ns) == 2 && ns[0] > ns[1] {
				ns[0], ns[1] = ns[1], ns[0]
			}
			return fmt.Sprint(ns)
		}

		t.Run("a tenant session changes its own currency, and two notifications follow", func(t *testing.T) {
			n, err := set(sessA, a, "EUR", "", in(10))
			if err != nil || n != 1 {
				t.Fatalf("a pending change: %d %v", n, err)
			}
			if got, want := sorted(notifications(listener, 400*time.Millisecond)), fmt.Sprint([]string{"catalog:" + a.String(), "limits:" + a.String()}); got != want {
				t.Errorf("notifications %s, want %s", got, want)
			}
			// Withdrawn: the currency in force is the previous one again.
			if n, err := set(sessA, a, "", "", nil); err != nil || n != 1 {
				t.Errorf("a pending change withdrawn: %d %v", n, err)
			}
			if got := row(a); got != "||" {
				t.Errorf("the columns after the withdrawal %q", got)
			}
		})

		t.Run("nobody else changes it: the platform scope, another tenant, no session, a superuser without a scope", func(t *testing.T) {
			expect("the platform scope", insufficientPrivilege, errOf(set(platform, a, "EUR", "", in(5))))
			expect("the platform scope, every column of the row", insufficientPrivilege,
				errOf(execIn(t, ad, platform, `UPDATE pandokos.tenant SET active_currency = 'EUR', active_currency_effective_from = now() + interval '1 day'`)))
			if n, err := set(sessA, b, "EUR", "", in(10)); err != nil || n != 0 {
				t.Errorf("A's session changed B's row: %d %v", n, err)
			}
			for _, s := range []session{noSession, {name: "empty", setting: "app.tenant_id", value: ""}} {
				if n, err := set(s, a, "EUR", "", in(10)); err == nil && n != 0 {
					t.Errorf("session %s changed %d rows", s.name, n)
				}
			}
			expect("a superuser without a scope", insufficientPrivilege,
				errOf(execIn(t, super0(t, d), noSession, `UPDATE pandokos.tenant SET active_currency = 'EUR', active_currency_effective_from = now() + interval '1 day' WHERE id = $1::uuid`, a.String())))
			expect("a superuser in the platform scope", insufficientPrivilege,
				errOf(execIn(t, super0(t, d), platform, `UPDATE pandokos.tenant SET active_currency = 'EUR', active_currency_effective_from = now() + interval '1 day' WHERE id = $1::uuid`, a.String())))
			if got := row(a) + " " + row(b); got != "|| ||" {
				t.Errorf("the columns after the refusals: %q", got)
			}
			// The other columns of the row keep their policies: the platform scope still renames.
			if n, err := execIn(t, ad, platform, `UPDATE pandokos.tenant SET display_name = 'Tenant B' WHERE id = $1::uuid`, b.String()); err != nil || n != 1 {
				t.Errorf("platform scope, display name: %d %v", n, err)
			}
			// A tenant session cannot rename, change the state or the zone through the same statement.
			expect("the currency and the display name together", insufficientPrivilege,
				errOf(execIn(t, ad, sessA, `UPDATE pandokos.tenant SET display_name = 'x', active_currency = 'EUR', active_currency_effective_from = now() + interval '1 day' WHERE id = $1::uuid`, a.String())))
		})

		t.Run("the gateway and the worker cannot update", func(t *testing.T) {
			for _, role := range []string{"gateway", "worker"} {
				for _, s := range []session{sessA, platform} {
					_, err := execIn(t, d.Connect(t, role), s, `UPDATE pandokos.tenant SET active_currency = 'EUR'`)
					expect(role+" in session "+s.name, insufficientPrivilege, err)
				}
			}
		})

		t.Run("every refusal of the guard", func(t *testing.T) {
			expect("a currency in force changed without a pending instant", checkViolation, errOf(set(sessA, a, "EUR", "", nil)))
			expect("a previous currency that is not the one in force", checkViolation, errOf(set(sessA, a, "EUR", "GBP", in(5))))
			expect("an instant in the past", checkViolation, errOf(set(sessA, a, "EUR", "", in(-0.5))))
			expect("an instant now", checkViolation, errOf(set(sessA, a, "EUR", "", time.Now().UTC())))
			expect("an instant 33 days away", checkViolation, errOf(set(sessA, a, "EUR", "", in(33))))
			expect("a malformed currency", checkViolation, errOf(set(sessA, a, "eur", "", in(5))))
			expect("a currency of two letters", checkViolation, errOf(set(sessA, a, "EU", "", in(5))))
			expect("a malformed previous currency", checkViolation, errOf(set(sessA, a, "EUR", "usd", in(5))))
			if got := row(a); got != "||" {
				t.Fatalf("the columns %q after refusals", got)
			}
			if n, err := set(sessA, a, "EUR", "", in(31)); err != nil || n != 1 {
				t.Fatalf("an instant 31 days away: %d %v", n, err)
			}
			pending := scalar(t, super, `SELECT active_currency_effective_from::text FROM pandokos.tenant WHERE id = $1::uuid`, a.String())
			// A replacement keeps the instant; one that moves it is refused.
			expect("a replacement that moves the instant", checkViolation, errOf(set(sessA, a, "GBP", "", in(5))))
			expect("a replacement with another previous", checkViolation, errOf(set(sessA, a, "GBP", "EUR", pending)))
			if n, err := execIn(t, ad, sessA, `UPDATE pandokos.tenant SET active_currency = 'GBP' WHERE id = $1::uuid`, a.String()); err != nil || n != 1 {
				t.Errorf("a replacement that keeps the instant: %d %v", n, err)
			}
			if got := scalar(t, super, `SELECT active_currency || '|' || active_currency_previous || '|' || (active_currency_effective_from::text = $2)::text FROM pandokos.tenant WHERE id = $1::uuid`, a.String(), pending); got != "GBP||true" {
				t.Errorf("after the replacement: %q", got)
			}
			if n, err := set(sessA, a, "", "", nil); err != nil || n != 1 {
				t.Errorf("withdrawal: %d %v", n, err)
			}
			expect("a currency of the wrong format in the constraint, whoever writes it", checkViolation,
				errOf(execIn(t, super0(t, d), session{name: "tenant-a", setting: "app.tenant_id", value: a.String()}, `UPDATE pandokos.tenant SET active_currency = 'eur', active_currency_effective_from = now() + interval '1 day' WHERE id = $1::uuid`, a.String())))
		})

		t.Run("after the instant the next change is a new pending one", func(t *testing.T) {
			// A change that took effect: the instant lies in the past. Only a superuser with the triggers off can write it.
			c := d.SeedTenant(t, "cur-c")
			d.SetTenantCurrency(t, c, "EUR", "", time.Now().Add(-24*time.Hour))
			sessC := sessions(c)[0]
			expect("the currency in force is EUR: changing it without a pending instant", checkViolation, errOf(set(sessC, c, "GBP", "", nil)))
			expect("a previous currency that is the old one", checkViolation, errOf(set(sessC, c, "GBP", "", in(5))))
			if n, err := set(sessC, c, "GBP", "EUR", in(5)); err != nil || n != 1 {
				t.Errorf("a new pending change after the instant: %d %v", n, err)
			}
			if n, err := set(sessC, c, "EUR", "", nil); err != nil || n != 1 {
				t.Errorf("a withdrawal: %d %v", n, err)
			}
		})

		t.Run("a tenant is created with the default", func(t *testing.T) {
			rows, err := query(t, ad, platform, `INSERT INTO pandokos.tenant (name) VALUES ('cur-new') RETURNING active_currency, active_currency_previous, active_currency_effective_from::text AS at`)
			if err != nil || len(rows) != 1 || rows[0]["active_currency"] != "" || rows[0]["active_currency_previous"] != "" || rows[0]["at"] != nil {
				t.Errorf("created: %v %v", rows, err)
			}
			for what, sql := range map[string]string{
				"a currency":             `INSERT INTO pandokos.tenant (name, active_currency) VALUES ('cur-ins-1', 'EUR')`,
				"a previous currency":    `INSERT INTO pandokos.tenant (name, active_currency_previous) VALUES ('cur-ins-2', 'EUR')`,
				"a pending change":       `INSERT INTO pandokos.tenant (name, active_currency, active_currency_previous, active_currency_effective_from) VALUES ('cur-ins-3', 'EUR', 'USD', now() + interval '1 day')`,
				"an instant and nothing": `INSERT INTO pandokos.tenant (name, active_currency_effective_from) VALUES ('cur-ins-4', now() + interval '1 day')`,
			} {
				_, err := execIn(t, ad, platform, sql)
				if got := sqlstate(err); got != insufficientPrivilege && got != checkViolation {
					t.Errorf("%s from the administration role: %v", what, err)
				}
				expect(what+" from a superuser", checkViolation, errOf(execIn(t, super0(t, d), noSession, sql)))
			}
		})
	})
}

// super0 is a fresh superuser connection, for statements that run in a session of their own.
func super0(t *testing.T, d *dbtest.Database) *pgx.Conn { t.Helper(); return d.Super(t) }
