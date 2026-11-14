package authz

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"sort"
	"testing"

	"github.com/pandokos/pandokos/internal/tenant"
)

func id(b byte) tenant.ID {
	var i tenant.ID
	i[0], i[6], i[8] = b, 0x70, 0x80
	return i
}

func sa(tid tenant.ID, state tenant.State, roles []string, scopes []string) Principal {
	return Principal{Kind: KindServiceAccount, ID: "sa", TenantID: tid, TenantState: state, Roles: roles, Scopes: scopes}
}

// TestCatalogue is T-201 (changed in sprint 2.1: T-309, twenty permissions in order; changed in
// sprint 2.2: T-382, the five catalogue permissions of section 5.9 appended in order).
func TestCatalogue(t *testing.T) {
	want := []struct {
		name  string
		scope Scope
	}{
		{"identity:read", ScopeSelf},
		{"tenant:read", ScopePlatform},
		{"tenant:write", ScopePlatform},
		{"tenant:suspend", ScopePlatform},
		{"platform_key:read", ScopePlatform},
		{"platform_key:write", ScopePlatform},
		{"platform_key:revoke", ScopePlatform},
		{"service_account:read", ScopeTenant},
		{"service_account:write", ScopeTenant},
		{"virtual_key:read", ScopeTenant},
		{"virtual_key:write", ScopeTenant},
		{"virtual_key:revoke", ScopeTenant},
		{"model:invoke", ScopeTenant},
		{"credential:read", ScopeTenant},
		{"credential:write", ScopeTenant},
		{"credential:rotate", ScopeTenant},
		{"credential:delete", ScopeTenant},
		{"credential:use", ScopeTenant},
		{"tenant_credential:read", ScopePlatform},
		{"tenant_credential:revoke", ScopePlatform},
		{"catalog:read", ScopePlatform},
		{"catalog:write", ScopePlatform},
		{"catalog:price", ScopePlatform},
		{"model:read", ScopeTenant},
		{"model:restrict", ScopeTenant},
	}
	got := Catalogue()
	if len(got) != 25 {
		t.Fatalf("%d permissions, want 25", len(got))
	}
	re := regexp.MustCompile(`^[a-z_]+:[a-z_]+$`)
	seen := map[string]bool{}
	for i, p := range got {
		if p.Name != want[i].name || p.Scope != want[i].scope {
			t.Errorf("entry %d is %s/%s, want %s/%s", i, p.Name, p.Scope, want[i].name, want[i].scope)
		}
		if !re.MatchString(p.Name) || p.Description == "" || p.Scope == "" {
			t.Errorf("%+v is incomplete", p)
		}
		if seen[p.Name] {
			t.Errorf("%s is listed twice", p.Name)
		}
		seen[p.Name] = true
		l, ok := Lookup(p.Name)
		if !ok || l != p {
			t.Errorf("Lookup(%s) = %+v, %v", p.Name, l, ok)
		}
	}
	if _, ok := Lookup("nope:nope"); ok {
		t.Error("Lookup found an unknown permission")
	}
	for name, c := range map[string]string{
		"IdentityRead": IdentityRead, "TenantRead": TenantRead, "TenantWrite": TenantWrite, "TenantSuspend": TenantSuspend,
		"PlatformKeyRead": PlatformKeyRead, "PlatformKeyWrite": PlatformKeyWrite, "PlatformKeyRevoke": PlatformKeyRevoke,
		"ServiceAccountRead": ServiceAccountRead, "ServiceAccountWrite": ServiceAccountWrite,
		"VirtualKeyRead": VirtualKeyRead, "VirtualKeyWrite": VirtualKeyWrite, "VirtualKeyRevoke": VirtualKeyRevoke,
		"ModelInvoke":    ModelInvoke,
		"CredentialRead": CredentialRead, "CredentialWrite": CredentialWrite, "CredentialRotate": CredentialRotate,
		"CredentialDelete": CredentialDelete, "CredentialUse": CredentialUse,
		"TenantCredentialRead": TenantCredentialRead, "TenantCredentialRevoke": TenantCredentialRevoke,
		"CatalogRead": CatalogRead, "CatalogWrite": CatalogWrite, "CatalogPrice": CatalogPrice,
		"ModelRead": ModelRead, "ModelRestrict": ModelRestrict,
	} {
		if _, ok := Lookup(c); !ok {
			t.Errorf("constant %s = %q is not in the catalogue", name, c)
		}
	}
}

