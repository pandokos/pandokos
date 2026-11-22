package isolation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
)

// TestTenantRoleTable is T-854: the harness's negative tests on tenant_role for every runtime role,
// the grants of the matrix of appendix A1, and the lint with nothing to report.
func TestTenantRoleTable(t *testing.T) {
	spec := DefaultSpec()
	d := dbtest.NewMigrated(t)
	findings, err := Lint(ctx(t), d.Super(t), spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Errorf("lint %s %s: %s", f.Rule, f.Object, f.Detail)
	}
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		a, b := d.SeedTenant(t, "tenant-a"), d.SeedTenant(t, "tenant-b")
		super := d.Super(t)
		scoped := false
		for _, tbl := range tenantScopedTables(t, super, spec) {
			scoped = scoped || tbl == "tenant_role"
		}
		if !scoped {
			t.Fatal("tenant_role is not among the tenant-scoped tables of the catalogue")
		}
		fx, ok := spec.Fixtures["tenant_role"]
		if !ok {
			t.Fatal("tenant_role has no fixture")
		}
		keyA, keyB := insertRow(t, super, fx, a), insertRow(t, super, fx, b)
		before := snapshot(t, super, "tenant_role", b)
		if before == "[]" {
			t.Fatal("tenant B has no row in tenant_role: the test would prove nothing")
		}
		for _, role := range runtimeRoles {
			conn := d.Connect(t, role)
			for _, sess := range sessions(a) {
				for _, st := range statements("tenant_role", fx, a, b, keyB, sess) {
					t.Run(role+"/"+sess.name+"/"+st.name, func(t *testing.T) {
						ensureRow(t, super, "tenant_role", fx, a, &keyA)
						res := runCommitted(t, conn, sess, st)
						st.check(t, res, b)
						if after := snapshot(t, super, "tenant_role", b); after != before {
							t.Fatalf("tenant B's rows changed:\nbefore %s\nafter  %s", before, after)
						}
					})
				}
			}
		}

		// The grants: the gateway role reads and writes nothing, the worker role has nothing, the
		// admin role writes the columns of the matrix and deletes.
		sessA := sessions(a)[0]
		gw, adm, wk := d.Connect(t, "gateway"), d.Connect(t, "admin"), d.Connect(t, "worker")
		run := func(conn *pgx.Conn, q string, args ...any) (int64, error) {
			var n int64
			err := inSession(t, conn, sessA, func(c context.Context, tx pgx.Tx) error {
				tag, err := tx.Exec(c, q, args...)
				n = tag.RowsAffected()
				return err
			})
			return n, err
		}
		if err := inSession(t, gw, sessA, func(c context.Context, tx pgx.Tx) error {
			var n int
			if err := tx.QueryRow(c, `SELECT count(*) FROM pandokos.tenant_role`).Scan(&n); err != nil || n < 1 {
				t.Errorf("the gateway role reads %d roles of its tenant (%v)", n, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		for name, q := range map[string]string{
			"insert": `INSERT INTO pandokos.tenant_role (tenant_id, name, permissions) VALUES ($1::uuid, 'by-gateway', ARRAY['member:read'])`,
			"update": `UPDATE pandokos.tenant_role SET description = 'x' WHERE tenant_id = $1::uuid`,
			"delete": `DELETE FROM pandokos.tenant_role WHERE tenant_id = $1::uuid`,
		} {
			if _, err := run(gw, q, a.String()); sqlstate(err) != insufficientPrivilege {
				t.Errorf("the gateway role's %s on tenant_role: %v, want SQLSTATE %s", name, err, insufficientPrivilege)
			}
		}
		if _, err := run(wk, `SELECT count(*) FROM pandokos.tenant_role`); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the worker role reading tenant_role: %v, want SQLSTATE %s", err, insufficientPrivilege)
		}
		if n, err := run(adm, `INSERT INTO pandokos.tenant_role (tenant_id, name, description, permissions) VALUES ($1::uuid, 'by-admin', 'd', ARRAY['member:read'])`, a.String()); err != nil || n != 1 {
			t.Errorf("the admin role inserting a role: %d %v", n, err)
		}
		if _, err := run(adm, `INSERT INTO pandokos.tenant_role (tenant_id, name, permissions) VALUES ($1::uuid, 'other-tenant', ARRAY['member:read'])`, b.String()); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the admin role inserting a role for another tenant: %v, want SQLSTATE %s", err, insufficientPrivilege)
		}
		if _, err := run(adm, `INSERT INTO pandokos.tenant_role (tenant_id, name, permissions, state) VALUES ($1::uuid, 'with-state', ARRAY['member:read'], 'inactive')`, a.String()); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the admin role inserting the state column: %v, want SQLSTATE %s (a role is born active)", err, insufficientPrivilege)
		}
		if n, err := run(adm, `UPDATE pandokos.tenant_role SET description = 'y', permissions = ARRAY['role:read'], state = 'inactive' WHERE name = 'by-admin'`); err != nil || n != 1 {
			t.Errorf("the admin role updating description, permissions and state: %d %v", n, err)
		}
		for _, col := range []string{"name = 'renamed'", "tenant_id = '" + b.String() + "'::uuid", "created_at = now()"} {
			if _, err := run(adm, `UPDATE pandokos.tenant_role SET `+col+` WHERE name = 'by-admin'`); sqlstate(err) != insufficientPrivilege {
				t.Errorf("the admin role updating %s: %v, want SQLSTATE %s", col, err, insufficientPrivilege)
			}
		}
		if n, err := run(adm, `DELETE FROM pandokos.tenant_role WHERE name = 'by-admin'`); err != nil || n != 1 {
			t.Errorf("the admin role deleting a role: %d %v", n, err)
		}
	})
}

