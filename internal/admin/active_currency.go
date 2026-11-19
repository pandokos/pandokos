package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// afterActiveCurrencySet is a test seam (T-751): when set, setActiveCurrency calls it right after the
// write of the tenant row and fails with its error. Nil in production.
var afterActiveCurrencySet func() error

// GetActiveCurrency reads the active currency of a tenant. Implemented in sprint 5.4, step C6.
func (s *server) GetActiveCurrency(context.Context, adminapi.GetActiveCurrencyInput) (*adminapi.ActiveCurrency, error) {
	return nil, errNotImplemented
}

// SetActiveCurrency chooses the active currency of a tenant. Implemented in sprint 5.4, step C6.
func (s *server) SetActiveCurrency(context.Context, adminapi.SetActiveCurrencyInput) (*adminapi.ActiveCurrency, error) {
	return nil, errNotImplemented
}
