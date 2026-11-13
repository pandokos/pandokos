# Sprint 1.3 — Implementation plan

- Design: [sprint.md](sprint.md) (frozen). Section numbers below ("§5.7") refer to it; test IDs
  ("T-270") to its section 9; decisions ("D-8") to its section 8; "appendix A/B of the design"
  to its two appendices. This plan has three appendices of its own: A (the generator), B (the
  hand-written part of `internal/adminapi`), C (the query files).
- This plan is followed literally and in order. If a step cannot be done as written, or the
  design and the plan disagree, stop and raise a question; do not improvise
  ([planning/README.md](../../../README.md)).
- The steps are numbered so that the work can be resumed: every step of part C ends with a
  check and a commit. To resume, find the last commit of the list in rule 2 and continue with
  the next step.

## Rules for the implementer

1. Work on a branch `feat/1.3-service-accounts-keys-authz-audit` created from `main`. Sign off
   every commit (`git commit -s`).
2. Commits follow the parts of this plan: one for part A, one for part B (tests, failing), one
   per step of part C (the commit message is given with the step), one for part D, one for part
   E. Do not squash locally.
3. **No dependency is added.** `go.mod` and `go.sum` of the product module must be
   byte-identical to `main` at the end (`git diff main -- go.mod go.sum` prints nothing). If a
   tool or an editor changes them, revert the change. `sqlc` is run with `go run …@version`; it
   is never required by a `go.mod`.
4. Never write a key, a password, a certificate or a private key in any file of the repository.
   Keys are generated at run time by `authn.Generate`; certificates by `test/testcert`. There is
   no key-shaped constant, in product code or in tests.
5. Never pass a key, the revealed value of a `secret.Secret`, a hash of a key, an
   `Authorization` header, a request body or a database error's message to a logger, an error
   returned to a caller, a problem response or an audit record. `Reveal()` on a key is called in
   exactly three places: the JSON response of the three create handlers (through one helper,
   step C9), the output of `init` (C13), and the `Authorization` header of the CLI client (C14).
6. The files given in full by the design or this plan are copied exactly: the OpenAPI document
   (design appendix A), migration 0002 (design appendix B), `internal/apigen/main.go` (appendix
   A), `internal/adminapi/runtime.go` (appendix B), the five query files (appendix C),
   `sqlc.yaml` (§5.9). Do not reformat, reorder or improve them. The only permitted changes are
   those step C4 names for the query files.
7. Generated files (`internal/adminapi/adminapi.gen.go`, `internal/store/queries/*.go`) are
   never edited by hand. To change them, change their source and run `make generate`.
8. Product code runs SQL only as `queries.New(tx).<Method>(…)` inside a `store` helper. No
   package outside `internal/store/...` calls `Exec`, `Query` or `QueryRow`, and no SQL text
   appears outside `internal/store/...` and the migration files.
9. A handler that changes data writes its audit record in the same `store` transaction
   (§5.11). There is no "audit later", no goroutine, no second transaction for the record of a
   successful change.
10. Every error response of the admin API is written by `internal/admin/problem.go`. No other
    function calls `WriteHeader` with a status of 400 or more on the admin listener.
11. Everything is written in English. Every exported identifier has a doc comment.
12. All commands are run from the repository root unless a step says otherwise. The test
    database must be up (`make db-up`) from step A6 on.
13. Versions in this plan were resolved on 2026-11-12. Before using one, run the command given
    next to it. If the result differs, stop and ask.
14. A database test must never be made to pass by skipping it, and a bound must never be made
    to pass by lengthening it.

## Before you start

- Docker is needed for the test database (1.2 plan). From this sprint `test/db/up.sh` also
  needs `go` on the path.
- The release toolchain is needed locally for `make generate` (it builds `sqlc`):
  `GOTOOLCHAIN=go$(cat .go-version) go version` must print that version. If it cannot be
  downloaded, say so in the pull request and rely on the CI job `generate`; do not write the
  query package by hand.
- Verify the tool version:

  ```
  go list -m -json github.com/sqlc-dev/sqlc@v1.31.1 | grep -E '"(Version|Time)"'
  # "Version": "v1.31.1", "Time": "2026-04-22T14:52:15Z"
  ```

## Part A — Scaffolding (commit: `chore: API document, generation targets, test certificates`)

### A1. `.golangci.yml`

Inside `linters.settings.depguard.rules`:

1. In rule `all`, add four lines to `allow`, after `github.com/jackc/pgx/v5`:

   ```yaml
            - go/ast
            - go/format
            - go/parser
            - go/token
   ```

   (`$gostd` does not cover them in golangci-lint v2.5.0; without the lines `internal/apigen`
   and T-R12 do not pass the linter.)

2. In rule `gateway-takes-plain-values`, leave `files` as it is and add to
   its `deny`:

   ```yaml
            - pkg: github.com/pandokos/pandokos/internal/admin
              desc: the planes do not import each other (ADR 0003)
            - pkg: github.com/pandokos/pandokos/internal/authn
              desc: the gateway sees authentication through gateway.Authenticator
            - pkg: github.com/pandokos/pandokos/internal/cli
              desc: the gateway does not know the CLI
            - pkg: github.com/pandokos/pandokos/internal/store
              desc: the gateway does not reach the database itself
   ```

   (`internal/admin` as a prefix also denies `internal/adminapi`.)

3. After `gateway-takes-plain-values`, add:

   ```yaml
        admin-takes-plain-values:
          list-mode: lax
          files:
            - "**/internal/admin/**"
            - "!$test"
          deny:
            - pkg: github.com/pandokos/pandokos/internal/gateway
              desc: the planes do not import each other (ADR 0003)
            - pkg: github.com/pandokos/pandokos/internal/family
              desc: families belong to the data plane
            - pkg: github.com/pandokos/pandokos/internal/server
              desc: the handlers do not know the server
            - pkg: github.com/pandokos/pandokos/internal/config
              desc: the handlers receive plain values through Options
        adminapi-is-standard-library-only:
          list-mode: strict
          files:
            - "**/internal/adminapi/**"
            - "!$test"
          allow:
            - $gostd
        authz-is-a-leaf:
          list-mode: strict
          files:
            - "**/internal/authz/**"
            - "**/internal/requestid/**"
            - "!$test"
          allow:
            - $gostd
            - github.com/pandokos/pandokos/internal/tenant
        cli-is-a-client:
          list-mode: strict
          files:
            - "**/internal/cli/**"
            - "!$test"
          allow:
            - $gostd
            - github.com/pandokos/pandokos/internal/adminapi
            - github.com/pandokos/pandokos/internal/secret
            - github.com/pandokos/pandokos/internal/buildinfo
   ```

   In rule `pgx-only-in-store` nothing changes: `internal/store/queries` is under
   `internal/store`.

Check: `golangci-lint config verify` succeeds.

### A2. `Makefile`

Add after the `GOVULNCHECK` line:

```make
SQLC := github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
GENERATED := internal/adminapi internal/store/queries
```

Add `generate generate-check` to `.PHONY`, and these targets after `migrations-sum`:

```make
generate: ## Regenerate internal/adminapi (apigen) and internal/store/queries (sqlc)
	go run ./internal/apigen -spec api/openapi/admin.v1.yaml -out internal/adminapi/adminapi.gen.go
	CGO_ENABLED=0 GOTOOLCHAIN=go$(RELEASE_GO) go run $(SQLC) generate

generate-check: generate ## Fail if the generated code is not what is committed
	@test -z "$$(git status --porcelain -- $(GENERATED))" || { git status --porcelain -- $(GENERATED); echo "generated code is not current: run make generate and commit the result"; exit 1; }
```

Change `check:` to `check: fmt-check vet lint tidy generate-check test test-sdk vuln`.

### A3. `api/openapi/admin.v1.yaml` (new) and `sqlc.yaml` (new)

1. Create `api/openapi/admin.v1.yaml` with exactly the content of appendix A of the design.
   Check: `python3 -c "import yaml;d=yaml.safe_load(open('api/openapi/admin.v1.yaml'));print(len(d['paths']), sum(1 for p in d['paths'].values() for m in p if m!='parameters'))"`
   prints `15 21` (any YAML tool will do; the numbers are 15 paths and 21 operations).
2. Create `sqlc.yaml` at the repository root with exactly the content of §5.9.
3. Add `api` to the `Dockerfile` only if a later step says so (none does: the document is not
   embedded in the binary).

### A4. `test/testcert/` (new)

Package `testcert` as in §7.1, standard library only (`crypto/ecdsa`, `crypto/x509`,
`encoding/pem`).

- `NewCA`: ECDSA P-256 key; self-signed certificate, common name `pandokos test CA`, random
  128-bit serial, `IsCA`, `BasicConstraintsValid`, key usage `CertSign`.
- `Issue`: ECDSA P-256 key; random serial; key usage `DigitalSignature`; extended key usage
  `ServerAuth`; `DNSNames` and `IPAddresses` as given; key as PKCS #8 in a `PRIVATE KEY` block.
- `Files`: `NewCA(time.Hour)`, `Issue` valid from one minute ago for one hour, writes into
  `tb.TempDir()`.
- `cmd/testcert/main.go`: flags `-dir` (required), `-dns`, `-ip` (comma-separated), `-days`
  (default 30). If `<dir>/server.pem` parses and is valid for more than 24 hours, print
  `certificate is current` and exit 0 without writing. Otherwise create the directory (0755),
  write `ca.pem` (0644), `server.pem` (0644), `server.key` (0600), print
  `wrote <dir>/ca.pem, server.pem, server.key`. It prints no key material.
- `testcert_test.go`: a certificate from `Issue` verifies against the CA for a listed name and
  IP and fails for another name; `Files` writes the key with mode 0600.

`test/testutil.WriteCA` of 1.2 stays as it is.

### A5. `test/db/` — TLS on the test PostgreSQL

1. `test/db/up.sh`: after the line that writes `userlist.txt` and before `docker compose`, add:

   ```sh
   go run ./test/testcert/cmd/testcert -dir "$dir/tls" -dns pandokos-test-db -ip 127.0.0.1 -days 30
   ```

   and in the block that writes `$dir/env` add a third line:

   ```sh
       printf 'PANDOKOS_TEST_DATABASE_CA_FILE=%s/%s/tls/ca.pem\n' "$(pwd)" "$dir"
   ```

2. `test/db/compose.yaml`, service `postgres`: add an `entrypoint`, extend `command` and add a
   volume. The image line and everything not shown stay as they are.

   ```yaml
       entrypoint:
         - sh
         - -c
         - |
           set -eu
           mkdir -p /run/pandokos-tls
           install -o postgres -g postgres -m 0644 /tls/server.pem /run/pandokos-tls/server.pem
           install -o postgres -g postgres -m 0600 /tls/server.key /run/pandokos-tls/server.key
           exec docker-entrypoint.sh "$$@"
         - --
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
         - -c
         - ssl=on
         - -c
         - ssl_cert_file=/run/pandokos-tls/server.pem
         - -c
         - ssl_key_file=/run/pandokos-tls/server.key
       volumes:
         - ../../local/testdb/tls:/tls:ro
   ```

3. `test/db/pgbouncer.ini`: add the line `server_tls_sslmode = disable` after `pool_mode`.

### A6. Check of the test database (do not skip)

```
make db-down ; make db-up
. ./local/testdb/env
docker compose -f test/db/compose.yaml exec -T postgres psql -U postgres -Atc "SHOW ssl"       # on
docker compose -f test/db/compose.yaml exec -T postgres sh -c 'psql "host=127.0.0.1 user=postgres sslmode=require" -Atc "SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()"'   # t
make test          # the suite of main still passes: plain connections and the pooler work as before
```

If PostgreSQL does not start, or `SHOW ssl` is not `on`, or the existing suite fails through
the pooler: stop and ask, and attach
`docker compose -f test/db/compose.yaml logs --tail 100`. Do not change `database.tls.mode`
rules, do not make the key world-readable, and do not add a second PostgreSQL service on your
own (design risk R-3).

### A7. `test/dbtest` — the CA file

In `gate.go` add the constants `EnvDatabaseCAFile = "PANDOKOS_TEST_DATABASE_CA_FILE"` and
`EnvNoDatabaseTLS = "PANDOKOS_TEST_NO_DATABASE_TLS"`; in `dbtest.go` add

```go
// CAFile returns the file with the certificate authority of the test PostgreSQL. It fails or
// skips the test according to the gate (missing variable: fail; opt-out variable outside CI: skip).
func CAFile(tb testing.TB) string
```

using the existing `gate` function. Extend T-119 with the new pair of variables.

