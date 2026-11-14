package queries_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// catalogStatements is the part of T-393 that covers the 45 statements of sprint 2.2 and the
// model columns of the key statements: providers, endpoints, models, grants, restrictions, price
// imports, versions and rates, directly and through the pooler. A value that goes in comes back
// unchanged: nullable bigints, arrays, rates as decimal text, timestamps.
func catalogStatements(c context.Context, t *testing.T, admin, gateway *store.DB, tid tenant.ID, tenantID, serviceAccountID string, hash []byte, called map[string]bool) {
	t.Helper()
	mark := func(name string) { called[name] = true }
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	i64 := func(n int64) *int64 { return &n }
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	var providerID, endpoint1, endpoint2, model1, model2 string
	var v1, v2 queries.CreatePriceVersionRow
	var importID string
	future := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Microsecond)

	must("platform catalogue statements", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)

		// Providers.
		mark("CreateProvider")
		p, err := q.CreateProvider(c, queries.CreateProviderParams{Name: "synthetic-393", DisplayName: "Synthetic, ünï", Description: "d, é", ApiFamily: "openai", Enabled: true, DatasetProvider: "synthetic"})
		if err != nil {
			return err
		}
		if p.Name != "synthetic-393" || p.DisplayName != "Synthetic, ünï" || p.ApiFamily != "openai" || !p.Enabled || p.DatasetProvider != "synthetic" || len(p.ID) != 36 || p.CreatedAt.IsZero() {
			t.Errorf("CreateProvider: %+v", p)
		}
		providerID = p.ID
		mark("GetProvider")
		got, err := q.GetProvider(c, "synthetic-393")
		if err != nil || got != p {
			t.Errorf("GetProvider: %+v %v", got, err)
		}
		mark("GetProviderForUpdate")
		if locked, err := q.GetProviderForUpdate(c, "synthetic-393"); err != nil || locked != got {
			t.Errorf("GetProviderForUpdate: %+v %v", locked, err)
		}
		if _, err := q.GetProvider(c, "no-such-provider"); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetProvider of a provider that does not exist: %v", err)
		}
		mark("ListProviders")
		all, err := q.ListProviders(c, queries.ListProvidersParams{PageSize: 10})
		if err != nil || len(all) != 3 || all[0].Name != "anthropic" || all[1].Name != "openai" || all[2].Name != "synthetic-393" {
			t.Errorf("ListProviders: %d rows, %v (the seed has anthropic and openai)", len(all), err)
		}
		if next, err := q.ListProviders(c, queries.ListProvidersParams{After: true, AfterName: "anthropic", PageSize: 1}); err != nil || len(next) != 1 || next[0].Name != "openai" {
			t.Errorf("ListProviders after the first with a page of one: %+v %v", next, err)
		}
		mark("UpdateProvider")
		upd, err := q.UpdateProvider(c, queries.UpdateProviderParams{Name: "synthetic-393", SetDisplayName: true, DisplayName: "", SetEnabled: true, Enabled: false})
		if err != nil || upd.DisplayName != "" || upd.Enabled || upd.Description != "d, é" || upd.DatasetProvider != "synthetic" {
			t.Errorf("UpdateProvider (display name cleared, disabled): %+v %v", upd, err)
		}
		if upd, err = q.UpdateProvider(c, queries.UpdateProviderParams{Name: "synthetic-393", SetEnabled: true, Enabled: true, SetDatasetProvider: true, DatasetProvider: "", SetDescription: true, Description: "x"}); err != nil ||
			!upd.Enabled || upd.DatasetProvider != "" || upd.Description != "x" {
			t.Errorf("UpdateProvider (enabled, dataset provider cleared): %+v %v", upd, err)
		}
		if upd, err = q.UpdateProvider(c, queries.UpdateProviderParams{Name: "synthetic-393"}); err != nil || upd.Description != "x" {
			t.Errorf("an update that sets nothing: %+v %v", upd, err)
		}
		if _, err := q.UpdateProvider(c, queries.UpdateProviderParams{Name: "no-such-provider", SetEnabled: true}); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("UpdateProvider of a provider that does not exist: %v", err)
		}

		// Endpoints: the first becomes the default whatever it asks for.
		mark("CreateEndpoint")
		e1, err := q.CreateEndpoint(c, queries.CreateEndpointParams{ProviderID: providerID, Name: "main", BaseUrl: "https://api.synthetic.invalid", Region: "eu", Enabled: true, Description: "first"})
		if err != nil {
			return err
		}
		if !e1.IsDefault || e1.Region != "eu" || e1.Private || !e1.Enabled || e1.BaseUrl != "https://api.synthetic.invalid" || e1.ProviderID != providerID {
			t.Errorf("CreateEndpoint (the first of the provider): %+v", e1)
		}
		endpoint1 = e1.ID
		e2, err := q.CreateEndpoint(c, queries.CreateEndpointParams{ProviderID: providerID, Name: "internal", BaseUrl: "http://10.0.0.5:8000/v1", Private: true, Enabled: true})
		if err != nil {
			return err
		}
		if e2.IsDefault || !e2.Private {
			t.Errorf("CreateEndpoint (the second): %+v", e2)
		}
		endpoint2 = e2.ID
		mark("GetEndpoint")
		if ge, err := q.GetEndpoint(c, queries.GetEndpointParams{ProviderID: providerID, Name: "internal"}); err != nil || ge != e2 {
			t.Errorf("GetEndpoint: %+v %v", ge, err)
		}
		mark("ListEndpoints")
		eps, err := q.ListEndpoints(c, queries.ListEndpointsParams{ProviderID: providerID, PageSize: 10})
		if err != nil || len(eps) != 2 || eps[0].Name != "internal" || eps[1].Name != "main" {
			t.Errorf("ListEndpoints: %+v %v", eps, err)
		}
		if next, err := q.ListEndpoints(c, queries.ListEndpointsParams{ProviderID: providerID, After: true, AfterName: "internal", PageSize: 1}); err != nil || len(next) != 1 || next[0].Name != "main" {
			t.Errorf("ListEndpoints after the first: %+v %v", next, err)
		}
		mark("ClearDefaultEndpoint")
		if n, err := q.ClearDefaultEndpoint(c, queries.ClearDefaultEndpointParams{ProviderID: providerID, Keep: "internal"}); err != nil || n != 1 {
			t.Errorf("ClearDefaultEndpoint: %d %v, want 1", n, err)
		}
		if n, err := q.ClearDefaultEndpoint(c, queries.ClearDefaultEndpointParams{ProviderID: providerID, Keep: "internal"}); err != nil || n != 0 {
			t.Errorf("a second ClearDefaultEndpoint: %d %v, want 0", n, err)
		}
		mark("UpdateEndpoint")
		ue, err := q.UpdateEndpoint(c, queries.UpdateEndpointParams{ProviderID: providerID, Name: "internal", SetIsDefault: true, IsDefault: true, SetRegion: true, Region: "us",
			SetBaseUrl: true, BaseUrl: "https://api2.synthetic.invalid", SetPrivate: true, Private: false, SetEnabled: true, Enabled: false, SetDescription: true, Description: "moved"})
		if err != nil || !ue.IsDefault || ue.Region != "us" || ue.BaseUrl != "https://api2.synthetic.invalid" || ue.Private || ue.Enabled || ue.Description != "moved" {
			t.Errorf("UpdateEndpoint: %+v %v", ue, err)
		}
		if ue, err = q.UpdateEndpoint(c, queries.UpdateEndpointParams{ProviderID: providerID, Name: "internal", SetRegion: true, Region: "", SetEnabled: true, Enabled: true}); err != nil || ue.Region != "" || !ue.Enabled || !ue.IsDefault {
			t.Errorf("UpdateEndpoint (region cleared): %+v %v", ue, err)
		}

		// Models.
		mark("CreateModel")
		m1, err := q.CreateModel(c, queries.CreateModelParams{ProviderID: providerID, Name: "synthetic-model", UpstreamID: "synthetic-model", DisplayName: "Model, ünï", Description: "d",
			Status: "active", Availability: "selected", Capabilities: []string{"text", "tools"}, ContextWindow: i64(128000)})
		if err != nil {
			return err
		}
		model1 = m1
		tick()
		m2, err := q.CreateModel(c, queries.CreateModelParams{ProviderID: providerID, Name: "meta/synthetic-slash", UpstreamID: "upstream/slash", Status: "deprecated", Availability: "all", Capabilities: []string{}, MaxOutputTokens: i64(4096)})
		if err != nil {
			return err
		}
		model2 = m2
		mark("GetModel")
		gm, err := q.GetModel(c, model1)
		if err != nil || gm.ID != model1 || gm.Provider != "synthetic-393" || gm.ApiFamily != "openai" || gm.Name != "synthetic-model" || gm.UpstreamID != "synthetic-model" ||
			gm.DisplayName != "Model, ünï" || gm.Status != "active" || gm.Availability != "selected" || !reflect.DeepEqual(gm.Capabilities, []string{"text", "tools"}) ||
			gm.ContextWindow == nil || *gm.ContextWindow != 128000 || gm.MaxOutputTokens != nil || gm.PriceOverride || gm.CreatedAt.IsZero() {
			t.Errorf("GetModel: %+v %v (a null bound comes back as nil)", gm, err)
		}
		if g2, err := q.GetModel(c, model2); err != nil || g2.ContextWindow != nil || g2.MaxOutputTokens == nil || *g2.MaxOutputTokens != 4096 || g2.Capabilities == nil || len(g2.Capabilities) != 0 || g2.Name != "meta/synthetic-slash" {
			t.Errorf("GetModel (the second): %+v %v", g2, err)
		}
		if _, err := q.GetModel(c, "0192f0f0-0000-7000-8000-000000000bad"); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetModel of a model that does not exist: %v", err)
		}
		mark("ListModels")
		for _, sortBy := range []string{"created_at", "-created_at"} {
			ms, err := q.ListModels(c, queries.ListModelsParams{Provider: "synthetic-393", Sort: sortBy, AfterID: zeroID, PageSize: 10})
			if err != nil || len(ms) != 2 {
				t.Fatalf("ListModels %s: %d rows, %v", sortBy, len(ms), err)
			}
			want := map[string]string{"created_at": model1, "-created_at": model2}[sortBy]
			if ms[0].ID != want {
				t.Errorf("ListModels %s: first is %s, want %s", sortBy, ms[0].ID, want)
			}
			next, err := q.ListModels(c, queries.ListModelsParams{Provider: "synthetic-393", Sort: sortBy, After: true, AfterID: ms[0].ID, PageSize: 10})
			if err != nil || len(next) != 1 || next[0].ID != ms[1].ID {
				t.Errorf("ListModels %s after the first: %+v %v", sortBy, next, err)
			}
		}
		for name, f := range map[string]struct {
			p    queries.ListModelsParams
			want int
		}{
			"the seed":         {queries.ListModelsParams{Provider: "openai"}, 12},
			"by name":          {queries.ListModelsParams{Name: "synthetic-model"}, 1},
			"by status":        {queries.ListModelsParams{Provider: "synthetic-393", Status: "deprecated"}, 1},
			"by availability":  {queries.ListModelsParams{Provider: "synthetic-393", Availability: "all"}, 1},
			"all of them":      {queries.ListModelsParams{}, 19},
			"nothing":          {queries.ListModelsParams{Provider: "synthetic-393", Status: "retired"}, 0},
			"another provider": {queries.ListModelsParams{Provider: "anthropic"}, 5},
		} {
			f.p.Sort, f.p.AfterID, f.p.PageSize = "created_at", zeroID, 100
			ms, err := q.ListModels(c, f.p)
			if err != nil || len(ms) != f.want {
				t.Errorf("ListModels %s: %d rows, %v, want %d", name, len(ms), err, f.want)
			}
		}
		mark("LockModel")
		if lm, err := q.LockModel(c, model1); err != nil || lm != (queries.LockModelRow{ID: model1, Name: "synthetic-model", PriceOverride: false}) {
			t.Errorf("LockModel: %+v %v", lm, err)
		}
		mark("UpdateModel")
		if n, err := q.UpdateModel(c, queries.UpdateModelParams{ID: model1, SetUpstreamID: true, UpstreamID: "synthetic-model-v2", SetDisplayName: true, DisplayName: "", SetDescription: true, Description: "new",
			SetStatus: true, Status: "deprecated", SetAvailability: true, Availability: "all", SetCapabilities: true, Capabilities: []string{"vision"},
			SetContextWindow: true, ContextWindow: nil, SetMaxOutputTokens: true, MaxOutputTokens: i64(2048)}); err != nil || n != 1 {
			t.Fatalf("UpdateModel: %d %v", n, err)
		}
		if gm, err = q.GetModel(c, model1); err != nil || gm.UpstreamID != "synthetic-model-v2" || gm.DisplayName != "" || gm.Description != "new" || gm.Status != "deprecated" || gm.Availability != "all" ||
			!reflect.DeepEqual(gm.Capabilities, []string{"vision"}) || gm.ContextWindow != nil || gm.MaxOutputTokens == nil || *gm.MaxOutputTokens != 2048 {
			t.Errorf("GetModel after UpdateModel: %+v %v (the context window was cleared to null)", gm, err)
		}
		if n, err := q.UpdateModel(c, queries.UpdateModelParams{ID: model1, SetAvailability: true, Availability: "selected", SetStatus: true, Status: "active"}); err != nil || n != 1 {
			t.Errorf("UpdateModel (availability, status): %d %v", n, err)
		}
		if n, err := q.UpdateModel(c, queries.UpdateModelParams{ID: "0192f0f0-0000-7000-8000-000000000bad", SetStatus: true, Status: "active"}); err != nil || n != 0 {
			t.Errorf("UpdateModel of a model that does not exist: %d %v", n, err)
		}
		mark("SetModelPriceOverride")
		if n, err := q.SetModelPriceOverride(c, queries.SetModelPriceOverrideParams{ID: model1, PriceOverride: true}); err != nil || n != 1 {
			t.Errorf("SetModelPriceOverride: %d %v", n, err)
		}
		if lm, err := q.LockModel(c, model1); err != nil || !lm.PriceOverride {
			t.Errorf("LockModel after the override flag: %+v %v", lm, err)
		}
		if n, err := q.SetModelPriceOverride(c, queries.SetModelPriceOverrideParams{ID: model1, PriceOverride: false}); err != nil || n != 1 {
			t.Errorf("SetModelPriceOverride back: %d %v", n, err)
		}

		// Grants.
		mark("GrantModel")
		if n, err := q.GrantModel(c, queries.GrantModelParams{ModelID: model1, TenantID: tenantID}); err != nil || n != 1 {
			t.Errorf("GrantModel: %d %v", n, err)
		}
		if n, err := q.GrantModel(c, queries.GrantModelParams{ModelID: model1, TenantID: tenantID}); err != nil || n != 0 {
			t.Errorf("GrantModel again (idempotent): %d %v", n, err)
		}
		mark("GetModelGrant")
		g, err := q.GetModelGrant(c, queries.GetModelGrantParams{ModelID: model1, TenantID: tenantID})
		if err != nil || g.ModelID != model1 || g.TenantID != tenantID || g.TenantName != "tenant-230" || g.CreatedAt.IsZero() {
			t.Errorf("GetModelGrant: %+v %v", g, err)
		}
		mark("ListModelGrants")
		gs, err := q.ListModelGrants(c, queries.ListModelGrantsParams{ModelID: model1, AfterID: zeroID, PageSize: 10})
		if err != nil || len(gs) != 1 || gs[0].TenantID != tenantID || gs[0].TenantName != "tenant-230" {
			t.Errorf("ListModelGrants: %+v %v", gs, err)
		}
		if none, err := q.ListModelGrants(c, queries.ListModelGrantsParams{ModelID: model1, After: true, AfterID: tenantID, PageSize: 10}); err != nil || len(none) != 0 {
			t.Errorf("ListModelGrants after the only grant: %+v %v", none, err)
		}
		mark("RevokeModelGrant")
		if n, err := q.RevokeModelGrant(c, queries.RevokeModelGrantParams{ModelID: model1, TenantID: tenantID}); err != nil || n != 1 {
			t.Errorf("RevokeModelGrant: %d %v", n, err)
		}
		if n, err := q.RevokeModelGrant(c, queries.RevokeModelGrantParams{ModelID: model1, TenantID: tenantID}); err != nil || n != 0 {
			t.Errorf("RevokeModelGrant again: %d %v", n, err)
		}
		if _, err := q.GetModelGrant(c, queries.GetModelGrantParams{ModelID: model1, TenantID: tenantID}); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetModelGrant of a revoked grant: %v", err)
		}
		if n, err := q.GrantModel(c, queries.GrantModelParams{ModelID: model1, TenantID: tenantID}); err != nil || n != 1 {
			t.Errorf("GrantModel again after the revocation: %d %v", n, err)
		}

		// Price imports.
		mark("LockPriceImports")
		if err := q.LockPriceImports(c); err != nil {
			t.Errorf("LockPriceImports: %v", err)
		}
		mark("ClockTimestamp")
		if at, err := q.ClockTimestamp(c); err != nil || time.Since(at) > time.Minute || time.Until(at) > time.Minute {
			t.Errorf("ClockTimestamp: %v %v", at, err)
		}
		mark("PriceImportHistory")
		hist, err := q.PriceImportHistory(c, queries.PriceImportHistoryParams{Digest: digest, Since: time.Now().Add(-time.Hour)})
		if err != nil {
			t.Errorf("PriceImportHistory with no import at all: %v (a '-infinity' of last_remote cannot be scanned into a time.Time)", err)
		} else if hist.DigestSeen || hist.NewerImport || hist.LastRemote.After(time.Unix(0, 0)) {
			t.Errorf("PriceImportHistory with no import at all: %+v", hist)
		}
		mark("CreatePriceImport")
		started := time.Now().Add(-time.Second).UTC().Truncate(time.Microsecond)
		imp, err := q.CreatePriceImport(c, queries.CreatePriceImportParams{StartedAt: started, Source: "embedded", SourceRef: "", DatasetFormat: "genai-prices-v2", Digest: digest, Outcome: "success",
			ActorType: "host", ActorID: "pandokos", ModelsSeen: 19, VersionsCreated: 2, Unchanged: 1, Overridden: 3, Unmatched: 4, Skipped: 5, Report: `{"items":[{"model":"ünï","outcome":"created"}],"truncated":0}`})
		if err != nil || len(imp.ID) != 36 || imp.CreatedAt.IsZero() {
			t.Fatalf("CreatePriceImport: %+v %v", imp, err)
		}
		importID = imp.ID
		hist, err = q.PriceImportHistory(c, queries.PriceImportHistoryParams{Digest: digest, Since: time.Now().Add(-time.Hour)})
		if err != nil || !hist.DigestSeen || hist.NewerImport {
			t.Errorf("PriceImportHistory after an embedded import: %+v %v", hist, err)
		}
		if hist, err = q.PriceImportHistory(c, queries.PriceImportHistoryParams{Digest: digest[:63] + "0", Since: time.Now().Add(-time.Hour)}); err != nil || hist.DigestSeen {
			t.Errorf("PriceImportHistory for another digest: %+v %v", hist, err)
		}
		tick()
		if _, err := q.CreatePriceImport(c, queries.CreatePriceImportParams{StartedAt: started, Source: "file", SourceRef: "prices.json", DatasetFormat: "genai-prices-v2", Digest: digest[:63] + "e", Outcome: "success",
			ActorType: "host", Report: `{}`}); err != nil {
			return err
		}
		if hist, err = q.PriceImportHistory(c, queries.PriceImportHistoryParams{Digest: "", Since: time.Now().Add(-time.Hour)}); err != nil || !hist.NewerImport {
			t.Errorf("PriceImportHistory after a file import: %+v %v", hist, err)
		}
		if hist, err = q.PriceImportHistory(c, queries.PriceImportHistoryParams{Digest: "", Since: time.Now().Add(time.Hour)}); err != nil || hist.NewerImport {
			t.Errorf("PriceImportHistory with a later 'since': %+v %v", hist, err)
		}
		tick()
		if _, err := q.CreatePriceImport(c, queries.CreatePriceImportParams{StartedAt: started, Source: "remote", SourceRef: "https://prices.invalid/data.json", DatasetFormat: "genai-prices-v2", Digest: digest[:62] + "dd", Outcome: "failure",
			ErrorClass: "status", ActorType: "host", Report: `{}`}); err != nil {
			return err
		}
		if hist, err = q.PriceImportHistory(c, queries.PriceImportHistoryParams{Digest: digest[:62] + "dd", Since: time.Now().Add(time.Hour)}); err != nil || hist.DigestSeen || hist.LastRemote.After(time.Unix(0, 0)) {
			t.Errorf("PriceImportHistory after a failed remote import (neither seen nor a remote success): %+v %v", hist, err)
		}
		mark("ListPriceImports")
		imps, err := q.ListPriceImports(c, queries.ListPriceImportsParams{AfterID: zeroID, PageSize: 10})
		if err != nil || len(imps) != 3 || imps[0].Source != "remote" || imps[2].ID != importID {
			t.Errorf("ListPriceImports (newest first): %+v %v", imps, err)
		}
		if next, err := q.ListPriceImports(c, queries.ListPriceImportsParams{After: true, AfterID: imps[0].ID, PageSize: 1}); err != nil || len(next) != 1 || next[0].ID != imps[1].ID {
			t.Errorf("ListPriceImports after the first: %+v %v", next, err)
		}
		mark("GetPriceImport")
		gi, err := q.GetPriceImport(c, importID)
		if err != nil || gi.ID != importID || gi.Source != "embedded" || gi.Digest != digest || gi.Outcome != "success" || gi.ActorType != "host" || gi.ModelsSeen != 19 || gi.VersionsCreated != 2 ||
			gi.Unchanged != 1 || gi.Overridden != 3 || gi.Unmatched != 4 || gi.Skipped != 5 || !gi.StartedAt.Equal(started) {
			t.Errorf("GetPriceImport: %+v %v", gi, err)
		}
		if gi.Report == "" || !reflect.DeepEqual(jsonOf(t, gi.Report), jsonOf(t, `{"items":[{"model":"ünï","outcome":"created"}],"truncated":0}`)) {
			t.Errorf("the report came back as %s", gi.Report)
		}

		// Price versions and rates.
		mark("PriceAt")
		if _, err := q.PriceAt(c, queries.PriceAtParams{ModelID: model1}); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("PriceAt of a model without a version: %v", err)
		}
		mark("CreatePriceVersion")
		if v1, err = q.CreatePriceVersion(c, queries.CreatePriceVersionParams{ModelID: model1, Currency: "USD", Source: "import", ImportID: &importID, Note: "n, é"}); err != nil {
			return err
		}
		if len(v1.ID) != 36 || v1.ValidFrom.IsZero() || v1.CreatedAt.IsZero() {
			t.Errorf("CreatePriceVersion: %+v", v1)
		}
		mark("CreatePriceRates")
		if n, err := q.CreatePriceRates(c, queries.CreatePriceRatesParams{PriceVersionID: v1.ID, Categories: []string{"output", "input", "input"}, Thresholds: []int64{0, 0, 100000}, Rates: []string{"10", "2.5", "5.000000000001"}}); err != nil || n != 3 {
			t.Fatalf("CreatePriceRates: %d %v", n, err)
		}
		if v2, err = q.CreatePriceVersion(c, queries.CreatePriceVersionParams{ModelID: model1, ValidFrom: &future, Currency: "EUR", Source: "override"}); err != nil {
			return err
		}
		if !v2.ValidFrom.Equal(future) {
			t.Errorf("the future version starts at %v, want %v", v2.ValidFrom, future)
		}
		if _, err := q.CreatePriceRates(c, queries.CreatePriceRatesParams{PriceVersionID: v2.ID, Categories: []string{"input"}, Thresholds: []int64{0}, Rates: []string{"0.000000000001"}}); err != nil {
			return err
		}
		pa, err := q.PriceAt(c, queries.PriceAtParams{ModelID: model1})
		if err != nil || pa.ID != v1.ID || pa.Currency != "USD" || pa.Source != "import" || pa.ImportID == nil || *pa.ImportID != importID || pa.Note != "n, é" {
			t.Fatalf("PriceAt now: %+v %v", pa, err)
		}
		if !reflect.DeepEqual(pa.Categories, []string{"input", "input", "output"}) || !reflect.DeepEqual(pa.Thresholds, []int64{0, 100000, 0}) ||
			!reflect.DeepEqual(pa.Rates, []string{"2.500000000000", "5.000000000001", "10.000000000000"}) {
			t.Errorf("rates %v %v %v, want input 0 2.5, input 100000 5.000000000001, output 0 10 (ordered by category, then threshold; numeric(24,12) text)", pa.Categories, pa.Thresholds, pa.Rates)
		}
		beyond := future.Add(time.Minute)
		if pb, err := q.PriceAt(c, queries.PriceAtParams{ModelID: model1, At: &beyond}); err != nil || pb.ID != v2.ID || pb.Currency != "EUR" || pb.ImportID != nil || len(pb.Rates) != 1 || pb.Rates[0] != "0.000000000001" {
			t.Errorf("PriceAt after the future version starts: %+v %v", pb, err)
		}
		before := v1.ValidFrom.Add(-time.Microsecond)
		if _, err := q.PriceAt(c, queries.PriceAtParams{ModelID: model1, At: &before}); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("PriceAt before the first version: %v", err)
		}
		mark("GetPriceVersion")
		gv, err := q.GetPriceVersion(c, queries.GetPriceVersionParams{ID: v2.ID, ModelID: model1})
		if err != nil || gv.ID != v2.ID || gv.ModelID != model1 || gv.Currency != "EUR" || gv.Source != "override" || gv.Note != "" || len(gv.Rates) != 1 {
			t.Errorf("GetPriceVersion: %+v %v", gv, err)
		}
		if _, err := q.GetPriceVersion(c, queries.GetPriceVersionParams{ID: v2.ID, ModelID: model2}); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetPriceVersion under another model: %v", err)
		}
		mark("ListPriceVersions")
		vs, err := q.ListPriceVersions(c, queries.ListPriceVersionsParams{ModelID: model1, PageSize: 10})
		if err != nil || len(vs) != 2 || vs[0].ID != v2.ID || vs[1].ID != v1.ID {
			t.Errorf("ListPriceVersions (newest first): %+v %v", vs, err)
		}
		if next, err := q.ListPriceVersions(c, queries.ListPriceVersionsParams{ModelID: model1, After: true, AfterValidFrom: vs[0].ValidFrom, PageSize: 10}); err != nil || len(next) != 1 || next[0].ID != v1.ID {
			t.Errorf("ListPriceVersions after the first: %+v %v", next, err)
		}
		if none, err := q.ListPriceVersions(c, queries.ListPriceVersionsParams{ModelID: model2, PageSize: 10}); err != nil || len(none) != 0 {
			t.Errorf("ListPriceVersions of a model without versions: %+v %v", none, err)
		}
		mark("HasFuturePriceVersion")
		if has, err := q.HasFuturePriceVersion(c, model1); err != nil || !has {
			t.Errorf("HasFuturePriceVersion: %v %v, want true", has, err)
		}
		if has, err := q.HasFuturePriceVersion(c, model2); err != nil || has {
			t.Errorf("HasFuturePriceVersion of a model without versions: %v %v", has, err)
		}
		return nil
	}))

	// The reads of the gateway role, in a tenant session and with no scope.
	var loaded []queries.LoadPriceVersionsRow
	must("gateway catalogue statements", gateway.Unscoped(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("LoadProviders")
		ps, err := q.LoadProviders(c)
		if err != nil || len(ps) != 3 || ps[0].Name != "anthropic" || ps[2].Name != "synthetic-393" || ps[2].ID != providerID || !ps[2].Enabled || ps[2].ApiFamily != "openai" {
			t.Errorf("LoadProviders: %+v %v", ps, err)
		}
		mark("LoadEndpoints")
		es, err := q.LoadEndpoints(c)
		if err != nil || len(es) != 4 {
			t.Errorf("LoadEndpoints: %d rows, %v (two of the seed, two of this test)", len(es), err)
		}
		mine := 0
		for _, e := range es {
			if e.ProviderID == providerID {
				mine++
				if (e.Name == "internal") != e.IsDefault {
					t.Errorf("LoadEndpoints: %+v", e)
				}
			}
		}
		if mine != 2 {
			t.Errorf("LoadEndpoints: %d endpoints of the provider", mine)
		}
		mark("LoadModels")
		ms, err := q.LoadModels(c)
		if err != nil || len(ms) != 19 {
			t.Errorf("LoadModels: %d rows, %v", len(ms), err)
		}
		for _, m := range ms {
			if m.ID == model1 && (m.ProviderID != providerID || m.Name != "synthetic-model" || m.UpstreamID != "synthetic-model-v2" || m.Status != "active" || m.Availability != "selected" || m.ContextWindow != nil || m.MaxOutputTokens == nil) {
				t.Errorf("LoadModels: %+v", m)
			}
		}
		mark("LoadPriceVersions")
		if loaded, err = q.LoadPriceVersions(c); err != nil || len(loaded) != 2 {
			t.Errorf("LoadPriceVersions: %+v %v", loaded, err)
		} else if loaded[0].ID != v1.ID || loaded[1].ID != v2.ID || len(loaded[0].Rates) != 3 || loaded[0].Currency != "USD" {
			t.Errorf("LoadPriceVersions (by model, then valid_from): %+v", loaded)
		}
		return nil
	}))

	// Tenant statements: the restriction and the grants, in the tenant's own session.
	for _, db := range []*store.DB{admin, gateway} {
		must("tenant reads", db.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
			q := queries.New(tx)
			mark("ListTenantGrants")
			if gs, err := q.ListTenantGrants(c); err != nil || !reflect.DeepEqual(gs, []string{model1}) {
				t.Errorf("ListTenantGrants as %s: %v %v", db.Role(), gs, err)
			}
			mark("ListVisibleModels")
			vm, err := q.ListVisibleModels(c, queries.ListVisibleModelsParams{AfterID: zeroID, PageSize: 100})
			if err != nil {
				return err
			}
			// 17 seeded models are available to all; the granted model1 is visible; model2 is
			// deprecated but available to all.
			if len(vm) != 19 {
				t.Errorf("ListVisibleModels as %s: %d models, want 19", db.Role(), len(vm))
			}
			if one, err := q.ListVisibleModels(c, queries.ListVisibleModelsParams{Provider: "synthetic-393", AfterID: zeroID, PageSize: 100}); err != nil || len(one) != 2 {
				t.Errorf("ListVisibleModels of one provider: %+v %v", one, err)
			}
			if page, err := q.ListVisibleModels(c, queries.ListVisibleModelsParams{After: true, AfterID: vm[0].ID, PageSize: 1}); err != nil || len(page) != 1 || page[0].ID != vm[1].ID {
				t.Errorf("ListVisibleModels after the first with a page of one: %+v %v", page, err)
			}
			return nil
		}))
	}
	must("restrictions", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("GetModelRestriction")
		if _, err := q.GetModelRestriction(c); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetModelRestriction with no restriction: %v", err)
		}
		mark("SetModelRestriction")
		refs := []string{"openai/gpt-4o", "synthetic-393/meta/synthetic-slash", "ünï/é"}
		r, err := q.SetModelRestriction(c, queries.SetModelRestrictionParams{TenantID: tenantID, Models: refs})
		if err != nil || !reflect.DeepEqual(r.Models, refs) || r.UpdatedAt.IsZero() {
			t.Errorf("SetModelRestriction: %+v %v", r, err)
		}
		if r, err = q.SetModelRestriction(c, queries.SetModelRestrictionParams{TenantID: tenantID, Models: []string{}}); err != nil || r.Models == nil || len(r.Models) != 0 {
			t.Errorf("SetModelRestriction replaced by an empty list: %+v %v (an empty restriction allows nothing; it is not no restriction)", r, err)
		}
		if r, err = q.SetModelRestriction(c, queries.SetModelRestrictionParams{TenantID: tenantID, Models: refs[:1]}); err != nil || !reflect.DeepEqual(r.Models, refs[:1]) {
			t.Errorf("SetModelRestriction again: %+v %v", r, err)
		}
		gr, err := q.GetModelRestriction(c)
		if err != nil || !reflect.DeepEqual(gr.Models, refs[:1]) || gr.UpdatedAt.IsZero() {
			t.Errorf("GetModelRestriction: %+v %v", gr, err)
		}
		return nil
	}))
	must("the gateway reads the restriction", gateway.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		if gr, err := queries.New(tx).GetModelRestriction(c); err != nil || !reflect.DeepEqual(gr.Models, []string{"openai/gpt-4o"}) {
			t.Errorf("GetModelRestriction by the gateway: %+v %v", gr, err)
		}
		return nil
	}))
	must("restriction cleared", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("ClearModelRestriction")
		if n, err := q.ClearModelRestriction(c); err != nil || n != 1 {
			t.Errorf("ClearModelRestriction: %d %v", n, err)
		}
		if n, err := q.ClearModelRestriction(c); err != nil || n != 0 {
			t.Errorf("ClearModelRestriction again: %d %v", n, err)
		}
		if _, err := q.GetModelRestriction(c); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetModelRestriction after the clearing: %v", err)
		}

		// The key statements carry the model list: a key with none, and one with an empty list.
		list := []string{"openai/gpt-4o", "synthetic-393/meta/synthetic-slash"}
		k, err := queries.New(tx).CreateVirtualKey(c, queries.CreateVirtualKeyParams{TenantID: tenantID, ServiceAccountID: serviceAccountID, KeyID: "tmodelkeyaaaaaaaa", SecretHash: hash, ModelsRestricted: true, Models: list})
		if err != nil || !k.ModelsRestricted || !reflect.DeepEqual(k.Models, list) {
			t.Errorf("CreateVirtualKey with a model list: %+v %v", k, err)
		}
		empty, err := queries.New(tx).CreateVirtualKey(c, queries.CreateVirtualKeyParams{TenantID: tenantID, ServiceAccountID: serviceAccountID, KeyID: "temptykeyaaaaaaaa", SecretHash: hash, ModelsRestricted: true, Models: []string{}})
		if err != nil || !empty.ModelsRestricted || empty.Models == nil || len(empty.Models) != 0 {
			t.Errorf("CreateVirtualKey with an empty model list: %+v %v (an empty list restricts to nothing)", empty, err)
		}
		none, err := queries.New(tx).CreateVirtualKey(c, queries.CreateVirtualKeyParams{TenantID: tenantID, ServiceAccountID: serviceAccountID, KeyID: "tfreekeyaaaaaaaaa", SecretHash: hash})
		if err != nil || none.ModelsRestricted || len(none.Models) != 0 {
			t.Errorf("CreateVirtualKey without a model list: %+v %v", none, err)
		}
		if gk, err := queries.New(tx).GetVirtualKey(c, "tmodelkeyaaaaaaaa"); err != nil || !gk.ModelsRestricted || !reflect.DeepEqual(gk.Models, list) {
			t.Errorf("GetVirtualKey: %+v %v", gk.Models, err)
		}
		return nil
	}))
	must("the gateway reads the model list of a key", gateway.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		p, err := queries.New(tx).GetKeyPrincipal(c, "tmodelkeyaaaaaaaa")
		if err != nil || !p.ModelsRestricted || len(p.Models) != 2 {
			t.Errorf("GetKeyPrincipal: %+v %v", p, err)
		}
		p, err = queries.New(tx).GetKeyPrincipal(c, "tfreekeyaaaaaaaaa")
		if err != nil || p.ModelsRestricted {
			t.Errorf("GetKeyPrincipal of a key with no list: %+v %v", p, err)
		}
		return nil
	}))

	// Deleting: a future version with its rates, an endpoint, a model without prices, a provider.
	must("deletes", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("DeletePriceVersion")
		if n, err := q.DeletePriceVersion(c, queries.DeletePriceVersionParams{ID: v2.ID, ModelID: model2}); err != nil || n != 0 {
			t.Errorf("DeletePriceVersion under another model: %d %v", n, err)
		}
		if n, err := q.DeletePriceVersion(c, queries.DeletePriceVersionParams{ID: v2.ID, ModelID: model1}); err != nil || n != 1 {
			t.Errorf("DeletePriceVersion of a future version: %d %v", n, err)
		}
		if has, err := q.HasFuturePriceVersion(c, model1); err != nil || has {
			t.Errorf("HasFuturePriceVersion after the deletion: %v %v", has, err)
		}
		var rates int
		if err := tx.QueryRow(c, `SELECT count(*) FROM pandokos.price_rate WHERE price_version_id = $1::uuid`, v2.ID).Scan(&rates); err != nil || rates != 0 {
			t.Errorf("the rates of a deleted version: %d %v", rates, err)
		}
		mark("DeleteModel")
		if n, err := q.DeleteModel(c, model2); err != nil || n != 1 {
			t.Errorf("DeleteModel: %d %v", n, err)
		}
		if n, err := q.DeleteModel(c, model2); err != nil || n != 0 {
			t.Errorf("DeleteModel again: %d %v", n, err)
		}
		mark("DeleteEndpoint")
		de, err := q.DeleteEndpoint(c, queries.DeleteEndpointParams{ProviderID: providerID, Name: "main"})
		if err != nil || de.ID != endpoint1 || de.Name != "main" {
			t.Errorf("DeleteEndpoint: %+v %v", de, err)
		}
		if _, err := q.DeleteEndpoint(c, queries.DeleteEndpointParams{ProviderID: providerID, Name: "main"}); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("DeleteEndpoint again: %v", err)
		}
		if de, err = q.DeleteEndpoint(c, queries.DeleteEndpointParams{ProviderID: providerID, Name: "internal"}); err != nil || de.ID != endpoint2 {
			t.Errorf("DeleteEndpoint of the last: %+v %v", de, err)
		}
		return nil
	}))
	err := admin.Platform(c, func(c context.Context, tx store.Tx) error {
		_, err := queries.New(tx).DeleteProvider(c, "synthetic-393")
		if kind, _, ok := store.ConstraintViolation(err); !ok || kind != "foreign_key" {
			t.Errorf("DeleteProvider with a model: %v", err)
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("the provider with a model was deleted, or the transaction failed: %v", err)
	}
	must("a provider with nothing under it is deleted", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		if _, err := q.CreateProvider(c, queries.CreateProviderParams{Name: "synthetic-393-gone", ApiFamily: "anthropic", Enabled: true}); err != nil {
			return err
		}
		mark("DeleteProvider")
		dp, err := q.DeleteProvider(c, "synthetic-393-gone")
		if err != nil || dp.Name != "synthetic-393-gone" || dp.ApiFamily != "anthropic" {
			t.Errorf("DeleteProvider: %+v %v", dp, err)
		}
		if _, err := q.DeleteProvider(c, "synthetic-393-gone"); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("DeleteProvider again: %v", err)
		}
		return nil
	}))
}

var errRollback = errors.New("rolled back by the test")

func jsonOf(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("%q is not JSON: %v", s, err)
	}
	return v
}

// tick lets the millisecond of the identifiers of rows change: identifiers are time-ordered
// UUIDs, and the tests assert the order of creation.
func tick() { time.Sleep(3 * time.Millisecond) }
