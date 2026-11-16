package authn

import (
	"context"
	"crypto"
	"time"

	"github.com/pandokos/pandokos/internal/jwks"
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
