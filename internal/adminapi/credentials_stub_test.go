package adminapi_test

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// The credential operations of sprint 2.1 are not exercised by this suite yet: part B of the
// sprint replaces these placeholders with recording methods.

func (f *fake) ListTenantCredentials(_ context.Context, _ adminapi.ListTenantCredentialsInput) (*adminapi.CredentialList, error) {
	return &adminapi.CredentialList{}, nil
}

func (f *fake) GetTenantCredential(_ context.Context, _ adminapi.GetTenantCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fake) DisableTenantCredential(_ context.Context, _ adminapi.DisableTenantCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fake) RevokeTenantCredential(_ context.Context, _ adminapi.RevokeTenantCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fake) ListCredentials(_ context.Context, _ adminapi.ListCredentialsInput) (*adminapi.CredentialList, error) {
	return &adminapi.CredentialList{}, nil
}

func (f *fake) CreateCredential(_ context.Context, _ adminapi.CreateCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fake) GetCredential(_ context.Context, _ adminapi.GetCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fake) UpdateCredential(_ context.Context, _ adminapi.UpdateCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fake) DeleteCredential(_ context.Context, _ adminapi.DeleteCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fake) DisableCredential(_ context.Context, _ adminapi.DisableCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fake) EnableCredential(_ context.Context, _ adminapi.EnableCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fake) RevokeCredential(_ context.Context, _ adminapi.RevokeCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fake) ReplaceCredentialSecret(_ context.Context, _ adminapi.ReplaceCredentialSecretInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fake) SetDefaultCredential(_ context.Context, _ adminapi.SetDefaultCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}
