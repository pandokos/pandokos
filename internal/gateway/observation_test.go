package gateway_test

import (
	"bufio"
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/openai"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/limitstest"
	"github.com/pandokos/pandokos/test/testutil"
)

// observer is a gateway.Metrics that keeps what it was given.
type observer struct {
	mu  sync.Mutex
	obs []gateway.RequestObservation
}

func (o *observer) Observe(x gateway.RequestObservation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.obs = append(o.obs, x)
}

func (o *observer) all() []gateway.RequestObservation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]gateway.RequestObservation(nil), o.obs...)
}

// wait returns the observations once there are n; it then lets the gateway show it makes no more.
func (o *observer) wait(t *testing.T, n int) []gateway.RequestObservation {
	t.Helper()
	testutil.Eventually(t, 5*time.Second, "the observation of the request", func() bool { return len(o.all()) >= n })
	time.Sleep(50 * time.Millisecond)
	got := o.all()
	if len(got) != n {
		t.Fatalf("%d observations, want exactly %d (one Observe per request): %+v", len(got), n, got)
	}
	return got
}

// observed returns the options that set the Metrics, then apply mutate.
func observed(o *observer, mutate func(*gateway.Options)) func(*gateway.Options) {
	return func(opts *gateway.Options) {
		opts.Metrics = o
		if mutate != nil {
			mutate(opts)
		}
	}
}

// panicUsageAdapter is the openai adapter whose reader of a plain answer's usage panics.
func panicUsageAdapter() map[string]family.Adapter {
	return map[string]family.Adapter{"openai": softAdapter{
		Adapter: openai.Adapter(),
		readUsage: func(family.Operation, []byte) (family.Usage, bool, error) {
			panic(fixtures.CanaryCompletion)
		},
	}}
}

