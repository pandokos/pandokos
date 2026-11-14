package pricing_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/pricing"
)

// sumLines adds the amounts of the breakdown.
func sumLines(c pricing.Cost) pricing.Decimal {
	var s pricing.Decimal
	for _, l := range c.Lines {
		s = s.Add(l.Amount)
	}
	return s
}

func cats(cs []pricing.Category) []string {
	var out []string
	for _, c := range cs {
		out = append(out, string(c))
	}
	sort.Strings(out)
	return out
}

// TestCost is T-371. Every expected amount is written by hand from section 5.4 of the design
// (tokens x rate per million / 10^6); none is computed with the code under test.
func TestCost(t *testing.T) {
	plain := version(t, "plain", rate(t, catInput, 0, "2.5"), rate(t, catOutput, 0, "10"))
	cached := version(t, "cached",
		rate(t, catInput, 0, "3"), rate(t, catOutput, 0, "15"), rate(t, catCacheRead, 0, "0.3"), rate(t, catCacheWrite, 0, "3.75"))
	tiered := version(t, "tiered",
		rate(t, catInput, 0, "1"), rate(t, catInput, 100000, "2"), rate(t, catInput, 500000, "4"),
		rate(t, catOutput, 0, "3"), rate(t, catOutput, 100000, "6"))
	onlyInput := version(t, "only-input", rate(t, catInput, 0, "3"))

	rows := []struct {
		name      string
		v         pricing.Version
		u         family.Usage
		amount    string
		fallbacks []string
		lines     map[pricing.Category]string // category -> rate per million after tier and fallback
	}{
		{name: "input and output", v: plain, u: family.Usage{InputTokens: 1000, OutputTokens: 500},
			amount: "0.0075", lines: map[pricing.Category]string{catInput: "2.5", catOutput: "10"}},
		{name: "one million of each", v: plain, u: family.Usage{InputTokens: 1000000, OutputTokens: 1000000}, amount: "12.5"},
		{name: "input only", v: plain, u: family.Usage{InputTokens: 4000}, amount: "0.01"},
		{name: "output only", v: plain, u: family.Usage{OutputTokens: 100}, amount: "0.001"},
		{name: "reasoning is inside the output", v: plain, u: family.Usage{InputTokens: 10, OutputTokens: 1000, ReasoningTokens: 400},
			amount: "0.010025", lines: map[pricing.Category]string{catInput: "2.5", catOutput: "10"}},
		{name: "cache read and write are priced apart and leave the input", v: cached,
			u:      family.Usage{InputTokens: 1000, CacheReadTokens: 400, CacheWriteTokens: 100, OutputTokens: 200},
			amount: "0.004995",
			lines:  map[pricing.Category]string{catInput: "3", catOutput: "15", catCacheRead: "0.3", catCacheWrite: "3.75"}},
		{name: "cache read only", v: cached, u: family.Usage{InputTokens: 1000, CacheReadTokens: 1000}, amount: "0.0003"},
		{name: "cache read priced as input when it has no rate", v: plain,
			u: family.Usage{InputTokens: 1000, CacheReadTokens: 400}, amount: "0.0025",
			fallbacks: []string{"cache_read"}, lines: map[pricing.Category]string{catInput: "2.5", catCacheRead: "2.5"}},
		{name: "cache write priced as input when it has no rate", v: plain,
			u: family.Usage{InputTokens: 1000, CacheWriteTokens: 200}, amount: "0.0025", fallbacks: []string{"cache_write"}},
		{name: "one hour cache write priced as cache write", v: cached,
			u:      family.Usage{InputTokens: 1000, CacheWriteTokens: 100, Extra: map[string]int64{"cache_write_1h_tokens": 40}},
			amount: "0.003075", fallbacks: []string{"cache_write_1h"},
			lines: map[pricing.Category]string{catInput: "3", catCacheWrite: "3.75", catCacheWrite1h: "3.75"}},
		{name: "one hour cache write with its own rate", v: version(t, "1h",
			rate(t, catInput, 0, "3"), rate(t, catCacheWrite, 0, "3.75"), rate(t, catCacheWrite1h, 0, "6")),
			u:      family.Usage{InputTokens: 1000, CacheWriteTokens: 100, Extra: map[string]int64{"cache_write_1h_tokens": 40}},
			amount: "0.003165", lines: map[pricing.Category]string{catCacheWrite1h: "6"}},
		{name: "one hour cache write falls back to cache write, then to input", v: plain,
			u:      family.Usage{InputTokens: 1000, CacheWriteTokens: 100, Extra: map[string]int64{"cache_write_1h_tokens": 40}},
			amount: "0.0025", fallbacks: []string{"cache_write", "cache_write_1h"}},
		{name: "input audio with its own rate", v: version(t, "audio",
			rate(t, catInput, 0, "2"), rate(t, catOutput, 0, "8"), rate(t, catInputAudio, 0, "30"), rate(t, catOutputAudio, 0, "60")),
			u:      family.Usage{InputTokens: 1000, OutputTokens: 500, Extra: map[string]int64{"input_audio_tokens": 100, "output_audio_tokens": 50}},
			amount: "0.0114", // 900 x 2 + 100 x 30 + 450 x 8 + 50 x 60, per million
			lines:  map[pricing.Category]string{catInput: "2", catInputAudio: "30", catOutput: "8", catOutputAudio: "60"}},
		{name: "audio priced as text when it has no rate", v: plain,
			u:      family.Usage{InputTokens: 1000, OutputTokens: 500, Extra: map[string]int64{"input_audio_tokens": 100, "output_audio_tokens": 50}},
			amount: "0.0075", fallbacks: []string{"input_audio", "output_audio"}},
		{name: "tier below the threshold", v: tiered, u: family.Usage{InputTokens: 99999, OutputTokens: 1000},
			amount: "0.102999", // 99999 x 1 + 1000 x 3, per million
			lines:  map[pricing.Category]string{catInput: "1", catOutput: "3"}},
		{name: "tier at the threshold", v: tiered, u: family.Usage{InputTokens: 100000, OutputTokens: 1000},
			amount: "0.206", lines: map[pricing.Category]string{catInput: "2", catOutput: "6"}},
		{name: "tier above the threshold", v: tiered, u: family.Usage{InputTokens: 100001, OutputTokens: 1000},
			amount: "0.206002", lines: map[pricing.Category]string{catInput: "2", catOutput: "6"}},
		{name: "second tier", v: tiered, u: family.Usage{InputTokens: 500000, OutputTokens: 10},
			amount: "2.00006", lines: map[pricing.Category]string{catInput: "4", catOutput: "6"}},
		{name: "the tier is chosen by the total input, cache included", v: version(t, "tier-cache",
			rate(t, catInput, 0, "1"), rate(t, catInput, 100000, "2"), rate(t, catCacheRead, 0, "0.1")),
			u:      family.Usage{InputTokens: 100000, CacheReadTokens: 90000},
			amount: "0.029", // 10000 x 2 + 90000 x 0.1, per million; cache_read has only a base rate
			lines:  map[pricing.Category]string{catInput: "2", catCacheRead: "0.1"}},
		{name: "zero units that are unpriced are free", v: onlyInput, u: family.Usage{InputTokens: 1000, OutputTokens: 0}, amount: "0.003"},
		{name: "no usage at all", v: plain, u: family.Usage{}, amount: "0"},
		{name: "a rate of zero", v: version(t, "free", rate(t, catInput, 0, "0"), rate(t, catOutput, 0, "0")),
			u: family.Usage{InputTokens: 5000, OutputTokens: 5000}, amount: "0"},
		{name: "exact to eighteen fraction digits", v: version(t, "tiny", rate(t, catInput, 0, "0.000000000001")),
			u: family.Usage{InputTokens: 1}, amount: "0.000000000000000001"},
		{name: "large counts stay exact", v: version(t, "big", rate(t, catInput, 0, "1000000"), rate(t, catOutput, 0, "0")),
			u: family.Usage{InputTokens: 9000000000000000000}, amount: "9000000000000000000"},
		{name: "twelve digit rate times seven tokens", v: version(t, "tiny7", rate(t, catInput, 0, "0.123456789012")),
			u: family.Usage{InputTokens: 7}, amount: "0.000000864197523084"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			c := pricing.CostOf(r.u, r.v, true)
			if !c.Known || c.Reason != "" {
				t.Fatalf("unknown: reason %q", c.Reason)
			}
			if got := c.Amount.String(); got != r.amount {
				t.Errorf("amount = %s, want %s", got, r.amount)
			}
			if c.Currency != "USD" || c.VersionID != r.v.ID {
				t.Errorf("currency %q, version %q", c.Currency, c.VersionID)
			}
			if sumLines(c).Cmp(c.Amount) != 0 {
				t.Errorf("the lines sum to %s, the amount is %s", sumLines(c), c.Amount)
			}
			want := r.fallbacks
			if want == nil {
				want = []string{}
			}
			got := cats(c.Fallbacks)
			if got == nil {
				got = []string{}
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("fallbacks = %v, want %v", got, want)
			}
			for cat, wantRate := range r.lines {
				found := false
				for _, l := range c.Lines {
					if l.Category == cat && l.Units > 0 {
						found = true
						if l.Rate.String() != wantRate {
							t.Errorf("line %s: rate %s, want %s", cat, l.Rate, wantRate)
						}
					}
				}
				if !found {
					t.Errorf("no line for %s: %+v", cat, c.Lines)
				}
			}
		})
	}

	t.Run("units of the lines", func(t *testing.T) {
		c := pricing.CostOf(family.Usage{InputTokens: 1000, CacheReadTokens: 400, CacheWriteTokens: 100, OutputTokens: 200, ReasoningTokens: 50,
			Extra: map[string]int64{"input_audio_tokens": 50}}, version(t, "u",
			rate(t, catInput, 0, "1"), rate(t, catOutput, 0, "1"), rate(t, catCacheRead, 0, "1"), rate(t, catCacheWrite, 0, "1"), rate(t, catInputAudio, 0, "1")), true)
		want := map[pricing.Category]int64{catInput: 450, catCacheRead: 400, catCacheWrite: 100, catOutput: 200, catInputAudio: 50}
		got := map[pricing.Category]int64{}
		for _, l := range c.Lines {
			if l.Units != 0 {
				got[l.Category] = l.Units
			}
		}
		if len(got) != len(want) {
			t.Fatalf("units %v, want %v", got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("units of %s = %d, want %d", k, got[k], v)
			}
		}
	})

	t.Run("lines follow the order of the categories", func(t *testing.T) {
		order := []pricing.Category{catInput, catOutput, catCacheRead, catCacheWrite, catCacheWrite1h, catInputAudio, catOutputAudio}
		v := version(t, "all", rate(t, catInput, 0, "1"), rate(t, catOutput, 0, "1"), rate(t, catCacheRead, 0, "1"), rate(t, catCacheWrite, 0, "1"),
			rate(t, catCacheWrite1h, 0, "1"), rate(t, catInputAudio, 0, "1"), rate(t, catOutputAudio, 0, "1"))
		c := pricing.CostOf(family.Usage{InputTokens: 1000, OutputTokens: 1000, CacheReadTokens: 10, CacheWriteTokens: 100,
			Extra: map[string]int64{"cache_write_1h_tokens": 10, "input_audio_tokens": 10, "output_audio_tokens": 10}}, v, true)
		pos := -1
		for _, l := range c.Lines {
			if l.Units == 0 {
				continue
			}
			i := -1
			for j, o := range order {
				if o == l.Category {
					i = j
				}
			}
			if i <= pos {
				t.Errorf("category %s is out of order: %+v", l.Category, c.Lines)
			}
			pos = i
		}
	})

	t.Run("no price", func(t *testing.T) {
		c := pricing.CostOf(family.Usage{InputTokens: 1000}, pricing.Version{}, false)
		if c.Known || c.Reason != "no_price" || c.VersionID != "" || c.Amount.Sign() != 0 || len(c.Lines) != 0 {
			t.Errorf("%+v", c)
		}
	})

	t.Run("a category without a rate and without a fallback", func(t *testing.T) {
		for name, u := range map[string]family.Usage{
			"output":       {InputTokens: 1000, OutputTokens: 10},
			"output audio": {InputTokens: 10, Extra: map[string]int64{"output_audio_tokens": 0}, OutputTokens: 5},
		} {
			c := pricing.CostOf(u, onlyInput, true)
			if c.Known || c.Reason != "category_unpriced" || c.Amount.Sign() != 0 {
				t.Errorf("%s: %+v", name, c)
			}
		}
		// A fallback that exists is not an error, and the cost is not known when it does not.
		noInput := version(t, "no-input", rate(t, catOutput, 0, "1"))
		if c := pricing.CostOf(family.Usage{InputTokens: 10}, noInput, true); c.Known || c.Reason != "category_unpriced" {
			t.Errorf("input without a rate: %+v", c)
		}
		if c := pricing.CostOf(family.Usage{InputTokens: 10, CacheReadTokens: 10}, noInput, true); c.Known || c.Reason != "category_unpriced" {
			t.Errorf("cache read with no input rate to fall back on: %+v", c)
		}
	})

	t.Run("inconsistent usage", func(t *testing.T) {
		for name, u := range map[string]family.Usage{
			"cache read above input":           {InputTokens: 100, CacheReadTokens: 200},
			"cache read and write above input": {InputTokens: 100, CacheReadTokens: 60, CacheWriteTokens: 60},
			"one hour above cache write":       {InputTokens: 1000, CacheWriteTokens: 10, Extra: map[string]int64{"cache_write_1h_tokens": 20}},
			"input audio above input":          {InputTokens: 10, Extra: map[string]int64{"input_audio_tokens": 20}},
			"output audio above output":        {InputTokens: 10, OutputTokens: 10, Extra: map[string]int64{"output_audio_tokens": 20}},
			"negative input":                   {InputTokens: -1},
			"negative output":                  {OutputTokens: -1},
		} {
			c := pricing.CostOf(u, cached, true)
			if c.Known || c.Reason != "inconsistent_usage" || c.Amount.Sign() != 0 {
				t.Errorf("%s: %+v", name, c)
			}
		}
	})
}
