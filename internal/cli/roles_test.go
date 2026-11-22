package cli_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
)

const (
	aid  = "0192f0f0-0000-7000-8000-000000000005" // a support action
	rid  = "0192f0f0-0000-7000-8000-000000000006" // a role
	uid3 = "0192f0f0-0000-7000-8000-000000000007"
)

// The operations of sprint 4.3 on the fake of this package.
func (f *fakeAPI) ListPermissions(_ context.Context, in adminapi.ListPermissionsInput) (*adminapi.PermissionList, error) {
	return pick[adminapi.PermissionList](f, "listPermissions"), f.rec("listPermissions", in)
}
func (f *fakeAPI) ListBuiltinRoles(_ context.Context, in adminapi.ListBuiltinRolesInput) (*adminapi.BuiltinRoleList, error) {
	return pick[adminapi.BuiltinRoleList](f, "listBuiltinRoles"), f.rec("listBuiltinRoles", in)
}
func (f *fakeAPI) ListRoles(_ context.Context, in adminapi.ListRolesInput) (*adminapi.RoleList, error) {
	return pick[adminapi.RoleList](f, "listRoles"), f.rec("listRoles", in)
}
func (f *fakeAPI) CreateRole(_ context.Context, in adminapi.CreateRoleInput) (*adminapi.Role, error) {
	return pick[adminapi.Role](f, "createRole"), f.rec("createRole", in)
}
func (f *fakeAPI) GetRole(_ context.Context, in adminapi.GetRoleInput) (*adminapi.Role, error) {
	return pick[adminapi.Role](f, "getRole"), f.rec("getRole", in)
}
func (f *fakeAPI) UpdateRole(_ context.Context, in adminapi.UpdateRoleInput) (*adminapi.Role, error) {
	return pick[adminapi.Role](f, "updateRole"), f.rec("updateRole", in)
}
func (f *fakeAPI) DeleteRole(_ context.Context, in adminapi.DeleteRoleInput) (*adminapi.NoContent, error) {
	return &adminapi.NoContent{}, f.rec("deleteRole", in)
}
func (f *fakeAPI) DeactivateRole(_ context.Context, in adminapi.DeactivateRoleInput) (*adminapi.Role, error) {
	return pick[adminapi.Role](f, "deactivateRole"), f.rec("deactivateRole", in)
}
func (f *fakeAPI) ActivateRole(_ context.Context, in adminapi.ActivateRoleInput) (*adminapi.Role, error) {
	return pick[adminapi.Role](f, "activateRole"), f.rec("activateRole", in)
}
func (f *fakeAPI) ListTenantSupportActions(_ context.Context, in adminapi.ListTenantSupportActionsInput) (*adminapi.SupportActionList, error) {
	return pick[adminapi.SupportActionList](f, "listTenantSupportActions"), f.rec("listTenantSupportActions", in)
}
func (f *fakeAPI) OpenSupportAction(_ context.Context, in adminapi.OpenSupportActionInput) (*adminapi.SupportAction, error) {
	return pick[adminapi.SupportAction](f, "openSupportAction"), f.rec("openSupportAction", in)
}
func (f *fakeAPI) GetTenantSupportAction(_ context.Context, in adminapi.GetTenantSupportActionInput) (*adminapi.SupportAction, error) {
	return pick[adminapi.SupportAction](f, "getTenantSupportAction"), f.rec("getTenantSupportAction", in)
}
func (f *fakeAPI) CloseSupportAction(_ context.Context, in adminapi.CloseSupportActionInput) (*adminapi.SupportAction, error) {
	return pick[adminapi.SupportAction](f, "closeSupportAction"), f.rec("closeSupportAction", in)
}
func (f *fakeAPI) ListSupportActions(_ context.Context, in adminapi.ListSupportActionsInput) (*adminapi.SupportActionList, error) {
	return pick[adminapi.SupportActionList](f, "listSupportActions"), f.rec("listSupportActions", in)
}
func (f *fakeAPI) GetSupportAction(_ context.Context, in adminapi.GetSupportActionInput) (*adminapi.SupportAction, error) {
	return pick[adminapi.SupportAction](f, "getSupportAction"), f.rec("getSupportAction", in)
}
func (f *fakeAPI) EndSupportAction(_ context.Context, in adminapi.EndSupportActionInput) (*adminapi.SupportAction, error) {
	return pick[adminapi.SupportAction](f, "endSupportAction"), f.rec("endSupportAction", in)
}