### A8. `.gitignore`, `.dockerignore`

Nothing to add: `local/` is already ignored. Verify `git status --short` shows no file under
`local/`.

## Part B — Tests first (commit: `test: sprint 1.3 tests, failing`)

Write every test file of this part before any file of part C. Then run
`go vet ./... ; make test` and save the output: every package touched must **fail** (build
errors for the packages that do not exist yet). Paste the output in the pull request. This is
the tests-first record.

General rules, in addition to those of the 1.1 and 1.2 plans:

- Test function names are exactly those of §9 (the part after the dot).
- A test that needs a key calls `authn.Generate` or creates it through the API. To assert
  absence it calls `testutil.AssertAbsent(tb, where, text, testutil.KeyForms(key)...)`, where
  `KeyForms` (new, in `test/testutil`) returns: the key, the 59 characters after the second
  underscore, the 52-character secret, and the SHA-256 of the 32 secret bytes in lower-case
  hexadecimal, standard base64 and URL-safe base64.
- Tests over "every operation" range over `adminapi.Operations`; they keep a table from
  operation ID to "how to make a valid call" (path parameters and body). An operation missing
  from that table must fail the test, not be skipped.
- Bounds: poll with `testutil.Eventually` and the deadline of the test's row in §9.

### B1. `test/testutil` — additions

```go
// StaticAuthenticator is a gateway.Authenticator for in-process tests: the map key is the
// presented key. Any other credential is authz.ErrUnauthenticated.
func StaticAuthenticator(principals map[string]authz.Principal) gateway.Authenticator

// InferencePrincipal returns an active service-account principal of a random tenant with the
// role inference, for tests that only need a caller that is allowed.
func InferencePrincipal() authz.Principal

// NewKey returns a new virtual key (authn.Generate, kind tenant) as a string.
func NewKey(tb testing.TB) string

// KeyForms returns the forms of a key that must never be found in an output.
func KeyForms(key string) []string
```

Delete `fixtures.CanaryAccessToken`; `fixtures.Canaries()` returns three values; delete the
length assertion in `fixtures_test.go`.

### B2. `test/apitest/` (new) — package `apitest`

```go
type API struct {
    DB     *dbtest.Database
    URL    string               // of the httptest server in front of admin.New
    Logs   *testutil.LogBuffer  // the handler's logger, level debug
    Admin  *store.DB            // the admin pool the handler uses
    // unexported: issued keys
}

func New(tb testing.TB) *API                         // migrated database, handler, bootstrap key
func NewOn(tb testing.TB, d *dbtest.Database) *API   // the same on a given database (pooled view)
func (a *API) PlatformKey() string                   // the bootstrap key
func (a *API) Client(key string) *adminapi.Client
func (a *API) Tenant(tb testing.TB, name string) (t adminapi.Tenant, adminKey string)
func (a *API) Do(tb testing.TB, method, path, key string, body any, hdr http.Header) *Response // raw request
func (a *API) Secrets() []string                     // KeyForms of every key issued through a
type Response struct { Status int; Header http.Header; Body []byte }
```

`New` creates the bootstrap key by calling the same function `init` uses (step C13 exports it
from `internal/admin` as `admin.Bootstrap`), not by SQL.

### B3. Unit tests

Files and tests: `internal/authz/authz_test.go` (T-201 to T-204);
`internal/authn/key_test.go` (T-205 to T-207), `internal/authn/cache_test.go` (T-208);
`internal/audit/audit_test.go` (T-209); `internal/apigen/apigen_test.go` (T-210, T-211: the
defective documents are small YAML strings in the test, each a copy of a minimal valid document
with one defect); `internal/adminapi/adminapi_test.go` (T-212);
`internal/config/config_test.go` (T-213 to T-215 and the changed tests);
`cmd/pandokos/main_test.go` (T-216); `internal/cli/cli_test.go` (T-217 to T-219);
`internal/requestid/requestid_test.go` (32 hexadecimal characters, distinct, header name
`X-Pandokos-Request-Id` equal to `family.HeaderRequestID`).

For T-213 build certificates with `testcert`: a valid pair; a pair whose key belongs to another
certificate; a certificate with `notAfter` one hour ago; one with `notBefore` one hour ahead;
one that expires in 10 days (warning).

### B4. Database tests

`internal/store/queries/queries_db_test.go` (package `queries_test`, T-230);
`internal/store/store_db_test.go` (T-231, T-265, T-266: additions);
`internal/authn/authn_db_test.go` (package `authn_test`: T-260 to T-263);
`internal/admin/*_db_test.go` (package `admin_test`, on `apitest`: T-232 to T-239, T-264,
T-281 to T-284); `test/isolation/api_test.go` (T-270 to T-275),
`test/isolation/audit_test.go` (T-280), `test/isolation/keys_test.go` (T-286); changes to
`lint_test.go`, `negative_test.go`, `spec.go` expectations (T-140 to T-144).

Details that are easy to get wrong:

- **T-235, forced database error**: through `apitest.DB.Super`, run
  `REVOKE SELECT ON pandokos.tenant FROM pandokos_admin`, call `listTenants`, expect 500
  `internal`, and assert that the body contains none of `permission`, `tenant`, `pandokos`,
  `SQLSTATE`, `42501`. Then call with the database refusing connections
  (`SetAcceptingConnections(false)`) and expect 503.
- **T-262**: build the gateway authenticator directly (`authn.New` with `CacheTTL: 2 *
  time.Second` on the gateway pool) and never call `Invalidate`. Authenticate once (fills the
  cache); revoke through the API; authenticate again at once and expect success; then
  `Eventually` within 2500 ms expect `authz.ErrUnauthenticated` with reason `revoked`.
- **T-263**: `CacheTTL: time.Minute`, a `store.Listener` on the direct address wired to
  `Invalidate` and `Flush`; wait for the log line `cache listener connected` before the first
  authentication; deadline 2 s.
- **T-270**: compare responses with a helper that deletes the headers `X-Pandokos-Request-Id`
  and `Date` and the JSON member `request_id`, then compares status, the remaining headers
  (sorted) and the remaining body bytes. A random tenant ID is made with the database function
  `pandokos.uuid_v7()` through the superuser connection, so that it has the same shape as a
  real one.
- **T-282**: the failing trigger is
  `CREATE FUNCTION pandokos.zz_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit is down'; END $$;`
  with `BEFORE INSERT` triggers named `a_fail` on both audit tables (the name sorts before
  `audit_event_stamp`), created through the superuser connection after the fixtures exist.
  Snapshot every table of the schema as JSON before and after each call.
- **T-284**: the limiter's clock is the `Now` of `admin.Options`; fix it so that no token is
  refilled during the burst.
- **T-280**: the superuser's `UPDATE` and `DELETE` must fail with SQLSTATE `23001`; its
  `TRUNCATE` too.

### B5. In-process and end-to-end tests

`internal/gateway/gateway_test.go` (T-267 and the changed tests),
`internal/server/server_test.go` (T-240 to T-243), `test/e2e/e2e_test.go` (T-244, T-245, T-250
to T-252, T-290 to T-292 and the changed tests), `test/container/container_test.go` (T-72),
`test/sdk/sdk_test.go` (changed), `test/repo/repo_test.go` (T-R12 to T-R16, T-R19).

- e2e helpers: `bootstrap(t, p)` runs `<binary> init --key-out <temp file>` with the process's
  environment and returns the file; `cli(t, p, keyFile, args...)` runs the binary with
  `PANDOKOS_CLI_SERVER=http://<admin address>` and `PANDOKOS_CLI_KEY_FILE`. The admin address
  comes from the `listening` log line with `listener` `admin`; the test configuration sets
  `listeners.admin.address: 127.0.0.1:0`.
- **T-245**: environment from `dbtest.Env` with `PANDOKOS_DATABASE_TLS_MODE=verify-full` and
  `PANDOKOS_DATABASE_TLS_CA_FILE=dbtest.CAFile(t)`; the address must be the direct one,
  `127.0.0.1:55432`. The negative cases: a CA file from `testutil.WriteCA`; and
  `PANDOKOS_DATABASE_ADDRESS=localhost:55432`.
- **T-291**: three sub-tests as in §9; in the two "notification" sub-tests set
  `cache.principal_ttl` to `60s` and the deadline to 2 s; in the third set it to `2s` and the
  deadline to 2500 ms, and assert the log line `cache invalidation unavailable`.
- **T-R12**: use `go/parser` and `go/ast`. For each call expression whose function is a
  selector named `Exec`, `Query`, `QueryRow`, `SendBatch`, `CopyFrom` or `Prepare`: (a) the file
  must be under `internal/store/`; (b) the argument at index 1 must be a `*ast.BasicLit` of kind
  `STRING`, or an identifier declared in the same package as a `const` whose value is a string
  literal or a `+` of string literals. Skip `_test.go` files and files whose first comment line
  contains `Code generated`. Exceptions, listed by file and call in the test with a reason:
  `internal/store/migrate/apply.go` executes a migration file's content (a build input, not a
  built string). The test also parses three synthetic sources (concatenation with a variable,
  `fmt.Sprintf`, a `var`) and asserts that each is reported.
- **T-R13**: run `go list -deps -f '{{.ImportPath}}' ./internal/<pkg>` with `os/exec` and
  inspect the list.

## Part C — Implementation

One commit per step, with the message given. After each step: `go build ./...`, `go vet ./...`
and the tests named in the step pass; tests of later steps still fail. Do not start a step
before the check of the previous one passes.

Exception: steps C10 to C13 change types that the next step's package uses (the configuration,
`gateway.Options`, `server.New`). For those four steps the check is the package's own tests
(`go test ./internal/config/`, and so on); `go build ./...` and `go vet ./...` must pass again
at the end of C13.

### C1. `internal/requestid/` — `feat: request id package`

```go
// Header is the response header that carries the request ID on both listeners.
const Header = "X-Pandokos-Request-Id"

// New returns 16 random bytes as 32 lower-case hexadecimal characters.
func New() string
```

`gateway.NewRequestID` becomes `func NewRequestID() string { return requestid.New() }`; nothing
else in the gateway changes in this step. Check: `go test ./internal/requestid/ ./internal/gateway/`.

### C2. `internal/authz/` — `feat: permission catalogue, roles, principal, decision`

One file `authz.go`, exactly the API of §5.6: the constants, `Catalogue`, `Lookup`, `Roles`,
`RoleByName`, `Principal`, `Effective`, `Target`, `Decision`, `Decide` (the five rules, in
order), the context functions (an unexported key type), the two errors. Descriptions of
permissions are the "Allows" column of the catalogue table. `Roles()` builds new slices on
every call. Check: T-201 to T-204.

### C3. Migration 0002 and the lint — `feat: migration 0002`

1. Create `internal/store/migrations/sql/0002_identity_audit.sql` with exactly the content of
   appendix B of the design; run `make migrations-sum`. The line of `0001_init.sql` in
   `migrations.sum` must not change.
2. `test/isolation/spec.go`: add `AppendOnly []string` to `Spec`; update `DefaultSpec()` to
   §6.1: the three global tables, the grants matrix of §5.10 (column lists sorted, as in 1.2;
   `service_account` loses `DELETE`), the three fixtures, `SecurityDefiner`, `AppendOnly`.
   Fixture SQL:

   ```sql
   -- role_assignment
   WITH sa AS (INSERT INTO pandokos.service_account (tenant_id, name)
               VALUES ($1::uuid, 'sa-' || substr(md5(random()::text), 1, 12)) RETURNING id)
   INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role)
   SELECT $1::uuid, sa.id, 'inference' FROM sa RETURNING id::text
   -- virtual_key
   WITH sa AS (INSERT INTO pandokos.service_account (tenant_id, name)
               VALUES ($1::uuid, 'sa-' || substr(md5(random()::text), 1, 12)) RETURNING id)
   INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash)
   SELECT $1::uuid, sa.id, 't' || translate(substr(md5(random()::text), 1, 16), '0189', 'ghij'),
          sha256(random()::text::bytea) FROM sa RETURNING id::text
   -- audit_event
   INSERT INTO pandokos.audit_event (tenant_id, actor_type, action, resource_type, outcome)
   VALUES ($1::uuid, 'platform', 'test.fixture', 'test', 'success') RETURNING id::text
   ```

3. `test/isolation/lint.go`: rules L-17 and L-18 of §6.1. L-17 reads `pg_trigger` (not
   internal, `tgenabled <> 'D'`) joined to `pg_proc` for the function name, and decodes
   `tgtype` (bit 1 row-level, bit 2 before, bit 8 delete, bit 16 update, bit 32 truncate); the
   privileges come from the data the L-12 code already reads. L-18 reads `pg_proc.proconfig`
   and looks for the element `search_path=pg_catalog, pg_temp`.
