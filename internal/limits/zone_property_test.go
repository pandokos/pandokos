package limits

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/tenant"
)

// zoneRow is the three columns of the tenant row the calendar is built from.
type zoneRow struct {
	cur, prev string
	from      time.Time // zero: no pending change
}

// zoneProp is one random schedule of requests and zone changes against the reference of sprint 5.2
// (the store, the reconciler, the fake ledger). The reference counts, for every limit, the whole
// range of the civil period of the zone in force at the start of the request.
type zoneProp struct {
	t     *testing.T
	name  string
	seed  uint64
	rng   func(n int) int
	clk   *clock
	m     *Memory
	model *refModel
	rec   *Reconciler
	tn    tenant.ID
	cals  map[string]*Calendar
	row   zoneRow
	seen  map[string]Limit
	trace []string
}

func (p *zoneProp) logf(format string, args ...any) {
	p.trace = append(p.trace, fmt.Sprintf("%s  %s", p.clk.now().UTC().Format("01-02 15:04:05"), fmt.Sprintf(format, args...)))
}

func (p *zoneProp) fail(format string, args ...any) {
	p.t.Helper()
	tail := p.trace
	if len(tail) > 40 {
		tail = tail[len(tail)-40:]
	}
	p.t.Fatalf("%s\n  failure: %s\n  last steps:\n    %s", p.name, fmt.Sprintf(format, args...), strings.Join(tail, "\n    "))
}

// inForce is the zone in force at t, by the definition of section 5.7.
func (p *zoneProp) inForce(t time.Time) string {
	if !p.row.from.IsZero() && t.Before(p.row.from) {
		return p.row.prev
	}
	return p.row.cur
}

func (p *zoneProp) calendar() TenantCalendar {
	c := TenantCalendar{Current: p.cals[p.row.cur]}
	if !p.row.from.IsZero() {
		c.Previous, c.From = p.cals[p.row.prev], p.row.from
	}
	return c
}

// setZone is the change rule of section 5.7 as the administration API applies it (plan C6).
func (p *zoneProp) setZone(name string) {
	now := p.clk.now()
	inForce := p.inForce(now)
	pending := !p.row.from.IsZero() && now.Before(p.row.from)
	switch {
	case name == p.row.cur:
		p.logf("zone %s: already set", name)
	case name == inForce:
		p.row = zoneRow{cur: inForce}
		p.logf("zone %s: the pending change is withdrawn", name)
	case pending:
		p.row.cur = name
		p.logf("zone %s replaces the pending one; the instant %s stays", name, p.row.from.Format(time.RFC3339))
	default:
		e := NextChange(p.cals[inForce], now)
		if want := p.cals[inForce].Of(PerMonth, now).End; !e.Equal(want) {
			p.fail("NextChange(%s, %s) = %s, want the end of the month in force, %s", inForce, now, e, want)
		}
		p.row = zoneRow{cur: name, prev: inForce, from: e}
		p.logf("zone %s takes effect at %s (in force: %s)", name, e.Format(time.RFC3339), inForce)
	}
}

type zoneSpec struct {
	id      string
	subject SubjectKind
	sid     string
	metric  Metric
	max     int64
}

var zoneSpecs = []zoneSpec{
	{"r1", SubjectTenant, "", RequestsPerDay, 6},
	{"r2", SubjectTenant, "", TokensPerMonth, 2500},
	{"r3", SubjectTenant, "", CostPerDay, 700_000},
	{"r4", SubjectKey, "k1", TokensPerDay, 700},
	{"r5", SubjectServiceAccount, "s1", RequestsPerMonth, 25},
	{"r6", SubjectTenant, "", CostPerMonth, 2_500_000},
}

