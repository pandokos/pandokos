package gateway_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/egress"
	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/openai"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/usage"

	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

const route = "/openai/v1/chat/completions"

// testKey is a virtual key generated when the tests start; the static authenticator knows it
// and its principal. No key is written in the source.
var (
	testKey       = newTestKey()
	testPrincipal = testutil.InferencePrincipal()
	testAuth      = testutil.StaticAuthenticator(map[string]authz.Principal{testKey: testPrincipal})
)

func newTestKey() string {
	k, _, _, err := authn.Generate(authn.KindTenant)
	if err != nil {
		panic(err)
	}
	return k.Reveal()
}

type harness struct {
	up      *fakeprovider.Server // nil for a harness over a raw handler
	srv     *httptest.Server     // serves the gateway.Handler
	handler *gateway.Handler
	logs    *testutil.LogBuffer
	cat     *testutil.Catalogue
	creds   *testutil.Credentials
	targets map[string]gateway.Target // "provider/model": the catalogue's targets; tests may add
	rec     *testutil.MemoryRecorder  // the recorder the gateway was built with (unless a test replaced it)
	spans   *tracetest.SpanRecorder   // the spans of the tracer provider the gateway was built with
}

// fakeModels are the models of the fake provider; the harness gives each a target of provider
// "openai" (and "fake-model", the default target of the harness).
var fakeModels = []string{
	fakeprovider.ModelBasic, fakeprovider.ModelDetails, fakeprovider.ModelGatedStream, fakeprovider.ModelHang,
	fakeprovider.ModelError429, fakeprovider.ModelError500Echo, fakeprovider.ModelUsageOnLastChunk,
	fakeprovider.ModelRedirect, fakeprovider.ModelEchoAuth, fakeprovider.ModelClose, "fake-model",
	fakeprovider.ModelNoUsage, fakeprovider.ModelStreamError, fakeprovider.ModelKeepalive, fakeprovider.ModelTools,
	fakeprovider.ModelTinyWrites, fakeprovider.ModelCut, fakeprovider.ModelCRLF, fakeprovider.ModelBigEvent,
}

// trusting returns an option function for testutil.TestGuard that trusts a TLS fake.
func trusting(up *fakeprovider.Server) func(*egress.Options) {
	return func(o *egress.Options) { o.RootCAs = up.CertPool() }
}

func newHarness(t *testing.T, mutate func(*gateway.Options)) *harness {
	t.Helper()
	return newHarnessWith(t, fakeprovider.New(t), testutil.TestGuard(t), mutate)
}

// newHarnessWith builds the gateway over the fake provider up, with real transports from guard.
func newHarnessWith(t *testing.T, up *fakeprovider.Server, guard *egress.Guard, mutate func(*gateway.Options)) *harness {
	t.Helper()
	targets := map[string]gateway.Target{}
	for _, m := range fakeModels {
		targets["openai/"+m] = testutil.TargetFor(up, "openai", m)
	}
	h := build(t, targets, guard, mutate)
	h.up = up
	return h
}

// newRaw builds the gateway in front of a handler that is the provider: every model of the fake
// is a private target at its address.
func newRaw(t *testing.T, handler http.HandlerFunc) *harness {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	base, _ := url.Parse(ts.URL)
	targets := map[string]gateway.Target{}
	for _, m := range fakeModels {
		targets["openai/"+m] = gateway.Target{Provider: "openai", Endpoint: "synthetic-endpoint", ModelID: "synthetic-model-" + m,
			Model: m, UpstreamModel: m, BaseURL: base, Private: true}
	}
	return build(t, targets, testutil.TestGuard(t), nil)
}

func build(t *testing.T, targets map[string]gateway.Target, guard *egress.Guard, mutate func(*gateway.Options)) *harness {
	t.Helper()
	logs := &testutil.LogBuffer{}
	cat := testutil.StaticCatalogue(map[string]string{"openai": "openai"}, targets)
	creds := testutil.StaticCredentials(map[string]string{"openai": fixtures.CanaryProviderKey})
	rec := &testutil.MemoryRecorder{}
	tp, spans := testutil.SpanRecorder(t)
	o := gateway.Options{
		Recorder:               rec,
		Tracer:                 tp,
		Logger:                 telemetry.NewLogger(logs, "debug"),
		Authenticator:          testAuth,
		Catalogue:              cat,
		Credentials:            creds,
		Families:               map[string]family.Adapter{"openai": openai.Adapter()},
		DefaultFamily:          "openai",
		Public:                 guard.Transport(false),
		Private:                guard.Transport(true),
		MaxRequestBodyBytes:    1 << 20,
		RequestBodyReadTimeout: 2 * time.Second,
		MaxRequestDuration:     10 * time.Second,
	}
	if mutate != nil {
		mutate(&o)
	}
	g, err := gateway.New(o)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(g)
	allowManyHeaderValues(srv.Config)
	srv.Start()
	t.Cleanup(srv.Close)
	return &harness{srv: srv, handler: g, logs: logs, cat: cat, creds: creds, targets: targets, rec: rec, spans: spans}
}

func chat(model string, stream bool) []byte {
	s := `{"model":"` + model + `","messages":[{"role":"user","content":"` + fixtures.CanaryPrompt + `"}]`
	if stream {
		s += `,"stream":true`
	}
	return []byte(s + "}")
}

