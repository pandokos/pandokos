package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

func ctx30(t testing.TB) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

func nilIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func i64OrNil(p *int64) any {
	if p == nil {
		return nil
	}
	return float64(*p)
}

func msOrNil(d *time.Duration) any {
	if d == nil {
		return nil
	}
	return float64(d.Milliseconds())
}

func stringMap(m map[string]string) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func intMap(m map[string]int64) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = float64(v)
	}
	return out
}

// expectedRow is what the row of a record must hold, as to_jsonb shows it, without the columns
// compared apart: started_at, recorded_at and cost.
func expectedRow(r usage.Record) map[string]any {
	provIn, provOut := r.InputSource == usage.SourceProvider, r.OutputSource == usage.SourceProvider
	estIn, estOut := r.InputSource == usage.SourceEstimated, r.OutputSource == usage.SourceEstimated
	num := func(ok bool, n int64) any {
		if !ok {
			return nil
		}
		return float64(n)
	}
	var stream any
	if r.Stream != nil {
		stream = *r.Stream
	}
	var status any
	if r.UpstreamStatus != 0 {
		status = float64(r.UpstreamStatus)
	}
	fallbacks := []any{}
	for _, c := range r.Cost.Fallbacks {
		fallbacks = append(fallbacks, string(c))
	}
	reason := ""
	if r.CostStatus == "unknown" {
		reason = r.Cost.Reason
	}
	other, reported := map[string]any{}, map[string]any{}
	if provIn || provOut {
		other, reported = intMap(r.Usage.Extra), intMap(r.Usage.Reported)
	}
	return map[string]any{
		"tenant_id": r.TenantID.String(), "id": r.ID, "request_id": r.RequestID, "trace_id": r.TraceID,
		"principal_kind": r.PrincipalKind, "principal_id": r.PrincipalID, "key_id": r.KeyID, "credential_id": nilIf(r.CredentialID),
		"funding": r.Funding, "provider": r.Provider, "api_family": r.APIFamily, "operation": r.Operation,
		"endpoint": r.Endpoint, "model": r.Model, "model_id": nilIf(r.ModelID), "upstream_model": r.UpstreamModel,
		"stream": stream, "outcome": string(r.Outcome), "error_code": r.ErrorCode, "upstream_status": status,
		"ttfb_ms": msOrNil(r.TTFB), "duration_ms": float64(r.Duration.Milliseconds()),
		"request_bytes": i64OrNil(r.RequestBytes), "response_bytes": i64OrNil(r.ResponseBytes),
		"input_source": string(r.InputSource), "output_source": string(r.OutputSource),
		"input_tokens": num(provIn, r.Usage.InputTokens), "output_tokens": num(provOut, r.Usage.OutputTokens),
		"cache_read_tokens": num(provIn, r.Usage.CacheReadTokens), "cache_write_tokens": num(provIn, r.Usage.CacheWriteTokens),
		"reasoning_tokens": num(provOut, r.Usage.ReasoningTokens),
		"other_units":      other, "provider_usage": reported,
		"estimated_input_tokens": num(estIn, r.Estimated.InputTokens), "estimated_output_tokens": num(estOut, r.Estimated.OutputTokens),
		"cost_status": r.CostStatus, "cost_reason": reason, "currency": nilIf(r.Cost.Currency), "price_version_id": nilIf(r.Cost.VersionID),
		"cost_fallbacks": fallbacks, "metadata": stringMap(r.Metadata), "metadata_dropped": float64(r.MetadataDropped),
	}
}

type storedRow struct {
	fields     map[string]any
	cost       *string
	started    time.Time
	recordedAt time.Time
	xmin       string
}

