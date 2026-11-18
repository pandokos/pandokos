package resolver

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/tenant"
)

// Ensurer makes sure the period counters of limits are loaded from the ledger (the limits.Reconciler).
type Ensurer interface {
	Ensure(ctx context.Context, t tenant.ID, ls []limits.Limit) error
}

// LimitsOptions configure Limits.
type LimitsOptions struct {
	Rules  *limits.RuleCache
	Store  limits.Store
	TTL    time.Duration // the lifetime of a reservation the store was not told about
	Logger *slog.Logger
	// Sprint 5.2: the calendar of the period limits (nil: they do not apply, as in 5.1), what loads
	// their counters, and the two settings that say what a request that cannot be measured or
	// verified meets: "refuse" (the default) or "allow".
	Calendar       *limits.Calendar
	Reconciler     Ensurer
	OnUnknownPrice string
	OnStoreFailure string
}

// Limits implements gateway.Limits over the rule cache and a store of counters.
type Limits struct {
	rules       *limits.RuleCache
	store       limits.Store
	ttl         time.Duration
	log         *slog.Logger
	cal         *limits.Calendar
	ensurer     Ensurer
	refusePrice bool // on_unknown_price is "refuse" (the default)
	refuseStore bool // on_store_failure is "refuse" (the default)

	mu      sync.Mutex
	noticed map[tenant.ID]time.Time
}

var _ gateway.Limits = (*Limits)(nil)

// NewLimits returns the gateway's view of the limits. The rule cache, the store and the logger
// are required.
func NewLimits(o LimitsOptions) (*Limits, error) {
	switch {
	case o.Rules == nil:
		return nil, errors.New("resolver: limits need a rule cache")
	case o.Store == nil:
		return nil, errors.New("resolver: limits need a store")
	case o.Logger == nil:
		return nil, errors.New("resolver: limits need a logger")
	}
	return &Limits{
		rules: o.Rules, store: o.Store, ttl: o.TTL, log: o.Logger, cal: o.Calendar, ensurer: o.Reconciler,
		refusePrice: o.OnUnknownPrice != "allow", refuseStore: o.OnStoreFailure != "allow",
		noticed: map[tenant.ID]time.Time{},
	}, nil
}

// settleTimeout bounds a call to the store made after the request ended.
const settleTimeout = 2 * time.Second

// noReservation is the reservation of a call no limit applies to: it does nothing.
type noReservation struct{}

func (noReservation) Settle(gateway.LimitUsage) {}
func (noReservation) Release()                  {}

// reservation ends a reservation of the store. It uses a context of its own: the request's is
// often cancelled by the time the request ends.
type reservation struct {
	l   *Limits
	id  string
	res limits.Reservation
}

func (r *reservation) Settle(u gateway.LimitUsage) {
	ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
	defer cancel()
	r.log(r.l.store.Settle(ctx, r.res, limits.Amounts{Tokens: u.Tokens, Cost: u.Cost}))
}

func (r *reservation) Release() {
	ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
	defer cancel()
	r.log(r.l.store.Release(ctx, r.res))
}

func (r *reservation) log(err error) {
	if err != nil {
		r.l.log.Error("limit settle failed", "request_id", r.id, "error", err.Error())
	}
}

// missingStore is implemented by a store that knows which period counters it cannot serve.
type missingStore interface {
	Missing(ls []limits.Limit) []limits.Limit
}

// noticeEvery is how often, per tenant, the line "limit counters unavailable" may appear.
const noticeEvery = 10 * time.Second

// marked is the reservation of a call that holds nothing in the store, or whose limits were
// partly left out: it says so through Unpriced and Unverified.
type marked struct {
	inner      gateway.Reservation
	unpriced   bool
	unverified bool
}

func (m *marked) Settle(u gateway.LimitUsage) { m.inner.Settle(u) }
func (m *marked) Release()                    { m.inner.Release() }
func (m *marked) Unpriced() bool              { return m.unpriced }
func (m *marked) Unverified() bool            { return m.unverified }

// wrap returns res itself unless a mark applies.
func wrap(res gateway.Reservation, unpriced, unverified bool) gateway.Reservation {
	if !unpriced && !unverified {
		return res
	}
	return &marked{inner: res, unpriced: unpriced, unverified: unverified}
}

// unavailable logs, at most once per tenant and noticeEvery, that counters could not be used.
func (l *Limits) unavailable(t tenant.ID, err error) {
	now := time.Now()
	l.mu.Lock()
	if last, ok := l.noticed[t]; ok && now.Sub(last) < noticeEvery {
		l.mu.Unlock()
		return
	}
	if len(l.noticed) >= 1024 {
		for k, v := range l.noticed {
			if now.Sub(v) >= noticeEvery {
				delete(l.noticed, k)
			}
		}
	}
	l.noticed[t] = now
	l.mu.Unlock()
	l.log.Warn("limit counters unavailable", "tenant_id", t.String(), "error_class", errorClass(err))
}

// errorClass names the kind of an error without its text.
func errorClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, limits.ErrNotLoaded):
		return "not_loaded"
	}
	return "store"
}

