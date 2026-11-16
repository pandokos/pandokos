# 0014. Events for external systems: transactional outbox and CloudEvents webhooks

- Status: accepted
- Date: 2026-11-11

## Context

Usage and limit notifications must reach external systems reliably (EVT-01 to EVT-04, LIM-13)
without Pandokos becoming a billing system and without adding a message broker to every
installation.

## Decision

- **Outbox.** The transaction that records a fact also inserts the event that announces it into
  an outbox table. The ledger writer inserts the usage row and its event together.
- **Relay.** The worker claims pending events with `FOR UPDATE SKIP LOCKED`, delivers them, and
  marks them delivered only on a successful response. Failures retry with exponential backoff;
  after a configured number of attempts an event is parked as dead and can be replayed through the
  API. Several workers can run at once.
- **Guarantee.** At least once. The event ID is stable across retries and is the receiver's
  idempotency key. Order is not guaranteed; events carry their own timestamps and sequence within
  a tenant.
- **Format.** CloudEvents 1.0 in JSON. Event types are versioned: `usage.recorded`,
  `limit.threshold_reached`, `limit.exhausted`, `tenant.deleted`.
- **Destinations.** First: an HTTPS webhook signed with an HMAC over body and timestamp, and a
  newline-delimited JSON export over the API for systems that prefer to pull.
- **Content of a usage event.** Quantities by token category, the price version, the estimated
  cost and its status, the tenant and its external reference, the originating request. No content.
  The cost is the estimate of the ledger, not an amount to charge.

## Consequences

- No broker to operate; delivery survives restarts because pending events are rows.
- Receivers must be idempotent. This is the norm for metering APIs and is documented with an
  example.
- Throughput is bounded by the relay, which is adequate for usage events and is not designed as a
  general event bus.

## Alternatives considered

**Publishing to a broker** (Kafka, NATS, RabbitMQ). Appropriate at scale and for many consumers; a
mandatory extra dependency otherwise. A broker destination can be added later without changing the
outbox.

**Calling the receiving system synchronously on the request path.** Couples inference availability
to a third party.

**A vendor adapter as the only output.** Locks the product to one receiving system.
