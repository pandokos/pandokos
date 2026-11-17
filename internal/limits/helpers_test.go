package limits

import (
	"bytes"
	"context"
	"log/slog"
	"math/rand/v2"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

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
