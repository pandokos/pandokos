package jwks

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/url"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
)

// publicJWKs returns key sets made at run time (no key is kept anywhere): the shapes the
// parser meets, usable and not.
func publicJWKs(f *testing.F) [][]byte {
	f.Helper()
	ec256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		f.Fatal(err)
	}
	ec384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		f.Fatal(err)
	}
	ed, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		f.Fatal(err)
	}
	_ = edPriv
	rsa2048, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		f.Fatal(err)
	}
	rsa1024, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		f.Fatal(err)
	}
	enc := func(k jose.JSONWebKey) json.RawMessage {
		b, err := k.MarshalJSON()
		if err != nil {
			f.Fatal(err)
		}
		return b
	}
	set := func(members ...json.RawMessage) []byte {
		b, err := json.Marshal(map[string]any{"keys": members})
		if err != nil {
			f.Fatal(err)
		}
		return b
	}
	ecPub := jose.JSONWebKey{Key: &ec256.PublicKey, KeyID: "synthetic-ec", Algorithm: "ES256", Use: "sig"}
	out := [][]byte{
		set(enc(ecPub)),
		set(enc(jose.JSONWebKey{Key: &ec384.PublicKey, KeyID: "synthetic-ec384"}), enc(jose.JSONWebKey{Key: ed, KeyID: "synthetic-ed", Algorithm: "EdDSA"})),
		set(enc(jose.JSONWebKey{Key: &rsa2048.PublicKey, KeyID: "synthetic-rsa", Algorithm: "RS256"})),
		set(enc(jose.JSONWebKey{Key: &rsa1024.PublicKey, KeyID: "synthetic-small"})),
		set(enc(ecPub), enc(ecPub)), // the same key ID twice
		set(enc(jose.JSONWebKey{Key: ec256, KeyID: "synthetic-private"})),
		set(enc(jose.JSONWebKey{Key: []byte("synthetic-symmetric-key-0123456789"), KeyID: "synthetic-oct"})),
		set(enc(jose.JSONWebKey{Key: &ec256.PublicKey, KeyID: "synthetic-enc", Use: "enc"})),
		set(enc(jose.JSONWebKey{Key: &ec256.PublicKey, KeyID: "synthetic-mismatch", Algorithm: "ES384"})),
		set(enc(jose.JSONWebKey{Key: &ec256.PublicKey, KeyID: "synthetic-hs", Algorithm: "HS256"})),
		[]byte(`{"keys":[{"kty":"oct","k":"c3ludGhldGljLXN5bW1ldHJpYy1rZXk"},{"kty":"EC"},{"kty":"RSA","n":"AQAB","e":"AQAB"},null,7,"x"]}`),
		[]byte(`{"keys":[]}`), []byte(`{"keys":null}`), []byte(`{}`), []byte(`[]`), []byte(`null`), []byte(``),
	}
	many := make([]json.RawMessage, 0, 40)
	for i := 0; i < 40; i++ {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			f.Fatal(err)
		}
		many = append(many, enc(jose.JSONWebKey{Key: &k.PublicKey, KeyID: "synthetic-many-" + string(rune('a'+i%26)) + string(rune('a'+i/26))}))
	}
	return append(out, set(many...))
}

// FuzzParseKeySet is T-1106: parseKeySet never panics and keeps at most maxKeys keys; each is a
// public key of a type and size that fits an algorithm of the allow-list (and the one its alg
// member names), never a symmetric key, and no key ID is held twice.
func FuzzParseKeySet(f *testing.F) {
	for _, seed := range publicJWKs(f) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, doc []byte) {
		if len(doc) > 1<<20 {
			t.Skip()
		}
		keys, err := parseKeySet(doc)
		if err != nil {
			if keys != nil {
				t.Fatalf("keys and an error: %d keys, %v", len(keys), err)
			}
			if json.Valid(doc) {
				var probe struct {
					Keys []json.RawMessage `json:"keys"`
				}
				if json.Unmarshal(doc, &probe) == nil {
					t.Fatalf("a document encoding/json reads as a key set is refused: %v", err)
				}
			}
			return
		}
		if len(keys) > maxKeys {
			t.Fatalf("%d keys kept, at most %d", len(keys), maxKeys)
		}
		seen := map[string]bool{}
		for _, k := range keys {
			switch pub := k.pub.(type) {
			case *rsa.PublicKey, *ecdsa.PublicKey, ed25519.PublicKey:
			case []byte:
				t.Fatalf("a symmetric key of %d bytes was kept (kid %q)", len(pub), k.kid)
			default:
				t.Fatalf("a key of type %T was kept", k.pub)
			}
			if !fitsSomething(k.pub) {
				t.Fatalf("a key of type %T that fits no algorithm of the product was kept", k.pub)
			}
			if k.alg != "" && (!isProductAlg(k.alg) || !Fits(k.pub, k.alg)) {
				t.Fatalf("a key whose alg member %q it does not fit was kept", k.alg)
			}
			if k.kid != "" {
				if seen[k.kid] {
					t.Fatalf("the key ID %q is held twice", k.kid)
				}
				seen[k.kid] = true
			}
		}
	})
}

func fitsSomething(pub crypto.PublicKey) bool {
	for _, a := range algorithms {
		if Fits(pub, a) {
			return true
		}
	}
	return false
}

