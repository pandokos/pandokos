# 0032. Audit log, first version

- Status: proposed
- Date: 2026-11-12

Refines [ADR 0013](0013-ledger-and-audit.md).

## Context

AUD-01 to AUD-04 and AUD-07 ask for an audit record written atomically with each change, append-only, with a request ID shared with logs and responses.

## Decision

Two tables, `audit_event` (tenant-scoped) and `platform_audit_event`, written in the transaction of the change by `internal/audit`; append-only by missing grants and by triggers that bind every role, the owner included; the ID and time of a row are the database's; details never hold a sensitive key and are at most 8192 bytes. Authentication failures of the admin listener are recorded up to 60 a minute per instance. The action table of the sprint design is the vocabulary. No hash chain, no partitions and no read grant yet.

## Consequences

Nothing reads the audit log before sprint 6.2, which adds the read API, the chain and retention; changing these tables later needs care under forced row-level security.

## Alternatives considered

The hash chain now; partitions now; audit rows written by a database trigger (no actor, no request ID).
