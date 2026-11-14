package isolation

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

// TestAuditIsAppendOnly is T-280 (changed in sprint 2.2: the worker role inserts platform audit
// rows in platform scope, because the price importer and its refresh job audit what they change,
// D-17).
func TestAuditIsAppendOnly(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a := d.SeedTenant(t, "tenant-a")
	super := d.Super(t)
	const (
		tenantInsert   = `INSERT INTO pandokos.audit_event (tenant_id, actor_type, action, resource_type, outcome, request_id) VALUES ($1::uuid, 'platform', 'test.insert', 'test', 'success', 'req-280')`
		platformInsert = `INSERT INTO pandokos.platform_audit_event (actor_type, action, resource_type, outcome, request_id) VALUES ('host', 'test.insert', 'test', 'success', 'req-280')`
	)
	if _, err := super.Exec(ctx(t), tenantInsert, a.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := super.Exec(ctx(t), platformInsert); err != nil {
		t.Fatal(err)
	}

	tables := []struct {
		name    string
		session session
		insert  string
		args    []any
	}{
		{"audit_event", sessions(a)[0], tenantInsert, []any{a.String()}},
		{"platform_audit_event", sessions(a)[3], platformInsert, nil},
	}
	for _, tb := range tables {
		stmts := map[string]string{
			"UPDATE":   `UPDATE pandokos.` + tb.name + ` SET reason = 'changed'`,
			"DELETE":   `DELETE FROM pandokos.` + tb.name,
			"TRUNCATE": `TRUNCATE pandokos.` + tb.name,
			"SELECT":   `SELECT count(*) FROM pandokos.` + tb.name,
		}
		// Each runtime role gets 42501 on UPDATE, DELETE, TRUNCATE and SELECT, in every session.
		for _, role := range runtimeRoles {
			conn := d.Connect(t, role)
			for _, sess := range sessions(a) {
				for op, sql := range stmts {
					err := inSession(t, conn, sess, func(c context.Context, tx pgx.Tx) error {
						_, err := tx.Exec(c, sql)
						return err
					})
					if sqlstate(err) != insufficientPrivilege {
						t.Errorf("%s: %s as %s in session %s: %v, want SQLSTATE %s", tb.name, op, role, sess.name, err, insufficientPrivilege)
					}
				}
			}
		}

		// The admin role inserts (the positive control); nobody else does.
		admin := d.Connect(t, "admin")
		if err := inSession(t, admin, tb.session, func(c context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(c, tb.insert, tb.args...)
			return err
		}); err != nil {
			t.Errorf("%s: the admin role cannot insert: %v", tb.name, err)
		}
		for _, role := range []string{"gateway", "worker"} {
			err := inSession(t, d.Connect(t, role), tb.session, func(c context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(c, tb.insert, tb.args...)
				return err
			})
			switch {
			case role == "worker" && tb.name == "platform_audit_event":
				if err != nil {
					t.Errorf("%s: the worker role cannot insert in platform scope: %v", tb.name, err)
				}
				// Only in platform scope: any other session is refused by the policy.
				for _, other := range []session{sessions(a)[0], sessions(a)[1], sessions(a)[2]} {
					err := inSession(t, d.Connect(t, role), other, func(c context.Context, tx pgx.Tx) error {
						_, err := tx.Exec(c, tb.insert, tb.args...)
						return err
					})
					if sqlstate(err) != insufficientPrivilege {
						t.Errorf("%s: the worker role inserted in session %s: %v", tb.name, other.name, err)
					}
				}
			case sqlstate(err) != insufficientPrivilege:
				t.Errorf("%s: %s inserted: %v", tb.name, role, err)
			}
		}
		// The identity and the time of a row are the database's: the admin role cannot choose them.
		cols := "tenant_id, "
		vals := "$1::uuid, "
		var args []any
		if tb.name == "audit_event" {
			args = []any{a.String()}
		} else {
			cols, vals = "", ""
		}
		for _, col := range []struct{ name, value string }{{"id", "pandokos.uuid_v7()"}, {"occurred_at", "now()"}} {
			err := inSession(t, admin, tb.session, func(c context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(c, `INSERT INTO pandokos.`+tb.name+` (`+cols+col.name+`, actor_type, action, resource_type, outcome) VALUES (`+vals+col.value+`, 'platform', 'test.x', 'test', 'success')`, args...)
				return err
			})
			if sqlstate(err) != insufficientPrivilege {
				t.Errorf("%s: the admin role chose %s: %v", tb.name, col.name, err)
			}
		}
		// The database stamps the row itself.
		var id string
		var skew float64
		if err := super.QueryRow(ctx(t), `SELECT id::text, abs(extract(epoch FROM now() - occurred_at)) FROM pandokos.`+tb.name+` WHERE request_id = 'req-280' LIMIT 1`).Scan(&id, &skew); err != nil || id == "" || skew > 120 {
			t.Errorf("%s: id %q, skew %v, %v", tb.name, id, skew, err)
		}

		// The superuser and the owner are stopped by the triggers.
		for op, sql := range map[string]string{
			"UPDATE":   `UPDATE pandokos.` + tb.name + ` SET reason = 'changed'`,
			"DELETE":   `DELETE FROM pandokos.` + tb.name,
			"TRUNCATE": `TRUNCATE pandokos.` + tb.name,
		} {
			if _, err := super.Exec(ctx(t), sql); sqlstate(err) != restrictViolation {
				t.Errorf("%s: %s as superuser: %v, want SQLSTATE %s", tb.name, op, err, restrictViolation)
			}
			owner := d.Connect(t, "owner")
			err := inSession(t, owner, tb.session, func(c context.Context, tx pgx.Tx) error {
				tag, err := tx.Exec(c, sql)
				if err == nil && tag.RowsAffected() != 0 {
					t.Errorf("%s: %s as owner changed %d rows", tb.name, op, tag.RowsAffected())
				}
				return err
			})
			switch {
			case sqlstate(err) == restrictViolation:
			case err == nil && tb.name == "platform_audit_event" && op != "TRUNCATE":
				// Forced row-level security hides the rows of this table from the owner: there is
				// no row for the trigger to stop, and none is changed.
			default:
				t.Errorf("%s: %s as owner: %v, want SQLSTATE %s", tb.name, op, err, restrictViolation)
			}
		}
		var n int
		if err := super.QueryRow(ctx(t), `SELECT count(*) FROM pandokos.`+tb.name+` WHERE request_id = 'req-280'`).Scan(&n); err != nil || n < 2 {
			t.Errorf("%s: %d rows survive, %v", tb.name, n, err)
		}
	}
}

// TestRecordAuthFailure is T-443: the function through which the gateway role writes one kind of
// audit row (migration 0005, sprint 3.1 section 5.10).
func TestRecordAuthFailure(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a := d.SeedTenant(t, "tenant-a")
	b := d.SeedTenant(t, "tenant-b")
	super := d.Super(t)
	const (
		call      = `SELECT pandokos.record_auth_failure($1, $2, $3)`
		keyID     = "tabcdefghijklmnop"
		requestID = "0123456789abcdef0123456789abcdef"
	)
	rows := func(tenantID tenant.ID) int {
		t.Helper()
		var n int
		if err := super.QueryRow(ctx(t), `SELECT count(*) FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'auth.failure'`, tenantID.String()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	invoke := func(conn *pgx.Conn, s session, key, reason, request string) error {
		return inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(c, call, key, reason, request)
			return err
		})
	}
	tenantA := sessions(a)[0]
	gateway := d.Connect(t, "gateway")

	t.Run("the gateway role writes the row of the tenant of its session", func(t *testing.T) {
		if err := invoke(gateway, tenantA, keyID, "revoked", requestID); err != nil {
			t.Fatal(err)
		}
		var actor, resType, resID, outcome, request, key string
		var detail string
		if err := super.QueryRow(ctx(t), `SELECT actor_type, resource_type, resource_id, outcome, request_id, key_id, detail::text FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'auth.failure'`, a.String()).
			Scan(&actor, &resType, &resID, &outcome, &request, &key, &detail); err != nil {
			t.Fatal(err)
		}
		if actor != "anonymous" || resType != "virtual_key" || resID != keyID || outcome != "failure" || request != requestID || key != keyID {
			t.Errorf("the row: %s %s %s %s %s %s", actor, resType, resID, outcome, request, key)
		}
		var ok bool
		if err := super.QueryRow(ctx(t), `SELECT $1::jsonb = '{"reason": "revoked", "listener": "inference"}'::jsonb`, detail).Scan(&ok); err != nil || !ok {
			t.Errorf("detail %s (%v), want exactly reason and listener", detail, err)
		}
		if err := invoke(gateway, sessions(b)[0], keyID, "expired", requestID); err != nil {
			t.Fatal(err)
		}
		if rows(a) != 1 || rows(b) != 1 {
			t.Errorf("rows: tenant a %d, tenant b %d, want one each: the function writes for the tenant of the session", rows(a), rows(b))
		}
	})

	t.Run("outside a tenant session the row is refused", func(t *testing.T) {
		before := rows(a) + rows(b)
		for _, s := range sessions(a)[1:] {
			if err := invoke(gateway, s, keyID, "revoked", requestID); sqlstate(err) != insufficientPrivilege {
				t.Errorf("session %s: %v, want SQLSTATE %s", s.name, err, insufficientPrivilege)
			}
		}
		if rows(a)+rows(b) != before {
			t.Error("a row was written outside a tenant session")
		}
	})

	t.Run("nobody else may execute it", func(t *testing.T) {
		before := rows(a)
		for _, role := range []string{"admin", "worker"} {
			if err := invoke(d.Connect(t, role), tenantA, keyID, "revoked", requestID); sqlstate(err) != insufficientPrivilege {
				t.Errorf("the %s role: %v, want SQLSTATE %s", role, err, insufficientPrivilege)
			}
		}
		// A login that belongs to no role, given the right to connect and to see the schema, so
		// that what stops it is the privilege on the function, which only the gateway role holds.
		login := d.CreateLogin(t, "nobody", "")
		for _, stmt := range []string{
			"GRANT CONNECT ON DATABASE " + pgx.Identifier{d.Name}.Sanitize() + " TO " + pgx.Identifier{login.User}.Sanitize(),
			"GRANT USAGE ON SCHEMA pandokos TO " + pgx.Identifier{login.User}.Sanitize(),
		} {
			if _, err := super.Exec(ctx(t), stmt); err != nil {
				t.Fatal(err)
			}
		}
		nobody := d.ConnectWith(t, login)
		if err := invoke(nobody, tenantA, keyID, "revoked", requestID); sqlstate(err) != insufficientPrivilege {
			t.Errorf("a login with no role membership: %v, want SQLSTATE %s", err, insufficientPrivilege)
		}
		var acl string
		if err := super.QueryRow(ctx(t), `SELECT proacl::text FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'pandokos' AND p.proname = 'record_auth_failure'`).Scan(&acl); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(acl, "pandokos_gateway=X/") || strings.Contains(acl, "{=X") || strings.Contains(acl, ",=X") {
			t.Errorf("the privileges of the function: %s (PUBLIC must not execute it)", acl)
		}
		if rows(a) != before {
			t.Error("a role that may not execute the function wrote a row")
		}
	})

	t.Run("malformed arguments are refused", func(t *testing.T) {
		before := rows(a)
		bad := []struct{ name, key, reason, request string }{
			{"a key ID without the t", "xabcdefghijklmnop", "revoked", requestID},
			{"a key ID that is too short", "tabcdef", "revoked", requestID},
			{"a key ID with a digit outside the alphabet", "tabcdefghijklmn018", "revoked", requestID},
			{"a key that looks like a whole key", "pdk_tabcdefghijklmnop_synthetic", "revoked", requestID},
			{"an empty key ID", "", "revoked", requestID},
			{"an upper-case reason", keyID, "Revoked", requestID},
			{"a reason with a space", keyID, "wrong secret", requestID},
			{"a reason with digits", keyID, "pdk_abc123_secret", requestID},
			{"an empty reason", keyID, "", requestID},
			{"a reason of 41 characters", keyID, strings.Repeat("a", 41), requestID},
			{"a request ID of 31 characters", keyID, "revoked", requestID[:31]},
			{"a request ID in upper case", keyID, "revoked", strings.ToUpper(requestID)},
			{"a request ID that is a statement", keyID, "revoked", "0123456789abcdef'; DROP TABLE x;--"},
			{"a request ID with a newline", keyID, "revoked", requestID[:31] + "\n"},
		}
		for _, c := range bad {
			if err := invoke(gateway, tenantA, c.key, c.reason, c.request); sqlstate(err) != checkViolation {
				t.Errorf("%s: %v, want SQLSTATE %s", c.name, err, checkViolation)
			}
		}
		if rows(a) != before {
			t.Error("a malformed call wrote a row")
		}
		if err := invoke(gateway, tenantA, keyID, strings.Repeat("a", 40), requestID); err != nil {
			t.Errorf("a reason of 40 characters: %v", err)
		}
	})

	t.Run("the gateway role still holds no privilege on the audit table", func(t *testing.T) {
		for op, sql := range map[string]string{
			"INSERT": `INSERT INTO pandokos.audit_event (tenant_id, actor_type, action, resource_type, outcome, request_id) VALUES ($1::uuid, 'anonymous', 'auth.failure', 'virtual_key', 'failure', 'req-443')`,
			"SELECT": `SELECT count(*) FROM pandokos.audit_event`,
		} {
			err := inSession(t, d.Connect(t, "gateway"), tenantA, func(c context.Context, tx pgx.Tx) error {
				var err error
				if op == "INSERT" {
					_, err = tx.Exec(c, sql, a.String())
				} else {
					_, err = tx.Exec(c, sql)
				}
				return err
			})
			if sqlstate(err) != insufficientPrivilege {
				t.Errorf("%s as the gateway role: %v, want SQLSTATE %s", op, err, insufficientPrivilege)
			}
		}
	})

	t.Run("the lint has no finding", func(t *testing.T) {
		findings, err := Lint(ctx(t), super, DefaultSpec())
		if err != nil {
			t.Fatal(err)
		}
		if len(findings) != 0 {
			t.Errorf("findings: %v", findings)
		}
	})
}
