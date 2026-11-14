package telemetry

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Blank uses so that the requirements of the exporter and of logr stay until the real
// implementation of traces lands.
var (
	_ = otlptracehttp.WithEndpointURL
	_ logr.Logger
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

// NewTraces builds the provider from explicit options; no OTEL_* variable configures it.
func NewTraces(o TracesOptions) (*Traces, error) {
	_ = o
	return &Traces{tp: sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))}, nil
}

// Provider returns the tracer provider the gateway uses.
func (t *Traces) Provider() trace.TracerProvider { return t.tp }

// Shutdown flushes the queue and stops the exporter, within ctx.
func (t *Traces) Shutdown(ctx context.Context) error { return t.tp.Shutdown(ctx) }
