---
id: SPRINT-5.2
title: Quotas and budgets
epic: EPIC-5
release: v0.2
status: done
design: draft 2026-11-18
---

# Sprint 5.2 — Quotas and budgets

- Epic: [5 — Limits and budgets](../epic.md)
- Implementation plan: [plan.md](plan.md)
- Previous sprint: [5.1 — Reserve and settle](../5.1-reserve-and-settle/sprint.md)
- Risk class: **high** (money: a budget that must hold under concurrency, across a period
  boundary and across a restart; counters that must agree with the ledger without counting a
  request twice or losing one; a new administration surface with two permission scopes)
- Decisions implemented: ADR [0012](../../../../docs/adr/0012-limits-and-budget-reservation.md)
  (period counters, rebuild from the ledger, reconciliation, the failing store, unknown prices),
  [0048](../../../../docs/adr/0048-reserve-and-settle.md) and
  [0049](../../../../docs/adr/0049-limit-rules.md) (the mechanism and the rules of 5.1, extended),
  [0013](../../../../docs/adr/0013-ledger-and-audit.md) and
  [0032](../../../../docs/adr/0032-audit-log-first-version.md) (audit of limit changes),
  [0014](../../../../docs/adr/0014-events-outbox.md) (the fact is recorded now; the outbox and
  delivery are 6.4), [0017](../../../../docs/adr/0017-admin-api-spec-first.md) and
  [0028](../../../../docs/adr/0028-admin-api-generator.md) (twelve operations, specification
  first), [0019](../../../../docs/adr/0019-configuration.md) (four keys),
  [0020](../../../../docs/adr/0020-error-contract.md) (`quota_exceeded` put into use; one new
  code), [0026](../../../../docs/adr/0026-schema-migrations.md),
  [0027](../../../../docs/adr/0027-database-roles-and-session-context.md) and
  [0029](../../../../docs/adr/0029-static-sql-sqlc.md) (migration 0010, queries),
  [0031](../../../../docs/adr/0031-authorization-core-and-provisioning.md) (six permissions),
  [0036](../../../../docs/adr/0036-prices-costs-and-importer.md) (costs keep their currency),
  [0039](../../../../docs/adr/0039-usage-record-admission-and-ledger-writer.md) (the ledger is what
  counters are rebuilt from), [0043](../../../../docs/adr/0043-tenant-capability-check-and-usage-list.md)
  (`checks.budget`)
- New decision records written by this sprint: **0050** (quotas and budgets: periods, attribution,
  currencies, unknown prices, the bound), **0051** (period counters and the ledger: load, cut,
  reconciliation, the failing store), **0052** (administration of limits: scopes, thresholds and
  their events, API, permissions, audit); history lines on 0012, 0014, 0020, 0032, 0035, 0043,
  0048, 0049. Text in [plan.md](plan.md), part E.

This document is the design. The implementer follows [plan.md](plan.md); a gap in either document
is raised as a question, not filled silently ([planning/README.md](../../../README.md)).

Items carried into this sprint, and where each is decided:

| Item | From | Decided in |
|---|---|---|
| One administration API for every kind of limit, the rate rules of 5.1 included; permissions; audit; existence checks of the subject; the 5,000 rule count enforced on write | 5.1 D-13, O-1, §11 | §5.11, §5.12, D-15 to D-17 |
| Metrics per day and month; scope columns for provider and model; `RuleSet.For` gains the target | 5.1 §11 | §5.2, §5.3, D-1, D-9 |
| A second counter kind behind the same `Store`; `Amounts.Cost` gets its metric | 5.1 §11 | §5.4, D-3 |
| Counters rebuilt from `usage_record`; reconciliation by the worker role | 3.2 §11, 5.1 §11, ADR 0012 | §5.6, D-6 to D-8 |
| Handling of several currencies | 2.2 O-6, 3.2 §11, epic 2 record | §5.5, D-4 |
| Behaviour with unknown prices; behaviour when the counter store fails | ADR 0012, 5.1 §2 | §5.5, §5.7, D-5, D-10 |
| The code `quota_exceeded` and its headers | 5.1 D-11, ADR 0020 | §5.8, D-11 |
| `checks.budget` of the capability check: `available` or `exhausted`, reason `budget_exhausted` | 3.3 §11, 5.1 D-14 | §5.13, D-18 |
| A default output reservation per model | 5.1 O-5 | §5.10, D-14 |
| Python and TypeScript SDK tests of a Pandokos 429 | 5.1 §2 | §7, T-689, T-690 |
| Soft thresholds record an event; delivery is 6.4 | epic 5, ADR 0014 | §5.9, D-12, D-13 |

## 1. Outcome

A tenant can be given a quota or a budget, and it holds:

1. Six new metrics count requests, tokens and estimated cost **per day and per month**, in one
   configured time zone. They are reserved in step 7b with the three metrics of 5.1, in the same
   atomic call, and settled with the figures of the usage record.
2. A **budget** is an amount in one currency. A request is refused with 429 `quota_exceeded` once
   what is reserved and spent in the period, plus its own estimate, would exceed the amount; the
   first request that starts in the next period is admitted. What happens when the price of a
   request is unknown is configured, and refusal is the default.
3. A rule can be narrowed to a **provider or a model**, and can carry **thresholds**: reaching one
   records an event, once per rule and period; a rule that is not enforced only records.
4. Counters are **loaded from the ledger** before they are first used and brought back to it by a
   periodic reconciliation: after a restart they equal the ledger, and twelve and a half minutes
   (with the defaults) after a request started, whatever a counter held for it has been replaced
   by what the ledger holds.
5. Every rule, the rate rules of 5.1 included, is written through the **administration API and the
   CLI**, with permissions per scope, cross-tenant negative tests and an audit record per change.
6. The capability check answers whether a quota or budget is exhausted; a model can carry its own
   default output reservation; `docs/operations/limits.md` states what is strict, what is bounded
   and what is best effort.

Counters still live in the memory of one instance (epic 8 shares them).

## 2. Scope

### In scope

- `internal/limits`: periods and the calendar, period counters in `Memory`, rules with scope,
  currency, enforcement and thresholds, the ledger reader, the reconciler, the event writer.
- Migration 0010: columns and constraints of both rule tables, the table `limit_event`
  (tenant-scoped, append-only), the column `model.default_output_tokens`; queries; the isolation
  harness.
- The pipeline: the estimate carries provider, model and start; `quota_exceeded` and `no_price`;
  a request counts once a credential was opened for it (D-2).
- Administration API (twelve operations), six permissions, six audit actions, CLI commands.
- `checks.budget`; the per-model default; four configuration keys.
- Property, race and load tests; end-to-end tests with the built binary, a restart included;
  Python, TypeScript and Go SDK tests.
- `docs/operations/limits.md` and the changed pages; ADR 0050 to 0052.

### Not in scope

| Item | Where |
|---|---|
| Delivery of threshold and exhaustion events: outbox, relay, webhook, replay (LIM-13) | 6.4; this sprint records the fact in `limit_event` |
| Limits consistent across instances; the Valkey store (LIM-10) | epic 8 |
| Metrics of limit rejections, reservations, reconciliation runs (OBS-06) | 6.1 |
| Usage reports and rollups; a "used against limit" report | 6.2 (the capability check answers exhausted or not) |
| A time zone per tenant or per rule | not scheduled (O-2) |
| Currency conversion | not planned (D-4) |
| Removal of a tenant's rules and events with the tenant | 6.3 |
| Custom roles holding the new permissions | 4.3 |
| Allowance headers on admitted responses | not scheduled (5.1 O-7) |
| Periods other than day and month (week, year, rolling) | not scheduled |
| The capability check for a specific service account or key | not scheduled (3.3 O-3) |

## 3. Requirements claimed

The meaning is quoted from [docs/requirements](../../../../docs/requirements/README.md).

| ID | Meaning | Delivered here | Extent |
|---|---|---|---|
| LIM-02 | "Cumulative quotas of requests and tokens per day or month, in a configurable time zone (UTC by default)" | Metrics `requests_per_day`, `requests_per_month`, `tokens_per_day`, `tokens_per_month`; `limits.time_zone` (§5.2) | full, single instance; one zone per installation |
| LIM-03 | "Monetary budgets per period" | Metrics `cost_per_day`, `cost_per_month` with a currency (§5.5) | full, single instance |
| LIM-04 | "Limits apply at platform, tenant and virtual-key level, optionally per provider or model. A lower level can only restrict" | Scope columns on both rule tables, for all nine metrics (§5.3); rules written through the API | completes 5.1 |
| LIM-05 | "Hard limits block. Soft thresholds raise an event" | `enforce` and `thresholds` of a rule; `limit_event` (§5.9) | the event is recorded; delivery is LIM-13 |
| LIM-06 | "Limits are checked before forwarding and settled with the actual usage afterwards" | The six metrics in step 7b and `finish` | extends 5.1 |
| LIM-07 | "Concurrent requests cannot overshoot a budget without bound: capacity is reserved from an estimate before forwarding and settled afterwards. The maximum overshoot is documented" | The estimated cost is reserved; the bound of §5.5 | money, completing 5.1 |
| LIM-08 | "The treatment of failed, cancelled, refused and usage-less requests is defined and documented" | The table of 5.1 §5.7 with the change of D-2 and the cost column (§5.4) | extends 5.1 |
| LIM-09 | "A limit rejection returns HTTP 429 with a stable code, `Retry-After` and remaining-allowance headers, and is distinguishable from a provider's own 429" | `quota_exceeded` (§5.8) | completes 5.1 |
| LIM-11 | "Behaviour when the counter store is unavailable is configurable; the default is to refuse" | `limits.on_store_failure` (§5.7) | full for the in-process store; the Valkey store reuses it |
| LIM-12 | "The documentation states which limits are strict and which are best effort, and how reservations, resets and missing prices are handled" | `docs/operations/limits.md` (§5.15), checked by T-R42 | full |
| IAM-04 | "A virtual key always belongs to exactly one tenant and can be restricted by scope, model allow-list, expiry and limits" | Rules of subject `key` are written through the API | the writing part 5.1 left open |
| AUD-01 | "Pandokos audits authentication failures and every creation, change, disabling, revocation and deletion of tenants, identities, keys, roles, catalogue entries, credentials and limits" | Six audit actions (§5.12) | the limits part |
| AUD-03 | "The audit record is written atomically with the change it describes" | Same transaction (T-677) | for the new operations |
| API-07 | "An integrator can ask whether a tenant is currently able to call a given provider and model (credential present and valid, model allowed, tenant active, budget available)" | `checks.budget` (§5.13) | the budget part |
| TEN-04, TEN-06, TEN-07 | Row-level security on tenant data; isolation "uniformly for … limit counters … and background jobs"; negative tests for every tenant-scoped table and route | `limit_event` has the policy and a fixture; the reconciler reads each tenant in its own session; every new route has a cross-tenant test (T-661, T-659, T-679) | full for what this sprint adds |
| SEC-04 | "Defaults are secure. At start-up Pandokos validates its configuration and refuses or warns about weak settings" | Both failure settings default to refusal; `allow` warns (§5.14) | for the four keys |

