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
	case payload == "platform", payload == "issuers", strings.HasPrefix(payload, "probe:"), strings.HasPrefix(payload, "vault:"),
		payload == "catalog", strings.HasPrefix(payload, "catalog:"), payload == "limits", strings.HasPrefix(payload, "limits:"):
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

// lookupCache is the cache of the token path (section 5.6), used twice: for the registration
// selected by an issuer and audiences, and for the principal of a subject. Positive entries live
// for the configured lifetime counted from the start of the lookup; negative ones, which carry the
// reason of the refusal, for five seconds. Positive entries are indexed by tenant, and by "a
// platform-level registration", so that the payloads tenant:<id> and issuers drop exactly what
// section 5.6 says. A nil *lookupCache caches nothing.
type lookupCache[V any] struct {
	mu       sync.Mutex
	now      func() time.Time
	ttl      time.Duration
	log      *slog.Logger
	kind     string // "registration" or "token principal", for the log
	pos      map[string]lookupEntry[V]
	neg      map[string]negativeEntry
	byTenant map[string]map[string]bool
	platform map[string]bool
}

type lookupEntry[V any] struct {
	v        V
	tenantID string // "" for a registration that is not tenant-bound
	platform bool
	expires  time.Time
}

type negativeEntry struct {
	reason  string
	detail  string // what the failure names besides the reason: the tenant of a mapping that is missing
	expires time.Time
}

func newLookupCache[V any](kind string, now func() time.Time, ttl time.Duration, log *slog.Logger) *lookupCache[V] {
	c := &lookupCache[V]{now: now, ttl: ttl, log: log, kind: kind}
	c.resetLocked()
	return c
}

func (c *lookupCache[V]) resetLocked() {
	c.pos = map[string]lookupEntry[V]{}
	c.neg = map[string]negativeEntry{}
	c.byTenant = map[string]map[string]bool{}
	c.platform = map[string]bool{}
}

func (c *lookupCache[V]) get(key string) (V, bool) {
	var zero V
	if c == nil {
		return zero, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.pos[key]
	if !ok {
		return zero, false
	}
	if !c.now().Before(e.expires) {
		c.removeLocked(key)
		return zero, false
	}
	return e.v, true
}

func (c *lookupCache[V]) removeLocked(key string) {
	e, ok := c.pos[key]
	if !ok {
		return
	}
	delete(c.pos, key)
	delete(c.platform, key)
	if set := c.byTenant[e.tenantID]; set != nil {
		delete(set, key)
		if len(set) == 0 {
			delete(c.byTenant, e.tenantID)
		}
	}
}

// putAt caches v for the lifetime counted from start, the moment the lookup began.
func (c *lookupCache[V]) putAt(key, tenantID string, platform bool, v V, start time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.pos[key]; !exists && len(c.pos) >= maxEntries {
		c.resetLocked()
		c.log.Debug("lookup cache flushed", "cache", c.kind, "reason", "full")
	}
	c.removeLocked(key)
	c.pos[key] = lookupEntry[V]{v: v, tenantID: tenantID, platform: platform, expires: start.Add(c.ttl)}
	if tenantID != "" {
		if c.byTenant[tenantID] == nil {
			c.byTenant[tenantID] = map[string]bool{}
		}
		c.byTenant[tenantID][key] = true
	}
	if platform {
		c.platform[key] = true
	}
}

// putNegativeAt remembers a refusal for five seconds from start.
func (c *lookupCache[V]) putNegativeAt(key, reason, detail string, start time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.neg[key]; !exists && len(c.neg) >= maxEntries {
		c.neg = map[string]negativeEntry{}
		c.log.Debug("lookup cache flushed", "cache", c.kind, "reason", "full")
	}
	c.neg[key] = negativeEntry{reason: reason, detail: detail, expires: start.Add(negativeTTL)}
}

// negative returns the reason remembered for key, and its detail.
func (c *lookupCache[V]) negative(key string) (reason, detail string, ok bool) {
	if c == nil {
		return "", "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, found := c.neg[key]
	if !found {
		return "", "", false
	}
	if !c.now().Before(e.expires) {
		delete(c.neg, key)
		return "", "", false
	}
	return e.reason, e.detail, true
}

func (c *lookupCache[V]) flush(reason string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetLocked()
	c.log.Debug("lookup cache flushed", "cache", c.kind, "reason", reason)
}

// invalidate applies a notification payload: tenant:<id> drops the entries of that tenant,
// issuers those of platform-level registrations; the payloads of the other caches change nothing;
// a payload nobody knows flushes (ADR 0030).
func (c *lookupCache[V]) invalidate(payload string) {
	if c == nil {
		return
	}
	switch {
	case payload == "platform", strings.HasPrefix(payload, "probe:"), strings.HasPrefix(payload, "vault:"),
		payload == "catalog", strings.HasPrefix(payload, "catalog:"), payload == "limits", strings.HasPrefix(payload, "limits:"):
		return
	case payload == "issuers":
		c.mu.Lock()
		defer c.mu.Unlock()
		for key := range c.platform {
			c.removeLocked(key)
		}
	case strings.HasPrefix(payload, "tenant:") && len(payload) > len("tenant:"):
		id := strings.TrimPrefix(payload, "tenant:")
		c.mu.Lock()
		defer c.mu.Unlock()
		for key := range c.byTenant[id] {
			delete(c.pos, key)
			delete(c.platform, key)
		}
		delete(c.byTenant, id)
	default:
		c.flush("unknown_payload")
	}
}
