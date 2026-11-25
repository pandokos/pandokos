package limits

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/tenant"
)

// recEnv is a store, a fake ledger over the reference and a reconciler, at 2026-11-17 12:00 UTC.
type recEnv struct {
	clk     *clock
	m       *Memory
	sink    *recordingSink
	model   *refModel
	led     *fakeLedger
	rec     *Reconciler
	tn      tenant.ID
	pending time.Time // what the writer reports as its oldest uncommitted start; zero: none
}

func newRecEnv(t *testing.T) *recEnv {
	t.Helper()
	clk := newClock()
	e := &recEnv{clk: clk, sink: &recordingSink{}, model: &refModel{}, tn: tid(1)}
	e.m = newMemoryWith(t, clk, e.sink)
	e.led = &fakeLedger{model: e.model, clk: clk}
	rec, err := NewReconciler(ReconcilerOptions{
		Store: e.m, Ledger: e.led, Horizon: testHorizon, Interval: testInterval, Logger: quietLogger(), Now: clk.now,
		Pending: func() (time.Time, bool) { return e.pending, !e.pending.IsZero() },
	})
	if err != nil {
		t.Fatal(err)
	}
	e.rec = rec
	return e
}

// commit adds a committed, settled record of the tenant to the ledger of the reference.
func (e *recEnv) commit(start time.Time, key string, tokens, costMicro int64) {
	e.model.add(&refReq{tn: e.tn, sa: "sa1", key: key, provider: "openai", model: "gpt", start: start, end: refSettled,
		admitted: true, actTokens: tokens, actCost: costMicro, commitAt: start, resolveAt: start})
}

func (e *recEnv) limit(m Metric, p Period) Limit {
	l := perLimit(e.tn, SubjectTenant, "", m, 1_000_000, p)
	if m.Cost() {
		l.Currency = refCurrency
	}
	return l
}

func (e *recEnv) cut(t *testing.T) time.Time {
	t.Helper()
	c, ok := e.m.Cut(e.tn)
	if !ok {
		t.Fatal("the tenant has no cut")
	}
	return c
}

