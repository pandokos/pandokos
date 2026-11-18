package limits

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/tenant"
)

const (
	testHorizon  = 12*time.Minute + 30*time.Second
	testInterval = 5 * time.Minute
	propTTL      = 10 * time.Minute // shorter than the horizon, as the reservation lifetime of 5.1 is
)

// ruleSpec is a rule of a property schedule; it exists from step activeFrom on.
type ruleSpec struct {
	id              string
	subject         SubjectKind
	sid             string
	provider, model string
	metric          Metric
	max             int64
	currency        string
	activeFrom      int
}

func (r ruleSpec) limit(tn tenant.ID, cal *Calendar, start time.Time) Limit {
	k, _ := r.metric.Period()
	return Limit{Tenant: tn, Subject: r.subject, ID: r.sid, ProviderID: r.provider, ModelID: r.model, Metric: r.metric,
		Currency: r.currency, Max: r.max, Level: LevelTenant, RuleID: r.id, Period: cal.Of(k, start)}
}

func (r ruleSpec) applies(q *refReq) bool {
	switch r.subject {
	case SubjectServiceAccount:
		if r.sid != q.sa {
			return false
		}
	case SubjectKey:
		if r.sid != q.key {
			return false
		}
	}
	return (r.provider == "" || r.provider == q.provider) && (r.model == "" || r.model == q.model)
}

// prop is one random schedule against the store (and, with rec, the reconciler) and the reference.
type prop struct {
	t          *testing.T
	name       string
	rng        *rand.Rand
	clk        *clock
	cal        *Calendar
	m          *Memory
	model      *refModel
	led        *fakeLedger
	rec        *Reconciler
	tn         tenant.ID
	specs      []ruleSpec
	step       int
	allowAbove bool
	pend, open []*refReq
	seen       map[string]Limit
	trace      []string
	lastTick   time.Time
	admitting  *refReq   // the request being admitted: it left pend but its start bounds the cut
	boundary   time.Time // restart schedules: the cut the by-hand ensure uses, before the period starts
	laggy      bool      // records are committed after a delay, may be lost, others write too
}

