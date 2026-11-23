package outbox

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// Janitor removes the events that retention no longer keeps, and the limit events with them
// (sprint 6.4, section 5.13). The worker role runs it.
type Janitor struct {
	DB          *store.DB
	Retention   time.Duration
	UsageMonths int
	Logger      *slog.Logger

	mu           sync.Mutex
	stats        JanitorStats
	failedPasses int
	lastFailLine time.Time
}

// JanitorStats holds the counters behind M55 and the two kinds of M44. Nothing in it names a
// tenant.
type JanitorStats struct {
	Events, Expired, LimitEvents uint64 // totals over the installation since the start
	PassesOK, PassesFailed       uint64
}

const (
	janitorEvery   = time.Hour
	janitorBatch   = 5000
	janitorTenant  = 30 * time.Second // the time one tenant may take in one pass
	janitorLineGap = time.Hour        // "event retention failing" at most once an hour
)

func (j *Janitor) log() *slog.Logger {
	if j.Logger != nil {
		return j.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// Run passes at once and then every hour, until ctx ends.
func (j *Janitor) Run(ctx context.Context) {
	t := time.NewTicker(janitorEvery)
	defer t.Stop()
	for {
		_ = j.Pass(ctx) // counted and logged by the pass
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Pass removes one batch of what is due: for each tenant that is active or suspended, the
// retention function in the session of the tenant until a call removes less than a batch, for at
// most 30 seconds. A tenant that fails does not stop the others; the pass then returns the first
// failure.
func (j *Janitor) Pass(ctx context.Context) (err error) {
	var ids []string
	err = j.DB.Platform(ctx, func(c context.Context, tx store.Tx) (err error) {
		ids, err = queries.New(tx).ListTenantsForWorker(c)
		return err
	})
	var events, expired, limitEvents uint64
	if err == nil {
		hours := int32(j.Retention / time.Hour) //nolint:gosec // 24 to 2160 by the configuration
		for _, s := range ids {
			if ctx.Err() != nil {
				err = ctx.Err()
				break
			}
			id, perr := tenant.ParseID(s)
			if perr != nil {
				continue
			}
			e, x, l, terr := j.tenant(ctx, id, hours)
			events, expired, limitEvents = events+e, expired+x, limitEvents+l
			if terr != nil && err == nil {
				err = terr
			}
		}
	}
	j.mu.Lock()
	j.stats.Events += events
	j.stats.Expired += expired
	j.stats.LimitEvents += limitEvents
	if err != nil && ctx.Err() == nil {
		j.stats.PassesFailed++
	} else if err == nil {
		j.stats.PassesOK++
		j.failedPasses = 0
	}
	var failures int
	var line bool
	if err != nil && ctx.Err() == nil {
		j.failedPasses++
		failures = j.failedPasses
		if j.lastFailLine.IsZero() || time.Since(j.lastFailLine) >= janitorLineGap {
			j.lastFailLine, line = time.Now(), true
		}
	}
	j.mu.Unlock()
	if line {
		j.log().Warn("event retention failing", slog.String("error_class", errorClass(err)), slog.String("sqlstate", store.SQLState(err)), slog.Int("failures", failures))
	}
	if events+limitEvents > 0 {
		j.log().Info("event retention applied", slog.Uint64("events", events), slog.Uint64("expired", expired), slog.Uint64("limit_events", limitEvents))
	}
	return err
}

// tenant applies the retention to one tenant, batch by batch, and returns what was removed even
// when a later batch failed.
func (j *Janitor) tenant(ctx context.Context, id tenant.ID, hours int32) (events, expired, limitEvents uint64, err error) {
	deadline := time.Now().Add(janitorTenant)
	for {
		var row queries.TenantOutboxRetentionRow
		err = j.DB.WithTenant(ctx, id, func(c context.Context, tx store.Tx) (err error) {
			row, err = queries.New(tx).TenantOutboxRetention(c, queries.TenantOutboxRetentionParams{
				EventHours: hours, UsageMonths: int32(j.UsageMonths), Batch: janitorBatch, //nolint:gosec // 0 or 2 to 120 by the configuration
			})
			return err
		})
		if err != nil {
			return events, expired, limitEvents, err
		}
		events += uint64(row.REvents)           //nolint:gosec // counts are not negative
		expired += uint64(row.RExpired)         //nolint:gosec // as above
		limitEvents += uint64(row.RLimitEvents) //nolint:gosec // as above
		if row.REvents < janitorBatch || time.Now().After(deadline) {
			return events, expired, limitEvents, nil
		}
	}
}

// Stats returns a copy of the counters.
func (j *Janitor) Stats() JanitorStats {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.stats
}
