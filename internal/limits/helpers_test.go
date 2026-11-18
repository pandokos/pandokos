package limits

import (
	"bytes"
	"context"
	"log/slog"
	"math/rand/v2"
	"os"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"
	_ "time/tzdata" // the zones of the tests come from the embedded database

	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/tenant"
)

// clock is a fake clock safe for concurrent use.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// logBuffer collects log output; it is safe for concurrent use.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newLogger() (*slog.Logger, *logBuffer) {
	b := &logBuffer{}
	return slog.New(slog.NewJSONHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug})), b
}

func newMemory(tb testing.TB, clk *clock) (*Memory, *logBuffer) {
	tb.Helper()
	l, b := newLogger()
	m, err := NewMemory(MemoryOptions{Logger: l, Now: clk.now})
	if err != nil {
		tb.Fatal(err)
	}
	return m, b
}

// tid returns a tenant ID that differs per n.
func tid(n byte) tenant.ID {
	var id tenant.ID
	id[0], id[15] = 0xAB, n
	return id
}

func lim(t tenant.ID, kind SubjectKind, id string, m Metric, max int64) Limit {
	return Limit{Tenant: t, Subject: kind, ID: id, Metric: m, Max: max, Level: LevelTenant}
}

// nineLimits returns the nine limits of a request in the order of resolution, with the same
// maxima for every subject.
func nineLimits(t tenant.ID, conc, reqs, toks int64) []Limit {
	var out []Limit
	for _, k := range SubjectKinds() {
		id := ""
		switch k {
		case SubjectServiceAccount:
			id = "sa-1"
		case SubjectKey:
			id = "key-1"
		}
		out = append(out,
			lim(t, k, id, ConcurrentRequests, conc),
			lim(t, k, id, RequestsPerMinute, reqs),
			lim(t, k, id, TokensPerMinute, toks))
	}
	return out
}

func request(id string, ttl time.Duration, tokens int64, limits ...Limit) Request {
	return Request{ID: id, Limits: limits, Estimate: Amounts{Tokens: tokens}, TTL: ttl}
}

// mustReserve reserves and fails the test unless the request was admitted.
func mustReserve(tb testing.TB, s Store, r Request) Reservation {
	tb.Helper()
	res, ref, err := s.Reserve(context.Background(), r)
	if err != nil {
		tb.Fatalf("reserve %s: %v", r.ID, err)
	}
	if ref != nil {
		tb.Fatalf("reserve %s refused: %+v", r.ID, *ref)
	}
	return res
}

// mustRefuse reserves and fails the test unless the request was refused.
func mustRefuse(tb testing.TB, s Store, r Request) *Refusal {
	tb.Helper()
	_, ref, err := s.Reserve(context.Background(), r)
	if err != nil {
		tb.Fatalf("reserve %s: %v", r.ID, err)
	}
	if ref == nil {
		tb.Fatalf("reserve %s was admitted, want a refusal", r.ID)
	}
	return ref
}

// headroom returns how much a limit has left, without changing anything: for tokens the
// remaining amount reported by the refusal of an estimate that cannot fit; for requests and
// concurrency the number of reservations that fit, taken and given back.
func headroom(tb testing.TB, s Store, l Limit) int64 {
	tb.Helper()
	ctx := context.Background()
	if l.Metric == TokensPerMinute {
		_, ref, err := s.Reserve(ctx, request("probe", time.Hour, l.Max+1, l))
		if err != nil || ref == nil {
			tb.Fatalf("probe of %s: refusal %v, error %v", l.Metric, ref, err)
		}
		return ref.Remaining
	}
	var held []Reservation
	for {
		res, ref, err := s.Reserve(ctx, request("probe", time.Hour, 0, l))
		if err != nil {
			tb.Fatalf("probe of %s: %v", l.Metric, err)
		}
		if ref != nil {
			break
		}
		held = append(held, res)
		if int64(len(held)) > l.Max {
			tb.Fatalf("probe of %s: more reservations than the limit %d", l.Metric, l.Max)
		}
	}
	for _, r := range held {
		if err := s.Release(ctx, r); err != nil {
			tb.Fatalf("probe release: %v", err)
		}
	}
	return int64(len(held))
}

