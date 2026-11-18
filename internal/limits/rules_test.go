package limits

import (
	"fmt"
	"reflect"
	"testing"
)

func mm(kv ...any) map[Metric]int64 {
	out := map[Metric]int64{}
	for i := 0; i < len(kv); i += 2 {
		out[kv[i].(Metric)] = kv[i+1].(int64)
	}
	return out
}

func find(ls []Limit, kind SubjectKind, id string, m Metric) (Limit, bool) {
	for _, l := range ls {
		if l.Subject == kind && l.ID == id && l.Metric == m {
			return l, true
		}
	}
	return Limit{}, false
}

// TestResolve is T-619.
// rateMetrics are the three metrics that RuleSet resolves in this version of the rule set: the
// rate and concurrency metrics of sprint 5.1. Metrics returns six more since sprint 5.2.
var rateMetrics = []Metric{ConcurrentRequests, RequestsPerMinute, TokensPerMinute}

func TestResolve(t *testing.T) {
	a := tid(1)

	t.Run("table", func(t *testing.T) {
		for _, c := range []struct {
			name     string
			set      RuleSet
			wantCnt  int
			wantMax  map[string]int64 // "subject/metric" -> Max, checked when present
			wantLvls map[string]string
		}{
			{name: "no rule, no limit", set: NewRuleSet(a, nil, nil, nil), wantCnt: 0},
			{
				name: "a platform default alone limits the tenant",
				set:  NewRuleSet(a, mm(TokensPerMinute, int64(1000)), nil, nil), wantCnt: 1,
				wantMax:  map[string]int64{"tenant/tokens_per_minute": 1000},
				wantLvls: map[string]string{"tenant/tokens_per_minute": LevelPlatform},
			},
			{
				name: "a tenant rule below the platform default wins",
				set:  NewRuleSet(a, mm(TokensPerMinute, int64(1000)), nil, []Rule{{SubjectKind: SubjectTenant, Metric: TokensPerMinute, Value: 500}}), wantCnt: 1,
				wantMax:  map[string]int64{"tenant/tokens_per_minute": 500},
				wantLvls: map[string]string{"tenant/tokens_per_minute": LevelTenant},
			},
			{
				name: "a tenant rule above the platform default has no effect",
				set:  NewRuleSet(a, mm(TokensPerMinute, int64(1000)), nil, []Rule{{SubjectKind: SubjectTenant, Metric: TokensPerMinute, Value: 2000}}), wantCnt: 1,
				wantMax:  map[string]int64{"tenant/tokens_per_minute": 1000},
				wantLvls: map[string]string{"tenant/tokens_per_minute": LevelPlatform},
			},
			{
				name: "a platform row for the tenant raises the default",
				set:  NewRuleSet(a, mm(RequestsPerMinute, int64(100)), mm(RequestsPerMinute, int64(5000)), nil), wantCnt: 1,
				wantMax: map[string]int64{"tenant/requests_per_minute": 5000},
			},
			{
				name: "a platform row for the tenant lowers the default",
				set:  NewRuleSet(a, mm(RequestsPerMinute, int64(100)), mm(RequestsPerMinute, int64(10)), nil), wantCnt: 1,
				wantMax: map[string]int64{"tenant/requests_per_minute": 10},
			},
			{
				name: "the platform row replaces the default for its metric only",
				set: NewRuleSet(a, mm(RequestsPerMinute, int64(100), ConcurrentRequests, int64(8)),
					mm(RequestsPerMinute, int64(10)), nil), wantCnt: 2,
				wantMax: map[string]int64{"tenant/requests_per_minute": 10, "tenant/concurrent_requests": 8},
			},
			{
				name: "a service account and a key rule restrict their subjects",
				set: NewRuleSet(a, nil, nil, []Rule{
					{SubjectKind: SubjectServiceAccount, SubjectID: "sa-1", Metric: RequestsPerMinute, Value: 7},
					{SubjectKind: SubjectServiceAccount, SubjectID: "sa-2", Metric: RequestsPerMinute, Value: 3},
					{SubjectKind: SubjectKey, SubjectID: "key-1", Metric: ConcurrentRequests, Value: 2},
					{SubjectKind: SubjectKey, SubjectID: "key-9", Metric: ConcurrentRequests, Value: 1},
				}), wantCnt: 2,
				wantMax: map[string]int64{"service_account/requests_per_minute": 7, "key/concurrent_requests": 2},
			},
			{
				name: "a key rule above the tenant limit is valid and returned",
				set: NewRuleSet(a, nil, nil, []Rule{
					{SubjectKind: SubjectTenant, SubjectID: "", Metric: RequestsPerMinute, Value: 10},
					{SubjectKind: SubjectKey, SubjectID: "key-1", Metric: RequestsPerMinute, Value: 1000},
				}), wantCnt: 2,
				wantMax: map[string]int64{"tenant/requests_per_minute": 10, "key/requests_per_minute": 1000},
			},
		} {
			t.Run(c.name, func(t *testing.T) {
				got := c.set.For(Query{ServiceAccountID: "sa-1", KeyID: "key-1"}, nil)
				if len(got) != c.wantCnt {
					t.Fatalf("%d limits: %+v, want %d", len(got), got, c.wantCnt)
				}
				for _, l := range got {
					if l.Tenant != a {
						t.Errorf("limit of tenant %s", l.Tenant)
					}
					if l.Subject == SubjectTenant && l.ID != "" {
						t.Errorf("tenant limit with ID %q", l.ID)
					}
					switch l.Subject {
					case SubjectServiceAccount:
						if l.ID != "sa-1" {
							t.Errorf("service account limit for %q", l.ID)
						}
					case SubjectKey:
						if l.ID != "key-1" {
							t.Errorf("key limit for %q", l.ID)
						}
					}
					k := string(l.Subject) + "/" + string(l.Metric)
					if want, ok := c.wantMax[k]; !ok || l.Max != want {
						t.Errorf("%s: Max %d, want %d (present %v)", k, l.Max, want, ok)
					}
					if want, ok := c.wantLvls[k]; ok && l.Level != want {
						t.Errorf("%s: Level %q, want %q", k, l.Level, want)
					}
				}
			})
		}
	})

	t.Run("nine limits in a fixed order", func(t *testing.T) {
		var rules []Rule
		for _, m := range []Metric{TokensPerMinute, RequestsPerMinute, ConcurrentRequests} { // shuffled on purpose
			rules = append(rules, Rule{SubjectKind: SubjectKey, SubjectID: "key-1", Metric: m, Value: 5}, Rule{SubjectKind: SubjectServiceAccount, SubjectID: "sa-1", Metric: m, Value: 6}, Rule{SubjectKind: SubjectTenant, SubjectID: "", Metric: m, Value: 7})
		}
		got := NewRuleSet(a, nil, nil, rules).For(Query{ServiceAccountID: "sa-1", KeyID: "key-1"}, nil)
		if len(got) != 9 {
			t.Fatalf("%d limits, want 9", len(got))
		}
		i := 0
		for _, k := range SubjectKinds() {
			for _, m := range rateMetrics {
				if got[i].Subject != k || got[i].Metric != m {
					t.Fatalf("position %d: %s/%s, want %s/%s", i, got[i].Subject, got[i].Metric, k, m)
				}
				i++
			}
		}
	})

	t.Run("random rule sets", func(t *testing.T) {
		seed := seedOf(t)
		for n := 0; n < 500; n++ {
			rng := rngOf(seed, n)
			val := func() int64 { return int64(1 + rng.IntN(1000)) }
			maybe := func() map[Metric]int64 {
				out := map[Metric]int64{}
				for _, m := range rateMetrics {
					if rng.IntN(2) == 0 {
						out[m] = val()
					}
				}
				return out
			}
			pd, pt := maybe(), maybe()
			var rules []Rule
			for _, sub := range []struct {
				k  SubjectKind
				id string
			}{{SubjectTenant, ""}, {SubjectServiceAccount, "sa-1"}, {SubjectServiceAccount, "sa-2"}, {SubjectKey, "key-1"}, {SubjectKey, "key-2"}} {
				for m, v := range maybe() {
					rules = append(rules, Rule{SubjectKind: sub.k, SubjectID: sub.id, Metric: m, Value: v})
				}
			}
			set := NewRuleSet(a, pd, pt, rules)
			got := set.For(Query{ServiceAccountID: "sa-1", KeyID: "key-1"}, nil)
			if len(got) > 9 {
				t.Fatalf("seed %d case %d: %d limits", seed, n, len(got))
			}
			rank := func(l Limit) int {
				r := 0
				for i, k := range SubjectKinds() {
					if k == l.Subject {
						r = i * 3
					}
				}
				for i, m := range rateMetrics {
					if m == l.Metric {
						r += i
					}
				}
				return r
			}
			for i := 1; i < len(got); i++ {
				if rank(got[i]) <= rank(got[i-1]) {
					t.Fatalf("seed %d case %d: order %+v", seed, n, got)
				}
			}
			rule := func(k SubjectKind, id string, m Metric) (int64, bool) {
				for _, r := range rules {
					if r.SubjectKind == k && r.SubjectID == id && r.Metric == m {
						return r.Value, true
					}
				}
				return 0, false
			}
			for _, m := range rateMetrics {
				// the tenant: the smaller of the platform ceiling and the tenant's own rule
				ceil, hasCeil := pd[m]
				if v, ok := pt[m]; ok {
					ceil, hasCeil = v, true
				}
				own, hasOwn := rule(SubjectTenant, "", m)
				var want int64
				switch {
				case hasCeil && hasOwn:
					want = min(ceil, own)
				case hasCeil:
					want = ceil
				case hasOwn:
					want = own
				}
				l, ok := find(got, SubjectTenant, "", m)
				if ok != (hasCeil || hasOwn) || (ok && l.Max != want) {
					t.Fatalf("seed %d case %d: tenant %s: got %+v (present %v), want %d (ceiling %d/%v own %d/%v)", seed, n, m, l, ok, want, ceil, hasCeil, own, hasOwn)
				}
				if ok && l.Max > own && hasOwn {
					t.Fatalf("seed %d case %d: tenant limit %d above the tenant's own rule %d", seed, n, l.Max, own)
				}
				if ok && hasCeil && l.Max > ceil {
					t.Fatalf("seed %d case %d: tenant limit %d above the platform ceiling %d", seed, n, l.Max, ceil)
				}
				for _, sub := range []struct {
					k  SubjectKind
					id string
				}{{SubjectServiceAccount, "sa-1"}, {SubjectKey, "key-1"}} {
					v, has := rule(sub.k, sub.id, m)
					l, ok := find(got, sub.k, sub.id, m)
					if ok != has || (ok && l.Max != v) {
						t.Fatalf("seed %d case %d: %s %s: got %+v (present %v), want %d (%v)", seed, n, sub.k, m, l, ok, v, has)
					}
				}
			}
			// Adding or lowering a service account or key rule never changes a tenant limit.
			var more []Rule
			more = append(more, rules...)
			more = append(more, Rule{SubjectKind: SubjectKey, SubjectID: "key-1", Metric: RequestsPerMinute, Value: 1})
			for i, r := range more {
				if r.SubjectKind != SubjectTenant {
					more[i].Value = max(1, r.Value/2)
				}
			}
			after := NewRuleSet(a, pd, pt, dedupe(more)).For(Query{ServiceAccountID: "sa-1", KeyID: "key-1"}, nil)
			for _, l := range got {
				if l.Subject != SubjectTenant {
					continue
				}
				l2, ok := find(after, SubjectTenant, "", l.Metric)
				if !ok || l2.Max != l.Max {
					t.Fatalf("seed %d case %d: lowering a lower-level rule changed the tenant limit %+v -> %+v", seed, n, l, l2)
				}
			}
		}
	})
}

