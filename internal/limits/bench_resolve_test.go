package limits

import (
	"testing"
	"time"
)

// BenchmarkResolve is T-622 (sprint 5.3): the resolution of the limits of a request, rules matched
// by identifier, with and without a scoped rule. No threshold.
func BenchmarkResolve(b *testing.B) {
	zones, err := NewZones(time.UTC)
	if err != nil {
		b.Fatal(err)
	}
	cal, _ := zones.Calendar("")
	tn := tid(1)
	rules := []Rule{
		{ID: "r1", SubjectKind: SubjectTenant, Metric: TokensPerMinute, Value: 1 << 40, Enforce: true},
		{ID: "r2", SubjectKind: SubjectServiceAccount, SubjectID: "sa1", Metric: RequestsPerMinute, Value: 1 << 40, Enforce: true},
		{ID: "r3", SubjectKind: SubjectKey, SubjectID: "k1", Metric: ConcurrentRequests, Value: 1 << 30, Enforce: true},
		{ID: "r4", SubjectKind: SubjectKey, SubjectID: "k1", Metric: RequestsPerDay, Value: 1 << 40, Enforce: true},
		{ID: "r5", SubjectKind: SubjectKey, SubjectID: "k1", Metric: TokensPerMonth, Value: 1 << 40, Enforce: true},
		{ID: "r6", SubjectKind: SubjectKey, SubjectID: "k1", ProviderID: "p1", Provider: "openai", Metric: RequestsPerMinute, Value: 1 << 40, Enforce: true},
		{ID: "r7", SubjectKind: SubjectKey, SubjectID: "k1", ProviderID: "p1", Provider: "openai", ModelID: "m1", Model: "gpt", Metric: TokensPerDay, Value: 1 << 40, Enforce: true},
		{ID: "r8", SubjectKind: SubjectKey, SubjectID: "k1", ProviderID: "p1", Provider: "openai", ModelID: "m1", Model: "gpt", Metric: CostPerDay, Value: 1 << 50, Currency: "USD", Enforce: true},
	}
	set := NewScopedRuleSet(tn, nil, nil, rules)
	set.Calendar = TenantCalendar{Current: cal}
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for name, q := range map[string]Query{
		"scoped":   {ServiceAccountID: "sa1", KeyID: "k1", ProviderID: "p1", ModelID: "m1", Provider: "openai", Model: "gpt", Start: start},
		"unscoped": {ServiceAccountID: "sa1", KeyID: "k1", ProviderID: "p2", ModelID: "m2", Start: start},
		"no rule":  {ServiceAccountID: "sa9", KeyID: "k9", ProviderID: "p2", ModelID: "m2", Start: start},
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if ls := set.For(q); name == "scoped" && len(ls) == 0 {
					b.Fatal("no limit")
				}
			}
		})
	}
}
