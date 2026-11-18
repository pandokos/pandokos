# 0043. The tenant capability check and the minimal usage list

- Status: proposed
- Date: 2026-11-14

Refines [ADR 0035](0035-catalogue-and-model-access.md), [ADR 0037](0037-request-pipeline-and-resolution.md)
and [ADR 0039](0039-usage-record-admission-and-ledger-writer.md).

## Context

API-07 asks that an integrator can learn whether a tenant can call a provider and model now. The
gateway decides this at steps 7 and 8 with the catalogue cache and the vault, which opens the
credential. The administration plane must never decrypt a credential. The quick-start must also show
a usage record without SQL, while reports belong to a later release.

## Decision

- `GET /v1/tenants/{tenant}/capability?provider=&model=` (permission `model:read`) evaluates, on
  committed state, the gateway's rules for a key of the tenant without pin or model list: the catalogue
  rule (`catalog.Snapshot.Resolve`) and the credential resolution rule applied to credential metadata.
  It answers `callable`, the failing checks in a fixed order, and `budget: not_enforced` until limits
  exist. A suspended tenant is answered 403 `tenant_suspended` by the authorization rule. Nothing is
  opened, cached or audited.
- `GET /v1/tenants/{tenant}/usage-records` (new permission `usage:read`, held by `tenant-admin`) lists the
  tenant's records newest first in a window of at most 31 days, with a keyset cursor bound to the window
  and filter, read as the admin role in the tenant's session.

## Consequences

- The answer can differ from a call made at the same moment by at most the gateway's cache lifetimes.
- A key's pin and model list can only narrow the answer; they are not evaluated.
- The admin role's `SELECT` on the ledger, granted in 0006, is now used by a route.

## Alternatives considered

**An endpoint on the inference listener that runs steps 3 to 8 for the caller's key.** Exact for that
key and its caches, but an application concern rather than an integrator's, and it would open the
credential to answer a question.
**No list until reports exist.** The quick-start would need SQL as the database owner.

## History

- 2026-11-18: sprint 5.2: `checks.budget` takes the values `not_enforced`, `unpriced`, `exhausted` and `available`, with the reasons `no_price` and `budget_exhausted` ([ADR 0051](0051-period-counters-and-the-ledger.md)).
- 2026-11-18: the budget check reads the ledger with the worker database role, inside the tenant's session under row-level security, not with the admin role: the reader is built by `serve` on the worker pool and handed to the administration plane ([ADR 0051](0051-period-counters-and-the-ledger.md)). The `SELECT` of the admin role on the ledger is still used by the usage list.
