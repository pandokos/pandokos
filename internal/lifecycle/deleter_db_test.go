package lifecycle_test

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

var (
	reTxControl = regexp.MustCompile(`(?i)^\s*(begin|commit|rollback)\b`)
	reSetting   = regexp.MustCompile(`^SELECT pg_catalog\.set_config\(\$1, \$2, true\)$`)
	reStep      = regexp.MustCompile(`(?s)pandokos\.tenant_erasure_step\(`)
	reOpen      = regexp.MustCompile(`(?s)FROM pandokos\.tenant_deletion d\s+WHERE d\.state = 'running' OR`)
	reCount     = regexp.MustCompile(`(?s)SELECT d\.state, count\(\*\)::bigint AS n FROM pandokos\.tenant_deletion d`)
	reWrites    = regexp.MustCompile(`(?i)\b(insert|update|delete|truncate|alter|drop|create)\b`)
)

// logsOf returns the lines of the log with the message.
func logsOf(t *testing.T, logs *testutil.LogBuffer, msg string) []map[string]any {
	t.Helper()
	return logs.Find(t, msg)
}

// TestDeleterLoop is T-993: what the job reads and calls, where its loop stops, that one tenant's
// failure does not stop another's, and what it counts and says.
func TestDeleterLoop(t *testing.T) {
	d := dbtest.NewMigrated(t)
	proxy := newRecordingProxy(t, fmt.Sprintf("%s:%d", d.Host, d.Port))
	db := workerVia(t, d, proxy.addr)
	clk := newClock()
	logs := &testutil.LogBuffer{}
	job := deleter(db, logs, clk)

	due, later, broken, bystander := d.PopulatedTenant(t), d.PopulatedTenant(t), d.PopulatedTenant(t), d.PopulatedTenant(t)
	seedLedger(t, d, due, 120)
	d.RequestDeletion(t, due, time.Now().Add(-time.Minute))
	d.RequestDeletion(t, later, time.Now().Add(24*time.Hour))
	d.RequestDeletion(t, broken, time.Now().Add(-time.Minute))
	// The tenant of a deletion that cannot begin: it is not suspended (a state that only a fault in the
	// platform can leave a due request in).
	d.SuperExecReplica(t, "make the tenant active", `UPDATE pandokos.tenant SET state = 'active' WHERE id = $1::uuid`, broken.String())
	bystanderBefore := d.TableDigests(t, bystander)
	laterBefore := d.TableDigests(t, later)
	requestID := text(t, d, `SELECT request_id FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, due.String())

	// First pass: the due tenant goes as far as the two minutes of settling; the one that is not due
	// is not called; the broken one fails and does not stop the others.
	proxy.reset()
	_ = job.Pass(ctx(t))
	var opens, counts, steps int
	for _, sql := range proxy.statements() {
		switch {
		case reTxControl.MatchString(sql), reSetting.MatchString(sql):
		case reOpen.MatchString(sql):
			opens++
		case reCount.MatchString(sql):
			counts++
		case reStep.MatchString(sql):
			steps++
			if reWrites.MatchString(sql) {
				t.Errorf("a step statement writes: %s", sql)
			}
		default:
			t.Errorf("the job sent a statement that is neither a list of open deletions, a count nor the step function: %s", sql)
		}
	}
	if opens != 1 || counts != 1 {
		t.Errorf("the pass listed the open deletions %d times and counted them %d times, want once each", opens, counts)
	}
	if steps == 0 {
		t.Fatal("the pass called no step")
	}
	if got := stepOf(t, d, due); got != "running/complete" {
		t.Errorf("the due deletion is %s after the pass, want running/complete (the loop stops at settling)", got)
	}
	if got := stepOf(t, d, later); got != "scheduled/begin" {
		t.Errorf("the deletion that is not due is %s: it was called", got)
	}
	if got := stepOf(t, d, broken); got != "scheduled/begin" {
		t.Errorf("the broken deletion is %s", got)
	}
	if got := d.TableDigests(t, bystander); fmt.Sprint(withoutGlobal(got)) != fmt.Sprint(withoutGlobal(bystanderBefore)) {
		t.Error("a tenant with no request changed")
	}
	if got := d.TableDigests(t, later); fmt.Sprint(withoutGlobal(got)) != fmt.Sprint(withoutGlobal(laterBefore)) {
		t.Error("a tenant whose deletion is not due changed")
	}

	st := job.Stats()
	for step, want := range map[string]int64{"begin": 1, "access": 1, "secrets": 1, "configuration": 1, "usage": 2, "audit": 1, "complete": 0} {
		if got := st.Steps[[2]string{step, "ok"}]; got != want {
			t.Errorf("M47 counts %d ok steps of %s, want %d", got, step, want)
		}
	}
	if got := st.Steps[[2]string{"begin", "failed"}]; got != 1 {
		t.Errorf("M47 counts %d failed steps of begin, want 1 (the broken tenant)", got)
	}
	if st.Open["scheduled"] != 3 || st.Open["running"] != 0 {
		t.Errorf("M48 %v at the start of the pass, want 3 scheduled (the due one, the one that is not due, the broken one) and none running", st.Open)
	}
	if started := logsOf(t, logs, "tenant deletion started"); len(started) != 1 || started[0]["request_id"] != requestID || started[0]["level"] != "INFO" {
		t.Errorf("the lines tenant deletion started: %v, want one with the request ID %s", started, requestID)
	}
	failing := logsOf(t, logs, "tenant deletion failing")
	if len(failing) != 1 || failing[0]["level"] != "WARN" || failing[0]["step"] != "begin" || failing[0]["sqlstate"] != "55000" || failing[0]["failures"] != float64(1) || failing[0]["error_class"] == nil {
		t.Errorf("the lines tenant deletion failing: %v, want one warning for the step begin with sqlstate 55000", failing)
	}
	if len(logsOf(t, logs, "tenant deletion completed")) != 0 {
		t.Error("tenant deletion completed before the deletion ended")
	}

	// Second pass, the settling time over: the due tenant ends; the failing one is retried and fails
	// again, with at most one line a minute.
	d.AgeDeletion(t, due)
	clk.Advance(30 * time.Second)
	_ = job.Pass(ctx(t))
	if got := stepOf(t, d, due); got != "completed/done" {
		t.Errorf("the due deletion is %s after the second pass", got)
	}
	if got := len(logsOf(t, logs, "tenant deletion failing")); got != 1 {
		t.Errorf("%d failing lines after a second failure within a minute, want 1", got)
	}
	if completed := logsOf(t, logs, "tenant deletion completed"); len(completed) != 1 || completed[0]["request_id"] != requestID || completed[0]["duration_ms"] == nil {
		t.Errorf("the lines tenant deletion completed: %v, want one with the request ID and a duration", completed)
	}
	if got := job.Stats().Steps[[2]string{"complete", "ok"}]; got != 1 {
		t.Errorf("M47 counts %d ok steps of complete, want 1", got)
	}
	if got := job.Stats().Steps[[2]string{"begin", "failed"}]; got != 2 {
		t.Errorf("M47 counts %d failed steps of begin, want 2", got)
	}
	if got := job.Stats().Open; got["running"] != 1 || got["scheduled"] != 2 {
		t.Errorf("M48 %v at the start of the second pass, want 2 scheduled and 1 running", got)
	}

	// The failing tenant is repaired: the next step that succeeds says so, once.
	if _, err := d.Super(t).Exec(ctx(t), `UPDATE pandokos.tenant SET state = 'suspended' WHERE id = $1::uuid`, broken.String()); err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * time.Minute)
	_ = job.Pass(ctx(t))
	if recovered := logsOf(t, logs, "tenant deletion recovered"); len(recovered) != 1 || recovered[0]["level"] != "INFO" || recovered[0]["failures"] == nil {
		t.Errorf("the lines tenant deletion recovered: %v, want one", recovered)
	}

	// No line names a tenant, its name or a count of its rows.
	for _, tid := range []tenant.ID{due, later, broken, bystander} {
		if strings.Contains(logs.String(), tid.String()) {
			t.Errorf("a log line names the tenant %s", tid)
		}
	}
	for _, name := range []string{"pop-", "removed", "service_account", "canary-team"} {
		if strings.Contains(logs.String(), name) {
			t.Errorf("a log line holds %q: a name, a table or a count of a tenant", name)
		}
	}
}

// withoutGlobal drops the digests of the global tables that another tenant's deletion changes
// legitimately: the platform log, the tenant rows and the sessions and accounts of its users (the
// digests of those tables are of the whole table).
func withoutGlobal(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		switch k {
		case "platform_audit_event", "platform_audit_chain", "tenant", "user_session", "local_account", "user_identity":
		default:
			out[k] = v
		}
	}
	return out
}

// TestDeleterBudget is the other half of T-993: the loop for one tenant stops at the budget of 30 s.
func TestDeleterBudget(t *testing.T) {
	d := dbtest.NewMigrated(t)
	proxy := newRecordingProxy(t, fmt.Sprintf("%s:%d", d.Host, d.Port))
	db := workerVia(t, d, proxy.addr)
	logs := &testutil.LogBuffer{}
	id := d.PopulatedTenant(t)
	d.RequestDeletion(t, id, time.Now().Add(-time.Minute))

	// A clock that goes 31 s forward each time it is read: one step is the whole budget.
	var now = time.Now()
	jumping := deleter(db, logs, newClock())
	jumping.Now = func() time.Time { now = now.Add(31 * time.Second); return now }
	for i, want := range []string{"running/access", "running/secrets", "running/configuration"} {
		_ = jumping.Pass(ctx(t))
		if got := stepOf(t, d, id); got != want {
			t.Fatalf("after pass %d the deletion is %s, want %s (one step a pass when the budget is spent at once)", i+1, got, want)
		}
	}
	// With the real clock the same job goes on to the end of the settling time in one pass.
	real := deleter(db, logs, newClock())
	real.Now = time.Now
	_ = real.Pass(ctx(t))
	if got := stepOf(t, d, id); got != "running/complete" {
		t.Errorf("a pass with the budget intact ended at %s, want running/complete", got)
	}
}

// TestDeleterCrashResume is T-994: a job that stops after each step, in the middle of the ledger
// batches, and starts again, ends where an uninterrupted job on an identical tenant ends.
func TestDeleterCrashResume(t *testing.T) {
	d := dbtest.NewMigrated(t)
	worker := d.Open(t, store.RoleWorker)
	logs := &testutil.LogBuffer{}

	whole, crashed := d.PopulatedTenant(t), d.PopulatedTenant(t)
	for _, id := range []tenant.ID{whole, crashed} {
		seedLedger(t, d, id, 11000) // three batches of 5000
	}
	d.RequestDeletion(t, whole, time.Now().Add(-time.Minute))

	// The uninterrupted run.
	clk := newClock()
	job := deleter(worker, logs, clk)
	_ = job.Pass(ctx(t))
	if got := stepOf(t, d, whole); got != "running/complete" {
		t.Fatalf("the uninterrupted run is %s after one pass, want running/complete", got)
	}
	// The other tenant is deleted by a job that stops after each step (the clock jumps over the budget).
	d.RequestDeletion(t, crashed, time.Now().Add(-time.Minute))
	var passes int
	var jump = time.Now()
	stopping := deleter(worker, logs, clk)
	stopping.Now = func() time.Time { jump = jump.Add(31 * time.Second); return jump }
	for i := 0; i < 40 && stepOf(t, d, crashed) != "running/complete"; i++ {
		_ = stopping.Pass(ctx(t))
		passes++
	}
	if got := stepOf(t, d, crashed); got != "running/complete" {
		t.Fatalf("the interrupted run is %s after %d passes", got, passes)
	}
	if passes < 8 {
		t.Errorf("the interrupted run took %d passes: 11000 records are three batches, so at least eight steps", passes)
	}
	// A context cancelled before the pass stops it without a change.
	cancelled, cancel := context.WithCancel(ctx(t))
	cancel()
	before := stepOf(t, d, crashed)
	_ = stopping.Pass(cancelled)
	if got := stepOf(t, d, crashed); got != before {
		t.Errorf("a pass with a cancelled context changed the deletion: %s -> %s", before, got)
	}
	for _, id := range []tenant.ID{whole, crashed} {
		d.AgeDeletion(t, id)
	}
	_ = job.Pass(ctx(t))

	for _, id := range []tenant.ID{whole, crashed} {
		if got := stepOf(t, d, id); got != "completed/done" {
			t.Fatalf("the deletion of %s is %s", id, got)
		}
	}
	// The same result: the counts of the record, the kept rows, the chain.
	record := func(id tenant.ID) map[string]string {
		out := map[string]string{}
		for name, sql := range map[string]string{
			"removed":      `SELECT removed::text FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`,
			"passes":       `SELECT passes::text FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`,
			"kek_versions": `SELECT kek_versions::text FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`,
			"audit rows":   `SELECT audit ->> 'rows' FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`,
			"ledger rows":  `SELECT count(*)::text FROM pandokos.usage_record WHERE tenant_id = $1::uuid`,
			"ledger pseudonymized": `SELECT count(*)::text FROM pandokos.usage_record WHERE tenant_id = $1::uuid
				AND principal_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' AND principal_id NOT LIKE '0192f0f0-3333%' AND metadata = '{}'::jsonb`,
			"audit rows kept": `SELECT count(*)::text FROM pandokos.audit_event WHERE tenant_id = $1::uuid`,
			"record":          `SELECT detail - 'name' - 'requested_at' - 'started_at' - 'audit' ->> 'residue' FROM pandokos.platform_audit_event WHERE action = 'tenant.delete_complete' AND resource_id = $1`,
		} {
			out[name] = text(t, d, sql, id.String())
		}
		return out
	}
	a, b := record(whole), record(crashed)
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Errorf("the interrupted deletion differs from the uninterrupted one:\n%v\n%v", a, b)
	}
	if a["ledger rows"] != "11002" || a["ledger pseudonymized"] != "11002" {
		t.Errorf("the kept ledger: %v, want 11002 records (11000 and the two of the populated tenant), all pseudonymized", a)
	}
	for _, id := range []tenant.ID{whole, crashed} {
		res, head := d.VerifyAuditChain(t, "tenant:"+id.String())
		if len(res.Breaks) != 0 || res.Rows == 0 || res.Last != head.Seq {
			t.Errorf("the kept log of %s does not verify: %+v", id, res)
		}
	}
	if res, _ := d.VerifyAuditChain(t, "platform"); len(res.Breaks) != 0 {
		t.Errorf("the platform log does not verify: %+v", res)
	}
}

