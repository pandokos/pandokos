package admin

// The permission catalogue, the built-in roles and the custom roles of a tenant (sprint 4.3).

import (
	"context"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// maxCustomRoles is the number of custom roles a tenant may define.
const maxCustomRoles = 100

// ListPermissions lists the catalogue.
func (s *server) ListPermissions(_ context.Context, _ adminapi.ListPermissionsInput) (*adminapi.PermissionList, error) {
	out := &adminapi.PermissionList{Items: []adminapi.Permission{}}
	for _, p := range authz.Catalogue() {
		out.Items = append(out.Items, adminapi.Permission{
			Name: p.Name, Scope: adminapi.PermissionScope(p.Scope), Description: p.Description, InTenant: p.InTenant, Support: p.Support,
		})
	}
	return out, nil
}

// ListBuiltinRoles lists the roles that the product defines.
func (s *server) ListBuiltinRoles(_ context.Context, _ adminapi.ListBuiltinRolesInput) (*adminapi.BuiltinRoleList, error) {
	out := &adminapi.BuiltinRoleList{Items: []adminapi.BuiltinRole{}}
	for _, r := range authz.Roles() {
		out.Items = append(out.Items, adminapi.BuiltinRole{
			Name: r.Name, Scope: adminapi.PermissionScope(r.Scope), Members: r.Members, Description: r.Description, Permissions: nonNil(r.Permissions),
		})
	}
	return out, nil
}

// roleOut is the API form of a custom role; assignments is nil in a list.
func roleOut(id, tenantID, name, description string, permissions []string, state string, createdAt, updatedAt time.Time, a *adminapi.RoleAssignments) adminapi.Role {
	return adminapi.Role{
		ID: id, TenantID: tenantID, Name: name, Description: description, Permissions: nonNil(permissions), State: adminapi.RoleState(state),
		Assignments: a, CreatedAt: createdAt.UTC(), UpdatedAt: updatedAt.UTC(),
	}
}

func roleOfGet(r queries.GetTenantRoleRow) adminapi.Role {
	return roleOut(r.ID, r.TenantID, r.Name, r.Description, r.Permissions, r.State, r.CreatedAt, r.UpdatedAt,
		&adminapi.RoleAssignments{Members: int(r.Members), ServiceAccounts: int(r.ServiceAccounts)})
}

// checkReasonText refuses a reason that is only white space.
func checkReasonText(reason string) error {
	if strings.TrimSpace(reason) == "" {
		return invalid("reason", "must not be only white space")
	}
	return nil
}

func mapRoleConstraint(err error) error {
	kind, name, ok := store.ConstraintViolation(err)
	if !ok {
		return err
	}
	switch {
	case kind == "unique" && name == "tenant_role_name_key":
		return errConflict
	case kind == "foreign_key" && name == "tenant_role_in_use":
		return errRoleInUse
	case kind == "check" && (name == "tenant_role_name_format" || name == "tenant_role_name_not_builtin"):
		return invalid("name", "is not a name that a custom role may have")
	case kind == "check" && name == "tenant_role_description_format":
		return invalid("description", "must have at most 500 characters and no control character")
	case kind == "check" && name == "tenant_role_permissions_format":
		return invalid("permissions", "must name 1 to 64 distinct tenant permissions")
	}
	return err
}

// reservedRoleName says whether a custom role may not have the name: the name of a built-in role, or
// one of the two prefixes kept for roles that the product adds later.
func reservedRoleName(name string) bool {
	if strings.HasPrefix(name, "platform-") || strings.HasPrefix(name, "pandokos-") {
		return true
	}
	_, builtin := authz.RoleByName(name)
	return builtin
}

// roleEvent is an audit event about a custom role.
func roleEvent(ctx context.Context, action, id, reason string, detail map[string]any) audit.Event {
	return audit.Event{Actor: audit.ActorOf(principalOf(ctx)), Action: action, ResourceType: "role", ResourceID: id,
		Outcome: "success", RequestID: stateOf(ctx).id, Reason: reason, Detail: detail}
}

// ListRoles lists the custom roles of a tenant.
func (s *server) ListRoles(ctx context.Context, in adminapi.ListRolesInput) (*adminapi.RoleList, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	sortBy := string(in.Sort)
	if sortBy == "" {
		sortBy = "created_at"
	}
	after, name, id, err := decodeCursor(in.Cursor, sortBy)
	if err != nil {
		return nil, err
	}
	limit, ask := limitOf(in.Limit)
	var rows []queries.ListTenantRolesRow
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		var err error
		rows, err = queries.New(tx).ListTenantRoles(ctx, queries.ListTenantRolesParams{
			State: string(in.State), After: after, Sort: sortBy, AfterID: afterID(after, id), AfterName: name, PageSize: ask,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.RoleList{Items: []adminapi.Role{}}
	for i, r := range rows {
		if i == limit {
			last := rows[limit-1]
			out.NextCursor = encodeCursor(sortBy, last.Name, last.ID)
			break
		}
		out.Items = append(out.Items, roleOut(r.ID, r.TenantID, r.Name, r.Description, r.Permissions, r.State, r.CreatedAt, r.UpdatedAt, nil))
	}
	return out, nil
}

// CreateRole defines a custom role.
func (s *server) CreateRole(ctx context.Context, in adminapi.CreateRoleInput) (*adminapi.Role, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	b := in.Body
	perms, offender := authz.CheckCustom(b.Permissions)
	if offender != "" {
		return nil, invalid("permissions", "names a permission that is not a tenant permission: "+offender)
	}
	if reservedRoleName(b.Name) {
		return nil, invalid("name", "is the name of a built-in role or starts with a prefix that is kept for roles of the product")
	}
	var out adminapi.Role
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		n, err := q.CountTenantRoles(ctx)
		if err != nil {
			return err
		}
		if n >= maxCustomRoles {
			return errConflict
		}
		row, err := q.CreateTenantRole(ctx, queries.CreateTenantRoleParams{TenantID: tid.String(), Name: b.Name, Description: b.Description, Permissions: perms})
		if err != nil {
			return err
		}
		out = roleOut(row.ID, row.TenantID, row.Name, row.Description, row.Permissions, row.State, row.CreatedAt, row.UpdatedAt,
			&adminapi.RoleAssignments{})
		return audit.RecordTenant(ctx, tx, tid, roleEvent(ctx, "role.create", row.ID, "", map[string]any{"name": row.Name, "permissions": row.Permissions}))
	})
	if err != nil {
		return nil, mapRoleConstraint(err)
	}
	return &out, nil
}

// GetRole reads a custom role with the number of its holders.
func (s *server) GetRole(ctx context.Context, in adminapi.GetRoleInput) (*adminapi.Role, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	var row queries.GetTenantRoleRow
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = queries.New(tx).GetTenantRole(ctx, in.Role)
		return err
	})
	if isNoRowsErr(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	out := roleOfGet(row)
	return &out, nil
}

