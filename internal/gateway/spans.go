package gateway

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/pandokos/pandokos/internal/usage"
)

// The attribute keys the gateway sets on its spans (sprint.md section 5.9). No other key is
// ever set: a span holds no URL, host, header value, caller metadata or content.
const (
	attrHTTPMethod       = "http.request.method"
	attrHTTPRoute        = "http.route"
	attrHTTPStatus       = "http.response.status_code"
	attrRequestID        = "pandokos.request.id"
	attrRecordID         = "pandokos.record.id"
	attrTenantID         = "pandokos.tenant.id"
	attrPrincipalID      = "pandokos.principal.id"
	attrKeyID            = "pandokos.key.id"
	attrProvider         = "pandokos.provider"
	attrOperation        = "pandokos.operation"
	attrOutcome          = "pandokos.outcome"
	attrErrorCode        = "pandokos.error.code"
	attrEndpoint         = "pandokos.endpoint"
	attrModel            = "pandokos.model"
	attrStream           = "pandokos.stream"
	attrCredentialID     = "pandokos.credential.id" //nolint:gosec // an attribute name, not a credential
	attrFunding          = "pandokos.funding"
	attrUpstreamError    = "pandokos.upstream.error"
	attrTTFB             = "pandokos.ttfb_ms"
	attrUsageSource      = "pandokos.usage.source"
	attrCostStatus       = "pandokos.cost.status"
	attrCostAmount       = "pandokos.cost.amount"
	attrCostCurrency     = "pandokos.cost.currency"
	instrumentationScope = "github.com/pandokos/pandokos"
)

// SpanAttributes lists every attribute key the gateway sets on a span.
var SpanAttributes = []string{
	attrHTTPMethod, attrHTTPRoute, attrHTTPStatus, attrRequestID, attrRecordID, attrTenantID,
	attrPrincipalID, attrKeyID, attrProvider, attrOperation, attrOutcome, attrErrorCode,
	attrEndpoint, attrModel, attrStream, attrCredentialID, attrFunding, attrUpstreamError,
	attrTTFB, attrUsageSource, attrCostStatus, attrCostAmount, attrCostCurrency,
}

// failed reports whether the outcome sets the status of a span to Error.
func failed(o usage.Outcome) bool {
	return o == usage.OutcomeUpstreamError || o == usage.OutcomeTimeout || o == usage.OutcomeInternalError
}

// endClient sets the attributes and the status of the client span from the record and ends it.
func (st *reqState) endClient(r *usage.Record) {
	s := st.client
	if s == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String(attrProvider, r.Provider),
		attribute.String(attrEndpoint, r.Endpoint),
		attribute.String(attrModel, r.Model),
		attribute.String(attrOperation, r.Operation),
		attribute.String(attrCredentialID, r.CredentialID),
		attribute.String(attrFunding, r.Funding),
		attribute.String(attrUsageSource, string(r.InputSource)),
		attribute.String(attrCostStatus, r.CostStatus),
	}
	if r.Stream != nil {
		attrs = append(attrs, attribute.Bool(attrStream, *r.Stream))
	}
	if r.UpstreamStatus != 0 {
		attrs = append(attrs, attribute.Int(attrHTTPStatus, r.UpstreamStatus))
	}
	if st.upstreamErr != "" {
		attrs = append(attrs, attribute.String(attrUpstreamError, st.upstreamErr))
	}
	if r.TTFB != nil {
		attrs = append(attrs, attribute.Int64(attrTTFB, r.TTFB.Milliseconds()))
	}
	if r.Cost.Known {
		attrs = append(attrs, attribute.String(attrCostAmount, r.Cost.Amount.String()), attribute.String(attrCostCurrency, r.Cost.Currency))
	}
	s.SetAttributes(attrs...)
	if failed(r.Outcome) {
		s.SetStatus(codes.Error, "")
	}
	s.End()
}

// endServer sets the attributes and the status of the server span and ends it.
func (st *reqState) endServer(r *usage.Record) {
	s := st.server
	if s == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String(attrHTTPMethod, st.op.Method),
		attribute.String(attrHTTPRoute, "/{provider}"+st.op.Path),
		attribute.String(attrRequestID, r.RequestID),
		attribute.String(attrRecordID, r.ID),
		attribute.String(attrTenantID, r.TenantID.String()),
		attribute.String(attrPrincipalID, r.PrincipalID),
		attribute.String(attrKeyID, r.KeyID),
		attribute.String(attrProvider, r.Provider),
		attribute.String(attrOperation, r.Operation),
		attribute.String(attrOutcome, string(r.Outcome)),
	}
	if code := st.sw.status(); code != 0 {
		attrs = append(attrs, attribute.Int(attrHTTPStatus, code))
	}
	if r.ErrorCode != "" {
		attrs = append(attrs, attribute.String(attrErrorCode, r.ErrorCode))
	}
	s.SetAttributes(attrs...)
	if failed(r.Outcome) {
		s.SetStatus(codes.Error, "")
	}
	s.End()
}

// traceIDOf returns the trace ID of a span, "" when it has none (a tracer that records nothing).
func traceIDOf(s trace.Span) string {
	if id := s.SpanContext().TraceID(); id.IsValid() {
		return id.String()
	}
	return ""
}
