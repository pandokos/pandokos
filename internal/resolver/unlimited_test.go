package resolver_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/resolver"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// stepClock is a clock the test moves by hand.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *stepClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *stepClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// auditor is a running UnlimitedAuditor on the gateway pool of a database, with its log.
type auditor struct {
	*resolver.UnlimitedAuditor
	logs *logSink
}

func startAuditor(t *testing.T, d *dbtest.Database, clk *stepClock) *auditor {
	t.Helper()
	sink := &logSink{}
	a := resolver.NewUnlimitedAuditor(d.Open(t, store.RoleGateway), slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug})), clk.now)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		cc, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		a.Close(cc)
	})
	return &auditor{a, sink}
}

// rowsOf counts the rows limit.unlimited_traffic of a tenant in each log.
func rowsOf(t *testing.T, d *dbtest.Database, id tenant.ID) (tenantLog, platformLog int) {
	t.Helper()
	tenantLog = d.Count(t, `SELECT count(*) FROM pandokos.audit_event WHERE action = 'limit.unlimited_traffic' AND tenant_id = $1::uuid`, id.String())
	platformLog = d.Count(t, `SELECT count(*) FROM pandokos.platform_audit_event WHERE action = 'limit.unlimited_traffic' AND resource_id = $1`, id.String())
	return
}

func (a *auditor) lines(substr, level string) int {
	n := 0
	for _, l := range strings.Split(a.logs.String(), "\n") {
		if strings.Contains(l, substr) && strings.Contains(l, `"level":"`+level+`"`) {
			n++
		}
	}
	return n
}

func reqID(n int) string { return fmt.Sprintf("%032x", n) }

