package reports

import (
	"context"
	"log/slog"
	"time"

	"github.com/pandokos/pandokos/internal/store"
)

// Rollup adds the usage ledger to the usage rollup, tenant by tenant, in the session of each
// tenant (ADR 0054: the platform reads no usage of a tenant).
type Rollup struct {
	DB       *store.DB
	Interval time.Duration
	Logger   *slog.Logger
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

// RollupStats are the counters of a Rollup.
type RollupStats struct {
	OK, Failed int64 // tenant passes by result
	// Lag is the largest now - watermark seen in the last pass.
	Lag time.Duration
}

// Run runs a pass every Interval until ctx ends. Sprint 6.2 part C3 builds it.
func (r *Rollup) Run(ctx context.Context) { <-ctx.Done() }

// Pass rolls up every tenant once. Sprint 6.2 part C3 builds it.
func (r *Rollup) Pass(ctx context.Context) error { _ = ctx; return nil }

// Stats returns the counters.
func (r *Rollup) Stats() RollupStats { return RollupStats{} }
