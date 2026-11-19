package pricing_test

import (
	"math/rand/v2"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/pricing"
)

// priced builds a version of a currency from a start, with an input rate of 1 and an output rate
// of 2 unless a rate is given.
func priced(t testing.TB, id, currency string, from time.Time, rates ...pricing.Rate) pricing.Version {
	t.Helper()
	if len(rates) == 0 {
		rates = []pricing.Rate{rate(t, catInput, 0, "1"), rate(t, catOutput, 0, "2")}
	}
	v := version(t, id, rates...)
	v.Currency, v.ValidFrom = currency, from
	return v
}

func ended(id, currency string, from time.Time) pricing.Version {
	return pricing.Version{ID: id, ModelID: "model-1", ValidFrom: from, Currency: currency, Source: "tenant", End: true}
}

// TestListAt is T-726: the rule of section 5.2, each row of it.
func TestListAt(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	h := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Hour) }
	usd := pricing.Active{Current: "USD"}
	eur := pricing.Active{Current: "EUR"}

	type want struct {
		id, source string // "" for no price
	}
	for _, c := range []struct {
		name string
		l    pricing.List
		at   time.Time
		want want
	}{
		{"the tenant's price in the active currency", pricing.List{Currency: eur,
			Catalogue: []pricing.Version{priced(t, "cat-usd", "USD", h(0))},
			Tenant:    []pricing.Version{priced(t, "ten-eur", "EUR", h(0))}}, h(1), want{"ten-eur", pricing.SourceTenant}},
		{"a later catalogue version does not displace the tenant's price", pricing.List{Currency: usd,
			Catalogue: []pricing.Version{priced(t, "cat-old", "USD", h(0)), priced(t, "cat-new", "USD", h(5))},
			Tenant:    []pricing.Version{priced(t, "ten", "USD", h(1))}}, h(8), want{"ten", pricing.SourceTenant}},
		{"the tenant's version that starts later takes over", pricing.List{Currency: usd,
			Tenant: []pricing.Version{priced(t, "ten-1", "USD", h(0)), priced(t, "ten-2", "USD", h(4))}}, h(5), want{"ten-2", pricing.SourceTenant}},
		{"before the tenant's first version the catalogue prices", pricing.List{Currency: usd,
			Catalogue: []pricing.Version{priced(t, "cat", "USD", h(0))},
			Tenant:    []pricing.Version{priced(t, "ten", "USD", h(4))}}, h(2), want{"cat", pricing.SourceCatalogue}},
		{"the catalogue's version in the active currency when the list has none", pricing.List{Currency: usd,
			Catalogue: []pricing.Version{priced(t, "cat", "USD", h(0))}}, h(1), want{"cat", pricing.SourceCatalogue}},
		{"an end gives the catalogue price back", pricing.List{Currency: usd,
			Catalogue: []pricing.Version{priced(t, "cat", "USD", h(0))},
			Tenant:    []pricing.Version{priced(t, "ten", "USD", h(0)), ended("end", "USD", h(3))}}, h(4), want{"cat", pricing.SourceCatalogue}},
		{"before the end the tenant's price", pricing.List{Currency: usd,
			Catalogue: []pricing.Version{priced(t, "cat", "USD", h(0))},
			Tenant:    []pricing.Version{priced(t, "ten", "USD", h(0)), ended("end", "USD", h(3))}}, h(2), want{"ten", pricing.SourceTenant}},
		{"an end and no catalogue price in the currency is no price", pricing.List{Currency: eur,
			Catalogue: []pricing.Version{priced(t, "cat", "USD", h(0))},
			Tenant:    []pricing.Version{priced(t, "ten", "EUR", h(0)), ended("end", "EUR", h(3))}}, h(4), want{}},
		{"a price after an end is the tenant's again", pricing.List{Currency: usd,
			Tenant: []pricing.Version{priced(t, "ten-1", "USD", h(0)), ended("end", "USD", h(3)), priced(t, "ten-2", "USD", h(6))}}, h(7), want{"ten-2", pricing.SourceTenant}},
		{"a catalogue version in another currency is no price", pricing.List{Currency: eur,
			Catalogue: []pricing.Version{priced(t, "cat", "USD", h(0))}}, h(1), want{}},
		{"the catalogue's version in force is the one that counts, whatever its currency", pricing.List{Currency: usd,
			Catalogue: []pricing.Version{priced(t, "cat-usd", "USD", h(0)), priced(t, "cat-eur", "EUR", h(2))}}, h(3), want{}},
		{"a list in another currency is ignored", pricing.List{Currency: usd,
			Catalogue: []pricing.Version{priced(t, "cat", "USD", h(0))},
			Tenant:    []pricing.Version{priced(t, "ten-eur", "EUR", h(0))}}, h(1), want{"cat", pricing.SourceCatalogue}},
		{"nothing before any version", pricing.List{Currency: usd,
			Catalogue: []pricing.Version{priced(t, "cat", "USD", h(5))},
			Tenant:    []pricing.Version{priced(t, "ten", "USD", h(5))}}, h(4), want{}},
		{"a pending change: the old currency before its instant", pricing.List{Currency: pricing.Active{Current: "EUR", Previous: "USD", From: h(10)},
			Catalogue: []pricing.Version{priced(t, "cat", "USD", h(0))},
			Tenant:    []pricing.Version{priced(t, "ten-eur", "EUR", h(0))}}, h(9), want{"cat", pricing.SourceCatalogue}},
		{"a pending change: the new currency from its instant", pricing.List{Currency: pricing.Active{Current: "EUR", Previous: "USD", From: h(10)},
			Catalogue: []pricing.Version{priced(t, "cat", "USD", h(0))},
			Tenant:    []pricing.Version{priced(t, "ten-eur", "EUR", h(0))}}, h(10), want{"ten-eur", pricing.SourceTenant}},
		{"the zero Active prices from the catalogue whatever its currency", pricing.List{
			Catalogue: []pricing.Version{priced(t, "cat", "GBP", h(0))}}, h(1), want{"cat", pricing.SourceCatalogue}},
		{"the zero Active ignores the tenant's versions", pricing.List{
			Catalogue: []pricing.Version{priced(t, "cat", "USD", h(0))},
			Tenant:    []pricing.Version{priced(t, "ten", "USD", h(0))}}, h(1), want{"cat", pricing.SourceCatalogue}},
		{"unsorted input", pricing.List{Currency: usd,
			Catalogue: []pricing.Version{priced(t, "cat-2", "USD", h(6)), priced(t, "cat-0", "USD", h(0)), priced(t, "cat-1", "USD", h(3))},
			Tenant:    []pricing.Version{ended("end", "USD", h(5)), priced(t, "ten-2", "USD", h(8)), priced(t, "ten-1", "USD", h(1))}}, h(7), want{"cat-2", pricing.SourceCatalogue}},
	} {
		t.Run(c.name, func(t *testing.T) {
			v, source, found := c.l.At(c.at)
			if c.want.id == "" {
				if found || source != "" {
					t.Errorf("found %s from %q, want no price", v.ID, source)
				}
				return
			}
			if !found || v.ID != c.want.id || source != c.want.source {
				t.Errorf("got %q from %q (found %v), want %q from %q", v.ID, source, found, c.want.id, c.want.source)
			}
		})
	}

	t.Run("Cost sets Source exactly when it sets VersionID", func(t *testing.T) {
		u := family.Usage{InputTokens: 1_000_000, OutputTokens: 500_000}
		l := pricing.List{Currency: eur,
			Catalogue: []pricing.Version{priced(t, "cat", "EUR", h(0))},
			Tenant:    []pricing.Version{priced(t, "ten", "EUR", h(4), rate(t, catInput, 0, "3"), rate(t, catOutput, 0, "4"))}}
		c := l.Cost(u, h(5))
		if !c.Known || c.VersionID != "ten" || c.Source != pricing.SourceTenant || c.Currency != "EUR" || c.Amount.String() != "5" {
			t.Errorf("a tenant price: %+v (amount %s), want 3 + 2 = 5 EUR from the tenant", c, c.Amount)
		}
		c = l.Cost(u, h(1))
		if !c.Known || c.VersionID != "cat" || c.Source != pricing.SourceCatalogue || c.Amount.String() != "2" {
			t.Errorf("a catalogue price: %+v (amount %s), want 1 + 1 = 2 EUR from the catalogue", c, c.Amount)
		}
		c = l.Cost(u, h(-1))
		if c.Known || c.VersionID != "" || c.Source != "" || c.Reason != pricing.ReasonNoPrice {
			t.Errorf("no price: %+v", c)
		}
		// A price whose category has no rate gives an unknown cost that still names its version, and its source.
		onlyInput := pricing.List{Currency: eur, Tenant: []pricing.Version{priced(t, "ten", "EUR", h(0), rate(t, catInput, 0, "3"))}}
		c = onlyInput.Cost(family.Usage{InputTokens: 1_000_000, OutputTokens: 5}, h(1))
		if c.Known || c.VersionID != "ten" || c.Source != pricing.SourceTenant || c.Reason != pricing.ReasonCategoryUnpriced {
			t.Errorf("an unpriced category: %+v", c)
		}
	})
}

