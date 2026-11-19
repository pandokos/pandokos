package limits

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/pricing"
)

// curProp is one random schedule of requests, budgets in three currencies, currency changes and zone
// changes against the reference of 5.2 extended with "a cost limit counts only the requests whose cost
// is in its currency" (the helpers' refReq.cur). It reuses the machinery of the zone property: the
// clock, the store, the reconciler over the reference ledger and the zone change rule.
type curProp struct {
	*zoneProp
	cur    zoneRow // the three currency columns; zoneRow is "current, previous, from"
	rules  []Rule
	change []time.Time // the instants of the currency changes made so far
}

var curNames = []string{"USD", "EUR", "GBP"}

// curSpecs are the budgets of the tenant: a daily and a monthly one in each currency, with values
// that differ per currency so that a limit applied in the wrong currency is noticed; and two count
// limits that no currency touches.
func curRules() []Rule {
	var out []Rule
	for i, c := range curNames {
		out = append(out,
			Rule{ID: "d-" + c, SubjectKind: SubjectTenant, Metric: CostPerDay, Value: int64(700_000 + 150_000*i), Currency: c, Enforce: true},
			Rule{ID: "m-" + c, SubjectKind: SubjectTenant, Metric: CostPerMonth, Value: int64(2_500_000 + 400_000*i), Currency: c, Enforce: true},
		)
	}
	out = append(out,
		Rule{ID: "rd", SubjectKind: SubjectTenant, Metric: RequestsPerDay, Value: 40, Enforce: true},
		Rule{ID: "tm", SubjectKind: SubjectKey, SubjectID: "k1", Metric: TokensPerMonth, Value: 4000, Enforce: true},
	)
	return out
}

// active is the Active of the row.
func (p *curProp) active() pricing.Active {
	a := pricing.Active{Current: p.cur.cur}
	if !p.cur.from.IsZero() {
		a.Previous, a.From = p.cur.prev, p.cur.from
	}
	return a
}

// setCurrency is the change rule of section 5.3 as the administration API applies it (plan C6): the
// instant is limits.NextChange of the zone in force; a second change keeps it; the currency in force
// withdraws the pending change.
func (p *curProp) setCurrency(name string) {
	now := p.clk.now()
	act := p.active()
	inForce := act.At(now)
	pending := !p.cur.from.IsZero() && now.Before(p.cur.from)
	switch {
	case name == p.cur.cur:
		p.logf("currency %s: already set", name)
	case name == inForce:
		p.cur = zoneRow{cur: inForce}
		p.logf("currency %s: the pending change is withdrawn", name)
	case pending:
		p.cur.cur = name
		p.logf("currency %s replaces the pending one; the instant %s stays", name, p.cur.from.Format(time.RFC3339))
	default:
		zone := p.inForce(now)
		e := NextChange(p.cals[zone], now)
		if want := p.cals[zone].Of(PerMonth, now).End; !e.Equal(want) {
			p.fail("NextChange(%s, %s) = %s, want the end of the month in force, %s", zone, now, e, want)
		}
		if day := p.cals[zone].Of(PerDay, now).End; day.After(e) {
			p.fail("the day in progress at %s ends at %s, after the change at %s", now, day, e)
		}
		if !p.row.from.IsZero() && now.Before(p.row.from) && !p.row.from.Equal(e) {
			p.fail("a zone change is pending for %s and the currency change would take effect at %s: both must share the instant", p.row.from, e)
		}
		p.cur = zoneRow{cur: name, prev: inForce, from: e}
		p.change = append(p.change, e)
		p.logf("currency %s takes effect at %s (in force: %s)", name, e.Format(time.RFC3339), inForce)
	}
}

