# 0067. Tenant deletion: stages, closed tenants and the records that are kept

- Status: proposed
- Date: 2026-11-23

Refines [ADR 0013](0013-ledger-and-audit.md), [ADR 0027](0027-database-roles-and-session-context.md),
[ADR 0054](0054-usage-data-is-the-tenants.md) and [ADR 0065](0065-the-audit-hash-chain-reading-and-retention.md);
amends ADR 0013 ("rows are never updated": once, by the erasure function). Requirements TEN-11, TEN-09,
AUD-01, USE-07, TEN-06; sprint 6.3.

## Context

A tenant must be deletable (TEN-11): suspension, revocation of access, cryptographic erasure of secrets,
removal of data under the retention policy, and a record that can be verified; never touching global data
or another tenant. Until now `deleting` and `deleted` were states without an operation. The ledger and the
audit logs are append-only tables (ADR 0013, 0065), the platform reads no usage (ADR 0054), and the worker
runs in a tenant's session with no standing right to delete anything. A deletion is destructive, long
(a ledger can hold millions of rows) and must survive a crash at any point.

## Decision

1. **A deletion is a row** of the global table `tenant_deletion` (one per tenant) with the states
   `scheduled`, `running` and `completed`. A platform administrator requests it with a reason and the
   tenant's name as confirmation (permission `tenant:delete`, `platform-admin` only). The request suspends
   the tenant in the same transaction and schedules the deletion after `tenant_deletion.grace_period`
   (7 days by default, installation-wide, 0 to 90 days). (D-1, D-2)
2. **A scheduled deletion can be cancelled** (the row is removed, the tenant stays suspended) and holds off
   reactivation (a trigger on `tenant`); a deletion that started cannot be cancelled. A repeated request
   changes nothing. (D-2, D-3)
3. **The request and the cancellation are audited in the platform's log**; the tenant's own log is written
   by the worker only, when it closes it. (D-4)
4. **A tenant whose deletion started is closed.** Its ID enters the global table `tenant_closed`, and every
   tenant-scoped table and `idempotency_key` refuse an `INSERT` in its session (a statement trigger,
   `tenant_open`), as do five global tables that name a tenant in a row written in platform scope (a row
   trigger). A row passes only when the statement runs as the owner under the setting `pandokos.erasure`,
   that is, inside a function of migration 0016. No data key can reappear (2.1 R-9). (D-5)
5. **The worker deletes through one definer function**, `tenant_erasure_step(batch)`, called in the tenant's
   session until it answers `done`. Each call is one transaction and one step or one batch, recorded in the
   row, idempotent; two workers serialize on the row. The steps are `begin`, `access`, `secrets`,
   `configuration`, `usage`, `audit`, `complete`. **No Go code issues a `DELETE` or an `UPDATE` of a
   tenant's rows**; the job only calls the function (a repository test holds this). (D-6)
6. **The order inside the steps is the one the foreign keys impose.** The data key is destroyed before the
   credentials, in one transaction. `append_only` admits a `DELETE` only under `pandokos.retention` (ADR 0065)
   and now an `UPDATE` only under `pandokos.erasure`; a started price version is removed under the retention
   setting. Only functions of migrations 0015 and 0016 set the two settings; no runtime role holds `UPDATE` or
   `DELETE` on an append-only table. `TRUNCATE` stays refused. For the global tables that hold a tenant's
   rows the owner has one policy each, limited to the tenant of the session under `pandokos.erasure`. (D-7)
7. **The ledger is kept pseudonymized.** `principal_id` and `key_id` become pseudonyms and `metadata` becomes
   `{}`; a pseudonym is the text of a UUID made of the first 16 bytes of SHA-256 over a per-deletion salt and
   the value, marked as a UUID of version 8 so that a record that escaped it can be found, and the salt is destroyed at completion, so nothing can recompute it. Equal identifiers give
   equal pseudonyms inside one tenant. (D-8)
8. **The audit log is kept, closed and sealed again.** A closing row `tenant.delete` is appended; `actor_id`,
   `key_id` and `resource_id` become pseudonyms; `actor_name`, `reason` and `detail` are emptied; the hashes are
   computed again from the anchor with the encoding of ADR 0065, which does not change. It stays an ordinary
   chain and the verifier does not change. (D-9)
9. **The platform reads a deleted tenant's kept log** (head and export, `platform_audit:read`, only for a tenant
   that is `deleted`); nobody reads its kept ledger through the product. (D-10)
10. **The completion record** is a row of the platform's chain, `tenant.delete_complete`, with counts by table
    (not for the ledger, the limit events and the rollup), the key versions that wrapped the data keys, both
    heads of the tenant's log and a residue of 0 counted by the database. It holds no usage figure.
    `pandokos tenant deletion verify` checks it against both chains. The completion waits two minutes after the
    start and counts what is left; if rows were left by a transaction that began before the closing, a second
    pass runs. (D-11)
11. **Retention applies to the kept records** through `deleted_tenant_retention`, recorded in the platform's
    log; the rollup and the retention of living tenants skip tenants that are `deleting` or `deleted`. (D-12)
12. **`lifecycle.Deleter`** runs every `tenant_deletion.interval` (1 minute): due deletions are listed by ID in
    platform scope (seven columns), steps are called in the tenant's session, at most 30 s per tenant and tick,
    and a failed step is tried again at the next tick. (D-13)
13. **A user the platform manages is not deleted or anonymised** by a tenant's deletion: it loses the membership;
    in the tenant's kept log its ID is a pseudonym like any other; the platform's chain is never rewritten. (D-22)

## Consequences

- A deletion is crash-safe and resumable, and cannot start without a request, early, or for another tenant;
  the worker alone cannot delete a tenant and the admin role alone cannot start one early.
- ADR 0013's "rows are never updated" now has one exception, by the erasure function, under a setting that only
  the owner's functions set. The cost is a trigger on the insert path of every tenant table, the ledger included
  (one primary-key lookup per statement, one per batch of the writer); measured in sprint 10.1.
- The ledger and the audit log of a deleted tenant stay in the database until retention removes them; with the
  retention keys at 0, forever. An operator who must erase everything sets them (open point O-1).
- A long transaction that began before the closing and commits after the completion leaves a row behind (R-3).
  The completion waits and counts; a row left after `deleted` holds no secret and is found by the residue query
  of the [deletion guide](../operations/tenant-deletion.md).
- For sprint 6.4: the event "tenant deleted" is written by `tenant_erasure_step` in the transaction of the
  completion record, and the external reference of a tenant must be copied into `tenant_deletion` at the request;
  the outbox table is one more table for the erasure and needs the `tenant_open` trigger. For epic 10: deleting
  platform-managed users and scrubbing a deleted tenant's name.
- Erasure in backups is not decided here: [ADR 0068](0068-key-encryption-key-versions-rotation-and-erasure-in-backups.md).

## Alternatives considered

**Cascading foreign keys from `tenant`.** No order, no record, no pseudonymization, and no way to resume.

**Deleting in Go, with grants to the worker.** A standing right to delete any tenant's rows for a process that
also serves requests; a bug becomes a data loss. One definer function that works only for a requested deletion in
the tenant's own session is a smaller authority.

**Keeping the audit chain and its identifiers.** Personal data kept for no purpose: names, addresses and
identifiers would stay readable.

**A marker in the verifier for re-sealed chains.** The platform's chain, which is never rewritten, already says
that and when; the verifier stays as it is.

**Removing the ledger and the log at the deletion** (or a choice in the request). Possible on the same steps
later (open point O-1); the epic and the data model keep them under the retention switch.

## History

- 2026-11-23: proposed with sprint 6.3.
