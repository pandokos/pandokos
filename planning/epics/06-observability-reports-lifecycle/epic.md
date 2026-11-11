---
id: EPIC-6
title: Observability, reports and lifecycle
status: todo
release: v0.2
depends_on: [EPIC-3]
---

# Epic 6 — Observability, reports and lifecycle

## Outcome

Operators see what the system does, tenants see what they spent and who changed what, and a tenant
can be removed for good.

## Decisions it implements

ADR 0013 (reading, hash chain, retention), 0015, 0019 (export and apply), 0008 (rotation and
erasure).

## Sprints

### 6.1 Metrics and conventions

- OTLP metrics: requests, errors, time to first byte, duration, tokens, cost, limit refusals.
- GenAI attributes on spans and metrics at a pinned convention version, documented.
- Cardinality policy: tenant label off by default, opt-in.
- Prometheus endpoint on the admin listener.
- Redacting log handler; log reference.
- Example dashboards.

Requirements: OBS-01 (metrics), OBS-03, OBS-06, OBS-07.

Exit: the example dashboard shows latency, tokens and cost by model from a collector; with the
collector stopped, inference latency is unchanged.

### 6.2 Reports and audit reading

- Usage rollups maintained by the worker; report API by period, provider, model, caller, funding
  mode; platform aggregates.
- Usage export as newline-delimited JSON and CSV.
- Audit reading API with filters; per-tenant hash chain; `pandokos audit verify`; audit export.
- Retention settings for usage and audit; partition dropping through the audited function.
- Restriction of accepted metadata keys per tenant.
- `pandokos export` and `pandokos apply` for declarative configuration.

Requirements: USE-06 to USE-08, AUD-04 (tamper evidence), AUD-05, AUD-06, CAT-10, PRV-03.

Exit: a tenant's usage viewer obtains last month's cost by model and cannot see another tenant's;
altering an audit row by hand makes verification fail; `apply` of an exported document is a
no-op.

### 6.3 Tenant deletion and key rotation

- Staged tenant deletion run by the worker: suspension, removal of access, destruction of the data
  key, removal of configuration, pseudonymized retention of records, completion record.
- Rotation of the key-encryption key: new version, re-wrap job, retirement of the old version.
- Backup and restore guide, including the key-encryption key; dependency-failure behaviour
  reference.

Requirements: TEN-11, VLT-10, VLT-11, OPS-10 (documented), OPS-11.

Exit: after deletion, a backup taken before it cannot yield the tenant's credentials with the
current keys; after rotation every credential still works and no data key is wrapped by the old
version.

## Risks

- Deletion touches every table and must be resumable. It is written as idempotent steps with
  recorded progress.
