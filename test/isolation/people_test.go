package isolation

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

var peopleTables = []string{"app_user", "local_account", "user_identity", "user_session", "platform_role_assignment", "membership", "membership_role",
	"membership_directory", "signin_throttle", "oidc_login", "idempotency_key"}

// TestMembershipTables is T-777: the harness's negative tests on the two tenant-scoped tables of
// sprint 4.2, for every runtime role, and the lint with nothing to report.
func TestMembershipTables(t *testing.T) {
	t.Parallel()
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
		scoped := map[string]bool{}
		for _, tbl := range tenantScopedTables(t, super, spec) {
			scoped[tbl] = true
		}
		for _, tbl := range []string{"membership", "membership_role"} {
			if !scoped[tbl] {
				t.Fatalf("%s is not among the tenant-scoped tables of the catalogue", tbl)
			}
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
							ensureRow(t, super, tbl, fx, a, &keyA)
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

// people is the world of the next tests: two tenants, a user each of them manages, a user the
// platform manages and is a member of both, a federated user, a session each, throttle rows.
type people struct {
	d                     *dbtest.Database
	a, b                  tenant.ID
	plat, ua, ub          string
	fed                   string
	platS, uaS, ubS, fedS string
}

func seedPeople(t *testing.T, d *dbtest.Database) *people {
	t.Helper()
	p := &people{d: d, a: d.SeedTenant(t, "tenant-a"), b: d.SeedTenant(t, "tenant-b")}
	super := d.Super(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := super.Exec(ctx(t), q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	scan := func(q string, args ...any) string {
		t.Helper()
		var s string
		if err := super.QueryRow(ctx(t), q, args...).Scan(&s); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
		return s
	}
	const phc = "$argon2id$v=19$m=19456,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	user := func(signIn string, tid any, username string) string {
		id := scan(`INSERT INTO pandokos.app_user (sign_in, tenant_id) VALUES ($1, $2::uuid) RETURNING id::text`, signIn, tid)
		if username != "" {
			exec(`INSERT INTO pandokos.local_account (user_id, username, password_hash) VALUES ($1::uuid, $2, $3)`, id, username, phc)
		}
		return id
	}
	p.plat = user("local", nil, "plat.person")
	p.ua = user("local", p.a.String(), "a.person")
	p.ub = user("local", p.b.String(), "b.person")
	p.fed = user("oidc", nil, "")
	exec(`INSERT INTO pandokos.user_identity (user_id, issuer, subject) VALUES ($1::uuid, 'https://idp.example.test', 'fed-1')`, p.fed)
	exec(`INSERT INTO pandokos.platform_role_assignment (user_id, role) VALUES ($1::uuid, 'platform-admin')`, p.plat)
	for _, m := range []struct {
		tid  tenant.ID
		user string
	}{{p.a, p.plat}, {p.b, p.plat}, {p.a, p.ua}, {p.b, p.ub}} {
		exec(`INSERT INTO pandokos.membership (tenant_id, user_id) VALUES ($1::uuid, $2::uuid)`, m.tid.String(), m.user)
		exec(`INSERT INTO pandokos.membership_role (tenant_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'tenant-admin')`, m.tid.String(), m.user)
	}
	session := func(user, n string) string {
		return scan(`INSERT INTO pandokos.user_session (token_hash, user_id, method, expires_at) VALUES (sha256($2::bytea), $1::uuid, 'local', now() + interval '1 hour') RETURNING id::text`, user, "token-"+n)
	}
	p.platS, p.uaS, p.ubS, p.fedS = session(p.plat, "p"), session(p.ua, "a"), session(p.ub, "b"), session(p.fed, "f")
	exec(`INSERT INTO pandokos.signin_throttle (key, kind, failures) VALUES (sha256('t1'::bytea), 'account', 2), (sha256('t2'::bytea), 'source', 5)`)
	exec(`INSERT INTO pandokos.oidc_login (state_hash, expires_at) VALUES (sha256('o1'::bytea), now() + interval '5 minutes')`)
	return p
}

func tenantSession(id tenant.ID) session { return session{"tenant", "app.tenant_id", id.String()} }

var platformSession = session{"platform", "app.scope", "platform"}

func count(t *testing.T, conn *pgx.Conn, s session, tbl string) int {
	t.Helper()
	var n int
	err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
		return tx.QueryRow(c, `SELECT count(*) FROM pandokos.`+tbl).Scan(&n)
	})
	if err != nil {
		t.Fatalf("count %s as %s: %v", tbl, s.name, err)
	}
	return n
}

func affected(t *testing.T, conn *pgx.Conn, s session, sql string, args ...any) (int64, error) {
	t.Helper()
	var n int64
	err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(c, sql, args...)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}

// TestPeopleTablesAreAdminOnly is T-778: the gateway and the worker role have no privilege on the
// tables and functions of migration 0013, whatever the session; neither has any role on the directory.
func TestPeopleTablesAreAdminOnly(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	a := d.SeedTenant(t, "tenant-a")
	super := d.Super(t)
	firstColumn := func(tbl string) string {
		var c string
		if err := super.QueryRow(ctx(t), `SELECT column_name FROM information_schema.columns WHERE table_schema = 'pandokos' AND table_name = $1 ORDER BY ordinal_position LIMIT 1`, tbl).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	for _, role := range []string{"gateway", "worker"} {
		conn := d.Connect(t, role)
		for _, sess := range append(sessions(a), session{"tenant-a-platform", "app.scope", "platform"}) {
			for _, tbl := range peopleTables {
				col := pgx.Identifier{firstColumn(tbl)}.Sanitize()
				tn := table(tbl)
				for name, sql := range map[string]string{
					"select": `SELECT count(*) FROM ` + tn, "insert": `INSERT INTO ` + tn + ` DEFAULT VALUES`,
					"update": `UPDATE ` + tn + ` SET ` + col + ` = ` + col, "delete": `DELETE FROM ` + tn, "truncate": `TRUNCATE ` + tn,
					"select-for-update": `SELECT ` + col + ` FROM ` + tn + ` FOR UPDATE`,
				} {
					_, err := affected(t, conn, sess, sql)
					if sqlstate(err) != insufficientPrivilege {
						t.Errorf("%s/%s/%s %s: %v, want SQLSTATE %s", role, sess.name, tbl, name, err, insufficientPrivilege)
					}
				}
			}
			// The two functions that people call: refused for want of the privilege to execute them.
			for _, sql := range []string{`SELECT pandokos.user_tenants('0192f0f0-0000-7000-8000-000000000001')`, `SELECT pandokos.platform_user_lookup('0192f0f0-0000-7000-8000-000000000001', NULL)`,
				`SELECT pandokos.platform_user_lookup(NULL, 'someone')`} {
				if _, err := affected(t, conn, sess, sql); sqlstate(err) != insufficientPrivilege {
					t.Errorf("%s/%s %s: %v, want SQLSTATE %s", role, sess.name, sql, err, insufficientPrivilege)
				}
			}
			// The three trigger functions cannot be used as functions by anyone: refused, with the
			// privilege error or with the error that a trigger function cannot be called.
			for _, fn := range []string{"membership_guard", "membership_directory_sync", "platform_role_assignment_guard"} {
				_, err := affected(t, conn, sess, `SELECT pandokos.`+fn+`()`)
				if st := sqlstate(err); st != insufficientPrivilege && st != "0A000" {
					t.Errorf("%s/%s %s(): %v, want a refusal", role, sess.name, fn, err)
				}
			}
		}
	}
	// No runtime role has any privilege on the directory: not even the admin role.
	for _, role := range runtimeRoles {
		conn := d.Connect(t, role)
		for _, sess := range sessions(a) {
			if _, err := affected(t, conn, sess, `SELECT count(*) FROM pandokos.membership_directory`); sqlstate(err) != insufficientPrivilege {
				t.Errorf("%s/%s reads the directory: %v", role, sess.name, err)
			}
		}
	}
}

// TestUserVisibility is T-779: who sees and writes which rows, as the admin role, and the two guards.
func TestUserVisibility(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	p := seedPeople(t, d)
	conn := d.Connect(t, "admin")

	want := []struct {
		sess                                                                        session
		users, accounts, sessions, identities, throttle, roles, memberships, logins int
	}{
		{platformSession, 4, 3, 4, 1, 2, 1, 0, 1},    // the platform scope sees no membership: those are the tenants'
		{tenantSession(p.a), 2, 2, 1, 0, 0, 0, 2, 0}, // the user A manages and its platform-managed member
		{tenantSession(p.b), 2, 2, 1, 0, 0, 0, 2, 0},
		{session{"none", "", ""}, 0, 0, 0, 0, 0, 0, 0, 0},
		{session{"empty", "app.tenant_id", ""}, 0, 0, 0, 0, 0, 0, 0, 0},
	}
	for _, w := range want {
		got := []int{count(t, conn, w.sess, "app_user"), count(t, conn, w.sess, "local_account"), count(t, conn, w.sess, "user_session"),
			count(t, conn, w.sess, "user_identity"), count(t, conn, w.sess, "signin_throttle"), count(t, conn, w.sess, "platform_role_assignment"),
			count(t, conn, w.sess, "membership"), count(t, conn, w.sess, "oidc_login")}
		exp := []int{w.users, w.accounts, w.sessions, w.identities, w.throttle, w.roles, w.memberships, w.logins}
		if fmt.Sprint(got) != fmt.Sprint(exp) {
			t.Errorf("%s sees users, accounts, sessions, identities, throttle rows, platform roles, memberships, sign-in transactions: %v, want %v", w.sess.name, got, exp)
		}
	}
	// Which users: A's session sees ua and the platform user, not ub, not the federated user.
	names := func(s session) string {
		var out []string
		err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(c, `SELECT COALESCE(a.username, 'federated') FROM pandokos.app_user u LEFT JOIN pandokos.local_account a ON a.user_id = u.id`)
			if err != nil {
				return err
			}
			out, err = pgx.CollectRows(rows, pgx.RowTo[string])
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	if got := names(tenantSession(p.a)); got != "a.person,plat.person" {
		t.Errorf("tenant A sees %s", got)
	}
	if got := names(tenantSession(p.b)); got != "b.person,plat.person" {
		t.Errorf("tenant B sees %s", got)
	}

	// Tenant A's session writes the users it manages, and only them.
	sa := tenantSession(p.a)
	if n, err := affected(t, conn, sa, `UPDATE pandokos.app_user SET display_name = 'changed'`); err != nil || n != 1 {
		t.Errorf("update of the users: %d rows, %v, want only the one A manages", n, err)
	}
	if n, err := affected(t, conn, sa, `UPDATE pandokos.local_account SET must_change = true`); err != nil || n != 1 {
		t.Errorf("update of the accounts: %d rows, %v, want only the one A manages", n, err)
	}
	if n, err := affected(t, conn, sa, `DELETE FROM pandokos.user_session`); err != nil || n != 1 {
		t.Errorf("delete of the sessions: %d rows, %v, want only the one of the user A manages", n, err)
	}
	if n, err := affected(t, conn, sa, `INSERT INTO pandokos.app_user (sign_in, tenant_id) VALUES ('local', $1::uuid)`, p.a.String()); err != nil || n != 1 {
		t.Errorf("insert of a user A manages: %d %v", n, err)
	}
	for name, tid := range map[string]any{"the platform": nil, "tenant B": p.b.String()} {
		if _, err := affected(t, conn, sa, `INSERT INTO pandokos.app_user (sign_in, tenant_id) VALUES ('local', $1::uuid)`, tid); sqlstate(err) != insufficientPrivilege {
			t.Errorf("insert of a user managed by %s from A's session: %v, want SQLSTATE %s", name, err, insufficientPrivilege)
		}
	}
	if _, err := affected(t, conn, sa, `INSERT INTO pandokos.user_identity (user_id, issuer, subject) VALUES ($1::uuid, 'https://x.example.test', 's')`, p.fed); sqlstate(err) != insufficientPrivilege {
		t.Errorf("insert of an identity from a tenant's session: %v", err)
	}
	if _, err := affected(t, conn, sa, `INSERT INTO pandokos.platform_role_assignment (user_id, role) VALUES ($1::uuid, 'platform-admin')`, p.fed); sqlstate(err) != insufficientPrivilege {
		t.Errorf("insert of a platform role from a tenant's session: %v", err)
	}
	if _, err := affected(t, conn, sa, `DELETE FROM pandokos.app_user`); sqlstate(err) != insufficientPrivilege {
		t.Errorf("delete of users: %v (users are never deleted)", err)
	}
	// Another tenant's session and an unscoped session write nothing of A's.
	for _, s := range []session{tenantSession(p.b), {"none", "", ""}} {
		if n, err := affected(t, conn, s, `UPDATE pandokos.app_user SET display_name = 'hijacked' WHERE id = $1::uuid`, p.ua); err != nil || n != 0 {
			t.Errorf("%s: update of A's user: %d rows, %v", s.name, n, err)
		}
	}
	// A cannot reach a user that B manages, nor make it a member (the guard).
	if _, err := affected(t, conn, tenantSession(p.b), `INSERT INTO pandokos.membership (tenant_id, user_id) VALUES ($1::uuid, $2::uuid)`, p.b.String(), p.ua); sqlstate(err) != checkViolation {
		t.Errorf("a user that A manages made a member of B: %v, want SQLSTATE %s", err, checkViolation)
	}
	if _, err := affected(t, conn, platformSession, `INSERT INTO pandokos.platform_role_assignment (user_id, role) VALUES ($1::uuid, 'platform-admin')`, p.ub); sqlstate(err) != checkViolation {
		t.Errorf("a platform role for a user that B manages: %v, want SQLSTATE %s", err, checkViolation)
	}
	if n, err := affected(t, conn, platformSession, `INSERT INTO pandokos.platform_role_assignment (user_id, role) VALUES ($1::uuid, 'platform-admin')`, p.fed); err != nil || n != 1 {
		t.Errorf("a platform role for a federated user: %d %v", n, err)
	}
}

// TestPeopleFunctions is T-780: the definer functions return only what they are given a name for.
func TestPeopleFunctions(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	p := seedPeople(t, d)
	conn := d.Connect(t, "admin")
	userTenants := func(s session, user string) ([]string, error) {
		var out []string
		err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(c, `SELECT t::text FROM pandokos.user_tenants($1::uuid) t`, user)
			if err != nil {
				return err
			}
			out, err = pgx.CollectRows(rows, pgx.RowTo[string])
			return err
		})
		return out, err
	}

	want := []string{p.a.String(), p.b.String()}
	sort.Strings(want)
	if got, err := userTenants(platformSession, p.plat); err != nil || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("user_tenants in the platform scope: %v %v, want %v", got, err, want)
	}
	if got, err := userTenants(platformSession, p.ua); err != nil || len(got) != 1 || got[0] != p.a.String() {
		t.Errorf("user_tenants of a user a tenant manages: %v %v", got, err)
	}
	if got, err := userTenants(platformSession, p.fed); err != nil || len(got) != 0 {
		t.Errorf("user_tenants of a user with no membership: %v %v", got, err)
	}
	for _, s := range []session{tenantSession(p.a), {"none", "", ""}, {"empty", "app.tenant_id", ""}} {
		if got, err := userTenants(s, p.plat); sqlstate(err) != insufficientPrivilege || len(got) != 0 {
			t.Errorf("user_tenants outside the platform scope (%s): %v %v, want SQLSTATE %s", s.name, got, err, insufficientPrivilege)
		}
	}

	lookup := func(s session, id, username any) any {
		t.Helper()
		var out *string
		err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
			return tx.QueryRow(c, `SELECT pandokos.platform_user_lookup($1::uuid, $2::text)::text`, id, username).Scan(&out)
		})
		if err != nil {
			t.Fatalf("platform_user_lookup: %v", err)
		}
		if out == nil {
			return nil
		}
		return *out
	}
	sa := tenantSession(p.a)
	for name, c := range map[string]struct {
		id, username any
		want         any
	}{
		"a platform-managed user by ID":        {p.plat, nil, p.plat},
		"a platform-managed user by username":  {nil, "plat.person", p.plat},
		"a federated user by ID":               {p.fed, nil, p.fed},
		"a user a tenant manages, by ID":       {p.ua, nil, nil},
		"a user a tenant manages, by username": {nil, "a.person", nil},
		"a user another tenant manages":        {p.ub, nil, nil},
		"an ID that does not exist":            {"0192f0f0-0000-7000-8000-0000000000ff", nil, nil},
		"a username that does not exist":       {nil, "nobody", nil},
		"both an ID and a username":            {p.plat, "plat.person", nil},
		"neither":                              {nil, nil, nil},
		"a pattern in place of a username":     {nil, "%", nil},
		"a username that is the empty string":  {nil, "", nil},
	} {
		if got := lookup(sa, c.id, c.username); got != c.want {
			t.Errorf("platform_user_lookup, %s: %v, want %v", name, got, c.want)
		}
	}
	// It answers in a tenant's session only.
	for _, s := range []session{platformSession, {"none", "", ""}, {"empty", "app.tenant_id", ""}} {
		if got := lookup(s, p.plat, nil); got != nil {
			t.Errorf("platform_user_lookup in the session %s: %v, want nothing", s.name, got)
		}
	}
	// It takes one ID or one username and returns one ID: it cannot list.
	var setOf bool
	if err := d.Super(t).QueryRow(ctx(t), `SELECT proretset FROM pg_catalog.pg_proc WHERE proname = 'platform_user_lookup' AND pronamespace = 'pandokos'::regnamespace`).Scan(&setOf); err != nil || setOf {
		t.Errorf("platform_user_lookup returns a set (%v, %v)", setOf, err)
	}
	var n int
	if err := d.Super(t).QueryRow(ctx(t), `SELECT pronargs FROM pg_catalog.pg_proc WHERE proname = 'platform_user_lookup' AND pronamespace = 'pandokos'::regnamespace`).Scan(&n); err != nil || n != 2 {
		t.Errorf("platform_user_lookup takes %d arguments (%v), want 2", n, err)
	}
}
