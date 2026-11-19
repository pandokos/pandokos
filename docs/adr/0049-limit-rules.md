# 0049. Limit rules: levels, subjects and resolution

- Status: accepted
- Date: 2026-11-17

Refines [ADR 0012](0012-limits-and-budget-reservation.md).

## Context

LIM-04 asks for limits at platform, tenant and key level, where a lower level can only restrict.
0012 said that every applicable limit is reserved; it did not say where rules live, what the
platform level means, or what a rule's subject is now that a caller can be a key or a token.

## Decision

1. Two tables. `platform_limit_rule` is global and written in platform scope: a row without tenant
   is the default ceiling of every tenant, a row with a tenant replaces the default for it.
   `limit_rule` is tenant-scoped: a rule names a subject (the tenant, a service account, or a key: a
   virtual key by key ID, a token caller by its subject mapping), a metric and a value; there is no
   foreign key to the subject.
2. A request is reserved against one limit per subject and metric that has a rule; for the tenant
   subject the value is the smaller of the platform ceiling and the tenant's rule. Every one must
   admit: a rule can only remove room.
3. No rule means no limit, and nothing is seeded.
4. The gateway reads a tenant's rules in that tenant's session and caches them per tenant, with a
   lifetime and the notifications `limits` and `limits:<tenant>`; when they cannot be read and none
   are cached, the request is refused as unavailable. A changed rule applies to the counts already
   held.
5. Rules are written through the administration API of sprint 5.2; this record fixes their storage
   and meaning only.

## Consequences

- No validation between levels exists to be wrong; a rule above its parent is valid and idle; a rule
  for a subject that does not exist is idle.
- A platform ceiling counts each tenant on its own, and an installation-wide sum is not expressible.

## Alternatives considered

**Limits as columns of the key.** Does not cover tokens, tenants or the platform.

**One table with a level column.** A tenant-scoped row that only the platform may write needs a
policy on the actor, not on the scope.

**Rejecting a rule that exceeds its parent.** A later change of the parent would invalidate rows.

**Rules in the configuration file.** ADR 0019 puts limits with the state managed through the API.

## Amendment (sprint 5.3)

Decided in the review of 2026-11-18; replaces "there is no foreign key to the subject" of decision 1 and
the consequence "a rule for a subject that does not exist is idle".

1. A rule names its subject by typed columns (`service_account_id`, `virtual_key_id`,
   `subject_mapping_id`) with composite foreign keys `(tenant_id, id)`, and its scope by `provider_id`
   and `model_id`, in both tables. All are `ON DELETE RESTRICT`: deletions are audited by the
   application, so nothing cascades. `limit_event` has no foreign key.
2. The API takes and shows names; the gateway and the counters match by identifier. A model created
   with the name of a deleted one is another model.
3. Revoking a virtual key, deleting a subject mapping and deleting an issuer with its mappings delete
   the rules of those subjects in the same transaction, each with an audit row `limit.delete` that
   names the actor and the cause. A disabled service account keeps its rules. A revoked key has no rule
   (two triggers, since keys are never deleted).
4. A provider or model that a rule of any tenant or of the platform references cannot be deleted (409
   `invalid_state`); the foreign key decides, without reading across tenants.
5. No job looks for orphan rules; a schema test checks the foreign keys.
6. Migration 0011 resolved existing names to what they designated and deleted, with an audit row each,
   the rules that designated nothing or a revoked key.
7. The usage record states how limits treated the request (`limit_state`: `limited`, `unlimited`, `unverified`,
   `not_evaluated`), and a tenant's first unlimited request of a day leaves one audit row
   `limit.unlimited_traffic` in the tenant's log and in the platform's.

## Amendment (sprint 5.4)

A cost limit outside the tenant's active currency is left out when the limits of a request are resolved. A tenant's ceiling replaces the default of the same scope, metric and currency.

## History

- 2026-11-18: sprint 5.2 adds the columns (scope, currency, enforcement, thresholds) and the administration API ([ADR 0052](0052-administration-of-limits.md)).
- 2026-11-18: accepted by the maintainer after review. Amendments planned in sprint 5.3: typed subject columns with foreign keys (`ON DELETE RESTRICT`), the scope by identifier, rules deleted with audit when their subject is deleted or a key revoked, deletion of a referenced provider or model refused, unlimited traffic traced in the usage record and once a day per tenant in the audit log.
- 2026-11-18: amended by sprint 5.3 (see Amendment).
- 2026-11-18: amended by sprint 5.4 (see Amendment); [ADR 0053](0053-tenant-price-lists-and-active-currency.md).
