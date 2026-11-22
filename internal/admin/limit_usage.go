package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// GetLimitUsage is a stub until sprint 6.2 part C4.
func (s *server) GetLimitUsage(context.Context, adminapi.GetLimitUsageInput) (*adminapi.LimitUsage, error) {
	return nil, errNotImplemented
}