// TestSupportActionVisibility is T-855: support_action per session.
func TestSupportActionVisibility(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		a, b := d.SeedTenant(t, "tenant-a"), d.SeedTenant(t, "tenant-b")
		actionA := d.SupportAction(t, a, "platform", "pabcdefghijklmnop", "recover A", time.Hour, "member:read")
		actionB := d.SupportAction(t, b, "platform", "pabcdefghijklmnop", "recover B", time.Hour, "member:read")
		super := d.Super(t)
		admin, gateway, worker := d.Connect(t, "admin"), d.Connect(t, "gateway"), d.Connect(t, "worker")
		platform := session{"platform", "app.scope", "platform"}
		sessA := sessions(a)[0]
		none, empty := session{name: "none"}, session{"empty", "app.tenant_id", ""}

		count := func(conn *pgx.Conn, s session) int {
			t.Helper()
			var n int
			if err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
				return tx.QueryRow(c, `SELECT count(*) FROM pandokos.support_action`).Scan(&n)
			}); err != nil {
				t.Fatalf("count in %s: %v", s.name, err)
			}
			return n
		}
		exec := func(conn *pgx.Conn, s session, q string, args ...any) (int64, error) {
			t.Helper()
			var n int64
			err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
				tag, err := tx.Exec(c, q, args...)
				n = tag.RowsAffected()
				return err
			})
			return n, err
		}

		t.Run("reading", func(t *testing.T) {
			if n := count(admin, platform); n != 2 {
				t.Errorf("the platform scope sees %d actions, want both", n)
			}
			if n := count(admin, sessA); n != 1 {
				t.Errorf("tenant A's session sees %d actions, want its own", n)
			}
			if n := count(admin, none) + count(admin, empty); n != 0 {
				t.Errorf("a session without a tenant sees %d actions", n)
			}
			var tenantOfSeen string
			if err := inSession(t, admin, sessA, func(c context.Context, tx pgx.Tx) error {
				return tx.QueryRow(c, `SELECT tenant_id::text FROM pandokos.support_action`).Scan(&tenantOfSeen)
			}); err != nil || tenantOfSeen != a.String() {
				t.Errorf("tenant A's session saw %q (%v)", tenantOfSeen, err)
			}
		})
		t.Run("the gateway and worker roles are refused", func(t *testing.T) {
			for name, conn := range map[string]*pgx.Conn{"gateway": gateway, "worker": worker} {
				for _, s := range []session{platform, sessA, none} {
					if _, err := exec(conn, s, `SELECT count(*) FROM pandokos.support_action`); sqlstate(err) != insufficientPrivilege {
						t.Errorf("%s reading in %s: %v, want SQLSTATE %s", name, s.name, err, insufficientPrivilege)
					}
				}
				if _, err := exec(conn, platform, `UPDATE pandokos.support_action SET closed_at = now(), closed_by = 'platform'`); sqlstate(err) != insufficientPrivilege {
					t.Errorf("%s closing: %v", name, err)
				}
			}
		})
		t.Run("a tenant cannot open one, another tenant's cannot be ended", func(t *testing.T) {
			ins := `INSERT INTO pandokos.support_action (tenant_id, actor_type, actor_id, reason, permissions, expires_at)
				VALUES ($1::uuid, 'platform', 'pzzzzzzzzzzzzzzzz', 'self-issued', ARRAY['member:read'], now() + interval '1 hour')`
			for _, tid := range []string{a.String(), b.String()} {
				if _, err := exec(admin, sessA, ins, tid); sqlstate(err) != insufficientPrivilege {
					t.Errorf("tenant A's session opening an action for %s: %v, want SQLSTATE %s", tid, err, insufficientPrivilege)
				}
				if _, err := exec(admin, none, ins, tid); sqlstate(err) != insufficientPrivilege {
					t.Errorf("a session without a tenant opening an action for %s: %v", tid, err)
				}
			}
			if n, err := exec(admin, sessA, `UPDATE pandokos.support_action SET closed_at = now(), closed_by = 'tenant', closed_by_id = 'u' WHERE id = $1::uuid`, actionB); err != nil || n != 0 {
				t.Errorf("tenant A's session ending B's action: %d rows, %v, want none", n, err)
			}
			if n, err := exec(admin, none, `UPDATE pandokos.support_action SET closed_at = now(), closed_by = 'tenant' WHERE id = $1::uuid`, actionA); err != nil || n != 0 {
				t.Errorf("a session without a tenant ending A's action: %d rows, %v", n, err)
			}
			var open int
			if err := super.QueryRow(ctx(t), `SELECT count(*) FROM pandokos.support_action WHERE closed_at IS NULL`).Scan(&open); err != nil || open != 2 {
				t.Errorf("%d actions are open, want both (%v)", open, err)
			}
		})
		t.Run("a tenant's session cannot change what an action says", func(t *testing.T) {
			for _, set := range []string{"reason = 'x'", "permissions = ARRAY['member:write']", "expires_at = expires_at + interval '1 hour'", "actor_id = 'someone'"} {
				if _, err := exec(admin, sessA, `UPDATE pandokos.support_action SET `+set+` WHERE id = $1::uuid`, actionA); sqlstate(err) != insufficientPrivilege {
					t.Errorf("UPDATE SET %s in the tenant's session: %v, want SQLSTATE %s", set, err, insufficientPrivilege)
				}
			}
			if _, err := exec(admin, sessA, `DELETE FROM pandokos.support_action WHERE id = $1::uuid`, actionA); sqlstate(err) != insufficientPrivilege {
				t.Errorf("deleting: %v", err)
			}
		})
		t.Run("the platform scope opens and closes", func(t *testing.T) {
			if _, err := exec(admin, platform, `INSERT INTO pandokos.support_action (tenant_id, actor_type, actor_id, reason, permissions, expires_at)
				VALUES ($1::uuid, 'user', '0192f0f0-0000-7000-8000-000000000001', 'by the platform', ARRAY['member:read'], now() + interval '1 hour')`, a.String()); err != nil {
				t.Errorf("the platform scope opening an action: %v", err)
			}
			if n, err := exec(admin, platform, `UPDATE pandokos.support_action SET closed_at = now(), closed_by = 'platform', closed_by_id = 'p' WHERE id = $1::uuid`, actionB); err != nil || n != 1 {
				t.Errorf("the platform scope closing: %d rows, %v", n, err)
			}
		})
		t.Run("a tenant's session ends its own action", func(t *testing.T) {
			if n, err := exec(admin, sessA, `UPDATE pandokos.support_action SET closed_at = now(), closed_by = 'tenant', closed_by_id = 'u', close_reason = 'done' WHERE id = $1::uuid`, actionA); err != nil || n != 1 {
				t.Errorf("tenant A's session ending its action: %d rows, %v", n, err)
			}
			var by string
			if err := super.QueryRow(ctx(t), `SELECT closed_by FROM pandokos.support_action WHERE id = $1::uuid`, actionA).Scan(&by); err != nil || by != "tenant" {
				t.Errorf("closed_by %q (%v)", by, err)
			}
		})
		t.Run("the gateway role cannot write tenant_role", func(t *testing.T) {
			for _, q := range []string{
				`INSERT INTO pandokos.tenant_role (tenant_id, name, permissions) VALUES ($1::uuid, 'from-gateway', ARRAY['member:read'])`,
				`UPDATE pandokos.tenant_role SET description = 'x'`,
				`DELETE FROM pandokos.tenant_role`,
			} {
				if _, err := exec(gateway, sessA, strings.ReplaceAll(q, "$1", "'"+a.String()+"'")); sqlstate(err) != insufficientPrivilege {
					t.Errorf("the gateway role running %q: %v, want SQLSTATE %s", q, err, insufficientPrivilege)
				}
			}
		})
	})
}

