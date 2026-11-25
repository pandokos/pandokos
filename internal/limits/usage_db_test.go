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

// groupKey identifies a group by what the ledger reader fills: identifiers, not names.
type groupKey struct {
	minute                                        int64
	principal, key, providerID, modelID, currency string
}

func groupsByKey(t *testing.T, gs []limits.UsageGroup) map[groupKey]limits.UsageGroup {
	t.Helper()
	out := map[groupKey]limits.UsageGroup{}
	for _, g := range gs {
		k := groupKey{g.Minute, g.PrincipalID, g.KeyID, g.ProviderID, g.ModelID, g.Currency}
		if _, dup := out[k]; dup {
			t.Errorf("two groups for %+v", k)
		}
		out[k] = g
	}
	return out
}

// TestLedgerUsage is T-658.
func TestLedgerUsage(t *testing.T) {
	t.Parallel()
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		a, b := d.SeedTenant(t, "usage-a"), d.SeedTenant(t, "usage-b")
		reader := limits.NewLedger(d.Open(t, store.RoleWorker))
		// The ledger names a model by its ID and the reader answers with identifiers.
		for _, m := range []string{"gpt-4", "edge", "edge-end", "edge-out", "edge-before"} {
			d.SeedModel(t, "openai", m)
		}
		d.SeedModel(t, "anthropic", "claude")
		oa, an := d.ProviderID(t, "openai"), d.ProviderID(t, "anthropic")
		gpt4, claude := d.ModelID(t, "openai", "gpt-4"), d.ModelID(t, "anthropic", "claude")
		edge, edgeEnd, edgeOut, edgeBefore := d.ModelID(t, "openai", "edge"), d.ModelID(t, "openai", "edge-end"), d.ModelID(t, "openai", "edge-out"), d.ModelID(t, "openai", "edge-before")

		from := utc(2026, 11, 1, 0, 0, 0)
		liveFrom := utc(2026, 11, 17, 11, 47, 0)
		to := utc(2026, 11, 17, 12, 0, 0)
		minute := func(t time.Time) int64 { return t.Unix() / 60 }

		d.SeedUsage(t, a,
			// before the live part: one group, minute 0
			dbtest.UsageRow{StartedAt: utc(2026, 11, 3, 9, 0, 0), PrincipalID: "00000000-0000-4000-8000-000000000001", KeyID: "k1", Provider: "openai", Model: "gpt-4", InputTokens: 10, OutputTokens: 5, Cost: "0.25", Currency: "EUR"},
			dbtest.UsageRow{StartedAt: utc(2026, 11, 4, 9, 0, 0), PrincipalID: "00000000-0000-4000-8000-000000000001", KeyID: "k1", Provider: "openai", Model: "gpt-4", InputTokens: 1, OutputTokens: 1, Cost: "0.5", Currency: "EUR"},
			// estimated tokens (the provider reported nothing) and an unknown cost
			dbtest.UsageRow{StartedAt: utc(2026, 11, 5, 9, 0, 0), PrincipalID: "00000000-0000-4000-8000-000000000002", KeyID: "k2", Provider: "anthropic", Model: "claude", EstimatedInputTokens: 30, EstimatedOutputTokens: 70},
			// a request for which no credential was opened: not counted
			dbtest.UsageRow{StartedAt: utc(2026, 11, 6, 9, 0, 0), PrincipalID: "00000000-0000-4000-8000-000000000001", KeyID: "k1", Provider: "openai", Model: "gpt-4", NoCredential: true, InputTokens: 999, Cost: "9", Currency: "EUR"},
			// refused by a limit: counts nothing
			dbtest.UsageRow{StartedAt: utc(2026, 11, 7, 9, 0, 0), PrincipalID: "00000000-0000-4000-8000-000000000001", KeyID: "k1", Provider: "openai", Model: "gpt-4", NoCredential: true, Outcome: "rejected_limit", EstimatedInputTokens: 500},
			// the live part, by minute
			dbtest.UsageRow{StartedAt: utc(2026, 11, 17, 11, 47, 5), PrincipalID: "00000000-0000-4000-8000-000000000001", KeyID: "k1", Provider: "openai", Model: "gpt-4", InputTokens: 7, Cost: "0.1", Currency: "EUR"},
			dbtest.UsageRow{StartedAt: utc(2026, 11, 17, 11, 47, 50), PrincipalID: "00000000-0000-4000-8000-000000000001", KeyID: "k1", Provider: "openai", Model: "gpt-4", InputTokens: 3, Cost: "0.2", Currency: "EUR"},
			dbtest.UsageRow{StartedAt: utc(2026, 11, 17, 11, 59, 0), PrincipalID: "00000000-0000-4000-8000-000000000001", KeyID: "k1", Provider: "openai", Model: "gpt-4", InputTokens: 1},
			// the boundaries: one microsecond before liveFrom is in the part before, one before `to` is in, `to` is out
			dbtest.UsageRow{StartedAt: liveFrom.Add(-time.Microsecond), PrincipalID: "00000000-0000-4000-8000-000000000003", KeyID: "k3", Provider: "openai", Model: "edge", InputTokens: 11},
			dbtest.UsageRow{StartedAt: to.Add(-time.Microsecond), PrincipalID: "00000000-0000-4000-8000-000000000003", KeyID: "k3", Provider: "openai", Model: "edge-end", InputTokens: 13},
			dbtest.UsageRow{StartedAt: to, PrincipalID: "00000000-0000-4000-8000-000000000003", KeyID: "k3", Provider: "openai", Model: "edge-out", InputTokens: 17},
			dbtest.UsageRow{StartedAt: from.Add(-time.Microsecond), PrincipalID: "00000000-0000-4000-8000-000000000003", KeyID: "k3", Provider: "openai", Model: "edge-before", InputTokens: 19},
		)
		d.SeedUsage(t, b, dbtest.UsageRow{StartedAt: utc(2026, 11, 3, 9, 0, 0), PrincipalID: "00000000-0000-4000-8000-000000000001", KeyID: "k1", Provider: "openai", Model: "gpt-4", InputTokens: 4000, Cost: "40", Currency: "EUR"})

		gs, err := reader.Usage(ctx, a, from, liveFrom, to)
		if err != nil {
			t.Fatal(err)
		}
		got := groupsByKey(t, gs)

		g := got[groupKey{0, "00000000-0000-4000-8000-000000000001", "k1", oa, gpt4, "EUR"}]
		if g.Requests != 2 || g.Tokens != 17 || g.Cost.String() != "0.75" {
			t.Errorf("credential rule and sums: %+v, want 2 requests, 17 tokens, cost 0.75 (the row without credential counts nothing)", g)
		}
		g = got[groupKey{0, "00000000-0000-4000-8000-000000000002", "k2", an, claude, ""}]
		if g.Requests != 1 || g.Tokens != 100 || g.Cost.String() != "0" {
			t.Errorf("estimated tokens and unknown cost: %+v, want 1 request, 100 tokens, cost 0", g)
		}
		g = got[groupKey{minute(utc(2026, 11, 17, 11, 47, 0)), "00000000-0000-4000-8000-000000000001", "k1", oa, gpt4, "EUR"}]
		if g.Requests != 2 || g.Tokens != 10 || g.Cost.String() != "0.3" {
			t.Errorf("minute 11:47: %+v, want 2 requests, 10 tokens, cost 0.3", g)
		}
		g = got[groupKey{minute(utc(2026, 11, 17, 11, 59, 0)), "00000000-0000-4000-8000-000000000001", "k1", oa, gpt4, ""}]
		if g.Requests != 1 || g.Tokens != 1 {
			t.Errorf("minute 11:59 without a cost: %+v", g)
		}
		if g := got[groupKey{0, "00000000-0000-4000-8000-000000000003", "k3", oa, edge, ""}]; g.Requests != 1 || g.Tokens != 11 {
			t.Errorf("a row one microsecond before liveFrom: %+v, want it in the part before (minute 0)", g)
		}
		if g := got[groupKey{minute(to.Add(-time.Minute)), "00000000-0000-4000-8000-000000000003", "k3", oa, edgeEnd, ""}]; g.Requests != 1 || g.Tokens != 13 {
			t.Errorf("a row one microsecond before `to`: %+v, want it in", g)
		}
		for k, g := range got {
			if k.modelID == edgeOut || k.modelID == edgeBefore {
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

// TestLedgerUsageByIdentifier is T-704: the aggregate as the worker role groups by the ID of the
// model and by the ID of the provider that has the row's name now.
func TestLedgerUsageByIdentifier(t *testing.T) {
	t.Parallel()
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		a, b := d.SeedTenant(t, "byid-a"), d.SeedTenant(t, "byid-b")
		reader := limits.NewLedger(d.Open(t, store.RoleWorker))
		super := d.Super(t)
		from, to := utc(2026, 11, 1, 0, 0, 0), utc(2026, 12, 1, 0, 0, 0)
		pid := "00000000-0000-4000-8000-000000000001"
		row := func(day int, provider, model string, tokens int64) dbtest.UsageRow {
			return dbtest.UsageRow{StartedAt: utc(2026, 11, day, 9, 0, 0), PrincipalID: pid, KeyID: "k1", Provider: provider, Model: model, InputTokens: tokens}
		}

		// Two models of one name: the first is deleted, a second is created.
		m1 := d.SeedModel(t, "openai", "reborn")
		d.SeedUsage(t, a, row(3, "openai", "reborn", 10), row(4, "openai", "reborn", 20))
		if _, err := super.Exec(ctx, `DELETE FROM pandokos.model WHERE id = $1::uuid`, m1); err != nil {
			t.Fatal(err)
		}
		m2 := d.SeedModel(t, "openai", "reborn")
		d.SeedUsage(t, a, row(5, "openai", "reborn", 100))
		// A provider that no longer exists.
		d.SeedProvider(t, "gone-provider")
		m3 := d.SeedModel(t, "gone-provider", "orphan")
		d.SeedUsage(t, a, row(6, "gone-provider", "orphan", 7))
		if _, err := super.Exec(ctx, `DELETE FROM pandokos.model WHERE id = $1::uuid`, m3); err != nil {
			t.Fatal(err)
		}
		if _, err := super.Exec(ctx, `DELETE FROM pandokos.provider WHERE name = 'gone-provider'`); err != nil {
			t.Fatal(err)
		}
		// A row of a model the catalogue never had: no model ID.
		d.SeedUsage(t, a, row(7, "openai", "never-in-the-catalogue", 3))
		d.SeedUsage(t, b, row(3, "openai", "reborn", 5000))

		gs, err := reader.Usage(ctx, a, from, to, to)
		if err != nil {
			t.Fatal(err)
		}
		oa := d.ProviderID(t, "openai")
		byModel := map[string]limits.UsageGroup{}
		for _, g := range gs {
			if _, dup := byModel[g.ModelID]; dup {
				t.Errorf("two groups for the model %q: %+v", g.ModelID, g)
			}
			byModel[g.ModelID] = g
		}
		if len(gs) != 4 {
			t.Errorf("%d groups, want 4 (the two models of one name, the orphan, the unknown): %+v", len(gs), gs)
		}
		if g := byModel[m1]; g.Tokens != 30 || g.Requests != 2 || g.ProviderID != oa {
			t.Errorf("the deleted model's group: %+v, want 2 requests, 30 tokens under the provider openai", g)
		}
		if g := byModel[m2]; g.Tokens != 100 || g.Requests != 1 || g.ProviderID != oa {
			t.Errorf("the new model's group: %+v", g)
		}
		if m1 == m2 {
			t.Fatal("the model created again has the old ID")
		}
		if g, ok := byModel[m3]; !ok || g.Tokens != 7 || g.ProviderID != "" {
			t.Errorf("a row of a provider that no longer exists: %+v (present %v), want an empty provider ID", g, ok)
		}
		if g, ok := byModel[""]; !ok || g.Tokens != 3 || g.ProviderID != oa {
			t.Errorf("a row without a model ID: %+v (present %v)", g, ok)
		}
		// The names are not filled by the reader.
		for _, g := range gs {
			if g.Provider != "" || g.Model != "" {
				t.Errorf("the reader filled names: %+v", g)
			}
		}
		// Sum: a limit on the new model counts its rows only; a limit on the provider counts all four groups' share.
		l := limits.Limit{Tenant: a, Subject: limits.SubjectTenant, Metric: limits.TokensPerMonth, ProviderID: oa, ModelID: m2}
		if got := limits.Sum(l, gs); got.N != 100 {
			t.Errorf("the new model's limit counts %d, want 100", got.N)
		}
		l.ModelID = ""
		if got := limits.Sum(l, gs); got.N != 100+30+3 {
			t.Errorf("the provider's limit counts %d, want 133", got.N)
		}
		// Another tenant's rows are never seen.
		gsB, err := reader.Usage(ctx, b, from, to, to)
		if err != nil || len(gsB) != 1 || gsB[0].Tokens != 5000 {
			t.Errorf("tenant B reads %+v (%v)", gsB, err)
		}
		for _, g := range gs {
			if g.Tokens == 5000 {
				t.Errorf("tenant A reads a row of tenant B: %+v", g)
			}
		}
	})
}
