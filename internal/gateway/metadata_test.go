package gateway_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/test/testutil"
)

// metaCanary is a metadata value that must be found on the record and nowhere else.
const metaCanary = "synthetic-metadata-canary-77aa"

func withHeaders(extra http.Header) http.Header {
	h := authed()
	for k, v := range extra {
		h[k] = v
	}
	return h
}

// TestMetadata is T-488.
func TestMetadata(t *testing.T) {
	const p = "X-Pandokos-Metadata-"
	t.Run("kept entries are on the record, nothing is dropped", func(t *testing.T) {
		h := newHarness(t, nil)
		c := h.send(t, http.MethodPost, route, chat("fake-basic", false), withHeaders(http.Header{p + "Team": {"alpha"}, p + "Note": {metaCanary}}))
		if c.status != 200 {
			t.Fatalf("%d %s", c.status, c.body)
		}
		r := h.oneRecord(t)
		if len(r.Metadata) != 2 || r.Metadata["team"] != "alpha" || r.Metadata["note"] != metaCanary || r.MetadataDropped != 0 {
			t.Errorf("metadata %v dropped %d", r.Metadata, r.MetadataDropped)
		}
		if v := c.header.Values("X-Pandokos-Metadata-Dropped"); len(v) != 0 {
			t.Errorf("the dropped counter is on the response when nothing was dropped: %v", v)
		}
		line := h.lastRequestLine(t)
		if _, ok := line["metadata_dropped"]; ok {
			t.Errorf("metadata_dropped is logged when nothing was dropped: %v", line)
		}
		if n, ok := line["metadata_count"]; ok && n != float64(2) {
			t.Errorf("metadata_count %v", n)
		}
	})

	t.Run("dropped entries are counted on the record, the response and the log", func(t *testing.T) {
		h := newHarness(t, nil)
		hdr := http.Header{
			p + "Good": {"v"}, p + "-bad": {"v"}, p + "empty": {""}, p + "Dup": {"1", "2"}, p + "Long": {strings.Repeat("x", 200)},
		}
		for _, k := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
			hdr[p+strings.ToUpper(k)] = []string{"v"}
		}
		c := h.send(t, http.MethodPost, route, chat("fake-basic", false), withHeaders(hdr))
		r := h.oneRecord(t)
		// Valid keys in order: a b c d e f g good h; the ninth (h) is dropped. Four more fail a rule.
		if len(r.Metadata) != 8 || r.Metadata["h"] != "" || r.Metadata["good"] != "v" || r.MetadataDropped != 5 {
			t.Errorf("metadata %v dropped %d", r.Metadata, r.MetadataDropped)
		}
		if got := c.header.Get("X-Pandokos-Metadata-Dropped"); got != "5" {
			t.Errorf("the response header says %q, want 5", got)
		}
		line := h.lastRequestLine(t)
		if line["metadata_dropped"] != float64(5) {
			t.Errorf("the log says %v", line["metadata_dropped"])
		}
	})

	t.Run("the dropped counter is on a refusal after authentication too", func(t *testing.T) {
		h := newHarness(t, nil)
		c := h.send(t, http.MethodPost, route, []byte(`{}`), withHeaders(http.Header{p + "-bad": {"v"}}))
		if c.status != 400 || c.header.Get("X-Pandokos-Metadata-Dropped") != "1" {
			t.Errorf("%d %v", c.status, c.header)
		}
	})

	t.Run("the provider receives no X-Pandokos header, also when the caller names one in Connection", func(t *testing.T) {
		h := newHarness(t, nil)
		hdr := withHeaders(http.Header{
			p + "Team": {"alpha"}, p + "Dropped": {"9"}, "X-Pandokos-Request-Id": {"x"}, "X-Pandokos-Other": {"y"},
			"Connection": {"keep-alive, X-Pandokos-Metadata-Team"},
		})
		if c := h.send(t, http.MethodPost, route, chat("fake-basic", false), hdr); c.status != 200 {
			t.Fatalf("%d %s", c.status, c.body)
		}
		reqs := h.up.Requests()
		if len(reqs) != 1 {
			t.Fatalf("%d requests", len(reqs))
		}
		for name, v := range reqs[0].Header {
			if strings.HasPrefix(strings.ToLower(name), "x-pandokos") || strings.Contains(strings.Join(v, ","), "alpha") {
				t.Errorf("the provider received %s: %v", name, v)
			}
		}
	})

	t.Run("values are absent from the logs and the spans", func(t *testing.T) {
		h := newHarness(t, nil)
		h.send(t, http.MethodPost, route, chat("fake-basic", true), withHeaders(http.Header{p + "Note": {metaCanary}}))
		h.send(t, http.MethodPost, route, []byte(`{}`), withHeaders(http.Header{p + "Note": {metaCanary}}))
		h.requestLines(t, 2)
		h.records(t, 2)
		testutil.AssertAbsent(t, "the logs", h.logs.String(), metaCanary)
		testutil.AssertAbsent(t, "the spans", renderSpans(h.spans.Ended()), metaCanary)
	})

	t.Run("a refusal is the same with and without metadata", func(t *testing.T) {
		noRole := principalWith(func(p *authz.Principal) { p.Roles = nil })
		for name, mutate := range map[string]func(*gateway.Options){
			"a key that fails authorization": func(o *gateway.Options) { o.Authenticator = fixedAuth{p: noRole} },
			"a key that fails authentication": func(o *gateway.Options) {
				o.Authenticator = fixedAuth{err: authz.ErrUnauthenticated}
			},
		} {
			h := newHarness(t, mutate)
			plain := h.send(t, http.MethodPost, route, chat("fake-basic", false), authed())
			with := h.send(t, http.MethodPost, route, chat("fake-basic", false), withHeaders(http.Header{p + "Team": {"alpha"}, p + "-bad": {"v"}}))
			if name == "a key that fails authentication" {
				if plain.normalized() != with.normalized() {
					t.Errorf("%s: the answers differ:\n%s\n%s", name, plain.normalized(), with.normalized())
				}
				continue
			}
			// After authentication the caller learns how many entries were dropped, nothing else.
			with.header.Del("X-Pandokos-Metadata-Dropped")
			if plain.normalized() != with.normalized() {
				t.Errorf("%s: the answers differ:\n%s\n%s", name, plain.normalized(), with.normalized())
			}
		}
	})

	t.Run("no decision reads the metadata", func(t *testing.T) {
		h := newHarness(t, nil)
		for _, v := range []string{"allow", "deny", "admin", "model:invoke", "tenant_suspended"} {
			if c := h.send(t, http.MethodPost, route, chat("fake-basic", false), withHeaders(http.Header{p + "Role": {v}, p + "Model": {"nope"}})); c.status != 200 {
				t.Errorf("metadata %q changed the answer: %d", v, c.status)
			}
		}
	})
}

