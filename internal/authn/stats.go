package authn

import (
	"errors"
	"sync/atomic"

	"github.com/pandokos/pandokos/internal/authz"
)

// Attempt is what a call of Authenticate is counted by: the kind of credential (key, token, or
// unknown for a value that is neither), the result (ok, failed, unavailable) and, for a failure,
// its reason (one of Reasons). It holds no key, tenant, issuer or subject.
type Attempt struct{ Kind, Result, Reason string }

// Stats is a snapshot of the counters of an Authenticator.
type Stats struct {
	Attempts map[Attempt]int64
}

func (a *Authenticator) count(kind string, err error) {
	if a.attempts == nil {
		return
	}
	k := Attempt{Kind: kind, Result: "ok"}
	var f *Failure
	switch {
	case err == nil:
	case errors.Is(err, authz.ErrUnavailable):
		k.Result = "unavailable"
	case errors.As(err, &f):
		k.Result, k.Reason = "failed", f.Reason
	default:
		k.Result = "failed"
	}
	c, ok := a.attempts.Load(k)
	if !ok {
		c, _ = a.attempts.LoadOrStore(k, new(atomic.Int64))
	}
	c.(*atomic.Int64).Add(1)
}

// Stats returns a copy of the counters of this instance.
func (a *Authenticator) Stats() Stats {
	m := map[Attempt]int64{}
	if a.attempts == nil {
		return Stats{Attempts: m}
	}
	a.attempts.Range(func(k, v any) bool {
		m[k.(Attempt)] = v.(*atomic.Int64).Load()
		return true
	})
	return Stats{Attempts: m}
}
