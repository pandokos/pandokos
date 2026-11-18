package limits

import "time"

const windowBuckets = 61

// window is the sliding window of one counter: a ring of 61 buckets of one second. An amount
// added at time t belongs to the bucket of second floor(t) and is counted while
// now < floor(t) + 61 s, so every amount is counted for at least 60 s and less than 61 s.
//
// Time never goes back in a window: a second that is not after the newest one is treated as the
// newest. A window is not safe for concurrent use; the owner holds a lock.
type window struct {
	buckets [windowBuckets]int64
	total   int64
	newest  int64 // the second of the newest bucket, valid once started is set
	started bool
}

func secondOf(t time.Time) int64 { return t.Unix() }

func slot(sec int64) int {
	i := int(sec % windowBuckets)
	if i < 0 {
		i += windowBuckets
	}
	return i
}

// advance moves the window to the second sec: the buckets that left are zeroed and subtracted
// from the total; a jump of 61 s or more empties it. A second that is not after the newest one is
// a no-op.
func (w *window) advance(sec int64) {
	if !w.started {
		w.started = true
		w.newest = sec
		return
	}
	if sec <= w.newest {
		return
	}
	if sec-w.newest >= windowBuckets {
		w.buckets = [windowBuckets]int64{}
		w.total = 0
	} else {
		for s := w.newest + 1; s <= sec; s++ {
			i := slot(s)
			w.total -= w.buckets[i]
			w.buckets[i] = 0
		}
	}
	w.newest = sec
}

// add attributes n to the second of at, or to the newest second when at is earlier (time never
// goes back), and returns the second it was attributed to.
func (w *window) add(at time.Time, n int64) int64 {
	w.advance(secondOf(at))
	w.buckets[slot(w.newest)] += n
	w.total += n
	return w.newest
}

// sub takes n back from the second of at, if that second is still in the window at now. A
// negative n adds.
func (w *window) sub(at, now time.Time, n int64) { w.subSec(secondOf(at), now, n) }

// subSec is sub for a second already known, as returned by add.
func (w *window) subSec(s int64, now time.Time, n int64) {
	w.advance(secondOf(now))
	if s > w.newest {
		s = w.newest
	}
	if w.newest-s >= windowBuckets {
		return
	}
	w.buckets[slot(s)] -= n
	w.total -= n
}

// count returns the total of the seconds still in the window at now.
func (w *window) count(now time.Time) int64 {
	w.advance(secondOf(now))
	return w.total
}

// leaves returns how long after now enough of the oldest counted seconds have left for need
// units to be freed, rounded up to a second and kept between 1 s and 61 s. The window must have
// been advanced to now.
func (w *window) leaves(now time.Time, need int64) time.Duration {
	var acc int64
	for s := w.newest - (windowBuckets - 1); s <= w.newest; s++ {
		acc += w.buckets[slot(s)]
		if acc >= need {
			return clampRetry(time.Unix(s+windowBuckets, 0).Sub(now))
		}
	}
	return windowBuckets * time.Second
}

// reset returns the time until the newest counted second leaves; 0 for an empty window.
func (w *window) reset(now time.Time) time.Duration {
	for s := w.newest; s > w.newest-windowBuckets; s-- {
		if w.buckets[slot(s)] != 0 {
			return time.Unix(s+windowBuckets, 0).Sub(now)
		}
	}
	return 0
}

func clampRetry(d time.Duration) time.Duration {
	d = (d + time.Second - 1) / time.Second * time.Second
	if d < time.Second {
		d = time.Second
	}
	if d > windowBuckets*time.Second {
		d = windowBuckets * time.Second
	}
	return d
}
