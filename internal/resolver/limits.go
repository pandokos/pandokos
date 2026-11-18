package resolver

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/tenant"
)

// LimitsOptions configure Limits.
type LimitsOptions struct {
	Rules  *limits.RuleCache
	Store  limits.Store
	TTL    time.Duration // the lifetime of a reservation the store was not told about
	Logger *slog.Logger
}

// Limits implements gateway.Limits over the rule cache and a store of counters.
type Limits struct {
	rules *limits.RuleCache
	store limits.Store
	ttl   time.Duration
	log   *slog.Logger
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
	return &Limits{rules: o.Rules, store: o.Store, ttl: o.TTL, log: o.Logger}, nil
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
	ls := rules.For(limits.Query{ServiceAccountID: p.ID, KeyID: p.KeyID}, nil)
	if len(ls) == 0 {
		return noReservation{}, nil, nil
	}
	res, ref, err := l.store.Reserve(ctx, limits.Request{
		ID:       e.RequestID,
		Limits:   ls,
		Estimate: limits.Amounts{Tokens: e.InputTokens + e.OutputTokens, Cost: e.Cost},
		TTL:      l.ttl,
	})
	switch {
	case err != nil:
		return nil, nil, err
	case ref != nil:
		return nil, &gateway.LimitRefusal{
			Level: ref.Limit.Level, Subject: string(ref.Limit.Subject), Metric: string(ref.Limit.Metric),
			Limit: ref.Limit.Max, Remaining: ref.Remaining, RetryAfter: ref.RetryAfter, Reset: ref.Reset,
			OverLimit: ref.OverLimit,
		}, nil
	}
	return &reservation{l: l, id: e.RequestID, res: res}, nil, nil
}
