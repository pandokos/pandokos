package limits

import (
	"math/rand/v2"
	"sync"
	"testing"
	"time"
	_ "time/tzdata" // the zones of the tests come from the embedded database
)

// testZones are the fifteen zones of T-647: UTC, half- and quarter-hour offsets, +14, zones whose
// clocks change at midnight, and a 30-minute change.
var testZones = []string{
	"UTC", "Europe/Rome", "America/New_York", "America/St_Johns", "America/Santiago", "America/Havana",
	"America/Asuncion", "Africa/Cairo", "Asia/Beirut", "Asia/Kolkata", "Asia/Kathmandu",
	"Australia/Lord_Howe", "Pacific/Apia", "Pacific/Kiritimati", "Pacific/Chatham",
}

func mustZone(tb testing.TB, name string) *time.Location {
	tb.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		tb.Fatalf("zone %s: %v", name, err)
	}
	return loc
}

func mustCalendar(tb testing.TB, name string) *Calendar {
	tb.Helper()
	cal, err := NewCalendar(mustZone(tb, name))
	if err != nil {
		tb.Fatalf("calendar %s: %v", name, err)
	}
	return cal
}

// dayLabel and monthLabel are the labels of an instant in a zone, comparable as integers.
func dayLabel(t time.Time, loc *time.Location) int {
	y, m, d := t.In(loc).Date()
	return y*10000 + int(m)*100 + d
}

func monthLabel(t time.Time, loc *time.Location) int { return dayLabel(t, loc) / 100 }