// TestLoadFoldRebase is T-651.
func TestLoadFoldRebase(t *testing.T) {
	t.Parallel()
	bg := context.Background()

	t.Run("load: base and seed from groups, existing counters untouched", func(t *testing.T) {
		e := newPeriodsEnv(t)
		cut := at(2026, 11, 17, 11, 47, 0)
		e.m.SetCut(e.tn, cut)
		l := perLimit(e.tn, SubjectTenant, "", TokensPerMonth, 100_000, e.mon)
		lk := perLimit(e.tn, SubjectKey, "k1", TokensPerMonth, 100_000, e.mon)
		rows := []UsageGroup{
			{PrincipalID: "sa1", KeyID: "k1", ProviderID: "openai", ModelID: "gpt", Requests: 3, Tokens: 300},
			{PrincipalID: "sa2", KeyID: "k2", ProviderID: "openai", ModelID: "gpt", Requests: 1, Tokens: 20},
			{Minute: cut.Unix() / 60, PrincipalID: "sa1", KeyID: "k1", ProviderID: "openai", ModelID: "gpt", Requests: 1, Tokens: 40},
			{Minute: cut.Unix()/60 + 5, PrincipalID: "sa2", KeyID: "k2", ProviderID: "openai", ModelID: "gpt", Requests: 1, Tokens: 4},
		}
		e.m.Load(e.tn, e.mon, []Limit{l, lk}, rows)
		if c := counted(t, e.m, l); c.N != 364 {
			t.Errorf("tenant counter %d, want 364 (base 320 and a seed of 44)", c.N)
		}
		if c := counted(t, e.m, lk); c.N != 340 {
			t.Errorf("key counter %d, want 340", c.N)
		}
		e.m.Load(e.tn, e.mon, []Limit{l}, []UsageGroup{{Requests: 9, Tokens: 9_000}})
		if c := counted(t, e.m, l); c.N != 364 {
			t.Errorf("a second load changed the counter to %d", c.N)
		}
		if miss := e.m.Missing([]Limit{l, lk}); len(miss) != 0 {
			t.Errorf("missing %+v after the load", miss)
		}
		if periods := e.m.Periods(e.tn); len(periods) != 1 || periods[0] != e.mon {
			t.Errorf("periods %+v", periods)
		}
		if ts := e.m.PeriodTenants(); len(ts) != 1 || ts[0] != e.tn {
			t.Errorf("tenants %+v", ts)
		}
	})

	t.Run("fold: pieces go to the periods that contain them", func(t *testing.T) {
		clk := newClock()
		clk.t = at(2026, 11, 30, 23, 52, 0)
		m := newMemoryWith(t, clk, nil)
		tn := tid(1)
		cut := at(2026, 11, 30, 23, 50, 0)
		m.SetCut(tn, cut)
		nov, dec1 := utcCal.Of(PerMonth, cut), utcCal.Of(PerMonth, at(2026, 12, 1, 0, 5, 0))
		d30, d1 := utcCal.Of(PerDay, cut), utcCal.Of(PerDay, at(2026, 12, 1, 0, 5, 0))
		ls := []Limit{
			perLimit(tn, SubjectTenant, "", TokensPerMonth, 1e9, nov), perLimit(tn, SubjectTenant, "", TokensPerMonth, 1e9, dec1),
			perLimit(tn, SubjectTenant, "", TokensPerDay, 1e9, d30), perLimit(tn, SubjectTenant, "", TokensPerDay, 1e9, d1),
		}
		m.Load(tn, nov, ls[:1], nil)
		m.Load(tn, dec1, ls[1:2], nil)
		m.Load(tn, d30, ls[2:3], nil)
		m.Load(tn, d1, ls[3:], nil)
		// A request of this instance in minute 23:51, in the live part.
		res := mustReserve(t, m, periodReq("live", at(2026, 11, 30, 23, 51, 10), time.Hour, 100, pricing.Cost{}, ls[0], ls[2]))
		if err := m.Settle(bg, res, Amounts{Tokens: 100}); err != nil {
			t.Fatal(err)
		}
		if c := counted(t, m, ls[0]); c.N != 100 {
			t.Fatalf("November counts %d before the fold, want 100", c.N)
		}
		newCut := at(2026, 12, 1, 0, 10, 0)
		m.Fold(tn, []Piece{
			{From: cut, To: d1.Start, Rows: []UsageGroup{{Requests: 2, Tokens: 107}}}, // the request above, committed, and a record of another writer
			{From: d1.Start, To: newCut, Rows: []UsageGroup{{Requests: 1, Tokens: 20}}},
		}, newCut)
		for i, want := range []int64{107, 20, 107, 20} {
			if c := counted(t, m, ls[i]); c.N != want {
				t.Errorf("counter %d (%s from %s) counts %d after the fold, want %d", i, ls[i].Metric, ls[i].Period.Start, c.N, want)
			}
		}
		if got, ok := m.Cut(tn); !ok || !got.Equal(newCut) {
			t.Errorf("cut %v, want %v", got, newCut)
		}
	})

	t.Run("fold: a request that settles after its minute was dropped does not count twice", func(t *testing.T) {
		clk := newClock()
		m := newMemoryWith(t, clk, nil)
		tn := tid(1)
		day := utcCal.Of(PerDay, clk.now())
		m.SetCut(tn, day.Start)
		l := perLimit(tn, SubjectTenant, "", TokensPerDay, 1e9, day)
		res := mustReserve(t, m, periodReq("a", clk.now(), time.Hour, 10, pricing.Cost{}, l))
		_ = m.Settle(bg, res, Amounts{Tokens: 10})
		newCut := clk.now().Truncate(time.Minute).Add(time.Minute)
		m.Fold(tn, []Piece{{From: day.Start, To: newCut, Rows: []UsageGroup{{Requests: 1, Tokens: 10}}}}, newCut)
		if c := counted(t, m, l); c.N != 10 {
			t.Errorf("counted %d after the minute moved from memory to the ledger, want 10", c.N)
		}
	})

	t.Run("rebase replaces the base only", func(t *testing.T) {
		clk := newClock()
		m := newMemoryWith(t, clk, nil)
		tn := tid(1)
		cut := at(2026, 11, 17, 11, 50, 0)
		m.SetCut(tn, cut)
		mon := utcCal.Of(PerMonth, clk.now())
		l := perLimit(tn, SubjectTenant, "", TokensPerMonth, 1e9, mon)
		m.Load(tn, mon, []Limit{l}, []UsageGroup{{Requests: 5, Tokens: 100}})
		res := mustReserve(t, m, periodReq("a", at(2026, 11, 17, 11, 55, 0), time.Hour, 40, pricing.Cost{}, l))
		_ = m.Settle(bg, res, Amounts{Tokens: 40})
		m.Rebase(tn, mon, []UsageGroup{{Requests: 9, Tokens: 70}})
		if c := counted(t, m, l); c.N != 110 {
			t.Errorf("counted %d after the rebase, want 70 (base) + 40 (live)", c.N)
		}
		other := utcCal.Of(PerMonth, at(2026, 10, 3, 0, 0, 0))
		m.Rebase(tn, other, []UsageGroup{{Tokens: 5}}) // a period without counters: nothing happens
		if c := counted(t, m, l); c.N != 110 {
			t.Errorf("a rebase of another period changed the counter to %d", c.N)
		}
	})

	t.Run("the marks are tested after a fold and after a rebase", func(t *testing.T) {
		e := newPeriodsEnv(t)
		l := perLimit(e.tn, SubjectTenant, "", TokensPerDay, 1000, e.day)
		l.RuleID, l.Level = "r1", LevelTenant
		l.Marks = []Mark{{At: 500, Percent: 50, RuleID: "r1", Level: LevelTenant, Value: 1000}}
		mon := perLimit(e.tn, SubjectTenant, "", TokensPerMonth, 1000, e.mon)
		mon.RuleID, mon.Level = "r2", LevelTenant
		mon.Marks = []Mark{{At: 900, Percent: 90, RuleID: "r2", Level: LevelTenant, Value: 1000}}
		e.m.Load(e.tn, e.mon, []Limit{mon}, nil)
		mustReserve(t, e.m, periodReq("a", e.clk.now(), time.Hour, 1, pricing.Cost{}, l, mon))
		newCut := e.clk.now().Truncate(time.Minute) // the request above started in this minute: it stays in the live part
		e.m.Fold(e.tn, []Piece{{From: e.day.Start, To: newCut, Rows: []UsageGroup{{Requests: 3, Tokens: 600}}}}, newCut)
		if n := e.sink.count(EventThresholdReached, 50); n != 1 {
			t.Errorf("%d events at 50%% after the fold, want 1", n)
		}
		e.m.Rebase(e.tn, e.mon, []UsageGroup{{Requests: 9, Tokens: 950}})
		if n := e.sink.count(EventThresholdReached, 90); n != 1 {
			t.Errorf("%d events at 90%% after the rebase, want 1", n)
		}
	})

	t.Run("the reconciler: ensure loads base and seed from the ledger", func(t *testing.T) {
		e := newRecEnv(t)
		mon := utcCal.Of(PerMonth, e.clk.now())
		e.commit(at(2026, 11, 3, 9, 0, 0), "k1", 500, 0)   // before the live part
		e.commit(at(2026, 11, 17, 11, 50, 0), "k1", 50, 0) // in the live part
		e.commit(at(2026, 10, 30, 9, 0, 0), "k1", 7000, 0) // another month
		l := e.limit(TokensPerMonth, mon)
		if err := e.rec.Ensure(bg, e.tn, []Limit{l}); err != nil {
			t.Fatal(err)
		}
		if cut := e.cut(t); !cut.Equal(at(2026, 11, 17, 11, 47, 0)) {
			t.Errorf("cut %v, want 11:47 (now minus the horizon, to the minute)", cut)
		}
		if c := counted(t, e.m, l); c.N != 550 {
			t.Errorf("counted %d, want 550", c.N)
		}
		calls := e.led.callLog()
		if len(calls) != 1 || !calls[0].from.Equal(mon.Start) || !calls[0].liveFrom.Equal(at(2026, 11, 17, 11, 47, 0)) || calls[0].tenant != e.tn {
			t.Errorf("ledger calls %+v, want one from the month start with the live part from the cut", calls)
		}
		if err := e.rec.Ensure(bg, e.tn, []Limit{l}); err != nil || len(e.led.callLog()) != 1 {
			t.Errorf("a second ensure queried the ledger again: %v, %d calls", err, len(e.led.callLog()))
		}
	})

	t.Run("the reconciler: a restart just after the boundary loads the period that starts after the cut", func(t *testing.T) {
		e := newRecEnv(t)
		e.clk.t = at(2026, 11, 17, 0, 5, 0) // the cut is 23:52 of the day before: the day starts after it
		day := utcCal.Of(PerDay, e.clk.now())
		for i := range 10 {
			e.commit(at(2026, 11, 17, 0, 1, i), "k1", 5, 0)
		}
		l := e.limit(RequestsPerDay, day)
		l.Max = 10
		lm := e.limit(TokensPerMonth, utcCal.Of(PerMonth, e.clk.now()))
		if err := e.rec.Ensure(bg, e.tn, []Limit{l, lm}); err != nil {
			t.Fatal(err)
		}
		if cut := e.cut(t); !cut.Before(day.Start) {
			t.Fatalf("cut %v, want a cut before the day starts", cut)
		}
		if c := counted(t, e.m, l); c.N != 10 {
			t.Fatalf("the day counts %d after the restart, the ledger holds 10", c.N)
		}
		if c := counted(t, e.m, lm); c.N != 50 {
			t.Fatalf("the month counts %d, want 50", c.N)
		}
		mustRefuse(t, e.m, periodReq("after", e.clk.now(), time.Hour, 1, pricing.Cost{}, l))
		// A limit of the same tenant first needed later (another key): the process saw the tenant
		// after the period began, so its counter is loaded too, not created empty.
		e.commit(at(2026, 11, 17, 0, 2, 0), "k2", 7, 0)
		e.clk.advance(time.Minute)
		lk := perLimit(e.tn, SubjectKey, "k2", TokensPerDay, 1_000_000, day)
		if err := e.rec.Ensure(bg, e.tn, []Limit{lk}); err != nil {
			t.Fatal(err)
		}
		if c := counted(t, e.m, lk); c.N != 7 {
			t.Fatalf("the key counts %d, the ledger holds 7", c.N)
		}
		// The live rows are swapped, not added, by the fold.
		e.clk.advance(15 * time.Minute)
		e.rec.Tick(bg)
		if c := counted(t, e.m, l); c.N != 11 {
			t.Errorf("the day counts %d after the fold, want 11: the 10 loaded as live minutes swapped once, and the record of k2", c.N)
		}
	})

	t.Run("the reconciler: a tick forgets the locks and rebases of tenants the store dropped", func(t *testing.T) {
		e := newRecEnv(t)
		l := e.limit(TokensPerDay, utcCal.Of(PerDay, e.clk.now()))
		if err := e.rec.Ensure(bg, e.tn, []Limit{l}); err != nil {
			t.Fatal(err)
		}
		size := func() (locks, rebased int) {
			e.rec.locks.Range(func(any, any) bool { locks++; return true })
			e.rec.mu.Lock()
			defer e.rec.mu.Unlock()
			return locks, len(e.rec.rebased)
		}
		if lk, rb := size(); lk != 1 || rb != 1 {
			t.Fatalf("locks %d, rebased %d after a load, want 1 and 1", lk, rb)
		}
		e.clk.advance(26 * time.Hour)
		if e.m.Sweep(e.clk.now()); len(e.m.PeriodTenants()) != 0 {
			t.Fatalf("the sweep kept the tenant: %v", e.m.PeriodTenants())
		}
		e.rec.Tick(bg)
		if lk, rb := size(); lk != 0 || rb != 0 {
			t.Errorf("locks %d, rebased %d after a tick, want 0 and 0", lk, rb)
		}
	})

	t.Run("the reconciler: lock and forget never let two holders into one tenant", func(t *testing.T) {
		e := newRecEnv(t)
		var inside, bad atomic.Int32
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					unlock, err := e.rec.lock(bg, e.tn)
					if err != nil {
						return
					}
					if inside.Add(1) != 1 {
						bad.Add(1)
					}
					runtime.Gosched()
					inside.Add(-1)
					unlock()
				}
			}()
		}
		for range 20000 {
			e.rec.forget(nil)
		}
		close(stop)
		wg.Wait()
		if n := bad.Load(); n != 0 {
			t.Errorf("%d times two holders were inside the critical section of one tenant", n)
		}
	})

	t.Run("the reconciler: the cut stops at a pending record and never moves back", func(t *testing.T) {
		e := newRecEnv(t)
		mon := utcCal.Of(PerMonth, e.clk.now())
		l := e.limit(TokensPerMonth, mon)
		if err := e.rec.Ensure(bg, e.tn, []Limit{l}); err != nil {
			t.Fatal(err)
		}
		e.clk.advance(10 * time.Minute) // 12:10
		e.pending = at(2026, 11, 17, 11, 52, 0)
		e.rec.Tick(bg)
		if cut := e.cut(t); !cut.Equal(at(2026, 11, 17, 11, 52, 0)) {
			t.Errorf("cut %v, want 11:52: the oldest pending record", cut)
		}
		e.pending = at(2026, 11, 17, 11, 40, 0)
		e.clk.advance(time.Minute)
		e.rec.Tick(bg)
		if cut := e.cut(t); !cut.Equal(at(2026, 11, 17, 11, 52, 0)) {
			t.Errorf("cut %v moved back to a pending record older than the cut", cut)
		}
		e.pending = time.Time{}
		e.clk.advance(time.Minute) // 12:12
		e.rec.Tick(bg)
		if cut := e.cut(t); !cut.Equal(at(2026, 11, 17, 11, 59, 0)) {
			t.Errorf("cut %v, want 11:59 (12:12 minus 12m30s, to the minute)", cut)
		}
	})

	t.Run("the reconciler: a fold queries one piece per day and adds it to the right counters", func(t *testing.T) {
		e := newRecEnv(t)
		e.clk.t = at(2026, 11, 17, 0, 2, 0)
		day16 := utcCal.Of(PerDay, at(2026, 11, 16, 12, 0, 0))
		day17 := utcCal.Of(PerDay, e.clk.now())
		mon := utcCal.Of(PerMonth, e.clk.now())
		l16, l17, lm := e.limit(TokensPerDay, day16), e.limit(TokensPerDay, day17), e.limit(TokensPerMonth, mon)
		if err := e.rec.Ensure(bg, e.tn, []Limit{l16, lm}); err != nil {
			t.Fatal(err)
		}
		if cut := e.cut(t); !cut.Equal(at(2026, 11, 16, 23, 49, 0)) {
			t.Fatalf("cut %v, want 23:49", cut)
		}
		// Records of this period that the live part did not hold: committed after the load.
		e.commit(at(2026, 11, 16, 23, 55, 0), "k1", 11, 0)
		e.commit(at(2026, 11, 17, 0, 3, 0), "k1", 22, 0)
		e.m.Load(e.tn, day17, []Limit{l17}, nil)
		before := len(e.led.callLog())
		e.clk.advance(20 * time.Minute) // 00:22: the cut moves to 00:09
		e.rec.Tick(bg)
		calls := e.led.callLog()[before:]
		// The pieces are queried first; a rebase, when one is due, comes after them.
		if len(calls) < 2 ||
			!calls[0].from.Equal(at(2026, 11, 16, 23, 49, 0)) || !calls[0].to.Equal(day17.Start) || !calls[0].liveFrom.Equal(calls[0].to) ||
			!calls[1].from.Equal(day17.Start) || !calls[1].to.Equal(at(2026, 11, 17, 0, 9, 0)) {
			t.Fatalf("ledger calls %+v, want a piece up to midnight and a piece after it", calls)
		}
		for _, c := range []struct {
			l    Limit
			want int64
		}{{l16, 11}, {l17, 22}, {lm, 33}} {
			if got := counted(t, e.m, c.l).N; got != c.want {
				t.Errorf("%s of %s counts %d after the fold, want %d", c.l.Metric, c.l.Period.Start, got, c.want)
			}
		}
	})

	t.Run("the reconciler: a rebase after an hour, not before", func(t *testing.T) {
		e := newRecEnv(t)
		mon := utcCal.Of(PerMonth, e.clk.now())
		l := e.limit(TokensPerMonth, mon)
		if err := e.rec.Ensure(bg, e.tn, []Limit{l}); err != nil {
			t.Fatal(err)
		}
		isRebase := func(c ledgerCall) bool { return c.from.Equal(mon.Start) && c.liveFrom.Equal(c.to) }
		rebases := func() int {
			n := 0
			for _, c := range e.led.callLog() {
				if isRebase(c) && c.to.After(at(2026, 11, 17, 11, 47, 0)) {
					n++
				}
			}
			return n
		}
		e.clk.advance(30 * time.Minute)
		e.rec.Tick(bg)
		if n := rebases(); n != 0 {
			t.Errorf("%d rebase queries after 30 minutes", n)
		}
		// A record that never reached this instance, and one that the live part counted wrongly.
		e.commit(at(2026, 11, 5, 8, 0, 0), "k1", 123, 0)
		// The load counts as the first rebase of the tenant: the clock of the hour starts there.
		e.clk.advance(35 * time.Minute)
		e.rec.Tick(bg)
		if n := rebases(); n != 1 {
			t.Errorf("%d rebase queries after 65 minutes, want 1", n)
		}
		if c := counted(t, e.m, l); c.N != 123 {
			t.Errorf("counted %d after the rebase, want 123", c.N)
		}
	})
}

