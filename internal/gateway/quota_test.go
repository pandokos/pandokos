package gateway_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/limitstest"
	"github.com/pandokos/pandokos/test/testutil"
)

// recordLedger is a limits.Ledger over the records a MemoryRecorder holds: it aggregates them the
// way the query of the database does, for the tests that run without the database.
type recordLedger struct {
	rec   *testutil.MemoryRecorder
	shift func() time.Duration // how far the clock of the limits is ahead of the real time
}

func (l recordLedger) Usage(_ context.Context, t tenant.ID, from, liveFrom, to time.Time) ([]limits.UsageGroup, error) {
	type key struct {
		minute                                int64
		principal, key, provider, model, curr string
	}
	groups := map[key]*limits.UsageGroup{}
	for _, r := range l.rec.Records() {
		started := r.StartedAt
		if l.shift != nil {
			started = started.Add(l.shift())
		}
		if r.TenantID != t || r.CredentialID == "" || started.Before(from) || !started.Before(to) {
			continue
		}
		k := key{principal: r.PrincipalID, key: r.KeyID, provider: r.Provider, model: r.Model}
		if !started.Before(liveFrom) {
			k.minute = started.Unix() / 60
		}
		if r.Cost.Known {
			k.curr = r.Cost.Currency
		}
		g := groups[k]
		if g == nil {
			g = &limits.UsageGroup{Minute: k.minute, PrincipalID: k.principal, KeyID: k.key, Provider: k.provider, Model: k.model, Currency: k.curr}
			groups[k] = g
		}
		g.Requests++
		g.Tokens += recordTokens(r)
		if r.Cost.Known {
			g.Cost = g.Cost.Add(r.Cost.Amount)
		}
	}
	out := make([]limits.UsageGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	return out, nil
}

// numText is a number of a JSON log line as text: a decimal is a string or a number, an integer
// a number that is not written in exponent form.
func numText(v any) string {
	if f, ok := v.(float64); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// quotaRefusal is a refusal of a period limit, with values that differ between the metrics.
func quotaRefusal(metric string) gateway.LimitRefusal {
	switch metric {
	case "requests_per_day":
		return gateway.LimitRefusal{Code: "quota_exceeded", Scope: "all", Level: "tenant", Subject: "tenant", Metric: metric,
			Limit: 100, Remaining: 0, RetryAfter: time.Hour, Reset: time.Hour}
	case "requests_per_month":
		return gateway.LimitRefusal{Code: "quota_exceeded", Scope: "model", Level: "tenant", Subject: "key", Metric: metric,
			Limit: 5000, Remaining: 17, RetryAfter: 19*24*time.Hour + 5*time.Second, Reset: 19*24*time.Hour + 5*time.Second}
	case "tokens_per_day":
		return gateway.LimitRefusal{Code: "quota_exceeded", Scope: "provider", Level: "platform", Subject: "service_account", Metric: metric,
			Limit: 2_000_000, Remaining: 1500, RetryAfter: 7261 * time.Second, Reset: 7261 * time.Second}
	case "tokens_per_month":
		return gateway.LimitRefusal{Code: "quota_exceeded", Scope: "all", Level: "tenant", Subject: "tenant", Metric: metric,
			Limit: 1_000_000_000_000, Remaining: 999_999_999_999, RetryAfter: 30 * 24 * time.Hour, Reset: 30 * 24 * time.Hour}
	case "cost_per_day":
		return gateway.LimitRefusal{Code: "quota_exceeded", Scope: "model", Level: "tenant", Subject: "tenant", Metric: metric, Currency: "EUR",
			Limit: 250_500_000, Remaining: 0, RetryAfter: 41_000 * time.Second, Reset: 41_000 * time.Second}
	}
	return gateway.LimitRefusal{Code: "quota_exceeded", Scope: "provider", Level: "platform", Subject: "tenant", Metric: metric, Currency: "USD",
		Limit: 1_234_567, Remaining: 100_000, RetryAfter: 86_400 * time.Second, Reset: 86_400 * time.Second}
}

// TestQuotaExceeded is T-665: the 429 quota_exceeded of section 5.8, for both families and each of
// the six period metrics, with every header at its exact value.
func TestQuotaExceeded(t *testing.T) {
	type want struct {
		limit, remaining, currency string // "" currency: the header is absent
	}
	metrics := []struct {
		name string
		want want
	}{
		{"requests_per_day", want{"100", "0", ""}},
		{"requests_per_month", want{"5000", "17", ""}},
		{"tokens_per_day", want{"2000000", "1500", ""}},
		{"tokens_per_month", want{"1000000000000", "999999999999", ""}},
		{"cost_per_day", want{"250.5", "0", "EUR"}},
		{"cost_per_month", want{"1.234567", "0.1", "USD"}},
	}
	for _, f := range limFamilies() {
		for _, m := range metrics {
			t.Run(f.name+"/"+m.name, func(t *testing.T) {
				ref := quotaRefusal(m.name)
				lim := testutil.NewStaticLimits().Refuse(ref)
				h := f.newH(t, func(o *gateway.Options) { o.Limits = lim })
				c := f.send(t, h, f.body("fake-basic", false))
				f.contract(t, m.name, c, family.CodeQuotaExceeded)
				seconds := strconv.Itoa(int(ref.RetryAfter / time.Second))
				headers := map[string]string{
					"Retry-After":               seconds,
					family.HeaderLimitReset:     seconds,
					"X-Pandokos-Error":          "quota_exceeded",
					family.HeaderLimitSubject:   ref.Subject,
					family.HeaderLimitScope:     ref.Scope,
					family.HeaderLimitMetric:    ref.Metric,
					family.HeaderLimit:          m.want.limit,
					family.HeaderLimitRemaining: m.want.remaining,
				}
				if m.want.currency != "" {
					headers[family.HeaderLimitCurrency] = m.want.currency
				}
				for k, v := range headers {
					if got := c.header.Values(k); len(got) != 1 || got[0] != v {
						t.Errorf("%s: %q, want %q", k, got, v)
					}
				}
				if m.want.currency == "" && len(c.header.Values(family.HeaderLimitCurrency)) != 0 {
					t.Errorf("%s on a limit that is not a budget: %q", family.HeaderLimitCurrency, c.header.Values(family.HeaderLimitCurrency))
				}
				for k := range c.header {
					if strings.HasPrefix(k, "X-Pandokos-Limit") {
						switch k {
						case family.HeaderLimitSubject, family.HeaderLimitScope, family.HeaderLimitMetric, family.HeaderLimit,
							family.HeaderLimitRemaining, family.HeaderLimitReset, family.HeaderLimitCurrency:
						default:
							t.Errorf("unexpected header %s", k)
						}
					}
				}
				for _, id := range []string{testPrincipal.ID, testPrincipal.KeyID, testPrincipal.TenantID.String()} {
					if strings.Contains(c.dump(), id) {
						t.Errorf("the answer holds %q", id)
					}
				}

				r := h.oneRecord(t)
				if r.Outcome != usage.OutcomeRejectedLimit || r.ErrorCode != "quota_exceeded" || r.CredentialID != "" {
					t.Errorf("record: outcome %q, code %q, credential %q", r.Outcome, r.ErrorCode, r.CredentialID)
				}
				line := h.awaitOutcome(t, "pandokos_error")
				if line["error_code"] != "quota_exceeded" || line["limit_metric"] != ref.Metric || line["limit_scope"] != ref.Scope ||
					line["limit_subject"] != ref.Subject || line["limit_level"] != ref.Level || line["retry_after_s"] != float64(ref.RetryAfter/time.Second) {
					t.Errorf("request line %v", line)
				}
				if numText(line["limit"]) != m.want.limit {
					t.Errorf("logged limit %v, want %s (a budget is logged as a decimal)", line["limit"], m.want.limit)
				}
				if cur, ok := line["limit_currency"]; m.want.currency != "" && cur != m.want.currency || m.want.currency == "" && ok {
					t.Errorf("limit_currency %v (present %v), want %q", cur, ok, m.want.currency)
				}
				if n := len(h.creds.Calls()) + len(h.up.Requests()); n != 0 {
					t.Errorf("%d credential opens or provider requests after a refusal", n)
				}
				testutil.AssertNoCanary(t, "the logs", h.logs.String())
			})
		}

		t.Run(f.name+"/a provider's own 429 still passes unchanged", func(t *testing.T) {
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
		})
	}
}

// budgetRules is a budget in USD and the tenant of testPrincipal.
func budgetRules(rules ...limits.Rule) limitstest.Options {
	return limitstest.Options{Rules: map[tenant.ID][]limits.Rule{testPrincipal.TenantID: rules}}
}

func costRule(id string, m limits.Metric, cur string, micro int64) limits.Rule {
	return limits.Rule{ID: id, SubjectKind: limits.SubjectTenant, Metric: m, Value: micro, Currency: cur, Enforce: true}
}

func countRule(id string, m limits.Metric, n int64) limits.Rule {
	return limits.Rule{ID: id, SubjectKind: limits.SubjectTenant, Metric: m, Value: n, Enforce: true}
}

// limitsOf resolves the limits of the principal for a model as the pipeline does, to look at their counters.
func limitsOf(t *testing.T, r *limitstest.Real, o limitstest.Options, provider, model string) []limits.Limit {
	t.Helper()
	set := limits.NewScopedRuleSet(testPrincipal.TenantID, o.PlatformDefault, o.Platform[testPrincipal.TenantID], o.Rules[testPrincipal.TenantID])
	return set.For(limits.Query{ServiceAccountID: testPrincipal.ID, KeyID: testPrincipal.KeyID, Provider: provider, Model: model, Start: r.Now()}, r.Calendar)
}

// TestNoPrice is T-666: a budget applies and the model has no price it can be measured with.
func TestNoPrice(t *testing.T) {
	for _, f := range limFamilies() {
		unpriced := func(h *harness) {
			tgt := h.targets[f.provider+"/fake-basic"]
			tgt.Prices = nil
			h.targets[f.provider+"/fake-basic"] = tgt
		}
		t.Run(f.name+"/refuse: 409 no_price, nothing forwarded", func(t *testing.T) {
			real := limitstest.NewWith(t, budgetRules(costRule("r-budget", limits.CostPerDay, "USD", 100_000_000)))
			h := f.newH(t, func(o *gateway.Options) { o.Limits = real })
			unpriced(h)
			c := f.send(t, h, f.body("fake-basic", false))
			f.contract(t, "no_price", c, family.CodeNoPrice)
			if c.status != 409 {
				t.Errorf("status %d", c.status)
			}
			if c.header.Get("Retry-After") != "" {
				t.Errorf("Retry-After %q: waiting does not help", c.header.Get("Retry-After"))
			}
			for k := range c.header {
				if strings.HasPrefix(k, "X-Pandokos-Limit") {
					t.Errorf("%s on a no_price refusal", k)
				}
			}
			r := h.oneRecord(t)
			if r.Outcome != usage.OutcomeRejectedLimit || r.ErrorCode != "no_price" || r.CredentialID != "" {
				t.Errorf("record: outcome %q, code %q, credential %q", r.Outcome, r.ErrorCode, r.CredentialID)
			}
			if n := len(h.creds.Calls()) + len(h.up.Requests()); n != 0 {
				t.Errorf("%d credential opens or provider requests", n)
			}
			line := h.awaitOutcome(t, "pandokos_error")
			if line["error_code"] != "no_price" {
				t.Errorf("request line %v", line)
			}
		})

		t.Run(f.name+"/a price in another currency is no price for that budget", func(t *testing.T) {
			real := limitstest.NewWith(t, budgetRules(costRule("r-budget", limits.CostPerDay, "EUR", 100_000_000)))
			h := f.newH(t, func(o *gateway.Options) { o.Limits = real }) // the model is priced in USD
			c := f.send(t, h, f.body("fake-basic", false))
			f.contract(t, "no_price", c, family.CodeNoPrice)
		})

		t.Run(f.name+"/a price in the currency of the budget is measured", func(t *testing.T) {
			real := limitstest.NewWith(t, budgetRules(costRule("r-budget", limits.CostPerDay, "USD", 100_000_000)))
			h := f.newH(t, func(o *gateway.Options) { o.Limits = real })
			if c := f.send(t, h, f.body("fake-basic", false)); c.status != 200 {
				t.Fatalf("%d %s", c.status, c.body)
			}
		})

		t.Run(f.name+"/allow: forwarded, other limits enforced, the budget unchanged", func(t *testing.T) {
			opts := budgetRules(costRule("r-budget", limits.CostPerDay, "USD", 100_000_000), countRule("r-count", limits.RequestsPerDay, 2))
			opts.OnUnknownPrice = "allow"
			real := limitstest.NewWith(t, opts)
			h := f.newH(t, func(o *gateway.Options) { o.Limits = real })
			unpriced(h)
			for i := 1; i <= 2; i++ {
				if c := f.send(t, h, f.body("fake-basic", false)); c.status != 200 {
					t.Fatalf("call %d: %d %s", i, c.status, c.body)
				}
			}
			c := f.send(t, h, f.body("fake-basic", false))
			f.contract(t, "third", c, family.CodeQuotaExceeded)
			if c.header.Get(family.HeaderLimitMetric) != "requests_per_day" {
				t.Errorf("refused by %q, want the request quota", c.header.Get(family.HeaderLimitMetric))
			}
			if n := len(h.up.Requests()); n != 2 {
				t.Errorf("%d requests reached the provider, want 2", n)
			}
			marked := 0
			for _, l := range h.requestLines(t, 3) {
				if l["budget_unpriced"] == true {
					marked++
				}
			}
			if marked != 2 {
				t.Errorf("%d request lines carry budget_unpriced, want 2", marked)
			}
			for _, l := range limitsOf(t, real, opts, f.provider, "fake-basic") {
				if l.Metric != limits.CostPerDay {
					continue
				}
				if a, ok := real.Mem.Counted(l); ok && a.Cost.String() != "0" {
					t.Errorf("the budget counts %s although it could not measure the requests", a.Cost)
				}
			}
		})
	}
}

// TestDayInZone is T-674: a daily quota in Europe/Rome resets at local midnight, on a day of 23
// hours and on a day of 25, and Retry-After is the time to that instant.
func TestDayInZone(t *testing.T) {
	for _, c := range []struct {
		name       string
		start      time.Time // the clock, UTC
		nextDay    time.Time // local midnight, UTC
		nextLength time.Duration
	}{
		{"the day of the spring change lasts 23 hours", time.Date(2026, 3, 28, 22, 30, 0, 0, time.UTC), time.Date(2026, 3, 28, 23, 0, 0, 0, time.UTC), 23 * time.Hour},
		{"the day of the autumn change lasts 25 hours", time.Date(2026, 10, 24, 21, 30, 0, 0, time.UTC), time.Date(2026, 10, 24, 22, 0, 0, 0, time.UTC), 25 * time.Hour},
	} {
		t.Run(c.name, func(t *testing.T) {
			opts := budgetRules(countRule("r-day", limits.RequestsPerDay, 1))
			opts.Zone, opts.Start = "Europe/Rome", c.start
			real := limitstest.NewWith(t, opts)
			h := newHarness(t, func(o *gateway.Options) { o.Limits = real })
			if r := h.sendAuthed(t, route, chat("fake-basic", false)); r.status != 200 {
				t.Fatalf("first call: %d %s", r.status, r.body)
			}
			r := h.sendAuthed(t, route, chat("fake-basic", false))
			assertContract(t, "second call", r, family.CodeQuotaExceeded)
			if want := strconv.Itoa(int(c.nextDay.Sub(c.start) / time.Second)); r.header.Get("Retry-After") != want || r.header.Get(family.HeaderLimitReset) != want {
				t.Errorf("Retry-After %q, reset %q, want %s: the time to local midnight", r.header.Get("Retry-After"), r.header.Get(family.HeaderLimitReset), want)
			}
			real.AdvanceTo(c.nextDay.Add(-time.Second)) // still the same day
			assertContract(t, "a second before midnight", h.sendAuthed(t, route, chat("fake-basic", false)), family.CodeQuotaExceeded)
			real.AdvanceTo(c.nextDay) // local midnight: the new day
			if r := h.sendAuthed(t, route, chat("fake-basic", false)); r.status != 200 {
				t.Fatalf("at midnight: %d %s", r.status, r.body)
			}
			r = h.sendAuthed(t, route, chat("fake-basic", false))
			assertContract(t, "the new day", r, family.CodeQuotaExceeded)
			if want := strconv.Itoa(int(c.nextLength / time.Second)); r.header.Get("Retry-After") != want {
				t.Errorf("Retry-After %q, want %s: the whole length of the day", r.header.Get("Retry-After"), want)
			}
		})
	}
}
