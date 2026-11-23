# 0064. Usage rollup, reports and exports

- Status: proposed
- Date: 2026-11-23

Refines [ADR 0013](0013-ledger-and-audit.md), [ADR 0051](0051-period-counters-and-the-ledger.md),
[ADR 0054](0054-usage-data-is-the-tenants.md) and [ADR 0028](0028-admin-api-generator.md).
Requirements USE-06, USE-07, USE-08, PRV-03.

## Context

Reports, the limit counters and the capability check aggregate the usage ledger. Read from raw rows
they grow with traffic; ADR 0013 says reports read rollups maintained by the worker. Counters must keep
the figures of `limits.Sum`, and costs stay per currency and price source (ADR 0053).

## Decision

1. A rollup per tenant by UTC hour of the start and by caller, key, provider, model, model ID,
   currency, price source, outcome and whether a credential was opened, with token and cost sums as
   recorded. The worker adds to it in the tenant's session by time of recording, behind a watermark,
   one minute behind the clock, at most seven days per transaction.
2. One exact reader: whole hours that no period boundary cuts come from the rollup plus the records of
   those hours recorded since the watermark; everything else from the ledger; in one statement. The
   counters' aggregate keeps its contract and becomes this reader.
3. Reports for the tenant only (`usage:read`): by the tenant's day, month or window, by provider, model,
   caller and key, always split by currency and price source, `rejected_limit` apart; at most 400 days
   and 10 000 rows. Usage against a daily or monthly rule from the same reader.
4. Exports as NDJSON (the record's API schema) and CSV, streamed in pages, at most 31 days, a CSV
   formula guard, a transfer aborted on failure. The generator gains streamed answers
   (`application/x-ndjson`, `text/csv`) and raw JSON members.
5. A tenant can restrict the metadata keys it accepts; the gateway applies the list with the tenant's
   catalogue view.

## Consequences

- A stopped rollup makes reads slower, never wrong.
- The ledger gains an index by time of recording; building it on a large ledger is an operator step.
- Platform roles read no report and no export, as ADR 0054 decides.

## Alternatives considered

**Rollup by the tenant's day.** Breaks on a change of time zone and serves the counters badly; the
hour, with boundaries read from the ledger, works for every zone whose offsets are whole minutes.

**A rollup by start time with a fixed lateness.** Misses records written after an outage.

**Materialized views.** Not maintainable per tenant under forced row-level security; forbidden by
the schema lint (L-02).

## History

- 2026-11-23: proposed with sprint 6.2.