4. `test/dbtest`: nothing (migrations are embedded).

Check: `go test ./internal/store/... ./test/isolation/` — T-106, T-120 to T-127, T-140 to
T-151 pass with the changes of §9 (T-144: the `DELETE` is refused), and T-280, T-286 pass. If
T-143 fails for a new table, the defect is in the fixture or the specification, not in the
migration: the migration is not edited (rule 6).

### C4. Query files and `sqlc` — `feat: sqlc queries`

1. Create the five files of appendix C under `internal/store/queries/sql/`.
2. Run `make generate` (the `apigen` half fails until C8: for this step run only
   `CGO_ENABLED=0 GOTOOLCHAIN=go$(cat .go-version) go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate`).
   The first run downloads and builds `sqlc`; it takes a few minutes.
3. Expected result: `internal/store/queries/` holds `db.go`, `models.go` and one `*.sql.go` per
   query file, package `queries`, with `func New(db DBTX) *Queries` and 28 methods named as the
   `-- name:` lines. Add `internal/store/queries/doc.go` by hand with the package comment
   (`// Package queries is generated by sqlc from sql/*.sql; see sqlc.yaml. Do not edit the generated files.`).
4. Inspect the generated signatures (`go doc ./internal/store/queries`). They must use only
   `string`, `*string`, `bool`, `int32`, `int64`, `[]string`, `[]byte`, `time.Time`,
   `*time.Time` in parameters and results. **What you may adjust, and nothing else:**
   - if a parameter or column comes out as a `pgtype` type or `interface{}`: add or correct a
     cast in the query file (the rule is that every parameter and every computed column has
     one), or add an override of the same form to `sqlc.yaml`; record the change in the pull
     request;
   - if `sqlc` rejects the *syntax of a query file*: correct the query without changing what it
     selects or changes, and record it.
   **What is a question to raise, not to solve:** `sqlc` cannot read a migration file; a query
   needs a different result to compile; a type cannot be made plain by a cast or an override.
   Do not edit a migration, do not add a separate schema file for `sqlc`, and do not write the
   package by hand (design risk R-1).
5. `store.Tx` already has the three methods of the generated `DBTX` interface. If `sqlc`
   generates a `DBTX` with more methods (for example `CopyFrom`, because of a query
   annotation), a query file is wrong: none of the 28 uses such an annotation.

Check: `go build ./...`; T-230 is completed in C5 (it needs `Unscoped`).

### C5. `internal/store/` — `feat: unscoped and provisioning transactions, listener, error classes`

1. `tx.go`: `Unscoped` (the existing `inTx` with no setting: refactor `inTx` to take a slice of
   setting pairs, possibly empty); `Provision` (admin role only, else `ErrNotAdmin`; sets the
   platform scope; passes `bind`, which checks non-zero and first call, then runs the same
   `set_config` statement for `app.tenant_id` inside the transaction); `ErrNotAdmin`,
   `ErrNoRows`.
2. `errors.go` (new): `ConstraintViolation` (SQLSTATE `23505` unique, `23514` check, `23503`
   foreign_key, `23001` restrict, `23502` not_null; the constraint name from the error),
   `IsUnavailable` (true for: `context.DeadlineExceeded`, `context.Canceled`, a
   `*pgconn.ConnectError`, an error for which `pgconn.SafeToRetry` is true or
   `pgconn.Timeout` is true, `io.EOF` / `io.ErrUnexpectedEOF`, a `net.Error`, and SQLSTATE
   classes `08`, `53`, `57`), `SQLState`.
3. `listen.go` (new): the listener of §5.8. Connection from
   `connConfig(o.Conn, "pandokos-"+role+"-listen")` with `pgx.ConnectConfig`; `LISTEN
   pandokos_cache` is a string literal. The loop:

   ```
   for ctx is not done:
       connect (timeout: the options' connect timeout); on error: log disconnected, backoff, continue
       Exec LISTEN; OnReset(); log connected
       healthy := probe()            // Exec the pg_notify statement with the nonce as a parameter,
                                     // then WaitForNotification until the nonce or 5 s
       if !healthy: log "cache invalidation unavailable" (once until a probe succeeds),
                    close, OnReset(), backoff, continue
       reset backoff
       loop:
           n, err := WaitForNotification(ctx with 30 s timeout)
           timeout  -> if !probe(): break loop
           error    -> break loop
           payload starting with "probe:" -> ignore
           else     -> OnNotify(payload)
       close; OnReset(); log disconnected (unless ctx is done); backoff
   ```

   Notifications that arrive while a probe waits are delivered to `OnNotify`, not dropped. The
   connection is closed with a 5 s limit when `ctx` ends.
4. `doc.go`: add one sentence on the listener being the only session state.

Check: T-231, T-265, T-266, T-230 (complete it now), and the 1.2 store tests.

### C6. `internal/authn/` — `feat: key format, authenticator, principal cache`

- `key.go`: `Generate`, `Parse`, `Pattern`, the encoding. Layout of a key and of the checksum
  input as in §5.7. `Parse` checks, in order: length 81; prefix; the kind letter; the two
  separators at index 3 and 21; the alphabet of the three parts; the checksum; and returns the
  SHA-256 of the 32 decoded secret bytes. It allocates no error that contains the input.
