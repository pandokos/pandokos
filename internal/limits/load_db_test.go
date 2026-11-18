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
		sa1 := d.SeedServiceAccount(t, a, "sa-1")
		k1, _ := d.SeedVirtualKey(t, a, sa1)
		sab := d.SeedServiceAccount(t, b, "sa-b")
		kb, _ := d.SeedVirtualKey(t, b, sab)
		d.SeedLimitRule(t, a, "tenant", "", "tokens_per_minute", 3000)
		d.SeedLimitRule(t, a, "service_account", sa1, "requests_per_minute", 5)
		d.SeedLimitRule(t, a, "key", k1, "concurrent_requests", 2)
		d.SeedLimitRule(t, b, "key", kb, "concurrent_requests", 9)
		d.SeedLimitRule(t, b, "tenant", "", "concurrent_requests", 4)

		got, err := limits.LoadRules(ctx, gw, a, zonesOf(t))
		if err != nil {
			t.Fatal(err)
		}
		want := limits.NewRuleSet(a,
			map[limits.Metric]int64{limits.RequestsPerMinute: 1000, limits.TokensPerMinute: 5000},
			map[limits.Metric]int64{limits.RequestsPerMinute: 50},
			[]limits.Rule{
				{SubjectKind: limits.SubjectTenant, SubjectID: "", Metric: limits.TokensPerMinute, Value: 3000},
				{SubjectKind: limits.SubjectServiceAccount, SubjectID: sa1, Metric: limits.RequestsPerMinute, Value: 5},
				{SubjectKind: limits.SubjectKey, SubjectID: k1, Metric: limits.ConcurrentRequests, Value: 2},
			})
		g, w := got.For(limits.Query{ServiceAccountID: sa1, KeyID: k1}), want.For(limits.Query{ServiceAccountID: sa1, KeyID: k1})
		if len(g) != 4 || len(g) != len(w) {
			t.Fatalf("limits %+v, want %+v", g, w)
		}
		for i := range g {
			g[i].RuleID, w[i].RuleID = "", "" // the rows have identifiers, the rules built by hand do not
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
		for _, l := range got.For(limits.Query{ServiceAccountID: "", KeyID: kb}) {
			if l.Subject != limits.SubjectTenant { // the tenant's own limits apply to every caller
				t.Errorf("a limit for B's key in A's rules: %+v", l)
			}
		}
		if _, ok := find(g, limits.SubjectTenant, "", limits.ConcurrentRequests); ok {
			t.Error("B's platform row or tenant rule is in A's rules")
		}

		gotB, err := limits.LoadRules(ctx, gw, b, zonesOf(t))
		if err != nil {
			t.Fatal(err)
		}
		lb, ok := find(gotB.For(limits.Query{ServiceAccountID: "", KeyID: ""}), limits.SubjectTenant, "", limits.ConcurrentRequests)
		if !ok || lb.Max != 1 || lb.Tenant != b {
			t.Errorf("B's own ceiling and rule: %+v (the smaller, 1, is wanted)", lb)
		}

		t.Run("a tenant without rules gets the defaults", func(t *testing.T) {
			c := d.SeedTenant(t, "load-c")
			set, err := limits.LoadRules(ctx, gw, c, zonesOf(t))
			if err != nil {
				t.Fatal(err)
			}
			if ls := set.For(limits.Query{ServiceAccountID: "", KeyID: ""}); len(ls) != 2 {
				t.Errorf("limits %+v, want the two defaults", ls)
			}
		})

		t.Run("the load runs as the gateway role, in the tenant's session", func(t *testing.T) {
			if _, err := limits.LoadRules(ctx, d.Open(t, store.RoleWorker), a, zonesOf(t)); err == nil {
				t.Error("the worker role loaded rules: the load does not depend on the grants of the gateway role")
			}
		})

		t.Run("5,000 rules load and 5,001 are an error", func(t *testing.T) {
			c := d.SeedTenant(t, "load-many")
			sa := d.SeedServiceAccount(t, c, "many")
			if _, err := d.Super(t).Exec(ctx, `INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash)
				SELECT $1::uuid, $2::uuid, 't' || lpad(translate(g::text, '0123456789', 'abcdefghij'), 16, 'a'), decode(repeat('ab', 32), 'hex')
				  FROM generate_series(1, $3::int) g`, c.String(), sa, limits.MaxRulesPerTenant+1); err != nil {
				t.Fatal(err)
			}
			adm := d.Open(t, store.RoleAdmin)
			insert := func(from, to int) {
				t.Helper()
				err := adm.WithTenant(ctx, c, func(ctx context.Context, tx store.Tx) error {
					_, err := tx.Exec(ctx, `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, virtual_key_id, metric, limit_value)
						SELECT $1::uuid, 'key', k.id, 'requests_per_minute', 10 FROM pandokos.virtual_key k
						 WHERE k.key_id IN (SELECT 't' || lpad(translate(g::text, '0123456789', 'abcdefghij'), 16, 'a') FROM generate_series($2::int, $3::int) g)`, c.String(), from, to)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			insert(1, limits.MaxRulesPerTenant)
			set, err := limits.LoadRules(ctx, gw, c, zonesOf(t))
			if err != nil {
				t.Fatalf("%d rules: %v", limits.MaxRulesPerTenant, err)
			}
			last := "t" + "aaaaaaaaaaaa" + "faaa" // the key of g = 5000: 5 -> f, 0 -> a
			if _, ok := find(set.For(limits.Query{ServiceAccountID: "", KeyID: last}), limits.SubjectKey, last, limits.RequestsPerMinute); !ok {
				t.Error("the last rule is missing")
			}
			insert(limits.MaxRulesPerTenant+1, limits.MaxRulesPerTenant+1)
			if _, err := limits.LoadRules(ctx, gw, c, zonesOf(t)); err == nil {
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
		d.SeedModel(t, "openai", "gpt-4")
		prov, model, anthID := d.ProviderID(t, "openai"), d.ModelID(t, "openai", "gpt-4"), d.ProviderID(t, "anthropic")
		sa1 := d.SeedServiceAccount(t, a, "sa-1")
		k1, _ := d.SeedVirtualKey(t, a, sa1)
		d.SeedLimitRule(t, a, "tenant", "", "tokens_per_day", 400_000, dbtest.LimitOptions{Provider: "openai", Model: "gpt-4", Thresholds: []int{50, 80}})
		d.SeedLimitRule(t, a, "tenant", "", "cost_per_month", 5_000_000_000, dbtest.LimitOptions{Currency: "EUR", NoEnforce: true, Thresholds: []int{100}})
		d.SeedLimitRule(t, a, "key", k1, "requests_per_day", 7, dbtest.LimitOptions{Provider: "anthropic"})
		d.SeedLimitRule(t, a, "service_account", sa1, "requests_per_month", 70)

		set, err := limits.LoadRules(ctx, gw, a, zonesOf(t))
		if err != nil {
			t.Fatal(err)
		}
		got := set.For(limits.Query{ServiceAccountID: sa1, KeyID: k1, ProviderID: prov, ModelID: model, Provider: "openai", Model: "gpt-4", Start: start})
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
		if l := byKey["service_account///requests_per_month"]; l.Max != 70 || l.ID != sa1 {
			t.Errorf("service account rule %+v", l)
		}
		anth := set.For(limits.Query{KeyID: k1, ProviderID: anthID, Provider: "anthropic", Start: start})
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
			set, err := limits.LoadRules(ctx, gw, c, zonesOf(t))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			var ls []limits.Limit
			for _, l := range set.For(limits.Query{Start: start}) {
				if l.Level == limits.LevelTenant { // the platform default seeded above applies to every tenant
					ls = append(ls, l)
				}
			}
			if len(ls) != 1 || ls[0].Metric != limits.TokensPerDay || ls[0].Max != 20 {
				t.Errorf("limits %+v, want the known rule only", ls)
			}
		})
	})
}

// TestLoadRulesTyped is T-703: the loader maps the typed columns of both tables to rules, with the
// identifiers and the names, the key ID of a virtual key through the join, and the calendar of the
// tenant for no zone, a zone, a pending change and one that took effect.
func TestLoadRulesTyped(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		a := d.SeedTenant(t, "typed-a")
		gw := d.Open(t, store.RoleGateway)
		zones, err := limits.NewZones(time.UTC)
		if err != nil {
			t.Fatalf("NewZones: %v", err)
		}
		start := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

		d.SeedModel(t, "openai", "typed-model")
		prov, model := d.ProviderID(t, "openai"), d.ModelID(t, "openai", "typed-model")
		anth := d.ProviderID(t, "anthropic")
		sa := d.SeedServiceAccount(t, a, "typed-sa")
		keyID, _ := d.SeedVirtualKey(t, a, sa)
		issuer := d.SeedTrustedIssuer(t, a, "typed-issuer")
		mapping := d.SeedSubjectMapping(t, a, issuer, false, sa, "typed-subject")

		d.SeedPlatformLimit(t, nil, "tokens_per_day", 5_000_000, dbtest.LimitOptions{Provider: "openai", Model: "typed-model"})
		d.SeedPlatformLimit(t, &a, "requests_per_day", 900, dbtest.LimitOptions{Provider: "anthropic"})
		d.SeedLimitRule(t, a, "service_account", sa, "requests_per_month", 70)
		d.SeedLimitRule(t, a, "key", keyID, "requests_per_day", 7, dbtest.LimitOptions{Provider: "openai", Model: "typed-model"})
		d.SeedLimitRule(t, a, "key", mapping, "tokens_per_day", 77, dbtest.LimitOptions{Provider: "anthropic"})

		t.Run("identifiers, names and the key ID through the join", func(t *testing.T) {
			set, err := limits.LoadRules(ctx, gw, a, zones)
			if err != nil {
				t.Fatal(err)
			}
			cal := mustUTC(t)
			set.Calendar = limits.TenantCalendar{Current: cal} // the scopes below do not depend on the calendar
			got := set.For(limits.Query{ServiceAccountID: sa, KeyID: keyID, ProviderID: prov, ModelID: model, Provider: "openai", Model: "typed-model", Start: start})
			byKey := map[string]limits.Limit{}
			for _, l := range got {
				byKey[fmt.Sprintf("%s/%s/%s", l.Subject, l.ID, l.Metric)] = l
			}
			if l, ok := byKey[fmt.Sprintf("key/%s/requests_per_day", keyID)]; !ok || l.Max != 7 || l.ProviderID != prov || l.ModelID != model || l.Provider != "openai" || l.Model != "typed-model" {
				t.Errorf("the key rule: %+v (the subject ID is the key ID, not the row ID)", l)
			}
			if l, ok := byKey[fmt.Sprintf("service_account/%s/requests_per_month", sa)]; !ok || l.Max != 70 || l.ProviderID != "" || l.ModelID != "" {
				t.Errorf("the service account rule: %+v", l)
			}
			if l, ok := byKey["tenant//tokens_per_day"]; !ok || l.Max != 5_000_000 || l.Level != limits.LevelPlatform || l.ProviderID != prov || l.ModelID != model {
				t.Errorf("the platform default with a scope: %+v", l)
			}
			// The mapping ID is what a token's principal carries as KeyID.
			tok := set.For(limits.Query{KeyID: mapping, ProviderID: anth, Provider: "anthropic", Start: start})
			var found, platform bool
			for _, l := range tok {
				found = found || (l.Subject == limits.SubjectKey && l.ID == mapping && l.Max == 77 && l.ProviderID == anth)
				platform = platform || (l.Level == limits.LevelPlatform && l.Metric == limits.RequestsPerDay && l.Max == 900 && l.ProviderID == anth)
			}
			if !found || !platform {
				t.Errorf("the mapping rule found %v, the platform row of the tenant found %v in %+v", found, platform, tok)
			}
			// Another tenant's loader sees none of it.
			b := d.SeedTenant(t, "typed-b")
			other, err := limits.LoadRules(ctx, gw, b, zones)
			if err != nil || len(other.For(limits.Query{KeyID: keyID, ServiceAccountID: sa, ProviderID: prov, ModelID: model, Start: start})) > 1 {
				t.Errorf("tenant B's rules: %v %v", other, err)
			}
		})

		t.Run("no zone: the default zone, no previous", func(t *testing.T) {
			tn := d.SeedTenant(t, "typed-none")
			set, err := limits.LoadRules(ctx, gw, tn, zones)
			if err != nil {
				t.Fatal(err)
			}
			if c := set.Calendar; zoneOf(c.Current) != "UTC" || c.Previous != nil || !c.From.IsZero() {
				t.Errorf("calendar %s -> %s from %v, want the default (UTC) only", zoneOf(c.Previous), zoneOf(c.Current), c.From)
			}
		})
		t.Run("a zone of its own", func(t *testing.T) {
			tn := d.SeedTenant(t, "typed-zone")
			d.SetTenantZone(t, tn, "Europe/Rome", "", time.Time{})
			set, err := limits.LoadRules(ctx, gw, tn, zones)
			if c := set.Calendar; err != nil || zoneOf(c.Current) != "Europe/Rome" || c.Previous != nil {
				t.Errorf("calendar %s -> %s (%v), want Europe/Rome only", zoneOf(c.Previous), zoneOf(c.Current), err)
			}
		})
		t.Run("a pending change", func(t *testing.T) {
			tn := d.SeedTenant(t, "typed-pending")
			from := time.Now().UTC().Add(10 * 24 * time.Hour).Truncate(time.Microsecond)
			d.SetTenantZone(t, tn, "Asia/Tokyo", "", from)
			set, err := limits.LoadRules(ctx, gw, tn, zones)
			c := set.Calendar
			if err != nil || zoneOf(c.Current) != "Asia/Tokyo" || zoneOf(c.Previous) != "UTC" || !c.From.Equal(from) {
				t.Fatalf("calendar %s -> %s from %v (%v), want UTC until %s, then Asia/Tokyo", zoneOf(c.Previous), zoneOf(c.Current), c.From, err, from)
			}
			if zoneOf(c.At(from.Add(-time.Second))) != "UTC" || zoneOf(c.At(from)) != "Asia/Tokyo" {
				t.Error("At does not follow the instant")
			}
		})
		t.Run("a change that took effect", func(t *testing.T) {
			tn := d.SeedTenant(t, "typed-effective")
			from := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond)
			d.SetTenantZone(t, tn, "Europe/Rome", "Asia/Tokyo", from)
			set, err := limits.LoadRules(ctx, gw, tn, zones)
			c := set.Calendar
			if err != nil || zoneOf(c.Current) != "Europe/Rome" || zoneOf(c.Previous) != "Asia/Tokyo" || !c.From.Equal(from) {
				t.Fatalf("calendar %s -> %s from %v (%v)", zoneOf(c.Previous), zoneOf(c.Current), c.From, err)
			}
			if got := zoneOf(c.At(time.Now())); got != "Europe/Rome" {
				t.Errorf("the zone in force now is %s, not the new one", got)
			}
		})
		t.Run("an unknown zone is a load error", func(t *testing.T) {
			tn := d.SeedTenant(t, "typed-unknown")
			d.SetTenantZone(t, tn, "Mars/Olympus", "", time.Time{})
			if set, err := limits.LoadRules(ctx, gw, tn, zones); err == nil {
				t.Errorf("rules loaded with a zone the binary does not know: %s", zoneOf(set.Calendar.Current))
			}
		})
	})
}

// zoneOf is the name of the zone of a calendar, or "none".
func zoneOf(c *limits.Calendar) string {
	if c == nil {
		return "none"
	}
	return c.Zone()
}

func mustUTC(tb testing.TB) *limits.Calendar {
	tb.Helper()
	c, err := limits.NewCalendar(time.UTC)
	if err != nil {
		tb.Fatal(err)
	}
	return c
}

// zonesOf returns the zones of an installation whose default zone is UTC.
func zonesOf(tb testing.TB) *limits.Zones {
	tb.Helper()
	z, err := limits.NewZones(time.UTC)
	if err != nil {
		tb.Fatalf("NewZones: %v", err)
	}
	return z
}
