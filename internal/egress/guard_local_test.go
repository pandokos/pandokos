package egress_test

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/egress"
	"github.com/pandokos/pandokos/test/testcert"
	"github.com/pandokos/pandokos/test/testutil"
)

// allPublic is the classification of the tests that need the public transport to reach a local
// server: nothing outside the package can install it (export_test.go).
func allPublic(netip.Addr) egress.AddrClass { return egress.AddrPublic }

// srv is a local TLS server that counts the connections it accepted and closed.
type srv struct {
	*httptest.Server
	accepted, closed, requests atomic.Int64
	ca                         *testcert.CA
}

func (s *srv) port() string {
	_, p, _ := net.SplitHostPort(s.Listener.Addr().String())
	return p
}

type srvOpts struct {
	ca      *testcert.CA
	dns     []string
	ips     []net.IP
	host    string // the address to listen on; default 127.0.0.1
	handler http.HandlerFunc
	tls     func(*tls.Config)
}

func startTLS(t *testing.T, o srvOpts) *srv {
	t.Helper()
	if o.ca == nil {
		ca, err := testcert.NewCA(time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		o.ca = ca
	}
	if o.host == "" {
		o.host = "127.0.0.1"
	}
	if o.dns == nil && o.ips == nil {
		o.dns, o.ips = []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")}
	}
	certPEM, keyPEM, err := o.ca.Issue(o.dns, o.ips, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	s := &srv{ca: o.ca}
	h := o.handler
	if h == nil {
		h = func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }
	}
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		h(w, r)
	}))
	l, err := net.Listen("tcp", net.JoinHostPort(o.host, "0"))
	if err != nil {
		t.Skipf("cannot listen on %s: %v", o.host, err)
	}
	s.Listener.Close()
	s.Listener = l
	s.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	if o.tls != nil {
		o.tls(s.TLS)
	}
	s.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		switch st {
		case http.StateNew:
			s.accepted.Add(1)
		case http.StateClosed, http.StateHijacked:
			s.closed.Add(1)
		}
	}
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

func (s *srv) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AppendCertsFromPEM(s.ca.CertPEM())
	return p
}

// do sends a request through rt and drains the body.
func do(t *testing.T, rt http.RoundTripper, method, rawURL string, hdr http.Header) (int, http.Header, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, resp.Header, nil
}

func hostURL(host string, s *srv, path string) string {
	return "https://" + net.JoinHostPort(host, s.port()) + path
}

// TestDialledAddressIsChecked is T-415: the check is made on the address actually dialled.
func TestDialledAddressIsChecked(t *testing.T) {
	s := startTLS(t, srvOpts{})
	g := newGuard(t, egress.Options{RootCAs: s.pool()})

	for _, h := range []string{"localhost", "127.0.0.1", "::1", "::ffff:127.0.0.1"} {
		t.Run("public transport, "+h, func(t *testing.T) {
			_, _, err := do(t, g.Transport(false), "GET", hostURL(h, s, "/"), nil)
			if err == nil {
				t.Fatal("reached a loopback server through the public transport")
			}
			be := blocked(t, err)
			if be.Reason != "private_address" {
				t.Errorf("reason %q, want private_address", be.Reason)
			}
			if h == "localhost" {
				if a := be.BlockedAddr(); a != "127.0.0.1" && a != "::1" {
					t.Errorf("the refused address is %q: the check must be on the address dialled", a)
				}
			}
		})
	}
	if n := s.accepted.Load(); n != 0 {
		t.Fatalf("the server accepted %d connections from the public transport", n)
	}

	t.Run("private transport", func(t *testing.T) {
		for _, h := range []string{"127.0.0.1", "localhost"} {
			status, _, err := do(t, g.Transport(true), "GET", hostURL(h, s, "/"), nil)
			if err != nil || status != 200 {
				t.Errorf("%s: %d %v", h, status, err)
			}
		}
	})
}

