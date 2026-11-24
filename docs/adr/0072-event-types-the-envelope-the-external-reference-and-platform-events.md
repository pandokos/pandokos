# 0072. Event types, the envelope, the external reference and the platform's events

- Status: accepted
- Date: 2026-11-23

Refines [ADR 0014](0014-events-outbox.md) ("Format" and "Content of a usage event") and builds on
[ADR 0054](0054-usage-data-is-the-tenants.md), [ADR 0070](0070-the-outbox-as-a-log-relay-by-destination-and-retention.md)
and [ADR 0071](0071-webhook-destinations-signature-secrets-and-egress.md). Requirements EVT-01, EVT-02, LIM-13;
sprint 6.4.

## Context

ADR 0014 chose CloudEvents 1.0 in JSON and listed four type names. What an event holds, how it is versioned, who
may read which log, and how an operator's other systems recognise a tenant were open.

## Decision

1. **CloudEvents 1.0, structured JSON.** A webhook body and a line of the pull export are the same document:
   `specversion`, `id` (stable, the receiver's idempotency key), `source` (`events.source`, default `urn:pandokos`),
   `type`, `time`, `subject`, `datacontenttype`, the extensions `tenantid`, `tenantref` (the external reference,
   absent when empty), `sequence` (a string of digits), and `data`. `data` is built by the database when the event
   is written and never changed; the envelope is rendered by `internal/outbox` when the event is delivered or
   exported.
2. **Nine types, versioned in the name.** A tenant's log: `io.pandokos.usage.recorded.v1`,
   `io.pandokos.limit.threshold_reached.v1`, `io.pandokos.limit.exhausted.v1`. The platform's log:
   `io.pandokos.tenant.deletion_requested.v1`, `…deletion_cancelled.v1`, `…deleted.v1`,
   `io.pandokos.kek.version_activated.v1`, `…version_retired.v1`. Not in a log: `io.pandokos.destination.test.v1`.
   A breaking change of `data` is a new version, delivered beside the old one; adding a member is not. A JSON
   Schema (2020-12) of each type, envelope included, is embedded in the binary, published in
   `docs/reference/events/` by `make generate` and printed by `pandokos event schema`. No `dataschema` attribute is
   sent: the project publishes no URL for it.
3. **The usage event** holds the record's quantities by token category, the estimated cost and its status in its
   currency (never converted; the ledger's estimate, not an amount to charge), the price version, the principal and
   key IDs, the request and trace IDs, the provider, model and outcome, `limit_state`, and the caller's attribution
   metadata, because the event goes to the tenant that attached it. **No content** and not the provider's own
   usage object. The limits a record counted against are not in it: the ledger does not record them.
4. **What is not an event:** `limit.unlimited_traffic`, a change of the active currency, refused grants and refused
   support attempts, and audit rows. Audit rows as events can be a later type.
5. **The external reference** is `tenant.external_reference`: up to 255 characters, no control character, empty by
   default, not unique (two tenants may be one customer). It is the operator's name for the tenant. **The platform
   sets it** (`tenant:write`, in `TenantCreate` and `TenantUpdate`); a trigger refuses a change outside the platform
   scope. Every event of a tenant carries it as `tenantref`, and a deletion copies it into the deletion row at the
   request so that the platform's deletion events can name it after the tenant is gone.
6. **The platform's log is pull only**, `exportPlatformEvents` with `platform_event:read` (held by both platform
   roles). It is written by the triggers of `tenant_deletion` and `kek_version`, holds no usage figure and names no
   person, and is not removed by retention. There is no platform webhook; the vault seals under tenants' data keys
   only, so a webhook for the platform's events waits for sprint 12.1. A restore replays the deletions listed by
   `tenant.deleted` events after the backup.
7. **Events of a tenant are the tenant's** (ADR 0054): no platform permission, operation or function returns one,
   or a count of one tenant's. A support action can carry `event_destination:read` and nothing else of events. The
   five permissions are `event_destination:read`, `:write`, `:delete`, `event:read` and `platform_event:read`.

## Consequences

- A receiver learns a type from its name and knows what changes are safe; a new type is a trigger or a call of the
  append function, a schema, a row of the reference and the type in the table check of `event_destination`.
- Metadata of the caller travels to the tenant's own receiver, so a tenant must not put in it what it does not
  want to send there.
- The external reference is set by the operator, not by the tenant, so the platform's events can be matched to the
  operator's systems.
- Destinations and the external reference are not in `export` and `apply` (sprint 12.3).

## Alternatives considered

**Binary content mode** of CloudEvents: the document would be split between headers and body, and a pull line has
no headers.

**A platform webhook with a secret in the configuration:** a secret outside the vault, with none of its
protections.

**The reference set by the tenant:** the platform's events depend on it, and a tenant could take another's.

## History

- 2026-11-23: proposed with sprint 6.4.
- 2026-11-24: accepted by the maintainer after review.
