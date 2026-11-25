package gateway_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/test/dbtest"
)

// retryAfter is the Retry-After of a refusal in seconds.
func retryAfter(t *testing.T, c captured) time.Duration {
	t.Helper()
	n, err := strconv.Atoi(c.header.Get("Retry-After"))
	if err != nil {
		t.Fatalf("Retry-After %q", c.header.Get("Retry-After"))
	}
	return time.Duration(n) * time.Second
}

// near reports whether got is within slack of want.
func near(got, want, slack time.Duration) bool {
	d := got - want
	return d >= -slack && d <= slack
}

// TestTenantZoneQuota is T-710: the periods of a quota or a budget are those of the tenant's zone.
func TestTenantZoneQuota(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	const slack = 30 * time.Second // the stack's clock runs at the speed of the real one from its start

	t.Run("two tenants with different zones and one daily quota reset at their own midnights", func(t *testing.T) {
		start := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
		rome, la := d.SeedTenant(t, "zone-rome"), d.SeedTenant(t, "zone-la")
		d.SetTenantZone(t, rome, "Europe/Rome", "", time.Time{})
		d.SetTenantZone(t, la, "America/Los_Angeles", "", time.Time{})
		d.SeedLimitRule(t, rome, "tenant", "", "requests_per_day", 1)
		d.SeedLimitRule(t, la, "tenant", "", "requests_per_day", 1)
		sr := newQuotaStack(t, d, rome, stackOptions{start: start})
		sl := newQuotaStack(t, d, la, stackOptions{start: start})

		for _, s := range []*quotaStack{sr, sl} {
			s.ok(t, "a", "fake-basic")
		}
		cr := sr.call(t, "a", "openai", "fake-basic")
		cl := sl.call(t, "a", "openai", "fake-basic")
		if cr.status != 429 || cl.status != 429 {
			t.Fatalf("second requests: %d and %d, want 429 for both", cr.status, cl.status)
		}
		// 12:00Z is 13:00 in Rome (midnight in 11 h) and 04:00 in Los Angeles (midnight in 20 h).
		if got := retryAfter(t, cr); !near(got, 11*time.Hour, slack) {
			t.Errorf("Rome: Retry-After %s, want 11 h", got)
		}
		if got := retryAfter(t, cl); !near(got, 20*time.Hour, slack) {
			t.Errorf("Los Angeles: Retry-After %s, want 20 h", got)
		}
		// Rome's midnight is 23:00Z: Rome is admitted again, Los Angeles is not.
		for _, s := range []*quotaStack{sr, sl} {
			s.real.AdvanceTo(time.Date(2026, 11, 17, 23, 0, 30, 0, time.UTC))
		}
		sr.ok(t, "a", "fake-basic")
		if c := sl.call(t, "a", "openai", "fake-basic"); c.status != 429 {
			t.Errorf("Los Angeles at Rome's midnight: %d", c.status)
		}
		// Los Angeles' midnight is 08:00Z of the next day.
		for _, s := range []*quotaStack{sr, sl} {
			s.real.AdvanceTo(time.Date(2026, 11, 18, 8, 0, 30, 0, time.UTC))
		}
		sl.ok(t, "a", "fake-basic")
	})

	t.Run("a zone changed mid-month changes nothing until the old zone's month ends", func(t *testing.T) {
		start := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
		tn := d.SeedTenant(t, "zone-month")
		d.SeedLimitRule(t, tn, "tenant", "", "requests_per_month", 2)
		s := newQuotaStack(t, d, tn, stackOptions{start: start})
		s.ok(t, "a", "fake-basic")
		s.ok(t, "a", "fake-basic")
		if c := s.call(t, "a", "openai", "fake-basic"); c.status != 429 {
			t.Fatalf("the third request of the month: %d", c.status)
		}
		// The tenant changes its zone to Tokyo: it takes effect at the end of November in UTC, the zone in force.
		e := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
		d.SetTenantZone(t, tn, "Asia/Tokyo", "", e)
		s.cache.Flush()
		c := s.call(t, "a", "openai", "fake-basic")
		if c.status != 429 {
			t.Fatalf("after the change, before its instant: %d: an exhausted month was reopened", c.status)
		}
		if got := retryAfter(t, c); !near(got, e.Sub(start), slack) {
			t.Errorf("Retry-After %s, want the time to the end of the old zone's month, %s", got, e.Sub(start))
		}
		s.real.AdvanceTo(e.Add(-time.Minute))
		if c := s.call(t, "a", "openai", "fake-basic"); c.status != 429 {
			t.Errorf("a minute before the instant: %d", c.status)
		}
		// From the instant the month of the tenant is the regular month of Tokyo, which began at 15:00Z of the 30th.
		s.real.AdvanceTo(e.Add(time.Hour))
		s.ok(t, "a", "fake-basic")
		s.ok(t, "a", "fake-basic")
		c = s.call(t, "a", "openai", "fake-basic")
		if c.status != 429 {
			t.Fatalf("the third request of the new zone's month: %d", c.status)
		}
		tokyo := mustTokyoMonth(t, e.Add(time.Hour))
		if got := retryAfter(t, c); !near(got, tokyo.End.Sub(e.Add(time.Hour)), slack) {
			t.Errorf("Retry-After %s, want the time to the end of Tokyo's month, %s", got, tokyo.End.Sub(e.Add(time.Hour)))
		}
	})

	t.Run("after the instant the new zone's day counts the usage of the hours before it", func(t *testing.T) {
		start := time.Date(2026, 11, 30, 16, 0, 0, 0, time.UTC)
		tn := d.SeedTenant(t, "zone-day")
		d.SeedLimitRule(t, tn, "tenant", "", "requests_per_day", 2)
		// Two requests of this UTC day, which are also in Tokyo's December 1st (it began at 15:00Z).
		d.SeedUsage(t, tn,
			dbtest.UsageRow{StartedAt: time.Date(2026, 11, 30, 16, 0, 0, 0, time.UTC), InputTokens: 1},
			dbtest.UsageRow{StartedAt: time.Date(2026, 11, 30, 20, 0, 0, 0, time.UTC), InputTokens: 1})
		e := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
		d.SetTenantZone(t, tn, "Asia/Tokyo", "", e)
		s := newQuotaStack(t, d, tn, stackOptions{ledger: true, start: start})

		s.real.AdvanceTo(e.Add(-30 * time.Minute))
		c := s.call(t, "a", "openai", "fake-basic")
		if c.status != 429 {
			t.Fatalf("before the instant: %d %s", c.status, c.body)
		}
		if got := retryAfter(t, c); !near(got, 30*time.Minute, slack) {
			t.Errorf("Retry-After %s just before the instant, want 30 min: the end of the old zone's day, where the new zone's period is tested afresh", got)
		}
		s.real.AdvanceTo(e.Add(time.Minute))
		c = s.call(t, "a", "openai", "fake-basic")
		if c.status != 429 {
			t.Fatalf("after the instant: %d: the new zone's day does not count the hours before it", c.status)
		}
		if want := time.Date(2026, 12, 1, 15, 0, 0, 0, time.UTC).Sub(e.Add(time.Minute)); !near(retryAfter(t, c), want, slack) {
			t.Errorf("Retry-After %s, want the time to Tokyo's midnight, %s", retryAfter(t, c), want)
		}
		s.real.AdvanceTo(time.Date(2026, 12, 1, 15, 1, 0, 0, time.UTC))
		s.ok(t, "a", "fake-basic")
	})
}

func mustTokyoMonth(t *testing.T, at time.Time) limits.Period {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	cal, err := limits.NewCalendar(loc)
	if err != nil {
		t.Fatal(err)
	}
	return cal.Of(limits.PerMonth, at)
}
