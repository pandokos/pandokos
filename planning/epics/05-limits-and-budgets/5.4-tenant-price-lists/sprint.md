---
id: SPRINT-5.4
title: Tenant price lists
epic: EPIC-5
release: v0.2
status: in-progress
design: draft 2026-11-18, second version after the maintainer's review of the same day
---

# Sprint 5.4 — Tenant price lists

- Epic: [5 — Limits and budgets](../epic.md)
- Implementation plan: [plan.md](plan.md)
- Previous sprint: [5.3 — Integrity of limit rules](../5.3-integrity-of-limit-rules/sprint.md)
- Need: issue #48. Scope: the epic file, section 5.4, as revised by the maintainer on 2026-11-18;
  it supersedes the issue where they differ.
- Risk class: **high** (a second source for the price of a request and a currency per tenant on
  the reservation path; a rule of which budgets apply inside the limiter's rule set; two new
  tenant-scoped tables; changes of the `tenant` row, of both rule tables and of the usage
  ledger; what the operator's logs and traces may say about a tenant)
- Decisions implemented: two new records, **ADR 0053** (tenant price lists and the active
  currency) and **ADR 0054** (usage data is the tenant's); ADR
  [0036](../../../../docs/adr/0036-prices-costs-and-importer.md) (the rules of a price version,
  applied to a second table);
  [0050](../../../../docs/adr/0050-quotas-and-budgets-periods-attribution-currencies.md) as
  amended here; [0048](../../../../docs/adr/0048-reserve-and-settle.md) (one cost per request);
  [0049](../../../../docs/adr/0049-limit-rules.md) and
  [0052](../../../../docs/adr/0052-administration-of-limits.md) as amended here (one rule per
  currency); [0011](../../../../docs/adr/0011-pricing-source.md),
  [0012](../../../../docs/adr/0012-limits-and-budget-reservation.md);
  [0013](../../../../docs/adr/0013-ledger-and-audit.md),
  [0032](../../../../docs/adr/0032-audit-log-first-version.md);
  [0015](../../../../docs/adr/0015-observability.md) and
  [0040](../../../../docs/adr/0040-traces.md) (what logs and spans carry);
  [0019](../../../../docs/adr/0019-configuration.md) (two keys);
  [0020](../../../../docs/adr/0020-error-contract.md) (no new code);
  [0026](../../../../docs/adr/0026-schema-migrations.md),
  [0027](../../../../docs/adr/0027-database-roles-and-session-context.md),
  [0029](../../../../docs/adr/0029-static-sql-sqlc.md) (migration 0012, no definer function);
  [0031](../../../../docs/adr/0031-authorization-core-and-provisioning.md) (three permissions);
  [0035](../../../../docs/adr/0035-catalogue-and-model-access.md) (the per-tenant entry of the
  catalogue cache); [0039](../../../../docs/adr/0039-usage-record-admission-and-ledger-writer.md);
  [0043](../../../../docs/adr/0043-tenant-capability-check-and-usage-list.md);
  [0051](../../../../docs/adr/0051-period-counters-and-the-ledger.md) (unchanged)
- Decision records changed by this sprint: new ADR 0053 and 0054; an **Amendment (sprint 5.4)**
  section and a history line on 0049, 0050 and 0052; history lines on 0011, 0015, 0031, 0035,
  0036, 0039, 0040, 0043. Text in [plan.md](plan.md), part E.

This document is the design. The implementer follows [plan.md](plan.md); a gap in either document
is raised as a question, not filled silently ([planning/README.md](../../../README.md)).

| Decided by the maintainer (2026-11-18) | Designed in |
|---|---|
| Only the tenant writes and reads its price lists; no platform door, no grant | §5.1, §5.5, D-1 |
| One list per currency; an active currency per tenant selects the list of every cost | §5.2, D-3 |
| A change needs a list in that currency and takes effect at the start of the next month of the tenant's zone | §5.3, D-4, D-5 |
| Only budgets and ceilings in the active currency apply; a budget may be written in any currency that has a list | §5.6, §5.7, D-6, D-7 |
| One cost per request; a ceiling in the active currency measures the tenant's price | §5.6, D-8 |
| The tenant's usage data is reserved to the tenant; logs and traces without cost, currency and tokens | §5.10, §5.11, D-15, D-16 |
| CAT-11; USE-06, USE-07, AZ-06; notes on 6.1 and 6.2; issue #33 in the security model; ADR 0053 and 0054 | §5.12, plan part E |

## 1. Outcome

1. A tenant holds **price lists of its own, one per currency**, for models it can see, with the
   rules of the catalogue's versions: immutable, starting now or later, rates per category with
   tiers and fallbacks, exact decimals. Only the tenant writes and reads them.
2. A tenant has one **active currency**. Every cost of the tenant is computed in it: with the
   tenant's list in that currency where it prices the model, otherwise with the catalogue price
   when that is in the currency; otherwise the cost is unknown. Nothing is converted and no two
   currencies are ever added.
3. **Only budgets and ceilings in the active currency apply.** A change of the active currency
   takes effect at the start of the next month of the tenant's zone, so it never frees a budget.
4. Every usage record says which price list its price version belongs to (`catalogue` or
   `tenant`).
5. **Through the product, the platform reads no usage record, limit event or price list of a
   tenant**, and the request log and the traces carry no cost, currency or token count unless
   the operator turns that on.

## 2. Scope

### In scope

- Migration 0012: `tenant_price_version`, `tenant_price_rate` (tenant-scoped); three columns, a
  guard and a notification on `tenant`; the unique keys of both rule tables gain the currency;
  `usage_record.price_source`.
- `internal/pricing`: the one definition of the price in force for a tenant (`List`, `Active`).
- `internal/catalog`, `internal/resolver`, `internal/gateway`: the tenant's currency and prices
  in its entry of the catalogue cache; the estimate and the record priced through `List`; the
  log line and the spans without usage details.
- `internal/limits` (`rules.go`, `load.go` only): one rule per currency; cost limits outside the
  active currency are left out of what applies to a request.
- Administration API: seven operations, three permissions, three audit actions; changed
  behaviour of the rule operations (currency check, coexistence, `active`), `getCapability`,
  `listTenantModels`, `listUsageRecords`, `getTenant`, `listTenants`, `deleteModel`; CLI.
- Two configuration keys: `pricing.default_currency`, `telemetry.tenant_usage_details`.
- Negative tests of what a platform session and a platform principal can read.
- Tests at every level; the upgrade from a database of sprint 5.3.
- Documentation, ADR 0053 and 0054, amendments, requirements CAT-11, USE-06, USE-07, AZ-06, the
  notes on sprints 6.1 and 6.2.

### Not in scope

| Item | Where |
|---|---|
| Currency conversion; exchange rates | not planned (ADR 0050, option A) |
| Automatic retrieval of contract prices from providers | not planned |
| Prices per key or per service account | not planned: a list belongs to a tenant |
| Any way for the platform to read or write a tenant's prices | rejected in the review |
| Confidentiality against whoever administers the database, the hosts or the backups | out of scope (issue #33); stated in the security model |
| Tenant reports and rollups; export of usage | 6.2 (§11) |
| Tenant price lists in `pandokos export` / `apply` (CAT-10) | 6.2 (§11) |
| Tenant data export and deletion | 6.3 (§11) |
| Metrics | 6.1 (§11: no tenant label on cost and token metrics) |
| A notification to the platform that a tenant changes its active currency | 6.4 (O-4) |
| A foreign key from the usage ledger to a price version | not planned (D-11) |

## 3. Requirements claimed

The meaning is quoted from [docs/requirements](../../../../docs/requirements/README.md). CAT-11
is added, and USE-06, USE-07 and AZ-06 are reworded, by this sprint (plan part E); the new
wording is quoted.

| ID | Meaning | Delivered here | Extent |
|---|---|---|---|
| **CAT-11** (new) | "A tenant can hold price lists of its own, one per currency, for the models it can use. Its active currency selects the list: where that list prices a model it takes precedence over the catalogue price in that currency. Only the tenant reads and writes its lists" | §5.1 to §5.5 | full |
| CAT-08 | "Prices are versioned in time, with a currency. The cost of a request uses the price in force when it was made. When no price is known the usage is recorded with an unknown cost" | One function decides the price in force for a tenant; a model priced in neither list of the active currency has an unknown cost; the record names the version and its list | extended |
| LIM-03 | "Monetary budgets per period" | Budgets in the tenant's currency; only those in the active currency apply; a change never frees one (§5.3, §5.6) | completes 5.2 and 5.3 |
| **USE-06** (reworded) | "Aggregated reports are available to the tenant, by period, provider, model and caller" | Nothing is built here; the wording and the plan of 6.2 change (§5.12, §11) | wording |
| **USE-07** (reworded) | "Tenant roles read their tenant's usage. Platform roles read no usage of a tenant: they see operational indicators of the installation, with no tenant and no cost" | The database and the API already hold it; negative tests for platform sessions and principals; logs and traces (§5.10, §5.11) | full for what exists; metrics come with 6.1 |
| **AZ-06** (reworded) | "Platform roles are separate from tenant roles and imply no tenant membership: platform admin, platform auditor. No platform role reads the usage or the costs of a tenant" | No platform permission reads usage, events or prices (T-747, T-R52) | for the roles that exist |
| AUD-01, AUD-03 | Every change audited, atomically | `tenant_price.create`, `tenant_price.delete`, `tenant.active_currency_update` (§5.9) | for the new resources |
| TEN-04, TEN-06, TEN-07 | Row-level security on tenant data; isolation "uniformly for … usage … caches …"; negative tests for every tenant-scoped table and route | Two tables with the canonical policy, fixtures and tests; the `tenant` row; every route has its cross-tenant test (§6) | full for what this sprint adds |

Not claimed: USE-03 (the record gains a field the requirement does not list), USE-08, CAT-09
(untouched: T-760 shows it), CAT-10, TEN-11, TEN-12, OBS-*.

## 4. Repository layout

New:

```
internal/store/migrations/sql/0012_tenant_price_lists.sql
internal/store/queries/sql/tenant_prices.sql
internal/pricing/list.go            Active, List, the source of a cost
internal/admin/tenant_prices.go     the five operations on versions and lists
internal/admin/active_currency.go   the two operations and the change rule
internal/cli/tenant_prices.go
docs/adr/0053-tenant-price-lists-and-active-currency.md
docs/adr/0054-usage-data-is-the-tenants.md
```

Changed: `internal/pricing/{price,cost}.go`, `internal/catalog/{access,load,cache}.go`,
`internal/resolver/{catalogue,limits}.go`, `internal/gateway/{ports,estimate,record,limits,
handler,spans}.go`, `internal/limits/{rules,load}.go`, `internal/ledger/rows.go`,
`internal/config`, `internal/admin/{budget_currency,limits,platform_limits,limit_fields,
capability,model_access,usage,tenants,convert_catalog,admin}.go`, `internal/authz`,
`internal/cli/{commands,limits,capability}.go`, `cmd/pandokos/serve.go`,
`api/openapi/admin.v1.yaml`, `internal/store/queries/sql/{usage,limits,tenants}.sql`,
`test/isolation`, `test/dbtest`, `test/apitest`, `test/limitstest`, generated code,
`migrations.sum`, documentation.

Not changed: the counters, the reconciler and the ledger reader of `internal/limits`; the
importer; `internal/admin/prices.go`. No new Go module, no new error code, no definer function.

## 5. Architecture

### 5.1 The tables (migration 0012; plan appendix A1)

| Table | Change |
|---|---|
| `tenant_price_version` (new, tenant-scoped, canonical policy) | `(tenant_id, id)`, `model_id`, `currency`, `valid_from`, `kind`, `note`, `created_at`. The versions of one currency are one list |
| `tenant_price_rate` (new, tenant-scoped, canonical policy) | `(tenant_id, price_version_id, category, threshold)`, `rate numeric(24,12)` |
| `tenant` | `active_currency`, `active_currency_previous` (`''` or three capital letters), `active_currency_effective_from` |
| `limit_rule`, `platform_limit_rule` | the unique key gains `currency` |
| `usage_record` | `price_source text`, nullable |

| Constraint or trigger | Meaning |
|---|---|
| `tenant_price_version_valid_from_key` | unique `(tenant_id, currency, model_id, valid_from)` |
| `tenant_price_version_model_fkey` | `model_id` references `model (id)`, `ON DELETE RESTRICT` (§5.8) |
| trigger `tenant_price_version_guard` | for every role: a version does not start before `now()`; it is never updated; it is deleted only before it starts |
| `tenant_price_rate_version_fkey` | `(tenant_id, price_version_id)` references the version, `ON DELETE CASCADE` |
| constraint triggers `tenant_price_version_shape`, `tenant_price_rate_shape` (deferred) | at commit: a `price` has at least one rate and a base rate for each category it prices; an `end` has none; rates were written in the transaction that created their version |
| trigger `tenant_active_currency_guard` | for every role: **only a session of the tenant itself** changes the three currency columns; the currency in force changes only through a pending change whose instant lies in the future (at most 32 days); a pending change that is replaced keeps its instant; a tenant is created with the default |
| trigger `tenant_active_currency_notify` | `catalog:<tenant>` and `limits:<tenant>` when a currency column changes |
| `limit_rule_key`, `platform_limit_rule_key` | one rule per subject, scope, metric **and currency** (`''` for count metrics, so nothing changes for them) |
| `usage_record_price_source_valid` (`NOT VALID`) | NULL, or `catalogue` or `tenant` with a price version ID |
| triggers `…_no_truncate`, `tenant_price_rate_immutable`, `tenant_price_version_notify` | no truncation; rates never change; `catalog:<tenant>` on insert and delete |

Grants: the gateway and the admin role read both price tables; the admin role inserts versions
and rates and deletes versions; nobody updates; the worker holds nothing. The admin role may
update the three currency columns of `tenant` (through the policy `tenant_self_update` of
migration 0011).

**No policy shows a price row to the platform scope and no operation of the platform opens a
tenant's session to read one** (D-1). The only place where the platform meets a tenant's prices
is a refusal: a model that a version references cannot be deleted (§5.8).

### 5.2 Which price applies (D-2, D-3, D-10)

A tenant has, at every instant *t*, one **active currency** *C(t)*: `tenant.active_currency`, or
`active_currency_previous` while a change is pending; `''` is the default of the installation,
`pricing.default_currency` (`USD` unless configured) — the currency of the tenants that chose
none, as `limits.time_zone` is their zone.

For a tenant, a model and an instant *t*:

1. in the tenant's list in *C(t)*, the version of the model in force at *t* (the greatest
   `valid_from ≤ t`), when it exists and its kind is `price`, is the price — source `tenant`;
2. otherwise the catalogue's version of the model in force at *t* (ADR 0036), **when its
   currency is *C(t)***, is the price — source `catalogue`;
3. otherwise there is no price: the cost is unknown (`no_price`), and
   `limits.on_unknown_price` decides when a budget applies (§5.6).

Lists in other currencies are stored and take no part. A version of kind **`end`** stops a list
for one model: from its start rule 2 applies again. It is kept from the first design because a
list must be able to stop pricing a model without a version being updated (O-6).

A tenant that never chose a currency and has no list is priced exactly as before this sprint, as
long as the catalogue's prices are in the default currency. A catalogue price in another currency
(an operator override, catalogue guide section 6) no longer prices requests of tenants whose
active currency differs: their cost for that model is unknown. This is the rule "never mix
currencies" applied to the catalogue too; §5.13 says what an installation with such overrides
does on upgrade (R-2).

One function decides, and nothing else may:

```go
// internal/pricing/list.go
const (
    SourceCatalogue = "catalogue"
    SourceTenant    = "tenant"
)

// Active is the active currency of a tenant: Previous before From, Current from it. From is zero
// and Previous empty when no change is pending. Both are resolved codes, never "".
type Active struct {
    Current, Previous string
    From              time.Time
}
func (a Active) At(t time.Time) string

// List is the prices of one model for one tenant. No slice need be sorted.
type List struct {
    Currency  Active
    Catalogue []Version // the catalogue's versions of the model, of any currency
    Tenant    []Version // the tenant's versions of the model, of the currencies of Currency; End marks an end
}

// At returns the version in force at t and the list it belongs to (the rule of section 5.2).
func (l List) At(t time.Time) (v Version, source string, found bool)

// Cost is CostOf(u, version in force at t), with Cost.Source set whenever Cost.VersionID is.
func (l List) Cost(u family.Usage, t time.Time) Cost
```

`Version` gains `End bool`; `Cost` gains `Source string`. Tiers, fallbacks and exact arithmetic
are those of `CostOf`. A `List` whose `Currency` is the zero value prices from the catalogue
whatever its currency (the behaviour of 5.3; unit fixtures that know no tenant rely on it).
`pricing.VersionAt` is called inside `internal/pricing` only (T-R51).

### 5.3 Changing the active currency (D-4, D-5)

| Operation | Method and path | Permission | Audit |
|---|---|---|---|
| `getActiveCurrency` | `GET /v1/tenants/{tenant}/active-currency` | `price_list:read` | — |
| `setActiveCurrency` | `PUT /v1/tenants/{tenant}/active-currency` | `price_list:write` | `tenant.active_currency_update` |

- **Who.** The tenant only. The platform sets nothing: `createTenant` and `updateTenant` do not
  take the currency, and the trigger refuses a change from any session but the tenant's.
- **Permission** (D-4): `price_list:write`. The active currency is the choice of the price list;
  whoever may write the prices of the active list can already change what every budget counts,
  so the two belong to one permission. Its effect on budgets is not hidden from the holder of
  `limit:*`: the rule resources say which rules are active (§5.7), and the change waits for the
  next month.
- **Precondition.** `PUT {"currency": "EUR"}` is accepted only when **a list in that currency
  exists** for the tenant: a price of its own in it, or a catalogue price in it for a model the
  tenant can be given — in both cases a version in force or starting later (`CurrencyHasList`).
  Otherwise 400 on `currency`: `no price list in this currency: add prices in it first`. The empty
  string, the default of the installation, is always accepted.
- **The change rule is that of the time zone** (5.3 §5.7), with the same instant. A change made
  at *t₀* takes effect at **E = the start of the next month in the time zone in force at *t₀***
  (`limits.NextChange`). Until *E* nothing changes. A second change before *E* replaces the
  pending currency and keeps *E*; setting the currency in force again withdraws the pending
  change. When a zone change is pending too, both take effect at the same *E*.
- **Why no budget is freed.** Every period of a day or a month that is in progress at *t₀* ends
  at or before *E* (a month of the zone in force ends exactly at *E*, and so does its last day).
  Until *E* the currency in force, the prices and the rules that apply are what they were. From
  *E* the budgets of the new currency start whole periods, counted from the ledger over their
  range like any counter (5.2 §5.6): the costs recorded before *E* are in the old currency and
  are not theirs. No budget stops applying in the middle of one of its periods, and none starts
  with a period already running.
- **The answer** (`ActiveCurrency`): `currency` (as set; empty for the default), `in_force` (the
  resolved code in force now), `effective_from` (while a change is pending) and, in the answer to
  a change, **`inactive_limits`**: every cost rule of the tenant (any subject) and every platform
  ceiling that applies to it whose currency is not the new one — `level`, `rule_id`, `metric`,
  `currency`, at most 100, a default ceiling replaced by the tenant's own row left out. From *E*
  they count and refuse nothing.
- The tenant row is updated in the tenant's session, in one transaction with the audit row. The
  two caches of the gateway are notified.
- The platform reads a tenant's active currency on the `Tenant` resource (`active_currency`,
  `active_currency_in_force`, `active_currency_effective_from`): it is a setting of the tenant,
  not usage and not a price, and the platform needs it to know which of its ceilings apply (D-8).

### 5.4 How the gateway gets a tenant's currency and prices (D-9)

The per-tenant entry of the catalogue cache (`catalog.TenantAccess`: grants and restriction,
loaded in one tenant transaction on the gateway pool, living for `cache.catalog_ttl`, dropped by
`catalog:<tenant>`) gains two members:

```go
type TenantAccess struct {
    Granted    map[string]bool
    Restricted bool
    Allowed    map[string]bool
    Currency   pricing.Active                // resolved with the default of the installation
    Prices     map[string][]pricing.Version  // the tenant's versions by model ID, of the currencies of Currency
}
```

- `LoadTenantAccess` reads the currency columns (`GetTenantActiveCurrency`) and then
  `LoadTenantPriceVersions` for the currency in force and, while a change is pending, the one
  that follows: per list and model, the version that was in force an hour ago and every later
  one. A scheduled version, and a pending change of currency, therefore take effect at their
  instant without any notification. The hour is the margin between the start of a request and
  the load that prices it.
- `Cache.ResolveIn` returns the `TenantAccess` it resolved with; `resolver.Catalogue.Resolve`
  fills `Target.Currency` and `Target.TenantPrices`; `Target.PriceList()` builds the `List`.
  `estimateOf` and `recordOf` price with `t.PriceList().Cost(usage, start)`: the estimate and the
  settlement of one request use the same version and the same currency.
- The rule set of a tenant (`limits.RuleSet`, the rule cache) carries the same `pricing.Active`,
  read with the rules and the calendar in their transaction (§5.6).
- A change reaches a gateway through the notifications of the two triggers, normally within a
  second, and within `cache.catalog_ttl` / `cache.limits_ttl` (30 s by default, 5 m at most)
  when they are not delivered. If the two caches of one gateway disagree for that time, the
  estimate is in one currency and the budgets that apply in another: the request meets
  `limits.on_unknown_price` (refused by default). A change of currency is known a month ahead
  unless it is made in the last minutes of a month.
- Cost on the request path: nothing for a cached tenant; two more statements in the load of a
  tenant's entry and one in the load of its rules.

### 5.5 The administration API of price lists (D-14)

| Operation | Method and path | Permission | Audit |
|---|---|---|---|
| `listPriceLists` | `GET /v1/tenants/{tenant}/price-lists` | `price_list:read` | — |
| `listPriceListVersions` | `GET /v1/tenants/{tenant}/prices` | `price_list:read` | — |
| `createPriceListVersion` | `POST /v1/tenants/{tenant}/prices` | `price_list:write` | `tenant_price.create` |
| `getPriceListVersion` | `GET /v1/tenants/{tenant}/prices/{price}` | `price_list:read` | — |
| `deletePriceListVersion` | `DELETE /v1/tenants/{tenant}/prices/{price}` | `price_list:delete` | `tenant_price.delete` |

All three permissions are tenant-scoped and held by `tenant-admin`; `inference` holds none; no
platform role holds any, and a platform principal cannot hold a tenant permission (ADR 0007).
There is no grant and no other condition.

**`TenantPriceList`** (one per currency in which the tenant has a version): `currency`,
`versions`, `models_priced` (models with a price in force now), `active` (its currency is the
one in force).

**`TenantPriceVersion`**: `id`, `model_id`, `provider`, `model`, `currency`, `kind` (`price`,
`end`), `valid_from`, `rates` (as `PriceRate`; empty for an end), `note`, `in_force` (the version
of its list and model in force now — whether the list is used is `active` of the list),
`created_at`.

**Creation** (`TenantPriceCreate`: `provider`, `model`, `currency` required; `kind` (default
`price`), `rates`, `valid_from`, `note`). In one tenant transaction, under the tenant's price
lock (`LockTenantPrices`):

1. the model: one the tenant can see — its provider enabled, not retired, available to all or
   granted to the tenant — else 400 on `model`: `must name a model available to this tenant`,
   the same answer for a model that is elsewhere and one that is nowhere;
2. the version: for a `price`, rates that pass `pricing.Version.Validate` (the messages of
   `createPriceOverride`); for an `end`, no rates;
3. the start: `valid_from`, or the database's clock read after the lock; never in the past (400
   on `valid_from`);
