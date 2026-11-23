package reports_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/reports"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

// retentionWorld is two tenants and the platform with rows of old months: sealed audit rows, a
// ledger row of ten months ago (in the default partition) and a partition of six months ago.
type retentionWorld struct {
	d       *dbtest.Database
	a, b    tenant.ID
	oldPart string
}

func newRetentionWorld(t *testing.T) *retentionWorld {
	t.Helper()
	d := dbtest.NewMigrated(t)
	now := time.Now().UTC()
	month := func(back int) time.Time {
		return time.Date(now.Year(), now.Month()-time.Month(back), 15, 12, 0, 0, 0, time.UTC)
	}
	w := &retentionWorld{d: d, a: d.SeedTenant(t, "ret-a"), b: d.SeedTenant(t, "ret-b")}
	for _, tid := range []tenant.ID{w.a, w.b} {
		for _, back := range []int{5, 3, 0} {
			d.SealedRow(t, tid, month(back), "old.row")
		}
		d.SeedUsage(t, tid, dbtest.UsageRow{StartedAt: month(10)}, dbtest.UsageRow{StartedAt: now})
	}
	for _, back := range []int{5, 3, 0} {
		d.SealedPlatformRow(t, month(back), "old.row")
	}
	w.oldPart = d.OldPartition(t, month(6))
	return w
}

