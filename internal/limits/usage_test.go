package limits

import (
	"testing"

	"github.com/pandokos/pandokos/internal/pricing"
)

// TestUsageSum is T-657.
func TestUsageSum(t *testing.T) {
	groups := []UsageGroup{
		{PrincipalID: "sa1", KeyID: "k1", ProviderID: "openai", ModelID: "gpt", Currency: "EUR", Requests: 2, Tokens: 100, Cost: dec(t, "1.5")},
		{PrincipalID: "sa1", KeyID: "k2", ProviderID: "openai", ModelID: "gpt-mini", Currency: "EUR", Requests: 1, Tokens: 50, Cost: dec(t, "0.25")},
		{PrincipalID: "sa2", KeyID: "k1", ProviderID: "anthropic", ModelID: "claude", Currency: "USD", Requests: 4, Tokens: 400, Cost: dec(t, "3")},
		{PrincipalID: "sa2", KeyID: "k2", ProviderID: "openai", ModelID: "gpt", Currency: "", Requests: 1, Tokens: 10, Cost: pricing.Decimal{}},
		{Minute: 29_000_000, PrincipalID: "sa1", KeyID: "k1", ProviderID: "openai", ModelID: "gpt", Currency: "EUR", Requests: 1, Tokens: 5, Cost: dec(t, "0.5")},
	}
	tn := tid(1)
	mk := func(kind SubjectKind, id, provider, model string, m Metric, cur string) Limit {
		l := lim(tn, kind, id, m, 1)
		l.ProviderID, l.ModelID, l.Currency = provider, model, cur
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

// TestUsageSumByIdentifier is T-702: Sum compares the provider ID and the model ID of the ledger
// groups with those of the limit, for each kind of subject; a group without a model ID counts for
// the wider scopes only.
func TestUsageSumByIdentifier(t *testing.T) {
	groups := []UsageGroup{
		{PrincipalID: "sa1", KeyID: "k1", ProviderID: "p1", ModelID: "m1", Currency: "EUR", Requests: 2, Tokens: 100, Cost: dec(t, "1.5")},
		{PrincipalID: "sa1", KeyID: "k2", ProviderID: "p1", ModelID: "m2", Currency: "EUR", Requests: 1, Tokens: 50, Cost: dec(t, "0.25")},
		{PrincipalID: "sa2", KeyID: "k1", ProviderID: "p2", ModelID: "m3", Currency: "USD", Requests: 4, Tokens: 400, Cost: dec(t, "3")},
		{PrincipalID: "sa2", KeyID: "k2", ProviderID: "p1", ModelID: "", Currency: "", Requests: 1, Tokens: 10},  // no model ID
		{PrincipalID: "sa2", KeyID: "k2", ProviderID: "", ModelID: "", Currency: "", Requests: 8, Tokens: 800},   // no provider either
		{PrincipalID: "sa1", KeyID: "k1", ProviderID: "p1", ModelID: "m1", Currency: "", Requests: 1, Tokens: 5}, // no cost
	}
	tn := tid(1)
	mk := func(kind SubjectKind, id, prov, mod string, m Metric, cur string) Limit {
		l := lim(tn, kind, id, m, 1)
		l.ProviderID, l.ModelID, l.Currency = prov, mod, cur
		// The names of the rule are for display: they differ from anything in the groups on purpose.
		if prov != "" {
			l.Provider = "name-of-" + prov
		}
		if mod != "" {
			l.Model = "name-of-" + mod
		}
		return l
	}
	for _, c := range []struct {
		name string
		l    Limit
		n    int64
		cost string
	}{
		{"tenant requests", mk(SubjectTenant, "", "", "", RequestsPerDay, ""), 17, ""},
		{"tenant tokens", mk(SubjectTenant, "", "", "", TokensPerMonth, ""), 1365, ""},
		{"tenant cost in EUR", mk(SubjectTenant, "", "", "", CostPerDay, "EUR"), 0, "1.75"},
		{"provider scope", mk(SubjectTenant, "", "p1", "", RequestsPerDay, ""), 5, ""},
		{"provider scope, the other provider", mk(SubjectTenant, "", "p2", "", TokensPerDay, ""), 400, ""},
		{"model scope", mk(SubjectTenant, "", "p1", "m1", RequestsPerDay, ""), 3, ""},
		{"model scope: a group without a model ID is not counted", mk(SubjectTenant, "", "p1", "m2", RequestsPerDay, ""), 1, ""},
		{"model of another provider", mk(SubjectTenant, "", "p2", "m1", RequestsPerDay, ""), 0, ""},
		{"a model ID alone does not match under another provider ID", mk(SubjectTenant, "", "p1", "m3", RequestsPerDay, ""), 0, ""},
		{"service account", mk(SubjectServiceAccount, "sa1", "", "", RequestsPerDay, ""), 4, ""},
		{"service account and provider", mk(SubjectServiceAccount, "sa2", "p1", "", RequestsPerDay, ""), 1, ""},
		{"service account of nobody", mk(SubjectServiceAccount, "sa9", "", "", RequestsPerDay, ""), 0, ""},
		{"key", mk(SubjectKey, "k1", "", "", RequestsPerMonth, ""), 7, ""},
		{"key and model", mk(SubjectKey, "k1", "p1", "m1", TokensPerDay, ""), 105, ""},
		{"key cost with scope", mk(SubjectKey, "k1", "p1", "m1", CostPerDay, "EUR"), 0, "1.5"},
		{"names do not decide", func() Limit {
			l := mk(SubjectTenant, "", "p9", "m9", RequestsPerDay, "")
			l.Provider, l.Model = "openai", "gpt" // the names of a group in a test of 5.2
			return l
		}(), 0, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := Sum(c.l, groups)
			if c.l.Metric.Cost() {
				if got.Cost.String() != c.cost {
					t.Errorf("cost %s, want %s", got.Cost, c.cost)
				}
				return
			}
			if got.N != c.n {
				t.Errorf("counted %d, want %d", got.N, c.n)
			}
		})
	}

	t.Run("a group with names and no identifiers counts for the wider scopes only", func(t *testing.T) {
		g := []UsageGroup{{PrincipalID: "sa1", KeyID: "k1", Provider: "openai", Model: "gpt", Requests: 3}}
		if got := Sum(mk(SubjectTenant, "", "", "", RequestsPerDay, ""), g); got.N != 3 {
			t.Errorf("the tenant counts %d", got.N)
		}
		if got := Sum(mk(SubjectTenant, "", "p1", "", RequestsPerDay, ""), g); got.N != 0 {
			t.Errorf("a provider rule counted a group by its name: %d", got.N)
		}
	})
}
