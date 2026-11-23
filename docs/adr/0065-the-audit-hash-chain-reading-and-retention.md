# 0065. The audit hash chain, reading and retention

- Status: proposed
- Date: 2026-11-23

Refines [ADR 0013](0013-ledger-and-audit.md) and [ADR 0032](0032-audit-log-first-version.md); amends
ADR 0013 on how audit rows are retained. Requirements AUD-04, AUD-05, AUD-06, AZ-10.

## Context

ADR 0013 asks for a hash chain per tenant and for the platform, a verification command, reading and
retention through an audited function. Rows are written by the admin and worker roles, by three
functions of the gateway and by migrations; the tables are append-only for every role.

## Decision

1. A `BEFORE INSERT` trigger that runs as the owner seals every row: ID, time, a sequence number per
   chain, the previous hash and the row's hash, under a lock of the chain's head row. A row that is
   not inserted leaves the head as it was. The existing rows were sealed once by migration 0015.
2. Canonical encoding version 1: length-prefixed UTF-8 fields in a fixed order and the SHA-256 of a
   tagged, key-sorted encoding of the detail, defined in SQL and in Go and tested against each other.
3. Verification is client-side, over an export: `pandokos audit verify`, with the head, the anchor
   and an expected head kept elsewhere. The export stops at the head it read; the verifier also reads
   the rows past that head from the list and checks them as a continuation of the chain, so rows
   written after the head was read are verified and rows put there by hand are reported.
4. Reading: `audit:read` (tenant; `owner`, `tenant-admin`, `auditor`; not `read-only`, not `Support`)
   and `platform_audit:read` (both platform roles). The detail of a price action is withheld from a
   reader without `price_list:read`, with its digest, so the chain still verifies.
5. Retention removes the oldest rows of a chain as a prefix and moves its anchor, recording the removal
   in the chain, instead of dropping partitions: the audit tables are not partitioned, partitioning
   would lose the unique keys of `limit.unlimited_traffic`, and a chain needs an anchor in any case. The
   ledger keeps partition dropping. Both go through two functions run by the worker; `append_only`
   admits a delete only under a setting those functions alone set.

## Consequences

- Audit writes of one tenant, and every write of the platform log, are serialized by the head lock.
- A rewrite of a whole chain by whoever can write the tables is detected only against an anchored head.
- Tenant deletion (sprint 6.3) reuses the switch of point 5 and decides how a pseudonymized chain is
  re-sealed.

## Alternatives considered

**Hashing in the application.** Misses the rows written by the gateway's functions and by migrations.

**RFC 8785 canonical JSON.** Not available in PostgreSQL; its number rules differ from `jsonb`'s.

**Verification in the database.** The tool that checks would run where the tampering happens.

**`audit:read` with `Support`.** The log holds price rates and budget amounts (ADR 0061, point 3).

## History

- 2026-11-23: proposed with sprint 6.2.
