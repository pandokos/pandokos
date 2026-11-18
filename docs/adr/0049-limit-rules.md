# 0049. Limit rules: levels, subjects and resolution

- Status: proposed
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
