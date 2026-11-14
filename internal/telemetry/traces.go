package telemetry

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const (
	serviceName      = "pandokos"
	queueSize        = 2048
	batchSize        = 512
	batchDelay       = 2 * time.Second
	spanAttrLimit    = 64
	spanAttrValLimit = 256
	errorLogInterval = time.Minute
)

// TracesOptions configure the trace provider.
type TracesOptions struct {
	Endpoint       *url.URL          // nil: nothing is exported
	Headers        map[string]string // sent with every export; values are secrets
	Timeout        time.Duration     // one export
	SampleRatio    float64           // 0 to 1
	Attributes     map[string]string // extra resource attributes
	ServiceVersion string
	Transport      http.RoundTripper // required when Endpoint is set
	Logger         *slog.Logger      // required
}

// Traces owns the trace provider and its exporter.
type Traces struct {
	tp *sdktrace.TracerProvider
}

// NewTraces builds the provider from explicit options; no OTEL_* variable configures it, except
// that OTEL_RESOURCE_ATTRIBUTES adds attributes to the resource (the SDK merges it and offers no
// way to switch that off). The SDK's logger and error handler are process-wide and are routed to
// o.Logger.
func NewTraces(o TracesOptions) (*Traces, error) {
	if o.Logger == nil {
		return nil, errors.New("telemetry: a logger is required")
	}
	if o.Endpoint != nil && o.Transport == nil {
		return nil, errors.New("telemetry: a transport is required when an endpoint is set")
	}
	if !(o.SampleRatio >= 0 && o.SampleRatio <= 1) {
		return nil, errors.New("telemetry: the sample ratio must be between 0 and 1")
	}

	// The diagnostics are installed before the provider so that its own messages reach the logger.
	otel.SetLogger(logr.FromSlogHandler(o.Logger.Handler()))
	handler := newErrorHandler(o.Logger)
	otel.SetErrorHandler(handler)

	res, err := newResource(o)
	if err != nil {
		return nil, fmt.Errorf("telemetry: resource: %w", err)
	}
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithRawSpanLimits(sdktrace.SpanLimits{
			AttributeCountLimit:         spanAttrLimit,
			AttributeValueLengthLimit:   spanAttrValLimit,
			EventCountLimit:             0,
			LinkCountLimit:              0,
			AttributePerEventCountLimit: 0,
			AttributePerLinkCountLimit:  0,
		}),
	}
	if o.Endpoint == nil {
		opts = append(opts, sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.NeverSample())))
		return &Traces{tp: sdktrace.NewTracerProvider(opts...)}, nil
	}

	endpoint := *o.Endpoint
	endpoint.Path = strings.TrimSuffix(endpoint.Path, "/") + "/v1/traces"
	endpoint.RawPath = ""
	exporter, err := otlptracehttp.New(context.Background(),
		otlptracehttp.WithEndpointURL(endpoint.String()),
		otlptracehttp.WithHeaders(o.Headers),
		otlptracehttp.WithTimeout(o.Timeout),
		otlptracehttp.WithCompression(otlptracehttp.NoCompression),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}),
		otlptracehttp.WithHTTPClient(&http.Client{
			Transport:     o.Transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}),
	)
	if err != nil {
		// The exporter's errors do not carry header values, but the message is not passed on.
		return nil, errors.New("telemetry: the trace exporter could not be created")
	}
	opts = append(opts,
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(o.SampleRatio))),
		sdktrace.WithBatcher(&reportingExporter{SpanExporter: exporter, h: handler},
			sdktrace.WithMaxQueueSize(queueSize),
			sdktrace.WithMaxExportBatchSize(batchSize),
			sdktrace.WithBatchTimeout(batchDelay),
			sdktrace.WithExportTimeout(o.Timeout),
		),
	)
	return &Traces{tp: sdktrace.NewTracerProvider(opts...)}, nil
}

func newResource(o TracesOptions) (*resource.Resource, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	id[6] = id[6]&0x0f | 0x40 // version 4
	id[8] = id[8]&0x3f | 0x80
	instance := fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:])

	attrs := map[string]string{
		"service.name":        serviceName,
		"service.version":     o.ServiceVersion,
		"service.instance.id": instance,
	}
	for k, v := range o.Attributes {
		attrs[k] = v
	}
	kvs := make([]attribute.KeyValue, 0, len(attrs))
	for k, v := range attrs {
		kvs = append(kvs, attribute.String(k, v))
	}
	return resource.NewSchemaless(kvs...), nil
}

// reportingExporter hands export failures to the handler of its own provider instead of the
// process-wide one, so that the failures of one provider are never logged by another.
type reportingExporter struct {
	sdktrace.SpanExporter
	h *errorHandler
}

// ExportSpans exports the spans; a failure is reported and not returned.
func (e *reportingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	if err := e.SpanExporter.ExportSpans(ctx, spans); err != nil {
		e.h.Handle(err)
	}
	return nil
}

// errorHandler logs SDK errors as "trace export failing", at most once a minute, with the number
// of errors since the last line and a class. The error text is never logged: it may quote the
// endpoint or the collector's answer.
type errorHandler struct {
	log *slog.Logger

	mu      sync.Mutex
	count   int64
	lastLog time.Time
}

func newErrorHandler(l *slog.Logger) *errorHandler { return &errorHandler{log: l} }

// Handle implements otel.ErrorHandler.
func (h *errorHandler) Handle(err error) {
	if err == nil {
		return
	}
	h.mu.Lock()
	h.count++
	if !h.lastLog.IsZero() && time.Since(h.lastLog) < errorLogInterval {
		h.mu.Unlock()
		return
	}
	n := h.count
	h.count = 0
	h.lastLog = time.Now()
	h.mu.Unlock()
	h.log.Warn("trace export failing", "count", n, "error_class", errorClass(err))
}

func errorClass(err error) string {
	var ne net.Error
	var ue *url.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case strings.Contains(err.Error(), "retry-able request failure"), strings.Contains(err.Error(), "failed to send to "):
		return "status"
	case errors.As(err, &ue):
		if ue.Timeout() {
			return "timeout"
		}
		return "connection"
	case errors.As(err, &ne):
		if ne.Timeout() {
			return "timeout"
		}
		return "connection"
	default:
		return "other"
	}
}

// Provider returns the tracer provider the gateway uses.
func (t *Traces) Provider() trace.TracerProvider { return t.tp }

// Shutdown flushes the queue and stops the exporter, within ctx.
func (t *Traces) Shutdown(ctx context.Context) error { return t.tp.Shutdown(ctx) }
