package limits

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/tenant"
)

// utcCal is nil until the calendar exists: a test of the periods then fails on its own assertions.
var utcCal, _ = NewCalendar(time.UTC)

func at(y int, mo time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
}

// periodsEnv is a store with the tenant's cut at the start of 2026-11-17 and the clock at noon.
type periodsEnv struct {
	clk  *clock
	m    *Memory
	sink *recordingSink
	tn   tenant.ID
	day  Period
	mon  Period
}

func newPeriodsEnv(t *testing.T) *periodsEnv {
	t.Helper()
	clk := newClock() // 2026-11-17 12:00:00 UTC
	sink := &recordingSink{}
	e := &periodsEnv{clk: clk, sink: sink, tn: tid(1), m: newMemoryWith(t, clk, sink)}
	e.day, e.mon = utcCal.Of(PerDay, clk.now()), utcCal.Of(PerMonth, clk.now())
	e.m.SetCut(e.tn, e.day.Start)
	return e
}

// loadMonth makes the month counters of ls exist (the month started before the cut).
func (e *periodsEnv) loadMonth(ls ...Limit) { e.m.Load(e.tn, e.mon, ls, nil) }

// TestPeriodCounters is T-648.
func TestPeriodCounters(t *testing.T) {
	bg := context.Background()

	t.Run("reserve, settle below and above, release, expiry", func(t *testing.T) {
		e := newPeriodsEnv(t)
		reqs := perLimit(e.tn, SubjectTenant, "", RequestsPerDay, 5, e.day)
		toks := perLimit(e.tn, SubjectTenant, "", TokensPerDay, 1000, e.day)
		start := e.clk.now()
		res := mustReserve(t, e.m, periodReq("a", start, time.Minute, 400, pricing.Cost{}, reqs, toks))
		if c := counted(t, e.m, reqs); c.N != 1 {
			t.Errorf("requests counted %d after the reservation, want 1", c.N)
		}
		if c := counted(t, e.m, toks); c.N != 400 {
			t.Errorf("tokens counted %d after the reservation, want 400", c.N)
		}
		if err := e.m.Settle(bg, res, Amounts{Tokens: 250}); err != nil {
			t.Fatal(err)
		}
		if c := counted(t, e.m, toks); c.N != 250 {
			t.Errorf("tokens counted %d after a settlement below the reservation, want 250", c.N)
		}
		res = mustReserve(t, e.m, periodReq("b", start, time.Minute, 400, pricing.Cost{}, reqs, toks))
		if err := e.m.Settle(bg, res, Amounts{Tokens: 900}); err != nil {
			t.Fatal(err)
		}
		if c := counted(t, e.m, toks); c.N != 1150 {
			t.Errorf("tokens counted %d after a settlement above the reservation, want 1150", c.N)
		}
		if c := counted(t, e.m, reqs); c.N != 2 {
			t.Errorf("requests counted %d, want 2", c.N)
		}
		ref := mustRefuse(t, e.m, periodReq("c", start, time.Minute, 1, pricing.Cost{}, toks))
		if ref.Remaining != 0 {
			t.Errorf("remaining %d once the counter is over its limit, want 0", ref.Remaining)
		}
		res = mustReserve(t, e.m, periodReq("d", start, time.Minute, 0, pricing.Cost{}, reqs))
		if err := e.m.Release(bg, res); err != nil {
			t.Fatal(err)
		}
		if c := counted(t, e.m, reqs); c.N != 2 {
			t.Errorf("requests counted %d after a release, want 2", c.N)
		}
		// Expiry.
		mustReserve(t, e.m, periodReq("e", start, time.Minute, 0, pricing.Cost{}, reqs))
		if c := counted(t, e.m, reqs); c.N != 3 {
			t.Fatalf("requests counted %d, want 3", c.N)
		}
		e.clk.advance(2 * time.Minute)
		if n := e.m.Sweep(e.clk.now()); n != 1 {
			t.Fatalf("%d reservations expired, want 1", n)
		}
		if c := counted(t, e.m, reqs); c.N != 2 {
			t.Errorf("requests counted %d after an expiry, want 2", c.N)
		}
		if err := e.m.Settle(bg, res, Amounts{}); !errors.Is(err, ErrNotOutstanding) {
			t.Errorf("settling a released reservation: %v", err)
		}
	})

	t.Run("an admission after midnight counts in the period of the start", func(t *testing.T) {
		e := newPeriodsEnv(t)
		e.clk.advance(11*time.Hour + 59*time.Minute + 55*time.Second) // 23:59:55
		start := e.clk.now()
		next := utcCal.Of(PerDay, start.Add(time.Minute))
		l17 := perLimit(e.tn, SubjectTenant, "", TokensPerDay, 1000, e.day)
		l18 := perLimit(e.tn, SubjectTenant, "", TokensPerDay, 1000, next)
		res := mustReserve(t, e.m, periodReq("late", start, time.Hour, 100, pricing.Cost{}, l17))
		e.clk.advance(20 * time.Second) // admitted again after midnight: a new request of the new day
		mustReserve(t, e.m, periodReq("after", e.clk.now(), time.Hour, 7, pricing.Cost{}, l18))
		if err := e.m.Settle(bg, res, Amounts{Tokens: 80}); err != nil {
			t.Fatal(err)
		}
		if c := counted(t, e.m, l17); c.N != 80 {
			t.Errorf("the old day counts %d, want 80: the settlement belongs to the period of the start", c.N)
		}
		if c := counted(t, e.m, l18); c.N != 7 {
			t.Errorf("the new day counts %d, want 7", c.N)
		}
	})

	t.Run("a new period starts empty without a load", func(t *testing.T) {
		e := newPeriodsEnv(t)
		e.clk.advance(24 * time.Hour)
		next := utcCal.Of(PerDay, e.clk.now())
		l := perLimit(e.tn, SubjectTenant, "", RequestsPerDay, 3, next)
		if miss := e.m.Missing([]Limit{l}); len(miss) != 0 {
			t.Errorf("a period that starts after the cut is missing: %+v", miss)
		}
		mustReserve(t, e.m, periodReq("n", e.clk.now(), time.Hour, 0, pricing.Cost{}, l))
		if c := counted(t, e.m, l); c.N != 1 {
			t.Errorf("counted %d, want 1", c.N)
		}
	})

	t.Run("a refusal says how much is left and when the period ends", func(t *testing.T) {
		e := newPeriodsEnv(t)
		l := perLimit(e.tn, SubjectTenant, "", TokensPerDay, 100, e.day)
		res := mustReserve(t, e.m, periodReq("fill", e.clk.now(), time.Hour, 60, pricing.Cost{}, l))
		_ = e.m.Settle(bg, res, Amounts{Tokens: 60})
		ref := mustRefuse(t, e.m, periodReq("x", e.clk.now(), time.Hour, 50, pricing.Cost{}, l))
		if ref.Remaining != 40 || ref.OverLimit || !reflect.DeepEqual(ref.Limit, l) {
			t.Errorf("refusal %+v", ref)
		}
		if ref.RetryAfter != 12*time.Hour || ref.Reset != 12*time.Hour {
			t.Errorf("retry after %v, reset %v, want 12h (the end of the period)", ref.RetryAfter, ref.Reset)
		}
		// Rounded up to a second.
		e.clk.advance(400 * time.Millisecond)
		ref = mustRefuse(t, e.m, periodReq("y", e.clk.now(), time.Hour, 50, pricing.Cost{}, l))
		if ref.RetryAfter != 12*time.Hour {
			t.Errorf("retry after %v, want 12h (11h59m59.6s rounded up)", ref.RetryAfter)
		}
		// At least one second, however close the end.
		e.clk.advance(12*time.Hour - 1200*time.Millisecond) // 23:59:59.2
		ref = mustRefuse(t, e.m, periodReq("z", e.clk.now().Add(-time.Hour), time.Hour, 50, pricing.Cost{}, l))
		if ref.RetryAfter != time.Second || ref.Reset != time.Second {
			t.Errorf("retry after %v, reset %v 800 ms before the end, want 1s", ref.RetryAfter, ref.Reset)
		}
		// The estimate alone exceeds the limit.
		ref = mustRefuse(t, e.m, periodReq("o", e.clk.now().Add(-time.Hour), time.Hour, 101, pricing.Cost{}, l))
		if !ref.OverLimit {
			t.Errorf("an estimate above the limit is not OverLimit: %+v", ref)
		}
	})

	t.Run("the first limit without room is named and the longest retry wins", func(t *testing.T) {
		e := newPeriodsEnv(t)
		dayL := perLimit(e.tn, SubjectTenant, "", RequestsPerDay, 1, e.day)
		monL := perLimit(e.tn, SubjectTenant, "", RequestsPerMonth, 1, e.mon)
		e.loadMonth(monL)
		mustReserve(t, e.m, periodReq("one", e.clk.now(), time.Hour, 0, pricing.Cost{}, dayL, monL))
		ref := mustRefuse(t, e.m, periodReq("two", e.clk.now(), time.Hour, 0, pricing.Cost{}, dayL, monL))
		if !reflect.DeepEqual(ref.Limit, dayL) {
			t.Errorf("named %+v, want the day limit first", ref.Limit)
		}
		if want := e.mon.End.Sub(e.clk.now()); ref.RetryAfter != want {
			t.Errorf("retry after %v, want %v (the end of the month)", ref.RetryAfter, want)
		}
	})

	t.Run("a limit without a maximum is counted and never refuses", func(t *testing.T) {
		e := newPeriodsEnv(t)
		l := perLimit(e.tn, SubjectTenant, "", RequestsPerDay, 0, e.day)
		for i := range 50 {
			mustReserve(t, e.m, periodReq("u", e.clk.now(), time.Hour, 0, pricing.Cost{}, l))
			if c := counted(t, e.m, l); c.N != int64(i+1) {
				t.Fatalf("counted %d after %d requests", c.N, i+1)
			}
		}
	})

	t.Run("nothing is taken when any limit refuses", func(t *testing.T) {
		e := newPeriodsEnv(t)
		conc := lim(e.tn, SubjectTenant, "", ConcurrentRequests, 2)
		rpm := lim(e.tn, SubjectTenant, "", RequestsPerMinute, 10)
		rpd := perLimit(e.tn, SubjectTenant, "", RequestsPerDay, 1, e.day)
		tpd := perLimit(e.tn, SubjectTenant, "", TokensPerDay, 100, e.day)
		mustReserve(t, e.m, periodReq("one", e.clk.now(), time.Hour, 10, pricing.Cost{}, conc, rpm, rpd, tpd))
		before := headrooms(t, e.m, []Limit{conc, rpm})
		outBefore := e.m.Outstanding()
		cBefore, tBefore := counted(t, e.m, rpd), counted(t, e.m, tpd)
		mustRefuse(t, e.m, periodReq("two", e.clk.now(), time.Hour, 10, pricing.Cost{}, conc, rpm, rpd, tpd))
		if got := headrooms(t, e.m, []Limit{conc, rpm}); !equalInts(got, before) {
			t.Errorf("rate headroom %v, was %v: a refused request took something", got, before)
		}
		if e.m.Outstanding() != outBefore {
			t.Errorf("outstanding %d, was %d", e.m.Outstanding(), outBefore)
		}
		if counted(t, e.m, rpd) != cBefore || counted(t, e.m, tpd) != tBefore {
			t.Errorf("a period counter changed on a refusal")
		}
	})

	t.Run("a period that started before the cut must be loaded", func(t *testing.T) {
		e := newPeriodsEnv(t)
		l := perLimit(e.tn, SubjectTenant, "", RequestsPerMonth, 5, e.mon)
		if miss := e.m.Missing([]Limit{l}); len(miss) != 1 {
			t.Fatalf("missing %+v, want the month limit", miss)
		}
		_, ref, err := e.m.Reserve(bg, periodReq("nl", e.clk.now(), time.Hour, 0, pricing.Cost{}, l))
		if !errors.Is(err, ErrNotLoaded) || ref != nil {
			t.Fatalf("reserve: refusal %v, error %v, want ErrNotLoaded", ref, err)
		}
		if e.m.Outstanding() != 0 {
			t.Errorf("a failed reserve left %d reservations", e.m.Outstanding())
		}
		e.loadMonth(l)
		if miss := e.m.Missing([]Limit{l}); len(miss) != 0 {
			t.Errorf("missing %+v after the load", miss)
		}
		mustReserve(t, e.m, periodReq("ok", e.clk.now(), time.Hour, 0, pricing.Cost{}, l))
	})

	t.Run("a tenant without a cut is missing everything", func(t *testing.T) {
		clk := newClock()
		m, _ := newMemory(t, clk)
		l := perLimit(tid(5), SubjectTenant, "", RequestsPerDay, 5, utcCal.Of(PerDay, clk.now()))
		if _, ok := m.Cut(tid(5)); ok {
			t.Fatal("a new tenant has a cut")
		}
		if miss := m.Missing([]Limit{l}); len(miss) != 1 {
			t.Errorf("missing %+v", miss)
		}
		if _, _, err := m.Reserve(bg, periodReq("x", clk.now(), time.Hour, 0, pricing.Cost{}, l)); !errors.Is(err, ErrNotLoaded) {
			t.Errorf("reserve without a cut: %v", err)
		}
		m.SetCut(tid(5), at(2026, 11, 17, 0, 0, 0))
		m.SetCut(tid(5), at(2026, 11, 16, 0, 0, 0)) // only when the tenant has none
		if cut, ok := m.Cut(tid(5)); !ok || !cut.Equal(at(2026, 11, 17, 0, 0, 0)) {
			t.Errorf("cut %v %v, want the first one kept", cut, ok)
		}
	})

	t.Run("a start before the cut is an error", func(t *testing.T) {
		e := newPeriodsEnv(t)
		cut := e.clk.now().Truncate(time.Minute)
		e.m.Fold(e.tn, nil, cut)
		l := perLimit(e.tn, SubjectTenant, "", RequestsPerDay, 5, e.day)
		_, ref, err := e.m.Reserve(bg, periodReq("old", cut.Add(-time.Minute), time.Hour, 0, pricing.Cost{}, l))
		if err == nil || ref != nil {
			t.Fatalf("reserve: refusal %v, error %v, want an error", ref, err)
		}
		if e.m.Outstanding() != 0 {
			t.Errorf("%d reservations", e.m.Outstanding())
		}
		if c, ok := e.m.Counted(l); ok && c.N != 0 {
			t.Errorf("counted %d", c.N)
		}
	})

	t.Run("two tenants with the same IDs do not share a counter", func(t *testing.T) {
		e := newPeriodsEnv(t)
		other := tid(2)
		e.m.SetCut(other, e.day.Start)
		la := perLimit(e.tn, SubjectKey, "k1", RequestsPerDay, 2, e.day)
		lb := perLimit(other, SubjectKey, "k1", RequestsPerDay, 2, e.day)
		mustReserve(t, e.m, periodReq("a1", e.clk.now(), time.Hour, 0, pricing.Cost{}, la))
		mustReserve(t, e.m, periodReq("a2", e.clk.now(), time.Hour, 0, pricing.Cost{}, la))
		mustRefuse(t, e.m, periodReq("a3", e.clk.now(), time.Hour, 0, pricing.Cost{}, la))
		mustReserve(t, e.m, periodReq("b1", e.clk.now(), time.Hour, 0, pricing.Cost{}, lb))
		if c := counted(t, e.m, lb); c.N != 1 {
			t.Errorf("tenant B counts %d, want 1", c.N)
		}
		if c := counted(t, e.m, la); c.N != 2 {
			t.Errorf("tenant A counts %d, want 2", c.N)
		}
	})

	t.Run("the scope is part of the counter", func(t *testing.T) {
		e := newPeriodsEnv(t)
		all := perLimit(e.tn, SubjectTenant, "", RequestsPerDay, 10, e.day)
		prov := all
		prov.Provider = "openai"
		model := prov
		model.Model = "gpt"
		for range 3 {
			mustReserve(t, e.m, periodReq("s", e.clk.now(), time.Hour, 0, pricing.Cost{}, all, prov, model))
		}
		mustReserve(t, e.m, periodReq("p", e.clk.now(), time.Hour, 0, pricing.Cost{}, all, prov))
		if a, p, mo := counted(t, e.m, all).N, counted(t, e.m, prov).N, counted(t, e.m, model).N; a != 4 || p != 4 || mo != 3 {
			t.Errorf("counters all %d, provider %d, model %d, want 4, 4, 3", a, p, mo)
		}
	})
}

