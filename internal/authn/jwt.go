package authn

import (
	"context"
	"encoding/json"
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
// the validation of tokens of trusted issuers (checks 1 to 9 of sprint 4.1, section 5.4), exported
// for the OpenID Connect relying party (sprint 4.2). A refusal is a *Failure; keys that cannot be
// obtained are an error that wraps authz.ErrUnavailable.
func VerifyJWT(ctx context.Context, keys KeySource, now time.Time, token string, c JWTCheck) (map[string]json.RawMessage, error) {
	if c.MaxBytes <= 0 || !looksLikeToken(token, c.MaxBytes) {
		return nil, &Failure{Reason: ReasonMalformed}
	}
	alg, kid, _, _, err := peek(token)
	if err != nil {
		return nil, err
	}
	return verifyJWT(ctx, keys, now, token, alg, kid, c)
}
