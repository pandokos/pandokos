package authn

import (
	"bytes"
	"context"
	"crypto"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/jwks"
	"github.com/pandokos/pandokos/internal/tenant"
)

// KindToken is the kind of a credential that is a JWT of a registered issuer. It is accepted by
// the authenticator of the gateway only (sprint 4.1, D-1); it has no key letter, because a token
// is not a key and Parse never returns it.
const KindToken Kind = 'j'

// TokenOptions configure the validation of tokens. Keys is required when KindToken is accepted.
type TokenOptions struct {
	Keys        KeySource     // where verification keys come from
	MaxBytes    int           // tokens.max_token_bytes
	ClockSkew   time.Duration // tokens.clock_skew
	MaxLifetime time.Duration // tokens.max_lifetime
}

// KeySource gives the verification key of a token. ErrKeyNotFound of package jwks is a refusal; an
// error that wraps authz.ErrUnavailable means the keys cannot be obtained now. *jwks.Cache is the
// implementation of the gateway.
type KeySource interface {
	Key(ctx context.Context, src jwks.Source, kid, alg string) (crypto.PublicKey, error)
}

// registration is what validation needs of a row of trusted_issuer or platform_issuer.
type registration struct {
	Scope, ID                 string    // Scope is "tenant" or "platform"
	TenantID                  tenant.ID // zero for platform
	Issuer, Audience, JWKSURI string
	Algorithms                []string
	SubjectClaim, TenantClaim string
	RequiredClaims            map[string]string
	Enabled                   bool
}

// verified is what a valid token yields.
type verified struct{ Subject, TenantClaimValue string }

const (
	maxAudiences  = 16  // entries of aud that are looked at
	maxClaimChars = 255 // subject and tenant claim, in characters
	maxTimeValue  = 1e12
)

// productAlgorithms is the allow-list of the product (section 5.3): asymmetric algorithms only.
// It is applied before any key is looked up and whatever a registration stores.
var productAlgorithms = map[string]bool{
	"RS256": true, "RS384": true, "RS512": true, "PS256": true, "PS384": true, "PS512": true,
	"ES256": true, "ES384": true, "ES512": true, "EdDSA": true,
}

// looksLikeToken reports whether presented has the shape of a compact JWT of at most maxBytes
// (section 5.2): exactly three non-empty segments of the unpadded base64url alphabet, and not a
// value that starts like a key.
func looksLikeToken(presented string, maxBytes int) bool {
	if len(presented) == 0 || len(presented) > maxBytes || strings.HasPrefix(presented, "pdk_") {
		return false
	}
	dots, segLen := 0, 0
	for i := 0; i < len(presented); i++ {
		switch c := presented[i]; {
		case c == '.':
			if segLen == 0 {
				return false
			}
			dots++
			segLen = 0
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			segLen++
		default:
			return false
		}
	}
	return dots == 2 && segLen > 0
}

// decodeObject decodes a JSON object whose member names are all different at the top level, with
// nothing after it. Duplicate names are refused here, by Pandokos's own decoding, because the
// decoders of other implementations disagree about which of two members wins.
func decodeObject(b []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("not an object")
	}
	m := map[string]json.RawMessage{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		name, ok := t.(string)
		if !ok {
			return nil, errors.New("not a member name")
		}
		if _, dup := m[name]; dup {
			return nil, errors.New("duplicate member")
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		m[name] = v
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return nil, errors.New("unterminated object")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data")
	}
	return m, nil
}

func segment(s string) ([]byte, error) { return base64.RawURLEncoding.Strict().DecodeString(s) }

