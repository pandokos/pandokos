package catalog

import (
	"errors"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/tenant"
)

func tid(b byte) tenant.ID {
	var i tenant.ID
	i[0], i[6], i[8] = b, 0x70, 0x80
	return i
}

// synthetic builds a catalogue without a database through NewSnapshot, the constructor that
// catalog offers to tests and to its own loader.
//
//	p-open     enabled, default endpoint enabled
//	p-off      disabled
//	p-noep     enabled, no endpoint
//	p-nodef    enabled, one enabled endpoint that is not the default
//	p-defoff   enabled, its default endpoint disabled
func synthetic() *Snapshot {
	return NewSnapshot(SnapshotData{
		Providers: []Provider{
			{ID: "pid-open", Name: "p-open", Family: "openai", Enabled: true},
			{ID: "pid-off", Name: "p-off", Family: "openai", Enabled: false},
			{ID: "pid-noep", Name: "p-noep", Family: "openai", Enabled: true},
			{ID: "pid-nodef", Name: "p-nodef", Family: "openai", Enabled: true},
			{ID: "pid-defoff", Name: "p-defoff", Family: "openai", Enabled: true},
		},
		Endpoints: map[string][]Endpoint{
			"p-open":   {{Name: "secondary", BaseURL: "https://b.example.com", Enabled: true}, {Name: "main", BaseURL: "https://a.example.com", Enabled: true, Default: true}},
			"p-off":    {{Name: "main", BaseURL: "https://off.example.com", Enabled: true, Default: true}},
			"p-nodef":  {{Name: "main", BaseURL: "https://nodef.example.com", Enabled: true}},
			"p-defoff": {{Name: "main", BaseURL: "https://defoff.example.com", Enabled: false, Default: true}, {Name: "other", BaseURL: "https://o.example.com", Enabled: true}},
		},
		Models: syntheticModels(),
	})
}

func syntheticModels() []Model {
	m := func(id, provider, name string, avail, status string) Model {
		return Model{ID: id, Provider: provider, Name: name, UpstreamID: name, Status: status, Availability: avail}
	}
	return []Model{
		m("m-all", "p-open", "all-model", "all", "active"),
		m("m-sel", "p-open", "sel-model", "selected", "active"),
		m("m-sel2", "p-open", "sel-model-2", "selected", "active"),
		m("m-dep", "p-open", "dep-model", "all", "deprecated"),
		m("m-ret", "p-open", "ret-model", "all", "retired"),
		m("m-ret-sel", "p-open", "ret-sel-model", "selected", "retired"),
		{ID: "m-up", Provider: "p-open", Name: "nick", UpstreamID: "upstream-id-1", Status: "active", Availability: "all", ContextWindow: 8192, MaxOutputTokens: 1024},
		m("m-slash", "p-open", "meta/llama-3", "all", "active"),
		m("m-off", "p-off", "off-model", "all", "active"),
		m("m-noep", "p-noep", "noep-model", "all", "active"),
		m("m-nodef", "p-nodef", "nodef-model", "all", "active"),
		m("m-defoff", "p-defoff", "defoff-model", "all", "active"),
	}
}

func principalOf(models []string) authz.Principal {
	return authz.Principal{Kind: authz.KindServiceAccount, ID: "sa", TenantID: tid(1), TenantState: tenant.StateActive,
		Roles: []string{authz.RoleInference}, Models: models}
}

