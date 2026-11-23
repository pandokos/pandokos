package limits

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// PeriodKind is the length of a quota or budget period.
type PeriodKind int

// The kinds of period.
const (
	PerDay PeriodKind = iota + 1
	PerMonth
	// PeriodTotal is the whole window of a report as a single period (sprint 6.2). It is not a
	// period of a rule: a calendar has no period of this kind.
	PeriodTotal
)

// Period is [Start, End) in absolute time.
type Period struct{ Start, End time.Time }

// Calendar computes periods in one zone. It is safe for concurrent use and caches the periods of
// the last instant it was asked about.
type Calendar struct {
	loc *time.Location
	day atomic.Pointer[Period]
	mon atomic.Pointer[Period]
}

// The years whose offsets NewCalendar checks.
const (
	zoneCheckFrom = 2020
	zoneCheckTo   = 2040
)

// NewCalendar returns the calendar of a zone. It fails when an offset the zone uses between 2020
// and 2040 is not a whole number of minutes: period boundaries must lie on whole minutes of
// absolute time.
func NewCalendar(loc *time.Location) (*Calendar, error) {
	if loc == nil {
		return nil, errors.New("limits: the calendar needs a time zone")
	}
	to := time.Date(zoneCheckTo, 1, 1, 0, 0, 0, 0, time.UTC)
	t := time.Date(zoneCheckFrom, 1, 1, 0, 0, 0, 0, time.UTC).In(loc)
	for {
		name, off := t.Zone()
		if off%60 != 0 {
			return nil, fmt.Errorf("limits: time zone %q uses the offset %s (%d s), which is not a whole number of minutes", loc, name, off)
		}
		_, end := t.ZoneBounds()
		if end.IsZero() || !end.Before(to) {
			break
		}
		t = end
	}
	return &Calendar{loc: loc}, nil
}

// Zone returns the name of the zone of the calendar.
func (c *Calendar) Zone() string { return c.loc.String() }

// label is the civil date (or month) of an instant: year*10000 + month*100 + day, or year*100 +
// month, comparable as integers.
func (c *Calendar) label(k PeriodKind, t time.Time) int {
	y, m, d := t.In(c.loc).Date()
	if k == PerMonth {
		return y*100 + int(m)
	}
	return y*10000 + int(m)*100 + d
}

// civil returns the date of a label as a UTC midnight (the first of the month for a month).
func civil(k PeriodKind, l int) time.Time {
	if k == PerMonth {
		return time.Date(l/100, time.Month(l%100), 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(l/10000, time.Month(l/100%100), l%100, 0, 0, 0, 0, time.UTC)
}

// next returns the label after l.
func next(k PeriodKind, l int) int {
	d := civil(k, l)
	if k == PerMonth {
		d = d.AddDate(0, 1, 0)
		return d.Year()*100 + int(d.Month())
	}
	d = d.AddDate(0, 0, 1)
	return d.Year()*10000 + int(d.Month())*100 + d.Day()
}

// start returns S(l): the earliest instant whose label is l or later.
func (c *Calendar) start(k PeriodKind, l int) time.Time {
	cv := civil(k, l)
	local := time.Date(cv.Year(), cv.Month(), cv.Day(), 0, 0, 0, 0, c.loc)
	cands := []time.Time{local}
	lo, hi := local.Add(-36*time.Hour), local.Add(36*time.Hour)
	// The civil midnight read at every offset in effect around it, and every transition.
	t := lo.In(c.loc)
	for {
		_, off := t.Zone()
		cands = append(cands, cv.Add(-time.Duration(off)*time.Second))
		_, end := t.ZoneBounds()
		if end.IsZero() || end.After(hi) {
			break
		}
		cands = append(cands, end)
		t = end
	}
	var best time.Time
	found := false
	for _, cand := range cands {
		if c.label(k, cand) < l || c.label(k, cand.Add(-time.Nanosecond)) >= l {
			continue
		}
		if !found || cand.Before(best) {
			best, found = cand, true
		}
	}
	if !found {
		return local
	}
	return best.UTC()
}

// Of returns the period of kind k that contains t.
func (c *Calendar) Of(k PeriodKind, t time.Time) Period {
	t = t.Truncate(time.Microsecond)
	slot := &c.day
	if k == PerMonth {
		slot = &c.mon
	}
	if p := slot.Load(); p != nil && !t.Before(p.Start) && t.Before(p.End) {
		return *p
	}
	l := c.label(k, t)
	p := Period{Start: c.start(k, l), End: c.start(k, next(k, l))}
	slot.Store(&p)
	return p
}