4. for an `end`: the version of the list and model in force at the start must be a `price`, else
   409 `invalid_state` (`errors`: field `kind`, `the list has no price in force for this model
   at that time`);
5. at most 50,000 versions per tenant (409 `invalid_state` beyond); the same list, model and
   start twice is 409 `conflict`;
6. the insert of the version and its rates, the audit row, the answer (201).

Writing in any currency is free: a list in a currency that is not active changes nothing until
the tenant switches to it. Writing in the active list changes the cost of the tenant's requests
for the model from the version's start, and with it what its budgets and the platform's ceilings
count.

**Deletion**: only a version that has not started (409 `invalid_state` otherwise); the answer is
the version as it was. **Lists of versions**: newest first; filters `currency`, `provider`,
`model` (needs `provider`) and `at` (the versions in force then, one per list and model); cursor
on the ID. **Read**: any version, replaced ones included. A price ID of another tenant is the 404
of an ID that does not exist.

### 5.6 Budgets and ceilings apply in the active currency only (D-6, D-8)

**The rule.** A cost limit — a budget of the tenant, of a service account or of a key, or a
ceiling of the platform — applies to a request of a tenant only when its currency is the
tenant's active currency at the request's start. A cost limit in another currency is
**inactive**: it is not evaluated, holds no counter, records no event, and never refuses.

