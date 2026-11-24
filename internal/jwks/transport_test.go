package jwks_test

import (
	"crypto/x509"
	"os"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/egress"
	"github.com/pandokos/pandokos/internal/jwks"
	"github.com/pandokos/pandokos/test/fakeidp"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestTransportClass is T-579: which transport fetches which document, and what the real egress
// guard makes of it.
func TestTransportClass(t *testing.T) {
	keySet := []byte(`{"keys":[]}`)
	discovery := func(host string) []byte {
		return []byte(`{"issuer":"https://` + host + `/","jwks_uri":"https://` + host + `/jwks.json"}`)
	}
	fetchedBy := func(t *testing.T, rec *classRecorder) (public, private []string) {
		t.Helper()
		for _, s := range rec.public.requests() {
			public = append(public, s.Path)
		}
		for _, s := range rec.private.requests() {
			private = append(private, s.Path)
		}
		return
	}

	t.Run("public for an unlisted host, private for a listed one", func(t *testing.T) {
		rec := newClassRecorder(discovery, keySet)
		r := newRig(t, rec.transport, func(o *jwks.Options) { o.PrivateHosts = []string{"idp.internal.test"} })
		_, _ = r.key(nil, jwks.Source{Issuer: "https://idp.example.test/"}, "k", "ES256")
		if pub, priv := fetchedBy(t, rec); len(pub) != 2 || len(priv) != 0 {
			t.Errorf("an unlisted host: public %v, private %v", pub, priv)
		}
		rec = newClassRecorder(discovery, keySet)
		r = newRig(t, rec.transport, func(o *jwks.Options) { o.PrivateHosts = []string{"idp.internal.test"} })
		_, _ = r.key(nil, jwks.Source{Issuer: "https://idp.internal.test/"}, "k", "ES256")
		if pub, priv := fetchedBy(t, rec); len(pub) != 0 || len(priv) != 2 {
			t.Errorf("a listed host: public %v, private %v", pub, priv)
		}
	})

	t.Run("the class is chosen per document: discovery by the issuer's host, the key set by the host of jwks_uri", func(t *testing.T) {
		rec := newClassRecorder(func(string) []byte {
			return []byte(`{"issuer":"https://idp.internal.test/","jwks_uri":"https://keys.example.test/jwks.json"}`)
		}, keySet)
		r := newRig(t, rec.transport, func(o *jwks.Options) { o.PrivateHosts = []string{"idp.internal.test"} })
		_, _ = r.key(nil, jwks.Source{Issuer: "https://idp.internal.test/"}, "k", "ES256")
		pub, priv := fetchedBy(t, rec)
		if len(priv) != 1 || priv[0] != "/.well-known/openid-configuration" || len(pub) != 1 || pub[0] != "/jwks.json" {
			t.Errorf("public %v, private %v: discovery should be private and the key set public", pub, priv)
		}
		// The other way round: a public issuer whose key set is on a listed host.
		rec = newClassRecorder(func(string) []byte {
			return []byte(`{"issuer":"https://idp.example.test/","jwks_uri":"https://keys.internal.test/jwks.json"}`)
		}, keySet)
		r = newRig(t, rec.transport, func(o *jwks.Options) { o.PrivateHosts = []string{"keys.internal.test"} })
		_, _ = r.key(nil, jwks.Source{Issuer: "https://idp.example.test/"}, "k", "ES256")
		pub, priv = fetchedBy(t, rec)
		if len(pub) != 1 || pub[0] != "/.well-known/openid-configuration" || len(priv) != 1 || priv[0] != "/jwks.json" {
			t.Errorf("public %v, private %v: discovery should be public and the key set private", pub, priv)
		}
	})

	t.Run("the host match is exact and case-insensitive, and ignores the port", func(t *testing.T) {
		for _, c := range []struct {
			issuer  string
			private bool
		}{
			{"https://idp.internal.test/", true},
			{"https://IDP.Internal.TEST/", true},
			{"https://idp.internal.test:8443/", true},
			{"https://idp.internal.test:443/realm", true},
			{"https://evil.idp.internal.test/", false},      // a subdomain is another host
			{"https://idp.internal.test.evil.test/", false}, // a suffix is another host
			{"https://xidp.internal.test/", false},
			{"https://internal.test/", false},
			{"https://idp.internal.test.:8443/", false}, // a rooted name is not the listed one: the guard still protects it
		} {
			rec := newClassRecorder(discovery, keySet)
			r := newRig(t, rec.transport, func(o *jwks.Options) { o.PrivateHosts = []string{"idp.internal.test"} })
			_, _ = r.key(nil, jwks.Source{Issuer: c.issuer, JWKSURI: strings.TrimSuffix(c.issuer, "/") + "/jwks.json"}, "k", "ES256")
			pub, priv := fetchedBy(t, rec)
			if got := len(priv) > 0; got != c.private || (len(pub) > 0) == c.private {
				t.Errorf("%s: public %v private %v, want private=%v", c.issuer, pub, priv, c.private)
			}
		}
	})

	t.Run("http is refused for both classes, before any request", func(t *testing.T) {
		for _, host := range []string{"idp.example.test", "idp.internal.test"} {
			rec := newClassRecorder(discovery, keySet)
			r := newRig(t, rec.transport, func(o *jwks.Options) { o.PrivateHosts = []string{"idp.internal.test"} })
			for name, src := range map[string]jwks.Source{
				"an http issuer":   {Issuer: "http://" + host + "/"},
				"an http jwks_uri": {Issuer: "https://" + host + "/", JWKSURI: "http://" + host + "/jwks.json"},
			} {
				if _, err := r.key(nil, src, "k", "ES256"); !isUnavailable(err) {
					t.Errorf("%s on %s: err = %v, want ErrUnavailable", name, host, err)
				}
			}
			if pub, priv := fetchedBy(t, rec); len(pub)+len(priv) != 0 {
				t.Errorf("%s: requests were made for http addresses: public %v, private %v", host, pub, priv)
			}
		}
	})

	// The real guard, for the last two rows.
	pool := func(t *testing.T, idp *fakeidp.Server) func(*egress.Options) {
		t.Helper()
		pem, err := os.ReadFile(idp.CAFile())
		if err != nil {
			t.Fatal(err)
		}
		p := x509.NewCertPool()
		if !p.AppendCertsFromPEM(pem) {
			t.Fatal("the authority of the fake issuer does not load")
		}
		return func(o *egress.Options) { o.RootCAs = p }
	}

	t.Run("with the real guard an issuer on loopback is blocked unless its host is listed", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		guard := testutil.TestGuard(t, pool(t, idp))
		r := newRig(t, guard.Transport)
		if _, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256"); !isUnavailable(err) {
			t.Errorf("unlisted: err = %v, want ErrUnavailable", err)
		}
		if got := r.failureClasses(t); len(got) != 1 || got[0] != "blocked" {
			t.Errorf("failure classes %v, want [blocked]", got)
		}
		if lines := r.logs.Find(t, "issuer keys not fetched"); len(lines) == 1 && lines[0]["reason"] != "private_address" {
			t.Errorf("the guard's reason: %v, want private_address", lines[0]["reason"])
		}
		if d, j := idp.Requests(); d+j != 0 {
			t.Errorf("the blocked issuer received %d requests", d+j)
		}
		// Listed: fetched.
		r = newRig(t, guard.Transport, func(o *jwks.Options) { o.PrivateHosts = []string{"localhost"} })
		pub, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256")
		if err != nil || pub == nil {
			t.Errorf("listed: %v\n%s", err, r.logs.String())
		}
		if d, j := idp.Requests(); d != 1 || j != 1 {
			t.Errorf("listed: %d discovery and %d key set requests", d, j)
		}
		// The match is on the host name, not on what it resolves to: another name for the same
		// address is not listed (127.0.0.1 is an address literal, and the guard refuses it too).
		r = newRig(t, guard.Transport, func(o *jwks.Options) { o.PrivateHosts = []string{"example.test"} })
		if _, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256"); !isUnavailable(err) {
			t.Errorf("another host listed: %v", err)
		}
	})

	t.Run("a forbidden address is blocked even when its host is listed", func(t *testing.T) {
		guard := testutil.TestGuard(t)
		for _, host := range []string{"169.254.169.254", "100.100.100.200", "[fd00:ec2::254]", "169.254.170.23", "[fd00:ec2::23]", "[fd20:ce::254]", "[fe80::a9fe:a9fe]"} {
			r := newRig(t, guard.Transport, func(o *jwks.Options) { o.PrivateHosts = []string{strings.Trim(host, "[]")} })
			src := jwks.Source{Issuer: "https://" + host + "/", JWKSURI: "https://" + host + "/jwks.json"}
			if _, err := r.key(nil, src, "k", "ES256"); !isUnavailable(err) {
				t.Errorf("%s: err = %v, want ErrUnavailable", host, err)
			}
			if got := r.failureClasses(t); len(got) != 1 || got[0] != "blocked" {
				t.Errorf("%s: failure classes %v, want [blocked]", host, got)
			}
			if lines := r.logs.Find(t, "issuer keys not fetched"); len(lines) == 1 && lines[0]["reason"] != "forbidden_address" {
				t.Errorf("%s: the guard's reason %v, want forbidden_address", host, lines[0]["reason"])
			}
		}
	})
}