func headrooms(tb testing.TB, s Store, ls []Limit) []int64 {
	tb.Helper()
	out := make([]int64, len(ls))
	for i, l := range ls {
		out[i] = headroom(tb, s, l)
	}
	return out
}

func equalInts(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// seedOf returns the seed of a property test, from PANDOKOS_TEST_SEED or the clock, and logs it.
func seedOf(tb testing.TB) uint64 {
	tb.Helper()
	seed := uint64(time.Now().UnixNano())
	if s := os.Getenv("PANDOKOS_TEST_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			tb.Fatalf("PANDOKOS_TEST_SEED: %v", err)
		}
		seed = n
	}
	tb.Logf("seed %d (set PANDOKOS_TEST_SEED to reproduce)", seed)
	return seed
}

func rngOf(seed uint64, n int) *rand.Rand {
	return rand.New(rand.NewPCG(seed, uint64(n)+0x9E3779B97F4A7C15))
}

// schedules returns the number of schedules of a property test.
func schedules() int {
	if testing.Short() {
		return 200
	}
	return 2000
}

// ---- sprint 5.2 helpers -------------------------------------------------------------------

// dec parses a decimal.
func dec(tb testing.TB, s string) pricing.Decimal {
	tb.Helper()
	d, err := pricing.ParseDecimal(s)
	if err != nil {
		tb.Fatalf("decimal %q: %v", s, err)
	}
	return d
}

// micro returns n millionths as a decimal.
func micro(n int64) pricing.Decimal {
	d, err := pricing.ParseDecimal(strconv.FormatInt(n, 10))
	if err != nil {
		panic(err)
	}
	return d.DivPow10(6)
}

// known is a known cost in a currency.
func known(cur string, d pricing.Decimal) pricing.Cost {
	return pricing.Cost{Known: true, Amount: d, Currency: cur}
}

// perLimit is a limit of a period metric whose period is p.
func perLimit(t tenant.ID, kind SubjectKind, id string, m Metric, max int64, p Period) Limit {
	l := lim(t, kind, id, m, max)
	l.Period = p
	return l
}

// costLimit is a budget in a currency; max is in millionths.
func costLimit(t tenant.ID, m Metric, cur string, max int64, p Period) Limit {
	l := perLimit(t, SubjectTenant, "", m, max, p)
	l.Currency = cur
	return l
}

// periodReq is a request of period limits that started at start.
func periodReq(id string, start time.Time, ttl time.Duration, tokens int64, cost pricing.Cost, ls ...Limit) Request {
	return Request{ID: id, Limits: ls, Start: start, Estimate: Amounts{Tokens: tokens, Cost: cost}, TTL: ttl}
}

// recordingSink collects the events offered to it; refuse makes it refuse every offer.
type recordingSink struct {
	mu      sync.Mutex
	events  []Event
	offers  int
	refuse  bool
	refused int
}

func (s *recordingSink) Offer(e Event) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offers++
	if s.refuse {
		s.refused++
		return false
	}
	s.events = append(s.events, e)
	return true
}

func (s *recordingSink) setRefuse(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuse = v
}

func (s *recordingSink) taken() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events...)
}

func (s *recordingSink) count(kind string, percent int) int {
	n := 0
	for _, e := range s.taken() {
		if e.Kind == kind && e.Percent == percent {
			n++
		}
	}
	return n
}

func newMemoryWith(tb testing.TB, clk *clock, sink EventSink) *Memory {
	tb.Helper()
	l, _ := newLogger()
	m, err := NewMemory(MemoryOptions{Logger: l, Now: clk.now, Events: sink})
	if err != nil {
		tb.Fatal(err)
	}
	return m
}

