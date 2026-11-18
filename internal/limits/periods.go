package limits

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/tenant"
)

// Piece is the ledger aggregate of [From, To).
type Piece struct {
	From, To time.Time
	Rows     []UsageGroup
}

// amt is an amount in the unit of a metric: n for requests and tokens, c for the cost metrics.
// Only the field of the metric is ever used.
type amt struct {
	n int64
	c pricing.Decimal
}

func addAmt(cost bool, x, y amt) amt {
	if cost {
		return amt{c: x.c.Add(y.c)}
	}
	return amt{n: x.n + y.n}
}

func subAmt(cost bool, x, y amt) amt {
	if cost {
		return amt{c: x.c.Add(y.c.MulInt(-1))}
	}
	return amt{n: x.n - y.n}
}

func (a amt) isZero(cost bool) bool {
	if cost {
		return a.c.Sign() == 0
	}
	return a.n == 0
}

func decInt(n int64) pricing.Decimal {
	d, err := pricing.ParseDecimal(strconv.FormatInt(n, 10))
	if err != nil {
		return pricing.Decimal{}
	}
	return d
}

// microDec is n millionths as a decimal.
func microDec(n int64) pricing.Decimal { return decInt(n).DivPow10(6) }

// floorMicros is a non-negative decimal rounded down to a whole number of millionths, as that
// number; a negative decimal is 0 and one beyond int64 saturates.
func floorMicros(d pricing.Decimal) int64 {
	if d.Sign() <= 0 {
		return 0
	}
	ip, fp, _ := strings.Cut(d.String(), ".")
	fp = (fp + "000000")[:6]
	n, err := strconv.ParseInt(ip+fp, 10, 64)
	if err != nil {
		return math.MaxInt64
	}
	return n
}

// markID identifies a mark that was recorded.
type markID struct {
	rule string
	pct  int
}

type liveMin struct {
	minute int64 // Unix minute of the start of the requests
	v      amt
}

// periodCounter is the state of a counter of a calendar period (sprint.md 5.4): counted is
// base plus live; used, what the thresholds follow, is counted minus reserved.
type periodCounter struct {
	lim         Limit // the identity of the counter; Max, marks and rule are those of the last request
	cost        bool
	base        amt       // what the ledger holds for the period before the cut
	live        amt       // the sum of mins
	mins        []liveMin // ascending by minute: what this instance holds from the cut on
	reserved    amt       // the part of live that belongs to reservations outstanding
	outstanding int
	touched     time.Time
	recorded    []markID
	exhausted   bool
	maxMicro    int64
	maxDec      pricing.Decimal
}

func newPeriodCounter(l Limit, now time.Time) *counter {
	return &counter{per: &periodCounter{lim: l, cost: l.Metric.Cost(), touched: now}}
}

func (pc *periodCounter) counted() amt { return addAmt(pc.cost, pc.base, pc.live) }
func (pc *periodCounter) used() amt    { return subAmt(pc.cost, pc.counted(), pc.reserved) }

// maxDecimal returns max millionths as a decimal, cached.
func (pc *periodCounter) maxDecimal(max int64) pricing.Decimal {
	if pc.maxMicro != max || max != 0 && pc.maxDec.Sign() == 0 {
		pc.maxMicro, pc.maxDec = max, microDec(max)
	}
	return pc.maxDec
}

// addLive adds d (which may be negative) to the minute.
func (pc *periodCounter) addLive(minute int64, d amt) {
	if d.isZero(pc.cost) {
		return
	}
	pc.live = addAmt(pc.cost, pc.live, d)
	i := len(pc.mins) - 1
	for i >= 0 && pc.mins[i].minute > minute {
		i--
	}
	if i >= 0 && pc.mins[i].minute == minute {
		pc.mins[i].v = addAmt(pc.cost, pc.mins[i].v, d)
		return
	}
	pc.mins = slices.Insert(pc.mins, i+1, liveMin{minute: minute, v: d})
}

func (pc *periodCounter) reserve(l Limit, minute int64, a amt, now time.Time) {
	pc.lim = l
	pc.addLive(minute, a)
	pc.reserved = addAmt(pc.cost, pc.reserved, a)
	pc.outstanding++
	pc.touched = now
}