// TestTLS is T-417: certificates are always verified, TLS 1.2 or later.
func TestTLS(t *testing.T) {
	t.Run("an authority nobody trusts", func(t *testing.T) {
		s := startTLS(t, srvOpts{})
		g, err := egress.NewGuardWithClassifier(egress.Options{ConnectTimeout: 2 * time.Second}, allPublic)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(g.CloseIdleConnections)
		for _, private := range []bool{false, true} {
			_, _, err := do(t, g.Transport(private), "GET", hostURL("localhost", s, "/"), nil)
			var cve *tls.CertificateVerificationError
			if !errors.As(err, &cve) {
				t.Errorf("private=%v: %v, want a certificate verification error", private, err)
			}
		}
		if n := s.requests.Load(); n != 0 {
			t.Errorf("%d requests reached the handler of a server with an untrusted certificate", n)
		}
	})

	t.Run("accepted with the authority in RootCAs", func(t *testing.T) {
		s := startTLS(t, srvOpts{})
		g, err := egress.NewGuardWithClassifier(egress.Options{ConnectTimeout: 2 * time.Second, RootCAs: s.pool()}, allPublic)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(g.CloseIdleConnections)
		for _, private := range []bool{false, true} {
			if status, _, err := do(t, g.Transport(private), "GET", hostURL("localhost", s, "/"), nil); err != nil || status != 200 {
				t.Errorf("private=%v: %d %v", private, status, err)
			}
		}
	})

	t.Run("a wrong host name", func(t *testing.T) {
		s := startTLS(t, srvOpts{dns: []string{"other.example.test"}, ips: []net.IP{net.ParseIP("10.9.9.9")}})
		g, err := egress.NewGuardWithClassifier(egress.Options{ConnectTimeout: 2 * time.Second, RootCAs: s.pool()}, allPublic)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(g.CloseIdleConnections)
		for _, private := range []bool{false, true} {
			_, _, err := do(t, g.Transport(private), "GET", hostURL("127.0.0.1", s, "/"), nil)
			var he x509.HostnameError
			if !errors.As(err, &he) {
				t.Errorf("private=%v: %v, want a host name error", private, err)
			}
		}
	})

	t.Run("a TLS 1.1 server", func(t *testing.T) {
		s := startTLS(t, srvOpts{tls: func(c *tls.Config) { c.MinVersion, c.MaxVersion = tls.VersionTLS10, tls.VersionTLS11 }})
		g, err := egress.NewGuardWithClassifier(egress.Options{ConnectTimeout: 2 * time.Second, RootCAs: s.pool()}, allPublic)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(g.CloseIdleConnections)
		for _, private := range []bool{false, true} {
			if status, _, err := do(t, g.Transport(private), "GET", hostURL("localhost", s, "/"), nil); err == nil {
				t.Errorf("private=%v: a TLS 1.1 server answered %d", private, status)
			}
		}
		if n := s.requests.Load(); n != 0 {
			t.Errorf("%d requests reached the handler", n)
		}
	})

	t.Run("the transports verify and require TLS 1.2", func(t *testing.T) {
		pool := x509.NewCertPool()
		g := newGuard(t, egress.Options{RootCAs: pool})
		for name, tr := range map[string]*http.Transport{"public": g.PublicTransport(), "private": g.PrivateTransport()} {
			c := tr.TLSClientConfig
			if c == nil {
				t.Fatalf("%s: no TLS configuration", name)
			}
			// The field is read by name: no file of internal/ writes the identifier (part F greps for it).
			if reflect.ValueOf(c).Elem().FieldByName("Insecure" + "SkipVerify").Bool() {
				t.Errorf("%s: certificate verification is switched off", name)
			}
			if c.MinVersion != tls.VersionTLS12 {
				t.Errorf("%s: MinVersion %#x, want TLS 1.2", name, c.MinVersion)
			}
			if c.RootCAs != pool {
				t.Errorf("%s: RootCAs is not the pool given in Options", name)
			}
			if tr.Proxy != nil {
				t.Errorf("%s: a proxy function is set without Options.Proxy", name)
			}
			if !tr.DisableCompression {
				t.Errorf("%s: compression is not disabled", name)
			}
			if tr.MaxResponseHeaderBytes != 262144 {
				t.Errorf("%s: MaxResponseHeaderBytes = %d", name, tr.MaxResponseHeaderBytes)
			}
		}
	})
}

// nonLoopback starts a TLS server reached through an address of the machine that is not loopback
// (Go never applies a proxy setting to loopback), or skips the test.
func nonLoopback(t *testing.T, handler http.HandlerFunc) (*srv, string) {
	t.Helper()
	host := testutil.NonLoopbackHost()
	if host == "" {
		t.Skip("this machine has no address that is not loopback")
	}
	return startTLS(t, srvOpts{host: host, ips: []net.IP{net.ParseIP(host)}, dns: []string{"localhost"}, handler: handler}), host
}

