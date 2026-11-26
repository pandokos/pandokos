# Epic 6 — Observability, reports and lifecycle: record

Sprints 6.1 to 6.3 were completed 2026-11-22 and 2026-11-23 and released in
[v0.2.0](../../releases/v0.2.0.md); sprint 6.4 was completed 2026-11-23 for v0.3.0
([release page](../../releases/v0.3.0.md); the draft notes became the page), which closes the epic. Epic file as closed:
[epic.md](epic.md).

## Outcome

**Observability (6.1).** Pandokos exports metrics over OTLP/HTTP to the collector that receives its
traces and, when the operator turns it on, serves them for scraping at `GET /metrics` on the
administration listener to a platform credential holding `metrics:read`. Inference metrics follow
the OpenTelemetry GenAI semantic conventions pinned at v1.41.0; every component named by the earlier
epics reports its indicators (the [metrics reference](../../reference/metrics.md) lists them). No
metric names a tenant, a principal, a key or a request: cost and tokens exist per installation, cost
carries its currency and is never converted, and the one identifier is the provider credential on
the provider-429 counter (issue #32). Every log line passes a redacting handler (sensitive keys and
the shapes of Pandokos secrets) and is listed in the [log reference](../../reference/logs.md), which a
test checks against the code and the output of every process. An observability overlay for Compose
runs a collector, Prometheus and Grafana with two example dashboards; with the collector stopped,
inference latency is unchanged.

**Reports and audit reading (6.2).** The worker maintains a usage rollup per tenant by hour; one
exact reader (rollup for whole hours, ledger for the rest) serves the tenant's reports, the limit
counters and the capability check with the same figures. A tenant reads its usage by day, month or
window in its own calendar, grouped by provider, model, caller and key, split by currency and price
source, never converted or added; it reads its usage against a limit rule and exports its records as
NDJSON or CSV. There is no platform aggregate per tenant. The audit log is sealed in a hash chain per
tenant and for the platform by a database trigger; it is read with filters (including the support
action), exported, and verified client-side by `pandokos audit verify`, which detects altered,
removed, reordered and leading-deleted rows ([audit guide](../../operations/audit.md)). Retention of
usage and audit, off by default, runs through two audited functions. A tenant can restrict the caller
metadata keys it accepts. `pandokos export` and `pandokos apply` keep a platform's or a tenant's
configuration as a YAML document; `apply` of an export is a no-op
([declarative guide](../../operations/declarative.md)).

**Deletion and rotation (6.3).** The platform requests a tenant's deletion with a reason and the
tenant's name; after a grace period (7 days by default) the worker runs resumable, idempotent steps:
suspension, removal of access, destruction of the data key before the credentials, removal of
configuration, pseudonymization of the kept ledger and audit log (the tenant's chain re-sealed from
its anchor), and a completion record in the platform chain that `pandokos tenant deletion verify`
checks. A tenant whose deletion started is closed: every table refuses its rows. Key-encryption key
versions have a state; the configured version is activated at start, the worker re-wraps every data
key and retires a version once nothing is wrapped by it, without any action by tenants. A backup taken
before a deletion yields no credential once the recorded versions are retired and their files
destroyed. A tenant exports its data as NDJSON with a JSON Schema generated from the API
specification ([deletion guide](../../operations/tenant-deletion.md),
[vault](../../operations/vault.md), [tenant export](../../operations/tenant-export.md),
[backup and restore](../../operations/backup-and-restore.md),
[behaviour on failure](../../reference/failure-behaviour.md)).

**Events (6.4).** Usage records, limit thresholds and exhaustion are written as events by database
triggers in the transaction of the fact, in a per-tenant log with an exact sequence; the platform's
log carries deletion requested, cancelled and completed and key versions activated and retired.
Events are CloudEvents 1.0 with versioned types and nine published JSON Schemas, and carry the
tenant's external reference, set by the platform. A tenant registers up to eight signed webhook
destinations (Standard Webhooks headers, a server-generated secret shown once, rotation with an
overlap); the relay in the worker claims destinations with leases, follows a cursor per destination,
backs off with jitter, dead-letters, and supports replay and rewind. Events are also pulled with
`pandokos event export` and `pandokos platform-event export`. Delivery is at least once; events are
kept for `events.retention`, and an undelivered event that expires is counted. An example receiver in
`examples/webhook-receiver` verifies the signature and handles events idempotently; across injected
crashes, duplicates and outages it ends with exactly one entry per call, and a receiver that stays
down delays no inference ([events guide](../../operations/events.md),
[event reference](../../reference/events.md)).

## Sprints

