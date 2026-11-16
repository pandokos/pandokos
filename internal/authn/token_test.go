package authn

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/jwks"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/fakeidp"
)

// TestTokenValid is T-569: for each of the ten algorithms a token signed by the fake issuer yields
// its verified claims, with and without a key ID, with the audience as a string and as an array.
func TestTokenValid(t *testing.T) {
	r := newTokenRig(t)
	for _, alg := range fakeidp.Algorithms {
		for _, withKID := range []bool{true, false} {
			for _, aud := range []any{testAudience, []string{"someone-else", testAudience}} {
				name := fmt.Sprintf("%s kid=%v aud=%T", alg, withKID, aud)
				c := r.claims("workload-7").With("aud", aud)
				var opts []fakeidp.SignOption
				if !withKID {
					opts = append(opts, fakeidp.WithoutKID())
				}
				tok := r.idp.Sign(alg, c, opts...)
				v, err := r.run(tok)
				v = mustAccept(t, name, v, err)
				if v.Subject != "workload-7" {
					t.Errorf("%s: subject %q", name, v.Subject)
				}
				if v.TenantClaimValue != "" {
					t.Errorf("%s: a tenant-bound registration yielded the tenant claim %q", name, v.TenantClaimValue)
				}
			}
		}
	}
	// The key source is asked for the registration's source and the header's key ID and algorithm.
	r.keys.calls = nil
	tok := r.idp.Sign("PS384", r.claims("x"))
	if _, err := r.run(tok); err != nil {
		t.Fatal(err)
	}
	calls := r.keys.callLog()
	if len(calls) != 1 || calls[0].Kid != r.idp.KeyID("PS384") || calls[0].Alg != "PS384" || calls[0].Src.Issuer != r.reg.Issuer {
		t.Errorf("key source calls: %+v", calls)
	}
	// The subject claim of a registration and the claims it requires.
	reg := r.reg
	reg.SubjectClaim = "client_id"
	reg.RequiredClaims = map[string]string{"tier": "gold"}
	v, err := r.runAgainst(r.idp.Sign("ES256", r.claims("ignored").With("client_id", "svc-a").With("tier", []string{"silver", "gold"})), reg)
	if v = mustAccept(t, "subject claim and required claim", v, err); v.Subject != "svc-a" {
		t.Errorf("subject %q, want svc-a", v.Subject)
	}
	// Platform-level: the tenant claim is returned, unverified against any list here.
	reg = r.reg
	reg.Scope, reg.TenantClaim = "platform", "org"
	v, err = r.runAgainst(r.idp.Sign("ES256", r.claims("w").With("org", "acme-org")), reg)
	if v = mustAccept(t, "platform tenant claim", v, err); v.TenantClaimValue != "acme-org" {
		t.Errorf("tenant claim value %q", v.TenantClaimValue)
	}
}

// pieces splits a compact token.
func pieces(t testing.TB, tok string) (h, p, s string) {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token with %d segments", len(parts))
	}
	return parts[0], parts[1], parts[2]
}

