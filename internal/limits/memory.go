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
}

type counterKey struct {
	tenant tenant.ID
	kind   SubjectKind
	id     string
	metric Metric
}

// counter is a window (requests, tokens) or a gauge (concurrency), by metric.
type counter struct {
	win   window
	gauge int64
}

// held is an outstanding reservation.
type held struct {
	res       Reservation // the stored copy is what settles, whatever the caller passes back
	requestID string
	secs      []int64 // per limit of res: the second its window attributed the amount to
}

type shard struct {
	mu       sync.Mutex
	counters map[counterKey]*counter
	held     map[string]*held
}

// Memory is the in-process Store.
type Memory struct {
	log    *slog.Logger
	now    func() time.Time
	events EventSink
	seq    atomic.Uint64
	shards [shardCount]shard
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
	m := &Memory{log: o.Logger, now: now, events: o.Events}
	for i := range m.shards {
		m.shards[i].counters = make(map[counterKey]*counter)
		m.shards[i].held = make(map[string]*held)
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
	return counterKey{tenant: l.Tenant, kind: l.Subject, id: l.ID, metric: l.Metric}
}

func amountOf(l Limit, est int64) int64 {
	if l.Metric == TokensPerMinute {
		return est
	}
	return 1
}

func validate(r Request) error {
	if r.Estimate.Tokens < 0 {
		return errors.New("limits: negative estimate")
	}
	if r.TTL <= 0 {
		return errors.New("limits: the reservation needs a positive TTL")
	}
	for i, l := range r.Limits {
		// Pairwise: a request has at most a handful of limits, and this does not allocate.
		for _, e := range r.Limits[:i] {
			if keyOf(e) == keyOf(l) {
				return fmt.Errorf("limits: limit %d repeats the counter of an earlier limit", i)
			}
		}
		if l.Tenant != r.Limits[0].Tenant {
			return errors.New("limits: the limits of one request belong to one tenant")
		}
		switch l.Metric {
		case ConcurrentRequests, RequestsPerMinute, TokensPerMinute:
		default:
			return fmt.Errorf("limits: unknown metric %q", l.Metric)
		}
		if l.Max < 1 {
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

	sh.mu.Lock()
	defer sh.mu.Unlock()
	// The clock is read under the lock: admissions of one shard are attributed in the order
	// they take the lock (sprint.md 5.4).
	now := m.now()
	res.At, res.Expires = now, now.Add(r.TTL)

	var buf [12]*counter
	cs := buf[:0]
	var ref *Refusal
	var retry time.Duration
	for _, l := range res.Limits {
		c := sh.counters[keyOf(l)]
		cs = append(cs, c)
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
		var rt, reset time.Duration
		over := false
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
		if rt > retry {
			retry = rt
		}
		if ref == nil {
			ref = &Refusal{Limit: l, Remaining: max(l.Max-counted, 0), Reset: reset, OverLimit: over}
		}
	}
	if ref != nil {
		ref.RetryAfter = retry
		return Reservation{}, ref, nil
	}
	secs := make([]int64, len(res.Limits))
	for i, l := range res.Limits {
		c := cs[i]
		if c == nil {
			// Keys are distinct (validate), so no other limit of this request made it.
			c = &counter{}
			sh.counters[keyOf(l)] = c
		}
		if l.Metric == ConcurrentRequests {
			c.gauge++
		} else {
			secs[i] = c.win.add(now, amountOf(l, r.Estimate.Tokens))
		}
	}
	sh.held[id] = &held{res: res, requestID: r.ID, secs: secs}
	return res, nil, nil
}

// take removes the reservation from its shard; the caller holds the lock.
func (sh *shard) take(id string) (*held, bool) {
	h, ok := sh.held[id]
	if ok {
		delete(sh.held, id)
	}
	return h, ok
}

// finish applies the end of a reservation at now with the tokens it finally counts; the caller
// holds the lock. A released reservation takes everything back.
func (sh *shard) finish(h *held, now time.Time, actual int64, release bool) {
	for i, l := range h.res.Limits {
		k := keyOf(l)
		c := sh.counters[k]
		switch l.Metric {
		case ConcurrentRequests:
			if c != nil && c.gauge > 0 {
				c.gauge--
			}
		case RequestsPerMinute:
			if release && c != nil {
				c.win.subSec(h.secs[i], now, 1)
			}
		case TokensPerMinute:
			delta := actual - h.res.Amounts.Tokens
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
			c.win.subSec(h.secs[i], now, -delta)
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
	sh.finish(h, now, max(actual.Tokens, 0), false)
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
	sh.finish(h, now, 0, true)
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

// Sweep releases expired reservations and drops empty counters; it returns the number expired.
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
			sh.finish(h, now, 0, true)
			all = append(all, expired{h.requestID, h.res.Limits[0].Tenant})
		}
		for k, c := range sh.counters {
			if c.gauge == 0 && c.win.count(now) == 0 {
				delete(sh.counters, k)
			}
		}
		sh.mu.Unlock()
	}
	for _, e := range all {
		m.log.Error("limit reservation expired", "request_id", e.requestID, "tenant_id", e.tenant.String())
	}
	return len(all)
}

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
		sh.mu.Unlock()
	}
	return n
}