var (
	role = adminapi.Role{ID: rid, TenantID: tid, Name: "reader", Description: "reads", Permissions: []string{"member:read", "role:read"}, State: adminapi.RoleStateActive,
		Assignments: &adminapi.RoleAssignments{Members: 2, ServiceAccounts: 1}, CreatedAt: t0, UpdatedAt: t0}
	action = adminapi.SupportAction{ID: aid, TenantID: tid, Actor: adminapi.SupportActionActor{Type: adminapi.SupportActionActorTypePlatform, ID: pk, Name: "ops"},
		Reason: "recover the tenant", Permissions: []string{"member:write"}, State: adminapi.SupportActionState("open"), OpenedAt: t0, ExpiresAt: t1}
)

// TestRoleAndSupportCommands is T-885: every operation of sprint 4.3 has a command, the flags map
// to the bodies, and --reason is required on the nine operations of section 5.8.
func TestRoleAndSupportCommands(t *testing.T) {
	call := func(t *testing.T, id string, args ...string) (any, string) {
		t.Helper()
		r := newRig(t)
		r.f.out["listPermissions"] = &adminapi.PermissionList{Items: []adminapi.Permission{{Name: "role:read", Scope: adminapi.PermissionScope("tenant"), Description: "Reading roles"}}}
		r.f.out["listBuiltinRoles"] = &adminapi.BuiltinRoleList{Items: []adminapi.BuiltinRole{{Name: "owner", Scope: adminapi.PermissionScope("tenant"), Members: true, Description: "d", Permissions: []string{"role:read"}}}}
		for _, op := range []string{"createRole", "getRole", "updateRole", "deactivateRole", "activateRole"} {
			r.f.out[op] = &role
		}
		r.f.out["listRoles"] = &adminapi.RoleList{Items: []adminapi.Role{role}}
		for _, op := range []string{"openSupportAction", "getTenantSupportAction", "closeSupportAction", "getSupportAction", "endSupportAction"} {
			r.f.out[op] = &action
		}
		r.f.out["listTenantSupportActions"] = &adminapi.SupportActionList{Items: []adminapi.SupportAction{action}}
		r.f.out["listSupportActions"] = &adminapi.SupportActionList{Items: []adminapi.SupportAction{action}}
		out, _ := mustRun(t, r, args...)
		return r.f.only(t, id), out
	}
	check := func(t *testing.T, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("the API received\n %+v\nwant\n %+v", got, want)
		}
	}

	t.Run("catalogue", func(t *testing.T) {
		got, out := call(t, "listPermissions", "permission", "list")
		check(t, got, adminapi.ListPermissionsInput{})
		if !strings.Contains(out, "role:read") {
			t.Errorf("the catalogue is not printed:\n%s", out)
		}
		got, out = call(t, "listBuiltinRoles", "role", "builtin")
		check(t, got, adminapi.ListBuiltinRolesInput{})
		if !strings.Contains(out, "owner") {
			t.Errorf("the built-in roles are not printed:\n%s", out)
		}
	})
	t.Run("role", func(t *testing.T) {
		got, out := call(t, "listRoles", "role", "list", "--tenant", tid, "--state", "inactive", "--sort", "-name", "--limit", "5")
		check(t, got, adminapi.ListRolesInput{Tenant: tid, Limit: 5, Sort: adminapi.NamedSortMinusName, State: adminapi.RoleStateInactive})
		if !strings.Contains(out, "reader") {
			t.Errorf("the roles are not printed:\n%s", out)
		}
		got, out = call(t, "createRole", "role", "create", "--tenant", tid, "--description", "reads", "--permission", "member:read", "--permission", "role:read", "reader")
		check(t, got, adminapi.CreateRoleInput{Tenant: tid, Body: adminapi.RoleCreate{Name: "reader", Description: "reads", Permissions: []string{"member:read", "role:read"}}})
		if !lineWith(out, "name: reader") {
			t.Errorf("the role is printed as name: value lines:\n%s", out)
		}
		got, out = call(t, "getRole", "role", "get", "--tenant", tid, "reader")
		check(t, got, adminapi.GetRoleInput{Tenant: tid, Role: "reader"})
		if !lineWith(out, "name: reader") {
			t.Errorf("get:\n%s", out)
		}
		got, _ = call(t, "updateRole", "role", "update", "--tenant", tid, "--description", "reads more", "--permission", "member:read", "--reason", "narrowed", "reader")
		check(t, got, adminapi.UpdateRoleInput{Tenant: tid, Role: "reader", Body: adminapi.RoleUpdate{
			Description: adminapi.Some("reads more"), Permissions: adminapi.Some([]string{"member:read"}), Reason: adminapi.Some("narrowed")}})
		// What is not given is not sent.
		got, _ = call(t, "updateRole", "role", "update", "--tenant", tid, "--description", "only this", "reader")
		check(t, got, adminapi.UpdateRoleInput{Tenant: tid, Role: "reader", Body: adminapi.RoleUpdate{Description: adminapi.Some("only this")}})
		got, _ = call(t, "deleteRole", "role", "delete", "--tenant", tid, "reader")
		check(t, got, adminapi.DeleteRoleInput{Tenant: tid, Role: "reader"})
		for verb, id := range map[string]string{"deactivate": "deactivateRole", "activate": "activateRole"} {
			got, _ = call(t, id, "role", verb, "--tenant", tid, "--reason", "reviewed by the owner", "reader")
			if in := reflect.ValueOf(got); in.FieldByName("Tenant").String() != tid || in.FieldByName("Role").String() != "reader" || bodyReason(got) != "reviewed by the owner" {
				t.Errorf("%s: %+v", verb, got)
			}
		}
	})
	t.Run("tenant-support", func(t *testing.T) {
		got, out := call(t, "openSupportAction", "tenant-support", "open", "--tenant", tid, "--permission", "member:write", "--permission", "service_account:read",
			"--duration", "30m", "--reason", "recover the tenant")
		check(t, got, adminapi.OpenSupportActionInput{Tenant: tid, Body: adminapi.SupportActionOpen{Reason: "recover the tenant", Permissions: []string{"member:write", "service_account:read"}, DurationSeconds: 1800}})
		if !lineWith(out, "id: "+aid) {
			t.Errorf("the action is printed as name: value lines:\n%s", out)
		}
		got, _ = call(t, "openSupportAction", "tenant-support", "open", "--tenant", tid, "--permission", "member:read", "--reason", "default duration")
		check(t, got, adminapi.OpenSupportActionInput{Tenant: tid, Body: adminapi.SupportActionOpen{Reason: "default duration", Permissions: []string{"member:read"}}})
		got, _ = call(t, "listTenantSupportActions", "tenant-support", "list", "--tenant", tid, "--state", "open", "--sort", "opened_at", "--limit", "4")
		check(t, got, adminapi.ListTenantSupportActionsInput{Tenant: tid, Limit: 4, Sort: adminapi.SupportActionSort("opened_at"), State: adminapi.SupportActionFilterOpen})
		got, _ = call(t, "getTenantSupportAction", "tenant-support", "get", "--tenant", tid, aid)
		check(t, got, adminapi.GetTenantSupportActionInput{Tenant: tid, Action: aid})
		got, _ = call(t, "closeSupportAction", "tenant-support", "close", "--tenant", tid, "--reason", "done", aid)
		check(t, got, adminapi.CloseSupportActionInput{Tenant: tid, Action: aid, Body: adminapi.Reason{Reason: "done"}})
		got, _ = call(t, "closeSupportAction", "tenant-support", "close", "--tenant", tid, aid)
		check(t, got, adminapi.CloseSupportActionInput{Tenant: tid, Action: aid})
	})
	t.Run("support-action", func(t *testing.T) {
		got, _ := call(t, "listSupportActions", "support-action", "list", "--tenant", tid, "--state", "closed", "--sort", "-opened_at", "--limit", "9")
		check(t, got, adminapi.ListSupportActionsInput{Tenant: tid, Limit: 9, Sort: adminapi.SupportActionSort("-opened_at"), State: adminapi.SupportActionFilterClosed})
		got, _ = call(t, "getSupportAction", "support-action", "get", "--tenant", tid, aid)
		check(t, got, adminapi.GetSupportActionInput{Tenant: tid, Action: aid})
		got, _ = call(t, "endSupportAction", "support-action", "end", "--tenant", tid, "--reason", "not needed", aid)
		check(t, got, adminapi.EndSupportActionInput{Tenant: tid, Action: aid, Body: adminapi.Reason{Reason: "not needed"}})
	})
	t.Run("platform-key create --role", func(t *testing.T) {
		got, _ := call(t, "createPlatformKey", "platform-key", "create", "--role", "platform-auditor", "--description", "inventory")
		in, ok := got.(adminapi.CreatePlatformKeyInput)
		if !ok || in.Body.Role != "platform-auditor" || in.Body.Description != "inventory" {
			t.Errorf("createPlatformKey: %+v", got)
		}
		got, _ = call(t, "createPlatformKey", "platform-key", "create", "--description", "default role")
		if in := got.(adminapi.CreatePlatformKeyInput); in.Body.Role != "" {
			t.Errorf("a key made without --role sends the role %q: the server's default decides", in.Body.Role)
		}
	})

	t.Run("--reason is a required flag on the nine operations", func(t *testing.T) {
		nine := []struct {
			op   string
			args []string
		}{
			{"suspendTenant", []string{"tenant", "suspend", tid}},
			{"reactivateTenant", []string{"tenant", "reactivate", tid}},
			{"disableTenantCredential", []string{"tenant-credential", "disable", "--tenant", tid, cid}},
			{"revokeTenantCredential", []string{"tenant-credential", "revoke", "--tenant", tid, cid}},
			{"openSupportAction", []string{"tenant-support", "open", "--tenant", tid, "--permission", "member:read"}},
			{"resetUserPassword", []string{"platform-user", "reset-password", uid}},
			{"resetMemberPassword", []string{"member", "reset-password", "--tenant", tid, uid2}},
			{"deactivateRole", []string{"role", "deactivate", "--tenant", tid, "reader"}},
			{"activateRole", []string{"role", "activate", "--tenant", tid, "reader"}},
		}
		for _, c := range nine {
			r := newRig(t)
			code, _, errs := r.run(t, c.args...)
			if code == 0 || !strings.Contains(errs, "--reason") {
				t.Errorf("%s without --reason: exit %d, stderr %q, want a refusal that names --reason", c.op, code, errs)
			}
			if n := len(r.f.calls(c.op)); n != 0 {
				t.Errorf("%s without --reason reached the API %d times", c.op, n)
			}
			// With the flag the call is made and carries the reason.
			r = newRig(t)
			r.f.out["suspendTenant"], r.f.out["reactivateTenant"] = &tenant, &tenant
			r.f.out["disableTenantCredential"], r.f.out["revokeTenantCredential"] = &cred, &cred
			r.f.out["resetUserPassword"] = &adminapi.PasswordReset{UserID: uid, Password: "zyxwvutsrqponmlkjihgfedc", ExpiresAt: t1}
			r.f.out["resetMemberPassword"] = &adminapi.PasswordReset{UserID: uid2, Password: "zyxwvutsrqponmlkjihgfedc", ExpiresAt: t1}
			r.f.out["openSupportAction"] = &action
			r.f.out["deactivateRole"], r.f.out["activateRole"] = &role, &role
			args := append(append([]string(nil), c.args...), "--reason", "because it is required")
			if code, out, errs := r.run(t, args...); code != 0 {
				t.Errorf("%s with --reason: exit %d\n%s\n%s", c.op, code, out, errs)
				continue
			}
			if got := r.f.only(t, c.op); bodyReason(got) != "because it is required" {
				t.Errorf("%s: the reason did not reach the body: %+v", c.op, got)
			}
		}
	})
	t.Run("a role or a support command without a tenant", func(t *testing.T) {
		for _, args := range [][]string{{"role", "list"}, {"role", "get", "reader"}, {"support-action", "list"}, {"tenant-support", "list"}} {
			r := newRig(t)
			if code, _, errs := r.run(t, args...); code == 0 || !strings.Contains(errs, "--tenant") {
				t.Errorf("%v: exit %d, stderr %q, want a refusal that names --tenant", args, code, errs)
			}
		}
	})
}
