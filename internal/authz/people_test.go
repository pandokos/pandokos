package authz

import (
	"os"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/tenant"
)

func user(tid tenant.ID, state tenant.State, roles ...string) Principal {
	p := Principal{Kind: KindUser, ID: "user", Name: "ada", SessionID: "session", TenantID: tid, Roles: roles}
	if !tid.IsZero() {
		p.TenantName, p.TenantState = "acme", state
	}
	return p
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

// TestUserPrincipal is T-805: Decide for a user over the whole catalogue and the three scopes.
func TestUserPrincipal(t *testing.T) {
	a, b := id(1), id(2)
	self := []string{IdentityRead, SessionSelf}

	platformAdmin, _ := RoleByName(RolePlatformAdmin)
	tenantAdmin, _ := RoleByName(RoleTenantAdmin)

	cases := []struct {
		name string
		p    Principal
		tid  tenant.ID // the tenant of the target, for a tenant permission
		held []string  // what the principal holds
	}{
		{"platform administrator", user(tenant.ID{}, "", RolePlatformAdmin), tenant.ID{}, append(append([]string{}, platformAdmin.Permissions...), self...)},
		{"user without roles on the platform", user(tenant.ID{}, ""), tenant.ID{}, self},
		{"tenant administrator", user(a, tenant.StateActive, RoleTenantAdmin), a, append(append([]string{}, tenantAdmin.Permissions...), self...)},
		{"member without roles", user(a, tenant.StateActive), a, self},
		{"role of the other scope on a platform user", user(tenant.ID{}, "", RoleTenantAdmin), tenant.ID{}, self},
		{"role of the other scope on a member", user(a, tenant.StateActive, RolePlatformAdmin), a, self},
		{"unknown role", user(a, tenant.StateActive, "ghost"), a, self},
	}
	for _, c := range cases {
		if got := c.p.Effective(); len(got) != len(dedupe(c.held)) {
			t.Errorf("%s: Effective %v, want %v", c.name, got, dedupe(c.held))
		}
		for _, perm := range Catalogue() {
			want := contains(c.held, perm.Name)
			got := Decide(c.p, perm.Name, Target{TenantID: c.tid})
			if perm.Scope == ScopeTenant && c.p.TenantID.IsZero() {
				// A user without a tenant holds no tenant permission: whatever the reason, not allowed.
				if got == Allow {
					t.Errorf("%s: %s allowed to a principal without a tenant", c.name, perm.Name)
				}
				continue
			}
			if want && got != Allow {
				t.Errorf("%s: %s: decision %d, want Allow", c.name, perm.Name, got)
			}
			if !want && got == Allow {
				t.Errorf("%s: %s: allowed", c.name, perm.Name)
			}
		}
	}

	// The tenant rule: another tenant's resources are not found, with or without the permission.
	admin := user(a, tenant.StateActive, RoleTenantAdmin)
	if got := Decide(admin, ServiceAccountRead, Target{TenantID: b}); got != DenyNotFound {
		t.Errorf("tenant administrator on another tenant: %d, want DenyNotFound", got)
	}
	if got := Decide(user(a, tenant.StateActive), ServiceAccountRead, Target{TenantID: b}); got != DenyNotFound {
		t.Errorf("member without roles on another tenant: %d, want DenyNotFound", got)
	}
	if got := Decide(admin, MemberWrite, Target{}); got != Deny {
		t.Errorf("a tenant permission without a target: %d, want Deny", got)
	}
	// The state rule applies as to a service account.
	for _, s := range []tenant.State{tenant.StateProvisioning, tenant.StateSuspended, tenant.StateDeleting, tenant.StateDeleted} {
		if got := Decide(user(a, s, RoleTenantAdmin), MemberRead, Target{TenantID: a}); got != DenyTenantState {
			t.Errorf("tenant in state %s: %d, want DenyTenantState", s, got)
		}
	}
	// The platform scope is not a tenant: a user with a tenant never holds a platform permission.
	if got := Decide(user(a, tenant.StateActive, RolePlatformAdmin, RoleTenantAdmin), UserWrite, Target{}); got != Deny {
		t.Errorf("platform permission for a member: %d, want Deny", got)
	}
	// A principal that is a user of kind user only: the Kind decides.
	if got := Decide(Principal{Kind: Kind("user "), Roles: []string{RolePlatformAdmin}}, UserRead, Target{}); got == Allow {
		t.Error("a made-up kind was allowed")
	}

	// session:self is refused for keys and tokens: they have no session.
	for name, p := range map[string]Principal{
		"platform key":    {Kind: KindPlatform, ID: "k", Roles: []string{RolePlatformAdmin}},
		"service account": sa(a, tenant.StateActive, []string{RoleTenantAdmin, RoleInference}, nil),
		"token":           {Kind: KindServiceAccount, ID: "sa", IssuerID: "issuer", Subject: "subject", TenantID: a, TenantState: tenant.StateActive, Roles: []string{RoleTenantAdmin}},
	} {
		if got := Decide(p, SessionSelf, Target{}); got != Deny {
			t.Errorf("%s: session:self decided %d, want Deny", name, got)
		}
		if contains(p.Effective(), SessionSelf) {
			t.Errorf("%s: Effective holds session:self", name)
		}
	}

	// Keys and tokens decide as before: the matrix of sprint 1.3 for service accounts and platform keys.
	platform := Principal{Kind: KindPlatform, ID: "p", Roles: []string{RolePlatformAdmin}}
	if Decide(platform, UserWrite, Target{}) != Allow || Decide(platform, MemberWrite, Target{TenantID: a}) != Deny {
		t.Error("a platform key: user:write is allowed, member:write is not")
	}
	if Decide(sa(a, tenant.StateActive, []string{RoleTenantAdmin}, nil), MemberWrite, Target{TenantID: a}) != Allow {
		t.Error("a service account with tenant-admin holds member:write")
	}
	if Decide(sa(a, tenant.StateActive, []string{RoleInference}, nil), MemberRead, Target{TenantID: a}) != Deny {
		t.Error("a service account with inference holds member:read")
	}
	if got := Decide(sa(a, tenant.StateActive, []string{RoleTenantAdmin}, nil), UserRead, Target{}); got != Deny {
		t.Errorf("a service account holds user:read: %d", got)
	}
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// TestPeoplePermissions is T-806.
func TestPeoplePermissions(t *testing.T) {
	want := map[string]Scope{UserRead: ScopePlatform, UserWrite: ScopePlatform, MemberRead: ScopeTenant, MemberWrite: ScopeTenant, SessionSelf: ScopeSelf}
	for name, scope := range want {
		p, ok := Lookup(name)
		if !ok || p.Scope != scope || p.Description == "" {
			t.Errorf("%s: %+v %v, want scope %s", name, p, ok, scope)
		}
	}
	pa, _ := RoleByName(RolePlatformAdmin)
	ta, _ := RoleByName(RoleTenantAdmin)
	inf, _ := RoleByName(RoleInference)
	if pa.Scope != ScopePlatform || ta.Scope != ScopeTenant || inf.Scope != ScopeTenant {
		t.Errorf("role scopes: %s %s %s", pa.Scope, ta.Scope, inf.Scope)
	}
	for _, perm := range []string{UserRead, UserWrite} {
		if !contains(pa.Permissions, perm) || contains(ta.Permissions, perm) || contains(inf.Permissions, perm) {
			t.Errorf("%s: platform-admin only", perm)
		}
	}
	for _, perm := range []string{MemberRead, MemberWrite} {
		if !contains(ta.Permissions, perm) || contains(pa.Permissions, perm) || contains(inf.Permissions, perm) {
			t.Errorf("%s: tenant-admin only", perm)
		}
	}
	if contains(pa.Permissions, SessionSelf) || contains(ta.Permissions, SessionSelf) {
		t.Error("session:self belongs to no role: every user holds it")
	}

	a := id(1)
	platform := Principal{Kind: KindPlatform, ID: "p", Roles: []string{RolePlatformAdmin}}
	adminUser := user(tenant.ID{}, "", RolePlatformAdmin)
	tenantAdmin := user(a, tenant.StateActive, RoleTenantAdmin)
	saAdmin := sa(a, tenant.StateActive, []string{RoleTenantAdmin}, nil)
	withoutOne := func(p Principal, perm string) Principal {
		for _, e := range p.Effective() {
			if e != perm {
				p.Scopes = append(p.Scopes, e)
			}
		}
		if p.Scopes == nil {
			p.Scopes = []string{}
		}
		return p
	}

	// Changed in sprint 4.3 (the rule against escalation): Assignable is gone, and what it decided about
	// the permissions of a role is CanGrant's (T-850); whether a role can be assigned at all (it exists,
	// has the scope, a person can hold it) is decided where it is assigned (T-866).
	for name, c := range map[string]struct {
		p    Principal
		role string
		want bool
	}{
		"platform key grants platform-admin":              {platform, RolePlatformAdmin, true},
		"platform administrator grants platform-admin":    {adminUser, RolePlatformAdmin, true},
		"tenant administrator grants tenant-admin":        {tenantAdmin, RoleTenantAdmin, true},
		"service account grants tenant-admin":             {saAdmin, RoleTenantAdmin, true},
		"tenant administrator grants platform-admin":      {tenantAdmin, RolePlatformAdmin, false},
		"platform key grants tenant-admin":                {platform, RoleTenantAdmin, false},
		"platform key without user:write":                 {withoutOne(platform, UserWrite), RolePlatformAdmin, false},
		"platform administrator without user:read":        {withoutOne(adminUser, UserRead), RolePlatformAdmin, false},
		"platform administrator without tenant:suspend":   {withoutOne(adminUser, TenantSuspend), RolePlatformAdmin, false},
		"tenant administrator without member:write":       {withoutOne(tenantAdmin, MemberWrite), RoleTenantAdmin, false},
		"tenant administrator without virtual_key:revoke": {withoutOne(tenantAdmin, VirtualKeyRevoke), RoleTenantAdmin, false},
		"a principal with no role":                        {user(a, tenant.StateActive), RoleTenantAdmin, false},
		"a principal that is nobody":                      {Principal{}, RolePlatformAdmin, false},
	} {
		role, _ := RoleByName(c.role)
		if got := c.p.CanGrant(role.Permissions); got != c.want {
			t.Errorf("%s: CanGrant of %s = %v, want %v", name, c.role, got, c.want)
		}
	}
}

// TestPermissionsReferenceListsPeople is the part of T-806 that reads the reference page.
func TestPermissionsReferenceListsPeople(t *testing.T) {
	b, err := os.ReadFile("../../docs/reference/permissions.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, perm := range []string{UserRead, UserWrite, MemberRead, MemberWrite, SessionSelf} {
		p, _ := Lookup(perm)
		row := "| `" + perm + "` | " + string(p.Scope) + " |"
		if !strings.Contains(doc, row) {
			t.Errorf("permissions.md has no row that starts %q", row)
		}
	}
	if len(Catalogue()) != 62 {
		t.Errorf("the catalogue holds %d permissions, want 62 (the 49 of sprint 4.2, the eight of sprint 4.3, metrics:read of sprint 6.1, the two audit permissions of sprint 6.2 and the two of sprint 6.3)", len(Catalogue()))
	}
}
