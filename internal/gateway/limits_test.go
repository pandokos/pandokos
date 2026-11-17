package gateway_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/openai"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// limFam is one API family as the limits tests drive it.
type limFam struct {
	name          string
	path          string
	hdr           func() http.Header
	body          func(model string, stream bool) []byte
	newH          func(t *testing.T, mutate func(*gateway.Options)) *harness
	contract      func(t *testing.T, name string, c captured, code family.ErrorCode)
	fixtureTokens int64  // input plus output the provider reports for fake-basic
	fixture429    []byte // the provider's own 429
	setOutput     int64  // the output limit the body of body() sets; 0 for none
	provider      string
}

func limFamilies() []limFam {
	return []limFam{
		{name: "openai", path: route, hdr: authed, body: chat, newH: newHarness, contract: assertContract,
			fixtureTokens: 21, fixture429: fixtures.Read("error_429.json"), provider: "openai"},
		{name: "anthropic", path: anthropicRoute, hdr: anthropicHdr, body: messages, newH: newFamilies, contract: assertAnthropicContract,
			fixtureTokens: 19, fixture429: fixtures.ReadAnthropic("error_429.json"), setOutput: 16, provider: "anthropic"},
	}
}

func (f limFam) send(t *testing.T, h *harness, body []byte) captured {
	t.Helper()
	return h.send(t, http.MethodPost, f.path, body, f.hdr())
}

// refusalOf is a refusal of the metric with values that differ between the metrics.
func refusalOf(metric string) gateway.LimitRefusal {
	switch metric {
	case "tokens_per_minute":
		return gateway.LimitRefusal{Level: "platform", Subject: "service_account", Metric: metric, Limit: 1000, Remaining: 12,
			RetryAfter: 17 * time.Second, Reset: 44 * time.Second}
	case "concurrent_requests":
		return gateway.LimitRefusal{Level: "tenant", Subject: "key", Metric: metric, Limit: 3, Remaining: 0, RetryAfter: time.Second}
	}
	return gateway.LimitRefusal{Level: "tenant", Subject: "tenant", Metric: metric, Limit: 5, Remaining: 0,
		RetryAfter: 30 * time.Second, Reset: 45 * time.Second}
}

func ceil4(n int) int64 { return int64((n + 3) / 4) }

