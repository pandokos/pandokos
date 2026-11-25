package catalog

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/tenant"
)

// priceLoader is a loader whose access carries a currency and prices per tenant, which a test changes
// between loads; it counts its loads and can fail.
type priceLoader struct {
	fakeLoader
	mu2      sync.Mutex
	currency map[tenant.ID]pricing.Active
	prices   map[tenant.ID]map[string][]pricing.Version
	fails    atomic.Int64
}

func (f *priceLoader) access(_ context.Context, id tenant.ID) (TenantAccess, error) {
	f.accesses.Add(1)
	f.mu.Lock()
	if f.perTenant == nil {
		f.perTenant = map[tenant.ID]int{}
	}
	f.perTenant[id]++
	f.mu.Unlock()
	if f.fails.Add(-1) >= 0 {
		return TenantAccess{}, errors.New("synthetic load failure")
	}
	f.clk.advance(f.took)
	f.mu2.Lock()
	defer f.mu2.Unlock()
	return TenantAccess{Granted: map[string]bool{"m-sel": true}, Currency: f.currency[id], Prices: f.prices[id]}, nil
}

func (f *priceLoader) set(id tenant.ID, cur pricing.Active, prices map[string][]pricing.Version) {
	f.mu2.Lock()
	defer f.mu2.Unlock()
	if f.currency == nil {
		f.currency, f.prices = map[tenant.ID]pricing.Active{}, map[tenant.ID]map[string][]pricing.Version{}
	}
	f.currency[id], f.prices[id] = cur, prices
}

func newPriceCache(t *testing.T, ttl time.Duration) (*Cache, *priceLoader, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)}
	l := &priceLoader{fakeLoader: fakeLoader{clk: clk}}
	c, err := newCache(l, CacheOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), TTL: ttl, Now: clk.now})
	if err != nil {
		t.Fatal(err)
	}
	return c, l, clk
}

func tenantVersion(id, currency string, from time.Time) pricing.Version {
	return pricing.Version{ID: id, ModelID: "m-all", ValidFrom: from, Currency: currency, Source: pricing.SourceTenant}
}

