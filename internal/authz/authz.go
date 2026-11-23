// Package authz is the authorization core: the catalogue of permissions, the built-in roles, the
// principal that every authentication mechanism produces, and the one decision function that
// every route calls (ADR 0007). The default is deny.
package authz

import (
	"context"
	"errors"
	"sort"
	"sync"

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
	InTenant    bool // a platform permission whose operations read or change one tenant
	Support     bool // a tenant permission that a support action may carry
}

// The permissions of the catalogue.
const (
	IdentityRead        = "identity:read"
	TenantRead          = "tenant:read"
	TenantWrite         = "tenant:write"
	TenantSuspend       = "tenant:suspend"
	TenantDelete        = "tenant:delete"
	KEKRead             = "kek:read"
	PlatformKeyRead     = "platform_key:read"
	PlatformKeyWrite    = "platform_key:write"
	PlatformKeyRevoke   = "platform_key:revoke"
	MetricsRead         = "metrics:read"
	ServiceAccountRead  = "service_account:read"
	ServiceAccountWrite = "service_account:write"
	VirtualKeyRead      = "virtual_key:read"
	VirtualKeyWrite     = "virtual_key:write"
	VirtualKeyRevoke    = "virtual_key:revoke"
	ModelInvoke         = "model:invoke"

	CredentialRead         = "credential:read"          //nolint:gosec // a permission name, not a credential
	CredentialWrite        = "credential:write"         //nolint:gosec // a permission name, not a credential
	CredentialRotate       = "credential:rotate"        //nolint:gosec // a permission name, not a credential
	CredentialDelete       = "credential:delete"        //nolint:gosec // a permission name, not a credential
	CredentialUse          = "credential:use"           //nolint:gosec // a permission name, not a credential
	TenantCredentialRead   = "tenant_credential:read"   //nolint:gosec // a permission name, not a credential
	TenantCredentialRevoke = "tenant_credential:revoke" //nolint:gosec // a permission name, not a credential

	CatalogRead   = "catalog:read"
	CatalogWrite  = "catalog:write"
	CatalogPrice  = "catalog:price"
	ModelRead     = "model:read"
	ModelRestrict = "model:restrict"
	UsageRead     = "usage:read"

	TokenIssuerRead      = "token_issuer:read"   //nolint:gosec // a permission name, not a credential
	TokenIssuerWrite     = "token_issuer:write"  //nolint:gosec // a permission name, not a credential
	TokenIssuerDelete    = "token_issuer:delete" //nolint:gosec // a permission name, not a credential
	SubjectMappingRead   = "subject_mapping:read"
	SubjectMappingWrite  = "subject_mapping:write"
	SubjectMappingDelete = "subject_mapping:delete"
	PlatformIssuerRead   = "platform_issuer:read"
	PlatformIssuerWrite  = "platform_issuer:write"
	PlatformIssuerDelete = "platform_issuer:delete"

	LimitRead           = "limit:read"
	LimitWrite          = "limit:write"
	LimitDelete         = "limit:delete"
	PlatformLimitRead   = "platform_limit:read"
	PlatformLimitWrite  = "platform_limit:write"
	PlatformLimitDelete = "platform_limit:delete"
	AuditRead           = "audit:read"
	PlatformAuditRead   = "platform_audit:read"

	PriceListRead   = "price_list:read"
	PriceListWrite  = "price_list:write"
	PriceListDelete = "price_list:delete"

	UserRead    = "user:read"
	UserWrite   = "user:write"
	MemberRead  = "member:read"
	MemberWrite = "member:write"
	SessionSelf = "session:self"

	RoleRead           = "role:read"
	RoleWrite          = "role:write"
	RoleDelete         = "role:delete"
	SupportActionRead  = "support_action:read"
	SupportActionEnd   = "support_action:end"
	TenantSettingWrite = "tenant_setting:write"
	TenantSupportOpen  = "tenant_support:open"
	TenantSupportRead  = "tenant_support:read"
)

