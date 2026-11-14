package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// Sprint 2.1, section 5.4: the oversight of a platform administrator. It reads metadata and can
// disable or revoke with a reason; it cannot create, replace, update, delete, enable or choose a
// default, and it never reaches the sealer. Each operation authorizes with the platform
// permission, then runs in the session of the tenant named in the path and verifies that the
// tenant exists; the audit row goes to that tenant's log with the platform principal as actor.

// ListTenantCredentials lists the credentials of any tenant, as metadata.
func (s *server) ListTenantCredentials(ctx context.Context, in adminapi.ListTenantCredentialsInput) (*adminapi.CredentialList, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	return s.listCredentials(ctx, tid, true, listQuery{in.Sort, in.Cursor, in.Limit, string(in.State), in.Provider})
}

// GetTenantCredential reads the metadata of a credential of any tenant.
func (s *server) GetTenantCredential(ctx context.Context, in adminapi.GetTenantCredentialInput) (*adminapi.Credential, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	return s.getCredential(ctx, tid, true, in.Credential)
}

// DisableTenantCredential disables a credential of a tenant, with a reason.
func (s *server) DisableTenantCredential(ctx context.Context, in adminapi.DisableTenantCredentialInput) (*adminapi.Credential, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	return s.changeState(ctx, tid, true, in.Credential, "disable", in.Body.Reason)
}

// RevokeTenantCredential revokes a credential of a tenant, with a reason.
func (s *server) RevokeTenantCredential(ctx context.Context, in adminapi.RevokeTenantCredentialInput) (*adminapi.Credential, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	return s.changeState(ctx, tid, true, in.Credential, "revoke", in.Body.Reason)
}
