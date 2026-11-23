# Events for external systems

How Pandokos tells other systems what happened: a usage record was written, a limit was reached, a tenant was
deleted. A billing system, a data warehouse or an alerting tool receives the events by **webhook**, or reads them
by **pull**, in one open format (EVT-01 to EVT-04, LIM-13). This page is for the operator who runs the worker and
for the tenant administrator who registers destinations. The types and their schemas are in the
[event reference](../reference/events.md); the decisions are
[ADR 0070](../adr/0070-the-outbox-as-a-log-relay-by-destination-and-retention.md),
[ADR 0071](../adr/0071-webhook-destinations-signature-secrets-and-egress.md) and
[ADR 0072](../adr/0072-event-types-the-envelope-the-external-reference-and-platform-events.md).

## 1. What an event is, and when it is written

An event is a row of an append-only log, one log for each tenant and one for the platform. **The database writes
it, in the transaction of the fact it announces**; no Go code inserts one:

| Fact | Event |
|---|---|
| A usage record is written by the ledger writer | `io.pandokos.usage.recorded.v1`, one for each record actually written |
| A limit crosses a threshold, or is exhausted | `io.pandokos.limit.threshold_reached.v1`, `io.pandokos.limit.exhausted.v1` |
| A tenant deletion is requested, cancelled or completed | `io.pandokos.tenant.deletion_requested.v1`, `…deletion_cancelled.v1`, `…deleted.v1` (platform log) |
| A key version is activated or retired | `io.pandokos.kek.version_activated.v1`, `…version_retired.v1` (platform log) |

So a usage record cannot be written without its event, and no event exists for a fact that did not commit. The
call of a caller never waits for an event or a destination: events are written with the ledger's own batch, and
delivery is the worker's job. Events are written **for every tenant**, whether it has a destination or not, from
the moment migration 0017 is applied: **nothing is produced for the past**. A destination created later receives
what is recorded after it; a consumer that needs the past reads the usage export ([usage guide](usage.md)).

The sequence number (`sequence`) of a tenant's log follows the order of commits: when a reader has seen
sequence `n`, it has seen every smaller number. A transaction that rolls back leaves no gap: its reservation of
numbers rolls back with it. Do not assume the numbers a consumer sees are gapless all the same: a destination
with a type filter skips the events of other types, and retention removes old events.

What is **not** an event: audit rows (read them with the [audit guide](audit.md)), a request that was refused, a
change of the active currency, unlimited traffic. A usage event holds no content: no prompt and no completion.

## 2. Destinations

