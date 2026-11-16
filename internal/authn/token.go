package authn

import (
	"context"
	"crypto"
	"errors"
	"time"

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

var errTokenNotImplemented = errors.New("authn: token validation is not implemented")

// looksLikeToken reports whether presented has the shape of a compact JWT of at most maxBytes
// (section 5.2). The scaffolding recognizes none.
func looksLikeToken(_ string, _ int) bool { return false }

// peek decodes the header and the payload of a token without trusting them: checks 1, 2 and the
// shapes of 3. The scaffolding refuses every token.
func peek(_ string) (alg, kid, iss string, aud []string, err error) {
	return "", "", "", nil, errTokenNotImplemented
}

// validate runs checks 4 to 12 of section 5.4 against the registration. The scaffolding refuses
// every token.
func (a *Authenticator) validate(_ context.Context, _, _, _ string, _ registration) (verified, error) {
	return verified{}, errTokenNotImplemented
}
