package limits_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// watchedLedger wraps the reader of the database: it logs every call and can fail, or block.
type watchedLedger struct {
	inner limits.Ledger
	mu    sync.Mutex
	calls []watchedCall
	fail  error
	block chan struct{} // when set, a call waits for it (or for its context)
}

type watchedCall struct {
	tenant             tenant.ID
	from, liveFrom, to time.Time
}

func (w *watchedLedger) Usage(ctx context.Context, t tenant.ID, from, liveFrom, to time.Time) ([]limits.UsageGroup, error) {
	w.mu.Lock()
	w.calls = append(w.calls, watchedCall{t, from, liveFrom, to})
	fail, block := w.fail, w.block
	w.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if fail != nil {
		return nil, fail
	}
	return w.inner.Usage(ctx, t, from, liveFrom, to)
}

func (w *watchedLedger) setFail(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.fail = err
}

func (w *watchedLedger) n() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.calls)
}

func (w *watchedLedger) callsSince(i int) []watchedCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]watchedCall(nil), w.calls[i:]...)
}

type reconcilerFixture struct {
	clk    *fakeNow
	mem    *limits.Memory
	led    *watchedLedger
	rec    *limits.Reconciler
	cal    *limits.Calendar
	logs   *safeBuf
	oldest atomic.Pointer[time.Time]
}

type safeBuf struct {
	mu sync.Mutex
	b  []byte
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b = append(s.b, p...)
	return len(p), nil
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.b)
}

