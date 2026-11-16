package jwks_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/jwks"
	"github.com/pandokos/pandokos/test/fakeidp"
)

// TestDiscovery is T-574: where the key set is found, and what is refused on the way.
func TestDiscovery(t *testing.T) {
	t.Run("the discovery path of an issuer with and without a trailing slash", func(t *testing.T) {
		for _, path := range []string{"/", "/realms/acme", "/realms/acme/"} {
			idp := fakeidp.Start(t, fakeidp.Options{Path: path})
			r := overFake(t, idp)
			pub, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256")
			if err != nil || pub == nil {
				t.Errorf("issuer %q: %v", idp.Issuer(), err)
				continue
			}
			if d, j := idp.Requests(); d != 1 || j != 1 {
				t.Errorf("issuer %q: %d discovery and %d key set requests, want 1 and 1", idp.Issuer(), d, j)
			}
		}
	})

	t.Run("the issuer of the document must be the registered one, byte for byte", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{Path: "/realms/acme"})
		for name, issuer := range map[string]string{
			"a trailing slash added": idp.Issuer() + "/",
			"upper case":             strings.ToUpper(idp.Issuer()),
			"another path":           strings.Replace(idp.Issuer(), "/realms/acme", "/realms/other", 1),
			"another host":           "https://other.example.test/realms/acme",
			"a query":                idp.Issuer() + "?x=1",
		} {
			r := overFake(t, idp)
			_, err := r.key(idp, jwks.Source{Issuer: issuer}, idp.KeyID("ES256"), "ES256")
			if err == nil {
				t.Errorf("%s: keys were returned for an issuer the document does not name", name)
			} else if !isUnavailable(err) {
				t.Errorf("%s: %v, want ErrUnavailable (the keys cannot be obtained)", name, err)
			}
		}
		// A document that names another issuer: the class issuer_mismatch, and no key set request.
		idp2 := fakeidp.Start(t, fakeidp.Options{})
		idp2.SetFault(fakeidp.WrongIssuer)
		r := overFake(t, idp2)
		if _, err := r.key(idp2, discover(idp2), idp2.KeyID("ES256"), "ES256"); !isUnavailable(err) {
			t.Errorf("a document of another issuer: %v", err)
		}
		if _, j := idp2.Requests(); j != 0 {
			t.Errorf("the key set was fetched (%d requests) after a discovery document of another issuer", j)
		}
		if got := r.failureClasses(t); len(got) != 1 || got[0] != "issuer_mismatch" {
			t.Errorf("failure classes %v, want [issuer_mismatch]", got)
		}
	})

	t.Run("a jwks_uri that is not an https URL without user information is refused", func(t *testing.T) {
		for name, uri := range map[string]string{
			"http":             "http://localhost:1/jwks.json",
			"user information": "https://user:secret@localhost:1/jwks.json",
			"a user name only": "https://user@localhost:1/jwks.json",
			"relative":         "/jwks.json",
			"a relative path":  "jwks.json",
			"no host":          "https:///jwks.json",
			"another scheme":   "ftp://localhost/jwks.json",
			"a data URL":       "data:application/json,{}",
			"empty":            "",
		} {
			idp := fakeidp.Start(t, fakeidp.Options{})
			idp.SetDiscoveryJWKSURI(uri)
			r := overFake(t, idp)
			_, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256")
			if err == nil {
				t.Errorf("%s: keys were returned", name)
				continue
			}
			if !isUnavailable(err) {
				t.Errorf("%s: %v, want ErrUnavailable", name, err)
			}
			if _, j := idp.Requests(); j != 0 {
				t.Errorf("%s: the key set was requested %d times", name, j)
			}
			if strings.Contains(r.logs.String(), "secret") {
				t.Errorf("%s: the log holds the user information of the address:\n%s", name, r.logs.String())
			}
		}
	})

	t.Run("an explicit jwks_uri skips discovery", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{NoDiscovery: true})
		r := overFake(t, idp)
		if _, err := r.key(idp, explicit(idp), idp.KeyID("EdDSA"), "EdDSA"); err != nil {
			t.Fatal(err)
		}
		if d, j := idp.Requests(); d != 0 || j != 1 {
			t.Errorf("%d discovery and %d key set requests, want 0 and 1", d, j)
		}
		// And discovery is not attempted when the issuer has none and no address is given.
		r2 := overFake(t, idp)
		if _, err := r2.key(idp, discover(idp), idp.KeyID("EdDSA"), "EdDSA"); !isUnavailable(err) {
			t.Errorf("an issuer without discovery and without an address: %v", err)
		}
	})

	t.Run("the key set may be on another host than the issuer", func(t *testing.T) {
		issuer := fakeidp.Start(t, fakeidp.Options{})
		keys := fakeidp.Start(t, fakeidp.Options{NoDiscovery: true})
		_ = issuer
		r := overFake(t, keys)
		pub, err := r.key(keys, jwks.Source{Issuer: "https://issuer.example.test/", JWKSURI: keys.JWKSURI()}, keys.KeyID("ES256"), "ES256")
		if err != nil || pub == nil {
			t.Errorf("%v", err)
		}
	})

	t.Run("status 404 and 500 are failures of class status", func(t *testing.T) {
		for _, f := range []fakeidp.Fault{fakeidp.Status(404), fakeidp.Status500, fakeidp.Status(403), fakeidp.Status(204)} {
			idp := fakeidp.Start(t, fakeidp.Options{})
			idp.SetFault(f)
			r := overFake(t, idp)
			if _, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256"); !isUnavailable(err) {
				t.Errorf("err = %v, want ErrUnavailable", err)
			}
			if got := r.failureClasses(t); len(got) != 1 || got[0] != "status" {
				t.Errorf("failure classes %v, want [status]", got)
			}
		}
		// The key set answering 404 after a good discovery document.
		idp := fakeidp.Start(t, fakeidp.Options{})
		idp.SetDiscoveryJWKSURI("https://" + idp.Host() + "/missing.json")
		r := overFake(t, idp)
		if _, err := r.key(idp, discover(idp), idp.KeyID("ES256"), "ES256"); !isUnavailable(err) {
			t.Errorf("err = %v", err)
		}
		if got := r.failureClasses(t); len(got) != 1 || got[0] != "status" {
			t.Errorf("failure classes %v, want [status]", got)
		}
	})

	t.Run("a redirect is not followed", func(t *testing.T) {
		target := fakeidp.Start(t, fakeidp.Options{})
		for name, mk := range map[string]func(*fakeidp.Server) jwks.Source{
			"in discovery":   discover,
			"in the key set": explicit,
		} {
			idp := fakeidp.Start(t, fakeidp.Options{})
			idp.SetFault(fakeidp.Redirect(target.JWKSURI()))
			r := overFake(t, idp)
			if _, err := r.key(idp, mk(idp), idp.KeyID("ES256"), "ES256"); !isUnavailable(err) {
				t.Errorf("%s: err = %v, want ErrUnavailable", name, err)
			}
			if _, j := target.Requests(); j != 0 {
				t.Errorf("%s: the redirect target was requested %d times", name, j)
			}
			if got := r.failureClasses(t); len(got) != 1 || got[0] != "status" {
				t.Errorf("%s: failure classes %v, want [status]", name, got)
			}
		}
	})

	t.Run("a fetch is a GET with Accept and nothing else of its own", func(t *testing.T) {
		d := &docs{keys: []byte(`{"keys":[]}`), discovery: func(string) []byte {
			return []byte(`{"issuer":"https://idp.example.test/","jwks_uri":"https://idp.example.test/jwks.json"}`)
		}}
		r := newRig(t, func(bool) http.RoundTripper { return d })
		_, _ = r.key(nil, jwks.Source{Issuer: "https://idp.example.test/"}, "k", "ES256")
		seen := d.requests()
		if len(seen) != 2 {
			t.Fatalf("%d requests, want 2 (discovery, key set): %+v", len(seen), seen)
		}
		for _, s := range seen {
			if s.Method != "GET" || s.Scheme != "https" || s.Host != "idp.example.test" || s.Accept != "application/json" {
				t.Errorf("request %+v", s)
			}
			for h := range s.Header {
				if h != "Accept" && h != "User-Agent" {
					t.Errorf("the request carries the header %s", h)
				}
			}
		}
		if seen[0].Path != "/.well-known/openid-configuration" || seen[1].Path != "/jwks.json" {
			t.Errorf("paths %q %q", seen[0].Path, seen[1].Path)
		}
	})

	_ = time.Second
}
