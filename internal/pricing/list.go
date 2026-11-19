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

// At returns the version in force at t and the list it belongs to: the tenant's version of the
// currency in force when it is a price, else the catalogue's version when it is in that currency.
// A List with the zero Currency prices from the catalogue whatever its currency.
func (l List) At(t time.Time) (Version, string, bool) {
	if l.Currency == (Active{}) {
		v, found := VersionAt(l.Catalogue, t)
		if !found {
			return Version{}, "", false
		}
		return v, SourceCatalogue, true
	}
	c := l.Currency.At(t)
	var own []Version
	for _, v := range l.Tenant {
		if v.Currency == c {
			own = append(own, v)
		}
	}
	if v, found := VersionAt(own, t); found && !v.End {
		return v, SourceTenant, true
	}
	if v, found := VersionAt(l.Catalogue, t); found && v.Currency == c {
		return v, SourceCatalogue, true
	}
	return Version{}, "", false
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

// ResolveActive builds the Active of a tenant from its three columns: "" is def, and Previous and
// From are set only while a change is pending (from is not nil).
func ResolveActive(current, previous string, from *time.Time, def string) Active {
	if current == "" {
		current = def
	}
	a := Active{Current: current}
	if from != nil {
		a.From = *from
		a.Previous = previous
		if a.Previous == "" {
			a.Previous = def
		}
	}
	return a
}
