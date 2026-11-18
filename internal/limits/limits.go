// Package limits holds the counters a request is reserved against before it reaches a provider
// credential, and the rules that say how large they may grow (sprint 5.1). It sees numbers and
// identifiers only: no body, header value, credential or model output ever reaches it.
package limits

import (
	"context"
	"errors"
	"time"

	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/tenant"
)

// Metric is what a limit counts.
type Metric string

// The metrics of this sprint, in the order of resolution within a subject.
const (
	ConcurrentRequests Metric = "concurrent_requests"
	RequestsPerMinute  Metric = "requests_per_minute"
	TokensPerMinute    Metric = "tokens_per_minute" //nolint:gosec // a metric name, not a credential
	RequestsPerDay     Metric = "requests_per_day"
	RequestsPerMonth   Metric = "requests_per_month"
	TokensPerDay       Metric = "tokens_per_day"   //nolint:gosec // a metric name, not a credential
	TokensPerMonth     Metric = "tokens_per_month" //nolint:gosec // a metric name, not a credential
	CostPerDay         Metric = "cost_per_day"
	CostPerMonth       Metric = "cost_per_month"
)

// CostScale is the number of millionths in one unit of a currency: the value of a cost limit is
// stored in millionths.
const CostScale = 1_000_000

// Metrics returns every metric in the order of resolution within a subject and a scope.
func Metrics() []Metric {
	return []Metric{
		ConcurrentRequests, RequestsPerMinute, TokensPerMinute,
		RequestsPerDay, RequestsPerMonth, TokensPerDay, TokensPerMonth, CostPerDay, CostPerMonth,
	}
}

// Period returns the kind of period a metric is counted over; false for the three metrics of
// sprint 5.1, which have no calendar period.
func (m Metric) Period() (PeriodKind, bool) {
	switch m {
	case RequestsPerDay, TokensPerDay, CostPerDay:
		return PerDay, true
	case RequestsPerMonth, TokensPerMonth, CostPerMonth:
		return PerMonth, true
	}
	return 0, false
}

// Cost reports whether the metric counts money rather than requests or tokens.
func (m Metric) Cost() bool { return m == CostPerDay || m == CostPerMonth }

// SubjectKind is what a limit applies to.
type SubjectKind string

// The subjects of a limit.
const (
	SubjectTenant         SubjectKind = "tenant"
	SubjectServiceAccount SubjectKind = "service_account"
	SubjectKey            SubjectKind = "key"
)

// SubjectKinds returns every subject kind in the order of resolution.
func SubjectKinds() []SubjectKind {
	return []SubjectKind{SubjectTenant, SubjectServiceAccount, SubjectKey}
}

// The levels that can set the value of a limit in force.
const (
	LevelPlatform = "platform"
	LevelTenant   = "tenant"
)

// ErrNotOutstanding is returned by Settle and Release for a reservation that was already
// settled, released or expired.
var ErrNotOutstanding = errors.New("limits: reservation is not outstanding")

// ErrNotLoaded is returned by Reserve for a period counter whose period started before the
// tenant's cut and that was not loaded from the ledger.
var ErrNotLoaded = errors.New("limits: period counter is not loaded")

// Limit is one effective limit a request is reserved against.
type Limit struct {
	Tenant          tenant.ID
	Subject         SubjectKind
	ID              string // "" for the tenant; a service account ID; a key ID or a subject mapping ID
	Provider, Model string // scope; "" means every
	Metric          Metric
	Currency        string // cost metrics
	Max             int64  // 0: not enforced. Cost metrics: millionths of the currency
	Level, RuleID   string // the rule that set Max (of the first rule when none is enforced); "platform" or "tenant"
	Period          Period // zero for the three metrics of 5.1
	Marks           []Mark // thresholds, ascending by At
}

// Mark is one threshold of one rule, as an absolute value in the unit of Max.
type Mark struct {
	At      int64 // ceil(value x percent / 100)
	Percent int
	RuleID  string
	Level   string
	Value   int64 // limit_value of the rule
}

// Amounts is what a request reserves, or what it consumed. One request is implied.
type Amounts struct {
	Tokens int64
	Cost   pricing.Cost // read by the cost metrics
}

// Request is what a call asks the store to reserve.
type Request struct {
	ID       string        // the request ID: diagnostics only
	Limits   []Limit       // all of one tenant; may be empty
	Start    time.Time     // start of the request: the period its amounts belong to
	Estimate Amounts       // what the call is expected to consume
	TTL      time.Duration // after it an unsettled reservation is released by the store
}

// Reservation is a value: a store over the network needs nothing else to settle it.
type Reservation struct {
	ID      string
	Limits  []Limit
	Amounts Amounts
	At      time.Time // admission time: the second the amounts are attributed to
	Expires time.Time
}

// Refusal says why a reservation was not taken.
type Refusal struct {
	Limit      Limit         // the first limit, in the order of resolution, that had no room
	Remaining  int64         // Max minus what is counted, never negative
	RetryAfter time.Duration // at least 1 s, the longest over every limit that had no room: up to 61 s for a rate limit, the time to the end of the period for a period limit
	Reset      time.Duration // until the refused counter is empty if nothing is admitted (the end of the period for a period limit); 0 for concurrency
	OverLimit  bool          // the estimate alone exceeds Limit.Max: waiting does not help
}

// Store holds the counters. All three methods are safe for concurrent use and never block on I/O
// in the in-process implementation.
type Store interface {
	// Reserve takes Estimate (and one request, and one slot) against every limit, or nothing.
	// Exactly one of the Reservation and the Refusal is meaningful; an error means the decision
	// could not be taken and nothing was reserved.
	Reserve(ctx context.Context, r Request) (Reservation, *Refusal, error)
	// Settle replaces the reservation by actual and frees the slot. ErrNotOutstanding when the
	// reservation was already settled, released or expired; then nothing changes.
	Settle(ctx context.Context, res Reservation, actual Amounts) error
	// Release returns everything the reservation took. ErrNotOutstanding as for Settle.
	Release(ctx context.Context, res Reservation) error
}
