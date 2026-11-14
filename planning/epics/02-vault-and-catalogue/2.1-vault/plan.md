# Sprint 2.1 — Implementation plan

- Design: [sprint.md](sprint.md). Section numbers (§) refer to it.
- Each part below is self-contained: an agent needs sprint.md and that part only. Parts run in
  order A, B, C1 … C10, D, E, F.

## Rules for the implementer

1. Branch `feat/2.1-vault` from `main`; sign off every commit. One commit for A, one for B
   (tests, failing), one per step of C (message given with the step), one for D, one for E.
2. **No dependency is added.** `git diff main -- go.mod go.sum` prints nothing at the end.
   Cryptography is the Go standard library only.
3. Never write a key, a secret, a key file or a sealed value made from real random key material
   in the repository. Key-encryption keys in tests come from `vault.GenerateLocalKey` at run
   time; the literal vectors of T-301 are made from synthetic byte sequences (step B3).
4. Never pass a secret, a revealed `secret.Secret`, a data key, a sealed secret, a wrapped key,
   a request body, a provider response body or the text of an HTTP client error to a logger, an
   error returned to a caller, a problem response or an audit record. In product code of this
   sprint `Reveal()` is called in exactly three places: `vault.Sealer.Seal` (to seal),
   `vault.NewLocalProvider` (to decode the key file) and the validator (to set the header); in
   the CLI, where the request body of create and replace is built.
5. Appendix A (migration), B (OpenAPI additions) and C (query files) are copied exactly. They
   were applied to PostgreSQL 16, generated with `sqlc` v1.31.1 and with `internal/apigen` while
   designing. If one of them does not work as written, stop and ask.
6. Generated files are never edited by hand: change the source, run `make generate`.
7. SQL runs only as `queries.New(tx).<Method>` inside a `store` helper (T-R10, T-R12).
8. A handler that changes data writes its audit row in the same transaction. The validation
   call to the provider happens **before** the transaction is opened, never inside it.
9. The admin role never reads `provider_credential.secret`; do not "fix" a permission error by
   widening a grant. Admin statements read `provider_credential_metadata` or name metadata
   columns.
10. A bound is never made to pass by lengthening it, a database test never by skipping it.
11. English everywhere; every exported identifier has a doc comment.

## Environment

- `make db-up` starts PostgreSQL 16 with TLS and PgBouncer in transaction mode (Docker) and
  writes `local/testdb/env`; `make db-down` discards them.
- `make test` (root module, race detector, needs the database), `make test-sdk`,
  `make image` then `make test-container`.
- `make generate` (apigen and `sqlc`; needs the release toolchain of `.go-version` for `sqlc`),
  `make generate-check`, `make migrations-sum`.
- `make lint`, `make vuln`, `make secrets`, and `make check` for everything a pull request must
  pass except the image.
- Before starting: `make db-up && make check` must pass on `main`.

---

## Part A — Scaffolding (commit: `chore: vault scaffolding, fixtures, generation inputs`)

**Files:** `.golangci.yml`, `test/fixtures/canary.go`, `test/fakeprovider/fakeprovider.go`,
`test/testutil/testutil.go`, `api/openapi/admin.v1.yaml`, `internal/store/migrations/sql/0003_vault.sql`,
`internal/store/migrations/migrations.sum`, `internal/store/queries/sql/*.sql`,
`internal/vault/doc.go`.

1. **Migration.** Create `0003_vault.sql` from appendix A, byte for byte. Run
   `make migrations-sum`; the file gains one line and the two existing lines do not change.
2. **Queries.** Create `vault.sql` and `credentials.sql` from appendix C and apply the listed
   changes to `virtual_keys.sql`.
3. **OpenAPI.** Insert appendix B into `api/openapi/admin.v1.yaml` at the places it names. In
   `info.description` nothing changes.
4. `make generate`. Commit the generated files. The repository does not compile yet where
   handlers are missing: add to `internal/admin` one file `credentials_todo.go` in which the
   fourteen new `adminapi.Server` methods return `errors.New("not implemented")`, and pass
   `CredentialID: nil` where `CreateVirtualKeyParams` gained a member. This file is deleted in
   C7. `go build ./...` must pass at the end of part A.
5. **Linter.** In `.golangci.yml`, `depguard`: add a rule for `internal/vault/**` that denies
   `internal/admin`, `internal/adminapi`, `internal/gateway`, `internal/family`,
   `internal/server`, `internal/cli`, `internal/config`; and a rule for every package except
   `internal/vault/**` that denies `crypto/aes`, `crypto/cipher`, `crypto/hkdf` (follow the
   shape of the existing per-package rules).
6. **Fixtures.** `fixtures.CanaryCredential = "pandokos-canary-credential-7b1e4c9d20a8f365"`;
   `Canaries()` returns four values; adjust `fixtures_test.go`.
7. **Fake provider.** `GET /v1/models`: 200 with `{"object":"list","data":[]}` when the bearer
   is in the accepted set (default: `CanaryProviderKey`; `Accept(keys ...string)` replaces it);
   otherwise 401 with an OpenAI-shaped error whose message **contains the presented key**. A
   setter `ModelsBehaviour(b string)` with `"403"`, `"429"`, `"500"`, `"redirect"` (302 to
   another path of itself that answers 200) and `"stall"` (no answer until the request is
   cancelled). Requests are recorded as today.
8. **testutil.** `WriteKEK(tb testing.TB) string` writes `vault.GenerateLocalKey()` to a file
   of mode 0600 in `tb.TempDir()` and returns the path; `NewKeyProvider(tb) vault.KeyProvider`
   returns a `LocalProvider` on a fresh key. They need symbols of step C1, so they are written
   in part B with the other failing tests, not here.
9. `internal/vault/doc.go`: the package comment (what the package is, ADR 0008 and 0033).

**Verify:** `go build ./... && make generate-check && make lint` pass; `make test` passes
except tests that enumerate operations, permissions or the lint spec (they fail until B and C).

---

## Part B — Tests first (commit: `test: sprint 2.1 tests, failing`)

Write every test of §9 before any file of part C. Then run `go vet ./... ; make test` and keep
the output for the pull request: packages touched must fail. Names are exactly those of §9.

General rules: tests over "every operation" range over `adminapi.Operations` and fail on an
operation they do not know; absence is asserted with `testutil.AssertAbsent` on the plain,
standard-base64, URL-base64 and hexadecimal forms of the value (`testutil.SecretForms(s)`, new);
bounds are polled with `testutil.Eventually`.

### B1. Helpers

- `test/apitest`: `Params.Credential`; `CallFor` knows the fourteen operations (bodies:
  create `{"provider":"openai","label":UniqueName("cred"),"secret":fixtures.CanaryCredential}`;
  replace `{"secret": fixtures.CanaryCredential}`; update `{"description":"changed by a test"}`;
  the two platform verbs `{"reason":"test"}`; the others `{}` or none) and substitutes
  `{credential}`. `New` builds `admin.Options.Sealer` from `testutil.NewKeyProvider` and exposes
  it as `API.Sealer` and `API.KeyProvider`; `API.Credential(tb, tenantID, adminKey, provider,
  label) adminapi.Credential` creates one through the API.
- `test/dbtest`: `Env` adds `PANDOKOS_VAULT_KEK_LOCAL_KEY_FILE` (a `WriteKEK` file per
  `Database`, the same file on every call).
- `test/isolation/api_test.go`: `platformAndSelf` gains the four `…TenantCredential…`
  operations as `platform`; the second-parameter block also triggers on `{credential}` with a
  credential of B against a random UUID; the body-member block also runs `createVirtualKey`
  with `credential_id` of B against a random UUID (both 400, identical).

### B2. Lint spec (`test/isolation/spec.go`, `lint.go`, tests)

- `Global`: `"kek_version": {Reason: "identifiers of key-encryption key versions; describes the installation, holds no key and no tenant information"}`.
- `Views`: `"provider_credential_metadata"`.
- `Grants` (strings as the lint builds them, columns sorted):

  ```
  kek_version:
    pandokos_gateway: SELECT
    pandokos_admin:   INSERT(id,provider), SELECT
    pandokos_worker:  SELECT
  tenant_data_key:
    pandokos_gateway: SELECT
    pandokos_admin:   INSERT(kek_version,tenant_id,version,wrapped_key), SELECT
  provider_credential:
    pandokos_gateway: SELECT
    pandokos_admin:   DELETE,
                      INSERT(description,expires_at,fingerprint,hint,id,is_default,key_version,label,provider,secret,tenant_id,validated_at),
                      SELECT(created_at,description,expires_at,fingerprint,hint,id,is_default,key_version,label,provider,rotated_at,state,state_changed_at,state_changed_by,state_reason,tenant_id,updated_at,validated_at),
                      UPDATE(description,expires_at,fingerprint,hint,is_default,key_version,label,secret,state,state_changed_by,state_reason,validated_at)
  provider_credential_metadata:
    pandokos_admin:   SELECT
  virtual_key (changed):
    pandokos_admin:   INSERT(credential_id,description,expires_at,key_id,scopes,secret_hash,service_account_id,tenant_id), SELECT, UPDATE(revoked_at)
  ```