// dedupe keeps the last rule of each (subject, metric).
func dedupe(rs []Rule) []Rule {
	type k struct {
		kind SubjectKind
		id   string
		m    Metric
	}
	last := map[k]int{}
	for i, r := range rs {
		last[k{r.SubjectKind, r.SubjectID, r.Metric}] = i
	}
	var out []Rule
	for i, r := range rs {
		if last[k{r.SubjectKind, r.SubjectID, r.Metric}] == i {
			out = append(out, r)
		}
	}
	return out
}

// ---- sprint 5.2: scope, currency, enforcement and thresholds ----------------------------------

type ckey struct {
	kind            SubjectKind
	id, prov, model string
	metric          Metric
	currency        string
}

func ckeyOf(l Limit) ckey {
	return ckey{l.Subject, l.ID, l.Provider, l.Model, l.Metric, l.Currency}
}

func scoped(id string, kind SubjectKind, sid, prov, model string, m Metric, v int64, enforce bool, thr ...int) Rule {
	r := Rule{ID: id, SubjectKind: kind, SubjectID: sid, Provider: prov, Model: model, Metric: m, Value: v, Enforce: enforce, Thresholds: thr}
	if m.Cost() {
		r.Currency = "EUR"
	}
	return r
}

func metricIndex(m Metric) int {
	for i, x := range Metrics() {
		if x == m {
			return i
		}
	}
	return -1
}

