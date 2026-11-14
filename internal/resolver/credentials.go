package resolver

import (
	"context"
	"errors"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/vault"
)

// Credentials implements gateway.Credentials over the vault.
type Credentials struct {
	opener *vault.Opener
}

// NewCredentials returns the gateway's way to open a provider credential.
func NewCredentials(o *vault.Opener) *Credentials { return &Credentials{opener: o} }

// Open opens the credential a call is made with.
func (c *Credentials) Open(ctx context.Context, p authz.Principal, requestID, provider string) (gateway.Credential, error) {
	_, _, _, _ = ctx, p, requestID, provider
	return gateway.Credential{}, errors.New("resolver: not implemented")
}
