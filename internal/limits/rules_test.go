package limits

import "testing"

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
				set:  NewRuleSet(a, mm(TokensPerMinute, int64(1000)), nil, []Rule{{SubjectTenant, "", TokensPerMinute, 500}}), wantCnt: 1,
				wantMax:  map[string]int64{"tenant/tokens_per_minute": 500},
				wantLvls: map[string]string{"tenant/tokens_per_minute": LevelTenant},
			},
			{
				name: "a tenant rule above the platform default has no effect",
				set:  NewRuleSet(a, mm(TokensPerMinute, int64(1000)), nil, []Rule{{SubjectTenant, "", TokensPerMinute, 2000}}), wantCnt: 1,
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
					{SubjectServiceAccount, "sa-1", RequestsPerMinute, 7},
					{SubjectServiceAccount, "sa-2", RequestsPerMinute, 3},
					{SubjectKey, "key-1", ConcurrentRequests, 2},
					{SubjectKey, "key-9", ConcurrentRequests, 1},
				}), wantCnt: 2,
				wantMax: map[string]int64{"service_account/requests_per_minute": 7, "key/concurrent_requests": 2},
			},
			{
				name: "a key rule above the tenant limit is valid and returned",
				set: NewRuleSet(a, nil, nil, []Rule{
					{SubjectTenant, "", RequestsPerMinute, 10},
					{SubjectKey, "key-1", RequestsPerMinute, 1000},
				}), wantCnt: 2,
				wantMax: map[string]int64{"tenant/requests_per_minute": 10, "key/requests_per_minute": 1000},
			},
		} {
			t.Run(c.name, func(t *testing.T) {
				got := c.set.For("sa-1", "key-1")
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
			rules = append(rules, Rule{SubjectKey, "key-1", m, 5}, Rule{SubjectServiceAccount, "sa-1", m, 6}, Rule{SubjectTenant, "", m, 7})
		}
		got := NewRuleSet(a, nil, nil, rules).For("sa-1", "key-1")
		if len(got) != 9 {
			t.Fatalf("%d limits, want 9", len(got))
		}
		i := 0
		for _, k := range SubjectKinds() {
			for _, m := range Metrics() {
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
				for _, m := range Metrics() {
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
					rules = append(rules, Rule{sub.k, sub.id, m, v})
				}
			}
			set := NewRuleSet(a, pd, pt, rules)
			got := set.For("sa-1", "key-1")
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
				for i, m := range Metrics() {
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
			for _, m := range Metrics() {
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
			more = append(more, Rule{SubjectKey, "key-1", RequestsPerMinute, 1})
			for i, r := range more {
				if r.SubjectKind != SubjectTenant {
					more[i].Value = max(1, r.Value/2)
				}
			}
			after := NewRuleSet(a, pd, pt, dedupe(more)).For("sa-1", "key-1")
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
