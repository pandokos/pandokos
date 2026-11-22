package account

import (
	"errors"
	"sync"
)

// SignInKey is what a sign-in is counted by: the method (password, oidc) and a result of
// SignInResults. It holds nothing of a user.
type SignInKey struct{ Method, Result string }

// SignInStats is a snapshot of the sign-ins counted.
type SignInStats struct {
	Attempts map[SignInKey]int64
	Locks    int64 // accounts locked by too many failures
}

// SignInCounter counts sign-ins; the zero value is ready and it is safe for concurrent use.
type SignInCounter struct {
	mu    sync.Mutex
	n     map[SignInKey]int64
	locks int64
}

// Add counts one sign-in.
func (c *SignInCounter) Add(method, result string) {
	c.mu.Lock()
	if c.n == nil {
		c.n = map[SignInKey]int64{}
	}
	c.n[SignInKey{method, result}]++
	c.mu.Unlock()
}

// Lock counts one lock.
func (c *SignInCounter) Lock() {
	c.mu.Lock()
	c.locks++
	c.mu.Unlock()
}

// Snapshot returns a copy of the counts.
func (c *SignInCounter) Snapshot() SignInStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := make(map[SignInKey]int64, len(c.n))
	for k, v := range c.n {
		m[k] = v
	}
	return SignInStats{Attempts: m, Locks: c.locks}
}

// Stats returns the sign-ins of the service.
func (s *Service) Stats() SignInStats { return s.counter.Snapshot() }

// signInResult maps the outcome of a local sign-in to a result of SignInResults.
func signInResult(err error) string {
	if err == nil {
		return SignInOK
	}
	var r *Refusal
	if errors.As(err, &r) {
		switch r.Reason {
		case "throttled":
			return SignInLocked
		case "user_disabled":
			return SignInDisabled
		}
		return SignInFailed
	}
	return SignInUnavailable
}
