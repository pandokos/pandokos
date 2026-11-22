package gateway

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/usage"
)

// Target is where an authorized call goes. Every field comes from the catalogue.
type Target struct {
	Provider, Endpoint string // names
	ProviderID         string // catalogue ID of the provider (sprint 5.3, C3)
	ModelID, Model     string // catalogue ID and public name
	UpstreamModel      string // what the provider receives as model
	BaseURL            *url.URL
	Private            bool
	Prices             []pricing.Version // the model's price versions, from the snapshot that resolved it; read-only
	StreamUsage        bool              // the provider's stream_usage_option is "add"
	MaxOutputTokens    int64             // the model's max_output_tokens; 0 when the catalogue has none
	ContextWindow      int64             // the model's context window in tokens; 0 when the catalogue has none
	// DefaultOutputTokens is the output reservation per choice of a request that sets no output
	// limit; 0 when the model has none and the configured default applies.
	DefaultOutputTokens int64
	// Currency is the active currency of the tenant; the zero value prices from the catalogue
	// whatever its currency.
	Currency pricing.Active
	// TenantPrices holds the tenant's price versions of the model, of the currencies of Currency;
	// read-only.
	TenantPrices []pricing.Version
}

// PriceList is the price list of the tenant for the model: its currency, the catalogue's versions
// and its own.
func (t Target) PriceList() pricing.List {
	return pricing.List{Currency: t.Currency, Catalogue: t.Prices, Tenant: t.TenantPrices}
}

// Recorder receives the usage record of every admitted request.
type Recorder interface {
	// Admit reserves room for the record of one request. When it returns false the request
	// is refused and Record is not called.
	Admit() bool
	// Record takes the record of an admitted request. It does not block.
	Record(r usage.Record)
}

// Catalogue is the gateway's view of the catalogue.
type Catalogue interface {
	// Provider returns the API family of an enabled provider; ok is false for a name that is
	// unknown or disabled. An error means the catalogue could not be read.
	Provider(ctx context.Context, name string) (family string, ok bool, err error)
	// Resolve returns the target for a model. ErrModelNotAllowed and ErrProviderUnavailable are
	// refusals; any other error means the decision could not be taken.
	Resolve(ctx context.Context, p authz.Principal, provider, model string) (Target, error)
}

// LimitEstimate is what a call is expected to consume (sprint 5.1 section 5.3).
type LimitEstimate struct {
	RequestID                 string
	InputTokens, OutputTokens int64
	Cost                      pricing.Cost
	Provider, Model           string    // names of the target
	ProviderID, ModelID       string    // identifiers of the target (sprint 5.3, C3)
	Start                     time.Time // start of the request: the period its amounts belong to
}

// LimitUsage is what a call consumed: the figures of its usage record.
type LimitUsage struct {
	Tokens int64
	Cost   pricing.Cost
}

// LimitRefusal is a refusal of the limits, in plain values.
type LimitRefusal struct {
	Code                   string // "rate_limited", "quota_exceeded" or "no_price"
	Level, Subject, Metric string
	Scope, Currency        string // how narrow the rule is (all, provider, model); the currency of a budget
	PriceCurrency          string // with code no_price: the currency of the estimate's price, empty without one (sprint 5.3, C3)
	PriceSource            string // with code no_price: the list the estimate's price came from, empty without one
	Limit, Remaining       int64
	RetryAfter, Reset      time.Duration
	OverLimit              bool
}

// LimitState is how the limits treated a request (sprint 5.3, section 5.5): the value of
// usage_record.limit_state.
type LimitState string

// The states of a request.
const (
	LimitStateNotEvaluated LimitState = "not_evaluated" // the request ended before the limits, or there is no limiter
	LimitStateUnlimited    LimitState = "unlimited"     // no rule matched the request
	LimitStateLimited      LimitState = "limited"       // every matching limit was evaluated
	LimitStateUnverified   LimitState = "unverified"    // a matching limit could not be evaluated
)

// Metrics receives one observation per request of the inference listener, at its end.
// Observe does not block and does not fail.
type Metrics interface {
	Observe(o RequestObservation)
}