func (h *harness) request(ctx context.Context, t *testing.T, method, path string, body []byte, hdr http.Header) (*http.Response, error) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	return h.srv.Client().Do(req)
}

func authed() http.Header {
	return http.Header{"Authorization": {"Bearer " + testKey}, "Content-Type": {"application/json"}}
}

func ctx5(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// call sends an authenticated POST to the route and returns status, headers and the whole body.
func (h *harness) call(t *testing.T, body []byte) (*http.Response, []byte) {
	t.Helper()
	resp, err := h.request(ctx5(t), t, http.MethodPost, route, body, authed())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	return resp, b
}

// requestLines waits for n request lines and returns them.
func (h *harness) requestLines(t *testing.T, n int) []map[string]any {
	t.Helper()
	var lines []map[string]any
	testutil.Eventually(t, 5*time.Second, fmt.Sprintf("%d request log lines", n), func() bool {
		lines = h.logs.Find(t, "request")
		return len(lines) >= n
	})
	return lines
}

func (h *harness) lastRequestLine(t *testing.T) map[string]any {
	t.Helper()
	l := h.requestLines(t, 1)
	return l[len(l)-1]
}

func (h *harness) awaitOutcome(t *testing.T, outcome string) map[string]any {
	t.Helper()
	var found map[string]any
	testutil.Eventually(t, 5*time.Second, "request line with outcome "+outcome, func() bool {
		for _, l := range h.logs.Find(t, "request") {
			if l["outcome"] == outcome {
				found = l
				return true
			}
		}
		return false
	})
	return found
}

// oneRecord waits for the recorder to hold exactly one record and returns it.
func (h *harness) oneRecord(t *testing.T) usage.Record {
	t.Helper()
	recs := h.records(t, 1)
	if len(recs) != 1 {
		t.Fatalf("%d records, want 1", len(recs))
	}
	return recs[0]
}

// records waits for at least n records and returns what the recorder holds.
func (h *harness) records(t *testing.T, n int) []usage.Record {
	t.Helper()
	testutil.Eventually(t, 5*time.Second, fmt.Sprintf("%d usage records", n), func() bool { return len(h.rec.Records()) >= n })
	return h.rec.Records()
}

func usageGroup(t *testing.T, line map[string]any) map[string]any {
	t.Helper()
	g, ok := line["usage"].(map[string]any)
	if !ok {
		t.Fatalf("no usage group in %v", line)
	}
	return g
}

func TestForwardNonStreaming(t *testing.T) {
	h := newHarness(t, nil)
	body := chat("fake-basic", false)
	resp, got := h.call(t, body)
	if resp.StatusCode != 200 || !bytes.Equal(got, fixtures.Read("chat_completion.json")) {
		t.Fatalf("status %d, body differs", resp.StatusCode)
	}
	reqs := h.up.Requests()
	if len(reqs) != 1 || !bytes.Equal(reqs[0].Body, body) || reqs[0].Path != "/v1/chat/completions" || reqs[0].Method != "POST" {
		t.Fatalf("provider saw %+v", reqs)
	}
	line := h.lastRequestLine(t)
	if line["usage_source"] != "provider" || line["outcome"] != "ok" || line["stream"] != false {
		t.Errorf("line: %v", line)
	}
	u := usageGroup(t, line)
	if u["input_tokens"] != float64(9) || u["output_tokens"] != float64(12) {
		t.Errorf("usage: %v", u)
	}
}

func TestStreamingEventByEvent(t *testing.T) {
	h := newHarness(t, nil)
	ctx := ctx5(t)
	resp, err := h.request(ctx, t, http.MethodPost, route, chat(fakeprovider.ModelGatedStream, true), authed())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	br := bufio.NewReader(resp.Body)
	first, err := testutil.ReadEvent(br)
	if err != nil || !strings.HasPrefix(first, "data: {") {
		t.Fatalf("first event %q %v", first, err)
	}
	if n := h.up.InFlight(); n != 1 {
		t.Fatalf("provider in flight = %d: the response was buffered", n)
	}
	h.up.Release()
	rest, err := io.ReadAll(br)
	if err != nil || !bytes.HasSuffix(rest, []byte("data: [DONE]\n\n")) {
		t.Fatalf("rest %q %v", rest, err)
	}
}

func TestStreamUsageInjectedAndStripped(t *testing.T) {
	h := newHarness(t, nil)
	body := chat("fake-basic", true)
	resp, got := h.call(t, body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	full := fixtures.Read("chat_stream_usage.sse")
	var want []byte
	for i, ev := range bytes.SplitAfter(full, []byte("\n\n")) {
		if i != 4 {
			want = append(want, ev...)
		}
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("caller stream:\n%s", got)
	}
	if bytes.Contains(got, []byte(`"choices":[]`)) || !bytes.HasSuffix(got, []byte("data: [DONE]\n\n")) {
		t.Error("usage-only event delivered or [DONE] missing")
	}
	if resp.Header.Get("Content-Length") != "" {
		t.Error("Content-Length must be removed when the body is filtered")
	}
	wantBody := string(body[:len(body)-1]) + `,"stream_options":{"include_usage":true}}`
	if reqs := h.up.Requests(); len(reqs) != 1 || string(reqs[0].Body) != wantBody {
		t.Fatalf("provider received %q, want %q", reqs[0].Body, wantBody)
	}
	line := h.lastRequestLine(t)
	if line["usage_injected"] != true || line["stream"] != true || line["usage_source"] != "provider" {
		t.Errorf("line: %v", line)
	}
	if u := usageGroup(t, line); u["input_tokens"] != float64(9) || u["output_tokens"] != float64(12) {
		t.Errorf("usage: %v", u)
	}
}

func TestStreamUsageRequestedByCaller(t *testing.T) {
	h := newHarness(t, nil)
	body := []byte(`{"model":"fake-basic","stream":true,"stream_options":{"include_usage":true}}`)
	_, got := h.call(t, body)
	if !bytes.Equal(got, fixtures.Read("chat_stream_usage.sse")) {
		t.Fatalf("caller stream differs:\n%s", got)
	}
	if reqs := h.up.Requests(); len(reqs) != 1 || !bytes.Equal(reqs[0].Body, body) {
		t.Fatalf("provider received %q", reqs[0].Body)
	}
	line := h.lastRequestLine(t)
	if line["usage_injected"] != false || line["usage_source"] != "provider" {
		t.Errorf("line: %v", line)
	}
	if u := usageGroup(t, line); u["input_tokens"] != float64(9) {
		t.Errorf("usage: %v", u)
	}
}

func TestClientDisconnectCancelsUpstream(t *testing.T) {
	run := func(t *testing.T, h *harness) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		resp, err := h.request(ctx, t, http.MethodPost, route, chat(fakeprovider.ModelGatedStream, true), authed())
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if _, err := testutil.ReadEvent(bufio.NewReader(resp.Body)); err != nil {
			t.Fatal(err)
		}
		cancel()
		if !h.up.WaitCancelled(2 * time.Second) {
			t.Fatal("the provider request was not cancelled")
		}
		h.awaitOutcome(t, "client_cancelled")
	}
	t.Run("http1", func(t *testing.T) { run(t, newHarness(t, nil)) })
	t.Run("http2", func(t *testing.T) {
		up := fakeprovider.NewTLS(t)
		h := newHarnessWith(t, up, testutil.TestGuard(t, trusting(up)), nil)
		run(t, h)
		if reqs := up.Requests(); len(reqs) != 1 || reqs[0].Proto != "HTTP/2.0" {
			t.Fatalf("the provider did not see HTTP/2: %+v", reqs)
		}
	})
}

func TestClientDisconnectBeforeResponse(t *testing.T) {
	h := newHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		resp, err := h.request(ctx, t, http.MethodPost, route, chat(fakeprovider.ModelHang, false), authed())
		if resp != nil {
			resp.Body.Close()
		}
		errc <- err
	}()
	testutil.Eventually(t, 5*time.Second, "provider to receive the request", func() bool { return h.up.InFlight() == 1 })
	cancel()
	if err := <-errc; err == nil {
		t.Fatal("expected the client call to fail")
	}
	if !h.up.WaitCancelled(2 * time.Second) {
		t.Fatal("the provider request was not cancelled")
	}
	h.awaitOutcome(t, "client_cancelled")
}

