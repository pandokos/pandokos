# 0031. Authorization core and tenant provisioning

- Status: proposed
- Date: 2026-11-12

Refines [ADR 0007](0007-authorization-rbac.md) and [ADR 0027](0027-database-roles-and-session-context.md).

## Context

A platform administrator has no permission inside a tenant (AZ-06) and tenants cannot register themselves (IAM-01), yet somebody must create the first identity of a tenant.

## Decision

Permissions have one scope (platform, tenant, self) and the decision is one function with five rules, default deny; a tenant principal that addresses another tenant gets 404 whether or not the tenant exists. `tenant-admin` holds every tenant permission, `model:invoke` included. `store.Provision` is the single transaction that carries platform scope and, after `bind`, the new tenant: it creates a tenant, its first administrator and key, and activates it, atomically.

## Consequences

The platform administrator sees the first key of a tenant; a platform administrator cannot repair a tenant until the support actions of a later sprint.

## Alternatives considered

A platform permission inside tenants; provisioning in several transactions; self-registration of tenants (excluded by IAM-01).
