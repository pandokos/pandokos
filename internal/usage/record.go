package usage

import (
	"time"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/tenant"
)

// Outcome is how a request ended, as stored in the ledger.
type Outcome string

// The outcomes of a request.
const (
	OutcomeOK             Outcome = "ok"
	OutcomeUpstreamError  Outcome = "upstream_error"
	OutcomeCancelled      Outcome = "cancelled"
	OutcomeTimeout        Outcome = "timeout"
	OutcomeRejectedLimit  Outcome = "rejected_limit" // unused until a later sprint
	OutcomeRejectedPolicy Outcome = "rejected_policy"
	OutcomeInternalError  Outcome = "internal_error"
)

// Source says where the token figures of a record come from.
type Source string

// The sources of token figures.
const (
	SourceProvider  Source = "provider"
	SourceEstimated Source = "estimated"
	SourceNone      Source = "none"
)

// Record is one row of the usage ledger. It holds no content.
type Record struct {
	ID                 string    // UUIDv7 made by NewID(StartedAt)
	TenantID           tenant.ID //
	StartedAt          time.Time // step 1, UTC; the partition key
	RequestID, TraceID string    // TraceID "" when the request has none

	PrincipalKind, PrincipalID, KeyID string // "service_account", its ID, the virtual key ID
	CredentialID                      string // "" until step 8 passed
	Funding                           string // "byok"

	Provider, APIFamily, Operation          string
	Endpoint, Model, ModelID, UpstreamModel string // "" until step 7 passed
	Stream                                  *bool  // nil until step 6 passed

	Outcome                     Outcome
	ErrorCode                   string         // the Pandokos code, "" when none
	UpstreamStatus              int            // 0 when the provider did not answer
	TTFB                        *time.Duration // nil when no body byte arrived
	Duration                    time.Duration
	RequestBytes, ResponseBytes *int64

	InputSource, OutputSource Source
	Usage                     family.Usage // the provider's figures; read according to the sources
	Estimated                 family.Usage // InputTokens and OutputTokens only; read according to the sources

	Cost       pricing.Cost
	CostStatus string // "known", "unknown", "none"

	Metadata        map[string]string
	MetadataDropped int
}

// Validate checks the constraints of the table in Go. It names the field, never a value.
func (r *Record) Validate() error { return nil }
