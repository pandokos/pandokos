package admin_test

import (
	"sort"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
)

// TestActiveCurrency is T-750: set, replace while pending (the instant stays), withdraw, set again after
// the instant; the instant is limits.NextChange in the zone in force, also with a zone change pending; a
// currency without a list is 400, with a tenant price or a catalogue price it is accepted, a scheduled
// price counts; the default is always accepted; in_force resolves the default; inactive_limits names the
// tenant's cost rules of every subject and the ceilings that apply, not those in the new currency, not
// count rules, not a default replaced by the tenant's ceiling; the audit row; the unchanged value writes
// nothing.
func TestActiveCurrency(t *testing.T) {
	w := newPW(t)
	api := w.api
	prov, all := w.cat.Provider.Name, w.cat.All
	path := "/v1/tenants/" + w.a.ID + "/active-currency"
	seen := seenIDs(t, api)
	super := api.Super(t)
	put := func(key, currency string) *apitest.Response {
		return w.do(t, key, "PUT", path, map[string]any{"currency": currency})
	}
	get := func() adminapi.ActiveCurrency {
		return decodeInto[adminapi.ActiveCurrency](t, w.do(t, w.ka, "GET", path, nil), 200)
	}
	columns := func() string {
		var s string
		if err := super.QueryRow(ctx(t), `SELECT active_currency || '|' || active_currency_previous || '|' || (active_currency_effective_from IS NOT NULL)::text FROM pandokos.tenant WHERE id = $1::uuid`, w.a.ID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	t.Run("the default: in_force resolves it; the tenant resource shows the three members", func(t *testing.T) {
		if c := get(); c.Currency != "" || c.InForce != "USD" || c.EffectiveFrom != nil || len(c.InactiveLimits) != 0 {
			t.Errorf("%+v", c)
		}
		tn := decodeInto[adminapi.Tenant](t, w.do(t, w.plat, "GET", "/v1/tenants/"+w.a.ID, nil), 200)
		if tn.ActiveCurrency != "" || tn.ActiveCurrencyInForce != "USD" || tn.ActiveCurrencyEffectiveFrom != nil {
			t.Errorf("tenant %+v", tn)
		}
	})

	t.Run("the default and an unchanged value write nothing", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			if c := decodeInto[adminapi.ActiveCurrency](t, put(w.ka, ""), 200); c.Currency != "" || c.InForce != "USD" || c.EffectiveFrom != nil {
				t.Errorf("%+v", c)
			}
		}
		if rows := newRows(t, api, seen); len(rows) != 0 {
			t.Errorf("an unchanged value wrote %+v", rows)
		}
	})

	t.Run("a currency without a list is a 400", func(t *testing.T) {
		p := invalidOn(t, "EUR without a list", put(w.ka, "EUR"), "currency")
		if m := messageOf(p, "currency"); m != "no price list in this currency: add prices in it first" {
			t.Errorf("message %q", m)
		}
		invalidOn(t, "a malformed currency", put(w.ka, "eur"), "currency")
		if got := columns(); got != "||false" {
			t.Errorf("columns %s", got)
		}
	})

	t.Run("a scheduled tenant price makes a list", func(t *testing.T) {
		w.createPrice(t, priceBody(all, prov, "EUR", nil, map[string]any{"valid_from": scheduled(3, 0)}))
		newRows(t, api, seen) // the audit row of the version is not the change's
		want := monthEnd(t, "UTC", dbNow(t, api))
		r := put(w.ka, "EUR")
		c := decodeInto[adminapi.ActiveCurrency](t, r, 200)
		if c.Currency != "EUR" || c.InForce != "USD" || c.EffectiveFrom == nil || !c.EffectiveFrom.Equal(want) {
			t.Fatalf("%+v, want EUR from %s with USD in force", c, want)
		}
		if g := get(); g.Currency != "EUR" || g.InForce != "USD" || g.EffectiveFrom == nil || !g.EffectiveFrom.Equal(want) {
			t.Errorf("get %+v", g)
		}
		rows := newRows(t, api, seen)
		if len(rows) != 1 || rows[0].Action != "tenant.active_currency_update" || rows[0].Log != "tenant" || rows[0].Detail["from"] != "" || rows[0].Detail["to"] != "EUR" || rows[0].Detail["in_force"] != "USD" {
			t.Errorf("audit rows %+v", rows)
		}
		tn := decodeInto[adminapi.Tenant](t, w.do(t, w.plat, "GET", "/v1/tenants/"+w.a.ID, nil), 200)
		if tn.ActiveCurrency != "EUR" || tn.ActiveCurrencyInForce != "USD" || tn.ActiveCurrencyEffectiveFrom == nil {
			t.Errorf("the platform sees %+v", tn)
		}
		if again := decodeInto[adminapi.ActiveCurrency](t, put(w.ka, "EUR"), 200); again.EffectiveFrom == nil || !again.EffectiveFrom.Equal(want) {
			t.Errorf("the same value again: %+v", again)
		}
		if rows := newRows(t, api, seen); len(rows) != 0 {
			t.Errorf("the same value again wrote %+v", rows)
		}
	})

	t.Run("a second change keeps the instant, the currency in force withdraws it", func(t *testing.T) {
		before := get()
		w.createPrice(t, priceBody(all, prov, "GBP", nil, map[string]any{"valid_from": scheduled(3, 0)}))
		newRows(t, api, seen) // the audit row of the version is not the change's
		c := decodeInto[adminapi.ActiveCurrency](t, put(w.ka, "GBP"), 200)
		if c.Currency != "GBP" || c.InForce != "USD" || c.EffectiveFrom == nil || !c.EffectiveFrom.Equal(*before.EffectiveFrom) {
			t.Errorf("%+v, want GBP and the instant %v", c, before.EffectiveFrom)
		}
		if rows := newRows(t, api, seen); len(rows) != 1 || rows[0].Detail["from"] != "EUR" || rows[0].Detail["to"] != "GBP" {
			t.Errorf("rows %+v", rows)
		}
		withdrawn := decodeInto[adminapi.ActiveCurrency](t, put(w.ka, ""), 200)
		if withdrawn.Currency != "" || withdrawn.EffectiveFrom != nil || columns() != "||false" {
			t.Errorf("%+v %s", withdrawn, columns())
		}
		if rows := newRows(t, api, seen); len(rows) != 1 {
			t.Errorf("the withdrawal wrote %d rows", len(rows))
		}
	})

	t.Run("after the instant a new change is a new pending one", func(t *testing.T) {
		api.DB.SetTenantCurrency(t, w.idA, "EUR", "", time.Now().Add(-24*time.Hour)) // the change took effect
		if c := get(); c.Currency != "EUR" || c.InForce != "EUR" || c.EffectiveFrom != nil && c.EffectiveFrom.After(time.Now()) {
			t.Errorf("in force after the instant: %+v", c)
		}
		c := decodeInto[adminapi.ActiveCurrency](t, put(w.ka, "GBP"), 200)
		if c.Currency != "GBP" || c.InForce != "EUR" || c.EffectiveFrom == nil || !c.EffectiveFrom.After(time.Now()) {
			t.Errorf("%+v", c)
		}
		api.DB.SetTenantCurrency(t, w.idA, "", "", time.Time{})
		newRows(t, api, seen)
	})

	t.Run("the instant is that of the zone in force, also with a zone change pending", func(t *testing.T) {
		pb := newPW(t)
		path := "/v1/tenants/" + pb.a.ID + "/active-currency"
		pb.createPrice(t, priceBody(pb.cat.All, pb.cat.Provider.Name, "EUR", nil, map[string]any{"valid_from": scheduled(3, 0)}))
		// A zone that took effect: the instant is the end of the month of that zone.
		api := pb.api
		api.DB.SetTenantZone(t, pb.idA, "Asia/Tokyo", "UTC", time.Now().Add(-24*time.Hour))
		c := decodeInto[adminapi.ActiveCurrency](t, pb.do(t, pb.ka, "PUT", path, map[string]any{"currency": "EUR"}), 200)
		if want := monthEnd(t, "Asia/Tokyo", dbNow(t, api)); c.EffectiveFrom == nil || !c.EffectiveFrom.Equal(want) {
			t.Errorf("zone in force Asia/Tokyo: %+v, want %s", c, want)
		}
		// A zone change that is pending takes effect at the same instant.
		pc := newPW(t)
		pc.createPrice(t, priceBody(pc.cat.All, pc.cat.Provider.Name, "EUR", nil, map[string]any{"valid_from": scheduled(3, 0)}))
		z := decodeInto[adminapi.TenantTimeZone](t, pc.do(t, pc.ka, "PUT", "/v1/tenants/"+pc.a.ID+"/time-zone", map[string]any{"time_zone": "Europe/Rome"}), 200)
		c = decodeInto[adminapi.ActiveCurrency](t, pc.do(t, pc.ka, "PUT", "/v1/tenants/"+pc.a.ID+"/active-currency", map[string]any{"currency": "EUR"}), 200)
		if z.EffectiveFrom == nil || c.EffectiveFrom == nil || !c.EffectiveFrom.Equal(*z.EffectiveFrom) {
			t.Errorf("zone %+v currency %+v: both take effect at the same instant", z, c)
		}
	})

	t.Run("a catalogue price in the currency makes a list too", func(t *testing.T) {
		pd := newPW(t)
		path := "/v1/tenants/" + pd.b.ID + "/active-currency"
		invalidOn(t, "no list", pd.do(t, pd.kb, "PUT", path, map[string]any{"currency": "EUR"}), "currency")
		pd.api.DB.SeedPrice(t, pd.cat.Provider.Name, pd.cat.All.Name, "EUR", time.Now().Add(24*time.Hour)) // starts later: counts
		if c := decodeInto[adminapi.ActiveCurrency](t, pd.do(t, pd.kb, "PUT", path, map[string]any{"currency": "EUR"}), 200); c.Currency != "EUR" {
			t.Errorf("%+v", c)
		}
		// A catalogue price for a model the tenant cannot be given does not count.
		invalidOn(t, "a price of a model that is not granted", pd.do(t, pd.kb, "PUT", path, map[string]any{"currency": "CHF"}), "currency")
	})

	t.Run("inactive_limits", func(t *testing.T) {
		pe := newPW(t)
		pe.createPrice(t, priceBody(pe.cat.All, pe.cat.Provider.Name, "EUR", nil, map[string]any{"valid_from": scheduled(3, 0)}))
		d := pe.api.DB
		a := pe.idA
		d.SeedLimitRule(t, a, "tenant", "", "cost_per_month", 1_000, dbtest.LimitOptions{Currency: "EUR"})                 // in the new currency: not listed
		d.SeedLimitRule(t, a, "tenant", "", "cost_per_day", 1_000, dbtest.LimitOptions{Currency: "USD"})                   // listed
		d.SeedLimitRule(t, a, "service_account", pe.saA.ID, "cost_per_month", 1_000, dbtest.LimitOptions{Currency: "USD"}) // listed: any subject
		d.SeedLimitRule(t, a, "tenant", "", "requests_per_day", 10)                                                        // a count rule: not listed
		d.SeedPlatformLimit(t, nil, "cost_per_month", 5_000, dbtest.LimitOptions{Currency: "USD"})                         // a default that applies: listed
		d.SeedPlatformLimit(t, nil, "cost_per_day", 5_000, dbtest.LimitOptions{Currency: "USD"})                           // a default replaced by the tenant's own row: not listed
		d.SeedPlatformLimit(t, &a, "cost_per_day", 4_000, dbtest.LimitOptions{Currency: "USD"})                            // the tenant's own row in USD: listed
		d.SeedPlatformLimit(t, &a, "cost_per_month", 4_000, dbtest.LimitOptions{Currency: "EUR"})                          // in the new currency: not listed
		d.SeedPlatformLimit(t, nil, "tokens_per_day", 5_000)                                                               // a count default: not listed
		c := decodeInto[adminapi.ActiveCurrency](t, pe.do(t, pe.ka, "PUT", "/v1/tenants/"+pe.a.ID+"/active-currency", map[string]any{"currency": "EUR"}), 200)
		type key struct{ level, metric, currency string }
		got := map[key]int{}
		for _, l := range c.InactiveLimits {
			got[key{l.Level, string(l.Metric), l.Currency}]++
			if l.RuleID == "" {
				t.Errorf("a limit without a rule ID: %+v", l)
			}
		}
		want := map[key]int{
			{"tenant", "cost_per_day", "USD"}: 1, {"tenant", "cost_per_month", "USD"}: 1,
			{"platform", "cost_per_month", "USD"}: 1, {"platform", "cost_per_day", "USD"}: 1,
		}
		if len(c.InactiveLimits) != 4 {
			t.Errorf("%d inactive limits: %+v, want 4", len(c.InactiveLimits), c.InactiveLimits)
		}
		for k, n := range want {
			if got[k] != n {
				t.Errorf("inactive limit %v: %d, want %d (%+v)", k, got[k], n, c.InactiveLimits)
			}
		}
		levels := make([]string, len(c.InactiveLimits))
		for i, l := range c.InactiveLimits {
			levels[i] = l.Level
		}
		if !sort.StringsAreSorted(levels) {
			t.Errorf("ordered by level, then rule ID: %v", levels)
		}
		// The audit row counts them.
		rows := auditRows(t, pe.api)
		last := rows[len(rows)-1]
		if last.Action != "tenant.active_currency_update" || last.Detail["inactive_limits"] != float64(4) {
			t.Errorf("audit row %+v", last)
		}
		// The answer to a read does not carry them.
		if g := decodeInto[adminapi.ActiveCurrency](t, pe.do(t, pe.ka, "GET", "/v1/tenants/"+pe.a.ID+"/active-currency", nil), 200); len(g.InactiveLimits) != 0 {
			t.Errorf("a read carries %d inactive limits", len(g.InactiveLimits))
		}
	})
}