Not claimed: LIM-10, LIM-13, OBS-06, USE-06.

## 4. Repository layout

New:

```
internal/limits/period.go          Period, Calendar
internal/limits/periods.go         period counters of Memory: reserve, settle, load, fold, rebase
internal/limits/usage.go           UsageGroup, Matches, Sum; the ledger reader
internal/limits/reconcile.go       Reconciler: Ensure, Run
internal/limits/events.go          Event, EventWriter
internal/admin/limits.go           the seven tenant operations
internal/admin/platform_limits.go  the five platform operations
internal/cli/limits.go             the CLI commands
internal/store/migrations/sql/0010_quotas_budgets.sql
test/sdk/python/test_limits.py, test/sdk/ts/limits.test.mjs
docs/adr/0050-…, 0051-…, 0052-…
```

Changed: `internal/limits/{limits,memory,rules,load,cache}.go`, `internal/gateway/{ports,limits,
estimate,handler,record}.go`, `internal/resolver/{limits,catalogue}.go`, `internal/family/*`
(two codes, two headers), `internal/ledger/writer.go` (one method), `internal/catalog` (one model
value), `internal/admin/{capability,models,admin}.go`, `internal/authz/authz.go`,
`internal/cli/catalogue.go`, `internal/config/*`, `internal/server/server.go`,
`cmd/pandokos/{main,serve}.go`, `api/openapi/admin.v1.yaml`, `internal/store/queries/sql/
{limits,catalog}.sql`, `test/isolation/spec.go`, `test/dbtest`, `test/limitstest`, `test/sdk`,
generated code, `migrations.sum`, documentation.

No new Go module. `cmd/pandokos` imports `time/tzdata` (D-1).

## 5. Architecture

### 5.1 The pipeline, as changed

Steps are those of 5.1 §5.1. Three things change:

| Where | Change |
|---|---|
| Step 7b | The estimate carries the provider, the model and the start time of the request. The resolver adds the period limits, makes sure their counters are loaded (§5.6), applies the rule for unknown prices (§5.5) and reserves everything in the one call. Refusals: 429 `rate_limited` (5.1), 429 `quota_exceeded` (§5.8), 409 `no_price` (§5.5), 503 `unavailable` (rules or counters cannot be verified and the setting says refuse, §5.7) |
| Step 8 | `counted` is set together with `opened`, when the credential was opened (D-2) |
| finish | **settle** when `counted`, **release** otherwise. The flag `forwarded` of 5.1 is replaced by `counted` |

**D-2 in one sentence.** A request counts, as a request and with the tokens and cost of its
record, exactly when its usage record carries a credential ID. That is the one predicate the
ledger can evaluate (§5.6), and the only case in which it differs from 5.1 is a caller that goes
away, or a panic, between the opening of the credential and the call to the proxy: such a request
is now settled (one request, no tokens) instead of released.

### 5.2 Periods and the calendar (D-1)

`limits.time_zone` names one IANA zone for the installation; `UTC` is the default. The binary
embeds the zone database, so the answer does not depend on the container image.

```go
type PeriodKind int // PerDay, PerMonth

// Period is [Start, End) in absolute time.
type Period struct{ Start, End time.Time }

// Calendar computes periods in one zone. It is safe for concurrent use and caches the periods
// of the last instant it was asked about.
type Calendar struct{ /* loc, cache */ }
func NewCalendar(loc *time.Location) (*Calendar, error) // error: an offset in use is not a whole number of minutes
func (c *Calendar) Of(k PeriodKind, t time.Time) Period
func (c *Calendar) Zone() string
```

**Definition.** The *label* of an instant is its civil date in the zone (for a month: year and
month). `S(L)` is the earliest instant whose label is `L` or later. The period of an instant `t`
with label `L` is `[S(L), S(L'))`, where `L'` is the next date (or month). Consequences:

- Every instant belongs to exactly one day and one month, and periods of one kind are contiguous.
- A day is as long as the zone makes it: 23 or 25 hours when the clocks change, and the period
  starts at the first instant of the civil date even when local midnight does not exist (a zone
  that moves the clock from 00:00 to 01:00) or exists twice.
- A month starts where its first day starts; a day lies inside one month.
- Period boundaries are whole minutes of absolute time (checked by `NewCalendar`), which the
  minute buckets of §5.4 rely on.

**Which period a request belongs to.** The period that contains the **start time of the request**
(`started_at` of its usage record, step 1, truncated to the microsecond as the ledger stores it),
not the time of admission. A request that starts before midnight and is admitted, or ends, after
it is reserved against, and settled into, the period it started in. This is what makes a counter
and a ledger aggregate over `[Start, End)` the same sum. The rate windows of 5.1 keep the
admission second.

**Changing the zone** takes effect at restart: counters are loaded from the ledger with the new
boundaries. Events of the period in which the zone changed can be recorded once more (their key
contains the period start).

### 5.3 Rules: metrics, scope, currency, enforcement, thresholds (D-9)

Both rule tables gain the same columns (migration 0010, plan appendix A1):

| Column | Meaning |
|---|---|
| `metric` | the three of 5.1 and `requests_per_day`, `requests_per_month`, `tokens_per_day`, `tokens_per_month`, `cost_per_day`, `cost_per_month` |
| `provider`, `model` | the scope: names of the catalogue; empty means every provider, every model of the provider. A model needs its provider. No foreign key: a rule whose scope names nothing never matches |
| `currency` | three capital letters for a cost metric, empty otherwise |
| `limit_value` | requests or tokens (1 to 10¹²); for a cost metric, **millionths of the currency** (1 to 10¹⁵, that is up to 10⁹ units) |
| `enforce` | `true`: the limit refuses. `false`: it never refuses and only records its thresholds. Always `true` for the three metrics of 5.1 |
| `thresholds` | up to four percentages (1 to 100) of `limit_value`; empty for the three metrics of 5.1; at least one when `enforce` is false |

Unique per (tenant, subject, provider, model, metric): one rule per metric and scope, hence one
currency per budget. Only `limit_value`, `enforce` and `thresholds` change after creation.

**Resolution.** `RuleSet.For` takes what the request is:

```go
type Query struct {
    ServiceAccountID, KeyID string
    Provider, Model         string    // names of the target
    Start                   time.Time // start of the request
}
func (s RuleSet) For(q Query, cal *Calendar) []Limit
```

It returns one limit per (subject, scope, metric) that has a rule and matches, in a fixed order:
subject `tenant`, `service_account`, `key`; within a subject the scope *all*, then *provider*,
then *model*; within a scope the nine metrics in the order `concurrent_requests`,
`requests_per_minute`, `tokens_per_minute`, `requests_per_day`, `requests_per_month`,
`tokens_per_day`, `tokens_per_month`, `cost_per_day`, `cost_per_month`. A rule scoped to a
provider matches requests to that provider; a rule scoped to a model matches that model of that
provider. Every limit returned has its own counter and must admit, so a narrower rule can only
restrict (LIM-04), as in 5.1.

For the subject `tenant` the platform ceiling (the row of that tenant, else the default row, per
scope and metric) and the tenant's own rule share one counter when they have the same currency:
`Max` is the smaller of the **enforced** values, and the thresholds of both rules are kept. With
different currencies they are two limits with two counters.

```go
type Limit struct {
    Tenant          tenant.ID
    Subject         SubjectKind
    ID              string
    Provider, Model string // scope; "" means every
    Metric          Metric
    Currency        string // cost metrics
    Max             int64  // 0: not enforced. Cost metrics: millionths of the currency
    Level, RuleID   string // the rule that set Max; of the first rule when none is enforced
    Period          Period // zero for the three metrics of 5.1
    Marks           []Mark // thresholds, ascending by At
}

// Mark is one threshold of one rule, as an absolute value in the unit of Max.
type Mark struct {
    At      int64 // ceil(value × percent / 100)
    Percent int
    RuleID  string
    Level   string
    Value   int64 // limit_value of the rule
}
```

A counter is identified by (tenant, subject kind, subject ID, provider, model, metric, currency)
and, for a period metric, the period start. A request has at most 81 limits (three subjects,
three scopes, nine metrics), a few more when currencies differ between levels.

### 5.4 Period counters of the in-process store (D-3)

`Store` keeps its three methods. `Request` gains `Start time.Time`; `Amounts.Cost` is now read.

