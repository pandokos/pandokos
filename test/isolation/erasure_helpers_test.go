package isolation

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

// The helpers of the deletion tests (sprint 6.3): the worker's call, a whole deletion, and a tenant
// stopped at a given point. Every call is made as pandokos_worker in the session of the tenant,
// as the deletion job does; nothing here updates or deletes a row of the tenant.

// stepOnce calls tenant_erasure_step(batch) as the worker in the tenant's session and returns the
// answer and the error.
func stepOnce(t *testing.T, conn *pgx.Conn, id tenant.ID, batch int) (string, error) {
	t.Helper()
	var next string
	err := inSession(t, conn, tenantSession(id), func(c context.Context, tx pgx.Tx) error {
		return tx.QueryRow(c, `SELECT pandokos.tenant_erasure_step($1::integer)`, batch).Scan(&next)
	})
	return next, err
}

// stepUntil calls the step function until it answers want (or fails the test after 200 calls), and
// returns every answer in order, consecutive repeats collapsed. When the function answers
// "settling" the settle time is moved to now, as dbtest.AgeDeletion does, unless stopAtSettling.
func stepUntil(t *testing.T, d *dbtest.Database, conn *pgx.Conn, id tenant.ID, batch int, want string) []string {
	t.Helper()
	var answers []string
	for i := 0; i < 200; i++ {
		next, err := stepOnce(t, conn, id, batch)
		if err != nil {
			t.Fatalf("tenant_erasure_step after %v: %v", answers, err)
		}
		if len(answers) == 0 || answers[len(answers)-1] != next {
			answers = append(answers, next)
		}
		if next == want {
			return answers
		}
		if next == "settling" || next == "scheduled" {
			d.AgeDeletion(t, id)
		}
	}
	t.Fatalf("the deletion did not reach %q: %v", want, answers)
	return nil
}

// deleteTenant requests the deletion of the tenant with a past not_before and runs it to its end.
func deleteTenant(t *testing.T, d *dbtest.Database, id tenant.ID, batch int) []string {
	t.Helper()
	d.RequestDeletion(t, id, time.Now().Add(-time.Minute))
	return stepUntil(t, d, d.Connect(t, "worker"), id, batch, "done")
}

// closedTenants returns a tenant that is being deleted (the step "begin" ran, nothing else) and one
// that is deleted.
func closedTenants(t *testing.T, d *dbtest.Database) (deleting, deleted tenant.ID) {
	t.Helper()
	deleting, deleted = d.PopulatedTenant(t), d.PopulatedTenant(t)
	worker := d.Connect(t, "worker")
	d.RequestDeletion(t, deleting, time.Now().Add(-time.Minute))
	if next, err := stepOnce(t, worker, deleting, 100); err != nil || next != "access" {
		t.Fatalf("the step begin: %q, %v", next, err)
	}
	deleteTenant(t, d, deleted, 100)
	return deleting, deleted
}

// dbText runs a query that returns one text as the superuser.
func dbText(t *testing.T, d *dbtest.Database, sql string, args ...any) string {
	t.Helper()
	var s string
	if err := d.Super(t).QueryRow(ctx(t), sql, args...).Scan(&s); err != nil {
		t.Fatalf("%v\n%s", err, sql)
	}
	return s
}

// dbNumber runs a query that returns one integer as the superuser.
func dbNumber(t *testing.T, d *dbtest.Database, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := d.Super(t).QueryRow(ctx(t), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%v\n%s", err, sql)
	}
	return n
}