// TestReserveStep is T-630: Reserve is called once, after Resolve and before Open, with the
// estimate and the principal; refusals of steps 2 to 7 never call it; a refusal of step 7b opens
// no credential and sends nothing.
func TestReserveStep(t *testing.T) {
	for _, f := range limFamilies() {
		t.Run(f.name, func(t *testing.T) {
			t.Run("once, after Resolve and before Open, with the estimate and the principal", func(t *testing.T) {
				lim := testutil.NewStaticLimits()
				var h *harness
				var resolvesAtReserve int64
				var opensAtReserve int
				lim.OnReserve = func(authz.Principal, gateway.LimitEstimate) {
					resolvesAtReserve, opensAtReserve = h.cat.Resolves.Load(), len(h.creds.Calls())
				}
				h = f.newH(t, func(o *gateway.Options) { o.Limits = lim })
				body := f.body("fake-basic", false)
				c := f.send(t, h, body)
				if c.status != 200 {
					t.Fatalf("%d %s", c.status, c.body)
				}
				rs := lim.Of("reserve")
				if len(rs) != 1 {
					t.Fatalf("%d reserves", len(rs))
				}
				if resolvesAtReserve != 1 || opensAtReserve != 0 {
					t.Errorf("at the reserve: %d resolves and %d credential opens, want 1 and 0", resolvesAtReserve, opensAtReserve)
				}
				if got := len(h.creds.Calls()); got != 1 {
					t.Errorf("%d credential opens after the request, want 1", got)
				}
				e, p := rs[0].Estimate, rs[0].Principal
				if p.TenantID != testPrincipal.TenantID || p.ID != testPrincipal.ID || p.KeyID != testPrincipal.KeyID {
					t.Errorf("principal %+v", p)
				}
				if e.RequestID != c.header.Get("X-Pandokos-Request-Id") || e.RequestID == "" {
					t.Errorf("request ID %q, header %q", e.RequestID, c.header.Get("X-Pandokos-Request-Id"))
				}
				wantOut := int64(4096)
				if f.setOutput > 0 {
					wantOut = f.setOutput
				}
				if e.InputTokens != ceil4(len(body)) || e.OutputTokens != wantOut {
					t.Errorf("estimate %d + %d, want %d + %d", e.InputTokens, e.OutputTokens, ceil4(len(body)), wantOut)
				}
				if !e.Cost.Known {
					t.Errorf("cost %+v: the model has a price", e.Cost)
				}
				line := h.lastRequestLine(t)
				if got, ok := line["reserved_tokens"].(float64); !ok || int64(got) != e.InputTokens+e.OutputTokens {
					t.Errorf("reserved_tokens %v, want %d", line["reserved_tokens"], e.InputTokens+e.OutputTokens)
				}
			})

			t.Run("the output limit of the caller and the model's maximum shape the estimate", func(t *testing.T) {
				lim := testutil.NewStaticLimits()
				h := f.newH(t, func(o *gateway.Options) { o.Limits = lim })
				tgt := h.targets[f.provider+"/fake-basic"]
				tgt.MaxOutputTokens, tgt.ContextWindow = 8, 10
				h.targets[f.provider+"/fake-basic"] = tgt
				body := f.body("fake-basic", false)
				if c := f.send(t, h, body); c.status != 200 {
					t.Fatalf("%d", c.status)
				}
				rs := lim.Of("reserve")
				if len(rs) != 1 {
					t.Fatalf("%d reserves", len(rs))
				}
				e := rs[0].Estimate
				if e.OutputTokens != 8 || e.InputTokens != 10 {
					t.Errorf("estimate %d + %d, want 10 + 8 (context window and model maximum)", e.InputTokens, e.OutputTokens)
				}
			})

			t.Run("refusals of steps 2 to 7 never call it", func(t *testing.T) {
				lim := testutil.NewStaticLimits()
				h := f.newH(t, func(o *gateway.Options) { o.Limits = lim })
				h.send(t, http.MethodPost, f.path, f.body("fake-basic", false), http.Header{}) // step 3
				h.send(t, http.MethodGet, f.path, nil, f.hdr())                                // step 2
				f.send(t, h, []byte(`{}`))                                                     // step 6
				f.send(t, h, f.body("nope", false))                                            // step 7
				h.cat.ResolveErr = errors.New("synthetic")                                     // step 7, a decision that cannot be taken
				f.send(t, h, f.body("fake-basic", false))
				if n := len(lim.Calls()); n != 0 {
					t.Errorf("%d limiter calls: %+v", n, lim.Calls())
				}
			})

			t.Run("a refusal of 7b opens no credential and sends nothing", func(t *testing.T) {
				lim := testutil.NewStaticLimits().Refuse(refusalOf("requests_per_minute"))
				h := f.newH(t, func(o *gateway.Options) { o.Limits = lim })
				c := f.send(t, h, f.body("fake-basic", false))
				if c.status != 429 {
					t.Fatalf("%d %s", c.status, c.body)
				}
				if n := len(h.creds.Calls()); n != 0 {
					t.Errorf("%d credentials opened", n)
				}
				if n := len(h.up.Requests()); n != 0 {
					t.Errorf("%d requests reached the provider", n)
				}
				if s, r := lim.Of("settle"), lim.Of("release"); len(s)+len(r) != 0 {
					t.Errorf("a refused call settled or released: %d %d", len(s), len(r))
				}
			})
		})
	}

	t.Run("anthropic count_tokens reserves a request and no tokens", func(t *testing.T) {
		lim := testutil.NewStaticLimits()
		h := newFamilies(t, func(o *gateway.Options) { o.Limits = lim })
		c := h.sendA(t, countRoute, messages(fakeprovider.ModelBasic, false), anthropicHdr())
		if c.status != 200 {
			t.Fatalf("%d %s", c.status, c.body)
		}
		rs := lim.Of("reserve")
		if len(rs) != 1 || rs[0].Estimate.InputTokens != 0 || rs[0].Estimate.OutputTokens != 0 {
			t.Fatalf("reserves %+v, want one with no tokens", rs)
		}
	})

	t.Run("embeddings reserve the input and no output", func(t *testing.T) {
		lim := testutil.NewStaticLimits()
		h := newHarness(t, func(o *gateway.Options) { o.Limits = lim })
		body := opEmbeddings.body("fake-basic", false)
		if c := h.sendAuthed(t, opEmbeddings.path, body); c.status != 200 {
			t.Fatalf("%d %s", c.status, c.body)
		}
		rs := lim.Of("reserve")
		if len(rs) != 1 || rs[0].Estimate.OutputTokens != 0 || rs[0].Estimate.InputTokens != ceil4(len(body)) {
			t.Fatalf("reserves %+v", rs)
		}
	})
}

