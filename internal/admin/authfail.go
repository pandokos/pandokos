package admin

import (
	"sync"
	"time"
)

// failureLimiter is a token bucket for the audit of authentication failures: they can be caused
// by anyone who reaches the listener, so at most capacity are written at once and one more each
// second. Beyond that a failure is only logged.
type failureLimiter struct {
	mu     sync.Mutex
	now    func() time.Time
	tokens float64
	last   time.Time
}

const (
	limiterCapacity = 60
	limiterRate     = 1.0 // tokens per second
)

func newFailureLimiter(now func() time.Time) *failureLimiter {
	return &failureLimiter{now: now, tokens: limiterCapacity, last: now()}
}

func (l *failureLimiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.now()
	if d := t.Sub(l.last).Seconds(); d > 0 {
		l.tokens += d * limiterRate
		if l.tokens > limiterCapacity {
			l.tokens = limiterCapacity
		}
		l.last = t
	}
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}
