# 0052. Administration of limits: scopes, thresholds, events, API and permissions

- Status: proposed
- Date: 2026-11-18

Refines [ADR 0049](0049-limit-rules.md); implements the first half of [ADR 0014](0014-events-outbox.md)
for limits.

## Context

0049 fixed the storage and meaning of rules and left their writing to this sprint. LIM-04 asks for
provider and model scopes, LIM-05 for soft thresholds.

## Decision

1. Both rule tables carry scope, currency, enforcement and thresholds for all metrics. There is one
   rule per subject, scope and metric. Value, enforcement and thresholds are the only mutable fields.
2. Two API resources, tenant rules and platform ceilings, with read, write and delete permissions per
   scope (`limit:*`, `platform_limit:*`); a tenant reads the ceilings that apply to it.
3. A write checks that the subject and the scope exist, with one answer for "elsewhere" and
   "nowhere", and bounds a tenant to 5,000 rules under a lock, answering 409 `invalid_state` beyond.
4. Every change is audited in its transaction (`limit.create|update|delete`,
   `platform_limit.create|update|delete`).
5. A threshold is reached when what was used passes it; an enforced limit is exhausted at its first
   refusal in a period. Both are rows of the append-only table `limit_event`, unique per rule,
   period, kind and percentage, inserted by the gateway role off the request path. Sprint 6.4 adds
   the outbox row in the same transaction.

## Consequences

- Events are durable and idempotent before any delivery exists; one can be lost only across a crash
  at the end of a period.
- A rule whose subject or scope later disappears stays and matches nothing.
- Limits can be managed by a tenant administrator without any platform privilege, and a platform
  administrator can set a ceiling that a tenant cannot lift.

## Alternatives considered

**Thresholds as separate soft rules.** Two rows to keep in step for one fact.

**Events detected by the worker from ledger aggregates.** Minutes late, and blind to a refusal.

**Writing the outbox now.** Its schema belongs to 6.4.
