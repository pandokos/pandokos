# 0053. Tenant price lists and the active currency

- Status: proposed
- Date: 2026-11-18

Refines [ADR 0036](0036-prices-costs-and-importer.md) and
[ADR 0050](0050-quotas-and-budgets-periods-attribution-currencies.md). Requirement CAT-11.

## Context

Catalogue prices are global: one price per model for every tenant, imported in USD or overridden
by the operator for everyone (0036). Budgets do not convert (0050). A tenant could neither budget
in its own currency nor record the cost of a contract price (issue #48). The maintainer's review
of 2026-11-18 decided who owns such prices and how a currency is chosen.

## Decision

1. A tenant holds price lists of its own, one per currency: versions for models it can see, with
   the rules of 0036 (immutable, starting now or later, rates per category with tiers and
   fallbacks, exact decimals). A version is a price or an end. They are stored in two
   tenant-scoped tables; only the tenant reads and writes them (`price_list:*`). The platform has
   no permission, route, policy or function that reads or writes them.
2. A tenant has one active currency, stored on the tenant; the default is the installation's
   (`pricing.default_currency`, USD). Every cost of the tenant is computed in it: with the
   tenant's list in that currency where it prices the model, otherwise with the catalogue's
   version in force when it is in that currency; otherwise the cost is unknown. Lists in other
   currencies are kept and unused. Nothing is converted and no two currencies are added. One
   function (`pricing.List`) decides.
3. Only the tenant changes its active currency, and only to a currency that has a list (its own
   or the catalogue's). A change takes effect at the start of the next month in the tenant's
   time zone, the instant of a zone change, so that no budget is freed.
4. Only cost limits in the active currency apply to a tenant: its budgets and the platform's
   ceilings alike. The others are inactive. A rule is one per subject, scope, metric and
   currency, so that a change can be prepared.
5. A request has one cost. A ceiling in the active currency counts the cost at the tenant's
   price. The platform reads a tenant's active currency, not its prices.
6. The usage record states the price version and the list it belongs to (`price_source`). The
   ledger has no foreign key to a version; a version that has started cannot be removed.
7. The gateway keeps a tenant's currency and prices in the tenant's entry of the catalogue
   cache, and the currency in its rule set.

## Consequences

- A tenant budgets and reads its costs in one currency of its choice, at its own prices; other
  tenants, imports and overrides are unaffected.
- A catalogue price in another currency than a tenant's active currency does not price that
  tenant's requests.
- A tenant that changes its currency leaves the platform's cost ceilings in the old currency
  behind; the platform sees the change coming and can set a ceiling in the new one. Ceilings in
  requests and tokens do not depend on currencies.
- Under BYOK a tenant list that is wrong makes the tenant's own figures wrong.
- A model that a tenant has priced is retired, not deleted.

## Alternatives considered

**A global override in the tenant's currency.** Changes the price for every tenant and stops
imports for the model.

**Lists written or read by the platform, or a grant from the platform.** Rejected in the review:
a tenant's prices are the tenant's.

**Precedence of the tenant's price whatever its currency.** Costs of one tenant in several
currencies within a period, budgets that silently stop measuring some models.

**A second cost per request at the catalogue price, for the platform's ceilings.** Two costs in
the estimate, the settlement, the ledger and the counters.

**A change of currency that applies at once.** Frees the budgets of the period in progress.

**A `valid_until` on a version.** Ending a list would update a version.

## History

- 2026-11-18: proposed with sprint 5.4, after the maintainer's review of the same day.