func TestMaxRequestDuration(t *testing.T) {
	t.Run("before_headers", func(t *testing.T) {
		h := newHarness(t, func(o *gateway.Options) { o.MaxRequestDuration = 200 * time.Millisecond })
		resp, body := h.call(t, chat(fakeprovider.ModelHang, false))
		if resp.StatusCode != 504 || resp.Header.Get("X-Pandokos-Error") != "upstream_timeout" || !bytes.Contains(body, []byte(`"code":"upstream_timeout"`)) {
			t.Fatalf("got %d %s", resp.StatusCode, body)
		}
		if !h.up.WaitCancelled(2 * time.Second) {
			t.Fatal("provider not cancelled")
		}
		line := h.awaitOutcome(t, "pandokos_error")
		if line["error_code"] != "upstream_timeout" || line["status"] != float64(504) {
			t.Errorf("line: %v", line)
		}
	})
	t.Run("mid_stream", func(t *testing.T) {
		h := newHarness(t, func(o *gateway.Options) { o.MaxRequestDuration = 300 * time.Millisecond })
		resp, err := h.request(ctx5(t), t, http.MethodPost, route, chat(fakeprovider.ModelGatedStream, true), authed())
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		br := bufio.NewReader(resp.Body)
		if _, err := testutil.ReadEvent(br); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadAll(br); err == nil {
			t.Fatal("the stream must be cut, not ended cleanly")
		}
		if !h.up.WaitCancelled(2 * time.Second) {
			t.Fatal("provider not cancelled")
		}
		h.awaitOutcome(t, "timeout")
	})
}

