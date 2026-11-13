package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testcert"
	"github.com/pandokos/pandokos/test/testutil"
)

// testKey is generated when the tests start; the static authenticator knows it.
var testKey = func() string {
	k, _, _, err := authn.Generate(authn.KindTenant)
	if err != nil {
		panic(err)
	}
	return k.Reveal()
}()

// fakeAdmin is the Admin handler of the tests: it counts its requests, answers GET /v1/me and
// answers everything else with a not_found problem, as the real handler does.
type fakeAdmin struct{ n atomic.Int64 }

func (f *fakeAdmin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.n.Add(1)
	if r.Method == http.MethodGet && r.URL.Path == "/v1/me" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"admin":true}`))
		return
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"code":"not_found"}`))
}

func testConfig(t *testing.T, up *fakeprovider.Server) *config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.Listeners.Inference.Address = "127.0.0.1:0"
	cfg.Listeners.Admin.Address = "127.0.0.1:0"
	u, err := url.Parse(up.BaseURL())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Skeleton.OpenAI.BaseURL = up.BaseURL()
	cfg.Skeleton.OpenAI.ParsedBaseURL = u
	cfg.Skeleton.OpenAI.APIKey = secret.New(fixtures.CanaryProviderKey)
	return cfg
}

func newOptions(cfg *config.Config, logs io.Writer, admin http.Handler) Options {
	principal := testutil.InferencePrincipal()
	return Options{
		Config: cfg, Logger: telemetry.NewLogger(logs, "debug"), Admin: admin,
		Authenticator: testutil.StaticAuthenticator(map[string]authz.Principal{testKey: principal}),
	}
}

func startServer(t *testing.T, mutate func(*config.Config)) (*Server, *fakeprovider.Server, *testutil.LogBuffer, func(), <-chan error) {
	t.Helper()
	srv, up, logs, cancel, done, _ := startServerWithAdmin(t, mutate)
	return srv, up, logs, cancel, done
}

