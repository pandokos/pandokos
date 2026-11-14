package gateway_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/openai"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// readEventWithin reads one event from r and fails the test if it takes longer than d: with the
// provider gated, a gateway that buffers cannot deliver the event.
func readEventWithin(t *testing.T, r *bufio.Reader, d time.Duration) string {
	t.Helper()
	type result struct {
		ev  string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		ev, err := testutil.ReadEvent(r)
		ch <- result{ev, err}
	}()
	select {
	case res := <-ch:
		if res.err != nil && res.ev == "" {
			t.Fatalf("reading an event: %v", res.err)
		}
		return res.ev
	case <-time.After(d):
		t.Fatalf("no event within %s: the stream is held somewhere", d)
		return ""
	}
}

func eventStrings(b []byte) []string {
	var out []string
	for _, ev := range splitEvents(b) {
		out = append(out, string(ev))
	}
	return out
}

// TestStreamNoAddedBuffering is T-489: the provider does not send event n+1 before the test has
// read event n from the caller's side.
func TestStreamNoAddedBuffering(t *testing.T) {
	chatUsage := fixtures.Read("chat_stream_usage.sse")
	for _, c := range []struct {
		name     string
		op       opSpec
		body     []byte
		provider []byte // what the provider sends, event by event
		deliver  func(ev string) bool
	}{
		{"chat, observe mode (the caller asked for usage)", opChat,
			[]byte(`{"model":"fake-basic","stream":true,"stream_options":{"include_usage":true},"messages":[]}`), chatUsage,
			func(string) bool { return true }},
		{"chat, strip mode (the usage option was added)", opChat, chat("fake-basic", true), chatUsage,
			func(ev string) bool { return !strings.Contains(ev, `"choices":[]`) }},
		{"responses", opResponses, opResponses.body("fake-basic", true), fixtures.Read("responses_stream.sse"),
			func(string) bool { return true }},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.up.GateEachEvent()
			t.Cleanup(h.up.Release)
			resp, err := h.request(ctx5(t), t, http.MethodPost, c.op.path, c.body, authed())
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("status %d", resp.StatusCode)
			}
			br := bufio.NewReader(resp.Body)
			events := eventStrings(c.provider)
			for i, ev := range events {
				if c.deliver(ev) {
					if got := readEventWithin(t, br, 3*time.Second); got != ev {
						t.Fatalf("event %d:\n got %q\nwant %q", i, got, ev)
					}
				}
				if i < len(events)-1 {
					h.up.Advance() // the provider may now send event i+1
				}
			}
			if rest, err := io.ReadAll(br); err != nil || len(rest) != 0 {
				t.Errorf("after the last event: %q %v", rest, err)
			}
			if r := h.oneRecord(t); r.InputSource != usage.SourceProvider {
				t.Errorf("usage source %q", r.InputSource)
			}
		})
	}
}

// pacedCall sends a streaming request with the provider gated after every event and reads the
// events one at a time, letting the provider send the next after each: every event reaches the
// reader as a chunk of its own, whatever the network does with small writes.
func pacedCall(t *testing.T, h *harness, body []byte) captured {
	t.Helper()
	h.up.GateEachEvent()
	t.Cleanup(h.up.Release)
	resp, err := h.request(ctx5(t), t, http.MethodPost, route, body, authed())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	var got []byte
	for {
		ev, err := testutil.ReadEvent(br)
		got = append(got, ev...)
		if err != nil {
			break
		}
		h.up.Advance()
	}
	return captured{status: resp.StatusCode, line: resp.Proto + " " + resp.Status, header: resp.Header, body: got}
}

// softAdapter is the openai adapter with a reader and a usage reader of the test's making.
type softAdapter struct {
	family.Adapter
	newReader func(op family.Operation, strip bool) family.StreamReader
	readUsage func(op family.Operation, body []byte) (family.Usage, bool, error)
}

