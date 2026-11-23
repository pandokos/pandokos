package telemetry

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/semconv/v1.41.0/genaiconv"
	"go.opentelemetry.io/otel/semconv/v1.41.0/httpconv"
)

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

// otherValue is what an attribute records when the value is outside its closed set.
const otherValue = "_OTHER"

// The closed sets of section 5.3.1 for the attributes whose values the product owns. The package
// imports no product package, so the lists are repeated here and checked by the tests against the
// packages that own them.
var (
	closedMethods    = set("GET", "POST", "HEAD", "PUT", "DELETE", "PATCH", "OPTIONS", otherValue)
	closedSchemes    = set("http", "https")
	closedOutcomes   = set("ok", "pandokos_error", "client_error", "client_cancelled", "shutdown_aborted", "timeout", "upstream_aborted")
	closedLimitState = set("not_evaluated", "unlimited", "limited", "unverified")
	closedFamilies   = set("openai", "anthropic")
	closedOperations = set("chat", "embeddings", "messages.count_tokens")
	closedCallErrors = set("timeout", "upstream_unreachable", "upstream_aborted", "cancelled", otherValue)
	closedCostReason = set("no_price", "category_unpriced", "inconsistent_usage")
	closedLimitCodes = set("rate_limited", "quota_exceeded", "no_price")
	closedMetrics    = set("concurrent_requests", "requests_per_minute", "tokens_per_minute", "requests_per_day", "requests_per_month",
		"tokens_per_day", "tokens_per_month", "cost_per_day", "cost_per_month")
	closedLevels = set("platform", "tenant")
	closedKinds  = set("tenant", "service_account", "key")
	closedScopes = set("all", "provider", "model")
	closedErrors = set(
		"unauthenticated", "endpoint_not_supported", "request_too_large", "upstream_unreachable", "upstream_timeout", "internal",
		"forbidden", "tenant_suspended", "unavailable", "model_not_allowed", "no_credential", "invalid_request", "request_timeout",
		"rate_limited", "quota_exceeded", "no_price")
)

func set(values ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(values))
	for _, v := range values {
		m[v] = struct{}{}
	}
	return m
}

// closed returns v when it is in the set, otherwise the defensive value; "" stays "" (absent).
func closed(m map[string]struct{}, v string) string {
	if v == "" {
		return ""
	}
	if _, ok := m[v]; ok {
		return v
	}
	return otherValue
}

// closedRequired is closed for an attribute that is always present: "" is outside the set too.
func closedRequired(m map[string]struct{}, v string) string {
	if _, ok := m[v]; ok {
		return v
	}
	return otherValue
}

// currency is an ISO 4217 code: three capital letters.
func currency(v string) string {
	if len(v) == 3 && v[0] >= 'A' && v[0] <= 'Z' && v[1] >= 'A' && v[1] <= 'Z' && v[2] >= 'A' && v[2] <= 'Z' {
		return v
	}
	return otherValue
}

// callError is the error.type of the provider call: a status of three digits from 400, or a class.
func callError(v string) string {
	if v == "" {
		return ""
	}
	if len(v) == 3 && v[0] >= '4' && v[0] <= '5' && v[1] >= '0' && v[1] <= '9' && v[2] >= '0' && v[2] <= '9' {
		return v
	}
	return closed(closedCallErrors, v)
}

// route is the route label of a matched route.
func route(v string) string {
	if strings.HasPrefix(v, "/{provider}/") {
		return v
	}
	if v == "" {
		return ""
	}
	return otherValue
}

// statusText is the status as three digits, "" when none was written or it is not a status.
func statusText(status int) string {
	if status < 100 || status > 599 {
		return ""
	}
	return statusTexts[status]
}

// statusTexts are the texts of the statuses, built once: the text of a status is not allocated per request.
var statusTexts = func() (t [600]string) {
	for i := 100; i < 600; i++ {
		t[i] = strconv.Itoa(i)
	}
	return t
}()

// setKey identifies an attribute set: the instrument and the values of its attributes, "" for an
// attribute that is absent. It is comparable, so that the sets are cached (D-20).
type setKey struct {
	inst uint8
	v    [7]string
}

const (
	setHTTP uint8 = iota
	setRequests
	setOperation // M3
	setFirstChunk
	setTokens // M5 and M6
	setCost
	setCostUnknown
	setRateLimited
	setRefusal
	setEstimate
)

