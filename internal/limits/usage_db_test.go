package limits_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/test/dbtest"
)

// fakeNow is a clock the external tests move by hand.
type fakeNow struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeNow(t time.Time) *fakeNow { return &fakeNow{t: t} }

func (c *fakeNow) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeNow) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func utc(y int, mo time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
}

type groupKey struct {
	minute                                    int64
	principal, key, provider, model, currency string
}

func groupsByKey(t *testing.T, gs []limits.UsageGroup) map[groupKey]limits.UsageGroup {
	t.Helper()
	out := map[groupKey]limits.UsageGroup{}
	for _, g := range gs {
		k := groupKey{g.Minute, g.PrincipalID, g.KeyID, g.Provider, g.Model, g.Currency}
		if _, dup := out[k]; dup {
			t.Errorf("two groups for %+v", k)
		}
		out[k] = g
	}
	return out
}

// TestLedgerUsage is T-658.
func TestLedgerUsage(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		a, b := d.SeedTenant(t, "usage-a"), d.SeedTenant(t, "usage-b")
		reader := limits.NewLedger(d.Open(t, store.RoleWorker))

		from := utc(2026, 11, 1, 0, 0, 0)
		liveFrom := utc(2026, 11, 17, 11, 47, 0)
		to := utc(2026, 11, 17, 12, 0, 0)
		minute := func(t time.Time) int64 { return t.Unix() / 60 }

		d.SeedUsage(t, a,
			// before the live part: one group, minute 0
			dbtest.UsageRow{StartedAt: utc(2026, 11, 3, 9, 0, 0), PrincipalID: "sa-1", KeyID: "k1", Provider: "openai", Model: "gpt-4", InputTokens: 10, OutputTokens: 5, Cost: "0.25", Currency: "EUR"},
			dbtest.UsageRow{StartedAt: utc(2026, 11, 4, 9, 0, 0), PrincipalID: "sa-1", KeyID: "k1", Provider: "openai", Model: "gpt-4", InputTokens: 1, OutputTokens: 1, Cost: "0.5", Currency: "EUR"},
			// estimated tokens (the provider reported nothing) and an unknown cost
			dbtest.UsageRow{StartedAt: utc(2026, 11, 5, 9, 0, 0), PrincipalID: "sa-2", KeyID: "k2", Provider: "anthropic", Model: "claude", EstimatedInputTokens: 30, EstimatedOutputTokens: 70},
			// a request for which no credential was opened: not counted
			dbtest.UsageRow{StartedAt: utc(2026, 11, 6, 9, 0, 0), PrincipalID: "sa-1", KeyID: "k1", Provider: "openai", Model: "gpt-4", NoCredential: true, InputTokens: 999, Cost: "9", Currency: "EUR"},
			// refused by a limit: counts nothing
			dbtest.UsageRow{StartedAt: utc(2026, 11, 7, 9, 0, 0), PrincipalID: "sa-1", KeyID: "k1", Provider: "openai", Model: "gpt-4", NoCredential: true, Outcome: "rejected_limit", EstimatedInputTokens: 500},
			// the live part, by minute
			dbtest.UsageRow{StartedAt: utc(2026, 11, 17, 11, 47, 5), PrincipalID: "sa-1", KeyID: "k1", Provider: "openai", Model: "gpt-4", InputTokens: 7, Cost: "0.1", Currency: "EUR"},
			dbtest.UsageRow{StartedAt: utc(2026, 11, 17, 11, 47, 50), PrincipalID: "sa-1", KeyID: "k1", Provider: "openai", Model: "gpt-4", InputTokens: 3, Cost: "0.2", Currency: "EUR"},
			dbtest.UsageRow{StartedAt: utc(2026, 11, 17, 11, 59, 0), PrincipalID: "sa-1", KeyID: "k1", Provider: "openai", Model: "gpt-4", InputTokens: 1},
			// the boundaries: one microsecond before liveFrom is in the part before, one before `to` is in, `to` is out
			dbtest.UsageRow{StartedAt: liveFrom.Add(-time.Microsecond), PrincipalID: "sa-3", KeyID: "k3", Provider: "openai", Model: "edge", InputTokens: 11},
			dbtest.UsageRow{StartedAt: to.Add(-time.Microsecond), PrincipalID: "sa-3", KeyID: "k3", Provider: "openai", Model: "edge-end", InputTokens: 13},
			dbtest.UsageRow{StartedAt: to, PrincipalID: "sa-3", KeyID: "k3", Provider: "openai", Model: "edge-out", InputTokens: 17},
			dbtest.UsageRow{StartedAt: from.Add(-time.Microsecond), PrincipalID: "sa-3", KeyID: "k3", Provider: "openai", Model: "edge-before", InputTokens: 19},
		)
		d.SeedUsage(t, b, dbtest.UsageRow{StartedAt: utc(2026, 11, 3, 9, 0, 0), PrincipalID: "sa-1", KeyID: "k1", Provider: "openai", Model: "gpt-4", InputTokens: 4000, Cost: "40", Currency: "EUR"})

		gs, err := reader.Usage(ctx, a, from, liveFrom, to)
		if err != nil {
			t.Fatal(err)
		}
		got := groupsByKey(t, gs)

		g := got[groupKey{0, "sa-1", "k1", "openai", "gpt-4", "EUR"}]
		if g.Requests != 2 || g.Tokens != 17 || g.Cost.String() != "0.75" {
			t.Errorf("credential rule and sums: %+v, want 2 requests, 17 tokens, cost 0.75 (the row without credential counts nothing)", g)
		}
		g = got[groupKey{0, "sa-2", "k2", "anthropic", "claude", ""}]
		if g.Requests != 1 || g.Tokens != 100 || g.Cost.String() != "0" {
			t.Errorf("estimated tokens and unknown cost: %+v, want 1 request, 100 tokens, cost 0", g)
		}
		g = got[groupKey{minute(utc(2026, 11, 17, 11, 47, 0)), "sa-1", "k1", "openai", "gpt-4", "EUR"}]
		if g.Requests != 2 || g.Tokens != 10 || g.Cost.String() != "0.3" {
			t.Errorf("minute 11:47: %+v, want 2 requests, 10 tokens, cost 0.3", g)
		}
		g = got[groupKey{minute(utc(2026, 11, 17, 11, 59, 0)), "sa-1", "k1", "openai", "gpt-4", ""}]
		if g.Requests != 1 || g.Tokens != 1 {
			t.Errorf("minute 11:59 without a cost: %+v", g)
		}
		if g := got[groupKey{0, "sa-3", "k3", "openai", "edge", ""}]; g.Requests != 1 || g.Tokens != 11 {
			t.Errorf("a row one microsecond before liveFrom: %+v, want it in the part before (minute 0)", g)
		}
		if g := got[groupKey{minute(to.Add(-time.Minute)), "sa-3", "k3", "openai", "edge-end", ""}]; g.Requests != 1 || g.Tokens != 13 {
			t.Errorf("a row one microsecond before `to`: %+v, want it in", g)
		}
		for k, g := range got {
			if k.model == "edge-out" || k.model == "edge-before" {
				t.Errorf("a row outside [from, to) was counted: %+v", g)
			}
		}
		var tokens, requests int64
		var cost pricing.Decimal
		for _, g := range gs {
			tokens += g.Tokens
			requests += g.Requests
			cost = cost.Add(g.Cost)
		}
		if requests != 2+1+3+2 || tokens != 17+100+11+1+10+13 || cost.String() != "1.05" {
			t.Errorf("totals %d requests, %d tokens, cost %s: a rejected_limit row, a row without credential or a row of another tenant counted", requests, tokens, cost)
		}

		t.Run("another tenant sees only its own rows", func(t *testing.T) {
			gsB, err := reader.Usage(ctx, b, from, liveFrom, to)
			if err != nil {
				t.Fatal(err)
			}
			if len(gsB) != 1 || gsB[0].Tokens != 4000 || gsB[0].Cost.String() != "40" {
				t.Errorf("tenant B reads %+v", gsB)
			}
		})

		t.Run("the gateway role cannot run the query", func(t *testing.T) {
			if _, err := limits.NewLedger(d.Open(t, store.RoleGateway)).Usage(ctx, a, from, liveFrom, to); err == nil {
				t.Error("the gateway role read the ledger")
			}
		})

		t.Run("the admin role can, in the session of the tenant", func(t *testing.T) {
			gsAdmin, err := limits.NewLedger(d.Open(t, store.RoleAdmin)).Usage(ctx, a, from, liveFrom, to)
			if err != nil || len(gsAdmin) != len(gs) {
				t.Errorf("admin read %d groups, %v; the worker read %d", len(gsAdmin), err, len(gs))
			}
		})

		t.Run("an empty range", func(t *testing.T) {
			gs, err := reader.Usage(ctx, a, utc(2025, 1, 1, 0, 0, 0), utc(2025, 1, 1, 0, 0, 0), utc(2025, 1, 2, 0, 0, 0))
			if err != nil || len(gs) != 0 {
				t.Errorf("%d groups, %v", len(gs), err)
			}
		})
	})
}