A period counter holds, in the unit of its metric (an integer for requests and tokens, an exact
decimal for cost):

```
base      what the ledger holds for the requests of the period that started before the tenant's cut (§5.6)
live[m]   per minute m of request start, m ≥ cut: what this instance reserved and settled for the
          requests that started in that minute, and what was read from the ledger for it at load
reserved  the part of live that belongs to reservations still outstanding
counted   = base + Σ live          what a reservation is tested against
used      = counted − reserved     what a threshold is tested against
```

| Call | Effect on a period counter, under the lock of the tenant's shard |
|---|---|
| **Reserve** | amount `a`: 1, the estimated tokens, or the estimated cost. If `Max > 0` and `counted + a > Max`: refusal, nothing changes anywhere. Otherwise `live[minute(Start)] += a`, `reserved += a` |
| **Settle** | `live[minute(Start)] += actual − a`, `reserved −= a`. `actual` is 1 for requests; the record's tokens; the record's cost when it is known and in the counter's currency, else 0. Then the marks are tested (§5.9) |
| **Release**, expiry | `live[minute(Start)] −= a`, `reserved −= a` |

- A counter whose period starts at or after the tenant's cut is created empty by `Reserve`. One
  whose period started before the cut must have been loaded (§5.6); if it is not there, `Reserve`
  returns `ErrNotLoaded` and reserves nothing.
- A refusal by a period limit: `Remaining = max(0, Max − counted)` (for cost: rounded down to a
  millionth), `RetryAfter = Reset =` the time to `Period.End`, rounded up to a second, at least
  1 s; `OverLimit` when `a > Max`. The refusal names the first limit without room in the order of
  §5.3 and carries the longest `RetryAfter`, as in 5.1. The first refusal of a counter in a period
  records `exhausted` (§5.9).
- A limit with `Max = 0` is counted and never refuses.
- A request whose `Start` is before the tenant's cut is an error (it would be counted in a minute
  that was already replaced by the ledger): nothing is reserved.
- `Sweep` additionally drops a period counter once its period ended more than one horizon ago,
  and one that nothing touched for an hour and that has nothing reserved (it is loaded again on
  next use); a tenant without period counters loses its cut.
- Reserve is all or nothing over rate, concurrency and period limits together; the lock and the
  shards are those of 5.1. Nothing in `Reserve`, `Settle` or `Release` performs I/O.

How a request ends (5.1 §5.7, with D-2 and the cost): a settled request counts 1 request, its
record's tokens and its record's known cost; a released one counts nothing; a request sent and
failed upstream counts 1 request, no tokens, no cost. `rejected_limit` records count nothing.

### 5.5 Budgets: currency, unknown prices, the bound (D-4, D-5)

**Currency.** A budget counts costs in its own currency and nothing else. Costs keep the currency
of the price version that produced them (ADR 0036) and are never converted. For a given budget a
request whose price is in another currency is in the same position as a request without a price:
the budget cannot measure it.

**Unknown price at reservation.** The estimate of 5.1 §5.3 has no known cost when the model has no
price version in force, when the version cannot price the estimate, or — for a given budget — when
its currency is not the budget's. If at least one cost limit applies to the request and cannot
measure it, `limits.on_unknown_price` decides:

| Value | Behaviour |
|---|---|
| `refuse` (default) | 409 `no_price`: `Pandokos: a budget applies to this call and the model has no price the budget can be measured with.` Usage record with outcome `rejected_limit`, `error_code` `no_price`. No `Retry-After`: waiting does not help |
| `allow` | The request is admitted and reserved against every other limit; the budgets that cannot measure it do not count it. Log field `budget_unpriced: true` |

**Unknown price at settlement.** A record whose cost is unknown (a category without a rate
appeared in the provider's figures) settles its cost at 0: a budget counts known costs only, as
the ledger sum does.

**The bound.** For a period counter with limit *L*, and with *I* the period:

`Σ actual(r), r started in I and counted  ≤  L + Σ max(0, actual(r) − reserved(r)), r in flight at the last admission of I`

The proof is that of 5.1 §5.6 (3) with the period in place of the window. Corollaries: strict
when no request exceeds its reservation; at most *k* × *e* over, with *k* the requests in flight
(bounded by a concurrency limit when one is set) and *e* the most one request can exceed its
reservation; once over *L* nothing is admitted until the period ends. For tokens *e* is that of
5.1 §5.6 (4). For cost *e* is larger than the token bound times a rate, because the estimate
prices plain input and output at the tier of the estimated input, while the record can hold
dearer categories (cache writes, audio) and a higher tier: *e* is at most the cost of a full
context window and a full output at the highest rates of the price version, minus the reserved
cost. `limits.md` says so and advises a concurrency limit next to a tight budget.

This bound holds for what the counter sees. Section 5.6 says when a counter sees less than was
spent.

### 5.6 Counters and the ledger: load, cut, fold, rebase (D-6 to D-8)

**What the ledger gives.** One query (plan appendix A2, `AggregateUsageForLimits`), run in the
session of the tenant, returns for a range of `started_at` the number of records with a
credential ID, the sum of `COALESCE(input_tokens, estimated_input_tokens, 0) +
COALESCE(output_tokens, estimated_output_tokens, 0)` and the sum of `cost`, grouped by service
account, key, provider, model and currency — and by start minute for the part of the range at or
after a given instant. These are the figures a request was settled with (5.1 D-2, and D-2 here).
One Go function, `Sum(Limit, []UsageGroup)`, says which groups a counter counts; the store, the
reconciler and the capability check all use it.

```go
type UsageGroup struct {
    Minute                                       int64 // 0: before the live part
    PrincipalID, KeyID, Provider, Model, Currency string
    Requests, Tokens                              int64
    Cost                                          pricing.Decimal
}
type Ledger interface {
    // Usage aggregates the requests that started in [from, to); those that started at or after
    // liveFrom are grouped by minute.
    Usage(ctx context.Context, t tenant.ID, from, liveFrom, to time.Time) ([]UsageGroup, error)
}
```

The reader runs on the **worker** role, the only runtime role besides the admin role that may
read `usage_record`; the gateway role still reads nothing of it.

**Horizon.** `H = limits.request_body_read_timeout + limits.max_request_duration + 2 m` (12 m 30 s
with the defaults): longer than any request lives from its start to the hand-over of its record.
The reservation lifetime of 5.1 (`max_request_duration + 1 m` from admission) ends before it.

**Cut.** Each tenant with period counters has a cut `C`, a whole minute:

`C = floor_minute( min( now − H, start time of the oldest usage record handed to the writer and not yet committed ) )`

and never moves back. Every request that started before `C` has ended and its record is committed,
rejected by the database or lost; nothing this instance still holds can change the ledger before
`C`. `ledger.Writer` gains `OldestPendingStart() (time.Time, bool)` for the second term: while the
database is down, the cut does not advance.

**Invariant.** For every period counter: `base` equals the ledger aggregate over `[Period.Start,
C)`, and `live` covers `[C, now]`. Three operations maintain it; each computes outside the lock
and applies under it, and a mutex per tenant serializes them:

| Operation | When | What |
|---|---|---|
| **Load** | Step 7b needs a period counter that does not exist and whose period started before the cut, or the tenant has no cut yet. Single flight per tenant; requests of that tenant wait for it, bounded by 5 s | The tenant gets its cut if it has none. One ledger query per distinct period among the missing counters: `[Period.Start, now]` with the live part from `C`. The counter is created with `base` and with `live` seeded per minute from the ledger. Existing counters are not touched |
| **Fold** | Every `limits.reconcile_interval`, for each tenant with a cut | `C' =` the new cut. One query per piece of `[C, C')` cut at day starts. Under the lock: every counter whose period contains a piece adds the piece to `base`; `live` minutes before `C'` are dropped; `C := C'` |
| **Rebase** | After the fold, when an hour has passed since the tenant's last one | One query per distinct period: `base :=` the aggregate over `[Period.Start, C)` |

After each of them the marks of the counters it changed are tested (§5.9).

**Why nothing is counted twice and nothing is lost.**

- A request this instance reserved lives in `live[minute(Start)]` only. When the cut passes that
  minute, the minute is dropped and the ledger's figure for it is added, in the same critical
  section: the request moves from memory to ledger exactly once.
- A request can still change `live` only while it is outstanding, and an outstanding reservation
  started less than `H` ago, after any cut: a fold never drops a minute that a settle will still
  write. (If the invariant is ever broken, the settle finds no reservation — it expired — and
  changes nothing; the ledger row is picked up by the next rebase.)
- A seed and a later settle cannot overlap: a counter is seeded when it is created, and only
  requests reserved after its creation write to it.
- Ledger rows that this instance never held — written by the process that ran before, by a
  process still draining during a restart, or for a request admitted before the rule existed —
  are in `base` or in the seed when they were committed at load time, and otherwise enter with
  the fold that passes their minute.
- A record the database rejected, or one lost at shutdown or in a crash, is in no aggregate: the
  counter follows the ledger, by definition (ADR 0012: PostgreSQL is the source of truth).

**Stated guarantees** (`limits.md`, LIM-12):

| Situation | What the counter holds |
|---|---|
| Steady state, one instance | Exactly what was reserved and settled: the bound of §5.5 |
| After a restart | The ledger, from the first request of each tenant. Requests of the previous process whose record was not written are not counted |
| A rule created in the middle of a period | The ledger at creation; requests in flight at that moment enter when the cut passes them, at most `H` + the interval later (17 m 30 s with the defaults) |
| Any discrepancy between memory and ledger | Gone `H` + the interval after the request started; anything else within the hour (rebase) |
| Several instances | Each sees the others' usage through the ledger, `H` + the interval late. Not a supported configuration for limits until epic 8 |

**Rate windows and the concurrency gauge** are not loaded, folded or rebased (ADR 0012, 5.1 D-20).

### 5.7 When the counters cannot be verified (D-10, LIM-11)

The in-process store cannot be unreachable, but it depends on the ledger for a load. The setting
`limits.on_store_failure` covers both that and an error of `Store.Reserve` (the case of the store
of epic 8):

| Value | A load fails (and the counter is not there), or `Reserve` returns an error |
|---|---|
| `refuse` (default) | 503 `unavailable`, `Retry-After: 5`, usage record `internal_error`, nothing forwarded (as 5.1 T-636) |
| `allow` | The request is admitted. After a failed load it is reserved against every limit whose counter exists and not against the others; after a failed `Reserve` it holds no reservation. Log field `limits_unverified: true`; the log line `limit counters unavailable` at warn, at most once per tenant and 10 s. The start-up warns that the setting is in force |

- Rules that cannot be loaded still answer 503 whatever the setting: rules are not counters, and
  a request whose limits are unknown is not forwarded (5.1 §5.5, OPS-11).
- A failed load is not cached; the next request tries again.
- A fold or rebase that fails changes nothing and is retried at the next interval (`limit
  reconciliation failed`, warn, with the class of the error).
- What `allow` admits is in the ledger, so the counters include it once they can be read again.

### 5.8 The 429 `quota_exceeded` and the headers (D-11, LIM-09)

A refusal by a period limit follows 5.1 §5.8 with these differences:

| Part | Value |
|---|---|
| Body | `openai`: `type` `pandokos_error`, `code` `quota_exceeded`; `anthropic`: `error.type` `rate_limit_error`. Message: `Pandokos: a quota or budget of Pandokos was reached; it resets at the time given in Retry-After.` |
| `X-Pandokos-Error` | `quota_exceeded` |
| `Retry-After`, `X-Pandokos-Limit-Reset` | whole seconds to the end of the period, at least 1 |
| `X-Pandokos-Limit-Metric` | one of the six period metrics |
| `X-Pandokos-Limit`, `X-Pandokos-Limit-Remaining` | integers for requests and tokens; for a budget a decimal in the currency with at most six fraction digits and no trailing zeros (`250.5`, `0`) |
| `X-Pandokos-Limit-Currency` | the currency of a budget; absent otherwise |
| `X-Pandokos-Limit-Scope` | `all`, `provider` or `model`: how narrow the rule is. Set on `rate_limited` too |

- When limits of both kinds have no room, the refusal is the first in the order of §5.3 and its
  kind decides the code.
- `LimitRefusal` gains `Code`, `Scope` and `Currency`.
- An SDK treats `quota_exceeded` as its rate-limit error. Its automatic retries are bounded by
  its own retry count; a `Retry-After` of hours is not waited for (T-689 to T-691).
- `no_price` (§5.5) is a 409 with `X-Pandokos-Error` and no limit header; Anthropic type
  `invalid_request_error`, as for every 409.
- Usage record: outcome `rejected_limit` for both codes. Log fields of 5.1 §5.9, with
  `limit_scope` and, for a budget, `limit_currency`; values of a budget are logged as decimals.

### 5.9 Thresholds and events (D-12, D-13, LIM-05)

- A **mark** is reached when `used ≥ At`: thresholds follow what was consumed, not what is
  reserved, so an estimate that is returned at settlement does not raise an alarm. It is tested
  after a settle, a load, a fold and a rebase.
- An enforced period limit is **exhausted** when it refuses a request for the first time in a
  period.
- Both are recorded once per rule and period in `limit_event` (tenant-scoped, append-only; plan
  appendix A1): kind `threshold_reached` (with the percentage) or `exhausted`, the rule and its
  level, subject, scope, metric, currency, period, the rule's value and the value observed. No
  request, caller or content is named.
- The store hands an event to a sink without blocking; `EventWriter` inserts it on the gateway
  role in the tenant's session with `ON CONFLICT DO NOTHING`. The unique key (tenant, rule,
  period start, kind, percentage) makes recording idempotent: the store marks a threshold as
  recorded only when the sink took the event, tests it again otherwise, and after a restart the
  load finds `used` above the mark and offers the event again, which inserts nothing.