func b64s(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// TestTokenMalformed is T-570: what is not a well-formed token is refused, whatever the signature.
func TestTokenMalformed(t *testing.T) {
	r := newTokenRig(t)
	good := r.idp.Sign("ES256", r.claims("w"))
	h, p, s := pieces(t, good)
	sign := r.idp.SignWith("ES256")
	kid := r.idp.KeyID("ES256")
	hdr := `{"alg":"ES256","kid":"` + kid + `","typ":"JWT"}`
	payload := fmt.Sprintf(`{"iss":%q,"sub":"w","aud":%q,"iat":%d,"exp":%d}`, r.idp.Issuer(), testAudience, tokenNow.Unix(), tokenNow.Add(time.Minute).Unix())
	signed := func(header, body string) string {
		return fakeidp.RawJSON([]byte(header), []byte(body), sign)
	}
	if _, err := r.run(signed(hdr, payload)); err != nil {
		t.Fatalf("the control token is refused: %v", err)
	}

	type row struct {
		name  string
		token string
	}
	rows := []row{
		{"a header that is not JSON", b64s("not json") + "." + p + "." + s},
		{"a payload that is not JSON", h + "." + b64s("not json") + "." + s},
		{"a header that is an array", b64s(`["ES256"]`) + "." + p + "." + s},
		{"a payload that is an array", h + "." + b64s(`[1,2,3]`) + "." + s},
		{"a header that is a string", b64s(`"ES256"`) + "." + p + "." + s},
		{"a payload that is null", h + "." + b64s(`null`) + "." + s},
		{"a header with trailing data", b64s(hdr+` {}`) + "." + p + "." + s},
		{"a payload with trailing data", h + "." + b64s(payload+`x`) + "." + s},
		{"an empty header", b64s(``) + "." + p + "." + s},
		{"a header that is not base64url", "!!!!." + p + "." + s},
		{"padding in the header", h + "==." + p + "." + s},
		{"the standard alphabet", strings.ReplaceAll(h, "-", "+") + "+/." + p + "." + s},
		{"two segments", h + "." + p},
		{"four segments", good + ".AAAA"},
		{"an empty payload segment", h + ".." + s},
		{"a duplicate member in the header", signed(`{"alg":"ES256","kid":"`+kid+`","kid":"`+kid+`"}`, payload)},
		{"a duplicate algorithm in the header", signed(`{"alg":"ES256","alg":"ES256","kid":"`+kid+`"}`, payload)},
		{"a duplicate algorithm, the first one allowed", signed(`{"alg":"ES256","alg":"none","kid":"`+kid+`"}`, payload)},
		{"a duplicate member in the payload", signed(hdr, strings.Replace(payload, `"sub":"w"`, `"sub":"w","sub":"w"`, 1))},
		{"a duplicate subject, the second one an attacker's", signed(hdr, strings.Replace(payload, `"sub":"w"`, `"sub":"w","sub":"admin"`, 1))},
		{"a duplicate audience", signed(hdr, strings.Replace(payload, `"aud":"`+testAudience+`"`, `"aud":"`+testAudience+`","aud":"x"`, 1))},
		{"a duplicate expiry", signed(hdr, strings.Replace(payload, fmt.Sprintf(`"exp":%d`, tokenNow.Add(time.Minute).Unix()), fmt.Sprintf(`"exp":%d,"exp":99999999999`, tokenNow.Add(time.Minute).Unix()), 1))},
		{"an audience of 17 entries", signed(hdr, fmt.Sprintf(`{"iss":%q,"sub":"w","aud":[%s],"iat":%d,"exp":%d}`, r.idp.Issuer(),
			`"`+testAudience+`"`+strings.Repeat(`,"x"`, 16), tokenNow.Unix(), tokenNow.Add(time.Minute).Unix()))},
		{"an audience that holds a number", signed(hdr, fmt.Sprintf(`{"iss":%q,"sub":"w","aud":["%s",7],"exp":%d}`, r.idp.Issuer(), testAudience, tokenNow.Add(time.Minute).Unix()))},
		{"an issuer that is a number", signed(hdr, fmt.Sprintf(`{"iss":7,"sub":"w","aud":%q,"exp":%d}`, testAudience, tokenNow.Add(time.Minute).Unix()))},
		{"an issuer that is an array", signed(hdr, fmt.Sprintf(`{"iss":[%q],"sub":"w","aud":%q,"exp":%d}`, r.idp.Issuer(), testAudience, tokenNow.Add(time.Minute).Unix()))},
	}
	for _, c := range rows {
		_, err := r.run(c.token)
		f := refusal(t, c.name, err)
		if f.Reason != "malformed" && f.Reason != "algorithm_not_allowed" {
			t.Errorf("%s: reason %q, want malformed", c.name, f.Reason)
		}
		if f.Subject != "" {
			t.Errorf("%s: the failure carries the subject %q of an unverified token", c.name, f.Subject)
		}
	}
	// The reason of the rows that are about the structure is malformed itself.
	for _, c := range rows[:12] {
		if _, err := r.run(c.token); err != nil {
			if f := refusal(t, c.name, err); f.Reason != "malformed" {
				t.Errorf("%s: reason %q, want malformed", c.name, f.Reason)
			}
		}
	}

	// Numbers as strings: a failure of the check of that claim (section 5.4, last paragraph).
	str := func(claim, value string) string {
		c := fmt.Sprintf(`{"iss":%q,"sub":"w","aud":%q,"iat":%d,"nbf":%d,"exp":%d}`, r.idp.Issuer(), testAudience, tokenNow.Unix(), tokenNow.Unix(), tokenNow.Add(time.Minute).Unix())
		var m map[string]any
		_ = json.Unmarshal([]byte(c), &m)
		m[claim] = value
		b, _ := json.Marshal(m)
		return signed(hdr, string(b))
	}
	for claim, reason := range map[string]string{"exp": "expired", "nbf": "not_yet_valid", "iat": "not_yet_valid"} {
		_, err := r.run(str(claim, fmt.Sprint(tokenNow.Add(time.Minute).Unix())))
		_ = wantReason(t, claim+" as a string", err, reason, "malformed")
	}
	// Other types for the time claims.
	for _, v := range []string{`true`, `null`, `[1]`, `{"a":1}`} {
		body := fmt.Sprintf(`{"iss":%q,"sub":"w","aud":%q,"exp":%s}`, r.idp.Issuer(), testAudience, v)
		_, err := r.run(signed(hdr, body))
		_ = wantReason(t, "exp = "+v, err, "expired", "malformed")
	}
	// Time claims may have a fractional part.
	frac := fmt.Sprintf(`{"iss":%q,"sub":"w","aud":%q,"iat":%d.25,"exp":%d.75}`, r.idp.Issuer(), testAudience, tokenNow.Unix(), tokenNow.Add(time.Minute).Unix())
	if _, err := r.run(signed(hdr, frac)); err != nil {
		t.Errorf("fractional time claims: %v", err)
	}
	// A number far outside the range is a failure of its check, not a panic or an overflow.
	for _, v := range []string{`1e400`, `-1e400`, `99999999999999999999999999`, `1e13`, `-1e13`} {
		body := fmt.Sprintf(`{"iss":%q,"sub":"w","aud":%q,"exp":%s}`, r.idp.Issuer(), testAudience, v)
		if _, err := r.run(signed(hdr, body)); err == nil {
			t.Errorf("exp = %s was accepted", v)
		}
	}
	// A very large token is the shape's business (T-568), but a deep or huge payload is not a crash.
	deep := strings.Repeat(`{"a":`, 5000) + `1` + strings.Repeat(`}`, 5000)
	_, err := r.run(signed(hdr, `{"iss":"x","x":`+deep+`}`))
	_ = refusal(t, "a deeply nested payload", err)
}

// TestTokenAlgorithmAttacks is T-571: every way of making a verifier use the wrong algorithm or the
// wrong key is refused, and nothing in the header is read as key material.
func TestTokenAlgorithmAttacks(t *testing.T) {
	r := newTokenRig(t)
	other := fakeidp.Start(t, fakeidp.Options{}) // an issuer of the attacker's
	pay := r.claims("workload")
	kidOf := r.idp.KeyID

	t.Run("none, in every letter case, with an empty and a non-empty signature", func(t *testing.T) {
		for _, alg := range []string{"none", "None", "NONE", "nOnE", "noNE", "none "} {
			for _, sig := range []func([]byte) []byte{nil, func([]byte) []byte { return []byte("signature") }} {
				tok := r.idp.Raw(map[string]any{"alg": alg, "typ": "JWT"}, pay, sig)
				if sig == nil {
					// An empty signature has an empty last segment: three segments, the last empty.
					if !strings.HasSuffix(tok, ".") {
						t.Fatalf("token %q", tok)
					}
				}
				_, err := r.run(tok)
				_ = wantReason(t, fmt.Sprintf("alg %q", alg), err, "algorithm_not_allowed", "malformed")
			}
		}
	})

	t.Run("HMAC keyed with the public key", func(t *testing.T) {
		for _, key := range []string{"ES256", "RS256", "EdDSA"} {
			for _, secret := range [][]byte{r.idp.PublicKeyPEM(key), r.idp.PublicKeyDER(key), []byte(r.idp.Issuer())} {
				for alg, sign := range map[string]func([]byte) []byte{"HS256": fakeidp.HS256(secret), "HS512": fakeidp.HS512(secret)} {
					tok := r.idp.Raw(map[string]any{"alg": alg, "kid": kidOf(key), "typ": "JWT"}, pay, sign)
					_, err := r.run(tok)
					_ = wantReason(t, alg+" keyed with the "+key+" key", err, "algorithm_not_allowed")
				}
			}
		}
		// Also when the registration lists it: the list of the product has no HMAC, whatever is stored.
		reg := r.reg
		reg.Algorithms = []string{"HS256", "ES256"}
		tok := r.idp.Raw(map[string]any{"alg": "HS256", "kid": kidOf("ES256")}, pay, fakeidp.HS256(r.idp.PublicKeyDER("ES256")))
		_, err := r.runAgainst(tok, reg)
		_ = wantReason(t, "HS256 listed by the registration", err, "algorithm_not_allowed")
	})

	t.Run("a key of another type than the algorithm", func(t *testing.T) {
		rsaTok := r.idp.Sign("RS256", pay)
		ecTok := r.idp.Sign("ES256", pay)
		edTok := r.idp.Sign("EdDSA", pay)
		for name, c := range map[string]struct {
			tok string
			key any
		}{
			"RS256 token, EC key":      {rsaTok, r.idp.PublicKey("ES256")},
			"RS256 token, Ed25519 key": {rsaTok, r.idp.PublicKey("EdDSA")},
			"ES256 token, RSA key":     {ecTok, r.idp.PublicKey("RS256")},
			"ES256 token, Ed25519 key": {ecTok, r.idp.PublicKey("EdDSA")},
			"EdDSA token, RSA key":     {edTok, r.idp.PublicKey("RS256")},
			"EdDSA token, EC key":      {edTok, r.idp.PublicKey("ES256")},
			"ES256 token, P-384 key":   {ecTok, r.idp.PublicKey("ES384")},
			"ES256 token, P-521 key":   {ecTok, r.idp.PublicKey("ES512")},
		} {
			// A source that hands out whatever it holds, as a buggy key cache would.
			a := *r.a
			a.tok.Keys = &forced{c.key}
			alg, kid, _, _, err := peek(c.tok)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			_, err = a.validate(t.Context(), c.tok, alg, kid, r.reg)
			_ = wantReason(t, name, err, "unknown_signing_key", "bad_signature")
		}
	})

	t.Run("a token of a curve that is not the algorithm's", func(t *testing.T) {
		// Signed with a P-384 key under the header ES256, the P-384 key served.
		k384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		tok := r.idp.Raw(map[string]any{"alg": "ES256", "kid": "k"}, pay, fakeidp.SignWithKey(t, "ES256", k384))
		a := *r.a
		a.tok.Keys = &forced{&k384.PublicKey}
		alg, kid, _, _, err := peek(tok)
		if err != nil {
			t.Fatal(err)
		}
		_, err = a.validate(t.Context(), tok, alg, kid, r.reg)
		_ = wantReason(t, "ES256 with a P-384 key", err, "unknown_signing_key", "bad_signature")
	})

	t.Run("RSA of 1024 bits", func(t *testing.T) {
		weak, err := rsa.GenerateKey(rand.Reader, 1024)
		if err != nil {
			t.Fatal(err)
		}
		tok := r.idp.Raw(map[string]any{"alg": "RS256", "kid": "weak"}, pay, fakeidp.SignWithKey(t, "RS256", weak))
		a := *r.a
		a.tok.Keys = &forced{&weak.PublicKey}
		alg, kid, _, _, err := peek(tok)
		if err != nil {
			t.Fatal(err)
		}
		_, err = a.validate(t.Context(), tok, alg, kid, r.reg)
		_ = wantReason(t, "a 1024-bit key", err, "unknown_signing_key", "bad_signature")
		// And through the source that does its job: the weak key of the issuer is no key at all.
		r.idp.WithWeakKeys(true)
		_, err = r.run(r.idp.Sign("RS256", pay, fakeidp.WithWeakKey()))
		_ = wantReason(t, "the weak key of the issuer", err, "unknown_signing_key", "bad_signature")
	})

	t.Run("an algorithm that is absent, not a string, or not allowed", func(t *testing.T) {
		sign := r.idp.SignWith("ES256")
		for name, h := range map[string]map[string]any{
			"absent":         {"kid": kidOf("ES256")},
			"a number":       {"alg": 256, "kid": kidOf("ES256")},
			"null":           {"alg": nil, "kid": kidOf("ES256")},
			"an array":       {"alg": []string{"ES256"}, "kid": kidOf("ES256")},
			"an object":      {"alg": map[string]any{"a": "ES256"}, "kid": kidOf("ES256")},
			"empty":          {"alg": "", "kid": kidOf("ES256")},
			"lower case":     {"alg": "es256", "kid": kidOf("ES256")},
			"RS1":            {"alg": "RS1", "kid": kidOf("ES256")},
			"ES256K":         {"alg": "ES256K", "kid": kidOf("ES256")},
			"RSA1_5 (JWE)":   {"alg": "RSA1_5", "kid": kidOf("ES256")},
			"A128KW (JWE)":   {"alg": "A128KW", "kid": kidOf("ES256")},
			"dir (JWE)":      {"alg": "dir", "kid": kidOf("ES256")},
			"an unknown one": {"alg": "XS256", "kid": kidOf("ES256")},
		} {
			_, err := r.run(r.idp.Raw(h, pay, sign))
			_ = wantReason(t, "alg "+name, err, "algorithm_not_allowed", "malformed")
		}
		// An algorithm of the product's list that this registration does not list (check 4).
		reg := r.reg
		reg.Algorithms = []string{"RS256"}
		_, err := r.runAgainst(r.idp.Sign("ES256", pay), reg)
		_ = wantReason(t, "ES256 against a registration that lists RS256", err, "algorithm_not_allowed")
		if _, err := r.runAgainst(r.idp.Sign("RS256", pay), reg); err != nil {
			t.Errorf("RS256 against a registration that lists RS256: %v", err)
		}
		// The default of a registration, RS256 and ES256, does not admit EdDSA or PS256.
		reg.Algorithms = []string{"RS256", "ES256"}
		for _, alg := range []string{"EdDSA", "PS256", "ES384"} {
			_, err := r.runAgainst(r.idp.Sign(alg, pay), reg)
			_ = wantReason(t, alg+" against the default list", err, "algorithm_not_allowed")
		}
	})

	t.Run("crit", func(t *testing.T) {
		for name, crit := range map[string]any{"a name": []string{"exp"}, "empty": []string{}, "a string": "exp", "a made up one": []string{"x-secure"}} {
			tok := r.idp.Raw(map[string]any{"alg": "ES256", "kid": kidOf("ES256"), "crit": crit, "exp": 1}, pay, r.idp.SignWith("ES256"))
			_, err := r.run(tok)
			_ = wantReason(t, "crit "+name, err, "algorithm_not_allowed", "malformed")
		}
	})

	t.Run("key material in the header is never read", func(t *testing.T) {
		attackerKey := other.PublicKey("ES256")
		x5c := base64.StdEncoding.EncodeToString(other.PublicKeyDER("ES256"))
		jwk := jose.JSONWebKey{Key: attackerKey, KeyID: "attacker"}
		jwkJSON, err := jwk.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		var jwkMap map[string]any
		_ = json.Unmarshal(jwkJSON, &jwkMap)
		for name, extra := range map[string]map[string]any{
			"jwk": {"jwk": jwkMap},
			"jku": {"jku": "https://attacker.example.test/keys.json"},
			"x5u": {"x5u": "https://attacker.example.test/cert.pem"},
			"x5c": {"x5c": []string{x5c}},
			"all": {"jwk": jwkMap, "jku": "https://attacker.example.test/keys.json", "x5u": "https://attacker.example.test/cert.pem", "x5c": []string{x5c}},
		} {
			for _, withKID := range []bool{true, false} {
				h := map[string]any{"alg": "ES256", "typ": "JWT"}
				if withKID {
					h["kid"] = kidOf("ES256") // the real key's ID, the attacker's signature
				}
				for k, v := range extra {
					h[k] = v
				}
				r.keys.calls = nil
				tok := r.idp.Raw(h, pay, other.SignWith("ES256"))
				_, err := r.run(tok)
				_ = wantReason(t, fmt.Sprintf("%s, kid=%v, signed by the attacker", name, withKID), err, "bad_signature", "unknown_signing_key", "algorithm_not_allowed", "malformed")
				for _, c := range r.keys.callLog() {
					if c.Src.Issuer != r.reg.Issuer || c.Src.JWKSURI != r.reg.JWKSURI {
						t.Errorf("%s: the key source was asked about %+v", name, c.Src)
					}
				}
			}
		}
		// The attacker's token carries its own key ID, which this issuer does not have.
		tok := other.Sign("ES256", pay)
		_, err = r.run(tok)
		_ = wantReason(t, "a kid of another source", err, "unknown_signing_key")
		// The same token against the right key set, for the control.
		if _, err := r.run(r.idp.Sign("ES256", pay)); err != nil {
			t.Errorf("control: %v", err)
		}
	})

	t.Run("the signature must be the one of the key", func(t *testing.T) {
		good := r.idp.Sign("RS256", pay)
		h, p, s := pieces(t, good)
		sig, _ := base64.RawURLEncoding.DecodeString(s)
		flipped := append([]byte(nil), sig...)
		flipped[0] ^= 0x01
		for name, tok := range map[string]string{
			"one bit of the signature": h + "." + p + "." + base64.RawURLEncoding.EncodeToString(flipped),
			"a short signature":        h + "." + p + "." + base64.RawURLEncoding.EncodeToString(sig[:len(sig)-1]),
			"another payload":          h + "." + b64s(strings.Replace(string(mustDecode(t, p)), `"workload"`, `"admin"`, 1)) + "." + s,
			"another header":           b64s(strings.Replace(string(mustDecode(t, h)), `"JWT"`, `"JWT" `, 1)) + "." + p + "." + s,
		} {
			_, err := r.run(tok)
			_ = wantReason(t, name, err, "bad_signature", "malformed")
		}
	})
}

func mustDecode(t testing.TB, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// forced is a key source that hands out one key whatever it is asked.
type forced struct{ key any }

func (f *forced) Key(_ context.Context, _ jwks.Source, _, _ string) (crypto.PublicKey, error) {
	return f.key, nil
}

// TestTokenClaims is T-572: each row of checks 7 to 12 at its boundary, with a fixed clock.
func TestTokenClaims(t *testing.T) {
	r := newTokenRig(t)
	now := tokenNow.Unix()
	skew := int64(testSkew / time.Second)
	life := int64(testMaxLifetime / time.Second)

	// accept and refuse sign the claims with ES256 and run them.
	try := func(_ string, c fakeidp.Claims, reg registration) (verified, error) {
		return r.runAgainst(r.idp.Sign("ES256", c), reg)
	}
	base := func() fakeidp.Claims { return r.claims("w") }
	ok := func(name string, c fakeidp.Claims) {
		t.Helper()
		if _, err := try(name, c, r.reg); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
	bad := func(name string, c fakeidp.Claims, reason string) {
		t.Helper()
		_, err := try(name, c, r.reg)
		_ = wantReason(t, name, err, reason)
	}

	t.Run("expiry", func(_ *testing.T) {
		ok("exp one second inside the skew", base().With("exp", now-skew+1))
		bad("exp at now minus the skew", base().With("exp", now-skew), "expired")
		bad("exp long ago", base().With("exp", now-86400), "expired")
		bad("exp missing", base().Without("exp"), "expired")
		ok("exp in the far future with no iat", base().Without("iat").With("exp", now+life))
	})
	t.Run("not before", func(_ *testing.T) {
		ok("nbf at now plus the skew", base().With("nbf", now+skew))
		bad("nbf one second beyond the skew", base().With("nbf", now+skew+1), "not_yet_valid")
		ok("nbf in the past", base().With("nbf", now-3600))
		ok("nbf absent", base().Without("nbf"))
	})
	t.Run("issued at", func(_ *testing.T) {
		ok("iat at now plus the skew", base().With("iat", now+skew).With("exp", now+skew+600))
		bad("iat one second beyond the skew", base().With("iat", now+skew+1).With("exp", now+skew+601), "not_yet_valid")
		ok("iat absent", base().Without("iat"))
	})
	t.Run("lifetime", func(_ *testing.T) {
		ok("exp minus iat at the maximum", base().With("iat", now-60).With("exp", now-60+life))
		bad("exp minus iat one second over", base().With("iat", now-60).With("exp", now-60+life+1), "lifetime_too_long")
		ok("without iat: exp minus now at the maximum", base().Without("iat").With("exp", now+life))
		bad("without iat: exp minus now one second over", base().Without("iat").With("exp", now+life+1), "lifetime_too_long")
		bad("a long lifetime that started long ago", base().With("iat", now-10*life).With("exp", now+60), "lifetime_too_long")
	})
	t.Run("audience", func(_ *testing.T) {
		bad("aud missing", base().Without("aud"), "wrong_audience")
		bad("aud different", base().With("aud", "somebody-else"), "wrong_audience")
		bad("aud a prefix of the registered one", base().With("aud", testAudience[:len(testAudience)-1]), "wrong_audience")
		bad("aud the registered one and more", base().With("aud", testAudience+"-2"), "wrong_audience")
		bad("aud in another case", base().With("aud", strings.ToUpper(testAudience)), "wrong_audience")
		bad("aud with a trailing space", base().With("aud", testAudience+" "), "wrong_audience")
		bad("aud an empty array", base().With("aud", []string{}), "wrong_audience")
		ok("aud among several", base().With("aud", []string{"a", testAudience, "c"}))
		ok("aud the only entry of an array", base().With("aud", []string{testAudience}))
	})
	t.Run("issuer", func(_ *testing.T) {
		iss := r.idp.Issuer()
		bad("iss with a trailing slash added", base().With("iss", iss+"/"), "issuer_mismatch")
		bad("iss with the trailing slash removed", base().With("iss", strings.TrimSuffix(iss, "/")), "issuer_mismatch")
		bad("iss in upper case", base().With("iss", strings.ToUpper(iss)), "issuer_mismatch")
		bad("iss another issuer", base().With("iss", "https://other.example.test/"), "issuer_mismatch")
		bad("iss missing", base().Without("iss"), "issuer_mismatch")
	})
	t.Run("required claims", func(t *testing.T) {
		reg := r.reg
		reg.RequiredClaims = map[string]string{"tier": "gold", "env": "prod"}
		pass := base().With("tier", "gold").With("env", "prod")
		if _, err := try("all present", pass, reg); err != nil {
			t.Errorf("all present: %v", err)
		}
		if _, err := try("one in an array", pass.With("tier", []string{"silver", "gold"}), reg); err != nil {
			t.Errorf("one in an array: %v", err)
		}
		for name, c := range map[string]fakeidp.Claims{
			"missing":                base().With("env", "prod"),
			"another value":          pass.With("tier", "silver"),
			"a prefix of the value":  pass.With("tier", "gol"),
			"another case":           pass.With("tier", "GOLD"),
			"an array without it":    pass.With("tier", []string{"silver", "bronze"}),
			"an empty array":         pass.With("tier", []string{}),
			"a number":               pass.With("tier", 1),
			"a boolean":              pass.With("tier", true),
			"an object":              pass.With("tier", map[string]any{"gold": true}),
			"null":                   pass.With("tier", nil),
			"an array with a number": pass.With("tier", []any{1, 2}),
		} {
			_, err := try(name, c, reg)
			_ = wantReason(t, "required claim "+name, err, "required_claim_missing")
		}
	})
	t.Run("subject", func(t *testing.T) {
		long := strings.Repeat("x", 255)
		ok("a subject of 255 characters", base().With("sub", long))
		bad("a subject of 256 characters", base().With("sub", long+"x"), "subject_missing")
		bad("a missing subject", base().Without("sub"), "subject_missing")
		bad("an empty subject", base().With("sub", ""), "subject_missing")
		bad("a numeric subject", base().With("sub", 12), "subject_missing")
		bad("an array subject", base().With("sub", []string{"a"}), "subject_missing")
		bad("a subject with a control character", base().With("sub", "a\nb"), "subject_missing")
		bad("a subject with NUL", base().With("sub", "a\x00b"), "subject_missing")
		ok("a subject of 255 multi-byte characters", base().With("sub", strings.Repeat("é", 255)))
		bad("a subject of 256 multi-byte characters", base().With("sub", strings.Repeat("é", 256)), "subject_missing")
		reg := r.reg
		reg.SubjectClaim = "azp"
		if v, err := try("another subject claim", base().With("azp", "client-1").With("sub", "ignored"), reg); err != nil || v.Subject != "client-1" {
			t.Errorf("another subject claim: %+v %v", v, err)
		}
		_, err := try("the standard subject is not the registered claim", base(), reg)
		_ = wantReason(t, "sub present but azp is the subject claim", err, "subject_missing")
	})
	t.Run("tenant claim of a platform-level registration", func(t *testing.T) {
		reg := r.reg
		reg.Scope, reg.TenantClaim, reg.TenantID = "platform", "org", tenant.ID{}
		long := strings.Repeat("o", 255)
		if v, err := try("a tenant claim", base().With("org", long), reg); err != nil || v.TenantClaimValue != long {
			t.Errorf("a tenant claim of 255 characters: %+v %v", v, err)
		}
		for name, c := range map[string]fakeidp.Claims{
			"missing":        base(),
			"empty":          base().With("org", ""),
			"a number":       base().With("org", 7),
			"an array":       base().With("org", []string{"acme"}),
			"an object":      base().With("org", map[string]any{"id": "acme"}),
			"null":           base().With("org", nil),
			"256 characters": base().With("org", long+"o"),
		} {
			_, err := try(name, c, reg)
			f := wantReason(t, "tenant claim "+name, err, "tenant_claim_missing")
			if f.IssuerScope != "platform" || f.IssuerID != testRegID {
				t.Errorf("tenant claim %s: the failure names %q %q", name, f.IssuerScope, f.IssuerID)
			}
			if !f.TenantID.IsZero() {
				t.Errorf("tenant claim %s: the failure names a tenant", name)
			}
		}
	})
	t.Run("what a failure carries", func(t *testing.T) {
		// Registration fields from the first moment; the subject only after check 11.
		cases := []struct {
			name        string
			c           fakeidp.Claims
			wantSubject bool
		}{
			{"wrong audience", base().With("aud", "x"), false},
			{"expired", base().With("exp", now-3600), false},
			{"lifetime", base().With("iat", now-60).With("exp", now+life*2), false},
			{"required claim", base(), false},
			{"subject", base().Without("sub"), false},
		}
		reg := r.reg
		reg.RequiredClaims = map[string]string{"tier": "gold"}
		for _, c := range cases[:4] {
			claims := c.c
			if c.name != "required claim" {
				claims = claims.With("tier", "gold")
			}
			_, err := try(c.name, claims, reg)
			f := refusal(t, c.name, err)
			if f.IssuerID != testRegID || f.IssuerScope != "tenant" || f.TenantID.String() != testTenantID {
				t.Errorf("%s: failure %+v does not carry the registration", c.name, f)
			}
			if f.Subject != "" {
				t.Errorf("%s: the failure carries the subject %q before check 11", c.name, f.Subject)
			}
		}
		_, err := try("subject", base().Without("sub").With("tier", "gold"), reg)
		if f := refusal(t, "subject", err); f.Subject != "" || f.IssuerID != testRegID {
			t.Errorf("subject_missing: %+v", f)
		}
		// A failure after check 11 carries the verified subject.
		reg.Scope, reg.TenantClaim = "platform", "org"
		_, err = try("tenant claim", base().With("tier", "gold"), reg)
		if f := refusal(t, "tenant claim", err); f.Subject != "w" {
			t.Errorf("tenant_claim_missing: subject %q, want w (check 12 follows check 11)", f.Subject)
		}
	})
	t.Run("a disabled registration", func(t *testing.T) {
		reg := r.reg
		reg.Enabled = false
		_, err := try("disabled", base(), reg)
		_ = wantReason(t, "a disabled registration", err, "issuer_disabled")
	})
	t.Run("keys that cannot be obtained are an outage, not a refusal", func(t *testing.T) {
		r.keys.err = fmt.Errorf("%w: no keys", authz.ErrUnavailable)
		defer func() { r.keys.err = nil }()
		_, err := try("outage", base(), r.reg)
		if err == nil || !errorsIsUnavailable(err) {
			t.Errorf("err = %v, want authz.ErrUnavailable", err)
		}
		var f *Failure
		if errorsAs(err, &f) {
			t.Errorf("the outage is a refusal: %+v", f)
		}
	})
}

// TestSignatureOnEveryRequest is T-573: a token accepted once and then altered is refused, and
// what the token path keeps in memory has no member that could hold a token or a digest of one.
func TestSignatureOnEveryRequest(t *testing.T) {
	r := newTokenRig(t)
	tok := r.idp.Sign("ES256", r.claims("workload"))
	for i := 0; i < 3; i++ {
		if _, err := r.run(tok); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	h, p, s := pieces(t, tok)
	altered := map[string]string{
		"the payload":   h + "." + b64s(strings.Replace(string(mustDecode(t, p)), `"workload"`, `"workloaD"`, 1)) + "." + s,
		"the signature": h + "." + p + "." + flipLast(s),
		"the header":    b64s(strings.Replace(string(mustDecode(t, h)), `"JWT"`, `"JWS"`, 1)) + "." + p + "." + s,
	}
	for name, a := range altered {
		if a == tok {
			t.Fatalf("%s: the alteration changed nothing", name)
		}
		_, err := r.run(a)
		_ = wantReason(t, "after acceptance, altered "+name, err, "bad_signature", "malformed")
		// The original still works: nothing was learned from the refusal.
		if _, err := r.run(tok); err != nil {
			t.Errorf("the original token after the refusal of the altered %s: %v", name, err)
		}
	}
	// Every call verifies: the key source is asked each time (the key cache is its business).
	r.keys.calls = nil
	for i := 0; i < 4; i++ {
		if _, err := r.run(tok); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(r.keys.callLog()); n != 4 {
		t.Errorf("4 validations asked the key source %d times: a verification was skipped", n)
	}

	// What the token path keeps: no struct of the files that hold it has a member that can hold a
	// token, a segment, a signature or a digest of one.
	fset := token.NewFileSet()
	forbidden := []string{"presented", "jwt", "digest", "signature", "segment", "compact", "raw", "sha", "hash", "bearer"}
	checked := 0
	for _, file := range []string{"token.go", "tokens.go", "cache.go"} {
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			// The options and the interfaces of the configuration are not what is kept.
			if ts.Name.Name == "TokenOptions" {
				return true
			}
			checked++
			for _, field := range st.Fields.List {
				names := []string{}
				for _, id := range field.Names {
					names = append(names, id.Name)
				}
				typ := types(field.Type)
				for _, name := range names {
					low := strings.ToLower(name)
					for _, bad := range forbidden {
						if strings.Contains(low, bad) {
							t.Errorf("%s: struct %s has a member %q of type %s (it could hold a token or a digest)", file, ts.Name.Name, name, typ)
						}
					}
					if low == "token" || low == "tokens" || strings.HasSuffix(low, "token") {
						t.Errorf("%s: struct %s has a member %q of type %s (a token is never kept)", file, ts.Name.Name, name, typ)
					}
				}
				if typ == "[]byte" {
					t.Errorf("%s: struct %s has a member of type %s (bytes of a token or of a digest)", file, ts.Name.Name, typ)
				}
				if strings.HasPrefix(typ, "[") && strings.HasSuffix(typ, "]byte") && !strings.HasPrefix(typ, "[]") {
					t.Errorf("%s: struct %s has a member of type %s (a digest)", file, ts.Name.Name, typ)
				}
			}
			return true
		})
	}
	if checked < 3 {
		t.Errorf("only %d struct types were inspected: the scan is not looking at the token path", checked)
	}
}

func types(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.ArrayType:
		if x.Len == nil {
			return "[]" + types(x.Elt)
		}
		return "[" + types(x.Len) + "]" + types(x.Elt)
	case *ast.BasicLit:
		return x.Value
	case *ast.StarExpr:
		return "*" + types(x.X)
	case *ast.SelectorExpr:
		return types(x.X) + "." + x.Sel.Name
	case *ast.MapType:
		return "map[" + types(x.Key) + "]" + types(x.Value)
	}
	return fmt.Sprintf("%T", e)
}

func flipLast(s string) string {
	b := []byte(s)
	if b[len(b)-1] == 'A' {
		b[len(b)-1] = 'B'
	} else {
		b[len(b)-1] = 'A'
	}
	return string(b)
}

// FuzzParseToken is the fuzz target of T-570: whatever the bytes, the validation does not panic,
// and a token that is accepted verifies under the library with the key of the issuer.
func FuzzParseToken(f *testing.F) {
	idp := fakeidp.Start(f, fakeidp.Options{})
	other := fakeidp.Start(f, fakeidp.Options{})
	keys := &serverKeys{idp: idp}
	a := &Authenticator{
		kinds: map[Kind]bool{KindToken: true},
		now:   func() time.Time { return tokenNow },
		tok:   TokenOptions{Keys: keys, MaxBytes: 8192, ClockSkew: testSkew, MaxLifetime: testMaxLifetime},
	}
	reg := registration{Scope: "tenant", ID: testRegID, Issuer: idp.Issuer(), Audience: testAudience, Algorithms: fakeidp.Algorithms, SubjectClaim: "sub", Enabled: true}
	claims := idp.StandardAt(tokenNow, "workload", testAudience, 10*time.Minute)

	// Seeds: a valid token of each algorithm, and every token of T-571.
	for _, alg := range fakeidp.Algorithms {
		f.Add(idp.Sign(alg, claims))
		f.Add(idp.Sign(alg, claims, fakeidp.WithoutKID()))
	}
	for _, alg := range []string{"none", "None", "NONE"} {
		f.Add(idp.Raw(map[string]any{"alg": alg}, claims, nil))
		f.Add(idp.Raw(map[string]any{"alg": alg}, claims, func([]byte) []byte { return []byte("x") }))
	}
	f.Add(idp.Raw(map[string]any{"alg": "HS256", "kid": idp.KeyID("RS256")}, claims, fakeidp.HS256(idp.PublicKeyPEM("RS256"))))
	f.Add(idp.Raw(map[string]any{"alg": "HS256", "kid": idp.KeyID("RS256")}, claims, fakeidp.HS256(idp.PublicKeyDER("RS256"))))
	f.Add(idp.Raw(map[string]any{"alg": "ES256", "kid": idp.KeyID("ES256"), "crit": []string{"exp"}}, claims, idp.SignWith("ES256")))
	f.Add(idp.Raw(map[string]any{"alg": "ES256", "kid": idp.KeyID("ES256"), "jku": "https://attacker.example.test/k"}, claims, other.SignWith("ES256")))
	f.Add(other.Sign("ES256", claims))
	f.Add(fakeidp.RawJSON([]byte(`{"alg":"ES256","alg":"none"}`), []byte(`{"sub":"a","sub":"b"}`), idp.SignWith("ES256")))
	f.Add("aaaa.bbbb.cccc")
	f.Add("..")
	f.Add("")

	f.Fuzz(func(t *testing.T, tok string) {
		alg, kid, _, _, err := peek(tok)
		if err != nil {
			return
		}
		v, err := a.validate(t.Context(), tok, alg, kid, reg)
		if err != nil {
			return
		}
		// Accepted: the signature must verify with the key of the issuer, under the library.
		jws, perr := jose.ParseSignedCompact(tok, []jose.SignatureAlgorithm{jose.SignatureAlgorithm(alg)})
		if perr != nil {
			t.Fatalf("accepted a token the library cannot parse: %v\n%q", perr, tok)
		}
		pub, kerr := keys.Key(t.Context(), sourceOf(reg), kid, alg)
		if kerr != nil {
			t.Fatalf("accepted a token whose key the source does not have: %v\n%q", kerr, tok)
		}
		if _, verr := jws.Verify(pub); verr != nil {
			t.Fatalf("accepted a token without a valid signature (%v), subject %q\n%q", verr, v.Subject, tok)
		}
	})
}
