package telemetry_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"

	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/otlptest"
	"github.com/pandokos/pandokos/test/testutil"
)

const (
	scopeName      = "github.com/pandokos/pandokos"
	headerCanary   = "synthetic-collector-header-secret-5d21"
	headerName     = "X-Collector-Key"
	callerTrace    = "4bf92f3577b34da6a3ce929d0e0e4736"
	callerSpan     = "00f067aa0ba902b7"
	serviceVersion = "v9.9.9"
)

// countingTransport counts the exports sent through it.
type countingTransport struct {
	inner http.RoundTripper
	n     atomic.Int64
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return c.inner.RoundTrip(r)
}

func newTraces(t *testing.T, endpoint *url.URL, mutate func(*telemetry.TracesOptions)) (*telemetry.Traces, *testutil.LogBuffer) {
	t.Helper()
	logs := &testutil.LogBuffer{}
	o := telemetry.TracesOptions{
		Endpoint: endpoint, Headers: map[string]string{headerName: headerCanary}, Timeout: time.Second, SampleRatio: 1,
		Attributes: map[string]string{"deployment.environment": "test"}, ServiceVersion: serviceVersion,
		Logger: telemetry.NewLogger(logs, "debug"),
	}
	if endpoint != nil {
		o.Transport = testutil.TestGuard(t).Transport(true)
	}
	if mutate != nil {
		mutate(&o)
	}
	tr, err := telemetry.NewTraces(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = tr.Shutdown(ctx)
	})
	return tr, logs
}

func emit(tr *telemetry.Traces, names ...string) {
	tracer := tr.Provider().Tracer(scopeName, trace.WithInstrumentationVersion(serviceVersion))
	for _, n := range names {
		_, s := tracer.Start(context.Background(), n)
		s.End()
	}
}

func shutdown(t *testing.T, tr *telemetry.Traces) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tr.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func resourceAttrs(rs interface{ GetAttributes() []*commonpb.KeyValue }) map[string]string {
	m := map[string]string{}
	for _, kv := range rs.GetAttributes() {
		m[kv.Key] = kv.Value.GetStringValue()
	}
	return m
}