// seedOf returns the seed of a property test, from PANDOKOS_TEST_SEED or the clock, and logs it.
func seedOf(tb testing.TB) uint64 {
	tb.Helper()
	seed := uint64(time.Now().UnixNano())
	if s := os.Getenv("PANDOKOS_TEST_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			tb.Fatalf("PANDOKOS_TEST_SEED: %v", err)
		}
		seed = n
	}
	tb.Logf("seed %d (set PANDOKOS_TEST_SEED to reproduce)", seed)
	return seed
}

// TestListProperty is T-727: for random lists, currencies and instants, At equals a naive
// reference, and never returns a version that starts after the instant, an end, or a version in
// another currency than the one in force.
func TestListProperty(t *testing.T) {
	seed := seedOf(t)
	rng := rand.New(rand.NewPCG(seed, 0x5EED))
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	currencies := []string{"USD", "EUR", "GBP"}
	at := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Hour) }

	for iter := 0; iter < 3000; iter++ {
		var l pricing.List
		n := 0
		for i, k := 0, rng.IntN(8); i < k; i++ {
			l.Catalogue = append(l.Catalogue, priced(t, "c"+strconv.Itoa(n), currencies[rng.IntN(3)], at(rng.IntN(100))))
			n++
		}
		for i, k := 0, rng.IntN(10); i < k; i++ {
			cur, from := currencies[rng.IntN(3)], at(rng.IntN(100))
			if rng.IntN(4) == 0 {
				l.Tenant = append(l.Tenant, ended("t"+strconv.Itoa(n), cur, from))
			} else {
				l.Tenant = append(l.Tenant, priced(t, "t"+strconv.Itoa(n), cur, from))
			}
			n++
		}
		switch rng.IntN(3) {
		case 0: // the zero Active
		case 1:
			l.Currency = pricing.Active{Current: currencies[rng.IntN(3)]}
		default:
			l.Currency = pricing.Active{Current: currencies[rng.IntN(3)], Previous: currencies[rng.IntN(3)], From: at(rng.IntN(100))}
		}
		rng.Shuffle(len(l.Catalogue), func(i, j int) { l.Catalogue[i], l.Catalogue[j] = l.Catalogue[j], l.Catalogue[i] })
		rng.Shuffle(len(l.Tenant), func(i, j int) { l.Tenant[i], l.Tenant[j] = l.Tenant[j], l.Tenant[i] })

		for probe := 0; probe < 6; probe++ {
			now := at(rng.IntN(110) - 5)
			v, source, found := l.At(now)
			wantID, wantSource := reference(l, now)
			switch {
			case wantID == "" && found:
				t.Fatalf("seed %d: found %s from %s, the reference has no price\nlist %+v at %v", seed, v.ID, source, l, now)
			case wantID != "" && (!found || v.ID != wantID || source != wantSource):
				t.Fatalf("seed %d: got %q from %q (found %v), the reference says %q from %q\nlist %+v at %v", seed, v.ID, source, found, wantID, wantSource, l, now)
			}
			if !found {
				continue
			}
			if v.ValidFrom.After(now) || v.End {
				t.Fatalf("seed %d: the version %s starts after the instant or is an end", seed, v.ID)
			}
			if l.Currency != (pricing.Active{}) && v.Currency != l.Currency.At(now) {
				t.Fatalf("seed %d: the version %s is in %s, in force is %s", seed, v.ID, v.Currency, l.Currency.At(now))
			}
		}
	}
}

// reference is the rule of section 5.2 written the slow way: scan, keep the best.
func reference(l pricing.List, now time.Time) (id, source string) {
	best := func(vs []pricing.Version, currency string) (pricing.Version, bool) {
		var b pricing.Version
		ok := false
		for _, v := range vs {
			if currency != "" && v.Currency != currency {
				continue
			}
			if v.ValidFrom.After(now) {
				continue
			}
			if !ok || v.ValidFrom.After(b.ValidFrom) {
				b, ok = v, true
			}
		}
		return b, ok
	}
	if l.Currency == (pricing.Active{}) {
		if v, ok := best(l.Catalogue, ""); ok {
			return v.ID, pricing.SourceCatalogue
		}
		return "", ""
	}
	c := l.Currency.Current
	if !l.Currency.From.IsZero() && l.Currency.Previous != "" && now.Before(l.Currency.From) {
		c = l.Currency.Previous
	}
	if v, ok := best(l.Tenant, c); ok && !v.End {
		return v.ID, pricing.SourceTenant
	}
	if v, ok := best(l.Catalogue, ""); ok && v.Currency == c {
		return v.ID, pricing.SourceCatalogue
	}
	return "", ""
}
