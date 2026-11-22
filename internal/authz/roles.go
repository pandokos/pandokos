package authz

import (
	"sort"
	"strings"
	"sync"
)

// Role is a built-in role. Roles are defined in code and immutable (AZ-03).
type Role struct {
	Name        string
	Scope       Scope    // ScopePlatform or ScopeTenant: where a principal holds it
	Members     bool     // a person can hold it
	Permissions []string // sorted
	Description string
}

// The names of the built-in roles.
const (
	RolePlatformAdmin = "platform-admin"
	RoleTenantAdmin   = "tenant-admin"
	RoleInference     = "inference"

	RoleOwner             = "owner"
	RoleCredentialManager = "credential-manager" //nolint:gosec // a role name, not a credential
	RoleUsageViewer       = "usage-viewer"
	RoleAuditor           = "auditor"
	RoleDeveloper         = "developer"
	RoleReadOnly          = "read-only"
	RolePlatformAuditor   = "platform-auditor"
)

// ownerOnly lists the tenant permissions that tenant-admin does not hold: they are kept for owners.
var ownerOnly = []string{SupportActionEnd}

func sorted(names ...string) []string {
	out := append([]string(nil), names...)
	sort.Strings(out)
	return out
}

func withIdentity(names ...string) []string {
	return sorted(append([]string{IdentityRead}, names...)...)
}

// permissionsOf returns the sorted names of the permissions of the catalogue that keep.
func permissionsOf(keep func(Permission) bool) []string {
	var out []string
	for _, p := range catalogue {
		if keep(p) {
			out = append(out, p.Name)
		}
	}
	sort.Strings(out)
	return out
}

// TenantPermissions returns the names of the permissions of scope tenant, sorted.
func TenantPermissions() []string {
	return permissionsOf(func(p Permission) bool { return p.Scope == ScopeTenant })
}

// SupportPermissions returns the names of the permissions that a support action may carry, sorted.
func SupportPermissions() []string {
	return permissionsOf(func(p Permission) bool { return p.Support })
}

func platformPermissions() []string {
	return permissionsOf(func(p Permission) bool { return p.Scope == ScopePlatform })
}

var builtin = sync.OnceValue(buildRoles)

// Roles returns the ten built-in roles, in a fixed order; the slices are copies.
func Roles() []Role {
	out := append([]Role(nil), builtin()...)
	for i := range out {
		out[i].Permissions = append([]string(nil), out[i].Permissions...)
	}
	return out
}

func buildRoles() []Role {
	tenantAdmin := []string{IdentityRead}
	for _, name := range TenantPermissions() {
		keep := true
		for _, o := range ownerOnly {
			if name == o {
				keep = false
			}
		}
		if keep {
			tenantAdmin = append(tenantAdmin, name)
		}
	}
	readOnly := []string{IdentityRead}
	for _, name := range TenantPermissions() {
		if strings.HasSuffix(name, ":read") {
			readOnly = append(readOnly, name)
		}
	}
	return []Role{
		{RoleOwner, ScopeTenant, true, withIdentity(TenantPermissions()...),
			"Holds every permission of the tenant, now and when permissions are added"},
		{RoleTenantAdmin, ScopeTenant, true, sorted(tenantAdmin...),
			"Administers one tenant day to day: everything but what is kept for owners"},
		{RoleCredentialManager, ScopeTenant, true, withIdentity(CredentialRead, CredentialWrite, CredentialRotate, CredentialDelete, ModelRead),
			"Manages the provider credentials of the tenant"},
		{RoleUsageViewer, ScopeTenant, true, withIdentity(UsageRead, PriceListRead, LimitRead, ModelRead),
			"Reads usage, prices and limits"},
		{RoleAuditor, ScopeTenant, true, withIdentity(ServiceAccountRead, VirtualKeyRead, CredentialRead, MemberRead, RoleRead, TokenIssuerRead,
			SubjectMappingRead, LimitRead, ModelRead, SupportActionRead),
			"Reads how the tenant is configured, who may do what, and what the platform did in it"},
		{RoleDeveloper, ScopeTenant, true, withIdentity(ModelInvoke, CredentialUse, ModelRead, CredentialRead, ServiceAccountRead, VirtualKeyRead,
			VirtualKeyWrite, VirtualKeyRevoke, UsageRead, LimitRead),
			"Issues virtual keys and calls models"},
		{RoleInference, ScopeTenant, false, sorted(ModelInvoke, CredentialUse), "Calls models through the inference listener"},
		{RoleReadOnly, ScopeTenant, true, sorted(readOnly...), "Reads everything of the tenant that can be read"},
		{RolePlatformAdmin, ScopePlatform, true, withIdentity(platformPermissions()...),
			"Administers the installation: tenants and platform keys"},
		{RolePlatformAuditor, ScopePlatform, true, withIdentity(TenantRead, PlatformKeyRead, MetricsRead, CatalogRead, PlatformIssuerRead, PlatformLimitRead, UserRead, TenantSupportRead),
			"Reads the installation: tenants, keys, catalogue, issuers, limits, users and support actions"},
	}
}

// RoleByName returns the built-in role with the given name.
func RoleByName(name string) (Role, bool) {
	for _, r := range Roles() {
		if r.Name == name {
			return r, true
		}
	}
	return Role{}, false
}

// CheckCustom normalizes the permissions of a custom role: sorted, without duplicates. offender
// is the first name, in the order given, that is not a permission of scope tenant.
func CheckCustom(permissions []string) (normalized []string, offender string) {
	seen := map[string]bool{}
	for _, name := range permissions {
		if p, ok := Lookup(name); !ok || p.Scope != ScopeTenant {
			return nil, name
		}
		if !seen[name] {
			seen[name] = true
			normalized = append(normalized, name)
		}
	}
	sort.Strings(normalized)
	return normalized, ""
}
