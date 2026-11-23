package outbox

import (
	"testing"
	"time"
)

// TestBackoff is T-1028: for every attempt count and maximum the wait is within the envelope of
// section 5.7 (5 s doubling each failure, capped at the maximum, less a random part of up to 20 %),
// never above the maximum and never below 80 % of the nominal value.
func TestBackoff(t *testing.T) {
	nominal := func(failures int, max time.Duration) time.Duration {
		n := 5 * time.Second
		for i := 1; i < failures && n < max; i++ {
			n *= 2
		}
		if n > max {
			n = max
		}
		return n
	}
	for _, max := range []time.Duration{5 * time.Second, 12 * time.Second, 30 * time.Second, 15 * time.Minute, time.Hour, 24 * time.Hour} {
		for failures := 1; failures <= 70; failures++ {
			for _, r := range []float64{0, 0.001, 0.25, 0.5, 0.75, 0.999999} {
				got := Backoff(failures, max, func() float64 { return r })
				nom := nominal(failures, max)
				if got > nom || got > max {
					t.Fatalf("failures %d, max %v, random %v: %v is above the nominal %v", failures, max, r, got, nom)
				}
				if got < nom*8/10-time.Millisecond {
					t.Fatalf("failures %d, max %v, random %v: %v is below 80%% of the nominal %v", failures, max, r, got, nom)
				}
			}
		}
	}
	// The first waits, with no random part, are the series of the design.
	for failures, want := range map[int]time.Duration{1: 5 * time.Second, 2: 10 * time.Second, 3: 20 * time.Second, 4: 40 * time.Second, 5: 80 * time.Second, 8: 640 * time.Second, 9: 15 * time.Minute, 10: 15 * time.Minute} {
		if got := Backoff(failures, 15*time.Minute, func() float64 { return 0 }); got != want {
			t.Errorf("failures %d: %v, want %v", failures, got, want)
		}
	}
	// The whole random part is 20 % and no more.
	if got := Backoff(3, time.Hour, func() float64 { return 0.9999999 }); got < 16*time.Second || got > 16*time.Second+time.Millisecond {
		t.Errorf("the largest reduction of 20 s is %v, want 16 s", got)
	}
	// The waits of the ten failures of the defaults add up to about fifty-one minutes (section 5.7).
	var sum time.Duration
	for failures := 1; failures <= 10; failures++ {
		sum += Backoff(failures, 15*time.Minute, func() float64 { return 0 })
	}
	if sum < 51*time.Minute || sum > 52*time.Minute {
		t.Errorf("the waits of ten failures add up to %v, want about 51 minutes", sum)
	}
	// A huge number of failures does not overflow.
	for _, failures := range []int{1000, 1 << 30} {
		if got := Backoff(failures, 24*time.Hour, func() float64 { return 0 }); got != 24*time.Hour {
			t.Errorf("failures %d: %v", failures, got)
		}
	}
}
