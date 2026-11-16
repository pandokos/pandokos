---
id: EPIC-10
title: Hardening
status: todo
release: v1.0
depends_on: [EPIC-8, EPIC-9]
---

# Epic 10 — Hardening

## Outcome

Version 1.0: measured, reviewed, recoverable, with a stable upgrade policy.

## Sprints

### 10.1 Performance and resilience

- Load-test harness with a mock provider that streams at realistic rates.
- Measurement against the targets of GW-17: added latency, concurrent streams, memory. Profiling
  and fixes; revision of the targets where the evidence says so.
- Failure injection for PostgreSQL, Valkey, the key provider, the identity provider, the collector
  and the provider; behaviour checked against the documented table.
- Published benchmark method and figures.

Requirements: GW-17, OPS-11 (tested).

### 10.2 Independent security assessment

- Independent security assessment, on top of the review of sprint 12.2; fixes for its findings.
- Optional check of authentication-strength claims on sensitive operations.

Requirements: SEC-09, IAM-17.

### 10.3 Documentation and 1.0

- The restore drill and the upgrade test of sprint 12.3, repeated on the release candidate.
- Complete documentation: installation, operations, security, API reference, integration guide.
- Release 1.0.

Requirements: OPS-10 (tested, repeated), OPS-13 (in force).

Exit for the epic: the acceptance criteria of R5 in the requirements.

## Risks

- An independent assessment depends on money or volunteers. If neither is available, the
  release is made with the limitation stated prominently.
