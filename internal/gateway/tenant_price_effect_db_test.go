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

	t.Run("a scheduled version applies from its instant with no notification", func(t *testing.T) {
		tn := d.SeedTenant(t, "effect-scheduled")
		d.SeedTenantPrice(t, tn, "USD", "openai", "fake-basic", time.Now().Add(1200*time.Millisecond))
		s := w.stack(t, tn, priceOptions{}) // no listener: nothing is notified of anything
		if src, cur := s.lastSource(t); src != "catalogue" || cur != "USD" {
			t.Fatalf("before its instant: %s %s", src, cur)
		}
		s.becomes(t, 4*time.Second, "the scheduled version to apply", "tenant", "USD")
	})

	t.Run("a scheduled end applies from its instant with no notification", func(t *testing.T) {
		tn := d.SeedTenant(t, "effect-scheduled-end")
		d.SeedTenantPrice(t, tn, "USD", "openai", "fake-basic", time.Time{})
		d.SeedTenantPriceEnd(t, tn, "USD", "openai", "fake-basic", time.Now().Add(1200*time.Millisecond))
		s := w.stack(t, tn, priceOptions{})
		if src, cur := s.lastSource(t); src != "tenant" || cur != "USD" {
			t.Fatalf("before the end: %s %s", src, cur)
		}
		s.becomes(t, 4*time.Second, "the end to apply", "catalogue", "USD")
	})

	t.Run("a pending currency applies from its instant with no notification", func(t *testing.T) {
		tn := d.SeedTenant(t, "effect-pending")
		d.SeedTenantPrice(t, tn, "EUR", "openai", "fake-basic", time.Time{})
		d.SetTenantCurrency(t, tn, "EUR", "USD", time.Now().Add(1200*time.Millisecond))
		s := w.stack(t, tn, priceOptions{})
		if src, cur := s.lastSource(t); src != "catalogue" || cur != "USD" {
			t.Fatalf("before the instant: %s %s", src, cur)
		}
		s.becomes(t, 4*time.Second, "the new currency to apply", "tenant", "EUR")
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
