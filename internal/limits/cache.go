package limits

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
)

// RuleCacheOptions configure a RuleCache.
type RuleCacheOptions struct {
	DB     *store.DB // the gateway pool
	Logger *slog.Logger
	TTL    time.Duration // cache.limits_ttl, 1 s to 5 m
	Now    func() time.Time
}

// RuleCache holds the RuleSet of each tenant that has asked.
type RuleCache struct {
	load func(context.Context, tenant.ID) (RuleSet, error)
	log  *slog.Logger
	ttl  time.Duration
	now  func() time.Time
}

// NewRuleCache builds a RuleCache on the gateway pool.
func NewRuleCache(o RuleCacheOptions) (*RuleCache, error) {
	if o.DB == nil {
		return nil, errors.New("limits: the rule cache needs a database")
	}
	return newRuleCache(func(ctx context.Context, id tenant.ID) (RuleSet, error) {
		return LoadRules(ctx, o.DB, id)
	}, o)
}

// NewRuleCacheFunc builds a RuleCache whose loader is load, for tests and tools that keep the rules
// in memory instead of in the database.
func NewRuleCacheFunc(load func(context.Context, tenant.ID) (RuleSet, error), o RuleCacheOptions) (*RuleCache, error) {
	return newRuleCache(load, o)
}

func newRuleCache(load func(context.Context, tenant.ID) (RuleSet, error), o RuleCacheOptions) (*RuleCache, error) {
	if load == nil {
		return nil, errors.New("limits: the rule cache needs a loader")
	}
	if o.Logger == nil {
		return nil, errors.New("limits: the rule cache needs a logger")
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &RuleCache{load: load, log: o.Logger, ttl: o.TTL, now: now}, nil
}

// Rules returns the rules of a tenant. Not implemented yet.
func (c *RuleCache) Rules(context.Context, tenant.ID) (RuleSet, error) {
	return RuleSet{}, errNotImplemented
}

// Invalidate drops what a notification payload says changed. Not implemented yet.
func (c *RuleCache) Invalidate(string) {}

// Flush drops every entry. Not implemented yet.
func (c *RuleCache) Flush() {}