// TestExport is T-499.
func TestExport(t *testing.T) {
	for name, path := range map[string]string{"a path without a trailing slash": "/collector", "a path with a trailing slash": "/collector/", "no path": ""} {
		t.Run(name, func(t *testing.T) {
			c := otlptest.New(t)
			u := c.URL()
			u.Path = path
			ct := &countingTransport{inner: testutil.TestGuard(t).Transport(true)}
			tr, _ := newTraces(t, u, func(o *telemetry.TracesOptions) { o.Transport = ct })
			emit(tr, "one", "two")
			shutdown(t, tr)

			testutil.Eventually(t, 5*time.Second, "the export", func() bool { return len(c.Spans()) == 2 })
			reqs := c.Requests()
			if len(reqs) == 0 {
				t.Fatal("no request")
			}
			wantPath := strings.TrimSuffix(path, "/") + "/v1/traces"
			if reqs[0].Path != wantPath {
				t.Errorf("path %q, want %q", reqs[0].Path, wantPath)
			}
			if got := reqs[0].Header.Get(headerName); got != headerCanary {
				t.Errorf("header %s = %q", headerName, got)
			}
			if ct := reqs[0].Header.Get("Content-Type"); ct != "application/x-protobuf" {
				t.Errorf("Content-Type %q", ct)
			}
			if ce := reqs[0].Header.Get("Content-Encoding"); ce != "" {
				t.Errorf("the export is compressed: %q", ce)
			}
			if ct.n.Load() == 0 {
				t.Error("the export did not go through the transport that was given")
			}
			rs := c.ResourceSpans()
			if len(rs) == 0 {
				t.Fatal("no resource")
			}
			attrs := resourceAttrs(rs[0].GetResource())
			if attrs["service.name"] != "pandokos" || attrs["service.version"] != serviceVersion || attrs["deployment.environment"] != "test" {
				t.Errorf("resource %v", attrs)
			}
			if id := attrs["service.instance.id"]; len(id) < 16 {
				t.Errorf("service.instance.id %q", id)
			}
			scope := rs[0].GetScopeSpans()[0].GetScope()
			if scope.GetName() != scopeName || scope.GetVersion() != serviceVersion {
				t.Errorf("scope %q %q", scope.GetName(), scope.GetVersion())
			}
		})
	}

	t.Run("an attribute may replace service.name; the instance ID differs per provider", func(t *testing.T) {
		c := otlptest.New(t)
		ids := map[string]bool{}
		for i := 0; i < 2; i++ {
			tr, _ := newTraces(t, c.URL(), func(o *telemetry.TracesOptions) { o.Attributes = map[string]string{"service.name": "custom-name"} })
			emit(tr, "x")
			shutdown(t, tr)
		}
		testutil.Eventually(t, 5*time.Second, "both exports", func() bool { return len(c.ResourceSpans()) >= 2 })
		for _, rs := range c.ResourceSpans() {
			a := resourceAttrs(rs.GetResource())
			if a["service.name"] != "custom-name" {
				t.Errorf("service.name %q", a["service.name"])
			}
			ids[a["service.instance.id"]] = true
		}
		if len(ids) != 2 {
			t.Errorf("the instance ID is the same for two providers: %v", ids)
		}
	})

	t.Run("a redirect answer is not followed", func(t *testing.T) {
		target := otlptest.New(t)
		redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL().String()+"/v1/traces", http.StatusTemporaryRedirect)
		}))
		t.Cleanup(redirecting.Close)
		u, _ := url.Parse(redirecting.URL)
		tr, _ := newTraces(t, u, nil)
		emit(tr, "x")
		shutdown(t, tr)
		time.Sleep(200 * time.Millisecond)
		if n := len(target.Requests()); n != 0 {
			t.Errorf("the redirect target received %d requests", n)
		}
	})

	t.Run("the options are checked", func(t *testing.T) {
		logs := telemetry.NewLogger(&testutil.LogBuffer{}, "info")
		c := otlptest.New(t)
		if _, err := telemetry.NewTraces(telemetry.TracesOptions{Endpoint: c.URL(), Timeout: time.Second, SampleRatio: 1, Logger: logs}); err == nil {
			t.Error("an endpoint without a transport was accepted")
		}
		if _, err := telemetry.NewTraces(telemetry.TracesOptions{SampleRatio: 1}); err == nil {
			t.Error("no logger was accepted")
		}
		if _, err := telemetry.NewTraces(telemetry.TracesOptions{SampleRatio: 1.5, Logger: logs}); err == nil {
			t.Error("a ratio over 1 was accepted")
		}
		if _, err := telemetry.NewTraces(telemetry.TracesOptions{SampleRatio: -0.1, Logger: logs}); err == nil {
			t.Error("a negative ratio was accepted")
		}
	})
}

// TestCollectorNeverBlocks is T-500: a collector that stalls, fails, is closed or is refused by
// the guard does not slow the ending of spans, and Shutdown returns within its context.
func TestCollectorNeverBlocks(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T) *url.URL{
		"stalled": func(t *testing.T) *url.URL {
			c := otlptest.New(t)
			c.Stall()
			t.Cleanup(c.Release)
			return c.URL()
		},
		"answering 503": func(t *testing.T) *url.URL {
			c := otlptest.New(t)
			c.Fail(1 << 30)
			return c.URL()
		},
		"closed": func(t *testing.T) *url.URL {
			c := otlptest.New(t)
			u := c.URL()
			c.Close()
			return u
		},
		"refused by the guard": func(*testing.T) *url.URL {
			u, _ := url.Parse("http://169.254.169.254:4318")
			return u
		},
	} {
		t.Run(name, func(t *testing.T) {
			tr, logs := newTraces(t, setup(t), nil)
			tracer := tr.Provider().Tracer(scopeName)
			began := time.Now()
			for i := 0; i < 10_000; i++ {
				_, s := tracer.Start(context.Background(), "synthetic")
				s.End()
			}
			if took := time.Since(began); took > 500*time.Millisecond {
				t.Errorf("ending 10 000 spans took %s, the bound is 500 ms", took)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			defer cancel()
			sbegan := time.Now()
			_ = tr.Shutdown(ctx)
			if took := time.Since(sbegan); took > 3*time.Second {
				t.Errorf("Shutdown took %s with a context of 1.5 s", took)
			}
			lines := logs.Find(t, "trace export failing")
			if len(lines) > 3 {
				t.Errorf("%d `trace export failing` lines", len(lines))
			}
			for _, l := range lines {
				switch l["error_class"] {
				case "timeout", "connection", "status", "other":
				default:
					t.Errorf("error_class %v", l["error_class"])
				}
				if l["level"] != "WARN" || l["count"] == nil {
					t.Errorf("the line: %v", l)
				}
			}
			if name == "answering 503" && len(lines) > 0 && lines[0]["error_class"] != "status" {
				t.Errorf("error_class %v for a 503", lines[0]["error_class"])
			}
			testutil.AssertAbsent(t, "the logs", logs.String(), headerCanary)
			testutil.AssertNoCanary(t, "the logs", logs.String())
		})
	}

	t.Run("a failing collector is logged", func(t *testing.T) {
		c := otlptest.New(t)
		c.Fail(1 << 30)
		tr, logs := newTraces(t, c.URL(), nil)
		emit(tr, "x")
		shutdown(t, tr)
		testutil.Eventually(t, 5*time.Second, "`trace export failing`", func() bool { return len(logs.Find(t, "trace export failing")) >= 1 })
	})
}