func (a softAdapter) NewStreamReader(op family.Operation, strip bool) family.StreamReader {
	if a.newReader != nil {
		return a.newReader(op, strip)
	}
	return a.Adapter.NewStreamReader(op, strip)
}

func (a softAdapter) ReadUsage(op family.Operation, body []byte) (family.Usage, bool, error) {
	if a.readUsage != nil {
		return a.readUsage(op, body)
	}
	return a.Adapter.ReadUsage(op, body)
}

// brokenReader wraps the real reader and misbehaves on demand. It records what the real reader was
// given and what it returned, so that the test knows which bytes the reader had taken.
type brokenReader struct {
	real family.StreamReader

	mu          sync.Mutex
	calls       int
	panicFilter int  // panic in the call with this number (1-based); 0 never
	panicUsage  bool // panic in Usage
	garbage     bool // return other bytes than the input
	given       int  // bytes handed to the real reader
	returned    []byte
}

func (r *brokenReader) Filter(p []byte) []byte {
	r.mu.Lock()
	r.calls++
	n := r.calls
	r.mu.Unlock()
	if r.panicFilter == n {
		panic(fixtures.CanaryCompletion)
	}
	out := r.real.Filter(p)
	r.mu.Lock()
	r.given += len(p)
	r.returned = append(r.returned, out...)
	r.mu.Unlock()
	if r.garbage {
		return []byte("garbage that is not the stream")
	}
	return out
}
func (r *brokenReader) Flush() []byte { return r.real.Flush() }
func (r *brokenReader) Usage() (family.Usage, bool) {
	if r.panicUsage {
		panic(fixtures.CanaryPrompt)
	}
	return r.real.Usage()
}
func (r *brokenReader) Failed() bool  { return r.real.Failed() }
func (r *brokenReader) Deltas() int64 { return r.real.Deltas() }

