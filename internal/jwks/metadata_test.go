package jwks_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/jwks"
	"github.com/pandokos/pandokos/test/fakeidp"
)

func (r *rig) metadata(src string) (jwks.Metadata, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return r.c.Metadata(ctx, src)
}

// TestMetadata is the jwks half of T-811: the discovery document of the identity provider, with
// the endpoints of the code flow.
func TestMetadata(t *testing.T) {
	t.Run("the endpoints of the document", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{Path: "/realms/acme"})
		r := overFake(t, idp)
		m, err := r.metadata(idp.Issuer())
		if err != nil {
			t.Fatalf("Metadata: %v", err)
		}
		want := jwks.Metadata{Issuer: idp.Issuer(), JWKSURI: idp.JWKSURI(), AuthorizationEndpoint: idp.AuthorizeURL(), TokenEndpoint: idp.TokenURL()}
		if m != want {
			t.Errorf("Metadata %+v, want %+v", m, want)
		}
	})

	t.Run("the issuer of the document must be the one asked for", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		idp.SetFault(fakeidp.WrongIssuer)
		_, err := overFake(t, idp).metadata(idp.Issuer())
		if !isUnavailable(err) {
			t.Errorf("a document of another issuer: %v, want ErrUnavailable", err)
		}
		other := fakeidp.Start(t, fakeidp.Options{Path: "/realms/acme"})
		if _, err := overFake(t, other).metadata(other.Issuer() + "/"); !isUnavailable(err) {
			t.Errorf("an issuer with a trailing slash that the document does not have: %v", err)
		}
	})

	t.Run("every endpoint must be https and present", func(t *testing.T) {
		doc := func(auth, token, jwksURI string) func(string) []byte {
			return func(host string) []byte {
				return []byte(fmt.Sprintf(`{"issuer":"https://%s/","jwks_uri":%q,"authorization_endpoint":%q,"token_endpoint":%q}`, host, jwksURI, auth, token))
			}
		}
		cases := map[string]func(string) []byte{
			"authorization endpoint over http": doc("http://idp.example.test/authorize", "https://idp.example.test/token", "https://idp.example.test/jwks.json"),
			"token endpoint over http":         doc("https://idp.example.test/authorize", "http://idp.example.test/token", "https://idp.example.test/jwks.json"),
			"no authorization endpoint":        doc("", "https://idp.example.test/token", "https://idp.example.test/jwks.json"),
			"no token endpoint":                doc("https://idp.example.test/authorize", "", "https://idp.example.test/jwks.json"),
			"a javascript endpoint":            doc("javascript:alert(1)", "https://idp.example.test/token", "https://idp.example.test/jwks.json"),
			"jwks_uri over http":               doc("https://idp.example.test/authorize", "https://idp.example.test/token", "http://idp.example.test/jwks.json"),
		}
		for name, d := range cases {
			rec := newClassRecorder(d, []byte(`{"keys":[]}`))
			r := newRig(t, rec.transport)
			if _, err := r.metadata("https://idp.example.test/"); !isUnavailable(err) {
				t.Errorf("%s: %v, want ErrUnavailable", name, err)
			}
		}
		ok := newClassRecorder(doc("https://idp.example.test/authorize", "https://idp.example.test/token", "https://idp.example.test/jwks.json"), []byte(`{"keys":[]}`))
		if m, err := newRig(t, ok.transport).metadata("https://idp.example.test/"); err != nil || m.TokenEndpoint != "https://idp.example.test/token" {
			t.Errorf("a well-formed document: %+v %v", m, err)
		}
	})

	t.Run("a private host only when it is listed", func(t *testing.T) {
		doc := func(host string) []byte {
			return []byte(fmt.Sprintf(`{"issuer":"https://%s/","jwks_uri":"https://%s/jwks.json","authorization_endpoint":"https://%s/authorize","token_endpoint":"https://%s/token"}`, host, host, host, host))
		}
		rec := newClassRecorder(doc, []byte(`{"keys":[]}`))
		r := newRig(t, rec.transport, func(o *jwks.Options) { o.PrivateHosts = []string{"idp.internal.test"} })
		if _, err := r.metadata("https://idp.example.test/"); err != nil {
			t.Fatal(err)
		}
		if len(rec.private.requests()) != 0 || len(rec.public.requests()) != 1 {
			t.Errorf("an unlisted host: public %d, private %d requests", len(rec.public.requests()), len(rec.private.requests()))
		}
		if _, err := r.metadata("https://idp.internal.test/"); err != nil {
			t.Fatal(err)
		}
		if len(rec.private.requests()) != 1 {
			t.Errorf("a listed host: %d private requests, want 1", len(rec.private.requests()))
		}
	})

	t.Run("cached as keys are, refreshed in the background, dropped when stale", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		r := overFake(t, idp)
		for i := 0; i < 3; i++ {
			if _, err := r.metadata(idp.Issuer()); err != nil {
				t.Fatal(err)
			}
		}
		if d, _ := idp.Requests(); d != 1 {
			t.Fatalf("%d discovery requests for three calls, want 1", d)
		}
		r.clk.advance(refreshInterval + time.Second)
		if _, err := r.metadata(idp.Issuer()); err != nil {
			t.Errorf("a call after the refresh interval is answered from memory: %v", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for d, _ := idp.Requests(); d < 2 && time.Now().Before(deadline); d, _ = idp.Requests() {
			time.Sleep(10 * time.Millisecond)
		}
		if d, _ := idp.Requests(); d < 2 {
			t.Errorf("%d discovery requests after the refresh interval, want a refresh", d)
		}
		idp.Stop()
		r.clk.advance(maxStale + time.Hour)
		if _, err := r.metadata(idp.Issuer()); !isUnavailable(err) {
			t.Errorf("a document older than the stale limit with the provider stopped: %v, want ErrUnavailable", err)
		}
	})

	t.Run("an unreachable provider is unavailable", func(t *testing.T) {
		idp := fakeidp.Start(t, fakeidp.Options{})
		idp.Stop()
		if _, err := overFake(t, idp).metadata(idp.Issuer()); !isUnavailable(err) {
			t.Errorf("provider stopped: %v, want ErrUnavailable", err)
		}
		for name, f := range map[string]fakeidp.Fault{"500": fakeidp.Status500, "garbage": fakeidp.Garbage, "refused": fakeidp.Refuse, "oversize": fakeidp.Oversize} {
			idp := fakeidp.Start(t, fakeidp.Options{})
			idp.SetFault(f)
			if _, err := overFake(t, idp).metadata(idp.Issuer()); !isUnavailable(err) {
				t.Errorf("%s: %v, want ErrUnavailable", name, err)
			}
		}
	})
}