// TestBudgetCounters is T-649.
func TestBudgetCounters(t *testing.T) {
	bg := context.Background()

	t.Run("costs with eighteen fraction digits sum exactly", func(t *testing.T) {
		e := newPeriodsEnv(t)
		l := costLimit(e.tn, CostPerDay, "EUR", 10_000_000, e.day)
		for range 3 {
			res := mustReserve(t, e.m, periodReq("c", e.clk.now(), time.Hour, 0, known("EUR", dec(t, "0.100000000000000001")), l))
			if err := e.m.Settle(bg, res, Amounts{Cost: known("EUR", dec(t, "0.100000000000000001"))}); err != nil {
				t.Fatal(err)
			}
		}
		if got := counted(t, e.m, l).Cost.String(); got != "0.300000000000000003" {
			t.Errorf("counted %s, want 0.300000000000000003", got)
		}
	})

	t.Run("the maximum is in millionths", func(t *testing.T) {
		e := newPeriodsEnv(t)
		l := costLimit(e.tn, CostPerDay, "EUR", 1_000_000, e.day) // one euro
		half := known("EUR", dec(t, "0.5"))
		mustReserve(t, e.m, periodReq("a", e.clk.now(), time.Hour, 0, half, l))
		mustReserve(t, e.m, periodReq("b", e.clk.now(), time.Hour, 0, half, l))
		ref := mustRefuse(t, e.m, periodReq("c", e.clk.now(), time.Hour, 0, known("EUR", dec(t, "0.000001")), l))
		if ref.Remaining != 0 || ref.OverLimit {
			t.Errorf("refusal %+v", ref)
		}
		e2 := newPeriodsEnv(t)
		l2 := costLimit(e2.tn, CostPerDay, "EUR", 1_000_000, e2.day)
		ref = mustRefuse(t, e2.m, periodReq("d", e2.clk.now(), time.Hour, 0, known("EUR", dec(t, "1.000001")), l2))
		if !ref.OverLimit {
			t.Errorf("an estimate of 1.000001 against 1 is not OverLimit: %+v", ref)
		}
		mustReserve(t, e2.m, periodReq("e", e2.clk.now(), time.Hour, 0, known("EUR", dec(t, "1.000000")), l2))
	})

	t.Run("the remainder is rounded down to a millionth", func(t *testing.T) {
		for _, c := range []struct {
			spent string
			want  int64
		}{
			{"0.123456789012345678", 876_543},
			{"0.9999995", 0},
			{"0.999999", 1},
			{"1", 0},
		} {
			e := newPeriodsEnv(t)
			l := costLimit(e.tn, CostPerDay, "EUR", 1_000_000, e.day)
			res := mustReserve(t, e.m, periodReq("s", e.clk.now(), time.Hour, 0, known("EUR", dec(t, c.spent)), l))
			_ = e.m.Settle(bg, res, Amounts{Cost: known("EUR", dec(t, c.spent))})
			ref := mustRefuse(t, e.m, periodReq("r", e.clk.now(), time.Hour, 0, known("EUR", dec(t, "2")), l))
			if ref.Remaining != c.want {
				t.Errorf("spent %s: remaining %d, want %d", c.spent, ref.Remaining, c.want)
			}
		}
	})

	t.Run("a settled cost that is unknown or in another currency counts nothing", func(t *testing.T) {
		e := newPeriodsEnv(t)
		l := costLimit(e.tn, CostPerDay, "EUR", 10_000_000, e.day)
		for _, actual := range []pricing.Cost{
			{Known: false, Reason: "no_price"},
			known("USD", dec(t, "3")),
		} {
			res := mustReserve(t, e.m, periodReq("u", e.clk.now(), time.Hour, 0, known("EUR", dec(t, "0.5")), l))
			if c := counted(t, e.m, l).Cost.String(); c != "0.5" {
				t.Fatalf("reserved cost %s, want 0.5", c)
			}
			if err := e.m.Settle(bg, res, Amounts{Cost: actual}); err != nil {
				t.Fatal(err)
			}
			if c := counted(t, e.m, l).Cost.String(); c != "0" {
				t.Errorf("counted %s after a settlement with cost %+v, want 0", c, actual)
			}
		}
	})

	t.Run("counters of two currencies are apart", func(t *testing.T) {
		e := newPeriodsEnv(t)
		eur := costLimit(e.tn, CostPerDay, "EUR", 5_000_000, e.day)
		usd := costLimit(e.tn, CostPerDay, "USD", 5_000_000, e.day)
		mustReserve(t, e.m, periodReq("e", e.clk.now(), time.Hour, 0, known("EUR", dec(t, "4")), eur))
		mustReserve(t, e.m, periodReq("u", e.clk.now(), time.Hour, 0, known("USD", dec(t, "1")), usd))
		if c := counted(t, e.m, eur).Cost.String(); c != "4" {
			t.Errorf("EUR counts %s, want 4", c)
		}
		if c := counted(t, e.m, usd).Cost.String(); c != "1" {
			t.Errorf("USD counts %s, want 1", c)
		}
		mustRefuse(t, e.m, periodReq("e2", e.clk.now(), time.Hour, 0, known("EUR", dec(t, "2")), eur))
		mustReserve(t, e.m, periodReq("u2", e.clk.now(), time.Hour, 0, known("USD", dec(t, "4")), usd))
	})
}

