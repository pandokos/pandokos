package admin_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
)

// TestBudgetCurrency is T-716: a budget is accepted when a price in force, or one that starts later,
// is in its currency, within the scope of the rule; otherwise it is refused with a 400 on currency
// that lists the currencies the scope has, sorted. A replaced version does not count; the set of a
// tenant leaves out the models it cannot be given; a scope without a priced model has its own message;
// count metrics are not checked.
func TestBudgetCurrency(t *testing.T) {
	t.Parallel()
	w := newLWWith(t, false)
	newModel := func(t *testing.T, provider, availability string) adminapi.Model {
		t.Helper()
		return decodeInto[adminapi.Model](t, w.do(t, w.plat, "POST", "/v1/models", map[string]any{"provider": provider, "name": apitest.UniqueName("m"), "availability": availability}), 201)
	}
	newProvider := func(t *testing.T) string {
		t.Helper()
		name := apitest.UniqueName("prov")
		decodeInto[adminapi.Provider](t, w.do(t, w.plat, "POST", "/v1/providers", map[string]any{"name": name, "api_family": "openai"}), 201)
		return name
	}
	budget := func(provider, model, currency string, extra map[string]any) map[string]any {
		b := map[string]any{"metric": "cost_per_day", "amount": "5", "currency": currency}
		if provider != "" {
			b["provider"] = provider
		}
		if model != "" {
			b["model"] = model
		}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	// refusedWith expects a 400 on currency whose message names exactly the currencies listed.
	refusedWith := func(t *testing.T, what string, r *apitest.Response, currencies string) {
		t.Helper()
		p := expect(t, r, 400, adminapi.ProblemCodeInvalidRequest)
		var msg string
		for _, e := range p.Errors {
			if e.Field == "currency" {
				msg = e.Message
			}
		}
		want := "must be a currency the prices of this scope use: " + currencies
		if currencies == "" {
			want = "no model of this scope has a price"
		}
		if msg != want {
			t.Errorf("%s: errors %+v, want on currency %q", what, p.Errors, want)
		}
	}
	tenantPost := func(body map[string]any) *apitest.Response { return w.do(t, w.ka, "POST", w.baseA, body) }
	platPost := func(body map[string]any) *apitest.Response {
		return w.do(t, w.plat, "POST", "/v1/platform-limits", body)
	}

	t.Run("a model scope", func(t *testing.T) {
		p := newProvider(t)
		m, other := newModel(t, p, "all"), newModel(t, p, "all")
		w.api.DB.SeedPrice(t, p, m.Name, "EUR", time.Time{})
		w.api.DB.SeedPrice(t, p, other.Name, "USD", time.Time{})
		if r := tenantPost(budget(p, m.Name, "EUR", nil)); r.Status != 201 {
			t.Errorf("the currency of the model's price: %d\n%s", r.Status, r.Body)
		}
		refusedWith(t, "another currency", tenantPost(budget(p, m.Name, "USD", nil)), "EUR") // the USD price is another model's
		refusedWith(t, "an unenforced budget is checked too", tenantPost(budget(p, m.Name, "USD", map[string]any{"metric": "cost_per_month", "enforce": false, "thresholds": []int{80}})), "EUR")
		if r := platPost(budget(p, other.Name, "USD", nil)); r.Status != 201 {
			t.Errorf("a platform ceiling in the currency of the model: %d\n%s", r.Status, r.Body)
		}
		refusedWith(t, "a platform ceiling in another", platPost(budget(p, other.Name, "EUR", map[string]any{"metric": "cost_per_month"})), "USD")
	})

	t.Run("a provider scope and the scope of every model", func(t *testing.T) {
		p := newProvider(t)
		a, b := newModel(t, p, "all"), newModel(t, p, "all")
		w.api.DB.SeedPrice(t, p, a.Name, "EUR", time.Time{})
		w.api.DB.SeedPrice(t, p, b.Name, "USD", time.Time{})
		if r := tenantPost(budget(p, "", "USD", map[string]any{"metric": "cost_per_month"})); r.Status != 201 {
			t.Errorf("a provider budget in the currency of one of its models: %d\n%s", r.Status, r.Body)
		}
		refusedWith(t, "a provider budget in a currency of none of its models", tenantPost(budget(p, "", "GBP", nil)), "EUR, USD")
		// Every model: the set grows with the prices of every provider; the currencies are sorted.
		w.api.DB.SeedPrice(t, p, newModel(t, p, "all").Name, "CHF", time.Time{})
		r := tenantPost(budget("", "", "JPY", nil))
		pr := expect(t, r, 400, adminapi.ProblemCodeInvalidRequest)
		var msg string
		for _, e := range pr.Errors {
			if e.Field == "currency" {
				msg = e.Message
			}
		}
		if !strings.HasPrefix(msg, "must be a currency the prices of this scope use: CHF, EUR, USD") {
			t.Errorf("every model: %q, want the sorted currencies of every model", msg)
		}
		if r := tenantPost(budget("", "", "CHF", map[string]any{"metric": "cost_per_month"})); r.Status != 201 {
			t.Errorf("every model, a currency of one of them: %d\n%s", r.Status, r.Body)
		}
	})

	t.Run("a replaced version does not count and a later one does", func(t *testing.T) {
		p := newProvider(t)
		m := newModel(t, p, "all")
		w.api.DB.SeedPrice(t, p, m.Name, "EUR", time.Time{})
		time.Sleep(20 * time.Millisecond)
		w.api.DB.SeedPrice(t, p, m.Name, "USD", time.Time{}) // in force now: the EUR version is replaced
		refusedWith(t, "a currency of a replaced version", tenantPost(budget(p, m.Name, "EUR", nil)), "USD")
		if r := tenantPost(budget(p, m.Name, "USD", nil)); r.Status != 201 {
			t.Errorf("the version in force: %d\n%s", r.Status, r.Body)
		}
		w.api.DB.SeedPrice(t, p, m.Name, "GBP", time.Now().Add(10*24*time.Hour)) // starts later: it can be in force
		if r := tenantPost(budget(p, m.Name, "GBP", map[string]any{"metric": "cost_per_month"})); r.Status != 201 {
			t.Errorf("a version that starts later: %d\n%s", r.Status, r.Body)
		}
		refusedWith(t, "the replaced one, with a later one in the set", tenantPost(budget(p, m.Name, "EUR", map[string]any{"metric": "cost_per_month"})), "GBP, USD")
	})

	t.Run("the set of a tenant leaves out the models it cannot be given", func(t *testing.T) {
		p := newProvider(t)
		open, closed := newModel(t, p, "all"), newModel(t, p, "selected")
		w.api.DB.SeedPrice(t, p, open.Name, "EUR", time.Time{})
		w.api.DB.SeedPrice(t, p, closed.Name, "JPY", time.Time{})
		refusedWith(t, "a currency of a model the tenant cannot be given", tenantPost(budget(p, "", "JPY", nil)), "EUR")
		// The model itself: not visible, so the answer is the one of a model that does not exist (no oracle),
		// whatever the currency; the visibility check comes before the currency check.
		missing := tenantPost(budget(p, apitest.UniqueName("nomodel"), "JPY", nil))
		hidden := tenantPost(budget(p, closed.Name, "JPY", nil))
		for what, r := range map[string]*apitest.Response{"a model that does not exist": missing, "a model not granted": hidden} {
			pr := expect(t, r, 400, adminapi.ProblemCodeInvalidRequest)
			if len(pr.Errors) != 1 || pr.Errors[0].Field != "model" || pr.Errors[0].Message != "must name a model available to this tenant" {
				t.Errorf("%s: errors %+v, want the one on model", what, pr.Errors)
			}
		}
		if missing.Status != hidden.Status {
			t.Errorf("a missing model and a non-granted one differ: %d vs %d", missing.Status, hidden.Status)
		}
		// The platform sees every model.
		if r := platPost(budget(p, "", "JPY", nil)); r.Status != 201 {
			t.Errorf("the platform in the currency of a selected model: %d\n%s", r.Status, r.Body)
		}
		// Granted to the tenant, the model is in its set.
		if r := w.do(t, w.plat, "PUT", "/v1/models/"+closed.ID+"/grants/"+w.a.ID, map[string]any{}); r.Status != 200 {
			t.Fatalf("grant: %d\n%s", r.Status, r.Body)
		}
		if r := tenantPost(budget(p, "", "JPY", nil)); r.Status != 201 {
			t.Errorf("after the grant: %d\n%s", r.Status, r.Body)
		}
	})

	t.Run("a scope without a priced model has its own message; count metrics are not checked", func(t *testing.T) {
		p := newProvider(t)
		m := newModel(t, p, "all")
		refusedWith(t, "a provider with a model and no price", tenantPost(budget(p, "", "EUR", nil)), "")
		refusedWith(t, "a model without a price", tenantPost(budget(p, m.Name, "EUR", nil)), "")
		refusedWith(t, "a platform ceiling on it", platPost(budget(p, m.Name, "EUR", nil)), "")
		if r := tenantPost(map[string]any{"metric": "tokens_per_day", "limit": 5, "provider": p, "model": m.Name}); r.Status != 201 {
			t.Errorf("a count metric on a scope without prices: %d\n%s", r.Status, r.Body)
		}
		if r := platPost(map[string]any{"metric": "requests_per_day", "limit": 5, "provider": p}); r.Status != 201 {
			t.Errorf("a platform count metric: %d\n%s", r.Status, r.Body)
		}
	})

	t.Run("an update checks the currency of the rule it changes", func(t *testing.T) {
		p := newProvider(t)
		m := newModel(t, p, "all")
		later := time.Now().Add(7 * 24 * time.Hour)
		version := w.api.DB.SeedPrice(t, p, m.Name, "EUR", later)
		rule := decodeInto[adminapi.LimitRule](t, tenantPost(budget(p, m.Name, "EUR", nil)), 201)
		pl := decodeInto[adminapi.PlatformLimitRule](t, platPost(budget(p, m.Name, "EUR", map[string]any{"metric": "cost_per_month"})), 201)
		if r := w.do(t, w.ka, "PATCH", w.baseA+"/"+rule.ID, map[string]any{"amount": "6"}); r.Status != 200 {
			t.Fatalf("an update while the price is there: %d\n%s", r.Status, r.Body)
		}
		// The only price that could be in force is removed.
		if r := w.do(t, w.plat, "DELETE", fmt.Sprintf("/v1/models/%s/prices/%s", m.ID, version), nil); r.Status != 200 {
			t.Fatalf("delete of a version that has not started: %d\n%s", r.Status, r.Body)
		}
		refusedWith(t, "an update of a budget whose currency has no price", w.do(t, w.ka, "PATCH", w.baseA+"/"+rule.ID, map[string]any{"amount": "7"}), "")
		refusedWith(t, "a platform update", w.do(t, w.plat, "PATCH", "/v1/platform-limits/"+pl.ID, map[string]any{"amount": "7"}), "")
		// A count rule's update is not checked.
		cnt := decodeInto[adminapi.LimitRule](t, tenantPost(map[string]any{"metric": "tokens_per_month", "limit": 5, "provider": p, "model": m.Name}), 201)
		if r := w.do(t, w.ka, "PATCH", w.baseA+"/"+cnt.ID, map[string]any{"limit": 6}); r.Status != 200 {
			t.Errorf("an update of a count rule: %d\n%s", r.Status, r.Body)
		}
	})
}

// TestBudgetCurrencyLists is T-754: the table of 5.7, for the scopes model, provider and all: EUR is
// accepted for a tenant's rule once the tenant has a EUR price in force or scheduled for the scope,
// refused before with the sorted currencies; the catalogue's currencies are still accepted; another
// tenant's check is unchanged; a ceiling for a tenant is accepted in its active and its pending currency
// and refused in a currency in which the tenant only has an unused list; a default ceiling is as in 5.3.
func TestBudgetCurrencyLists(t *testing.T) {
	t.Parallel()
	w := newPW(t)
	prov := w.cat.Provider.Name
	model := w.cat.All
	other := decodeInto[adminapi.Model](t, w.do(t, w.plat, "POST", "/v1/models", map[string]any{"provider": prov, "name": apitest.UniqueName("m"), "availability": "all"}), 201)
	w.api.DB.SeedPrice(t, prov, model.Name, "USD", time.Time{})
	rulesA, rulesB := "/v1/tenants/"+w.a.ID+"/limits", "/v1/tenants/"+w.b.ID+"/limits"
	budget := func(provider, model, currency string) map[string]any {
		b := map[string]any{"metric": "cost_per_month", "amount": "5", "currency": currency}
		if provider != "" {
			b["provider"] = provider
		}
		if model != "" {
			b["model"] = model
		}
		return b
	}
	currencies := func(t *testing.T, r *apitest.Response) string {
		t.Helper()
		p := expect(t, r, 400, adminapi.ProblemCodeInvalidRequest)
		return messageOf(p, "currency")
	}
	const prefix = "must be a currency the prices of this scope use: "

	t.Run("a tenant's rule: EUR is refused until the tenant has a EUR price for the scope", func(t *testing.T) {
		for what, b := range map[string]map[string]any{
			"model": budget(prov, model.Name, "EUR"), "provider": budget(prov, "", "EUR"), "all": budget("", "", "EUR"),
		} {
			if m := currencies(t, w.do(t, w.ka, "POST", rulesA, b)); !strings.HasPrefix(m, prefix+"USD") {
				t.Errorf("%s: %q, want the sorted currencies of the catalogue's prices (USD)", what, m)
			}
		}
		w.createPrice(t, priceBody(model, prov, "EUR", nil, map[string]any{"valid_from": scheduled(2, 0)})) // scheduled counts
		for what, b := range map[string]map[string]any{
			"model": budget(prov, model.Name, "EUR"), "provider": budget(prov, "", "EUR"), "all": budget("", "", "EUR"),
		} {
			b["metric"] = "cost_per_day"
			if r := w.do(t, w.ka, "POST", rulesA, b); r.Status != 201 {
				t.Errorf("%s: a currency of the tenant's own price: %d\n%s", what, r.Status, r.Body)
			}
		}
		// The scope of the price matters: another model of the provider has no EUR price (it has a USD one).
		w.api.DB.SeedPrice(t, prov, other.Name, "USD", time.Time{})
		if m := currencies(t, w.do(t, w.ka, "POST", rulesA, budget(prov, other.Name, "EUR"))); !strings.HasPrefix(m, prefix) {
			t.Errorf("another model: %q", m)
		}
		// The catalogue's currencies are still accepted.
		if r := w.do(t, w.ka, "POST", rulesA, budget(prov, model.Name, "USD")); r.Status != 201 {
			t.Errorf("the catalogue's currency: %d\n%s", r.Status, r.Body)
		}
		// Another tenant's check is unchanged by A's list.
		if m := currencies(t, w.do(t, w.kb, "POST", rulesB, budget(prov, model.Name, "EUR"))); !strings.HasPrefix(m, prefix+"USD") {
			t.Errorf("tenant B: %q", m)
		}
		// The currencies in the message are those of both lists, sorted.
		w.createPrice(t, priceBody(model, prov, "CHF", nil, nil))
		if m := currencies(t, w.do(t, w.ka, "POST", rulesA, budget(prov, model.Name, "GBP"))); !strings.HasPrefix(m, prefix+"CHF, EUR, USD") {
			t.Errorf("%q, want CHF, EUR, USD", m)
		}
	})

	t.Run("a ceiling for one tenant: the catalogue's set plus its active and pending currency", func(t *testing.T) {
		pb := newPW(t)
		post := func(currency string) *apitest.Response {
			return pb.do(t, pb.plat, "POST", "/v1/platform-limits", map[string]any{"tenant": pb.a.ID, "metric": "cost_per_month", "amount": "9", "currency": currency, "provider": pb.cat.Provider.Name})
		}
		// The tenant has a list in CHF that it does not use, and is in EUR with GBP pending.
		pb.createPrice(t, priceBody(pb.cat.All, pb.cat.Provider.Name, "CHF", nil, nil))
		pb.api.DB.SetTenantCurrency(t, pb.idA, "GBP", "EUR", time.Now().Add(48*time.Hour))
		for _, cur := range []string{"EUR", "GBP"} {
			if r := post(cur); r.Status != 201 {
				t.Errorf("the tenant's %s: %d\n%s", cur, r.Status, r.Body)
			}
		}
		if m := currencies(t, post("CHF")); !strings.HasPrefix(m, prefix) || strings.Contains(m, "CHF") {
			t.Errorf("a list the tenant does not use: %q: the platform does not read the tenant's lists", m)
		}
		// A default ceiling: the catalogue's set, as in 5.3.
		pb.api.DB.SeedPrice(t, pb.cat.Provider.Name, pb.cat.All.Name, "USD", time.Time{})
		if r := pb.do(t, pb.plat, "POST", "/v1/platform-limits", map[string]any{"metric": "cost_per_month", "amount": "9", "currency": "USD", "provider": pb.cat.Provider.Name}); r.Status != 201 {
			t.Errorf("a default ceiling in the catalogue's currency: %d\n%s", r.Status, r.Body)
		}
		if r := pb.do(t, pb.plat, "POST", "/v1/platform-limits", map[string]any{"metric": "cost_per_month", "amount": "9", "currency": "EUR", "provider": pb.cat.Provider.Name}); r.Status != 400 {
			t.Errorf("a default ceiling in a currency of no catalogue price: %d", r.Status)
		}
	})
}
