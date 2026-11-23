# Database guide

Pandokos needs one thing besides itself: PostgreSQL. This guide says what to prepare, how the
schema is created and upgraded, what `pandokos serve` checks when it starts, and how to run
behind a connection pooler.

## 1. Requirements

- PostgreSQL 16 or later. One database.
- Nothing else: no extension, no second service.
- Recommended: one PostgreSQL cluster per Pandokos installation. The four privilege roles below
  are cluster-wide, so two installations in one cluster share their names. If that cannot be
  avoided, give each installation separate login users and keep `CONNECT` revoked from `PUBLIC`
  on each database, as the statements below do.

## 2. Roles and users

Pandokos separates what may change the schema from what runs requests. There are four
**privilege roles** with fixed names. They cannot log in and have no password:

| Privilege role | Used by | What it may do |
|---|---|---|
| `pandokos_owner` | `pandokos migrate` only | Owns every object of schema `pandokos`; creates and changes the schema |
| `pandokos_gateway` | the gateway | Reads; the grants are listed by table in the schema lint |
| `pandokos_admin` | the admin API | Reads and writes tenants and service accounts, within its column grants |
| `pandokos_worker` | background jobs | Reads tenants, in platform scope |

**Database users** are login roles with a password, created by you. Each process role connects as
a user that is a member of exactly one privilege role, and the three runtime users must be
different users:

| Privilege role | Its user is configured in | Membership needed |
|---|---|---|
| `pandokos_owner` | `database.owner.user` | member with the `SET` option (the default of `GRANT`); `migrate` switches to the role for each transaction |
| `pandokos_gateway` | `database.gateway.user` | member with `INHERIT` (the default) |
| `pandokos_admin` | `database.admin.user` | same |
| `pandokos_worker` | `database.worker.user` | same |

The privilege roles are created once per PostgreSQL cluster by this script, as a superuser or as a
role with `CREATEROLE`. It is idempotent (`deploy/postgres/roles.sql` in the repository):

```sql
-- Pandokos privilege roles. Run once per PostgreSQL cluster, as a superuser or as a role
-- with CREATEROLE. Idempotent. The roles cannot log in and have no password: database users
-- are granted membership in them (docs/operations/database.md).
DO $$
DECLARE
    r text;
BEGIN
    FOREACH r IN ARRAY ARRAY['pandokos_owner', 'pandokos_gateway', 'pandokos_admin', 'pandokos_worker'] LOOP
        BEGIN
            EXECUTE format('CREATE ROLE %I NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS', r);
        EXCEPTION WHEN duplicate_object OR unique_violation THEN
            NULL;
        END;
    END LOOP;
END
$$;
```

Create the database, owned by the owner role, reachable only by the runtime roles:

```sql
CREATE DATABASE pandokos OWNER pandokos_owner;
REVOKE ALL ON DATABASE pandokos FROM PUBLIC;
GRANT CONNECT ON DATABASE pandokos TO pandokos_gateway, pandokos_admin, pandokos_worker;
```

### Recommended: one login user per role

Four users with their own passwords let you rotate a password or a user without touching the
schema. In `psql`, `\password` prompts for the password, so it never appears in a script or in the
shell history:

```sql
CREATE ROLE acme_owner   LOGIN INHERIT;
CREATE ROLE acme_gateway LOGIN INHERIT;
CREATE ROLE acme_admin   LOGIN INHERIT;
CREATE ROLE acme_worker  LOGIN INHERIT;
\password acme_owner
\password acme_gateway
\password acme_admin
\password acme_worker
GRANT pandokos_owner   TO acme_owner;
GRANT pandokos_gateway TO acme_gateway;
GRANT pandokos_admin   TO acme_admin;
GRANT pandokos_worker  TO acme_worker;
```

Then name them in the configuration: `database.owner.user: acme_owner`,
`database.gateway.user: acme_gateway`, `database.admin.user: acme_admin`,
`database.worker.user: acme_worker`, each with its `password_file`.

### Shortest: the privilege roles are the users

The default user names equal the role names, so the shortest setup gives the four privilege roles
a login and a password and creates no other user. The same checks pass:

```sql
ALTER ROLE pandokos_owner   LOGIN;
ALTER ROLE pandokos_gateway LOGIN;
ALTER ROLE pandokos_admin   LOGIN;
ALTER ROLE pandokos_worker  LOGIN;
\password pandokos_owner
\password pandokos_gateway
\password pandokos_admin
\password pandokos_worker
```

Do not make any of these roles a superuser, give it `BYPASSRLS`, or make one a member of another:
`pandokos migrate` (privilege roles that are superusers or have `BYPASSRLS`) and `pandokos serve`
(database users that are superusers, have `BYPASSRLS`, or belong to the wrong roles) refuse at run
time. The roles must also not have `CREATEROLE`, `CREATEDB` or `REPLICATION`; `roles.sql` creates
them without those attributes, and the schema lint of the test suite checks it, but nothing checks
it at run time.

**Exact grants after migration 0001.** A privilege that is not in this table is not granted.

| Object | `pandokos_owner` | `pandokos_gateway` | `pandokos_admin` | `pandokos_worker` | `PUBLIC` |
|---|---|---|---|---|---|
| schema `pandokos` | owner | `USAGE` | `USAGE` | `USAGE` | none |
| `schema_migration` | owner | `SELECT` | `SELECT` | `SELECT` | none |
| `tenant` | owner, subject to the policies (it has none: it sees no row) | `SELECT` | `SELECT`, `INSERT (name, display_name)`, `UPDATE (name, display_name, state)` | `SELECT` | none |
| `service_account` | owner, subject to the policy | `SELECT` | `SELECT`, `INSERT (tenant_id, name, description, state, valid_from, valid_until)`, `UPDATE (name, description, state, valid_from, valid_until)`, `DELETE` | none | none |
| `uuid_v7()`, `current_tenant_id()`, `platform_scope()` | owner | `EXECUTE` | `EXECUTE` | `EXECUTE` | none |
| `tenant_guard()`, `row_guard()` | owner | none | none | none | none |

No runtime role holds `TRUNCATE`, `REFERENCES` or `TRIGGER` on anything, `CREATE` on the schema or
on the database, or membership in another Pandokos role.

## 3. Configuration