// scopeOf says how narrow a limit is.
func scopeOf(l limits.Limit) string {
	switch {
	case l.Model != "":
		return "model"
	case l.Provider != "":
		return "provider"
	}
	return "all"
}

// Reserve reserves the estimate against the limits of the principal's service account, key and
// tenant. A principal no limit applies to gets a reservation that does nothing.
func (l *Limits) Reserve(ctx context.Context, p authz.Principal, e gateway.LimitEstimate) (gateway.Reservation, *gateway.LimitRefusal, error) {
	if p.Kind != authz.KindServiceAccount || p.TenantID == (tenant.ID{}) {
		return nil, nil, errors.New("resolver: limits need a service account principal with a tenant")
	}
	rules, err := l.rules.Rules(ctx, p.TenantID)
	if err != nil {
		return nil, nil, err
	}
	ls := rules.For(limits.Query{ServiceAccountID: p.ID, KeyID: p.KeyID, Provider: e.Provider, Model: e.Model, Start: e.Start}, l.cal)
	if len(ls) == 0 {
		return noReservation{}, nil, nil
	}

	// Unpriced: a cost limit whose currency the estimate cannot be priced in.
	var unpriced, enforced bool
	for _, x := range ls {
		if cannotMeasure(x, e) {
			unpriced = true
			// An unenforced budget (threshold events only) never refuses: enforce false.
			enforced = enforced || x.Max > 0
		}
	}
	if unpriced {
		if l.refusePrice && enforced {
			return nil, &gateway.LimitRefusal{Code: string(family.CodeNoPrice)}, nil
		}
		kept := make([]limits.Limit, 0, len(ls))
		for _, x := range ls {
			if !cannotMeasure(x, e) {
				kept = append(kept, x)
			}
		}
		ls = kept
	}

	unverified := false
	hasPeriod := false
	for _, x := range ls {
		if _, ok := x.Metric.Period(); ok {
			hasPeriod = true
			break
		}
	}
	if hasPeriod && l.ensurer != nil {
		if err := l.ensurer.Ensure(ctx, p.TenantID, ls); err != nil {
			if l.refuseStore || ctx.Err() != nil {
				return nil, nil, err
			}
			l.unavailable(p.TenantID, err)
			unverified = true
			if ms, ok := l.store.(missingStore); ok {
				ls = without(ls, ms.Missing(ls))
			}
		}
	}
	if len(ls) == 0 {
		return wrap(noReservation{}, unpriced, unverified), nil, nil
	}

	req := limits.Request{
		ID:       e.RequestID,
		Limits:   ls,
		Start:    e.Start,
		Estimate: limits.Amounts{Tokens: e.InputTokens + e.OutputTokens, Cost: e.Cost},
		TTL:      l.ttl,
	}
	res, ref, err := l.store.Reserve(ctx, req)
	if errors.Is(err, limits.ErrNotLoaded) && hasPeriod && l.ensurer != nil && ctx.Err() == nil {
		// A sweep dropped the counter or the cut between the load and the reservation: load it
		// again, once, before the failure policy applies.
		if err = l.ensurer.Ensure(ctx, p.TenantID, ls); err == nil {
			res, ref, err = l.store.Reserve(ctx, req)
		}
	}
	switch {
	case err != nil:
		if l.refuseStore || ctx.Err() != nil {
			return nil, nil, err
		}
		l.unavailable(p.TenantID, err)
		return wrap(noReservation{}, unpriced, true), nil, nil
	case ref != nil:
		out := &gateway.LimitRefusal{
			Code:  string(family.CodeRateLimited),
			Level: ref.Limit.Level, Subject: string(ref.Limit.Subject), Metric: string(ref.Limit.Metric),
			Scope: scopeOf(ref.Limit),
			Limit: ref.Limit.Max, Remaining: ref.Remaining, RetryAfter: ref.RetryAfter, Reset: ref.Reset,
			OverLimit: ref.OverLimit,
		}
		if _, ok := ref.Limit.Metric.Period(); ok {
			out.Code = string(family.CodeQuotaExceeded)
		}
		if ref.Limit.Metric.Cost() {
			out.Currency = ref.Limit.Currency
		}
		return nil, out, nil
	}
	return wrap(&reservation{l: l, id: e.RequestID, res: res}, unpriced, unverified), nil, nil
}

// cannotMeasure reports whether x is a cost limit the estimate has no price in the currency of.
func cannotMeasure(x limits.Limit, e gateway.LimitEstimate) bool {
	return x.Metric.Cost() && (!e.Cost.Known || e.Cost.Currency != x.Currency)
}

// without returns ls minus the limits in drop.
func without(ls, drop []limits.Limit) []limits.Limit {
	if len(drop) == 0 {
		return ls
	}
	out := make([]limits.Limit, 0, len(ls))
next:
	for _, x := range ls {
		for _, d := range drop {
			if x.RuleID == d.RuleID && x.Subject == d.Subject && x.ID == d.ID && x.Metric == d.Metric &&
				x.Provider == d.Provider && x.Model == d.Model && x.Period == d.Period {
				continue next
			}
		}
		out = append(out, x)
	}
	return out
}
