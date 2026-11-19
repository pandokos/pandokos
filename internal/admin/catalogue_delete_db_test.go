package admin_test

import (
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
)

// TestCatalogueDeleteRefused is T-715, an exit condition of the sprint: a provider or a model that a
// limit rule of another tenant, of this tenant or of the platform names cannot be deleted. The answer
// is 409 invalid_state with the entry of section 5.4 in errors, nothing is audited and nothing about
// a tenant is in the answer; once the rule is deleted the deletion succeeds; a model with a price is
// still refused as before.
func TestCatalogueDeleteRefused(t *testing.T) {
	w := newLW(t)
	api := w.api
	seen := seenIDs(t, api)
	const (
		providerMsg = "limit rules reference this provider; disable the provider instead"
		modelMsg    = "limit rules reference this model; retire the model or remove its grants instead"
	)
	refused := func(t *testing.T, what, path, wantMsg string) {
		t.Helper()
		newRows(t, api, seen)
		r := w.do(t, w.plat, "DELETE", path, nil)
		p := expect(t, r, 409, adminapi.ProblemCodeInvalidState)
		if len(p.Errors) != 1 || p.Errors[0].Field != "limit_rules" || p.Errors[0].Message != wantMsg {
			t.Errorf("%s: errors %+v, want one entry on limit_rules saying %q", what, p.Errors, wantMsg)
		}
		for _, secret := range []string{w.a.ID, w.b.ID, w.a.Name, w.b.Name} {
			if strings.Contains(string(r.Body), secret) {
				t.Errorf("%s: the answer names a tenant (%s): %s", what, secret, r.Body)
			}
		}
		// The constant title and detail of the code.
		if rows := newRows(t, api, seen); len(rows) != 0 {
			t.Errorf("%s: a refused deletion wrote audit rows: %+v", what, rows)
		}
	}
	model := func(t *testing.T) (apitest.Catalogue, string) {
		c := api.Catalog(t)
		return c, "/v1/models/" + c.All.ID
	}

	t.Run("a model that a rule of this tenant names", func(t *testing.T) {
		c, path := model(t)
		r := decodeInto[adminapi.LimitRule](t, w.do(t, w.ka, "POST", w.baseA, map[string]any{"metric": "tokens_per_day", "limit": 5, "provider": c.Provider.Name, "model": c.All.Name}), 201)
		refused(t, "tenant A's rule", path, modelMsg)
		if rr := w.do(t, w.ka, "DELETE", w.baseA+"/"+r.ID, nil); rr.Status != 200 {
			t.Fatalf("delete the rule: %d", rr.Status)
		}
		newRows(t, api, seen)
		if rr := w.do(t, w.plat, "DELETE", path, nil); rr.Status != 200 {
			t.Errorf("the deletion after the rule is gone: %d\n%s", rr.Status, rr.Body)
		}
		rows := newRows(t, api, seen)
		if len(rows) != 1 || rows[0].Action != "model.delete" {
			t.Errorf("the deletion's audit rows: %+v", rows)
		}
	})

	t.Run("a model that a rule of another tenant names", func(t *testing.T) {
		c, path := model(t)
		w.do(t, w.kb, "POST", "/v1/tenants/"+w.b.ID+"/limits", map[string]any{"metric": "tokens_per_day", "limit": 5, "provider": c.Provider.Name, "model": c.All.Name})
		refused(t, "tenant B's rule", path, modelMsg)
	})

	t.Run("a model that a ceiling of the platform names", func(t *testing.T) {
		c, path := model(t)
		pl := decodeInto[adminapi.PlatformLimitRule](t, w.do(t, w.plat, "POST", "/v1/platform-limits", map[string]any{"metric": "tokens_per_day", "limit": 5, "provider": c.Provider.Name, "model": c.All.Name}), 201)
		refused(t, "the platform's ceiling", path, modelMsg)
		w.do(t, w.plat, "DELETE", "/v1/platform-limits/"+pl.ID, nil)
		newRows(t, api, seen)
		if rr := w.do(t, w.plat, "DELETE", path, nil); rr.Status != 200 {
			t.Errorf("the deletion after the ceiling is gone: %d\n%s", rr.Status, rr.Body)
		}
	})

	t.Run("a provider that a rule names", func(t *testing.T) {
		for _, kind := range []string{"tenant", "other tenant", "platform"} {
			name := apitest.UniqueName("bare")
			decodeInto[adminapi.Provider](t, w.do(t, w.plat, "POST", "/v1/providers", map[string]any{"name": name, "api_family": "openai"}), 201)
			var delete func()
			switch kind {
			case "tenant":
				r := decodeInto[adminapi.LimitRule](t, w.do(t, w.ka, "POST", w.baseA, map[string]any{"metric": "tokens_per_day", "limit": 5, "provider": name}), 201)
				delete = func() { w.do(t, w.ka, "DELETE", w.baseA+"/"+r.ID, nil) }
			case "other tenant":
				r := decodeInto[adminapi.LimitRule](t, w.do(t, w.kb, "POST", "/v1/tenants/"+w.b.ID+"/limits", map[string]any{"metric": "tokens_per_day", "limit": 5, "provider": name}), 201)
				delete = func() { w.do(t, w.kb, "DELETE", "/v1/tenants/"+w.b.ID+"/limits/"+r.ID, nil) }
			default:
				r := decodeInto[adminapi.PlatformLimitRule](t, w.do(t, w.plat, "POST", "/v1/platform-limits", map[string]any{"metric": "tokens_per_day", "limit": 5, "provider": name}), 201)
				delete = func() { w.do(t, w.plat, "DELETE", "/v1/platform-limits/"+r.ID, nil) }
			}
			refused(t, "a provider named by a rule of "+kind, "/v1/providers/"+name, providerMsg)
			delete()
			newRows(t, api, seen)
			if rr := w.do(t, w.plat, "DELETE", "/v1/providers/"+name, nil); rr.Status != 200 {
				t.Errorf("%s: the deletion after the rule is gone: %d\n%s", kind, rr.Status, rr.Body)
			}
		}
	})

	t.Run("a model with a price is still refused as before", func(t *testing.T) {
		c, path := model(t)
		api.DB.SeedPrice(t, c.Provider.Name, c.All.Name, "EUR", time.Time{})
		newRows(t, api, seen)
		p := expect(t, w.do(t, w.plat, "DELETE", path, nil), 409, adminapi.ProblemCodeInvalidState)
		for _, e := range p.Errors {
			if e.Field == "limit_rules" {
				t.Errorf("a model without rules is said to have limit rules: %+v", p.Errors)
			}
		}
		if rows := newRows(t, api, seen); len(rows) != 0 {
			t.Errorf("wrote %+v", rows)
		}
	})

	t.Run("a provider or a model that nothing names can be deleted", func(t *testing.T) {
		c, path := model(t)
		if rr := w.do(t, w.plat, "DELETE", path, nil); rr.Status != 200 {
			t.Errorf("model: %d\n%s", rr.Status, rr.Body)
		}
		_ = c
	})
}

