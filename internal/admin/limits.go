package admin

import (
	"context"
	"errors"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// errLimitsNotImplemented is what the operations on limits answer until sprint 5.2 implements them.
var errLimitsNotImplemented = errors.New("admin: limits administration is not implemented")

// ListLimits: List the limit rules of a tenant
func (s *server) ListLimits(context.Context, adminapi.ListLimitsInput) (*adminapi.LimitRuleList, error) {
	return nil, errLimitsNotImplemented
}

// CreateLimit: Create a limit rule of a tenant
func (s *server) CreateLimit(context.Context, adminapi.CreateLimitInput) (*adminapi.LimitRule, error) {
	return nil, errLimitsNotImplemented
}

// GetLimit: Read a limit rule
func (s *server) GetLimit(context.Context, adminapi.GetLimitInput) (*adminapi.LimitRule, error) {
	return nil, errLimitsNotImplemented
}

// UpdateLimit: Change the value, the enforcement or the thresholds of a limit rule
func (s *server) UpdateLimit(context.Context, adminapi.UpdateLimitInput) (*adminapi.LimitRule, error) {
	return nil, errLimitsNotImplemented
}

// DeleteLimit: Delete a limit rule
func (s *server) DeleteLimit(context.Context, adminapi.DeleteLimitInput) (*adminapi.LimitRule, error) {
	return nil, errLimitsNotImplemented
}

// ListTenantPlatformLimits: List the ceilings the platform sets for a tenant
func (s *server) ListTenantPlatformLimits(context.Context, adminapi.ListTenantPlatformLimitsInput) (*adminapi.PlatformLimitRuleList, error) {
	return nil, errLimitsNotImplemented
}

// ListLimitEvents: List what the tenant's quotas and budgets recorded, newest first
func (s *server) ListLimitEvents(context.Context, adminapi.ListLimitEventsInput) (*adminapi.LimitEventList, error) {
	return nil, errLimitsNotImplemented
}
