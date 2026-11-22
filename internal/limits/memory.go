package limits

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pandokos/pandokos/internal/tenant"
)

const shardCount = 64

// MemoryOptions configure a Memory.
type MemoryOptions struct {
	Logger *slog.Logger     // required
	Now    func() time.Time // default time.Now
	Events EventSink        // takes the events of thresholds and exhausted limits; nil records none
	// Horizon is how long after the end of its period a period counter is kept, so that the
	// reconciler can still fold the last pieces of the period into it (default 12 m 30 s).
	Horizon time.Duration
}

type counterKey struct {
	tenant          tenant.ID
	kind            SubjectKind
	id              string
	provider, model string // identifiers of the scope
	metric          Metric
	currency        string
	periodStart     int64 // Unix seconds; 0 for the metrics of sprint 5.1
}

// counter is a window (requests, tokens) or a gauge (concurrency), by metric, or a period counter.
type counter struct {
	win   window
	gauge int64
	per   *periodCounter // non-nil for a period counter
}

// heldLimit is what a reservation remembers of one of its limits.
type heldLimit struct {
	sec    int64    // rate limits: the second the window attributed the amount to
	c      *counter // period limits: the counter, which a sweep does not drop while it is reserved
	minute int64    // period limits: the minute of the start of the request
}

// held is an outstanding reservation.
type held struct {
	res       Reservation // the stored copy is what settles, whatever the caller passes back
	requestID string
	items     []heldLimit // per limit of res
}

// tenantPeriods is what a shard keeps for a tenant that has period counters: its cut and them.
type tenantPeriods struct {
	cut   time.Time
	setAt time.Time // when the cut was set: a cut without counters is dropped by a sweep after a minute
	pcs   map[counterKey]*counter
}

type shard struct {
	mu       sync.Mutex
	counters map[counterKey]*counter
	held     map[string]*held
	ten      map[tenant.ID]*tenantPeriods
}

// Memory is the in-process Store.
type Memory struct {
	log     *slog.Logger
	now     func() time.Time
	events  EventSink
	horizon time.Duration
	seq     atomic.Uint64
	expired atomic.Int64
	shards  [shardCount]shard
}

var _ Store = (*Memory)(nil)

