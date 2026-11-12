package health

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/testutil"
)

func newState(t *testing.T) (*State, *testutil.LogBuffer) {
	t.Helper()
	logs := &testutil.LogBuffer{}
	return New(telemetry.NewLogger(logs, "debug")), logs
}

func get(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func assertResponse(t *testing.T, rec *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status %d, want %d", rec.Code, status)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != body {
		t.Errorf("body %q, want %q", got, body)
	}
	if rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %v", rec.Header())
	}
}

func TestLiveness(t *testing.T) {
	s, _ := newState(t)
	h := s.Handler()
	steps := []func(){func() {}, s.SetStarted, s.SetDraining}
	for i, step := range steps {
		step()
		assertResponse(t, get(h, http.MethodGet, "/healthz"), 200, `{"status":"ok"}`)
		rec := get(h, http.MethodHead, "/healthz")
		if rec.Code != 200 || rec.Body.Len() != 0 {
			t.Errorf("step %d: HEAD returned %d with %d bytes", i, rec.Code, rec.Body.Len())
		}
	}
}

func TestReadiness(t *testing.T) {
	s, _ := newState(t)
	h := s.Handler()
	ctx := context.Background()
	if s.Ready(ctx) {
		t.Error("ready before SetStarted")
	}
	assertResponse(t, get(h, http.MethodGet, "/readyz"), 503, `{"status":"not_ready"}`)
	s.SetStarted()
	if !s.Ready(ctx) {
		t.Error("not ready after SetStarted")
	}
	assertResponse(t, get(h, http.MethodGet, "/readyz"), 200, `{"status":"ready"}`)
	if rec := get(h, http.MethodHead, "/readyz"); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD: %d %d", rec.Code, rec.Body.Len())
	}
	s.SetDraining()
	if s.Ready(ctx) {
		t.Error("ready while draining")
	}
	assertResponse(t, get(h, http.MethodGet, "/readyz"), 503, `{"status":"not_ready"}`)
	s.SetStarted() // draining is irreversible
	if s.Ready(ctx) {
		t.Error("draining was reverted")
	}
}

func TestReadinessReflectsChecks(t *testing.T) {
	s, logs := newState(t)
	var failing = true
	s.AddCheck("database", func(context.Context) error {
		if failing {
			return errors.New("connection refused")
		}
		return nil
	})
	s.SetStarted()
	h := s.Handler()
	rec := get(h, http.MethodGet, "/readyz")
	assertResponse(t, rec, 503, `{"status":"not_ready"}`)
	if strings.Contains(rec.Body.String(), "database") {
		t.Error("the response names the failing check")
	}
	lines := logs.Find(t, "readiness check failed")
	if len(lines) == 0 || lines[0]["check"] != "database" || lines[0]["level"] != "WARN" {
		t.Fatalf("log: %v", lines)
	}
	if !strings.Contains(lines[0]["error"].(string), "connection refused") {
		t.Errorf("log lacks the error: %v", lines[0])
	}
	failing = false
	assertResponse(t, get(h, http.MethodGet, "/readyz"), 200, `{"status":"ready"}`)

	t.Run("a blocking check is cut", func(t *testing.T) {
		s, _ := newState(t)
		s.AddCheck("slow", func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})
		s.SetStarted()
		start := time.Now()
		rec := get(s.Handler(), http.MethodGet, "/readyz")
		if rec.Code != 503 {
			t.Errorf("status %d", rec.Code)
		}
		if time.Since(start) > 3*time.Second {
			t.Errorf("took %s", time.Since(start))
		}
	})
}

func TestOtherMethodsAndPathsAreNotHandled(t *testing.T) {
	s, _ := newState(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	for _, c := range []struct{ method, path string }{{"POST", "/healthz"}, {"GET", "/other"}} {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, nil)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Errorf("%s %s answered 200", c.method, c.path)
		}
	}
}