// finish ends a reservation of est with the amount act (zero when released). live is false when
// the minute of the request was already replaced by the ledger: then only reserved changes.
func (pc *periodCounter) finish(_ Limit, minute int64, live bool, est, act amt, now time.Time) {
	if live {
		pc.addLive(minute, subAmt(pc.cost, act, est))
	}
	pc.reserved = subAmt(pc.cost, pc.reserved, est)
	pc.outstanding--
	pc.touched = now
}

// droppable reports whether a sweep at now may drop the counter.
func (pc *periodCounter) droppable(cut, now time.Time, horizon time.Duration) bool {
	if pc.outstanding > 0 {
		return false
	}
	if !now.Before(pc.lim.Period.End.Add(horizon)) {
		return true
	}
	// One whose period starts at or after the cut is not loaded again but created empty: it must
	// stay until the cut has passed its start.
	return pc.lim.Period.Start.Before(cut) && now.Sub(pc.touched) >= idlePeriodCounter
}

// usable is the amount of a cost the budget of a currency can measure: nothing when the cost is
// unknown or in another currency.
func usable(c pricing.Cost, currency string) pricing.Decimal {
	if c.Known && c.Currency == currency && c.Amount.Sign() > 0 {
		return c.Amount
	}
	return pricing.Decimal{}
}

// estAmount is what a request reserves from a period limit.
func estAmount(l Limit, est Amounts) amt {
	switch l.Metric {
	case TokensPerDay, TokensPerMonth:
		return amt{n: est.Tokens}
	case CostPerDay, CostPerMonth:
		return amt{c: usable(est.Cost, l.Currency)}
	}
	return amt{n: 1}
}

// actualAmount is what a settled request counts in a period limit.
func actualAmount(l Limit, actual Amounts) amt {
	switch l.Metric {
	case TokensPerDay, TokensPerMonth:
		return amt{n: max(actual.Tokens, 0)}
	case CostPerDay, CostPerMonth:
		return amt{c: usable(actual.Cost, l.Currency)}
	}
	return amt{n: 1}
}

// periodRoom says whether a period limit has no room for the estimate (full), what remains of it
// and whether the estimate alone exceeds it. c may be nil: an empty counter.
func periodRoom(l Limit, c *counter, est Amounts) (full bool, remaining int64, over bool) {
	if l.Max == 0 {
		return false, 0, false
	}
	a := estAmount(l, est)
	if !l.Metric.Cost() {
		var counted int64
		if c != nil {
			counted = c.per.base.n + c.per.live.n
		}
		return counted+a.n > l.Max, max(l.Max-counted, 0), a.n > l.Max
	}
	var maxDec pricing.Decimal
	var counted pricing.Decimal
	if c != nil {
		maxDec = c.per.maxDecimal(l.Max)
		counted = c.per.base.c.Add(c.per.live.c)
	} else {
		maxDec = microDec(l.Max)
	}
	full = counted.Add(a.c).Cmp(maxDec) > 0
	if !full {
		return false, 0, false
	}
	return true, floorMicros(maxDec.Add(counted.MulInt(-1))), a.c.Cmp(maxDec) > 0
}

// event builds the event of a counter; the caller fills kind, percent, value and what was seen.
func (pc *periodCounter) event(kind string, percent int, value int64, observed pricing.Decimal) Event {
	l := pc.lim
	return Event{Tenant: l.Tenant, Kind: kind, Level: l.Level, RuleID: l.RuleID, Subject: l.Subject, SubjectID: l.ID,
		Provider: l.Provider, Model: l.Model, Metric: l.Metric, Currency: l.Currency, Period: l.Period,
		Percent: percent, Value: value, Observed: observed}
}

func (pc *periodCounter) observed(a amt) pricing.Decimal {
	if pc.cost {
		return a.c
	}
	return decInt(a.n)
}

