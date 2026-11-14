package ledger

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

const (
	ensureTimeout   = 10 * time.Second
	defaultAhead    = 3
	defaultInterval = time.Hour
)

// Partitioner keeps the monthly partitions of the ledger ahead of the clock. It runs on the
// worker role.
type Partitioner struct {
	DB       *store.DB    // the worker role
	Logger   *slog.Logger // required
	Interval time.Duration
	Ahead    int

	warned atomic.Bool // the default partition was reported once
}

// Ensure creates the missing partitions once and logs what it did.
func (p *Partitioner) Ensure(ctx context.Context) error {
	ahead := p.Ahead
	if ahead <= 0 || ahead > 12 {
		ahead = defaultAhead
	}
	ctx, cancel := context.WithTimeout(ctx, ensureTimeout)
	defer cancel()
	var res queries.EnsureUsagePartitionsRow
	err := p.DB.Unscoped(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		res, err = queries.New(tx).EnsureUsagePartitions(ctx, int32(ahead)) //nolint:gosec // 1 to 12
		return err
	})
	if err != nil {
		return fmt.Errorf("ledger: ensure usage partitions: %w", err)
	}
	if res.Created > 0 {
		p.Logger.Info("usage partitions created", "count", res.Created)
	}
	if res.DefaultInUse && p.warned.CompareAndSwap(false, true) {
		p.Logger.Warn("usage records in the default partition")
	}
	return nil
}

// Run calls Ensure every Interval until ctx ends. A failure is logged at warn.
func (p *Partitioner) Run(ctx context.Context) {
	interval := p.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := p.Ensure(ctx); err != nil && ctx.Err() == nil {
			p.Logger.Warn("usage partitions not ensured", "error", err.Error())
		}
	}
}
