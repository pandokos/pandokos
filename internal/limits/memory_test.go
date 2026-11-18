package limits

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/pricing"
)

var bg = context.Background()

// exhaust makes l have no room for a request of tokens estimate 10 and returns the limits to hold
// it so that the test can release them.
func exhaust(t *testing.T, m *Memory, l Limit) {
	t.Helper()
	switch l.Metric {
	case TokensPerMinute:
		// Max is 5 in this case and the estimate is 10: nothing to prepare.
	case RequestsPerMinute:
		res := mustReserve(t, m, request("fill", time.Hour, 0, l))
		if err := m.Settle(bg, res, Amounts{}); err != nil {
			t.Fatal(err)
		}
	case ConcurrentRequests:
		mustReserve(t, m, request("fill", time.Hour, 0, l))
	}
}

// TestReserveAtomic is T-611.
func TestReserveAtomic(t *testing.T) {
	a := tid(1)
	t.Run("any one of nine limits without room changes nothing and is named first in order", func(t *testing.T) {
		for i := 0; i < 9; i++ {
			clk := newClock()
			m, _ := newMemory(t, clk)
			all := nineLimits(a, 100, 100, 1000)
			// The limits from i on have no room; those before have plenty.
			for j := i; j < 9; j++ {
				if all[j].Metric == TokensPerMinute {
					all[j].Max = 5
				} else {
					all[j].Max = 1
				}
				exhaust(t, m, all[j])
			}
			before := headrooms(t, m, all[:i])
			outBefore := m.Outstanding()
			ref := mustRefuse(t, m, request("r", time.Minute, 10, all...))
			if !reflect.DeepEqual(ref.Limit, all[i]) {
				t.Fatalf("case %d: refused by %+v, want the first without room %+v", i, ref.Limit, all[i])
			}
			if m.Outstanding() != outBefore {
				t.Fatalf("case %d: outstanding %d->%d", i, outBefore, m.Outstanding())
			}
			if after := headrooms(t, m, all[:i]); !equalInts(before, after) {
				t.Fatalf("case %d: a limit with room changed: %v -> %v", i, before, after)
			}
		}
	})

	t.Run("a refusal is atomic for every single limit without room", func(t *testing.T) {
		for i := 0; i < 9; i++ {
			clk := newClock()
			m, _ := newMemory(t, clk)
			all := nineLimits(a, 100, 100, 1000)
			if all[i].Metric == TokensPerMinute {
				all[i].Max = 5
			} else {
				all[i].Max = 1
			}
			exhaust(t, m, all[i])
			before := headrooms(t, m, all)
			mustRefuse(t, m, request("r", time.Minute, 10, all...))
			if after := headrooms(t, m, all); !equalInts(before, after) {
				t.Fatalf("case %d: counters changed by a refusal: %v -> %v", i, before, after)
			}
		}
	})

	t.Run("an admission takes one request, one slot and the tokens from every limit", func(t *testing.T) {
		clk := newClock()
		m, _ := newMemory(t, clk)
		all := nineLimits(a, 5, 5, 100)
		res := mustReserve(t, m, request("r", time.Minute, 30, all...))
		if res.ID == "" || len(res.Limits) != 9 || res.Amounts.Tokens != 30 || !res.At.Equal(clk.now()) || !res.Expires.After(res.At) {
			t.Fatalf("reservation %+v", res)
		}
		for i, l := range all {
			want := l.Max - 1
			if l.Metric == TokensPerMinute {
				want = l.Max - 30
			}
			if got := headroom(t, m, l); got != want {
				t.Errorf("limit %d (%s): headroom %d, want %d", i, l.Metric, got, want)
			}
		}
		if m.Outstanding() != 1 {
			t.Fatalf("%d outstanding", m.Outstanding())
		}
	})

	t.Run("RetryAfter is the longest over every limit without room", func(t *testing.T) {
		clk := newClock()
		m, _ := newMemory(t, clk)
		req := lim(a, SubjectTenant, "", RequestsPerMinute, 1)
		tok := lim(a, SubjectTenant, "", TokensPerMinute, 100)
		res := mustReserve(t, m, request("one", time.Hour, 0, req))
		_ = m.Settle(bg, res, Amounts{})
		clk.advance(30 * time.Second)
		res = mustReserve(t, m, request("two", time.Hour, 100, tok))
		_ = m.Settle(bg, res, Amounts{Tokens: 100})
		clk.advance(10 * time.Second) // 40 s after the request, 10 s after the tokens
		ref := mustRefuse(t, m, request("three", time.Hour, 50, req, tok))
		if !reflect.DeepEqual(ref.Limit, req) {
			t.Fatalf("named %+v, want the request limit first", ref.Limit)
		}
		if ref.RetryAfter != 51*time.Second {
			t.Fatalf("RetryAfter %s, want 51s (the tokens leave last)", ref.RetryAfter)
		}
	})

	t.Run("limits of two tenants are an error and reserve nothing", func(t *testing.T) {
		clk := newClock()
		m, _ := newMemory(t, clk)
		l := []Limit{lim(tid(1), SubjectTenant, "", RequestsPerMinute, 5), lim(tid(2), SubjectTenant, "", RequestsPerMinute, 5)}
		_, ref, err := m.Reserve(bg, request("r", time.Minute, 1, l...))
		if err == nil || ref != nil {
			t.Fatalf("error %v, refusal %v: want an error", err, ref)
		}
		if m.Outstanding() != 0 || m.Counters() != 0 {
			t.Fatalf("outstanding %d counters %d after an error", m.Outstanding(), m.Counters())
		}
	})

	t.Run("no limit is admitted and holds nothing", func(t *testing.T) {
		clk := newClock()
		m, _ := newMemory(t, clk)
		mustReserve(t, m, request("r", time.Minute, 10))
		if m.Counters() != 0 {
			t.Fatalf("%d counters for a request without limits", m.Counters())
		}
	})
}