- `cache.go`: the cache of §5.8 (two maps, the tenant index, the clock from the options).
- `authn.go`: `Options`, `New` (requires `DB`, `Logger`, at least one kind; `KindPlatform`
  requires the admin role's pool), `Authenticate` (§5.7, the two procedures), `Failure`
  (`Error()` returns `authn: ` + reason; `Unwrap()` returns `authz.ErrUnauthenticated`),
  `Invalidate` (payload forms of §5.8), `Flush`.
- A database error becomes `fmt.Errorf("%w: %s", authz.ErrUnavailable, store.SQLState(err))`:
  the driver's message is not wrapped.

Check: T-205 to T-208, T-260 to T-263.

### C7. `internal/audit/` — `feat: audit events`

The API of §5.11. `ActorOf`: type from the principal's kind, `ID`, `Name`, `KeyID`. The two
writers call `InsertAuditEvent` and `InsertPlatformAuditEvent`. `Detail` nil is `{}`. Times in
a detail are RFC 3339 strings; a nil pointer is left out. Check: T-209 (T-280 already passes).

### C8. `internal/apigen/` and `internal/adminapi/` — `feat: API generator and generated API package`

1. Create `internal/apigen/main.go` with exactly the content of appendix A and
   `internal/adminapi/runtime.go` with exactly the content of appendix B.
2. Run `make generate`. It writes `internal/adminapi/adminapi.gen.go`: about 1730 lines, 21
   entries in `Operations`, a `Server` interface with 21 methods. Commit it.
3. `gofmt -l internal/apigen internal/adminapi` prints nothing; `go vet ./internal/...` passes.
   If the generator fails on the document, the document was not copied exactly (step A3): fix
   the copy, not the generator.
4. `internal/apigen/apigen_test.go` needs a function to call: `generate(path string) ([]byte,
   error)` exists in appendix A.

Check: T-210, T-211, T-212, and `make generate-check` (commit first, or it reports the new
files).

### C9. `internal/admin/` — `feat: administration API handlers`

Files:

| File | Content |
|---|---|
| `admin.go` | `Options`, `New`, the outer handler (request ID, headers, panic recovery, log line), the `Before` hook |
| `problem.go` | the table of §5.4 as one `map[adminapi.ProblemCode]struct{status int; title, detail string}`; `writeProblem`; the `Error` hook; the internal errors `errNotFound`, `errConflict`, `errInvalidState`, `errForbidden`, `errTenantState`, `invalidError` |
| `cursor.go` | encode and decode of the cursor (§5.4) |
| `secretjson.go` | `revealOnce(secret.Secret) string`, the one helper that calls `Reveal()` (rule 5) |
| `authfail.go` | the token bucket (capacity 60, one token per second, clock from the options) and the audit of authentication failures |
| `bootstrap.go` | `Bootstrap(ctx context.Context, db *store.DB, o BootstrapOptions) (BootstrapResult, error)`: step 2 of §5.14, used by `init` and by `test/apitest`. `BootstrapOptions`: `TTL`, `Description`, `Recover`, `Now`, and `BeforeCommit func(key secret.Secret, keyID string, expiresAt time.Time) error`, called inside the transaction after the audit record (an error rolls back). `BootstrapResult`: `Key secret.Secret`, `KeyID`, `ExpiresAt`. `ErrUsableKeyExists` |
| `me.go`, `tenants.go`, `service_accounts.go`, `virtual_keys.go`, `platform_keys.go` | the 21 methods of `adminapi.Server`, as the table of §5.12 |
| `convert.go` | from query rows to API types: times in UTC; `state` strings to the enumerations; `scopes` left nil when the row says not scoped; `roles` and `items` never nil |

Rules:

- A handler gets the principal with `authz.FromContext`; a missing principal is a panic (the
  router cannot call a handler without `Before`).
- The tenant of a tenant-scoped handler is the path parameter, parsed again with
  `tenant.ParseID`; `Before` has already compared it with the principal.
- Mapping of database errors uses `store.ConstraintViolation` with the constraint names of the
  migrations (`tenant_name_key`, `service_account_name_key`,
  `service_account_validity_order`, `virtual_key_key_id_key`,
  `virtual_key_service_account_fkey`) and `errors.Is(err, store.ErrNoRows)`. A constraint not
  in the table of §5.12 is `internal`.
- "In the future" means later than the handler's `Now()`.
- The request ID for audit events comes from the context value the outer handler sets.
- The `Error` hook logs `internal error` only for status 500, and `authentication failed` is
  logged in `Before`.

Check: T-232 to T-239, T-264, T-281 to T-284; `test/isolation` T-270 to T-275.

### C10. `internal/config/` — `feat: listener TLS, admin listener, cache and init configuration`

§5.2 in full: types, key table (order as stated), defaults, validation rules in the order of the
table, warnings, `PurposeInit`, the `PANDOKOS_CLI_` prefix, removal of the access token and of
its rules. `loadSecrets` keeps only the provider key. Loading a pair: read both files with a
size check, `tls.X509KeyPair`, parse the leaf with `x509.ParseCertificate` for the dates.
Update `docs/operations/configuration.md` in part E, not here (T-R5 fails until then).

Check: T-04 to T-16 as changed, T-113 to T-118 as changed, T-213 to T-215.

### C11. `internal/gateway/` and `internal/family/` — `feat: inference authenticates with virtual keys`

- `internal/family/family.go`: the three codes of §5.4 with status and message.
- `internal/gateway/handler.go`: `Options.Authenticator` replaces `AccessToken` (required);
  the `Authenticator` interface; the route table with its permission; step 4 as §5.13; the three
  log fields; `authentication failed` with `listener` `inference`.
- Tests of the package use `testutil.StaticAuthenticator`.

Check: `go test ./internal/gateway/ ./internal/family/...` (T-37 to T-57 as changed, T-267).

### C12. `internal/server/` — `feat: admin listener and TLS on both listeners`

§5.3. `server.New(Options)`; two `http.Server` values; `Listen` binds both; `serveOne` chooses
`ServeTLS` or `Serve`; the shutdown runs both `Shutdown` calls concurrently under one drain
context; the admin route function sends the two health paths to `health` (with the debug
`admin request` line) and the rest to `Options.Admin`.

Check: T-58 to T-61 as changed, T-240 to T-243.

### C13. `cmd/pandokos/` — `feat: init command, serve wiring`

- `serve.go`: the start sequence of §5.3. Listener options: the gateway user's `ConnOptions`
  with `Host` and `Port` replaced by `DirectHost` and `DirectPort`; `OnNotify` =
  `gatewayAuth.Invalidate`, `OnReset` = `gatewayAuth.Flush`. The bootstrap warning: a goroutine
  with a one-hour ticker, first run at once.
- `init.go`: §5.14. Flags with the standard `flag` package; `--ttl` parsed with
  `time.ParseDuration`. The key is written to `--key-out` **before** the transaction commits
  (in `BootstrapOptions.BeforeCommit`), so that a file error leaves no key behind; if the
  commit then fails, the file is removed.
- `configcheck.go`: `--for init`. `usage.go`: the text of §5.1. `main.go`: the cases `init`,
  `whoami`, `tenant`, `service-account`, `key`, `platform-key`. `client.go`: one function that
  calls `cli.Run`.

Check: T-62 to T-64 as changed, T-216; `make build && ./bin/pandokos help`.

### C14. `internal/cli/` — `feat: API commands`

§5.15. One file per command group plus `cli.go` (`Run`, connection flags, client construction,
output helpers, name resolution) and `commands.go` (the table
`var commands = []command{{words: []string{"tenant", "list"}, operation: "listTenants", run: …}, …}`
that T-219 reads). The key is read once per command with `secret.ReadFile`; `Client.Key`
returns `Reveal()` (rule 5). `Client.OnResponse` looks for `X-Pandokos-Warning`.

Check: T-217 to T-219, T-R13.

### C15. Everything together — `test: end-to-end, container and SDK suites pass`

`test/e2e`, `test/container`, `test/sdk`, `test/repo` as changed and added. `Dockerfile`:
`EXPOSE 8080 8081`. Run the whole of part F up to `make test`; fix defects in the code of the
step they belong to, with a new commit that names the step.

## Part D — CI and release (commit: `build: generation check in CI, API document in the release`)

### D1. `.github/workflows/ci.yml`

Add a job after `lint`, with the same pinned actions the other jobs use (copy the two `uses:`
lines from the `vuln` job; do not look up new SHAs):

```yaml
  generate:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@<as in the vuln job> # <same comment>
        with:
          persist-credentials: false
      - uses: actions/setup-go@<as in the vuln job> # <same comment>
        with:
          go-version-file: .go-version
      - run: make generate-check
```

Nothing else changes: `test` and `image` already run `test/db/up.sh` after `setup-go`.

### D2. `.github/workflows/release.yml`

In the job `publish`: add, as the first step, the checkout step of the other jobs of that file
(same pinned `uses:` line, `persist-credentials: false`), and append
`api/openapi/admin.v1.yaml` to the files of `gh release create`, after `sbom.spdx.json`. The
download step must keep writing `sbom.spdx.json` into the working directory. T-R6 and T-R16
must pass.

### D3. Verification of part D

`go test ./test/repo/` passes (T-R1: every `uses:` pinned). The `generate` job is green on the
pull request; note its duration in the pull request.

## Part E — Documentation and decision records (commit: `docs: sprint 1.3 documentation and ADRs`)

### E1. New pages

- `docs/operations/keys.md`: what a platform key and a virtual key are; the format and the
  scanner pattern (`authn.Pattern`, verbatim); `pandokos init`, its output, `--key-out`,
  `--recover`, the bootstrap warnings; a first-run walk-through with the exact commands of
  T-290; rotation with two active keys; the bound table of §5.8, verbatim, with
  `cache.principal_ttl` and `database.direct_address`; what suspension does; that the first
  admin key of a tenant passes through the platform admin (R-8); bearer keys and TLS (SEC-02:
  a key that is seen can be replayed until it is revoked or expires).
- `docs/operations/tls.md`: the listener keys and rules of §5.2; that certificates are read at
  start; plain HTTP behind a proxy and `allow_plaintext`; TLS towards PostgreSQL (from
  `database.md`, which keeps a link) with `verify-full` and a private authority.
- `docs/reference/admin-api.md`: the conventions of §5.4, the problem table, pagination, the
  accepted subset of §5.5, where the document is (`api/openapi/admin.v1.yaml`, attached to each
  release), and the CLI command table of §5.15.
- `docs/reference/permissions.md`: the two tables of §5.6 and the decision rules.

### E2. Changed pages

- `docs/operations/configuration.md`: new and removed keys, the purposes, the `PANDOKOS_CLI_`
  variables (T-R5).
- `docs/operations/container.md`: port 8081; the admin listener is on loopback inside the
  container by default; `docker exec <container> /pandokos init` and API commands with a mounted
  key file; how to expose the admin API (address plus TLS or `allow_plaintext`).
- `docs/operations/database.md`: the grants of 0002 in one paragraph; `database.direct_address`
  with a pooler; that forced row-level security hides audit rows from the owner too.
- `docs/architecture/data-model.md`: rule 7 gains "or, while a tenant is being created, both";
  the lookup paragraph is replaced by §5.10 ("crosses the policy for one fact only");
  `platform_key` and `virtual_key_directory` join the table of groups; the role table: gateway
  cannot read platform keys; admin cannot read audit yet.
- `docs/architecture/overview.md`: §7 principal cache row (lifetime `cache.principal_ttl`;
  the admin API does not cache); §10 Admin API row ("generated by an in-repository generator");
  §11 layout (the new directories of design §4).
- `docs/architecture/security-model.md`: the paragraph "Credentials issued by Pandokos" with the
  format of §5.7.
- `SECURITY.md`: one sentence that leaked keys match the pattern of `docs/operations/keys.md`.
- `README.md`, `CONTRIBUTING.md`: `make generate`; "never edit generated files"; the first-run
  commands in the README's status section.

### E3. Decision records

Five files in `docs/adr/`, status *proposed*, date 2026-11-12, in the template's four sections.
Content (write it out in full sentences; keep each to one page):

- **0028. Administration API: in-repository generator and conventions.** Refines 0017.
  *Context*: 0017 wants generated interfaces, validation and client; 0018 wants the standard
  library first; the facts of D-1 about `oapi-codegen` and its runtime module, with versions and
  the date. *Decision*: `internal/apigen`; the strict subset; generated output uses only the
  standard library and is committed; CI regenerates; the conventions of D-2 and D-20;
  `x-pandokos-permission` mandatory. *Consequences*: the project owns about 800 lines; a new
  construct is generator work; the document stays valid OpenAPI 3.1 for other generators
  (console). *Alternatives*: `oapi-codegen` (runtime module, no validation, no permission);
  `ogen`; hand-written handlers checked against the document by a test.
- **0029. Static SQL: `sqlc`, text-mode rules, and a test against built SQL.** Refines 0004 and
  0027. *Decision*: D-3, D-4; `sqlc` pinned in the Makefile, run without cgo with the release
  toolchain, never in `go.mod`; the rules for query files of §5.9; the generated package lives
  under `internal/store`; T-R12. *Consequences*: a parameter without a cast fails at run time,
  so every statement has a database test (T-230); list queries with several orders are written
  with `CASE`. *Alternatives*: hand-written query functions; a query builder (builds strings).
- **0030. Keys: format, storage, lookup, platform keys, revocation bound.** Refines 0005, 0004
  and 0003. *Decision*: D-5, D-6, D-7, D-10, D-11, D-12 and the bound table. *Consequences*:
  two round trips on a cache miss; the listener needs a direct address behind a transaction
  pooler; `init` needs the admin database password on the host. *Alternatives*: the tenant ID
  inside the key (no directory, longer key, tenant visible in the key); a policy for the owner
  on `virtual_key`; one key table with a nullable tenant; a cache on the admin API.
- **0031. Authorization core and tenant provisioning.** Refines 0007 and 0027. *Decision*: D-8,
  D-9, the decision rules of §5.6, default deny, 404 for another tenant; `store.Provision` as
  the single transaction with both settings. *Consequences*: the platform admin sees the first
  key of a tenant; a platform admin cannot repair a tenant until support actions exist.
  *Alternatives*: a platform permission inside tenants; provisioning in several transactions;
  self-registration of tenants (excluded by IAM-01).
- **0032. Audit log, first version.** Refines 0013. *Decision*: D-13; the columns; the action
  table of §5.11 as the vocabulary; the detail guard. *Consequences*: no reading, chain or
  retention until 6.2; changing these tables later needs care under forced row-level security.
  *Alternatives*: the hash chain now; partitions now; audit rows written by a database trigger
  (no actor, no request ID).

`docs/adr/0027-…md`: add a section `## History` with the line
`- 2026-11: sprint 1.3 adds the transaction with neither setting (Unscoped), the provisioning transaction with both (ADR 0031), and one LISTEN connection per gateway (ADR 0030).`
`docs/adr/README.md`: five rows.

### E4. Planning files

- This sprint's `sprint.md`: `status: in-progress` at the start of implementation; set to
  `done` by the review.
- `planning/epics/01-foundations/epic.md` is not changed.
- Open points O-1 to O-8 are copied into the pull request under "Questions for the maintainer",
  and the maintainer action of §10 under "Maintainer actions".

## Part F — Verification

Run all of these from the repository root before opening the pull request; paste the final
lines of each in the pull request description.

```
make db-up
git diff main -- go.mod go.sum                      # prints nothing
gofmt -l cmd internal test                          # prints nothing
go mod tidy -diff                                   # prints nothing
go build ./...
go vet ./...
make generate-check                                 # prints nothing after "go run" lines
golangci-lint config verify && golangci-lint run
make test                                           # go test -race -count=1 ./... with the database
( . ./local/testdb/env && export PANDOKOS_TEST_DATABASE_URL PANDOKOS_TEST_POOLER_ADDR PANDOKOS_TEST_DATABASE_CA_FILE && GOTOOLCHAIN=go$(cat .go-version) go test -race -count=1 ./... )
( . ./local/testdb/env && export PANDOKOS_TEST_DATABASE_URL PANDOKOS_TEST_POOLER_ADDR PANDOKOS_TEST_DATABASE_CA_FILE && go test -count=1 -v ./test/isolation/ ./internal/admin/ ./internal/authn/ ) | grep -c -- '--- SKIP'    # prints 0
( cd test/sdk && GOTOOLCHAIN=go$(cat .go-version) go vet ./... && GOTOOLCHAIN=go$(cat .go-version) go test -race -count=1 ./... )
make vuln
go test ./internal/authn/ -run '^$' -fuzz '^FuzzParse$' -fuzztime 30s
make build && ./bin/pandokos help
make image && make test-container
git grep -nE 'pdk_[tp][a-z2-7]{16}_[a-z2-7]{59}' ; echo "exit $?"      # exit 1: nothing found
make test-nodb
make db-down
```

Final checks against the design:

1. Every test name of §9 exists:
   `grep -rhoE 'func (Test|Fuzz)[A-Za-z0-9_]+' cmd internal test | sort` against the tables.
2. `grep -rn 'Reveal()' cmd internal --include='*.go' | grep -v _test.go` shows, beyond the
   call sites present on `main` minus the deleted access-token ones, exactly three new files:
   `internal/admin/secretjson.go`, `cmd/pandokos/init.go`, `internal/cli/cli.go`.
3. `grep -rnE '\.(Exec|Query|QueryRow)\(' cmd internal --include='*.go' | grep -v _test.go | grep -v '^internal/store/'`
   prints nothing.
4. `grep -rn 'WriteHeader(' internal/admin --include='*.go' | grep -v _test.go` shows only
   `problem.go` (and none for a success: success responses are written by `adminapi`).
5. `api/openapi/admin.v1.yaml` and `internal/store/migrations/sql/0002_identity_audit.sql`
   equal the appendices of the design; `internal/apigen/main.go`,
   `internal/adminapi/runtime.go` equal appendices A and B of this plan; the query files equal
   appendix C except for the changes recorded under step C4.
6. `git status --short` and `git diff main --stat` show no file under `local/`.
7. Go through the definition of done (§10) line by line and tick each box in the pull request,
   with the evidence.

## What the implementer does not do

- Does not add a dependency, a table, a column, a grant, a permission, a role, an operation, a
  schema member, a problem code, a configuration key, a command, a flag or a log field beyond
  those named in the design.
- Does not edit generated files, the migration after step C3 is committed, or `0001_init.sql`.
- Does not add a way to read a key or a hash back: no `secret` in a list or get response, no
  hash in any response, log or audit detail, no debug switch.
- Does not add an endpoint that works without a credential, a default key, a test-only bypass
  in product code, or a flag that disables authentication, authorization, audit or certificate
  verification.
- Does not cache principals on the admin listener, lengthen a cache lifetime or a test deadline
  to make a bound pass, or make the listener's failure a start-up error.
- Does not add `serve --role`, audit reading, a hash chain, partitions, users, sessions, other
  roles, or anything of the "not in scope" table.
- Does not weaken a test, a linter, the vulnerability gate, the database gate or the generation
  check, and does not push tags or change repository settings.

## Appendix A — `internal/apigen/main.go`

The complete file (step C8). It was run against appendix A of the design with Go 1.25.0.

```go
// Command apigen generates internal/adminapi from the OpenAPI document of the administration
// API. It accepts a deliberate subset of OpenAPI 3.1 and fails on anything else, so that the
// document cannot say something the generated code does not enforce. The output imports only
// the standard library.
//
// Usage: go run ./internal/apigen -spec api/openapi/admin.v1.yaml -out internal/adminapi/adminapi.gen.go
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

type document struct {
	OpenAPI    string                `yaml:"openapi"`
	Info       map[string]any        `yaml:"info"`
	Security   []map[string][]string `yaml:"security"`
	Tags       []map[string]string   `yaml:"tags"`
	Paths      map[string]pathItem   `yaml:"paths"`
	Components struct {
		SecuritySchemes map[string]map[string]string `yaml:"securitySchemes"`
		Parameters      map[string]*parameter        `yaml:"parameters"`
		Responses       map[string]*response         `yaml:"responses"`
		Schemas         map[string]*schema           `yaml:"schemas"`
	} `yaml:"components"`
}

type pathItem struct {
	Parameters []*parameter `yaml:"parameters"`
	Get        *operation   `yaml:"get"`
	Post       *operation   `yaml:"post"`
	Patch      *operation   `yaml:"patch"`
	Put        *operation   `yaml:"put"`
	Delete     *operation   `yaml:"delete"`
}

type operation struct {
	OperationID string               `yaml:"operationId"`
	Tags        []string             `yaml:"tags"`
	Summary     string               `yaml:"summary"`
	Description string               `yaml:"description"`
	Permission  string               `yaml:"x-pandokos-permission"`
	Parameters  []*parameter         `yaml:"parameters"`
	RequestBody *requestBody         `yaml:"requestBody"`
	Responses   map[string]*response `yaml:"responses"`
}

type parameter struct {
	Ref         string  `yaml:"$ref"`
	Name        string  `yaml:"name"`
	In          string  `yaml:"in"`
	Required    bool    `yaml:"required"`
	Description string  `yaml:"description"`
	Schema      *schema `yaml:"schema"`
}

type mediaType struct {
	Schema *schema `yaml:"schema"`
}

type requestBody struct {
	Required bool                 `yaml:"required"`
	Content  map[string]mediaType `yaml:"content"`
}

type response struct {
	Ref         string               `yaml:"$ref"`
	Description string               `yaml:"description"`
	Content     map[string]mediaType `yaml:"content"`
}

// typeSpec is "string" or ["string", "null"].
type typeSpec struct {
	Name     string
	Nullable bool
}

func (t *typeSpec) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		t.Name = n.Value
		return nil
	}
	var list []string
	if err := n.Decode(&list); err != nil || len(list) != 2 || list[1] != "null" {
		return errors.New(`type must be a name or [name, "null"]`)
	}
	t.Name, t.Nullable = list[0], true
	return nil
}

// properties keeps the order of the document.
type properties struct {
	Names []string
	By    map[string]*schema
}

func (p *properties) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return errors.New("properties must be a mapping")
	}
	p.By = map[string]*schema{}
	for i := 0; i < len(n.Content); i += 2 {
		s := &schema{}
		if err := n.Content[i+1].Decode(s); err != nil {
			return err
		}
		p.Names = append(p.Names, n.Content[i].Value)
		p.By[n.Content[i].Value] = s
	}
	return nil
}

type schema struct {
	Ref                  string     `yaml:"$ref"`
	Type                 typeSpec   `yaml:"type"`
	Format               string     `yaml:"format"`
	Description          string     `yaml:"description"`
	Pattern              string     `yaml:"pattern"`
	Enum                 []string   `yaml:"enum"`
	Default              *string    `yaml:"default"`
	MinLength            *int       `yaml:"minLength"`
	MaxLength            *int       `yaml:"maxLength"`
	Minimum              *int       `yaml:"minimum"`
	Maximum              *int       `yaml:"maximum"`
	MinItems             *int       `yaml:"minItems"`
	MaxItems             *int       `yaml:"maxItems"`
	UniqueItems          bool       `yaml:"uniqueItems"`
	Items                *schema    `yaml:"items"`
	Properties           properties `yaml:"properties"`
	Required             []string   `yaml:"required"`
	AdditionalProperties *bool      `yaml:"additionalProperties"`
}

type generator struct {
	doc      document
	buf      bytes.Buffer
	patterns []string // regular expressions, in order of first use
}

func main() {
	spec := flag.String("spec", "api/openapi/admin.v1.yaml", "OpenAPI document")
	out := flag.String("out", "internal/adminapi/adminapi.gen.go", "output file")
	flag.Parse()
	src, err := generate(*spec)
	if err == nil {
		err = os.WriteFile(*out, src, 0o644) //nolint:gosec // generated source, not a secret
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "apigen:", err)
		os.Exit(1)
	}
}

func generate(path string) ([]byte, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the path is a build input
	if err != nil {
		return nil, err
	}
	g := &generator{}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true) // a construct outside the subset is an error
	if err := dec.Decode(&g.doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if g.doc.OpenAPI != "3.1.0" {
		return nil, errors.New("openapi must be 3.1.0")
	}
	body, err := g.run()
	if err != nil {
		return nil, err
	}
	src, err := format.Source(body)
	if err != nil {
		return nil, fmt.Errorf("generated code does not parse: %w", err)
	}
	return src, nil
}

func (g *generator) p(format string, args ...any) { fmt.Fprintf(&g.buf, format+"\n", args...) }

func refName(ref, kind string) (string, error) {
	prefix := "#/components/" + kind + "/"
	if !strings.HasPrefix(ref, prefix) {
		return "", fmt.Errorf("reference %q must start with %s", ref, prefix)
	}
	return strings.TrimPrefix(ref, prefix), nil
}

// resolve follows a reference to a component schema. name is "" for an inline schema.
func (g *generator) resolve(s *schema) (*schema, string, error) {
	if s == nil {
		return nil, "", errors.New("missing schema")
	}
	if s.Ref == "" {
		return s, "", nil
	}
	name, err := refName(s.Ref, "schemas")
	if err != nil {
		return nil, "", err
	}
	t, ok := g.doc.Components.Schemas[name]
	if !ok {
		return nil, "", fmt.Errorf("unknown schema %q", name)
	}
	if t.Ref != "" {
		return nil, "", fmt.Errorf("schema %q: a component schema cannot be a reference", name)
	}
	return t, name, nil
}

func (g *generator) re(pattern string) string {
	for i, p := range g.patterns {
		if p == pattern {
			return "re" + strconv.Itoa(i)
		}
	}
	g.patterns = append(g.patterns, pattern)
	return "re" + strconv.Itoa(len(g.patterns)-1)
}

func goName(s string) string {
	var b strings.Builder
	up := true
	for _, r := range s {
		if r == '_' || r == '-' {
			up = true
			continue
		}
		if up {
			b.WriteString(strings.ToUpper(string(r)))
			up = false
		} else {
			b.WriteRune(r)
		}
	}
	n := b.String()
	for _, a := range [][2]string{{"Id", "ID"}, {"Url", "URL"}} {
		if strings.HasSuffix(n, a[0]) {
			n = strings.TrimSuffix(n, a[0]) + a[1]
		}
	}
	return n
}

func enumConst(typ, v string) string {
	return typ + goName(strings.ReplaceAll(strings.ReplaceAll(v, "-", "minus_"), ":", "_"))
}

func comment(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// field describes how one property or parameter is represented in Go.
type field struct {
	goType   string
	kind     string // "string", "enum", "time", "int", "bool", "object", "strings", "objects"
	nullable bool
	s        *schema // resolved schema, for the constraints
	item     *schema // resolved item schema of an array
}

func (g *generator) fieldOf(s *schema, required bool) (field, error) {
	t, name, err := g.resolve(s)
	if err != nil {
		return field{}, err
	}
	f := field{s: t, nullable: t.Type.Nullable}
	if f.nullable && (name != "" || t.Type.Name != "string") {
		return f, errors.New(`only an inline string can be nullable`)
	}
	switch {
	case t.Type.Name == "object":
		if name == "" {
			return f, errors.New("an object must be a component schema")
		}
		f.kind, f.goType = "object", name
		if !required {
			f.goType = "*" + name
		}
	case t.Type.Name == "string" && len(t.Enum) > 0:
		if name == "" {
			return f, errors.New("an enum must be a component schema")
		}
		f.kind, f.goType = "enum", name
	case t.Type.Name == "string" && t.Format == "date-time":
		f.kind, f.goType = "time", "time.Time"
		if f.nullable {
			f.goType = "Nullable[time.Time]"
		} else if !required {
			f.goType = "*time.Time"
		}
	case t.Type.Name == "string" && t.Format == "":
		f.kind, f.goType = "string", "string"
		if f.nullable {
			f.goType = "Nullable[string]"
		}
	case t.Type.Name == "integer":
		f.kind, f.goType = "int", "int"
	case t.Type.Name == "boolean":
		f.kind, f.goType = "bool", "bool"
	case t.Type.Name == "array":
		it, itName, err := g.resolve(t.Items)
		if err != nil {
			return f, fmt.Errorf("items: %w", err)
		}
		f.item = it
		switch {
		case it.Type.Name == "object" && itName != "":
			f.kind, f.goType = "objects", "[]"+itName
		case it.Type.Name == "string" && len(it.Enum) == 0 && it.Format == "":
			f.kind, f.goType = "strings", "[]string"
		default:
			return f, errors.New("array items must be a component object or a plain string")
		}
	default:
		return f, fmt.Errorf("unsupported type %q with format %q", t.Type.Name, t.Format)
	}
	return f, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (g *generator) run() ([]byte, error) {
	var body bytes.Buffer
	if err := g.schemas(); err != nil {
		return nil, err
	}
	if err := g.operations(); err != nil {
		return nil, err
	}
	body.WriteString("// Code generated by internal/apigen from api/openapi/admin.v1.yaml. DO NOT EDIT.\n\n")
	body.WriteString("package adminapi\n\nimport (\n\t\"context\"\n\t\"net/http\"\n\t\"net/url\"\n\t\"regexp\"\n\t\"strconv\"\n\t\"time\"\n)\n\n")
	body.WriteString("var (\n\t_ = strconv.Itoa\n\t_ = time.Now\n\t_ = url.PathEscape\n)\n\n")
	if len(g.patterns) > 0 {
		body.WriteString("var (\n")
		for i, p := range g.patterns {
			fmt.Fprintf(&body, "\tre%d = regexp.MustCompile(%s)\n", i, strconv.Quote(p))
		}
		body.WriteString(")\n\n")
	}
	body.Write(g.buf.Bytes())
	return body.Bytes(), nil
}

func (g *generator) schemas() error {
	for _, name := range sortedKeys(g.doc.Components.Schemas) {
		s := g.doc.Components.Schemas[name]
		var err error
		switch {
		case s.Type.Name == "object":
			err = g.object(name, s)
		case s.Type.Name == "string" && len(s.Enum) > 0:
			g.enum(name, s)
		case s.Type.Name == "string" || s.Type.Name == "integer":
			// A named scalar: used through references, it has no Go type of its own.
		default:
			err = fmt.Errorf("unsupported component schema of type %q", s.Type.Name)
		}
		if err != nil {
			return fmt.Errorf("schema %s: %w", name, err)
		}
	}
	return nil
}

func (g *generator) enum(name string, s *schema) {
	if s.Description != "" {
		g.p("// %s: %s", name, comment(s.Description))
	} else {
		g.p("// %s is an enumeration of the administration API.", name)
	}
	g.p("type %s string\n", name)
	g.p("// Values of %s.", name)
	g.p("const (")
	for _, v := range s.Enum {
		g.p("%s %s = %q", enumConst(name, v), name, v)
	}
	g.p(")\n")
	g.p("// Valid reports whether v is a value of the enumeration.")
	g.p("func (v %s) Valid() bool {", name)
	g.p("switch v {")
	consts := make([]string, len(s.Enum))
	for i, v := range s.Enum {
		consts[i] = enumConst(name, v)
	}
	g.p("case %s:", strings.Join(consts, ", "))
	g.p("return true")
	g.p("}")
	g.p("return false")
	g.p("}\n")
}

func (g *generator) object(name string, s *schema) error {
	if s.AdditionalProperties == nil || *s.AdditionalProperties {
		return errors.New("an object must say additionalProperties: false")
	}
	required := map[string]bool{}
	for _, r := range s.Required {
		if _, ok := s.Properties.By[r]; !ok {
			return fmt.Errorf("required names %q, which is not a property", r)
		}
		required[r] = true
	}
	if s.Description != "" {
		g.p("// %s: %s", name, comment(s.Description))
	} else {
		g.p("// %s is a schema of the administration API.", name)
	}
	g.p("type %s struct {", name)
	var checks bytes.Buffer
	for _, pn := range s.Properties.Names {
		ps := s.Properties.By[pn]
		f, err := g.fieldOf(ps, required[pn])
		if err != nil {
			return fmt.Errorf("property %s: %w", pn, err)
		}
		tag := pn
		switch {
		case f.nullable:
			if required[pn] {
				return fmt.Errorf("property %s: a nullable property cannot be required", pn)
			}
			tag += ",omitzero"
		case !required[pn]:
			tag += ",omitempty"
		}
		if d := comment(ps.Description); d != "" {
			g.p("// %s", d)
		}
		g.p("%s %s `json:%q`", goName(pn), f.goType, tag)
		g.check(&checks, "v."+goName(pn), pn, f, required[pn])
	}
	g.p("}\n")
	g.p("var required%s = []string{%s}\n", name, quoteList(s.Required))
	g.p("// Validate returns the rules of the document that v breaks. A message never repeats a value.")
	g.p("func (v *%s) Validate() []FieldError {", name)
	g.p("var errs []FieldError")
	g.buf.Write(checks.Bytes())
	g.p("return errs")
	g.p("}\n")
	return nil
}

func quoteList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = strconv.Quote(s)
	}
	return strings.Join(q, ", ")
}

// check writes the validation of one value. expr is the Go expression, name the member name.
func (g *generator) check(w *bytes.Buffer, expr, name string, f field, required bool) {
	add := func(cond, msg string) {
		fmt.Fprintf(w, "if %s {\nerrs = append(errs, FieldError{Field: %q, Message: %q})\n}\n", cond, name, msg)
	}
	str := func(expr string, s *schema, guard string) {
		if s.Pattern != "" {
			add(guard+"!"+g.re(s.Pattern)+".MatchString("+expr+")", "must match "+s.Pattern)
		}
		if s.MinLength != nil {
			add(guard+"runeCount("+expr+") < "+strconv.Itoa(*s.MinLength), "length must be at least "+strconv.Itoa(*s.MinLength))
		}
		if s.MaxLength != nil {
			add("runeCount("+expr+") > "+strconv.Itoa(*s.MaxLength), "length must be at most "+strconv.Itoa(*s.MaxLength))
		}
	}
	switch f.kind {
	case "string":
		if f.nullable {
			str(expr+".Value", f.s, expr+".Set && !"+expr+".Null && ")
			return
		}
		guard := ""
		if !required {
			guard = expr + ` != "" && `
		}
		str(expr, f.s, guard)
	case "enum":
		guard := ""
		if !required {
			guard = expr + ` != "" && `
		}
		add(guard+"!"+expr+".Valid()", "must be one of "+strings.Join(f.s.Enum, ", "))
	case "int":
		if f.s.Minimum != nil {
			add(expr+" < "+strconv.Itoa(*f.s.Minimum), "must be at least "+strconv.Itoa(*f.s.Minimum))
		}
		if f.s.Maximum != nil {
			add(expr+" > "+strconv.Itoa(*f.s.Maximum), "must be at most "+strconv.Itoa(*f.s.Maximum))
		}
	case "strings":
		if f.s.MinItems != nil {
			add(expr+" != nil && len("+expr+") < "+strconv.Itoa(*f.s.MinItems), "must have at least "+strconv.Itoa(*f.s.MinItems)+" items")
		}
		if f.s.MaxItems != nil {
			add("len("+expr+") > "+strconv.Itoa(*f.s.MaxItems), "must have at most "+strconv.Itoa(*f.s.MaxItems)+" items")
		}
		if f.s.UniqueItems {
			add("!unique("+expr+")", "must not repeat an item")
		}
		if f.item.Pattern != "" || f.item.MinLength != nil || f.item.MaxLength != nil {
			fmt.Fprintf(w, "for _, item := range %s {\n", expr)
			str("item", f.item, "")
			fmt.Fprintf(w, "}\n")
		}
	}
}

type param struct {
	name string
	f    field
	p    *parameter
}

func (g *generator) param(p *parameter) (*parameter, error) {
	if p.Ref == "" {
		return p, nil
	}
	name, err := refName(p.Ref, "parameters")
	if err != nil {
		return nil, err
	}
	t, ok := g.doc.Components.Parameters[name]
	if !ok {
		return nil, fmt.Errorf("unknown parameter %q", name)
	}
	return t, nil
}

// successBody returns the status and the schema name of the one 2xx response, and checks that
// every other response is a problem.
func (g *generator) successBody(op *operation) (int, string, error) {
	status, name := 0, ""
	for _, code := range sortedKeys(op.Responses) {
		r := op.Responses[code]
		rn, err := refName(r.Ref, "responses")
		if err != nil {
			return 0, "", fmt.Errorf("response %s: %w", code, err)
		}
		t, ok := g.doc.Components.Responses[rn]
		if !ok {
			return 0, "", fmt.Errorf("response %s: unknown response %q", code, rn)
		}
		n, err := strconv.Atoi(code)
		if err != nil || n < 200 || n > 599 {
			return 0, "", fmt.Errorf("response %q: the status must be a number", code)
		}
		if n >= 300 {
			m, ok := t.Content["application/problem+json"]
			if !ok || len(t.Content) != 1 || m.Schema == nil || m.Schema.Ref != "#/components/schemas/Problem" {
				return 0, "", fmt.Errorf("response %s: an error response must be application/problem+json with the schema Problem", code)
			}
			continue
		}
		m, ok := t.Content["application/json"]
		if !ok || len(t.Content) != 1 || status != 0 {
			return 0, "", errors.New("an operation has exactly one 2xx response, with application/json content")
		}
		s, sn, err := g.resolve(m.Schema)
		if err != nil || sn == "" || s.Type.Name != "object" {
			return 0, "", fmt.Errorf("response %s: the schema must be a component object", code)
		}
		status, name = n, sn
	}
	if status == 0 {
		return 0, "", errors.New("an operation has exactly one 2xx response")
	}
	return status, name, nil
}

var rePermission = regexp.MustCompile(`^[a-z_]+:[a-z_]+$`)

func (g *generator) operations() error {
	type opInfo struct {
		id, method, path, permission, input, output, body string
		status                                            int
		bodyRequired                                      bool
		pathParams, queryParams                           []param
		doc                                               string
	}
	var ops []opInfo
	seen := map[string]bool{}
	for _, path := range sortedKeys(g.doc.Paths) {
		item := g.doc.Paths[path]
		for _, m := range []struct {
			method string
			op     *operation
		}{{"GET", item.Get}, {"POST", item.Post}, {"PATCH", item.Patch}, {"PUT", item.Put}, {"DELETE", item.Delete}} {
			if m.op == nil {
				continue
			}
			op := m.op
			fail := func(err error) error { return fmt.Errorf("%s %s: %w", m.method, path, err) }
			if !regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`).MatchString(op.OperationID) || seen[op.OperationID] {
				return fail(errors.New("operationId must be a unique lowerCamelCase name"))
			}
			seen[op.OperationID] = true
			if !rePermission.MatchString(op.Permission) {
				return fail(errors.New("x-pandokos-permission must name one permission (resource:action)"))
			}
			info := opInfo{id: op.OperationID, method: m.method, path: path, permission: op.Permission, doc: comment(op.Summary)}
			info.input = goName(op.OperationID) + "Input"
			var err error
			if info.status, info.output, err = g.successBody(op); err != nil {
				return fail(err)
			}
			for _, raw := range append(append([]*parameter{}, item.Parameters...), op.Parameters...) {
				p, err := g.param(raw)
				if err != nil {
					return fail(err)
				}
				f, err := g.fieldOf(p.Schema, true)
				if err != nil {
					return fail(fmt.Errorf("parameter %s: %w", p.Name, err))
				}
				switch {
				case p.In == "path" && p.Required && f.kind == "string" && f.s.Pattern != "" && strings.Contains(path, "{"+p.Name+"}"):
					info.pathParams = append(info.pathParams, param{p.Name, f, p})
				case p.In == "query" && !p.Required && (f.kind == "string" || f.kind == "enum" || f.kind == "int"):
					info.queryParams = append(info.queryParams, param{p.Name, f, p})
				default:
					return fail(fmt.Errorf("parameter %s: must be a required path string with a pattern, or an optional query string, enum or integer", p.Name))
				}
			}
			if n := strings.Count(path, "{"); n != len(info.pathParams) {
				return fail(errors.New("every {name} of the path needs a path parameter"))
			}
			if op.RequestBody != nil {
				m, ok := op.RequestBody.Content["application/json"]
				if !ok || len(op.RequestBody.Content) != 1 {
					return fail(errors.New("a request body is application/json"))
				}
				s, sn, err := g.resolve(m.Schema)
				if err != nil || sn == "" || s.Type.Name != "object" {
					return fail(errors.New("a request body must be a component object"))
				}
				for _, pn := range s.Properties.Names {
					if f, err := g.fieldOf(s.Properties.By[pn], false); err == nil && (f.kind == "object" || f.kind == "objects") {
						return fail(fmt.Errorf("request body %s: member %s is an object; request bodies are flat", sn, pn))
					}
				}
				info.body, info.bodyRequired = sn, op.RequestBody.Required
			}
			ops = append(ops, info)
		}
	}

	// Operation table.
	g.p("// Operations lists every operation of the document, sorted by path and method.")
	g.p("var Operations = []Operation{")
	for _, o := range ops {
		var pp []string
		for _, p := range o.pathParams {
			pp = append(pp, fmt.Sprintf("{Name: %q, Pattern: %s}", p.name, g.re(p.f.s.Pattern)))
		}
		g.p("{ID: %q, Method: %q, Path: %q, Permission: %q, Status: %d, HasBody: %t, PathParams: []PathParam{%s}},",
			o.id, o.method, o.path, o.permission, o.status, o.body != "", strings.Join(pp, ", "))
	}
	g.p("}\n")

	// Inputs.
	for _, o := range ops {
		g.p("// %s is the input of %s.", o.input, o.id)
		g.p("type %s struct {", o.input)
		for _, p := range o.pathParams {
			g.p("%s string // path", goName(p.name))
		}
		for _, p := range o.queryParams {
			g.p("%s %s // query; the zero value means absent", goName(p.name), p.f.goType)
		}
		if o.body != "" {
			g.p("Body %s", o.body)
		}
		g.p("}\n")
	}

	// Server interface.
	g.p("// Server is implemented by the handlers of the administration API: one method per operation.")
	g.p("type Server interface {")
	for _, o := range ops {
		g.p("// %s: %s %s. %s", goName(o.id), o.method, o.path, o.doc)
		g.p("%s(ctx context.Context, in %s) (*%s, error)", goName(o.id), o.input, o.output)
	}
	g.p("}\n")

	// Dispatch.
	g.p("func (h *handler) serve(op *Operation, w http.ResponseWriter, r *http.Request, params map[string]string) {")
	g.p("switch op.ID {")
	for _, o := range ops {
		g.p("case %q:", o.id)
		g.p("var in %s", o.input)
		for _, p := range o.pathParams {
			g.p("in.%s = params[%q]", goName(p.name), p.name)
		}
		var names []string
		for _, p := range o.queryParams {
			names = append(names, p.name)
		}
		if len(names) == 0 {
			g.p("if _, err := parseQuery(r); err != nil { h.fail(w, r, op, err); return }")
		} else {
			g.p("q, err := parseQuery(r, %s)", quoteList(names))
			g.p("if err != nil { h.fail(w, r, op, err); return }")
		}
		if len(o.queryParams) > 0 {
			g.p("var errs []FieldError")
		}
		for _, p := range o.queryParams {
			fn, s := "in."+goName(p.name), p.f.s
			switch p.f.kind {
			case "int":
				if s.Default != nil {
					g.p("%s = %s", fn, *s.Default)
				}
				g.p("if v, ok := q[%q]; ok {", p.name)
				g.p("n, err := strconv.Atoi(v)")
				g.p("if err != nil { errs = append(errs, FieldError{Field: %q, Message: \"must be an integer\"}) } else { %s = n }", p.name, fn)
				g.p("}")
			case "enum":
				if s.Default != nil {
					g.p("%s = %s(%q)", fn, p.f.goType, *s.Default)
				}
				g.p("if v, ok := q[%q]; ok { %s = %s(v) }", p.name, fn, p.f.goType)
			default:
				g.p("if v, ok := q[%q]; ok {", p.name)
				g.p("if v == \"\" { errs = append(errs, FieldError{Field: %q, Message: \"must not be empty\"}) }", p.name)
				g.p("%s = v", fn)
				g.p("}")
			}
			var checks bytes.Buffer
			g.check(&checks, fn, p.name, p.f, p.f.kind == "int")
			g.buf.Write(checks.Bytes())
		}
		if len(o.queryParams) > 0 {
			g.p("if len(errs) > 0 { h.fail(w, r, op, invalid(errs)); return }")
		}
		if o.body != "" {
			g.p("if err := decodeBody(r, h.hooks.MaxBodyBytes, %t, required%s, &in.Body); err != nil { h.fail(w, r, op, err); return }", o.bodyRequired, o.body)
			g.p("if errs := in.Body.Validate(); len(errs) > 0 { h.fail(w, r, op, invalid(errs)); return }")
		}
		g.p("out, err := h.server.%s(r.Context(), in)", goName(o.id))
		g.p("if err != nil { h.fail(w, r, op, err); return }")
		g.p("writeJSON(w, %d, out)", o.status)
	}
	g.p("}")
	g.p("}\n")

	// Client.
	for _, o := range ops {
		g.p("// %s calls %s %s. %s", goName(o.id), o.method, o.path, o.doc)
		g.p("func (c *Client) %s(ctx context.Context, in %s) (*%s, error) {", goName(o.id), o.input, o.output)
		expr := strconv.Quote(o.path)
		for _, p := range o.pathParams {
			expr = strings.Replace(expr, "{"+p.name+"}", `" + url.PathEscape(in.`+goName(p.name)+`) + "`, 1)
		}
		g.p("q := url.Values{}")
		for _, p := range o.queryParams {
			fn := "in." + goName(p.name)
			switch p.f.kind {
			case "int":
				g.p("if %s != 0 { q.Set(%q, strconv.Itoa(%s)) }", fn, p.name, fn)
			default:
				g.p("if %s != \"\" { q.Set(%q, string(%s)) }", fn, p.name, fn)
			}
		}
		bodyArg := "nil"
		if o.body != "" {
			bodyArg = "&in.Body"
		}
		g.p("var out %s", o.output)
		g.p("if err := c.do(ctx, %q, %s, q, %s, %d, &out); err != nil { return nil, err }", o.method, expr, bodyArg, o.status)
		g.p("return &out, nil")
		g.p("}\n")
	}
	return nil
}
```

## Appendix B — `internal/adminapi/runtime.go`

The complete file (step C8).

```go
// Package adminapi is the administration API as Go: the types, the server interface, the router
// and the client. adminapi.gen.go is generated from api/openapi/admin.v1.yaml by
// internal/apigen; this file is the hand-written part the generated code relies on. The package
// imports only the standard library.
package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Operation describes one operation of the document.
type Operation struct {
	ID         string // operationId
	Method     string
	Path       string // as in the document, with {name} segments
	Permission string // x-pandokos-permission
	Status     int    // status of the successful response
	HasBody    bool
	PathParams []PathParam
}