func scopeIndex(l Limit) int {
	switch {
	case l.Model != "":
		return 2
	case l.Provider != "":
		return 1
	}
	return 0
}

func subjectIndex(k SubjectKind) int {
	for i, x := range SubjectKinds() {
		if x == k {
			return i
		}
	}
	return -1
}

func mustOne(t *testing.T, ls []Limit) Limit {
	t.Helper()
	if len(ls) == 0 {
		t.Fatal("no limit")
	}
	return ls[0]
}

// TestResolveScopes is T-654.
func TestResolveScopes(t *testing.T) {
	a := tid(1)
	q := Query{ServiceAccountID: "sa-1", KeyID: "key-1", Provider: "openai", Model: "gpt-4", Start: at(2026, 11, 17, 12, 0, 0)}

	t.Run("the order of section 5.3", func(t *testing.T) {
		var rules []Rule
		n := 0
		// Every combination, added in a shuffled order.
		for _, m := range []Metric{CostPerMonth, TokensPerDay, ConcurrentRequests, RequestsPerMonth, RequestsPerMinute, TokensPerMonth, CostPerDay, TokensPerMinute, RequestsPerDay} {
			for _, sc := range [][2]string{{"openai", "gpt-4"}, {"", ""}, {"openai", ""}} {
				for _, sub := range []struct {
					k  SubjectKind
					id string
				}{{SubjectKey, "key-1"}, {SubjectTenant, ""}, {SubjectServiceAccount, "sa-1"}} {
					n++
					rules = append(rules, scoped(fmt.Sprintf("r%d", n), sub.k, sub.id, sc[0], sc[1], m, 1000, true))
				}
			}
		}
		got := NewScopedRuleSet(a, nil, nil, rules).For(q, utcCal)
		if len(got) != 81 {
			t.Fatalf("%d limits, want 81 (three subjects, three scopes, nine metrics)", len(got))
		}
		for i := 1; i < len(got); i++ {
			p, c := got[i-1], got[i]
			pk := [3]int{subjectIndex(p.Subject), scopeIndex(p), metricIndex(p.Metric)}
			ck := [3]int{subjectIndex(c.Subject), scopeIndex(c), metricIndex(c.Metric)}
			if pk[0] > ck[0] || (pk[0] == ck[0] && (pk[1] > ck[1] || (pk[1] == ck[1] && pk[2] >= ck[2]))) {
				t.Fatalf("position %d: %s/%d/%s after %s/%d/%s", i, c.Subject, scopeIndex(c), c.Metric, p.Subject, scopeIndex(p), p.Metric)
			}
		}
		seen := map[ckey]bool{}
		for _, l := range got {
			if seen[ckeyOf(l)] {
				t.Fatalf("two limits for one counter: %+v", l)
			}
			seen[ckeyOf(l)] = true
		}
	})

	t.Run("scope matching", func(t *testing.T) {
		set := NewScopedRuleSet(a, nil, nil, []Rule{
			scoped("p", SubjectTenant, "", "openai", "", RequestsPerDay, 10, true),
			scoped("pa", SubjectTenant, "", "anthropic", "", RequestsPerDay, 11, true),
			scoped("m", SubjectTenant, "", "openai", "gpt-4", TokensPerDay, 20, true),
			scoped("mo", SubjectTenant, "", "openai", "gpt-3", TokensPerDay, 21, true),
			scoped("mx", SubjectTenant, "", "other", "gpt-4", TokensPerDay, 22, true), // the model of another provider
			scoped("k", SubjectKey, "key-1", "openai", "gpt-4", RequestsPerMinute, 5, true),
			scoped("k2", SubjectKey, "key-2", "openai", "gpt-4", RequestsPerMinute, 6, true),
		})
		got := set.For(q, utcCal)
		var ids []string
		for _, l := range got {
			ids = append(ids, l.RuleID)
		}
		if want := []string{"p", "m", "k"}; !reflect.DeepEqual(ids, want) {
			t.Fatalf("rules in force %v, want %v", ids, want)
		}
		for _, l := range got {
			if l.Tenant != a {
				t.Errorf("tenant %v", l.Tenant)
			}
		}
		if l := got[1]; l.Provider != "openai" || l.Model != "gpt-4" || l.Max != 20 || l.Level != LevelTenant || l.Subject != SubjectTenant {
			t.Errorf("the model limit is %+v", l)
		}
		if other := set.For(Query{Provider: "anthropic", Model: "claude", Start: q.Start}, utcCal); len(other) != 1 || other[0].RuleID != "pa" {
			t.Errorf("limits for another provider %+v", other)
		}
	})

	t.Run("the period and the calendar", func(t *testing.T) {
		set := NewScopedRuleSet(a, nil, nil, []Rule{
			scoped("d", SubjectTenant, "", "", "", RequestsPerDay, 10, true),
			scoped("mo", SubjectTenant, "", "", "", TokensPerMonth, 10, true),
			scoped("rate", SubjectTenant, "", "", "", RequestsPerMinute, 10, true),
		})
		got := set.For(q, utcCal)
		if len(got) != 3 {
			t.Fatalf("%d limits", len(got))
		}
		if got[0].Metric != RequestsPerMinute || got[0].Period != (Period{}) {
			t.Errorf("a rate limit has a period: %+v", got[0])
		}
		if want := utcCal.Of(PerDay, q.Start); got[1].Period != want {
			t.Errorf("day period %v, want %v", got[1].Period, want)
		}
		if want := utcCal.Of(PerMonth, q.Start); got[2].Period != want {
			t.Errorf("month period %v, want %v", got[2].Period, want)
		}
		if nocal := set.For(q, nil); len(nocal) != 1 || nocal[0].Metric != RequestsPerMinute {
			t.Errorf("without a calendar: %+v, want the rate limit only", nocal)
		}
	})

	t.Run("a platform and a tenant rule share one counter", func(t *testing.T) {
		plat := []Rule{scoped("pl", SubjectTenant, "", "", "", TokensPerDay, 1000, true, 80)}
		ten := []Rule{scoped("te", SubjectTenant, "", "", "", TokensPerDay, 600, true, 50)}
		got := NewScopedRuleSet(a, plat, nil, ten).For(q, utcCal)
		if len(got) != 1 {
			t.Fatalf("%d limits, want one counter", len(got))
		}
		l := got[0]
		if l.Max != 600 || l.Level != LevelTenant || l.RuleID != "te" {
			t.Errorf("limit %+v, want the tenant's 600", l)
		}
		wantMarks := []Mark{
			{At: 300, Percent: 50, RuleID: "te", Level: LevelTenant, Value: 600},
			{At: 800, Percent: 80, RuleID: "pl", Level: LevelPlatform, Value: 1000},
		}
		if !reflect.DeepEqual(l.Marks, wantMarks) {
			t.Errorf("marks %+v, want %+v", l.Marks, wantMarks)
		}
		// The tenant's rule above the ceiling: the ceiling stays, both sets of marks are kept.
		ten[0].Value = 2000
		l = mustOne(t, NewScopedRuleSet(a, plat, nil, ten).For(q, utcCal))
		if l.Max != 1000 || l.Level != LevelPlatform || l.RuleID != "pl" {
			t.Errorf("limit %+v, want the ceiling of 1000", l)
		}
		if len(l.Marks) != 2 || l.Marks[0].At != 800 || l.Marks[0].RuleID != "pl" || l.Marks[1].At != 1000 || l.Marks[1].RuleID != "te" {
			t.Errorf("marks %+v, want the platform's 800 and the tenant's 1000, ascending", l.Marks)
		}
	})

	t.Run("the minimum of the enforced values; an unenforced rule only marks", func(t *testing.T) {
		plat := []Rule{scoped("pl", SubjectTenant, "", "", "", TokensPerMonth, 1000, true)}
		soft := []Rule{scoped("so", SubjectTenant, "", "", "", TokensPerMonth, 500, false, 100)}
		l := mustOne(t, NewScopedRuleSet(a, plat, nil, soft).For(q, utcCal))
		if l.Max != 1000 || l.RuleID != "pl" || len(l.Marks) != 1 || l.Marks[0].At != 500 || l.Marks[0].Percent != 100 || l.Marks[0].RuleID != "so" {
			t.Errorf("limit %+v", l)
		}
		bothSoft := NewScopedRuleSet(a, []Rule{scoped("ps", SubjectTenant, "", "", "", TokensPerMonth, 1000, false, 90)}, nil, soft).For(q, utcCal)
		if len(bothSoft) != 1 || bothSoft[0].Max != 0 || len(bothSoft[0].Marks) != 2 {
			t.Errorf("two unenforced rules: %+v, want Max 0 and two marks", bothSoft)
		}
	})

	t.Run("the platform row of a tenant replaces the default of its scope and metric", func(t *testing.T) {
		def := []Rule{scoped("d1", SubjectTenant, "", "", "", RequestsPerDay, 100, true), scoped("d2", SubjectTenant, "", "openai", "", RequestsPerDay, 50, true)}
		own := []Rule{scoped("o1", SubjectTenant, "", "", "", RequestsPerDay, 500, true)}
		got := NewScopedRuleSet(a, def, own, nil).For(q, utcCal)
		if len(got) != 2 || got[0].Max != 500 || got[0].RuleID != "o1" || got[1].Max != 50 || got[1].RuleID != "d2" {
			t.Errorf("limits %+v", got)
		}
	})

	t.Run("different currencies are two limits with two counters", func(t *testing.T) {
		plat := []Rule{scoped("pl", SubjectTenant, "", "", "", CostPerDay, 5_000_000, true)}
		ten := scoped("te", SubjectTenant, "", "", "", CostPerDay, 3_000_000, true)
		ten.Currency = "USD"
		got := NewScopedRuleSet(a, plat, nil, []Rule{ten}).For(q, utcCal)
		if len(got) != 2 {
			t.Fatalf("%d limits, want 2: %+v", len(got), got)
		}
		cur := map[string]int64{}
		for _, l := range got {
			cur[l.Currency] = l.Max
		}
		if !reflect.DeepEqual(cur, map[string]int64{"EUR": 5_000_000, "USD": 3_000_000}) {
			t.Errorf("limits by currency %v", cur)
		}
		same := scoped("te2", SubjectTenant, "", "", "", CostPerDay, 3_000_000, true) // EUR, as the ceiling
		got = NewScopedRuleSet(a, plat, nil, []Rule{same}).For(q, utcCal)
		if len(got) != 1 || got[0].Max != 3_000_000 || got[0].Currency != "EUR" {
			t.Errorf("same currency: %+v", got)
		}
	})

	t.Run("a mark is the value times the percentage, rounded up", func(t *testing.T) {
		got := mustOne(t, NewScopedRuleSet(a, nil, nil, []Rule{scoped("r", SubjectTenant, "", "", "", TokensPerDay, 1001, true, 33, 100, 1)}).For(q, utcCal))
		var at []int64
		for _, m := range got.Marks {
			at = append(at, m.At)
		}
		if want := []int64{11, 331, 1001}; !reflect.DeepEqual(at, want) {
			t.Errorf("marks at %v, want %v", at, want)
		}
		big := mustOne(t, NewScopedRuleSet(a, nil, nil, []Rule{scoped("r", SubjectTenant, "", "", "", CostPerMonth, 1_000_000_000_000_000, true, 99)}).For(q, utcCal))
		if len(big.Marks) != 1 || big.Marks[0].At != 990_000_000_000_000 {
			t.Errorf("marks %+v of 10^15 at 99%%", big.Marks)
		}
	})

	t.Run("property: adding a rule never removes or raises a limit", func(t *testing.T) {
		seed := seedOf(t)
		for n := range schedules() {
			rng := rngOf(seed, n)
			metrics := Metrics()
			randRule := func(i int) Rule {
				m := metrics[rng.IntN(len(metrics))]
				kind := SubjectKinds()[rng.IntN(3)]
				sid := ""
				switch kind {
				case SubjectServiceAccount:
					sid = []string{"sa-1", "sa-2"}[rng.IntN(2)]
				case SubjectKey:
					sid = []string{"key-1", "key-2"}[rng.IntN(2)]
				}
				prov := []string{"", "openai", "anthropic"}[rng.IntN(3)]
				model := ""
				if prov != "" && rng.IntN(2) == 0 {
					model = []string{"gpt-4", "gpt-3"}[rng.IntN(2)]
				}
				r := scoped(fmt.Sprintf("r%d", i), kind, sid, prov, model, m, int64(1+rng.IntN(1000)), true)
				if m.Cost() && rng.IntN(3) == 0 {
					r.Currency = "USD"
				}
				if m == ConcurrentRequests || m == RequestsPerMinute || m == TokensPerMinute {
					return r
				}
				if rng.IntN(3) == 0 {
					r.Enforce, r.Thresholds = false, []int{1 + rng.IntN(100)}
				}
				return r
			}
			var plat, rules []Rule
			for i := range rng.IntN(12) {
				r := randRule(i)
				switch rng.IntN(3) {
				case 0:
					r.SubjectKind, r.SubjectID = SubjectTenant, ""
					plat = append(plat, r)
				default:
					rules = append(rules, r)
				}
			}
			dedupeRules := func(rs []Rule) []Rule { // one rule per subject, scope and metric (and currency for the platform)
				seen := map[ckey]bool{}
				var out []Rule
				for _, r := range rs {
					k := ckey{r.SubjectKind, r.SubjectID, r.Provider, r.Model, r.Metric, ""}
					if !seen[k] {
						seen[k] = true
						out = append(out, r)
					}
				}
				return out
			}
			plat, rules = dedupeRules(plat), dedupeRules(rules)
			before := NewScopedRuleSet(a, plat, nil, rules).For(q, utcCal)
			seen := map[ckey]bool{}
			for _, l := range before {
				if seen[ckeyOf(l)] {
					t.Fatalf("seed %d case %d: two limits for the counter %+v", seed, n, ckeyOf(l))
				}
				seen[ckeyOf(l)] = true
			}
			extra := randRule(100)
			var after []Limit
			if rng.IntN(2) == 0 {
				extra.SubjectKind, extra.SubjectID = SubjectTenant, ""
				after = NewScopedRuleSet(a, dedupeRules(append(append([]Rule(nil), plat...), extra)), nil, rules).For(q, utcCal)
			} else {
				after = NewScopedRuleSet(a, plat, nil, dedupeRules(append(append([]Rule(nil), rules...), extra))).For(q, utcCal)
			}
			byKey := map[ckey]Limit{}
			for _, l := range after {
				byKey[ckeyOf(l)] = l
			}
			for _, b := range before {
				l, ok := byKey[ckeyOf(b)]
				if !ok {
					t.Fatalf("seed %d case %d: adding %+v removed the limit %+v", seed, n, extra, b)
				}
				if b.Max > 0 && (l.Max == 0 || l.Max > b.Max) {
					t.Fatalf("seed %d case %d: adding %+v raised %+v to %d", seed, n, extra, b, l.Max)
				}
			}
		}
	})
}
