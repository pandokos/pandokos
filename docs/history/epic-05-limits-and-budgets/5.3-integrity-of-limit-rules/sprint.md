---
id: SPRINT-5.3
title: Integrity of limit rules
epic: EPIC-5
release: v0.2
status: done
design: draft 2026-11-18
---

# Sprint 5.3 — Integrity of limit rules

- Epic: [5 — Limits and budgets](../epic.md)
- Implementation plan: [plan.md](plan.md)
- Previous sprint: [5.2 — Quotas and budgets](../5.2-quotas-and-budgets/sprint.md)
- Risk class: **high** (a data migration of rules that are being enforced, on tables under forced
  row-level security; deletions that cross the tenant boundary inside one transaction; a calendar
  per tenant under counters that must still agree with the ledger; a change of the usage ledger)
- Decisions implemented: ADR [0049](../../../../docs/adr/0049-limit-rules.md),
  [0050](../../../../docs/adr/0050-quotas-and-budgets-periods-attribution-currencies.md) and
  [0052](../../../../docs/adr/0052-administration-of-limits.md) as amended here;
  [0051](../../../../docs/adr/0051-period-counters-and-the-ledger.md) (counters stay rebuilt from
  the ledger); [0012](../../../../docs/adr/0012-limits-and-budget-reservation.md);
  [0013](../../../../docs/adr/0013-ledger-and-audit.md) and
  [0032](../../../../docs/adr/0032-audit-log-first-version.md) (two audit actions, a cause on
  `limit.delete`); [0020](../../../../docs/adr/0020-error-contract.md) (no new code; constant
  messages); [0026](../../../../docs/adr/0026-schema-migrations.md),
  [0027](../../../../docs/adr/0027-database-roles-and-session-context.md) and
  [0029](../../../../docs/adr/0029-static-sql-sqlc.md) (migration 0011, two definer functions,
  queries); [0034](../../../../docs/adr/0034-credential-model-and-platform-oversight.md),
  [0035](../../../../docs/adr/0035-catalogue-and-model-access.md) and
  [0036](../../../../docs/adr/0036-prices-costs-and-importer.md) (catalogue deletion, prices are
  global); [0039](../../../../docs/adr/0039-usage-record-admission-and-ledger-writer.md) (one more
  column of the usage record); [0043](../../../../docs/adr/0043-tenant-capability-check-and-usage-list.md)
  (the check uses the tenant's calendar); [0045](../../../../docs/adr/0045-trusted-token-issuers.md)
  (subject mappings and their deletion)
- Decision records changed by this sprint: an **Amendment** section and a history line on 0049,
  0050 and 0052; history lines on 0013, 0026, 0027, 0032, 0039, 0043, 0045, 0051. No new record.
  Text in [plan.md](plan.md), part E.

This document is the design. The implementer follows [plan.md](plan.md); a gap in either document
is raised as a question, not filled silently ([planning/README.md](../../../README.md)).

The scope was closed by the maintainer on 2026-11-18, after the review of ADR 0049 and 0050.
What was decided there is not reopened here; where this design had to choose, the choice is a
decision of §8 and, when the maintainer may want another answer, an open point of §12.

| Decided in the review | Designed in |
|---|---|
| Typed subject columns with composite foreign keys, `ON DELETE RESTRICT`; no cascade | §5.1, D-1 |
| Scope by identifier in both rule tables; names accepted and resolved on write | §5.1, §5.2, D-2, D-3 |
| Rules deleted with their subject, in its transaction, each with `limit.delete` | §5.3, D-5 to D-7 |
| A referenced provider or model cannot be deleted (409) | §5.4, D-8 |
| No orphan job; a schema test | §6, D-9 |
| The usage record says limited, unlimited or unverified | §5.5, D-10 |
| `limit.unlimited_traffic`, once per tenant and day, in both logs | §5.6, D-11 |
| A time zone per tenant; a change never frees a quota | §5.7, D-12, D-13 |
| A budget's currency is checked on write | §5.8, D-14 |
| Documentation of issues #30 and #32; price overrides in another currency | §5.11 |
| Amendments to ADR 0049, 0050, 0052 | plan part E |

## 1. Outcome

1. A limit rule names its subject and its scope **by identifier, with foreign keys**. No statement
   of any role can leave a rule whose service account, key, subject mapping, provider or model
   does not exist, and the database refuses to delete any of them while a rule names it.
2. Whoever removes a subject removes its rules **in the same transaction, audited**: revoking a
   virtual key, deleting a subject mapping, deleting an issuer with its mappings. A provider or a
   model that a rule of any tenant or of the platform names cannot be deleted.
3. The gateway matches a rule to a request by identifier: a model created later with the name of
   a deleted one is another model.
4. Every usage record says how limits treated the request, and the first request of a tenant in
   a day that no rule covers leaves exactly one audit row in the tenant's log and one in the
   platform's.
5. Days and months start in the **time zone of the tenant**; changing the zone never frees a
   quota.
6. A budget cannot be written in a currency that no price of its scope uses.

Counters still live in the memory of one instance (epic 8 shares them).

## 2. Scope

### In scope

- Migration 0011: columns, foreign keys, checks, indexes and the data migration of both rule
  tables; three columns of `tenant`; one column of `usage_record`; two definer functions; four
  triggers; five policies; two partial unique indexes on the audit logs.
- `internal/limits`: rules, limits and usage groups by identifier; the calendar of a tenant; the
  reconciler without a calendar of its own.
- The pipeline: the state of the limits in the usage record and the log; the recorder of
  unlimited traffic; two log fields on `no_price`.
- Administration API: two new operations (the tenant's time zone); changed behaviour of
  thirteen (rules, keys, mappings, issuers, providers, models, tenants, usage, capability);
  CLI.
- Tests at every level, the upgrade from a database of sprint 5.2 included.
- `docs/operations/limits.md`, `tokens.md`, `catalogue.md`, `database.md`, `usage.md`, the
  reference pages; amendments to three decision records.

### Not in scope

| Item | Where |
|---|---|
| Tenant price lists; the price source in the usage record | 5.4 (issue #48); the currency check of §5.8 is built to take them |
| Reports and rollups in the tenant's zone | 6.2 (§11) |
| Deletion of a tenant with its rules and events | 6.3 (§11: the order the foreign keys impose) |
| Reading the audit log through the API (AUD-05) | epic 6 |
| Metrics of limit states, of rules deleted with a subject, of unlimited traffic | 6.1 |
| Delivery of events; an outbox row for `limit.unlimited_traffic` | 6.4 |
| A foreign key from `limit_event` to a rule, a subject or a model | not planned: the event is history and outlives all of them |
| A provider identifier in the usage record | not planned (O-8) |
| Currency conversion | not planned (ADR 0050, option A) |
| Deleting service accounts or virtual keys | not planned: they are disabled and revoked |
| A job that looks for orphan rules | rejected in the review: integrity is structural |

## 3. Requirements claimed

The meaning is quoted from [docs/requirements](../../../../docs/requirements/README.md).

| ID | Meaning | Delivered here | Extent |
|---|---|---|---|
| LIM-02 | "Cumulative quotas of requests and tokens per day or month, in a configurable time zone (UTC by default)" | The zone is configurable per tenant; `limits.time_zone` is the default (§5.7) | completes 5.2 |
| LIM-03 | "Monetary budgets per period" | A budget is refused on write when no price of its scope can be in its currency (§5.8); budget periods follow the tenant's zone | completes 5.2, before tenant price lists |
| LIM-04 | "Limits apply at platform, tenant and virtual-key level, optionally per provider or model. A lower level can only restrict" | Subject and scope are references the database checks; matching is by identifier (§5.1, §5.2) | integrity of what 5.1 and 5.2 delivered |
| AUD-01 | "Pandokos audits authentication failures and every creation, change, disabling, revocation and deletion of tenants, identities, keys, roles, catalogue entries, credentials and limits" | Every rule deleted with its subject, and by the migration, has its `limit.delete` row; the time zone change; unlimited traffic (§5.3, §5.6, §5.10) | the limits part, completed |
| AUD-03 | "The audit record is written atomically with the change it describes" | The rule deletions and the deletion of the subject are one transaction (T-714) | for the new paths |
| TEN-04, TEN-06, TEN-07 | Row-level security on tenant data; isolation "uniformly for … limit counters … and background jobs"; negative tests for every tenant-scoped table and route | The changed tables keep policy, fixture and tests; the two functions that cross the tenant boundary are restricted and tested; both new routes have a cross-tenant test (§6) | full for what this sprint changes |

Not claimed: USE-03 (the record gains a field the requirement does not list), USE-06, AUD-05,
LIM-10, LIM-13.

## 4. Repository layout

New:

```
internal/store/migrations/sql/0011_limit_integrity.sql
internal/limits/zones.go            Zones, TenantCalendar
internal/resolver/unlimited.go      UnlimitedAuditor
internal/admin/limit_subjects.go    deletion of the rules of a subject, with audit
internal/admin/budget_currency.go   the currencies of a scope's prices
internal/admin/time_zone.go         the two operations and the change rule
```

Changed: `internal/limits/{limits,rules,load,usage,reconcile,cache,events,periods}.go`,
`internal/gateway/{ports,limits,handler,record,estimate}.go`, `internal/resolver/{limits,
catalogue}.go`, `internal/usage` (one field), `internal/ledger/rows.go`, `internal/admin/{limits,
limit_fields,platform_limits,virtual_keys,subject_mappings,token_issuers,platform_issuers,
providers,models,convert_catalog,tenants,capability,usage,problem,admin}.go`, `internal/authz`
(two descriptions), `internal/cli/{limits,commands,capability}.go`, `cmd/pandokos/serve.go`,
`api/openapi/admin.v1.yaml`, `internal/store/queries/sql/{limits,usage,tenants}.sql`,
`test/isolation`, `test/dbtest`, `test/limitstest`, generated code, `migrations.sum`,
documentation.

No new Go module, no new configuration key, no new permission, no new error code.

## 5. Architecture

### 5.1 The rule tables (migration 0011; plan appendix A1)

| Table | Before (0010) | After |
|---|---|---|
| `limit_rule` | `subject_kind`, `subject_id text`, `provider text`, `model text` | `subject_kind`, `service_account_id`, `virtual_key_id`, `subject_mapping_id`, `provider_id`, `model_id` (all `uuid`, nullable) |
| `platform_limit_rule` | `tenant_id`, `provider text`, `model text` | `tenant_id`, `provider_id`, `model_id` |

| Constraint | Meaning |
|---|---|
| `limit_rule_service_account_fkey`, `…_virtual_key_fkey`, `…_subject_mapping_fkey` | `(tenant_id, x_id)` references `(tenant_id, id)` of the subject's table, `ON DELETE RESTRICT`: a subject of another tenant cannot be named, a named subject cannot be deleted |
| `…_provider_fkey` (both tables) | `provider_id` references `provider (id)`, `ON DELETE RESTRICT` |
| `…_model_fkey` (both tables) | `(provider_id, model_id)` references `model (provider_id, id)`, `ON DELETE RESTRICT`: the model is one of the rule's provider. `model` gains the unique key `(provider_id, id)` for it |
| `limit_rule_subject_by_kind` | `tenant`: no subject column. `service_account`: `service_account_id` only. `key`: exactly one of `virtual_key_id` and `subject_mapping_id` |
| `…_scope_valid` (both) | a model needs its provider |
| `limit_rule_key`, `platform_limit_rule_key` | unique, nulls not distinct, over tenant, the subject columns, `provider_id`, `model_id` and `metric`: one rule per subject, scope and metric, as before |
| triggers `limit_rule_key_guard`, `virtual_key_revoke_guard` | a rule is not created for a revoked key; a transaction that revokes a key and leaves a rule of it fails at commit (§5.3) |

`subject_kind` keeps its three values: the API, the limiter (`SubjectKey` with the key ID or the
mapping ID, which is what a principal carries) and `limit_event` do not change (D-1). Subject,
scope, metric and currency of a rule still never change (column grants). `limit_event` keeps its
text columns and has no foreign key: it is the history of what happened, with the names of the
time.

The integrity that follows, for every role, the owner included:

- a rule's subject and scope exist, in the rule's tenant, for as long as the rule exists;
- deleting a subject mapping, a trusted issuer or a platform issuer (which cascade to mappings,
  migration 0008), a provider or a model fails while a rule names it — referential checks ignore
  row-level security, so the session that deletes need not see the rule;
- a revoked key has no rule.

### 5.2 Names at the API, identifiers below it (D-2, D-3)

**On write.** `createLimit` and `createPlatformLimit` take what they took: `subject_kind`,
`subject_id` (a service account ID; a key ID; a subject mapping ID), `provider` and `model` by
name. The handler resolves them in the transaction of the creation (`ResolveLimitSubject`,
`ResolveLimitScope`) and inserts identifiers. The answers are those of 5.2 — 400 on `subject_id`,
`provider` or `model`, one message for "elsewhere" and "nowhere" — with one addition: **a revoked
key is not a subject** (same 400 on `subject_id`). The foreign keys are the backstop: a violation
that passes validation (a subject deleted between the check and the insert cannot happen under
the lock of §5.3, but the constraint does not depend on that) is mapped to the same 400.

**On read.** A rule resource keeps `subject_id`, `provider` and `model` and gains `provider_id`
and `model_id` (empty when the rule is not scoped). Names are read through the foreign keys at
the time of the call. The list filters `subject_id`, `provider` and `model` keep their meaning.

**In the gateway.**

```go
type Rule struct {
    ID                    string
    SubjectKind           SubjectKind
    SubjectID             string // "" | service account ID | key ID of the virtual key | subject mapping ID
    ProviderID, ModelID   string // scope; "" means every
    Provider, Model       string // names, for events, headers and logs only
    Metric                Metric
    Value                 int64
    Currency              string
    Enforce               bool
    Thresholds            []int
}
type Query struct {
    ServiceAccountID, KeyID string
    ProviderID, ModelID     string    // identifiers of the target
    Start                   time.Time
}
```

- `Limit` gains `ProviderID` and `ModelID`; `Provider` and `Model` stay as names. A counter is
  identified by (tenant, subject kind, subject ID, **provider ID, model ID**, metric, currency)
  and the period start. `RuleSet.For` matches the scopes *all*, *provider ID*, *provider ID and
  model ID*; the order of resolution is that of 5.2 §5.3.
- `gateway.Target` gains `ProviderID` (the catalogue snapshot has it); `LimitEstimate` carries
  `ProviderID` and `ModelID` besides the names.
- The loader reads a rule with the key ID of its virtual key (a join), so that a principal's
  `KeyID` matches as before.
- `UsageGroup` carries `ProviderID` and `ModelID`; `AggregateUsageForLimits` groups by
  `usage_record.model_id` and by the identifier of the provider that has the record's provider
  name **now** (the ledger stores the provider by name). `limits.Sum` compares identifiers.

**What "a later model with the same name" meets.** A model that a rule names cannot be deleted
(§5.4). Once its rules are gone it can be; a model created afterwards with that name has another
ID, so no rule cached by a gateway for up to `cache.limits_ttl` matches it, and a rule created
for it later counts the ledger rows of the new model only (`model_id`). For a **provider**
deleted and created again with the same name inside one period, the rows of the old provider
count for a rule on the new one: the ledger has no provider ID. That errs towards restricting,
needs a provider to lose every model, credential and rule first, and is documented (O-8).

### 5.3 Deleting a subject deletes its rules (D-5 to D-7)

Facts of the code: a virtual key is revoked, never deleted; a service account is disabled and
enabled, never deleted (migration 0002 revokes the privilege); a subject mapping is deleted by
`deleteSubjectMapping` and by the cascade of `deleteTokenIssuer` and `deletePlatformIssuer`.
Removing a tenant from the list of a platform issuer deletes no mapping.

| Operation | Session | Rules deleted | `cause` | `cause_id` |
|---|---|---|---|---|
| `revokeVirtualKey` | tenant | those of the key | `key_revoked` | the key ID |
| `deleteSubjectMapping` | tenant | those of the mapping | `subject_mapping_deleted` | the mapping ID |
| `deleteTokenIssuer` | tenant | those of every mapping of the issuer | `token_issuer_deleted` | the issuer ID |
| `deletePlatformIssuer` | platform | those of every mapping of the issuer, in every tenant | `platform_issuer_deleted` | the issuer ID |
| `disableServiceAccount` | tenant | none: the rules wait for the account to be enabled | — | — |

For each of the four:

1. The transaction takes the advisory lock of the tenant's rules (`LockLimitRules`, the lock
   `createLimit` holds from before its existence check to its commit), so no rule is created for
   the subject while it goes.
2. It deletes the rules (`DeleteLimitRulesOfSubject`), then the subject (or revokes the key).
3. For each rule deleted it writes one audit row in the tenant's log: action `limit.delete`,
   resource `limit_rule` and the rule's ID, **the actor of the operation** (the principal that
   revokes or deletes), the request ID, the `reason` of the request when it has one, and the
   detail of an ordinary `limit.delete` (every field of the rule, §5.9) plus `cause` and
   `cause_id`.
4. The audit row of the operation itself (`virtual_key.revoke`, `subject_mapping.delete`,
   `token_issuer.delete`, `platform_issuer.delete`) gains `limit_rules_deleted`: the number.

Everything is one transaction: a failure after the rules are deleted leaves the subject and its
rules. A path that forgets step 2 is refused by the database (`restrict_violation` for a mapping,
the deferred trigger for a key) and answers 500 with nothing changed — a defect that a test
finds, never an orphan. The responses of the four operations do not change. Revoking a key that
is already revoked still changes and audits nothing.

**The platform issuer.** The platform session sees neither the tenants' rules nor their audit
logs. `deletePlatformIssuer` calls the definer function
`pandokos.delete_platform_issuer_limit_rules(issuer, tenants, actor ID, actor name, key ID,
request ID)` before it deletes the registration. The function follows
`platform_issuer_mapping_count` of migration 0008: it refuses a session outside the platform
scope, goes tenant by tenant as that tenant's session, takes the tenant's rule lock, deletes the
rules of that issuer's mappings and inserts their audit rows with actor type `platform` and the
actor it was given, restores the caller's tenant setting, and returns one number. It can delete
nothing but rules of mappings of the issuer it is given, and it is executable by the admin role
only. Its audit detail is built in SQL with the members of the Go detail (T-714 compares them).

The gateway learns of the deletions through the notifications of the two rule tables, as for any
deleted rule.

### 5.4 Deleting a provider or a model that a rule names (D-8)

`deleteProvider` and `deleteModel` are refused with **409 `invalid_state`** while a rule of any
tenant or of the platform names the provider or the model, as `deleteModel` already is while a
price version names the model. The check is the foreign key: the database evaluates it without
row-level security, so the platform session is refused for a rule it cannot see, and learns one
thing — that limit rules reference the entry. No function counts or lists rules across tenants,
and none is needed (ADR 0027: the smallest crossing is no crossing).

The problem carries the constant title and detail of `invalid_state` and one entry in `errors`,
chosen by the name of the violated constraint:

| Constraint | `errors[0]` |
|---|---|
| `limit_rule_provider_fkey`, `platform_limit_rule_provider_fkey` | field `limit_rules`: `limit rules reference this provider; disable the provider instead` |
| `limit_rule_model_fkey`, `platform_limit_rule_model_fkey` | field `limit_rules`: `limit rules reference this model; retire the model or remove its grants instead` |

Nothing is audited (the transaction rolls back). The catalogue guide says what to do instead:
disable the provider, retire the model, or remove its grants; platform rules can be listed and
deleted by the platform administrator, tenant rules belong to the tenants.

### 5.5 How limits treated a request (D-10)

`usage_record.limit_state` (text, nullable; a check constraint):

| Value | When |
|---|---|
| `not_evaluated` | The request ended before step 7b (refused for its body, its model, a policy), or the gateway runs without a limiter |
| `unlimited` | Step 7b found no rule that matches the request: none of the platform, the tenant, the service account or the key, for its provider and model |
| `limited` | At least one rule matched and every matching limit was evaluated: the request was reserved against them, or refused by one (429, or 409 `no_price`). A budget that `limits.on_unknown_price: allow` leaves out of the count was evaluated |
| `unverified` | At least one matching limit could not be evaluated: the rules could not be read (503), or counters could not be loaded or reserved — refused (503) or, with `limits.on_store_failure: allow`, admitted without them |
| NULL | The row was written before this sprint |

The value is decided where the decision is taken: `gateway.Limits.Reserve` returns it with the
reservation, the refusal or the error (plan C3); the handler puts it in the usage record and in
the request log line as `limit_state`. The log fields `limits_unverified` and `budget_unpriced`
of 5.2 stay. `listUsageRecords` shows `limit_state` (empty for older rows) and filters on it.

### 5.6 Unlimited traffic in the audit log (D-11)

The first request of a tenant, in a day of that tenant (§5.7), whose state is `unlimited` writes
**one row in the tenant's audit log and one in the platform's**:

| Column | Value |
|---|---|
| `action`, `resource_type`, `resource_id` | `limit.unlimited_traffic`, `tenant`, the tenant ID |
| `actor_type`, `actor_name` | `platform`, `gateway` (no caller is the actor: the row is about the tenant) |
| `outcome`, `request_id` | `success`; the request that was first |
| `detail` | `day` (`2026-11-18`, the civil date in the tenant's zone), `time_zone`, `provider`, `model` (of that request) |

- **Who writes.** The gateway role has no privilege on either log. It executes
  `pandokos.record_unlimited_traffic(day, time zone, provider, model, request ID)`, a definer
  function in the manner of `record_auth_failure`: one action, fixed columns, the tenant of the
  session, arguments checked (the day within two days of the database's date). The platform row
  needs a policy for the function's owner, limited to this action.
- **Exactly once.** Two partial unique indexes — `audit_event (tenant_id, detail ->> 'day')` and
  `platform_audit_event (resource_id, detail ->> 'day')`, both `WHERE action =
  'limit.unlimited_traffic'` — and `ON CONFLICT DO NOTHING` in the function. Any number of
  instances, restarts and repetitions leave one row per tenant and day in each log, and both
  rows are written by one call or neither.
- **Off the request path.** `resolver.UnlimitedAuditor` keeps, per tenant, the last day it
  recorded. Step 7b costs one map lookup; when the day is new the request ID and the day go to a
  bounded queue (256) without blocking, and one goroutine calls the function on the gateway pool
  in the tenant's session (2 s). After a restart the first unlimited request of each tenant
  costs one call that inserts nothing. A failed or dropped write forgets the day, so a later
  request tries again, at most once per tenant every 10 s. A day's row is missing only if the
  database could not be written from the first unlimited request to the end of the day.
- The row says "this tenant sent traffic that nothing limited", not how much: the usage records
  say that (`limit_state`).

### 5.7 The time zone of a tenant (D-12, D-13)

**Storage.** `tenant.time_zone` (an IANA name; empty: the default of the installation,
`limits.time_zone`), `time_zone_previous` and `time_zone_effective_from`. While
`effective_from` lies in the future a change is *pending*: the zone in force is
`time_zone_previous` until that instant and `time_zone` from it.

**The calendar of a tenant.**

```go
// Zones shares one Calendar per zone name; "" is the default zone of the installation.
type Zones struct{ /* default, calendars by name */ }
func NewZones(def *time.Location) (*Zones, error)
func (z *Zones) Calendar(name string) (*Calendar, error) // unknown zone, or offsets with seconds: error

// TenantCalendar is Previous before From and Current from it. Previous is nil when the tenant
// never changed its zone.
type TenantCalendar struct {
    Current, Previous *Calendar
    From              time.Time
}
func (c TenantCalendar) At(t time.Time) *Calendar          // the calendar in force at t
func (c TenantCalendar) Of(k PeriodKind, t time.Time) Period // At(t).Of(k, t)
func (c TenantCalendar) Day(t time.Time) string            // civil date at t, "2006-01-02"
```

The rule cache loads the three columns with the rules, in the same tenant transaction, and the
`RuleSet` carries the `TenantCalendar`; `RuleSet.For(q)` takes no calendar any more. A change of
the columns notifies `limits:<tenant>`. A zone the binary cannot load is a load error: the
tenant's requests are refused as unavailable, like rules that cannot be read.

**The definition.** At every instant *t* one zone *Z(t)* is in force for a tenant. The period of
kind *k* of a request that starts at *t* is the **regular civil period** (5.2 §5.2) of *Z(t)*
that contains *t*, with its whole range `[Start, End)`. A counter of a period counts every
counted request whose start lies in that range — whatever zone was in force when it started.
Within one zone periods tile the time line; around a change the last periods of the old zone and
the first of the new one overlap.

**The change rule.** A change made at *t₀* takes effect at **E = the start of the next month in
the zone in force at *t₀***, for days and months alike. Until *E* nothing changes. A second
change before *E* replaces the pending zone and keeps *E*; setting the zone in force again
withdraws the pending change. A zone given when the tenant is created is in force at once.

**Why no quota is freed.**

1. *Nothing in progress ends early.* For *t* < *E* the zone in force, and so every period, is
   what it would have been without the change. The month in progress at *t₀* ends exactly at
   *E*; the day in progress ends where the old zone ends it.
2. *Every period is a whole one.* From *E* on, a request is tested against the complete usage of
   a regular day and a regular month of the new zone, including what was used in their ranges
   before *E* (the counter is loaded from the ledger over its range, 5.2 §5.6). There is no
   shortened first period that starts empty at *E*.
3. *So a change admits nothing that a boundary would not have admitted.* Before *E* the old
   zone's test applies unchanged. After *E*, in every regular period of the new zone at most the
   limit (and the bound of 5.2 §5.5) is counted while that period is in force. What a change can
   do is what moving a boundary means: later boundaries fall at other instants. It can happen
   once per month of the zone in force.
4. *The database holds the shape.* The trigger `tenant_time_zone_guard` refuses a change of the
   zone in force without a pending instant, an instant that is not in the future or more than 32
   days away, and a replacement that moves the instant (§6). The instant itself is computed by
   the administration API with the calendar the gateway uses (T-717).

Consequences stated in the guide: a change waits up to a month; the day in which the new zone
takes over can refuse at once (its range began before *E*, and the usage of those hours counts);
a `Retry-After` given just before *E* points at *E*, where the new zone's period is tested
afresh; threshold events of both zones' periods can appear for the hours they share.

**Where the calendar is used.**

| Place | Change |
|---|---|
| Counters (`RuleSet.For`) | the period of a limit comes from the tenant's calendar |
| Ledger reader | unchanged: it takes a range |
| Reconciler | no calendar: fold pieces are cut at every start and end of a period the store holds for the tenant (`Store.Periods`), which is what "every counter whose period contains a piece" needs with two zones; load and rebase take the period of the limit |
| Events | unchanged: period start and end come from the limit |
| Capability check | loads the tenant's rules and calendar (`limits.LoadRules`), queries by identifier |
| `Retry-After`, `X-Pandokos-Limit-Reset` | the end of the limit's period: the tenant's |
| `limit.unlimited_traffic` | `TenantCalendar.Day` |

A counter of the new zone whose period started at or after the tenant's cut is created empty and
receives the usage of its first minutes when the cut passes them (5.2 §5.6, "a rule created in
the middle of a period"); one whose period started before the cut is loaded. With the defaults
that is at most 17 m 30 s of the old zone's usage seen late, once, and only when the two zones'
boundaries are less than the horizon apart.

**Changing `limits.time_zone`** still takes effect at restart, for the tenants that have no zone
of their own, as in 5.2. It is an operator's decision about every such tenant; periods of the
new default count the whole usage of their range.

**Who sets it.**

| Operation | Method and path | Permission | Audit |
|---|---|---|---|
| `createTenant` (`time_zone`) | `POST /v1/tenants` | `tenant:write` | `tenant.create` (platform log), detail `time_zone` |
| `updateTenant` (`time_zone`) | `PATCH /v1/tenants/{tenant}` | `tenant:write` | `tenant.update` (platform log), detail `time_zone` from/to and `time_zone_effective_from` |
| `getTenantTimeZone` | `GET /v1/tenants/{tenant}/time-zone` | `limit:read` | — |
| `setTenantTimeZone` | `PUT /v1/tenants/{tenant}/time-zone` | `limit:write` | `tenant.time_zone_update` (tenant log), resource `tenant`, detail `from`, `to`, `in_force`, `effective_from` |

The tenant's own route is under the permissions of limits (D-13): the zone decides when quotas
and budgets reset, and a principal that may write rules may already remove them. The tenant
session updates its own row through a new policy; the trigger leaves it the three zone columns
and nothing else.

The resource `TenantTimeZone`: `time_zone` (as set; empty for the default), `in_force` (the name
of the zone in force now, the default resolved), `effective_from` (present while a change is
pending). `Tenant` gains `time_zone`, `time_zone_in_force` and `time_zone_effective_from`.
`PUT` takes `{"time_zone": "Europe/Rome"}`; an empty string is the default. 400 on `time_zone`
for a name the embedded zone database does not know or whose offsets are not whole minutes.
Setting the value already set changes and audits nothing.

### 5.8 The currency of a budget is checked on write (D-14)

Creating a rule with a cost metric, and updating one, is refused when **no catalogue price that
can be in force for its scope is in its currency**:

- *The models of the scope*: the model; the models of the provider; every model. For a tenant
  rule, those the tenant can be given (available to all, or granted to it); for a platform rule,
  all of them.
- *A price that can be in force*: for each model, the version in force now and every version
  that starts later (a version that was replaced cannot come back).
- The answer is **400 `invalid_request`** on `currency`: `must be a currency the prices of this
  scope use: EUR, USD` (sorted), or `no model of this scope has a price` when the set is empty.
  It applies to enforced and unenforced budgets and, on `PATCH`, to the rule's own currency.

One function, `budgetCurrencies(ctx, q, scope, tenantSession)`, returns the set from one query
(`ListPriceCurrenciesForScope`); both handlers call it inside their transaction. Sprint 5.4 adds
the currencies of the tenant's price list to the set for tenant rules, in that function and
nowhere else.

The check is about the moment of the write. Prices change afterwards; a request that a budget
cannot measure still meets `limits.on_unknown_price` (5.2 §5.5). The 409 `no_price` keeps its
constant message; its log line gains `limit_currency` (the budget's) and `price_currency` (the
currency of the price in force, absent when there is none), and `LimitRefusal` gains
`PriceCurrency`.

### 5.9 Audit actions and details

| Action | Log | Change |
|---|---|---|
| `limit.create`, `limit.delete`, `platform_limit.create`, `platform_limit.delete` | as 5.2 | detail gains `provider_id`, `model_id` when scoped |
| `limit.delete` written with a subject | tenant | detail gains `cause` and `cause_id` (§5.3); the actor is the actor of that operation |
| `limit.delete`, `platform_limit.delete` written by the migration | tenant, platform | actor type `platform`, actor name `migration 0011`; `cause` `migration_subject_missing`, `migration_key_revoked` or `migration_scope_missing` (§5.10) |
| `virtual_key.revoke`, `subject_mapping.delete`, `token_issuer.delete`, `platform_issuer.delete` | as before | detail gains `limit_rules_deleted` |
| `limit.unlimited_traffic` | tenant and platform | new (§5.6) |
| `tenant.time_zone_update` | tenant | new (§5.7) |
| `tenant.create`, `tenant.update` | platform | detail gains the time zone |

### 5.10 The migration and the upgrade (D-4)

Migration 0011 runs in one transaction as the owner, like every migration (ADR 0026).

**Order.** (1) the tenant columns, their trigger and policy; (2) the usage column; (3) the new
columns of the rule tables; (4) the data migration; (5) the old columns dropped, the foreign
keys, checks, unique keys, indexes and grants; (6) the key guards; (7) the two functions and
their policies and indexes.

**Data migration.** Names become identifiers by what they designate **now** — which is exactly
what the rule matched until now:

| Rule of 0010 | Becomes |
|---|---|
| `service_account`, `subject_id` an account of the tenant | `service_account_id` |
| `key`, `subject_id` the key ID of a key of the tenant that is not revoked | `virtual_key_id` |
| `key`, `subject_id` the ID of a subject mapping of the tenant | `subject_mapping_id` |
| `provider`, `model` names of the catalogue | `provider_id`, `model_id` |
| a subject that is not there, or a key that is revoked | deleted; one `limit.delete` row in the tenant's log |
| a provider or model name that designates nothing | deleted; one `limit.delete` (tenant rule) or `platform_limit.delete` (ceiling) row |

A rule that is deleted matched no request (its subject cannot authenticate, its scope names
nothing), so the migration changes nothing that is enforced; its audit row keeps every field,
the `cause` and the old names. No rule is lost silently, and none makes the migration fail.

**Forced row-level security.** The owner sees no tenant row under the forced policies. The
migration lifts `FORCE ROW LEVEL SECURITY` on the nine tables it reads or writes (the two rule
tables, `service_account`, `virtual_key`, `subject_mapping`, `provider`, `model`, the two audit
logs) and puts it back before it ends; the row triggers of the two rule tables are off
meanwhile, so `updated_at` keeps its value. All in the one transaction: a failure leaves the
schema of 0010. The schema lint (T-140) checks the final state.

**Locks and duration.** `ACCESS EXCLUSIVE` on those nine tables, on `tenant` and on
`usage_record` for the length of the transaction. The rule tables are small (at most 5,000 rows
per tenant) and are rewritten by nothing but the updates; `usage_record` gains a nullable column
without default and a check constraint that is `NOT VALID` (new rows are checked; existing rows
all hold NULL), so no partition is rewritten or scanned; the two partial indexes scan the audit
logs once and index nothing. Building them is the only step that grows with the installation.

**Upgrade.** Stop, migrate, start (ADR 0026). The migration is **not** compatible with the
binary of 5.2: that binary turns not ready when the ledger of migrations no longer matches
(OPS-09), and a request it still takes is refused with 503 because its rule query names columns
that are gone — it fails closed. A new binary against the schema of 0010 does not start. v0.2 is
not released; installations that run `main` with rules are the case this covers (T-692).

### 5.11 Configuration, CLI, documentation

**Configuration.** No new key. `limits.time_zone` is described as the zone of the tenants that
have none of their own.

**CLI.**

```
pandokos tenant create … [--time-zone ZONE]
pandokos tenant update TENANT [--time-zone ZONE]          ("" for the default)
pandokos limit time-zone get --tenant T
pandokos limit time-zone set --tenant T ZONE | --default
pandokos usage list … [--limit-state limited|unlimited|unverified|not_evaluated]
```

`limit list|get` and `platform-limit list|get` show the scope by name, as before; `-o json` has
the identifiers. `limit create` is unchanged.

**Documentation** (plan part E):

- `limits.md`: a new section **"Two quotas"** — the quota decided in Pandokos (rules, counted
  here) and the quota a provider grants to a credential (the provider's rate and spend tiers,
  which Pandokos cannot see and meets only as the provider's own 429) — with the recipe of issue
  #32: protect a provider tier with a rule of the tenant scoped to the provider, below the tier;
  keep a batch key from starving an interactive one with a rule of subject `key` scoped to the
  provider on the batch key, so that the tenant's default credential of that provider (one per
  tenant and provider; a key may be pinned to another) keeps room. The sections on levels,
  scopes, periods (the time zone per tenant and the change rule), budgets (the currency check;
  prices are set by the platform for every tenant; **when a price override in another currency
  makes sense** — a provider or contract billed in it — and why not as a shortcut for one
  tenant's budget: it changes the price for every tenant and stops imports for the model),
  "what is strict", troubleshooting (`limit_state`, the unlimited-traffic row).
- `tokens.md`: the table of section 1 says that a subject mapping can be the subject of a limit
  rule and that the limits of the service account apply to tokens (issue #30); deleting a
  mapping or an issuer deletes those rules.
- `keys.md`: revoking a key deletes its rules. `catalogue.md`: the refusal of §5.4 and what to
  do instead. `usage.md`: `limit_state`. `database.md`: section 16, migration 0011 and the
  upgrade. `docs/reference/admin-api.md`, `permissions.md`, the data model and the security
  model (two definer functions, the tenant's self-update policy).

## 6. Isolation harness

- **`limit_rule`** (tenant-scoped): policy unchanged; grants replaced (insert of the typed
  columns); fixture updated; T-694 for constraints, foreign keys across tenants and the two key
  guards. **`platform_limit_rule`**: grants replaced; T-694.
- **`tenant`** (global, restricted): the policy `tenant_self_update` for the admin role and the
  trigger that limits a tenant session to the zone columns; T-697 covers every role and session
  and every refusal of the trigger. The gateway and worker roles still only read.
- **`usage_record`**: the grant of the gateway gains `limit_state`; T-698.
- **`audit_event`, `platform_audit_event`**: no grant changes. Two policies for the owner on the
  platform log, limited to `limit.unlimited_traffic`; T-696 shows the gateway role still has no
  privilege on either log and that the owner's policy admits no other action.
- **`provider`, `model`**: a read policy for the owner (the definer function reads names for
  the audit detail); no grant changes.
- **Definer functions** (`Spec.SecurityDefiner` gains two):
  `delete_platform_issuer_limit_rules` — admin role, platform scope only, deletes only rules of
  mappings of the given issuer in the named tenants, restores the tenant setting (T-695);
  `record_unlimited_traffic` — gateway role, tenant session only, one row per log and day
  (T-696). Neither returns a row of a tenant.
- **Schema test** (T-693, the replacement of an orphan job): every `uuid` column of the two rule
  tables other than `id` is the referencing column of a foreign key; the subject and scope keys
  are `ON DELETE RESTRICT`; the subject keys are composite with `tenant_id`.
- Routes: `getTenantTimeZone` and `setTenantTimeZone` declare a permission and have a
  cross-tenant negative test (T-718, T-719). No statement names the tenant in a `WHERE` clause
  except the two that address the `tenant` row by ID under its policies.
- The ledger reader still runs as the worker role in the tenant's session; it now also reads
  `provider`, which every runtime role may.

## 7. Test infrastructure and CI

- `dbtest.MigrateTo(tb, n)` applies the first *n* embedded migrations; T-692 seeds a database at
  0010 with one rule of every case of §5.10 and then applies 0011 with the real migrator.
- `dbtest.SeedLimitRule` and `SeedPlatformLimit` resolve subject and scope to identifiers and
  fail when one does not exist: tests of 5.1 and 5.2 that used invented subject IDs or scope
  names create the subject, the provider or the model first (plan B1 lists them).
- `limitstest.Real` gains a tenant calendar per tenant and `SetZone(tenant, zone, at)`.
- `UnlimitedAuditor` and `TenantCalendar` take a clock; no test sleeps across a boundary.
- CI: no new job. `make check` is unchanged in what it runs.

## 8. Decisions

| # | Decision | Reason | Recorded in |
|---|---|---|---|
| D-1 | **Three typed subject columns with composite foreign keys; `subject_kind` keeps `tenant`, `service_account`, `key`**, and `key` sets exactly one of `virtual_key_id` and `subject_mapping_id` | The review's decision; the kind `key` is what a principal carries and what the API, the limiter and `limit_event` already use, so nothing above the table changes | ADR 0049 amendment |
| D-2 | **Scope is `provider_id` and `model_id` in both tables; the model key is composite with the provider. The API takes and shows names, and shows the identifiers** | A rule's model is a model of its provider by construction; names stay the interface | ADR 0049 amendment |
| D-3 | **The gateway and the counters match by identifier; the ledger aggregate groups by `model_id` and by the provider that has the record's name now** | The ledger has the model's ID and the provider's name; a second model of the same name is told apart exactly, a second provider of the same name within a period is not, and counts in the restrictive direction | ADR 0049 amendment, history of 0051 |
| D-4 | **The migration resolves names to what they designate now, deletes the rules that designate nothing or a revoked key, and writes one audit row for each**; it lifts forced row-level security on nine tables inside its transaction; it is not compatible with the previous binary | What a name designates now is what was enforced; an idle rule is not worth a failed upgrade but is worth a record; ADR 0026 has no compatibility window | ADR 0049 amendment, history of 0026 |
| D-5 | **Revoking a key, deleting a mapping and deleting an issuer delete the rules of those subjects first, under the tenant's rule lock, each with `limit.delete` naming the actor, `cause` and `cause_id`; a disabled service account keeps its rules** | The review's decision; the lock closes the race with `createLimit`; the human reason of a request stays in `reason`, the machine cause in the detail | ADR 0052 amendment, history of 0032, 0045 |
| D-6 | **For a platform issuer the rules are deleted by a definer function**, tenant by tenant, with the platform principal as actor | The only way one transaction can write the tenants' logs from the platform scope; the pattern of 0008 | history of 0027 |
| D-7 | **A revoked key has no rule, held by two triggers** (no insert for a revoked key; a deferred check when a key is revoked) | Keys are never deleted, so `RESTRICT` cannot hold the rule of the review for them; the deferred check lets the handler revoke and delete in either order | ADR 0049 amendment |
| D-8 | **Deleting a referenced provider or model answers 409 `invalid_state` from the foreign key itself, with one `errors` entry; no function counts rules across tenants** | Referential checks ignore row-level security, so the refusal needs no crossing; it reveals that rules exist, which the refusal reveals anyway | ADR 0049 amendment |
| D-9 | **No periodic job; one schema test over the foreign keys of the rule tables** | The review's decision | ADR 0049 amendment |
| D-10 | **`usage_record.limit_state`: `limited`, `unlimited`, `unverified`, and `not_evaluated` for a request that ended before the limits; NULL for older rows; a nullable column and a `NOT VALID` check** | A record of a request that never reached the limits should not claim it was unlimited; the ledger is append-only and partitioned, so nothing is rewritten or scanned | history of 0039 |
| D-11 | **`limit.unlimited_traffic`: a definer function with `ON CONFLICT DO NOTHING` on two partial unique indexes of the audit logs; recorded off the request path from a per-tenant memory of the last day** | Exactly once by structure, for any number of instances; no new table; the request pays a map lookup | ADR 0052 amendment, history of 0013, 0027 |
| D-12 | **A tenant's zone is three columns of `tenant`; a change takes effect at the start of the next month of the zone in force, for days and months alike; periods are always whole civil periods of the zone in force, counted over their whole range** | One instant keeps one calendar per tenant at any time; whole periods make "never frees a quota" a property of the definition (§5.7) rather than of a transition rule | ADR 0050 amendment |
| D-13 | **The tenant sets its zone with `limit:write` on `PUT /v1/tenants/{tenant}/time-zone`; the platform with `tenant:write`** | No new permission for one setting that only moves quota boundaries | ADR 0050 amendment |
| D-14 | **The currency check is one function over the currencies of the prices that can be in force for the scope; it answers 400 on `currency` with the currencies in use** | The same class of answer as a subject or scope that does not exist; one place for 5.4 to extend | ADR 0050 amendment |
| D-15 | **`no_price` keeps its constant message; the log names the two currencies** | ADR 0020: the contract does not change | sprint decision |
| D-16 | **No new dependency, configuration key, permission or error code** | ADR 0018, 0019, 0020, 0031 | sprint decision |

## 9. Tests

Levels as in 5.2: **U** unit; **D** test database; **I** in-process gateway with the fake
provider; **E** built binary; **R** repository test; **P** property test. IDs continue from
T-691 and T-R44.

### New tests

| ID | Test | Level | Proves | Requirement |
|---|---|---|---|---|
| T-692 | `migrations.TestLimitIntegrityUpgrade` | D | A database at 0010 with a rule of every row of §5.10, in two tenants, and ceilings: after 0011 each surviving rule has its identifiers and its `updated_at`; each deleted rule has exactly one audit row in the right log with `cause`, actor `migration 0011` and every field; forced row-level security and the triggers are back on the nine tables; a database without rules migrates; the migration applied twice is refused by the ledger | **LIM-04**, **AUD-01** |
| T-693 | `isolation.TestRuleForeignKeys` | D | The schema test of §6: every reference column of both rule tables has its foreign key, with the delete rule and the composite columns stated there | **LIM-04** |
| T-694 | `isolation.TestLimitRuleIntegrity` | D | As each role: every case of `subject_by_kind`; a subject of another tenant and a model of another provider are refused; the unique key with nulls; deleting a mapping, a trusted issuer, a platform issuer, a provider and a model is refused while a rule names it, also from a session that cannot see the rule; a rule for a revoked key is refused; revoking a key with a rule fails at commit and succeeds when the rule is deleted in the transaction; the gateway reads rules with their joins; the worker reads none | **LIM-04**, **TEN-04**, TEN-07 |
| T-695 | `isolation.TestDeletePlatformIssuerLimitRules` | D | Only the admin role, only in the platform scope; rules of that issuer's mappings in the named tenants go, others stay (and then the issuer cannot be deleted); one audit row per rule in its tenant's log with the given actor; the tenant setting is restored; malformed actor arguments are refused; the result is a number | **TEN-04**, AUD-01 |
| T-696 | `isolation.TestRecordUnlimitedTraffic` | D | Only the gateway role, only in a tenant session; 16 concurrent calls and a repeated call leave one row per tenant and day in each log; two tenants do not collide; a day outside the window and malformed arguments are refused; the gateway still cannot insert into or read either log; the owner's policy admits no other action | **AUD-01**, **TEN-04** |
| T-697 | `isolation.TestTenantTimeZonePolicies` | D | The admin role in a tenant session changes the zone columns of its own row only (name, display name and state are refused; another tenant's row is not reached); every refusal of the trigger of §5.7 (4); the platform scope changes any tenant; gateway and worker cannot; the notification is `limits:<tenant>`; a tenant is created with a zone and without a pending one | **TEN-04**, LIM-02 |
| T-698 | `isolation.TestUsageLimitState` | D | The gateway inserts each of the four values and NULL; another value is refused in an existing and in a newly created partition; the row is still append-only | TEN-04 |
| T-699 | `limits.TestTenantCalendar` | U, P | `At`, `Of`, `Day`; for random pairs of zones and instants of change: for `t < From` the periods are those of the old zone, for `t ≥ From` regular periods of the new one, never a truncated one; the instant of a change is the end of the old zone's month that contains it; `Zones` shares calendars, resolves the default, refuses an unknown zone and offsets with seconds | **LIM-02** |
| T-700 | `limits.TestZoneChangeNeverFrees` | U, P | Random schedules of requests and zone changes against the reference of 5.2 (store, reconciler, fake ledger): at every admission, the usage of the whole civil day and month of the zone in force plus the estimate is within the limit (usage before the change included); no period in progress at a change ends before its old end; the bound of 5.2 §5.5 holds per period | **LIM-02**, LIM-07 |
| T-701 | `limits.TestResolveByIdentifier` | U | A rule on a model ID does not match a target with the same name and another ID; provider and model scopes by ID; the key subject by key ID and by mapping ID; names never decide; at most one limit per counter | **LIM-04** |
| T-702 | `limits.TestUsageSumByIdentifier` | U | `Sum` by provider ID and model ID for each subject kind; a group without model ID counts for the wider scopes only | LIM-04 |
| T-703 | `limits.TestLoadRulesTyped` | D | The loader maps the typed columns (the key ID through the join), identifiers and names, both tables; the tenant calendar for no zone, a zone, a pending change and one that took effect; an unknown zone is a load error | LIM-04, LIM-02 |
| T-704 | `limits.TestLedgerUsageByIdentifier` | D | The aggregate as the worker role: rows of two models with one name (one deleted, one created again) are two groups; the provider ID is that of the provider with the row's name; a row of a provider that no longer exists has an empty provider ID; another tenant's rows are never seen | **LIM-04**, TEN-04 |
| T-705 | `limits.TestReconcilerTwoZones` | U | Fold pieces are cut at every boundary of a held period of either zone; after a change the counters of the old and the new zone both equal the ledger over their ranges; the reconciler takes no calendar | LIM-02 |
| T-706 | `resolver.TestLimitState` | U | The table of §5.5, each row, with both values of both settings | LIM-11 |
| T-707 | `resolver.TestUnlimitedAuditor` | U, D | One call per tenant and day; the day is the tenant's civil date; a full queue does not block and forgets the day; a failed write is retried by a later request, at most once per 10 s; a second auditor on the same database inserts nothing | **AUD-01** |
| T-708 | `gateway.TestLimitStateRecorded` | I | Each state reaches the usage record and the log field `limit_state`: no rule; a rule admitted; 429; 409 `no_price`; unpriced and allowed; counters unavailable with refuse and with allow; rules unavailable; a request refused before step 7b | LIM-04 |
| T-709 | `gateway.TestUnlimitedTrafficAudit` | I, D | The exit condition: the first unlimited request of a tenant writes one row in each log with the fields of §5.6; a hundred more write none; the next day of the tenant's zone writes one; a tenant with a matching rule writes none; two gateways on one database write one | **AUD-01** |
| T-710 | `gateway.TestTenantZoneQuota` | I, D | Two tenants with different zones and the same daily quota reset at their own midnights; `Retry-After` is the time to the tenant's boundary; a zone changed mid-month changes nothing until the old zone's month ends; an exhausted month is not reopened by a change; after the instant the new zone's day counts the usage of the hours before it | **LIM-02**, **LIM-03** |
| T-711 | `gateway.TestScopeByIdentifier` | I, D | A model rule limits its model; with the rule deleted, the model deleted and a model created with the same name, a gateway that still caches the old rule set does not limit the new model; a new rule on the new model starts from the new model's usage | **LIM-04** |
| T-712 | `gateway.TestNoPriceLogFields` | I | The 409 body is the constant of 5.2; the log line has `limit_currency` and `price_currency` (absent without a price) | LIM-03 |
| T-713 | `admin.TestLimitSubjectsTyped` | D | Creation with each subject kind stores the typed column; a revoked key, another tenant's subject and a missing one get the same 400; scope names resolve to identifiers and a model of another provider is refused; the resource shows names and identifiers; the list filters by `subject_id`, `provider`, `model` | **LIM-04** |
| T-714 | `admin.TestSubjectDeletionDeletesRules` | D | The exit condition, for each row of §5.3: the rules are gone and each has one `limit.delete` row with the actor, the request ID, `cause`, `cause_id` and every field, in the transaction of the operation (a failure injected after the rule deletion leaves subject and rules); the operation's own row has `limit_rules_deleted`; a platform issuer with mappings in two tenants writes to both logs, and the detail of the function equals the detail of the other paths member by member; revoking a revoked key writes nothing; a disabled service account keeps its rules; `createLimit` racing a revocation leaves no rule | **AUD-01**, **AUD-03**, LIM-04 |
| T-715 | `admin.TestCatalogueDeleteRefused` | D | The exit condition: a provider or model named by a rule of another tenant, of this tenant or of the platform cannot be deleted: 409 `invalid_state` with the `errors` entry of §5.4, nothing audited, nothing about a tenant in the answer; after the rule is deleted the deletion succeeds; a model with a price is still refused as before | **LIM-04** |
| T-716 | `admin.TestBudgetCurrency` | D | Creation and update of tenant and platform budgets for each scope: accepted when a price in force or a later one is in the currency, refused with the sorted currencies otherwise; a replaced version does not count; a tenant's set ignores models it cannot be given; no priced model gives the second message; count metrics are not checked | **LIM-03** |
| T-717 | `admin.TestTenantTimeZone` | D | Platform and tenant routes: set, replace while pending (the instant stays), withdraw, set again after the instant; the instant is the start of the next month in the zone in force, for zones whose clocks change around a month start; unknown zone and offsets with seconds are 400; the fields of both resources; the audit rows of §5.7; unchanged value writes nothing; the rule cache is notified | **LIM-02**, **AUD-01** |
| T-718 | `admin.TestTimeZonePermissions` | D | Both operations need their permission; refused to `inference`, to a key scoped without it and to a platform key; `updateTenant` with a zone needs `tenant:write` | TEN-07 |
| T-719 | `isolation.TestTimeZoneAPI` | D | A key of tenant B gets for tenant A's time zone the 404 of a tenant that does not exist, on both methods | **TEN-07** |
| T-720 | `admin.TestCapabilityTenantZone` | D | `checks.budget` uses the tenant's calendar and matches by identifier: a daily quota exhausted in the tenant's day; a model scope; a pending change | LIM-02 |
| T-721 | `admin.TestUsageLimitState` | D | `listUsageRecords` shows the field (empty for a row without it) and filters on each value | — |
| T-722 | `ledger.TestWriterLimitState` | D | The writer stores the state of each record | — |
| T-723 | `cli.TestIntegrityCommands` | U | The commands and flags of §5.11 call their operations; `--default` and a zone are exclusive | LIM-02 |
| T-724 | `e2e.TestRuleIntegrity` | E | The binary and the CLI: a key with a rule is revoked and `limit list` no longer shows it; a mapping with a rule is deleted; a model with a rule cannot be deleted and can after the rule is; a tenant's zone is set and read; `usage list` shows `limited` and `unlimited`; the audit log holds the `limit.delete` rows and one `limit.unlimited_traffic` row | **LIM-04**, **AUD-01** |
| T-725 | `e2e.TestUpgradeWithRules` | E | `pandokos migrate` on a database at 0010 that enforces a quota: after the upgrade and a restart the quota still refuses, with the ledger's count | LIM-04 |
| T-R45 | `repo.TestLimitsGuideIntegrity` | R | `limits.md` has the section on the two quotas with the two recipes, the time zone per tenant and its change rule, the currency check, price overrides in another currency, the four states and the unlimited-traffic row; `tokens.md` section 1 names limit rules for a subject mapping | LIM-12 |
| T-R46 | `repo.TestIntegrityReference` | R | `admin-api.md` lists the two operations, the two audit actions and the four causes; `database.md` has the section of migration 0011 with the upgrade note; `usage.md` names `limit_state` | AUD-01 |
| T-R47 | `repo.TestAmendedRecords` | R | ADR 0049, 0050 and 0052 are `accepted`, and each has an `Amendment (sprint 5.3)` section and a history line that names it | — |

### Changed tests of earlier sprints

| Test | Change |
|---|---|
| T-140, `TestDefaultSpecMatchesDesign` | grants of four tables, two definer functions, the fixture of `limit_rule` |
| migration tests (count, checksums) | migration 0011 |
| T-611, T-619, T-620, T-622, T-654, T-657 | `Rule`, `Limit`, `Query` and `UsageGroup` by identifier; `For` without a calendar argument |
| T-624, T-626, T-662 | typed columns; the constraint cases move to T-694 where they are new |
| T-647 | unchanged; `TenantCalendar` is T-699 |
| T-651, T-652, T-659 | the reconciler without a calendar; pieces cut at held boundaries |
| T-658 | groups by identifier |
| T-664, T-665, T-668, T-674 | `Reserve` returns the state; tenant calendar in the fixtures |
| T-675, T-676, T-677 | resources with identifiers; audit detail with identifiers; subjects and scopes must exist in every case (they did through the API; fixtures that wrote rows directly are fixed); "a rule whose subject disappears stays" is removed |
| T-681 | the capability check through the tenant's calendar |
| T-686, T-687, T-688 | unchanged in what they prove; the canary sweep covers the new audit rows and log fields |
| tests of key revocation, subject mappings, issuers, providers, models, tenants | the added audit detail; the new refusals |
| `TestEveryOperationNeedsACredential`, `TestEveryRouteDeclaresAPermission`, CLI command coverage, T-R44 | two operations, the commands |
| usage list tests (3.3) | the field |
| T-R42 | the guide's new sections do not remove an old one |

### Requirement coverage

| Requirement | Tests |
|---|---|
| LIM-02 | T-699, T-700, T-705, T-710, T-717, T-720 |
| LIM-03 | T-710, T-712, T-716 |
| LIM-04 | T-692, T-693, T-694, T-701, T-704, T-711, T-713, T-715, T-724 |
| AUD-01, AUD-03 | T-692, T-695, T-696, T-707, T-709, T-714, T-717, T-724, T-R46 |
| TEN-04, TEN-06, TEN-07 | T-694 to T-698, T-704, T-718, T-719, T-140 |

## 10. Definition of done for this sprint

**Requirements and tests**

- [ ] Every test of §9 exists under the stated name and passes; the changed tests pass; `make
      check`, `make test-sdk-python`, `make test-sdk-ts`, `make test-container` and `make
      test-compose` pass.
- [ ] The tests-first record exists: the commit of part B precedes the implementation commits
      and the pull request quotes its failing output.
- [ ] The property tests (T-699, T-700) ran with at least three seeds besides the default, and
      `go test -race -count=20 ./internal/limits/` passes; both are quoted.
- [ ] The coverage table is repeated in the pull request with links to the CI run.

**Exit conditions of the epic's sprint**

- [ ] Deleting a mapping or an issuer, or revoking a key, removes the rules of those subjects in
      the same transaction with one audit row each (T-714, T-724).
- [ ] A provider or model that a rule references cannot be deleted (T-715).
- [ ] No statement can leave a rule whose subject or scope does not exist (T-693, T-694).
- [ ] Every usage record says whether the request was limited, unlimited or unverified, or that
      it never reached the limits (T-708, T-722).
- [ ] A tenant's first unlimited request of a day writes exactly one audit row in each log
      (T-696, T-709).

**Isolation and permissions**

- [ ] T-140 reports nothing; the spec lists the two definer functions; T-694 to T-698 pass.
- [ ] Both new routes declare a permission and have a cross-tenant negative test (T-718, T-719).
- [ ] The gateway role still holds no privilege on an audit table and reads no usage record.

**Secrets and content**

- [ ] T-688 passes at log level `debug`; `make secrets` passes. The new audit rows and log
      fields hold identifiers, names of the catalogue, dates and zone names only.

**Upgrade**

- [ ] T-692 and T-725 pass; `database.md` states "stop, migrate, start" and what the previous
      binary does against the new schema.

**Documentation and decisions**

- [ ] The pages of §5.11; ADR 0049, 0050 and 0052 amended; the history lines; `docs/adr/README.md`
      unchanged in statuses.
- [ ] `api/openapi/admin.v1.yaml` is current and `make generate-check` passes.
- [ ] The epic file and the roadmap say what moved; `status:` of this file is `done`; the review
      records the answers to §12.

## 11. Consequences for later sprints

- **5.4** (tenant price lists): `budgetCurrencies` adds the currencies of the tenant's list for
  tenant rules (and keeps the catalogue's: the catalogue price applies where the list has none);
  a tenant price in another currency than the tenant's budgets makes the catalogue check too
  lenient or too strict only through that function. `price_currency` in the `no_price` log is
  then the tenant's price. The usage record gains the price source next to `limit_state`; the
  migration pattern for the partitioned ledger is the one of §5.10 (nullable column, `NOT
  VALID` check). Tenant price tables that reference `model` meet the same question as §5.4:
  restrict or cascade.
- **6.1** (metrics): requests by `limit_state`; unlimited-traffic rows written, skipped and
  failed; rules deleted with a subject, by cause; zone changes pending.
- **6.2** (reports and rollups): reports group by the **tenant's** day and month, with
  `limits.TenantCalendar` as the one definition, the pending change included. A rollup keyed by
  a civil date fixes one zone; a rollup keyed by a UTC bucket no longer than 15 minutes serves
  any zone whose offsets are multiples of it and falls back to the ledger otherwise — decide
  there. The aggregate for counters groups by `model_id`; a rollup must keep it. "Unlimited
  traffic by tenant" is one filter on `limit_state`. A provider ID in the ledger (O-8) belongs
  with the rollup's key if it is wanted.
- **6.3** (tenant deletion): the foreign keys impose an order — `limit_rule` before
  `subject_mapping`, `virtual_key` and `service_account`; `platform_limit_rule` rows of the
  tenant before nothing (they reference the tenant only). Rules removed with a tenant are a
  deletion of limits: audited in the completion record, not row by row. `audit_event` rows of
  `limit.unlimited_traffic` are ordinary audit rows. The definer function of §5.3 is the model
  for deleting across the boundary with an audit trail.
- **6.4** (events): decides whether `limit.unlimited_traffic` is also an event for external
  systems; the audit row is written in a function, so an outbox row can join it there.
- **Epic 8** (shared counters): the Valkey store keys counters by provider and model ID and by
  period start, so two zones' periods coexist as here; every instance derives the same calendar
  from the tenant row, and `limits.time_zone` must still be equal on all. The per-instance
  memory of `UnlimitedAuditor` is safe with any number of instances (the unique indexes), at
  one extra call per instance, tenant and day.
- **4.3** (roles): no new permission; a role with `limit:write` can move the tenant's zone.
- **12** (operability, Helm): the upgrade note of §5.10 belongs in the release notes of v0.2.

## 12. Risks and open points

### What was verified while designing

- Migration 0011 as written in the plan was applied on the test PostgreSQL (16.15, the Docker
  instance of `local/testdb`, 2026-11-18) to a scratch database migrated to 0010 and seeded with
  two tenants, a revoked and a live key, a trusted and a platform issuer with mappings, and
  fourteen tenant rules and five ceilings covering every row of §5.10: seven rules and three
  ceilings survived with the right identifiers and unchanged `updated_at`, seven tenant rules
  and two ceilings were deleted with one audit row each, and every table was left with forced
  row-level security. It was also applied to an empty database after 0001 to 0010.
- Exercised on that database as each runtime role: the foreign keys refuse the deletion of a
  model, a subject mapping, a trusted issuer and a platform issuer while a rule names them, from
  sessions that cannot see the rule (the platform scope sees no tenant rule and is refused by
  `limit_rule_model_fkey`); `delete_platform_issuer_limit_rules` (the admin role in the platform
  scope only; the rule of the other tenant deleted and audited; the tenant setting restored;
  then the issuer deletes); the two key guards, the deferred one at commit; every case of
  `subject_by_kind`, a subject of another tenant, a model of another provider, the unique key
  with nulls; `record_unlimited_traffic` (one row per log and day over repeated calls; the day
  window; no tenant session; the gateway still without privilege on `audit_event`); the time
  zone trigger (every refusal of §5.7, a tenant session that tries to change name or state, the
  platform scope, a superuser session without scope, which existing test helpers rely on);
  `usage_record.limit_state` with a valid and an invalid value, in a partition.
- Found while doing so and fixed in the plan: `INSERT … ON CONFLICT` with an arbiter needs a
  `SELECT` policy for the inserting role under forced row-level security (the owner got one,
  limited to the action); the zone trigger must not constrain a session that has neither a
  tenant nor the platform scope.
- `ADD CONSTRAINT … CHECK … NOT VALID` is accepted on the partitioned `usage_record` and
  enforced on new rows of every partition.
- The queries of appendix A2 and the changed statements of `usage.sql` were compiled with
  `sqlc` v1.31.1 against the eleven migrations on a copy of the repository; a scalar subquery
  that can be NULL must be wrapped (`COALESCE(…, '')`), or the generated field is not nullable.
  The statements with data-modifying CTEs and joins, the currency query (a version in force, a
  later one, a replaced one) and the aggregate were run as their roles.
- The schema lint (T-140) was run against the eleven migrations: its only findings are the ones
  appendix A4 resolves (the grants of four tables, two definer functions).
- Virtual keys are only ever updated (`revoked_at`); the admin role has no `DELETE` on
  `service_account` or `virtual_key`; `subject_mapping` rows go through `DeleteSubjectMapping`
  and the two cascades of migration 0008; `deleteModel` already maps a foreign-key violation to
  409 `invalid_state` (`mapCatalogError`); `usage_record` already stores `model_id`; the
  catalogue snapshot has the provider's ID; test helpers update `tenant.state` as a superuser
  without a scope.
- The highest test IDs are T-691 and T-R44; the highest migration is 0010.
- **Not verified**: `apigen` on appendix A3; the property test T-700 (the argument of §5.7 is
  on paper); the time the two partial indexes take on a large audit log; that PostgreSQL's and
  Go's zone names agree for every name the format check admits (the database only checks the
  shape; Go decides validity).

### Risks

| # | Risk | Mitigation |
|---|---|---|
| R-1 | The migration deletes a rule an operator believed was enforced | Only rules that matched nothing are deleted; each leaves an audit row with every field; `database.md` shows the query that lists them; T-692 |
| R-2 | A code path deletes a subject without its rules | The database refuses it (`RESTRICT`, the deferred trigger): a 500 and no change, found by T-714 and T-724, never an orphan |
| R-3 | A deadlock between `createLimit` and a deletion with rules | Both take the tenant's advisory lock first, then rows; the function takes it per tenant in the order of the tenant list |
| R-4 | The foreign keys make rule creation or catalogue deletion slow | Rule tables are small; partial indexes on `provider_id`, `model_id`, the mapping and the key |
| R-5 | The definer function is given tenants or an actor it should not trust | It runs only for the admin role in the platform scope, deletes only rules of the given issuer's mappings, and validates the actor's shape; the actor is audit text, as in every handler |
| R-6 | A tenant changes its zone to reopen a quota | §5.7: nothing changes before the old month ends, and the new periods count the whole range; T-700, T-710 |
| R-7 | Zone databases disagree between instances, or the stored zone is unknown to a newer or older binary | The binary embeds the database (5.2 D-1); an unknown zone refuses the tenant's requests (visible at once) rather than counting in another zone |
| R-8 | The unlimited-traffic row is missed | Only when the database is unwritable from the first unlimited request to the end of the tenant's day; `limit_state` in the ledger is the complete record |
| R-9 | A flood of tenants on their first request of the day fills the queue | The queue drops without blocking and the day is forgotten, so the next request offers it again; the write is one function call |
| R-10 | The currency check blocks a legitimate budget (prices not imported yet, a tenant price list in 5.4) | The message says which currencies exist or that none does; importing prices or adding an override resolves it; 5.4 extends the set |
| R-11 | Tests of 5.1 and 5.2 that seeded rules with invented subjects break in bulk | The seed helpers resolve and fail loudly; plan B1 lists the affected tests; fixtures gain real subjects |
| R-12 | The previous binary runs against the new schema during a careless upgrade | It turns not ready and refuses with 503 (fail closed); documented |
| R-13 | A provider recreated under the same name inherits the period's usage for a provider-scoped rule | Restrictive, rare, documented (O-8) |

### Open points (raised to the maintainer; none blocks the sprint)

| # | Point | Recommendation |
|---|---|---|
| O-1 | A zone change takes effect at the start of the next month of the zone in force, for days and months alike, so it can wait up to a month. Alternative: days switch at the old zone's next midnight and months at its next month start (two instants, three zones in play after a second change) | Keep one instant: one calendar per tenant at any time, simpler reports in 6.2 |
| O-2 | After the instant, the first day and month of the new zone count the usage of their whole range, the hours before the instant included (overlap), instead of a shortened or lengthened transition period | Keep: it is what makes "never frees a quota" hold without a special period; the cost is a day that can refuse at its start |
| O-3 | A tenant that never had a zone also waits for the next month; only a zone given at creation is in force at once. Alternative: apply at once while the tenant has no usage record | Keep the single rule; onboarding sets the zone at creation (`tenant create --time-zone`) |
| O-4 | The tenant sets its zone with `limit:write`. Alternative: a permission of its own | Keep: no new permission for one setting; 4.3 can split it |
| O-5 | A platform change of a tenant's zone is audited in the platform log only (as every `tenant.update`); the tenant sees it in `getTenantTimeZone`, not in its audit log | Keep; a second row needs another definer function for little |
| O-6 | `limit_state` has a fourth value, `not_evaluated`, and NULL for older rows; the epic names three | Keep: a request refused before the limits was not "unlimited" |
| O-7 | A request admitted past a budget that cannot measure it (`on_unknown_price: allow`) is `limited`, not `unverified`: the rule was evaluated and the configuration decided | Keep, as the epic words `unverified`; `budget_unpriced` stays in the log |
| O-8 | Provider-scoped counters read the provider of a ledger row by name. Alternative: a `provider_id` column in `usage_record` | Keep for now; add it with the rollup key of 6.2 if wanted |
| O-9 | The migration deletes the rules of revoked keys and of subjects and scopes that are gone, with audit rows, instead of failing or parking them in a table | Keep: they matched nothing, and the audit row holds every field |
| O-10 | Deleting a referenced provider or model tells the platform administrator that limit rules reference it, tenants' rules included, and nothing else | Keep: the refusal itself reveals it; a count or a list would be a crossing |
| O-11 | The currency check answers 400 on `currency` (not 409) and also applies to unenforced budgets and to `PATCH` | Keep: the class of "names something that is not there"; a budget in a currency without prices can only ever count zero |
| O-12 | The actor of `limit.unlimited_traffic` and of the migration's rows is `platform` with the actor names `gateway` and `migration 0011`; no new actor type | Keep: a new type means rewriting two check constraints on append-only logs |
| O-13 | Removing a tenant from a platform issuer's list keeps the tenant's mappings of that issuer and their rules (it deletes no subject) | Keep; the mappings come back into use when the tenant is listed again |

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
| O-11 | Accepted as recommended |
| O-12 | Accepted as recommended |
| O-13 | Accepted as recommended |
