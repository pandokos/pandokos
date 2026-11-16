package isolation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
)

// query runs a statement as the role in the session, in its own transaction, and returns the rows
// as maps (an error rolls the transaction back).
func query(t *testing.T, conn *pgx.Conn, s session, sql string, args ...any) ([]map[string]any, error) {
	t.Helper()
	var out []map[string]any
	err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(c, sql, args...)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowToMap)
		return err
	})
	return out, err
}

func execIn(t *testing.T, conn *pgx.Conn, s session, sql string, args ...any) (int64, error) {
	t.Helper()
	var n int64
	err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(c, sql, args...)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}

func scalar(t *testing.T, super *pgx.Conn, sql string, args ...any) string {
	t.Helper()
	var s *string
	if err := super.QueryRow(ctx(t), sql, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	if s == nil {
		return "<null>"
	}
	return *s
}

var noSession = session{name: "none"}

// TestIssuerTables is T-585: the two tenant-scoped tables of migration 0008 pass the negative tests
// of the harness for every runtime role, in every kind of session, and the schema lint reports
// nothing with the migration applied.
func TestIssuerTables(t *testing.T) {
	d := dbtest.NewMigrated(t)
	super := d.Super(t)
	spec := DefaultSpec()

	findings, err := Lint(ctx(t), super, spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Errorf("lint %s %s: %s", f.Rule, f.Object, f.Detail)
	}
	for _, name := range []string{"trusted_issuer", "subject_mapping"} {
		if _, global := spec.Global[name]; global {
			t.Errorf("%s is tenant-scoped and is listed as a global table", name)
		}
		if _, ok := spec.Fixtures[name]; !ok {
			t.Errorf("%s has no fixture", name)
		}
		if _, ok := spec.Grants[name]; !ok {
			t.Errorf("%s has no grants in the spec", name)
		}
		var policies string
		if err := super.QueryRow(ctx(t), `SELECT COALESCE(string_agg(policyname, ',' ORDER BY policyname), '') FROM pg_catalog.pg_policies WHERE schemaname = 'pandokos' AND tablename = $1`, name).Scan(&policies); err != nil {
			t.Fatal(err)
		}
		if policies != "tenant_isolation" {
			t.Errorf("%s has the policies %q, want tenant_isolation", name, policies)
		}
	}
	scoped := map[string]bool{}
	for _, tbl := range tenantScopedTables(t, super, spec) {
		scoped[tbl] = true
	}
	if !scoped["trusted_issuer"] || !scoped["subject_mapping"] {
		t.Fatalf("the harness does not see the two tables as tenant-scoped: %v", scoped)
	}

	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		a, b := d.SeedTenant(t, "tenant-a"), d.SeedTenant(t, "tenant-b")
		super := d.Super(t)
		for _, tbl := range []string{"trusted_issuer", "subject_mapping"} {
			fx := spec.Fixtures[tbl]
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

		sessA := sessions(a)[0]
		t.Run("the privileges of each role", func(t *testing.T) {
			for _, tbl := range []string{"trusted_issuer", "subject_mapping"} {
				fx := spec.Fixtures[tbl]
				key := insertRow(t, super, fx, a)
				// The gateway reads its tenant's rows and writes nothing, in any session.
				gw := d.Connect(t, "gateway")
				rows, err := query(t, gw, sessA, `SELECT id::text AS id FROM `+table(tbl))
				if err != nil || len(rows) == 0 {
					t.Errorf("%s: the gateway read %d rows of A: %v", tbl, len(rows), err)
				}
				for _, sess := range sessions(a) {
					for what, sql := range map[string]string{
						"INSERT": fx.Insert,
						"UPDATE": `UPDATE ` + table(tbl) + ` SET ` + fx.Update,
						"DELETE": `DELETE FROM ` + table(tbl),
					} {
						var err error
						if what == "INSERT" {
							_, err = query(t, gw, sess, sql, a.String())
						} else {
							_, err = execIn(t, gw, sess, sql)
						}
						if sqlstate(err) != insufficientPrivilege {
							t.Errorf("%s: the gateway's %s in session %s: %v, want %s", tbl, what, sess.name, err, insufficientPrivilege)
						}
					}
				}
				// The worker has no privilege at all.
				wk := d.Connect(t, "worker")
				if _, err := query(t, wk, sessA, `SELECT count(*) FROM `+table(tbl)); sqlstate(err) != insufficientPrivilege {
					t.Errorf("%s: the worker's SELECT: %v, want %s", tbl, err, insufficientPrivilege)
				}
				// The admin role reads and, for a registration, changes its own rows.
				ad := d.Connect(t, "admin")
				if rows, err := query(t, ad, sessA, `SELECT id::text AS id FROM `+table(tbl)+` WHERE id::text = $1`, key); err != nil || len(rows) != 1 {
					t.Errorf("%s: the admin role read %d rows: %v", tbl, len(rows), err)
				}
				_ = key
			}
			// A mapping is immutable: no UPDATE for any role, whatever the column.
			ad := d.Connect(t, "admin")
			for _, col := range []string{"subject = 'x'", "description = 'x'", "service_account_id = service_account_id", "tenant_id = tenant_id"} {
				if _, err := execIn(t, ad, sessA, `UPDATE pandokos.subject_mapping SET `+col); sqlstate(err) != insufficientPrivilege {
					t.Errorf("the admin role's UPDATE of subject_mapping (%s): %v, want %s", col, err, insufficientPrivilege)
				}
			}
			// The tenant of a registration cannot be changed, and a row cannot be made for another tenant.
			if _, err := execIn(t, ad, sessA, `UPDATE pandokos.trusted_issuer SET tenant_id = $1::uuid`, b.String()); sqlstate(err) != insufficientPrivilege {
				t.Errorf("the admin role moved a registration to another tenant: %v", err)
			}
			if n, err := execIn(t, ad, sessA, `UPDATE pandokos.trusted_issuer SET description = 'changed by the admin role'`); err != nil || n < 1 {
				t.Errorf("the admin role's UPDATE of its own registrations: %d rows, %v", n, err)
			}
			if _, err := query(t, ad, sessA, spec.Fixtures["trusted_issuer"].Insert, b.String()); sqlstate(err) != insufficientPrivilege {
				t.Errorf("the admin role inserted a registration for tenant B in A's session: %v", err)
			}
			if _, err := query(t, ad, sessA, spec.Fixtures["trusted_issuer"].Insert, a.String()); err != nil {
				t.Errorf("the admin role's INSERT of a registration for A in A's session: %v", err)
			}
			if _, err := query(t, ad, noSession, spec.Fixtures["trusted_issuer"].Insert, a.String()); sqlstate(err) != insufficientPrivilege {
				t.Errorf("the admin role inserted a registration without a tenant session: %v", err)
			}
		})
	})
}

