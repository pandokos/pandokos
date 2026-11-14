package authn

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/pandokos/pandokos/internal/store/queries"
)

const (
	maxEntries  = 10000
	negativeTTL = 5 * time.Second
)

// entry is what the cache holds for a key: the row read in the tenant's session.
type entry struct {
	row queries.GetKeyPrincipalRow
}

type positive struct {
	e        *entry
	tenantID string
	expires  time.Time
}

// cache is the principal cache of the gateway authenticator: positive entries live for the
// configured lifetime from their creation however often they are read; negative entries (key IDs
// that belong to nobody) for five seconds.
type cache struct {
	mu       sync.Mutex
	now      func() time.Time
	ttl      time.Duration
	log      *slog.Logger
	pos      map[string]positive
	neg      map[string]time.Time
	byTenant map[string]map[string]bool
}

func newCache(now func() time.Time, ttl time.Duration, log *slog.Logger) *cache {
	return &cache{now: now, ttl: ttl, log: log, pos: map[string]positive{}, neg: map[string]time.Time{}, byTenant: map[string]map[string]bool{}}
}

func (c *cache) get(keyID string) (*entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.pos[keyID]
	if !ok {
		return nil, false
	}
	if !c.now().Before(p.expires) {
		c.removeLocked(keyID)
		return nil, false
	}
	return p.e, true
}

func (c *cache) removeLocked(keyID string) {
	if p, ok := c.pos[keyID]; ok {
		delete(c.pos, keyID)
		if set := c.byTenant[p.tenantID]; set != nil {
			delete(set, keyID)
			if len(set) == 0 {
				delete(c.byTenant, p.tenantID)
			}
		}
	}
}

func (c *cache) put(keyID, tenantID string, e *entry) { c.putAt(keyID, tenantID, e, c.now()) }

// putAt caches e for the lifetime counted from start, the moment the lookup began: the bound on
// how long a revocation can go unseen does not grow with the latency of the reads.
func (c *cache) putAt(keyID, tenantID string, e *entry, start time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.pos[keyID]; !exists && len(c.pos) >= maxEntries {
		c.flushLocked("full")
	}
	c.removeLocked(keyID)
	c.pos[keyID] = positive{e: e, tenantID: tenantID, expires: start.Add(c.ttl)}
	if c.byTenant[tenantID] == nil {
		c.byTenant[tenantID] = map[string]bool{}
	}
	c.byTenant[tenantID][keyID] = true
}

func (c *cache) putNegative(keyID string) { c.putNegativeAt(keyID, c.now()) }

// putNegativeAt is putAt for an unknown key.
func (c *cache) putNegativeAt(keyID string, start time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.neg[keyID]; !exists && len(c.neg) >= maxEntries {
		c.neg = map[string]time.Time{}
		c.log.Debug("principal cache flushed", "reason", "full")
	}
	c.neg[keyID] = start.Add(negativeTTL)
}

func (c *cache) negative(keyID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	exp, ok := c.neg[keyID]
	if !ok {
		return false
	}
	if !c.now().Before(exp) {
		delete(c.neg, keyID)
		return false
	}
	return true
}

func (c *cache) flushLocked(reason string) {
	c.pos = map[string]positive{}
	c.neg = map[string]time.Time{}
	c.byTenant = map[string]map[string]bool{}
	c.log.Debug("principal cache flushed", "reason", reason)
}

func (c *cache) flush(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flushLocked(reason)
}

// invalidate applies a notification payload (sprint 1.3, section 5.8).
func (c *cache) invalidate(payload string) {
	switch {
	case payload == "platform", strings.HasPrefix(payload, "probe:"), strings.HasPrefix(payload, "vault:"):
		return
	case strings.HasPrefix(payload, "tenant:") && len(payload) > len("tenant:"):
		id := strings.TrimPrefix(payload, "tenant:")
		c.mu.Lock()
		defer c.mu.Unlock()
		for keyID := range c.byTenant[id] {
			delete(c.pos, keyID)
		}
		delete(c.byTenant, id)
	default:
		c.flush("unknown_payload")
	}
}

func (c *cache) sizes() (positive, negative int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pos), len(c.neg)
}