var catalogue = []Permission{
	{IdentityRead, ScopeSelf, "Reading one's own identity, roles and permissions", false, false},
	{TenantRead, ScopePlatform, "Reading and listing tenants, with their time zone, their active currency and their deletion", false, false},
	{TenantWrite, ScopePlatform, "Creating a tenant with its first administrator; changing its name and display name", false, false},
	{TenantSuspend, ScopePlatform, "Suspending and reactivating a tenant", false, false},
	{PlatformKeyRead, ScopePlatform, "Reading and listing platform keys (metadata)", false, false},
	{PlatformKeyWrite, ScopePlatform, "Issuing a platform key", false, false},
	{PlatformKeyRevoke, ScopePlatform, "Revoking a platform key", false, false},
	{MetricsRead, ScopePlatform, "Reading the installation's metrics on the scrape endpoint", false, false},
	{ServiceAccountRead, ScopeTenant, "Reading and listing service accounts and their roles", false, true},
	{ServiceAccountWrite, ScopeTenant, "Creating, changing, disabling and enabling service accounts; assigning roles", false, true},
	{VirtualKeyRead, ScopeTenant, "Reading and listing virtual keys (metadata)", false, true},
	{VirtualKeyWrite, ScopeTenant, "Issuing a virtual key", false, true},
	{VirtualKeyRevoke, ScopeTenant, "Revoking a virtual key", false, true},
	{ModelInvoke, ScopeTenant, "Calling a model through the inference listener", false, false},
	{CredentialRead, ScopeTenant, "Reading and listing provider credentials (metadata)", false, false},
	{CredentialWrite, ScopeTenant, "Registering a credential; changing its label, description and expiry; disabling and enabling it; choosing the default", false, false},
	{CredentialRotate, ScopeTenant, "Replacing the secret of a credential", false, false},
	{CredentialDelete, ScopeTenant, "Revoking and deleting a credential", false, false},
	{CredentialUse, ScopeTenant, "Having one's model calls authenticated to the provider with a credential of the tenant", false, false},
	{TenantCredentialRead, ScopePlatform, "Reading and listing the credentials of a tenant (metadata)", true, false},
	{TenantCredentialRevoke, ScopePlatform, "Disabling or revoking a credential of a tenant, with a reason", true, false},
	{CatalogRead, ScopePlatform, "Reading providers, endpoints, models, model grants, price versions and price imports", false, false},
	{CatalogWrite, ScopePlatform, "Creating, changing and deleting providers, endpoints and models; granting models to tenants and revoking grants", false, false},
	{CatalogPrice, ScopePlatform, "Setting and removing price overrides; running a price import", false, false},
	{ModelRead, ScopeTenant, "Listing the models available to the tenant, with the prices that apply to it; reading the tenant's model restriction", false, false},
	{ModelRestrict, ScopeTenant, "Setting and clearing the tenant's model restriction", false, false},
	{UsageRead, ScopeTenant, "Listing the tenant's usage records", false, false},
	{TokenIssuerRead, ScopeTenant, "Reading and listing the tenant's trusted token issuers and the platform-level issuers that list the tenant", false, false},
	{TokenIssuerWrite, ScopeTenant, "Registering a trusted token issuer; changing, disabling and enabling it", false, false},
	{TokenIssuerDelete, ScopeTenant, "Deleting a trusted token issuer with its subject mappings", false, false},
	{SubjectMappingRead, ScopeTenant, "Reading and listing subject mappings", false, false},
	{SubjectMappingWrite, ScopeTenant, "Letting a token subject act as a service account, with all its roles", false, false},
	{SubjectMappingDelete, ScopeTenant, "Deleting a subject mapping", false, false},
	{PlatformIssuerRead, ScopePlatform, "Reading and listing platform-level token issuers and their tenant lists", false, false},
	{PlatformIssuerWrite, ScopePlatform, "Registering and changing a platform-level token issuer; adding and removing tenants of its list", false, false},
	{PlatformIssuerDelete, ScopePlatform, "Deleting a platform-level token issuer with its tenant list and subject mappings", false, false},
	{LimitRead, ScopeTenant, "Reading and listing the tenant's limit rules, the platform ceilings that apply to it, its limit events and the tenant's time zone", false, false},
	{LimitWrite, ScopeTenant, "Creating and changing a limit rule of the tenant", false, false},
	{LimitDelete, ScopeTenant, "Deleting a limit rule of the tenant", false, false},
	{PlatformLimitRead, ScopePlatform, "Reading and listing the ceilings the platform sets for tenants", false, false},
	{PlatformLimitWrite, ScopePlatform, "Creating and changing the ceilings the platform sets for tenants", false, false},
	{PlatformLimitDelete, ScopePlatform, "Deleting a ceiling the platform sets for tenants", false, false},
	{PriceListRead, ScopeTenant, "Reading the tenant's price lists and their versions, and the tenant's active currency", false, false},
	{PriceListWrite, ScopeTenant, "Adding a price version to one of the tenant's price lists", false, false},
	{PriceListDelete, ScopeTenant, "Removing a price version of the tenant's lists that has not started", false, false},
	{UserRead, ScopePlatform, "Reading and listing the users that the platform manages, with their platform roles", false, false},
	{UserWrite, ScopePlatform, "Creating and changing such users; disabling and enabling them; resetting their password; unlocking them; ending their sessions; assigning platform roles", false, false},
	{MemberRead, ScopeTenant, "Reading and listing the members of the tenant and their roles", false, true},
	{MemberWrite, ScopeTenant, "Adding and removing members and setting their roles; creating local accounts that the tenant manages, and for those: disabling, enabling, resetting the password, unlocking, ending sessions", false, true},
	{SessionSelf, ScopeSelf, "Signing out, changing one's own password, listing and ending one's own sessions", false, false},
	{RoleRead, ScopeTenant, "Reading and listing the custom roles of the tenant", false, true},
	{RoleWrite, ScopeTenant, "Defining and changing a custom role; deactivating and activating it", false, true},
	{RoleDelete, ScopeTenant, "Deleting a custom role that nobody holds", false, true},
	{SupportActionRead, ScopeTenant, "Reading the support actions that the platform opened in the tenant", false, true},
	{SupportActionEnd, ScopeTenant, "Ending a support action of the platform in the tenant", false, false},
	{TenantSettingWrite, ScopeTenant, "Setting the tenant's time zone, its active currency and the metadata keys it accepts", false, false},
	{TenantSupportOpen, ScopePlatform, "Opening a support action in a tenant, with a reason, and closing support actions", true, false},
	{TenantSupportRead, ScopePlatform, "Reading and listing the support actions of a tenant", true, false},
	{AuditRead, ScopeTenant, "Reading the tenant's audit log, the head of its chain and its export", false, false},
	{PlatformAuditRead, ScopePlatform, "Reading the platform's audit log, the head of its chain and its export", false, false},
	{TenantDelete, ScopePlatform, "Requesting and cancelling the deletion of a tenant, with a reason", false, false},
	{KEKRead, ScopePlatform, "Reading the versions of the key-encryption key and their state", false, false},
}

