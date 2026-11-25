package limits_test

import (
	"context"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/reports"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/test/dbtest"
)

// TestLedgerReaderUnchanged is T-951: the reader of the limits gives the same groups, and Sum the
// same amounts, whether the usage rollup is current, half current or empty (sprint 6.2, section
// 5.2): the rollup makes the reads faster and never different. The reconciler, rebase and
// capability tests of earlier sprints run unchanged beside this one and must stay green.
func TestLedgerReaderUnchanged(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	type result struct {
		groups   []string
		requests int64
		tokens   int64
		cost     string
	}
	run := func(t *testing.T, mode string) result {
		d := dbtest.NewMigrated(t)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		tid := d.SeedTenant(t, "reader-"+mode)
		d.SeedModel(t, "openai", "reader-model")
		worker := d.Open(t, store.RoleWorker)
		roll := &reports.Rollup{DB: worker, Interval: time.Minute, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		sa := "0192f0f0-0000-7000-8000-000000000001"
		batch := func(recorded time.Time, startedAgo time.Duration) {
			var rows []dbtest.UsageRow
			for i := 0; i < 12; i++ {
				rows = append(rows, dbtest.UsageRow{StartedAt: now.Add(-startedAgo - time.Duration(i)*7*time.Minute), RecordedAt: recorded, PrincipalID: sa,
					Model: "reader-model", InputTokens: int64(10 + i), OutputTokens: 5, Cost: "0.25", Currency: "EUR"})
			}
			d.SeedUsage(t, tid, rows...)
		}
		switch mode {
		case "empty": // nothing is rolled up
			batch(now.Add(-30*time.Minute), 3*time.Hour)
			batch(now.Add(-20*time.Minute), 30*time.Minute)
		case "half": // the first batch is rolled up, the second is not
			batch(now.Add(-30*time.Minute), 3*time.Hour)
			if err := roll.Pass(ctx); err != nil {
				t.Fatal(err)
			}
			// Recorded at the watermark: records are recorded at the database's clock, so none is
			// recorded before the watermark of a pass that has run (section 5.1).
			var wm time.Time
			if err := d.Super(t).QueryRow(ctx, `SELECT watermark FROM pandokos.usage_rollup_state WHERE tenant_id = $1::uuid`, tid.String()).Scan(&wm); err != nil {
				t.Fatal(err)
			}
			batch(wm, 30*time.Minute)
		case "current":
			batch(now.Add(-30*time.Minute), 3*time.Hour)
			batch(now.Add(-20*time.Minute), 30*time.Minute)
			if err := roll.Pass(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if mode != "empty" {
			var n int
			if err := d.Super(t).QueryRow(ctx, `SELECT count(*) FROM pandokos.usage_rollup WHERE tenant_id = $1::uuid`, tid.String()).Scan(&n); err != nil || n == 0 {
				t.Fatalf("mode %s: the rollup holds %d rows (%v): the state of the database is not the one the test names", mode, n, err)
			}
		}
		reader := limits.NewLedger(worker)
		from, liveFrom, to := now.Add(-6*time.Hour).Truncate(time.Hour), now.Add(-12*time.Minute), now.Add(time.Minute)
		groups, err := reader.Usage(ctx, tid, from, liveFrom, to)
		if err != nil {
			t.Fatal(err)
		}
		var res result
		for _, g := range groups {
			res.groups = append(res.groups, strings.Join([]string{string(rune('0' + g.Minute%10)), g.PrincipalID, g.KeyID, g.Currency, g.Cost.String()}, "|"))
		}
		sort.Strings(res.groups)
		// The ledger reader of the old body, read by the same session, is the reference.
		var ref []queries.AggregateUsageForLimitsFromLedgerRow
		if err := worker.WithTenant(ctx, tid, func(c context.Context, tx store.Tx) error {
			var err error
			ref, err = queries.New(tx).AggregateUsageForLimitsFromLedger(c, queries.AggregateUsageForLimitsFromLedgerParams{FromTime: from, LiveFrom: liveFrom, ToTime: to})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		var refRequests int64
		for _, r := range ref {
			refRequests += r.Requests
		}
		sum := limits.Sum(limits.Limit{Metric: limits.RequestsPerDay}, groups)
		res.requests = sum.N
		res.tokens = limits.Sum(limits.Limit{Metric: limits.TokensPerDay}, groups).N
		res.cost = limits.Sum(limits.Limit{Metric: limits.CostPerDay, Currency: "EUR"}, groups).Cost.String()
		if refRequests != res.requests || res.requests != 24 {
			t.Errorf("mode %s: Sum counts %d requests, the ledger reader %d, want 24", mode, res.requests, refRequests)
		}
		return res
	}
	empty, half, current := run(t, "empty"), run(t, "half"), run(t, "current")
	for name, got := range map[string]result{"half": half, "current": current} {
		if strings.Join(got.groups, "\n") != strings.Join(empty.groups, "\n") || got.requests != empty.requests || got.tokens != empty.tokens || got.cost != empty.cost {
			t.Errorf("a rollup that is %s changes the answer:\n%+v\nwhereas with an empty rollup:\n%+v", name, got, empty)
		}
	}
}
