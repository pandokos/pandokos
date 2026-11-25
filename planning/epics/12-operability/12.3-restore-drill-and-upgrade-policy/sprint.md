---
id: SPRINT-12.3
title: Restore drill and upgrade policy
epic: EPIC-12
release: v0.3
status: in-progress
design: draft 2026-11-25
---

# Sprint 12.3 — Restore drill and upgrade policy

- Epic: [12 — Operability](../epic.md)
- Implementation plan: [plan.md](plan.md)
- Previous sprint: [12.2 — Threat model review and fuzzing](../12.2-threat-model-review-and-fuzzing/sprint.md)
- Scope: the epic file, section 12.3, with the items handed to 12.3 by sprints 6.2, 6.3, 6.4, 12.1
  and 12.2 and by the histories of epics 4 and 5 (§5.12).
- Risk class: **high** (the sprint destroys and restores databases and key services, runs the
  recovery script that revokes credentials, handles restored credentials and keys, and states what
  an operator may rely on for a restore and for an upgrade)
- Decisions implemented: [ADR 0019](../../../../docs/adr/0019-configuration.md) (static settings,
  declarative documents), [0026](../../../../docs/adr/0026-schema-migrations.md) (stop, migrate,
  start), [0066](../../../../docs/adr/0066-declarative-export-and-apply.md),
  [0068](../../../../docs/adr/0068-key-encryption-key-versions-rotation-and-erasure-in-backups.md),
  [0072](../../../../docs/adr/0072-event-types-the-envelope-the-external-reference-and-platform-events.md),
  [0073](../../../../docs/adr/0073-the-transit-key-provider.md),
  [0074](../../../../docs/adr/0074-threat-model-review-and-continuous-fuzzing.md) (a review record per release).
- Decision records changed by this sprint: new **ADR 0075** (upgrade policy and the tested upgrade;
  **amends** ADR 0026 "Consequences"), new **ADR 0076** (the restore drill, its record, and the
  recovery after a lost key; **amends** ADR 0068 and ADR 0073 on backups), new **ADR 0077** (an
  instance defined by configuration alone: the scope of a key, issuers, mappings, destinations and
  the external reference in the declarative document; **amends** ADR 0066 point 3 and ADR 0072);
  history lines on 0019, 0026, 0066, 0068, 0072, 0073 and 0074. Text in [plan.md](plan.md), part E.

The implementer follows [plan.md](plan.md); a gap in either document is raised as a question, not
filled silently ([planning/README.md](../../../README.md)). The design was written at commit
`eed15464357ba06b0a54b7159690c911fb3107ae` (2026-11-25); what was verified by command is §12.

| Given by the epic and the closed sprints | Designed in |
|---|---|
| Restore drill from backups, including loss of the key-encryption key | §5.1 to §5.6, T-1136 to T-1146 |
| Exit: a restore into an empty environment serves calls with the restored credentials | T-1137, T-1138 |
| Exit: the drill's record states what cannot be recovered and what the owner must re-enter | §5.6, §5.7, T-R101 |
| Upgrade policy and tested upgrade from the previous minor release | §5.8, §5.9, T-1129 to T-1135, T-R102 |
| Exit: the previous minor release upgrades with traffic | T-1130 |
| An instance defined by configuration alone, checked by two instances side by side | §5.10, T-1127, T-1147, T-R98, T-R99 |
| 6.2: issuers and subject mappings join the declarative document | §5.11, T-1119 to T-1122, T-1126 |
| 6.4 O-12: destinations (without secrets) and the external reference join it | §5.11, T-1123 to T-1126 |
| 6.3: the drill follows the backup guide as written, four cases of the key, a deletion replayed | §5.2, §5.4, §5.5, T-R100 |
| 6.2 R-6: both audit chains verified after restore, on the target version | §5.3, T-1137, T-1144 |
| 12.1 §11: the drill once with a key service; the service's own backup; the service's column | §5.5, T-1141 |
| 12.2 §11: a key that is not exportable; a review record per release; `allow_exportable_key` may differ | §5.5, §5.8, §5.10 |
| Epic 4 and epic 5 histories: five settings and two rules for "configuration alone" or the chart | §5.12 |

## 1. Outcome

An operator who has lost everything but the backups can follow
[the backup and restore guide](../../../../docs/operations/backup-and-restore.md) step by step and
end with an installation that serves calls with the restored credentials, and the guide has been
executed by a test, not only read: from a logical dump and from a physical backup with WAL, with a
key file and with a key service, in each of the four cases of the key. A dated record states, case
by case, what came back, what cannot be recovered and what each owner must enter again, with the
time each step took.

An operator who runs v0.2.x can upgrade to this version by a stated procedure whose cost is known:
the test starts the released v0.2.0, puts traffic on it, upgrades it, and asserts what happened to
every request. The policy says which upgrades are supported, what a patch release may contain, how
to go back, and what is checked at every release.

Two installations on one host share nothing but the binary: each is what its file or its environment
says, and what lives in the database of one can be written as documents and applied to another,
token issuers, subject mappings, event destinations and the external reference included.

## 2. Scope

### In scope

| Item | Where |
|---|---|
| The drill as an automated suite (`test/drill`, build tag `drill`, CI job `drill`): PostgreSQL servers of its own, logical and physical backups, restore into an empty server | §5.1 to §5.3 |
| After a restore: deletions replayed from the list kept outside and from the `tenant.deleted` events; revocations lost; usage lost; heads kept after the backup | §5.4 |
| The four cases of the key with a key file, and with a key service (OpenBao with its own storage, backed up and restored by its snapshot) | §5.5 |
| The recovery script after a lost key, with the signing secrets of event destinations (finding F-1) | §5.6 |
| The drill record `docs/operations/restore-drill-2026-11.md`, with figures | §5.7 |
| Corrections of the backup guide found by executing it (G-1 to G-7) | §5.2 |
| The upgrade policy: `docs/operations/upgrade.md`, ADR 0075 | §5.8 |
| The upgrade suite (`test/upgrade`, build tag `upgrade`, CI job `upgrade`) against a binary built from the tag v0.2.0, and one test against the released image | §5.9 |
| The scope of every configuration key (`instance` or `installation`), two instances side by side, a repository test on what the process reads | §5.10 |
| `export` and `apply`: platform-level issuers with their tenants, tenant token issuers, subject mappings, limit rules on a mapping, event destinations, the external reference | §5.11 |

### Not in scope

| Item | Where it goes |
|---|---|
| Rolling upgrades, two versions on one schema, a compatibility window for migrations | 8.1 and 8.2: needs replicas; ADR 0026 stands, the policy says so |
| A check at start that every instance of an installation has the same `installation` settings | 8.1 (the first sprint with more than one instance); this sprint classifies the keys and documents the rule |
| The Helm chart's wiring of `auth.public_origin`, the client secret, `support.*`, and `init` as a job | 8.2 (§5.12) |
| A default concurrency rule created by `init` or by a migration | not built: it is a line of a platform document (§5.12, D-17) |
| Restore figures at scale (10⁷ ledger records, 10⁴ tenants), failure injection during a restore | 10.1; the figures here are indicative |
| The drill and the upgrade test on the release candidate | epic 10 (its epic file); the suites are built to be re-run |
| HashiCorp Vault in the drill | not drilled: OpenBao is the tested reference of the epic; the record says so (O-9) |
| Backup tooling: scheduling, retention, encryption of the backup store, pgBackRest or Barman | the operator's; the guide names PostgreSQL's own tools only |
| Members, users, credentials, keys in the declarative document | never (ADR 0066) |
| Destinations in the tenant data export | not planned (6.4 O-12) |
| Kubernetes authentication to the key service | 8.2 |
| A new migration, a change of the API document, a permission, an error code, a module | none is needed (D-20) |

## 3. Requirements claimed

The meaning is quoted from [docs/requirements](../../../../docs/requirements/README.md).

| ID | Meaning | Delivered here | Extent |
|---|---|---|---|
| OPS-10 | "Backup and restore, including keys, are documented, and a restore is tested" | The guide executed as written by the suite of §5.1 to §5.6, its corrections, the record | **R3 (tested)**: full; OPS-10 is fully delivered |
| OPS-13 | "Releases follow semantic versioning with upgrade notes, and upgrading from the previous minor version is supported" | The policy and guide (§5.8), the upgrade from v0.2.0 with traffic (§5.9), the pin of the previous release under a test | full |
| VLT-16 | "The security limits of the vault and the recovery procedure for lost or compromised keys are documented, distinguishing recoverable secrets from those the owner must re-enter" | The recovery procedure executed for a key file and a key service; the table of what must be entered again, signing secrets included (§5.5, §5.6) | **R3 (tested)**: full |
| OPS-08 | "Static settings come from a file and environment variables; secrets are referenced as files. Dynamic settings live in the database and are managed through the API" | Two instances from two configurations; nothing else read by the process; the dynamic settings that distinguish installations in the declarative document (§5.10, §5.11) | full (R1 requirement, now proved side by side) |
| OPS-09 | "Schema migrations are versioned and run by an explicit command. An instance refuses to start against a schema it does not match" | Touched: the two refusals met during an upgrade, against a real previous binary (T-1131) | touched (test) |
| OPS-12 | "On shutdown, in-flight streams are drained up to a configurable deadline" | Touched: streams in flight at the stop of the old version complete (T-1130) | touched (test) |
| VLT-12 | "The key-encryption key comes from a pluggable provider: a local key file by default, an external KMS or transit service optionally" | Touched: the key service's backup, its restore and the loss of a version (§5.5) | touched |
| VLT-14 | "Secrets never appear in logs, traces, metrics, error messages, events or diagnostics. Automated tests plant canary secrets and search every output for them" | Touched: canaries over the drill's and the upgrade's outputs, the backup files and the secret files of `apply` (T-1133, T-1145, T-1126) | touched |
| AUD-04 (the chain) | as in sprint 6.2 | Touched: both chains verify after a restore, after an upgrade and on a later PostgreSQL major | touched (test) |
| CAT-10 | as in sprint 6.2 (`export`, `apply`) | Extended: five more sections (§5.11) | extended |
| EVT-03 | as in sprint 6.4 | Touched: what a receiver and a puller see after a restore is stated and tested (§5.4) | touched |

