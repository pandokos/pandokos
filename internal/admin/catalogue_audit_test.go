package admin_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/testutil"
)

// wantRow is an audit row the way section 5.11 describes it: the actor is the platform key of the
// test (or the tenant's administrator), the request ID is the one of the response, and the detail
// holds at least the members named here.
type wantRow struct {
	log          string // "platform" (default) or "tenant"
	action       string
	resourceType string
	resourceID   string
	detail       map[string]any // a subset of the detail
}

// TestCatalogAudit is T-405: every mutating operation of the catalogue writes the row of section
// 5.11 with the request ID of its response; an operation that changes nothing writes none; a
// failing audit insert rolls the change back.
func TestCatalogAudit(t *testing.T) {
	c := newCat(t)
	api := c.api
	seen := seenIDs(t, api)
	platID, platName := api.PlatformKey()[4:21], "apitest"
	admin := *me(t, api, c.ka)

	check := func(what string, r *apitest.Response, status int, want ...wantRow) []auditRow {
		t.Helper()
		if r.Status != status {
			t.Fatalf("%s: status %d, want %d\n%s", what, r.Status, status, r.Body)
		}
		got := newRows(t, api, seen)
		if len(got) != len(want) {
			t.Errorf("%s: %d audit rows, want %d: %+v", what, len(got), len(want), got)
			return got
		}
		used := make([]bool, len(got))
		for _, w := range want {
			found := false
			for i, g := range got {
				if used[i] || g.Action != w.action {
					continue
				}
				used[i], found = true, true
				log := w.log
				if log == "" {
					log = "platform"
				}
				if g.Log != log || g.ResourceType != w.resourceType || g.Outcome != "success" || g.RequestID != r.Header.Get("X-Pandokos-Request-Id") {
					t.Errorf("%s: row %+v, want log %s, resource %s, success, request %s", what, g, log, w.resourceType, r.Header.Get("X-Pandokos-Request-Id"))
				}
				if w.resourceID != "" && g.ResourceID != w.resourceID {
					t.Errorf("%s: resource ID %q, want %q", what, g.ResourceID, w.resourceID)
				}
				if log == "platform" && (g.ActorType != "platform" || g.ActorID != platID || g.ActorName != platName || g.KeyID != platID) {
					t.Errorf("%s: actor %s %s %s", what, g.ActorType, g.ActorID, g.ActorName)
				}
				if log == "tenant" && (g.ActorType != "service_account" || g.ActorID != admin.ID || g.TenantID != c.a.ID) {
					t.Errorf("%s: actor %s %s in tenant %s", what, g.ActorType, g.ActorID, g.TenantID)
				}
				for k, v := range w.detail {
					if !reflect.DeepEqual(g.Detail[k], v) {
						t.Errorf("%s: detail %s = %#v, want %#v (all: %v)", what, k, g.Detail[k], v, g.Detail)
					}
				}
			}
			if !found {
				t.Errorf("%s: no row %s in %+v", what, w.action, got)
			}
		}
		return got
	}
	nothing := func(what string, r *apitest.Response, status int) {
		t.Helper()
		check(what, r, status)
	}

	// Providers and endpoints.
	r := c.platform(t, "POST", "/v1/providers", map[string]any{"name": "synthetic-audit", "api_family": "openai", "display_name": "Audit", "dataset_provider": "synthetic"})
	check("createProvider", r, 201, wantRow{action: "provider.create", resourceType: "provider", resourceID: "synthetic-audit", detail: map[string]any{"api_family": "openai", "name": "synthetic-audit"}})
	r = c.platform(t, "PATCH", "/v1/providers/synthetic-audit", map[string]any{"display_name": "Renamed", "enabled": false, "description": ""})
	check("updateProvider", r, 200, wantRow{action: "provider.update", resourceType: "provider", resourceID: "synthetic-audit",
		detail: map[string]any{"display_name": map[string]any{"from": "Audit", "to": "Renamed"}, "enabled": map[string]any{"from": true, "to": false}}})
	var got []auditRow
	nothing("updateProvider to the same values", c.platform(t, "PATCH", "/v1/providers/synthetic-audit", map[string]any{"display_name": "Renamed", "enabled": false, "description": ""}), 200)
	nothing("updateProvider with nothing to change", c.platform(t, "PATCH", "/v1/providers/synthetic-audit", map[string]any{}), 200)
	c.platform(t, "PATCH", "/v1/providers/synthetic-audit", map[string]any{"enabled": true})
	newRows(t, api, seen)

	r = c.platform(t, "POST", "/v1/providers/synthetic-audit/endpoints", map[string]any{"name": "main", "base_url": "https://audit.example.com", "region": "eu"})
	check("createEndpoint", r, 201, wantRow{action: "endpoint.create", resourceType: "endpoint", resourceID: "synthetic-audit/main", detail: map[string]any{"base_url": "https://audit.example.com"}})
	r = c.platform(t, "POST", "/v1/providers/synthetic-audit/endpoints", map[string]any{"name": "second", "base_url": "https://second.example.com"})
	check("createEndpoint (second)", r, 201, wantRow{action: "endpoint.create", resourceType: "endpoint", resourceID: "synthetic-audit/second"})
	r = c.platform(t, "PATCH", "/v1/providers/synthetic-audit/endpoints/second", map[string]any{"region": "us", "is_default": true})
	check("updateEndpoint", r, 200, wantRow{action: "endpoint.update", resourceType: "endpoint", resourceID: "synthetic-audit/second",
		detail: map[string]any{"region": map[string]any{"from": "", "to": "us"}, "is_default": map[string]any{"from": false, "to": true}}})
	nothing("updateEndpoint to the same values", c.platform(t, "PATCH", "/v1/providers/synthetic-audit/endpoints/second", map[string]any{"region": "us"}), 200)
	r = c.platform(t, "DELETE", "/v1/providers/synthetic-audit/endpoints/main", nil)
	check("deleteEndpoint", r, 200, wantRow{action: "endpoint.delete", resourceType: "endpoint", resourceID: "synthetic-audit/main"})

	// Models and grants.
	r = c.platform(t, "POST", "/v1/models", map[string]any{"provider": "synthetic-audit", "name": "audited-model", "availability": "selected"})
	check("createModel", r, 201, wantRow{action: "model.create", resourceType: "model", detail: map[string]any{"provider": "synthetic-audit", "name": "audited-model"}})
	mid := decode[adminapi.ModelList](t, c.platform(t, "GET", "/v1/models?provider=synthetic-audit&name=audited-model", nil), 200).Items[0].ID
	r = c.platform(t, "PATCH", "/v1/models/"+mid, map[string]any{"status": "deprecated", "context_window": 4096})
	check("updateModel", r, 200, wantRow{action: "model.update", resourceType: "model", resourceID: mid,
		detail: map[string]any{"provider": "synthetic-audit", "name": "audited-model", "status": map[string]any{"from": "active", "to": "deprecated"}, "context_window": map[string]any{"from": float64(0), "to": float64(4096)}}})
	nothing("updateModel to the same values", c.platform(t, "PATCH", "/v1/models/"+mid, map[string]any{"status": "deprecated", "context_window": 4096}), 200)

	r = c.platform(t, "PUT", "/v1/models/"+mid+"/grants/"+c.a.ID, nil)
	check("grantModel", r, 200, wantRow{action: "model_grant.create", resourceType: "model", resourceID: mid,
		detail: map[string]any{"provider": "synthetic-audit", "name": "audited-model", "tenant_id": c.a.ID, "tenant_name": c.a.Name}})
	nothing("grantModel again", c.platform(t, "PUT", "/v1/models/"+mid+"/grants/"+c.a.ID, nil), 200)
	r = c.platform(t, "DELETE", "/v1/models/"+mid+"/grants/"+c.a.ID, nil)
	check("revokeModelGrant", r, 200, wantRow{action: "model_grant.delete", resourceType: "model", resourceID: mid,
		detail: map[string]any{"provider": "synthetic-audit", "name": "audited-model", "tenant_id": c.a.ID, "tenant_name": c.a.Name}})
	nothing("revokeModelGrant again", c.platform(t, "DELETE", "/v1/models/"+mid+"/grants/"+c.a.ID, nil), 404)

	// Prices.
	r = c.platform(t, "POST", "/v1/models/"+mid+"/prices", map[string]any{"currency": "USD", "note": "n", "rates": []map[string]any{
		{"category": "input", "threshold": 0, "rate": "2.50"}, {"category": "output", "threshold": 0, "rate": "10"}}})
	pv := decode[adminapi.PriceVersion](t, r, 201)
	got = check("createPriceOverride", r, 201, wantRow{action: "price.create", resourceType: "price_version", resourceID: pv.ID, detail: map[string]any{
		"provider": "synthetic-audit", "model": "audited-model", "source": "override", "currency": "USD",
		"rates": []any{map[string]any{"category": "input", "threshold": float64(0), "rate": "2.5"}, map[string]any{"category": "output", "threshold": float64(0), "rate": "10"}}}})
	if len(got) == 1 && !sameInstant(got[0].Detail["valid_from"], pv.ValidFrom) {
		t.Errorf("price.create: valid_from %v, the version starts %v", got[0].Detail["valid_from"], pv.ValidFrom)
	}
	future := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)
	r = c.platform(t, "POST", "/v1/models/"+mid+"/prices", map[string]any{"currency": "EUR", "valid_from": future.Format(time.RFC3339), "rates": []map[string]any{{"category": "input", "threshold": 0, "rate": "3"}}})
	pv2 := decode[adminapi.PriceVersion](t, r, 201)
	got = check("createPriceOverride (future)", r, 201, wantRow{action: "price.create", resourceType: "price_version", resourceID: pv2.ID, detail: map[string]any{"currency": "EUR"}})
	if len(got) == 1 {
		// The row names the version that was in force when it was created (it was not replaced: it was the previous one).
		if !strings.Contains(string(mustJSONBytes(got[0].Detail)), pv.ID) {
			t.Errorf("the row of a new version does not name the previous version %s: %v", pv.ID, got[0].Detail)
		}
	}
	r = c.platform(t, "DELETE", "/v1/models/"+mid+"/prices/"+pv2.ID, nil)
	got = check("deletePriceVersion", r, 200, wantRow{action: "price.delete", resourceType: "price_version", resourceID: pv2.ID,
		detail: map[string]any{"provider": "synthetic-audit", "model": "audited-model"}})
	if len(got) == 1 && !sameInstant(got[0].Detail["valid_from"], pv2.ValidFrom) {
		t.Errorf("price.delete: valid_from %v, the version started %v", got[0].Detail["valid_from"], pv2.ValidFrom)
	}
	nothing("deletePriceVersion of a version that has started", c.platform(t, "DELETE", "/v1/models/"+mid+"/prices/"+pv.ID, nil), 409)
	r = c.platform(t, "POST", "/v1/models/"+mid+"/clear-price-override", map[string]any{"reason": "dataset again"})
	got = check("clearPriceOverride", r, 200, wantRow{action: "price.clear_override", resourceType: "model", resourceID: mid, detail: map[string]any{"provider": "synthetic-audit", "model": "audited-model"}})
	if len(got) == 1 && got[0].Reason != "dataset again" {
		t.Errorf("the reason of the clearing: %q", got[0].Reason)
	}
	nothing("clearPriceOverride again", c.platform(t, "POST", "/v1/models/"+mid+"/clear-price-override", map[string]any{}), 200)

	// A dry run writes nothing; an import writes one row per new version and one for the run.
	nothing("a dry-run import", c.platform(t, "POST", "/v1/price-imports", map[string]any{"source": "embedded", "dry_run": true}), 201)
	r = c.platform(t, "POST", "/v1/price-imports", map[string]any{"source": "embedded"})
	imp := decode[adminapi.PriceImport](t, r, 201)
	all := newRows(t, api, seen)
	creates, runs := 0, 0
	for _, g := range all {
		switch g.Action {
		case "price.create":
			creates++
			if g.Log != "platform" || g.ResourceType != "price_version" || g.RequestID != r.Header.Get("X-Pandokos-Request-Id") || g.Detail["import_id"] != imp.ID || g.Detail["source"] != "import" || g.Detail["currency"] != "USD" {
				t.Errorf("a price.create row of an import: %+v", g)
			}
			if _, ok := g.Detail["rates"].([]any); !ok {
				t.Errorf("a price.create row without its rates: %v", g.Detail)
			}
		case "price_import.run":
			runs++
			if g.ResourceType != "price_import" || g.ResourceID != imp.ID || g.RequestID != r.Header.Get("X-Pandokos-Request-Id") ||
				g.Detail["source"] != "embedded" || g.Detail["digest"] != imp.Digest || g.Detail["dry_run"] != false || g.ActorType != "platform" || g.ActorID != platID {
				t.Errorf("the price_import.run row: %+v", g)
			}
			if got, want := g.Detail["created"], float64(imp.VersionsCreated); got != want && g.Detail["versions_created"] != want {
				t.Errorf("the run row does not say how many versions it created: %v", g.Detail)
			}
		default:
			t.Errorf("an import wrote %s", g.Action)
		}
	}
	if creates != imp.VersionsCreated || runs != 1 {
		t.Errorf("%d price.create rows for %d versions, %d run rows", creates, imp.VersionsCreated, runs)
	}
	// The same import again creates no version, and still records that somebody asked for it.
	again := c.platform(t, "POST", "/v1/price-imports", map[string]any{"source": "embedded"})
	check("an import that changes nothing", again, 201, wantRow{action: "price_import.run", resourceType: "price_import"})

	// The tenant's log: restrictions and the model list of a key.
	path := "/v1/tenants/" + c.a.ID + "/model-restriction"
	c.platform(t, "PUT", "/v1/models/"+mid+"/grants/"+c.a.ID, nil)
	newRows(t, api, seen)
	r = c.do(t, "PUT", path, c.ka, map[string]any{"models": []string{"openai/gpt-4o", "synthetic-audit/audited-model"}})
	check("setModelRestriction", r, 200, wantRow{log: "tenant", action: "model_restriction.set", resourceType: "model_restriction",
		detail: map[string]any{"models": []any{"openai/gpt-4o", "synthetic-audit/audited-model"}}})
	nothing("setModelRestriction to the same list", c.do(t, "PUT", path, c.ka, map[string]any{"models": []string{"openai/gpt-4o", "synthetic-audit/audited-model"}}), 200)
	r = c.do(t, "PUT", path, c.ka, map[string]any{"models": []string{"openai/gpt-4o"}})
	check("setModelRestriction (replaced)", r, 200, wantRow{log: "tenant", action: "model_restriction.set", resourceType: "model_restriction",
		detail: map[string]any{"models": []any{"openai/gpt-4o"}, "previous": []any{"openai/gpt-4o", "synthetic-audit/audited-model"}}})
	r = c.do(t, "DELETE", path, c.ka, nil)
	check("clearModelRestriction", r, 200, wantRow{log: "tenant", action: "model_restriction.clear", resourceType: "model_restriction"})
	nothing("clearModelRestriction again", c.do(t, "DELETE", path, c.ka, nil), 200)
	nothing("a restriction that is refused", c.do(t, "PUT", path, c.ka, map[string]any{"models": []string{"openai/no-such-model"}}), 400)

	sa, err := api.Client(c.ka).CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: c.a.ID, Body: adminapi.ServiceAccountCreate{Name: "audit-sa", Roles: []string{"inference"}}})
	if err != nil {
		t.Fatal(err)
	}
	newRows(t, api, seen)
	r = c.do(t, "POST", "/v1/tenants/"+c.a.ID+"/virtual-keys", c.ka, map[string]any{"service_account_id": sa.ID, "models": []string{"openai/gpt-4o"}})
	created := decode[adminapi.VirtualKeyCreated](t, r, 201)
	check("createVirtualKey with models", r, 201, wantRow{log: "tenant", action: "virtual_key.create", resourceType: "virtual_key", resourceID: created.Key.ID,
		detail: map[string]any{"models": []any{"openai/gpt-4o"}}})
	r = c.do(t, "POST", "/v1/tenants/"+c.a.ID+"/virtual-keys", c.ka, map[string]any{"service_account_id": sa.ID})
	rows2 := check("createVirtualKey without models", r, 201, wantRow{log: "tenant", action: "virtual_key.create", resourceType: "virtual_key"})
	if len(rows2) == 1 {
		if _, present := rows2[0].Detail["models"]; present {
			t.Errorf("a key without a list has models in its audit row: %v", rows2[0].Detail)
		}
	}

	// Nothing in the audit rows holds a key or a secret.
	for _, g := range auditRows(t, api) {
		testutil.AssertAbsent(t, "audit row "+g.Action, mustJSONString(g), api.Secrets()...)
	}

	t.Run("a failing audit insert rolls the change back", func(t *testing.T) {
		f := newCat(t)
		fapi := f.api
		p := f.createProvider(t, nil)
		f.createEndpoint(t, p.Name, nil)
		second := f.createEndpoint(t, p.Name, map[string]any{"name": "second"})
		model := f.createModel(t, p.Name, map[string]any{"name": "atomic-model", "availability": "all"})
		spare := f.createModel(t, p.Name, map[string]any{"name": "atomic-spare"})
		disposable := f.createProvider(t, nil)
		f.grant(t, spare.ID, f.b.ID)
		futureAt := time.Now().Add(4 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
		decode[adminapi.PriceVersion](t, f.platform(t, "POST", "/v1/models/"+model.ID+"/prices", map[string]any{"currency": "USD", "rates": []map[string]any{{"category": "input", "threshold": 0, "rate": "1"}}}), 201)
		fut := decode[adminapi.PriceVersion](t, f.platform(t, "POST", "/v1/models/"+model.ID+"/prices", map[string]any{"currency": "USD", "valid_from": futureAt, "rates": []map[string]any{{"category": "input", "threshold": 0, "rate": "2"}}}), 201)
		decode[adminapi.ModelRestriction](t, f.do(t, "PUT", "/v1/tenants/"+f.a.ID+"/model-restriction", f.ka, map[string]any{"models": []string{model.Ref}}), 200)
		sa, err := fapi.Client(f.ka).CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: f.a.ID, Body: adminapi.ServiceAccountCreate{Name: "atomic-sa", Roles: []string{"inference"}}})
		if err != nil {
			t.Fatal(err)
		}

		calls := map[string]func() *apitest.Response{
			"createProvider": func() *apitest.Response {
				return f.platform(t, "POST", "/v1/providers", map[string]any{"name": "atomic-new", "api_family": "openai"})
			},
			"updateProvider": func() *apitest.Response {
				return f.platform(t, "PATCH", "/v1/providers/"+p.Name, map[string]any{"description": "changed"})
			},
			"deleteProvider": func() *apitest.Response { return f.platform(t, "DELETE", "/v1/providers/"+disposable.Name, nil) },
			"createEndpoint": func() *apitest.Response {
				return f.platform(t, "POST", "/v1/providers/"+p.Name+"/endpoints", map[string]any{"name": "atomic-ep", "base_url": "https://x.example.com", "is_default": true})
			},
			"updateEndpoint": func() *apitest.Response {
				return f.platform(t, "PATCH", "/v1/providers/"+p.Name+"/endpoints/"+second.Name, map[string]any{"is_default": true})
			},
			"deleteEndpoint": func() *apitest.Response {
				return f.platform(t, "DELETE", "/v1/providers/"+p.Name+"/endpoints/"+second.Name, nil)
			},
			"createModel": func() *apitest.Response {
				return f.platform(t, "POST", "/v1/models", map[string]any{"provider": p.Name, "name": "atomic-created"})
			},
			"updateModel": func() *apitest.Response {
				return f.platform(t, "PATCH", "/v1/models/"+model.ID, map[string]any{"description": "changed"})
			},
			"deleteModel": func() *apitest.Response { return f.platform(t, "DELETE", "/v1/models/"+spare.ID, nil) },
			"grantModel":  func() *apitest.Response { return f.platform(t, "PUT", "/v1/models/"+spare.ID+"/grants/"+f.a.ID, nil) },
			"revokeModelGrant": func() *apitest.Response {
				return f.platform(t, "DELETE", "/v1/models/"+spare.ID+"/grants/"+f.b.ID, nil)
			},
			"createPriceOverride": func() *apitest.Response {
				return f.platform(t, "POST", "/v1/models/"+spare.ID+"/prices", map[string]any{"currency": "USD", "rates": []map[string]any{{"category": "input", "threshold": 0, "rate": "1"}}})
			},
			"deletePriceVersion": func() *apitest.Response {
				return f.platform(t, "DELETE", "/v1/models/"+model.ID+"/prices/"+fut.ID, nil)
			},
			"clearPriceOverride": func() *apitest.Response {
				return f.platform(t, "POST", "/v1/models/"+spare.ID+"/clear-price-override", map[string]any{})
			},
			"runPriceImport": func() *apitest.Response {
				return f.platform(t, "POST", "/v1/price-imports", map[string]any{"source": "embedded"})
			},
			"setModelRestriction": func() *apitest.Response {
				return f.do(t, "PUT", "/v1/tenants/"+f.a.ID+"/model-restriction", f.ka, map[string]any{"models": []string{}})
			},
			"clearModelRestriction": func() *apitest.Response {
				return f.do(t, "DELETE", "/v1/tenants/"+f.a.ID+"/model-restriction", f.ka, nil)
			},
			"createVirtualKey (models)": func() *apitest.Response {
				return f.do(t, "POST", "/v1/tenants/"+f.a.ID+"/virtual-keys", f.ka, map[string]any{"service_account_id": sa.ID, "models": []string{model.Ref}})
			},
		}
		for id := range catalogueMutations {
			if calls[id] == nil {
				t.Errorf("the mutating operation %s has no call here: add it", id)
			}
		}
		// clearPriceOverride refuses a model that is not flagged only in a way that changes nothing: flag it first.
		decode[adminapi.PriceVersion](t, f.platform(t, "POST", "/v1/models/"+spare.ID+"/prices", map[string]any{"currency": "USD", "rates": []map[string]any{{"category": "input", "threshold": 0, "rate": "1"}}}), 201)

		super := fapi.Super(t)
		for _, q := range []string{
			`CREATE FUNCTION pandokos.zz_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit is down'; END $$`,
			`CREATE TRIGGER a_fail BEFORE INSERT ON pandokos.audit_event FOR EACH ROW EXECUTE FUNCTION pandokos.zz_fail()`,
			`CREATE TRIGGER a_fail BEFORE INSERT ON pandokos.platform_audit_event FOR EACH ROW EXECUTE FUNCTION pandokos.zz_fail()`,
		} {
			if _, err := super.Exec(ctx(t), q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		for op, call := range calls {
			before := fapi.Snapshot(t)
			resp := call()
			p := expect(t, resp, 500, adminapi.ProblemCodeInternal)
			if strings.Contains(string(resp.Body), "audit is down") || p.Detail != "Internal error." {
				t.Errorf("%s: the failure leaks: %s", op, resp.Body)
			}
			if after := fapi.Snapshot(t); after != before {
				t.Errorf("%s: a table changed although the audit record could not be written", op)
			}
		}
		for _, q := range []string{`DROP TRIGGER a_fail ON pandokos.audit_event`, `DROP TRIGGER a_fail ON pandokos.platform_audit_event`} {
			if _, err := super.Exec(ctx(t), q); err != nil {
				t.Fatal(err)
			}
		}
		for _, op := range []string{"updateProvider", "createModel", "grantModel", "setModelRestriction"} {
			if resp := calls[op](); resp.Status != 200 && resp.Status != 201 {
				t.Errorf("%s after the trigger is gone: %d %s", op, resp.Status, resp.Body)
			}
		}
	})
}

// sameInstant reports whether the detail value is a time text of the same instant.
func sameInstant(v any, want time.Time) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	got, err := time.Parse(time.RFC3339Nano, s)
	return err == nil && got.Sub(want).Abs() < time.Millisecond
}

func mustJSONBytes(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func mustJSONString(v any) string { return string(mustJSONBytes(v)) }
