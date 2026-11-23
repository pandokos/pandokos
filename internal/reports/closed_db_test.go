package reports_test

import (
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/reports"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

// TestJobsSkipClosedTenants is T-1012: the rollup and the retention of living tenants are not run for a
// tenant that is being deleted or is deleted (they would write rows that the tenant refuses); the third
// pass of retention calls deleted_tenant_retention for deleted tenants only, in their session, and counts
// what it removes with the two new kinds of M44.
func TestJobsSkipClosedTenants(t *testing.T) {
	d := dbtest.NewMigrated(t)
	now := time.Now().UTC()
	old := now.AddDate(0, -30, 0)
	mk := func(name string) tenant.ID {
		id := d.SeedTenant(t, name)
		// Old rows (a prefix of the log, a record of the default partition) and recent ones.
		d.SealedRow(t, id, old, "old.event")
		d.SealedRow(t, id, now.Add(-time.Hour), "recent.event")
		d.SeedUsage(t, id, dbtest.UsageRow{StartedAt: now.AddDate(0, -20, 0), RecordedAt: now.Add(-3 * time.Hour)}, dbtest.UsageRow{StartedAt: now.Add(-time.Hour), RecordedAt: now.Add(-time.Hour)})
		return id
	}
	active, deleting, deleted := mk("closed-active"), mk("closed-deleting"), mk("closed-deleted")
	d.BeginDeletion(t, deleting)
	d.CompleteDeletion(t, deleted)
	// Rows of the closed tenants as the deletion left them, to be compared after the passes.
	deletingBefore := d.TableDigests(t, deleting)
	deletedLedger := d.Count(t, `SELECT count(*)::int FROM pandokos.usage_record_default WHERE tenant_id = $1::uuid`, deleted.String())
	if deletedLedger != 1 {
		t.Fatalf("the deleted tenant keeps %d old records in the default partition, want 1", deletedLedger)
	}
	chainBefore := d.Count(t, `SELECT count(*)::int FROM pandokos.audit_event WHERE tenant_id = $1::uuid`, deleted.String())

	worker := d.OpenSized(t, store.RoleWorker, 4)
	logs := &logBuffer{}
	roll := &reports.Rollup{DB: worker, Interval: time.Minute, Logger: jsonLogger(logs)}
	if err := roll.Pass(ctx(t)); err != nil {
		t.Fatalf("the rollup pass: %v", err)
	}
	if st := roll.Stats(); st.Failed != 0 {
		t.Errorf("the rollup failed %d times: it wrote for a closed tenant", st.Failed)
	}
	state := func(id tenant.ID) int {
		return d.Count(t, `SELECT count(*)::int FROM pandokos.usage_rollup_state WHERE tenant_id = $1::uuid`, id.String())
	}
	if state(active) != 1 {
		t.Errorf("the active tenant has %d rollup states, want 1", state(active))
	}
	if state(deleting) != 0 || state(deleted) != 0 {
		t.Errorf("a closed tenant has a rollup state (deleting %d, deleted %d): the rollup ran for it", state(deleting), state(deleted))
	}

	job := &reports.Retention{DB: worker, UsageMonths: 12, AuditMonths: 24, Logger: jsonLogger(logs)}
	if err := job.Pass(ctx(t)); err != nil {
		t.Fatalf("the retention pass: %v", err)
	}
	st := job.Stats()
	if st.Failed != 0 {
		t.Errorf("retention failed %d times", st.Failed)
	}
	// The active tenant's old rows are gone, by tenant_retention.
	if n := d.Count(t, `SELECT count(*)::int FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'old.event'`, active.String()); n != 0 {
		t.Errorf("the active tenant keeps %d old audit rows", n)
	}
	// The tenant being deleted is skipped entirely: not one row of it changed.
	// (The platform log is a global table: the retention record of the deleted tenant is in it.)
	if got := d.TableDigests(t, deleting); fmtMap(withoutPlatformLog(got)) != fmtMap(withoutPlatformLog(deletingBefore)) {
		t.Error("retention changed a tenant that is being deleted")
	}
	// The deleted tenant: the third pass removed its old records and the prefix of its log.
	if n := d.Count(t, `SELECT count(*)::int FROM pandokos.usage_record_default WHERE tenant_id = $1::uuid`, deleted.String()); n != 0 {
		t.Errorf("the deleted tenant keeps %d records of the default partition older than the cut", n)
	}
	if n := d.Count(t, `SELECT count(*)::int FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'old.event'`, deleted.String()); n != 0 {
		t.Errorf("the deleted tenant keeps %d old audit rows", n)
	}
	if n := d.Count(t, `SELECT count(*)::int FROM pandokos.audit_event WHERE tenant_id = $1::uuid`, deleted.String()); n >= chainBefore {
		t.Errorf("the deleted tenant's log has %d rows, was %d: nothing was removed", n, chainBefore)
	}
	if n := d.Count(t, `SELECT count(*)::int FROM pandokos.platform_audit_event WHERE action = 'tenant.records_retention' AND resource_id = $1`, deleted.String()); n != 1 {
		t.Errorf("%d tenant.records_retention rows for the deleted tenant, want 1", n)
	}
	if n := d.Count(t, `SELECT count(*)::int FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action IN ('audit.retention', 'usage.retention')`, deleted.String()); n != 0 {
		t.Errorf("tenant_retention wrote %d rows in the closed log of the deleted tenant", n)
	}
	if st.Removed["deleted_tenant_usage_record"] != 1 || st.Removed["deleted_tenant_audit_event"] < 1 {
		t.Errorf("M44 counts %v, want one deleted_tenant_usage_record and at least one deleted_tenant_audit_event", st.Removed)
	}
	// The counts of the tenants that live do not include the deleted tenant's.
	if st.Removed["usage_record"] != 1 {
		t.Errorf("usage_record counts %d, want the one of the active tenant", st.Removed["usage_record"])
	}
	// A second pass removes nothing more and writes nothing.
	rows := d.Count(t, `SELECT count(*)::int FROM pandokos.platform_audit_event WHERE action = 'tenant.records_retention'`)
	if err := job.Pass(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if n := d.Count(t, `SELECT count(*)::int FROM pandokos.platform_audit_event WHERE action = 'tenant.records_retention'`); n != rows {
		t.Errorf("a second pass wrote %d more records of retention", n-rows)
	}
}

// withoutPlatformLog drops the digests of the platform's own log, which the retention of a deleted
// tenant writes to.
func withoutPlatformLog(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if k != "platform_audit_event" && k != "platform_audit_chain" {
			out[k] = v
		}
	}
	return out
}

func fmtMap(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	out := ""
	for _, k := range keys {
		out += k + "=" + m[k] + ";"
	}
	return out
}
