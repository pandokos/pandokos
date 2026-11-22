package telemetry_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/otlptest"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestMetricExport is T-904: metrics reach the collector as protobuf on <path>/v1/metrics with the
// configured headers, uncompressed, through the transport that was given; the resource is the one of
// the traces; a redirect is not followed; and no OTEL_* variable changes any of it.
func TestMetricExport(t *testing.T) {
	for name, path := range map[string]string{"a path without a trailing slash": "/collector", "a path with a trailing slash": "/collector/", "no path": ""} {
		t.Run(name, func(t *testing.T) {
			c := otlptest.New(t)
			u := c.URL()
			u.Path = path
			ct := &countingTransport{inner: testutil.TestGuard(t).Transport(true)}
			r := newRigOn(t, c, func(o *telemetry.MetricsOptions) { o.Endpoint, o.Transport = u, ct })
			snap := r.observe(chatRequest("model-a"))

			var metricsReqs []otlptest.Request
			for _, q := range c.Requests() {
				if strings.HasSuffix(q.Path, "/v1/metrics") {
					metricsReqs = append(metricsReqs, q)
				}
			}
			if len(metricsReqs) == 0 {
				t.Fatal("no export reached /v1/metrics")
			}
			first := metricsReqs[0]
			if want := strings.TrimSuffix(path, "/") + "/v1/metrics"; first.Path != want {
				t.Errorf("path %q, want %q", first.Path, want)
			}
			if got := first.Header.Get(headerName); got != headerCanary {
				t.Errorf("header %s = %q", headerName, got)
			}
			if ct := first.Header.Get("Content-Type"); ct != "application/x-protobuf" {
				t.Errorf("Content-Type %q", ct)
			}
			if ce := first.Header.Get("Content-Encoding"); ce != "" {
				t.Errorf("the export is compressed: %q", ce)
			}
			if ct.n.Load() == 0 {
				t.Error("the export did not go through the transport that was given")
			}
			if snap.Resource["service.name"] != "pandokos" || snap.Resource["service.version"] != serviceVersion || snap.Resource["deployment.environment"] != "test" {
				t.Errorf("resource %v", snap.Resource)
			}
			testutil.AssertAbsent(t, "the logs", r.logs.String(), headerCanary)
		})
	}

	t.Run("the resource is the traces' resource", func(t *testing.T) {
		tc := otlptest.New(t)
		r := newRig(t, nil)
		tr, _ := newTraces(t, tc.URL(), func(o *telemetry.TracesOptions) { o.Resource = r.res })
		emit(tr, "x")
		shutdown(t, tr)
		testutil.Eventually(t, 5*time.Second, "the span", func() bool { return len(tc.ResourceSpans()) > 0 })
		snap := r.observe(chatRequest("model-a"))
		traces := resourceAttrs(tc.ResourceSpans()[0].GetResource())
		if traces["service.instance.id"] == "" || traces["service.instance.id"] != snap.Resource["service.instance.id"] {
			t.Errorf("service.instance.id: traces %q, metrics %q", traces["service.instance.id"], snap.Resource["service.instance.id"])
		}
		for k, v := range traces {
			if snap.Resource[k] != v {
				t.Errorf("resource attribute %s: traces %q, metrics %q", k, v, snap.Resource[k])
			}
		}
	})

	t.Run("a redirect answer is not followed", func(t *testing.T) {
		target := otlptest.New(t)
		redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL().String()+"/v1/metrics", http.StatusTemporaryRedirect)
		}))
		t.Cleanup(redirecting.Close)
		u, _ := url.Parse(redirecting.URL)
		r := newRig(t, func(o *telemetry.MetricsOptions) { o.Endpoint = u })
		r.m.ObserveRequest(chatRequest("model-a"))
		time.Sleep(300 * time.Millisecond)
		if n := len(target.Requests()); n != 0 {
			t.Errorf("the redirect target received %d requests", n)
		}
	})

	t.Run("OTEL_* variables change nothing", func(t *testing.T) {
		other := otlptest.New(t)
		t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", other.URL().String()+"/elsewhere")
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", other.URL().String())
		t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "X-From-Env=1")
		t.Setenv("OTEL_EXPORTER_OTLP_COMPRESSION", "gzip")
		t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "3600000")
		t.Setenv("OTEL_METRIC_EXPORT_TIMEOUT", "3600000")
		c := otlptest.New(t)
		r := newRigOn(t, c, nil)
		r.observe(chatRequest("model-a")) // two exports within a few intervals: the variable's hour is ignored
		if n := len(other.Requests()); n != 0 {
			t.Errorf("the endpoint of the environment received %d requests", n)
		}
		for _, q := range c.Requests() {
			if q.Header.Get("X-From-Env") != "" || q.Header.Get("Content-Encoding") != "" || q.Header.Get(headerName) != headerCanary {
				t.Errorf("the environment changed the export: %v", q.Header)
			}
		}
	})

	t.Run("the options are checked", func(t *testing.T) {
		logs := telemetry.NewLogger(&testutil.LogBuffer{}, "info")
		c := otlptest.New(t)
		res, err := telemetry.NewResource(nil, serviceVersion)
		if err != nil {
			t.Fatal(err)
		}
		base := telemetry.MetricsOptions{Resource: res, Endpoint: c.URL(), OTLP: true, Timeout: time.Second, Interval: time.Second, Logger: logs,
			Transport: testutil.TestGuard(t).Transport(true)}
		bad := map[string]func(*telemetry.MetricsOptions){
			"no logger":                     func(o *telemetry.MetricsOptions) { o.Logger = nil },
			"an endpoint without transport": func(o *telemetry.MetricsOptions) { o.Transport = nil },
			"a zero interval":               func(o *telemetry.MetricsOptions) { o.Interval = 0 },
		}
		for name, mutate := range bad {
			o := base
			mutate(&o)
			if m, err := telemetry.NewMetrics(o); err == nil {
				_ = m.Shutdown(t.Context())
				t.Errorf("%s was accepted", name)
			}
		}
	})
}
