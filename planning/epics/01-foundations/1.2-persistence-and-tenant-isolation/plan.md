# Sprint 1.2 — Implementation plan

- Design: [sprint.md](sprint.md) (frozen). Section numbers below ("§5.4") refer to it; test IDs
  ("T-143") refer to its section 9; decisions ("D-6") to its section 8.
- This plan is followed literally and in order. If a step cannot be done as written, or the
  design and the plan disagree, stop and raise a question; do not improvise
  ([planning/README.md](../../../README.md)).

## Rules for the implementer

1. Work on a branch `feat/1.2-persistence-and-tenant-isolation` created from `main`. Sign off
   every commit (`git commit -s`).
2. Commits follow the parts of this plan: one commit for part A, one for part B (tests,
   failing), one per step of part C, one for part D, one for part E. Do not squash locally.
3. The only new direct dependency of the product module is `github.com/jackc/pgx/v5 v5.12.0`.
   `go.mod` says `go 1.25.0` and has **no** `toolchain` line. Adding any other module, or
   another version, is a design change: stop and ask.
4. Never write a password, or a URL that contains one, in any file of the repository. Test
   passwords are generated at run time. The directory `local/` is git-ignored: nothing in it is
   ever committed.
5. Never pass a password, a `secret.Secret`'s revealed value, or a connection string to a
   logger or into an error message. The only place that calls `Reveal()` on a database password
   is `connConfig` (step C5).
6. All SQL in product code and in migrations qualifies every object with its schema
   (`pandokos.tenant`, `pg_catalog.set_config`). Nothing may depend on `search_path`.
7. Product code runs SQL only through `store.DB`. Outside `internal/store/...` no package under
   `cmd/` or `internal/` imports `pgx`. The strings `app.tenant_id` and `app.scope` appear in Go
   code only in `internal/store/tx.go` and in tests.
8. The SQL of migration `0001_init` and of `deploy/postgres/roles.sql` is given in sprint.md and
   is copied exactly. Do not reformat it, reorder it or "improve" it.
9. Everything is written in English. Every exported identifier has a doc comment.
10. All commands are run from the repository root unless a step says otherwise.
11. Versions, image digests and the Go patch release in this plan were resolved on 2026-11-12.
    Before using each one, verify it with the command given next to it. If a value differs,
    follow the instruction given with that value; where none is given, stop and ask.
12. A database test must never be made to pass by skipping it. If the test database cannot be
    started in your environment, say so in the pull request; do not weaken the gate of §7.1.

## Before you start: a test database

Docker is needed. From the repository root, after step A7 exists:

```
make db-up        # starts PostgreSQL 16 on 127.0.0.1:55432 and PgBouncer on 127.0.0.1:56432
make test         # reads local/testdb/env by itself
make db-down      # stops both and discards the data
```

If Docker cannot be used but a PostgreSQL 16 server is available on which you can create a
superuser (for example the cluster on `localhost:5432` of the development machine), create a
superuser role with a password there, export
`PANDOKOS_TEST_DATABASE_URL=postgres://<user>:<password>@127.0.0.1:5432/postgres?sslmode=disable`
in your shell (never in a file of the repository), and run the pooler tests in CI only by
exporting `PANDOKOS_TEST_NO_POOLER=1` locally. Say in the pull request that you did.

## Part A — Scaffolding (commit: `chore: pgx, Go 1.25 floor, test database`)

### A1. Go floor and driver

1. Edit `go.mod`: change `go 1.24.0` to `go 1.25.0`.
2. Run `go get github.com/jackc/pgx/v5@v5.12.0`, then `go mod tidy`.
3. `go.mod` must now be exactly (the order inside the blocks is what `go mod tidy` writes):

   ```
   module github.com/pandokos/pandokos

   go 1.25.0

   require (
   	github.com/jackc/pgx/v5 v5.12.0
   	go.yaml.in/yaml/v3 v3.0.6
   )

   require (
   	github.com/jackc/pgpassfile v1.0.0 // indirect
   	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
   	github.com/jackc/puddle/v2 v2.2.3 // indirect
   	golang.org/x/sync v0.22.0 // indirect
   	golang.org/x/text v0.41.0 // indirect
   )
   ```

   `go mod tidy` removes the `pgx` requirement while no Go file imports it. That is expected at
   this step: add a temporary file `internal/store/doc.go` containing

   ```go
   // Package store is the database access of Pandokos: one pool per process role, transactions
   // that carry the tenant, and the start-up and readiness checks.
   package store

   import _ "github.com/jackc/pgx/v5/pgxpool" // replaced by real use in step C5
   ```

   and run `go mod tidy` again. In step C5 the blank import is deleted and the package comment
   stays in `doc.go`.
4. If `go.mod` differs from the block above in any version, stop and ask. There must be no
   `toolchain` line: delete it if a tool added one.
5. `go version` now reports go1.25.0 or later (the local go1.24.7 downloads it). If the
   download fails, stop and ask (sprint.md R-1).
6. In `test/sdk`: run `go mod tidy` and commit whatever it changes in `test/sdk/go.mod` and
   `test/sdk/go.sum`.

Verify the driver version is the intended one and is free of known vulnerabilities:

```
go list -m github.com/jackc/pgx/v5                 # github.com/jackc/pgx/v5 v5.12.0
GOTOOLCHAIN=go1.27.4 go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...   # No vulnerabilities found.
```

### A2. `.gitattributes` (new)

```
*.sql text eol=lf
*.sum text eol=lf
*.sh  text eol=lf
```

### A3. `.golangci.yml`

Two edits inside `linters.settings.depguard.rules`. This configuration was validated with
`golangci-lint config verify` (v2.5.0).

1. In rule `all`, add one line to `allow`, after `go.yaml.in/yaml/v3`:

   ```yaml
            - github.com/jackc/pgx/v5
   ```

2. After rule `all` and before `product-does-not-import-tests`, add three rules:

   ```yaml
        pgx-only-in-store:
          list-mode: lax
          files:
            - "**/cmd/**"
            - "**/internal/**"
            - "!**/internal/store/**"
          deny:
            - pkg: github.com/jackc/pgx/v5
              desc: only internal/store talks to the database; use store.DB
        store-takes-plain-values:
          list-mode: lax
          files:
            - "**/internal/store/**"
            - "!$test"
          deny:
            - pkg: github.com/pandokos/pandokos/internal/config
              desc: the store receives plain values through ConnOptions
            - pkg: github.com/pandokos/pandokos/internal/server
              desc: the store does not know the server
            - pkg: github.com/pandokos/pandokos/internal/gateway
              desc: the store does not know the gateway
        tenant-is-a-leaf:
          list-mode: lax
          files:
            - "**/internal/tenant/**"
            - "!$test"
          deny:
            - pkg: github.com/pandokos/pandokos/internal
              desc: the tenant package imports only the standard library
            - pkg: github.com/jackc/pgx/v5
              desc: the tenant package has no database code
   ```

Verify: `golangci-lint config verify`.

### A4. `deploy/postgres/roles.sql` (new)

Exactly the SQL block of sprint.md §5.6 that starts with `-- Pandokos privilege roles.` and
ends with `$$;`, followed by one newline.

### A5. `test/db/compose.yaml` (new)

```yaml
# PostgreSQL and PgBouncer for the test suite. Started by test/db/up.sh. Not for any other use.
name: pandokos-testdb

services:
  postgres:
    image: postgres:16-alpine@sha256:aa6cb33cf4c19c765cefcf48d96176258800870a9a1e930ad126200f9d825139
    command:
      - postgres
      - -c
      - max_connections=300
      - -c
      - fsync=off
      - -c
      - synchronous_commit=off
      - -c
      - full_page_writes=off
    environment:
      POSTGRES_PASSWORD_FILE: /run/secrets/postgres_password
    secrets:
      - postgres_password
    tmpfs:
      - /var/lib/postgresql/data
    ports:
      - "127.0.0.1:55432:5432"
    healthcheck:
      test: ["CMD", "pg_isready", "-h", "127.0.0.1", "-U", "postgres"]
      interval: 1s
      timeout: 3s
      retries: 60

  pgbouncer:
    image: edoburu/pgbouncer:v1.26.1-p0@sha256:488422e38cff912895d0d65d8963fcd306e6ac589a724398b6eb8e294e94d624
    depends_on:
      postgres:
        condition: service_healthy
    volumes:
      - ./pgbouncer.ini:/etc/pgbouncer/pgbouncer.ini:ro
      - ../../local/testdb/userlist.txt:/etc/pgbouncer/userlist.txt:ro
    ports:
      - "127.0.0.1:56432:6432"
    healthcheck:
      test: ["CMD", "pg_isready", "-h", "127.0.0.1", "-p", "6432"]
      interval: 1s
      timeout: 3s
      retries: 60

secrets:
  postgres_password:
    file: ../../local/testdb/postgres_password
```

Verify the two digests (each command prints a `Digest:` line that must equal the value above):

```
docker buildx imagetools inspect postgres:16-alpine
docker buildx imagetools inspect edoburu/pgbouncer:v1.26.1-p0
```

`postgres:16-alpine` moves with every PostgreSQL 16 patch release: a different digest there is
expected over time. In that case use the digest the command prints **today** and say so in the
pull request. A different digest for the versioned PgBouncer tag is not expected: stop and ask.

### A6. `test/db/pgbouncer.ini` (new)

```ini
; PgBouncer for the test suite: transaction pooling in front of the test PostgreSQL.
; Any database and any user. Passwords are looked up in PostgreSQL (auth_query), so users that
; a test creates on the fly can log in, and PgBouncer logs in to PostgreSQL as each of them.
[databases]
* = host=postgres port=5432

[pgbouncer]
listen_addr = 0.0.0.0
listen_port = 6432
unix_socket_dir =
auth_type = scram-sha-256
auth_file = /etc/pgbouncer/userlist.txt
auth_user = postgres
auth_query = SELECT usename, passwd FROM pg_catalog.pg_shadow WHERE usename = $1
pool_mode = transaction
max_client_conn = 1000
default_pool_size = 4
server_idle_timeout = 30
ignore_startup_parameters = extra_float_digits
```

`default_pool_size = 4` is deliberate: tests open more client connections than that, so server
connections are shared between clients, which is what the pooler tests need.

### A7. `test/db/up.sh` (new, mode 0755) and the Makefile

```sh
#!/bin/sh
# Starts the test PostgreSQL and PgBouncer and writes local/testdb/env.
# The superuser password is generated here and never leaves the git-ignored directory local/.
set -eu
cd "$(dirname "$0")/../.."
dir=local/testdb
mkdir -p "$dir"
if [ ! -s "$dir/postgres_password" ]; then
    (umask 077 && od -An -N24 -tx1 /dev/urandom | tr -d ' \n' > "$dir/postgres_password")
fi
pw=$(cat "$dir/postgres_password")
# PgBouncer runs as a non-root user and must be able to read its user list.
(umask 022 && printf '"postgres" "%s"\n' "$pw" > "$dir/userlist.txt")
docker compose -f test/db/compose.yaml up -d --wait
(umask 077 && {
    printf 'PANDOKOS_TEST_DATABASE_URL=postgres://postgres:%s@127.0.0.1:55432/postgres?sslmode=disable\n' "$pw"
    printf 'PANDOKOS_TEST_POOLER_ADDR=127.0.0.1:56432\n'
} > "$dir/env")
echo "test database is up; settings are in $dir/env"
```

The script never prints the password. `local/` is already in `.gitignore` (`/local/`): check it.

`Makefile` changes:

