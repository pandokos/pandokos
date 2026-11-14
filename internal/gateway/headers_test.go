package gateway_test

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/family/openai"
	"github.com/pandokos/pandokos/test/fixtures"
)

// TestRequestHeaders is T-431: the outbound header is built from an empty one (section 5.3). The
// lists range over the adapter's own, and a header the test does not know about fails it.
func TestRequestHeaders(t *testing.T) {
	adapter := openai.Adapter()
	allowed := map[string][]string{}
	for _, n := range adapter.RequestHeaders() {
		allowed[http.CanonicalHeaderKey(n)] = []string{"synthetic-" + strings.ToLower(n) + "-1", "synthetic-" + strings.ToLower(n) + "-2"}
	}
	for _, n := range []string{"Accept", "Content-Type", "User-Agent", "Openai-Organization", "Openai-Project", "Openai-Beta"} {
		if _, ok := allowed[n]; !ok {
			t.Fatalf("the adapter does not list %s", n)
		}
	}
	dropped := map[string]string{
		"Cookie": "session=synthetic", "X-Api-Key": "synthetic", "X-Forwarded-For": "1.2.3.4", "X-Forwarded-Host": "caller.example",
		"X-Forwarded-Proto": "https", "Forwarded": "for=1.2.3.4", "Accept-Encoding": "gzip", "Idempotency-Key": "synthetic-idem",
		"X-Stainless-Lang": "go", "X-Stainless-Retry-Count": "0", "Traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		"Tracestate": "k=v", "Proxy-Authorization": "Basic synthetic", "X-Custom": "v", "Keep-Alive": "timeout=5", "Proxy-Connection": "keep-alive",
		"Te": "trailers", "Upgrade": "websocket", "Referer": "https://caller.example", "Origin": "https://caller.example",
		"X-Pandokos-Request-Id": "caller-chosen", "X-Pandokos-Error": "spoofed",
	}

	h := newHarness(t, nil)
	hdr := http.Header{"Authorization": {"Bearer " + testKey}}
	for n, vs := range allowed {
		if n == "Content-Type" {
			vs = vs[:1]
		}
		hdr[n] = vs
	}
	hdr.Set("Content-Type", "application/json")
	allowed["Content-Type"] = []string{"application/json"}
	for n, v := range dropped {
		hdr.Set(n, v)
	}
	body := chat("fake-basic", true)
	c := h.send(t, http.MethodPost, route, body, hdr)
	if c.status != 200 {
		t.Fatalf("status %d %s", c.status, c.body)
	}
	reqs := h.up.Requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	got := reqs[0]

	for n, want := range allowed {
		if n == "User-Agent" {
			if g := got.Header.Values(n); len(g) != 1 || g[0] != want[0] {
				t.Errorf("User-Agent: %v, want %v", g, want[:1])
			}
			continue
		}
		g := got.Header.Values(n)
		if strings.Join(g, "|") != strings.Join(want, "|") {
			t.Errorf("%s: %v, want every value of the caller: %v", n, g, want)
		}
	}
	for n := range dropped {
		if v := got.Header.Values(n); len(v) != 0 {
			t.Errorf("%s reached the provider: %v", n, v)
		}
	}
	if v := got.Header.Values("Authorization"); len(v) != 1 || v[0] != "Bearer "+fixtures.CanaryProviderKey {
		t.Errorf("Authorization: %v, want the credential of the tenant", v)
	}
	// Nothing else: every header the provider saw is allowed, the credential, or the transport's own.
	own := map[string]bool{"Authorization": true, "Content-Length": true, "Accept-Encoding": false}
	var extra []string
	for n := range got.Header {
		if _, ok := allowed[n]; !ok && !own[n] {
			extra = append(extra, n)
		}
	}
	sort.Strings(extra)
	if len(extra) != 0 {
		t.Errorf("headers the provider received that no list explains: %v", extra)
	}
	if got.Header.Get("Accept-Encoding") != "" {
		t.Errorf("compression was requested: %q", got.Header.Get("Accept-Encoding"))
	}
	u, _ := url.Parse(h.up.URL())
	if got.Host != u.Host {
		t.Errorf("Host = %q, want the endpoint's %q", got.Host, u.Host)
	}
	// The prepared body carries the usage option: Content-Length is its length, not the caller's.
	wantLen := len(body) - 1 + len(`,"stream_options":{"include_usage":true}}`)
	if got.ContentLength != int64(wantLen) || len(got.Body) != wantLen {
		t.Errorf("Content-Length %d, body %d bytes, want %d", got.ContentLength, len(got.Body), wantLen)
	}
	if got.Header.Get("Content-Length") != "" && got.Header.Get("Content-Length") != strconv.Itoa(wantLen) {
		t.Errorf("Content-Length header %q", got.Header.Get("Content-Length"))
	}

	t.Run("a header named in Connection is dropped even when allow-listed", func(t *testing.T) {
		h := newHarness(t, nil)
		hdr := authed()
		hdr.Set("User-Agent", "synthetic-agent/1.0")
		hdr.Set("Openai-Project", "proj-1")
		hdr.Set("X-Custom", "v")
		hdr.Set("Connection", "User-Agent, X-Custom")
		if c := h.send(t, http.MethodPost, route, chat("fake-basic", false), hdr); c.status != 200 {
			t.Fatalf("status %d", c.status)
		}
		g := h.up.Requests()[0].Header
		if v := g.Values("User-Agent"); len(v) != 0 && v[0] == "synthetic-agent/1.0" {
			t.Errorf("User-Agent was forwarded although named in Connection: %v", v)
		}
		if v := g.Values("X-Custom"); len(v) != 0 {
			t.Errorf("X-Custom reached the provider: %v", v)
		}
		if g.Get("Openai-Project") != "proj-1" {
			t.Errorf("a header that is not named in Connection was dropped: %v", g)
		}
		if v := g.Values("Connection"); len(v) != 0 && strings.Contains(strings.Join(v, ","), "X-Custom") {
			t.Errorf("the caller's Connection header was forwarded: %v", v)
		}
	})

	t.Run("a request without allowed headers sends only the credential and the framing", func(t *testing.T) {
		h := newHarness(t, nil)
		hdr := http.Header{"Authorization": {"Bearer " + testKey}}
		if c := h.send(t, http.MethodPost, route, chat("fake-basic", false), hdr); c.status != 200 {
			t.Fatalf("status %d", c.status)
		}
		for n := range h.up.Requests()[0].Header {
			switch n {
			case "Authorization", "Content-Length", "User-Agent":
			default:
				t.Errorf("the provider received the header %s", n)
			}
		}
	})
}