// TestObservation is T-910: one Observe per request, with the fields of section 5.2.1, for each way
// a request can end.
func TestObservation(t *testing.T) {
	const wantRoute = "/{provider}/v1/chat/completions"
	type observation = gateway.RequestObservation

	// matched asserts the dimensions of a request whose route matched.
	matched := func(t *testing.T, o observation) {
		t.Helper()
		if o.Method != "POST" || o.Scheme != "http" || o.Route != wantRoute || o.Provider != "openai" || o.Family != "openai" || o.Operation != "chat" {
			t.Errorf("method %q scheme %q route %q provider %q family %q operation %q", o.Method, o.Scheme, o.Route, o.Provider, o.Family, o.Operation)
		}
	}
	refused := func(t *testing.T, o observation, status int, code string) {
		t.Helper()
		if o.Status != status || o.ErrorCode != code || o.Outcome != "pandokos_error" {
			t.Errorf("status %d code %q outcome %q, want %d %q pandokos_error", o.Status, o.ErrorCode, o.Outcome, status, code)
		}
		if o.Forwarded || o.Call != 0 || o.FirstChunk != 0 || o.CallError != "" || o.UpstreamStatus != 0 || o.CredentialID != "" {
			t.Errorf("a request refused before step 9 has call fields: forwarded %v call %s first %s error %q upstream %d credential %q",
				o.Forwarded, o.Call, o.FirstChunk, o.CallError, o.UpstreamStatus, o.CredentialID)
		}
		if o.InputTokens != 0 || o.OutputTokens != 0 || o.CostKnown {
			t.Errorf("a refused request has usage: %+v", o)
		}
	}
	noSource := func(s string) bool { return s == "" || s == "none" }

	cases := []struct {
		name   string
		mutate func(*gateway.Options)
		drive  func(t *testing.T, h *harness)
		check  func(t *testing.T, h *harness, o observation)
	}{
		{name: "an unmatched path",
			drive: func(t *testing.T, h *harness) { h.send(t, http.MethodGet, "/nope/v1/x", nil, authed()) },
			check: func(t *testing.T, _ *harness, o observation) {
				refused(t, o, 404, "endpoint_not_supported")
				if o.Method != "GET" || o.Route != "" || o.Provider != "" || o.Family != "" || o.Operation != "" || o.Model != "" {
					t.Errorf("an unmatched request names a route or a provider: %+v", o)
				}
				if o.LimitState != "not_evaluated" {
					t.Errorf("limit state %q", o.LimitState)
				}
			}},
		{name: "a missing key",
			drive: func(t *testing.T, h *harness) {
				h.send(t, http.MethodPost, route, chat("fake-basic", false), http.Header{"Content-Type": {"application/json"}})
			},
			check: func(t *testing.T, _ *harness, o observation) {
				refused(t, o, 401, "unauthenticated")
				matched(t, o)
				if o.Model != "" {
					t.Errorf("model %q before the catalogue resolved it", o.Model)
				}
			}},
		{name: "a ledger refusal",
			mutate: func(o *gateway.Options) {
				r := &testutil.MemoryRecorder{Capacity: 1}
				r.Admit()
				o.Recorder = r
			},
			drive: func(t *testing.T, h *harness) { h.sendAuthed(t, route, chat("fake-basic", false)) },
			check: func(t *testing.T, _ *harness, o observation) {
				refused(t, o, 503, "unavailable")
				if o.Model != "" {
					t.Errorf("model %q for a request refused at admission", o.Model)
				}
			}},
		{name: "an invalid body",
			drive: func(t *testing.T, h *harness) { h.sendAuthed(t, route, []byte(`{}`)) },
			check: func(t *testing.T, _ *harness, o observation) {
				refused(t, o, 400, "invalid_request")
				matched(t, o)
				if o.Model != "" {
					t.Errorf("model %q", o.Model)
				}
			}},
		{name: "a model not allowed",
			drive: func(t *testing.T, h *harness) { h.sendAuthed(t, route, chat("nope", false)) },
			check: func(t *testing.T, _ *harness, o observation) {
				refused(t, o, 403, "model_not_allowed")
				matched(t, o)
				if o.Model != "" {
					t.Errorf("the model the caller asked for, %q, is not a catalogue name: it was not resolved", o.Model)
				}
			}},
		{name: "a 429 of the limits",
			mutate: func(o *gateway.Options) {
				o.Limits = testutil.NewStaticLimits().Refuse(gateway.LimitRefusal{
					Code: "rate_limited", Level: "tenant", Subject: "key", Metric: "requests_per_minute", Scope: "all", Limit: 5, RetryAfter: time.Second,
				})
			},
			drive: func(t *testing.T, h *harness) { h.sendAuthed(t, route, chat("fake-basic", false)) },
			check: func(t *testing.T, _ *harness, o observation) {
				refused(t, o, 429, "rate_limited")
				matched(t, o)
				if o.Model != "fake-basic" {
					t.Errorf("model %q, want the resolved catalogue name", o.Model)
				}
				if o.Refusal == nil || *o.Refusal != (gateway.LimitRefusalObservation{Code: "rate_limited", Metric: "requests_per_minute", Level: "tenant", SubjectKind: "key", Scope: "all"}) {
					t.Errorf("refusal %+v", o.Refusal)
				}
			}},
		{name: "no credential",
			mutate: func(o *gateway.Options) { o.Credentials = testutil.StaticCredentials(nil) },
			drive:  func(t *testing.T, h *harness) { h.sendAuthed(t, route, chat("fake-basic", false)) },
			check: func(t *testing.T, _ *harness, o observation) {
				refused(t, o, 409, "no_credential")
				matched(t, o)
				if o.Model != "fake-basic" {
					t.Errorf("model %q", o.Model)
				}
			}},
		{name: "a 200 answer",
			drive: func(t *testing.T, h *harness) { priced(h); h.sendAuthed(t, route, chat("fake-basic", false)) },
			check: func(t *testing.T, h *harness, o observation) {
				matched(t, o)
				if o.Status != 200 || o.Outcome != "ok" || o.ErrorCode != "" || o.Model != "fake-basic" || o.LimitState != "not_evaluated" {
					t.Errorf("status %d outcome %q code %q model %q state %q", o.Status, o.Outcome, o.ErrorCode, o.Model, o.LimitState)
				}
				if !o.Forwarded || o.Call <= 0 || o.Call > o.Duration || o.Stream || o.FirstChunk != 0 || o.CallError != "" || o.UpstreamStatus != 200 || o.CredentialID != "" {
					t.Errorf("forwarded %v call %s of %s stream %v first %s error %q upstream %d credential %q",
						o.Forwarded, o.Call, o.Duration, o.Stream, o.FirstChunk, o.CallError, o.UpstreamStatus, o.CredentialID)
				}
				rec := h.oneRecord(t)
				if o.InputTokens != 9 || o.OutputTokens != 12 || o.InputSource != "provider" || o.OutputSource != "provider" {
					t.Errorf("tokens %d/%d sources %q/%q", o.InputTokens, o.OutputTokens, o.InputSource, o.OutputSource)
				}
				if !o.CostKnown || o.CostCurrency != rec.Cost.Currency || o.CostAmount != rec.Cost.Amount.Float64() || o.CostAmount <= 0 || o.CostReason != "" {
					t.Errorf("cost known %v %v %s (record: %v %s), reason %q", o.CostKnown, o.CostAmount, o.CostCurrency, rec.Cost.Amount, rec.Cost.Currency, o.CostReason)
				}
				if o.Refusal != nil || o.Estimate != nil {
					t.Errorf("refusal %+v estimate %+v without limits", o.Refusal, o.Estimate)
				}
			}},
		{name: "a settled reservation",
			mutate: func(o *gateway.Options) {
				o.Limits = limitstest.NewWith(t, budgetRules(costRule("r", limits.CostPerDay, "USD", 100_000_000_000), countRule("c", limits.TokensPerMinute, 1_000_000)))
			},
			drive: func(t *testing.T, h *harness) { h.sendAuthed(t, route, chat("fake-basic", false)) },
			check: func(t *testing.T, _ *harness, o observation) {
				if o.LimitState != "limited" || o.Refusal != nil {
					t.Errorf("limit state %q refusal %+v", o.LimitState, o.Refusal)
				}
				e := o.Estimate
				if e == nil {
					t.Fatal("no Estimate for a settled reservation")
				}
				if e.ReservedTokens <= 0 || e.SettledTokens != 21 {
					t.Errorf("reserved %d settled %d tokens, want a reservation and the 21 the provider reported", e.ReservedTokens, e.SettledTokens)
				}
				if !e.CostComparable || e.ReservedCost <= 0 || e.SettledCost <= 0 {
					t.Errorf("costs reserved %v settled %v comparable %v", e.ReservedCost, e.SettledCost, e.CostComparable)
				}
			}},
		{name: "a 200 answer of an unpriced model",
			drive: func(t *testing.T, h *harness) { h.sendAuthed(t, route, chat("fake-basic", false)) },
			check: func(t *testing.T, _ *harness, o observation) {
				if o.CostKnown || o.CostReason != "no_price" || o.CostAmount != 0 {
					t.Errorf("cost known %v reason %q amount %v, want unknown with reason no_price", o.CostKnown, o.CostReason, o.CostAmount)
				}
			}},
		{name: "a 200 stream",
			drive: func(t *testing.T, h *harness) { h.sendAuthed(t, route, chat("fake-basic", true)) },
			check: func(t *testing.T, _ *harness, o observation) {
				matched(t, o)
				if o.Status != 200 || o.Outcome != "ok" || !o.Forwarded || !o.Stream || o.CallError != "" {
					t.Errorf("%+v", o)
				}
				if o.FirstChunk <= 0 || o.FirstChunk > o.Call || o.Call > o.Duration {
					t.Errorf("first chunk %s, call %s, duration %s", o.FirstChunk, o.Call, o.Duration)
				}
				if o.InputSource != "provider" || o.OutputSource != "provider" || o.InputTokens <= 0 || o.OutputTokens <= 0 {
					t.Errorf("tokens %d/%d sources %q/%q", o.InputTokens, o.OutputTokens, o.InputSource, o.OutputSource)
				}
			}},
		{name: "a provider 429",
			drive: func(t *testing.T, h *harness) { h.sendAuthed(t, route, chat(fakeprovider.ModelError429, false)) },
			check: func(t *testing.T, _ *harness, o observation) {
				matched(t, o)
				if o.Status != 429 || o.UpstreamStatus != 429 || o.CallError != "429" || !o.Forwarded || o.ErrorCode != "" || o.Outcome != "ok" {
					t.Errorf("%+v", o)
				}
				if o.CredentialID != "synthetic-credential-openai" {
					t.Errorf("credential %q: the provider credential ID is set when the provider answers 429", o.CredentialID)
				}
				if !noSource(o.InputSource) || !noSource(o.OutputSource) || o.InputTokens != 0 || o.OutputTokens != 0 {
					t.Errorf("usage of an answer that served nothing: %d/%d %q/%q", o.InputTokens, o.OutputTokens, o.InputSource, o.OutputSource)
				}
			}},
		{name: "a provider 500",
			drive: func(t *testing.T, h *harness) { h.sendAuthed(t, route, chat(fakeprovider.ModelError500Echo, false)) },
			check: func(t *testing.T, _ *harness, o observation) {
				if o.Status != 500 || o.UpstreamStatus != 500 || o.CallError != "500" || !o.Forwarded {
					t.Errorf("%+v", o)
				}
				if o.CredentialID != "" {
					t.Errorf("credential %q set for a provider 500", o.CredentialID)
				}
			}},
		{name: "a call timeout before the headers",
			mutate: func(o *gateway.Options) { o.MaxRequestDuration = 250 * time.Millisecond },
			drive:  func(t *testing.T, h *harness) { h.sendAuthed(t, route, chat(fakeprovider.ModelHang, false)) },
			check: func(t *testing.T, _ *harness, o observation) {
				if o.Status != 504 || o.ErrorCode != "upstream_timeout" || o.Outcome != "pandokos_error" || !o.Forwarded || o.CallError != "timeout" {
					t.Errorf("%+v", o)
				}
				if o.Call < 200*time.Millisecond {
					t.Errorf("call %s with a 250ms limit", o.Call)
				}
			}},
		{name: "a call timeout in the middle of a stream",
			mutate: func(o *gateway.Options) { o.MaxRequestDuration = 300 * time.Millisecond },
			drive: func(t *testing.T, h *harness) {
				resp, err := h.request(ctx5(t), t, http.MethodPost, route, chat(fakeprovider.ModelGatedStream, true), authed())
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				br := bufio.NewReader(resp.Body)
				_, _ = testutil.ReadEvent(br)
				for {
					if _, err := testutil.ReadEvent(br); err != nil {
						break
					}
				}
			},
			check: func(t *testing.T, _ *harness, o observation) {
				if o.Outcome != "timeout" || o.Status != 200 || !o.Forwarded || !o.Stream || o.CallError != "timeout" {
					t.Errorf("%+v", o)
				}
			}},
		{name: "a caller that leaves in the middle of a stream",
			drive: func(t *testing.T, h *harness) {
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
			},
			check: func(t *testing.T, _ *harness, o observation) {
				if o.Outcome != "client_cancelled" || o.Status != 200 || !o.Forwarded || o.CallError != "cancelled" {
					t.Errorf("%+v", o)
				}
			}},
		{name: "a body that breaks",
			drive: func(t *testing.T, h *harness) { _, _, _ = h.sendLoose(t, route, chat(fakeprovider.ModelCut, false)) },
			check: func(t *testing.T, _ *harness, o observation) {
				if o.Outcome != "upstream_aborted" || !o.Forwarded || o.CallError != "upstream_aborted" {
					t.Errorf("%+v", o)
				}
			}},
		{name: "a panic in a usage reader",
			mutate: func(o *gateway.Options) { o.Families = panicUsageAdapter() },
			drive:  func(t *testing.T, h *harness) { h.sendAuthed(t, route, chat("fake-basic", false)) },
			check: func(t *testing.T, _ *harness, o observation) {
				matched(t, o)
				if o.Status != 200 || o.Outcome != "ok" || !o.Forwarded || o.CallError != "" {
					t.Errorf("%+v", o)
				}
				if o.InputSource != "estimated" || o.OutputSource != "estimated" {
					t.Errorf("the reader gave up: the sides are estimated, got %q/%q", o.InputSource, o.OutputSource)
				}
			}},
		{name: "a panic in the handler",
			mutate: func(o *gateway.Options) { o.Private = panicTransport{} },
			drive:  func(t *testing.T, h *harness) { h.sendAuthed(t, route, chat("fake-basic", false)) },
			check: func(t *testing.T, _ *harness, o observation) {
				if o.Status != 500 || o.ErrorCode != "internal" || o.Outcome != "pandokos_error" {
					t.Errorf("the observation of a recovered panic: %+v", o)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ob := &observer{}
			h := newHarness(t, observed(ob, c.mutate))
			c.drive(t, h)
			got := ob.wait(t, 1)
			o := got[0]
			if o.Duration <= 0 {
				t.Errorf("duration %s", o.Duration)
			}
			c.check(t, h, o)
		})
	}

	t.Run("a method outside the known set is _OTHER", func(t *testing.T) {
		ob := &observer{}
		h := newHarness(t, observed(ob, nil))
		h.send(t, "BREW", route, nil, authed())
		if o := ob.wait(t, 1)[0]; o.Method != "_OTHER" {
			t.Errorf("method %q", o.Method)
		}
	})

	t.Run("without a Metrics nothing is observed and nothing fails", func(t *testing.T) {
		h := newHarness(t, nil)
		if c := h.sendAuthed(t, route, chat("fake-basic", false)); c.status != 200 {
			t.Errorf("status %d", c.status)
		}
	})

	t.Run("a Metrics that panics never reaches the response", func(t *testing.T) {
		h := newHarness(t, func(o *gateway.Options) { o.Metrics = panicMetrics{} })
		c := h.sendAuthed(t, route, chat("fake-basic", false))
		if c.status != 200 {
			t.Errorf("status %d: a panic in Observe reached the caller", c.status)
		}
		testutil.Eventually(t, 5*time.Second, "`metrics observation failed`", func() bool { return len(h.logs.Find(t, "metrics observation failed")) >= 1 })
		l := h.logs.Find(t, "metrics observation failed")[0]
		if l["level"] != "ERROR" || l["request_id"] == nil || l["panic_type"] == nil {
			t.Errorf("the line: %v", l)
		}
		testutil.AssertNoCanary(t, "the logs", h.logs.String())
	})
}

type panicMetrics struct{}

func (panicMetrics) Observe(gateway.RequestObservation) { panic(fixtures.CanaryPrompt) }
