package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/usage"
)

// The waits of the loop after the database could not be reached: the first, doubling up to the
// second. Variables so that tests can shorten them; a Writer reads them once, in newWriter.
var (
	backoffInitial = 100 * time.Millisecond
	backoffMax     = 5 * time.Second
)

const (
	writeTimeout    = 5 * time.Second // one insert
	failingLogEvery = time.Minute     // how often `ledger write failing` repeats while the state lasts
)

// Options configure a Writer.
type Options struct {
	DB            *store.DB     // the gateway role; required
	Logger        *slog.Logger  // required
	BufferSize    int           // records admitted and not yet written; > 0
	BatchSize     int           // records per flush; > 0
	FlushInterval time.Duration // how long a record waits for company; > 0
}

// Stats is a snapshot of the counters of a Writer.
type Stats struct {
	InUse     int   // slots taken
	Queued    int   // records waiting to be written
	Written   int64 // rows committed
	Rejected  int64 // records the database refused
	Unwritten int64 // records declared lost at shutdown
}

// sink is the store seam: it inserts the records of one tenant in one transaction of that tenant.
type sink interface {
	insert(ctx context.Context, t tenant.ID, records []usage.Record) error
}

// dbSink is the sink of the database.
type dbSink struct{ db *store.DB }

func newDBSink(db *store.DB) sink { return dbSink{db: db} }

func (s dbSink) insert(ctx context.Context, t tenant.ID, records []usage.Record) error {
	rows := make([]row, len(records))
	for i := range records {
		rows[i] = rowOf(&records[i])
	}
	payload, err := json.Marshal(rows)
	if err != nil {
		return fmt.Errorf("ledger: encode records: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return s.db.WithTenant(ctx, t, func(ctx context.Context, tx store.Tx) error {
		_, err := queries.New(tx).InsertUsageRecords(ctx, string(payload))
		return err
	})
}

// Writer is the usage ledger writer; it implements gateway.Recorder.
type Writer struct {
	sink                       sink
	log                        *slog.Logger
	bufferSize, batchSize      int
	flushInterval              time.Duration
	backoffInitial, backoffMax time.Duration

	wake      chan struct{} // capacity 1: a record arrived or Close began
	closeCh   chan struct{} // closed when Close begins
	drained   chan struct{} // closed when closing and no slot is in use
	stop      chan struct{} // closed when Close is done: Run returns
	closeDone chan struct{} // closed when the first Close has returned
	runDone   chan struct{} // closed when Run has returned

	mu         sync.Mutex
	inUse      int
	queue      []usage.Record
	saturated  bool
	closing    bool
	closed     bool // Close has taken the queue: a later record is written off at once
	running    bool
	cancelRun  context.CancelFunc
	warnedFree bool // `usage record without admission` was logged
	written    int64
	rejected   int64
	unwritten  int64
	closeCount int
}

// New validates the options and returns a Writer.
func New(o Options) (*Writer, error) {
	if o.DB == nil {
		return nil, errors.New("ledger: no database")
	}
	return newWriter(o, newDBSink(o.DB))
}

func newWriter(o Options, s sink) (*Writer, error) {
	switch {
	case o.Logger == nil:
		return nil, errors.New("ledger: no logger")
	case o.BufferSize <= 0:
		return nil, errors.New("ledger: buffer size must be positive")
	case o.BatchSize <= 0:
		return nil, errors.New("ledger: batch size must be positive")
	case o.FlushInterval <= 0:
		return nil, errors.New("ledger: flush interval must be positive")
	case s == nil:
		return nil, errors.New("ledger: no sink")
	}
	return &Writer{
		sink: s, log: o.Logger,
		bufferSize: o.BufferSize, batchSize: o.BatchSize, flushInterval: o.FlushInterval,
		backoffInitial: backoffInitial, backoffMax: backoffMax,
		wake: make(chan struct{}, 1), closeCh: make(chan struct{}), drained: make(chan struct{}),
		stop: make(chan struct{}), closeDone: make(chan struct{}), runDone: make(chan struct{}),
	}, nil
}