// limitsFor builds the limits of a request with the periods of the tenant's calendar, and checks
// them against the definition: the regular civil period of the zone in force at the start.
func (p *zoneProp) limitsFor(q *refReq) []Limit {
	tc := p.calendar()
	var out []Limit
	for _, r := range zoneSpecs {
		if (r.subject == SubjectKey && r.sid != q.key) || (r.subject == SubjectServiceAccount && r.sid != q.sa) {
			continue
		}
		kind, _ := r.metric.Period()
		got := tc.Of(kind, q.start)
		zone := p.inForce(q.start)
		want := p.cals[zone].Of(kind, q.start)
		if got != want {
			p.fail("request at %s: the %s period of %s is %v, want the regular period %v of the zone in force, %s", q.start, r.metric, r.id, got, want, zone)
		}
		l := Limit{Tenant: p.tn, Subject: r.subject, ID: r.sid, Metric: r.metric, Max: r.max, Level: LevelTenant, RuleID: r.id, Period: got}
		if r.metric.Cost() {
			l.Currency = refCurrency
		}
		out = append(out, l)
	}
	if got, want := tc.Day(q.start), civilDate(p.t, p.inForce(q.start), q.start); got != want {
		p.fail("Day(%s) = %q, want %q", q.start, got, want)
	}
	return out
}

