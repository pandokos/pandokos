package gateway_test

import (
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/limitstest"
	"github.com/pandokos/pandokos/test/testutil"
)

// requestLineFields is every field the request line may hold (ADR 0055): what the operator needs to
// run the installation and to explain a refusal or a failure. No identity of the caller or of its
// tenant, no model, no size, no usage, no cost and no limit rule.
var requestLineFields = []string{
	"time", "level", "msg",
	"request_id", "method", "route", "status", "outcome", "error_code", "limit_state", "limits_unverified",
	"invalid_reason", "duration_ms", "provider", "endpoint", "stream", "usage_injected", "upstream_status",
	"ttfb_ms", "upstream_error", "egress_reason", "ledger", "trace_id", "usage_reader",
}

// unknownFields returns the fields of a line that are not in the list.
func unknownFields(line map[string]any, allowed []string) []string {
	var out []string
	for k := range line {
		if !slices.Contains(allowed, k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// TestRequestLineIsOperational is T-739 as ADR 0055 redefines it: the request line of an answered
// request, of a 429 and of a 409 no_price holds only the fields of requestLineFields, and neither
// the tenant ID nor the key ID nor the principal ID appears in it.
func TestRequestLineIsOperational(t *testing.T) {
	t.Parallel()
	budget := func(rule limits.Rule) limitstest.Options { return budgetRules(rule) }
	cases := []struct {
		name   string
		rules  limitstest.Options
		status int
	}{
		{"an answered request", budget(costRule("r", limits.CostPerDay, "USD", 100_000_000_000)), 200},
		{"a 429", budget(costRule("r", limits.CostPerDay, "USD", 1)), 429},
		{"a 409 no_price", budget(costRule("r", limits.CostPerDay, "EUR", 100_000_000)), 409},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, func(o *gateway.Options) { o.Limits = limitstest.NewWith(t, c.rules) })
			got := h.sendAuthed(t, route, chat("fake-basic", false))
			if got.status != c.status {
				t.Fatalf("status %d %s, want %d", got.status, got.body, c.status)
			}
			line := h.lastRequestLine(t)
			if extra := unknownFields(line, requestLineFields); len(extra) != 0 {
				t.Errorf("the line has fields outside the list: %v\n%v", extra, line)
			}
			for _, id := range []string{testPrincipal.TenantID.String(), testPrincipal.ID, testPrincipal.KeyID} {
				if strings.Contains(h.logs.String(), id) {
					t.Errorf("a log line holds the identifier %s:\n%s", id, grepLines(h.logs.String(), id))
				}
			}
		})
	}
}

// TestLogsHoldNoTenantData is the sweep of ADR 0055: at level debug no line of the gateway, the limiter,
// the reconciler or the ledger writer holds the tenant ID, a key ID or the canary token counts and
// amount of the fixture.
func TestLogsHoldNoTenantData(t *testing.T) {
	t.Parallel()
	// A canary of 7,654,321 tokens, output 7,654,320, and an amount: at 3 and 6 per million, 1 input token
	// cost 0.000003 and the output 45.92592 (7654320 x 6 / 1e6): 45.925923.
	canaries := []string{"7654321", "7654320", "45.925923"}
	d := dbtest.NewMigrated(t)
	tn := d.SeedTenant(t, "operational-log")
	d.SeedLimitRule(t, tn, "tenant", "", "cost_per_month", 100_000_000_000, dbtest.LimitOptions{Currency: "USD"})
	d.SeedLimitRule(t, tn, "tenant", "", "tokens_per_month", 100_000_000_000)
	limiterLogs := &testutil.LogBuffer{}
	s := newQuotaStack(t, d, tn, stackOptions{ledger: true, logs: limiterLogs})
	s.h.up.ReportTokens(7654321)
	for i := 0; i < 3; i++ {
		if c := s.call(t, "a", "openai", "fake-basic"); c.status != 200 {
			t.Fatalf("%d %s", c.status, c.body)
		}
	}
	testutil.Eventually(t, 5*time.Second, "the records to be written", func() bool { return s.writer.Stats().Written >= 3 })
	time.Sleep(100 * time.Millisecond)
	ids := append([]string{tn.String()}, canaries...)
	for _, p := range s.principals {
		ids = append(ids, p.KeyID, p.ID)
	}
	for what, text := range map[string]string{"the gateway": s.h.logs.String(), "the limiter, the reconciler and the writer": limiterLogs.String()} {
		if text == "" && what == "the gateway" {
			t.Errorf("%s logged nothing at level debug: the sweep proves nothing", what)
		}
		for _, id := range ids {
			if strings.Contains(text, id) {
				t.Errorf("a log line of %s holds %s:\n%s", what, id, grepLines(text, id))
			}
		}
	}
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
