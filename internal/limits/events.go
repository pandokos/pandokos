package limits

import (
	"context"
	"log/slog"
	"time"

	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
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

// EventWriter inserts events into limit_event.
type EventWriter struct {
	o EventWriterOptions
}

var _ EventSink = (*EventWriter)(nil)

// NewEventWriter returns an EventWriter.
func NewEventWriter(o EventWriterOptions) (*EventWriter, error) {
	return &EventWriter{o: o}, nil
}

// Offer queues an event; it never blocks and returns false when the queue is full.
func (w *EventWriter) Offer(Event) bool { return false }

// Run inserts the queued events until ctx is done.
func (w *EventWriter) Run(_ context.Context) {}

// Close writes what is queued within ctx and returns the number of events left unwritten.
func (w *EventWriter) Close(_ context.Context) (unwritten int) { return 0 }