// TestReconcilerTwoZones is T-705: with the periods of two zones held at once, the pieces the
// reconciler folds are cut at every start and end of a period the store holds, so that every piece
// lies inside every counter it is added to; after a change of zone the counters of the old and of
// the new zone both equal the ledger over their ranges; the reconciler takes no calendar.
func TestReconcilerTwoZones(t *testing.T) {
	t.Parallel()
	clk := newClock()
	clk.t = at(2026, 11, 30, 15, 30, 30)
	model := &refModel{}
	m := newMemoryWith(t, clk, nil)
	led := &fakeLedger{model: model, clk: clk}
	rec, err := NewReconciler(ReconcilerOptions{Store: m, Ledger: led, Horizon: testHorizon, Interval: testInterval, Logger: quietLogger(), Now: clk.now})
	if err != nil {
		t.Fatalf("the reconciler takes no calendar (sprint 5.3): %v", err)
	}
	tn := tid(1)
	utc, tokyo := mustCalendar(t, "UTC"), mustCalendar(t, "Asia/Tokyo")
	// 2026-11-30T15:00Z is midnight of December 1st in Tokyo and 15:00 in UTC: the boundaries of the
	// two zones, a day and a month, fall inside the piece the reconciler is about to fold.
	cut := at(2026, 11, 30, 14, 30, 0)
	m.SetCut(tn, cut)
	probe := at(2026, 11, 30, 14, 45, 0)
	periods := map[string]Period{
		"utc day": utc.Of(PerDay, probe), "utc month": utc.Of(PerMonth, probe),
		"tokyo day (old)": tokyo.Of(PerDay, probe), "tokyo month (old)": tokyo.Of(PerMonth, probe),
		"tokyo day (new)": tokyo.Of(PerDay, at(2026, 11, 30, 15, 10, 0)), "tokyo month (new)": tokyo.Of(PerMonth, at(2026, 11, 30, 15, 10, 0)),
	}
	add := func(start time.Time, key string, tokens int64) {
		model.add(&refReq{tn: tn, sa: "sa1", key: key, start: start, end: refSettled, admitted: true, actTokens: tokens, commitAt: start, resolveAt: start})
	}
	add(at(2026, 11, 30, 3, 0, 0), "k1", 1)
	add(at(2026, 11, 30, 14, 31, 0), "k1", 10)
	add(at(2026, 11, 30, 14, 45, 0), "k1", 100)
	add(at(2026, 11, 30, 14, 59, 59), "k1", 1000)
	add(at(2026, 11, 30, 15, 0, 0), "k1", 10_000)
	add(at(2026, 11, 30, 15, 10, 0), "k1", 100_000)
	add(at(2026, 11, 30, 15, 17, 0), "k1", 1_000_000)
	add(at(2026, 11, 29, 16, 0, 0), "k1", 7) // Tokyo's November 30th, UTC's November 29th

	limitOf := func(name string) Limit {
		p := periods[name]
		metric := TokensPerDay
		if p.End.Sub(p.Start) > 32*time.Hour {
			metric = TokensPerMonth
		}
		return perLimit(tn, SubjectTenant, "", metric, 1e12, p)
	}
	held := map[string]Limit{}
	for name, p := range periods {
		l := limitOf(name)
		held[name] = l
		m.Load(tn, p, []Limit{l}, model.Ledger(clk.now(), tn, p.Start, cut, earlier(p.End, cut.Add(time.Hour))))
	}
	before := len(led.callLog())

	rec.Tick(context.Background())

	next, ok := m.Cut(tn)
	if !ok || !next.After(cut) {
		t.Fatalf("the cut did not move: %v %v", next, ok)
	}
	// Every piece lies inside every period the store holds.
	boundaries := map[time.Time]bool{}
	for _, p := range periods {
		boundaries[p.Start], boundaries[p.End] = true, true
	}
	pieces := 0
	for _, c := range led.callLog()[before:] {
		pieces++
		for x := range boundaries {
			if c.from.Before(x) && x.Before(c.to) {
				t.Errorf("the piece [%s, %s) straddles the boundary %s of a held period", c.from.Format("15:04:05"), c.to.Format("15:04:05"), x.Format("01-02 15:04:05"))
			}
		}
	}
	if pieces < 2 {
		t.Errorf("%d ledger reads for a range that holds a boundary: the range was not cut", pieces)
	}
	// Every counter equals the ledger over its range, up to the new cut.
	for name, l := range held {
		end := earlier(l.Period.End, next)
		want := Sum(l, model.Ledger(clk.now(), tn, l.Period.Start, end, end))
		got, ok := m.Counted(l)
		if !ok || got.N != want.N {
			t.Errorf("%s %v: counted %d (present %v), the ledger holds %d", name, l.Period, got.N, ok, want.N)
		}
	}
	// A year of this: the same boundaries a day later, with the clock moved on.
	clk.advance(25 * time.Hour)
	add(at(2026, 12, 1, 10, 0, 0), "k1", 5)
	rec.Tick(context.Background())
	if c2, _ := m.Cut(tn); !c2.After(next) {
		t.Errorf("the second tick left the cut at %s", c2)
	}
}

