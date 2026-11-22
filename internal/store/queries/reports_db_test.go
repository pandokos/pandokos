package queries_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

// reportStatements covers the statements of sprint 6.2 (T-323 extended): the usage rollup, the
// report, the export, retention, the metadata key policy and the audit reads. The rollup is written
// by the worker role in the session of the tenant; the other tenant statements run as the admin
// role; the two platform statements as the worker (retention, tenant identifiers) and the admin role
// (the platform log).
func reportStatements(c context.Context, t *testing.T, d *dbtest.Database, admin *store.DB, _ tenant.ID, called map[string]bool) {
	t.Helper()
	// A tenant of its own: the counts below are those of the rows seeded here.
	tid := d.SeedTenant(t, "reports-statements")
	mark := func(name string) { called[name] = true }
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	worker := d.Open(t, store.RoleWorker)
	// before_seq is exclusive: the first page of a list asks for everything below the largest number.
	const firstPage = int64(1<<63 - 1)
	now := time.Now().UTC()

	// Three ledger rows of the last two hours, one of them with a known cost.
	d.SeedUsage(t, tid,
		dbtest.UsageRow{StartedAt: now.Add(-3 * time.Hour), InputTokens: 10, OutputTokens: 5, Cost: "0.5", Currency: "EUR"},
		dbtest.UsageRow{StartedAt: now.Add(-2 * time.Hour), InputTokens: 20},
		dbtest.UsageRow{StartedAt: now.Add(-90 * time.Minute), Outcome: "rejected_limit"})

	var watermark time.Time
	must("rollup statements", worker.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("LockUsageRollup")
		w, err := q.LockUsageRollup(c)
		if err != nil || w.IsZero() {
			t.Errorf("LockUsageRollup: %v %v", w, err)
		}
		if again, err := q.LockUsageRollup(c); err != nil || !again.Equal(w) {
			t.Errorf("LockUsageRollup again: %v %v, want the same watermark %v", again, err, w)
		}
		watermark = w
		mark("RollUpUsage")
		// Records recorded in [w, w + 7 days) and before now - 1 min: the rows just seeded are
		// recorded now, so none is in the window yet.
		row, err := q.RollUpUsage(c, w)
		if err != nil || row.Groups != 0 || !row.Watermark.Equal(w) {
			t.Errorf("RollUpUsage: %+v %v, want no group and the watermark unchanged (%v)", row, err, w)
		}
		return nil
	}))

	must("report statements", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		from, to := now.Add(-4*time.Hour).Truncate(time.Hour), now.Add(time.Hour)
		mark("UsageReport")
		rows, err := q.UsageReport(c, queries.UsageReportParams{ByModel: true, MaxRows: 100, Bounds: []time.Time{from, to}})
		if err != nil {
			return err
		}
		var requests, ok, rejected int64
		for _, r := range rows {
			requests += r.Requests
			ok += r.RequestsOk
			rejected += r.RejectedLimit
		}
		// A request refused by a limit is counted apart from the requests (sprint 5.2, section 11).
		if requests != 2 || ok != 2 || rejected != 1 {
			t.Errorf("UsageReport: %d requests, %d ok, %d rejected in %+v, want 2, 2 and 1", requests, ok, rejected, rows)
		}
		mark("ExportUsageRecords")
		recs, err := q.ExportUsageRecords(c, queries.ExportUsageRecordsParams{FromAt: from, ToAt: to, PageSize: 2, AfterID: zeroID})
		if err != nil || len(recs) != 2 || !recs[0].StartedAt.Before(recs[1].StartedAt) {
			t.Fatalf("ExportUsageRecords: %d rows, %v, want the two oldest first", len(recs), err)
		}
		next, err := q.ExportUsageRecords(c, queries.ExportUsageRecordsParams{FromAt: from, ToAt: to, PageSize: 2, After: true, AfterStartedAt: recs[1].StartedAt, AfterID: recs[1].ID})
		if err != nil || len(next) != 1 {
			t.Errorf("ExportUsageRecords after the page: %d rows, %v, want 1", len(next), err)
		}
		mark("AggregateUsageForLimitsFromLedger")
		ledger, err := q.AggregateUsageForLimitsFromLedger(c, queries.AggregateUsageForLimitsFromLedgerParams{FromTime: from, LiveFrom: now.Add(-time.Hour), ToTime: now.Add(time.Minute)})
		if err != nil {
			return err
		}
		reader, err := q.AggregateUsageForLimits(c, queries.AggregateUsageForLimitsParams{FromTime: from, LiveFrom: now.Add(-time.Hour), ToTime: now.Add(time.Minute)})
		if err != nil || len(reader) != len(ledger) {
			t.Errorf("AggregateUsageForLimits: %d rows, %v; the ledger reader %d rows", len(reader), err, len(ledger))
		}
		return nil
	}))

	must("metadata key policy", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("GetMetadataKeyPolicy")
		if _, err := q.GetMetadataKeyPolicy(c); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetMetadataKeyPolicy without a policy: %v, want no rows", err)
		}
		mark("SetMetadataKeyPolicy")
		set, err := q.SetMetadataKeyPolicy(c, []string{"team", "project"})
		if err != nil || len(set.Keys) != 2 || set.UpdatedAt.IsZero() {
			t.Errorf("SetMetadataKeyPolicy: %+v %v", set, err)
		}
		if set, err = q.SetMetadataKeyPolicy(c, []string{"team"}); err != nil || len(set.Keys) != 1 {
			t.Errorf("SetMetadataKeyPolicy again: %+v %v", set, err)
		}
		got, err := q.GetMetadataKeyPolicy(c)
		if err != nil || len(got.Keys) != 1 || got.Keys[0] != "team" {
			t.Errorf("GetMetadataKeyPolicy: %+v %v", got, err)
		}
		mark("ClearMetadataKeyPolicy")
		if n, err := q.ClearMetadataKeyPolicy(c); err != nil || n != 1 {
			t.Errorf("ClearMetadataKeyPolicy: %d %v", n, err)
		}
		if n, err := q.ClearMetadataKeyPolicy(c); err != nil || n != 0 {
			t.Errorf("ClearMetadataKeyPolicy again: %d %v", n, err)
		}
		return nil
	}))

	// The audit reads: three rows of the tenant's log, written as the owner (sealed by the trigger).
	for i := 0; i < 3; i++ {
		if _, err := d.Super(t).Exec(c, `INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_name, action, resource_type, resource_id, outcome)
			VALUES ($1::uuid, 'platform', 'statements', 'test.read', 'test', $2, 'success')`, tid.String(), string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	must("audit read statements", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("GetAuditHead")
		head, err := q.GetAuditHead(c)
		if err != nil || head.Seq < 3 || len(head.Hash) != 32 || head.AnchorSeq != 1 || len(head.AnchorHash) != 32 {
			t.Fatalf("GetAuditHead: %+v %v", head, err)
		}
		mark("ListAuditEvents")
		list, err := q.ListAuditEvents(c, queries.ListAuditEventsParams{PageSize: 100, BeforeSeq: firstPage})
		if err != nil || int64(len(list)) != head.Seq || list[0].Seq != head.Seq || list[len(list)-1].Seq != 1 {
			t.Errorf("ListAuditEvents: %d rows, %v, want %d, newest first", len(list), err, head.Seq)
		}
		filtered, err := q.ListAuditEvents(c, queries.ListAuditEventsParams{PageSize: 100, BeforeSeq: firstPage, Action: "test.read", ResourceID: "b"})
		if err != nil || len(filtered) != 1 {
			t.Errorf("ListAuditEvents filtered: %d rows, %v, want 1", len(filtered), err)
		}
		page, err := q.ListAuditEvents(c, queries.ListAuditEventsParams{PageSize: 100, BeforeSeq: head.Seq})
		if err != nil || len(page) != len(list)-1 {
			t.Errorf("ListAuditEvents before the head: %d rows, %v", len(page), err)
		}
		mark("ExportAuditEvents")
		exp, err := q.ExportAuditEvents(c, queries.ExportAuditEventsParams{AfterSeq: 0, ToSeq: head.Seq, PageSize: 2})
		if err != nil || len(exp) != 2 || exp[0].Seq != 1 || exp[1].Seq != 2 {
			t.Errorf("ExportAuditEvents: %d rows, %v, want seq 1 and 2", len(exp), err)
		}
		return nil
	}))

	// The platform: the log is read in platform scope by the admin role; the worker lists tenant
	// identifiers and runs the retention of the platform.
	must("platform statements", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("GetPlatformAuditHead")
		head, err := q.GetPlatformAuditHead(c)
		if err != nil || head.Seq < 1 || len(head.Hash) != 32 {
			t.Fatalf("GetPlatformAuditHead: %+v %v", head, err)
		}
		mark("ListPlatformAuditEvents")
		list, err := q.ListPlatformAuditEvents(c, queries.ListPlatformAuditEventsParams{PageSize: 1000, BeforeSeq: firstPage})
		if err != nil || int64(len(list)) != head.Seq-head.AnchorSeq+1 || list[0].Seq != head.Seq {
			t.Errorf("ListPlatformAuditEvents: %d rows, %v, head %+v", len(list), err, head)
		}
		mark("ExportPlatformAuditEvents")
		exp, err := q.ExportPlatformAuditEvents(c, queries.ExportPlatformAuditEventsParams{AfterSeq: 0, ToSeq: head.Seq, PageSize: 1000})
		if err != nil || len(exp) != len(list) || exp[0].Seq != head.AnchorSeq {
			t.Errorf("ExportPlatformAuditEvents: %d rows, %v", len(exp), err)
		}
		return nil
	}))
	must("worker platform statements", worker.Platform(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("ListTenantsForWorker")
		ids, err := q.ListTenantsForWorker(c)
		found := false
		for _, id := range ids {
			found = found || id == tid.String()
		}
		if err != nil || !found {
			t.Errorf("ListTenantsForWorker: %v %v, want the tenant of the test among the identifiers", ids, err)
		}
		mark("PlatformRetention")
		if r, err := q.PlatformRetention(c, queries.PlatformRetentionParams{UsageMonths: 0, AuditMonths: 0}); err != nil || r.PartitionsDropped != 0 || r.AuditRows != 0 {
			t.Errorf("PlatformRetention(0, 0): %+v %v, want nothing removed", r, err)
		}
		return nil
	}))
	// A value out of range is refused by the function; the transaction fails with it.
	if err := worker.Platform(c, func(c context.Context, tx store.Tx) error {
		_, err := queries.New(tx).PlatformRetention(c, queries.PlatformRetentionParams{UsageMonths: 1, AuditMonths: 0})
		return err
	}); err == nil {
		t.Error("PlatformRetention(1, 0) was accepted")
	}
	must("worker tenant statements", worker.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("TenantRetention")
		if r, err := q.TenantRetention(c, queries.TenantRetentionParams{UsageMonths: 0, AuditMonths: 0}); err != nil || r.UsageRows != 0 || r.RollupRows != 0 || r.AuditRows != 0 {
			t.Errorf("TenantRetention(0, 0): %+v %v, want nothing removed", r, err)
		}
		return nil
	}))
	_ = watermark
}
