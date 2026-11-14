package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// LoadSnapshot reads the whole catalogue in one transaction with no scope: the catalogue tables
// are readable in any session.
func LoadSnapshot(ctx context.Context, db *store.DB) (*Snapshot, error) {
	var s *Snapshot
	err := db.Unscoped(ctx, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		provs, err := q.LoadProviders(ctx)
		if err != nil {
			return err
		}
		eps, err := q.LoadEndpoints(ctx)
		if err != nil {
			return err
		}
		models, err := q.LoadModels(ctx)
		if err != nil {
			return err
		}
		vers, err := q.LoadPriceVersions(ctx)
		if err != nil {
			return err
		}
		d := SnapshotData{Endpoints: map[string][]Endpoint{}, Versions: map[string][]pricing.Version{}}
		nameOf := make(map[string]string, len(provs))
		for _, p := range provs {
			nameOf[p.ID] = p.Name
			d.Providers = append(d.Providers, Provider{ID: p.ID, Name: p.Name, Family: p.ApiFamily, DatasetProvider: p.DatasetProvider, Enabled: p.Enabled})
		}
		for _, e := range eps {
			name, ok := nameOf[e.ProviderID]
			if !ok {
				continue
			}
			d.Endpoints[name] = append(d.Endpoints[name], Endpoint{Name: e.Name, BaseURL: e.BaseUrl, Region: e.Region, Private: e.Private, Enabled: e.Enabled, Default: e.IsDefault})
		}
		for _, m := range models {
			name, ok := nameOf[m.ProviderID]
			if !ok {
				continue
			}
			d.Models = append(d.Models, Model{ID: m.ID, Provider: name, Name: m.Name, UpstreamID: m.UpstreamID, Status: m.Status, Availability: m.Availability,
				ContextWindow: deref(m.ContextWindow), MaxOutputTokens: deref(m.MaxOutputTokens)})
		}
		for _, v := range vers {
			rates, err := ratesOf(v.Categories, v.Thresholds, v.Rates)
			if err != nil {
				return err
			}
			d.Versions[v.ModelID] = append(d.Versions[v.ModelID], pricing.Version{ID: v.ID, ModelID: v.ModelID, ValidFrom: v.ValidFrom, Currency: v.Currency, Source: v.Source, Rates: rates})
		}
		s = NewSnapshot(d)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("catalog: load the catalogue: %w", err)
	}
	return s, nil
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// errRates is a price the database holds that the pricing package cannot read; it never quotes it.
var errRates = errors.New("catalog: a stored price version is unreadable")

func categoryRank(c pricing.Category) int {
	for i, x := range pricing.Categories {
		if x == c {
			return i
		}
	}
	return len(pricing.Categories)
}

func ratesOf(categories []string, thresholds []int64, rates []string) ([]pricing.Rate, error) {
	if len(categories) != len(thresholds) || len(categories) != len(rates) {
		return nil, errRates
	}
	out := make([]pricing.Rate, len(categories))
	for i := range categories {
		d, err := pricing.ParseDecimal(rates[i])
		if err != nil {
			return nil, errRates
		}
		out[i] = pricing.Rate{Category: pricing.Category(categories[i]), Threshold: thresholds[i], PerMillion: d}
	}
	// The database orders by category name; a version lists its rates in the order of the constants.
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := categoryRank(out[i].Category), categoryRank(out[j].Category); ri != rj {
			return ri < rj
		}
		return out[i].Threshold < out[j].Threshold
	})
	return out, nil
}

// LoadTenantAccess reads what one tenant was granted and the restriction it set, in one tenant
// transaction. A tenant with no restriction row is unrestricted.
func LoadTenantAccess(ctx context.Context, db *store.DB, id tenant.ID) (TenantAccess, error) {
	ta := TenantAccess{Granted: map[string]bool{}}
	err := db.WithTenant(ctx, id, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		grants, err := q.ListTenantGrants(ctx)
		if err != nil {
			return err
		}
		for _, g := range grants {
			ta.Granted[g] = true
		}
		r, err := q.GetModelRestriction(ctx)
		switch {
		case errors.Is(err, store.ErrNoRows):
			return nil
		case err != nil:
			return err
		}
		ta.Restricted = true
		ta.Allowed = make(map[string]bool, len(r.Models))
		for _, ref := range r.Models {
			ta.Allowed[ref] = true
		}
		return nil
	})
	if err != nil {
		return TenantAccess{}, fmt.Errorf("catalog: load tenant access: %w", err)
	}
	return ta, nil
}

// PriceAt returns the version of a model in force at the instant at, with its rates. found is
// false when the model has no version that started by then. tx may be in any scope.
func PriceAt(ctx context.Context, tx store.Tx, modelID string, at time.Time) (v pricing.Version, found bool, err error) {
	row, err := queries.New(tx).PriceAt(ctx, queries.PriceAtParams{ModelID: modelID, At: &at})
	switch {
	case errors.Is(err, store.ErrNoRows):
		return pricing.Version{}, false, nil
	case err != nil:
		return pricing.Version{}, false, fmt.Errorf("catalog: price at: %w", err)
	}
	rates, err := ratesOf(row.Categories, row.Thresholds, row.Rates)
	if err != nil {
		return pricing.Version{}, false, err
	}
	return pricing.Version{ID: row.ID, ModelID: row.ModelID, ValidFrom: row.ValidFrom, Currency: row.Currency, Source: row.Source, Rates: rates}, true, nil
}
