package authz

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"sort"
	"strings"
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
		{"metrics:read", ScopePlatform},
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
		{"usage:read", ScopeTenant},
		{"token_issuer:read", ScopeTenant},
		{"token_issuer:write", ScopeTenant},
		{"token_issuer:delete", ScopeTenant},
		{"subject_mapping:read", ScopeTenant},
		{"subject_mapping:write", ScopeTenant},
		{"subject_mapping:delete", ScopeTenant},
		{"platform_issuer:read", ScopePlatform},
		{"platform_issuer:write", ScopePlatform},
		{"platform_issuer:delete", ScopePlatform},
		{"limit:read", ScopeTenant},
		{"limit:write", ScopeTenant},
		{"limit:delete", ScopeTenant},
		{"platform_limit:read", ScopePlatform},
		{"platform_limit:write", ScopePlatform},
		{"platform_limit:delete", ScopePlatform},
		{"price_list:read", ScopeTenant},
		{"price_list:write", ScopeTenant},
		{"price_list:delete", ScopeTenant},
		{"user:read", ScopePlatform},
		{"user:write", ScopePlatform},
		{"member:read", ScopeTenant},
		{"member:write", ScopeTenant},
		{"session:self", ScopeSelf},
		{"role:read", ScopeTenant},
		{"role:write", ScopeTenant},
		{"role:delete", ScopeTenant},
		{"support_action:read", ScopeTenant},
		{"support_action:end", ScopeTenant},
		{"tenant_setting:write", ScopeTenant},
		{"tenant_support:open", ScopePlatform},
		{"tenant_support:read", ScopePlatform},
		{"audit:read", ScopeTenant},
		{"platform_audit:read", ScopePlatform},
	}
	got := Catalogue()
	if len(got) != 60 {
		t.Fatalf("%d permissions, want 60", len(got))
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
		"ModelRead": ModelRead, "ModelRestrict": ModelRestrict, "UsageRead": UsageRead,
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
		TenantCredentialRead, TenantCredentialRevoke, CatalogRead, CatalogWrite, CatalogPrice, MetricsRead,
		PlatformIssuerRead, PlatformIssuerWrite, PlatformIssuerDelete, PlatformLimitRead, PlatformLimitWrite, PlatformLimitDelete, UserRead, UserWrite,
		TenantSupportOpen, TenantSupportRead, PlatformAuditRead}
	tenantAll := []string{IdentityRead, ServiceAccountRead, ServiceAccountWrite, MemberRead, MemberWrite, VirtualKeyRead, VirtualKeyWrite, VirtualKeyRevoke, ModelInvoke,
		CredentialRead, CredentialWrite, CredentialRotate, CredentialDelete, CredentialUse, ModelRead, ModelRestrict, UsageRead,
		TokenIssuerRead, TokenIssuerWrite, TokenIssuerDelete, SubjectMappingRead, SubjectMappingWrite, SubjectMappingDelete, LimitRead, LimitWrite, LimitDelete,
		PriceListRead, PriceListWrite, PriceListDelete,
		RoleRead, RoleWrite, RoleDelete, SupportActionRead, TenantSettingWrite, AuditRead}
	want := []struct {
		name  string
		scope Scope
		perm  []string
	}{
		{RolePlatformAdmin, ScopePlatform, platform},
		{RoleTenantAdmin, ScopeTenant, tenantAll},
		{RoleInference, ScopeTenant, []string{ModelInvoke, CredentialUse}},
	}
	roles := Roles()
	if len(roles) != 10 {
		t.Fatalf("%d roles, want 10 (the three of sprint 1.3 and the seven of sprint 4.3)", len(roles))
	}
	byName := map[string]Role{}
	for _, r := range roles {
		byName[r.Name] = r
	}
	for _, w := range want {
		r := byName[w.name]
		exp := append([]string(nil), w.perm...)
		sort.Strings(exp)
		if r.Name != w.name || r.Scope != w.scope || !reflect.DeepEqual(r.Permissions, exp) || r.Description == "" {
			t.Errorf("role %s = %+v, want %s %s %v", w.name, r, w.name, w.scope, exp)
		}
		for _, p := range r.Permissions {
			perm, ok := Lookup(p)
			if !ok {
				t.Errorf("%s: %s is not in the catalogue", r.Name, p)
				continue
			}
			switch {
			case r.Scope == ScopePlatform && perm.Scope == ScopeTenant:
				t.Errorf("%s holds the tenant permission %s", r.Name, p)
			case r.Scope == ScopeTenant && perm.Scope == ScopePlatform:
				t.Errorf("%s holds the platform permission %s", r.Name, p)
			}
		}
		got, ok := RoleByName(w.name)
		if !ok || !reflect.DeepEqual(got, r) {
			t.Errorf("RoleByName(%s) = %+v, %v", w.name, got, ok)
		}
	}
	if _, ok := RoleByName("ghost"); ok {
		t.Error("RoleByName found a role that does not exist")
	}
	// The returned slices are copies.
	for i := range roles {
		switch roles[i].Name {
		case RoleInference:
			roles[i].Permissions[0] = "changed:changed"
		case RolePlatformAdmin:
			roles[i].Name = "changed"
		}
	}
	again := Roles()
	if byNameOf(again, RoleInference).Permissions[0] != CredentialUse || byNameOf(again, RolePlatformAdmin).Name != RolePlatformAdmin {
		t.Error("changing a returned slice changed the next call")
	}
}

