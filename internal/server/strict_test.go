package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/testutil"
)

// seen is what a recording handler saw of one request.
type seen struct {
	method, target, path, host, body string
	header                           http.Header
}

// recorder is the handler that replaces the gateway and the administration API in the tests of
// the listeners: it records what reached it and answers 200.
type recorder struct {
	mu   sync.Mutex
	runs []seen
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(http.MaxBytesReader(w, req.Body, 1<<20))
	r.mu.Lock()
	r.runs = append(r.runs, seen{method: req.Method, target: req.RequestURI, path: req.URL.Path, host: req.Host, body: string(body), header: req.Header.Clone()})
	r.mu.Unlock()
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ran")
}

func (r *recorder) snapshot() []seen {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]seen(nil), r.runs...)
}

// listeners starts both listeners of the server, built by New from the configuration, with the
// recording handlers in place of the two handlers; it returns the address and the recorder of
// each.
func listeners(t *testing.T, mutate func(*config.Config)) map[string]struct {
	addr string
	rec  *recorder
} {
	t.Helper()
	up := fakeprovider.New(t)
	cfg := testConfig(t)
	if mutate != nil {
		mutate(cfg)
	}
	srv, err := New(newOptions(t, up, cfg, &testutil.LogBuffer{}, &fakeAdmin{}))
	if err != nil {
		t.Fatal(err)
	}
	inf, adm := &recorder{}, &recorder{}
	srv.inf.srv.Handler, srv.adm.srv.Handler = inf, adm
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	return map[string]struct {
		addr string
		rec  *recorder
	}{
		"inference": {srv.InferenceAddr().String(), inf},
		"admin":     {srv.AdminAddr().String(), adm},
	}
}

// answer is one response read from a raw connection.
type answer struct {
	status int
	body   string
	close  bool
}

// exchange writes raw to addr and reads the responses that come back: the first within 2 s, then
// the ones that follow while they arrive within 200 ms of each other. closed reports that the
// server closed the connection.
func exchange(t *testing.T, addr, raw string) (answers []answer, closed bool) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(conn, raw); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	wait := 2 * time.Second
	for len(answers) < 4 {
		_ = conn.SetReadDeadline(time.Now().Add(wait))
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			return answers, !isTimeout(err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		answers = append(answers, answer{status: resp.StatusCode, body: string(body), close: resp.Close})
		wait = 200 * time.Millisecond
	}
	return answers, false
}

// isTimeout reports whether err is the deadline of a read: the connection is still open.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// hidden is a request that an attacker puts where a body is: when it reaches a handler as a
// request of its own, the listener has framed the message differently from RFC 9112.
const hidden = "GET /smuggled HTTP/1.1\r\nHost: x\r\nX-Hidden: yes\r\n\r\n"

// chunk frames s as one chunk of a chunked body.
func chunk(s string) string { return fmt.Sprintf("%x\r\n%s\r\n0\r\n\r\n", len(s), s) }

