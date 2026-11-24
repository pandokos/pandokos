package egress_test

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/pandokos/pandokos/internal/egress"
)

// TestKeyServiceTransport is T-1077: the transport of Guard.KeyService.
func TestKeyServiceTransport(t *testing.T) {
	t.Run("it has its own connection pool", func(t *testing.T) {
		s := startTLS(t, srvOpts{})
		g := newGuard(t, egress.Options{RootCAs: s.pool()})
		ks := g.KeyService(nil)
		if ks == g.Transport(true) || ks == g.Transport(false) {
			t.Fatal("the key service uses the transport of another consumer")
		}
		if status, _, err := do(t, g.Transport(true), "GET", hostURL("127.0.0.1", s, "/"), nil); err != nil || status != 200 {
			t.Fatalf("the private transport: %d %v", status, err)
		}
		before := s.accepted.Load()
		if status, _, err := do(t, ks, "GET", hostURL("127.0.0.1", s, "/"), nil); err != nil || status != 200 {
			t.Fatalf("the key service: %d %v", status, err)
		}
		if got := s.accepted.Load(); got != before+1 {
			t.Errorf("the key service reused a connection of the private transport (%d accepted, was %d)", got, before)
		}
		// Its own connections are reused by itself.
		after := s.accepted.Load()
		if _, _, err := do(t, ks, "GET", hostURL("127.0.0.1", s, "/"), nil); err != nil {
			t.Fatal(err)
		}
		if got := s.accepted.Load(); got != after {
			t.Errorf("the key service does not reuse its own connection (%d accepted, was %d)", got, after)
		}
		// Closing the idle connections of the guard does not leave the pool of the key service behind.
		g.CloseIdleConnections()
	})

	t.Run("with roots given only those authorities are trusted", func(t *testing.T) {
		a := startTLS(t, srvOpts{})
		b := startTLS(t, srvOpts{}) // another authority
		// The guard trusts B, as upstream.tls.ca_file would.
		g := newGuard(t, egress.Options{RootCAs: b.pool()})
		ks := g.KeyService(a.pool())
		if status, _, err := do(t, ks, "GET", hostURL("127.0.0.1", a, "/"), nil); err != nil || status != 200 {
			t.Errorf("a certificate of the authority given: %d %v", status, err)
		}
		_, _, err := do(t, ks, "GET", hostURL("127.0.0.1", b, "/"), nil)
		var cve *tls.CertificateVerificationError
		if !errors.As(err, &cve) {
			t.Errorf("a certificate of the authority that the guard trusts: %v, want a certificate verification error", err)
		}
		if n := b.requests.Load(); n != 0 {
			t.Errorf("%d requests reached a server of another authority", n)
		}
		// Without roots the roots of the guard are used.
		if status, _, err := do(t, g.KeyService(nil), "GET", hostURL("127.0.0.1", b, "/"), nil); err != nil || status != 200 {
			t.Errorf("without roots, a certificate of the guard's authority: %d %v", status, err)
		}
		if _, _, err := do(t, g.KeyService(nil), "GET", hostURL("127.0.0.1", a, "/"), nil); err == nil {
			t.Errorf("without roots, a certificate of an authority nobody trusts was accepted")
		}
	})

	t.Run("another name, an expired certificate and TLS 1.1 are refused", func(t *testing.T) {
		other := startTLS(t, srvOpts{dns: []string{"other.example.test"}, ips: []net.IP{net.ParseIP("10.9.9.9")}})
		_, _, err := do(t, newGuard(t, egress.Options{}).KeyService(other.pool()), "GET", hostURL("127.0.0.1", other, "/"), nil)
		var he x509.HostnameError
		if !errors.As(err, &he) {
			t.Errorf("a certificate for another name: %v, want a host name error", err)
		}

		old := startTLS(t, srvOpts{expired: true})
		_, _, err = do(t, newGuard(t, egress.Options{}).KeyService(old.pool()), "GET", hostURL("127.0.0.1", old, "/"), nil)
		var ci x509.CertificateInvalidError
		if !errors.As(err, &ci) || ci.Reason != x509.Expired {
			t.Errorf("an expired certificate: %v, want an expired certificate error", err)
		}

		legacy := startTLS(t, srvOpts{tls: func(c *tls.Config) { c.MinVersion, c.MaxVersion = tls.VersionTLS10, tls.VersionTLS11 }})
		if status, _, err := do(t, newGuard(t, egress.Options{}).KeyService(legacy.pool()), "GET", hostURL("127.0.0.1", legacy, "/"), nil); err == nil {
			t.Errorf("a TLS 1.1 server answered %d", status)
		}
		for _, s := range []*srv{other, old, legacy} {
			if n := s.requests.Load(); n != 0 {
				t.Errorf("%d requests reached a handler through a connection that must be refused", n)
			}
		}
	})

	t.Run("it uses no proxy, with a proxy configured and with proxy variables", func(t *testing.T) {
		var contacted atomic.Int64
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				contacted.Add(1)
				_ = c.Close()
			}
		}()
		t.Cleanup(func() { _ = l.Close() })
		proxy := "http://" + l.Addr().String()
		for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
			t.Setenv(k, proxy)
		}
		t.Setenv("NO_PROXY", "")
		t.Setenv("no_proxy", "")
		pu, _ := url.Parse(proxy)

		s, host := nonLoopback(t, nil) // Go never applies a proxy to loopback: a control that proves something
		g := newGuard(t, egress.Options{RootCAs: s.pool(), Proxy: pu})
		if status, _, err := do(t, g.KeyService(nil), "GET", hostURL(host, s, "/"), nil); err != nil || status != 200 {
			t.Errorf("host %s: %d %v", host, status, err)
		}
		if n := contacted.Load(); n != 0 {
			t.Errorf("a proxy was contacted %d times", n)
		}
	})

	t.Run("it follows no redirect", func(t *testing.T) {
		target := startTLS(t, srvOpts{})
		s := startTLS(t, srvOpts{ca: target.ca, handler: func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/elsewhere", http.StatusTemporaryRedirect)
		}})
		g := newGuard(t, egress.Options{RootCAs: s.pool()})
		status, hdr, err := do(t, g.KeyService(nil), "POST", hostURL("127.0.0.1", s, "/v1/transit/encrypt/k"), nil)
		if err != nil || status != http.StatusTemporaryRedirect || hdr.Get("Location") == "" {
			t.Errorf("a redirect: %d %v, want it returned", status, err)
		}
		if n := target.accepted.Load(); n != 0 {
			t.Errorf("the redirect target accepted %d connections", n)
		}
	})

	t.Run("it connects to loopback and private addresses and refuses the metadata address", func(t *testing.T) {
		s := startTLS(t, srvOpts{})
		g := newGuard(t, egress.Options{RootCAs: s.pool()})
		ks := g.KeyService(nil)
		if status, _, err := do(t, ks, "GET", hostURL("127.0.0.1", s, "/"), nil); err != nil || status != 200 {
			t.Errorf("loopback: %d %v", status, err)
		}
		if h, ok := privateHost(t); ok {
			p := startTLS(t, srvOpts{host: h, ips: []net.IP{net.ParseIP(h)}})
			g2 := newGuard(t, egress.Options{RootCAs: p.pool()})
			if status, _, err := do(t, g2.KeyService(nil), "GET", hostURL(h, p, "/"), nil); err != nil || status != 200 {
				t.Errorf("a private address %s: %d %v", h, status, err)
			}
		}
		for _, addr := range []string{"169.254.169.254", "[fd00:ec2::254]", "0.0.0.0", "224.0.0.1"} {
			_, _, err := do(t, ks, "GET", "https://"+addr+":8200/", nil)
			var be *egress.BlockedError
			if !errors.As(err, &be) || be.Reason != egress.BlockForbiddenAddress {
				t.Errorf("%s: %v, want a refusal as a forbidden address", addr, err)
			}
		}
		if n := g.Stats().Blocked[egress.BlockForbiddenAddress]; n < 4 {
			t.Errorf("the refusals are not counted by the guard: %d", n)
		}
		// Credentials in the URL are refused as everywhere.
		if _, _, err := do(t, ks, "GET", "https://user:pw@127.0.0.1:8200/", nil); err == nil {
			t.Errorf("user information in the URL was accepted")
		}
	})

	t.Run("http is for the private class", func(t *testing.T) {
		plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
		t.Cleanup(plain.Close)
		g := newGuard(t, egress.Options{})
		if status, _, err := do(t, g.KeyService(nil), "GET", plain.URL+"/", nil); err != nil || status != 200 {
			t.Errorf("http to loopback: %d %v", status, err)
		}
	})
}

// privateHost returns an address of this machine in a private range, if there is one.
func privateHost(t *testing.T) (string, bool) {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", false
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && ipn.IP.IsPrivate() {
			return ipn.IP.String(), true
		}
	}
	return "", false
}
