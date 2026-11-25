package gateway_test

import (
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/test/limitstest"
)

// TestNoPriceLogFields is T-712 as ADR 0055 redefines it: the 409 no_price keeps the constant body of
// 5.2; its request line names the error and neither the currency of the budget nor that of the price,
// which are the tenant's.
func TestNoPriceLogFields(t *testing.T) {
	t.Parallel()
	for _, f := range limFamilies() {
		t.Run(f.name+"/a price in another currency", func(t *testing.T) {
			real := limitstest.NewWith(t, budgetRules(costRule("r-budget", limits.CostPerDay, "EUR", 100_000_000)))
			h := f.newH(t, func(o *gateway.Options) { o.Limits = real }) // the model is priced in USD
			c := f.send(t, h, f.body("fake-basic", false))
			f.contract(t, "no_price", c, family.CodeNoPrice)
			line := h.awaitOutcome(t, "pandokos_error")
			if line["error_code"] != "no_price" || line["limit_currency"] != nil || line["price_currency"] != nil {
				t.Errorf("request line %v, want the error and no currency", line)
			}
		})

		t.Run(f.name+"/no price at all", func(t *testing.T) {
			real := limitstest.NewWith(t, budgetRules(costRule("r-budget", limits.CostPerDay, "USD", 100_000_000)))
			h := f.newH(t, func(o *gateway.Options) { o.Limits = real })
			tgt := h.targets[f.provider+"/fake-basic"]
			tgt.Prices = nil
			h.targets[f.provider+"/fake-basic"] = tgt
			c := f.send(t, h, f.body("fake-basic", false))
			f.contract(t, "no_price", c, family.CodeNoPrice)
			line := h.awaitOutcome(t, "pandokos_error")
			if line["error_code"] != "no_price" || line["limit_currency"] != nil {
				t.Errorf("request line %v, want the error and no currency", line)
			}
			if v, ok := line["price_currency"]; ok {
				t.Errorf("price_currency %v on a request without a price: the field is absent", v)
			}
		})

		t.Run(f.name+"/the body and the headers are those of 5.2", func(t *testing.T) {
			real := limitstest.NewWith(t, budgetRules(costRule("r-budget", limits.CostPerDay, "EUR", 100_000_000)))
			h := f.newH(t, func(o *gateway.Options) { o.Limits = real })
			c := f.send(t, h, f.body("fake-basic", false))
			f.contract(t, "no_price", c, family.CodeNoPrice) // status, headers and the constant message
			if c.status != 409 || c.header.Get("Retry-After") != "" {
				t.Errorf("status %d, Retry-After %q", c.status, c.header.Get("Retry-After"))
			}
			for _, k := range []string{"X-Pandokos-Limit", "X-Pandokos-Limit-Currency", "X-Pandokos-Price-Currency"} {
				if v := c.header.Get(k); v != "" {
					t.Errorf("%s %q on the 409: the currencies are for the log", k, v)
				}
			}
			for _, cur := range []string{"EUR", "USD"} {
				if containsBytes(c.body, cur) {
					t.Errorf("the body names %s: it keeps its constant message", cur)
				}
			}
		})
	}
}

func containsBytes(b []byte, s string) bool {
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return true
		}
	}
	return false
}
