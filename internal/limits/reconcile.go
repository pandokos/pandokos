package limits

import (
	"context"
	"errors"
	"log/slog"
	"slices"
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
	foldAttempts    = 4 // reads of the pieces before a fold that keeps meeting new counters gives up until the next tick
)

var errFoldRaced = errors.New("limits: counters kept appearing inside the pieces of the fold")

// ReconcilerOptions configure a Reconciler.
type ReconcilerOptions struct {
	Store  *Memory
	Ledger Ledger
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
	if o.Store == nil || o.Ledger == nil {
		return nil, errors.New("limits: the reconciler needs a store and a ledger")
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

// lock takes the mutex of a tenant; waiting gives up with ctx. A channel that forget removed
// while it was being taken is an orphan: the lock is given back and taken again on the current one.
func (r *Reconciler) lock(ctx context.Context, t tenant.ID) (unlock func(), err error) {
	for {
		v, _ := r.locks.LoadOrStore(t, make(chan struct{}, 1))
		ch := v.(chan struct{})
		select {
		case ch <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if cur, ok := r.locks.Load(t); ok && cur.(chan struct{}) == ch {
			return func() { <-ch }, nil
		}
		<-ch
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
	tenants := r.o.Store.PeriodTenants()
	r.forget(tenants)
	for _, t := range tenants {
		if ctx.Err() != nil {
			return
		}
		if err := r.reconcile(ctx, t); err != nil && ctx.Err() == nil {
			r.o.Logger.Warn("limit reconciliation failed", slog.String("error_class", errorClass(err)))
		}
	}
}

// forget drops the lock and the last rebase of every tenant the store no longer follows; a
// lock somebody holds stays.
func (r *Reconciler) forget(live []tenant.ID) {
	keep := make(map[tenant.ID]struct{}, len(live))
	for _, t := range live {
		keep[t] = struct{}{}
	}
	r.locks.Range(func(k, v any) bool {
		if _, ok := keep[k.(tenant.ID)]; ok {
			return true
		}
		// Delete only while holding the lock, so nobody holds the channel being removed.
		ch := v.(chan struct{})
		select {
		case ch <- struct{}{}:
			r.locks.CompareAndDelete(k, v)
			<-ch
		default:
		}
		return true
	})
	r.mu.Lock()
	for t := range r.rebased {
		if _, ok := keep[t]; !ok {
			delete(r.rebased, t)
		}
	}
	r.mu.Unlock()
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
		// Pieces are cut at every start and end of a period the store holds for the tenant, so that a
		// piece lies inside every counter it is added to, whatever zones the tenant had (sprint 5.3).
		// A counter created while the ledger is read (by a Reserve) can have a boundary inside a piece:
		// Fold then refuses, atomically, and the pieces are read again with that boundary.
		folded := false
		for attempt := 0; attempt < foldAttempts && !folded; attempt++ {
			var pieces []Piece
			points := r.cutPoints(t, cut, next)
			for from := cut; from.Before(next); {
				to := next
				if len(points) > 0 {
					to, points = points[0], points[1:]
				}
				rows, err := r.o.Ledger.Usage(ctx, t, from, to, to)
				if err != nil {
					return err
				}
				pieces = append(pieces, Piece{From: from, To: to, Rows: rows})
				from = to
			}
			folded = r.o.Store.Fold(t, pieces, next)
		}
		if !folded {
			return errFoldRaced
		}
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

// cutPoints returns, ascending and without duplicates, the starts and ends of the periods the store
// holds for t that lie strictly inside (from, to).
func (r *Reconciler) cutPoints(t tenant.ID, from, to time.Time) []time.Time {
	var pts []time.Time
	for _, p := range r.o.Store.Periods(t) {
		for _, x := range [2]time.Time{p.Start, p.End} {
			if x.After(from) && x.Before(to) {
				pts = append(pts, x)
			}
		}
	}
	slices.SortFunc(pts, func(a, b time.Time) int { return a.Compare(b) })
	return slices.CompactFunc(pts, func(a, b time.Time) bool { return a.Equal(b) })
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
