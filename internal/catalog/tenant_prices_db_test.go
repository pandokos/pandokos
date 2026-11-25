package catalog_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// byID indexes the versions of a model by ID.
func byID(vs []pricing.Version) map[string]pricing.Version {
	m := map[string]pricing.Version{}
	for _, v := range vs {
		m[v.ID] = v
	}
	return m
}

// TestLoadTenantPrices is T-732: as the gateway role, only the lists of the currency in force and of the
// pending one; per model the version in force and the scheduled ones; a version replaced more than an
// hour ago is not loaded; rates in category order; End; the default currency resolved; another
// tenant's never; a new version, a deletion and a currency change reach a running cache within 2 s.
func TestLoadTenantPrices(t *testing.T) {
	t.Parallel()
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		w := newWorld(t, d, d)
		now := time.Now()
		model := modelID(t, w.env, "synthetic-resolve")
		other := modelID(t, w.env, "synthetic-cached")

		t.Run("the default currency is resolved and nothing is loaded", func(t *testing.T) {
			for _, def := range []string{"USD", "CHF"} {
				ta, err := catalog.LoadTenantAccess(ctx(t), w.gateway, w.a, def)
				if err != nil || ta.Currency != (pricing.Active{Current: def}) || len(ta.Prices) != 0 {
					t.Errorf("default %s: %+v (%v)", def, ta, err)
				}
			}
		})

		// Tenant A: lists in USD, EUR and GBP; B has its own EUR price on the same model.
		oldUSD := d.SeedTenantPriceStarted(t, w.a, "USD", "synthetic", "synthetic-resolve", 5*time.Hour)
		curUSD := d.SeedTenantPriceStarted(t, w.a, "USD", "synthetic", "synthetic-resolve", 2*time.Hour) // replaced the old one more than an hour ago (the load keeps the version in force an hour ago)
		laterUSD := d.SeedTenantPrice(t, w.a, "USD", "synthetic", "synthetic-resolve", now.Add(3*time.Hour))
		gbp := d.SeedTenantPrice(t, w.a, "GBP", "synthetic", "synthetic-resolve", time.Time{})
		eur := d.SeedTenantPrice(t, w.a, "EUR", "synthetic", "synthetic-resolve", time.Time{},
			dbtest.Rate{Category: "output", Rate: "10"}, dbtest.Rate{Category: "cache_read", Rate: "0.3"}, dbtest.Rate{Category: "input", Threshold: 100000, Rate: "4"},
			dbtest.Rate{Category: "input", Rate: "3"}, dbtest.Rate{Category: "output", Threshold: 100000, Rate: "12"})
		endEUR := d.SeedTenantPriceEnd(t, w.a, "EUR", "synthetic", "synthetic-resolve", now.Add(4*time.Hour))
		otherModelUSD := d.SeedTenantPrice(t, w.a, "USD", "synthetic", "synthetic-cached", time.Time{})
		bPrice := d.SeedTenantPrice(t, w.b, "USD", "synthetic", "synthetic-resolve", time.Time{})
		_ = other

		t.Run("only the list of the currency in force; the version in force and the later ones", func(t *testing.T) {
			ta, err := catalog.LoadTenantAccess(ctx(t), w.gateway, w.a, "USD")
			if err != nil {
				t.Fatal(err)
			}
			got := byID(ta.Prices[model])
			if len(got) != 2 || got[curUSD].ID == "" || got[laterUSD].ID == "" {
				t.Errorf("the versions of the model: %v, want the one in force and the scheduled one of USD", ids(ta.Prices[model]))
			}
			if _, ok := got[oldUSD]; ok {
				t.Error("a version replaced more than an hour ago is loaded")
			}
			for _, v := range ta.Prices[model] {
				if v.Currency != "USD" || v.Source != pricing.SourceTenant || v.ModelID != model {
					t.Errorf("version %+v: want a USD version of the model with the source tenant", v)
				}
			}
			if _, ok := got[gbp]; ok {
				t.Error("a list in GBP was loaded")
			}
			if _, ok := got[eur]; ok {
				t.Error("a list in EUR was loaded for a tenant in USD")
			}
			if ps := ta.Prices[modelID(t, w.env, "synthetic-cached")]; len(ps) != 1 || ps[0].ID != otherModelUSD {
				t.Errorf("the other model: %v", ids(ps))
			}
			if _, ok := byID(ta.Prices[model])[bPrice]; ok {
				t.Error("the version of another tenant was loaded")
			}
		})

		t.Run("a pending change loads both lists, a change in force one", func(t *testing.T) {
			d.SetTenantCurrency(t, w.a, "EUR", "USD", now.Add(24*time.Hour))
			ta, err := catalog.LoadTenantAccess(ctx(t), w.gateway, w.a, "USD")
			if err != nil {
				t.Fatal(err)
			}
			if ta.Currency.Current != "EUR" || ta.Currency.Previous != "USD" || ta.Currency.From.Sub(now.Add(24*time.Hour)).Abs() > time.Millisecond {
				t.Errorf("currency %+v", ta.Currency)
			}
			got := byID(ta.Prices[model])
			for _, id := range []string{curUSD, laterUSD, eur, endEUR} {
				if got[id].ID == "" {
					t.Errorf("version %s of the lists in force and pending is missing: %v", id, ids(ta.Prices[model]))
				}
			}
			if _, ok := got[gbp]; ok {
				t.Error("GBP loaded")
			}
			d.SetTenantCurrency(t, w.a, "EUR", "", time.Time{})
			ta, err = catalog.LoadTenantAccess(ctx(t), w.gateway, w.a, "USD")
			if err != nil || ta.Currency != (pricing.Active{Current: "EUR"}) {
				t.Fatalf("%+v %v", ta, err)
			}
			if got = byID(ta.Prices[model]); len(got) != 2 || got[eur].ID == "" || got[endEUR].ID == "" {
				t.Errorf("the list of EUR: %v", ids(ta.Prices[model]))
			}
		})

		t.Run("rates in category order, and an end", func(t *testing.T) {
			ta, err := catalog.LoadTenantAccess(ctx(t), w.gateway, w.a, "USD") // the currency of the tenant row is EUR now
			if err != nil {
				t.Fatal(err)
			}
			got := byID(ta.Prices[model])
			var order []string
			for _, r := range got[eur].Rates {
				order = append(order, string(r.Category)+"@"+itoa(r.Threshold))
			}
			if want := "input@0 input@100000 output@0 output@100000 cache_read@0"; strings.Join(order, " ") != want {
				t.Errorf("rates %v, want %s", order, want)
			}
			if v := got[eur]; v.End || len(v.Rates) == 0 || v.Rates[0].PerMillion.String() != "3" {
				t.Errorf("the price: %+v", v)
			}
			if v := got[endEUR]; !v.End || len(v.Rates) != 0 || v.Currency != "EUR" {
				t.Errorf("the end: %+v", v)
			}
		})

		t.Run("another tenant's rows never", func(t *testing.T) {
			tb, err := catalog.LoadTenantAccess(ctx(t), w.gateway, w.b, "USD")
			if err != nil {
				t.Fatal(err)
			}
			if got := tb.Prices[model]; len(got) != 1 || got[0].ID != bPrice {
				t.Errorf("tenant B: %v", ids(got))
			}
			if len(tb.Prices) != 1 {
				t.Errorf("tenant B has the prices of %d models", len(tb.Prices))
			}
		})
	})

	t.Run("a change reaches a running cache within 2 s", func(t *testing.T) {
		d := dbtest.NewMigrated(t)
		w := newWorld(t, d, d)
		model := modelID(t, w.env, "synthetic-resolve")
		logs := &testutil.LogBuffer{}
		cache, err := catalog.NewCache(catalog.CacheOptions{DB: w.gateway, Logger: telemetry.NewLogger(logs, "debug"), TTL: 5 * time.Minute, DefaultCurrency: "USD"})
		if err != nil {
			t.Fatal(err)
		}
		l, err := store.NewListener(store.ListenerOptions{Conn: d.ConnOptions("gateway"), Role: store.RoleGateway, Logger: telemetry.NewLogger(logs, "debug"), OnNotify: cache.Invalidate, OnReset: cache.Flush})
		if err != nil {
			t.Fatal(err)
		}
		lctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { l.Run(lctx); close(done) }()
		t.Cleanup(func() { cancel(); <-done })
		testutil.Eventually(t, 10*time.Second, "the cache listener to connect", func() bool {
			return strings.Contains(logs.String(), `"msg":"cache listener connected"`)
		})
		access := func(id tenant.ID) catalog.TenantAccess {
			ta, err := cache.Access(ctx(t), id)
			if err != nil {
				t.Fatal(err)
			}
			return ta
		}
		eventually := func(what string, cond func() bool) {
			t.Helper()
			testutil.Eventually(t, 2*time.Second, what, cond)
		}
		if ta := access(w.a); len(ta.Prices) != 0 || ta.Currency.Current != "USD" {
			t.Fatalf("the first load: %+v", ta)
		}
		access(w.b)

		v := d.SeedTenantPrice(t, w.a, "USD", "synthetic", "synthetic-resolve", time.Time{})
		eventually("a new version to reach the cache", func() bool { return len(access(w.a).Prices[model]) == 1 })
		if len(access(w.b).Prices) != 0 {
			t.Error("A's version reached B's entry")
		}
		d.SetTenantCurrency(t, w.a, "EUR", "USD", time.Now().Add(48*time.Hour))
		eventually("a change of currency to reach the cache", func() bool { return access(w.a).Currency.Current == "EUR" })
		eur := d.SeedTenantPrice(t, w.a, "EUR", "synthetic", "synthetic-resolve", time.Now().Add(2*time.Hour))
		eventually("a version of the pending list to reach the cache", func() bool { return byID(access(w.a).Prices[model])[eur].ID != "" })
		// A deletion: only a version that has not started can be deleted.
		w.sql(`DELETE FROM pandokos.tenant_price_version WHERE id = $1::uuid`, eur)
		eventually("a deletion to reach the cache", func() bool { return byID(access(w.a).Prices[model])[eur].ID == "" })
		_ = v
	})
}

func ids(vs []pricing.Version) []string {
	var out []string
	for _, v := range vs {
		out = append(out, v.ID)
	}
	return out
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
