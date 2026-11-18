package limits

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
)

const (
	defaultHorizon  = 12*time.Minute + 30*time.Second
	defaultInterval = 5 * time.Minute
	rebaseEvery     = time.Hour
	ensureTimeout   = 5 * time.Second
)

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

	locks sync.Map // tenant.ID -> chan struct{}: the mutex of a tenant, which waiting can give up

	mu      sync.Mutex
	rebased map[tenant.ID]time.Time // the last rebase of a tenant; the load counts as the first
}

// NewReconciler returns a Reconciler.
func NewReconciler(o ReconcilerOptions) (*Reconciler, error) {
	if o.Store == nil || o.Ledger == nil || o.Calendar == nil {
		return nil, errors.New("limits: the reconciler needs a store, a ledger and a calendar")
	}
	if o.Horizon <= 0 {
		o.Horizon = defaultHorizon
	}
	if o.Interval <= 0 {
		o.Interval = defaultInterval
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Reconciler{o: o, rebased: map[tenant.ID]time.Time{}}, nil
}

// lock takes the mutex of a tenant; waiting gives up with ctx.
func (r *Reconciler) lock(ctx context.Context, t tenant.ID) (unlock func(), err error) {
	v, _ := r.locks.LoadOrStore(t, make(chan struct{}, 1))
	ch := v.(chan struct{})
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// cut is the cut at now: now minus the horizon or the start of the oldest record not yet
// committed, whichever is earlier, to the minute.
func (r *Reconciler) cut(now time.Time) time.Time {
	c := now.Add(-r.o.Horizon)
	if r.o.Pending != nil {
		if p, ok := r.o.Pending(); ok && p.Before(c) {
			c = p
		}
	}
	return c.Truncate(time.Minute)
}

func earlier(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// Ensure makes sure the period counters of ls are loaded. When there is nothing to load it
// takes no lock of its own.
func (r *Reconciler) Ensure(ctx context.Context, t tenant.ID, ls []Limit) error {
	if len(r.o.Store.NeedsLoad(ls)) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, ensureTimeout)
	defer cancel()
	unlock, err := r.lock(ctx, t)
	if err != nil {
		return err
	}
	defer unlock()
	missing := r.o.Store.NeedsLoad(ls)
	if len(missing) == 0 {
		return nil
	}
	now := r.o.Now()
	cut, has := r.o.Store.Cut(t)
	if !has {
		cut = r.cut(now)
	}
	var loads []PeriodLoad
next:
	for _, l := range missing {
		for i := range loads {
			if samePeriod(loads[i].Period, l.Period) {
				loads[i].Limits = append(loads[i].Limits, l)
				continue next
			}
		}
		loads = append(loads, PeriodLoad{Period: l.Period, Limits: []Limit{l}})
	}
	for i := range loads {
		rows, err := r.o.Ledger.Usage(ctx, t, loads[i].Period.Start, cut, earlier(now, loads[i].Period.End))
		if err != nil {
			return err
		}
		loads[i].Rows = rows
	}
	// Nothing was changed until every query succeeded; the cut and the counters appear together.
	r.o.Store.Install(t, cut, loads)
	r.mu.Lock()
	if _, ok := r.rebased[t]; !ok {
		r.rebased[t] = now
	}
	r.mu.Unlock()
	return nil
}

// Run folds and rebases every Interval until ctx is done.
func (r *Reconciler) Run(ctx context.Context) {
	tk := time.NewTicker(r.o.Interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			r.Tick(ctx)
		}
	}
}

// Tick is one pass of Run, for tests.
func (r *Reconciler) Tick(ctx context.Context) {
	for _, t := range r.o.Store.PeriodTenants() {
		if ctx.Err() != nil {
			return
		}
		if err := r.reconcile(ctx, t); err != nil && ctx.Err() == nil {
			r.o.Logger.Warn("limit reconciliation failed", slog.String("tenant", t.String()), slog.String("error_class", errorClass(err)))
		}
	}
}

// reconcile folds the tenant to the new cut and rebases it when an hour has passed; every query
// comes before the first change, so a failure leaves the tenant as it was.
func (r *Reconciler) reconcile(ctx context.Context, t tenant.ID) error {
	unlock, err := r.lock(ctx, t)
	if err != nil {
		return err
	}
	defer unlock()
	cut, ok := r.o.Store.Cut(t)
	if !ok {
		return nil
	}
	now := r.o.Now()
	if next := r.cut(now); next.After(cut) {
		var pieces []Piece
		for from := cut; from.Before(next); {
			to := earlier(r.o.Calendar.Of(PerDay, from).End, next)
			rows, err := r.o.Ledger.Usage(ctx, t, from, to, to)
			if err != nil {
				return err
			}
			pieces = append(pieces, Piece{From: from, To: to, Rows: rows})
			from = to
		}
		r.o.Store.Fold(t, pieces, next)
		cut = next
	}
	r.mu.Lock()
	last, seen := r.rebased[t]
	if !seen {
		r.rebased[t] = now
		last = now
	}
	r.mu.Unlock()
	if now.Sub(last) < rebaseEvery {
		return nil
	}
	// A period that ended before the cut is read up to its end, not up to the cut.
	for _, p := range r.o.Store.Periods(t) {
		end := earlier(cut, p.End)
		if !end.After(p.Start) {
			continue
		}
		rows, err := r.o.Ledger.Usage(ctx, t, p.Start, end, end)
		if err != nil {
			return err
		}
		r.o.Store.Rebase(t, p, rows)
	}
	r.mu.Lock()
	r.rebased[t] = now
	r.mu.Unlock()
	return nil
}

// errorClass names the kind of an error without its text.
func errorClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case store.IsUnavailable(err):
		return "unavailable"
	case store.SQLState(err) != "":
		return "database"
	default:
		return "other"
	}
}
