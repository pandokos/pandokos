package resolver

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/limits"
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

// Reserve is not implemented yet.
func (l *Limits) Reserve(context.Context, authz.Principal, gateway.LimitEstimate) (gateway.Reservation, *gateway.LimitRefusal, error) {
	return nil, nil, errors.New("resolver: limits are not implemented")
}
