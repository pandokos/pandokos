---
id: EPIC-12
title: Operability
status: in-progress
release: v0.3
depends_on: [EPIC-6]
---

# Epic 12 — Operability

## Outcome

An operator can run Pandokos for other organizations and answer for it: the key that protects
every tenant's secrets is not on the hosts, the threat model has been checked against the code,
a restore has been done for real, and upgrades follow a stated policy.

## Decisions it implements

ADR 0008 (external provider), 0019, and the review of the threat model of
[security-model.md](../../../docs/architecture/security-model.md).

## Sprints

### 12.1 External key-encryption key

The part with the highest stakes first: a mistake here loses or exposes every credential.

- External key-encryption key provider over a transit API, behind the provider interface of the
  vault.
- Migration from the local key file to the external provider and back, by re-wrapping data keys.
- Behaviour when the provider is slow or unavailable: bounded caches, refusal, readiness.
- Canary tests extended to the provider's requests and errors.

Requirements: VLT-12 (external).

Exit: with the transit provider, no key-encryption key exists on the Pandokos hosts; a restart
during an outage of the provider serves nothing and recovers alone; moving between providers
needs no action by tenants.

*Deferred from 12.1* (rows of its design, section 2, that name a later sprint): a fuzz target for the
response parser of the key service client and the review of boundary 4 against the code (12.2); the
restore drill with a key service, and the figures for it (12.3); Kubernetes authentication to the key
service (8.2); failure injection as a suite and latency figures with a remote key service (10.1).

### 12.2 Threat model review and fuzzing

- Review of the threat model against the code as built, trust boundary by trust boundary; fixes
  and a dated record of the review.
- Continuous fuzzing of the parsers that read from the network.
- Review of the supply-chain controls against what the pipeline actually produces.

Requirements: SEC-05 (reviewed).

Exit: every threat in the model names the control that answers it and the test that proves the
control; the fuzz targets run in CI.

### 12.3 Restore drill and upgrade policy

- Restore drill from backups, including loss of the key-encryption key.
- Upgrade policy and tested upgrade from the previous minor release.
- An instance defined by configuration alone: every setting that distinguishes one installation
  from another (database, key-encryption key, trusted issuers, listeners, telemetry) comes from
  the file or the environment, checked by a test that starts two instances side by side from two
  configurations. *Note from 6.2:* `pandokos export` and `pandokos apply` (declarative documents of the
  platform's catalogue and of a tenant) are built; platform-level and tenant token issuers and subject
  mappings join the document here. The restore drill also verifies both audit chains after the restore
  (`pandokos audit verify`, `platform-audit verify`).

Requirements: OPS-10 (tested), OPS-13, VLT-16 (tested), OPS-08.

Exit: a restore into an empty environment serves calls with the restored credentials; the drill's
record states what cannot be recovered and what the owner must re-enter; the previous minor
release upgrades with traffic.

## Risks

- A transit service differs between vendors. The interface is the small common part (wrap and
  unwrap), and one open source implementation is the tested reference.
- The review is done by the people who wrote the code. The independent assessment of sprint 10.2
  remains the outside check.
