package gateway

import (
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/anthropic"
	"github.com/pandokos/pandokos/internal/family/openai"
	"github.com/pandokos/pandokos/internal/pricing"
)

func operationOf(t *testing.T, a family.Adapter, name string) family.Operation {
	t.Helper()
	for _, op := range a.Operations() {
		if op.Name == name {
			return op
		}
	}
	t.Fatalf("no operation %q", name)
	return family.Operation{}
}

func priceAt(t *testing.T, id string, from time.Time, in, out string) pricing.Version {
	t.Helper()
	i, err := pricing.ParseDecimal(in)
	if err != nil {
		t.Fatal(err)
	}
	o, err := pricing.ParseDecimal(out)
	if err != nil {
		t.Fatal(err)
	}
	return pricing.Version{ID: id, ModelID: "synthetic-model", ValidFrom: from, Currency: "USD", Source: "override",
		Rates: []pricing.Rate{{Category: pricing.CategoryInput, PerMillion: i}, {Category: pricing.CategoryOutput, PerMillion: o}}}
}

// TestEstimate is T-628: each line of the formula of section 5.3 at its boundary.
func TestEstimate(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	oa, aa := openai.Adapter(), anthropic.Adapter()
	chat := operationOf(t, oa, openai.OperationChatCompletions)
	emb := operationOf(t, oa, openai.OperationEmbeddings)
	count := operationOf(t, aa, anthropic.OperationCountTokens)
	msgs := operationOf(t, aa, anthropic.OperationMessages)
	const def = 4096
	const limit = int64(1_000_000_000_000)

	type tc struct {
		name   string
		a      family.Adapter
		op     family.Operation
		bound  family.OutputBound
		bytes  int64
		target Target
		def    int64
		in     int64
		out    int64
	}
	one := family.OutputBound{Choices: 1}
	cases := []tc{
		{"the input is bytes over four and the output the default", oa, chat, one, 400, Target{}, def, 100, def},
		{"the input rounds up", oa, chat, one, 401, Target{}, def, 101, def},
		{"an empty body", oa, chat, one, 0, Target{}, def, 0, def},
		{"the context window caps the input", oa, chat, one, 4_000_000, Target{ContextWindow: 8000}, def, 8000, def},
		{"a context window above the input changes nothing", oa, chat, one, 400, Target{ContextWindow: 8000}, def, 100, def},
		{"a context window equal to the input", oa, chat, one, 400, Target{ContextWindow: 100}, def, 100, def},
		{"the caller's limit replaces the default", oa, chat, family.OutputBound{Tokens: 1000, Choices: 1}, 400, Target{}, def, 100, 1000},
		{"the caller's limit above the default is kept", oa, chat, family.OutputBound{Tokens: 50_000, Choices: 1}, 400, Target{}, def, 100, 50_000},
		{"the model's maximum caps the caller's limit", oa, chat, family.OutputBound{Tokens: 1000, Choices: 1}, 400, Target{MaxOutputTokens: 500}, def, 100, 500},
		{"the model's maximum caps the default", oa, chat, one, 400, Target{MaxOutputTokens: 2048}, def, 100, 2048},
		{"a model maximum above the output changes nothing", oa, chat, one, 400, Target{MaxOutputTokens: 100_000}, def, 100, def},
		{"choices multiply the caller's limit", oa, chat, family.OutputBound{Tokens: 1000, Choices: 3}, 400, Target{}, def, 100, 3000},
		{"choices multiply the default", oa, chat, family.OutputBound{Choices: 3}, 400, Target{}, def, 100, 3 * def},
		{"the cap applies per choice, before the multiplication", oa, chat, family.OutputBound{Tokens: 1000, Choices: 3}, 400, Target{MaxOutputTokens: 600}, def, 100, 1800},
		{"embeddings produce no output", oa, emb, family.OutputBound{None: true, Choices: 1}, 400, Target{}, def, 100, 0},
		{"embeddings: a model maximum changes nothing", oa, emb, family.OutputBound{None: true, Choices: 1}, 400, Target{MaxOutputTokens: 10}, def, 100, 0},
		{"count_tokens reserves no tokens", aa, count, family.OutputBound{None: true, Choices: 1}, 400, Target{}, def, 0, 0},
		{"messages: max_tokens is the caller's limit", aa, msgs, family.OutputBound{Tokens: 16, Choices: 1}, 400, Target{}, def, 100, 16},
		{"a default of one", oa, chat, one, 8, Target{}, 1, 2, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := estimateOf(c.a, c.op, c.bound, c.bytes, c.target, c.def, start)
			if got.InputTokens != c.in || got.OutputTokens != c.out {
				t.Errorf("input %d output %d, want %d and %d", got.InputTokens, got.OutputTokens, c.in, c.out)
			}
		})
	}

	t.Run("the sum saturates at 1e12", func(t *testing.T) {
		got := estimateOf(oa, chat, family.OutputBound{Tokens: 999_999_999_999, Choices: 128}, 400, Target{}, def, start)
		if got.InputTokens != 100 || got.InputTokens+got.OutputTokens != limit {
			t.Errorf("input %d output %d, want 100 and a sum of %d", got.InputTokens, got.OutputTokens, limit)
		}
		got = estimateOf(oa, chat, family.OutputBound{Tokens: 999_999_999_999, Choices: 1}, 4_000_000_000, Target{}, def, start)
		if got.InputTokens+got.OutputTokens != limit || got.InputTokens < 0 || got.OutputTokens < 0 {
			t.Errorf("input %d output %d: not saturated at %d", got.InputTokens, got.OutputTokens, limit)
		}
		got = estimateOf(oa, chat, family.OutputBound{Tokens: 999_999_999_999, Choices: 128}, 400, Target{MaxOutputTokens: 1}, def, start)
		if got.OutputTokens != 128 {
			t.Errorf("output %d, want 128 with a model maximum of 1", got.OutputTokens)
		}
	})

	t.Run("the cost is that of the estimated usage at the price in force at the start", func(t *testing.T) {
		before := priceAt(t, "synthetic-price-before", start.Add(-time.Hour), "1", "2")
		after := priceAt(t, "synthetic-price-after", start.Add(time.Hour), "10", "20")
		target := Target{Prices: []pricing.Version{before, after}}
		got := estimateOf(oa, chat, one, 400, target, def, start)
		want := pricing.CostOf(family.Usage{InputTokens: 100, OutputTokens: def}, before, true)
		if !got.Cost.Known || got.Cost.VersionID != "synthetic-price-before" || got.Cost.Amount.String() != want.Amount.String() {
			t.Errorf("cost %+v, want %+v", got.Cost, want)
		}
		later := estimateOf(oa, chat, one, 400, target, def, start.Add(2*time.Hour))
		if later.Cost.VersionID != "synthetic-price-after" {
			t.Errorf("a request that starts later is priced at %q", later.Cost.VersionID)
		}
	})

	t.Run("an unknown price leaves the cost unknown and the tokens reserved", func(t *testing.T) {
		got := estimateOf(oa, chat, one, 400, Target{}, def, start)
		if got.Cost.Known || got.Cost.Reason != pricing.ReasonNoPrice {
			t.Errorf("cost %+v, want unknown (no price)", got.Cost)
		}
		if got.InputTokens != 100 || got.OutputTokens != def {
			t.Errorf("tokens %d + %d: an unknown price must not change the reservation", got.InputTokens, got.OutputTokens)
		}
	})
}

