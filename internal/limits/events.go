package limits

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// Event kinds.
const (
	EventThresholdReached = "threshold_reached"
	EventExhausted        = "exhausted"
)

// Event is a threshold that was reached or a limit that refused a request for the first time in
// a period. It names no request, caller or content.
type Event struct {
	Tenant          tenant.ID
	Kind            string
	RuleID, Level   string
	Subject         SubjectKind
	SubjectID       string
	Provider, Model string
	Metric          Metric
	Currency        string
	Period          Period
	Percent         int             // 0 for exhausted
	Value           int64           // limit_value of the rule, in the unit of the metric
	Observed        pricing.Decimal // what was used when the event was recorded
}

// EventSink takes events without blocking; it returns false when it did not take the event.
type EventSink interface {
	Offer(Event) bool
}

// EventWriterOptions configure an EventWriter.
type EventWriterOptions struct {
	DB     *store.DB // the gateway pool
	Logger *slog.Logger
	Now    func() time.Time
}

const (
	eventQueue      = 1024
	eventAttempt    = 5 * time.Second
	eventBackoffMin = 100 * time.Millisecond
	eventBackoffMax = 5 * time.Second
)

// EventWriter inserts events into limit_event, off the request path.
type EventWriter struct {
	o EventWriterOptions
	q chan Event

	recorded, dropped, failed atomic.Int64

	mu          sync.Mutex
	interrupted *Event // the event whose insert Run abandoned when its context ended; Close writes it first
}

var _ EventSink = (*EventWriter)(nil)

// NewEventWriter returns an EventWriter.
func NewEventWriter(o EventWriterOptions) (*EventWriter, error) {
	if o.DB == nil {
		return nil, errors.New("limits: the event writer needs a pool")
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &EventWriter{o: o, q: make(chan Event, eventQueue)}, nil
}

// Offer queues an event; it never blocks and returns false when the queue is full.
func (w *EventWriter) Offer(e Event) bool {
	select {
	case w.q <- e:
		return true
	default:
		w.dropped.Add(1)
		return false
	}
}

// EventStats is a snapshot of the counters of an EventWriter.
type EventStats struct {
	Recorded int64 // events inserted
	Dropped  int64 // events refused by a full queue or left unwritten by Close
	Failed   int64 // events the database refused
}

// Stats returns the counters.
func (w *EventWriter) Stats() EventStats {
	return EventStats{Recorded: w.recorded.Load(), Dropped: w.dropped.Load(), Failed: w.failed.Load()}
}

// Run inserts the queued events until ctx is done. An event whose insert was interrupted is kept
// for Close, which writes it before the queue (the queue may be full by then).
func (w *EventWriter) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-w.q:
			if !w.write(ctx, e) {
				w.mu.Lock()
				w.interrupted = &e
				w.mu.Unlock()
				return
			}
		}
	}
}

// Close writes what is queued within ctx and returns the number of events left unwritten. Run
// must have returned.
func (w *EventWriter) Close(ctx context.Context) (unwritten int) {
	unwritten = w.close(ctx)
	w.dropped.Add(int64(unwritten))
	return unwritten
}

func (w *EventWriter) close(ctx context.Context) (unwritten int) {
	w.mu.Lock()
	first := w.interrupted
	w.interrupted = nil
	w.mu.Unlock()
	if first != nil && !w.write(ctx, *first) {
		w.mu.Lock()
		w.interrupted = first
		w.mu.Unlock()
		return len(w.q) + 1
	}
	for {
		select {
		case e := <-w.q:
			if !w.write(ctx, e) {
				w.mu.Lock()
				w.interrupted = &e
				w.mu.Unlock()
				return len(w.q) + 1
			}
		default:
			return 0
		}
	}
}

// write inserts one event in its own transaction. It retries with a backoff while the database is
// unavailable and returns false only when ctx ended first; any other error drops the event.
func (w *EventWriter) write(ctx context.Context, e Event) bool {
	backoff := eventBackoffMin
	for {
		err := w.insert(ctx, e)
		if err == nil {
			w.recorded.Add(1)
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		if !store.IsUnavailable(err) {
			w.failed.Add(1)
			// The event is the tenant's: the line names its kind and the error, nothing of the tenant (ADR 0055).
			w.o.Logger.Error("limit event not recorded", slog.String("kind", e.Kind), slog.String("sqlstate", store.SQLState(err)), slog.String("error_class", errorClass(err)))
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, eventBackoffMax)
	}
}

func (w *EventWriter) insert(ctx context.Context, e Event) error {
	ctx, cancel := context.WithTimeout(ctx, eventAttempt)
	defer cancel()
	pct := int16(min(max(e.Percent, 0), 100)) //nolint:gosec // clamped to 0..100
	err := w.o.DB.WithTenant(ctx, e.Tenant, func(ctx context.Context, tx store.Tx) (err error) {
		_, err = queries.New(tx).InsertLimitEvent(ctx, queries.InsertLimitEventParams{
			Kind: e.Kind, RuleID: e.RuleID, RuleLevel: e.Level, SubjectKind: string(e.Subject), SubjectID: e.SubjectID,
			Provider: e.Provider, Model: e.Model, Metric: string(e.Metric), Currency: e.Currency,
			PeriodStart: e.Period.Start, PeriodEnd: e.Period.End, Percent: pct, LimitValue: e.Value,
			Observed: e.Observed.String()})
		return err
	})
	if err != nil {
		return err
	}
	return nil
}