func newProp(t *testing.T, seed uint64, n int, withReconciler bool) *prop {
	t.Helper()
	rng := rngOf(seed, n)
	zone := []string{"Europe/Rome", "UTC", "America/New_York", "Australia/Lord_Howe"}[rng.IntN(4)]
	cal := mustCalendar(t, zone)
	bases := []time.Time{
		at(2026, 1, 31, 20, 0, 0), at(2026, 2, 28, 22, 0, 0), at(2026, 3, 28, 22, 0, 0), at(2026, 10, 24, 22, 0, 0),
		at(2026, 12, 31, 22, 30, 0), at(2028, 2, 28, 23, 0, 0), at(2026, 6, 15, 10, 0, 0),
	}
	clk := newClock()
	clk.t = bases[rng.IntN(len(bases))].Add(time.Duration(rng.IntN(7200)) * time.Second)
	p := &prop{t: t, rng: rng, clk: clk, cal: cal, tn: tid(1), model: &refModel{}, seen: map[string]Limit{},
		name: fmt.Sprintf("seed %d schedule %d (%s)", seed, n, zone), allowAbove: rng.IntN(2) == 0, laggy: withReconciler}
	p.m, _ = newMemory(t, clk)
	p.led = &fakeLedger{model: p.model, clk: clk}
	v := func(lo, hi int) int64 { return int64(lo + rng.IntN(hi-lo+1)) }
	p.specs = []ruleSpec{
		{id: "r1", subject: SubjectTenant, metric: RequestsPerDay, max: v(3, 15)},
		{id: "r2", subject: SubjectTenant, metric: TokensPerMonth, max: v(800, 5000)},
		{id: "r3", subject: SubjectTenant, metric: CostPerDay, currency: refCurrency, max: v(200_000, 1_500_000)},
		{id: "r4", subject: SubjectKey, sid: "k1", provider: "p1", metric: TokensPerDay, max: v(200, 1500)},
		{id: "r5", subject: SubjectServiceAccount, sid: "s1", metric: RequestsPerMonth, max: v(10, 40)},
		{id: "r6", subject: SubjectTenant, provider: "p2", metric: CostPerMonth, currency: refCurrency, max: v(500_000, 4_000_000)},
	}
	if withReconciler {
		p.specs[3].activeFrom, p.specs[5].activeFrom = 10+rng.IntN(20), 20+rng.IntN(20) // created mid-way
		var err error
		p.rec, err = NewReconciler(ReconcilerOptions{
			Store: p.m, Ledger: p.led, Pending: p.pending, Horizon: testHorizon, Interval: testInterval,
			Logger: quietLogger(), Now: clk.now,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	p.lastTick = clk.now()
	return p
}

// restartAfterBoundary makes the schedule start the way a restart just after local midnight or the
// 1st of the month does: the clock is a few minutes after the boundary (inside the horizon) and the
// ledger holds the records of the period that began before the process started. The store knows
// none of them, so every counter must be loaded from the ledger, not created empty.
func (p *prop) restartAfterBoundary(n int) {
	rng := rngOf(p.rng.Uint64(), n+1000)
	kind := PerDay
	if rng.IntN(3) == 0 {
		kind = PerMonth
	}
	per := p.cal.Of(kind, p.clk.now())
	now := per.Start.Add(time.Duration(1+rng.IntN(12)) * time.Minute)
	p.clk.t = now
	p.lastTick = now
	p.boundary = per.Start.Add(-time.Minute)
	for range 6 + rng.IntN(12) {
		start := per.Start.Add(time.Duration(rng.Int64N(int64(now.Sub(per.Start)))))
		tokens := int64(1 + rng.IntN(300))
		p.model.add(&refReq{tn: p.tn, sa: []string{"s1", "s2"}[rng.IntN(2)], key: []string{"k1", "k2"}[rng.IntN(2)],
			provider: []string{"p1", "p2"}[rng.IntN(2)], model: []string{"m1", "m2"}[rng.IntN(2)], start: start, end: refSettled,
			admitted: true, actTokens: tokens, actCost: int64(1 + rng.IntN(400_000)), commitAt: start, resolveAt: start})
	}
	p.logf("restart %s after the start of the %s", now.Sub(per.Start), per.Start.Format(time.RFC3339))
}

func (p *prop) logf(format string, args ...any) {
	p.trace = append(p.trace, fmt.Sprintf("%s  %s", p.clk.now().UTC().Format("01-02 15:04:05"), fmt.Sprintf(format, args...)))
}

func (p *prop) fail(format string, args ...any) {
	p.t.Helper()
	tail := p.trace
	if len(tail) > 60 {
		tail = tail[len(tail)-60:]
	}
	p.t.Fatalf("%s step %d\n  failure: %s\n  last steps:\n    %s", p.name, p.step, fmt.Sprintf(format, args...), strings.Join(tail, "\n    "))
}

// pending is the oldest record handed to the writer and not yet resolved.
func (p *prop) pending() (time.Time, bool) {
	now := p.clk.now()
	var oldest time.Time
	found := false
	p.model.mu.Lock()
	defer p.model.mu.Unlock()
	for _, q := range p.model.reqs {
		if q.end == refSettled && !q.other && now.Before(q.resolveAt) && (!found || q.start.Before(oldest)) {
			oldest, found = q.start, true
		}
	}
	return oldest, found
}

func (p *prop) limitsFor(q *refReq) []Limit {
	var out []Limit
	for _, r := range p.specs {
		if p.step >= r.activeFrom && r.applies(q) {
			out = append(out, r.limit(p.tn, p.cal, q.start))
		}
	}
	return out
}

func amountFor(l Limit, q *refReq) (n int64, cost int64) {
	switch {
	case l.Metric.Cost():
		return 0, q.estCost
	case l.Metric == TokensPerDay || l.Metric == TokensPerMonth:
		return q.estTokens, 0
	}
	return 1, 0
}

// ensure makes the counters of ls exist: the reconciler, or by hand with the same ledger.
func (p *prop) ensure(ls []Limit) error {
	if p.rec != nil {
		return p.rec.Ensure(context.Background(), p.tn, ls)
	}
	miss := p.m.NeedsLoad(ls)
	if len(miss) == 0 {
		return nil
	}
	now := p.clk.now()
	if _, ok := p.m.Cut(p.tn); !ok {
		oldest := now
		for _, q := range append(append([]*refReq{p.admitting}, p.pend...), p.open...) {
			if q == nil {
				continue
			}
			if q.start.Before(oldest) {
				oldest = q.start
			}
		}
		if !p.boundary.IsZero() {
			oldest = p.boundary
		}
		p.m.SetCut(p.tn, oldest.Truncate(time.Minute))
	}
	cut, _ := p.m.Cut(p.tn)
	byPeriod := map[time.Time][]Limit{}
	periods := map[time.Time]Period{}
	for _, l := range miss {
		byPeriod[l.Period.Start] = append(byPeriod[l.Period.Start], l)
		periods[l.Period.Start] = l.Period
	}
	for s, ls := range byPeriod {
		rows, err := p.led.Usage(context.Background(), p.tn, s, cut, now.Add(time.Second))
		if err != nil {
			return err
		}
		p.m.Load(p.tn, periods[s], ls, rows)
	}
	return nil
}

func (p *prop) startReq() {
	q := &refReq{tn: p.tn, start: p.clk.now(),
		sa: []string{"s1", "s2"}[p.rng.IntN(2)], key: []string{"k1", "k2"}[p.rng.IntN(2)],
		provider: []string{"p1", "p2"}[p.rng.IntN(2)], model: []string{"m1", "m2"}[p.rng.IntN(2)],
		estTokens: int64(1 + p.rng.IntN(300)), estCost: int64(1 + p.rng.IntN(400_000))}
	p.model.add(q)
	p.pend = append(p.pend, q)
	p.logf("start #%d %s/%s k=%s s=%s est %d tokens %d micro", q.id, q.provider, q.model, q.key, q.sa, q.estTokens, q.estCost)
}

// predictRefusal is the decision of the reference: the first limit without room, in order.
func (p *prop) predictRefusal(q *refReq, ls []Limit) (Limit, bool) {
	for _, l := range ls {
		if l.Max == 0 {
			continue
		}
		cur := p.model.sum(l, func(*refReq) bool { return true })
		n, cost := amountFor(l, q)
		if l.Metric.Cost() {
			if cur.Cost.Add(micro(cost)).Cmp(micro(l.Max)) > 0 {
				return l, true
			}
		} else if cur.N+n > l.Max {
			return l, true
		}
	}
	return Limit{}, false
}

func (p *prop) admit() {
	if len(p.pend) == 0 {
		return
	}
	i := p.rng.IntN(len(p.pend))
	q := p.pend[i]
	p.admitting = q
	p.pend = append(p.pend[:i], p.pend[i+1:]...)
	if p.clk.now().Sub(q.start) > 60*time.Second {
		q.end = refReleased
		p.logf("#%d dropped: pending for more than a minute", q.id)
		return
	}
	ls := p.limitsFor(q)
	if err := p.ensure(ls); err != nil {
		q.end = refReleased
		p.logf("#%d not admitted: ensure: %v", q.id, err)
		return
	}
	want, refuse := Limit{}, false
	if !p.laggy {
		want, refuse = p.predictRefusal(q, ls)
	}
	res, ref, err := p.m.Reserve(context.Background(), Request{ID: fmt.Sprintf("q%d", q.id), Limits: ls, Start: q.start,
		Estimate: Amounts{Tokens: q.estTokens, Cost: known(refCurrency, micro(q.estCost))}, TTL: propTTL})
	if err != nil {
		p.fail("reserve #%d: %v", q.id, err)
	}
	if !p.laggy && (ref != nil) != refuse {
		p.fail("#%d: the store refused = %v, the reference says %v (limit %+v)", q.id, ref != nil, refuse, want)
	}
	if ref != nil {
		if !p.laggy && ref.Limit.Metric != want.Metric {
			p.fail("#%d refused by %s, the reference names %s", q.id, ref.Limit.Metric, want.Metric)
		}
		q.end = refReleased
		p.logf("#%d refused by %s", q.id, ref.Limit.Metric)
		return
	}
	q.admitted, q.admit, q.admitSeq, q.res, q.limits = true, p.clk.now(), p.model.tick(), res, ls
	p.open = append(p.open, q)
	for _, l := range ls {
		p.seen[fmt.Sprintf("%s/%s/%s/%s/%s/%s/%d", l.Subject, l.ID, l.ProviderID, l.ModelID, l.Metric, l.Currency, l.Period.Start.Unix())] = l
	}
	p.logf("#%d admitted with %d limits", q.id, len(ls))
}

func (p *prop) endReq() {
	if len(p.open) == 0 {
		return
	}
	i := p.rng.IntN(len(p.open))
	q := p.open[i]
	p.open = append(p.open[:i], p.open[i+1:]...)
	p.endOne(q, p.rng.IntN(4) == 0)
}

func (p *prop) endOne(q *refReq, release bool) {
	now := p.clk.now()
	q.endAt, q.endSeq = now, p.model.tick()
	if release {
		if err := p.m.Release(context.Background(), q.res); err != nil {
			p.fail("release #%d: %v", q.id, err)
		}
		q.end = refReleased
		p.logf("#%d released", q.id)
		return
	}
	hi := q.estTokens + 1
	hc := q.estCost + 1
	if p.allowAbove {
		hi, hc = 2*q.estTokens+1, 2*q.estCost+1
	}
	q.actTokens, q.actCost = p.rng.Int64N(hi), p.rng.Int64N(hc)
	if p.rng.IntN(10) == 0 {
		q.actCost = -1
	}
	cost := pricing.Cost{}
	if q.actCost >= 0 {
		cost = known(refCurrency, micro(q.actCost))
	}
	if err := p.m.Settle(context.Background(), q.res, Amounts{Tokens: q.actTokens, Cost: cost}); err != nil {
		p.fail("settle #%d: %v", q.id, err)
	}
	q.end = refSettled
	if p.laggy {
		delay := time.Duration(p.rng.IntN(60)) * time.Second
		q.resolveAt = now.Add(delay)
		if p.rng.IntN(12) == 0 {
			q.lost = true // rejected by the database, or lost: it leaves the queue and never reaches the ledger
		} else {
			q.commitAt = now.Add(delay)
		}
	} else {
		q.commitAt, q.resolveAt = now, now
	}
	p.logf("#%d settled with %d tokens, %d micro", q.id, q.actTokens, q.actCost)
}

func (p *prop) other() {
	q := &refReq{tn: p.tn, other: true, end: refSettled, start: p.clk.now().Add(-time.Duration(p.rng.IntN(30)) * time.Second),
		sa: []string{"s1", "s2"}[p.rng.IntN(2)], key: []string{"k1", "k2"}[p.rng.IntN(2)],
		provider: []string{"p1", "p2"}[p.rng.IntN(2)], model: []string{"m1", "m2"}[p.rng.IntN(2)],
		actTokens: int64(1 + p.rng.IntN(300)), actCost: int64(1 + p.rng.IntN(300_000))}
	q.commitAt = p.clk.now().Add(time.Duration(p.rng.IntN(60)) * time.Second)
	q.resolveAt = q.commitAt
	p.model.add(q)
	p.logf("another writer: #%d started %s, commits %s", q.id, q.start.UTC().Format("15:04:05"), q.commitAt.UTC().Format("15:04:05"))
}

// advance moves the clock, in chunks, sweeping after each and ticking the reconciler as Run does.
func (p *prop) advance(d time.Duration) {
	chunk := 6 * time.Hour
	if p.rec != nil {
		chunk = testInterval
	}
	for d > 0 {
		c := min(d, chunk)
		d -= c
		p.clk.advance(c)
		now := p.clk.now()
		expired := 0
		for i := 0; i < len(p.open); {
			q := p.open[i]
			if !now.Before(q.res.Expires) {
				q.end, q.endAt, q.endSeq = refExpired, now, p.model.tick()
				p.open = append(p.open[:i], p.open[i+1:]...)
				expired++
				continue
			}
			i++
		}
		if n := p.m.Sweep(now); n != expired {
			p.fail("sweep expired %d reservations, the reference expects %d", n, expired)
		}
		if p.rec != nil && now.Sub(p.lastTick) >= testInterval {
			p.rec.Tick(context.Background())
			p.lastTick = now
		}
		if p.rec != nil {
			p.check()
		}
	}
	p.logf("advanced")
}

func (p *prop) randomAdvance() {
	switch r := p.rng.IntN(100); {
	case r < 70:
		p.advance(time.Duration(1+p.rng.IntN(120)) * time.Second)
	case r < 95:
		p.advance(time.Duration(1+p.rng.IntN(30)) * time.Minute)
	default:
		p.advance(time.Duration(1+p.rng.IntN(40)) * time.Hour) // across midnights and month ends
	}
}

// reserved and uncommitted are the parts of what a counter may hold above the ledger.
func (p *prop) check() {
	now := p.clk.now()
	for key, l := range p.seen {
		c, ok := p.m.Counted(l)
		if !ok {
			continue
		}
		if !p.laggy {
			want := p.model.sum(l, func(*refReq) bool { return true })
			if c.N != want.N || c.Cost.String() != want.Cost.String() {
				p.fail("counter %s: counted %d / %s, the reference holds %d / %s", key, c.N, c.Cost, want.N, want.Cost)
			}
			continue
		}
		led := Sum(l, p.model.Ledger(now, l.Tenant, l.Period.Start, l.Period.End, l.Period.End))
		extra := p.model.sum(l, func(q *refReq) bool { return q.end == refOpen || (q.end == refSettled && !q.inLedger(now)) })
		if c.N > led.N+extra.N || c.Cost.Cmp(led.Cost.Add(extra.Cost)) > 0 {
			p.fail("counter %s: counted %d / %s above the ledger %d / %s plus what is not committed %d / %s: something is counted twice",
				key, c.N, c.Cost, led.N, led.Cost, extra.N, extra.Cost)
		}
	}
}

func (p *prop) run(ops int) {
	for p.step = 0; p.step < ops; p.step++ {
		switch r := p.rng.IntN(100); {
		case r < 25:
			p.startReq()
		case r < 50:
			p.admit()
		case r < 72:
			p.endReq()
		case r < 90:
			p.randomAdvance()
		case r < 94 && p.laggy:
			p.other()
		case r < 97 && p.rec != nil:
			p.rec.Tick(context.Background())
		case r < 99 && p.laggy:
			p.led.setErr(fmt.Errorf("ledger down"))
			p.logf("ledger down")
		default:
			p.led.setErr(nil)
		}
		p.check()
	}
	// The end: everything outstanding ends.
	p.led.setErr(nil)
	for len(p.open) > 0 {
		q := p.open[0]
		p.open = p.open[1:]
		p.endOne(q, p.rng.IntN(3) == 0)
	}
	p.check()
}

// TestPropertyPeriods is T-650.
func TestPropertyPeriods(t *testing.T) {
	seed := seedOf(t)
	total := 0
	var last *prop
	defer func() {
		if total == 0 && last != nil && !t.Failed() {
			last.fail("no counter was used in any schedule")
		}
	}()
	for n := range max(1, schedules()/10) {
		p := newProp(t, seed, n, false)
		last = p
		if n%2 == 1 {
			p.restartAfterBoundary(n)
		}
		p.run(80)
		if got := p.m.Outstanding(); got != 0 {
			p.fail("%d reservations outstanding after everything ended", got)
		}
		checked := 0
		for key, l := range p.seen {
			spent, bound, excess := p.model.Bound(l)
			switch {
			case spent > bound:
				p.fail("counter %s: spent %d above the bound %d (limit %d, excess %d)", key, spent, bound, l.Max, excess)
			case !p.allowAbove && spent > l.Max:
				p.fail("counter %s: spent %d above the limit %d although nothing exceeded its reservation", key, spent, l.Max)
			}
			// Nothing is reserved any more: the counter holds what was settled, and the ledger agrees.
			if c, ok := p.m.Counted(l); ok {
				want := p.model.sum(l, func(*refReq) bool { return true })
				if c.N != want.N || c.Cost.String() != want.Cost.String() {
					p.fail("counter %s ends with %d / %s, settled %d / %s: a reservation is still counted", key, c.N, c.Cost, want.N, want.Cost)
				}
			}
			checked++
		}
		total += checked // a schedule can end without an admission; some must have had one
	}
}

// TestPropertyReconcile is T-652.
func TestPropertyReconcile(t *testing.T) {
	seed := seedOf(t)
	totalCompared := 0
	var last *prop
	defer func() {
		if totalCompared == 0 && last != nil && !t.Failed() {
			last.fail("no counter was left to compare in any schedule")
		}
	}()
	for n := range max(2, schedules()/20) { // the odd ones restart just after a boundary
		p := newProp(t, seed, n, true)
		last = p
		if n%2 == 1 {
			p.restartAfterBoundary(n)
		}
		p.run(90)
		// H + the interval after the last request, and one more tick: every counter is the ledger.
		p.advance(testHorizon + testInterval + 2*time.Minute)
		p.rec.Tick(context.Background())
		compared := 0
		for key, l := range p.seen {
			c, ok := p.m.Counted(l)
			if !ok {
				continue
			}
			want := Sum(l, p.model.Ledger(p.clk.now(), l.Tenant, l.Period.Start, l.Period.End, l.Period.End))
			if c.N != want.N || c.Cost.String() != want.Cost.String() {
				p.fail("counter %s: counted %d / %s, the ledger holds %d / %s: something was lost or counted twice", key, c.N, c.Cost, want.N, want.Cost)
			}
			compared++
		}
		totalCompared += compared
		if got := p.m.Outstanding(); got != 0 {
			p.fail("%d reservations outstanding", got)
		}
	}
}
