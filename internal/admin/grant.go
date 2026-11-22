package admin

import (
	"context"
	"slices"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// resolveRoles returns, for each name, the permissions of the role: a built-in tenant role (one a
// person can hold, when member) or an active custom role of the tenant, whose row is read FOR
// SHARE so that a concurrent deletion waits for the assignment (or the assignment finds no role).
// Any other name is invalid_request on roles. q is the tenant's session transaction.
//
//nolint:unparam // the guard of the next step reads the permissions
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