1. Add to `.PHONY`: `db-up db-down test-nodb migrations-sum`.
2. Add, after the `COMMIT ?=` block of variables:

   ```make
   # Loads local/testdb/env (written by make db-up) unless the variable is already set.
   DBENV = set -a; if [ -z "$$PANDOKOS_TEST_DATABASE_URL" ] && [ -f local/testdb/env ]; then . ./local/testdb/env; fi; set +a;
   ```

3. Replace the recipes of `test` and `test-container` and add four targets:

   ```make
   test: ## All tests of the root module, with the race detector (needs make db-up)
   	@$(DBENV) go test -race -count=1 ./...

   test-nodb: ## The same without the tests that need PostgreSQL (local use only, refused in CI)
   	PANDOKOS_TEST_NO_DATABASE=1 PANDOKOS_TEST_NO_POOLER=1 go test -race -count=1 ./...

   test-container: ## Tests against a built image: make image and make db-up first
   	@$(DBENV) PANDOKOS_TEST_IMAGE=$(IMAGE) go test -tags container -count=1 ./test/container/

   db-up: ## Start the test PostgreSQL and PgBouncer (Docker)
   	sh test/db/up.sh

   db-down: ## Stop them and discard the data
   	docker compose -f test/db/compose.yaml down -v

   migrations-sum: ## Rewrite the checksum file of the migrations
   	@cd internal/store/migrations/sql && for f in *.sql; do printf '%s %s\n' "$$f" "$$(sha256sum "$$f" | cut -d' ' -f1)"; done > ../migrations.sum
   ```

Verify: `make db-up` ends with `test database is up`; `docker compose -f test/db/compose.yaml ps`
shows both services `healthy`; `git status --short` shows nothing under `local/`.

### A8. `.github/dependabot.yml`

Append:

```yaml
  - package-ecosystem: docker-compose
    directory: /test/db
    schedule:
      interval: weekly
```

## Part B — Tests first (commit: `test: sprint 1.2 tests, failing`)

Write every file of this part before any file of part C. At the end of part B run
`make db-up` and then `go vet ./... ; make test` and save the output: every package touched by
this part must **fail** (mostly with build errors, because the packages under test do not exist
yet). Paste that output in the pull request description. This is the tests-first record.

General rules, in addition to those of the 1.1 plan (exact test names, no `time.Sleep` to wait
for a condition, timeouts on everything):

- A test that needs the database obtains it only through `test/dbtest` (B2). It never reads the
  environment variables itself.
- Database tests of `internal/store` and `internal/store/migrate` are in the **external** test
  packages `store_test` and `migrate_test` (files `*_db_test.go`), because `test/dbtest` imports
  those packages. Unit tests that need unexported names are in the package itself.
- Every context has a deadline: `ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)`
  unless stated.