// TestReconcilerCounterCreatedMidFold: the cut points are read before the ledger queries, so a
// counter that a Reserve creates empty while the fold is reading, with its period starting strictly
// inside a piece, must not make the fold skip that piece and still move the cut past the start of
// the period (design 5.7: the counter receives the usage of its first minutes when the cut passes
// them). The fold is refused and read again with the new cut points.
func TestReconcilerCounterCreatedMidFold(t *testing.T) {
	t.Parallel()
	clk := newClock()
	clk.t = at(2026, 11, 30, 15, 20, 30)
	model := &refModel{}
	m := newMemoryWith(t, clk, nil)
	led := &fakeLedger{model: model, clk: clk}
	rec, err := NewReconciler(ReconcilerOptions{Store: m, Ledger: led, Horizon: testHorizon, Interval: testInterval, Logger: quietLogger(), Now: clk.now})
	if err != nil {
		t.Fatal(err)
	}
	tn := tid(1)
	utc, tokyo := mustCalendar(t, "UTC"), mustCalendar(t, "Asia/Tokyo")
	cut := at(2026, 11, 30, 14, 30, 0)
	m.SetCut(tn, cut)
	add := func(start time.Time, tokens int64) {
		model.add(&refReq{tn: tn, sa: "sa1", key: "k1", start: start, end: refSettled, admitted: true, actTokens: tokens, commitAt: start, resolveAt: start})
	}
	add(at(2026, 11, 30, 14, 40, 0), 10)
	add(at(2026, 11, 30, 15, 5, 0), 1000) // inside Tokyo's new day, which starts at 15:00Z
	uday := perLimit(tn, SubjectTenant, "", TokensPerDay, 1e12, utc.Of(PerDay, cut))
	m.Load(tn, uday.Period, []Limit{uday}, model.Ledger(clk.now(), tn, uday.Period.Start, cut, cut))
	tday := perLimit(tn, SubjectTenant, "", TokensPerDay, 1e12, tokyo.Of(PerDay, at(2026, 11, 30, 15, 10, 0)))
	if !tday.Period.Start.Equal(at(2026, 11, 30, 15, 0, 0)) {
		t.Fatalf("tokyo day starts %s", tday.Period.Start)
	}
	var once sync.Once
	led.onUsage = func(int) {
		// A request of the tenant in its Tokyo day arrives during the fold: the counter is created empty.
		once.Do(func() { m.Load(tn, tday.Period, []Limit{tday}, nil) })
	}
	rec.Tick(context.Background())
	next, _ := m.Cut(tn)
	if !next.After(at(2026, 11, 30, 15, 5, 0)) {
		t.Fatalf("the cut did not pass the usage: %s", next)
	}
	got, ok := m.Counted(tday)
	end := earlier(tday.Period.End, next)
	want := Sum(tday, model.Ledger(clk.now(), tn, tday.Period.Start, end, end))
	if !ok || got.N != want.N || want.N != 1000 {
		t.Errorf("tokyo day counted %d (present %v), the ledger holds %d, want 1000", got.N, ok, want.N)
	}
}
