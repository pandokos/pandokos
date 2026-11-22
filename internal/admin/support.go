package admin

// Support actions of the platform inside a tenant, seen from both sides (sprint 4.3).

import (
	"context"
	"errors"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// ListTenantSupportActions is a stub of part A (sprint 4.3): step C writes it.
func (s *server) ListTenantSupportActions(_ context.Context, _ adminapi.ListTenantSupportActionsInput) (*adminapi.SupportActionList, error) {
	return nil, errors.New("not implemented")
}

// OpenSupportAction is a stub of part A (sprint 4.3): step C writes it.
func (s *server) OpenSupportAction(_ context.Context, _ adminapi.OpenSupportActionInput) (*adminapi.SupportAction, error) {
	return nil, errors.New("not implemented")
}

// GetTenantSupportAction is a stub of part A (sprint 4.3): step C writes it.
func (s *server) GetTenantSupportAction(_ context.Context, _ adminapi.GetTenantSupportActionInput) (*adminapi.SupportAction, error) {
	return nil, errors.New("not implemented")
}

// CloseSupportAction is a stub of part A (sprint 4.3): step C writes it.
func (s *server) CloseSupportAction(_ context.Context, _ adminapi.CloseSupportActionInput) (*adminapi.SupportAction, error) {
	return nil, errors.New("not implemented")
}

// ListSupportActions is a stub of part A (sprint 4.3): step C writes it.
func (s *server) ListSupportActions(_ context.Context, _ adminapi.ListSupportActionsInput) (*adminapi.SupportActionList, error) {
	return nil, errors.New("not implemented")
}

// GetSupportAction is a stub of part A (sprint 4.3): step C writes it.
func (s *server) GetSupportAction(_ context.Context, _ adminapi.GetSupportActionInput) (*adminapi.SupportAction, error) {
	return nil, errors.New("not implemented")
}

// EndSupportAction is a stub of part A (sprint 4.3): step C writes it.
func (s *server) EndSupportAction(_ context.Context, _ adminapi.EndSupportActionInput) (*adminapi.SupportAction, error) {
	return nil, errors.New("not implemented")
}