- The writer retries while the database is unavailable, drops an event the database refuses (one
  error log line) and is flushed at shutdown within what is left of
  `ledger.shutdown_flush_timeout`. An event is lost only when the process dies between the
  crossing and the insert **and** the period ends before the next start.
- Each inserted row is logged: `limit event recorded` (info; tenant, kind, metric, subject kind,
  scope, percentage).
- Sprint 6.4 writes the outbox row in the transaction that inserts the `limit_event` row, and
  maps the kinds to `limit.threshold_reached` and `limit.exhausted` of ADR 0014. Until then the
  events are read through the API (§5.11).

### 5.10 The per-model default output reservation (D-14)

`model.default_output_tokens` (1 to 1,000,000, null by default), written with the model
(`catalog:write`), carried by the catalogue snapshot into `Target.DefaultOutputTokens`. The line
of 5.1 §5.3 becomes:

```
per = bound.Tokens                      when the caller set a limit
    = target.DefaultOutputTokens        otherwise, when the model has one
    = limits.default_output_tokens      otherwise
per = min(per, target.MaxOutputTokens)  when the catalogue has one
```

### 5.11 Administration API (specification first; plan appendix A3)

| Operation | Method and path | Permission |
|---|---|---|
| `listLimits` | `GET /v1/tenants/{tenant}/limits` | `limit:read` |
| `createLimit` | `POST /v1/tenants/{tenant}/limits` | `limit:write` |
| `getLimit` | `GET /v1/tenants/{tenant}/limits/{rule}` | `limit:read` |
| `updateLimit` | `PATCH /v1/tenants/{tenant}/limits/{rule}` | `limit:write` |
| `deleteLimit` | `DELETE /v1/tenants/{tenant}/limits/{rule}` | `limit:delete` |
| `listTenantPlatformLimits` | `GET /v1/tenants/{tenant}/platform-limits` | `limit:read` |
| `listLimitEvents` | `GET /v1/tenants/{tenant}/limit-events` | `limit:read` |
| `listPlatformLimits` | `GET /v1/platform-limits` | `platform_limit:read` |
| `createPlatformLimit` | `POST /v1/platform-limits` | `platform_limit:write` |
| `getPlatformLimit` | `GET /v1/platform-limits/{rule}` | `platform_limit:read` |
| `updatePlatformLimit` | `PATCH /v1/platform-limits/{rule}` | `platform_limit:write` |
| `deletePlatformLimit` | `DELETE /v1/platform-limits/{rule}` | `platform_limit:delete` |

**A rule as a resource.** `id`, `subject_kind`, `subject_id`, `provider`, `model`, `metric`,
`limit` (integer; 0 for a cost metric), `amount` (decimal string with at most six fraction
digits; empty unless a cost metric), `currency`, `enforce`, `thresholds`, `created_at`,
`updated_at`. A platform rule has `tenant` (an ID, empty for a default) instead of the subject.

**Validation on write** (400 `invalid_request` with the field; 409 `conflict` for an existing
rule of the same subject, scope and metric):

- A count metric needs `limit` (1 to 10¹²) and takes no `amount` or `currency`; a cost metric
  needs `amount` (greater than 0, at most 10⁹, at most six fraction digits) and `currency`, and
  takes no `limit`.
- The three metrics of 5.1 take `enforce: true` and no thresholds. `enforce: false` needs at
  least one threshold. Thresholds are stored ascending; a repeated value is refused.
- `subject_kind` `tenant` takes no `subject_id`; the others need one that **exists in the
  tenant**: a service account by ID; for `key` a virtual key by key ID or a subject mapping by ID.
  The message is the same whether the ID exists in another tenant or nowhere.
- `model` needs `provider`. The provider must exist; the model must be one the tenant can see
  (for a platform rule: must exist). Same message for "elsewhere" and "nowhere".
- A tenant has at most 5,000 rules: the count is checked in the transaction of the creation,
  under an advisory lock per tenant, and the 5,001st is refused with 409 `invalid_state` (problem
  details are constant per code since 1.3; no new code, O-14).
- A platform rule with a `tenant` needs an existing tenant (400 on the field otherwise).
- `PATCH` takes `limit` or `amount`, `enforce`, `thresholds`; at least one; the result must
  satisfy the rules above. Subject, scope, metric and currency never change: delete and create.

Rules whose subject or scope disappears later stay and match nothing (ADR 0049).
`listTenantPlatformLimits` shows a tenant the ceilings that apply to it — the defaults and its own
rows — and nothing of another tenant: the policy of 5.1 does that, not a filter.
`listLimitEvents` is newest first, with filters `kind` and `rule`.

A change reaches the gateway through the notifications of 5.1 (`limits`, `limits:<tenant>`),
normally at once and at the latest after `cache.limits_ttl`; counters are not reset by a change.

### 5.12 Permissions and audit (D-16, D-17)

| Permission | Scope | Role |
|---|---|---|
| `limit:read` | tenant | `tenant-admin` |
| `limit:write` | tenant | `tenant-admin` |
| `limit:delete` | tenant | `tenant-admin` |
| `platform_limit:read` | platform | `platform-admin` |
| `platform_limit:write` | platform | `platform-admin` |
| `platform_limit:delete` | platform | `platform-admin` |

The role `inference` gets none. A platform principal cannot write a tenant's own rules and a
tenant principal cannot write or read platform rules beyond `listTenantPlatformLimits`: the scope
rule of ADR 0031, tested per route (T-678, T-679).

