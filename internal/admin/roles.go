package admin

// The permission catalogue, the built-in roles and the custom roles of a tenant (sprint 4.3).

import (
	"context"
	"errors"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// ListPermissions is a stub of part A (sprint 4.3): step C writes it.
func (s *server) ListPermissions(_ context.Context, _ adminapi.ListPermissionsInput) (*adminapi.PermissionList, error) {
	return nil, errors.New("not implemented")
}

// ListBuiltinRoles is a stub of part A (sprint 4.3): step C writes it.
func (s *server) ListBuiltinRoles(_ context.Context, _ adminapi.ListBuiltinRolesInput) (*adminapi.BuiltinRoleList, error) {
	return nil, errors.New("not implemented")
}

// ListRoles is a stub of part A (sprint 4.3): step C writes it.
func (s *server) ListRoles(_ context.Context, _ adminapi.ListRolesInput) (*adminapi.RoleList, error) {
	return nil, errors.New("not implemented")
}

// CreateRole is a stub of part A (sprint 4.3): step C writes it.
func (s *server) CreateRole(_ context.Context, _ adminapi.CreateRoleInput) (*adminapi.Role, error) {
	return nil, errors.New("not implemented")
}

// GetRole is a stub of part A (sprint 4.3): step C writes it.
func (s *server) GetRole(_ context.Context, _ adminapi.GetRoleInput) (*adminapi.Role, error) {
	return nil, errors.New("not implemented")
}

// UpdateRole is a stub of part A (sprint 4.3): step C writes it.
func (s *server) UpdateRole(_ context.Context, _ adminapi.UpdateRoleInput) (*adminapi.Role, error) {
	return nil, errors.New("not implemented")
}

// DeleteRole is a stub of part A (sprint 4.3): step C writes it.
func (s *server) DeleteRole(_ context.Context, _ adminapi.DeleteRoleInput) (*adminapi.NoContent, error) {
	return nil, errors.New("not implemented")
}

// DeactivateRole is a stub of part A (sprint 4.3): step C writes it.
func (s *server) DeactivateRole(_ context.Context, _ adminapi.DeactivateRoleInput) (*adminapi.Role, error) {
	return nil, errors.New("not implemented")
}

// ActivateRole is a stub of part A (sprint 4.3): step C writes it.
func (s *server) ActivateRole(_ context.Context, _ adminapi.ActivateRoleInput) (*adminapi.Role, error) {
	return nil, errors.New("not implemented")
}
