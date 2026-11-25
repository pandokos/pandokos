# Backup and restore

How to back Pandokos up, including its keys, and how to restore it (OPS-10). The procedure on this page is
executed step by step by an automated drill against PostgreSQL servers and a key service that it starts and
destroys; the steps carry the identifiers the drill uses, and the page and the drill change together (section
7, [the record](restore-drill-2026-11.md)). It follows from the vault ([vault guide](vault.md)), the audit
chains ([audit guide](audit.md)) and the deletion of tenants ([deletion guide](tenant-deletion.md)); the
erasure rule for backups is [ADR 0068](../adr/0068-key-encryption-key-versions-rotation-and-erasure-in-backups.md).

The commands are written as the drill runs them: the PostgreSQL tools as a superuser on the database server
(`<database>` is the name of the Pandokos database), the paths of the repository relative to its root, and
`/backup` for the directory of your backup store as the server sees it. Replace what is yours.

## 1. What to back up

| What | How | Where |
|---|---|---|
| **The database** | A logical dump (step 1.a) or a physical base backup with WAL archiving (step 1.b). Both are made by a superuser: a role that row-level security binds (the owner role, the runtime roles) makes `pg_dump` fail rather than write a partial dump. Roles are created from `deploy/postgres/roles.sql`, which a dump does not carry | the backup store of the database |
| **The key file of every version that is `active` or `retiring`** (`pandokos kek-version list`) | Copy the file (and every file of `vault.kek.local.unwrap_key_files`) when it is generated and at every rotation | **separately from the database**, in a store that the database's administrators cannot read |
| **With a key service: its snapshot, its unseal shares and a way to authenticate** | The service is backed up by its own storage snapshot (`GET sys/storage/raft/snapshot` in OpenBao), taken **after every rotation of the key and before Pandokos is restarted** to use the new version; keep with each snapshot the **unseal shares** it needs and a token of the original service that can administer it (an AppRole comes back with the snapshot). The Pandokos token or AppRole files are with the configuration's secrets ([external key guide](external-key.md#10-backups)). The transit engine's own key-backup endpoint is not the procedure: it needs an exportable key, which Pandokos refuses | the same separate store |
| **The configuration** | The YAML file and the environment, without the secrets that live in files | with your deployment's configuration |
| **Outside the database: the heads of the audit chains, with their `seq`** | `pandokos audit head --tenant T -o json` for every tenant and `pandokos platform-audit head -o json`, appended to a store that only appends ([audit guide](audit.md#7-anchoring-the-head-elsewhere)). Keep the `seq` with each hash: the check of step 4.6 needs both | a host that is not the installation |
| **Outside the database: the list of completed deletions** | `pandokos tenant deletion list --state completed -o json`, kept with the date; or the platform's events: `pandokos platform-event export --type io.pandokos.tenant.deleted.v1` | the same store |

<a id="step-1-a"></a>
**1.a The logical backup.** A dump in the custom format, made by a superuser:

```sh
pg_dump -U postgres -d <database> -Fc -f /backup/pandokos.dump
```

Keep one file per time point. It holds sealed secrets and wrapped data keys, never a key.

<a id="step-1-b"></a>
**1.b The physical backup.** The server runs with `wal_level = replica`, `archive_mode = on` and an
`archive_command` that copies each WAL file to the archive (`/backup/wal`). One base backup serves every
later time point:

```sh
pg_basebackup -U postgres -D /backup/base -Ft -z -X none --checkpoint=fast
```

To name a point you may restore to, run `SELECT pg_create_restore_point('<name>')` and `SELECT pg_switch_wal()`,
and wait until `pg_stat_archiver.last_archived_wal` has reached the WAL file that held the point: what a
restore reads must be in the archive before the server that wrote it is lost.

A database backup holds only sealed secrets and wrapped data keys, so it cannot open anything; a key file holds
nothing without the database. Keeping them apart is the point.

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
file (a backup made before the rotation needs the old key, section 6). After a deletion, rotate and
back up again if you promise that the tenant's credentials are gone from backups
([deletion guide](tenant-deletion.md#6-erasure-and-backups-the-operators-rule)).

With a key service, three rules keep every backup you hold recoverable:

1. Snapshot the service **after every rotation of the key in it and before Pandokos is restarted** to use the
   new version. A database backup taken after the re-wrap needs the new version, and a snapshot older than the
   rotation does not hold it (section 6, row 4s).
2. Do not trim a version, and do not raise `min_decryption_version` past it, while a database backup you keep
   was taken before the re-wrap away from it completed (row 3s). `pandokos kek-version list` and the date of the
   backup tell which. This is the erasure rule of ADR 0068 read from the other side: trimming is how the
   credentials of deleted tenants leave old backups, and also how everyone's leave them.
3. Keep with each snapshot the unseal shares it needs and the means to authenticate.

## 4. Restore, step by step

Restore into an **empty** server: no role of Pandokos, no database. A physical restore needs a server of the
**same major version** as the one that made the base backup (PostgreSQL refuses another with `database files
are incompatible with server`); a logical restore may go to a later major (the drill restored into PostgreSQL 18
and verified both chains).

<a id="step-4-1"></a>
**4.1 Roles.** For a dump, create the privilege roles and the login users before the data:

```sh
psql -U postgres -v ON_ERROR_STOP=1 -f deploy/postgres/roles.sql
```

then the login users the [database guide](database.md#2-roles-and-users) describes, with their passwords (the
passwords are the operator's: they are in no backup of Pandokos). A **physical** backup carries the roles and
the passwords of the login users with it: skip this step.

<a id="step-4-2"></a>
**4.2 The database.** For a dump: create the database as the database guide does, empty, after the roles, then
restore the whole archive at once, stopping at the first error. Do not use `--data-only`, `--disable-triggers`,
`--no-owner` or `--no-acl`: the data must load before the triggers and policies exist, so that the sealed audit
rows arrive byte for byte. A plain `createdb` leaves `CONNECT` granted to everyone, and a dump does not carry the
grants on the database, hence the statements:

```sh
psql -U postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE <database> OWNER pandokos_owner" -c "REVOKE ALL ON DATABASE <database> FROM PUBLIC" -c "GRANT CONNECT ON DATABASE <database> TO pandokos_gateway, pandokos_admin, pandokos_worker"
pg_restore -U postgres -d <database> --exit-on-error /backup/pandokos.dump
```

For a **base backup**: as the user the server runs as (`postgres`), unpack it into the empty data directory before
the server starts, give recovery its settings and the signal file, then start the server:

```sh
tar -xzf /backup/base/base.tar.gz -C "$PGDATA"
touch "$PGDATA/recovery.signal"
```

Append to `postgresql.auto.conf` of that directory (the second line only to stop at a restore point you made in
step 1.b; without it recovery runs to the end of the archive):

```
restore_command = 'cp /backup/wal/%f %p'
recovery_target_name = '<restore point>'
recovery_target_action = 'promote'
```

The server answers connections while it recovers: wait until `SELECT pg_is_in_recovery()` says `f`. The
restored database equals the source's: a schema-only dump (`pg_dump --schema-only --no-comments`) of both
differs in nothing PostgreSQL preserves. Two things it does not preserve are not differences: the random token
that `pg_dump` writes in its `\restrict` and `\unrestrict` lines, and the nesting of the parentheses of a `CHECK`
constraint that mixes `BETWEEN` with `AND` (the constraint is the same; compare those lines without them).

<a id="step-4-3"></a>
**4.3 The schema.** The binary must **not be older than the schema** (a binary refuses a schema that is older or
newer than the one it was built for):

```sh
pandokos migrate status
```

If the backup is older than the binary, run `pandokos migrate` with the binary of the version you run now.

<a id="step-4-4"></a>
**4.4 The key files.** Put the key of the version that the database says is `active`, and of every `retiring`
one, in the configuration: the key that wraps as `vault.kek.local.key_file`, the others in
`vault.kek.local.unwrap_key_files`; with a key service, the same `vault.kek.transit.*` settings and
credentials (the service itself restored first, section 6). `serve` activates the version of the key that
**wraps** (`key_file`). So for a backup older than a rotation:

- the key of the backup's active version as `key_file` starts with `kek_activation: unchanged`;
- the **current** key as `key_file` and the old one in `unwrap_key_files` starts with `kek_activation:
  activated`: `vault ready` names the current version, the old one becomes `retiring`, and the worker re-wraps
  every data key and retires it;
- the old key as `key_file` with a backup **newer** than the rotation refuses to start, naming both versions:
  `the configured key-encryption key version <old> was replaced by <new>`. Put the key of the new version in
  `key_file`.

See "The four cases of the key" for a key you no longer have.

<a id="step-4-5"></a>
**4.5 Start.**

```sh
pandokos serve
```

<a id="step-4-6"></a>
**4.6 The checks.** The chains first:

```sh
pandokos audit export --tenant <tenant ID> --to-seq <seq kept with the head> --out tenant.ndjson
pandokos audit verify --tenant <tenant ID> --file tenant.ndjson --expect-head <hash kept outside>
pandokos platform-audit export --to-seq <seq kept with the head> --out platform.ndjson
pandokos platform-audit verify --file platform.ndjson --expect-head <hash kept outside>
```

Then:

- `vault ready` in the log names the **expected** `kek_version` (the version of the key that wraps, 4.4) and its
  `kek_activation`.
- The chains, for every tenant and for the platform, against the heads you kept, **with their `seq`**. Export
  the chain up to that `seq` and verify the export against the hash: the whole log may have rows after your
  head (a start that activates a key writes `kek.activate` and `kek.retire` rows to the platform's chain), and a
  head kept after the backup's time shows the rows the restore lost. The commands are above; `--file`
  needs the tenant's ID, and for a tenant whose deletion is complete in the restored database there is no chain
  any more: run `pandokos tenant deletion verify <tenant ID>`.
- A call with a tenant's key reaches the provider with that tenant's own credential (the credential opens),
  and a quota refuses at the count that the restored ledger gives.
- `pandokos kek-version list` shows the versions and their state.
- A destination receives a new event, signed with the secret it had when the backup was made.

<a id="step-4-7"></a>
**4.7 Rotate nothing** until the checks pass.

## 5. After a restore

<a id="step-5-a"></a>
**5.a Request again the deletions completed after the backup.** A restored backup brings deleted tenants
back, with their state of that time, and takes the platform's log back with it (the completion records too).
Compare the list of completed deletions you kept outside the database with the restored one:

```sh
pandokos tenant deletion list --state completed -o json
```

and for each tenant missing from it run `pandokos tenant delete` again (it is suspended first, the grace period
applies, and the worker waits about two minutes for older transactions to end). Their credentials, if your old
backups are the cause, are only gone for good once the key version is retired and its file destroyed.

<a id="step-5-b"></a>
**5.b The platform's events list the deletions too.** One event for each completed deletion, with the tenant
ID, its external reference and the time. A system that has pulled the platform's events (they are never
removed) holds the same list as the one above, and the events recorded after the backup are exactly the
deletions to request again:

```sh
pandokos platform-event export --type io.pandokos.tenant.deleted.v1
```

<a id="step-5-c"></a>
**5.c Expect the usage recorded after the backup to be missing**, and the audit rows with it: the heads you
kept show how far the chain went (a head kept after the backup does not verify; the one kept at the backup
does). The budget consumed after the backup is available again.

<a id="step-5-d"></a>
**5.d Sessions and keys revoked since the backup are valid again**: revoke what must stay revoked. A key
revoked after the backup answers `200` again until you revoke it again.

<a id="step-5-e"></a>
**5.e A deletion that was running continues** where its row says, without a new request; its steps are safe to
repeat.

<a id="step-5-f"></a>
**5.f The events after the backup are lost, and the log is reissued.** The restored log of a tenant is behind
the log as it was when the installation was lost, and new events take sequence numbers that were already
issued, with new identifiers. A destination's cursor is the backup's: the events between it and the backup are
delivered again, with identifiers the receiver has already seen (delivery is at least once; the receiver keeps
each identifier once, [events guide](events.md)). A system that pulls with `event export --after N` and kept a
cursor past the restored head is answered with `200` and no events, not an error: **reset its cursor to the
restored head**, or it misses the events that reuse the numbers it already passed. The ledger is the record, and
the usage export reads it. Destinations are in the backup, with their sealed secrets: a restore needs the key
of the version that sealed them, like any credential.

## 6. The four cases of the key

What the restore needs depends on which key you still hold ([vault guide](vault.md)). With a key service, read
"key file" as "the key versions in the service". The rows, for a key file and for a key service (`s`):

| | You have, with a key file | You have, with a key service | What happens |
|---|---|---|---|
| 1 | The backup and its key (the file of every version it needs) | The backup, and the service with the versions that wrapped its data keys in range of `min_decryption_version`; or the service **restored from its own snapshot** into an empty server, with the same `vault.kek.transit.*` settings and credentials | Everything works; nothing is entered again |
| 2 | A backup older than a rotation, and the old key kept | The same backup, and the service after a rotation with the old version still in range | Everything works; the worker re-wraps again; no tenant action |
| 3 | The same backup, without the old key | The same backup, after the old version was **trimmed** in the service | The credentials and signing secrets sealed under data keys of that version can never be opened: recover (below) |
| 4 | The key lost entirely | The key deleted in the service, or the service lost with no snapshot, or **a snapshot older than a rotation with a database backup taken after the re-wrap** | The same for every version: recover for each, a new key, every tenant enters its credentials again; nothing else is lost |

<a id="step-6-1"></a>
**6.1 The backup and its key.** Start with it as `key_file` (and `unwrap_key_files` for more than one).

<a id="step-6-2"></a>
**6.2 A backup older than a rotation, the old key kept.** Start with the key of the backup's active version as
`key_file`, or the current key as `key_file` and the old one in `unwrap_key_files` (step 4.4 says what each
does). Every credential opens and the worker re-wraps again.

<a id="step-6-3"></a>
**6.3 The same backup, without the old key.** `serve` refuses to start, naming the version
(`the database holds data keys wrapped by key-encryption key version <v> which this configuration does not provide`).
Run the recovery script as a superuser in the Pandokos database with the lost version (from the refusal or an
earlier `vault ready` line):

```sh
psql -U postgres -d <database> -v kek_version=<version> -f deploy/postgres/recover-lost-kek.sql
```

In one transaction it revokes the credentials sealed under data keys of that version, **disables the event
destinations whose signing secret was sealed under one** (their URL, types and cursor stay, so the events the
log still holds are delivered once the destination is enabled again), drops a previous signing secret that was,
and removes those data keys and the version. It prints the number of credentials revoked, of destinations
disabled and of previous secrets dropped; run again, or for a version that does not exist, it changes nothing.
It writes no audit row: it is an act of the database administrator outside the product, so record it in your own
log. Then start with a new key (step 6.4). Tenants whose data keys were under another version are not touched (proved by `TestLostKeyRecovery`, `internal/vault/sealer_db_test.go`; every tenant of a drill world is under the lost version).
The tenant **enters its credentials again**; the owner of each destination runs `pandokos event-destination
rotate-secret`, gives the receiver the new secret and enables the destination. Everything else restores.

<a id="step-6-4"></a>
**6.4 The key lost entirely.** The same, for every version in the database: the recovery script for each, then
a new key, and every tenant enters its credentials again and rotates its destinations' secrets. Nothing else is
lost.

```sh
pandokos kek generate --out <new key file>
```

<a id="step-6-1s"></a>
**6.1s The service restored from its snapshot.** Start an empty server of the service, initialise it, unseal it
and restore the snapshot into it (`POST sys/storage/raft/snapshot-force` with the new server's own root token),
then unseal with the **original** unseal share: the original root token and the Pandokos token work again, and the
new server's own do not. Point Pandokos at the new address with the same token file. The service you have is
unchanged when it was not lost: start Pandokos on it.

<a id="step-6-2s"></a>
**6.2s The old version still in range.** Start Pandokos with the backup and the service as it is: `serve`
activates the service's newest version (`kek_activation: activated`), the old version becomes `retiring` and the
worker re-wraps.

<a id="step-6-3s"></a>
**6.3s The old version trimmed.** The identifier of a version of a key service is `transit-…`; take it from the
refusal or from `pandokos kek-version list` kept before the loss. Run the script with it, then start Pandokos on
the service (it already has a newer version): tenants enter their credentials again, destinations are rotated as
in 6.3.

```sh
psql -U postgres -d <database> -v kek_version=<transit-version> -f deploy/postgres/recover-lost-kek.sql
```

<a id="step-6-4s"></a>
**6.4s The key lost in the service.** Three forms, one outcome: the key deleted in the service (create it again:
the policy of Pandokos does not delete or create keys), the service lost with no snapshot (install it again with a
new key, policy and token), and a snapshot older than a rotation with a database backup taken after the re-wrap
(the service holds only the older version; `serve` refuses with `the configured key-encryption key version … was
replaced by …`). In all three, run the script of 6.3s for every version the database names, start Pandokos on
the service, and every tenant enters its credentials again.

Choosing whether to keep an old key is a choice about erasure: an old backup with its old key keeps the
credentials of tenants deleted before the rotation recoverable. See
[the deletion guide](tenant-deletion.md#6-erasure-and-backups-the-operators-rule).

## 7. Drills

The procedure of sections 1 to 6 is run by a suite (`make test-drill`, needs Docker only): a logical and a
physical restore into an empty server, the restore after a loss with both audit chains verified, a restore on a
later PostgreSQL major, the four cases of the key with a key file and with an OpenBao key service, a deletion
replayed after a restore, and the recovery after a lost key with destinations. Every step of this page that
carries an identifier is a step of the suite, and a test of the repository holds the two equal. The
[record of the drill](restore-drill-2026-11.md) says what was drilled, what was found, what cannot be recovered
and what must be entered again, with the figures of one run. Repeat the drill, with a new dated record, when the
procedure changes, when a migration changes a table the vault or the chains use, and on every release candidate.

A suite is not your installation: rehearse the procedure on a spare instance, with your own backup store, your
own key service and your own pooler, before you need it. Not drilled: HashiCorp Vault's snapshot, a backup store's
own encryption, a pooler in front of the target, a restore by someone who did not write this page.
