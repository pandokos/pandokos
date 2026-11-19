package resolver

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/testutil"
)

// fakeSource is what the catalogue adapter reads: the two methods of *catalog.Cache that it uses
// (the unexported interface `source`, which NewCatalogue satisfies with a *catalog.Cache). It
// serves a snapshot that need not satisfy the constraints of the database.
type fakeSource struct {
	snap   *catalog.Snapshot
	err    error
	reads  *int                 // how many times the snapshot was read, when not nil
	access catalog.TenantAccess // what the tenant's entry holds (sprint 5.4): its currency and prices
}

func (f fakeSource) read() {
	if f.reads != nil {
		*f.reads++
	}
}

func (f fakeSource) Snapshot(context.Context) (*catalog.Snapshot, error) {
	f.read()
	return f.snap, f.err
}

func (f fakeSource) Resolve(_ context.Context, p authz.Principal, provider, name string) (catalog.Model, catalog.Endpoint, error) {
	f.read()
	if f.err != nil {
		return catalog.Model{}, catalog.Endpoint{}, f.err
	}
	return f.snap.Resolve(p, catalog.TenantAccess{}, provider, name)
}

func (f fakeSource) ResolveIn(ctx context.Context, p authz.Principal, provider, name string) (*catalog.Snapshot, catalog.TenantAccess, catalog.Model, catalog.Endpoint, error) {
	m, e, err := f.Resolve(ctx, p, provider, name)
	return f.snap, f.access, m, e, err
}

func testPrincipal() authz.Principal {
	var id tenant.ID
	id[0], id[6], id[8] = 1, 0x70, 0x80
	return authz.Principal{Kind: authz.KindServiceAccount, ID: "sa", TenantID: id, TenantState: tenant.StateActive, Roles: []string{authz.RoleInference}}
}

// TestCatalogueEndpointUnusable is the case of T-440 that the database refuses to build: a stored
// URL that fails the validation of the catalogue. It is ErrProviderUnavailable, logged at error
// with the names of the provider and the endpoint, never the URL.
func TestCatalogueEndpointUnusable(t *testing.T) {
	const secretHost = "synthetic-host-marker.invalid"
	bad := map[string]struct {
		url     string
		private bool
	}{
		"plain http on a public endpoint": {"http://" + secretHost, false},
		"user information":                {"https://user:synthetic-pw@" + secretHost, false},
		"a query":                         {"https://" + secretHost + "/?token=synthetic-token", false},
		"a loopback literal, public":      {"https://127.0.0.1", false},
		"another scheme":                  {"ftp://" + secretHost, true},
		"no host":                         {"https://", false},
	}
	for name, c := range bad {
		t.Run(name, func(t *testing.T) {
			logs := &testutil.LogBuffer{}
			snap := catalog.NewSnapshot(catalog.SnapshotData{
				Providers: []catalog.Provider{{ID: "p1", Name: "synth", Family: "openai", Enabled: true}},
				Endpoints: map[string][]catalog.Endpoint{"synth": {{Name: "main", BaseURL: c.url, Private: c.private, Enabled: true, Default: true}}},
				Models:    []catalog.Model{{ID: "m1", Provider: "synth", Name: "m", UpstreamID: "m", Status: "active", Availability: "all"}},
			})
			cat := newCatalogue(fakeSource{snap: snap}, telemetry.NewLogger(logs, "debug"))
			_, err := cat.Resolve(context.Background(), testPrincipal(), "synth", "m")
			if !errors.Is(err, gateway.ErrProviderUnavailable) {
				t.Fatalf("%v, want ErrProviderUnavailable", err)
			}
			lines := logs.Find(t, "endpoint unusable")
			if len(lines) != 1 || lines[0]["level"] != "ERROR" || lines[0]["provider"] != "synth" || lines[0]["endpoint"] != "main" {
				t.Fatalf("the log lines: %v", lines)
			}
			testutil.AssertAbsent(t, "the logs", logs.String(), secretHost, "synthetic-pw", "synthetic-token", "127.0.0.1")
		})
	}

	t.Run("a valid private endpoint resolves", func(t *testing.T) {
		snap := catalog.NewSnapshot(catalog.SnapshotData{
			Providers: []catalog.Provider{{ID: "p1", Name: "synth", Family: "openai", Enabled: true}},
			Endpoints: map[string][]catalog.Endpoint{"synth": {{Name: "main", BaseURL: "http://10.0.0.5:8000/v1", Private: true, Enabled: true, Default: true}}},
			Models:    []catalog.Model{{ID: "m1", Provider: "synth", Name: "m", UpstreamID: "up-m", Status: "active", Availability: "all"}},
		})
		cat := newCatalogue(fakeSource{snap: snap}, telemetry.NewLogger(&testutil.LogBuffer{}, "debug"))
		got, err := cat.Resolve(context.Background(), testPrincipal(), "synth", "m")
		if err != nil || got.BaseURL == nil || got.BaseURL.String() != "http://10.0.0.5:8000/v1" || !got.Private || got.UpstreamModel != "up-m" || got.ModelID != "m1" {
			t.Errorf("%+v %v", got, err)
		}
	})

	t.Run("Resolve reads one snapshot for the model and the prices", func(t *testing.T) {
		snap := catalog.NewSnapshot(catalog.SnapshotData{
			Providers: []catalog.Provider{{ID: "p1", Name: "synth", Family: "openai", Enabled: true}},
			Endpoints: map[string][]catalog.Endpoint{"synth": {{Name: "main", BaseURL: "http://10.0.0.5:8000/v1", Private: true, Enabled: true, Default: true}}},
			Models:    []catalog.Model{{ID: "m1", Provider: "synth", Name: "m", UpstreamID: "up-m", Status: "active", Availability: "all"}},
		})
		reads := 0
		cat := newCatalogue(fakeSource{snap: snap, reads: &reads}, telemetry.NewLogger(&testutil.LogBuffer{}, "debug"))
		if _, err := cat.Resolve(context.Background(), testPrincipal(), "synth", "m"); err != nil || reads != 1 {
			t.Errorf("%d reads of the snapshot, error %v", reads, err)
		}
	})

	t.Run("a failure to read the snapshot is not a refusal", func(t *testing.T) {
		cat := newCatalogue(fakeSource{err: errors.New("synthetic failure")}, telemetry.NewLogger(&testutil.LogBuffer{}, "debug"))
		_, err := cat.Resolve(context.Background(), testPrincipal(), "synth", "m")
		if err == nil || errors.Is(err, gateway.ErrModelNotAllowed) || errors.Is(err, gateway.ErrProviderUnavailable) {
			t.Errorf("Resolve: %v", err)
		}
		if _, _, err := cat.Provider(context.Background(), "synth"); err == nil {
			t.Errorf("Provider: %v", err)
		}
	})
}