// TestResolve is T-379: the rule of section 5.3, in its order.
func TestResolve(t *testing.T) {
	s := synthetic()
	grant := func(ids ...string) TenantAccess {
		g := map[string]bool{}
		for _, id := range ids {
			g[id] = true
		}
		return TenantAccess{Granted: g}
	}
	restrict := func(base TenantAccess, refs ...string) TenantAccess {
		a := map[string]bool{}
		for _, r := range refs {
			a[r] = true
		}
		base.Restricted, base.Allowed = true, a
		return base
	}

	type c struct {
		name     string
		p        authz.Principal
		ta       TenantAccess
		provider string
		model    string
		want     error  // nil: allowed
		wantID   string // the model returned when allowed
	}
	cases := []c{
		{name: "an available-to-all model", p: principalOf(nil), provider: "p-open", model: "all-model", wantID: "m-all"},
		{name: "a deprecated model is still allowed", p: principalOf(nil), provider: "p-open", model: "dep-model", wantID: "m-dep"},
		{name: "a name with a slash", p: principalOf(nil), provider: "p-open", model: "meta/llama-3", wantID: "m-slash"},
		{name: "a selected model without a grant", p: principalOf(nil), provider: "p-open", model: "sel-model", want: ErrModelNotAllowed},
		{name: "a selected model with a grant", p: principalOf(nil), ta: grant("m-sel"), provider: "p-open", model: "sel-model", wantID: "m-sel"},
		{name: "a grant of another model does not help", p: principalOf(nil), ta: grant("m-sel2"), provider: "p-open", model: "sel-model", want: ErrModelNotAllowed},
		{name: "a retired model, available to all", p: principalOf(nil), provider: "p-open", model: "ret-model", want: ErrModelNotAllowed},
		{name: "a retired model with a grant", p: principalOf(nil), ta: grant("m-ret-sel"), provider: "p-open", model: "ret-sel-model", want: ErrModelNotAllowed},
		{name: "an unknown model", p: principalOf(nil), provider: "p-open", model: "no-such-model", want: ErrModelNotAllowed},
		{name: "the upstream identifier is not a name", p: principalOf(nil), provider: "p-open", model: "upstream-id-1", want: ErrModelNotAllowed},
		{name: "the public name of a model with an upstream identifier", p: principalOf(nil), provider: "p-open", model: "nick", wantID: "m-up"},
		{name: "names are case-sensitive", p: principalOf(nil), provider: "p-open", model: "ALL-MODEL", want: ErrModelNotAllowed},
		{name: "an empty name", p: principalOf(nil), provider: "p-open", model: "", want: ErrModelNotAllowed},
		{name: "a model of another provider's name", p: principalOf(nil), provider: "p-nodef", model: "all-model", want: ErrProviderUnavailable},
		{name: "an unknown provider", p: principalOf(nil), provider: "nobody", model: "all-model", want: ErrProviderUnavailable},
		{name: "a disabled provider", p: principalOf(nil), provider: "p-off", model: "off-model", want: ErrProviderUnavailable},
		{name: "a provider without an endpoint", p: principalOf(nil), provider: "p-noep", model: "noep-model", want: ErrProviderUnavailable},
		{name: "a provider whose endpoint is not the default", p: principalOf(nil), provider: "p-nodef", model: "nodef-model", want: ErrProviderUnavailable},
		{name: "a provider whose default endpoint is disabled", p: principalOf(nil), provider: "p-defoff", model: "defoff-model", want: ErrProviderUnavailable},
		// the tenant's restriction
		{name: "the restriction names the model", p: principalOf(nil), ta: restrict(TenantAccess{}, "p-open/all-model"), provider: "p-open", model: "all-model", wantID: "m-all"},
		{name: "the restriction does not name the model", p: principalOf(nil), ta: restrict(TenantAccess{}, "p-open/dep-model"), provider: "p-open", model: "all-model", want: ErrModelNotAllowed},
		{name: "an empty restriction allows nothing", p: principalOf(nil), ta: restrict(TenantAccess{}), provider: "p-open", model: "all-model", want: ErrModelNotAllowed},
		{name: "a restriction does not grant", p: principalOf(nil), ta: restrict(TenantAccess{}, "p-open/sel-model"), provider: "p-open", model: "sel-model", want: ErrModelNotAllowed},
		{name: "a restriction and a grant", p: principalOf(nil), ta: restrict(grant("m-sel"), "p-open/sel-model"), provider: "p-open", model: "sel-model", wantID: "m-sel"},
		{name: "a restriction names a reference, not a name alone", p: principalOf(nil), ta: restrict(TenantAccess{}, "all-model"), provider: "p-open", model: "all-model", want: ErrModelNotAllowed},
		{name: "a restriction of a slash name", p: principalOf(nil), ta: restrict(TenantAccess{}, "p-open/meta/llama-3"), provider: "p-open", model: "meta/llama-3", wantID: "m-slash"},
		{name: "restricted but with a nil map", p: principalOf(nil), ta: TenantAccess{Restricted: true}, provider: "p-open", model: "all-model", want: ErrModelNotAllowed},
		// the key's list
		{name: "the key's list names the model", p: principalOf([]string{"p-open/all-model"}), provider: "p-open", model: "all-model", wantID: "m-all"},
		{name: "the key's list does not name the model", p: principalOf([]string{"p-open/dep-model"}), provider: "p-open", model: "all-model", want: ErrModelNotAllowed},
		{name: "an empty list of a key allows nothing", p: principalOf([]string{}), provider: "p-open", model: "all-model", want: ErrModelNotAllowed},
		{name: "a key's list does not grant", p: principalOf([]string{"p-open/sel-model"}), provider: "p-open", model: "sel-model", want: ErrModelNotAllowed},
		// both
		{name: "restriction and list both name it", p: principalOf([]string{"p-open/all-model", "p-open/dep-model"}),
			ta: restrict(TenantAccess{}, "p-open/all-model", "p-open/dep-model"), provider: "p-open", model: "dep-model", wantID: "m-dep"},
		{name: "only the restriction names it", p: principalOf([]string{"p-open/dep-model"}),
			ta: restrict(TenantAccess{}, "p-open/all-model"), provider: "p-open", model: "all-model", want: ErrModelNotAllowed},
		{name: "only the list names it", p: principalOf([]string{"p-open/all-model"}),
			ta: restrict(TenantAccess{}, "p-open/dep-model"), provider: "p-open", model: "all-model", want: ErrModelNotAllowed},
		{name: "the intersection of a restriction and a list", p: principalOf([]string{"p-open/dep-model", "p-open/meta/llama-3"}),
			ta: restrict(TenantAccess{}, "p-open/all-model", "p-open/dep-model"), provider: "p-open", model: "dep-model", wantID: "m-dep"},
		{name: "a list entry outside the restriction", p: principalOf([]string{"p-open/dep-model", "p-open/meta/llama-3"}),
			ta: restrict(TenantAccess{}, "p-open/all-model", "p-open/dep-model"), provider: "p-open", model: "meta/llama-3", want: ErrModelNotAllowed},
		// a platform principal cannot call models
		{name: "a platform principal", p: authz.Principal{Kind: authz.KindPlatform, ID: "pk", Roles: []string{authz.RolePlatformAdmin}},
			provider: "p-open", model: "all-model", want: ErrModelNotAllowed},
	}
	for _, k := range cases {
		t.Run(k.name, func(t *testing.T) {
			m, e, err := s.Resolve(k.p, k.ta, k.provider, k.model)
			if k.want != nil {
				if !errors.Is(err, k.want) {
					t.Fatalf("error %v, want %v (model %+v)", err, k.want, m)
				}
				if m.ID != "" || e.Name != "" || e.BaseURL != "" {
					t.Errorf("a refusal returned data: %+v %+v", m, e)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if m.ID != k.wantID || m.Name != k.model || m.Provider != k.provider {
				t.Errorf("model %+v, want %s", m, k.wantID)
			}
			if k.provider == "p-open" && (e.Name != "main" || e.BaseURL != "https://a.example.com" || !e.Default || !e.Enabled) {
				t.Errorf("endpoint %+v, want the default one", e)
			}
		})
	}

	t.Run("every refusal is the same error with the same text", func(t *testing.T) {
		// A caller must not learn which models another tenant was granted, whether a model
		// is retired, or what its upstream identifier is: the model refusals are one value.
		var errs []error
		for _, c := range cases {
			if errors.Is(c.want, ErrModelNotAllowed) {
				_, _, err := s.Resolve(c.p, c.ta, c.provider, c.model)
				errs = append(errs, err)
			}
		}
		if len(errs) < 10 {
			t.Fatalf("only %d refusals compared", len(errs))
		}
		for _, err := range errs {
			if err != ErrModelNotAllowed { //nolint:errorlint // the point is that the value is not wrapped or decorated
				t.Errorf("a refusal wraps or decorates the error: %#v", err)
			}
		}
		if ErrModelNotAllowed == ErrProviderUnavailable || errors.Is(ErrModelNotAllowed, ErrProviderUnavailable) { //nolint:errorlint // two values that must be different
			t.Error("the two errors are not distinct")
		}
	})

	t.Run("Visible ignores the restriction and the key", func(t *testing.T) {
		byID := func(id string) Model {
			for _, m := range syntheticModels() {
				if m.ID == id {
					return m
				}
			}
			t.Fatalf("no model %s", id)
			return Model{}
		}
		for _, c := range []struct {
			id   string
			ta   TenantAccess
			want bool
		}{
			{"m-all", TenantAccess{}, true},
			{"m-all", restrict(TenantAccess{}), true}, // a restriction naming nothing
			{"m-dep", TenantAccess{}, true},
			{"m-sel", TenantAccess{}, false},
			{"m-sel", grant("m-sel"), true},
			{"m-sel", grant("m-sel2"), false},
			{"m-ret", TenantAccess{}, false},
			{"m-ret-sel", grant("m-ret-sel"), false},
		} {
			if got := s.Visible(c.ta, byID(c.id)); got != c.want {
				t.Errorf("Visible(%s, %+v) = %v, want %v", c.id, c.ta, got, c.want)
			}
		}
	})

	t.Run("ParseRef and the references of a principal agree", func(t *testing.T) {
		p, n, err := ParseRef("p-open/meta/llama-3")
		if err != nil || p != "p-open" || n != "meta/llama-3" {
			t.Fatalf("ParseRef: %q %q %v", p, n, err)
		}
		if _, _, err := s.Resolve(principalOf([]string{"p-open/meta/llama-3"}), TenantAccess{}, p, n); err != nil {
			t.Errorf("a list naming a slash name: %v", err)
		}
	})
}

// TestSnapshotVersions covers Snapshot.Versions: ordered by ValidFrom, a copy, empty for an
// unknown model; the order VersionAt relies on.
func TestSnapshotVersions(t *testing.T) {
	t0 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	mk := func(id string, d time.Duration) pricing.Version {
		return pricing.Version{ID: id, ModelID: "m-all", ValidFrom: t0.Add(d), Currency: "USD", Source: "import",
			Rates: []pricing.Rate{{Category: "input", PerMillion: mustDecimal(t, "1")}}}
	}
	s := NewSnapshot(SnapshotData{
		Providers: []Provider{{ID: "p", Name: "p-open", Family: "openai", Enabled: true}},
		Models:    []Model{{ID: "m-all", Provider: "p-open", Name: "all-model", Status: "active", Availability: "all"}},
		Versions:  map[string][]pricing.Version{"m-all": {mk("v3", 48*time.Hour), mk("v1", 0), mk("v2", 24*time.Hour)}},
	})
	got := s.Versions("m-all")
	if len(got) != 3 || got[0].ID != "v1" || got[1].ID != "v2" || got[2].ID != "v3" {
		t.Fatalf("versions %+v", got)
	}
	got[0].ID = "changed"
	if again := s.Versions("m-all"); again[0].ID != "v1" {
		t.Error("Versions returned the snapshot's own slice")
	}
	if v := s.Versions("unknown"); len(v) != 0 {
		t.Errorf("versions of an unknown model: %+v", v)
	}
	if v, ok := pricing.VersionAt(s.Versions("m-all"), t0.Add(30*time.Hour)); !ok || v.ID != "v2" {
		t.Errorf("VersionAt over the snapshot's versions: %+v %v", v, ok)
	}
}

func mustDecimal(t testing.TB, s string) pricing.Decimal {
	t.Helper()
	d, err := pricing.ParseDecimal(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
