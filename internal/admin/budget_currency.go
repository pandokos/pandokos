package admin

import (
	"context"
	"strings"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// budgetCurrencies is the one definition of the currencies a budget of a scope can be written in
// (sprint 5.3, section 5.8): those of the catalogue prices that can be in force for the scope, that is
// for each model of the scope the version in force now and every version that starts later, sorted.
// A nil providerID is every model; a nil modelID is every model of the provider. For a tenant session
// the models are those the tenant can be given. Sprint 5.4 adds the currencies of the tenant's price
// list to the set of a tenant rule here and nowhere else.
func budgetCurrencies(ctx context.Context, q *queries.Queries, providerID, modelID *string, tenantSession bool) ([]string, error) {
	return q.ListPriceCurrenciesForScope(ctx, queries.ListPriceCurrenciesForScopeParams{
		ProviderID: providerID, ModelID: modelID, VisibleOnly: tenantSession,
	})
}

// checkBudgetCurrency refuses a budget whose currency no price of its scope uses: a 400 on currency
// that lists the currencies in use, or says that no model of the scope has a price. A rule that is not
// a budget is not checked.
func checkBudgetCurrency(ctx context.Context, q *queries.Queries, metric, currency, providerID, modelID string, tenantSession bool) error {
	if !costMetric(adminapi.LimitMetric(metric)) {
		return nil
	}
	set, err := budgetCurrencies(ctx, q, nilIfEmpty(providerID), nilIfEmpty(modelID), tenantSession)
	if err != nil {
		return err
	}
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
