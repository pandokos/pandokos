---
id: EPIC-2
title: Vault and catalogue
status: todo
release: v0.1
depends_on: [EPIC-1]
---

# Epic 2 — Vault and catalogue

## Outcome

A tenant stores provider credentials that nobody can read back, and the platform admin describes
which providers, endpoints and models exist, which tenants may use them and what they cost.

## Decisions it implements

ADR 0008, 0011, and the catalogue part of 0009.

## Sprints

### 2.1 Vault

- `KeyProvider` interface and the local-file provider, with key versions.
- Tenant data key created with the tenant, stored wrapped.
- Credentials: create, replace, disable, revoke, delete; labels; one default per provider;
  fingerprint; metadata-only reads.
- Encryption with associated data that binds tenant, credential and key version.
- Column privileges and the metadata view for platform-scope sessions.
- Optional validation of a credential against the provider at write time.
- Gateway-side decryption with bounded caches for data keys and credentials; invalidation on
  change; documented and tested revocation bound.
- Canary-secret tests across logs, errors, traces and API responses.
- Operations notes: protecting and backing up the key-encryption key; what its loss means.

Requirements: VLT-01 to VLT-09, VLT-12 (local), VLT-13 to VLT-15, VLT-16 (draft), AZ-08.

Exit: no API path and no runtime database role acting at platform scope returns a secret or its
ciphertext; a ciphertext copied to another tenant's row fails to decrypt; the canary suite finds
nothing.

### 2.2 Catalogue and prices

- Providers, API families, endpoints with an enabled flag; private endpoints marked explicitly.
- Models: public name, upstream identifier, capabilities, status, limits.
- Grants: a model available to all tenants or to selected ones; tenant-level and key-level
  restriction.
- Price versions; cost calculation with decimal arithmetic; unknown-price behaviour.
- Importer for the open price dataset: embedded snapshot, CLI import, optional periodic refresh by
  the worker; operator overrides.
- Catalogue cache in the gateway with invalidation.
- Seed data for OpenAI and Anthropic.

Requirements: CAT-01, CAT-02 (tier 1 data), CAT-03 (platform endpoints), CAT-04 to CAT-06, CAT-08,
CAT-09.

Exit: a model is visible to one tenant and not to another; a cost computed for a past timestamp
uses the price version valid then; an import that changes a price adds a version and an audit
record.

## Risks

- The format of the external price dataset may not map cleanly to every pricing scheme (tiers,
  time-based prices). The importer skips what it cannot represent and reports it; operators can
  override.
