// Package limitstest holds limits for tests of the pipeline that need the real counters. It is
// apart from testutil because the resolver's own tests use testutil.
package limitstest

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/resolver"
	"github.com/pandokos/pandokos/internal/tenant"

	_ "time/tzdata" // the zone of a test comes from the embedded database
)

// Real is a resolver.Limits over a real limits.Memory and a rule cache fed from a fixed rule set,
// for the tenant of InferencePrincipal's callers it is built for. Its clock is the real one plus
// an offset that Advance moves.
type Real struct {
	*resolver.Limits
	Mem *limits.Memory
	// Sprint 5.2: the calendar, the reconciler and the ledger the counters are loaded from; nil
	// on a Real built by New, ForTenants or WithCache, which have no period limits.
	Calendar   *limits.Calendar
	Reconciler *limits.Reconciler
	Ledger     limits.Ledger
	offset     atomic.Int64
	origin     time.Time // the instant the clock showed when Real was built; zero: the real time
	realOrigin time.Time // the real time at that instant
	withPeriod bool      // built by NewWith: Reserve gives the request the start time of this clock

	zmu   sync.Mutex
	zones map[tenant.ID]limits.TenantCalendar // by tenant, for the rules kept in memory (sprint 5.3)
}

// TenantCalendar is the calendar of a tenant whose rules are kept in memory: the zone of Real until
// SetZone says another (sprint 5.3, section 5.7).
func (r *Real) TenantCalendar(id tenant.ID) limits.TenantCalendar {
	r.zmu.Lock()
	defer r.zmu.Unlock()
	if c, ok := r.zones[id]; ok {
		return c
	}
	return limits.TenantCalendar{Current: r.Calendar}
}

// SetZone gives a tenant whose rules are kept in memory another zone, taking effect at the instant
// at (a test that wants the change rule of section 5.7 passes the instant limits.NextChange gives).
// The rule set of the tenant is built again at its next load.
func (r *Real) SetZone(tb testing.TB, id tenant.ID, zone string, at time.Time) {
	tb.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		tb.Fatal(err)
	}
	cal, err := limits.NewCalendar(loc)
	if err != nil {
		tb.Fatal(err)
	}
	r.zmu.Lock()
	defer r.zmu.Unlock()
	if r.zones == nil {
		r.zones = map[tenant.ID]limits.TenantCalendar{}
	}
	prev := r.Calendar
	if c, ok := r.zones[id]; ok {
		prev = c.At(at)
	}
	r.zones[id] = limits.TenantCalendar{Current: cal, Previous: prev, From: at}
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

func (r *Real) now() time.Time { return time.Now().Add(r.Shift()) }

// Advance moves the clock of the store forward.
func (r *Real) Advance(d time.Duration) { r.offset.Add(int64(d)) }

// Sweep sweeps the store at its own time.
func (r *Real) Sweep() int { return r.Mem.Sweep(r.now()) }

// Outstanding returns the reservations the store holds.
func (r *Real) Outstanding() int { return r.Mem.Outstanding() }

// Counters returns the counters the store holds.
func (r *Real) Counters() int { return r.Mem.Counters() }

// Options configure NewWith.
type Options struct {
	// Rules are the tenant rules, by tenant, with scope, currency, enforcement and thresholds;
	// Platform are the platform rows of a tenant and PlatformDefault the defaults. Ignored when
	// Cache is set.
	Rules           map[tenant.ID][]limits.Rule
	Platform        map[tenant.ID][]limits.Rule
	PlatformDefault []limits.Rule
	// Cache feeds the rules from a rule cache (for example on the test database).
	Cache *limits.RuleCache

	Zone    string        // an IANA zone; UTC when empty
	Ledger  limits.Ledger // the counters are loaded from it; an empty ledger when nil
	Events  limits.EventSink
	Pending func() (time.Time, bool) // the oldest record the ledger writer has not committed; none when nil

	// Ensurer replaces the reconciler as what loads the counters of a request (a test that makes the
	// load fail); the reconciler is built all the same.
	Ensurer resolver.Ensurer

	OnUnknownPrice string // "refuse" (default) or "allow"
	OnStoreFailure string // "refuse" (default) or "allow"

	// Unlimited is told of the first unlimited request of a tenant in a day of the tenant (sprint
	// 5.3); nobody when nil.
	Unlimited resolver.UnlimitedNoter

	Horizon  time.Duration // default 12m30s
	Interval time.Duration // default 5m
	// Start is the instant the clock of the store, of the reconciler and of every request shows
	// when Real is built; it runs at the speed of the real time from there. The real time when zero.
	Start time.Time
	Logs  io.Writer // the log of the store, the reconciler and the adapter; discarded when nil
}