// TestIssuerFunctions is T-586: what the three functions the gateway role may execute return and
// refuse; the other roles cannot execute them.
func TestIssuerFunctions(t *testing.T) {
	d := dbtest.NewMigrated(t)
	super := d.Super(t)
	a, b := d.SeedTenant(t, "fn-a"), d.SeedTenant(t, "fn-b")
	sessA := sessions(a)[0]
	sessB := sessions(b)[0]
	const iss = "https://fn.example.test"
	ins := func(sql string, args ...any) string { return scalar(t, super, sql, args...) }
	tiA := ins(`INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience) VALUES ($1::uuid, 'ia', $2, 'aud-a') RETURNING id::text`, a.String(), iss)
	tiB := ins(`INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience) VALUES ($1::uuid, 'ib', $2, 'aud-b') RETURNING id::text`, b.String(), iss)
	piP := ins(`INSERT INTO pandokos.platform_issuer (name, issuer, audience, tenant_claim) VALUES ('ip', $1, 'aud-p', 'org') RETURNING id::text`, iss)
	if _, err := super.Exec(ctx(t), `INSERT INTO pandokos.platform_issuer_tenant (issuer_id, tenant_id, claim_value) VALUES ($1::uuid, $2::uuid, 'org-a'), ($1::uuid, $3::uuid, 'org-b')`, piP, a.String(), b.String()); err != nil {
		t.Fatal(err)
	}
	gw := d.Connect(t, "gateway")

	t.Run("issuer_lookup returns the exact pairs and nothing else", func(t *testing.T) {
		lookup := func(sess session, issuer any, audiences any) []map[string]any {
			t.Helper()
			rows, err := query(t, gw, sess, `SELECT scope, COALESCE(tenant_id::text, '') AS tenant_id, issuer_id::text AS issuer_id FROM pandokos.issuer_lookup($1::text, $2::text[])`, issuer, audiences)
			if err != nil {
				t.Fatalf("issuer_lookup(%v, %v): %v", issuer, audiences, err)
			}
			return rows
		}
		for _, sess := range []session{noSession, sessA, sessB} { // the answer does not depend on the session
			rows := lookup(sess, iss, []string{"aud-a"})
			if len(rows) != 1 || rows[0]["scope"] != "tenant" || rows[0]["tenant_id"] != a.String() || rows[0]["issuer_id"] != tiA {
				t.Errorf("session %s, A's pair: %v", sess.name, rows)
			}
		}
		if rows := lookup(noSession, iss, []string{"aud-p"}); len(rows) != 1 || rows[0]["scope"] != "platform" || rows[0]["tenant_id"] != "" || rows[0]["issuer_id"] != piP {
			t.Errorf("the platform-level pair: %v", rows)
		}
		if rows := lookup(noSession, iss, []string{"nobody", "aud-b", "nobody-else"}); len(rows) != 1 || rows[0]["issuer_id"] != tiB {
			t.Errorf("one known audience among unknown ones: %v", rows)
		}
		// Two audiences that match two registrations give two rows: the caller refuses the token.
		if rows := lookup(noSession, iss, []string{"aud-a", "aud-b"}); len(rows) != 2 {
			t.Errorf("two matching audiences: %v", rows)
		}
		if rows := lookup(noSession, iss, []string{"aud-a", "aud-a"}); len(rows) != 1 {
			t.Errorf("the same audience twice: %v", rows)
		}
		// 16 audiences are allowed, 17 are not, and neither is an empty list or NULL.
		sixteen := []string{"aud-a"}
		for i := 1; i < 16; i++ {
			sixteen = append(sixteen, fmt.Sprintf("x%d", i))
		}
		if rows := lookup(noSession, iss, sixteen); len(rows) != 1 {
			t.Errorf("16 audiences: %v", rows)
		}
		if rows := lookup(noSession, iss, append(sixteen, "x16")); len(rows) != 0 {
			t.Errorf("17 audiences: %v", rows)
		}
		if rows := lookup(noSession, iss, []string{}); len(rows) != 0 {
			t.Errorf("an empty list: %v", rows)
		}
		if rows := lookup(noSession, iss, nil); len(rows) != 0 {
			t.Errorf("NULL audiences: %v", rows)
		}
		if rows := lookup(noSession, nil, []string{"aud-a"}); len(rows) != 0 {
			t.Errorf("a NULL issuer: %v", rows)
		}
		// No pattern matches: the values are compared byte for byte.
		for _, c := range []struct{ issuer, audience string }{
			{iss + "%", "aud-a"}, {"https://fn.example.test_", "aud-a"}, {"https://%.example.test", "aud-a"}, {"%", "aud-a"}, {iss, "aud-%"}, {iss, "aud_a"}, {iss, "%"},
			{iss, ".*"}, {iss, "aud-a|aud-b"}, {iss + "/", "aud-a"}, {strings.ToUpper(iss), "aud-a"}, {iss, "AUD-A"}, {iss, "aud-a "}, {" " + iss, "aud-a"}, {"", ""}, {iss, ""},
		} {
			if rows := lookup(noSession, c.issuer, []string{c.audience}); len(rows) != 0 {
				t.Errorf("issuer %q audience %q matched: %v", c.issuer, c.audience, rows)
			}
		}
		// An array element that is NULL matches nothing and does not hide the others.
		if rows, err := query(t, gw, noSession, `SELECT issuer_id::text AS issuer_id FROM pandokos.issuer_lookup($1, ARRAY['aud-a', NULL]::text[])`, iss); err != nil || len(rows) != 1 {
			t.Errorf("a NULL element: %v %v", rows, err)
		}
	})

	t.Run("platform_issuer_tenant_lookup returns one ID or NULL", func(t *testing.T) {
		lookup := func(sess session, issuer, value any) string {
			t.Helper()
			rows, err := query(t, gw, sess, `SELECT COALESCE(pandokos.platform_issuer_tenant_lookup($1::uuid, $2::text)::text, '<null>') AS tenant_id`, issuer, value)
			if err != nil || len(rows) != 1 {
				t.Fatalf("platform_issuer_tenant_lookup(%v, %v): %v %v", issuer, value, rows, err)
			}
			return rows[0]["tenant_id"].(string)
		}
		for _, sess := range []session{noSession, sessA, sessB} { // the same in every session: the function is the way across
			if got := lookup(sess, piP, "org-a"); got != a.String() {
				t.Errorf("session %s: %s, want A", sess.name, got)
			}
			if got := lookup(sess, piP, "org-b"); got != b.String() {
				t.Errorf("session %s: %s, want B", sess.name, got)
			}
		}
		for name, c := range map[string][2]any{
			"an unknown value":                     {piP, "org-x"},
			"the value in another case":            {piP, "ORG-A"},
			"the tenant's ID instead of the value": {piP, a.String()},
			"an empty value":                       {piP, ""},
			"a NULL value":                         {piP, nil},
			"a NULL issuer":                        {nil, "org-a"},
			"a tenant-bound registration's ID":     {tiA, "org-a"},
			"an issuer that does not exist":        {"0192f0f0-0000-7000-8000-0000000000ff", "org-a"},
			"a pattern":                            {piP, "org-%"},
		} {
			if got := lookup(noSession, c[0], c[1]); got != "<null>" {
				t.Errorf("%s: %s, want NULL", name, got)
			}
		}
		// It cannot list: it takes one registration and one value and returns one scalar.
		var retset bool
		var rettype string
		if err := super.QueryRow(ctx(t), `SELECT proretset, prorettype::regtype::text FROM pg_catalog.pg_proc WHERE oid = 'pandokos.platform_issuer_tenant_lookup(uuid, text)'::regprocedure`).Scan(&retset, &rettype); err != nil {
			t.Fatal(err)
		}
		if retset || rettype != "uuid" {
			t.Errorf("the function returns a set: %v, type %s", retset, rettype)
		}
	})

	t.Run("record_token_auth_failure writes into the session's tenant only", func(t *testing.T) {
		const request = "0123456789abcdef0123456789abcdef"
		call := func(sess session, scope, issuer, reason, subject, req any) error {
			_, err := execIn(t, gw, sess, `SELECT pandokos.record_token_auth_failure($1::text, $2::uuid, $3::text, $4::text, $5::text)`, scope, issuer, reason, subject, req)
			return err
		}
		if err := call(sessA, "tenant", tiA, "bad_signature", "", request); err != nil {
			t.Fatal(err)
		}
		var tenantID, actor, action, resourceType, resourceID, outcome, req, detail string
		if err := super.QueryRow(ctx(t), `SELECT tenant_id::text, actor_type, action, resource_type, resource_id, outcome, request_id, detail::text FROM pandokos.audit_event WHERE request_id = $1`, request).
			Scan(&tenantID, &actor, &action, &resourceType, &resourceID, &outcome, &req, &detail); err != nil {
			t.Fatal(err)
		}
		if tenantID != a.String() || actor != "anonymous" || action != "auth.failure" || resourceType != "token_issuer" || resourceID != tiA || outcome != "failure" {
			t.Errorf("the row: %s %s %s %s %s %s", tenantID, actor, action, resourceType, resourceID, outcome)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(detail), &got); err != nil || len(got) != 3 || got["reason"] != "bad_signature" || got["listener"] != "inference" || got["issuer_scope"] != "tenant" {
			t.Errorf("detail %s: want exactly reason, listener and issuer_scope (no subject for an unverified token)", detail)
		}
		// A subject is recorded when given.
		const request2 = "fedcba9876543210fedcba9876543210"
		if err := call(sessA, "platform", piP, "subject_not_mapped", "spiffe://a/ns/ci", request2); err != nil {
			t.Fatal(err)
		}
		if err := super.QueryRow(ctx(t), `SELECT detail::text FROM pandokos.audit_event WHERE request_id = $1`, request2).Scan(&detail); err != nil {
			t.Fatal(err)
		}
		got = nil
		if err := json.Unmarshal([]byte(detail), &got); err != nil || got["subject"] != "spiffe://a/ns/ci" || got["issuer_scope"] != "platform" || len(got) != 4 {
			t.Errorf("detail %s", detail)
		}
		// Tenant B's log has nothing, whichever registration the row names.
		if n := scalar(t, super, `SELECT count(*)::text FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'auth.failure'`, b.String()); n != "0" {
			t.Errorf("tenant B has %s failure rows", n)
		}
		// A registration of B named in A's session still writes into A's log: the tenant is the session's.
		const request3 = "00112233445566778899aabbccddeeff"
		if err := call(sessA, "tenant", tiB, "bad_signature", "", request3); err != nil {
			t.Fatal(err)
		}
		if tid := scalar(t, super, `SELECT tenant_id::text FROM pandokos.audit_event WHERE request_id = $1`, request3); tid != a.String() {
			t.Errorf("the row went to %s", tid)
		}
		// The platform log is untouched, and the audit_event table is not writable by the role.
		if n := scalar(t, super, `SELECT count(*)::text FROM pandokos.platform_audit_event WHERE action = 'auth.failure'`); n != "0" {
			t.Errorf("%s rows in the platform log", n)
		}
		if _, err := execIn(t, gw, sessA, `INSERT INTO pandokos.audit_event (tenant_id, actor_type, action, resource_type, outcome) VALUES ($1::uuid, 'anonymous', 'auth.failure', 'x', 'failure')`, a.String()); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the gateway role inserted into audit_event: %v", err)
		}
	})

	t.Run("record_token_auth_failure refuses what is not well formed", func(t *testing.T) {
		const request = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		for name, c := range map[string][5]any{
			"another scope":            {"global", tiA, "bad_signature", "", request},
			"a NULL scope":             {nil, tiA, "bad_signature", "", request},
			"a NULL issuer":            {"tenant", nil, "bad_signature", "", request},
			"a NULL reason":            {"tenant", tiA, nil, "", request},
			"a reason in upper case":   {"tenant", tiA, "Bad_Signature", "", request},
			"a reason with a space":    {"tenant", tiA, "bad signature", "", request},
			"an empty reason":          {"tenant", tiA, "", "", request},
			"a reason of 41 letters":   {"tenant", tiA, strings.Repeat("a", 41), "", request},
			"a reason with a digit":    {"tenant", tiA, "bad1", "", request},
			"a NULL request ID":        {"tenant", tiA, "bad_signature", "", nil},
			"a short request ID":       {"tenant", tiA, "bad_signature", "", request[:31]},
			"a long request ID":        {"tenant", tiA, "bad_signature", "", request + "a"},
			"a request ID in upper":    {"tenant", tiA, "bad_signature", "", strings.ToUpper(request)},
			"a request ID not in hex":  {"tenant", tiA, "bad_signature", "", strings.Repeat("g", 32)},
			"a NULL subject":           {"tenant", tiA, "bad_signature", nil, request},
			"a subject of 256 chars":   {"tenant", tiA, "bad_signature", strings.Repeat("s", 256), request},
			"a subject with a newline": {"tenant", tiA, "bad_signature", "a\nb", request},
		} {
			_, err := execIn(t, gw, sessA, `SELECT pandokos.record_token_auth_failure($1::text, $2::uuid, $3::text, $4::text, $5::text)`, c[0], c[1], c[2], c[3], c[4])
			if err == nil {
				t.Errorf("%s: accepted", name)
			} else if code := sqlstate(err); code != checkViolation && code != "" {
				// a NULL issuer ID may also be refused by the argument handling of the function; any refusal is a refusal
				t.Logf("%s: %s", name, code)
			}
		}
		if n := scalar(t, super, `SELECT count(*)::text FROM pandokos.audit_event WHERE request_id IN ($1, $2)`, request, strings.ToUpper(request)); n != "0" {
			t.Errorf("%s rows were written by refused calls", n)
		}
		// A subject of 255 characters is fine.
		if _, err := execIn(t, gw, sessA, `SELECT pandokos.record_token_auth_failure('tenant', $1::uuid, 'bad_signature', $2, 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb')`, tiA, strings.Repeat("s", 255)); err != nil {
			t.Errorf("a subject of 255 characters: %v", err)
		}
	})

	t.Run("record_token_auth_failure fails outside a tenant session", func(t *testing.T) {
		const request = "cccccccccccccccccccccccccccccccc"
		for _, sess := range []session{noSession, {name: "empty", setting: "app.tenant_id", value: ""}, {name: "platform", setting: "app.scope", value: "platform"}} {
			if _, err := execIn(t, gw, sess, `SELECT pandokos.record_token_auth_failure('tenant', $1::uuid, 'bad_signature', '', $2)`, tiA, request); err == nil {
				t.Errorf("session %s: the failure was recorded without a tenant", sess.name)
			}
		}
		if n := scalar(t, super, `SELECT count(*)::text FROM pandokos.audit_event WHERE request_id = $1`, request); n != "0" {
			t.Errorf("%s rows without a tenant", n)
		}
	})

	t.Run("the admin and worker roles cannot execute the functions", func(t *testing.T) {
		for _, role := range []string{"admin", "worker"} {
			conn := d.Connect(t, role)
			for name, sql := range map[string]string{
				"issuer_lookup":                 `SELECT * FROM pandokos.issuer_lookup('` + iss + `', ARRAY['aud-a'])`,
				"platform_issuer_tenant_lookup": `SELECT pandokos.platform_issuer_tenant_lookup('` + piP + `', 'org-a')`,
				"record_token_auth_failure":     `SELECT pandokos.record_token_auth_failure('tenant', '` + tiA + `', 'bad_signature', '', 'dddddddddddddddddddddddddddddddd')`,
			} {
				if _, err := query(t, conn, sessA, sql); sqlstate(err) != insufficientPrivilege {
					t.Errorf("%s cannot execute %s: %v, want SQLSTATE %s", role, name, err, insufficientPrivilege)
				}
			}
		}
		// And the privileges are exactly the gateway's: nothing for PUBLIC.
		rows, err := super.Query(ctx(t), `SELECT p.proname, COALESCE((SELECT string_agg(r.rolname, ',' ORDER BY r.rolname) FROM pg_catalog.pg_roles r WHERE has_function_privilege(r.oid, p.oid, 'EXECUTE') AND r.rolname LIKE 'pandokos\_%'), '')
			FROM pg_catalog.pg_proc p WHERE p.pronamespace = 'pandokos'::regnamespace AND p.proname IN ('issuer_lookup', 'platform_issuer_tenant_lookup', 'record_token_auth_failure')`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			var name, who string
			if err := rows.Scan(&name, &who); err != nil {
				t.Fatal(err)
			}
			n++
			for _, role := range []string{"pandokos_admin", "pandokos_worker"} {
				if strings.Contains(who, role) {
					t.Errorf("%s may execute %s", role, name)
				}
			}
			if !strings.Contains(who, "pandokos_gateway") {
				t.Errorf("the gateway role may not execute %s (%s)", name, who)
			}
		}
		if n != 3 {
			t.Errorf("%d functions found", n)
		}
	})
}

