# Backup and restore

How to back Pandokos up, including its keys, and how to restore it (OPS-10). This page **documents** the
procedure; a restore that is rehearsed and tested is sprint 12.3. It follows from the vault
([vault guide](vault.md)), the audit chains ([audit guide](audit.md)) and the deletion of tenants
([deletion guide](tenant-deletion.md)); the erasure rule for backups is
[ADR 0068](../adr/0068-key-encryption-key-versions-rotation-and-erasure-in-backups.md).

## 1. What to back up

| What | How | Where |
|---|---|---|
| **The database** | A physical base backup with WAL archiving (point-in-time recovery), or `pg_dump` of the Pandokos database made with a role that sees everything (a superuser, or the owner role, which row-level security still binds: use a superuser). Roles are created from `deploy/postgres/roles.sql`, which a dump does not carry | the backup store of the database |
| **The key file of every version that is `active` or `retiring`** (`pandokos kek-version list`) | Copy the file (and every file of `vault.kek.local.unwrap_key_files`) | **separately from the database**, in a store that the database's administrators cannot read |
| **The configuration** | The YAML file and the environment, without the secrets that live in files | with your deployment's configuration |
| **Outside the database: the heads of the audit chains** | `pandokos audit head --tenant T -o json` for every tenant and `pandokos platform-audit head -o json`, appended to a store that only appends ([audit guide](audit.md#7-anchoring-the-head-elsewhere)) | a host that is not the installation |
| **Outside the database: the list of completed deletions** | `pandokos tenant deletion list --state completed -o json`, kept with the date; or the platform's events: `pandokos platform-event export --type io.pandokos.tenant.deleted.v1` | the same store |

A database backup holds only sealed secrets and wrapped data keys, so it cannot open anything; a key file
holds nothing without the database. Keeping them apart is the point. The key file is backed up when it is
generated, and again at every rotation.

## 2. What is not backed up, and why it does not matter

- **Caches** (principals, credentials, data keys, catalogue, issuer keys): rebuilt from the database.
- **Limit counters**: rebuilt from the ledger at start ([ADR 0051](../adr/0051-period-counters-and-the-ledger.md)).
- **The ledger's memory buffer**: records still in memory when a process dies are lost with it
  ([usage guide](usage.md#8-shutdown)); the backup holds what was committed.
- **Sessions**, if you accept that people sign in again (they are in the database, so a backup holds them
  as of its time; a restore brings back sessions that may have been revoked since, so revoke them if that
  matters).

## 3. When

After **every rotation** of the key: take a new backup of the database before you destroy the old key
file (a backup made before the rotation needs the old key, the next section). After a deletion, rotate and
back up again if you promise that the tenant's credentials are gone from backups
([deletion guide](tenant-deletion.md#6-erasure-and-backups-the-operators-rule)).

## 4. Restore, step by step

1. **Roles.** Create the roles with `deploy/postgres/roles.sql` and the login users the
   [database guide](database.md) describes, before the data.
2. **The database.** Restore the base backup (and the WAL up to the point you choose), or `pg_restore` the
   dump, as the owner of the objects.
3. **The schema.** `pandokos migrate status`: the binary must **not be older than the schema** (a binary
   refuses a schema that is older or newer than the one it was built for). If the backup is older, run
   `pandokos migrate` with the binary of the version you run now.
4. **The key files.** Put the key file of every version that is `active` or `retiring` in the **backup**, as
   `vault.kek.local.key_file` (the active one) and `vault.kek.local.unwrap_key_files` (the others). See
   "The four cases of the key" for a key you no longer have.
5. **Start** `serve`.
6. **The checks:**
   - `vault ready` in the log names the **expected** `kek_version` (the active version of the backup) and
     `kek_activation: unchanged`; if the backup is older than a rotation and you hold the old key as
     `key_file`, `serve` refuses to start naming both versions: put the key of the active version in
     `key_file`.
   - `pandokos platform-audit verify --expect-head <hash kept outside>` and `pandokos audit verify --tenant T
     --expect-head <hash kept outside>` for each tenant, against the heads you kept: a head kept after the
     backup's time shows the rows the restore lost; a log that does not reach the head you kept was cut.
   - A call with a tenant's key reaches the provider (the credential opens).
   - `pandokos kek-version list` shows the versions and their state.
7. **Rotate nothing** until the checks pass.

## 5. After a restore

- **Request again the deletions completed after the backup.** A restored backup brings deleted tenants
  back, with their state of that time, and takes the platform's log back with it (the completion records
  too). Compare the list of completed deletions you kept outside the database with
  `pandokos tenant deletion list`, and for each tenant missing from it run `pandokos tenant delete` again
  (it is suspended first, the grace period applies). Their credentials, if your old backups are the cause,
  are only gone for good once the key version is retired and its file destroyed.
- **The platform's events list the deletions too.** `pandokos platform-event export --type io.pandokos.tenant.deleted.v1`
  prints one event for each completed deletion, with the tenant ID, its external reference and the time; a system
  that has pulled the platform's events (they are never removed) holds the same list as the one above, and the
  events recorded after the backup are exactly the deletions to request again. A restored database also loses the
  events of the tenants' logs recorded after the backup; the ledger is the record, and the usage export reads it
  ([events guide](events.md)). Destinations are in the backup, with their sealed secrets: a restore needs the key
  file of the version that sealed them, like any credential.
- **Expect the usage recorded after the backup to be missing**, and the audit rows with it: the heads you
  kept show how far the chain went.
- **Sessions and keys revoked since the backup are valid again**: revoke what must stay revoked.
- A deletion that was running continues where its row says; its steps are safe to repeat.

## 6. The four cases of the key

What the restore needs depends on which key file you still hold ([vault guide](vault.md)):

| You have | What happens |
|---|---|
| **The backup and its key** (the file of every version it needs) | Everything: start with it as `key_file` (and `unwrap_key_files` for more than one) |
| **A backup older than a rotation, and the old key kept** | Start with the **current** key as `key_file` and the old key in `vault.kek.local.unwrap_key_files`. `serve` activates what the database says is active and the worker **re-wraps** again; every credential opens |
| **The same backup, without the old key** | The credentials sealed under data keys of that version can never be opened: run `deploy/postgres/recover-lost-kek.sql` with the lost version (`psql -v kek_version=local-… -f deploy/postgres/recover-lost-kek.sql`), which revokes them and removes the data keys and the version, then start with a new key (`pandokos kek generate`); tenants **enter their credentials again**. Everything else restores |
| **The key lost entirely** | The same: the recovery script, a new key, every tenant enters its credentials again ([vault guide](vault.md#5-what-its-loss-means)). Nothing else is lost |

Choosing whether to keep an old key is a choice about erasure: an old backup with its old key keeps the
credentials of tenants deleted before the rotation recoverable. See
[the deletion guide](tenant-deletion.md#6-erasure-and-backups-the-operators-rule).

## 7. What sprint 12.3 adds

The drill that follows this page, including the four cases of the key and a deletion replayed after a
restore, with figures for the time it takes. Until then, rehearse it yourself on a spare instance before
you need it: restore the latest backup, start `serve` with the copy of the key, run the checks of step 6.
