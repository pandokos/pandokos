package limits_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

func eventFor(a tenant.ID, rule string, percent int, periodStart time.Time) limits.Event {
	kind := limits.EventThresholdReached
	if percent == 0 {
		kind = limits.EventExhausted
	}
	return limits.Event{Tenant: a, Kind: kind, RuleID: rule, Level: limits.LevelTenant, Subject: limits.SubjectKey, SubjectID: "k-event",
		Provider: "openai", Model: "gpt-4", Metric: limits.CostPerMonth, Currency: "EUR",
		Period: limits.Period{Start: periodStart, End: periodStart.AddDate(0, 1, 0)}, Percent: percent, Value: 250_500_000, Observed: pricing.Decimal{}}
}

func ruleUUID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

func countEvents(t *testing.T, d *dbtest.Database, a tenant.ID) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var n int
	err := d.Open(t, store.RoleAdmin).WithTenant(ctx, a, func(ctx context.Context, tx store.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM pandokos.limit_event`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestEventWriter is T-660.
func TestEventWriter(t *testing.T) {
	t.Parallel()
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		a := d.SeedTenant(t, "event-a")
		b := d.SeedTenant(t, "event-b")
		logs := &safeBuf{}
		newWriter := func(t *testing.T) *limits.EventWriter {
			t.Helper()
			w, err := limits.NewEventWriter(limits.EventWriterOptions{DB: d.Open(t, store.RoleGateway),
				Logger: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Now: time.Now})
			if err != nil {
				t.Fatal(err)
			}
			return w
		}
		runWriter := func(t *testing.T, w *limits.EventWriter) (stop func()) {
			t.Helper()
			rctx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { w.Run(rctx); close(done) }()
			return func() {
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("Run did not return")
				}
			}
		}
		nov := utc(2026, 11, 1, 0, 0, 0)

		t.Run("one row per rule, period, kind and percentage however often offered", func(t *testing.T) {
			w := newWriter(t)
			stop := runWriter(t, w)
			defer stop()
			obs, _ := pricing.ParseDecimal("201.25")
			for range 5 {
				e := eventFor(a, ruleUUID(1), 80, nov)
				e.Observed = obs
				if !w.Offer(e) {
					t.Fatal("the queue refused an event")
				}
			}
			w.Offer(eventFor(a, ruleUUID(1), 100, nov))                 // another percentage
			w.Offer(eventFor(a, ruleUUID(1), 0, nov))                   // another kind
			w.Offer(eventFor(a, ruleUUID(2), 80, nov))                  // another rule
			w.Offer(eventFor(a, ruleUUID(1), 80, nov.AddDate(0, 1, 0))) // another period
			testutil.Eventually(t, 10*time.Second, "five rows", func() bool { return countEvents(t, d, a) >= 5 })
			time.Sleep(200 * time.Millisecond)
			if n := countEvents(t, d, a); n != 5 {
				t.Errorf("%d rows, want 5", n)
			}
			// What was written.
			var kind, level, subjectKind, subjectID, provider, model, metric, currency string
			var percent int16
			var value int64
			var observed string
			var ps, pe time.Time
			err := d.Open(t, store.RoleAdmin).WithTenant(ctx, a, func(ctx context.Context, tx store.Tx) error {
				return tx.QueryRow(ctx, `SELECT kind, rule_level, subject_kind, subject_id, provider, model, metric, currency,
						period_start, period_end, percent, limit_value, observed::text
					FROM pandokos.limit_event WHERE rule_id = $1::uuid AND percent = 80 AND period_start = $2`, ruleUUID(1), nov).
					Scan(&kind, &level, &subjectKind, &subjectID, &provider, &model, &metric, &currency, &ps, &pe, &percent, &value, &observed)
			})
			if err != nil {
				t.Fatal(err)
			}
			if kind != "threshold_reached" || level != "tenant" || subjectKind != "key" || subjectID != "k-event" || provider != "openai" || model != "gpt-4" ||
				metric != "cost_per_month" || currency != "EUR" || percent != 80 || value != 250_500_000 || !ps.Equal(nov) || !pe.Equal(nov.AddDate(0, 1, 0)) {
				t.Errorf("row: %s %s %s %s %s %s %s %s %v %v %d %d", kind, level, subjectKind, subjectID, provider, model, metric, currency, ps, pe, percent, value)
			}
			if d, _ := pricing.ParseDecimal(observed); d.String() != "201.25" {
				t.Errorf("observed %s, want 201.25", observed)
			}
			// Nothing of another tenant.
			if n := countEvents(t, d, b); n != 0 {
				t.Errorf("tenant B has %d events", n)
			}
		})

		t.Run("the row and the log line name no caller", func(t *testing.T) {
			w := newWriter(t)
			stop := runWriter(t, w)
			defer stop()
			e := eventFor(b, ruleUUID(7), 50, nov)
			e.SubjectID = "k-log-canary"
			w.Offer(e)
			testutil.Eventually(t, 10*time.Second, "the event row", func() bool { return countEvents(t, d, b) == 1 })
			// A recorded event writes no line: the event is the tenant's (ADR 0055).
			time.Sleep(100 * time.Millisecond)
			for _, no := range []string{"limit event recorded", b.String(), "k-log-canary"} {
				if strings.Contains(logs.String(), no) {
					t.Errorf("the log holds %s: %s", no, logs.String())
				}
			}
		})

		t.Run("an event the database refuses is dropped with one log line", func(t *testing.T) {
			w := newWriter(t)
			stop := runWriter(t, w)
			defer stop()
			bad := eventFor(a, ruleUUID(30), 80, nov)
			bad.Percent = 0 // kind threshold_reached with percent 0: the check constraint refuses it
			w.Offer(bad)
			w.Offer(eventFor(a, ruleUUID(31), 80, nov)) // the next one still goes through
			testutil.Eventually(t, 10*time.Second, "the good event after the bad one", func() bool {
				var n int
				_ = d.Open(t, store.RoleAdmin).WithTenant(ctx, a, func(ctx context.Context, tx store.Tx) error {
					return tx.QueryRow(ctx, `SELECT count(*) FROM pandokos.limit_event WHERE rule_id = $1::uuid`, ruleUUID(31)).Scan(&n)
				})
				return n == 1
			})
			time.Sleep(100 * time.Millisecond)
			lines := 0
			for _, l := range strings.Split(logs.String(), "\n") {
				if strings.Contains(l, "limit event not recorded") {
					lines++
					if !strings.Contains(l, `"level":"ERROR"`) {
						t.Errorf("not an error line: %s", l)
					}
				}
			}
			if lines != 1 {
				t.Errorf("%d log lines for one refused event, want 1", lines)
			}
		})

		t.Run("a full queue refuses the offer", func(t *testing.T) {
			w := newWriter(t) // not running: nothing drains the queue
			for i := range 1024 {
				if !w.Offer(eventFor(a, ruleUUID(1000+i), 80, nov)) {
					t.Fatalf("offer %d refused: the queue holds 1,024", i+1)
				}
			}
			if w.Offer(eventFor(a, ruleUUID(5000), 80, nov)) {
				t.Error("offer 1,025 was taken")
			}
		})

		t.Run("close flushes what is queued", func(t *testing.T) {
			w := newWriter(t)
			before := countEvents(t, d, a)
			for i := range 10 {
				w.Offer(eventFor(a, ruleUUID(2000+i), 90, nov))
			}
			cctx, stop := context.WithTimeout(ctx, 20*time.Second)
			defer stop()
			if left := w.Close(cctx); left != 0 {
				t.Errorf("%d events left unwritten", left)
			}
			if n := countEvents(t, d, a); n != before+10 {
				t.Errorf("%d rows, want %d", n, before+10)
			}
		})

		t.Run("retry while the database is unavailable", func(t *testing.T) {
			w := newWriter(t)
			stop := runWriter(t, w)
			defer stop()
			before := countEvents(t, d, a)
			// The reader is opened now and tolerates errors while it recovers: behind the pooler the
			// failed logins are cached for a while after the database accepts connections again.
			admin := d.Open(t, store.RoleAdmin)
			d.SetAcceptingConnections(t, false)
			for i := range 3 {
				w.Offer(eventFor(a, ruleUUID(3000+i), 70, nov))
			}
			time.Sleep(400 * time.Millisecond)
			d.SetAcceptingConnections(t, true)
			testutil.Eventually(t, 30*time.Second, "the events to be written once the database is back", func() bool {
				rctx, stop := context.WithTimeout(ctx, 5*time.Second)
				defer stop()
				var n int
				err := admin.WithTenant(rctx, a, func(ctx context.Context, tx store.Tx) error {
					return tx.QueryRow(ctx, `SELECT count(*) FROM pandokos.limit_event`).Scan(&n)
				})
				return err == nil && n == before+3
			})
		})

		t.Run("an event interrupted when Run stops is kept for Close, even with a full queue", func(t *testing.T) {
			w := newWriter(t)
			before := countEvents(t, d, a)
			d.SetAcceptingConnections(t, false)
			defer d.SetAcceptingConnections(t, true)
			stop := runWriter(t, w)
			w.Offer(eventFor(a, ruleUUID(7000), 60, nov))
			time.Sleep(300 * time.Millisecond) // Run took it and retries against the database that is down
			for i := range 1024 {
				if !w.Offer(eventFor(a, ruleUUID(7001+i), 60, nov)) {
					t.Fatalf("offer %d refused", i+1)
				}
			}
			stop()
			cctx, cancelClose := context.WithTimeout(ctx, 400*time.Millisecond)
			defer cancelClose()
			if left := w.Close(cctx); left != 1025 {
				t.Errorf("%d events left unwritten, want 1025: the interrupted one and the 1,024 queued", left)
			}
			// The database is back: the interrupted event is written first, then the queue.
			d.SetAcceptingConnections(t, true)
			wctx, cancelWrite := context.WithTimeout(ctx, 60*time.Second)
			defer cancelWrite()
			if left := w.Close(wctx); left != 0 {
				t.Errorf("%d events left after the database came back", left)
			}
			if n := countEvents(t, d, a); n != before+1025 {
				t.Errorf("%d rows, want %d", n, before+1025)
			}
		})

		t.Run("close gives up within its context and reports what it left", func(t *testing.T) {
			w := newWriter(t)
			d.SetAcceptingConnections(t, false)
			defer d.SetAcceptingConnections(t, true)
			for i := range 4 {
				w.Offer(eventFor(a, ruleUUID(4000+i), 60, nov))
			}
			cctx, stop := context.WithTimeout(ctx, 600*time.Millisecond)
			defer stop()
			start := time.Now()
			if left := w.Close(cctx); left != 4 {
				t.Errorf("%d events left, want 4", left)
			}
			if time.Since(start) > 3*time.Second {
				t.Errorf("close took %v", time.Since(start))
			}
		})
	})
}

// TestEventLinesHoldNoTenantData is the part of T-739 that belongs to the event writer, as ADR 0055
// redefines it: a recorded event writes no line (the event is the tenant's), and the line of an event
// the database refuses names its kind and the error, never the tenant, the rule or the metric.
func TestEventLinesHoldNoTenantData(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	a := d.SeedTenant(t, "event-lines")
	logs := &safeBuf{}
	w, err := limits.NewEventWriter(limits.EventWriterOptions{DB: d.Open(t, store.RoleGateway),
		Logger: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	nov := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	rule := ruleUUID(9)
	w.Offer(eventFor(a, rule, 50, nov))
	w.Offer(eventFor(a, "not-a-uuid", 80, nov)) // the database refuses it: `limit event not recorded`
	testutil.Eventually(t, 10*time.Second, "the refusal line", func() bool {
		return strings.Contains(logs.String(), "limit event not recorded")
	})
	text := logs.String()
	if strings.Contains(text, "limit event recorded") {
		t.Errorf("a recorded event wrote a line: %s", text)
	}
	if !strings.Contains(text, `"kind":"threshold_reached"`) {
		t.Errorf("the refusal line lacks the kind: %s", text)
	}
	for _, no := range []string{a.String(), rule, "not-a-uuid", `"metric"`, `"subject_kind"`, `"limit_scope"`, `"percent"`, `"tenant"`, `"rule_id"`} {
		if strings.Contains(text, no) {
			t.Errorf("the log holds %s: %s", no, text)
		}
	}
}
