package admin

import (
	"context"
	"errors"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// GetCapability says whether a tenant can call a provider and model now. Not implemented yet.
func (s *server) GetCapability(_ context.Context, _ adminapi.GetCapabilityInput) (*adminapi.Capability, error) {
	return nil, errors.New("admin: not implemented")
}
