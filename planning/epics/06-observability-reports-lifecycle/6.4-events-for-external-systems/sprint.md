---
id: SPRINT-6.4
title: Events for external systems
epic: EPIC-6
release: v0.3
status: in-progress
design: draft 2026-11-23
---

# Sprint 6.4 — Events for external systems

- Epic: [6 — Observability, reports and lifecycle](../epic.md)
- Implementation plan: [plan.md](plan.md)
- Previous sprint: [6.3 — Tenant deletion and key rotation](../6.3-tenant-deletion-and-key-rotation/sprint.md)
- Scope: the epic file, section 6.4, with the paragraph "Carried to 6.4 from 6.3", the items carried
  to 6.4 by the closed epics ([epic 3](../../../../docs/history/epic-03-gateway/README.md),
  [epic 5](../../../../docs/history/epic-05-limits-and-budgets/README.md)), section 11 of sprints 5.2,
  6.2 and 6.3, and the maintainer decisions O-12 of 6.2 and O-12 of 6.3.
- Risk class: **high** (a trigger on the insert path of the ledger; a lock that orders every event
  of a tenant; claiming by several workers; signing secrets; outbound requests to URLs that tenants
  supply; five more tables for the erasure of a tenant; seven new tables and their isolation)
- Decisions implemented: [ADR 0014](../../../../docs/adr/0014-events-outbox.md) (the base); 0038 (webhooks
  leave through the egress guard, public class); 0039 (the event joins the writer's transaction);
  0008, 0033 and 0068 (signing secrets under the data key); 0054 and 0055 (events of usage go to the
  tenant only; telemetry names no tenant); 0052 (limit events get their outbox row); 0065 (the
  retention switch); 0066; 0067 (more tables for the steps, `tenant_open`, the event "tenant
  deleted"); 0017 and 0028; 0020 (no new code); 0027; 0059; 0060 and 0061; 0062 (seven instruments);
  0063 (six lines, one shape); 0019 (nine keys). The records are in [docs/adr](../../../../docs/adr/README.md).
- Decision records changed by this sprint: new **ADR 0070** (the outbox as a log per tenant, its
  order, the relay by destination, dead letters, replay, retention; amends the "Relay" paragraph of
  ADR 0014), **ADR 0071** (webhook destinations: signature, secrets, egress) and **ADR 0072** (the
  event types, the envelope, the external reference, the platform's events); history lines on 0014,
  0033, 0038, 0039, 0052, 0054, 0062, 0063, 0065, 0066 and 0067. Text in [plan.md](plan.md), part E.

This document is the design. The implementer follows [plan.md](plan.md); a gap in either document is
raised as a question, not filled silently ([planning/README.md](../../../README.md)).

| Given by the epic and the closed sprints | Designed in |
|---|---|
| Outbox table written in the transaction that records the fact; the usage event in the writer's transaction (epic 3); the outbox row of a limit event with its `limit_event` row (5.2 §11) | §5.1, §5.2, D-1, D-2 |
| Relay in the worker with claiming, backoff, dead-letter state and replay; several workers (epic 8) | §5.6 to §5.8, D-9 to D-11 |
| CloudEvents and versioned types: usage recorded, limit threshold reached, limit exhausted, tenant deleted; kinds mapped to the types of ADR 0014 | §5.3, D-3, D-4 |
| Does a usage event carry the limits it counted against (epic 5) | D-4: no; it carries `limit_state` |
| An external reference per tenant, carried by every event; copied into `tenant_deletion` at the request | §5.4, D-6 |
| A change of active currency as an event; `limit.unlimited_traffic` as an event (epic 5); refused grants and refused support attempts (6.2 O-12); forwarding audit rows (6.2, deferred) | D-5: none becomes an event here; O-7 |
| Signed webhook destination; pull export of events | §5.5, §5.9, §5.10, D-8, D-12 to D-15 |
| Administration API and CLI for destinations, delivery state and replay | §5.11, §5.12, D-19 |
| The event "tenant deleted" written by `tenant_erasure_step` in the transaction of the completion record; "deletion requested" and "key rotated" as platform events; the list of completed deletions that a restore needs | §5.4, D-7 |
| The outbox table is one more table for the erasure steps and needs `tenant_open`; events of a closed tenant that wait for delivery | §5.14, D-17, O-4 |
| Limit events gain their retention (6.3 O-12) | §5.13, D-16 |
| Receiver example that shows idempotent handling | §5.16, D-24 |
| Failure tests: worker crash in mid-delivery, receiver outage, duplicate delivery | T-1032, T-1052, T-1053 |
| The cost of writing the outbox in the ledger writer's batch, measured before the sprint closes (epic risk) | §5.2, D-25, T-1050 |
| Exit: across injected crashes and outages, an idempotent receiver ends with exactly one entry per call | T-1052 |
| Exit: a receiver that stays down loses nothing and delays no inference | T-1053 |

## 1. Outcome

1. Every usage record and every limit event of a tenant is also an **event in the tenant's event
   log**, written by the database in the transaction that records the fact. No code path can record
   one without the other.
2. A tenant registers **webhook destinations**. The worker delivers each event to each destination
   as a CloudEvents 1.0 JSON document, signed with a secret that the tenant saw once, **at least
   once**, with retries, a dead-letter state and replay. A destination that fails holds back only
   its own queue; nothing on the path of an inference request waits for it.
3. A tenant, or a system that prefers to pull, **reads the same log** as newline-delimited JSON by a
   cursor that never misses an event.
4. Every event carries the tenant and its **external reference**, a new attribute of a tenant that
   the platform sets.
5. The platform has an **event log of its own**: a deletion requested, cancelled and completed, a
   key-encryption key version activated and retired. It is read by pull, and it is the list of
   completed deletions that a restore needs.
6. Events are removed by retention; limit events get theirs; a deleted tenant's destinations and
   events go with it.
7. A **receiver example** verifies signatures and handles duplicates; the end-to-end tests use it
   to hold the exit criteria.

## 2. Scope

### In scope

- Migration `0017_events` (plan appendix A1, run on PostgreSQL 16.15 while designing) and the
  statements of plan A2.
- New package `internal/outbox` (envelope, signature, delivery, relay, janitor, schemas) and the
  directory `examples/webhook-receiver`.
- `internal/vault` (sealing and opening a named secret), `internal/admin` (twelve operations and the
  external reference), `internal/authz` (five permissions), `internal/cli`, `internal/config` (nine
  keys), `internal/telemetry` (seven instruments, one redaction shape), `cmd/pandokos` (wiring).
- Tests at every level, the guide and references of plan part E, ADR 0070 to 0072, the release notes
  draft of v0.3.0.

### Not in scope

| Item | Where |
|---|---|
| A webhook destination of the platform (for the platform's events) | not planned here: the vault holds no secret of the platform; pull export serves them (D-7, O-5); revisit with sprint 12.1 |
| Destinations other than a webhook (a broker, a vendor adapter, a file drop) | ADR 0014: can be added without changing the outbox |
| Events for `limit.unlimited_traffic`, a change of active currency, refused grants and refused support attempts; forwarding audit rows as events | D-5, O-7: not events; the audit export of 6.2 serves the audit log |
| The limits a usage record counted against, in its event | D-4: the ledger does not record them |
| Batched delivery (several events in one request), ordering guarantees across retries | not planned; ADR 0014 promises no order |
| Destinations and the external reference in `pandokos export` and `apply`; destinations in the tenant data export | D-22, O-12; sprint 12.3 for the first |
| An event log partitioned by time; figures under load (10⁷ events, a relay over 10⁴ destinations) | sprint 10.1 |
| Several worker processes, and the relay in a process of its own | sprint 8.2; the claim is ready for it (T-1033) |
| Retention of `platform_outbox_event` | not planned: a few rows per deletion and per rotation (D-16) |
| Automatic disabling of a destination that keeps failing | not planned; its state and its expired events are shown (§5.8) |

## 3. Requirements claimed

The meaning is quoted from [docs/requirements](../../../../docs/requirements/README.md).

| ID | Meaning | Delivered here | Extent |
|---|---|---|---|
| EVT-01 | "Usage is published as events for external systems, one per usage record, with no content" | The trigger `usage_record_outbox` (§5.2); the type `io.pandokos.usage.recorded.v1` (§5.3) | full |
| EVT-02 | "An event has a stable unique ID usable as idempotency key, the tenant and its external reference, quantities, estimated cost, currency, price version and the originating request" | The envelope and the data of §5.3; the ID is the ID of the usage record | full |
| EVT-03 | "Delivery is asynchronous and at least once, with retries, a dead-letter state and replay. No event is lost when Pandokos or the destination fails, and a failing destination never delays inference" | The relay of §5.6 to §5.8; "no event is lost" holds while the event is in the log, that is for `events.retention` (D-16, O-3) | full, with that bound |
| EVT-04 | "Destinations are generic: a signed webhook and a file export, both in a documented open format" | §5.5, §5.9 (webhook), §5.10 (export); the format is CloudEvents 1.0 with published JSON Schemas (§5.3) | full |
| LIM-13 | "Threshold and exhaustion events can be delivered to external systems (EVT-03, EVT-04)" | The trigger `limit_event_outbox` and the two limit types | full |
| UC-13 | "Usage is delivered to an external system, for example a billing system" | T-1052 | full |
| TEN-11 | "Deletion is a staged process … It never touches global data or other tenants" | Touched: three more tables in the steps; the event "tenant deleted" | touched |
| USE-07 | "… Platform roles read no usage of a tenant …" | Touched: no platform operation, function or event of the platform returns an event of a tenant or a count of them | touched |
| AUD-01 | "Pandokos audits … every creation, change, disabling, revocation and deletion of …" | Touched: six actions on destinations; the external reference in `tenant.update` | touched |
| GW-06 | (egress address checks) | Touched: webhook requests go through the guard | touched |

## 4. Repository layout

New: `internal/store/migrations/sql/0017_events.sql`, `internal/store/queries/sql/outbox.sql`,
`internal/outbox/` (`doc.go`, `event.go`, `sign.go`, `deliver.go`, `relay.go`, `janitor.go`,
`schema.go`, `schema/*.schema.json`), `internal/vault/named.go`,
`internal/admin/{event_destinations,events}.go`, `internal/cli/events.go`,
`internal/store/migrations/upgrade_0017_db_test.go`, `test/isolation/outbox_test.go`,
`test/e2e/events_test.go`, `test/sdk/events_test.go`, `test/repo/events_test.go`,
`examples/webhook-receiver/{main.go,receiver/receiver.go,receiver/receiver_test.go,README.md}`,
`docs/operations/events.md`, `docs/reference/events.md`, `docs/reference/events/*.schema.json`,
`docs/adr/0070-…`, `0071-…`, `0072-…` (names in plan E2), `docs/releases/v0.3.0-draft.md`.

Changed: `internal/store/queries/sql/{tenants,lifecycle}.sql` and the generated package,
`internal/store/migrations/migrations.sum`, `api/openapi/admin.v1.yaml`, `internal/adminapi`
(generated), `internal/authz/{authz,roles}.go` and `testdata/roles.golden`,
`internal/admin/{tenants,tenant_deletion,convert,problem}.go`, `internal/config/{config,keys,validate}.go`,
`internal/telemetry/{instruments,sources,redact}.go`, `cmd/pandokos/{serve,metrics,main}.go`,
`internal/cli/{cli,commands}.go`, `test/isolation/{spec,lint}.go`, `test/dbtest`, `test/e2e` helpers,
`Makefile` (`generate`, `bench`), the tests of §9 "Changed tests", the documents of plan part E.

## 5. Architecture

### 5.1 The event log of a tenant (D-1, D-2)

`outbox_event` is tenant-scoped (policy `tenant_isolation`) and append-only like the ledger: a row
is an event, with `seq`, `id`, `type`, `subject`, `occurred_at`, `recorded_at` and `data`. It is
written only by two triggers (§5.2); no runtime role holds `INSERT`, `UPDATE` or `DELETE` on it. The
admin role reads it for the export, the worker for the relay, the gateway not at all.

**Order.** `seq` comes from `outbox_stream`, one row per tenant: the first event of a transaction
locks that row (`INSERT … ON CONFLICT DO UPDATE`) and the lock is held until the transaction ends.
So `seq` grows in the order of the commits: when a reader sees `seq = n`, every event with a
smaller number is already visible to it, and "every event after `n`" is `seq > n`, exactly. A
transaction that rolls back leaves a gap; the sequence is increasing, not gapless. The lock is per
tenant, taken once per statement of the ledger writer (one batch), and held for the rest of that
transaction, which is that statement.

**Lock order.** A transaction that writes a tenant's audit chain and its event log would have to
take the two locks in one order. No transaction of this sprint does: the two sources of tenant
events (a ledger batch, a limit event) write no audit row. A later event type that does must lock
the chain first (§11). The platform's log, which is written in transactions that also write the
platform's chain, locks the chain first (§5.4).

### 5.2 The facts that are events (D-1, D-25)

| Fact | Trigger | Events |
|---|---|---|
| A statement that inserts usage records (the batch of the ledger writer) | `usage_record_outbox`, `AFTER INSERT … REFERENCING NEW TABLE … FOR EACH STATEMENT` | one `io.pandokos.usage.recorded.v1` per record actually inserted (a record left out by `ON CONFLICT DO NOTHING` has no event: the first insert wrote it); one reservation of sequence numbers and one insert per statement |
| A row of `limit_event` | `limit_event_outbox`, `AFTER INSERT … FOR EACH ROW` | `io.pandokos.limit.threshold_reached.v1` or `io.pandokos.limit.exhausted.v1`, by `kind` |

Both functions are `SECURITY DEFINER`, start with `definer_settings_off()` (L-22) and write under
the policy of the tenant of the rows. `internal/ledger` and `limits.EventWriter` do not change: the
"transaction of the writer" is the statement they already send. The guarantee is structural: a
usage record without its event cannot be written by any role through `pandokos.usage_record`
(L-23 keeps the two triggers present and enabled). A superuser, whom no policy binds, may insert
without a tenant session (the fixtures and an operator's repair do): the trigger then writes each
tenant's log in a session of that tenant and puts the session back.

A closed tenant (6.3 §5.2) takes no event: `outbox_event` and `outbox_stream` have the
`tenant_open` trigger, and the usage record of such a statement is refused with it, as in 6.3.

**Cost.** The event doubles the rows of a batch and adds one row lock per batch. Measured while
designing (PostgreSQL 16.15 on the design machine, one new session per measurement, a batch of 500
records with realistic columns, the final text of the migration): 26 ms without the trigger, 45 ms
with it; a single record 4.2 ms and 7.2 ms. A first version that built the data in a SQL function
with a `search_path`, called once per row, took 83 ms against 30 ms in its own run: the expression
is therefore written in the trigger's statement. The figure of record is produced by
T-1050 and `BenchmarkLedgerBatchOutbox` on the CI machine and written in `docs/operations/events.md`
and in the pull request (plan F4); T-1050 fails above three times the cost without the trigger.

### 5.3 The format: CloudEvents 1.0, structured JSON (D-3, D-4)

A webhook request body and a line of an export are the same document:

```json
{"specversion":"1.0","id":"01a2096c-074d-750b-9e85-c37222e3b9c9","source":"urn:pandokos",
 "type":"io.pandokos.usage.recorded.v1","time":"2026-11-23T17:20:04.426849Z",
 "subject":"eccbc87e4b5ce2fe28308fd9f2a7baf3","datacontenttype":"application/json",
 "tenantid":"01a2096c-0745-7db9-b65e-3f2f525e04af","tenantref":"cust-42","sequence":"1","data":{…}}
```

| Attribute | Value |
|---|---|
| `id` | stable, the receiver's idempotency key: the ID of the usage record, of the `limit_event` row, or of the platform event |
| `source` | `events.source` (default `urn:pandokos`): one value per installation |
| `type` | the table below; a breaking change of `data` is a new version, delivered beside the old one |
| `time` | when the fact was recorded (`recorded_at` of the usage record, `occurred_at` of the limit event), UTC, microseconds |
| `subject` | the request ID (usage), the rule ID (limits), the tenant ID or the key version (platform) |
| `tenantid`, `tenantref` | extensions: the tenant, and its external reference as it is when the document is rendered; `tenantref` is absent when empty; both absent on the two `kek` types |
| `sequence` | extension (a string of digits): `seq` in the tenant's log, or in the platform's; absent on a test event |
| `data` | stored with the event, never changed |

`data` is built by the database when the event is written; the envelope is rendered by
`internal/outbox` when the event is delivered or exported, with members in the order above.

| Type | Log | `data` (members with no value are absent) |
|---|---|---|
| `io.pandokos.usage.recorded.v1` | tenant | `record_id`, `request_id`, `trace_id`, `started_at`, `duration_ms`, `principal` {`kind`, `id`}, `key_id`, `provider`, `api_family`, `operation`, `model`, `upstream_model`, `stream`, `outcome`, `error_code`, `usage` {`input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`, `reasoning_tokens`, `input_source`, `output_source`, `other_units`}, `cost` {`status`, `reason`, `amount` (decimal string), `currency`, `price_version_id`, `price_source`, `fallbacks`}, `limit_state`, `metadata` |
| `io.pandokos.limit.threshold_reached.v1`, `io.pandokos.limit.exhausted.v1` | tenant | `limit_event_id`, `rule_id`, `rule_level`, `subject` {`kind`, `id`}, `provider`, `model`, `metric`, `currency`, `period` {`start`, `end`}, `percent` (threshold only), `limit` (requests, tokens) or `amount` (decimal string, cost), `observed` (decimal string) |
| `io.pandokos.tenant.deletion_requested.v1`, `…deletion_cancelled.v1` | platform | `tenant_id`, `external_reference`, `requested_at`, `not_before` (requested only) |
| `io.pandokos.tenant.deleted.v1` | platform | `tenant_id`, `external_reference`, `requested_at`, `started_at`, `completed_at`, `kek_versions`, `completion_seq` (the completion record in the platform's audit chain) |
| `io.pandokos.kek.version_activated.v1`, `…version_retired.v1` | platform | `version`, `provider` |
| `io.pandokos.destination.test.v1` | none (§5.11) | `destination_id`, `name` |

- **No content** (EVT-01): the ledger holds none. The provider's own usage object (`provider_usage`)
  is left out; the caller's attribution metadata (GW-13) is in, because the event goes to the
  tenant that attached it (O-8).
- **The cost is the estimate of the ledger**, as recorded, in its currency, never converted (ADR
  0014, 5.4 §11).
- **The limits a record counted against are not in the event**: the ledger does not record them
  (rules are matched by attribution when they are read, 5.2). The event carries `limit_state`, how
  limits treated the request, as the record does.
- A JSON Schema (2020-12) of each type, envelope included, is kept in `internal/outbox/schema/`,
  embedded, copied to `docs/reference/events/` by `make generate`, and printed by
  `pandokos event schema [type]` (D-23). No `dataschema` attribute is sent: the project publishes
  no URL for it yet.

### 5.4 The external reference and the platform's events (D-6, D-7)

**External reference.** `tenant.external_reference`: up to 255 characters, no control character,
empty by default, not unique (two tenants may be one customer). It is what the operator's other
systems call the tenant. The platform sets it (`tenant:write`, in `TenantCreate` and
`TenantUpdate`); a database trigger refuses a change outside the platform scope. It is returned in
`Tenant`, audited in `tenant.create` and `tenant.update`, carried by every event as `tenantref`,
and copied by `tenant_deletion_guard` into `tenant_deletion.external_reference` when a deletion is
requested (6.3 §11): the events of the deletion use that copy.

**The platform's log.** `platform_outbox_event` is a restricted global table, append-only, read by
the admin role in platform scope and written only by `platform_outbox_append`, which three triggers
call:

| Trigger | When | Event |
|---|---|---|
| `tenant_deletion_outbox` on `tenant_deletion` | a row is inserted (the request); a scheduled row is deleted (the cancel); `state` becomes `completed` — the statement of `tenant_erasure_step` that follows the completion record, in its transaction | `tenant.deletion_requested`, `tenant.deletion_cancelled`, `tenant.deleted` |
| `kek_version_outbox` on `kek_version` | a version becomes `active`; a version becomes `retired` | `kek.version_activated`, `kek.version_retired` |

`platform_outbox_append` locks the head of the platform's audit chain before the row of
`platform_outbox_stream`, so every transaction takes the two in one order. These events hold no
figure of usage and name no person. They have **no webhook**: a signing secret of the platform has
no place in a vault that seals under tenant data keys. They are pulled (§5.10) and are not removed
by retention. A restore replays the deletions listed by `tenant.deleted` events after the backup
(the backup guide gains the command, plan E1).

### 5.5 Destinations (D-8)

`event_destination` is tenant-scoped: `name` (unique in the tenant), `description`, `url`,
`event_types` (empty: every type of the tenant's log), `enabled`, the sealed signing secret and,
during a rotation, the previous one with its expiry, and the state of delivery (§5.6). A tenant has
at most 8 (a trigger under an advisory lock). A destination starts at the head of the log: it
receives the events recorded after it was created.

The URL is `https`, at most 2048 characters, without user information or fragment; `http` only for
a host listed in `events.webhook.private_hosts`. It is configuration, not a secret: every holder of
`event_destination:read` reads it, so the guide says to put no token in it; the signature is what
authenticates a delivery. An address literal that the guard would refuse is refused when the
destination is written.

`event_destination_directory` (global, identifiers only, no privilege for any runtime role, like the
directories of 2.1 and 4.1) lists which tenants have destinations: it is what the claim walks.

### 5.6 The relay (D-9)

ADR 0014 let the worker claim pending *events*. This sprint claims *destinations*: the log is the
queue, and each destination has a cursor in it. A delivery that succeeds writes no row per event;
an outage writes nothing at all. ADR 0070 records the change; the guarantees of ADR 0014 are kept.

`cursor_seq` of a destination is the event up to which everything is **settled** for it:
delivered, parked as a dead letter, not of its types, or removed by retention.

One pass of `outbox.Relay` (worker role), every `events.relay.interval`, and again at once while a
pass found a full batch:

1. **Claim.** `outbox_relay_claim(token, lease_seconds, limit)` in platform scope: for each tenant
   of the directory, in random order, the destinations that are not leased and either have a test
   request waiting (§5.11) or are enabled, due (`next_attempt_at`) and have an event after their
   cursor or a dead letter to replay, are locked `FOR UPDATE SKIP LOCKED` and given the caller's
   lease. It answers tenant and destination IDs, nothing
   else. Workers never wait for each other, and a destination has one holder at a time.
2. **Read**, in the tenant's session, one transaction: the destination (only if it still holds the
   token), the tenant's external reference, the head of the log, then the dead letters to replay
   and up to `events.relay.batch_size` events after the cursor of the destination's types; the
   secrets are opened (§5.9).
3. **Deliver**, with no transaction open: a waiting test request; then, for a destination that is
   enabled and due, replays (one attempt each) and, if no failure is pending, the batch with up to
   4 requests at a time, otherwise the head event alone. No new request starts after the first failure in sequence order, or after half the lease.
4. **Settle**, one transaction in the tenant's session, conditional on the token: the cursor moves
   to the last event before the first failure (to the head of the log when the batch was not full
   and nothing failed); the failure state of §5.7 is written; dead letters are written; the lease
   is released. A settle that finds another token (the lease expired and somebody else holds the
   destination, or the administration moved the cursor) changes nothing.

The lease is `max(60 s, 6 × events.delivery.timeout)`. **A worker that dies between 3 and 4** leaves
the lease to expire; the next claim delivers the same events again. That is the at-least-once
guarantee, and the reason the event ID is the receiver's idempotency key. Within a destination,
events are sent in order in the normal case; order is not guaranteed (a batch is sent in parallel,
and a replay is out of order by nature).

The relay reads the configuration at every round: a change of URL, types, secret or `enabled`
needs no cache and no notification. It runs for tenants that are `active` or `suspended` (a
suspended tenant's pending events are still delivered), and holds no statement that writes
`outbox_event`.

### 5.7 Failure, backoff and dead letters (D-10)

A delivery succeeds on a `2xx` answer and fails otherwise:

| Outcome | When |
|---|---|
| `delivered` | status 200 to 299 |
| `status` | any other status (the code is kept in `last_status`); a `3xx` is `redirect`: redirects are never followed |
| `timeout` | no complete answer within `events.delivery.timeout` |
| `connection`, `tls` | the connection or the handshake failed |
| `blocked` | the egress guard refused the address or the scheme |

When the event at the head of a destination's queue fails, the destination waits: `failures`
counts the consecutive failed attempts of that event, and `next_attempt_at` is set to
`min(5 s × 2^(failures−1), events.delivery.backoff_max)`, less a random part of up to 20 %. While
`failures > 0` a round sends that event alone. When it succeeds the counter is cleared and the
queue flows. When it has failed `events.delivery.max_attempts` times (10: about 51 minutes with the
defaults) it is **parked**: a row of `outbox_dead_letter` (destination, `seq`, event ID, type,
attempts, last outcome and status), the cursor moves past it, and the next event is tried at once.
So one event that a receiver cannot accept delays the others by a bounded time and never blocks
them; a receiver that is down keeps its whole queue, in order, in the log, and loses nothing while
the log keeps the events (§5.13) — the few events that reached `max_attempts` during the outage are
dead letters to replay.

A failure that is not the destination's — the database, or a secret that cannot be opened because
the key-encryption key is unavailable — consumes no attempt: the round is abandoned, the lease
expires, the line `event relay failing` says so.

### 5.8 Delivery state and replay (D-11)

A destination shows `delivery`: `state` (`disabled`; `failing` when `failures > 0`; `delivering`
when the cursor is behind the head; `idle`), `cursor`, `head`, `failures`, `failing_since`,
`next_attempt_at`, `last_success_at`, `last_failure_at`, `last_error`, `last_status`, `dead_letters`
and `expired_events` (§5.13); and `test`, the last test request with its outcome (§5.11).

- **Replay of dead letters**: all of a destination or chosen sequence numbers become `replay`; the
  next round sends each once; a success removes the row, a failure parks it again with one more
  attempt.
- **Rewind**: the cursor of a destination is set back to a sequence number, or to the last event
  recorded before a time; everything after it that the log still holds is delivered again. A
  rewind clears the failure state and the lease, so a round in flight settles nothing.
- Enabling a disabled destination clears the failure state; it resumes at its cursor.

### 5.9 The signed request (D-12 to D-14)

```
POST <url>
Content-Type: application/cloudevents+json; charset=utf-8
User-Agent: pandokos/<version>
webhook-id: <event id>
webhook-timestamp: <seconds since the epoch, when the attempt is made>
webhook-signature: v1,<base64 of HMAC-SHA256(key, "<webhook-id>.<webhook-timestamp>.<body>")>
```

The headers and the signed text are those of the Standard Webhooks specification, so a receiver can
use an existing library. The key is 32 random bytes generated by the server; the tenant sees it once
as `whsec_<base64>` when the destination is created or its secret rotated, never again, and cannot
supply its own. During a rotation the previous key stays valid for a time the caller chooses (24 h
by default, at most 7 days) and `webhook-signature` carries two signatures separated by a space. A
receiver refuses a timestamp more than five minutes from its clock and a signature that matches
neither key (the example does).

**Secrets.** Sealed by `vault.Sealer.SealNamed` under the tenant's data key, with associated data of
a kind of their own (`pandokos/webhook-secret/v1`, the tenant, the destination, the data key
version): a credential's ciphertext does not open as a signing secret, nor one destination's as
another's. The worker opens them for one round (`vault.OpenNamed`) and clears them after. The admin
role cannot read the sealed columns (column grants, as for provider credentials): it seals a new
secret, and in a rotation a database trigger keeps the replaced one as the previous secret. They are a `secret.Secret` from generation to the HMAC;
the shape `whsec_…` joins the shapes the log handler redacts; they appear in no audit row, log
line, error, export or response other than the two that show them once. Destroying the tenant's data
key (6.3) destroys them.

**Egress.** Requests use the public transport of the egress guard (ADR 0038): `https`, public
addresses checked on the address dialled, TLS 1.2 or later with verification, the outbound proxy
when one is configured, no redirect followed. A host of `events.webhook.private_hosts` uses the
private transport (for receivers inside the operator's network, and for the tests). The request has
one deadline (`events.delivery.timeout`); the response body is read up to 64 KiB and discarded;
nothing of the answer but the status is kept. `internal/outbox` builds no transport and no dialer.

### 5.10 Pull export (D-15)

`GET /v1/tenants/{tenant}/events/export?after=<seq>&limit=<n>&type=<type>` (`event:read`) answers
`application/x-ndjson`: the events with `seq > after`, oldest first, at most `limit` (1 to 10000,
default 1000), one CloudEvents document per line. A consumer keeps the `sequence` of the last line
and asks again with it; because of §5.1 it misses nothing and, unlike a webhook, sees each event
once. `GET /v1/platform-events/export` (`platform_event:read`) is the same over the platform's log.
An `after` older than what retention kept simply starts at the oldest event.

### 5.11 Operations added to the OpenAPI document (D-19, D-20)

| Operation | Method and path | Permission |
|---|---|---|
| `listEventDestinations`, `createEventDestination` | `GET`, `POST /v1/tenants/{tenant}/event-destinations` | `event_destination:read`, `:write` |
| `getEventDestination`, `updateEventDestination`, `deleteEventDestination` | `GET`, `PATCH`, `DELETE …/event-destinations/{destination}` | `:read`, `:write`, `:delete` |
| `rotateEventDestinationSecret` | `POST …/{destination}/rotate-secret` | `:write` |
| `testEventDestination` | `POST …/{destination}/test` | `:write` |
| `rewindEventDestination` | `POST …/{destination}/rewind` | `:write` |
| `listEventDeadLetters`, `replayEventDeadLetters` | `GET …/{destination}/dead-letters`, `POST …/dead-letters/replay` | `:read`, `:write` |
| `exportEvents` | `GET /v1/tenants/{tenant}/events/export` | `event:read` |
| `exportPlatformEvents` | `GET /v1/platform-events/export` | `platform_event:read` |

`Tenant`, `TenantCreate` and `TenantUpdate` gain `external_reference`; `TenantDeletion` gains it
read-only. Schemas and parameters are in plan appendix A3.

- `createEventDestination` and `rotateEventDestinationSecret` take `Idempotency-Key` (ADR 0059);
  the secret is absent on a replay.
- **No new error code.** A URL, a type or a name that is not valid, a `rewind` with both or neither
  of `after` and `since`: `400 invalid_request`; a name already used, a ninth destination: `409
  conflict`; a destination of another tenant: `404 not_found`; the vault unable to seal or open:
  `503 unavailable`, as for credentials.
- **`testEventDestination`** records a request with a new event ID and answers `202` with it. The
  worker sends one `io.pandokos.destination.test.v1` event with that ID, signed like any other,
  within a relay interval, to a disabled destination too, and records the outcome and status, shown
  as `test` of the destination. The event is not written to the log and moves no cursor or failure
  state. The admin process makes no webhook request and opens no signing secret.
- **Audit**, in the tenant's chain, in the transaction of the change: `event_destination.create`,
  `.update`, `.delete`, `.rotate_secret`, `.rewind`, `.replay`. The detail holds the name, the host
  of the URL (never its path or query), the types, `enabled`, the cursor or the count; never a
  secret.

### 5.12 Command line

`pandokos event-destination list|get|create|update|delete|rotate-secret|test [--wait]|rewind|dead-letters|replay`,
`pandokos event export`, `pandokos platform-event export`, `pandokos event schema [type]` (prints
the embedded schema, no connection), `pandokos tenant create|update --external-reference`. `create`
and `rotate-secret` print the secret once on standard output and say that it is not shown again.

### 5.13 Retention (D-16)

`outbox.Janitor` (worker role) runs hourly: for each tenant that is `active` or `suspended`, in its
session, `tenant_outbox_retention(hours, usage_months, batch)` until a call removes less than a
batch.

- **Events** recorded more than `events.retention` ago (168 h by default) are removed, oldest first,
  under `pandokos.retention` like every append-only row (ADR 0065). The rule is time alone: an event
  that an enabled destination had not passed is removed too, the destination's cursor is moved past
  it, and it is counted in `expired_events` of that destination and in M55. This is the bound of
  EVT-03 (O-3): nothing is lost within the retention; after it, the ledger still holds the usage and
  the usage export of 6.2 still reads it.
- **Dead letters** go with their event.
- **Limit events** (6.3 O-12) are usage data and live as long as usage: with
  `retention.usage_months > 0`, rows of `limit_event` older than that cut are removed, with an audit
  row `limit_event.retention` in the tenant's chain. They are not tied to the retention of events:
  a limit event is also what makes a threshold fire once per period, and must outlive the period.
- The platform's events are kept.

### 5.14 Tenant deletion (D-17)

- From the request to the start (the grace period, the tenant suspended) the relay keeps delivering
  what is pending.
- Step `access` removes `outbox_dead_letter`, `event_destination` and the tenant's rows of
  `event_destination_directory`: a webhook is a way out for the tenant's data, and its secret is
  about to become unreadable. The count of destinations joins the completion record; counts of
  events and dead letters do not (ADR 0054).
- Step `configuration` removes `outbox_event` (under the retention switch) and `outbox_stream`.
  **Events not delivered when the deletion begins are removed, not delivered** (O-4): the receiver
  had the grace period, and the platform learns of the deletion by `tenant.deleted`. The row of the
  stream is skipped when a transaction that began before the deletion still holds it; `complete`
  then finds it, and that transaction's events, as residue, and the second pass removes them.
- `tenant_residue` counts the five tables; T-979's list of tables gains them (group A: dead letters,
  destinations, directory; group C: events, stream; kept: `platform_outbox_event`).
- A round in flight for a tenant that is closed meanwhile settles nothing (its destination is gone)
  and writes no dead letter (`tenant_open` refuses it).

### 5.15 Metrics, logs and configuration (D-21)

Seven instruments, no tenant attribute (ADR 0062); the gauges are refreshed once a minute by the
relay through `outbox_relay_state()`, a definer function that walks the directory in platform scope
and answers four counts and one age, never an identifier:

| # | Name | Kind | Unit | Attributes | Source |
|---|---|---|---|---|---|
| M52 | `pandokos.events.deliveries` | OC | `{delivery}` | `pandokos.delivery.outcome` (`delivered`, `status`, `redirect`, `timeout`, `connection`, `tls`, `blocked`) | one per request sent, replays and tests included |
| M53 | `pandokos.events.delivery.duration` | H | `s` | `pandokos.result` (`ok`, `failed`) | the time of a request |
| M54 | `pandokos.events.dead_letters` | OC | `{event}` | — | events parked |
| M55 | `pandokos.events.expired` | OC | `{event}` | — | events removed by retention before an enabled destination passed them |
| M56 | `pandokos.events.destinations` | G | `{destination}` | `pandokos.destination.state` (`enabled`, `failing`, `disabled`) | `outbox_relay_state` (`failing` is a subset of `enabled`) |
| M57 | `pandokos.events.lag` | G | `s` | — | the age of the oldest event that an enabled destination has not settled; 0 when none |
| M58 | `pandokos.events.relay.passes` | OC | `{pass}` | `pandokos.result` (`ok`, `failed`) | passes of the relay |

`pandokos.retention.kind` (M44) gains `outbox_event` and `limit_event`; `pandokos.export.kind` (M46)
gains `event` and `platform_event`.

| Message | Level | Fields | When |
|---|---|---|---|
| `event relay ready` | info | `interval`, `workers`, `batch_size`, `max_attempts`, `retention` | at start, with the worker role |
| `event relay failing` | warn | `error_class`, `sqlstate`, `cause`, `failures` | a claim, a read or a settle failed, or a secret could not be opened; at most once a minute |
| `event relay recovered` | info | `failures` | the first pass that succeeds after failures |
| `event deliveries failing` | warn | `outcome`, `count` | requests failed since the last line, by the most frequent outcome; at most once a minute; no destination, URL or tenant |
| `event retention applied` | info | `events`, `expired`, `limit_events` | a janitor pass removed something; totals over the installation |
| `event retention failing` | warn | `error_class`, `sqlstate`, `failures` | at most once an hour |

| Key | Type | Default | Rule (all `serve` only) |
|---|---|---|---|
| `events.source` | string | `urn:pandokos` | a URI reference of 1 to 255 printable characters |
| `events.retention` | duration | `168h` | `24h` to `2160h`, whole hours |
| `events.relay.interval` | duration | `1s` | `100ms` to `1m` |
| `events.relay.workers` | integer | `4` | 1 to 64: destinations delivered at once by this process |
| `events.relay.batch_size` | integer | `100` | 1 to 1000 |
| `events.delivery.timeout` | duration | `10s` | `1s` to `30s` |
| `events.delivery.max_attempts` | integer | `10` | 1 to 100 |
| `events.delivery.backoff_max` | duration | `15m` | `5s` to `24h` |
| `events.webhook.private_hosts` | list | empty | as `tokens.private_hosts`: at most 32 lower-case DNS names, no literal, no wildcard |

### 5.16 The receiver example (D-24)

`examples/webhook-receiver` uses the standard library only and is part of the root module, so it is
built, vetted and tested with it. `receiver.Handler` verifies the three headers against one or two
secrets and the five-minute tolerance, parses the CloudEvents document, and hands it to a `Store`
whose `Put(id)` reports whether the ID was new; it answers `204` in both cases and `401` for a bad
signature. `main.go` keeps IDs in a file and prints one line per new event. The end-to-end tests
import the package with a store that counts duplicates and with fault injection in front of it.
Its signature check is written independently of `internal/outbox/sign.go`: the two agreeing is the
interoperability test.

### 5.17 Requirements, records and plans that change

EVT-03 keeps its text; `docs/requirements` gains a note on its bound (O-3). ADR 0014 gets a history
line and its "Relay" and "Guarantee" paragraphs are refined by ADR 0070. The data model's "Outbox"
section and table list are rewritten (`outbox_delivery` never existed; the tables of this sprint
replace it). The overview's code layout already names `internal/outbox`.

## 6. Isolation and permissions

| Table / function | Scope | Gateway | Admin | Worker |
|---|---|---|---|---|
| `outbox_event`, `outbox_stream` (new) | tenant | — | `SELECT` | `SELECT` |
| `event_destination` (new) | tenant | — | `SELECT` of every column but the two sealed secrets; `INSERT` of the configuration and the secret; `UPDATE` of the configuration, the secret and the expiry of the previous one, the test request, and the cursor with the failure state and the lease; `DELETE` | `SELECT`; `UPDATE` of the cursor, the failure state, the lease, `last_*` and the test result |
| `outbox_dead_letter` (new) | tenant | — | `SELECT`; `UPDATE (state, replay_requested_at)` | `SELECT`, `INSERT`, `UPDATE` of the state columns, `DELETE` |
| `event_destination_directory`, `platform_outbox_stream` (new) | global | — | — | — |
| `platform_outbox_event` (new) | global, restricted | — | `SELECT` in platform scope | — |
| `tenant` | | | `+ INSERT, UPDATE (external_reference)`, platform scope only (trigger) | |
| `tenant_deletion` | | | `+ SELECT (external_reference)` | |
| `outbox_relay_claim(uuid, integer, integer)`, `outbox_relay_state()` | definer, platform scope | — | — | `EXECUTE` |
| `tenant_outbox_retention(integer, integer, integer)` | definer, tenant session | — | — | `EXECUTE` |
| `usage_record_outbox`, `limit_event_outbox`, `tenant_deletion_outbox`, `kek_version_outbox`, `event_destination_directory_sync` | definer, triggers | — | — | — |
| `outbox_seq_reserve`, `platform_outbox_append`, `rfc3339`, `event_destination_guard`, `tenant_external_reference_guard` | invoker; the first three callable by the owner only | — | — | — |

- **Events of a tenant are the tenant's** (ADR 0054). No platform permission, operation or definer
  function returns one, or a count of one tenant's. The two functions the worker calls in platform
  scope answer identifiers and installation-wide counts. A support action can carry
  `event_destination:read` only.
- **The gateway role gains nothing.** It cannot read or write an event; its inserts into the ledger
  and into `limit_event` produce them.
- **The worker cannot redirect a tenant's events**: it holds no `UPDATE` on `url`, `event_types`,
  `enabled` or a secret. **The admin role cannot read a sealed signing secret**, nor copy one (it
  holds no `SELECT` on the two columns), and cannot forge delivery history: no `UPDATE` on `last_*`,
  `expired_events` or a test result.
- **Permissions.**

| Permission | Scope | Support | Roles | Covers |
|---|---|---|---|---|
| `event_destination:read` | tenant | yes | `owner`, `tenant-admin`, `auditor`, `read-only` | destinations, their delivery state, dead letters |
| `event_destination:write` | tenant | no | `owner`, `tenant-admin` | create, change, enable and disable, rotate, test, rewind, replay |
| `event_destination:delete` | tenant | no | `owner`, `tenant-admin` | delete |
| `event:read` | tenant | no | `owner`, `tenant-admin`, `usage-viewer`, `read-only` | the export of the tenant's events (usage data) |
| `platform_event:read` | platform | — | `platform-admin`, `platform-auditor` | the export of the platform's events |

  `read-only` receives the two `:read` permissions by its rule; it already reads usage. `developer`
  and `credential-manager` receive none.
- **Every new route declares its permission** and is covered by the cross-tenant harness (T-1043,
  T-270).
- The lint gains **L-23**: `usage_record` has the enabled statement trigger `usage_record_outbox`
  and `limit_event` the enabled row trigger `limit_event_outbox`; `Spec` gains the four tenant
  tables with fixtures, the three global ones, the grants above and eight definer functions.

## 7. Test infrastructure and CI

- `dbtest` gains `SeedEventDestination(t, tenant, url) (id, secret)` (sealed with the test key, as
  the admin role), `OutboxEvents(t, tenant)` (the superuser reads the log) and `AgeOutbox(t, tenant,
  d)` (the superuser moves `recorded_at` back, as `AgeDeletion` does); `PopulatedTenant` gains a
  destination, a dead letter and events.
- `test/e2e`: `startBinary` gains `events` options (interval, timeout, attempts, backoff, private
  hosts); `faultReceiver` wraps `receiver.Handler` with scripted faults (refuse connections, answer
  500, stall, answer after the client gave up) and counts duplicates.
- `test/sdk` validates events against the published schemas with the validator added in 6.3; no
  module is added anywhere.
- `Makefile`: `generate` copies the schemas; `bench` runs `BenchmarkLedgerBatchOutbox`. CI is
  unchanged otherwise: the new tests run in the existing jobs.

## 8. Decisions

| # | Decision |
|---|---|
| D-1 | The outbox is `outbox_event`, an append-only log per tenant, written by two database triggers (a statement trigger on `usage_record`, a row trigger on `limit_event`) in the transaction of the fact; no Go code and no runtime role writes it |
| D-2 | `seq` is assigned under the lock of the tenant's row of `outbox_stream`, held to the end of the transaction: it follows commit order, is not gapless, and makes `seq > n` an exact cursor |
| D-3 | CloudEvents 1.0, structured JSON; `data` is stored with the event; the envelope (`source`, `tenantid`, `tenantref`, `sequence`) is rendered at delivery and export |
| D-4 | Types `io.pandokos.<name>.v1` (§5.3); the usage event is the record without `provider_usage`, with the caller's metadata, the cost as recorded, `limit_state`, and not the rules it counted against |
| D-5 | Not events in this sprint: `limit.unlimited_traffic`, a change of active currency, refused grants and refused support attempts, audit rows |
| D-6 | `tenant.external_reference`: set by the platform, not unique, audited, in every event as `tenantref`, copied into `tenant_deletion` at the request |
| D-7 | The platform's events (deletion requested, cancelled, completed; key version activated, retired) are rows of `platform_outbox_event` written by triggers; pull only, no webhook, kept |
| D-8 | A destination is a webhook of a tenant: at most 8, a filter of types, `enabled`; it starts at the head of the log |
| D-9 | The relay claims destinations, not events: a cursor per destination, a lease given by `outbox_relay_claim` with `FOR UPDATE SKIP LOCKED`, a settle conditional on the lease; at least once (amends ADR 0014) |
| D-10 | The head of a destination's queue is retried with exponential backoff and jitter and blocks it; after `max_attempts` it is parked as a dead letter and the queue moves on; failures that are not the destination's consume no attempt |
| D-11 | Replay is a state of a dead letter (`replay`) or a rewind of the cursor (to a sequence number or a time) |
| D-12 | Standard Webhooks headers and signature (HMAC-SHA256 over `id.timestamp.body`); the key is generated by the server, shown once as `whsec_…`; a rotation keeps the previous key valid for a chosen time, with two signatures |
| D-13 | Signing secrets are sealed under the tenant's data key with associated data of their own kind, opened by the worker per round and by nobody else (the admin role cannot read the columns), redacted by shape in the log, and die with the data key |
| D-14 | Webhook requests use the egress guard: public class, `https`, no redirect; `events.webhook.private_hosts` for named internal receivers; one deadline; 64 KiB of answer read and discarded |
| D-15 | Pull export: NDJSON of CloudEvents by `after`, for the tenant's log (`event:read`) and the platform's (`platform_event:read`) |
| D-16 | Events live `events.retention` (7 days), delivered or not; expired events are counted per destination and in a metric; `limit_event` follows `retention.usage_months`; platform events are kept |
| D-17 | A tenant's destinations and dead letters go in step `access`, its events and stream in `configuration`; events undelivered when the deletion begins are removed; delivery continues during the grace period |
| D-18 | Five permissions (§6); only `event_destination:read` may be carried by a support action |
| D-19 | Twelve operations; no new error code; `Idempotency-Key` on create and rotate; six audit actions whose detail holds the host of the URL, never the URL or a secret |
| D-20 | A test request is recorded by the administration and sent by the worker; its outcome is read on the destination. The admin process makes no webhook request |
| D-21 | Seven instruments without tenant attribute, fed by counters of the relay and by `outbox_relay_state()`; six log lines that name no destination |
| D-22 | Destinations and the external reference are not in `pandokos export` and `apply`; the tenant data export is unchanged |
| D-23 | One JSON Schema per type, envelope included, embedded, published under `docs/reference/events/`, printed by `pandokos event schema` |
| D-24 | `examples/webhook-receiver`: standard library only, in the root module, used by the end-to-end tests |
| D-25 | The cost of the event on the ledger's insert path is measured by a benchmark and bounded by a test (three times the insert without it) |
| D-26 | One migration, eight definer functions, five permissions, nine keys, twelve operations, seven instruments, lint rule L-23; no new error code or module |

## 9. Tests

Levels: U unit, I integration (in-process, may use the test database), D database (the isolation
harness and the schema), E end to end (the binary), R repository.

### New tests

| ID | Test | Level | Asserts | Requirement |
|---|---|---|---|---|
| T-1013 | `isolation.TestOutboxTables` | D | For the four tenant tables: each runtime role in the tenant's session, another tenant's and the platform scope sees and writes exactly what §6 says; the gateway nothing; no runtime role inserts, updates or deletes an event or a stream row, with or without `pandokos.retention` or `pandokos.erasure` set; `TRUNCATE` refused; the directory and `platform_outbox_stream` give no privilege; `platform_outbox_event` is read by the admin role in platform scope only | EVT-01, USE-07 |
| T-1014 | `isolation.TestUsageRecordOutbox` | D | One event per inserted record, in the same transaction (a rollback leaves neither); a batch of 500 gives 500 consecutive `seq`; a record left out by the conflict clause gives no second event; `id`, `subject`, `occurred_at` and `data` are those of §5.3 for a record with every column set and for one with the minimum (golden); no `provider_usage`, no content; a statement of a superuser without a session, over two tenants, writes each log; a closed tenant's statement is refused whole | **EVT-01**, **EVT-02** |
| T-1015 | `isolation.TestLimitEventOutbox` | D | The type by kind; `id` is the row's; `data` for a cost and a count metric (golden); a second insert that the unique key refuses writes no event | **LIM-13** |
| T-1016 | `isolation.TestOutboxSequenceOrder` | D | Eight writers committing 200 transactions each with random pauses, one reader polling `seq > cursor`: the reader gets every ID once and in increasing `seq`; a second writer of the same tenant waits for the first to end; writers of two tenants do not wait for each other; a rollback leaves a gap | EVT-03, EVT-04 |
| T-1017 | `isolation.TestEventDestinationGuards` | D | The cursor of a new destination is the head; a ninth is refused, also under concurrent inserts; every check of the table; the directory follows insert and delete; the admin role cannot select `secret` or `previous_secret`, nor assign one from the other; a new secret with an expiry keeps the replaced one as previous, without an expiry drops it; the admin role cannot write `last_*`, `expired_events` or a test result, the worker cannot write the URL, the types, `enabled` or a secret | EVT-04 |
| T-1018 | `isolation.TestOutboxRelayClaim` | D | Refused outside the platform scope, with bad arguments, to the admin and gateway roles; claims only destinations that are not leased and are enabled, due and have work (an event after the cursor, or a `replay` letter), or have a test waiting (a disabled one too); an expired lease is claimed again; two concurrent claims are disjoint; the limit holds across tenants; the answer is identifiers; `app.tenant_id` and both settings are as before; a role that sets a setting first gains nothing | **EVT-03** |
| T-1019 | `isolation.TestTenantOutboxRetention` | D | Only rows older than the hours, at most a batch, oldest first; cursors behind are moved and `expired_events` counts for enabled destinations only; dead letters of removed events go; `limit_event` by months, with the audit row and without one when nothing went; another tenant untouched; bad arguments, the platform scope, the admin role refused | EVT-03 |
| T-1020 | `isolation.TestPlatformOutbox` | D | Each of the five types is written by its trigger in the transaction of its cause (a request rolled back leaves none); the data of §5.3; no member that is a usage figure; `seq` in commit order; a request and a completing erasure step run concurrently without deadlock; a tenant session, the worker and the gateway read nothing; append-only | EVT-01 |
| T-1021 | `isolation.TestErasureRemovesEvents` | D | With `PopulatedTenant`: after `access` no destination, dead letter or directory row; after `configuration` no event and no stream; a transaction holding the stream row does not make the step wait, and its row and events are removed by the second pass; `tenant.deleted` is written with the completion, carries the reference copied at the request (a later change of the tenant's does not show) and `completion_seq`; the completion record counts destinations and no events; the other tenant's log is unchanged | TEN-11, EVT-01 |
| T-1022 | `isolation.TestOutboxRelayState` | D | The four counts and the age over three tenants; 0 when nothing is pending; scope and roles | — |
| T-1023 | `isolation.TestExternalReference` | D | Set at insert and update in platform scope; refused in a tenant session; the length and control-character checks; copied by the deletion request | EVT-02 |
| T-1024 | `migrations.TestUpgrade0017` | D | A database at 0016 with usage records, limit events, a scheduled and a completed deletion: after 0017 no event exists for the past, every reference is empty, every existing row is unchanged, the triggers exist; a new record then has its event; the tests of 0016 migrate to their own version | EVT-01 |
| T-1025 | `isolation.TestSchemaLintOutbox` | D | L-23 and its detection of a missing or disabled trigger; no finding of L-08 to L-13, L-17, L-21, L-22 for the new objects | EVT-01 |
| T-1026 | `outbox.TestRender` | U | The envelope for every type (golden): member order, `tenantref` absent when empty, `sequence` a string, the time in UTC with microseconds, `source` from the option; `data` is passed through byte for byte | **EVT-02** |
| T-1027 | `outbox.TestSign` | U | The three headers; the signed text; one and two keys; a key that is not `whsec_` and 32 bytes is refused with an error that quotes nothing; every formatting path of a key prints the redaction marker | **EVT-04** |
| T-1028 | `outbox.TestBackoff` | U | For every attempt count and maximum: the wait is within the envelope of §5.7, never above the maximum, never below 80 % of the nominal value | EVT-03 |
| T-1029 | `outbox.TestDeliver` | I | Against local servers named as private hosts: each outcome of §5.7; a redirect is not followed; the deadline; a body of 1 MiB is not read past 64 KiB; the request headers are exactly those of §5.9 (no `Authorization`, no cookie); a private address without the host listed, an `http` URL and a metadata address are `blocked` before any connection | **EVT-04**, GW-06 |
| T-1030 | `outbox.TestRelayRound` | I | A batch is delivered and the cursor is the last event; a destination with a type filter skips the others and ends at the head; a statement recorder sees only the statements of plan A2, none of them writing `outbox_event`; a settle with a stale token changes nothing | **EVT-03** |
| T-1031 | `outbox.TestRelayHeadOfLine` | I | A failing head: the next rounds send it alone, at the stored times; after `max_attempts` a dead letter exists and the following events are delivered; a success clears the state; events behind a failed head are sent again at most once | **EVT-03** |
| T-1032 | `outbox.TestRelayCrashMidDelivery` | I | A relay stopped after the receiver answered and before the settle: the lease expires, a second relay delivers again, the receiver's store holds each ID once and none is missing; the same with the stop before the request | **EVT-03** |
| T-1033 | `outbox.TestRelayTwoWorkers` | I | Two relays on one database, 5 destinations, 2000 events: no destination has two rounds at once (a hook counts), every event reaches every destination | EVT-03 |
| T-1034 | `outbox.TestRelayReplayAndRewind` | I | Replay of all and of chosen letters: one attempt each, removed on success, parked again with one more attempt on failure; a rewind to a sequence number and to a time delivers again; a rewind during a round makes its settle a no-op | **EVT-03** |
| T-1035 | `outbox.TestRelaySecretRotation` | I | After a rotation requests carry two signatures until the expiry, then one; the receiver with either key accepts during the overlap | EVT-04 |
| T-1036 | `outbox.TestRelayNotTheDestinationsFault` | I | The database out of reach: `event relay failing` once, then `recovered`, M58; a key-encryption key that is unavailable: no request, no attempt counted, `cause` in the line, delivery resumes when it is back | EVT-03, OPS-11 |
| T-1037 | `outbox.TestRelayClosedTenant` | I | A destination deleted, disabled, or its tenant closed between claim and settle: nothing is written, no line names anything of the tenant | TEN-11 |
| T-1038 | `outbox.TestJanitor` | I | Calls the function per tenant until less than a batch; a failing tenant does not stop the others; deleted tenants are not visited; the totals reach the stats | EVT-03 |
| T-1039 | `vault.TestSealNamed` | I | Round trip through the database; the associated data binds the kind, the tenant and the object (a credential's ciphertext, another destination's, another tenant's do not open); buffers cleared; printing gives the marker | EVT-04 |
| T-1040 | `admin.TestEventDestinationAPI` | I | Create, read, list, update, delete; the validation table (URL forms, types, names, the ninth); the secret once and absent on an idempotent replay; `delivery` as §5.8; the six audit actions with the host only and no secret (canary) | **EVT-04** |
| T-1041 | `admin.TestEventDestinationOperations` | I | Rotate (the previous key and its expiry; the bound of 7 days); test: `202` with an ID, then, with a relay running, `test` shows each outcome, for a disabled destination too, with nothing written to the log and no cursor or failure state moved; the handler sends no request; rewind (both forms, their validation); dead letters listed and replayed | EVT-03, EVT-04 |
| T-1042 | `admin.TestEventExport` | I | NDJSON lines are the documents of T-1026; `after`, `limit`, `type`; a consumer that follows `sequence` while writers commit gets every event once; the platform's export | **EVT-04** |
| T-1043 | `admin.TestEventPermissions` | I | The twelve operations for every built-in role; a principal of another tenant gets `403` and another tenant's destination ID `404`; a platform principal gets `403` without a support action and, with one, only what `event_destination:read` allows; `exportEvents` never under a support action | EVT-04, USE-07 |
| T-1044 | `admin.TestTenantExternalReference` | I | Create, update, read, validation, the audit detail, the copy shown by `TenantDeletion` | **EVT-02** |
| T-1045 | `admin.TestEventSecretCanary` | I | After creation and rotation the secret is in no response, audit row, log line or error; the stored column is not the secret and the admin pool cannot select it; a log call with the secret is redacted and counted | EVT-04 |
| T-1046 | `cli.TestEventCommands` | U | Every command of §5.12 against a fake server: arguments, output, the once-only notice; `event schema` offline | EVT-04 |
| T-1047 | `config.TestEventKeys` | U | The nine keys: defaults, bounds, the environment names, `serve` only | — |
| T-1048 | `telemetry.TestEventInstruments` | U | The seven instruments in the catalogue, with their attributes and closed value sets; none has a tenant attribute; the redaction shape `whsec_…` | — |
| T-1049 | `receiver.TestHandler` | U | A valid request is stored once; the same ID again answers `204` and stores nothing; a bad signature, a missing header, a timestamp six minutes old are `401`; either of two secrets verifies | **EVT-03** |
| T-1050 | `ledger.TestOutboxWriteCost` | D | Batches of 500 through the writer's statement with the trigger enabled and disabled (the owner disables it in the test database), medians of 20: the ratio is below 3; the two figures are logged | EVT-03 |
| T-1051 | `sdk.TestEventSchemas` | E | Events of every type taken from a running binary's exports and from a webhook validate against the published schemas with the third-party validator; a document with a missing required member does not | **EVT-04** |
| T-1052 | `e2e.TestEventsExactlyOncePerCall` | E | **Exit.** 300 calls through the gateway while the receiver is scripted to refuse connections, answer 500, stall and drop answers, and the binary is killed (`SIGKILL`) three times in mid-delivery and restarted: at the end the receiver's store has exactly one entry per call, each with the request ID of a call, duplicates were seen and absorbed, no dead letter remains after a replay | **EVT-01**, **EVT-03**, UC-13 |
| T-1053 | `e2e.TestEventsReceiverDown` | E | **Exit.** With a receiver that accepts connections and never answers: 200 calls complete, the ledger writer's queue returns to 0, the readiness check stays ready, M57 grows and the destination is `failing`; the outbox holds 200 events; when the receiver answers, all 200 arrive | **EVT-03** |
| T-1054 | `e2e.TestEventsPull` | E | A consumer that pulls by `sequence` across two restarts of the binary reads each event once; `pandokos event export` gives the same lines | EVT-04 |
| T-1055 | `e2e.TestEventsAndDeletion` | E | Pending events are delivered during the grace period; after the deletion no destination, event or secret of the tenant remains; `pandokos platform-event export` shows requested and deleted with the external reference; a backup taken before cannot sign (the secret does not open) | TEN-11, EVT-02 |
| T-1056 | `e2e.TestEventsTelemetryCanary` | E | The output of the processes of T-1052 and T-1055 and a scrape of the metrics hold no tenant ID, destination ID, URL, host or secret; every line is in the log reference | EVT-04 |
| T-R81 | `repo.TestEventsOffTheRequestPath` | R | `internal/gateway`, `internal/ledger`, `internal/limits`, `internal/resolver` do not import `internal/outbox`; no statement of `internal/store/queries/sql` inserts into, updates or deletes from `outbox_event`, `outbox_stream` or `platform_outbox_event` | **EVT-03** |
| T-R82 | `repo.TestEventReference` | R | The types of the table check, of the schema directory, of `docs/reference/events.md` and of the OpenAPI enumeration are the same set; published schemas equal the embedded ones | EVT-04 |
| T-R83 | `repo.TestOutboxEgress` | R | `internal/outbox` constructs no `http.Transport`, `net.Dialer` or `tls.Config`; its client refuses redirects | GW-06 |
| T-R84 | `repo.TestEventDocs` | R | The failure reference has the row "event destination" and "worker crash in a delivery" citing tests that exist; the configuration, log and metrics references list the keys, lines and instruments of §5.15 | OPS-11 |

### Changed tests of earlier sprints

| Test | Change |
|---|---|
| T-979 `isolation.TestTenantErasureSteps` | The groups gain the five tables (§5.14); `platform_outbox_event` is kept |
| T-977 `isolation.TestTenantOpenGuard`, `TestTenantOpenDefinerFunctions` | `event_destination` and `outbox_dead_letter` join the tables with an `INSERT` grant; `outbox_relay_claim`, `outbox_relay_state` and `tenant_outbox_retention` get their calls |
| T-747 `isolation.TestPlatformSeesNoTenantUsage` | Pins the eight definer functions with their result types; `limit_event_outbox` and `tenant_outbox_retention` may name `limit_event` (a trigger; counts) |
| `isolation.TestSchemaLint`, `TestSchemaLintDetects` and the tests that run the lint | `Spec` (plan A4) |
| T-846, T-847 (golden of roles), T-219, T-270 | The five permissions; the commands; the twelve operations classified |
| `queries.TestEveryStatement` | The statements of plan A2 |
| `repo.TestConfigurationReference`, `TestLogReference`, `TestMetricsReference`, T-R71 | Nine keys, six lines, seven instruments; the two platform operations read no tenant report |
| `migrations.TestUpgrade0016`; `e2e.TestUpgrade…` | Migrates to its own version; ends at 0017 |
| The admin tests of tenants and of tenant deletion | `external_reference` in the bodies |

### Requirement coverage

| Requirement | Tests |
|---|---|
| EVT-01 | T-1014, T-1020, T-1024, T-1025, T-1052 |
| EVT-02 | T-1014, T-1023, T-1026, T-1044, T-1055 |
| EVT-03 | T-1016, T-1018, T-1019, T-1030 to T-1034, T-1036, T-1049, T-1052, T-1053, T-R81 |
| EVT-04 | T-1027, T-1029, T-1035, T-1040 to T-1042, T-1051, T-1054, T-R82 |
| LIM-13 | T-1015, T-1051 |
| UC-13 | T-1052 |
| Exit | T-1052; T-1053 |
| Canary and isolation | T-1013, T-1043, T-1045, T-1056; the four tenant tables through `Spec.Fixtures` and the cross-tenant harness |

## 10. Definition of done for this sprint

- [ ] Every test of §9 exists under its name, failed before its implementation, and passes; the
      changed tests are changed as listed.
- [ ] EVT-01 to EVT-04 and LIM-13 are met and covered (§9).
- [ ] The four tenant tables have the policy, fixtures and isolation tests; the three global ones
      are declared; every new route declares a permission and has its cross-tenant negative test.
- [ ] No secret, URL, tenant identifier or content appears in a log line, a metric or an audit
      detail beyond what §5.11 and §5.15 say (T-1045, T-1056, T-930).
- [ ] The two exit tests pass (T-1052, T-1053).
- [ ] The cost of the event on the ledger's insert is measured on the CI machine and written in
      `docs/operations/events.md` and in the pull request (T-1050, plan F4).
- [ ] Migration 0017 is applied by `pandokos migrate` on a copy of a database of v0.2.0 with data,
      and the binary starts on it (T-1024; plan F2).
- [ ] `make check` passes, and the `sdk` job.
- [ ] The events guide, the event reference with its schemas, the API, permissions, configuration,
      log, metrics and failure references, the data model, the security model, the overview, the
      deletion and backup guides, ADR 0070 to 0072 with the history lines, and the draft release
      notes of v0.3.0 are current.

## 11. Consequences for later sprints

- **Closing epic 6.** This is its last sprint: the epic is closed with its record in
  `docs/history/`, which takes the items below.
- **A new event type.** It is a trigger or a call of the append function in the transaction of its
  fact, a schema, a row of the reference and the type in the check of `event_destination`. A
  transaction that also writes the tenant's audit chain must lock the chain before the stream
  (§5.1): audit rows as events (`audit.recorded`), if wanted, are that case.
- **Sprint 8.2 (separate processes).** The relay and the janitor belong to the worker process, which
  needs the key files (it opens signing secrets) and the egress settings. Several workers already
  share the work (T-1033). The gateway process needs nothing of this sprint.
- **Sprint 10.1 (load).** Measures the trigger under concurrent writers of one tenant (the lock of
  `outbox_stream`), a log of 10⁷ events with retention running (bloat, the vacuum that follows; a
  log partitioned by day is the alternative), the claim over 10⁴ destinations, and a receiver slower
  than the tenant's traffic. The failure suite uses the two new rows of the failure reference.
- **Sprint 12.1 (external key service).** Opening signing secrets unwraps the data key once per
  round and destination: with a remote key service this needs the data key cache of the opener. A
  secret store of the platform would allow a webhook for the platform's events (O-5).
- **Sprint 12.3.** Destinations (without secrets) and the external reference in `export` and
  `apply`; the restore drill replays the deletions listed by `tenant.deleted`.
- **Release v0.3.0.** The notes announce the five permissions, the nine keys, the twelve operations
  and the commands, migration 0017 (events start with it: nothing is produced for the past), the
  cost on the ledger's insert, and the bound of EVT-03.

## 12. Risks and open points

### What was verified while designing

| Fact | How |
|---|---|
| Migration 0017 (plan appendix A1) applies after 0001–0016 on PostgreSQL 16.15 | a scratch database on the design machine, `psql` as `pandokos_owner` |
| A statement trigger with a transition table works on the partitioned `usage_record`; the gateway role's batch of three records produces three events with consecutive `seq` and the data of §5.3; the gateway cannot read them; another tenant sees none | the same, as the runtime roles |
| A limit event of each kind produces its event with `amount` or `limit`; a destination starts at the head; the checks refuse an unknown type and an `ftp` URL; a claim finds nothing at the head, one destination after a rewind, none while leased, and restores the session; the worker cannot change a URL; no runtime role updates or deletes an event, with a setting set or not | the same |
| Retention removes four aged events of five, moves the cursor, counts four expired, removes one aged limit event and writes its audit row | the same |
| A deletion request, its cancel, a new request and the nine steps of the erasure produce the three platform events with the reference copied at the request; a key activation produces its event; after the steps no destination, directory row, event or stream row remains; a usage record for the closed tenant is refused; the admin role cannot change a platform event and other sessions do not see them | the same |
| The external reference cannot be changed in a tenant session and can in platform scope; the admin role cannot select or copy a sealed secret; a new secret with an expiry keeps the replaced one, without one drops it; a test request on a disabled destination is claimed once and not again after its result is written | the same |
| With 0017 and no other change, the isolation suite fails only where §9 "Changed tests" says: `Spec` (three global tables, the grants, eight definer functions, four fixtures), the table groups of T-979, the pinned functions of T-747, the calls of `TestTenantOpenDefinerFunctions`; the lint has no finding of L-08 to L-11, L-17, L-21 or L-22 once the three global tables are declared; `internal/ledger`, `internal/limits` and the migration tests pass unchanged | a scratch copy of the repository against the scratch server |
| Two defects of the first draft, found by that suite: the triggers used the tenant of the session, which a superuser's fixture does not have (they now use the tenant of the rows); the step `configuration` waited for a transaction that held the stream row (`TestErasureLateRows`; it now skips a locked row and the second pass removes it) | the same; both tests pass with the final text |
| `sqlc` v1.31.1 compiles migration 0017; the tenant queries must name `external_reference` in their column lists to keep returning the model type | `make generate` in the scratch copy |
| The cost figures of §5.2 | `psql` with `\timing`, eight runs each, the first two discarded |

Not verified: the Go code of every part; the relay under real concurrency (T-1033) and real
crashes (T-1052); the behaviour of the lock of `outbox_stream` with many concurrent writers of one
tenant (10.1); that an existing Standard Webhooks library accepts the signature (O-11; the header
and signature format are written from the specification as the designer knows it, and plan C2 has
the implementer check it against the published text before coding).

### Risks

| # | Risk | Mitigation |
|---|---|---|
| R-1 | The trigger slows the ledger writer or, failing, loses usage records | One statement more per batch, measured and bounded (T-1050); the function has no branch that can fail on valid records (T-1014 covers every column set and unset); a failure rolls the batch back and the writer retries or rejects as today, so a record is never written without its event |
| R-2 | The per-tenant lock serializes the ledger writers of several gateway instances for one tenant | It is held for one statement; the audit chain already works this way; measured in 10.1 |
| R-3 | Two workers deliver for one destination at once, or none does | One statement gives the lease under `SKIP LOCKED`; the settle is conditional on the token; an expired lease is claimable (T-1018, T-1032, T-1033) |
| R-4 | A crash between the request and the settle delivers twice | By design: at least once; the ID is stable; the example and T-1052 show the receiver's side |
| R-5 | A tenant points a destination at an internal address (SSRF), or uses the test request to probe | The guard checks the address dialled, for every attempt; no redirect; only the outcome and the status code come back; requests leave from the worker only (T-1029, T-1041) |
| R-6 | A signing secret leaks through a log, an audit row or an error | A `secret.Secret` end to end; redaction by shape; canary tests (T-1045, T-1056) |
| R-7 | A receiver that is slow but not failing never catches up with the tenant's traffic | Parallel requests within a round, batches back to back; M57 and `delivery.cursor` show it; events expire visibly (`expired_events`, M55); pull is the alternative |
| R-8 | The log grows faster than retention removes it | Batches of 5000 per call until none is left; measured in 10.1; a partitioned log is the fallback |
| R-9 | Events are lost when a destination is down longer than the retention, or at a deletion | Stated (O-3, O-4), counted and shown; the ledger and its export remain the record |
| R-10 | A later event type takes the stream lock and the chain lock in the other order | Stated in §5.1, §11 and ADR 0070; the platform's append function shows the pattern |
| R-11 | The event data, built in SQL, drifts from the published schema | T-1051 validates real events; T-R82 ties types, schemas and reference together; a change of `data` is a migration and a new version |
| R-12 | A URL with a token in its path is shown to every reader of destinations | Documented; the audit log keeps the host only; the signature, not the URL, authenticates |

### Open points (raised to the maintainer; none blocks the sprint)

| # | Question | Recommendation |
|---|---|---|
| O-1 | Events are written for every tenant, whether it has a destination or ever pulls. Or only for tenants that opt in? | Always: the pull export must work without registration, a destination created later needs no back-fill rule, and the cost is bounded by retention and measured (D-25) |
| O-2 | The relay claims destinations with a cursor instead of claiming events one by one as ADR 0014 says. Confirm the amendment (ADR 0070)? | Confirm: no row is written per delivered event, an outage writes nothing, and the guarantees of ADR 0014 are unchanged |
| O-3 | An event older than `events.retention` (7 days) is removed even if a destination never received it; EVT-03 "no event is lost" holds within that window. Or keep undelivered events without limit? | Confirm the bound: storage must be bounded by something other than a receiver's health; the loss is counted and shown, and the ledger remains |
| O-4 | Events of a tenant not delivered when its deletion begins are removed with it; delivery continues during the grace period. Or hold the deletion until the queue is empty? | Confirm: a dead receiver must not block an erasure; with the default grace period the receiver had seven days |
| O-5 | The platform's events are pulled; there is no platform webhook, because the vault seals under tenant data keys only. Confirm the deferral? | Confirm; revisit with the external key service (12.1) |
| O-6 | The external reference is set by the platform only (`tenant:write`), not by the tenant. Confirm? | Confirm: it is the operator's name for the tenant, and the platform's events depend on it |
| O-7 | `limit.unlimited_traffic`, a change of active currency, refused grants and refused support attempts, and audit rows are not events. Confirm? | Confirm: the first and the last are audit rows, read by the audit export; a usage event already carries its currency; refused attempts stay log lines (6.2 D-23). Audit rows as events can be one later type |
| O-8 | The usage event carries the caller's metadata, the principal and the key ID. Confirm? | Confirm: the event goes to the tenant's own destination and they are what a billing system attributes by; no content is involved |
| O-9 | The signing secret is always generated by the server; a caller cannot supply one. Confirm? | Confirm: no weak secret, no secret in a request body or a declarative document |
| O-10 | The test request is asynchronous: the administration records it and the worker sends it, so that the admin role never reads a sealed signing secret and only the worker makes webhook requests; the caller reads the outcome on the destination (the CLI waits for it). Confirm? | Confirm: it keeps the rule of the vault (the administration seals and never opens) at the price of a second read |
| O-11 | Add the Standard Webhooks reference library for Go as a test-only dependency in `test/sdk`, to verify a real delivery with it? | Add it if `go list -m -versions` finds a tagged version (plan B1 gives the command); otherwise the independent example is the check |
| O-12 | Destinations and the external reference stay out of `pandokos export`/`apply`; destinations stay out of the tenant data export. Confirm? | Confirm for 6.4; the first joins sprint 12.3; the second can be an additive file of the export format |
| O-13 | The numbers: retention 7 days, 10 attempts, backoff 5 s to 15 min, 8 destinations per tenant, batches of 100, 4 requests at a time. Confirm? | Confirm; all but the last two fixed ones are configuration |
| O-14 | A support action may carry `event_destination:read` (state, URL, dead letters) and nothing else of events. Confirm? | Confirm: it is what an operator needs to help with a delivery, and it shows no usage |
| O-15 | A new top-level directory `examples/`, part of the root module. Confirm? | Confirm: the example is compiled and tested with the product and needs no dependency |

### Maintainer decisions on the open points (2026-11-23)

| Open point | Decision |
|---|---|
| O-1 | Confirmed: events are written for every tenant, no opt-in |
| O-2 | Confirmed: the relay claims destinations with a cursor each, not single events; ADR 0070 amends ADR 0014 |
| O-3 | Confirmed: undelivered events expire after `events.retention`; the loss is counted and the ledger remains the record |
| O-4 | Confirmed: undelivered events of a tenant are removed when its deletion begins; the grace period is the receiver's window |
| O-5 | Confirmed: platform events are pull only; a platform webhook is revisited in sprint 12.1 |
| O-6 | Confirmed: the external reference is set by the platform only |
| O-7 | Confirmed: unlimited traffic, active-currency changes, refused attempts and audit rows are not events in 6.4 |
| O-8 | Confirmed: the usage event carries caller metadata, principal ID and key ID |
| O-9 | Confirmed: the signing secret is always generated by the server |
| O-10 | Confirmed: the test request is recorded by the administration and sent by the worker |
| O-11 | Confirmed: the Standard Webhooks Go library is added as a test-only dependency of `test/sdk` if a tagged version exists; otherwise the example receiver is the check |
| O-12 | Confirmed for 6.4: destinations and the external reference stay out of `export`/`apply` (sprint 12.3) and out of the tenant data export |
| O-13 | Confirmed: the proposed defaults |
| O-14 | Confirmed: a support action may carry `event_destination:read` only |
| O-15 | Confirmed: a top-level `examples/` directory in the root module |