// TestDeleterDatabaseOutage is T-995: a failure injected into each step, as a serialization failure
// and as an unavailable database, leaves the deletion where it was; the next tick tries the step
// again, the log says it failed and then that it recovered.
func TestDeleterDatabaseOutage(t *testing.T) {
	d := dbtest.NewMigrated(t)
	worker := d.Open(t, store.RoleWorker)
	logs := &testutil.LogBuffer{}
	// A clock that goes 31 s forward each time it is read: a pass does one step of the tenant, and
	// two passes are more than a minute apart (the failing line is written at most once a minute).
	now := time.Now()
	job := deleter(worker, logs, newClock())
	job.Now = func() time.Time { now = now.Add(31 * time.Second); return now }

	id := d.PopulatedTenant(t)
	seedLedger(t, d, id, 150)
	d.RequestDeletion(t, id, time.Now().Add(-time.Minute))

	// The fault: a trigger on the row of the deletion, that fails the update made by the step the row is at.
	super := d.Super(t)
	for _, q := range []string{
		`CREATE TABLE pandokos.zz_fault (step text PRIMARY KEY, sqlstate text NOT NULL)`,
		`GRANT SELECT ON pandokos.zz_fault TO PUBLIC`, // the step function runs as the owner, which reads the table
		`CREATE FUNCTION pandokos.zz_fault() RETURNS trigger LANGUAGE plpgsql AS $$
		 DECLARE s text;
		 BEGIN
		   SELECT f.sqlstate INTO s FROM pandokos.zz_fault f WHERE f.step = OLD.step;
		   IF s IS NOT NULL THEN RAISE EXCEPTION 'injected fault' USING ERRCODE = s; END IF;
		   RETURN NEW;
		 END $$`,
		`CREATE TRIGGER zz_fault BEFORE UPDATE ON pandokos.tenant_deletion FOR EACH ROW EXECUTE FUNCTION pandokos.zz_fault()`,
	} {
		if _, err := super.Exec(ctx(t), q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	setFault := func(step, sqlstate string) {
		t.Helper()
		if _, err := super.Exec(ctx(t), `DELETE FROM pandokos.zz_fault`); err != nil {
			t.Fatal(err)
		}
		if step != "" {
			if _, err := super.Exec(ctx(t), `INSERT INTO pandokos.zz_fault VALUES ($1, $2)`, step, sqlstate); err != nil {
				t.Fatal(err)
			}
		}
	}
	everything := func() string {
		var parts []string
		for tbl, dg := range d.TableDigests(t, id) {
			if tbl != "zz_fault" {
				parts = append(parts, tbl+"="+dg)
			}
		}
		sort.Strings(parts)
		return strings.Join(parts, ",") + text(t, d, `SELECT COALESCE(md5(string_agg(u::text, ',' ORDER BY u.id)), '') FROM pandokos.usage_record u WHERE u.tenant_id = $1::uuid`, id.String())
	}

	for i, step := range []string{"begin", "access", "secrets", "configuration", "usage", "audit", "complete"} {
		sqlstate := []string{"40001", "57P03"}[i%2] // a serialization failure, then an unavailable database
		want := "running/" + step
		if step == "begin" {
			want = "scheduled/begin"
		}
		if step == "complete" {
			d.AgeDeletion(t, id) // the settling time is over: the call at complete does its work
		}
		if got := stepOf(t, d, id); got != want {
			t.Fatalf("before the fault of %s the deletion is %s, want %s", step, got, want)
		}
		rows := everything()
		failingBefore, recoveredBefore := len(logsOf(t, logs, "tenant deletion failing")), len(logsOf(t, logs, "tenant deletion recovered"))

		setFault(step, sqlstate)
		_ = job.Pass(ctx(t))
		if got := stepOf(t, d, id); got != want {
			t.Errorf("a failure injected into %s moved the deletion from %s to %s", step, want, got)
		}
		if everything() != rows {
			t.Errorf("a failure injected into %s left the tenant half done: a table changed", step)
		}
		if step == "complete" && number(t, d, `SELECT count(*) FROM pandokos.platform_audit_event WHERE action = 'tenant.delete_complete' AND resource_id = $1`, id.String()) != 0 {
			t.Error("the completion record was written although the step failed")
		}
		failing := logsOf(t, logs, "tenant deletion failing")
		if len(failing) != failingBefore+1 || failing[len(failing)-1]["step"] != step || failing[len(failing)-1]["sqlstate"] != sqlstate {
			t.Errorf("after a failure injected into %s (%s): %v, want a new line tenant deletion failing for that step", step, sqlstate, failing)
		}

		// The next tick, the fault gone: the step is tried again, and the recovery is said once.
		setFault("", "")
		_ = job.Pass(ctx(t))
		if step != "usage" && stepOf(t, d, id) == want {
			t.Errorf("after the fault of %s was removed the deletion is still %s", step, want)
		}
		if got := len(logsOf(t, logs, "tenant deletion recovered")); got != recoveredBefore+1 {
			t.Errorf("%d lines tenant deletion recovered after the fault of %s, want %d", got, step, recoveredBefore+1)
		}
		if step == "usage" { // the batch is done; the next call finds no record and goes on to the log
			_ = job.Pass(ctx(t))
		}
	}
	if got := stepOf(t, d, id); got != "completed/done" {
		t.Fatalf("the deletion is %s after the last step", got)
	}
	if number(t, d, `SELECT count(*) FROM pandokos.usage_record WHERE tenant_id = $1::uuid`, id.String()) != 152 { // 150 and the two of the populated tenant
		t.Error("the ledger lost records")
	}
	if res, _ := d.VerifyAuditChain(t, "tenant:"+id.String()); len(res.Breaks) != 0 {
		t.Errorf("the kept log does not verify after the failures: %+v", res)
	}

	// The database out of reach: the pass says it could not, and the next one works.
	other := d.PopulatedTenant(t)
	d.RequestDeletion(t, other, time.Now().Add(-time.Minute))
	d.SetAcceptingConnections(t, false)
	down, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := job.Pass(down)
	cancel()
	d.SetAcceptingConnections(t, true)
	if err == nil {
		t.Error("a pass with the database out of reach returned no error")
	}
	if got := stepOf(t, d, other); got != "scheduled/begin" {
		t.Errorf("the deletion is %s after a pass with the database out of reach", got)
	}
	testutil.Eventually(t, 15*time.Second, "the deletion to begin after the outage", func() bool {
		_ = job.Pass(ctx(t))
		return stepOf(t, d, other) != "scheduled/begin"
	})
}
