package pricing

import (
	"time"

	"github.com/pandokos/pandokos/internal/family"
)

// The lists a price can come from.
const (
	SourceCatalogue = "catalogue"
	SourceTenant    = "tenant"
)

// Active is the active currency of a tenant: Previous before From, Current from it. From is zero
// and Previous empty when no change is pending. Both are resolved codes, never "".
type Active struct {
	Current, Previous string
	From              time.Time
}

// At returns the currency in force at t.
func (a Active) At(t time.Time) string {
	if a.Previous != "" && !a.From.IsZero() && t.Before(a.From) {
		return a.Previous
	}
	return a.Current
}

// List is the prices of one model for one tenant. No slice need be sorted.
type List struct {
	Currency  Active
	Catalogue []Version // the catalogue's versions of the model, of any currency
	Tenant    []Version // the tenant's versions of the model, of the currencies of Currency; End marks an end
}

// At returns the version in force at t and the list it belongs to. Until the sprint's rule of the
// price lists is in place it answers from the catalogue alone.
func (l List) At(t time.Time) (Version, string, bool) {
	v, found := VersionAt(l.Catalogue, t)
	if !found {
		return Version{}, "", false
	}
	return v, SourceCatalogue, true
}

// Cost is CostOf(u, the version in force at t), with Cost.Source set whenever Cost.VersionID is.
func (l List) Cost(u family.Usage, t time.Time) Cost {
	v, source, found := l.At(t)
	c := CostOf(u, v, found)
	if c.VersionID != "" {
		c.Source = source
	}
	return c
}