// TestNothingBeforeAuthentication is T-498.
func TestNothingBeforeAuthentication(t *testing.T) {
	const tp = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	noise := http.Header{
		"Traceparent": {tp}, "Tracestate": {"vendor=value"}, "Baggage": {"k=v"},
		"X-Pandokos-Metadata-Team": {"alpha"}, "X-Pandokos-Metadata-" + strings.Repeat("k", 40): {"v"}, "X-Pandokos-Metadata--bad": {"v"},
	}
	for name, hdr := range map[string]http.Header{
		"no credential":          {"Content-Type": {"application/json"}},
		"a key nobody knows":     {"Authorization": {"Bearer " + testutil.NewKey(t)}, "Content-Type": {"application/json"}},
		"a malformed credential": {"Authorization": {"Basic x"}, "Content-Type": {"application/json"}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil)
			plain := h.send(t, http.MethodPost, route, chat("fake-basic", false), hdr)
			with := h.send(t, http.MethodPost, route, chat("fake-basic", false), func() http.Header {
				m := hdr.Clone()
				for k, v := range noise {
					m[k] = v
				}
				return m
			}())
			if plain.status != 401 || plain.normalized() != with.normalized() {
				t.Errorf("the 401 differs with the extra headers:\n%s\n%s", plain.normalized(), with.normalized())
			}
			if v := with.header.Values("X-Pandokos-Metadata-Dropped"); len(v) != 0 {
				t.Errorf("metadata was parsed before authentication: %v", v)
			}
			h.requestLines(t, 2)
			if n := h.rec.Admits(); n != 0 {
				t.Errorf("%d admissions", n)
			}
			if n := len(h.rec.Records()); n != 0 {
				t.Errorf("%d records", n)
			}
			if n := len(h.spans.Started()) + len(h.spans.Ended()); n != 0 {
				t.Errorf("%d spans for a caller that is not authenticated", n)
			}
			for _, l := range h.logs.Find(t, "request") {
				for _, k := range []string{"record_id", "trace_id", "metadata_count", "metadata_dropped", "cost_status"} {
					if _, ok := l[k]; ok {
						t.Errorf("the field %s is logged for an anonymous request", k)
					}
				}
			}
		})
	}
}
