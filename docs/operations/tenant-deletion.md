# Tenant deletion guide

A platform administrator can delete a tenant. The deletion is staged: the tenant is suspended and the
deletion scheduled after a grace period; then the worker removes the tenant's access, destroys its data
key and credentials, removes its configuration, and keeps its usage ledger and its audit log
**pseudonymized**; a completion record in the platform's audit log can be verified. It touches nothing
global and nothing of another tenant, and the platform reads no usage while doing it. Decisions:
[ADR 0067](../adr/0067-tenant-deletion-stages-closed-tenants-and-kept-records.md) and
[ADR 0068](../adr/0068-key-encryption-key-versions-rotation-and-erasure-in-backups.md); the key side is in
the [vault guide](vault.md).

## 1. The procedure

1. **Announce it** to the tenant. A suspended tenant reads nothing (TEN-10), so what the tenant wants to
   keep must be exported **before** the request.
2. **The tenant exports its data** with `pandokos tenant-export` ([export guide](tenant-export.md)). The
   platform cannot do it for the tenant: it reads no usage ([ADR 0054](../adr/0054-usage-data-is-the-tenants.md)).
   A tenant suspended for another reason is reactivated for the export.
3. **Request the deletion**, with a reason and the tenant's name as confirmation:

   ```
   pandokos tenant delete acme --reason "contract ended, ticket 4711" --confirm-name acme
   ```

   The permission is `tenant:delete`, held by `platform-admin` only. The tenant becomes `suspended` at
   once (its keys and sessions are refused within the bound of the suspension, at most
   `cache.principal_ttl`, 30 s by default) and the deletion is scheduled after
   `tenant_deletion.grace_period` (7 days by default, installation-wide). The request is audited in the
   platform's log (`tenant.delete_request`, with the actor, the reason and the time it starts). A wrong
   name is `400 invalid_request` on `confirm_name`; a tenant that is `provisioning`, `deleting` or
   `deleted` is `409 invalid_state`. A second request while the deletion is scheduled answers `200` with
   the existing deletion and writes nothing; once it is running the tenant is `deleting`, so a second
   request is `409 invalid_state` too.
4. **The grace period.** Nothing is removed. `pandokos tenant deletion get acme` shows the state and
   `not_before`. While it is scheduled the tenant cannot be reactivated (`409 invalid_state` on `state`).
5. **Cancel**, if it was a mistake: `pandokos tenant deletion cancel acme --reason "..."` (also
   `tenant:delete`). The scheduled row is removed and `tenant.delete_cancel` written; the tenant stays
   `suspended` and is reactivated with the existing command. **A deletion that started cannot be
   cancelled** (`409 invalid_state`): `deleting` cannot be undone.
6. **The worker runs it** at `not_before`, then reports `completed`. `pandokos tenant deletion list
   [--state S]` shows every deletion, newest request first.
7. **Verify it** (section 5) and **keep the list of completed deletions outside the database**
   ([backup and restore guide](backup-and-restore.md), "After a restore").
8. **Erase it from backups** (section 6): rotate the key within the time you promise erasure in.

