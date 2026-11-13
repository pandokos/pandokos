// Package authz is the authorization core: the catalogue of permissions, the built-in roles, the
// principal that every authentication mechanism produces, and the one decision function that
// every route calls (ADR 0007). The default is deny.
package authz

import (
	"context"
	"errors"
	"sort"

	"github.com/pandokos/pandokos/internal/tenant"
)

// Scope says where a permission applies.
type Scope string

// The scopes of a permission.
const (
	ScopePlatform Scope = "platform" // the installation; held by platform principals only
	ScopeTenant   Scope = "tenant"   // one tenant; held by principals of that tenant only
	ScopeSelf     Scope = "self"     // the caller itself
)

// Permission is one resource:action pair of the catalogue.
type Permission struct {
	Name        string // "resource:action"
	Scope       Scope
	Description string
}

// The permissions of the catalogue.
const (
	IdentityRead        = "identity:read"
	TenantRead          = "tenant:read"
	TenantWrite         = "tenant:write"
	TenantSuspend       = "tenant:suspend"
	PlatformKeyRead     = "platform_key:read"
	PlatformKeyWrite    = "platform_key:write"
	PlatformKeyRevoke   = "platform_key:revoke"
	ServiceAccountRead  = "service_account:read"
	ServiceAccountWrite = "service_account:write"
	VirtualKeyRead      = "virtual_key:read"
	VirtualKeyWrite     = "virtual_key:write"
	VirtualKeyRevoke    = "virtual_key:revoke"
	ModelInvoke         = "model:invoke"

	CredentialRead         = "credential:read"
	CredentialWrite        = "credential:write"
	CredentialRotate       = "credential:rotate"
	CredentialDelete       = "credential:delete"
	CredentialUse          = "credential:use"
	TenantCredentialRead   = "tenant_credential:read"
	TenantCredentialRevoke = "tenant_credential:revoke"
)

var catalogue = []Permission{
	{IdentityRead, ScopeSelf, "Reading one's own identity, roles and permissions"},
	{TenantRead, ScopePlatform, "Reading and listing tenants"},
	{TenantWrite, ScopePlatform, "Creating a tenant with its first administrator; changing its name and display name"},
	{TenantSuspend, ScopePlatform, "Suspending and reactivating a tenant"},
	{PlatformKeyRead, ScopePlatform, "Reading and listing platform keys (metadata)"},
	{PlatformKeyWrite, ScopePlatform, "Issuing a platform key"},
	{PlatformKeyRevoke, ScopePlatform, "Revoking a platform key"},
	{ServiceAccountRead, ScopeTenant, "Reading and listing service accounts and their roles"},
	{ServiceAccountWrite, ScopeTenant, "Creating, changing, disabling and enabling service accounts; assigning roles"},
	{VirtualKeyRead, ScopeTenant, "Reading and listing virtual keys (metadata)"},
	{VirtualKeyWrite, ScopeTenant, "Issuing a virtual key"},
	{VirtualKeyRevoke, ScopeTenant, "Revoking a virtual key"},
	{ModelInvoke, ScopeTenant, "Calling a model through the inference listener"},
	{CredentialRead, ScopeTenant, "Reading and listing provider credentials (metadata)"},
	{CredentialWrite, ScopeTenant, "Registering a credential; changing its label, description and expiry; disabling and enabling it; choosing the default"},
	{CredentialRotate, ScopeTenant, "Replacing the secret of a credential"},
	{CredentialDelete, ScopeTenant, "Revoking and deleting a credential"},
	{CredentialUse, ScopeTenant, "Having one's model calls authenticated to the provider with a credential of the tenant"},
	{TenantCredentialRead, ScopePlatform, "Reading and listing the credentials of a tenant (metadata)"},
	{TenantCredentialRevoke, ScopePlatform, "Disabling or revoking a credential of a tenant, with a reason"},
}

// Catalogue returns the permissions, in a fixed order.
func Catalogue() []Permission { return append([]Permission(nil), catalogue...) }

// Lookup returns the permission with the given name.
func Lookup(name string) (Permission, bool) {
	for _, p := range catalogue {
		if p.Name == name {
			return p, true
		}
	}
	return Permission{}, false
}

// Kind is the kind of a principal.
type Kind string

// The kinds of principal of this sprint.
const (
	KindPlatform       Kind = "platform"
	KindServiceAccount Kind = "service_account"
)

// Role is a built-in role. Roles are defined in code and immutable (AZ-03).
type Role struct {
	Name        string
	Kind        Kind     // which kind of principal can hold it
	Permissions []string // sorted
	Description string
}

