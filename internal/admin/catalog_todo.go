package admin

import (
	"context"
	"errors"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// errNotImplemented is returned by the catalogue operations until their handlers exist.
var errNotImplemented = errors.New("not implemented")

// The catalogue operations are declared by the document and implemented in step C8 of sprint 2.2;
// this file keeps the package compiling until then and is deleted with it.

// ListModels is not implemented yet.
func (s *server) ListModels(_ context.Context, _ adminapi.ListModelsInput) (*adminapi.ModelList, error) {
	return nil, errNotImplemented
}

// CreateModel is not implemented yet.
func (s *server) CreateModel(_ context.Context, _ adminapi.CreateModelInput) (*adminapi.Model, error) {
	return nil, errNotImplemented
}

// GetModel is not implemented yet.
func (s *server) GetModel(_ context.Context, _ adminapi.GetModelInput) (*adminapi.Model, error) {
	return nil, errNotImplemented
}

// UpdateModel is not implemented yet.
func (s *server) UpdateModel(_ context.Context, _ adminapi.UpdateModelInput) (*adminapi.Model, error) {
	return nil, errNotImplemented
}

// DeleteModel is not implemented yet.
func (s *server) DeleteModel(_ context.Context, _ adminapi.DeleteModelInput) (*adminapi.Model, error) {
	return nil, errNotImplemented
}

// ClearPriceOverride is not implemented yet.
func (s *server) ClearPriceOverride(_ context.Context, _ adminapi.ClearPriceOverrideInput) (*adminapi.Model, error) {
	return nil, errNotImplemented
}

// ListModelGrants is not implemented yet.
func (s *server) ListModelGrants(_ context.Context, _ adminapi.ListModelGrantsInput) (*adminapi.ModelGrantList, error) {
	return nil, errNotImplemented
}

// GrantModel is not implemented yet.
func (s *server) GrantModel(_ context.Context, _ adminapi.GrantModelInput) (*adminapi.ModelGrant, error) {
	return nil, errNotImplemented
}

// RevokeModelGrant is not implemented yet.
func (s *server) RevokeModelGrant(_ context.Context, _ adminapi.RevokeModelGrantInput) (*adminapi.ModelGrant, error) {
	return nil, errNotImplemented
}

// ListPriceVersions is not implemented yet.
func (s *server) ListPriceVersions(_ context.Context, _ adminapi.ListPriceVersionsInput) (*adminapi.PriceVersionList, error) {
	return nil, errNotImplemented
}

// CreatePriceOverride is not implemented yet.
func (s *server) CreatePriceOverride(_ context.Context, _ adminapi.CreatePriceOverrideInput) (*adminapi.PriceVersion, error) {
	return nil, errNotImplemented
}

// DeletePriceVersion is not implemented yet.
func (s *server) DeletePriceVersion(_ context.Context, _ adminapi.DeletePriceVersionInput) (*adminapi.PriceVersion, error) {
	return nil, errNotImplemented
}

// ListPriceImports is not implemented yet.
func (s *server) ListPriceImports(_ context.Context, _ adminapi.ListPriceImportsInput) (*adminapi.PriceImportList, error) {
	return nil, errNotImplemented
}

// RunPriceImport is not implemented yet.
func (s *server) RunPriceImport(_ context.Context, _ adminapi.RunPriceImportInput) (*adminapi.PriceImport, error) {
	return nil, errNotImplemented
}

// GetPriceImport is not implemented yet.
func (s *server) GetPriceImport(_ context.Context, _ adminapi.GetPriceImportInput) (*adminapi.PriceImport, error) {
	return nil, errNotImplemented
}

// ListProviders is not implemented yet.
func (s *server) ListProviders(_ context.Context, _ adminapi.ListProvidersInput) (*adminapi.ProviderList, error) {
	return nil, errNotImplemented
}

// CreateProvider is not implemented yet.
func (s *server) CreateProvider(_ context.Context, _ adminapi.CreateProviderInput) (*adminapi.Provider, error) {
	return nil, errNotImplemented
}

// GetProvider is not implemented yet.
func (s *server) GetProvider(_ context.Context, _ adminapi.GetProviderInput) (*adminapi.Provider, error) {
	return nil, errNotImplemented
}

// UpdateProvider is not implemented yet.
func (s *server) UpdateProvider(_ context.Context, _ adminapi.UpdateProviderInput) (*adminapi.Provider, error) {
	return nil, errNotImplemented
}

// DeleteProvider is not implemented yet.
func (s *server) DeleteProvider(_ context.Context, _ adminapi.DeleteProviderInput) (*adminapi.Provider, error) {
	return nil, errNotImplemented
}

// ListEndpoints is not implemented yet.
func (s *server) ListEndpoints(_ context.Context, _ adminapi.ListEndpointsInput) (*adminapi.EndpointList, error) {
	return nil, errNotImplemented
}

// CreateEndpoint is not implemented yet.
func (s *server) CreateEndpoint(_ context.Context, _ adminapi.CreateEndpointInput) (*adminapi.Endpoint, error) {
	return nil, errNotImplemented
}

// GetEndpoint is not implemented yet.
func (s *server) GetEndpoint(_ context.Context, _ adminapi.GetEndpointInput) (*adminapi.Endpoint, error) {
	return nil, errNotImplemented
}

// UpdateEndpoint is not implemented yet.
func (s *server) UpdateEndpoint(_ context.Context, _ adminapi.UpdateEndpointInput) (*adminapi.Endpoint, error) {
	return nil, errNotImplemented
}

// DeleteEndpoint is not implemented yet.
func (s *server) DeleteEndpoint(_ context.Context, _ adminapi.DeleteEndpointInput) (*adminapi.Endpoint, error) {
	return nil, errNotImplemented
}

// GetModelRestriction is not implemented yet.
func (s *server) GetModelRestriction(_ context.Context, _ adminapi.GetModelRestrictionInput) (*adminapi.ModelRestriction, error) {
	return nil, errNotImplemented
}

// SetModelRestriction is not implemented yet.
func (s *server) SetModelRestriction(_ context.Context, _ adminapi.SetModelRestrictionInput) (*adminapi.ModelRestriction, error) {
	return nil, errNotImplemented
}

// ClearModelRestriction is not implemented yet.
func (s *server) ClearModelRestriction(_ context.Context, _ adminapi.ClearModelRestrictionInput) (*adminapi.ModelRestriction, error) {
	return nil, errNotImplemented
}

// ListTenantModels is not implemented yet.
func (s *server) ListTenantModels(_ context.Context, _ adminapi.ListTenantModelsInput) (*adminapi.TenantModelList, error) {
	return nil, errNotImplemented
}