// TestReaderFailsSoft is T-490.
func TestReaderFailsSoft(t *testing.T) {
	provider := fixtures.Read("chat_stream_usage.sse")
	var br *brokenReader
	build := func(t *testing.T, newReader func(strip bool) *brokenReader, readUsage func(family.Operation, []byte) (family.Usage, bool, error)) *harness {
		br = nil
		return newHarness(t, func(o *gateway.Options) {
			o.Families = map[string]family.Adapter{"openai": softAdapter{
				Adapter: openai.Adapter(),
				newReader: func(op family.Operation, strip bool) family.StreamReader {
					br = newReader(strip)
					br.real = openai.NewStreamReaderFor(op.Name, strip)
					return br
				},
				readUsage: readUsage,
			}}
		})
	}
	stripBody := chat("fake-basic", true)
	observeBody := []byte(`{"model":"fake-basic","stream":true,"stream_options":{"include_usage":true},"messages":[]}`)

	check := func(t *testing.T, h *harness, resp captured, wantBody []byte) {
		t.Helper()
		if resp.status != 200 || !strings.HasPrefix(resp.header.Get("Content-Type"), "text/event-stream") {
			t.Errorf("status %d headers %v", resp.status, resp.header)
		}
		if !bytes.Equal(resp.body, wantBody) {
			t.Errorf("the caller's bytes differ from what they must be (%d, want %d)", len(resp.body), len(wantBody))
		}
		r := h.oneRecord(t)
		if r.InputSource != usage.SourceEstimated || r.OutputSource != usage.SourceEstimated || r.Outcome != usage.OutcomeOK {
			t.Errorf("record: sources %q %q outcome %q", r.InputSource, r.OutputSource, r.Outcome)
		}
		testutil.Eventually(t, 5*time.Second, "`usage reader failed`", func() bool { return len(h.logs.Find(t, "usage reader failed")) >= 1 })
		lines := h.logs.Find(t, "usage reader failed")
		if len(lines) != 1 || lines[0]["level"] != "ERROR" || lines[0]["panic_type"] == nil {
			t.Errorf("the log: %v", lines)
		}
		testutil.AssertNoCanary(t, "the logs", h.logs.String())
		if line := h.lastRequestLine(t); line["usage_reader"] != "failed" {
			t.Errorf("the request line: %v", line)
		}
	}

	t.Run("observe: a panic in Filter", func(t *testing.T) {
		h := build(t, func(bool) *brokenReader { return &brokenReader{panicFilter: 2} }, nil)
		check(t, h, pacedCall(t, h, observeBody), provider)
	})
	t.Run("observe: a panic in Usage", func(t *testing.T) {
		h := build(t, func(bool) *brokenReader { return &brokenReader{panicUsage: true} }, nil)
		check(t, h, h.sendAuthed(t, route, observeBody), provider)
	})
	t.Run("observe: a reader that returns garbage", func(t *testing.T) {
		h := build(t, func(bool) *brokenReader { return &brokenReader{garbage: true} }, nil)
		c := h.sendAuthed(t, route, observeBody)
		if !bytes.Equal(c.body, provider) {
			t.Errorf("the caller's bytes are not the provider's: %q", c.body)
		}
	})
	t.Run("plain: a panic in ReadUsage", func(t *testing.T) {
		h := build(t, func(bool) *brokenReader { return &brokenReader{} }, func(family.Operation, []byte) (family.Usage, bool, error) {
			panic(fixtures.CanaryCompletion)
		})
		c := h.sendAuthed(t, route, chat("fake-basic", false))
		if c.status != 200 || !bytes.Equal(c.body, fixtures.Read("chat_completion.json")) {
			t.Errorf("%d %s", c.status, c.body)
		}
		r := h.oneRecord(t)
		if r.InputSource != usage.SourceEstimated || r.OutputSource != usage.SourceEstimated {
			t.Errorf("sources %q %q", r.InputSource, r.OutputSource)
		}
		testutil.Eventually(t, 5*time.Second, "`usage reader failed`", func() bool { return len(h.logs.Find(t, "usage reader failed")) == 1 })
		testutil.AssertNoCanary(t, "the logs", h.logs.String())
	})
	t.Run("strip: everything from the failing chunk on is forwarded", func(t *testing.T) {
		h := build(t, func(bool) *brokenReader { return &brokenReader{panicFilter: 2} }, nil)
		c := pacedCall(t, h, stripBody)
		// What the reader took before it failed went through it; the failing chunk and the
		// rest are the provider's bytes as they came.
		want := append(append([]byte(nil), br.returned...), provider[br.given:]...)
		check(t, h, c, want)
		if !bytes.Contains(c.body, []byte(`"choices":[]`)) {
			t.Error("the usage-only event was removed after the reader failed")
		}
	})
	t.Run("strip: a panic in Usage after a clean stream", func(t *testing.T) {
		h := build(t, func(bool) *brokenReader { return &brokenReader{panicUsage: true} }, nil)
		c := h.sendAuthed(t, route, stripBody)
		want := provider[:0:0]
		for _, ev := range splitEvents(provider) {
			if !bytes.Contains(ev, []byte(`"choices":[]`)) {
				want = append(want, ev...)
			}
		}
		check(t, h, c, want)
	})
}

// edgeBehaviours are the behaviours of the fake that every reader must survive.
var edgeBehaviours = []string{
	fakeprovider.ModelStreamError, fakeprovider.ModelKeepalive, fakeprovider.ModelTools, fakeprovider.ModelTinyWrites,
	fakeprovider.ModelCRLF, fakeprovider.ModelBigEvent, fakeprovider.ModelCut,
}

