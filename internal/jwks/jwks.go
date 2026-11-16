// Package jwks retrieves and caches the verification keys of the token issuers a gateway trusts
// (sprint 4.1, section 5.5). It is the only package that fetches an issuer's documents, and it does
// so only through the transport function it is given.
package jwks

import (
	"container/list"
	"context"
	"crypto"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
)

// ErrKeyNotFound is returned by Key when the issuer's key set has no usable key for the token. It
// is a refusal of the token, not an outage.
var ErrKeyNotFound = errors.New("jwks: no usable key for this token")

// Source says where the keys of one issuer are: its URL, and the address of its key set.
type Source struct {
	Issuer  string // the issuer URL of the registration
	JWKSURI string // the address of the key set; empty: OpenID Connect discovery under the issuer
}

// Options configure a Cache.
type Options struct {
	// Transport returns the round tripper that fetches the issuer's documents; private says that
	// the host is one of tokens.private_hosts. Required.
	Transport func(private bool) http.RoundTripper
	// PrivateHosts are the lower-case host names that may resolve to private addresses.
	PrivateHosts []string
	// FetchTimeout bounds one fetch; RefreshInterval is the period of the background refresh;
	// MinRefetchInterval spaces the refetches caused by an unknown key ID, per source; MaxStale is
	// how long keys are used after the last successful fetch. All must be positive, and MaxStale at
	// least twice RefreshInterval.
	FetchTimeout, RefreshInterval, MinRefetchInterval, MaxStale time.Duration
	Logger                                                      *slog.Logger // required
	Now                                                         func() time.Time
}

const maxSources = 1024 // entries of the cache; over that the least recently used is dropped

// Cache holds the keys of the issuers: one entry per Source (section 5.5).
type Cache struct {
	o            Options
	hosts        map[string]bool
	public, priv *http.Client
	mu           sync.Mutex // guards lru and byKey
	lru          *list.List // of *entry, most recently used first
	byKey        map[Source]*list.Element
}

// entry is the state of one source. Its mutex guards every field; a fetch in flight runs without
// holding it.
type entry struct {
	src         Source
	mu          sync.Mutex
	keys        []usableKey
	haveKeys    bool
	fetchedAt   time.Time     // the start of the last successful fetch
	attemptedAt time.Time     // the start of the last fetch, successful or not; zero: none
	inflight    chan struct{} // closed when the fetch in flight ends; nil: none
}

