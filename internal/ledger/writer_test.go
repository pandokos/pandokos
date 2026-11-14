package ledger

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// The tests of this file run the writer over an in-package fake of the store seam (the unexported
// interface sink and the constructor newWriter, which New calls with the sink of the database).

// errGone is an availability error: store.IsUnavailable is true for it.
var errGone = fmt.Errorf("fake store: %w", io.ErrUnexpectedEOF)

// errRefused is an error that is not about availability: the database refused a statement.
var errRefused = errors.New("fake store: refused")

type fakeCall struct {
	at      time.Time
	tenant  tenant.ID
	records []usage.Record
	err     error
}

// fakeStore is a sink that keeps what it committed and can fail or block on demand.
type fakeStore struct {
	mu    sync.Mutex
	calls []fakeCall
	rows  []usage.Record
	gate  chan struct{}
	fail  func(t tenant.ID, recs []usage.Record) error
}

func (f *fakeStore) insert(ctx context.Context, t tenant.ID, records []usage.Record) error {
	f.mu.Lock()
	gate, fail := f.gate, f.fail
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	call := fakeCall{at: time.Now(), tenant: t, records: append([]usage.Record(nil), records...)}
	if fail != nil {
		call.err = fail(t, records)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	if call.err == nil {
		f.rows = append(f.rows, records...)
	}
	return call.err
}

func (f *fakeStore) setFail(fn func(t tenant.ID, recs []usage.Record) error) {
	f.mu.Lock()
	f.fail = fn
	f.mu.Unlock()
}

func (f *fakeStore) setGate(g chan struct{}) {
	f.mu.Lock()
	f.gate = g
	f.mu.Unlock()
}

func (f *fakeStore) committed() []usage.Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]usage.Record(nil), f.rows...)
}

func (f *fakeStore) allCalls() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall(nil), f.calls...)
}

// rig is a writer over a fake store, running.
type rig struct {
	w    *Writer
	fs   *fakeStore
	logs *testutil.LogBuffer
	done chan struct{}
}

func opts(buffer, batch int, flush time.Duration) (Options, *testutil.LogBuffer) {
	logs := &testutil.LogBuffer{}
	return Options{Logger: telemetry.NewLogger(logs, "debug"), BufferSize: buffer, BatchSize: batch, FlushInterval: flush}, logs
}

// start builds a writer over fs and runs it; the cleanup closes it.
func start(t *testing.T, buffer, batch int, flush time.Duration, fs *fakeStore) *rig {
	t.Helper()
	o, logs := opts(buffer, batch, flush)
	w, err := newWriter(o, fs)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{w: w, fs: fs, logs: logs, done: make(chan struct{})}
	go func() { w.Run(context.Background()); close(r.done) }()
	t.Cleanup(func() {
		fs.setGate(nil)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		w.Close(ctx)
		select {
		case <-r.done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return after Close")
		}
	})
	return r
}

// admitRecord takes a slot and hands the writer a valid record of tenant tid.
func (r *rig) admitRecord(t *testing.T, tid tenant.ID, mutate ...func(*usage.Record)) usage.Record {
	t.Helper()
	if !r.w.Admit() {
		t.Fatal("Admit refused")
	}
	rec := testutil.ValidRecord(tid)
	for _, m := range mutate {
		m(&rec)
	}
	r.w.Record(rec)
	return rec
}

func tenantID(t testing.TB) tenant.ID {
	t.Helper()
	id, err := tenant.ParseID(testutil.RandomUUID())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (r *rig) wait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	testutil.Eventually(t, 2*time.Second, what, cond)
}

// TestNew checks the options and the names of the hooks of the package.
func TestNew(t *testing.T) {
	good, _ := opts(10, 5, time.Millisecond)
	for name, mutate := range map[string]func(*Options){
		"no logger":         func(o *Options) { o.Logger = nil },
		"no buffer":         func(o *Options) { o.BufferSize = 0 },
		"no batch":          func(o *Options) { o.BatchSize = 0 },
		"no flush interval": func(o *Options) { o.FlushInterval = 0 },
		"negative buffer":   func(o *Options) { o.BufferSize = -1 },
		"no database (New)": func(*Options) {},
	} {
		o := good
		mutate(&o)
		if w, err := New(o); err == nil || w != nil {
			t.Errorf("%s: New accepted the options", name)
		}
	}
	if backoffInitial != 100*time.Millisecond || backoffMax != 5*time.Second {
		t.Errorf("back-off %s to %s, want 100ms to 5s", backoffInitial, backoffMax)
	}
}