// Catalogue returns the permissions, in a fixed order.
func Catalogue() []Permission { return append([]Permission(nil), catalogue...) }

// Lookup returns the permission with the given name.
func Lookup(name string) (Permission, bool) {
	p, ok := byName()[name]
	return p, ok
}

// byName indexes the catalogue by name, built once.
var byName = sync.OnceValue(func() map[string]Permission {
	m := make(map[string]Permission, len(catalogue))
	for _, p := range catalogue {
		m[p.Name] = p
	}
	return m
})

// Kind is the kind of a principal.
type Kind string

// The kinds of principal.
const (
	KindPlatform       Kind = "platform"
	KindServiceAccount Kind = "service_account"
	KindUser           Kind = "user"
)

// Principal is who a request acts as. Every authentication mechanism produces one (ADR 0005).
type Principal struct {
	Kind      Kind
	ID        string // service account ID, or the key ID of the platform key
	Name      string // service account name, or the description of the platform key
	KeyID     string // key ID of the virtual or platform key in use; for a token, the ID of the subject mapping
	Bootstrap bool   // the credential is the bootstrap key
	// IssuerID is the ID of the registration (trusted or platform-level token issuer) that
	// accepted the token the request carried; empty for a key.
	IssuerID string
	// Subject is the verified subject of that token; empty for a key.
	Subject string
	// SessionID is the session of a user's request; empty for any other credential.
	SessionID string
	// MustChangePassword is true for a session whose account has to change its password first.
	MustChangePassword bool
	TenantID           tenant.ID
	TenantName         string
	TenantState        tenant.State
	Roles              []string
	Scopes             []string // nil: no restriction; otherwise the permissions the key is limited to
	// CredentialID is the provider credential the key in use is pinned to; empty for none.
	CredentialID string
	// Models is the allow-list of the key in use, as model references (provider/name); nil: the
	// key has no list. An empty, non-nil list allows nothing.
	Models []string
	// Granted holds the permissions that come from data: those of the active custom roles of the
	// principal, or, for a platform principal under a support action, those of the action. It
	// has no effect until Effective reads it.
	Granted []string
	// SupportActionID is the support action the request runs under; empty otherwise.
	SupportActionID string
}