func byNameOf(roles []Role, name string) Role {
	for _, r := range roles {
		if r.Name == name {
			return r
		}
	}
	return Role{}
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
			[]string{AuditRead, CredentialDelete, CredentialRead, CredentialRotate, CredentialUse, CredentialWrite, IdentityRead, LimitDelete, LimitRead, LimitWrite, MemberRead, MemberWrite, ModelInvoke,
				ModelRead, ModelRestrict, PriceListDelete, PriceListRead, PriceListWrite, RoleDelete, RoleRead, RoleWrite, ServiceAccountRead, ServiceAccountWrite, SubjectMappingDelete, SubjectMappingRead, SubjectMappingWrite,
				SupportActionRead, TenantSettingWrite,
				TokenIssuerDelete, TokenIssuerRead, TokenIssuerWrite, UsageRead, VirtualKeyRead, VirtualKeyRevoke, VirtualKeyWrite}},
		{"scopes intersect", sa(id(1), tenant.StateActive, []string{RoleTenantAdmin}, []string{VirtualKeyRead, "tenant:read", ModelInvoke}),
			[]string{ModelInvoke, VirtualKeyRead}},
		{"unknown role", sa(id(1), tenant.StateActive, []string{"ghost"}, nil), nil},
		{"role of the other kind", sa(id(1), tenant.StateActive, []string{RolePlatformAdmin}, nil), nil},
		{"platform role on a platform principal", Principal{Kind: KindPlatform, Roles: []string{RolePlatformAdmin, RoleInference}},
			[]string{CatalogPrice, CatalogRead, CatalogWrite, IdentityRead, MetricsRead, PlatformAuditRead, PlatformIssuerDelete, PlatformIssuerRead, PlatformIssuerWrite, PlatformKeyRead, PlatformKeyRevoke, PlatformKeyWrite, PlatformLimitDelete, PlatformLimitRead, PlatformLimitWrite, TenantRead, TenantSuspend, TenantWrite, TenantCredentialRead, TenantCredentialRevoke, TenantSupportOpen, TenantSupportRead, UserRead, UserWrite}},
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

