package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/vault"
)

// Relay delivers the events of every tenant to its destinations: it claims destinations with a
// lease, reads the events after the cursor of each and delivers them in order (sprint 6.4,
// section 5.6). The worker role runs it.
type Relay struct {
	DB          *store.DB
	Provider    vault.KeyProvider
	Deliverer   *Deliverer
	Source      string
	Interval    time.Duration
	Workers     int
	BatchSize   int
	MaxAttempts int
	BackoffMax  time.Duration
	Logger      *slog.Logger
	Now         func() time.Time
}

// RelayStats holds the counters behind the instruments M52 to M58. Nothing in it names a tenant, a
// destination or a URL.
type RelayStats struct {
	// Deliveries counts the requests sent, by outcome (M52).
	Deliveries map[Outcome]uint64
	// DeliveriesOK and DeliveriesFailed count the requests by result, and the *Seconds fields add
	// up their time (M53).
	DeliveriesOK, DeliveriesFailed           uint64
	DeliverySecondsOK, DeliverySecondsFailed float64
	DeadLetters                              uint64  // events parked (M54)
	Destinations, Failing, Disabled          int64   // the gauges of M56, as the last state read
	LagSeconds                               float64 // M57
	PassesOK, PassesFailed                   uint64  // M58
}

// Run passes until ctx ends.
func (r *Relay) Run(ctx context.Context) { <-ctx.Done() }

// Pass claims and settles one round of destinations; it returns how many rounds it ran.
func (r *Relay) Pass(ctx context.Context) (rounds int, err error) {
	_ = ctx
	return 0, errNotBuilt
}

// Stats returns a copy of the counters.
func (r *Relay) Stats() RelayStats { return RelayStats{} }
