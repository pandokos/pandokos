# Event reference

The events Pandokos publishes for external systems (EVT-01 to EVT-04, LIM-13): the envelope, every type with
its `data`, the published schemas and the rule that versions them. How to receive them is in the
[events guide](../operations/events.md); the decisions are
[ADR 0070](../adr/0070-the-outbox-as-a-log-relay-by-destination-and-retention.md),
[ADR 0071](../adr/0071-webhook-destinations-signature-secrets-and-egress.md) and
[ADR 0072](../adr/0072-event-types-the-envelope-the-external-reference-and-platform-events.md).

A webhook request body and a line of an export are the same document: a CloudEvents 1.0 event in the
structured JSON mode. A repository test (T-R82) keeps this page, the schemas, the table check of migration 0017
and the enumerations of the OpenAPI document as one set of types.

## 1. The envelope

```json
{"specversion":"1.0","id":"01a2096c-074d-750b-9e85-c37222e3b9c9","source":"urn:pandokos",
 "type":"io.pandokos.usage.recorded.v1","time":"2026-11-23T17:20:04.426849Z",
 "subject":"eccbc87e4b5ce2fe28308fd9f2a7baf3","datacontenttype":"application/json",
 "tenantid":"01a2096c-0745-7db9-b65e-3f2f525e04af","tenantref":"cust-42","sequence":"1","data":{}}
```

(`data` is empty here for brevity; section 2 gives its members.)

| Attribute | Value |
|---|---|
| `specversion` | `1.0` |
| `id` | Stable across attempts and replays: **the receiver's idempotency key**. The ID of the usage record, of the `limit_event` row, or of the platform event; for a test event, the ID the request for the test answered with |
| `source` | `events.source` (default `urn:pandokos`): one value for the installation |
| `type` | One of the types of section 2. A breaking change of `data` is a new version, delivered beside the old one |
| `time` | When the fact was recorded (`recorded_at` of the usage record, `occurred_at` of the limit event), UTC, with microseconds |
| `subject` | The request ID (usage), the rule ID (limits), the tenant ID or the key version (platform) |
| `datacontenttype` | `application/json` |
| `tenantid` | Extension: the tenant. Absent on the two `kek` types |
| `tenantref` | Extension: the tenant's external reference as it is when the document is rendered (set by the platform with `tenant create|update --external-reference`); absent when empty and on the two `kek` types |
| `sequence` | Extension, a string of digits: `seq` in the tenant's log, or in the platform's; absent on a test event. A rollback leaves no gap, but a gap is not a loss (type filters and retention leave holes), so a consumer does not rely on gaplessness; the pull export asks for `after=<sequence>` |
| `data` | Stored with the event when it is written and never changed. Members with no value are absent |

Over webhook the request has `Content-Type: application/cloudevents+json; charset=utf-8`; over the pull export
the lines are `application/x-ndjson`. The members of the envelope are written in the order above.

## 2. The types

Nine types, none of which can be received by the destination of another tenant. `usage.recorded`, the two
`limit` types are written to the **tenant's log** (webhook and pull); the five `tenant` and `kek` types to the
**platform's log** (pull only, section 4); `destination.test` is not written to any log.

