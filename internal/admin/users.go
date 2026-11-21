package admin

// Handlers of the operations on users that the platform manages (sprint 4.2). Part A: each one answers with an error,
// which the router turns into a 500; steps C3 to C8 write them.

import (
	"context"
	"errors"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// ListUsers is not implemented yet.
func (s *server) ListUsers(_ context.Context, _ adminapi.ListUsersInput) (*adminapi.UserList, error) {
	return nil, errors.New("not implemented")
}

// CreateUser is not implemented yet.
func (s *server) CreateUser(_ context.Context, _ adminapi.CreateUserInput) (*adminapi.UserCreated, error) {
	return nil, errors.New("not implemented")
}

// GetUser is not implemented yet.
func (s *server) GetUser(_ context.Context, _ adminapi.GetUserInput) (*adminapi.User, error) {
	return nil, errors.New("not implemented")
}

// UpdateUser is not implemented yet.
func (s *server) UpdateUser(_ context.Context, _ adminapi.UpdateUserInput) (*adminapi.User, error) {
	return nil, errors.New("not implemented")
}

// DisableUser is not implemented yet.
func (s *server) DisableUser(_ context.Context, _ adminapi.DisableUserInput) (*adminapi.User, error) {
	return nil, errors.New("not implemented")
}

// EnableUser is not implemented yet.
func (s *server) EnableUser(_ context.Context, _ adminapi.EnableUserInput) (*adminapi.User, error) {
	return nil, errors.New("not implemented")
}

// ResetUserPassword is not implemented yet.
func (s *server) ResetUserPassword(_ context.Context, _ adminapi.ResetUserPasswordInput) (*adminapi.PasswordReset, error) {
	return nil, errors.New("not implemented")
}

// RevokeUserSessions is not implemented yet.
func (s *server) RevokeUserSessions(_ context.Context, _ adminapi.RevokeUserSessionsInput) (*adminapi.SessionsRevoked, error) {
	return nil, errors.New("not implemented")
}

// UnlockUser is not implemented yet.
func (s *server) UnlockUser(_ context.Context, _ adminapi.UnlockUserInput) (*adminapi.User, error) {
	return nil, errors.New("not implemented")
}