func (w *Writer) poke() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Admit takes a slot for one request or reports false when none is free.
func (w *Writer) Admit() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closing {
		return false
	}
	if w.inUse >= w.bufferSize {
		w.saturated = true
		return false
	}
	w.inUse++
	return true
}

// Record takes the record of an admitted request. It does not block.
func (w *Writer) Record(r usage.Record) {
	err := r.Validate()
	w.mu.Lock()
	if w.closed {
		// Close has finished: nobody reads the queue any more.
		w.unwritten++
		w.mu.Unlock()
		w.logNotWritten(&r)
		return
	}
	covered := w.inUse > len(w.queue) // a slot is held for a request that has not recorded yet
	if err != nil {
		w.rejected++
		if covered {
			w.inUse--
			w.afterFree()
		}
		warn := !covered && !w.warnedFree
		if warn {
			w.warnedFree = true
		}
		w.mu.Unlock()
		// What is logged: the fields that passed their checks, never the one that failed.
		w.log.Error("usage record rejected", "record_id", r.ID, "tenant_id", r.TenantID.String(), "error", err.Error(),
			"row", rowJSON(&r, r.Failed()...))
		if warn {
			w.log.Error("usage record without admission")
		}
		return
	}
	warn := false
	if !covered {
		w.inUse++
		warn = !w.warnedFree
		w.warnedFree = true
	}
	w.queue = append(w.queue, r)
	w.mu.Unlock()
	if warn {
		w.log.Error("usage record without admission")
	}
	w.poke()
}

// afterFree runs under mu after slots were freed.
func (w *Writer) afterFree() {
	if w.saturated && w.inUse <= w.bufferSize/2 {
		w.saturated = false
	}
	if w.closing && w.inUse == 0 {
		select {
		case <-w.drained:
		default:
			close(w.drained)
		}
	}
}

// identity is the attributes that name a record without metadata values.
func (w *Writer) identity(r *usage.Record, extra ...any) []any {
	return append([]any{
		"record_id", r.ID, "request_id", r.RequestID, "tenant_id", r.TenantID.String(), "outcome", string(r.Outcome),
	}, extra...)
}

// Ready fails with "ledger: buffer full" from the moment an Admit is refused until the slots
// in use fall to half of BufferSize; it is a health.Check.
func (w *Writer) Ready(ctx context.Context) error {
	_ = ctx
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.saturated {
		return fmt.Errorf("ledger: buffer full (%d of %d slots in use)", w.inUse, w.bufferSize)
	}
	return nil
}

// Stats returns the counters.
func (w *Writer) Stats() Stats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return Stats{InUse: w.inUse, Queued: len(w.queue), Written: w.written, Rejected: w.rejected, Unwritten: w.unwritten}
}