## 4. Repository layout

New: `test/drill/` (suite, `compose.yaml`, `openbao.hcl`), `test/upgrade/` (suite, `PREVIOUS`),
`test/instance/` (a process harness for the two suites), `docs/operations/upgrade.md`,
`docs/operations/restore-drill-2026-11.md`, `docs/adr/0075-…`, `0076-…`, `0077-…`,
`examples/declarative/platform.yaml`, `test/repo/operability_test.go`.

Changed: `internal/declare/{declare,parse,state,plan}.go`, `internal/cli/declare.go`,
`internal/config/keys.go` (a `Scope` on every key), `deploy/postgres/recover-lost-kek.sql`,
`test/container/container_test.go`, `test/e2e/{declare_test,e2e_test}.go` (two tests added, no
helper changed), `Makefile`, `.github/workflows/ci.yml`, `.github/dependabot.yml`, `CONTRIBUTING.md`,
and the documents of plan part E.

Not changed: `internal/store/migrations`, `api/openapi/admin.v1.yaml`, `go.mod`, the vault, the
gateway, the migrator.

## 5. Architecture

### 5.1 The drill: what it is

A Go test suite under the build tag `drill`, in `test/drill`, run by `make test-drill` and by the
CI job `drill`. It is not part of `make test`: it starts database servers and a key service of its
own and destroys them.

- **Each case owns its world.** A case starts a Compose project of its own
  (`docker compose -f test/drill/compose.yaml -p pandokos-drill-<random>`) with the services it
  needs: `source` (PostgreSQL 16, WAL archiving on), `target` (PostgreSQL 16, empty), `target-next`
  (the later major of O-1, empty), `openbao` (OpenBao with integrated storage, not development
  mode) and `openbao-target` (the same, empty). Ports are published on `127.0.0.1` with a port the
  system chooses and read back with `docker compose port`. A case calls `t.Parallel()`; nothing is
  shared between cases but the built binary. Everything is removed at the end of the case, on
  failure too (`down -v`).
- **The tools are the guide's and run where the data is:** `pg_dump`, `pg_restore`,
  `pg_basebackup` and `psql` are run with `docker compose exec` in the PostgreSQL containers
  (verified present in the pinned image, §12), so the host needs Docker and Go only. Backup files
  live in a directory of the test (`t.TempDir`), mounted in both servers.
- **The installation is installed as the guides say**, not seeded by SQL: `deploy/postgres/roles.sql`,
  the login users of the database guide, `pandokos migrate`, `init`, `serve`, then the command line
  for tenants, credentials, keys, limits, issuers, destinations. Calls go through the inference
  listener to `test/fakeprovider`, which records the credential it was given.
- **The world of a case** (`drill.World`): four tenants `a` to `d`, each with a provider credential
  whose value is a canary, an inference key, a quota, a signed destination on a receiver of the
  test, audit rows and usage; `c` is the tenant deleted later; `d` has a key revoked later. Time
  points: **B1** a backup; **ROT** a rotation of the key (re-wrap complete, old version retired);
  **B2** a backup; **DEL** the deletion of `c` completes and the key of `d` is revoked, more usage;
  **X** the disaster: the `source` service and its volumes are removed. At B1, B2 and X the test
  keeps "outside" (a directory that survives X) what the guide's §1 says to keep outside: the heads
  of the chains, the list of completed deletions, and the platform's events pulled so far.
- **Steps are named after the guide.** Every action of a case is a `drill.Step` whose identifier is
  the guide's (`4.1` roles … `4.7`, `5.a` … `5.e`, `6.1` … `6.4`, and `6.1s` … `6.4s` for the key
  service). The guide carries the same identifiers as anchors; T-R100 holds the two sets equal, so
  a step added to the guide without a drill step, or the reverse, fails.
- **The report.** With `PANDOKOS_DRILL_REPORT=<file>` the suite writes one JSON document: for each
  case its identifier, outcome, the steps with their durations, the versions (binary, PostgreSQL,
  key service) and the sizes (rows of the ledger and of the audit logs, bytes of the backup). It
  holds no name of a tenant, no secret and no address. The record (§5.7) is written from it.

### 5.2 The guide as written, and what executing it is expected to correct

The drill executes `backup-and-restore.md` literally. Where the guide gives no command, or one that
does not do what the text says, that is a finding **G-n**: the guide is corrected, the drill
executes the corrected text, and the record lists the finding. Seven are known from reading (§12
says which were confirmed by command); the implementer adds what the first run finds.

| # | Finding (from reading) | Decision |
|---|---|---|
| G-1 | §4 step 6 says `audit verify --expect-head <hash kept outside>`; the audit guide says `--expect-head` is the hash of the *last* row read, and `serve` writes rows at its first start after a restore, so a kept head is no longer the last row | The step becomes the audit guide's own procedure for an older head: `audit export --to-seq N` (the `seq` kept with the head) then `audit verify --file … --expect-head H`; the same for the platform's chain. T-1137 asserts both the wrong and the right form |
| G-2 | §1 and §4 name the tools and give no command line | The guide gains the commands the drill runs (§5.3), each under its step identifier |
| G-3 | §4 step 1 ("roles, before the data") is true for a dump only: a physical backup carries the roles and the passwords of the login users | The step says so; the drill's physical case skips it and asserts the roles are there |
| G-4 | `recover-lost-kek.sql` predates event destinations: their signing secrets are sealed under the tenant's data key and the script does not touch them; the vault guide's table of what must be entered again names credentials only | Finding **F-1**, §5.6, D-8 |
| G-5 | §6 row 2 ("start with the **current** key as `key_file` and the old key in `unwrap_key_files`") and §4 step 6 first bullet ("put the key of the active version in `key_file`") can be read as opposite instructions for a backup older than a rotation | T-1140 executes row 2 both ways and the guide keeps the one sentence that is true, with the log line that proves it |
| G-6 | With a key service the guide says "back the service up by its own procedure" and does not say **when** relative to a rotation in the service | §5.5: the rule and its two orders, both drilled |
| G-7 | §5 says the events recorded after the backup are lost; it does not say what a receiver and a puller see next (replayed deliveries, sequences issued again) | §5.4: observed by T-1143, stated in the guide and in the events guide; O-3 |

The guide's §7 ("What sprint 12.3 adds") becomes "Drills": how to run the suite against your own
procedure's assumptions, the link to the record, and the advice to rehearse on a spare instance,
which stays.

### 5.3 Backups and restores the drill makes

**Logical (case family L).** As the guide's §1: a dump of the Pandokos database by a superuser, in
the custom format; roles are not in it.

```sh
pg_dump -U postgres -d <database> -Fc -f /backup/pandokos.dump                        # 1.a
psql -U postgres -v ON_ERROR_STOP=1 -f roles.sql ; (login users, database guide §2)   # 4.1
createdb -U postgres -O pandokos_owner <database>                                     # 4.2
pg_restore -U postgres -d <database> --exit-on-error /backup/pandokos.dump            # 4.2
pandokos migrate status                                                               # 4.3
```

The exact options are settled by running them (plan C5) and are then the guide's; the constraints
are fixed here: the dump is made by a superuser and **fails** when made by a role that row-level
security binds (T-1136: `pg_dump` refuses rather than writing a partial dump, because it runs with
`row_security` off); the restore is one `pg_restore` of the whole archive with `--exit-on-error`
into a database created empty after the roles, so that data is loaded before triggers and policies
exist and the sealed audit rows arrive byte for byte; no `--data-only`, no `--disable-triggers`,
no `--no-owner`, no `--no-acl`. Proof that the restore is whole: the schema-only dump of the target
equals the source's (objects, owners, grants, policies, triggers), `migrate status` exits 0, and
`serve` passes its start checks (database guide §5), which include ownership and membership.

**Physical (case family P).** The `source` server runs with `wal_level=replica`, `archive_mode=on`
and an `archive_command` that copies to the backup directory.

```sh
pg_basebackup -U postgres -D /backup/base -Ft -z -X none --checkpoint=fast            # 1.b
SELECT pg_create_restore_point('<name>') ; SELECT pg_switch_wal()                     # at B1, B2, X
# target: unpack base into an empty data directory, restore_command = 'cp /backup/wal/%f %p',
# recovery_target_name = '<name>' (or none: to the end of the archive), recovery.signal    # 4.2
```

One base backup serves every time point: recovery to the restore point of B1 or B2, or to the end
of the archive (which holds DEL). The target must be the same major version; the drill asserts the
refusal of the other major is PostgreSQL's own and says so in the guide.

**A later major (R-6 of sprint 6.2).** The dump of B2 is restored into `target-next`. Two checks,
the first independent of Pandokos: for each audit table, a digest over `seq` and the text of every
`jsonb` column is equal on source and target (the canonical encoding of the chain depends on that
text); then the binary runs against the target and both chains verify (T-1144). If the binary does
not start on that major for a reason that is not the restore, that is a finding of its own, the
second check is recorded as not run, and the first still stands.

