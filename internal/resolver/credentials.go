package resolver

import (
	"context"
	"errors"
	"fmt"

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

// Open opens the credential a call is made with. A missing, disabled or revoked credential is
// ErrNoCredential, a principal that may not use one ErrCredentialDenied; any other failure is
// returned wrapped.
func (c *Credentials) Open(ctx context.Context, p authz.Principal, requestID, provider string) (gateway.Credential, error) {
	o, err := c.opener.Open(ctx, vault.Use{Principal: p, RequestID: requestID, Provider: provider})
	var nu *vault.NotUsableError
	switch {
	case errors.Is(err, vault.ErrNoCredential), errors.As(err, &nu):
		return gateway.Credential{}, gateway.ErrNoCredential
	case errors.Is(err, vault.ErrDenied):
		return gateway.Credential{}, gateway.ErrCredentialDenied
	case err != nil:
		return gateway.Credential{}, fmt.Errorf("resolver: opening the credential: %w", err)
	}
	return gateway.Credential{ID: o.ID, Pinned: o.Pinned, Secret: o.Secret}, nil
}
