package limits

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// UsageGroup is what the ledger holds for requests that share the dimensions a rule can name.
type UsageGroup struct {
	Minute                       int64 // 0: before the live part
	PrincipalID, KeyID, Currency string
	ProviderID, ModelID          string // what the ledger reader fills; the only fields that decide
	Provider, Model              string // names, for display; they never decide
	Requests, Tokens             int64
	Cost                         pricing.Decimal
}

// Amount is what a counter holds: N for requests and tokens, Cost for the cost metrics.
type Amount struct {
	N    int64
	Cost pricing.Decimal
}

// groupMatches reports whether the counter of l counts the group: the subject matches (the tenant:
// always; a service account: PrincipalID; a key: KeyID), the provider ID and the model ID are empty
// in the limit or equal. Names never decide; a group without a model ID counts for the scopes all
// and provider only.
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
	return (l.ProviderID == "" || l.ProviderID == g.ProviderID) && (l.ModelID == "" || l.ModelID == g.ModelID)
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
// ID is empty or equal and the model ID is empty or equal; it adds Requests, Tokens or, when its
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

// usageTimeout bounds one ledger read.
const usageTimeout = 5 * time.Second

// Usage runs AggregateUsageForLimits in the session of the tenant.
func (l *dbLedger) Usage(ctx context.Context, t tenant.ID, from, liveFrom, to time.Time) ([]UsageGroup, error) {
	ctx, cancel := context.WithTimeout(ctx, usageTimeout)
	defer cancel()
	var out []UsageGroup
	err := l.db.WithTenant(ctx, t, func(ctx context.Context, tx store.Tx) error {
		rows, err := queries.New(tx).AggregateUsageForLimits(ctx, queries.AggregateUsageForLimitsParams{
			LiveFrom: liveFrom, FromTime: from, ToTime: to})
		if err != nil {
			return err
		}
		out = make([]UsageGroup, 0, len(rows))
		for _, r := range rows {
			tokens, err := parseTokens(r.Tokens)
			if err != nil {
				return err
			}
			cost, err := pricing.ParseDecimal(r.Cost)
			if err != nil {
				return fmt.Errorf("limits: usage cost %q: %w", r.Cost, err)
			}
			out = append(out, UsageGroup{Minute: r.Minute, PrincipalID: r.PrincipalID, KeyID: r.KeyID, ProviderID: r.ProviderID,
				ModelID: r.ModelID, Currency: r.Currency, Requests: r.Requests, Tokens: tokens, Cost: cost})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("limits: read usage: %w", err)
	}
	return out, nil
}

// parseTokens reads the numeric text of a token sum as an integer, saturating at MaxInt64.
func parseTokens(s string) (int64, error) {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) && !strings.HasPrefix(s, "-") {
			return math.MaxInt64, nil
		}
		return 0, fmt.Errorf("limits: usage tokens %q: %w", s, err)
	}
	return n, nil
}
