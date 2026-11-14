# 0039. Usage record, admission and the ledger writer

- Status: proposed
- Date: 2026-11-14

Refines [ADR 0010](0010-usage-extraction.md), [ADR 0013](0013-ledger-and-audit.md) and
[ADR 0037](0037-request-pipeline-and-resolution.md).

## Context

ADR 0013 fixes one row per request, a batching writer, a bounded buffer and readiness tied to
it; ADR 0010 fixes provider values, estimates and their marking. Building them needs a record
layout that cannot hold content, a rule for when a value is estimated, a way to keep "exactly
one record" true while the buffer is bounded, and a way to write rows of many tenants under
row-level security and to create partitions with roles that own nothing.

## Decision

1. A request is **admitted** after authentication by taking a slot of the ledger buffer; the
   slot is freed when its row is committed. Without a free slot the request is refused with
   `unavailable` and has no record. Every admitted request produces exactly one record,
   whatever its outcome. Anonymous requests produce none.
2. The record has separate columns for provider-reported and estimated token counts and a
   source per total (`provider`, `estimated`, `none`), enforced by check constraints. The
   provider's own counters are kept as a flat map of integers. Every text column is
   format-checked; no column can hold a body.
3. Usage is estimated only when the provider answered 2xx and no usage could be read. No
   answer and error statuses are recorded without usage. For the `openai` family the input
   estimate is the request size divided by four, the output estimate one token per content
   event of a stream or the response size divided by four.
4. Cost is computed when the request ends, with the price version in force at its start, from
   the versions delivered with the resolved model; the status is known, unknown with the
   reason, or none.
5. The writer groups waiting records by tenant and inserts each group in a transaction of
   that tenant; the gateway role holds `INSERT` only. Record IDs are made by the gateway and
   the insert ignores conflicts, so a repeated batch writes each row once. A record the
   database refuses is logged and dropped without holding up the others.
6. On shutdown the writer flushes under its own deadline after the drain; records it cannot
   write are logged in full.
7. The table is partitioned by UTC month with a default partition. A `SECURITY DEFINER`
   function, executable by the worker role, creates partitions ahead of time; partitions carry
   no grants and the same forced policy as the parent.
8. Callers attach metadata as `X-Pandokos-Metadata-<key>` headers, at most eight short ASCII
   entries, read after authentication, stored on the record, never forwarded; entries outside
   the bounds are dropped and counted.
9. No API reads the ledger yet; the admin and worker roles hold `SELECT` under the tenant
   policy.

## Consequences

The buffer size is also the number of requests an instance admits at once. A request refused
for a full buffer is the one authenticated request without a record. Failed provider calls
show no consumption. Estimates are coarse and marked. A process that is killed loses the
records in memory, as ADR 0013 states.

## Alternatives considered

Bounding only finished records (in-flight requests could then exceed the bound or lose their
record); a definer function inserting whole batches across tenants (a special path around the
policy); estimating every forwarded request (errors would look like consumption); storing the
provider's usage object as received (a channel for text); no default partition (a missing
partition would reject records); metadata in one JSON header (parsing ambiguity).
