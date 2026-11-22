package telemetry_test

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/otlptest"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestNoReaderNoProvider is T-906: without an endpoint and without the scrape switch the meter
// provider is the no-op one: no goroutine is started, Handler() is nil and every recording is
// harmless; with telemetry.metrics.otlp off and an endpoint, traces are exported and metrics are not.
func TestNoReaderNoProvider(t *testing.T) {
	logs := telemetry.NewLogger(&testutil.LogBuffer{}, "debug")
	res, err := telemetry.NewResource(nil, serviceVersion)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("neither reader", func(t *testing.T) {
		for name, o := range map[string]telemetry.MetricsOptions{
			"no endpoint":              {Resource: res, OTLP: true, Interval: time.Second, Logger: logs, Sources: fullSources()},
			"an endpoint and otlp off": {Resource: res, OTLP: false, Interval: time.Second, Logger: logs, Sources: fullSources()},
			"nothing but the interval": {Resource: res, Interval: time.Second, Logger: logs},
		} {
			c := otlptest.New(t)
			if name == "an endpoint and otlp off" {
				o.Endpoint, o.Transport = c.URL(), testutil.TestGuard(t).Transport(true)
			}
			runtime.GC()
			before := runtime.NumGoroutine()
			m, err := telemetry.NewMetrics(o)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if m.Handler() != nil {
				t.Errorf("%s: Handler() is not nil", name)
			}
			for i := 0; i < 100; i++ {
				m.ObserveRequest(streamRequest("model-a"))
			}
			time.Sleep(100 * time.Millisecond)
			if after := runtime.NumGoroutine(); after > before {
				t.Errorf("%s: %d goroutines before, %d after: the no-op provider starts none", name, before, after)
			}
			if err := m.Shutdown(t.Context()); err != nil {
				t.Errorf("%s: Shutdown: %v", name, err)
			}
			if n := len(c.Requests()); n != 0 {
				t.Errorf("%s: the collector received %d exports", name, n)
			}
		}
	})

	t.Run("the scrape switch alone makes a handler and no export", func(t *testing.T) {
		m, err := telemetry.NewMetrics(telemetry.MetricsOptions{Resource: res, Prometheus: true, Interval: time.Second, Logger: logs})
		if err != nil {
			t.Fatal(err)
		}
		defer m.Shutdown(t.Context()) //nolint:errcheck // test cleanup
		if m.Handler() == nil {
			t.Error("Handler() is nil with the scrape switch on")
		}
	})

	t.Run("otlp off: traces are exported and metrics are not", func(t *testing.T) {
		c := otlptest.New(t)
		r := newRigOn(t, c, func(o *telemetry.MetricsOptions) { o.OTLP = false })
		tr, _ := newTraces(t, c.URL(), func(o *telemetry.TracesOptions) { o.Resource = r.res })
		emit(tr, "x")
		r.m.ObserveRequest(chatRequest("model-a"))
		shutdown(t, tr)
		testutil.Eventually(t, 5*time.Second, "the span", func() bool { return len(c.Spans()) == 1 })
		time.Sleep(300 * time.Millisecond)
		for _, q := range c.Requests() {
			if strings.HasSuffix(q.Path, "/v1/metrics") {
				t.Errorf("metrics were exported with telemetry.metrics.otlp off: %s", q.Path)
			}
		}
		if len(c.Metrics()) != 0 {
			t.Error("the collector holds metrics")
		}
	})
}