func TestNothingLeaksAfterCancel(t *testing.T) {
	guard := testutil.TestGuard(t)
	h := newHarnessWith(t, fakeprovider.New(t), guard, nil)
	client := &http.Client{Transport: &http.Transport{}}
	ct := client.Transport.(*http.Transport)
	do := func(model string, stream bool, cancelAfterFirst bool) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.srv.URL+route, bytes.NewReader(chat(model, stream)))
		req.Header = authed()
		resp, err := client.Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		if cancelAfterFirst {
			if _, err := testutil.ReadEvent(bufio.NewReader(resp.Body)); err != nil {
				t.Error(err)
			}
			cancel()
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	do("fake-basic", false, false) // warm-up
	baseline := runtime.NumGoroutine()
	for i := 0; i < 50; i++ {
		do(fakeprovider.ModelGatedStream, true, true)
	}
	testutil.Eventually(t, 5*time.Second, "provider handlers to finish", func() bool { return h.up.InFlight() == 0 })
	testutil.Eventually(t, 5*time.Second, "goroutines to return to the baseline", func() bool {
		ct.CloseIdleConnections()
		guard.CloseIdleConnections()
		h.srv.Client().CloseIdleConnections()
		return runtime.NumGoroutine() <= baseline+3
	})
}

func TestUnauthenticated(t *testing.T) {
	h := newHarness(t, nil)
	cases := map[string]http.Header{
		"no header":    {},
		"basic":        {"Authorization": {"Basic " + testKey}},
		"wrong token":  {"Authorization": {"Bearer wrong-token"}},
		"provider key": {"Authorization": {"Bearer " + fixtures.CanaryProviderKey}},
		"bare token":   {"Authorization": {testKey}},
	}
	type shape struct {
		status      int
		body, code  string
		contentType string
	}
	var first *shape
	for name, hdr := range cases {
		resp, err := h.request(ctx5(t), t, http.MethodPost, route, chat("fake-basic", false), hdr)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		got := shape{resp.StatusCode, string(b), resp.Header.Get("X-Pandokos-Error"), resp.Header.Get("Content-Type")}
		if got.status != 401 || got.code != "unauthenticated" {
			t.Errorf("%s: %+v", name, got)
		}
		if first == nil {
			first = &got
		} else if *first != got {
			t.Errorf("%s: response differs from the others: %+v vs %+v", name, got, *first)
		}
	}
	if n := len(h.up.Requests()); n != 0 {
		t.Fatalf("provider contacted %d times", n)
	}
}

func TestCredentialSwap(t *testing.T) {
	h := newHarness(t, nil)
	h.call(t, chat("fake-basic", false))
	reqs := h.up.Requests()
	if len(reqs) != 1 {
		t.Fatal("no request")
	}
	if got := reqs[0].Header.Values("Authorization"); len(got) != 1 || got[0] != "Bearer "+fixtures.CanaryProviderKey {
		t.Errorf("provider saw %v", got)
	}
	for k, v := range reqs[0].Header {
		if strings.Contains(strings.Join(v, ","), testKey) {
			t.Errorf("the access token reached the provider in %s", k)
		}
	}
}

func TestResponseHeaderAllowList(t *testing.T) {
	h := newHarness(t, nil)
	resp, _ := h.call(t, chat("fake-basic", false))
	for _, n := range []string{"Set-Cookie", "Server", "X-Pandokos-Error"} {
		if v := resp.Header.Values(n); len(v) != 0 {
			t.Errorf("%s reached the caller: %v", n, v)
		}
	}
	for n, want := range map[string]string{"X-Ratelimit-Remaining-Requests": "99", "Openai-Processing-Ms": "1", "X-Request-Id": "req_fake_0001"} {
		if resp.Header.Get(n) != want {
			t.Errorf("%s = %q, want %q", n, resp.Header.Get(n), want)
		}
	}
	if resp.Header.Get("X-Pandokos-Request-Id") == "" {
		t.Error("request id header missing")
	}
	resp, _ = h.call(t, chat("fake-error-429", false))
	if resp.Header.Get("Retry-After") != "7" {
		t.Errorf("Retry-After = %q", resp.Header.Get("Retry-After"))
	}
}

func TestQueryNotForwarded(t *testing.T) {
	h := newHarness(t, nil)
	resp, err := h.request(ctx5(t), t, http.MethodPost, route+"?a=b&token=zzz", chat("fake-basic", false), authed())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	reqs := h.up.Requests()
	if len(reqs) != 1 || reqs[0].RawQuery != "" || reqs[0].Path != "/v1/chat/completions" {
		t.Fatalf("provider saw %+v", reqs)
	}
	if strings.Contains(h.logs.String(), "zzz") {
		t.Error("the query was logged")
	}
}

func TestEndpointNotSupported(t *testing.T) {
	h := newHarness(t, nil)
	cases := []struct{ method, path string }{
		{"GET", route},
		{"POST", route + "/"},
		{"POST", "/openai/v1/models"},
		{"GET", "/openai/v1/embeddings"},
		{"GET", "/openai/v1/responses"},
		{"POST", "/openai/v1/embeddings/"},
		{"POST", "/openai/v1/responses/"},
		{"POST", "/v1/chat/completions"},
		{"POST", "/"},
		{"POST", "//openai/v1/chat/completions"},
		{"POST", "/healthz"},
		{"POST", "/openai/v1"},
		{"POST", "/openai/chat/completions"},
	}
	want := `{"error":{"message":"Pandokos: this endpoint is not supported.","type":"pandokos_error","param":null,"code":"endpoint_not_supported"}}`
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			req, _ := http.NewRequestWithContext(ctx5(t), c.method, "http://"+h.srv.Listener.Addr().String(), strings.NewReader("{}"))
			req.URL = &url.URL{Scheme: "http", Host: h.srv.Listener.Addr().String(), Path: c.path}
			req.Header = authed()
			resp, err := h.srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 404 || string(b) != want || resp.Header.Get("X-Pandokos-Error") != "endpoint_not_supported" {
				t.Fatalf("got %d %s", resp.StatusCode, b)
			}
		})
	}
	if n := len(h.up.Requests()); n != 0 {
		t.Errorf("provider contacted %d times", n)
	}
	if n := h.cat.Resolves.Load(); n != 0 {
		t.Errorf("the model was resolved %d times for a path that is not served", n)
	}
	for _, l := range h.requestLines(t, len(cases)) {
		if l["route"] != "unmatched" {
			t.Errorf("route logged as %v", l["route"])
		}
	}
}