// TestPlatformIssuerVisibility is T-587: what a tenant's session sees of the global tables of
// platform-level issuers, and who can write them.
func TestPlatformIssuerVisibility(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		super := d.Super(t)
		a, b := d.SeedTenant(t, "vis-a"), d.SeedTenant(t, "vis-b")
		sessA := sessions(a)[0]
		platform := session{name: "platform", setting: "app.scope", value: "platform"}
		p1 := scalar(t, super, `INSERT INTO pandokos.platform_issuer (name, issuer, audience, tenant_claim) VALUES ('vis-1', 'https://vis-1.example.test', 'aud', 'org') RETURNING id::text`)
		p2 := scalar(t, super, `INSERT INTO pandokos.platform_issuer (name, issuer, audience, tenant_claim) VALUES ('vis-2', 'https://vis-2.example.test', 'aud', 'org') RETURNING id::text`)
		if _, err := super.Exec(ctx(t), `INSERT INTO pandokos.platform_issuer_tenant (issuer_id, tenant_id, claim_value) VALUES ($1::uuid, $2::uuid, 'org-a'), ($1::uuid, $3::uuid, 'org-b'), ($4::uuid, $3::uuid, 'org-b2')`, p1, a.String(), b.String(), p2); err != nil {
			t.Fatal(err)
		}
		_ = scalar(t, super, `INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience) VALUES ($1::uuid, 'vis-t', 'https://vis-t.example.test', 'aud') RETURNING id::text`, a.String())

		for _, role := range []string{"gateway", "admin"} {
			conn := d.Connect(t, role)
			t.Run(role+": a tenant's session sees its own rows of the tenant list", func(t *testing.T) {
				rows, err := query(t, conn, sessA, `SELECT issuer_id::text AS issuer_id, tenant_id::text AS tenant_id, claim_value FROM pandokos.platform_issuer_tenant`)
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) != 1 || rows[0]["tenant_id"] != a.String() || rows[0]["claim_value"] != "org-a" || rows[0]["issuer_id"] != p1 {
					t.Errorf("rows: %v, want exactly A's entry of the first registration", rows)
				}
				// Not by a filter either: a lookup of B's claim value finds nothing.
				rows, err = query(t, conn, sessA, `SELECT claim_value FROM pandokos.platform_issuer_tenant WHERE claim_value LIKE 'org-b%' OR tenant_id = $1::uuid`, b.String())
				if err != nil || len(rows) != 0 {
					t.Errorf("B's entries: %v %v", rows, err)
				}
				// The registrations themselves are visible (their list is not), without a tenant too.
				for _, sess := range []session{sessA, noSession} {
					rows, err = query(t, conn, sess, `SELECT id::text AS id FROM pandokos.platform_issuer ORDER BY name`)
					if err != nil || len(rows) != 2 {
						t.Errorf("session %s: platform_issuer: %v %v", sess.name, rows, err)
					}
				}
				// Without a tenant, no entry of the list is visible to these roles.
				rows, err = query(t, conn, noSession, `SELECT 1 AS n FROM pandokos.platform_issuer_tenant`)
				if err != nil || len(rows) != 0 {
					t.Errorf("without a tenant session: %v %v", rows, err)
				}
			})
		}
		t.Run("the platform scope of the admin role sees and changes every entry", func(t *testing.T) {
			ad := d.Connect(t, "admin")
			rows, err := query(t, ad, platform, `SELECT tenant_id::text AS tenant_id FROM pandokos.platform_issuer_tenant ORDER BY claim_value`)
			if err != nil || len(rows) != 3 {
				t.Fatalf("%v %v", rows, err)
			}
			if _, err := query(t, ad, platform, `INSERT INTO pandokos.platform_issuer (name, issuer, audience, tenant_claim) VALUES ('vis-3', 'https://vis-3.example.test', 'aud', 'org') RETURNING id::text AS id`); err != nil {
				t.Errorf("platform scope INSERT of a registration: %v", err)
			}
			if n, err := execIn(t, ad, platform, `UPDATE pandokos.platform_issuer SET description = 'changed' WHERE id = $1::uuid`, p2); err != nil || n != 1 {
				t.Errorf("platform scope UPDATE of a registration: %d %v", n, err)
			}
			if n, err := execIn(t, ad, platform, `UPDATE pandokos.platform_issuer_tenant SET claim_value = 'org-b3' WHERE issuer_id = $1::uuid`, p2); err != nil || n != 1 {
				t.Errorf("platform scope UPDATE of a list entry: %d %v", n, err)
			}
			if n, err := execIn(t, ad, platform, `DELETE FROM pandokos.platform_issuer_tenant WHERE issuer_id = $1::uuid`, p2); err != nil || n != 1 {
				t.Errorf("platform scope DELETE of a list entry: %d %v", n, err)
			}
		})
		t.Run("outside the platform scope the admin role writes none of the global tables", func(t *testing.T) {
			ad := d.Connect(t, "admin")
			for _, sess := range []session{sessA, noSession, {name: "empty", setting: "app.tenant_id", value: ""}} {
				for what, sql := range map[string]string{
					"INSERT platform_issuer":        `INSERT INTO pandokos.platform_issuer (name, issuer, audience, tenant_claim) VALUES ('vis-x', 'https://vis-x.example.test', 'aud', 'org')`,
					"INSERT platform_issuer_tenant": `INSERT INTO pandokos.platform_issuer_tenant (issuer_id, tenant_id, claim_value) VALUES ('` + p1 + `', '` + b.String() + `', 'org-bb')`,
				} {
					if _, err := execIn(t, ad, sess, sql); sqlstate(err) != insufficientPrivilege {
						t.Errorf("session %s: %s: %v, want SQLSTATE %s", sess.name, what, err, insufficientPrivilege)
					}
				}
				for what, sql := range map[string]string{
					"UPDATE platform_issuer":        `UPDATE pandokos.platform_issuer SET description = 'x'`,
					"DELETE platform_issuer":        `DELETE FROM pandokos.platform_issuer`,
					"UPDATE platform_issuer_tenant": `UPDATE pandokos.platform_issuer_tenant SET claim_value = 'x'`,
					"DELETE platform_issuer_tenant": `DELETE FROM pandokos.platform_issuer_tenant`,
				} {
					n, err := execIn(t, ad, sess, sql)
					if err == nil && n != 0 {
						t.Errorf("session %s: %s changed %d rows", sess.name, what, n)
					} else if err != nil && sqlstate(err) != insufficientPrivilege {
						t.Errorf("session %s: %s: %v", sess.name, what, err)
					}
				}
			}
			if n := scalar(t, super, `SELECT count(*)::text FROM pandokos.platform_issuer WHERE name IN ('vis-1', 'vis-2') AND description = ''`); n != "1" {
				// vis-2 was changed by the platform scope above; vis-1 must be as it was.
				t.Errorf("registrations changed outside the platform scope: %s untouched of 2 (want 1: vis-2 was changed on purpose)", n)
			}
		})

		t.Run("no runtime role reads or writes issuer_directory", func(t *testing.T) {
			if n := scalar(t, super, `SELECT count(*)::text FROM pandokos.issuer_directory`); n == "0" {
				t.Fatal("the directory is empty: the test would prove nothing")
			}
			for _, role := range runtimeRoles {
				conn := d.Connect(t, role)
				for _, sess := range []session{sessA, noSession, platform} {
					for what, sql := range map[string]string{
						"SELECT": `SELECT * FROM pandokos.issuer_directory`,
						"INSERT": `INSERT INTO pandokos.issuer_directory (issuer, audience, scope, tenant_id, issuer_id) VALUES ('https://x.example.test', 'a', 'platform', NULL, gen_random_uuid())`,
						"UPDATE": `UPDATE pandokos.issuer_directory SET audience = 'x'`,
						"DELETE": `DELETE FROM pandokos.issuer_directory`,
					} {
						if _, err := execIn(t, conn, sess, sql); sqlstate(err) != insufficientPrivilege {
							t.Errorf("%s in session %s: %s: %v, want SQLSTATE %s", role, sess.name, what, err, insufficientPrivilege)
						}
					}
					if _, err := execIn(t, conn, sess, `TRUNCATE pandokos.issuer_directory`); sqlstate(err) != insufficientPrivilege {
						t.Errorf("%s: TRUNCATE issuer_directory: %v", role, err)
					}
				}
			}
		})

		t.Run("the gateway role cannot write any of the five tables", func(t *testing.T) {
			gw := d.Connect(t, "gateway")
			for _, sess := range []session{sessA, noSession, platform} {
				for _, tbl := range []string{"trusted_issuer", "subject_mapping", "platform_issuer", "platform_issuer_tenant", "issuer_directory"} {
					for what, sql := range map[string]string{
						"UPDATE":   `UPDATE ` + table(tbl) + ` SET ` + map[string]string{"trusted_issuer": "description = 'x'", "subject_mapping": "description = 'x'", "platform_issuer": "description = 'x'", "platform_issuer_tenant": "claim_value = 'x'", "issuer_directory": "audience = 'x'"}[tbl],
						"DELETE":   `DELETE FROM ` + table(tbl),
						"TRUNCATE": `TRUNCATE ` + table(tbl),
					} {
						if _, err := execIn(t, gw, sess, sql); sqlstate(err) != insufficientPrivilege {
							t.Errorf("gateway, session %s, %s %s: %v, want SQLSTATE %s", sess.name, what, tbl, err, insufficientPrivilege)
						}
					}
				}
			}
			for what, sql := range map[string]string{
				"INSERT trusted_issuer":         `INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience) VALUES ('` + a.String() + `', 'gw', 'https://gw.example.test', 'aud')`,
				"INSERT platform_issuer":        `INSERT INTO pandokos.platform_issuer (name, issuer, audience, tenant_claim) VALUES ('gw', 'https://gw.example.test', 'aud', 'org')`,
				"INSERT platform_issuer_tenant": `INSERT INTO pandokos.platform_issuer_tenant (issuer_id, tenant_id, claim_value) VALUES ('` + p1 + `', '` + b.String() + `', 'gw')`,
			} {
				for _, sess := range []session{sessA, platform} {
					if _, err := execIn(t, gw, sess, sql); sqlstate(err) != insufficientPrivilege {
						t.Errorf("gateway, session %s, %s: %v, want SQLSTATE %s", sess.name, what, err, insufficientPrivilege)
					}
				}
			}
		})

		t.Run("the worker role has no privilege on any of the five tables", func(t *testing.T) {
			wk := d.Connect(t, "worker")
			for _, tbl := range []string{"trusted_issuer", "subject_mapping", "platform_issuer", "platform_issuer_tenant", "issuer_directory"} {
				for _, sess := range []session{sessA, platform} {
					if _, err := query(t, wk, sess, `SELECT count(*) FROM `+table(tbl)); sqlstate(err) != insufficientPrivilege {
						t.Errorf("worker, session %s, SELECT %s: %v, want SQLSTATE %s", sess.name, tbl, err, insufficientPrivilege)
					}
				}
			}
		})
	})
}

