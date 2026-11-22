package gateway

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/pandokos/pandokos/internal/usage"
)

// The attribute keys the gateway sets on its spans: only those the OpenTelemetry semantic
// conventions mark Required, nothing else (ADR 0055). The server span follows the HTTP server
// span, the provider span the GenAI inference span. No pandokos.* attribute exists.
const (
	attrHTTPMethod       = "http.request.method"
	attrURLPath          = "url.path"
	attrURLScheme        = "url.scheme"
	attrGenAIOperation   = "gen_ai.operation.name"
	attrGenAIProvider    = "gen_ai.provider.name"
	instrumentationScope = "github.com/pandokos/pandokos"
)

// SpanAttributes lists every attribute key the gateway sets on a span.
var SpanAttributes = []string{attrHTTPMethod, attrURLPath, attrURLScheme, attrGenAIOperation, attrGenAIProvider}

// serverSpanAttributes are the attributes of the server span, set when it starts.
func (st *reqState) serverSpanAttributes(scheme string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String(attrHTTPMethod, st.op.Method),
		attribute.String(attrURLPath, "/"+st.provider+st.op.Path),
		attribute.String(attrURLScheme, scheme),
	}
}

// clientSpanAttributes are the attributes of the provider span, set when it starts.
func (st *reqState) clientSpanAttributes() []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String(attrGenAIOperation, st.op.GenAI),
		attribute.String(attrGenAIProvider, st.provider),
	}
}

// failed reports whether the outcome sets the status of a span to Error.
func failed(o usage.Outcome) bool {
	return o == usage.OutcomeUpstreamError || o == usage.OutcomeTimeout || o == usage.OutcomeInternalError
}

// endClient sets the status of the provider span and ends it.
func (st *reqState) endClient(r *usage.Record) {
	if !st.forwardAt.IsZero() {
		st.callEnd = st.now()
	}
	endSpan(st.client, r)
}

// endServer sets the status of the server span and ends it.
func (st *reqState) endServer(r *usage.Record) {
	endSpan(st.server, r)
}

func endSpan(s trace.Span, r *usage.Record) {
	if s == nil {
		return
	}
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