// counted returns what the store counts for a limit, failing when there is no counter.
func counted(tb testing.TB, m *Memory, l Limit) Amount {
	tb.Helper()
	a, ok := m.Counted(l)
	if !ok {
		tb.Fatalf("no counter for %s %s", l.Metric, l.Period.Start)
	}
	return a
}

// ---- the reference model of the property tests (sprint.md section 7) ------------------------

type refEnd int

const (
	refOpen refEnd = iota
	refSettled
	refReleased
	refExpired
)

// refCurrency is the currency of every model of the reference.
const refCurrency = "EUR"

// refReq is one request of the reference model: its start, admission, limits, reserved amounts,
// the way it ended, and when its usage record was committed (never: lost or rejected).
type refReq struct {
	id               int
	tn               tenant.ID
	sa, key          string
	provider, model  string
	start, admit     time.Time
	admitSeq, endSeq int // order of events in the schedule: the time alone does not order them
	estTokens        int64
	estCost          int64 // millionths
	end              refEnd
	endAt            time.Time
	actTokens        int64
	actCost          int64 // millionths; -1: unknown
	commitAt         time.Time
	lost             bool
	other            bool      // a record of another writer: the store never held it
	admitted         bool      // the store took the reservation; false while pending or when refused
	resolveAt        time.Time // when the record leaves the writer's queue: committed, rejected or lost
	limits           []Limit
	res              Reservation
}

func (q *refReq) matches(l Limit) bool {
	if q.tn != l.Tenant {
		return false
	}
	switch l.Subject {
	case SubjectServiceAccount:
		if l.ID != q.sa {
			return false
		}
	case SubjectKey:
		if l.ID != q.key {
			return false
		}
	}
	return (l.Provider == "" || l.Provider == q.provider) && (l.Model == "" || l.Model == q.model)
}

// amount is what the request counts against a metric: reserved while it is open, actual once
// settled, nothing when released or expired.
func (q *refReq) amount(m Metric) (n int64, cost int64) {
	if !q.admitted {
		return 0, 0
	}
	switch q.end {
	case refOpen:
		switch {
		case m.Cost():
			return 0, q.estCost
		case m == TokensPerDay || m == TokensPerMonth:
			return q.estTokens, 0
		}
		return 1, 0
	case refSettled:
		switch {
		case m.Cost():
			return 0, max(q.actCost, 0)
		case m == TokensPerDay || m == TokensPerMonth:
			return q.actTokens, 0
		}
		return 1, 0
	}
	return 0, 0
}

// inLedger reports whether the usage record of the request is, at asOf, in the ledger.
func (q *refReq) inLedger(asOf time.Time) bool {
	if (q.end != refSettled && !q.other) || q.lost || q.commitAt.IsZero() {
		return false
	}
	return !q.commitAt.After(asOf)
}

// refModel keeps every request and computes the ledger and the bound independently of the store.
type refModel struct {
	mu   sync.Mutex
	reqs []*refReq
	seq  int
}

// tick returns the next number of the order of events.
func (r *refModel) tick() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	return r.seq
}

func (r *refModel) add(q *refReq) {
	r.mu.Lock()
	defer r.mu.Unlock()
	q.id = len(r.reqs)
	r.reqs = append(r.reqs, q)
}

// sum is what the store should count for l from the requests of the model: the amounts of the
// requests that match l and started in its period.
func (r *refModel) sum(l Limit, include func(*refReq) bool) Amount {
	r.mu.Lock()
	defer r.mu.Unlock()
	var a Amount
	var c int64
	for _, q := range r.reqs {
		if !q.matches(l) || q.start.Before(l.Period.Start) || !q.start.Before(l.Period.End) || !include(q) {
			continue
		}
		n, cost := q.amount(l.Metric)
		a.N += n
		c += cost
	}
	a.Cost = micro(c)
	return a
}

