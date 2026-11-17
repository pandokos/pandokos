package limits

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The property tests are written against Store and the three accessors of Memory (Outstanding,
// Counters and, through headroom, the counters themselves), so that the same schedules can run
// on another store (epic 8).

type evState int

const (
	evOpen evState = iota
	evSettled
	evReleased
)

// pevent is one admitted request of the reference model.
type pevent struct {
	at        time.Time
	admitSeq  int
	closeSeq  int
	reserved  int64
	actual    int64 // when settled
	state     evState
	res       Reservation
	expiresAt time.Time
}

// schedCfg configures a schedule.
type schedCfg struct {
	ttl        time.Duration
	allowAbove bool // settle with more than reserved
	model      bool // check every decision against the reference (needs a TTL longer than the schedule)
	ops        int
}

// sched is one random schedule run against a Memory and, when cfg.model is set, the reference.
type sched struct {
	t     *testing.T
	rng   *rand.Rand
	clk   *clock
	m     *Memory
	cfg   schedCfg
	limit [3]Limit // concurrency, requests, tokens of the tenant
	evs   []*pevent
	seq   int
	name  string
	trace []string
}

func newSched(t *testing.T, seed uint64, n int, cfg schedCfg) *sched {
	rng := rngOf(seed, n)
	clk := newClock()
	m, _ := newMemory(t, clk)
	a := tid(1)
	s := &sched{t: t, rng: rng, clk: clk, m: m, cfg: cfg, name: fmt.Sprintf("seed %d schedule %d", seed, n)}
	s.limit = [3]Limit{
		lim(a, SubjectTenant, "", ConcurrentRequests, int64(1+rng.IntN(6))),
		lim(a, SubjectTenant, "", RequestsPerMinute, int64(1+rng.IntN(30))),
		lim(a, SubjectTenant, "", TokensPerMinute, int64(50+rng.IntN(2000))),
	}
	return s
}

func (s *sched) fail(format string, args ...any) {
	s.t.Helper()
	// The failing prefix: every step up to the failure, so that the run can be replayed by hand.
	s.t.Fatalf("%s\n  failure: %s\n  schedule up to the failing step (%d steps):\n    %s",
		s.name, fmt.Sprintf(format, args...), len(s.trace), strings.Join(s.trace, "\n    "))
}

func (s *sched) open() []*pevent {
	var out []*pevent
	for _, e := range s.evs {
		if e.state == evOpen {
			out = append(out, e)
		}
	}
	return out
}

// counted returns the requests and tokens the events admitted at most maxAge ago hold (the
// comparison of the age is strict when strict is set).
func (s *sched) counted(now time.Time, maxAge time.Duration, strict bool) (reqs, tokens int64) {
	for _, e := range s.evs {
		if e.state == evReleased {
			continue
		}
		age := now.Sub(e.at)
		if age < maxAge || (!strict && age == maxAge) {
			reqs++
			if e.state == evOpen {
				tokens += e.reserved
			} else {
				tokens += e.actual
			}
		}
	}
	return
}

func (s *sched) gauge() int64 { return int64(len(s.open())) }

func (s *sched) stepReserve() {
	est := int64(s.rng.IntN(400))
	if s.rng.IntN(20) == 0 {
		est = s.limit[2].Max + int64(s.rng.IntN(50))
	}
	now := s.clk.now()
	s.trace = append(s.trace, fmt.Sprintf("reserve(%d)@%s", est, now.Format("15:04:05.000")))
	res, ref, err := s.m.Reserve(context.Background(), request(fmt.Sprintf("r%d", s.seq), s.cfg.ttl, est, s.limit[:]...))
	if err != nil {
		s.fail("reserve: %v", err)
	}
	if s.cfg.model {
		lowReq, lowTok := s.counted(now, 60*time.Second, false)  // certainly counted by the store
		highReq, highTok := s.counted(now, 61*time.Second, true) // the most the store can count
		g := s.gauge()
		mustRefuse := g >= s.limit[0].Max || lowReq+1 > s.limit[1].Max || lowTok+est > s.limit[2].Max
		mustAdmit := g < s.limit[0].Max && highReq+1 <= s.limit[1].Max && highTok+est <= s.limit[2].Max
		if ref == nil && mustRefuse {
			s.fail("admitted what the exact 60 s reference refuses (gauge %d, requests %d, tokens %d, estimate %d)", g, lowReq, lowTok, est)
		}
		if ref != nil && mustAdmit {
			s.fail("refused what the exact 61 s reference admits (gauge %d, requests %d, tokens %d, estimate %d): %+v", g, highReq, highTok, est, *ref)
		}
	}
	if ref != nil {
		if ref.RetryAfter < time.Second || ref.RetryAfter > 61*time.Second || ref.Remaining < 0 {
			s.fail("refusal out of its bounds: %+v", *ref)
		}
		return
	}
	s.seq++
	s.evs = append(s.evs, &pevent{at: now, admitSeq: s.seq, reserved: est, res: res, expiresAt: res.Expires})
}

func (s *sched) actualFor(e *pevent) int64 {
	switch k := s.rng.IntN(3); {
	case k == 0:
		return int64(s.rng.IntN(int(e.reserved) + 1))
	case k == 1 || !s.cfg.allowAbove:
		return e.reserved
	default:
		return e.reserved + 1 + int64(s.rng.IntN(300))
	}
}

func (s *sched) close(e *pevent, release bool) {
	now := s.clk.now()
	var err error
	if release {
		s.trace = append(s.trace, fmt.Sprintf("release(%d)", e.admitSeq))
		err = s.m.Release(context.Background(), e.res)
	} else {
		a := s.actualFor(e)
		s.trace = append(s.trace, fmt.Sprintf("settle(%d, %d of %d)", e.admitSeq, a, e.reserved))
		e.actual = a
		err = s.m.Settle(context.Background(), e.res, Amounts{Tokens: a})
	}
	s.seq++
	e.closeSeq = s.seq
	switch {
	case err == nil:
		if release {
			e.state = evReleased
		} else {
			e.state = evSettled
		}
	case errors.Is(err, ErrNotOutstanding) && !now.Before(e.expiresAt):
		e.state = evReleased // expired: it holds nothing
	default:
		s.fail("close of %d: %v", e.admitSeq, err)
	}
}

func (s *sched) step() {
	switch k := s.rng.IntN(100); {
	case k < 40:
		s.stepReserve()
	case k < 60:
		if o := s.open(); len(o) > 0 {
			s.close(o[s.rng.IntN(len(o))], false)
		}
	case k < 70:
		if o := s.open(); len(o) > 0 {
			s.close(o[s.rng.IntN(len(o))], true)
		}
	case k < 95:
		var d time.Duration
		switch s.rng.IntN(10) {
		case 0:
		case 1, 2, 3, 4:
			d = time.Duration(1+s.rng.IntN(999)) * time.Millisecond
		default:
			d = time.Duration(1+s.rng.IntN(90)) * time.Second
		}
		s.trace = append(s.trace, fmt.Sprintf("advance(%s)", d))
		s.clk.advance(d)
	default:
		s.trace = append(s.trace, "sweep")
		s.m.Sweep(s.clk.now())
	}
}

func (s *sched) run() {
	for i := 0; i < s.cfg.ops; i++ {
		s.step()
	}
}

// drain settles everything still open.
func (s *sched) drain() {
	for _, e := range s.open() {
		s.close(e, false)
	}
}

func (s *sched) live() []*pevent {
	var out []*pevent
	for _, e := range s.evs {
		if e.state != evReleased {
			out = append(out, e)
		}
	}
	return out
}

// TestPropertyRequests is T-616.
func TestPropertyRequests(t *testing.T) {
	seed := seedOf(t)
	for n := 0; n < schedules(); n++ {
		s := newSched(t, seed, n, schedCfg{ttl: 24 * time.Hour, allowAbove: true, model: true, ops: 200})
		s.run()
		if g := s.gauge(); g > s.limit[0].Max {
			s.fail("%d outstanding, concurrency limit %d", g, s.limit[0].Max)
		}
		s.drain()
		// In every interval of 60 s the requests admitted and not released are at most L.
		live := s.live()
		for _, e := range live {
			var c int64
			for _, f := range live {
				if f.at.After(e.at.Add(-60*time.Second)) && !f.at.After(e.at) {
					c++
				}
			}
			if c > s.limit[1].Max {
				s.fail("%d requests admitted in the 60 s before %s, limit %d", c, e.at.Format("15:04:05.000"), s.limit[1].Max)
			}
		}
	}
}

// TestPropertyTokens is T-617.
func TestPropertyTokens(t *testing.T) {
	seed := seedOf(t)
	for _, strict := range []bool{false, true} {
		for n := 0; n < schedules(); n++ {
			s := newSched(t, seed, n, schedCfg{ttl: 24 * time.Hour, allowAbove: !strict, model: true, ops: 200})
			s.run()
			s.drain()
			live := s.live()
			for _, first := range live {
				// I = [first.at, first.at + 60 s); s = the last admission in it, released ones included.
				end := first.at.Add(60 * time.Second)
				var last *pevent
				for _, e := range s.evs {
					if !e.at.Before(first.at) && e.at.Before(end) && (last == nil || e.admitSeq > last.admitSeq) {
						last = e
					}
				}
				var sum, over int64
				for _, e := range live {
					if e.at.Before(first.at) || !e.at.Before(end) {
						continue
					}
					sum += e.actual
					inFlight := e.admitSeq <= last.admitSeq && e.closeSeq > last.admitSeq
					if inFlight && e.actual > e.reserved {
						over += e.actual - e.reserved
					}
				}
				if sum > s.limit[2].Max+over {
					s.fail("tokens of the interval from %s: %d actual, limit %d plus %d of excess in flight",
						first.at.Format("15:04:05.000"), sum, s.limit[2].Max, over)
				}
				if strict && sum > s.limit[2].Max {
					s.fail("no request exceeds its reservation, yet %d tokens were admitted in an interval, limit %d", sum, s.limit[2].Max)
				}
			}
		}
	}
}

// TestPropertyConservation is T-618.
func TestPropertyConservation(t *testing.T) {
	seed := seedOf(t)
	for n := 0; n < schedules(); n++ {
		ttl := time.Duration(1+rngOf(seed, n+1<<20).IntN(120)) * time.Second
		s := newSched(t, seed, n, schedCfg{ttl: ttl, allowAbove: true, model: false, ops: 200})
		s.run()
		// Some reservations are left open on purpose: they must expire.
		for _, e := range s.open() {
			if s.rng.IntN(2) == 0 {
				s.close(e, s.rng.IntN(2) == 0)
			}
		}
		s.clk.advance(ttl + time.Second)
		s.m.Sweep(s.clk.now())
		if o := s.m.Outstanding(); o != 0 {
			s.fail("%d reservations outstanding after the TTL and a sweep", o)
		}
		if h := headroom(s.t, s.m, s.limit[0]); h != s.limit[0].Max {
			s.fail("gauge not zero: %d of %d slots free", h, s.limit[0].Max)
		}
		s.clk.advance(61 * time.Second)
		s.m.Sweep(s.clk.now())
		if c := s.m.Counters(); c != 0 {
			s.fail("%d counters held 61 s after everything ended", c)
		}
	}
}

// TestMemoryRace is T-620: run with -race.
func TestMemoryRace(t *testing.T) {
	clk := newClock()
	m, _ := newMemory(t, clk)
	const tenants, workers, perWorker = 8, 64, 300
	type tl struct {
		limits   []Limit
		inFlight atomic.Int64
	}
	var ts [tenants]*tl
	for i := range ts {
		ts[i] = &tl{limits: nineLimits(tid(byte(i+1)), 4, 1000000, 1000000)} // same subject IDs in every tenant
	}
	seed := seedOf(t)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rngOf(seed, w)
			t1 := ts[w%tenants]
			for i := 0; i < perWorker; i++ {
				res, ref, err := m.Reserve(context.Background(), request("race", time.Hour, int64(rng.IntN(100)), t1.limits...))
				if err != nil {
					t.Errorf("reserve: %v", err)
					return
				}
				if ref != nil {
					m.Sweep(clk.now())
					continue
				}
				if n := t1.inFlight.Add(1); n > 4 {
					t.Errorf("%d in flight of tenant %d, limit 4", n, w%tenants)
				}
				runtime.Gosched()
				t1.inFlight.Add(-1)
				var err2 error
				if rng.IntN(2) == 0 {
					err2 = m.Settle(context.Background(), res, Amounts{Tokens: int64(rng.IntN(150))})
				} else {
					err2 = m.Release(context.Background(), res)
				}
				if err2 != nil {
					t.Errorf("close: %v", err2)
					return
				}
				if i%50 == 0 {
					m.Sweep(clk.now())
				}
			}
		}()
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	if o := m.Outstanding(); o != 0 {
		t.Fatalf("%d outstanding after every reservation ended", o)
	}
	for i, x := range ts {
		if h := headroom(t, m, x.limits[0]); h != 4 {
			t.Fatalf("tenant %d: gauge not zero, %d of 4 slots free", i, h)
		}
	}
	clk.advance(61 * time.Second)
	m.Sweep(clk.now())
	if c := m.Counters(); c != 0 {
		t.Fatalf("%d counters held 61 s later", c)
	}
}
