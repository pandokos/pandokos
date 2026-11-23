package isolation

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
		// Each runtime role gets 42501 on UPDATE, DELETE and TRUNCATE, in every session, and the
		// gateway and worker roles on SELECT too. Since sprint 6.2 the admin role reads the log of
		// its tenant's session and, for the platform's log, in platform scope (AUD-05); in every
		// other session it sees no row.
		for _, role := range runtimeRoles {
			conn := d.Connect(t, role)
			for _, sess := range sessions(a) {
				if role == "admin" {
					var n int
					err := inSession(t, conn, sess, func(c context.Context, tx pgx.Tx) error {
						return tx.QueryRow(c, stmts["SELECT"]).Scan(&n)
					})
					if err != nil {
						t.Errorf("%s: SELECT as admin in session %s: %v", tb.name, sess.name, err)
					} else if own := sess.name == tb.session.name; own && n < 1 || !own && n != 0 {
						t.Errorf("%s: the admin role sees %d rows in session %s (its own session: %v)", tb.name, n, sess.name, own)
					}
				}
				for op, sql := range stmts {
					if op == "SELECT" && role == "admin" {
						continue
					}
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
		// The identity, the time and the place in the chain of a row are the database's: the admin role cannot choose them.
		cols := "tenant_id, "
		vals := "$1::uuid, "
		var args []any
		if tb.name == "audit_event" {
			args = []any{a.String()}
		} else {
			cols, vals = "", ""
		}
		for _, col := range []struct{ name, value string }{{"id", "pandokos.uuid_v7()"}, {"occurred_at", "now()"},
			// Sprint 6.2: the place in the chain and the hash are the database's too.
			{"seq", "1"}, {"prev_hash", "decode(repeat('00', 32), 'hex')"}, {"hash", "decode(repeat('00', 32), 'hex')"}} {
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

// TestRecordUnlimitedTraffic is T-696: pandokos.record_unlimited_traffic runs for the gateway role
// in a tenant session only; however many calls, instances and repetitions there are, one row per
// tenant and day is left in each log; arguments are checked; the gateway role still has no
// privilege on either log; the owner's policy on the platform log admits this one action only.
func TestRecordUnlimitedTraffic(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		super := d.Super(t)
		a, b := d.SeedTenant(t, "unl-a"), d.SeedTenant(t, "unl-b")
		sessA, sessB := sessions(a)[0], session{name: "tenant-b", setting: "app.tenant_id", value: b.String()}
		platform := session{name: "platform", setting: "app.scope", value: "platform"}
		today := time.Now().UTC().Format("2006-01-02")
		day := func(offset int) string { return time.Now().UTC().AddDate(0, 0, offset).Format("2006-01-02") }
		const reqID = "0123456789abcdef0123456789abcdef"

		call := func(conn *pgx.Conn, s session, day, zone, provider, model, request string) (bool, error) {
			rows, err := query(t, conn, s, `SELECT pandokos.record_unlimited_traffic($1::date, $2, $3, $4, $5) AS first`, day, zone, provider, model, request)
			if err != nil {
				return false, err
			}
			return rows[0]["first"].(bool), nil
		}
		count := func(tbl, where string, args ...any) string {
			return scalar(t, super, `SELECT count(*)::text FROM pandokos.`+tbl+` WHERE action = 'limit.unlimited_traffic' `+where, args...)
		}

		t.Run("the first call writes a row in each log, a repeated call none", func(t *testing.T) {
			gw := d.Connect(t, "gateway")
			if first, err := call(gw, sessA, today, "Europe/Rome", "openai", "gpt-4o", reqID); err != nil || !first {
				t.Fatalf("the first call: %v %v", first, err)
			}
			if first, err := call(gw, sessA, today, "Europe/Rome", "anthropic", "claude", "fedcba9876543210fedcba9876543210"); err != nil || first {
				t.Errorf("a repeated call wrote a row: %v %v", first, err)
			}
			if n := count("audit_event", `AND tenant_id = $1::uuid`, a.String()); n != "1" {
				t.Errorf("%s rows in the tenant's log, want 1", n)
			}
			if n := count("platform_audit_event", `AND resource_id = $1`, a.String()); n != "1" {
				t.Errorf("%s rows in the platform log, want 1", n)
			}
			var actor, name, resource, request string
			var detail []byte
			if err := super.QueryRow(ctx(t), `SELECT actor_type, actor_name, resource_type, request_id, detail FROM pandokos.audit_event WHERE action = 'limit.unlimited_traffic' AND tenant_id = $1::uuid`,
				a.String()).Scan(&actor, &name, &resource, &request, &detail); err != nil {
				t.Fatal(err)
			}
			var det map[string]any
			if err := json.Unmarshal(detail, &det); err != nil {
				t.Fatal(err)
			}
			if actor != "platform" || name != "gateway" || resource != "tenant" || request != reqID ||
				det["day"] != today || det["time_zone"] != "Europe/Rome" || det["provider"] != "openai" || det["model"] != "gpt-4o" || len(det) != 4 {
				t.Errorf("row: %s %s %s %s %s", actor, name, resource, request, detail)
			}
			if n := scalar(t, super, `SELECT resource_id FROM pandokos.platform_audit_event WHERE action = 'limit.unlimited_traffic' AND resource_id = $1`, a.String()); n != a.String() {
				t.Errorf("the platform row names %s", n)
			}
		})

		t.Run("two tenants do not collide, two days do not either", func(t *testing.T) {
			gw := d.Connect(t, "gateway")
			if first, err := call(gw, sessB, today, "UTC", "openai", "gpt-4o", reqID); err != nil || !first {
				t.Errorf("tenant B, same day: %v %v", first, err)
			}
			if first, err := call(gw, sessA, day(1), "UTC", "openai", "gpt-4o", reqID); err != nil || !first {
				t.Errorf("tenant A, next day: %v %v", first, err)
			}
			if n := count("audit_event", ""); n != "3" {
				t.Errorf("%s rows in the tenants' logs, want 3", n)
			}
			if n := count("platform_audit_event", ""); n != "3" {
				t.Errorf("%s rows in the platform log, want 3", n)
			}
		})

		t.Run("sixteen concurrent calls leave one row", func(t *testing.T) {
			c := d.SeedTenant(t, "unl-c")
			var wg sync.WaitGroup
			var firsts atomic.Int32
			for i := 0; i < 16; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					conn := d.Connect(t, "gateway")
					if first, err := call(conn, sessions(c)[0], today, "UTC", "openai", "gpt-4o", reqID); err != nil {
						t.Errorf("concurrent call: %v", err)
					} else if first {
						firsts.Add(1)
					}
				}()
			}
			wg.Wait()
			if firsts.Load() != 1 {
				t.Errorf("%d calls say they wrote the row, want 1", firsts.Load())
			}
			if n := count("audit_event", `AND tenant_id = $1::uuid`, c.String()); n != "1" {
				t.Errorf("%s rows in the tenant's log", n)
			}
			if n := count("platform_audit_event", `AND resource_id = $1`, c.String()); n != "1" {
				t.Errorf("%s rows in the platform log", n)
			}
		})

		t.Run("a day outside the window and malformed arguments are refused", func(t *testing.T) {
			gw := d.Connect(t, "gateway")
			for what, args := range map[string][5]string{
				"three days ago":           {day(-3), "UTC", "openai", "gpt-4o", reqID},
				"three days ahead":         {day(3), "UTC", "openai", "gpt-4o", reqID},
				"a zone with a space":      {today, "Europe Rome", "openai", "gpt-4o", reqID},
				"an empty zone":            {today, "", "openai", "gpt-4o", reqID},
				"an upper-case provider":   {today, "UTC", "OpenAI", "gpt-4o", reqID},
				"an empty provider":        {today, "UTC", "", "gpt-4o", reqID},
				"a model with a space":     {today, "UTC", "openai", "gpt 4", reqID},
				"a request ID of 31":       {today, "UTC", "openai", "gpt-4o", reqID[:31]},
				"a request ID in capitals": {today, "UTC", "openai", "gpt-4o", strings.ToUpper(reqID)},
			} {
				if _, err := call(gw, sessA, args[0], args[1], args[2], args[3], args[4]); sqlstate(err) != checkViolation {
					t.Errorf("%s: %v, want SQLSTATE %s", what, err, checkViolation)
				}
			}
			// An empty model is the one that is allowed: a request without one.
			if first, err := call(gw, sessA, day(2), "UTC", "openai", "", reqID); err != nil || !first {
				t.Errorf("a request without a model: %v %v", first, err)
			}
		})

		t.Run("only in a tenant session, only for the gateway role", func(t *testing.T) {
			for _, s := range []session{noSession, platform, {name: "empty", setting: "app.tenant_id", value: ""}} {
				if _, err := call(d.Connect(t, "gateway"), s, today, "UTC", "openai", "gpt-4o", reqID); sqlstate(err) != checkViolation {
					t.Errorf("gateway in session %s: %v, want SQLSTATE %s", s.name, err, checkViolation)
				}
			}
			for _, role := range []string{"admin", "worker"} {
				if _, err := call(d.Connect(t, role), sessA, day(1), "UTC", "openai", "gpt-4o", reqID); sqlstate(err) != insufficientPrivilege {
					t.Errorf("%s: %v, want SQLSTATE %s", role, err, insufficientPrivilege)
				}
			}
		})

		t.Run("the gateway role has no privilege on either log", func(t *testing.T) {
			gw := d.Connect(t, "gateway")
			for _, tbl := range []string{"audit_event", "platform_audit_event"} {
				for _, s := range []session{sessA, platform, noSession} {
					if _, err := query(t, gw, s, `SELECT count(*) FROM pandokos.`+tbl); sqlstate(err) != insufficientPrivilege {
						t.Errorf("SELECT %s in session %s: %v", tbl, s.name, err)
					}
					if _, err := execIn(t, gw, s, `INSERT INTO pandokos.`+tbl+` (actor_type, action, resource_type, outcome, request_id) VALUES ('platform', 'limit.unlimited_traffic', 'tenant', 'success', 'x')`); sqlstate(err) != insufficientPrivilege {
						t.Errorf("INSERT into %s in session %s: %v", tbl, s.name, err)
					}
				}
			}
		})

		t.Run("the owner's policy on the platform log admits this action only", func(t *testing.T) {
			owner := d.Connect(t, "owner")
			if _, err := owner.Exec(ctx(t), "SET ROLE pandokos_owner"); err != nil {
				t.Fatal(err)
			}
			const ins = `INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, resource_id, outcome, request_id, detail)
				VALUES ('platform', 'gateway', $1, 'tenant', 'x', 'success', 'r', '{}')`
			if _, err := execIn(t, owner, noSession, ins, "tenant.create"); sqlstate(err) != insufficientPrivilege {
				t.Errorf("the owner inserted another action: %v, want SQLSTATE %s", err, insufficientPrivilege)
			}
			if _, err := execIn(t, owner, noSession, ins, "limit.unlimited_traffic"); err != nil {
				t.Errorf("the owner's insert of this action: %v", err)
			}
			rows, err := query(t, owner, noSession, `SELECT DISTINCT action FROM pandokos.platform_audit_event`)
			if err != nil || len(rows) != 1 || rows[0]["action"] != "limit.unlimited_traffic" {
				t.Errorf("the owner sees %v (%v), want this action only", rows, err)
			}
		})
	})
}
