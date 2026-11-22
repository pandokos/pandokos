package resolver_test

import (
	"context"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/resolver"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestLimitsStats is T-922 (resolver part): the requests admitted without a price are counted when
// on_unknown_price is allow and the estimate cannot be measured against a budget; a refusal, an
// estimate with a price in the budget's currency and a request with no budget are not.
func TestLimitsStats(t *testing.T) {
	a := limitTenantID(7)
	start := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	p := limitPrincipal(a, "sa-1", "key-1", "")
	bg := context.Background()
	budget := periodRule("b", limits.CostPerMonth, 1_000_000_000, "", "", "EUR")
	quota := periodRule("q", limits.RequestsPerDay, 100, "", "", "")
	estimate := func(cost pricing.Cost) gateway.LimitEstimate {
		return gateway.LimitEstimate{RequestID: "req-stats", InputTokens: 100, OutputTokens: 400, Provider: "openai", Model: "gpt-4o",
			ProviderID: "openai", ModelID: "gpt-4o", Start: start, Cost: cost}
	}
	usd := pricing.Cost{Known: true, Amount: dec(t, "0.5"), Currency: "USD", VersionID: "v1"}
	eur := pricing.Cost{Known: true, Amount: dec(t, "0.5"), Currency: "EUR", VersionID: "v2"}
	none := pricing.Cost{Reason: pricing.ReasonNoPrice}

	allow, _ := periodAdapter(t, []limits.Rule{budget, quota}, &fakeStore{}, &fakeEnsurer{}, "allow", "")
	if got := allow.Stats().AdmittedUnpriced; got != 0 {
		t.Fatalf("a new adapter has admitted %d unpriced", got)
	}
	// A price in another currency than the budget's cannot be measured; so cannot a missing price.
	for i, cost := range []pricing.Cost{usd, none} {
		res, ref, _, err := allow.Reserve(bg, p, estimate(cost))
		if err != nil || ref != nil || res == nil {
			t.Fatalf("%v %v %v", res, ref, err)
		}
		if got := allow.Stats().AdmittedUnpriced; got != int64(i+1) {
			t.Errorf("after %d unpriced admissions the count is %d", i+1, got)
		}
	}
	// A price in the budget's currency is measured: not unpriced.
	if _, ref, _, err := allow.Reserve(bg, p, estimate(eur)); err != nil || ref != nil {
		t.Fatalf("%v %v", ref, err)
	}
	if got := allow.Stats().AdmittedUnpriced; got != 2 {
		t.Errorf("a measurable request was counted as unpriced: %d", got)
	}

	// With on_unknown_price refuse the request is refused, not admitted unpriced.
	refuse, _ := periodAdapter(t, []limits.Rule{budget, quota}, &fakeStore{}, &fakeEnsurer{}, "refuse", "")
	if _, ref, _, err := refuse.Reserve(bg, p, estimate(usd)); err != nil || ref == nil || ref.Code != "no_price" {
		t.Fatalf("%v %v", ref, err)
	}
	if got := refuse.Stats().AdmittedUnpriced; got != 0 {
		t.Errorf("a refusal was counted as an unpriced admission: %d", got)
	}
	// No budget at all: the unknown price is irrelevant.
	plain, _ := periodAdapter(t, []limits.Rule{quota}, &fakeStore{}, &fakeEnsurer{}, "allow", "")
	if _, ref, _, err := plain.Reserve(bg, p, estimate(none)); err != nil || ref != nil {
		t.Fatalf("%v %v", ref, err)
	}
	if got := plain.Stats().AdmittedUnpriced; got != 0 {
		t.Errorf("a request without a budget was counted as unpriced: %d", got)
	}
}

// TestUnlimitedStats is T-922 (unlimited traffic): rows written, skipped (the database already had
// the day's row) and failed (the write did not happen).
func TestUnlimitedStats(t *testing.T) {
	d := dbtest.NewMigrated(t)
	clk := &stepClock{t: time.Now().UTC().Truncate(time.Second)}
	today := clk.now().Format("2006-01-02")

	t.Run("written and skipped", func(t *testing.T) {
		a := d.SeedTenant(t, "unl-stats")
		first, second := startAuditor(t, d, clk), startAuditor(t, d, clk)
		if st := first.Stats(); st.Written != 0 || st.Skipped != 0 || st.Failed != 0 {
			t.Fatalf("a new auditor: %+v", st)
		}
		first.Note(a, today, "UTC", "openai", "gpt-4o", reqID(1))
		testutil.Eventually(t, 5*time.Second, "the row to be written", func() bool { return first.Stats().Written == 1 })
		// 200 more notes of the same day cost a map lookup and write nothing.
		for i := 0; i < 200; i++ {
			first.Note(a, today, "UTC", "openai", "gpt-4o", reqID(10+i))
		}
		time.Sleep(200 * time.Millisecond)
		if st := first.Stats(); st.Written != 1 || st.Skipped != 0 || st.Failed != 0 {
			t.Errorf("after 200 notes of the same day: %+v", st)
		}
		// Another instance finds the row there: one call that inserts nothing is a skipped row.
		second.Note(a, today, "UTC", "openai", "gpt-4o", reqID(500))
		testutil.Eventually(t, 5*time.Second, "the second instance to skip", func() bool { return second.Stats().Skipped == 1 })
		if st := second.Stats(); st.Written != 0 || st.Failed != 0 {
			t.Errorf("the second instance: %+v", st)
		}
	})

	t.Run("dropped day is failed", func(t *testing.T) {
		// An auditor that is never run: its queue (256 entries) fills and the next day offered is
		// dropped, which is a day that will not be written now.
		aud := resolver.NewUnlimitedAuditor(d.Open(t, store.RoleGateway), nil, clk.now)
		for i := 1; i <= 256; i++ {
			aud.Note(tenant.ID{byte(i), byte(i >> 8), 1}, today, "UTC", "openai", "gpt-4o", reqID(i))
		}
		if st := aud.Stats(); st.Failed != 0 {
			t.Fatalf("a queue that is not full: %+v", st)
		}
		aud.Note(tenant.ID{0, 0, 2}, today, "UTC", "openai", "gpt-4o", reqID(999))
		if st := aud.Stats(); st.Failed != 1 || st.Written != 0 || st.Skipped != 0 {
			t.Errorf("after one dropped day: %+v", st)
		}
	})

	t.Run("failed", func(t *testing.T) {
		a := d.SeedTenant(t, "unl-fail")
		aud := startAuditor(t, d, clk)
		aud.Note(a, "not-a-day", "UTC", "openai", "gpt-4o", reqID(900)) // the write cannot be made
		testutil.Eventually(t, 5*time.Second, "the failed write", func() bool { return aud.Stats().Failed == 1 })
		if st := aud.Stats(); st.Written != 0 || st.Skipped != 0 {
			t.Errorf("%+v", st)
		}
	})
}