// TestUnlimitedAuditor is T-707: one call per tenant and day, off the request path.
func TestUnlimitedAuditor(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	clk := &stepClock{t: time.Now().UTC().Truncate(time.Second)}
	today := clk.now().Format("2006-01-02")
	tomorrow := clk.now().AddDate(0, 0, 1).Format("2006-01-02")

	t.Run("one call per tenant and day; the day is the one the caller gives", func(t *testing.T) {
		a := d.SeedTenant(t, "aud-a")
		aud := startAuditor(t, d, clk)
		for i := 0; i < 200; i++ {
			aud.Note(a, today, "Europe/Rome", "openai", "gpt-4o", reqID(i+1))
		}
		testutil.Eventually(t, 5*time.Second, "the first day's row", func() bool { n, p := rowsOf(t, d, a); return n == 1 && p == 1 })
		time.Sleep(200 * time.Millisecond)
		if n, p := rowsOf(t, d, a); n != 1 || p != 1 {
			t.Errorf("%d and %d rows after two hundred notes of one day", n, p)
		}
		if got := aud.lines("unlimited traffic recorded", "INFO"); got != 0 {
			t.Errorf("%d lines 'unlimited traffic recorded': the row is the tenant's audit, not a log line (ADR 0055)", got)
		}
		for key, want := range map[string]string{"day": today, "time_zone": "Europe/Rome", "provider": "openai"} {
			if n := d.Count(t, `SELECT count(*) FROM pandokos.audit_event WHERE action = 'limit.unlimited_traffic' AND tenant_id = $1::uuid AND detail ->> $2 = $3`, a.String(), key, want); n != 1 {
				t.Errorf("the row does not say %s = %s", key, want)
			}
		}
		// The next day of the tenant: one more.
		for i := 0; i < 50; i++ {
			aud.Note(a, tomorrow, "Europe/Rome", "openai", "gpt-4o", reqID(1000+i))
		}
		testutil.Eventually(t, 5*time.Second, "the next day's row", func() bool { n, p := rowsOf(t, d, a); return n == 2 && p == 2 })
		// Two tenants do not share a day.
		b := d.SeedTenant(t, "aud-b")
		aud.Note(b, today, "UTC", "openai", "", reqID(5))
		testutil.Eventually(t, 5*time.Second, "tenant B's row", func() bool { n, p := rowsOf(t, d, b); return n == 1 && p == 1 })
	})

	t.Run("a second auditor on the same database inserts nothing", func(t *testing.T) {
		a := d.SeedTenant(t, "aud-two")
		first, second := startAuditor(t, d, clk), startAuditor(t, d, clk)
		first.Note(a, today, "UTC", "openai", "gpt-4o", reqID(1))
		testutil.Eventually(t, 5*time.Second, "the row", func() bool { n, _ := rowsOf(t, d, a); return n == 1 })
		second.Note(a, today, "UTC", "openai", "gpt-4o", reqID(2)) // after a restart: one call that inserts nothing
		time.Sleep(300 * time.Millisecond)
		if n, p := rowsOf(t, d, a); n != 1 || p != 1 {
			t.Errorf("%d and %d rows from two auditors", n, p)
		}
		if got := second.lines("unlimited traffic recorded", "INFO"); got != 0 {
			t.Errorf("the second auditor says it recorded a row: %d lines", got)
		}
		if got := second.lines("unlimited traffic", "ERROR") + second.lines("unlimited traffic", "WARN"); got != 0 {
			t.Errorf("the second auditor failed: %s", second.logs.String())
		}
	})

	lockLog := func(t *testing.T) (release func()) { return d.LockTable(t, "audit_event") }

	t.Run("a full queue does not block and forgets the day", func(t *testing.T) {
		const n = 600
		ids := d.SeedTenants(t, "aud-many", n)
		aud := startAuditor(t, d, clk)
		release := lockLog(t)
		start := time.Now()
		for i, id := range ids {
			aud.Note(id, today, "UTC", "openai", "gpt-4o", reqID(i+1))
		}
		if took := time.Since(start); took > time.Second {
			t.Errorf("%d notes took %s while the writer was stuck: Note blocks", n, took)
		}
		release()
		// What was in the queue is written; what did not fit was forgotten and is offered again by a later request, 10 s on.
		testutil.Eventually(t, 15*time.Second, "the queued rows", func() bool {
			return d.Count(t, `SELECT count(*) FROM pandokos.audit_event WHERE action = 'limit.unlimited_traffic' AND detail ->> 'day' = $1`, today) >= 200
		})
		clk.advance(11 * time.Second)
		for i, id := range ids {
			aud.Note(id, today, "UTC", "openai", "gpt-4o", reqID(i+1))
			if i%100 == 99 {
				time.Sleep(50 * time.Millisecond) // the queue is bounded: let it drain
			}
		}
		testutil.Eventually(t, 20*time.Second, "every tenant to have its row", func() bool {
			got := d.Count(t, `SELECT count(DISTINCT tenant_id) FROM pandokos.audit_event WHERE action = 'limit.unlimited_traffic' AND detail ->> 'day' = $1
				AND tenant_id = ANY($2::uuid[])`, today, idStrings(ids))
			if got < n {
				// A later request offers the day again (10 s have not passed since the second round for the ones just dropped).
				clk.advance(11 * time.Second)
				for i, id := range ids {
					aud.Note(id, today, "UTC", "openai", "gpt-4o", reqID(i+1))
				}
			}
			return got == n
		})
	})

	t.Run("a failed write is retried by a later request, at most once per 10 s", func(t *testing.T) {
		a := d.SeedTenant(t, "aud-retry")
		aud := startAuditor(t, d, clk)
		release := lockLog(t)
		aud.Note(a, today, "UTC", "openai", "gpt-4o", reqID(1)) // the write waits for the lock and times out after 2 s
		testutil.Eventually(t, 10*time.Second, "the failure to be logged", func() bool { return aud.lines("unlimited traffic", "DEBUG") >= 1 })
		release()
		// Within 10 s of the failure no request starts another attempt.
		clk.advance(5 * time.Second)
		for i := 0; i < 20; i++ {
			aud.Note(a, today, "UTC", "openai", "gpt-4o", reqID(10+i))
		}
		time.Sleep(300 * time.Millisecond)
		if n, _ := rowsOf(t, d, a); n != 0 {
			t.Errorf("a row was written %s after the failure: at most one attempt per 10 s", 5*time.Second)
		}
		clk.advance(6 * time.Second)
		aud.Note(a, today, "UTC", "openai", "gpt-4o", reqID(99))
		testutil.Eventually(t, 5*time.Second, "the retry", func() bool { n, p := rowsOf(t, d, a); return n == 1 && p == 1 })
	})

	t.Run("the request path costs no database call", func(t *testing.T) {
		a := d.SeedTenant(t, "aud-nodb")
		aud := startAuditor(t, d, clk)
		release := lockLog(t)
		defer release()
		start := time.Now()
		for i := 0; i < 1000; i++ {
			aud.Note(a, today, "UTC", "openai", "gpt-4o", reqID(i+1))
		}
		if took := time.Since(start); took > 500*time.Millisecond {
			t.Errorf("a thousand notes of one tenant took %s", took)
		}
	})
}

func idStrings(ids []tenant.ID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}