| Action | Log | Resource | Detail |
|---|---|---|---|
| `limit.create`, `limit.delete` | tenant | `limit_rule`, the rule ID | every field of the rule |
| `limit.update` | tenant | `limit_rule` | `from`/`to` of each changed field; subject, scope and metric |
| `platform_limit.create`, `platform_limit.delete` | platform | `platform_limit_rule` | every field, `tenant` included |
| `platform_limit.update` | platform | `platform_limit_rule` | as `limit.update`, with `tenant` |

Written in the transaction of the change (AUD-03); a refused change writes none. A model's
`default_output_tokens` appears in the detail of `model.create` and `model.update`.

### 5.13 The capability check (D-18, API-07)

`checks.budget` takes four values; the check stays an evaluation on committed state by the admin
role, which may read the ledger:

| Value | When | Reason added |
|---|---|---|
| `not_enforced` | no enforced period limit of subject `tenant` matches the provider and model | — |
| `unpriced` | a budget matches, the model has no price in force in the budget's currency, and `limits.on_unknown_price` is `refuse` | `no_price` |
| `exhausted` | for one of the matching limits the ledger aggregate of the current period is at or above the limit | `budget_exhausted` |
| `available` | otherwise | — |

The two reasons come after those of 3.3, in this order: `no_price`, `budget_exhausted`. As since
3.3, the check describes a key of the tenant without pin and model list, so rules of service
accounts and keys are not evaluated; rate limits are not either (their state lasts seconds, 5.1
D-14). `available` means "not exhausted": a call whose estimate exceeds the remainder is still
refused. The aggregate does not include requests in flight.

### 5.14 Configuration

| Key | Type | Default | Bounds | Meaning |
|---|---|---|---|---|
| `limits.time_zone` | string | `UTC` | an IANA zone name known to the embedded database, whose offsets are whole minutes | The zone in which days and months of quotas and budgets start |
| `limits.on_unknown_price` | string | `refuse` | `refuse`, `allow` | What happens to a request that a budget applies to and cannot measure (§5.5) |
| `limits.on_store_failure` | string | `refuse` | `refuse`, `allow` | What happens when the counters of a request cannot be verified (§5.7). `allow` produces a start-up warning |
| `limits.reconcile_interval` | duration | `5m` | 10 s to 1 h | How often period counters are folded into the ledger (§5.6) |

A value out of bounds is a start-up error that names the key. The horizon, the rebase interval
(1 h), the load timeout (5 s) and the event queue (1,024) are not keys.

### 5.15 Wiring, CLI, documentation

**`serve`** builds the calendar, the ledger reader on the worker pool, the event writer on the
gateway pool, `Memory` with the event sink, and the `Reconciler` (store, reader, calendar,
`writer.OldestPendingStart`, horizon, interval); passes the reconciler, the calendar and the two
settings to `resolver.NewLimits`, and the calendar and `on_unknown_price` to `admin.New`. It runs
`Reconciler.Run` and `EventWriter.Run` with the background context; at shutdown the event writer
is closed after the ledger writer. Nothing is read at start: a tenant's counters are loaded with
its first request.

**CLI** (each command maps to one operation; `--tenant` as elsewhere):

```
pandokos limit list|get|create|update|delete      --tenant T [RULE_ID]
pandokos limit platform --tenant T                 the ceilings that apply to the tenant
pandokos limit events   --tenant T [--kind K] [--rule ID]
pandokos platform-limit list|get|create|update|delete [RULE_ID]
pandokos model create|update … --default-output-tokens N     (0 clears)
```

`create` flags: `--metric` (required), `--limit N` or `--amount DECIMAL --currency CCC`,
`--subject tenant|service-account|key` with `--service-account ID_OR_NAME` or `--key KEY_ID`
(tenant by default), `--provider`, `--model`, `--soft` (not enforced), `--threshold N`
(repeatable); for `platform-limit`, `--for-tenant T` (absent: the default). `update` flags:
`--limit`, `--amount`, `--soft`, `--enforce`, `--threshold`, `--no-thresholds`.

**`docs/operations/limits.md`** gains: periods and the time zone, with the daylight-saving cases;
budgets, currencies and unknown prices; scopes; thresholds and events; the table "strict, bounded,
best effort" below; counters and the ledger (restart, new rules, the horizon); the failing store;
managing rules with the CLI; choosing values for a budget.

| Limit | Guarantee |
|---|---|
| Requests per minute, concurrent requests | strict (5.1) |
| Requests per day or month | strict on what the counter sees |
| Tokens per minute, day or month; cost per day or month | strict on reserved amounts; bounded on actual ones (§5.5) |
| Any period limit, right after a rule is created, or with records that never reached the ledger | best effort for the cases of the table in §5.6 |
| Any limit with several instances | per instance (until epic 8) |
| Thresholds and events | recorded once per rule and period; best effort across a crash at the end of a period |

## 6. Isolation harness

- `limit_event`: tenant-scoped with the policy `tenant_isolation`; a fixture in
  `DefaultSpec.Fixtures` (insert only); listed in `AppendOnly`; grants in `DefaultSpec` (gateway:
  insert of the listed columns; admin: select; worker: none). Its own test (T-661) for the roles,
  the constraints and the unique key.
- `limit_rule`, `platform_limit_rule`: the changed grants and the new columns in `DefaultSpec`;
  T-626 extended to the new constraints.
- The ledger reader runs as the worker role in `WithTenant`; no statement names the tenant in a
  `WHERE` clause (T-658, T-659). The capability check reads the ledger as the admin role in the
  tenant's session.
- Every one of the twelve routes declares a permission (`TestEveryRouteDeclaresAPermission`) and
  has a cross-tenant negative test (T-679); the platform routes are refused to every tenant
  principal (T-678).
- Counters: keyed by tenant; T-621 extended by T-648 (a period counter of one tenant is not
  changed by another with the same IDs).
- No definer function is added.

## 7. Test infrastructure and CI

- Time: `Memory`, `Calendar`, `Reconciler` and `EventWriter` take a clock; no test sleeps to pass
  a boundary. `limitstest.Real` gains the reconciler, a fake `Ledger` that commits records with a
  chosen delay, and `Advance` across midnight and across a month.
- A reference model for the property tests keeps every request (start, admission, reservation,
  settlement, commit time) and computes the ledger and the bound independently of the store.
- Zones in tests come from the embedded database (`time/tzdata` imported by the test helper).
- Rules in database tests are written through the admin role (`dbtest.SeedLimitRule` and
  `SeedPlatformLimit` gain the new columns) or through the API; usage rows through the ledger
  writer or `dbtest.SeedUsage` on the gateway role.
- The SDK harness (`test/sdk/external_test.go`) creates, through the CLI, one key per family and
  code with a rule of subject `key` (`requests_per_minute` 1; `requests_per_day` 1) and passes them
  as `PANDOKOS_KEY_RATE_OPENAI`, `…_RATE_ANTHROPIC`, `…_QUOTA_OPENAI`, `…_QUOTA_ANTHROPIC`.
- CI: no new job. `make bench` includes a period-limit case in T-622.

## 8. Decisions

| # | Decision | Reason | Recorded in |
|---|---|---|---|
| D-1 | **One time zone per installation** (`limits.time_zone`, default `UTC`), zone database embedded; a period is defined by the civil date of the request's **start**; days are 23 to 25 hours long where the zone says so | LIM-02 asks for a configurable zone; one zone keeps one counter per rule and lets platform and tenant rules share it; the start time is the ledger's `started_at`, so counter and aggregate are the same sum | ADR 0050 |
| D-2 | **A request counts exactly when its record carries a credential ID**; the gateway settles when the credential was opened and releases otherwise | The ledger must be able to say which records count; `credential_id` is there, a "forwarded" flag is not. Changes 5.1 only for a caller gone, or a panic, between the credential and the proxy call | ADR 0050, history of 0048 |
| D-3 | **Period counters live behind the same `Store`**, as `base` from the ledger and `live` by start minute; `Reserve` stays one atomic call over all kinds | One call keeps "all or nothing"; the split is what reconciliation needs | ADR 0051 |
| D-4 | **A budget has one currency and counts costs in that currency only; nothing is converted**; a request priced in another currency is, for that budget, unpriced | Conversion needs rates Pandokos does not have and would make the budget depend on them; one rule decides both cases | ADR 0050 |
| D-5 | **Unknown price under a budget: refuse by default with 409 `no_price`, or admit uncounted** (`limits.on_unknown_price`); an unknown cost at settlement counts 0 | ADR 0012: refusal is the default. A 429 would invite retries that cannot succeed; a distinct code tells the operator to add a price | ADR 0050, history of 0020 |
| D-6 | **Counters are loaded from the ledger on first use, per tenant, not at start** | Start does not depend on the number of tenants; the first request of a tenant already loads its rules; "rebuilt at start" holds from the first request | ADR 0051 |
| D-7 | **Reconciliation by a cut**: before the cut a counter is the ledger, after it the instance's own reservations and settlements by start minute; fold every interval, rebase every hour; the cut waits for unwritten records | Exactly-once hand-over from memory to ledger without tracking single requests and without stopping traffic; self-correcting within the horizon | ADR 0051 |
| D-8 | **The reader runs on the worker role, in each tenant's session, inside the one process** | The gateway role reads no usage (ADR 0039); TEN-06: no unscoped identity. A gateway process without the worker role (epic 8) uses the shared store | ADR 0051 |
| D-9 | **Scope, currency, enforcement and thresholds are columns of both rule tables**, for all nine metrics; one rule per subject, scope and metric | LIM-04 does not single out metrics; one shape for one API | ADR 0052, history of 0049 |
| D-10 | **`limits.on_store_failure`: refuse (503) by default; `allow` admits without the limits that could not be verified.** Rules that cannot be loaded always refuse | LIM-11; rules are authorization data (OPS-11), counters are accounting that the ledger restores | ADR 0051 |
| D-11 | **`quota_exceeded` for the six period metrics**, `Retry-After` to the end of the period; two more headers (`…-Currency`, `…-Scope`); budget values as decimals | ADR 0020 has the code; the caller needs the unit and the reset | ADR 0050, history of 0020 |
| D-12 | **Thresholds are percentages on the rule, tested against what was used; a rule can be unenforced** | One counter serves the hard limit and its warnings; "alert, never block" is one flag | ADR 0052 |
| D-13 | **An event is a row of `limit_event`, unique per rule, period, kind and percentage, inserted by the gateway role off the request path; `exhausted` is the first refusal of a period** | The fact is durable and idempotent now; 6.4 adds the outbox in the same transaction (ADR 0014) | ADR 0052, history of 0014 |
| D-14 | **Per-model default output reservation: `model.default_output_tokens`**, before the configuration key | The epic's risk note; the catalogue is where per-model values live | ADR 0050, history of 0035 |
| D-15 | **Two resources**: `/v1/tenants/{tenant}/limits` and `/v1/platform-limits`, each for every metric; a tenant reads the ceilings that apply to it | 5.1 O-1; mirrors the two tables and the two scopes | ADR 0052 |
| D-16 | **Six permissions**, read, write and delete per scope; `tenant-admin` and `platform-admin` get theirs | The pattern of token issuers; delete is separable | ADR 0052 |
| D-17 | **Six audit actions**; subject and scope must exist on write, with one message for "elsewhere" and "nowhere"; 5,000 rules per tenant enforced under a lock | AUD-01; no oracle for other tenants; the loader's limit can then never be hit | ADR 0052, history of 0032 |
| D-18 | **`checks.budget` is computed from the ledger by the admin role**, for rules of subject `tenant`; value `unpriced` added | Independent of where counters live (a split deployment keeps working); same evaluation "on committed state" as the rest of the check | history of 0043 |
| D-19 | **Budget values are integers of millionths in storage and in `Limit.Max`, decimals at the API and in headers; counters of cost are exact decimals** | One column and one comparison for all metrics; no rounding of costs, which have up to 18 fraction digits | ADR 0050 |
| D-20 | **No new dependency**; `time/tzdata` is part of the standard library | ADR 0018 | sprint decision |

