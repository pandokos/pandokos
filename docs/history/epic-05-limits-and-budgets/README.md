# Epic 5 — Limits and budgets: record

Completed 2026-11-18. Epic file as closed: [epic.md](epic.md).

## Outcome

A request is refused before it reaches a provider when it would pass a limit, and the caller is told
which one and when to try again. Rules limit requests per minute, tokens per minute and requests in
flight, and, over a calendar day or month in a configured time zone, requests, tokens and cost, for
a tenant, a service account or a key, optionally scoped to a provider or model. The platform admin
can set default ceilings for every tenant. The pipeline reserves an estimate after the model check
and settles with the usage record's own figures when the request finishes, so that what is counted
equals what the ledger stores. Refusals use the stable codes `rate_limited` (429), `quota_exceeded`
(429) and `no_price` (409, a budget that cannot measure a model with no price in its currency), with
`Retry-After` and limit headers, in the error shape of the API family, and each leaves a
`rejected_limit` usage record.

Period counters are rebuilt from the usage ledger at a tenant's first request and reconciled with it
by the worker, so a restart does not reset a budget; a store that cannot be read refuses with 503 or
admits, as `limits.on_store_failure` says. Soft thresholds and exhaustion are recorded as limit
events. Rules are written through the administration API and the CLI with their own permissions and
audit actions, and the capability check's `budget` field now reads `available`, `exhausted` or
`unpriced`. Counters are per gateway instance; the bound on the overshoot is documented in the
[limits guide](../../operations/limits.md).

## Sprints

| Sprint | Design and plan | Delivered | Review |
|---|---|---|---|
| 5.1 Reserve and settle | [sprint](5.1-reserve-and-settle/sprint.md), [plan](5.1-reserve-and-settle/plan.md) | PR #39, merged as f680d9d | 3 rounds (8 minor findings, then 1 minor, then none) and one CI fix (the container test expected schema version 9) |
| 5.2 Quotas and budgets | [sprint](5.2-quotas-and-budgets/sprint.md), [plan](5.2-quotas-and-budgets/plan.md) | PR #TBD | 3 rounds (1 major, the period counters of a tenant's first load near a period boundary were not loaded from the ledger, and 5 minor; then 1 minor, a race in the pruning of the reconciler's lock; then none) |

Each sprint followed the cycle of the earlier epics: a frozen design, tests committed failing before
the implementation, a review against the design repeated until it reported no findings, a pull
request with green CI. Open points raised by each design and the maintainer's decisions on them are
at the end of section 12 of each `sprint.md`; deviations accepted at review are in the pull requests
and under "Amendments" in section 12.

## Decisions recorded

ADR 0048–0049 (sprint 5.1: reserve and settle, the estimate, counters, guarantees and the 429
contract; limit rules, levels, subjects and resolution), ADR 0050–0052 (sprint 5.2: periods,
attribution, currencies and unknown prices; period counters and the ledger; administration of
limits), with history lines on ADR 0012, 0014, 0020, 0032, 0035, 0037, 0039 and 0043. See the
[ADR index](../../adr/README.md).

## Carried forward

- Sprint 6.1 (metrics): refusals by code, metric, subject kind and scope; requests admitted
  unverified or unpriced; reservations outstanding and period counters held; expired reservations
  (which must stay at zero); reconciliation runs, duration and failures; the lag of the cut (a cut
  that does not advance is an alert); limit events recorded and dropped; reserved against settled
  tokens and cost, the measure of the estimate's quality; rule-cache loads; the cost of the budget
  path on the hot path (about 105 allocations per request, 5.2) is measured and reduced here or in
  epic 8; the volume of `rejected_limit` rows under a retry storm (5.1 O-11) is revisited with these
  measurements.
- Sprint 6.2 (reports, rollups, retention): a rollup by day, service account, key, provider, model
  and currency makes the aggregate at counter load, at the hourly rebase and for each capability
  check cheap, and must use the same figures and the credential rule (`limits.Sum`); the "used
  against limit" endpoint (5.2 O-15, deferred); reports count `rejected_limit` rows apart from usage;
  the capability check reads the ledger on the worker pool, one aggregate per call, and ignores
  requests in flight, until the rollup exists.
- Sprint 6.3 (tenant deletion): removes the tenant's rows of `limit_rule`, `platform_limit_rule` and
  `limit_event`; the last is append-only for every role, so through a definer function.
- Sprint 6.4 (events): the outbox row is written in `EventWriter`'s transaction for the limit events
  recorded today in `limit_event`, the kinds are mapped to the CloudEvents types of ADR 0014, and
  the decision is taken whether a usage event carries the limits it counted against.
- Epic 8 (shared counters): a Valkey `Store` implements `Reserve`, `Settle` and `Release` over the
  same keys, with `Reservation.Expires` as the key lifetime and the same attribution of periods; the
  worker reconciles with the same cut, reading the oldest pending record of every instance; a
  gateway process without the worker role cannot load from the ledger and needs the shared store;
  `limits.on_store_failure` then covers a store that is really unreachable; the property tests of
  the counters run against it unchanged. Until then each instance enforces alone.
- Epic 12 (operability, Helm): `limits.time_zone` must be the same on every instance; a shipped
  default concurrency rule (5.1 O-4) can be created by `init` or the chart.
- Sprint 4.3 (roles): the six limit permissions are available to custom roles; a "limits manager"
  role is a candidate.
- Release R4 (unified endpoint): the estimate and the scopes of rules name the resolved provider and
  model; the output bound is per family.
- Not scheduled: allowance headers on admitted responses (5.1 O-7); a time zone per tenant (5.2 O-2);
  several budgets per scope in other currencies (5.2 O-5); a raise of the bound of six fraction
  digits and 10⁹ units of a budget amount (5.2 O-11) when someone needs it.

## Maintainer actions

- Migrations `0009_limit_rules` (5.1) and `0010_quotas_budgets` (5.2) are applied by `pandokos
  migrate` before the new binary starts. Both add only tables and columns with defaults, so they are
  quick and an old binary keeps working on the new schema; it ignores the metrics it does not know,
  so quotas and budgets are not enforced until the new binary runs.
- New configuration keys, all optional ([configuration reference](../../operations/configuration.md)):
  `cache.limits_ttl` (`30s`), `limits.default_output_tokens` (`4096`), `limits.time_zone` (`UTC`),
  `limits.on_unknown_price` (`refuse`), `limits.on_store_failure` (`refuse`) and
  `limits.reconcile_interval` (`5m`). Set `limits.time_zone` identically on every instance.
- No rule means no limit: an installation upgrades with nothing refused until a rule is written
  (platform defaults included; nothing is seeded). Write rules with the CLI or the administration
  API as described in the [limits guide](../../operations/limits.md).
- An installation with budgets and a model without a price in the budget's currency refuses that
  model for the tenants with the budget (`no_price`): add a price or set `limits.on_unknown_price`
  to `allow`.
- Run more than one instance only knowing that each counts alone until epic 8.
- The container and compose tests run only in CI, not in the sandbox of the implementation.
