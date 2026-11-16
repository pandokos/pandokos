package jwks_test

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/jwks"
	"github.com/pandokos/pandokos/test/fakeidp"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestRefreshAndRefetch is T-577: how often the issuer is asked, with a clock the test moves.
func TestRefreshAndRefetch(t *testing.T) {
	t.Run("fifty concurrent first callers cause one fetch", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		idp.SetFault(fakeidp.Delay(150 * time.Millisecond)) // the callers pile up behind the one fetch
		r := overFake(t, idp, func(o *jwks.Options) { o.FetchTimeout = 2 * time.Second })
		var wg sync.WaitGroup
		var failed atomic.Int32
		start := make(chan struct{})
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if _, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256"); err != nil {
					failed.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		if n := failed.Load(); n != 0 {
			t.Errorf("%d of 50 callers failed", n)
		}
		if d, j := idp.Requests(); d != 1 || j != 1 {
			t.Errorf("%d discovery and %d key set requests for 50 callers, want 1 and 1", d, j)
		}
	})

	t.Run("a caller that gives up does not cancel the fetch the others wait for", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		idp.SetFault(fakeidp.Delay(300 * time.Millisecond))
		r := overFake(t, idp, func(o *jwks.Options) { o.FetchTimeout = 2 * time.Second })
		first := make(chan error, 1)
		go func() {
			ctx, cancel := cancelAfter(50 * time.Millisecond)
			defer cancel()
			_, err := r.c.Key(ctx, discover(idp), idp.KeyID("ES256"), "ES256")
			first <- err
		}()
		time.Sleep(20 * time.Millisecond)
		if _, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256"); err != nil {
			t.Errorf("the second caller: %v", err)
		}
		if err := <-first; !isUnavailable(err) {
			t.Errorf("the caller that gave up got %v, want ErrUnavailable", err)
		}
		if d, j := idp.Requests(); d != 1 || j != 1 {
			t.Errorf("%d discovery and %d key set requests, want 1 and 1", d, j)
		}
	})

	t.Run("after the refresh interval the next use refreshes in the background and is served from memory", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		r := overFake(t, idp, func(o *jwks.Options) { o.FetchTimeout = 2 * time.Second })
		kid := idp.KeyID("ES256")
		if _, err := r.key(idp, discover(idp), kid, "ES256"); err != nil {
			t.Fatal(err)
		}
		r.clk.advance(refreshInterval - time.Second)
		if _, err := r.key(idp, discover(idp), kid, "ES256"); err != nil {
			t.Fatal(err)
		}
		if d, j := idp.Requests(); d != 1 || j != 1 {
			t.Errorf("before the interval: %d and %d requests, want 1 and 1", d, j)
		}
		r.clk.advance(time.Second) // exactly the interval
		idp.SetFault(fakeidp.Delay(800 * time.Millisecond))
		start := time.Now()
		if _, err := r.key(idp, discover(idp), kid, "ES256"); err != nil {
			t.Fatal(err)
		}
		if took := time.Since(start); took > 300*time.Millisecond {
			t.Errorf("the call after the interval waited %v for the refresh", took)
		}
		testutil.Eventually(t, 2*time.Second, "the background refresh", func() bool { _, j := idp.Requests(); return j == 2 })
		if d, j := idp.Requests(); d != 2 || j != 2 {
			t.Errorf("after the interval: %d discovery and %d key set requests, want 2 and 2 (both are fetched at every refresh)", d, j)
		}
		// Callers during the refresh start no second one.
		for i := 0; i < 20; i++ {
			if _, err := r.key(idp, discover(idp), kid, "ES256"); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(900 * time.Millisecond)
		if _, j := idp.Requests(); j != 2 {
			t.Errorf("%d key set requests: callers during a refresh started more", j)
		}
	})

	t.Run("an unknown key ID refetches once and finds a rotated key", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		r := overFake(t, idp)
		if _, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256"); err != nil {
			t.Fatal(err)
		}
		idp.Rotate()
		newKid := idp.KeyID("ES256")
		// Within the minimum interval of the last attempt: refused at once, no request.
		r.clk.advance(minRefetch - time.Second)
		if _, err := r.key(idp, discover(idp), newKid, "ES256"); !errors.Is(err, jwks.ErrKeyNotFound) {
			t.Errorf("an unknown key ID within %v of the last fetch: %v, want ErrKeyNotFound", minRefetch, err)
		}
		if d, j := idp.Requests(); d != 1 || j != 1 {
			t.Errorf("a refetch within the minimum interval: %d and %d requests", d, j)
		}
		r.clk.advance(time.Second) // the interval has elapsed
		pub, err := r.key(idp, discover(idp), newKid, "ES256")
		if err != nil || pub == nil {
			t.Fatalf("the rotated key: %v", err)
		}
		if d, j := idp.Requests(); d != 2 || j != 2 {
			t.Errorf("%d discovery and %d key set requests, want 2 and 2", d, j)
		}
		// And the old key is gone from the set.
		r.clk.advance(minRefetch)
		if _, err := r.key(idp, discover(idp), "kid-from-before-the-rotation", "ES256"); !errors.Is(err, jwks.ErrKeyNotFound) {
			t.Errorf("an old key ID: %v", err)
		}
	})

	t.Run("a thousand unknown key IDs in ten seconds cause one fetch", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		r := overFake(t, idp)
		if _, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256"); err != nil {
			t.Fatal(err)
		}
		r.clk.advance(minRefetch) // a fetch is allowed now
		var wg sync.WaitGroup
		var notFound, other atomic.Int32
		for g := 0; g < 10; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < 100; i++ {
					_, err := r.key(idp, discover(idp), fmt.Sprintf("forged-%d-%d", g, i), "ES256")
					if errors.Is(err, jwks.ErrKeyNotFound) {
						notFound.Add(1)
					} else {
						other.Add(1)
					}
				}
			}(g)
		}
		wg.Wait()
		if notFound.Load() != 1000 || other.Load() != 0 {
			t.Errorf("%d refused as unknown, %d otherwise, want 1000 and 0", notFound.Load(), other.Load())
		}
		if d, j := idp.Requests(); d != 2 || j != 2 {
			t.Errorf("%d discovery and %d key set requests: 1000 forged key IDs caused more than one refetch (1 + 1 expected)", d, j)
		}
		// Ten seconds later one more is allowed, not more.
		r.clk.advance(minRefetch)
		for i := 0; i < 50; i++ {
			_, _ = r.key(idp, discover(idp), fmt.Sprintf("later-%d", i), "ES256")
		}
		if d, j := idp.Requests(); d != 3 || j != 3 {
			t.Errorf("%d discovery and %d key set requests after ten more seconds, want 3 and 3", d, j)
		}
	})

	t.Run("a failed fetch is not repeated before the interval", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		idp.SetFault(fakeidp.Status500)
		r := overFake(t, idp)
		for i := 0; i < 20; i++ {
			if _, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256"); !isUnavailable(err) {
				t.Fatalf("call %d: %v, want ErrUnavailable", i, err)
			}
		}
		if d, _ := idp.Requests(); d != 1 {
			t.Errorf("%d discovery requests for 20 calls within the interval, want 1", d)
		}
		r.clk.advance(minRefetch - time.Second)
		_, _ = r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256")
		if d, _ := idp.Requests(); d != 1 {
			t.Errorf("a retry before the interval: %d requests", d)
		}
		idp.SetFault(fakeidp.None)
		r.clk.advance(time.Second)
		if _, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256"); err != nil {
			t.Errorf("after the interval, the issuer being well again: %v", err)
		}
	})

	t.Run("registrations with the same source share one entry; other sources do not", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		r := overFake(t, idp)
		for i := 0; i < 5; i++ {
			if _, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256"); err != nil {
				t.Fatal(err)
			}
		}
		if d, j := idp.Requests(); d != 1 || j != 1 {
			t.Errorf("%d and %d requests for one source", d, j)
		}
		if _, err := r.key(idp, explicit(idp), idp.KeyID("ES256"), "ES256"); err != nil {
			t.Fatal(err)
		}
		if _, j := idp.Requests(); j != 2 {
			t.Errorf("the explicit source of the same issuer shares the entry of the discovered one: %d key set requests", j)
		}
	})
}

