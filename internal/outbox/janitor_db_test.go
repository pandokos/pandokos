package outbox

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestJanitor is T-1038: the janitor calls the retention function per tenant until a call removes
// less than a batch; a failing tenant does not stop the others; deleted tenants are not visited; the
// totals reach the stats and the line of the pass.
func TestJanitor(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a, b, c, gone := d.SeedTenant(t, "jan-a"), d.SeedTenant(t, "jan-b"), d.SeedTenant(t, "jan-c"), d.SeedTenant(t, "jan-gone")
	// The destination's sealed secret is a placeholder: the janitor never opens it.
	enabled := dbText(t, d, `INSERT INTO pandokos.event_destination (tenant_id, name, url, secret, secret_key_version)
		VALUES ($1::uuid, 'enabled', 'https://hooks.example.test/in', decode(repeat('ab', 40), 'hex'), 1) RETURNING id::text`, a.String())
	d.SeedUsageRecords(t, a, 5200) // more than one batch of 5000
	d.SeedUsageRecords(t, b, 150)
	d.SeedUsageRecords(t, c, 120)
	d.SeedUsageRecords(t, gone, 3)
	// A limit event of the tenant b, older than the retention of usage (its event is aged with the others).
	setSQL(t, d, `INSERT INTO pandokos.limit_event (tenant_id, kind, rule_id, rule_level, subject_kind, metric, period_start, period_end, percent, limit_value, observed, occurred_at)
		VALUES ($1::uuid, 'threshold_reached', pandokos.uuid_v7(), 'tenant', 'tenant', 'tokens_per_day', now() - interval '200 days', now() - interval '199 days', 80, 10, 8, now() - interval '200 days')`, b.String())
	d.AgeOutbox(t, a, 200*time.Hour)
	d.AgeOutbox(t, b, 200*time.Hour)
	d.AgeOutbox(t, c, 200*time.Hour)
	d.CompleteDeletion(t, gone)

	// One tenant fails: a trigger that refuses the removal of its events.
	setSQL(t, d, `CREATE FUNCTION pandokos.zz_janitor_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'retention is refused' USING ERRCODE = 'integrity_constraint_violation'; END $$`)
	setSQL(t, d, fmt.Sprintf(`CREATE TRIGGER zz_janitor_fail BEFORE DELETE ON pandokos.outbox_event FOR EACH ROW WHEN (OLD.tenant_id = '%s') EXECUTE FUNCTION pandokos.zz_janitor_fail()`, c.String()))

	proxy := newPGProxy(t, d.Host, d.Port)
	host, port := proxy.addr()
	opts := d.ConnOptions("worker")
	opts.Host, opts.Port = host, port
	db, err := store.Open(store.Options{Conn: opts, Role: store.RoleWorker, MaxConns: 4, Migrations: mustMigrations(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	logs := &testutil.LogBuffer{}
	j := &Janitor{DB: db, Retention: 168 * time.Hour, UsageMonths: 2, Logger: telemetry.NewLogger(logs, "debug")}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	err = j.Pass(ctx)
	if err == nil {
		t.Fatal("a pass with a tenant that fails returned no error")
	}
	remaining := func(id string) int64 {
		return dbNumber(t, d, `SELECT count(*) FROM pandokos.outbox_event WHERE tenant_id = $1::uuid`, id)
	}
	if remaining(a.String()) != 0 || remaining(b.String()) != 0 {
		t.Errorf("events remain for the tenants that did not fail: %d and %d (the first holds more than a batch)", remaining(a.String()), remaining(b.String()))
	}
	if remaining(c.String()) != 120 {
		t.Errorf("the tenant that fails has %d events, want its 120 untouched", remaining(c.String()))
	}
	st := j.Stats()
	if st.Events != 5351 || st.Expired != 5200 || st.LimitEvents != 1 || st.PassesFailed != 1 || st.PassesOK != 0 {
		t.Errorf("the stats after the pass: %+v, want 5351 events, 5200 expired (the enabled destination had not passed them), 1 limit event, 1 failed pass", st)
	}
	if got := dbNumber(t, d, `SELECT expired_events FROM pandokos.event_destination WHERE id = $1::uuid`, enabled); got != 5200 {
		t.Errorf("the destination counts %d expired events, want 5200", got)
	}
	if got := dbNumber(t, d, `SELECT cursor_seq FROM pandokos.event_destination WHERE id = $1::uuid`, enabled); got != 5200 {
		t.Errorf("the cursor of the destination is %d, want past the removed events", got)
	}
	// The tenant that was deleted is not visited; the others are.
	params := strings.Join(proxy.parameters(), " ")
	if strings.Contains(params, gone.String()) {
		t.Error("the janitor opened a session for a tenant that is deleted")
	}
	for _, id := range []string{a.String(), b.String(), c.String()} {
		if !strings.Contains(params, id) {
			t.Errorf("the janitor never opened a session for %s", id)
		}
	}
	checkStatements(t, proxy.statements())
	for _, s := range proxy.statements() {
		if strings.Contains(s, "tenant_outbox_retention") && !strings.Contains(s, "$1") {
			t.Errorf("the retention statement carries literals: %s", s)
		}
	}
	// The failure is one line, with the class and the SQLSTATE and no tenant.
	failing := logs.Find(t, "event retention failing")
	if len(failing) != 1 || failing[0]["level"] != "WARN" || failing[0]["error_class"] == nil || failing[0]["sqlstate"] != "23000" {
		t.Errorf("the failure lines %v, want one warning with an error_class and the SQLSTATE 23000", failing)
	}
	if strings.Contains(logs.String(), c.String()) || strings.Contains(logs.String(), a.String()) {
		t.Errorf("a line names a tenant: %s", logs.String())
	}
	// The audit row of the limit events, in the chain of the tenant.
	found := false
	for _, r := range d.AuditRows(t, b) {
		found = found || r.Action == "limit_event.retention"
	}
	if !found {
		t.Error("no limit_event.retention audit row for the tenant whose limit event was removed")
	}

	// The failure is repaired: the next pass removes what is left, counts it, and writes one line.
	setSQL(t, d, `DROP TRIGGER zz_janitor_fail ON pandokos.outbox_event`)
	logs.Reset()
	if err := j.Pass(ctx); err != nil {
		t.Fatalf("the second pass: %v", err)
	}
	if remaining(c.String()) != 0 {
		t.Errorf("%d events remain for the tenant that was repaired", remaining(c.String()))
	}
	st = j.Stats()
	if st.Events != 5471 || st.PassesOK != 1 || st.PassesFailed != 1 {
		t.Errorf("the stats after the second pass: %+v, want 5471 events in all", st)
	}
	applied := logs.Find(t, "event retention applied")
	if len(applied) != 1 || applied[0]["level"] != "INFO" || fmt.Sprint(applied[0]["events"]) != "120" || fmt.Sprint(applied[0]["expired"]) != "0" || fmt.Sprint(applied[0]["limit_events"]) != "0" {
		t.Errorf("the line of the second pass: %v, want the totals of that pass over the installation", applied)
	}
	// A pass that removes nothing writes no line.
	logs.Reset()
	if err := j.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if lines := logs.Find(t, "event retention applied"); len(lines) != 0 {
		t.Errorf("a pass that removed nothing wrote %v", lines)
	}
}

func dbText(t *testing.T, d *dbtest.Database, sql string, args ...any) string {
	t.Helper()
	var s string
	if err := d.Super(t).QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		t.Fatalf("%v\n%s", err, sql)
	}
	return s
}