// metadataSeeds are discovery documents: the one asked for, one for another issuer, and the
// malformed ones.
func metadataSeeds() []struct {
	issuer string
	doc    []byte
} {
	const iss = "https://idp.example.test/realm"
	doc := func(m map[string]any) []byte {
		b, _ := json.Marshal(m)
		return b
	}
	good := map[string]any{"issuer": iss, "jwks_uri": iss + "/jwks.json", "authorization_endpoint": iss + "/authorize", "token_endpoint": iss + "/token"}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for key, val := range good {
			m[key] = val
		}
		m[k] = v
		return m
	}
	return []struct {
		issuer string
		doc    []byte
	}{
		{iss, doc(good)},
		{iss, doc(with("issuer", "https://other.example.test/"))},
		{iss, doc(with("issuer", iss+"/"))},
		{iss, doc(with("jwks_uri", "http://idp.example.test/jwks.json"))},
		{iss, doc(with("jwks_uri", "https://user:pw@idp.example.test/jwks.json"))},
		{iss, doc(with("jwks_uri", "https://idp.example.test/jwks.json#frag"))},
		{iss, doc(with("token_endpoint", ""))},
		{iss, doc(with("token_endpoint", nil))},
		{iss, doc(with("authorization_endpoint", 7))},
		{iss, []byte(`{"issuer":"` + iss + `"}`)},
		{iss, []byte(`{"issuer":"` + iss + `","jwks_uri":"https://idp.example.test/k"}`)},
		{iss, []byte(`{"Issuer":"` + iss + `","JWKS_URI":"https://idp.example.test/k","Authorization_Endpoint":"https://idp.example.test/a","token_ENDPOINT":"https://idp.example.test/t"}`)},
		{iss, []byte(`[]`)}, {iss, []byte(`null`)}, {iss, []byte(``)}, {iss, []byte(`{`)},
		{"", doc(good)},
		{"http://idp.example.test/", doc(with("issuer", "http://idp.example.test/"))},
	}
}

// FuzzMetadata is T-1107: parseMetadata and parseDiscovery never panic; a document is accepted only
// when its issuer member equals the issuer asked for, byte for byte, and every address it yields is
// an https URL with a host and without user information (the rule of parseHTTPS, which both
// functions apply and which allows a fragment and a query).
func FuzzMetadata(f *testing.F) {
	for _, s := range metadataSeeds() {
		f.Add(s.doc, s.issuer)
	}
	f.Fuzz(func(t *testing.T, doc []byte, issuer string) {
		if len(doc) > 1<<20 || len(issuer) > 1<<16 {
			t.Skip()
		}
		var probe struct {
			Issuer                *string `json:"issuer"`
			JWKSURI               *string `json:"jwks_uri"`
			AuthorizationEndpoint *string `json:"authorization_endpoint"`
			TokenEndpoint         *string `json:"token_endpoint"`
		}
		decodes := json.Unmarshal(doc, &probe) == nil

		md, err := parseMetadata(doc, issuer)
		if err == nil {
			if !decodes || probe.Issuer == nil || *probe.Issuer != issuer || md.Issuer != issuer {
				t.Fatalf("accepted a document whose issuer is not the one asked for: %q for %q", md.Issuer, issuer)
			}
			for name, raw := range map[string]string{"jwks_uri": md.JWKSURI, "authorization_endpoint": md.AuthorizationEndpoint, "token_endpoint": md.TokenEndpoint} {
				checkEndpoint(t, name, raw)
			}
			if md.JWKSURI != *probe.JWKSURI || md.AuthorizationEndpoint != *probe.AuthorizationEndpoint || md.TokenEndpoint != *probe.TokenEndpoint {
				t.Fatalf("an address changed on the way: %+v", md)
			}
		} else if fe := (*fetchError)(nil); !errors.As(err, &fe) {
			t.Fatalf("a failure that is not a fetchError: %T", err)
		}

		// parseDiscovery reads two members only.
		var disc struct {
			Issuer  *string `json:"issuer"`
			JWKSURI *string `json:"jwks_uri"`
		}
		discDecodes := json.Unmarshal(doc, &disc) == nil
		u, err := parseDiscovery(doc, issuer)
		if err == nil {
			if !discDecodes || disc.Issuer == nil || *disc.Issuer != issuer || disc.JWKSURI == nil || u == nil {
				t.Fatalf("accepted a discovery document whose issuer is not the one asked for (%q)", issuer)
			}
			checkEndpoint(t, "jwks_uri", u.String())
			if u.String() != mustParse(t, *disc.JWKSURI).String() {
				t.Fatalf("the address changed on the way: %q", u)
			}
		} else if fe := (*fetchError)(nil); !errors.As(err, &fe) {
			t.Fatalf("a failure that is not a fetchError: %T", err)
		}
	})
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("an accepted address does not parse: %v", err)
	}
	return u
}

// checkEndpoint holds the rule every address of a document must meet: the rule of parseHTTPS, which
// the code states and applies to each address of a document: the https scheme, a host, no user
// information, not opaque. (A fragment is not refused by the rule; the guard never sends it.)
func checkEndpoint(t *testing.T, name, raw string) {
	t.Helper()
	u, ok := parseHTTPS(raw)
	if !ok {
		t.Fatalf("%s: an accepted address that parseHTTPS refuses", name)
	}
	if u.Scheme != "https" || u.User != nil || u.Hostname() == "" || u.Opaque != "" {
		t.Fatalf("%s: an accepted address that is not an https URL with a host and no user information: %q", name, raw)
	}
}
