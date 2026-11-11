# 0007. Authorization: role-based, with a permission catalogue defined in code

- Status: proposed
- Date: 2026-11-11

## Context

Tenants need built-in and custom roles; the platform needs its own roles; every route and job must
be checked (AZ-01 to AZ-10). The model must be explainable to an auditor.

## Decision

- **Permissions** are `resource:action` strings (`credential:write`, `credential:use`,
  `usage:read`, `model:invoke`, …) declared in one Go package, which is the catalogue. Each
  administration route and each gateway operation declares the permission it requires; a test
  fails if a route declares none.
- **Roles** are named sets of permissions. Built-in tenant roles and platform roles are defined in
  code and cannot be edited. Custom tenant roles are rows that reference catalogue permissions.
- **Assignments** bind a user or service account to a role within a tenant. Platform roles are
  assigned separately and confer no tenant permission.
- **Evaluation** happens in process: the union of the principal's permissions, intersected with
  the scopes of the credential in use, checked against the required permission and the state of
  the tenant. Resource-level constraints that are not roles (a key's model allow-list, a pinned
  credential) are checked by the component that owns the resource.
- **No escalation.** A principal can grant or put into a custom role only permissions it holds.
- **Platform support actions** inside a tenant are a distinct permission that requires a reason
  and writes to the tenant's audit log.

## Consequences

- The whole model fits on a page that is generated from the catalogue.
- Adding a permission is a code change, reviewed like any other.
- Attribute-based rules (time of day, network, resource tags) are not expressible. None is
  required.

## Alternatives considered

**An external policy engine (OPA, Cedar).** Powerful and auditable, but a new language, a larger
dependency or a sidecar on the hot path, for a model that needs set membership.

**Relationship-based access control.** Suited to deep resource hierarchies, which Pandokos does
not have.

**Roles only, without a permission layer.** Makes custom roles impossible and ties every check to
role names.
