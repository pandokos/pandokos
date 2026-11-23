package reports

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

const (
	// retentionEvery is how often a pass runs after the first (section 5.10).
	retentionEvery = time.Hour
	// retentionWarnEvery limits the line "retention failing".
	retentionWarnEvery = time.Hour
)

// Retention removes usage and audit rows older than the configured months, through the two
// functions of migration 0015 (tenant_retention, platform_retention), and nothing else: it holds no
// statement that deletes, and with both months 0 it calls no function.
type Retention struct {
	DB          *store.DB
	UsageMonths int // 0 keeps everything, otherwise 2 to 120
	AuditMonths int // 0 keeps everything, otherwise 1 to 1200
	Logger      *slog.Logger

	mu         sync.Mutex
	stats      RetentionStats
	failures   int64
	lastWarned time.Time
}

// RetentionStats are the counters of a Retention.
type RetentionStats struct {
	// Runs counts the passes that did work, Failed those among them that failed in any part.
	Runs, Failed int64
	// Removed is what the runs removed, by kind (the values of telemetry.AttrRetentionKind).
	Removed map[string]int64
}

func (r *Retention) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// Run says that retention is ready, then runs a pass at once and every hour until ctx ends; with
// both months 0 it returns after the line.
func (r *Retention) Run(ctx context.Context) {
	r.logger().Info("retention ready", "usage_months", r.UsageMonths, "audit_months", r.AuditMonths)
	if r.UsageMonths == 0 && r.AuditMonths == 0 {
		return
	}
	t := time.NewTicker(retentionEvery)
	defer t.Stop()
	for {
		_ = r.Pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Pass runs retention once: the platform first (the ledger partitions and the platform log), then
// each tenant in its own session. A tenant that fails is counted and logged and the others go on;
// when the platform pass fails the tenant pass does not run, because it removes the rollup of the
// months whose ledger the platform pass could not drop, and a report of those months would be
// partial. The error returned is the first failure.
func (r *Retention) Pass(ctx context.Context) error {
	if r.UsageMonths == 0 && r.AuditMonths == 0 {
		return nil
	}
	var first error
	note := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	removed := map[string]int64{}

	err := r.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		res, err := queries.New(tx).PlatformRetention(ctx, queries.PlatformRetentionParams{
			UsageMonths: months(r.UsageMonths), AuditMonths: months(r.AuditMonths)})
		if err != nil {
			return err
		}
		removed["usage_partition"] += int64(res.PartitionsDropped)
		removed["platform_audit_event"] += res.AuditRows
		return nil
	})
	note(err)
	if err != nil {
		// The tenants' rollup of the months before the cut must outlive the ledger of those months:
		// when the partitions could not be dropped, the tenant pass does not remove the rollup either.
		return r.finish(first, removed)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	var ids []string
	note(r.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		ids, err = queries.New(tx).ListTenantsForWorker(ctx)
		return err
	}))
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		tid, err := tenant.ParseID(id)
		if err != nil {
			continue
		}
		var res queries.TenantRetentionRow
		err = r.DB.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
			var err error
			res, err = queries.New(tx).TenantRetention(ctx, queries.TenantRetentionParams{
				UsageMonths: months(r.UsageMonths), AuditMonths: months(r.AuditMonths)})
			return err
		})
		if err != nil {
			note(err)
			continue
		}
		removed["usage_record"] += res.UsageRows
		removed["usage_rollup"] += res.RollupRows
		removed["audit_event"] += res.AuditRows
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// The third pass, for the tenants that are deleted: the ledger and the log kept for them (§5.6).
	// The function records its removal in the platform log, since the closed chain takes no row.
	var gone []string
	note(r.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		gone, err = queries.New(tx).ListDeletedTenantsForWorker(ctx)
		return err
	}))
	for _, id := range gone {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		tid, err := tenant.ParseID(id)
		if err != nil {
			continue
		}
		var res queries.DeletedTenantRetentionRow
		err = r.DB.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
			var err error
			res, err = queries.New(tx).DeletedTenantRetention(ctx, queries.DeletedTenantRetentionParams{
				UsageMonths: months(r.UsageMonths), AuditMonths: months(r.AuditMonths)})
			return err
		})
		if err != nil {
			note(err)
			continue
		}
		if res.UsageRows != nil {
			removed["deleted_tenant_usage_record"] += *res.UsageRows
		}
		if res.AuditRows != nil {
			removed["deleted_tenant_audit_event"] += *res.AuditRows
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return r.finish(first, removed)
}

// finish records the outcome of a pass: the counters, and the log lines.
func (r *Retention) finish(first error, removed map[string]int64) error {
	failed := first

	r.mu.Lock()
	r.stats.Runs++
	if failed != nil {
		r.stats.Failed++
		r.failures++
	}
	if r.stats.Removed == nil {
		r.stats.Removed = map[string]int64{}
	}
	for k, n := range removed {
		r.stats.Removed[k] += n
	}
	n := r.failures
	due := failed != nil && time.Since(r.lastWarned) >= retentionWarnEvery
	if due {
		r.lastWarned = time.Now()
	}
	if failed == nil {
		r.failures = 0
	}
	r.mu.Unlock()

	if due {
		r.logger().Warn("retention failing", "error_class", errorClass(failed), "sqlstate", store.SQLState(failed), "failures", n)
	}
	if removed["usage_partition"]+removed["usage_record"]+removed["audit_event"]+removed["platform_audit_event"] > 0 {
		r.logger().Info("retention applied", "partitions", removed["usage_partition"], "usage_records", removed["usage_record"],
			"audit_events", removed["audit_event"], "platform_audit_events", removed["platform_audit_event"])
	}
	return first
}

// Stats returns the counters.
func (r *Retention) Stats() RetentionStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := RetentionStats{Runs: r.stats.Runs, Failed: r.stats.Failed, Removed: map[string]int64{}}
	for k, v := range r.stats.Removed {
		out.Removed[k] = v
	}
	return out
}

// months converts a configured number of months, which the configuration bounds to 1200.
func months(n int) int32 { return int32(min(max(n, 0), 1200)) } //nolint:gosec // clamped to 0..1200
