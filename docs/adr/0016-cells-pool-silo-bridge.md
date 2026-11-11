# 0016. Pool, silo and bridge as deployments of cells

- Status: proposed
- Date: 2026-11-11

## Context

The requirements name three isolation tiers from multi-tenant SaaS practice: *pool* (shared
resources, logical isolation), *silo* (resources dedicated to a tenant) and *bridge* (both in one
platform). They also forbid depending on Kubernetes (OPS-01) and ask that the API contract not
change between tiers (OPS-06).

Dedicated resources are worth having only if the dedication is real: a "silo" that shares the
database and the key-encryption key with everyone else isolates nothing that matters.

## Decision

The unit of deployment is the **cell**: gateway instances, their database, their key-encryption
key and, if present, their counter store.

- **Pool** is one cell that serves many tenants. It is the default and the only tier in the first
  releases.
- **Silo** is a cell configured with the single tenant it may serve (`tenancy.pinned_tenant`).
  A pinned cell refuses to create or serve any other tenant, at start-up and on every request. It
  has its own database and its own key-encryption key.
- **Bridge** is a platform made of a pool cell and one or more silo cells. An edge proxy operated
  by the platform routes each tenant's host name to its cell. Callers and integrators see the
  same API everywhere.

Pandokos provides the pinning guard, the Compose and Helm material to deploy a cell, and a
documented export and import procedure to move a tenant between cells. Provider credentials do
not move in clear: the tenant re-enters them, or the operator runs an offline re-wrap between the
two key-encryption keys.

Pandokos does **not** provide: creation, scaling or removal of cells; a fleet-wide administration
API across cells; automatic routing. Those belong to the operator's tooling. A Kubernetes operator
is a possible later project outside the core.

## Consequences

- A silo gives a tenant what the word promises: separate data at rest, separate keys, separate
  failure domain.
- Each cell is administered through its own admin API. A platform with many silos needs its own
  automation on top. This is an accepted limit of the product's scope.
- Nothing in the core has to know about cells beyond the pinning guard, so the tiers add almost no
  complexity to the code that every installation runs.

## Alternatives considered

**Dedicated gateway instances over a shared database.** Isolates compute and noisy neighbours,
not data or keys. It can be done with ordinary load-balancer rules and does not need product
support.

**A control plane that provisions cells.** Requires an orchestrator API and makes the control
plane the most privileged component. Out of scope.

**A database or schema per tenant inside one installation.** See
[ADR 0004](0004-postgresql-and-rls.md).
