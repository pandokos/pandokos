package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/exemplar"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

const (
	meterName        = "github.com/pandokos/pandokos"
	cardinalityLimit = 2000
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
	Admin       func() AdminStats     // grants refused, support actions opened and ended by cause
	Limits      func() LimitsStats    // memory, reconciler, events, rule cache
	Resolver    func() ResolverStats  // admitted unpriced, unlimited traffic by result
	Reports     func() ReportsStats   // the usage rollup, retention and exports (M42 to M46)
	Lifecycle   func() LifecycleStats // tenant deletion and key rotation (M47 to M51)
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
	sdk      *sdkmetric.MeterProvider // nil with the no-op provider
	handler  http.Handler
	reader   *sdkmetric.ManualReader // the scrape reader; nil unless Prometheus is on
	inst     *instruments            // nil with the no-op provider

	h *errorHandler
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
	exportOTLP := o.Endpoint != nil && o.OTLP
	if !exportOTLP && !o.Prometheus {
		return &Metrics{provider: noop.NewMeterProvider()}, nil
	}
	if o.Resource == nil {
		return nil, errors.New("telemetry: a resource is required")
	}
	if o.Now == nil {
		o.Now = time.Now
	}

	installDiagnostics(o.Logger)
	m := &Metrics{h: newErrorHandler(o.Logger, "metric export failing", signalMetrics)}
	opts := []sdkmetric.Option{
		sdkmetric.WithResource(o.Resource),
		sdkmetric.WithCardinalityLimit(cardinalityLimit),
		sdkmetric.WithExemplarFilter(exemplar.AlwaysOffFilter),
		sdkmetric.WithView(views()...),
	}
	if exportOTLP {
		endpoint := *o.Endpoint
		endpoint.Path = strings.TrimSuffix(endpoint.Path, "/") + "/v1/metrics"
		endpoint.RawPath = ""
		exp, err := otlpmetrichttp.New(context.Background(),
			otlpmetrichttp.WithEndpointURL(endpoint.String()),
			otlpmetrichttp.WithHeaders(o.Headers),
			otlpmetrichttp.WithTimeout(o.Timeout),
			otlpmetrichttp.WithCompression(otlpmetrichttp.NoCompression),
			otlpmetrichttp.WithRetry(otlpmetrichttp.RetryConfig{Enabled: false}),
			otlpmetrichttp.WithHTTPClient(&http.Client{
				Transport:     o.Transport,
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			}),
		)
		if err != nil {
			// The exporter's errors do not carry header values, but the message is not passed on.
			return nil, errors.New("telemetry: the metric exporter could not be created")
		}
		popts := []sdkmetric.PeriodicReaderOption{sdkmetric.WithInterval(o.Interval)}
		if o.Timeout > 0 {
			popts = append(popts, sdkmetric.WithTimeout(o.Timeout))
		}
		opts = append(opts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(&reportingMetricExporter{Exporter: exp, h: m.h}, popts...)))
	}
	if o.Prometheus {
		m.reader = sdkmetric.NewManualReader()
		opts = append(opts, sdkmetric.WithReader(m.reader))
		m.handler = m.newHandler()
	}
	m.sdk = sdkmetric.NewMeterProvider(opts...)
	m.provider = m.sdk

	meter := m.sdk.Meter(meterName,
		metric.WithInstrumentationVersion(o.ServiceVersion),
		metric.WithSchemaURL(semconv.SchemaURL))
	k := &maker{meter: meter}
	m.inst = newInstruments(k)
	registerSelf(k)
	registerSources(k, o)
	if k.err != nil {
		_ = m.sdk.Shutdown(context.Background())
		return nil, errors.New("telemetry: the instruments could not be created")
	}
	return m, nil
}

// ObserveRequest records one request of the inference listener. It only aggregates in memory: it
// never waits for the network.
func (m *Metrics) ObserveRequest(r Request) {
	if m.inst != nil {
		m.inst.observe(r)
	}
}

// Handler returns the scrape handler; nil unless Prometheus is enabled.
func (m *Metrics) Handler() http.Handler { return m.handler }

// Shutdown flushes the readers and stops the provider, within ctx.
func (m *Metrics) Shutdown(ctx context.Context) error {
	if m.sdk == nil {
		return nil
	}
	return m.sdk.Shutdown(ctx)
}