```yaml
database:
  address: db.internal:5432        # PostgreSQL, or the pooler in front of it
  name: pandokos
  tls:
    mode: verify-full              # or: disable
    ca_file: /run/secrets/database_ca.pem
  connect_timeout: 5s
  gateway:
    user: acme_gateway
    password_file: /run/secrets/db_gateway
    max_connections: 10
  admin:
    user: acme_admin
    password_file: /run/secrets/db_admin
    max_connections: 10
  worker:
    user: acme_worker
    password_file: /run/secrets/db_worker
    max_connections: 5
  owner:
    user: acme_owner
    password_file: /run/secrets/db_owner
```

All keys are in the [configuration reference](configuration.md). Which command reads which
password:

| Command | Passwords it reads |
|---|---|
| `pandokos serve` | gateway, admin and worker. Never the owner's: the process that serves requests cannot change the schema |
| `pandokos migrate`, `pandokos migrate status` | the owner's only |

TLS towards the database is on by default and verified: the certificate chain and the host name
(`verify-full`). `database.tls.ca_file` names the authorities to trust, else the system roots are
used. There is no mode that encrypts without verifying. `disable` is for a database on the same
host or on a private container network; it warns when the host is not a loopback address.

## 4. Creating and upgrading the schema

The schema is created by SQL files embedded in the binary, applied by an explicit command.

```
pandokos migrate [--config FILE]
pandokos migrate status [--config FILE]
```

`pandokos migrate` connects as the owner user and applies every pending migration in order, each in
its own transaction. It prints `applied <NNNN>_<name>` for each migration it applied, then
`schema is at version <N>`; when the schema is current it prints only the last line. It exits 0 on
success and 1 on a configuration error, a connection failure, a failed preflight (the server is
older than PostgreSQL 16, a privilege role is missing or privileged, or the user cannot act as
`pandokos_owner`), a failed migration or a ledger that does not match the binary.

`pandokos migrate status` changes nothing. It prints one line per migration, `<NNNN>_<name>  applied
<time>` or `<NNNN>_<name>  pending`, then `schema is at version <N>; this binary needs version
<M>`. It exits 0 when the schema matches the binary and 1 otherwise.

Rules to know:

- Upgrades are **stop, migrate, start**. A binary refuses a schema that is older or newer than the
  one it was built for, so there is no window in which two versions run together.
- Migrations are forward-only. There are no down migrations: a mistake is corrected by a new
  release, or by a restore. A migration that has been released is never edited, and a ledger row
  that differs from the binary stops both `migrate` and `serve`.
- Every object is owned by `pandokos_owner`, whichever user runs `migrate`.
- Concurrent `migrate` runs are safe: they wait for each other.
- The owner's password is needed only here. Prefer a direct connection to PostgreSQL for
  `migrate`; it also works through a pooler.

With the container image:

```
docker run --rm --read-only --cap-drop ALL --security-opt no-new-privileges \
  -v "$PWD/secrets:/run/secrets:ro" \
  -e PANDOKOS_DATABASE_ADDRESS=db.internal:5432 \
  -e PANDOKOS_DATABASE_OWNER_USER=acme_owner \
  -e PANDOKOS_DATABASE_OWNER_PASSWORD_FILE=/run/secrets/db_owner \
  ghcr.io/pandokos/pandokos:<version> migrate
```

## 5. What `serve` checks at start

Before it listens, `pandokos serve` opens the three pools and checks each database user and the
schema. The first failure is logged as `startup failed`, with the text below in `error`, and the
process exits with code 1. There is no retry: let your orchestrator restart it. Every message is
prefixed `database (<role>): `, where `<role>` is `gateway`, `admin` or `worker`.

| Message | Remedy |
|---|---|
| `cannot connect: <driver error>` | Check the address, the user, the password file and `database.tls.*`. The driver's text names the user, the database and the address, never the password |
| `user "<u>" is a superuser; a runtime role must not be` | Use a plain login role |
| `user "<u>" has BYPASSRLS; a runtime role must not` | `ALTER ROLE <u> NOBYPASSRLS` |
| `user "<u>" is not a member of pandokos_<role>` | `GRANT pandokos_<role> TO <u>` |
| `user "<u>" is a member of pandokos_owner; a runtime role must not own the schema` | `REVOKE pandokos_owner FROM <u>` |
| `user "<u>" is also a member of pandokos_<other>; each role needs its own database user` | Give each process role its own user |
| `user "<u>" owns objects in schema pandokos; a runtime role must own nothing` | Objects must belong to `pandokos_owner`; run `migrate` as the owner |
| `the database has no Pandokos schema; run "pandokos migrate"` | Run `pandokos migrate` |
| `the database schema is older than this binary: schema version <k>, this binary needs version <n>; run "pandokos migrate"` | Run `pandokos migrate` |
| `the database schema is newer than this binary: schema version <k>, this binary knows version <n>; upgrade Pandokos` | Run the binary that matches the schema |
| `a migration recorded in the database differs from this binary: <NNNN>_<name>` | The database was migrated by a different build; restore, or run the matching release |
| `the migration ledger is inconsistent: <detail>` | The ledger table was edited; restore it |
| `vault: the database holds data keys wrapped by key-encryption key version <v> which this configuration does not provide; see docs/operations/vault.md` | The key file is not the one that wrapped the data keys; mount the right file, or follow the loss procedure in the [vault guide](vault.md) |

**Readiness.** `/readyz` has one check per role (`database-gateway`, `database-admin`,
`database-worker`). Each reads the migration ledger and fails when the database does not answer
or when the schema no longer matches the binary. An instance whose database goes away, or is
migrated under it, turns not-ready and recovers on its own when the database is back and matching.
`/healthz` does not depend on the database.

## 6. Connection poolers

Pandokos keeps **no session state** on a database connection, so it behaves the same with and
without a transaction-mode pooler such as PgBouncer. There is no configuration switch.

| Session feature | What Pandokos does |
|---|---|
| Tenant and scope settings | Set inside the transaction and gone at commit or rollback; never at session level |
| Prepared statements | None: parameters travel in the extended protocol with the unnamed statement, in one round trip |
| Role switching and lock timeout in `migrate` | `SET LOCAL`, inside the migration's transaction |
| Advisory locks | Transaction-level only |
| `search_path` | Not used: every name is schema-qualified |
| Connection start parameters | Only `application_name` |
| Statements outside a transaction | Only in the start-up check and readiness: single read-only statements on global objects |
| Temporary tables, cursors `WITH HOLD`, session advisory locks | Not used; forbidden by this design |
| `LISTEN` / `NOTIFY` | One dedicated connection of the gateway user, outside the pool, to `database.direct_address` when set (else `database.address`): the principal cache listens for revocations. It proves itself with probes sent from a second connection; behind a transaction pooler without a direct address the probe fails, the caches are emptied (the safe side: a listener that cannot prove itself is not trusted, and the next connection empties them anyway) and the cache lifetime is the bound (`cache invalidation unavailable`). When the connection is lost the caches are **not** emptied: calls continue from them while the database is stopped, and they are emptied when the listener connects again, because notifications may have been missed. The bound on a revocation or a disable stays the cache lifetimes (`cache.principal_ttl` and `cache.credential_ttl`, at most 60 s; `cache.catalog_ttl`) |