// TestRateLimited is T-631: the 429 of section 5.8 for both families and each metric.
func TestRateLimited(t *testing.T) {
	for _, f := range limFamilies() {
		for _, metric := range []string{"requests_per_minute", "tokens_per_minute", "concurrent_requests"} {
			t.Run(f.name+"/"+metric, func(t *testing.T) {
				ref := refusalOf(metric)
				lim := testutil.NewStaticLimits().Refuse(ref)
				h := f.newH(t, func(o *gateway.Options) { o.Limits = lim })
				c := f.send(t, h, f.body("fake-basic", false))
				f.contract(t, metric, c, family.CodeRateLimited)
				want := map[string]string{
					"Retry-After":               strconv.Itoa(int(ref.RetryAfter / time.Second)),
					"X-Pandokos-Error":          "rate_limited",
					family.HeaderLimitSubject:   ref.Subject,
					family.HeaderLimitMetric:    ref.Metric,
					family.HeaderLimit:          strconv.FormatInt(ref.Limit, 10),
					family.HeaderLimitRemaining: strconv.FormatInt(ref.Remaining, 10),
				}
				for k, v := range want {
					if got := c.header.Values(k); len(got) != 1 || got[0] != v {
						t.Errorf("%s: %q, want %q", k, got, v)
					}
				}
				reset := c.header.Values(family.HeaderLimitReset)
				if metric == "concurrent_requests" {
					if len(reset) != 0 {
						t.Errorf("%s present for concurrency: %q", family.HeaderLimitReset, reset)
					}
				} else if len(reset) != 1 || reset[0] != strconv.Itoa(int(ref.Reset/time.Second)) {
					t.Errorf("%s: %q, want %d", family.HeaderLimitReset, reset, int(ref.Reset/time.Second))
				}
				if _, err := strconv.Atoi(c.header.Get("Retry-After")); err != nil {
					t.Errorf("Retry-After %q is not an integer", c.header.Get("Retry-After"))
				}
				// A subject kind, never an identifier; the level is not a header.
				for _, id := range []string{testPrincipal.ID, testPrincipal.KeyID, testPrincipal.TenantID.String(), ref.Level + "-level"} {
					if strings.Contains(c.dump(), id) {
						t.Errorf("the answer holds %q", id)
					}
				}
				for k := range c.header {
					if strings.HasPrefix(k, "X-Pandokos-Limit") {
						switch k {
						case family.HeaderLimitSubject, family.HeaderLimitMetric, family.HeaderLimit, family.HeaderLimitRemaining, family.HeaderLimitReset:
						default:
							t.Errorf("unexpected header %s", k)
						}
					}
				}
			})
		}

		t.Run(f.name+"/a provider's own 429 passes unchanged", func(t *testing.T) {
			lim := testutil.NewStaticLimits()
			h := f.newH(t, func(o *gateway.Options) { o.Limits = lim })
			c := f.send(t, h, f.body(fakeprovider.ModelError429, false))
			if c.status != 429 || string(c.body) != string(f.fixture429) {
				t.Fatalf("%d %s", c.status, c.body)
			}
			if c.header.Get("X-Pandokos-Error") != "" {
				t.Errorf("X-Pandokos-Error on a provider's 429: %q", c.header.Get("X-Pandokos-Error"))
			}
			for k := range c.header {
				if strings.HasPrefix(k, "X-Pandokos-Limit") {
					t.Errorf("%s on a provider's 429", k)
				}
			}
			if f.name == "anthropic" && c.header.Get("Retry-After") == "" && c.header.Get("Anthropic-Ratelimit-Requests-Remaining") == "" {
				t.Errorf("the provider's own rate limit headers are gone: %v", c.header)
			}
			if got := lim.Of("settle"); len(got) != 1 || got[0].Usage.Tokens != 0 {
				t.Errorf("settles %+v, want one with 0 tokens", got)
			}
		})
	}
}