// TestEnvironment is T-501: no OTEL_* variable configures Pandokos, except that
// OTEL_RESOURCE_ATTRIBUTES adds attributes to the resource and never replaces the ones Pandokos sets.
func TestEnvironment(t *testing.T) {
	decoy := otlptest.New(t)
	proxyURL, proxied := testutil.ConnectProxy(t)
	for k, v := range map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT":        decoy.URL().String(),
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": decoy.URL().String() + "/v1/traces",
		"OTEL_EXPORTER_OTLP_HEADERS":         "X-Decoy=1",
		"OTEL_EXPORTER_OTLP_TRACES_HEADERS":  "X-Decoy=1",
		"OTEL_TRACES_SAMPLER":                "always_off",
		"OTEL_SDK_DISABLED":                  "true",
		"OTEL_BSP_SCHEDULE_DELAY":            "600000",
		"OTEL_BSP_MAX_QUEUE_SIZE":            "1",
		"OTEL_EXPORTER_OTLP_COMPRESSION":     "gzip",
		"OTEL_EXPORTER_OTLP_PROTOCOL":        "grpc",
		"HTTP_PROXY":                         proxyURL.String(),
		"HTTPS_PROXY":                        proxyURL.String(),
		"NO_PROXY":                           "",
		"OTEL_RESOURCE_ATTRIBUTES":           "deployment.environment=from-env,service.name=hijack,extra.key=from-env",
	} {
		t.Setenv(k, v)
	}
	c := otlptest.New(t)
	tr, _ := newTraces(t, c.URL(), func(o *telemetry.TracesOptions) {
		o.Attributes = map[string]string{"deployment.environment": "configured"}
	})
	emit(tr, "one", "two", "three")
	shutdown(t, tr)

	testutil.Eventually(t, 5*time.Second, "the export", func() bool { return len(c.Spans()) == 3 })
	if n := len(decoy.Requests()); n != 0 {
		t.Errorf("the collector of the environment received %d requests", n)
	}
	if n := len(proxied()); n != 0 {
		t.Errorf("the proxy of the environment was used %d times", n)
	}
	for _, r := range c.Requests() {
		if r.Header.Get("X-Decoy") != "" || r.Header.Get(headerName) != headerCanary || r.Header.Get("Content-Encoding") != "" {
			t.Errorf("headers of an export: %v", r.Header)
		}
	}
	attrs := resourceAttrs(c.ResourceSpans()[0].GetResource())
	if attrs["service.name"] != "pandokos" || attrs["deployment.environment"] != "configured" {
		t.Errorf("OTEL_RESOURCE_ATTRIBUTES replaced what Pandokos sets: %v", attrs)
	}
	if attrs["extra.key"] != "from-env" {
		t.Errorf("OTEL_RESOURCE_ATTRIBUTES no longer adds its attributes (the guide and the warning say it does): %v", attrs)
	}
}

