package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/pandokos/pandokos/internal/store"
)

// Janitor removes the events that retention no longer keeps, and the limit events with them
// (sprint 6.4, section 5.13). The worker role runs it.
type Janitor struct {
	DB          *store.DB
	Retention   time.Duration
	UsageMonths int
	Logger      *slog.Logger
}

// JanitorStats holds the counters behind M55 and the two kinds of M44. Nothing in it names a
// tenant.
type JanitorStats struct {
	Events, Expired, LimitEvents uint64 // totals over the installation since the start
	PassesOK, PassesFailed       uint64
}

// Run passes until ctx ends.
func (j *Janitor) Run(ctx context.Context) { <-ctx.Done() }

// Pass removes one batch of what is due.
func (j *Janitor) Pass(ctx context.Context) error {
	_ = ctx
	return errNotBuilt
}

// Stats returns a copy of the counters.
func (j *Janitor) Stats() JanitorStats { return JanitorStats{} }