// TestAdmission is T-470.
func TestAdmission(t *testing.T) {
	t.Run("BufferSize admissions, then false", func(t *testing.T) {
		r := start(t, 3, 10, 5*time.Millisecond, &fakeStore{})
		for i := 0; i < 3; i++ {
			if !r.w.Admit() {
				t.Fatalf("admission %d refused", i)
			}
		}
		for i := 0; i < 2; i++ {
			if r.w.Admit() {
				t.Fatal("a fourth admission was granted")
			}
		}
		if got := r.w.Stats().InUse; got != 3 {
			t.Errorf("InUse %d", got)
		}
	})

	t.Run("a slot is freed only after the write", func(t *testing.T) {
		fs := &fakeStore{gate: make(chan struct{})}
		r := start(t, 3, 10, 5*time.Millisecond, fs)
		tid := tenantID(t)
		for i := 0; i < 3; i++ {
			r.admitRecord(t, tid)
		}
		time.Sleep(100 * time.Millisecond)
		if s := r.w.Stats(); s.InUse != 3 || s.Written != 0 {
			t.Fatalf("while the store is blocked: %+v", s)
		}
		close(fs.gate)
		r.wait(t, "the slots are freed", func() bool { return r.w.Stats().InUse == 0 })
		if s := r.w.Stats(); s.Written != 3 || s.Queued != 0 || s.Rejected != 0 {
			t.Errorf("after the write: %+v", s)
		}
		if !r.w.Admit() {
			t.Error("a freed slot is not admitted again")
		}
	})

	t.Run("Ready fails from the first refusal until use falls to half", func(t *testing.T) {
		fs := &fakeStore{gate: make(chan struct{})}
		r := start(t, 4, 1, time.Millisecond, fs)
		tid := tenantID(t)
		for i := 0; i < 4; i++ {
			if !r.w.Admit() {
				t.Fatal("Admit refused")
			}
		}
		if err := r.w.Ready(context.Background()); err != nil {
			t.Fatalf("Ready fails with a full buffer and no refusal: %v", err)
		}
		if r.w.Admit() {
			t.Fatal("the fifth admission was granted")
		}
		err := r.w.Ready(context.Background())
		if err == nil || !strings.Contains(err.Error(), "ledger: buffer full") {
			t.Fatalf("Ready after a refusal: %v", err)
		}
		release := func(n int) {
			for i := 0; i < n; i++ {
				rec := testutil.ValidRecord(tid)
				r.w.Record(rec)
			}
		}
		release(2) // two records are queued; the store is blocked: nothing is freed
		time.Sleep(50 * time.Millisecond)
		if r.w.Ready(context.Background()) == nil {
			t.Fatal("Ready passes while nothing was written")
		}
		close(fs.gate)
		r.wait(t, "two slots freed", func() bool { return r.w.Stats().InUse == 2 })
		if err := r.w.Ready(context.Background()); err != nil {
			t.Errorf("Ready with two slots of four in use: %v", err)
		}
		if !r.w.Admit() {
			t.Error("Admit refused after readiness came back")
		}
	})

	t.Run("Ready stays failed above half", func(t *testing.T) {
		fs := &fakeStore{}
		r := start(t, 4, 1, time.Millisecond, fs)
		tid := tenantID(t)
		for i := 0; i < 4; i++ {
			r.w.Admit()
		}
		r.w.Admit() // refused
		r.w.Record(testutil.ValidRecord(tid))
		r.wait(t, "one slot freed", func() bool { return r.w.Stats().InUse == 3 })
		if r.w.Ready(context.Background()) == nil {
			t.Error("Ready passes with three slots of four in use")
		}
		r.w.Record(testutil.ValidRecord(tid))
		r.wait(t, "readiness", func() bool { return r.w.Ready(context.Background()) == nil })
		if got := r.w.Stats().InUse; got != 2 {
			t.Errorf("InUse %d", got)
		}
	})

	t.Run("Stats adds up", func(t *testing.T) {
		r := start(t, 10, 4, 5*time.Millisecond, &fakeStore{})
		tid := tenantID(t)
		for i := 0; i < 7; i++ {
			r.admitRecord(t, tid)
		}
		r.wait(t, "seven rows", func() bool { return r.w.Stats().Written == 7 })
		if s := r.w.Stats(); s.InUse != 0 || s.Queued != 0 || s.Rejected != 0 || s.Unwritten != 0 {
			t.Errorf("%+v", s)
		}
		if n := len(r.fs.committed()); n != 7 {
			t.Errorf("the store holds %d records", n)
		}
	})

	t.Run("a Record without Admit is logged once and still written", func(t *testing.T) {
		r := start(t, 5, 10, 5*time.Millisecond, &fakeStore{})
		tid := tenantID(t)
		r.w.Record(testutil.ValidRecord(tid))
		r.w.Record(testutil.ValidRecord(tid))
		r.wait(t, "both written", func() bool { return len(r.fs.committed()) == 2 })
		lines := r.logs.Find(t, "usage record without admission")
		if len(lines) != 1 || lines[0]["level"] != "ERROR" {
			t.Errorf("%d lines %v", len(lines), lines)
		}
	})

	t.Run("an invalid record is rejected and its slot freed", func(t *testing.T) {
		r := start(t, 5, 10, 5*time.Millisecond, &fakeStore{})
		tid := tenantID(t)
		r.admitRecord(t, tid, func(rec *usage.Record) { rec.RequestID = fixtures.CanaryPrompt })
		r.wait(t, "the slot is freed", func() bool { return r.w.Stats().InUse == 0 })
		if s := r.w.Stats(); s.Rejected != 1 || s.Written != 0 {
			t.Errorf("%+v", s)
		}
		if n := len(r.fs.allCalls()); n != 0 {
			t.Errorf("the store was called %d times for a record that fails Validate", n)
		}
		lines := r.logs.Find(t, "usage record rejected")
		if len(lines) != 1 {
			t.Fatalf("%d lines", len(lines))
		}
		testutil.AssertNoCanary(t, "the log", r.logs.String())
	})
}

