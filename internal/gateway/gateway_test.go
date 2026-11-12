package gateway

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

const route = "/openai/v1/chat/completions"

type harness struct {
	up   *fakeprovider.Server
	srv  *httptest.Server // serves the gateway.Handler
	logs *testutil.LogBuffer
}

func newHarness(t *testing.T, mutate func(*Options)) *harness {
	t.Helper()
	return newHarnessWith(t, fakeprovider.New(t), nil, mutate)
}

func newHarnessWith(t *testing.T, up *fakeprovider.Server, transport http.RoundTripper, mutate func(*Options)) *harness {
	t.Helper()
	base, err := url.Parse(up.BaseURL())
	if err != nil {
		t.Fatal(err)
	}
	if transport == nil {
		tr := NewTransport(2 * time.Second)
		t.Cleanup(tr.CloseIdleConnections)
		transport = tr
	}
	logs := &testutil.LogBuffer{}
	o := Options{
		Logger:                 telemetry.NewLogger(logs, "debug"),
		BaseURL:                base,
		ProviderKey:            secret.New(fixtures.CanaryProviderKey),
		AccessToken:            secret.New(fixtures.CanaryAccessToken),
		MaxRequestBodyBytes:    1 << 20,
		RequestBodyReadTimeout: 2 * time.Second,
		MaxRequestDuration:     10 * time.Second,
		Transport:              transport,
	}
	if mutate != nil {
		mutate(&o)
	}
	h, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &harness{up: up, srv: srv, logs: logs}
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
	return http.Header{"Authorization": {"Bearer " + fixtures.CanaryAccessToken}, "Content-Type": {"application/json"}}
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

func usageGroup(t *testing.T, line map[string]any) map[string]any {
	t.Helper()
	g, ok := line["usage"].(map[string]any)
	if !ok {
		t.Fatalf("no usage group in %v", line)
	}
	return g
}

func TestNewValidatesOptions(t *testing.T) {
	good := func() Options {
		base, _ := url.Parse("http://127.0.0.1:1/v1")
		return Options{
			Logger: telemetry.NewLogger(io.Discard, "info"), BaseURL: base,
			ProviderKey: secret.New("k"), AccessToken: secret.New("t"),
			MaxRequestBodyBytes: 1, RequestBodyReadTimeout: 1, MaxRequestDuration: 1,
			Transport: http.DefaultTransport,
		}
	}
	if _, err := New(good()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Options){
		"logger":    func(o *Options) { o.Logger = nil },
		"base url":  func(o *Options) { o.BaseURL = nil },
		"key":       func(o *Options) { o.ProviderKey = secret.Secret{} },
		"token":     func(o *Options) { o.AccessToken = secret.Secret{} },
		"transport": func(o *Options) { o.Transport = nil },
		"body":      func(o *Options) { o.MaxRequestBodyBytes = 0 },
		"read":      func(o *Options) { o.RequestBodyReadTimeout = 0 },
		"duration":  func(o *Options) { o.MaxRequestDuration = 0 },
	} {
		o := good()
		mutate(&o)
		if _, err := New(o); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
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
		h := newHarnessWith(t, up, up.Transport(), nil)
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
		h := newHarness(t, func(o *Options) { o.MaxRequestDuration = 200 * time.Millisecond })
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
		h := newHarness(t, func(o *Options) { o.MaxRequestDuration = 300 * time.Millisecond })
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

func TestSlowRequestBody(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.RequestBodyReadTimeout = 200 * time.Millisecond })
	conn, err := net.Dial("tcp", h.srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"model\":", route, fixtures.CanaryAccessToken)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, err = io.ReadAll(conn)
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatal("the server did not close the connection within 3 s")
	}
	if n := len(h.up.Requests()); n != 0 {
		t.Fatalf("provider contacted %d times", n)
	}
	if line := h.awaitOutcome(t, "client_error"); line["status"] != float64(0) {
		t.Errorf("line: %v", line)
	}
}

func TestNothingLeaksAfterCancel(t *testing.T) {
	tr := NewTransport(2 * time.Second)
	h := newHarnessWith(t, fakeprovider.New(t), tr, nil)
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
		tr.CloseIdleConnections()
		h.srv.Client().CloseIdleConnections()
		return runtime.NumGoroutine() <= baseline+3
	})
}

func TestUpstreamUnreachable(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.BaseURL, _ = url.Parse("http://127.0.0.1:1/v1") })
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
	if line["outcome"] != "pandokos_error" || line["error_code"] != "upstream_unreachable" {
		t.Errorf("line: %v", line)
	}
	if strings.Contains(h.logs.String(), "127.0.0.1:1") {
		t.Error("the address was logged")
	}
}

func TestUnauthenticated(t *testing.T) {
	h := newHarness(t, nil)
	cases := map[string]http.Header{
		"no header":    {},
		"basic":        {"Authorization": {"Basic " + fixtures.CanaryAccessToken}},
		"wrong token":  {"Authorization": {"Bearer wrong-token"}},
		"provider key": {"Authorization": {"Bearer " + fixtures.CanaryProviderKey}},
		"bare token":   {"Authorization": {fixtures.CanaryAccessToken}},
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
		if strings.Contains(strings.Join(v, ","), fixtures.CanaryAccessToken) {
			t.Errorf("the access token reached the provider in %s", k)
		}
	}
}

