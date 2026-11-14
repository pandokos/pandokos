package pricing

import (
	"errors"
	"sort"
	"time"
)

// Category is a kind of unit that has a price.
type Category string

// The categories, in the order of cost lines.
const (
	CategoryInput        Category = "input"
	CategoryOutput       Category = "output"
	CategoryCacheRead    Category = "cache_read"
	CategoryCacheWrite   Category = "cache_write"
	CategoryCacheWrite1h Category = "cache_write_1h"
	CategoryInputAudio   Category = "input_audio"
	CategoryOutputAudio  Category = "output_audio"
)

// Categories lists every category in the canonical order.
var Categories = []Category{
	CategoryInput, CategoryOutput, CategoryCacheRead, CategoryCacheWrite,
	CategoryCacheWrite1h, CategoryInputAudio, CategoryOutputAudio,
}

// rank is the position of c in Categories; unknown categories come last.
func (c Category) rank() int {
	for i, x := range Categories {
		if x == c {
			return i
		}
	}
	return len(Categories)
}

// Limits of a version.
const (
	maxThreshold    = 100_000_000
	maxRateFraction = 12
)

var maxRate = Decimal{unscaled: pow10(6)}

// Rate is a price per one million units of a category.
type Rate struct {
	Category   Category
	Threshold  int64 // start of a tier, in input tokens of the request; 0 for the base rate
	PerMillion Decimal
}

// Version is a price of a model from a start instant. Versions are immutable.
type Version struct {
	ID, ModelID string
	ValidFrom   time.Time
	Currency    string // ISO 4217 letters
	Source      string // import, override
	Rates       []Rate // sorted by category, then threshold
}

// Rules a version can break. None carries a value.
var (
	errVersionCurrency   = errors.New("pricing: currency must be three capital letters")
	errVersionNoRates    = errors.New("pricing: a version needs at least one rate")
	errVersionCategory   = errors.New("pricing: unknown rate category")
	errVersionNoBase     = errors.New("pricing: every category with a rate needs a rate at threshold 0")
	errVersionThreshold  = errors.New("pricing: tier threshold out of range: 0 to 100000000")
	errVersionDuplicate  = errors.New("pricing: two rates share a category and a threshold")
	errVersionRateRange  = errors.New("pricing: rate out of range: 0 to 1000000 per million")
	errVersionRateDigits = errors.New("pricing: rate has more than 12 fraction digits")
)

// Validate checks the rules of a version: a currency of three capital letters, at least one
// rate, a rate at threshold 0 for every category, unique thresholds from 0 to 100 000 000, and
// rates from 0 to 1 000 000 with at most 12 fraction digits. An error names the rule, never a
// value.
func (v Version) Validate() error {
	if len(v.Currency) != 3 {
		return errVersionCurrency
	}
	for i := 0; i < 3; i++ {
		if v.Currency[i] < 'A' || v.Currency[i] > 'Z' {
			return errVersionCurrency
		}
	}
	if len(v.Rates) == 0 {
		return errVersionNoRates
	}
	type key struct {
		c Category
		t int64
	}
	seen := make(map[key]bool, len(v.Rates))
	base := map[Category]bool{}
	cats := map[Category]bool{}
	for _, r := range v.Rates {
		if r.Category.rank() == len(Categories) {
			return errVersionCategory
		}
		if r.Threshold < 0 || r.Threshold > maxThreshold {
			return errVersionThreshold
		}
		k := key{r.Category, r.Threshold}
		if seen[k] {
			return errVersionDuplicate
		}
		seen[k] = true
		if r.PerMillion.Sign() < 0 || r.PerMillion.Cmp(maxRate) > 0 {
			return errVersionRateRange
		}
		if r.PerMillion.scale > maxRateFraction {
			return errVersionRateDigits
		}
		cats[r.Category] = true
		if r.Threshold == 0 {
			base[r.Category] = true
		}
	}
	for c := range cats {
		if !base[c] {
			return errVersionNoBase
		}
	}
	return nil
}

// sortedRates returns a copy of the rates sorted by category, then threshold.
func sortedRates(rs []Rate) []Rate {
	out := append([]Rate(nil), rs...)
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := out[i].Category.rank(), out[j].Category.rank(); ri != rj {
			return ri < rj
		}
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Threshold < out[j].Threshold
	})
	return out
}

// SameRates reports whether v and w have the same rates, whatever their order and the trailing
// zeros of their texts. It does not compare the currency.
func (v Version) SameRates(w Version) bool {
	if len(v.Rates) != len(w.Rates) {
		return false
	}
	a, b := sortedRates(v.Rates), sortedRates(w.Rates)
	for i := range a {
		if a[i].Category != b[i].Category || a[i].Threshold != b[i].Threshold || a[i].PerMillion.Cmp(b[i].PerMillion) != 0 {
			return false
		}
	}
	return true
}
