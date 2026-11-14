package isolation

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

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