// The names of the built-in roles.
const (
	RolePlatformAdmin = "platform-admin"
	RoleTenantAdmin   = "tenant-admin"
	RoleInference     = "inference"
)

func sorted(names ...string) []string {
	out := append([]string(nil), names...)
	sort.Strings(out)
	return out
}

// Roles returns the three built-in roles, in a fixed order; the slices are copies.
func Roles() []Role {
	return []Role{
		{RolePlatformAdmin, KindPlatform, sorted(IdentityRead, TenantRead, TenantWrite, TenantSuspend, PlatformKeyRead, PlatformKeyWrite, PlatformKeyRevoke, TenantCredentialRead, TenantCredentialRevoke),
			"Administers the installation: tenants and platform keys"},
		{RoleTenantAdmin, KindServiceAccount, sorted(IdentityRead, ServiceAccountRead, ServiceAccountWrite, VirtualKeyRead, VirtualKeyWrite, VirtualKeyRevoke, ModelInvoke,
			CredentialRead, CredentialWrite, CredentialRotate, CredentialDelete, CredentialUse),
			"Administers one tenant: service accounts and virtual keys; may call models"},
		{RoleInference, KindServiceAccount, sorted(ModelInvoke, CredentialUse), "Calls models through the inference listener"},
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

// Principal is who a request acts as. Every authentication mechanism produces one (ADR 0005).
type Principal struct {
	Kind        Kind
	ID          string // service account ID, or the key ID of the platform key
	Name        string // service account name, or the description of the platform key
	KeyID       string // key ID of the credential in use
	Bootstrap   bool   // the credential is the bootstrap key
	TenantID    tenant.ID
	TenantName  string
	TenantState tenant.State
	Roles       []string
	Scopes      []string // nil: no restriction; otherwise the permissions the key is limited to
	// CredentialID is the provider credential the key in use is pinned to; empty for none.
	CredentialID string
}

// Effective returns the permissions p holds: the union of the permissions of its roles that
// exist and are of p's kind, intersected with Scopes when Scopes is not nil. Sorted.
func (p Principal) Effective() []string {
	set := map[string]bool{}
	for _, name := range p.Roles {
		if r, ok := RoleByName(name); ok && r.Kind == p.Kind {
			for _, perm := range r.Permissions {
				set[perm] = true
			}
		}
	}
	if p.Scopes != nil {
		allowed := map[string]bool{}
		for _, s := range p.Scopes {
			allowed[s] = true
		}
		for perm := range set {
			if !allowed[perm] {
				delete(set, perm)
			}
		}
	}
	out := make([]string, 0, len(set))
	for perm := range set {
		out = append(out, perm)
	}
	sort.Strings(out)
	return out
}

func (p Principal) holds(permission string) bool {
	for _, e := range p.Effective() {
		if e == permission {
			return true
		}
	}
	return false
}

// Target is what a request acts on. TenantID is set for a tenant permission, zero otherwise.
type Target struct{ TenantID tenant.ID }

// Decision is the outcome of Decide.
type Decision int

// The decisions.
const (
	Deny Decision = iota // 403 forbidden; the zero value
	Allow
	DenyNotFound    // 404 not_found: the target is another tenant
	DenyTenantState // 403 tenant_suspended
)

// Decide applies the rules of ADR 0007 in order; the first that applies wins.
func Decide(p Principal, permission string, t Target) Decision {
	perm, ok := Lookup(permission)
	if !ok {
		return Deny
	}
	if perm.Scope == ScopePlatform {
		if p.Kind != KindPlatform || !p.holds(permission) {
			return Deny
		}
		return Allow
	}
	if p.Kind == KindServiceAccount {
		if p.TenantID.IsZero() {
			return Deny
		}
		if p.TenantState != tenant.StateActive {
			return DenyTenantState
		}
	}
	if perm.Scope == ScopeTenant {
		switch {
		case p.Kind == KindPlatform, t.TenantID.IsZero():
			return Deny
		case t.TenantID != p.TenantID:
			return DenyNotFound
		}
	}
	if p.holds(permission) {
		return Allow
	}
	return Deny
}

type principalKey struct{}

// WithPrincipal returns a context that carries p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// FromContext returns the principal of ctx.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// The errors of authentication.
var (
	ErrUnauthenticated = errors.New("authz: unauthenticated")
	ErrUnavailable     = errors.New("authz: cannot verify the credential now")
)
