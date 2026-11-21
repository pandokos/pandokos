package admin

// Handlers of the operations on sign-in and the caller's own sessions (sprint 4.2). Part A: each one answers with an error,
// which the router turns into a 500; steps C3 to C8 write them.

import (
	"context"
	"errors"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// GetAuthMethods is not implemented yet.
func (s *server) GetAuthMethods(_ context.Context, _ adminapi.GetAuthMethodsInput) (*adminapi.AuthMethods, error) {
	return nil, errors.New("not implemented")
}

// CompleteOidcSignIn is not implemented yet.
func (s *server) CompleteOidcSignIn(_ context.Context, _ adminapi.CompleteOidcSignInInput) (*adminapi.Redirect, error) {
	return nil, errors.New("not implemented")
}

// StartOidcSignIn is not implemented yet.
func (s *server) StartOidcSignIn(_ context.Context, _ adminapi.StartOidcSignInInput) (*adminapi.Redirect, error) {
	return nil, errors.New("not implemented")
}

// ChangePassword is not implemented yet.
func (s *server) ChangePassword(_ context.Context, _ adminapi.ChangePasswordInput) (*adminapi.Session, error) {
	return nil, errors.New("not implemented")
}

// ListOwnSessions is not implemented yet.
func (s *server) ListOwnSessions(_ context.Context, _ adminapi.ListOwnSessionsInput) (*adminapi.SessionList, error) {
	return nil, errors.New("not implemented")
}

// DeleteOwnSession is not implemented yet.
func (s *server) DeleteOwnSession(_ context.Context, _ adminapi.DeleteOwnSessionInput) (*adminapi.Session, error) {
	return nil, errors.New("not implemented")
}

// SignIn is not implemented yet.
func (s *server) SignIn(_ context.Context, _ adminapi.SignInInput) (*adminapi.Session, error) {
	return nil, errors.New("not implemented")
}

// SignOut is not implemented yet.
func (s *server) SignOut(_ context.Context, _ adminapi.SignOutInput) (*adminapi.SignedOut, error) {
	return nil, errors.New("not implemented")
}
