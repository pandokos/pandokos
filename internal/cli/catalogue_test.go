package cli_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// The catalogue operations of sprint 2.2 on the fake: each records its input and returns the
// canned output of r.f.out[<operation>] or a zero value.

func (f *fakeAPI) ListProviders(_ context.Context, in adminapi.ListProvidersInput) (*adminapi.ProviderList, error) {
	return pick[adminapi.ProviderList](f, "listProviders"), f.rec("listProviders", in)
}
func (f *fakeAPI) CreateProvider(_ context.Context, in adminapi.CreateProviderInput) (*adminapi.Provider, error) {
	return pick[adminapi.Provider](f, "createProvider"), f.rec("createProvider", in)
}
func (f *fakeAPI) GetProvider(_ context.Context, in adminapi.GetProviderInput) (*adminapi.Provider, error) {
	return pick[adminapi.Provider](f, "getProvider"), f.rec("getProvider", in)
}
func (f *fakeAPI) UpdateProvider(_ context.Context, in adminapi.UpdateProviderInput) (*adminapi.Provider, error) {
	return pick[adminapi.Provider](f, "updateProvider"), f.rec("updateProvider", in)
}
func (f *fakeAPI) DeleteProvider(_ context.Context, in adminapi.DeleteProviderInput) (*adminapi.Provider, error) {
	return pick[adminapi.Provider](f, "deleteProvider"), f.rec("deleteProvider", in)
}
func (f *fakeAPI) ListEndpoints(_ context.Context, in adminapi.ListEndpointsInput) (*adminapi.EndpointList, error) {
	return pick[adminapi.EndpointList](f, "listEndpoints"), f.rec("listEndpoints", in)
}
func (f *fakeAPI) CreateEndpoint(_ context.Context, in adminapi.CreateEndpointInput) (*adminapi.Endpoint, error) {
	return pick[adminapi.Endpoint](f, "createEndpoint"), f.rec("createEndpoint", in)
}
func (f *fakeAPI) GetEndpoint(_ context.Context, in adminapi.GetEndpointInput) (*adminapi.Endpoint, error) {
	return pick[adminapi.Endpoint](f, "getEndpoint"), f.rec("getEndpoint", in)
}
func (f *fakeAPI) UpdateEndpoint(_ context.Context, in adminapi.UpdateEndpointInput) (*adminapi.Endpoint, error) {
	return pick[adminapi.Endpoint](f, "updateEndpoint"), f.rec("updateEndpoint", in)
}
func (f *fakeAPI) DeleteEndpoint(_ context.Context, in adminapi.DeleteEndpointInput) (*adminapi.Endpoint, error) {
	return pick[adminapi.Endpoint](f, "deleteEndpoint"), f.rec("deleteEndpoint", in)
}
func (f *fakeAPI) ListModels(_ context.Context, in adminapi.ListModelsInput) (*adminapi.ModelList, error) {
	return pick[adminapi.ModelList](f, "listModels"), f.rec("listModels", in)
}
func (f *fakeAPI) CreateModel(_ context.Context, in adminapi.CreateModelInput) (*adminapi.Model, error) {
	return pick[adminapi.Model](f, "createModel"), f.rec("createModel", in)
}
func (f *fakeAPI) GetModel(_ context.Context, in adminapi.GetModelInput) (*adminapi.Model, error) {
	return pick[adminapi.Model](f, "getModel"), f.rec("getModel", in)
}
func (f *fakeAPI) UpdateModel(_ context.Context, in adminapi.UpdateModelInput) (*adminapi.Model, error) {
	return pick[adminapi.Model](f, "updateModel"), f.rec("updateModel", in)
}
func (f *fakeAPI) DeleteModel(_ context.Context, in adminapi.DeleteModelInput) (*adminapi.Model, error) {
	return pick[adminapi.Model](f, "deleteModel"), f.rec("deleteModel", in)
}
func (f *fakeAPI) ClearPriceOverride(_ context.Context, in adminapi.ClearPriceOverrideInput) (*adminapi.Model, error) {
	return pick[adminapi.Model](f, "clearPriceOverride"), f.rec("clearPriceOverride", in)
}
func (f *fakeAPI) ListModelGrants(_ context.Context, in adminapi.ListModelGrantsInput) (*adminapi.ModelGrantList, error) {
	return pick[adminapi.ModelGrantList](f, "listModelGrants"), f.rec("listModelGrants", in)
}
func (f *fakeAPI) GrantModel(_ context.Context, in adminapi.GrantModelInput) (*adminapi.ModelGrant, error) {
	return pick[adminapi.ModelGrant](f, "grantModel"), f.rec("grantModel", in)
}
func (f *fakeAPI) RevokeModelGrant(_ context.Context, in adminapi.RevokeModelGrantInput) (*adminapi.ModelGrant, error) {
	return pick[adminapi.ModelGrant](f, "revokeModelGrant"), f.rec("revokeModelGrant", in)
}
func (f *fakeAPI) ListPriceVersions(_ context.Context, in adminapi.ListPriceVersionsInput) (*adminapi.PriceVersionList, error) {
	return pick[adminapi.PriceVersionList](f, "listPriceVersions"), f.rec("listPriceVersions", in)
}
func (f *fakeAPI) CreatePriceOverride(_ context.Context, in adminapi.CreatePriceOverrideInput) (*adminapi.PriceVersion, error) {
	return pick[adminapi.PriceVersion](f, "createPriceOverride"), f.rec("createPriceOverride", in)
}
func (f *fakeAPI) DeletePriceVersion(_ context.Context, in adminapi.DeletePriceVersionInput) (*adminapi.PriceVersion, error) {
	return pick[adminapi.PriceVersion](f, "deletePriceVersion"), f.rec("deletePriceVersion", in)
}
func (f *fakeAPI) ListPriceImports(_ context.Context, in adminapi.ListPriceImportsInput) (*adminapi.PriceImportList, error) {
	return pick[adminapi.PriceImportList](f, "listPriceImports"), f.rec("listPriceImports", in)
}
func (f *fakeAPI) RunPriceImport(_ context.Context, in adminapi.RunPriceImportInput) (*adminapi.PriceImport, error) {
	return pick[adminapi.PriceImport](f, "runPriceImport"), f.rec("runPriceImport", in)
}
func (f *fakeAPI) GetPriceImport(_ context.Context, in adminapi.GetPriceImportInput) (*adminapi.PriceImport, error) {
	return pick[adminapi.PriceImport](f, "getPriceImport"), f.rec("getPriceImport", in)
}
func (f *fakeAPI) GetModelRestriction(_ context.Context, in adminapi.GetModelRestrictionInput) (*adminapi.ModelRestriction, error) {
	return pick[adminapi.ModelRestriction](f, "getModelRestriction"), f.rec("getModelRestriction", in)
}
func (f *fakeAPI) SetModelRestriction(_ context.Context, in adminapi.SetModelRestrictionInput) (*adminapi.ModelRestriction, error) {
	return pick[adminapi.ModelRestriction](f, "setModelRestriction"), f.rec("setModelRestriction", in)
}
func (f *fakeAPI) ClearModelRestriction(_ context.Context, in adminapi.ClearModelRestrictionInput) (*adminapi.ModelRestriction, error) {
	return pick[adminapi.ModelRestriction](f, "clearModelRestriction"), f.rec("clearModelRestriction", in)
}
func (f *fakeAPI) ListTenantModels(_ context.Context, in adminapi.ListTenantModelsInput) (*adminapi.TenantModelList, error) {
	return pick[adminapi.TenantModelList](f, "listTenantModels"), f.rec("listTenantModels", in)
}

