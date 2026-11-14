package catalog

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
)

const (
	// maxTenantEntries bounds the tenant access entries; a full map is emptied.
	maxTenantEntries = 10000
	minTTL           = time.Second
	maxTTL           = 5 * time.Minute
	// loadTimeout bounds one load of the catalogue, whatever the callers waiting for it do.
	loadTimeout = 15 * time.Second
)

// CacheOptions configure a Cache.
type CacheOptions struct {
	DB     *store.DB // the gateway pool
	Logger *slog.Logger
	TTL    time.Duration // cache.catalog_ttl, 1 s to 5 m
	Now    func() time.Time
}

// loader is where a Cache gets what it caches.
type loader interface {
	snapshot(ctx context.Context) (*Snapshot, error)
	access(ctx context.Context, id tenant.ID) (TenantAccess, error)
}

type dbLoader struct{ db *store.DB }

func (l dbLoader) snapshot(ctx context.Context) (*Snapshot, error) { return LoadSnapshot(ctx, l.db) }
func (l dbLoader) access(ctx context.Context, id tenant.ID) (TenantAccess, error) {
	return LoadTenantAccess(ctx, l.db, id)
}

// flight is one load in progress. Callers that arrive while it runs and that no invalidation has
// outdated wait for it instead of loading again.
type flight[T any] struct {
	done  chan struct{}
	epoch uint64
	val   T
	err   error
}

type accessEntry struct {
	ta      TenantAccess
	expires time.Time
}

// Cache is the gateway's copy of the catalogue: one snapshot and the access of each tenant that
// has asked. An entry lives for the lifetime from the start of its load; a notification drops it
// sooner; a failure is not cached. It is safe for concurrent use.
type Cache struct {
	l   loader
	log *slog.Logger
	ttl time.Duration
	now func() time.Time

	mu          sync.Mutex
	snap        *Snapshot
	snapExpires time.Time
	snapEpoch   uint64 // changes whenever the snapshot is invalidated
	snapFlight  *flight[*Snapshot]
	access      map[tenant.ID]accessEntry
	accessEpoch uint64 // changes whenever any access entry is invalidated
	flights     map[tenant.ID]*flight[TenantAccess]
}

// NewCache builds a Cache on the gateway pool. The lifetime is 1 s to 5 m; a database and a
// logger are required.
func NewCache(o CacheOptions) (*Cache, error) {
	if o.DB == nil {
		return nil, errors.New("catalog: the cache needs a database")
	}
	return newCache(dbLoader{o.DB}, o)
}