type emptyLedger struct{}

func (emptyLedger) Usage(context.Context, tenant.ID, time.Time, time.Time, time.Time) ([]limits.UsageGroup, error) {
	return nil, nil
}

// NewWith returns limits with the period counters of sprint 5.2: the calendar of a zone, a
// reconciler over a ledger, an event sink, and the two settings. The clock of the store, of the
// reconciler and the start time of every request it reserves are the clock of Real: Advance and
// AdvanceTo move them, so that a test crosses a day or a month without waiting.
func NewWith(tb testing.TB, o Options) *Real {
	tb.Helper()
	logw := o.Logs
	if logw == nil {
		logw = io.Discard
	}
	log := slog.New(slog.NewJSONHandler(logw, &slog.HandlerOptions{Level: slog.LevelDebug}))
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := &Real{origin: o.Start, realOrigin: time.Now(), withPeriod: true}
	zone := o.Zone
	if zone == "" {
		zone = "UTC"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		tb.Fatal(err)
	}
	r.Calendar, err = limits.NewCalendar(loc)
	if err != nil {
		tb.Fatal(err)
	}
	r.Mem, err = limits.NewMemory(limits.MemoryOptions{Logger: log, Now: r.now, Events: o.Events})
	if err != nil {
		tb.Fatal(err)
	}
	r.Ledger = o.Ledger
	if r.Ledger == nil {
		r.Ledger = emptyLedger{}
	}
	horizon, interval := o.Horizon, o.Interval
	if horizon == 0 {
		horizon = 12*time.Minute + 30*time.Second
	}
	if interval == 0 {
		interval = 5 * time.Minute
	}
	r.Reconciler, err = limits.NewReconciler(limits.ReconcilerOptions{Store: r.Mem, Ledger: r.Ledger,
		Pending: o.Pending, Horizon: horizon, Interval: interval, Logger: log, Now: r.now})
	if err != nil {
		tb.Fatal(err)
	}
	cache := o.Cache
	if cache == nil {
		cache, err = limits.NewRuleCacheFunc(func(_ context.Context, id tenant.ID) (limits.RuleSet, error) {
			rs := limits.NewScopedRuleSet(id, o.PlatformDefault, o.Platform[id], o.Rules[id])
			rs.Calendar = r.TenantCalendar(id)
			return rs, nil
		}, limits.RuleCacheOptions{Logger: quiet, TTL: 5 * time.Minute})
		if err != nil {
			tb.Fatal(err)
		}
	}
	var ensurer resolver.Ensurer = r.Reconciler
	if o.Ensurer != nil {
		ensurer = o.Ensurer
	}
	r.Limits, err = resolver.NewLimits(resolver.LimitsOptions{Rules: cache, Store: r.Mem, TTL: time.Hour, Logger: log,
		Reconciler: ensurer, OnUnknownPrice: o.OnUnknownPrice, OnStoreFailure: o.OnStoreFailure,
		Unlimited: o.Unlimited})
	if err != nil {
		tb.Fatal(err)
	}
	return r
}

// Shift is how far the clock of Real is ahead of the real time: what Start and Advance added.
func (r *Real) Shift() time.Duration {
	if r.origin.IsZero() {
		return time.Duration(r.offset.Load())
	}
	return r.origin.Sub(r.realOrigin) + time.Duration(r.offset.Load())
}

// Reserve reserves at the clock of Real: the start time of the request is the one the gateway took
// moved by Shift, so that a test that starts the clock elsewhere or advances it moves the periods
// the request belongs to; with a Shift of 0 it is the very instant of the usage record.
func (r *Real) Reserve(ctx context.Context, p authz.Principal, e gateway.LimitEstimate) (gateway.Reservation, *gateway.LimitRefusal, gateway.LimitState, error) {
	if r.withPeriod {
		e.Start = e.Start.Add(r.Shift())
	}
	return r.Limits.Reserve(ctx, p, e)
}

// Now is the clock of the store.
func (r *Real) Now() time.Time { return r.now() }

// AdvanceTo moves the clock to t (forward only).
func (r *Real) AdvanceTo(t time.Time) {
	if d := t.Sub(r.now()); d > 0 {
		r.Advance(d)
	}
}

// Tick runs one pass of the reconciler, as its loop does every interval.
func (r *Real) Tick(ctx context.Context) {
	if r.Reconciler != nil {
		r.Reconciler.Tick(ctx)
	}
}
