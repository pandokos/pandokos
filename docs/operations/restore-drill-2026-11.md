# Restore drill, November 2026

The record of the drill of the procedure in the [backup and restore guide](backup-and-restore.md): what was
run, what it proved, what it found, what cannot be recovered and what must be entered again, and how long it
took. Decisions: [ADR 0076](../adr/0076-the-restore-drill-its-record-and-recovery-after-a-lost-key.md).

## What was drilled

- **Tree:** the suite of commit `642752a` (`test/drill`), run on 2026-11-25 on the tree of the branch of sprint
  12.3. **Command:** `PANDOKOS_DRILL_REPORT=local/drill/report.json make test-drill` with the default sizes
  (`go test -tags drill -count=1 -timeout 30m -parallel 4 ./test/drill/...`).
- **Versions:** the binary of the tree (`dev`); PostgreSQL 16.15 (`postgres:16-alpine`, pinned by digest) for the
  source and the targets, 18.7 (`postgres:18-alpine`, pinned by digest) for the later major; OpenBao 2.7.3 with
  integrated storage, pinned by digest, for the key service.
- **How:** each case starts a Compose project of its own, installs Pandokos on a PostgreSQL server as the
  guides say (roles, login users, `migrate`, `init`, `serve`, then the command line), makes four tenants with
  provider credentials that are canary values, inference keys, daily quotas and signed destinations, takes
  backups, rotates the key, deletes a tenant, destroys the source, and restores into an empty server by the
  steps of the guide, whose identifiers the suite uses. Everything is removed at the end of the case.
- **After every restore:** `vault ready` names the expected version; both chains verify against the heads kept
  at the backup (step 4.6); a call with each tenant's key reaches the provider **with that tenant's own
  credential**; `kek-version list` shows the versions of the backup; the quota refuses at the count the
  restored ledger gives; a destination receives a new event signed with the secret issued before the backup;
  nothing was rotated before these passed.
- **Secrets:** credentials, signing secrets, key files, database passwords, unseal shares and tokens are
  generated at run time, registered as canaries, and found in no output, no server log and not in the report.
  No backup, base backup, WAL file or snapshot is uploaded from CI.

## The cases

| Case | Test | Proof |
|---|---|---|
| L1, logical restore | TestRestoreLogical | Steps 4.1 to 4.7 on an empty server; the schema-only dumps equal (findings G-9, G-10); `migrate status` exits 0 |
| P1, base backup with WAL to the end of the archive | TestRestorePhysical | The archive holds the deletion; roles and the passwords of the login users came with the backup; every assertion above |
| P2, base backup to the restore point of B1 | TestRestorePhysical | The same, with the older key kept; a server of another major refuses the base backup with PostgreSQL's own message |
| K1, the backup and its key | TestKeyCasesFile | Everything works, nothing entered again |
| K2, a backup older than a rotation, the old key kept | TestKeyCasesFile | Both readings of the guide executed (finding G-5); every credential opens |
| K3, the same backup without the old key | TestKeyCasesFile | `serve` refuses naming the version; the recovery script, a new key; the tenants enter their credentials again and are served |
| K4, the key lost entirely | TestKeyCasesFile | The script for each version; every tenant enters its credentials again; nothing else is lost |
| S1, the service restored from its snapshot | TestKeyCasesService | Snapshot restored into an empty server, unsealed with the original share, the original tokens work |
| S2, the old version still in range | TestKeyCasesService | Everything works; the worker re-wraps |
| S3, the old version trimmed | TestKeyCasesService | The policy of Pandokos cannot trim (403); the recovery with a `transit-…` identifier |
| S4, the key lost in three forms | TestKeyCasesService | Key deleted, service lost with no snapshot, snapshot older than a rotation with a database backup taken after the re-wrap: recovery for each version |
| N1, a later major | TestRestoreOnLaterMajor | Dump of B2 restored into PostgreSQL 18.7; the digests of the text of every `jsonb` column of both audit tables equal; the binary starts and both chains verify (decision O-1: nothing to open as an issue) |
| After a restore | TestAfterRestore | Steps 5.a to 5.e: a deleted tenant is back and its credential opens; the deletion requested again completes; the platform's events name the same tenants; the head kept after the backup does not verify, the head kept at the backup does; a revoked key answers 200 again; a deletion that was running completes without a new request |
| Events after a restore | TestEventsAfterRestore | Step 5.f (finding G-7) |
| Recovery and destinations | TestRecoveryAndDestinations | Finding F-1 |
| Dumps | TestDump | A dump by a role that row-level security binds fails; the dump, the base backup and the WAL archive hold no canary and no byte of a key file |
| Canaries | TestDrillCanary | No secret in any output, log or the report |
| Figures and report | TestFiguresAndReport | The figure case and the report |

