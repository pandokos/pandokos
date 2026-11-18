package admin

import (
	"context"
	"strings"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/store/queries"
)

// budgetScope is the scope of a cost rule and whose rule it is.
type budgetScope struct {
	ProviderID, ModelID *string
	Tenant              bool     // a rule of the tenant whose session q runs in
	TenantCurrencies    []string // for a ceiling of one tenant: its active and pending currency, resolved
}

// budgetCurrencies is the one definition of the currencies a budget of a scope can be written in
// (sprint 5.3, section 5.8): those of the catalogue prices that can be in force for the scope, that is
// for each model of the scope the version in force now and every version that starts later, sorted.
// A nil ProviderID is every model; a nil ModelID is every model of the provider. For a tenant rule
// the models are those the tenant can be given. Sprint 5.4 adds the currencies of the tenant's price
// lists here (C5) and nowhere else; until then the answer is that of 5.3.
func budgetCurrencies(ctx context.Context, q *queries.Queries, s budgetScope) ([]string, error) {
	return q.ListPriceCurrenciesForScope(ctx, queries.ListPriceCurrenciesForScopeParams{
		ProviderID: s.ProviderID, ModelID: s.ModelID, VisibleOnly: s.Tenant,
	})
}

// checkBudgetCurrency refuses a budget whose currency no price of its scope uses: a 400 on currency
// that lists the currencies in use, or says that no model of the scope has a price. A rule that is not
// a budget is not checked.
func checkBudgetCurrency(ctx context.Context, q *queries.Queries, metric, currency string, scope budgetScope) error {
	if !costMetric(adminapi.LimitMetric(metric)) {
		return nil
	}
	set, err := budgetCurrencies(ctx, q, scope)
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
