package gateway_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// splitEvents splits a stream into events by the test's own rule: an event ends after an empty
// line (LF or CRLF); the last one may be incomplete.
func splitEvents(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(b); {
		j := bytes.IndexByte(b[i:], '\n')
		if j < 0 {
			break
		}
		line := b[i : i+j]
		i += j + 1
		if len(line) == 0 || string(line) == "\r" {
			out = append(out, b[start:i])
			start = i
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

func complete(ev []byte) bool {
	return bytes.HasSuffix(ev, []byte("\n\n")) || bytes.HasSuffix(ev, []byte("\r\n\r\n"))
}

// contentEvents counts the complete content events of a stream of an operation: chunks with a
// non-empty choices array (chat) and events whose type ends in .delta (responses).
func contentEvents(op opSpec, stream []byte) int64 {
	var n int64
	for _, ev := range splitEvents(stream) {
		if !complete(ev) {
			continue
		}
		switch {
		case op.name == opChat.name && bytes.Contains(ev, []byte(`"choices":[{`)):
			n++
		case op.name == opResponses.name && bytes.Contains(ev, []byte(`.delta"`)):
			n++
		}
	}
	return n
}

// priced gives every target of the harness the two versions of testutil.TwoPrices, the first in force.
func priced(h *harness) {
	for k, tgt := range h.targets {
		tgt.Prices = testutil.TwoPrices(time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
		h.targets[k] = tgt
	}
}

// expectedCost is the cost of usage u at the first version of TwoPrices.
func expectedCost(t *testing.T, u family.Usage) pricing.Cost {
	t.Helper()
	v, ok := pricing.VersionAt(testutil.TwoPrices(time.Now().Add(-time.Hour), time.Now().Add(time.Hour)), time.Now())
	return pricing.CostOf(u, v, ok)
}

// sendLoose sends a request and returns what arrived, also when the answer was cut.
func (h *harness) sendLoose(t *testing.T, path string, body []byte) (status int, got []byte, readErr error) {
	t.Helper()
	resp, err := h.request(ctx5(t), t, http.MethodPost, path, body, authed())
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, b, err
}

func rawJSON(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// TestUsageSources is T-486: each row of the first table of section 5.4, with each cause of the
// last row.
func TestUsageSources(t *testing.T) {
	type srcCase struct {
		name   string
		op     opSpec
		model  string
		stream bool
		mutate func(*gateway.Options)
		raw    http.HandlerFunc // the provider, when the fake cannot do it
		do     func(t *testing.T, h *harness, c *srcCase) (status int, received []byte)

		in, out  usage.Source
		outcome  usage.Outcome
		want     *family.Usage // provider usage, when the sources say provider
		estimate func(t *testing.T, c *srcCase, rec usage.Record, received []byte) family.Usage
		reader   string // the log field usage_reader, when the reader gave up
		status   int
	}
	plainEstimate := func(_ *testing.T, c *srcCase, rec usage.Record, _ []byte) family.Usage {
		out := (*rec.ResponseBytes + 3) / 4
		if c.op.name == opEmbeddings.name {
			out = 0
		}
		return family.Usage{InputTokens: (*rec.RequestBytes + 3) / 4, OutputTokens: out}
	}
	streamEstimate := func(_ *testing.T, c *srcCase, rec usage.Record, received []byte) family.Usage {
		return family.Usage{InputTokens: (*rec.RequestBytes + 3) / 4, OutputTokens: contentEvents(c.op, received)}
	}
	defaultDo := func(t *testing.T, h *harness, c *srcCase) (int, []byte) {
		status, b, _ := h.sendLoose(t, c.op.path, c.op.body(c.model, c.stream))
		return status, b
	}
	chatU := &family.Usage{InputTokens: 9, OutputTokens: 12}
	respU := &family.Usage{InputTokens: 20, OutputTokens: 15, CacheReadTokens: 5, ReasoningTokens: 4}
	embU := &family.Usage{InputTokens: 8}
	provided := map[string]*family.Usage{opChat.name: chatU, opResponses.name: respU, opEmbeddings.name: embU}
	short := func(o *gateway.Options) { o.MaxRequestDuration = 300 * time.Millisecond }

	var cases []srcCase
	for _, op := range allOps {
		for _, stream := range []bool{false, true} {
			if stream && !op.streams {
				continue
			}
			kind := "plain"
			est := plainEstimate
			if stream {
				kind, est = "stream", streamEstimate
			}
			cases = append(cases,
				srcCase{name: op.name + " " + kind + ": the provider reports usage", op: op, model: "fake-basic", stream: stream,
					in: usage.SourceProvider, out: usage.SourceProvider, outcome: usage.OutcomeOK, want: provided[op.name], status: 200},
				srcCase{name: op.name + " " + kind + ": the provider sent no usage", op: op, model: fakeprovider.ModelNoUsage, stream: stream,
					in: usage.SourceEstimated, out: usage.SourceEstimated, outcome: usage.OutcomeOK, estimate: est, status: 200},
				srcCase{name: op.name + " " + kind + ": the answer was cut", op: op, model: fakeprovider.ModelCut, stream: stream,
					in: usage.SourceEstimated, out: usage.SourceEstimated, outcome: usage.OutcomeUpstreamError, estimate: est, status: 200},
			)
			if !stream {
				cases = append(cases, srcCase{name: op.name + " plain: a JSON answer over 4 MiB", op: op, model: fakeprovider.ModelBigEvent,
					in: usage.SourceEstimated, out: usage.SourceEstimated, outcome: usage.OutcomeOK, estimate: plainEstimate, status: 200})
			} else {
				cases = append(cases, srcCase{name: op.name + " stream: an event over the bound", op: op, model: fakeprovider.ModelBigEvent, stream: true,
					in: usage.SourceEstimated, out: usage.SourceEstimated, outcome: usage.OutcomeOK, reader: "failed", status: 200,
					estimate: func(_ *testing.T, _ *srcCase, rec usage.Record, _ []byte) family.Usage {
						return family.Usage{InputTokens: (*rec.RequestBytes + 3) / 4, OutputTokens: rec.Estimated.OutputTokens}
					}})
			}
		}
	}
	cases = append(cases,
		srcCase{name: "chat stream: the caller cancels", op: opChat, model: fakeprovider.ModelGatedStream, stream: true,
			in: usage.SourceEstimated, out: usage.SourceEstimated, outcome: usage.OutcomeCancelled, status: 200,
			do: func(t *testing.T, h *harness, c *srcCase) (int, []byte) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				resp, err := h.request(ctx, t, http.MethodPost, c.op.path, c.op.body(c.model, true), authed())
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				ev, err := testutil.ReadEvent(bufio.NewReader(resp.Body))
				if err != nil {
					t.Fatal(err)
				}
				cancel()
				return resp.StatusCode, []byte(ev)
			}, estimate: streamEstimate},
		srcCase{name: "chat stream: the duration limit passes", op: opChat, model: fakeprovider.ModelGatedStream, stream: true, mutate: short,
			in: usage.SourceEstimated, out: usage.SourceEstimated, outcome: usage.OutcomeTimeout, status: 200, estimate: streamEstimate},
		srcCase{name: "invalid JSON", op: opChat, model: "fake-basic", raw: rawJSON(200, `{"usage":`),
			in: usage.SourceEstimated, out: usage.SourceEstimated, outcome: usage.OutcomeOK, estimate: plainEstimate, status: 200},
		srcCase{name: "a negative count", op: opChat, model: "fake-basic", raw: rawJSON(200, `{"usage":{"prompt_tokens":-1,"completion_tokens":1}}`),
			in: usage.SourceEstimated, out: usage.SourceEstimated, outcome: usage.OutcomeOK, estimate: plainEstimate, status: 200},
		srcCase{name: "an empty body", op: opChat, model: "fake-basic", raw: rawJSON(200, ``),
			in: usage.SourceEstimated, out: usage.SourceEstimated, outcome: usage.OutcomeOK, estimate: plainEstimate, status: 200},
		// No estimate for what the provider did not serve.
		srcCase{name: "a 429", op: opChat, model: fakeprovider.ModelError429, in: usage.SourceNone, out: usage.SourceNone, outcome: usage.OutcomeUpstreamError, status: 429},
		srcCase{name: "a 500", op: opResponses, model: fakeprovider.ModelError500Echo, in: usage.SourceNone, out: usage.SourceNone, outcome: usage.OutcomeUpstreamError, status: 500},
		srcCase{name: "a redirect", op: opChat, model: fakeprovider.ModelRedirect, in: usage.SourceNone, out: usage.SourceNone, outcome: usage.OutcomeUpstreamError, status: 307},
		srcCase{name: "a refusal", op: opChat, model: "nope", in: usage.SourceNone, out: usage.SourceNone, outcome: usage.OutcomeRejectedPolicy},
		srcCase{name: "no answer before the headers", op: opChat, model: fakeprovider.ModelHang, mutate: short, in: usage.SourceNone, out: usage.SourceNone, outcome: usage.OutcomeTimeout},
	)

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var h *harness
			if c.raw != nil {
				h = newRaw(t, c.raw)
				if c.mutate != nil {
					t.Fatal("a raw case with options")
				}
			} else {
				h = newHarness(t, c.mutate)
			}
			priced(h)
			if c.name == "a redirect" {
				h.up.RedirectTo("http://127.0.0.1:1/elsewhere")
			}
			do := c.do
			if do == nil {
				do = defaultDo
			}
			_, received := do(t, h, &c)
			r := h.settle(t, 1)[0]

			if r.InputSource != c.in || r.OutputSource != c.out || r.Outcome != c.outcome {
				t.Fatalf("sources %q %q outcome %q, want %q %q %q", r.InputSource, r.OutputSource, r.Outcome, c.in, c.out, c.outcome)
			}
			if c.status != 0 && r.UpstreamStatus != c.status {
				t.Errorf("upstream status %d, want %d", r.UpstreamStatus, c.status)
			}
			line := h.lastRequestLine(t)
			if line["usage_source"] != nil && line["usage_source"] != string(c.in) {
				t.Errorf("usage_source %v, want %s", line["usage_source"], c.in)
			}
			if c.reader != "" && line["usage_reader"] != c.reader {
				t.Errorf("usage_reader %v, want %q", line["usage_reader"], c.reader)
			}
			switch c.in {
			case usage.SourceProvider:
				u := r.Usage
				u.Reported, u.Extra = nil, nil
				if !reflect.DeepEqual(u, *c.want) {
					t.Errorf("usage %+v, want %+v", r.Usage, *c.want)
				}
				if len(r.Usage.Reported) == 0 {
					t.Error("the provider's own counters were not kept")
				}
				if !reflect.DeepEqual(r.Estimated, family.Usage{}) {
					t.Errorf("an estimate beside the provider's figures: %+v", r.Estimated)
				}
				if got, want := r.Cost, expectedCost(t, r.Usage); !got.Known || got.Amount.Cmp(want.Amount) != 0 {
					t.Errorf("cost %+v, want %+v", got, want)
				}
			case usage.SourceEstimated:
				if r.Usage.InputTokens != 0 || r.Usage.OutputTokens != 0 || r.Usage.CacheReadTokens != 0 || r.Usage.ReasoningTokens != 0 || r.Usage.Reported != nil || r.Usage.Extra != nil {
					t.Errorf("provider columns beside an estimate: %+v", r.Usage)
				}
				want := c.estimate(t, &c, r, received)
				if r.Estimated.InputTokens != want.InputTokens || r.Estimated.OutputTokens != want.OutputTokens {
					t.Errorf("estimate %d/%d, want %d/%d", r.Estimated.InputTokens, r.Estimated.OutputTokens, want.InputTokens, want.OutputTokens)
				}
				if r.Estimated.CacheReadTokens != 0 || r.Estimated.ReasoningTokens != 0 || r.Estimated.Reported != nil {
					t.Errorf("the estimate holds more than the two totals: %+v", r.Estimated)
				}
				if got, wantCost := r.Cost, expectedCost(t, r.Estimated); !got.Known || got.Amount.Cmp(wantCost.Amount) != 0 {
					t.Errorf("the cost was not computed on the estimate: %+v, want %s", got, wantCost.Amount)
				}
			default:
				if r.Usage.InputTokens != 0 || r.Estimated.InputTokens != 0 || r.Estimated.OutputTokens != 0 || r.CostStatus != "none" || r.Cost.Known {
					t.Errorf("usage %+v estimate %+v cost %q", r.Usage, r.Estimated, r.CostStatus)
				}
			}
		})
	}
}

// TestResponsesAndEmbeddings is T-494.
func TestResponsesAndEmbeddings(t *testing.T) {
	t.Run("both operations reach base plus native path with the body unchanged", func(t *testing.T) {
		for _, op := range []opSpec{opResponses, opEmbeddings} {
			h := newHarness(t, nil)
			body := op.body("fake-basic", false)
			c := h.sendAuthed(t, op.path, body)
			if c.status != 200 {
				t.Fatalf("%s: %d %s", op.name, c.status, c.body)
			}
			reqs := h.up.Requests()
			if len(reqs) != 1 || reqs[0].Path != strings.TrimPrefix(op.path, "/openai") || !bytes.Equal(reqs[0].Body, body) {
				t.Errorf("%s: the provider saw %+v", op.name, reqs)
			}
			if bytes.Contains(reqs[0].Body, []byte("include_usage")) {
				t.Errorf("%s: the usage option was spliced", op.name)
			}
		}
	})

	t.Run("the model is replaced when the names differ", func(t *testing.T) {
		for _, op := range []opSpec{opResponses, opEmbeddings} {
			h := newHarness(t, nil)
			tgt := h.targets["openai/fake-basic"]
			tgt.Model = "nick"
			h.targets["openai/nick"] = tgt
			c := h.sendAuthed(t, op.path, op.body("nick", false))
			if c.status != 200 {
				t.Fatalf("%s: %d %s", op.name, c.status, c.body)
			}
			want := strings.Replace(string(op.body("nick", false)), `"model":"nick"`, `"model":"fake-basic"`, 1)
			if reqs := h.up.Requests(); len(reqs) != 1 || string(reqs[0].Body) != want {
				t.Errorf("%s: the provider received %q, want %q", op.name, reqs[0].Body, want)
			}
			if r := h.oneRecord(t); r.Model != "nick" || r.UpstreamModel != "fake-basic" {
				t.Errorf("%s: record model %q upstream %q", op.name, r.Model, r.UpstreamModel)
			}
		}
	})

	t.Run("usage from the JSON answers", func(t *testing.T) {
		for op, want := range map[string]family.Usage{
			opResponses.name:  {InputTokens: 20, OutputTokens: 15, CacheReadTokens: 5, ReasoningTokens: 4},
			opEmbeddings.name: {InputTokens: 8},
		} {
			h := newHarness(t, nil)
			o := opResponses
			if op == opEmbeddings.name {
				o = opEmbeddings
			}
			c := h.sendOp(t, o, "fake-basic", false)
			wantBody := fixtures.Read("responses.json")
			if op == opEmbeddings.name {
				wantBody = fixtures.Read("embeddings.json")
			}
			if !bytes.Equal(c.body, wantBody) {
				t.Errorf("%s: the caller's body differs from the provider's", op)
			}
			r := h.oneRecord(t)
			got := r.Usage
			got.Reported, got.Extra = nil, nil
			if !reflect.DeepEqual(got, want) || r.InputSource != usage.SourceProvider || r.OutputSource != usage.SourceProvider {
				t.Errorf("%s: usage %+v sources %q %q", op, r.Usage, r.InputSource, r.OutputSource)
			}
		}
	})

	t.Run("a responses stream is forwarded as it came and its usage is read", func(t *testing.T) {
		h := newHarness(t, nil)
		c := h.sendOp(t, opResponses, "fake-basic", true)
		if !bytes.Equal(c.body, fixtures.Read("responses_stream.sse")) {
			t.Errorf("the stream differs:\n%s", c.body)
		}
		if reqs := h.up.Requests(); len(reqs) != 1 || !bytes.Equal(reqs[0].Body, opResponses.body("fake-basic", true)) {
			t.Errorf("the provider received %+v", reqs)
		}
		r := h.oneRecord(t)
		if r.InputSource != usage.SourceProvider || r.Usage.InputTokens != 20 || r.Usage.OutputTokens != 15 || r.Usage.CacheReadTokens != 5 || r.Usage.ReasoningTokens != 4 {
			t.Errorf("usage %+v", r.Usage)
		}
		if r.Stream == nil || !*r.Stream {
			t.Errorf("Stream %v", r.Stream)
		}
		line := h.lastRequestLine(t)
		if line["usage_injected"] != false {
			t.Errorf("usage_injected %v for a responses stream", line["usage_injected"])
		}
	})

	t.Run("background true is refused and nothing is forwarded", func(t *testing.T) {
		h := newHarness(t, nil)
		c := h.sendAuthed(t, opResponses.path, []byte(`{"model":"fake-basic","input":"x","background":true}`))
		assertContract(t, "background", c, family.CodeInvalidRequest)
		if n := len(h.up.Requests()); n != 0 {
			t.Errorf("the provider received %d requests", n)
		}
		r := h.oneRecord(t)
		if r.Outcome != usage.OutcomeRejectedPolicy || r.ErrorCode != "invalid_request" || r.Operation != "responses" {
			t.Errorf("record %+v", r)
		}
		if line := h.lastRequestLine(t); line["invalid_reason"] != "background_not_supported" {
			t.Errorf("line %v", line)
		}
		if c := h.sendAuthed(t, opResponses.path, []byte(`{"model":"fake-basic","input":"x","background":false}`)); c.status != 200 {
			t.Errorf("background false: %d", c.status)
		}
	})

	t.Run("embeddings with stream true are forwarded unchanged and treated as plain", func(t *testing.T) {
		h := newHarness(t, nil)
		body := []byte(`{"model":"fake-basic","input":"` + fixtures.CanaryPrompt + `","stream":true}`)
		c := h.sendAuthed(t, opEmbeddings.path, body)
		if c.status != 200 || !bytes.Equal(c.body, fixtures.Read("embeddings.json")) {
			t.Fatalf("%d %s", c.status, c.body)
		}
		if reqs := h.up.Requests(); len(reqs) != 1 || !bytes.Equal(reqs[0].Body, body) {
			t.Errorf("the provider received %+v", reqs)
		}
		r := h.oneRecord(t)
		if r.Stream == nil || *r.Stream || r.InputSource != usage.SourceProvider || r.Usage.InputTokens != 8 || r.Usage.OutputTokens != 0 {
			t.Errorf("record: stream %v usage %+v", r.Stream, r.Usage)
		}
		line := h.lastRequestLine(t)
		if line["stream"] != false {
			t.Errorf("stream %v", line["stream"])
		}
	})

	t.Run("both operations are routes with both permissions", func(t *testing.T) {
		seen := map[string]bool{}
		for _, r := range gateway.Routes() {
			if r.Method == http.MethodPost && (r.Path == "/{provider}/v1/responses" || r.Path == "/{provider}/v1/embeddings") {
				seen[r.Path] = len(r.Permissions) == 2
			}
		}
		if len(seen) != 2 || !seen["/{provider}/v1/responses"] || !seen["/{provider}/v1/embeddings"] {
			t.Errorf("routes %v", seen)
		}
	})
}
