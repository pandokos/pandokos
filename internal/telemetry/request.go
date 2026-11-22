package telemetry

import "time"

// Request is what the gateway measured of one request of the inference listener, copied by the
// adapter of cmd/pandokos from gateway.RequestObservation: the same fields, owned by this package.
// It holds no identifier of a tenant, caller, key, request or trace (ADR 0055, ADR 0062).
type Request struct {
	Method     string        // http.request.method: the method, or "_OTHER"
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

	Forwarded      bool          // the call was handed to the provider
	Call           time.Duration // from the start of the call to the end of the response body
	FirstChunk     time.Duration // stream calls with a first body byte: the call's start to that byte; else 0
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

	Refusal  *RefusalObservation  // a refusal of the limits; nil otherwise
	Estimate *EstimateObservation // a settled reservation; nil otherwise
}

// RefusalObservation names the kind of limit that refused, never the rule or its subject.
type RefusalObservation struct{ Code, Metric, Level, SubjectKind, Scope string }

// EstimateObservation compares what was reserved with what was settled.
type EstimateObservation struct {
	ReservedTokens, SettledTokens int64
	ReservedCost, SettledCost     float64 // same currency (one cost per request); 0 when unknown
	CostComparable                bool
}