// PathParam is one {name} segment and the pattern its value must match.
type PathParam struct {
	Name    string
	Pattern *regexp.Regexp
}

// Nullable is a member that can be absent, null or a value (ServiceAccountUpdate.valid_until).
type Nullable[T any] struct {
	Set   bool // the member was present
	Null  bool // it was null
	Value T
}

// Null returns a member that is present and null.
func Null[T any]() Nullable[T] { return Nullable[T]{Set: true, Null: true} }

// Value returns a member that is present with a value.
func Value[T any](v T) Nullable[T] { return Nullable[T]{Set: true, Value: v} }

// IsZero makes the omitzero tag leave an absent member out.
func (n Nullable[T]) IsZero() bool { return !n.Set }

// MarshalJSON writes null or the value.
func (n Nullable[T]) MarshalJSON() ([]byte, error) {
	if n.Null || !n.Set {
		return []byte("null"), nil
	}
	return json.Marshal(n.Value)
}

// UnmarshalJSON records that the member was present, and whether it was null.
func (n *Nullable[T]) UnmarshalJSON(b []byte) error {
	n.Set, n.Null = true, false
	if string(bytes.TrimSpace(b)) == "null" {
		n.Null = true
		return nil
	}
	return json.Unmarshal(b, &n.Value)
}

