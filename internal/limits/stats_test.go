package limits

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestStats is T-922 (limits part, without the database): the expired reservations of Memory, the
// runs of the Reconciler with the duration and the cut of the last pass, and the loads of the
// RuleCache are counted where they happen and read through Stats().
func TestStats(t *testing.T) {
	bg := context.Background()
	a := tid(1)

	t.Run("Memory counts the reservations the sweep expired, and only those", func(t *testing.T) {
		clk := newClock()
		m, _ := newMemory(t, clk)
		if got := m.Stats().Expired; got != 0 {
			t.Fatalf("a new store has expired %d", got)
		}
		all := nineLimits(a, 100, 100, 1000)
		settled := mustReserve(t, m, request("settled", time.Minute, 10, all...))
		if err := m.Settle(bg, settled, Amounts{Tokens: 10}); err != nil {
			t.Fatal(err)
		}
		released := mustReserve(t, m, request("released", time.Minute, 10, all...))
		if err := m.Release(bg, released); err != nil {
			t.Fatal(err)
		}
		mustReserve(t, m, request("lost-1", time.Minute, 10, all...))
		mustReserve(t, m, request("lost-2", 2*time.Minute, 10, all...))

		clk.advance(30 * time.Second)
		if n := m.Sweep(clk.now()); n != 0 || m.Stats().Expired != 0 {
			t.Fatalf("a sweep before any expiry: %d swept, %d counted", n, m.Stats().Expired)
		}
		clk.advance(time.Minute)
		if n := m.Sweep(clk.now()); n != 1 || m.Stats().Expired != 1 {
			t.Errorf("the first expiry: %d swept, %d counted, want 1 and 1", n, m.Stats().Expired)
		}
		clk.advance(time.Minute)
		if n := m.Sweep(clk.now()); n != 1 || m.Stats().Expired != 2 {
			t.Errorf("the second expiry: %d swept, %d counted, want 1 and 2", n, m.Stats().Expired)
		}
		if n := m.Sweep(clk.now()); n != 0 || m.Stats().Expired != 2 {
			t.Errorf("a sweep with nothing to expire changed the count to %d", m.Stats().Expired)
		}
	})

	t.Run("the Reconciler counts one run for each tenant it reconciles, by result, with the duration and the cut of the last pass", func(t *testing.T) {
		e := newRecEnv(t)
		if st := e.rec.Stats(); st.OK != 0 || st.Failed != 0 || !st.LastCut.IsZero() || st.LastDuration != 0 {
			t.Fatalf("a new reconciler: %+v", st)
		}
		// The tenant is followed once it has a period counter: ensure loads one and sets the cut.
		l := e.limit(TokensPerMonth, utcCal.Of(PerMonth, e.clk.now()))
		if err := e.rec.Ensure(bg, e.tn, []Limit{l}); err != nil {
			t.Fatal(err)
		}
		// Each read of the ledger takes 250 ms of the test clock, so that the pass has a duration.
		e.led.onUsage = func(int) { e.clk.advance(250 * time.Millisecond) }

		e.clk.advance(10 * time.Minute) // the next cut is after the cut the load set
		e.rec.Tick(bg)
		st := e.rec.Stats()
		if st.OK != 1 || st.Failed != 0 {
			t.Fatalf("after a pass that folded: %+v, want one ok run", st)
		}
		if cut := e.cut(t); !st.LastCut.Equal(cut) || st.LastCut.After(e.clk.now()) {
			t.Errorf("the last cut is %v, the store's is %v", st.LastCut, cut)
		}
		if st.LastDuration < 250*time.Millisecond {
			t.Errorf("the duration of the last pass is %v, want at least the 250 ms the ledger took", st.LastDuration)
		}

		// A pass with nothing to fold is still a successful run.
		e.rec.Tick(bg)
		if st := e.rec.Stats(); st.OK != 2 || st.Failed != 0 {
			t.Errorf("a quiet pass: %+v, want two ok runs", st)
		}

		// A ledger that fails: the run is counted as failed and the cut stays.
		before := e.rec.Stats().LastCut
		e.led.setErr(errors.New("synthetic ledger failure"))
		e.clk.advance(10 * time.Minute)
		e.rec.Tick(bg)
		st = e.rec.Stats()
		if st.OK != 2 || st.Failed != 1 {
			t.Errorf("after a failing pass: %+v, want two ok and one failed", st)
		}
		if !st.LastCut.Equal(before) {
			t.Errorf("a failed fold moved the last cut from %v to %v", before, st.LastCut)
		}
		e.led.setErr(nil)
		e.rec.Tick(bg)
		if st := e.rec.Stats(); st.OK != 3 || st.Failed != 1 || !st.LastCut.After(before) {
			t.Errorf("after recovery: %+v", st)
		}
	})

	t.Run("the RuleCache counts loads, not lookups", func(t *testing.T) {
		c, l, clk := newTestRuleCache(t, 30*time.Second)
		if st := c.Stats(); st.OK != 0 || st.Failed != 0 {
			t.Fatalf("a new cache: %+v", st)
		}
		for i := 0; i < 3; i++ {
			if _, err := c.Rules(bg, a); err != nil {
				t.Fatal(err)
			}
		}
		if st := c.Stats(); st.OK != 1 || st.Failed != 0 {
			t.Errorf("three lookups of one entry: %+v, want one ok load", st)
		}
		clk.advance(time.Minute)
		l.failNext.Store(1)
		if _, err := c.Rules(bg, a); err == nil {
			t.Fatal("a failing load was not reported")
		}
		if st := c.Stats(); st.OK != 1 || st.Failed != 1 {
			t.Errorf("after a failing load: %+v, want one ok and one failed", st)
		}
		if _, err := c.Rules(bg, a); err != nil {
			t.Fatal(err)
		}
		if st := c.Stats(); st.OK != 2 || st.Failed != 1 {
			t.Errorf("after the retry: %+v, want two ok and one failed", st)
		}
	})
}