// NewMemory returns an empty Memory. A logger is required.
func NewMemory(o MemoryOptions) (*Memory, error) {
	if o.Logger == nil {
		return nil, errors.New("limits: the store needs a logger")
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	horizon := o.Horizon
	if horizon <= 0 {
		horizon = 12*time.Minute + 30*time.Second
	}
	m := &Memory{log: o.Logger, now: now, events: o.Events, horizon: horizon}
	for i := range m.shards {
		m.shards[i].counters = make(map[counterKey]*counter)
		m.shards[i].held = make(map[string]*held)
		m.shards[i].ten = make(map[tenant.ID]*tenantPeriods)
	}
	return m, nil
}

func (m *Memory) shardOf(t tenant.ID) *shard {
	h := uint32(2166136261)
	for _, b := range t {
		h ^= uint32(b)
		h *= 16777619
	}
	return &m.shards[h%shardCount]
}

func keyOf(l Limit) counterKey {
	k := counterKey{tenant: l.Tenant, kind: l.Subject, id: l.ID, provider: l.ProviderID, model: l.ModelID, metric: l.Metric, currency: l.Currency}
	if _, ok := l.Metric.Period(); ok {
		k.periodStart = l.Period.Start.Unix()
	}
	return k
}

func isPeriod(m Metric) bool {
	_, ok := m.Period()
	return ok
}

// amountOf is what a request takes from a rate limit.
func amountOf(l Limit, est int64) int64 {
	if l.Metric == TokensPerMinute {
		return est
	}
	return 1
}

// maxLimitsPairwise is the number of limits up to which a repeated counter is detected by
// comparing pairs, which does not allocate.
const maxLimitsPairwise = 16

func validate(r Request) error {
	if r.Estimate.Tokens < 0 {
		return errors.New("limits: negative estimate")
	}
	if r.TTL <= 0 {
		return errors.New("limits: the reservation needs a positive TTL")
	}
	var seen map[counterKey]struct{}
	if len(r.Limits) > maxLimitsPairwise {
		seen = make(map[counterKey]struct{}, len(r.Limits))
	}
	for i, l := range r.Limits {
		k := keyOf(l)
		if seen != nil {
			if _, dup := seen[k]; dup {
				return fmt.Errorf("limits: limit %d repeats the counter of an earlier limit", i)
			}
			seen[k] = struct{}{}
		} else {
			// Pairwise: a request has at most a handful of limits, and this does not allocate.
			for _, e := range r.Limits[:i] {
				if keyOf(e) == k {
					return fmt.Errorf("limits: limit %d repeats the counter of an earlier limit", i)
				}
			}
		}
		if l.Tenant != r.Limits[0].Tenant {
			return errors.New("limits: the limits of one request belong to one tenant")
		}
		if !knownMetric(l.Metric) {
			return fmt.Errorf("limits: unknown metric %q", l.Metric)
		}
		if isPeriod(l.Metric) {
			if l.Max < 0 {
				return fmt.Errorf("limits: limit %d has a negative maximum", i)
			}
			if l.Period.Start.IsZero() || !l.Period.End.After(l.Period.Start) {
				return fmt.Errorf("limits: period limit %d has no period", i)
			}
			if l.Metric.Cost() && l.Currency == "" {
				return fmt.Errorf("limits: budget %d has no currency", i)
			}
		} else if l.Max < 1 {
			return fmt.Errorf("limits: limit %d has a maximum below 1", i)
		}
	}
	return nil
}

// Reserve takes the request, a slot and the estimated tokens from every limit, or nothing.
func (m *Memory) Reserve(_ context.Context, r Request) (Reservation, *Refusal, error) {
	if err := validate(r); err != nil {
		return Reservation{}, nil, err
	}
	id := r.ID + "#" + strconv.FormatUint(m.seq.Add(1), 10)
	res := Reservation{ID: id, Amounts: r.Estimate}
	if len(r.Limits) == 0 {
		now := m.now()
		res.At, res.Expires = now, now.Add(r.TTL)
		return res, nil, nil
	}
	res.Limits = append([]Limit(nil), r.Limits...)
	sh := m.shardOf(r.Limits[0].Tenant)
	tn := r.Limits[0].Tenant
	start := r.Start.Truncate(time.Microsecond)
	minute := start.Unix() / 60

	sh.mu.Lock()
	defer sh.mu.Unlock()
	// The clock is read under the lock: admissions of one shard are attributed in the order
	// they take the lock (sprint.md 5.4).
	now := m.now()
	res.At, res.Expires = now, now.Add(r.TTL)

	var buf [maxLimitsPairwise]*counter
	cs := buf[:0]
	if len(res.Limits) > len(buf) {
		cs = make([]*counter, 0, len(res.Limits))
	}
	// Period counters first: one that must be loaded, or a start before the cut, is an error
	// before anything is looked at.
	ts := sh.ten[tn]
	for _, l := range res.Limits {
		if !isPeriod(l.Metric) {
			continue
		}
		if ts == nil {
			return Reservation{}, nil, ErrNotLoaded
		}
		if start.Before(ts.cut) {
			return Reservation{}, nil, errors.New("limits: the request started before the cut of its tenant")
		}
		if ts.pcs[keyOf(l)] == nil && l.Period.Start.Before(ts.cut) {
			return Reservation{}, nil, ErrNotLoaded
		}
	}

	var ref *Refusal
	var retry time.Duration
	for _, l := range res.Limits {
		var c *counter
		if isPeriod(l.Metric) {
			c = ts.pcs[keyOf(l)]
		} else {
			c = sh.counters[keyOf(l)]
		}
		cs = append(cs, c)
		var rt, reset time.Duration
		var remaining int64
		over := false
		if isPeriod(l.Metric) {
			var full bool
			full, remaining, over = periodRoom(l, c, r.Estimate)
			if !full {
				continue
			}
			rt = untilEnd(l.Period.End, now)
			reset = rt
		} else {
			amount := amountOf(l, r.Estimate.Tokens)
			var counted int64
			if c != nil {
				if l.Metric == ConcurrentRequests {
					counted = c.gauge
				} else {
					counted = c.win.count(now)
				}
			}
			if counted+amount <= l.Max {
				continue
			}
			remaining = max(l.Max-counted, 0)
			switch {
			case l.Metric == ConcurrentRequests:
				rt = time.Second
			case amount > l.Max:
				over, rt = true, 60*time.Second
				if c != nil {
					reset = c.win.reset(now)
				}
			default:
				rt = c.win.leaves(now, counted+amount-l.Max)
				reset = c.win.reset(now)
			}
		}
		if rt > retry {
			retry = rt
		}
		if ref == nil {
			ref = &Refusal{Limit: l, Remaining: remaining, Reset: reset, OverLimit: over}
		}
		if isPeriod(l.Metric) {
			m.exhausted(ts, c, l, now)
		}
	}
	if ref != nil {
		ref.RetryAfter = retry
		return Reservation{}, ref, nil
	}
	items := make([]heldLimit, len(res.Limits))
	for i, l := range res.Limits {
		c := cs[i]
		if isPeriod(l.Metric) {
			if c == nil {
				c = newPeriodCounter(l, now)
				ts.pcs[keyOf(l)] = c
			}
			c.per.reserve(l, minute, estAmount(l, r.Estimate), now)
			items[i] = heldLimit{c: c, minute: minute}
			continue
		}
		if c == nil {
			// Keys are distinct (validate), so no other limit of this request made it.
			c = &counter{}
			sh.counters[keyOf(l)] = c
		}
		if l.Metric == ConcurrentRequests {
			c.gauge++
		} else {
			items[i].sec = c.win.add(now, amountOf(l, r.Estimate.Tokens))
		}
	}
	sh.held[id] = &held{res: res, requestID: r.ID, items: items}
	return res, nil, nil
}

// untilEnd is the time from now to the end of a period, rounded up to a second, at least 1 s.
func untilEnd(end, now time.Time) time.Duration {
	d := end.Sub(now)
	d = (d + time.Second - 1) / time.Second * time.Second
	return max(d, time.Second)
}

// take removes the reservation from its shard; the caller holds the lock.
func (sh *shard) take(id string) (*held, bool) {
	h, ok := sh.held[id]
	if ok {
		delete(sh.held, id)
	}
	return h, ok
}

// finish applies the end of a reservation at now with what it finally counts; the caller holds
// the lock. A released reservation takes everything back. The period counters it changed are
// returned when settle is true, to test their marks.
func (m *Memory) finish(sh *shard, h *held, now time.Time, actual Amounts, release bool) {
	ts := sh.ten[h.res.Limits[0].Tenant]
	for i, l := range h.res.Limits {
		if isPeriod(l.Metric) {
			it := h.items[i]
			live := ts == nil || it.minute >= ts.cut.Unix()/60
			est := estAmount(l, h.res.Amounts)
			act := amt{}
			if !release {
				act = actualAmount(l, actual)
			}
			it.c.per.finish(l, it.minute, live, est, act, now)
			if !release {
				m.testMarks(it.c, l.Marks)
			}
			continue
		}
		k := keyOf(l)
		c := sh.counters[k]
		switch l.Metric {
		case ConcurrentRequests:
			if c != nil && c.gauge > 0 {
				c.gauge--
			}
		case RequestsPerMinute:
			if release && c != nil {
				c.win.subSec(h.items[i].sec, now, 1)
			}
		case TokensPerMinute:
			delta := actual.Tokens - h.res.Amounts.Tokens
			if release {
				delta = -h.res.Amounts.Tokens
			}
			if delta == 0 {
				continue
			}
			if c == nil {
				// An empty counter may have been swept while this reservation was outstanding.
				if delta < 0 {
					continue
				}
				c = &counter{}
				sh.counters[k] = c
			}
			c.win.subSec(h.items[i].sec, now, -delta)
		}
	}
}

// Settle replaces the reservation by what was consumed and frees the slot.
func (m *Memory) Settle(_ context.Context, res Reservation, actual Amounts) error {
	if len(res.Limits) == 0 {
		return nil
	}
	sh := m.shardOf(res.Limits[0].Tenant)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	now := m.now()
	h, ok := sh.take(res.ID)
	if !ok {
		return ErrNotOutstanding
	}
	actual.Tokens = max(actual.Tokens, 0)
	m.finish(sh, h, now, actual, false)
	return nil
}

// Release returns everything the reservation took.
func (m *Memory) Release(_ context.Context, res Reservation) error {
	if len(res.Limits) == 0 {
		return nil
	}
	sh := m.shardOf(res.Limits[0].Tenant)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	now := m.now()
	h, ok := sh.take(res.ID)
	if !ok {
		return ErrNotOutstanding
	}
	m.finish(sh, h, now, Amounts{}, true)
	return nil
}

// Run calls Sweep every second until ctx is done.
func (m *Memory) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Sweep(m.now())
		}
	}
}