// TestRejectedLimitRecord is T-632.
func TestRejectedLimitRecord(t *testing.T) {
	for _, f := range limFamilies() {
		for _, over := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/over limit %v", f.name, over), func(t *testing.T) {
				ref := refusalOf("tokens_per_minute")
				ref.OverLimit = over
				lim := testutil.NewStaticLimits().Refuse(ref)
				h := f.newH(t, func(o *gateway.Options) { o.Limits = lim })
				c := f.send(t, h, f.body("fake-basic", false))
				r := h.oneRecord(t)
				if r.Outcome != usage.OutcomeRejectedLimit || r.ErrorCode != "rate_limited" {
					t.Errorf("outcome %q code %q", r.Outcome, r.ErrorCode)
				}
				if r.Model != "fake-basic" || r.ModelID == "" || r.Endpoint == "" || r.UpstreamModel == "" || r.Provider != f.provider || r.Operation == "" {
					t.Errorf("model fields: %+v", r)
				}
				if r.CredentialID != "" {
					t.Errorf("credential %q: none was opened", r.CredentialID)
				}
				if r.InputSource != usage.SourceNone || r.OutputSource != usage.SourceNone || r.Usage.InputTokens != 0 || r.Usage.OutputTokens != 0 ||
					r.Estimated.InputTokens != 0 || r.Estimated.OutputTokens != 0 {
					t.Errorf("usage: sources %q %q, %+v %+v", r.InputSource, r.OutputSource, r.Usage, r.Estimated)
				}
				if r.CostStatus != "none" || r.Cost.Known {
					t.Errorf("cost status %q %+v", r.CostStatus, r.Cost)
				}
				if r.RequestID != c.header.Get("X-Pandokos-Request-Id") {
					t.Errorf("request ID %q, header %q", r.RequestID, c.header.Get("X-Pandokos-Request-Id"))
				}
				line := h.awaitOutcome(t, "pandokos_error")
				if line["error_code"] != "rate_limited" || line["limit_subject"] != ref.Subject || line["limit_metric"] != ref.Metric ||
					line["limit_level"] != ref.Level || line["limit"] != float64(ref.Limit) || line["retry_after_s"] != float64(17) {
					t.Errorf("request line %v", line)
				}
				if v, ok := line["estimate_over_limit"]; over && v != true {
					t.Errorf("estimate_over_limit %v (present %v), want true", v, ok)
				} else if !over && v == true {
					t.Errorf("estimate_over_limit is true for a refusal that waiting cures")
				}
				if _, ok := line["reserved_tokens"]; ok {
					t.Errorf("reserved_tokens on a refusal: %v", line["reserved_tokens"])
				}
				testutil.AssertNoCanary(t, "the logs", h.logs.String())
				server, client := endedSpans(t, h, 1)
				if client != nil {
					t.Error("a client span for a refusal")
				}
				sa := attrs(server)
				if sa["pandokos.outcome"].AsString() != "rejected_limit" || sa["pandokos.error.code"].AsString() != "rate_limited" {
					t.Errorf("span attributes %v", sa)
				}
				if server.Status().Code != codes.Unset {
					t.Errorf("span status %v for a refusal", server.Status().Code)
				}
			})
		}
	}
}

// recordTokens is what a record says was consumed: input plus output of the figures its cost was
// computed on, provider figures for a side the provider reported and the estimate for the other.
func recordTokens(r usage.Record) int64 {
	var n int64
	switch r.InputSource {
	case usage.SourceProvider:
		n += r.Usage.InputTokens
	case usage.SourceEstimated:
		n += r.Estimated.InputTokens
	}
	switch r.OutputSource {
	case usage.SourceProvider:
		n += r.Usage.OutputTokens
	case usage.SourceEstimated:
		n += r.Estimated.OutputTokens
	}
	return n
}

// onlySettle returns the usage of the one settle of a one-reservation run and fails when a
// reservation was not settled exactly once or was released.
func onlySettle(t *testing.T, lim *testutil.StaticLimits) gateway.LimitUsage {
	t.Helper()
	testutil.Eventually(t, 3*time.Second, "the reservation to end", func() bool { return lim.Admitted() == 1 && lim.Open() == 0 })
	time.Sleep(30 * time.Millisecond)
	s, r := lim.Of("settle"), lim.Of("release")
	if len(s) != 1 || len(r) != 0 {
		t.Fatalf("%d settles and %d releases, want 1 and 0: %+v", len(s), len(r), lim.Calls())
	}
	return s[0].Usage
}

