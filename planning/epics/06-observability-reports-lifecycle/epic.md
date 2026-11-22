---
id: EPIC-6
title: Observability, reports and lifecycle
status: in-progress
release: v0.2, v0.3 (sprint 6.4)
depends_on: [EPIC-3]
---

# Epic 6 — Observability, reports and lifecycle

## Outcome

Operators see what the system does, tenants see what they spent and who changed what, a tenant
can be removed for good, and external systems receive what happened as events.

## Decisions it implements

ADR 0013 (reading, hash chain, retention), 0015, 0019 (export and apply), 0008 (rotation and
erasure), 0014.

## Sprints

### 6.1 Metrics and conventions

- OTLP metrics: requests, errors, time to first byte, duration, tokens, cost, limit refusals.
- GenAI attributes on metrics at a pinned convention version, documented; spans keep only the Required attributes (ADR 0055).
- Cardinality policy: no tenant label; cost and tokens exist per installation only (ADR 0054, ADR 0055). A tenant dimension of operational telemetry is issue #52.
- Prometheus endpoint on the admin listener.
- Redacting log handler; log reference; logs and spans hold no tenant data (ADR 0055).
- Example dashboards.
- The provider's own 429 answers counted per provider credential (never as a tenant label), so that
  an operator can see which credential is saturated (issue #32).

Requirements: OBS-01 (metrics), OBS-03, OBS-06, OBS-07.

Exit: the example dashboard shows latency, tokens and cost by model from a collector; with the
collector stopped, inference latency is unchanged.

### 6.2 Reports and audit reading

- Usage rollups maintained by the worker in each tenant's session; report API for the tenant by
  period, provider, model and caller; no platform aggregate per tenant: the platform sees
  operational indicators of the installation, without tenant and cost (ADR 0054). The rollup serves
  the limit counters and the capability check (5.2 §11).
  *Note:* costs are reported as recorded, per currency and price source, never converted or added
  across currencies (5.4 §11).
- Usage export as newline-delimited JSON and CSV.
- Audit reading API with filters; per-tenant hash chain; `pandokos audit verify`; audit export.
  *Note:* `audit:read` is one tenant permission, given to `owner`, `tenant-admin` and `auditor` and not to `read-only`
  (4.3 §11); whether a support action may carry it is decided here by giving it the attribute `Support` or not. The
  read API can filter by `support_action_id`. Refused grants and refused support attempts, logged but not audited
  in 4.3, can become events here.
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
- Tenant data export in a documented format.
- Backup and restore guide, including the key-encryption key; dependency-failure behaviour
  reference.

Requirements: TEN-11, TEN-12, VLT-10, VLT-11, OPS-10 (documented), OPS-11.

Exit: after deletion, a backup taken before it cannot yield the tenant's credentials with the
current keys; after rotation every credential still works and no data key is wrapped by the old
version; a tenant export can be read with the published schema.

### 6.4 Events for external systems — part of v0.3

- Outbox table written in the transaction that records the fact; relay in the worker with
  claiming, backoff, dead-letter state and replay. The outbox row of a limit event is written with
  its `limit_event` row (5.2 §11).
- CloudEvents format and versioned event types: usage recorded, limit threshold reached, limit
  exhausted, tenant deleted. An external reference per tenant, carried by every event.
- Signed webhook destination; pull export of events.
- Administration API and CLI for destinations, delivery state and replay.
- Receiver example that shows idempotent handling.
- Failure tests: worker crash in mid-delivery, receiver outage, duplicate delivery.

Requirements: EVT-01 to EVT-04, LIM-13.

Exit: across injected crashes and outages, an idempotent receiver ends with exactly one entry per
call; a receiver that stays down loses nothing and delays no inference.

## Risks

- Deletion touches every table and must be resumable. It is written as idempotent steps with
  recorded progress.
- Usage events multiply the rows written per call. The outbox is written in the ledger writer's
  batch, and its cost is measured before the sprint closes.