**Where it is decided.** In the rule set, which already says which limits apply to a request:

```go
type RuleSet struct {
    // …
    Calendar TenantCalendar
    Currency pricing.Active // zero: unknown, no cost limit is left out (rule sets built without a tenant row)
}
```

- `RuleSet.For(q)` leaves out every cost limit whose currency is not `Currency.At(q.Start)`.
  Count limits are untouched.
- A rule is one per subject, scope, metric **and currency**, so a budget in the currency in
  force and one prepared in another coexist. A platform ceiling of the tenant replaces the
  default of the same scope, metric and currency.
- The loader reads the currency columns in the transaction of the rules and resolves `''`.

Everything below is unchanged: the resolver still asks whether a limit can measure the estimate
(`cannotMeasure`) — now true only for a request without a price in the active currency, or for
the minutes of §5.4 — and the store, the counters, the reconciler and the events see only limits
that apply.

**Consequences.**

- A request that only inactive cost limits match is `unlimited` (5.3 §5.5): nothing limited it,
  and the first of a day writes the unlimited-traffic rows.
- A model priced in neither list of the active currency: with an active budget or ceiling,
  `limits.on_unknown_price` (409 `no_price` by default); without one the request passes and its
  cost is recorded as unknown.
- **One cost per request** (ADR 0048). A platform ceiling in the tenant's active currency counts
  the cost at the tenant's price where its list prices the model. Under BYOK the tenant pays its
  provider; a tenant list that is wrong is the tenant's own account that is wrong.
- **A tenant that changes its active currency leaves the platform's cost ceilings in the old
  currency behind**, from the next month. The platform sees the pending change on the `Tenant`
  resource and can write a ceiling for that tenant in the new currency before it takes effect
  (§5.7). Ceilings in requests and tokens do not depend on currencies; the guide says that a
  platform that needs a ceiling no tenant can step out of sets it in tokens (R-1, O-4).

### 5.7 Writing rules: the currency check, coexistence, `active` (D-7)

**`budgetCurrencies`** stays the one definition of the currencies a cost rule of a scope can be
written in — "any currency that has a list":

