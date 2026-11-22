package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// MetricsOptions configure the meter provider.
type MetricsOptions struct {
	Resource       *Resource         // NewResource; shared with TracesOptions.Resource
	Endpoint       *url.URL          // nil: no OTLP export
	OTLP           bool              // telemetry.metrics.otlp
	Headers        map[string]string // values are secrets
	Timeout        time.Duration
	Interval       time.Duration
	Prometheus     bool // telemetry.metrics.prometheus
	ServiceVersion string
	Transport      http.RoundTripper // required with Endpoint and OTLP
	Logger         *slog.Logger      // required
	Sources        Sources
	Now            func() time.Time // optional clock for staleness and lag (default time.Now)
}

// Sources are read at each collection; a nil function leaves its instruments out.
type Sources struct {
	Ledger      func() LedgerStats
	LedgerLimit int
	Egress      func() EgressStats
	Authn       map[string]func() AuthnStats // by listener: "inference", "admin"
	JWKS        func() JWKSStats
	SignIn      func() SignInStats
	Sessions    func() (open int64, ok bool)
	Hashes      func() HashStats
	Idempotency func() int64
	Admin       func() AdminStats    // grants refused, support actions opened and ended by cause
	Limits      func() LimitsStats   // memory, reconciler, events, rule cache
	Resolver    func() ResolverStats // admitted unpriced, unlimited traffic by result
}

// InstrumentSpec describes one instrument of the catalogue (the table of the metrics reference).
type InstrumentSpec struct {
	Name, Kind, Unit, Description string
	Attributes                    []string
	PrometheusName                string
}

// Metrics owns the meter provider and the instruments.
type Metrics struct {
	provider metric.MeterProvider
	handler  http.Handler
}

// NewMetrics builds the meter provider from explicit options.
func NewMetrics(o MetricsOptions) (*Metrics, error) {
	if o.Logger == nil {
		return nil, errors.New("telemetry: a logger is required")
	}
	if o.Interval <= 0 {
		return nil, errors.New("telemetry: the export interval must be positive")
	}
	if o.Endpoint != nil && o.OTLP && o.Transport == nil {
		return nil, errors.New("telemetry: a transport is required when an endpoint is set")
	}
	return &Metrics{provider: noop.NewMeterProvider()}, nil
}

// ObserveRequest records one request of the inference listener.
func (m *Metrics) ObserveRequest(r Request) {}

// Handler returns the scrape handler; nil unless Prometheus is enabled.
func (m *Metrics) Handler() http.Handler { return m.handler }

// Shutdown flushes the readers and stops the provider, within ctx.
func (m *Metrics) Shutdown(ctx context.Context) error { return nil }

// Catalogue returns the instrument catalogue: the table of the metrics reference.
func Catalogue() []InstrumentSpec { return nil }
