# 0070. The outbox as a log: relay by destination, and retention

- Status: accepted
- Date: 2026-11-23

Refines [ADR 0014](0014-events-outbox.md) ("Relay" and "Guarantee"); builds on
[ADR 0039](0039-usage-record-admission-and-ledger-writer.md), [ADR 0054](0054-usage-data-is-the-tenants.md),
[ADR 0065](0065-the-audit-hash-chain-reading-and-retention.md) and
[ADR 0067](0067-tenant-deletion-stages-closed-tenants-and-kept-records.md). Requirements EVT-01, EVT-03, EVT-04,
LIM-13; sprint 6.4.

## Context

ADR 0014 says: the transaction that records a fact also inserts an outbox row; the worker claims pending *events*
with `FOR UPDATE SKIP LOCKED`, delivers each one and marks it delivered. That works for one receiver. Written as
it is, it means a row per event and destination that is written and then updated for every delivery, and an
outage of a receiver that writes retries for every event. It also leaves open how events keep their order for a
reader (a consumer that asks for "everything after n" must not miss one that commits late), how long an event is
kept when a receiver never takes it, and what happens to undelivered events when a tenant is deleted.

## Decision

1. **Events are rows of an append-only log for each tenant**, `outbox_event`, written only by triggers of the
   tables that hold the facts (`usage_record`, `limit_event`) in the transaction of the fact: a usage record cannot
   be written without its event, and no event exists for a fact that did not commit. The database writes events;
   Go code does not (a repository test keeps it so, and a lint rule keeps the triggers present and enabled). The
   platform has a log of its own (`platform_outbox_event`) written by the triggers of `tenant_deletion` and
   `kek_version`. (ADR 0072 says which facts and what they carry.)
2. **The sequence follows commit order.** `seq` comes from `outbox_stream`, one row for each tenant; the first
   event of a transaction locks that row and the lock is held to the end of the transaction. So when a reader has
   seen `seq = n`, every event with a smaller number is visible to it, and "every event after `n`" is `seq > n`,
   exactly. A rollback leaves no gap (the reservation of numbers rolls back with the transaction), but a consumer must not assume the sequence it sees is gapless: type filters and retention leave holes. Transaction IDs were the
   alternative; they break on dump and restore. **Lock order:** a transaction that also writes the audit chain of
   the same log locks the chain first; the platform's append function does, and no tenant event of this sprint
   writes an audit row.
3. **The relay claims destinations, not events.** `outbox_relay_claim` gives a destination, with a lease, to one
   worker at a time (`FOR UPDATE SKIP LOCKED`); the destination has a cursor in the log, `cursor_seq`: the event up
   to which everything is settled (delivered, parked, not of its types, or removed by retention). A round reads the
   events after the cursor, sends them (up to four requests at a time), and settles in one conditional transaction
   that moves the cursor and writes the failure state. A delivery that succeeds writes no row; an outage writes
   nothing at all. The lease is the longer of 60 seconds and six delivery timeouts.
4. **The head of a queue is retried with backoff and then parked.** When the event at the head fails, the
   destination waits `min(5 s × 2^(failures − 1), events.delivery.backoff_max)` less up to 20 % and sends that event
   alone; after `events.delivery.max_attempts` it becomes a row of `outbox_dead_letter` and the cursor moves past
   it. One event a receiver cannot accept delays the others for a bounded time and never blocks them; a receiver
   that is down keeps its queue, in order, in the log. **Replay** sends dead letters again; **rewind** sets the
   cursor back to a sequence number or a time.
5. **At least once, no order guaranteed.** A worker that dies between the request and the settlement leaves its
   lease to expire; the next claim sends the same events again. The event ID is stable and is the receiver's
   idempotency key. Events are sent in order in the normal case; a batch goes in parallel and a replay is out of
   order.
6. **Events live for a configured time, delivered or not.** `events.retention` (168 hours by default) removes events
   oldest first, under the retention switch of ADR 0065; an event an enabled destination had not passed is removed
   too, its cursor is moved past it and it is counted (`expired_events`, `pandokos.events.expired`). So "no event is
   lost" (EVT-03) holds within the retention, and storage is bounded by something other than a receiver's health.
   The ledger and its export remain the record. A deleted tenant's undelivered events are removed with the tenant:
   delivery continues during the grace period, and the platform learns of the deletion from its own events.
   Limit events live as long as usage, not as long as events.
7. **Events are written for every tenant**, whether it has a destination or ever pulls (no opt-in), from the
   migration on: nothing is produced for the past. A pull export (`seq > after`) works without registration.

## Consequences

- No row is written per delivery and an outage writes nothing; the log is both the queue and the pull source.
- The event doubles the rows of a ledger batch and adds one row lock per batch. The first measurements are in the
  events guide (a batch of 500 records: 1.34 times the cost without the trigger); the lock is per tenant and held
  for one statement, and its behaviour under concurrent writers of one tenant is measured in sprint 10.1.
- A later event type that also writes the tenant's audit chain must lock the chain before the stream.
- An event not delivered within the retention is lost to the destination, visibly. A tenant that needs more asks for
  a longer retention or pulls.
- The relay and the janitor belong to the worker role, which needs the key files (it opens signing secrets) and
  the egress settings (ADR 0071).

## Alternatives considered

**A delivery row for each event and destination (ADR 0014 as written).** A row written and updated for every
delivery, and an outage that writes for every retry.

**Transaction IDs as the order.** They do not survive a dump and restore.

**Events written by the Go writers.** A second statement for each batch, and a writer that can forget; the
trigger is structural.

**Keeping undelivered events without limit.** Storage would depend on a receiver's health.

## History

- 2026-11-23: proposed with sprint 6.4.
- 2026-11-24: accepted by the maintainer after review.
