// Package limitstest holds limits for tests of the pipeline that need the real counters. It is
// apart from testutil because the resolver's own tests use testutil.
package limitstest

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/resolver"
	"github.com/pandokos/pandokos/internal/tenant"
)

// Real is a resolver.Limits over a real limits.Memory and a rule cache fed from a fixed rule set,
// for the tenant of InferencePrincipal's callers it is built for. Its clock is the real one plus
// an offset that Advance moves.
type Real struct {
	*resolver.Limits
	Mem    *limits.Memory
	offset atomic.Int64
}

// New returns limits that enforce rules for the tenant of the given principal.
func New(tb testing.TB, p authz.Principal, rules ...limits.Rule) *Real {
	tb.Helper()
	return ForTenants(tb, map[tenant.ID][]limits.Rule{p.TenantID: rules})
}

// ForTenants returns limits that enforce the rules of each tenant of the map; a tenant that is
// not in it has no rule.
func ForTenants(tb testing.TB, rules map[tenant.ID][]limits.Rule) *Real {
	tb.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := &Real{}
	mem, err := limits.NewMemory(limits.MemoryOptions{Logger: quiet, Now: r.now})
	if err != nil {
		tb.Fatal(err)
	}
	cache, err := limits.NewRuleCacheFunc(func(_ context.Context, id tenant.ID) (limits.RuleSet, error) {
		rs, ok := rules[id]
		if !ok {
			return limits.NewRuleSet(id, nil, nil, nil), nil
		}
		return limits.NewRuleSet(id, nil, nil, rs), nil
	}, limits.RuleCacheOptions{Logger: quiet, TTL: 5 * time.Minute})
	if err != nil {
		tb.Fatal(err)
	}
	l, err := resolver.NewLimits(resolver.LimitsOptions{Rules: cache, Store: mem, TTL: time.Hour, Logger: quiet})
	if err != nil {
		tb.Fatal(err)
	}
	r.Limits, r.Mem = l, mem
	return r
}

// WithCache returns limits over a rule cache the test built (for example on the test database) and
// a new store, so that no test outside this package names the store.
func WithCache(tb testing.TB, cache *limits.RuleCache) *Real {
	tb.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := &Real{}
	mem, err := limits.NewMemory(limits.MemoryOptions{Logger: quiet, Now: r.now})
	if err != nil {
		tb.Fatal(err)
	}
	l, err := resolver.NewLimits(resolver.LimitsOptions{Rules: cache, Store: mem, TTL: time.Hour, Logger: quiet})
	if err != nil {
		tb.Fatal(err)
	}
	r.Limits, r.Mem = l, mem
	return r
}

func (r *Real) now() time.Time { return time.Now().Add(time.Duration(r.offset.Load())) }

// Advance moves the clock of the store forward.
func (r *Real) Advance(d time.Duration) { r.offset.Add(int64(d)) }

// Sweep sweeps the store at its own time.
func (r *Real) Sweep() int { return r.Mem.Sweep(r.now()) }

// Outstanding returns the reservations the store holds.
func (r *Real) Outstanding() int { return r.Mem.Outstanding() }

// Counters returns the counters the store holds.
func (r *Real) Counters() int { return r.Mem.Counters() }
