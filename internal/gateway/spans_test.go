package gateway_test

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// renderSpans is everything a span says, as text: name, kind, status, attributes, events, links.
func renderSpans(spans []sdktrace.ReadOnlySpan) string {
	var sb strings.Builder
	for _, s := range spans {
		fmt.Fprintf(&sb, "span %q kind=%v status=%v %q\n", s.Name(), s.SpanKind(), s.Status().Code, s.Status().Description)
		for _, a := range s.Attributes() {
			fmt.Fprintf(&sb, "  %s=%v\n", a.Key, a.Value.String())
		}
		for _, e := range s.Events() {
			fmt.Fprintf(&sb, "  event %q %v\n", e.Name, e.Attributes)
		}
		for _, l := range s.Links() {
			fmt.Fprintf(&sb, "  link %v\n", l.SpanContext.TraceID())
		}
	}
	return sb.String()
}

// attrs returns the attributes of a span by key.
func attrs(s sdktrace.ReadOnlySpan) map[string]attribute.Value {
	m := map[string]attribute.Value{}
	for _, a := range s.Attributes() {
		m[string(a.Key)] = a.Value
	}
	return m
}

func keysOf(m map[string]attribute.Value) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// endedSpans waits for n ended spans and returns the server and the client span (the client one is
// nil when n is 1).
func endedSpans(t *testing.T, h *harness, n int) (server, client sdktrace.ReadOnlySpan) {
	t.Helper()
	testutil.Eventually(t, 5*time.Second, fmt.Sprintf("%d ended spans", n), func() bool { return len(h.spans.Ended()) >= n })
	time.Sleep(30 * time.Millisecond)
	spans := h.spans.Ended()
	if len(spans) != n {
		t.Fatalf("%d spans, want %d:\n%s", len(spans), n, renderSpans(spans))
	}
	for _, s := range spans {
		switch s.SpanKind() {
		case trace.SpanKindServer:
			server = s
		case trace.SpanKindClient:
			client = s
		}
	}
	if server == nil {
		t.Fatal("no server span")
	}
	return server, client
}

const (
	callerTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	callerSpan  = "00f067aa0ba902b7"
	callerTP    = "00-" + callerTrace + "-" + callerSpan + "-01"
)

var serverAttrs = []string{
	"http.request.method", "http.route", "http.response.status_code", "pandokos.request.id", "pandokos.record.id",
	"pandokos.tenant.id", "pandokos.principal.id", "pandokos.key.id", "pandokos.provider", "pandokos.operation", "pandokos.outcome",
}

var clientAttrs = []string{
	"pandokos.provider", "pandokos.endpoint", "pandokos.model", "pandokos.operation", "pandokos.stream", "pandokos.credential.id",
	"pandokos.funding", "http.response.status_code", "pandokos.ttfb_ms", "pandokos.usage.source", "pandokos.cost.status",
	"pandokos.cost.amount", "pandokos.cost.currency",
}

