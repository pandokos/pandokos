package ledger

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/usage"
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

// Writer is the usage ledger writer; it implements gateway.Recorder.
type Writer struct{}

// New validates the options and returns a Writer.
func New(o Options) (*Writer, error) {
	_ = o
	return nil, errors.New("ledger: not implemented")
}

// Admit takes a slot for one request or reports false when none is free.
func (w *Writer) Admit() bool { return true }

// Record takes the record of an admitted request. It does not block.
func (w *Writer) Record(r usage.Record) { _ = r }

// Run is the writing loop; it returns after Close, or when ctx ends.
func (w *Writer) Run(ctx context.Context) { _ = ctx }

// Close flushes what it can until ctx ends and returns the number of records not written.
func (w *Writer) Close(ctx context.Context) (unwritten int) {
	_ = ctx
	return 0
}

// Ready fails with "ledger: buffer full" from the moment an Admit is refused until the slots
// in use fall to half of BufferSize; it is a health.Check.
func (w *Writer) Ready(ctx context.Context) error {
	_ = ctx
	return nil
}

// Stats returns the counters.
func (w *Writer) Stats() Stats { return Stats{} }
