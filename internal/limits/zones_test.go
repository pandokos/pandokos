package limits

import (
	"fmt"
	"testing"
	"time"
)

// zoneNames are the zones of the tests of the calendar of a tenant: whole-hour and half-hour
// offsets, a southern hemisphere zone and UTC.
var zoneNames = []string{"UTC", "Europe/Rome", "America/New_York", "Asia/Tokyo", "Australia/Lord_Howe", "America/Sao_Paulo"}

// civilDate is the reference of TenantCalendar.Day: the date of t in the zone, written by the
// standard library.
func civilDate(tb testing.TB, zone string, t time.Time) string {
	tb.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		tb.Fatal(err)
	}
	return t.In(loc).Format("2006-01-02")
}

// TestTenantCalendar is T-699: At, Of and Day of the calendar of a tenant, the instant of a change,
// and the zones an installation shares.
func TestTenantCalendar(t *testing.T) {
	rome, ny := mustCalendar(t, "Europe/Rome"), mustCalendar(t, "America/New_York")

	t.Run("a tenant that never changed its zone has one calendar", func(t *testing.T) {
		c := TenantCalendar{Current: rome}
		for _, x := range []time.Time{at(2026, 1, 1, 0, 0, 0), at(2026, 6, 15, 12, 0, 0), at(2030, 12, 31, 23, 59, 59)} {
			if c.At(x) != rome {
				t.Errorf("At(%s) is not the current calendar", x)
			}
			for _, k := range []PeriodKind{PerDay, PerMonth} {
				if got, want := c.Of(k, x), rome.Of(k, x); got != want {
					t.Errorf("Of(%d, %s) = %v, want %v", k, x, got, want)
				}
			}
			if got, want := c.Day(x), civilDate(t, "Europe/Rome", x); got != want {
				t.Errorf("Day(%s) = %q, want %q", x, got, want)
			}
		}
	})

	t.Run("previous before the instant, current from it", func(t *testing.T) {
		from := rome.Of(PerMonth, at(2026, 11, 17, 12, 0, 0)).End // 2026-11-30T23:00:00Z
		c := TenantCalendar{Current: ny, Previous: rome, From: from}
		for _, d := range []time.Duration{-30 * 24 * time.Hour, -time.Hour, -time.Nanosecond} {
			if c.At(from.Add(d)) != rome {
				t.Errorf("At(From%+v) is not the previous calendar", d)
			}
		}
		for _, d := range []time.Duration{0, time.Nanosecond, time.Hour, 30 * 24 * time.Hour} {
			if c.At(from.Add(d)) != ny {
				t.Errorf("At(From%+v) is not the current calendar", d)
			}
		}
		// The day in which the instant falls is a regular day of the new zone: it began before the instant.
		day := c.Of(PerDay, from)
		if day != ny.Of(PerDay, from) || !day.Start.Before(from) {
			t.Errorf("the day of the change is %v, want the regular day of the new zone %v, which began before %v", day, ny.Of(PerDay, from), from)
		}
		if got, want := c.Of(PerMonth, from.Add(-time.Second)), rome.Of(PerMonth, from.Add(-time.Second)); got != want || !got.End.Equal(from) {
			t.Errorf("the month in progress ends at %v, want %v: it ends where the old zone ends it", got.End, from)
		}
		if got, want := c.Day(from.Add(-time.Second)), civilDate(t, "Europe/Rome", from.Add(-time.Second)); got != want {
			t.Errorf("Day before = %q, want %q", got, want)
		}
		if got, want := c.Day(from), civilDate(t, "America/New_York", from); got != want {
			t.Errorf("Day from = %q, want %q", got, want)
		}
	})

	t.Run("the instant of a change is the end of the month of the zone in force", func(t *testing.T) {
		for _, tc := range []struct {
			zone string
			now  time.Time
			want time.Time
		}{
			{"Europe/Rome", at(2026, 11, 17, 12, 0, 0), at(2026, 11, 30, 23, 0, 0)},
			{"Europe/Rome", at(2026, 10, 31, 22, 59, 59), at(2026, 10, 31, 23, 0, 0)}, // the last second of October in Rome (CET starts the 25th)
			{"Europe/Rome", at(2026, 10, 31, 23, 0, 0), at(2026, 11, 30, 23, 0, 0)},   // the first of November
			{"Europe/Rome", at(2026, 3, 10, 0, 0, 0), at(2026, 3, 31, 22, 0, 0)},      // the clocks changed on the 29th: March ends at 22:00Z
			{"America/New_York", at(2026, 3, 1, 4, 59, 59), at(2026, 3, 1, 5, 0, 0)},  // the clocks change on the 8th
			{"America/New_York", at(2026, 3, 20, 0, 0, 0), at(2026, 4, 1, 4, 0, 0)},
			{"Australia/Lord_Howe", at(2026, 4, 5, 0, 0, 0), at(2026, 4, 30, 13, 30, 0)}, // +10:30 after the change of the 5th
			{"UTC", at(2028, 2, 10, 5, 0, 0), at(2028, 3, 1, 0, 0, 0)},                   // a leap year
		} {
			cal := mustCalendar(t, tc.zone)
			got := NextChange(cal, tc.now)
			if want := cal.Of(PerMonth, tc.now).End; !got.Equal(want) {
				t.Errorf("NextChange(%s, %s) = %s, want the end of the month in force, %s", tc.zone, tc.now, got, want)
			}
			if !got.Equal(tc.want) {
				t.Errorf("NextChange(%s, %s) = %s, want %s", tc.zone, tc.now, got, tc.want)
			}
			if !got.After(tc.now) {
				t.Errorf("NextChange(%s, %s) = %s is not in the future", tc.zone, tc.now, got)
			}
		}
	})

	t.Run("Zones shares calendars, resolves the default and refuses what it cannot use", func(t *testing.T) {
		z, err := NewZones(time.UTC)
		if err != nil {
			t.Fatalf("NewZones: %v", err)
		}
		a, err := z.Calendar("Europe/Rome")
		if err != nil {
			t.Fatal(err)
		}
		b, err := z.Calendar("Europe/Rome")
		if err != nil || a != b {
			t.Errorf("two calls for one zone gave two calendars (%v)", err)
		}
		def, err := z.Calendar("")
		if err != nil || def == nil || def.Zone() != "UTC" {
			t.Errorf("the default zone: %v %v", def, err)
		}
		if other, _ := z.Calendar("UTC"); other == nil || other.Zone() != "UTC" {
			t.Error("the zone UTC")
		}
		for _, name := range []string{"Mars/Olympus", "Europe/Rom", "europe/rome", "Europe Rome", "../etc/passwd", "Local"} {
			if c, err := z.Calendar(name); err == nil {
				t.Errorf("the zone %q was accepted: %v", name, c)
			}
		}
		// Offsets with seconds: the zone has local mean time in its history, but the calendar looks at 2020 to 2040 only;
		// a zone whose offset in that time is not a whole number of minutes is refused by NewCalendar.
		rome2, err := NewZones(mustLocation(t, "Europe/Rome"))
		if err != nil {
			t.Fatal(err)
		}
		if def, err := rome2.Calendar(""); err != nil || def.Zone() != "Europe/Rome" {
			t.Errorf("a default that is not UTC: %v %v", def, err)
		}
		// An offset with seconds (a zone of a few seconds from UTC) is refused as a default and, through
		// NewCalendar, never becomes a calendar: period boundaries lie on whole minutes.
		if _, err := NewZones(time.FixedZone("UTC+0:01:30", 90)); err == nil {
			t.Error("NewZones accepted a default zone whose offset is 90 seconds")
		}
		if _, err := NewZones(time.FixedZone("UTC-0:00:30", -30)); err == nil {
			t.Error("NewZones accepted a default zone whose offset is -30 seconds")
		}
		if z, err := NewZones(time.FixedZone("UTC+1", 3600)); err != nil || z == nil {
			t.Errorf("a fixed zone of a whole hour: %v", err)
		}
		if _, err := NewZones(nil); err == nil {
			t.Error("NewZones without a default accepted nil")
		}
	})

	t.Run("Zones is safe for concurrent use", func(t *testing.T) {
		z, err := NewZones(time.UTC)
		if err != nil {
			t.Fatalf("NewZones: %v", err)
		}
		done := make(chan *Calendar, 8)
		for range 8 {
			go func() { c, _ := z.Calendar("Asia/Tokyo"); done <- c }()
		}
		first := <-done
		for range 7 {
			if c := <-done; c != first || c == nil {
				t.Errorf("a concurrent call gave another calendar: %p %p", c, first)
			}
		}
	})
}

