# 0013. Usage ledger and audit log: append-only tables in PostgreSQL

- Status: proposed
- Date: 2026-11-11

## Context

Three records are easily conflated and must not be (AUD-07): who changed what (audit), what was
consumed (usage), and how the system behaved (telemetry). The first two are evidence; the third is
disposable.

## Decision

**Usage ledger.**

- One row per inference request, in a table partitioned by month, with no content.
- Written by the gateway after the response through a batching writer: records are grouped for a
  few milliseconds and inserted together. The gateway's database role can insert usage and nothing
  else on that table.
- If the database is unreachable, records wait in a bounded in-memory buffer. When the buffer is
  full the instance reports not ready and refuses new requests, rather than serve calls it cannot
  record. On shutdown the buffer is flushed before the process exits.
- The window in which a crash loses records is the batching interval plus any buffered backlog.
  It is documented. Closing it for platform-funded calls with a durable intent row is an open
  question tied to the billing work.
- Rows are never updated. Corrections are rows in a separate table that reference the original.
- Reports read from rollup tables maintained by the worker, not from raw rows.

**Audit log.**

- One row per administrative or security event, inserted in the same transaction as the change.
  If the audit row cannot be written, the change does not happen.
- Values are recorded as a redacted description of the change. Secrets are never included; a
  credential change records the fingerprint.
- Each row carries the hash of the previous row in the same tenant's chain. A verification
  command walks a chain and reports any break. Anchoring the head of the chain in an external
  system is left to the operator, through the export.
- Platform events have their own table and chain.

**Both.** Runtime roles hold no update, delete or truncate privilege. Retention removes whole
partitions through one audited function run by the worker.

**Telemetry** is produced separately ([ADR 0015](0015-observability.md)) and is never read to
answer an accounting or audit question.

## Consequences

- One store to back up, with transactional consistency between configuration and its audit trail.
- The ledger grows with traffic. Partitioning keeps retention cheap; very large installations may
  later want to offload old partitions, which the export already allows.
- A hash chain shows tampering; it does not prevent it by someone who can rewrite the whole chain.
  The security model says so.

## Alternatives considered

**A separate analytical store for usage.** Better for large-scale analytics, and a second
mandatory dependency.

**Usage derived from traces.** Sampling, loss and vendor variability make telemetry unfit as an
accounting source.

**Audit as log lines.** Not transactional with the change, and not queryable per tenant with
isolation.