// exhausted records, once per period, that a limit refused a request.
func (m *Memory) exhausted(ts *tenantPeriods, c *counter, l Limit, now time.Time) {
	if m.events == nil {
		return
	}
	if c == nil {
		c = newPeriodCounter(l, now)
		ts.pcs[keyOf(l)] = c
	}
	pc := c.per
	if pc.exhausted {
		return
	}
	pc.lim = l
	if m.events.Offer(pc.event(EventExhausted, 0, l.Max, pc.observed(pc.counted()))) {
		pc.exhausted = true
	}
}

// testMarks offers the event of every mark the counter reached and has not recorded; a mark is
// recorded only when the sink took its event. The caller holds the lock.
func (m *Memory) testMarks(c *counter, marks []Mark) {
	if m.events == nil || len(marks) == 0 {
		return
	}
	pc := c.per
	used := pc.used()
	for _, mk := range marks {
		id := markID{mk.RuleID, mk.Percent}
		if slices.Contains(pc.recorded, id) {
			continue
		}
		if pc.cost {
			if used.c.Cmp(microDec(mk.At)) < 0 {
				continue
			}
		} else if used.n < mk.At {
			continue
		}
		ev := pc.event(EventThresholdReached, mk.Percent, mk.Value, pc.observed(used))
		ev.RuleID, ev.Level = mk.RuleID, mk.Level
		if m.events.Offer(ev) {
			pc.recorded = append(pc.recorded, id)
		}
	}
}

func samePeriod(a, b Period) bool { return a.Start.Equal(b.Start) && a.End.Equal(b.End) }

// Cut returns the cut of a tenant: the instant before which the ledger is the truth of its
// period counters.
func (m *Memory) Cut(t tenant.ID) (time.Time, bool) {
	sh := m.shardOf(t)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if ts := sh.ten[t]; ts != nil {
		return ts.cut, true
	}
	return time.Time{}, false
}

// SetCut gives the tenant its cut, only when it has none.
func (m *Memory) SetCut(t tenant.ID, cut time.Time) {
	sh := m.shardOf(t)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.ten[t] == nil {
		sh.ten[t] = &tenantPeriods{cut: cut, setAt: m.now(), pcs: map[counterKey]*counter{}}
	}
}

// Missing returns the period limits of ls that Reserve could not serve: no counter, and the
// tenant has no cut or the period started before it.
func (m *Memory) Missing(ls []Limit) []Limit {
	var out []Limit
	var sh *shard
	var locked tenant.ID
	for _, l := range ls {
		if !isPeriod(l.Metric) {
			continue
		}
		if sh == nil || l.Tenant != locked {
			if sh != nil {
				sh.mu.Unlock()
			}
			sh, locked = m.shardOf(l.Tenant), l.Tenant
			sh.mu.Lock()
		}
		ts := sh.ten[l.Tenant]
		if ts == nil || (ts.pcs[keyOf(l)] == nil && l.Period.Start.Before(ts.cut)) {
			out = append(out, l)
		}
	}
	if sh != nil {
		sh.mu.Unlock()
	}
	return out
}

// seed splits the rows a limit counts into the base (before the live part) and the minutes.
func seed(l Limit, rows []UsageGroup) (base amt, mins []liveMin) {
	cost := l.Metric.Cost()
	for i := range rows {
		g := &rows[i]
		a, ok := groupAmount(l, g)
		if !ok {
			continue
		}
		if g.Minute == 0 {
			base = addAmt(cost, base, a)
			continue
		}
		if g.Minute*60 >= l.Period.End.Unix() {
			continue // a minute after the period: another period's request
		}
		j, found := slices.BinarySearchFunc(mins, g.Minute, func(e liveMin, m int64) int { return cmpInt(e.minute, m) })
		if found {
			mins[j].v = addAmt(cost, mins[j].v, a)
		} else {
			mins = slices.Insert(mins, j, liveMin{minute: g.Minute, v: a})
		}
	}
	return base, mins
}

