# 0012. Limits: reserve and settle, with in-process or Valkey counters

- Status: accepted
- Date: 2026-11-11

## Context

Rate limits, quotas and monetary budgets must be checked before a request is forwarded, although
its real cost is known only afterwards, and must hold when many requests run at once and when
several instances serve the same tenant (LIM-01 to LIM-12). The default installation has no
dependency beyond PostgreSQL. A database round trip per request on the hot path is not acceptable
at scale.

## Decision

**Semantics: reserve, then settle.**

1. Before forwarding, the gateway computes an estimate of the request (one request; estimated
   input tokens; output tokens up to the request's limit; the resulting cost) and atomically
   reserves it against every applicable limit: platform, tenant, key, and per model or provider
   where configured. If any reservation fails, all are released and the request is refused with
   the limit that failed.
2. After the response, the reservation is replaced by actual usage. An unused remainder returns to
   the allowance.
3. A reservation that is never settled (a crashed instance) expires on its own.

**Kinds of limit.**

- Rate limits (requests and tokens per minute): sliding-window counters.
- Quotas and budgets per day or month: a counter per period, in the configured time zone.
- Concurrency: a counter of in-flight requests.

**Guarantees, stated in the documentation.**

- Request-rate and concurrency limits are strict.
- Token and money limits are strict with respect to *reserved* amounts. Actual usage can exceed
  the estimate when the caller sets no output limit; the overshoot is bounded by the model's
  maximum output for each request in flight when the limit is reached.
- A request that fails upstream settles at zero unless the provider reports usage. A cancelled
  stream settles at the usage observed or estimated so far.

**Counter stores**, behind one interface:

- *In process*, for a single instance. Counters live in memory and are rebuilt at start from
  ledger aggregates for the current periods.
- *Valkey* (any server speaking the Redis protocol), required with more than one gateway
  instance. Reserve and settle are single atomic scripts.

PostgreSQL remains the source of truth: the worker periodically reconciles period counters with
ledger aggregates and corrects drift. Rate-limit windows are not reconciled; they are short-lived
by nature.

**When the counter store is unreachable** the gateway refuses requests by default. An operator can
choose to continue with per-instance counters, accepting that limits become approximate.

Limit refusals return HTTP 429 with `Retry-After`, remaining-allowance headers and a code that
distinguishes them from a provider's own 429.

## Consequences

- No database access on the hot path for limits.
- Valkey becomes a dependency of multi-instance deployments, and of their availability unless the
  operator relaxes the default.
- Budgets are enforced on estimated cost. Where the price is unknown the budget cannot be enforced
  and platform-funded use is blocked ([ADR 0011](0011-pricing-source.md)).

## Alternatives considered

**Counters in PostgreSQL.** Exact and dependency-free, but a write per request on the hot path
and row contention on busy tenants.

**Check only, without reservation.** Simple, and concurrent requests overshoot without bound.

**Post-paid enforcement** (block after the budget is exceeded). Acceptable for soft thresholds,
not for a hard budget on someone else's money.

**A gossip-based or approximate distributed limiter.** Removes the dependency, at the cost of
guarantees that are hard to state and test.
