package e2e

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/otlptest"
	"github.com/pandokos/pandokos/test/testutil"
)

const (
	callerTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	callerSpan  = "00f067aa0ba902b7"
	callerTP    = "00-" + callerTrace + "-" + callerSpan + "-01"

	collectorHeaderCanary = "synthetic-collector-header-value-6e3a"
	metadataCanary        = "synthetic-metadata-canary-77aa"
)

func traceHex(b []byte) string { return hex.EncodeToString(b) }

// spansOfTrace waits until the collector holds a server span and a client span of the trace.
func spansOfTrace(t *testing.T, c *otlptest.Collector, trace string) (server, client *tracepb.Span) {
	t.Helper()
	testutil.Eventually(t, 15*time.Second, "the spans of the trace at the collector", func() bool {
		server, client = nil, nil
		for _, s := range c.Spans() {
			if traceHex(s.TraceId) != trace {
				continue
			}
			switch s.Kind {
			case tracepb.Span_SPAN_KIND_SERVER:
				server = s
			case tracepb.Span_SPAN_KIND_CLIENT:
				client = s
			}
		}
		return server != nil && client != nil
	})
	return server, client
}

// TestTraces is T-511.
func TestTraces(t *testing.T) {
	t.Run("a call carrying traceparent yields a server and a client span of that trace", func(t *testing.T) {
		up := fakeprovider.New(t)
		col := otlptest.New(t)
		p := startBinary(t, opts{level: "info", up: up, collector: col, otlpEndpoint: col.URL().String(), otlpHeader: collectorHeaderCanary, propagate: true})
		col = p.collector(t)
		key := p.inferenceKey(t)
		tid := tenantOfKey(t, p)
		hdr := authHeader(key)
		hdr.Set("Traceparent", callerTP)
		if status, text := exchangeWith(t, p.client, "POST", p.url(chatPath), hdr, chatBody("fake-basic", false, fixtures.CanaryPrompt)); status != 200 {
			t.Fatalf("%d\n%s", status, text)
		}
		server, client := spansOfTrace(t, col, callerTrace)
		if traceHex(server.ParentSpanId) != callerSpan {
			t.Errorf("the server span's parent is %s, want the caller's %s", traceHex(server.ParentSpanId), callerSpan)
		}
		if !bytes.Equal(client.ParentSpanId, server.SpanId) {
			t.Error("the client span is not a child of the server span")
		}
		rows := waitRows(t, p.db, tid, 1)
		if rows[0]["trace_id"] != callerTrace {
			t.Errorf("the row's trace_id %v, want %s", rows[0]["trace_id"], callerTrace)
		}
		for _, r := range col.Requests() {
			if got := r.Header.Get("X-Collector-Key"); got != collectorHeaderCanary {
				t.Errorf("the export carries header %q", got)
			}
			if r.Path != "/v1/traces" {
				t.Errorf("path %q", r.Path)
			}
		}
		// Propagation is on: the provider received the context of the client span of the same trace.
		var seen string
		for _, r := range up.Requests() {
			if r.Path == "/v1/chat/completions" {
				seen = r.Header.Get("Traceparent")
			}
		}
		m := regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-0[01]$`).FindStringSubmatch(seen)
		if m == nil || m[1] != callerTrace || m[2] != traceHex(client.SpanId) {
			t.Errorf("the provider received traceparent %q, want trace %s and span %s", seen, callerTrace, traceHex(client.SpanId))
		}
		if code := p.stop(t); code != 0 {
			t.Errorf("exit code %d", code)
		}
	})

	t.Run("without propagation the provider receives no trace context", func(t *testing.T) {
		up := fakeprovider.New(t)
		col := otlptest.New(t)
		p := startBinary(t, opts{level: "info", up: up, collector: col, otlpEndpoint: col.URL().String()})
		key := p.inferenceKey(t)
		hdr := authHeader(key)
		hdr.Set("Traceparent", callerTP)
		hdr.Set("Tracestate", "vendor=value")
		hdr.Set("Baggage", "k=v")
		if status, _ := exchangeWith(t, p.client, "POST", p.url(chatPath), hdr, chatBody("fake-basic", false, "x")); status != 200 {
			t.Fatalf("status %d", status)
		}
		spansOfTrace(t, col, callerTrace)
		for _, r := range up.Requests() {
			for _, k := range []string{"Traceparent", "Tracestate", "Baggage"} {
				if v := r.Header.Values(k); len(v) != 0 {
					t.Errorf("the provider received %s: %v", k, v)
				}
			}
		}
	})

	t.Run("with the collector stopped calls succeed as fast and the process stops cleanly", func(t *testing.T) {
		up := fakeprovider.New(t)
		col := otlptest.New(t)
		p := startBinary(t, opts{level: "info", up: up, collector: col, otlpEndpoint: col.URL().String(), otlpTimeout: "1s", flushTimeout: "3s"})
		key := p.inferenceKey(t)
		timeCalls := func() time.Duration {
			began := time.Now()
			for i := 0; i < 20; i++ {
				if status, text := p.chatCall(t, key, fakeprovider.ModelBasic, i%2 == 1); status != 200 {
					t.Fatalf("%d\n%s", status, text)
				}
			}
			return time.Since(began)
		}
		withCollector := timeCalls()
		col.Close()
		without := timeCalls()
		if without > withCollector*3+500*time.Millisecond {
			t.Errorf("20 calls took %s with the collector and %s without", withCollector, without)
		}
		if code := p.stop(t); code != 0 {
			t.Errorf("exit code %d", code)
		}
	})
}

// TestNoContentAnywhere is T-512.
func TestNoContentAnywhere(t *testing.T) {
	for _, level := range []string{"info", "debug"} {
		t.Run(level, func(t *testing.T) {
			up := fakeprovider.New(t)
			col := otlptest.New(t)
			p := startBinary(t, opts{level: level, up: up, collector: col, otlpEndpoint: col.URL().String(), otlpHeader: collectorHeaderCanary, propagate: true,
				env: []string{"PANDOKOS_LIMITS_MAX_REQUEST_BODY_BYTES=4096"}})
			key := p.inferenceKey(t)
			tid := tenantOfKey(t, p)
			meta := func(h http.Header) http.Header {
				h.Set("X-Pandokos-Metadata-Note", metadataCanary)
				h.Set("Traceparent", callerTP)
				return h
			}
			responses := map[string]string{}
			call := func(label, path string, hdr http.Header, body []byte) {
				_, responses[label] = exchangeWith(t, p.client, "POST", p.url(path), hdr, body)
			}
			prompt := fixtures.CanaryPrompt
			call("1 chat", chatPath, meta(authHeader(key)), chatBody("fake-basic", false, prompt))
			call("2 chat stream", chatPath, meta(authHeader(key)), chatBody("fake-basic", true, prompt))
			call("3 responses", responsesPath, meta(authHeader(key)), []byte(`{"model":"fake-basic","input":"`+prompt+`"}`))
			call("4 responses stream", responsesPath, meta(authHeader(key)), []byte(`{"model":"fake-basic","stream":true,"input":"`+prompt+`"}`))
			call("5 embeddings", embeddingsPath, meta(authHeader(key)), []byte(`{"model":"fake-basic","input":"`+prompt+`"}`))
			call("6 provider error", chatPath, meta(authHeader(key)), chatBody("fake-error-500-echo", false, prompt))
			call("7 model not allowed", chatPath, meta(authHeader(key)), chatBody("synthetic-no-such-model", false, prompt))
			call("8 invalid body", chatPath, meta(authHeader(key)), []byte(`{"messages":[{"content":"`+prompt+`"}]}`))
			call("9 too large", chatPath, meta(authHeader(key)), chatBody("fake-basic", false, strings.Repeat(prompt, 300)))
			call("10 background", responsesPath, meta(authHeader(key)), []byte(`{"model":"fake-basic","background":true,"input":"`+prompt+`"}`))
			// A cancellation mid-stream.
			ctx, cancel := context.WithCancel(context.Background())
			resp := p.openStream(ctx, t, key, chatPath, chatBody(fakeprovider.ModelGatedStream, true, prompt))
			_, _ = bufioRead(resp)
			cancel()
			resp.Body.Close()

			// Ten requests were answered by Pandokos or the provider and one was cancelled: eleven rows.
			waitRows(t, p.db, tid, 11)
			if code := p.stop(t); code != 0 {
				t.Fatalf("exit code %d", code)
			}

			forms := append(append(testutil.KeyForms(key), testutil.SecretForms(fixtures.CanaryCredential)...), testutil.SecretForms(collectorHeaderCanary)...)
			forms = append(forms, collectorHeaderCanary)

			// Everything but the metadata column of the ledger: no canary of any kind.
			rows := usageRows(t, p.db, tid)
			sawMetadata := 0
			for _, r := range rows {
				md, _ := r["metadata"].(map[string]any)
				if md["note"] == metadataCanary {
					sawMetadata++
				}
				delete(r, "metadata")
				text := fmt.Sprint(r)
				testutil.AssertNoCanary(t, "a column of usage_record", text)
				testutil.AssertAbsent(t, "a column of usage_record", text, append(forms, metadataCanary)...)
			}
			if sawMetadata == 0 {
				t.Error("the metadata canary is in no usage_record.metadata")
			}

			where := map[string]string{
				"stdout":         p.stdout.String(),
				"stderr":         p.stderr.String(),
				"audit_event":    tableText(t, p.db, "audit_event"),
				"platform_audit": tableText(t, p.db, "platform_audit_event"),
			}
			var raw strings.Builder
			for _, r := range col.Requests() {
				raw.Write(r.Body)
			}
			where["the bodies received by the collector"] = raw.String()
			var decoded strings.Builder
			for _, s := range col.Spans() {
				fmt.Fprintf(&decoded, "%s %v\n", s.Name, s.Attributes)
			}
			where["the spans received by the collector"] = decoded.String()
			if raw.Len() == 0 {
				t.Error("the collector received nothing")
			}
			for name, text := range where {
				testutil.AssertNoCanary(t, name, text)
				testutil.AssertAbsent(t, name, text, append(forms, metadataCanary)...)
			}
			// The collector's header value is sent as a header and is in no body.
			for _, r := range col.Requests() {
				if r.Header.Get("X-Collector-Key") != collectorHeaderCanary {
					t.Errorf("the export carries header %q", r.Header.Get("X-Collector-Key"))
				}
			}
			for label, text := range responses {
				if label == "6 provider error" {
					continue // the provider's own error body echoes the canaries by design
				}
				testutil.AssertAbsent(t, "response "+label, text, forms...)
			}
		})
	}
}

// tableText returns every row of a table of the pandokos schema as JSON text.
func tableText(t *testing.T, d *dbtest.Database, table string) string {
	t.Helper()
	conn := d.Super(t)
	rows, err := conn.Query(context.Background(), fmt.Sprintf(`SELECT to_jsonb(x)::text FROM pandokos.%s x`, table))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var sb strings.Builder
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		sb.WriteString(s + "\n")
	}
	return sb.String()
}

// bufioRead reads the first event of a stream response.
func bufioRead(resp *http.Response) (string, error) {
	var sb strings.Builder
	buf := make([]byte, 1)
	for !strings.HasSuffix(sb.String(), "\n\n") {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			return sb.String(), err
		}
	}
	return sb.String(), nil
}