func testMarkLimit(e *periodsEnv, max int64, marks ...Mark) Limit {
	l := perLimit(e.tn, SubjectTenant, "", TokensPerDay, max, e.day)
	l.RuleID, l.Level, l.Marks = "r-ten", LevelTenant, marks
	return l
}

func settleTokens(t *testing.T, e *periodsEnv, l Limit, reserve, actual int64) {
	t.Helper()
	res := mustReserve(t, e.m, periodReq("m", e.clk.now(), time.Hour, reserve, pricing.Cost{}, l))
	if err := e.m.Settle(context.Background(), res, Amounts{Tokens: actual}); err != nil {
		t.Fatal(err)
	}
}

// TestMarks is T-653.
func TestMarks(t *testing.T) {
	m80 := Mark{At: 80, Percent: 80, RuleID: "r-ten", Level: LevelTenant, Value: 100}

	t.Run("a threshold follows what was used, not what was reserved", func(t *testing.T) {
		e := newPeriodsEnv(t)
		l := testMarkLimit(e, 100, m80)
		res := mustReserve(t, e.m, periodReq("big", e.clk.now(), time.Hour, 90, pricing.Cost{}, l))
		if n := len(e.sink.taken()); n != 0 {
			t.Fatalf("%d events on a reservation of 90 against a mark at 80", n)
		}
		if err := e.m.Release(context.Background(), res); err != nil {
			t.Fatal(err)
		}
		settleTokens(t, e, l, 90, 10) // the estimate is returned at settlement: no alarm
		if n := len(e.sink.taken()); n != 0 {
			t.Fatalf("%d events after a settlement of 10", n)
		}
		settleTokens(t, e, l, 85, 85)
		evs := e.sink.taken()
		if len(evs) != 1 {
			t.Fatalf("%d events, want 1: %+v", len(evs), evs)
		}
		ev := evs[0]
		if ev.Kind != EventThresholdReached || ev.Percent != 80 || ev.Value != 100 || ev.RuleID != "r-ten" || ev.Level != LevelTenant ||
			ev.Metric != TokensPerDay || ev.Subject != SubjectTenant || ev.Tenant != e.tn || ev.Period != e.day || ev.Observed.String() != "95" {
			t.Errorf("event %+v", ev)
		}
	})

	t.Run("once per rule, percentage and period", func(t *testing.T) {
		e := newPeriodsEnv(t)
		l := testMarkLimit(e, 1000, Mark{At: 80, Percent: 8, RuleID: "r-ten", Level: LevelTenant, Value: 1000})
		for range 5 {
			settleTokens(t, e, l, 50, 50)
		}
		if n := e.sink.count(EventThresholdReached, 8); n != 1 {
			t.Errorf("%d events for one mark", n)
		}
		// The next period records again.
		e.clk.advance(24 * time.Hour)
		next := utcCal.Of(PerDay, e.clk.now())
		l2 := l
		l2.Period = next
		settleTokens(t, e, l2, 100, 100)
		if n := e.sink.count(EventThresholdReached, 8); n != 2 {
			t.Errorf("%d events after a new period, want 2", n)
		}
	})

	t.Run("a platform and a tenant rule on one counter", func(t *testing.T) {
		e := newPeriodsEnv(t)
		l := testMarkLimit(e, 100,
			Mark{At: 50, Percent: 50, RuleID: "r-ten", Level: LevelTenant, Value: 100},
			Mark{At: 80, Percent: 80, RuleID: "r-plat", Level: LevelPlatform, Value: 100})
		settleTokens(t, e, l, 60, 60)
		evs := e.sink.taken()
		if len(evs) != 1 || evs[0].Percent != 50 || evs[0].RuleID != "r-ten" || evs[0].Level != LevelTenant {
			t.Fatalf("events %+v, want the tenant's 50%%", evs)
		}
		settleTokens(t, e, l, 30, 30)
		evs = e.sink.taken()
		if len(evs) != 2 || evs[1].Percent != 80 || evs[1].RuleID != "r-plat" || evs[1].Level != LevelPlatform {
			t.Fatalf("events %+v, want the platform's 80%% second", evs)
		}
	})

	t.Run("an offer that was refused is repeated", func(t *testing.T) {
		e := newPeriodsEnv(t)
		l := testMarkLimit(e, 100, m80)
		e.sink.setRefuse(true)
		settleTokens(t, e, l, 90, 90)
		if len(e.sink.taken()) != 0 || e.sink.offers == 0 {
			t.Fatalf("taken %d, offers %d", len(e.sink.taken()), e.sink.offers)
		}
		e.sink.setRefuse(false)
		settleTokens(t, e, l, 1, 1)
		if n := e.sink.count(EventThresholdReached, 80); n != 1 {
			t.Errorf("%d events after the sink took offers again, want 1", n)
		}
		settleTokens(t, e, l, 1, 1)
		if n := e.sink.count(EventThresholdReached, 80); n != 1 {
			t.Errorf("%d events, want still 1", n)
		}
	})

	t.Run("exhausted is recorded at the first refusal only", func(t *testing.T) {
		e := newPeriodsEnv(t)
		l := testMarkLimit(e, 10)
		l.RuleID = "r-x"
		settleTokens(t, e, l, 10, 10)
		mustRefuse(t, e.m, periodReq("a", e.clk.now(), time.Hour, 1, pricing.Cost{}, l))
		evs := e.sink.taken()
		if len(evs) != 1 || evs[0].Kind != EventExhausted || evs[0].Percent != 0 || evs[0].RuleID != "r-x" || evs[0].Value != 10 || evs[0].Period != e.day {
			t.Fatalf("events %+v", evs)
		}
		mustRefuse(t, e.m, periodReq("b", e.clk.now(), time.Hour, 1, pricing.Cost{}, l))
		if n := len(e.sink.taken()); n != 1 {
			t.Errorf("%d events after a second refusal, want 1", n)
		}
	})

	t.Run("a counter loaded above the mark offers the event again", func(t *testing.T) {
		e := newPeriodsEnv(t)
		e.sink.setRefuse(false)
		fresh := newMemoryWith(t, e.clk, e.sink) // a restart: nothing remembered
		fresh.SetCut(e.tn, e.day.Start)
		l := testMarkLimit(e, 100, m80)
		fresh.Load(e.tn, e.day, []Limit{l}, []UsageGroup{{Requests: 3, Tokens: 85}})
		if n := e.sink.count(EventThresholdReached, 80); n != 1 {
			t.Fatalf("%d events after a load at 85 against a mark at 80", n)
		}
		quiet := &recordingSink{}
		below := newMemoryWith(t, e.clk, quiet)
		below.SetCut(e.tn, e.day.Start)
		below.Load(e.tn, e.day, []Limit{l}, []UsageGroup{{Requests: 3, Tokens: 50}})
		if len(quiet.taken()) != 0 {
			t.Errorf("events %+v for a load below the mark", quiet.taken())
		}
	})

	t.Run("a changed value moves the mark", func(t *testing.T) {
		e := newPeriodsEnv(t)
		l := testMarkLimit(e, 100, m80)
		settleTokens(t, e, l, 50, 50)
		lowered := testMarkLimit(e, 100, Mark{At: 32, Percent: 80, RuleID: "r-ten", Level: LevelTenant, Value: 40})
		settleTokens(t, e, lowered, 1, 1) // used is 51, above the new mark at 32
		evs := e.sink.taken()
		if len(evs) != 1 || evs[0].Value != 40 {
			t.Errorf("events %+v, want one for the lowered rule", evs)
		}
		e2 := newPeriodsEnv(t)
		raised := testMarkLimit(e2, 1000, Mark{At: 800, Percent: 80, RuleID: "r-ten", Level: LevelTenant, Value: 1000})
		settleTokens(t, e2, raised, 90, 90)
		if n := len(e2.sink.taken()); n != 0 {
			t.Errorf("%d events with the mark moved to 800", n)
		}
	})

	t.Run("an unenforced rule never refuses and records its thresholds", func(t *testing.T) {
		e := newPeriodsEnv(t)
		l := testMarkLimit(e, 0, Mark{At: 100, Percent: 100, RuleID: "r-soft", Level: LevelTenant, Value: 100})
		for range 3 {
			settleTokens(t, e, l, 60, 60)
		}
		if n := e.sink.count(EventThresholdReached, 100); n != 1 {
			t.Errorf("%d events at 100%%, want 1", n)
		}
		if n := e.sink.count(EventExhausted, 0); n != 0 {
			t.Errorf("%d exhausted events for an unenforced rule", n)
		}
	})

	t.Run("a nil sink marks nothing", func(t *testing.T) {
		clk := newClock()
		m := newMemoryWith(t, clk, nil)
		tn := tid(1)
		day := utcCal.Of(PerDay, clk.now())
		m.SetCut(tn, day.Start)
		l := perLimit(tn, SubjectTenant, "", TokensPerDay, 100, day)
		l.Marks = []Mark{{At: 10, Percent: 10, RuleID: "r", Level: LevelTenant, Value: 100}}
		res := mustReserve(t, m, periodReq("n", clk.now(), time.Hour, 50, pricing.Cost{}, l))
		if err := m.Settle(context.Background(), res, Amounts{Tokens: 50}); err != nil {
			t.Fatal(err)
		}
	})
}

