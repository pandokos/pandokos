package testutil

import (
	"context"
	"errors"
	"sync"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/gateway"
)

// LimitCall is one call of a StaticLimits or of one of its reservations.
type LimitCall struct {
	// Kind is "reserve", "settle" or "release".
	Kind string
	// N numbers the reservation: the n-th admitted call of Reserve (1-based); 0 for a refusal or
	// an error.
	N int
	// Principal and Estimate are those of a reserve.
	Principal authz.Principal
	Estimate  gateway.LimitEstimate
	// Usage is the usage of a settle.
	Usage gateway.LimitUsage
	// Admitted tells, for a reserve, what the answer was: "admit", "refuse" or "error".
	Answer string
}

type limitAnswer struct {
	refusal *gateway.LimitRefusal
	err     error
}

// StaticLimits is a gateway.Limits with a queue of answers and a log of calls, safe for
// concurrent use. When the queue is empty every call is admitted.
type StaticLimits struct {
	// OnReserve, when set before use, is called at the start of every Reserve.
	OnReserve func(p authz.Principal, e gateway.LimitEstimate)
	// PanicOnSettle and PanicOnRelease make the settle or the release of every reservation panic
	// (after the call is logged).
	PanicOnSettle, PanicOnRelease bool
	// State is the limit state Reserve returns with an admission or a refusal (sprint 5.3); empty
	// by default.
	State gateway.LimitState

	mu       sync.Mutex
	queue    []limitAnswer
	calls    []LimitCall
	admitted int
}

// NewStaticLimits returns a StaticLimits that admits everything.
func NewStaticLimits() *StaticLimits { return &StaticLimits{} }

// Refuse queues a refusal as the answer of the next Reserve without an answer queued before it.
func (s *StaticLimits) Refuse(r gateway.LimitRefusal) *StaticLimits {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, limitAnswer{refusal: &r})
	return s
}

// Fail queues an error as the answer of the next such Reserve.
func (s *StaticLimits) Fail(err error) *StaticLimits {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, limitAnswer{err: err})
	return s
}

// Admit queues an explicit admission.
func (s *StaticLimits) Admit() *StaticLimits {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, limitAnswer{})
	return s
}

// Reserve implements gateway.Limits.
func (s *StaticLimits) Reserve(_ context.Context, p authz.Principal, e gateway.LimitEstimate) (gateway.Reservation, *gateway.LimitRefusal, gateway.LimitState, error) {
	if s.OnReserve != nil {
		s.OnReserve(p, e)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var a limitAnswer
	if len(s.queue) > 0 {
		a, s.queue = s.queue[0], s.queue[1:]
	}
	call := LimitCall{Kind: "reserve", Principal: p, Estimate: e}
	switch {
	case a.err != nil:
		call.Answer = "error"
		s.calls = append(s.calls, call)
		return nil, nil, "", a.err
	case a.refusal != nil:
		call.Answer = "refuse"
		s.calls = append(s.calls, call)
		return nil, a.refusal, s.State, nil
	}
	s.admitted++
	call.N, call.Answer = s.admitted, "admit"
	s.calls = append(s.calls, call)
	return &staticReservation{s: s, n: s.admitted}, nil, s.State, nil
}

type staticReservation struct {
	s *StaticLimits
	n int
}

func (r *staticReservation) Settle(u gateway.LimitUsage) {
	r.s.mu.Lock()
	r.s.calls = append(r.s.calls, LimitCall{Kind: "settle", N: r.n, Usage: u})
	r.s.mu.Unlock()
	if r.s.PanicOnSettle {
		panic("synthetic panic in Settle")
	}
}

func (r *staticReservation) Release() {
	r.s.mu.Lock()
	r.s.calls = append(r.s.calls, LimitCall{Kind: "release", N: r.n})
	r.s.mu.Unlock()
	if r.s.PanicOnRelease {
		panic("synthetic panic in Release")
	}
}

// Calls returns every call, in order.
func (s *StaticLimits) Calls() []LimitCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]LimitCall(nil), s.calls...)
}

// Of returns the calls of one kind ("reserve", "settle" or "release"), in order.
func (s *StaticLimits) Of(kind string) []LimitCall {
	var out []LimitCall
	for _, c := range s.Calls() {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

// Ended returns, for each admitted reservation, how many times it was settled and released.
func (s *StaticLimits) Ended() (settled, released map[int]int) {
	settled, released = map[int]int{}, map[int]int{}
	for _, c := range s.Calls() {
		switch c.Kind {
		case "settle":
			settled[c.N]++
		case "release":
			released[c.N]++
		}
	}
	return settled, released
}

// Admitted returns the number of reservations the limits admitted.
func (s *StaticLimits) Admitted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.admitted
}

// Open returns the number of admitted reservations that were neither settled nor released.
func (s *StaticLimits) Open() int {
	settled, released := s.Ended()
	n := 0
	for i := 1; i <= s.Admitted(); i++ {
		if settled[i]+released[i] == 0 {
			n++
		}
	}
	return n
}

var _ gateway.Limits = (*StaticLimits)(nil)

// ErrSyntheticLimits is the error of a StaticLimits.Fail in tests that need one.
var ErrSyntheticLimits = errors.New("synthetic limits failure")