// TestRoles is T-202 (changed in sprint 2.1: T-309, the three roles with the permissions of
// sprint 2.1, section 5.5; changed in sprint 2.2: T-382, section 5.9: platform-admin gains the
// three platform catalogue permissions, tenant-admin the two tenant ones, inference nothing).
func TestRoles(t *testing.T) {
	platform := []string{IdentityRead, TenantRead, TenantWrite, TenantSuspend, PlatformKeyRead, PlatformKeyWrite, PlatformKeyRevoke,
		TenantCredentialRead, TenantCredentialRevoke, CatalogRead, CatalogWrite, CatalogPrice}
	tenantAll := []string{IdentityRead, ServiceAccountRead, ServiceAccountWrite, VirtualKeyRead, VirtualKeyWrite, VirtualKeyRevoke, ModelInvoke,
		CredentialRead, CredentialWrite, CredentialRotate, CredentialDelete, CredentialUse, ModelRead, ModelRestrict}
	want := []struct {
		name string
		kind Kind
		perm []string
	}{
		{RolePlatformAdmin, KindPlatform, platform},
		{RoleTenantAdmin, KindServiceAccount, tenantAll},
		{RoleInference, KindServiceAccount, []string{ModelInvoke, CredentialUse}},
	}
	roles := Roles()
	if len(roles) != 3 {
		t.Fatalf("%d roles, want 3", len(roles))
	}
	for i, w := range want {
		r := roles[i]
		exp := append([]string(nil), w.perm...)
		sort.Strings(exp)
		if r.Name != w.name || r.Kind != w.kind || !reflect.DeepEqual(r.Permissions, exp) || r.Description == "" {
			t.Errorf("role %d = %+v, want %s %s %v", i, r, w.name, w.kind, exp)
		}
		for _, p := range r.Permissions {
			perm, ok := Lookup(p)
			if !ok {
				t.Errorf("%s: %s is not in the catalogue", r.Name, p)
				continue
			}
			switch {
			case r.Kind == KindPlatform && perm.Scope == ScopeTenant:
				t.Errorf("%s holds the tenant permission %s", r.Name, p)
			case r.Kind == KindServiceAccount && perm.Scope == ScopePlatform:
				t.Errorf("%s holds the platform permission %s", r.Name, p)
			}
		}
		got, ok := RoleByName(w.name)
		if !ok || !reflect.DeepEqual(got, r) {
			t.Errorf("RoleByName(%s) = %+v, %v", w.name, got, ok)
		}
	}
	if _, ok := RoleByName("owner"); ok {
		t.Error("RoleByName found a role that does not exist")
	}
	// The returned slices are copies.
	roles[2].Permissions[0] = "changed:changed"
	roles[0].Name = "changed"
	again := Roles()
	if again[2].Permissions[0] != CredentialUse || again[0].Name != RolePlatformAdmin {
		t.Error("changing a returned slice changed the next call")
	}
}

