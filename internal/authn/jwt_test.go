package authn

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/test/fakeidp"
)

func (r *tokenRig) check() JWTCheck {
	return JWTCheck{
		Issuer: r.reg.Issuer, Audience: testAudience, JWKSURI: r.reg.JWKSURI,
		Algorithms: []string{"RS256", "ES256"}, ClockSkew: testSkew, MaxLifetime: testMaxLifetime, MaxBytes: 8192,
	}
}

// TestVerifyJWT is T-824: the exported verifier gives the claims of a valid token and each
// refusal reason of the checks 4 to 9 of sprint 4.1.
func TestVerifyJWT(t *testing.T) {
	r := newTokenRig(t)
	ctx := context.Background()
	verify := func(tok string, c JWTCheck) (map[string]json.RawMessage, error) {
		return VerifyJWT(ctx, r.keys, tokenNow, tok, c)
	}

	for _, alg := range []string{"RS256", "ES256"} {
		claims, err := verify(r.idp.Sign(alg, r.claims("person-1").With("name", "Ada").With("groups", []string{"a", "b"})), r.check())
		if err != nil {
			t.Fatalf("%s: a valid token: %v", alg, err)
		}
		var sub, name string
		var groups []string
		if json.Unmarshal(claims["sub"], &sub) != nil || sub != "person-1" ||
			json.Unmarshal(claims["name"], &name) != nil || name != "Ada" ||
			json.Unmarshal(claims["groups"], &groups) != nil || len(groups) != 2 {
			t.Errorf("%s: claims %v", alg, claims)
		}
	}

	reason := func(what string, tok string, c JWTCheck, reasons ...string) {
		t.Helper()
		_, err := verify(tok, c)
		var f *Failure
		if !errors.As(err, &f) || !errors.Is(err, authz.ErrUnauthenticated) {
			t.Errorf("%s: err = %v, want a *Failure", what, err)
			return
		}
		for _, want := range reasons {
			if f.Reason == want {
				return
			}
		}
		t.Errorf("%s: reason %q, want one of %v", what, f.Reason, reasons)
	}

	c := r.check()
	good := r.claims("person-1")
	reason("not a token", "not-a-token", c, "malformed")
	reason("two segments", "a.b", c, "malformed")
	reason("empty", "", c, "malformed")
	reason("too large", strings.Repeat("a", 9000)+".b.c", c, "malformed")
	reason("alg none", r.idp.Raw(map[string]any{"alg": "none"}, good, nil), c, "algorithm_not_allowed", "malformed")
	reason("HS256", r.idp.Raw(map[string]any{"alg": "HS256", "kid": r.idp.KeyID("RS256")}, good, fakeidp.HS256(r.idp.PublicKeyDER("RS256"))), c, "algorithm_not_allowed")
	reason("an algorithm outside the check", r.idp.Sign("PS256", good), c, "algorithm_not_allowed")
	reason("another key", r.idp.Sign("RS256", good, fakeidp.WithKID("no-such-key")), c, "unknown_signing_key")
	other := fakeidp.Start(t, fakeidp.Options{})
	reason("another issuer's key", other.Sign("RS256", good, fakeidp.WithKID(r.idp.KeyID("RS256"))), c, "bad_signature")
	reason("wrong issuer", r.idp.Sign("RS256", good.With("iss", "https://other.example.test")), c, "issuer_mismatch")
	reason("no issuer", r.idp.Sign("RS256", good.Without("iss")), c, "issuer_mismatch")
	reason("wrong audience", r.idp.Sign("RS256", good.With("aud", "someone-else")), c, "wrong_audience")
	reason("no audience", r.idp.Sign("RS256", good.Without("aud")), c, "wrong_audience")
	reason("expired", r.idp.Sign("RS256", r.idp.StandardAt(tokenNow.Add(-time.Hour), "p", testAudience, 10*time.Minute)), c, "expired")
	reason("no exp", r.idp.Sign("RS256", good.Without("exp")), c, "expired")
	reason("not yet valid (nbf)", r.idp.Sign("RS256", good.With("nbf", tokenNow.Add(10*time.Minute).Unix())), c, "not_yet_valid")
	reason("not yet valid (iat)", r.idp.Sign("RS256", r.idp.StandardAt(tokenNow.Add(10*time.Minute), "p", testAudience, 10*time.Minute)), c, "not_yet_valid")
	reason("lifetime", r.idp.Sign("RS256", r.idp.StandardAt(tokenNow, "p", testAudience, 48*time.Hour)), c, "lifetime_too_long")

	// The skew: a token that expired a few seconds ago is still honoured, one that expired long ago is not.
	if _, err := verify(r.idp.Sign("RS256", r.idp.StandardAt(tokenNow.Add(-10*time.Minute-30*time.Second), "p", testAudience, 10*time.Minute)), c); err != nil {
		t.Errorf("a token inside the clock skew: %v", err)
	}

	// Keys that cannot be obtained are an outage, not a refusal.
	r.keys.err = authz.ErrUnavailable
	if _, err := verify(r.idp.Sign("RS256", good), c); !errors.Is(err, authz.ErrUnavailable) || errors.Is(err, authz.ErrUnauthenticated) {
		t.Errorf("keys unavailable: %v, want authz.ErrUnavailable", err)
	}
}
