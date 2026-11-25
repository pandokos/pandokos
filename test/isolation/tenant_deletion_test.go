package isolation

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/test/dbtest"
)

// TestTenantDeletionTable is T-978: who may write and read the request of a deletion, and what the
// table refuses to everybody.
func TestTenantDeletionTable(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	active, suspended, running := d.SeedTenant(t, "active-one"), d.SeedTenant(t, "suspended-one"), d.SeedTenant(t, "running-one")
	admin, worker, gateway := d.Connect(t, "admin"), d.Connect(t, "worker"), d.Connect(t, "gateway")
	platform := platformSession

	commit := func(conn *pgx.Conn, s session, sql string, args ...any) error {
		t.Helper()
		return inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(c, sql, args...)
			return err
		})
	}
	const insertRequest = `INSERT INTO pandokos.tenant_deletion (tenant_id, reason, requested_by_type, requested_by_id, requested_by_name, request_id, not_before)
		VALUES ($1::uuid, 'because', 'platform', 'pkeyabc', 'ops', 'req-1', now() + interval '1 hour')`

	// A request is inserted for a suspended tenant only; the request does not suspend by itself.
	if err := commit(admin, platform, insertRequest, active.String()); sqlstate(err) != restrictViolation || constraintOf(err) != "tenant_deletion_suspended" {
		t.Errorf("a request for an active tenant: %v, want SQLSTATE %s tenant_deletion_suspended", err, restrictViolation)
	}
	if err := commit(admin, platform, `UPDATE pandokos.tenant SET state = 'suspended' WHERE id = $1::uuid`, suspended.String()); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if err := commit(admin, platform, insertRequest, suspended.String()); err != nil {
		t.Fatalf("a request for a suspended tenant: %v", err)
	}
	if st := dbText(t, d, `SELECT state || '/' || step FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, suspended.String()); st != "scheduled/begin" {
		t.Errorf("a new request is %s, want scheduled/begin", st)
	}

	// The columns the admin role may not write: the state, the step, the salt, the cursor.
	for _, col := range []string{"state", "step", "salt", "started_at", "settle_at", "passes", "kek_versions", "removed", "audit", "completion_seq", "usage_id"} {
		sql := `INSERT INTO pandokos.tenant_deletion (tenant_id, reason, requested_by_type, not_before, ` + col + `) SELECT $1::uuid, 'x', 'platform', now(), NULL`
		if err := commit(admin, platform, sql, running.String()); sqlstate(err) != insufficientPrivilege {
			t.Errorf("an insert that sets %s: %v, want SQLSTATE %s", col, err, insufficientPrivilege)
		}
	}
	// What it may not read: the salt and the cursor of the ledger.
	for _, col := range []string{"salt", "usage_started_at", "usage_id"} {
		err := inSession(t, admin, platform, func(c context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(c, `SELECT `+col+` FROM pandokos.tenant_deletion`)
			return err
		})
		if sqlstate(err) != insufficientPrivilege {
			t.Errorf("the admin role reads %s: %v, want SQLSTATE %s", col, err, insufficientPrivilege)
		}
	}
	err := inSession(t, admin, platform, func(c context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(c, `SELECT * FROM pandokos.tenant_deletion`)
		return err
	})
	if sqlstate(err) != insufficientPrivilege {
		t.Errorf("SELECT * by the admin role: %v, want SQLSTATE %s (no grant on the salt)", err, insufficientPrivilege)
	}
	var n int
	if err := inSession(t, admin, platform, func(c context.Context, tx pgx.Tx) error {
		return tx.QueryRow(c, `SELECT count(*) FROM pandokos.tenant_deletion WHERE state = 'scheduled'`).Scan(&n)
	}); err != nil || n != 1 {
		t.Errorf("the admin role reads %d scheduled requests (%v), want 1", n, err)
	}

	// No update, by any runtime role.
	for _, c := range []struct {
		role string
		conn *pgx.Conn
	}{{"admin", admin}, {"worker", worker}, {"gateway", gateway}} {
		for _, col := range []string{"reason", "not_before", "state"} {
			val := map[string]string{"reason": "'x'", "not_before": "now()", "state": "'running'"}[col]
			if err := commit(c.conn, platform, `UPDATE pandokos.tenant_deletion SET `+col+` = `+val); sqlstate(err) != insufficientPrivilege {
				t.Errorf("%s updates %s: %v, want SQLSTATE %s", c.role, col, err, insufficientPrivilege)
			}
		}
		if err := commit(c.conn, platform, `TRUNCATE pandokos.tenant_deletion`); sqlstate(err) != insufficientPrivilege {
			t.Errorf("%s truncates: %v", c.role, err)
		}
	}
	for _, role := range []string{"worker", "gateway"} {
		conn := map[string]*pgx.Conn{"worker": worker, "gateway": gateway}[role]
		if err := commit(conn, platform, insertRequest, running.String()); sqlstate(err) != insufficientPrivilege {
			t.Errorf("%s inserts a request: %v, want SQLSTATE %s", role, err, insufficientPrivilege)
		}
		if err := commit(conn, platform, `DELETE FROM pandokos.tenant_deletion`); sqlstate(err) != insufficientPrivilege {
			t.Errorf("%s deletes a request: %v, want SQLSTATE %s", role, err, insufficientPrivilege)
		}
	}

	// A tenant's session and the gateway see nothing.
	for _, c := range []struct {
		role string
		conn *pgx.Conn
		sess session
		want string // "denied" or "none"
	}{
		{"admin", admin, tenantSession(suspended), "none"},
		{"worker", worker, tenantSession(suspended), "none"},
		{"admin", admin, session{name: "none"}, "none"},
		{"gateway", gateway, tenantSession(suspended), "denied"},
		{"gateway", gateway, platform, "denied"},
	} {
		var rows int
		err := inSession(t, c.conn, c.sess, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(tenant_id) FROM pandokos.tenant_deletion`).Scan(&rows)
		})
		switch {
		case c.want == "denied" && sqlstate(err) != insufficientPrivilege:
			t.Errorf("%s in session %s reads the table: %v, want SQLSTATE %s", c.role, c.sess.name, err, insufficientPrivilege)
		case c.want == "none" && (err != nil || rows != 0):
			// the worker has a grant on seven columns only; a count of tenant_id is among them
			t.Errorf("%s in session %s sees %d requests (%v), want none", c.role, c.sess.name, rows, err)
		}
	}

	// The worker reads seven columns in platform scope, and nothing else.
	if err := inSession(t, worker, platform, func(c context.Context, tx pgx.Tx) error {
		return tx.QueryRow(c, `SELECT count(*) FROM (SELECT tenant_id, state, step, request_id, not_before, started_at, kek_versions FROM pandokos.tenant_deletion) x`).Scan(&n)
	}); err != nil || n != 1 {
		t.Errorf("the worker reads %d requests by its seven columns (%v), want 1", n, err)
	}
	for _, col := range []string{"reason", "requested_by_type", "requested_by_id", "requested_by_name", "requested_at", "settle_at", "completed_at", "passes", "removed", "audit",
		"completion_seq", "salt", "usage_started_at", "usage_id"} {
		err := inSession(t, worker, platform, func(c context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(c, `SELECT `+col+` FROM pandokos.tenant_deletion`)
			return err
		})
		if sqlstate(err) != insufficientPrivilege {
			t.Errorf("the worker reads %s: %v, want SQLSTATE %s", col, err, insufficientPrivilege)
		}
	}

	// The admin role deletes a scheduled row and no other.
	d.RequestDeletion(t, running, time.Now().Add(-time.Minute))
	if next, err := stepOnce(t, worker, running, 100); err != nil || next != "access" {
		t.Fatalf("begin: %q %v", next, err)
	}
	if err := inSession(t, admin, platform, func(c context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(c, `DELETE FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, running.String())
		if err != nil || tag.RowsAffected() != 0 {
			t.Errorf("the admin role deletes a running deletion: %d rows, %v, want none", tag.RowsAffected(), err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := dbNumber(t, d, `SELECT count(*) FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, running.String()); n != 1 {
		t.Errorf("the running deletion is gone")
	}
	// The tenant of the scheduled request cannot be reactivated, by anybody who updates it; the
	// worker and the gateway cannot update a tenant at all.
	err = commit(admin, platform, `UPDATE pandokos.tenant SET state = 'active' WHERE id = $1::uuid`, suspended.String())
	if sqlstate(err) != restrictViolation || constraintOf(err) != "tenant_deletion_holds" {
		t.Errorf("reactivation while a deletion is requested: %v, want SQLSTATE %s tenant_deletion_holds", err, restrictViolation)
	}
	if st := dbText(t, d, `SELECT state FROM pandokos.tenant WHERE id = $1::uuid`, suspended.String()); st != "suspended" {
		t.Errorf("the tenant is %s after the refused reactivation", st)
	}
	var deleted int64
	if err := inSession(t, admin, platform, func(c context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(c, `DELETE FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, suspended.String())
		deleted = tag.RowsAffected()
		return err
	}); err != nil || deleted != 1 {
		t.Errorf("the admin role cancels a scheduled request: %d rows, %v", deleted, err)
	}
	if err := commit(admin, platform, `UPDATE pandokos.tenant SET state = 'active' WHERE id = $1::uuid`, suspended.String()); err != nil {
		t.Errorf("reactivation after the cancel: %v", err)
	}
	// And the tenant that is being deleted cannot go back either.
	if err := commit(admin, platform, `UPDATE pandokos.tenant SET state = 'active' WHERE id = $1::uuid`, running.String()); err == nil {
		t.Error("a tenant that is being deleted was set active by the admin role")
	}
}
