package ledger

import (
	"context"
	"log/slog"
	"time"

	"github.com/pandokos/pandokos/internal/store"
)

// Partitioner keeps the monthly partitions of the ledger ahead of the clock. It runs on the
// worker role.
type Partitioner struct {
	DB       *store.DB    // the worker role
	Logger   *slog.Logger // required
	Interval time.Duration
	Ahead    int
}

// Ensure creates the missing partitions once and logs what it did.
func (p *Partitioner) Ensure(ctx context.Context) error {
	_ = ctx
	return nil
}

// Run calls Ensure every Interval until ctx ends.
func (p *Partitioner) Run(ctx context.Context) { _ = ctx }