func TestRequestHeaderAllowList(t *testing.T) {
	h := newHarness(t, nil)
	hdr := authed()
	hdr.Set("Accept", "text/event-stream")
	hdr.Set("User-Agent", "pandokos-test/1.0")
	hdr.Set("Cookie", "session=abc")
	hdr.Set("X-Api-Key", "k")
	hdr.Set("X-Forwarded-For", "1.2.3.4")
	hdr.Set("Accept-Encoding", "gzip")
	hdr.Set("OpenAI-Organization", "org-x")
	hdr.Set("X-Custom", "v")
	resp, err := h.request(ctx5(t), t, http.MethodPost, route, chat("fake-basic", false), hdr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	got := h.up.Requests()[0].Header
	for _, n := range []string{"Cookie", "X-Api-Key", "X-Forwarded-For", "Accept-Encoding", "Openai-Organization", "X-Custom", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		if v := got.Values(n); len(v) != 0 {
			t.Errorf("%s reached the provider: %v", n, v)
		}
	}
	for n, want := range map[string]string{"Accept": "text/event-stream", "Content-Type": "application/json", "User-Agent": "pandokos-test/1.0"} {
		if got.Get(n) != want {
			t.Errorf("%s = %q, want %q", n, got.Get(n), want)
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
		{"POST", "/openai/v1/embeddings"},
		{"POST", "/v1/chat/completions"},
		{"POST", "/"},
		{"POST", "//openai/v1/chat/completions"},
		{"POST", "/healthz"},
		{"POST", "/openai/v1"},
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
	for _, l := range h.requestLines(t, len(cases)) {
		if l["route"] != "unmatched" {
			t.Errorf("route logged as %v", l["route"])
		}
	}
}

func TestRequestTooLarge(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.MaxRequestBodyBytes = 100 })
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
	h := newHarnessWith(t, fakeprovider.New(t), panicTransport{}, nil)
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

func TestRequestLogFields(t *testing.T) {
	h := newHarness(t, nil)
	hdr := authed()
	hdr.Set("Cookie", "session=cookie-value")
	resp, err := h.request(ctx5(t), t, http.MethodPost, route+"?q=query-value", chat("fake-basic", true), hdr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	line := h.lastRequestLine(t)
	want := []string{
		"time", "level", "msg", "request_id", "method", "route", "status", "outcome", "duration_ms", "provider",
		"operation", "stream", "usage_injected", "upstream_status", "ttfb_ms", "request_bytes", "response_bytes",
		"usage_source", "usage",
	}
	var got []string
	for k := range line {
		got = append(got, k)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("fields\n got %v\nwant %v", got, want)
	}
	strs := map[string]string{"method": "POST", "route": "POST " + route, "outcome": "ok", "provider": "openai", "operation": "chat.completions", "usage_source": "provider", "level": "INFO", "msg": "request"}
	for k, v := range strs {
		if line[k] != v {
			t.Errorf("%s = %v, want %q", k, line[k], v)
		}
	}
	for _, k := range []string{"status", "duration_ms", "upstream_status", "ttfb_ms", "request_bytes", "response_bytes"} {
		if _, ok := line[k].(float64); !ok {
			t.Errorf("%s is %T, want a number", k, line[k])
		}
	}
	if line["status"] != float64(200) || line["upstream_status"] != float64(200) {
		t.Errorf("status fields: %v %v", line["status"], line["upstream_status"])
	}
	if line["stream"] != true || line["usage_injected"] != true {
		t.Errorf("booleans: %v %v", line["stream"], line["usage_injected"])
	}
	if len(line["request_id"].(string)) != 32 {
		t.Error("request_id length")
	}
	ug := usageGroup(t, line)
	var ukeys []string
	for k := range ug {
		ukeys = append(ukeys, k)
	}
	sort.Strings(ukeys)
	if strings.Join(ukeys, ",") != "cache_read_tokens,cache_write_tokens,input_tokens,output_tokens,reasoning_tokens" {
		t.Errorf("usage keys: %v", ukeys)
	}
	all := h.logs.String()
	for _, leak := range []string{"query-value", "cookie-value", "Bearer", "stream_options", fixtures.CanaryAccessToken} {
		if strings.Contains(all, leak) {
			t.Errorf("logs contain %q", leak)
		}
	}
	testutil.AssertNoCanary(t, "logs", all)
	if !strings.Contains(all, `"route":"POST /openai/v1/chat/completions"`) {
		t.Error("route missing")
	}
}

// rawUpstream serves handler as the provider and returns a gateway in front of it.
func rawUpstream(t *testing.T, handler http.HandlerFunc) *harness {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	base, _ := url.Parse(ts.URL + "/v1")
	logs := &testutil.LogBuffer{}
	tr := NewTransport(2 * time.Second)
	t.Cleanup(tr.CloseIdleConnections)
	g, err := New(Options{
		Logger: telemetry.NewLogger(logs, "debug"), BaseURL: base,
		ProviderKey: secret.New(fixtures.CanaryProviderKey), AccessToken: secret.New(fixtures.CanaryAccessToken),
		MaxRequestBodyBytes: 1 << 20, RequestBodyReadTimeout: 2 * time.Second, MaxRequestDuration: 10 * time.Second,
		Transport: tr,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return &harness{srv: srv, logs: logs}
}

func TestInformationalResponsesAreDropped(t *testing.T) {
	h := rawUpstream(t, func(w http.ResponseWriter, r *http.Request) {
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
			h := rawUpstream(t, func(w http.ResponseWriter, r *http.Request) {
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