// TestSettle is T-612.
func TestSettle(t *testing.T) {
	a := tid(1)
	conc := lim(a, SubjectTenant, "", ConcurrentRequests, 5)
	reqs := lim(a, SubjectTenant, "", RequestsPerMinute, 10)
	toks := lim(a, SubjectTenant, "", TokensPerMinute, 1000)
	all := []Limit{conc, reqs, toks}

	t.Run("below the reservation the remainder returns at once", func(t *testing.T) {
		clk := newClock()
		m, _ := newMemory(t, clk)
		res := mustReserve(t, m, request("r", time.Hour, 400, all...))
		clk.advance(20 * time.Second)
		if err := m.Settle(bg, res, Amounts{Tokens: 100}); err != nil {
			t.Fatal(err)
		}
		if got := headrooms(t, m, all); !equalInts(got, []int64{5, 9, 900}) {
			t.Fatalf("headrooms %v, want [5 9 900]: slot freed, request counted, 100 tokens counted", got)
		}
		if m.Outstanding() != 0 {
			t.Fatalf("%d outstanding", m.Outstanding())
		}
	})

	t.Run("above the reservation the excess is counted", func(t *testing.T) {
		clk := newClock()
		m, _ := newMemory(t, clk)
		res := mustReserve(t, m, request("r", time.Hour, 400, all...))
		if err := m.Settle(bg, res, Amounts{Tokens: 700}); err != nil {
			t.Fatal(err)
		}
		if got := headroom(t, m, toks); got != 300 {
			t.Fatalf("tokens headroom %d, want 300", got)
		}
		// Over the limit: nothing is admitted until the window has moved on.
		res = mustReserve(t, m, request("s", time.Hour, 300, all...))
		_ = m.Settle(bg, res, Amounts{Tokens: 1200}) // 700 + 1200 > 1000
		mustRefuse(t, m, request("t", time.Hour, 1, all...))
		if got := headroom(t, m, toks); got != 0 {
			t.Fatalf("tokens headroom %d over the limit, want 0 (never negative)", got)
		}
	})

	t.Run("after the admission second left the window nothing changes", func(t *testing.T) {
		clk := newClock()
		m, _ := newMemory(t, clk)
		res := mustReserve(t, m, request("r", time.Hour, 400, all...))
		clk.advance(61 * time.Second)
		if err := m.Settle(bg, res, Amounts{Tokens: 900}); err != nil {
			t.Fatal(err)
		}
		if got := headrooms(t, m, all); !equalInts(got, []int64{5, 10, 1000}) {
			t.Fatalf("headrooms %v, want everything free", got)
		}
	})

	t.Run("a second settle is ErrNotOutstanding and changes nothing", func(t *testing.T) {
		clk := newClock()
		m, _ := newMemory(t, clk)
		res := mustReserve(t, m, request("r", time.Hour, 400, all...))
		if err := m.Settle(bg, res, Amounts{Tokens: 100}); err != nil {
			t.Fatal(err)
		}
		before := headrooms(t, m, all)
		if err := m.Settle(bg, res, Amounts{Tokens: 900}); !errors.Is(err, ErrNotOutstanding) {
			t.Fatalf("second settle: %v", err)
		}
		if err := m.Release(bg, res); !errors.Is(err, ErrNotOutstanding) {
			t.Fatalf("release after settle: %v", err)
		}
		if after := headrooms(t, m, all); !equalInts(before, after) {
			t.Fatalf("a second settle changed %v -> %v", before, after)
		}
	})
}

