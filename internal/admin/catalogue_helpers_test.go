package admin_test

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
)

// catalogueMutations are the mutating operations of sprint 2.2 that TestCatalogAudit (T-405)
// covers; the enumerating tests of epic 1 and sprint 2.1 (T-281, T-282) leave them to it.
var catalogueMutations = map[string]bool{
	"createProvider": true, "updateProvider": true, "deleteProvider": true,
	"createEndpoint": true, "updateEndpoint": true, "deleteEndpoint": true,
	"createModel": true, "updateModel": true, "deleteModel": true,
	"grantModel": true, "revokeModelGrant": true,
	"createPriceOverride": true, "deletePriceVersion": true, "clearPriceOverride": true, "runPriceImport": true,
	"setModelRestriction": true, "clearModelRestriction": true,
}

// cat is an API with two tenants, for the tests of the catalogue.
type cat struct {
	api    *apitest.API
	plat   string // the platform key
	a, b   adminapi.Tenant
	ka, kb string // the keys of their administrators
}

func newCat(t *testing.T, opts ...func(*admin.Options)) *cat {
	t.Helper()
	c := &cat{api: apitest.New(t, opts...)}
	c.plat = c.api.PlatformKey()
	c.a, c.ka = c.api.Tenant(t, "cat-a")
	c.b, c.kb = c.api.Tenant(t, "cat-b")
	return c
}

// do calls the API with a key.
func (c *cat) do(t testing.TB, method, path, key string, body any) *apitest.Response {
	t.Helper()
	return c.api.Do(t, method, path, key, body, nil)
}

// platform calls the API with the platform key.
func (c *cat) platform(t testing.TB, method, path string, body any) *apitest.Response {
	t.Helper()
	return c.do(t, method, path, c.plat, body)
}

// decode checks the status of a successful response and decodes its body.
func decode[T any](t testing.TB, r *apitest.Response, status int) T {
	t.Helper()
	var v T
	ok(t, r, status, &v)
	return v
}

// createProvider makes a provider (family openai unless the body says otherwise) and returns it.
func (c *cat) createProvider(t testing.TB, body map[string]any) adminapi.Provider {
	t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	if _, ok := body["name"]; !ok {
		body["name"] = apitest.UniqueName("synthetic")
	}
	if _, ok := body["api_family"]; !ok {
		body["api_family"] = "openai"
	}
	return decode[adminapi.Provider](t, c.platform(t, "POST", "/v1/providers", body), 201)
}

// createEndpoint makes an endpoint of the provider and returns it.
func (c *cat) createEndpoint(t testing.TB, provider string, body map[string]any) adminapi.Endpoint {
	t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	if _, ok := body["name"]; !ok {
		body["name"] = apitest.UniqueName("ep")
	}
	if _, ok := body["base_url"]; !ok {
		body["base_url"] = "https://synthetic.invalid"
	}
	return decode[adminapi.Endpoint](t, c.platform(t, "POST", "/v1/providers/"+provider+"/endpoints", body), 201)
}

// createModel makes a model of the provider and returns it.
func (c *cat) createModel(t testing.TB, provider string, body map[string]any) adminapi.Model {
	t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	body["provider"] = provider
	if _, ok := body["name"]; !ok {
		body["name"] = apitest.UniqueName("synthetic-model")
	}
	return decode[adminapi.Model](t, c.platform(t, "POST", "/v1/models", body), 201)
}

// grant grants a model to a tenant.
func (c *cat) grant(t testing.TB, model, tenant string) adminapi.ModelGrant {
	t.Helper()
	return decode[adminapi.ModelGrant](t, c.platform(t, "PUT", "/v1/models/"+model+"/grants/"+tenant, nil), 200)
}

// visible returns the references of the models the tenant sees (every page).
func (c *cat) visible(t testing.TB, tenant, key string) map[string]adminapi.TenantModel {
	t.Helper()
	out := map[string]adminapi.TenantModel{}
	cursor := ""
	for {
		q := "?limit=200"
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		page := decode[adminapi.TenantModelList](t, c.do(t, "GET", "/v1/tenants/"+tenant+"/models"+q, key, nil), 200)
		for _, m := range page.Items {
			out[m.Model.Ref] = m
		}
		if page.NextCursor == "" {
			return out
		}
		cursor = page.NextCursor
	}
}

// fieldsOf returns the members named by an invalid_request.
func fieldsOf(p adminapi.Problem) []string {
	var out []string
	for _, e := range p.Errors {
		out = append(out, e.Field)
	}
	return out
}

// invalidOn expects a 400 that names the member.
func invalidOn(t testing.TB, what string, r *apitest.Response, field string) adminapi.Problem {
	t.Helper()
	if r.Status != 400 {
		t.Errorf("%s: status %d, want 400 on %s\n%s", what, r.Status, field, r.Body)
		return adminapi.Problem{}
	}
	p := problem(t, r)
	if p.Code != adminapi.ProblemCodeInvalidRequest {
		t.Errorf("%s: code %s", what, p.Code)
	}
	named := false
	for _, f := range fieldsOf(p) {
		named = named || f == field
	}
	if !named {
		t.Errorf("%s: the member %q is not named: %+v", what, field, p.Errors)
	}
	return p
}

// rawJSON decodes a response body into a generic object.
func rawJSON(t testing.TB, r *apitest.Response) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("%v\n%s", err, r.Body)
	}
	return m
}

// normalizeBody is a response body without its request ID, for comparing two refusals.
func normalizeBody(r *apitest.Response) string {
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		return string(r.Body)
	}
	delete(m, "request_id")
	b, _ := json.Marshal(m)
	return string(b)
}
