package resolver

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// UnlimitedNoter is told about the first unlimited request of a tenant in a day of the tenant
// (sprint 5.3, section 5.6). Note never blocks.
type UnlimitedNoter interface {
	Note(t tenant.ID, day, zone, provider, model, requestID string)
}

// The bounds of the auditor.
const (
	unlimitedQueue   = 256              // entries waiting for the writer
	unlimitedMaxSeen = 10_000           // tenants remembered; the memory is emptied when full
	unlimitedTimeout = 2 * time.Second  // one write
	unlimitedBackoff = 10 * time.Second // a tenant whose write failed or was dropped is not offered again for this long
)

// unlimitedEntry is what a request offers: one day of one tenant.
type unlimitedEntry struct {
	tenant                                tenant.ID
	day, zone, provider, model, requestID string
}

// seenDay is what the auditor remembers of a tenant: the last day recorded or offered, and, after a
// failure, the instant before which the tenant is not offered again.
type seenDay struct {
	day   string
	retry time.Time
}

// UnlimitedAuditor records, once per tenant and day, that a request was admitted with no limit rule
// applying to it, through pandokos.record_unlimited_traffic on the gateway pool in the tenant's
// session. Note costs one map lookup; one goroutine (Run) does the writing. The database makes the
// row unique per tenant and day, so that any number of instances and restarts leave one.
type UnlimitedAuditor struct {
	db  *store.DB
	log *slog.Logger
	now func() time.Time

	mu   sync.Mutex
	seen map[tenant.ID]seenDay
	q    chan unlimitedEntry

	written, skipped, failed atomic.Int64
}

// UnlimitedStats is a snapshot of the counters of an UnlimitedAuditor.
type UnlimitedStats struct {
	Written, Skipped, Failed int64 // rows inserted; calls that found the day's row there; writes that did not happen
}

// Stats returns the counters.
func (a *UnlimitedAuditor) Stats() UnlimitedStats {
	return UnlimitedStats{Written: a.written.Load(), Skipped: a.skipped.Load(), Failed: a.failed.Load()}
}

var _ UnlimitedNoter = (*UnlimitedAuditor)(nil)

// NewUnlimitedAuditor returns an auditor that writes on db and takes its time from now (time.Now
// when nil).
func NewUnlimitedAuditor(db *store.DB, logger *slog.Logger, now func() time.Time) *UnlimitedAuditor {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &UnlimitedAuditor{db: db, log: logger, now: now, seen: map[tenant.ID]seenDay{}, q: make(chan unlimitedEntry, unlimitedQueue)}
}

// Note offers the day of a tenant to the auditor: day is the civil date of the request in the
// tenant's zone ("2006-01-02"), zone the name of that zone. It never blocks and never reaches the
// database; a day already recorded or offered costs a map lookup.
func (a *UnlimitedAuditor) Note(t tenant.ID, day, zone, provider, model, requestID string) {
	if day == "" {
		return
	}
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.seen[t]
	if ok && (s.day == day || now.Before(s.retry)) {
		return
	}
	if !ok && len(a.seen) >= unlimitedMaxSeen {
		clear(a.seen)
	}
	select {
	case a.q <- unlimitedEntry{tenant: t, day: day, zone: zone, provider: provider, model: model, requestID: requestID}:
		a.seen[t] = seenDay{day: day}
	default:
		a.failed.Add(1) // the day is dropped: it is not written now
		a.seen[t] = seenDay{retry: now.Add(unlimitedBackoff)}
	}
}

// forget removes the day of a tenant whose write failed, and holds the tenant back for the backoff.
func (a *UnlimitedAuditor) forget(t tenant.ID) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seen[t] = seenDay{retry: a.now().Add(unlimitedBackoff)}
}

// Run writes what Note queued until ctx ends.
func (a *UnlimitedAuditor) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-a.q:
			a.write(e)
		}
	}
}

// Close writes what is queued, waiting until ctx ends.
func (a *UnlimitedAuditor) Close(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-a.q:
			a.write(e)
		default:
			return
		}
	}
}

// write makes one call of the definer function. A failure is a debug line: the row is missing only
// if the database stays unwritable until the end of the day.
func (a *UnlimitedAuditor) write(e unlimitedEntry) {
	day, err := time.Parse("2006-01-02", e.day)
	if err != nil {
		a.failed.Add(1)
		a.forget(e.tenant)
		a.log.Debug("unlimited traffic not recorded", "request_id", e.requestID, "error_class", "day")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), unlimitedTimeout)
	defer cancel()
	var recorded bool
	err = a.db.WithTenant(ctx, e.tenant, func(ctx context.Context, tx store.Tx) error {
		var err error
		recorded, err = queries.New(tx).RecordUnlimitedTraffic(ctx, queries.RecordUnlimitedTrafficParams{
			Day: store.Date(day), TimeZone: e.zone, Provider: e.provider, Model: e.model, RequestID: e.requestID,
		})
		return err
	})
	if err != nil {
		a.failed.Add(1)
		a.forget(e.tenant)
		a.log.Debug("unlimited traffic not recorded", "request_id", e.requestID, "error_class", errorClass(err))
		return
	}
	if recorded {
		a.written.Add(1)
	} else {
		a.skipped.Add(1)
	}
}
