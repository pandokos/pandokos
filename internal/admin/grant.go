package admin

import (
	"context"
	"slices"
	"sort"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// resolveRoles returns, for each name, the permissions of the role: a built-in tenant role (one a
// person can hold, when member) or an active custom role of the tenant, whose row is read FOR
// SHARE so that a concurrent deletion waits for the assignment (or the assignment finds no role).
// Any other name is invalid_request on roles. q is the tenant's session transaction.
func (s *server) resolveRoles(ctx context.Context, q *queries.Queries, names []string, member bool) (map[string][]string, error) {
	out := make(map[string][]string, len(names))
	var custom []string
	for _, name := range names {
		if r, ok := authz.RoleByName(name); ok {
			if r.Scope != authz.ScopeTenant || (member && !r.Members) {
				return nil, errRoleNotAssignable
			}
			out[name] = r.Permissions
			continue
		}
		if !slices.Contains(custom, name) {
			custom = append(custom, name)
		}
	}
	if len(custom) == 0 {
		return out, nil
	}
	rows, err := q.LockTenantRolesForAssignment(ctx, custom)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.State == "active" {
			out[r.Name] = r.Permissions
		}
	}
	for _, name := range custom {
		if _, ok := out[name]; !ok {
			return nil, errRoleNotAssignable
		}
	}
	return out, nil
}

var errRoleNotAssignable = invalid("roles", "must name built-in tenant roles or active custom roles of the tenant")

// guardGrant is the rule against escalation (sprint 4.3, section 5.4): nobody gives, or takes over,
// a permission they do not hold. Every set must be grantable by p; a refusal is logged with the
// request and the operation only, writes no audit row and is the caller's to return before the
// first write of its transaction.
func (s *server) guardGrant(ctx context.Context, p authz.Principal, sets ...[]string) error {
	for _, set := range sets {
		if !p.CanGrant(set) {
			st := stateOf(ctx)
			s.log.Warn("grant refused", "request_id", st.id, "operation", st.operation)
			return errEscalation
		}
	}
	return nil
}

// rolePermissions returns the permissions of each named role as stored: a built-in role from the
// catalogue, a custom role from its row whatever its state. A name that is neither (a role that is
// gone) has none. The custom rows are read FOR SHARE, as resolveRoles reads them.
func (s *server) rolePermissions(ctx context.Context, q *queries.Queries, names []string) (map[string][]string, error) {
	out := make(map[string][]string, len(names))
	var custom []string
	for _, name := range names {
		if r, ok := authz.RoleByName(name); ok {
			out[name] = r.Permissions
		} else if !slices.Contains(custom, name) {
			custom = append(custom, name)
		}
	}
	if len(custom) == 0 {
		return out, nil
	}
	rows, err := q.LockTenantRolesForAssignment(ctx, custom)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.Name] = r.Permissions
	}
	return out, nil
}

// permissionsOf is the union of the permissions of the roles, sorted.
func permissionsOf(roles []string, byRole map[string][]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range roles {
		for _, perm := range byRole[r] {
			if !seen[perm] {
				seen[perm] = true
				out = append(out, perm)
			}
		}
	}
	sort.Strings(out)
	return out
}

// guardAssignment applies the guard to the change of a set of tenant roles: only the roles that are
// added or removed are conferred or taken, those that stay are not checked. wantPerms are the
// permissions of the roles after the change, as resolveRoles returned them; a removed role that was
// deactivated since keeps its stored permissions, and one that is gone has none.
func (s *server) guardAssignment(ctx context.Context, q *queries.Queries, p authz.Principal, have, want []string, wantPerms map[string][]string) error {
	added, removed := difference(sortedCopy(want), sortedCopy(have)), difference(sortedCopy(have), sortedCopy(want))
	if len(added) == 0 && len(removed) == 0 {
		return nil
	}
	gone, err := s.rolePermissions(ctx, q, removed)
	if err != nil {
		return err
	}
	return s.guardGrant(ctx, p, permissionsOf(added, wantPerms), permissionsOf(removed, gone))
}

// guardHeld applies the guard to every role of an identity (the roles that go with a deletion).
func (s *server) guardHeld(ctx context.Context, q *queries.Queries, p authz.Principal, roles []string) error {
	byRole, err := s.rolePermissions(ctx, q, roles)
	if err != nil {
		return err
	}
	return s.guardGrant(ctx, p, permissionsOf(roles, byRole))
}

// targetPrincipal is the principal that an identity of the tenant would be: a service account (with the
// scope list of a key, nil for none) or a member. What the guard compares is its Effective(), the
// function the gateway uses, so that the two cannot disagree about what a key can do.
func targetPrincipal(kind authz.Kind, tid tenant.ID, roles, granted, scopes []string) authz.Principal {
	return authz.Principal{Kind: kind, TenantID: tid, Roles: roles, Granted: granted, Scopes: scopes}
}
