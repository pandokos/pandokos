package resolver

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
)

const (
	bucketCapacity = 60
	bucketRate     = 1.0 // tokens per second
	suppressWindow = 10 * time.Minute
	maxSeen        = 4096
	recordTimeout  = 2 * time.Second
)

// FailureAuditor implements gateway.FailureRecorder: it writes authentication failures of the
// inference listener to the audit log of the tenant of the key, at a bounded rate.
type FailureAuditor struct {
	db  *store.DB
	log *slog.Logger
	now func() time.Time

	mu     sync.Mutex
	tokens float64
	last   time.Time
	seen   map[string]time.Time
}

// NewFailureAuditor returns a recorder that writes through db.
func NewFailureAuditor(db *store.DB, log *slog.Logger, now func() time.Time) *FailureAuditor {
	if now == nil {
		now = time.Now
	}
	return &FailureAuditor{db: db, log: log, now: now, tokens: bucketCapacity, last: now(), seen: map[string]time.Time{}}
}

// allow takes the suppression and the bucket decisions, and reserves the key: a concurrent
// identical failure is suppressed while the record is being written. It consumes a token only
// when the failure is not suppressed.
func (a *FailureAuditor) allow(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.now()
	if at, ok := a.seen[key]; ok && t.Sub(at) < suppressWindow {
		return false
	}
	if d := t.Sub(a.last).Seconds(); d > 0 {
		a.tokens += d * bucketRate
		if a.tokens > bucketCapacity {
			a.tokens = bucketCapacity
		}
		a.last = t
	}
	if a.tokens < 1 {
		return false
	}
	a.tokens--
	if len(a.seen) >= maxSeen {
		clear(a.seen)
	}
	a.seen[key] = t
	return true
}

// release drops the reservation of a key whose record could not be written, so that the next
// failure is recorded.
func (a *FailureAuditor) release(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.seen, key)
}

// Record reports whether a record was written. It writes only a failure that names a tenant,
// not repeated for the same key and reason in the last ten minutes, within the rate of the
// bucket; the write does not depend on the context of the request.
func (a *FailureAuditor) Record(ctx context.Context, f gateway.AuthFailure) bool {
	tid, err := tenant.ParseID(f.TenantID)
	if err != nil {
		return false
	}
	key := f.KeyID + "\x00" + f.Reason
	if !a.allow(key) {
		return false
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	err = a.db.WithTenant(wctx, tid, func(ctx context.Context, tx store.Tx) error {
		return audit.RecordAuthFailure(ctx, tx, f.KeyID, f.Reason, f.RequestID)
	})
	if err != nil {
		a.log.DebugContext(ctx, "authentication failure not recorded", "request_id", f.RequestID)
		a.release(key)
		return false
	}
	return true
}
