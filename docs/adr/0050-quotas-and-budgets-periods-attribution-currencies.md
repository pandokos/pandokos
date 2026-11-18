# 0050. Quotas and budgets: periods, attribution, currencies and unknown prices

- Status: accepted
- Date: 2026-11-18

Refines [ADR 0012](0012-limits-and-budget-reservation.md) and [ADR 0048](0048-reserve-and-settle.md).

## Context

0012 named a counter per period "in the configured time zone" and left open which requests a
period holds, what a budget's unit is, how several currencies behave and what happens to a request
whose price is unknown. Those four answers decide whether a counter and the usage ledger can ever
agree, and whether a budget can be trusted.

## Decision

1. Six metrics per day and month (`requests_per_day`, `requests_per_month`, `tokens_per_day`,
   `tokens_per_month`, `cost_per_day`, `cost_per_month`). One time zone per installation
   (`limits.time_zone`, `UTC` by default), taken from a zone database embedded in the binary. A
   period is the set of requests whose start has that civil date (or month), so days follow the
   zone's clock changes and last 23 or 25 hours when the clocks move.
2. A request counts exactly when its usage record carries a credential ID. This replaces "forwarded"
   of 0048 and differs from it in one narrow case: a caller that goes away, or a panic, between the
   opening of the credential and the call to the proxy is now settled (one request, no tokens)
   instead of released.
3. A budget is an amount in one currency, stored in millionths, counted with exact decimals, and it
   counts known costs in that currency only; nothing is converted.
4. A request a budget cannot measure (no price in force, or a price in another currency) is refused
   with 409 `no_price` by default, or admitted without being counted by that budget when
   `limits.on_unknown_price` is `allow`. An unknown cost at settlement counts zero.
5. Period limits refuse with 429 `quota_exceeded`; `Retry-After` is the time to the end of the
   period.
6. The bound of 0048 holds per period. For cost the excess of one request over its reservation is
   bounded by the cost of a full context window and a full output at the highest rates of the price
   version in force, minus the reserved cost.
7. A model can carry its default output reservation (`model.default_output_tokens`), used when the
   caller sets no limit.

## Consequences

- A counter and a ledger aggregate over the same period are the same sum: the period of a request
  is that of its start, which the usage record stores.
- A tenant with a budget cannot call a model without a price unless the operator says so; mixed
  currencies need one budget per currency, on narrower scopes or other subjects.
- Changing the zone moves every boundary and takes effect at restart; the events of the period in
  which it changed can be recorded once more.
- The binary is larger by the embedded zone database (about 0.4 MB).

## Alternatives considered

**Attribution by admission time.** Differs from the ledger at every boundary, so counters and
aggregates could never be reconciled.

**A zone per tenant or per rule.** Two rules on one counter could disagree on the period.

**Converting currencies.** Needs exchange rates the product does not have and makes a budget depend
on a moving number.

**Counting unpriced requests at zero silently.** A budget would be bypassed by choosing a model
without a price.

## Amendment (sprint 5.3)

Decided in the review of 2026-11-18; replaces "one time zone per installation" of decision 1, the
alternative "a zone per tenant or per rule" and the consequence "changing the zone ... takes effect at
restart" for tenants that have a zone of their own.

1. A tenant has a time zone (`tenant.time_zone`); `limits.time_zone` is the zone of the tenants that
   have none. The platform sets it (`tenant:write`), and so does the tenant (`limit:write`); both are
   audited.
2. A change takes effect at the start of the next month in the zone in force, for days and months
   alike. A period is always a whole civil period of the zone in force at the request's start, and its
   counter counts the whole range, so the first periods of a new zone include what was used in their
   hours before the change took effect: moving a boundary never frees a quota. Platform and tenant
   rules of one tenant still share one calendar.
3. A budget is refused on write (400 on `currency`, naming the currencies in use) when no catalogue
   price that can be in force for its scope is in its currency. Nothing is converted. Sprint 5.4 adds
   the tenant's own prices to that set.

## History

- 2026-11: `limits.on_unknown_price`, `limits.time_zone` and the code `no_price` are added in sprint 5.2.
- 2026-11-18: accepted by the maintainer after review. Amendments planned in sprint 5.3 (a time zone per tenant, effective from the next period; a budget refused on write when no price of its scope is in its currency) and sprint 5.4 (price lists per tenant, issue #48).
- 2026-11-18: amended by sprint 5.3 (see Amendment); tenant price lists remain planned for sprint 5.4.
