package server

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

func testConfig(t *testing.T, up *fakeprovider.Server) *config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.Listeners.Inference.Address = "127.0.0.1:0"
	u, err := url.Parse(up.BaseURL())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Skeleton.OpenAI.BaseURL = up.BaseURL()
	cfg.Skeleton.OpenAI.ParsedBaseURL = u
	cfg.Skeleton.AccessToken = secret.New(fixtures.CanaryAccessToken)
	cfg.Skeleton.OpenAI.APIKey = secret.New(fixtures.CanaryProviderKey)
	return cfg
}

func startServer(t *testing.T, mutate func(*config.Config)) (*Server, *fakeprovider.Server, *testutil.LogBuffer, func(), <-chan error) {
	t.Helper()
	up := fakeprovider.New(t)
	cfg := testConfig(t, up)
	if mutate != nil {
		mutate(cfg)
	}
	logs := &testutil.LogBuffer{}
	srv, err := New(cfg, telemetry.NewLogger(logs, "debug"))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(ctx)
		close(done) // a second receive, in the cleanup, returns at once
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	return srv, up, logs, cancel, done
}

func get(t *testing.T, srv *Server, path string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+srv.InferenceAddr().String()+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func postStream(t *testing.T, srv *Server, model string) (*http.Response, error) {
	t.Helper()
	body := `{"model":"` + model + `","stream":true,"messages":[{"role":"user","content":"` + fixtures.CanaryPrompt + `"}]}`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+srv.InferenceAddr().String()+"/openai/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+fixtures.CanaryAccessToken)
	req.Header.Set("Content-Type", "application/json")
	return (&http.Client{Transport: &http.Transport{}}).Do(req)
}

func TestHealthOnInferenceListener(t *testing.T) {
	srv, _, logs, _, _ := startServer(t, nil)
	testutil.Eventually(t, 5*time.Second, "readiness", func() bool { return get(t, srv, "/readyz") == 200 })
	if got := get(t, srv, "/healthz"); got != 200 {
		t.Errorf("/healthz = %d", got)
	}
	lines := logs.Find(t, "listening")
	if len(lines) != 1 || lines[0]["listener"] != "inference" || lines[0]["address"] != srv.InferenceAddr().String() {
		t.Fatalf("listening line: %v", lines)
	}
	if strings.HasSuffix(srv.InferenceAddr().String(), ":0") {
		t.Error("the logged address must be the bound one")
	}
	if got := get(t, srv, "/nope"); got != 404 {
		t.Errorf("/nope = %d", got)
	}
}

func TestGracefulShutdownDrains(t *testing.T) {
	srv, up, logs, cancel, done := startServer(t, nil)
	testutil.Eventually(t, 5*time.Second, "readiness", func() bool { return get(t, srv, "/readyz") == 200 })
	resp, err := postStream(t, srv, fakeprovider.ModelGatedStream)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	if _, err := testutil.ReadEvent(br); err != nil {
		t.Fatal(err)
	}
	cancel()
	testutil.Eventually(t, 5*time.Second, "the listener to close", func() bool {
		c, err := net.DialTimeout("tcp", srv.InferenceAddr().String(), 200*time.Millisecond)
		if err == nil {
			c.Close()
			return false
		}
		return true
	})
	if srv.Health().Ready(context.Background()) {
		t.Error("readiness must be false while draining")
	}
	up.Release()
	rest, err := io.ReadAll(br)
	if err != nil || !bytes.HasSuffix(rest, []byte("data: [DONE]\n\n")) {
		t.Fatalf("the stream did not complete: %v %q", err, rest)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
	if l := logs.Find(t, "shutdown started"); len(l) != 1 {
		t.Errorf("shutdown started: %v", l)
	}
	l := logs.Find(t, "shutdown complete")
	if len(l) != 1 || l[0]["forced"] != false {
		t.Fatalf("shutdown complete: %v", l)
	}
	if _, ok := l[0]["duration_ms"].(float64); !ok {
		t.Error("duration_ms missing")
	}
}

func TestShutdownDeadlineForces(t *testing.T) {
	srv, up, logs, cancel, done := startServer(t, func(c *config.Config) {
		c.Shutdown.DrainTimeout = config.Duration(200 * time.Millisecond)
	})
	testutil.Eventually(t, 5*time.Second, "readiness", func() bool { return get(t, srv, "/readyz") == 200 })
	resp, err := postStream(t, srv, fakeprovider.ModelGatedStream)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	if _, err := testutil.ReadEvent(br); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return within 3 s")
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Error("Serve returned before the drain deadline")
	}
	if !up.WaitCancelled(2 * time.Second) {
		t.Fatal("the provider request was not cancelled")
	}
	if _, err := io.ReadAll(br); err == nil {
		t.Error("the stream must be cut")
	}
	l := logs.Find(t, "shutdown complete")
	if len(l) != 1 || l[0]["forced"] != true || l[0]["level"] != "WARN" {
		t.Fatalf("shutdown complete: %v", l)
	}
	testutil.Eventually(t, 5*time.Second, "shutdown_aborted outcome", func() bool {
		for _, r := range logs.Find(t, "request") {
			if r["outcome"] == "shutdown_aborted" {
				return true
			}
		}
		return false
	})
}

func TestListenFailure(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	up := fakeprovider.New(t)
	cfg := testConfig(t, up)
	cfg.Listeners.Inference.Address = busy.Addr().String()
	srv, err := New(cfg, telemetry.NewLogger(io.Discard, "info"))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Listen(); err == nil {
		t.Fatal("expected a listen error")
	}
}

func TestHealthRequestLogUsesFixedRoute(t *testing.T) {
	srv, _, logs, _, _ := startServer(t, nil)
	testutil.Eventually(t, 5*time.Second, "readiness", func() bool { return get(t, srv, "/readyz") == 200 })
	for _, c := range []struct{ method, path, route string }{
		{"GET", "/healthz", "GET /healthz"},
		{"HEAD", "/healthz", "GET /healthz"},
		{"HEAD", "/readyz", "GET /readyz"},
	} {
		req, _ := http.NewRequest(c.method, "http://"+srv.InferenceAddr().String()+c.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		found := false
		for _, l := range logs.Find(t, "request") {
			if l["route"] == c.route && l["method"] == c.method && l["level"] == "DEBUG" && l["status"] == float64(200) {
				found = true
			}
			if r, _ := l["route"].(string); r == "HEAD /healthz" || r == "HEAD /readyz" {
				t.Errorf("route label %q is not a fixed label", r)
			}
		}
		if !found {
			t.Errorf("no debug request line for %s %s with route %q", c.method, c.path, c.route)
		}
	}
}
