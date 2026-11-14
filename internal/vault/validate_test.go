package vault_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/egress"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/test/fixtures"
)

// recorded is what the fake provider saw of one request.
type recorded struct {
	Method, Path, RawQuery string
	Header                 http.Header
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
		m.reqs = append(m.reqs, recorded{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), r.ContentLength})
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

func status(code int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

// tripper is a round tripper that counts what goes through it. It is a plain transport: the
// guard is the subject of its own tests.
type tripper struct {
	inner        http.RoundTripper
	calls        atomic.Int64
	bodyRead     atomic.Int64
	bodiesClosed atomic.Int64
}

func newTripper() *tripper { return &tripper{inner: &http.Transport{}} }

func (p *tripper) RoundTrip(r *http.Request) (*http.Response, error) {
	p.calls.Add(1)
	resp, err := p.inner.RoundTrip(r)
	if err == nil {
		resp.Body = &countingBody{ReadCloser: resp.Body, p: p}
	}
	return resp, err
}

type countingBody struct {
	io.ReadCloser
	p *tripper
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.p.bodyRead.Add(int64(n))
	return n, err
}

func (b *countingBody) Close() error {
	b.p.bodiesClosed.Add(1)
	return b.ReadCloser.Close()
}

type failing struct{ err error }

func (f failing) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

// TestValidator is T-444: one validator for every family, against a target given per call.
func TestValidator(t *testing.T) {
	key := secret.New(fixtures.CanaryCredential)
	echo := `{"error":{"message":"Incorrect API key provided: ` + fixtures.CanaryCredential + `.","type":"invalid_request_error"}}`
	families := []string{"openai", "anthropic"}
	target := func(family, base string) vault.ValidationTarget {
		return vault.ValidationTarget{Family: family, BaseURL: base}
	}
	newValidator := func(timeout time.Duration) (vault.Validator, *tripper, *tripper) {
		pub, priv := newTripper(), newTripper()
		return vault.NewValidator(pub, priv, timeout), pub, priv
	}

	t.Run("the families it knows", func(t *testing.T) {
		if got := vault.ValidatorFamilies(); !reflect.DeepEqual(got, []string{"anthropic", "openai"}) {
			t.Errorf("ValidatorFamilies = %v, want [anthropic openai] (sorted)", got)
		}
	})

	t.Run("the verdict follows the status", func(t *testing.T) {
		for _, fam := range families {
			for _, c := range []struct {
				code int
				want vault.Verdict
			}{
				{200, vault.Accepted}, {204, vault.Accepted},
				{401, vault.Rejected}, {403, vault.Rejected},
				{302, vault.Indeterminate}, {307, vault.Indeterminate},
				{400, vault.Indeterminate}, {404, vault.Indeterminate}, {429, vault.Indeterminate},
				{500, vault.Indeterminate}, {502, vault.Indeterminate}, {503, vault.Indeterminate},
			} {
				m := newModels(t, status(c.code, echo))
				v, _, _ := newValidator(2 * time.Second)
				got, upstream := v.Validate(t.Context(), target(fam, m.srv.URL), key)
				if got != c.want || upstream != c.code {
					t.Errorf("%s, HTTP %d: verdict %v, upstream status %d; want %v, %d", fam, c.code, got, upstream, c.want, c.code)
				}
				if n := len(m.requests()); n != 1 {
					t.Errorf("%s, HTTP %d: %d requests, want exactly one", fam, c.code, n)
				}
				if strings.Contains(fmt.Sprint(got, upstream), fixtures.CanaryCredential) {
					t.Errorf("%s, HTTP %d: the key is in what Validate returns", fam, c.code)
				}
			}
		}
	})

	t.Run("the request of each family", func(t *testing.T) {
		for _, fam := range families {
			for _, c := range []struct{ name, base, path string }{
				{"a base without a path", "", "/v1/models"},
				{"a base with a path", "/prefix", "/prefix/v1/models"},
				{"a trailing slash is not doubled", "/prefix/", "/prefix/v1/models"},
				{"a trailing slash on the root", "/", "/v1/models"},
			} {
				m := newModels(t, status(200, `{"object":"list","data":[]}`))
				v, _, _ := newValidator(2 * time.Second)
				if got, _ := v.Validate(t.Context(), target(fam, m.srv.URL+c.base), key); got != vault.Accepted {
					t.Fatalf("%s, %s: verdict %v", fam, c.name, got)
				}
				reqs := m.requests()
				if len(reqs) != 1 {
					t.Fatalf("%s, %s: %d requests", fam, c.name, len(reqs))
				}
				r := reqs[0]
				if r.Method != "GET" || r.Path != c.path || r.RawQuery != "" || r.ContentLength > 0 {
					t.Errorf("%s, %s: request %s %s?%s with %d bytes of body", fam, c.name, r.Method, r.Path, r.RawQuery, r.ContentLength)
				}
			}
			m := newModels(t, status(200, `{}`))
			v, _, _ := newValidator(2 * time.Second)
			v.Validate(t.Context(), target(fam, m.srv.URL), key)
			h := m.requests()[0].Header
			allowed := map[string]bool{"User-Agent": true, "Accept-Encoding": true}
			switch fam {
			case "openai":
				if got := h.Get("Authorization"); got != "Bearer "+fixtures.CanaryCredential {
					t.Errorf("openai: Authorization is %q", got)
				}
				allowed["Authorization"] = true
			case "anthropic":
				if got := h.Get("X-Api-Key"); got != fixtures.CanaryCredential {
					t.Errorf("anthropic: x-api-key is %q", got)
				}
				if got := h.Get("Anthropic-Version"); got != "2023-06-01" {
					t.Errorf("anthropic: anthropic-version is %q", got)
				}
				if h.Get("Authorization") != "" {
					t.Error("anthropic: an Authorization header is sent")
				}
				allowed["X-Api-Key"], allowed["Anthropic-Version"] = true, true
			}
			for name := range h {
				if !allowed[name] {
					t.Errorf("%s: the request carries the header %s", fam, name)
				}
			}
		}
	})

	t.Run("an unknown family sends nothing", func(t *testing.T) {
		m := newModels(t, status(200, `{}`))
		v, pub, priv := newValidator(2 * time.Second)
		for _, fam := range []string{"", "gemini", "OpenAI", "bedrock"} {
			if got, upstream := v.Validate(t.Context(), target(fam, m.srv.URL), key); got != vault.Indeterminate || upstream != 0 {
				t.Errorf("family %q: %v %d", fam, got, upstream)
			}
		}
		if n := len(m.requests()) + int(pub.calls.Load()+priv.calls.Load()); n != 0 {
			t.Errorf("%d requests for unknown families", n)
		}
	})

	t.Run("Private selects the round tripper", func(t *testing.T) {
		m := newModels(t, status(200, `{}`))
		v, pub, priv := newValidator(2 * time.Second)
		v.Validate(t.Context(), vault.ValidationTarget{Family: "openai", BaseURL: m.srv.URL, Private: false}, key)
		if pub.calls.Load() != 1 || priv.calls.Load() != 0 {
			t.Errorf("a public target: public %d, private %d", pub.calls.Load(), priv.calls.Load())
		}
		v.Validate(t.Context(), vault.ValidationTarget{Family: "openai", BaseURL: m.srv.URL, Private: true}, key)
		if pub.calls.Load() != 1 || priv.calls.Load() != 1 {
			t.Errorf("a private target: public %d, private %d", pub.calls.Load(), priv.calls.Load())
		}
	})

	t.Run("a redirect is not followed", func(t *testing.T) {
		var hits atomic.Int64
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.WriteHeader(200)
		}))
		t.Cleanup(other.Close)
		for _, fam := range families {
			m := newModels(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, other.URL+"/v1/models", http.StatusFound)
			})
			v, _, _ := newValidator(2 * time.Second)
			got, upstream := v.Validate(t.Context(), target(fam, m.srv.URL), key)
			if got != vault.Indeterminate || upstream != 302 {
				t.Errorf("%s, a 302: verdict %v, status %d, want Indeterminate and 302", fam, got, upstream)
			}
		}
		if hits.Load() != 0 {
			t.Error("the redirect was followed, and the credential sent to another address")
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
		v, _, _ := newValidator(300 * time.Millisecond)
		start := time.Now()
		got, upstream := v.Validate(t.Context(), target("openai", m.srv.URL), key)
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
		v, _, _ := newValidator(30 * time.Second)
		start := time.Now()
		got, _ := v.Validate(ctx, target("anthropic", m.srv.URL), key)
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
		v, _, _ := newValidator(2 * time.Second)
		got, upstream := v.Validate(t.Context(), target("openai", "http://"+addr), key)
		if got != vault.Indeterminate || upstream != 0 {
			t.Errorf("a refused connection: verdict %v, status %d", got, upstream)
		}
	})

	t.Run("a refusal of the guard is indeterminate", func(t *testing.T) {
		const marker = "synthetic-marker-error-text"
		v := vault.NewValidator(failing{&egress.BlockedError{Reason: "private_address"}}, failing{fmt.Errorf("%s %s", marker, fixtures.CanaryCredential)}, time.Second)
		for _, private := range []bool{false, true} {
			got, upstream := v.Validate(t.Context(), vault.ValidationTarget{Family: "openai", BaseURL: "https://provider.example.test", Private: private}, key)
			if got != vault.Indeterminate || upstream != 0 {
				t.Errorf("private=%v: verdict %v, status %d", private, got, upstream)
			}
			if strings.Contains(fmt.Sprint(got, upstream), marker) {
				t.Error("the error text is in what Validate returns")
			}
		}
	})

	t.Run("a malformed base URL sends nothing", func(t *testing.T) {
		v, pub, priv := newValidator(time.Second)
		for _, base := range []string{"", "://", "not a url", "http://[::1"} {
			if got, upstream := v.Validate(t.Context(), target("openai", base), key); got != vault.Indeterminate || upstream != 0 {
				t.Errorf("%q: %v %d", base, got, upstream)
			}
		}
		if pub.calls.Load()+priv.calls.Load() != 0 {
			t.Error("a request was made for a malformed base URL")
		}
	})

	t.Run("at most 64 KiB of the response is read, and the body is closed", func(t *testing.T) {
		chunk := []byte(strings.Repeat("x", 4096))
		m := newModels(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			f, _ := w.(http.Flusher)
			for i := 0; i < 256; i++ { // 1 MiB if nobody stops it
				if _, err := w.Write(chunk); err != nil {
					return
				}
				if f != nil {
					f.Flush()
				}
				if r.Context().Err() != nil {
					return
				}
			}
		})
		v, pub, _ := newValidator(5 * time.Second)
		start := time.Now()
		got, upstream := v.Validate(t.Context(), target("openai", m.srv.URL), key)
		if got != vault.Accepted || upstream != 200 {
			t.Errorf("verdict %v, status %d for a 200 with a long body", got, upstream)
		}
		if d := time.Since(start); d > 4*time.Second {
			t.Errorf("the call took %s: the body was read to its end", d)
		}
		if n := pub.bodyRead.Load(); n > 64*1024 {
			t.Errorf("%d bytes of the response were read, want at most 65536", n)
		}
		if pub.bodiesClosed.Load() != 1 {
			t.Errorf("%d response bodies closed, want 1", pub.bodiesClosed.Load())
		}
	})

	t.Run("a timeout that is not positive means ten seconds", func(t *testing.T) {
		m := newModels(t, status(200, `{}`))
		v := vault.NewValidator(newTripper(), newTripper(), 0)
		if got, _ := v.Validate(t.Context(), target("openai", m.srv.URL), key); got != vault.Accepted {
			t.Errorf("verdict %v", got)
		}
	})
}
