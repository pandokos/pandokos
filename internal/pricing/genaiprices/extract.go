package genaiprices

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/pricing"
)

// Reasons of an Issue.
const (
	ReasonRegexUnsupported      = "regex_unsupported"
	ReasonScheduledPrice        = "scheduled_price_not_imported"
	ReasonTimeOfDayPrice        = "time_of_day_price_ignored"
	ReasonPriceVariantsIgnored  = "price_variants_ignored"
	ReasonCategoryIgnored       = "category_ignored"
	ReasonValueNotRepresentable = "value_not_representable"
	ReasonNoSupportedPrice      = "no_supported_price"
)

// Issue is something the importer reports about a model. Detail holds identifiers only (a
// category name, a variant key, a model name), never a value of the document.
type Issue struct{ Reason, Detail string }

var (
	// ErrSkipped is returned by Extract for a model that has no usable price; the issues say why.
	ErrSkipped = errors.New("genaiprices: the model has no importable price")
	// ErrMalformedPrices is returned by Extract when the prices of a model have the wrong shape
	// or types. It quotes nothing of the document.
	ErrMalformedPrices = errors.New("genaiprices: the prices of the model have an unexpected shape")
)

// mtok maps the categories the importer supports to the member names of the dataset.
var mtok = map[string]pricing.Category{
	"input_mtok":          pricing.CategoryInput,
	"output_mtok":         pricing.CategoryOutput,
	"cache_read_mtok":     pricing.CategoryCacheRead,
	"cache_write_mtok":    pricing.CategoryCacheWrite,
	"cache_write_1h_mtok": pricing.CategoryCacheWrite1h,
	"input_audio_mtok":    pricing.CategoryInputAudio,
	"output_audio_mtok":   pricing.CategoryOutputAudio,
}

type conditional struct {
	Constraint *struct {
		StartDate *string          `json:"start_date"`
		StartTime *json.RawMessage `json:"start_time"`
		EndTime   *json.RawMessage `json:"end_time"`
	} `json:"constraint"`
	Prices json.RawMessage `json:"prices"`
}

// Extract reads the price of a model on the UTC day today and returns it as a version in USD
// with no identity (no ID, model or ValidFrom). The prices are an object or a list of
// conditional prices, of which the last whose constraint is absent or a start date not after
// today applies. Constructs the importer does not follow are reported as issues and a model
// with nothing usable returns ErrSkipped with the issues.
func Extract(m *Model, today time.Time) (pricing.Version, []Issue, error) {
	var issues []Issue
	day := time.Date(today.UTC().Year(), today.UTC().Month(), today.UTC().Day(), 0, 0, 0, 0, time.UTC)

	if len(m.PriceVariants) > 0 {
		keys := map[string]bool{}
		for _, v := range m.PriceVariants {
			for k := range v.When {
				keys[k] = true
			}
		}
		issues = append(issues, Issue{Reason: ReasonPriceVariantsIgnored, Detail: strings.Join(sortedKeys(keys), ",")})
	}

	raw := bytes.TrimSpace(m.Prices)
	if len(raw) == 0 {
		return pricing.Version{}, issues, ErrMalformedPrices
	}
	var candidate json.RawMessage
	switch raw[0] {
	case '{':
		candidate = raw
	case '[':
		var entries []conditional
		if err := json.Unmarshal(raw, &entries); err != nil {
			return pricing.Version{}, issues, ErrMalformedPrices
		}
		for _, e := range entries {
			if e.Constraint != nil && (e.Constraint.StartTime != nil || e.Constraint.EndTime != nil) {
				issues = append(issues, Issue{Reason: ReasonTimeOfDayPrice})
				continue
			}
			if e.Constraint != nil && e.Constraint.StartDate != nil {
				start, err := time.Parse(time.DateOnly, *e.Constraint.StartDate)
				if err != nil {
					return pricing.Version{}, issues, ErrMalformedPrices
				}
				if start.After(day) {
					issues = append(issues, Issue{Reason: ReasonScheduledPrice})
					continue
				}
			}
			candidate = e.Prices
		}
		if candidate == nil {
			return pricing.Version{}, append(issues, Issue{Reason: ReasonNoSupportedPrice}), ErrSkipped
		}
	default:
		return pricing.Version{}, issues, ErrMalformedPrices
	}

	var prices map[string]json.RawMessage
	if err := json.Unmarshal(candidate, &prices); err != nil || prices == nil {
		return pricing.Version{}, issues, ErrMalformedPrices
	}
	names := make([]string, 0, len(prices))
	for k := range prices {
		names = append(names, k)
	}
	sort.Strings(names)

	var rates []pricing.Rate
	unrepresentable := false
	for _, name := range names {
		cat, ok := mtok[name]
		if !ok {
			issues = append(issues, Issue{Reason: ReasonCategoryIgnored, Detail: name})
			continue
		}
		rs, status := categoryRates(cat, prices[name])
		switch status {
		case statusMalformed:
			return pricing.Version{}, issues, ErrMalformedPrices
		case statusUnrepresentable:
			unrepresentable = true
			issues = append(issues, Issue{Reason: ReasonValueNotRepresentable, Detail: name})
		default:
			rates = append(rates, rs...)
		}
	}
	if unrepresentable {
		return pricing.Version{}, issues, ErrSkipped
	}
	if len(rates) == 0 {
		return pricing.Version{}, append(issues, Issue{Reason: ReasonNoSupportedPrice}), ErrSkipped
	}
	sort.SliceStable(rates, func(i, j int) bool {
		ci, cj := categoryRank(rates[i].Category), categoryRank(rates[j].Category)
		if ci != cj {
			return ci < cj
		}
		return rates[i].Threshold < rates[j].Threshold
	})
	v := pricing.Version{Currency: "USD", Source: "import", Rates: rates}
	if v.Validate() != nil {
		return pricing.Version{}, append(issues, Issue{Reason: ReasonValueNotRepresentable}), ErrSkipped
	}
	return v, issues, nil
}