// RequestError is a request that the router refuses before the Server sees it.
type RequestError struct {
	Status int          // 400, 404, 413 or 415
	Code   ProblemCode  // invalid_request, not_found, request_too_large, unsupported_media_type
	Fields []FieldError // for invalid_request, when single members are at fault
}

func (e *RequestError) Error() string { return "adminapi: " + string(e.Code) }

// ErrNoRoute is passed to Hooks.Error when no operation matches the method and the path.
var ErrNoRoute = &RequestError{Status: http.StatusNotFound, Code: ProblemCodeNotFound}

func invalid(fields []FieldError) *RequestError {
	return &RequestError{Status: http.StatusBadRequest, Code: ProblemCodeInvalidRequest, Fields: fields}
}

func invalidField(field, message string) *RequestError {
	return invalid([]FieldError{{Field: field, Message: message}})
}

// Hooks connect the generated router to the code that authenticates, authorizes and reports.
type Hooks struct {
	// Before runs when an operation matched, before the query and the body are read. It
	// authenticates and authorizes; the context it returns is the one the Server receives.
	Before func(ctx context.Context, r *http.Request, op *Operation, params map[string]string) (context.Context, error)
	// Error writes the response for err: ErrNoRoute (op is nil), an error of Before, a
	// *RequestError, or an error of the Server.
	Error func(w http.ResponseWriter, r *http.Request, op *Operation, err error)
	// MaxBodyBytes limits request bodies.
	MaxBodyBytes int64
}

