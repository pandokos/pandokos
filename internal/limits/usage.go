package limits

import (
	"context"
	"time"

	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
)

// UsageGroup is what the ledger holds for requests that share the dimensions a rule can name.
type UsageGroup struct {
	Minute                                        int64 // 0: before the live part
	PrincipalID, KeyID, Provider, Model, Currency string
	Requests, Tokens                              int64
	Cost                                          pricing.Decimal
}

// Amount is what a counter holds: N for requests and tokens, Cost for the cost metrics.
type Amount struct {
	N    int64
	Cost pricing.Decimal
}

// Sum says which groups a counter counts and adds them: the one definition of what the ledger
// counts, used by the store, the reconciler and the capability check.
func Sum(_ Limit, _ []UsageGroup) Amount {
	return Amount{}
}

// Ledger reads what the ledger holds for the counters.
type Ledger interface {
	// Usage aggregates the requests that started in [from, to); those that started at or after
	// liveFrom are grouped by minute.
	Usage(ctx context.Context, t tenant.ID, from, liveFrom, to time.Time) ([]UsageGroup, error)
}

type dbLedger struct{ db *store.DB }

// NewLedger returns the reader of the ledger on a pool (the worker pool, or the admin pool for
// the capability check).
func NewLedger(db *store.DB) Ledger { return &dbLedger{db: db} }

func (l *dbLedger) Usage(context.Context, tenant.ID, time.Time, time.Time, time.Time) ([]UsageGroup, error) {
	return nil, errNotImplemented
}
