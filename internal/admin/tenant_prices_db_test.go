package admin_test

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
)

// TestTenantPriceLists is T-749: creation now and scheduled in two currencies, every field, in_force;
// listPriceLists; the list of versions with each filter, at and paging; reading a replaced version;
// deleting a scheduled one; 409 for a started one; 409 conflict for the same start; the rules of an end;
// each 400 of 5.5 (a model elsewhere, nowhere, not granted and retired answer alike); the bound; two
// concurrent creations without a start both succeed, in order.
func TestTenantPriceLists(t *testing.T) {
	w := newPW(t)
	prov, all := w.cat.Provider.Name, w.cat.All
	path := w.pricesPath(w.a)

	var now adminapi.TenantPriceVersion
	t.Run("a version that starts now, every field", func(t *testing.T) {
		before := dbNow(t, w.api)
		v := w.createPrice(t, priceBody(all, prov, "USD", tpRates("input", "0", "2.50", "output", "0", "10", "input", "100000", "5"), map[string]any{"note": "negotiated"}))
		now = v
		if v.ID == "" || v.ModelID != all.ID || v.Provider != prov || v.Model != all.Name || v.Currency != "USD" || v.Kind != adminapi.TenantPriceKindPrice || v.Note != "negotiated" || !v.InForce || v.CreatedAt.IsZero() {
			t.Errorf("%+v", v)
		}
		if v.ValidFrom.Before(before.Add(-time.Second)) || v.ValidFrom.After(dbNow(t, w.api).Add(time.Second)) {
			t.Errorf("valid_from %v: the database's clock when none is given", v.ValidFrom)
		}
		got := map[string]string{}
		for _, r := range v.Rates {
			got[string(r.Category)+"@"+itoa(int64(r.Threshold))] = r.Rate
		}
		if len(v.Rates) != 3 || got["input@0"] != "2.5" || got["output@0"] != "10" || got["input@100000"] != "5" {
			t.Errorf("rates %+v", v.Rates)
		}
		if again := decodeInto[adminapi.TenantPriceVersion](t, w.do(t, w.ka, "GET", path+"/"+v.ID, nil), 200); again.ID != v.ID || !again.InForce {
			t.Errorf("get: %+v", again)
		}
	})

	var eurSoon, usdLater adminapi.TenantPriceVersion
	t.Run("scheduled in two currencies", func(t *testing.T) {
		eurSoon = w.createPrice(t, priceBody(all, prov, "EUR", nil, map[string]any{"valid_from": scheduled(3, 0)}))
		usdLater = w.createPrice(t, priceBody(all, prov, "USD", tpRates("input", "0", "3"), map[string]any{"valid_from": scheduled(5, 0)}))
		for _, v := range []adminapi.TenantPriceVersion{eurSoon, usdLater} {
			if v.InForce || !v.ValidFrom.After(time.Now()) {
				t.Errorf("a scheduled version: %+v", v)
			}
		}
	})

	t.Run("listPriceLists", func(t *testing.T) {
		l := decodeInto[adminapi.TenantPriceListList](t, w.do(t, w.ka, "GET", "/v1/tenants/"+w.a.ID+"/price-lists", nil), 200)
		by := map[string]adminapi.TenantPriceList{}
		for _, x := range l.Items {
			by[x.Currency] = x
		}
		if len(l.Items) != 2 || by["USD"].Versions != 2 || by["USD"].ModelsPriced != 1 || !by["USD"].Active || by["EUR"].Versions != 1 || by["EUR"].ModelsPriced != 0 || by["EUR"].Active {
			t.Errorf("lists %+v", l.Items)
		}
	})

	t.Run("the list of versions: newest first, filters, at and paging", func(t *testing.T) {
		list := func(q string) adminapi.TenantPriceVersionList {
			return decodeInto[adminapi.TenantPriceVersionList](t, w.do(t, w.ka, "GET", path+q, nil), 200)
		}
		all3 := list("")
		if len(all3.Items) != 3 {
			t.Fatalf("%d versions, want 3", len(all3.Items))
		}
		for i := 1; i < len(all3.Items); i++ {
			if all3.Items[i-1].ID < all3.Items[i].ID {
				t.Errorf("not newest first: %s before %s", all3.Items[i-1].ID, all3.Items[i].ID)
			}
		}
		if got := list("?currency=EUR"); len(got.Items) != 1 || got.Items[0].ID != eurSoon.ID {
			t.Errorf("currency=EUR: %+v", got.Items)
		}
		if got := list("?provider=" + prov); len(got.Items) != 3 {
			t.Errorf("provider: %d", len(got.Items))
		}
		if got := list("?provider=" + prov + "&model=" + all.Name + "&currency=USD"); len(got.Items) != 2 {
			t.Errorf("model: %d", len(got.Items))
		}
		if got := list("?provider=" + prov + "&model=nothing-here"); len(got.Items) != 0 {
			t.Errorf("another model: %d", len(got.Items))
		}
		invalidOn(t, "model without provider", w.do(t, w.ka, "GET", path+"?model="+all.Name, nil), "provider")
		invalidOn(t, "at that is not a time", w.do(t, w.ka, "GET", path+"?at=yesterday", nil), "at")
		at := list("?at=" + scheduled(6, 0))
		ids := map[string]bool{}
		for _, v := range at.Items {
			ids[v.ID] = true
		}
		if len(at.Items) != 2 || !ids[usdLater.ID] || !ids[eurSoon.ID] {
			t.Errorf("at +6h: one version per list and model, the later USD and the EUR: %+v", ids)
		}
		if at := list("?at=" + time.Now().UTC().Format(time.RFC3339Nano)); len(at.Items) != 1 || at.Items[0].ID != now.ID {
			t.Errorf("at now: %+v", at.Items)
		}
		p1 := list("?limit=2")
		if len(p1.Items) != 2 || p1.NextCursor == "" {
			t.Fatalf("page 1: %+v", p1)
		}
		p2 := list("?limit=2&cursor=" + p1.NextCursor)
		if len(p2.Items) != 1 || p2.NextCursor != "" || p2.Items[0].ID == p1.Items[0].ID || p2.Items[0].ID == p1.Items[1].ID {
			t.Errorf("page 2: %+v", p2)
		}
	})

	t.Run("a replaced version is still read", func(t *testing.T) {
		second := w.createPrice(t, priceBody(all, prov, "USD", tpRates("input", "0", "9"), nil))
		if !second.InForce {
			t.Errorf("the new version is not in force: %+v", second)
		}
		old := decodeInto[adminapi.TenantPriceVersion](t, w.do(t, w.ka, "GET", path+"/"+now.ID, nil), 200)
		if old.InForce || old.ID != now.ID {
			t.Errorf("the replaced version: %+v", old)
		}
	})

	t.Run("deletion: a scheduled version, not a started one", func(t *testing.T) {
		gone := decodeInto[adminapi.TenantPriceVersion](t, w.do(t, w.ka, "DELETE", path+"/"+usdLater.ID, nil), 200)
		if gone.ID != usdLater.ID || len(gone.Rates) != 1 {
			t.Errorf("the answer is the version as it was: %+v", gone)
		}
		expect(t, w.do(t, w.ka, "GET", path+"/"+usdLater.ID, nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, w.do(t, w.ka, "DELETE", path+"/"+now.ID, nil), 409, adminapi.ProblemCodeInvalidState)
		expect(t, w.do(t, w.ka, "DELETE", path+"/"+now.ID+"x", nil), 404, adminapi.ProblemCodeNotFound)
	})

	t.Run("the same list, model and start twice is a 409 conflict", func(t *testing.T) {
		start := scheduled(20, 0)
		w.createPrice(t, priceBody(all, prov, "GBP", nil, map[string]any{"valid_from": start}))
		expect(t, w.do(t, w.ka, "POST", path, priceBody(all, prov, "GBP", nil, map[string]any{"valid_from": start})), 409, adminapi.ProblemCodeConflict)
		w.createPrice(t, priceBody(all, prov, "CHF", nil, map[string]any{"valid_from": start})) // another list
	})

	t.Run("an end", func(t *testing.T) {
		// No price in force for the model at the start: 409 on kind.
		p := expect(t, w.do(t, w.ka, "POST", path, map[string]any{"provider": prov, "model": w.granted.Name, "currency": "USD", "kind": "end"}), 409, adminapi.ProblemCodeInvalidState)
		if fieldsOf(p)[0] != "kind" {
			t.Errorf("errors %+v", p.Errors)
		}
		if m := messageOf(p, "kind"); m != "the list has no price in force for this model at that time" {
			t.Errorf("message %q", m)
		}
		invalidOn(t, "an end with rates", w.do(t, w.ka, "POST", path, map[string]any{"provider": prov, "model": all.Name, "currency": "USD", "kind": "end", "rates": tpRates("input", "0", "1")}), "rates")
		end := decodeInto[adminapi.TenantPriceVersion](t, w.do(t, w.ka, "POST", path, map[string]any{"provider": prov, "model": all.Name, "currency": "USD", "kind": "end", "valid_from": scheduled(30, 0)}), 201)
		if end.Kind != adminapi.TenantPriceKindEnd || len(end.Rates) != 0 || end.InForce {
			t.Errorf("the end: %+v", end)
		}
		// An end for a list that was ended before the start of the second end has no price in force any more.
		expect(t, w.do(t, w.ka, "POST", path, map[string]any{"provider": prov, "model": all.Name, "currency": "USD", "kind": "end", "valid_from": scheduled(31, 0)}), 409, adminapi.ProblemCodeInvalidState)
	})

	t.Run("the 400s", func(t *testing.T) {
		gone := map[string]any{"provider": prov, "model": "no-such-model", "currency": "USD", "rates": tpRates("input", "0", "1")}
		var first *string
		for what, body := range map[string]map[string]any{
			"a model that does not exist": gone,
			"a model of another provider": {"provider": "openai", "model": "no-such-model", "currency": "USD", "rates": tpRates("input", "0", "1")},
			"a model that is not granted": priceBody(w.cat.Selected, prov, "USD", nil, nil),
			"a model that is retired":     priceBody(w.retired, prov, "USD", nil, nil),
		} {
			r := w.do(t, w.ka, "POST", path, body)
			p := invalidOn(t, what, r, "model")
			if m := messageOf(p, "model"); m != "must name a model available to this tenant" {
				t.Errorf("%s: %q", what, m)
			}
			norm := normalizeBody(r)
			if first == nil {
				first = &norm
			} else if *first != norm {
				t.Errorf("%s answers differently from the others:\n%s\n%s", what, *first, norm)
			}
		}
		// Model B's granted model is visible to A only.
		if r := w.do(t, w.kb, "POST", w.pricesPath(w.b), priceBody(w.granted, prov, "USD", nil, nil)); r.Status != 400 {
			t.Errorf("tenant B naming A's model: %d", r.Status)
		}
		invalidOn(t, "a price without rates", w.do(t, w.ka, "POST", path, map[string]any{"provider": prov, "model": all.Name, "currency": "USD"}), "rates")
		invalidOn(t, "a rate without a base rate", w.do(t, w.ka, "POST", path, priceBody(all, prov, "USD", tpRates("input", "1000", "1"), nil)), "rates")
		invalidOn(t, "a negative rate", w.do(t, w.ka, "POST", path, priceBody(all, prov, "USD", tpRates("input", "0", "-1"), nil)), "rates[0].rate")
		invalidOn(t, "a start in the past", w.do(t, w.ka, "POST", path, priceBody(all, prov, "USD", nil, map[string]any{"valid_from": scheduled(-2, 0)})), "valid_from")
		invalidOn(t, "a malformed currency", w.do(t, w.ka, "POST", path, priceBody(all, prov, "usd", nil, nil)), "currency")
		invalidOn(t, "an unknown kind", w.do(t, w.ka, "POST", path, priceBody(all, prov, "USD", nil, map[string]any{"kind": "free"})), "kind")
	})

	t.Run("the bound", func(t *testing.T) {
		pb := newPW(t)
		restore := admin.SetMaxTenantPriceVersions(3)
		defer restore()
		for i := 0; i < 3; i++ {
			pb.createPrice(t, priceBody(pb.cat.All, pb.cat.Provider.Name, "USD", nil, map[string]any{"valid_from": scheduled(2+i, 0)}))
		}
		expect(t, pb.do(t, pb.ka, "POST", pb.pricesPath(pb.a), priceBody(pb.cat.All, pb.cat.Provider.Name, "USD", nil, map[string]any{"valid_from": scheduled(10, 0)})), 409, adminapi.ProblemCodeInvalidState)
		// The bound is per tenant: tenant B is not at it.
		decodeInto[adminapi.TenantPriceVersion](t, pb.do(t, pb.kb, "POST", pb.pricesPath(pb.b), priceBody(pb.cat.All, pb.cat.Provider.Name, "USD", nil, nil)), 201)
	})

	t.Run("two concurrent creations without a start both succeed, in order", func(t *testing.T) {
		pc := newPW(t)
		var wg sync.WaitGroup
		out := make([]adminapi.TenantPriceVersion, 2)
		for i := range out {
			wg.Add(1)
			go func() {
				defer wg.Done()
				body := priceBody(pc.cat.All, pc.cat.Provider.Name, "USD", tpRates("input", "0", itoa(int64(i+1))), nil)
				out[i] = decodeInto[adminapi.TenantPriceVersion](t, pc.do(t, pc.ka, "POST", pc.pricesPath(pc.a), body), 201)
			}()
		}
		wg.Wait()
		if out[0].ID == out[1].ID || out[0].ValidFrom.Equal(out[1].ValidFrom) {
			t.Errorf("the two versions: %+v %+v", out[0], out[1])
		}
		l := decodeInto[adminapi.TenantPriceVersionList](t, pc.do(t, pc.ka, "GET", pc.pricesPath(pc.a)+"?currency=USD", nil), 200)
		if len(l.Items) != 2 || !l.Items[0].ValidFrom.After(l.Items[1].ValidFrom) || !l.Items[0].InForce || l.Items[1].InForce {
			t.Errorf("the later start is in force: %+v", l.Items)
		}
	})
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
