# 0004. PostgreSQL as system of record, with forced row-level security

- Status: proposed
- Date: 2026-11-11

## Context

Tenant isolation must hold even when application code forgets a filter (TEN-04). The product
needs transactions across configuration, audit and outgoing events, and a single mandatory
dependency (OPS-01).

## Decision

PostgreSQL (16 or later) is the only required data store and the system of record for everything
durable.

Isolation uses a **shared schema with row-level security**:

- every tenant-scoped table has `tenant_id` and has row-level security both enabled and forced;
- policies compare `tenant_id` with the transaction-local setting `app.tenant_id`; an absent
  setting matches no rows;
- the application opens every tenant-scoped transaction through one helper that sets the tenant;
  there is no other way to obtain a connection;
- runtime roles are not owners and do not have `BYPASSRLS`; there is one role per process role
  (gateway, admin, worker), each with the minimum grants;
- the one lookup that must cross tenants, finding a virtual key by its ID, goes through a narrow
  `SECURITY DEFINER` function;
- column privileges keep credential ciphertext out of reach of platform-scope sessions.

Access code is plain SQL in files, compiled to typed Go with `sqlc`, on the `pgx` driver. There is
no ORM. Migrations are SQL files embedded in the binary and applied by `pandokos migrate`.

A schema lint in CI fails the build when a table is neither declared global nor protected by a
policy.

## Consequences

- A bug in a query yields an empty result or a rejected write, not another tenant's data.
- Transaction-local settings work behind connection poolers in transaction mode.
- Every tenant-scoped statement runs inside a transaction, including single reads. The hot path
  avoids the cost by reading from memory.
- Cross-tenant work (reports for the platform, retention) must be written as a loop over tenants
  or through purpose-built aggregate tables. This is a deliberate friction.
- Policies add a predicate to every query; indexes lead with `tenant_id`.
- Supporting a second database engine is not a goal.

## Alternatives considered

**A schema or a database per tenant.** Stronger separation and simple deletion, but migrations
and connection pools multiply with tenants and the pool model becomes expensive. Dedicated
databases are offered at a coarser grain through cells ([ADR 0016](0016-cells-pool-silo-bridge.md)).

**Application-level filtering only.** One missed filter is a breach.

**SQLite for small installations.** Attractive for a single container, but it has no row-level
security, so the product would have two isolation models. Rejected.

**An ORM.** Hides the SQL that the isolation model depends on and adds a large dependency.
