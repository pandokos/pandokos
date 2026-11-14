# Epic 2 — Vault and catalogue: record

Completed 2026-11-14. Epic file as closed: [epic.md](epic.md).

## Outcome

A tenant stores provider credentials that nobody can read back: secrets are sealed with AES-256-GCM
under a per-tenant data key wrapped by a key-encryption key from a local key file, bound to tenant,
credential and data-key version, and readable in clear only by the gateway role in the tenant's
session. Credentials have labels, one default per provider, a fingerprint, a life cycle (disable,
enable, revoke, delete) and optional validation against OpenAI at write time; the platform
administrator sees their metadata and can disable or revoke them, never read them. The gateway has an
`Opener` with bounded caches and a documented revocation bound.

The platform administrator describes providers, API families, endpoints (private ones marked
explicitly) and models, grants models to all tenants or to selected ones; a tenant administrator
narrows the list, and a virtual key can carry its own model list. Prices are versioned in time,
computed with decimal arithmetic, imported from the open `genai-prices` dataset (an embedded
snapshot at start-up, `pandokos import-prices`, the API, or an optional periodic refresh) and
overridden by the operator; every change is audited.

The inference route still forwards with the operator's single key of `skeleton.openai.*`: taking the
endpoint, the model check and the credential from the catalogue and the vault is sprint 3.1.

## Sprints

| Sprint | Design and plan | Delivered | Review |
|---|---|---|---|
| 2.1 Vault | [sprint](2.1-vault/sprint.md), [plan](2.1-vault/plan.md) | PR #9 | 3 rounds (5 findings, then 1, then none) |
| 2.2 Catalogue and prices | [sprint](2.2-catalogue-and-prices/sprint.md), [plan](2.2-catalogue-and-prices/plan.md) | PR #10 | 2 rounds (8 findings, then none) |

Each sprint followed the cycle of epic 1: a frozen design, tests committed failing before the
implementation, a review against the design repeated until it reported no findings, a pull request
with green CI. Open points raised by each design and the maintainer's decisions on them are at the
end of section 12 of each `sprint.md`; deviations accepted at review are listed in the pull requests.

## Decisions recorded

ADR 0033–0034 (sprint 2.1: vault formats, derived keys, key identifiers and caches; credential model
and platform oversight), ADR 0035–0036 (sprint 2.2: catalogue and model access; prices, costs and
the price importer), with history lines on ADR 0008, 0009, 0011, 0027, 0028 and 0030. See the
[ADR index](../../adr/README.md).

## Carried forward

- Sprint 3.1: the inference route takes endpoint, model check (`catalog.Resolve`) and credential
  (`vault.Opener`) from the catalogue and the vault, removes `skeleton.openai.*`, moves credential
  validation onto catalogue endpoints behind the egress guard with validators per provider, and
  decides `HTTPS_PROXY` for the price download together with provider traffic (2.2 O-5). It also
  confirms the pin rule of a key (2.1 D-6) and what a key scoped to `model:invoke` alone may do
  (2.1 O-4).
- Sprint 3.2: the usage record carries `credential_id` (VLT-15) and a cost computed with
  `pricing.CostOf` at the price version in force at the request time.
- Sprint 4.3: the *credential manager* role from the five credential permissions (AZ-10).
- Epics 5 and 7: handling of several currencies (prices keep the currency of their version and are
  never converted, 2.2 O-6); platform-owned credentials (7.1).
- Epic 6: rotation of the key-encryption key (VLT-10, re-wrap job for the worker); tenant deletion
  destroys the data key, and the wording of ADR 0008 on old backups is corrected together with its
  remedy (2.1 O-8).
- Sprint 8.2: separate processes, so that "the admin plane cannot decrypt" becomes a property of the
  deployment and not only of database grants; external key providers implement `KeyProvider`.
- Epic 10: a strict switch that refuses a key file readable by others (2.1 O-2).
- Not scheduled: cross-tenant search of credentials by hint for incident response (2.1 O-1); the
  importer creating models (2.2 O-2, revisited with the declarative catalogue export of CAT-10).
- Maintainer actions outside the repository: none new. Operators upgrading provide a key file
  (`pandokos kek generate --out FILE`, `vault.kek.local.key_file`) before `serve` starts; see the
  [vault guide](../../operations/vault.md).
