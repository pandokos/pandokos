# 0051. Period counters and the ledger: load, cut, reconciliation, the failing store

- Status: accepted
- Date: 2026-11-18

Refines [ADR 0012](0012-limits-and-budget-reservation.md).

## Context

0012 says counters are rebuilt at start and reconciled by the worker; it does not say how a running
instance replaces what it counted by what the ledger holds without counting a request twice, or what
happens when the ledger cannot be read.

## Decision

1. A period counter is `base` (the ledger's figure for the requests of the period that started
   before the tenant's cut) plus `live` (this instance's reservations and settlements, by minute of
   request start).
2. The cut is the minute before which every request has ended and its record is committed, rejected
   or lost: now minus the horizon, held back by the oldest record handed to the ledger writer and not
   yet committed. It never moves back. The horizon is `limits.request_body_read_timeout` +
   `limits.max_request_duration` + 2 minutes (12 m 30 s with the defaults).
3. A tenant's counters are loaded on its first use; every `limits.reconcile_interval` (default 5 m)
   the requests the cut has passed are folded into `base`; once an hour `base` is recomputed. All of
   them read the ledger with the worker role in the tenant's session, through one query and one
   matching function (`limits.Sum`).
4. Rate windows and the concurrency gauge are not reconciled.
5. `limits.on_store_failure`: when a counter cannot be loaded, refuse with 503 `unavailable` (the
   default) or admit without the limits that could not be verified (`allow`). Rules that cannot be
   loaded always refuse.
6. The capability check computes `checks.budget` from the ledger, with the same matching function.
   It reads the ledger through the reader that `serve` builds on the worker pool, so that only the
   command composes it, with the worker database role inside the tenant's session under row-level
   security; the check sits in the administration plane, which is told the reader and never builds
   it.

## Consequences

- After a restart counters equal the ledger. Any discrepancy disappears one horizon and one interval
  after the request started (17 m 30 s with the defaults); anything else within the hour.
- Records that never reach the ledger are not counted: PostgreSQL is the source of truth.
- A gateway process without the worker role cannot load period counters; it needs the shared store
  of epic 8.
- With `allow` a quota or budget can be exceeded while the ledger is unreadable.

## Alternatives considered

**Loading every tenant at start.** Slow for many tenants and useless for the idle ones.

**Tracking each request until its record is committed.** Needs state across the writer and the
limiter for the sake of minutes.

**Reconciling by full recomputation only.** Needs quiescence or counts requests in flight twice.

**Counters in PostgreSQL.** Settled against in 0012: a write per request on the hot path.

## History

- 2026-11: `limits.on_store_failure` and `limits.reconcile_interval` are added in sprint 5.2.
- 2026-11-18: after review of sprint 5.2: a tenant without a cut loads every period that began before the process followed it, also one that starts inside the horizon (a restart just after midnight), and the cut and the loads are installed together.
- 2026-11-18: accepted by the maintainer after review. Point 5 (the ledger read with the worker role, also by the capability check) is reconsidered with sprint 8.2, when the roles run as separate processes.
- 2026-11-18: sprint 5.3: counters are keyed by provider and model identifier; fold pieces are cut at the boundaries of the periods the store holds, which serves two time zones around a change ([ADR 0050](0050-quotas-and-budgets-periods-attribution-currencies.md)).
