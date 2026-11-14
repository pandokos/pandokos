package pricing

import "github.com/pandokos/pandokos/internal/family"

// Reasons of an unknown cost.
const (
	ReasonNoPrice           = "no_price"
	ReasonCategoryUnpriced  = "category_unpriced"
	ReasonInconsistentUsage = "inconsistent_usage"
)

// Cost is the price of one request.
type Cost struct {
	Known     bool
	Reason    string  // when !Known: "no_price", "category_unpriced", "inconsistent_usage"
	Amount    Decimal // in Currency; zero when !Known
	Currency  string
	VersionID string     // "" when no version
	Lines     []CostLine // the breakdown, in category order, for the categories that have units
	Fallbacks []Category // categories priced at another category's rate
}

// CostLine is the cost of one category.
type CostLine struct {
	Category Category
	Units    int64
	Rate     Decimal // per million, after tier and fallback
	Amount   Decimal
}

// fallbacks names, for each category, the categories whose rate stands in when it has none.
var fallbacks = map[Category][]Category{
	CategoryCacheRead:    {CategoryInput},
	CategoryCacheWrite:   {CategoryInput},
	CategoryInputAudio:   {CategoryInput},
	CategoryCacheWrite1h: {CategoryCacheWrite, CategoryInput},
	CategoryOutputAudio:  {CategoryOutput},
}

// tierRate returns the rate of c that applies to a request of the given input tokens: the one
// with the greatest threshold not above them.
func tierRate(rates []Rate, c Category, input int64) (Rate, bool) {
	var best Rate
	found := false
	for _, r := range rates {
		if r.Category != c || r.Threshold > input {
			continue
		}
		if !found || r.Threshold > best.Threshold {
			best, found = r, true
		}
	}
	return best, found
}

// CostOf computes the cost of the usage at the version in force; found is false when there is
// none. The tokens are split into categories (input excludes cache and audio, output excludes
// audio, reasoning stays in output), each priced at its tier chosen by the request's total
// input tokens. A category without a rate is priced at its fallback; without one the cost is
// unknown. The amount is exact.
func CostOf(u family.Usage, v Version, found bool) Cost {
	if !found {
		return Cost{Reason: ReasonNoPrice}
	}
	unknown := func(reason string) Cost {
		return Cost{Reason: reason, Currency: v.Currency, VersionID: v.ID}
	}
	cacheWrite1h := u.Extra["cache_write_1h_tokens"]
	inputAudio := u.Extra["input_audio_tokens"]
	outputAudio := u.Extra["output_audio_tokens"]
	units := map[Category]int64{
		CategoryInput:        u.InputTokens - u.CacheReadTokens - u.CacheWriteTokens - inputAudio,
		CategoryOutput:       u.OutputTokens - outputAudio,
		CategoryCacheRead:    u.CacheReadTokens,
		CategoryCacheWrite:   u.CacheWriteTokens - cacheWrite1h,
		CategoryCacheWrite1h: cacheWrite1h,
		CategoryInputAudio:   inputAudio,
		CategoryOutputAudio:  outputAudio,
	}
	for _, c := range Categories {
		if units[c] < 0 {
			return unknown(ReasonInconsistentUsage)
		}
	}
	cost := Cost{Known: true, Currency: v.Currency, VersionID: v.ID}
	for _, c := range Categories {
		n := units[c]
		if n == 0 {
			continue
		}
		r, ok := tierRate(v.Rates, c, u.InputTokens)
		if !ok {
			for _, f := range fallbacks[c] {
				if r, ok = tierRate(v.Rates, f, u.InputTokens); ok {
					cost.Fallbacks = append(cost.Fallbacks, c)
					break
				}
			}
		}
		if !ok {
			return unknown(ReasonCategoryUnpriced)
		}
		amount := r.PerMillion.MulInt(n).DivPow10(6)
		cost.Lines = append(cost.Lines, CostLine{Category: c, Units: n, Rate: r.PerMillion, Amount: amount})
		cost.Amount = cost.Amount.Add(amount)
	}
	return cost
}