// maxCachedSets bounds the cache: beyond it the sets are built per call and not kept.
const maxCachedSets = 16384

// build makes the attribute set of the key.
func (k setKey) build() attribute.Set {
	kvs := make([]attribute.KeyValue, 0, len(k.v))
	add := func(key string, v string) {
		if v != "" {
			kvs = append(kvs, attribute.String(key, v))
		}
	}
	v := k.v
	switch k.inst {
	case setHTTP:
		add("http.request.method", v[0])
		add("url.scheme", v[1])
		add("http.route", v[2])
		if v[3] != "" {
			code, _ := strconv.Atoi(v[3])
			kvs = append(kvs, attribute.Int("http.response.status_code", code))
		}
		add("error.type", v[4])
	case setRequests:
		add("pandokos.outcome", v[0])
		add("pandokos.error_code", v[1])
		add("pandokos.limit_state", v[2])
		add("gen_ai.provider.name", v[3])
		add("pandokos.api_family", v[4])
		add("gen_ai.operation.name", v[5])
		add("gen_ai.request.model", v[6])
	case setOperation, setFirstChunk:
		add("gen_ai.operation.name", v[0])
		add("gen_ai.provider.name", v[1])
		add("gen_ai.request.model", v[2])
		add("error.type", v[3])
	case setTokens:
		add("gen_ai.operation.name", v[0])
		add("gen_ai.provider.name", v[1])
		add("gen_ai.request.model", v[2])
		add("gen_ai.token.type", v[3])
	case setCost:
		add("gen_ai.operation.name", v[0])
		add("gen_ai.provider.name", v[1])
		add("gen_ai.request.model", v[2])
		add("pandokos.currency", v[3])
	case setCostUnknown:
		add("gen_ai.operation.name", v[0])
		add("gen_ai.provider.name", v[1])
		add("gen_ai.request.model", v[2])
		add("pandokos.cost.reason", v[3])
	case setRateLimited:
		add("gen_ai.provider.name", v[0])
		add("pandokos.credential.id", v[1])
	case setRefusal:
		add("pandokos.limit.code", v[0])
		add("pandokos.limit.metric", v[1])
		add("pandokos.limit.level", v[2])
		add("pandokos.limit.subject", v[3])
		add("pandokos.limit.scope", v[4])
	case setEstimate:
		add("pandokos.limit.quantity", v[0])
	}
	return attribute.NewSet(kvs...)
}

// instruments are the instruments the gateway's observations are recorded on.
type instruments struct {
	httpDuration httpconv.ServerRequestDuration
	requests     metric.Int64Counter
	opDuration   genaiconv.ClientOperationDuration
	firstChunk   genaiconv.ClientOperationTimeToFirstChunk
	tokenUsage   genaiconv.ClientTokenUsage
	estimated    metric.Int64Counter
	cost         metric.Float64Counter
	costUnknown  metric.Int64Counter
	rateLimited  metric.Int64Counter
	refusals     metric.Int64Counter
	estimate     metric.Float64Histogram
	eventDur     metric.Float64Histogram

	mu   sync.RWMutex
	sets map[setKey]metric.MeasurementOption // the options of the attribute sets seen, kept boxed once
}

func newInstruments(k *maker) *instruments {
	in := &instruments{sets: make(map[setKey]metric.MeasurementOption)}
	var err error
	in.httpDuration, err = httpconv.NewServerRequestDuration(k.meter)
	k.fail(err)
	in.opDuration, err = genaiconv.NewClientOperationDuration(k.meter)
	k.fail(err)
	in.firstChunk, err = genaiconv.NewClientOperationTimeToFirstChunk(k.meter)
	k.fail(err)
	in.tokenUsage, err = genaiconv.NewClientTokenUsage(k.meter)
	k.fail(err)
	in.requests = k.int64Counter(nameRequests)
	in.estimated = k.int64Counter(nameEstimated)
	in.cost = k.float64Counter(nameCost)
	in.costUnknown = k.int64Counter(nameCostUnknown)
	in.rateLimited = k.int64Counter(nameRateLimited)
	in.refusals = k.int64Counter(nameRefusals)
	in.estimate = k.float64Histogram(nameEstimate)
	in.eventDur = k.float64Histogram(nameEventDuration)
	return in
}

