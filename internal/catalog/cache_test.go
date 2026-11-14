package catalog

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/tenant"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

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

// fakeLoader is the loader the cache is driven by in T-380: it counts its calls, can take time
// (on the injected clock) and can fail.
type fakeLoader struct {
	clk       *clock
	snapshots atomic.Int64
	accesses  atomic.Int64
	took      time.Duration // advances the clock during each load
	failNext  atomic.Int64  // loads that fail
	mu        sync.Mutex
	perTenant map[tenant.ID]int
}

func (f *fakeLoader) snapshot(context.Context) (*Snapshot, error) {
	f.snapshots.Add(1)
	if f.failNext.Add(-1) >= 0 {
		return nil, errors.New("synthetic load failure")
	}
	f.clk.advance(f.took)
	return synthetic(), nil
}

func (f *fakeLoader) access(_ context.Context, id tenant.ID) (TenantAccess, error) {
	f.accesses.Add(1)
	f.mu.Lock()
	if f.perTenant == nil {
		f.perTenant = map[tenant.ID]int{}
	}
	f.perTenant[id]++
	f.mu.Unlock()
	if f.failNext.Add(-1) >= 0 {
		return TenantAccess{}, errors.New("synthetic load failure")
	}
	f.clk.advance(f.took)
	return TenantAccess{Granted: map[string]bool{"m-sel": true}}, nil
}

func (f *fakeLoader) loadsOf(id tenant.ID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.perTenant[id]
}

func newTestCache(t *testing.T, ttl time.Duration) (*Cache, *fakeLoader, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)}
	l := &fakeLoader{clk: clk}
	c, err := newCache(l, CacheOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), TTL: ttl, Now: clk.now})
	if err != nil {
		t.Fatal(err)
	}
	return c, l, clk
}

