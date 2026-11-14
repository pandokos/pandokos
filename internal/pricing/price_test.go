package pricing_test

import (
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/pricing"
)

const (
	catInput        pricing.Category = "input"
	catOutput       pricing.Category = "output"
	catCacheRead    pricing.Category = "cache_read"
	catCacheWrite   pricing.Category = "cache_write"
	catCacheWrite1h pricing.Category = "cache_write_1h"
	catInputAudio   pricing.Category = "input_audio"
	catOutputAudio  pricing.Category = "output_audio"
)

// rate builds a rate from decimal text.
func rate(t testing.TB, c pricing.Category, threshold int64, perMillion string) pricing.Rate {
	t.Helper()
	return pricing.Rate{Category: c, Threshold: threshold, PerMillion: mustDec(t, perMillion)}
}

// version builds a valid USD version from category, threshold and rate triples.
func version(t testing.TB, id string, rates ...pricing.Rate) pricing.Version {
	t.Helper()
	return pricing.Version{
		ID: id, ModelID: "model-1", ValidFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Currency: "USD", Source: "override", Rates: rates,
	}
}

// TestVersionValidate is T-373.
func TestVersionValidate(t *testing.T) {
	base := func() []pricing.Rate {
		return []pricing.Rate{rate(t, catInput, 0, "2.5"), rate(t, catOutput, 0, "10")}
	}
	if err := version(t, "v", base()...).Validate(); err != nil {
		t.Fatalf("a valid version: %v", err)
	}
	// The bounds themselves.
	ok := version(t, "v",
		rate(t, catInput, 0, "1000000"), rate(t, catInput, 100000000, "0"), rate(t, catOutput, 0, "0.000000000001"),
		rate(t, catCacheRead, 0, "0"), rate(t, catCacheWrite, 0, "1"), rate(t, catCacheWrite1h, 0, "1"),
		rate(t, catInputAudio, 0, "1"), rate(t, catOutputAudio, 0, "1"))
	if err := ok.Validate(); err != nil {
		t.Errorf("the bounds: %v", err)
	}

	cases := map[string]func(v *pricing.Version){
		"currency in lower case":      func(v *pricing.Version) { v.Currency = "usd" },
		"currency of two letters":     func(v *pricing.Version) { v.Currency = "US" },
		"currency of four letters":    func(v *pricing.Version) { v.Currency = "USDX" },
		"currency with a digit":       func(v *pricing.Version) { v.Currency = "US1" },
		"empty currency":              func(v *pricing.Version) { v.Currency = "" },
		"currency with a space":       func(v *pricing.Version) { v.Currency = "US " },
		"no rate":                     func(v *pricing.Version) { v.Rates = nil },
		"no base rate for a category": func(v *pricing.Version) { v.Rates = append(v.Rates, rate(t, catCacheRead, 1000, "1")) },
		"only a tier, no base":        func(v *pricing.Version) { v.Rates = []pricing.Rate{rate(t, catInput, 100000, "1")} },
		"duplicate threshold":         func(v *pricing.Version) { v.Rates = append(v.Rates, rate(t, catInput, 0, "3")) },
		"duplicate tier": func(v *pricing.Version) {
			v.Rates = append(v.Rates, rate(t, catInput, 10, "3"), rate(t, catInput, 10, "4"))
		},
		"negative rate":            func(v *pricing.Version) { v.Rates[0] = rate(t, catInput, 0, "-0.000001") },
		"above one million":        func(v *pricing.Version) { v.Rates[0] = rate(t, catInput, 0, "1000000.000001") },
		"thirteen fraction digits": func(v *pricing.Version) { v.Rates[0] = rate(t, catInput, 0, "0.1234567890123") },
		"unknown category":         func(v *pricing.Version) { v.Rates = append(v.Rates, rate(t, "web_searches", 0, "1")) },
		"empty category":           func(v *pricing.Version) { v.Rates[0].Category = "" },
		"negative threshold":       func(v *pricing.Version) { v.Rates = append(v.Rates, rate(t, catInput, -1, "1")) },
		"threshold above one hundred million": func(v *pricing.Version) {
			v.Rates = append(v.Rates, rate(t, catInput, 100000001, "1"))
		},
	}
	for name, mutate := range cases {
		v := version(t, "v", base()...)
		mutate(&v)
		err := v.Validate()
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		// An error names the rule and never a value.
		for _, leak := range []string{"1000000.000001", "0.1234567890123", "web_searches", "USDX"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("%s: the error quotes a value: %v", name, err)
			}
		}
	}

	t.Run("SameRates", func(t *testing.T) {
		a := version(t, "a", rate(t, catInput, 0, "2.5"), rate(t, catInput, 100000, "5"), rate(t, catOutput, 0, "10"))
		b := version(t, "b", rate(t, catOutput, 0, "10.000"), rate(t, catInput, 100000, "5.0"), rate(t, catInput, 0, "2.50"))
		if !a.SameRates(b) || !b.SameRates(a) {
			t.Error("the same rates in another order and with trailing zeros differ")
		}
		for name, other := range map[string]pricing.Version{
			"another rate":      version(t, "c", rate(t, catInput, 0, "2.51"), rate(t, catInput, 100000, "5"), rate(t, catOutput, 0, "10")),
			"another threshold": version(t, "c", rate(t, catInput, 0, "2.5"), rate(t, catInput, 100001, "5"), rate(t, catOutput, 0, "10")),
			"another category":  version(t, "c", rate(t, catInput, 0, "2.5"), rate(t, catInput, 100000, "5"), rate(t, catCacheRead, 0, "10")),
			"a rate missing":    version(t, "c", rate(t, catInput, 0, "2.5"), rate(t, catOutput, 0, "10")),
			"a rate more":       version(t, "c", rate(t, catInput, 0, "2.5"), rate(t, catInput, 100000, "5"), rate(t, catOutput, 0, "10"), rate(t, catCacheRead, 0, "1")),
		} {
			if a.SameRates(other) || other.SameRates(a) {
				t.Errorf("%s: reported as the same", name)
			}
		}
	})
}