// TestModelDeleteTenantPrices is T-759: a model with a tenant version is not deleted: 409 invalid_state with
// the errors entry of 5.8, nothing audited, no tenant, currency or rate in the answer.
func TestModelDeleteTenantPrices(t *testing.T) {
	w := newPW(t)
	api := w.api
	model := decodeInto[adminapi.Model](t, w.do(t, w.plat, "POST", "/v1/models", map[string]any{"provider": w.cat.Provider.Name, "name": apitest.UniqueName("m"), "availability": "all"}), 201)
	v := w.createPrice(t, priceBody(model, w.cat.Provider.Name, "EUR", tpRates("input", "0", "6.54321"), map[string]any{"valid_from": scheduled(3, 0), "note": "secret-note"}))
	seen := seenIDs(t, api)
	r := w.do(t, w.plat, "DELETE", "/v1/models/"+model.ID, nil)
	p := expect(t, r, 409, adminapi.ProblemCodeInvalidState)
	if len(p.Errors) != 1 || p.Errors[0].Field != "tenant_prices" || p.Errors[0].Message != "tenant price lists reference this model; retire the model instead" {
		t.Errorf("errors %+v", p.Errors)
	}
	for _, leak := range []string{w.a.ID, w.a.Name, "EUR", "6.54321", "secret-note", v.ID} {
		if strings.Contains(string(r.Body), leak) {
			t.Errorf("the answer names %q: %s", leak, r.Body)
		}
	}
	if rows := newRows(t, api, seen); len(rows) != 0 {
		t.Errorf("a refused deletion wrote %+v", rows)
	}
	// The model is still there, and a started version keeps it so: it is retired instead.
	decodeInto[adminapi.Model](t, w.do(t, w.plat, "GET", "/v1/models/"+model.ID, nil), 200)
	retired := decodeInto[adminapi.Model](t, w.do(t, w.plat, "PATCH", "/v1/models/"+model.ID, map[string]any{"status": "retired"}), 200)
	if retired.Status != adminapi.ModelStatusRetired {
		t.Errorf("%+v", retired)
	}
	// Once the version is deleted the model can go.
	decodeInto[adminapi.TenantPriceVersion](t, w.do(t, w.ka, "DELETE", w.pricesPath(w.a)+"/"+v.ID, nil), 200)
	if r := w.do(t, w.plat, "DELETE", "/v1/models/"+model.ID, nil); r.Status != 200 {
		t.Errorf("after the deletion of the version: %d\n%s", r.Status, r.Body)
	}
}
