package gateway

import (
	"fmt"
	"strconv"
	"sync"

	"github.com/pandokos/pandokos/internal/usage"
)

// observe hands the observation of the request to the Metrics, under a recover: a failure of the
// metrics never reaches the caller. It runs once, after the request line.
func (h *Handler) observe(st *reqState, outcome string, est *EstimateObservation) {
	defer func() {
		if v := recover(); v != nil {
			h.logger.Error("metrics observation failed", "request_id", st.id, "panic_type", fmt.Sprintf("%T", v))
		}
	}()
	h.metrics.Observe(h.observation(st, outcome, est))
}

// observation builds the observation from the state of the request and its record only: no
// identifier of a tenant, a caller, a request or a trace.
func (h *Handler) observation(st *reqState, outcome string, est *EstimateObservation) RequestObservation {
	o := RequestObservation{
		Method:     st.method,
		Scheme:     st.scheme,
		Status:     st.sw.status(),
		Outcome:    outcome,
		ErrorCode:  string(st.errCode),
		LimitState: string(st.limitState),
		Duration:   st.now().Sub(st.start),
		Stream:     st.streamKnown && st.stream,
		Estimate:   est,
	}
	if o.Method == "OTHER" {
		o.Method = "_OTHER"
	}
	if st.matched {
		o.Route = routeLabel(st.op.Path)
		o.Provider, o.Family, o.Operation = st.provider, st.family, st.op.GenAI
	}
	if st.resolved {
		o.Model = st.target.Model
	}
	if !st.forwardAt.IsZero() {
		o.Forwarded = true
		end := st.callEnd
		if end.IsZero() {
			end = st.now()
		}
		o.Call = end.Sub(st.forwardAt)
		if o.Stream && !st.firstByte.IsZero() {
			o.FirstChunk = st.firstByte.Sub(st.forwardAt)
		}
		o.CallError = callErrorOf(st, outcome)
		o.UpstreamStatus = st.upstreamStatus
		if st.upstreamStatus == 429 {
			o.CredentialID = st.credentialID
		}
	}
	if r := st.rec; r != nil {
		o.InputSource, o.OutputSource = string(r.InputSource), string(r.OutputSource)
		o.InputTokens = sideTokens(r.InputSource, r.Usage.InputTokens, r.Estimated.InputTokens)
		o.OutputTokens = sideTokens(r.OutputSource, r.Usage.OutputTokens, r.Estimated.OutputTokens)
		if r.Cost.Known {
			o.CostKnown, o.CostAmount, o.CostCurrency = true, r.Cost.Amount.Float64(), r.Cost.Currency
		} else if r.InputSource != usage.SourceNone {
			o.CostReason = r.Cost.Reason
		}
	}
	if f := st.refusal; f != nil {
		o.Refusal = &LimitRefusalObservation{Code: f.Code, Metric: f.Metric, Level: f.Level, SubjectKind: f.Subject, Scope: f.Scope}
		if o.Refusal.Code == "" {
			o.Refusal.Code = "rate_limited"
		}
	}
	return o
}

func sideTokens(s usage.Source, provider, estimated int64) int64 {
	switch s {
	case usage.SourceProvider:
		return provider
	case usage.SourceEstimated:
		return estimated
	}
	return 0
}

// callErrorOf is the error.type of a forwarded call, "" when it did not fail (section 5.3.1).
func callErrorOf(st *reqState, outcome string) string {
	switch {
	case st.upstreamStatus >= 400:
		return strconv.Itoa(st.upstreamStatus)
	case outcome == "timeout" || st.errCode == "upstream_timeout":
		return "timeout"
	case st.errCode == "upstream_unreachable":
		return "upstream_unreachable"
	case outcome == "upstream_aborted":
		return "upstream_aborted"
	case outcome == "client_cancelled" || outcome == "shutdown_aborted" || outcome == "client_error":
		return "cancelled"
	case st.errCode != "":
		return "_OTHER"
	}
	return ""
}

// routeLabels caches the label of each route: the paths are those of the families' operations.
var routeLabels sync.Map

func routeLabel(path string) string {
	if v, ok := routeLabels.Load(path); ok {
		return v.(string)
	}
	l := "/{provider}" + path
	routeLabels.Store(path, l)
	return l
}