// TestUsageRead is T-548: usage:read is in the catalogue at tenant scope with its text, tenant-admin
// holds it and no other role does, and Decide treats it as any tenant permission.
func TestUsageRead(t *testing.T) {
	p, ok := Lookup(UsageRead)
	if !ok || p.Name != "usage:read" || UsageRead != "usage:read" || p.Scope != ScopeTenant || p.Description != "Listing the tenant's usage records" {
		t.Fatalf("usage:read: %+v %v", p, ok)
	}
	count := 0
	for _, c := range Catalogue() {
		if c.Name == UsageRead {
			count++
		}
	}
	if count != 1 {
		t.Errorf("usage:read is listed %d times", count)
	}
	holders := map[string]bool{}
	for _, r := range Roles() {
		for _, perm := range r.Permissions {
			if perm == UsageRead {
				holders[r.Name] = true
			}
		}
	}
	// Sprint 4.3: the roles that read usage are the owner, the administrator, the usage viewer, the
	// developer and the read-only role (sprint 4.3, section 5.2).
	wantHolders := map[string]bool{RoleOwner: true, RoleTenantAdmin: true, RoleUsageViewer: true, RoleDeveloper: true, RoleReadOnly: true}
	if !reflect.DeepEqual(holders, wantHolders) {
		t.Errorf("usage:read is held by %v, want %v", holders, wantHolders)
	}

	a, b := id(1), id(2)
	for _, c := range []struct {
		name string
		p    Principal
		tgt  Target
		want Decision
	}{
		{"tenant admin, own tenant", sa(a, tenant.StateActive, []string{RoleTenantAdmin}, nil), Target{TenantID: a}, Allow},
		{"tenant admin, another tenant", sa(a, tenant.StateActive, []string{RoleTenantAdmin}, nil), Target{TenantID: b}, DenyNotFound},
		{"tenant admin, suspended", sa(a, tenant.StateSuspended, []string{RoleTenantAdmin}, nil), Target{TenantID: a}, DenyTenantState},
		{"inference", sa(a, tenant.StateActive, []string{RoleInference}, nil), Target{TenantID: a}, Deny},
		{"platform admin", Principal{Kind: KindPlatform, ID: "p", Roles: []string{RolePlatformAdmin}}, Target{TenantID: a}, Deny},
		{"a key scoped to model:read", sa(a, tenant.StateActive, []string{RoleTenantAdmin}, []string{ModelRead}), Target{TenantID: a}, Deny},
		{"a key scoped to usage:read", sa(a, tenant.StateActive, []string{RoleTenantAdmin}, []string{UsageRead}), Target{TenantID: a}, Allow},
	} {
		if got := Decide(c.p, UsageRead, c.tgt); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// TestLimitPermissions is T-683: the six permissions of sprint 5.2 with their scopes, the two roles
// that hold them, and the role inference unchanged.
func TestLimitPermissions(t *testing.T) {
	tenantPerms := []string{LimitRead, LimitWrite, LimitDelete}
	platformPerms := []string{PlatformLimitRead, PlatformLimitWrite, PlatformLimitDelete}
	for name, scope := range map[string]Scope{
		"limit:read": ScopeTenant, "limit:write": ScopeTenant, "limit:delete": ScopeTenant,
		"platform_limit:read": ScopePlatform, "platform_limit:write": ScopePlatform, "platform_limit:delete": ScopePlatform,
	} {
		p, ok := Lookup(name)
		if !ok || p.Scope != scope || p.Description == "" {
			t.Errorf("%s: %+v %v, want scope %s and a description", name, p, ok, scope)
		}
	}
	for _, c := range []struct {
		role string
		has  []string
		not  []string
	}{
		{RoleTenantAdmin, tenantPerms, platformPerms},
		{RolePlatformAdmin, platformPerms, tenantPerms},
		{RoleInference, nil, append(append([]string(nil), tenantPerms...), platformPerms...)},
	} {
		r, ok := RoleByName(c.role)
		if !ok {
			t.Fatalf("no role %s", c.role)
		}
		held := map[string]bool{}
		for _, p := range r.Permissions {
			held[p] = true
		}
		for _, p := range c.has {
			if !held[p] {
				t.Errorf("%s lacks %s", c.role, p)
			}
		}
		for _, p := range c.not {
			if held[p] {
				t.Errorf("%s holds %s", c.role, p)
			}
		}
	}
	if r, _ := RoleByName(RoleInference); !reflect.DeepEqual(r.Permissions, []string{CredentialUse, ModelInvoke}) {
		t.Errorf("inference holds %v, want credential:use and model:invoke only", r.Permissions)
	}

	a, b := id(1), id(2)
	admin := sa(a, tenant.StateActive, []string{RoleTenantAdmin}, nil)
	platform := Principal{Kind: KindPlatform, ID: "p", Roles: []string{RolePlatformAdmin}}
	for _, p := range tenantPerms {
		if d := Decide(admin, p, Target{TenantID: a}); d != Allow {
			t.Errorf("tenant admin, %s, own tenant: %v", p, d)
		}
		if d := Decide(admin, p, Target{TenantID: b}); d != DenyNotFound {
			t.Errorf("tenant admin, %s, another tenant: %v", p, d)
		}
		if d := Decide(platform, p, Target{TenantID: a}); d != Deny {
			t.Errorf("platform admin, %s: %v, want a refusal", p, d)
		}
		if d := Decide(sa(a, tenant.StateActive, []string{RoleInference}, nil), p, Target{TenantID: a}); d != Deny {
			t.Errorf("inference, %s: %v", p, d)
		}
		if d := Decide(sa(a, tenant.StateActive, []string{RoleTenantAdmin}, []string{VirtualKeyRead}), p, Target{TenantID: a}); d != Deny {
			t.Errorf("a key scoped without %s: %v", p, d)
		}
	}
	for _, p := range platformPerms {
		if d := Decide(platform, p, Target{}); d != Allow {
			t.Errorf("platform admin, %s: %v", p, d)
		}
		if d := Decide(admin, p, Target{TenantID: a}); d != Deny {
			t.Errorf("tenant admin, %s: %v, want a refusal", p, d)
		}
	}
}

// TestPriceListPermissions is T-753: the three permissions of sprint 5.4 have tenant scope and their
// texts; tenant-admin holds them; platform-admin and inference hold none.
func TestPriceListPermissions(t *testing.T) {
	want := map[string]string{
		PriceListRead:   "Reading the tenant's price lists and their versions, and the tenant's active currency",
		PriceListWrite:  "Adding a price version to one of the tenant's price lists",
		PriceListDelete: "Removing a price version of the tenant's lists that has not started",
	}
	for name, text := range want {
		p, ok := Lookup(name)
		if !ok || p.Scope != ScopeTenant || p.Description != text {
			t.Errorf("%s: %+v (found %v), want tenant scope and %q", name, p, ok, text)
		}
	}
	for _, r := range Roles() {
		held := 0
		for _, perm := range r.Permissions {
			if _, ok := want[perm]; ok {
				held++
			}
		}
		switch r.Name {
		case RoleTenantAdmin, RoleOwner:
			if held != 3 {
				t.Errorf("%s holds %d of the three", r.Name, held)
			}
		case RoleUsageViewer, RoleReadOnly:
			if held != 1 {
				t.Errorf("%s holds %d price list permissions, want price_list:read", r.Name, held)
			}
		default:
			if held != 0 {
				t.Errorf("%s holds %d price list permissions", r.Name, held)
			}
		}
	}
}

// TestSettingDescriptions: the time zone and the active currency are set with tenant_setting:write
// (D-1), so the descriptions of limit:write and price_list:write do not offer them.
func TestSettingDescriptions(t *testing.T) {
	for _, name := range []string{LimitWrite, PriceListWrite} {
		p, _ := Lookup(name)
		for _, word := range []string{"time zone", "currency"} {
			if strings.Contains(p.Description, word) {
				t.Errorf("%s: %q names the %s, which tenant_setting:write sets", name, p.Description, word)
			}
		}
	}
	p, _ := Lookup(TenantSettingWrite)
	for _, word := range []string{"time zone", "currency"} {
		if !strings.Contains(p.Description, word) {
			t.Errorf("%s: %q does not name the %s", TenantSettingWrite, p.Description, word)
		}
	}
}

// TestMetricsPermission is T-926: metrics:read is a platform permission without attributes, held by
// platform-admin and platform-auditor only; a tenant principal of any role is refused; a platform key
// can be scoped to it.
func TestMetricsPermission(t *testing.T) {
	p, ok := Lookup(MetricsRead)
	if !ok || MetricsRead != "metrics:read" {
		t.Fatalf("metrics:read is not in the catalogue: %+v %v", p, ok)
	}
	if p.Scope != ScopePlatform || p.InTenant || p.Support || p.Description == "" {
		t.Errorf("metrics:read is %+v, want a platform permission with a description and no attribute", p)
	}

	var holders []string
	for _, r := range Roles() {
		for _, perm := range r.Permissions {
			if perm == MetricsRead {
				holders = append(holders, r.Name)
			}
		}
	}
	if want := []string{RolePlatformAdmin, RolePlatformAuditor}; !equalSets(holders, want) {
		t.Errorf("the roles holding metrics:read are %v, want %v only", holders, want)
	}

	a := id(1)
	for _, r := range Roles() {
		if r.Scope != ScopeTenant {
			continue
		}
		if d := Decide(sa(a, tenant.StateActive, []string{r.Name}, nil), MetricsRead, Target{}); d == Allow {
			t.Errorf("a tenant principal with the role %s may read the metrics", r.Name)
		}
		if d := Decide(sa(a, tenant.StateActive, []string{r.Name}, nil), MetricsRead, Target{TenantID: a}); d == Allow {
			t.Errorf("a tenant principal with the role %s may read the metrics of its tenant", r.Name)
		}
	}
	for _, role := range []string{RolePlatformAdmin, RolePlatformAuditor} {
		if d := Decide(Principal{Kind: KindPlatform, ID: "p", Roles: []string{role}}, MetricsRead, Target{}); d != Allow {
			t.Errorf("%s: %v, want Allow", role, d)
		}
	}
	if d := Decide(Principal{Kind: KindPlatform, ID: "p"}, MetricsRead, Target{}); d == Allow {
		t.Error("a platform principal without a role may read the metrics")
	}

	// A platform key scoped to the permission alone holds it; scoped to another, it does not.
	admin := Principal{Kind: KindPlatform, ID: "p", Roles: []string{RolePlatformAdmin}}
	scoped := admin
	scoped.Scopes = []string{MetricsRead}
	if d := Decide(scoped, MetricsRead, Target{}); d != Allow {
		t.Errorf("a key scoped to metrics:read: %v", d)
	}
	if d := Decide(scoped, TenantRead, Target{}); d == Allow {
		t.Errorf("a key scoped to metrics:read reads tenants: %v", d)
	}
	other := admin
	other.Scopes = []string{CatalogRead}
	if d := Decide(other, MetricsRead, Target{}); d == Allow {
		t.Errorf("a key scoped to catalog:read reads the metrics: %v", d)
	}
}

func equalSets(a, b []string) bool {
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	return reflect.DeepEqual(x, y)
}