// Ledger answers what the usage_record table holds at asOf for the requests of tenant t that
// started in [from, to), grouped as the query groups them.
func (r *refModel) Ledger(asOf time.Time, t tenant.ID, from, liveFrom, to time.Time) []UsageGroup {
	r.mu.Lock()
	defer r.mu.Unlock()
	type key struct {
		minute                                int64
		principal, key, provider, model, curr string
	}
	groups := map[key]*UsageGroup{}
	for _, q := range r.reqs {
		if q.tn != t || !q.inLedger(asOf) || q.start.Before(from) || !q.start.Before(to) {
			continue
		}
		k := key{principal: q.sa, key: q.key, provider: q.provider, model: q.model}
		if !q.start.Before(liveFrom) {
			k.minute = q.start.Unix() / 60
		}
		if q.actCost >= 0 {
			k.curr = refCurrency
		}
		g := groups[k]
		if g == nil {
			g = &UsageGroup{Minute: k.minute, PrincipalID: q.sa, KeyID: q.key, Provider: q.provider, Model: q.model, Currency: k.curr}
			groups[k] = g
		}
		g.Requests++
		g.Tokens += q.actTokens
		if q.actCost > 0 {
			g.Cost = g.Cost.Add(micro(q.actCost))
		}
	}
	out := make([]UsageGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Minute != b.Minute {
			return a.Minute < b.Minute
		}
		return a.PrincipalID+a.KeyID+a.Provider+a.Model+a.Currency < b.PrincipalID+b.KeyID+b.Provider+b.Model+b.Currency
	})
	return out
}

// Bound returns, for the counter l (whose Period is I), what the settled requests spent, the
// right-hand side of the inequality of sprint.md section 5.5 (the maximum plus the excess over
// their reservation of the requests in flight at the last admission of I) and that excess.
func (r *refModel) Bound(l Limit) (spent, bound, excess int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	last := 0
	for _, q := range r.reqs {
		if q.matches(l) && !q.start.Before(l.Period.Start) && q.start.Before(l.Period.End) && q.admitSeq > last && !q.other && q.admitted {
			last = q.admitSeq
		}
	}
	for _, q := range r.reqs {
		if q.other || !q.admitted || !q.matches(l) || q.start.Before(l.Period.Start) || !q.start.Before(l.Period.End) {
			continue
		}
		inFlight := q.end == refSettled && q.admitSeq <= last && q.endSeq > last
		switch l.Metric {
		case TokensPerDay, TokensPerMonth:
			if q.end == refSettled {
				spent += q.actTokens
			}
			if inFlight {
				excess += max(0, q.actTokens-q.estTokens)
			}
		case CostPerDay, CostPerMonth:
			if q.end == refSettled {
				spent += max(q.actCost, 0)
			}
			if inFlight {
				excess += max(0, max(q.actCost, 0)-q.estCost)
			}
		default:
			if q.end == refSettled {
				spent++
			}
		}
	}
	return spent, l.Max + excess, excess
}

// fakeLedger is a Ledger over the reference, with an injectable error and a call log.
type fakeLedger struct {
	model *refModel
	clk   *clock
	mu    sync.Mutex
	err   error
	calls []ledgerCall
}

type ledgerCall struct {
	tenant             tenant.ID
	from, liveFrom, to time.Time
}

func (f *fakeLedger) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeLedger) callLog() []ledgerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ledgerCall(nil), f.calls...)
}

func (f *fakeLedger) Usage(_ context.Context, t tenant.ID, from, liveFrom, to time.Time) ([]UsageGroup, error) {
	f.mu.Lock()
	f.calls = append(f.calls, ledgerCall{t, from, liveFrom, to})
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return f.model.Ledger(f.clk.now(), t, from, liveFrom, to), nil
}

func quietLogger() *slog.Logger {
	l, _ := newLogger()
	return l
}
