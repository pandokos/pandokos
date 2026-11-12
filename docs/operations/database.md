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

Do not make any of these roles a superuser, give it `BYPASSRLS`, `CREATEROLE`, `CREATEDB` or
`REPLICATION`, or make one a member of another: `pandokos migrate` and `pandokos serve` refuse.

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
| `LISTEN` / `NOTIFY` | Not used yet. A listener needs a session, so a later release will need one direct connection for notifications |

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
  not a configuration interface. Pandokos overwrites whatever they set.
- Statement-mode pooling.
- PostgreSQL older than 16.
