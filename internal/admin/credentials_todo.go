package admin

import (
	"context"
	"errors"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// errNotImplemented is returned by the credential operations until step C7 of sprint 2.1
// replaces this file with the real handlers.
var errNotImplemented = errors.New("not implemented")

// ListTenantCredentials is a placeholder removed in C7.
func (s *server) ListTenantCredentials(_ context.Context, _ adminapi.ListTenantCredentialsInput) (*adminapi.CredentialList, error) {
	return nil, errNotImplemented
}

// GetTenantCredential is a placeholder removed in C7.
func (s *server) GetTenantCredential(_ context.Context, _ adminapi.GetTenantCredentialInput) (*adminapi.Credential, error) {
	return nil, errNotImplemented
}

// DisableTenantCredential is a placeholder removed in C7.
func (s *server) DisableTenantCredential(_ context.Context, _ adminapi.DisableTenantCredentialInput) (*adminapi.Credential, error) {
	return nil, errNotImplemented
}

// RevokeTenantCredential is a placeholder removed in C7.
func (s *server) RevokeTenantCredential(_ context.Context, _ adminapi.RevokeTenantCredentialInput) (*adminapi.Credential, error) {
	return nil, errNotImplemented
}

// ListCredentials is a placeholder removed in C7.
func (s *server) ListCredentials(_ context.Context, _ adminapi.ListCredentialsInput) (*adminapi.CredentialList, error) {
	return nil, errNotImplemented
}

// CreateCredential is a placeholder removed in C7.
func (s *server) CreateCredential(_ context.Context, _ adminapi.CreateCredentialInput) (*adminapi.Credential, error) {
	return nil, errNotImplemented
}

// GetCredential is a placeholder removed in C7.
func (s *server) GetCredential(_ context.Context, _ adminapi.GetCredentialInput) (*adminapi.Credential, error) {
	return nil, errNotImplemented
}

// UpdateCredential is a placeholder removed in C7.
func (s *server) UpdateCredential(_ context.Context, _ adminapi.UpdateCredentialInput) (*adminapi.Credential, error) {
	return nil, errNotImplemented
}

// DeleteCredential is a placeholder removed in C7.
func (s *server) DeleteCredential(_ context.Context, _ adminapi.DeleteCredentialInput) (*adminapi.Credential, error) {
	return nil, errNotImplemented
}

// DisableCredential is a placeholder removed in C7.
func (s *server) DisableCredential(_ context.Context, _ adminapi.DisableCredentialInput) (*adminapi.Credential, error) {
	return nil, errNotImplemented
}

// EnableCredential is a placeholder removed in C7.
func (s *server) EnableCredential(_ context.Context, _ adminapi.EnableCredentialInput) (*adminapi.Credential, error) {
	return nil, errNotImplemented
}

// RevokeCredential is a placeholder removed in C7.
func (s *server) RevokeCredential(_ context.Context, _ adminapi.RevokeCredentialInput) (*adminapi.Credential, error) {
	return nil, errNotImplemented
}

// ReplaceCredentialSecret is a placeholder removed in C7.
func (s *server) ReplaceCredentialSecret(_ context.Context, _ adminapi.ReplaceCredentialSecretInput) (*adminapi.Credential, error) {
	return nil, errNotImplemented
}

// SetDefaultCredential is a placeholder removed in C7.
func (s *server) SetDefaultCredential(_ context.Context, _ adminapi.SetDefaultCredentialInput) (*adminapi.Credential, error) {
	return nil, errNotImplemented
}
