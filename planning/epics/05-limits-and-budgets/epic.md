---
id: EPIC-5
title: Limits and budgets
status: in-progress
release: v0.2
depends_on: [EPIC-3]
---

# Epic 5 — Limits and budgets

## Outcome

Rate limits, quotas and monetary budgets are enforced before a request is forwarded and hold under
concurrency within a documented bound, on a single instance. Shared counters for several instances
come with epic 8.

## Decisions it implements

ADR 0012.

## Sprints

### 5.1 Reserve and settle

The riskiest part first: the semantics under concurrency.

- The limiter interface: reserve, settle, release, with expiry of abandoned reservations.
- In-process counter store.
- Request-side estimation of tokens and cost.
- Requests and tokens per minute; per-tenant concurrency.
- Limit rules at platform, tenant and key level; a lower level only restricts.
- The 429 contract: codes, `Retry-After`, remaining-allowance headers, usage record with outcome
  `rejected_limit`.
- Property and race tests for the limiter.

Requirements: LIM-01, LIM-04, LIM-06 to LIM-09, GW-16 (concurrency).

Exit: under a parallel load test, admitted requests never exceed a request-rate limit, and token
overshoot stays within the documented bound; a failed or cancelled request leaves nothing
reserved.

### 5.2 Quotas and budgets

Designed and delivered as [5.2-quotas-and-budgets/sprint.md](5.2-quotas-and-budgets/sprint.md).

- Periodic counters per day and month with a configurable time zone.
- Monetary budgets on estimated cost; behaviour with unknown prices.
- Per-provider and per-model rules.
- Soft thresholds that record an event (delivery to external systems comes with sprint 6.4).
- Counters rebuilt from ledger aggregates at start; periodic reconciliation by the worker.
- Behaviour when the counter store fails; configuration of the default.
- The `budget` field of the capability check (carried from epic 3, with the read of a counter it needs).
- A default output reservation per model (carried from 5.1: sprint 5.1 reserves one configured default).
- Python and TypeScript SDK tests of a Pandokos 429, once the CLI can write a rule.
- Administration API and CLI for limits, with permissions and audit records for limit rules and
  existence checks of the subject on write; documentation of what is strict and what is best effort.

Requirements: LIM-02, LIM-03, LIM-05, LIM-11, LIM-12.

Exit: a tenant with a monthly budget is refused once reservations reach it and admitted again
after the period resets; after a restart counters match the ledger.

### 5.3 Integrity of limit rules

Added after the review of ADR 0049 by the maintainer (2026-11-18). Scope closed on 2026-11-18.

- Typed subject columns on `limit_rule` (`service_account_id`, `virtual_key_id`,
  `subject_mapping_id`) with composite foreign keys `(tenant_id, id)` and `ON DELETE RESTRICT`, in
  place of the text `subject_id`; a check that exactly the column of `subject_kind` is set; the
  existing rows migrated.
- The scope of a rule by identifier (`provider_id`, `model_id`, with foreign keys) in both rule
  tables, in place of names; the API keeps accepting names and resolves them on write. A model
  created later with the name of a deleted one is not matched by an old rule.
- Deleting a subject mapping, deleting an issuer with its mappings and revoking a virtual key delete
  the rules of those subjects in the same transaction, each with an audit row `limit.delete` naming
  the actor of the deletion and the reason. A disabled service account keeps its rules.
- Deleting a provider or a model that a rule of any tenant or of the platform references is refused
  with 409; the documentation says to disable the model or remove its grants instead.
- No periodic job looks for orphan rules: integrity is structural, and a schema test checks that
  every subject and scope column of the rule tables has its foreign key.
- The usage record states how limits treated the request: `limited` (at least one rule applied and
  was verified), `unlimited` (no rule applied) or `unverified` (rules or counters could not be
  verified and `limits.on_store_failure` is `allow`).
- The first request of a tenant in a day that no rule covers writes one audit row
  `limit.unlimited_traffic` in the tenant's log and in the platform log, through a restricted
  function as for authentication failures.
- A time zone per tenant for days and months (a column of the tenant, `limits.time_zone` becoming
  the default for tenants that have none), set by the platform or by the tenant; a change takes
  effect from the first period that starts after it, so moving a boundary never frees a quota. The
  reports of sprint 6.2 use the same zone.
- A budget is refused on write when no price that can be in force for its scope is in its currency
  (no conversion, ADR 0050 option A), with an answer that names the currencies the prices of that
  scope use; the limits guide explains that prices are set by the platform for every tenant.
- Documentation: the limits guide explains the two quotas, the one decided in Pandokos and the one
  a provider grants to a credential, which Pandokos cannot see, and shows how to protect a
  provider tier and keep a batch key from starving an interactive one with rules scoped to the
  provider (issue #32); the table of the tokens guide states that a subject mapping can be the
  subject of a limit rule and that service-account limits apply to tokens (issue #30).
- Amendments to ADR 0049 and ADR 0050 record these decisions.

Requirements: LIM-02, LIM-03, LIM-04, AUD-01.

Exit: deleting a mapping or an issuer, or revoking a key, removes the rules of those subjects in the
same transaction with one audit row each; a provider or model that a rule references cannot be
deleted; no statement can leave a rule whose subject or scope does not exist; every usage record
says whether the request was limited, unlimited or unverified; a tenant's first unlimited request
of a day writes exactly one audit row.

### 5.4 Tenant price lists

Added after the review of ADR 0050 by the maintainer (2026-11-18); the need and the proposal are in
issue #48.

- Price versions held by a tenant for the models it can see, in any currency, with the rules of the
  catalogue prices (ADR 0036): immutable, never backdated, exact decimals, tiers and fallbacks.
- For that tenant they take precedence over the catalogue price; the usage record states the price
  version and its source (`catalogue` or `tenant`).
- Tenant-scoped storage under row-level security with isolation tests; administration API and CLI
  with permissions of their own, separate from catalogue permissions; audit records.
- The budget write check of 5.3, the reservation and the capability check use tenant prices.
- A new requirement for tenant price lists (proposed CAT-11).

Requirements: CAT-08, CAT-09, LIM-03, new CAT-11.

Exit: a tenant with a EUR price list and a EUR budget is refused once its reservations reach the
budget, while another tenant on the same models keeps the catalogue USD price and its USD budget.

## Risks

- Estimating output tokens when the caller sets no limit forces a conservative default that may
  refuse requests near a budget's end. The default is per model and configurable, and the
  behaviour is documented. (Delivered as `model.default_output_tokens`.)
