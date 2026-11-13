package vault_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/test/fixtures"
)

// recorded is what the fake provider saw of one request.
type recorded struct {
	Method, Path, RawQuery string
	Header                 http.Header
	Close                  bool
	ContentLength          int64
}

type models struct {
	srv   *httptest.Server
	mu    sync.Mutex
	reqs  []recorded
	serve func(w http.ResponseWriter, r *http.Request)
}

func newModels(t *testing.T, serve func(w http.ResponseWriter, r *http.Request)) *models {
	t.Helper()
	m := &models{serve: serve}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.reqs = append(m.reqs, recorded{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), r.Close, r.ContentLength})
		m.mu.Unlock()
		m.serve(w, r)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *models) requests() []recorded {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]recorded(nil), m.reqs...)
}

func baseOf(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func status(code int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

// TestOpenAIValidator is T-307.
func TestOpenAIValidator(t *testing.T) {
	key := secret.New(fixtures.CanaryCredential)
	echo := `{"error":{"message":"Incorrect API key provided: ` + fixtures.CanaryCredential + `.","type":"invalid_request_error"}}`

	t.Run("the verdict follows the status", func(t *testing.T) {
		for _, c := range []struct {
			code int
			want vault.Verdict
		}{
			{200, vault.Accepted}, {204, vault.Accepted},
			{401, vault.Rejected}, {403, vault.Rejected},
			{400, vault.Indeterminate}, {404, vault.Indeterminate}, {429, vault.Indeterminate},
			{500, vault.Indeterminate}, {502, vault.Indeterminate}, {503, vault.Indeterminate},
		} {
			m := newModels(t, status(c.code, echo))
			v := vault.NewOpenAIValidator(baseOf(t, m.srv.URL), 2*time.Second)
			got, upstream := v.Validate(t.Context(), key)
			if got != c.want || upstream != c.code {
				t.Errorf("HTTP %d: verdict %v, upstream status %d; want %v, %d", c.code, got, upstream, c.want, c.code)
			}
			if n := len(m.requests()); n != 1 {
				t.Errorf("HTTP %d: %d requests, want exactly one", c.code, n)
			}
			// Nothing a caller receives holds the body, which echoes the key.
			if strings.Contains(fmt.Sprint(got, upstream), fixtures.CanaryCredential) {
				t.Errorf("HTTP %d: the key is in what Validate returns", c.code)
			}
		}
	})

	t.Run("the request is GET models with the bearer and nothing else", func(t *testing.T) {
		m := newModels(t, status(200, `{"object":"list","data":[]}`))
		v := vault.NewOpenAIValidator(baseOf(t, m.srv.URL+"/v1"), 2*time.Second)
		if got, _ := v.Validate(t.Context(), key); got != vault.Accepted {
			t.Fatalf("verdict %v", got)
		}
		reqs := m.requests()
		if len(reqs) != 1 {
			t.Fatalf("%d requests", len(reqs))
		}
		r := reqs[0]
		if r.Method != "GET" || r.Path != "/v1/models" || r.RawQuery != "" || r.ContentLength > 0 {
			t.Errorf("request %s %s?%s with %d bytes of body", r.Method, r.Path, r.RawQuery, r.ContentLength)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+fixtures.CanaryCredential {
			t.Errorf("Authorization header is %q", got)
		}
		allowed := map[string]bool{"Authorization": true, "User-Agent": true, "Accept-Encoding": true}
		for name := range r.Header {
			if !allowed[name] {
				t.Errorf("the request carries the header %s", name)
			}
		}
		if !r.Close {
			t.Error("the connection is not closed after the request: connections are not reused")
		}

		// A base without a path.
		m2 := newModels(t, status(200, `{}`))
		vault.NewOpenAIValidator(baseOf(t, m2.srv.URL), time.Second).Validate(t.Context(), key)
		if reqs := m2.requests(); len(reqs) != 1 || reqs[0].Path != "/models" {
			t.Errorf("a base without a path: %+v", reqs)
		}
		// A base with a trailing slash does not double it.
		m3 := newModels(t, status(200, `{}`))
		vault.NewOpenAIValidator(baseOf(t, m3.srv.URL+"/v1/"), time.Second).Validate(t.Context(), key)
		if reqs := m3.requests(); len(reqs) != 1 || reqs[0].Path != "/v1/models" {
			t.Errorf("a base with a trailing slash: %+v", reqs)
		}
	})

	t.Run("a redirect is not followed", func(t *testing.T) {
		var hits atomic.Int64
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.WriteHeader(200)
		}))
		t.Cleanup(target.Close)
		m := newModels(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/models", http.StatusFound)
		})
		got, upstream := vault.NewOpenAIValidator(baseOf(t, m.srv.URL+"/v1"), 2*time.Second).Validate(t.Context(), key)
		if got != vault.Indeterminate || upstream != 302 {
			t.Errorf("a 302: verdict %v, status %d, want Indeterminate and 302", got, upstream)
		}
		if hits.Load() != 0 {
			t.Error("the redirect was followed, and the bearer sent to another address")
		}
	})

	t.Run("a stall is cut at the timeout", func(t *testing.T) {
		release := make(chan struct{})
		m := newModels(t, func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-release:
			}
		})
		t.Cleanup(func() { close(release) })
		start := time.Now()
		got, upstream := vault.NewOpenAIValidator(baseOf(t, m.srv.URL), 300*time.Millisecond).Validate(t.Context(), key)
		if got != vault.Indeterminate || upstream != 0 {
			t.Errorf("a stall: verdict %v, status %d, want Indeterminate and 0", got, upstream)
		}
		if d := time.Since(start); d < 250*time.Millisecond || d > 5*time.Second {
			t.Errorf("the call took %s with a timeout of 300ms", d)
		}
	})

	t.Run("a cancelled context ends the call", func(t *testing.T) {
		release := make(chan struct{})
		m := newModels(t, func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-release:
			}
		})
		t.Cleanup(func() { close(release) })
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		start := time.Now()
		got, _ := vault.NewOpenAIValidator(baseOf(t, m.srv.URL), 30*time.Second).Validate(ctx, key)
		if got != vault.Indeterminate || time.Since(start) > 5*time.Second {
			t.Errorf("verdict %v after %s", got, time.Since(start))
		}
	})

	t.Run("a refused connection", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		l.Close()
		got, upstream := vault.NewOpenAIValidator(baseOf(t, "http://"+addr), 2*time.Second).Validate(t.Context(), key)
		if got != vault.Indeterminate || upstream != 0 {
			t.Errorf("a refused connection: verdict %v, status %d", got, upstream)
		}
	})

	t.Run("a server whose certificate is not trusted", func(t *testing.T) {
		var hits atomic.Int64
		srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
		t.Cleanup(srv.Close)
		got, upstream := vault.NewOpenAIValidator(baseOf(t, srv.URL), 2*time.Second).Validate(t.Context(), key)
		if got != vault.Indeterminate || upstream != 0 || hits.Load() != 0 {
			t.Errorf("an untrusted certificate: verdict %v, status %d, %d requests reached the handler", got, upstream, hits.Load())
		}
	})

	t.Run("the proxy of the environment is not used", func(t *testing.T) {
		proxy, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { proxy.Close() })
		var contacted atomic.Int64
		go func() {
			for {
				c, err := proxy.Accept()
				if err != nil {
					return
				}
				contacted.Add(1)
				c.Close()
			}
		}()
		for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
			t.Setenv(name, "http://"+proxy.Addr().String())
		}
		t.Setenv("NO_PROXY", "")
		t.Setenv("no_proxy", "")
		// A destination that is not loopback (Go never proxies loopback) and where nothing
		// answers: with the proxy it would go to the listener above.
		const dest = "http://192.0.2.1:9" // TEST-NET-1, reserved for documentation
		// The control: the default transport of the standard library does use the proxy here.
		req, _ := http.NewRequestWithContext(t.Context(), "GET", dest+"/models", nil)
		cl := &http.Client{Timeout: time.Second}
		if resp, err := cl.Do(req); err == nil {
			resp.Body.Close()
		}
		if contacted.Load() == 0 {
			t.Fatal("the control failed: the default transport of the standard library did not use the proxy of the environment, so this test would prove nothing (the environment of a process is read once, by the first request that looks at it)")
		}
		contacted.Store(0)
		got, upstream := vault.NewOpenAIValidator(baseOf(t, dest), 500*time.Millisecond).Validate(t.Context(), key)
		if got != vault.Indeterminate || upstream != 0 {
			t.Errorf("verdict %v, status %d", got, upstream)
		}
		if n := contacted.Load(); n != 0 {
			t.Errorf("the validator talked to the proxy of the environment %d times", n)
		}
	})

	t.Run("at most 64 KiB of the response is read", func(t *testing.T) {
		var written atomic.Int64
		chunk := []byte(strings.Repeat("x", 4096))
		m := newModels(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			f, _ := w.(http.Flusher)
			for i := 0; i < 64*1024; i++ { // 256 MiB if nobody stops it
				if _, err := w.Write(chunk); err != nil {
					return
				}
				written.Add(int64(len(chunk)))
				if f != nil {
					f.Flush()
				}
				if r.Context().Err() != nil {
					return
				}
			}
		})
		start := time.Now()
		got, upstream := vault.NewOpenAIValidator(baseOf(t, m.srv.URL), 5*time.Second).Validate(t.Context(), key)
		if got != vault.Accepted || upstream != 200 {
			t.Errorf("verdict %v, status %d for a 200 with a long body", got, upstream)
		}
		if d := time.Since(start); d > 4*time.Second {
			t.Errorf("the call took %s: the body was read to its end", d)
		}
		time.Sleep(200 * time.Millisecond)
		if n := written.Load(); n > 32<<20 {
			t.Errorf("the server wrote %d MiB before the client stopped reading", n>>20)
		}
	})
}
