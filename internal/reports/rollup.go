package reports

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

const (
	// rollupBudget is how long one pass works on one tenant before it leaves the rest to the next
	// tick (section 5.1).
	rollupBudget = 30 * time.Second
	// rollupCaughtUp is how far behind the clock a watermark may be for the tenant to be done.
	rollupCaughtUp = 2 * time.Minute
	// failureLogEvery limits the line "usage rollup failing".
	failureLogEvery = time.Minute
)

// Rollup adds the usage ledger to the usage rollup, tenant by tenant, in the session of each
// tenant (ADR 0054: the platform reads no usage of a tenant).
type Rollup struct {
	DB       *store.DB
	Interval time.Duration
	Logger   *slog.Logger
	// Now is the clock; time.Now when nil.
	Now func() time.Time

	mu         sync.Mutex
	stats      RollupStats
	failures   int64     // tenant passes that failed since the last clean pass
	lastWarned time.Time // when "usage rollup failing" was last written
}

// RollupStats are the counters of a Rollup.
type RollupStats struct {
	OK, Failed int64 // tenant passes by result
	// Lag is the largest now - watermark seen in the last pass.
	Lag time.Duration
}

func (r *Rollup) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Rollup) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// Run runs a pass at once and then every Interval (one minute when unset) until ctx ends.
func (r *Rollup) Run(ctx context.Context) {
	interval := r.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
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

// Pass rolls up every tenant once. A tenant whose pass fails is counted and logged, and the others
// go on; the error returned is that of listing the tenants.
func (r *Rollup) Pass(ctx context.Context) error {
	var ids []string
	err := r.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		ids, err = queries.New(tx).ListTenantsForWorker(ctx)
		return err
	})
	if err != nil {
		r.fail(err)
		return err
	}
	var lag time.Duration
	failed := false
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		tid, err := tenant.ParseID(id)
		if err != nil {
			continue
		}
		l, err := r.rollUpTenant(ctx, tid)
		r.mu.Lock()
		if err != nil {
			r.stats.Failed++
		} else {
			r.stats.OK++
		}
		r.mu.Unlock()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failed = true
			r.fail(err)
			continue
		}
		lag = max(lag, l)
	}
	r.mu.Lock()
	r.stats.Lag = lag
	recovered := !failed && r.failures > 0
	n := r.failures
	if recovered {
		r.failures = 0
	}
	r.mu.Unlock()
	if recovered {
		r.logger().Info("usage rollup recovered", "failures", n)
	}
	return nil
}

// rollUpTenant moves the watermark of one tenant towards now, in transactions of the tenant's
// session, and returns how far behind it was left.
func (r *Rollup) rollUpTenant(ctx context.Context, tid tenant.ID) (time.Duration, error) {
	deadline := time.Now().Add(rollupBudget)
	var wm time.Time
	for {
		err := r.DB.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
			q := queries.New(tx)
			cur, err := q.LockUsageRollup(ctx)
			if err != nil {
				return err
			}
			next, err := q.RollUpUsage(ctx, cur)
			if err != nil {
				return err
			}
			wm = next.Watermark
			return nil
		})
		if err != nil {
			return 0, err
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if r.now().Sub(wm) <= rollupCaughtUp || !time.Now().Before(deadline) {
			break
		}
	}
	return max(r.now().Sub(wm), 0), nil
}

// fail counts a failure and writes the line, at most once a minute and never with a tenant.
func (r *Rollup) fail(err error) {
	r.mu.Lock()
	r.failures++
	n := r.failures
	due := r.now().Sub(r.lastWarned) >= failureLogEvery
	if due {
		r.lastWarned = r.now()
	}
	r.mu.Unlock()
	if due {
		r.logger().Warn("usage rollup failing", "error_class", errorClass(err), "sqlstate", store.SQLState(err), "failures", n)
	}
}

// errorClass names a failure without its text: the text could hold data.
func errorClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case store.IsUnavailable(err):
		return "unavailable"
	case store.SQLState(err) != "":
		return "database"
	default:
		return "other"
	}
}

// Stats returns the counters.
func (r *Rollup) Stats() RollupStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats
}