## 9. Tests

Levels as in 5.1: **U** unit; **D** test database; **I** in-process gateway with the fake
provider; **E** built binary; **S** SDK suite; **R** repository test; **P** property test;
**−race** value is the race detector. IDs continue from T-646 and T-R41.

### New tests

| ID | Test | Level | Proves | Requirement |
|---|---|---|---|---|
| T-647 | `limits.TestCalendar` | U, P | For fifteen zones (UTC, half- and quarter-hour offsets, +14, zones whose clocks change at midnight, a 30-minute change) and every day of 2024 to 2030 plus random instants: `Start ≤ t < End`; the label one nanosecond before `Start` is earlier; periods are contiguous; a month starts where its first day starts; days of 23 and 25 hours; boundaries are whole minutes; an offset with seconds is refused | **LIM-02** |
| T-648 | `limits.TestPeriodCounters` | U | The table of §5.4: reserve, settle below and above, release, expiry; attribution to the period of `Start` when admission is after midnight; a new period starts empty without a load; refusal fields (`Remaining`, `RetryAfter` to the period end, `OverLimit`); `Max = 0` never refuses; mixed with rate limits nothing is taken when any limit refuses; `ErrNotLoaded`; a start before the cut is an error; two tenants with the same IDs do not share a counter | **LIM-02**, **LIM-06**, TEN-06 |
| T-649 | `limits.TestBudgetCounters` | U | Costs with 18 fraction digits sum exactly; `Max` in millionths; `Remaining` rounds down; a settled cost that is unknown or in another currency counts 0; counters of two currencies are apart | **LIM-03** |
| T-650 | `limits.TestPropertyPeriods` | U, P | Random schedules with clock steps across days and months against the reference: the inequality of §5.5 for requests, tokens and cost; strict when nothing exceeds its reservation; after every reservation ended `reserved` is 0 | **LIM-07**, LIM-03 |
| T-651 | `limits.TestLoadFoldRebase` | U | Load: `base` and seed from groups, existing counters untouched; fold: pieces cut at day starts go to the right periods, dropped minutes equal added figures; the cut never moves back and stops at a pending record; rebase replaces `base` only; marks tested after each | LIM-03 |
| T-652 | `limits.TestPropertyReconcile` | U, P | Random interleavings of requests (start, admission, end, commit delay, lost and rejected records, records of another writer, rules created mid-way) with loads, folds and rebases: at every step `counted ≤ ledger + uncommitted settled + reserved` (never twice); `H` + interval after the last request every counter equals the ledger sum (never lost) | **LIM-07**, LIM-08 |
| T-653 | `limits.TestMarks` | U | Fires on `used`, not on a reservation; once per rule, percentage and period; marks of a platform and a tenant rule on one counter; a refused offer is repeated; `exhausted` on the first refusal only; offered again after a load above the mark; a changed value moves the mark | **LIM-05** |
| T-654 | `limits.TestResolveScopes` | U, P | The order of §5.3; scope matching; the merge of platform and tenant rules (minimum of enforced values, both sets of marks, two limits when currencies differ); for random rule sets adding a rule never removes or raises a limit; at most one limit per counter | **LIM-04** |
| T-655 | `limits.TestMemoryRacePeriods` | U, −race | 64 goroutines reserve and settle across a day boundary while others load, fold, rebase and sweep: no race; conservation holds | LIM-06 |
| T-656 | `limits.TestSweepPeriods` | U | A counter is dropped one horizon after its period ended, and after an idle hour with nothing reserved; a tenant without counters loses its cut; a dropped counter is `ErrNotLoaded` until loaded | LIM-08 |
| T-657 | `limits.TestUsageSum` | U | `Sum` for each subject kind, scope and unit; a group of another currency adds requests and tokens and no cost | LIM-03, LIM-04 |
| T-658 | `limits.TestLedgerUsage` | D | The query as the worker role in the tenant's session against seeded rows: credential rule, provider and estimated tokens, unknown cost, minute grouping from `liveFrom`, a row one microsecond before a boundary, `rejected_limit` rows count nothing; another tenant's rows are never seen; the gateway role cannot run it | **TEN-04**, LIM-08 |
| T-659 | `limits.TestReconciler` | D | `Ensure`: one load for concurrent callers, nothing cached after a failure, 5 s bound; `Run`: fold at the interval, rebase after an hour, a failing ledger changes nothing and is retried; every query runs in the session of its tenant | **TEN-06**, LIM-11 |
| T-660 | `limits.TestEventWriter` | D | One row per rule, period, kind and percentage however often offered; retry while the database is unavailable; a refused row is dropped with one log line; a full queue refuses the offer; `Close` flushes; the row and the log line name no caller | **LIM-05** |
| T-661 | `isolation.TestLimitEventPolicies` | D | `limit_event` for each role and session: gateway inserts own tenant only and reads nothing; admin reads own tenant only; worker nothing; nobody updates, deletes or truncates, the owner included; every check constraint | **TEN-04**, **TEN-07** |
| T-662 | `limits.TestLoadRulesColumns` | D | The loader maps scope, currency, enforcement and thresholds of both tables; a row this version does not know is skipped | LIM-04 |
| T-663 | `gateway.TestEstimateModelDefault` | U | The formula of §5.10 at each branch | LIM-07 |
| T-664 | `resolver.TestLimitsAdapterPeriods` | U | Provider, model and start reach `For`; `Ensure` is called only when a period limit applies; both values of both settings; a currency mismatch is unpriced; the refusal's code, scope and currency | **LIM-11**, LIM-03 |
| T-665 | `gateway.TestQuotaExceeded` | I | Both families, each of the six metrics: status, body, every header of §5.8 with its exact value (decimals of a budget, currency, scope); the record and the log fields; a provider's own 429 still passes unchanged | **LIM-09** |
| T-666 | `gateway.TestNoPrice` | I | `refuse`: 409 `no_price` in both shapes, nothing forwarded, record `rejected_limit`; `allow`: forwarded, other limits enforced, `budget_unpriced` logged, the budget unchanged | **LIM-03**, LIM-12 |
| T-667 | `gateway.TestCountedWithCredential` | I | Caller gone before the credential: release, record without credential. Caller gone, or a panic, after it: settle with one request and no tokens, record with credential. The table of 5.1 §5.7 otherwise unchanged | **LIM-08** |
| T-668 | `gateway.TestStoreFailure` | I | A ledger that fails: `refuse` gives 503 with `Retry-After: 5` and forwards nothing; `allow` forwards, still enforces rate limits and loaded counters, logs `limits_unverified`; rules that cannot be loaded give 503 with both values | **LIM-11** |
| T-669 | `gateway.TestMonthlyBudget` | I, D | The exit condition with the real store, reconciler, ledger writer and database: requests are refused once reservations reach the budget; the unused part of a reservation returns at settlement; the first request of the next month is admitted; the old month's counter still takes the settlement of a request in flight | **LIM-03**, LIM-02 |
| T-670 | `gateway.TestRestartMatchesLedger` | I, D | After traffic of several keys and models, a second store built on the same database holds, for every rule, exactly the ledger aggregate; an exhausted budget refuses the first request after the restart; a record lost before the flush is not counted | **LIM-03**, LIM-12 |
| T-671 | `gateway.TestParallelBudget` | I, −race | 64 workers against a budget *L* with concurrency *k* and a provider that reports *d* above every reservation: spend ≤ *L* + *k*·cost(*d*), and ≤ *L* when *d* is 0; with a fold running every 50 ms the result is the same | **LIM-07** |
| T-672 | `gateway.TestScopedRules` | I, D | A provider rule limits that provider only; a model rule that model only; a rate rule with a scope; a key rule with a model scope; the tightest refuses | **LIM-04** |
| T-673 | `gateway.TestThresholdEvents` | I, D | Crossing 80 % of a budget writes one `threshold_reached` row; an unenforced rule never refuses and records 100 %; the first refusal writes one `exhausted` row and the next none; a new period records again | **LIM-05** |
| T-674 | `gateway.TestDayInZone` | I | Zone `Europe/Rome`: a daily quota resets at local midnight on a 23-hour and a 25-hour day; `Retry-After` is the time to that instant | **LIM-02** |
| T-675 | `admin.TestLimits` | D | The seven tenant operations: every validation rule of §5.11 with its field; subject and scope existence with the same answer for another tenant's IDs; duplicate 409 `conflict`; the 5,000 rule bound (lowered through a test seam) under concurrent creations, 409 `invalid_state`; pagination and filters; a change is notified | **LIM-04**, **IAM-04** |
| T-676 | `admin.TestPlatformLimits` | D | The five platform operations; default and per-tenant rows; unknown tenant; `listTenantPlatformLimits` shows the defaults and the tenant's rows only | **LIM-04** |
| T-677 | `admin.TestLimitAudit` | D | Each of the six actions writes one row in the right log, in the transaction of the change, with the detail of §5.12; a refused change writes none | **AUD-01**, **AUD-03** |
| T-678 | `admin.TestLimitPermissions` | D | Each of the twelve operations needs its permission: refused to the role `inference`, to a key scoped without it, to a platform key on tenant routes and to a tenant key on platform routes | AUD-01, TEN-07 |
| T-679 | `isolation.TestLimitAPI` | D | For every new route with a tenant in the path: a key of tenant B gets the same 404 for tenant A as for a tenant that does not exist; a rule ID or event of A is not found under B | **TEN-07** |
| T-680 | `admin.TestLimitEvents` | D | Newest first, filters, cursor; nothing of another tenant | LIM-05 |
| T-681 | `admin.TestCapabilityBudget` | D | The four values of §5.13 for requests, tokens and cost; scoped rules; unenforced and service-account rules ignored; reasons and their order; `callable` false when exhausted or unpriced | **API-07** |
| T-682 | `admin.TestModelDefaultOutput` | D | Create, change and clear the value; bounds; audit detail; it reaches `Target` | LIM-07 |
| T-683 | `authz.TestLimitPermissions` | U | Six permissions with their scopes; the two roles; `inference` unchanged | AUD-01 |
| T-684 | `cli.TestLimitCommands` | U | Each command calls its operation with the flags of §5.15; flag errors; `--amount` is passed as written | LIM-04 |
| T-685 | `config.TestQuotaKeys` | U | Defaults and bounds of §5.14; an unknown zone and an unknown value name the key; the warning for `allow` | **SEC-04** |
| T-686 | `e2e.TestMonthlyBudget` | E | The binary: a budget created with the CLI; calls until 429 `quota_exceeded`; the process is stopped and started and the next call is refused at once; raising the budget with the CLI admits the next call within 2 s; the audit log holds the two changes | **LIM-03**, **LIM-09**, AUD-01 |
| T-687 | `e2e.TestLimitAdministration` | E | With the CLI: a rate rule of 5.1, a quota, a scoped rule, a platform default, a soft rule; list, change, delete; `capability check` says `exhausted`; `limit events` shows the threshold | LIM-04, **LIM-05**, API-07 |
| T-688 | `e2e.TestQuotaCanary` | E | At log level `debug`, with canaries in body, provider credential and virtual key: no log line, record, event, audit row, header or error body holds them | OBS-04 |
| T-689 | `test_limits.py` | S | The OpenAI and Anthropic Python SDKs raise their rate-limit error for `rate_limited` and for `quota_exceeded`, with status 429 and `x-pandokos-error` readable | **LIM-09** |
| T-690 | `limits.test.mjs` | S | The same for the TypeScript SDKs | **LIM-09** |
| T-691 | `sdk.TestQuotaError` | S | The OpenAI Go SDK: `quota_exceeded` is its 429 error; with default retries the call returns within 10 s although `Retry-After` is hours | LIM-09 |
| T-R42 | `repo.TestLimitsGuideQuotas` | R | `limits.md` names every metric, both codes, every header constant, the four keys, the horizon, and has the table of §5.15 with its six rows | **LIM-12** |
| T-R43 | `repo.TestLimitsBoundaries` | R | `internal/limits` does not import `internal/ledger` or `internal/admin`; only `cmd/pandokos` and tests give the reader the worker pool; `internal/admin` names neither `limits.Memory` nor `limits.Reconciler` | — |
| T-R44 | `repo.TestLimitsReference` | R | `docs/reference/permissions.md` lists the six permissions and `docs/reference/admin-api.md` the twelve operations and six audit actions | AUD-01 |

