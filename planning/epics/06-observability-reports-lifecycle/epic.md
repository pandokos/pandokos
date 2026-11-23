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
  in 4.3, stay log lines (D-23); 6.4 decides whether they become events.
- Retention settings for usage and audit; partition dropping through the audited function.
- Restriction of accepted metadata keys per tenant.
- `pandokos export` and `pandokos apply` for declarative configuration.

Requirements: USE-06 to USE-08, AUD-04 (tamper evidence), AUD-05, AUD-06, CAT-10, PRV-03.

Exit: a tenant's usage viewer obtains last month's cost by model and cannot see another tenant's;
altering an audit row by hand makes verification fail; `apply` of an exported document is a
no-op.

*Deferred from 6.2 (sprint 6.2 §2):* retention of `limit_event` and `price_import`, and what pseudonymization
of a deleted tenant's records means for its audit chain, are decided in 6.3, on the mechanism of the
retention switch (ADR 0065); platform and tenant token issuers and subject mappings in `export` and
`apply` are sprint 12.3 ("an instance defined by configuration alone"); forwarding audit rows as they
happen is 6.4.

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

*Deferred from 6.3 (sprint 6.3 §2):* a restore that is tested, a restore drill and the loss of the key as a
drill are sprint 12.3; a key-encryption key held by an external service (transit API), after which a deletion
may trigger a rotation by itself, is sprint 12.1; the event "tenant deleted" and the external reference of a
tenant are 6.4; retention of `limit_event` for living tenants is 6.4 (limit events become outbox events and
are retained with them), and `price_import` retention is not planned; failure injection for every dependency
as a suite, and published figures (a deletion of a tenant with 10^7 ledger records, a re-seal of a log of 10^5
rows, a rotation over 10^4 tenants), are sprint 10.1; deleting or anonymising a user the platform manages,
rewriting the platform's chain and scrubbing a deleted tenant's name are the privacy review of epic 10; separate
processes for the worker and the gateway are sprint 8.2.

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

*Carried to 6.4 from 6.3 (sprint 6.3 §11):* the event "tenant deleted" is written by `tenant_erasure_step` in the
transaction of the completion record, with the tenant's ID and the **external reference** that 6.4 adds to a
tenant; the reference must be copied into `tenant_deletion` at the request, because the event is produced after
the tenant's configuration is gone. The outbox table of a tenant is one more table for the erasure steps (the
test that reads the catalogue will ask for it) and needs the `tenant_open` trigger (lint rule L-21 asks for it).
What happens to events of a closed tenant that still wait for delivery (deliver, then remove) is 6.4's decision.
"Deletion requested" and "key rotated" can be platform events. Limit events gain their retention here. The list
of completed deletions that a restore needs can come from these events.

Requirements: EVT-01 to EVT-04, LIM-13.

Exit: across injected crashes and outages, an idempotent receiver ends with exactly one entry per
call; a receiver that stays down loses nothing and delays no inference.

## Risks

- Deletion touches every table and must be resumable. It is written as idempotent steps with
  recorded progress.
- Usage events multiply the rows written per call. The outbox is written in the ledger writer's
  batch, and its cost is measured before the sprint closes.