// TestRelease is T-613.
func TestRelease(t *testing.T) {
	a := tid(1)
	all := nineLimits(a, 5, 10, 1000)

	for _, advance := range []time.Duration{0, 10 * time.Second, 60 * time.Second, 61 * time.Second, 5 * time.Minute} {
		clk := newClock()
		m, _ := newMemory(t, clk)
		keep := mustReserve(t, m, request("keep", time.Hour, 50, all...))
		before := headrooms(t, m, all)
		res := mustReserve(t, m, request("r", time.Hour, 300, all...))
		clk.advance(advance)
		if err := m.Release(bg, res); err != nil {
			t.Fatal(err)
		}
		want := before
		if advance >= 61*time.Second {
			// The first reservation has left the window too: only the slot it holds remains.
			want = headrooms(t, m, all)
			for i, l := range all {
				if l.Metric != ConcurrentRequests && want[i] != l.Max {
					t.Fatalf("advance %s: headroom %d of %s, want it free", advance, want[i], l.Metric)
				}
			}
		} else if got := headrooms(t, m, all); !equalInts(got, want) {
			t.Fatalf("advance %s: after release %v, before the reserve %v", advance, got, before)
		}
		if err := m.Release(bg, res); !errors.Is(err, ErrNotOutstanding) {
			t.Fatalf("second release: %v", err)
		}
		if m.Outstanding() != 1 {
			t.Fatalf("advance %s: %d outstanding, want 1", advance, m.Outstanding())
		}
		_ = keep
	}

	t.Run("a released request returns to an empty store", func(t *testing.T) {
		clk := newClock()
		m, _ := newMemory(t, clk)
		res := mustReserve(t, m, request("r", time.Hour, 300, all...))
		if err := m.Release(bg, res); err != nil {
			t.Fatal(err)
		}
		if got := headrooms(t, m, all); got[0] != 5 || got[1] != 10 || got[2] != 1000 {
			t.Fatalf("headrooms %v", got)
		}
		if m.Outstanding() != 0 {
			t.Fatalf("%d outstanding", m.Outstanding())
		}
	})
}