### Changed tests of earlier sprints

| Test | Change |
|---|---|
| T-140 (schema lint), `TestDefaultSpecMatchesDesign` | the columns, grants, `limit_event`, its fixture and `AppendOnly` entry |
| migration tests (count, checksums) | migration 0010 |
| T-611, T-619, T-620, T-622 | `Limit` has a scope; `For` takes a `Query`; the pairwise check of repeated counters scales to 81 limits; a period case in the benchmark |
| T-624, T-626 | the new columns and constraints of both rule tables |
| T-628 | the model default (the cases move to T-663 where they are new) |
| T-631, T-632 | `X-Pandokos-Limit-Scope` on `rate_limited`; `limit_scope` in the log |
| T-634, T-635 | the cases after the credential settle (D-2) |
| T-636, T-637 | the store-failure setting; `Query` |
| sweeps over every error code, the outcome table | `quota_exceeded` (429, `rate_limit_error`) and `no_price` (409, `invalid_request_error`) → `rejected_limit` |
| T-R40, T-R41 | the import list and the constants of this sprint |
| capability tests of 3.3 | `checks.budget` is `not_enforced` only without a matching rule |
| catalogue tests (model resource, snapshot) | `default_output_tokens` |
| `TestEveryOperationNeedsACredential`, CLI command coverage, the configuration reference test | twelve operations, the commands, four keys |
| SDK harness | the four limited keys |

### Requirement coverage

| Requirement | Tests |
|---|---|
| LIM-02 | T-647, T-648, T-669, T-674 |
| LIM-03 | T-649, T-650, T-664, T-666, T-669, T-670, T-686 |
| LIM-04 | T-654, T-662, T-672, T-675, T-676, T-687 |
| LIM-05 | T-653, T-660, T-673, T-680, T-687 |
| LIM-06, LIM-07, LIM-08 | T-648, T-650, T-652, T-663, T-667, T-671 |
| LIM-09 | T-665, T-686, T-689 to T-691 |
| LIM-11 | T-659, T-664, T-668 |
| LIM-12 | T-R42, T-666, T-670 |
| IAM-04 (limits) | T-675 |
| AUD-01, AUD-03 | T-677, T-678, T-683, T-686, T-R44 |
| API-07 (budget) | T-681, T-687 |
| TEN-04, TEN-06, TEN-07 | T-648, T-658, T-659, T-661, T-679, T-140 |
| SEC-04 | T-685 |

## 10. Definition of done for this sprint

**Requirements and tests**

- [ ] Every test of §9 exists under the stated name and passes; the changed tests pass; `make
      check`, `make test-sdk-python`, `make test-sdk-ts`, `make test-container` and `make
      test-compose` pass.
- [ ] The tests-first record exists: the commit of part B precedes the implementation commits and
      the pull request quotes its failing output.
- [ ] The property tests (T-647, T-650, T-652, T-654) ran with at least three seeds besides the
      default, and `go test -race -count=20 ./internal/limits/` passes; both are quoted.
- [ ] The coverage table is repeated in the pull request with links to the CI run.

**Exit conditions of the epic's sprint**

- [ ] A tenant with a monthly budget is refused once reservations reach it and admitted again
      after the period resets (T-669, T-686).
- [ ] After a restart counters match the ledger (T-670, T-686).

**Isolation and permissions**

- [ ] `limit_event` has the policy `tenant_isolation`, a fixture and its `AppendOnly` entry;
      T-140 reports nothing; T-661 passes.
- [ ] Each of the twelve routes declares a permission and has a cross-tenant negative test
      (T-678, T-679).
- [ ] Every change of a rule writes an audit record in its transaction (T-677).
- [ ] The gateway role reads no usage record and the worker role reads no rule.

**Secrets and content**

- [ ] T-688 passes at log level `debug`; `make secrets` passes.
- [ ] `internal/limits` still receives numbers and identifiers only; `limit_event` names no
      request and no caller.

**Documentation and decisions**

- [ ] `docs/operations/limits.md` with the sections of §5.15; `configuration.md`, `gateway.md`,
      `usage.md`, `catalogue.md`, `database.md`; `docs/reference/admin-api.md` and
      `permissions.md`; overview, data model and security model.
- [ ] ADR 0050 to 0052 with status `proposed`; history lines on 0012, 0014, 0020, 0032, 0035,
      0043, 0048, 0049; the index.
- [ ] `api/openapi/admin.v1.yaml` is current and `make generate-check` passes.
- [ ] The epic file and the roadmap say what moved to later sprints; `status:` of this file is
      `done`; the review records the answers to §12.

## 11. Consequences for later sprints

- **6.1** (metrics): refusals by code, metric, subject kind and scope; requests admitted
  unverified or unpriced; reservations outstanding and period counters held; reconciliation runs,
  their duration and failures; the lag of the cut (a cut that does not advance is an alert);
  events recorded and dropped; reserved against settled cost.