func (p *zoneProp) predict(q *refReq, ls []Limit) (Limit, bool) {
	for _, l := range ls {
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

// request is one whole request: reserved, then settled at once, its record in the ledger at once.
func (p *zoneProp) request() {
	rnd := p.rng
	q := &refReq{tn: p.tn, start: p.clk.now(), sa: []string{"s1", "s2"}[rnd(2)], key: []string{"k1", "k2"}[rnd(2)],
		estTokens: int64(1 + rnd(300)), estCost: int64(1 + rnd(400_000))}
	p.model.add(q)
	ls := p.limitsFor(q)
	if err := p.rec.Ensure(context.Background(), p.tn, ls); err != nil {
		p.fail("ensure: %v", err)
	}
	want, refuse := p.predict(q, ls)
	res, ref, err := p.m.Reserve(context.Background(), Request{ID: fmt.Sprintf("q%d", q.id), Limits: ls, Start: q.start,
		Estimate: Amounts{Tokens: q.estTokens, Cost: known(refCurrency, micro(q.estCost))}, TTL: propTTL})
	if err != nil {
		p.fail("reserve: %v", err)
	}
	if (ref != nil) != refuse {
		p.fail("request #%d at %s: refused = %v, the whole-period reference says %v (first limit without room: %+v)", q.id, q.start, ref != nil, refuse, want)
	}
	if ref != nil {
		q.end = refReleased
		p.logf("#%d refused by %s", q.id, ref.Limit.Metric)
		return
	}
	q.admitted, q.admit, q.admitSeq, q.res, q.limits = true, q.start, p.model.tick(), res, ls
	q.actTokens, q.actCost = int64(rnd(int(q.estTokens)+1)), int64(rnd(int(q.estCost)+1))
	if err := p.m.Settle(context.Background(), res, Amounts{Tokens: q.actTokens, Cost: known(refCurrency, micro(q.actCost))}); err != nil {
		p.fail("settle: %v", err)
	}
	q.end, q.endAt, q.endSeq, q.commitAt, q.resolveAt = refSettled, q.start, p.model.tick(), q.start, q.start
	for _, l := range ls {
		p.seen[fmt.Sprintf("%s/%s/%s/%d", l.Subject, l.ID, l.Metric, l.Period.Start.Unix())] = l
		c, ok := p.m.Counted(l)
		want := p.model.sum(l, func(*refReq) bool { return true })
		if !ok || c.N != want.N || c.Cost.String() != want.Cost.String() {
			p.fail("request #%d: the counter of %s %v holds %d / %s (present %v), the whole period holds %d / %s", q.id, l.Metric, l.Period, c.N, c.Cost, ok, want.N, want.Cost)
		}
	}
	p.logf("#%d admitted, %d tokens %d micro", q.id, q.actTokens, q.actCost)
}

func (p *zoneProp) advance(d time.Duration) {
	for d > 0 {
		c := min(d, testInterval)
		d -= c
		p.clk.advance(c)
		p.m.Sweep(p.clk.now())
		p.rec.Tick(context.Background())
	}
}

func (p *zoneProp) run(ops int) {
	for i := 0; i < ops; i++ {
		switch r := p.rng(100); {
		case r < 50:
			p.request()
		case r < 80:
			switch p.rng(10) {
			case 0:
				p.advance(time.Duration(1+p.rng(30)) * time.Hour)
			case 1, 2, 3:
				p.advance(time.Duration(1+p.rng(30)) * time.Minute)
			default:
				p.advance(time.Duration(1+p.rng(120)) * time.Second)
			}
		case r < 88 && !p.row.from.IsZero() && p.clk.now().Before(p.row.from):
			// Jump to the neighbourhood of the instant of a change that is pending.
			to := p.row.from.Add(time.Duration(p.rng(300)-120) * time.Minute)
			if to.After(p.clk.now()) {
				p.advance(to.Sub(p.clk.now()))
			}
		case r < 94:
			p.setZone(zoneNames[p.rng(len(zoneNames))])
		default:
			p.advance(time.Duration(1+p.rng(20)) * 24 * time.Hour)
		}
	}
}

// TestZoneChangeNeverFrees is T-700: random schedules of requests and zone changes against the
// reference of 5.2. At every admission the usage of the whole civil day and month of the zone in
// force, plus the estimate, is within the limit (what was used before the change is included); no
// period in progress at a change ends before its old end; the bound of 5.2 section 5.5 holds per
// period.
func TestZoneChangeNeverFrees(t *testing.T) {
	seed := seedOf(t)
	bases := []time.Time{
		at(2026, 1, 31, 20, 0, 0), at(2026, 2, 28, 22, 0, 0), at(2026, 3, 28, 22, 0, 0), at(2026, 10, 24, 22, 0, 0),
		at(2026, 12, 31, 22, 30, 0), at(2028, 2, 28, 23, 0, 0), at(2026, 6, 28, 10, 0, 0), at(2026, 4, 4, 12, 0, 0),
	}
	changes, admitted := 0, 0
	for n := range max(1, schedules()/40) {
		rng := rngOf(seed, n)
		intn := func(k int) int { return rng.IntN(k) }
		clk := newClock()
		clk.t = bases[intn(len(bases))].Add(time.Duration(intn(7200)) * time.Second)
		p := &zoneProp{t: t, seed: seed, rng: intn, clk: clk, tn: tid(1), model: &refModel{}, cals: map[string]*Calendar{}, seen: map[string]Limit{}}
		for _, z := range zoneNames {
			p.cals[z] = mustCalendar(t, z)
		}
		start := zoneNames[intn(len(zoneNames))]
		p.row = zoneRow{cur: start}
		p.name = fmt.Sprintf("seed %d schedule %d (start zone %s)", seed, n, start)
		p.m, _ = newMemory(t, clk)
		led := &fakeLedger{model: p.model, clk: clk}
		var err error
		p.rec, err = NewReconciler(ReconcilerOptions{Store: p.m, Ledger: led, Horizon: testHorizon, Interval: testInterval, Logger: quietLogger(), Now: clk.now})
		if err != nil {
			t.Fatalf("the reconciler takes no calendar (sprint 5.3): %v", err)
		}
		p.run(120)
		for _, r := range p.model.reqs {
			if r.admitted {
				admitted++
			}
		}
		changes += strings.Count(strings.Join(p.trace, "\n"), "takes effect at")
		// The bound of 5.2 section 5.5, per period: nothing exceeded its reservation, so what was spent is within the limit.
		for key, l := range p.seen {
			if spent, bound, _ := p.model.Bound(l); spent > bound || spent > l.Max {
				p.fail("period %s: spent %d above the limit %d (bound %d)", key, spent, l.Max, bound)
			}
		}
		if got := p.m.Outstanding(); got != 0 {
			p.fail("%d reservations outstanding", got)
		}
	}
	if admitted == 0 || changes == 0 {
		t.Errorf("the schedules admitted %d requests and made %d zone changes: they prove nothing", admitted, changes)
	}
}