// TestSettleEqualsRecord is T-633.
func TestSettleEqualsRecord(t *testing.T) {
	type sc struct {
		name   string
		model  string
		stream bool
		want   int64 // the tokens expected when the figures are known: -1 for "whatever the record says"
	}
	for _, f := range limFamilies() {
		cases := []sc{
			{"provider usage", fakeprovider.ModelBasic, false, f.fixtureTokens},
			{"provider usage, stream", fakeprovider.ModelBasic, true, f.fixtureTokens},
			{"estimated usage", fakeprovider.ModelNoUsage, false, -1},
			{"estimated usage, stream", fakeprovider.ModelNoUsage, true, -1},
			{"a provider error", fakeprovider.ModelError500Echo, false, 0},
			{"a provider's 429", fakeprovider.ModelError429, false, 0},
		}
		if f.name == "anthropic" {
			cases = append(cases, sc{"sources mixed: the stream is cut after its first event", fakeprovider.ModelCutAfterStart, true, -1})
		}
		for _, c := range cases {
			t.Run(f.name+"/"+c.name, func(t *testing.T) {
				lim := testutil.NewStaticLimits()
				h := f.newH(t, func(o *gateway.Options) { o.Limits = lim })
				resp := f.send(t, h, f.body(c.model, c.stream))
				_ = resp
				r := h.oneRecord(t)
				u := onlySettle(t, lim)
				if want := recordTokens(r); u.Tokens != want {
					t.Errorf("settled %d tokens, the record holds %d (sources %q %q)", u.Tokens, want, r.InputSource, r.OutputSource)
				}
				if c.want >= 0 && u.Tokens != c.want {
					t.Errorf("settled %d tokens, want %d", u.Tokens, c.want)
				}
				if c.want < 0 && u.Tokens == 0 && r.InputSource != usage.SourceNone {
					t.Errorf("an estimated record settled 0 tokens")
				}
			})
		}

		t.Run(f.name+"/unreachable provider settles 0", func(t *testing.T) {
			lim := testutil.NewStaticLimits()
			h := f.newH(t, func(o *gateway.Options) { o.Limits = lim })
			tgt := h.targets[f.provider+"/fake-basic"]
			tgt.BaseURL, _ = urlOf("http://127.0.0.1:1")
			h.targets[f.provider+"/fake-basic"] = tgt
			f.send(t, h, f.body("fake-basic", false))
			if u := onlySettle(t, lim); u.Tokens != 0 {
				t.Errorf("settled %d tokens", u.Tokens)
			}
		})
	}

	t.Run("anthropic count_tokens settles 0", func(t *testing.T) {
		lim := testutil.NewStaticLimits()
		h := newFamilies(t, func(o *gateway.Options) { o.Limits = lim })
		if c := h.sendA(t, countRoute, messages(fakeprovider.ModelBasic, false), anthropicHdr()); c.status != 200 {
			t.Fatalf("%d", c.status)
		}
		if u := onlySettle(t, lim); u.Tokens != 0 {
			t.Errorf("settled %d tokens", u.Tokens)
		}
	})

	t.Run("the cost of the record reaches the limiter", func(t *testing.T) {
		lim := testutil.NewStaticLimits()
		h := newHarness(t, func(o *gateway.Options) { o.Limits = lim })
		h.sendAuthed(t, route, chat("fake-basic", false))
		r := h.oneRecord(t)
		u := onlySettle(t, lim)
		if !r.Cost.Known || !u.Cost.Known || u.Cost.Amount.String() != r.Cost.Amount.String() {
			t.Errorf("settled cost %+v, record cost %+v", u.Cost, r.Cost)
		}
	})
}

// blockingCreds blocks Open until the request ends.
type blockingCreds struct{ opened atomic.Int64 }

func (b *blockingCreds) Open(ctx context.Context, _ authz.Principal, _, _ string) (gateway.Credential, error) {
	b.opened.Add(1)
	<-ctx.Done()
	return gateway.Credential{}, ctx.Err()
}