// TestStreamEdgeCasesThroughTheGateway is T-491.
func TestStreamEdgeCasesThroughTheGateway(t *testing.T) {
	observe := func(o opSpec, model string) []byte {
		if o.name == opChat.name {
			return []byte(`{"model":"` + model + `","stream":true,"stream_options":{"include_usage":true},"messages":[]}`)
		}
		return o.body(model, true)
	}
	type mode struct {
		name  string
		op    opSpec
		body  func(model string) []byte
		strip bool
	}
	modes := []mode{
		{"chat observe", opChat, func(m string) []byte { return observe(opChat, m) }, false},
		{"chat strip", opChat, func(m string) []byte { return chat(m, true) }, true},
		{"responses", opResponses, func(m string) []byte { return opResponses.body(m, true) }, false},
	}
	provides := map[string]bool{ // behaviours whose stream carries usage the reader can read
		fakeprovider.ModelKeepalive: true, fakeprovider.ModelTools: true, fakeprovider.ModelTinyWrites: true, fakeprovider.ModelCRLF: true,
	}
	for _, m := range modes {
		for _, model := range edgeBehaviours {
			t.Run(m.name+" "+model, func(t *testing.T) {
				h := newHarness(t, nil)
				// The provider's own bytes: the same request made straight to the fake.
				direct := func() []byte {
					req, _ := http.NewRequestWithContext(ctx5(t), http.MethodPost, h.up.URL()+strings.TrimPrefix(m.op.path, "/openai"), bytes.NewReader(m.body(model)))
					req.Header.Set("Authorization", "Bearer "+fixtures.CanaryProviderKey)
					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
					b, _ := io.ReadAll(resp.Body)
					return b
				}
				want := direct()
				// A reader that gave up (an event over the bound, before the usage-only event)
				// forwards everything, the usage-only event included.
				if m.strip && model != fakeprovider.ModelBigEvent {
					var kept []byte
					for _, ev := range splitEvents(want) {
						if !bytes.Contains(ev, []byte(`"choices":[]`)) || !bytes.Contains(ev, []byte(`"usage":{`)) {
							kept = append(kept, ev...)
						}
					}
					want = kept
				}
				status, got, _ := h.sendLoose(t, m.op.path, m.body(model))
				if status != 200 {
					t.Fatalf("status %d", status)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("the caller's bytes are not the provider's (%d, want %d)", len(got), len(want))
				}
				r := h.settle(t, 1)[0]
				wantSource := usage.SourceEstimated
				if provides[model] {
					wantSource = usage.SourceProvider
				}
				if r.InputSource != wantSource || r.OutputSource != wantSource {
					t.Errorf("sources %q %q, want %q", r.InputSource, r.OutputSource, wantSource)
				}
				wantOutcome := usage.OutcomeOK
				if model == fakeprovider.ModelCut {
					wantOutcome = usage.OutcomeUpstreamError
				}
				if r.Outcome != wantOutcome {
					t.Errorf("outcome %q, want %q", r.Outcome, wantOutcome)
				}
				if wantSource == usage.SourceEstimated && model != fakeprovider.ModelBigEvent {
					if wantOut := contentEvents(m.op, got); r.Estimated.OutputTokens != wantOut {
						t.Errorf("estimated output %d, want the %d content events delivered", r.Estimated.OutputTokens, wantOut)
					}
				}
				if r.ResponseBytes == nil || *r.ResponseBytes != int64(len(got)) {
					t.Errorf("ResponseBytes %v, want %d", r.ResponseBytes, len(got))
				}
			})
		}
	}
}

