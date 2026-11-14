package catalog

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/buildinfo"
	"github.com/pandokos/pandokos/test/testutil"
)

// testRoots trusts the certificate authority of testutil.PriceServer. The product reads the
// system roots; the unexported roots option is how the unit test substitutes them (the
// end-to-end tests use SSL_CERT_FILE instead).
func testRoots(t *testing.T) *x509.CertPool {
	t.Helper()
	pem, err := os.ReadFile(testutil.SSLCertFile(t))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("the test certificate authority is not PEM")
	}
	return pool
}

func fetchClass(t *testing.T, err error) string {
	t.Helper()
	var fe *FetchError
	if !errors.As(err, &fe) {
		t.Fatalf("error %v is not a *FetchError", err)
	}
	return fe.Class
}

// TestFetch is T-386: the bounded download of the price dataset.
func TestFetch(t *testing.T) {
	ctx := context.Background()
	body := []byte(`[{"id":"synthetic","models":[]}]`)

	t.Run("200 is accepted and returned whole", func(t *testing.T) {
		u := testutil.PriceServer(t, body, testutil.PriceOK)
		got, err := Fetch(ctx, u, FetchOptions{roots: testRoots(t)})
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(body) {
			t.Errorf("body %q", got)
		}
	})

	t.Run("the failure classes", func(t *testing.T) {
		cases := []struct {
			name      string
			behaviour string
			timeout   time.Duration
			class     string
		}{
			{"404", testutil.PriceNotFound, 0, "status"},
			{"a redirect is not followed", testutil.PriceRedirect, 0, "redirect"},
			{"a stall past the timeout", testutil.PriceStall, 300 * time.Millisecond, "timeout"},
			{"a body over 16 MiB", testutil.PriceHuge, 0, "too_large"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				u := testutil.PriceServer(t, body, c.behaviour)
				start := time.Now()
				got, err := Fetch(ctx, u, FetchOptions{roots: testRoots(t), Timeout: c.timeout})
				if err == nil {
					t.Fatalf("accepted: %d bytes", len(got))
				}
				if class := fetchClass(t, err); class != c.class {
					t.Errorf("class %q, want %q (%v)", class, c.class, err)
				}
				if len(got) != 0 {
					t.Errorf("a failed fetch returned %d bytes", len(got))
				}
				if time.Since(start) > 10*time.Second {
					t.Errorf("took %s", time.Since(start))
				}
				if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "elsewhere") {
					t.Errorf("the error carries content of the response: %v", err)
				}
			})
		}
	})

	t.Run("a refused connection", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		l.Close()
		_, err = Fetch(ctx, "https://"+addr+"/data.json", FetchOptions{roots: testRoots(t), Timeout: 5 * time.Second})
		if class := fetchClass(t, err); class != "unreachable" {
			t.Errorf("class %q (%v)", class, err)
		}
	})

	t.Run("a certificate nobody trusts is unreachable", func(t *testing.T) {
		u := testutil.PriceServer(t, body, testutil.PriceOK)
		// Empty roots: the system pool of the machine does not contain the test authority.
		_, err := Fetch(ctx, u, FetchOptions{Timeout: 5 * time.Second})
		if class := fetchClass(t, err); class != "unreachable" {
			t.Errorf("class %q (%v)", class, err)
		}
	})

	t.Run("exactly 16 MiB is accepted", func(t *testing.T) {
		pad := make([]byte, 16<<20-2)
		for i := range pad {
			pad[i] = ' '
		}
		doc := append(append([]byte("["), pad...), ']')
		if len(doc) != 16<<20 {
			t.Fatalf("%d bytes", len(doc))
		}
		u := testutil.PriceServer(t, doc, testutil.PriceOK)
		got, err := Fetch(ctx, u, FetchOptions{roots: testRoots(t), Timeout: 30 * time.Second})
		if err != nil || len(got) != 16<<20 {
			t.Fatalf("%d bytes, %v", len(got), err)
		}
	})

	t.Run("a cancelled context stops the download", func(t *testing.T) {
		u := testutil.PriceServer(t, body, testutil.PriceStall)
		cctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := Fetch(cctx, u, FetchOptions{roots: testRoots(t)})
		if err == nil {
			t.Fatal("accepted")
		}
		var fe *FetchError
		if !errors.As(err, &fe) {
			t.Fatalf("%v is not a FetchError", err)
		}
		if time.Since(start) > 5*time.Second {
			t.Errorf("took %s", time.Since(start))
		}
	})

	t.Run("an http URL is refused before any connection", func(t *testing.T) {
		if _, err := Fetch(ctx, "http://example.invalid/data.json", FetchOptions{}); err == nil {
			t.Error("accepted")
		}
	})

	t.Run("only the two headers are sent", func(t *testing.T) {
		u, requests := testutil.PriceServerOn(t, body, testutil.PriceOK, "127.0.0.1")
		if _, err := Fetch(ctx, u, FetchOptions{roots: testRoots(t)}); err != nil {
			t.Fatal(err)
		}
		reqs := requests()
		if len(reqs) != 1 {
			t.Fatalf("%d requests", len(reqs))
		}
		h := reqs[0]
		if h.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q", h.Get("Accept"))
		}
		if want := "pandokos/" + buildinfo.Version; h.Get("User-Agent") != want {
			t.Errorf("User-Agent = %q, want %q", h.Get("User-Agent"), want)
		}
		// Only Accept and User-Agent: nothing the operator's installation could leak (cookies,
		// authorization, identifiers), and no Accept-Encoding that Go's transport would add.
		for k := range h {
			switch k {
			case "Accept", "User-Agent":
			default:
				t.Errorf("an extra header was sent: %s", k)
			}
		}
	})

	t.Run("a response byte is never in an error", func(t *testing.T) {
		const marker = "synthetic-marker-response-byte"
		u := testutil.PriceServer(t, []byte(marker), testutil.PriceNotFound)
		_, err := Fetch(ctx, u, FetchOptions{roots: testRoots(t)})
		if err == nil || strings.Contains(err.Error(), marker) {
			t.Fatalf("%v", err)
		}
		u = testutil.PriceServer(t, []byte(marker), testutil.PriceRedirect)
		_, err = Fetch(ctx, u, FetchOptions{roots: testRoots(t)})
		if err == nil || strings.Contains(err.Error(), marker) {
			t.Fatalf("%v", err)
		}
	})

	t.Run("HTTPS_PROXY and HTTP_PROXY are not used", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		contacted := make(chan struct{}, 1)
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				select {
				case contacted <- struct{}{}:
				default:
				}
				c.Close()
			}
		}()
		t.Cleanup(func() { l.Close() })
		proxy := "http://" + l.Addr().String()
		for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy", "ALL_PROXY", "all_proxy"} {
			t.Setenv(k, proxy)
		}
		t.Setenv("NO_PROXY", "")
		t.Setenv("no_proxy", "")
		// Go never proxies a loopback destination, so the server is reached through an address
		// of the machine that is not loopback when there is one; otherwise through loopback.
		host := testutil.NonLoopbackHost()
		if host == "" {
			host = "127.0.0.1"
		}
		u, _ := testutil.PriceServerOn(t, body, testutil.PriceOK, host)
		if _, err := Fetch(ctx, u, FetchOptions{roots: testRoots(t), Timeout: 5 * time.Second}); err != nil {
			t.Fatalf("a fetch with a proxy in the environment: %v", err)
		}
		select {
		case <-contacted:
			t.Error("the proxy of the environment was contacted")
		default:
		}
	})

	t.Run("FetchError says only its class", func(t *testing.T) {
		for _, class := range []string{"unreachable", "timeout", "status", "redirect", "too_large", "dataset_invalid"} {
			err := &FetchError{Class: class}
			if !strings.Contains(err.Error(), class) {
				t.Errorf("%q does not name its class", err.Error())
			}
		}
	})

}
