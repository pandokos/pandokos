package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/egress"
)

// countingTransport wraps a round tripper and counts the bytes of the responses that the caller
// reads: the measure of "never read past 64 KiB".
type countingTransport struct {
	rt   http.RoundTripper
	read atomic.Int64
}

type countingBody struct {
	io.ReadCloser
	n *atomic.Int64
}

func (b countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n.Add(int64(n))
	return n, err
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.rt.RoundTrip(r)
	if resp != nil {
		resp.Body = countingBody{resp.Body, &c.read}
	}
	return resp, err
}

// local is a server of the machine that counts its connections and answers with handler; its URL
// names the host localhost, which the deliverers of these tests list as private.
type local struct {
	*httptest.Server
	conns atomic.Int64
	reqs  atomic.Int64
}

func newLocal(t *testing.T, handler http.HandlerFunc, tls bool) *local {
	t.Helper()
	l := &local{}
	l.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.reqs.Add(1)
		handler(w, r)
	}))
	l.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			l.conns.Add(1)
		}
	}
	if tls {
		l.StartTLS()
	} else {
		l.Start()
	}
	t.Cleanup(l.Close)
	return l
}

func (l *local) url(path string) string {
	return strings.Replace(l.URL, "127.0.0.1", "localhost", 1) + path
}