// TestStrictParsing is T-1103 (sprint 12.2, section 5.8): what the listeners refuse. Each case is
// sent over a raw connection to the inference listener and to the administration listener, built
// by New, whose handlers record what reaches them. A request that carries a second request in its
// body must reach the handler with that text as its body, once, or be answered 400 or 501 and the
// connection closed; in no case does the hidden request reach a handler as a request of its own.
// The answer of each case was taken from a first run on the floor toolchain and on the release
// toolchain of the CI (they agreed) and is fixed in the table.
func TestStrictParsing(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		status int    // of the first answer
		runs   int    // requests that reached the handler
		body   string // the body of the one that did
		host   string // its Host, when it ran
	}{
		{
			name:   "Content-Length and chunked together: the chunked framing decides",
			raw:    "POST /a HTTP/1.1\r\nHost: x\r\nContent-Length: " + fmt.Sprint(len(chunk(hidden))) + "\r\nTransfer-Encoding: chunked\r\n\r\n" + chunk(hidden),
			status: 200, runs: 1, body: hidden, host: "x",
		},
		{
			name:   "chunked and Content-Length together, a Content-Length that stops short",
			raw:    "POST /a HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\nContent-Length: 4\r\n\r\n" + chunk(hidden),
			status: 200, runs: 1, body: hidden, host: "x",
		},
		{
			name:   "two different Content-Length",
			raw:    "POST /a HTTP/1.1\r\nHost: x\r\nContent-Length: 5\r\nContent-Length: " + fmt.Sprint(len(hidden)) + "\r\n\r\n" + hidden,
			status: 400, runs: 0,
		},
		{
			name:   "a list of Content-Length",
			raw:    "POST /a HTTP/1.1\r\nHost: x\r\nContent-Length: 5, " + fmt.Sprint(len(hidden)) + "\r\n\r\n" + hidden,
			status: 400, runs: 0,
		},
		{
			name:   "a signed Content-Length",
			raw:    "POST /a HTTP/1.1\r\nHost: x\r\nContent-Length: +" + fmt.Sprint(len(hidden)) + "\r\n\r\n" + hidden,
			status: 400, runs: 0,
		},
		{
			name:   "a space before the colon of Content-Length",
			raw:    "POST /a HTTP/1.1\r\nHost: x\r\nContent-Length : " + fmt.Sprint(len(hidden)) + "\r\n\r\n" + hidden,
			status: 400, runs: 0,
		},
		{
			name:   "a space before the colon of Transfer-Encoding",
			raw:    "POST /a HTTP/1.1\r\nHost: x\r\nTransfer-Encoding : chunked\r\nContent-Length: " + fmt.Sprint(len(hidden)) + "\r\n\r\n" + hidden,
			status: 400, runs: 0,
		},
		{
			name:   "a folded header line is joined before it is read",
			raw:    "POST /a HTTP/1.1\r\nHost: x\r\nX-Folded: one\r\n two\r\nContent-Length: " + fmt.Sprint(len(hidden)) + "\r\n\r\n" + hidden,
			status: 200, runs: 1, body: hidden, host: "x",
		},
		{
			name:   "a folded Content-Length is joined before it is read",
			raw:    "POST /a HTTP/1.1\r\nHost: x\r\nContent-Length:\r\n " + fmt.Sprint(len(hidden)) + "\r\n\r\n" + hidden,
			status: 200, runs: 1, body: hidden, host: "x",
		},
		{
			name:   "a bare line feed in the request line",
			raw:    "POST /a HTTP/1.1\nHost: x\r\nContent-Length: " + fmt.Sprint(len(hidden)) + "\r\n\r\n" + hidden,
			status: 200, runs: 1, body: hidden, host: "x",
		},
		{
			name:   "a bare line feed after a header",
			raw:    "POST /a HTTP/1.1\r\nHost: x\nContent-Length: " + fmt.Sprint(len(hidden)) + "\r\n\r\n" + hidden,
			status: 200, runs: 1, body: hidden, host: "x",
		},
		{
			name:   "an absolute URI of another host: the authority of the URI is the host",
			raw:    "POST http://other.example/abs HTTP/1.1\r\nHost: x\r\nContent-Length: " + fmt.Sprint(len(hidden)) + "\r\n\r\n" + hidden,
			status: 200, runs: 1, body: hidden, host: "other.example",
		},
		{
			name:   "Transfer-Encoding gzip",
			raw:    "POST /a HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: gzip\r\n\r\n" + chunk(hidden),
			status: 501, runs: 0,
		},
		{
			name:   "Transfer-Encoding gzip, chunked",
			raw:    "POST /a HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: gzip, chunked\r\n\r\n" + chunk(hidden),
			status: 501, runs: 0,
		},
		{
			name:   "Transfer-Encoding chunked, gzip",
			raw:    "POST /a HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked, gzip\r\n\r\n" + chunk(hidden),
			status: 501, runs: 0,
		},
		{
			name:   "Transfer-Encoding identity",
			raw:    "POST /a HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: identity\r\n\r\n" + chunk(hidden),
			status: 501, runs: 0,
		},
		{
			name:   "no Host header",
			raw:    "POST /a HTTP/1.1\r\nContent-Length: " + fmt.Sprint(len(hidden)) + "\r\n\r\n" + hidden,
			status: 400, runs: 0,
		},
		{
			name:   "the plain request, to show the method works",
			raw:    "POST /a HTTP/1.1\r\nHost: x\r\nContent-Length: " + fmt.Sprint(len(hidden)) + "\r\n\r\n" + hidden,
			status: 200, runs: 1, body: hidden, host: "x",
		},
	}
	for _, name := range []string{"inference", "admin"} {
		t.Run(name, func(t *testing.T) {
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					ls := listeners(t, nil)
					l := ls[name]
					answers, closed := exchange(t, l.addr, c.raw)
					ran := l.rec.snapshot()
					for _, r := range ran {
						if r.path == "/smuggled" || r.header.Get("X-Hidden") != "" {
							t.Fatalf("the request hidden in the body reached the handler as a request of its own: %+v", r)
						}
					}
					if len(answers) == 0 || answers[0].status != c.status {
						t.Fatalf("answers %+v (closed %v), want the first to be %d", answers, closed, c.status)
					}
					if len(ran) != c.runs {
						t.Fatalf("the handler ran %d times, want %d: %+v", len(ran), c.runs, ran)
					}
					if c.runs == 0 {
						if !closed && !answers[0].close {
							t.Errorf("the connection stays open after a refusal: %+v", answers)
						}
						if len(answers) != 1 {
							t.Errorf("%d answers to a refused request: %+v", len(answers), answers)
						}
						return
					}
					if ran[0].body != c.body || ran[0].host != c.host {
						t.Errorf("the handler saw body %q host %q, want %q %q", ran[0].body, ran[0].host, c.body, c.host)
					}
					if len(answers) != 1 {
						t.Errorf("%d answers to one request: %+v", len(answers), answers)
					}
				})
			}
		})
	}
}