What you must provide when a pooler is in front of PostgreSQL:

1. **Transaction or session pooling.** Statement pooling is not supported: it would split a
   transaction.
2. **Each of the three users authenticates as itself.** A shared server user would collapse the
   roles. With PgBouncer, `auth_query` (or a user list) with `auth_type = scram-sha-256` does it.
3. **TLS** between Pandokos and the pooler follows `database.tls.*`.

A minimal PgBouncer excerpt:

```ini
[databases]
pandokos = host=db.internal port=5432 dbname=pandokos

[pgbouncer]
pool_mode = transaction
auth_type = scram-sha-256
auth_user = pgbouncer_auth
auth_query = SELECT usename, passwd FROM pg_catalog.pg_shadow WHERE usename = $1
```

Point `database.address` at the pooler. `pandokos migrate` also works through it, but a direct
connection is recommended.

## 7. Inspecting data

Row-level security is **forced** on every tenant table: it applies to the owner too, and the owner
has no policy that shows rows. Connecting as `pandokos_owner` and finding every table empty is
therefore expected. To inspect data, use a superuser, and treat that access as the infrastructure
operator's, outside the security boundary the product enforces.

## 8. Unsupported

- libpq environment variables (`PGHOST`, `PGUSER`, `PGPASSWORD`, `PGSSLMODE`, `PGOPTIONS`, ...) are
  not a configuration interface. Pandokos gives every setting that the driver reads from them an
  explicit value, so none of them changes how it connects. Two cannot be neutralised: if
  `PGSERVICE` or `PGSERVICEFILE` is set, `serve` and `migrate` refuse to connect with
  `store: environment variable PGSERVICE is set; Pandokos does not use libpq service files, unset
  it`. Unset the variable.
- Statement-mode pooling.
- PostgreSQL older than 16.

## 9. Grants of migration 0002