- `Fixtures`:

  ```go
  "tenant_data_key": {
      Insert: `WITH k AS (INSERT INTO pandokos.kek_version (id, provider) VALUES ('test-fixture', 'test') ON CONFLICT (id) DO NOTHING)
  INSERT INTO pandokos.tenant_data_key (tenant_id, kek_version, wrapped_key)
  VALUES ($1::uuid, 'test-fixture', decode(repeat('00', 61), 'hex')) RETURNING id::text`,
      Update: `wrapped_key = decode(repeat('11', 61), 'hex')`,
  },
  "provider_credential": {
      Insert: `INSERT INTO pandokos.provider_credential (tenant_id, provider, label, key_version, secret, fingerprint)
  VALUES ($1::uuid, 'openai', 'c-' || substr(md5(random()::text), 1, 12), 1, decode(repeat('00', 61), 'hex'), md5(random()::text)) RETURNING id::text`,
      Update: `description = 'changed'`,
  },
  ```

- **L-19** in `lint.go`: for every relation of kind `v`, `pg_class.reloptions` must contain
  `security_invoker=true`; finding text `a view without security_invoker=true`. T-141 gets the
  case `CREATE VIEW pandokos.zz_v AS SELECT 1 AS x` (with `zz_v` allowed in the spec of that
  case) → `L-19`. Update the rule count in the doc comment of `Lint` and T-142.

### B3. Unit tests (`internal/vault`, `internal/authn`, `internal/authz`, `internal/config`, `internal/cli`, `cmd/pandokos`)

T-300 to T-309, T-311, T-312 as §9 describes. Notes:

- T-301: the synthetic inputs are tenant `00010203-…-0f` (bytes 0x00 to 0x0f), credential
  bytes 0x10 to 0x1f, version 1, data key bytes 0x20 to 0x3f, key-encryption key bytes 0x40 to
  0x5f, plaintext `synthetic-plaintext`. Expected associated data are written as literals
  computed by hand from §5.2: 36 bytes for a data key (15 of label, one zero byte, 16, 4) and
  59 bytes for a credential (22, one, 32, 4). The literal sealed value and wrapped key are produced once, after C1, by a throwaway
  program, and pasted as hexadecimal; until then the test fails on a `TODO` literal. These are
  not key material in the sense of rule 3: the keys are public byte sequences.
- T-305: `Opener` reads through an unexported interface (`source`: `credentialByID`,
  `defaultCredential`, `dataKey`) so the unit test injects a fake; the exported constructor
  wires the database implementation. The test lives in package `vault`.
- T-307: the proxy case sets `HTTPS_PROXY` and `HTTP_PROXY` with `t.Setenv` to a listener that
  fails the test when contacted.
- T-312: call `run([]string{"kek","generate","--out",path}, …)` as the other command tests do.

### B4. Database and API tests

T-310, T-321 to T-329 (`test/isolation`, `internal/vault`, `internal/store/queries`), T-340 to
T-346 (`internal/admin`). Notes:

- T-321 uses the `inSession` helper for each role and session and asserts SQLSTATE `42501` or
  zero rows exactly as §9 lists; the statement list is in the test, not derived.
- T-325 copies bytes with a superuser connection (`d.Super`), which bypasses row-level
  security: `UPDATE pandokos.provider_credential SET secret = (SELECT secret FROM … WHERE id =
  $src) WHERE id = $dst`, and the same for `tenant_data_key.wrapped_key`.
- T-326 and T-327 follow the 1.3 tests of the principal cache (T-262 to T-266): same listener
  construction, same injected clock, same pooled database for the no-notification case.
- T-329 creates two `LocalProvider`s, seals credentials for two tenants under each, then runs
  the script file with `psql`-free means: read the file, replace `:'kek_version'` by a quoted
  literal, execute on `d.Super`. The script must therefore use only that one variable.
- T-341 walks `adminapi.Operations` filtered to the fourteen, makes for each the valid call and
  the error variants of §5.4, and scans status line, headers and body.
- T-344 injects `admin.Options.Validators = {"openai": vault.NewOpenAIValidator(fakeURL, 2*time.Second)}`.

### B5. End-to-end, container, repository

T-360, T-361 (`test/e2e`), T-362 (`test/container`), T-R20 to T-R22 (`test/repo`). `serveEnv`
and `startBinary` get the key file through `d.Env`. T-361's table scan: for every table of
schema `pandokos`, `SELECT to_jsonb(t)::text FROM <table> t` as superuser. `to_jsonb` renders `bytea` as
hexadecimal (`\x…`), so the hexadecimal forms of the canary and of the key are searched as
well as the plain and base64 forms.

**Verify (part B):** `go vet ./...` reports only missing symbols of part C; the saved
`make test` output shows the new tests failing or not compiling.

---

## Part C — Implementation

After each step: `go build ./... && go vet ./...`, and the tests named with the step pass.

### C1. `internal/vault` core — `feat: key provider, local provider, envelope`

**Files:** `provider.go`, `local.go`, `envelope.go`. Signatures in §5.1 and §5.2.

- Errors: `ErrKeyVersionUnknown`, `ErrUnwrap`, `ErrUndecryptable`, `ErrInvalidKey` (bad key
  file content; its text is constant).
- `LocalProvider`: `map[string]cipher.AEAD` plus the active identifier; constructed from one
  key now. `String`, `GoString`, `Format`, `LogValue`, `MarshalJSON` print `[REDACTED]`
  (as `secret.Secret` does). `DataKey` the same.
- `Providers() []string` returns `{"anthropic", "openai"}`; `KnownProvider(name) bool`.
- `DataAAD(id tenant.ID, version int32) []byte` and
  `credentialAAD(id tenant.ID, credentialID string, version int32) ([]byte, error)` implement
  §5.2 exactly; `credentialID` is parsed as a canonical UUID into 16 bytes and anything else is
  an error.
- After `hkdf.Key` the caller's raw key is not retained; `Seal` and `Open` use the cached
  `cipher.AEAD`; `Open` returns a fresh slice the caller clears.
- Fill the two literals of T-301 now.

**Verify:** T-300 to T-304.

### C2. Migration and lint — `feat: migration 0003, lint rule L-19`

The migration file exists since part A. Add L-19 and the spec of B2.

**Verify:** `go test ./internal/store/... ./test/isolation/ -run 'Lint|Spec|Migrat|CrossTenant|Positive|VaultColumn|CredentialGuards'`
(T-140, T-142, T-143, T-310, T-321, T-322) and T-323.

### C3. `Sealer` and `Check` — `feat: data keys and sealing`

**Files:** `sealer.go`, `check.go`. Signatures in §5.7.

- `EnsureDataKey`: `GetDataKey(1)`; on `store.ErrNoRows`: 32 random bytes, `Wrap` with
  `DataAAD`, `RegisterKEKVersion`, `CreateDataKey`; `created` is true when the insert affected
  one row. Clear the raw bytes.
- `Seal`: `GetDataKey(1)` (after ensuring), `Unwrap`, `NewDataKey`, seal
  `[]byte(plaintext.Reveal())`, fingerprint, `Hint`; clear what it owns. An `Unwrap` failure
  maps to an error for which `store.IsUnavailable`-style handling in the admin `fail` hook
  yields `unavailable`: export `vault.ErrUnavailable` and wrap with it; add the case to
  `internal/admin/admin.go` `fail` in C7.
- `Check`: wrap and unwrap 32 random bytes with a fixed associated data `pandokos/check/v1`;
  then `db.Platform` → `ListKEKVersions`; every row must have `provider == p.Name()` and
  `p.Available` nil. The error text is the one of §5.7 with the version filled in.

**Verify:** T-324, T-328.

### C4. `Opener` — `feat: gateway-side opening with bounded caches`

**File:** `opener.go`. Everything in §5.6. Constants `maxCredentialEntries = 10000`,
`maxDataKeyEntries = 10000`. `NewOpener` refuses lifetimes outside the bounds of §5.9, a nil
database, provider or logger. Log lines of §5.10. In `internal/authn/cache.go` `invalidate`,
add `strings.HasPrefix(payload, "vault:")` to the first case (ignored).

**Verify:** T-305, T-306, T-325, T-327 (T-326 after C7).

### C5. Validator — `feat: credential validation against the provider`

