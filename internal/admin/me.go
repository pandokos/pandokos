package admin

import (
	"context"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/tenant"
)

// principalOf returns the principal of the request. The router cannot call a handler without the
// Before hook, so a missing principal is a programming error.
func principalOf(ctx context.Context) authz.Principal {
	p, ok := authz.FromContext(ctx)
	if !ok {
		panic("admin: a handler was called without a principal")
	}
	return p
}

func tenantID(s string) (tenant.ID, error) {
	id, err := tenant.ParseID(s)
	if err != nil {
		return tenant.ID{}, errNotFound
	}
	return id, nil
}

// GetMe is built from the principal; it reads nothing.
func (s *server) GetMe(ctx context.Context, _ adminapi.GetMeInput) (*adminapi.Me, error) {
	p := principalOf(ctx)
	m := &adminapi.Me{
		PrincipalType: adminapi.PrincipalType(p.Kind), ID: p.ID, Name: p.Name, KeyID: p.KeyID, Bootstrap: p.Bootstrap,
		Roles: nonNil(append([]string(nil), p.Roles...)), Permissions: nonNil(p.Effective()),
	}
	if p.Kind == authz.KindServiceAccount {
		m.Tenant = &adminapi.TenantRef{ID: p.TenantID.String(), Name: p.TenantName, State: adminapi.TenantState(p.TenantState)}
	}
	return m, nil
}
