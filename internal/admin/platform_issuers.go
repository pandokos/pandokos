package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// ListPlatformIssuers: List the platform-level token issuers
func (s *server) ListPlatformIssuers(_ context.Context, _ adminapi.ListPlatformIssuersInput) (*adminapi.PlatformIssuerList, error) {
	return nil, errNotImplemented
}

// CreatePlatformIssuer: Register a platform-level token issuer
func (s *server) CreatePlatformIssuer(_ context.Context, _ adminapi.CreatePlatformIssuerInput) (*adminapi.PlatformIssuer, error) {
	return nil, errNotImplemented
}

// GetPlatformIssuer: Read a platform-level token issuer
func (s *server) GetPlatformIssuer(_ context.Context, _ adminapi.GetPlatformIssuerInput) (*adminapi.PlatformIssuer, error) {
	return nil, errNotImplemented
}

// UpdatePlatformIssuer: Change, disable or enable a platform-level token issuer
func (s *server) UpdatePlatformIssuer(_ context.Context, _ adminapi.UpdatePlatformIssuerInput) (*adminapi.PlatformIssuer, error) {
	return nil, errNotImplemented
}

// DeletePlatformIssuer: Delete a platform-level token issuer with its tenant list and subject mappings
func (s *server) DeletePlatformIssuer(_ context.Context, _ adminapi.DeletePlatformIssuerInput) (*adminapi.PlatformIssuer, error) {
	return nil, errNotImplemented
}

// ListPlatformIssuerTenants: List the tenants of a platform-level token issuer
func (s *server) ListPlatformIssuerTenants(_ context.Context, _ adminapi.ListPlatformIssuerTenantsInput) (*adminapi.PlatformIssuerTenantList, error) {
	return nil, errNotImplemented
}

// PutPlatformIssuerTenant: Add a tenant to a platform-level token issuer, or change its claim value
func (s *server) PutPlatformIssuerTenant(_ context.Context, _ adminapi.PutPlatformIssuerTenantInput) (*adminapi.PlatformIssuerTenant, error) {
	return nil, errNotImplemented
}

// DeletePlatformIssuerTenant: Remove a tenant from a platform-level token issuer
func (s *server) DeletePlatformIssuerTenant(_ context.Context, _ adminapi.DeletePlatformIssuerTenantInput) (*adminapi.PlatformIssuerTenant, error) {
	return nil, errNotImplemented
}
