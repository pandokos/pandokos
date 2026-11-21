package authn

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// JWTCheck says what a verified JWT must satisfy: its issuer and audience, the address of its key
// set, the algorithms allowed, the clock skew and the longest lifetime, and its size.
type JWTCheck struct {
	Issuer, Audience, JWKSURI string
	Algorithms                []string
	ClockSkew, MaxLifetime    time.Duration
	MaxBytes                  int
}

// VerifyJWT checks the signature and the claims of token against c and returns its claims. It is
// the validation of tokens of trusted issuers, exported for the OpenID Connect relying party
// (sprint 4.2); its behaviour for tokens does not change. Part A gives it no body.
func VerifyJWT(ctx context.Context, keys KeySource, now time.Time, token string, c JWTCheck) (map[string]json.RawMessage, error) {
	_, _, _, _, _ = ctx, keys, now, token, c
	return nil, errors.New("authn: VerifyJWT is not implemented")
}