// TestBatching is T-472.
func TestBatching(t *testing.T) {
	t.Run("a lone record is written after FlushInterval", func(t *testing.T) {
		r := start(t, 10, 100, 300*time.Millisecond, &fakeStore{})
		began := time.Now()
		r.admitRecord(t, tenantID(t))
		r.wait(t, "the record is written", func() bool { return len(r.fs.committed()) == 1 })
		if waited := r.fs.allCalls()[0].at.Sub(began); waited < 250*time.Millisecond {
			t.Errorf("written after %s, before the flush interval", waited)
		}
	})

	t.Run("BatchSize records are written without waiting", func(t *testing.T) {
		r := start(t, 10, 5, 1500*time.Millisecond, &fakeStore{})
		tid := tenantID(t)
		for i := 0; i < 5; i++ {
			r.admitRecord(t, tid)
		}
		testutil.Eventually(t, time.Second, "a full batch is written at once", func() bool { return len(r.fs.committed()) == 5 })
	})

	t.Run("arrival order is kept within a tenant, one call per tenant and flush", func(t *testing.T) {
		r := start(t, 100, 12, 1500*time.Millisecond, &fakeStore{})
		a, b := tenantID(t), tenantID(t)
		var wantA, wantB []string
		for i := 0; i < 12; i++ {
			tid := a
			if i%3 == 2 {
				tid = b
			}
			rec := r.admitRecord(t, tid)
			if tid == a {
				wantA = append(wantA, rec.ID)
			} else {
				wantB = append(wantB, rec.ID)
			}
		}
		testutil.Eventually(t, time.Second, "the batch is written", func() bool { return len(r.fs.committed()) == 12 })
		got := map[tenant.ID][]string{}
		calls := r.fs.allCalls()
		for _, c := range calls {
			for _, rec := range c.records {
				if rec.TenantID != c.tenant {
					t.Errorf("a record of %s was inserted for %s", rec.TenantID, c.tenant)
				}
				got[c.tenant] = append(got[c.tenant], rec.ID)
			}
		}
		if len(calls) != 2 {
			t.Errorf("%d store calls for one flush of two tenants", len(calls))
		}
		if strings.Join(got[a], ",") != strings.Join(wantA, ",") || strings.Join(got[b], ",") != strings.Join(wantB, ",") {
			t.Errorf("order not kept:\n%v\n%v", got, [][]string{wantA, wantB})
		}
	})
}