// Load creates the counters of ls for one period from rows read with liveFrom = the tenant's
// cut. A counter that exists is left alone; a tenant without a cut gets no counter. A day and a
// month that start at the same instant are loaded from the same rows, so a limit is served when
// its period starts at p.Start.
func (m *Memory) Load(t tenant.ID, p Period, ls []Limit, rows []UsageGroup) {
	sh := m.shardOf(t)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	ts := sh.ten[t]
	if ts == nil {
		return
	}
	now := m.now()
	for _, l := range ls {
		if l.Tenant != t || !isPeriod(l.Metric) || !l.Period.Start.Equal(p.Start) {
			continue
		}
		k := keyOf(l)
		if ts.pcs[k] != nil {
			continue
		}
		c := newPeriodCounter(l, now)
		pc := c.per
		pc.base, pc.mins = seed(l, rows)
		for _, e := range pc.mins {
			pc.live = addAmt(pc.cost, pc.live, e.v)
		}
		ts.pcs[k] = c
		m.testMarks(c, l.Marks)
	}
}

// Fold adds the pieces to the base of every counter of t whose period contains them, drops the
// live minutes before cut and sets the cut. A cut that is not after the current one changes
// nothing.
func (m *Memory) Fold(t tenant.ID, pieces []Piece, cut time.Time) {
	sh := m.shardOf(t)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	ts := sh.ten[t]
	if ts == nil {
		ts = &tenantPeriods{cut: cut, setAt: m.now(), pcs: map[counterKey]*counter{}}
		sh.ten[t] = ts
	} else if !cut.After(ts.cut) {
		return
	}
	cutMinute := cut.Unix() / 60
	for _, c := range ts.pcs {
		pc := c.per
		p := pc.lim.Period
		for _, pi := range pieces {
			if pi.From.Before(p.Start) || pi.To.After(p.End) {
				continue
			}
			pc.base = addAmt(pc.cost, pc.base, amtOfSum(pc.lim, pi.Rows))
		}
		n := 0
		for n < len(pc.mins) && pc.mins[n].minute < cutMinute {
			pc.live = subAmt(pc.cost, pc.live, pc.mins[n].v)
			n++
		}
		pc.mins = slices.Delete(pc.mins, 0, n)
	}
	ts.cut = cut
	for _, c := range ts.pcs {
		m.testMarks(c, c.per.lim.Marks)
	}
}

// Rebase replaces the base of every counter of t with period p by the sum of rows.
func (m *Memory) Rebase(t tenant.ID, p Period, rows []UsageGroup) {
	sh := m.shardOf(t)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	ts := sh.ten[t]
	if ts == nil {
		return
	}
	for _, c := range ts.pcs {
		if !samePeriod(c.per.lim.Period, p) {
			continue
		}
		c.per.base = amtOfSum(c.per.lim, rows)
		m.testMarks(c, c.per.lim.Marks)
	}
}

// PeriodTenants returns the tenants that have a cut.
func (m *Memory) PeriodTenants() []tenant.ID {
	var out []tenant.ID
	for i := range m.shards {
		sh := &m.shards[i]
		sh.mu.Lock()
		for t := range sh.ten {
			out = append(out, t)
		}
		sh.mu.Unlock()
	}
	return out
}

// Periods returns the distinct periods of the period counters of t, earliest first.
func (m *Memory) Periods(t tenant.ID) []Period {
	sh := m.shardOf(t)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	ts := sh.ten[t]
	if ts == nil {
		return nil
	}
	var out []Period
	for _, c := range ts.pcs {
		p := c.per.lim.Period
		if !slices.ContainsFunc(out, func(o Period) bool { return samePeriod(o, p) }) {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b Period) int {
		if c := a.Start.Compare(b.Start); c != 0 {
			return c
		}
		return a.End.Compare(b.End)
	})
	return out
}

// Counted returns what the period counter of l counts (base plus live), for tests and metrics.
func (m *Memory) Counted(l Limit) (Amount, bool) {
	sh := m.shardOf(l.Tenant)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	ts := sh.ten[l.Tenant]
	if ts == nil {
		return Amount{}, false
	}
	c := ts.pcs[keyOf(l)]
	if c == nil {
		return Amount{}, false
	}
	a := c.per.counted()
	return Amount{N: a.n, Cost: a.c}, true
}
