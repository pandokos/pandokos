# 0077. An instance defined by configuration alone

- Status: proposed
- Date: 2026-11-25

**Amends** [ADR 0066](0066-declarative-export-and-apply.md) (point 3 delivered and extended) and
[ADR 0072](0072-event-types-the-envelope-the-external-reference-and-platform-events.md) (destinations and the
external reference are in `export` and `apply`). Builds on [ADR 0019](0019-configuration.md). Sprint 12.3.

## Context

Replicas and Kubernetes (epic 8) need to say what makes two instances the same installation and what makes a new
one. "The same on every instance" was a sentence in one guide for one key, and the declarative document could not
carry issuers, mappings or destinations.

## Decision

1. **Every configuration key has a scope**, `instance` (it may differ between instances of one installation) or
   `installation` (it must not), in the key table and in the reference. It is documentation until a later sprint
   enforces it. (D-13)
2. **The declarative document gains** platform-level issuers with their tenants, tenant issuers, subject mappings,
   limit rules on a mapping, destinations, and the external reference, in a platform section `tenants` that never
   creates a tenant. (D-14)
3. **`apply` creates a destination only with `--secrets-out DIR`**, which writes the secret shown once to a file
   (mode 0600, created exclusively) there; otherwise it skips the destination and says so. (D-15)
4. **A claim value equal to the tenant's ID is not exported**, and an absent one means the target tenant's ID. (D-16)
5. **No default limit is created by `init` or by a migration**; a default ceiling is a line of an example platform
   document. (D-17)

## Consequences

- What distinguishes installations is a file or the environment plus documents; tenants, people and secrets stay
  outside.
- `apply` never creates tenants, never deletes, never rotates a secret.
- A new key needs a scope; a new resource that distinguishes installations is in the document or in its list of
  what never is.

## Alternatives considered

- **Creating tenants from a document:** a tenant has people and secrets; the platform creates it.
- **Secrets in a document:** it would stop being safe to keep in version control.
- **A start-time comparison of instances now:** it needs replicas; the scope is the list to compare then.

## History

- 2026-11-25: proposed with sprint 12.3.
