package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/pandokos/pandokos/internal/store"
)

const (
	// Batch is the number of rows a call of the erasure step removes at most.
	Batch = 5000
	// tenantBudget is the time one pass spends on one tenant.
	tenantBudget = 30 * time.Second
)

// DeleterStats is what the deletion job counts. Nothing in it names a tenant.
type DeleterStats struct {
	Steps map[[2]string]int64 // by step and result (ok, failed)
	Open  map[string]int64    // open deletions by state: scheduled, running
}

// Deleter runs the open deletions (§5.7).
type Deleter struct {
	DB       *store.DB
	Interval time.Duration
	Logger   *slog.Logger
	Now      func() time.Time
}

// Run passes at every tick until ctx ends.
func (d *Deleter) Run(_ context.Context) {}

// Pass runs one pass over the open deletions.
func (d *Deleter) Pass(_ context.Context) error {
	_ = tenantBudget
	return errors.New("lifecycle: not implemented")
}

// Stats returns the counters.
func (d *Deleter) Stats() DeleterStats { return DeleterStats{} }