// TestIssuerAPI is T-588: each operation of sprint 4.1 with the key of another tenant, with a key
// that lacks the permission, and with a tenant key on a platform operation; what a mapping may name;
// and what a tenant sees of platform-level registrations.
func TestIssuerAPI(t *testing.T) {
	w := newAPIWorld(t)
	apiA, apiB := w.a, w.b
	tenantOps := map[string]bool{"listTokenIssuers": true, "createTokenIssuer": true, "getTokenIssuer": true, "updateTokenIssuer": true, "deleteTokenIssuer": true,
		"listTenantPlatformIssuers": true, "listSubjectMappings": true, "createSubjectMapping": true, "getSubjectMapping": true, "deleteSubjectMapping": true}
	platformOps := map[string]bool{"listPlatformIssuers": true, "createPlatformIssuer": true, "getPlatformIssuer": true, "updatePlatformIssuer": true, "deletePlatformIssuer": true,
		"listPlatformIssuerTenants": true, "putPlatformIssuerTenant": true, "deletePlatformIssuerTenant": true}
	seen := 0
	for _, op := range adminapi.Operations {
		if !tenantOps[op.ID] && !platformOps[op.ID] {
			continue
		}
		seen++
		scope := scopeOf(t, op)
		switch {
		case tenantOps[op.ID]:
			if scope != authz.ScopeTenant {
				t.Errorf("%s has the scope %s, want tenant", op.ID, scope)
			}
		default:
			if scope != authz.ScopePlatform {
				t.Errorf("%s has the scope %s, want platform", op.ID, scope)
			}
		}
	}
	if seen != 18 {
		t.Fatalf("%d of the 18 operations of sprint 4.1 are in the API", seen)
	}

	// A key that holds no role of the tenant and one that holds the inference role.
	noRole := func(s side) string {
		c := w.api.Client(s.adminKey)
		sa, err := c.CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: s.tenant.ID, Body: adminapi.ServiceAccountCreate{Name: apitest.UniqueName("no-role")}})
		if err != nil {
			t.Fatal(err)
		}
		k, err := c.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: s.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: sa.ID}})
		if err != nil {
			t.Fatal(err)
		}
		return k.Secret
	}
	noRoleKey := noRole(apiA)

	t.Run("a key of tenant B on tenant A's path is a 404", func(t *testing.T) {
		before := w.api.SnapshotTenant(t, apiA.tenant.ID)
		for _, op := range adminapi.Operations {
			if !tenantOps[op.ID] {
				continue
			}
			real := w.fresh(t, apiA, op)
			missing := real
			missing.Tenant = randomUUID(t, w)
			cr, cm := apitest.CallFor(t, op, real), apitest.CallFor(t, op, missing)
			r1 := w.api.Do(t, cr.Method, cr.Path, apiB.adminKey, cr.Body, nil)
			r2 := w.api.Do(t, cm.Method, cm.Path, apiB.adminKey, cm.Body, nil)
			if r1.Status != 404 || !strings.Contains(string(r1.Body), `"code":"not_found"`) {
				t.Errorf("%s: B's key on A's path gives %d\n%s", op.ID, r1.Status, r1.Body)
			}
			if normalize(t, r1) != normalize(t, r2) {
				t.Errorf("%s: A's tenant and a tenant that does not exist answer differently to B's key:\n%s\n%s", op.ID, normalize(t, r1), normalize(t, r2))
			}
		}
		if after := w.api.SnapshotTenant(t, apiA.tenant.ID); after != before {
			t.Error("a call with B's key changed a row of tenant A")
		}
	})

	t.Run("a key without the permission is a 403", func(t *testing.T) {
		for _, op := range adminapi.Operations {
			if !tenantOps[op.ID] {
				continue
			}
			for name, key := range map[string]string{"no role": noRoleKey, "the inference role": apiA.inference.Secret} {
				call := apitest.CallFor(t, op, w.fresh(t, apiA, op))
				r := w.api.Do(t, call.Method, call.Path, key, call.Body, nil)
				if r.Status != 403 || !strings.Contains(string(r.Body), `"code":"forbidden"`) {
					t.Errorf("%s with a key of %s: status %d\n%s", op.ID, name, r.Status, r.Body)
				}
			}
		}
	})

	t.Run("a tenant key on a platform operation is a 403, the same for an existing and a missing resource", func(t *testing.T) {
		for _, op := range adminapi.Operations {
			if !platformOps[op.ID] {
				continue
			}
			real := apiA.params()
			missing := real
			missing.PlatformIssuer, missing.Tenant = randomUUID(t, w), randomUUID(t, w)
			cr, cm := apitest.CallFor(t, op, real), apitest.CallFor(t, op, missing)
			r1 := w.api.Do(t, cr.Method, cr.Path, apiA.adminKey, cr.Body, nil)
			r2 := w.api.Do(t, cm.Method, cm.Path, apiA.adminKey, cm.Body, nil)
			expect403(t, op, r1)
			expect403(t, op, r2)
			if normalize(t, r1) != normalize(t, r2) {
				t.Errorf("%s: differs for an existing and a missing resource:\n%s\n%s", op.ID, normalize(t, r1), normalize(t, r2))
			}
		}
		// A platform key on a tenant operation is no better (the existing tests cover every tenant operation; these ten are named here).
		for _, op := range adminapi.Operations {
			if !tenantOps[op.ID] {
				continue
			}
			call := apitest.CallFor(t, op, apiA.params())
			expect403(t, op, w.api.Do(t, call.Method, call.Path, w.api.PlatformKey(), call.Body, nil))
		}
	})

	t.Run("a mapping names a registration and a service account of the tenant only", func(t *testing.T) {
		platformOnlyB := w.api.SeedPlatformIssuer(t)
		w.api.SeedPlatformIssuerTenant(t, platformOnlyB, apiB.tenant.ID)
		op := adminapi.Operation{}
		for _, o := range adminapi.Operations {
			if o.ID == "createSubjectMapping" {
				op = o
			}
		}
		create := func(scope, issuer, sa string) *apitest.Response {
			p := apiA.params()
			p.ServiceAccount = sa
			c := apitest.CallFor(t, op, p)
			body := c.Body.(map[string]any)
			body["issuer"] = map[string]any{"scope": scope, "id": issuer}
			return w.api.Do(t, c.Method, c.Path, apiA.adminKey, body, nil)
		}
		before := w.api.SnapshotTenant(t, apiB.tenant.ID)
		for name, c := range map[string]struct{ real, missing *apitest.Response }{
			"a registration of tenant B":                                  {create("tenant", apiB.tokenIssuer, apiA.sa.ID), create("tenant", randomUUID(t, w), apiA.sa.ID)},
			"a platform-level registration that does not list the tenant": {create("platform", platformOnlyB, apiA.sa.ID), create("platform", randomUUID(t, w), apiA.sa.ID)},
			"a service account of tenant B":                               {create("tenant", apiA.tokenIssuer, apiB.sa.ID), create("tenant", apiA.tokenIssuer, randomUUID(t, w))},
		} {
			if (c.real.Status != 404 && c.real.Status != 400) || c.real.Status != c.missing.Status {
				t.Errorf("%s: status %d, a missing one %d, want the same 404 or 400\n%s", name, c.real.Status, c.missing.Status, c.real.Body)
				continue
			}
			if normalize(t, c.real) != normalize(t, c.missing) {
				t.Errorf("%s: the answer differs from the one for a resource that does not exist:\n%s\n%s", name, normalize(t, c.real), normalize(t, c.missing))
			}
			for _, secret := range []string{apiB.tenant.ID, apiB.tokenIssuer, apiB.sa.ID, platformOnlyB} {
				if strings.Contains(string(c.real.Body), secret) {
					t.Errorf("%s: the answer holds %s", name, secret)
				}
			}
		}
		if after := w.api.SnapshotTenant(t, apiB.tenant.ID); after != before {
			t.Error("a mapping request of tenant A changed a row of tenant B")
		}
		// The control: A's own registration, service account and a platform-level registration that lists A.
		if r := create("tenant", apiA.tokenIssuer, apiA.sa.ID); r.Status != 201 {
			t.Errorf("a mapping of A's own registration and service account: %d\n%s", r.Status, r.Body)
		}
		if r := create("platform", apiA.platformIssuer, apiA.sa.ID); r.Status != 201 {
			t.Errorf("a mapping of a platform-level registration that lists A: %d\n%s", r.Status, r.Body)
		}
	})

	t.Run("a tenant sees of platform-level registrations only those that list it, and only its own claim value", func(t *testing.T) {
		both := w.api.SeedPlatformIssuer(t)
		w.api.SeedPlatformIssuerTenant(t, both, apiA.tenant.ID)
		w.api.SeedPlatformIssuerTenant(t, both, apiB.tenant.ID)
		onlyB := w.api.SeedPlatformIssuer(t)
		w.api.SeedPlatformIssuerTenant(t, onlyB, apiB.tenant.ID)
		super := w.api.Super(t)
		if _, err := super.Exec(ctx(t), `UPDATE pandokos.platform_issuer_tenant SET claim_value = CASE WHEN tenant_id = $2::uuid THEN 'org-of-a' ELSE 'org-of-b-secret' END WHERE issuer_id = $1::uuid`, both, apiA.tenant.ID); err != nil {
			t.Fatal(err)
		}
		var onlyBName string
		if err := super.QueryRow(ctx(t), `SELECT name FROM pandokos.platform_issuer WHERE id = $1::uuid`, onlyB).Scan(&onlyBName); err != nil {
			t.Fatal(err)
		}
		r := w.api.Do(t, "GET", "/v1/tenants/"+apiA.tenant.ID+"/platform-issuers", apiA.adminKey, nil, nil)
		if r.Status != 200 {
			t.Fatalf("status %d\n%s", r.Status, r.Body)
		}
		var list struct {
			Items []struct {
				ID         string `json:"id"`
				ClaimValue string `json:"claim_value"`
			} `json:"items"`
		}
		if err := json.Unmarshal(r.Body, &list); err != nil {
			t.Fatal(err)
		}
		found := map[string]string{}
		for _, it := range list.Items {
			found[it.ID] = it.ClaimValue
		}
		if found[both] != "org-of-a" {
			t.Errorf("the registration that lists A and B shows %q for A, want org-of-a", found[both])
		}
		if _, ok := found[onlyB]; ok {
			t.Error("a registration that lists B only is shown to A")
		}
		for _, secret := range []string{apiB.tenant.ID, apiB.tenant.Name, "org-of-b-secret", onlyB, onlyBName} {
			if strings.Contains(string(r.Body), secret) {
				t.Errorf("the list shown to A holds %q:\n%s", secret, r.Body)
			}
		}
		// B's view is its own; and the platform's list of a registration still shows both tenants.
		rb := w.api.Do(t, "GET", "/v1/tenants/"+apiB.tenant.ID+"/platform-issuers", apiB.adminKey, nil, nil)
		if rb.Status != 200 || strings.Contains(string(rb.Body), apiA.tenant.ID) || strings.Contains(string(rb.Body), "org-of-a") || !strings.Contains(string(rb.Body), onlyB) {
			t.Errorf("B's list: %d\n%s", rb.Status, rb.Body)
		}
		rp := w.api.Do(t, "GET", "/v1/platform-issuers/"+both+"/tenants", w.api.PlatformKey(), nil, nil)
		if rp.Status != 200 || !strings.Contains(string(rp.Body), apiA.tenant.ID) || !strings.Contains(string(rp.Body), apiB.tenant.ID) {
			t.Errorf("the platform's view of the list: %d\n%s", rp.Status, rp.Body)
		}
		// A tenant cannot read a platform-level registration directly, whatever it lists.
		for _, id := range []string{both, onlyB} {
			expect403Op(t, w.api.Do(t, "GET", "/v1/platform-issuers/"+id, apiA.adminKey, nil, nil))
		}
		// A's own registrations are not B's: the list of token issuers shows A's only.
		rl := w.api.Do(t, "GET", "/v1/tenants/"+apiA.tenant.ID+"/token-issuers", apiA.adminKey, nil, nil)
		if rl.Status != 200 || strings.Contains(string(rl.Body), apiB.tokenIssuer) || !strings.Contains(string(rl.Body), apiA.tokenIssuer) {
			t.Errorf("A's token issuers: %d\n%s", rl.Status, rl.Body)
		}
		rm := w.api.Do(t, "GET", "/v1/tenants/"+apiA.tenant.ID+"/subject-mappings", apiA.adminKey, nil, nil)
		if rm.Status != 200 || strings.Contains(string(rm.Body), apiB.mapping) || strings.Contains(string(rm.Body), apiB.tenant.ID) || !strings.Contains(string(rm.Body), apiA.mapping) {
			t.Errorf("A's subject mappings: %d\n%s", rm.Status, rm.Body)
		}
	})

	t.Run("the registrations and mappings of tenant B are not found by A by ID", func(t *testing.T) {
		for path, name := range map[string]string{
			"/v1/tenants/" + apiA.tenant.ID + "/token-issuers/" + apiB.tokenIssuer: "a registration",
			"/v1/tenants/" + apiA.tenant.ID + "/subject-mappings/" + apiB.mapping:  "a mapping",
		} {
			real := w.api.Do(t, "GET", path, apiA.adminKey, nil, nil)
			missing := w.api.Do(t, "GET", strings.Replace(strings.Replace(path, apiB.tokenIssuer, randomUUID(t, w), 1), apiB.mapping, randomUUID(t, w), 1), apiA.adminKey, nil, nil)
			if real.Status != 404 || normalize(t, real) != normalize(t, missing) {
				t.Errorf("%s of B in A's path: %d, a missing one %d\n%s", name, real.Status, missing.Status, real.Body)
			}
		}
		for _, method := range []string{"PATCH", "DELETE"} {
			p := "/v1/tenants/" + apiA.tenant.ID + "/token-issuers/" + apiB.tokenIssuer
			var body any
			if method == "PATCH" {
				body = map[string]any{"description": "taken over"}
			}
			if r := w.api.Do(t, method, p, apiA.adminKey, body, nil); r.Status != 404 {
				t.Errorf("%s a registration of B in A's path: %d\n%s", method, r.Status, r.Body)
			}
		}
		if r := w.api.Do(t, "DELETE", "/v1/tenants/"+apiA.tenant.ID+"/subject-mappings/"+apiB.mapping, apiA.adminKey, nil, nil); r.Status != 404 {
			t.Errorf("DELETE a mapping of B in A's path: %d\n%s", r.Status, r.Body)
		}
	})
}

func expect403Op(t *testing.T, r *apitest.Response) {
	t.Helper()
	if r.Status != 403 || !strings.Contains(string(r.Body), `"code":"forbidden"`) {
		t.Errorf("status %d, want 403 forbidden\n%s", r.Status, r.Body)
	}
}