// holdsRole reports whether a role of the given scope counts for p: a platform role for a
// principal without a tenant that is not a service account, a tenant role for a principal with a
// tenant that is not a platform key.
func (p Principal) holdsRole(scope Scope) bool {
	switch p.Kind {
	case KindPlatform:
		return scope == ScopePlatform
	case KindServiceAccount:
		return scope == ScopeTenant
	case KindUser:
		if p.TenantID.IsZero() {
			return scope == ScopePlatform
		}
		return scope == ScopeTenant
	}
	return false
}

// Effective returns the permissions p holds, sorted, in four steps: the permissions of the built-in
// roles among Roles whose scope counts for p; for a principal that holds tenant roles, the names
// of Granted that are tenant permissions of the catalogue (anything else is dropped); for a user,
// identity:read and session:self; all intersected with Scopes when Scopes is not nil. A principal
// that runs under a support action has instead the permissions of the action that carry the
// attribute Support (sprint 4.3, sections 5.3 and 5.6).
func (p Principal) Effective() []string {
	set := map[string]bool{}
	if p.SupportActionID != "" {
		for _, name := range p.Granted {
			if perm, ok := Lookup(name); ok && perm.Scope == ScopeTenant && perm.Support {
				set[name] = true
			}
		}
	} else {
		for _, name := range p.Roles {
			if r, ok := RoleByName(name); ok && p.holdsRole(r.Scope) {
				for _, perm := range r.Permissions {
					set[perm] = true
				}
			}
		}
		if len(p.Roles) > 0 && p.holdsRole(ScopeTenant) {
			for _, name := range p.Granted {
				if perm, ok := Lookup(name); ok && perm.Scope == ScopeTenant {
					set[name] = true
				}
			}
		}
	}
	if p.Kind == KindUser {
		// A user holds the two self permissions whatever its roles. A key or a token has no
		// session, so session:self is never in the roles and never held by them.
		set[IdentityRead] = true
		set[SessionSelf] = true
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

// supportsTenant is true for a platform key that runs under a support action: it is treated as a
// principal of the tenant of the action.
func (p Principal) supportsTenant() bool {
	return p.Kind == KindPlatform && p.SupportActionID != ""
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
		// Only a principal without a tenant: a platform key without a support action, or a user
		// without a tenant.
		if !p.holds(permission) || (p.Kind != KindPlatform && p.Kind != KindUser) || !p.TenantID.IsZero() {
			return Deny
		}
		return Allow
	}
	if p.Kind == KindServiceAccount || (p.Kind == KindUser && !p.TenantID.IsZero()) || p.supportsTenant() {
		if p.TenantID.IsZero() {
			return Deny
		}
		if p.TenantState != tenant.StateActive {
			return DenyTenantState
		}
	}
	if perm.Scope == ScopeTenant {
		switch {
		case p.Kind == KindPlatform && p.SupportActionID == "", t.TenantID.IsZero():
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
