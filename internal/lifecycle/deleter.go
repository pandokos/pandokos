package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

const (
	// Batch is the number of rows a call of the erasure step removes at most.
	Batch = 5000
	// tenantBudget is the time one pass spends on one tenant.
	tenantBudget = 30 * time.Second
	// warnEvery limits the line "tenant deletion failing".
	warnEvery = time.Minute

	// The answers of the step function that stop the loop of a tenant, and its steps.
	answerDone      = "done"
	answerScheduled = "scheduled"
	answerSettling  = "settling"
	stepBegin       = "begin"
	stepAccess      = "access"
	stepUsage       = "usage"
	stepComplete    = "complete"
	stepUnknown     = "unknown"
)

// DeleterStats is what the deletion job counts. Nothing in it names a tenant.
type DeleterStats struct {
	Steps map[[2]string]int64 // by step and result (ok, failed)
	Open  map[string]int64    // open deletions by state: scheduled, running
}

// Deleter runs the open deletions (§5.7). It calls the erasure step function of the database in
// the session of each tenant and nothing else: it holds no statement that writes a tenant's rows.
type Deleter struct {
	DB       *store.DB
	Interval time.Duration
	Logger   *slog.Logger
	Now      func() time.Time

	mu         sync.Mutex
	steps      map[[2]string]int64
	open       map[string]int64
	at         map[string]string // the step each tenant's deletion is at, as the last answer said
	failures   int64
	lastWarned time.Time
}

func (d *Deleter) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.New(slog.DiscardHandler)
}

func (d *Deleter) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Run says that the job is ready, then passes at once and at every tick until ctx ends.
func (d *Deleter) Run(ctx context.Context) {
	interval := d.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		_ = d.Pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Pass runs one pass over the open deletions. A tenant that fails is counted and logged and the
// others go on; the step that failed is tried again at the next pass. The error returned is the
// first failure and never carries a tenant's data.
func (d *Deleter) Pass(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var open []queries.ListOpenTenantDeletionsRow
	var counts []queries.CountTenantDeletionsRow
	err := d.DB.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		var err error
		if open, err = q.ListOpenTenantDeletions(ctx); err != nil {
			return err
		}
		counts, err = q.CountTenantDeletions(ctx)
		return err
	})
	if err != nil {
		if ctx.Err() == nil {
			d.fail(stepUnknown, err)
		}
		return err
	}
	d.mu.Lock()
	d.open = map[string]int64{"scheduled": 0, "running": 0}
	for _, c := range counts {
		d.open[c.State] = c.N
	}
	d.mu.Unlock()

	var first error
	succeeded, failed := false, false
	for _, row := range open {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ok, err := d.tenant(ctx, row)
		if ok {
			succeeded = true
		}
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			failed = true
			if first == nil {
				first = err
			}
		}
	}
	if succeeded && !failed {
		d.recovered()
	}
	if first == nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return first
}

// tenant calls the step function for one tenant until the deletion waits, ends, fails or the
// budget is spent. ok says that at least one call succeeded.
func (d *Deleter) tenant(ctx context.Context, row queries.ListOpenTenantDeletionsRow) (ok bool, err error) {
	id, perr := tenant.ParseID(row.TenantID)
	if perr != nil {
		return false, errors.New("lifecycle: the identifier of an open deletion is not a tenant ID")
	}
	deadline := d.now().Add(tenantBudget)
	var begunAt time.Time
	for {
		if err := ctx.Err(); err != nil {
			return ok, err
		}
		at := d.stepOf(row)
		started := d.now()
		var next string
		err := d.DB.WithTenant(ctx, id, func(ctx context.Context, tx store.Tx) error {
			var err error
			next, err = queries.New(tx).TenantErasureStep(ctx, Batch)
			return err
		})
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return ok, cerr
			}
			d.count(at, "failed")
			d.fail(at, err)
			return ok, err
		}
		ok = true
		d.advance(row.TenantID, at, next)
		if at == stepBegin && next == stepAccess {
			begunAt = started
			d.logger().Info("tenant deletion started", "request_id", row.RequestID)
		}
		switch next {
		case answerDone:
			since := begunAt
			if since.IsZero() && row.StartedAt != nil {
				since = *row.StartedAt
			}
			dur := int64(0)
			if !since.IsZero() {
				dur = max(d.now().Sub(since).Milliseconds(), 0)
			}
			d.logger().Info("tenant deletion completed", "request_id", row.RequestID, "duration_ms", dur)
			return ok, nil
		case answerScheduled, answerSettling:
			return ok, nil
		}
		if !d.now().Before(deadline) {
			return ok, nil
		}
	}
}

// stepOf is the step a tenant's deletion is at: what the last answer said in this process, begin
// for a deletion that has not started, and unknown for one a previous process started.
func (d *Deleter) stepOf(row queries.ListOpenTenantDeletionsRow) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s, ok := d.at[row.TenantID]; ok {
		return s
	}
	if row.StartedAt == nil {
		return stepBegin
	}
	return stepUnknown
}

// advance records the answer of a call made at step at: M47 counts one step for an answer that
// differs from the step before it, and one usage step for each call made at usage.
func (d *Deleter) advance(id, at, next string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.at == nil {
		d.at = map[string]string{}
	}
	switch next {
	case answerDone:
		delete(d.at, id)
	case answerScheduled:
		d.at[id] = stepBegin
	case answerSettling:
		d.at[id] = stepComplete
	default:
		d.at[id] = next
	}
	if at == stepUnknown || next == answerScheduled || next == answerSettling {
		return
	}
	if next != at || at == stepUsage {
		d.addLocked(at, "ok")
	}
}

func (d *Deleter) count(step, result string) {
	if step == stepUnknown {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.addLocked(step, result)
}

func (d *Deleter) addLocked(step, result string) {
	if d.steps == nil {
		d.steps = map[[2]string]int64{}
	}
	d.steps[[2]string{step, result}]++
}

// fail counts a failure and writes the line, at most once a minute and never with a tenant.
func (d *Deleter) fail(step string, err error) {
	d.mu.Lock()
	d.failures++
	n := d.failures
	now := d.now()
	due := now.Sub(d.lastWarned) >= warnEvery
	if due {
		d.lastWarned = now
	}
	d.mu.Unlock()
	if due {
		d.logger().Warn("tenant deletion failing", "step", step, "error_class", errorClass(err), "sqlstate", store.SQLState(err), "failures", n)
	}
}

// recovered says, once, that the passes work again after failures.
func (d *Deleter) recovered() {
	d.mu.Lock()
	n := d.failures
	d.failures = 0
	d.mu.Unlock()
	if n > 0 {
		d.logger().Info("tenant deletion recovered", "failures", n)
	}
}

// errorClass names a failure without its text: the text could hold data.
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

// Stats returns the counters.
func (d *Deleter) Stats() DeleterStats {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := DeleterStats{Steps: map[[2]string]int64{}, Open: map[string]int64{}}
	for k, v := range d.steps {
		out.Steps[k] = v
	}
	for k, v := range d.open {
		out.Open[k] = v
	}
	return out
}