Migration `0002_identity_audit` adds role assignments, virtual keys, platform keys and two audit
tables. The gateway reads `role_assignment` and `virtual_key` and can call the key lookup function;
it holds no privilege on `platform_key`, the directory of key IDs or the audit tables. The admin
role writes them with column-level grants (a key can only be revoked, once; audit rows can only be
inserted, and their ID and time are the database's). Nobody, the owner included, can change or
remove an audit row: triggers refuse it. Forced row-level security hides audit rows from the
owner too. `DELETE` on `service_account` is revoked: service accounts are disabled, never deleted.
If `database.address` is a pooler in transaction mode, set `database.direct_address` (see
[keys](keys.md)).

## 10. Grants of migration 0003

Migration `0003_vault` adds the vault: `kek_version`, `tenant_data_key` and `provider_credential`, a
view `provider_credential_metadata`, and the pin `virtual_key.credential_id`.

- `kek_version` is global: all three runtime roles read it, the admin role registers a version.
- `tenant_data_key` is read by the gateway and admin roles; the admin role inserts a wrapped data key,
  and nobody updates or deletes one (the worker will re-wrap them in a later sprint).
- `provider_credential` holds the sealed secret in the column `secret`. Only the gateway role can read
  it, in the tenant's session. The admin role has `SELECT` on every column except `secret`, inserts and
  updates the secret and the metadata with column-level grants, and deletes rows; it can never read the
  sealed secret, in any session.
- `provider_credential_metadata` is a view with the metadata columns, created with
  `security_invoker`: it runs with the caller's rights and so under the tenant policy. Lint rule L-19
  requires this of every view.
- Vault tables carry the canonical tenant policy and nothing else: a transaction in platform scope
  sees none of their rows. A trigger enforces the credential lifecycle (revocation is final and
  destroys the secret) and notifies `vault:<tenant>` on the notification channel.
- The admin role may set `virtual_key.credential_id` when a key is created; deleting a credential
  clears the pins that point to it.

## 11. Grants of migration 0004

Migration `0004_catalogue` adds the catalogue ([catalogue guide](catalogue.md)): `provider`,
`provider_endpoint`, `model`, `model_tenant_grant`, `price_import`, `price_version`, `price_rate` and
`tenant_model_restriction`, plus the column `virtual_key.models`, and seeds the providers `openai` and
`anthropic` with their endpoints and 17 models. No price is seeded.

- The seven catalogue tables are global with forced row-level security. A `SELECT` policy lets the
  roles that read them see every row in any session, a tenant session included. `INSERT`, `UPDATE` and
  `DELETE` policies for the admin role (and for the worker, where listed) require platform scope: an
  administration transaction in a tenant's session, or with no scope, cannot change the catalogue,
  whatever a handler does. If a catalogue write fails with a row-level security error, the transaction
  was not in platform scope; do not add a policy or a grant.
- `provider`, `provider_endpoint` and `model`: all three runtime roles read `provider` and `model`; the
  gateway and the admin role read `provider_endpoint`; the admin role inserts, updates (never the
  name, the family or the owning provider) and deletes. Triggers refuse a change of an immutable
  column.
- `model_tenant_grant` is the `model_grant` of the data model, made a platform table. It describes tenants, so a
  tenant's session sees only its own rows; in platform scope the admin role sees all of them and is
  the only role that inserts or deletes.
- `price_version` and `price_rate`: all roles read; the admin role and the worker insert in platform
  scope; the admin role deletes, and a trigger allows it only for a version that has not started. A
  version never changes. `price_import` is append-only: admin and worker read and insert in platform
  scope; nobody updates or deletes a row, the owner included.
- `tenant_model_restriction` carries the canonical tenant policy: the gateway reads it; the admin role
  reads, inserts, updates the list `models` and deletes.
- `virtual_key.models` is set at insert by the admin role and cannot change.
- The worker gains `INSERT` on `price_import`, `price_version` and `price_rate`, and on
  `platform_audit_event` with a policy that requires platform scope: the price refresh job writes its
  audit rows there. It still reads no tenant data.
- `provider_credential.provider` has a foreign key to `provider (name)`; a provider that credentials
  name cannot be deleted.
- Notifications: a change to the catalogue sends `catalog` on the notification channel and a change to
  a grant or a restriction sends `catalog:<tenant>`; the gateway empties its cache on them.

## 12. Grants of migration 0006

Migration `0006_usage_ledger` adds the usage ledger ([usage guide](usage.md)): the table
`usage_record`, partitioned by month, and the function that creates its partitions.

- `usage_record` carries the canonical tenant policy, forced, and nothing else. `pandokos_gateway`
  holds `INSERT` on the listed columns and **nothing else**: it cannot read a row, not even one it
  wrote, and holds no `UPDATE`, `DELETE` or `TRUNCATE`. `pandokos_admin` and `pandokos_worker` hold
  `SELECT`, under the policy (the tenant's rows in a tenant session, none without one). The columns
  `recorded_at` (the database's clock) and the defaults are not insertable by the gateway. Triggers
  refuse `UPDATE`, `DELETE` and `TRUNCATE` for everyone, the owner included.
- The insert ends in `ON CONFLICT DO NOTHING` without a target, which is why the gateway needs no
  `SELECT`: a batch that is sent twice stores each row once (the primary key is `tenant_id`,
  `started_at`, `id`). If a gateway permission error appears, do not widen the grant.
- The only foreign key is `tenant_id`: a row outlives the key, the credential, the model and the
  price it names.
- Rows are written in the tenant's session, one transaction per tenant and batch.
- Nothing in the product reads the ledger yet; the `SELECT` grants exist so that a later release
  needs no migration.

### Usage partitions

`usage_record` is a partitioned table with one partition per calendar month (UTC), named
`usage_record_yYYYYmMM`, and a default partition `usage_record_default` that takes any row whose month
has no partition (it is expected to stay empty).

- The function `pandokos.ensure_usage_partitions(p_ahead integer)` creates the partitions from the
  month before the current one to `p_ahead` months after it (1 to 12), skipping a month whose rows are
  already in the default partition. It reports how many partitions it created and whether the default
  partition has ever held a row. It is `SECURITY DEFINER` (it runs with the owner's rights, because
  runtime roles own nothing and cannot create tables), executable by `pandokos_worker` only, takes no
  name or date from its caller, and runs under an advisory lock with a 5 s lock timeout, so it is safe
  to call in parallel and repeatedly.
- `serve` calls it with 3 months ahead at start and then every hour; see the
  [usage guide](usage.md), section 7, for the log lines and the warning about the default partition.
- No runtime role holds any privilege on a partition: rows are reached through `usage_record`. Each
  partition has forced row-level security with the same policy and its own `BEFORE TRUNCATE` trigger,
  set by the owner-only function `protect_usage_partition`. The schema lint (rule L-20) checks all of
  this for every partition.
- With `retention.usage_months` set, the worker drops the partitions that end before the cut, through
  `platform_retention` (section 20); by default none is ever dropped.

## 13. Grants of migration 0008

Migration `0008_trusted_issuers` adds the registrations of trusted token issuers
([tokens guide](tokens.md)): `trusted_issuer` and `subject_mapping` (tenant-scoped, canonical tenant
policy), `platform_issuer` and `platform_issuer_tenant` (global, explicit policies), and
`issuer_directory`. It also widens the check constraint on `usage_record.principal_kind` to accept
`token`.

- The gateway role reads the four registration tables and writes none of them. The admin role reads
  them and holds column-level `INSERT`, `UPDATE` (not on `subject_mapping`: a mapping is deleted and
  created again) and `DELETE`. The worker role holds nothing on them.
- `platform_issuer` is readable by the gateway and admin roles; writes need the platform scope.
  `platform_issuer_tenant` shows a tenant's session its own rows only; the platform scope sees and
  changes all of them. A mapping belongs to a tenant, so the platform session sees none: deleting a
  platform-level registration deletes the tenants' mappings by cascade, and the audit row's
  `mappings_deleted` counts them through the definer function `platform_issuer_mapping_count` (below).
- `issuer_directory` holds one row per registration, maintained by triggers, and its primary key
  (`issuer`, `audience`) makes the pair unique across the installation and across both kinds. **No
  runtime role has any privilege on it.**
- The gateway role executes three `SECURITY DEFINER` functions, each taking single values and unable
  to list: `issuer_lookup(issuer, audiences)` (the registrations that an issuer and up to 16 audiences
  select, exact matches only), `platform_issuer_tenant_lookup(issuer_id, claim_value)` (one tenant ID or
  NULL) and `record_token_auth_failure(...)` (writes one `auth.failure` row in the session's tenant;
  the gateway role still holds nothing on the audit tables). A fourth function, `issuer_directory_sync`,
  is the trigger that maintains the directory and is not executable by any runtime role. One more
  definer function is executable by the **admin role only**: `platform_issuer_mapping_count(issuer_id,
  tenant_ids)` returns one number, the mappings of a platform-level registration in the named
  tenants, for the audit row of its deletion. The policy of `subject_mapping` is forced on the
  function's owner too and no tenant policy names the owner, so the function counts tenant by
  tenant, as that tenant's session, and restores the caller's tenant setting; the caller passes all
  tenants, which its platform session lists.
- Notifications: a change to `trusted_issuer` or `subject_mapping` sends `tenant:<id>`; a change to
  `platform_issuer` or `platform_issuer_tenant` sends `issuers`. The gateway empties its registration
  and token-principal caches on them; the vault and catalogue caches ignore `issuers`.
- `required_claims` is limited to 4096 bytes of stored size by a check constraint.

## 14. Grants of migration 0009

Migration `0009_limit_rules` adds the two tables of the limit rules ([limits guide](limits.md),
[ADR 0049](../adr/0049-limit-rules.md)) and one trigger function.

- `limit_rule` is tenant-scoped (canonical `tenant_isolation` policy). A row names a subject: the
  tenant (`subject_kind` `tenant`, empty `subject_id`), a service account by its ID, or a key by its
  key ID (a virtual key) or by the ID of its subject mapping (a caller with a token), a metric
  (`requests_per_minute`, `tokens_per_minute` or `concurrent_requests`) and a `limit_value` from 1 to
  1,000,000,000,000. The combination of tenant, subject and metric is unique. Migration 0009 had no
  foreign key to the subject; migration 0011 replaced the names by identifiers with foreign keys
  (section 16).
- `platform_limit_rule` is global: a row without a tenant is the default ceiling of every tenant,
  a row with a tenant replaces the default of that metric for it (unique per tenant and metric, nulls
  not distinct). A tenant's session sees the defaults and its own rows; writes need the platform scope.
- Who reads and writes. **The gateway role reads both and writes neither**; it reads a tenant's rules
  with two queries inside that tenant's session. The admin role reads both and holds column-level
  `INSERT` and `DELETE`, and `UPDATE` on `limit_value` only (a rule changes its value, never its
  subject or metric). The worker role holds nothing. Rules are written through the administration API
  and the CLI (migration 0010 and [limits guide](limits.md)); nothing is seeded.
- Notifications on `pandokos_cache`: a change to `limit_rule`, or to a row of `platform_limit_rule`
  that has a tenant, sends `limits:<tenant id>`; a change to a platform default (a row without a
  tenant) sends `limits`. The gateway's rule cache drops that tenant on `limits:<id>` and on
  `tenant:<id>`, and every tenant on `limits`; the other caches ignore both payloads. The cache
  lifetime is `cache.limits_ttl`, the bound on how long a change takes when no notification arrives.

## 15. Grants of migration 0010

Migration `0010_quotas_budgets` adds quotas and budgets to the limit rules ([limits guide](limits.md),
[ADR 0050](../adr/0050-quotas-and-budgets-periods-attribution-currencies.md),
[ADR 0052](../adr/0052-administration-of-limits.md)). It runs in one transaction as the owner role.

- `limit_rule` and `platform_limit_rule` gain `provider`, `model`, `currency`, `enforce` and
  `thresholds`, all with defaults (empty, empty, empty, true, no thresholds), and the unique key,
  the metric check and the value range are replaced by ones that know the nine metrics and the scope.
  The admin role gets `INSERT` on the new columns and `UPDATE` on `enforce` and `thresholds` besides
  `limit_value`; a rule's subject, scope, metric and currency still never change.
- The table `limit_event` is new: tenant-scoped (`tenant_isolation`), append-only for every role
  (update, delete and truncate are refused by triggers), with a unique key per tenant, rule, period
  start, kind and percentage. The gateway role holds `INSERT` on the listed columns, the admin role
  `SELECT`, the worker role nothing. Notifications are unchanged.
- `model` gains `default_output_tokens` (null by default); the admin role holds `INSERT` and `UPDATE`
  on it.
- Nothing is rewritten: only columns with defaults are added, so a table that already holds rows does
  not need a rewrite, `usage_record` is not touched, and the migration is quick. **It is safe to apply
  before the new binary starts**, and an old binary keeps working on the new schema: it reads rules
  with the three metrics it knows and ignores rules of metrics it does not know (it logs how many in
  one debug line), so rules written for quotas and budgets are not enforced until the new binary runs.
- The usage ledger reads for the counters (`AggregateUsageForLimits`) run on the worker role in the
  tenant's session; the gateway role still reads no usage record.

## 16. Grants of migration 0011

Migration `0011_limit_integrity` makes the limit rules reference their subject and their scope by
identifier, adds a time zone to the tenant and a state to the usage record, and adds two functions that
cross the tenant boundary ([limits guide](limits.md),
[ADR 0049](../adr/0049-limit-rules.md), [ADR 0050](../adr/0050-quotas-and-budgets-periods-attribution-currencies.md),
[ADR 0052](../adr/0052-administration-of-limits.md)). It runs in one transaction as the owner role.

**What changes.**

- `limit_rule` loses `subject_id`, `provider` and `model` and gains `service_account_id`,
  `virtual_key_id`, `subject_mapping_id`, `provider_id` and `model_id` (all `uuid`, nullable);
  `platform_limit_rule` gains `provider_id` and `model_id`. Composite foreign keys
  `(tenant_id, x_id)` reference the service account, the virtual key and the subject mapping of the
  rule's own tenant; `provider_id` references `provider`, and `(provider_id, model_id)` references
  `model`, which gains the unique key `model_provider_id_key`. All are `ON DELETE RESTRICT`: nothing
  cascades, because deletions are audited by the application. Checks tie the subject columns to
  `subject_kind` (`tenant`: none; `service_account`: the account; `key`: exactly one of the virtual
  key and the subject mapping) and a model to its provider. The unique key is over the typed columns and
  the metric. The admin role gets `INSERT` on the new columns and still no `UPDATE` on subject, scope,
  metric or currency. `limit_event` is unchanged and has no foreign key: it is history.
- Two triggers hold "a revoked key has no rule", since a key is never deleted and its foreign key
  never fires: `limit_rule_key_guard` refuses a rule for a revoked key, and the deferred constraint
  trigger `virtual_key_revoke_guard` fails the commit of a transaction that revokes a key and leaves a
  rule of it. The first takes a share lock of the key row before it looks at `revoked_at`, so the pair
  is race-free for every role and caller, with or without the advisory lock of the handlers: a
  revocation waits for a transaction that inserts a rule, and an insert that waited for a revocation
  sees it and is refused.
- `tenant` gains `time_zone`, `time_zone_previous` and `time_zone_effective_from`. The trigger
  `tenant_time_zone_guard` checks the shape of every change: the zone in force changes only through a
  pending change whose instant is in the future and at most 32 days away, a pending change that is
  replaced keeps its instant, and a tenant session changes nothing of its row but the zone. The policy
  `tenant_self_update` lets a tenant's own session update its row (the guard leaves it the zone);
  `tenant_time_zone_notify` sends `limits:<tenant id>` on a change. The admin role holds `INSERT` on
  `time_zone` and `UPDATE` on the three columns.
- `usage_record` gains the nullable column `limit_state` and the check
  `usage_record_limit_state_valid`, created `NOT VALID`: new rows are checked, existing rows hold NULL,
  and no partition is rewritten or scanned. The gateway role holds `INSERT` on the column.
- Two partial unique indexes, `audit_event_unlimited_traffic_key` on `(tenant_id, detail ->> 'day')`
  and `platform_audit_event_unlimited_traffic_key` on `(resource_id, detail ->> 'day')`, both for the
  action `limit.unlimited_traffic`, make that row once per tenant and day in each log.
- Five policies: `tenant_self_update`, `provider_owner_read` and `model_owner_read` (the owner role,
  for the first function below) and `platform_audit_event_unlimited_traffic_insert` and
  `..._select` (the owner role, limited to the one action, for the second).

**The two functions, and why each crosses the boundary.** Both are `SECURITY DEFINER` with a fixed
`search_path`, `REVOKE`d from `PUBLIC` and executable by one role.

- `delete_platform_issuer_limit_rules(issuer, tenants, actor ID, actor name, key ID, request ID)`,
  executable by the admin role. Deleting a platform-level issuer cascades to the subject mappings of
  its tenants, and a mapping that a rule names cannot go. The platform session sees neither the
  tenants' rules nor their audit logs, so the function refuses a session outside the platform scope,
  goes tenant by tenant in a fixed order as that tenant's session, skips a tenant that has no mapping
  of the issuer (no lock, nothing touched, so the caller may name every tenant), takes the lock of the
  others' rules,
  deletes the rules of that issuer's mappings, writes one `limit.delete` audit row for each in the
  log of the tenant (`cause` `platform_issuer_deleted`, actor type `platform`), restores the caller's
  tenant setting and returns the number. It can delete nothing but rules of mappings of the issuer it
  is given.
- `record_unlimited_traffic(day, time zone, provider, model, request ID)`, executable by the gateway
  role, which holds no privilege on either audit log. It writes the row `limit.unlimited_traffic` in
  the tenant's log (the tenant of the session) and in the platform's, `ON CONFLICT DO NOTHING` on the
  two indexes above, with fixed columns (actor type `platform`, actor name `gateway`), after checking
  every argument; the day must lie within two days of the database's date.

**Locks and duration.** The migration takes `ACCESS EXCLUSIVE` locks on the two rule tables,
`service_account`, `virtual_key`, `subject_mapping`, `provider`, `model` and the two audit logs, and on
`tenant` and `usage_record`, for the length of the transaction. The rule tables are small (at most 5,000
rules per tenant); the two audit indexes scan the audit logs once, which is the only step that grows
with the installation. To read and write the rows of every tenant, the data migration lifts forced
row-level security on the nine tables it touches and puts it back before the transaction ends; the
row triggers of the two rule tables are off meanwhile, so `updated_at` keeps its value. A failure
rolls everything back to the schema of 0010.

**What happens to existing rules.** Names become identifiers by what they designate now, which is what
the rule matched until now. A rule whose subject is gone, whose key is revoked, or whose provider or
model names nothing matched no request: it is deleted, and one audit row keeps every field. Tenant rules
write `limit.delete` in the tenant's log, ceilings write `platform_limit.delete` in the platform log; the
actor name is `migration 0011` and `detail.cause` is `migration_subject_missing`,
`migration_key_revoked` or `migration_scope_missing`. To list what the migration deleted of a tenant:

```sql
SELECT occurred_at, resource_id, detail FROM pandokos.audit_event
 WHERE action = 'limit.delete' AND actor_name = 'migration 0011';   -- in a tenant session
```

(ceilings: the same on `pandokos.platform_audit_event` with `action = 'platform_limit.delete'`, in the
platform scope). No rule is lost silently and none makes the migration fail.

**Upgrade: stop, migrate, start.** The migration is **not compatible with the binary of sprint 5.2**.
Stop every instance, run `pandokos migrate`, start the new binary. An old binary that is still running
turns not ready, because the ledger of migrations no longer matches, and a request it still takes is
refused with 503: its rule query names columns that are gone, so it fails closed and enforces nothing
wrongly. A new binary against the schema of 0010 does not start. The migration changes nothing that was
enforced. There is no down migration; to go back, restore the database from a backup taken before.

## 17. Grants of migration 0012

Migration `0012_tenant_price_lists` adds the price lists of a tenant and its active currency, makes the
limit rules one per currency, and records the source of a price in the usage ledger
([catalogue guide](catalogue.md), section 7, [limits guide](limits.md), section 5,
[ADR 0053](../adr/0053-tenant-price-lists-and-active-currency.md),
[ADR 0054](../adr/0054-usage-data-is-the-tenants.md)). It runs in one transaction as the owner role, and
changes no existing row and lifts no row-level security.

**What changes.**

- Two tenant-scoped tables, `tenant_price_version` (a version of one model in one currency: `kind`
  `price` or `end`, `valid_from`, `note`) and `tenant_price_rate` (its rates per category and
  threshold). Both have forced row-level security with the one policy `tenant_isolation`. **No policy
  shows a row of either to the platform scope**, and the platform's operations never open a tenant's
  session to read them: only the tenant reads and writes its prices. The gateway and the admin role
  read both tables; the admin role inserts versions and rates (by column) and deletes versions; nobody
  updates; **the worker holds nothing on them**, because counters are rebuilt from recorded costs, not
  from prices.
- Triggers: `tenant_price_version_guard` holds for every role, the owner included: a version never
  starts before `now()`, is never updated, and is deleted only before it starts; the deferred
  constraint triggers `tenant_price_version_shape` and `tenant_price_rate_shape` check at commit that a
  `price` has a base rate for every category it prices and an `end` has none; rates are immutable and
  no table can be truncated; a version inserted or deleted sends `catalog:<tenant id>`.
  `tenant_price_version_model_fkey` references `model` with `ON DELETE RESTRICT`: a model that a tenant
  priced cannot be deleted (409 `invalid_state`, field `tenant_prices`, which says only that some
  tenant priced it); retire it instead.
- `tenant` gains `active_currency`, `active_currency_previous` (`''` or three capital letters) and
  `active_currency_effective_from`, with the same shape as the time zone columns. The trigger
  `tenant_active_currency_guard` holds for every role: **only a session of the tenant itself changes
  them**, the currency in force changes only through a pending change whose instant is in the future
  and at most 32 days away, and a pending change that is replaced keeps its instant. `''` is the
  default of the installation, `pricing.default_currency`. `tenant_active_currency_notify` sends
  `catalog:<tenant id>` and `limits:<tenant id>`. The admin role holds `UPDATE` on the three columns
  (the policy `tenant_self_update` of migration 0011 limits it to the tenant's own row).
- `limit_rule` and `platform_limit_rule`: the unique key gains `currency` (`''` for count metrics, so
  nothing changes for them): one rule per subject, scope, metric and currency. The indexes are rebuilt.
- `usage_record` gains the nullable `price_source` (`catalogue` or `tenant`) and the check
  `usage_record_price_source_valid`, created `NOT VALID`: new rows are checked, existing rows hold
  NULL, and no partition is rewritten or scanned. The gateway role holds `INSERT` on the column. The
  ledger has no foreign key to a version.
- Four trigger functions are added (`tenant_active_currency_guard`, `notify_tenant_currency`,
  `tenant_price_version_guard` and `tenant_price_shape`); none is `SECURITY DEFINER`, they run with the
  rights of the session that writes and read nothing of another tenant: nothing that crosses the tenant
  boundary is new.

**Locks and duration.** `ACCESS EXCLUSIVE` on `tenant`, the two rule tables and `usage_record` for
the length of the transaction. Three columns with a constant default are added to `tenant` (no
rewrite); the rule tables are small (at most 5,000 rules per tenant); no partition is touched. A
failure rolls everything back to the schema of 0011.

**Upgrade: stop, migrate, start.** The schema holds nothing that the binary of sprint 5.3 would
misread (every tenant has the default currency), but that binary no longer serves: it turns not ready
because the ledger of migrations is not its own. A new binary against the schema of 0011 does not
start. There is no down migration; to go back, restore a backup taken before.

**What changes in behaviour for an installation at the upgrade.**

1. **Cost budgets and platform cost ceilings apply only in the tenant's active currency**, which is
   `pricing.default_currency` (`USD` unless you set it) for every tenant at the upgrade. An
   installation whose prices and budgets are in another currency sets the key **before** starting the
   new binary; otherwise its budgets are inactive until each tenant changes its currency (which takes
   effect at the start of the next month of the tenant's zone, [limits guide](limits.md), section 5).
2. **A catalogue price in another currency than the tenant's active currency no longer prices the
   tenant's requests**, and a budget in another currency no longer refuses with `no_price`: it is
   inactive. A model whose only catalogue price is an override in another currency has an unknown
   cost for a tenant in the default currency, and `no_price` for a tenant with an active budget
   (`limits.on_unknown_price`).
3. **The request log and the spans lose every field of the tenant**: identifiers of the tenant and
   the caller, model, credential, sizes, usage, cost, currency and limit rules; the spans keep only
   the attributes the OpenTelemetry conventions make Required, and the ledger writer no longer logs
   rows ([ADR 0055](../adr/0055-operational-telemetry-holds-no-tenant-data.md),
   [telemetry guide](telemetry.md)). No setting brings them back. The troubleshooting of
   `no_price` therefore moves from the operator's log to the tenant's tools (the capability check,
   `active` in `limit list`, `listTenantModels`, [limits guide](limits.md), section 15).

## 18. Grants of migration 0013

Migration `0013_users_sessions` adds people, sessions and idempotency keys
([sign-in guide](sign-in.md), [data model](../architecture/data-model.md),
[ADR 0056](../adr/0056-users-memberships-and-sessions-as-principals.md),
[ADR 0057](../adr/0057-local-accounts-and-sessions-formats-and-numbers.md),
[ADR 0059](../adr/0059-idempotency-keys.md)). It runs in one transaction as the owner role, changes no
existing row and lifts no row-level security. **The gateway and worker roles receive no privilege on any
table or function of this migration**, in any session (IAM-22 as a property of the database).

**What changes.**

- Nine global tables with forced row-level security and explicit policies, all granted to the admin role
  only, by column where a column never changes: `app_user` (the platform scope sees and writes every
  user; a tenant's session sees the users it manages and its members and writes the former),
  `local_account`, `user_identity`, `user_session`, `platform_role_assignment`, `signin_throttle`,
  `oidc_login` and `idempotency_key` (platform scope, or the scope that a row names), and
  `membership_directory` with no grant to any runtime role (read through `user_tenants`).
- Two tenant-scoped tables, `membership` and `membership_role`, with the one policy `tenant_isolation`;
  the admin role reads, inserts and deletes them (and updates the `source` of a membership); nothing
  else touches them.
- Five definer functions: `user_tenants` (the tenants of one user, platform scope only),
  `platform_user_lookup` (one platform-managed user for a tenant's session, by ID or username, never a
  list) and three trigger functions that keep the managing rule, the directory and the role rules
  (`membership_guard`, `membership_directory_sync`, `platform_role_assignment_guard`).
- The admin role may `EXECUTE` `issuer_lookup` and `platform_issuer_tenant_lookup` (migration 0010), for
  tokens on the administration listener.
- The check constraint of the audit tables accepts the actor type `host`.

**Locks and duration.** New tables and constraints only; the audit constraint is the one statement that
touches an existing table, on tables of the audit log that are small relative to the ledger. A failure rolls
everything back to the schema of 0012.

**Upgrade: stop, migrate, start.** An earlier binary does not start against the new schema, and a new
binary does not start against the schema of 0012. There is no down migration; to go back, restore a backup
taken before. After the upgrade `serve` warns while local accounts are on and no active user holds a
platform role: run `pandokos init --admin-username NAME` (an existing installation keeps its keys) or link
a person through the identity provider.

**What the administration role does at run time.** It purges expired sessions, throttle rows, sign-in
transactions and idempotency keys every five minutes. The worker is not involved.

## 19. Grants of migration 0014

Migration `0014_roles_support` adds custom roles, support actions and the role `owner`
([roles guide](roles.md), [data model](../architecture/data-model.md),
[ADR 0060](../adr/0060-built-in-roles-custom-roles-and-the-rule-against-escalation.md),
[ADR 0061](../adr/0061-support-actions-and-reasons-how-the-platform-acts-inside-a-tenant.md)). It runs in
one transaction as the owner role. **The worker role receives no privilege on anything of this
migration**, and no function of it is `SECURITY DEFINER`.

**What changes.**

- `tenant_role`, a tenant-scoped table with the one policy `tenant_isolation`: the name, description,
  sorted permissions and state of a tenant's custom role. The admin role reads, inserts (name,
  description, permissions), updates (description, permissions, state) and deletes; the **gateway role
  receives `SELECT` and nothing else**, because it reads the permissions of the custom roles of a key's
  service account in the statement that already reads its roles. Checks refuse the name of a built-in
  role and the prefixes `platform-` and `pandokos-`; a trigger refuses to rename a role or delete one
  that a service account or a member holds; another sends the tenant's cache notification on every
  change (ADR 0030).
- `support_action`, a global table with three policies (the platform scope sees and writes every row; a
  tenant's session reads the rows of its tenant and may end them). One open action per actor and tenant
  (a unique index); a row changes once, when it is closed; it is never deleted before tenant deletion.
  Granted to the admin role only.
- `audit_event` gains the nullable column `support_action_id` (no row is rewritten; the admin role may
  insert it). The audit log is still append-only.
- Two indexes on `role_assignment` and `membership_role` by (tenant, role), for the check that a role is
  unused.

**The owner step.** Until this migration `tenant-admin` held every tenant permission. The migration adds
the role `owner` to every service account and every member that holds `tenant-admin` by an
administrator's hand (they keep `tenant-admin`), so that every tenant has an owner and nobody holds
less. Rows that group rules wrote (source `idp`) are left to the rules. To do it the migration lifts
`FORCE ROW LEVEL SECURITY` on `role_assignment` and `membership_role`, as migration 0011 does, and forces
it again before it ends.

**Locks and duration.** New tables, two indexes and one nullable column; the inserts of the owner step
are proportional to the number of administrators. A failure rolls everything back to the schema of 0013.

**Upgrade: stop, migrate, start.** An earlier binary does not start against the new schema and a new
binary does not start against the schema of 0013. There is no down migration; restore a backup taken
before to go back. **Contract changes of v0.2 that the upgrade brings** (also in the release notes of
v0.2.0 and in the [roles guide](roles.md#10-what-changes-for-an-installation-of-v01)): a required reason
on `suspendTenant` and `reactivateTenant`; the time zone and the active currency are set with
`tenant_setting:write`, so keys scoped to `limit:write` or `price_list:write` lose the two settings; the
first service account of a tenant is `owner`.

## 20. Grants of migration 0015

Migration `0015_reports_audit` adds the hash chain of the two audit logs, the usage rollup, retention and
the metadata keys a tenant accepts ([audit guide](audit.md), [usage guide](usage.md),
[ADR 0064](../adr/0064-usage-rollup-reports-and-exports.md),
[ADR 0065](../adr/0065-the-audit-hash-chain-reading-and-retention.md)). It runs in one transaction as the
owner role. It adds no role. Its file is immutable once released; a fix is a later migration.

**What changes.**

- **The chain.** `audit_event` and `platform_audit_event` gain `seq`, `prev_hash` and `hash`; two heads,
  `audit_chain` (tenant-scoped, forced row-level security, one row per tenant) and `platform_audit_chain`
  (one row), are read by the admin role and written only by the sealing triggers. A `BEFORE INSERT`
  trigger on each log (`audit_seal_tenant`, `audit_seal_platform`, `SECURITY DEFINER`, replacing
  `audit_stamp`) numbers and hashes every row under a lock of the head. Three functions define the hash,
  `audit_field`, `audit_canonical` and `audit_row_hash`, and exist in this migration only.
  **Every existing audit row is sealed once**, chain by chain in the order of its time and ID: the
  migration lifts forced row-level security and the append-only triggers on the audit tables for that
  step and restores them before it ends. The function `record_unlimited_traffic` (the gateway's) is
  replaced: it inserts in a subtransaction and treats a unique violation as "already written", same
  signature and same answer.
- **Reading.** The admin role holds `SELECT` on `audit_event` (under the tenant's policy) and on
  `platform_audit_event` (under the platform scope).
- **Retention.** `append_only` lets a `DELETE` through when the setting `pandokos.retention` is `on`.
  Two functions are the only code that sets it, around their own `DELETE`:
  `tenant_retention(usage_months, audit_months)` (in a tenant's session) and
  `platform_retention(usage_months, audit_months)` (in platform scope), both `SECURITY DEFINER`, both
  granted `EXECUTE` to the **worker role** and to no other. They take no name and no date from the
  caller, check their arguments (0, or 2 to 120 months of usage, or 1 to 1200 months of audit), drop or
  delete only what lies before the cut, move the anchor, and write `usage.retention` and
  `audit.retention` in the log they touch. No runtime role holds `DELETE` on an append-only table, so for
  them the setting changes nothing; `UPDATE` and `TRUNCATE` stay refused for everybody. `grep -rn
  pandokos.retention internal/store/migrations/sql/` finds this migration only.
- **The rollup.** `usage_rollup` and `usage_rollup_state`, tenant-scoped with forced row-level security
  and the canonical policy. The worker may read, insert, update and delete the rollup and read, insert
  (`tenant_id`, `watermark`) and update (`watermark`, `updated_at`) its state; the admin role reads both.
  They are derived and can be rebuilt, so they are not append-only. The gateway has no grant on them.
- **The index.** `usage_record_recorded_idx` on `(tenant_id, recorded_at)`, on the partitioned ledger.
- **Metadata keys.** `metadata_key_policy`, tenant-scoped with the canonical policy, a unique row per
  tenant; the gateway and admin roles read it, the admin role inserts the keys, updates them and deletes
  the row. A change notifies the gateways' catalogue cache.

**Locks and duration.** The sealing of existing rows reads and updates every audit row once: it is
proportional to the audit log, which is small next to the ledger. The statement that builds the index
takes a lock that blocks writes to the ledger for as long as it runs; on a large ledger read the next
section first. A failure rolls everything back to the schema of 0014. There is no down migration; to go
back restore a backup taken before.

**Upgrade: stop, migrate, start.** An earlier binary does not start against the new schema, and a new
binary does not start against the schema of 0014.

### The index of migration 0015 on a large ledger

`CREATE INDEX` on a partitioned table builds one index per partition inside the migration's transaction,
and every build blocks writes to its partition. On a ledger of tens of millions of records that can last
minutes during which the gateway cannot record usage. To avoid the pause, **build the indexes beforehand**
without blocking writes, while the old binary still runs, as the **database owner role**, one statement
per partition (outside a transaction block, one at a time):

```sql
-- once for each partition that exists: the monthly ones and the default partition
CREATE INDEX CONCURRENTLY usage_record_y2026m10_tenant_id_recorded_at_idx
    ON pandokos.usage_record_y2026m10 (tenant_id, recorded_at);
CREATE INDEX CONCURRENTLY usage_record_y2026m11_tenant_id_recorded_at_idx
    ON pandokos.usage_record_y2026m11 (tenant_id, recorded_at);
-- ... every month up to the newest partition, then
CREATE INDEX CONCURRENTLY usage_record_default_tenant_id_recorded_at_idx
    ON pandokos.usage_record_default (tenant_id, recorded_at);
```

(the name of a partition is `usage_record_yYYYYmMM`; list them with `\d+ pandokos.usage_record`).
Then run `pandokos migrate`: when it creates the index of the parent table, PostgreSQL **attaches** the
existing index of each partition that has the same definition instead of building it, so the statement
is fast. A failed `CONCURRENTLY` build leaves an invalid index: drop it (`DROP INDEX`) and build it again.
Partitions that the worker creates later get their index from the parent. The index serves the rollup; the
reports and the limit counters are correct without it, only slower.

**Manual inserts into the ledger are not supported.** The rollup counts a record when its `recorded_at`
passes the watermark; a record put in by hand with a `recorded_at` behind it is not counted by the rollup.