func (w *retentionWorld) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := w.d.SharedSuper(t).QueryRow(ctx(t), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestRetentionJob is T-963: with both keys 0 nothing is removed and nothing is said; with values
// the tenant pass and then the platform pass run; a failing tenant does not stop the others; the
// log lines retention ready, retention applied and retention failing; the counters M44 and M45.
func TestRetentionJob(t *testing.T) {
	c := ctx(t)

	t.Run("both keys 0 remove nothing", func(t *testing.T) {
		w := newRetentionWorld(t)
		logs := &logBuffer{}
		job := &reports.Retention{DB: w.d.Open(t, store.RoleWorker), Logger: jsonLogger(logs)}
		job.Run(c) // returns at once with both months 0
		if err := job.Pass(c); err != nil {
			t.Fatal(err)
		}
		if n := w.count(t, `SELECT count(*) FROM pandokos.audit_event WHERE action IN ('audit.retention', 'usage.retention')`) +
			w.count(t, `SELECT count(*) FROM pandokos.platform_audit_event WHERE action IN ('audit.retention', 'usage.retention')`); n != 0 {
			t.Errorf("%d retention rows with both keys 0", n)
		}
		if n := w.count(t, `SELECT count(*) FROM pandokos.usage_record`); n != 4 {
			t.Errorf("the ledger holds %d rows, want 4", n)
		}
		if got := logs.lines(t, "retention applied"); len(got) != 0 {
			t.Errorf("retention applied with both keys 0: %v", got)
		}
		if st := job.Stats(); st.Failed != 0 {
			t.Errorf("Stats %+v", st)
		}
	})

	t.Run("a pass removes what is old, the platform first, and says so", func(t *testing.T) {
		w := newRetentionWorld(t)
		logs := &logBuffer{}
		job := &reports.Retention{DB: w.d.Open(t, store.RoleWorker), UsageMonths: 2, AuditMonths: 1, Logger: jsonLogger(logs)}
		if err := job.Pass(c); err != nil {
			t.Fatal(err)
		}
		if w.count(t, `SELECT count(*) FROM pg_class WHERE relname = $1`, w.oldPart) != 0 {
			t.Errorf("the partition %s was not dropped", w.oldPart)
		}
		for _, tid := range []tenant.ID{w.a, w.b} {
			if n := w.count(t, `SELECT count(*) FROM pandokos.usage_record WHERE tenant_id = $1::uuid`, tid.String()); n != 1 {
				t.Errorf("tenant %s keeps %d ledger rows, want the current one", tid, n)
			}
			// 5 and 3 months back are removed, the current one stays, and audit.retention and usage.retention are written.
			if n := w.count(t, `SELECT count(*) FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'old.row'`, tid.String()); n != 1 {
				t.Errorf("tenant %s keeps %d old rows, want 1", tid, n)
			}
			if n := w.count(t, `SELECT count(*) FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action IN ('audit.retention', 'usage.retention')`, tid.String()); n != 2 {
				t.Errorf("tenant %s has %d retention rows, want 2", tid, n)
			}
		}
		if n := w.count(t, `SELECT count(*) FROM pandokos.platform_audit_event WHERE action IN ('audit.retention', 'usage.retention')`); n != 2 {
			t.Errorf("the platform log has %d retention rows, want 2 (usage.retention and audit.retention)", n)
		}
		// The platform pass is first: no platform row is written after the first tenant row, so a failure
		// of the platform pass can stop the tenant pass that removes the rollup of the months it keeps.
		if w.count(t, `SELECT count(*) FROM pandokos.audit_event e, pandokos.platform_audit_event p
			WHERE e.action IN ('audit.retention', 'usage.retention') AND p.action IN ('audit.retention', 'usage.retention') AND p.occurred_at > e.occurred_at`) != 0 {
			t.Error("a platform retention row is later than a tenant's: the platform pass comes first")
		}
		applied := logs.lines(t, "retention applied")
		if len(applied) != 1 || applied[0]["level"] != "INFO" {
			t.Fatalf("the line retention applied: %v", applied)
		}
		for k, want := range map[string]float64{"partitions": 1, "usage_records": 2, "audit_events": 4, "platform_audit_events": 2} {
			if applied[0][k] != want {
				t.Errorf("retention applied: %s = %v, want %v (%v)", k, applied[0][k], want, applied[0])
			}
		}
		st := job.Stats()
		if st.Runs != 1 || st.Failed != 0 || st.Removed["usage_partition"] != 1 || st.Removed["usage_record"] != 2 || st.Removed["audit_event"] != 4 || st.Removed["platform_audit_event"] != 2 {
			t.Errorf("Stats %+v", st)
		}
		for _, tid := range []tenant.ID{w.a, w.b} {
			if strings.Contains(logs.text(), tid.String()) {
				t.Errorf("a log line names tenant %s", tid)
			}
		}
		// Nothing older is left: a second pass writes nothing and says nothing.
		if err := job.Pass(c); err != nil {
			t.Fatal(err)
		}
		if got := logs.lines(t, "retention applied"); len(got) != 1 {
			t.Errorf("%d lines retention applied after a second pass, want 1", len(got))
		}
		if n := w.count(t, `SELECT count(*) FROM pandokos.platform_audit_event WHERE action IN ('audit.retention', 'usage.retention')`); n != 2 {
			t.Errorf("a second pass wrote retention rows: %d", n)
		}
	})

	t.Run("a failing tenant does not stop the others", func(t *testing.T) {
		w := newRetentionWorld(t)
		super := w.d.SharedSuper(t)
		for _, q := range []string{
			`CREATE FUNCTION public.probe_boom() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'probe: this tenant fails'; END $$`,
			fmt.Sprintf(`CREATE TRIGGER probe_boom BEFORE UPDATE ON pandokos.audit_chain FOR EACH ROW WHEN (NEW.tenant_id = '%s') EXECUTE FUNCTION public.probe_boom()`, w.a),
		} {
			if _, err := super.Exec(c, q); err != nil {
				t.Fatal(err)
			}
		}
		logs := &logBuffer{}
		job := &reports.Retention{DB: w.d.Open(t, store.RoleWorker), UsageMonths: 2, AuditMonths: 1, Logger: jsonLogger(logs)}
		_ = job.Pass(c)
		if n := w.count(t, `SELECT count(*) FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'old.row'`, w.b.String()); n != 1 {
			t.Errorf("tenant B keeps %d old rows, want 1: the failure of A stopped it", n)
		}
		if n := w.count(t, `SELECT count(*) FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'old.row'`, w.a.String()); n != 3 {
			t.Errorf("tenant A lost rows although its retention failed: %d", n)
		}
		if w.count(t, `SELECT count(*) FROM pandokos.platform_audit_event WHERE action = 'audit.retention'`) != 1 {
			t.Error("the platform pass did not run after a tenant failed")
		}
		failing := logs.lines(t, "retention failing")
		if len(failing) != 1 || failing[0]["level"] != "WARN" || failing[0]["error_class"] == nil || failing[0]["sqlstate"] == nil || failing[0]["failures"] == nil {
			t.Errorf("the line retention failing: %v", failing)
		}
		_ = job.Pass(c)
		if got := logs.lines(t, "retention failing"); len(got) != 1 {
			t.Errorf("%d lines retention failing within an hour, want 1", len(got))
		}
		if st := job.Stats(); st.Failed < 1 || st.Runs < 1 {
			t.Errorf("Stats %+v, want failed runs counted", st)
		}
		if strings.Contains(logs.text(), w.a.String()) {
			t.Error("a log line names the failing tenant")
		}
	})

	t.Run("a failing platform pass leaves the tenants alone", func(t *testing.T) {
		// A tenant's rollup of a month before the cut must outlive the ledger partition of that month:
		// when the platform cannot drop the partition, the tenant pass does not run.
		w := newRetentionWorld(t)
		super := w.d.SharedSuper(t)
		for _, q := range []string{
			`CREATE FUNCTION public.probe_boom() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'probe: the platform fails'; END $$`,
			`CREATE TRIGGER probe_boom BEFORE DELETE ON pandokos.platform_audit_event FOR EACH ROW EXECUTE FUNCTION public.probe_boom()`,
		} {
			if _, err := super.Exec(c, q); err != nil {
				t.Fatal(err)
			}
		}
		job := &reports.Retention{DB: w.d.Open(t, store.RoleWorker), UsageMonths: 2, AuditMonths: 1, Logger: jsonLogger(&logBuffer{})}
		if err := job.Pass(c); err == nil {
			t.Fatal("the pass did not fail although the platform's did")
		}
		if w.count(t, `SELECT count(*) FROM pg_class WHERE relname = $1`, w.oldPart) != 1 {
			t.Error("the partition was dropped although the platform pass failed")
		}
		for _, tid := range []tenant.ID{w.a, w.b} {
			if n := w.count(t, `SELECT count(*) FROM pandokos.usage_record WHERE tenant_id = $1::uuid`, tid.String()); n != 2 {
				t.Errorf("tenant %s keeps %d ledger rows, want both: its pass must not run", tid, n)
			}
			if n := w.count(t, `SELECT count(*) FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'old.row'`, tid.String()); n != 3 {
				t.Errorf("tenant %s keeps %d old rows, want 3: its pass must not run", tid, n)
			}
		}
		if st := job.Stats(); st.Failed != 1 {
			t.Errorf("Stats %+v, want one failed run", st)
		}
	})

	t.Run("Run applies retention at the start and says that it is ready", func(t *testing.T) {
		w := newRetentionWorld(t)
		logs := &logBuffer{}
		job := &reports.Retention{DB: w.d.Open(t, store.RoleWorker), UsageMonths: 2, AuditMonths: 1, Logger: jsonLogger(logs)}
		rc, cancel := context.WithCancel(c)
		done := make(chan struct{})
		go func() { job.Run(rc); close(done) }()
		deadline := time.Now().Add(20 * time.Second)
		for w.count(t, `SELECT count(*) FROM pg_class WHERE relname = $1`, w.oldPart) != 0 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("Run does not stop with its context")
		}
		if w.count(t, `SELECT count(*) FROM pg_class WHERE relname = $1`, w.oldPart) != 0 {
			t.Error("Run did not apply retention at its start")
		}
		ready := logs.lines(t, "retention ready")
		if len(ready) != 1 || ready[0]["level"] != "INFO" || ready[0]["usage_months"] != float64(2) || ready[0]["audit_months"] != float64(1) {
			t.Errorf("the line retention ready: %v", ready)
		}
	})
}