func newReconcilerFixture(t *testing.T, d *dbtest.Database, interval time.Duration) *reconcilerFixture {
	t.Helper()
	f := &reconcilerFixture{clk: newFakeNow(utc(2026, 11, 17, 12, 0, 0)), logs: &safeBuf{}}
	logger := slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var err error
	f.cal, err = limits.NewCalendar(time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	f.mem, err = limits.NewMemory(limits.MemoryOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: f.clk.now})
	if err != nil {
		t.Fatal(err)
	}
	f.led = &watchedLedger{inner: limits.NewLedger(d.Open(t, store.RoleWorker))}
	f.rec, err = limits.NewReconciler(limits.ReconcilerOptions{
		Store: f.mem, Ledger: f.led, Horizon: 12*time.Minute + 30*time.Second, Interval: interval,
		Logger: logger, Now: f.clk.now,
		Pending: func() (time.Time, bool) {
			if p := f.oldest.Load(); p != nil {
				return *p, true
			}
			return time.Time{}, false
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *reconcilerFixture) limit(tn tenant.ID, m limits.Metric, kind limits.PeriodKind) limits.Limit {
	return limits.Limit{Tenant: tn, Subject: limits.SubjectTenant, Metric: m, Max: 1_000_000, Level: limits.LevelTenant,
		RuleID: "00000000-0000-4000-8000-000000000001", Period: f.cal.Of(kind, f.clk.now())}
}

// TestReconciler is T-659.
func TestReconciler(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		a, b := d.SeedTenant(t, "rec-a"), d.SeedTenant(t, "rec-b")
		d.SeedUsage(t, a,
			dbtest.UsageRow{StartedAt: utc(2026, 11, 3, 9, 0, 0), InputTokens: 100, OutputTokens: 20},
			dbtest.UsageRow{StartedAt: utc(2026, 11, 17, 11, 50, 0), InputTokens: 5},
			dbtest.UsageRow{StartedAt: utc(2026, 11, 17, 11, 59, 0), InputTokens: 6}, // after the cut: seeds the live part
		)
		d.SeedUsage(t, b, dbtest.UsageRow{StartedAt: utc(2026, 11, 3, 9, 0, 0), InputTokens: 70_000})

		t.Run("ensure: one load for concurrent callers, in the session of the tenant", func(t *testing.T) {
			f := newReconcilerFixture(t, d, time.Minute)
			hold := make(chan struct{})
			f.led.block = hold
			l := f.limit(a, limits.TokensPerMonth, limits.PerMonth)
			var wg sync.WaitGroup
			errs := make(chan error, 16)
			for range 16 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs <- f.rec.Ensure(ctx, a, []limits.Limit{l})
				}()
			}
			testutil.Eventually(t, 2*time.Second, "the first load to reach the ledger", func() bool { return f.led.n() >= 1 })
			close(hold)
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Errorf("ensure: %v", err)
				}
			}
			if n := f.led.n(); n != 1 {
				t.Errorf("%d ledger queries for 16 concurrent callers, want 1", n)
			}
			if c, ok := f.mem.Counted(l); !ok || c.N != 131 {
				t.Errorf("counted %v %v, want 131 (the rows of tenant A only: 100+20 before the cut, 5 and 6 after)", c, ok)
			}
			for _, c := range f.led.callsSince(0) {
				if c.tenant != a {
					t.Errorf("a query for %v while loading %v", c.tenant, a)
				}
			}
			if cut, ok := f.mem.Cut(a); !ok || !cut.Equal(utc(2026, 11, 17, 11, 47, 0)) {
				t.Errorf("cut %v %v", cut, ok)
			}
		})

		t.Run("ensure: a restart just after midnight counts the day from the ledger", func(t *testing.T) {
			c := d.SeedTenant(t, "rec-midnight")
			d.SeedUsage(t, c,
				dbtest.UsageRow{StartedAt: utc(2026, 11, 17, 0, 1, 0), InputTokens: 4},
				dbtest.UsageRow{StartedAt: utc(2026, 11, 17, 0, 1, 30), InputTokens: 6},
			)
			f := newReconcilerFixture(t, d, time.Minute)
			f.clk.advance(-(11*time.Hour + 55*time.Minute)) // 00:05
			l := f.limit(c, limits.RequestsPerDay, limits.PerDay)
			l.Max = 2
			if err := f.rec.Ensure(ctx, c, []limits.Limit{l}); err != nil {
				t.Fatal(err)
			}
			if got := mustCounted(t, f.mem, l); got.N != 2 {
				t.Fatalf("the day counts %d, the ledger holds 2", got.N)
			}
			_, ref, err := f.mem.Reserve(ctx, limits.Request{ID: "r", Limits: []limits.Limit{l}, Start: f.clk.now(), TTL: time.Minute})
			if err != nil || ref == nil {
				t.Fatalf("reserve after the restart: refusal %v, error %v; want a refusal", ref, err)
			}
		})

		t.Run("ensure: nothing is cached after a failure", func(t *testing.T) {
			f := newReconcilerFixture(t, d, time.Minute)
			l := f.limit(a, limits.TokensPerMonth, limits.PerMonth)
			f.led.setFail(errors.New("ledger down"))
			if err := f.rec.Ensure(ctx, a, []limits.Limit{l}); err == nil {
				t.Fatal("ensure succeeded with a failing ledger")
			}
			if miss := f.mem.Missing([]limits.Limit{l}); len(miss) != 1 {
				t.Errorf("missing %+v after a failed load, want the counter still missing", miss)
			}
			if _, ok := f.mem.Counted(l); ok {
				t.Error("a failed load left a counter")
			}
			f.led.setFail(nil)
			before := f.led.n()
			if err := f.rec.Ensure(ctx, a, []limits.Limit{l}); err != nil {
				t.Fatalf("the next ensure: %v", err)
			}
			if f.led.n() == before {
				t.Error("the next ensure did not try again")
			}
			if c := mustCounted(t, f.mem, l); c.N != 131 {
				t.Errorf("counted %d, want 131", c.N)
			}
		})

		t.Run("ensure: a caller that gives up does not wait for the load", func(t *testing.T) {
			f := newReconcilerFixture(t, d, time.Minute)
			f.led.block = make(chan struct{}) // never released
			l := f.limit(a, limits.TokensPerMonth, limits.PerMonth)
			short, stop := context.WithTimeout(ctx, 150*time.Millisecond)
			defer stop()
			start := time.Now()
			err := f.rec.Ensure(short, a, []limits.Limit{l})
			if err == nil {
				t.Fatal("ensure succeeded against a ledger that never answers")
			}
			if time.Since(start) > 2*time.Second {
				t.Errorf("ensure waited %v: it must honour its context (and wait at most 5 s without one)", time.Since(start))
			}
		})

		t.Run("ensure: nothing to load, no query", func(t *testing.T) {
			f := newReconcilerFixture(t, d, time.Minute)
			rate := limits.Limit{Tenant: a, Subject: limits.SubjectTenant, Metric: limits.RequestsPerMinute, Max: 5, Level: limits.LevelTenant}
			if err := f.rec.Ensure(ctx, a, []limits.Limit{rate}); err != nil {
				t.Fatal(err)
			}
			if err := f.rec.Ensure(ctx, a, nil); err != nil {
				t.Fatal(err)
			}
			if n := f.led.n(); n != 0 {
				t.Errorf("%d queries for limits without a period", n)
			}
		})

		t.Run("tick: fold at the interval, rebase after an hour", func(t *testing.T) {
			f := newReconcilerFixture(t, d, time.Minute)
			l := f.limit(a, limits.TokensPerMonth, limits.PerMonth)
			if err := f.rec.Ensure(ctx, a, []limits.Limit{l}); err != nil {
				t.Fatal(err)
			}
			// A row that started after the load and committed since: the fold picks it up.
			d.SeedUsage(t, a, dbtest.UsageRow{StartedAt: utc(2026, 11, 17, 12, 1, 0), InputTokens: 1000})
			f.clk.advance(20 * time.Minute) // 12:20: the cut moves to 12:07
			f.rec.Tick(ctx)
			if cut, _ := f.mem.Cut(a); !cut.Equal(utc(2026, 11, 17, 12, 7, 0)) {
				t.Errorf("cut %v, want 12:07", cut)
			}
			if c := mustCounted(t, f.mem, l); c.N != 1131 {
				t.Errorf("counted %d after the fold, want 1131", c.N)
			}
			// A row the live part never held and that is old: only a rebase finds it.
			d.SeedUsage(t, a, dbtest.UsageRow{StartedAt: utc(2026, 11, 10, 8, 0, 0), InputTokens: 50_000})
			f.clk.advance(5 * time.Minute)
			f.rec.Tick(ctx)
			if c := mustCounted(t, f.mem, l); c.N != 1131 {
				t.Errorf("counted %d before an hour has passed, want 1131", c.N)
			}
			f.clk.advance(60 * time.Minute)
			f.rec.Tick(ctx)
			if c := mustCounted(t, f.mem, l); c.N != 51_131 {
				t.Errorf("counted %d after the rebase, want 51131", c.N)
			}
		})

		t.Run("tick: a failing ledger changes nothing and is retried", func(t *testing.T) {
			f := newReconcilerFixture(t, d, time.Minute)
			l := f.limit(a, limits.TokensPerMonth, limits.PerMonth)
			if err := f.rec.Ensure(ctx, a, []limits.Limit{l}); err != nil {
				t.Fatal(err)
			}
			cut0, _ := f.mem.Cut(a)
			before := mustCounted(t, f.mem, l)
			f.led.setFail(errors.New("ledger down"))
			f.clk.advance(30 * time.Minute)
			f.rec.Tick(ctx)
			if cut, _ := f.mem.Cut(a); !cut.Equal(cut0) {
				t.Errorf("cut moved to %v although the ledger failed", cut)
			}
			if c := mustCounted(t, f.mem, l); c.N != before.N {
				t.Errorf("counter changed from %v to %v on a failed fold", before, c)
			}
			if logs := f.logs.String(); !strings.Contains(logs, "limit reconciliation failed") {
				t.Errorf("no warning in the log: %s", logs)
			}
			f.led.setFail(nil)
			f.rec.Tick(ctx)
			if cut, _ := f.mem.Cut(a); !cut.After(cut0) {
				t.Errorf("cut %v: the fold was not retried", cut)
			}
		})

		t.Run("run: ticks at the interval and stops with its context", func(t *testing.T) {
			f := newReconcilerFixture(t, d, 20*time.Millisecond)
			l := f.limit(a, limits.TokensPerMonth, limits.PerMonth)
			if err := f.rec.Ensure(ctx, a, []limits.Limit{l}); err != nil {
				t.Fatal(err)
			}
			cut0, _ := f.mem.Cut(a)
			runCtx, stop := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { f.rec.Run(runCtx); close(done) }()
			f.clk.advance(30 * time.Minute)
			testutil.Eventually(t, 2*time.Second, "a fold by Run", func() bool {
				c, _ := f.mem.Cut(a)
				return c.After(cut0)
			})
			stop()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("Run did not return after its context was cancelled")
			}
		})

		t.Run("every query runs in the session of its tenant", func(t *testing.T) {
			f := newReconcilerFixture(t, d, time.Minute)
			// Tenant A of the sibling subtests has gained rows since the seed above: this one gets a
			// tenant with the original rows.
			a := d.SeedTenant(t, "rec-c")
			d.SeedUsage(t, a,
				dbtest.UsageRow{StartedAt: utc(2026, 11, 3, 9, 0, 0), InputTokens: 100, OutputTokens: 20},
				dbtest.UsageRow{StartedAt: utc(2026, 11, 17, 11, 50, 0), InputTokens: 5},
				dbtest.UsageRow{StartedAt: utc(2026, 11, 17, 11, 59, 0), InputTokens: 6},
			)
			la, lb := f.limit(a, limits.TokensPerMonth, limits.PerMonth), f.limit(b, limits.TokensPerMonth, limits.PerMonth)
			if err := f.rec.Ensure(ctx, a, []limits.Limit{la}); err != nil {
				t.Fatal(err)
			}
			if err := f.rec.Ensure(ctx, b, []limits.Limit{lb}); err != nil {
				t.Fatal(err)
			}
			f.clk.advance(30 * time.Minute)
			f.rec.Tick(ctx)
			if ca, cb := mustCounted(t, f.mem, la).N, mustCounted(t, f.mem, lb).N; ca != 131 || cb != 70_000 {
				t.Errorf("tenant A counts %d, tenant B %d, want 131 and 70000: a query crossed tenants", ca, cb)
			}
			for _, c := range f.led.callsSince(0) {
				if c.tenant != a && c.tenant != b {
					t.Errorf("query for %v", c.tenant)
				}
			}
			if ts := f.mem.PeriodTenants(); len(ts) != 2 {
				t.Errorf("tenants with a cut: %v", ts)
			}
		})
	})
}

func mustCounted(t *testing.T, m *limits.Memory, l limits.Limit) limits.Amount {
	t.Helper()
	c, ok := m.Counted(l)
	if !ok {
		t.Fatalf("no counter for %s", l.Metric)
	}
	return c
}
