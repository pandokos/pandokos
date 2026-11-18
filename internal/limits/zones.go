package limits

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// This file holds the calendar of a tenant (sprint 5.3, section 5.7): the calendars of an
// installation by zone name, and the pair of calendars a tenant has around a change of its zone.

// Zones shares one Calendar per zone name; "" is the default zone of the installation. It is safe
// for concurrent use; the map is bounded by the number of zone names the embedded database holds.
type Zones struct {
	def *Calendar

	mu   sync.Mutex
	cals map[string]*Calendar
}

// NewZones returns the zones of an installation whose default zone is def. A default whose offsets
// are not whole minutes is refused.
func NewZones(def *time.Location) (*Zones, error) {
	if def == nil {
		return nil, errors.New("limits: the zones need a default time zone")
	}
	c, err := NewCalendar(def)
	if err != nil {
		return nil, err
	}
	return &Zones{def: c, cals: map[string]*Calendar{def.String(): c}}, nil
}

// Calendar returns the calendar of a zone by its IANA name; "" is the default. An unknown zone, or
// one whose offsets are not whole minutes, is an error.
func (z *Zones) Calendar(name string) (*Calendar, error) {
	if z == nil {
		return nil, errors.New("limits: no zones")
	}
	if name == "" {
		return z.def, nil
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	if c, ok := z.cals[name]; ok {
		return c, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil || loc.String() != name || name == "Local" {
		return nil, fmt.Errorf("limits: unknown time zone %q", name)
	}
	c, err := NewCalendar(loc)
	if err != nil {
		return nil, err
	}
	z.cals[name] = c
	return c, nil
}

// TenantCalendar is Previous before From and Current from it. Previous is nil when the tenant never
// changed its zone. The zero value has no calendar: it yields no period.
type TenantCalendar struct {
	Current, Previous *Calendar
	From              time.Time
}

// At returns the calendar in force at t.
func (c TenantCalendar) At(t time.Time) *Calendar {
	if c.Previous != nil && t.Before(c.From) {
		return c.Previous
	}
	return c.Current
}

// Of returns the period of kind k that contains t in the calendar in force at t; the zero Period
// when there is no calendar.
func (c TenantCalendar) Of(k PeriodKind, t time.Time) Period {
	cal := c.At(t)
	if cal == nil {
		return Period{}
	}
	return cal.Of(k, t)
}

// Day returns the civil date at t in the calendar in force at t, as "2006-01-02"; "" when there is
// no calendar.
func (c TenantCalendar) Day(t time.Time) string {
	cal := c.At(t)
	if cal == nil {
		return ""
	}
	return t.In(cal.loc).Format("2006-01-02")
}

// NextChange returns the instant at which a change of zone made at now takes effect: the end of the
// month of the zone in force that contains now. It is the one place that computes the instant.
func NextChange(inForce *Calendar, now time.Time) time.Time {
	return inForce.Of(PerMonth, now).End
}
