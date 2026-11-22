package jwks

import (
	"errors"
	"sync/atomic"
	"time"
)

// FetchKey is what a fetch is counted by: the document (keys or metadata) and its result (ok or a
// class of FetchClasses). It holds no issuer and no URL.
type FetchKey struct{ Document, Result string }

// Stats is a snapshot of the counters of a Cache.
type Stats struct {
	Fetches map[FetchKey]int64
	// Sources counts the key sources held by state: fresh (a success within twice the refresh
	// interval), stale (keys held, older) and missing (no usable key held).
	Sources map[string]int64
	// OldestStale is the time of the last success of the oldest stale source; zero when none is stale.
	OldestStale time.Time
}

func (c *Cache) countFetch(document string, err error) {
	k := FetchKey{Document: document, Result: "ok"}
	if err != nil {
		k.Result = classOther
		var fe *fetchError
		if errors.As(err, &fe) {
			k.Result = fe.class
		}
	}
	n, ok := c.fetches.Load(k)
	if !ok {
		n, _ = c.fetches.LoadOrStore(k, new(atomic.Int64))
	}
	n.(*atomic.Int64).Add(1)
}

// classOther is the class of a failure that is none of the others; it is never produced by the
// fetch code, which always returns a fetchError.
const classOther = classNetwork

// Stats returns a copy of the counters and the states of the sources now.
func (c *Cache) Stats() Stats {
	st := Stats{Fetches: map[FetchKey]int64{}, Sources: map[string]int64{"fresh": 0, "stale": 0, "missing": 0}}
	c.fetches.Range(func(k, v any) bool {
		st.Fetches[k.(FetchKey)] = v.(*atomic.Int64).Load()
		return true
	})
	now := c.o.Now()
	c.mu.Lock()
	entries := make([]*entry, 0, len(c.byKey))
	for _, el := range c.byKey {
		entries = append(entries, el.Value.(*entry))
	}
	c.mu.Unlock()
	for _, e := range entries {
		e.mu.Lock()
		have, at := e.haveKeys, e.fetchedAt
		e.mu.Unlock()
		switch age := now.Sub(at); {
		case !have || age > c.o.MaxStale:
			st.Sources["missing"]++
		case age <= 2*c.o.RefreshInterval:
			st.Sources["fresh"]++
		default:
			st.Sources["stale"]++
			if st.OldestStale.IsZero() || at.Before(st.OldestStale) {
				st.OldestStale = at
			}
		}
	}
	return st
}