// readRows reads the rows of a tenant as the superuser of the test database.
func readRows(t testing.TB, d *dbtest.Database, tid tenant.ID) []storedRow {
	t.Helper()
	rows, err := d.Super(t).Query(ctx30(t), `SELECT to_jsonb(u) - 'cost' - 'recorded_at' - 'started_at', cost::text, started_at, recorded_at, u.xmin::text
		FROM pandokos.usage_record u WHERE tenant_id = $1::uuid ORDER BY started_at, id`, tid.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []storedRow
	for rows.Next() {
		var raw []byte
		var r storedRow
		if err := rows.Scan(&raw, &r.cost, &r.started, &r.recordedAt, &r.xmin); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &r.fields); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func hex32(c byte) string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = "0123456789abcdef"[int(c+byte(i))%16]
	}
	return string(b)
}

func dec(t testing.TB, s string) pricing.Decimal {
	t.Helper()
	d, err := pricing.ParseDecimal(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func ptr[T any](v T) *T { return &v }

// roundTripRecords returns records of one tenant that cover every column: nulls, 18 fraction
// digits, metadata, each source combination.
func roundTripRecords(t testing.TB, tid tenant.ID, base time.Time) []usage.Record {
	mk := func(at time.Time, mutate func(*usage.Record)) usage.Record {
		r := testutil.ValidRecord(tid)
		r.StartedAt = at.UTC().Truncate(time.Microsecond)
		r.ID = usage.NewID(r.StartedAt)
		mutate(&r)
		return r
	}
	return []usage.Record{
		mk(base, func(r *usage.Record) { // every provider column, every optional value
			r.TraceID = hex32(3)
			r.Stream, r.TTFB, r.Duration = ptr(true), ptr(1500*time.Millisecond), 2750*time.Millisecond
			r.Usage = family.Usage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 20, CacheWriteTokens: 5, ReasoningTokens: 7,
				Extra:    map[string]int64{"input_audio_tokens": 3},
				Reported: map[string]int64{"prompt_tokens": 100, "completion_tokens": 50, "prompt_tokens_details.cached_tokens": 20}}
			r.Cost = pricing.Cost{Known: true, Amount: dec(t, "0.123456789012345678"), Currency: "USD", VersionID: testutil.RandomUUID(),
				Fallbacks: []pricing.Category{pricing.CategoryCacheRead, pricing.CategoryCacheWrite}}
			r.Metadata, r.MetadataDropped = map[string]string{"team": "alpha", "cost-center": "x1"}, 2
		}),
		mk(base.Add(time.Second), func(r *usage.Record) { // estimated
			r.InputSource, r.OutputSource = usage.SourceEstimated, usage.SourceEstimated
			r.Usage = family.Usage{}
			r.Estimated = family.Usage{InputTokens: 11, OutputTokens: 22}
			r.Cost = pricing.Cost{Known: true, Amount: dec(t, "0.000001"), Currency: "EUR", VersionID: testutil.RandomUUID()}
		}),
		mk(base.Add(2*time.Second), func(r *usage.Record) { // a refusal: attribution only
			r.CredentialID, r.ModelID, r.Endpoint, r.Model, r.UpstreamModel = "", "", "", "", ""
			r.Stream, r.TTFB, r.RequestBytes, r.ResponseBytes = nil, nil, nil, nil
			r.Outcome, r.ErrorCode, r.UpstreamStatus, r.Duration = usage.OutcomeRejectedPolicy, "model_not_allowed", 0, 3*time.Millisecond
			r.InputSource, r.OutputSource, r.Usage = usage.SourceNone, usage.SourceNone, family.Usage{}
			r.Cost, r.CostStatus = pricing.Cost{}, "none"
		}),
		mk(base.Add(3*time.Second), func(r *usage.Record) { // provider input, estimated output, unknown cost
			r.InputSource, r.OutputSource = usage.SourceProvider, usage.SourceEstimated
			r.Usage = family.Usage{InputTokens: 40, OutputTokens: 99, CacheReadTokens: 4, ReasoningTokens: 9}
			r.Estimated = family.Usage{InputTokens: 77, OutputTokens: 13}
			r.Cost, r.CostStatus = pricing.Cost{Reason: pricing.ReasonNoPrice}, "unknown"
		}),
		mk(base.Add(4*time.Second), func(r *usage.Record) { // cancelled: no status, no first byte
			r.Outcome, r.UpstreamStatus, r.TTFB = usage.OutcomeCancelled, 0, nil
			r.InputSource, r.OutputSource = usage.SourceEstimated, usage.SourceEstimated
			r.Usage, r.Estimated = family.Usage{}, family.Usage{InputTokens: 3, OutputTokens: 0}
			r.Cost, r.CostStatus = pricing.Cost{}, "none"
		}),
	}
}

func lastMonthEnd() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Add(-time.Microsecond)
}

