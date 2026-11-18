package resolver_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/resolver"
	"github.com/pandokos/pandokos/internal/tenant"
)

// BenchmarkReserve is T-622 (sprint 5.3): the limiter of the gateway for a request that meets
// limits (the lookup of the unlimited-traffic recorder is not on that path: allocations must not
// grow with it) and for a request that meets none (a day already noted costs one map lookup).
func BenchmarkReserve(b *testing.B) {
	zones, err := limits.NewZones(time.UTC)
	if err != nil {
		b.Fatal(err)
	}
	cal, _ := zones.Calendar("")
	rules := []limits.Rule{
		{SubjectKind: limits.SubjectKey, SubjectID: "key-1", Metric: limits.RequestsPerMinute, Value: 1 << 40, Enforce: true},
		{SubjectKind: limits.SubjectKey, SubjectID: "key-1", ProviderID: "p1", ModelID: "m1", Provider: "openai", Model: "gpt", Metric: limits.TokensPerMinute, Value: 1 << 40, Enforce: true},
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	cache, err := limits.NewRuleCacheFunc(func(_ context.Context, tn tenant.ID) (limits.RuleSet, error) {
		s := limits.NewScopedRuleSet(tn, nil, nil, rules)
		s.Calendar = limits.TenantCalendar{Current: cal}
		return s, nil
	}, limits.RuleCacheOptions{Logger: quiet, TTL: time.Minute})
	if err != nil {
		b.Fatal(err)
	}
	est := gateway.LimitEstimate{RequestID: "req", ProviderID: "p1", ModelID: "m1", Provider: "openai", Model: "gpt", InputTokens: 10, OutputTokens: 10,
		Start: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	tn := limitTenantID(1)
	run := func(b *testing.B, key string, withAuditor bool) {
		o := resolver.LimitsOptions{Rules: cache, Store: &fakeStore{}, TTL: time.Minute, Logger: quiet}
		if withAuditor {
			o.Unlimited = resolver.NewUnlimitedAuditor(nil, quiet, nil)
		}
		l, err := resolver.NewLimits(o)
		if err != nil {
			b.Fatal(err)
		}
		p := limitPrincipal(tn, "sa-1", key, "")
		ctx := context.Background()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			res, ref, _, err := l.Reserve(ctx, p, est)
			if err != nil || ref != nil {
				b.Fatal(err, ref)
			}
			res.Release()
		}
	}
	b.Run("limited", func(b *testing.B) { run(b, "key-1", true) })
	b.Run("limited-no-recorder", func(b *testing.B) { run(b, "key-1", false) })
	b.Run("unlimited", func(b *testing.B) { run(b, "key-9", true) })
}
