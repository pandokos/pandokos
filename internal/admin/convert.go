package admin

import (
	"encoding/json"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/store/queries"
)

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *server) tenantOut(r queries.PandokosTenant) adminapi.Tenant {
	z := s.tenantTimeZoneOut(r.TimeZone, r.TimeZonePrevious, r.TimeZoneEffectiveFrom)
	c := s.activeCurrencyOut(r.ActiveCurrency, r.ActiveCurrencyPrevious, r.ActiveCurrencyEffectiveFrom, s.now())
	return adminapi.Tenant{
		ID: r.ID, Name: r.Name, DisplayName: r.DisplayName, State: adminapi.TenantState(r.State),
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(), StateChangedAt: r.StateChangedAt.UTC(),
		TimeZone: r.TimeZone, TimeZoneInForce: z.InForce, TimeZoneEffectiveFrom: z.EffectiveFrom,
		ActiveCurrency: r.ActiveCurrency, ActiveCurrencyInForce: c.InForce, ActiveCurrencyEffectiveFrom: c.EffectiveFrom,
		ExternalReference: r.ExternalReference,
	}
}

type saRow struct {
	ID, TenantID, Name, Description, State string
	ValidFrom, ValidUntil                  *time.Time
	CreatedAt, UpdatedAt                   time.Time
	Roles                                  []string
}

func saFromGet(r queries.GetServiceAccountRow) saRow {
	return saRow{r.ID, r.TenantID, r.Name, r.Description, r.State, r.ValidFrom, r.ValidUntil, r.CreatedAt, r.UpdatedAt, r.Roles}
}

func saFromList(r queries.ListServiceAccountsRow) saRow {
	return saRow{r.ID, r.TenantID, r.Name, r.Description, r.State, r.ValidFrom, r.ValidUntil, r.CreatedAt, r.UpdatedAt, r.Roles}
}

