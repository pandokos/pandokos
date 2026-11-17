package limits

import "time"

// window is the sliding window of one counter: a ring of 61 buckets of one second. An amount
// added at time t belongs to the bucket of second floor(t) and is counted while
// now < floor(t) + 61 s. Not implemented yet.
type window struct{}

// add attributes n to the second of at.
func (w *window) add(time.Time, int64) {}

// sub takes n back from the second of at, if that second is still in the window at now.
func (w *window) sub(time.Time, time.Time, int64) {}

// count returns the total of the seconds still in the window at now.
func (w *window) count(time.Time) int64 { return 0 }