// TestReleaseWhenNotForwarded is T-634: when nothing was sent to the provider the reservation is
// released once and never settled.
func TestReleaseWhenNotForwarded(t *testing.T) {
	for _, f := range limFamilies() {
		release := func(t *testing.T, lim *testutil.StaticLimits) {
			t.Helper()
			testutil.Eventually(t, 3*time.Second, "the reservation to end", func() bool { return lim.Admitted() == 1 && lim.Open() == 0 })
			time.Sleep(30 * time.Millisecond)
			if s, r := lim.Of("settle"), lim.Of("release"); len(s) != 0 || len(r) != 1 {
				t.Fatalf("%d settles and %d releases, want 0 and 1: %+v", len(s), len(r), lim.Calls())
			}
		}
		for name, tc := range map[string]struct {
			mutate func(*gateway.Options)
			code   family.ErrorCode
		}{
			"no credential":                    {func(o *gateway.Options) { o.Credentials = testutil.StaticCredentials(nil) }, family.CodeNoCredential},
			"the principal may not use one":    {func(o *gateway.Options) { o.Credentials = &testutil.Credentials{Err: gateway.ErrCredentialDenied} }, family.CodeForbidden},
			"a credential that is unavailable": {func(o *gateway.Options) { o.Credentials = &testutil.Credentials{Err: errors.New("synthetic")} }, family.CodeUnavailable},
		} {
			t.Run(f.name+"/"+name, func(t *testing.T) {
				lim := testutil.NewStaticLimits()
				h := f.newH(t, func(o *gateway.Options) { o.Limits = lim; tc.mutate(o) })
				c := f.send(t, h, f.body("fake-basic", false))
				f.contract(t, name, c, tc.code)
				release(t, lim)
				if n := len(h.up.Requests()); n != 0 {
					t.Errorf("%d requests reached the provider", n)
				}
			})
		}

		t.Run(f.name+"/the caller goes away before the forward", func(t *testing.T) {
			lim := testutil.NewStaticLimits()
			creds := &blockingCreds{}
			h := f.newH(t, func(o *gateway.Options) { o.Limits = lim; o.Credentials = creds })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				resp, err := h.request(ctx, t, http.MethodPost, f.path, f.body("fake-basic", false), f.hdr())
				if err == nil {
					resp.Body.Close()
				}
			}()
			testutil.Eventually(t, 3*time.Second, "the credential to be asked for", func() bool { return creds.opened.Load() == 1 })
			cancel()
			<-done
			release(t, lim)
			if n := len(h.up.Requests()); n != 0 {
				t.Errorf("%d requests reached the provider", n)
			}
		})
	}
}

