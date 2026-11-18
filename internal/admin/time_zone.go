package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// GetTenantTimeZone reads the time zone of a tenant. Implemented in sprint 5.3, step C6.
func (s *server) GetTenantTimeZone(context.Context, adminapi.GetTenantTimeZoneInput) (*adminapi.TenantTimeZone, error) {
	return nil, errNotImplemented
}

// SetTenantTimeZone sets the time zone of a tenant. Implemented in sprint 5.3, step C6.
func (s *server) SetTenantTimeZone(context.Context, adminapi.SetTenantTimeZoneInput) (*adminapi.TenantTimeZone, error) {
	return nil, errNotImplemented
}