// limitsFor resolves the limits of a request through the rule set, as the gateway does.
func (p *curProp) limitsFor(q *refReq) []Limit {
	set := NewScopedRuleSet(p.tn, nil, nil, p.rules)
	set.Calendar, set.Currency = p.calendar(), p.active()
	ls := set.For(Query{ServiceAccountID: q.sa, KeyID: q.key, Start: q.start})
	want := p.active().At(q.start)
	costs := 0
	for _, l := range ls {
		if !l.Metric.Cost() {
			continue
		}
		costs++
		if l.Currency != want {
			p.fail("request at %s: a budget in %s applies, the currency in force is %s: %+v", q.start, l.Currency, want, l)
		}
		// The period of the limit is the regular period of the zone in force at the start, and a budget
		// of the old currency never reaches into the new one.
		if act := p.active(); !act.From.IsZero() && q.start.Before(act.From) && l.Period.End.After(act.From) {
			p.fail("request at %s: the %s period %v of the budget in %s ends after the change at %s", q.start, l.Metric, l.Period, l.Currency, act.From)
		}
	}
	if costs != 2 {
		p.fail("request at %s: %d cost limits apply, want the daily and the monthly budget of one currency", q.start, costs)
	}
	return ls
}

func (p *curProp) request() {
	rnd := p.rng
	q := &refReq{tn: p.tn, start: p.clk.now(), sa: []string{"s1", "s2"}[rnd(2)], key: []string{"k1", "k2"}[rnd(2)],
		estTokens: int64(1 + rnd(300)), estCost: int64(1 + rnd(400_000))}
	q.cur = p.active().At(q.start)
	p.model.add(q)
	ls := p.limitsFor(q)
	if err := p.rec.Ensure(context.Background(), p.tn, ls); err != nil {
		p.fail("ensure: %v", err)
	}
	want, refuse := p.predict(q, ls)
	res, ref, err := p.m.Reserve(context.Background(), Request{ID: fmt.Sprintf("q%d", q.id), Limits: ls, Start: q.start,
		Estimate: Amounts{Tokens: q.estTokens, Cost: known(q.cur, micro(q.estCost))}, TTL: propTTL})
	if err != nil {
		p.fail("reserve: %v", err)
	}
	if (ref != nil) != refuse {
		p.fail("request #%d at %s in %s: refused = %v, the whole-period reference says %v (first limit without room: %+v)", q.id, q.start, q.cur, ref != nil, refuse, want)
	}
	if ref != nil {
		q.end = refReleased
		p.logf("#%d refused by %s", q.id, ref.Limit.Metric)
		return
	}
	q.admitted, q.admit, q.admitSeq, q.res, q.limits = true, q.start, p.model.tick(), res, ls
	q.actTokens, q.actCost = int64(rnd(int(q.estTokens)+1)), int64(rnd(int(q.estCost)+1))
	if err := p.m.Settle(context.Background(), res, Amounts{Tokens: q.actTokens, Cost: known(q.cur, micro(q.actCost))}); err != nil {
		p.fail("settle: %v", err)
	}
	q.end, q.endAt, q.endSeq, q.commitAt, q.resolveAt = refSettled, q.start, p.model.tick(), q.start, q.start
	for _, l := range ls {
		p.seen[fmt.Sprintf("%s/%s/%s/%s/%d", l.Subject, l.ID, l.Metric, l.Currency, l.Period.Start.Unix())] = l
		c, ok := p.m.Counted(l)
		whole := p.model.sum(l, func(*refReq) bool { return true })
		if !ok || c.N != whole.N || c.Cost.String() != whole.Cost.String() {
			p.fail("request #%d: the counter of %s in %s %v holds %d / %s (present %v), the whole period holds %d / %s", q.id, l.Metric, l.Currency, l.Period, c.N, c.Cost, ok, whole.N, whole.Cost)
		}
	}
	p.logf("#%d admitted in %s, %d tokens %d micro", q.id, q.cur, q.actTokens, q.actCost)
}

