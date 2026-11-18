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

Added after the review of ADR 0049 by the maintainer (2026-11-18). Content still being collected.

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
- An amendment to ADR 0049 records these decisions.

Requirements: LIM-04, AUD-01.

Exit: deleting a mapping or an issuer, or revoking a key, removes the rules of those subjects in the
same transaction with one audit row each; a provider or model that a rule references cannot be
deleted; no statement can leave a rule whose subject or scope does not exist; every usage record
says whether the request was limited, unlimited or unverified; a tenant's first unlimited request
of a day writes exactly one audit row.

## Risks

- Estimating output tokens when the caller sets no limit forces a conservative default that may
  refuse requests near a budget's end. The default is per model and configurable, and the
  behaviour is documented. (Delivered as `model.default_output_tokens`.)
