package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// Test seams (T-751): when set, createPriceListVersion calls afterTenantPriceInsert right after the
// insert of the version and its rates and fails with its error, so that a test can check that the
// version and its audit row stay together. Nil in production.
var afterTenantPriceInsert func() error

// maxTenantPriceVersions is the bound on the versions of one tenant (sprint 5.4, section 5.5); a
// variable so that T-749 can lower it.
var maxTenantPriceVersions = 50_000

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