// TestNoRedirectNoEnvironmentProxy is T-418.
func TestNoRedirectNoEnvironmentProxy(t *testing.T) {
	t.Run("a redirect is returned, not followed", func(t *testing.T) {
		target := startTLS(t, srvOpts{})
		s := startTLS(t, srvOpts{handler: func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/elsewhere", http.StatusFound)
		}})
		g := newGuard(t, egress.Options{RootCAs: s.pool()})
		for _, code := range []int{301, 302, 307, 308} {
			handler := func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL+"/elsewhere", code) }
			s := startTLS(t, srvOpts{handler: handler, ca: s.ca})
			status, hdr, err := do(t, g.Transport(true), "POST", hostURL("127.0.0.1", s, "/v1/chat/completions"), nil)
			if err != nil || status != code || hdr.Get("Location") == "" {
				t.Errorf("%d: status %d, error %v, Location %q", code, status, err, hdr.Get("Location"))
			}
		}
		if n := target.accepted.Load(); n != 0 {
			t.Errorf("the redirect target accepted %d connections", n)
		}
	})

	t.Run("proxy variables of the environment are not used", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		var contacted atomic.Int64
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				contacted.Add(1)
				c.Close()
			}
		}()
		t.Cleanup(func() { l.Close() })
		proxy := "http://" + l.Addr().String()
		for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
			t.Setenv(k, proxy)
		}
		t.Setenv("NO_PROXY", "")
		t.Setenv("no_proxy", "")

		s, host := nonLoopback(t, nil)
		// The control: the standard library does pick the proxy up for this destination, so a
		// guard that used it would have sent the request there.
		probe, _ := http.NewRequestWithContext(t.Context(), "GET", hostURL(host, s, "/"), nil)
		fromEnvironment := http.DefaultTransport.(*http.Transport).Proxy // the function that reads the variables
		if u, err := fromEnvironment(probe); err != nil || u == nil {
			t.Fatalf("the control failed: the proxy of the environment = %v, %v; the test would prove nothing", u, err)
		}
		g := newGuard(t, egress.Options{RootCAs: s.pool()})
		for _, private := range []bool{true, false} {
			if status, _, err := do(t, g.Transport(private), "GET", hostURL(host, s, "/"), nil); private && (err != nil || status != 200) {
				t.Errorf("private=%v: %d %v", private, status, err)
			}
		}
		if n := contacted.Load(); n != 0 {
			t.Errorf("the proxy of the environment was contacted %d times", n)
		}
	})
}

