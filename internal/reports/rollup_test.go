package reports_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/reports"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

// logBuffer is a log sink the tests read lines from.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) lines(t testing.TB, msg string) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("a log line that is not JSON: %q", l)
		}
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

func (b *logBuffer) text() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func jsonLogger(b *logBuffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// aged returns n records recorded and started a while ago, so that a rollup pass takes them.
func aged(n int, started, recorded time.Time) []rec {
	out := make([]rec, n)
	for i := range out {
		out[i] = rec{Started: started.Add(time.Duration(i) * time.Second), Recorded: recorded, Principal: uuidOf(1), Provider: "openai", Credential: true,
			InSource: "provider", OutSource: "provider", In: 10, Out: 5, Outcome: "ok"}
	}
	return out
}

func rollupTotals(t testing.TB, d *dbtest.Database, tid tenant.ID) (requests, rows int64, watermark *time.Time) {
	t.Helper()
	c := ctx(t)
	if err := d.SharedSuper(t).QueryRow(c, `SELECT COALESCE(sum(requests), 0), count(*) FROM pandokos.usage_rollup WHERE tenant_id = $1::uuid`, tid.String()).Scan(&requests, &rows); err != nil {
		t.Fatal(err)
	}
	var w time.Time
	if err := d.SharedSuper(t).QueryRow(c, `SELECT watermark FROM pandokos.usage_rollup_state WHERE tenant_id = $1::uuid`, tid.String()).Scan(&w); err == nil {
		watermark = &w
	}
	return
}

// TestRollupAtomicAndConcurrent is T-948: a pass that fails between its statements leaves the
// rollup and the watermark as they were; workers on one tenant never count a record twice; a tenant
// with no record gets a state and no row.
func TestRollupAtomicAndConcurrent(t *testing.T) {
	d := dbtest.NewMigrated(t)
	super := d.SharedSuper(t)
	c := ctx(t)
	worker := d.OpenSized(t, store.RoleWorker, 8)
	roll := &reports.Rollup{DB: worker, Interval: time.Minute, Logger: quietLogger()}
	now := time.Now().UTC()

	t.Run("a tenant with no record gets a state and no row", func(t *testing.T) {
		empty := d.SeedTenant(t, "rollup-empty")
		if err := roll.Pass(c); err != nil {
			t.Fatal(err)
		}
		req, rows, wm := rollupTotals(t, d, empty)
		if wm == nil || req != 0 || rows != 0 {
			t.Errorf("the empty tenant: watermark %v, %d requests in %d rows, want a state and no row", wm, req, rows)
		}
	})

	t.Run("a pass that fails leaves everything as it was", func(t *testing.T) {
		a := d.SeedTenant(t, "rollup-atomic")
		insertRecords(t, d, a, aged(20, now.Add(-3*time.Hour), now.Add(-10*time.Minute))...)
		if err := roll.Pass(c); err != nil {
			t.Fatal(err)
		}
		req0, rows0, wm0 := rollupTotals(t, d, a)
		if wm0 == nil || req0 != 20 {
			t.Fatalf("after the first pass: %d requests, watermark %v, want 20", req0, wm0)
		}
		// Recorded at the watermark: a record is recorded at the database's clock, so none can be
		// recorded before the watermark of a pass that has run (section 5.1).
		insertRecords(t, d, a, aged(7, now.Add(-2*time.Hour), *wm0)...)
		// Whatever moves the watermark fails after the rollup rows were written in the same transaction.
		for _, q := range []string{
			`CREATE FUNCTION public.probe_boom() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'probe: the pass fails'; END $$`,
			`CREATE TRIGGER probe_boom BEFORE UPDATE ON pandokos.usage_rollup_state FOR EACH ROW WHEN (OLD.watermark IS DISTINCT FROM NEW.watermark) EXECUTE FUNCTION public.probe_boom()`,
		} {
			if _, err := super.Exec(c, q); err != nil {
				t.Fatal(err)
			}
		}
		before := roll.Stats().Failed
		_ = roll.Pass(c)
		req1, rows1, wm1 := rollupTotals(t, d, a)
		if req1 != req0 || rows1 != rows0 || wm1 == nil || !wm1.Equal(*wm0) {
			t.Errorf("a failed pass changed the rollup: %d requests in %d rows, watermark %v; before %d in %d, %v", req1, rows1, wm1, req0, rows0, wm0)
		}
		if roll.Stats().Failed <= before {
			t.Errorf("the failed pass was not counted: %+v", roll.Stats())
		}
		for _, q := range []string{`DROP TRIGGER probe_boom ON pandokos.usage_rollup_state`, `DROP FUNCTION public.probe_boom()`} {
			if _, err := super.Exec(c, q); err != nil {
				t.Fatal(err)
			}
		}
		if err := roll.Pass(c); err != nil {
			t.Fatal(err)
		}
		if req2, _, _ := rollupTotals(t, d, a); req2 != 27 {
			t.Errorf("after the pass that works: %d requests, want 27 (nothing lost, nothing twice)", req2)
		}
	})

	t.Run("workers on one tenant never count a record twice", func(t *testing.T) {
		a := d.SeedTenant(t, "rollup-concurrent")
		for i := 0; i < 5; i++ {
			insertRecords(t, d, a, aged(60, now.Add(-time.Duration(5-i)*time.Hour), now.Add(-time.Duration(20-i)*time.Minute))...)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 32)
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := &reports.Rollup{DB: worker, Interval: time.Minute, Logger: quietLogger()}
				for i := 0; i < 3; i++ {
					if err := r.Pass(c); err != nil {
						errs <- err
					}
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Errorf("a pass failed: %v", err)
		}
		if req, _, _ := rollupTotals(t, d, a); req != 300 {
			t.Errorf("the rollup counts %d requests, want 300", req)
		}
		_, _, wmNow := rollupTotals(t, d, a)
		insertRecords(t, d, a, aged(10, now.Add(-time.Hour), *wmNow)...) // recorded at the watermark, as above
		var wg2 sync.WaitGroup
		for w := 0; w < 4; w++ {
			wg2.Add(1)
			go func() {
				defer wg2.Done()
				_ = (&reports.Rollup{DB: worker, Interval: time.Minute, Logger: quietLogger()}).Pass(c)
			}()
		}
		wg2.Wait()
		if req, _, _ := rollupTotals(t, d, a); req != 310 {
			t.Errorf("after a second round the rollup counts %d requests, want 310", req)
		}
	})
}

// TestRollupLateRecords is T-949: a record that started hours ago and was recorded now is added to
// its old hour by the pass that follows its minute, and is in the report from the start; a record
// recorded less than a minute ago is not yet in the rollup but is in the report.
func TestRollupLateRecords(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a := d.SeedTenant(t, "late")
	c := ctx(t)
	worker, admin := d.Open(t, store.RoleWorker), d.Open(t, store.RoleAdmin)
	roll := &reports.Rollup{DB: worker, Interval: time.Minute, Logger: quietLogger()}
	now := time.Now().UTC()

	report := func(t *testing.T) (requests int64) {
		t.Helper()
		from, to := now.Add(-6*time.Hour).Truncate(time.Hour), now.Add(time.Hour)
		if err := admin.WithTenant(c, a, func(c context.Context, tx store.Tx) error {
			rows, err := queries.New(tx).UsageReport(c, queries.UsageReportParams{MaxRows: 1000, Bounds: []time.Time{from, to}})
			for _, r := range rows {
				requests += r.Requests
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	rolledAt := func(hour time.Time) (n int64) {
		if err := d.SharedSuper(t).QueryRow(c, `SELECT COALESCE(sum(requests), 0) FROM pandokos.usage_rollup WHERE tenant_id = $1::uuid AND hour = $2`, a.String(), hour).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return
	}
	one := func(started, recorded time.Time) rec {
		return rec{Started: started, Recorded: recorded, Principal: uuidOf(1), Provider: "openai", Credential: true, InSource: "provider", OutSource: "provider", In: 1, Out: 1}
	}
	late := now.Add(-4 * time.Hour)  // started four hours ago, recorded two minutes ago: a backlog after an outage
	fresh := now.Add(-3 * time.Hour) // started three hours ago, recorded just now
	insertRecords(t, d, a, one(late, now.Add(-2*time.Minute)), one(fresh, now))
	if err := roll.Pass(c); err != nil {
		t.Fatal(err)
	}
	if n := rolledAt(late.Truncate(time.Hour)); n != 1 {
		t.Errorf("the late record is not in its old hour after the pass: %d requests at %v, want 1", n, late.Truncate(time.Hour))
	}
	if n := rolledAt(fresh.Truncate(time.Hour)); n != 0 {
		t.Errorf("a record recorded less than a minute ago is in the rollup (%d requests)", n)
	}
	if n := report(t); n != 2 {
		t.Errorf("the report holds %d requests, want 2: the late one from the rollup and the fresh one from the ledger", n)
	}
	// A second pass changes nothing: the late record is not counted twice.
	if err := roll.Pass(c); err != nil {
		t.Fatal(err)
	}
	if n := rolledAt(late.Truncate(time.Hour)); n != 1 {
		t.Errorf("after a second pass the old hour holds %d requests, want 1", n)
	}
	if n := report(t); n != 2 {
		t.Errorf("after a second pass the report holds %d requests, want 2", n)
	}
}

// TestRollupDelayExceedsWriterTimeout is T-950: the delay of the rollup (the constant of
// reports.sql) is longer than what a ledger insert transaction can last: the writer's timeout plus
// the bound of a commit.
func TestRollupDelayExceedsWriterTimeout(t *testing.T) {
	read := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	sql := read("../store/queries/sql/reports.sql")
	m := regexp.MustCompile(`LEAST\(now\(\) - interval '(\d+) minute'`).FindStringSubmatch(sql)
	if m == nil {
		t.Fatal("RollUpUsage does not hold now() - interval 'N minute' in reports.sql")
	}
	minutes, _ := strconv.Atoi(m[1])
	delay := time.Duration(minutes) * time.Minute
	w := regexp.MustCompile(`writeTimeout\s*=\s*(\d+)\s*\*\s*time\.Second`).FindStringSubmatch(read("../ledger/writer.go"))
	if w == nil {
		t.Fatal("internal/ledger/writer.go has no writeTimeout in seconds")
	}
	seconds, _ := strconv.Atoi(w[1])
	// The commit of a transaction is bounded by the restore timeout of the store, 5 s.
	commit := regexp.MustCompile(`restoreTimeout\s*=\s*(\d+)\s*\*\s*time\.Second`).FindStringSubmatch(read("../store/tx.go"))
	if commit == nil {
		t.Fatal("internal/store/tx.go has no restoreTimeout in seconds")
	}
	cs, _ := strconv.Atoi(commit[1])
	bound := time.Duration(seconds+cs) * time.Second
	if delay <= bound {
		t.Errorf("the rollup waits %v, an insert transaction of the ledger can last %v (writeTimeout %ds + commit %ds)", delay, bound, seconds, cs)
	}
	if delay != time.Minute {
		t.Errorf("the delay is %v: ADR 0064 and the design say one minute", delay)
	}
	// The statement text that the generated code runs holds the same constant.
	gen := read("../store/queries/reports.sql.go")
	if !strings.Contains(gen, "now() - interval '1 minute'") {
		t.Error("the generated RollUpUsage does not hold now() - interval '1 minute'")
	}
}

// TestRollupTenantLoop is T-953: tenants are listed in platform scope by ID only; each tenant's
// statements run in the session of that tenant (a probe in the database records the scope and the
// tenant of every write); a tenant whose pass fails does not stop the others; the counters M42 and
// M43 and the two log lines.
func TestRollupTenantLoop(t *testing.T) {
	d := dbtest.NewMigrated(t)
	c := ctx(t)
	super := d.SharedSuper(t)
	worker := d.Open(t, store.RoleWorker)
	now := time.Now().UTC()
	tenants := []tenant.ID{d.SeedTenant(t, "loop-a"), d.SeedTenant(t, "loop-b"), d.SeedTenant(t, "loop-c")}
	for i, tid := range tenants {
		insertRecords(t, d, tid, aged(5+i, now.Add(-2*time.Hour), now.Add(-10*time.Minute))...)
	}
	bad := tenants[1]
	for _, q := range []string{
		`CREATE TABLE public.probe (tbl text, row_tenant text, scope text, session_tenant text)`,
		`CREATE FUNCTION public.probe_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			INSERT INTO public.probe VALUES (TG_TABLE_NAME, NEW.tenant_id::text, COALESCE(current_setting('app.scope', true), ''), COALESCE(current_setting('app.tenant_id', true), ''));
			RETURN NEW; END $$`,
		`CREATE TRIGGER probe_rollup AFTER INSERT OR UPDATE ON pandokos.usage_rollup FOR EACH ROW EXECUTE FUNCTION public.probe_write()`,
		`CREATE TRIGGER probe_state AFTER INSERT OR UPDATE ON pandokos.usage_rollup_state FOR EACH ROW EXECUTE FUNCTION public.probe_write()`,
		`GRANT INSERT ON public.probe TO PUBLIC`,
		`CREATE FUNCTION public.probe_boom() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'probe: this tenant fails'; END $$`,
		fmt.Sprintf(`CREATE TRIGGER probe_boom BEFORE UPDATE ON pandokos.usage_rollup_state FOR EACH ROW WHEN (NEW.tenant_id = '%s' AND OLD.watermark IS DISTINCT FROM NEW.watermark) EXECUTE FUNCTION public.probe_boom()`, bad),
	} {
		if _, err := super.Exec(c, q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	logs := &logBuffer{}
	roll := &reports.Rollup{DB: worker, Interval: time.Minute, Logger: jsonLogger(logs)}
	_ = roll.Pass(c)

	rows, err := super.Query(c, `SELECT tbl, row_tenant, scope, session_tenant FROM public.probe`)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		var tbl, rowTenant, scope, sessionTenant string
		if err := rows.Scan(&tbl, &rowTenant, &scope, &sessionTenant); err != nil {
			t.Fatal(err)
		}
		n++
		if sessionTenant != rowTenant || scope == "platform" {
			t.Errorf("a write of %s for tenant %s ran in scope %q with the session of tenant %q", tbl, rowTenant, scope, sessionTenant)
		}
	}
	rows.Close()
	if n == 0 {
		t.Error("the probe saw no write: no tenant was rolled up")
	}
	for _, tid := range tenants {
		req, _, _ := rollupTotals(t, d, tid)
		if tid == bad && req != 0 {
			t.Errorf("the failing tenant has %d requests rolled up", req)
		}
		if tid != bad && req == 0 {
			t.Errorf("tenant %s was not rolled up: a failing tenant stopped the others", tid)
		}
	}
	st := roll.Stats()
	if st.OK < 2 || st.Failed != 1 || st.Lag < 0 {
		t.Errorf("Stats %+v, want 2 or more passes ok, 1 failed", st)
	}
	failing := logs.lines(t, "usage rollup failing")
	if len(failing) != 1 || failing[0]["level"] != "WARN" || failing[0]["error_class"] == nil || failing[0]["sqlstate"] == nil || failing[0]["failures"] == nil {
		t.Fatalf("the line usage rollup failing: %v", failing)
	}
	_ = roll.Pass(c) // the same tenant fails again within the minute: no second line
	if got := logs.lines(t, "usage rollup failing"); len(got) != 1 {
		t.Errorf("%d lines usage rollup failing within a minute, want 1", len(got))
	}
	for _, tid := range tenants {
		if strings.Contains(logs.text(), tid.String()) {
			t.Errorf("a log line names tenant %s", tid)
		}
	}
	if _, err := super.Exec(c, `DROP TRIGGER probe_boom ON pandokos.usage_rollup_state`); err != nil {
		t.Fatal(err)
	}
	if err := roll.Pass(c); err != nil {
		t.Fatal(err)
	}
	rec := logs.lines(t, "usage rollup recovered")
	if len(rec) != 1 || rec[0]["level"] != "INFO" || rec[0]["failures"] == nil {
		t.Errorf("the line usage rollup recovered: %v", rec)
	}
	if req, _, _ := rollupTotals(t, d, bad); req != 6 {
		t.Errorf("after the recovery the failing tenant has %d requests rolled up, want 6", req)
	}
	_ = context.Background
}
