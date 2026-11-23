package ledger_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

// setOutboxTrigger enables or disables the statement trigger that turns a batch of usage records into
// events, as the owner of the test database (the lint L-23 is what keeps it enabled in production).
func setOutboxTrigger(tb testing.TB, d *dbtest.Database, enabled bool) {
	tb.Helper()
	verb := "DISABLE"
	if enabled {
		verb = "ENABLE"
	}
	if _, err := d.Super(tb).Exec(context.Background(), `ALTER TABLE pandokos.usage_record `+verb+` TRIGGER usage_record_outbox`); err != nil {
		tb.Fatalf("%s the trigger: %v", verb, err)
	}
}

func timeBatch(tb testing.TB, d *dbtest.Database, id tenant.ID, n int) time.Duration {
	tb.Helper()
	start := time.Now()
	d.SeedUsageRecords(tb, id, n)
	return time.Since(start)
}

func median(ds []time.Duration) time.Duration {
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return ds[len(ds)/2]
}

// TestOutboxWriteCost is T-1050: batches of 500 records through the statement of the ledger writer
// with the trigger enabled and disabled, the medians of 20: the ratio is below 3; the two figures
// are logged (and written in the guide of operations).
func TestOutboxWriteCost(t *testing.T) {
	d := dbtest.NewMigrated(t)
	id := d.SeedTenant(t, "cost")
	// A few batches to warm the connections, the partitions and the plans.
	for i := 0; i < 3; i++ {
		timeBatch(t, d, id, 500)
	}
	var with, without []time.Duration
	for i := 0; i < 20; i++ {
		setOutboxTrigger(t, d, true)
		with = append(with, timeBatch(t, d, id, 500))
		setOutboxTrigger(t, d, false)
		without = append(without, timeBatch(t, d, id, 500))
	}
	setOutboxTrigger(t, d, true)
	mw, mo := median(with), median(without)
	t.Logf("a batch of 500 records: median %v without the trigger, %v with it (ratio %.2f)", mo, mw, float64(mw)/float64(mo))
	if mo <= 0 || float64(mw) >= 3*float64(mo) {
		t.Errorf("the median of a batch is %v with the trigger and %v without: the cost must stay below three times", mw, mo)
	}
	// The trigger did its work for the batches that had it: 500 events each; none for the others.
	if n := len(d.OutboxEvents(t, id)); n != (3+20)*500 {
		t.Errorf("%d events for %d records written with the trigger enabled, want one each", n, (3+20)*500)
	}
}

// BenchmarkLedgerBatchOutbox measures a batch of 500 records with and without the trigger: the figure
// of record written in the guide of operations (make bench; it needs make db-up).
func BenchmarkLedgerBatchOutbox(b *testing.B) {
	for _, c := range []struct {
		name    string
		trigger bool
	}{{"without", false}, {"with", true}} {
		b.Run(c.name, func(b *testing.B) {
			d := dbtest.NewMigrated(b)
			id := d.SeedTenant(b, "bench")
			setOutboxTrigger(b, d, c.trigger)
			defer setOutboxTrigger(b, d, true)
			timeBatch(b, d, id, 500)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d.SeedUsageRecords(b, id, 500)
			}
		})
	}
}
