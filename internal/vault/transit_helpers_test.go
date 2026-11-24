package vault_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/internal/vault/transittest"
	"github.com/pandokos/pandokos/test/testutil"
	"github.com/pandokos/pandokos/test/vaulttest"
)

// rigStart is the time the clock of a rig starts at.
var rigStart = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

// transitRig is a double of the key service, its files and a provider on them, with a log at debug
// level. The provider is built by newTransitRig and started by start.
type transitRig struct {
	s     *transittest.Server
	f     vaulttest.TransitFiles
	p     *vault.TransitProvider
	o     vault.TransitOptions
	logs  *testutil.LogBuffer
	clock *tClock
}

// withClock gives the provider the test clock; the double keeps its own (Advance).
func withClock(c *tClock) func(*vault.TransitOptions) {
	return func(o *vault.TransitOptions) { o.Now = c.now }
}

// withTimeout sets the request timeout.
func withTimeout(d time.Duration) func(*vault.TransitOptions) {
	return func(o *vault.TransitOptions) { o.RequestTimeout = d }
}

// withTransport wraps the transport of the provider.
func withTransport(wrap func(http.RoundTripper) http.RoundTripper) func(*vault.TransitOptions) {
	return func(o *vault.TransitOptions) { o.Transport = wrap(o.Transport) }
}

// newTransitRig makes a double with a key, its files and a provider that is built and not started.
func newTransitRig(t *testing.T, setup vaulttest.TransitSetup, adjust ...func(*vault.TransitOptions)) *transitRig {
	t.Helper()
	s := transittest.New(t)
	return newTransitRigOn(t, s, setup, adjust...)
}

func newTransitRigOn(t *testing.T, s *transittest.Server, setup vaulttest.TransitSetup, adjust ...func(*vault.TransitOptions)) *transitRig {
	t.Helper()
	r := &transitRig{s: s, logs: &testutil.LogBuffer{}, clock: &tClock{t: rigStart}}
	r.f = vaulttest.Transit(t, s, setup)
	r.build(t, adjust...)
	return r
}

// build makes the provider from the files, replacing the one it had.
func (r *transitRig) build(t *testing.T, adjust ...func(*vault.TransitOptions)) {
	t.Helper()
	all := append([]func(*vault.TransitOptions){func(o *vault.TransitOptions) { o.Logger = newLogger(r.logs) }}, adjust...)
	o, g := vaulttest.TransitOptions(t, r.s, r.f, all...)
	t.Cleanup(g.CloseIdleConnections)
	if inner := o.Now; inner != nil {
		// r.advance moves the clock the test gave the provider as well.
		t0 := rigStart
		o.Now = func() time.Time { return inner().Add(r.clock.now().Sub(t0)) }
	}
	r.o = o
	p, err := vault.NewTransitProvider(o)
	if err != nil {
		t.Fatalf("NewTransitProvider: %v", err)
	}
	r.p = p
}

// start starts the provider and, when run is true, runs its token lifecycle until the test ends.
func (r *transitRig) start(t *testing.T, run bool) {
	t.Helper()
	if err := r.p.Start(ctx(t)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if run {
		r.run(t)
	}
}

func (r *transitRig) run(t *testing.T) {
	t.Helper()
	c, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.p.Run(c) }()
	t.Cleanup(func() { stop(); <-done })
}

// advance moves both clocks.
func (r *transitRig) advance(d time.Duration) {
	r.clock.advance(d)
	r.s.Advance(d)
}

// readyRig is a started rig on the default setup.
func readyRig(t *testing.T, adjust ...func(*vault.TransitOptions)) *transitRig {
	t.Helper()
	r := newTransitRig(t, vaulttest.TransitSetup{}, adjust...)
	r.start(t, false)
	return r
}

// dataKeyBytes returns 32 bytes that are not a real key.
func dataKeyBytes(seed byte) []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = seed + byte(i)*7
	}
	return b
}

// interceptor is a RoundTripper that answers some requests itself: fail maps a substring of
// "<METHOD> <path>" to a function that makes the answer; the others go on to the inner transport.
type interceptor struct {
	inner http.RoundTripper
	mu    sync.Mutex
	fail  map[string]func(*http.Request) *http.Response
	seen  []string
	rewr  func(req *http.Request, resp *http.Response) *http.Response
}

func newInterceptor(inner http.RoundTripper) *interceptor {
	return &interceptor{inner: inner, fail: map[string]func(*http.Request) *http.Response{}}
}

func (i *interceptor) RoundTrip(req *http.Request) (*http.Response, error) {
	key := req.Method + " " + req.URL.Path
	i.mu.Lock()
	i.seen = append(i.seen, key)
	var f func(*http.Request) *http.Response
	for sub, fn := range i.fail {
		if strings.Contains(key, sub) {
			f = fn
		}
	}
	rewr := i.rewr
	i.mu.Unlock()
	if f != nil {
		if req.Body != nil {
			_, _ = io.Copy(io.Discard, req.Body)
			_ = req.Body.Close()
		}
		return f(req), nil
	}
	resp, err := i.inner.RoundTrip(req)
	if err == nil && rewr != nil {
		resp = rewr(req, resp)
	}
	return resp, err
}

func (i *interceptor) set(sub string, f func(*http.Request) *http.Response) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.fail[sub] = f
}

func (i *interceptor) rewrite(f func(req *http.Request, resp *http.Response) *http.Response) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.rewr = f
}

// answer is a canned response with a body that repeats canary text, as a hostile service would.
func answer(status int, body string) func(*http.Request) *http.Response {
	return func(req *http.Request) *http.Response {
		return &http.Response{
			StatusCode: status, Status: http.StatusText(status), Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req,
		}
	}
}

// rewriteData changes the "data" object of a JSON answer.
func rewriteData(resp *http.Response, change func(map[string]any)) *http.Response {
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		resp.Body = io.NopCloser(bytes.NewReader(b))
		return resp
	}
	if d, ok := m["data"].(map[string]any); ok {
		change(d)
	}
	nb, _ := json.Marshal(m)
	resp.Body = io.NopCloser(bytes.NewReader(nb))
	resp.ContentLength = int64(len(nb))
	resp.Header.Del("Content-Length")
	return resp
}

// transitErr extracts the *TransitError of an error chain.
func transitErr(err error) (*vault.TransitError, bool) {
	var te *vault.TransitError
	if errors.As(err, &te) {
		return te, true
	}
	return nil, false
}

// writeFile writes content to the file of a secret (mode 0600).
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// tClock is a clock that a test moves.
type tClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *tClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *tClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// rewriteJSON changes the top level object of a JSON answer.
func rewriteJSON(resp *http.Response, change func(map[string]any)) *http.Response {
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		resp.Body = io.NopCloser(bytes.NewReader(b))
		return resp
	}
	change(m)
	nb, _ := json.Marshal(m)
	resp.Body = io.NopCloser(bytes.NewReader(nb))
	resp.ContentLength = int64(len(nb))
	resp.Header.Del("Content-Length")
	return resp
}