type handler struct {
	server Server
	hooks  Hooks
}

// NewHandler returns the router of the administration API. All three hooks are required.
func NewHandler(s Server, h Hooks) http.Handler {
	if s == nil || h.Before == nil || h.Error == nil || h.MaxBodyBytes <= 0 {
		panic("adminapi: NewHandler needs a Server and all Hooks")
	}
	return &handler{server: s, hooks: h}
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, op *Operation, err error) {
	h.hooks.Error(w, r, op, err)
}

// Match finds the operation for a method and a path. A path parameter that does not match its
// pattern is not a match: a malformed identifier is the same as an unknown resource.
func Match(method, path string) (*Operation, map[string]string) {
	if !strings.HasPrefix(path, "/") {
		return nil, nil
	}
	segs := strings.Split(path[1:], "/")
next:
	for i := range Operations {
		op := &Operations[i]
		want := strings.Split(op.Path[1:], "/")
		if op.Method != method || len(want) != len(segs) {
			continue
		}
		params := map[string]string{}
		for j, w := range want {
			if strings.HasPrefix(w, "{") {
				name := w[1 : len(w)-1]
				for _, p := range op.PathParams {
					if p.Name == name && !p.Pattern.MatchString(segs[j]) {
						continue next
					}
				}
				params[name] = segs[j]
			} else if w != segs[j] {
				continue next
			}
		}
		return op, params
	}
	return nil, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	op, params := Match(r.Method, r.URL.Path)
	if op == nil {
		h.fail(w, r, nil, ErrNoRoute)
		return
	}
	ctx, err := h.hooks.Before(r.Context(), r, op, params)
	if err != nil {
		h.fail(w, r, op, err)
		return
	}
	h.serve(op, w, r.WithContext(ctx), params)
}

// parseQuery returns the query parameters. A parameter that the operation does not declare, or
// that is given twice, is an error: nothing a caller adds is silently ignored.
func parseQuery(r *http.Request, allowed ...string) (map[string]string, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, invalidField("query", "is not a valid query string")
	}
	out := map[string]string{}
	var errs []FieldError
	for name, vs := range values {
		known := false
		for _, a := range allowed {
			known = known || a == name
		}
		switch {
		case !known:
			errs = append(errs, FieldError{Field: "query", Message: "has a parameter this operation does not take"})
		case len(vs) != 1:
			errs = append(errs, FieldError{Field: name, Message: "must be given once"})
		default:
			out[name] = vs[0]
		}
	}
	if len(errs) > 0 {
		return nil, invalid(errs[:1])
	}
	return out, nil
}

// decodeBody reads a JSON object into dst. Unknown members, missing required members, a second
// JSON value and a body over the limit are errors. A body that is not required may be empty.
func decodeBody(r *http.Request, limit int64, bodyRequired bool, required []string, dst any) error {
	raw, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return &RequestError{Status: http.StatusRequestEntityTooLarge, Code: ProblemCodeRequestTooLarge}
		}
		return invalidField("body", "could not be read")
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		if bodyRequired {
			return invalidField("body", "is required")
		}
		return nil
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return &RequestError{Status: http.StatusUnsupportedMediaType, Code: ProblemCodeUnsupportedMediaType}
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil || members == nil {
		return invalidField("body", "must be a JSON object")
	}
	var errs []FieldError
	for _, name := range required {
		if v, ok := members[name]; !ok || string(v) == "null" {
			errs = append(errs, FieldError{Field: name, Message: "is required"})
		}
	}
	if len(errs) > 0 {
		return invalid(errs)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Field != "" {
			return invalidField(te.Field, "has the wrong type")
		}
		if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
			return invalidField(strings.Trim(name, `"`), "is not a member of this body")
		}
		return invalidField("body", "does not match the schema")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("adminapi: response of type %T cannot be encoded", v))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

func runeCount(s string) int { return utf8.RuneCountInString(s) }

func unique(items []string) bool {
	seen := map[string]bool{}
	for _, s := range items {
		if seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}

// ProblemError is an error response of the API, as the client returns it.
type ProblemError struct {
	Problem Problem
}

func (e *ProblemError) Error() string {
	return string(e.Problem.Code) + ": " + e.Problem.Detail + " (request " + e.Problem.RequestID + ")"
}

// Client calls the administration API. The zero value is not usable: set BaseURL and Key.
type Client struct {
	BaseURL    string               // for example https://127.0.0.1:8081, without a trailing slash
	HTTP       *http.Client         // http.DefaultClient when nil
	Key        func() string        // returns the key for the Authorization header
	OnResponse func(*http.Response) // optional: sees every response before its body is read
	UserAgent  string
}

const maxResponseBytes = 8 << 20

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any, want int, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("adminapi: encode the request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	u := strings.TrimSuffix(c.BaseURL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return fmt.Errorf("adminapi: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Key != nil {
		req.Header.Set("Authorization", "Bearer "+c.Key())
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL is not repeated in the message
		}
		return fmt.Errorf("adminapi: the server could not be reached: %w", err)
	}
	defer resp.Body.Close()
	if c.OnResponse != nil {
		c.OnResponse(resp)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("adminapi: read the response: %w", err)
	}
	if resp.StatusCode != want {
		var p Problem
		if json.Unmarshal(raw, &p) != nil || p.Code == "" {
			return fmt.Errorf("adminapi: unexpected response with status %d", resp.StatusCode)
		}
		return &ProblemError{Problem: p}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("adminapi: decode the response: %w", err)
	}
	return nil
}
```

## Appendix C — query files

The five files of `internal/store/queries/sql/` (step C4). With `sqlc.arg` and `sqlc.narg`
replaced by positional parameters, every statement was prepared and executed on PostgreSQL 16.15
against the schema of migrations 0001 and 0002.

### `tenants.sql`

```sql
-- Tenants. Every statement runs in platform scope (store.DB.Platform or Provision), except
-- GetTenantSelf, which runs in the tenant's own session.

-- name: CreateTenant :one
INSERT INTO pandokos.tenant (name, display_name)
VALUES (sqlc.arg(name)::text, sqlc.arg(display_name)::text)
RETURNING id, name, display_name, state, created_at, updated_at, state_changed_at;

-- name: GetTenant :one
SELECT id, name, display_name, state, created_at, updated_at, state_changed_at
  FROM pandokos.tenant
 WHERE id = sqlc.arg(id)::uuid;

-- name: GetTenantForUpdate :one
SELECT id, name, display_name, state, created_at, updated_at, state_changed_at
  FROM pandokos.tenant
 WHERE id = sqlc.arg(id)::uuid
   FOR UPDATE;

-- name: ListTenants :many
SELECT t.id, t.name, t.display_name, t.state, t.created_at, t.updated_at, t.state_changed_at
  FROM pandokos.tenant t
 WHERE (sqlc.arg(state)::text = '' OR t.state = sqlc.arg(state)::text)
   AND (sqlc.arg(name)::text = '' OR t.name = sqlc.arg(name)::text)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN t.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN t.id < sqlc.arg(after_id)::uuid
            WHEN 'name'        THEN (t.name, t.id) > (sqlc.arg(after_name)::text, sqlc.arg(after_id)::uuid)
            WHEN '-name'       THEN (t.name, t.id) < (sqlc.arg(after_name)::text, sqlc.arg(after_id)::uuid)
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN t.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN t.id END DESC,
          CASE WHEN sqlc.arg(sort)::text = 'name'        THEN t.name END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-name'       THEN t.name END DESC,
          CASE WHEN sqlc.arg(sort)::text = 'name'        THEN t.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-name'       THEN t.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: UpdateTenant :one