func (r saRow) out() adminapi.ServiceAccount {
	return adminapi.ServiceAccount{
		ID: r.ID, TenantID: r.TenantID, Name: r.Name, Description: r.Description, State: adminapi.ServiceAccountState(r.State),
		ValidFrom: utc(r.ValidFrom), ValidUntil: utc(r.ValidUntil), Roles: nonNil(r.Roles),
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

type vkRow struct {
	KeyID, TenantID, ServiceAccountID, Description string
	Scopes                                         []string
	Scoped                                         bool
	ExpiresAt, RevokedAt                           *time.Time
	CreatedAt                                      time.Time
	State                                          string
	CredentialID                                   *string
	Models                                         []string
	ModelsRestricted                               bool
}

func (r vkRow) out() adminapi.VirtualKey {
	k := adminapi.VirtualKey{
		ID: r.KeyID, TenantID: r.TenantID, ServiceAccountID: r.ServiceAccountID, Description: r.Description,
		State: adminapi.KeyState(r.State), ExpiresAt: utc(r.ExpiresAt), RevokedAt: utc(r.RevokedAt), CreatedAt: r.CreatedAt.UTC(),
	}
	if r.CredentialID != nil {
		k.CredentialID = *r.CredentialID
	}
	if r.Scoped {
		k.Scopes = nonNil(r.Scopes)
	}
	if r.ModelsRestricted {
		k.Models = nonNil(r.Models)
	}
	return k
}

type pkRow struct {
	KeyID, Description, Role string
	Bootstrap                bool
	ExpiresAt, RevokedAt     *time.Time
	CreatedAt                time.Time
	State                    string
}

func (r pkRow) out() adminapi.PlatformKey {
	return adminapi.PlatformKey{
		ID: r.KeyID, Description: r.Description, Role: r.Role, Bootstrap: r.Bootstrap, State: adminapi.KeyState(r.State),
		ExpiresAt: utc(r.ExpiresAt), RevokedAt: utc(r.RevokedAt), CreatedAt: r.CreatedAt.UTC(),
	}
}

// credRow is the metadata of a credential: every row shape that sqlc generates for the
// statements of credentials has these members and no other, so each converts to it directly.
// The sealed secret is not among them and no statement of the admin role can return it.
type credRow = queries.PandokosProviderCredentialMetadatum

func credentialOut(r credRow) adminapi.Credential {
	return adminapi.Credential{
		ID: r.ID, TenantID: r.TenantID, Provider: r.Provider, Label: r.Label, Description: r.Description,
		Owner: adminapi.CredentialOwnerTenant, State: adminapi.CredentialState(r.State), IsDefault: r.IsDefault,
		Fingerprint: r.Fingerprint, Hint: r.Hint, ExpiresAt: utc(r.ExpiresAt), ValidatedAt: utc(r.ValidatedAt), RotatedAt: utc(r.RotatedAt),
		StateChangedAt: r.StateChangedAt.UTC(), StateChangedBy: adminapi.CredentialActor(r.StateChangedBy), StateReason: r.StateReason,
		CreatedAt: r.CreatedAt.UTC(),
	}
}

// tiRow is a tenant-bound registration as the statements of issuers.sql return it: each of them
// has these members and no other, so each converts to it directly.
type tiRow queries.GetTrustedIssuerRow

// claimsOut reads the required claims the database holds (a JSON object of strings).
func claimsOut(raw string) map[string]string {
	out := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return map[string]string{}
	}
	return out
}

func (r tiRow) out() adminapi.TokenIssuer {
	return adminapi.TokenIssuer{
		ID: r.ID, TenantID: r.TenantID, Name: r.Name, Description: r.Description, Issuer: r.Issuer, Audience: r.Audience,
		JwksUri: r.JwksUri, Algorithms: nonNil(r.Algorithms), SubjectClaim: r.SubjectClaim, RequiredClaims: claimsOut(r.RequiredClaims),
		Enabled: r.Enabled, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

func (r tiRow) values() issuerValues {
	return issuerValues{Name: r.Name, Description: r.Description, Issuer: r.Issuer, Audience: r.Audience, JwksURI: r.JwksUri,
		Algorithms: nonNil(r.Algorithms), SubjectClaim: r.SubjectClaim, RequiredClaims: claimsOut(r.RequiredClaims), Enabled: r.Enabled}
}

// piRow is a platform-level registration as the statements of issuers.sql return it.
type piRow queries.GetPlatformIssuerRow

func (r piRow) out() adminapi.PlatformIssuer {
	return adminapi.PlatformIssuer{
		ID: r.ID, Name: r.Name, Description: r.Description, Issuer: r.Issuer, Audience: r.Audience, JwksUri: r.JwksUri,
		Algorithms: nonNil(r.Algorithms), SubjectClaim: r.SubjectClaim, TenantClaim: r.TenantClaim, RequiredClaims: claimsOut(r.RequiredClaims),
		TenantCount: int(r.TenantCount), Enabled: r.Enabled, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

func (r piRow) values() issuerValues {
	return issuerValues{Name: r.Name, Description: r.Description, Issuer: r.Issuer, Audience: r.Audience, JwksURI: r.JwksUri,
		Algorithms: nonNil(r.Algorithms), SubjectClaim: r.SubjectClaim, TenantClaim: r.TenantClaim, RequiredClaims: claimsOut(r.RequiredClaims), Enabled: r.Enabled}
}

// mappingRow is a subject mapping with the scope, ID and name of its registration.
type mappingRow queries.GetSubjectMappingRow

func (r mappingRow) out() adminapi.SubjectMapping {
	return adminapi.SubjectMapping{
		ID: r.ID, TenantID: r.TenantID, Issuer: adminapi.IssuerRef{Scope: adminapi.IssuerScope(r.IssuerScope), ID: r.IssuerID, Name: r.IssuerName},
		Subject: r.Subject, ServiceAccountID: r.ServiceAccountID, ServiceAccountName: r.ServiceAccountName, Description: r.Description,
		CreatedAt: r.CreatedAt.UTC(),
	}
}

// detail is what an audit row holds of a mapping: identifiers, names and the subject, which is a
// public identifier of a workload, never a credential.
func (r mappingRow) detail() map[string]any {
	return map[string]any{"issuer_scope": r.IssuerScope, "issuer_id": r.IssuerID, "issuer_name": r.IssuerName, "subject": r.Subject,
		"service_account_id": r.ServiceAccountID, "service_account_name": r.ServiceAccountName, "description": r.Description}
}

func tenantPlatformIssuerOut(r queries.GetTenantPlatformIssuerRow) adminapi.TenantPlatformIssuer {
	return adminapi.TenantPlatformIssuer{ID: r.ID, Name: r.Name, Description: r.Description, Issuer: r.Issuer, Audience: r.Audience,
		SubjectClaim: r.SubjectClaim, TenantClaim: r.TenantClaim, ClaimValue: r.ClaimValue, Enabled: r.Enabled}
}

func issuerTenantOut(r queries.ListPlatformIssuerTenantsRow) adminapi.PlatformIssuerTenant {
	return adminapi.PlatformIssuerTenant{IssuerID: r.IssuerID, TenantID: r.TenantID, TenantName: r.TenantName, ClaimValue: r.ClaimValue, CreatedAt: r.CreatedAt.UTC()}
}

// currencyName resolves a stored currency to a code: "" is the default of the installation.
func (s *server) currencyName(raw string) string {
	if raw == "" {
		return s.defaultCurrency
	}
	return raw
}

// currencyInForce returns the raw currency that is in force at now: the previous one while a change is
// pending, the current one otherwise.
func currencyInForce(current, previous string, from *time.Time, now time.Time) string {
	if from != nil && now.Before(*from) {
		return previous
	}
	return current
}

// activeCurrencyOut builds the resource of the active currency of a tenant, without inactive limits.
func (s *server) activeCurrencyOut(current, previous string, from *time.Time, now time.Time) adminapi.ActiveCurrency {
	out := adminapi.ActiveCurrency{Currency: current, InForce: s.currencyName(currencyInForce(current, previous, from, now))}
	if from != nil && now.Before(*from) {
		out.EffectiveFrom = utcPtr(from)
	}
	return out
}

// userOut is the API form of a user that the platform manages; locked is the end of the lock of its
// account, read from the throttle row.
func userOut(r queries.GetUserRow, locked *time.Time) adminapi.User {
	u := adminapi.User{
		ID: r.ID, SignIn: adminapi.SignInMethod(r.SignIn), Username: r.Username, Subject: r.Subject, DisplayName: r.DisplayName, Email: r.Email,
		State: adminapi.UserState(r.State), StateChangedAt: r.StateChangedAt, LockedUntil: utc(locked),
		PlatformRoles: nonNil(r.PlatformRoles), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.SignIn == "local" {
		u.MustChangePassword = r.MustChange
	}
	return u
}

// memberOut is the API form of a member; for a member that the tenant manages it also says whether
// the password must be changed and until when the account is locked.
func memberOut(r queries.GetMemberRow, locked *time.Time) adminapi.Member {
	m := adminapi.Member{
		UserID: r.UserID, SignIn: adminapi.SignInMethod(r.SignIn), Username: r.Username, DisplayName: r.DisplayName, Email: r.Email,
		State: adminapi.UserState(r.State), ManagedBy: adminapi.ManagedByPlatform, Roles: nonNil(r.Roles), Source: adminapi.RoleSource(r.Source),
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.ManagingTenantID != nil {
		m.ManagedBy = adminapi.ManagedByTenant
		m.MustChangePassword = r.MustChange
		m.LockedUntil = utc(locked)
	}
	return m
}
