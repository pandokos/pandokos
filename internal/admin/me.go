package admin

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/pandokos/pandokos/internal/account"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/session"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
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

// GetMe is built from the principal; for a key it reads nothing, for a user it reads the user's
// tenants and its roles in each (section 5.3).
func (s *server) GetMe(ctx context.Context, _ adminapi.GetMeInput) (*adminapi.Me, error) {
	p := principalOf(ctx)
	m := &adminapi.Me{
		PrincipalType: adminapi.PrincipalType(p.Kind), ID: p.ID, Name: p.Name, KeyID: p.KeyID, Bootstrap: p.Bootstrap,
		Roles: nonNil(append([]string(nil), p.Roles...)), Permissions: nonNil(p.Effective()),
	}
	if p.Kind == authz.KindServiceAccount {
		m.Tenant = &adminapi.TenantRef{ID: p.TenantID.String(), Name: p.TenantName, State: adminapi.TenantState(p.TenantState)}
	}
	if p.Kind != authz.KindUser {
		return m, nil
	}
	st := stateOf(ctx)
	if st.session == nil || st.user == nil {
		panic("admin: a user principal without a session")
	}
	m.MustChangePassword = p.MustChangePassword
	m.Session = s.sessionBody(*st.session, *st.user, true)
	var ids []string
	if err := s.db.Platform(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		ids, err = queries.New(tx).UserTenants(ctx, p.ID)
		return err
	}); err != nil {
		return nil, err
	}
	m.Memberships = []adminapi.Membership{}
	for _, raw := range ids {
		tid, err := tenant.ParseID(raw)
		if err != nil {
			return nil, err
		}
		var row queries.GetMemberPrincipalRow
		err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
			var err error
			row, err = queries.New(tx).GetMemberPrincipal(ctx, p.ID)
			return err
		})
		if errors.Is(err, store.ErrNoRows) {
			continue // removed since the first read
		}
		if err != nil {
			return nil, err
		}
		m.Memberships = append(m.Memberships, adminapi.Membership{
			Tenant: adminapi.TenantRef{ID: tid.String(), Name: row.TenantName, State: adminapi.TenantState(row.TenantState)},
			Roles:  nonNil(row.Roles),
		})
	}
	sort.Slice(m.Memberships, func(i, j int) bool { return m.Memberships[i].Tenant.Name < m.Memberships[j].Tenant.Name })
	return m, nil
}

// sessionBody is the API form of a session; current says that it is the session of the request.
func (s *server) sessionBody(sess session.Session, u account.User, current bool) *adminapi.Session {
	idle := 0
	if s.sessions != nil {
		idle = int(s.sessions.IdleTimeout() / time.Second)
	}
	return &adminapi.Session{
		ID: sess.ID, Method: adminapi.SignInMethod(sess.Method), MustChangePassword: u.MustChange, Current: current,
		User:      adminapi.SessionUser{ID: u.ID, SignIn: adminapi.SignInMethod(u.SignIn), Username: u.Username, DisplayName: u.DisplayName},
		CreatedAt: sess.CreatedAt, LastUsedAt: sess.LastUsedAt, ExpiresAt: sess.ExpiresAt, IdleTimeoutSeconds: idle,
	}
}