// TestOutage is T-578: the issuer stops answering.
func TestOutage(t *testing.T) {
	t.Run("keys serve until max_stale, then the source has none", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		r := overFake(t, idp, func(o *jwks.Options) { o.FetchTimeout = 500 * time.Millisecond })
		kid := idp.KeyID("ES256")
		if _, err := r.key(idp, discover(idp), kid, "ES256"); err != nil {
			t.Fatal(err)
		}
		idp.Stop()
		// Through the refresh interval and on: served from memory, and the refresh fails quietly.
		for _, at := range []time.Duration{time.Minute, refreshInterval + time.Second, 3 * time.Hour, maxStale - time.Second} {
			r.clk.advance(at - sinceStart(r))
			pub, err := r.key(idp, discover(idp), kid, "ES256")
			if err != nil || pub == nil {
				t.Errorf("%v into the outage: %v", at, err)
			}
		}
		r.clk.advance(2 * time.Second) // past max_stale
		_, err := r.key(idp, discover(idp), kid, "ES256")
		if err == nil || !isUnavailable(err) || errors.Is(err, jwks.ErrKeyNotFound) {
			t.Errorf("after max_stale: %v, want ErrUnavailable and not a refusal", err)
		}
		// Keys that are stale are dropped for good: the next call does not find them either.
		if _, err := r.key(idp, discover(idp), kid, "ES256"); !isUnavailable(err) {
			t.Errorf("the next call: %v", err)
		}
	})

	t.Run("the issuer returns: the next use after the interval restores the keys", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		r := overFake(t, idp, func(o *jwks.Options) { o.FetchTimeout = 500 * time.Millisecond })
		kid := idp.KeyID("ES256")
		if _, err := r.key(idp, discover(idp), kid, "ES256"); err != nil {
			t.Fatal(err)
		}
		idp.Stop()
		r.clk.advance(maxStale + time.Hour)
		if _, err := r.key(idp, discover(idp), kid, "ES256"); !isUnavailable(err) {
			t.Fatalf("during the outage: %v", err)
		}
		idp.Resume()
		// Within the minimum interval of the failed attempt, no fetch is made.
		if _, err := r.key(idp, discover(idp), kid, "ES256"); !isUnavailable(err) {
			t.Errorf("within the interval of the failed attempt: %v", err)
		}
		r.clk.advance(minRefetch)
		if pub, err := r.key(idp, discover(idp), kid, "ES256"); err != nil || pub == nil {
			t.Errorf("after the interval, the issuer being back: %v", err)
		}
	})

	t.Run("a failed refresh keeps the keys and is retried after the minimum interval", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		r := overFake(t, idp, func(o *jwks.Options) { o.FetchTimeout = 500 * time.Millisecond })
		kid := idp.KeyID("ES256")
		if _, err := r.key(idp, discover(idp), kid, "ES256"); err != nil {
			t.Fatal(err)
		}
		idp.SetFault(fakeidp.Status500)
		r.clk.advance(refreshInterval + time.Second)
		if _, err := r.key(idp, discover(idp), kid, "ES256"); err != nil {
			t.Fatal(err)
		}
		testutil.Eventually(t, 2*time.Second, "the failed refresh", func() bool { d, _ := idp.Requests(); return d == 2 })
		for i := 0; i < 10; i++ {
			if _, err := r.key(idp, discover(idp), kid, "ES256"); err != nil {
				t.Fatalf("with the refresh failed: %v", err)
			}
		}
		time.Sleep(100 * time.Millisecond)
		if d, _ := idp.Requests(); d != 2 {
			t.Errorf("%d discovery requests: a failed refresh was retried within the minimum interval", d)
		}
		r.clk.advance(minRefetch)
		idp.SetFault(fakeidp.None)
		if _, err := r.key(idp, discover(idp), kid, "ES256"); err != nil {
			t.Fatal(err)
		}
		testutil.Eventually(t, 2*time.Second, "the retry", func() bool { d, _ := idp.Requests(); return d == 3 })
	})

	t.Run("a source that never answered is ErrUnavailable, not a refusal", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		idp.Stop()
		r := overFake(t, idp, func(o *jwks.Options) { o.FetchTimeout = 500 * time.Millisecond })
		_, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256")
		if !isUnavailable(err) || errors.Is(err, jwks.ErrKeyNotFound) {
			t.Errorf("err = %v, want ErrUnavailable and not ErrKeyNotFound", err)
		}
		// A key set that answers but does not hold the key is a refusal, which is different.
		live := fakeidp.Start(t, fakeidp.Options{})
		r2 := overFake(t, live)
		if _, err := r2.key(live, discover(live), "no-such-key", "ES256"); !errors.Is(err, jwks.ErrKeyNotFound) || isUnavailable(err) {
			t.Errorf("an unknown key in a key set that answers: %v, want ErrKeyNotFound", err)
		}
	})

	t.Run("the cache is bounded at 1024 sources", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{NoDiscovery: true})
		r := overFake(t, idp)
		for i := 0; i < 1100; i++ {
			src := jwks.Source{Issuer: fmt.Sprintf("https://issuer-%d.example.test/", i), JWKSURI: idp.JWKSURI()}
			if _, err := r.key(idp, src, idp.KeyID("ES256"), "ES256"); err != nil {
				t.Fatalf("source %d: %v", i, err)
			}
		}
		// The first source was the least recently used: it is fetched again; the last one is not.
		_, before := idp.Requests()
		if _, err := r.key(idp, jwks.Source{Issuer: "https://issuer-1099.example.test/", JWKSURI: idp.JWKSURI()}, idp.KeyID("ES256"), "ES256"); err != nil {
			t.Fatal(err)
		}
		if _, after := idp.Requests(); after != before {
			t.Error("the most recent source was evicted")
		}
		if _, err := r.key(idp, jwks.Source{Issuer: "https://issuer-0.example.test/", JWKSURI: idp.JWKSURI()}, idp.KeyID("ES256"), "ES256"); err != nil {
			t.Fatal(err)
		}
		if _, after := idp.Requests(); after != before+1 {
			t.Errorf("the least recently used source was not evicted: %d requests, want %d", after, before+1)
		}
	})
}

// sinceStart is how far the clock of the rig has moved from the instant it started at.
func sinceStart(r *rig) time.Duration {
	return r.clk.now().Sub(time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC))
}
