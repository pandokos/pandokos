package limits

import (
	"testing"

	"github.com/pandokos/pandokos/internal/pricing"
)

// TestUsageSum is T-657.
func TestUsageSum(t *testing.T) {
	groups := []UsageGroup{
		{PrincipalID: "sa1", KeyID: "k1", Provider: "openai", Model: "gpt", Currency: "EUR", Requests: 2, Tokens: 100, Cost: dec(t, "1.5")},
		{PrincipalID: "sa1", KeyID: "k2", Provider: "openai", Model: "gpt-mini", Currency: "EUR", Requests: 1, Tokens: 50, Cost: dec(t, "0.25")},
		{PrincipalID: "sa2", KeyID: "k1", Provider: "anthropic", Model: "claude", Currency: "USD", Requests: 4, Tokens: 400, Cost: dec(t, "3")},
		{PrincipalID: "sa2", KeyID: "k2", Provider: "openai", Model: "gpt", Currency: "", Requests: 1, Tokens: 10, Cost: pricing.Decimal{}},
		{Minute: 29_000_000, PrincipalID: "sa1", KeyID: "k1", Provider: "openai", Model: "gpt", Currency: "EUR", Requests: 1, Tokens: 5, Cost: dec(t, "0.5")},
	}
	tn := tid(1)
	mk := func(kind SubjectKind, id, provider, model string, m Metric, cur string) Limit {
		l := lim(tn, kind, id, m, 1)
		l.Provider, l.Model, l.Currency = provider, model, cur
		return l
	}
	for _, c := range []struct {
		name     string
		l        Limit
		n        int64
		cost     string
		wantCost bool
	}{
		{"tenant requests", mk(SubjectTenant, "", "", "", RequestsPerDay, ""), 9, "", false},
		{"tenant tokens", mk(SubjectTenant, "", "", "", TokensPerMonth, ""), 565, "", false},
		{"tenant cost in EUR", mk(SubjectTenant, "", "", "", CostPerDay, "EUR"), 0, "2.25", true},
		{"tenant cost in USD", mk(SubjectTenant, "", "", "", CostPerMonth, "USD"), 0, "3", true},
		{"tenant cost in a currency nobody used", mk(SubjectTenant, "", "", "", CostPerDay, "JPY"), 0, "0", true},
		{"service account requests", mk(SubjectServiceAccount, "sa1", "", "", RequestsPerDay, ""), 4, "", false},
		{"service account tokens", mk(SubjectServiceAccount, "sa2", "", "", TokensPerDay, ""), 410, "", false},
		{"service account of nobody", mk(SubjectServiceAccount, "sa9", "", "", RequestsPerDay, ""), 0, "", false},
		{"key requests", mk(SubjectKey, "k1", "", "", RequestsPerMonth, ""), 7, "", false},
		{"key cost", mk(SubjectKey, "k1", "", "", CostPerDay, "EUR"), 0, "2", true},
		{"provider scope", mk(SubjectTenant, "", "openai", "", RequestsPerDay, ""), 5, "", false},
		{"provider scope, other provider", mk(SubjectTenant, "", "anthropic", "", TokensPerDay, ""), 400, "", false},
		{"model scope", mk(SubjectTenant, "", "openai", "gpt", RequestsPerDay, ""), 4, "", false},
		{"model scope and key", mk(SubjectKey, "k1", "openai", "gpt", TokensPerDay, ""), 105, "", false},
		{"model of another provider", mk(SubjectTenant, "", "anthropic", "gpt", RequestsPerDay, ""), 0, "", false},
		{"cost with scope", mk(SubjectTenant, "", "openai", "gpt", CostPerDay, "EUR"), 0, "2", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := Sum(c.l, groups)
			if c.wantCost {
				if got.Cost.String() != c.cost {
					t.Errorf("cost %s, want %s", got.Cost, c.cost)
				}
				if got.N != 0 {
					t.Errorf("a budget counted %d requests or tokens", got.N)
				}
				return
			}
			if got.N != c.n {
				t.Errorf("counted %d, want %d", got.N, c.n)
			}
		})
	}

	t.Run("a group of another currency adds requests and tokens and no cost", func(t *testing.T) {
		l := mk(SubjectTenant, "", "anthropic", "", RequestsPerDay, "")
		if got := Sum(l, groups); got.N != 4 {
			t.Errorf("requests %d, want 4", got.N)
		}
		l = mk(SubjectTenant, "", "anthropic", "", CostPerDay, "EUR")
		if got := Sum(l, groups); got.Cost.String() != "0" {
			t.Errorf("EUR cost %s of a USD-only provider, want 0", got.Cost)
		}
	})

	t.Run("no groups", func(t *testing.T) {
		if got := Sum(mk(SubjectTenant, "", "", "", RequestsPerDay, ""), nil); got.N != 0 || got.Cost.String() != "0" {
			t.Errorf("%+v", got)
		}
	})
}
