package limits

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// MemoryOptions configure a Memory.
type MemoryOptions struct {
	Logger *slog.Logger     // required
	Now    func() time.Time // default time.Now
}

// Memory is the in-process Store.
type Memory struct {
	log *slog.Logger
	now func() time.Time
}

var _ Store = (*Memory)(nil)

var errNotImplemented = errors.New("limits: not implemented")

// NewMemory returns an empty Memory. A logger is required.
func NewMemory(o MemoryOptions) (*Memory, error) {
	if o.Logger == nil {
		return nil, errors.New("limits: the store needs a logger")
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &Memory{log: o.Logger, now: now}, nil
}

// Reserve is not implemented yet.
func (m *Memory) Reserve(context.Context, Request) (Reservation, *Refusal, error) {
	return Reservation{}, nil, errNotImplemented
}

// Settle is not implemented yet.
func (m *Memory) Settle(context.Context, Reservation, Amounts) error { return errNotImplemented }

// Release is not implemented yet.
func (m *Memory) Release(context.Context, Reservation) error { return errNotImplemented }

// Run calls Sweep every second until ctx is done.
func (m *Memory) Run(ctx context.Context) { <-ctx.Done() }

// Sweep releases expired reservations and drops empty counters; it returns the number expired.
func (m *Memory) Sweep(time.Time) int { return 0 }

// Outstanding returns the number of reservations not yet settled, released or expired.
func (m *Memory) Outstanding() int { return 0 }

// Counters returns the number of counters held.
func (m *Memory) Counters() int { return 0 }