// TestExpiry is T-614.
func TestExpiry(t *testing.T) {
	a := tid(1)
	all := nineLimits(a, 5, 10, 1000)
	const ttl = 10 * time.Second

	clk := newClock()
	m, logs := newMemory(t, clk)
	res := mustReserve(t, m, request("req-expiry-1", ttl, 300, all...))
	start := clk.now()

	if n := m.Sweep(start.Add(ttl - time.Nanosecond)); n != 0 {
		t.Fatalf("a reservation expired %s before its TTL", time.Nanosecond)
	}
	if m.Outstanding() != 1 {
		t.Fatalf("%d outstanding before the TTL", m.Outstanding())
	}
	clk.advance(ttl + time.Second)
	if n := m.Sweep(clk.now()); n != 1 {
		t.Fatalf("Sweep after the TTL expired %d, want 1", n)
	}
	if m.Outstanding() != 0 {
		t.Fatalf("%d outstanding after the sweep", m.Outstanding())
	}
	if got := headrooms(t, m, all); got[0] != 5 || got[1] != 10 || got[2] != 1000 {
		t.Fatalf("headrooms %v: slot, request and tokens must be free", got)
	}
	if n := m.Sweep(clk.now()); n != 0 {
		t.Fatalf("a second sweep expired %d", n)
	}
	lines := 0
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "limit reservation expired") {
			lines++
			if !strings.Contains(line, `"level":"ERROR"`) || !strings.Contains(line, "req-expiry-1") || !strings.Contains(line, a.String()) {
				t.Errorf("log line %s: want level error, the request ID and the tenant", line)
			}
		}
	}
	if lines != 1 {
		t.Fatalf("%d expiry log lines, want 1", lines)
	}
	if err := m.Settle(bg, res, Amounts{Tokens: 10}); !errors.Is(err, ErrNotOutstanding) {
		t.Fatalf("settle after expiry: %v", err)
	}
	if err := m.Release(bg, res); !errors.Is(err, ErrNotOutstanding) {
		t.Fatalf("release after expiry: %v", err)
	}

	t.Run("empty counters are dropped", func(t *testing.T) {
		clk := newClock()
		m, _ := newMemory(t, clk)
		res := mustReserve(t, m, request("r", ttl, 10, all...))
		_ = m.Settle(bg, res, Amounts{Tokens: 10})
		if m.Counters() == 0 {
			t.Fatal("no counters held while amounts are in their window")
		}
		clk.advance(30 * time.Second)
		m.Sweep(clk.now())
		if m.Counters() == 0 {
			t.Fatal("counters dropped while an amount is still in the window")
		}
		clk.advance(32 * time.Second)
		m.Sweep(clk.now())
		if n := m.Counters(); n != 0 {
			t.Fatalf("%d counters held when every window is empty", n)
		}
	})

	t.Run("Run returns when its context ends", func(t *testing.T) {
		clk := newClock()
		m, _ := newMemory(t, clk)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { m.Run(ctx); close(done) }()
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return after its context was cancelled")
		}
	})

	t.Run("Run sweeps on its own", func(t *testing.T) {
		clk := newClock()
		m, _ := newMemory(t, clk)
		mustReserve(t, m, request("r", time.Second, 10, all...))
		clk.advance(2 * time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go m.Run(ctx)
		deadline := time.Now().Add(2500 * time.Millisecond)
		for m.Outstanding() != 0 {
			if time.Now().After(deadline) {
				t.Fatal("Run did not expire the reservation within its sweep period")
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

// TestRetryAfter is T-615.
func TestRetryAfter(t *testing.T) {
	a := tid(1)
	reqs := lim(a, SubjectTenant, "", RequestsPerMinute, 1)
	toks := lim(a, SubjectTenant, "", TokensPerMinute, 100)
	conc := lim(a, SubjectTenant, "", ConcurrentRequests, 1)

	t.Run("requests: until the admission second leaves, rounded up", func(t *testing.T) {
		for _, c := range []struct {
			offset time.Duration // of the admission within its second
			after  time.Duration // time after the admission of the refused call
			want   time.Duration
		}{
			{0, 10 * time.Second, 51 * time.Second},
			{250 * time.Millisecond, 10 * time.Second, 51 * time.Second}, // 50.75 s
			{999 * time.Millisecond, 0, 61 * time.Second},                // the second leaves 60.001 s later
			{0, 0, 61 * time.Second},
			{0, 60*time.Second + 500*time.Millisecond, 1 * time.Second}, // 0.5 s rounded up
		} {
			clk := newClock()
			clk.advance(c.offset)
			m, _ := newMemory(t, clk)
			res := mustReserve(t, m, request("a", time.Hour, 0, reqs))
			_ = m.Settle(bg, res, Amounts{})
			clk.advance(c.after)
			ref := mustRefuse(t, m, request("b", time.Hour, 0, reqs))
			if ref.RetryAfter != c.want {
				t.Errorf("%+v: RetryAfter %s, want %s", c, ref.RetryAfter, c.want)
			}
			if ref.OverLimit {
				t.Errorf("%+v: OverLimit for one request", c)
			}
			if ref.RetryAfter < time.Second || ref.RetryAfter > 61*time.Second {
				t.Errorf("%+v: RetryAfter %s out of 1 s to 61 s", c, ref.RetryAfter)
			}
			if ref.Reset <= 0 || ref.Reset > 61*time.Second {
				t.Errorf("%+v: Reset %s", c, ref.Reset)
			}
			if ref.Remaining != 0 {
				t.Errorf("%+v: Remaining %d, want 0", c, ref.Remaining)
			}
			clk.advance(ref.RetryAfter)
			if _, ref2, err := m.Reserve(bg, request("c", time.Hour, 0, reqs)); err != nil || ref2 != nil {
				t.Errorf("%+v: a retry at RetryAfter was refused: %v %v", c, ref2, err)
			}
		}
	})

	t.Run("tokens: as many seconds must leave as it takes for the request to fit", func(t *testing.T) {
		for _, c := range []struct {
			estimate int64
			want     time.Duration
		}{
			{estimate: 50, want: 36 * time.Second}, // the 60 admitted at 0 leave at 61, 25 s from now
			{estimate: 95, want: 56 * time.Second}, // all must leave: the last, admitted at 20, leaves at 81
			{estimate: 100, want: 56 * time.Second},
		} {
			clk := newClock()
			m, _ := newMemory(t, clk)
			for _, step := range []struct {
				tokens int64
				wait   time.Duration
			}{{60, 10 * time.Second}, {30, 10 * time.Second}, {10, 5 * time.Second}} {
				res := mustReserve(t, m, request("fill", time.Hour, step.tokens, toks))
				_ = m.Settle(bg, res, Amounts{Tokens: step.tokens})
				clk.advance(step.wait)
			}
			// Now: 25 s after the first admission.
			ref := mustRefuse(t, m, request("r", time.Hour, c.estimate, toks))
			if ref.RetryAfter != c.want {
				t.Errorf("estimate %d: RetryAfter %s, want %s", c.estimate, ref.RetryAfter, c.want)
			}
			if ref.Remaining != 0 {
				t.Errorf("estimate %d: Remaining %d, want 0 (the window holds 100)", c.estimate, ref.Remaining)
			}
			clk.advance(ref.RetryAfter)
			if _, ref2, err := m.Reserve(bg, request("again", time.Hour, c.estimate, toks)); err != nil || ref2 != nil {
				t.Errorf("estimate %d: a retry at RetryAfter was refused: %v %v", c.estimate, ref2, err)
			}
		}
	})

	t.Run("tokens: Remaining can be positive and smaller than the request", func(t *testing.T) {
		clk := newClock()
		m, _ := newMemory(t, clk)
		res := mustReserve(t, m, request("fill", time.Hour, 70, toks))
		_ = m.Settle(bg, res, Amounts{Tokens: 70})
		ref := mustRefuse(t, m, request("r", time.Hour, 50, toks))
		if ref.Remaining != 30 || !reflect.DeepEqual(ref.Limit, toks) {
			t.Fatalf("refusal %+v", ref)
		}
	})

	t.Run("an estimate above the limit is OverLimit and waits 60 s", func(t *testing.T) {
		clk := newClock()
		m, _ := newMemory(t, clk)
		ref := mustRefuse(t, m, request("r", time.Hour, 101, toks))
		if !ref.OverLimit || ref.RetryAfter != 60*time.Second {
			t.Fatalf("refusal %+v, want OverLimit and 60 s", ref)
		}
		if ref.Remaining != 100 {
			t.Fatalf("Remaining %d on an empty window, want 100", ref.Remaining)
		}
	})

	t.Run("concurrency waits 1 s and has no reset", func(t *testing.T) {
		clk := newClock()
		m, _ := newMemory(t, clk)
		mustReserve(t, m, request("a", time.Hour, 0, conc))
		ref := mustRefuse(t, m, request("b", time.Hour, 0, conc))
		if ref.RetryAfter != time.Second || ref.Reset != 0 || ref.Remaining != 0 || !reflect.DeepEqual(ref.Limit, conc) {
			t.Fatalf("refusal %+v", ref)
		}
	})
}

// TestTenantSeparation is T-621.
func TestTenantSeparation(t *testing.T) {
	clk := newClock()
	m, _ := newMemory(t, clk)
	a, b := tid(1), tid(2)
	la, lb := nineLimits(a, 2, 3, 100), nineLimits(b, 2, 3, 100)

	for i := 0; i < 2; i++ {
		mustReserve(t, m, request("a", time.Hour, 10, la...))
	}
	mustRefuse(t, m, request("a", time.Hour, 10, la...)) // concurrency of A is full
	if got := headrooms(t, m, lb); got[0] != 2 || got[1] != 3 || got[2] != 100 || got[3] != 2 || got[8] != 100 {
		t.Fatalf("tenant B's counters changed by tenant A: %v", got)
	}
	// Exhaust the tokens of A, then B is still admitted in full.
	for i := 0; i < 2; i++ {
		res := mustReserve(t, m, request("b", time.Hour, 50, lb...))
		_ = m.Release(bg, res)
	}
	if got := headrooms(t, m, la); got[0] != 0 {
		t.Fatalf("tenant A's slots %d after B's traffic, want 0", got[0])
	}
	resB := mustReserve(t, m, request("b", time.Hour, 100, lb...))
	if got := headroom(t, m, la[2]); got != 80 {
		t.Fatalf("A's tenant tokens headroom %d, want 80 (two reservations of 10): B's tokens leaked", got)
	}
	_ = resB
}

// TestAttributedSecond covers sprint.md 5.4: an amount belongs to the second it was admitted
// into, under the shard lock. When the wall clock steps back, Reserve attributes the amount to
// the newest second of the window and settle and release take it from that same second.
func TestAttributedSecond(t *testing.T) {
	a := tid(1)
	for _, tc := range []struct {
		name   string
		metric Metric
		max    int64
		tokens int64
		end    func(*testing.T, *Memory, Reservation)
	}{
		{"tokens released", TokensPerMinute, 20, 10, func(t *testing.T, m *Memory, r Reservation) {
			if err := m.Release(bg, r); err != nil {
				t.Fatal(err)
			}
		}},
		{"tokens settled to nothing", TokensPerMinute, 20, 10, func(t *testing.T, m *Memory, r Reservation) {
			if err := m.Settle(bg, r, Amounts{}); err != nil {
				t.Fatal(err)
			}
		}},
		{"requests released", RequestsPerMinute, 2, 0, func(t *testing.T, m *Memory, r Reservation) {
			if err := m.Release(bg, r); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := newClock()
			m, _ := newMemory(t, clk)
			l := lim(a, SubjectTenant, "", tc.metric, tc.max)
			mustReserve(t, m, request("a", time.Hour, tc.tokens, l))
			clk.advance(-30 * time.Second) // the wall clock steps back
			second := mustReserve(t, m, request("b", time.Hour, tc.tokens, l))
			tc.end(t, m, second)
			// What is left is the first reservation alone, still in its second.
			per := tc.max / 2
			if got := headroom(t, m, l); got != tc.max-per {
				t.Fatalf("right after: headroom %d, want %d", got, tc.max-per)
			}
			clk.advance(61 * time.Second) // 31 s after the first admission
			if got := headroom(t, m, l); got != tc.max-per {
				t.Fatalf("31 s later: headroom %d, want %d (a phantom count or a lost one)", got, tc.max-per)
			}
			clk.advance(30 * time.Second) // 61 s after the first admission: it has left
			if got := headroom(t, m, l); got != tc.max {
				t.Fatalf("61 s after the first admission: headroom %d, want %d", got, tc.max)
			}
		})
	}
}

// TestDuplicateCounterKeys: two limits of one request with the same counter key are a caller
// error; they would otherwise create the counter twice and lose an increment.
func TestDuplicateCounterKeys(t *testing.T) {
	a := tid(1)
	for _, metric := range []Metric{ConcurrentRequests, RequestsPerMinute, TokensPerMinute} {
		t.Run(string(metric), func(t *testing.T) {
			clk := newClock()
			m, _ := newMemory(t, clk)
			var s Store = m
			l1 := lim(a, SubjectTenant, "", metric, 15)
			l2 := lim(a, SubjectTenant, "", metric, 100)
			l2.Level = LevelPlatform
			if _, ref, err := s.Reserve(bg, request("dup", time.Minute, 10, l1, l2)); err == nil {
				t.Fatalf("admitted (refusal %v), want an error", ref)
			}
			if m.Outstanding() != 0 || m.Counters() != 0 {
				t.Fatalf("a rejected request left %d reservations and %d counters", m.Outstanding(), m.Counters())
			}
		})
	}
}

// TestMemoryRacePeriods is T-655: reservations and settlements across a day boundary while the
// cut is folded forward, counters are rebased and loaded, and the store is swept. Nothing races
// and nothing is lost or counted twice: at the end every counter holds exactly what was settled.
func TestMemoryRacePeriods(t *testing.T) {
	clk := newClock()
	clk.t = at(2026, 11, 17, 23, 55, 0)
	m, _ := newMemory(t, clk)
	tn := tid(1)
	day17 := utcCal.Of(PerDay, clk.now())
	day18 := utcCal.Of(PerDay, day17.End)
	m.SetCut(tn, day17.Start) // the cut is the start of the first period: nothing needs a load

	type rec struct {
		start  time.Time
		tokens int64
	}
	var (
		mu       sync.Mutex // the writer's side: records settled, requests in flight
		recs     []rec
		inflight = map[int]time.Time{}
		adm      sync.Mutex // serializes fold, rebase and load, as the mutex of a tenant does
	)
	limitsAt := func(start time.Time) []Limit {
		p := utcCal.Of(PerDay, start)
		return []Limit{
			perLimit(tn, SubjectTenant, "", RequestsPerDay, 1<<40, p),
			perLimit(tn, SubjectTenant, "", TokensPerDay, 1<<40, p),
		}
	}
	sumRecs := func(from, to time.Time) UsageGroup {
		var g UsageGroup
		for _, r := range recs {
			if !r.start.Before(from) && r.start.Before(to) {
				g.Requests++
				g.Tokens += r.tokens
			}
		}
		return g
	}

	// Both counters exist from the start (a reconciler would have loaded them).
	m.Load(tn, day17, limitsAt(day17.Start), nil)
	m.Load(tn, day18, limitsAt(day18.Start), nil)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	var workers sync.WaitGroup
	for w := range 64 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			rng := rngOf(uint64(w), 1)
			for i := range 40 {
				id := w*1000 + i
				mu.Lock()
				start := clk.now()
				inflight[id] = start
				mu.Unlock()
				ls := limitsAt(start)
				est := int64(1 + rng.IntN(50))
				res, ref, err := m.Reserve(context.Background(), periodReq(fmt.Sprintf("w%d-%d", w, i), start, time.Hour, est, pricing.Cost{}, ls...))
				if err != nil || ref != nil {
					t.Errorf("reserve: refusal %v, error %v", ref, err)
					return
				}
				runtime.Gosched()
				actual := int64(rng.IntN(120))
				if err := m.Settle(context.Background(), res, Amounts{Tokens: actual}); err != nil {
					t.Errorf("settle: %v", err)
					return
				}
				mu.Lock()
				recs = append(recs, rec{start, actual})
				delete(inflight, id) // after the record: the cut may pass a minute only when its records are all there
				mu.Unlock()
			}
		}()
	}
	// The clock crosses midnight while the workers run.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 30 {
			clk.advance(20 * time.Second)
			runtime.Gosched()
		}
	}()
	foldTo := func() {
		adm.Lock()
		defer adm.Unlock()
		mu.Lock()
		cut, _ := m.Cut(tn)
		limit := clk.now()
		for _, s := range inflight {
			if s.Before(limit) {
				limit = s
			}
		}
		newCut := limit.Truncate(time.Minute)
		var pieces []Piece
		if newCut.After(cut) && !cut.IsZero() {
			for from := cut; from.Before(newCut); {
				to := utcCal.Of(PerDay, from).End
				if !to.After(from) {
					break // no calendar: the test fails on its assertions, not by running forever
				}
				if to.After(newCut) {
					to = newCut
				}
				g := sumRecs(from, to)
				pieces = append(pieces, Piece{From: from, To: to, Rows: []UsageGroup{g}})
				from = to
			}
		}
		mu.Unlock()
		if newCut.After(cut) {
			m.Fold(tn, pieces, newCut)
		}
	}
	rebase := func() {
		adm.Lock()
		defer adm.Unlock()
		mu.Lock()
		cut, _ := m.Cut(tn)
		g17, g18 := sumRecs(day17.Start, minTime(cut, day17.End)), sumRecs(day18.Start, cut)
		mu.Unlock()
		m.Rebase(tn, day17, []UsageGroup{g17})
		if cut.After(day18.Start) {
			m.Rebase(tn, day18, []UsageGroup{g18})
		}
	}
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				switch g % 4 {
				case 0:
					foldTo()
				case 1:
					rebase()
				case 2:
					adm.Lock()
					m.Load(tn, day17, limitsAt(day17.Start), []UsageGroup{{Requests: 1 << 30, Tokens: 1 << 30}}) // counters exist: untouched
					adm.Unlock()
					m.Missing(limitsAt(clk.now()))
					m.Counted(limitsAt(day17.Start)[0])
				default:
					m.Sweep(clk.now())
				}
				runtime.Gosched()
			}
		}()
	}
	workers.Wait()
	close(stop)
	wg.Wait()
	foldTo()

	mu.Lock()
	defer mu.Unlock()
	if got := m.Outstanding(); got != 0 {
		t.Fatalf("%d reservations outstanding", got)
	}
	for _, p := range []Period{day17, day18} {
		want := sumRecs(p.Start, p.End)
		ls := limitsAt(p.Start)
		if c := counted(t, m, ls[0]); c.N != want.Requests {
			t.Errorf("requests of %s: counted %d, settled %d", p.Start, c.N, want.Requests)
		}
		if c := counted(t, m, ls[1]); c.N != want.Tokens {
			t.Errorf("tokens of %s: counted %d, settled %d", p.Start, c.N, want.Tokens)
		}
	}
	if len(recs) != 64*40 {
		t.Errorf("%d records, want %d", len(recs), 64*40)
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// TestReserveManyLimits is part of T-620 (sprint 5.2): a request has up to 81 limits (three subjects,
// three scopes, nine metrics) and the check for a repeated counter scales to them.
func TestReserveManyLimits(t *testing.T) {
	e := newPeriodsEnv(t)
	var ls []Limit
	for _, k := range SubjectKinds() {
		id := map[SubjectKind]string{SubjectTenant: "", SubjectServiceAccount: "sa-1", SubjectKey: "key-1"}[k]
		for _, scope := range [][2]string{{"", ""}, {"openai", ""}, {"openai", "gpt-4"}} {
			for _, m := range Metrics() {
				l := lim(e.tn, k, id, m, 1_000_000)
				l.Provider, l.Model = scope[0], scope[1]
				if kind, ok := m.Period(); ok {
					l.Period = utcCal.Of(kind, e.clk.now())
				}
				if m.Cost() {
					l.Currency = "EUR"
				}
				ls = append(ls, l)
			}
		}
	}
	if len(ls) != 81 {
		t.Fatalf("%d limits", len(ls))
	}
	var months []Limit
	for _, l := range ls {
		if l.Period == e.mon {
			months = append(months, l)
		}
	}
	e.loadMonth(months...)
	res := mustReserve(t, e.m, periodReq("many", e.clk.now(), time.Hour, 10, known("EUR", dec(t, "0.5")), ls...))
	if err := e.m.Settle(context.Background(), res, Amounts{Tokens: 10, Cost: known("EUR", dec(t, "0.5"))}); err != nil {
		t.Fatal(err)
	}
	for _, l := range ls {
		if k, ok := l.Metric.Period(); ok && k == PerDay && l.Metric == RequestsPerDay {
			if c := counted(t, e.m, l); c.N != 1 {
				t.Errorf("%s %s/%s counts %d, want 1", l.Subject, l.Provider, l.Model, c.N)
			}
		}
	}
	// A repeated counter anywhere among the 81 is an error, and nothing is reserved.
	dup := append(append([]Limit(nil), ls...), ls[40])
	if _, _, err := e.m.Reserve(context.Background(), periodReq("dup", e.clk.now(), time.Hour, 10, known("EUR", dec(t, "0.5")), dup...)); err == nil {
		t.Error("a request that repeats a counter was accepted")
	}
	if e.m.Outstanding() != 0 {
		t.Errorf("%d reservations outstanding", e.m.Outstanding())
	}
}