// UpdateRole changes the description and the permissions of a custom role.
func (s *server) UpdateRole(ctx context.Context, in adminapi.UpdateRoleInput) (*adminapi.Role, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	b := in.Body
	if !b.Description.Set && !b.Permissions.Set {
		return nil, invalid("body", "must change at least one of description and permissions")
	}
	var perms []string
	if b.Permissions.Set {
		var offender string
		if perms, offender = authz.CheckCustom(b.Permissions.Value); offender != "" {
			return nil, invalid("permissions", "names a permission that is not a tenant permission: "+offender)
		}
	}
	if b.Reason.Set {
		if err := checkReasonText(b.Reason.Value); err != nil {
			return nil, err
		}
	}
	var out adminapi.Role
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		cur, err := q.LockTenantRole(ctx, in.Role)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		par := queries.UpdateTenantRoleParams{ID: cur.ID, Description: cur.Description, Permissions: cur.Permissions}
		detail := map[string]any{}
		if b.Description.Set && b.Description.Value != cur.Description {
			par.Description = b.Description.Value
			detail["description"] = change(cur.Description, b.Description.Value)
		}
		if b.Permissions.Set && !sameStrings(perms, cur.Permissions) {
			par.Permissions = perms
			detail["permissions"] = map[string]any{"added": nonNil(difference(perms, cur.Permissions)), "removed": nonNil(difference(cur.Permissions, perms))}
		}
		if len(detail) > 0 {
			if _, err := q.UpdateTenantRole(ctx, par); err != nil {
				return err
			}
		}
		row, err := q.GetTenantRole(ctx, in.Role)
		if err != nil {
			return err
		}
		out = roleOfGet(row)
		if len(detail) == 0 {
			return nil
		}
		return audit.RecordTenant(ctx, tx, tid, roleEvent(ctx, "role.update", cur.ID, b.Reason.Value, detail))
	})
	if err != nil {
		return nil, mapRoleConstraint(err)
	}
	return &out, nil
}

// setRoleState sets the state of a custom role; a role in the state already is answered unchanged.
func (s *server) setRoleState(ctx context.Context, tenantText, name, reason, to, action string) (*adminapi.Role, error) {
	tid, err := tenantID(tenantText)
	if err != nil {
		return nil, err
	}
	if err := checkReasonText(reason); err != nil {
		return nil, err
	}
	var out adminapi.Role
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		cur, err := q.LockTenantRole(ctx, name)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if cur.State != to {
			if _, err := q.SetTenantRoleState(ctx, queries.SetTenantRoleStateParams{ID: cur.ID, State: to}); err != nil {
				return err
			}
			if err := audit.RecordTenant(ctx, tx, tid, roleEvent(ctx, action, cur.ID, reason, nil)); err != nil {
				return err
			}
		}
		row, err := q.GetTenantRole(ctx, name)
		if err != nil {
			return err
		}
		out = roleOfGet(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeactivateRole makes a custom role give nothing to its holders.
func (s *server) DeactivateRole(ctx context.Context, in adminapi.DeactivateRoleInput) (*adminapi.Role, error) {
	return s.setRoleState(ctx, in.Tenant, in.Role, in.Body.Reason, "inactive", "role.deactivate")
}

// ActivateRole gives a custom role back to its holders.
func (s *server) ActivateRole(ctx context.Context, in adminapi.ActivateRoleInput) (*adminapi.Role, error) {
	return s.setRoleState(ctx, in.Tenant, in.Role, in.Body.Reason, "active", "role.activate")
}

// DeleteRole deletes a custom role that nobody holds.
func (s *server) DeleteRole(ctx context.Context, in adminapi.DeleteRoleInput) (*adminapi.NoContent, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		cur, err := q.LockTenantRole(ctx, in.Role)
		if isNoRowsErr(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		holders, err := q.CountRoleHolders(ctx, cur.Name)
		if err != nil {
			return err
		}
		if holders.Members+holders.ServiceAccounts > 0 {
			return errRoleInUse
		}
		if _, err := q.DeleteTenantRole(ctx, cur.ID); err != nil {
			return err
		}
		return audit.RecordTenant(ctx, tx, tid, roleEvent(ctx, "role.delete", cur.ID, "", map[string]any{"permissions": cur.Permissions}))
	})
	if err != nil {
		return nil, mapRoleConstraint(err)
	}
	return &adminapi.NoContent{}, nil
}