**File:** `validate.go`, §5.8. `http.Client{Timeout: timeout, CheckRedirect: func(…) error
{ return http.ErrUseLastResponse }}` on a transport with `Proxy: nil`, `DisableKeepAlives:
true`, `TLSClientConfig{MinVersion: tls.VersionTLS12}`, `ForceAttemptHTTP2: true`. The request
path is the base path joined with `models`. Read with `io.CopyN(io.Discard, body, 65536)`.

**Verify:** T-307.

### C6. Permissions and principal — `feat: credential permissions, pinned credential on the principal`

`internal/authz`: the seven constants and catalogue rows of §5.5, roles updated,
`Principal.CredentialID`. `internal/authn`: fill it from `GetKeyPrincipalRow.CredentialID`.

**Verify:** T-309, the `authn` database tests, T-203 and T-204 unchanged.

### C7. Admin handlers — `feat: credential operations of the administration API`

**Files:** `internal/admin/credentials.go`, `tenant_credentials.go`, `admin.go`, `problem.go`,
`virtual_keys.go`, `tenants.go`, `convert.go`; delete `credentials_todo.go`.

- `Options`: `Sealer *vault.Sealer` (required), `Validators map[string]vault.Validator`.
- `problem.go`: the two codes of §5.4 and errors `errCredentialRejected`,
  `errValidationUnavailable`; `fail` maps them, and `vault.ErrUnavailable` to `unavailable`
  with the error log line (cause class only).
- One converter from the three row shapes `sqlc` generates (`CreateCredentialRow`,
  `PandokosProviderCredentialMetadatum`, the `RETURNING` rows) to `adminapi.Credential`
  (`owner` is always `tenant`; `state_reason`, `expires_at`, `validated_at`, `rotated_at`
  omitted when empty).
- Create: rules of §5.4 → optional validation (§5.8; log line) → `WithTenant`:
  `NewCredentialID`, `Seal`, `CreateCredential`, audit `data_key.create` when
  `Sealed.DataKeyCreated`, audit `credential.create`. Unique violation on
  `provider_credential_label_key` → `conflict`; on `provider_credential_default_key` → retry
  the transaction once.
- Replace: validation → `WithTenant`: `LockCredential` (`not_found`, `invalid_state` when
  revoked), `Seal` with the existing ID, `ReplaceCredentialSecret`, audit.
- State verbs, set default, delete: `LockCredential` first, then the tables of §5.4.
  Set default: `ClearDefaultCredential` (ignore no row) then `SetDefaultCredential`. Delete:
  `CountUsablePins`, then `CountOtherCredentials` when it is the default, then
  `DeleteCredential`.
- Platform operations: `WithTenant(tid)`; `GetTenant(tid)` no row → `errNotFound`; then the
  same code paths as the tenant verbs with `changed_by = "platform"` and the required reason.
  They share functions with the tenant handlers; they never call `Seal`.
- `createTenant`: after `bind`, `Sealer.EnsureDataKey` and the audit row.
- `createVirtualKey`: `credential_id` checked with `GetCredential` (missing or revoked →
  `invalid("credential_id", "must name a credential of the tenant that is not revoked")`);
  a foreign-key violation on `virtual_key_credential_fkey` maps to the same error.
  `VirtualKey.credential_id` in responses.

**Verify:** T-340 to T-346, T-270 to T-274, T-281, T-282, T-326.

### C8. Configuration — `feat: vault and cache configuration`

`internal/config`: `Config.Vault{KEK{Provider string; Local{KeyFile string; Key secret.Secret}}}`,
`Cache.CredentialTTL`, `Cache.DataKeyTTL`; four `keyTable` rows; defaults; validation and
loading per §5.9 (the key is loaded with the serve secrets; decode check with
`base64.StdEncoding.Strict()`; the decoded bytes are cleared); the mode warning
(`os.Stat`, skipped on Windows).

**Verify:** T-308, T-R (configuration reference) after part E.

### C9. Commands and wiring — `feat: kek generate, serve wiring, credential commands`

- `cmd/pandokos/kek.go`: `kek generate --out FILE` (§5.9): `os.OpenFile(path,
  O_WRONLY|O_CREATE|O_EXCL, 0o600)`; `usage.go` gains the command and the two command groups.
- `cmd/pandokos/serve.go`, after the database checks: `vault.NewLocalProvider(cfg.Vault.KEK.Local.Key)`;
  `vault.Check(ctx, adminDB, kp)` (30 s) → `vault ready` or `startup failed`;
  `vault.NewSealer`; `vault.NewOpener` on the gateway pool; listener
  `OnNotify: func(p string) { gatewayAuth.Invalidate(p); opener.Invalidate(p) }`,
  `OnReset: func() { gatewayAuth.Flush(); opener.Flush() }`; `admin.Options{Sealer, Validators:
  {"openai": vault.NewOpenAIValidator(cfg.Skeleton.OpenAI.ParsedBaseURL, 10*time.Second)}}`.
  The opener is not passed to `server.New`.
- `internal/cli`: the commands of §5.9, in the style of the existing ones; `TENANT` accepts a
  name; `CREDENTIAL` is an ID.

**Verify:** T-311, T-312, T-360, T-361.

### C10. Everything together — `test: end-to-end, container and SDK suites pass`

`make check`, `make image`, `make test-container`. Fix what the suites show; no new behaviour.

---

## Part D — CI and container (commit: `build: key file in the container test`)

**Files:** `test/container/container_test.go`, `.github/workflows/ci.yml` only if a job lists
environment variables for `serve` explicitly.

- The container test mounts a `testutil.WriteKEK` file read-only at
  `/run/secrets/pandokos_kek` (mode 0444 so the image's non-root user reads it; the warning of
  §5.1 is then expected in the logs and asserted) and sets
  `PANDOKOS_VAULT_KEK_LOCAL_KEY_FILE`.
- No new job, service or secret in CI. The `Dockerfile` does not change: the key is never in
  the image (add this sentence to T-R (Dockerfile) only if that test lists forbidden content).

**Verify:** `make image && make test-container`; CI green on the branch.

---

## Part E — Documentation and decision records (commit: `docs: sprint 2.1 documentation and ADRs`)

### E1. New: `docs/operations/vault.md`

Sections, in this order (T-R22 checks the marked facts):

1. **What is stored where**: the hierarchy of §5.2 in one table (key-encryption key: file;
   data key: wrapped, in `tenant_data_key`; secret: sealed, in `provider_credential`).
2. **Creating the key**: `pandokos kek generate --out`, the configuration key, the `vault
   ready` line and its version.
3. **Protecting it**: not in the image, not in the configuration file, not in the database or
   its backups; mode 0400 or 0440; Kubernetes `defaultMode: 0440` with `fsGroup`, Compose
   file-based secret; the warning.
4. **Backing it up** *(VLT-13)*: a database backup holds only sealed secrets and wrapped data
   keys; the key file is backed up **separately**, once, when generated, to a store the
   database administrators cannot read; test the copy by starting a spare instance against a
   restored database and seeing `vault ready`.