// TestEstimateTenantPrice is T-734: the estimate is priced through the list of the target: amount,
// currency, version ID and source; unknown when neither list prices the model in the active currency;
// an amount that does not fit keeps currency, version and source.
func TestEstimateTenantPrice(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	oa := openai.Adapter()
	chat := operationOf(t, oa, openai.OperationChatCompletions)
	one := family.OutputBound{Tokens: 1000, Choices: 1}
	const def = 4096
	priced := func(id, currency, source string, from time.Time, in, out string) pricing.Version {
		v := priceAt(t, id, from, in, out)
		v.Currency, v.Source = currency, source
		return v
	}
	// 400 bytes are 100 input tokens and the output is 1000: the amounts are written by hand.
	// At 1 EUR per million input tokens and 2 per million output tokens: 100/1e6 + 2000/1e6 = 0.0021 EUR.
	tenantEUR := priced("tenant-eur", "EUR", pricing.SourceTenant, start.Add(-time.Hour), "1", "2")
	catUSD := priced("cat-usd", "USD", "import", start.Add(-time.Hour), "10", "20")
	catEUR := priced("cat-eur", "EUR", "import", start.Add(-time.Hour), "5", "6")

	t.Run("a tenant price in the active currency", func(t *testing.T) {
		target := Target{Currency: pricing.Active{Current: "EUR"}, Prices: []pricing.Version{catUSD}, TenantPrices: []pricing.Version{tenantEUR}}
		got := estimateOf(oa, chat, one, 400, target, def, start)
		c := got.Cost
		if !c.Known || c.Amount.String() != "0.0021" || c.Currency != "EUR" || c.VersionID != "tenant-eur" || c.Source != pricing.SourceTenant {
			t.Errorf("cost %+v (amount %s), want 0.0021 EUR from the tenant's version", c, c.Amount)
		}
		if got.InputTokens != 100 || got.OutputTokens != 1000 {
			t.Errorf("tokens %d + %d", got.InputTokens, got.OutputTokens)
		}
	})

	t.Run("the catalogue's price in the active currency", func(t *testing.T) {
		target := Target{Currency: pricing.Active{Current: "USD"}, Prices: []pricing.Version{catUSD}, TenantPrices: []pricing.Version{tenantEUR}}
		c := estimateOf(oa, chat, one, 400, target, def, start).Cost
		if !c.Known || c.Amount.String() != "0.021" || c.Currency != "USD" || c.VersionID != "cat-usd" || c.Source != pricing.SourceCatalogue {
			t.Errorf("cost %+v (amount %s), want 0.021 USD from the catalogue", c, c.Amount)
		}
	})

	t.Run("a pending change prices in the currency in force at the start", func(t *testing.T) {
		change := start.Add(time.Hour)
		target := Target{Currency: pricing.Active{Current: "EUR", Previous: "USD", From: change}, Prices: []pricing.Version{catUSD}, TenantPrices: []pricing.Version{tenantEUR}}
		if c := estimateOf(oa, chat, one, 400, target, def, start).Cost; c.Currency != "USD" || c.Source != pricing.SourceCatalogue {
			t.Errorf("before the change: %+v", c)
		}
		if c := estimateOf(oa, chat, one, 400, target, def, change).Cost; c.Currency != "EUR" || c.Source != pricing.SourceTenant {
			t.Errorf("from the change: %+v", c)
		}
	})

	t.Run("unknown when neither list prices the model in the active currency", func(t *testing.T) {
		target := Target{Currency: pricing.Active{Current: "EUR"}, Prices: []pricing.Version{catUSD}}
		got := estimateOf(oa, chat, one, 400, target, def, start)
		if c := got.Cost; c.Known || c.Reason != pricing.ReasonNoPrice || c.VersionID != "" || c.Source != "" {
			t.Errorf("cost %+v, want unknown without a version or a source", c)
		}
		if got.InputTokens != 100 || got.OutputTokens != 1000 {
			t.Errorf("tokens %d + %d: an unknown price must not change the reservation", got.InputTokens, got.OutputTokens)
		}
	})

	t.Run("the catalogue in the active currency when the list has none", func(t *testing.T) {
		target := Target{Currency: pricing.Active{Current: "EUR"}, Prices: []pricing.Version{catEUR}}
		c := estimateOf(oa, chat, one, 400, target, def, start).Cost
		if !c.Known || c.Amount.String() != "0.0065" || c.Source != pricing.SourceCatalogue {
			t.Errorf("cost %+v (amount %s)", c, c.Amount)
		}
	})

	t.Run("an amount that does not fit keeps the currency, the version and the source", func(t *testing.T) {
		huge := priced("tenant-huge", "EUR", pricing.SourceTenant, start.Add(-time.Hour), "100000000000000", "100000000000000")
		target := Target{Currency: pricing.Active{Current: "EUR"}, TenantPrices: []pricing.Version{huge}}
		got := estimateOf(oa, chat, family.OutputBound{Tokens: 999_999_999_999, Choices: 1}, 400, target, def, start)
		c := got.Cost
		if c.Known || c.Reason != pricing.ReasonInconsistentUsage || c.Currency != "EUR" || c.VersionID != "tenant-huge" || c.Source != pricing.SourceTenant {
			t.Errorf("cost %+v, want unknown (inconsistent usage) with currency EUR, version and source of the tenant", c)
		}
	})
}