UPDATE pandokos.tenant
   SET name         = CASE WHEN sqlc.arg(set_name)::boolean THEN sqlc.arg(name)::text ELSE name END,
       display_name = CASE WHEN sqlc.arg(set_display_name)::boolean THEN sqlc.arg(display_name)::text ELSE display_name END
 WHERE id = sqlc.arg(id)::uuid
RETURNING id, name, display_name, state, created_at, updated_at, state_changed_at;

-- name: SetTenantState :one
UPDATE pandokos.tenant
   SET state = sqlc.arg(state)::text
 WHERE id = sqlc.arg(id)::uuid
RETURNING id, name, display_name, state, created_at, updated_at, state_changed_at;
```

### `service_accounts.sql`

```sql
-- Service accounts and their roles. Every statement runs in a tenant session; none names the
-- tenant in a WHERE clause on purpose: the policy does (TEN-04). tenant_id appears only where
-- a row is inserted.

-- name: CreateServiceAccount :one
INSERT INTO pandokos.service_account (tenant_id, name, description, valid_from, valid_until)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(name)::text, sqlc.arg(description)::text,
        sqlc.narg(valid_from)::timestamptz, sqlc.narg(valid_until)::timestamptz)
RETURNING id, tenant_id, name, description, state, valid_from, valid_until, created_at, updated_at;

-- name: GetServiceAccount :one
SELECT s.id, s.tenant_id, s.name, s.description, s.state, s.valid_from, s.valid_until, s.created_at, s.updated_at,
       COALESCE((SELECT array_agg(r.role ORDER BY r.role) FROM pandokos.role_assignment r
                  WHERE r.service_account_id = s.id), '{}')::text[] AS roles
  FROM pandokos.service_account s
 WHERE s.id = sqlc.arg(id)::uuid;

-- name: LockServiceAccount :one
SELECT s.id, s.state
  FROM pandokos.service_account s
 WHERE s.id = sqlc.arg(id)::uuid
   FOR UPDATE;

-- name: ListServiceAccounts :many
SELECT s.id, s.tenant_id, s.name, s.description, s.state, s.valid_from, s.valid_until, s.created_at, s.updated_at,
       COALESCE((SELECT array_agg(r.role ORDER BY r.role) FROM pandokos.role_assignment r
                  WHERE r.service_account_id = s.id), '{}')::text[] AS roles
  FROM pandokos.service_account s
 WHERE (sqlc.arg(state)::text = '' OR s.state = sqlc.arg(state)::text)
   AND (sqlc.arg(name)::text = '' OR s.name = sqlc.arg(name)::text)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN s.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN s.id < sqlc.arg(after_id)::uuid
            WHEN 'name'        THEN (s.name, s.id) > (sqlc.arg(after_name)::text, sqlc.arg(after_id)::uuid)
            WHEN '-name'       THEN (s.name, s.id) < (sqlc.arg(after_name)::text, sqlc.arg(after_id)::uuid)
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN s.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN s.id END DESC,
          CASE WHEN sqlc.arg(sort)::text = 'name'        THEN s.name END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-name'       THEN s.name END DESC,
          CASE WHEN sqlc.arg(sort)::text = 'name'        THEN s.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-name'       THEN s.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: UpdateServiceAccount :execrows
UPDATE pandokos.service_account
   SET name        = CASE WHEN sqlc.arg(set_name)::boolean THEN sqlc.arg(name)::text ELSE name END,
       description = CASE WHEN sqlc.arg(set_description)::boolean THEN sqlc.arg(description)::text ELSE description END,
       valid_from  = CASE WHEN sqlc.arg(set_valid_from)::boolean THEN sqlc.narg(valid_from)::timestamptz ELSE valid_from END,
       valid_until = CASE WHEN sqlc.arg(set_valid_until)::boolean THEN sqlc.narg(valid_until)::timestamptz ELSE valid_until END
 WHERE id = sqlc.arg(id)::uuid;

-- name: SetServiceAccountState :execrows
UPDATE pandokos.service_account
   SET state = sqlc.arg(state)::text
 WHERE id = sqlc.arg(id)::uuid;

-- name: DeleteRoleAssignments :execrows
DELETE FROM pandokos.role_assignment
 WHERE service_account_id = sqlc.arg(service_account_id)::uuid;

-- name: CreateRoleAssignment :exec
INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(service_account_id)::uuid, sqlc.arg(role)::text);
```

### `virtual_keys.sql`

```sql
-- Virtual keys. LookupVirtualKeyTenant runs with no tenant and no scope (store.DB.Unscoped);
-- every other statement runs in a tenant session.

-- name: LookupVirtualKeyTenant :one
SELECT COALESCE(pandokos.virtual_key_lookup(sqlc.arg(key_id)::text)::text, '')::text AS tenant_id;

-- name: GetKeyPrincipal :one
SELECT k.key_id, k.secret_hash, COALESCE(k.scopes, '{}')::text[] AS scopes, (k.scopes IS NOT NULL)::boolean AS scoped,
       k.expires_at, k.revoked_at,
       s.id AS service_account_id, s.name AS service_account_name, s.state AS service_account_state,
       s.valid_from, s.valid_until,
       t.id AS tenant_id, t.name AS tenant_name, t.state AS tenant_state,
       COALESCE((SELECT array_agg(r.role ORDER BY r.role) FROM pandokos.role_assignment r
                  WHERE r.service_account_id = k.service_account_id), '{}')::text[] AS roles
  FROM pandokos.virtual_key k
  JOIN pandokos.service_account s ON s.tenant_id = k.tenant_id AND s.id = k.service_account_id
  JOIN pandokos.tenant t ON t.id = k.tenant_id
 WHERE k.key_id = sqlc.arg(key_id)::text;

-- name: CreateVirtualKey :one
INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, description, secret_hash, scopes, expires_at)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(service_account_id)::uuid, sqlc.arg(key_id)::text,
        sqlc.arg(description)::text, sqlc.arg(secret_hash)::bytea,
        CASE WHEN sqlc.arg(scoped)::boolean THEN sqlc.arg(scopes)::text[] END,
        sqlc.narg(expires_at)::timestamptz)
RETURNING key_id, tenant_id, service_account_id, description, COALESCE(scopes, '{}')::text[] AS scopes,
          (scopes IS NOT NULL)::boolean AS scoped, expires_at, revoked_at, created_at,
          (CASE WHEN revoked_at IS NOT NULL THEN 'revoked' WHEN expires_at <= now() THEN 'expired' ELSE 'active' END)::text AS state;

-- name: GetVirtualKey :one
SELECT k.key_id, k.tenant_id, k.service_account_id, k.description, COALESCE(k.scopes, '{}')::text[] AS scopes,
       (k.scopes IS NOT NULL)::boolean AS scoped, k.expires_at, k.revoked_at, k.created_at,
       (CASE WHEN k.revoked_at IS NOT NULL THEN 'revoked' WHEN k.expires_at <= now() THEN 'expired' ELSE 'active' END)::text AS state
  FROM pandokos.virtual_key k
 WHERE k.key_id = sqlc.arg(key_id)::text;

-- name: ListVirtualKeys :many
SELECT k.id, k.key_id, k.tenant_id, k.service_account_id, k.description, COALESCE(k.scopes, '{}')::text[] AS scopes,
       (k.scopes IS NOT NULL)::boolean AS scoped, k.expires_at, k.revoked_at, k.created_at,
       (CASE WHEN k.revoked_at IS NOT NULL THEN 'revoked' WHEN k.expires_at <= now() THEN 'expired' ELSE 'active' END)::text AS state
  FROM pandokos.virtual_key k
 WHERE (sqlc.arg(state)::text = ''
        OR (CASE WHEN k.revoked_at IS NOT NULL THEN 'revoked' WHEN k.expires_at <= now() THEN 'expired' ELSE 'active' END) = sqlc.arg(state)::text)
   AND (NOT sqlc.arg(by_service_account)::boolean OR k.service_account_id = sqlc.arg(service_account_id)::uuid)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN k.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN k.id < sqlc.arg(after_id)::uuid
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN k.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN k.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: RevokeVirtualKey :execrows
UPDATE pandokos.virtual_key
   SET revoked_at = now()
 WHERE key_id = sqlc.arg(key_id)::text
   AND revoked_at IS NULL;
```

### `platform_keys.sql`

```sql
-- Platform keys. Every statement runs in platform scope with the admin role.

-- name: LockPlatformKeys :exec
-- Serializes pandokos init: the bytes of "pdk-init" as a big-endian integer.
SELECT pg_catalog.pg_advisory_xact_lock(8098715872705538420);

-- name: CountUsablePlatformKeys :one
SELECT count(*)::integer AS usable,
       (count(*) FILTER (WHERE bootstrap))::integer AS bootstrap
  FROM pandokos.platform_key
 WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now());

-- name: CreatePlatformKey :one
INSERT INTO pandokos.platform_key (key_id, description, role, secret_hash, bootstrap, expires_at)
VALUES (sqlc.arg(key_id)::text, sqlc.arg(description)::text, sqlc.arg(role)::text, sqlc.arg(secret_hash)::bytea,
        sqlc.arg(bootstrap)::boolean, sqlc.narg(expires_at)::timestamptz)
RETURNING id, key_id, description, role, bootstrap, expires_at, revoked_at, created_at,
          (CASE WHEN revoked_at IS NOT NULL THEN 'revoked' WHEN expires_at <= now() THEN 'expired' ELSE 'active' END)::text AS state;

-- name: GetPlatformKey :one
SELECT k.id, k.key_id, k.description, k.role, k.secret_hash, k.bootstrap, k.expires_at, k.revoked_at, k.created_at,
       (CASE WHEN k.revoked_at IS NOT NULL THEN 'revoked' WHEN k.expires_at <= now() THEN 'expired' ELSE 'active' END)::text AS state
  FROM pandokos.platform_key k
 WHERE k.key_id = sqlc.arg(key_id)::text;

-- name: ListPlatformKeys :many
SELECT k.id, k.key_id, k.description, k.role, k.bootstrap, k.expires_at, k.revoked_at, k.created_at,
       (CASE WHEN k.revoked_at IS NOT NULL THEN 'revoked' WHEN k.expires_at <= now() THEN 'expired' ELSE 'active' END)::text AS state
  FROM pandokos.platform_key k
 WHERE (sqlc.arg(state)::text = ''
        OR (CASE WHEN k.revoked_at IS NOT NULL THEN 'revoked' WHEN k.expires_at <= now() THEN 'expired' ELSE 'active' END) = sqlc.arg(state)::text)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN k.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN k.id < sqlc.arg(after_id)::uuid
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN k.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN k.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: RevokePlatformKey :execrows
UPDATE pandokos.platform_key
   SET revoked_at = now()
 WHERE key_id = sqlc.arg(key_id)::text
   AND revoked_at IS NULL;
```

### `audit.sql`

```sql
-- Audit. Insert only: there is no statement that reads, changes or removes an audit row.
-- detail is passed as JSON text and cast: the driver sends text parameters (ADR 0027).

-- name: InsertAuditEvent :exec
INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_id, actor_name, key_id, action, resource_type,
                                  resource_id, outcome, request_id, reason, detail)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(actor_type)::text, sqlc.arg(actor_id)::text, sqlc.arg(actor_name)::text,
        sqlc.arg(key_id)::text, sqlc.arg(action)::text, sqlc.arg(resource_type)::text, sqlc.arg(resource_id)::text,
        sqlc.arg(outcome)::text, sqlc.arg(request_id)::text, sqlc.arg(reason)::text, sqlc.arg(detail)::text::jsonb);

-- name: InsertPlatformAuditEvent :exec
INSERT INTO pandokos.platform_audit_event (actor_type, actor_id, actor_name, key_id, action, resource_type,
                                           resource_id, outcome, request_id, reason, detail)
VALUES (sqlc.arg(actor_type)::text, sqlc.arg(actor_id)::text, sqlc.arg(actor_name)::text,
        sqlc.arg(key_id)::text, sqlc.arg(action)::text, sqlc.arg(resource_type)::text, sqlc.arg(resource_id)::text,
        sqlc.arg(outcome)::text, sqlc.arg(request_id)::text, sqlc.arg(reason)::text, sqlc.arg(detail)::text::jsonb);
```