| Type | Log | `data` |
|---|---|---|
| `io.pandokos.usage.recorded.v1` | tenant | `record_id`, `request_id`, `trace_id`, `started_at`, `duration_ms`, `principal` {`kind`, `id`}, `key_id`, `provider`, `api_family`, `operation`, `model`, `upstream_model`, `stream`, `outcome`, `error_code`, `usage` {`input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`, `reasoning_tokens`, `input_source`, `output_source`, `other_units`}, `cost` {`status`, `reason`, `amount` (decimal string), `currency`, `price_version_id`, `price_source`, `fallbacks`}, `limit_state`, `metadata` |
| `io.pandokos.limit.threshold_reached.v1` | tenant | `limit_event_id`, `rule_id`, `rule_level`, `subject` {`kind`, `id`}, `provider`, `model`, `metric`, `currency`, `period` {`start`, `end`}, `percent`, `limit` (requests, tokens) or `amount` (decimal string, cost), `observed` (decimal string) |
| `io.pandokos.limit.exhausted.v1` | tenant | as `threshold_reached`, without `percent` |
| `io.pandokos.tenant.deletion_requested.v1` | platform | `tenant_id`, `external_reference`, `requested_at`, `not_before` |
| `io.pandokos.tenant.deletion_cancelled.v1` | platform | `tenant_id`, `external_reference`, `requested_at` |
| `io.pandokos.tenant.deleted.v1` | platform | `tenant_id`, `external_reference`, `requested_at`, `started_at`, `completed_at`, `kek_versions`, `completion_seq` (the completion record in the platform's audit chain) |
| `io.pandokos.kek.version_activated.v1` | platform | `version`, `provider` |
| `io.pandokos.kek.version_retired.v1` | platform | `version`, `provider` |
| `io.pandokos.destination.test.v1` | none | `destination_id`, `name` |

Rules that hold for every type:

- **No content.** The ledger holds none, and an event holds none: no prompt, no completion, no header. The
  provider's own usage object is left out. The caller's attribution metadata (GW-13) is in a usage event,
  because the event goes to the tenant that attached it.
- **The cost is the ledger's estimate**, as recorded, in its currency, never converted. It is not an amount to
  charge ([ADR 0014](../adr/0014-events-outbox.md)).
- **The limits a record counted against are not in the event**: the ledger does not record them. The event
  carries `limit_state`, how limits treated the request (`limited`, `unlimited`, `unverified`,
  `not_evaluated`).
- **One usage event for each usage record**, with the record's own ID: a record the ledger writer skipped as a
  duplicate has no second event. A limit event is one event for each `limit_event` row, so a threshold fires once
  for a period.
- **Not events:** `limit.unlimited_traffic`, a change of the active currency, refused grants, refused support
  attempts and audit rows. Audit rows are read with the audit export.
- The platform's events hold no figure of usage and name no person.

## 3. The schemas

A JSON Schema (draft 2020-12) of each type, **envelope included**, is published in
[`docs/reference/events/`](events/), one file for each type, named by the type without its prefix:

| Type | Schema |
|---|---|
| `io.pandokos.usage.recorded.v1` | [`usage.recorded.v1.schema.json`](events/usage.recorded.v1.schema.json) |
| `io.pandokos.limit.threshold_reached.v1` | [`limit.threshold_reached.v1.schema.json`](events/limit.threshold_reached.v1.schema.json) |
| `io.pandokos.limit.exhausted.v1` | [`limit.exhausted.v1.schema.json`](events/limit.exhausted.v1.schema.json) |
| `io.pandokos.tenant.deletion_requested.v1` | [`tenant.deletion_requested.v1.schema.json`](events/tenant.deletion_requested.v1.schema.json) |
| `io.pandokos.tenant.deletion_cancelled.v1` | [`tenant.deletion_cancelled.v1.schema.json`](events/tenant.deletion_cancelled.v1.schema.json) |
| `io.pandokos.tenant.deleted.v1` | [`tenant.deleted.v1.schema.json`](events/tenant.deleted.v1.schema.json) |
| `io.pandokos.kek.version_activated.v1` | [`kek.version_activated.v1.schema.json`](events/kek.version_activated.v1.schema.json) |
| `io.pandokos.kek.version_retired.v1` | [`kek.version_retired.v1.schema.json`](events/kek.version_retired.v1.schema.json) |
| `io.pandokos.destination.test.v1` | [`destination.test.v1.schema.json`](events/destination.test.v1.schema.json) |

The files are the ones embedded in the binary: `make generate` copies `internal/outbox/schema/*.schema.json`
here, and a repository test compares the two. `pandokos event schema` lists the types and
`pandokos event schema <type>` prints a schema without a connection. No `dataschema` attribute is sent: the
project publishes no URL for the schemas yet. The tests validate real events (a usage record written through the
ledger, a limit event, a platform event) against them.

## 4. The two logs

| | The tenant's log | The platform's log |
|---|---|---|
| Types | `usage.recorded`, `limit.threshold_reached`, `limit.exhausted` | `tenant.deletion_requested`, `tenant.deletion_cancelled`, `tenant.deleted`, `kek.version_activated`, `kek.version_retired` |
| Written by | Triggers of `usage_record` and `limit_event`, in the transaction of the fact | Triggers of `tenant_deletion` and `kek_version`, in the transaction of the fact |
| Sequence | One for each tenant; follows the order of commits | One for the installation |
| Read by | The tenant: webhook destinations and `exportEvents` (`event:read`) | The platform: `exportPlatformEvents` (`platform_event:read`) |
| Kept | `events.retention` (168 h by default) | Always |

A tenant's events are the tenant's ([ADR 0054](../adr/0054-usage-data-is-the-tenants.md)): no platform
permission, operation or function returns one, or the count of one tenant's.

## 5. Versioning

- The version is the last part of the type (`.v1`). **Adding a member to `data`, or an attribute to the
  envelope, is not a new version**: a receiver ignores what it does not know.
- **Removing or renaming a member, changing its type or its meaning, is a new version** (`.v2`). The new type
  is written beside the old one for as long as the old one is supported; a destination chooses the types it
  receives, so a receiver moves when it is ready. The schema of the old version is kept.
- The data of an event is built by the database when the event is written, and a change of it is a migration;
  an event already written keeps the version it was written with.
- A new type is a trigger or a call of the append function in the transaction of its fact, a schema, a row
  here and the type in the check of `event_destination`.

## 6. Where the rest is

| What | Where |
|---|---|
| Destinations, retries, replay, rewind, the signature, the export | [events guide](../operations/events.md) |
| The operations and their audit actions | [administration API](admin-api.md#events-and-event-destinations-sprint-64) |
| The permissions | [permissions](permissions.md) |
| Keys, log lines, instruments | [configuration](../operations/configuration.md), [logs](logs.md#events), [metrics](metrics.md) |
| Failure | [failure behaviour](failure-behaviour.md) |