// TestEveryEndSettles is T-635: however a request ends, its reservation is settled or released,
// exactly once and never both.
func TestEveryEndSettles(t *testing.T) {
	exactlyOne := func(t *testing.T, lim *testutil.StaticLimits) {
		t.Helper()
		testutil.Eventually(t, 3*time.Second, "the reservation to end", func() bool { return lim.Admitted() == 1 && lim.Open() == 0 })
		time.Sleep(50 * time.Millisecond)
		settled, released := lim.Ended()
		if settled[1]+released[1] != 1 {
			t.Fatalf("settled %d and released %d times: %+v", settled[1], released[1], lim.Calls())
		}
	}
	short := func(o *gateway.Options) { o.MaxRequestDuration = 300 * time.Millisecond }

	for _, f := range limFamilies() {
		t.Run(f.name+"/the client disconnects mid-stream: settles what the record holds", func(t *testing.T) {
			lim := testutil.NewStaticLimits()
			h := f.newH(t, func(o *gateway.Options) { o.Limits = lim })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			resp, err := h.request(ctx, t, http.MethodPost, f.path, f.body(fakeprovider.ModelGatedStream, true), f.hdr())
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if _, err := testutil.ReadEvent(bufio.NewReader(resp.Body)); err != nil {
				t.Fatal(err)
			}
			cancel()
			if !h.up.WaitCancelled(2 * time.Second) {
				t.Fatal("the provider request was not cancelled")
			}
			r := h.oneRecord(t)
			u := onlySettle(t, lim)
			if u.Tokens != recordTokens(r) {
				t.Errorf("settled %d, the record holds %d", u.Tokens, recordTokens(r))
			}
		})

		t.Run(f.name+"/the upstream aborts mid-stream", func(t *testing.T) {
			lim := testutil.NewStaticLimits()
			h := f.newH(t, func(o *gateway.Options) { o.Limits = lim })
			model := fakeprovider.ModelCut
			if f.name == "anthropic" {
				model = fakeprovider.ModelCutAfterStart
			}
			resp, err := h.request(ctx5(t), t, http.MethodPost, f.path, f.body(model, true), f.hdr())
			if err == nil {
				_, _ = io.ReadAll(resp.Body)
				resp.Body.Close()
			}
			r := h.oneRecord(t)
			if u := onlySettle(t, lim); u.Tokens != recordTokens(r) {
				t.Errorf("settled %d, the record holds %d", u.Tokens, recordTokens(r))
			}
		})

		t.Run(f.name+"/max_request_duration passes", func(t *testing.T) {
			lim := testutil.NewStaticLimits()
			h := f.newH(t, func(o *gateway.Options) { short(o); o.Limits = lim })
			f.send(t, h, f.body(fakeprovider.ModelHang, false))
			exactlyOne(t, lim)
		})

		t.Run(f.name+"/max_request_duration passes mid-stream", func(t *testing.T) {
			lim := testutil.NewStaticLimits()
			h := f.newH(t, func(o *gateway.Options) { short(o); o.Limits = lim })
			resp, err := h.request(ctx5(t), t, http.MethodPost, f.path, f.body(fakeprovider.ModelGatedStream, true), f.hdr())
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			exactlyOne(t, lim)
		})

		t.Run(f.name+"/a panic before the response header", func(t *testing.T) {
			lim := testutil.NewStaticLimits()
			h := f.newH(t, func(o *gateway.Options) { o.Private = panicTransport{}; o.Limits = lim })
			f.send(t, h, f.body("fake-basic", false))
			exactlyOne(t, lim)
			if r := h.oneRecord(t); r.Outcome != usage.OutcomeInternalError {
				t.Errorf("outcome %q", r.Outcome)
			}
		})

		t.Run(f.name+"/a panic after the response header", func(t *testing.T) {
			lim := testutil.NewStaticLimits()
			h := f.newH(t, func(o *gateway.Options) { o.Private = bodyPanicTransport{}; o.Limits = lim })
			resp, err := h.request(ctx5(t), t, http.MethodPost, f.path, f.body("fake-basic", false), f.hdr())
			if err == nil {
				_, _ = io.ReadAll(resp.Body)
				resp.Body.Close()
			}
			exactlyOne(t, lim)
		})
	}

	t.Run("a forced shutdown", func(t *testing.T) {
		lim := testutil.NewStaticLimits()
		h := newHarness(t, func(o *gateway.Options) { o.Limits = lim })
		shutdown, stop := context.WithCancelCause(context.Background())
		stop(gateway.ErrShutdown)
		req, _ := http.NewRequestWithContext(shutdown, http.MethodPost, route, strings.NewReader(string(chat("fake-basic", false))))
		req.Header = authed()
		func() {
			defer func() { _ = recover() }()
			h.handler.ServeHTTP(newDiscard(), req)
		}()
		exactlyOne(t, lim)
	})

	t.Run("a forced shutdown during a stream", func(t *testing.T) {
		lim := testutil.NewStaticLimits()
		h := newHarness(t, func(o *gateway.Options) { o.Limits = lim })
		shutdown, stop := context.WithCancelCause(context.Background())
		defer stop(nil)
		resp, err := h.request(shutdown, t, http.MethodPost, route, chat(fakeprovider.ModelGatedStream, true), authed())
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if _, err := testutil.ReadEvent(bufio.NewReader(resp.Body)); err != nil {
			t.Fatal(err)
		}
		stop(gateway.ErrShutdown)
		exactlyOne(t, lim)
	})

	t.Run("a usage reader that panics", func(t *testing.T) {
		lim := testutil.NewStaticLimits()
		h := newHarness(t, func(o *gateway.Options) {
			o.Limits = lim
			o.Families = map[string]family.Adapter{"openai": softAdapter{
				Adapter: openai.Adapter(),
				newReader: func(op family.Operation, strip bool) family.StreamReader {
					return &brokenReader{real: openai.NewStreamReaderFor(op.Name, strip), panicUsage: true}
				},
			}}
		})
		if c := h.sendAuthed(t, route, chat("fake-basic", true)); c.status != 200 {
			t.Fatalf("%d", c.status)
		}
		exactlyOne(t, lim)
	})
}