A tenant registers up to **8** destinations: a name (unique in the tenant), an `https` URL, the types it wants
(none given: every type of the tenant's log) and whether it is enabled. A destination starts at the head of the
log: it receives the events recorded after it was created.

```
pandokos event-destination create billing --tenant acme \
    --url https://billing.example.com/pandokos/events \
    --event-type io.pandokos.usage.recorded.v1
```

The command prints the signing secret **once**, as `whsec_<base64>`, and says so. The server generates it; a
caller cannot supply one, and it is never shown again.

- **The URL** is `https`, at most 2048 characters, with no user information and no fragment. `http` is accepted
  only for a host the operator lists in `events.webhook.private_hosts`, and only those hosts may resolve to
  private addresses ([section 5](#5-the-request-and-how-to-verify-it)). **The URL is configuration, not a secret:**
  every holder of `event_destination:read` reads it. Put no token in it; the signature is what authenticates a
  delivery. The audit log keeps the host, never the path or query.
- **Permissions.** `event_destination:read` (owners, tenant administrators, auditors, read-only; a support action
  may carry this one), `event_destination:write` and `event_destination:delete` (owners and tenant
  administrators), `event:read` for the pull export. See [permissions](../reference/permissions.md).
- **State.** `pandokos event-destination get` shows `delivery`: `state` (`idle`, `delivering`, `failing`,
  `disabled`), the `cursor` (every event up to it is settled for the destination) and the `head` of the log, the
  consecutive `failures`, `next_attempt_at`, the last success and failure, `last_error`, `last_status`, the number
  of `dead_letters` and of `expired_events`, and `test`, the last test request.
- **Change.** `update` changes the name, description, URL, types or `enabled`. Enabling a disabled destination
  clears its failure state; it resumes at its cursor. Disabling stops delivery; the log keeps growing and the
  destination catches up when it is enabled again, within the retention.
- **Delete** removes the destination with its dead letters. The events stay in the log.
- **Test.** `pandokos event-destination test billing --wait` asks the worker to send one signed event of type
  `io.pandokos.destination.test.v1`, to a disabled destination too. The administration only records the request
  (it answers `202`); the **worker** makes the request, within a relay interval, and the outcome appears as
  `test` of the destination (the command polls every 500 ms for up to two minutes). A test event is not written to
  the log and moves no cursor or failure state. A test request made while another test is in flight stays
  pending: the outcome of the first is not recorded for it, and the next round sends a new test event.
- **Audit.** Every change is a row of the tenant's audit log: `event_destination.create`, `.update`,
  `.rotate_secret`, `.rewind`, `.replay`, `.delete`, with the name and the host of the URL, never a secret.

Destinations are not part of `pandokos export` and `apply` or of the tenant data export; the first joins
sprint 12.3.

## 3. Delivery: retries, dead letters, replay and rewind

The relay of the worker role works destination by destination, not event by event. A destination is **claimed**
with a lease (the longer of 60 s and six delivery timeouts), so that one worker holds it at a time even when
several workers run; its events after the cursor are read, sent with up to four requests at a time, and the cursor
is moved past what was settled. A delivery succeeds on a `2xx` answer. Every other outcome fails:

| Outcome (`pandokos.delivery.outcome`) | When |
|---|---|
| `delivered` | status 200 to 299 |
| `status` | any other status; the code is kept as `last_status` |
| `redirect` | a `3xx`: redirects are never followed |
| `timeout` | no complete answer within `events.delivery.timeout` |
| `connection`, `tls` | the connection or the handshake failed |
| `blocked` | the egress guard refused the address or the scheme |

**Backoff.** When the event at the head of a destination's queue fails, the destination waits
`min(5 s × 2^(failures − 1), events.delivery.backoff_max)` less a random part of up to 20 %, and the next round
sends that event alone. When it succeeds the counter is cleared and the queue flows again.

**Dead letters.** After `events.delivery.max_attempts` attempts (10 by default: about 51 minutes of backoff with
the defaults) the event is **parked** as a dead letter, the cursor moves past it and the next event is tried at
once. One event that a receiver cannot accept therefore delays the others for a bounded time and never blocks
them. A receiver that is **down** keeps its whole queue, in order, in the log; the few events that reached the
attempt limit during the outage are dead letters.

```
pandokos event-destination dead-letters billing --tenant acme
pandokos event-destination replay billing --tenant acme            # every dead letter
pandokos event-destination replay billing --tenant acme --seq 1204 # chosen ones
```

A replay sends each chosen dead letter once, out of order by nature; a success removes the row, a failure parks it
again with one more attempt.

**Rewind** sets the cursor of a destination back, to a sequence number (`--after`) or to the last event recorded
before a time (`--since`): everything after it that the log still holds is delivered again. The cursor never
moves forward by a rewind, and events that retention removed are not delivered again. A rewind clears the failure
state; a round in flight settles nothing.

**At least once, no order guaranteed.** A worker that stops between the receiver's answer and the settlement
leaves its lease to expire, and the next claim delivers the same events again. A receiver must therefore treat the
event `id` as its idempotency key. Events of a destination are sent in order in the normal case, but a batch is
sent in parallel and a replay is out of order.

**A failure that is not the destination's** (the database, or a signing secret that cannot be opened because the
key-encryption key is unavailable) consumes no attempt: the round is abandoned, the lease expires and the line
`event relay failing` says so. Tenants that are `suspended` still have their pending events delivered; a tenant
that is being deleted has them removed (section 8).

## 4. The pull export

A system that prefers to pull reads the log directly, with no destination:

```
pandokos event export --tenant acme --after 0 --limit 1000 --out events.ndjson
pandokos platform-event export --type io.pandokos.tenant.deleted.v1
```

`GET /v1/tenants/{tenant}/events/export?after=<seq>&limit=<n>&type=<type>` (`event:read`) answers
`application/x-ndjson`: the events with `sequence` greater than `after`, oldest first, one CloudEvents document
per line, at most `limit` (1 to 10000, default 1000). Keep the `sequence` of the last line and ask again with it:
nothing is missed and, unlike a webhook, each event is seen once. An `after` older than what retention kept starts
at the oldest event. `GET /v1/platform-events/export` (`platform_event:read`) is the same over the platform's log.

## 5. The request and how to verify it

```
POST <url>
Content-Type: application/cloudevents+json; charset=utf-8
User-Agent: pandokos/<version>
webhook-id: <event id>
webhook-timestamp: <seconds since the epoch, when the attempt is made>
webhook-signature: v1,<base64 of HMAC-SHA256(key, "<webhook-id>.<webhook-timestamp>.<body>")>
```

The headers and the signed text are those of the [Standard Webhooks](https://www.standardwebhooks.com/)
specification, so an existing library of a receiver's language can verify them. The key is the 32 random bytes
inside `whsec_<base64>`. A receiver **must**:

1. Verify the signature over the body **exactly as received**, in constant time.
2. Refuse a `webhook-timestamp` more than five minutes from its clock, which limits the replay of a captured
   request.
3. Use `webhook-id` (the event `id`) to ignore a delivery it has already handled.
4. Answer `2xx` quickly. Any other answer, or none within `events.delivery.timeout`, is a failed attempt.

[`examples/webhook-receiver`](../../examples/webhook-receiver) is a small receiver in Go,
standard library only: it checks the three headers, accepts the two secrets of a rotation, keeps the IDs it has
seen in a file and answers `204` to a repeat. Its signature check is written independently of the sender's, and
the two agreeing is a test of the repository.

**Rotating the secret.** `pandokos event-destination rotate-secret billing --previous-valid-for 24h` prints a new
secret once. The previous one keeps signing, beside the new one, for the time given (24 h by default, at most
7 days, `0s` stops it at once), and `webhook-signature` carries two signatures separated by a space. Configure the
receiver with both, then drop the old one. Rotating twice within the window drops the first secret.

**Where a request goes.** Requests leave from the **worker** only, through the egress guard
([ADR 0038](../adr/0038-egress-guard-upstream-tls-and-proxy.md)): `https`, public addresses checked on the address
dialled, TLS 1.2 or later with verification, the outbound proxy when one is configured, **no redirect followed**,
one deadline, and a response body read up to 64 KiB and discarded; nothing of the answer but the status is kept.
A host listed in `events.webhook.private_hosts` may be `http` and may resolve to private addresses (a receiver
inside your network, a test). List only hosts you control: a tenant administrator can point a destination at any
listed host.

## 6. Retention, and the bound of EVT-03

Events are kept for `events.retention` (168 h by default, 24 h to 2160 h, whole hours), **delivered or not**; the
rule is time alone, and it bounds how far back a destination can be rewound. An event that an enabled destination
had not passed when it expired is removed, the destination's cursor is moved past it, and it is counted: in
`expired_events` of the destination and in `pandokos.events.expired`. This is the bound of EVT-03, "no event is
lost": **nothing is lost within the retention**; after it, the ledger still holds the usage, and the usage export
reads it. Dead letters go with their event.

Limit events are usage data and live as long as usage (`retention.usage_months`), not as long as events. The
platform's events are kept. The janitor of the worker role runs hourly. Disabling a destination does not stop
the clock: a destination disabled for longer than the retention loses events.

## 7. The platform's events

Five types announce deletions and key versions to the operator's own systems. They have **no webhook** (the vault
seals signing secrets under tenants' data keys, and the platform has none) and are pulled with
`pandokos platform-event export` (`platform_event:read`, held by `platform-admin` and `platform-auditor`). They
name no person and hold no usage. The deletion events carry the tenant's **external reference**, the name the
operator's other systems know the tenant by: the platform sets it with `tenant create|update
--external-reference` (`tenant:write`; up to 255 characters; not unique), and every tenant event carries it as
`tenantref`. The completed deletions are listed by `tenant.deleted` events: keep that list outside the database
([backup guide](backup-and-restore.md#5-after-a-restore)).

## 8. What a deletion does

- From the request to the start (the grace period, the tenant `suspended`) the relay keeps delivering what is
  pending.
- When the deletion starts, its step `access` removes the destinations and their dead letters, because a webhook
  is a way out for the tenant's data and its secret is about to become unreadable. The step `configuration`
  removes the tenant's events.
- **Events not delivered when the deletion begins are removed, not delivered.** The receiver had the grace period,
  and the platform learns of the deletion by `tenant.deleted`. A round in flight for a closed tenant settles
  nothing. See the [deletion guide](tenant-deletion.md).
- Destroying the tenant's data key makes the signing secrets unreadable for good.

## 9. Keys

All keys are read by `serve` only; see the [configuration reference](configuration.md) for the full table.

| Key | Default | Meaning |
|---|---|---|
| `events.source` | `urn:pandokos` | The `source` of every event: one value for the installation |
| `events.retention` | `168h` | How long events are kept, and how far back a destination can be rewound |
| `events.relay.interval` | `1s` | Time between two passes of the relay; a pass repeats at once while a batch was full |
| `events.relay.workers` | `4` | Destinations this process delivers to at once |
| `events.relay.batch_size` | `100` | Events read for one destination in one round |
| `events.delivery.timeout` | `10s` | Time allowed to one request |
| `events.delivery.max_attempts` | `10` | Attempts before an event is parked |
| `events.delivery.backoff_max` | `15m` | Longest wait between two attempts |
| `events.webhook.private_hosts` | none | Hosts whose URLs may be `http` and resolve to private addresses |

Eight destinations for each tenant and four requests at a time within a destination are fixed numbers.

## 10. What to watch

The seven instruments of [metrics](../reference/metrics.md) carry no tenant, destination or URL. Alert on:

| Instrument | Alert when | It means |
|---|---|---|
| `pandokos.events.lag` | above your tolerance (minutes) for a sustained time | An enabled destination has not settled the oldest event: a receiver is down or slower than the traffic. The age of the oldest unsettled event, 0 when none |
| `pandokos.events.dead_letters` | any increase | Events were parked: a receiver refused them ten times |
| `pandokos.events.expired` | any increase | Events were removed before an enabled destination received them: raise `events.retention`, fix the receiver, or pull |
| `pandokos.events.relay.passes` with `pandokos.result=failed` | a run of failures | The database, or the key file, is unavailable to the worker |
| `pandokos.events.destinations` with `pandokos.destination.state=failing` | not zero for long | Receivers that fail |

`pandokos.events.deliveries` by outcome and `pandokos.events.delivery.duration` show what failures are made of.
The log lines are `event relay ready`, `event relay failing`, `event relay recovered`, `event deliveries failing`,
`event retention applied` and `event retention failing` ([log reference](../reference/logs.md#events)); none names
a tenant, a destination or a URL. What happens when the receiver or a worker fails is in the
[failure reference](../reference/failure-behaviour.md).

A tenant sees its own side in `event-destination get` (`failures`, `last_error`, `dead_letters`,
`expired_events`); it cannot see another tenant's, and the operator cannot read a tenant's events.

## 11. The cost on the ledger

The event doubles the rows of a ledger batch and adds one row lock per batch. The sprint measured it with the
test of the repository (`TestOutboxWriteCost`, T-1050): batches of 500 records written through the statement of
the ledger writer on a development machine, the median of the runs.

| | Median of a batch of 500 records |
|---|---|
| Without the trigger | 108.7 ms |
| With the trigger | 145.5 ms |
| Ratio | 1.34 |

The test fails above three times the cost without the trigger. `make bench` runs `BenchmarkLedgerBatchOutbox`
(it needs `make db-up`); the figures depend on the machine, so run it on yours before relying on them. The lock is
held for the one statement of the batch and is per tenant: measuring it under concurrent writers of one tenant and
a log of ten million events is sprint 10.1.

## 12. Limits of this version

- No platform webhook, no destination kind other than a webhook, no batching of events in one request, no
  automatic disabling of a failing destination.
- Destinations and the external reference are not in `export`/`apply` (sprint 12.3).
- The next claim of the relay waits for the slowest round of a pass: a receiver that answers slowly, though it
  succeeds, holds back the destinations of other tenants until its round ends (a round's own time is bounded:
  no request starts after half the lease, counted from the start of that round). Sprint 10.1 measures it.