// TestCalendar is T-647.
func TestCalendar(t *testing.T) {
	t.Parallel()
	seed := seedOf(t)
	for _, name := range testZones {
		t.Run(name, func(t *testing.T) {
			loc := mustZone(t, name)
			cal := mustCalendar(t, name)
			if cal.Zone() != name {
				t.Errorf("Zone() = %q, want %q", cal.Zone(), name)
			}
			check := func(at time.Time) {
				t.Helper()
				for _, k := range []PeriodKind{PerDay, PerMonth} {
					label := dayLabel
					if k == PerMonth {
						label = monthLabel
					}
					p := cal.Of(k, at)
					if p.Start.After(at) || !at.Before(p.End) {
						t.Fatalf("kind %d at %s: %v is not inside [%s, %s)", k, at, at, p.Start, p.End)
					}
					l := label(at, loc)
					if label(p.Start, loc) != l {
						t.Fatalf("kind %d at %s: Start %s is not on the label %d", k, at, p.Start, l)
					}
					if label(p.Start.Add(-time.Nanosecond), loc) >= l {
						t.Fatalf("kind %d at %s: the instant before Start %s has label %d, not an earlier one", k, at, p.Start, label(p.Start.Add(-time.Nanosecond), loc))
					}
					if label(p.End.Add(-time.Nanosecond), loc) != l || label(p.End, loc) <= l {
						t.Fatalf("kind %d at %s: End %s is not the first instant of the next label", k, at, p.End)
					}
					if p.Start.Unix()%60 != 0 || p.Start.Nanosecond() != 0 || p.End.Unix()%60 != 0 || p.End.Nanosecond() != 0 {
						t.Fatalf("kind %d at %s: [%s, %s) is not on whole minutes", k, at, p.Start, p.End)
					}
					if next := cal.Of(k, p.End); !next.Start.Equal(p.End) {
						t.Fatalf("kind %d at %s: the period after [%s, %s) starts at %s: not contiguous", k, at, p.Start, p.End, next.Start)
					}
					if prev := cal.Of(k, p.Start.Add(-time.Nanosecond)); !prev.End.Equal(p.Start) {
						t.Fatalf("kind %d at %s: the period before ends at %s, not at %s", k, at, prev.End, p.Start)
					}
				}
				d, m := cal.Of(PerDay, at), cal.Of(PerMonth, at)
				if d.Start.Before(m.Start) || d.End.After(m.End) {
					t.Fatalf("at %s: the day [%s, %s) is not inside its month [%s, %s)", at, d.Start, d.End, m.Start, m.End)
				}
				if first := cal.Of(PerDay, m.Start); !first.Start.Equal(m.Start) {
					t.Fatalf("at %s: the month starts at %s, its first day at %s", at, m.Start, first.Start)
				}
				if dayLabel(m.Start, loc)%100 != 1 {
					t.Fatalf("at %s: the month starts on %s, not on the first", at, m.Start.In(loc))
				}
			}
			// Every day of 2024 to 2030: its first instant, its last, and noon.
			for d := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC); d.Year() < 2031; d = d.AddDate(0, 0, 1) {
				for _, h := range []time.Duration{-30 * time.Hour, -1, 0, 1, 11 * time.Hour, 23*time.Hour + 59*time.Minute} {
					check(d.Add(h))
				}
			}
			rng := rngOf(seed, len(name))
			lo, hi := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix(), time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
			for range 3000 {
				check(time.Unix(lo+rng.Int64N(hi-lo), rng.Int64N(1e9)).UTC())
			}
			// A time is truncated to the microsecond before anything else.
			at := time.Date(2026, 6, 1, 23, 59, 59, 999_999_999, time.UTC)
			if got, want := cal.Of(PerDay, at), cal.Of(PerDay, at.Truncate(time.Microsecond)); got != want {
				t.Errorf("a nanosecond changed the period: %v vs %v", got, want)
			}
		})
	}

	t.Run("days of 23 and 25 hours", func(t *testing.T) {
		cal := mustCalendar(t, "Europe/Rome")
		spring := cal.Of(PerDay, time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC))
		autumn := cal.Of(PerDay, time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))
		normal := cal.Of(PerDay, time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))
		if d := spring.End.Sub(spring.Start); d != 23*time.Hour {
			t.Errorf("spring day lasts %v", d)
		}
		if d := autumn.End.Sub(autumn.Start); d != 25*time.Hour {
			t.Errorf("autumn day lasts %v", d)
		}
		if d := normal.End.Sub(normal.Start); d != 24*time.Hour {
			t.Errorf("a normal day lasts %v", d)
		}
		if want := time.Date(2026, 3, 28, 23, 0, 0, 0, time.UTC); !spring.Start.Equal(want) {
			t.Errorf("spring day starts at %v, want %v", spring.Start, want)
		}
	})

	t.Run("a day that starts when local midnight does not exist", func(t *testing.T) {
		// Havana moves the clocks from 00:00 to 01:00 on 2026-03-08: the day starts at 01:00.
		loc := mustZone(t, "America/Havana")
		cal := mustCalendar(t, "America/Havana")
		p := cal.Of(PerDay, time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC))
		if got := p.Start.In(loc); got.Hour() != 1 || got.Day() != 8 {
			t.Errorf("the day starts at %v, want 01:00 on the 8th", got)
		}
		if d := p.End.Sub(p.Start); d != 23*time.Hour {
			t.Errorf("the day lasts %v", d)
		}
	})

	t.Run("a month is as long as the calendar says", func(t *testing.T) {
		cal := mustCalendar(t, "UTC")
		p := cal.Of(PerMonth, time.Date(2028, 2, 10, 0, 0, 0, 0, time.UTC))
		if !p.Start.Equal(time.Date(2028, 2, 1, 0, 0, 0, 0, time.UTC)) || !p.End.Equal(time.Date(2028, 3, 1, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("February 2028: %v", p)
		}
	})

	t.Run("an offset with seconds is refused", func(t *testing.T) {
		if _, err := NewCalendar(time.FixedZone("odd", 3600+30)); err == nil {
			t.Error("a zone with an offset of 1h 0m 30s was accepted")
		}
		if _, err := NewCalendar(nil); err == nil {
			t.Error("a nil location was accepted")
		}
		if _, err := NewCalendar(time.FixedZone("whole", 5*3600+45*60)); err != nil {
			t.Errorf("a whole-minute offset was refused: %v", err)
		}
	})

	t.Run("concurrent use", func(t *testing.T) {
		cal := mustCalendar(t, "Australia/Lord_Howe")
		var wg sync.WaitGroup
		for g := range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rng := rand.New(rand.NewPCG(uint64(g), 7))
				for range 500 {
					at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(rng.Int64N(int64(365 * 24 * time.Hour))))
					p := cal.Of(PerDay, at)
					if at.Before(p.Start) || !at.Before(p.End) {
						t.Errorf("%v not in %v", at, p)
						return
					}
				}
			}()
		}
		wg.Wait()
	})
}