const (
	mid  = "0192f0f0-0000-7000-8000-000000000010"
	pvid = "0192f0f0-0000-7000-8000-000000000011"
	iid  = "0192f0f0-0000-7000-8000-000000000012"
)

var (
	provider = adminapi.Provider{ID: "0192f0f0-0000-7000-8000-000000000020", Name: "acme", DisplayName: "Acme", ApiFamily: adminapi.ApiFamilyOpenai, Enabled: true, CreatedAt: t0, UpdatedAt: t0}
	model    = adminapi.Model{ID: mid, Provider: "openai", ApiFamily: adminapi.ApiFamilyOpenai, Name: "gpt-4o", Ref: "openai/gpt-4o", UpstreamID: "gpt-4o",
		Status: adminapi.ModelStatusActive, Availability: adminapi.ModelAvailabilityAll, Capabilities: []string{"text"}, ContextWindow: 128000, CreatedAt: t0, UpdatedAt: t0}
)

// TestCatalogueCommands is part of T-385: each command of sprint 2.2, section 5.10 calls its
// operation with the right input (with TestEveryOperationHasACommand, T-219). A member that is
// not given is not sent; an empty value clears; a model is an ID or a reference.
func TestCatalogueCommands(t *testing.T) {
	// call runs a command that must succeed and returns the only input its operation received.
	call := func(t *testing.T, id string, args ...string) any {
		t.Helper()
		r := newRig(t)
		mustRun(t, r, args...)
		return r.f.only(t, id)
	}
	check := func(t *testing.T, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("the API received\n %+v\nwant\n %+v", got, want)
		}
	}
	// A rig whose catalogue has one model, found by its reference.
	withModel := func(t *testing.T) *rig {
		t.Helper()
		r := newRig(t)
		r.f.out["listModels"] = &adminapi.ModelList{Items: []adminapi.Model{model}}
		return r
	}

	t.Run("provider", func(t *testing.T) {
		check(t, call(t, "listProviders", "provider", "list", "--limit", "5"), adminapi.ListProvidersInput{Limit: 5})
		check(t, call(t, "createProvider", "provider", "create", "--family", "anthropic", "--display-name", "Acme", "--description", "d", "--dataset-provider", "acme", "acme"),
			adminapi.CreateProviderInput{Body: adminapi.ProviderCreate{Name: "acme", ApiFamily: adminapi.ApiFamilyAnthropic, DisplayName: "Acme", Description: "d", DatasetProvider: "acme"}})
		check(t, call(t, "createProvider", "provider", "create", "--family", "openai", "bare"),
			adminapi.CreateProviderInput{Body: adminapi.ProviderCreate{Name: "bare", ApiFamily: adminapi.ApiFamilyOpenai}})
		check(t, call(t, "getProvider", "provider", "get", "acme"), adminapi.GetProviderInput{Provider: "acme"})
		check(t, call(t, "deleteProvider", "provider", "delete", "acme"), adminapi.DeleteProviderInput{Provider: "acme"})

		// An absent member is not sent, an empty one clears, a boolean can be set to false.
		check(t, call(t, "updateProvider", "provider", "update", "--display-name", "Acme 2", "acme"),
			adminapi.UpdateProviderInput{Provider: "acme", Body: adminapi.ProviderUpdate{DisplayName: adminapi.Some("Acme 2")}})
		check(t, call(t, "updateProvider", "provider", "update", "--description", "", "--dataset-provider", "", "--enabled=false", "acme"),
			adminapi.UpdateProviderInput{Provider: "acme", Body: adminapi.ProviderUpdate{Description: adminapi.Some(""), DatasetProvider: adminapi.Some(""), Enabled: adminapi.Some(false)}})
		check(t, call(t, "updateProvider", "provider", "update", "--enabled", "acme"),
			adminapi.UpdateProviderInput{Provider: "acme", Body: adminapi.ProviderUpdate{Enabled: adminapi.Some(true)}})

		r := newRig(t)
		for name, args := range map[string][]string{
			"create without a family":     {"provider", "create", "acme"},
			"create with a bad family":    {"provider", "create", "--family", "gemini", "acme"},
			"create without a name":       {"provider", "create", "--family", "openai"},
			"update that changes nothing": {"provider", "update", "acme"},
			"get without a name":          {"provider", "get"},
			"delete with two names":       {"provider", "delete", "a", "b"},
			"an unknown subcommand":       {"provider", "frobnicate"},
		} {
			if code, out, errs := r.run(t, args...); code != 2 || out != "" || errs == "" {
				t.Errorf("%s: exit %d, stdout %q, stderr %q, want 2", name, code, out, errs)
			}
		}
		for _, id := range []string{"createProvider", "updateProvider", "getProvider", "deleteProvider"} {
			if n := len(r.f.calls(id)); n != 0 {
				t.Errorf("a refused command reached %s %d times", id, n)
			}
		}
	})

	t.Run("endpoint", func(t *testing.T) {
		check(t, call(t, "listEndpoints", "endpoint", "list", "--limit", "2", "acme"), adminapi.ListEndpointsInput{Provider: "acme", Limit: 2})
		check(t, call(t, "createEndpoint", "endpoint", "create", "--base-url", "https://api.example.com/v1", "--region", "eu", "--private", "--default", "--description", "d", "acme", "eu"),
			adminapi.CreateEndpointInput{Provider: "acme", Body: adminapi.EndpointCreate{Name: "eu", BaseURL: "https://api.example.com/v1", Region: "eu", Private: true, IsDefault: true, Description: "d"}})
		check(t, call(t, "createEndpoint", "endpoint", "create", "--base-url", "https://api.example.com", "acme", "main"),
			adminapi.CreateEndpointInput{Provider: "acme", Body: adminapi.EndpointCreate{Name: "main", BaseURL: "https://api.example.com"}})
		check(t, call(t, "getEndpoint", "endpoint", "get", "acme", "eu"), adminapi.GetEndpointInput{Provider: "acme", Endpoint: "eu"})
		check(t, call(t, "deleteEndpoint", "endpoint", "delete", "acme", "eu"), adminapi.DeleteEndpointInput{Provider: "acme", Endpoint: "eu"})
		check(t, call(t, "updateEndpoint", "endpoint", "update", "--base-url", "https://b.example.com", "--region", "", "--private=false", "--enabled=false", "--default", "--description", "", "acme", "eu"),
			adminapi.UpdateEndpointInput{Provider: "acme", Endpoint: "eu", Body: adminapi.EndpointUpdate{BaseURL: adminapi.Some("https://b.example.com"), Region: adminapi.Some(""),
				Private: adminapi.Some(false), Enabled: adminapi.Some(false), IsDefault: adminapi.Some(true), Description: adminapi.Some("")}})
		check(t, call(t, "updateEndpoint", "endpoint", "update", "--default=false", "acme", "eu"),
			adminapi.UpdateEndpointInput{Provider: "acme", Endpoint: "eu", Body: adminapi.EndpointUpdate{IsDefault: adminapi.Some(false)}})

		r := newRig(t)
		for name, args := range map[string][]string{
			"create without a URL":        {"endpoint", "create", "acme", "eu"},
			"create without a name":       {"endpoint", "create", "--base-url", "https://x.example.com", "acme"},
			"update that changes nothing": {"endpoint", "update", "acme", "eu"},
			"list without a provider":     {"endpoint", "list"},
		} {
			if code, out, errs := r.run(t, args...); code != 2 || out != "" || errs == "" {
				t.Errorf("%s: exit %d, stdout %q, stderr %q, want 2", name, code, out, errs)
			}
		}
	})

	t.Run("model", func(t *testing.T) {
		check(t, call(t, "listModels", "model", "list", "--provider", "openai", "--name", "gpt-4o", "--status", "active", "--availability", "all", "--sort", "-created_at", "--limit", "3"),
			adminapi.ListModelsInput{Provider: "openai", Name: "gpt-4o", Status: adminapi.ModelStatusActive, Availability: adminapi.ModelAvailabilityAll, Sort: adminapi.SortMinusCreatedAt, Limit: 3})
		check(t, call(t, "createModel", "model", "create", "--upstream-id", "u", "--display-name", "M", "--description", "d", "--status", "deprecated", "--availability", "selected",
			"--capability", "text", "--capability", "tools", "--context-window", "8192", "--max-output-tokens", "1024", "ollama/meta-llama/Llama-3"),
			adminapi.CreateModelInput{Body: adminapi.ModelCreate{Provider: "ollama", Name: "meta-llama/Llama-3", UpstreamID: "u", DisplayName: "M", Description: "d",
				Status: adminapi.ModelStatusDeprecated, Availability: adminapi.ModelAvailabilitySelected, Capabilities: []string{"text", "tools"}, ContextWindow: 8192, MaxOutputTokens: 1024}})
		check(t, call(t, "createModel", "model", "create", "openai/gpt-x"), adminapi.CreateModelInput{Body: adminapi.ModelCreate{Provider: "openai", Name: "gpt-x"}})

		// By ID, nothing is looked up; by reference the model is found with listModels.
		r := newRig(t)
		mustRun(t, r, "model", "get", mid)
		check(t, r.f.only(t, "getModel"), adminapi.GetModelInput{Model: mid})
		if n := len(r.f.calls("listModels")); n != 0 {
			t.Errorf("an ID was looked up: %d calls of listModels", n)
		}
		r = withModel(t)
		mustRun(t, r, "model", "get", "openai/gpt-4o")
		check(t, r.f.only(t, "listModels"), adminapi.ListModelsInput{Provider: "openai", Name: "gpt-4o", Limit: 50, Sort: adminapi.SortCreatedAt})
		check(t, r.f.only(t, "getModel"), adminapi.GetModelInput{Model: mid})
		// A name with slashes is split at the first one.
		r = withModel(t)
		r.f.out["listModels"] = &adminapi.ModelList{Items: []adminapi.Model{{ID: mid, Provider: "ollama", Name: "meta-llama/Llama-3"}}}
		mustRun(t, r, "model", "get", "ollama/meta-llama/Llama-3")
		check(t, r.f.only(t, "listModels"), adminapi.ListModelsInput{Provider: "ollama", Name: "meta-llama/Llama-3", Limit: 50, Sort: adminapi.SortCreatedAt})
		check(t, r.f.only(t, "getModel"), adminapi.GetModelInput{Model: mid})

		for _, c := range []struct {
			id   string
			args []string
			want any
		}{
			{"deleteModel", []string{"model", "delete"}, adminapi.DeleteModelInput{Model: mid}},
			{"listModelGrants", []string{"model", "grants", "--limit", "2"}, adminapi.ListModelGrantsInput{Model: mid, Limit: 2}},
			{"grantModel", []string{"model", "grant", "--tenant", tid}, adminapi.GrantModelInput{Model: mid, Tenant: tid}},
			{"revokeModelGrant", []string{"model", "revoke-grant", "--tenant", tid}, adminapi.RevokeModelGrantInput{Model: mid, Tenant: tid}},
		} {
			// every command takes the model as an ID or as a reference
			for _, ref := range []string{mid, "openai/gpt-4o"} {
				r := withModel(t)
				mustRun(t, r, append(append([]string(nil), c.args...), ref)...)
				check(t, r.f.only(t, c.id), c.want)
			}
		}
		// A tenant by name is resolved like everywhere else.
		r = withModel(t)
		r.f.out["listTenants"] = &adminapi.TenantList{Items: []adminapi.Tenant{tenant}}
		mustRun(t, r, "model", "grant", "--tenant", "acme", mid)
		check(t, r.f.only(t, "grantModel"), adminapi.GrantModelInput{Model: mid, Tenant: tid})

		// update: absent is not sent, empty clears, 0 clears a bound, the capabilities are replaced.
		check(t, call(t, "updateModel", "model", "update", "--status", "retired", mid), adminapi.UpdateModelInput{Model: mid, Body: adminapi.ModelUpdate{Status: adminapi.Some(adminapi.ModelStatusRetired)}})
		check(t, call(t, "updateModel", "model", "update", "--description", "", "--display-name", "", "--upstream-id", "u2", "--context-window", "0", "--max-output-tokens", "0",
			"--availability", "all", "--no-capabilities", mid),
			adminapi.UpdateModelInput{Model: mid, Body: adminapi.ModelUpdate{Description: adminapi.Some(""), DisplayName: adminapi.Some(""), UpstreamID: adminapi.Some("u2"),
				ContextWindow: adminapi.Some(0), MaxOutputTokens: adminapi.Some(0), Availability: adminapi.Some(adminapi.ModelAvailabilityAll), Capabilities: adminapi.Some([]string{})}})
		check(t, call(t, "updateModel", "model", "update", "--capability", "text", "--capability", "vision", mid),
			adminapi.UpdateModelInput{Model: mid, Body: adminapi.ModelUpdate{Capabilities: adminapi.Some([]string{"text", "vision"})}})

		// A reference that names nothing is a failure (exit 1) and nothing else is called.
		r = newRig(t)
		if code, out, errs := r.run(t, "model", "get", "openai/no-such-model"); code != 1 || out != "" || !strings.Contains(errs, "no model") {
			t.Errorf("an unknown reference: exit %d, stdout %q, stderr %q", code, out, errs)
		}
		if n := len(r.f.calls("getModel")); n != 0 {
			t.Errorf("getModel was called %d times for a reference that names nothing", n)
		}
		// A model that is neither an ID nor a reference, and bad flags, are usage errors.
		for name, args := range map[string][]string{
			"neither an ID nor a reference": {"model", "get", "gpt-4o"},
			"create without a reference":    {"model", "create"},
			"create with a bare name":       {"model", "create", "gpt-x"},
			"create with a bad status":      {"model", "create", "--status", "gone", "openai/gpt-x"},
			"create with a bad window":      {"model", "create", "--context-window", "many", "openai/gpt-x"},
			"update that changes nothing":   {"model", "update", mid},
			"grant without a tenant":        {"model", "grant", mid},
		} {
			r := newRig(t)
			if name == "grant without a tenant" {
				// a platform key has no tenant of its own to fall back on
				r.f.out["getMe"] = &adminapi.Me{}
			}
			if code, out, errs := r.run(t, args...); code != 2 || out != "" || errs == "" {
				t.Errorf("%s: exit %d, stdout %q, stderr %q, want 2", name, code, out, errs)
			}
		}
	})

	t.Run("price", func(t *testing.T) {
		for _, ref := range []string{mid, "openai/gpt-4o"} {
			r := withModel(t)
			mustRun(t, r, "price", "list", "--at", "2026-11-14T12:00:00Z", "--limit", "2", ref)
			check(t, r.f.only(t, "listPriceVersions"), adminapi.ListPriceVersionsInput{Model: mid, At: "2026-11-14T12:00:00Z", Limit: 2})
		}
		check(t, call(t, "listPriceVersions", "price", "list", mid), adminapi.ListPriceVersionsInput{Model: mid, Limit: 50})
		check(t, call(t, "deletePriceVersion", "price", "delete", mid, pvid), adminapi.DeletePriceVersionInput{Model: mid, PriceVersion: pvid})
		check(t, call(t, "clearPriceOverride", "price", "clear-override", "--reason", "why", mid), adminapi.ClearPriceOverrideInput{Model: mid, Body: adminapi.Reason{Reason: "why"}})
		check(t, call(t, "clearPriceOverride", "price", "clear-override", mid), adminapi.ClearPriceOverrideInput{Model: mid})

		// An override: rates stay decimal text, in the order given; a tier is category@threshold.
		check(t, call(t, "createPriceOverride", "price", "override", "--currency", "EUR", "--valid-from", t1.Format(time.RFC3339), "--note", "n",
			"--rate", "input=2.5", "--rate", "output=10.000000000001", "--rate", "input@100000=5", "--rate", "cache_read=0", mid),
			adminapi.CreatePriceOverrideInput{Model: mid, Body: adminapi.PriceOverride{Currency: "EUR", ValidFrom: &t1, Note: "n", Rates: []adminapi.PriceRate{
				{Category: adminapi.PriceCategoryInput, Threshold: 0, Rate: "2.5"},
				{Category: adminapi.PriceCategoryOutput, Threshold: 0, Rate: "10.000000000001"},
				{Category: adminapi.PriceCategoryInput, Threshold: 100000, Rate: "5"},
				{Category: adminapi.PriceCategoryCacheRead, Threshold: 0, Rate: "0"},
			}}})
		check(t, call(t, "createPriceOverride", "price", "override", "--currency", "USD", "--rate", "input=1", mid),
			adminapi.CreatePriceOverrideInput{Model: mid, Body: adminapi.PriceOverride{Currency: "USD", Rates: []adminapi.PriceRate{{Category: adminapi.PriceCategoryInput, Rate: "1"}}}})

		r := newRig(t)
		for name, args := range map[string][]string{
			"override without a currency":   {"price", "override", "--rate", "input=1", mid},
			"override without a rate":       {"price", "override", "--currency", "USD", mid},
			"a rate without a value":        {"price", "override", "--currency", "USD", "--rate", "input", mid},
			"a rate without a category":     {"price", "override", "--currency", "USD", "--rate", "=1", mid},
			"a threshold that is no number": {"price", "override", "--currency", "USD", "--rate", "input@many=1", mid},
			"a bad time":                    {"price", "override", "--currency", "USD", "--rate", "input=1", "--valid-from", "tomorrow", mid},
			"delete without a version":      {"price", "delete", mid},
		} {
			if code, out, errs := r.run(t, args...); code != 2 || out != "" || errs == "" {
				t.Errorf("%s: exit %d, stdout %q, stderr %q, want 2", name, code, out, errs)
			}
		}
		if n := len(r.f.calls("createPriceOverride")); n != 0 {
			t.Errorf("a refused override reached the API %d times", n)
		}
	})

	t.Run("price-import", func(t *testing.T) {
		check(t, call(t, "listPriceImports", "price-import", "list", "--limit", "2"), adminapi.ListPriceImportsInput{Limit: 2})
		check(t, call(t, "getPriceImport", "price-import", "get", iid), adminapi.GetPriceImportInput{PriceImport: iid})
		check(t, call(t, "runPriceImport", "price-import", "run", "--source", "embedded", "--dry-run"),
			adminapi.RunPriceImportInput{Body: adminapi.PriceImportRun{Source: adminapi.PriceImportRunSourceEmbedded, DryRun: true}})
		check(t, call(t, "runPriceImport", "price-import", "run", "--source", "remote"),
			adminapi.RunPriceImportInput{Body: adminapi.PriceImportRun{Source: adminapi.PriceImportRunSourceRemote}})
		// The source defaults to the embedded snapshot.
		check(t, call(t, "runPriceImport", "price-import", "run"),
			adminapi.RunPriceImportInput{Body: adminapi.PriceImportRun{Source: adminapi.PriceImportRunSourceEmbedded}})
		// No URL and no file travel through the API: the only sources are the two the operator configured.
		r := newRig(t)
		for name, args := range map[string][]string{
			"a file":      {"price-import", "run", "--source", "file"},
			"a URL":       {"price-import", "run", "--url", "https://example.com/data.json"},
			"a file flag": {"price-import", "run", "--file", "data.json"},
			"get nothing": {"price-import", "get"},
		} {
			if code, out, errs := r.run(t, args...); code != 2 || out != "" || errs == "" {
				t.Errorf("%s: exit %d, stdout %q, stderr %q, want 2", name, code, out, errs)
			}
		}
		if n := len(r.f.calls("runPriceImport")); n != 0 {
			t.Errorf("a refused import reached the API %d times", n)
		}
	})

	t.Run("tenant-model and model-restriction", func(t *testing.T) {
		check(t, call(t, "listTenantModels", "tenant-model", "list", "--tenant", tid, "--limit", "2"), adminapi.ListTenantModelsInput{Tenant: tid, Limit: 2})
		check(t, call(t, "getModelRestriction", "model-restriction", "get", "--tenant", tid), adminapi.GetModelRestrictionInput{Tenant: tid})
		check(t, call(t, "clearModelRestriction", "model-restriction", "clear", "--tenant", tid), adminapi.ClearModelRestrictionInput{Tenant: tid})
		check(t, call(t, "setModelRestriction", "model-restriction", "set", "--tenant", tid, "--model", "openai/gpt-4o", "--model", "ollama/meta-llama/Llama-3"),
			adminapi.SetModelRestrictionInput{Tenant: tid, Body: adminapi.ModelRestrictionSet{Models: []string{"openai/gpt-4o", "ollama/meta-llama/Llama-3"}}})
		// An empty restriction (nothing allowed) is asked for explicitly.
		check(t, call(t, "setModelRestriction", "model-restriction", "set", "--tenant", tid, "--none"),
			adminapi.SetModelRestrictionInput{Tenant: tid, Body: adminapi.ModelRestrictionSet{Models: []string{}}})
		// The tenant of the key is the default for a tenant key.
		r := newRig(t)
		r.f.out["getMe"] = &adminapi.Me{Tenant: &adminapi.TenantRef{ID: tid, Name: "acme"}}
		mustRun(t, r, "model-restriction", "get")
		check(t, r.f.only(t, "getModelRestriction"), adminapi.GetModelRestrictionInput{Tenant: tid})

		r = newRig(t)
		for name, args := range map[string][]string{
			"set without a model":         {"model-restriction", "set", "--tenant", tid},
			"set with a model and --none": {"model-restriction", "set", "--tenant", tid, "--model", "openai/gpt-4o", "--none"},
			"set with a bare name":        {"model-restriction", "set", "--tenant", tid, "--model", "gpt-4o"},
			"get with an argument":        {"model-restriction", "get", "--tenant", tid, "extra"},
		} {
			if code, out, errs := r.run(t, args...); code != 2 || out != "" || errs == "" {
				t.Errorf("%s: exit %d, stdout %q, stderr %q, want 2", name, code, out, errs)
			}
		}
		if n := len(r.f.calls("setModelRestriction")); n != 0 {
			t.Errorf("a refused restriction reached the API %d times", n)
		}
	})

	t.Run("key create --model", func(t *testing.T) {
		check(t, call(t, "createVirtualKey", "key", "create", "--tenant", tid, "--service-account", sid, "--model", "openai/gpt-4o", "--model", "ollama/meta-llama/Llama-3"),
			adminapi.CreateVirtualKeyInput{Tenant: tid, Body: adminapi.VirtualKeyCreate{ServiceAccountID: sid, Models: []string{"openai/gpt-4o", "ollama/meta-llama/Llama-3"}}})
		// Without the flag no list is sent: a key with no list has no restriction.
		got := call(t, "createVirtualKey", "key", "create", "--tenant", tid, "--service-account", sid).(adminapi.CreateVirtualKeyInput)
		if got.Body.Models != nil {
			t.Errorf("Models = %v without --model", got.Body.Models)
		}
		r := newRig(t)
		if code, _, errs := r.run(t, "key", "create", "--tenant", tid, "--service-account", sid, "--model", "gpt-4o"); code != 2 || errs == "" {
			t.Errorf("a bare model name: exit %d, stderr %q", code, errs)
		}
		if n := len(r.f.calls("createVirtualKey")); n != 0 {
			t.Errorf("a refused key reached the API %d times", n)
		}
	})

	t.Run("credential and service account updates clear through Some", func(t *testing.T) {
		got := call(t, "updateCredential", "credential", "update", "--tenant", tid, "--description", "", cid).(adminapi.UpdateCredentialInput)
		if !got.Body.Description.Set || got.Body.Description.Value != "" {
			t.Errorf("--description \"\" sent %+v, want adminapi.Some(\"\")", got.Body.Description)
		}
		got2 := call(t, "updateServiceAccount", "service-account", "update", "--tenant", tid, "--description", "", sid).(adminapi.UpdateServiceAccountInput)
		if !got2.Body.Description.Set || got2.Body.Description.Value != "" {
			t.Errorf("--description \"\" sent %+v, want adminapi.Some(\"\")", got2.Body.Description)
		}
	})

	t.Run("output", func(t *testing.T) {
		r := newRig(t)
		r.f.out["getProvider"] = &provider
		out, _ := mustRun(t, r, "provider", "get", "acme")
		for _, line := range []string{"name: acme", "api_family: openai", "enabled: true"} {
			if !lineWith(out, line) {
				t.Errorf("%q is missing:\n%s", line, out)
			}
		}
		r.f.out["listModels"] = &adminapi.ModelList{Items: []adminapi.Model{model}}
		out, _ = mustRun(t, r, "model", "list", "-o", "json")
		if !strings.Contains(out, `"ref":"openai/gpt-4o"`) && !strings.Contains(out, `"ref": "openai/gpt-4o"`) {
			t.Errorf("the JSON output lacks the reference:\n%s", out)
		}
	})
}