// attrs returns the cached option of the attribute set of the key.
func (in *instruments) attrs(k setKey) metric.MeasurementOption {
	in.mu.RLock()
	o, ok := in.sets[k]
	in.mu.RUnlock()
	if ok {
		return o
	}
	o = metric.WithAttributeSet(k.build())
	in.mu.Lock()
	if len(in.sets) < maxCachedSets {
		in.sets[k] = o
	}
	in.mu.Unlock()
	return o
}

// observe records one request (T-913); it allocates nothing but what the SDK does.
func (in *instruments) observe(r Request) {
	ctx := context.Background()
	outcome := closedRequired(closedOutcomes, r.Outcome)
	provider, operation, model := r.Provider, closed(closedOperations, r.Operation), r.Model

	// M1: every request.
	httpKey := setKey{inst: setHTTP, v: [7]string{closedRequired(closedMethods, r.Method), closedRequired(closedSchemes, r.Scheme), route(r.Route), statusText(r.Status)}}
	switch {
	case r.Status >= 500:
		httpKey.v[4] = httpKey.v[3]
	case r.Status == 0 && r.Outcome != "ok":
		httpKey.v[4] = outcome
	}
	in.httpDuration.Inst().Record(ctx, r.Duration.Seconds(), in.attrs(httpKey))

	// M2: every request.
	in.requests.Add(ctx, 1, in.attrs(setKey{inst: setRequests, v: [7]string{
		outcome, closed(closedErrors, r.ErrorCode), closedRequired(closedLimitState, r.LimitState),
		provider, closed(closedFamilies, r.Family), operation, model,
	}}))

	genai := [7]string{operation, provider, model}
	if r.Forwarded {
		k := setKey{inst: setOperation, v: genai}
		k.v[3] = callError(r.CallError)
		in.opDuration.Inst().Record(ctx, r.Call.Seconds(), in.attrs(k))
		if r.Stream && r.FirstChunk > 0 {
			in.firstChunk.Inst().Record(ctx, r.FirstChunk.Seconds(), in.attrs(setKey{inst: setFirstChunk, v: genai}))
		}
	}

	for _, side := range [...]struct {
		kind, source string
		n            int64
	}{{"input", r.InputSource, r.InputTokens}, {"output", r.OutputSource, r.OutputTokens}} {
		if side.n <= 0 {
			continue
		}
		k := setKey{inst: setTokens, v: genai}
		k.v[3] = side.kind
		switch side.source {
		case "provider":
			in.tokenUsage.Inst().Record(ctx, side.n, in.attrs(k))
		case "estimated":
			in.estimated.Add(ctx, side.n, in.attrs(k))
		}
	}

	switch {
	case r.CostKnown:
		k := setKey{inst: setCost, v: genai}
		k.v[3] = currency(r.CostCurrency)
		in.cost.Add(ctx, r.CostAmount, in.attrs(k))
	case (r.InputSource != "" && r.InputSource != "none") || (r.OutputSource != "" && r.OutputSource != "none"):
		k := setKey{inst: setCostUnknown, v: genai}
		k.v[3] = closedRequired(closedCostReason, r.CostReason)
		in.costUnknown.Add(ctx, 1, in.attrs(k))
	}

	if r.UpstreamStatus == 429 {
		in.rateLimited.Add(ctx, 1, in.attrs(setKey{inst: setRateLimited, v: [7]string{provider, r.CredentialID}}))
	}

	if f := r.Refusal; f != nil {
		in.refusals.Add(ctx, 1, in.attrs(setKey{inst: setRefusal, v: [7]string{
			closedRequired(closedLimitCodes, f.Code), closedRequired(closedMetrics, f.Metric), closedRequired(closedLevels, f.Level),
			closedRequired(closedKinds, f.SubjectKind), closedRequired(closedScopes, f.Scope),
		}}))
	}

	if e := r.Estimate; e != nil {
		if e.ReservedTokens > 0 {
			in.estimate.Record(ctx, float64(e.SettledTokens)/float64(e.ReservedTokens), in.attrs(setKey{inst: setEstimate, v: [7]string{"tokens"}}))
		}
		if e.CostComparable && e.ReservedCost > 0 {
			in.estimate.Record(ctx, e.SettledCost/e.ReservedCost, in.attrs(setKey{inst: setEstimate, v: [7]string{"cost"}}))
		}
	}
}
