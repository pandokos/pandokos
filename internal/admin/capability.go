package admin

import (
	"context"
	"errors"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// credentialPage is the page size the capability check reads credential metadata with.
const credentialPage = 200

// GetCapability says whether a tenant can call a provider and model now. It evaluates the
// gateway's own rules on committed state: the catalogue snapshot's Resolve for the provider and
// the model, and the tenant's default credential as metadata. It never opens a credential and
// writes nothing.
func (s *server) GetCapability(ctx context.Context, in adminapi.GetCapabilityInput) (*adminapi.Capability, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	var missingProvider, missingModel *adminapi.FieldError
	switch {
	case in.Provider == "":
		missingProvider = &adminapi.FieldError{Field: "provider", Message: "is required"}
	case catalog.ValidateProviderName(in.Provider) != nil:
		missingProvider = &adminapi.FieldError{Field: "provider", Message: "is not a provider name"}
	}
	switch {
	case in.Model == "":
		missingModel = &adminapi.FieldError{Field: "model", Message: "is required"}
	case catalog.ValidateModelName(in.Model) != nil:
		missingModel = &adminapi.FieldError{Field: "model", Message: "is not a model name"}
	}
	if err := fieldsError(missingModel, missingProvider); err != nil {
		return nil, err
	}

	snap, err := catalog.LoadSnapshot(ctx, s.db)
	if err != nil {
		return nil, err
	}
	ta, err := catalog.LoadTenantAccess(ctx, s.db, tid)
	if err != nil {
		return nil, err
	}
	p := authz.Principal{Kind: authz.KindServiceAccount, TenantID: tid}
	_, _, rerr := snap.Resolve(p, ta, in.Provider, in.Model)

	out := &adminapi.Capability{Provider: in.Provider, Model: in.Model, Reasons: []string{}}
	out.Checks.ProviderAvailable = !errors.Is(rerr, catalog.ErrProviderUnavailable)
	out.Checks.ModelAllowed = rerr == nil
	out.Checks.Budget = adminapi.BudgetAvailabilityNotEnforced
	switch {
	case errors.Is(rerr, catalog.ErrProviderUnavailable):
		out.Reasons = append(out.Reasons, "provider_unavailable")
	case errors.Is(rerr, catalog.ErrModelNotAllowed):
		out.Reasons = append(out.Reasons, "model_not_allowed")
	case rerr != nil:
		return nil, rerr
	}

	def, found, err := s.defaultCredential(ctx, tid, in.Provider)
	if err != nil {
		return nil, err
	}
	out.Checks.Credential = adminapi.CredentialAvailabilityMissing
	if found {
		out.Checks.CredentialID = def.ID
		out.Checks.CredentialValidated = def.ValidatedAt != nil
		switch def.State {
		case "disabled":
			out.Checks.Credential = adminapi.CredentialAvailabilityDisabled
			out.Reasons = append(out.Reasons, "credential_disabled")
		case "revoked":
			out.Checks.Credential = adminapi.CredentialAvailabilityRevoked
			out.Reasons = append(out.Reasons, "credential_revoked")
		default:
			out.Checks.Credential = adminapi.CredentialAvailabilityActive
		}
	} else {
		out.Reasons = append(out.Reasons, "no_credential")
	}
	out.Callable = len(out.Reasons) == 0
	out.CheckedAt = time.Now().UTC()
	return out, nil
}

// defaultCredential returns the metadata of the tenant's default credential of a provider, read
// through the tenant's session; the sealed secret is never read.
func (s *server) defaultCredential(ctx context.Context, tid tenant.ID, provider string) (queries.PandokosProviderCredentialMetadatum, bool, error) {
	var def queries.PandokosProviderCredentialMetadatum
	var found bool
	err := s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		q := queries.New(tx)
		after, afterKey := false, ""
		for !found {
			rows, err := q.ListCredentials(ctx, queries.ListCredentialsParams{
				Provider: provider, After: after, Sort: "created_at", AfterID: afterID(after, afterKey), PageSize: credentialPage,
			})
			if err != nil {
				return err
			}
			for _, r := range rows {
				if r.IsDefault {
					def, found = r, true
					break
				}
			}
			if len(rows) < credentialPage {
				break
			}
			after, afterKey = true, rows[len(rows)-1].ID
		}
		return nil
	})
	return def, found, err
}