**Keys.** A key file is copied at generation and at each rotation to the "outside" directory, apart
from the dump (the guide's §1). The key service is backed up by its own snapshot (§5.5).

**What is asserted after every restore** (the guide's §4 step 6, as corrected):

1. `vault ready` names the expected `kek_version`, and `kek_activation` has the value the case expects.
2. Both chains verify against the heads kept at the backup's time (G-1).
3. For every tenant that should work, a call with its inference key reaches the provider, and the
   provider received **the credential's canary value**: the restored credential opens. This is the
   exit criterion.
4. `pandokos kek-version list` shows the versions and states of the backup.
5. Limit counters equal what the restored ledger gives: the quota of each tenant refuses at the
   count the ledger holds.
6. A destination receives a new event signed with the secret issued before the backup.
7. Nothing was rotated before these passed (step 4.7).

### 5.4 After a restore (the guide's §5)

Each sentence of the guide's §5 becomes an assertion of T-1139 or T-1143:

| Step | The guide says | The drill asserts |
|---|---|---|
| 5.a | Request again the deletions completed after the backup | After restoring B2, tenant `c` is back and its credential opens. The list of completed deletions kept at X minus `pandokos tenant deletion list` gives `c`; `pandokos tenant delete` runs again (suspension, the grace period of the case, completion); `c` is gone, its data key is gone, a completion record exists again |
| 5.b | The platform's events list the deletions too | The `tenant.deleted` events pulled before X and kept outside name exactly the same tenant, with its external reference; the two lists are equal |
| 5.c | Expect the usage after the backup to be missing, and the audit rows with it | The ledger and the chains end at B2; a head kept at X is reported as a mismatch in the way the corrected guide says; the heads kept at B2 verify. The budget consumed after B2 is available again, and the record says so |
| 5.d | Sessions and keys revoked since the backup are valid again | The key of `d` revoked at DEL **answers 200** after the restore, until it is revoked again; the record lists it under what the operator must redo |
| 5.e | A deletion that was running continues | A case whose backup is taken during the grace period: after the restore the deletion completes without a new request |
| 5.f (G-7) | Events after the backup are lost | A destination's cursor is the backup's: events between the cursor and B2 are delivered again (the receiver sees identifiers it has already seen, which is at-least-once); new events take sequences that were already issued before X; a puller that kept a cursor past B2 is told what to do in the guide. T-1143 records what happens today; O-3 decides whether anything is built |

### 5.5 The four cases of the key, for a key file and for a key service

The rows are the guide's §6; the service's column is added by this sprint (12.1 §11). "Works" means
the seven assertions of §5.3; "recovery" means §5.6.

| Case | Key file (`K`) | Key service (`S`) | Outcome |
|---|---|---|---|
| 1 | B2 and the file of every version it needs | B2 and the service with the versions that wrapped B2's data keys in range of `min_decryption_version`; the **service itself restored from its own snapshot into an empty server**; the same `vault.kek.transit.*` settings and credentials | Everything works; nothing is entered again |
| 2 | B1 (older than the rotation) and the old key kept | B1 and the service after a rotation, the old version still in range | Everything works; the worker re-wraps again; no tenant action |
| 3 | B1 without the old key | B1 after the old version was **trimmed** in the service (`min_decryption_version` raised, then `trim`) | The credentials sealed under data keys of that version cannot be opened, ever: recovery with that version's identifier; a new key; those tenants enter their credentials again; everything else restores |
| 4 | The key lost entirely | The key deleted, or the service lost with no snapshot, or **a snapshot older than a rotation with a database backup taken after the re-wrap** | The same for every version: recovery for each, a new key, every tenant enters its credentials again; nothing else is lost |

**The key service in the drill.** OpenBao, the pinned image of the transit suite, started as a
server with integrated storage (`test/drill/openbao.hcl`), initialized with one unseal share,
unsealed; the transit engine, the key and the policy of `deploy/transit/pandokos-policy.hcl` are
created by the test with the root token; Pandokos authenticates with a token of that policy. The
key is created with the service's defaults, so it is **not exportable**:
`vault.kek.transit.allow_exportable_key` is never set in the drill, `pandokos kek check` reports no
weak setting, and the case fails if it does (12.2 §11). For the same reason the transit engine's
own key backup endpoint is not the procedure: it needs an exportable key. The service's backup is
its storage snapshot (`GET sys/storage/raft/snapshot`), restored into an empty, initialized server
with `POST sys/storage/raft/snapshot-force`, then unsealed with the **original** unseal share: the
operator must keep the unseal shares and a token of the original service with the snapshot, and the
guide says so. Verified by command while designing (§12).

**The rule for a key service** (D-7, external key guide §10, backup guide §3):

1. Snapshot the service **after every rotation of the key in it and before Pandokos is restarted**
   to use the new version. A database backup taken after the re-wrap needs the new version; a
   snapshot older than the rotation does not hold it (case 4s, third form: unrecoverable).
2. Do not trim a version, and do not raise `min_decryption_version` past it, while a database backup
   you keep was taken before the re-wrap away from it completed (case 3s). `pandokos kek-version list`
   and the date of the backup tell which.
3. Keep, with each snapshot: the unseal shares it needs, and the means to authenticate (the policy
   and a way to issue a token; an AppRole comes back with the snapshot).

The second rule is the erasure rule of ADR 0068 read from the other side: trimming is how the
credentials of deleted tenants leave old backups, and it is also how the credentials of everyone
leave them.

### 5.6 Recovery after a lost key, and what must be entered again (F-1)

`deploy/postgres/recover-lost-kek.sql` today revokes the provider credentials sealed under data keys
of the lost version and removes those data keys and the version. It was last changed in sprint 6.3;
sprint 6.4 then sealed the signing secret of every event destination under the same data keys
(`event_destination.secret_key_version`, `previous_secret_key_version`), with no reference between
the tables. After the script a destination still names a data key version that no longer exists, or
that a later credential write creates again with other bytes.

**D-8.** The script, in the same transaction, for every destination whose secret or previous secret
was sealed under a data key of the lost version: sets `enabled = false` (the relay skips it; its
cursor stays, so nothing is skipped when it is enabled again); and clears the previous secret where
only that one is affected. It does not delete the destination (its URL, types and cursor are
configuration the owner keeps) and cannot give it a secret (only the owner's `rotate-secret` can).
The owner runs `pandokos event-destination rotate-secret`, gives the receiver the new secret, and
enables the destination; events kept by `events.retention` are then delivered. The script prints
the number of credentials revoked and of destinations disabled. T-1142 is written first and shows
what happens today without the change; the outcome is in the record.

What the record and the vault guide state (the table of VLT-16, replacing the vault guide's §5 table):

| Lost | Comes back | Cannot be recovered | The owner (tenant) must | The operator must |
|---|---|---|---|---|
| Nothing but time: backup and its key | everything as of the backup | usage, audit rows, events and administration after the backup | redo administration done after the backup | request again the deletions completed after the backup; revoke again what was revoked after it; reset pull cursors (5.f) |
| The key of an old backup's version (case 3) | everything else | the provider credentials and signing secrets sealed under that version | enter each provider credential again; rotate each destination's signing secret and enable it | run the recovery script for the version; activate a new key |
| The key entirely (case 4) | tenants, identities, keys, roles, limits, prices, usage, audit, credential metadata, destinations' configuration | every provider credential, every signing secret | the same, every tenant | the same, for every version |
| One tenant's data key row | everything else | that tenant's credentials and signing secrets | the same, that tenant | nothing |
| The key service's unseal shares, with a snapshot | as case 4 | as case 4 | as case 4 | as case 4 |
| The database with no backup | nothing | everything | everything | install again |
| The audit heads kept outside | the chains still verify internally | the proof that the restored chain is the one that existed | nothing | anchor the heads again from now |

Inference keys, platform keys, passwords and sessions are stored as hashes and need no key: they
come back with the database and are never entered again. Secrets that are files of the configuration
(database passwords, the OIDC client secret, the key service's token) are the operator's and are not
in any backup of Pandokos; the guide's §1 lists them with the configuration.

### 5.7 The record

`docs/operations/restore-drill-2026-11.md`, linked from the backup guide. Sections, held by T-R101:
what was drilled (commit, versions, the suite's command); the cases with outcome and proof (case
identifier, test name); the findings G-n and F-n with their decision; the table of §5.6; the
figures; what was not drilled (HashiCorp Vault, a backup store's own encryption, a pooler in front
of the target, a ledger at scale, a restore by someone who did not write the guide, see O-7); the
date and who ran it. The figures are the durations of the report for a stated dataset (10⁵ ledger
records, 10⁴ audit rows, four tenants: the bulk written by SQL in the figure case only, T-1146) on
the machine of the run, labelled indicative; scale figures are sprint 10.1's.

A drill is repeated, with a new dated record, when the guide's procedure changes, when a migration
changes a table the vault or the chains use, and on every release candidate (epic 10). A release
whose record is the previous one says so in its page.

### 5.8 The upgrade policy (ADR 0075, `docs/operations/upgrade.md`)

1. **Versions.** Semantic versioning. Before 1.0 a minor version may break compatibility and only
   the latest minor is supported (CONTRIBUTING, SECURITY.md: unchanged). Every release page has
   "Breaking changes" (each with what to do) and "Upgrade notes".
2. **The supported upgrade** is from **any patch of the previous minor** to the new minor: v0.2.x to
   v0.3.y. It is tested on every pull request from the newest patch of the previous minor, pinned in
   `test/upgrade/PREVIOUS` (§5.9). From an older minor, upgrade through each minor in turn, running
   the newest patch of each: the migrations would apply in one run, but the breaking changes and the
   start checks of the versions in between are not tested together, so it is not supported.
3. **A patch release** (v0.3.0 to v0.3.1) contains no migration, removes or renames no configuration
   key, changes no default in a way that changes behaviour, and has no breaking change. It is
   installed by replacing the binary, and undone by putting the previous patch back. Consequence:
   every patch of a minor has the same schema, which is what makes "any patch" in point 2 true.
4. **The procedure of a minor upgrade** (the guide gives the commands): read the release page; back
   up (database, key, configuration) and keep the heads of the chains; `pandokos config check` with
   the **new** binary against the **current** configuration; stop every instance (they drain);
   `pandokos migrate` with the new binary; start; check (`migrate status`, `/readyz`, `vault ready`
   with `kek_activation: unchanged`, a call, both chains). The installation does not serve between
   the stop and the start: the test measures that interval and the notes give it.
5. **No two versions at once.** ADR 0026 stands: a binary serves only the schema it was built for,
   an instance whose schema changes under it turns not ready, and an old binary does not start on a
   new schema. Rolling upgrades need a compatibility rule for migrations and more than one instance;
   they are decided with epic 8.
6. **Going back** from a minor is a restore of the backup taken before `migrate`, run by the old
   binary; migrations are forward-only. What was written after the upgrade is lost, as in any
   restore (§5.4). This path is tested (T-1132).
7. **Formats** that leave the installation are versioned by themselves and grow by addition within
   their version: the declarative document (`pandokos: v1`), the tenant export (`v1`), the events
   (`.v1` types), the audit encoding (`pandokos-audit-v1`), the administration API (`/v1`). A
   document written by the previous minor is accepted by the new one (T-1135).
8. **PostgreSQL.** The minimum is 16. A move to a later major is the database's own procedure; the
   chains stay verifiable across it (§5.3), and the record names the majors the drill ran on.
9. **At every release** (CONTRIBUTING, "Releasing", step 4 gains what is new): the `upgrade` and
   `drill` jobs are green on the commit; the threat model was reviewed and a record file exists for
   the release (`docs/architecture/threat-model-review-YYYY-MM.md`, short when nothing changed: ADR
   0074, 12.2 §11); the release page links it and the drill record in force. T-R102 holds the link
   for every release page from v0.3.0 on.
10. **After a minor is released**, `test/upgrade/PREVIOUS` moves to it in the first pull request on
    `main` (a step of "Releasing"), and after each of its patch releases to that patch.

### 5.9 The tested upgrade

**The previous release in the tests.** Both ways of obtaining v0.2.0 were verified by command (§12):
the tag's commit builds with the repository's toolchain, and the released image exists.

- `test/upgrade/PREVIOUS` holds three lines: `version=`, `commit=` (the commit the tag points to)
  and `image=` (the released image by digest). The values are written by the implementer from the
  commands of plan A3; T-R102 checks their form and that `version` is the newest patch of the minor
  before the one in development, as `docs/releases` tells it.
- `make previous-binary` fetches that commit (`git fetch origin <commit>`, which needs no tag and no
  credentials on a public repository, so it works in a shallow CI checkout), extracts it with
  `git archive` into `local/previous/src` and builds `local/previous/pandokos` with the Makefile's
  flags, `VERSION` and `COMMIT` from the file. T-1129 asserts the result is that version and that
  commit and needs schema version 16.
- The suite `test/upgrade` (build tag `upgrade`, `make test-upgrade`, CI job `upgrade`) uses the
  test PostgreSQL of `make db-up` through `dbtest.New` (an empty database: the old binary's
  `migrate` creates the schema), so its tests are parallel like the end-to-end ones.
- The released image is used once, in the `image` job, where a built image of this tree exists:
  T-1134 runs the previous image by digest (`migrate`, `init`, `serve`, a tenant, a credential, a
  call), then the new image (`migrate`, `serve`), and the call works. It proves the artefact
  operators run, and guards the build from the tag against drift.

**The upgrade with traffic (T-1130, the exit test).**

1. *The world, made by v0.2.0 through its own commands:* three tenants with provider credentials
   (canaries), inference keys, a daily request quota that the test will reach, a tenant price list,
   a platform-level issuer and a subject mapping, a local account with a session, a revoked key.
   Heads of the chains are kept. No SQL writes the world.
2. *Traffic:* for each tenant a closed loop of clients, plain and streaming calls to the fake
   provider, each call identified; every outcome is recorded with its times. The fake provider
   echoes which credential it saw.
3. *The upgrade, as §5.8 point 4:* `config check` with the new binary on the old configuration
   exits 0; `pg_dump` (kept for T-1132); SIGTERM to v0.2.0; `migrate` with the new binary prints
   `applied 0017_events` and `schema is at version 17`; the new `serve` starts on the same
   configuration. Clients keep calling throughout and find the instance again by themselves.
4. *Asserted:*
   - every call ended in exactly one of: a complete answer; a refusal by quota; a connection refused
     or closed **before any byte of an answer**, and only between the SIGTERM and the new instance's
     readiness. No call got a partial answer, a `5xx` other than a `503` while draining, or an
     answer made with another tenant's credential;
   - streams in flight at the SIGTERM completed (OPS-12), and v0.2.0 exited 0 within its deadline;
   - for every complete answer the ledger holds exactly one usage record, written by either version;
     none was lost at the stop;
   - the quota is one quota across the upgrade: the number admitted in the day equals the limit, and
     refusals carry the same headers before and after (counters are rebuilt from the ledger);
   - the keys and the session still authenticate; the revoked key is still refused; a token of the
     issuer still maps to its service account;
   - both chains verify with the new binary, and the export up to the kept `seq` verifies against
     the kept head: rows sealed by v0.2 verify under v0.3;
   - events exist for the usage recorded after the migration and for none before it;
   - `vault ready` has the same `kek_version` and `kek_activation: unchanged`; no data key was
     re-wrapped;
   - the interval without service is written to the report; it is bounded only by a generous
     ceiling (60 s) so the test does not measure the machine.
5. *Canaries (T-1133):* no credential value, key, password or session token in the output of either
   binary, of `migrate` or of `config check`.

**Around it.** T-1131: an old instance left running while `migrate` runs turns not ready with the
schema in the check's text, and what its listeners then answer is asserted and written in the
guide's "if you forget to stop an instance"; started again it refuses with "the database schema is
newer than this binary". T-1132: the dump taken before `migrate`, restored into an empty database,
is served by v0.2.0 with the credentials opening, while the upgraded database is untouched. T-1135:
the documents `pandokos export` of v0.2.0 writes are accepted by the new `apply` on the upgraded
installation with an empty plan.

Nothing in this sprint needs a migration, so the upgrade from v0.2.x crosses exactly one: `0017_events`
(verified, §12).

### 5.10 An instance defined by configuration alone

**What is already true** (verified, §12): the static configuration has 144 keys (the rows of the reference's table); a unit test
(`config.TestKeysTable`) holds the key table equal to the leaves of the configuration structure, so
every setting can come from the file and from the environment; the process reads the environment in
one place (`cmd/pandokos/main.go` passes it to `config.Load`) and one guard
(`internal/store/conn.go` refuses the service variables of libpq); `auth.public_origin`,
`auth.oidc.client_secret_file`, `support.enabled`, `support.max_duration` and `limits.time_zone`
are keys of that table.

**What this sprint adds.**

1. **A scope on every key** (D-13). `config.Key` gains `Scope`: `instance` (may differ between two
   instances of one installation: listeners and their TLS files, `log.level`, cache lifetimes, pool
   sizes, timeouts, `telemetry.*`, `shutdown.*`) or `installation` (must be equal on every instance
   that shares a database: `database.address`, `database.name`, `vault.kek.*`, `limits.time_zone`,
   `limits.on_*`, `pricing.default_currency`, `retention.*`, `tenant_deletion.*`, `events.*`,
   `tokens.*`, `auth.*`, `support.*`, `idempotency.*`). The configuration reference shows the scope
   of each key and has a section "What makes an installation"; T-R99 holds the document equal to
   the table and T-1147 refuses a key without a scope. The assignment of each of the 144 keys is
   made in plan C3 by one rule: *if two instances on one database disagreed on it, would a request
   be decided differently depending on which instance took it, or would stored data be read
   wrongly?* Then it is `installation`. No check at start compares instances: that needs a place to
   compare, and is 8.1's (§2).
2. **A repository test on what the process reads** (T-R98): in `cmd/` and `internal/`, outside test
   files, no call reads the environment, the host name, the home or working directory, except the
   two places above; every `PANDOKOS_…` name in the code is a key of the table, `PANDOKOS_CONFIG`,
   a variable of the API commands (`PANDOKOS_CLI_…`) or the one listed test hook.
3. **The ambient environment, stated.** The configuration reference gains a table of the variables
   other software defines and what Pandokos does with each: `PG*` (neutralised; `PGSERVICE` and
   `PGSERVICEFILE` refused), `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` and `OTEL_*` (as their
   warnings say today), `SSL_CERT_FILE` and `SSL_CERT_DIR` (read by the Go runtime for the system
   trust store, which only the connections without a configured CA file use), `TZ` (not used:
   every zone is named), `GODEBUG` (the runtime's). The table says what is, from the code; nothing
   is changed in this sprint, and a variable that turns out to change behaviour silently is a
   finding raised to the maintainer.
4. **Two instances side by side (T-1127).** One binary, two processes, one host, the same ambient
   environment (with `PGHOST`, `PGPASSWORD`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `HTTPS_PROXY` and `TZ`
   set to values that would break either if read):

   | | Instance A | Instance B |
   |---|---|---|
   | Defined by | a file only (`--config`), no `PANDOKOS_…` variable | the environment only, no file |
   | Database | its own | its own |
   | Key-encryption key | `provider: local`, a key file | `provider: transit` (the test double), `allow_exportable_key: true` on a key made exportable, with the refusal asserted first without it |
   | Trusted issuers | a platform-level issuer on fake IdP 1, applied from a document | a tenant issuer on fake IdP 2, applied from a document |
   | Listeners | plain, loopback | TLS on both, its own certificate; `auth.public_origin` set |
   | Telemetry | collector 1, its resource attributes, metrics by OTLP | collector 2, other attributes, Prometheus scrape |
   | Other | `support.enabled: false`, `limits.time_zone: Europe/Rome` | `support.max_duration: 30m`, `limits.time_zone: UTC` |

   Asserted: each serves a call with its own credential; a key, a token, a session and a platform
   key of one are refused by the other; each collector receives only its instance's telemetry, with
   its attributes; a support action is refused on A and limited to 30 minutes on B; a tenant's day
   starts in each instance's zone; `pandokos config check` of each configuration is clean but for
   the expected warnings; nothing of A appears in B's log or database and the reverse (identifiers
   and canaries are searched for).
5. **The closure (T-1126).** What distinguishes two installations in the database, apart from
   people, secrets and usage, can be written and applied: §5.11.

What stays outside a document, by decision (ADR 0066, restated in the declarative guide): creating a
tenant (it issues the first key), people, credentials, keys, sessions, usage. A new installation is:
the file or the environment, `migrate`, `init`, the platform document, then for each tenant its
creation and its document.

### 5.11 The declarative document: five more sections

All through existing operations of the administration API; no operation is added.

| Kind | Section | Natural key | Members | Rules |
|---|---|---|---|---|
| `platform` | `platform_issuers` | `name` | `description`, `issuer`, `audience`, `jwks_uri`, `algorithms`, `subject_claim`, `tenant_claim`, `required_claims`, `enabled` | every member is mutable: `update` |
| | `platform_issuers[].tenants` | `tenant` (the tenant's **name**) | `claim_value` | `claim_value` is exported only when it is not the tenant's ID, and an absent one means the default (the tenant's ID **in the target**), so a document is portable; a tenant that does not exist is a `conflict`; a tenant listed in the installation and not in the document is `not in document` |
| | `tenants` | `name` | `external_reference` | exported for tenants that have a reference; `apply` only **updates** the reference of an existing tenant; an absent tenant is a `conflict` (`apply` never creates a tenant: its first key is a secret); an empty reference leaves it alone |
| `tenant` | `token_issuers` | `name` | the members of the create body | `update` on any difference |
| | `subject_mappings` | `issuer` (`scope`: `tenant` or `platform`, and `name`), `subject` | `service_account` (a name), `description` | a mapping has no update operation: a different service account or description is a `conflict`; an issuer or a service account that does not exist is a `conflict` |
| | `event_destinations` | `name` | `description`, `url`, `event_types`, `enabled` | no secret, no cursor, no delivery state. `update` on any difference. **Creation** is below |
| | `limits[].subject` | | gains the form `mapping: {scope, issuer, subject}` | the rules on a subject mapping, left out and listed in a comment until now, are exported and applied; the comment line goes |

**Creating a destination returns a signing secret once** (D-15). `apply` gains
`--secrets-out DIR`. With it, an absent destination is created and its secret is written to
`DIR/event-destination-<name>` (the directory must exist and be accessible by its owner only; the
file is created exclusively with mode 0600; an existing file is an error before any call; the secret
is never printed). Without it, an absent destination is reported
`skipped: event_destination <name> needs --secrets-out` and nothing is created. `--dry-run` writes
nothing. `apply` never rotates a secret. This keeps "no secret in a document" and never loses a
secret that was shown once.

**Order.** Platform: … existing order …, then `tenants` (external reference), then
`platform_issuers` and their tenants. Tenant: … service accounts and roles, then `token_issuers`,
then `subject_mappings`, then limits (which may now name a mapping), price lists, the currency, then
`event_destinations`.

**Unchanged:** nothing is deleted, no `--prune`; the plan of a document just exported is empty; the
document is `pandokos: v1` (additive, §5.8 point 7); a forbidden member (`secret`, `signing_secret`,
`id`) is an invalid document, exit 2.

`examples/declarative/platform.yaml` is a platform document an operator can start from: a provider
with an endpoint and a model, and a default ceiling `platform_limits: [{tenant: default, metric:
concurrent_requests, limit: 256, enforce: true}]` (§5.12). T-R103 parses it with `declare.Parse`.

### 5.12 Items carried forward, each with its decision

| From | Item | Decision |
|---|---|---|
| 6.3 §11, D-20 | The drill follows the guide as written, four cases, a deletion replayed, `tenant.deleted` events; OPS-10 fully delivered | Delivered: §5.1 to §5.5, T-R100; the requirements page marks OPS-10 tested |
| 6.3 §12 | `pg_dump` and physical backups as the guide describes them | Delivered: §5.3, both families |
| 6.2 §11 | `apply` gains platform-level issuers, tenant token issuers and subject mappings | Delivered: §5.11 |
| 6.2 R-6 | The chain's canonical encoding depends on the text `jsonb` writes for numbers | Delivered: §5.3 "A later major", T-1144; the chains are also verified after every restore and after the upgrade |
| 6.2 §11 | The drill compares the head with one anchored before the backup | Delivered, with correction G-1 |
| 6.4 O-12 | Destinations (without secrets) and the external reference join `export`/`apply` | Delivered: §5.11, D-15 |
| 12.1 §11 | The drill once with a key service; the service's backup; "do not trim what a kept backup needs"; the service's column; the trimmed version as the unrecoverable case | Delivered: §5.5, D-7, T-1141 |
| 12.1 §11 | The two instances include one with `provider: transit` | Delivered: §5.10 (instance B) |
| 12.1 §11 | The upgrade from the previous minor needs nothing for 12.1 | Confirmed: v0.2.0 has no key service; the upgrade keeps `provider: local`; nothing is tested for it |
| 12.1 epic note | Figures for the restore with a key service | Delivered as indicative figures in the record; scale is 10.1's |
| 12.2 §11 | The drill uses a key that is not exportable, or sets the option and says why | Delivered: not exportable, the option never set in the drill (§5.5) |
| 12.2 §11 | The policy states the threat model is reviewed at every release, a record file per release | Delivered: §5.8 point 9, T-R102 |
| 12.2 §11 | The two instances may differ in `allow_exportable_key` | Delivered: §5.10 (B sets it, with the refusal asserted first) |
| Epic 4 history | `auth.public_origin` from the ingress host; the client secret; `support.enabled`; `support.max_duration` | They **are** configuration alone already (four keys of the table, the secret as a file); the two instances differ in three of them. Deriving the origin from an ingress host and mounting the secret are the chart's: **8.2** |
| Epic 4 history | `init --admin-username` as a job | `init` stays a command run once with the admin database credential; it takes no configuration key (its flags are an action, not a setting). Running it as a job is the chart's: **8.2** |
| Epic 5 history | `limits.time_zone` must be the same on every instance | Delivered as the scope `installation` (§5.10); enforcement between instances is **8.1** |
| Epic 5 history, 5.1 O-4 | A shipped default concurrency rule created by `init` or the chart | Not by `init` and not by a migration (an upgrade must not start refusing traffic, 5.1 D-10): it is one line of the example platform document (§5.11); the chart may apply it: **8.2** (D-17) |
| Epic 6 history | A move of PostgreSQL's major version keeps the chain verifiable (checked by the 12.3 drill) | Delivered: T-1144 |
| Epic 10 file | The drill and the upgrade test repeated on the release candidate | Enabled: both are `make` targets and CI jobs; the repetition is epic 10's |

Nothing else names sprint 12.3 or epic 12 in the "Carried forward" sections of `docs/history` or in
the maintainer-decision tables of the closed sprints (searched, §12).

## 6. Isolation and permissions

No table, policy, grant, route or permission is added or changed. `export` and `apply` read and
write the new sections with the permissions their operations already need (`token_issuer:*`,
`subject_mapping:*`, `platform_issuer:*`, `event_destination:*`, `tenant:write` for the reference);
a missing one is the API's `403`. The recovery script is run by a superuser, as today, and its new
statement touches only destinations of the tenants whose data keys it removes. The drill's and the
upgrade's worlds have several tenants so that every "works" assertion is also a "with its own
credential" assertion; the cross-tenant suite is unchanged.

## 7. Test infrastructure and CI

- **Conventions.** The new tests in the default suite (`internal/declare`, `internal/config`,
  `internal/cli`, `test/e2e`, `test/repo`) follow CONTRIBUTING: `dbtest.NewMigrated` for a migrated
  database, `t.Parallel()` first, port 0. The two-instance test is one parallel end-to-end test.
- **`test/instance`** is a small process harness (start a binary by path with an environment, wait
  for readiness, run a command-line call, stop, collect output for canaries) shared by `test/drill`
  and `test/upgrade`, because the end-to-end helpers are private to their package and are not
  touched while a pull request on them is open. It takes the binary's path, so it drives v0.2.0 too.
- **`test/upgrade`** (tag `upgrade`): `make test-upgrade` needs `make db-up` and
  `make previous-binary`; it fails, not skips, without the previous binary. CI job `upgrade`.
- **`test/drill`** (tag `drill`): `make test-drill` needs Docker only; `go test -tags drill
  -timeout 30m -parallel 4`. CI job `drill`, 30 minutes at most. `test/drill/compose.yaml` pins its
  images by digest (PostgreSQL 16 and OpenBao: the digests already in `test/db` and `test/transit`,
  copied; the later major: looked up by the command of plan A2) and gets its entry in
  `dependabot.yml` (T-R93, T-R94).
- **No backup file is an artefact.** The jobs upload nothing but the report (JSON, no secret); the
  dumps hold only sealed data and test canaries, and are still never uploaded (R-9).
- Both jobs are in `ci.yml`, so the release workflow, which calls it, runs them on the tag.
- T-1134 is one more test of `test/container`, in the `image` job; the job pulls the previous image
  by digest.

## 8. Decisions

Recorded in the ADR named in the last column.

| # | Decision | Reason | ADR |
|---|---|---|---|
| D-1 | The tested restore is an automated suite that executes the guide step by step, with the guide's step identifiers held equal to the suite's by a repository test | "A restore is tested" must stay true when the guide or the code changes; a manual drill is true once | 0076 |
| D-2 | The drill owns its database servers and key service, started per case; it is not in `make test` | It destroys servers; "an empty environment" is a server with no role and no database, which the shared test server is not | 0076 |
| D-3 | Both backup methods of the guide are drilled: a superuser's `pg_dump` in custom format restored whole with `pg_restore`, and a base backup with archived WAL recovered to a named point and to the end | The guide offers both; they fail differently (roles, ownership, row-level security, triggers on load; versions for the physical one) | 0076 |
| D-4 | The backup tools run in the database containers | The host needs nothing; the tool's version is the server's | 0076 |
| D-5 | A finding of the drill corrects the guide, and the drill executes the corrected text; findings are listed in the record | "As written" is a property to restore, not to fake | 0076 |
| D-6 | With a key service the drill uses OpenBao with integrated storage and its snapshot, and a key that is not exportable | The reference implementation of the epic; the snapshot is the service's own procedure and needs no exportable key; the transit backup endpoint does | 0076, amends 0073 |
| D-7 | The rule for backups with a key service: snapshot after every rotation and before the restart; never trim or raise `min_decryption_version` past a version that a kept database backup needs; keep the unseal shares with the snapshot | The two ways a kept backup becomes unrecoverable with a service, both drilled | 0076, amends 0068 and 0073 |
| D-8 | `recover-lost-kek.sql` also disables the event destinations whose signing secret was sealed under the lost version and drops an affected previous secret; the owner rotates the secret and enables | F-1: otherwise the relay signs with a secret it cannot open, or opens it with another key; disabling keeps the cursor and the configuration | 0076 |
| D-9 | A dated drill record states cases, proofs, findings, what cannot be recovered and what must be entered again, figures, and what was not drilled; a new record when the procedure or the vault's tables change and on a release candidate | The exit criterion; the pattern of the review record of 12.2 | 0076 |
| D-10 | The supported upgrade is from any patch of the previous minor; a patch release has no migration, no removed or renamed key, no breaking change; going back from a minor is a restore | OPS-13 made precise enough to test; forward-only migrations (ADR 0026) | 0075 |
| D-11 | Upgrades stay "stop, migrate, start"; the interval without service is measured and stated; rolling upgrades are decided with epic 8 | One instance today; a compatibility window is a rule for every later migration and has no user before replicas | 0075, amends 0026 |
| D-12 | The previous release in the tests is pinned in `test/upgrade/PREVIOUS` (version, commit, image digest); the traffic test runs a binary built from that commit, one container test runs the released image; the file moves at every release | The harness drives processes; the image is what operators run; both verified obtainable; a pin under a test cannot go stale silently | 0075 |
| D-13 | Every configuration key has a scope, `instance` or `installation`, in the key table and in the reference | "The same on every instance" was a sentence in one guide for one key; replicas need the list | 0077, history on 0019 |
| D-14 | The declarative document gains platform-level issuers with their tenants, tenant issuers, subject mappings, limit rules on a mapping, destinations, and the external reference in a platform section `tenants` that never creates a tenant | ADR 0066 point 3 and 6.4 O-12; the reference is set by the platform, so it is in the platform's document | 0077, amends 0066 and 0072 |
| D-15 | `apply` creates a destination only with `--secrets-out DIR` and writes the secret shown once to a file there; otherwise it skips and says so | No secret in a document or on the terminal, and no secret lost | 0077 |
| D-16 | A claim value equal to the tenant's ID is not exported, and an absent one means the target tenant's ID | IDs are not portable between installations | 0077 |
| D-17 | No default limit is created by `init` or by a migration; a default ceiling is a line of an example platform document | 5.1 D-10; the document is the place for policy | 0077 |
| D-18 | The settings of the epic 4 history are configuration already; their wiring in a chart and `init` as a job are 8.2 | They are keys of the table today | none (this table) |
| D-19 | The threat model is reviewed at every release, with a record file per release linked from the release page | 12.2 §11, ADR 0074 | 0075 |
| D-20 | No migration, no change of the API document, no permission, no error code, no new module; one new flag (`apply --secrets-out`); two new build tags and two CI jobs | Nothing designed needs more | 0075 to 0077 |

## 9. Tests

Levels: **U** unit; **D** needs the test database; **E** the built binary, end to end; **V** the
upgrade suite (tag `upgrade`, needs the previous binary); **X** the drill suite (tag `drill`, needs
Docker); **C** the container suite; **R** repository test. IDs continue from T-1118 and T-R97.

### New tests

| ID | Test | Level | Asserts | Requirement |
|---|---|---|---|---|
| T-1119 | `declare.TestParseNewSections` | U | The five sections and the `mapping` subject parse; an unknown member, `secret`, `signing_secret`, `id`, a duplicate natural key, a `scope` other than the two are invalid documents; error texts quote no value | CAT-10 |
| T-1120 | `declare.TestExportIssuersAndMappings` | U | Deterministic output; platform issuers with tenants by name; `claim_value` absent when it is the tenant's ID; mappings by issuer scope and name and service account name; limits on a mapping exported, no comment line | CAT-10 |
| T-1121 | `declare.TestPlanIssuers` | U | `create`, `update` per member, tenants added and `claim_value` changed, a missing tenant is a `conflict`, a listed tenant not in the document is `not in document`; nothing deleted | CAT-10 |
| T-1122 | `declare.TestPlanSubjectMappings` | U | `create`; a different service account or description is a `conflict`; a missing issuer or account is a `conflict`; planned after issuers and accounts and before limits | CAT-10 |
| T-1123 | `declare.TestPlanDestinations` | U | Without a secret sink an absent destination is `skipped` with the stated text; with one it is created and the secret goes to the sink only; `update` of the four members; no secret, cursor or delivery state in an export; no rotation ever | CAT-10, VLT-14 |
| T-1124 | `declare.TestPlanExternalReference` | U | `update` of an existing tenant; an absent tenant is a `conflict` and no tenant is created; an empty reference leaves it alone; export lists only tenants with a reference | CAT-10 |
| T-1125 | `cli.TestApplySecretsOut` | U | The directory must exist and be private; the file is `event-destination-<name>`, mode 0600, created exclusively; an existing file fails before any call; `--dry-run` writes nothing; the secret is in no output | VLT-14 |
| T-1126 | `e2e.TestExportApplyIssuersRoundTrip` | E | From instance 1 to a fresh instance 2 with the same tenant names: the exports are equal; a token of the fake IdP authenticates on 2 as the mapped account; a destination on 2 delivers an event that verifies with the secret of the file; a second `apply` plans nothing; the secret file's content is in no output or log | CAT-10, OPS-08 |
| T-1127 | `e2e.TestTwoInstancesSideBySide` | E | The table and the assertions of §5.10 point 4 | OPS-08 |
| T-1128 | `e2e.TestExamplePlatformDocument` | E | `examples/declarative/platform.yaml` applies to a fresh installation; a tenant with no rule is then limited to the default ceiling; a second `apply` plans nothing | OPS-08 |
| T-1129 | `upgrade.TestPreviousBinary` | V | The binary built from `PREVIOUS` reports that version and commit; its `migrate status` on an empty database needs version 16; this tree needs 17 | OPS-13 |
| T-1130 | `upgrade.TestUpgradeWithTraffic` | V | §5.9, steps 1 to 4 (**exit**) | OPS-13, OPS-12 |
| T-1131 | `upgrade.TestVersionsDoNotMix` | V | An old instance running through `migrate` turns not ready, naming the schema; what it answers then is as the upgrade guide says; restarted, it refuses with the "newer than this binary" message; the new binary refuses the schema of v0.2.0 with the "older" message | OPS-09 |
| T-1132 | `upgrade.TestGoingBackIsARestore` | V | The dump taken before `migrate`, restored into an empty database, is served by the previous binary and its credentials open; the upgraded database is unchanged | OPS-13, OPS-10 |
| T-1133 | `upgrade.TestUpgradeCanary` | V | Over T-1130: no credential, key-encryption key, password, session token or inference key in any output of either binary | VLT-14 |
| T-1134 | `container.TestUpgradeFromPreviousImage` | C | §5.9: the released image by digest, then the image of this tree; the call works; the previous image's digest is the one of `PREVIOUS` | OPS-13 |
| T-1135 | `upgrade.TestPreviousDocumentsApply` | V | Platform and tenant documents exported by the previous binary are valid for the new `apply` and plan nothing on the upgraded installation | OPS-13, CAT-10 |
| T-1136 | `drill.TestDump` | X | A dump by a role bound by row-level security fails and names it; a superuser's succeeds; the dump, the base backup and the WAL archive contain no canary credential, no signing secret and no byte sequence of the key file | OPS-10, VLT-14 |
| T-1137 | `drill.TestRestoreLogical` | X | Case L1 (**exit**): the guide's steps 4.1 to 4.7 on an empty server from the dump of B2 and its key; the seven assertions of §5.3; schema-only dumps equal; G-1 in both forms | OPS-10, AUD-04 |
| T-1138 | `drill.TestRestorePhysical` | X | Cases P1, P2: the base backup with WAL to the end of the archive and to the restore point of B1; the same assertions; roles and passwords come with it (G-3); the other major refuses | OPS-10 |
| T-1139 | `drill.TestAfterRestore` | X | §5.4, rows 5.a to 5.e | OPS-10 |
| T-1140 | `drill.TestKeyCasesFile` | X | Cases K1 to K4 of §5.5, one subtest each; row 2 executed in both readings (G-5); in K3 and K4 the tenants whose data keys were under the lost version get `revoked` credentials with the script's reason, enter a credential again and are served, and the other tenants (K3) never stopped | OPS-10, VLT-16 |
| T-1141 | `drill.TestKeyCasesService` | X | Cases S1 to S4 of §5.5 with OpenBao: S1 restores the service from its snapshot into an empty server; S3 trims; S4 in its three forms; `kek check` reports no weak setting and `allow_exportable_key` is unset; the recovery script takes a `transit-…` identifier | OPS-10, VLT-16, VLT-12 |
| T-1142 | `drill.TestRecoveryAndDestinations` | X | F-1, written first: without the change, what the relay does with a destination after the script (recorded); with it, the destination is disabled, its cursor kept, an unaffected destination untouched, and after `rotate-secret` and enable the kept events arrive signed with the new secret; the script run twice changes nothing more; an unknown version changes nothing | VLT-16, EVT-03 |
| T-1143 | `drill.TestEventsAfterRestore` | X | §5.4 row 5.f: redelivery from the backup's cursor with identifiers already seen; sequences of new events against those issued before X; what `event export` answers to a cursor past the restored head; all as the corrected guide states | EVT-03 |
| T-1144 | `drill.TestRestoreOnLaterMajor` | X | §5.3: the digests of the `jsonb` text of the audit tables are equal on both majors; the chains verify on the later one, or the second check is recorded as not run with its reason | AUD-04 |
| T-1145 | `drill.TestDrillCanary` | X | Over every case: no credential, signing secret, key file content, database password, unseal share or key-service token in any output of the binary, of the tools as captured, or in the report | VLT-14 |
| T-1146 | `drill.TestFiguresAndReport` | X | The figure case with the stated dataset; the report has every case of §5.5 and §5.3 with an outcome, every step with a duration, the versions, and nothing else | OPS-10 |
| T-1147 | `config.TestKeyScope` | U | Every key has a scope, one of the two; the keys named in §5.10 have the scope stated there | OPS-08 |
| T-R98 | `repo.TestProcessReadsOnlyItsConfiguration` | R | §5.10 point 2 | OPS-08 |
| T-R99 | `repo.TestConfigurationScopes` | R | The configuration reference shows each key's scope as the table has it, has the section "What makes an installation" and the table of the ambient environment; the limits guide links it for the time zone | OPS-08 |
| T-R100 | `repo.TestDrillFollowsTheGuide` | R | The step identifiers of the backup guide (§4, §5, §6 in both columns) equal the suite's `Step` constants; each command block of the guide under a step identifier is the command the suite runs for it | OPS-10 |
| T-R101 | `repo.TestDrillRecord` | R | The record exists, has the sections of §5.7, a row per case with a test that exists, the table of §5.6, and is linked from the backup guide; the guide no longer says the restore is untested; the requirements page marks OPS-10 and VLT-16 tested; no document says "sprint 12.3" of something still to come | OPS-10, VLT-16 |
| T-R102 | `repo.TestUpgradePolicy` | R | The upgrade guide exists with the points of §5.8; `PREVIOUS` has three well-formed lines and its version is the newest patch of the minor before the one in development; `Makefile` has `previous-binary`, `test-upgrade`, `test-drill`; `ci.yml` has the jobs `upgrade` and `drill` running them with no `continue-on-error`; "Releasing" names the two jobs, the record and the move of `PREVIOUS`; every release page from v0.3.0 on, the draft included, links a review record that exists | OPS-13 |
| T-R103 | `repo.TestDeclarativeDocuments` | R | The declarative guide's table has the new sections and the flag; the events guide and ADR 0066 and 0072 no longer defer them; the example document parses | CAT-10 |

### Changed tests of earlier sprints

| Test | Change |
|---|---|
| T-971 `e2e.TestExportApplyRoundTrip`, `declare` export and plan tests | the tenant of the round trip has an issuer, a mapping and a rule on it; no comment line for mapping rules |
| `repo` test on `recover-lost-kek.sql` (`test/repo/api_test.go`) and `vault` test that runs the script (`internal/vault/sealer_db_test.go`) | the statement on destinations |
| T-R80 `repo.TestLifecycleDocuments` | the backup guide's headings: "Drills" replaces "What sprint 12.3 adds"; step identifiers |
| `config.TestKeysTable` | unchanged in rule; the table's entries carry a scope |
| T-R93, T-R94 (pins and dependabot) | cover `test/drill/compose.yaml` |
| T-R86 `repo.TestTransitDocuments` | the external key guide's backup section names the snapshot rule |
| `repo.TestLatestReleaseIsPinned` | unchanged; T-R102 is beside it |
| The tests that count the commands or flags of the usage text | one flag more on `apply` |

### Requirement coverage

| Requirement | Tests |
|---|---|
| OPS-10 (tested) | T-1136 to T-1146, T-1132, T-R100, T-R101 |
| VLT-16 (tested) | T-1140, T-1141, T-1142, T-R101 |
| OPS-13 | T-1129 to T-1135, T-R102 |
| OPS-08 | T-1126, T-1127, T-1128, T-1147, T-R98, T-R99 |
| CAT-10 (extended) | T-1119 to T-1126, T-1135, T-R103 |
| OPS-09, OPS-12 (tests) | T-1131, T-1130 |
| AUD-04 (touched) | T-1130, T-1137, T-1144 |
| VLT-14 (touched) | T-1125, T-1126, T-1133, T-1136, T-1145 |
| EVT-03 (touched) | T-1142, T-1143 |

## 10. Definition of done for this sprint

- [ ] Every test of §9 exists under its name and passes; T-1119 to T-1128, T-1142, T-1147 and
      T-R98 to T-R103 failed before their implementation; the changed tests are changed as listed.
- [ ] **Exit:** T-1137 and T-1138 pass: a restore into an empty server serves calls, and the
      provider receives each tenant's restored credential.
- [ ] **Exit:** the record `restore-drill-2026-11.md` exists with the cases, the findings, the
      table of what cannot be recovered and what must be entered again, and the figures of a run
      whose commit it names; T-R101 passes.
- [ ] **Exit:** T-1130 passes against a binary built from the commit of the tag v0.2.0, and T-1134
      against the released image.
- [ ] The eight key cases (K1 to K4, S1 to S4) have the outcome of §5.5; the key of the service is
      not exportable in every one.
- [ ] Each finding G-1 to G-7 and F-1 has an outcome in the record; anything else the first run
      found is there too, with a decision, and nothing was changed in the guide or the script
      beyond what a finding names.
- [ ] The backup guide is the text the suite executes (T-R100); `upgrade.md` is the text T-1130
      executes.
- [ ] Two instances from two configurations pass T-1127; every key has a scope.
- [ ] No secret in any test, fixture, log, report, record or CI artefact; the canary tests pass;
      `make secrets` passes; the CI jobs upload no backup file.
- [ ] No migration, no change of `api/openapi/admin.v1.yaml`, no new module; `make check` passes;
      the jobs `upgrade`, `drill`, `image` and `transit` pass.
- [ ] An installation that uses none of the new document sections, and never loses a key, behaves
      as before.
- [ ] ADR 0075, 0076 and 0077 and the history lines; the backup, upgrade, vault, external key,
      declarative, events, deletion, configuration, database and limits guides; the failure
      reference; the requirements page (OPS-10, VLT-16 "tested"; OPS-13); the roadmap; the epic
      file; CONTRIBUTING ("Releasing", the suites); the draft release notes of v0.3.0 are current.
- [ ] The maintainer has decided the open points of §12, and done the walk of O-7 or declined it in
      the record.

## 11. Consequences for later sprints

- **Every later sprint.** A migration is tested from the previous minor by the `upgrade` job
  without being asked; a change to a table the vault, the chains or the event log use, or to a step
  of the backup guide, fails T-R100 or the drill until the guide, the suite and the record agree. A
  new configuration key needs a scope (T-1147). A new resource that distinguishes installations is
  either in the declarative document or in its list of what never is.
- **8.1 (replicas).** The `installation` scope is the list of settings to compare between
  instances at start; the upgrade policy's point 5 (no two versions at once) is reopened there with
  a compatibility rule for migrations, and the traffic test becomes the rolling one. The drill gains
  the counter store (what a restore means for shared counters).
- **8.2 (Kubernetes).** The chart sets `auth.public_origin` from the ingress host, mounts the
  client secret and the key service's files, exposes `support.*`, runs `migrate` and `init` as
  jobs, may apply the example platform document, and upgrades by the procedure of the upgrade guide
  (stop, migrate, start) until 8.1 allows better. `apply --secrets-out` writes to a mounted
  directory.
- **10.1 (figures, failure injection).** The figure case takes its dataset from a variable: the
  scale run is the same test with larger numbers. Failures during a restore (the key service away,
  the archive incomplete) use the drill's harness.
- **Epic 10 (release candidate).** `make test-drill` and `make test-upgrade` on the candidate, a new
  dated record, and the maintainer's walk if O-7 was deferred. The assessment of 10.2 reads the
  record's "what was not drilled".
- **9.x (providers).** Nothing: the drill's provider is the fake one; a family needs no case.
- **Release v0.3.0.** The notes say: the restore is tested and where the record is; the upgrade
  from v0.2.x, its procedure, the measured interval, the single migration crossed; the recovery
  script disables destinations; the five new sections of the document and `--secrets-out`; the
  scope of keys. After the release, `PREVIOUS` moves to v0.3.0.
- **The next review of the threat model** (the record for v0.3.0) may name the drill's tests as
  proofs for the rows on backups and on the key service's token.

## 12. Risks and open points

### What was verified while designing

At commit `eed15464357ba06b0a54b7159690c911fb3107ae`, on 2026-11-25:

| Fact | How |
|---|---|
| The remote has the tags `v0.1.0`, `v0.1.1`, `v0.2.0`; `v0.2.0` is annotated and points to commit `0d3fb5dd2fdb5c82b1e28efb04b30984deb30c96`, the head of `release/0.2` and an ancestor of this branch. The local clone has **no tags** | `git ls-remote --tags --heads origin`, `git tag`, `git merge-base --is-ancestor` |
| That commit can be fetched without a tag, extracted and built with the installed toolchain in about half a minute; `.go-version` (1.27.4) and the `go` line are the same there as here | `git fetch origin <commit>`; `git archive <commit> \| tar -x -C <dir>`; `CGO_ENABLED=0 go build ./cmd/pandokos`; the binary printed its usage with `export`, `apply`, `platform-issuer`, `subject-mapping` |
| The released image exists, for `linux/amd64` and `linux/arm64`; its index digest was `sha256:ab33bc5853077cce504e40177d4c7da686362d874c5bef4b46d4089b8bbc3e58` | `docker buildx imagetools inspect ghcr.io/pandokos/pandokos:0.2.0` (the implementer runs it again, plan A3) |
| Between v0.2.0 and this branch exactly one migration was added, `0017_events.sql`; nothing under `deploy/postgres` changed | `git diff --stat <commit> HEAD -- internal/store/migrations/sql deploy/postgres` |
| The test PostgreSQL image (`postgres:16-alpine`, pinned) has `pg_dump`, `pg_restore`, `pg_basebackup`, `pg_ctl`, `initdb`, version 16.15; Docker is available where the suite ran | `docker exec <container> sh -c 'pg_dump --version; which …'` |
| OpenBao 2.7.3 (the pinned image) with integrated storage: a snapshot taken through the API restores into a second, empty, initialized server with `snapshot-force`; after unsealing with the **first** server's share, the first server's token decrypts a ciphertext made before the snapshot; a key created with defaults is not exportable; after `min_decryption_version: 2` and `trim`, a ciphertext of version 1 is refused ("disallowed by policy (too old)") | run by hand against a container: `PUT sys/init`, `PUT sys/unseal`, `GET sys/storage/raft/snapshot`, `POST sys/storage/raft/snapshot-force`, `POST transit/keys/<k>/trim` |
| `recover-lost-kek.sql` was last changed in sprint 6.3 and has three statements, none on `event_destination`; `0017_events.sql` gives `event_destination` the columns `secret_key_version` and `previous_secret_key_version` with no foreign key to `tenant_data_key`; a tenant's data key versions are unique per tenant and start at 1 | `git log -- deploy/postgres/recover-lost-kek.sql`; reading the two files and `0003` |
| The backup guide's step 6 uses `--expect-head` with a kept head; the audit guide says the option is the hash of the last row read and gives another procedure for an older head | reading `backup-and-restore.md` §4 and `audit.md` §4, §7 |
| The configuration reference lists 144 keys (rows of its "Keys" table); `config.TestKeysTable` compares the key table with the leaves of the structure; outside tests the environment is read in `cmd/pandokos/main.go` and `internal/store/conn.go` only; `PANDOKOS_TEST_REFRESH_DELAY` is the one test hook | `grep -c`, reading `internal/config/config_test.go`, `grep -rn 'os\.\(Getenv\|LookupEnv\|Environ\|Hostname\|UserHomeDir\|Getwd\)('` |
| The issuer, mapping, destination and tenant-update operations exist with the members used in §5.11; a mapping has no update operation; creating a destination returns `secret` once; `external_reference` is a member of the tenant's update body | reading `api/openapi/admin.v1.yaml` |
| The documents that still defer something to sprint 12.3 | `grep -rn '12\.3' docs` : `backup-and-restore.md`, `declarative.md`, `events.md`, `tenant-deletion.md`, `failure-behaviour.md`, ADR 0066, 0072, 0073, 0074, the requirements page, the roadmap, the epic files of 6, 10 and 12, `v0.2.0.md` (a released page: not edited) |
| Nothing else is assigned to 12.3 or epic 12 in the histories | the same search over `docs/history/*/README.md` and the closed designs; the lines are quoted in §5.12 |
| The highest identifiers in use are T-1118 and T-R97; the next ADR number is 0075 | `grep -rhoE` over `planning` and `docs/history`; `docs/adr` |

Not verified: any Go code of this design; the exact options of `pg_dump`, `pg_restore` and
`pg_basebackup` and the recovery settings (plan C5 settles them by running them); G-5 and G-7 and
what the relay does after the recovery script today (the tests observe first); that the suite's
schema and the binary run on a PostgreSQL major later than 16 (O-1; the pinned digest of that image
is looked up, not known); a snapshot restore on HashiCorp Vault; the time the two jobs take on a
runner; whether the GitHub runner can pull the previous image without credentials (it is public
from where the design was written); how v0.2.0's listeners answer on a schema migrated under them
(T-1131 observes, the guide then says).

### Risks

| # | Risk | Mitigation |
|---|---|---|
| R-1 | The drill is written by the people who wrote the guide and executes their understanding of it | Step identifiers and commands are bound to the guide's text (T-R100); the record lists what was not drilled; O-7 asks for one walk by a person from the guide alone; epic 10 repeats on the candidate |
| R-2 | The drill's servers are not production: one node, small data, no pooler, a local disk | Stated in the record; the figures are labelled indicative; 10.1 owns scale |
| R-3 | A destructive suite is run against something that matters | It only ever talks to Compose projects it created, named `pandokos-drill-<random>`, on ports it read back; it takes no address from the environment; the recovery script is run only in those databases |
| R-4 | The traffic test is sensitive to timing and becomes flaky | It asserts categories of outcome and a window defined by events of the test (the signal, the readiness), not durations; the only bound is a generous ceiling; closed-loop clients |
| R-5 | The previous release stops being buildable (a toolchain no longer offered) or the image is removed | Two independent sources; the pin's form and currency are under T-R102; a failure of `make previous-binary` fails the job loudly rather than skipping |
| R-6 | The recovery script change disables a destination that was fine | It selects by the data key versions of the lost key-encryption key version only; T-1142 has an unaffected destination and an unknown version; the script stays one transaction |
| R-7 | `--secrets-out` leaves secrets on disk | The directory must be private, files 0600 and created exclusively; the guide says to move them to the secret store and delete them; nothing is written without the flag |
| R-8 | The later major's image or its data directory layout differs and the leg fails for reasons that are not ours | The leg's first check needs only SQL; a failure of the second is recorded as a finding, not papered over (O-1) |
| R-9 | A dump or a snapshot leaks through CI | They hold sealed data and test canaries only, live in the test's temporary directory, and no job uploads them (T-R102 reads the workflow for upload steps in the two jobs) |
| R-10 | The two new jobs make every pull request slower | Their own jobs, in parallel with the others; cases parallel; if `drill` exceeds 15 minutes on a runner, O-6 |
| R-11 | Classifying 144 keys by hand puts one in the wrong scope | One rule (§5.10); the keys this design names are tested (T-1147); the scope is documentation until 8.1 enforces it, which will test every one |
| R-12 | The guide's corrections change a procedure operators already follow | Each is a finding with its reason in the record and the release notes; the recovery script's new statement only adds a safe state |
| R-13 | A snapshot restore in a real, highly available key service differs from one node | The guide points to the service's own documentation for clusters; the drill proves the dependency (versions, unseal shares, order), not the service's operations |

### Open points (raised to the maintainer; none blocks the start, O-1 and O-7 block the close)

| # | Question | Recommendation |
|---|---|---|
| O-1 | Which later PostgreSQL major does the R-6 leg restore into? | **18** (the update proposed for the pinned images targets it); if the binary does not run there, record the finding, keep the SQL check, and open an issue for support of that major rather than fixing it in this sprint |
| O-2 | A destination created by `apply`: `--secrets-out DIR` (D-15), or create it disabled and let the owner run `rotate-secret`? | `--secrets-out`: one step, no window with a secret nobody holds, and the document stays free of secrets |
| O-3 | After a restore, sequences of the event log are issued again and a destination's cursor goes back (G-7). Build something (a script that moves the sequences past a value kept outside), or state it? | State it in this sprint and let T-1143 record exactly what a receiver and a puller see; decide on a script from that evidence, as an issue for 10.1 |
| O-4 | The previous release in the tests: a binary built from the tag's commit for the traffic test and the released image for one container test (D-12)? | Yes, both; the alternative (the image only) needs the harness to drive containers for the traffic test |
| O-5 | Upgrading across more than one minor: supported only through each minor in turn (D-10)? | Yes; it costs an operator one more stop per minor and keeps the tested path the supported one |
| O-6 | Do `drill` and `upgrade` run on every pull request? | Yes while they stay under 15 minutes each; otherwise `drill` moves to the nightly workflow and stays in the release's run, `upgrade` stays on every pull request |
| O-7 | A walk of the corrected guide by the maintainer, by hand, on the Compose reference (one logical restore, one lost key), recorded with its date | Do it once before v0.3.0; it is the only check not made by the authors' own automation. If deferred, it is epic 10's, and the record says so |
| O-8 | The recovery script disables affected destinations (D-8), or deletes them? | Disable: the URL, the types and the cursor are the owner's configuration and nothing is lost by keeping them |
| O-9 | HashiCorp Vault is not drilled | Accept; the conformance suite covers its transit API, and its snapshot is its own documented procedure. The record says "not drilled" |
| O-10 | The example platform document ships a default ceiling of 256 concurrent requests per tenant (the figure of 5.1 O-4) | Ship it as an example only; nothing applies it by default |
| O-11 | The drill record lives in `docs/operations/` beside the guide (the review record lives in `docs/architecture/` beside the model) | Yes: each record beside the document it tests |

### Maintainer decisions on the open points (2026-11-25)

| # | Decision |
|---|---|
| O-1 | Confirmed: PostgreSQL 18 for the leg of R-6; if the binary does not run there, the SQL check stays and an issue is opened |
| O-2 | Confirmed: `apply --secrets-out DIR` is the only way `apply` creates a destination |
| O-3 | Confirmed: the reissue of event sequences after a restore is stated now; a script is decided from the test's evidence (issue for 10.1) |
| O-4 | Confirmed: the previous release as a binary built from its commit (traffic test) and as its image by digest (one container test) |
| O-5 | Confirmed: an upgrade is supported through each minor in turn only |
| O-6 | Confirmed: `drill` and `upgrade` run on every pull request while each stays under 15 minutes; otherwise `drill` moves to the nightly run and stays in the release run |
| O-7 | Pending the maintainer: one manual walk of the corrected guide on the Compose reference before v0.3.0, otherwise in epic 10 |
| O-8 | Confirmed: the recovery script disables the affected destinations, it does not delete them |
| O-9 | Accepted: HashiCorp Vault is not drilled; the record says so |
| O-10 | Confirmed: the default ceiling of 256 concurrent requests ships as an example line only |
| O-11 | Confirmed: the drill record lives in `docs/operations/` beside the guide |