// Run is the writing loop; it returns after Close, or when ctx ends.
func (w *Writer) Run(ctx context.Context) {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return
	}
	select {
	case <-w.stop:
		w.mu.Unlock()
		return
	default:
	}
	w.running = true
	ctx, cancel := context.WithCancel(ctx)
	w.cancelRun = cancel
	w.mu.Unlock()
	defer close(w.runDone)
	defer cancel()

	l := loop{w: w, wait: w.backoffInitial}
	for {
		if !l.waitForBatch(ctx) {
			return
		}
		for l.flush(ctx) {
			if !l.backoff(ctx) {
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// loop is the state of the writing goroutine.
type loop struct {
	w        *Writer
	wait     time.Duration // the next wait after a failure
	failing  bool
	failures int
	lastWarn time.Time
	fast     bool // Close began: retry quickly
}

// waitForBatch blocks until a batch is due: BatchSize records are waiting, or FlushInterval has
// passed since a record was first seen waiting, or Close began and a record is waiting. It
// returns false when the loop must end.
func (l *loop) waitForBatch(ctx context.Context) bool {
	w := l.w
	var timer *time.Timer
	var fire <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		w.mu.Lock()
		n, closing := len(w.queue), w.closing
		w.mu.Unlock()
		select {
		case <-w.stop:
			return false
		case <-ctx.Done():
			return false
		default:
		}
		if n >= w.batchSize || n > 0 && closing {
			return true
		}
		if n > 0 && timer == nil {
			timer = time.NewTimer(w.flushInterval)
			fire = timer.C
		}
		select {
		case <-w.wake:
		case <-fire:
			return true
		case <-w.stop:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

// backoff waits before a retry; it returns false when the loop must end.
func (l *loop) backoff(ctx context.Context) bool {
	w := l.w
	d := l.wait
	l.wait = min(l.wait*2, w.backoffMax)
	timer := time.NewTimer(d)
	defer timer.Stop()
	var closing <-chan struct{}
	if !l.fast {
		closing = w.closeCh
	}
	select {
	case <-timer.C:
	case <-closing: // Close began: do not sit out a long wait; the waits that follow stay short
		l.fast = true
		l.wait = min(l.wait, w.backoffInitial)
	case <-w.stop:
		return false
	case <-ctx.Done():
		return false
	}
	return ctx.Err() == nil
}

type group struct {
	tenant tenant.ID
	idx    []int // positions in the batch, in arrival order
}

// flush writes one batch. It returns true when the database could not be reached and the loop
// must wait and try again.
func (l *loop) flush(ctx context.Context) (retry bool) {
	w := l.w
	w.mu.Lock()
	n := min(len(w.queue), w.batchSize)
	batch := append([]usage.Record(nil), w.queue[:n]...)
	w.mu.Unlock()
	if n == 0 {
		return false
	}

	var groups []*group
	byTenant := map[tenant.ID]*group{}
	for i := range batch {
		g := byTenant[batch[i].TenantID]
		if g == nil {
			g = &group{tenant: batch[i].TenantID}
			byTenant[g.tenant] = g
			groups = append(groups, g)
		}
		g.idx = append(g.idx, i)
	}

	settled := make([]bool, n)
	var written, rejected int
	var unavailable error
	pick := func(idx []int) []usage.Record {
		recs := make([]usage.Record, len(idx))
		for i, j := range idx {
			recs[i] = batch[j]
		}
		return recs
	}
groups:
	for _, g := range groups {
		err := w.sink.insert(ctx, g.tenant, pick(g.idx))
		if err == nil {
			for _, j := range g.idx {
				settled[j] = true
			}
			written += len(g.idx)
			l.succeeded()
			continue
		}
		if retryable(err) {
			unavailable = err
			break
		}
		if len(g.idx) == 1 {
			w.reject(&batch[g.idx[0]], err)
			settled[g.idx[0]] = true
			rejected++
			continue
		}
		for _, j := range g.idx { // the group alone failed: find the record that the database refuses
			err := w.sink.insert(ctx, g.tenant, batch[j:j+1])
			switch {
			case err == nil:
				settled[j] = true
				written++
				l.succeeded()
			case retryable(err):
				unavailable = err
				break groups
			default:
				w.reject(&batch[j], err)
				settled[j] = true
				rejected++
			}
		}
	}

	w.mu.Lock()
	if written+rejected > 0 {
		rest := make([]usage.Record, 0, len(w.queue)-written-rejected)
		for i := range batch {
			if !settled[i] {
				rest = append(rest, w.queue[i])
			}
		}
		rest = append(rest, w.queue[n:]...)
		w.queue = rest
		w.inUse -= written + rejected
		w.written += int64(written)
		w.rejected += int64(rejected)
		w.afterFree()
	}
	queued, inUse := len(w.queue), w.inUse
	w.mu.Unlock()

	if unavailable == nil {
		return false
	}
	if ctx.Err() != nil { // Close gave up or the loop's context ended: not a state of the database
		return false
	}
	l.failures++
	if !l.failing || time.Since(l.lastWarn) >= failingLogEvery {
		l.failing = true
		l.lastWarn = time.Now()
		w.log.Warn("ledger write failing", "error", unavailable.Error(), "queued", queued, "in_use", inUse, "failures", l.failures)
	}
	return true
}

// succeeded notes that the database answered.
func (l *loop) succeeded() {
	l.wait = l.w.backoffInitial
	l.fast = false
	if l.failing {
		l.failing = false
		l.w.log.Info("ledger write recovered", "failures", l.failures)
	}
	l.failures = 0
}

// reject logs a record the database refuses; its slot is freed by the caller. The line carries
// the SQLSTATE, the constraint and the row, without the values of the metadata.
func (w *Writer) reject(r *usage.Record, err error) {
	attrs := w.identity(r, "sqlstate", store.SQLState(err))
	_, constraint, _ := store.ConstraintViolation(err)
	attrs = append(attrs, "constraint", constraint, "metadata_count", len(r.Metadata), "row", rowJSON(r))
	w.log.Error("usage record rejected", attrs...)
}

// logNotWritten logs a record that will not reach the table: the identity fields and the whole
// row as JSON, so that it can be inserted by hand; the values of the metadata are left out.
func (w *Writer) logNotWritten(r *usage.Record) {
	w.log.Error("usage record not written", w.identity(r, "metadata_count", len(r.Metadata), "row", rowJSON(r))...)
}

// rowJSON is the record as the insert statement reads it, with the metadata values removed and
// the columns named in omit left out.
func rowJSON(r *usage.Record, omit ...string) string {
	rw := rowOf(r)
	rw.Metadata = map[string]string{}
	b, err := json.Marshal(rw)
	if err != nil || len(omit) == 0 {
		return string(b)
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	for _, k := range omit {
		delete(m, k)
	}
	b, _ = json.Marshal(m)
	return string(b)
}

// retryable says whether a refusal is about the state of the database and not about the record:
// the database is out of reach, or it is busy or failing over (serialization failure, deadlock,
// lock not available, read-only transaction). The records stay queued and the loop backs off.
func retryable(err error) bool {
	if store.IsUnavailable(err) {
		return true
	}
	switch store.SQLState(err) {
	case "40001", "40P01", "55P03", "25006":
		return true
	}
	return false
}

// Close flushes what it can until ctx ends and returns the number of records not written. It
// refuses further admissions; it returns when no slot is in use (every admitted request has
// recorded and every record is written) or when ctx ends, and then writes off what remains.
// Run returns before Close does. Close is idempotent.
func (w *Writer) Close(ctx context.Context) (unwritten int) {
	w.mu.Lock()
	if w.closing {
		w.mu.Unlock()
		select {
		case <-w.closeDone:
		case <-ctx.Done():
			return 0
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.closeCount
	}
	w.closing = true
	close(w.closeCh)
	w.afterFree()
	w.mu.Unlock()
	w.poke()

	select {
	case <-w.drained:
	case <-ctx.Done():
	}

	w.mu.Lock()
	running, cancel := w.running, w.cancelRun
	w.mu.Unlock()
	if ctx.Err() != nil && cancel != nil {
		cancel() // an insert in flight must not outlive the deadline
	}
	close(w.stop)
	if running {
		<-w.runDone
	}

	w.mu.Lock()
	remaining := w.queue
	w.queue = nil
	missing := w.inUse - len(remaining)
	w.inUse = 0
	w.unwritten += int64(len(remaining))
	w.closeCount = len(remaining)
	w.closed = true
	w.mu.Unlock()

	for i := range remaining {
		w.logNotWritten(&remaining[i])
	}
	if len(remaining) > 0 || missing > 0 {
		w.log.Error("ledger closed with records lost", "unwritten", len(remaining), "missing", missing)
	} else {
		w.log.Info("ledger closed", "unwritten", 0, "missing", 0)
	}
	close(w.closeDone)
	return len(remaining)
}
