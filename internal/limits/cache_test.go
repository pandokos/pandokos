package limits

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

	"github.com/pandokos/pandokos/internal/tenant"
)

// ruleLoader is the fake loader of T-623: it counts its calls per tenant, takes time on the fake
// clock, can fail and can wait for a gate.
type ruleLoader struct {
	clk      *clock
	loads    atomic.Int64
	took     time.Duration
	failNext atomic.Int64
	started  chan struct{} // when set, receives one value per load that starts
	gate     chan struct{} // when set, each load waits for it to be closed
	mu       sync.Mutex
	per      map[tenant.ID]int
}

func (l *ruleLoader) load(ctx context.Context, id tenant.ID) (RuleSet, error) {
	l.loads.Add(1)
	l.mu.Lock()
	if l.per == nil {
		l.per = map[tenant.ID]int{}
	}
	l.per[id]++
	n := int64(l.per[id])
	l.mu.Unlock()
	if l.started != nil {
		l.started <- struct{}{}
	}
	if l.gate != nil {
		select {
		case <-l.gate:
		case <-ctx.Done():
			return RuleSet{}, ctx.Err()
		}
	}
	if l.failNext.Add(-1) >= 0 {
		return RuleSet{}, errors.New("synthetic load failure")
	}
	l.clk.advance(l.took)
	return NewRuleSet(id, nil, nil, []Rule{{SubjectTenant, "", RequestsPerMinute, n}}), nil
}

func (l *ruleLoader) loadsOf(id tenant.ID) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.per[id]
}

func newTestRuleCache(t *testing.T, ttl time.Duration) (*RuleCache, *ruleLoader, *clock) {
	t.Helper()
	clk := newClock()
	l := &ruleLoader{clk: clk}
	c, err := newRuleCache(l.load, RuleCacheOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), TTL: ttl, Now: clk.now})
	if err != nil {
		t.Fatal(err)
	}
	return c, l, clk
}

// version returns the number of the load that produced the rule set.
func version(t *testing.T, s RuleSet) int64 {
	t.Helper()
	got := s.For("", "")
	if len(got) != 1 {
		t.Fatalf("rule set %+v: %d limits, want 1", s, len(got))
	}
	return got[0].Max
}

