package admin_test

import (
	"net/url"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
)

// TestModelVisibility is T-401, exit condition 1 through the API: a model is visible to one
// tenant and not to another.
func TestModelVisibility(t *testing.T) {
	c := newCat(t)
	p := c.createProvider(t, nil)
	c.createEndpoint(t, p.Name, nil)
	sel := c.createModel(t, p.Name, map[string]any{"name": "synthetic-selected"}) // selected: granted to nobody
	all := c.createModel(t, p.Name, map[string]any{"name": "synthetic-all", "availability": "all"})

	seesA := func() map[string]adminapi.TenantModel { return c.visible(t, c.a.ID, c.ka) }
	seesB := func() map[string]adminapi.TenantModel { return c.visible(t, c.b.ID, c.kb) }

	t.Run("a model that is selected is visible to nobody until it is granted", func(t *testing.T) {
		if a, b := seesA(), seesB(); a[sel.Ref].Model.ID != "" || b[sel.Ref].Model.ID != "" {
			t.Errorf("a selected model without a grant is visible: A %v, B %v", a[sel.Ref].Model.ID, b[sel.Ref].Model.ID)
		}
		for name, v := range map[string]map[string]adminapi.TenantModel{"A": seesA(), "B": seesB()} {
			m, ok := v[all.Ref]
			if !ok || !m.Allowed || m.Model.ID != all.ID || m.Price != nil {
				t.Errorf("%s: a model available to all: %+v %v", name, m, ok)
			}
			// The seed is available to every tenant.
			if v["openai/gpt-4o"].Model.ID == "" || v["anthropic/claude-sonnet-4-6"].Model.ID == "" {
				t.Errorf("%s does not see the seeded models", name)
			}
		}
	})

	t.Run("the grant", func(t *testing.T) {
		g := c.grant(t, sel.ID, c.a.ID)
		if g.ModelID != sel.ID || g.TenantID != c.a.ID || g.TenantName != c.a.Name || g.CreatedAt.IsZero() {
			t.Errorf("the grant: %+v", g)
		}
		// Idempotent: the same grant, the same time, one row.
		again := c.grant(t, sel.ID, c.a.ID)
		if again != g {
			t.Errorf("a second grant: %+v, want %+v", again, g)
		}
		grants := decode[adminapi.ModelGrantList](t, c.platform(t, "GET", "/v1/models/"+sel.ID+"/grants", nil), 200)
		if len(grants.Items) != 1 || grants.Items[0] != g {
			t.Errorf("the grants of the model: %+v", grants.Items)
		}
		a, b := seesA(), seesB()
		if m, ok := a[sel.Ref]; !ok || !m.Allowed || m.Model.Availability != adminapi.ModelAvailabilitySelected {
			t.Errorf("A does not see its model: %+v", m)
		}
		if _, ok := b[sel.Ref]; ok {
			t.Error("B sees a model that was granted to A only")
		}
		// A second grant, to B, and the pagination of the grants.
		c.grant(t, sel.ID, c.b.ID)
		page := decode[adminapi.ModelGrantList](t, c.platform(t, "GET", "/v1/models/"+sel.ID+"/grants?limit=1", nil), 200)
		if len(page.Items) != 1 || page.NextCursor == "" {
			t.Fatalf("the first page of grants: %+v", page)
		}
		rest := decode[adminapi.ModelGrantList](t, c.platform(t, "GET", "/v1/models/"+sel.ID+"/grants?limit=1&cursor="+url.QueryEscape(page.NextCursor), nil), 200)
		if len(rest.Items) != 1 || rest.Items[0].TenantID == page.Items[0].TenantID {
			t.Errorf("the second page of grants: %+v", rest)
		}
		if _, ok := seesB()[sel.Ref]; !ok {
			t.Error("B does not see the model granted to it")
		}

		// Revoking returns the grant as it was; a second revocation is not_found; the tenant loses the model.
		rv := decode[adminapi.ModelGrant](t, c.platform(t, "DELETE", "/v1/models/"+sel.ID+"/grants/"+c.b.ID, nil), 200)
		if rv.TenantID != c.b.ID || rv.TenantName != c.b.Name || rv.ModelID != sel.ID {
			t.Errorf("the revoked grant: %+v", rv)
		}
		expect(t, c.platform(t, "DELETE", "/v1/models/"+sel.ID+"/grants/"+c.b.ID, nil), 404, adminapi.ProblemCodeNotFound)
		if _, ok := seesB()[sel.Ref]; ok {
			t.Error("B still sees a model whose grant was revoked")
		}
		if _, ok := seesA()[sel.Ref]; !ok {
			t.Error("A lost its model because B's grant was revoked")
		}
		// Unknown model or tenant.
		expect(t, c.platform(t, "PUT", "/v1/models/0192f0f0-0000-7000-8000-00000000beef/grants/"+c.a.ID, nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, c.platform(t, "PUT", "/v1/models/"+sel.ID+"/grants/0192f0f0-0000-7000-8000-00000000beef", nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, c.platform(t, "DELETE", "/v1/models/"+sel.ID+"/grants/0192f0f0-0000-7000-8000-00000000beef", nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, c.platform(t, "GET", "/v1/models/0192f0f0-0000-7000-8000-00000000beef/grants", nil), 404, adminapi.ProblemCodeNotFound)
	})

	t.Run("availability all shows it to everybody, selected hides it again, grants are kept", func(t *testing.T) {
		decode[adminapi.Model](t, c.platform(t, "PATCH", "/v1/models/"+sel.ID, map[string]any{"availability": "all"}), 200)
		if _, ok := seesB()[sel.Ref]; !ok {
			t.Error("a model made available to all is not visible to B")
		}
		decode[adminapi.Model](t, c.platform(t, "PATCH", "/v1/models/"+sel.ID, map[string]any{"availability": "selected"}), 200)
		if _, ok := seesB()[sel.Ref]; ok {
			t.Error("B still sees the model after it went back to selected")
		}
		if _, ok := seesA()[sel.Ref]; !ok {
			t.Error("A lost the model: its grant must have been kept")
		}
		grants := decode[adminapi.ModelGrantList](t, c.platform(t, "GET", "/v1/models/"+sel.ID+"/grants", nil), 200)
		if len(grants.Items) != 1 || grants.Items[0].TenantID != c.a.ID {
			t.Errorf("the grants after the round trip: %+v", grants.Items)
		}
	})

	t.Run("a retired model and a disabled provider disappear", func(t *testing.T) {
		decode[adminapi.Model](t, c.platform(t, "PATCH", "/v1/models/"+all.ID, map[string]any{"status": "retired"}), 200)
		if _, ok := seesA()[all.Ref]; ok {
			t.Error("a retired model is still listed")
		}
		decode[adminapi.Model](t, c.platform(t, "PATCH", "/v1/models/"+all.ID, map[string]any{"status": "deprecated"}), 200)
		if m, ok := seesA()[all.Ref]; !ok || m.Model.Status != adminapi.ModelStatusDeprecated {
			t.Errorf("a deprecated model is listed with its status: %+v %v", m, ok)
		}
		decode[adminapi.Provider](t, c.platform(t, "PATCH", "/v1/providers/"+p.Name, map[string]any{"enabled": false}), 200)
		a := seesA()
		if _, ok := a[all.Ref]; ok {
			t.Error("a model of a disabled provider is listed")
		}
		if _, ok := a[sel.Ref]; ok {
			t.Error("a granted model of a disabled provider is listed")
		}
		if a["openai/gpt-4o"].Model.ID == "" {
			t.Error("disabling a provider hid the models of another")
		}
		decode[adminapi.Provider](t, c.platform(t, "PATCH", "/v1/providers/"+p.Name, map[string]any{"enabled": true}), 200)
		if _, ok := seesA()[sel.Ref]; !ok {
			t.Error("the granted model did not come back with its provider")
		}
	})

	t.Run("the list: filter by provider, pages, and the price in force", func(t *testing.T) {
		only := decode[adminapi.TenantModelList](t, c.do(t, "GET", "/v1/tenants/"+c.a.ID+"/models?provider="+p.Name, c.ka, nil), 200)
		if len(only.Items) != 2 {
			t.Errorf("A sees %d models of the provider, want 2 (selected and granted, all)", len(only.Items))
		}
		for _, it := range only.Items {
			if it.Model.Provider != p.Name {
				t.Errorf("a model of another provider: %+v", it.Model)
			}
		}
		seen := map[string]bool{}
		cursor, pages := "", 0
		for {
			q := "?limit=5"
			if cursor != "" {
				q += "&cursor=" + url.QueryEscape(cursor)
			}
			page := decode[adminapi.TenantModelList](t, c.do(t, "GET", "/v1/tenants/"+c.a.ID+"/models"+q, c.ka, nil), 200)
			if len(page.Items) > 5 {
				t.Fatalf("a page of %d with limit 5", len(page.Items))
			}
			for _, it := range page.Items {
				if seen[it.Model.ID] {
					t.Errorf("%s is listed twice", it.Model.Ref)
				}
				seen[it.Model.ID] = true
			}
			pages++
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if pages < 4 || len(seen) != len(seesA()) {
			t.Errorf("%d pages with %d models, A sees %d", pages, len(seen), len(seesA()))
		}
		// The price in force now comes with the model.
		if _, err := c.api.Client(c.plat).CreatePriceOverride(ctx(t), adminapi.CreatePriceOverrideInput{Model: all.ID, Body: adminapi.PriceOverride{
			Currency: "EUR", Rates: []adminapi.PriceRate{{Category: adminapi.PriceCategoryInput, Rate: "2.5"}, {Category: adminapi.PriceCategoryOutput, Rate: "10"}}}}); err != nil {
			t.Fatal(err)
		}
		m := seesA()[all.Ref]
		if m.Price == nil || m.Price.Currency != "EUR" || len(m.Price.Rates) != 2 || m.Price.Source != adminapi.PriceSourceOverride {
			t.Errorf("the price of a model: %+v", m.Price)
		}
		if m := seesA()[sel.Ref]; m.Price != nil {
			t.Errorf("a model without a price carries one: %+v", m.Price)
		}
	})

	t.Run("a tenant cannot read the grants or the catalogue", func(t *testing.T) {
		for _, path := range []string{"/v1/models", "/v1/models/" + sel.ID, "/v1/models/" + sel.ID + "/grants", "/v1/providers", "/v1/price-imports"} {
			expect(t, c.do(t, "GET", path, c.ka, nil), 403, adminapi.ProblemCodeForbidden)
		}
		// Another tenant's list is not found, whoever asks.
		expect(t, c.do(t, "GET", "/v1/tenants/"+c.b.ID+"/models", c.ka, nil), 404, adminapi.ProblemCodeNotFound)
	})
}

// TestModelRestrictions is T-402: a tenant narrows the models for itself and, when it issues a
// key, for that key; a reference to a model the tenant may not use is the same 400 whether the
// model is unknown or granted only to another tenant.
func TestModelRestrictions(t *testing.T) {
	c := newCat(t)
	p := c.createProvider(t, nil)
	c.createEndpoint(t, p.Name, nil)
	mine := c.createModel(t, p.Name, map[string]any{"name": "synthetic-mine"})
	c.grant(t, mine.ID, c.a.ID)
	theirs := c.createModel(t, p.Name, map[string]any{"name": "synthetic-theirs"})
	c.grant(t, theirs.ID, c.b.ID)
	open := c.createModel(t, p.Name, map[string]any{"name": "synthetic-open", "availability": "all"})
	retired := c.createModel(t, p.Name, map[string]any{"name": "synthetic-retired", "availability": "all", "status": "retired"})
	path := "/v1/tenants/" + c.a.ID + "/model-restriction"

	t.Run("set, read, clear", func(t *testing.T) {
		r := decode[adminapi.ModelRestriction](t, c.do(t, "GET", path, c.ka, nil), 200)
		if r.Restricted || r.Models == nil || len(r.Models) != 0 || r.UpdatedAt != nil {
			t.Errorf("no restriction yet: %+v", r)
		}
		raw := rawJSON(t, c.do(t, "GET", path, c.ka, nil))
		if ms, ok := raw["models"].([]any); !ok || len(ms) != 0 {
			t.Errorf("models is not an empty array in %v", raw)
		}
		s := decode[adminapi.ModelRestriction](t, c.do(t, "PUT", path, c.ka, map[string]any{"models": []string{mine.Ref, open.Ref, "openai/gpt-4o"}}), 200)
		if !s.Restricted || len(s.Models) != 3 || s.UpdatedAt == nil {
			t.Errorf("set: %+v", s)
		}
		g := decode[adminapi.ModelRestriction](t, c.do(t, "GET", path, c.ka, nil), 200)
		if !g.Restricted || len(g.Models) != 3 || g.Models[0] != mine.Ref {
			t.Errorf("read: %+v (the references come back in the order given)", g)
		}
		// Replaced as a whole, not merged.
		s = decode[adminapi.ModelRestriction](t, c.do(t, "PUT", path, c.ka, map[string]any{"models": []string{open.Ref}}), 200)
		if len(s.Models) != 1 || s.Models[0] != open.Ref {
			t.Errorf("replaced: %+v", s)
		}
		// An empty list restricts to nothing; clearing removes the restriction.
		s = decode[adminapi.ModelRestriction](t, c.do(t, "PUT", path, c.ka, map[string]any{"models": []string{}}), 200)
		if !s.Restricted || s.Models == nil || len(s.Models) != 0 {
			t.Errorf("an empty restriction: %+v", s)
		}
		if v := c.visible(t, c.a.ID, c.ka); len(v) == 0 {
			t.Error("a restriction hides models from the list instead of flagging them")
		} else {
			for ref, m := range v {
				if m.Allowed {
					t.Errorf("%s is allowed under an empty restriction", ref)
				}
			}
		}
		cl := decode[adminapi.ModelRestriction](t, c.do(t, "DELETE", path, c.ka, nil), 200)
		if cl.Restricted || len(cl.Models) != 0 {
			t.Errorf("cleared: %+v", cl)
		}
		if g := decode[adminapi.ModelRestriction](t, c.do(t, "GET", path, c.ka, nil), 200); g.Restricted {
			t.Errorf("read after clearing: %+v", g)
		}
		// Clearing twice is fine.
		if cl := decode[adminapi.ModelRestriction](t, c.do(t, "DELETE", path, c.ka, nil), 200); cl.Restricted {
			t.Errorf("cleared again: %+v", cl)
		}
	})

	t.Run("the allowed flag", func(t *testing.T) {
		decode[adminapi.ModelRestriction](t, c.do(t, "PUT", path, c.ka, map[string]any{"models": []string{open.Ref}}), 200)
		v := c.visible(t, c.a.ID, c.ka)
		if !v[open.Ref].Allowed || v[mine.Ref].Allowed || v["openai/gpt-4o"].Allowed {
			t.Errorf("allowed: open %v, mine %v, gpt-4o %v", v[open.Ref].Allowed, v[mine.Ref].Allowed, v["openai/gpt-4o"].Allowed)
		}
		if v[mine.Ref].Model.ID == "" {
			t.Error("a model outside the restriction is missing from the list: it is flagged, not hidden")
		}
		// The restriction of A does not touch B.
		if c.visible(t, c.b.ID, c.kb)[open.Ref].Allowed != true {
			t.Error("B is restricted by A's restriction")
		}
		decode[adminapi.ModelRestriction](t, c.do(t, "DELETE", path, c.ka, nil), 200)
		for ref, m := range c.visible(t, c.a.ID, c.ka) {
			if !m.Allowed {
				t.Errorf("%s is not allowed without a restriction", ref)
			}
		}
	})

	t.Run("references must name models available to the tenant, and say nothing else", func(t *testing.T) {
		var bodies []string
		for name, ref := range map[string]string{
			"a model that does not exist":       p.Name + "/synthetic-nothing",
			"a provider that does not exist":    "no-such-provider/x",
			"a model granted to another tenant": theirs.Ref,
			"a retired model":                   retired.Ref,
			"an upstream identifier":            p.Name + "/up-nothing",
		} {
			r := c.do(t, "PUT", path, c.ka, map[string]any{"models": []string{open.Ref, ref}})
			invalidOn(t, name, r, "models")
			bodies = append(bodies, normalizeBody(r))
		}
		for _, b := range bodies[1:] {
			if b != bodies[0] {
				t.Errorf("the refusals differ:\n%s\n%s", bodies[0], b)
			}
		}
		for name, refs := range map[string][]string{
			"a bare name":      {"synthetic-mine"},
			"a trailing slash": {p.Name + "/"},
			"a duplicate":      {mine.Ref, mine.Ref},
			"65 references":    manyRefs(65, open.Ref),
		} {
			if r := c.do(t, "PUT", path, c.ka, map[string]any{"models": refs}); r.Status != 400 {
				t.Errorf("%s: %d\n%s", name, r.Status, r.Body)
			}
		}
		if g := decode[adminapi.ModelRestriction](t, c.do(t, "GET", path, c.ka, nil), 200); g.Restricted {
			t.Errorf("a refused restriction was stored: %+v", g)
		}
		invalidOn(t, "no models member", c.do(t, "PUT", path, c.ka, map[string]any{}), "models")
		invalidOn(t, "null", c.do(t, "PUT", path, c.ka, map[string]any{"models": nil}), "models")
	})

	t.Run("a key may carry a list of models", func(t *testing.T) {
		sa, err := c.api.Client(c.ka).CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: c.a.ID, Body: adminapi.ServiceAccountCreate{Name: "listed", Roles: []string{"inference"}}})
		if err != nil {
			t.Fatal(err)
		}
		keys := "/v1/tenants/" + c.a.ID + "/virtual-keys"
		made := decode[adminapi.VirtualKeyCreated](t, c.do(t, "POST", keys, c.ka, map[string]any{"service_account_id": sa.ID, "models": []string{mine.Ref, open.Ref}}), 201)
		if len(made.Key.Models) != 2 || made.Key.Models[0] != mine.Ref || made.Key.Models[1] != open.Ref {
			t.Errorf("created: %+v", made.Key)
		}
		got := decode[adminapi.VirtualKey](t, c.do(t, "GET", keys+"/"+made.Key.ID, c.ka, nil), 200)
		if len(got.Models) != 2 || got.Models[0] != mine.Ref {
			t.Errorf("get: %+v", got)
		}
		found := false
		for _, k := range decode[adminapi.VirtualKeyList](t, c.do(t, "GET", keys+"?limit=100", c.ka, nil), 200).Items {
			if k.ID == made.Key.ID {
				found = len(k.Models) == 2
			}
		}
		if !found {
			t.Error("the list of keys does not show the models")
		}
		// A key without a list has none in its representation.
		plain := decode[adminapi.VirtualKeyCreated](t, c.do(t, "POST", keys, c.ka, map[string]any{"service_account_id": sa.ID}), 201)
		if plain.Key.Models != nil {
			t.Errorf("a key without a list shows %v", plain.Key.Models)
		}
		if m := rawJSON(t, c.do(t, "GET", keys+"/"+plain.Key.ID, c.ka, nil)); m["models"] != nil {
			t.Errorf("the member models is present for a key with no list: %v", m["models"])
		}
		// The two refusals are the same 400, and nothing is created.
		before := c.api.SnapshotTenant(t, c.a.ID)
		var bodies []string
		for _, ref := range []string{theirs.Ref, p.Name + "/synthetic-nothing", retired.Ref} {
			r := c.do(t, "POST", keys, c.ka, map[string]any{"service_account_id": sa.ID, "models": []string{ref}})
			invalidOn(t, "the model "+ref, r, "models")
			bodies = append(bodies, normalizeBody(r))
		}
		for _, b := range bodies[1:] {
			if b != bodies[0] {
				t.Errorf("the refusals differ:\n%s\n%s", bodies[0], b)
			}
		}
		for name, refs := range map[string][]string{"an empty list": {}, "a duplicate": {open.Ref, open.Ref}, "65 references": manyRefs(65, open.Ref), "a bare name": {"gpt-4o"}} {
			if r := c.do(t, "POST", keys, c.ka, map[string]any{"service_account_id": sa.ID, "models": refs}); r.Status != 400 {
				t.Errorf("%s: %d\n%s", name, r.Status, r.Body)
			}
		}
		if after := c.api.SnapshotTenant(t, c.a.ID); after != before {
			t.Error("a refused key changed a row of the tenant")
		}
		// The list is set when the key is made and never changes: there is no operation that changes it,
		// and the restriction of the tenant is a different thing.
		for _, m := range []string{"PATCH", "PUT"} {
			if r := c.do(t, m, keys+"/"+made.Key.ID, c.ka, map[string]any{"models": []string{open.Ref}}); r.Status != 404 && r.Status != 405 {
				t.Errorf("%s on a key: %d", m, r.Status)
			}
		}
		decode[adminapi.ModelRestriction](t, c.do(t, "PUT", path, c.ka, map[string]any{"models": []string{open.Ref}}), 200)
		if got := decode[adminapi.VirtualKey](t, c.do(t, "GET", keys+"/"+made.Key.ID, c.ka, nil), 200); len(got.Models) != 2 {
			t.Errorf("the restriction of the tenant changed the list of a key: %+v", got.Models)
		}
		// The platform audit and the tenant's: the row of the key names the list (T-405 checks the shape).
	})

	t.Run("a model granted to a tenant and later revoked may remain in a restriction", func(t *testing.T) {
		extra := c.createModel(t, p.Name, map[string]any{"name": "synthetic-extra"})
		c.grant(t, extra.ID, c.a.ID)
		decode[adminapi.ModelRestriction](t, c.do(t, "PUT", path, c.ka, map[string]any{"models": []string{extra.Ref}}), 200)
		decode[adminapi.ModelGrant](t, c.platform(t, "DELETE", "/v1/models/"+extra.ID+"/grants/"+c.a.ID, nil), 200)
		// A reference that stopped matching just stops matching: nothing is rewritten.
		if g := decode[adminapi.ModelRestriction](t, c.do(t, "GET", path, c.ka, nil), 200); len(g.Models) != 1 || g.Models[0] != extra.Ref {
			t.Errorf("%+v", g)
		}
		if _, ok := c.visible(t, c.a.ID, c.ka)[extra.Ref]; ok {
			t.Error("a revoked model is listed")
		}
	})
}

func manyRefs(n int, ref string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = ref + "-" + apitest.UniqueName("n")
	}
	return out
}
