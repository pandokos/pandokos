package limits

import (
	"context"
	"testing"
	"time"
)

// BenchmarkReserveSettle is T-622: ns and allocations per reserve-and-settle with nine limits,
// sequential and parallel on one tenant and on many. No threshold.
func BenchmarkReserveSettle(b *testing.B) {
	run := func(b *testing.B, tenants int, parallel bool) {
		clk := newClock()
		m, _ := newMemory(b, clk)
		sets := make([][]Limit, tenants)
		for i := range sets {
			sets[i] = nineLimits(tid(byte(i+1)), 1<<30, 1<<40, 1<<40)
		}
		ctx := context.Background()
		one := func(i int) {
			res, ref, err := m.Reserve(ctx, request("bench", time.Hour, 100, sets[i%tenants]...))
			if err != nil || ref != nil {
				b.Errorf("reserve: %v %v", err, ref)
				return
			}
			if err := m.Settle(ctx, res, Amounts{Tokens: 90}); err != nil {
				b.Error(err)
			}
		}
		b.ReportAllocs()
		b.ResetTimer()
		if !parallel {
			for i := 0; i < b.N; i++ {
				one(i)
			}
			return
		}
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				one(i)
				i++
			}
		})
	}
	// A period case (sprint 5.2): nine limits of which six are period limits, one of them a budget.
	period := func(b *testing.B, parallel bool) {
		clk := newClock()
		m, _ := newMemory(b, clk)
		tn := tid(1)
		day, mon := utcCal.Of(PerDay, clk.now()), utcCal.Of(PerMonth, clk.now())
		m.SetCut(tn, day.Start)
		ls := []Limit{
			lim(tn, SubjectTenant, "", ConcurrentRequests, 1<<30), lim(tn, SubjectTenant, "", RequestsPerMinute, 1<<40), lim(tn, SubjectTenant, "", TokensPerMinute, 1<<40),
			perLimit(tn, SubjectTenant, "", RequestsPerDay, 1<<40, day), perLimit(tn, SubjectTenant, "", TokensPerDay, 1<<40, day),
			perLimit(tn, SubjectKey, "k", RequestsPerMonth, 1<<40, mon), perLimit(tn, SubjectKey, "k", TokensPerMonth, 1<<40, mon),
			costLimit(tn, CostPerDay, "USD", 1<<50, day), costLimit(tn, CostPerMonth, "USD", 1<<50, mon),
		}
		m.Load(tn, mon, []Limit{ls[5], ls[6], ls[8]}, nil)
		ctx := context.Background()
		est := known("USD", micro(1000))
		one := func() {
			res, ref, err := m.Reserve(ctx, periodReq("bench", clk.now(), time.Hour, 100, est, ls...))
			if err != nil || ref != nil {
				b.Errorf("reserve: %v %v", err, ref)
				return
			}
			if err := m.Settle(ctx, res, Amounts{Tokens: 90, Cost: est}); err != nil {
				b.Error(err)
			}
		}
		b.ReportAllocs()
		b.ResetTimer()
		if !parallel {
			for i := 0; i < b.N; i++ {
				one()
			}
			return
		}
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				one()
			}
		})
	}
	b.Run("period-sequential", func(b *testing.B) { period(b, false) })
	b.Run("period-parallel", func(b *testing.B) { period(b, true) })
	b.Run("sequential", func(b *testing.B) { run(b, 1, false) })
	b.Run("parallel-one-tenant", func(b *testing.B) { run(b, 1, true) })
	b.Run("parallel-many-tenants", func(b *testing.B) { run(b, 64, true) })
}
