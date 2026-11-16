package jwks_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/pandokos/pandokos/internal/jwks"
)

const keyIssuer = "https://keys.example.test/"

// keyDoc serves a key set made of the given JWKs and the discovery document of keyIssuer.
func keyDoc(t testing.TB, keys ...any) *docs {
	t.Helper()
	body, err := json.Marshal(map[string]any{"keys": keys})
	if err != nil {
		t.Fatal(err)
	}
	return &docs{keys: body, discovery: func(string) []byte {
		return []byte(`{"issuer":"` + keyIssuer + `","jwks_uri":"https://keys.example.test/jwks.json"}`)
	}}
}

// jwk marshals a key as the member of a key set, with the given extra members set.
func jwk(t testing.TB, key any, kid string, extra map[string]any) map[string]any {
	t.Helper()
	b, err := (&jose.JSONWebKey{Key: key, KeyID: kid}).MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if kid == "" {
		delete(m, "kid")
	}
	for k, v := range extra {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	return m
}

func mustRSA(t testing.TB, bits int) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func mustEC(t testing.TB, c elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(c, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func mustEd(t testing.TB) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TestKeySelection is T-575: only usable keys are kept, a key is used only for an algorithm it
// fits, and the key of a token is the one named by its key ID, or the only candidate.
func TestKeySelection(t *testing.T) {
	src := jwks.Source{Issuer: keyIssuer}
	rsa2048, rsa1024 := mustRSA(t, 2048), mustRSA(t, 1024)
	p256, p384, p521, ed := mustEC(t, elliptic.P256()), mustEC(t, elliptic.P384()), mustEC(t, elliptic.P521()), mustEd(t)
	oct := []byte("0123456789abcdef0123456789abcdef")

	// found says what Key answers for a key set holding the keys.
	lookup := func(t *testing.T, d *docs, kid, alg string) (any, error) {
		t.Helper()
		r := newRig(t, func(bool) http.RoundTripper { return d })
		return r.key(nil, src, kid, alg)
	}
	ok := func(t *testing.T, d *docs, kid, alg string, want any) {
		t.Helper()
		got, err := lookup(t, d, kid, alg)
		if err != nil {
			t.Errorf("kid %q alg %s: %v", kid, alg, err)
			return
		}
		type equaler interface{ Equal(crypto.PublicKey) bool }
		if e, ok := got.(equaler); !ok || !e.Equal(want) {
			t.Errorf("kid %q alg %s: key %T is not the expected public key", kid, alg, got)
		}
		switch got.(type) {
		case *rsa.PublicKey, *ecdsa.PublicKey, ed25519.PublicKey:
		default:
			t.Errorf("kid %q alg %s: the key is a %T: not a public key", kid, alg, got)
		}
	}
	gone := func(t *testing.T, d *docs, kid, alg string) {
		t.Helper()
		_, err := lookup(t, d, kid, alg)
		if !errors.Is(err, jwks.ErrKeyNotFound) {
			t.Errorf("kid %q alg %s: err = %v, want ErrKeyNotFound", kid, alg, err)
		}
	}

	t.Run("each type serves its algorithms", func(t *testing.T) {
		d := keyDoc(t, jwk(t, rsa2048.Public(), "rsa", nil), jwk(t, p256.Public(), "p256", nil), jwk(t, p384.Public(), "p384", nil),
			jwk(t, p521.Public(), "p521", nil), jwk(t, ed.Public(), "ed", nil))
		for _, alg := range []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512"} {
			ok(t, d, "rsa", alg, &rsa2048.PublicKey)
		}
		ok(t, d, "p256", "ES256", &p256.PublicKey)
		ok(t, d, "p384", "ES384", &p384.PublicKey)
		ok(t, d, "p521", "ES512", &p521.PublicKey)
		ok(t, d, "ed", "EdDSA", ed.Public())
	})

	t.Run("a key is used only for an algorithm it fits", func(t *testing.T) {
		d := keyDoc(t, jwk(t, rsa2048.Public(), "rsa", nil), jwk(t, p256.Public(), "p256", nil), jwk(t, p384.Public(), "p384", nil),
			jwk(t, p521.Public(), "p521", nil), jwk(t, ed.Public(), "ed", nil))
		for _, c := range []struct{ kid, alg string }{
			{"rsa", "ES256"}, {"rsa", "EdDSA"}, {"p256", "RS256"}, {"p256", "PS256"}, {"p256", "ES384"}, {"p256", "ES512"}, {"p256", "EdDSA"},
			{"p384", "ES256"}, {"p384", "ES512"}, {"p521", "ES256"}, {"p521", "ES384"}, {"ed", "ES256"}, {"ed", "RS256"},
			{"rsa", "HS256"}, {"rsa", "none"}, {"p256", ""}, {"rsa", "rs256"},
		} {
			gone(t, d, c.kid, c.alg)
		}
	})

	t.Run("private keys, oct keys, keys for encryption, a mismatching alg and short RSA are dropped", func(t *testing.T) {
		d := keyDoc(t,
			jwk(t, p256, "private", nil),                                         // a JWK with the private member d
			jwk(t, oct, "oct", nil),                                              // a symmetric key
			jwk(t, p384.Public(), "enc", map[string]any{"use": "enc"}),           // for encryption
			jwk(t, p384.Public(), "algmismatch", map[string]any{"alg": "ES256"}), // the key is P-384, alg says ES256
			jwk(t, p256.Public(), "algother", map[string]any{"alg": "RS256"}),    // alg of another family
			jwk(t, rsa1024.Public(), "short", nil),                               // RSA of 1024 bits
			jwk(t, rsa2048.Public(), "good", map[string]any{"use": "sig"}),
		)
		for _, kid := range []string{"private", "oct", "enc", "algmismatch", "algother", "short"} {
			for _, alg := range []string{"ES256", "ES384", "RS256", "PS256", "HS256", "EdDSA"} {
				gone(t, d, kid, alg)
			}
		}
		ok(t, d, "good", "RS256", &rsa2048.PublicKey)
		// The JWK's own alg narrows the key to that algorithm.
		d = keyDoc(t, jwk(t, rsa2048.Public(), "narrow", map[string]any{"alg": "PS256"}), jwk(t, p256.Public(), "es", map[string]any{"alg": "ES256", "use": "sig"}))
		ok(t, d, "narrow", "PS256", &rsa2048.PublicKey)
		gone(t, d, "narrow", "RS256")
		ok(t, d, "es", "ES256", &p256.PublicKey)
		// use absent is accepted; key_ops are not read.
		d = keyDoc(t, jwk(t, p256.Public(), "nouse", map[string]any{"use": nil}))
		ok(t, d, "nouse", "ES256", &p256.PublicKey)
		// A key of 2048 bits is the minimum: 2047 is not offered by the generator, 3072 is fine.
		d = keyDoc(t, jwk(t, mustRSA(t, 3072).Public(), "big", nil))
		if _, err := lookup(t, d, "big", "RS256"); err != nil {
			t.Errorf("RSA of 3072 bits: %v", err)
		}
	})

	t.Run("a duplicate key ID keeps none of the keys that share it", func(t *testing.T) {
		other := mustEC(t, elliptic.P256())
		d := keyDoc(t, jwk(t, p256.Public(), "dup", nil), jwk(t, other.Public(), "dup", nil), jwk(t, p384.Public(), "single", nil))
		gone(t, d, "dup", "ES256")
		ok(t, d, "single", "ES384", &p384.PublicKey)
		// Two copies of the same key under one ID are no better.
		d = keyDoc(t, jwk(t, p256.Public(), "same", nil), jwk(t, p256.Public(), "same", nil), jwk(t, p384.Public(), "other", nil))
		gone(t, d, "same", "ES256")
		ok(t, d, "other", "ES384", &p384.PublicKey)
	})

	t.Run("at most 32 keys are kept", func(t *testing.T) {
		var set []any
		keys := make([]*ecdsa.PrivateKey, 40)
		for i := range keys {
			keys[i] = mustEC(t, elliptic.P256())
			set = append(set, jwk(t, keys[i].Public(), fmt.Sprintf("k%02d", i), nil))
		}
		d := keyDoc(t, set...)
		ok(t, d, "k00", "ES256", &keys[0].PublicKey)
		ok(t, d, "k31", "ES256", &keys[31].PublicKey)
		gone(t, d, "k32", "ES256")
		gone(t, d, "k39", "ES256")
		// Keys that are dropped do not count: 40 keys of which 20 are private leave 20.
		set = nil
		for i := 0; i < 40; i++ {
			if i%2 == 0 {
				set = append(set, jwk(t, keys[i], fmt.Sprintf("p%02d", i), nil)) // private: dropped
			} else {
				set = append(set, jwk(t, keys[i].Public(), fmt.Sprintf("p%02d", i), nil))
			}
		}
		d = keyDoc(t, set...)
		ok(t, d, "p39", "ES256", &keys[39].PublicKey)
		gone(t, d, "p38", "ES256")
	})

	t.Run("a token without a key ID", func(t *testing.T) {
		one := keyDoc(t, jwk(t, p256.Public(), "only", nil), jwk(t, rsa2048.Public(), "rsa", nil))
		ok(t, one, "", "ES256", &p256.PublicKey) // the only usable key for the algorithm
		ok(t, one, "", "RS256", &rsa2048.PublicKey)
		two := keyDoc(t, jwk(t, p256.Public(), "a", nil), jwk(t, mustEC(t, elliptic.P256()).Public(), "b", nil))
		gone(t, two, "", "ES256") // two candidates: never a guess
		// A candidate of the right type that does not fit the algorithm is no candidate.
		mixed := keyDoc(t, jwk(t, p256.Public(), "a", nil), jwk(t, p384.Public(), "b", nil))
		ok(t, mixed, "", "ES256", &p256.PublicKey)
		ok(t, mixed, "", "ES384", &p384.PublicKey)
		none := keyDoc(t, jwk(t, p256.Public(), "a", nil))
		gone(t, none, "", "RS256")
		// A key without a key ID is a candidate for a token without one.
		bare := keyDoc(t, jwk(t, p256.Public(), "", nil))
		ok(t, bare, "", "ES256", &p256.PublicKey)
	})

	t.Run("a key set that holds no usable key is a failure of class no_usable_key", func(t *testing.T) {
		d := keyDoc(t, jwk(t, oct, "oct", nil), jwk(t, p256, "private", nil))
		r := newRig(t, func(bool) http.RoundTripper { return d })
		_, err := r.key(nil, src, "oct", "ES256")
		if !isUnavailable(err) {
			t.Errorf("err = %v, want ErrUnavailable: a key set without keys is not a verdict on the token", err)
		}
		if got := r.failureClasses(t); len(got) != 1 || got[0] != "no_usable_key" {
			t.Errorf("failure classes %v, want [no_usable_key]", got)
		}
		d = keyDoc(t)
		r = newRig(t, func(bool) http.RoundTripper { return d })
		if _, err := r.key(nil, src, "k", "ES256"); !isUnavailable(err) {
			t.Errorf("an empty key set: %v", err)
		}
	})
}
