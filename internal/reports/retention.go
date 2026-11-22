package reports

import (
	"context"
	"log/slog"

	"github.com/pandokos/pandokos/internal/store"
)

// Retention removes usage and audit rows older than the configured months, through the two
// functions of migration 0015 (tenant_retention, platform_retention), and nothing else.
type Retention struct {
	DB          *store.DB
	UsageMonths int // 0 keeps everything, otherwise 2 to 120
	AuditMonths int // 0 keeps everything, otherwise 1 to 1200
	Logger      *slog.Logger
}

// RetentionStats are the counters of a Retention.
type RetentionStats struct {
	Runs, Failed int64
	// Removed is what the runs removed, by kind (the values of telemetry.AttrRetentionKind).
	Removed map[string]int64
}

// Run runs a pass at once and then every hour until ctx ends; with both months 0 it returns at
// once. Sprint 6.2 part C6 builds it.
func (r *Retention) Run(ctx context.Context) { _ = ctx }

// Pass runs retention once. Sprint 6.2 part C6 builds it.
func (r *Retention) Pass(ctx context.Context) error { _ = ctx; return nil }

// Stats returns the counters.
func (r *Retention) Stats() RetentionStats { return RetentionStats{Removed: map[string]int64{}} }
