package telemetry_test

import (
	"math"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/telemetry"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func attrs(kv ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

func hasKey(p point, key string) bool { _, ok := p.Attrs[key]; return ok }

// TestRequestRecording is T-913: what each observation records on which instrument.
func TestRequestRecording(t *testing.T) {
	r := newRig(t, nil)

	forwarded := func(model string, mutate func(*telemetry.Request)) telemetry.Request {
		q := chatRequest(model)
		if mutate != nil {
			mutate(&q)
		}
		return q
	}
	snap := r.observe(
		// Not forwarded: a refusal before step 9 records M1 and M2 and no provider call.
		telemetry.Request{Method: "POST", Scheme: "https", Route: "/{provider}/v1/chat/completions", Status: 429, Outcome: "pandokos_error",
			ErrorCode: "rate_limited", LimitState: "limited", Provider: "acme", Family: "openai", Operation: "chat", Model: "m-refused",
			Duration: 2 * time.Millisecond, InputSource: "none", OutputSource: "none"},
		// Forwarded, not a stream: M3 without error.type, no M4.
		forwarded("m-chat", nil),
		// A stream with a first chunk: M3 and M4.
		forwarded("m-stream", func(q *telemetry.Request) { q.Stream, q.FirstChunk = true, 40*time.Millisecond }),
		// A stream with no first byte: M3 and no M4.
		forwarded("m-stream0", func(q *telemetry.Request) { q.Stream, q.FirstChunk = true, 0 }),
		// A non-stream with a FirstChunk set by mistake: M4 is for streams only.
		forwarded("m-nostream", func(q *telemetry.Request) { q.Stream, q.FirstChunk = false, 10*time.Millisecond }),
		// Tokens: input from the provider, output estimated; a side with source none records nothing.
		forwarded("m-tok", func(q *telemetry.Request) {
			q.InputTokens, q.InputSource, q.OutputTokens, q.OutputSource = 100, "provider", 30, "estimated"
		}),
		forwarded("m-none", func(q *telemetry.Request) {
			q.InputTokens, q.InputSource, q.OutputTokens, q.OutputSource, q.CostKnown = 0, "none", 0, "none", false
			q.CostReason = "no_price"
		}),
		forwarded("m-zero", func(q *telemetry.Request) { q.InputTokens, q.OutputTokens = 0, 20 }),
		// Cost: two currencies kept apart, never added.
		forwarded("m-cost", nil),
		forwarded("m-cost", nil),
		forwarded("m-cost", func(q *telemetry.Request) { q.CostAmount, q.CostCurrency = 0.5, "USD" }),
		// Unknown cost with usage: M8 by reason, no M7.
		forwarded("m-unknown", func(q *telemetry.Request) {
			q.CostKnown, q.CostAmount, q.CostCurrency, q.CostReason = false, 0, "", "no_price"
		}),
		forwarded("m-unknown", func(q *telemetry.Request) {
			q.CostKnown, q.CostAmount, q.CostCurrency, q.CostReason = false, 0, "", "inconsistent_usage"
		}),
		// The provider's 429: M9 with the credential; M3 error.type 429.
		forwarded("m-429", func(q *telemetry.Request) {
			q.Status, q.UpstreamStatus, q.CallError, q.CredentialID = 429, 429, "429", "cred-0001"
		}),
		// A 500 never records M9, even with a credential ID set by mistake.
		forwarded("m-500", func(q *telemetry.Request) {
			q.Status, q.UpstreamStatus, q.CallError, q.CredentialID = 500, 500, "500", "cred-0002"
		}),
		// A timeout and a cancellation: the closed classes.
		forwarded("m-timeout", func(q *telemetry.Request) { q.Status, q.Outcome, q.CallError = 504, "timeout", "timeout" }),
		forwarded("m-cancel", func(q *telemetry.Request) { q.Status, q.Outcome, q.CallError = 0, "client_cancelled", "cancelled" }),
		// A refusal of the limits: M10 with the kinds only.
		telemetry.Request{Method: "POST", Scheme: "https", Route: "/{provider}/v1/chat/completions", Status: 429, Outcome: "pandokos_error",
			ErrorCode: "quota_exceeded", LimitState: "limited", Provider: "acme", Family: "openai", Operation: "chat", Model: "m-limit",
			Duration: time.Millisecond, InputSource: "none", OutputSource: "none",
			Refusal: &telemetry.RefusalObservation{Code: "quota_exceeded", Metric: "cost_per_day", Level: "platform", SubjectKind: "tenant", Scope: "provider"}},
		// Estimates: tokens ratio 0.6 and cost ratio 0.5; a reserved quantity of 0 gives no ratio; a cost
		// that is not comparable gives no cost ratio.
		forwarded("m-est", func(q *telemetry.Request) {
			q.Estimate = &telemetry.EstimateObservation{ReservedTokens: 1000, SettledTokens: 600, ReservedCost: 1, SettledCost: 0.5, CostComparable: true}
		}),
		forwarded("m-est0", func(q *telemetry.Request) {
			q.Estimate = &telemetry.EstimateObservation{ReservedTokens: 0, SettledTokens: 600, ReservedCost: 0, SettledCost: 0.5, CostComparable: true}
		}),
		forwarded("m-estnc", func(q *telemetry.Request) {
			q.Estimate = &telemetry.EstimateObservation{ReservedTokens: 200, SettledTokens: 100, ReservedCost: 1, SettledCost: 0, CostComparable: false}
		}),
		// M1 error.type: the status when >= 500, the outcome when no status was written, nothing otherwise.
		telemetry.Request{Method: "GET", Scheme: "http", Status: 0, Outcome: "client_cancelled", LimitState: "not_evaluated", Duration: time.Millisecond, InputSource: "none", OutputSource: "none"},
		telemetry.Request{Method: "_OTHER", Scheme: "https", Status: 404, Outcome: "pandokos_error", ErrorCode: "endpoint_not_supported", LimitState: "not_evaluated", Duration: time.Millisecond, InputSource: "none", OutputSource: "none"},
	)
	model := func(m string) map[string]string { return attrs("gen_ai.request.model", m) }

	t.Run("M2 counts every request, with the route's dimensions only when matched", func(t *testing.T) {
		if got := snap.total(t, "pandokos.gateway.requests", nil); got != 23 {
			t.Errorf("requests = %v, want 23", got)
		}
		unmatched := snap.find(t, "pandokos.gateway.requests", attrs("pandokos.error_code", "endpoint_not_supported"))
		if len(unmatched) != 1 {
			t.Fatalf("unmatched request: %d points", len(unmatched))
		}
		for _, k := range []string{"gen_ai.provider.name", "pandokos.api_family", "gen_ai.operation.name", "gen_ai.request.model"} {
			if hasKey(unmatched[0], k) {
				t.Errorf("an unmatched request has %s", k)
			}
		}
		if unmatched[0].Attrs["pandokos.outcome"] != "pandokos_error" || unmatched[0].Attrs["pandokos.limit_state"] != "not_evaluated" {
			t.Errorf("attributes %v", unmatched[0].Attrs)
		}
		ok := snap.find(t, "pandokos.gateway.requests", model("m-chat"))
		if len(ok) != 1 || hasKey(ok[0], "pandokos.error_code") || ok[0].Attrs["gen_ai.provider.name"] != "acme" || ok[0].Attrs["pandokos.api_family"] != "openai" || ok[0].Attrs["gen_ai.operation.name"] != "chat" {
			t.Errorf("m-chat: %v", ok)
		}
	})

	t.Run("M3 and M4 only when forwarded, M4 for streams with a first chunk", func(t *testing.T) {
		if n := snap.total(t, "gen_ai.client.operation.duration", model("m-refused")); n != 0 {
			t.Errorf("a refused request recorded %v provider calls", n)
		}
		if n := snap.total(t, "gen_ai.client.operation.duration", model("m-chat")); n != 1 {
			t.Errorf("m-chat M3 count %v", n)
		}
		if n := snap.total(t, "gen_ai.client.operation.time_to_first_chunk", model("m-chat")); n != 0 {
			t.Errorf("a non-stream recorded a first chunk: %v", n)
		}
		if n := snap.total(t, "gen_ai.client.operation.time_to_first_chunk", model("m-nostream")); n != 0 {
			t.Errorf("a non-stream with FirstChunk set recorded M4: %v", n)
		}
		if n := snap.total(t, "gen_ai.client.operation.time_to_first_chunk", model("m-stream0")); n != 0 {
			t.Errorf("a stream without a first byte recorded M4: %v", n)
		}
		pts := snap.find(t, "gen_ai.client.operation.time_to_first_chunk", model("m-stream"))
		if len(pts) != 1 || pts[0].Count != 1 || !near(pts[0].Sum, 0.04) {
			t.Errorf("m-stream M4: %+v", pts)
		}
		m3 := snap.find(t, "gen_ai.client.operation.duration", model("m-chat"))
		if len(m3) != 1 || !near(m3[0].Sum, 0.1) || hasKey(m3[0], "error.type") {
			t.Errorf("m-chat M3 (seconds, no error.type): %+v", m3)
		}
	})

	t.Run("M3 error.type per section 5.3.1", func(t *testing.T) {
		for m, want := range map[string]string{"m-429": "429", "m-500": "500", "m-timeout": "timeout", "m-cancel": "cancelled"} {
			pts := snap.find(t, "gen_ai.client.operation.duration", model(m))
			if len(pts) != 1 || pts[0].Attrs["error.type"] != want {
				t.Errorf("%s: %+v, want error.type %q", m, pts, want)
			}
		}
	})

	t.Run("M5 one point per provider-reported side, M6 for estimated sides", func(t *testing.T) {
		in := snap.find(t, "gen_ai.client.token.usage", model("m-tok"))
		if len(in) != 1 || in[0].Attrs["gen_ai.token.type"] != "input" || in[0].Count != 1 || !near(in[0].Sum, 100) {
			t.Errorf("m-tok M5: %+v", in)
		}
		est := snap.find(t, "pandokos.gen_ai.client.estimated_tokens", model("m-tok"))
		if len(est) != 1 || est[0].Attrs["gen_ai.token.type"] != "output" || !near(est[0].Value, 30) {
			t.Errorf("m-tok M6: %+v", est)
		}
		if n := len(snap.find(t, "gen_ai.client.token.usage", model("m-none"))) + len(snap.find(t, "pandokos.gen_ai.client.estimated_tokens", model("m-none"))); n != 0 {
			t.Errorf("sides with source none recorded %d points", n)
		}
		if pts := snap.find(t, "gen_ai.client.token.usage", attrs("gen_ai.request.model", "m-zero", "gen_ai.token.type", "input")); len(pts) != 0 {
			t.Errorf("a provider side with zero tokens recorded M5: %+v", pts)
		}
	})

	t.Run("M7 in its currency, never added across currencies; M8 by reason", func(t *testing.T) {
		eur := snap.find(t, "pandokos.gen_ai.client.cost", attrs("gen_ai.request.model", "m-cost", "pandokos.currency", "EUR"))
		usd := snap.find(t, "pandokos.gen_ai.client.cost", attrs("gen_ai.request.model", "m-cost", "pandokos.currency", "USD"))
		if len(eur) != 1 || !near(eur[0].Value, 0.5) || len(usd) != 1 || !near(usd[0].Value, 0.5) {
			t.Errorf("EUR %+v USD %+v", eur, usd)
		}
		if n := snap.total(t, "pandokos.gen_ai.client.cost", model("m-unknown")); n != 0 {
			t.Errorf("an unknown cost was added: %v", n)
		}
		for _, reason := range []string{"no_price", "inconsistent_usage"} {
			if n := snap.total(t, "pandokos.gen_ai.client.cost_unknown", attrs("gen_ai.request.model", "m-unknown", "pandokos.cost.reason", reason)); n != 1 {
				t.Errorf("cost_unknown %s = %v, want 1", reason, n)
			}
		}
		if n := snap.total(t, "pandokos.gen_ai.client.cost_unknown", model("m-none")); n != 0 {
			t.Errorf("a record without usage counted as an unknown cost: %v", n)
		}
	})

	t.Run("M9 only for a 429, with the provider and the credential", func(t *testing.T) {
		pts := snap.points(t, "pandokos.provider.rate_limited")
		if len(pts) != 1 {
			t.Fatalf("M9 points: %+v", pts)
		}
		if got := pts[0].Attrs; len(got) != 2 || got["gen_ai.provider.name"] != "acme" || got["pandokos.credential.id"] != "cred-0001" || pts[0].Value != 1 {
			t.Errorf("M9 point: %+v", pts[0])
		}
	})

	t.Run("M10 carries the kinds of the refusal", func(t *testing.T) {
		pts := snap.points(t, "pandokos.limits.refusals")
		if len(pts) != 1 {
			t.Fatalf("M10 points: %+v", pts)
		}
		want := attrs("pandokos.limit.code", "quota_exceeded", "pandokos.limit.metric", "cost_per_day", "pandokos.limit.level", "platform",
			"pandokos.limit.subject", "tenant", "pandokos.limit.scope", "provider")
		if !includes(pts[0].Attrs, want) || len(pts[0].Attrs) != len(want) || pts[0].Value != 1 {
			t.Errorf("M10 point: %+v", pts[0])
		}
	})

	t.Run("M11 ratios of settled over reserved", func(t *testing.T) {
		tokens := snap.find(t, "pandokos.limits.estimate_ratio", attrs("pandokos.limit.quantity", "tokens"))
		cost := snap.find(t, "pandokos.limits.estimate_ratio", attrs("pandokos.limit.quantity", "cost"))
		if len(tokens) != 1 || tokens[0].Count != 2 || !near(tokens[0].Sum, 0.6+0.5) {
			t.Errorf("tokens ratios (m-est 0.6 and m-estnc 0.5; m-est0 reserved nothing): %+v", tokens)
		}
		if len(cost) != 1 || cost[0].Count != 1 || !near(cost[0].Sum, 0.5) {
			t.Errorf("cost ratios (m-est only: m-estnc is not comparable, m-est0 reserved nothing): %+v", cost)
		}
	})

	t.Run("M1 error.type", func(t *testing.T) {
		var cancelled, notFound, ok200 []point
		for _, p := range snap.points(t, "http.server.request.duration") {
			switch {
			case !hasKey(p, "http.response.status_code"):
				cancelled = append(cancelled, p)
			case p.Attrs["http.response.status_code"] == "404":
				notFound = append(notFound, p)
			case p.Attrs["http.response.status_code"] == "200":
				ok200 = append(ok200, p)
			}
		}
		if len(cancelled) != 1 || cancelled[0].Attrs["error.type"] != "client_cancelled" {
			t.Errorf("no status written: %+v, want error.type=client_cancelled", cancelled)
		}
		if len(notFound) != 1 || hasKey(notFound[0], "error.type") || notFound[0].Attrs["http.request.method"] != "_OTHER" {
			t.Errorf("a 404: %+v (error.type only from 500)", notFound)
		}
		for _, p := range ok200 {
			if hasKey(p, "error.type") {
				t.Errorf("a 200 has error.type: %+v", p)
			}
		}
		if cancelled != nil && hasKey(cancelled[0], "http.route") {
			t.Error("an unmatched request has http.route")
		}
		for _, p := range ok200 {
			if p.Attrs["http.route"] != "/{provider}/v1/chat/completions" || p.Attrs["url.scheme"] != "https" || p.Attrs["http.request.method"] != "POST" {
				t.Errorf("a 200 point: %+v", p.Attrs)
			}
		}
	})

	t.Run("M1 error.type is the status as text from 500", func(t *testing.T) {
		r2 := newRig(t, nil)
		s2 := r2.observe(telemetry.Request{Method: "POST", Scheme: "https", Route: "/{provider}/v1/chat/completions", Status: 502,
			Outcome: "pandokos_error", ErrorCode: "upstream_unreachable", LimitState: "unlimited", Provider: "acme", Family: "openai",
			Operation: "chat", Model: "m", Duration: time.Millisecond, InputSource: "none", OutputSource: "none"})
		pts := s2.find(t, "http.server.request.duration", attrs("http.response.status_code", "502"))
		if len(pts) != 1 || pts[0].Attrs["error.type"] != "502" {
			t.Errorf("a 502: %+v", pts)
		}
	})
}