| Sprint | Design and plan | Delivered | Review |
|---|---|---|---|
| 6.1 Metrics and conventions | [sprint](6.1-metrics-and-conventions/sprint.md), [plan](6.1-metrics-and-conventions/plan.md) | [#57](https://github.com/pandokos/pandokos/pull/57), merged as 5e87bfd | 3 rounds: round 1, 1 major (the dashboard exit test T-931 was vacuous because of `or on() vector(0)`), 6 minor and 3 nit; round 2, 2 nit; round 3, none. `make check` also found `test/sdk` needing `go mod tidy` |
| 6.2 Reports and audit reading | [sprint](6.2-reports-and-audit-reading/sprint.md), [plan](6.2-reports-and-audit-reading/plan.md) | [#67](https://github.com/pandokos/pandokos/pull/67), merged as ead8b6c | 2 rounds: round 1, 1 major (`audit verify` accepted a chain whose oldest rows were deleted), 3 minor and 3 nit, plus an order-dependent test found by `make check`; round 2, none. Two defects of part A were found by the tests and fixed |
| 6.3 Tenant deletion and key rotation | [sprint](6.3-tenant-deletion-and-key-rotation/sprint.md), [plan](6.3-tenant-deletion-and-key-rotation/plan.md) | [#68](https://github.com/pandokos/pandokos/pull/68), merged as bda34bf | 3 rounds: round 1, 6 minor (notably a runtime role setting the erasure setting could write through older definer functions; a ledger row committed across the closing could stay unpseudonymized) and 4 nit; round 2, 1 minor (the same class of hole in the price-version guard); round 3, none. The fixes changed the unreleased migration 0016 and added lint rule L-22 |
| 6.4 Events for external systems | [sprint](6.4-events-for-external-systems/sprint.md), [plan](6.4-events-for-external-systems/plan.md) | [#73](https://github.com/pandokos/pandokos/pull/73) | 3 rounds: round 1, 7 minor (last status erased by a round that sent only the test event; a second test request completed with the first one's outcome; signing keys not cleared; docs on sequence gaps; a slow receiver consuming other rounds' deadline; a misleading relay-recovered line; a stale `tenantref` fallback); round 2, 1 regression in a query test; round 3, none |

Each sprint followed the cycle of the earlier epics: a frozen design, tests committed failing before
the implementation, a review against the design repeated until it reported no findings, a pull
request with green CI. Open points raised by each design and the maintainer's decisions on them are
at the end of section 12 of each `sprint.md`; changes made during implementation and after review
are recorded there too (6.3 and 6.4) and in the pull requests.

## Decisions recorded

ADR 0062 (metrics: instruments, attributes, cardinality and the scrape endpoint) and 0063 (the log:
redaction and the log reference), sprint 6.1; ADR 0064 (usage rollup, reports and exports), 0065
(the audit hash chain, reading and retention, amending 0013) and 0066 (declarative export and
apply), sprint 6.2; ADR 0067 (tenant deletion: stages, closed tenants and kept records), 0068
(key-encryption key versions, rotation and erasure in backups, amending 0008 and 0033) and 0069 (the
tenant data export and its schema), sprint 6.3 — all accepted. ADR 0070 (the outbox as a log: relay
by destination and retention, amending 0014), 0071 (webhook destinations: the signature, the signing
secrets and egress) and 0072 (event types, the envelope, the external reference and the platform's
events), sprint 6.4, were accepted by the maintainer on 2026-11-24. History lines were added to the ADRs
each sprint touched, among them 0015, 0018, 0019, 0040, 0054, 0055 and 0060. See the
[ADR index](../../adr/README.md).

## Carried forward

- **Scraper credential (6.1).** Platform keys carry no scopes, so the `platform-auditor` key of a
  scraper also reads tenants, users, platform keys, issuers and support actions; platform-key scopes
  or a scraper role holding `metrics:read` alone is to be scheduled before the credential is widely
  distributed.
- **Issue #52.** A tenant dimension of operational telemetry, if designed, is a new attribute with
  its own ADR; the closed attribute lists and their tests change on purpose.
- **A new event type** (6.4 §11) is a trigger or a call of the append function in the transaction of
  its fact, a schema, a row of the reference and the type in the check of `event_destination`. Not
  events in 6.4, by decision (O-7): `limit.unlimited_traffic`, a change of active currency, refused
  grants and refused support attempts (log lines, 6.2 D-23) and audit rows; audit rows as events
  (`audit.recorded`, the "forwarded" of AUD-06 with `seq` and `hash`) must lock the chain before the
  stream.
- **Epic 8 (scale-out).** Each instance exports its own series and dashboards sum over instances;
  shared counters (8.1) add store latency and failures to the catalogue. Several workers already
  share the rollup, retention, deletion, re-wrap and relay (state rows, advisory and row locks,
  leases). Sprint 8.2 (separate processes): the gateway process registers the gateway, ledger,
  egress and limiter metric sources; the admin process the people, support and grant sources, its
  own scrape endpoint, the reports and the streamed exports (the ingress must not buffer them); the
  worker process runs the rollup, retention, deletion, re-wrap, relay and janitor and needs the key
  files and the egress settings; activation of a key version belongs to the admin or worker process;
  the chart exposes `telemetry.metrics.*`, a `ServiceMonitor` example and `unwrap_key_files`.
- **Sprint 10.1 (load and resilience).** The allocation figure of the metrics path and the 105
  allocations of the budget path; the seal trigger under concurrent administration; the rollup at the
  target traffic and report latency on a year of data; the volume of `rejected_limit` rows under a
  retry storm; the statement trigger of the ledger's insert path and the outbox trigger under
  concurrent writers of one tenant; a deletion of a tenant with 10⁷ ledger records, a re-seal of 10⁵
  audit rows, a rotation over 10⁴ tenants; an event log of 10⁷ events with retention (or a log
  partitioned by day), the claim over 10⁴ destinations and a receiver slower than the traffic; pass
  gating of the relay (the next claim waits for the slowest round). Failure injection for every
  dependency uses the [failure reference](../../reference/failure-behaviour.md) as its list.
- **Sprint 12.1 (external key service).** A transit provider maps its key versions onto `kek_version`;
  destroying a key file becomes deleting a version, after which a deletion may trigger a rotation by
  itself; opening signing secrets needs the data key cache of the opener; a secret store of the
  platform would allow a platform webhook (6.4 O-5).
- **Sprint 12.3 (restore drill, configuration alone).** A tested restore and the loss of the key as
  drills, following the backup guide as written and replaying the deletions listed by
  `tenant.deleted`; both audit chains verified after restore; `apply` gains platform and tenant token
  issuers, subject mappings, event destinations (without secrets) and the external reference.
- **Epic 10 (hardening).** The privacy review decides on deleting or anonymising a user the platform
  manages, rewriting the platform's chain and scrubbing a deleted tenant's name; the security
  assessment reviews the erasure functions and the two settings.
- **Epic 9 (providers).** A new family adds its GenAI operation names (`generate_content` for
  Gemini). A move of the GenAI conventions past v1.41.0 needs a source other than the Go semconv
  package, which removed them in v1.42.0.
- **Not planned:** retention of `price_import`; per-tenant retention; `apply --prune`; destinations
  in the tenant data export (an additive file of the format if wanted); a PostgreSQL major-version
  move keeps the chain verifiable only while `jsonb` writes numbers as before (checked by the 12.3
  drill).
- **Release v0.3.0.** The notes announce the five event permissions, the nine `events.*` keys, the
  twelve operations and the commands, migration 0017 (events start with it), the cost on the
  ledger's insert and the bound of EVT-03.

## Maintainer actions

- Migrations of this epic: `0015_reports_audit` (6.2: seals every existing audit row, builds an index
  over the ledger; on a large ledger build the index beforehand as the
  [database guide](../../operations/database.md#the-index-of-migration-0015-on-a-large-ledger) says)
  and `0016_tenant_deletion_kek_rotation` (6.3: not compatible with earlier binaries) are part of
  v0.2.0 and are applied with every instance stopped: stop, `pandokos migrate`, start the new binary.
  `0017_events` (6.4, v0.3.0) is applied by `pandokos migrate` before the new binary starts; events
  start with it and nothing is produced for the past. Sprint 6.1 has no migration.
- ADR 0070, 0071 and 0072: accepted on 2026-11-24.
- Drills of sprint 6.4 not run in the sandbox of the implementation (plan F2 and F3): the manual
  `kill -9` of the binary in mid-delivery, the ten-minute receiver outage, and the upgrade drill from
  a v0.2.0 database.
- The cost of the outbox on the CI machine (plan F4): the development machine measured a batch of 500
  usage records at a median of 108.7 ms without the trigger and 145.5 ms with it (ratio 1.34,
  `TestOutboxWriteCost`); record the CI figure in the [events guide](../../operations/events.md).
- Rotation of the key-encryption key is by configuration and restart ([vault](../../operations/vault.md));
  erasure in backups is complete only once the recorded key versions are retired and their files
  destroyed ([backup and restore](../../operations/backup-and-restore.md)).
- Protect the scraper's `platform-auditor` key as an auditor credential (see Carried forward).
- The container, compose, browser, IdP and Python and TypeScript SDK jobs run only in CI, not in the
  sandbox of the implementation.