// TestSpans is T-495.
func TestSpans(t *testing.T) {
	seen := map[string]bool{}
	note := func(s sdktrace.ReadOnlySpan) {
		for k := range attrs(s) {
			seen[k] = true
		}
	}

	t.Run("a forwarded request: a server span and a client span", func(t *testing.T) {
		h := newHarness(t, nil)
		c := h.sendAuthed(t, route, chat("fake-basic", false))
		server, client := endedSpans(t, h, 2)
		note(server)
		note(client)
		rec := h.oneRecord(t)
		line := h.lastRequestLine(t)

		if server.Name() != "POST /{provider}/v1/chat/completions" || server.SpanKind() != trace.SpanKindServer {
			t.Errorf("server span %q %v", server.Name(), server.SpanKind())
		}
		if client.Name() != "chat.completions fake-basic" || client.SpanKind() != trace.SpanKindClient {
			t.Errorf("client span %q %v", client.Name(), client.SpanKind())
		}
		if client.Parent().SpanID() != server.SpanContext().SpanID() || client.SpanContext().TraceID() != server.SpanContext().TraceID() {
			t.Error("the client span is not a child of the server span")
		}
		if server.Parent().IsValid() {
			t.Errorf("the server span has a parent without a traceparent: %v", server.Parent())
		}
		if !server.StartTime().Equal(rec.StartedAt) {
			t.Errorf("the server span starts at %v, the request at %v", server.StartTime(), rec.StartedAt)
		}
		if client.StartTime().Before(server.StartTime()) || client.EndTime().After(server.EndTime()) {
			t.Errorf("the client span %v-%v is not inside the server span %v-%v", client.StartTime(), client.EndTime(), server.StartTime(), server.EndTime())
		}
		trace := server.SpanContext().TraceID().String()
		if rec.TraceID != trace || line["trace_id"] != trace {
			t.Errorf("trace IDs: span %s record %s log %v", trace, rec.TraceID, line["trace_id"])
		}

		sa := attrs(server)
		if got, want := keysOf(sa), append([]string(nil), serverAttrs...); !sameSet(got, want) {
			t.Errorf("server attributes %v, want %v", got, want)
		}
		for k, want := range map[string]string{
			"http.request.method": "POST", "http.route": "/{provider}/v1/chat/completions", "pandokos.request.id": c.header.Get("X-Pandokos-Request-Id"),
			"pandokos.record.id": rec.ID, "pandokos.tenant.id": testPrincipal.TenantID.String(), "pandokos.principal.id": testPrincipal.ID,
			"pandokos.key.id": testPrincipal.KeyID, "pandokos.provider": "openai", "pandokos.operation": "chat.completions", "pandokos.outcome": "ok",
		} {
			if sa[k].AsString() != want {
				t.Errorf("server %s = %q, want %q", k, sa[k].AsString(), want)
			}
		}
		if sa["http.response.status_code"].AsInt64() != 200 {
			t.Errorf("server status code %v", sa["http.response.status_code"])
		}

		ca := attrs(client)
		if got := keysOf(ca); !sameSet(got, clientAttrs) {
			t.Errorf("client attributes %v, want %v", got, clientAttrs)
		}
		for k, want := range map[string]string{
			"pandokos.provider": "openai", "pandokos.endpoint": "synthetic-endpoint", "pandokos.model": "fake-basic", "pandokos.operation": "chat.completions",
			"pandokos.credential.id": "synthetic-credential-openai", "pandokos.funding": "byok", "pandokos.usage.source": "provider",
			"pandokos.cost.status": "known", "pandokos.cost.amount": rec.Cost.Amount.String(), "pandokos.cost.currency": "USD",
		} {
			if ca[k].AsString() != want {
				t.Errorf("client %s = %q, want %q", k, ca[k].AsString(), want)
			}
		}
		if ca["pandokos.stream"].AsBool() || ca["http.response.status_code"].AsInt64() != 200 || ca["pandokos.ttfb_ms"].AsInt64() < 0 {
			t.Errorf("client attributes: %v", ca)
		}
		for _, s := range []sdktrace.ReadOnlySpan{server, client} {
			if s.Status().Code != codes.Unset || s.Status().Description != "" || len(s.Events()) != 0 || len(s.Links()) != 0 {
				t.Errorf("%q: status %v %q, %d events, %d links", s.Name(), s.Status().Code, s.Status().Description, len(s.Events()), len(s.Links()))
			}
		}
		if s := h.spans.Ended(); len(s) != 2 {
			t.Errorf("%d spans", len(s))
		}
	})

	t.Run("a valid traceparent gives the caller's trace and span as parent", func(t *testing.T) {
		h := newHarness(t, nil)
		h.send(t, http.MethodPost, route, chat("fake-basic", false), withHeaders(http.Header{"Traceparent": {callerTP}}))
		server, client := endedSpans(t, h, 2)
		if server.SpanContext().TraceID().String() != callerTrace || client.SpanContext().TraceID().String() != callerTrace {
			t.Errorf("trace %s / %s, want %s", server.SpanContext().TraceID(), client.SpanContext().TraceID(), callerTrace)
		}
		if p := server.Parent(); p.SpanID().String() != callerSpan || !p.IsRemote() {
			t.Errorf("the parent of the server span: %v", p)
		}
		if rec := h.oneRecord(t); rec.TraceID != callerTrace {
			t.Errorf("the record's trace %s", rec.TraceID)
		}
	})

	t.Run("an invalid traceparent gives a new trace", func(t *testing.T) {
		for _, bad := range []string{"garbage", "00-" + strings.Repeat("0", 32) + "-" + callerSpan + "-01", "00-" + callerTrace + "-" + callerSpan, "ff-" + callerTrace + "-" + callerSpan + "-01"} {
			h := newHarness(t, nil)
			h.send(t, http.MethodPost, route, chat("fake-basic", false), withHeaders(http.Header{"Traceparent": {bad}}))
			server, _ := endedSpans(t, h, 2)
			if id := server.SpanContext().TraceID().String(); id == callerTrace || !reTrace.MatchString(id) || id == strings.Repeat("0", 32) {
				t.Errorf("%q: trace %s", bad, id)
			}
			if server.Parent().IsValid() {
				t.Errorf("%q: the server span has a parent", bad)
			}
		}
	})

	t.Run("a refusal after authentication gives the server span only", func(t *testing.T) {
		noRole := principalWith(func(p *authz.Principal) { p.Roles = nil })
		for name, run := range map[string]func(t *testing.T) *harness{
			"step 4": func(t *testing.T) *harness {
				h := newHarness(t, func(o *gateway.Options) { o.Authenticator = fixedAuth{p: noRole} })
				h.sendAuthed(t, route, chat("fake-basic", false))
				return h
			},
			"step 7": func(t *testing.T) *harness {
				h := newHarness(t, nil)
				h.sendAuthed(t, route, chat("nope", false))
				return h
			},
		} {
			h := run(t)
			server, client := endedSpans(t, h, 1)
			note(server)
			if client != nil {
				t.Errorf("%s: a client span", name)
			}
			sa := attrs(server)
			if sa["pandokos.outcome"].AsString() != "rejected_policy" || sa["pandokos.error.code"].AsString() == "" {
				t.Errorf("%s: %v", name, sa)
			}
			if server.Status().Code != codes.Unset {
				t.Errorf("%s: status %v for a refusal", name, server.Status().Code)
			}
		}
	})

	t.Run("unrouted and unauthenticated requests give no span", func(t *testing.T) {
		h := newHarness(t, nil)
		h.send(t, http.MethodPost, "/nope/v1/chat/completions", chat("fake-basic", false), authed())
		h.send(t, http.MethodPost, route, chat("fake-basic", false), http.Header{"Traceparent": {callerTP}})
		h.requestLines(t, 2)
		if n := len(h.spans.Started()) + len(h.spans.Ended()); n != 0 {
			t.Errorf("%d spans", n)
		}
	})

	t.Run("an error status of the provider: status Error and the upstream error attributes", func(t *testing.T) {
		h := newHarness(t, nil)
		h.sendAuthed(t, route, chat(fakeprovider.ModelError429, false))
		server, client := endedSpans(t, h, 2)
		note(server)
		note(client)
		for _, s := range []sdktrace.ReadOnlySpan{server, client} {
			if s.Status().Code != codes.Error || s.Status().Description != "" {
				t.Errorf("%q: status %v %q", s.Name(), s.Status().Code, s.Status().Description)
			}
		}
		if got := attrs(server)["pandokos.outcome"].AsString(); got != "upstream_error" {
			t.Errorf("outcome %q", got)
		}
		if got := attrs(client)["pandokos.usage.source"].AsString(); got != "none" {
			t.Errorf("usage source %q", got)
		}
	})

	t.Run("the provider could not be reached: upstream error and error code", func(t *testing.T) {
		h := newHarness(t, nil)
		closedPortTarget(h)
		h.sendAuthed(t, route, chat("fake-basic", false))
		server, client := endedSpans(t, h, 2)
		note(server)
		note(client)
		sa, ca := attrs(server), attrs(client)
		if sa["pandokos.error.code"].AsString() != "upstream_unreachable" || ca["pandokos.upstream.error"].AsString() == "" {
			t.Errorf("server %v client %v", sa, ca)
		}
		if server.Status().Code != codes.Error || client.Status().Code != codes.Error {
			t.Errorf("status %v %v", server.Status().Code, client.Status().Code)
		}
		if v, ok := ca["http.response.status_code"]; ok && v.AsInt64() != 0 {
			t.Errorf("a status code for a provider that did not answer: %v", v)
		}
	})

	t.Run("the status follows the outcome", func(t *testing.T) {
		for outcome, run := range map[usage.Outcome]func(h *harness){
			usage.OutcomeTimeout:       func(h *harness) { h.sendAuthed(t, route, chat(fakeprovider.ModelHang, false)) },
			usage.OutcomeInternalError: func(h *harness) { h.sendAuthed(t, route, chat("fake-basic", false)) },
			usage.OutcomeOK:            func(h *harness) { h.sendAuthed(t, route, chat("fake-basic", false)) },
		} {
			h := newHarness(t, func(o *gateway.Options) {
				switch outcome {
				case usage.OutcomeTimeout:
					o.MaxRequestDuration = 200 * time.Millisecond
				case usage.OutcomeInternalError:
					o.Private = panicTransport{}
				}
			})
			run(h)
			server, _ := endedSpans(t, h, map[usage.Outcome]int{usage.OutcomeTimeout: 2, usage.OutcomeInternalError: 2, usage.OutcomeOK: 2}[outcome])
			want := codes.Error
			if outcome == usage.OutcomeOK {
				want = codes.Unset
			}
			if server.Status().Code != want || attrs(server)["pandokos.outcome"].AsString() != string(outcome) {
				t.Errorf("%s: status %v outcome %v", outcome, server.Status().Code, attrs(server)["pandokos.outcome"])
			}
		}
	})

	t.Run("the attributes are exactly gateway.SpanAttributes", func(t *testing.T) {
		known := map[string]bool{}
		for _, k := range append(append([]string(nil), serverAttrs...), clientAttrs...) {
			known[k] = true
		}
		known["pandokos.error.code"], known["pandokos.upstream.error"] = true, true
		for _, k := range gateway.SpanAttributes {
			if !known[k] {
				t.Errorf("gateway.SpanAttributes lists %q and this test does not know it", k)
			}
			if !seen[k] {
				t.Errorf("no scenario of this test produced the attribute %q", k)
			}
		}
		listed := map[string]bool{}
		for _, k := range gateway.SpanAttributes {
			listed[k] = true
		}
		for k := range seen {
			if !listed[k] {
				t.Errorf("a span carried %q, which gateway.SpanAttributes does not list", k)
			}
		}
	})
}