// TestCatalogueDefaultOutput is part of T-628 and T-682: the default output reservation of a model
// is carried to the target (sprint 5.2, section 5.10), 0 when the model has none.
func TestCatalogueDefaultOutput(t *testing.T) {
	snap := catalog.NewSnapshot(catalog.SnapshotData{
		Providers: []catalog.Provider{{ID: "p1", Name: "synth", Family: "openai", Enabled: true}},
		Endpoints: map[string][]catalog.Endpoint{"synth": {{Name: "main", BaseURL: "http://10.0.0.5:8000/v1", Private: true, Enabled: true, Default: true}}},
		Models: []catalog.Model{
			{ID: "m-default", Provider: "synth", Name: "default", UpstreamID: "default", Status: "active", Availability: "all", DefaultOutputTokens: 700},
			{ID: "m-none", Provider: "synth", Name: "none", UpstreamID: "none", Status: "active", Availability: "all"},
		},
	})
	cat := newCatalogue(fakeSource{snap: snap}, telemetry.NewLogger(&testutil.LogBuffer{}, "debug"))
	for name, want := range map[string]int64{"default": 700, "none": 0} {
		got, err := cat.Resolve(context.Background(), testPrincipal(), "synth", name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.DefaultOutputTokens != want {
			t.Errorf("%s: DefaultOutputTokens %d, want %d", name, got.DefaultOutputTokens, want)
		}
	}
}

// TestCatalogueLimitsValues is T-629: the two values the estimate needs come from the catalogue
// model, 0 when it has none.
func TestCatalogueLimitsValues(t *testing.T) {
	snap := catalog.NewSnapshot(catalog.SnapshotData{
		Providers: []catalog.Provider{{ID: "p1", Name: "synth", Family: "openai", Enabled: true}},
		Endpoints: map[string][]catalog.Endpoint{"synth": {{Name: "main", BaseURL: "http://10.0.0.5:8000/v1", Private: true, Enabled: true, Default: true}}},
		Models: []catalog.Model{
			{ID: "m-both", Provider: "synth", Name: "both", UpstreamID: "both", Status: "active", Availability: "all", ContextWindow: 200000, MaxOutputTokens: 8192},
			{ID: "m-window", Provider: "synth", Name: "window", UpstreamID: "window", Status: "active", Availability: "all", ContextWindow: 32000},
			{ID: "m-output", Provider: "synth", Name: "output", UpstreamID: "output", Status: "active", Availability: "all", MaxOutputTokens: 4096},
			{ID: "m-none", Provider: "synth", Name: "none", UpstreamID: "none", Status: "active", Availability: "all"},
		},
	})
	cat := newCatalogue(fakeSource{snap: snap}, telemetry.NewLogger(&testutil.LogBuffer{}, "debug"))
	for name, want := range map[string][2]int64{
		"both": {8192, 200000}, "window": {0, 32000}, "output": {4096, 0}, "none": {0, 0},
	} {
		got, err := cat.Resolve(context.Background(), testPrincipal(), "synth", name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.MaxOutputTokens != want[0] || got.ContextWindow != want[1] {
			t.Errorf("%s: MaxOutputTokens %d ContextWindow %d, want %d and %d", name, got.MaxOutputTokens, got.ContextWindow, want[0], want[1])
		}
	}
}

// TestCatalogueTenantPrices is T-733: the target carries the currency of the tenant, the catalogue's
// versions of the model and the tenant's versions of the resolved model only.
func TestCatalogueTenantPrices(t *testing.T) {
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	catVersion := pricing.Version{ID: "cat-1", ModelID: "m1", ValidFrom: from, Currency: "USD", Source: "import"}
	catOther := pricing.Version{ID: "cat-2", ModelID: "m2", ValidFrom: from, Currency: "USD", Source: "import"}
	own := pricing.Version{ID: "own-1", ModelID: "m1", ValidFrom: from, Currency: "EUR", Source: pricing.SourceTenant}
	ownEnd := pricing.Version{ID: "own-end", ModelID: "m1", ValidFrom: from.Add(time.Hour), Currency: "EUR", Source: pricing.SourceTenant, End: true}
	ownOther := pricing.Version{ID: "own-2", ModelID: "m2", ValidFrom: from, Currency: "EUR", Source: pricing.SourceTenant}
	snap := catalog.NewSnapshot(catalog.SnapshotData{
		Providers: []catalog.Provider{{ID: "p1", Name: "synth", Family: "openai", Enabled: true}},
		Endpoints: map[string][]catalog.Endpoint{"synth": {{Name: "main", BaseURL: "http://10.0.0.5:8000/v1", Private: true, Enabled: true, Default: true}}},
		Models: []catalog.Model{
			{ID: "m1", Provider: "synth", Name: "one", UpstreamID: "one", Status: "active", Availability: "all"},
			{ID: "m2", Provider: "synth", Name: "two", UpstreamID: "two", Status: "active", Availability: "all"},
		},
		Versions: map[string][]pricing.Version{"m1": {catVersion}, "m2": {catOther}},
	})
	active := pricing.Active{Current: "EUR", Previous: "USD", From: from.Add(24 * time.Hour)}
	access := catalog.TenantAccess{Currency: active, Prices: map[string][]pricing.Version{"m1": {own, ownEnd}, "m2": {ownOther}}}
	cat := newCatalogue(fakeSource{snap: snap, access: access}, telemetry.NewLogger(&testutil.LogBuffer{}, "debug"))

	got, err := cat.Resolve(context.Background(), testPrincipal(), "synth", "one")
	if err != nil {
		t.Fatal(err)
	}
	if got.Currency != active {
		t.Errorf("currency %+v, want the tenant's %+v", got.Currency, active)
	}
	if len(got.Prices) != 1 || got.Prices[0].ID != "cat-1" {
		t.Errorf("the catalogue's versions: %+v, want those of the model only", got.Prices)
	}
	if !reflect.DeepEqual(got.TenantPrices, []pricing.Version{own, ownEnd}) {
		t.Errorf("the tenant's versions: %+v, want those of the resolved model only", got.TenantPrices)
	}
	// The list built from the target: the catalogue's USD price before the change, none after the end of the list.
	l := got.PriceList()
	if v, src, ok := l.At(from.Add(30 * time.Minute)); !ok || v.ID != "cat-1" || src != pricing.SourceCatalogue {
		t.Errorf("before the change (USD in force): %+v %s %v", v, src, ok)
	}
	if v, src, ok := l.At(from.Add(25 * time.Hour)); ok {
		t.Errorf("after the change, the list of EUR has ended and the catalogue prices in USD: %+v %s", v, src)
	}
	eur := pricing.List{Currency: pricing.Active{Current: "EUR"}, Catalogue: got.Prices, Tenant: got.TenantPrices}
	if v, src, ok := eur.At(from.Add(30 * time.Minute)); !ok || v.ID != "own-1" || src != pricing.SourceTenant {
		t.Errorf("EUR in force: %+v %s %v", v, src, ok)
	}

	t.Run("a principal without a tenant is priced from the catalogue as before", func(t *testing.T) {
		cat := newCatalogue(fakeSource{snap: snap}, telemetry.NewLogger(&testutil.LogBuffer{}, "debug"))
		got, err := cat.Resolve(context.Background(), testPrincipal(), "synth", "two")
		if err != nil || got.Currency != (pricing.Active{}) || len(got.TenantPrices) != 0 || len(got.Prices) != 1 || got.Prices[0].ID != "cat-2" {
			t.Errorf("%+v %v", got, err)
		}
	})
}
