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

// groupMatches reports whether the counter of l counts the group: the subject matches (the tenant:
// always; a service account: PrincipalID; a key: KeyID), the provider and the model are empty or
// equal.
func groupMatches(l Limit, g *UsageGroup) bool {
	switch l.Subject {
	case SubjectServiceAccount:
		if g.PrincipalID != l.ID {
			return false
		}
	case SubjectKey:
		if g.KeyID != l.ID {
			return false
		}
	}
	return (l.Provider == "" || l.Provider == g.Provider) && (l.Model == "" || l.Model == g.Model)
}

// groupAmount is what the counter of l counts of a group: requests, tokens or, in the currency of
// the counter, cost.
func groupAmount(l Limit, g *UsageGroup) (amt, bool) {
	if !groupMatches(l, g) {
		return amt{}, false
	}
	switch l.Metric {
	case RequestsPerMinute, RequestsPerDay, RequestsPerMonth:
		return amt{n: g.Requests}, true
	case TokensPerMinute, TokensPerDay, TokensPerMonth:
		return amt{n: g.Tokens}, true
	case CostPerDay, CostPerMonth:
		if g.Currency == l.Currency {
			return amt{c: g.Cost}, true
		}
	}
	return amt{}, false
}

func amtOfSum(l Limit, rows []UsageGroup) amt {
	var out amt
	cost := l.Metric.Cost()
	for i := range rows {
		if a, ok := groupAmount(l, &rows[i]); ok {
			out = addAmt(cost, out, a)
		}
	}
	return out
}

// Sum says which groups a counter counts and adds them: the one definition of what the ledger
// counts, used by the store, the reconciler and the capability check. A group counts when the
// subject matches (the tenant: always; a service account: PrincipalID; a key: KeyID), the provider
// is empty or equal and the model is empty or equal; it adds Requests, Tokens or, when its
// Currency is the limit's, Cost.
func Sum(l Limit, rows []UsageGroup) Amount {
	a := amtOfSum(l, rows)
	return Amount{N: a.n, Cost: a.c}
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
