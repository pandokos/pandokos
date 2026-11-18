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

## Risks

- Estimating output tokens when the caller sets no limit forces a conservative default that may
  refuse requests near a budget's end. The default is per model and configurable, and the
  behaviour is documented. (Delivered as `model.default_output_tokens`.)