func (p *curProp) run(ops int) {
	for i := 0; i < ops; i++ {
		switch r := p.rng(100); {
		case r < 50:
			p.request()
		case r < 76:
			switch p.rng(10) {
			case 0:
				p.advance(time.Duration(1+p.rng(30)) * time.Hour)
			case 1, 2, 3:
				p.advance(time.Duration(1+p.rng(30)) * time.Minute)
			default:
				p.advance(time.Duration(1+p.rng(120)) * time.Second)
			}
		case r < 86 && !p.cur.from.IsZero() && p.clk.now().Before(p.cur.from):
			// Jump to the neighbourhood of the instant of a change that is pending.
			to := p.cur.from.Add(time.Duration(p.rng(300)-120) * time.Minute)
			if to.After(p.clk.now()) {
				p.advance(to.Sub(p.clk.now()))
			}
		case r < 93:
			p.setCurrency(curNames[p.rng(len(curNames))])
		case r < 95:
			p.setZone(zoneNames[p.rng(len(zoneNames))])
		default:
			p.advance(time.Duration(1+p.rng(20)) * 24 * time.Hour)
		}
	}
}

// TestCurrencyChangeNeverFrees is T-730: random schedules of requests, budgets in two or more
// currencies and currency changes (with and without a zone change pending) against the reference of
// 5.2. The instant of a change is the end of the month in progress and of its last day; until it every
// admission is within the limit of the old currency's budget over its whole period; from it within the
// new one's; no budget stops applying inside one of its periods.
func TestCurrencyChangeNeverFrees(t *testing.T) {
	seed := seedOf(t)
	bases := []time.Time{
		at(2026, 1, 31, 20, 0, 0), at(2026, 2, 28, 22, 0, 0), at(2026, 3, 28, 22, 0, 0), at(2026, 10, 24, 22, 0, 0),
		at(2026, 12, 31, 22, 30, 0), at(2028, 2, 28, 23, 0, 0), at(2026, 6, 28, 10, 0, 0), at(2026, 4, 4, 12, 0, 0),
	}
	changes, admitted, switched := 0, 0, 0
	for n := range max(1, schedules()/40) {
		rng := rngOf(seed, n)
		intn := func(k int) int { return rng.IntN(k) }
		clk := newClock()
		clk.t = bases[intn(len(bases))].Add(time.Duration(intn(7200)) * time.Second)
		zp := &zoneProp{t: t, seed: seed, rng: intn, clk: clk, tn: tid(1), model: &refModel{}, cals: map[string]*Calendar{}, seen: map[string]Limit{}}
		for _, z := range zoneNames {
			zp.cals[z] = mustCalendar(t, z)
		}
		startZone := zoneNames[intn(len(zoneNames))]
		zp.row = zoneRow{cur: startZone}
		p := &curProp{zoneProp: zp, rules: curRules()}
		p.cur = zoneRow{cur: curNames[intn(len(curNames))]}
		p.name = fmt.Sprintf("seed %d schedule %d (start zone %s, start currency %s)", seed, n, startZone, p.cur.cur)
		p.m, _ = newMemory(t, clk)
		led := &fakeLedger{model: p.model, clk: clk}
		var err error
		p.rec, err = NewReconciler(ReconcilerOptions{Store: p.m, Ledger: led, Horizon: testHorizon, Interval: testInterval, Logger: quietLogger(), Now: clk.now})
		if err != nil {
			t.Fatalf("the reconciler: %v", err)
		}
		p.run(120)
		currencies := map[string]bool{}
		for _, r := range p.model.reqs {
			if r.admitted {
				admitted++
				currencies[r.cur] = true
			}
		}
		if len(currencies) > 1 {
			switched++
		}
		changes += len(p.change)
		// The bound of 5.2 section 5.5, per period and currency: what was spent is within the limit.
		for key, l := range p.seen {
			if spent, bound, _ := p.model.Bound(l); spent > bound || spent > l.Max {
				p.fail("period %s: spent %d above the limit %d (bound %d)", key, spent, l.Max, bound)
			}
		}
		if got := p.m.Outstanding(); got != 0 {
			p.fail("%d reservations outstanding", got)
		}
		_ = strings.Join
	}
	if admitted == 0 || changes == 0 || switched == 0 {
		t.Errorf("the schedules admitted %d requests, made %d currency changes and %d of them admitted in two currencies: they prove nothing", admitted, changes, switched)
	}
}