// TestSampling is T-502.
func TestSampling(t *testing.T) {
	remote := func(sampled bool) context.Context {
		tid, _ := trace.TraceIDFromHex(callerTrace)
		sid, _ := trace.SpanIDFromHex(callerSpan)
		flags := trace.FlagsSampled
		if !sampled {
			flags = 0
		}
		return trace.ContextWithRemoteSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: flags, Remote: true}))
	}
	count := func(ratio float64, ctx context.Context, n int) int {
		c := otlptest.New(t)
		tr, _ := newTraces(t, c.URL(), func(o *telemetry.TracesOptions) { o.SampleRatio = ratio })
		tracer := tr.Provider().Tracer(scopeName)
		for i := 0; i < n; i++ {
			_, s := tracer.Start(ctx, "x")
			s.End()
		}
		shutdown(t, tr)
		time.Sleep(100 * time.Millisecond)
		return len(c.Spans())
	}
	t.Run("ratio 1 samples a request without context", func(t *testing.T) {
		if got := count(1, context.Background(), 5); got != 5 {
			t.Errorf("%d spans exported, want 5", got)
		}
	})
	t.Run("ratio 0 samples none of them", func(t *testing.T) {
		if got := count(0, context.Background(), 5); got != 0 {
			t.Errorf("%d spans exported, want 0", got)
		}
	})
	t.Run("a sampled remote parent is followed, whatever the ratio", func(t *testing.T) {
		if got := count(0, remote(true), 3); got != 3 {
			t.Errorf("%d spans exported, want 3", got)
		}
	})
	t.Run("an unsampled remote parent is followed, whatever the ratio", func(t *testing.T) {
		if got := count(1, remote(false), 3); got != 0 {
			t.Errorf("%d spans exported, want 0", got)
		}
	})
	t.Run("without an endpoint: valid IDs, not sampled, nothing sent", func(t *testing.T) {
		tr, _ := newTraces(t, nil, nil)
		_, s := tr.Provider().Tracer(scopeName).Start(context.Background(), "x")
		sc := s.SpanContext()
		s.End()
		if !sc.IsValid() || !sc.TraceID().IsValid() || !sc.SpanID().IsValid() {
			t.Errorf("the span context is not valid: %v", sc)
		}
		if sc.IsSampled() {
			t.Error("a span is sampled with no collector")
		}
		_, child := tr.Provider().Tracer(scopeName).Start(remote(true), "y")
		if got := child.SpanContext(); got.TraceID().String() != callerTrace || !got.IsSampled() {
			t.Errorf("a sampled remote parent without a collector: %v", got)
		}
		child.End()
		shutdown(t, tr)
	})
}

// TestDiagnosticsGoToTheLogger is T-503: an export failure and an SDK-internal message are JSON
// lines on the logger, and nothing is written to the process's standard error.
func TestDiagnosticsGoToTheLogger(t *testing.T) {
	stderr := captureStderr(t)
	c := otlptest.New(t)
	c.Fail(1 << 30)
	// The SDK says "TracerProvider created" through its own logger when the provider is built:
	// that logger is the product's.
	tr, logs := newTraces(t, c.URL(), nil)
	testutil.Eventually(t, 5*time.Second, "the SDK's own message on the logger", func() bool { return len(logs.Find(t, "TracerProvider created")) >= 1 })
	emit(tr, "x")
	shutdown(t, tr)
	testutil.Eventually(t, 5*time.Second, "`trace export failing`", func() bool { return len(logs.Find(t, "trace export failing")) >= 1 })

	// An error handed to the SDK's error handler is a line of the logger too.
	_, logs2 := newTraces(t, nil, nil)
	otel.Handle(errors.New("synthetic sdk error"))
	testutil.Eventually(t, 5*time.Second, "the handled error on the logger", func() bool { return len(logs2.Find(t, "trace export failing")) >= 1 })

	for _, b := range []*testutil.LogBuffer{logs, logs2} {
		for _, line := range b.Lines(t) { // Lines fails the test on a line that is not JSON
			if line["msg"] == nil || line["level"] == nil {
				t.Errorf("a line without msg or level: %v", line)
			}
		}
		testutil.AssertAbsent(t, "the logs", b.String(), headerCanary)
	}
	if got := stderr(); strings.TrimSpace(got) != "" {
		t.Errorf("something was written to standard error: %q", got)
	}
}
