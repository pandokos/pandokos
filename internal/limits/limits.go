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
)

// Metrics returns every metric in the order of resolution within a subject.
func Metrics() []Metric {
	return []Metric{ConcurrentRequests, RequestsPerMinute, TokensPerMinute}
}

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

// Limit is one effective limit a request is reserved against.
type Limit struct {
	Tenant  tenant.ID
	Subject SubjectKind
	ID      string // "" for the tenant; a service account ID; a key ID or a subject mapping ID
	Metric  Metric
	Max     int64  // 1 to 1e12
	Level   string // "platform" or "tenant": who set the value in force (diagnostic only)
}

// Amounts is what a request reserves, or what it consumed. One request is implied.
type Amounts struct {
	Tokens int64
	Cost   pricing.Cost // carried for sprint 5.2; no metric of sprint 5.1 reads it
}

// Request is what a call asks the store to reserve.
type Request struct {
	ID       string        // the request ID: diagnostics only
	Limits   []Limit       // all of one tenant; may be empty
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
	RetryAfter time.Duration // 1 s to 61 s, the longest over every limit that had no room
	Reset      time.Duration // until the refused counter is empty if nothing is admitted; 0 for concurrency
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
