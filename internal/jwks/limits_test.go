package jwks_test

import (
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/jwks"
	"github.com/pandokos/pandokos/test/fakeidp"
)

// TestLimits is T-576: size and time limits, the failure classes of section 5.5, and what a
// failure writes to the log.
func TestLimits(t *testing.T) {
	const discoveryMax, keySetMax = 64 << 10, 256 << 10

	t.Run("a discovery document of 64 KiB is read, one byte more is refused", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		idp.SetFault(fakeidp.Padded(discoveryMax))
		r := overFake(t, idp)
		if _, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256"); err != nil {
			t.Errorf("a document of exactly 64 KiB: %v", err)
		}
		idp = fakeidp.Start(t, fakeidp.Options{})
		idp.SetFault(fakeidp.Padded(discoveryMax + 1))
		r = overFake(t, idp)
		if _, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256"); !isUnavailable(err) {
			t.Errorf("a document of 64 KiB + 1: %v, want ErrUnavailable", err)
		}
		if got := r.failureClasses(t); len(got) != 1 || got[0] != "too_large" {
			t.Errorf("failure classes %v, want [too_large]", got)
		}
	})

	t.Run("a key set of 256 KiB is read, one byte more is refused", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{NoDiscovery: true})
		idp.SetFault(fakeidp.Padded(keySetMax))
		r := overFake(t, idp)
		if _, err := r.key(idp, explicit(idp), idp.KeyID("ES256"), "ES256"); err != nil {
			t.Errorf("a key set of exactly 256 KiB: %v", err)
		}
		idp = fakeidp.Start(t, fakeidp.Options{NoDiscovery: true})
		idp.SetFault(fakeidp.Padded(keySetMax + 1))
		r = overFake(t, idp)
		if _, err := r.key(idp, explicit(idp), idp.KeyID("ES256"), "ES256"); !isUnavailable(err) {
			t.Errorf("a key set of 256 KiB + 1: %v, want ErrUnavailable", err)
		}
		if got := r.failureClasses(t); len(got) != 1 || got[0] != "too_large" {
			t.Errorf("failure classes %v, want [too_large]", got)
		}
	})

	t.Run("an oversize answer is refused without being read to the end", func(t *testing.T) {
		for name, src := range map[string]func(*fakeidp.Server) jwks.Source{"discovery": discover, "key set": explicit} {
			idp := fakeidp.Start(t, fakeidp.Options{})
			idp.SetFault(fakeidp.Oversize) // the document and 64 MiB of blanks
			r := overFake(t, idp)
			start := time.Now()
			_, err := r.key(idp, src(idp), idp.KeyID("ES256"), "ES256")
			if !isUnavailable(err) {
				t.Errorf("%s: err = %v", name, err)
			}
			if took := time.Since(start); took > 2*time.Second {
				t.Errorf("%s: the refusal took %v", name, took)
			}
			// Whatever the kernel buffered, a reader that stopped at the limit leaves nearly all of
			// the 64 MiB unsent.
			time.Sleep(50 * time.Millisecond)
			if sent := idp.BytesSent(); sent > 16<<20 {
				t.Errorf("%s: the server sent %d bytes: the document was read to its end", name, sent)
			}
			if got := r.failureClasses(t); len(got) != 1 || got[0] != "too_large" {
				t.Errorf("%s: failure classes %v, want [too_large]", name, got)
			}
		}
	})

	t.Run("a response that never ends is cut at the timeout", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		idp.SetFault(fakeidp.Endless)
		r := overFake(t, idp, func(o *jwks.Options) { o.FetchTimeout = 300 * time.Millisecond })
		start := time.Now()
		_, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256")
		took := time.Since(start)
		if !isUnavailable(err) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
		if took < 250*time.Millisecond || took > 1500*time.Millisecond {
			t.Errorf("the fetch took %v with a timeout of 300 ms", took)
		}
		if got := r.failureClasses(t); len(got) != 1 || got[0] != "timeout" {
			t.Errorf("failure classes %v, want [timeout]", got)
		}
	})

	t.Run("the timeout covers discovery and key set together", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		idp.SetFault(fakeidp.Delay(400 * time.Millisecond)) // each document takes 400 ms
		r := overFake(t, idp, func(o *jwks.Options) { o.FetchTimeout = 600 * time.Millisecond })
		start := time.Now()
		_, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256")
		if !isUnavailable(err) {
			t.Errorf("two documents of 400 ms in 600 ms: err = %v, want ErrUnavailable", err)
		}
		if took := time.Since(start); took > 1200*time.Millisecond {
			t.Errorf("the fetch took %v with a timeout of 600 ms", took)
		}
		if got := r.failureClasses(t); len(got) != 1 || got[0] != "timeout" {
			t.Errorf("failure classes %v, want [timeout]", got)
		}
	})

	t.Run("the failure classes", func(t *testing.T) {
		cases := []struct {
			class string
			setup func(*fakeidp.Server)
		}{
			{"status", func(s *fakeidp.Server) { s.SetFault(fakeidp.Status500) }},
			{"too_large", func(s *fakeidp.Server) { s.SetFault(fakeidp.Padded(discoveryMax + 1)) }},
			{"invalid_document", func(s *fakeidp.Server) { s.SetFault(fakeidp.Garbage) }},
			{"issuer_mismatch", func(s *fakeidp.Server) { s.SetFault(fakeidp.WrongIssuer) }},
			{"network", func(s *fakeidp.Server) { s.SetFault(fakeidp.Refuse) }},
			{"network", func(s *fakeidp.Server) { s.Stop() }},
		}
		for _, c := range cases {
			idp := fakeidp.Start(t, fakeidp.Options{})
			c.setup(idp)
			r := overFake(t, idp)
			if _, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256"); !isUnavailable(err) {
				t.Errorf("%s: err = %v", c.class, err)
			}
			if got := r.failureClasses(t); len(got) != 1 || got[0] != c.class {
				t.Errorf("failure classes %v, want [%s]", got, c.class)
			}
		}
	})

	t.Run("a failure logs its class and the issuer, and nothing of the answer", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		idp.SetFault(fakeidp.Garbage)
		r := overFake(t, idp)
		_, _ = r.key(idp, jwks.Source{Issuer: idp.Issuer(), JWKSURI: idp.JWKSURI() + "?token=hunter2#frag"}, "k", "ES256")
		out := r.logs.String()
		for _, secret := range []string{"hunter2", "this is not json", "frag"} {
			if strings.Contains(out, secret) {
				t.Errorf("the log holds %q:\n%s", secret, out)
			}
		}
		lines := r.logs.Find(t, "issuer keys not fetched")
		if len(lines) != 1 || lines[0]["level"] != "WARN" && lines[0]["level"] != "warn" {
			t.Errorf("lines: %v", lines)
		}
	})

	t.Run("a success is logged with the number of keys and no key material", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		r := overFake(t, idp)
		if _, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256"); err != nil {
			t.Fatal(err)
		}
		lines := r.logs.Find(t, "issuer keys fetched")
		if len(lines) != 1 {
			t.Fatalf("lines: %v", lines)
		}
		if n, _ := lines[0]["keys"].(float64); n != 5 {
			t.Errorf("keys = %v, want 5", lines[0]["keys"])
		}
		for _, secret := range []string{string(idp.JWKS()), idp.KeyID("ES256")} {
			if strings.Contains(r.logs.String(), secret) {
				t.Errorf("the log holds %.40q", secret)
			}
		}
	})
}