// TestWriterRoundTrip is T-471: records of three tenants covering every column; one transaction
// per tenant and flush; each row read back equals its record; each row is its tenant's.
func TestWriterRoundTrip(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		gateway := d.Open(t, store.RoleGateway)
		tenants := []tenant.ID{d.SeedTenant(t, "ledger-a"), d.SeedTenant(t, "ledger-b"), d.SeedTenant(t, "ledger-c")}
		var all [][]usage.Record
		total := 0
		for i, tid := range tenants {
			base := time.Now().UTC().Add(-time.Minute)
			if i == 2 {
				base = lastMonthEnd().Add(-4 * time.Second) // the previous month's partition
			}
			recs := roundTripRecords(t, tid, base)
			all = append(all, recs)
			total += len(recs)
		}

		logs := &testutil.LogBuffer{}
		w, err := New(Options{DB: gateway, Logger: telemetry.NewLogger(logs, "debug"), BufferSize: total, BatchSize: total, FlushInterval: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		runDone := make(chan struct{})
		go func() { w.Run(context.Background()); close(runDone) }()
		for i := 0; i < len(all[0]); i++ { // interleave the tenants
			for _, recs := range all {
				if !w.Admit() {
					t.Fatal("Admit refused")
				}
				w.Record(recs[i])
			}
		}
		testutil.Eventually(t, 10*time.Second, "every record is written", func() bool { return w.Stats().Written == int64(total) })
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if n := w.Close(cctx); n != 0 {
			t.Errorf("Close: %d unwritten", n)
		}
		<-runDone

		xmins := map[string]tenant.ID{}
		for i, tid := range tenants {
			rows := readRows(t, d, tid)
			if len(rows) != len(all[i]) {
				t.Fatalf("tenant %d: %d rows, want %d", i, len(rows), len(all[i]))
			}
			var tenantXmin string
			for j, want := range all[i] {
				got := rows[j]
				if got.fields["tenant_id"] != tid.String() {
					t.Errorf("a row of another tenant: %v", got.fields["tenant_id"])
				}
				if !reflect.DeepEqual(got.fields, expectedRow(want)) {
					t.Errorf("tenant %d record %d:\n got %v\nwant %v", i, j, got.fields, expectedRow(want))
				}
				if !got.started.Equal(want.StartedAt) {
					t.Errorf("started_at %v, want %v", got.started, want.StartedAt)
				}
				if time.Since(got.recordedAt) > time.Minute || got.recordedAt.Before(time.Now().Add(-time.Minute)) {
					t.Errorf("recorded_at %v", got.recordedAt)
				}
				switch {
				case want.Cost.Known && got.cost == nil:
					t.Errorf("the cost is NULL")
				case want.Cost.Known:
					if back, err := pricing.ParseDecimal(*got.cost); err != nil || back.Cmp(want.Cost.Amount) != 0 {
						t.Errorf("cost %s, want %s", *got.cost, want.Cost.Amount)
					}
				case got.cost != nil:
					t.Errorf("cost %s for a record without a known cost", *got.cost)
				}
				if tenantXmin == "" {
					tenantXmin = got.xmin
				} else if got.xmin != tenantXmin {
					t.Errorf("tenant %d: the rows of one flush were not written in one transaction", i)
				}
			}
			if other, dup := xmins[tenantXmin]; dup {
				t.Errorf("tenants %s and %s share a transaction", other, tid)
			}
			xmins[tenantXmin] = tid
		}
		if lines := logs.Find(t, "usage record rejected"); len(lines) != 0 {
			t.Errorf("%d records were rejected", len(lines))
		}
	})
}

