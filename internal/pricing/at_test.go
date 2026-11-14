package pricing_test

import (
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/pricing"
)

// TestVersionAt is T-372: the version in force at an instant is the one with the greatest
// ValidFrom that is not after it.
func TestVersionAt(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	mk := func(id string, from time.Time) pricing.Version {
		v := version(t, id, rate(t, catInput, 0, "1"))
		v.ValidFrom = from
		return v
	}
	v1, v2, v3 := mk("v1", t0), mk("v2", t0.Add(24*time.Hour)), mk("v3", t0.Add(48*time.Hour))
	all := []pricing.Version{v1, v2, v3}

	for _, c := range []struct {
		name string
		at   time.Time
		want string // "" for none
	}{
		{"before the first", t0.Add(-time.Nanosecond), ""},
		{"long before the first", t0.Add(-1000 * time.Hour), ""},
		{"on the first", t0, "v1"},
		{"inside the first", t0.Add(time.Hour), "v1"},
		{"one nanosecond before the second", v2.ValidFrom.Add(-time.Nanosecond), "v1"},
		{"on the second", v2.ValidFrom, "v2"},
		{"inside the second", v2.ValidFrom.Add(time.Hour), "v2"},
		{"one nanosecond before the third", v3.ValidFrom.Add(-time.Nanosecond), "v2"},
		{"on the third", v3.ValidFrom, "v3"},
		{"long after the last", v3.ValidFrom.Add(10000 * time.Hour), "v3"},
		{"in another zone", v2.ValidFrom.In(time.FixedZone("x", 5*3600)), "v2"},
	} {
		got, ok := pricing.VersionAt(all, c.at)
		if c.want == "" {
			if ok {
				t.Errorf("%s: found %s", c.name, got.ID)
			}
			continue
		}
		if !ok || got.ID != c.want {
			t.Errorf("%s: got %q (found %v), want %s", c.name, got.ID, ok, c.want)
		}
	}

	t.Run("future versions are ignored", func(t *testing.T) {
		now := time.Now().UTC()
		past, future := mk("past", now.Add(-time.Hour)), mk("future", now.Add(time.Hour))
		got, ok := pricing.VersionAt([]pricing.Version{past, future}, now)
		if !ok || got.ID != "past" {
			t.Errorf("got %q %v", got.ID, ok)
		}
		if _, ok := pricing.VersionAt([]pricing.Version{future}, now); ok {
			t.Error("a version that has not started was returned")
		}
	})

	t.Run("no versions", func(t *testing.T) {
		if _, ok := pricing.VersionAt(nil, t0); ok {
			t.Error("found a version in nothing")
		}
	})
}
