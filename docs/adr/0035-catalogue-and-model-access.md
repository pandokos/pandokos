# 0035. Catalogue and model access

- Status: proposed
- Date: 2026-11-14

Refines [ADR 0009](0009-native-passthrough-by-api-family.md) and
[ADR 0027](0027-database-roles-and-session-context.md).

## Context

CAT-01 to CAT-06 ask for providers, families, endpoints and models as distinct concepts, for
models available to all or selected tenants and narrowed by tenants and keys, and for one
refusal of a model that is not allowed whatever identifier is used. The gateway reads the
catalogue without platform scope; the platform must still see which tenants a model is granted
to.

## Decision

1. Catalogue tables are global with forced row-level security: every runtime role that needs
   them reads them in any session; only the admin role in platform scope writes them (the
   worker inserts prices). Grants of models to tenants are a global table whose rows a tenant's
   session sees only for itself.
2. A provider has an immutable name (the first path segment; `healthz`, `readyz` and `v1` are
   reserved) and an immutable API family from a list in code. It has endpoints; one is the
   default the gateway uses. An endpoint declared private may use `http`; a public one may not
   name a private address.
3. A model has a public name (immutable) and an upstream identifier, each unique per provider;
   it is referenced as `provider/name`; status `active`, `deprecated` or `retired`;
   availability `all` or `selected`.
4. A principal may use a model when the provider is enabled with an enabled default endpoint,
   the model is not retired, it is available to the tenant (all, or granted), and neither the
   tenant's restriction nor the key's immutable model list leaves it out. Every other case is
   the same refusal.
5. The gateway caches the catalogue and each tenant's access for `cache.catalog_ttl`,
   invalidated by the payloads `catalog` and `catalog:<tenant>`.

## Consequences

A tenant cannot learn which models other tenants were granted. Catalogue writes cannot happen
from a tenant's session even through a defective handler. A change reaches the gateway within
the cache lifetime.

## Alternatives considered

Open global tables (writes would rest on handler code); a tenant-scoped grant table (the
platform could not list a model's tenants); matching callers' model names against upstream
identifiers or patterns (a second way in, which CAT-06 forbids); a mutable key model list (a
second invalidation path).