// TestRuleCache is T-623.
func TestRuleCache(t *testing.T) {
	ctx := context.Background()
	a, b := tid(1), tid(2)

	t.Run("an entry lives for the lifetime from the start of its load", func(t *testing.T) {
		c, l, clk := newTestRuleCache(t, 30*time.Second)
		l.took = 10 * time.Second
		for i := 0; i < 3; i++ {
			if _, err := c.Rules(ctx, a); err != nil {
				t.Fatal(err)
			}
		}
		if n := l.loadsOf(a); n != 1 {
			t.Fatalf("%d loads for three calls", n)
		}
		clk.advance(20*time.Second - time.Nanosecond)
		_, _ = c.Rules(ctx, a)
		if n := l.loadsOf(a); n != 1 {
			t.Fatalf("a reload before the lifetime from the start ended: %d", n)
		}
		clk.advance(time.Nanosecond)
		_, _ = c.Rules(ctx, a)
		if n := l.loadsOf(a); n != 2 {
			t.Fatalf("%d loads: the entry outlived start + lifetime", n)
		}
	})

	t.Run("a tenant has its own entry and the loader gets its ID", func(t *testing.T) {
		c, l, _ := newTestRuleCache(t, time.Minute)
		ra, err := c.Rules(ctx, a)
		if err != nil || ra.Tenant != a {
			t.Fatalf("%+v %v", ra, err)
		}
		rb, _ := c.Rules(ctx, b)
		_, _ = c.Rules(ctx, a)
		_, _ = c.Rules(ctx, b)
		if rb.Tenant != b || l.loadsOf(a) != 1 || l.loadsOf(b) != 1 {
			t.Errorf("loads: a %d, b %d, tenant of b's entry %s", l.loadsOf(a), l.loadsOf(b), rb.Tenant)
		}
	})

	t.Run("single flight", func(t *testing.T) {
		c, l, _ := newTestRuleCache(t, time.Minute)
		l.gate = make(chan struct{})
		l.started = make(chan struct{}, 16)
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := c.Rules(ctx, a); err != nil {
					t.Error(err)
				}
			}()
		}
		select {
		case <-l.started:
		case <-time.After(2 * time.Second):
			t.Fatal("no load started")
		}
		time.Sleep(50 * time.Millisecond) // let the others queue behind the load
		close(l.gate)
		wg.Wait()
		if n := l.loadsOf(a); n != 1 {
			t.Fatalf("%d loads for 8 concurrent callers, want 1", n)
		}
	})

	t.Run("a failed load is not cached", func(t *testing.T) {
		c, l, _ := newTestRuleCache(t, time.Minute)
		l.failNext.Store(1)
		if _, err := c.Rules(ctx, a); err == nil {
			t.Fatal("a failed load returned no error")
		}
		if _, err := c.Rules(ctx, a); err != nil {
			t.Fatalf("the failure was cached: %v", err)
		}
		if n := l.loadsOf(a); n != 2 {
			t.Fatalf("%d loads, want 2", n)
		}
	})

	t.Run("a load outdated by an invalidation is discarded", func(t *testing.T) {
		for _, payload := range []string{"limits:" + a.String(), "tenant:" + a.String(), "limits", "unknown:thing"} {
			c, l, _ := newTestRuleCache(t, time.Minute)
			l.gate = make(chan struct{})
			l.started = make(chan struct{}, 4)
			done := make(chan RuleSet, 1)
			go func() {
				r, _ := c.Rules(ctx, a)
				done <- r
			}()
			select {
			case <-l.started:
			case <-time.After(2 * time.Second):
				t.Fatalf("payload %q: no load started", payload)
			}
			c.Invalidate(payload) // arrives while the load runs
			close(l.gate)
			<-done
			l.gate, l.started = nil, nil
			r, err := c.Rules(ctx, a)
			if err != nil {
				t.Fatal(err)
			}
			if n := l.loadsOf(a); n != 2 || version(t, r) != 2 {
				t.Fatalf("payload %q: %d loads, version %d: the outdated load was kept", payload, n, version(t, r))
			}
		}
	})

	for _, prefix := range []string{"limits:", "tenant:"} {
		t.Run(prefix+" drops one tenant", func(t *testing.T) {
			c, l, _ := newTestRuleCache(t, time.Minute)
			_, _ = c.Rules(ctx, a)
			_, _ = c.Rules(ctx, b)
			c.Invalidate(prefix + a.String())
			ra, _ := c.Rules(ctx, a)
			_, _ = c.Rules(ctx, b)
			if l.loadsOf(a) != 2 || l.loadsOf(b) != 1 || version(t, ra) != 2 {
				t.Errorf("loads: a %d, b %d", l.loadsOf(a), l.loadsOf(b))
			}
		})
	}

	t.Run("limits drops every tenant", func(t *testing.T) {
		c, l, _ := newTestRuleCache(t, time.Minute)
		_, _ = c.Rules(ctx, a)
		_, _ = c.Rules(ctx, b)
		c.Invalidate("limits")
		_, _ = c.Rules(ctx, a)
		_, _ = c.Rules(ctx, b)
		if l.loadsOf(a) != 2 || l.loadsOf(b) != 2 {
			t.Errorf("loads: a %d, b %d", l.loadsOf(a), l.loadsOf(b))
		}
	})

	t.Run("payloads of the other caches touch nothing", func(t *testing.T) {
		c, l, _ := newTestRuleCache(t, time.Minute)
		_, _ = c.Rules(ctx, a)
		for _, p := range []string{"catalog", "catalog:" + a.String(), "platform", "issuers", "probe:x", "vault:" + a.String()} {
			c.Invalidate(p)
		}
		_, _ = c.Rules(ctx, a)
		if n := l.loadsOf(a); n != 1 {
			t.Errorf("%d loads: a foreign payload dropped the entry", n)
		}
	})

	t.Run("an unknown payload and Flush drop everything", func(t *testing.T) {
		for name, drop := range map[string]func(*RuleCache){
			"unknown":      func(c *RuleCache) { c.Invalidate("something:else") },
			"empty":        func(c *RuleCache) { c.Invalidate("") },
			"limits: bare": func(c *RuleCache) { c.Invalidate("limits:") },
			"bad tenant":   func(c *RuleCache) { c.Invalidate("limits:not-a-uuid") },
			"flush":        func(c *RuleCache) { c.Flush() },
		} {
			c, l, _ := newTestRuleCache(t, time.Minute)
			_, _ = c.Rules(ctx, a)
			_, _ = c.Rules(ctx, b)
			drop(c)
			_, _ = c.Rules(ctx, a)
			_, _ = c.Rules(ctx, b)
			if l.loadsOf(a) != 2 || l.loadsOf(b) != 2 {
				t.Errorf("%s: loads a %d, b %d, want 2 each", name, l.loadsOf(a), l.loadsOf(b))
			}
		}
	})

	t.Run("concurrent use is safe", func(t *testing.T) {
		c, _, _ := newTestRuleCache(t, time.Minute)
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				for j := 0; j < 50; j++ {
					_, _ = c.Rules(ctx, tid(byte(i%3+1)))
					switch j % 5 {
					case 0:
						c.Invalidate("limits")
					case 1:
						c.Invalidate("limits:" + tid(byte(i%3+1)).String())
					case 2:
						c.Flush()
					}
				}
			}(i)
		}
		wg.Wait()
	})

	t.Run("construction", func(t *testing.T) {
		quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
		l := &ruleLoader{clk: newClock()}
		for _, ttl := range []time.Duration{0, time.Second - 1, 5*time.Minute + 1, -time.Second} {
			if _, err := newRuleCache(l.load, RuleCacheOptions{Logger: quiet, TTL: ttl}); err == nil {
				t.Errorf("TTL %v accepted", ttl)
			}
		}
		for _, ttl := range []time.Duration{time.Second, 30 * time.Second, 5 * time.Minute} {
			if _, err := newRuleCache(l.load, RuleCacheOptions{Logger: quiet, TTL: ttl}); err != nil {
				t.Errorf("TTL %v: %v", ttl, err)
			}
		}
		if _, err := newRuleCache(l.load, RuleCacheOptions{TTL: time.Minute}); err == nil {
			t.Error("no logger accepted")
		}
		if _, err := NewRuleCache(RuleCacheOptions{Logger: quiet, TTL: time.Minute}); err == nil {
			t.Error("NewRuleCache without a database accepted")
		}
	})
}

// TestSkippedRulesLogged: the number of rules a load skipped is logged once, at debug level,
// through the cache's own logger.
func TestSkippedRulesLogged(t *testing.T) {
	l, buf := newLogger()
	a := tid(1)
	skipped := 2
	c, err := newCountingRuleCache(func(context.Context, tenant.ID) (RuleSet, int, error) {
		return NewRuleSet(a, nil, nil, nil), skipped, nil
	}, RuleCacheOptions{Logger: l, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Rules(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if n := strings.Count(out, "skipped"); n < 1 || !strings.Contains(out, `"skipped":2`) || !strings.Contains(out, `"level":"DEBUG"`) {
		t.Errorf("log %q, want one debug line with skipped=2", out)
	}
	skipped = 0
	c.Flush()
	buf.buf.Reset()
	if _, err := c.Rules(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "skipped") {
		t.Errorf("a load that skipped nothing logged %q", buf.String())
	}
}
