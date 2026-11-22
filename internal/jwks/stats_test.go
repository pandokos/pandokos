package jwks_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/jwks"
	"github.com/pandokos/pandokos/test/fakeidp"
	"github.com/pandokos/pandokos/test/testutil"
)

// multiTransport routes a request to the transport of its host: one cache over several fake issuers.
type multiTransport map[string]*http.Transport

func (m multiTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tr, ok := m[r.URL.Host]
	if !ok {
		return nil, errors.New("no transport for " + r.URL.Host)
	}
	return tr.RoundTrip(r)
}

// fetchCounts flattens Stats().Fetches (a map keyed by a value with the fields Document and Result)
// into "document|result" counts.
func fetchCounts(c *jwks.Cache) map[string]int64 {
	out := map[string]int64{}
	for k, v := range c.Stats().Fetches {
		out[k.Document+"|"+k.Result] += v
	}
	return out
}

// TestStats is T-919: fetches by document and result (ok and each class provoked); sources by state
// (fresh, stale, missing); the time of the oldest last success among the stale sources, from which
// the staleness grows while a source fails and is nothing after a success; no issuer ID or URL in
// the snapshot.
func TestStats(t *testing.T) {
	const keySetMax = 256 << 10

	t.Run("a new cache has counted nothing", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		r := overFake(t, idp)
		st := r.c.Stats()
		if len(fetchCounts(r.c)) != 0 || len(st.Sources) != 0 && st.Sources["fresh"]+st.Sources["stale"]+st.Sources["missing"] != 0 || !st.OldestStale.IsZero() {
			t.Errorf("%+v", st)
		}
	})

	t.Run("successful fetches by document", func(t *testing.T) {
		keysOnly := fakeidp.Start(t, fakeidp.Options{NoDiscovery: true})
		r := overFake(t, keysOnly)
		if _, err := r.key(keysOnly, explicit(keysOnly), keysOnly.KeyID("ES256"), "ES256"); err != nil {
			t.Fatal(err)
		}
		if got := fetchCounts(r.c); got["keys|ok"] != 1 || len(got) != 1 {
			t.Errorf("an explicit key set: %v", got)
		}
		if st := r.c.Stats(); st.Sources["fresh"] != 1 || st.Sources["stale"] != 0 || st.Sources["missing"] != 0 {
			t.Errorf("sources %v", st.Sources)
		}

		idp := fakeidp.Start(t, fakeidp.Options{})
		r2 := overFake(t, idp)
		if _, err := r2.key(idp, discover(idp), idp.KeyID("ES256"), "ES256"); err != nil {
			t.Fatal(err)
		}
		if got := fetchCounts(r2.c); got["metadata|ok"] != 1 || got["keys|ok"] != 1 || len(got) != 2 {
			t.Errorf("by discovery: %v", got)
		}
		// A second lookup is served from memory: nothing is fetched.
		if _, err := r2.key(idp, discover(idp), idp.KeyID("ES256"), "ES256"); err != nil {
			t.Fatal(err)
		}
		if got := fetchCounts(r2.c); got["metadata|ok"] != 1 || got["keys|ok"] != 1 {
			t.Errorf("a lookup from memory was counted as a fetch: %v", got)
		}
	})

	t.Run("each class of a failed fetch, by document", func(t *testing.T) {
		cases := []struct {
			class, document string
			discover        bool
			fault           fakeidp.Fault
			stop            bool
		}{
			{"status", "metadata", true, fakeidp.Status500, false},
			{"status", "keys", false, fakeidp.Status500, false},
			{"invalid_document", "metadata", true, fakeidp.Garbage, false},
			{"invalid_document", "keys", false, fakeidp.Garbage, false},
			{"issuer_mismatch", "metadata", true, fakeidp.WrongIssuer, false},
			{"too_large", "keys", false, fakeidp.Padded(keySetMax + 1), false},
			{"network", "metadata", true, fakeidp.Refuse, false},
			{"network", "keys", false, fakeidp.Refuse, false},
			{"network", "keys", false, fakeidp.None, true},
		}
		for _, c := range cases {
			idp := fakeidp.Start(t, fakeidp.Options{NoDiscovery: !c.discover})
			idp.SetFault(c.fault)
			if c.stop {
				idp.Stop()
			}
			r := overFake(t, idp)
			src := explicit(idp)
			if c.discover {
				src = discover(idp)
			}
			if _, err := r.key(idp, src, idp.KeyID("ES256"), "ES256"); !isUnavailable(err) {
				t.Errorf("%s/%s: err = %v", c.document, c.class, err)
			}
			got := fetchCounts(r.c)
			if got[c.document+"|"+c.class] != 1 || len(got) != 1 {
				t.Errorf("%s/%s: counted %v", c.document, c.class, got)
			}
			if st := r.c.Stats(); st.Sources["missing"] != 1 || st.Sources["fresh"] != 0 || st.Sources["stale"] != 0 {
				t.Errorf("%s/%s: a source that never fetched is missing: %v", c.document, c.class, st.Sources)
			}
		}
	})

	t.Run("a timeout", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{NoDiscovery: true})
		idp.SetFault(fakeidp.Delay(900 * time.Millisecond))
		r := overFake(t, idp, func(o *jwks.Options) { o.FetchTimeout = 300 * time.Millisecond })
		if _, err := r.key(idp, explicit(idp), idp.KeyID("ES256"), "ES256"); !isUnavailable(err) {
			t.Errorf("err = %v", err)
		}
		if got := fetchCounts(r.c); got["keys|timeout"] != 1 || len(got) != 1 {
			t.Errorf("counted %v", got)
		}
	})

	t.Run("states and staleness follow the clock and the fetches", func(t *testing.T) {
		a := fakeidp.Start(t, fakeidp.Options{NoDiscovery: true})
		b := fakeidp.Start(t, fakeidp.Options{NoDiscovery: true})
		rt := a.Transport()
		rtB := b.Transport()
		// One cache over both issuers: the transport picks the issuer's own trust.
		r := newRig(t, func(bool) http.RoundTripper { return multiTransport{a.Host(): rt, b.Host(): rtB} })

		t0 := r.clk.now()
		if _, err := r.key(a, explicit(a), a.KeyID("ES256"), "ES256"); err != nil {
			t.Fatal(err)
		}
		r.clk.advance(25 * time.Minute)
		t1 := r.clk.now()
		if _, err := r.key(b, explicit(b), b.KeyID("ES256"), "ES256"); err != nil {
			t.Fatal(err)
		}
		st := r.c.Stats()
		if st.Sources["fresh"] != 2 || st.Sources["stale"] != 0 || !st.OldestStale.IsZero() {
			t.Fatalf("two sources fetched within 2 refresh intervals: %v oldest stale %v", st.Sources, st.OldestStale)
		}

		// Both issuers go away; time passes beyond twice the refresh interval for the first only.
		a.SetFault(fakeidp.Status500)
		b.SetFault(fakeidp.Status500)
		r.clk.advance(10 * time.Minute) // a: 35 minutes since its success; b: 10
		testutil.Eventually(t, 5*time.Second, "the first source to be stale", func() bool {
			_, _ = r.key(a, explicit(a), a.KeyID("ES256"), "ES256") // answered from memory; the refresh fails in the background
			s := r.c.Stats()
			return s.Sources["stale"] == 1 && s.Sources["fresh"] == 1
		})
		if got := r.c.Stats().OldestStale; !got.Equal(t0) {
			t.Errorf("the oldest success among the stale sources is %v, want %v", got, t0)
		}

		// The staleness grows while the source keeps failing.
		r.clk.advance(30 * time.Minute) // a: 65 minutes; b: 40: both stale
		testutil.Eventually(t, 5*time.Second, "both sources to be stale", func() bool {
			_, _ = r.key(a, explicit(a), a.KeyID("ES256"), "ES256")
			_, _ = r.key(b, explicit(b), b.KeyID("ES256"), "ES256")
			s := r.c.Stats()
			return s.Sources["stale"] == 2 && s.Sources["fresh"] == 0
		})
		if got := r.c.Stats().OldestStale; !got.Equal(t0) || r.clk.now().Sub(got) != 65*time.Minute {
			t.Errorf("oldest stale %v at %v: the staleness must be 65 minutes", got, r.clk.now())
		}
		_ = t1

		// A success resets it.
		a.SetFault(fakeidp.None)
		b.SetFault(fakeidp.None)
		r.clk.advance(time.Minute)
		testutil.Eventually(t, 5*time.Second, "both sources to be fresh", func() bool {
			_, _ = r.key(a, explicit(a), a.KeyID("ES256"), "ES256")
			_, _ = r.key(b, explicit(b), b.KeyID("ES256"), "ES256")
			s := r.c.Stats()
			return s.Sources["fresh"] == 2 && s.Sources["stale"] == 0
		})
		if got := r.c.Stats().OldestStale; !got.IsZero() {
			t.Errorf("after a success the oldest stale time is %v, want none", got)
		}

		// Past MaxStale without a success the keys are dropped: the source is missing, not stale.
		a.SetFault(fakeidp.Status500)
		b.SetFault(fakeidp.Status500)
		r.clk.advance(maxStale + time.Hour)
		_, _ = r.key(a, explicit(a), a.KeyID("ES256"), "ES256")
		_, _ = r.key(b, explicit(b), b.KeyID("ES256"), "ES256")
		if st := r.c.Stats(); st.Sources["missing"] != 2 || st.Sources["stale"] != 0 || !st.OldestStale.IsZero() {
			t.Errorf("past MaxStale: %v oldest stale %v", st.Sources, st.OldestStale)
		}
	})

	t.Run("the snapshot holds no issuer ID or URL", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		r := overFake(t, idp)
		_, _ = r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256")
		idp.SetFault(fakeidp.Status500)
		r2 := overFake(t, idp)
		_, _ = r2.key(idp, explicit(idp), "k", "ES256")
		closed := map[string]bool{"ok": true}
		for _, c := range jwks.FetchClasses {
			closed[c] = true
		}
		for _, c := range []*jwks.Cache{r.c, r2.c} {
			st := c.Stats()
			for k := range st.Fetches {
				if (k.Document != "keys" && k.Document != "metadata") || !closed[k.Result] {
					t.Errorf("a value outside the closed lists: %+v", k)
				}
				if strings.Contains(k.Document+k.Result, idp.Host()) || strings.Contains(k.Document+k.Result, "https") {
					t.Errorf("a URL or host in %+v", k)
				}
			}
			for k := range st.Sources {
				if k != "fresh" && k != "stale" && k != "missing" {
					t.Errorf("the state %q", k)
				}
			}
		}
	})
}