// TestSweepPeriods is T-656.
func TestSweepPeriods(t *testing.T) {
	bg := context.Background()

	t.Run("a counter is dropped a horizon after its period ended", func(t *testing.T) {
		e := newPeriodsEnv(t)
		e.clk.advance(11*time.Hour + 50*time.Minute) // 23:50
		l := perLimit(e.tn, SubjectTenant, "", RequestsPerDay, 5, e.day)
		res := mustReserve(t, e.m, periodReq("a", e.clk.now(), time.Minute, 0, pricing.Cost{}, l))
		_ = e.m.Settle(bg, res, Amounts{})
		e.clk.advance(15 * time.Minute) // 00:05, five minutes after the end: inside the horizon
		e.m.Sweep(e.clk.now())
		if _, ok := e.m.Counted(l); !ok {
			t.Fatal("the counter was dropped five minutes after its period ended")
		}
		e.clk.advance(25 * time.Minute) // 00:30
		e.m.Sweep(e.clk.now())
		if _, ok := e.m.Counted(l); ok {
			t.Error("the counter is still there half an hour after its period ended")
		}
	})

	t.Run("an idle counter with nothing reserved is dropped after an hour", func(t *testing.T) {
		e := newPeriodsEnv(t)
		l := perLimit(e.tn, SubjectTenant, "", RequestsPerMonth, 100, e.mon)
		e.loadMonth(l)
		e.clk.advance(59 * time.Minute)
		e.m.Sweep(e.clk.now())
		if _, ok := e.m.Counted(l); !ok {
			t.Fatal("dropped after 59 minutes of idleness")
		}
		e.clk.advance(2 * time.Minute)
		e.m.Sweep(e.clk.now())
		if _, ok := e.m.Counted(l); ok {
			t.Error("still there after an idle hour")
		}
	})

	t.Run("a counter with a reservation outstanding is kept", func(t *testing.T) {
		e := newPeriodsEnv(t)
		l := perLimit(e.tn, SubjectTenant, "", RequestsPerMonth, 100, e.mon)
		e.loadMonth(l)
		mustReserve(t, e.m, periodReq("held", e.clk.now(), 5*time.Hour, 0, pricing.Cost{}, l))
		e.clk.advance(2 * time.Hour)
		e.m.Sweep(e.clk.now())
		if c, ok := e.m.Counted(l); !ok || c.N != 1 {
			t.Errorf("counter %v %v after two hours with a reservation outstanding", c, ok)
		}
	})

	t.Run("a tenant without counters loses its cut; a dropped counter must be loaded again", func(t *testing.T) {
		e := newPeriodsEnv(t)
		other := tid(2)
		e.m.SetCut(other, e.day.Start)
		l := perLimit(e.tn, SubjectTenant, "", RequestsPerMonth, 100, e.mon)
		lo := perLimit(other, SubjectTenant, "", RequestsPerMonth, 100, e.mon)
		e.loadMonth(l)
		e.m.Load(other, e.mon, []Limit{lo}, nil)
		if got := len(e.m.PeriodTenants()); got != 2 {
			t.Fatalf("%d tenants with a cut, want 2", got)
		}
		e.clk.advance(40 * time.Minute)
		mustReserve(t, e.m, periodReq("keep", e.clk.now(), time.Minute, 0, pricing.Cost{}, lo)) // touches the other tenant only
		e.clk.advance(30 * time.Minute)
		e.m.Sweep(e.clk.now())
		if _, ok := e.m.Cut(e.tn); ok {
			t.Error("a tenant without period counters kept its cut")
		}
		if _, ok := e.m.Cut(other); !ok {
			t.Error("a tenant with a period counter lost its cut")
		}
		if ps := e.m.Periods(other); len(ps) != 1 || ps[0] != e.mon {
			t.Errorf("periods of the other tenant %v", ps)
		}
		if _, _, err := e.m.Reserve(bg, periodReq("again", e.clk.now(), time.Minute, 0, pricing.Cost{}, l)); !errors.Is(err, ErrNotLoaded) {
			t.Errorf("reserve on a dropped counter: %v, want ErrNotLoaded", err)
		}
	})
}