// RequestObservation is what the gateway measures of one request of the inference listener.
// It holds no identifier of a tenant, caller, key, request or trace (ADR 0055, ADR 0062).
type RequestObservation struct {
	Method     string        // http.request.method: the method, or "_OTHER" (logMethod's set)
	Scheme     string        // "http" or "https"
	Route      string        // the route label "/{provider}/v1/…" when matched; "" otherwise
	Status     int           // the status written; 0 when none
	Outcome    string        // the request line's outcome
	ErrorCode  string        // the Pandokos error code; "" when none
	LimitState string        // not_evaluated, unlimited, limited, unverified
	Provider   string        // catalogue provider name; "" when the route did not match
	Family     string        // API family; "" when the route did not match
	Operation  string        // gen_ai.operation.name of the matched operation; ""
	Model      string        // catalogue model name of the resolved target; "" when not resolved
	Duration   time.Duration // the whole request, start to finish

	Forwarded      bool          // step 9 ran: the call was handed to the provider
	Call           time.Duration // from the start of step 9 to the end of the response body
	FirstChunk     time.Duration // stream calls with a first body byte: step 9 to that byte; else 0
	Stream         bool
	CallError      string // error.type of the provider call; "" when not in error
	UpstreamStatus int    // 0 when none
	CredentialID   string // set only when UpstreamStatus == 429

	InputTokens, OutputTokens int64  // the usage record's figures, provider or estimated
	InputSource, OutputSource string // "provider", "estimated" or "none"
	CostKnown                 bool
	CostAmount                float64 // the record's cost, when known
	CostCurrency              string  // ISO 4217, when known
	CostReason                string  // no_price, category_unpriced, inconsistent_usage, when not known

	Refusal  *LimitRefusalObservation // a refusal of the limits; nil otherwise
	Estimate *EstimateObservation     // a settled reservation; nil otherwise
}

// LimitRefusalObservation names the kind of limit that refused, never the rule or its subject.
type LimitRefusalObservation struct{ Code, Metric, Level, SubjectKind, Scope string }

// EstimateObservation compares what was reserved with what was settled.
type EstimateObservation struct {
	ReservedTokens, SettledTokens int64
	ReservedCost, SettledCost     float64 // same currency (one cost per request); 0 when unknown
	CostComparable                bool
}

// Limits reserves capacity for a call. A nil Limits enforces nothing.
type Limits interface {
	// Reserve returns the reservation of an admitted call (never nil then), a refusal, or an
	// error when the limits of the tenant cannot be verified now; with each the state the limits
	// found the request in.
	Reserve(ctx context.Context, p authz.Principal, e LimitEstimate) (Reservation, *LimitRefusal, LimitState, error)
}

// Reservation is settled or released exactly once by the gateway. Neither method blocks or
// returns an error: the implementation logs what fails.
type Reservation interface {
	Settle(u LimitUsage)
	Release()
}

// Credential is an opened provider credential.
type Credential struct {
	ID     string
	Pinned bool
	Secret secret.Secret
}

// Credentials opens the credential a call is made with.
type Credentials interface {
	// Open returns ErrNoCredential, ErrCredentialDenied, or another error when the credential
	// cannot be verified now.
	Open(ctx context.Context, p authz.Principal, requestID, provider string) (Credential, error)
}

// AuthFailure is a refused credential that names a key or a registration of a tenant. IssuerID,
// IssuerScope ("tenant", "platform") and Subject are set for a token only; Subject only when its
// signature was verified.
type AuthFailure struct {
	RequestID, Reason, KeyID, TenantID, IssuerID, IssuerScope, Subject string
}

// FailureRecorder records authentication failures; it reports whether a record was written.
type FailureRecorder interface {
	Record(ctx context.Context, f AuthFailure) bool
}

// The refusals the gateway distinguishes.
var (
	ErrModelNotAllowed     = errors.New("gateway: model not allowed")
	ErrProviderUnavailable = errors.New("gateway: provider unavailable")
	ErrNoCredential        = errors.New("gateway: no usable credential")
	ErrCredentialDenied    = errors.New("gateway: the principal may not use a credential")
)

// Route is one route of the gateway and the permissions it requires.
type Route struct {
	Method      string
	Path        string // "/{provider}" + native path
	Permissions []string
}

// Routes lists, for every operation of every family, the route and its permissions.
func Routes() []Route {
	var routes []Route
	adapters := Families()
	names := make([]string, 0, len(adapters))
	for name := range adapters {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, op := range adapters[name].Operations() {
			routes = append(routes, Route{Method: op.Method, Path: "/{provider}" + op.Path, Permissions: []string{authz.ModelInvoke, authz.CredentialUse}})
		}
	}
	return routes
}
