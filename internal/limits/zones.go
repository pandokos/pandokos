package limits

import (
	"errors"
	"time"
)

// This file holds the shape of the calendar of a tenant (sprint 5.3, section 5.7). The bodies are
// placed by step C2; until then they answer nothing, so that the tests of the sprint compile and
// fail for the right reason.

var errZonesNotImplemented = errors.New("limits: the calendar of a tenant is not implemented yet")

// Zones shares one Calendar per zone name; "" is the default zone of the installation.
type Zones struct{}

// NewZones returns the zones of an installation whose default zone is def.
func NewZones(_ *time.Location) (*Zones, error) { return nil, errZonesNotImplemented }

// Calendar returns the calendar of a zone by its IANA name; "" is the default. An unknown zone, or
// one whose offsets are not whole minutes, is an error.
func (z *Zones) Calendar(_ string) (*Calendar, error) { return nil, errZonesNotImplemented }

// TenantCalendar is Previous before From and Current from it. Previous is nil when the tenant never
// changed its zone.
type TenantCalendar struct {
	Current, Previous *Calendar
	From              time.Time
}

// At returns the calendar in force at t.
func (c TenantCalendar) At(_ time.Time) *Calendar { return nil }

// Of returns the period of kind k that contains t in the calendar in force at t.
func (c TenantCalendar) Of(_ PeriodKind, _ time.Time) Period { return Period{} }

// Day returns the civil date at t in the calendar in force at t, as "2006-01-02".
func (c TenantCalendar) Day(_ time.Time) string { return "" }

// NextChange returns the instant at which a change of zone made at now takes effect: the end of the
// month of the zone in force that contains now.
func NextChange(_ *Calendar, _ time.Time) time.Time { return time.Time{} }
