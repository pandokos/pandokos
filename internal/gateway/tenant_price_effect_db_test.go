package gateway_test

import (
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// lastSource asks once and returns the price source and the currency of the record of that request.
func (s *priceStack) lastSource(t *testing.T) (source, currency string) {
	t.Helper()
	n := len(s.readRows(t))
	if c := s.ask(t); c.status != 200 {
		t.Fatalf("a request answered %d %s", c.status, c.body)
	}
	rows := s.rows(t, n+1)
	r := rows[len(rows)-1]
	return r.Source, r.Currency
}

// becomes asks until the record of a request has the source and the currency, within limit.
func (s *priceStack) becomes(t *testing.T, limit time.Duration, what, source, currency string) {
	t.Helper()
	testutil.Eventually(t, limit, what, func() bool {
		src, cur := s.lastSource(t)
		return src == source && cur == currency
	})
}

// TestTenantPriceTakesEffect is T-738: a version written through the administration API prices the next
// request of a running gateway within 2 s; a scheduled version and a pending currency apply from their
// instant with no notification; an end gives the catalogue price back; with the notifications cut, a
// change reaches the gateway within the cache lifetimes.
func TestTenantPriceTakesEffect(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	w := newPriceWorld(t, d)

	t.Run("a version written through the administration API prices the next request within 2 s", func(t *testing.T) {
		api := apitest.NewOn(t, d)
		ten, key := api.Tenant(t, "effect-api")
		id, err := tenant.ParseID(ten.ID)
		if err != nil {
			t.Fatal(err)
		}
		s := w.stack(t, id, priceOptions{listener: true})
		if src, cur := s.lastSource(t); src != "catalogue" || cur != "USD" {
			t.Fatalf("before the version: %s %s", src, cur)
		}
		body := map[string]any{"provider": "openai", "model": "fake-basic", "currency": "USD",
			"rates": []map[string]any{{"category": "input", "threshold": 0, "rate": "10"}, {"category": "output", "threshold": 0, "rate": "20"}}}
		if r := api.Do(t, "POST", "/v1/tenants/"+ten.ID+"/prices", key, body, nil); r.Status != 201 {
			t.Fatalf("createPriceListVersion: %d %s", r.Status, r.Body)
		}
		s.becomes(t, 2*time.Second, "the tenant's version to price a request", "tenant", "USD")

		// An end gives the catalogue's price back.
		end := map[string]any{"provider": "openai", "model": "fake-basic", "currency": "USD", "kind": "end"}
		if r := api.Do(t, "POST", "/v1/tenants/"+ten.ID+"/prices", key, end, nil); r.Status != 201 {
			t.Fatalf("createPriceListVersion (end): %d %s", r.Status, r.Body)
		}
		s.becomes(t, 2*time.Second, "the end to give the catalogue's price back", "catalogue", "USD")
	})

	// scheduled sets a tenant up with an instant ahead of the clock of the handler, builds a stack with no
	// notification and asks once: the answer is the one before the instant. The handler clock is a test
	// clock that stands still until the test moves it, and the two caches live much longer than the test
	// and run on a frozen clock: nothing expires and nothing is notified, so the answer after the instant
	// can only come from the entries that were cached before it, read at the new request time.
	scheduled := func(t *testing.T, name string, setup func(tn tenant.ID, at time.Time), wantSource, wantCurrency string) (*priceStack, *testClock) {
		t.Helper()
		tn := d.SeedTenant(t, name)
		at := time.Now().Add(10 * time.Minute)
		setup(tn, at)
		clk := newTestClock() // after the seeds: the versions that start "now" have started on this clock
		frozen := time.Now()
		cacheClock := func() time.Time { return frozen }
		s := w.stack(t, tn, priceOptions{catalogTTL: 5 * time.Minute, catalogNow: cacheClock,
			stackOptions: stackOptions{rulesTTL: 5 * time.Minute, rulesNow: cacheClock, clock: clk.Now}}) // no listener: nothing is notified of anything
		if src, cur := s.lastSource(t); src != wantSource || cur != wantCurrency {
			t.Fatalf("before its instant: %s %s, want %s %s", src, cur, wantSource, wantCurrency)
		}
		return s, clk
	}
	// after moves the clock past the instant and checks the next request is priced from the cached entries.
	after := func(t *testing.T, s *priceStack, clk *testClock, wantSource, wantCurrency string) {
		t.Helper()
		clk.Advance(11 * time.Minute)
		if src, cur := s.lastSource(t); src != wantSource || cur != wantCurrency {
			t.Fatalf("after its instant: %s %s, want %s %s", src, cur, wantSource, wantCurrency)
		}
	}

	t.Run("a scheduled version applies from its instant with no notification", func(t *testing.T) {
		s, clk := scheduled(t, "effect-scheduled", func(tn tenant.ID, at time.Time) {
			d.SeedTenantPrice(t, tn, "USD", "openai", "fake-basic", at)
		}, "catalogue", "USD")
		after(t, s, clk, "tenant", "USD")
	})

	t.Run("a scheduled end applies from its instant with no notification", func(t *testing.T) {
		s, clk := scheduled(t, "effect-scheduled-end", func(tn tenant.ID, at time.Time) {
			d.SeedTenantPrice(t, tn, "USD", "openai", "fake-basic", time.Time{})
			d.SeedTenantPriceEnd(t, tn, "USD", "openai", "fake-basic", at)
		}, "tenant", "USD")
		after(t, s, clk, "catalogue", "USD")
	})

	t.Run("a pending currency applies from its instant with no notification", func(t *testing.T) {
		s, clk := scheduled(t, "effect-pending", func(tn tenant.ID, at time.Time) {
			d.SeedTenantPrice(t, tn, "EUR", "openai", "fake-basic", time.Time{})
			d.SetTenantCurrency(t, tn, "EUR", "USD", at)
		}, "catalogue", "USD")
		after(t, s, clk, "tenant", "EUR")
	})

	t.Run("with the notifications cut a change reaches the gateway within the cache lifetimes", func(t *testing.T) {
		tn := d.SeedTenant(t, "effect-cut")
		s := w.stack(t, tn, priceOptions{catalogTTL: time.Second, stackOptions: stackOptions{rulesTTL: time.Second}})
		if src, cur := s.lastSource(t); src != "catalogue" || cur != "USD" {
			t.Fatalf("before the change: %s %s", src, cur)
		}
		d.SeedTenantPrice(t, tn, "USD", "openai", "fake-basic", time.Time{})
		s.becomes(t, 5*time.Second, "the lifetime of the entry to end and the version to apply", "tenant", "USD")
		d.SetTenantCurrency(t, tn, "EUR", "USD", time.Time{})
		d.SeedTenantPrice(t, tn, "EUR", "openai", "fake-basic", time.Time{})
		s.becomes(t, 5*time.Second, "the new currency to apply", "tenant", "EUR")
	})
}