func testDeliverer(t *testing.T, timeout time.Duration) (*Deliverer, *countingTransport) {
	t.Helper()
	g, err := egress.New(egress.Options{ConnectTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.CloseIdleConnections)
	ct := &countingTransport{rt: g.Transport(true)}
	pub := g.Transport(false)
	return &Deliverer{
		Transport: func(private bool) http.RoundTripper {
			if private {
				return ct
			}
			return pub
		},
		PrivateHosts: []string{"localhost"}, Timeout: timeout, UserAgent: "pandokos/test",
		Now: func() time.Time { return time.Date(2026, 11, 23, 17, 20, 4, 0, time.UTC) },
	}, ct
}

var testEnv = func(t *testing.T) Envelope {
	return Envelope{Source: "urn:pandokos", TenantID: tenantID(t), TenantRef: "cust-42"}
}

func testDoc() Event {
	return Event{Seq: 3, ID: testEvent, Type: "io.pandokos.usage.recorded.v1", Subject: "eccbc87e4b5ce2fe28308fd9f2a7baf3", Time: at, Data: json.RawMessage(`{"model": "gpt-4o-mini"}`)}
}

// TestDeliver is T-1029: against local servers named as private hosts, each outcome of section
// 5.7; a redirect is not followed; the deadline; a body of 1 MiB is not read past 64 KiB; the
// headers are exactly those of section 5.9; an address that the guard refuses is blocked before any
// connection.
func TestDeliver(t *testing.T) {
	key := mustKey(t, secretText(5))

	t.Run("a 2xx answer is delivered", func(t *testing.T) {
		d, _ := testDeliverer(t, 2*time.Second)
		for _, code := range []int{200, 201, 202, 204, 299} {
			srv := newLocal(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }, false)
			if got := d.Send(context.Background(), srv.url("/hook"), testDoc(), testEnv(t), key); got.Outcome != OutcomeDelivered || got.Status != code || got.Duration <= 0 {
				t.Errorf("%d: %+v", code, got)
			}
		}
	})

	t.Run("any other status is a status, a 3xx a redirect that is not followed", func(t *testing.T) {
		d, _ := testDeliverer(t, 2*time.Second)
		for _, code := range []int{400, 401, 404, 410, 429, 500, 502, 503, 199} {
			srv := newLocal(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }, false)
			if got := d.Send(context.Background(), srv.url("/"), testDoc(), testEnv(t), key); got.Outcome != OutcomeStatus || got.Status != code {
				t.Errorf("%d: %+v", code, got)
			}
		}
		target := newLocal(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }, false)
		for _, code := range []int{301, 302, 303, 307, 308} {
			srv := newLocal(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.url("/elsewhere"), code)
			}, false)
			got := d.Send(context.Background(), srv.url("/"), testDoc(), testEnv(t), key)
			if got.Outcome != OutcomeRedirect || got.Status != code {
				t.Errorf("%d: %+v, want a redirect with its status", code, got)
			}
		}
		if n := target.reqs.Load(); n != 0 {
			t.Errorf("the target of the redirects got %d requests: a redirect is never followed", n)
		}
	})

	t.Run("no complete answer within the deadline is a timeout", func(t *testing.T) {
		d, _ := testDeliverer(t, 200*time.Millisecond)
		release := make(chan struct{})
		srv := newLocal(t, func(http.ResponseWriter, *http.Request) { <-release }, false)
		defer close(release)
		got := d.Send(context.Background(), srv.url("/"), testDoc(), testEnv(t), key)
		if got.Outcome != OutcomeTimeout || got.Duration < 150*time.Millisecond || got.Duration > 1500*time.Millisecond {
			t.Errorf("%+v, want a timeout after about 200 ms", got)
		}
		// The deadline covers the answer too: headers that arrive and a body that never ends.
		slow := newLocal(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-release
		}, false)
		if got := d.Send(context.Background(), slow.url("/"), testDoc(), testEnv(t), key); got.Outcome != OutcomeTimeout && got.Outcome != OutcomeDelivered {
			t.Errorf("a stalled body: %+v", got)
		}
	})

	t.Run("a refused or cut connection is a connection, an untrusted certificate a tls", func(t *testing.T) {
		d, _ := testDeliverer(t, 2*time.Second)
		closed := newLocal(t, func(http.ResponseWriter, *http.Request) {}, false)
		url := closed.url("/")
		closed.Close()
		if got := d.Send(context.Background(), url, testDoc(), testEnv(t), key); got.Outcome != OutcomeConnection || got.Status != 0 {
			t.Errorf("a closed port: %+v", got)
		}
		cut := newLocal(t, func(w http.ResponseWriter, _ *http.Request) {
			c, _, _ := w.(http.Hijacker).Hijack()
			_ = c.Close()
		}, false)
		if got := d.Send(context.Background(), cut.url("/"), testDoc(), testEnv(t), key); got.Outcome != OutcomeConnection {
			t.Errorf("a connection cut after the request: %+v", got)
		}
		secure := newLocal(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }, true)
		if got := d.Send(context.Background(), secure.url("/"), testDoc(), testEnv(t), key); got.Outcome != OutcomeTLS || secure.reqs.Load() != 0 {
			t.Errorf("a certificate that no root signed: %+v, %d requests reached the handler", got, secure.reqs.Load())
		}
	})

	t.Run("a body of 1 MiB is not read past 64 KiB", func(t *testing.T) {
		d, ct := testDeliverer(t, 3*time.Second)
		srv := newLocal(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "1048576")
			w.WriteHeader(200)
			chunk := bytes.Repeat([]byte("x"), 32<<10)
			for i := 0; i < 32; i++ {
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
		}, false)
		got := d.Send(context.Background(), srv.url("/"), testDoc(), testEnv(t), key)
		if got.Outcome != OutcomeDelivered || got.Status != 200 {
			t.Errorf("%+v", got)
		}
		if n := ct.read.Load(); n > 64<<10 {
			t.Errorf("%d bytes of the response were read, want at most 64 KiB", n)
		}
	})

	t.Run("the request is exactly the one of section 5.9", func(t *testing.T) {
		d, _ := testDeliverer(t, 2*time.Second)
		var got http.Header
		var method, path, host string
		var body []byte
		srv := newLocal(t, func(w http.ResponseWriter, r *http.Request) {
			got, method, path, host = r.Header.Clone(), r.Method, r.URL.RequestURI(), r.Host
			body, _ = io.ReadAll(r.Body)
			w.WriteHeader(204)
		}, false)
		res := d.Send(context.Background(), srv.url("/in/hook?x=1"), testDoc(), testEnv(t), key)
		if res.Outcome != OutcomeDelivered {
			t.Fatalf("%+v", res)
		}
		if method != "POST" || path != "/in/hook?x=1" || !strings.HasPrefix(host, "localhost:") {
			t.Errorf("%s %s to %s", method, path, host)
		}
		if want := Render(testDoc(), testEnv(t)); !bytes.Equal(body, want) {
			t.Errorf("the body is\n%s\nwant the rendering\n%s", body, want)
		}
		now := d.Now()
		sig := reference(5, testEvent, now.Unix(), body)
		for name, want := range map[string]string{
			"Content-Type": "application/cloudevents+json; charset=utf-8", "User-Agent": "pandokos/test", "Webhook-Id": testEvent,
			"Webhook-Timestamp": "", "Webhook-Signature": sig,
		} {
			if name == "Webhook-Timestamp" {
				want = itoa(now.Unix())
			}
			if g := got.Get(name); g != want {
				t.Errorf("%s is %q, want %q", name, g, want)
			}
		}
		var names []string
		for k := range got {
			names = append(names, k)
		}
		sort.Strings(names)
		allowed := map[string]bool{"Content-Type": true, "User-Agent": true, "Webhook-Id": true, "Webhook-Timestamp": true, "Webhook-Signature": true,
			"Content-Length": true, "Accept-Encoding": true} // the last two are the transport's own
		for _, n := range names {
			if !allowed[n] {
				t.Errorf("the request carries the header %s: %v", n, names)
			}
		}
		for _, banned := range []string{"Authorization", "Cookie", "Proxy-Authorization", "X-Forwarded-For", "Referer"} {
			if got.Get(banned) != "" {
				t.Errorf("the request carries %s", banned)
			}
		}
		// Two keys: two signatures in the header.
		other := mustKey(t, secretText(6))
		d.Send(context.Background(), srv.url("/"), testDoc(), testEnv(t), key, other)
		if s := got.Get("Webhook-Signature"); len(strings.Fields(s)) != 2 || !strings.HasPrefix(s, "v1,") || !strings.Contains(s, " v1,") {
			t.Errorf("the signature header with two keys is %q", s)
		}
	})

	t.Run("an address the guard refuses is blocked before any connection", func(t *testing.T) {
		g, err := egress.New(egress.Options{ConnectTimeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		d := &Deliverer{Transport: g.Transport, PrivateHosts: nil, Timeout: time.Second, UserAgent: "pandokos/test", Now: time.Now}
		srv := newLocal(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }, false)
		_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
		for what, url := range map[string]string{
			"a local server that is not listed, over http":          srv.url("/"),
			"a loopback literal over https":                         "https://127.0.0.1:" + port + "/",
			"an IPv6 loopback literal":                              "https://[::1]:" + port + "/",
			"the address of the metadata service":                   "https://169.254.169.254/latest/meta-data",
			"a private network address":                             "https://10.0.0.1/",
			"an http URL":                                           "http://hooks.example.test/",
			"user information":                                      "https://user:pass@hooks.example.test/",
			"a local server listed as private, with the wrong name": "http://127.0.0.1:" + port + "/",
		} {
			if got := d.Send(context.Background(), url, testDoc(), testEnv(t), key); got.Outcome != OutcomeBlocked || got.Status != 0 {
				t.Errorf("%s: %+v, want blocked", what, got)
			}
		}
		if n := srv.conns.Load(); n != 0 {
			t.Errorf("%d connections reached the local server: a blocked address is refused before any connection", n)
		}
	})

	t.Run("the guard that is listed as private takes a private address, and only for the host listed", func(t *testing.T) {
		d, _ := testDeliverer(t, time.Second)
		srv := newLocal(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }, false)
		if got := d.Send(context.Background(), srv.url("/"), testDoc(), testEnv(t), key); got.Outcome != OutcomeDelivered {
			t.Errorf("the listed host: %+v", got)
		}
		d.PrivateHosts = []string{"another.example.test"}
		if got := d.Send(context.Background(), srv.url("/"), testDoc(), testEnv(t), key); got.Outcome != OutcomeBlocked {
			t.Errorf("a host that is no longer listed: %+v", got)
		}
	})
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// TestValidateURL is the table of section 5.5.
func TestValidateURL(t *testing.T) {
	private := []string{"localhost", "hooks.internal.example"}
	for _, ok := range []string{
		"https://hooks.example.test/in", "https://hooks.example.test:8443/a/b?c=d", "https://example.test", "https://93.184.216.34:8443/in",
		"http://localhost:8080/in", "http://hooks.internal.example/in", "https://localhost/in",
	} {
		if err := ValidateURL(ok, private); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for what, bad := range map[string]string{
		"an http URL for a host that is not listed": "http://hooks.example.test/in",
		"ftp":                       "ftp://hooks.example.test/in",
		"no scheme":                 "hooks.example.test/in",
		"no host":                   "https:///in",
		"user information":          "https://user@hooks.example.test/in",
		"user and password":         "https://user:pass@hooks.example.test/in",
		"a fragment":                "https://hooks.example.test/in#frag",
		"an empty string":           "",
		"a space":                   "https://hooks.example.test/a b",
		"a newline":                 "https://hooks.example.test/a\nb",
		"2049 bytes":                "https://hooks.example.test/" + strings.Repeat("a", 2049-len("https://hooks.example.test/")),
		"the metadata address":      "https://169.254.169.254/latest",
		"a loopback literal":        "https://127.0.0.1/in",
		"a private literal":         "https://10.1.2.3/in",
		"an http literal":           "http://127.0.0.1/in",
		"an IPv6 loopback":          "https://[::1]/in",
		"an IPv4-mapped loopback":   "https://[::ffff:127.0.0.1]/in",
		"an opaque URL":             "mailto:someone@example.test",
		"a scheme in capitals only": "HTTPS://",
	} {
		if err := ValidateURL(bad, private); err == nil {
			t.Errorf("%s (%s): accepted", what, bad)
		} else if bad != "" && strings.Contains(err.Error(), "pass") {
			t.Errorf("%s: the error quotes the URL: %v", what, err)
		}
	}
	// The exact 2048-byte URL is valid.
	exact := "https://hooks.example.test/" + strings.Repeat("a", 2048-len("https://hooks.example.test/"))
	if err := ValidateURL(exact, nil); err != nil {
		t.Errorf("2048 bytes: %v", err)
	}
	if !reflect.DeepEqual(private, []string{"localhost", "hooks.internal.example"}) {
		t.Error("ValidateURL changed the list of private hosts")
	}
}