func newCache(l loader, o CacheOptions) (*Cache, error) {
	switch {
	case l == nil:
		return nil, errors.New("catalog: the cache needs a loader")
	case o.Logger == nil:
		return nil, errors.New("catalog: the cache needs a logger")
	case o.TTL < minTTL || o.TTL > maxTTL:
		return nil, errors.New("catalog: the cache lifetime must be between 1s and 5m")
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &Cache{l: l, log: o.Logger, ttl: o.TTL, now: now, access: map[tenant.ID]accessEntry{}, flights: map[tenant.ID]*flight[TenantAccess]{}}, nil
}

// Snapshot returns the global catalogue, loading it when it is absent, stale or older than the
// lifetime.
func (c *Cache) Snapshot(ctx context.Context) (*Snapshot, error) {
	c.mu.Lock()
	if c.snap != nil && c.now().Before(c.snapExpires) {
		s := c.snap
		c.mu.Unlock()
		return s, nil
	}
	f := c.snapFlight
	if f == nil || f.epoch != c.snapEpoch {
		f = &flight[*Snapshot]{done: make(chan struct{}), epoch: c.snapEpoch}
		c.snapFlight = f
		start := c.now()
		go func() {
			lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loadTimeout)
			defer cancel()
			f.val, f.err = c.l.snapshot(lctx)
			c.mu.Lock()
			if c.snapFlight == f {
				c.snapFlight = nil
			}
			if f.err == nil && f.epoch == c.snapEpoch {
				c.snap, c.snapExpires = f.val, start.Add(c.ttl)
			}
			c.mu.Unlock()
			close(f.done)
		}()
	}
	c.mu.Unlock()
	select {
	case <-f.done:
		return f.val, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Access returns the tenant's grants and restriction, loading them (one tenant transaction)
// when the entry is absent, dropped or older than the lifetime.
func (c *Cache) Access(ctx context.Context, id tenant.ID) (TenantAccess, error) {
	if id.IsZero() {
		return TenantAccess{}, store.ErrNoTenant
	}
	c.mu.Lock()
	if e, ok := c.access[id]; ok {
		if c.now().Before(e.expires) {
			c.mu.Unlock()
			return e.ta, nil
		}
		delete(c.access, id)
	}
	f := c.flights[id]
	if f == nil || f.epoch != c.accessEpoch {
		f = &flight[TenantAccess]{done: make(chan struct{}), epoch: c.accessEpoch}
		c.flights[id] = f
		start := c.now()
		go func() {
			lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loadTimeout)
			defer cancel()
			f.val, f.err = c.l.access(lctx, id)
			c.mu.Lock()
			if c.flights[id] == f {
				delete(c.flights, id)
			}
			if f.err == nil && f.epoch == c.accessEpoch {
				if _, exists := c.access[id]; !exists && len(c.access) >= maxTenantEntries {
					c.access = map[tenant.ID]accessEntry{}
					c.log.Debug("catalog cache flushed", "reason", "full")
				}
				c.access[id] = accessEntry{ta: f.val, expires: start.Add(c.ttl)}
			}
			c.mu.Unlock()
			close(f.done)
		}()
	}
	c.mu.Unlock()
	select {
	case <-f.done:
		return f.val, f.err
	case <-ctx.Done():
		return TenantAccess{}, ctx.Err()
	}
}

// Resolve is Snapshot, Access and Snapshot.Resolve. A principal that is not a service account
// is refused without loading anything of a tenant; a failed load is an error that is neither
// refusal.
func (c *Cache) Resolve(ctx context.Context, p authz.Principal, provider, name string) (Model, Endpoint, error) {
	s, err := c.Snapshot(ctx)
	if err != nil {
		return Model{}, Endpoint{}, err
	}
	var ta TenantAccess
	if p.Kind == authz.KindServiceAccount && !p.TenantID.IsZero() {
		if ta, err = c.Access(ctx, p.TenantID); err != nil {
			return Model{}, Endpoint{}, err
		}
	}
	return s.Resolve(p, ta, provider, name)
}

// Invalidate applies a notification payload: catalog marks the snapshot stale; catalog:<tenant>
// and tenant:<tenant> drop that tenant's entry; platform, probe:… and vault:… touch nothing;
// anything else, a payload this version does not know, flushes everything.
func (c *Cache) Invalidate(payload string) {
	switch {
	case payload == "catalog":
		c.mu.Lock()
		c.snapEpoch++
		c.snap = nil
		c.mu.Unlock()
		return
	case payload == "platform", strings.HasPrefix(payload, "probe:"), strings.HasPrefix(payload, "vault:"):
		return
	case strings.HasPrefix(payload, "catalog:"), strings.HasPrefix(payload, "tenant:"):
		_, rest, _ := strings.Cut(payload, ":")
		if id, err := tenant.ParseID(rest); err == nil {
			c.mu.Lock()
			c.accessEpoch++
			delete(c.access, id)
			c.mu.Unlock()
			return
		}
	}
	c.flush("unknown_payload")
}

// Flush empties the cache; the listener calls it when it loses its connection.
func (c *Cache) Flush() { c.flush("reset") }

func (c *Cache) flush(reason string) {
	c.mu.Lock()
	c.snapEpoch++
	c.accessEpoch++
	c.snap = nil
	c.access = map[tenant.ID]accessEntry{}
	c.mu.Unlock()
	c.log.Debug("catalog cache flushed", "reason", reason)
}