| Rule | Set |
|---|---|
| A rule of a tenant | For the models of the scope the tenant can be given: the currencies of the tenant's own prices and of the catalogue's prices, in both cases of the version in force and of every version that starts later (`ListBudgetCurrenciesForTenantScope`, one statement in the tenant's session) |
| A ceiling of the platform for one tenant | The catalogue's set for every model of the scope (as in 5.3) **plus the tenant's active currency and its pending one**, read from the tenant row in the platform scope. The platform does not read the tenant's lists; a currency the tenant made active has a list by §5.3 |
| A default ceiling | The catalogue's set (unchanged) |

The answers are those of 5.3 §5.8. Whether the currency is the active one is not checked: a
budget in another currency is how a change is prepared.

**Coexistence.** `createLimit` and `createPlatformLimit` answer 409 `conflict` only for the same
subject, scope, metric and currency.

**`active`.** `LimitRule` gains `active` (boolean): false for a cost rule whose currency is not
the tenant's active currency in force now, true otherwise. `listTenantPlatformLimits` sets it on
the ceilings it shows. The platform's own view of its ceilings (`listPlatformLimits`) does not
carry it; the platform compares with the tenant's `active_currency_in_force`.

**The capability check** (`checks.budget`) uses the same rule set and `List.At(now)`:
`not_enforced` when no enforced quota or budget in force matches (inactive budgets do not
count); `unpriced` when an active budget matches and the model has no price in the active
currency; `available` and `exhausted` as before.

**`listTenantModels`** keeps `price` (the catalogue's version in force) and gains `tenant_price`
(the version of the tenant's active list in force, when it is a `price`) and `price_source`
(`tenant`, `catalogue`, or empty when neither applies in the active currency).

### 5.8 The catalogue (D-12, D-21)

- **Imports and overrides** do not read or write a tenant table. `model.price_override` and the
  import report know nothing of tenant lists.
- **Deleting a model** that a version of any tenant references is refused by the foreign key
  with 409 `invalid_state` and one `errors` entry: field `tenant_prices`, `tenant price lists
  reference this model; retire the model instead`. The platform learns that some tenant priced
  the model, and nothing else — not which tenant, not a currency, not a rate (O-5). A version
  that has started can never be removed, so such a model is retired, as a model with a started
  catalogue price already is.
- **A grant of a model that is revoked** leaves the tenant's versions for it; they price nothing
  while the model cannot be called.

### 5.9 Audit actions and details

All three in the tenant's log, written in the transaction of the change, with the tenant
principal as actor.

| Action | Resource | Detail |
|---|---|---|
| `tenant_price.create` | `tenant_price_version`, the ID | `provider`, `model`, `model_id`, `currency`, `kind`, `rates` (a price), `valid_from`, and `previous_version` when the list had a version in force for the model at the start |
| `tenant_price.delete` | `tenant_price_version`, the ID | `provider`, `model`, `model_id`, `currency`, `kind`, `valid_from` |
| `tenant.active_currency_update` | `tenant`, the tenant ID | `from`, `to`, `in_force`, `effective_from`, `inactive_limits` (the number) |

The `note` of a version is stored with it and is not copied into an audit detail or a log line.
The tenant's audit log is the tenant's; the platform's log gets no row from these operations.

### 5.10 The tenant's usage data in the database and the API (D-15)

What was verified in the code of sprint 5.3, and what this sprint adds to hold it:

| Path | State | Added |
|---|---|---|
| `usage_record` (and every partition), `limit_event` | forced row-level security with `tenant_isolation` only; the platform scope and a session without scope see no row | T-746: every runtime role, platform scope and no scope, direct and pooled, each partition by name |
| `tenant_price_version`, `tenant_price_rate` | the same, new | T-741, T-746 |
| Definer functions (eleven) | none reads or returns a row of these tables: they look up a key or an issuer registration, record an authentication failure or the unlimited-traffic rows, count mappings, delete rules of an issuer, maintain two directories, and create partitions (returning two installation-wide values) | T-746 pins their names and result types as read from the catalogue; `Spec.SecurityDefiner` is unchanged |
| The worker | reads `usage_record` only through `AggregateUsageForLimits`, in the session of the tenant whose counters it rebuilds; holds nothing on `limit_event` or the price tables; in the platform scope (imports) it sees no usage row | T-746; T-R52 |
| The administration API | usage, limit events, prices and the capability check are tenant-scoped operations (`usage:read`, `limit:read`, `price_list:*`, `model:read`); a platform principal cannot hold a tenant permission. The platform's operations that open a tenant's session read credential metadata and delete rules of an issuer, nothing else | T-747: a platform key calls every platform operation against a tenant that has usage, events and prices with canary values, and no answer contains one; T-R52: no handler of a platform operation calls a statement on these tables |
| The platform's audit log | holds no cost, token count or price; `limit.unlimited_traffic` says that a tenant sent traffic no rule limited, not how much | unchanged |
| Platform ceilings | the platform knows the ceilings it wrote, not what was counted against them | unchanged |

Not covered, and said so in the security model (issue #33): whoever holds the owner role or a
superuser of the database, the hosts or the backups reads everything. Row-level security
protects tenants from each other and from the product's platform roles, not from the operator
of the installation.

### 5.11 Logs and traces (D-16)

The request log line and the spans go to the operator. By default they no longer describe a
tenant's usage:

| Where | Removed by default |
|---|---|
| Request log line of the inference listener | `cost`, `currency`, the `usage` group (token counts by category), `reserved_tokens`, `limit` (the value of the limit that refused), `limit_currency`, `price_currency` |
| Client span | `pandokos.cost.amount`, `pandokos.cost.currency` |

What stays: request ID, record ID, trace ID, tenant, principal and key IDs, provider, endpoint,
model, operation, outcome, error code, statuses, durations, byte counts, `usage_source`,
`cost_status`, `limit_state`, and of a refusal `limit_subject`, `limit_metric`, `limit_level`,
`limit_scope`, `retry_after_s`, `estimate_over_limit`. The spans never carried token counts.

**`telemetry.tenant_usage_details`** (`PANDOKOS_TELEMETRY_TENANT_USAGE_DETAILS`, boolean,
default `false`) brings all of them back, in the log line and on the span, and adds
`price_source` next to `price_currency`. When it is on, `serve` logs one warning at start
(`configuration warning`, key and message): tenant usage is written to logs and traces that the
tenant cannot see. It is for single-tenant installations and for debugging.

No other log line of the gateway, the worker or the administration API carries an amount or a
count of a tenant (verified by reading; T-739 sweeps every line at level `debug` for canary
amounts). The troubleshooting of `no_price` moves from the operator's log to the tenant's own
tools: the capability check, `active` on its rules, `listTenantModels`.

### 5.12 Requirements and plans that change

| Document | Change |
|---|---|
| Requirements | CAT-11 added; USE-06, USE-07 and AZ-06 reworded as quoted in §3; the section on what changed from the first version names both |
| Epic 6, sprint 6.1 | cost and token metrics exist per installation only, with provider and model as dimensions; the opt-in tenant label applies to request, error and latency metrics, never to cost or tokens; the redacting log handler keeps the rule of §5.11 |
| Epic 6, sprint 6.2 | no platform aggregates per tenant: "platform aggregates" becomes operational indicators of the installation without tenant and cost; reports and exports are the tenant's |
| Security model | the tenant's usage, limit events and prices are read by the tenant only through the product; confidentiality against the administrator of the database, hosts and backups is out of scope (issue #33) |

### 5.13 The migration and the upgrade (D-19)

Migration 0012 runs in one transaction as the owner. It adds three columns with a constant
default to `tenant` (no rewrite), replaces two unique constraints on the rule tables (their
indexes are rebuilt; at most 5,000 rows per tenant), creates two tables, and adds one nullable
column and one `NOT VALID` check to `usage_record` (no partition rewritten or scanned). It
changes no existing row and lifts no row-level security. `ACCESS EXCLUSIVE` on `tenant`, the two
rule tables and `usage_record`, for the length of the transaction.

**Upgrade.** Stop, migrate, start (ADR 0026). **State compatibility with the 5.3 binary**: the
schema of 0012 holds nothing the 5.3 binary would misread — every tenant has the default
currency, so nothing it does not know is in force; its statements name no dropped column; its
inserts leave the new columns to their defaults; the new unique keys admit everything the old
ones did. It still does not serve: a binary turns not ready when the ledger of migrations is not
exactly its own (OPS-09). A new binary against the schema of 0011 does not start.

**What changes for an installation at the upgrade**, in `database.md` and the release notes:

1. A tenant's cost budgets and the platform's cost ceilings apply only in the tenant's active
   currency, which is `pricing.default_currency` (`USD`) for every tenant. An installation whose
   prices and budgets are in another currency sets the key before starting the new binary.
2. A catalogue price in another currency than a tenant's active currency no longer prices that
   tenant's requests; a budget in another currency no longer refuses with `no_price`: it is
   inactive.
3. The request log and the spans lose the fields of §5.11.

### 5.14 Configuration, CLI, documentation

**Configuration.**

| Key | Values | Default | Meaning |
|---|---|---|---|
| `pricing.default_currency` | three capital letters | `USD` | The active currency of the tenants that chose none. The same on every instance; takes effect at restart, at once (it is the operator's decision about every such tenant) |
| `telemetry.tenant_usage_details` | boolean | `false` | §5.11 |

`cache.catalog_ttl` is described as also bounding the time a change of a tenant's prices or
currency takes to reach the gateway.

**CLI.**

```
pandokos price-list lists    --tenant T
pandokos price-list list     --tenant T [--currency EUR] [--model PROVIDER/MODEL | --provider P] [--at TIME]
pandokos price-list get      --tenant T ID
pandokos price-list set      --tenant T --currency EUR --model PROVIDER/MODEL --rate input=2.5 --rate output=10
                             [--rate input@200000=5] [--valid-from TIME] [--note TEXT]
pandokos price-list set      --tenant T --currency EUR --model PROVIDER/MODEL --end [--valid-from TIME]
pandokos price-list delete   --tenant T ID
pandokos price-list currency get --tenant T
pandokos price-list currency set --tenant T EUR | --default
```

`--rate` has the syntax of `price override`; `--end` excludes `--rate`. `currency set` prints
the resource and, under it, the limits that become inactive. `limit list` shows a column
`active`; `tenant get` shows the active currency; `usage list -o json` has `price_source`.

**Documentation** (plan part E): `catalogue.md` gains the section **"Tenant price lists and the
active currency"**; `limits.md` section 5 is rewritten around the active currency (which budgets
apply, preparing a change, what the platform's ceilings measure and what a tenant can step out
of, the catalogue price in another currency); `usage.md` (`price_source`; who reads usage);
`gateway.md`, `container.md`, `telemetry.md` (the log line and the spans; the key);
`configuration.md`; `database.md` section 17 with the upgrade notes of §5.13; the reference
pages; the data model; the security model; the requirements; epic 6.

## 6. Isolation harness

- **`tenant_price_version`, `tenant_price_rate`** (tenant-scoped): the canonical policy and
  nothing else, grants in `Spec.Grants`, a fixture each (one statement that creates a scheduled
  version with one rate; the key of the rate table is `price_version_id`), so the matrix of
  T-143 covers them for every role, session (the platform scope is one of its four) and
  statement, directly and through the pooler. T-741 adds the positive controls, T-742 the rules
  of the triggers and the foreign keys.
- **`tenant`** (global, restricted): the grant of the admin role gains the three columns; T-743
  covers every role and session and every refusal of the guard, the platform scope first.
- **`limit_rule`, `platform_limit_rule`**: grants and policies unchanged; T-744 for the new keys.
- **`usage_record`**: the grant of the gateway gains `price_source`; T-745.
- **What the platform cannot read**: T-746 (the database) and T-747 (the API), §5.10.
- **No definer function is added.** `Spec.SecurityDefiner` is unchanged; T-746 pins the result
  types of the existing ones.
- **Routes**: seven operations, each with a tenant permission; T-748 is their cross-tenant test;
  T-270 to T-274 take them through `apitest.CallFor`.
- **The caches**: both entries are keyed by tenant and loaded in that tenant's session; T-731
  shows that one tenant's notification, currency and prices never reach another's entry.
- No statement on the two price tables names the tenant in a `WHERE` clause. The statements on
  the `tenant` row address it by `pandokos.current_tenant_id()` or, to lock and update it, by ID
  under its policies, as those of the time zone do.

## 7. Test infrastructure and CI

- `dbtest`: `SeedTenantPrice(tenant, currency, provider, model, validFrom, rates…)`,
  `SeedTenantPriceEnd`, and `SetTenantCurrency(tenant, currency, previous, effectiveFrom)`,
  which writes the three columns with the triggers off, as `SetTenantZone` does: the state a
  change and the passing of a month leave. `MigrateTo(tb, 11)` for the upgrade test.
- `limitstest.Real` gains the currency of each tenant and `SetCurrency(tenant, currency, at)`;
  rule sets built without it keep every cost limit (the zero `Active`).
- `apitest.CallFor` learns the seven operations.
- Gateway tests take the log lines from the handler's logger and the spans from the in-memory
  exporter the trace tests use; nothing sleeps across an instant.
- The end-to-end test cannot wait for a month: it sets the currency through the CLI, checks the
  pending answer, then moves the row with `SetTenantCurrency` and waits for the notification.
- CI: no new job.

## 8. Decisions

D-1 to D-8 and D-15 to D-17 implement the maintainer's review; the rest are choices of this
design.

| # | Decision | Reason | Recorded in |
|---|---|---|---|
| D-1 | **Two tenant-scoped tables with the canonical policy; a list is the versions of one currency. No policy, permission, route or session lets the platform read or write them** | The review; a table of lists would hold nothing but its key | ADR 0053 |
| D-2 | **A version is a `price` or an `end`, with the rules of ADR 0036, held by the database for every role** (guard, deferred shape checks, rates only in the transaction of their version) | Versions stay immutable and a list can stop pricing a model; the catalogue's tables let a rate be added to an existing version, the new ones do not | ADR 0053 |
| D-3 | **The active currency is three columns of `tenant`; `''` is `pricing.default_currency`. The price of a request is the tenant's list in that currency, else the catalogue's version when it is in that currency, else unknown** | The review; the shape of the time zone; "never mix" must hold for the catalogue too, or a cost in a third currency would be recorded for a tenant | ADR 0053 |
| D-4 | **`setActiveCurrency` needs `price_list:write`, `getActiveCurrency` `price_list:read`** | The currency selects the price list; who writes the active list already decides what budgets count; no new permission for one setting (§5.3) | ADR 0053 |
| D-5 | **A change needs a list in the currency, takes effect at `limits.NextChange` of the zone in force, keeps its instant when replaced, and only a session of the tenant can make it (a trigger)** | The review; one instant shared with the zone means one boundary per tenant and month; the platform not choosing is structural | ADR 0053 |
| D-6 | **Cost limits outside the active currency are left out by `RuleSet.For`; a rule is one per subject, scope, metric and currency** | The rule set is where "which limits apply" lives, so the store, the counters and the events need no change; coexistence is how a change is prepared | ADR 0049, 0050, 0052 amendments |
| D-7 | **`budgetCurrencies`: a tenant's rule may use any currency its lists or the catalogue price for the scope; a ceiling for one tenant, the catalogue's currencies and that tenant's active and pending currency; a default ceiling, the catalogue's** | The review; the platform cannot read lists, and an active currency has a list by construction | ADR 0050 amendment |
| D-8 | **One cost per request; a ceiling in the active currency counts the tenant's price. The platform reads a tenant's active currency on the `Tenant` resource** | The review; the platform must be able to tell which ceilings apply and to prepare one in the new currency | ADR 0053 |
| D-9 | **The tenant's currency and prices live in its entry of the catalogue cache; the rule set carries the currency too; both are notified by the triggers. No new cache or payload** | The entries, their lifetimes and their isolation exist; a disagreement between the two fails closed | ADR 0053, history of 0035 |
| D-10 | **`pricing.List` is the one definition of the price in force for a tenant; `Cost.Source` carries the list** | One place for the rule, used by the estimate, the record, the capability check and the tenant's model list | ADR 0053 |
| D-11 | **`usage_record.price_source`: nullable, `NOT VALID` check, NULL with a version reads as `catalogue`; no foreign key from the ledger** | The pattern of 5.3 for the partitioned ledger; the column has never had a key; started versions cannot be removed | ADR 0053, history of 0039 |
| D-12 | **A version names a model the tenant can see, with `RESTRICT`; deleting such a model answers 409 with one `errors` entry; a tenant may price a model the catalogue does not** | The answer of 5.3 D-8; a cascade would destroy versions that records name | ADR 0053 |
| D-13 | **At most 50,000 versions per tenant and 64 rates per version; the gateway loads the lists of the currency in force and the pending one, per model the version in force an hour ago and later ones** | Bounds memory and the load of an entry without limiting history or the number of lists | sprint decision |
| D-14 | **Seven operations and three tenant permissions (`price_list:read|write|delete`) in `tenant-admin`; three audit actions in the tenant's log** | The review; nothing of it reaches the platform's log | ADR 0053 |
| D-15 | **No schema change is needed for confidentiality towards the platform: the policies hold it. It is pinned by negative tests on platform sessions, on every platform operation and on the result types of the definer functions, and by a repository test over the handlers** | The review asked to verify and to test; what is true by structure should fail a build when it stops being true | ADR 0054 |
| D-16 | **`telemetry.tenant_usage_details`, off by default, gates cost, currency, token counts, reserved tokens and the amounts and currencies of a refusal in the request log, and cost on spans; a warning at start when on** | The review; one key for both channels, since both go to the operator | ADR 0054, history of 0015, 0040 |
| D-17 | **Requirements, epic 6 and the security model are changed with the code** | The review | requirements; ADR 0054 |
| D-18 | **`pricing.default_currency`, `USD` by default** | "The catalogue currency" must be one value for the rule of D-3; imports write USD; an installation priced in another currency needs to say so | ADR 0053 |
| D-19 | **Migration 0012 is additive in what it stores; the behaviour changes of the upgrade are listed** | No rewrite of the ledger; nothing a 5.3 binary would misread | sprint decision (`database.md`) |
| D-20 | **The capability check, `listTenantModels` and the rule resources show what applies in the active currency** | They are the tenant's troubleshooting, now that the operator's log says less | history of 0043 |
| D-21 | **The importer, the overrides, the counters and the error contract are untouched; no new dependency or error code** | ADR 0018, 0020 | sprint decision |

## 9. Tests

Levels as in 5.3: **U** unit; **D** test database; **I** in-process gateway with the fake
provider; **E** built binary; **R** repository test; **P** property test. IDs continue from
T-725 and T-R47.

### New tests

| ID | Test | Level | Proves | Requirement |
|---|---|---|---|---|
| T-726 | `pricing.TestListAt` | U | The rule of §5.2, each row: the tenant's price in the active currency; a later catalogue version does not displace it; the catalogue's version in the active currency when the list has none or an `end`; a catalogue version in another currency is no price; a list in another currency is ignored; a pending change switches at its instant; the zero `Active`; unsorted input. `Cost` sets `Source` exactly when it sets `VersionID` | **CAT-11**, CAT-08 |
| T-727 | `pricing.TestListProperty` | P | For random lists, currencies and instants, `At` equals a naive reference; it never returns a version that starts after the instant, an `end`, or a version in another currency than the one in force | **CAT-11** |
| T-728 | `limits.TestActiveCurrencyRules` | U | `For` leaves out cost limits outside the currency in force at the request's start, at every level and subject; a pending change switches at its instant; count limits never; the zero `Active` leaves nothing out; two budgets that differ by currency are two rules; a tenant's ceiling replaces the default of its currency only; at most one limit per counter | **LIM-03** |
| T-729 | `limits.TestLoadRulesCurrency` | D | The loader reads the currency: the default resolved, a currency, a pending change, one that took effect; both tables with two currencies | LIM-03 |
| T-730 | `limits.TestCurrencyChangeNeverFrees` | U, P | Random schedules of requests, budgets in two currencies and currency changes (with and without a pending zone change) against the reference of 5.2: the instant of a change is the end of the month in progress and of its last day; until it every admission is within the limit of the old currency's budget over its whole period; from it within the new one's; no budget stops applying inside one of its periods | **LIM-03** |
| T-731 | `catalog.TestTenantPricesCache` | U | The entry of a tenant carries its currency and prices; `catalog:<tenant>` drops that entry and no other; the lifetime; a failed load is not cached; `ResolveIn` returns the access it resolved with | **TEN-06**, CAT-11 |
| T-732 | `catalog.TestLoadTenantPrices` | D | As the gateway role: only the lists of the currency in force and of the pending one; per model the version in force and the scheduled ones; a version replaced more than an hour ago is not loaded; rates in category order; `End`; the default currency resolved; another tenant's never; a new version, a deletion and a currency change reach a running cache within 2 s | **CAT-11**, TEN-04 |
| T-733 | `resolver.TestCatalogueTenantPrices` | U | The target carries the currency, the catalogue's versions and the tenant's versions of the resolved model only | CAT-11 |
| T-734 | `gateway.TestEstimateTenantPrice` | U | The estimate is priced through the list: amount, currency, version ID, source; unknown when neither list prices the model in the active currency; the amount that does not fit keeps currency, version and source | **LIM-03**, CAT-11 |
| T-735 | `gateway.TestRecordPriceSource` | I | The record's cost, currency and `price_source` for: a tenant price, the catalogue price, a catalogue price in another currency (unknown, `no_price`), an `end`; a version that starts while a request runs does not change that request | **CAT-08**, CAT-11 |
| T-736 | `gateway.TestActiveCurrencyBudget` | I, D | **The exit condition**: tenant A has a EUR list and a EUR monthly budget next to its USD one. Before the switch it is priced in USD and only the USD budget counts. After its active currency is EUR, it is refused with 429 `quota_exceeded` (currency EUR) once reservations reach the EUR budget, and the exhausted or untouched USD budget plays no part; tenant B on the same model keeps the catalogue's USD price and its USD budget; records carry `tenant`/EUR and `catalogue`/USD; A's counter rebuilt from the ledger equals what was counted. A default USD ceiling does not apply to A; A's own EUR ceiling does and counts the tenant's price | **LIM-03**, **CAT-11** |
| T-737 | `gateway.TestInactiveBudgets` | I, D | A budget in another currency never refuses and records no event; a request only inactive budgets match is `unlimited` and writes the unlimited-traffic rows; a model priced in neither list of the active currency is 409 `no_price` under an active budget, admitted uncounted with `allow`, and recorded with an unknown cost without one; rule cache and catalogue cache disagreeing on the currency refuse | **LIM-03** |
| T-738 | `gateway.TestTenantPriceTakesEffect` | I, D | A version written through the administration API prices the next request of a running gateway within 2 s; a scheduled version and a pending currency apply from their instant with no notification (clock); an `end` gives the catalogue price back; with notifications cut, within the cache lifetimes | **CAT-11** |
| T-739 | `gateway.TestUsageDetailsOff` | I | By default the request line of an answered request, of a 429 and of a 409 `no_price` has none of the fields of §5.11 and the client span has neither cost attribute; with the key on they are all there, with `price_source`; at level `debug`, with the key off, no line of the gateway, the limiter or the ledger writer contains the canary token counts or amounts of the fixture | **USE-07** |
| T-740 | `ledger.TestWriterPriceSource` | D | The writer stores `tenant`, `catalogue` and NULL | — |
| T-741 | `isolation.TestTenantPricePolicies` | D | Both price tables, direct and pooled: the admin role writes and reads its tenant's rows only; the gateway reads its tenant's only and writes nothing; the worker holds nothing; the platform scope and a session without scope see no row and write none | **TEN-04**, **TEN-07** |
| T-742 | `isolation.TestTenantPriceRules` | D | For the admin role and for a superuser: a start in the past; update; deletion of a started version; deletion of a scheduled one takes its rates; truncate; the unique key per currency; the format of the currency; the rate checks. At commit: a price without rate, without a base rate, an end with a rate, a rate added to an existing version; a rate for another tenant's version. A model with a version cannot be deleted, also from the platform scope | **CAT-11**, **TEN-04** |
| T-743 | `isolation.TestActiveCurrencyPolicies` | D | The admin role in the tenant's session changes the currency columns of its own row; the platform scope, another tenant's session, a session without scope and a superuser without scope are refused; the gateway and the worker cannot update; every refusal of the guard (no pending instant, an instant in the past or beyond 32 days, a replacement that moves the instant, a wrong previous currency); the format; a tenant is created with the default; the two notifications | **TEN-04**, CAT-11 |
| T-744 | `isolation.TestRuleKeysByCurrency` | D | Both rule tables: two cost rules that differ only by currency coexist; the same currency twice is refused; two count rules of one subject, scope and metric are still refused | LIM-03 |
| T-745 | `isolation.TestUsagePriceSource` | D | The gateway inserts `catalogue`, `tenant` (with a version) and NULL; a source without a version and another value are refused, in an existing and in a newly created partition; the row is still append-only | TEN-04 |
| T-746 | `isolation.TestPlatformSeesNoTenantUsage` | D | With usage records, limit events and prices of two tenants present: each runtime role in the platform scope and without scope, direct and pooled, reads zero rows or is refused on `usage_record`, on each of its partitions by name, on `limit_event`, on both price tables; the `SECURITY DEFINER` functions of the schema are the eleven of 5.3 with the result types the test pins, and the body of none names one of these tables except `ensure_usage_partitions` (partitions, no row) | **USE-07**, **TEN-04** |
| T-747 | `isolation.TestPlatformAPISeesNoTenantUsage` | D | Tenant A has records, events and prices with canary amounts, token counts and rates. A `platform-admin` key calls every operation of platform scope, with A wherever a path names a tenant: no answer and no row of the platform's audit log contains a canary; every tenant-scoped operation on A answers 403 | **USE-07**, **AZ-06** |
| T-748 | `isolation.TestTenantPriceAPI` | D | A key of tenant B on the seven operations of A gets the 404 of a tenant that does not exist; a price ID of B in A's path is the 404 of an unknown ID | **TEN-07** |
| T-749 | `admin.TestTenantPriceLists` | D | Creation now and scheduled in two currencies, every field, `in_force`; `listPriceLists` (`versions`, `models_priced`, `active`); the list of versions with each filter, `at` and paging; reading a replaced version; deleting a scheduled one; 409 for a started one; 409 `conflict` for the same start; the rules of an `end`; each 400 of §5.5 (model elsewhere, nowhere, not granted and retired answer alike); the bound; two concurrent creations without a start both succeed, in order | **CAT-11** |
| T-750 | `admin.TestActiveCurrency` | D | Set, replace while pending (the instant stays), withdraw, set again after the instant; the instant is `limits.NextChange` in the zone in force, also with a zone change pending; a currency without a list is 400, with a tenant price or a catalogue price it is accepted, a scheduled price counts; the default is always accepted; `in_force` resolves the default; `inactive_limits` names the tenant's cost rules of every subject and the ceilings that apply, not those in the new currency, not count rules, not a default replaced by the tenant's ceiling; the audit row; the unchanged value writes nothing | **CAT-11**, **LIM-03**, AUD-01 |
| T-751 | `admin.TestTenantPriceAudit` | D | Each creation, deletion and currency change writes one row in the tenant's log with the members of §5.9, in the transaction of the change (a failure injected after the write leaves neither); nothing in the platform's log; the note in no audit row and no log line | **AUD-01**, **AUD-03** |
| T-752 | `admin.TestTenantPricePermissions` | D | Each of the seven operations needs its permission: refused to `inference`, to a key scoped without it and to a platform key; `tenant-admin` needs nothing else | TEN-07 |
| T-753 | `authz.TestPriceListPermissions` | U | The three permissions with tenant scope and their texts; `tenant-admin` holds them; `platform-admin` and `inference` hold none | CAT-11, AZ-06 |
| T-754 | `admin.TestBudgetCurrencyLists` | D | The table of §5.7 on create and update, for the scopes model, provider and all: EUR accepted for a tenant's rule once the tenant has a EUR price in force or scheduled for the scope, refused before with the sorted currencies; the catalogue's currencies still accepted; another tenant's check unchanged; a ceiling for a tenant accepted in its active and its pending currency and refused in a currency in which the tenant only has an unused list; a default ceiling as in 5.3 | **LIM-03** |
| T-755 | `admin.TestLimitRulesByCurrency` | D | Two budgets that differ by currency are created for one subject and scope, for a tenant and for the platform; the same currency twice is 409 `conflict`; `active` on each rule before and after a switch; `listTenantPlatformLimits` sets it; the audit details name the currency | LIM-03 |
| T-756 | `admin.TestCapabilityActiveCurrency` | D | `checks.budget`: `available` with an active budget and a price in the active currency; `not_enforced` when the only budget is inactive; `unpriced` with an active budget and a model priced in neither list; `exhausted` from the ledger in the active currency only; another tenant unaffected | **LIM-03** |
| T-757 | `admin.TestTenantModelsPrice` | D | `listTenantModels` shows `price`, `tenant_price` and `price_source` for: the catalogue in the active currency, the tenant's list, a catalogue price in another currency (empty source), neither, an `end`, a list in an inactive currency (not shown) | CAT-11 |
| T-758 | `admin.TestUsagePriceSource` | D | `listUsageRecords` shows the source; a row written before the column, with a version, reads `catalogue`; for records of both sources the cost computed again from the version the record names equals the recorded cost | **CAT-08** |
| T-759 | `admin.TestModelDeleteTenantPrices` | D | A model with a tenant version is not deleted: 409 `invalid_state` with the `errors` entry of §5.8, nothing audited, no tenant, currency or rate in the answer | CAT-11 |
| T-760 | `admin.TestCatalogueUntouched` | D | An import and an override for a model leave a tenant's versions and their precedence as they were; the import report and `price_override` do not mention tenant lists | CAT-11 |
| T-761 | `admin.TestTenantShowsActiveCurrency` | D | `getTenant` and `listTenants` show the three members; `createTenant` and `updateTenant` refuse a currency member (400) | CAT-11 |
| T-762 | `config.TestCurrencyAndUsageDetailKeys` | U | Both keys from the file and the environment; a default currency that is not three capital letters is refused; the warning is listed when usage details are on | USE-07 |
| T-763 | `cli.TestPriceListCommands` | U | The commands and flags of §5.14 call their operations; `--end` excludes `--rate`; `--default` and a currency are exclusive; `--model` splits at the first slash | CAT-11 |
| T-764 | `e2e.TestTenantPriceLists` | E | The binary and the CLI, the exit condition end to end (the currency moved as §7 says): A's EUR list, EUR budget, switch, refusal at the budget; B in USD; a platform key gets 403 on A's prices, usage records and limit events; `usage list` shows both sources; the default log has no cost, and has it with the key on, with the warning at start | **CAT-11**, **LIM-03**, **USE-07** |
| T-765 | `migrations.TestTenantPriceListsUpgrade` | D | A database at 0011 with tenants, rules, ceilings and usage rows: after 0012 no row changed, every tenant has the default currency, the rows read `catalogue` or empty, the inserts of the 5.3 statements (a usage record without `price_source`, a tenant, a rule) are accepted, the migration applied twice is refused by the ledger | CAT-11 |
| T-R48 | `repo.TestTenantPriceGuide` | R | `catalogue.md` has the section on tenant price lists and the active currency (lists per currency, precedence, `end`, the change rule, what imports and overrides do not touch); `limits.md` section 5 says which budgets apply, how a change is prepared and what a ceiling measures; the telemetry and gateway guides name the key and no longer list the removed fields as always present | CAT-11, USE-07 |
| T-R49 | `repo.TestTenantPriceReference` | R | `admin-api.md` lists the seven operations and the three audit actions; `permissions.md` the three permissions; `configuration.md` the two keys; `database.md` the section of migration 0012 with the upgrade notes; `usage.md` names `price_source`; the requirements have CAT-11 and the new wording of USE-06, USE-07 and AZ-06 (no "platform usage viewer"); the security model names issue #33; the epic file of epic 6 carries the two notes | CAT-11, USE-06, USE-07, AZ-06 |
| T-R50 | `repo.TestPriceAndUsageRecords` | R | ADR 0053 and 0054 exist and are in the index; ADR 0049, 0050 and 0052 each have an `Amendment (sprint 5.4)` section and a history line that names it | — |
| T-R51 | `repo.TestOnePriceDefinition` | R | `pricing.VersionAt` is called in `internal/pricing` only; in `internal/gateway` the fields `Target.Prices`, `Target.TenantPrices` and `Target.Currency` are read by `PriceList` only | CAT-11 |
| T-R52 | `repo.TestUsageStaysWithTheTenant` | R | The statements that read `usage_record`, `limit_event` or a price table of a tenant (found by their SQL) are called only inside `WithTenant`, never inside `Platform` or `inPlatform`, and in `internal/admin` only from handlers of tenant-scoped operations; the gateway's request line builds the fields of §5.11 only behind the key | **USE-07**, **AZ-06** |

### Changed tests of earlier sprints

| Test | Change |
|---|---|
| T-140, `TestDefaultSpecMatchesDesign` | two tables, the grants of `tenant` and `usage_record`, two fixtures |
| T-143 | covers the two new tables through their fixtures |
| migration tests (count, checksums) | migration 0012 |
| T-201, T-202 | three permissions, one role |
| T-219, T-232, T-270 to T-274, T-R14, T-R15 | seven operations (`apitest.CallFor`) |
| T-379, T-380, T-397, T-398 | `TenantAccess` with currency and prices; `ResolveIn` returns it |
| T-628, T-663 | the estimate through `Target.PriceList` |
| T-611, T-619, T-701, T-703 | the slot of a rule includes the currency; the loader reads it |
| T-664, T-665, T-668, T-710 and any other fixture with a budget in a currency that is not the default | the fixture sets the tenant's currency, or the budget is in USD (plan B1 lists them) |
| T-666, T-712 and every test that expects 409 `no_price` for "a price in another currency" | the case is now an inactive budget (no refusal); `no_price` is tested with a model without a price in the active currency; the log fields need the key |
| tests that read `cost`, `currency`, `usage` or `reserved_tokens` from the request line or a span (gateway, e2e, compose, container) | turn the key on, or stop reading them |
| T-675, T-676, T-716 | `active` in the resource; the conflict by currency; `budgetCurrencies` has a new signature |
| T-543, T-681, T-720 | the capability check with the currency |
| T-546, T-721 | `price_source` in the list |
| T-715 | a third constraint name in `mapCatalogError` |
| T-688 (canary sweep) | covers the new audit rows; runs with the key off and on |
| tests of tenants and of `listTenantModels` | the new members |
| T-R42, T-R45 | sections of `limits.md` that are rewritten keep their anchors |

### Requirement coverage

| Requirement | Tests |
|---|---|
| CAT-11 | T-726, T-727, T-731 to T-736, T-738, T-742, T-743, T-749, T-750, T-757, T-764 |
| CAT-08 | T-726, T-735, T-758 |
| LIM-03 | T-728 to T-730, T-734, T-736, T-737, T-750, T-754 to T-756, T-764 |
| USE-07, AZ-06 (and the wording of USE-06) | T-739, T-746, T-747, T-753, T-762, T-764, T-R49, T-R52 |
| AUD-01, AUD-03 | T-750, T-751 |
| TEN-04, TEN-06, TEN-07 | T-140, T-143, T-731, T-732, T-741 to T-748, T-752 |

## 10. Definition of done for this sprint

**Requirements and tests**

- [ ] Every test of §9 exists under the stated name and passes; the changed tests pass; `make
      check`, `make test-sdk-python`, `make test-sdk-ts`, `make test-container` and `make
      test-compose` pass.
- [ ] The tests-first record exists: the commit of part B precedes the implementation commits
      and the pull request quotes its failing output.
- [ ] The property tests (T-727, T-730) ran with at least three seeds besides the default; `go
      test -race -count=20 ./internal/pricing/ ./internal/catalog/ ./internal/limits/` passes;
      both are quoted.
- [ ] The coverage table is repeated in the pull request with links to the CI run.

**Exit condition of the epic's sprint**

- [ ] A tenant with a EUR price list and a EUR budget, after switching its active currency to
      EUR, is refused once its reservations reach the budget, while another tenant on the same
      models keeps the catalogue's USD price and its USD budget (T-736, T-764).
- [ ] A platform session reads none of the first tenant's prices, usage records or limit events
      (T-746, T-747, T-764).

**Isolation and permissions**

- [ ] T-140 reports nothing; `Spec.SecurityDefiner` is unchanged; T-741 to T-748 pass.
- [ ] Each of the seven routes declares a tenant permission and has a cross-tenant negative test.
- [ ] No permission of platform scope was added; the worker holds no privilege on a price table.

**Secrets, content and usage**

- [ ] T-688 passes at log level `debug` with the key off and on; `make secrets` passes.
- [ ] With the default configuration no log line and no span of a run of the end-to-end suite
      carries a cost, a currency or a token count (T-739, T-764).

**Upgrade**

- [ ] T-765 passes; `database.md` states "stop, migrate, start", what §5.13 says of the 5.3
      binary, and the three changes of behaviour.

**Documentation and decisions**

- [ ] The pages of §5.14; ADR 0053 and 0054 `proposed` (accepted by the maintainer's merge or
      review); ADR 0049, 0050 and 0052 amended; the history lines; the requirements; epic 6.
- [ ] `api/openapi/admin.v1.yaml` is current and `make generate-check` passes.
- [ ] The roadmap says what moved; `status:` of this file is `done`; the review records the
      answers to §12.

## 11. Consequences for later sprints

- **6.1** (metrics): cost and token metrics are per installation, with provider and model as
  dimensions and **never a tenant label**, opt-in or not; the tenant label, where an operator
  turns it on, is for requests, errors, latency and limit refusals. Cost metrics carry a
  currency dimension and are never summed across it. The redacting log handler and the log
  reference take §5.11 as given; `telemetry.tenant_usage_details` stays the one switch.
- **6.2** (reports, exports, audit reading): reports, rollups and usage export are **the
  tenant's** (USE-06); there is no platform aggregate per tenant and no platform usage viewer.
  A report shows costs as recorded, with currency and `price_source`, and never converts or adds
  currencies; a tenant that changed its currency has one series per currency, split at a month
  boundary. A rollup keys by currency (the counters already do) and by `price_source`. Reading
  the audit log: the tenant's log to the tenant, the platform's to the platform; neither holds
  costs. What the platform gets are operational indicators of the installation, without tenant
  and cost.
- **CAT-10** (`pandokos export` / `apply`, 6.2): a tenant's declarative export, made with a
  tenant credential, includes its lists (per currency and model the version in force and the
  scheduled ones) and its active currency. `apply` cannot rewrite history: it adds a version
  where the list in force differs, never deletes a started one, and a currency change it applies
  waits for the next month like any other. The platform's export of the catalogue contains no
  tenant list.
- **6.3** (tenant deletion and export): the tenant data export holds every version with its
  rates and the history of the active currency is in the audit log. Deletion order:
  `tenant_price_rate` goes with `tenant_price_version` (cascade), both before `tenant`. The
  guard refuses the deletion of a started version for every role, the owner included, as the
  append-only triggers of the ledger and the audit log do; 6.3 needs one mechanism for all of
  them. The deletion job must itself respect D-15: it removes a tenant's usage without the
  platform reading it.
- **6.4** (events): a tenant's change of active currency as an event the platform can receive
  (O-4); events about a tenant's limits go to the tenant's subscribers only.
- **Epic 8** (shared counters): counters are keyed by currency already; every instance derives
  the same active currency from the tenant row, and `pricing.default_currency` must be equal on
  all, like `limits.time_zone`.
- **4.3** (roles): a custom role may hold `price_list:read` alone; a "platform usage viewer"
  role is not to be built (AZ-06).
- **10** (hardening): issue #33 — confidentiality against the operator of the database — stays
  open there; this sprint narrows what the product itself exposes.
- **12** (operability): the release notes of v0.2 carry the three changes of §5.13.

## 12. Risks and open points

### What was verified while designing

- Migration 0012 as written in the plan was applied, after 0001 to 0011 with the repository's
  migrator, on a PostgreSQL 16.15 started for the purpose. **The Docker test database of
  `local/testdb` could not be reached from the design session** (connection refused on its port,
  no Docker daemon), in the first pass and in this one; there was no pooler. The schema lint
  (T-140) reports nothing with the spec of appendix A4; the cross-tenant matrix (T-143, direct
  mode) passes for the two new tables; the isolation tests of 5.3 for the tenant's time zone and
  for the integrity of rules still pass (direct mode); the migration tests pass.
- Exercised on that database through the statements of appendix A2, as the admin, gateway and
  worker roles: prices in two currencies for one model, an end, the guard, the deferred checks
  (23000), the currency format; `CurrencyHasList` and `ListBudgetCurrenciesForTenantScope`
  before and after (a scheduled price counts); `ListTenantPriceLists`; the currency guard —
  a tenant session without a pending instant (23514), the platform scope (42501), another
  tenant's session (no row), an instant in the past and beyond 32 days, a pending change, a
  replacement that moves the instant and one that keeps it, the withdrawal, a superuser without
  scope changing another column (allowed, as test helpers need) and the currency (42501); the
  gateway reading the currency and the versions of one list; two budgets and two default
  ceilings that differ by currency, the same currency twice (23505), two count ceilings (23505);
  the admin role in the platform scope reading zero rows of `usage_record`, `limit_event` and
  both price tables, the worker zero rows of `usage_record` and refused on `limit_event`; the
  platform scope refused the deletion of a model by `tenant_price_version_model_fkey`.
- Appendix A2 and the changed statements of `usage.sql` and `tenants.sql` compile with `sqlc`
  v1.31.1 against the twelve migrations. Adding columns to `tenant` changes the generated model
  of the table: statements that returned exactly its columns must return the new ones too, or
  their row types stop being the model and `internal/admin/tenants.go` does not build (appendix
  A2b; verified).
- Appendix A3 was run through `apigen`: the seven operations and their schemas are generated.
- In the code of 5.3: the request line carries `cost`, `currency`, the `usage` group,
  `reserved_tokens` and the limit fields (`handler.go`, `limits.go`); the client span carries
  `pandokos.cost.amount` and `pandokos.cost.currency` and no token count (`spans.go`); no other
  log call of the limiter, the resolver, the ledger writer or the usage and capability handlers
  logs an amount. `RuleSet` slots are keyed by subject, scope and metric (`rules.go`). The
  isolation matrix already runs a platform-scope session. `getCapability`, `listUsageRecords`
  and `listLimitEvents` are tenant-scoped operations. The eleven definer functions were
  read: none selects from the ledger, the events or a price table. `PANDOKOS_TEST_SEED` is the seed variable of the property
  tests.
- The highest test IDs are T-725 and T-R47; the highest migration is 0011; the highest ADR 0052.
- **Not verified**: anything through PgBouncer; the Go contracts of §5.2, §5.4 and §5.6 (on
  paper); T-727 and T-730; how many tests of earlier sprints read the removed log fields or use
  a budget in a currency other than USD (plan B1 gives the commands that list them).

### Risks

| # | Risk | Mitigation |
|---|---|---|
| R-1 | A tenant steps out of the platform's cost ceilings by changing its active currency | The change waits for the next month and is visible on the `Tenant` resource; the platform can write a ceiling in the new currency for that tenant before; token ceilings do not depend on currencies; documented; O-4 |
| R-2 | After the upgrade, budgets and catalogue overrides in a currency that is not the default stop applying or pricing | `pricing.default_currency`; the three changes are in `database.md` and the release notes; tenants in another currency switch (and wait a month, O-1) |
| R-3 | The two caches of a gateway disagree on the currency for up to their lifetime | Fails closed through `limits.on_unknown_price`; a change is known a month ahead; T-737 |
| R-4 | A new tenant that wants another currency waits up to a month | O-1 |
| R-5 | Tests and dashboards of earlier sprints read the removed log fields | Plan B1; the key brings them back; the guides are changed with the code |
| R-6 | The rule of which budgets apply sits in the rule set of the limiter, the code with the strictest guarantees | Two files; the store and the counters are untouched; T-728 and the property test T-730; the zero `Active` keeps every existing fixture meaning what it meant |
| R-7 | Operators lose the troubleshooting of `no_price` from the log | The tenant's tools carry it (capability check, `active`, `listTenantModels`); the key for a single-tenant installation |
| R-8 | A gateway prices with a replaced version for up to the cache lifetime | The rule of the catalogue; the record names the version used; T-738 |
| R-9 | A model can no longer be deleted once a tenant priced it, and the refusal tells the platform so | Retire it; existence only (O-5) |
| R-10 | `created_at = now()` as "written in the transaction of the version" | Microsecond resolution; the administration API inserts rates through one statement next to the version it created; no rate that exists can change |
| R-11 | Someone adds a platform route or a function that returns usage | T-746, T-747 and T-R52 fail |

### Open points (raised to the maintainer; none blocks the sprint)

| # | Point | Recommendation |
|---|---|---|
| O-1 | By the review's rule a change of currency always waits for the next month, also for a tenant created today that has no budget and no usage. Alternative: at once when no cost rule or ceiling in the currency in force applies to the tenant (nothing can be freed); one branch in the handler and the trigger | Keep the single rule in this sprint; take the alternative if onboarding in another currency proves painful |
| O-2 | "The catalogue currency" is a configuration key, `pricing.default_currency` (`USD`). Alternative: the constant `USD` | Keep the key: installations priced by overrides in another currency exist |
| O-3 | A catalogue price in another currency than the tenant's active currency no longer prices its requests (before: it was recorded in its own currency) | Keep: it is what "never mix currencies" means; listed among the upgrade changes |
| O-4 | The platform is not told that a tenant changes its active currency; it sees it on the `Tenant` resource | Keep; an event in 6.4 |
| O-5 | The platform reads a tenant's active currency, and a refused model deletion tells it that some tenant priced the model | Keep: neither is a price or usage, and the first is needed for ceilings |
| O-6 | The kind `end` is kept from the first design | Keep: without it a list can never stop pricing a model |
| O-7 | `setActiveCurrency` is under `price_list:write` rather than `limit:write` | Keep (D-4); 4.3 can split it |
| O-8 | Besides cost, currency and tokens, the key also gates `reserved_tokens` and the amount and currencies of a refusal; byte counts, `cost_status` and `usage_source` stay | Keep: the first are usage or budget figures, the second operational |
| O-9 | A ceiling for one tenant can be written in that tenant's active or pending currency without any catalogue price in it | Keep: otherwise the platform could not follow a tenant into its currency |
| O-10 | ADR 0053 and 0054 are added as `proposed` | Maintainer's call |

### Maintainer decisions on the open points (2026-11-18)

| Point | Decision |
|---|---|
| O-1 | Accepted as recommended |
| O-2 | Accepted as recommended |
| O-3 | Accepted as recommended |
| O-4 | Accepted as recommended |
| O-5 | Accepted as recommended |
| O-6 | Accepted as recommended |
| O-7 | Accepted as recommended |
| O-8 | Accepted as recommended |
| O-9 | Accepted as recommended |
| O-10 | Accepted as recommended |