// TestProxy is T-419: Options.Proxy applies to the public transport only.
func TestProxy(t *testing.T) {
	t.Run("CONNECT, nothing of the request, TLS with the target through it", func(t *testing.T) {
		target := startTLS(t, srvOpts{})
		proxyURL, requests := testutil.ConnectProxy(t)
		g := newGuard(t, egress.Options{RootCAs: target.pool(), Proxy: proxyURL})
		hdr := http.Header{"Authorization": {"Bearer synthetic-credential"}, "X-Custom": {"v"}, "Accept": {"application/json"}}
		status, _, err := do(t, g.Transport(false), "POST", hostURL("localhost", target, "/v1/chat/completions"), hdr)
		if err != nil || status != 200 {
			t.Fatalf("through the proxy: %d %v", status, err)
		}
		reqs := requests()
		if len(reqs) != 1 {
			t.Fatalf("the proxy saw %d requests, want one CONNECT", len(reqs))
		}
		r := reqs[0]
		if r.Method != http.MethodConnect || r.Host != net.JoinHostPort("localhost", target.port()) {
			t.Errorf("the proxy saw %s %s (host %s)", r.Method, r.RequestURI, r.Host)
		}
		for name := range r.Header {
			if name != "User-Agent" {
				t.Errorf("the CONNECT request carries the header %s", name)
			}
		}
		if target.requests.Load() != 1 {
			t.Errorf("the target saw %d requests", target.requests.Load())
		}
	})

	t.Run("the private transport does not use it", func(t *testing.T) {
		target := startTLS(t, srvOpts{})
		proxyURL, requests := testutil.ConnectProxy(t)
		g := newGuard(t, egress.Options{RootCAs: target.pool(), Proxy: proxyURL})
		if status, _, err := do(t, g.Transport(true), "GET", hostURL("localhost", target, "/"), nil); err != nil || status != 200 {
			t.Fatalf("private transport: %d %v", status, err)
		}
		if n := len(requests()); n != 0 {
			t.Errorf("the proxy saw %d requests from the private transport", n)
		}
	})

	t.Run("a private literal target is refused before the proxy is contacted", func(t *testing.T) {
		proxyURL, requests := testutil.ConnectProxy(t)
		g := newGuard(t, egress.Options{Proxy: proxyURL})
		_, _, err := do(t, g.Transport(false), "GET", "https://10.0.0.5/v1", nil)
		if be := blocked(t, err); be.Reason != "private_address" {
			t.Errorf("reason %q", be.Reason)
		}
		if n := len(requests()); n != 0 {
			t.Errorf("the proxy saw %d requests", n)
		}
	})

	t.Run("a proxy on a forbidden address is refused", func(t *testing.T) {
		proxyURL, _ := url.Parse("http://169.254.169.254:3128")
		g := newGuard(t, egress.Options{Proxy: proxyURL})
		_, _, err := do(t, g.Transport(false), "GET", "https://provider.example.test/v1", nil)
		if be := blocked(t, err); be.Reason != "forbidden_address" {
			t.Errorf("reason %q", be.Reason)
		}
	})

	t.Run("a proxy on a private address is accepted", func(t *testing.T) {
		proxyURL, requests := testutil.ConnectProxy(t)
		if a, _ := netip.ParseAddr(proxyURL.Hostname()); !a.IsLoopback() {
			t.Fatalf("the test proxy is not on loopback: %s", proxyURL.Host)
		}
		target := startTLS(t, srvOpts{})
		g := newGuard(t, egress.Options{RootCAs: target.pool(), Proxy: proxyURL})
		if status, _, err := do(t, g.Transport(false), "GET", hostURL("localhost", target, "/"), nil); err != nil || status != 200 {
			t.Fatalf("%d %v", status, err)
		}
		if len(requests()) != 1 {
			t.Errorf("the proxy saw %d requests", len(requests()))
		}
	})
}

// TestClassesDoNotShareConnections is T-420.
func TestClassesDoNotShareConnections(t *testing.T) {
	t.Run("a connection made under the private rule does not serve a public request", func(t *testing.T) {
		s := startTLS(t, srvOpts{})
		g := newGuard(t, egress.Options{RootCAs: s.pool()})
		u := hostURL("localhost", s, "/") // a name: the check is at dialling, not before
		if status, _, err := do(t, g.Transport(true), "GET", u, nil); err != nil || status != 200 {
			t.Fatalf("private transport: %d %v", status, err)
		}
		_, _, err := do(t, g.Transport(false), "GET", u, nil)
		if err == nil {
			t.Fatal("the public transport reached a loopback server: it reused the private pool")
		}
		if be := blocked(t, err); be.Reason != "private_address" {
			t.Errorf("reason %q, want private_address", be.Reason)
		}
		if n := s.requests.Load(); n != 1 {
			t.Errorf("the server handled %d requests, want only the private one", n)
		}
	})

	t.Run("closing idle connections closes both pools", func(t *testing.T) {
		s := startTLS(t, srvOpts{})
		g, err := egress.NewGuardWithClassifier(egress.Options{ConnectTimeout: 2 * time.Second, RootCAs: s.pool()}, allPublic)
		if err != nil {
			t.Fatal(err)
		}
		u := hostURL("localhost", s, "/")
		for _, private := range []bool{false, true} {
			if status, _, err := do(t, g.Transport(private), "GET", u, nil); err != nil || status != 200 {
				t.Fatalf("private=%v: %d %v", private, status, err)
			}
		}
		if n := s.accepted.Load(); n != 2 {
			t.Fatalf("%d connections for two classes, want 2: the pools are shared", n)
		}
		g.CloseIdleConnections()
		testutil.Eventually(t, 2*time.Second, "both idle connections to be closed", func() bool { return s.closed.Load() == 2 })
	})
}
