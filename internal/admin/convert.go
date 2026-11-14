package admin

import (
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

func tenantOut(r queries.PandokosTenant) adminapi.Tenant {
	return adminapi.Tenant{
		ID: r.ID, Name: r.Name, DisplayName: r.DisplayName, State: adminapi.TenantState(r.State),
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(), StateChangedAt: r.StateChangedAt.UTC(),
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
