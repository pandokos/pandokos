# Upgrading

How a Pandokos installation moves to a newer version, what is promised, and what is checked before every
release (OPS-13). Decisions: [ADR 0075](../adr/0075-upgrade-policy-and-the-tested-upgrade.md), with
[ADR 0026](../adr/0026-schema-migrations.md).

## 1. Versions

Pandokos follows semantic versioning. Before 1.0 a minor version may break compatibility, and only the latest
minor is supported ([SECURITY.md](../../SECURITY.md)). Every release page has "Breaking changes", each with what
to do, and "Upgrade notes": read the page of the version you go to before you start.

## 2. The supported upgrade

The supported upgrade is from **any patch of the previous minor** to the new minor: v0.2.x to v0.3.y. It is
tested on every pull request, from the newest patch of the previous minor, whose version, commit and image are
pinned in `test/upgrade/PREVIOUS`: a binary built from that commit is upgraded under traffic, and the released
image runs once through the same path.

From an older minor, upgrade through each minor in turn, running the newest patch of each. The migrations would
apply in one run, but the breaking changes and the start checks of the versions in between are not tested
together, so it is not supported.

## 3. A patch release

A patch release (v0.3.0 to v0.3.1) contains no migration, removes or renames no configuration key, changes no
default in a way that changes behaviour, and has no breaking change. It is installed by replacing the binary
and undone by putting the previous patch back. Every patch of a minor therefore has the same schema, which is
what makes "any patch" in section 2 true.

## 4. The procedure of a minor upgrade

Upgrades are **stop, migrate, start**: there is no window in which two versions run together. The installation
does not serve between the stop and the start. On the machine of the test (one instance, a local database, small
data) the interval from the signal to the old instance to the readiness of the new one, with the migration of
v0.2.0 to v0.3.0 in between, was **1.0 s** (stop 0.6 s, `migrate` 0.1 s, start 0.4 s): labelled indicative, it
grows with the number of migrations and with the size of what they rewrite.

1. **Read** the release page: breaking changes, upgrade notes.
2. **Back up** the database, the key file of every `active` or `retiring` version (or, with a key service, its
   snapshot) and the configuration, and keep the heads of the audit chains with their `seq`
   ([backup and restore guide](backup-and-restore.md)). The backup is also how you go back (section 6).
3. **Check the configuration with the new binary**: `pandokos config check` run with the **new** binary against
   the **current** configuration must exit 0. A removed or renamed key is named here, before anything stops.
4. **Stop every instance.** They drain: streams in flight complete and the process exits 0.
5. **Migrate** with the new binary: `pandokos migrate` prints `applied <NNNN>_<name>` for each migration and
   `schema is at version <N>`. With the container image:

   ```sh
   docker run --rm --read-only --cap-drop ALL --security-opt no-new-privileges \
     -v "$PWD/secrets:/run/secrets:ro" \
     -e PANDOKOS_DATABASE_ADDRESS=db.internal:5432 \
     -e PANDOKOS_DATABASE_OWNER_USER=acme_owner \
     -e PANDOKOS_DATABASE_OWNER_PASSWORD_FILE=/run/secrets/db_owner \
     ghcr.io/pandokos/pandokos:<version> migrate
   ```

6. **Start** the new version with the same configuration.
7. **Check:** `pandokos migrate status` exits 0; `/readyz` answers 200 on both listeners; `vault ready` in the log
   names the same `kek_version` with `kek_activation: unchanged` (an upgrade re-wraps no data key); a call with a
   tenant's key reaches the provider; both audit chains verify, and the export up to a kept `seq` verifies
   against the head you kept.

A Compose installation does the same with `docker compose stop`, a one-off `migrate` container and
`docker compose up -d` ([container guide](container.md)).

## 5. If you forget to stop an instance

An instance of the old version that is still running when `migrate` runs does not break, but it cannot keep up:
its `/readyz` turns `503` with the check `not_ready` and a log line that says the schema changed, `/healthz` stays
`200`, and calls that its caches can still answer are answered (it keeps serving what it already holds, with the
old code, against a schema it does not know). Stop it. Started again it refuses with `the database schema is
newer than this binary`. A new binary started on a schema that is not yet migrated refuses with `run "pandokos
migrate"`. Neither version ever serves a schema it was not built for.

Rolling upgrades, with two versions serving at once, need a compatibility rule for migrations and more than one
instance; they are decided with the work on replicas.

## 6. Going back

There are no down migrations: migrations are forward-only. Going back from a minor is a **restore** of the backup
taken before `migrate`, run by the old binary, into an empty database ([backup and restore
guide](backup-and-restore.md)). What was written after the upgrade is lost, as in any restore. The upgrade suite
runs this path: a dump taken before `migrate`, restored into an empty database, is served by the old release with
every credential opening, while the upgraded database is untouched. Going back **within** a minor (a patch) is
putting the previous binary back.

## 7. Formats that leave the installation

These are versioned by themselves and grow by addition within their version: the declarative document
(`pandokos: v1`), the tenant export (`v1`), the events (`.v1` types), the audit encoding (`pandokos-audit-v1`)
and the administration API (`/v1`). A document written by the previous minor is accepted by the new one.

## 8. PostgreSQL

The minimum is PostgreSQL 16. A move to a later major is the database's own procedure; the audit chains stay
verifiable across it (the drill restored a dump into PostgreSQL 18 and verified both), and the [drill
record](restore-drill-2026-11.md) names the majors it ran on.

## 9. What is checked at every release

- The `upgrade` and `drill` jobs are green on the commit of the release.
- The threat model was reviewed and a record file exists for the release
  (`docs/architecture/threat-model-review-YYYY-MM.md`, short when nothing changed).
- The release page links that record and the drill record in force, and says when the drill record is the
  previous one.
- After a minor is released, `test/upgrade/PREVIOUS` moves to it in the first pull request on `main`, and after
  each of its patch releases to that patch ([CONTRIBUTING](../../CONTRIBUTING.md#releasing)).
