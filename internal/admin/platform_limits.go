package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// ListPlatformLimits: List the ceilings the platform sets for tenants
func (s *server) ListPlatformLimits(context.Context, adminapi.ListPlatformLimitsInput) (*adminapi.PlatformLimitRuleList, error) {
	return nil, errLimitsNotImplemented
}

// CreatePlatformLimit: Create a ceiling for every tenant or for one
func (s *server) CreatePlatformLimit(context.Context, adminapi.CreatePlatformLimitInput) (*adminapi.PlatformLimitRule, error) {
	return nil, errLimitsNotImplemented
}

// GetPlatformLimit: Read a platform ceiling
func (s *server) GetPlatformLimit(context.Context, adminapi.GetPlatformLimitInput) (*adminapi.PlatformLimitRule, error) {
	return nil, errLimitsNotImplemented
}

// UpdatePlatformLimit: Change the value, the enforcement or the thresholds of a platform ceiling
func (s *server) UpdatePlatformLimit(context.Context, adminapi.UpdatePlatformLimitInput) (*adminapi.PlatformLimitRule, error) {
	return nil, errLimitsNotImplemented
}

// DeletePlatformLimit: Delete a platform ceiling
func (s *server) DeletePlatformLimit(context.Context, adminapi.DeletePlatformLimitInput) (*adminapi.PlatformLimitRule, error) {
	return nil, errLimitsNotImplemented
}
