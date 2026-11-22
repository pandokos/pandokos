package telemetry

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const errorLogInterval = time.Minute

// The signals and the classes of an export failure (the values of M40).
const (
	signalTraces = iota
	signalMetrics
)

var (
	signalNames = [...]string{"traces", "metrics"}
	classNames  = [...]string{"timeout", "status", "connection", "other"}
)

// selfStats are the counters the telemetry pipeline keeps of itself (D-14): spans ended, exported
// and failed (M39), and export failures by signal and class (M40). They are process-wide, as the
// providers' diagnostics are, and atomics: nothing on the request path takes a lock for them.
var selfStats struct {
	ended, exported, failed atomic.Int64
	failures                [len(signalNames)][len(classNames)]atomic.Int64
}

// countingProcessor counts the spans that end, before the batcher can drop them.
type countingProcessor struct{}

func (countingProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) {}
func (countingProcessor) OnEnd(sdktrace.ReadOnlySpan)                     { selfStats.ended.Add(1) }
func (countingProcessor) Shutdown(context.Context) error                  { return nil }
func (countingProcessor) ForceFlush(context.Context) error                { return nil }

// errorHandler logs SDK errors under one message, at most once a minute, with the number of errors
// since the last line and a class. The error text is never logged: it may quote the endpoint or the
// collector's answer. A handler of a signal (traces, metrics) also counts the failure on M40.
type errorHandler struct {
	log    *slog.Logger
	msg    string
	signal int // signalTraces, signalMetrics, or -1: not attributable to one signal

	mu      sync.Mutex
	count   int64
	lastLog time.Time
}

func newErrorHandler(l *slog.Logger, msg string, signal int) *errorHandler {
	return &errorHandler{log: l, msg: msg, signal: signal}
}

// Handle implements otel.ErrorHandler.
func (h *errorHandler) Handle(err error) {
	if err == nil {
		return
	}
	class := errorClass(err)
	if h.signal >= 0 {
		for i, n := range classNames {
			if n == class {
				selfStats.failures[h.signal][i].Add(1)
			}
		}
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
	h.log.Warn(h.msg, "count", n, "error_class", class)
}

// The process-wide diagnostics of the SDK: one error handler registered once, which routes to the
// logger of the provider built last.
var (
	diagnosticsOnce sync.Once
	diagnostics     atomic.Pointer[errorHandler]
)

type handlerFunc func(error)

func (f handlerFunc) Handle(err error) { f(err) }

// installDiagnostics routes the SDK's logger and its error handler to l: the errors that reach the
// process-wide handler are logged as `telemetry error`.
func installDiagnostics(l *slog.Logger) {
	otel.SetLogger(logr.FromSlogHandler(l.Handler()))
	diagnostics.Store(newErrorHandler(l, "telemetry error", -1))
	diagnosticsOnce.Do(func() {
		otel.SetErrorHandler(handlerFunc(func(err error) {
			if h := diagnostics.Load(); h != nil {
				h.Handle(err)
			}
		}))
	})
}

// reportingMetricExporter hands export failures to the handler of the metrics instead of the
// process-wide one, and never returns them: the periodic reader would report them to it.
type reportingMetricExporter struct {
	sdkmetric.Exporter
	h *errorHandler
}

// Export exports the metrics; a failure is reported and not returned.
func (e *reportingMetricExporter) Export(ctx context.Context, rm *metricdata.ResourceMetrics) error {
	if err := e.Exporter.Export(ctx, rm); err != nil {
		e.h.Handle(err)
	}
	return nil
}

// registerSelf registers M39, M40 and M41, read from selfStats and the redaction counter at each
// collection.
func registerSelf(k *maker) {
	spans := k.int64ObservableCounter("pandokos.telemetry.spans")
	failures := k.int64ObservableCounter("pandokos.telemetry.export_failures")
	redacted := k.int64ObservableCounter("pandokos.log.redactions")
	if k.err != nil {
		return
	}
	states := [...]struct {
		state string
		n     *atomic.Int64
	}{{"ended", &selfStats.ended}, {"exported", &selfStats.exported}, {"failed", &selfStats.failed}}
	var spanSets [len(states)]attribute.Set
	for i, s := range states {
		spanSets[i] = attribute.NewSet(attribute.String("pandokos.telemetry.state", s.state))
	}
	var failureSets [len(signalNames)][len(classNames)]attribute.Set
	for s, signal := range signalNames {
		for c, class := range classNames {
			failureSets[s][c] = attribute.NewSet(attribute.String("pandokos.telemetry.signal", signal), attribute.String("error.type", class))
		}
	}
	_, err := k.meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		for i, s := range states {
			o.ObserveInt64(spans, s.n.Load(), metric.WithAttributeSet(spanSets[i]))
		}
		for s := range signalNames {
			for c := range classNames {
				o.ObserveInt64(failures, selfStats.failures[s][c].Load(), metric.WithAttributeSet(failureSets[s][c]))
			}
		}
		o.ObserveInt64(redacted, Redactions())
		return nil
	}, spans, failures, redacted)
	k.fail(err)
}