// TestCache is T-380: the gateway cache over an injected clock and a fake loader.
func TestCache(t *testing.T) {
	ctx := context.Background()
	a, b := tid(1), tid(2)

	t.Run("a snapshot lives for the lifetime from the start of its load", func(t *testing.T) {
		c, l, clk := newTestCache(t, 30*time.Second)
		l.took = 10 * time.Second // the load takes ten seconds
		for i := 0; i < 3; i++ {
			if _, err := c.Snapshot(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if n := l.snapshots.Load(); n != 1 {
			t.Fatalf("%d loads for three calls", n)
		}
		clk.advance(20*time.Second - time.Nanosecond) // 30 s - 1 ns after the start of the load
		_, _ = c.Snapshot(ctx)
		if n := l.snapshots.Load(); n != 1 {
			t.Fatalf("a reload %d before the lifetime from the start ended", n)
		}
		clk.advance(time.Nanosecond)
		_, _ = c.Snapshot(ctx)
		if n := l.snapshots.Load(); n != 2 {
			t.Fatalf("%d loads: the entry outlived start + lifetime (stamped at the end of the load?)", n)
		}
	})

	t.Run("access entries live for the lifetime too", func(t *testing.T) {
		c, l, clk := newTestCache(t, 30*time.Second)
		l.took = 10 * time.Second
		for i := 0; i < 3; i++ {
			if _, err := c.Access(ctx, a); err != nil {
				t.Fatal(err)
			}
		}
		if n := l.loadsOf(a); n != 1 {
			t.Fatalf("%d loads for three calls", n)
		}
		clk.advance(20*time.Second - time.Nanosecond)
		_, _ = c.Access(ctx, a)
		clk.advance(time.Nanosecond)
		_, _ = c.Access(ctx, a)
		if n := l.loadsOf(a); n != 2 {
			t.Fatalf("%d loads, want 2", n)
		}
	})

	t.Run("access is per tenant", func(t *testing.T) {
		c, l, _ := newTestCache(t, time.Minute)
		ta, err := c.Access(ctx, a)
		if err != nil || !ta.Granted["m-sel"] {
			t.Fatalf("%+v %v", ta, err)
		}
		_, _ = c.Access(ctx, b)
		_, _ = c.Access(ctx, a)
		_, _ = c.Access(ctx, b)
		if l.loadsOf(a) != 1 || l.loadsOf(b) != 1 {
			t.Errorf("loads: a %d, b %d", l.loadsOf(a), l.loadsOf(b))
		}
	})

	t.Run("catalog reloads the snapshot and nothing else", func(t *testing.T) {
		c, l, _ := newTestCache(t, time.Minute)
		_, _ = c.Snapshot(ctx)
		_, _ = c.Access(ctx, a)
		c.Invalidate("catalog")
		_, _ = c.Access(ctx, a)
		if l.loadsOf(a) != 1 {
			t.Errorf("the payload catalog dropped an access entry")
		}
		if n := l.snapshots.Load(); n != 1 {
			t.Errorf("the snapshot was reloaded eagerly: %d loads", n)
		}
		_, _ = c.Snapshot(ctx)
		if n := l.snapshots.Load(); n != 2 {
			t.Errorf("%d snapshot loads after catalog, want 2", n)
		}
		_, _ = c.Snapshot(ctx)
		if n := l.snapshots.Load(); n != 2 {
			t.Errorf("the fresh snapshot is not kept: %d loads", n)
		}
	})

	for _, prefix := range []string{"catalog:", "tenant:"} {
		t.Run(prefix+" drops one tenant", func(t *testing.T) {
			c, l, _ := newTestCache(t, time.Minute)
			_, _ = c.Snapshot(ctx)
			_, _ = c.Access(ctx, a)
			_, _ = c.Access(ctx, b)
			c.Invalidate(prefix + a.String())
			_, _ = c.Access(ctx, a)
			_, _ = c.Access(ctx, b)
			_, _ = c.Snapshot(ctx)
			if l.loadsOf(a) != 2 {
				t.Errorf("tenant a was loaded %d times, want 2", l.loadsOf(a))
			}
			if l.loadsOf(b) != 1 {
				t.Errorf("tenant b was loaded %d times, want 1: another tenant's entry was dropped", l.loadsOf(b))
			}
			if n := l.snapshots.Load(); n != 1 {
				t.Errorf("%s dropped the snapshot: %d loads", prefix, n)
			}
		})
	}

	t.Run("payloads that touch nothing", func(t *testing.T) {
		c, l, _ := newTestCache(t, time.Minute)
		_, _ = c.Snapshot(ctx)
		_, _ = c.Access(ctx, a)
		for _, p := range []string{"vault:" + a.String(), "vault:", "platform", "probe:0123456789abcdef", "probe:"} {
			c.Invalidate(p)
		}
		_, _ = c.Snapshot(ctx)
		_, _ = c.Access(ctx, a)
		if l.snapshots.Load() != 1 || l.loadsOf(a) != 1 {
			t.Errorf("snapshot loads %d, access loads %d: a payload that names nothing of the catalogue dropped an entry", l.snapshots.Load(), l.loadsOf(a))
		}
	})

	t.Run("an unknown payload and Flush drop everything", func(t *testing.T) {
		for _, how := range []string{"unknown", "empty", "malformed catalog tenant", "flush"} {
			c, l, _ := newTestCache(t, time.Minute)
			_, _ = c.Snapshot(ctx)
			_, _ = c.Access(ctx, a)
			_, _ = c.Access(ctx, b)
			switch how {
			case "unknown":
				c.Invalidate("something:else")
			case "empty":
				c.Invalidate("")
			case "malformed catalog tenant":
				c.Invalidate("catalog:") // names nobody: forward compatibility says flush
			case "flush":
				c.Flush()
			}
			_, _ = c.Snapshot(ctx)
			_, _ = c.Access(ctx, a)
			_, _ = c.Access(ctx, b)
			if l.snapshots.Load() != 2 || l.loadsOf(a) != 2 || l.loadsOf(b) != 2 {
				t.Errorf("%s: snapshot loads %d, tenant loads %d/%d, want 2/2/2", how, l.snapshots.Load(), l.loadsOf(a), l.loadsOf(b))
			}
		}
	})

	t.Run("a full map is emptied", func(t *testing.T) {
		c, l, _ := newTestCache(t, time.Minute)
		ids := make([]tenant.ID, maxTenantEntries+1)
		for i := range ids {
			ids[i][0], ids[i][1], ids[i][6], ids[i][8] = byte(i>>8), byte(i), 0x70, 0x80
			ids[i][15] = 1
		}
		for _, id := range ids[:maxTenantEntries] {
			if _, err := c.Access(ctx, id); err != nil {
				t.Fatal(err)
			}
		}
		_, _ = c.Access(ctx, ids[0]) // still cached while the map is not over its bound
		if l.loadsOf(ids[0]) != 1 {
			t.Fatalf("an entry of a map that is not full was dropped")
		}
		_, _ = c.Access(ctx, ids[maxTenantEntries]) // the 10001st: the map is emptied first
		_, _ = c.Access(ctx, ids[0])
		if l.loadsOf(ids[0]) != 2 {
			t.Errorf("the map was not emptied when it was full: %d loads of the first tenant", l.loadsOf(ids[0]))
		}
		before := l.loadsOf(ids[maxTenantEntries])
		_, _ = c.Access(ctx, ids[maxTenantEntries])
		if l.loadsOf(ids[maxTenantEntries]) != before {
			t.Error("the entry that triggered the flush was not kept")
		}
	})

	t.Run("failures are not cached", func(t *testing.T) {
		c, l, _ := newTestCache(t, time.Minute)
		l.failNext.Store(1)
		if _, err := c.Snapshot(ctx); err == nil {
			t.Fatal("a failed load returned no error")
		}
		if _, err := c.Snapshot(ctx); err != nil {
			t.Fatalf("the second call: %v", err)
		}
		if l.snapshots.Load() != 2 {
			t.Errorf("%d loads, want 2", l.snapshots.Load())
		}
		l.failNext.Store(1)
		if _, err := c.Access(ctx, a); err == nil {
			t.Fatal("a failed access load returned no error")
		}
		if _, err := c.Access(ctx, a); err != nil {
			t.Fatalf("the second access call: %v", err)
		}
		if l.loadsOf(a) != 2 {
			t.Errorf("%d access loads, want 2", l.loadsOf(a))
		}
	})

	t.Run("Resolve is Snapshot, Access and Snapshot.Resolve", func(t *testing.T) {
		c, l, _ := newTestCache(t, time.Minute)
		p := authz.Principal{Kind: authz.KindServiceAccount, ID: "sa", TenantID: a, TenantState: tenant.StateActive, Roles: []string{authz.RoleInference}}
		m, e, err := c.Resolve(ctx, p, "p-open", "sel-model") // granted by the fake's access
		if err != nil || m.ID != "m-sel" || e.Name != "main" {
			t.Fatalf("%+v %+v %v", m, e, err)
		}
		if _, _, err := c.Resolve(ctx, p, "p-open", "sel-model-2"); !errors.Is(err, ErrModelNotAllowed) {
			t.Errorf("a model that is not granted: %v", err)
		}
		if _, _, err := c.Resolve(ctx, p, "p-off", "off-model"); !errors.Is(err, ErrProviderUnavailable) {
			t.Errorf("a disabled provider: %v", err)
		}
		if l.snapshots.Load() != 1 || l.loadsOf(a) != 1 {
			t.Errorf("three resolutions made %d snapshot and %d access loads", l.snapshots.Load(), l.loadsOf(a))
		}
		// A failing load is an error that is neither refusal: the caller must not take it for "not allowed".
		c.Flush()
		l.failNext.Store(1)
		if _, _, err := c.Resolve(ctx, p, "p-open", "all-model"); err == nil || errors.Is(err, ErrModelNotAllowed) || errors.Is(err, ErrProviderUnavailable) {
			t.Errorf("a failed load: %v", err)
		}
		// A platform principal never loads a tenant's access.
		pp := authz.Principal{Kind: authz.KindPlatform, ID: "pk", Roles: []string{authz.RolePlatformAdmin}}
		before := l.accesses.Load()
		if _, _, err := c.Resolve(ctx, pp, "p-open", "all-model"); !errors.Is(err, ErrModelNotAllowed) {
			t.Errorf("a platform principal: %v", err)
		}
		if l.accesses.Load() != before {
			t.Error("an access load was made for a platform principal")
		}
	})

	t.Run("concurrent use is safe", func(t *testing.T) {
		c, _, _ := newTestCache(t, time.Minute)
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				for j := 0; j < 50; j++ {
					_, _ = c.Snapshot(ctx)
					_, _ = c.Access(ctx, tid(byte(i%3+1)))
					switch j % 5 {
					case 0:
						c.Invalidate("catalog")
					case 1:
						c.Invalidate("catalog:" + tid(byte(i%3+1)).String())
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
		l := &fakeLoader{clk: &clock{}}
		for _, ttl := range []time.Duration{0, time.Second - 1, 5*time.Minute + 1, -time.Second} {
			if _, err := newCache(l, CacheOptions{Logger: quiet, TTL: ttl}); err == nil {
				t.Errorf("TTL %v accepted", ttl)
			}
		}
		for _, ttl := range []time.Duration{time.Second, 30 * time.Second, 5 * time.Minute} {
			if _, err := newCache(l, CacheOptions{Logger: quiet, TTL: ttl}); err != nil {
				t.Errorf("TTL %v: %v", ttl, err)
			}
		}
		if _, err := newCache(l, CacheOptions{TTL: time.Minute}); err == nil {
			t.Error("no logger accepted")
		}
		if _, err := NewCache(CacheOptions{Logger: quiet, TTL: time.Minute}); err == nil {
			t.Error("NewCache without a database accepted")
		}
	})
}
