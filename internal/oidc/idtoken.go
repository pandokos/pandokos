package oidc

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pandokos/pandokos/internal/authn"
)

const (
	maxIDTokenBytes  = 64 << 10
	idTokenSkew      = 60 * time.Second
	idTokenLifetime  = 24 * time.Hour
	maxSubjectLength = 255
)

// identity validates the ID token (authn.VerifyJWT: algorithm, key, signature, issuer, audience,
// instants, lifetime) and then what OpenID Connect adds: nonce, azp, subject. It keeps the
// subject, the display attributes and the groups, and nothing else.
func (p *Provider) identity(ctx context.Context, jwksURI, idToken, nonce string) (Identity, error) {
	claims, err := authn.VerifyJWT(ctx, p.o.Cache, p.o.Now(), idToken, authn.JWTCheck{
		Issuer: p.o.Issuer, Audience: p.o.ClientID, JWKSURI: jwksURI, Algorithms: p.o.Algorithms,
		ClockSkew: idTokenSkew, MaxLifetime: idTokenLifetime, MaxBytes: maxIDTokenBytes,
	})
	var f *authn.Failure
	if errors.As(err, &f) {
		return Identity{}, &Failure{Reason: f.Reason}
	}
	if err != nil {
		return Identity{}, err
	}
	if got, ok := stringClaim(claims["nonce"]); !ok || !hmac.Equal([]byte(got), []byte(nonce)) {
		return Identity{}, &Failure{Reason: "nonce_mismatch"}
	}
	if raw, present := claims["azp"]; present {
		if azp, ok := stringClaim(raw); !ok || azp != p.o.ClientID {
			return Identity{}, &Failure{Reason: "azp_mismatch"}
		}
	} else if audienceCount(claims["aud"]) > 1 {
		return Identity{}, &Failure{Reason: "azp_missing"}
	}
	sub, ok := stringClaim(claims["sub"])
	if !ok || sub == "" || utf8.RuneCountInString(sub) > maxSubjectLength || strings.ContainsFunc(sub, unicode.IsControl) {
		return Identity{}, &Failure{Reason: "subject_missing"}
	}
	id := Identity{Subject: sub, Groups: Groups(claims, p.o.GroupsClaim)}
	id.Name, _ = stringClaim(claims[p.o.NameClaim])
	id.Email, _ = stringClaim(claims[p.o.EmailClaim])
	return id, nil
}

func stringClaim(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

func audienceCount(raw json.RawMessage) int {
	if len(raw) > 0 && raw[0] == '[' {
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) == nil {
			return len(list)
		}
		return 0
	}
	if len(raw) > 0 {
		return 1
	}
	return 0
}