func sameSet(a, b []string) bool {
	sort.Strings(a)
	sort.Strings(b)
	return strings.Join(a, ",") == strings.Join(b, ",")
}

var reTraceparent = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-0[01]$`)

// TestPropagation is T-496.
func TestPropagation(t *testing.T) {
	hdr := withHeaders(http.Header{"Traceparent": {callerTP}, "Tracestate": {"vendor=value"}, "Baggage": {"k=" + fixtures.CanaryPrompt}})

	t.Run("off: nothing of the caller's context reaches the provider", func(t *testing.T) {
		h := newHarness(t, nil)
		h.send(t, http.MethodPost, route, chat("fake-basic", false), hdr)
		reqs := h.up.Requests()
		if len(reqs) != 1 {
			t.Fatalf("%d requests", len(reqs))
		}
		for _, k := range []string{"Traceparent", "Tracestate", "Baggage"} {
			if v := reqs[0].Header.Values(k); len(v) != 0 {
				t.Errorf("the provider received %s: %v", k, v)
			}
		}
	})

	t.Run("on: the client span's context", func(t *testing.T) {
		h := newHarness(t, func(o *gateway.Options) { o.PropagateTrace = true })
		h.send(t, http.MethodPost, route, chat("fake-basic", false), hdr)
		_, client := endedSpans(t, h, 2)
		reqs := h.up.Requests()
		if len(reqs) != 1 {
			t.Fatalf("%d requests", len(reqs))
		}
		m := reTraceparent.FindStringSubmatch(reqs[0].Header.Get("Traceparent"))
		if m == nil {
			t.Fatalf("traceparent %q", reqs[0].Header.Get("Traceparent"))
		}
		if m[1] != callerTrace || m[1] != client.SpanContext().TraceID().String() {
			t.Errorf("trace ID %s, want the caller's %s", m[1], callerTrace)
		}
		if m[2] != client.SpanContext().SpanID().String() || m[2] == callerSpan {
			t.Errorf("span ID %s, want the client span's %s (not the caller's %s)", m[2], client.SpanContext().SpanID(), callerSpan)
		}
		if ts := reqs[0].Header.Get("Tracestate"); ts != "vendor=value" {
			t.Errorf("tracestate %q", ts)
		}
		if v := reqs[0].Header.Values("Baggage"); len(v) != 0 {
			t.Errorf("baggage reached the provider: %v", v)
		}
	})

	t.Run("on, without a caller context: Pandokos's own trace", func(t *testing.T) {
		h := newHarness(t, func(o *gateway.Options) { o.PropagateTrace = true })
		h.sendAuthed(t, route, chat("fake-basic", false))
		_, client := endedSpans(t, h, 2)
		m := reTraceparent.FindStringSubmatch(h.up.Requests()[0].Header.Get("Traceparent"))
		if m == nil || m[1] != client.SpanContext().TraceID().String() || m[2] != client.SpanContext().SpanID().String() {
			t.Errorf("traceparent %q", h.up.Requests()[0].Header.Get("Traceparent"))
		}
	})
}

// TestTelemetryCanaries is T-497.
func TestTelemetryCanaries(t *testing.T) {
	const unresolved = "synthetic-unresolved-model-canary"
	h := newHarness(t, nil)
	meta := http.Header{"X-Pandokos-Metadata-Note": {metaCanary}, "Traceparent": {callerTP}}
	send := func(body []byte) { h.send(t, http.MethodPost, route, body, withHeaders(meta)) }
	send(chat("fake-basic", false))                                                                                                      // a success
	send(chat("fake-basic", true))                                                                                                       // a stream
	send(chat(fakeprovider.ModelError500Echo, false))                                                                                    // a provider error that echoes both canaries
	send(chat(unresolved, false))                                                                                                        // a model that does not resolve
	send([]byte(`{"messages":[{"content":"` + fixtures.CanaryPrompt + `"}]}`))                                                           // an invalid body
	h.send(t, http.MethodPost, route, chat("fake-basic", false), withHeaders(http.Header{"Authorization": {"Bearer " + testKey + "x"}})) // a wrong key
	// A cancellation mid-stream.
	ctx, cancel := context.WithCancel(context.Background())
	resp, err := h.request(ctx, t, http.MethodPost, route, chat(fakeprovider.ModelGatedStream, true), withHeaders(meta))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testutil.ReadEvent(bufio.NewReader(resp.Body)); err != nil {
		t.Fatal(err)
	}
	cancel()
	resp.Body.Close()

	h.records(t, 6)                                                                                              // six requests were admitted: the wrong key was not
	testutil.Eventually(t, 5*time.Second, "every span ended", func() bool { return len(h.spans.Ended()) >= 10 }) // six server spans, four client spans

	spans := renderSpans(h.spans.Ended())
	testutil.AssertNoCanary(t, "the spans", spans)
	testutil.AssertAbsent(t, "the spans", spans, append(append(testutil.KeyForms(testKey), metaCanary, unresolved, fixtures.CanaryProviderKey), testutil.SecretForms(fixtures.CanaryProviderKey)...)...)

	records := h.rec.Records()
	for _, r := range records {
		text := fmt.Sprintf("%+v", r)
		testutil.AssertNoCanary(t, "a record", text)
		testutil.AssertAbsent(t, "a record", text, append(append(testutil.KeyForms(testKey), unresolved), testutil.SecretForms(fixtures.CanaryProviderKey)...)...)
	}
	carriers := 0
	for _, r := range records {
		if r.Metadata["note"] == metaCanary {
			carriers++
		}
		for k, v := range r.Metadata {
			if k != "note" || v != metaCanary {
				t.Errorf("unexpected metadata %q=%q", k, v)
			}
		}
	}
	if carriers == 0 {
		t.Error("the metadata value is on no record")
	}

	logs := h.logs.String()
	testutil.AssertNoCanary(t, "the logs", logs)
	testutil.AssertAbsent(t, "the logs", logs, append(append(testutil.KeyForms(testKey), metaCanary, unresolved), testutil.SecretForms(fixtures.CanaryProviderKey)...)...)
}