const (
	// idlePeriodCounter is how long a period counter that nothing touched and that has nothing
	// reserved is kept; it is loaded again on next use.
	idlePeriodCounter = time.Hour
	// cutGrace is how long a cut without period counters is kept: the time between the moment a
	// loader gives a tenant its cut and the moment it creates the counters.
	cutGrace = time.Minute
)

// Sweep releases expired reservations and drops counters that are not needed any more; it
// returns the number expired.
func (m *Memory) Sweep(now time.Time) int {
	type expired struct {
		requestID string
		tenant    tenant.ID
	}
	var all []expired
	for i := range m.shards {
		sh := &m.shards[i]
		sh.mu.Lock()
		for id, h := range sh.held {
			if now.Before(h.res.Expires) {
				continue
			}
			delete(sh.held, id)
			m.finish(sh, h, now, Amounts{}, true)
			all = append(all, expired{h.requestID, h.res.Limits[0].Tenant})
		}
		for k, c := range sh.counters {
			if c.gauge == 0 && c.win.count(now) == 0 {
				delete(sh.counters, k)
			}
		}
		for t, ts := range sh.ten {
			for k, c := range ts.pcs {
				if c.per.droppable(ts.cut, now, m.horizon) {
					delete(ts.pcs, k)
				}
			}
			if len(ts.pcs) == 0 && now.Sub(ts.setAt) >= cutGrace {
				delete(sh.ten, t)
			}
		}
		sh.mu.Unlock()
	}
	for _, e := range all {
		m.log.Error("limit reservation expired", "request_id", e.requestID)
	}
	m.expired.Add(int64(len(all)))
	return len(all)
}

// MemoryStats is a snapshot of the counters of a Memory.
type MemoryStats struct{ Expired int64 }

// Stats returns the counters: the reservations the sweep expired.
func (m *Memory) Stats() MemoryStats { return MemoryStats{Expired: m.expired.Load()} }

// Outstanding returns the number of reservations not yet settled, released or expired.
func (m *Memory) Outstanding() int {
	n := 0
	for i := range m.shards {
		sh := &m.shards[i]
		sh.mu.Lock()
		n += len(sh.held)
		sh.mu.Unlock()
	}
	return n
}

// Counters returns the number of counters held.
func (m *Memory) Counters() int {
	n := 0
	for i := range m.shards {
		sh := &m.shards[i]
		sh.mu.Lock()
		n += len(sh.counters)
		for _, ts := range sh.ten {
			n += len(ts.pcs)
		}
		sh.mu.Unlock()
	}
	return n
}
