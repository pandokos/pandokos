package cli_test

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// The credential operations of sprint 2.1 are not exercised by this suite yet: part B of the
// sprint replaces these placeholders with recording methods.

func (f *fakeAPI) ListTenantCredentials(_ context.Context, _ adminapi.ListTenantCredentialsInput) (*adminapi.CredentialList, error) {
	return &adminapi.CredentialList{}, nil
}

func (f *fakeAPI) GetTenantCredential(_ context.Context, _ adminapi.GetTenantCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fakeAPI) DisableTenantCredential(_ context.Context, _ adminapi.DisableTenantCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fakeAPI) RevokeTenantCredential(_ context.Context, _ adminapi.RevokeTenantCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fakeAPI) ListCredentials(_ context.Context, _ adminapi.ListCredentialsInput) (*adminapi.CredentialList, error) {
	return &adminapi.CredentialList{}, nil
}

func (f *fakeAPI) CreateCredential(_ context.Context, _ adminapi.CreateCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fakeAPI) GetCredential(_ context.Context, _ adminapi.GetCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fakeAPI) UpdateCredential(_ context.Context, _ adminapi.UpdateCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fakeAPI) DeleteCredential(_ context.Context, _ adminapi.DeleteCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fakeAPI) DisableCredential(_ context.Context, _ adminapi.DisableCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fakeAPI) EnableCredential(_ context.Context, _ adminapi.EnableCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fakeAPI) RevokeCredential(_ context.Context, _ adminapi.RevokeCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fakeAPI) ReplaceCredentialSecret(_ context.Context, _ adminapi.ReplaceCredentialSecretInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}

func (f *fakeAPI) SetDefaultCredential(_ context.Context, _ adminapi.SetDefaultCredentialInput) (*adminapi.Credential, error) {
	return &adminapi.Credential{}, nil
}
