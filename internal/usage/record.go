package usage

import (
	"strings"
	"time"
	"unicode/utf8"

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
func (r *Record) Validate() error {
	for _, c := range r.checks() {
		if !c.ok {
			return fieldError(c.field)
		}
	}
	return nil
}

// fieldError is a violated constraint. Its text names the column and never holds a value.
type fieldError string

func (e fieldError) Error() string { return "usage: invalid " + string(e) }

type check struct {
	field string
	ok    bool
}

func (r *Record) checks() []check {
	u, e := &r.Usage, &r.Estimated
	status := r.CostStatus
	known := r.Cost.Known
	return []check{
		{"request_id", matchHex(r.RequestID, 32, 32)},
		{"trace_id", r.TraceID == "" || matchHex(r.TraceID, 32, 32)},
		{"principal_kind", r.PrincipalKind == "service_account"},
		{"principal_id", len(r.PrincipalID) == 36 && allOf(r.PrincipalID, "0123456789abcdef-")},
		{"key_id", len(r.KeyID) <= 64 && allOf(r.KeyID, alnum+"_-")},
		{"funding", r.Funding == "byok"},
		{"provider", inRange(r.Provider, 1, 63) && allOf(r.Provider, lowerAlnum+"-")},
		{"api_family", inRange(r.APIFamily, 1, 63) && allOf(r.APIFamily, lowerAlnum+"-")},
		{"operation", inRange(r.Operation, 1, 63) && allOf(r.Operation, lowerAlnum+"._-")},
		{"endpoint", utf8.RuneCountInString(r.Endpoint) <= 63},
		{"model", utf8.RuneCountInString(r.Model) <= 200},
		{"upstream_model", utf8.RuneCountInString(r.UpstreamModel) <= 200},
		{"outcome", validOutcome(r.Outcome)},
		{"error_code", len(r.ErrorCode) <= 40 && allOf(r.ErrorCode, "abcdefghijklmnopqrstuvwxyz_")},
		{"upstream_status", r.UpstreamStatus == 0 || (r.UpstreamStatus >= 100 && r.UpstreamStatus <= 599)},
		{"duration_ms", r.Duration >= 0},
		{"ttfb_ms", r.TTFB == nil || *r.TTFB >= 0},
		{"request_bytes", r.RequestBytes == nil || *r.RequestBytes >= 0},
		{"response_bytes", r.ResponseBytes == nil || *r.ResponseBytes >= 0},
		{"input_source", validSource(r.InputSource)},
		{"output_source", validSource(r.OutputSource)},
		{"input_tokens", u.InputTokens >= 0},
		{"output_tokens", u.OutputTokens >= 0},
		{"cache_read_tokens", u.CacheReadTokens >= 0},
		{"cache_write_tokens", u.CacheWriteTokens >= 0},
		{"reasoning_tokens", u.ReasoningTokens >= 0},
		{"estimated_input_tokens", e.InputTokens >= 0},
		{"estimated_output_tokens", e.OutputTokens >= 0},
		{"other_units", numbersSize(u.Extra) <= 2048},
		{"provider_usage", numbersSize(u.Reported) <= 4096},
		{"cost_status", status == "known" || status == "unknown" || status == "none"},
		{"cost_reason", (status == "unknown") == validCostReason(r.Cost.Reason) && (status == "unknown" || r.Cost.Reason == "")},
		{"currency", r.Cost.Currency == "" && !known || r.Cost.Currency != "" && len(r.Cost.Currency) == 3 && allOf(r.Cost.Currency, "ABCDEFGHIJKLMNOPQRSTUVWXYZ")},
		{"cost", (status == "known") == known && (!known || (r.Cost.Amount.Sign() >= 0 && r.Cost.Currency != "" && r.Cost.VersionID != ""))},
		{"cost_fallbacks", len(r.Cost.Fallbacks) <= 8},
		{"metadata", metadataSize(r.Metadata) <= 4096},
		{"metadata_dropped", r.MetadataDropped >= 0 && r.MetadataDropped <= 1000},
	}
}

const (
	lowerAlnum = "abcdefghijklmnopqrstuvwxyz0123456789"
	alnum      = lowerAlnum + "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

func inRange(s string, lo, hi int) bool { return len(s) >= lo && len(s) <= hi }

func matchHex(s string, lo, hi int) bool {
	return inRange(s, lo, hi) && allOf(s, "0123456789abcdef")
}

func allOf(s, set string) bool {
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(set, s[i]) < 0 {
			return false
		}
	}
	return true
}

func validOutcome(o Outcome) bool {
	switch o {
	case OutcomeOK, OutcomeUpstreamError, OutcomeCancelled, OutcomeTimeout, OutcomeRejectedLimit,
		OutcomeRejectedPolicy, OutcomeInternalError:
		return true
	}
	return false
}

func validSource(s Source) bool {
	return s == SourceProvider || s == SourceEstimated || s == SourceNone
}

func validCostReason(r string) bool {
	return r == pricing.ReasonNoPrice || r == pricing.ReasonCategoryUnpriced || r == pricing.ReasonInconsistentUsage
}

// numbersSize is an upper bound of the size of a jsonb object of numbers: a header, and for each
// entry two entry words, the key and a number of at most 16 bytes. The table bounds the real size.
func numbersSize(m map[string]int64) int {
	n := 4
	for k := range m {
		n += 8 + len(k) + 16
	}
	return n
}

// metadataSize is the same bound for an object of strings.
func metadataSize(m map[string]string) int {
	n := 4
	for k, v := range m {
		n += 8 + len(k) + len(v)
	}
	return n
}
