package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// ListPriceLists lists the price lists of a tenant. Implemented in sprint 5.4, step C6.
func (s *server) ListPriceLists(context.Context, adminapi.ListPriceListsInput) (*adminapi.TenantPriceListList, error) {
	return nil, errNotImplemented
}

// ListPriceListVersions lists the price versions of a tenant's lists. Implemented in sprint 5.4, step C6.
func (s *server) ListPriceListVersions(context.Context, adminapi.ListPriceListVersionsInput) (*adminapi.TenantPriceVersionList, error) {
	return nil, errNotImplemented
}

// CreatePriceListVersion adds a price version to a tenant's list. Implemented in sprint 5.4, step C6.
func (s *server) CreatePriceListVersion(context.Context, adminapi.CreatePriceListVersionInput) (*adminapi.TenantPriceVersion, error) {
	return nil, errNotImplemented
}

// GetPriceListVersion reads a price version of a tenant's lists. Implemented in sprint 5.4, step C6.
func (s *server) GetPriceListVersion(context.Context, adminapi.GetPriceListVersionInput) (*adminapi.TenantPriceVersion, error) {
	return nil, errNotImplemented
}

// DeletePriceListVersion removes a price version that has not started. Implemented in sprint 5.4, step C6.
func (s *server) DeletePriceListVersion(context.Context, adminapi.DeletePriceListVersionInput) (*adminapi.TenantPriceVersion, error) {
	return nil, errNotImplemented
}
