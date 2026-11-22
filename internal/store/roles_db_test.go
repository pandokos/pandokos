package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

func sqlstate(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code
	}
	return ""
}

func constraintName(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.ConstraintName
	}
	return ""
}

const (
	checkViolation  = "23514"
	uniqueViolation = "23505"
	fkViolation     = "23503"
	noPrivilege     = "42501"
)

// TestRolesSchema is T-852: the constraints of migration 0014.
func TestRolesSchema(t *testing.T) {
	d := dbtest.NewMigrated(t)
	tid := d.SeedTenant(t, "roles-schema")
	other := d.SeedTenant(t, "roles-schema-other")
	super := d.Super(t)
	c := ctx(t)

	insertRole := func(tenantID tenant.ID, name string, perms []string) error {
		_, err := super.Exec(c, `INSERT INTO pandokos.tenant_role (tenant_id, name, permissions) VALUES ($1::uuid, $2, $3::text[])`, tenantID.String(), name, perms)
		return err
	}
	t.Run("role name", func(t *testing.T) {
		good := []string{"a", "reader", "reader-2", "x-" + strings.Repeat("a", 61), "platform", "platformer", "pandokos", "tenant-admin2"}
		for _, n := range good {
			if err := insertRole(tid, n, []string{"member:read"}); err != nil {
				t.Errorf("name %q refused: %v", n, err)
			}
		}
		bad := []string{"", "Upper", "1abc", "-a", "has space", "under_score", strings.Repeat("a", 64), "a.b",
			"owner", "tenant-admin", "credential-manager", "usage-viewer", "auditor", "developer", "inference", "read-only", "platform-admin", "platform-auditor",
			"platform-ops", "pandokos-x", "platform-"}
		for _, n := range bad {
			if err := insertRole(tid, n, []string{"member:read"}); sqlstate(err) != checkViolation {
				t.Errorf("name %q: %v, want a check violation", n, err)
			}
		}
	})
	t.Run("unique in the tenant, free in another", func(t *testing.T) {
		if err := insertRole(tid, "twin", []string{"member:read"}); err != nil {
			t.Fatal(err)
		}
		if err := insertRole(tid, "twin", []string{"member:read"}); sqlstate(err) != uniqueViolation || constraintName(err) != "tenant_role_name_key" {
			t.Errorf("a second role of the same name: %v", err)
		}
		if err := insertRole(other, "twin", []string{"member:read"}); err != nil {
			t.Errorf("the same name in another tenant: %v", err)
		}
	})
	t.Run("permissions", func(t *testing.T) {
		many := func(n int) []string {
			out := make([]string, n)
			for i := range out {
				out[i] = "res:act" + strings.Repeat("a", i%5) // names only have to match the format
			}
			return out
		}
		if err := insertRole(tid, "sixty-four", many(64)); err != nil {
			t.Errorf("64 permissions: %v", err)
		}
		for name, perms := range map[string][]string{
			"empty":        {},
			"sixty-five":   many(65),
			"upper":        {"Member:read"},
			"no colon":     {"memberread"},
			"two colons":   {"a:b:c"},
			"a digit":      {"member:read2"},
			"a space":      {"member: read"},
			"one good one": {"member:read", "bad"},
			"empty name":   {""},
		} {
			if err := insertRole(tid, "bad-perms-"+strings.ReplaceAll(name, " ", "-"), perms); sqlstate(err) != checkViolation {
				t.Errorf("permissions %s: %v, want a check violation", name, err)
			}
		}
		if _, err := super.Exec(c, `INSERT INTO pandokos.tenant_role (tenant_id, name, permissions) VALUES ($1::uuid, 'with-null', ARRAY['member:read', NULL])`, tid.String()); sqlstate(err) != checkViolation {
			t.Errorf("a NULL among the permissions: %v", err)
		}
		if _, err := super.Exec(c, `INSERT INTO pandokos.tenant_role (tenant_id, name, permissions) VALUES ($1::uuid, 'null-perms', NULL)`, tid.String()); sqlstate(err) != "23502" {
			t.Errorf("NULL permissions: %v", err)
		}
	})
	t.Run("state and description", func(t *testing.T) {
		if _, err := super.Exec(c, `INSERT INTO pandokos.tenant_role (tenant_id, name, permissions, state) VALUES ($1::uuid, 'frozen', ARRAY['member:read'], 'frozen')`, tid.String()); sqlstate(err) != checkViolation {
			t.Errorf("an unknown state: %v", err)
		}
		if _, err := super.Exec(c, `INSERT INTO pandokos.tenant_role (tenant_id, name, permissions, description) VALUES ($1::uuid, 'long-description', ARRAY['member:read'], $2)`, tid.String(), strings.Repeat("d", 501)); sqlstate(err) != checkViolation {
			t.Errorf("a description of 501 characters: %v", err)
		}
		if _, err := super.Exec(c, `INSERT INTO pandokos.tenant_role (tenant_id, name, permissions, description) VALUES ($1::uuid, 'ctrl-description', ARRAY['member:read'], E'a\nb')`, tid.String()); sqlstate(err) != checkViolation {
			t.Errorf("a control character in the description: %v", err)
		}
		if _, err := super.Exec(c, `INSERT INTO pandokos.tenant_role (tenant_id, name, permissions, description) VALUES ($1::uuid, 'ok-description', ARRAY['member:read'], $2)`, tid.String(), strings.Repeat("d", 500)); err != nil {
			t.Errorf("a description of 500 characters: %v", err)
		}
	})
	t.Run("the name is immutable, the rest changes", func(t *testing.T) {
		if err := insertRole(tid, "stays", []string{"member:read"}); err != nil {
			t.Fatal(err)
		}
		if _, err := super.Exec(c, `UPDATE pandokos.tenant_role SET name = 'renamed' WHERE tenant_id = $1::uuid AND name = 'stays'`, tid.String()); sqlstate(err) != checkViolation {
			t.Errorf("a rename: %v", err)
		}
		if _, err := super.Exec(c, `UPDATE pandokos.tenant_role SET description = 'x', permissions = ARRAY['role:read'], state = 'inactive' WHERE tenant_id = $1::uuid AND name = 'stays'`, tid.String()); err != nil {
			t.Errorf("a change of description, permissions and state: %v", err)
		}
	})
	t.Run("an assigned role is not deleted", func(t *testing.T) {
		if err := insertRole(tid, "held", []string{"member:read"}); err != nil {
			t.Fatal(err)
		}
		sa := d.SeedServiceAccount(t, tid, "holder")
		if _, err := super.Exec(c, `INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role) VALUES ($1::uuid, $2::uuid, 'held')`, tid.String(), sa); err != nil {
			t.Fatal(err)
		}
		_, err := super.Exec(c, `DELETE FROM pandokos.tenant_role WHERE tenant_id = $1::uuid AND name = 'held'`, tid.String())
		if sqlstate(err) != fkViolation || constraintName(err) != "tenant_role_in_use" {
			t.Errorf("deleting a role held by a service account: %v, want foreign_key_violation on tenant_role_in_use", err)
		}
		if _, err := super.Exec(c, `DELETE FROM pandokos.role_assignment WHERE role = 'held'`); err != nil {
			t.Fatal(err)
		}
		person := d.TenantUser(t, tid, "holder.member", []string{"held"})
		if _, err := super.Exec(c, `DELETE FROM pandokos.tenant_role WHERE tenant_id = $1::uuid AND name = 'held'`, tid.String()); sqlstate(err) != fkViolation {
			t.Errorf("deleting a role held by a member: %v", err)
		}
		if _, err := super.Exec(c, `DELETE FROM pandokos.membership_role WHERE user_id = $1::uuid`, person.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := super.Exec(c, `DELETE FROM pandokos.tenant_role WHERE tenant_id = $1::uuid AND name = 'held'`, tid.String()); err != nil {
			t.Errorf("deleting a role that nobody holds: %v", err)
		}
	})

	// Support actions.
	open := func(actorType, actorID, reason string, perms []string, seconds int) (string, error) {
		var id string
		err := super.QueryRow(c, `INSERT INTO pandokos.support_action (tenant_id, actor_type, actor_id, reason, permissions, expires_at)
			VALUES ($1::uuid, $2, $3, $4, $5::text[], now() + make_interval(secs => $6)) RETURNING id::text`,
			tid.String(), actorType, actorID, reason, perms, seconds).Scan(&id)
		return id, err
	}
	good := []string{"member:read"}
	t.Run("support action: what is accepted and what is not", func(t *testing.T) {
		if _, err := open("platform", "pabcdefghijklmnop", "recover the tenant", good, 3600); err != nil {
			t.Fatalf("a valid action: %v", err)
		}
		if _, err := open("user", "0192f0f0-0000-7000-8000-000000000001", "x", good, 86400); err != nil {
			t.Errorf("an action of exactly 24 hours: %v", err)
		}
		for name, c := range map[string]struct {
			actorType, actorID, reason string
			perms                      []string
			secs                       int
		}{
			"actor type":      {"anonymous", "p1", "r", good, 60},
			"actor id empty":  {"platform", "", "r", good, 60},
			"actor id format": {"platform", "has space", "r", good, 60},
			"reason empty":    {"platform", "p2", "", good, 60},
			"reason blank":    {"platform", "p3", "  \t ", good, 60},
			"reason long":     {"platform", "p4", strings.Repeat("r", 501), good, 60},
			"permissions":     {"platform", "p5", "r", []string{}, 60},
			"permission form": {"platform", "p6", "r", []string{"Bad"}, 60},
			"lifetime":        {"platform", "p7", "r", good, 86401},
			"already expired": {"platform", "p8", "r", good, -60},
			"no lifetime":     {"platform", "p9", "r", good, 0},
		} {
			if _, err := open(c.actorType, c.actorID, c.reason, c.perms, c.secs); sqlstate(err) != checkViolation {
				t.Errorf("%s: %v, want a check violation", name, err)
			}
		}
	})
	t.Run("support action: one open action per actor and tenant", func(t *testing.T) {
		first, err := open("platform", "pone0000000000000", "first", good, 600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := open("platform", "pone0000000000000", "second", good, 600); sqlstate(err) != uniqueViolation {
			t.Errorf("a second open action of the same actor: %v", err)
		}
		if _, err := open("user", "pone0000000000000", "another actor type", good, 600); err != nil {
			t.Errorf("the same ID as a user: %v", err)
		}
		if _, err := super.Exec(c, `UPDATE pandokos.support_action SET closed_at = now(), closed_by = 'platform' WHERE id = $1::uuid`, first); err != nil {
			t.Fatal(err)
		}
		if _, err := open("platform", "pone0000000000000", "after the close", good, 600); err != nil {
			t.Errorf("a new action after the first was closed: %v", err)
		}
	})
	t.Run("support action: the time of opening is the database's", func(t *testing.T) {
		var drift time.Duration
		var id string
		if err := super.QueryRow(c, `INSERT INTO pandokos.support_action (tenant_id, actor_type, actor_id, reason, permissions, opened_at, expires_at)
			VALUES ($1::uuid, 'platform', 'ptime000000000000', 'r', ARRAY['member:read'], now() - interval '3 days', now() + interval '1 hour') RETURNING id::text`, tid.String()).Scan(&id); err != nil {
			t.Fatalf("an opening with a time of its own: %v", err)
		}
		var secs float64
		if err := super.QueryRow(c, `SELECT extract(epoch FROM now() - opened_at) FROM pandokos.support_action WHERE id = $1::uuid`, id).Scan(&secs); err != nil {
			t.Fatal(err)
		}
		drift = time.Duration(secs * float64(time.Second))
		if drift > time.Minute || drift < -time.Minute {
			t.Errorf("the action was opened %v ago: the time of the row is not the database's", drift)
		}
		if _, err := super.Exec(c, `INSERT INTO pandokos.support_action (tenant_id, actor_type, actor_id, reason, permissions, expires_at, closed_at, closed_by)
			VALUES ($1::uuid, 'platform', 'popenclosed00000', 'r', ARRAY['member:read'], now() + interval '1 hour', now(), 'platform')`, tid.String()); sqlstate(err) != checkViolation {
			t.Errorf("an action inserted closed: %v", err)
		}
	})
	t.Run("support action: immutable but for its closing", func(t *testing.T) {
		id, err := open("platform", "pimm0000000000000", "immutable", good, 3600)
		if err != nil {
			t.Fatal(err)
		}
		for _, set := range []string{"reason = 'changed'", "permissions = ARRAY['member:write']", "actor_id = 'someoneelse'", "actor_type = 'user'", "actor_name = 'x'",
			"key_id = 'k'", "request_id = '0123456789abcdef0123456789abcdef'", "expires_at = expires_at + interval '1 minute'", "opened_at = opened_at - interval '1 minute'",
			"tenant_id = '" + other.String() + "'"} {
			if _, err := super.Exec(c, `UPDATE pandokos.support_action SET `+set+` WHERE id = $1::uuid`, id); sqlstate(err) != checkViolation {
				t.Errorf("UPDATE SET %s: %v, want a check violation", set, err)
			}
		}
		for name, set := range map[string]string{
			"closed_by without a time": "closed_by = 'platform'",
			"a time without closed_by": "closed_at = now()",
			"an unknown closer":        "closed_at = now(), closed_by = 'nobody'",
		} {
			if _, err := super.Exec(c, `UPDATE pandokos.support_action SET `+set+` WHERE id = $1::uuid`, id); sqlstate(err) != checkViolation {
				t.Errorf("%s: %v, want a check violation", name, err)
			}
		}
		if _, err := super.Exec(c, `UPDATE pandokos.support_action SET closed_at = now(), closed_by = 'expiry' WHERE id = $1::uuid`, id); sqlstate(err) != checkViolation {
			t.Errorf("closed as expired before the expiry: %v", err)
		}
		if _, err := super.Exec(c, `UPDATE pandokos.support_action SET closed_at = now() + interval '5 hours', closed_by = 'tenant', closed_by_id = 'u1', close_reason = 'done' WHERE id = $1::uuid`, id); err != nil {
			t.Fatalf("closing: %v", err)
		}
		var closedAfterExpiry bool
		if err := super.QueryRow(c, `SELECT closed_at <= expires_at AND closed_at <= now() FROM pandokos.support_action WHERE id = $1::uuid`, id).Scan(&closedAfterExpiry); err != nil || !closedAfterExpiry {
			t.Errorf("the closing time is not the database's now (%v, %v)", closedAfterExpiry, err)
		}
		if _, err := super.Exec(c, `UPDATE pandokos.support_action SET close_reason = 'again' WHERE id = $1::uuid`, id); sqlstate(err) != checkViolation {
			t.Errorf("a closed action changed again: %v", err)
		}
		if _, err := super.Exec(c, `UPDATE pandokos.support_action SET closed_at = now(), closed_by = 'platform' WHERE id = $1::uuid`, id); sqlstate(err) != checkViolation {
			t.Errorf("an action closed twice: %v", err)
		}
	})
	t.Run("support action: closed as expired only after the expiry", func(t *testing.T) {
		id, err := open("platform", "pexp0000000000000", "will expire", good, 3600)
		if err != nil {
			t.Fatal(err)
		}
		d.ExpireSupportAction(t, id)
		if _, err := super.Exec(c, `UPDATE pandokos.support_action SET closed_at = now(), closed_by = 'expiry' WHERE id = $1::uuid`, id); err != nil {
			t.Errorf("closing an expired action as expired: %v", err)
		}
		var atExpiry bool
		if err := super.QueryRow(c, `SELECT closed_at = expires_at FROM pandokos.support_action WHERE id = $1::uuid`, id).Scan(&atExpiry); err != nil || !atExpiry {
			t.Errorf("an action closed as expired is closed at its expiry (%v, %v)", atExpiry, err)
		}
	})
	t.Run("support action: no runtime role deletes one, and the others have no access", func(t *testing.T) {
		id, err := open("platform", "pdel0000000000000", "kept", good, 3600)
		if err != nil {
			t.Fatal(err)
		}
		admin := d.Open(t, store.RoleAdmin)
		err = admin.Platform(c, func(c context.Context, tx store.Tx) error {
			_, err := tx.Exec(c, `DELETE FROM pandokos.support_action WHERE id = $1::uuid`, id)
			return err
		})
		if sqlstate(err) != noPrivilege {
			t.Errorf("the admin role deleting a support action: %v, want SQLSTATE %s", err, noPrivilege)
		}
		err = admin.Platform(c, func(c context.Context, tx store.Tx) error {
			_, err := tx.Exec(c, `UPDATE pandokos.support_action SET reason = 'x' WHERE id = $1::uuid`, id)
			return err
		})
		if sqlstate(err) != noPrivilege {
			t.Errorf("the admin role changing the reason: %v, want SQLSTATE %s", err, noPrivilege)
		}
		for _, role := range []store.Role{store.RoleGateway, store.RoleWorker} {
			db := d.Open(t, role)
			err := db.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
				var n int
				return tx.QueryRow(c, `SELECT count(*) FROM pandokos.support_action`).Scan(&n)
			})
			if sqlstate(err) != noPrivilege {
				t.Errorf("%s reading support_action: %v, want SQLSTATE %s", role, err, noPrivilege)
			}
		}
	})
}

