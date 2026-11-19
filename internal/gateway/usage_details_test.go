package gateway_test

import (
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/limitstest"
	"github.com/pandokos/pandokos/test/testutil"
)

// The fields of the request line that describe a tenant's usage (sprint 5.4, section 5.11): written
// only behind telemetry.tenant_usage_details.
var usageDetailFields = []string{"cost", "currency", "usage", "reserved_tokens", "limit", "limit_currency", "price_currency", "price_source"}

// detailsOf returns the fields of the list that a line carries.
func detailsOf(line map[string]any, fields ...string) []string {
	var out []string
	for _, f := range fields {
		if _, ok := line[f]; ok {
			out = append(out, f)
		}
	}
	return out
}

// TestUsageDetailsOff is T-739: by default the request line of an answered request, of a 429 and of a 409
// no_price has none of the fields of section 5.11 and the client span has neither cost attribute; with
// the key on they are all there, with price_source; at level debug, with the key off, no line of the
// gateway, the limiter or the ledger writer contains the canary token counts or amounts of the fixture.
func TestUsageDetailsOff(t *testing.T) {
	budget := func(rule limits.Rule) limitstest.Options { return budgetRules(rule) }
	cases := []struct {
		name     string
		rules    limitstest.Options
		status   int
		outcome  string
		wantOn   []string // fields of the list that the line has with the key on
		keptAlso []string // fields that stay whatever the key says
	}{
		{"an answered request", budget(costRule("r", limits.CostPerDay, "USD", 100_000_000_000)), 200, "ok",
			[]string{"cost", "currency", "usage", "reserved_tokens"}, []string{"cost_status", "usage_source", "limit_state"}},
		{"a 429", budget(costRule("r", limits.CostPerDay, "USD", 1)), 429, "rejected_policy",
			[]string{"limit", "limit_currency"}, []string{"limit_subject", "limit_metric", "limit_level", "limit_scope", "retry_after_s"}},
		{"a 409 no_price", budget(costRule("r", limits.CostPerDay, "EUR", 100_000_000)), 409, "rejected_policy",
			[]string{"limit_currency", "price_currency", "price_source"}, []string{"limit_state"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, on := range []bool{false, true} {
				h := newHarness(t, func(o *gateway.Options) {
					o.Limits = limitstest.NewWith(t, c.rules)
					o.UsageDetails = on
				})
				got := h.sendAuthed(t, route, chat("fake-basic", false))
				if got.status != c.status {
					t.Fatalf("key %v: status %d %s, want %d", on, got.status, got.body, c.status)
				}
				line := h.lastRequestLine(t)
				if !on {
					if has := detailsOf(line, usageDetailFields...); len(has) != 0 {
						t.Errorf("by default the line has %v: %v", has, line)
					}
					for _, k := range c.keptAlso {
						if _, ok := line[k]; !ok {
							t.Errorf("by default the line lost %s: %v", k, line)
						}
					}
					continue
				}
				for _, k := range c.wantOn {
					if _, ok := line[k]; !ok {
						t.Errorf("with the key on the line has no %s: %v", k, line)
					}
				}
				if c.status == 409 && line["price_source"] != "catalogue" {
					t.Errorf("price_source %v, want catalogue next to price_currency", line["price_source"])
				}
			}
		})
	}

	t.Run("the client span has neither cost attribute by default", func(t *testing.T) {
		for _, on := range []bool{false, true} {
			h := newHarness(t, func(o *gateway.Options) { o.UsageDetails = on })
			if c := h.sendAuthed(t, route, chat("fake-basic", false)); c.status != 200 {
				t.Fatalf("%d", c.status)
			}
			_, client := endedSpans(t, h, 2)
			a := attrs(client)
			_, amount := a["pandokos.cost.amount"]
			_, currency := a["pandokos.cost.currency"]
			if status, ok := a["pandokos.cost.status"]; !ok || status.AsString() != "known" {
				t.Errorf("key %v: the cost status stays on the span: %v", on, a)
			}
			if amount != on || currency != on {
				t.Errorf("key %v: cost.amount %v, cost.currency %v", on, amount, currency)
			}
		}
	})

	t.Run("at level debug, with the key off, no line of the gateway, the limiter or the ledger writer holds the canaries", func(t *testing.T) {
		// A canary of 7,654,321 tokens, output 7,654,320, and an amount: at 3 and 6 per million, 1 input token
		// cost 0.000003 and the output 45.92592 (7654320 x 6 / 1e6): 45.925923.
		canaries := []string{"7654321", "7654320", "45.925923"}
		d := dbtest.NewMigrated(t)
		tn := d.SeedTenant(t, "usage-details")
		d.SeedLimitRule(t, tn, "tenant", "", "cost_per_month", 100_000_000_000, dbtest.LimitOptions{Currency: "USD"})
		d.SeedLimitRule(t, tn, "tenant", "", "tokens_per_month", 100_000_000_000)
		limiterLogs := &testutil.LogBuffer{}
		s := newQuotaStack(t, d, tn, stackOptions{ledger: true, logs: limiterLogs, newH: func(t *testing.T, mutate func(*gateway.Options)) *harness {
			return newHarness(t, func(o *gateway.Options) {
				mutate(o)
				o.UsageDetails = false
			})
		}})
		s.h.up.ReportTokens(7654321)
		for i := 0; i < 3; i++ {
			if c := s.call(t, "a", "openai", "fake-basic"); c.status != 200 {
				t.Fatalf("%d %s", c.status, c.body)
			}
		}
		testutil.Eventually(t, 5*time.Second, "the records to be written", func() bool { return s.writer.Stats().Written >= 3 })
		time.Sleep(100 * time.Millisecond)
		for what, text := range map[string]string{"the gateway": s.h.logs.String(), "the limiter, the reconciler and the writer": limiterLogs.String()} {
			if text == "" && what == "the gateway" {
				t.Errorf("%s logged nothing at level debug: the sweep proves nothing", what)
			}
			for _, canary := range canaries {
				if strings.Contains(text, canary) {
					t.Errorf("a log line of %s holds %s:\n%s", what, canary, grepLines(text, canary))
				}
			}
		}
	})
}

// grepLines returns the lines of text that contain s.
func grepLines(text, s string) string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, s) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
