package cli

import (
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// requireReason refuses an empty reason for a command whose operation requires one (section 5.8):
// before any call is made.
func requireReason(command, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return usagef("%s needs --reason", command)
	}
	return nil
}

func init() { commands = append(commands, roleCommands...) }

// roleCommands are the commands of sprint 4.3: the permission catalogue, custom roles and the
// support actions of the platform.
var roleCommands = []command{
	{[]string{"permission", "list"}, "listPermissions", func(a *app, args []string) error {
		if _, err := a.begin(a.flags("permission list"), args, 0); err != nil {
			return err
		}
		out, err := a.client.ListPermissions(a.ctx, adminapi.ListPermissionsInput{})
		if err != nil {
			return err
		}
		if a.output == "json" {
			return a.printJSON(out)
		}
		a.printTable(toAny(out.Items))
		return nil
	}},

	{[]string{"role", "builtin"}, "listBuiltinRoles", func(a *app, args []string) error {
		if _, err := a.begin(a.flags("role builtin"), args, 0); err != nil {
			return err
		}
		out, err := a.client.ListBuiltinRoles(a.ctx, adminapi.ListBuiltinRolesInput{})
		if err != nil {
			return err
		}
		if a.output == "json" {
			return a.printJSON(out)
		}
		a.printTable(toAny(out.Items))
		return nil
	}},

	{[]string{"role", "list"}, "listRoles", func(a *app, args []string) error {
		fs := a.flags("role list")
		var l listFlags
		l.register(fs)
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListRoles(a.ctx, adminapi.ListRolesInput{Tenant: tid, State: adminapi.RoleState(l.state), Sort: adminapi.NamedSort(l.sort), Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, func(items []any) any { return map[string]any{"items": items} })
	}},

	{[]string{"role", "create"}, "createRole", func(a *app, args []string) error {
		fs := a.flags("role create")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		desc := fs.String("description", "", "description")
		var perms multi
		fs.Var(&perms, "permission", "a permission of the role (repeatable)")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		r, err := a.client.CreateRole(a.ctx, adminapi.CreateRoleInput{Tenant: tid, Body: adminapi.RoleCreate{Name: pos[0], Description: *desc, Permissions: []string(perms)}})
		if err != nil {
			return err
		}
		return a.printResource(r)
	}},

	{[]string{"role", "get"}, "getRole", func(a *app, args []string) error {
		fs := a.flags("role get")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		r, err := a.client.GetRole(a.ctx, adminapi.GetRoleInput{Tenant: tid, Role: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(r)
	}},

	{[]string{"role", "update"}, "updateRole", func(a *app, args []string) error {
		fs := a.flags("role update")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		desc := fs.String("description", "", "new description")
		reason := fs.String("reason", "", "recorded in the audit log")
		var perms multi
		fs.Var(&perms, "permission", "a permission of the role, repeatable; replaces the whole set")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		var body adminapi.RoleUpdate
		if given(fs, "description") {
			body.Description = adminapi.Some(*desc)
		}
		if given(fs, "permission") {
			body.Permissions = adminapi.Some([]string(perms))
		}
		body.Reason = nonEmpty(*reason)
		r, err := a.client.UpdateRole(a.ctx, adminapi.UpdateRoleInput{Tenant: tid, Role: pos[0], Body: body})
		if err != nil {
			return err
		}
		return a.printResource(r)
	}},

	{[]string{"role", "deactivate"}, "deactivateRole", func(a *app, args []string) error {
		fs := a.flags("role deactivate")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		reason := fs.String("reason", "", "recorded in the audit log (required)")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		if err := requireReason("role deactivate", *reason); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		r, err := a.client.DeactivateRole(a.ctx, adminapi.DeactivateRoleInput{Tenant: tid, Role: pos[0], Body: adminapi.RequiredReason{Reason: *reason}})
		if err != nil {
			return err
		}
		return a.printResource(r)
	}},

	{[]string{"role", "activate"}, "activateRole", func(a *app, args []string) error {
		fs := a.flags("role activate")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		reason := fs.String("reason", "", "recorded in the audit log (required)")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		if err := requireReason("role activate", *reason); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		r, err := a.client.ActivateRole(a.ctx, adminapi.ActivateRoleInput{Tenant: tid, Role: pos[0], Body: adminapi.RequiredReason{Reason: *reason}})
		if err != nil {
			return err
		}
		return a.printResource(r)
	}},

	{[]string{"role", "delete"}, "deleteRole", func(a *app, args []string) error {
		fs := a.flags("role delete")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		if _, err := a.client.DeleteRole(a.ctx, adminapi.DeleteRoleInput{Tenant: tid, Role: pos[0]}); err != nil {
			return err
		}
		if a.output == "json" {
			return a.printJSON(map[string]any{})
		}
		return nil
	}},

	{[]string{"tenant-support", "open"}, "openSupportAction", func(a *app, args []string) error {
		fs := a.flags("tenant-support open")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		reason := fs.String("reason", "", "recorded in the audit log of the tenant (required)")
		duration := fs.String("duration", "", "how long the action lasts, e.g. 30m; the default is support.max_duration")
		var perms multi
		fs.Var(&perms, "permission", "a permission of the action (repeatable)")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		if err := requireReason("tenant-support open", *reason); err != nil {
			return err
		}
		seconds := 0
		if *duration != "" {
			d, err := time.ParseDuration(*duration)
			if err != nil || d < time.Second || d%time.Second != 0 {
				return usagef("--duration must be a whole number of seconds, like 30m or 1h")
			}
			seconds = int(d / time.Second)
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		out, err := a.client.OpenSupportAction(a.ctx, adminapi.OpenSupportActionInput{Tenant: tid, Body: adminapi.SupportActionOpen{Reason: *reason, Permissions: []string(perms), DurationSeconds: seconds}})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"tenant-support", "list"}, "listTenantSupportActions", func(a *app, args []string) error {
		fs := a.flags("tenant-support list")
		var l listFlags
		l.register(fs)
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListTenantSupportActions(a.ctx, adminapi.ListTenantSupportActionsInput{Tenant: tid, State: adminapi.SupportActionFilter(l.state), Sort: adminapi.SupportActionSort(l.sort), Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, func(items []any) any { return map[string]any{"items": items} })
	}},

	{[]string{"tenant-support", "get"}, "getTenantSupportAction", func(a *app, args []string) error {
		fs := a.flags("tenant-support get")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		out, err := a.client.GetTenantSupportAction(a.ctx, adminapi.GetTenantSupportActionInput{Tenant: tid, Action: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"tenant-support", "close"}, "closeSupportAction", func(a *app, args []string) error {
		fs := a.flags("tenant-support close")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		reason := fs.String("reason", "", "recorded in the audit log")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		out, err := a.client.CloseSupportAction(a.ctx, adminapi.CloseSupportActionInput{Tenant: tid, Action: pos[0], Body: adminapi.Reason{Reason: *reason}})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"support-action", "list"}, "listSupportActions", func(a *app, args []string) error {
		fs := a.flags("support-action list")
		var l listFlags
		l.register(fs)
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListSupportActions(a.ctx, adminapi.ListSupportActionsInput{Tenant: tid, State: adminapi.SupportActionFilter(l.state), Sort: adminapi.SupportActionSort(l.sort), Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, func(items []any) any { return map[string]any{"items": items} })
	}},

	{[]string{"support-action", "get"}, "getSupportAction", func(a *app, args []string) error {
		fs := a.flags("support-action get")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		out, err := a.client.GetSupportAction(a.ctx, adminapi.GetSupportActionInput{Tenant: tid, Action: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},

	{[]string{"support-action", "end"}, "endSupportAction", func(a *app, args []string) error {
		fs := a.flags("support-action end")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		reason := fs.String("reason", "", "recorded in the audit log")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		out, err := a.client.EndSupportAction(a.ctx, adminapi.EndSupportActionInput{Tenant: tid, Action: pos[0], Body: adminapi.Reason{Reason: *reason}})
		if err != nil {
			return err
		}
		return a.printResource(out)
	}},
}
