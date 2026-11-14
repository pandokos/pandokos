package resolver

import (
	"context"
	"errors"
	"testing"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/testutil"
)

// fakeSource is what the catalogue adapter reads: the two methods of *catalog.Cache that it uses
// (the unexported interface `source`, which NewCatalogue satisfies with a *catalog.Cache). It
// serves a snapshot that need not satisfy the constraints of the database.
type fakeSource struct {
	snap *catalog.Snapshot
	err  error
}

func (f fakeSource) Snapshot(context.Context) (*catalog.Snapshot, error) { return f.snap, f.err }

func (f fakeSource) Resolve(_ context.Context, p authz.Principal, provider, name string) (catalog.Model, catalog.Endpoint, error) {
	if f.err != nil {
		return catalog.Model{}, catalog.Endpoint{}, f.err
	}
	return f.snap.Resolve(p, catalog.TenantAccess{}, provider, name)
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