// TestLimiterFailures is T-636.
func TestLimiterFailures(t *testing.T) {
	for _, f := range limFamilies() {
		t.Run(f.name+"/Reserve returns an error", func(t *testing.T) {
			lim := testutil.NewStaticLimits().Fail(errors.New("synthetic limits failure"))
			h := f.newH(t, func(o *gateway.Options) { o.Limits = lim })
			c := f.send(t, h, f.body("fake-basic", false))
			f.contract(t, "unavailable", c, family.CodeUnavailable)
			if c.header.Get("Retry-After") != "5" {
				t.Errorf("Retry-After %q, want 5", c.header.Get("Retry-After"))
			}
			r := h.oneRecord(t)
			if r.Outcome != usage.OutcomeInternalError || r.ErrorCode != "unavailable" {
				t.Errorf("record %q %q", r.Outcome, r.ErrorCode)
			}
			if n := len(h.creds.Calls()) + len(h.up.Requests()); n != 0 {
				t.Errorf("%d credential opens or provider requests after a failed reserve", n)
			}
			if lim.Admitted() != 0 || len(lim.Of("settle"))+len(lim.Of("release")) != 0 {
				t.Errorf("calls %+v", lim.Calls())
			}
		})

		t.Run(f.name+"/a Settle that panics changes neither the answer nor the record", func(t *testing.T) {
			clean := f.newH(t, nil)
			want := f.send(t, clean, f.body("fake-basic", false))
			wantRec := clean.oneRecord(t)

			lim := testutil.NewStaticLimits()
			lim.PanicOnSettle = true
			h := f.newH(t, func(o *gateway.Options) { o.Limits = lim })
			c := f.send(t, h, f.body("fake-basic", false))
			if c.status != want.status || string(c.body) != string(want.body) {
				t.Errorf("answer %d %s, want %d %s", c.status, c.body, want.status, want.body)
			}
			r := h.oneRecord(t)
			if r.Outcome != wantRec.Outcome || r.ErrorCode != wantRec.ErrorCode || recordTokens(r) != recordTokens(wantRec) {
				t.Errorf("record %q %q %d, want %q %q %d", r.Outcome, r.ErrorCode, recordTokens(r), wantRec.Outcome, wantRec.ErrorCode, recordTokens(wantRec))
			}
			assertOneLimitErrorLine(t, h, c.header.Get("X-Pandokos-Request-Id"))
		})

		t.Run(f.name+"/a Release that panics changes neither the answer nor the record", func(t *testing.T) {
			lim := testutil.NewStaticLimits()
			lim.PanicOnRelease = true
			h := f.newH(t, func(o *gateway.Options) { o.Limits = lim; o.Credentials = testutil.StaticCredentials(nil) })
			c := f.send(t, h, f.body("fake-basic", false))
			f.contract(t, "no credential", c, family.CodeNoCredential)
			r := h.oneRecord(t)
			if r.Outcome != usage.OutcomeRejectedPolicy || r.ErrorCode != "no_credential" {
				t.Errorf("record %q %q", r.Outcome, r.ErrorCode)
			}
			assertOneLimitErrorLine(t, h, c.header.Get("X-Pandokos-Request-Id"))
		})
	}
}

// assertOneLimitErrorLine checks that the limiter's failure was logged once, at error, with the
// request ID and the type of the panic and nothing of the panic's value.
func assertOneLimitErrorLine(t *testing.T, h *harness, requestID string) {
	t.Helper()
	testutil.Eventually(t, 3*time.Second, "the limiter failure to be logged", func() bool {
		for _, l := range h.logs.Lines(t) {
			if msg, _ := l["msg"].(string); strings.HasPrefix(msg, "limit ") && strings.Contains(msg, "failed") {
				return true
			}
		}
		return false
	})
	n := 0
	for _, l := range h.logs.Lines(t) {
		if msg, _ := l["msg"].(string); strings.HasPrefix(msg, "limit ") && strings.Contains(msg, "failed") {
			n++
			if l["level"] != "ERROR" || l["request_id"] != requestID || l["panic_type"] == nil {
				t.Errorf("log line %v: want level ERROR, request_id %q and panic_type", l, requestID)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d limiter failure lines, want 1", n)
	}
	if strings.Contains(h.logs.String(), "synthetic panic in") {
		t.Error("the value of the panic was logged")
	}
}

// urlOf parses a URL.
func urlOf(s string) (*url.URL, error) { return url.Parse(s) }

// newDiscard returns a ResponseWriter that discards everything.
func newDiscard() http.ResponseWriter { return httptest.NewRecorder() }

// bytesReader returns a reader of b.
func bytesReader(b []byte) io.Reader { return strings.NewReader(string(b)) }
