package testutil

import (
	"bytes"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/testcert"
)

// The behaviours of PriceServer.
const (
	PriceOK       = "ok"       // 200 with the body
	PriceNotFound = "404"      // 404 with a short text
	PriceRedirect = "redirect" // 302 to another path of the same server
	PriceStall    = "stall"    // accepts the request and never answers
	PriceHuge     = "huge"     // 200 with 16 MiB plus one byte
)

var (
	priceCAOnce sync.Once
	priceCA     *testcert.CA
	priceCAErr  error
)

// priceTestCA returns the certificate authority shared by every PriceServer of the process, and
// therefore the one authority that SSLCertFile writes.
func priceTestCA(tb testing.TB) *testcert.CA {
	tb.Helper()
	priceCAOnce.Do(func() { priceCA, priceCAErr = testcert.NewCA(24 * time.Hour) })
	if priceCAErr != nil {
		tb.Fatal(priceCAErr)
	}
	return priceCA
}

// SSLCertFile writes the PEM certificate of the test authority that signs the certificates of
// PriceServer to a file of tb.TempDir() (mode 0600) and returns its path, for the variable
// SSL_CERT_FILE of a child process (the product reads the system roots, so the child trusts the
// test server without any option in the product).
func SSLCertFile(tb testing.TB) string {
	tb.Helper()
	p := filepath.Join(tb.TempDir(), "price-ca.pem")
	if err := os.WriteFile(p, priceTestCA(tb).CertPEM(), 0o600); err != nil {
		tb.Fatal(err)
	}
	return p
}

// localAddresses returns the loopback addresses and the addresses of the interfaces of the
// machine, which the certificate of the price server names, so that a test can reach the server
// through an address that is not loopback (a proxy setting is never applied to loopback).
func localAddresses() []net.IP {
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			ips = append(ips, n.IP)
		}
	}
	return ips
}

// PriceServer serves body over TLS with a certificate of the test authority (see SSLCertFile)
// and returns the URL of the document. Every path answers the same way, according to behaviour:
// PriceOK, PriceNotFound, PriceRedirect, PriceStall or PriceHuge. The server is closed when the
// test ends, and a stalled request is released first.
func PriceServer(tb testing.TB, body []byte, behaviour string) (documentURL string) {
	tb.Helper()
	documentURL, _ = priceServer(tb, body, behaviour, "127.0.0.1")
	return documentURL
}

// PriceServerOn is PriceServer reached through the given host (an address of the machine that is
// not loopback, from NonLoopbackHost), and also returns a function that lists the headers of
// every request received, in order.
func PriceServerOn(tb testing.TB, body []byte, behaviour, host string) (documentURL string, requests func() []http.Header) {
	tb.Helper()
	return priceServer(tb, body, behaviour, host)
}

// NonLoopbackHost returns an address of this machine that is not loopback, or "" when it has none.
func NonLoopbackHost() string {
	for _, ip := range localAddresses() {
		if !ip.IsLoopback() && ip.To4() != nil {
			return ip.String()
		}
	}
	return ""
}

func priceServer(tb testing.TB, body []byte, behaviour, host string) (string, func() []http.Header) {
	tb.Helper()
	var mu sync.Mutex
	var seen []http.Header
	done := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		switch behaviour {
		case PriceOK:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		case PriceNotFound:
			http.Error(w, "not found", http.StatusNotFound)
		case PriceRedirect:
			if r.URL.Path == "/elsewhere" {
				_, _ = w.Write(body)
				return
			}
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		case PriceStall:
			select {
			case <-done:
			case <-r.Context().Done():
			}
		case PriceHuge:
			w.Header().Set("Content-Type", "application/json")
			chunk := bytes.Repeat([]byte(" "), 1<<20)
			for i := 0; i < 16; i++ {
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
			_, _ = w.Write([]byte(" "))
		default:
			tb.Errorf("testutil.PriceServer: unknown behaviour %q", behaviour)
			http.Error(w, "bad behaviour", http.StatusInternalServerError)
		}
	})
	srv := httptest.NewUnstartedServer(h)
	ca := priceTestCA(tb)
	now := time.Now()
	certPEM, keyPEM, err := ca.Issue([]string{"localhost"}, localAddresses(), now.Add(-time.Minute), now.Add(time.Hour))
	if err != nil {
		tb.Fatal(err)
	}
	dir := tb.TempDir()
	_, certFile, keyFile, err := testcert.WriteFiles(dir, ca, certPEM, keyPEM)
	if err != nil {
		tb.Fatal(err)
	}
	if err := srv.Listener.Close(); err != nil {
		tb.Fatal(err)
	}
	// Listen on every interface so that the machine's own address reaches the server too.
	srv.Listener, err = net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		tb.Fatal(err)
	}
	srv.TLS = serverTLS(tb, certFile, keyFile)
	srv.StartTLS()
	tb.Cleanup(srv.Close)
	tb.Cleanup(func() { close(done) }) // runs first: releases a stalled request
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		tb.Fatal(err)
	}
	u := url.URL{Scheme: "https", Host: net.JoinHostPort(host, port), Path: "/prices/data.json"}
	return u.String(), func() []http.Header {
		mu.Lock()
		defer mu.Unlock()
		return append([]http.Header(nil), seen...)
	}
}

func serverTLS(tb testing.TB, certFile, keyFile string) *tls.Config {
	tb.Helper()
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		tb.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
}
