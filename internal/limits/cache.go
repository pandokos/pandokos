package limits

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
)

const (
	// maxRuleEntries bounds the entries of the cache; a full map is emptied.
	maxRuleEntries = 10000
	minRuleTTL     = time.Second
	maxRuleTTL     = 5 * time.Minute
	// ruleLoadTimeout bounds one load, whatever the callers waiting for it do.
	ruleLoadTimeout = 15 * time.Second
)

// RuleCacheOptions configure a RuleCache.
type RuleCacheOptions struct {
	DB     *store.DB // the gateway pool
	Logger *slog.Logger
	TTL    time.Duration // cache.limits_ttl, 1 s to 5 m
	Now    func() time.Time
	// Zones gives the calendar of each tenant's zone (sprint 5.3, C2).
	Zones *Zones
	// DefaultCurrency is pricing.default_currency (sprint 5.4): the active currency of a tenant that
	// chose none.
	DefaultCurrency string
}

type ruleEntry struct {
	set     RuleSet
	expires time.Time
}

// ruleFlight is one load in progress. Callers that arrive while it runs and that no invalidation
// has outdated wait for it instead of loading again.
type ruleFlight struct {
	done  chan struct{}
	epoch uint64
	val   RuleSet
	err   error
}

// RuleCache holds the RuleSet of each tenant that has asked. An entry lives for the lifetime from
// the start of its load; a notification drops it sooner; a failure is not cached. It is safe for
// concurrent use.
type RuleCache struct {
	load func(context.Context, tenant.ID) (RuleSet, int, error)
	log  *slog.Logger
	ttl  time.Duration
	now  func() time.Time

	mu      sync.Mutex
	entries map[tenant.ID]ruleEntry
	flights map[tenant.ID]*ruleFlight
	epoch   uint64 // changes whenever any entry is invalidated

	loadsOK, loadsFailed atomic.Int64
}

// RuleCacheStats is a snapshot of the loads of a RuleCache.
type RuleCacheStats struct{ OK, Failed int64 }

// Stats returns the counters: loads, not lookups.
func (c *RuleCache) Stats() RuleCacheStats {
	return RuleCacheStats{OK: c.loadsOK.Load(), Failed: c.loadsFailed.Load()}
}

// NewRuleCache builds a RuleCache on the gateway pool.
func NewRuleCache(o RuleCacheOptions) (*RuleCache, error) {
	if o.DB == nil {
		return nil, errors.New("limits: the rule cache needs a database")
	}
	if o.Zones == nil {
		return nil, errors.New("limits: the rule cache needs the zones of the installation")
	}
	return newCountingRuleCache(func(ctx context.Context, id tenant.ID) (RuleSet, int, error) {
		return loadRules(ctx, o.DB, id, o.Zones, o.DefaultCurrency)
	}, o)
}

// NewRuleCacheFunc builds a RuleCache whose loader is load, for tests and tools that keep the rules
// in memory instead of in the database.
func NewRuleCacheFunc(load func(context.Context, tenant.ID) (RuleSet, error), o RuleCacheOptions) (*RuleCache, error) {
	return newRuleCache(load, o)
}

func newRuleCache(load func(context.Context, tenant.ID) (RuleSet, error), o RuleCacheOptions) (*RuleCache, error) {
	if load == nil {
		return newCountingRuleCache(nil, o)
	}
	return newCountingRuleCache(func(ctx context.Context, id tenant.ID) (RuleSet, int, error) {
		set, err := load(ctx, id)
		return set, 0, err
	}, o)
}

// newCountingRuleCache is newRuleCache with a loader that also returns how many rules it skipped.
func newCountingRuleCache(load func(context.Context, tenant.ID) (RuleSet, int, error), o RuleCacheOptions) (*RuleCache, error) {
	switch {
	case load == nil:
		return nil, errors.New("limits: the rule cache needs a loader")
	case o.Logger == nil:
		return nil, errors.New("limits: the rule cache needs a logger")
	case o.TTL < minRuleTTL || o.TTL > maxRuleTTL:
		return nil, errors.New("limits: the rule cache lifetime must be between 1s and 5m")
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &RuleCache{load: load, log: o.Logger, ttl: o.TTL, now: now,
		entries: map[tenant.ID]ruleEntry{}, flights: map[tenant.ID]*ruleFlight{}}, nil
}

// Rules returns the rules of a tenant, loading them (one tenant transaction) when the entry is
// absent, dropped or older than the lifetime.
func (c *RuleCache) Rules(ctx context.Context, id tenant.ID) (RuleSet, error) {
	if id.IsZero() {
		return RuleSet{}, store.ErrNoTenant
	}
	c.mu.Lock()
	if e, ok := c.entries[id]; ok {
		if c.now().Before(e.expires) {
			c.mu.Unlock()
			return e.set, nil
		}
		delete(c.entries, id)
	}
	f := c.flights[id]
	if f == nil || f.epoch != c.epoch {
		f = &ruleFlight{done: make(chan struct{}), epoch: c.epoch}
		c.flights[id] = f
		start := c.now()
		go func() {
			lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ruleLoadTimeout)
			defer cancel()
			var skipped int
			f.val, skipped, f.err = c.load(lctx, id)
			if f.err != nil {
				c.loadsFailed.Add(1)
				c.log.Warn("limit rules unavailable")
			} else {
				c.loadsOK.Add(1)
			}
			if f.err == nil && skipped > 0 {
				c.log.Debug("limit rules of an unknown metric or subject skipped", "skipped", skipped)
			}
			c.mu.Lock()
			if c.flights[id] == f {
				delete(c.flights, id)
			}
			if f.err == nil && f.epoch == c.epoch {
				if _, exists := c.entries[id]; !exists && len(c.entries) >= maxRuleEntries {
					c.entries = map[tenant.ID]ruleEntry{}
					c.log.Debug("limit rule cache flushed", "reason", "full")
				}
				c.entries[id] = ruleEntry{set: f.val, expires: start.Add(c.ttl)}
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
		return RuleSet{}, ctx.Err()
	}
}

// Invalidate applies a notification payload: limits:<tenant> and tenant:<tenant> drop that
// tenant's entry; limits drops every entry; platform, issuers, catalog, catalog:…, vault:… and
// probe:… touch nothing; anything else, a payload this version does not know, flushes everything.
func (c *RuleCache) Invalidate(payload string) {
	switch {
	case payload == "limits":
		c.flush("limits")
		return
	case payload == "platform", payload == "issuers", payload == "catalog", strings.HasPrefix(payload, "catalog:"),
		strings.HasPrefix(payload, "vault:"), strings.HasPrefix(payload, "probe:"):
		return
	case strings.HasPrefix(payload, "limits:"), strings.HasPrefix(payload, "tenant:"):
		_, rest, _ := strings.Cut(payload, ":")
		if id, err := tenant.ParseID(rest); err == nil {
			c.mu.Lock()
			c.epoch++
			delete(c.entries, id)
			c.mu.Unlock()
			return
		}
	}
	c.flush("unknown_payload")
}

// Flush empties the cache; the listener calls it each time it (re)connects, because notifications
// may have been missed.
func (c *RuleCache) Flush() { c.flush("reset") }

func (c *RuleCache) flush(reason string) {
	c.mu.Lock()
	c.epoch++
	c.entries = map[tenant.ID]ruleEntry{}
	c.mu.Unlock()
	c.log.Debug("limit rule cache flushed", "reason", reason)
}