// TestRolesAndSupportAPI is T-888: the operations of sprint 4.3 across tenants.
func TestRolesAndSupportAPI(t *testing.T) {
	w := newAPIWorld(t)
	api := w.api
	plat := &apitest.Caller{API: api, Key: api.PlatformKey(), Name: "platform"}
	roleA := api.CustomRole(t, w.a.tenant.ID, "member:read", "role:read")
	actionA := apitest.OpenSupport(t, plat, w.a.tenant.ID, "member:read", "role:read")

	tenantSide := map[string]bool{"listRoles": true, "createRole": true, "getRole": true, "updateRole": true, "deleteRole": true, "deactivateRole": true, "activateRole": true,
		"listSupportActions": true, "getSupportAction": true, "endSupportAction": true}
	platformSide := map[string]bool{"listTenantSupportActions": true, "openSupportAction": true, "getTenantSupportAction": true, "closeSupportAction": true}

	memberOfB := api.MemberAs(t, w.b.tenant.ID, "owner")
	credentials := map[string]apitest.Doer{
		"B's key":     &apitest.Caller{API: api, Key: w.b.adminKey, Name: "owner of B"},
		"B's session": memberOfB,
	}

	// 1. Tenant B's credential on tenant A's path: 404, the same for what exists and for what does not.
	for name, who := range credentials {
		for _, op := range adminOps() {
			if !tenantSide[op.ID] {
				continue
			}
			real := w.a.params()
			real.Role, real.SupportAction = roleA, actionA
			missing := w.a.params()
			missing.Tenant = randomUUID(t, w) // a tenant that does not exist
			missing.Role, missing.SupportAction = apitest.NoRole, apitest.NoUser
			r1, r2 := apitest.Probe(t, who, op, real), apitest.Probe(t, who, op, missing)
			if r1.Status != 404 || r2.Status != 404 || r1.Problem() != "not_found" {
				t.Errorf("%s as %s: A's tenant gives %d, a missing tenant %d, want 404 not_found\n%s", op.ID, name, r1.Status, r2.Status, r1.Body)
			}
			if normalize(t, r1) != normalize(t, r2) {
				t.Errorf("%s as %s: A's tenant and a missing tenant differ:\n%s\n%s", op.ID, name, normalize(t, r1), normalize(t, r2))
			}
		}
		// In B's own tenant, A's role and A's action are not there.
		for _, op := range adminOps() {
			if !tenantSide[op.ID] || (!strings.Contains(op.Path, "{role}") && !strings.Contains(op.Path, "{action}")) {
				continue
			}
			real := w.b.params()
			real.Role, real.SupportAction = roleA, actionA
			missing := w.b.params()
			missing.Role, missing.SupportAction = apitest.NoRole, apitest.NoUser
			r1, r2 := apitest.Probe(t, who, op, real), apitest.Probe(t, who, op, missing)
			if r1.Status != 404 || r2.Status != 404 {
				t.Errorf("%s as %s: A's role or action in B's tenant gives %d, a missing one %d, want 404\n%s", op.ID, name, r1.Status, r2.Status, r1.Body)
			}
			if normalize(t, r1) != normalize(t, r2) {
				t.Errorf("%s as %s: A's resource and a missing one in B's tenant differ:\n%s\n%s", op.ID, name, normalize(t, r1), normalize(t, r2))
			}
		}
	}
	// A's role and A's action are untouched.
	if r := api.Owner(t, w.a.tenant.ID).Do(t, "GET", "/v1/tenants/"+w.a.tenant.ID+"/roles/"+roleA, nil, nil); r.Status != 200 {
		t.Errorf("A's role after the refused calls: %d %s", r.Status, r.Body)
	}
	if r := api.Owner(t, w.a.tenant.ID).Do(t, "GET", "/v1/tenants/"+w.a.tenant.ID+"/support-actions/"+actionA, nil, nil); r.Status != 200 {
		t.Errorf("A's support action after the refused calls: %d %s", r.Status, r.Body)
	}

	// 2. A support action in A gives nothing in B.
	for _, path := range []string{"/service-accounts", "/members", "/roles", "/virtual-keys"} {
		if r := plat.Do(t, "GET", "/v1/tenants/"+w.b.tenant.ID+path, nil, nil); !apitest.Refused(r) {
			t.Errorf("a platform key with an action in A reads B%s: %d %s, want 403 forbidden", path, r.Status, r.Body)
		}
		if r := plat.Do(t, "GET", "/v1/tenants/"+w.a.tenant.ID+path, nil, nil); r.Status != 200 && !apitest.Refused(r) {
			t.Errorf("the same in A%s: %d %s", path, r.Status, r.Body)
		}
	}
	for _, path := range []string{"/v1/tenants/" + w.b.tenant.ID + "/support-actions/" + actionA, "/v1/tenant-support/" + w.b.tenant.ID + "/actions/" + actionA} {
		var r *apitest.Response
		if strings.HasPrefix(path, "/v1/tenants/") {
			r = api.Owner(t, w.b.tenant.ID).Do(t, "GET", path, nil, nil)
		} else {
			r = plat.Do(t, "GET", path, nil, nil)
		}
		if r.Status != 404 {
			t.Errorf("GET %s (A's action in B's path): %d %s, want 404", path, r.Status, r.Body)
		}
	}
	if r := api.Owner(t, w.b.tenant.ID).Do(t, "POST", "/v1/tenants/"+w.b.tenant.ID+"/support-actions/"+actionA+"/end", map[string]any{}, nil); r.Status != 404 {
		t.Errorf("B's owner ending A's action through B's path: %d %s, want 404", r.Status, r.Body)
	}

	// 3. The platform-side operations with a tenant's credential are 403.
	for name, who := range credentials {
		for _, op := range adminOps() {
			if !platformSide[op.ID] {
				continue
			}
			for _, tid := range []string{w.a.tenant.ID, w.b.tenant.ID} {
				p := w.b.params()
				p.Tenant, p.SupportAction = tid, actionA
				if r := apitest.Probe(t, who, op, p); !apitest.Refused(r) {
					t.Errorf("%s as %s on tenant %s: %d %s, want 403 forbidden", op.ID, name, tid, r.Status, r.Body)
				}
			}
		}
	}
}

func adminOps() []adminapi.Operation { return apiOperations() }
