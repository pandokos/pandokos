# 0052. Administration of limits: scopes, thresholds, events, API and permissions

- Status: accepted
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

## Amendment (sprint 5.3)

Decided in the review of 2026-11-18; replaces the consequence "a rule whose subject or scope later
disappears stays and matches nothing".

1. The existence of a rule's subject and scope is held by foreign keys; the checks on write remain, to
   answer 400 with one message for "elsewhere" and "nowhere", and a revoked key is not a subject.
2. Rules deleted with their subject are audited as `limit.delete` with the actor of that operation and,
   in the detail, `cause` (`key_revoked`, `subject_mapping_deleted`, `token_issuer_deleted`,
   `platform_issuer_deleted`) and `cause_id`. For a platform-level issuer a definer function deletes
   and audits them in each tenant.
3. Two audit actions are added: `limit.unlimited_traffic` (tenant and platform log, written by a
   definer function the gateway role may execute, once per tenant and day) and
   `tenant.time_zone_update`.
4. Two operations are added, `getTenantTimeZone` and `setTenantTimeZone`, under `limit:read` and
   `limit:write`.

## Amendment (sprint 5.4)

There is one rule per subject, scope, metric and currency (decision 1). The rule resources show whether a cost rule is active.

## History

- 2026-11-18: accepted by the maintainer after review. Amendments planned in sprint 5.3: existence checks of subject and scope become foreign keys; automatic deletions of rules are audited as `limit.delete` with the actor and the reason.
- 2026-11-18: amended by sprint 5.3 (see Amendment).
- 2026-11-18: amended by sprint 5.4 (see Amendment); [ADR 0053](0053-tenant-price-lists-and-active-currency.md).
- 2026-11-22: sprint 4.3: the tenant's time zone is set with `tenant_setting:write`, no longer with `limit:write` ([ADR 0060](0060-built-in-roles-custom-roles-and-the-rule-against-escalation.md)).
- 2026-11-23: sprint 6.4 writes the outbox row this record deferred: a trigger on `limit_event` produces the event of each row ([ADR 0070](0070-the-outbox-as-a-log-relay-by-destination-and-retention.md), [ADR 0072](0072-event-types-the-envelope-the-external-reference-and-platform-events.md)).