// TestBackoffAndRecovery covers the unavailable-database path with the fake: the batch stays
// queued in order, the wait doubles up to a cap, the failing line is logged once and a recovery
// line when it ends.
func TestBackoffAndRecovery(t *testing.T) {
	oldInitial, oldMax := backoffInitial, backoffMax
	backoffInitial, backoffMax = 10*time.Millisecond, 80*time.Millisecond
	t.Cleanup(func() { backoffInitial, backoffMax = oldInitial, oldMax })

	fs := &fakeStore{}
	var down atomic.Bool
	down.Store(true)
	fs.setFail(func(tenant.ID, []usage.Record) error {
		if down.Load() {
			return errGone
		}
		return nil
	})
	r := start(t, 50, 3, 5*time.Millisecond, fs)
	tid := tenantID(t)
	var want []string
	for i := 0; i < 5; i++ {
		want = append(want, r.admitRecord(t, tid).ID)
	}
	r.wait(t, "seven failed attempts", func() bool { return len(fs.allCalls()) >= 7 })

	calls := fs.allCalls()
	nominal := backoffInitial
	for i := 1; i < 7; i++ {
		if gap := calls[i].at.Sub(calls[i-1].at); gap < nominal-5*time.Millisecond {
			t.Errorf("retry %d came after %s, want at least %s", i, gap, nominal)
		}
		nominal = min(nominal*2, backoffMax)
	}
	if n := len(r.logs.Find(t, "ledger write failing")); n != 1 {
		t.Errorf("%d `ledger write failing` lines while the store stays away", n)
	} else if lvl := r.logs.Find(t, "ledger write failing")[0]["level"]; lvl != "WARN" {
		t.Errorf("level %v", lvl)
	}
	if got := r.w.Stats().InUse; got != 5 {
		t.Errorf("InUse %d while the store is away", got)
	}
	down.Store(false)
	r.wait(t, "every record written", func() bool { return len(fs.committed()) == 5 })
	var got []string
	for _, rec := range fs.committed() {
		got = append(got, rec.ID)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order after recovery:\n%v\n%v", got, want)
	}
	r.wait(t, "the recovery line", func() bool { return len(r.logs.Find(t, "ledger write recovered")) == 1 })
	if r.logs.Find(t, "ledger write recovered")[0]["level"] != "INFO" {
		t.Error("the recovery is not logged at info")
	}
}

// TestRejectionIsolation: a refusal that is not about availability inserts the records of the
// group one by one; the one that is still refused is rejected and the others are written.
func TestRejectionIsolation(t *testing.T) {
	fs := &fakeStore{}
	fs.setFail(func(_ tenant.ID, recs []usage.Record) error {
		for _, rec := range recs {
			if rec.Operation == "bad-op" {
				return errRefused
			}
		}
		return nil
	})
	r := start(t, 20, 6, 1500*time.Millisecond, fs)
	a, b := tenantID(t), tenantID(t)
	for i := 0; i < 2; i++ {
		r.admitRecord(t, a)
		r.admitRecord(t, b)
	}
	r.admitRecord(t, a, func(rec *usage.Record) { rec.Operation = "bad-op" })
	r.admitRecord(t, b)
	r.wait(t, "the six slots are settled", func() bool { return r.w.Stats().InUse == 0 })
	s := r.w.Stats()
	if s.Written != 5 || s.Rejected != 1 {
		t.Errorf("%+v", s)
	}
	if n := len(fs.committed()); n != 5 {
		t.Errorf("%d records committed", n)
	}
	if n := len(r.logs.Find(t, "usage record rejected")); n != 1 {
		t.Errorf("%d `usage record rejected` lines", n)
	}
	if n := len(r.logs.Find(t, "ledger write failing")); n != 0 {
		t.Errorf("a refusal was logged as an unavailable database (%d lines)", n)
	}
}

