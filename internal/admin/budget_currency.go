package admin

import (
	"context"
	"slices"
	"strings"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// budgetScope is the scope of a cost rule: a nil ProviderID is every model, a nil ModelID is every model of
// the provider. TenantCurrencies is, for a ceiling of one tenant, its active and pending currency, resolved.
type budgetScope struct {
	ProviderID, ModelID *string
	TenantCurrencies    []string
}

// budgetCurrencies is the one definition of the currencies a platform budget (a ceiling) of a scope can be
// written in (sprint 5.3, section 5.8, and sprint 5.4, section 5.7): those of the catalogue prices that can
// be in force for the scope, for each model the version in force now and every version that starts later,
// plus the active and the pending currency of the tenant a ceiling is for. The platform never reads a
// tenant's price lists, so none is read here. Sorted, without duplicates.
func budgetCurrencies(ctx context.Context, q *queries.Queries, s budgetScope) ([]string, error) {
	set, err := q.ListPriceCurrenciesForScope(ctx, queries.ListPriceCurrenciesForScopeParams{ProviderID: s.ProviderID, ModelID: s.ModelID, VisibleOnly: false})
	if err != nil {
		return nil, err
	}
	set = append(set, s.TenantCurrencies...)
	slices.Sort(set)
	return slices.Compact(set), nil
}

// tenantBudgetCurrencies is the set of a tenant's own rule: the currencies of the catalogue's prices and of
// the tenant's own price lists that can be in force for the scope, for the models the tenant can be given.
// It runs in the session of the tenant and is called only from the handlers of its rules, inside the session
// (T-R52); they refuse with refuseCurrency.
func tenantBudgetCurrencies(ctx context.Context, q *queries.Queries, s budgetScope) ([]string, error) {
	return q.ListBudgetCurrenciesForTenantScope(ctx, queries.ListBudgetCurrenciesForTenantScopeParams{ProviderID: s.ProviderID, ModelID: s.ModelID})
}

// checkBudgetCurrency refuses a platform budget whose currency no price of its scope uses (see
// budgetCurrencies): a 400 on currency that lists the currencies in use, or says that no model of the scope
// has a price. A rule that is not a budget is not checked.
func checkBudgetCurrency(ctx context.Context, q *queries.Queries, metric, currency string, scope budgetScope) error {
	if !costMetric(adminapi.LimitMetric(metric)) {
		return nil
	}
	set, err := budgetCurrencies(ctx, q, scope)
	if err != nil {
		return err
	}
	return refuseCurrency(set, currency)
}

// refuseCurrency is nil when currency is in set; its two messages are those of sprint 5.3.
func refuseCurrency(set []string, currency string) error {
	for _, c := range set {
		if c == currency {
			return nil
		}
	}
	if len(set) == 0 {
		return invalid("currency", "no model of this scope has a price")
	}
	return invalid("currency", "must be a currency the prices of this scope use: "+strings.Join(set, ", "))
}

// platformBudgetScope is the scope of a platform ceiling: for a ceiling of one tenant (tenantID not nil)
// the active and the pending currency of that tenant, read from its row in the platform transaction and
// resolved with the default currency. Nothing of the tenant's price lists is read, and no tenant session
// is opened (sprint 5.4, section 5.7, T-R52).
func (s *server) platformBudgetScope(ctx context.Context, q *queries.Queries, providerID, modelID, tenantID *string) (budgetScope, error) {
	sc := budgetScope{ProviderID: providerID, ModelID: modelID}
	if tenantID == nil {
		return sc, nil
	}
	row, err := q.GetTenant(ctx, *tenantID)
	if isNoRowsErr(err) {
		return budgetScope{}, invalid("tenant", noSuchRuleTenant)
	}
	if err != nil {
		return budgetScope{}, err
	}
	a := pricing.ResolveActive(row.ActiveCurrency, row.ActiveCurrencyPrevious, row.ActiveCurrencyEffectiveFrom, s.defaultCurrency)
	sc.TenantCurrencies = []string{a.Current}
	if !a.From.IsZero() && s.now().Before(a.From) {
		sc.TenantCurrencies = append(sc.TenantCurrencies, a.Previous)
	}
	return sc, nil
}