// useFastBackoff shortens the retry waits of the writer for a test.
func useFastBackoff(t *testing.T) {
	t.Helper()
	oldInitial, oldMax := backoffInitial, backoffMax
	backoffInitial, backoffMax = 20*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { backoffInitial, backoffMax = oldInitial, oldMax })
}

func idsOf(t testing.TB, d *dbtest.Database, tid tenant.ID) map[string]int {
	t.Helper()
	rows, err := d.Super(t).Query(ctx30(t), `SELECT id::text FROM pandokos.usage_record WHERE tenant_id = $1::uuid`, tid.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out[id]++
	}
	return out
}

// TestDatabaseDown is T-473.
func TestDatabaseDown(t *testing.T) {
	useFastBackoff(t)
	d := dbtest.NewMigrated(t)
	gateway := d.Open(t, store.RoleGateway)
	tid := d.SeedTenant(t, "ledger-down")
	logs := &testutil.LogBuffer{}
	w, err := New(Options{DB: gateway, Logger: telemetry.NewLogger(logs, "debug"), BufferSize: 6, BatchSize: 3, FlushInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	go w.Run(context.Background())
	t.Cleanup(func() {
		d.SetAcceptingConnections(t, true)
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		w.Close(cctx)
	})

	d.SetAcceptingConnections(t, false)
	ids := map[string]bool{}
	for i := 0; i < 6; i++ {
		if !w.Admit() {
			t.Fatalf("admission %d refused before the buffer is full", i)
		}
		rec := testutil.ValidRecord(tid)
		ids[rec.ID] = true
		w.Record(rec)
	}
	if w.Admit() {
		t.Fatal("an admission was granted with the buffer full")
	}
	if err := w.Ready(context.Background()); err == nil {
		t.Error("Ready passes with the buffer full")
	}
	testutil.Eventually(t, 5*time.Second, "`ledger write failing` is logged", func() bool { return len(logs.Find(t, "ledger write failing")) >= 1 })
	time.Sleep(400 * time.Millisecond) // several retries
	if n := len(logs.Find(t, "ledger write failing")); n != 1 {
		t.Errorf("`ledger write failing` logged %d times while the state lasts", n)
	}
	if got := w.Stats(); got.InUse != 6 || got.Written != 0 {
		t.Errorf("stats while the database is away: %+v", got)
	}

	d.SetAcceptingConnections(t, true)
	testutil.Eventually(t, 10*time.Second, "every record is written", func() bool { return w.Stats().Written == 6 })
	got := idsOf(t, d, tid)
	if len(got) != 6 {
		t.Errorf("%d distinct rows, want 6", len(got))
	}
	for id, n := range got {
		if n != 1 || !ids[id] {
			t.Errorf("row %s appears %d times (known: %v)", id, n, ids[id])
		}
	}
	testutil.Eventually(t, 5*time.Second, "readiness is back", func() bool { return w.Ready(context.Background()) == nil })
	if n := len(logs.Find(t, "ledger write recovered")); n != 1 {
		t.Errorf("`ledger write recovered` logged %d times", n)
	}
	if !w.Admit() {
		t.Error("Admit refused after the database came back")
	}
}

// commitThenFail commits the first call and then reports a connection error, as a connection
// that drops before the answer does.
type commitThenFail struct {
	inner sink
	calls atomic.Int32
}

func (c *commitThenFail) insert(ctx context.Context, t tenant.ID, recs []usage.Record) error {
	err := c.inner.insert(ctx, t, recs)
	if c.calls.Add(1) == 1 && err == nil {
		return fmt.Errorf("fake: the answer was lost: %w", io.ErrUnexpectedEOF)
	}
	return err
}

// TestIdempotentRetry is T-474.
func TestIdempotentRetry(t *testing.T) {
	useFastBackoff(t)
	d := dbtest.NewMigrated(t)
	tid := d.SeedTenant(t, "ledger-retry")
	o := Options{Logger: telemetry.NewLogger(&testutil.LogBuffer{}, "debug"), BufferSize: 10, BatchSize: 4, FlushInterval: 10 * time.Millisecond}
	wrap := &commitThenFail{inner: newDBSink(d.Open(t, store.RoleGateway))}
	w, err := newWriter(o, wrap)
	if err != nil {
		t.Fatal(err)
	}
	go w.Run(context.Background())
	ids := map[string]bool{}
	for i := 0; i < 4; i++ {
		w.Admit()
		rec := testutil.ValidRecord(tid)
		ids[rec.ID] = true
		w.Record(rec)
	}
	testutil.Eventually(t, 10*time.Second, "the slots are freed", func() bool { return w.Stats().InUse == 0 })
	if n := wrap.calls.Load(); n < 2 {
		t.Errorf("the batch was sent %d times, want a retry", n)
	}
	got := idsOf(t, d, tid)
	if len(got) != 4 {
		t.Errorf("%d rows, want 4", len(got))
	}
	for id, n := range got {
		if n != 1 || !ids[id] {
			t.Errorf("row %s appears %d times", id, n)
		}
	}
	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w.Close(cctx)
}

// corrupting hands the database a record that passed Validate in a state the table refuses.
type corrupting struct {
	inner sink
	badID string
}

func (c *corrupting) insert(ctx context.Context, t tenant.ID, recs []usage.Record) error {
	cp := append([]usage.Record(nil), recs...)
	for i := range cp {
		if cp[i].ID == c.badID {
			cp[i].RequestID = "NOT-A-REQUEST-ID"
		}
	}
	return c.inner.insert(ctx, t, cp)
}

// TestRejectedRecord is T-475.
func TestRejectedRecord(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a, b := d.SeedTenant(t, "ledger-rej-a"), d.SeedTenant(t, "ledger-rej-b")
	logs := &testutil.LogBuffer{}
	o := Options{Logger: telemetry.NewLogger(logs, "debug"), BufferSize: 10, BatchSize: 5, FlushInterval: time.Second}
	bad := testutil.ValidRecord(a)
	bad.Metadata = map[string]string{"note": fixtures.CanaryCompletion}
	w, err := newWriter(o, &corrupting{inner: newDBSink(d.Open(t, store.RoleGateway)), badID: bad.ID})
	if err != nil {
		t.Fatal(err)
	}
	go w.Run(context.Background())
	good := map[tenant.ID][]string{}
	send := func(r usage.Record) {
		if !w.Admit() {
			t.Fatal("Admit refused")
		}
		w.Record(r)
	}
	for _, tid := range []tenant.ID{a, b, a} {
		r := testutil.ValidRecord(tid)
		good[tid] = append(good[tid], r.ID)
		send(r)
	}
	send(bad)
	r := testutil.ValidRecord(b)
	good[b] = append(good[b], r.ID)
	send(r)

	testutil.Eventually(t, 10*time.Second, "every slot is settled", func() bool { return w.Stats().InUse == 0 })
	if s := w.Stats(); s.Rejected != 1 || s.Written != 4 {
		t.Errorf("stats %+v", s)
	}
	for tid, want := range good {
		got := idsOf(t, d, tid)
		if len(got) != len(want) {
			t.Errorf("tenant %s: %d rows, want %d", tid, len(got), len(want))
		}
		for _, id := range want {
			if got[id] != 1 {
				t.Errorf("tenant %s: row %s appears %d times", tid, id, got[id])
			}
		}
	}
	if idsOf(t, d, a)[bad.ID] != 0 {
		t.Error("the bad record was written")
	}
	lines := logs.Find(t, "usage record rejected")
	if len(lines) != 1 {
		t.Fatalf("%d `usage record rejected` lines", len(lines))
	}
	l := lines[0]
	if l["level"] != "ERROR" || l["sqlstate"] != "23514" || l["constraint"] != "usage_record_request_id_format" || l["record_id"] != bad.ID {
		t.Errorf("the line: %v", l)
	}
	testutil.AssertNoCanary(t, "the log", logs.String())
	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w.Close(cctx)
}

// TestClose is T-476.
func TestClose(t *testing.T) {
	t.Run("Close writes the queue and waits for a late record", func(t *testing.T) {
		d := dbtest.NewMigrated(t)
		tid := d.SeedTenant(t, "ledger-close")
		w, err := New(Options{DB: d.Open(t, store.RoleGateway), Logger: telemetry.NewLogger(&testutil.LogBuffer{}, "debug"),
			BufferSize: 10, BatchSize: 10, FlushInterval: 20 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		runDone := make(chan struct{})
		go func() { w.Run(context.Background()); close(runDone) }()
		w.Admit()
		w.Admit()
		first, late := testutil.ValidRecord(tid), testutil.ValidRecord(tid)
		w.Record(first)
		closed := make(chan int, 1)
		go func() {
			cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			closed <- w.Close(cctx)
		}()
		time.Sleep(50 * time.Millisecond) // Admit in the poll below must not win the race with the goroutine and take a slot
		testutil.Eventually(t, 2*time.Second, "Admit refused after Close started", func() bool { return !w.Admit() })
		select {
		case n := <-closed:
			t.Fatalf("Close returned %d before the admitted request recorded", n)
		case <-time.After(300 * time.Millisecond):
		}
		w.Record(late)
		select {
		case n := <-closed:
			if n != 0 {
				t.Errorf("%d unwritten", n)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Close did not return")
		}
		select {
		case <-runDone:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return")
		}
		got := idsOf(t, d, tid)
		if len(got) != 2 || got[first.ID] != 1 || got[late.ID] != 1 {
			t.Errorf("rows %v", got)
		}
		if w.Admit() {
			t.Error("Admit after Close")
		}
	})

	t.Run("with the database away it returns at the deadline and logs every record", func(t *testing.T) {
		useFastBackoff(t)
		d := dbtest.NewMigrated(t)
		tid := d.SeedTenant(t, "ledger-close-away")
		logs := &testutil.LogBuffer{}
		w, err := New(Options{DB: d.Open(t, store.RoleGateway), Logger: telemetry.NewLogger(logs, "debug"),
			BufferSize: 10, BatchSize: 10, FlushInterval: 10 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		runDone := make(chan struct{})
		go func() { w.Run(context.Background()); close(runDone) }()
		d.SetAcceptingConnections(t, false)
		t.Cleanup(func() { d.SetAcceptingConnections(t, true) })
		ids := map[string]bool{}
		for i := 0; i < 3; i++ {
			w.Admit()
			rec := testutil.ValidRecord(tid)
			rec.Metadata = map[string]string{"note": fixtures.CanaryPrompt}
			ids[rec.ID] = true
			w.Record(rec)
		}
		cctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		began := time.Now()
		if n := w.Close(cctx); n != 3 {
			t.Errorf("Close returned %d, want 3", n)
		}
		if took := time.Since(began); took > 3*time.Second {
			t.Errorf("Close took %s with a deadline of 1 s", took)
		}
		select {
		case <-runDone:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return")
		}
		lines := logs.Find(t, "usage record not written")
		if len(lines) != 3 {
			t.Fatalf("%d `usage record not written` lines", len(lines))
		}
		for _, l := range lines {
			id, _ := l["record_id"].(string)
			if !ids[id] || l["tenant_id"] != tid.String() || l["request_id"] == nil || l["outcome"] == nil {
				t.Errorf("the line: %v", l)
			}
		}
		testutil.AssertNoCanary(t, "the log", logs.String())
	})
}
