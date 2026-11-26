package vault

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
)

// countingProvider counts the unwraps that reach the key provider.
type countingProvider struct {
	KeyProvider
	unwraps atomic.Int32
}

func (c *countingProvider) Unwrap(ctx context.Context, version string, wrapped, aad []byte) ([]byte, error) {
	c.unwraps.Add(1)
	return c.KeyProvider.Unwrap(ctx, version, wrapped, aad)
}

// gatedSource holds the first read of a data key until release is closed; every other read goes
// straight through. It is the caller that stays long between its cache miss and its unwrap, for
// instance queued on the database pool.
type gatedSource struct {
	*fakeSource
	first   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (g *gatedSource) dataKey(ctx context.Context, id tenant.ID, version int32) (queries.GetDataKeyRow, error) {
	if g.first.CompareAndSwap(false, true) {
		close(g.entered)
		<-g.release
	}
	return g.fakeSource.dataKey(ctx, id, version)
}

type flightWorld struct {
	*uworld
	prov *countingProvider
	src  *gatedSource
	tid  tenant.ID
}

func newFlightWorld(t *testing.T) *flightWorld {
	t.Helper()
	w := newUWorld(t)
	tid := uid(1)
	w.addTenant(tid)
	w.addCredential(tid, cuid(1), "openai", "main", "active", "synthetic-secret", true)
	cp := &countingProvider{KeyProvider: w.prov}
	gs := &gatedSource{fakeSource: w.src, entered: make(chan struct{}), release: make(chan struct{})}
	o, err := newOpener(OpenerOptions{Provider: cp, Logger: telemetry.NewLogger(w.logs, "debug"), Now: w.clk.now,
		CredentialTTL: ttlCredential, DataKeyTTL: ttlDataKey}, gs)
	if err != nil {
		t.Fatal(err)
	}
	w.o = o
	return &flightWorld{uworld: w, prov: cp, src: gs, tid: tid}
}

// flightRefs returns the callers that wait for the load of the data key of the tenant.
func (w *flightWorld) flightRefs() int {
	w.o.fmu.Lock()
	defer w.o.fmu.Unlock()
	n := 0
	for k, f := range w.o.dekFlights {
		if k.tid == w.tid {
			n += f.refs
		}
	}
	return n
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

type openResult struct {
	got string
	err error
}

func (w *flightWorld) openAsync(ctx context.Context) chan openResult {
	ch := make(chan openResult, 1)
	go func() {
		got, err := w.o.Open(ctx, Use{Principal: principalOf(w.tid, ""), RequestID: "req-0001", Provider: "openai"})
		ch <- openResult{got.Secret.Reveal(), err}
	}()
	return ch
}

// TestOpenerDataKeyFlight: concurrent first calls of a tenant share one load of the data key,
// even when one of them reaches the unwrap after the load of another has ended.
func TestOpenerDataKeyFlight(t *testing.T) {
	t.Parallel()

	t.Run("a caller held between its cache miss and the unwrap makes no second unwrap", func(t *testing.T) {
		w := newFlightWorld(t)
		held := w.openAsync(context.Background()) // misses the cache, then waits at the database
		<-w.src.entered
		late := w.openAsync(context.Background())
		// Without the flight, the second call completes and caches the key while the first waits;
		// with it, the second call joins the load of the first.
		waitFor(t, "the second call to finish or to join the load", func() bool {
			select {
			case r := <-late:
				late <- r
				return true
			default:
				return w.flightRefs() == 2
			}
		})
		close(w.src.release)
		for name, ch := range map[string]chan openResult{"held": held, "late": late} {
			if r := <-ch; r.err != nil || r.got != "synthetic-secret" {
				t.Errorf("%s call: %q %v", name, r.got, r.err)
			}
		}
		if n := w.prov.unwraps.Load(); n != 1 {
			t.Errorf("%d unwraps for two concurrent first calls, want 1", n)
		}
	})

	t.Run("a waiter that leaves does not cancel the load for the others", func(t *testing.T) {
		w := newFlightWorld(t)
		ctx, cancel := context.WithCancel(context.Background())
		leaving := w.openAsync(ctx)
		<-w.src.entered
		staying := w.openAsync(context.Background())
		waitFor(t, "two waiters", func() bool { return w.flightRefs() == 2 })
		cancel()
		r := <-leaving
		if !errors.Is(r.err, ErrUnavailable) || !errors.Is(r.err, context.Canceled) {
			t.Errorf("the waiter that left: %v, want ErrUnavailable wrapping context.Canceled", r.err)
		}
		close(w.src.release)
		if r := <-staying; r.err != nil || r.got != "synthetic-secret" {
			t.Errorf("the waiter that stayed: %q %v", r.got, r.err)
		}
		if n := w.prov.unwraps.Load(); n != 1 {
			t.Errorf("%d unwraps, want 1", n)
		}
	})

	t.Run("a load nobody waits for is cancelled and leaves no flight", func(t *testing.T) {
		w := newFlightWorld(t)
		ctx, cancel := context.WithCancel(context.Background())
		only := w.openAsync(ctx)
		<-w.src.entered
		cancel()
		if r := <-only; !errors.Is(r.err, ErrUnavailable) {
			t.Errorf("Open: %v, want ErrUnavailable", r.err)
		}
		close(w.src.release)
		waitFor(t, "the flight to end", func() bool {
			w.o.fmu.Lock()
			defer w.o.fmu.Unlock()
			return len(w.o.dekFlights) == 0
		})
		if _, ok := w.o.cachedDataKey(w.tid, 1); ok {
			t.Error("the data key was cached by a load that was cancelled")
		}
		// The next call starts a fresh load and succeeds.
		if r := <-w.openAsync(context.Background()); r.err != nil || r.got != "synthetic-secret" {
			t.Errorf("the next call: %q %v", r.got, r.err)
		}
	})

	t.Run("a load that started before a flush is not joined by a call after it", func(t *testing.T) {
		w := newFlightWorld(t)
		before := w.openAsync(context.Background())
		<-w.src.entered
		w.o.Flush()
		after := w.openAsync(context.Background())
		if r := <-after; r.err != nil || r.got != "synthetic-secret" {
			t.Fatalf("the call after the flush: %q %v", r.got, r.err)
		}
		if n := w.prov.unwraps.Load(); n != 1 {
			t.Errorf("%d unwraps before the older load is released, want 1: the call after the flush loads on its own", n)
		}
		close(w.src.release)
		if r := <-before; r.err != nil || r.got != "synthetic-secret" {
			t.Errorf("the call before the flush: %q %v", r.got, r.err)
		}
	})

	t.Run("every waiter gets the same mapped error", func(t *testing.T) {
		w := newFlightWorld(t)
		w.src.mu.Lock()
		w.src.fail["dek"] = errors.New("database down")
		w.src.mu.Unlock()
		var wg sync.WaitGroup
		errs := make([]error, 5)
		first := w.openAsync(context.Background())
		<-w.src.entered
		for i := 1; i < len(errs); i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = w.o.Open(context.Background(), Use{Principal: principalOf(w.tid, ""), RequestID: "req-0001", Provider: "openai"})
			}()
		}
		waitFor(t, "all waiters", func() bool { return w.flightRefs() == len(errs) })
		close(w.src.release)
		wg.Wait()
		errs[0] = (<-first).err
		for i, err := range errs {
			if !errors.Is(err, ErrUnavailable) {
				t.Errorf("call %d: %v, want ErrUnavailable", i, err)
			}
		}
		if n := w.prov.unwraps.Load(); n != 0 {
			t.Errorf("%d unwraps with the database down", n)
		}
	})
}
