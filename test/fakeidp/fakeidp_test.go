package fakeidp_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/fakeidp"
)

type jwk struct {
	Kty, Kid, Use, Alg, Crv, N, E, X, Y, D, K string
}

func get(t *testing.T, s *fakeidp.Server, url string) (int, []byte) {
	t.Helper()
	c := &http.Client{Transport: s.Transport(), Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func keys(t *testing.T, s *fakeidp.Server) map[string]jwk {
	t.Helper()
	status, body := get(t, s, s.JWKSURI())
	if status != 200 {
		t.Fatalf("key set: %d", status)
	}
	var set struct{ Keys []jwk }
	if err := json.Unmarshal(body, &set); err != nil {
		t.Fatal(err)
	}
	out := map[string]jwk{}
	for _, k := range set.Keys {
		out[k.Kid] = k
	}
	return out
}

func dec(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// verify checks the signature of a compact token with the standard library and the key of the key set.
func verify(t *testing.T, set map[string]jwk, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token with %d segments", len(parts))
	}
	var h struct{ Alg, Kid, Typ string }
	if err := json.Unmarshal(dec(t, parts[0]), &h); err != nil {
		t.Fatal(err)
	}
	k, ok := set[h.Kid]
	if !ok {
		t.Fatalf("the key set has no key %q", h.Kid)
	}
	in, sig := []byte(parts[0]+"."+parts[1]), dec(t, parts[2])
	var hash crypto.Hash
	switch {
	case strings.HasSuffix(h.Alg, "256"):
		hash = crypto.SHA256
	case strings.HasSuffix(h.Alg, "384"):
		hash = crypto.SHA384
	case strings.HasSuffix(h.Alg, "512"):
		hash = crypto.SHA512
	}
	var digest []byte
	switch hash {
	case crypto.SHA256:
		d := sha256.Sum256(in)
		digest = d[:]
	case crypto.SHA384:
		d := sha512.Sum384(in)
		digest = d[:]
	case crypto.SHA512:
		d := sha512.Sum512(in)
		digest = d[:]
	}
	switch k.Kty {
	case "RSA":
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(dec(t, k.N)), E: int(new(big.Int).SetBytes(dec(t, k.E)).Int64())}
		var err error
		if strings.HasPrefix(h.Alg, "PS") {
			err = rsa.VerifyPSS(pub, hash, digest, sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		} else {
			err = rsa.VerifyPKCS1v15(pub, hash, digest, sig)
		}
		if err != nil {
			t.Errorf("%s: %v", h.Alg, err)
		}
	case "EC":
		var curve elliptic.Curve
		switch k.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		}
		pub := &ecdsa.PublicKey{Curve: curve, X: new(big.Int).SetBytes(dec(t, k.X)), Y: new(big.Int).SetBytes(dec(t, k.Y))}
		size := (curve.Params().BitSize + 7) / 8
		if len(sig) != 2*size || !ecdsa.Verify(pub, digest, new(big.Int).SetBytes(sig[:size]), new(big.Int).SetBytes(sig[size:])) {
			t.Errorf("%s: the signature does not verify", h.Alg)
		}
	case "OKP":
		if !ed25519.Verify(ed25519.PublicKey(dec(t, k.X)), in, sig) {
			t.Errorf("%s: the signature does not verify", h.Alg)
		}
	default:
		t.Fatalf("key type %q", k.Kty)
	}
	var claims map[string]any
	if err := json.Unmarshal(dec(t, parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestSignedTokensVerifyWithTheStandardLibrary(t *testing.T) {
	s := fakeidp.Start(t, fakeidp.Options{})
	set := keys(t, s)
	for _, alg := range fakeidp.Algorithms {
		t.Run(alg, func(t *testing.T) {
			c := s.Standard("workload", "pandokos", time.Minute)
			claims := verify(t, set, s.Sign(alg, c))
			if claims["sub"] != "workload" || claims["iss"] != s.Issuer() {
				t.Errorf("claims %v", claims)
			}
			// Raw with the standard library signer gives the same.
			raw := s.Raw(map[string]any{"alg": alg, "kid": s.KeyID(alg)}, c, s.SignWith(alg))
			verify(t, set, raw)
		})
	}
}

func TestDocuments(t *testing.T) {
	s := fakeidp.Start(t, fakeidp.Options{Path: "/realms/acme"})
	if s.Issuer() != "https://"+s.Host()+"/realms/acme" {
		t.Errorf("issuer %q", s.Issuer())
	}
	status, body := get(t, s, s.DiscoveryURL())
	var doc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if status != 200 || json.Unmarshal(body, &doc) != nil || doc.Issuer != s.Issuer() || doc.JWKSURI != s.JWKSURI() {
		t.Errorf("discovery: %d %s", status, body)
	}
	if d, j := s.Requests(); d != 1 || j != 0 {
		t.Errorf("requests %d %d", d, j)
	}
	if status, _ := get(t, s, "https://"+s.Host()+"/other"); status != 404 {
		t.Errorf("an unknown path: %d", status)
	}
	only := fakeidp.Start(t, fakeidp.Options{NoDiscovery: true})
	if status, _ := get(t, only, only.DiscoveryURL()); status != 404 {
		t.Errorf("discovery of a key-set-only server: %d", status)
	}
	if len(keys(t, only)) != 5 {
		t.Errorf("keys: %d", len(keys(t, only)))
	}
}

func TestRotateAndWeakKeys(t *testing.T) {
	s := fakeidp.Start(t, fakeidp.Options{})
	before := keys(t, s)
	old := s.Sign("RS256", s.Standard("a", "b", time.Minute))
	s.Rotate()
	after := keys(t, s)
	for kid := range before {
		if _, ok := after[kid]; ok {
			t.Errorf("key %s survived the rotation", kid)
		}
	}
	parts := strings.Split(old, ".")
	var h struct{ Kid string }
	_ = json.Unmarshal(dec(t, parts[0]), &h)
	if _, ok := after[h.Kid]; ok {
		t.Error("the old key ID is still served")
	}
	if len(after) != 5 {
		t.Errorf("%d keys", len(after))
	}
	s.WithWeakKeys(true)
	weak := keys(t, s)
	if len(weak) != 7 || weak[s.WeakKeyID()].Kty != "RSA" || weak[s.OctKeyID()].Kty != "oct" {
		t.Errorf("weak keys: %+v", weak)
	}
	// The weak RSA key is 1024 bits, and a token signed with it verifies.
	if n := len(dec(t, weak[s.WeakKeyID()].N)); n != 128 {
		t.Errorf("the weak modulus has %d bytes", n)
	}
	verify(t, weak, s.Sign("RS256", s.Standard("a", "b", time.Minute), fakeidp.WithWeakKey()))
	s.Rotate()
	if len(keys(t, s)) != 7 {
		t.Error("the weak keys did not survive the rotation")
	}
}

func TestFaults(t *testing.T) {
	s := fakeidp.Start(t, fakeidp.Options{})
	c := &http.Client{Transport: s.Transport(), Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(url string) (int, int, error) {
		resp, err := c.Get(url)
		if err != nil {
			return 0, 0, err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return resp.StatusCode, len(b), err
	}
	s.SetFault(fakeidp.Status500)
	if st, _, _ := do(s.JWKSURI()); st != 500 {
		t.Errorf("Status500: %d", st)
	}
	s.SetFault(fakeidp.Refuse)
	if _, _, err := do(s.JWKSURI()); err == nil {
		t.Error("Refuse: no error")
	}
	s.SetFault(fakeidp.Redirect("https://example.test/"))
	if st, _, _ := do(s.JWKSURI()); st != 302 {
		t.Errorf("Redirect: %d", st)
	}
	s.SetFault(fakeidp.Padded(5000))
	if st, n, _ := do(s.JWKSURI()); st != 200 || n != 5000 {
		t.Errorf("Padded: %d %d", st, n)
	}
	s.SetFault(fakeidp.Oversize)
	if st, n, _ := do(s.JWKSURI()); st != 200 || n != 1<<20 {
		t.Errorf("Oversize: %d %d", st, n)
	}
	s.SetFault(fakeidp.WrongIssuer)
	_, body := get(t, s, s.DiscoveryURL())
	if strings.Contains(string(body), s.Issuer()+`"`) {
		t.Errorf("WrongIssuer: %s", body)
	}
	s.SetFault(fakeidp.None)
	if st, _, _ := do(s.JWKSURI()); st != 200 {
		t.Errorf("None: %d", st)
	}
	// Stop refuses, Resume serves the same keys again.
	kid := s.KeyID("ES256")
	s.Stop()
	if _, _, err := do(s.JWKSURI()); err == nil {
		t.Error("a stopped server answered")
	}
	s.Resume()
	if _, ok := keys(t, s)[kid]; !ok {
		t.Error("the key is gone after Resume")
	}
	if d, j := s.Requests(); d < 1 || j < 5 {
		t.Errorf("requests %d %d", d, j)
	}
}
