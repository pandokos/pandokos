package limits

import (
	"testing"
	"time"
)

// TestWindow is T-610.
func TestWindow(t *testing.T) {
	base := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

	t.Run("an amount is counted for at least 60 s and less than 61 s at every sub-second offset", func(t *testing.T) {
		for ms := 0; ms < 1000; ms += 37 {
			at := base.Add(time.Duration(ms) * time.Millisecond)
			second := at.Truncate(time.Second)
			var w window
			w.add(at, 5)
			if n := w.count(at); n != 5 {
				t.Fatalf("offset %d ms: %d counted at once", ms, n)
			}
			if n := w.count(at.Add(60 * time.Second)); n != 5 {
				t.Fatalf("offset %d ms: %d counted after 60 s, want 5", ms, n)
			}
			if n := w.count(second.Add(61*time.Second - time.Nanosecond)); n != 5 {
				t.Fatalf("offset %d ms: %d counted just before the second left, want 5", ms, n)
			}
			if n := w.count(second.Add(61 * time.Second)); n != 0 {
				t.Fatalf("offset %d ms: %d counted at 61 s from the second, want 0", ms, n)
			}
		}
	})

	t.Run("a gap longer than the window empties it", func(t *testing.T) {
		var w window
		w.add(base, 7)
		w.add(base.Add(30*time.Second), 3)
		if n := w.count(base.Add(40 * time.Second)); n != 10 {
			t.Fatalf("count %d, want 10", n)
		}
		if n := w.count(base.Add(10 * time.Minute)); n != 0 {
			t.Fatalf("count %d after a gap, want 0", n)
		}
		w.add(base.Add(10*time.Minute), 2)
		if n := w.count(base.Add(10 * time.Minute)); n != 2 {
			t.Fatalf("count %d, want 2: old amounts came back", n)
		}
	})

	t.Run("sub takes back from a second that is still in the window only", func(t *testing.T) {
		var w window
		w.add(base, 10)
		w.add(base.Add(5*time.Second), 4)
		w.sub(base, base.Add(10*time.Second), 3)
		if n := w.count(base.Add(10 * time.Second)); n != 11 {
			t.Fatalf("count %d, want 11", n)
		}
		now := base.Add(62 * time.Second)
		before := w.count(now)
		w.sub(base, now, 6) // the second of base has left the window: nothing changes
		if n := w.count(now); n != before || n != 4 {
			t.Fatalf("count %d (before %d), want 4 and no change", n, before)
		}
	})

	t.Run("totals equal the sum of live seconds after any sequence of advances", func(t *testing.T) {
		seed := seedOf(t)
		for i := 0; i < 200; i++ {
			rng := rngOf(seed, i)
			var w window
			type ev struct {
				at time.Time
				n  int64
			}
			var evs []ev
			now := base
			for step := 0; step < 120; step++ {
				switch rng.IntN(3) {
				case 0:
					now = now.Add(time.Duration(rng.IntN(3000)) * time.Millisecond)
				case 1:
					now = now.Add(time.Duration(rng.IntN(120)) * time.Second)
				}
				n := int64(rng.IntN(50))
				w.add(now, n)
				evs = append(evs, ev{now, n})
				var want int64
				for _, e := range evs {
					if now.Before(e.at.Truncate(time.Second).Add(61 * time.Second)) {
						want += e.n
					}
				}
				if got := w.count(now); got != want {
					t.Fatalf("schedule %d step %d: count %d, want %d", i, step, got, want)
				}
			}
		}
	})
}