// TestActInTenant is T-856.
func TestActInTenant(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		a, b := seed(t, d)
		admin := d.Open(t, store.RoleAdmin)
		super := d.Super(t)

		t.Run("the transaction has the platform scope and the tenant", func(t *testing.T) {
			err := admin.ActInTenant(ctx(t), a, func(c context.Context, tx store.Tx) error {
				var scope bool
				var tid string
				if err := tx.QueryRow(c, `SELECT pandokos.platform_scope(), COALESCE(pandokos.current_tenant_id()::text, '')`).Scan(&scope, &tid); err != nil {
					return err
				}
				if !scope || tid != a.String() {
					t.Errorf("platform scope %v, tenant %q, want true and %s", scope, tid, a)
				}
				if n := scalarInt(c, t, tx, "SELECT count(*) FROM pandokos.service_account"); n != 2 {
					t.Errorf("the tenant's own service accounts: %d, want 2 (A's, not B's)", n)
				}
				if n := scalarInt(c, t, tx, "SELECT count(*) FROM pandokos.tenant"); n < 2 {
					t.Errorf("the platform scope sees %d tenants", n)
				}
				// A tenant audit row and a platform audit row in one transaction.
				q := queries.New(tx)
				if err := q.InsertAuditEvent(c, queries.InsertAuditEventParams{TenantID: a.String(), ActorType: "platform", ActorID: "pabcdefghijklmnop", Action: "support_action.open",
					ResourceType: "support_action", Outcome: "success", Detail: "{}"}); err != nil {
					t.Errorf("the tenant's audit row: %v", err)
				}
				if err := q.InsertPlatformAuditEvent(c, queries.InsertPlatformAuditEventParams{ActorType: "platform", ActorID: "pabcdefghijklmnop", Action: "support_action.open",
					ResourceType: "support_action", Outcome: "success", Detail: "{}"}); err != nil {
					t.Errorf("the platform's audit row: %v", err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			var tenantRows, platformRows int
			if err := super.QueryRow(ctx(t), `SELECT (SELECT count(*) FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'support_action.open'),
				(SELECT count(*) FROM pandokos.platform_audit_event WHERE action = 'support_action.open')`, a.String()).Scan(&tenantRows, &platformRows); err != nil {
				t.Fatal(err)
			}
			if tenantRows != 1 || platformRows != 1 {
				t.Errorf("committed %d tenant rows and %d platform rows, want one of each", tenantRows, platformRows)
			}
		})

		t.Run("the settings end with the transaction", func(t *testing.T) {
			if err := admin.ActInTenant(ctx(t), a, func(context.Context, store.Tx) error { return nil }); err != nil {
				t.Fatal(err)
			}
			err := admin.Unscoped(ctx(t), func(c context.Context, tx store.Tx) error {
				var scope bool
				if err := tx.QueryRow(c, `SELECT pandokos.platform_scope()`).Scan(&scope); err != nil {
					return err
				}
				if scope {
					t.Error("the platform scope outlived the transaction")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})

		t.Run("the admin role only, and a tenant", func(t *testing.T) {
			for _, role := range []store.Role{store.RoleGateway, store.RoleWorker} {
				db := d.Open(t, role)
				called := false
				err := db.ActInTenant(ctx(t), a, func(context.Context, store.Tx) error { called = true; return nil })
				if !errors.Is(err, store.ErrNotAdmin) || called {
					t.Errorf("%s: %v (called %v), want store.ErrNotAdmin and no call", role, err, called)
				}
			}
			if err := admin.ActInTenant(ctx(t), tenant.ID{}, func(context.Context, store.Tx) error { return nil }); !errors.Is(err, store.ErrNoTenant) {
				t.Errorf("a zero tenant: %v, want store.ErrNoTenant", err)
			}
		})

		t.Run("a failure after the first of two audit inserts leaves neither row and no action", func(t *testing.T) {
			var tenantBefore, platformBefore, actionsBefore int
			count := func() (int, int, int) {
				var x, y, z int
				if err := super.QueryRow(ctx(t), `SELECT (SELECT count(*) FROM pandokos.audit_event WHERE tenant_id = $1::uuid), (SELECT count(*) FROM pandokos.platform_audit_event),
					(SELECT count(*) FROM pandokos.support_action WHERE tenant_id = $1::uuid)`, b.String()).Scan(&x, &y, &z); err != nil {
					t.Fatal(err)
				}
				return x, y, z
			}
			tenantBefore, platformBefore, actionsBefore = count()
			boom := errors.New("boom")
			err := admin.ActInTenant(ctx(t), b, func(c context.Context, tx store.Tx) error {
				q := queries.New(tx)
				if _, err := q.OpenSupportAction(c, queries.OpenSupportActionParams{TenantID: b.String(), ActorType: "platform", ActorID: "pabcdefghijklmnop", Reason: "r",
					Permissions: []string{"member:read"}, Seconds: 600}); err != nil {
					return err
				}
				if err := q.InsertAuditEvent(c, queries.InsertAuditEventParams{TenantID: b.String(), ActorType: "platform", ActorID: "pabcdefghijklmnop", Action: "support_action.open",
					ResourceType: "support_action", Outcome: "success", Detail: "{}"}); err != nil {
					return err
				}
				return boom // the platform's row is never written
			})
			if !errors.Is(err, boom) {
				t.Fatalf("the error of the function is not returned: %v", err)
			}
			if x, y, z := count(); x != tenantBefore || y != platformBefore || z != actionsBefore {
				t.Errorf("after the failure: %d tenant rows (%d before), %d platform rows (%d), %d actions (%d): nothing may remain", x, tenantBefore, y, platformBefore, z, actionsBefore)
			}
		})
	})
}

// TestInTenant: a transaction in platform scope reads a tenant's rows on its own connection, and
// has no tenant again afterwards.
func TestInTenant(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		a, b := seed(t, d)
		admin := d.OpenSized(t, store.RoleAdmin, 1)

		t.Run("the tenant is set on the same transaction, then removed", func(t *testing.T) {
			c, cancel := context.WithTimeout(ctx(t), 20*time.Second)
			defer cancel()
			err := admin.Platform(c, func(c context.Context, tx store.Tx) error {
				for _, id := range []tenant.ID{a, b} {
					err := store.InTenant(c, tx, id, func(c context.Context) error {
						var scope bool
						var tid string
						if err := tx.QueryRow(c, `SELECT pandokos.platform_scope(), COALESCE(pandokos.current_tenant_id()::text, '')`).Scan(&scope, &tid); err != nil {
							return err
						}
						if !scope || tid != id.String() {
							t.Errorf("platform scope %v, tenant %q, want true and %s", scope, tid, id)
						}
						if n := scalarInt(c, t, tx, "SELECT count(*) FROM pandokos.service_account"); n != 2 {
							t.Errorf("the tenant's own service accounts: %d, want 2", n)
						}
						return nil
					})
					if err != nil {
						return err
					}
					if n := scalarInt(c, t, tx, "SELECT count(*) FROM pandokos.service_account"); n != 0 {
						t.Errorf("after InTenant the transaction sees %d service accounts, want none (no tenant)", n)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})

		t.Run("the tenant is removed after an error of the function too", func(t *testing.T) {
			err := admin.Platform(ctx(t), func(c context.Context, tx store.Tx) error {
				err := store.InTenant(c, tx, a, func(c context.Context) error {
					var id string
					return tx.QueryRow(c, "SELECT id::text FROM pandokos.service_account WHERE name = 'none'").Scan(&id)
				})
				if !errors.Is(err, store.ErrNoRows) {
					t.Errorf("the error of the function: %v, want store.ErrNoRows", err)
				}
				if n := scalarInt(c, t, tx, "SELECT count(*) FROM pandokos.service_account"); n != 0 {
					t.Errorf("after an error the transaction sees %d service accounts, want none (no tenant)", n)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})

		t.Run("platform scope and a tenant only", func(t *testing.T) {
			err := admin.Unscoped(ctx(t), func(c context.Context, tx store.Tx) error {
				called := false
				if err := store.InTenant(c, tx, a, func(context.Context) error { called = true; return nil }); !errors.Is(err, store.ErrNoPlatformScope) || called {
					t.Errorf("a transaction without the platform scope: %v (called %v), want store.ErrNoPlatformScope and no call", err, called)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			err = admin.Platform(ctx(t), func(c context.Context, tx store.Tx) error {
				if err := store.InTenant(c, tx, tenant.ID{}, func(context.Context) error { return nil }); !errors.Is(err, store.ErrNoTenant) {
					t.Errorf("a zero tenant: %v, want store.ErrNoTenant", err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	})
}
