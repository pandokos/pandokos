package ledger

import (
	"context"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestWriterPriceSource is T-740: the writer stores the source of the price of a record: tenant,
// catalogue, and NULL when the record names no version.
func TestWriterPriceSource(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		tid := d.SeedTenant(t, "ledger-price-source")
		gateway := d.Open(t, store.RoleGateway)
		base := time.Now().UTC().Add(-time.Minute)
		mk := func(i int, cost pricing.Cost, status string) usage.Record {
			r := testutil.ValidRecord(tid)
			r.StartedAt = base.Add(time.Duration(i) * time.Second).Truncate(time.Microsecond)
			r.ID = usage.NewID(r.StartedAt)
			r.Cost, r.CostStatus = cost, status
			return r
		}
		known := func(currency, source string) pricing.Cost {
			return pricing.Cost{Known: true, Amount: dec(t, "0.5"), Currency: currency, VersionID: testutil.RandomUUID(), Source: source}
		}
		recs := []usage.Record{
			mk(0, known("EUR", pricing.SourceTenant), "known"),
			mk(1, known("USD", pricing.SourceCatalogue), "known"),
			mk(2, pricing.Cost{Reason: pricing.ReasonNoPrice}, "unknown"), // no version: NULL
			mk(3, pricing.Cost{}, "none"),                                 // none: NULL
			mk(4, pricing.Cost{Reason: pricing.ReasonCategoryUnpriced, Currency: "EUR", VersionID: testutil.RandomUUID(), Source: pricing.SourceTenant}, "unknown"), // a version without an amount
		}
		w, err := New(Options{DB: gateway, Logger: telemetry.NewLogger(&testutil.LogBuffer{}, "debug"), BufferSize: len(recs), BatchSize: len(recs), FlushInterval: 50 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { w.Run(context.Background()); close(done) }()
		for _, r := range recs {
			if !w.Admit() {
				t.Fatal("Admit refused")
			}
			w.Record(r)
		}
		testutil.Eventually(t, 10*time.Second, "the records to be written", func() bool { return w.Stats().Written == int64(len(recs)) })
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		w.Close(cctx)
		<-done

		rows := readRows(t, d, tid)
		if len(rows) != len(recs) {
			t.Fatalf("%d rows, want %d", len(rows), len(recs))
		}
		want := []any{pricing.SourceTenant, pricing.SourceCatalogue, nil, nil, pricing.SourceTenant}
		for i, r := range rows {
			got, present := r.fields["price_source"]
			if !present || got != want[i] {
				t.Errorf("record %d: price_source %v (present %v), want %v", i, got, present, want[i])
			}
		}
	})
}