// asString returns the string a raw JSON value is.
func asString(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// audiences reads aud: a string or an array of at most maxAudiences strings. An absent aud is no
// audience, not a failure.
func audiences(raw json.RawMessage, present bool) ([]string, bool) {
	if !present {
		return nil, true
	}
	if s, ok := asString(raw); ok {
		return []string{s}, true
	}
	var arr []json.RawMessage
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &arr) != nil || len(arr) > maxAudiences {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, r := range arr {
		s, ok := asString(r)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// peek decodes the header and the payload of a token without trusting them: checks 1, 2 and the
// shapes of 3. It returns the algorithm and key ID of the header and the issuer and audiences of
// the payload, which only select a registration, and nothing else of the payload. A missing iss
// or aud is returned empty: selection finds no registration for it.
func peek(presented string) (alg, kid, iss string, aud []string, err error) {
	malformed := &Failure{Reason: "malformed"}
	parts := strings.Split(presented, ".")
	if len(parts) != 3 {
		return "", "", "", nil, malformed
	}
	hb, herr := segment(parts[0])
	pb, perr := segment(parts[1])
	// The signature is decoded strictly too: a segment with bits set that no byte uses decodes to
	// the same signature as the canonical one, and so would give one token several spellings.
	if _, serr := segment(parts[2]); herr != nil || perr != nil || serr != nil {
		return "", "", "", nil, malformed
	}
	hdr, herr := decodeObject(hb)
	pay, perr := decodeObject(pb)
	if herr != nil || perr != nil {
		return "", "", "", nil, malformed
	}
	alg, ok := asString(hdr["alg"])
	if _, crit := hdr["crit"]; crit || !ok || !productAlgorithms[alg] {
		return "", "", "", nil, &Failure{Reason: "algorithm_not_allowed"}
	}
	if raw, present := hdr["kid"]; present {
		if kid, ok = asString(raw); !ok {
			return "", "", "", nil, malformed
		}
	}
	if raw, present := pay["iss"]; present {
		if iss, ok = asString(raw); !ok {
			return "", "", "", nil, malformed
		}
	}
	raw, present := pay["aud"]
	if aud, ok = audiences(raw, present); !ok {
		return "", "", "", nil, malformed
	}
	return alg, kid, iss, aud, nil
}

// seconds reads a numeric claim as an instant. A value that is not a JSON number, or lies
// outside plus or minus 1e12 seconds, is not an instant.
func seconds(raw json.RawMessage) (time.Time, bool) {
	if len(raw) == 0 || raw[0] == '"' {
		return time.Time{}, false
	}
	v, err := strconv.ParseFloat(string(raw), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > maxTimeValue {
		return time.Time{}, false
	}
	sec := math.Floor(v)
	return time.Unix(int64(sec), int64((v-sec)*1e9)), true
}

// claimString reads a string claim of 1 to maxClaimChars characters.
func claimString(claims map[string]json.RawMessage, name string) (string, bool) {
	s, ok := asString(claims[name])
	if !ok || s == "" || utf8.RuneCountInString(s) > maxClaimChars {
		return "", false
	}
	return s, true
}

// hasClaim reports whether the claim holds the wanted string, or an array of values that has it.
func hasClaim(raw json.RawMessage, want string) bool {
	if s, ok := asString(raw); ok {
		return s == want
	}
	var arr []json.RawMessage
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &arr) != nil {
		return false
	}
	for _, r := range arr {
		if s, ok := asString(r); ok && s == want {
			return true
		}
	}
	return false
}

// validate runs checks 4 to 12 of section 5.4 against the registration, for a token that peek
// accepted with the algorithm alg and key ID kid. The library is given exactly the one algorithm;
// the claims are read from the payload that verification returns, never from peek. A refusal is a
// *Failure; keys that cannot be obtained are an error that wraps authz.ErrUnavailable.
func (a *Authenticator) validate(ctx context.Context, presented, alg, kid string, r registration) (verified, error) {
	fail := func(reason string) *Failure {
		return &Failure{Reason: reason, IssuerID: r.ID, IssuerScope: r.Scope, TenantID: r.TenantID}
	}
	// 4: the registration; the algorithm in the product's list (again) and in the registration's.
	if !r.Enabled {
		return verified{}, fail("issuer_disabled")
	}
	listed := false
	for _, x := range r.Algorithms {
		listed = listed || x == alg
	}
	if !productAlgorithms[alg] || !listed {
		return verified{}, fail("algorithm_not_allowed")
	}
	// 5: the key, from the registration's source only.
	jws, err := jose.ParseSignedCompact(presented, []jose.SignatureAlgorithm{jose.SignatureAlgorithm(alg)})
	if err != nil || len(jws.Signatures) != 1 || jws.Signatures[0].Header.Algorithm != alg {
		return verified{}, fail("malformed")
	}
	pub, err := a.tok.Keys.Key(ctx, jwks.Source{Issuer: r.Issuer, JWKSURI: r.JWKSURI}, kid, alg)
	switch {
	case errors.Is(err, jwks.ErrKeyNotFound):
		return verified{}, fail("unknown_signing_key")
	case err != nil && errors.Is(err, authz.ErrUnavailable):
		return verified{}, err
	case err != nil:
		return verified{}, errors.Join(authz.ErrUnavailable, err)
	case !jwks.Fits(pub, alg): // whatever the source hands over, the type and size must fit
		return verified{}, fail("unknown_signing_key")
	}
	// 6: the signature, over the first two segments as received.
	payload, err := jws.Verify(pub)
	if err != nil {
		return verified{}, fail("bad_signature")
	}
	claims, err := decodeObject(payload)
	if err != nil {
		return verified{}, fail("malformed")
	}
	// 7: issuer and audience, again, from the verified payload.
	if iss, _ := asString(claims["iss"]); iss != r.Issuer {
		return verified{}, fail("issuer_mismatch")
	}
	raw, present := claims["aud"]
	auds, ok := audiences(raw, present)
	if !ok {
		return verified{}, fail("malformed")
	}
	found := false
	for _, x := range auds {
		found = found || x == r.Audience
	}
	if !found {
		return verified{}, fail("wrong_audience")
	}
	// 8: exp is required; nbf and iat are honoured when present.
	now, skew := a.now(), a.tok.ClockSkew
	exp, ok := seconds(claims["exp"])
	if !ok || !now.Before(exp.Add(skew)) {
		return verified{}, fail("expired")
	}
	if raw, present := claims["nbf"]; present {
		if nbf, ok := seconds(raw); !ok || now.Before(nbf.Add(-skew)) {
			return verified{}, fail("not_yet_valid")
		}
	}
	start := now
	if raw, present := claims["iat"]; present {
		iat, ok := seconds(raw)
		if !ok || iat.After(now.Add(skew)) {
			return verified{}, fail("not_yet_valid")
		}
		start = iat
	}
	// 9: the lifetime.
	if exp.Sub(start) > a.tok.MaxLifetime {
		return verified{}, fail("lifetime_too_long")
	}
	// 10: the required claims.
	for name, want := range r.RequiredClaims {
		if !hasClaim(claims[name], want) {
			return verified{}, fail("required_claim_missing")
		}
	}
	// 11: the subject.
	subject, ok := claimString(claims, r.SubjectClaim)
	if !ok || strings.ContainsFunc(subject, unicode.IsControl) {
		return verified{}, fail("subject_missing")
	}
	v := verified{Subject: subject}
	// 12: the tenant claim of a platform-level registration.
	if r.Scope == "platform" {
		if v.TenantClaimValue, ok = claimString(claims, r.TenantClaim); !ok || strings.ContainsFunc(v.TenantClaimValue, unicode.IsControl) {
			f := fail("tenant_claim_missing")
			f.Subject = subject
			return verified{}, f
		}
	}
	return v, nil
}
