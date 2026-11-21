package jwks

import (
	"context"
	"errors"
)

// Metadata is what the OpenID Connect relying party needs of a discovery document.
type Metadata struct{ Issuer, JWKSURI, AuthorizationEndpoint, TokenEndpoint string }

// Metadata returns the discovery document of an issuer, whose issuer member must be the one asked
// for (sprint 4.2). Part A gives it no body.
func (c *Cache) Metadata(ctx context.Context, issuer string) (Metadata, error) {
	_, _, _ = ctx, issuer, c
	return Metadata{}, errors.New("jwks: Metadata is not implemented")
}