// TestEstimateModelDefault is T-663: the formula of section 5.10 at each branch, and what the
// estimate carries for the period limits.
func TestEstimateModelDefault(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	chat := operationOf(t, openai.Adapter(), openai.OperationChatCompletions)
	oa := openai.Adapter()
	const configured = 4096
	for _, c := range []struct {
		name   string
		bound  family.OutputBound
		target Target
		want   int64
	}{
		{"the caller's limit wins over the model's default", family.OutputBound{Tokens: 1000, Choices: 1}, Target{DefaultOutputTokens: 800}, 1000},
		{"the model's default when the caller sets no limit", family.OutputBound{Choices: 1}, Target{DefaultOutputTokens: 800}, 800},
		{"the configured default when the model has none", family.OutputBound{Choices: 1}, Target{}, configured},
		{"the model's default above the configured one", family.OutputBound{Choices: 1}, Target{DefaultOutputTokens: 100_000}, 100_000},
		{"the model's maximum caps the model's default", family.OutputBound{Choices: 1}, Target{DefaultOutputTokens: 800, MaxOutputTokens: 500}, 500},
		{"a maximum above the model's default changes nothing", family.OutputBound{Choices: 1}, Target{DefaultOutputTokens: 800, MaxOutputTokens: 5000}, 800},
		{"choices multiply the model's default", family.OutputBound{Choices: 3}, Target{DefaultOutputTokens: 800}, 2400},
		{"the cap applies per choice", family.OutputBound{Choices: 3}, Target{DefaultOutputTokens: 800, MaxOutputTokens: 300}, 900},
		{"a default of one", family.OutputBound{Choices: 1}, Target{DefaultOutputTokens: 1}, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := estimateOf(oa, chat, c.bound, 400, c.target, configured, start)
			if got.OutputTokens != c.want {
				t.Errorf("output %d, want %d", got.OutputTokens, c.want)
			}
		})
	}

	t.Run("an operation without output ignores the model's default", func(t *testing.T) {
		emb := operationOf(t, oa, openai.OperationEmbeddings)
		got := estimateOf(oa, emb, family.OutputBound{None: true, Choices: 1}, 400, Target{DefaultOutputTokens: 800}, configured, start)
		if got.OutputTokens != 0 {
			t.Errorf("output %d, want 0", got.OutputTokens)
		}
	})

	t.Run("the estimate carries the provider, the model and the start", func(t *testing.T) {
		got := estimateOf(oa, chat, family.OutputBound{Choices: 1}, 400, Target{Provider: "openai-main", Model: "gpt-4o"}, configured, start)
		if got.Provider != "openai-main" || got.Model != "gpt-4o" || !got.Start.Equal(start) {
			t.Errorf("provider %q, model %q, start %v", got.Provider, got.Model, got.Start)
		}
	})
}
