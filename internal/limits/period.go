package limits

import (
	"errors"
	"time"
)

// errNotImplemented is what the parts of sprint 5.2 that are still scaffolding answer.
var errNotImplemented = errors.New("limits: not implemented")

// PeriodKind is the length of a quota or budget period.
type PeriodKind int

// The kinds of period.
const (
	PerDay PeriodKind = iota + 1
	PerMonth
)

// Period is [Start, End) in absolute time.
type Period struct{ Start, End time.Time }

// Calendar computes periods in one zone. It is safe for concurrent use and caches the periods of
// the last instant it was asked about.
type Calendar struct{}

// NewCalendar returns the calendar of a zone. It fails when an offset the zone uses is not a
// whole number of minutes.
func NewCalendar(_ *time.Location) (*Calendar, error) {
	return nil, errNotImplemented
}

// Of returns the period of kind k that contains t.
func (c *Calendar) Of(_ PeriodKind, _ time.Time) Period {
	return Period{}
}

// Zone returns the name of the zone of the calendar.
func (c *Calendar) Zone() string {
	return ""
}
