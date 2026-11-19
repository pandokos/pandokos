package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// micros formats an amount in millionths as a decimal with six fraction digits.
func micros(n int64) string { return fmt.Sprintf("%d.%06d", n/1_000_000, n%1_000_000) }

// TestTenantPriceLists is T-764: the binary and the CLI, the exit condition end to end. Tenant A has a EUR
// list (4 input and 8 output per million), a EUR monthly budget and a USD one; before the switch it is
// priced in USD (the catalogue's 3 and 6) and only the USD budget counts; the currency is set through the
// CLI, the pending answer is checked, the row is moved as the passing of a month would and the gateway is
// notified; then A is refused at the EUR budget. B stays in USD. A platform key gets 403 on A's prices,
// usage records and limit events; `usage list` shows both sources; the default log has no cost, and has it
// with the key on, with the warning at start.
func TestTenantPriceLists(t *testing.T) {
	p, _, boot := limitsProc(t, "info")
	a, b := p.newLimitsTenant(t, boot, "prices-a"), p.newLimitsTenant(t, boot, "prices-b")

	// The estimate of the fixture request: ceil(bytes / 4) input tokens and 4096 output tokens; the
	// fake's answer is 9 input and 12 output tokens. In millionths: USD at 3 and 6, EUR at 4 and 8.
	in := int64((len(chatBody("fake-basic", false, fixtures.CanaryPrompt)) + 3) / 4)
	estUSD, costUSD := 3*in+4096*6, int64(9*3+12*6)
	estEUR, costEUR := 4*in+4096*8, int64(9*4+12*8)

	p.mustCLI(t, a.admin, "price-list", "set", "--tenant", a.idStr, "--currency", "EUR", "--model", "openai/fake-basic", "--rate", "input=4", "--rate", "output=8")
	p.mustCLI(t, a.admin, "limit", "create", "--tenant", a.idStr, "--metric", "cost_per_month", "--amount", micros(estUSD+3*costUSD-1), "--currency", "USD")
	p.mustCLI(t, a.admin, "limit", "create", "--tenant", a.idStr, "--metric", "cost_per_month", "--amount", micros(estEUR+3*costEUR-1), "--currency", "EUR") // inactive until the switch: the three requests of the USD phase must not count against it
	p.mustCLI(t, b.admin, "limit", "create", "--tenant", b.idStr, "--metric", "cost_per_month", "--amount", micros(estUSD+3*costUSD-1), "--currency", "USD")

	t.Run("before the switch A is priced in USD and only the USD budget counts", func(t *testing.T) {
		for i := 1; i <= 3; i++ {
			if status, text := p.openaiCall(t, a.key); status != 200 {
				t.Fatalf("call %d: %d\n%s", i, status, text)
			}
		}
		status, text := p.openaiCall(t, a.key)
		assertPandokosError(t, "the fourth call in USD", status, text, 429, "quota_exceeded")
		if !strings.Contains(text, "X-Pandokos-Limit-Currency: USD") {
			t.Errorf("the refusal is not in USD:\n%s", text)
		}
	})

	t.Run("the currency is set through the CLI: pending, with the limits that become inactive", func(t *testing.T) {
		out := p.mustCLI(t, a.admin, "price-list", "currency", "set", "--tenant", a.idStr, "EUR")
		for _, want := range []string{"currency: EUR", "in_force: USD", "effective_from: ", "limits that will be inactive:", "USD"} {
			if !strings.Contains(out, want) {
				t.Errorf("the answer lacks %q:\n%s", want, out)
			}
		}
		got := p.mustCLI(t, a.admin, "price-list", "currency", "get", "--tenant", a.idStr)
		if !strings.Contains(got, "in_force: USD") {
			t.Errorf("get:\n%s", got)
		}
		// The passing of the month: the row as it is once the instant is behind it, and the notification.
		p.db.SetTenantCurrency(t, a.id, "EUR", "USD", time.Time{})
	})

	t.Run("after it A is refused at the EUR budget and B is not affected", func(t *testing.T) {
		if out := p.mustCLI(t, a.admin, "limit", "list", "--tenant", a.idStr); !strings.Contains(out, "active") {
			t.Errorf("limit list has no active column:\n%s", out)
		}
		testutil.Eventually(t, 3*time.Second, "the gateway to price A in EUR (the USD budget is exhausted and plays no part)", func() bool {
			status, _ := p.openaiCall(t, a.key)
			return status == 200
		})
		for i := 0; i < 2; i++ {
			if status, text := p.openaiCall(t, a.key); status != 200 {
				t.Fatalf("call %d in EUR: %d\n%s", i+2, status, text)
			}
		}
		status, text := p.openaiCall(t, a.key)
		assertPandokosError(t, "the fourth call in EUR", status, text, 429, "quota_exceeded")
		if !strings.Contains(text, "X-Pandokos-Limit-Currency: EUR") {
			t.Errorf("the refusal is not in EUR:\n%s", text)
		}
	})

	t.Run("B keeps USD", func(t *testing.T) {
		for i := 1; i <= 3; i++ {
			if status, text := p.openaiCall(t, b.key); status != 200 {
				t.Fatalf("call %d of B: %d\n%s", i, status, text)
			}
		}
		status, text := p.openaiCall(t, b.key)
		assertPandokosError(t, "the fourth call of B", status, text, 429, "quota_exceeded")
	})

	t.Run("a platform key sees none of A's prices, usage or events", func(t *testing.T) {
		for _, args := range [][]string{
			{"price-list", "lists", "--tenant", a.idStr}, {"price-list", "list", "--tenant", a.idStr}, {"price-list", "currency", "get", "--tenant", a.idStr},
			{"usage", "list", "--tenant", a.idStr}, {"limit", "events", "--tenant", a.idStr},
		} {
			code, out, errs := p.cli(t, boot, args...)
			if code == 0 || !strings.Contains(errs+out, "forbidden") && !strings.Contains(errs+out, "403") {
				t.Errorf("%s with a platform key: exit %d\n%s%s", strings.Join(args, " "), code, out, errs)
			}
		}
	})

	t.Run("usage list shows both sources", func(t *testing.T) {
		out := p.mustCLI(t, a.admin, "usage", "list", "--tenant", a.idStr, "-o", "json", "--all")
		if !strings.Contains(out, `"price_source": "catalogue"`) && !strings.Contains(out, `"price_source":"catalogue"`) {
			t.Errorf("no catalogue record:\n%s", out)
		}
		if !strings.Contains(out, `"price_source": "tenant"`) && !strings.Contains(out, `"price_source":"tenant"`) {
			t.Errorf("no tenant record:\n%s", out)
		}
	})

	t.Run("the default log has no cost and has it with the key on, with the warning at start", func(t *testing.T) {
		for _, l := range logLines(t, p.stdout.String(), "request") {
			for _, f := range []string{"cost", "currency", "usage", "reserved_tokens", "limit", "limit_currency", "price_currency", "price_source"} {
				if _, ok := l[f]; ok {
					t.Errorf("the default request line has %s: %v", f, l)
				}
			}
		}
		if n := len(logLines(t, p.stdout.String(), "configuration warning")); n != 0 {
			for _, l := range logLines(t, p.stdout.String(), "configuration warning") {
				if l["key"] == "telemetry.tenant_usage_details" {
					t.Errorf("the warning with the key off: %v", l)
				}
			}
		}
		on := startBinary(t, opts{level: "info", up: p.up, env: []string{"PANDOKOS_TELEMETRY_TENANT_USAGE_DETAILS=true"}})
		boot2 := on.bootstrap(t)
		on.prepareUpstream(t)
		tn := on.newLimitsTenant(t, boot2, "prices-on")
		if status, text := on.openaiCall(t, tn.key); status != 200 {
			t.Fatalf("%d\n%s", status, text)
		}
		testutil.Eventually(t, 5*time.Second, "the warning at start", func() bool {
			for _, l := range completeLines(on.stdout, "configuration warning") {
				if l["key"] == "telemetry.tenant_usage_details" {
					return true
				}
			}
			return false
		})
		testutil.Eventually(t, 5*time.Second, "a request line with the cost", func() bool {
			for _, l := range completeLines(on.stdout, "request") {
				if _, ok := l["cost"]; ok && l["currency"] == "USD" {
					return true
				}
			}
			return false
		})
	})
}