Twenty-one cases ran (these and a few more of the suite's own); all passed.

## Findings

Each finding says what was observed; the guide carries the corrected procedure and the suite executes it.

- **G-1: the check of the heads.** The guide said `audit verify --expect-head <hash kept outside>` on the whole
  log, on the premise that `serve` writes rows at its first start. Observed: with `kek_activation: unchanged`
  `serve` writes no audit row, and the whole-log form passes against the head kept at the backup; when `serve`
  activates a key (a backup older than a rotation with the current key) it writes `kek.activate` and
  `kek.retire` to the platform's chain (head 8 became 10) and the whole-log form fails on an intact restore.
  Decision: step 4.6 is the audit guide's procedure for an older head, `audit export --to-seq N` then
  `audit verify --file … --expect-head H`, for every chain, with the `seq` kept beside each hash. The drill
  asserts the whole-log form fails exactly when rows follow the kept head. Tests: TestRestoreLogical and every
  restore.
- **G-2: commands.** The guide named the tools and gave no command. It now carries the command the suite runs under
  each step.
- **G-3: roles.** Confirmed: a physical backup carries the roles and the passwords of the login users; step 4.1
  is for a dump.
- **G-4, F-1: the recovery script and destinations.** Observed on the script as it was: after it a destination
  stays enabled, the relay logs once `event relay failing` with `cause secret` and delivers nothing, the
  destination reads "delivering" with no failure and no error, the cursor stops while the log grows, and entering
  a credential again (a new data key with other bytes) does not repair it. Nothing is ever signed with a wrong
  key. Decision (D-8): the script disables a destination whose current secret was sealed under the lost version,
  keeps its cursor, drops an affected previous secret, and prints three counts; the owner rotates the secret
  (this works for a tenant that has no data key yet: it creates one), gives the receiver the new secret and
  enables the destination, and the events the log still holds arrive signed with it. Tests: TestLostKeyRecovery,
  TestRecoveryAndDestinations.
- **G-5: a backup older than a rotation.** The two sentences of the guide could be read as opposites. Observed:
  `serve` activates the version of the key that wraps. With the current key as `key_file` and the old one in
  `unwrap_key_files` it starts with `kek_activation: activated`, names the current version, and the worker
  re-wraps; with the old key as `key_file` it starts `unchanged`; the refusal naming both versions is for a
  backup **newer** than the key held as `key_file`. The guide says that.
- **G-6: a key service and a rotation.** The rule and its two orders are in section 3 of the guide and in the
  external key guide. Observed: a snapshot older than the rotation with a database backup taken after the
  re-wrap is unrecoverable (`serve` refuses: the configured version was replaced); a trimmed version is the
  lost-key case. A restored snapshot needs the original unseal share and answers to the original root token and
  Pandokos's token, not to the new server's own; trimming needs `min_encryption_version` as well as
  `min_decryption_version`; deleting the key needs `deletion_allowed` first.
- **G-7: events after a restore (O-3).** Observed with the backup taken while a destination was behind: the log
  of a tenant was at 8 when the installation was lost and at 5 after the restore, and the next event took
  sequence 6, so sequences are issued again with new identifiers; five deliveries of an identifier the receiver
  had already seen were made (at-least-once, from the backup's cursor); `event export --after 18` past the
  restored head answers `200` with no events. Decision: stated in the guide (step 5.f), with the instruction to
  reset a puller's cursor; a script that moves the sequences past a value kept outside is not built and is an
  issue for the scale work of epic 10 (decision O-3).
- **G-8: the database of a dump.** `createdb` leaves `CONNECT` granted to everyone and a dump does not carry the
  grants on the database; step 4.2 creates the database with the database guide's statements.
- **G-9: `\restrict`.** The `pg_dump` of PostgreSQL 16.15 writes a random token in the `\restrict` and
  `\unrestrict` lines of a plain dump, so two dumps of the same schema differ; the comparison drops those lines.
- **G-10: parentheses of a CHECK.** A constraint written with `BETWEEN` inside an `AND` is stored nested, dumped
  with its parentheses and read back flat: the constraint is the same and the text differs by parentheses only.
  The comparison removes them in those lines; the schema, the owners, the grants (the database's too), the
  policies and the triggers are otherwise equal.
- **A later major.** PostgreSQL 18.7 refuses a base backup of major 16 (`database files are incompatible with
  server`: the data directory was initialized by version 16, which is not compatible with 18.7). A dump restores
  into it, the text of the `jsonb` columns of the audit tables is equal, the binary starts, and both chains
  verify. Nothing was found that needs a change in this release.

## What must be entered again

| Lost | Comes back | Cannot be recovered | The owner (tenant) must | The operator must |
|---|---|---|---|---|
| Nothing but time: backup and its key | everything as of the backup | usage, audit rows, events and administration after the backup | redo administration done after the backup | request again the deletions completed after the backup; revoke again what was revoked after it (a key revoked later answers 200 again); reset pull cursors (5.f) |
| The key of an old backup's version (case 3) | everything else | the provider credentials and signing secrets sealed under that version | enter each provider credential again; rotate each destination's signing secret and enable it | run the recovery script for the version; activate a new key |
| The key entirely (case 4) | tenants, identities, keys, roles, limits, prices, usage, audit, credential metadata, destinations' configuration | every provider credential, every signing secret | the same, every tenant | the same, for every version |
| One tenant's data key row | everything else | that tenant's credentials and signing secrets | the same, that tenant | nothing |
| The key service's unseal shares, with a snapshot | as case 4 | as case 4 | as case 4 | as case 4 |
| The database with no backup | nothing | everything | everything | install again |
| The audit heads kept outside | the chains still verify internally | the proof that the restored chain is the one that existed | nothing | anchor the heads again from now |

Inference keys, platform keys, passwords and sessions are stored as hashes and need no key: they come back with
the database and are never entered again. Secrets that are files of the configuration (database passwords, the
OIDC client secret, the key service's token) are the operator's and are in no backup of Pandokos.

## Figures

These figures are indicative: one machine (4 cores, 15 GB), Docker, one node, small data, no pooler, a local disk. The scale of a
real installation is for the work of epic 10.

- The whole suite, all cases, four at a time: **5 min 30 s** of wall time (the first run 5 min 23 s).
- A case: installing the world about 5 s, a rotation about 1 s, a deletion about 15 s (the settling wait of the
  worker is ended by the test, the grace period is not touched), a dump of the small world about 0.5 s, its
  restore about 1 s, `serve` ready about 0.5 s.
- The figure case: four tenants, 100,108 ledger records (100,000 written by SQL in this case only) and 10,042 audit
  rows (made through the product, so the chains are real). The dump took 3.8 to 8.4 s, the restore 8.3 s, the
  verification of a chain of that size about 0.4 s, the dump is about 29 MB.
- The key service cases take 60 to 95 s each, dominated by the deletion and the second server.

## Not drilled

HashiCorp Vault (its transit API is covered by the conformance suite; its snapshot is its own procedure), a backup
store's own encryption, a pooler in front of the target, a ledger at scale (epic 10), a restore by someone who
did not write the guide, a physical restore across a later major (refused by PostgreSQL), a cluster of key
service nodes (the drill proves the dependency on versions, unseal shares and order, not the service's
operations), and anything on the host that is not Docker.

## Who ran it

The suite, run by the implementer of the sprint on 2026-11-25 on the machine above. The only check that is not
made by the authors' own automation is a manual walk: by the maintainer's decision of 2026-11-25 it is a full
run in a lab, backup and restore included, following the corrected guide, **after epic 12 closes and before the
tag of v0.3.0**; its date and outcome are recorded here when it has been made. Until then this record is of the
automated drill alone.