// TestCancellation is T-492.
func TestCancellation(t *testing.T) {
	baseline := runtime.NumGoroutine()
	t.Run("mid-stream", func(t *testing.T) {
		h := newHarness(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		resp, err := h.request(ctx, t, http.MethodPost, route, chat(fakeprovider.ModelGatedStream, true), authed())
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if _, err := testutil.ReadEvent(bufio.NewReader(resp.Body)); err != nil {
			t.Fatal(err)
		}
		cancel()
		if !h.up.WaitCancelled(2 * time.Second) {
			t.Fatal("the provider did not see the cancellation within 2 s")
		}
		r := h.settle(t, 1)[0]
		if r.Outcome != usage.OutcomeCancelled || r.InputSource != usage.SourceEstimated || r.OutputSource != usage.SourceEstimated || r.Estimated.OutputTokens != 1 {
			t.Errorf("record: %q sources %q %q, estimated output %d", r.Outcome, r.InputSource, r.OutputSource, r.Estimated.OutputTokens)
		}
		if n := h.rec.Admits() - len(h.rec.Records()); n != 0 {
			t.Errorf("%d slots in use", n)
		}
	})
	t.Run("before the headers", func(t *testing.T) {
		h := newHarness(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			if resp, err := h.request(ctx, t, http.MethodPost, route, chat(fakeprovider.ModelHang, false), authed()); err == nil {
				resp.Body.Close()
			}
		}()
		testutil.Eventually(t, 5*time.Second, "the provider to receive the request", func() bool { return h.up.InFlight() == 1 })
		cancel()
		<-done
		if !h.up.WaitCancelled(2 * time.Second) {
			t.Fatal("the provider did not see the cancellation within 2 s")
		}
		r := h.settle(t, 1)[0]
		if r.Outcome != usage.OutcomeCancelled || r.InputSource != usage.SourceNone || r.OutputSource != usage.SourceNone || r.UpstreamStatus != 0 {
			t.Errorf("record: %q sources %q %q status %d", r.Outcome, r.InputSource, r.OutputSource, r.UpstreamStatus)
		}
		if n := h.rec.Admits() - len(h.rec.Records()); n != 0 {
			t.Errorf("%d slots in use", n)
		}
	})
	testutil.Eventually(t, 5*time.Second, "the goroutine count to return to its base", func() bool { return runtime.NumGoroutine() <= baseline+3 })
}

// TestFirstByteTime is T-493: the time to the first body byte, not to the headers.
func TestFirstByteTime(t *testing.T) {
	t.Run("headers at once, the first byte 200 ms later", func(t *testing.T) {
		h := newRaw(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_ = http.NewResponseController(w).Flush()
			time.Sleep(200 * time.Millisecond)
			_, _ = w.Write(fixtures.Read("chat_completion.json"))
		})
		h.sendAuthed(t, route, chat("fake-basic", false))
		r := h.oneRecord(t)
		if r.TTFB == nil || *r.TTFB < 200*time.Millisecond {
			t.Fatalf("TTFB %v, want at least 200 ms", r.TTFB)
		}
		if line := h.lastRequestLine(t); line["ttfb_ms"] == nil || line["ttfb_ms"].(float64) < 200 {
			t.Errorf("ttfb_ms %v", line["ttfb_ms"])
		}
	})
	t.Run("a stream: the first event, not the headers", func(t *testing.T) {
		h := newRaw(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			_ = http.NewResponseController(w).Flush()
			time.Sleep(200 * time.Millisecond)
			_, _ = w.Write(fixtures.Read("chat_stream_usage.sse"))
		})
		h.sendAuthed(t, route, chat("fake-basic", true))
		if r := h.oneRecord(t); r.TTFB == nil || *r.TTFB < 200*time.Millisecond {
			t.Fatalf("TTFB %v", r.TTFB)
		}
	})
	t.Run("no body: nil", func(t *testing.T) {
		h := newRaw(t, rawJSON(200, ""))
		h.sendAuthed(t, route, chat("fake-basic", false))
		if r := h.oneRecord(t); r.TTFB != nil {
			t.Errorf("TTFB %v for an answer without a body", *r.TTFB)
		}
		if line := h.lastRequestLine(t); line["ttfb_ms"] != nil {
			t.Errorf("ttfb_ms %v", line["ttfb_ms"])
		}
	})
	t.Run("no answer: nil", func(t *testing.T) {
		h := newHarness(t, nil)
		closedPortTarget(h)
		h.sendAuthed(t, route, chat("fake-basic", false))
		if r := h.oneRecord(t); r.TTFB != nil {
			t.Errorf("TTFB %v", *r.TTFB)
		}
	})
}