**A grace period of `0`** starts the deletion at the worker's next tick (at most `tenant_deletion.interval`,
1 minute by default). There is no time to cancel or to export. It also ends, for the tenant, every call
still in flight without a usage record (section 4 of the [usage guide](usage.md#15-the-ledger-of-a-deleted-tenant)).
The value is installation-wide and read when a deletion is requested.

## 2. What each step removes

The worker calls one function of the database, `tenant_erasure_step`, in the session of the tenant, until
it answers `done`. Each call is one transaction and one step (or one batch of 5000 ledger records),
recorded in the row, safe to repeat; a call that fails changes nothing and the next tick tries again.
Go code issues no `DELETE` or `UPDATE` of a tenant's rows: the order and the content are in the database.

| Step | What it does |
|---|---|
| `begin` | Only at or after `not_before`, and only for a `suspended` tenant. The tenant becomes `deleting` and is **closed**: from this commit every table that takes a tenant's rows refuses an insert for it. A salt for the pseudonyms is made. Platform log: `tenant.delete_start` |
| `access` | Limit rules, subject mappings, trusted issuers and their directory, the tenant's entries of platform-level issuers, virtual keys, role assignments, idempotency keys, support actions, memberships and their roles, the sessions, accounts and identities of the users the tenant manages and those users, service accounts, custom roles |
| `secrets` | **Cryptographic erasure.** The key-encryption key versions that wrap the tenant's data keys are noted; then the data keys are deleted, then the provider credentials (the ciphertext, now unreadable, and its metadata), in one transaction |
| `configuration` | The tenant's platform limit rules (ceilings) and model grants, model restriction, metadata key policy, price versions (started ones too, with their rates), limit events, usage rollup and its state |
| `usage` | The ledger, in batches: `principal_id` and `key_id` become pseudonyms and `metadata` becomes `{}` |
| `audit` | A closing row `tenant.delete`; then the tenant's log is pseudonymized and its chain sealed again (section 3) |
| `complete` | At least two minutes after the start: ledger records and rows of the log that a transaction which began before the closing committed after `usage` or `audit` had passed are pseudonymized (the log gets a new closing row and is sealed again behind it), and the rows left in the tables of `access`, `secrets` and `configuration` are counted. If anything was found, a second pass runs those steps again (not `usage` and `audit`; `complete` looks at the ledger and the log again). Otherwise the completion record is written, the tenant becomes `deleted` and the salt is destroyed |

**What is not touched:** other tenants; the catalogue (providers, endpoints, models and their prices),
price imports, platform keys, the platform's users and role assignments, the platform's issuers (only the
tenant's entries go), `signin_throttle` and `oidc_login` (they expire by themselves) and the platform's
log. A **user the platform manages** (a federated user, or one the platform created) is not deleted: it
loses the membership and stays a user; in the tenant's kept log its ID is a pseudonym like any other. Deleting
such users, and the name the platform's log keeps, is a decision of epic 10.

## 3. What is kept, and how it is pseudonymized

The ledger and the audit log of the tenant stay attributable to the tenant (the organisation), which an
operator's own obligations ask for; nothing that names a person or a workload stays, and the directory that
could resolve the identifiers is gone. The `tenant` row stays, `deleted`, with its name (the name can be
reused by a new tenant, never the ID). Once two tenants hold one name, a command of the CLI that is given
that name refuses it and lists the IDs with their states (`2 tenants are named "acme": … (deleted), …
(active); give the ID of the one you mean`): give the ID, as `tenant deletion verify` of the deleted one
needs.

- **The pseudonym** of an identifier is the text of a UUID made of the first 16 bytes of SHA-256 over a
  random 32-byte salt and the value, marked as a UUID of version 8 (no identifier the product makes has
  that version); the empty text stays empty. The salt is made at the start of the
  deletion, can be read by no runtime role and is destroyed with the completion. Inside one tenant equal
  identifiers get equal pseudonyms, in the ledger and in the log alike ("one caller made these calls"
  survives); across tenants and after the completion nothing links a pseudonym to a value.
- **The ledger**: `principal_id` and `key_id` as pseudonyms, `metadata` emptied; times, provider, model,
  outcome, tokens, cost, currency, `request_id`, `trace_id`, `credential_id` and `price_version_id` stay.
  Nobody reads these rows through the product ([usage guide](usage.md#15-the-ledger-of-a-deleted-tenant)).
- **The audit log**: `actor_id`, `key_id` and `resource_id` as pseudonyms (a `resource_id` equal to the
  tenant's ID stays), `actor_name`, `reason` and `detail` emptied, the hashes computed again from the anchor
  with the encoding of version 1. It is an ordinary chain; the platform reads it with
  `pandokos tenant deletion audit-head` and `audit-export` ([audit guide](audit.md#9-the-log-of-a-deleted-tenant)).
- **Retention.** `retention.usage_months` and `retention.audit_months` apply: a third pass of the retention
  job, for deleted tenants, removes the rows older than the cut and records it in the platform's log
  (`tenant.records_retention`, with no usage figure). With both keys at `0`, nothing is ever removed. An
  operator who must erase everything sets them.

## 4. How fast access ends

At the request the tenant is suspended: keys, sessions and tokens are refused with `403 tenant_suspended`
within `cache.principal_ttl` (30 s by default, 60 s at most). At `begin` the tenant is `deleting` and
closed; the step `access` removes its identities, after which its keys get `401` on both listeners. The
gateways forget the tenant's data key within `cache.data_key_ttl` plus `cache.credential_ttl` at most
(at once with notifications, [vault guide](vault.md#7-how-fast-a-change-takes-effect)); the tenant's keys
were removed a step earlier, so nothing can ask for it. A platform operation that would write a row for a
closed tenant (a model grant, a platform limit, a support action, a user it manages) answers
`409 invalid_state` with an `errors` entry on `tenant`.

## 5. The completion record and `tenant deletion verify`

The last step writes `tenant.delete_complete` in the platform's log (actor `platform`, name `deletion`, the
request ID of the request) with the name of the tenant, `requested_at`, `started_at`, `passes`, `removed`
(one count per table of the steps `access`, `secrets` and `configuration`), `kek_versions`, `usage:
"pseudonymized"`, `audit` (`anchor_seq`, `last_seq`, `rows`, `original_head`, `resealed_head`) and
`residue`, the number of rows the database found left in those tables just before writing the record; the
record is written only when it is `0`. **It holds no usage figure**: no count of ledger records, limit
events or rollup rows ([ADR 0054](../adr/0054-usage-data-is-the-tenants.md)). The row's place in the
platform's chain is `completion_seq`.

```
pandokos tenant deletion verify acme
deletion of 8a1f… verified: record at seq 412, 912 audit rows kept, head 5c0e…
```

It needs `tenant:read` and `platform_audit:read`. It reads the deletion (it must be `completed`), reads the
platform's row and checks that its `seq` is `completion_seq` and that its detail equals the deletion's
fields, verifies the platform's chain up to its head as `platform-audit verify` does, and verifies the kept
log with the head `resealed_head` and the last row `tenant.delete`; a log that retention emptied verifies as
empty when the anchor is `last_seq + 1` and both the head and the anchor's hash are `resealed_head`, and
the line names that head like any other. It exits 0, or exits 1 with one line per failure:
`record_missing`, `record_mismatch`, `platform_chain_break`, `tenant_chain_break`, `head_mismatch`.

`pandokos tenant deletion get` also shows `step`, `passes` and `removed` while it runs; `step` is the one
the database records, never unknown. A deletion that keeps failing shows in the gauge
`pandokos.tenant_deletion.open` (M48), the counter `pandokos.tenant_deletion.steps` by result (M47) and the
log line `tenant deletion failing` (at most once a minute; `tenant deletion recovered` after), whose `step`
reads `unknown` when the worker has restarted since the deletion started and has not yet seen a step answer;
lines `tenant deletion started` and `tenant deletion completed` carry the request ID, never the tenant.
Check the database, then `tenant deletion list`; the job tries the step again at every tick.

## 6. Erasure and backups: the operator's rule

What the product can promise ([ADR 0068](../adr/0068-key-encryption-key-versions-rotation-and-erasure-in-backups.md)):

1. **In the database**, at the commit of the step `secrets`, the tenant's wrapped data keys and sealed
   secrets no longer exist.
2. **A backup taken before** still holds them. Whoever has that backup **and** a key file of the version that
   wrapped the data key can open them. Deleting a tenant does not change that. Only two things do: the
   backup expires, or the key version is **retired and every copy of its file destroyed**.
3. The deletion records the versions concerned. `pandokos tenant deletion get` shows `backup_erasure`:
   `pending` with the versions not yet retired, or `complete`. `pandokos kek-version list` shows, per
   version, `erasures_pending` (completed deletions that wait for it), and `pandokos.vault.erasures.pending`
   (M51) counts them. "Retired" is a fact of the database; destroying the file is yours, and the product
   cannot see it.
4. **The rule:** after a deletion, **rotate the key** within the time you promise erasure in
   ([vault guide](vault.md#6a-rotating-the-key)), wait for `key rotation complete`, take a new backup, then
   destroy the old key file and its copies. Or keep no backup longer than that time.
5. **The price:** a backup taken before the rotation opens no credential once the old key is destroyed.
   Everything else in it restores; the tenants' credentials are then revoked with `recover-lost-kek.sql` and
   entered again. An operator who keeps the old key to keep old backups whole keeps the deleted tenants'
   credentials recoverable from them: choose explicitly.
6. **A restored backup brings deleted tenants back** with their state of that time, and takes the platform's
   log back with it. Keep the output of `pandokos tenant deletion list` outside the database and request
   again the deletions completed after the backup ([backup and restore guide](backup-and-restore.md)).

## 7. The residue query

The database counts the residue itself before it writes the record. A database administrator who wants to
look can run, as a superuser (which row-level security does not bind), in the Pandokos database:

```sql
\set tenant '00000000-0000-0000-0000-000000000000'   -- the ID of the deleted tenant
SELECT 'SELECT * FROM (' || string_agg(
         format('SELECT %L AS table_name, count(*) AS rows FROM pandokos.%I WHERE tenant_id = %L', c.relname, c.relname, :'tenant'),
         ' UNION ALL ' ORDER BY c.relname) || ') AS t WHERE rows > 0 ORDER BY table_name'
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'pandokos' AND a.attname = 'tenant_id' AND NOT a.attisdropped
  AND c.relkind IN ('r', 'p') AND NOT c.relispartition
\gexec
```

After a completed deletion only `usage_record`, `audit_event`, `audit_chain`, `tenant_deletion` and
`tenant_closed` may show rows (the ledger and the log are kept; the `tenant` row has `id`, not `tenant_id`).
Anything else is a row left behind by a transaction that was open across the closing: it holds no secret
(no data key can be created for a closed tenant) and can be deleted by the database administrator.
**Do not hold a transaction open against a tenant for longer than the bounds of the product allow**; the
completion waits two minutes after the start for exactly this case.

## 8. Limits

- **The kept ledger has no reader.** Nobody reads a deleted tenant's records through the product; they leave
  with retention. A tenant that wants them exports first.
- **No self-service.** Only the platform requests a deletion; a tenant's owner cannot delete its own tenant.
- **The platform's chain is never rewritten**, so it still names the people who acted on the tenant, until audit
  retention removes the rows.
- **No event.** "Tenant deleted" is not announced to other systems yet (sprint 6.4); keep your own list.
- **No automatic rotation after a deletion** (section 6) and no restore drill yet (sprint 12.3).
- Nothing waits two minutes in tests: they move the clocks of the row; in production the settle time is real.