5. **What its loss means** *(exact phrase required: "every tenant must re-enter its provider
   credentials")*; what is recoverable and what is not:

   | Lost | Recoverable | Must be re-entered |
   |---|---|---|
   | Database, backup and key file exist | everything | nothing |
   | Key file (no copy) | tenants, identities, keys, audit, credential metadata | every provider credential |
   | One tenant's data key row | everything else | that tenant's credentials |
   | Database without backup | nothing | everything |

   The procedure: `deploy/postgres/recover-lost-kek.sql` (E3), run by a superuser with the lost
   version; then a new key file; tenants register credentials again.
6. **What its compromise means**: alone it opens nothing; with a copy of the database it opens
   every credential: tenants replace their keys at the provider. Rotation of the key arrives
   with VLT-10; until then the remedy is the loss procedure.
7. **How fast a change takes effect** *(the table of §5.6 with 30 s, 60 s, 5 m, 15 m)*, and
   what a missing listener changes (nothing but the usual delay).
8. **Validation**: what is sent, to which address, the two codes.
9. **Limits**: memory of a running gateway; the single process of v0.1 (R-5); erasure and old
   backups (O-8); no rotation yet (R-6).

### E2. Changed pages

| Page | Change |
|---|---|
| `docs/operations/configuration.md` | the four keys (generated table, T-R of the reference), the purposes table (key file: serve only), the warning, the example |
| `docs/operations/database.md` | new section "Grants of migration 0003": the matrix of B2 in prose; §5 "What `serve` checks at start" gains the key version check and its message |
| `docs/operations/keys.md` | `--credential`; the pin rule (D-6); a key with `scopes` needs `credential:use` next to `model:invoke` for tenant credentials |
| `docs/operations/container.md` | mounting the key file; limits of the pre-release |
| `docs/reference/permissions.md` | seven permissions, the three roles |
| `docs/reference/admin-api.md` | the two problem codes; `DELETE` returns the resource; commands table; "a secret is write-only: the provider credential is never returned, not even by create" |
| `docs/architecture/data-model.md` | role table: `pandokos_admin` "Cannot: read the sealed secret of a credential, in any session"; the paragraph "Platform administrators and tenant secrets" rewritten per D-7; `kek_version` stays in the platform group; `virtual_key` pin |
| `docs/architecture/security-model.md` | "Key hierarchy": the two derived subkeys, fingerprint and hint; threat row for the fingerprint |
| `docs/architecture/overview.md` | §11 `vault/` line: "envelope encryption, key providers, sealer, opener, validator" |
| `docs/adr/README.md` | 0033, 0034 |
| `README.md`, `planning/roadmap.md`, epic file | status only, at close |

### E3. `deploy/postgres/recover-lost-kek.sql`

```sql
-- Recovery after the loss of a key-encryption key (docs/operations/vault.md). Run as a
-- superuser, in the Pandokos database, with the lost version:
--   psql -v kek_version=local-0123456789abcdef -f recover-lost-kek.sql
-- Credentials sealed under data keys of that version can never be opened again: they are
-- revoked, the data keys and the version are removed, and tenants register credentials again.
BEGIN;
UPDATE pandokos.provider_credential c
   SET state = 'revoked', state_changed_by = 'platform', state_reason = 'key-encryption key lost'
 WHERE c.state <> 'revoked'
   AND EXISTS (SELECT 1 FROM pandokos.tenant_data_key d
                WHERE d.tenant_id = c.tenant_id AND d.version = c.key_version
                  AND d.kek_version = :'kek_version');
DELETE FROM pandokos.tenant_data_key WHERE kek_version = :'kek_version';
DELETE FROM pandokos.kek_version WHERE id = :'kek_version';
COMMIT;
```

(Verified as superuser on PostgreSQL 16. It writes no audit row: it is an act of the database
administrator, outside the product; the guide says to record it.)

### E4. Decision records (copy; status `proposed`, date of the commit)

**`docs/adr/0033-vault-formats-and-key-identifiers.md`**

> # 0033. Vault: formats, derived keys, key identifiers, caches
>
> Refines [ADR 0008](0008-vault-envelope-encryption.md) and [ADR 0030](0030-keys-format-storage-lookup.md).
>
> ## Context
>
> ADR 0008 fixes envelope encryption with AES-256-GCM, a per-tenant data key and a pluggable
> key-encryption key. Building it needs exact byte formats, a way to tell key versions apart, a
> fingerprint that does not help an attacker who holds the database, and numbers for the caches.
>
> ## Decision
>
> 1. Sealed values are `0x01 ‖ nonce(12) ‖ ciphertext ‖ tag(16)` with a random nonce per seal
>    (`cipher.NewGCMWithRandomNonce`). A key seals far fewer than 2^32 values.
> 2. The data key is a 32-byte root. HKDF-SHA-256 derives an encryption key (`info`
>    `pandokos/vault/v1/credential-encryption`) and a fingerprint key
>    (`pandokos/vault/v1/credential-fingerprint`).
> 3. Associated data are a versioned label, a zero byte and fixed-length fields: for a wrapped
>    data key the tenant ID and the data key version; for a credential the tenant ID, the
>    credential ID and the data key version. The version of the key-encryption key is stored
>    next to the wrapped data key and bound to nothing else, so re-wrapping leaves credentials
>    untouched.
> 4. The fingerprint is the first 16 bytes of HMAC-SHA-256 under the fingerprint key; the hint
>    is the last four characters, withheld for secrets shorter than 16 characters.
> 5. `KeyProvider` has `Wrap`, `Unwrap`, `Available` and `Name`. The local provider reads one
>    base64 line of 32 bytes; the version identifier is derived from the key (`local-` and 16
>    hexadecimal characters of a SHA-256). Versions that wrapped a data key are registered in
>    the global table `kek_version`; `serve` refuses to start when one of them is not available.
> 6. The gateway caches opened credentials for `cache.credential_ttl` (30 s, at most 60 s) and
>    derived data keys for `cache.data_key_ttl` (5 m, at most 15 m), each bounded to 10 000
>    entries, invalidated by the payload `vault:<tenant>` on the notification channel of ADR
>    0030. The lifetime is the guarantee; notifications shorten it.
>
> ## Consequences
>
> A change of algorithm is a new format byte and a new label. A wrong key file stops the
> process at start with the missing version named. A revoked credential can be used for up to
> the credential lifetime; a removed data key for up to the sum of both. Fingerprints of one
> secret differ between tenants and cannot be compared across them.
>
> ## Alternatives considered
>
> A counter nonce (needs durable state per key); the data key used directly for both purposes
> (one key, two primitives); an unkeyed or globally keyed hash as fingerprint (offline guessing,
> cross-tenant correlation); operator-chosen version labels (a label can be attached to the
> wrong file); no registry of versions (a wrong file would fail tenant by tenant).

**`docs/adr/0034-credential-model-and-platform-oversight.md`**

> # 0034. Credential model and platform oversight
>
> Refines [ADR 0007](0007-authorization-rbac.md), [ADR 0008](0008-vault-envelope-encryption.md)
> and [ADR 0027](0027-database-roles-and-session-context.md).
>
> ## Context
>
> VLT-01 to VLT-06 and AZ-08 ask for labelled credentials with one default per provider,
> write-only secrets, decryption by the data plane only, and a platform administrator who sees
> metadata and can disable or revoke but never read or replace.
>
> ## Decision
>
> 1. A credential is `active`, `disabled` or `revoked`. Revocation is final and destroys the
>    sealed secret in the same statement; the metadata stays until the credential is deleted.
>    The database enforces the lifecycle by trigger.
> 2. The first credential of a provider is its default; the default moves only on request. A
>    virtual key can be pinned to one credential when it is created. A pinned key uses its pin
>    for that credential's provider and the default for any other; nothing ever falls back to
>    another credential.
> 3. The admin database role cannot read the sealed secret in any session: it holds no `SELECT`
>    on that column. It writes sealed secrets and reads metadata, through the view
>    `provider_credential_metadata` (`security_invoker`). Only the gateway role reads sealed
>    secrets, in the tenant's session. Vault tables carry the canonical tenant policy and
>    nothing else: a transaction in platform scope sees none of their rows.
> 4. A platform administrator acts on a named tenant through four operations with the platform
>    permissions `tenant_credential:read` and `tenant_credential:revoke`; they run in that
>    tenant's session, require a reason for a change and write the audit row in the tenant's
>    log. No platform operation accepts a secret.
> 5. Tenant permissions are `credential:read`, `write`, `rotate`, `delete` and `use`; the vault
>    checks `credential:use` itself when asked to open a credential.
> 6. Until the catalogue exists a credential names its provider by an identifier from a list
>    in code; validation at write time exists for the provider whose endpoint the installation
>    knows, is asked per request and stores nothing unless the provider accepts.
>
> ## Consequences
>
> AZ-08 holds at the database, not only in handlers. A platform administrator cannot search
> all tenants for a fingerprint. In the single process of v0.1 both database users are in one
> memory space; the separation becomes a deployment property when roles run apart.
>
> ## Alternatives considered
>
> A platform-scope policy on the credential table with column privileges (a second policy on a
> tenant table, and a platform session that sees every tenant's metadata at once); platform
> principals holding tenant permissions (contradicts ADR 0031); deleting instead of revoking
> (loses the fingerprint that identifies what was revoked); a pin table per provider.

**History lines** to append: ADR 0008 — "2026-11: sprint 2.1 fixes formats, derived subkeys,
key identifiers and cache numbers (ADR 0033) and the credential model (ADR 0034)"; ADR 0027 —
"2026-11: sprint 2.1 adds lint rule L-19 (views run with the caller's rights) and the first
view"; ADR 0030 — "2026-11: sprint 2.1 adds the payload `vault:<tenant>`, which the principal
cache ignores".

### E5. Planning files

`sprint.md` front matter `status: done` at close; `epic.md` `status: in-progress` when part A
is merged; the roadmap row is unchanged.

**Verify (part E):** `go test ./test/repo/` (T-R14, T-R20 to T-R22, the configuration
reference test).

---

## Part F — Verification

Run in order and paste the summary lines in the pull request:

1. `make db-down; make db-up`
2. `make check` — format, vet, lint, tidy, generate-check, secrets, tests, SDK suite,
   vulnerability check.
3. `make image && make test-container`
4. `git diff main -- go.mod go.sum` — empty.
5. `go test ./test/isolation/ -run 'TestSchemaLint$' -v` — zero findings.
6. `go test ./internal/vault/ ./internal/admin/ ./test/e2e/ -run 'Canar|NoResponseHoldsASecret|OpenerDB' -v`
   — the three exit conditions of the epic.
7. By hand, once: `bin/pandokos kek generate --out /tmp/kek && ls -l /tmp/kek`; start `serve`
   with it; start again with a second generated key and read the refusal; delete both files.
8. Tick the definition of done (§10) item by item in the pull request, with the coverage table
   and the tests-first output of part B.

## What the implementer does not do

- Does not call `Opener.Open` from the inference route, and does not remove `skeleton.*` (3.1).
- Does not add a `provider` table, a platform-scope policy on a vault table, a `SELECT` on
  `secret` for the admin role, key rotation, a second key file, or a cache on the admin plane.
- Does not change `internal/apigen`, merged migrations, or the listener of `internal/store`.
- Does not log, return or store a provider's response body.

---

## Appendix A — `internal/store/migrations/sql/0003_vault.sql`

```sql
-- 0003_vault: key-encryption key versions, tenant data keys, provider credentials and their
-- metadata view, the credential a virtual key is pinned to, vault notifications. Runs in one
-- transaction as pandokos_owner. Every name is schema-qualified.

-- Versions of the key-encryption key that wrapped a data key of this database (global table).
-- The key itself is never in the database (VLT-09): a row holds an identifier only.
CREATE TABLE pandokos.kek_version (
    id         text        NOT NULL,
    provider   text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT kek_version_pkey PRIMARY KEY (id),
    CONSTRAINT kek_version_id_format CHECK (id ~ '^[a-z0-9][a-z0-9._:-]{0,126}$'),
    CONSTRAINT kek_version_provider_format CHECK (provider ~ '^[a-z][a-z0-9-]{0,30}$')
);

GRANT SELECT ON pandokos.kek_version TO pandokos_gateway, pandokos_admin, pandokos_worker;
GRANT INSERT (id, provider) ON pandokos.kek_version TO pandokos_admin;

-- Tells the instances that vault data of a tenant changed. The payload names a tenant, never a
-- credential or a key.
CREATE FUNCTION pandokos.notify_vault() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        PERFORM pg_catalog.pg_notify('pandokos_cache', 'vault:' || OLD.tenant_id::text);
    ELSE
        PERFORM pg_catalog.pg_notify('pandokos_cache', 'vault:' || NEW.tenant_id::text);
    END IF;
    RETURN NULL;
END
$$;

-- Data keys of tenants (tenant-scoped table). wrapped_key is the data key encrypted by the
-- key-encryption key version kek_version; it is useless without that key.
CREATE TABLE pandokos.tenant_data_key (
    tenant_id   uuid        NOT NULL,
    id          uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    version     integer     NOT NULL DEFAULT 1,
    kek_version text        NOT NULL,
    wrapped_key bytea       NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tenant_data_key_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT tenant_data_key_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT tenant_data_key_kek_version_fkey FOREIGN KEY (kek_version) REFERENCES pandokos.kek_version (id),
    CONSTRAINT tenant_data_key_version_key UNIQUE (tenant_id, version),
    CONSTRAINT tenant_data_key_version_positive CHECK (version >= 1),
    CONSTRAINT tenant_data_key_wrapped_key_length CHECK (octet_length(wrapped_key) BETWEEN 32 AND 1024)
);
CREATE INDEX tenant_data_key_kek_version_idx ON pandokos.tenant_data_key (kek_version);

CREATE FUNCTION pandokos.tenant_data_key_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NEW.version <> OLD.version THEN
        RAISE EXCEPTION 'the version of a data key is immutable' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER tenant_data_key_guard BEFORE INSERT OR UPDATE ON pandokos.tenant_data_key
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();
CREATE TRIGGER tenant_data_key_version_guard BEFORE UPDATE ON pandokos.tenant_data_key
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_data_key_guard();
CREATE TRIGGER tenant_data_key_notify AFTER UPDATE OR DELETE ON pandokos.tenant_data_key
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_vault();

ALTER TABLE pandokos.tenant_data_key ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.tenant_data_key FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.tenant_data_key
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.tenant_data_key TO pandokos_gateway, pandokos_admin;
GRANT INSERT (tenant_id, version, kek_version, wrapped_key) ON pandokos.tenant_data_key TO pandokos_admin;

-- Provider credentials of tenants (tenant-scoped table). secret is the sealed secret: only the
-- gateway role can read it, and it is destroyed when the credential is revoked.
CREATE TABLE pandokos.provider_credential (
    tenant_id        uuid        NOT NULL,
    id               uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    provider         text        NOT NULL,
    label            text        NOT NULL,
    description      text        NOT NULL DEFAULT '',
    state            text        NOT NULL DEFAULT 'active',
    is_default       boolean     NOT NULL DEFAULT false,
    key_version      integer     NOT NULL,
    secret           bytea,
    fingerprint      text        NOT NULL,
    hint             text        NOT NULL DEFAULT '',
    expires_at       timestamptz,
    validated_at     timestamptz,
    rotated_at       timestamptz,
    state_changed_at timestamptz NOT NULL DEFAULT now(),
    state_changed_by text        NOT NULL DEFAULT 'tenant',
    state_reason     text        NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT provider_credential_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT provider_credential_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT provider_credential_label_key UNIQUE (tenant_id, provider, label),
    CONSTRAINT provider_credential_provider_format CHECK (provider ~ '^[a-z][a-z0-9-]{0,62}$'),
    CONSTRAINT provider_credential_label_format CHECK (label ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    CONSTRAINT provider_credential_description_length CHECK (char_length(description) <= 500),
    CONSTRAINT provider_credential_state_valid CHECK (state IN ('active', 'disabled', 'revoked')),
    CONSTRAINT provider_credential_key_version_positive CHECK (key_version >= 1),
    CONSTRAINT provider_credential_secret_presence CHECK ((state = 'revoked') = (secret IS NULL)),
    CONSTRAINT provider_credential_secret_length CHECK (secret IS NULL OR octet_length(secret) BETWEEN 30 AND 8221),
    CONSTRAINT provider_credential_fingerprint_format CHECK (fingerprint ~ '^[0-9a-f]{32}$'),
    CONSTRAINT provider_credential_hint_length CHECK (char_length(hint) <= 4),
    CONSTRAINT provider_credential_state_changed_by_valid CHECK (state_changed_by IN ('tenant', 'platform')),
    CONSTRAINT provider_credential_state_reason_length CHECK (char_length(state_reason) <= 500)
);
-- At most one default per tenant and provider.
CREATE UNIQUE INDEX provider_credential_default_key ON pandokos.provider_credential (tenant_id, provider) WHERE is_default;

-- Lifecycle of a credential: created active; active and disabled alternate; revoked is final
-- and destroys the sealed secret; the provider never changes.
CREATE FUNCTION pandokos.provider_credential_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'active' THEN
            RAISE EXCEPTION 'a credential is created in state active' USING ERRCODE = 'check_violation';
        END IF;
        NEW.rotated_at := NULL;
        NEW.state_changed_at := now();
        RETURN NEW;
    END IF;
    IF NEW.provider <> OLD.provider THEN
        RAISE EXCEPTION 'the provider of a credential is immutable' USING ERRCODE = 'check_violation';
    END IF;
    NEW.rotated_at := OLD.rotated_at;
    NEW.state_changed_at := OLD.state_changed_at;
    IF OLD.state = 'revoked' THEN
        IF NEW.state <> 'revoked' OR NEW.secret IS NOT NULL THEN
            RAISE EXCEPTION 'a revoked credential stays revoked' USING ERRCODE = 'check_violation';
        END IF;
        IF NEW.is_default AND NOT OLD.is_default THEN
            RAISE EXCEPTION 'a revoked credential cannot become the default' USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.state <> OLD.state THEN
        NEW.state_changed_at := now();
    END IF;
    IF NEW.state = 'revoked' THEN
        NEW.secret := NULL;
        RETURN NEW;
    END IF;
    IF NEW.secret IS NULL THEN
        RAISE EXCEPTION 'only a revocation removes the secret' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.secret <> OLD.secret THEN
        NEW.rotated_at := now();
        IF NEW.validated_at IS NOT DISTINCT FROM OLD.validated_at THEN
            NEW.validated_at := NULL;
        END IF;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER provider_credential_guard BEFORE INSERT OR UPDATE ON pandokos.provider_credential
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();
CREATE TRIGGER provider_credential_lifecycle BEFORE INSERT OR UPDATE ON pandokos.provider_credential
    FOR EACH ROW EXECUTE FUNCTION pandokos.provider_credential_guard();
CREATE TRIGGER provider_credential_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.provider_credential
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_vault();

ALTER TABLE pandokos.provider_credential ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.provider_credential FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.provider_credential
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

-- The gateway reads everything, the sealed secret included, in the session of the tenant. The
-- admin role writes the sealed secret and can never read it back (AZ-08, VLT-04).
GRANT SELECT ON pandokos.provider_credential TO pandokos_gateway;
GRANT SELECT (tenant_id, id, provider, label, description, state, is_default, key_version, fingerprint, hint,
              expires_at, validated_at, rotated_at, state_changed_at, state_changed_by, state_reason, created_at, updated_at)
    ON pandokos.provider_credential TO pandokos_admin;
GRANT INSERT (tenant_id, id, provider, label, description, is_default, key_version, secret, fingerprint, hint,
              expires_at, validated_at)
    ON pandokos.provider_credential TO pandokos_admin;
GRANT UPDATE (label, description, state, is_default, key_version, secret, fingerprint, hint, expires_at, validated_at,
              state_changed_by, state_reason)
    ON pandokos.provider_credential TO pandokos_admin;
GRANT DELETE ON pandokos.provider_credential TO pandokos_admin;

-- What may be read about a credential by anybody but the gateway: every column except the
-- sealed secret. It runs with the privileges and the policies of the caller.
CREATE VIEW pandokos.provider_credential_metadata
    WITH (security_invoker = true, security_barrier = true) AS
SELECT c.tenant_id, c.id, c.provider, c.label, c.description, c.state, c.is_default, c.key_version, c.fingerprint,
       c.hint, c.expires_at, c.validated_at, c.rotated_at, c.state_changed_at, c.state_changed_by, c.state_reason,
       c.created_at, c.updated_at
  FROM pandokos.provider_credential c;

GRANT SELECT ON pandokos.provider_credential_metadata TO pandokos_admin;

-- The credential a virtual key is pinned to (VLT-01). It is set when the key is created and
-- never changes; deleting the credential clears it.
ALTER TABLE pandokos.virtual_key ADD COLUMN credential_id uuid;
ALTER TABLE pandokos.virtual_key ADD CONSTRAINT virtual_key_credential_fkey
    FOREIGN KEY (tenant_id, credential_id) REFERENCES pandokos.provider_credential (tenant_id, id)
    ON DELETE SET NULL (credential_id);
CREATE INDEX virtual_key_credential_idx ON pandokos.virtual_key (tenant_id, credential_id) WHERE credential_id IS NOT NULL;

CREATE FUNCTION pandokos.virtual_key_pin_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NEW.credential_id IS DISTINCT FROM OLD.credential_id AND NEW.credential_id IS NOT NULL THEN
        RAISE EXCEPTION 'the credential of a key is set when the key is created' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER virtual_key_pin_guard BEFORE UPDATE ON pandokos.virtual_key
    FOR EACH ROW EXECUTE FUNCTION pandokos.virtual_key_pin_guard();

GRANT INSERT (credential_id) ON pandokos.virtual_key TO pandokos_admin;
```

## Appendix B — additions to `api/openapi/admin.v1.yaml`

**Tags**: add `- name: credentials` and `- name: tenant-credentials` before `platform-keys`.
**`ProblemCode.enum`**: append `credential_rejected`, `validation_unavailable`.
**`VirtualKey.properties`** and **`VirtualKeyCreate.properties`**: add, after `description`,
`credential_id: {$ref: '#/components/schemas/ID'}`.

**Paths**, inserted before `/v1/platform-keys:`

```yaml
  /v1/tenants/{tenant}/credentials:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: listCredentials
      tags: [credentials]
      summary: List the provider credentials of a tenant (metadata)
      x-pandokos-permission: credential:read
      parameters:
        - {$ref: '#/components/parameters/Limit'}
        - {$ref: '#/components/parameters/Cursor'}
        - {$ref: '#/components/parameters/Sort'}
        - name: state
          in: query
          description: Only credentials in this state.
          schema: {$ref: '#/components/schemas/CredentialState'}
        - name: provider
          in: query
          description: Only credentials of this provider.
          schema: {$ref: '#/components/schemas/ProviderName'}
      responses:
        "200": {$ref: '#/components/responses/CredentialList'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
    post:
      operationId: createCredential
      tags: [credentials]
      summary: Register a provider credential
      description: >-
        The secret is encrypted under the data key of the tenant and is never returned, by this
        or by any other operation. The first credential of a provider becomes its default. With
        validate, the secret is first presented to the provider and nothing is stored unless the
        provider accepts it.
      x-pandokos-permission: credential:write
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/CredentialCreate'}
      responses:
        "201": {$ref: '#/components/responses/CredentialCreated'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "409": {$ref: '#/components/responses/Conflict'}
        "413": {$ref: '#/components/responses/RequestTooLarge'}
        "415": {$ref: '#/components/responses/UnsupportedMediaType'}
        "422": {$ref: '#/components/responses/CredentialRejected'}
        "500": {$ref: '#/components/responses/Internal'}
        "502": {$ref: '#/components/responses/ValidationUnavailable'}
        "503": {$ref: '#/components/responses/Unavailable'}
  /v1/tenants/{tenant}/credentials/{credential}:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
      - {$ref: '#/components/parameters/Credential'}
    get:
      operationId: getCredential
      tags: [credentials]
      summary: Read the metadata of a credential
      x-pandokos-permission: credential:read
      responses:
        "200": {$ref: '#/components/responses/Credential'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
    patch:
      operationId: updateCredential
      tags: [credentials]
      summary: Change the label, the description or the known expiry of a credential
      x-pandokos-permission: credential:write
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/CredentialUpdate'}
      responses:
        "200": {$ref: '#/components/responses/Credential'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "409": {$ref: '#/components/responses/Conflict'}
        "413": {$ref: '#/components/responses/RequestTooLarge'}
        "415": {$ref: '#/components/responses/UnsupportedMediaType'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
    delete:
      operationId: deleteCredential
      tags: [credentials]
      summary: Delete a credential
      description: >-
        Removes the row and its sealed secret; the response is the metadata as it was. Refused
        with invalid_state while a virtual key that is neither revoked nor expired is pinned to
        the credential, and while it is the default of a provider that has another credential
        that is not revoked. Pins of revoked and expired keys are cleared.
      x-pandokos-permission: credential:delete
      responses:
        "200": {$ref: '#/components/responses/Credential'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "409": {$ref: '#/components/responses/Conflict'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
  /v1/tenants/{tenant}/credentials/{credential}/secret:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
      - {$ref: '#/components/parameters/Credential'}
    put:
      operationId: replaceCredentialSecret
      tags: [credentials]
      summary: Replace the secret of a credential
      description: >-
        The credential keeps its identifier, label, default flag and pins. The known expiry is
        replaced by the one given, or cleared. A revoked credential is invalid_state.
      x-pandokos-permission: credential:rotate
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/CredentialSecret'}
      responses:
        "200": {$ref: '#/components/responses/Credential'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "409": {$ref: '#/components/responses/Conflict'}
        "413": {$ref: '#/components/responses/RequestTooLarge'}
        "415": {$ref: '#/components/responses/UnsupportedMediaType'}
        "422": {$ref: '#/components/responses/CredentialRejected'}
        "500": {$ref: '#/components/responses/Internal'}
        "502": {$ref: '#/components/responses/ValidationUnavailable'}
        "503": {$ref: '#/components/responses/Unavailable'}
  /v1/tenants/{tenant}/credentials/{credential}/disable:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
      - {$ref: '#/components/parameters/Credential'}
    post:
      operationId: disableCredential
      tags: [credentials]
      summary: Disable a credential
      description: >-
        Takes effect on every instance within the documented bound. Disabling a disabled
        credential changes nothing; a revoked credential is invalid_state. A disabled default
        stays the default: calls for its provider are refused, not moved to another credential.
      x-pandokos-permission: credential:write
      requestBody:
        required: false
        content:
          application/json:
            schema: {$ref: '#/components/schemas/Reason'}
      responses:
        "200": {$ref: '#/components/responses/Credential'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "409": {$ref: '#/components/responses/Conflict'}
        "413": {$ref: '#/components/responses/RequestTooLarge'}
        "415": {$ref: '#/components/responses/UnsupportedMediaType'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
  /v1/tenants/{tenant}/credentials/{credential}/enable:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
      - {$ref: '#/components/parameters/Credential'}
    post:
      operationId: enableCredential
      tags: [credentials]
      summary: Enable a disabled credential
      description: Enabling an active credential changes nothing; a revoked credential is invalid_state.
      x-pandokos-permission: credential:write
      requestBody:
        required: false
        content:
          application/json:
            schema: {$ref: '#/components/schemas/Reason'}
      responses:
        "200": {$ref: '#/components/responses/Credential'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "409": {$ref: '#/components/responses/Conflict'}
        "413": {$ref: '#/components/responses/RequestTooLarge'}
        "415": {$ref: '#/components/responses/UnsupportedMediaType'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
  /v1/tenants/{tenant}/credentials/{credential}/set-default:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
      - {$ref: '#/components/parameters/Credential'}
    post:
      operationId: setDefaultCredential
      tags: [credentials]
      summary: Make a credential the default of its provider
      description: >-
        The previous default of the provider stops being the default in the same transaction.
        Naming the current default changes nothing; a revoked credential is invalid_state.
      x-pandokos-permission: credential:write
      requestBody:
        required: false
        content:
          application/json:
            schema: {$ref: '#/components/schemas/Reason'}
      responses:
        "200": {$ref: '#/components/responses/Credential'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "409": {$ref: '#/components/responses/Conflict'}
        "413": {$ref: '#/components/responses/RequestTooLarge'}
        "415": {$ref: '#/components/responses/UnsupportedMediaType'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
  /v1/tenants/{tenant}/credentials/{credential}/revoke:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
      - {$ref: '#/components/parameters/Credential'}
    post:
      operationId: revokeCredential
      tags: [credentials]
      summary: Revoke a credential
      description: >-
        Destroys the sealed secret at once and takes effect on every instance within the
        documented bound. A revocation cannot be undone. Revoking a revoked credential changes
        nothing. The metadata stays until the credential is deleted.
      x-pandokos-permission: credential:delete
      requestBody:
        required: false
        content:
          application/json:
            schema: {$ref: '#/components/schemas/Reason'}
      responses:
        "200": {$ref: '#/components/responses/Credential'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "413": {$ref: '#/components/responses/RequestTooLarge'}
        "415": {$ref: '#/components/responses/UnsupportedMediaType'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
  /v1/tenant-credentials/{tenant}:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: listTenantCredentials
      tags: [tenant-credentials]
      summary: List the credentials of a tenant as a platform administrator (metadata)
      x-pandokos-permission: tenant_credential:read
      parameters:
        - {$ref: '#/components/parameters/Limit'}
        - {$ref: '#/components/parameters/Cursor'}
        - {$ref: '#/components/parameters/Sort'}
        - name: state
          in: query
          description: Only credentials in this state.
          schema: {$ref: '#/components/schemas/CredentialState'}
        - name: provider
          in: query
          description: Only credentials of this provider.
          schema: {$ref: '#/components/schemas/ProviderName'}
      responses:
        "200": {$ref: '#/components/responses/CredentialList'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
  /v1/tenant-credentials/{tenant}/{credential}:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
      - {$ref: '#/components/parameters/Credential'}
    get:
      operationId: getTenantCredential
      tags: [tenant-credentials]
      summary: Read the metadata of a credential of a tenant as a platform administrator
      x-pandokos-permission: tenant_credential:read
      responses:
        "200": {$ref: '#/components/responses/Credential'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
  /v1/tenant-credentials/{tenant}/{credential}/disable:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
      - {$ref: '#/components/parameters/Credential'}
    post:
      operationId: disableTenantCredential
      tags: [tenant-credentials]
      summary: Disable a credential of a tenant as a platform administrator
      description: >-
        The reason is mandatory and is recorded in the audit log of the tenant and on the
        credential, where the tenant reads it. The tenant can enable the credential again.
      x-pandokos-permission: tenant_credential:revoke
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/RequiredReason'}
      responses:
        "200": {$ref: '#/components/responses/Credential'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "409": {$ref: '#/components/responses/Conflict'}
        "413": {$ref: '#/components/responses/RequestTooLarge'}
        "415": {$ref: '#/components/responses/UnsupportedMediaType'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
  /v1/tenant-credentials/{tenant}/{credential}/revoke:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
      - {$ref: '#/components/parameters/Credential'}
    post:
      operationId: revokeTenantCredential
      tags: [tenant-credentials]
      summary: Revoke a credential of a tenant as a platform administrator
      description: >-
        Destroys the sealed secret; it cannot be undone. The reason is mandatory and is recorded
        in the audit log of the tenant and on the credential.
      x-pandokos-permission: tenant_credential:revoke
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/RequiredReason'}
      responses:
        "200": {$ref: '#/components/responses/Credential'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "413": {$ref: '#/components/responses/RequestTooLarge'}
        "415": {$ref: '#/components/responses/UnsupportedMediaType'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
```

**`components.parameters`**, before `Limit`:

```yaml
    Credential:
      name: credential
      in: path
      required: true
      description: Credential ID.
      schema: {$ref: '#/components/schemas/ID'}
```

**`components.responses`**, before `InvalidRequest`:

```yaml
    Credential:
      description: The metadata of the credential. The secret is never returned.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/Credential'}
    CredentialCreated:
      description: The metadata of the new credential. The secret is never returned.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/Credential'}
    CredentialList:
      description: One page of credentials (metadata).
      content:
        application/json:
          schema: {$ref: '#/components/schemas/CredentialList'}
    CredentialRejected:
      description: 'The provider refused the secret; nothing was stored. Code: credential_rejected.'
      content:
        application/problem+json:
          schema: {$ref: '#/components/schemas/Problem'}
    ValidationUnavailable:
      description: 'The provider could not be asked or gave no clear answer; nothing was stored. Code: validation_unavailable.'
      content:
        application/problem+json:
          schema: {$ref: '#/components/schemas/Problem'}
```

**`components.schemas`**, before `Sort`:

```yaml
    ProviderName:
      type: string
      description: Identifier of a provider. This version accepts openai and anthropic.
      pattern: '^[a-z][a-z0-9-]{0,62}$'
    CredentialState:
      type: string
      description: active, disabled (can be enabled again) or revoked (final; the sealed secret is destroyed).
      enum: [active, disabled, revoked]
    CredentialOwner:
      type: string
      description: Who owns the credential and pays the provider. Credentials of the platform come with a later version.
      enum: [tenant]
    CredentialActor:
      type: string
      description: Which side last changed the state of the credential.
      enum: [tenant, platform]
    Credential:
      type: object
      additionalProperties: false
      required: [id, tenant_id, provider, label, description, owner, state, is_default, fingerprint, hint, state_changed_at, state_changed_by, created_at]
      properties:
        id: {$ref: '#/components/schemas/ID'}
        tenant_id: {$ref: '#/components/schemas/ID'}
        provider: {$ref: '#/components/schemas/ProviderName'}
        label: {$ref: '#/components/schemas/Name'}
        description: {type: string}
        owner: {$ref: '#/components/schemas/CredentialOwner'}
        state: {$ref: '#/components/schemas/CredentialState'}
        is_default: {type: boolean, description: Whether calls for the provider use this credential when the key is not pinned to another.}
        fingerprint: {type: string, description: 'Keyed hash of the secret, 32 hexadecimal characters. Equal secrets of one tenant have equal fingerprints; it cannot be computed outside Pandokos.'}
        hint: {type: string, description: 'The last four characters of the secret; empty when the secret is shorter than 16 characters.'}
        expires_at: {type: string, format: date-time, description: Expiry as declared by the tenant. Informative.}
        validated_at: {type: string, format: date-time, description: When the provider last accepted the current secret.}
        rotated_at: {type: string, format: date-time, description: When the secret was last replaced.}
        state_changed_at: {type: string, format: date-time}
        state_changed_by: {$ref: '#/components/schemas/CredentialActor'}
        state_reason: {type: string, description: The reason given with the last change of state.}
        created_at: {type: string, format: date-time}
    CredentialCreate:
      type: object
      additionalProperties: false
      required: [provider, label, secret]
      properties:
        provider: {$ref: '#/components/schemas/ProviderName'}
        label: {$ref: '#/components/schemas/Name'}
        secret: {type: string, minLength: 8, maxLength: 8192, description: 'The provider API key. Write-only: no operation returns it.'}
        description: {type: string, maxLength: 500}
        expires_at: {type: string, format: date-time}
        validate: {type: boolean, description: Present the secret to the provider before storing it.}
    CredentialUpdate:
      type: object
      additionalProperties: false
      properties:
        label: {$ref: '#/components/schemas/Name'}
        description: {type: string, maxLength: 500}
        expires_at: {type: [string, "null"], format: date-time}
    CredentialSecret:
      type: object
      additionalProperties: false
      required: [secret]
      properties:
        secret: {type: string, minLength: 8, maxLength: 8192, description: 'The new provider API key. Write-only.'}
        expires_at: {type: string, format: date-time}
        validate: {type: boolean, description: Present the secret to the provider before storing it.}
    CredentialList:
      type: object
      additionalProperties: false
      required: [items]
      properties:
        items:
          type: array
          items: {$ref: '#/components/schemas/Credential'}
        next_cursor: {type: string, description: Absent on the last page.}
    RequiredReason:
      type: object
      additionalProperties: false
      required: [reason]
      properties:
        reason: {type: string, minLength: 1, maxLength: 500, description: Recorded in the audit log of the tenant and on the credential.}
```

## Appendix C — query files

### `internal/store/queries/sql/vault.sql` (new)

```sql
-- Key-encryption key versions and tenant data keys. The kek_version statements run in any
-- transaction; the data key statements in a tenant session.

-- name: RegisterKEKVersion :exec
INSERT INTO pandokos.kek_version (id, provider)
VALUES (sqlc.arg(id)::text, sqlc.arg(provider)::text)
ON CONFLICT (id) DO NOTHING;

-- name: ListKEKVersions :many
SELECT id, provider FROM pandokos.kek_version ORDER BY id;

-- name: CreateDataKey :execrows
INSERT INTO pandokos.tenant_data_key (tenant_id, version, kek_version, wrapped_key)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(version)::integer, sqlc.arg(kek_version)::text, sqlc.arg(wrapped_key)::bytea)
ON CONFLICT (tenant_id, version) DO NOTHING;

-- name: GetDataKey :one
SELECT version, kek_version, wrapped_key
  FROM pandokos.tenant_data_key
 WHERE version = sqlc.arg(version)::integer;
```

### `internal/store/queries/sql/credentials.sql` (new)

```sql
-- Provider credentials. Every statement runs in a tenant session. The statements of the admin
-- role read pandokos.provider_credential_metadata or name metadata columns only: that role
-- cannot read the sealed secret. The two ForUse statements are the gateway's.

-- name: NewCredentialID :one
SELECT pandokos.uuid_v7()::uuid AS id;

-- name: CreateCredential :one
INSERT INTO pandokos.provider_credential (tenant_id, id, provider, label, description, is_default, key_version,
                                          secret, fingerprint, hint, expires_at, validated_at)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(id)::uuid, sqlc.arg(provider)::text, sqlc.arg(label)::text,
        sqlc.arg(description)::text,
        NOT EXISTS (SELECT 1 FROM pandokos.provider_credential_metadata m
                     WHERE m.provider = sqlc.arg(provider)::text AND m.is_default),
        sqlc.arg(key_version)::integer, sqlc.arg(secret)::bytea, sqlc.arg(fingerprint)::text, sqlc.arg(hint)::text,
        sqlc.narg(expires_at)::timestamptz,
        CASE WHEN sqlc.arg(validated)::boolean THEN now() END)
RETURNING tenant_id, id, provider, label, description, state, is_default, key_version, fingerprint, hint, expires_at,
          validated_at, rotated_at, state_changed_at, state_changed_by, state_reason, created_at, updated_at;

-- name: GetCredential :one
SELECT m.tenant_id, m.id, m.provider, m.label, m.description, m.state, m.is_default, m.key_version, m.fingerprint,
       m.hint, m.expires_at, m.validated_at, m.rotated_at, m.state_changed_at, m.state_changed_by, m.state_reason,
       m.created_at, m.updated_at
  FROM pandokos.provider_credential_metadata m
 WHERE m.id = sqlc.arg(id)::uuid;

-- name: ListCredentials :many
SELECT m.tenant_id, m.id, m.provider, m.label, m.description, m.state, m.is_default, m.key_version, m.fingerprint,
       m.hint, m.expires_at, m.validated_at, m.rotated_at, m.state_changed_at, m.state_changed_by, m.state_reason,
       m.created_at, m.updated_at
  FROM pandokos.provider_credential_metadata m
 WHERE (sqlc.arg(state)::text = '' OR m.state = sqlc.arg(state)::text)
   AND (sqlc.arg(provider)::text = '' OR m.provider = sqlc.arg(provider)::text)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN m.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN m.id < sqlc.arg(after_id)::uuid
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN m.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN m.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: LockCredential :one
SELECT c.id, c.provider, c.label, c.state, c.is_default, c.fingerprint, c.hint
  FROM pandokos.provider_credential c
 WHERE c.id = sqlc.arg(id)::uuid
   FOR UPDATE;

-- name: UpdateCredential :one
UPDATE pandokos.provider_credential
   SET label       = CASE WHEN sqlc.arg(set_label)::boolean THEN sqlc.arg(label)::text ELSE label END,
       description = CASE WHEN sqlc.arg(set_description)::boolean THEN sqlc.arg(description)::text ELSE description END,
       expires_at  = CASE WHEN sqlc.arg(set_expires_at)::boolean THEN sqlc.narg(expires_at)::timestamptz ELSE expires_at END
 WHERE id = sqlc.arg(id)::uuid
RETURNING tenant_id, id, provider, label, description, state, is_default, key_version, fingerprint, hint, expires_at,
          validated_at, rotated_at, state_changed_at, state_changed_by, state_reason, created_at, updated_at;

-- name: ReplaceCredentialSecret :one
UPDATE pandokos.provider_credential
   SET key_version  = sqlc.arg(key_version)::integer,
       secret       = sqlc.arg(secret)::bytea,
       fingerprint  = sqlc.arg(fingerprint)::text,
       hint         = sqlc.arg(hint)::text,
       expires_at   = sqlc.narg(expires_at)::timestamptz,
       validated_at = CASE WHEN sqlc.arg(validated)::boolean THEN now() END
 WHERE id = sqlc.arg(id)::uuid
   AND state <> 'revoked'
RETURNING tenant_id, id, provider, label, description, state, is_default, key_version, fingerprint, hint, expires_at,
          validated_at, rotated_at, state_changed_at, state_changed_by, state_reason, created_at, updated_at;

-- name: SetCredentialState :one
UPDATE pandokos.provider_credential
   SET state            = sqlc.arg(state)::text,
       state_changed_by = sqlc.arg(changed_by)::text,
       state_reason     = sqlc.arg(reason)::text
 WHERE id = sqlc.arg(id)::uuid
RETURNING tenant_id, id, provider, label, description, state, is_default, key_version, fingerprint, hint, expires_at,
          validated_at, rotated_at, state_changed_at, state_changed_by, state_reason, created_at, updated_at;

-- name: ClearDefaultCredential :one
UPDATE pandokos.provider_credential
   SET is_default = false
 WHERE provider = sqlc.arg(provider)::text
   AND is_default
RETURNING id;

-- name: SetDefaultCredential :one
UPDATE pandokos.provider_credential
   SET is_default = true
 WHERE id = sqlc.arg(id)::uuid
RETURNING tenant_id, id, provider, label, description, state, is_default, key_version, fingerprint, hint, expires_at,
          validated_at, rotated_at, state_changed_at, state_changed_by, state_reason, created_at, updated_at;

-- name: CountOtherCredentials :one
SELECT count(*)::bigint AS n
  FROM pandokos.provider_credential_metadata m
 WHERE m.provider = sqlc.arg(provider)::text
   AND m.id <> sqlc.arg(id)::uuid
   AND m.state <> 'revoked';

-- name: CountUsablePins :one
SELECT count(*)::bigint AS n
  FROM pandokos.virtual_key k
 WHERE k.credential_id = sqlc.arg(credential_id)::uuid
   AND k.revoked_at IS NULL
   AND (k.expires_at IS NULL OR k.expires_at > now());

-- name: DeleteCredential :execrows
DELETE FROM pandokos.provider_credential
 WHERE id = sqlc.arg(id)::uuid;

-- name: GetCredentialForUse :one
SELECT c.id, c.provider, c.label, c.state, c.key_version, c.secret, c.fingerprint
  FROM pandokos.provider_credential c
 WHERE c.id = sqlc.arg(id)::uuid;

-- name: GetDefaultCredentialForUse :one
SELECT c.id, c.provider, c.label, c.state, c.key_version, c.secret, c.fingerprint
  FROM pandokos.provider_credential c
 WHERE c.provider = sqlc.arg(provider)::text
   AND c.is_default;
```

### `internal/store/queries/sql/virtual_keys.sql` (changed)

- `GetKeyPrincipal`: add `k.credential_id` after `k.revoked_at` in the select list.
- `CreateVirtualKey`: add the column `credential_id` and the value
  `sqlc.narg(credential_id)::uuid` at the end of both lists; add `credential_id` to
  `RETURNING` after `created_at`.
- `GetVirtualKey`, `ListVirtualKeys`: add `k.credential_id` after `k.created_at`.

`sqlc` gives the member the type `*string` in all four.
