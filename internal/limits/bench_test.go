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
	b.Run("sequential", func(b *testing.B) { run(b, 1, false) })
	b.Run("parallel-one-tenant", func(b *testing.B) { run(b, 1, true) })
	b.Run("parallel-many-tenants", func(b *testing.B) { run(b, 64, true) })
}
