package gateway_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/resolver"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestInactiveBudgets is T-737: a budget in another currency never refuses and records no event; a
// request that only inactive budgets match is unlimited and writes the unlimited-traffic rows; a model
// priced in neither list of the active currency is 409 no_price under an active budget, admitted
// uncounted with `allow`, and recorded with an unknown cost without one; a rule cache and a catalogue
// cache that disagree on the currency refuse.
func TestInactiveBudgets(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	w := newPriceWorld(t, d)

	events := func(tn string) int {
		var n int
		if err := d.Super(t).QueryRow(context.Background(), `SELECT count(*) FROM pandokos.limit_event WHERE tenant_id = $1::uuid`, tn).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("a budget in another currency never refuses and records no event, and the traffic is unlimited", func(t *testing.T) {
		tn := d.SeedTenant(t, "inactive-a")
		// A budget in EUR of one millionth, with thresholds: it would refuse and mark if it applied. The
		// tenant is in USD, the catalogue prices in USD.
		d.SeedLimitRule(t, tn, "tenant", "", "cost_per_month", 1, dbtest.LimitOptions{Currency: "EUR", Thresholds: []int{50, 100}})
		quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
		aud := resolver.NewUnlimitedAuditor(d.Open(t, store.RoleGateway), quiet, nil)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { aud.Run(ctx); close(done) }()
		t.Cleanup(func() {
			cancel()
			<-done
			cc, c := context.WithTimeout(context.Background(), 5*time.Second)
			defer c()
			aud.Close(cc)
		})
		s := w.stack(t, tn, priceOptions{stackOptions: stackOptions{unlimited: aud}})
		for i := 0; i < 5; i++ {
			if c := s.ask(t); c.status != 200 {
				t.Fatalf("request %d: %d %s: a budget in EUR refused a tenant in USD", i+1, c.status, c.body)
			}
		}
		for _, r := range s.rows(t, 5) {
			if r.Source != "catalogue" || r.Currency != "USD" || r.Status != "known" {
				t.Errorf("record %+v", r)
			}
		}
		time.Sleep(200 * time.Millisecond) // nothing to wait for: give a wrong implementation the time to write an event
		if n := events(tn.String()); n != 0 {
			t.Errorf("%d limit events for a budget that is not in force", n)
		}
		testutil.Eventually(t, 5*time.Second, "the unlimited-traffic rows", func() bool {
			return len(unlimitedRows(t, d, false, tn.String())) == 1 && len(unlimitedRows(t, d, true, tn.String())) == 1
		})
	})

	t.Run("a model priced in neither list of the active currency", func(t *testing.T) {
		tn := d.SeedTenant(t, "inactive-b")
		d.SetTenantCurrency(t, tn, "EUR", "", time.Time{}) // the catalogue prices in USD only
		d.SeedLimitRule(t, tn, "tenant", "", "cost_per_month", 1_000_000_000, dbtest.LimitOptions{Currency: "EUR"})

		t.Run("an active budget refuses with 409 no_price", func(t *testing.T) {
			s := w.stack(t, tn, priceOptions{})
			c := s.ask(t)
			if c.status != 409 || header(c, "X-Pandokos-Error") != "no_price" {
				t.Fatalf("%d %s, want 409 no_price", c.status, c.body)
			}
		})
		t.Run("with on_unknown_price allow the request is admitted and not counted", func(t *testing.T) {
			s := w.stack(t, tn, priceOptions{stackOptions: stackOptions{unknownPrice: "allow"}})
			for i := 0; i < 3; i++ {
				if c := s.ask(t); c.status != 200 {
					t.Fatalf("request %d: %d %s", i+1, c.status, c.body)
				}
			}
			for _, r := range served(s.rows(t, 3)) {
				if r.Status != "unknown" || r.Reason != "no_price" || r.Currency != "" || r.Source != "" {
					t.Errorf("record %+v, want an unknown cost for no price", r)
				}
			}
		})
		t.Run("without a budget the request passes and its cost is recorded as unknown", func(t *testing.T) {
			free := d.SeedTenant(t, "inactive-free")
			d.SetTenantCurrency(t, free, "EUR", "", time.Time{})
			s := w.stack(t, free, priceOptions{})
			if c := s.ask(t); c.status != 200 {
				t.Fatalf("%d %s", c.status, c.body)
			}
			rows := s.rows(t, 1)
			if rows[0].Status != "unknown" || rows[0].Reason != "no_price" {
				t.Errorf("record %+v", rows[0])
			}
		})
	})

	t.Run("a rule cache and a catalogue cache that disagree on the currency refuse", func(t *testing.T) {
		tn := d.SeedTenant(t, "inactive-split")
		d.SeedLimitRule(t, tn, "tenant", "", "cost_per_month", 1_000_000_000, dbtest.LimitOptions{Currency: "EUR"})
		s := w.stack(t, tn, priceOptions{}) // no listener: the two caches are told by hand
		if c := s.ask(t); c.status != 200 { // both caches load USD; the EUR budget is inactive
			t.Fatalf("the first request: %d %s", c.status, c.body)
		}
		d.SetTenantCurrency(t, tn, "EUR", "USD", time.Time{})
		s.cache.Invalidate("limits:" + tn.String()) // the rule cache knows EUR, the catalogue cache still USD
		c := s.ask(t)
		if c.status != 409 || header(c, "X-Pandokos-Error") != "no_price" {
			t.Errorf("%d %s: the estimate is priced in USD and the budget that applies is in EUR, the request meets on_unknown_price (refused)", c.status, c.body)
		}
		s.cat.Invalidate("catalog:" + tn.String()) // now both know
		if c := s.ask(t); c.status != 409 {        // EUR in force, the model has no price in EUR
			t.Errorf("both caches in EUR: %d %s, want 409 no_price (the model has no EUR price)", c.status, c.body)
		}
	})
}