// New validates o and returns a Cache.
func New(o Options) (*Cache, error) {
	switch {
	case o.Transport == nil:
		return nil, errors.New("jwks: Transport is required")
	case o.Logger == nil:
		return nil, errors.New("jwks: Logger is required")
	case o.FetchTimeout <= 0 || o.RefreshInterval <= 0 || o.MinRefetchInterval <= 0 || o.MaxStale <= 0:
		return nil, errors.New("jwks: every duration must be positive")
	case o.MaxStale < 2*o.RefreshInterval:
		return nil, fmt.Errorf("jwks: MaxStale must be at least twice RefreshInterval")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	c := &Cache{o: o, hosts: map[string]bool{}, lru: list.New(), byKey: map[Source]*list.Element{}}
	for _, h := range o.PrivateHosts {
		c.hosts[strings.ToLower(h)] = true
	}
	client := func(private bool) *http.Client {
		return &http.Client{
			Transport:     o.Transport(private),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	c.public, c.priv = client(false), client(true)
	return c, nil
}

// entryFor returns the entry of src, making it, and marks it most recently used.
func (c *Cache) entryFor(src Source) *entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.byKey[src]; ok {
		c.lru.MoveToFront(el)
		return el.Value.(*entry)
	}
	e := &entry{src: src}
	c.byKey[src] = c.lru.PushFront(e)
	if c.lru.Len() > maxSources {
		last := c.lru.Back()
		c.lru.Remove(last)
		delete(c.byKey, last.Value.(*entry).src)
	}
	return e
}

func unavailable(why string) error {
	return fmt.Errorf("%w: issuer keys: %s", authz.ErrUnavailable, why)
}

// Key returns the key that verifies a token signed with alg and the key ID kid. The error is
// ErrKeyNotFound for a refusal, and wraps authz.ErrUnavailable when the keys cannot be obtained
// now.
//
// The state rules (section 5.5): no keys and no attempt within MinRefetchInterval: fetch and wait;
// no keys and a recent attempt: unavailable at once; keys older than MaxStale are dropped first;
// keys older than RefreshInterval: refresh in the background and answer from memory; a key ID that
// is not found and no attempt within MinRefetchInterval: fetch, wait, look again.
func (c *Cache) Key(ctx context.Context, src Source, kid, alg string) (crypto.PublicKey, error) {
	e := c.entryFor(src)
	fetched := false // this call started or waited for a fetch: it does not start another
	for {
		e.mu.Lock()
		now := c.o.Now()
		if e.haveKeys && now.Sub(e.fetchedAt) > c.o.MaxStale {
			e.keys, e.haveKeys = nil, false
		}
		var wait chan struct{}
		if e.haveKeys {
			if pub, ok := find(e.keys, kid, alg); ok {
				if e.inflight == nil && now.Sub(e.fetchedAt) >= c.o.RefreshInterval && c.mayAttempt(e, now) {
					c.start(e, now)
				}
				e.mu.Unlock()
				return pub, nil
			}
			switch {
			case e.inflight != nil && !fetched:
				wait = e.inflight
			case e.inflight == nil && !fetched && c.mayAttempt(e, now):
				wait = c.start(e, now)
			default:
				e.mu.Unlock()
				return nil, ErrKeyNotFound
			}
		} else {
			switch {
			case e.inflight != nil && !fetched:
				wait = e.inflight
			case e.inflight == nil && !fetched && c.mayAttempt(e, now):
				wait = c.start(e, now)
			default:
				e.mu.Unlock()
				return nil, unavailable("none obtained")
			}
		}
		e.mu.Unlock()
		fetched = true
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, unavailable("gave up waiting")
		}
	}
}

// mayAttempt reports whether a fetch may start now: none was attempted within MinRefetchInterval.
// e.mu is held.
func (c *Cache) mayAttempt(e *entry, now time.Time) bool {
	return e.attemptedAt.IsZero() || now.Sub(e.attemptedAt) >= c.o.MinRefetchInterval
}

// start begins a fetch detached from every caller and returns the channel that closes when it
// ends. e.mu is held and no fetch is in flight.
func (c *Cache) start(e *entry, now time.Time) chan struct{} {
	done := make(chan struct{})
	e.inflight = done
	e.attemptedAt = now
	go func() {
		defer func() {
			e.mu.Lock()
			e.inflight = nil
			e.mu.Unlock()
			close(done)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), c.o.FetchTimeout)
		defer cancel()
		began := time.Now()
		keys, err := c.fetch(ctx, e.src)
		if err != nil {
			var fe *fetchError
			attrs := []any{"issuer", logIssuer(e.src.Issuer)}
			if errors.As(err, &fe) {
				attrs = append(attrs, "class", fe.class)
				if fe.reason != "" {
					attrs = append(attrs, "reason", fe.reason)
				}
			}
			c.o.Logger.Warn("issuer keys not fetched", attrs...)
			return
		}
		e.mu.Lock()
		e.keys, e.haveKeys, e.fetchedAt = keys, true, now
		e.mu.Unlock()
		c.o.Logger.Info("issuer keys fetched", "issuer", logIssuer(e.src.Issuer), "keys", len(keys), "duration", time.Since(began).String())
	}()
	return done
}

// logIssuer is the issuer without user information, query or fragment: all that is ever logged
// of an address.
func logIssuer(issuer string) string {
	u, err := url.Parse(issuer)
	if err != nil {
		return ""
	}
	u.User, u.RawQuery, u.Fragment, u.RawFragment = nil, "", "", ""
	return u.String()
}
