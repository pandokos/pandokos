package gateway_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/testutil"
)

// telemetryAdapter is the test-local twin of the adapter of cmd/pandokos/metrics.go: it copies the
// observation into a telemetry.Request field by field, without allocating.
type telemetryAdapter struct{ m *telemetry.Metrics }

func (a telemetryAdapter) Observe(o gateway.RequestObservation) {
	a.m.ObserveRequest(telemetry.Request{
		Method: o.Method, Scheme: o.Scheme, Route: o.Route, Status: o.Status, Outcome: o.Outcome, ErrorCode: o.ErrorCode,
		LimitState: o.LimitState, Provider: o.Provider, Family: o.Family, Operation: o.Operation, Model: o.Model, Duration: o.Duration,
		Forwarded: o.Forwarded, Call: o.Call, FirstChunk: o.FirstChunk, Stream: o.Stream, CallError: o.CallError,
		UpstreamStatus: o.UpstreamStatus, CredentialID: o.CredentialID,
		InputTokens: o.InputTokens, OutputTokens: o.OutputTokens, InputSource: o.InputSource, OutputSource: o.OutputSource,
		CostKnown: o.CostKnown, CostAmount: o.CostAmount, CostCurrency: o.CostCurrency, CostReason: o.CostReason,
		Refusal:  (*telemetry.RefusalObservation)(o.Refusal),
		Estimate: (*telemetry.EstimateObservation)(o.Estimate),
	})
}

type noopMetrics struct{}

func (noopMetrics) Observe(gateway.RequestObservation) {}

// realMetrics is a telemetry.Metrics with the scrape reader (a manual reader): recording is
// in-memory aggregation, with no export.
func realMetrics(tb testing.TB) *telemetry.Metrics {
	tb.Helper()
	res, err := telemetry.NewResource(nil, "v0.0.0-test")
	if err != nil {
		tb.Fatal(err)
	}
	m, err := telemetry.NewMetrics(telemetry.MetricsOptions{
		Resource: res, Prometheus: true, Interval: time.Minute, ServiceVersion: "v0.0.0-test",
		Logger: telemetry.NewLogger(&testutil.LogBuffer{}, "error"),
	})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = m.Shutdown(tb.Context()) })
	return m
}

// oneCall is a 200 chat request through the harness.
func oneCall(tb testing.TB, h *harness, client *http.Client, body []byte) {
	req, err := http.NewRequest(http.MethodPost, h.srv.URL+route, bytes.NewReader(body))
	if err != nil {
		tb.Fatal(err)
	}
	req.Header["Authorization"] = authed()["Authorization"]
	req.Header["Content-Type"] = authed()["Content-Type"]
	resp, err := client.Do(req)
	if err != nil {
		tb.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		tb.Fatalf("status %d", resp.StatusCode)
	}
}

// metricsHarnesses builds two harnesses over the same options but for the Metrics: the real one and
// a no-op.
func metricsHarnesses(t *testing.T) (real, noop *harness, m *telemetry.Metrics) {
	m = realMetrics(t)
	build := func(mx gateway.Metrics) *harness {
		return newHarness(t, func(o *gateway.Options) { o.Metrics = mx })
	}
	return build(telemetryAdapter{m}), build(noopMetrics{}), m
}

// TestMetricsAllocations is T-915: recording adds at most 8 allocations to a 200 chat request,
// measured as the allocations of the request with the real telemetry.Metrics against the same with
// a no-op Metrics, after 1,000 warm-up requests (the attribute sets are cached by then).
func TestMetricsAllocations(t *testing.T) {
	real, noop, m := metricsHarnesses(t)
	body := chat("fake-basic", false)
	client := real.srv.Client()
	client2 := noop.srv.Client()
	for i := 0; i < 1000; i++ {
		oneCall(t, real, client, body)
		oneCall(t, noop, client2, body)
	}

	// The real Metrics recorded what the gateway observed.
	if m.Handler() == nil {
		t.Fatal("the Metrics has no scrape handler")
	}
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "pandokos_gateway_requests_total") || !strings.Contains(rec.Body.String(), `gen_ai_request_model="fake-basic"`) {
		t.Fatalf("the warm-up requests are not in the Metrics:\n%s", rec.Body.String())
	}

	// The best of three runs of each: a collection or a goroutine switch must not decide the figure.
	measure := func(h *harness, c *http.Client) float64 {
		best := -1.0
		for i := 0; i < 3; i++ {
			a := testing.AllocsPerRun(200, func() { oneCall(t, h, c, body) })
			if best < 0 || a < best {
				best = a
			}
		}
		return best
	}
	with, without := measure(real, client), measure(noop, client2)
	t.Logf("allocations per request: %.1f with the Metrics, %.1f with a no-op: +%.1f", with, without, with-without)
	if with-without > 8 {
		t.Errorf("recording adds %.1f allocations per request, want at most 8", with-without)
	}
}

// BenchmarkRequestWithMetrics reports the cost of the same request with the real Metrics and with a
// no-op one.
func BenchmarkRequestWithMetrics(b *testing.B) {
	for _, c := range []struct {
		name string
		mx   func(testing.TB) gateway.Metrics
	}{
		{"metrics", func(tb testing.TB) gateway.Metrics { return telemetryAdapter{realMetrics(tb)} }},
		{"noop", func(testing.TB) gateway.Metrics { return noopMetrics{} }},
	} {
		b.Run(c.name, func(b *testing.B) {
			h := newHarnessWith(b, fakeprovider.New(b), testutil.TestGuard(b), func(o *gateway.Options) { o.Metrics = c.mx(b) })
			body := chat("fake-basic", false)
			client := h.srv.Client()
			for i := 0; i < 200; i++ {
				oneCall(b, h, client, body)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				oneCall(b, h, client, body)
			}
		})
	}
}