// TestTenantPricesCache is T-731: the entry of a tenant carries its currency and prices;
// catalog:<tenant> drops that entry and no other; the lifetime; a failed load is not cached; ResolveIn
// returns the access it resolved with.
func TestTenantPricesCache(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, b := tid(1), tid(2)
	start := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	eur := pricing.Active{Current: "EUR"}
	usd := pricing.Active{Current: "USD"}
	pa := map[string][]pricing.Version{"m-all": {tenantVersion("va", "EUR", start)}}
	pb := map[string][]pricing.Version{"m-all": {tenantVersion("vb", "USD", start)}}

	t.Run("the entry of a tenant carries its currency and its prices", func(t *testing.T) {
		c, l, _ := newPriceCache(t, 30*time.Second)
		l.set(a, eur, pa)
		l.set(b, usd, pb)
		ta, err := c.Access(ctx, a)
		if err != nil || ta.Currency != eur || !reflect.DeepEqual(ta.Prices, pa) {
			t.Errorf("tenant A: %+v (%v), want EUR and its own prices", ta, err)
		}
		tb, err := c.Access(ctx, b)
		if err != nil || tb.Currency != usd || !reflect.DeepEqual(tb.Prices, pb) {
			t.Errorf("tenant B: %+v (%v), want USD and its own prices", tb, err)
		}
	})

	t.Run("catalog:<tenant> drops that entry and no other", func(t *testing.T) {
		c, l, _ := newPriceCache(t, 30*time.Second)
		l.set(a, usd, nil)
		l.set(b, usd, nil)
		for _, id := range []tenant.ID{a, b} {
			if _, err := c.Access(ctx, id); err != nil {
				t.Fatal(err)
			}
		}
		// The tenant changes its currency and writes a price; the notification reaches the cache.
		l.set(a, eur, pa)
		l.set(b, eur, pa) // B's row changed too, but nothing told the cache
		if ta, _ := c.Access(ctx, a); ta.Currency != usd {
			t.Fatalf("the change was seen before the notification: %+v", ta)
		}
		c.Invalidate("catalog:" + a.String())
		ta, err := c.Access(ctx, a)
		if err != nil || ta.Currency != eur || !reflect.DeepEqual(ta.Prices, pa) {
			t.Errorf("tenant A after its notification: %+v (%v)", ta, err)
		}
		if tb, _ := c.Access(ctx, b); tb.Currency != usd || len(tb.Prices) != 0 {
			t.Errorf("tenant B was reloaded by A's notification: %+v", tb)
		}
		if n := l.loadsOf(b); n != 1 {
			t.Errorf("tenant B loaded %d times, want 1", n)
		}
		// And the notification of B reaches B's entry only.
		c.Invalidate("catalog:" + b.String())
		if tb, _ := c.Access(ctx, b); tb.Currency != eur {
			t.Errorf("tenant B after its notification: %+v", tb)
		}
		if n := l.loadsOf(a); n != 2 {
			t.Errorf("tenant A loaded %d times, want 2", n)
		}
	})

	t.Run("a tenant's notification never puts one tenant's currency or prices in another's entry", func(t *testing.T) {
		c, l, _ := newPriceCache(t, 30*time.Second)
		l.set(a, eur, pa)
		l.set(b, usd, pb)
		for i := 0; i < 5; i++ {
			c.Invalidate("catalog:" + a.String())
			c.Invalidate("catalog:" + b.String())
			ta, _ := c.Access(ctx, a)
			tb, _ := c.Access(ctx, b)
			if ta.Currency != eur || tb.Currency != usd || ta.Prices["m-all"][0].ID != "va" || tb.Prices["m-all"][0].ID != "vb" {
				t.Fatalf("round %d: A %+v, B %+v", i, ta, tb)
			}
		}
	})

	t.Run("the lifetime of the entry is the lifetime from the start of its load", func(t *testing.T) {
		c, l, clk := newPriceCache(t, 30*time.Second)
		l.took = 10 * time.Second
		l.set(a, usd, nil)
		for i := 0; i < 3; i++ {
			if _, err := c.Access(ctx, a); err != nil {
				t.Fatal(err)
			}
		}
		if n := l.loadsOf(a); n != 1 {
			t.Fatalf("%d loads for three calls", n)
		}
		l.set(a, eur, pa)
		clk.advance(20*time.Second - time.Nanosecond)
		if ta, _ := c.Access(ctx, a); ta.Currency != usd {
			t.Fatalf("a reload before the lifetime ended: %+v", ta)
		}
		clk.advance(time.Nanosecond)
		if ta, _ := c.Access(ctx, a); ta.Currency != eur || len(ta.Prices) != 1 {
			t.Errorf("no reload at the end of the lifetime: %+v (a change reaches the gateway within cache.catalog_ttl)", ta)
		}
	})

	t.Run("a failed load is not cached", func(t *testing.T) {
		c, l, _ := newPriceCache(t, 30*time.Second)
		l.set(a, eur, pa)
		l.fails.Store(1)
		if _, err := c.Access(ctx, a); err == nil {
			t.Fatal("the failure was swallowed")
		}
		ta, err := c.Access(ctx, a)
		if err != nil || ta.Currency != eur || len(ta.Prices) != 1 {
			t.Errorf("after the failure: %+v (%v)", ta, err)
		}
	})

	t.Run("ResolveIn returns the access it resolved with", func(t *testing.T) {
		c, l, _ := newPriceCache(t, 30*time.Second)
		l.set(tid(1), eur, pa)
		p := principalOf([]string{"p-open/all-model"})
		snap, ta, m, _, err := c.ResolveIn(ctx, p, "p-open", "all-model")
		if err != nil || snap == nil || m.ID != "m-all" {
			t.Fatalf("resolve: %v %v", m, err)
		}
		if ta.Currency != eur || !reflect.DeepEqual(ta.Prices, pa) || !ta.Granted["m-sel"] {
			t.Errorf("the access %+v is not the tenant's entry", ta)
		}
		// A principal with no tenant resolves with the zero access.
		root := p
		root.TenantID = tenant.ID{}
		root.Kind = "platform"
		if _, ta, _, _, _ := c.ResolveIn(ctx, root, "p-open", "all-model"); ta.Currency != (pricing.Active{}) || ta.Prices != nil {
			t.Errorf("a principal without a tenant got %+v", ta)
		}
	})
}
