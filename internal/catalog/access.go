package catalog

import (
	"errors"
	"sort"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/pricing"
)

// Provider is a provider of the catalogue.
type Provider struct {
	ID, Name, Family, DatasetProvider string
	Enabled                           bool
}

// Endpoint is an address of a provider.
type Endpoint struct {
	Name, BaseURL, Region     string
	Private, Enabled, Default bool
}

// Model is a model as a caller sees it.
type Model struct {
	ID, Provider, Name, UpstreamID, Status, Availability string
	ContextWindow, MaxOutputTokens                       int64 // 0: unknown
}

// The two refusals of Resolve. Every reason that makes a model unavailable to a caller is the
// same value, so that a caller cannot learn which models other tenants were granted, whether a
// model is retired or what its upstream identifier is.
var (
	ErrProviderUnavailable = errors.New("catalog: provider unknown, disabled or without an enabled default endpoint")
	ErrModelNotAllowed     = errors.New("catalog: model not allowed")
)

// Model statuses and availabilities the rule depends on.
const (
	statusRetired   = "retired"
	availabilityAll = "all"
)

// TenantAccess is what one tenant was granted and restricted itself to. The maps are never
// changed once built.
type TenantAccess struct {
	Granted    map[string]bool // model IDs granted to the tenant
	Restricted bool            // the tenant has a restriction row
	Allowed    map[string]bool // references of the restriction
}

// SnapshotData is what NewSnapshot builds a Snapshot from.
type SnapshotData struct {
	Providers []Provider
	Endpoints map[string][]Endpoint // by provider name
	Models    []Model
	Versions  map[string][]pricing.Version // by model ID
}

// Snapshot is the global catalogue at one moment: providers, endpoints, models, price versions.
// It is immutable once built and safe for concurrent use.
type Snapshot struct {
	providers map[string]Provider
	endpoints map[string][]Endpoint
	models    map[string]map[string]Model // provider name, model name
	versions  map[string][]pricing.Version
}

// NewSnapshot builds a Snapshot. It copies its input; the versions of each model are sorted by
// ValidFrom.
func NewSnapshot(d SnapshotData) *Snapshot {
	s := &Snapshot{
		providers: make(map[string]Provider, len(d.Providers)),
		endpoints: make(map[string][]Endpoint, len(d.Endpoints)),
		models:    map[string]map[string]Model{},
		versions:  make(map[string][]pricing.Version, len(d.Versions)),
	}
	for _, p := range d.Providers {
		s.providers[p.Name] = p
	}
	for name, eps := range d.Endpoints {
		s.endpoints[name] = append([]Endpoint(nil), eps...)
	}
	for _, m := range d.Models {
		byName := s.models[m.Provider]
		if byName == nil {
			byName = map[string]Model{}
			s.models[m.Provider] = byName
		}
		byName[m.Name] = m
	}
	for id, vs := range d.Versions {
		s.versions[id] = copyVersions(vs)
		sort.SliceStable(s.versions[id], func(i, j int) bool { return s.versions[id][i].ValidFrom.Before(s.versions[id][j].ValidFrom) })
	}
	return s
}

func copyVersions(vs []pricing.Version) []pricing.Version {
	out := make([]pricing.Version, len(vs))
	for i, v := range vs {
		out[i] = v
		out[i].Rates = append([]pricing.Rate(nil), v.Rates...)
	}
	return out
}

// Resolve returns the model and the default endpoint that p may use for provider/name. The
// rule, in this order: the provider exists, is enabled and has an enabled default endpoint
// (ErrProviderUnavailable); a model of that provider has exactly that public name, is not
// retired, is available to all or granted to the tenant, is named by the tenant's restriction
// if it has one and by the key's list if it has one (ErrModelNotAllowed, the same value for
// every failure). A principal that is not a service account never passes: it has no tenant.
func (s *Snapshot) Resolve(p authz.Principal, ta TenantAccess, provider, name string) (Model, Endpoint, error) {
	if p.Kind != authz.KindServiceAccount || p.TenantID.IsZero() {
		return Model{}, Endpoint{}, ErrModelNotAllowed
	}
	prov, ok := s.providers[provider]
	if !ok || !prov.Enabled {
		return Model{}, Endpoint{}, ErrProviderUnavailable
	}
	var ep Endpoint
	found := false
	for _, e := range s.endpoints[provider] {
		if e.Default && e.Enabled {
			ep, found = e, true
			break
		}
	}
	if !found {
		return Model{}, Endpoint{}, ErrProviderUnavailable
	}
	m, ok := s.models[provider][name]
	if !ok || !s.Visible(ta, m) {
		return Model{}, Endpoint{}, ErrModelNotAllowed
	}
	ref := provider + "/" + name
	if ta.Restricted && !ta.Allowed[ref] {
		return Model{}, Endpoint{}, ErrModelNotAllowed
	}
	if p.Models != nil && !contains(p.Models, ref) {
		return Model{}, Endpoint{}, ErrModelNotAllowed
	}
	return m, ep, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Visible reports whether the tenant may see m: it is not retired and is available to all or
// granted. It ignores the tenant's restriction and the key's list.
func (s *Snapshot) Visible(ta TenantAccess, m Model) bool {
	return m.Status != statusRetired && (m.Availability == availabilityAll || ta.Granted[m.ID])
}

// Versions returns a copy of the price versions of a model, sorted by ValidFrom.
func (s *Snapshot) Versions(modelID string) []pricing.Version {
	return copyVersions(s.versions[modelID])
}
