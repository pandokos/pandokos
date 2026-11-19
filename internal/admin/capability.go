package admin

import (
	"context"
	"errors"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/pricing"
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
	ta, err := catalog.LoadTenantAccess(ctx, s.db, tid, s.defaultCurrency)
	if err != nil {
		return nil, err
	}
	p := authz.Principal{Kind: authz.KindServiceAccount, TenantID: tid}
	model, _, rerr := snap.Resolve(p, ta, in.Provider, in.Model)

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
	now := s.now().UTC()
	budget, reasons, err := s.budgetCheck(ctx, tid, snap, model, rerr == nil, in.Provider, in.Model, now)
	if err != nil {
		return nil, err
	}
	out.Checks.Budget = budget
	out.Reasons = append(out.Reasons, reasons...)
	out.Callable = len(out.Reasons) == 0
	out.CheckedAt = now
	return out, nil
}

// budgetCheck is checks.budget (sprint 5.2, section 5.13): the enforced period limits of the tenant
// that match the provider and the model, read from the ledger on the worker pool (inside the tenant session, under RLS), never from the
// counters of the gateway. As the rest of the check it describes a key of the tenant without pin
// and model list, so the rules of service accounts and keys, soft rules and rate limits take no
// part; requests in flight are not counted.
func (s *server) budgetCheck(ctx context.Context, tid tenant.ID, snap *catalog.Snapshot, m catalog.Model, resolved bool,
	provider, model string, now time.Time) (adminapi.BudgetAvailability, []string, error) {
	zones := s.limits.Zones
	if zones == nil {
		return "", nil, errors.New("admin: the capability check needs the zones of the installation")
	}
	rules, err := limits.LoadRules(ctx, s.db, tid, zones, s.defaultCurrency)
	if err != nil {
		return "", nil, err
	}
	// The rules are matched by identifier: the provider is looked up by name in the snapshot, the
	// model is the one the snapshot resolved (none when it did not).
	q := limits.Query{Provider: provider, Model: model, Start: now}
	if prov, ok := snap.Provider(provider); ok {
		q.ProviderID = prov.ID
		if resolved {
			q.ModelID = m.ID
		}
	}
	var matching []limits.Limit
	for _, l := range rules.For(q) {
		if _, period := l.Metric.Period(); period && l.Max > 0 && l.Subject == limits.SubjectTenant {
			matching = append(matching, l)
		}
	}
	if len(matching) == 0 {
		return adminapi.BudgetAvailabilityNotEnforced, nil, nil
	}

	unpriced := false
	if s.limits.OnUnknownPrice != "allow" && resolved {
		version, found := pricing.VersionAt(snap.Versions(m.ID), now)
		for _, l := range matching {
			if l.Metric.Cost() && (!found || version.Currency != l.Currency) {
				unpriced = true
			}
		}
	}
	exhausted := false
	ledger := s.limits.Ledger
	byPeriod := map[limits.Period][]limits.UsageGroup{}
	for _, l := range matching {
		rows, read := byPeriod[l.Period]
		if !read {
			if rows, err = ledger.Usage(ctx, tid, l.Period.Start, now, now); err != nil {
				return "", nil, err
			}
			byPeriod[l.Period] = rows
		}
		used := limits.Sum(l, rows)
		if l.Metric.Cost() {
			exhausted = exhausted || used.Cost.Cmp(amountDecimal(l.Max)) >= 0
		} else {
			exhausted = exhausted || used.N >= l.Max
		}
	}
	var reasons []string
	value := adminapi.BudgetAvailabilityAvailable
	if unpriced {
		value, reasons = adminapi.BudgetAvailabilityUnpriced, append(reasons, "no_price")
	}
	if exhausted {
		if !unpriced {
			value = adminapi.BudgetAvailabilityExhausted
		}
		reasons = append(reasons, "budget_exhausted")
	}
	return value, reasons, nil
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