func mustLocation(tb testing.TB, name string) *time.Location {
	tb.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		tb.Fatal(err)
	}
	return loc
}

// TestTenantCalendarProperty is the property part of T-699: for random pairs of zones and instants
// of change, before the instant the periods are those of the old zone, from it they are regular
// periods of the new one and never a truncated one, and the instant is the end of the old zone's
// month that contains the change.
func TestTenantCalendarProperty(t *testing.T) {
	seed := seedOf(t)
	cals := map[string]*Calendar{}
	for _, z := range zoneNames {
		cals[z] = mustCalendar(t, z)
	}
	for n := range max(1, schedules()/4) {
		rng := rngOf(seed, n)
		oldZ, newZ := zoneNames[rng.IntN(len(zoneNames))], zoneNames[rng.IntN(len(zoneNames))]
		oldC, newC := cals[oldZ], cals[newZ]
		t0 := at(2026, 1, 1, 0, 0, 0).Add(time.Duration(rng.Int64N(int64(3 * 365 * 24 * time.Hour))))
		from := NextChange(oldC, t0)
		name := fmt.Sprintf("seed %d schedule %d: %s -> %s changed at %s", seed, n, oldZ, newZ, t0.Format(time.RFC3339))
		if want := oldC.Of(PerMonth, t0).End; !from.Equal(want) {
			t.Fatalf("%s: the instant is %s, want the end of the old zone's month, %s", name, from, want)
		}
		tc := TenantCalendar{Current: newC, Previous: oldC, From: from}
		for range 40 {
			var x time.Time
			switch rng.IntN(3) {
			case 0: // around the instant
				x = from.Add(time.Duration(rng.Int64N(int64(72*time.Hour))) - 36*time.Hour)
			case 1: // before
				x = t0.Add(time.Duration(rng.Int64N(int64(from.Sub(t0) + time.Second))))
			default: // after
				x = from.Add(time.Duration(rng.Int64N(int64(90 * 24 * time.Hour))))
			}
			zone, cal := oldZ, oldC
			if !x.Before(from) {
				zone, cal = newZ, newC
			}
			if tc.At(x) != cal {
				t.Fatalf("%s: At(%s) is not the calendar of %s", name, x, zone)
			}
			for _, k := range []PeriodKind{PerDay, PerMonth} {
				got, want := tc.Of(k, x), cal.Of(k, x)
				if got != want {
					t.Fatalf("%s: Of(%d, %s) = %v, want the regular period %v of %s (never a truncated one)", name, k, x, got, want, zone)
				}
				if x.Before(from) && k == PerMonth && got.End.After(from) {
					t.Fatalf("%s: the month in progress at %s ends at %s, after the instant %s", name, x, got.End, from)
				}
				if !got.Start.Before(got.End) || x.Before(got.Start) || !x.Before(got.End) {
					t.Fatalf("%s: %v does not contain %s", name, got, x)
				}
			}
			if got, want := tc.Day(x), civilDate(t, zone, x); got != want {
				t.Fatalf("%s: Day(%s) = %q, want %q", name, x, got, want)
			}
		}
	}
}