func categoryRank(c pricing.Category) int {
	for i, x := range pricing.Categories {
		if x == c {
			return i
		}
	}
	return len(pricing.Categories)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type status int

const (
	statusOK status = iota
	statusMalformed
	statusUnrepresentable
)

const (
	maxTierStart = 100_000_000
	maxRateText  = 1_000_000
)

// categoryRates reads the value of a category: a number, or {base, tiers: [{start, price}]}.
func categoryRates(cat pricing.Category, raw json.RawMessage) ([]pricing.Rate, status) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, statusMalformed
	}
	if raw[0] != '{' {
		d, st := rateOf(raw)
		if st != statusOK {
			return nil, st
		}
		return []pricing.Rate{{Category: cat, PerMillion: d}}, statusOK
	}
	var tiered struct {
		Base  json.RawMessage `json:"base"`
		Tiers []struct {
			Start json.RawMessage `json:"start"`
			Price json.RawMessage `json:"price"`
		} `json:"tiers"`
	}
	if err := json.Unmarshal(raw, &tiered); err != nil || len(tiered.Base) == 0 {
		return nil, statusMalformed
	}
	base, st := rateOf(tiered.Base)
	if st != statusOK {
		return nil, st
	}
	rates := []pricing.Rate{{Category: cat, PerMillion: base}}
	result := statusOK
	for _, t := range tiered.Tiers {
		start, st := numberOf(t.Start)
		if st != statusOK {
			if st == statusMalformed {
				return nil, st
			}
			result = statusUnrepresentable
			continue
		}
		price, st := rateOf(t.Price)
		if st != statusOK {
			if st == statusMalformed {
				return nil, st
			}
			result = statusUnrepresentable
			continue
		}
		n, ok := integerOf(start)
		if !ok || n < 0 || n > maxTierStart {
			result = statusUnrepresentable
			continue
		}
		rates = append(rates, pricing.Rate{Category: cat, Threshold: n, PerMillion: price})
	}
	if result != statusOK {
		return nil, result
	}
	return rates, statusOK
}

// rateOf reads a rate: a non-negative number of at most 12 fraction digits up to 1 000 000.
func rateOf(raw json.RawMessage) (pricing.Decimal, status) {
	d, st := numberOf(raw)
	if st != statusOK {
		return d, st
	}
	limit, _ := pricing.ParseDecimal("1000000")
	if d.Sign() < 0 || d.Cmp(limit) > 0 || fractionDigits(d) > 12 {
		return pricing.Decimal{}, statusUnrepresentable
	}
	return d, statusOK
}

func fractionDigits(d pricing.Decimal) int {
	_, frac, ok := strings.Cut(d.String(), ".")
	if !ok {
		return 0
	}
	return len(frac)
}

// integerOf converts a decimal that has no fraction to an int64 (at most 10^8 is of interest).
func integerOf(d pricing.Decimal) (int64, bool) {
	s := d.String()
	if strings.Contains(s, ".") || len(s) > 12 {
		return 0, false
	}
	neg := strings.HasPrefix(s, "-")
	var n int64
	for _, c := range strings.TrimPrefix(s, "-") {
		n = n*10 + int64(c-'0')
	}
	if neg {
		n = -n
	}
	return n, true
}

var jsonNumber = regexp.MustCompile(`^(-?)(0|[1-9][0-9]*)(?:\.([0-9]+))?(?:[eE]([-+]?[0-9]+))?$`)

// numberOf reads a JSON number from its own text, exponent included, without floating point.
func numberOf(raw json.RawMessage) (pricing.Decimal, status) {
	m := jsonNumber.FindStringSubmatch(string(bytes.TrimSpace(raw)))
	if m == nil {
		return pricing.Decimal{}, statusMalformed
	}
	sign, intPart, frac, expText := m[1], m[2], m[3], m[4]
	exp := 0
	if expText != "" {
		neg := strings.HasPrefix(expText, "-")
		expText = strings.TrimLeft(expText, "+-")
		if len(expText) > 3 {
			return pricing.Decimal{}, statusUnrepresentable
		}
		for _, c := range expText {
			exp = exp*10 + int(c-'0')
		}
		if neg {
			exp = -exp
		}
	}
	digits := intPart + frac
	point := len(intPart) + exp
	var text string
	switch {
	case point <= 0:
		if point < -40 {
			return pricing.Decimal{}, statusUnrepresentable
		}
		text = "0." + strings.Repeat("0", -point) + digits
	case point >= len(digits):
		if point > 80 {
			return pricing.Decimal{}, statusUnrepresentable
		}
		text = digits + strings.Repeat("0", point-len(digits))
	default:
		text = digits[:point] + "." + digits[point:]
	}
	// Drop leading zeros of the integer part, keeping one digit.
	ip, fp, hasPoint := strings.Cut(text, ".")
	ip = strings.TrimLeft(ip, "0")
	if ip == "" {
		ip = "0"
	}
	text = ip
	if hasPoint {
		text += "." + fp
	}
	d, err := pricing.ParseDecimal(sign + text)
	if err != nil {
		return pricing.Decimal{}, statusUnrepresentable
	}
	return d, statusOK
}