func TestRequestTooLarge(t *testing.T) {
	h := newHarness(t, func(o *gateway.Options) { o.MaxRequestBodyBytes = 100 })
	body := bytes.Repeat([]byte("a"), 200)
	check := func(t *testing.T, resp *http.Response, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 413 || resp.Header.Get("X-Pandokos-Error") != "request_too_large" || !bytes.Contains(b, []byte(`"code":"request_too_large"`)) {
			t.Fatalf("got %d %s", resp.StatusCode, b)
		}
	}
	t.Run("declared length", func(t *testing.T) {
		resp, err := h.request(ctx5(t), t, http.MethodPost, route, body, authed())
		check(t, resp, err)
	})
	t.Run("chunked", func(t *testing.T) {
		req, _ := http.NewRequestWithContext(ctx5(t), http.MethodPost, h.srv.URL+route, io.NopCloser(bytes.NewReader(body)))
		req.Header = authed()
		if req.ContentLength != 0 && req.ContentLength != -1 {
			t.Fatalf("content length %d", req.ContentLength)
		}
		resp, err := h.srv.Client().Do(req)
		check(t, resp, err)
	})
	if n := len(h.up.Requests()); n != 0 {
		t.Fatalf("provider contacted %d times", n)
	}
}

func TestProviderErrorPassesThrough(t *testing.T) {
	h := newHarness(t, nil)
	resp, body := h.call(t, chat("fake-error-429", false))
	if resp.StatusCode != 429 || !bytes.Equal(body, fixtures.Read("error_429.json")) || resp.Header.Get("Retry-After") != "7" {
		t.Fatalf("got %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Pandokos-Error") != "" {
		t.Error("a provider error carries X-Pandokos-Error")
	}
	line := h.lastRequestLine(t)
	if line["outcome"] != "ok" || line["upstream_status"] != float64(429) || line["usage_source"] != "none" {
		t.Errorf("line: %v", line)
	}
}

func TestRequestID(t *testing.T) {
	h := newHarness(t, nil)
	hexID := regexp.MustCompile(`^[0-9a-f]{32}$`)
	seen := map[string]bool{}
	send := func(body []byte, hdr http.Header) string {
		hdr.Set("X-Pandokos-Request-Id", "caller-chosen")
		resp, err := h.request(ctx5(t), t, http.MethodPost, route, body, hdr)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		id := resp.Header.Get("X-Pandokos-Request-Id")
		if !hexID.MatchString(id) || seen[id] {
			t.Fatalf("bad or repeated request id %q", id)
		}
		seen[id] = true
		return id
	}
	ids := []string{
		send(chat("fake-basic", false), authed()),
		send(chat("fake-basic", false), http.Header{}),
		send(chat("fake-error-429", false), authed()),
	}
	lines := h.requestLines(t, 3)
	logged := map[string]bool{}
	for _, l := range lines {
		logged[l["request_id"].(string)] = true
	}
	for _, id := range ids {
		if !logged[id] {
			t.Errorf("id %s not in the request log", id)
		}
	}
}

type panicTransport struct{}

func (panicTransport) RoundTrip(*http.Request) (*http.Response, error) {
	panic("boom " + fixtures.CanaryPrompt)
}

func TestPanicRecovered(t *testing.T) {
	h := newHarness(t, func(o *gateway.Options) { o.Private = panicTransport{} })
	resp, body := h.call(t, chat("fake-basic", false))
	if resp.StatusCode != 500 || resp.Header.Get("X-Pandokos-Error") != "internal" || !bytes.Contains(body, []byte(`"code":"internal"`)) {
		t.Fatalf("got %d %s", resp.StatusCode, body)
	}
	testutil.Eventually(t, 5*time.Second, "panic log line", func() bool { return len(h.logs.Find(t, "panic recovered")) == 1 })
	l := h.logs.Find(t, "panic recovered")[0]
	if l["panic_type"] != "string" || l["request_id"] == nil || l["level"] != "ERROR" {
		t.Errorf("line: %v", l)
	}
	testutil.AssertNoCanary(t, "logs", h.logs.String())
	line := h.awaitOutcome(t, "pandokos_error")
	if line["error_code"] != "internal" || line["status"] != float64(500) {
		t.Errorf("line: %v", line)
	}
}

func TestInformationalResponsesAreDropped(t *testing.T) {
	h := newRaw(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Set-Cookie", "early=1")
		w.Header().Set("X-Pandokos-Error", "spoofed")
		w.Header().Set("Link", "</x>; rel=preload")
		w.WriteHeader(http.StatusEarlyHints)
		w.Header().Del("Set-Cookie")
		w.Header().Del("X-Pandokos-Error")
		w.Header().Del("Link")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	var got1xx []int
	var seen http.Header
	ctx := httptrace.WithClientTrace(ctx5(t), &httptrace.ClientTrace{
		Got1xxResponse: func(code int, hdr textproto.MIMEHeader) error {
			got1xx = append(got1xx, code)
			seen = http.Header(hdr)
			return nil
		},
	})
	resp, err := h.request(ctx, t, http.MethodPost, route, chat("fake-basic", false), authed())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if len(got1xx) != 0 {
		t.Errorf("an informational response reached the caller: %v %v", got1xx, seen)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, n := range []string{"Set-Cookie", "X-Pandokos-Error", "Link"} {
		if v := resp.Header.Values(n); len(v) != 0 {
			t.Errorf("%s reached the caller: %v", n, v)
		}
	}
	if id := resp.Header.Get("X-Pandokos-Request-Id"); len(id) != 32 {
		t.Errorf("request id missing on the final response: %q", id)
	}
}

func TestProviderTrailersAreDropped(t *testing.T) {
	for _, mode := range []string{"json", "stream", "error"} {
		t.Run(mode, func(t *testing.T) {
			h := newRaw(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Trailer", "X-Pandokos-Error, Set-Cookie")
				status := 200
				switch mode {
				case "json":
					w.Header().Set("Content-Type", "application/json")
				case "stream":
					w.Header().Set("Content-Type", "text/event-stream")
				case "error":
					w.Header().Set("Content-Type", "text/plain")
					status = 500
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
				w.Header().Set("X-Pandokos-Error", "spoofed")
				w.Header().Set("Set-Cookie", "late=1")
			})
			resp, err := h.request(ctx5(t), t, http.MethodPost, route, chat("fake-basic", mode == "stream"), authed())
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if _, err := io.Copy(io.Discard, resp.Body); err != nil {
				t.Fatal(err)
			}
			if len(resp.Trailer) != 0 {
				t.Errorf("trailers reached the caller: %v", resp.Trailer)
			}
			for _, n := range []string{"X-Pandokos-Error", "Set-Cookie", "Trailer"} {
				if v := resp.Header.Values(n); len(v) != 0 {
					t.Errorf("%s reached the caller: %v", n, v)
				}
			}
		})
	}
}

// fixedAuth is an Authenticator that returns a given principal or error.
type fixedAuth struct {
	p   authz.Principal
	err error
}

func (f fixedAuth) Authenticate(context.Context, string) (authz.Principal, error) { return f.p, f.err }

// reasoned is an authentication failure that says why, as authn.Failure does.
type reasoned struct{ reason, keyID, tenantID string }

func (r reasoned) Error() string                    { return "failure: " + r.reason }
func (r reasoned) Unwrap() error                    { return authz.ErrUnauthenticated }
func (r reasoned) Detail() (string, string, string) { return r.reason, r.keyID, r.tenantID }

// TestAuthorization is T-267.
func TestAuthorization(t *testing.T) {
	active := testutil.InferencePrincipal()
	noRole := active
	noRole.Roles = nil
	scoped := active
	scoped.Roles = []string{authz.RoleTenantAdmin}
	scoped.Scopes = []string{authz.VirtualKeyRead}
	invokeOnly := active
	invokeOnly.Scopes = []string{authz.ModelInvoke}
	useOnly := active
	useOnly.Scopes = []string{authz.CredentialUse}
	suspended := active
	suspended.TenantState = "suspended"
	cases := []struct {
		name    string
		auth    fixedAuth
		status  int
		code    string
		message string
	}{
		{"no role", fixedAuth{p: noRole}, 403, "forbidden", "Pandokos: the credential does not allow this operation."},
		{"scopes exclude the permission", fixedAuth{p: scoped}, 403, "forbidden", "Pandokos: the credential does not allow this operation."},
		{"a key limited to model:invoke", fixedAuth{p: invokeOnly}, 403, "forbidden", "Pandokos: the credential does not allow this operation."},
		{"a key limited to credential:use", fixedAuth{p: useOnly}, 403, "forbidden", "Pandokos: the credential does not allow this operation."},
		{"suspended tenant", fixedAuth{p: suspended}, 403, "tenant_suspended", "Pandokos: the tenant is not active."},
		{"unavailable", fixedAuth{err: fmt.Errorf("%w: 08006", authz.ErrUnavailable)}, 503, "unavailable", "Pandokos: the request cannot be verified right now."},
		{"unauthenticated", fixedAuth{err: authz.ErrUnauthenticated}, 401, "unauthenticated", "Pandokos: missing or invalid credential."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, func(o *gateway.Options) { o.Authenticator = c.auth })
			resp, err := h.request(ctx5(t), t, http.MethodPost, route, chat("fake-basic", false), authed())
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != c.status || resp.Header.Get("X-Pandokos-Error") != c.code {
				t.Errorf("status %d, error %q: %s", resp.StatusCode, resp.Header.Get("X-Pandokos-Error"), b)
			}
			var body struct {
				Error struct {
					Message string `json:"message"`
					Type    string `json:"type"`
					Code    string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(b, &body); err != nil || body.Error.Message != c.message {
				t.Errorf("body %s (%v), want the message %q in the OpenAI shape", b, err, c.message)
			}
			if n := len(h.up.Requests()); n != 0 {
				t.Errorf("the provider was contacted %d times", n)
			}
			line := h.lastRequestLine(t)
			if line["error_code"] != c.code || line["outcome"] != "pandokos_error" || line["status"] != float64(c.status) {
				t.Errorf("request line: %v", line)
			}
			if c.auth.p.ID != "" && line["tenant_id"] != c.auth.p.TenantID.String() {
				t.Errorf("a request refused by the decision names its tenant: %v", line["tenant_id"])
			}
		})
	}

	t.Run("a good principal is forwarded and logged", func(t *testing.T) {
		h := newHarness(t, func(o *gateway.Options) { o.Authenticator = fixedAuth{p: active} })
		resp, b := h.call(t, chat("fake-basic", false))
		if resp.StatusCode != 200 || len(b) == 0 || len(h.up.Requests()) != 1 {
			t.Fatalf("status %d", resp.StatusCode)
		}
		line := h.lastRequestLine(t)
		if line["tenant_id"] != active.TenantID.String() || line["service_account_id"] != active.ID || line["key_id"] != active.KeyID {
			t.Errorf("identity fields: %v", line)
		}
	})

	t.Run("authentication failed is logged with the reason", func(t *testing.T) {
		const keyID, tenantID = "tabcdefghijklmnop", "0192f0f0-0000-7000-8000-000000000001"
		h := newHarness(t, func(o *gateway.Options) { o.Authenticator = fixedAuth{err: reasoned{"wrong_secret", keyID, tenantID}} })
		resp, err := h.request(ctx5(t), t, http.MethodPost, route, chat("fake-basic", false), authed())
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("status %d", resp.StatusCode)
		}
		lines := h.logs.Find(t, "authentication failed")
		if len(lines) != 1 {
			t.Fatalf("%d lines", len(lines))
		}
		l := lines[0]
		if l["level"] != "WARN" || l["listener"] != "inference" || l["reason"] != "wrong_secret" || l["key_id"] != keyID || l["tenant_id"] != tenantID || len(fmt.Sprint(l["request_id"])) != 32 {
			t.Errorf("the line: %v", l)
		}
		if _, ok := l["audited"]; ok {
			t.Error("the inference listener does not audit: no audited field")
		}
		testutil.AssertAbsent(t, "logs", h.logs.String(), testKey)
		req := h.lastRequestLine(t)
		if req["key_id"] != keyID {
			t.Errorf("a failed authentication names the key ID in the request line: %v", req["key_id"])
		}
	})

	t.Run("a missing or malformed header never reaches the authenticator", func(t *testing.T) {
		called := 0
		h := newHarness(t, func(o *gateway.Options) { o.Authenticator = countingAuth{n: &called} })
		hdrs := []http.Header{{}, {"Authorization": {"Basic abc"}}, {"Authorization": {"Bearer"}}}
		defer func() {
			lines := h.logs.Find(t, "authentication failed")
			if len(lines) != len(hdrs) {
				t.Errorf("%d authentication failed lines, want %d", len(lines), len(hdrs))
			}
			for _, l := range lines {
				if l["level"] != "WARN" || l["listener"] != "inference" || l["reason"] != "malformed" || len(fmt.Sprint(l["request_id"])) != 32 {
					t.Errorf("the line: %v", l)
				}
			}
		}()
		for _, hdr := range hdrs {
			resp, err := h.request(ctx5(t), t, http.MethodPost, route, chat("fake-basic", false), hdr)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != 401 {
				t.Errorf("%v: %d", hdr, resp.StatusCode)
			}
		}
		if called != 0 {
			t.Errorf("the authenticator was called %d times", called)
		}
	})

	t.Run("an unknown path is 404 before authentication", func(t *testing.T) {
		called := 0
		h := newHarness(t, func(o *gateway.Options) { o.Authenticator = countingAuth{n: &called} })
		resp, err := h.request(ctx5(t), t, http.MethodPost, "/openai/v1/nope", nil, authed())
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 || called != 0 {
			t.Errorf("status %d, authenticator called %d times", resp.StatusCode, called)
		}
	})
}

type countingAuth struct{ n *int }

func (c countingAuth) Authenticate(context.Context, string) (authz.Principal, error) {
	*c.n++
	return authz.Principal{}, authz.ErrUnauthenticated
}

// TestNewValidatesOptions is the validation of the options of sprint 3.1, section 5.4.
func TestNewValidatesOptions(t *testing.T) {
	logs := &testutil.LogBuffer{}
	cat := testutil.StaticCatalogue(map[string]string{"openai": "openai"}, nil)
	creds := testutil.StaticCredentials(nil)
	good := func() gateway.Options {
		return gateway.Options{
			Logger: telemetry.NewLogger(logs, "info"), Authenticator: testAuth, Catalogue: cat, Credentials: creds,
			Families: map[string]family.Adapter{"openai": openai.Adapter()}, DefaultFamily: "openai",
			Public: http.DefaultTransport, Private: http.DefaultTransport,
			MaxRequestBodyBytes: 1, RequestBodyReadTimeout: 1, MaxRequestDuration: 1,
			Recorder: &testutil.MemoryRecorder{}, Tracer: noopTracer(),
		}
	}
	if _, err := gateway.New(good()); err != nil {
		t.Fatal(err)
	}
	o := good()
	o.Failures = &failureRecorder{}
	if _, err := gateway.New(o); err != nil {
		t.Fatalf("Failures is optional and may be set: %v", err)
	}
	for name, mutate := range map[string]func(*gateway.Options){
		"logger":                func(o *gateway.Options) { o.Logger = nil },
		"authenticator":         func(o *gateway.Options) { o.Authenticator = nil },
		"catalogue":             func(o *gateway.Options) { o.Catalogue = nil },
		"credentials":           func(o *gateway.Options) { o.Credentials = nil },
		"families":              func(o *gateway.Options) { o.Families = nil },
		"no family":             func(o *gateway.Options) { o.Families = map[string]family.Adapter{} },
		"a nil adapter":         func(o *gateway.Options) { o.Families = map[string]family.Adapter{"openai": nil} },
		"default family":        func(o *gateway.Options) { o.DefaultFamily = "" },
		"unknown default":       func(o *gateway.Options) { o.DefaultFamily = "anthropic" },
		"public transport":      func(o *gateway.Options) { o.Public = nil },
		"private transport":     func(o *gateway.Options) { o.Private = nil },
		"body":                  func(o *gateway.Options) { o.MaxRequestBodyBytes = 0 },
		"read":                  func(o *gateway.Options) { o.RequestBodyReadTimeout = 0 },
		"duration":              func(o *gateway.Options) { o.MaxRequestDuration = 0 },
		"recorder":              func(o *gateway.Options) { o.Recorder = nil },
		"tracer":                func(o *gateway.Options) { o.Tracer = nil },
		"a negative body limit": func(o *gateway.Options) { o.MaxRequestBodyBytes = -1 },
	} {
		o := good()
		mutate(&o)
		if _, err := gateway.New(o); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// TestUpstreamUnreachable is T-46: a closed port on a private target.
func TestUpstreamUnreachable(t *testing.T) {
	h := newHarness(t, nil)
	closed, _ := url.Parse("http://127.0.0.1:1")
	tgt := h.targets["openai/fake-basic"]
	tgt.BaseURL = closed
	h.targets["openai/fake-basic"] = tgt
	resp, body := h.call(t, chat("fake-basic", false))
	want := `{"error":{"message":"Pandokos: the provider could not be reached.","type":"pandokos_error","param":null,"code":"upstream_unreachable"}}`
	if resp.StatusCode != 502 || string(body) != want || resp.Header.Get("X-Pandokos-Error") != "upstream_unreachable" {
		t.Fatalf("got %d %s", resp.StatusCode, body)
	}
	for k, v := range resp.Header {
		if strings.Contains(strings.Join(v, ","), "127.0.0.1") {
			t.Errorf("header %s contains the address", k)
		}
	}
	line := h.lastRequestLine(t)
	if line["outcome"] != "pandokos_error" || line["error_code"] != "upstream_unreachable" || line["upstream_error"] != "connect" {
		t.Errorf("line: %v", line)
	}
	if strings.Contains(h.logs.String(), "127.0.0.1:1") {
		t.Error("the address was logged")
	}
}

// TestRouteDeclaresAPermission is T-274: the routes of the gateway name both permissions.
func TestRouteDeclaresAPermission(t *testing.T) {
	routes := gateway.Routes()
	want := map[string]bool{}
	for _, op := range openai.Adapter().Operations() {
		want[op.Method+" /{provider}"+op.Path] = false
	}
	if len(want) == 0 {
		t.Fatal("the openai adapter has no operation")
	}
	for _, r := range routes {
		key := r.Method + " " + r.Path
		if _, ok := want[key]; !ok {
			t.Errorf("a route that no operation of an adapter explains: %+v", r)
			continue
		}
		want[key] = true
		perms := append([]string(nil), r.Permissions...)
		sort.Strings(perms)
		if strings.Join(perms, ",") != authz.CredentialUse+","+authz.ModelInvoke {
			t.Errorf("%s: permissions %v, want model:invoke and credential:use", key, r.Permissions)
		}
		for _, p := range r.Permissions {
			if _, ok := authz.Lookup(p); !ok {
				t.Errorf("%s: the permission %q is not in the catalogue", key, p)
			}
		}
	}
	for key, seen := range want {
		if !seen {
			t.Errorf("the operation %s has no route", key)
		}
	}
	if !want["POST /{provider}/v1/chat/completions"] {
		t.Error("POST /{provider}/v1/chat/completions is not a route")
	}
}

// failureRecorder is a gateway.FailureRecorder that stores its calls and answers as told.
type failureRecorder struct {
	mu     sync.Mutex
	calls  []gateway.AuthFailure
	answer bool
}

func (f *failureRecorder) Record(_ context.Context, af gateway.AuthFailure) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, af)
	return f.answer
}

func (f *failureRecorder) recorded() []gateway.AuthFailure {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gateway.AuthFailure(nil), f.calls...)
}

// noopTracer returns a tracer provider that records nothing, for options that need one.
func noopTracer() trace.TracerProvider { return tracenoop.NewTracerProvider() }
