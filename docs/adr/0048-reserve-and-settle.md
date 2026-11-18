# 0048. Reserve and settle in the pipeline: estimate, counters, guarantees and the 429 contract

- Status: proposed
- Date: 2026-11-17

Refines [ADR 0012](0012-limits-and-budget-reservation.md); refines [ADR 0020](0020-error-contract.md),
[ADR 0037](0037-request-pipeline-and-resolution.md) and [ADR 0039](0039-usage-record-admission-and-ledger-writer.md).

## Context

0012 fixed the semantics (reserve, settle, expiry), the kinds of limit and the guarantees in
outline. It left open where the reservation sits in the pipeline, how a request is estimated without
a tokenizer, what a sliding window is exactly, what "settle" uses, and the names of the 429
headers; its bound on the overshoot named the output only.

## Decision

1. The reservation is one atomic call between the model check and the credential, for every limit
   of the request, the concurrency slot included; settle or release happens once, in the handler's
   finish.
2. The estimate: input is the request's bytes over four through the family's estimator, capped by
   the model's context window; output is the caller's limit per choice times the choices, else a
   configured default, capped by the model's maximum output; a limit counts only when written as a
   plain positive integer; the cost is that usage at the price in force.
3. A rate window is 61 buckets of one second; an amount belongs to its admission second and is
   counted for at least 60 s; settle corrects that second while it is counted.
4. Settlement uses the figures of the usage record, whatever their source; a request that was never
   sent is released in full; a request that was sent stays counted as a request; a refused request
   consumes nothing; token counting is a request without tokens.
5. Guarantees: request rate and concurrency are strict; for tokens, in every 60 s interval the
   tokens of the requests admitted in it are at most the limit plus the sum, over those still in
   flight at the last admission, of what each used above its reservation; one request can exceed its
   reservation by at most the model's maximum output minus the reserved default when it set no limit,
   and by the context window minus the estimated input.
6. A reservation is a value with an expiry; the gateway sets it beyond the longest request; in
   process an expiry is a defect and is logged. Counters are per instance and are not rebuilt at
   start.
7. A refusal is 429 `rate_limited` in the family's shape with `Retry-After` and the headers
   `X-Pandokos-Limit-Subject`, `X-Pandokos-Limit-Metric`, `X-Pandokos-Limit`,
   `X-Pandokos-Limit-Remaining` and `X-Pandokos-Limit-Reset`; one code serves the three metrics; the
   usage record has outcome `rejected_limit`.

## Consequences

- The ledger and the counters agree by construction, which rebuilding counters from the ledger
  needs.
- A body that is large in bytes and small in tokens reserves more than it uses until it settles.
- A token limit below the largest request refuses that request for good.
- Limits multiply by the number of instances until the shared store exists.

## Alternatives considered

**Reserving the model's maximum output when the caller sets no limit.** One request in flight
exhausts a token limit.

**A tokenizer per provider.** A dependency per provider, and still an estimate.

**An exact log of events per counter.** Memory per request.

**The two-bucket weighted window.** Not strict.

**Attributing tokens to the time of settlement.** A long stream would be charged to a minute in
which nothing was admitted, and the bound could not be stated per interval.

**Taking the concurrency slot before the body is read.** Two calls that must be kept consistent.

**A code per metric.** One code with a metric header serves callers and SDKs as well.

## History

- 2026-11-18: sprint 5.2: a request counts when its credential was opened (its usage record carries a credential ID), replacing "forwarded"; a model can carry its own default output reservation ([ADR 0050](0050-quotas-and-budgets-periods-attribution-currencies.md)).