- SQLSTATE of an error: `var pe *pgconn.PgError; errors.As(err, &pe)`, then `pe.Code`.
  Constants used below: `42501` insufficient privilege (also "violates row-level security
  policy"), `23503` foreign key, `23505` unique, `23514` check, `23001` restrict, `22P02`
  invalid text representation.
- Tests may call `t.Parallel()`: every test has its own database.

### B1. `test/testutil/testutil.go` — additions

```go
// RepoRoot returns the directory of the product module's go.mod, found by walking up from the
// working directory.
func RepoRoot(tb testing.TB) string

// WriteCA writes a new self-signed CA certificate (ECDSA P-256, valid one hour) as PEM to a
// file in tb.TempDir() and returns its path.
func WriteCA(tb testing.TB) string

// AssertAbsent fails the test if text contains any of the given secrets; it reports the index
// of the secret and the named output, never the secret or the surrounding text.
func AssertAbsent(tb testing.TB, where, text string, secrets ...string)
```

`test/repo/repo_test.go` keeps its own `root` function; do not refactor it.

### B2. `test/dbtest/` — package `dbtest`

The helper is test infrastructure, written in part B; it compiles only after part C (it
imports `internal/store`, `internal/store/migrate`, `internal/store/migrations`,
`internal/tenant`). Files: `gate.go`, `dbtest.go`, `gate_test.go`.

`gate.go`:

```go
const (
    EnvDatabaseURL = "PANDOKOS_TEST_DATABASE_URL"
    EnvPoolerAddr  = "PANDOKOS_TEST_POOLER_ADDR"
    EnvNoDatabase  = "PANDOKOS_TEST_NO_DATABASE"
    EnvNoPooler    = "PANDOKOS_TEST_NO_POOLER"
)

type action int

const (
    run action = iota
    skip
    fail
)

// gate decides what a test that needs a resource does. value is the resource's variable,
// optOut the value of its opt-out variable, ci the value of CI.
func gate(name, optOutName, value, optOut, ci string) (action, string) {
    inCI := ci != "" && ci != "false"
    switch {
    case optOut != "" && inCI:
        return fail, optOutName + " is set, but database tests cannot be skipped in CI"
    case value != "":
        return run, ""
    case optOut != "":
        return skip, name + " is not set and " + optOutName + " is set: skipped"
    default:
        return fail, name + ` is not set: this test needs PostgreSQL. Run "make db-up" and "make test", ` +
            "or set " + optOutName + "=1 to skip these tests locally (refused in CI)"
    }
}
```

Note the order: the opt-out is refused in CI even when the variable is set.

`dbtest.go`:

```go
// Database is one test database with its four login users.
type Database struct {
    Name string
    Host string // where the product connects: PostgreSQL, or PgBouncer for a pooled view
    Port int
    // unexported: the admin (superuser) connection parameters, the users and their passwords
}

// New creates an empty database and four login users, and registers their removal.
// It fails or skips the test according to the gate.
func New(tb testing.TB) *Database

// NewMigrated is New followed by Migrate.
func NewMigrated(tb testing.TB) *Database

// Migrate applies the embedded migrations with the owner user (store.Migrate).
func (d *Database) Migrate(tb testing.TB)

// Pooled returns a view of the same database reached through PgBouncer. It fails or skips the
// test according to the pooler gate.
func (d *Database) Pooled(tb testing.TB) *Database

// EachMode runs fn in a sub-test "direct" and in a sub-test "pooler", each with its own
// migrated database; the pooler one is reached through PgBouncer.
func EachMode(t *testing.T, fn func(t *testing.T, d *Database))

// User returns the login name for "owner", "gateway", "admin" or "worker".
func (d *Database) User(role string) string

// Passwords returns the four passwords, for assertions that they never appear in an output.
func (d *Database) Passwords() []string

// ConnOptions returns the product's connection options for that user (TLS mode "disable",
// connect timeout 5 s), towards d.Host and d.Port.
func (d *Database) ConnOptions(role string) store.ConnOptions

// Open returns a checked store.DB for the role (store.Open, then Check), closed at cleanup.
func (d *Database) Open(tb testing.TB, role store.Role) *store.DB

// Connect returns a raw connection logged in as that user, towards d.Host and d.Port, in
// QueryExecModeExec, closed at cleanup. Tests use it to play code that bypasses the helper.
func (d *Database) Connect(tb testing.TB, role string) *pgx.Conn

// Super returns a superuser connection to this database (always direct). It bypasses
// row-level security: tests use it to arrange data and to verify.
func (d *Database) Super(tb testing.TB) *pgx.Conn

// CreateLogin creates one more login user "<database>_<suffix>" with the given attribute
// clause (for example "SUPERUSER" or "") and memberships, removed at cleanup, and returns
// connection options for it.
func (d *Database) CreateLogin(tb testing.TB, suffix, attributes string, memberOf ...string) store.ConnOptions

// SeedTenant inserts a tenant through Super and makes it active.
func (d *Database) SeedTenant(tb testing.TB, name string) tenant.ID

// SetAcceptingConnections makes the database refuse (and drop) or accept connections.
func (d *Database) SetAcceptingConnections(tb testing.TB, accept bool)

// Env returns the PANDOKOS_DATABASE_* variables for a process: address, name, TLS mode
// "disable", the three runtime users with password files, and the owner user with its
// password file. Password files are created with testutil.WriteSecret.
func (d *Database) Env(tb testing.TB) []string
```

Behaviour of `New`, in order:

1. `gate(EnvDatabaseURL, EnvNoDatabase, …)` with the three environment values: `tb.Skip` or
   `tb.Fatal` with the message, or continue.
2. Parse the URL with `pgx.ParseConfig`; connect (the *admin connection*; closed at cleanup).
   A connection failure is `tb.Fatalf("cannot reach the test database: %v", err)` (the driver's
   error does not contain the password).
3. Execute the content of `deploy/postgres/roles.sql` (read from `testutil.RepoRoot`).
4. Choose `name := "pdk_t_" + 8 hex characters` from `crypto/rand`. Execute, with identifiers
   quoted by `pgx.Identifier{…}.Sanitize()`:
   `CREATE DATABASE <name> OWNER pandokos_owner`,
   `REVOKE ALL ON DATABASE <name> FROM PUBLIC`,
   `GRANT CONNECT ON DATABASE <name> TO pandokos_gateway, pandokos_admin, pandokos_worker`.
5. For each of `owner`, `gateway`, `admin`, `worker`: a password of 32 hex characters from
   `crypto/rand`; build the statement on the server so that the password is a quoted literal:
   `SELECT format('CREATE ROLE %I LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L IN ROLE %I', $1, $2, $3)`
   with (`<name>_<role>`, password, `pandokos_<role>`), then execute the returned text.
6. `tb.Cleanup`: `DROP DATABASE IF EXISTS <name> WITH (FORCE)`, then `DROP ROLE IF EXISTS` for
   every user created (including those of `CreateLogin`), then close. Cleanup uses a fresh
   context with a 30 s deadline and logs, but does not fail the test on, an error.

`Pooled`: `gate(EnvPoolerAddr, EnvNoPooler, …)`; returns a copy whose `Host` and `Port` come
from the variable. `Super` and all arranging helpers of a pooled view still connect directly.

`SeedTenant`: `INSERT INTO pandokos.tenant (name) VALUES ($1) RETURNING id::text`, then
`UPDATE pandokos.tenant SET state = 'active' WHERE id = $1::uuid`, then `tenant.ParseID`.

`SetAcceptingConnections(false)`: on the admin connection,
`ALTER DATABASE <name> WITH ALLOW_CONNECTIONS false`, then
`SELECT pg_catalog.pg_terminate_backend(pid) FROM pg_catalog.pg_stat_activity WHERE datname = $1 AND pid <> pg_catalog.pg_backend_pid()`.
`true`: `ALTER DATABASE <name> WITH ALLOW_CONNECTIONS true`.

`Env` returns exactly: `PANDOKOS_DATABASE_ADDRESS=<host>:<port>`, `PANDOKOS_DATABASE_NAME`,
`PANDOKOS_DATABASE_TLS_MODE=disable`, and for each of gateway, admin, worker, owner
`PANDOKOS_DATABASE_<ROLE>_USER` and `PANDOKOS_DATABASE_<ROLE>_PASSWORD_FILE`.

`gate_test.go` — T-119 `TestGate`: a table over (value set or not) × (opt-out set or not) ×
(`ci` in `""`, `"false"`, `"true"`, `"1"`), asserting the action of sprint.md §7.1 and that
every `fail` and `skip` message names the variable.

### B3. `internal/tenant/tenant_test.go`

T-101, T-102, T-103. T-102 loops over `States()` × `States()` and compares with a literal set
of the seven allowed pairs written in the test.

### B4. `internal/store/migrate/` tests

`migrate_test.go` (package `migrate`, no database):

- T-104 `TestLoad` with `testing/fstest.MapFS`. Valid set: `sql/0001_init.sql`,
  `sql/0002_more.sql`. Error cases, each naming the file in the error: `sql/1_init.sql`,
  `sql/0001-init.sql`, `sql/0001_Init.sql`, two files with version 2, versions 1 and 3,
  versions starting at 2, an empty file, a file with invalid UTF-8. A file `sql/README.md` is
  ignored.
- T-105 `TestLoadRejectsForbiddenStatements`: one file per forbidden statement of sprint.md
  §5.4 (also in lower case, and `  COMMIT;` indented); accepted: a file whose only suspicious
  content is a plpgsql body with `BEGIN` … `END`, and a column named `commit_count`.
- T-112 `TestCompare`: the unexported `compare(applied []Applied, ms []Migration)` for every
  row of the comparison table, asserting `errors.Is` with the sentinel and the message text of
  sprint.md §5.4 and §5.8.

`export_test.go` (package `migrate`):

```go
package migrate

// Preflight exposes preflight to the external tests, with substitutable role names.
var Preflight = preflight
```

`migrate_db_test.go` (package `migrate_test`): T-120 to T-127.

- Helper `withExtra(tb, extra ...string) []migrate.Migration`: builds a `fstest.MapFS` holding
  the real embedded files (read from `internal/store/migrations/sql` on disk through
  `testutil.RepoRoot`) plus one file per `extra` SQL text named `NNNN_extra.sql` with the next
  versions, and returns `migrate.Load` of it.
- T-120 `TestApplyFromEmpty` uses `dbtest.EachMode`-style sub-tests `direct` and `pooler`, but
  with `dbtest.New` (not migrated): `conn := d.Connect(t, "owner")`, `migrate.Apply`. Then
  through `d.Super`: ledger rows; `applied_by` equals `d.User("owner")`;
  `SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'pandokos' AND pg_catalog.pg_get_userbyid(c.relowner) <> 'pandokos_owner'`
  is 0, and the same for `pg_proc`.
- T-122: `Apply` the real set; then `Apply(withExtra("CREATE TABLE pandokos.zz_one (i integer)"))`:
  `Result.Applied` has one element; the table exists.
- T-123 (amended at review: the ledger existence check is a `pg_class` query, see sprint.md §5.4): `withExtra("CREATE TABLE pandokos.zz_two (i integer); SELECT 1/0; CREATE TABLE pandokos.zz_three (i integer)")`:
  error contains `migration 0002_extra`; neither table exists; ledger at version 1.
- T-124: tamper through `d.Super` (`UPDATE … SET checksum = repeat('0', 64)`;
  `INSERT … (version, name, checksum, pandokos_version) VALUES (99, 'future', repeat('0', 64), 'x')`;
  for the gap insert version 3 with the real set at 1).
- T-125: four goroutines, each with its own `d.Connect(t, "owner")`.
- T-126: `migrate.Preflight(ctx, conn, ownerRole, runtimeRoles)` with: a runtime role name
  that does not exist (`"pandokos_missing_" + random`); as owner role the name of a login
  created with `d.CreateLogin(t, "su", "SUPERUSER")`; the real names on a connection logged in
  as `gateway` (→ `ErrNotOwner`).

### B5. `internal/store/migrations/migrations_test.go`

T-106 `TestEmbeddedMatchesSumFile`: `Load()` succeeds; parse `migrations.sum` (lines starting
with `#` and empty lines ignored; each other line is `<file name> <64 hex>`); the two lists are
equal in length, order, name (`m.ID() + ".sql"`) and checksum; `ms[0].Name == "init"`.

### B6. `internal/store/` tests

`store_test.go` (package `store`, no database): T-107 to T-111.

- T-107 `TestConnConfig` calls the unexported `poolConfig(o Options) (*pgxpool.Config, error)`.
  Sub-test `environment`: `t.Setenv` for `PGHOST=nowhere.invalid`, `PGPORT=1`, `PGUSER=nobody`,
  `PGPASSWORD=wrong`, `PGDATABASE=nodb`, `PGSSLMODE=require`, `PGAPPNAME=x`,
  `PGOPTIONS=-c statement_timeout=1`, `PGTARGETSESSIONATTRS=read-only`; assert `Host`, `Port`,
  `Database`, `User`, `Password` from the options; `RuntimeParams` is exactly
  `{"application_name": "pandokos-gateway"}`; `len(Fallbacks) == 0`; `ValidateConnect == nil`;
  `TLSConfig == nil` for `disable`. Sub-test `verify-full` with `testutil.WriteCA`:
  `TLSConfig.MinVersion == tls.VersionTLS12`, `ServerName == host`, `RootCAs != nil`,
  `InsecureSkipVerify == false`; without CA file `RootCAs == nil`. Sub-test `pool`: the pool
  values of sprint.md §5.7 and `DefaultQueryExecMode == pgx.QueryExecModeExec`,
  `StatementCacheCapacity == 0`, `DescriptionCacheCapacity == 0`.
- T-109, T-110: `Open` with `Host: "127.0.0.1", Port: 1` (nothing listens there) and a
  `Migrations` slice with one dummy element; the calls must return in well under a second.

`store_db_test.go` (package `store_test`): T-130 to T-139.

- T-131: "binary one ahead" is `store.Open` with `Migrations` = real set plus one extra (build
  it as in B4); "database one ahead" and "checksum changed" by tampering through `d.Super`.
- T-132: users from `d.CreateLogin`; the table for case 6 is created through `d.Super` with
  `CREATE TABLE pandokos.zz_owned (i integer)` and `ALTER TABLE pandokos.zz_owned OWNER TO <user>`.
- T-133: wrong password = options of `gateway` with `Password: secret.New("wrong-" + random)`;
  closed port = port 1. Assert `strings.Contains(err.Error(), "cannot connect")` and that the
  real and the wrong password are absent from `err.Error()`.
- T-134, T-136, T-137, T-139 use `dbtest.EachMode`. Rows are arranged with `d.Super` and
  `d.SeedTenant`: two tenants, two service accounts each
  (`INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, $2)`).
- T-134 "size 1": open the `DB` with `store.Open` directly and `MaxConns: 1`, so that the
  `Platform` transaction that follows reuses the connection of the tenant transaction. Inside
  it assert `SELECT COALESCE(pg_catalog.current_setting('app.tenant_id', true), '')` is `''`
  and `SELECT count(*) FROM pandokos.service_account` is 0. Use the admin role (the gateway has
  no `Platform`).
- T-137: `MaxConns: 8`; each goroutine, 200 in total, runs
  `SELECT count(*), count(*) FILTER (WHERE tenant_id <> $1::uuid) FROM pandokos.service_account`
  inside `WithTenant` and asserts (2, 0), then `SELECT pg_catalog.pg_sleep(0.005)`.
- T-138: `d.SetAcceptingConnections(t, false)` → `testutil.Eventually` (5 s) `Ready` returns an
  error; `true` → `Eventually` (10 s) nil. Then insert ledger version 99 through `d.Super` →
  `Ready` errors; delete it → nil.
- T-139: inside `WithTenant`, 100 iterations of
  `SELECT $1::text, $2::bigint, $3::boolean, $4::timestamptz, $5::uuid` with Go values
  `string`, `int64`, `bool`, `time.Time`, `string` (a UUID text), scanning them back and
  comparing. In the `direct` sub-test, afterwards in the same `DB` (`MaxConns: 1`):
  `SELECT count(*) FROM pg_catalog.pg_prepared_statements` is 0.

### B7. `internal/config/config_test.go` — changes and additions

1. `validSource(t)` also sets `PANDOKOS_DATABASE_ADDRESS=127.0.0.1:5432`,
   `PANDOKOS_DATABASE_TLS_MODE=disable` and the three runtime `…_PASSWORD_FILE` variables
   (files written with `testutil.WriteSecret`, values generated with `crypto/rand` in the
   test). Add `validMigrateSource(t)`: `Purpose: PurposeMigrate`, the address, the TLS mode and
   only `PANDOKOS_DATABASE_OWNER_PASSWORD_FILE`.
2. T-04 `TestDefaults`, `wantKeys`, `wantDefaultText`: insert the sixteen new keys of sprint.md
   §5.2 between `shutdown.drain_timeout` and `skeleton.access_token_file`, with defaults
   `""`, `pandokos`, `verify-full`, `""`, `5s`, `pandokos_gateway`, `""`, `10`,
   `pandokos_admin`, `""`, `10`, `pandokos_worker`, `""`, `5`, `pandokos_owner`, `""`.
3. T-05: add the assertion that no key name has `port` as its last dot-separated segment and
   no segment starts with `service_`.
4. T-07: two valid values per new key. For `database.tls.ca_file` two files from
   `testutil.WriteCA` (leave `database.tls.mode` at `verify-full` for that key's iteration);
   for `database.tls.mode` the values `disable` and `verify-full`.
5. T-11: add `PANDOKOS_DATABASE_PORT`, `PANDOKOS_FOO_SERVICE_HOST` to the silent cases.
6. T-12 gains nothing; the database rules are in the new T-113.
7. New tests T-113 to T-116 as specified in sprint.md §9 and §5.2. T-113 rows: address `""`,
   `nohost`, `db:0`, `db:70000`, `:5432`, `/var/run/postgresql:5432`; name `""`, `a b`, `-x`,
   64 characters; each user likewise; TLS mode `require`; CA file set with mode `disable`; CA
   file missing; CA file without a certificate; CA file larger than 1 MiB; connect timeout
   `0s`; each `max_connections` `0` and `1001`; admin user equal to the gateway user; owner
   user equal to the worker user. T-114 includes: serve with
   `PANDOKOS_DATABASE_OWNER_PASSWORD_FILE=/nonexistent/owner` loads without problem and
   `Database.Owner.Password.IsZero()`; migrate without the skeleton variables loads without
   problem.

### B8. `cmd/pandokos/main_test.go` — changes and additions

- T-62 `TestHelp`: the usage text contains the two `migrate` lines and `[--for serve|migrate]`.
- T-63, T-64: the valid configuration includes the database keys (as in B7.1).
- T-117 `TestMigrateUsage`, T-118 `TestConfigCheckFor`: through `run`, no database.

### B9. `test/isolation/` — package `isolation`

Files written in this part: `spec.go` (the types `Finding`, `Spec`, `GlobalTable`, `Fixture`
and `DefaultSpec`, sprint.md §6.1 and §6.2), `lint_test.go`, `negative_test.go`,
`tenant_test.go`, `helpers_test.go`. The function `Lint` (file `lint.go`) is written in step C8,
so the lint tests fail to compile until then.

`DefaultSpec()`:

```go
func DefaultSpec() Spec {
    return Spec{
        Global: map[string]GlobalTable{
            "schema_migration": {Reason: "describes the installation, holds no tenant information"},
            "tenant":           {Reason: "the list of tenants; protected by its own policies", Restricted: true},
        },
        Grants: map[string]map[string][]string{
            "schema_migration": {
                "pandokos_gateway": {"SELECT"},
                "pandokos_admin":   {"SELECT"},
                "pandokos_worker":  {"SELECT"},
            },
            "tenant": {
                "pandokos_gateway": {"SELECT"},
                "pandokos_admin":   {"INSERT(display_name,name)", "SELECT", "UPDATE(display_name,name,state)"},
                "pandokos_worker":  {"SELECT"},
            },
            "service_account": {
                "pandokos_gateway": {"SELECT"},
                "pandokos_admin": {
                    "DELETE",
                    "INSERT(description,name,state,tenant_id,valid_from,valid_until)",
                    "SELECT",
                    "UPDATE(description,name,state,valid_from,valid_until)",
                },
            },
        },
        Fixtures: map[string]Fixture{
            "service_account": {
                Insert: `INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, 'sa-' || substr(md5(random()::text), 1, 12)) RETURNING id::text`,
                Update: `description = 'changed'`,
            },
        },
    }
}
```

Privilege strings are sorted within each list; column names are sorted inside the parentheses.

T-142 `TestDefaultSpecMatchesDesign` (no database) compares `DefaultSpec()` with literals
written in the test.

T-140 `TestSchemaLint`: `d := dbtest.NewMigrated(t)`; `Lint(ctx, d.Super(t), DefaultSpec())`;
on any finding, print every finding as `<Rule> <Object>: <Detail>` and fail.

T-141 `TestSchemaLintDetects`: one sub-test per row, each with its own migrated database. The
DDL runs through `d.Super` after `SET ROLE pandokos_owner` (so that objects are owned by the
owner) unless the row says otherwise. `OK` below is the canonical tail

```sql
ALTER TABLE pandokos.zz ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.zz FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pandokos.zz
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));
```

and `T` is the canonical table

```sql
CREATE TABLE pandokos.zz (
    tenant_id uuid NOT NULL REFERENCES pandokos.tenant (id),
    id uuid NOT NULL DEFAULT pandokos.uuid_v7(),
    PRIMARY KEY (tenant_id, id)
);
```

| Sub-test | Defect | Rule that must fire |
|---|---|---|
| `owner` | `T` + `OK`, then (as superuser) `ALTER TABLE pandokos.zz OWNER TO pandokos_admin` | L-01 and L-15 |
| `view` | `CREATE VIEW pandokos.zz_v AS SELECT id FROM pandokos.service_account` | L-02 |
| `no-tenant-column` | `CREATE TABLE pandokos.zz (id uuid PRIMARY KEY)` | L-03 |
| `nullable-tenant` | `T` with `tenant_id uuid REFERENCES …` (nullable) and `PRIMARY KEY (id)` + `OK` | L-03 |
| `no-tenant-fk` | `T` without `REFERENCES pandokos.tenant (id)` + `OK` | L-04 |
| `rls-not-enabled` | `T` only | L-05 |
| `rls-not-forced` | `T` + `ENABLE` + the policy, without `FORCE` | L-06 |
| `no-policy` | `T` + `ENABLE` + `FORCE`, no policy | **L-07** |
| `policy-true` | `T` + `ENABLE` + `FORCE` + `CREATE POLICY tenant_isolation ON pandokos.zz USING (true) WITH CHECK (true)` | L-08 |
| `policy-select-only` | the canonical expression but `FOR SELECT` | L-08 |
| `policy-raw-setting` | `USING (tenant_id = current_setting('app.tenant_id', true)::uuid)` and the same `WITH CHECK` | L-08 |
| `policy-restricted-roles` | canonical but `TO pandokos_admin` | L-08 |
| `extra-policy` | `T` + `OK` + `CREATE POLICY wide ON pandokos.zz FOR SELECT USING (true)` | L-09 |
| `key-without-tenant` | `T` with `PRIMARY KEY (id)` + `OK` | L-10 |
| `fk-without-tenant` | `ALTER TABLE pandokos.service_account ADD CONSTRAINT zz_u UNIQUE (id)`; `T` with an extra column `sa uuid REFERENCES pandokos.service_account (id)` + `OK` | L-11 |
| `grant-truncate` | `GRANT TRUNCATE ON pandokos.service_account TO pandokos_admin` | L-12 |
| `grant-public` | `GRANT SELECT ON pandokos.service_account TO PUBLIC` | L-12 |
| `grant-gateway-write` | `GRANT UPDATE ON pandokos.service_account TO pandokos_gateway` | L-12 |
| `grant-column` | `GRANT UPDATE (id) ON pandokos.service_account TO pandokos_admin` | L-12 |
| `missing-grant` | `REVOKE SELECT ON pandokos.tenant FROM pandokos_worker` | L-12 |
| `tenant-not-forced` | `ALTER TABLE pandokos.tenant NO FORCE ROW LEVEL SECURITY` | L-13 |
| `security-definer` | `CREATE FUNCTION pandokos.zz_f() RETURNS integer LANGUAGE sql SECURITY DEFINER RETURN 1` | L-14 |
| `public-function` | `CREATE FUNCTION pandokos.zz_g() RETURNS integer LANGUAGE sql RETURN 1` then `GRANT EXECUTE ON FUNCTION pandokos.zz_g() TO PUBLIC` | L-14 |
| `runtime-create` | (as superuser) `GRANT CREATE ON SCHEMA pandokos TO pandokos_worker` | L-15 |
| `no-fixture` | `T` + `OK`, nothing else | L-16, and **no other finding**: assert the findings are exactly one, with rule L-16 and object `pandokos.zz` |
| `stale-spec` | no DDL; the spec passed to `Lint` has an extra `Fixtures["gone"]` | L-16 |

Each sub-test asserts that at least one finding has the stated rule (and, for `no-fixture`,
the exact list).

`negative_test.go` — T-143 `TestCrossTenantNegative`, T-144 `TestPositiveControls`, T-147,
T-148, T-149, T-150, T-151.

T-143 structure (sprint.md §6.3):

```go
func TestCrossTenantNegative(t *testing.T) {
    dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
        a, b := d.SeedTenant(t, "tenant-a"), d.SeedTenant(t, "tenant-b")
        super := d.Super(t)
        spec := DefaultSpec()
        for _, table := range tenantScopedTables(t, super, spec) { // from the catalogue
            fx := spec.Fixtures[table]                              // missing → t.Fatalf
            keyA, keyB := insertRow(t, super, fx, a), insertRow(t, super, fx, b)
            before := snapshot(t, super, table, b)
            for _, role := range []string{"gateway", "admin", "worker"} {
                conn := d.Connect(t, role)
                for _, sess := range sessions(a) {                  // tenant-a, none, empty, platform
                    for _, st := range statements(table, fx, a, b, keyB, sess) {
                        t.Run(table+"/"+role+"/"+sess.name+"/"+st.name, func(t *testing.T) {
                            ensureRow(t, super, table, fx, a, &keyA) // A may have deleted its own row
                            rows, affected, err := runCommitted(t, conn, sess, st)
                            st.check(t, rows, affected, err, b)
                            if after := snapshot(t, super, table, b); after != before {
                                t.Fatalf("tenant B's rows changed")
                            }
                        })
                    }
                }
            }
        }
    })
}
```

Helpers (in `helpers_test.go`):

- `tenantScopedTables`: `SELECT c.relname FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'pandokos' AND c.relkind IN ('r', 'p') AND NOT c.relispartition ORDER BY 1`,
  minus the names in `spec.Global`. It must return at least one table, else `t.Fatal`.
- `snapshot`: `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text)::text, '[]') FROM pandokos.<table> t WHERE t.tenant_id = $1::uuid`.
- `runCommitted`: `BEGIN`; the session's setting (`SELECT pg_catalog.set_config('app.tenant_id', $1, true)`
  with A's ID, or with `''`, or `set_config('app.scope', 'platform', true)`, or nothing); the
  statement; then `COMMIT` if it succeeded, `ROLLBACK` if it failed. For a `SELECT` it returns
  the rows as `[]map[string]any` (`pgx.CollectRows(rows, pgx.RowToMap)`).
- `statements`: the ten rows of the table in sprint.md §6.3, in that order, with names
  `select-all`, `select-by-key`, `select-for-update`, `insert-for-b`, `update-all`,
  `update-by-key`, `move-to-b` (only when the session is `tenant-a`), `delete-all`,
  `delete-by-key`, `truncate`. `insert-for-b` is `fx.Insert` with B's ID. Table and column
  names come from the catalogue and the fixture and are quoted with `pgx.Identifier`.
- `check` per statement, as in sprint.md §6.3: "an error" must have SQLSTATE `42501`; "rows
  none of which belongs to B" compares each row's `tenant_id` with B.

T-144: `d.Connect(t, "admin")` in A's session: select returns A's row; the fixture insert for
A succeeds; `UPDATE … SET <fx.Update>` affects at least one row; delete by key affects one row.
Gateway: select returns A's rows. Worker: select is `42501`.

T-147: for each role, each statement on its own (no transaction):
`SET ROLE pandokos_owner`;
`SET row_security = off` followed by `SELECT count(*) FROM pandokos.service_account` (the
`SET` succeeds, the `SELECT` must fail with `42501`; then `RESET row_security`);
`ALTER TABLE pandokos.service_account DISABLE ROW LEVEL SECURITY`;
`ALTER TABLE pandokos.service_account NO FORCE ROW LEVEL SECURITY`;
`DROP POLICY tenant_isolation ON pandokos.service_account`;
`CREATE POLICY zz ON pandokos.service_account USING (true)`;
`CREATE TABLE pandokos.zz (i integer)`;
`ALTER TABLE pandokos.service_account OWNER TO CURRENT_USER`;
`GRANT SELECT ON pandokos.service_account TO PUBLIC`;
`CREATE FUNCTION pandokos.zz() RETURNS integer LANGUAGE sql RETURN 1`.
Each must fail with `42501`. (`GRANT` by a non-owner without grant option raises a warning,
not an error, when the role holds no privilege at all to grant: for the worker role accept
"no error and the privilege was not granted", verified through `d.Super` with
`has_table_privilege('public', 'pandokos.service_account', 'SELECT')` being false; for every
role assert that fact after the attempt.)

T-148: `conn := d.Connect(t, "owner")`; `SET ROLE pandokos_owner`; with no setting, with A's
tenant, and with the platform scope: `SELECT count(*) FROM pandokos.service_account`… The
owner with A's tenant set **does** match the `tenant_isolation` policy (it is for `PUBLIC`):
assert instead what forced row-level security means for the owner: with no setting it sees 0
service accounts although rows exist (a table owner without `FORCE` would see all); with the
platform scope it sees 0 tenants (the tenant policies name only the runtime roles).

T-149: as admin: `set_config('app.tenant_id', 'not-a-uuid', true)` then a select on
`service_account` → `22P02`; with `''` → zero rows, no error.

T-150: through `d.Super` after `SET ROLE pandokos_owner` create

```sql
CREATE TABLE pandokos.zz_child (
    tenant_id uuid NOT NULL REFERENCES pandokos.tenant (id),
    id uuid NOT NULL DEFAULT pandokos.uuid_v7(),
    service_account_id uuid NOT NULL,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, service_account_id) REFERENCES pandokos.service_account (tenant_id, id)
);
ALTER TABLE pandokos.zz_child ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.zz_child FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pandokos.zz_child
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));
GRANT SELECT, INSERT ON pandokos.zz_child TO pandokos_admin;
```

As admin in A's session: inserting a child with A's own service account succeeds; with B's
service account ID fails with `23503`; with a random UUID fails with `23503`; the two failures
have the same `Code`, `ConstraintName` and `Message`.

`tenant_test.go` — T-145 `TestTenantTablePolicies` (`dbtest.EachMode`), T-146
`TestTenantLifecycle`.

- T-145: raw connections per role; sessions tenant A, platform, none; count of visible tenant
  rows equals the table of sprint.md §5.6. As admin in platform scope:
  `INSERT INTO pandokos.tenant (name, display_name) VALUES ('t3', 'T3') RETURNING id, state`
  returns state `provisioning`; `INSERT … (id, name)` and `INSERT … (name, state)` fail with
  `42501`; `UPDATE … SET state = 'active'` succeeds; `DELETE` fails with `42501`. As admin in
  A's session: `UPDATE pandokos.tenant SET display_name = 'x'` affects 0 rows. As gateway and
  worker in platform scope: insert and update fail with `42501`.
- T-146: through `d.Super` (the trigger applies to everyone). For each pair (from, to) of
  `tenant.States()`: create a tenant, walk it to `from` along a fixed path
  (`provisioning`; `→active`; `→active→suspended`; `→deleting`; `→deleting→deleted`), attempt
  `UPDATE … SET state = to`; when `from == to` the update must succeed (no state change), except for `deleted`, where every update is refused with `23514` (amended at review, sprint.md §5.3);
  otherwise it must succeed exactly when `tenant.CanTransition(from, to)`, and a refusal has
  SQLSTATE `23514`. Also: `INSERT … (name, state) VALUES ('x', 'active')` → `23514`;
  `DELETE` → `23001`; `UPDATE … SET id = pandokos.uuid_v7()` → `23514`; any update of a
  deleted tenant → `23514`; `state_changed_at` unchanged by an update of `display_name` and
  later after a transition; names `A`, `-a`, `a-`, `a_b`, 64 characters → `23514`; a second
  tenant with a live tenant's name → `23505`; with a deleted tenant's name → accepted.

### B10. `test/e2e/e2e_test.go` — changes and additions

- `opts` gains `db *dbtest.Database` and `noMigrate bool`. `startBinary`: when `o.db` is nil,
  `o.db = dbtest.New(t)`; unless `o.noMigrate`, `o.db.Migrate(t)`; append `o.db.Env(t)` to the
  process environment. Add a variant `runBinary(t, env []string, args ...string) (code int,
  stdout, stderr string)` for commands that exit (`migrate`, `migrate status`, a `serve` that
  must fail), with a 20 s deadline.
- T-65 unchanged otherwise. T-66: collect `o.db.Passwords()` and assert
  `testutil.AssertAbsent` on stdout and stderr of every process, in addition to the canaries.
- T-160 to T-165 as specified in sprint.md §9. Details:
  - T-161 "extra ledger row": after `d.Migrate`, through `d.Super`:
    `INSERT INTO pandokos.schema_migration (version, name, checksum, pandokos_version) VALUES (2, 'future', repeat('0', 64), 'x')`.
    "Never listens": no `listening` line in stdout.
  - T-162: `d.SetAcceptingConnections(t, false)`; `Eventually` (10 s) `/readyz` is 503 while
    `/healthz` is 200; `true`; `Eventually` (15 s) `/readyz` is 200.
  - T-163: `p := d.Pooled(t)`; `runBinary(migrate)` with `p.Env(t)` on an unmigrated database,
    then `startBinary` with `db: p, noMigrate: true`.
  - T-164: replace `PANDOKOS_DATABASE_GATEWAY_USER` and its password file with a login from
    `d.CreateLogin(t, "su", "SUPERUSER")`.
  - T-165: three runs with `log.level` `debug`: (a) the gateway password file holds a wrong
    value (a fresh random string); `serve` exits 1; (b) `PANDOKOS_DATABASE_ADDRESS=127.0.0.1:1`;
    `serve` and `migrate` exit 1; (c) a normal `migrate`, `migrate status` and `serve` with one
    request and SIGTERM. For every process: `AssertAbsent` with all real passwords and the
    wrong one, and `AssertNoCanary`.

### B11. `test/repo/repo_test.go` — changes and additions

- T-R2: the allow-list of direct requirements becomes exactly
  `github.com/jackc/pgx/v5` and `go.yaml.in/yaml/v3`; the `go` line must be `go 1.25.0`.
- T-R8 `TestTestDatabasePinned`: in `test/db/compose.yaml` every line matching `^\s*image:\s*(\S+)`
  must match `^\S+:\S+@sha256:[0-9a-f]{64}$`; one of them starts with `postgres:16`; every
  entry under `ports:` starts with `"127.0.0.1:`.
- T-R9 `TestDatabaseGuide`: `docs/operations/database.md` contains the whole text of
  `deploy/postgres/roles.sql`, and each of these strings: `pandokos_owner`, `pandokos_gateway`,
  `pandokos_admin`, `pandokos_worker`, `pandokos migrate`, `pandokos migrate status`,
  `is a superuser; a runtime role must not be`, `has BYPASSRLS; a runtime role must not`,
  `is not a member of`, `a runtime role must not own the schema`,
  `each role needs its own database user`, `a runtime role must own nothing`,
  `the database has no Pandokos schema; run "pandokos migrate"`,
  `the database schema is older than this binary`,
  `the database schema is newer than this binary`,
  `a migration recorded in the database differs from this binary`, `pool_mode = transaction`.
- T-R10 `TestStoreIsTheOnlyDatabaseAccess`: walk `cmd/` and `internal/`; a `.go` file outside
  `internal/store/` must not contain `"github.com/jackc/pgx`; a `.go` file other than
  `internal/store/tx.go` and files ending in `_test.go` must not contain `app.tenant_id` or
  `app.scope`; `internal/store/db.go` must not match
  `func \(db \*DB\) [A-Z]\w*\([^)]*\) [^{]*(pgxpool\.|pgx\.Conn|pgx\.Tx)`.
- T-R11 `TestCIRunsDatabaseSuite`: in `ci.yml`, the text of job `test` and of job `image`
  (split the file at lines matching `^  [a-z-]+:$`) each contains `test/db/up.sh`; the whole
  file contains neither `PANDOKOS_TEST_NO_DATABASE` nor `PANDOKOS_TEST_NO_POOLER`.

### B12. `test/container/container_test.go` — T-72 changes

After the image inspection: `d := dbtest.New(t)`; write the password files into the mounted
secrets directory (mode 0644, as the existing ones) under the names `db_owner`, `db_gateway`,
`db_admin`, `db_worker`; build the `-e` list from the names and users of `d` (address
`127.0.0.1:<port>`, since the container uses host networking) with password files under
`/run/secrets/`. Then:

1. `docker run --rm --network host --read-only --cap-drop ALL --security-opt no-new-privileges -v <dir>:/run/secrets:ro <env…> $IMAGE migrate`
   must print `schema is at version 1`.
2. Start `serve` as before with the same environment added; the rest of the test is unchanged.
3. Add `d.Passwords()` to the log assertion (`AssertAbsent`).

## Part C — Implementation

After each step run the stated command; it must pass before the next step. Commit after each
step with the message given. `make db-up` must have been run.

### C1. `internal/tenant/tenant.go` — `feat: tenant id and lifecycle states`

Per sprint.md §5.3. `ParseID`: check the length and the hyphen positions, reject any character
outside `0-9a-f`, decode with `encoding/hex` after removing the hyphens, reject the zero value.
`String`: `hex.EncodeToString` with hyphens inserted. `CanTransition` is a `switch` on `from`
or a map literal of the seven pairs.

Verify: `go test -race ./internal/tenant/` (T-101 to T-103).

### C2. `internal/store/migrate/` — `feat: schema migrator`

Files: `migrate.go` (constants, types, errors), `load.go` (`Load`), `compare.go` (`compare`),
`apply.go` (`preflight`, `readLedger`, `ReadStatus`, `Check`, `Apply`).

- `Load`: `fs.ReadDir(fsys, dir)`; ignore entries that are directories or do not end in
  `.sql`; name pattern `^(\d{4})_([a-z0-9_]+)\.sql$`; `utf8.Valid`; the forbidden-statement
  expressions of sprint.md §5.4 compiled with `(?i)`; checksum
  `hex.EncodeToString(sha256.Sum256(content))`; sort by version; check `1..n`.
- `readLedger(ctx, q) (exists bool, rows []Applied, err error)`:
  `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ...)` for the table `pandokos.schema_migration` (amended at review: not `to_regclass`, sprint.md §5.4); when true,
  `SELECT version, name, checksum, applied_at FROM pandokos.schema_migration ORDER BY version`.
- `compare(applied []Applied, ms []Migration) (pending []Migration, err error)`: the table of
  sprint.md §5.4. Order of checks: gaps (`ErrLedger`, detail
  `version <v> follows version <u>`), then newer, then name or checksum, then pending.
- `Check`: `readLedger`; not exists → `ErrNoSchema` with the text of sprint.md §5.8; `compare`;
  pending not empty → `ErrSchemaOlder` with its text. Errors are built with
  `fmt.Errorf("%w: schema version %d, this binary needs version %d; run \"pandokos migrate\"", ErrSchemaOlder, k, n)`
  and likewise, so that `errors.Is` works and `Error()` is exactly the message of the tables.
  `ErrNoSchema` is returned as `fmt.Errorf("%w; run \"pandokos migrate\"", ErrNoSchema)`.
- `preflight(ctx context.Context, q Querier, owner string, runtime []string) error`:
  1. `SELECT pg_catalog.current_setting('server_version_num')::integer, pg_catalog.current_setting('server_version')`;
  2. `SELECT r.rolname, r.rolsuper OR r.rolbypassrls FROM pg_catalog.pg_roles r WHERE r.rolname = ANY($1)`
     with the owner and runtime names as `[]string`; report the first missing name in the order
     owner, runtime…; then the first privileged one;
  3. `SELECT session_user, (SELECT rolsuper FROM pg_catalog.pg_roles WHERE rolname = session_user) OR pg_catalog.pg_has_role(session_user, $1, 'SET')`.
- `Apply`:

  ```go
  func Apply(ctx context.Context, conn *pgx.Conn, ms []Migration, pandokosVersion string) (Result, error) {
      if err := preflight(ctx, conn, OwnerRole, RuntimeRoles); err != nil {
          return Result{}, err
      }
      _, applied, err := readLedger(ctx, conn)
      if err != nil {
          return Result{}, err
      }
      pending, err := compare(applied, ms)
      if err != nil {
          return Result{}, err
      }
      res := Result{Version: len(applied)}
      for _, m := range pending {
          did, err := applyOne(ctx, conn, m, pandokosVersion)
          if err != nil {
              return res, fmt.Errorf("migration %s: %w", m.ID(), err)
          }
          if did {
              res.Applied = append(res.Applied, m)
          }
          res.Version = m.Version
      }
      return res, nil
  }
  ```

  `applyOne` opens `tx, err := conn.Begin(ctx)`, defers `tx.Rollback` with a context from
  `context.WithoutCancel(ctx)` and a 5 s timeout, then runs in this order, each with
  `tx.Exec` and no arguments unless shown:
  `SET LOCAL ROLE pandokos_owner`;
  `SELECT pg_catalog.pg_advisory_xact_lock($1)` with `LockKey`;
  `readLedger(ctx, tx)`: if a row with `m.Version` exists and has the same name and checksum →
  commit and return `false, nil`; if it differs → `ErrChecksum`; if `len(rows) != m.Version-1`
  → `ErrLedger` with detail `expected version <m.Version-1>, found <len(rows)>`;
  `SET LOCAL lock_timeout = '10s'`;
  `tx.Exec(ctx, m.SQL)` — **no arguments**, so that `pgx` sends it with the simple protocol
  and the file may hold many statements;
  `INSERT INTO pandokos.schema_migration (version, name, checksum, pandokos_version) VALUES ($1, $2, $3, $4)`;
  `tx.Commit(ctx)`; return `true, nil`.

  `SET LOCAL ROLE` takes an identifier, not a parameter: build it from the constant with
  `pgx.Identifier{OwnerRole}.Sanitize()`.

Verify: `go test -race ./internal/store/migrate/` passes T-104, T-105, T-112; the database
tests of the package still fail to build until C4.

### C3. `internal/store/migrations/` — `feat: migration 0001`

- `sql/0001_init.sql`: exactly the SQL of sprint.md §5.5, from the line
  `-- 0001_init: schema, migration ledger, …` to the last `GRANT DELETE …;`, ending with one
  newline. Copy it; do not retype it.
- `migrations.go`:

  ```go
  // Package migrations holds the schema migrations of Pandokos, embedded in the binary.
  package migrations

  import (
      "embed"

      "github.com/pandokos/pandokos/internal/store/migrate"
  )

  //go:embed sql/*.sql
  var files embed.FS

  // Load returns the embedded migrations, in order.
  func Load() ([]migrate.Migration, error) { return migrate.Load(files, "sql") }
  ```

- `migrations.sum`: first line
  `# One line per migration: file name and SHA-256. A merged migration is never edited.`,
  then run `make migrations-sum` **into a temporary file** and append its output (the make
  target overwrites the file: for this first creation run it, then re-insert the comment line
  at the top by hand). From now on: adding a migration means appending one line; a changed
  line of an existing migration is a review blocker.

Verify: `go test -race ./internal/store/migrations/` (T-106). Then apply the file by hand once
to see it work: `make db-up`, and with `psql` against `PANDOKOS_TEST_DATABASE_URL` run
`deploy/postgres/roles.sql`, create a scratch database owned by `pandokos_owner`, run
`BEGIN; SET LOCAL ROLE pandokos_owner; \i internal/store/migrations/sql/0001_init.sql COMMIT;`
in it, and drop it. If any statement fails, stop and ask: do not change the SQL.

### C4. `test/dbtest` compiles — no commit of its own

`test/dbtest` was written in B2 and needs `internal/store`. It is first usable after C5. Do C5
next and verify both together.

### C5. `internal/store/` — `feat: store with tenant transactions and start-up check`

Files: `doc.go` (delete the blank import of A1, keep the package comment), `conn.go`,
`db.go`, `tx.go`, `check.go`, `migrate.go`.

`conn.go` — the connection configuration. Use this code:

```go
func quoteValue(v string) string {
    return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

func tlsConfig(o ConnOptions) (*tls.Config, error) {
    switch o.TLSMode {
    case "disable":
        return nil, nil
    case "verify-full":
        c := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: o.Host}
        if o.TLSCAFile != "" {
            pem, err := os.ReadFile(o.TLSCAFile)
            if err != nil {
                return nil, fmt.Errorf("store: read the CA file: %w", err)
            }
            pool := x509.NewCertPool()
            if !pool.AppendCertsFromPEM(pem) {
                return nil, errors.New("store: the CA file contains no PEM certificate")
            }
            c.RootCAs = pool
        }
        return c, nil
    default:
        return nil, errors.New("store: the TLS mode must be verify-full or disable")
    }
}

func dsn(o ConnOptions) string {
    seconds := int(o.ConnectTimeout / time.Second)
    if seconds < 1 {
        seconds = 1
    }
    return "host=" + quoteValue(o.Host) +
        " port=" + strconv.Itoa(o.Port) +
        " dbname=" + quoteValue(o.Database) +
        " user=" + quoteValue(o.User) +
        " sslmode=disable connect_timeout=" + strconv.Itoa(seconds)
}

// connConfig builds the driver configuration. The password is never part of a string, and
// nothing that libpq environment variables can set survives.
func connConfig(o ConnOptions, applicationName string) (*pgx.ConnConfig, error) {
    tc, err := tlsConfig(o)
    if err != nil {
        return nil, err
    }
    cfg, err := pgx.ParseConfig(dsn(o))
    if err != nil {
        return nil, errors.New("store: invalid connection options") // the driver's text is not passed on
    }
    cfg.Host = o.Host
    cfg.Port = uint16(o.Port)
    cfg.Database = o.Database
    cfg.User = o.User
    cfg.Password = o.Password.Reveal()
    cfg.TLSConfig = tc
    cfg.ConnectTimeout = o.ConnectTimeout
    cfg.Fallbacks = nil
    cfg.RuntimeParams = map[string]string{"application_name": applicationName}
    cfg.ValidateConnect = nil
    cfg.AfterConnect = nil
    cfg.KerberosSrvName = ""
    cfg.KerberosSpn = ""
    cfg.DefaultQueryExecMode = pgx.QueryExecModeExec
    cfg.StatementCacheCapacity = 0
    cfg.DescriptionCacheCapacity = 0
    return cfg, nil
}

func poolConfig(o Options) (*pgxpool.Config, error) {
    cc, err := connConfig(o.Conn, "pandokos-"+string(o.Role))
    if err != nil {
        return nil, err
    }
    pc, err := pgxpool.ParseConfig(dsn(o.Conn))
    if err != nil {
        return nil, errors.New("store: invalid connection options")
    }
    pc.ConnConfig = cc
    pc.MaxConns = int32(o.MaxConns)
    pc.MinConns = 0
    pc.MaxConnLifetime = 30 * time.Minute
    pc.MaxConnIdleTime = 5 * time.Minute
    pc.HealthCheckPeriod = 30 * time.Second
    return pc, nil
}
```

`gosec` will flag the integer conversions: validate the ranges in `validate` (below) and add
`//nolint:gosec // range checked by validate` on those two lines.

`db.go`: `Role`, `DatabaseRole`, `ConnOptions`, `Options`, `DB`, `Open`, `Role()`, `Close`.
`Open` calls `validate(o)` — role is one of the three; host not empty; port 1 to 65535;
database and user not empty; password not zero; TLS mode one of the two; `ConnectTimeout > 0`;
`MaxConns` 1 to 1000; `len(Migrations) > 0`; each failure an error `store: <what>` — then
`poolConfig`, then `pgxpool.NewWithConfig(context.Background(), pc)`. With `MinConns` 0 this
does not connect.

`tx.go`:

```go
const (
    settingTenant = "app.tenant_id"
    settingScope  = "app.scope"
    scopePlatform = "platform"
)

type tx struct{ t pgx.Tx }

func (x tx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
    return x.t.Exec(ctx, sql, args...)
}
func (x tx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
    return x.t.Query(ctx, sql, args...)
}
func (x tx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
    return x.t.QueryRow(ctx, sql, args...)
}

func (db *DB) WithTenant(ctx context.Context, id tenant.ID, fn func(ctx context.Context, tx Tx) error) error {
    if id.IsZero() {
        return ErrNoTenant
    }
    return db.inTx(ctx, settingTenant, id.String(), fn)
}

func (db *DB) Platform(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error {
    if db.role == RoleGateway {
        return ErrNoPlatformScope
    }
    return db.inTx(ctx, settingScope, scopePlatform, fn)
}

func (db *DB) inTx(ctx context.Context, setting, value string, fn func(ctx context.Context, tx Tx) error) (err error) {
    t, err := db.pool.BeginTx(ctx, pgx.TxOptions{})
    if err != nil {
        return fmt.Errorf("store: begin: %w", err)
    }
    rollback := func() {
        rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
        defer cancel()
        _ = t.Rollback(rctx)
    }
    defer func() {
        if p := recover(); p != nil {
            rollback()
            panic(p)
        }
        if err != nil {
            rollback()
        }
    }()
    if _, err = t.Exec(ctx, "SELECT pg_catalog.set_config($1, $2, true)", setting, value); err != nil {
        return fmt.Errorf("store: set the transaction context: %w", err)
    }
    if err = fn(ctx, tx{t}); err != nil {
        return err
    }
    if err = t.Commit(ctx); err != nil {
        return fmt.Errorf("store: commit: %w", err)
    }
    return nil
}
```

`check.go`: `Check` and `Ready`.

- `Check`: `conn, err := db.pool.Acquire(ctx)` (error → `database (<role>): cannot connect: <err>`);
  `defer conn.Release()`; run the user query of sprint.md §5.8 with
  `$1 = db.role.DatabaseRole()` and `$2`, `$3` the two other runtime roles in the order of
  `migrate.RuntimeRoles`, scanning also `current_user` (add it as first column); evaluate the
  conditions in the order of the message table; then `migrate.Check(ctx, conn, db.migrations)`.
  Every error returned is `fmt.Errorf("database (%s): %w", db.role, err)` so that `errors.Is`
  still finds the `migrate` sentinels.
- `Ready`: one `db.pool.QueryRow` of the query of sprint.md §5.8; mismatch →
  `database (<role>): the schema changed: version <max>, this binary needs version <n>`.

`migrate.go`: `Migrate` and `MigrationStatus`: `connConfig(o, "pandokos-migrate")`,
`pgx.ConnectConfig`, defer `conn.Close` with a 5 s context, call `migrate.Apply` or
`migrate.ReadStatus`. A connection error is returned as `cannot connect: <err>`.

Verify: `go test -race ./internal/store/... ./test/dbtest/` (T-104 to T-112, T-119, T-120 to
T-139). Then `grep -rn 'Reveal()' internal/store` shows exactly one line, in `conn.go`.

### C6. `internal/config/` — `feat: database configuration`

- `config.go`: the types of sprint.md §5.2; `Defaults()` sets the defaults of the key table.
- `keys.go`: sixteen `def(...)` entries inserted between `shutdown.drain_timeout` and
  `skeleton.access_token_file`, in the order of the table, with the descriptions of its
  "Meaning" column (shortened to one line where needed; T-R5 compares key, variable and
  default, not the description). Integers use `parseInt` as the existing entries do.
- `load.go`: `Source.Purpose`. In `Load`, replace
  `problems = append(problems, loadSecrets(cfg)...)` by a switch: `PurposeServe` →
  `loadSecrets(cfg)` then `loadDatabaseSecrets(cfg, false)`; `PurposeMigrate` →
  `loadDatabaseSecrets(cfg, true)`. In `warnings`, also skip names matching the expression of
  sprint.md §5.2 (compile it once as a package variable `reServiceLink`).
- `validate.go`: `validateDatabase(c *Config) []Problem`, called from `validate`, implementing
  the rule table in its order; `checkDatabaseAddress` (like `checkAddress` but port 1 to 65535
  and a non-empty host), filling `Host` and `Port`; the CA file checks read the file with
  `io.LimitReader` of 1048577 bytes; `loadDatabaseSecrets(c, migrate bool) []Problem`; the
  warning of sprint.md §5.2 appended in `listenerAndURLWarnings`. Add
  `database.connect_timeout` to the existing loop of durations.

Verify: `go test -race ./internal/config/` (T-04 to T-16, T-113 to T-116).

### C7. `cmd/pandokos/` — `feat: migrate command, database at start-up`

- `usage.go`: the new lines of sprint.md §5.1.
- `main.go`: `case "migrate": return runMigrate(ctx, args[1:], stdout, stderr, environ)`.
- `database.go` (new): 

  ```go
  func connOptions(c *config.Config, user string, password secret.Secret) store.ConnOptions {
      d := c.Database
      return store.ConnOptions{
          Host: d.Host, Port: d.Port, Database: d.Name, User: user, Password: password,
          TLSMode: d.TLS.Mode, TLSCAFile: d.TLS.CAFile, ConnectTimeout: time.Duration(d.ConnectTimeout),
      }
  }
  ```

- `migrate.go` (new): `runMigrate`. If `args[0] == "status"` the sub-command is status and the
  flags are parsed from `args[1:]`. After `fs.Parse`, `fs.NArg() > 0` is the usage error of
  sprint.md §5.1 (exit 2). Load with `Purpose: config.PurposeMigrate`; print problems and
  warnings exactly as `runConfig` does; `migrations.Load()`; wrap `ctx` with
  `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)`; call `store.Migrate(ctx,
  connOptions(cfg, cfg.Database.Owner.User, cfg.Database.Owner.Password), ms, buildinfo.Version)`
  or `store.MigrationStatus`. Output per sprint.md §5.1:
  `fmt.Fprintf(stdout, "applied %s\n", m.ID())`, `fmt.Fprintf(stdout, "schema is at version %d\n", res.Version)`;
  status lines `fmt.Fprintf(stdout, "%s  applied %s\n", id, t.UTC().Format(time.RFC3339))` and
  `"%s  pending\n"`, then `"schema is at version %d; this binary needs version %d\n"`. Errors:
  `fmt.Fprintf(stderr, "error: %v\n", err)`, return 1. `migrate status` returns 1 when
  `len(st.Pending) > 0`.
- `configcheck.go`: flag `--for` (string, default `serve`); `serve` → `PurposeServe`, `migrate`
  → `PurposeMigrate`, anything else → `pandokos: --for must be serve or migrate` on stderr,
  usage, exit 2.
- `serve.go`: after the warnings are logged and before `server.New`:

  ```go
  ms, err := migrations.Load()
  if err != nil {
      logger.Error("startup failed", "error", err.Error())
      return 1
  }
  var dbs []*store.DB
  defer func() {
      for _, db := range dbs {
          db.Close()
      }
  }()
  for _, r := range []struct {
      role store.Role
      c    config.DatabaseRole
  }{
      {store.RoleGateway, cfg.Database.Gateway},
      {store.RoleAdmin, cfg.Database.Admin},
      {store.RoleWorker, cfg.Database.Worker},
  } {
      db, err := store.Open(store.Options{
          Conn: connOptions(cfg, r.c.User, r.c.Password), Role: r.role,
          MaxConns: r.c.MaxConnections, Migrations: ms,
      })
      if err != nil {
          logger.Error("startup failed", "error", err.Error())
          return 1
      }
      dbs = append(dbs, db)
      cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
      err = db.Check(cctx)
      cancel()
      if err != nil {
          logger.Error("startup failed", "error", err.Error())
          return 1
      }
      logger.Info("database ready", "role", string(r.role), "schema_version", len(ms))
  }
  ```

  and after `server.New` succeeds, before `srv.Listen()`:

  ```go
  for _, db := range dbs {
      srv.Health().AddCheck("database-"+string(db.Role()), db.Ready)
  }
  ```

  `internal/server` and `internal/gateway` are not changed.

Verify: `go test -race ./cmd/pandokos/` (T-62 to T-64, T-117, T-118), then
`go test -race ./test/e2e/` (T-65, T-66, T-160 to T-165).

### C8. `test/isolation/lint.go` — `test: schema lint and isolation suite pass`

`Lint` runs the queries below on the given connection and applies the rules of sprint.md §6.1.
All were run against the schema of migration 0001 while the design was written. `$1` is a
qualified table name passed as text and cast with `::regclass`.

```sql
-- relations (L-01, L-02, L-05, L-06, L-13, L-16)
SELECT c.relname, c.relkind::text, pg_catalog.pg_get_userbyid(c.relowner), c.relrowsecurity, c.relforcerowsecurity
  FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'pandokos' AND c.relkind IN ('r', 'p', 'v', 'm', 'f') AND NOT c.relispartition
 ORDER BY 1;

-- tenant_id column (L-03): no row means no column
SELECT a.attnotnull, pg_catalog.format_type(a.atttypid, a.atttypmod)
  FROM pg_catalog.pg_attribute a
 WHERE a.attrelid = $1::regclass AND a.attname = 'tenant_id' AND NOT a.attisdropped;

-- policies (L-07, L-08, L-09, L-13)
SELECT p.polname, p.polcmd::text, p.polpermissive,
       ARRAY(SELECT CASE WHEN r = 0 THEN 'public' ELSE pg_catalog.pg_get_userbyid(r) END
               FROM unnest(p.polroles) AS r ORDER BY 1)::text[],
       COALESCE(pg_catalog.pg_get_expr(p.polqual, p.polrelid), ''),
       COALESCE(pg_catalog.pg_get_expr(p.polwithcheck, p.polrelid), '')
  FROM pg_catalog.pg_policy p
 WHERE p.polrelid = $1::regclass
 ORDER BY 1;

-- keys and foreign keys (L-04, L-10, L-11)
SELECT con.conname, con.contype::text,
       ARRAY(SELECT a.attname FROM unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord)
               JOIN pg_catalog.pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
              ORDER BY k.ord)::text[],
       COALESCE(fc.relname, ''),
       ARRAY(SELECT a.attname FROM unnest(con.confkey) WITH ORDINALITY AS k(attnum, ord)
               JOIN pg_catalog.pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.attnum
              ORDER BY k.ord)::text[]
  FROM pg_catalog.pg_constraint con LEFT JOIN pg_catalog.pg_class fc ON fc.oid = con.confrelid
 WHERE con.conrelid = $1::regclass AND con.contype IN ('p', 'u', 'f')
 ORDER BY 1;

-- table privileges (L-12)
SELECT c.relname, CASE WHEN a.grantee = 0 THEN 'public' ELSE pg_catalog.pg_get_userbyid(a.grantee) END, a.privilege_type
  FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace,
       LATERAL aclexplode(COALESCE(c.relacl, acldefault('r', c.relowner))) AS a
 WHERE n.nspname = 'pandokos' AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
 ORDER BY 1, 2, 3;

-- column privileges (L-12)
SELECT c.relname, a.privilege_type,
       CASE WHEN a.grantee = 0 THEN 'public' ELSE pg_catalog.pg_get_userbyid(a.grantee) END,
       string_agg(att.attname, ',' ORDER BY att.attname)
  FROM pg_catalog.pg_attribute att
  JOIN pg_catalog.pg_class c ON c.oid = att.attrelid
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace,
       LATERAL aclexplode(att.attacl) AS a
 WHERE n.nspname = 'pandokos' AND att.attnum > 0 AND NOT att.attisdropped
 GROUP BY 1, 2, 3
 ORDER BY 1, 3, 2;

-- functions (L-14)
SELECT p.proname, p.prosecdef, pg_catalog.pg_get_userbyid(p.proowner),
       ARRAY(SELECT CASE WHEN a.grantee = 0 THEN 'public' ELSE pg_catalog.pg_get_userbyid(a.grantee) END
               FROM aclexplode(COALESCE(p.proacl, acldefault('f', p.proowner))) AS a
              WHERE a.privilege_type = 'EXECUTE' ORDER BY 1)::text[]
  FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
 WHERE n.nspname = 'pandokos'
 ORDER BY 1;

-- role attributes (L-15)
SELECT r.rolname, r.rolsuper, r.rolbypassrls, r.rolcreaterole, r.rolcreatedb, r.rolreplication
  FROM pg_catalog.pg_roles r
 WHERE r.rolname IN ('pandokos_owner', 'pandokos_gateway', 'pandokos_admin', 'pandokos_worker');

-- memberships between the four roles (L-15): any row is a finding
SELECT pg_catalog.pg_get_userbyid(m.roleid), pg_catalog.pg_get_userbyid(m.member)
  FROM pg_catalog.pg_auth_members m
 WHERE pg_catalog.pg_get_userbyid(m.roleid) LIKE 'pandokos\_%'
   AND pg_catalog.pg_get_userbyid(m.member) LIKE 'pandokos\_%';

-- ownership and CREATE by runtime roles (L-15), once per runtime role $1
SELECT (SELECT count(*) FROM pg_catalog.pg_class WHERE pg_catalog.pg_get_userbyid(relowner) = $1)
     + (SELECT count(*) FROM pg_catalog.pg_proc WHERE pg_catalog.pg_get_userbyid(proowner) = $1)
     + (SELECT count(*) FROM pg_catalog.pg_namespace WHERE pg_catalog.pg_get_userbyid(nspowner) = $1),
       pg_catalog.has_schema_privilege($1, 'pandokos', 'CREATE'),
       pg_catalog.has_database_privilege($1, pg_catalog.current_database(), 'CREATE');
```

Rule details:

- L-08: collapse every run of white space in the expression to one space before comparing with
  `(tenant_id = ( SELECT pandokos.current_tenant_id() AS current_tenant_id))`; `polcmd` must be
  `*`; `polpermissive` true; roles exactly `{public}`.
- L-12: build, per table and grantee, the sorted list of table-level privilege names and of
  `PRIV(cols)` strings; ignore the grantee `pandokos_owner`; compare with `spec.Grants[table][grantee]`
  (absent means empty) for the three runtime roles and `public`; any other grantee is a finding.
  A column-level privilege that duplicates a table-level one of the same grantee is a finding
  too (`grant-column` must fire even though… no: it fires because `UPDATE(id)` is not in the
  matrix; keep the comparison literal).
- L-14: `public` in the grantee list is a finding; `prosecdef` and not in
  `spec.SecurityDefiner` is a finding.
- Findings are sorted by rule, then object.

Verify: `go test -race -v ./test/isolation/` — T-140 to T-151 pass, none skipped. Save the
`-v` output for the pull request.

### C9. Everything together — no commit of its own

`make test` passes. `go vet ./...`, `golangci-lint run` pass (fix findings in the commit of the
step they belong to, with `git commit --fixup` avoided: make a commit
`chore: satisfy golangci-lint` as sprint 1.1 did).

## Part D — CI and container (commit: `build: CI runs the database suite`)

### D1. Go patch release for the floor jobs

`1.25.14`. Verify that it exists and is the latest 1.25 patch:

```
go list -m -versions golang.org/toolchain | tr ' ' '\n' | grep -o 'go1\.25\.[0-9]*\.linux-amd64' | sort -V | tail -1
# v0.0.1-go1.25.14.linux-amd64 → 1.25.14
```

If a later 1.25 patch exists, use it and say so in the pull request.

### D2. `.github/workflows/ci.yml`

1. Job `lint`: `go-version: "1.24.7"` → `go-version: "1.25.14"`.
2. Job `test`, matrix entry `floor`: `go-version: "1.24.7"` → `go-version: "1.25.14"`.
3. Job `test`: between the `setup-go` step and `- run: go build ./...` insert

   ```yaml
      - name: Start PostgreSQL and PgBouncer
        run: |
          sh test/db/up.sh
          echo "::add-mask::$(cat local/testdb/postgres_password)"
          cat local/testdb/env >> "$GITHUB_ENV"
   ```

   and after `- run: go test -race -count=1 ./...` append

   ```yaml
      - name: Database logs
        if: failure()
        run: docker compose -f test/db/compose.yaml logs --no-color --tail 200
   ```

4. Job `image`: insert the same "Start PostgreSQL and PgBouncer" step before the step
   "Container tests", and the same "Database logs" step after it.

No action is added, so the pins of sprint 1.1 are unchanged. Do not add `services:`.

### D3. Verification of part D

`go test -race ./test/repo/` passes T-R1, T-R2, T-R8, T-R10, T-R11. `make image && make
test-container` passes T-72. `Dockerfile` and `.dockerignore` need no change: the SQL files are
under `internal/`, which is already copied.

## Part E — Documentation and decision records (commit: `docs: sprint 1.2 documentation and ADRs`)

### E1. `docs/operations/database.md` (new)

Sections, in this order:

1. **Requirements.** PostgreSQL 16 or later; one database; nothing else. Recommendation: one
   PostgreSQL cluster per Pandokos installation (the privilege roles are cluster-wide).
2. **Roles and users.** The model of sprint.md §5.6 in prose and its first two tables.
   The content of `deploy/postgres/roles.sql` **verbatim** in a `sql` code block. Then the
   database creation statements of §5.6, then both ways to create users:
   - recommended: four login users, each `GRANT`ed one privilege role, for example
     `CREATE ROLE acme_gateway LOGIN INHERIT;`, `\password acme_gateway`,
     `GRANT pandokos_gateway TO acme_gateway;`, and the configuration keys that name them;
   - shortest: `ALTER ROLE pandokos_gateway LOGIN;` and `\password pandokos_gateway` for each
     of the four, keeping the default user names.
   Use `\password` (psql prompts) in every example: no example contains a password.
3. **Configuration.** The `database.*` keys with a complete YAML example; password files;
   which command reads which password (the purpose table of §5.2); TLS modes and the CA file.
4. **Creating and upgrading the schema.** `pandokos migrate`, `pandokos migrate status`, their
   output and exit codes; the order "stop, migrate, start"; migrations are forward-only, a
   mistake is corrected by a new release or a restore; the owner password is needed only here;
   a `docker run … migrate` example.
5. **What `serve` checks at start.** The message table of §5.8 with one line of remedy each;
   no retry; readiness.
6. **Connection poolers.** The table of §5.9; a minimal PgBouncer excerpt with
   `pool_mode = transaction`; the three rules for operators (transaction or session mode;
   each user authenticated as itself; TLS); a note that `migrate` works through the pooler but
   a direct connection is recommended; a note that a later release will need one direct
   connection for notifications.
7. **Inspecting data.** Row-level security is forced: the owner sees no rows; use a superuser
   for inspection, and treat that access as the infrastructure operator's of the security
   model.
8. **Unsupported.** libpq environment variables (`PG*`) are not a configuration interface;
   statement-mode pooling; PostgreSQL older than 16.

T-R9 checks the verbatim SQL and the strings listed in B11.

### E2. `docs/operations/configuration.md`

Add the sixteen keys to the key table (T-R5); a section "Purposes" (which command needs which
secret, `config check --for`); the database rows in the validation and warning tables; the
Kubernetes service-link paragraph of §5.2 next to the existing one; the database part of the
complete example file.

### E3. `docs/operations/container.md`

The start example becomes two commands, `migrate` then `serve`, with the database variables
and password files; a sentence that PostgreSQL is now required and a link to the database
guide; `/readyz` reflects the database.

### E4. `docs/architecture/data-model.md`

- Rules 2 and 3 and the SQL example: replace the example by the canonical policy of
  sprint.md §5.5 (`tenant_id = (SELECT pandokos.current_tenant_id())`), and say why in two
  sentences (empty setting on reused connections; the function returns NULL for it). Add to
  rule 3 the sentence on what row-level security does not protect against (sprint.md R-3).
- Add rule 7: "Global tables are listed, and those that describe tenants have explicit
  policies. A transaction acts for a tenant, or in platform scope, or sees nothing."
- "Database roles": say that the four names are privilege roles without login and that
  database users are members; link the operations guide. In the table, the worker's "List
  tenants" gains "(in platform scope)".
- "Testing isolation": layer 1 names the rules (link to `test/isolation`); layer 2 says that
  the table list comes from the catalogue.
- Change nothing else.

### E5. `docs/architecture/overview.md` §11

In the layout block: `store/` line becomes `store/                 database access: pools per
role, tenant transactions, migrator, embedded migrations`; add `deploy/postgres/`; under
`test/` add `db/`, `dbtest/`, `isolation/`. Change nothing else.

### E6. `docs/adr/0026-schema-migrations.md`

Status `proposed`, date of the commit. Title: "Schema migrations: in-house, forward-only,
exact match at start". Refines ADR 0004.

- **Context.** ADR 0004 says migrations are SQL files embedded in the binary and applied by
  `pandokos migrate`; OPS-09 adds that an instance refuses a schema it does not match. The
  migrator runs with the only credentials that can change the schema, must put every object
  under one owner whatever user logs in, and must work when the only path to the database is a
  transaction-mode pooler.
- **Decision.** (1) Pandokos has its own migrator, not a library. (2) Migrations are numbered
  SQL files, forward-only, each applied in one transaction that also writes a ledger row with
  the file's SHA-256. (3) Inside that transaction the migrator switches to `pandokos_owner`
  with `SET LOCAL ROLE` and serializes with a transaction-level advisory lock; it uses no
  session state. (4) A merged migration is immutable; a checksum file in the repository makes
  an edit visible, and a ledger row that differs from the binary stops both `migrate` and
  `serve`. (5) `serve` starts only when the ledger equals the migrations of the binary, and
  turns not-ready if that stops being true. (6) Statements that cannot run in a transaction
  are not supported until designed.
- **Consequences.** A wrong migration is fixed by a new one or a restore. Upgrades are "stop,
  migrate, start": rolling upgrades need a compatibility window that this ADR does not provide
  (OPS-13). The migrator is about two hundred lines that the project owns and tests. Data
  migrations must cope with forced row-level security.
- **Alternatives considered.** A migration library (golang-migrate, goose, tern): a second
  driver abstraction, session locks, down migrations and a dependency in the most privileged
  path, for a need this small. Down migrations: untested code with owner rights. Accepting a
  newer schema at start: needs rules for writing backward-compatible migrations first.
  Applying migrations at `serve` start: gives every instance owner credentials (against ADR
  0003).

### E7. `docs/adr/0027-database-roles-and-session-context.md`

Status `proposed`. Title: "Database roles, session context and pooler safety". Refines ADR
0004; does not supersede it.

- **Context.** ADR 0004 fixes forced row-level security keyed on `app.tenant_id`, one database
  role per process role and compatibility with transaction-mode poolers. Building it showed
  four things the ADR leaves open: how role names relate to login users; that a
  transaction-local setting reads as an empty string, not as absent, on a reused connection;
  that the tenant list itself needs protection although it is global; and what "works behind a
  pooler" requires of the driver.
- **Decision.** (1) Four privilege roles with fixed names and no login, created by an operator
  script; database users are members of exactly one; `serve` verifies each of its users at
  start and refuses a superuser, `BYPASSRLS`, the owner, a user of two roles or an owner of
  objects. (2) All objects live in schema `pandokos`, are owned by `pandokos_owner`, and every
  statement qualifies names; `search_path` is not used. (3) Policies read the tenant through
  `pandokos.current_tenant_id()`, which maps an empty setting to NULL and fails on a malformed
  one; every tenant-scoped table has exactly one policy of one canonical form. (4) A second
  transaction-local setting, `app.scope = 'platform'`, read through `pandokos.platform_scope()`,
  marks platform sessions; global tables that describe tenants have policies in terms of it,
  granted per role. A transaction without tenant and without scope sees no protected row.
  (5) Pandokos keeps no session state on database connections: transaction-local settings, no
  prepared statements (`QueryExecModeExec`), transaction-level locks, one start-up parameter.
  The behaviour is the same with and without a pooler; there is no switch. (6) Functions in
  the schema are not executable by `PUBLIC`; `SECURITY DEFINER` functions and views are
  allow-listed in the schema lint.
- **Consequences.** Users and passwords rotate without migrations. Statements are parsed on
  every execution; a statement cache for direct connections can be added later. Parameters
  travel in text form: SQL should cast explicitly (`$1::uuid`). The application, not the
  database, names the tenant: the model defends against missing filters, not against arbitrary
  SQL on a runtime connection. Role names are cluster-wide: one cluster per installation, or
  care with `CONNECT`. `LISTEN` needs a direct connection.
- **Alternatives considered.** Configurable role names (migrations become templates); roles
  created by a migration (the owner needs `CREATEROLE`, and concurrent databases race);
  the raw `current_setting(...)::uuid` in each policy (errors on reused connections); a tenant
  list without policies (any role in any session could list tenants); a configuration switch
  for pooler mode (two behaviours to test, one more way to misconfigure); session-level
  `SET` with a reset query (unsafe behind transaction pooling).

### E8. `docs/adr/0025-go-toolchain.md` and `docs/adr/README.md`

- ADR 0025: append a section `## History` with one line:
  `- 2026-11: the language floor is raised to Go 1.25.0, required by the PostgreSQL driver (sprint 1.2, decision D-1).`
  In "Alternatives considered", leave the text as it is.
- `docs/adr/README.md`: two rows, 0026 "Schema migrations: in-house, forward-only, exact match
  at start" and 0027 "Database roles, session context and pooler safety", status `proposed`.

### E9. `README.md`, `CONTRIBUTING.md`

- `README.md`: the status note says that a database with tested tenant isolation exists and
  that nothing is proxied through it yet; add the database guide to the documentation list.
- `CONTRIBUTING.md`, under "Pull requests", one item: "Tests that need PostgreSQL fail when it
  is missing. `make db-up` starts a throwaway PostgreSQL and PgBouncer in Docker; `make test`
  uses them; `make test-nodb` runs the rest."

### E10. Planning files

- This sprint's `sprint.md`: `status: in-progress` at the start of implementation. It is set to
  `done` by the review, not by the implementer.
- `planning/epics/01-foundations/epic.md` is not changed.
- Open points O-1 to O-6 of sprint.md are copied into the pull request description under the
  heading "Questions for the maintainer".

## Part F — Verification

Run all of these from the repository root before opening the pull request. Each must end
successfully; paste the final lines of each in the pull request description.

```
make db-up
gofmt -l cmd internal test          # prints nothing
go mod tidy -diff                   # prints nothing
( cd test/sdk && go mod tidy -diff )
go build ./...
go vet ./...
make test                           # go test -race -count=1 ./... with the database
golangci-lint config verify
golangci-lint run
( . ./local/testdb/env && export PANDOKOS_TEST_DATABASE_URL PANDOKOS_TEST_POOLER_ADDR && GOTOOLCHAIN=go1.27.4 go test -race -count=1 ./... )
( . ./local/testdb/env && export PANDOKOS_TEST_DATABASE_URL PANDOKOS_TEST_POOLER_ADDR && go test -count=1 -v ./test/isolation/ ) | grep -c -- '--- SKIP'    # prints 0
( cd test/sdk && GOTOOLCHAIN=go1.27.4 go vet ./... && GOTOOLCHAIN=go1.27.4 go test -race -count=1 ./... )
GOTOOLCHAIN=go1.27.4 go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -test ./...
( cd test/sdk && GOTOOLCHAIN=go1.27.4 go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -test ./... )
make build && ./bin/pandokos help
make image && make test-container
CI=true PANDOKOS_TEST_NO_DATABASE=1 go test -count=1 ./test/isolation/ ; echo "exit $?"   # must FAIL: exit 1
make test-nodb                      # passes, with database tests reported as skipped
make db-down
```

The `grep -c` line prints `0` and therefore ends with a non-zero status: that is the expected
result of that one line.

Final checks against the design:

1. Every test ID of sprint.md §9 exists:
   `grep -rhoE 'func (Test|Fuzz|Benchmark)[A-Za-z0-9_]+' cmd internal test | sort` and compare
   with the tables.
2. `grep -rn 'Reveal()' cmd internal --include='*.go' | grep -v _test.go` shows, in addition to
   the call sites of sprint 1.1, exactly one new one: `internal/store/conn.go`.
3. `grep -rn 'app\.tenant_id\|app\.scope' cmd internal --include='*.go' | grep -v _test.go`
   shows only `internal/store/tx.go`.
4. `git status --short` and `git diff main --stat` show no file under `local/`.
   `grep -rnE 'postgres(ql)?://[^ ]*:[^ @]+@' -- . ':!planning' ':!local'` (use `git grep -nE`
   with the same expression) finds only the `printf` line of `test/db/up.sh`.
5. The SQL of `internal/store/migrations/sql/0001_init.sql` and of `deploy/postgres/roles.sql`
   is identical to the blocks of sprint.md §5.5 and §5.6 (compare by eye or by extracting the
   blocks with a script; do not edit either side to make them match: ask).
6. Go through the definition of done (sprint.md §10) line by line and tick each box in the pull
   request description, with the evidence.

## What the implementer does not do

- Does not add tables, columns, policies, grants, roles, functions, configuration keys,
  commands, log fields, dependencies or actions beyond those named here.
- Does not add a method to `store.DB` that returns a connection, a pool or a transaction, and
  does not export the pool "for tests": tests that need raw access use `test/dbtest`.
- Does not add `sqlc`, a query package, a tenant repository, or any admin functionality: 1.3.
- Does not edit `0001_init.sql` after step C3 is committed. A defect found later is raised as a
  question; before the sprint is merged the maintainer decides whether the file is corrected or
  a second migration is added.
- Does not weaken a test, a linter, the vulnerability gate or the database gate to make a step
  pass, and does not push tags or change repository settings.