// TestCloseWithFake checks Close with the fake store: it writes what is queued, waits for the
// record of an admitted request, refuses further admissions and is idempotent.
func TestCloseWithFake(t *testing.T) {
	o, _ := opts(10, 10, 20*time.Millisecond)
	fs := &fakeStore{}
	w, err := newWriter(o, fs)
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan struct{})
	go func() { w.Run(context.Background()); close(runDone) }()
	tid := tenantID(t)
	for i := 0; i < 2; i++ {
		if !w.Admit() {
			t.Fatal("refused")
		}
	}
	w.Record(testutil.ValidRecord(tid))

	closed := make(chan int, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		closed <- w.Close(ctx)
	}()
	testutil.Eventually(t, 2*time.Second, "Admit refused after Close started", func() bool { return !w.Admit() })
	select {
	case n := <-closed:
		t.Fatalf("Close returned (%d) while an admitted request had not recorded", n)
	case <-time.After(200 * time.Millisecond):
	}
	w.Record(testutil.ValidRecord(tid)) // the late handler
	select {
	case n := <-closed:
		if n != 0 {
			t.Errorf("Close returned %d unwritten", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not return")
	}
	if n := len(fs.committed()); n != 2 {
		t.Errorf("%d records written", n)
	}
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Error("Run did not return")
	}
	if w.Admit() {
		t.Error("Admit after Close")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if n := w.Close(ctx); n != 0 {
		t.Errorf("a second Close returned %d", n)
	}
}

// TestCloseAtDeadline: with the store away Close returns at the deadline with the count, and logs
// each record in full.
func TestCloseAtDeadline(t *testing.T) {
	o, logs := opts(10, 10, 5*time.Millisecond)
	fs := &fakeStore{}
	fs.setFail(func(tenant.ID, []usage.Record) error { return errGone })
	w, err := newWriter(o, fs)
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan struct{})
	go func() { w.Run(context.Background()); close(runDone) }()
	tid := tenantID(t)
	var ids []string
	for i := 0; i < 3; i++ {
		w.Admit()
		rec := testutil.ValidRecord(tid)
		rec.Metadata = map[string]string{"note": fixtures.CanaryCompletion}
		ids = append(ids, rec.ID)
		w.Record(rec)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	began := time.Now()
	if n := w.Close(ctx); n != 3 {
		t.Errorf("Close returned %d, want 3", n)
	}
	if took := time.Since(began); took > 2*time.Second {
		t.Errorf("Close took %s", took)
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
	seen := map[string]bool{}
	for _, l := range lines {
		if l["level"] != "ERROR" {
			t.Errorf("level %v", l["level"])
		}
		seen[fmt.Sprint(l["record_id"])] = true
		for _, k := range []string{"record_id", "request_id", "tenant_id", "outcome"} {
			if _, ok := l[k]; !ok {
				t.Errorf("the line has no %s: %v", k, l)
			}
		}
	}
	for _, id := range ids {
		if !seen[id] {
			t.Errorf("record %s is not in the log", id)
		}
	}
	testutil.AssertNoCanary(t, "the log", logs.String())
	if got := w.Stats().Unwritten; got != 3 {
		t.Errorf("Stats.Unwritten %d", got)
	}
}

// TestConcurrency is T-478: 64 goroutines admit and record while the store fails and recovers;
// written + rejected + in use equals admitted, and no goroutine is left after Close.
func TestConcurrency(t *testing.T) {
	baseline := runtime.NumGoroutine()
	fs := &fakeStore{}
	var down atomic.Bool
	fs.setFail(func(_ tenant.ID, recs []usage.Record) error {
		if down.Load() {
			return errGone
		}
		for _, rec := range recs {
			if rec.Operation == "bad-op" {
				return errRefused
			}
		}
		return nil
	})
	oldInitial, oldMax := backoffInitial, backoffMax
	backoffInitial, backoffMax = time.Millisecond, 4*time.Millisecond
	t.Cleanup(func() { backoffInitial, backoffMax = oldInitial, oldMax })

	o, _ := opts(200, 16, 2*time.Millisecond)
	w, err := newWriter(o, fs)
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan struct{})
	go func() { w.Run(context.Background()); close(runDone) }()

	tenants := []tenant.ID{tenantID(t), tenantID(t), tenantID(t)}
	var admitted, refused atomic.Int64
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				if !w.Admit() {
					refused.Add(1)
					runtime.Gosched()
					continue
				}
				admitted.Add(1)
				rec := testutil.ValidRecord(tenants[(g+i)%len(tenants)])
				if i%17 == 0 {
					rec.Operation = "bad-op"
				}
				w.Record(rec)
			}
		}()
	}
	for i := 0; i < 6; i++ {
		down.Store(i%2 == 0)
		time.Sleep(25 * time.Millisecond)
	}
	down.Store(false)
	close(stop)
	wg.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if n := w.Close(ctx); n != 0 {
		t.Errorf("Close left %d unwritten", n)
	}
	<-runDone
	s := w.Stats()
	if s.InUse != 0 || s.Queued != 0 {
		t.Errorf("slots left: %+v", s)
	}
	if got, want := s.Written+s.Rejected+int64(s.InUse), admitted.Load(); got != want {
		t.Errorf("written %d + rejected %d + in use %d = %d, admitted %d", s.Written, s.Rejected, s.InUse, got, want)
	}
	if int64(len(fs.committed())) != s.Written {
		t.Errorf("the store holds %d, the writer counts %d written", len(fs.committed()), s.Written)
	}
	testutil.Eventually(t, 2*time.Second, "no goroutine is left", func() bool { return runtime.NumGoroutine() <= baseline+2 })
}