// TestDecide is T-203.
func TestDecide(t *testing.T) {
	a, b := id(1), id(2)
	admin := []string{RoleTenantAdmin}
	platform := Principal{Kind: KindPlatform, ID: "p", Roles: []string{RolePlatformAdmin}}
	cases := []struct {
		name string
		p    Principal
		perm string
		tgt  Target
		want Decision
	}{
		{"unknown permission", platform, "nope:nope", Target{}, Deny},
		{"unknown permission, tenant admin", sa(a, tenant.StateActive, admin, nil), "tenant:destroy", Target{TenantID: a}, Deny},
		{"platform permission, platform principal", platform, TenantRead, Target{}, Allow},
		{"platform permission, platform principal ignores the target", platform, TenantSuspend, Target{TenantID: b}, Allow},
		{"platform permission, tenant principal", sa(a, tenant.StateActive, admin, nil), TenantRead, Target{}, Deny},
		{"platform permission without the role", Principal{Kind: KindPlatform}, TenantRead, Target{}, Deny},
		{"platform permission with an unknown role", Principal{Kind: KindPlatform, Roles: []string{"root"}}, TenantRead, Target{}, Deny},
		{"tenant permission, platform principal", platform, ServiceAccountRead, Target{TenantID: a}, Deny},
		{"tenant permission, platform principal, no target", platform, ModelInvoke, Target{}, Deny},
		{"zero tenant principal", sa(tenant.ID{}, tenant.StateActive, admin, nil), ServiceAccountRead, Target{TenantID: a}, Deny},
		{"zero tenant principal, self", sa(tenant.ID{}, tenant.StateActive, admin, nil), IdentityRead, Target{}, Deny},
		{"zero target", sa(a, tenant.StateActive, admin, nil), ServiceAccountRead, Target{}, Deny},
		{"other tenant, with the permission", sa(a, tenant.StateActive, admin, nil), ServiceAccountRead, Target{TenantID: b}, DenyNotFound},
		{"other tenant, without the permission", sa(a, tenant.StateActive, []string{RoleInference}, nil), ServiceAccountRead, Target{TenantID: b}, DenyNotFound},
		{"own tenant, with the permission", sa(a, tenant.StateActive, admin, nil), ServiceAccountRead, Target{TenantID: a}, Allow},
		{"own tenant, without the permission", sa(a, tenant.StateActive, []string{RoleInference}, nil), ServiceAccountRead, Target{TenantID: a}, Deny},
		{"inference role invokes", sa(a, tenant.StateActive, []string{RoleInference}, nil), ModelInvoke, Target{TenantID: a}, Allow},
		{"scopes exclude the permission", sa(a, tenant.StateActive, admin, []string{VirtualKeyRead}), ModelInvoke, Target{TenantID: a}, Deny},
		{"scopes include the permission", sa(a, tenant.StateActive, admin, []string{VirtualKeyRead}), VirtualKeyRead, Target{TenantID: a}, Allow},
		{"credential permission, tenant admin", sa(a, tenant.StateActive, admin, nil), CredentialRotate, Target{TenantID: a}, Allow},
		{"credential permission, other tenant", sa(a, tenant.StateActive, admin, nil), CredentialRead, Target{TenantID: b}, DenyNotFound},
		{"credential use, inference", sa(a, tenant.StateActive, []string{RoleInference}, nil), CredentialUse, Target{TenantID: a}, Allow},
		{"credential read, inference", sa(a, tenant.StateActive, []string{RoleInference}, nil), CredentialRead, Target{TenantID: a}, Deny},
		{"credential use, scoped to model:invoke only", sa(a, tenant.StateActive, []string{RoleInference}, []string{ModelInvoke}), CredentialUse, Target{TenantID: a}, Deny},
		{"credential permission, platform principal", platform, CredentialRead, Target{TenantID: a}, Deny},
		{"tenant credential permission, platform principal", platform, TenantCredentialRevoke, Target{}, Allow},
		{"tenant credential permission, tenant admin", sa(a, tenant.StateActive, admin, nil), TenantCredentialRead, Target{}, Deny},
		{"catalogue write, platform principal", platform, CatalogWrite, Target{}, Allow},
		{"catalogue price, platform principal", platform, CatalogPrice, Target{TenantID: a}, Allow},
		{"catalogue read, tenant admin", sa(a, tenant.StateActive, admin, nil), CatalogRead, Target{}, Deny},
		{"catalogue write, tenant admin, own tenant", sa(a, tenant.StateActive, admin, nil), CatalogWrite, Target{TenantID: a}, Deny},
		{"model read, tenant admin", sa(a, tenant.StateActive, admin, nil), ModelRead, Target{TenantID: a}, Allow},
		{"model restrict, tenant admin", sa(a, tenant.StateActive, admin, nil), ModelRestrict, Target{TenantID: a}, Allow},
		{"model restrict, other tenant", sa(a, tenant.StateActive, admin, nil), ModelRestrict, Target{TenantID: b}, DenyNotFound},
		{"model read, inference", sa(a, tenant.StateActive, []string{RoleInference}, nil), ModelRead, Target{TenantID: a}, Deny},
		{"model restrict, scoped to model:read", sa(a, tenant.StateActive, admin, []string{ModelRead}), ModelRestrict, Target{TenantID: a}, Deny},
		{"model read, platform principal", platform, ModelRead, Target{TenantID: a}, Deny},
		{"model restrict, suspended tenant", sa(a, tenant.StateSuspended, admin, nil), ModelRestrict, Target{TenantID: a}, DenyTenantState},
		{"self permission", sa(a, tenant.StateActive, admin, nil), IdentityRead, Target{}, Allow},
		{"self permission without the role", sa(a, tenant.StateActive, nil, nil), IdentityRead, Target{}, Deny},
		{"self permission, platform", platform, IdentityRead, Target{}, Allow},
		{"self permission, scopes exclude it", sa(a, tenant.StateActive, admin, []string{VirtualKeyRead}), IdentityRead, Target{}, Deny},
	}
	for _, s := range []tenant.State{tenant.StateProvisioning, tenant.StateSuspended, tenant.StateDeleting, tenant.StateDeleted} {
		cases = append(cases,
			struct {
				name string
				p    Principal
				perm string
				tgt  Target
				want Decision
			}{"state " + string(s), sa(a, s, admin, nil), ServiceAccountRead, Target{TenantID: a}, DenyTenantState},
			struct {
				name string
				p    Principal
				perm string
				tgt  Target
				want Decision
			}{"state " + string(s) + ", self", sa(a, s, admin, nil), IdentityRead, Target{}, DenyTenantState},
			struct {
				name string
				p    Principal
				perm string
				tgt  Target
				want Decision
			}{"state " + string(s) + ", other tenant", sa(a, s, admin, nil), ServiceAccountRead, Target{TenantID: b}, DenyTenantState},
		)
	}
	for _, c := range cases {
		if got := Decide(c.p, c.perm, c.tgt); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
	if Deny != 0 {
		t.Error("the zero value of Decision must be Deny")
	}
}

// TestEffective is T-204.
func TestEffective(t *testing.T) {
	cases := []struct {
		name string
		p    Principal
		want []string
	}{
		{"union of roles", sa(id(1), tenant.StateActive, []string{RoleInference, RoleTenantAdmin}, nil),
			[]string{CredentialDelete, CredentialRead, CredentialRotate, CredentialUse, CredentialWrite, IdentityRead, ModelInvoke,
				ModelRead, ModelRestrict, ServiceAccountRead, ServiceAccountWrite, VirtualKeyRead, VirtualKeyRevoke, VirtualKeyWrite}},
		{"scopes intersect", sa(id(1), tenant.StateActive, []string{RoleTenantAdmin}, []string{VirtualKeyRead, "tenant:read", ModelInvoke}),
			[]string{ModelInvoke, VirtualKeyRead}},
		{"unknown role", sa(id(1), tenant.StateActive, []string{"ghost"}, nil), nil},
		{"role of the other kind", sa(id(1), tenant.StateActive, []string{RolePlatformAdmin}, nil), nil},
		{"platform role on a platform principal", Principal{Kind: KindPlatform, Roles: []string{RolePlatformAdmin, RoleInference}},
			[]string{CatalogPrice, CatalogRead, CatalogWrite, IdentityRead, PlatformKeyRead, PlatformKeyRevoke, PlatformKeyWrite, TenantRead, TenantSuspend, TenantWrite, TenantCredentialRead, TenantCredentialRevoke}},
		{"nil scopes are no restriction", sa(id(1), tenant.StateActive, []string{RoleInference}, nil), []string{CredentialUse, ModelInvoke}},
		{"empty scopes are a restriction to nothing", sa(id(1), tenant.StateActive, []string{RoleInference}, []string{}), nil},
		{"duplicate roles", sa(id(1), tenant.StateActive, []string{RoleInference, RoleInference}, nil), []string{CredentialUse, ModelInvoke}},
	}
	for _, c := range cases {
		got := c.p.Effective()
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestContext(t *testing.T) {
	if _, ok := FromContext(context.Background()); ok {
		t.Error("an empty context holds a principal")
	}
	p := sa(id(1), tenant.StateActive, []string{RoleInference}, nil)
	got, ok := FromContext(WithPrincipal(context.Background(), p))
	if !ok || !reflect.DeepEqual(got, p) {
		t.Errorf("FromContext = %+v, %v", got, ok)
	}
	if errors.Is(ErrUnauthenticated, ErrUnavailable) || ErrUnauthenticated.Error() == "" || ErrUnavailable.Error() == "" {
		t.Error("the two errors must be distinct")
	}
}
