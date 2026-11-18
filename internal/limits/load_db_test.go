package limits_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/test/dbtest"
)

// TestLoadRules is T-624.
func TestLoadRules(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		a, b := d.SeedTenant(t, "load-a"), d.SeedTenant(t, "load-b")
		gw := d.Open(t, store.RoleGateway)

		d.SeedPlatformLimit(t, nil, "requests_per_minute", 1000)
		d.SeedPlatformLimit(t, nil, "tokens_per_minute", 5000)
		d.SeedPlatformLimit(t, &a, "requests_per_minute", 50) // replaces the default for A
		d.SeedPlatformLimit(t, &b, "concurrent_requests", 1)  // never seen by A
		d.SeedLimitRule(t, a, "tenant", "", "tokens_per_minute", 3000)
		d.SeedLimitRule(t, a, "service_account", "sa-1", "requests_per_minute", 5)
		d.SeedLimitRule(t, a, "key", "k-1", "concurrent_requests", 2)
		d.SeedLimitRule(t, b, "key", "k-b", "concurrent_requests", 9)
		d.SeedLimitRule(t, b, "tenant", "", "concurrent_requests", 4)

		got, err := limits.LoadRules(ctx, gw, a)
		if err != nil {
			t.Fatal(err)
		}
		want := limits.NewRuleSet(a,
			map[limits.Metric]int64{limits.RequestsPerMinute: 1000, limits.TokensPerMinute: 5000},
			map[limits.Metric]int64{limits.RequestsPerMinute: 50},
			[]limits.Rule{
				{SubjectKind: limits.SubjectTenant, SubjectID: "", Metric: limits.TokensPerMinute, Value: 3000},
				{SubjectKind: limits.SubjectServiceAccount, SubjectID: "sa-1", Metric: limits.RequestsPerMinute, Value: 5},
				{SubjectKind: limits.SubjectKey, SubjectID: "k-1", Metric: limits.ConcurrentRequests, Value: 2},
			})
		g, w := got.For(limits.Query{ServiceAccountID: "sa-1", KeyID: "k-1"}, nil), want.For(limits.Query{ServiceAccountID: "sa-1", KeyID: "k-1"}, nil)
		if len(g) != 4 || len(g) != len(w) {
			t.Fatalf("limits %+v, want %+v", g, w)
		}
		for i := range g {
			if !reflect.DeepEqual(g[i], w[i]) {
				t.Errorf("limit %d: %+v, want %+v", i, g[i], w[i])
			}
		}
		if l, ok := find(g, limits.SubjectTenant, "", limits.RequestsPerMinute); !ok || l.Max != 50 {
			t.Errorf("the platform row of the tenant did not replace the default: %+v", l)
		}
		if l, ok := find(g, limits.SubjectTenant, "", limits.TokensPerMinute); !ok || l.Max != 3000 || l.Tenant != a {
			t.Errorf("the tenant rule below the default: %+v", l)
		}
		// Nothing of tenant B: not its rules, not its platform row, not its subjects.
		for _, l := range got.For(limits.Query{ServiceAccountID: "", KeyID: "k-b"}, nil) {
			if l.Subject != limits.SubjectTenant { // the tenant's own limits apply to every caller
				t.Errorf("a limit for B's key in A's rules: %+v", l)
			}
		}
		if _, ok := find(g, limits.SubjectTenant, "", limits.ConcurrentRequests); ok {
			t.Error("B's platform row or tenant rule is in A's rules")
		}

		gotB, err := limits.LoadRules(ctx, gw, b)
		if err != nil {
			t.Fatal(err)
		}
		lb, ok := find(gotB.For(limits.Query{ServiceAccountID: "", KeyID: ""}, nil), limits.SubjectTenant, "", limits.ConcurrentRequests)
		if !ok || lb.Max != 1 || lb.Tenant != b {
			t.Errorf("B's own ceiling and rule: %+v (the smaller, 1, is wanted)", lb)
		}

		t.Run("a tenant without rules gets the defaults", func(t *testing.T) {
			c := d.SeedTenant(t, "load-c")
			set, err := limits.LoadRules(ctx, gw, c)
			if err != nil {
				t.Fatal(err)
			}
			if ls := set.For(limits.Query{ServiceAccountID: "", KeyID: ""}, nil); len(ls) != 2 {
				t.Errorf("limits %+v, want the two defaults", ls)
			}
		})

		t.Run("the load runs as the gateway role, in the tenant's session", func(t *testing.T) {
			if _, err := limits.LoadRules(ctx, d.Open(t, store.RoleWorker), a); err == nil {
				t.Error("the worker role loaded rules: the load does not depend on the grants of the gateway role")
			}
		})

		t.Run("5,000 rules load and 5,001 are an error", func(t *testing.T) {
			c := d.SeedTenant(t, "load-many")
			adm := d.Open(t, store.RoleAdmin)
			insert := func(from, to int) {
				t.Helper()
				err := adm.WithTenant(ctx, c, func(ctx context.Context, tx store.Tx) error {
					_, err := tx.Exec(ctx, `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, subject_id, metric, limit_value)
						SELECT $1::uuid, 'key', 'k-' || g, 'requests_per_minute', 10 FROM generate_series($2::int, $3::int) g`, c.String(), from, to)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			insert(1, limits.MaxRulesPerTenant)
			set, err := limits.LoadRules(ctx, gw, c)
			if err != nil {
				t.Fatalf("%d rules: %v", limits.MaxRulesPerTenant, err)
			}
			if _, ok := find(set.For(limits.Query{ServiceAccountID: "", KeyID: "k-5000"}, nil), limits.SubjectKey, "k-5000", limits.RequestsPerMinute); !ok {
				t.Error("the last rule is missing")
			}
			insert(limits.MaxRulesPerTenant+1, limits.MaxRulesPerTenant+1)
			if _, err := limits.LoadRules(ctx, gw, c); err == nil {
				t.Errorf("%d rules loaded", limits.MaxRulesPerTenant+1)
			}
		})
	})
}

func find(ls []limits.Limit, kind limits.SubjectKind, id string, m limits.Metric) (limits.Limit, bool) {
	for _, l := range ls {
		if l.Subject == kind && l.ID == id && l.Metric == m {
			return l, true
		}
	}
	return limits.Limit{}, false
}

// TestLoadRulesColumns is T-662: the loader maps the scope, the currency, the enforcement and the
// thresholds of both tables, and skips a row this version does not know.
func TestLoadRulesColumns(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		a := d.SeedTenant(t, "cols-a")
		gw := d.Open(t, store.RoleGateway)
		cal, err := limits.NewCalendar(time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

		d.SeedPlatformLimit(t, nil, "cost_per_month", 9_000_000_000, dbtest.LimitOptions{Currency: "EUR", Thresholds: []int{90}})
		d.SeedPlatformLimit(t, nil, "tokens_per_day", 5_000_000, dbtest.LimitOptions{Provider: "openai"})
		d.SeedPlatformLimit(t, &a, "tokens_per_day", 1_000_000, dbtest.LimitOptions{Provider: "openai"}) // replaces the default of that scope
		d.SeedLimitRule(t, a, "tenant", "", "tokens_per_day", 400_000, dbtest.LimitOptions{Provider: "openai", Model: "gpt-4", Thresholds: []int{50, 80}})
		d.SeedLimitRule(t, a, "tenant", "", "cost_per_month", 5_000_000_000, dbtest.LimitOptions{Currency: "EUR", NoEnforce: true, Thresholds: []int{100}})
		d.SeedLimitRule(t, a, "key", "k-1", "requests_per_day", 7, dbtest.LimitOptions{Provider: "anthropic"})
		d.SeedLimitRule(t, a, "service_account", "sa-1", "requests_per_month", 70)

		set, err := limits.LoadRules(ctx, gw, a)
		if err != nil {
			t.Fatal(err)
		}
		got := set.For(limits.Query{ServiceAccountID: "sa-1", KeyID: "k-1", Provider: "openai", Model: "gpt-4", Start: start}, cal)
		byKey := map[string]limits.Limit{}
		for _, l := range got {
			byKey[fmt.Sprintf("%s/%s/%s/%s", l.Subject, l.Provider, l.Model, l.Metric)] = l
		}
		if l := byKey["tenant/openai//tokens_per_day"]; l.Max != 1_000_000 || l.Level != limits.LevelPlatform {
			t.Errorf("platform row of the tenant for the provider scope: %+v, want 1,000,000 from the platform", l)
		}
		if l := byKey["tenant/openai/gpt-4/tokens_per_day"]; l.Max != 400_000 || l.Level != limits.LevelTenant || len(l.Marks) != 2 || l.Marks[0].At != 200_000 || l.Marks[1].At != 320_000 {
			t.Errorf("model scope with thresholds: %+v", l)
		}
		l := byKey["tenant///"+string(limits.CostPerMonth)]
		if l.Currency != "EUR" || l.Max != 9_000_000_000 || len(l.Marks) != 2 {
			t.Errorf("a platform ceiling and an unenforced tenant budget on one counter: %+v, want Max 9e9 (the enforced one), two marks", l)
		}
		if l.Period != cal.Of(limits.PerMonth, start) {
			t.Errorf("period %v", l.Period)
		}
		if _, ok := byKey["key/anthropic//requests_per_day"]; ok {
			t.Error("a rule scoped to another provider matched")
		}
		if l := byKey["service_account///requests_per_month"]; l.Max != 70 || l.ID != "sa-1" {
			t.Errorf("service account rule %+v", l)
		}
		anth := set.For(limits.Query{KeyID: "k-1", Provider: "anthropic", Model: "claude", Start: start}, cal)
		found := false
		for _, l := range anth {
			if l.Subject == limits.SubjectKey && l.Provider == "anthropic" && l.Metric == limits.RequestsPerDay && l.Max == 7 {
				found = true
			}
		}
		if !found {
			t.Errorf("the key rule scoped to anthropic does not match anthropic: %+v", anth)
		}

		t.Run("a row this version does not know is skipped", func(t *testing.T) {
			c := d.SeedTenant(t, "cols-unknown")
			super := d.Super(t)
			for _, stmt := range []string{
				`ALTER TABLE pandokos.limit_rule DROP CONSTRAINT limit_rule_metric_valid`,
				`ALTER TABLE pandokos.limit_rule DROP CONSTRAINT limit_rule_soft_by_metric`,
			} {
				if _, err := super.Exec(ctx, stmt); err != nil {
					t.Fatalf("cannot prepare a row of a newer schema: %v", err)
				}
			}
			d.SeedLimitRule(t, c, "tenant", "", "tokens_per_hour", 10)
			d.SeedLimitRule(t, c, "tenant", "", "tokens_per_day", 20)
			set, err := limits.LoadRules(ctx, gw, c)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			ls := set.For(limits.Query{Start: start}, cal)
			if len(ls) != 1 || ls[0].Metric != limits.TokensPerDay || ls[0].Max != 20 {
				t.Errorf("limits %+v, want the known rule only", ls)
			}
		})
	})
}