func startServerWithAdmin(t *testing.T, mutate func(*config.Config)) (*Server, *fakeprovider.Server, *testutil.LogBuffer, func(), <-chan error, *fakeAdmin) {
	t.Helper()
	up := fakeprovider.New(t)
	cfg := testConfig(t, up)
	if mutate != nil {
		mutate(cfg)
	}
	logs := &testutil.LogBuffer{}
	admin := &fakeAdmin{}
	srv, err := New(newOptions(cfg, logs, admin))
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
	return srv, up, logs, cancel, done, admin
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
	req.Header.Set("Authorization", "Bearer "+testKey)
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
	if len(lines) != 2 || lines[0]["listener"] != "inference" || lines[0]["address"] != srv.InferenceAddr().String() || lines[0]["tls"] != false {
		t.Fatalf("listening lines: %v", lines)
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
	srv, err := New(newOptions(cfg, io.Discard, &fakeAdmin{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Listen(); err == nil {
		t.Fatal("expected a listen error")
	}
	// If the second listener fails, the first is closed.
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	inference := free.Addr().String()
	free.Close()
	cfg = testConfig(t, up)
	cfg.Listeners.Inference.Address = inference
	cfg.Listeners.Admin.Address = busy.Addr().String()
	srv, err = New(newOptions(cfg, io.Discard, &fakeAdmin{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Listen(); err == nil {
		t.Fatal("expected a listen error on the admin address")
	}
	ln, err := net.Listen("tcp", inference)
	if err != nil {
		t.Fatalf("the inference listener was not closed after the admin listener failed: %v", err)
	}
	ln.Close()
}

func TestNewValidatesOptions(t *testing.T) {
	up := fakeprovider.New(t)
	good := func() Options { return newOptions(testConfig(t, up), io.Discard, &fakeAdmin{}) }
	if _, err := New(good()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Options){
		"config":        func(o *Options) { o.Config = nil },
		"logger":        func(o *Options) { o.Logger = nil },
		"authenticator": func(o *Options) { o.Authenticator = nil },
		"admin":         func(o *Options) { o.Admin = nil },
	} {
		o := good()
		mutate(&o)
		if _, err := New(o); err == nil {
			t.Errorf("%s: expected an error", name)
		}
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

func getFrom(t *testing.T, client *http.Client, base, path string) (*http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(b))
	return resp, nil
}

func bodyOf(resp *http.Response) string {
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// TestListenersAreSeparate is T-240.
func TestListenersAreSeparate(t *testing.T) {
	srv, _, logs, cancel, done, admin := startServerWithAdmin(t, nil)
	if srv.InferenceAddr().String() == srv.AdminAddr().String() {
		t.Fatal("the two listeners share an address")
	}
	inf, adm := "http://"+srv.InferenceAddr().String(), "http://"+srv.AdminAddr().String()
	c := http.DefaultClient
	testutil.Eventually(t, 5*time.Second, "readiness", func() bool {
		r, err := getFrom(t, c, adm, "/readyz")
		return err == nil && r.StatusCode == 200
	})
	for _, base := range []string{inf, adm} {
		for _, p := range []string{"/healthz", "/readyz"} {
			if r, err := getFrom(t, c, base, p); err != nil || r.StatusCode != 200 {
				t.Errorf("%s%s: %v %v", base, p, r, err)
			}
		}
	}
	// An admin path on the inference listener is 404 in the OpenAI shape.
	r, err := getFrom(t, c, inf, "/v1/me")
	if err != nil || r.StatusCode != 404 || r.Header.Get("X-Pandokos-Error") != "endpoint_not_supported" || !strings.Contains(bodyOf(r), `"error"`) {
		t.Errorf("/v1/me on the inference listener: %v %v", r, err)
	}
	if admin.n.Load() != 0 {
		t.Error("the admin handler saw an inference request")
	}
	// The inference path on the admin listener is 404 not_found, from the admin handler.
	req, _ := http.NewRequest(http.MethodPost, adm+"/openai/v1/chat/completions", strings.NewReader("{}"))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 404 || !strings.Contains(string(b), `"not_found"`) || resp.Header.Get("X-Pandokos-Error") != "" {
		t.Errorf("the inference path on the admin listener: %d %s", resp.StatusCode, b)
	}
	// An admin request reaches the admin handler.
	if r, err := getFrom(t, c, adm, "/v1/me"); err != nil || r.StatusCode != 200 || bodyOf(r) != `{"admin":true}` {
		t.Errorf("/v1/me on the admin listener: %v %v", r, err)
	}
	// The health paths are the server's, not the handler's.
	before := admin.n.Load()
	_, _ = getFrom(t, c, adm, "/healthz")
	if admin.n.Load() != before {
		t.Error("a health request reached the admin handler")
	}
	if l := logs.Find(t, "listening"); len(l) != 2 || l[1]["listener"] != "admin" || l[1]["address"] != srv.AdminAddr().String() {
		t.Errorf("listening lines: %v", l)
	}
	debug := false
	for _, l := range logs.Find(t, "admin request") {
		if l["operation"] == "healthz" && l["level"] == "DEBUG" {
			debug = true
		}
	}
	if !debug {
		t.Error("no debug admin request line for the health path")
	}

	// Closing the context drains both listeners.
	cancel()
	testutil.Eventually(t, 5*time.Second, "both listeners to close", func() bool {
		for _, a := range []net.Addr{srv.InferenceAddr(), srv.AdminAddr()} {
			if c, err := net.DialTimeout("tcp", a.String(), 200*time.Millisecond); err == nil {
				c.Close()
				return false
			}
		}
		return true
	})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
	if l := logs.Find(t, "shutdown complete"); len(l) != 1 || l[0]["forced"] != false {
		t.Errorf("shutdown complete: %v", l)
	}
}

// tlsFiles returns a certificate for localhost and 127.0.0.1, the authority's pool, and a
// config with both listeners set to use it when their entries are set.
func tlsFor(t *testing.T) (*tls.Certificate, *x509.CertPool) {
	t.Helper()
	caFile, certFile, keyFile := testcert.Files(t, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	return &cert, pool
}

func tlsClient(pool *x509.CertPool, mutate func(*tls.Config)) *http.Client {
	c := &tls.Config{RootCAs: pool}
	if mutate != nil {
		mutate(c)
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: c, ForceAttemptHTTP2: true}}
}

// TestTLSOnBothListeners is T-241.
func TestTLSOnBothListeners(t *testing.T) {
	cert, pool := tlsFor(t)
	srv, up, logs, _, _, admin := startServerWithAdmin(t, func(c *config.Config) {
		c.Listeners.Inference.TLS = config.ListenerTLS{MinVersion: "TLS1.2", Certificate: cert}
		c.Listeners.Admin.TLS = config.ListenerTLS{MinVersion: "TLS1.2", Certificate: cert}
	})
	good := tlsClient(pool, nil)
	for name, addr := range map[string]net.Addr{"inference": srv.InferenceAddr(), "admin": srv.AdminAddr()} {
		base := "https://" + addr.String()
		testutil.Eventually(t, 5*time.Second, name+" readiness", func() bool {
			r, err := getFrom(t, good, base, "/readyz")
			return err == nil && r.StatusCode == 200
		})
		r, err := getFrom(t, good, base, "/healthz")
		if err != nil || r.StatusCode != 200 || r.ProtoMajor != 2 || r.TLS == nil {
			t.Errorf("%s: %v %v (HTTP/2 must be negotiated)", name, r, err)
		}
		// A client that does not trust the authority fails the handshake.
		if _, err := getFrom(t, tlsClient(x509.NewCertPool(), nil), base, "/healthz"); err == nil {
			t.Errorf("%s: a client that does not trust the authority connected", name)
		}
		// A plain HTTP request reaches no handler.
		plain, err := http.Get("http://" + addr.String() + "/v1/me")
		if err == nil {
			defer plain.Body.Close()
			if plain.StatusCode != 400 {
				t.Errorf("%s: a plain request to a TLS listener got %d", name, plain.StatusCode)
			}
		}
	}
	if admin.n.Load() != 0 {
		t.Errorf("the admin handler saw %d plain requests", admin.n.Load())
	}
	if n := len(up.Requests()); n != 0 {
		t.Errorf("the provider saw %d requests", n)
	}
	for _, l := range logs.Find(t, "listening") {
		if l["tls"] != true {
			t.Errorf("listening line: %v", l)
		}
	}
	// The gateway works through TLS.
	req, _ := http.NewRequest(http.MethodPost, "https://"+srv.InferenceAddr().String()+"/openai/v1/chat/completions",
		strings.NewReader(`{"model":"fake-basic","messages":[{"role":"user","content":"`+fixtures.CanaryPrompt+`"}]}`))
	req.Header.Set("Authorization", "Bearer "+testKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := good.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("inference over TLS: %d", resp.StatusCode)
	}
}

// TestTLSMinimumVersion is T-242.
func TestTLSMinimumVersion(t *testing.T) {
	cert, pool := tlsFor(t)
	for _, c := range []struct {
		name    string
		min     string
		client  func(*tls.Config)
		wantErr bool
	}{
		{"default refuses TLS 1.1", "TLS1.2", func(c *tls.Config) { c.MinVersion, c.MaxVersion = tls.VersionTLS10, tls.VersionTLS11 }, true},
		{"default accepts TLS 1.2", "TLS1.2", func(c *tls.Config) { c.MinVersion, c.MaxVersion = tls.VersionTLS12, tls.VersionTLS12 }, false},
		{"default accepts TLS 1.3", "TLS1.2", func(c *tls.Config) { c.MinVersion, c.MaxVersion = tls.VersionTLS13, tls.VersionTLS13 }, false},
		{"TLS1.3 refuses TLS 1.2", "TLS1.3", func(c *tls.Config) { c.MinVersion, c.MaxVersion = tls.VersionTLS12, tls.VersionTLS12 }, true},
		{"TLS1.3 accepts TLS 1.3", "TLS1.3", func(c *tls.Config) { c.MinVersion, c.MaxVersion = tls.VersionTLS13, tls.VersionTLS13 }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, _, _, _, _, _ := startServerWithAdmin(t, func(cfg *config.Config) {
				cfg.Listeners.Inference.TLS = config.ListenerTLS{MinVersion: c.min, Certificate: cert}
				cfg.Listeners.Admin.TLS = config.ListenerTLS{MinVersion: c.min, Certificate: cert}
			})
			for _, addr := range []net.Addr{srv.InferenceAddr(), srv.AdminAddr()} {
				_, err := getFrom(t, tlsClient(pool, c.client), "https://"+addr.String(), "/healthz")
				if (err != nil) != c.wantErr {
					t.Errorf("%s: error %v, want error %v", addr, err, c.wantErr)
				}
			}
		})
	}
}

// TestMixedListeners is T-243.
func TestMixedListeners(t *testing.T) {
	cert, pool := tlsFor(t)
	for _, c := range []struct {
		name                       string
		tlsOnInference, tlsOnAdmin bool
	}{{"TLS on the administration API only", false, true}, {"TLS on the inference API only", true, false}} {
		t.Run(c.name, func(t *testing.T) {
			srv, _, logs, _, _, _ := startServerWithAdmin(t, func(cfg *config.Config) {
				if c.tlsOnInference {
					cfg.Listeners.Inference.TLS = config.ListenerTLS{MinVersion: "TLS1.2", Certificate: cert}
				}
				if c.tlsOnAdmin {
					cfg.Listeners.Admin.TLS = config.ListenerTLS{MinVersion: "TLS1.2", Certificate: cert}
				}
			})
			scheme := func(on bool) string {
				if on {
					return "https://"
				}
				return "http://"
			}
			client := tlsClient(pool, nil)
			for _, l := range []struct {
				addr net.Addr
				on   bool
			}{{srv.InferenceAddr(), c.tlsOnInference}, {srv.AdminAddr(), c.tlsOnAdmin}} {
				testutil.Eventually(t, 5*time.Second, "readiness of "+l.addr.String(), func() bool {
					r, err := getFrom(t, client, scheme(l.on)+l.addr.String(), "/readyz")
					return err == nil && r.StatusCode == 200
				})
			}
			lines := logs.Find(t, "listening")
			if len(lines) != 2 || lines[0]["tls"] != c.tlsOnInference || lines[1]["tls"] != c.tlsOnAdmin {
				t.Errorf("listening lines: %v", lines)
			}
		})
	}
}