// TestHeaderLimits is T-1104 (sprint 12.2, section 5.8): max_header_bytes and read_header_timeout
// on both listeners, with small values. A header block of max_header_bytes plus 8 KiB gets 431 and
// the handler does not run; headers sent one byte at a time past read_header_timeout get the
// connection closed and the handler does not run; a request inside both limits is served.
func TestHeaderLimits(t *testing.T) {
	const maxHeader = 4096
	const headerTimeout = 300 * time.Millisecond
	small := func(c *config.Config) {
		c.Listeners.Inference.MaxHeaderBytes = maxHeader
		c.Listeners.Admin.MaxHeaderBytes = maxHeader
		c.Listeners.Inference.ReadHeaderTimeout = config.Duration(headerTimeout)
		c.Listeners.Admin.ReadHeaderTimeout = config.Duration(headerTimeout)
	}
	for _, name := range []string{"inference", "admin"} {
		t.Run(name, func(t *testing.T) {
			t.Run("a header block inside the limit is served", func(t *testing.T) {
				l := listeners(t, small)[name]
				raw := "GET /a HTTP/1.1\r\nHost: x\r\nX-Pad: " + strings.Repeat("a", maxHeader/2) + "\r\n\r\n"
				answers, _ := exchange(t, l.addr, raw)
				if len(answers) != 1 || answers[0].status != 200 || len(l.rec.snapshot()) != 1 {
					t.Errorf("answers %+v, runs %d", answers, len(l.rec.snapshot()))
				}
			})

			t.Run("max_header_bytes plus 8 KiB gets 431", func(t *testing.T) {
				l := listeners(t, small)[name]
				raw := "GET /a HTTP/1.1\r\nHost: x\r\nX-Pad: " + strings.Repeat("a", maxHeader+8<<10) + "\r\n\r\n"
				answers, closed := exchange(t, l.addr, raw)
				if len(answers) != 1 || answers[0].status != http.StatusRequestHeaderFieldsTooLarge {
					t.Fatalf("answers %+v (closed %v), want one 431", answers, closed)
				}
				if !answers[0].close && !closed {
					t.Error("the connection stays open after a 431")
				}
				if n := len(l.rec.snapshot()); n != 0 {
					t.Errorf("the handler ran %d times", n)
				}
			})

			t.Run("many small headers past the limit get 431", func(t *testing.T) {
				l := listeners(t, small)[name]
				var b strings.Builder
				b.WriteString("GET /a HTTP/1.1\r\nHost: x\r\n")
				for i := 0; b.Len() < maxHeader+8<<10; i++ {
					fmt.Fprintf(&b, "X-H%d: v\r\n", i)
				}
				b.WriteString("\r\n")
				answers, _ := exchange(t, l.addr, b.String())
				if len(answers) != 1 || answers[0].status != http.StatusRequestHeaderFieldsTooLarge || len(l.rec.snapshot()) != 0 {
					t.Errorf("answers %+v, runs %d", answers, len(l.rec.snapshot()))
				}
			})

			t.Run("headers sent one byte at a time past read_header_timeout close the connection", func(t *testing.T) {
				l := listeners(t, small)[name]
				conn, err := net.DialTimeout("tcp", l.addr, 2*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				head := "GET /a HTTP/1.1\r\nHost: x\r\nX-Slow: " + strings.Repeat("s", 200) // never finished
				start := time.Now()
				var writeErr error
				for i := 0; i < len(head) && writeErr == nil && time.Since(start) < 3*time.Second; i++ {
					_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
					_, writeErr = conn.Write([]byte{head[i]})
					time.Sleep(20 * time.Millisecond)
				}
				// Whether or not a write failed, the server must have closed the connection: a read
				// ends with the end of the stream (an answer, such as 408, may come first).
				_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				data, err := io.ReadAll(conn)
				if err != nil && isTimeout(err) {
					t.Fatalf("the connection is still open %v after the first byte (read %q)", time.Since(start), data)
				}
				if took := time.Since(start); took > 5*time.Second {
					t.Errorf("the connection was closed after %v", took)
				}
				if got := string(data); strings.Contains(got, " 200 ") {
					t.Errorf("an answer to a request that never finished its headers: %q", got)
				}
				if n := len(l.rec.snapshot()); n != 0 {
					t.Errorf("the handler ran %d times", n)
				}
			})
		})
	}
}
