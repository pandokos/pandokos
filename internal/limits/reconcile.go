package limits

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/pandokos/pandokos/internal/tenant"
)

// errNotImplemented is what the parts of sprint 5.2 that are still scaffolding answer.
var errNotImplemented = errors.New("limits: not implemented")

// ReconcilerOptions configure a Reconciler.
type ReconcilerOptions struct {
	Store    *Memory
	Ledger   Ledger
	Calendar *Calendar
	// Pending returns the start time of the oldest usage record handed to the ledger writer and
	// not yet committed; false when there is none.
	Pending  func() (time.Time, bool)
	Horizon  time.Duration
	Interval time.Duration
	Logger   *slog.Logger
	Now      func() time.Time
}

// Reconciler loads period counters from the ledger and folds them into it.
type Reconciler struct {
	o ReconcilerOptions
}

// NewReconciler returns a Reconciler.
func NewReconciler(o ReconcilerOptions) (*Reconciler, error) {
	return &Reconciler{o: o}, nil
}

// Ensure makes sure the period counters of ls are loaded.
func (r *Reconciler) Ensure(_ context.Context, _ tenant.ID, _ []Limit) error {
	return errNotImplemented
}

// Run folds and rebases every Interval until ctx is done.
func (r *Reconciler) Run(_ context.Context) {}

// Tick is one pass of Run, for tests.
func (r *Reconciler) Tick(_ context.Context) {}