- **6.2** (reports and rollups): the aggregate of §5.6 scans a tenant's rows of the period at
  load, at the hourly rebase and for each capability check; a rollup by day, service account, key,
  provider, model and currency makes all three cheap, and must use the same three figures and the
  credential rule (`limits.Sum` is the definition). A "used against limit" report belongs there.
  `rejected_limit` rows now have three codes.
- **6.3** (tenant deletion): removes the tenant's rows of `limit_rule`, `platform_limit_rule` and
  `limit_event`; the last is append-only for every role, so through the definer function it adds
  for the other append-only tables.
- **6.4** (events): inserts the outbox row in `EventWriter`'s transaction, grants the worker what
  the relay needs, maps the kinds to the CloudEvents types of ADR 0014, and decides whether a
  usage event carries the limits it counted against. The event has no request ID by design.
- **Epic 8** (shared counters): the Valkey `Store` holds period counters under the same key and
  the same attribution (start time); the worker reconciles them with the same cut, reading the
  oldest pending record of every instance; `limits.on_store_failure` then covers a store that is
  really unreachable, and "continue with per-instance counters" is its `allow`. A gateway process
  without the worker role cannot load from the ledger: it needs the shared store. `Calendar` and
  `Sum` are reused unchanged; the property tests T-650 and T-652 run against it.
- **4.3** (roles): the six permissions are available to custom roles; a "limits manager" role is
  a candidate.
- **12** (operability, Helm): `limits.time_zone` must be the same on every instance; a shipped
  default concurrency (5.1 O-4) can now be a platform rule created by `init` or the chart.
- **R4** (unified endpoint): scopes name the resolved provider and model.

## 12. Risks and open points

### What was verified while designing

- Migration 0010 as written in the plan was applied after 0001 to 0009 on a throwaway PostgreSQL
  16.15 started for the purpose (2026-11-18; the Docker test database was not reachable in the
  design session) and exercised as each runtime role: the new columns, every new check constraint
  with a failing row, the widened unique keys, the column grants (an update of `provider` or
  `currency` is refused), `limit_event` (gateway inserts in its tenant only and cannot read; the
  second insert of the same rule, period, kind and percentage inserts nothing; admin reads its
  tenant only; the worker reads nothing; the owner cannot delete), `model.default_output_tokens`.
- The queries of appendix A2 were compiled with `sqlc` v1.31.1 against the ten migrations on a
  copy of the repository; `AggregateUsageForLimits` was run as the worker role against rows with
  provider, estimated and no usage, with and without credential. The schema lint (T-140) and
  `apigen` on appendix A3 were **not** run: part A does.
- The ledger stores `started_at` truncated (not rounded) to the microsecond
  (`internal/ledger/rows.go`); settlement uses `costUsage`, whose input and output are what the
  two `COALESCE` terms of the query read back.
- Between the opening of the credential and the call to the proxy the handler can end by the
  caller's cancellation or a panic in `Prepare` only (`internal/gateway/handler.go`).
- `gateway` inserts usage rows with `ON CONFLICT DO NOTHING` without any `SELECT` privilege; the
  same works for `limit_event`.
- The highest test IDs are T-646 and T-R41; the highest ADR is 0049; the last migration is 0009.
  No outbox table exists yet.
- Not verified: the retry behaviour of the Python and TypeScript SDKs on a 429 whose
  `Retry-After` is hours (T-689, T-690 do); that the embedded zone database and the one of the
  runtime image never disagree (the embedded one is only used when the system has none).

### Risks

| # | Risk | Mitigation |
|---|---|---|
| R-1 | A request is counted twice or not at all around a fold | One invariant (§5.6), applied in one critical section; T-651; the property test T-652 against an independent reference; the hourly rebase bounds any defect |
| R-2 | A request outlives the horizon and its settlement lands in a dropped minute | The reservation expires first (5.1 §5.4) and is logged at error; the ledger row enters with the rebase |
| R-3 | The cost estimate is far below the actual cost (cache writes, audio, a higher price tier) and a budget is overshot | Bounded and documented (§5.5); settlement counts the actual cost, so nothing more is admitted; the guide advises a concurrency limit |
| R-4 | The cost estimate is far above (a high `max_tokens` by habit) and requests are refused near the end of a budget | The per-model default (D-14); the remainder returns at settlement; `reserved_tokens` and `estimate_over_limit` in the log; the guide |
| R-5 | The load makes the first request of a tenant slow, or fails and refuses it | One indexed range query per period in the tenant's session, single flight, 5 s bound; `limits.on_store_failure`; 6.2's rollup |
| R-6 | A tenant with millions of records per month makes load, rebase and capability check expensive | Range scan on the primary key within one partition; hourly at most for the rebase; measured in T-669's log and revisited with 6.2 |
| R-7 | The wall clock steps back and the cut or a period boundary is crossed twice | The cut never moves back; periods are a function of the request's start; a start before the cut is refused as unverifiable |
| R-8 | Operators run two instances and expect one budget | Stated in `limits.md` and the release notes; the ledger keeps the instances within `H` + interval of each other |
| R-9 | Records lost at a crash are spend that no budget counts | Documented as best effort; the provider's invoice is the authority (USE-09) |
| R-10 | `allow` is set and forgotten | Start-up warning; `limits_unverified` and `budget_unpriced` in the request log; 6.1 counts them |
| R-11 | An SDK retries a `quota_exceeded` and writes a ledger row per attempt | Bounded by the SDK's retry count (T-689 to T-691); refusals are cheap (5.1 R-6) |
| R-12 | A zone changes its rules and the embedded database is older than reality | A Pandokos release with a newer toolchain updates it; the system database is preferred when present |
| R-13 | The API lets a tenant create 5,000 rules and every request resolves against them | Resolution is by map lookup per subject and scope, not a scan; T-622 reports it |

### Open points (raised to the maintainer; none blocks the sprint)

| # | Point | Recommendation |
|---|---|---|
| O-1 | D-2 changes a 5.1 rule: a request whose caller goes away after the credential was opened and before the proxy is called now counts as a request. Alternative: a `forwarded` column in `usage_record` | Accept D-2: no ledger change, and the window is a few instructions wide |
| O-2 | One time zone per installation (D-1). Alternatives: a zone per tenant (a tenant attribute) or per rule | Keep one; a per-tenant zone can be added later without touching stored state, since counters are rebuilt from the ledger |
| O-3 | A new stable code `no_price` (409) for a request that a budget cannot measure. Alternatives: `quota_exceeded` (429) or `model_not_allowed` (403) | New code: neither alternative is true, and both mislead the caller's retry logic |
| O-4 | Refusal is the default for an unknown price (ADR 0012). An installation with budgets and one unpriced model refuses that model for the tenants with a budget | Keep; the guide says to add a price override or to set `allow` |
| O-5 | A price in another currency than the budget is treated as unpriced (D-4). Alternative: several budgets per scope, one per currency | Keep one budget per scope; a second currency can have its own budget on a provider or model scope |
| O-6 | Counters are loaded with a tenant's first request, not at start (D-6) | Keep; start-up time independent of the tenant count, and the exit condition holds from the first request |
| O-7 | The reconciliation interval is a key; horizon and rebase interval are not | Keep: the horizon is derived from two existing keys, the rebase is a safety net |
| O-8 | `exhausted` is recorded at the first refusal of a period, also for platform ceilings, in the tenant's event list | Keep: it is the event an integrator asked for (LIM-13) and it concerns the tenant |
| O-9 | Thresholds are tested against what was used, not what is reserved | Keep: an alarm should not fire for an estimate that is returned |
| O-10 | The capability check reads the ledger (D-18) and so ignores requests in flight, and costs one aggregate per call until 6.2 | Keep; documented; 6.2's rollup |
| O-11 | Budget amounts have at most six fraction digits and are at most 10⁹ units | Keep; raise the bound when someone needs it (a check constraint) |
| O-12 | Rate metrics can be scoped to a provider or model too (D-9) | Keep: LIM-04 does not distinguish, and it costs nothing |
| O-13 | Three permissions per scope. Alternative: read and write only | Keep three, as for token issuers |
| O-14 | The 5,000-rule bound answers 409 `invalid_state`, whose constant detail does not name the bound. Alternative: a code of its own | Keep `invalid_state`: no new code for a bound almost nobody meets; the reference page explains it |
| O-15 | No "used against limit" endpoint in this sprint | Defer to 6.2, which has the rollups; the 429 headers and the capability check cover the urgent cases |

### Maintainer decisions on the open points (2026-11-18)

| Point | Decision |
|---|---|
| O-1 | Accepted D-2: a request counts once its credential is opened; no `forwarded` column |
| O-2 | Accepted: one time zone per installation |
| O-3 | Accepted: new stable code `no_price` (409) |
| O-4 | Accepted: refuse is the default for an unknown price under a budget |
| O-5 | Accepted: a price in another currency is unpriced for that budget |
| O-6 | Accepted: counters are loaded from the ledger on a tenant's first use |
| O-7 | Accepted: only the reconcile interval is a configuration key |
| O-8 | Accepted: `exhausted` at the first refusal of a period, shown to the tenant also for platform ceilings |
| O-9 | Accepted: thresholds are tested against used, not reserved |
| O-10 | Accepted: the capability check ignores in-flight requests and runs one aggregate per call until 6.2 |
| O-11 | Accepted: amounts with at most 6 fraction digits and at most 1e9 units |
| O-12 | Accepted: rate metrics can be scoped too |
| O-13 | Accepted: three permissions per scope |
| O-14 | Accepted: the rule cap answers 409 `invalid_state` |
| O-15 | Deferred to 6.2: a "used against limit" endpoint |
