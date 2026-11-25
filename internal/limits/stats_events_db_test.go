package limits_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestEventWriterStats is T-922 (events): events recorded, dropped (a full queue refuses the offer;
// the events Close could not write are added) and failed (the database refused the insert).
func TestEventWriterStats(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	a := d.SeedTenant(t, "event-stats")
	nov := utc(2026, 11, 1, 0, 0, 0)
	newWriter := func(t *testing.T) *limits.EventWriter {
		t.Helper()
		w, err := limits.NewEventWriter(limits.EventWriterOptions{DB: d.Open(t, store.RoleGateway), Logger: slog.New(slog.DiscardHandler), Now: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}

	t.Run("recorded and failed", func(t *testing.T) {
		w := newWriter(t)
		if st := w.Stats(); st.Recorded != 0 || st.Dropped != 0 || st.Failed != 0 {
			t.Fatalf("a new writer: %+v", st)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { w.Run(ctx); close(done) }()
		defer func() { cancel(); <-done }()

		for i := 1; i <= 3; i++ {
			if !w.Offer(eventFor(a, ruleUUID(100+i), 80, nov)) {
				t.Fatal("an offer was refused")
			}
		}
		bad := eventFor(a, ruleUUID(104), 80, nov)
		bad.Percent = 0 // the check constraint refuses a threshold of 0%
		w.Offer(bad)
		testutil.Eventually(t, 10*time.Second, "three recorded and one failed", func() bool {
			st := w.Stats()
			return st.Recorded == 3 && st.Failed == 1
		})
		if st := w.Stats(); st.Dropped != 0 {
			t.Errorf("dropped %d: nothing was refused", st.Dropped)
		}
	})

	t.Run("dropped: a full queue, and what Close could not write", func(t *testing.T) {
		w := newWriter(t) // not running: the queue fills
		for i := 0; i < 1024; i++ {
			if !w.Offer(eventFor(a, ruleUUID(200+i), 80, nov)) {
				t.Fatalf("offer %d refused before the queue was full", i)
			}
		}
		for i := 1; i <= 2; i++ {
			if w.Offer(eventFor(a, ruleUUID(2000+i), 80, nov)) {
				t.Fatal("an offer to a full queue was accepted")
			}
			if st := w.Stats(); st.Dropped != int64(i) {
				t.Errorf("after %d refused offers: dropped %d", i, st.Dropped)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Close has no time: everything queued stays unwritten
		unwritten := w.Close(ctx)
		if unwritten <= 0 {
			t.Fatalf("Close left %d unwritten", unwritten)
		}
		if st := w.Stats(); st.Dropped != 2+int64(unwritten) || st.Recorded != 0 {
			t.Errorf("after Close: %+v, want dropped 2 refused + %d unwritten", st, unwritten)
		}
	})
}
