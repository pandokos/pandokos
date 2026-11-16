package admin

import (
	"context"
	"errors"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// errNotImplemented is what the operations of sprint 4.1 answer until their handlers exist: the
// package's internal error, which the router renders as 500 internal.
var errNotImplemented = errors.New("not implemented")

// ListTenantPlatformIssuers: List the platform-level token issuers that list the tenant
func (s *server) ListTenantPlatformIssuers(_ context.Context, _ adminapi.ListTenantPlatformIssuersInput) (*adminapi.TenantPlatformIssuerList, error) {
	return nil, errNotImplemented
}

// ListTokenIssuers: List the trusted token issuers of a tenant
func (s *server) ListTokenIssuers(_ context.Context, _ adminapi.ListTokenIssuersInput) (*adminapi.TokenIssuerList, error) {
	return nil, errNotImplemented
}

// CreateTokenIssuer: Register a trusted token issuer
func (s *server) CreateTokenIssuer(_ context.Context, _ adminapi.CreateTokenIssuerInput) (*adminapi.TokenIssuer, error) {
	return nil, errNotImplemented
}

// GetTokenIssuer: Read a trusted token issuer
func (s *server) GetTokenIssuer(_ context.Context, _ adminapi.GetTokenIssuerInput) (*adminapi.TokenIssuer, error) {
	return nil, errNotImplemented
}

// UpdateTokenIssuer: Change, disable or enable a trusted token issuer
func (s *server) UpdateTokenIssuer(_ context.Context, _ adminapi.UpdateTokenIssuerInput) (*adminapi.TokenIssuer, error) {
	return nil, errNotImplemented
}

// DeleteTokenIssuer: Delete a trusted token issuer with its subject mappings
func (s *server) DeleteTokenIssuer(_ context.Context, _ adminapi.DeleteTokenIssuerInput) (*adminapi.TokenIssuer, error) {
	return nil, errNotImplemented
}
