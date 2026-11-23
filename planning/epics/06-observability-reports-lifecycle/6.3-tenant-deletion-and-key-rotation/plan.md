# Sprint 6.3 — Implementation plan

- Design: [sprint.md](sprint.md). Section numbers (§) refer to it; T-numbers are its tests, D-numbers
  its decisions, M-numbers its instruments.
- Each part below is self-contained: an agent needs sprint.md and that part only. Parts run in order
  A, B, C1 … C9, D, E, F.
- Suggested grouping: **G1** = C1, C2 (key versions, activation, re-wrap); **G2** = C3, C4, C5 (the
  deletion job, its API, retention of kept records); **G3** = C6, C7 (the export and its schema);
  **G4** = C8 (command line). After part B the groups do not depend on each other, except that C8
  needs the generated client of C4 and C6. C9 is one agent after them. A, B, D, E and F are one agent
  each. Because the risk class is high, A1 (the migration), C1 and C2 (key material) and C3 with C4
  (destruction of data) are reviewed by an agent that did not write them, with §5.2 to §5.5, §5.8 to
  §5.10 and §6 as the checklist.

## Rules for the implementer

1. Work on the branch the orchestrator names; do not create one. Sign off every commit
   (`git commit -s`) in the maintainer's name. No co-author or attribution lines, no tool footers and
   no session links in any commit message, pull request text, comment or file. One commit for A, one
   for B (tests, failing), one per step of C (message given with the step), one for D, one for E, one
   for F if something is found.
2. **One migration (A1), copied byte for byte**: its SHA-256 as a file is given with it; a different
   digest means a wrong copy. A merged migration is immutable (ADR 0026); until the pull request
   merges, a correction changes the file and its line in `migrations.sum` together. If a statement of
   A1 or A2 does not work as written, stop and ask.
3. **No new error code and no new module in the product.** The only new permissions are
   `tenant:delete` and `kek:read`; the only new keys the three of §5.15; the only new definer
   functions the five of A1; the only new dependency `github.com/santhosh-tekuri/jsonschema/v6` in
   `test/sdk/go.mod`.
4. **The database deletes; Go does not.** No Go code issues a `DELETE` or an `UPDATE` on a tenant's
   rows for a deletion, sets `pandokos.erasure` or `pandokos.retention`, or changes the order of the
   steps: the job calls `tenant_erasure_step` and nothing else (T-R77). Do not weaken a trigger, a
   policy, a grant or a check to make a test pass.
5. **The platform reads no usage of a tenant** (ADR 0054), in a deletion too: no count of ledger
   records, limit events or rollup rows leaves the tenant's session except the two counts of
   `deleted_tenant_retention`, which go to M44 only.
6. **Key material**: a key is a `secret.Secret` from the file to the cipher; every type that holds
   one prints the redaction marker on every formatting path; an unwrapped data key is cleared
   (`clear`) when the transaction that used it ends; no error quotes a path's content or a key.
   Only `internal/vault` calls `KeyProvider.Wrap` or `Unwrap`; only `kek_activate` and `kek_retire`
   write `kek_version` (T-R78).
7. Every statement runs as `queries.New(tx).<Method>` inside a `store` transaction helper (T-R10,
   T-R12). Generated files are never edited by hand (`make generate`). Every new route declares its
   permission in the specification and gets its negative tests in the same commit as its handler.
   Every change by the admin role writes its audit row in its transaction.
8. Logs, metrics and spans hold no tenant data (ADR 0055): only the lines of §5.14 and M47–M51 are
   added. No body, header value, credential, key secret or real key in a test, fixture or document;
   canaries from `fixtures.Canaries()`.
9. Tests hold for root and non-root; nothing sleeps longer than 2 s; waiting is done by polling with
   a deadline; the two-minute settle is passed with `dbtest.AgeDeletion`, never by waiting. Database
   tests use `dbtest` and skip without `PANDOKOS_TEST_DATABASE_URL` as the others do.

## Environment

- Go as in `go.mod` (`go 1.26.0`), the toolchain of ADR 0025; `make generate` runs apigen, permdoc and
  `sqlc` v1.31.1 (pinned in the `Makefile`).
- The test database: `make db-up`, then `set -a; . local/testdb/env; set +a`.
- `make check` before every push of part F.

## Part A — Scaffolding (commit: `chore: sprint 6.3 scaffolding: migration 0016, queries, API specification, permissions, configuration keys`)

### A1. Migration — `internal/store/migrations/sql/0016_tenant_deletion_kek_rotation.sql`

The file is appendix A1, byte for byte (842 lines, 49284 bytes, ending with one newline). Then
`make migrations-sum`.

```
sha256sum internal/store/migrations/sql/0016_tenant_deletion_kek_rotation.sql
# c27133df3892c10b257990d0ad756db2a87385feca0f2348b123875cf6c21309
```

`deploy/postgres/recover-lost-kek.sql`: unchanged statements; its header gains one sentence: after
it, no version may be active, and the next start of `serve` activates the new key.

### A2. Queries

New `internal/store/queries/sql/lifecycle.sql` (header: which scope each statement needs):

```sql
-- name: InsertTenantDeletion :one          -- admin, platform scope
INSERT INTO pandokos.tenant_deletion (tenant_id, reason, requested_by_type, requested_by_id, requested_by_name, request_id, not_before)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(reason)::text, sqlc.arg(requested_by_type)::text, sqlc.arg(requested_by_id)::text,
        sqlc.arg(requested_by_name)::text, sqlc.arg(request_id)::text, now() + sqlc.arg(grace)::interval)
RETURNING tenant_id, state, step, reason, requested_by_type, requested_by_id, requested_by_name, request_id, requested_at,
          not_before, started_at, completed_at, passes, kek_versions, removed, audit, completion_seq;

-- name: GetTenantDeletion :one             -- admin, platform scope
SELECT d.tenant_id, d.state, d.step, d.reason, d.requested_by_type, d.requested_by_id, d.requested_by_name, d.request_id,
       d.requested_at, d.not_before, d.started_at, d.completed_at, d.passes, d.kek_versions, d.removed, d.audit, d.completion_seq,
       ARRAY(SELECT v.id FROM pandokos.kek_version v WHERE v.id = ANY (d.kek_versions) AND v.state <> 'retired' ORDER BY v.id)::text[] AS pending_kek_versions
  FROM pandokos.tenant_deletion d
 WHERE d.tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ListTenantDeletions :many          -- admin, platform scope; newest request first
-- The columns of GetTenantDeletion, WHERE (sqlc.arg(state)::text = '' OR d.state = sqlc.arg(state)::text)
--   AND (NOT sqlc.arg(after)::boolean OR (d.requested_at, d.tenant_id) < (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::uuid))
-- ORDER BY d.requested_at DESC, d.tenant_id DESC LIMIT sqlc.arg(page_size)::integer;

-- name: DeleteScheduledTenantDeletion :execrows   -- admin, platform scope
DELETE FROM pandokos.tenant_deletion WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND state = 'scheduled';

-- name: ListOpenTenantDeletions :many      -- worker, platform scope
SELECT d.tenant_id, d.request_id, d.started_at FROM pandokos.tenant_deletion d
 WHERE d.state = 'running' OR (d.state = 'scheduled' AND d.not_before <= now())
 ORDER BY d.not_before, d.tenant_id;

-- name: CountTenantDeletions :many         -- worker, platform scope
SELECT d.state, count(*)::bigint AS n FROM pandokos.tenant_deletion d WHERE d.state <> 'completed' GROUP BY d.state;

-- name: CountPendingErasures :one          -- worker, platform scope
SELECT count(*)::bigint FROM pandokos.tenant_deletion d
 WHERE d.state = 'completed'
   AND EXISTS (SELECT 1 FROM pandokos.kek_version v WHERE v.id = ANY (d.kek_versions) AND v.state <> 'retired');

-- name: TenantErasureStep :one             -- worker, session of the tenant
SELECT pandokos.tenant_erasure_step(sqlc.arg(batch)::integer)::text AS next;

-- name: ListDeletedTenantsForWorker :many  -- worker, platform scope
SELECT t.id FROM pandokos.tenant t WHERE t.state = 'deleted' ORDER BY t.id;

-- name: DeletedTenantRetention :one        -- worker, session of a deleted tenant
SELECT r.usage_rows, r.audit_rows
  FROM pandokos.deleted_tenant_retention(sqlc.arg(usage_months)::integer, sqlc.arg(audit_months)::integer) r;
```

`ListTenantDeletions` is written out in full with the column list of `GetTenantDeletion`.

`internal/store/queries/sql/vault.sql`: **remove** `RegisterKEKVersion`; replace `ListKEKVersions`;
add four statements:

```sql
-- name: ListKEKVersions :many              -- any transaction
SELECT v.id, v.provider, v.state, v.created_at, v.activated_at, v.retired_at FROM pandokos.kek_version v ORDER BY v.activated_at, v.id;

-- name: ListKEKVersionsWithErasures :many  -- admin, platform scope
SELECT v.id, v.provider, v.state, v.created_at, v.activated_at, v.retired_at,
       (SELECT count(*) FROM pandokos.tenant_deletion d
         WHERE d.state = 'completed' AND v.state <> 'retired' AND v.id = ANY (d.kek_versions))::bigint AS erasures_pending
  FROM pandokos.kek_version v ORDER BY v.activated_at, v.id;

-- name: ActivateKEKVersion :one            -- admin, platform scope
SELECT pandokos.kek_activate(sqlc.arg(id)::text, sqlc.arg(provider)::text)::text AS result;

-- name: RetireKEKVersions :one             -- worker, platform scope
SELECT pandokos.kek_retire()::integer AS retired;

-- name: ListTenantsForRewrap :many         -- worker, platform scope
SELECT t.id FROM pandokos.tenant t WHERE t.state <> 'deleted' ORDER BY t.id;

-- name: LockDataKeysForRewrap :many        -- worker, session of the tenant
SELECT k.version, k.kek_version, k.wrapped_key FROM pandokos.tenant_data_key k
 WHERE k.kek_version <> sqlc.arg(active)::text ORDER BY k.version FOR UPDATE;

-- name: RewrapDataKey :execrows            -- worker, session of the tenant
UPDATE pandokos.tenant_data_key
   SET kek_version = sqlc.arg(kek_version)::text, wrapped_key = sqlc.arg(wrapped_key)::bytea
 WHERE version = sqlc.arg(version)::integer AND kek_version = sqlc.arg(old_kek_version)::text;
```

`internal/store/queries/sql/reports.sql`: `ListTenantsForWorker` lists `('active', 'suspended')`.
The kept log is read with the existing `GetAuditHead` and `ExportAuditEvents` in the tenant's session:
no new statement.

`make generate`, `go build ./...`.

### A3. API specification — `api/openapi/admin.v1.yaml`

Appendix A3: seven paths' operations and the components. The permission descriptions of §5.12.
`make generate`; the server interface gains seven methods (stubs in A7).

### A4. Isolation harness — `test/isolation/{spec,lint}.go`

- `Global`: `"tenant_deletion": {Reason: "the request and the progress of the deletion of a tenant;
  platform scope only; written by the erasure function", Restricted: true}`, `"tenant_closed":
  {Reason: "the tenants that take no new row; a session reads its own tenant's row, the platform scope
  all; written by the erasure function", Restricted: true}`.
- `Grants`: `kek_version` admin `{"SELECT"}`; `tenant_data_key` worker `{"SELECT",
  "UPDATE(kek_version,wrapped_key)"}`; `tenant_closed` the three roles `{"SELECT"}`;
  `tenant_deletion` admin `{"DELETE", "INSERT(not_before,reason,request_id,requested_by_id,requested_by_name,requested_by_type,tenant_id)",
  "SELECT(audit,completed_at,completion_seq,kek_versions,not_before,passes,reason,removed,request_id,requested_at,requested_by_id,requested_by_name,requested_by_type,settle_at,started_at,state,step,tenant_id)"}`,
  worker `{"SELECT(kek_versions,not_before,request_id,started_at,state,step,tenant_id)"}`.
- `Fixtures`: `tenant_data_key` becomes `INSERT INTO pandokos.tenant_data_key (tenant_id, kek_version,
  wrapped_key) SELECT $1::uuid, v.id, decode(repeat('00', 61), 'hex') FROM pandokos.kek_version v WHERE
  v.state = 'active' RETURNING id::text` (the harness has an active version, B1).
- `SecurityDefiner`: add `"kek_activate", "kek_retire", "tenant_deletion_holds", "tenant_erasure_step",
  "deleted_tenant_retention"`.
- `lint.go`: rule **L-21** (§6): for every table with a policy named `tenant_isolation` that is not a
  partition, and for `idempotency_key`, a `BEFORE INSERT` statement trigger `<table>_tenant_open`
  executing `tenant_open`; for `app_user`, `model_tenant_grant`, `platform_issuer_tenant`,
  `platform_limit_rule`, `support_action`, a `BEFORE INSERT` row trigger of that name executing
  `tenant_open_row`. The header comment of `Lint` counts twenty-one rules.

`go test ./test/isolation -run 'TestSchemaLint$'` passes after A1 and A4.

### A5. Permissions — `internal/authz/{authz,roles}.go`, `testdata/roles.golden`

Constants `TenantDelete = "tenant:delete"`, `KEKRead = "kek:read"`; catalogue rows `{TenantDelete,
ScopePlatform, "Requesting and cancelling the deletion of a tenant, with a reason", false, false}` and
`{KEKRead, ScopePlatform, "Reading the versions of the key-encryption key and their state", false,
false}`; `tenant:read` reads "Reading and listing tenants, with their time zone, their active currency
and their deletion". `platform-auditor` gains `KEKRead` (`platform-admin` holds both by construction).
`make generate` (permdoc); update the golden file with `-update` and read the diff: two permissions on
two roles.

### A6. Configuration — `internal/config/{config,keys,validate}.go`

`serveDef` rows of §5.15; `TenantDeletion.GracePeriod`, `TenantDeletion.Interval` (`Duration`),
`Vault.KEK.Local.UnwrapKeyFiles []string` with `UnwrapKeys []secret.Secret` filled by `Load` when the
purpose is `serve` (read as `key_file` is: `readLimited`, the permission warning per file); the
validation of §5.15.

### A7. Types and stubs (no behaviour)

- `internal/vault`: `func NewLocalProvider(active secret.Secret, unwrap ...secret.Secret)
  (*LocalProvider, error)`; `func (p *LocalProvider) Versions() []string` (active first);
  `var ErrKeyReplaced = errors.New("vault: the configured key-encryption key version was replaced")`;
  `func Activate(ctx, admin, worker *store.DB, p KeyProvider) (version, result string, err error)`
  (retire, check, activate, §5.8); `type Rewrapper struct{ DB *store.DB; Provider KeyProvider; Logger
  *slog.Logger }` with `Run(ctx)`, `Pass(ctx) error`, `Stats() RewrapStats{OK, Failed int64; Versions
  map[string]int64; ErasuresPending int64}`. `KeyProvider` keeps its four methods.
- `internal/lifecycle`: `type Deleter struct{ DB *store.DB; Interval time.Duration; Logger
  *slog.Logger; Now func() time.Time }` with `Run(ctx)`, `Pass(ctx) error`, `Stats() DeleterStats{Steps
  map[[2]string]int64; Open map[string]int64}`; constants `Batch = 5000`, `tenantBudget = 30 *
  time.Second`.
- `internal/reports.Retention`: nothing new in its type; `Pass` gains the third pass (C5).
- `internal/tenantexport`: `type Kind struct{ Name, File, Def string }`, `var Kinds []Kind` (§5.11, in
  the order of the table), `func Write(ctx, c *adminapi.Client, tenant, dir string, opt Options)
  (Manifest, error)`, `type Manifest struct{…}` (the members of §5.11), `//go:embed
  tenant-export.v1.schema.json` as `Schema []byte` (the file of `docs/reference`, copied by `make
  generate`).
- `internal/admin`: the seven methods as stubs.
- `internal/telemetry`: `Sources.Lifecycle func() LifecycleStats`, the attribute keys
  `pandokos.deletion.step`, `pandokos.deletion.state`, `pandokos.kek.state` with their closed values.

## Part B — Tests first (commit: `test: sprint 6.3 tests, failing`)

### B1. Helpers and fixtures

- `test/dbtest`: every database it creates gets an active version (`ActivateKEKVersion` with the test
  key's version, as the admin role in platform scope) before any fixture runs; `PopulatedTenant(t)
  tenant.ID` (§7), `TableDigests(t, tenant) map[string]string` (per table, `md5` of the ordered
  `row_to_json` of the tenant's rows, as the superuser; and of the whole table for the global ones),
  `RequestDeletion(t, tenant, notBefore)`, `AgeDeletion(t, tenant)` (superuser: `not_before` and
  `settle_at` to now).
- `test/vaulttest`: `Keys(n)` returns n key files in a temporary directory with their versions.
- `test/e2e`: `startBinary` options `kekFiles`, `gracePeriod`, `deletionInterval`; `CopyDatabase(t)
  url` (`CREATE DATABASE … TEMPLATE …` as the superuser, with the source's connections closed first).
- `test/sdk/go.mod`: `go get github.com/santhosh-tekuri/jsonschema/v6@v6.0.3`, `go mod tidy`; check
  `go list -m -versions github.com/santhosh-tekuri/jsonschema/v6` still ends at a `v6.0.x` and record
  the version taken in the commit message.

### B2. Where each test lives

| Tests | File |
|---|---|
| T-977, T-978 | `test/isolation/tenant_open_test.go`, `tenant_deletion_test.go` |
| T-979 to T-984 | `test/isolation/erasure_test.go` |
| T-985 | `test/isolation/kek_test.go` |
| T-986 | `internal/store/migrations/upgrade_0016_db_test.go` |
| T-987 to T-992 | `internal/vault/{local_test,activate_db_test,rewrap_db_test}.go` |
| T-993 to T-995 | `internal/lifecycle/deleter_db_test.go` |
| T-996 to T-1000 | `internal/admin/{tenant_deletion,kek_versions,closed_tenant}_db_test.go` |
| T-1001, T-1002 | `internal/tenantexport/{writer,schema}_test.go` |
| T-1003 | `test/sdk/tenant_export_test.go` |
| T-1004 to T-1007, T-1011 | `test/e2e/{tenant_export,deletion,rotation}_test.go` |
| T-1008 to T-1010, T-1012 | `internal/config`, `internal/telemetry`, `internal/cli/lifecycle_test.go`, `internal/reports/retention_test.go` |
| T-R77 to T-R80 | `test/repo/lifecycle_test.go` |

The changed tests of §9 are changed in this commit. T-979 builds its list of tables from
`pg_attribute` (every ordinary or partitioned table of schema `pandokos` with a column `tenant_id`,
partitions excluded) and compares it with the groups of §5.3 written as a Go table in the test.

Verification: `go vet ./...`; `go test ./... 2>&1 | grep -c '^--- FAIL'` is not zero and every new
test fails for the missing behaviour, not for a compile error.

## Part C — Implementation

### C1. Several keys and activation — `feat: key-encryption key versions with a state; activation at start`

Files: `internal/vault/{local,check,activate,sealer}.go`, `cmd/pandokos/serve.go`,
`internal/config`, `test/dbtest`.

- `LocalProvider`: the map already keys by version; load every unwrap key; the same version twice is
  `ErrInvalidKey`; `Wrap` uses the active one.
- `Activate` (§5.8): `RetireKEKVersions` on the worker pool; the check of `vault.Check` over
  `ListKEKVersions` for rows `active` or `retiring` only; `ActivateKEKVersion` on the admin pool;
  the constraint `kek_version_replaced` becomes `ErrKeyReplaced` with both versions in the message.
  `Check` stays for a process that must not write (the gateway alone, 8.2), reading the same rows.
- `Sealer.EnsureDataKey`: no registration; the constraint `tenant_data_key_kek_active` becomes a
  `SealError` with the cause `kek_replaced`; `tenant_open` is returned as it is (C4 maps it).
- `serve`: builds the provider from `Key` and `UnwrapKeys`, calls `Activate`, logs `vault ready`
  with the fields of §5.14.

Tests: T-987, T-988, T-992, T-1008. `go test ./internal/vault ./internal/config ./cmd/pandokos`.

### C2. Re-wrap — `feat: the worker re-wraps data keys and retires replaced key versions`

Files: `internal/vault/rewrap.go`, `cmd/pandokos/serve.go` (the goroutine, after the listener).

One pass (§5.9): `ListKEKVersions` in platform scope (stats); when a version is `retiring`:
`ListTenantsForRewrap`; per tenant `db.WithTenant`: `LockDataKeysForRewrap(active)`, for each row
`Unwrap(old)`, `Wrap` (the returned version must be the active one, else stop the pass),
`Unwrap(new)` and `subtle.ConstantTimeCompare`, `RewrapDataKey` (one row, else an error), `clear` of
the plaintext in a `defer`; after a pass without failure `RetireKEKVersions` and one `key rotation
complete` per retired version; `CountPendingErasures` for M51. The tick is one minute; the pass has
no budget (a re-wrap is a few milliseconds per tenant) but stops when `ctx` ends, between tenants.

Tests: T-989, T-990, T-991, T-999 (the list, with C4). `go test ./internal/vault -run Rewrap`.

### C3. The deletion job — `feat: staged tenant deletion run by the worker`

Files: `internal/lifecycle/{doc,deleter}.go`, `cmd/pandokos/serve.go`.

`Pass` (§5.7): `ListOpenTenantDeletions` and `CountTenantDeletions` in platform scope; per tenant, a
loop of `db.WithTenant(id, TenantErasureStep(Batch))` until `done`, `scheduled`, `settling` or the
budget. M47 counts one step for a call whose answer differs from the step before it, and one `usage`
step for each call made at `usage`; a failed call counts `failed` for the step it was at. The lines
of §5.14: `tenant deletion started` when a call at `begin` answers `access`; `tenant deletion
completed` on `done`, with `duration_ms` since `started_at` (for a deletion begun in this pass, since
the call at `begin`); `failing` and `recovered` as the rollup job does them. The request ID comes
from the listed row; nothing else of the tenant is logged.

Tests: T-993, T-994, T-995. `go test ./internal/lifecycle`.

### C4. The API — `feat: request, cancel and read a tenant deletion; the kept audit log; key versions`

Files: `internal/admin/{tenant_deletion,kek_versions,problem,tenants}.go`.

- `RequestTenantDeletion`: `db.Platform`; `GetTenantForUpdate`; `GetTenantDeletion` (exists: answer
  it); state checks of §5.1; `confirm_name`; `SetTenantState(suspended)` when active;
  `InsertTenantDeletion` with the grace period as an interval; `audit.RecordPlatform` with the action
  `tenant.delete_request`, the reason and the detail `from`, `not_before`.
- `CancelTenantDeletion`: `DeleteScheduledTenantDeletion`; 0 rows with a deletion present is
  `errInvalidState`, with none `errNotFound`; `tenant.delete_cancel`.
- `GetTenantDeletion`, `ListTenantDeletions` (cursor bound to the filter, as the other lists).
- `GetDeletedTenantAuditHead`, `ExportDeletedTenantAuditEvents`: `db.Platform` reads the tenant
  (404 unknown, 409 unless `deleted`), then the existing head and export code in `db.WithTenant`; the
  export counts M46 with the kind `deleted_tenant_audit`; details are never withheld (they are `{}`).
- `ListKEKVersions`: `ListKEKVersionsWithErasures`.
- `problemOf`: the constraints of §5.16; `transition` answers the `errors` entry for
  `tenant_deletion_holds`.

Tests: T-996 to T-1000. `go test ./internal/admin -run 'Deletion|KEK|Closed|Lifecycle'`.

### C5. Retention of kept records — `feat: retention applies to the records kept for deleted tenants`

Files: `internal/reports/{retention,rollup}.go`. The third pass of §5.6 after the two existing ones,
with their failure rule (a failing tenant does not stop the others); M44's two new kinds.

Tests: T-984 (database), T-1012. `go test ./internal/reports ./test/isolation -run 'Retention'`.

### C6. The export schema — `feat: the JSON Schema of the tenant data export, generated from the API specification`

Files: `internal/apigen/main.go` (flag `-export-schema OUT`), `Makefile` (`generate` writes
`docs/reference/tenant-export.v1.schema.json` and copies it to `internal/tenantexport/`),
`internal/tenantexport/schema.go`.

The generator reads `tenantexport.Kinds` as a table in apigen (kind, operation whose `items` schema,
or whose `200` schema, is the kind's) and writes: `$schema` (`https://json-schema.org/draft/2020-12/schema`),
`$id` (`urn:pandokos:schema:tenant-export:v1`, in the form of the problem types), `title`, the manifest's
schema at the root, and under `$defs` each kind plus every component schema it references, with
`#/components/schemas/X` rewritten to `#/$defs/X`. `x-pandokos-*` members are dropped. `settings` is
an object of four members, each the schema of its operation's answer. Deterministic output (sorted
keys, two-space indentation, one final newline).

Tests: T-1002. `make generate-check`.

### C7. The export — `feat: pandokos tenant-export`

Files: `internal/tenantexport/writer.go`. §5.11: per kind, every page of the list (the cursor of the
generated client) written as the JSON of each item, one per line, exactly as received
(`json.RawMessage` from the client; if the generated client only exposes typed items, add a raw
variant to apigen in this step and say so in the commit); usage by `exportUsageRecords` windows;
audit by head then export; SHA-256 and counts while writing; `manifest.json` last; rename.

Tests: T-1001, T-1003, T-1004. `go test ./internal/tenantexport`; `cd test/sdk && go test -run TenantExport ./...`.

### C8. Command line — `feat: CLI for tenant deletion, key versions and the tenant export`

Files: `internal/cli/{lifecycle,tenant_export,commands}.go`, `cmd/pandokos/main.go` (usage text).
The commands of §5.13; `tenant deletion verify` as §5.5 with `auditchain.Verify`.

Tests: T-1010, T-219. `go test ./internal/cli`.

### C9. Metrics, wiring and end to end — `feat: lifecycle instruments and log lines; end-to-end deletion and rotation`

Files: `internal/telemetry/{instruments,sources}.go`, `cmd/pandokos/{serve,metrics}.go`,
`docs/reference/{logs,metrics}.md` (rows only; prose in E), `test/e2e`.

Tests: T-1005, T-1006, T-1007, T-1009, T-1011, the changed e2e upgrade test. `go test ./test/e2e -run
'Deletion|Rotation|Backup|Lifecycle'`.

## Part D — Repository tests and CI (commit: `ci: sprint 6.3 repository tests; a database for the sdk job`)

- `test/repo/lifecycle_test.go`: T-R77 to T-R80; T-R71 gains its exception (the two operations of
  the kept log); `TestConfigurationReference`, `TestLogReference`, `TestMetricsReference` updated.
- `.github/workflows/ci.yml`, job `sdk`: the steps "Start PostgreSQL and PgBouncer" and "Database
  logs" copied from the job `test`, with the same environment; no action is added, so no new pin.
- `Makefile`: `test-sdk` unchanged (it needs `make db-up` now; its help text says so).

Verification: `make check`; `cd test/sdk && go vet ./... && go mod tidy -diff && go test -race -count=1 ./...`.

## Part E — Documentation and decision records (commit: `docs: sprint 6.3 tenant deletion, key rotation, export, backup and failure references`)

### E1. Guides and references

| File | Content |
|---|---|
| `docs/operations/tenant-deletion.md` (new) | The procedure (announce, export, request, grace, cancel), the permission, what each step removes (§5.3 as a table), what is kept and how it is pseudonymized, the completion record and `tenant deletion verify`, retention of kept records, erasure and backups (§5.10, the operator's rule), how fast access ends, what a grace period of 0 means, the residue query for a database administrator, limits |
| `docs/operations/backup-and-restore.md` (new) | §5.17 |
| `docs/operations/tenant-export.md` (new) | Who runs it and with which permissions, the files, the manifest, the schema and how to validate with any JSON Schema 2020-12 validator, what is not in it (secrets), the version rule |
| `docs/reference/failure-behaviour.md` (new) | §5.17; `docs/architecture/overview.md` §9 keeps its table and points here |
| `docs/operations/vault.md` | §2 and §4 (several files, what to back up), §6 (compromise: rotate), a new "Rotating the key" (one instance; several instances; what to watch: `key rotation complete`, `kek-version list`, M49 to M51; when to destroy the old file; old backups), §9 (the limits R-6 and O-8 are removed) |
| `docs/operations/{database,usage,audit,configuration}.md`, `docs/reference/{logs,metrics,permissions,admin-api,data-flows}.md` | §5.18 |
| `docs/architecture/{data-model,overview,security-model}.md`, `docs/requirements/README.md` (VLT-11) | §5.18 |
| `README.md`, `docs/operations` index if any | Links to the four new pages |

#### E1b. Release notes entry (text to add under Epic 6 of `docs/releases/v0.2.0-draft.md`)

> **Sprint 6.3, tenant deletion and key rotation** ([deletion guide](../operations/tenant-deletion.md),
> [vault guide](../operations/vault.md), [backup and restore](../operations/backup-and-restore.md),
> [failure behaviour](../reference/failure-behaviour.md), [tenant export](../operations/tenant-export.md)).
>
> - **A tenant can be deleted.** `pandokos tenant delete` (permission `tenant:delete`, a reason and the
>   tenant's name) suspends the tenant and schedules its deletion after `tenant_deletion.grace_period`
>   (7 days); the request can be cancelled until then. The worker then removes access, destroys the
>   data key and the credentials, removes the configuration, keeps the usage ledger and the audit log
>   pseudonymized and writes a completion record in the platform's audit chain;
>   `pandokos tenant deletion verify` checks it.
> - **The key-encryption key can be rotated**: put the new key in `vault.kek.local.key_file`, the old
>   one in the new `vault.kek.local.unwrap_key_files`, restart; the worker re-wraps every data key and
>   retires the old version (`pandokos kek-version list`, permission `kek:read`). Credentials are
>   untouched.
> - **Erasure and backups, stated exactly**: a deleted tenant's credentials are gone from the database
>   at once, and from earlier backups once the key version that wrapped its data key is retired and its
>   file destroyed. VLT-11 and ADR 0008 say so now.
> - **A tenant exports its data** with `pandokos tenant-export`: NDJSON files and a manifest, with a
>   published JSON Schema (`pandokos tenant-export schema`).
> - **Migration 0016**, three keys (`tenant_deletion.grace_period`, `tenant_deletion.interval`,
>   `vault.kek.local.unwrap_key_files`), seven operations, five instruments (M47 to M51, 51 in all).
>   `serve` now records the key it starts with as the active version; `vault ready` has two new fields.

Add `0016_tenant_deletion_kek_rotation` to the table of the upgrade notes ("key versions with a
state, the deletion of tenants, the guard of closed tenants (6.3)") and extend "Migrations 0011 to
0015 are not compatible with the earlier binaries" to 0016; under "Known limits": the kept ledger of
a deleted tenant has no reader; no automatic rotation; a restore is documented, not yet drilled
(12.3); the tenant exports before the request.

### E2. Decision records

Three records in the form of `docs/adr/0000-template.md`, status `proposed`, date of the commit,
indexed in `docs/adr/README.md`:

- **`0067-tenant-deletion-stages-closed-tenants-and-kept-records.md`** — "Tenant deletion: stages,
  closed tenants and the records that are kept". Refines ADR 0013, 0027, 0054, 0065; amends ADR 0013
  ("rows are never updated": once, by the erasure function). Decision: D-1 to D-13 and D-22 as
  numbered points. Consequences: §11 for 6.4 and 10.1, R-3, R-4. Alternatives: cascading foreign keys
  (no order, no record, no pseudonymization); deleting in Go with grants to the worker (a standing
  right to delete any tenant); keeping the chain and its identifiers (personal data kept); a marker in
  the verifier for re-sealed chains (the platform's chain already says it); removing all records (O-1).
- **`0068-key-encryption-key-versions-rotation-and-erasure-in-backups.md`** — "Key-encryption key
  versions: activation, re-wrap, retirement, and erasure in backups". Refines ADR 0008 and 0033;
  **amends** ADR 0008 (the sentence "including in backups taken before the deletion" becomes §5.10
  points 1 and 2) and ADR 0033 point 5 (versions are registered by activation and have a state; `serve`
  needs the active and retiring ones). Decision: D-14 to D-17. Alternatives: an API operation that
  rotates (O-7); operator-chosen version labels (ADR 0033); counting data keys across tenants with a
  policy for the owner (the foreign key needs none); rotating by itself at each deletion (needs a key
  file nobody made).
- **`0069-tenant-data-export.md`** — "The tenant data export and its schema". Refines ADR 0054, 0064,
  0066. Decision: D-18, D-19, the version rule. Alternatives: a server-side archive (a platform path
  to a tenant's usage, or a long request); one file (kinds of very different size); the declarative
  document (configuration only, no records, no people); a hand-written schema (drifts from the API).

History lines (one each, dated, naming the new record): 0008 and 0033 → 0068; 0013, 0027, 0054, 0065
→ 0067; 0023 → the validator in `test/sdk` and its database; 0062 → M47 to M51; 0063 → the eight
lines.

### E3. Planning files and traceability

`planning/epics/06-observability-reports-lifecycle/epic.md`: under 6.3, a "*Deferred from 6.3*" note
with the rows of §2 that name a sprint; under 6.4, the note of §11. `planning/roadmap.md` is updated
when the sprint closes, not here.

Verification: `go test ./test/repo`; every relative link of the new pages resolves (`TestLinks` if
present, else `grep` the targets).

## Part F — Verification (no commit unless something is found)

1. `make check`; the `sdk` job's commands of part D; `make image` and the container tests if the
   image job is run locally.
2. **Upgrade**: a database of v0.1.x with data, migrated to 0015 by the binary of the branch before
   this sprint, then `pandokos migrate` to 0016 and `serve` with its existing key: `vault ready` shows
   `kek_activation: unchanged`; both audit chains verify; then a deletion and a rotation complete on
   it. Record the commands and the outputs in the pull request.
3. **The exit criteria by hand**, once, on that database: T-1006's scenario with `pg_dump` and
   `pg_restore` instead of a database copy; T-1007's; an export validated with a second JSON Schema
   validator of the reviewer's choice.
4. **Plans**: `EXPLAIN (ANALYZE, BUFFERS)` of one batch of `tenant_erasure_usage` on a ledger of 10⁶
   rows of one tenant (the batch must use the primary key, not a scan of the partition), and of
   `tenant_residue`; record them.
5. Read every new log line and metric of a run of T-1005 and T-1007 for a tenant ID, a name, a key
   or a count of one tenant.
6. Review by an agent that did not write them (the grouping at the top), with the checklist: no path
   erases a tenant without a request; no step touches another tenant or a global row; no plaintext
   key outlives its transaction; a replaced key cannot wrap; the completion record holds no usage
   figure.

## What the implementer does not do

- No deletion of platform-managed users, no change to the platform's chain, no change to the
  canonical encoding or the verifier's reasons.
- No event, outbox row or external reference (6.4); no external key provider (12.1); no restore
  drill (12.3).
- No per-tenant retention, no `records: remove` choice, no self-service deletion (O-1, O-4).
- No widening of a grant beyond §6 without asking; no change to migrations 0001 to 0015.

## Appendix A3 — additions to `api/openapi/admin.v1.yaml`

Paths (the tag `tenants` for the first six, a new tag `vault` — "Key-encryption key versions" — for
the last). Every operation also lists the responses `401`, `403`, `500` and `503` with the shared
references, as its neighbours do; they are left out below.

```yaml
  /v1/tenants/{tenant}/deletion:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    post:
      operationId: requestTenantDeletion
      tags: [tenants]
      summary: Request the deletion of a tenant
      description: >-
        Suspends an active tenant and schedules its deletion after the grace period of the installation
        (tenant_deletion.grace_period). confirm_name must be the tenant's name. Until not_before the request
        can be cancelled; then the worker removes the tenant's access, destroys its data key and its
        credentials, removes its configuration and keeps its usage ledger and audit log pseudonymized. A
        second request while one exists answers the existing deletion and changes nothing.
      x-pandokos-permission: tenant:delete
      x-pandokos-reason: required
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/TenantDeletionRequest'}
      responses:
        "200": {$ref: '#/components/responses/TenantDeletion'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
        "404": {$ref: '#/components/responses/NotFound'}
        "409": {$ref: '#/components/responses/Conflict'}
        "413": {$ref: '#/components/responses/RequestTooLarge'}
        "415": {$ref: '#/components/responses/UnsupportedMediaType'}
    get:
      operationId: getTenantDeletion
      tags: [tenants]
      summary: Read the deletion of a tenant
      description: Answers 404 when no deletion was requested for the tenant.
      x-pandokos-permission: tenant:read
      responses:
        "200": {$ref: '#/components/responses/TenantDeletion'}
        "404": {$ref: '#/components/responses/NotFound'}
  /v1/tenants/{tenant}/deletion/cancel:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    post:
      operationId: cancelTenantDeletion
      tags: [tenants]
      summary: Cancel a scheduled deletion
      description: >-
        Removes a deletion that has not started. The tenant stays suspended; reactivate it with
        reactivateTenant. A deletion that started cannot be cancelled (409).
      x-pandokos-permission: tenant:delete
      x-pandokos-reason: required
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/RequiredReason'}
      responses:
        "200": {$ref: '#/components/responses/Tenant'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
        "404": {$ref: '#/components/responses/NotFound'}
        "409": {$ref: '#/components/responses/Conflict'}
        "413": {$ref: '#/components/responses/RequestTooLarge'}
        "415": {$ref: '#/components/responses/UnsupportedMediaType'}
  /v1/tenant-deletions:
    get:
      operationId: listTenantDeletions
      tags: [tenants]
      summary: List the deletions of tenants, newest request first
      x-pandokos-permission: tenant:read
      parameters:
        - {$ref: '#/components/parameters/Limit'}
        - {$ref: '#/components/parameters/Cursor'}
        - name: state
          in: query
          description: Only the deletions in this state.
          schema: {$ref: '#/components/schemas/TenantDeletionState'}
      responses:
        "200": {$ref: '#/components/responses/TenantDeletionList'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
  /v1/tenants/{tenant}/deletion/audit-head:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: getDeletedTenantAuditHead
      tags: [tenants]
      summary: Read the head and the anchor of the audit chain kept for a deleted tenant
      description: Answers 409 until the tenant is deleted.
      x-pandokos-permission: platform_audit:read
      responses:
        "200": {$ref: '#/components/responses/AuditHead'}
        "404": {$ref: '#/components/responses/NotFound'}
        "409": {$ref: '#/components/responses/Conflict'}
  /v1/tenants/{tenant}/deletion/audit-events/export:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: exportDeletedTenantAuditEvents
      tags: [tenants]
      summary: Export the audit chain kept for a deleted tenant, oldest first, as NDJSON
      description: >-
        The pseudonymized events from the anchor of the chain to its head, one per line: actors, keys and
        resources are pseudonyms, names, reasons and details are empty. Answers 409 until the tenant is
        deleted. pandokos tenant deletion verify checks such an export against the completion record.
      x-pandokos-permission: platform_audit:read
      parameters:
        - {$ref: '#/components/parameters/AuditFromSeq'}
        - {$ref: '#/components/parameters/AuditToSeq'}
      responses:
        "200": {$ref: '#/components/responses/AuditEventExport'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
        "404": {$ref: '#/components/responses/NotFound'}
        "409": {$ref: '#/components/responses/Conflict'}
  /v1/kek-versions:
    get:
      operationId: listKEKVersions
      tags: [vault]
      summary: List the versions of the key-encryption key
      description: >-
        Every version that wrapped a data key of this installation, oldest activation first. Exactly one is
        active; a retiring version still wraps data keys and its key must stay configured; a retired version
        wraps none. The key itself is never returned.
      x-pandokos-permission: kek:read
      responses:
        "200": {$ref: '#/components/responses/KEKVersionList'}
```

Components (`components.responses`: `TenantDeletion`, `TenantDeletionList`, `KEKVersionList`, each
`application/json` with the schema of the same name; `components.schemas`):

```yaml
    TenantDeletionRequest:
      type: object
      additionalProperties: false
      required: [reason, confirm_name]
      properties:
        reason: {type: string, minLength: 1, maxLength: 500, description: Recorded in the audit log and with the deletion.}
        confirm_name: {$ref: '#/components/schemas/Name', description: The name of the tenant, as a confirmation.}
    TenantDeletionState:
      type: string
      enum: [scheduled, running, completed]
    TenantDeletion:
      type: object
      additionalProperties: false
      required: [tenant_id, state, step, reason, requested_by, request_id, requested_at, not_before, passes, removed, kek_versions, backup_erasure]
      properties:
        tenant_id: {$ref: '#/components/schemas/ID'}
        state: {$ref: '#/components/schemas/TenantDeletionState'}
        step:
          type: string
          enum: [begin, access, secrets, configuration, usage, audit, complete, done]
          description: The step that runs next; done when the deletion is completed.
        reason: {type: string}
        requested_by:
          type: object
          additionalProperties: false
          required: [type, id, name]
          properties:
            type: {type: string, enum: [platform, user]}
            id: {type: string}
            name: {type: string}
        request_id: {type: string}
        requested_at: {type: string, format: date-time}
        not_before: {type: string, format: date-time, description: The deletion does not start before this instant.}
        started_at: {type: string, format: date-time}
        completed_at: {type: string, format: date-time}
        passes: {type: integer, description: How many times the removal ran; more than 1 when rows written by transactions older than the deletion were found.}
        removed:
          type: object
          additionalProperties: {type: integer}
          description: Rows removed so far, by table. Figures of the tenant's usage are never counted here.
        kek_versions:
          type: array
          items: {type: string}
          description: The key-encryption key versions that wrapped the tenant's data keys when they were destroyed.
        backup_erasure:
          type: object
          additionalProperties: false
          required: [state, pending_kek_versions]
          description: >-
            Whether backups taken before the deletion can still open the tenant's credentials: pending while
            one of kek_versions is not retired. A retired version protects only once its key file is destroyed.
          properties:
            state: {type: string, enum: [not_applicable, pending, complete]}
            pending_kek_versions: {type: array, items: {type: string}}
        audit:
          type: object
          additionalProperties: false
          required: [anchor_seq, last_seq, rows, original_head, resealed_head]
          description: The tenant's audit chain as it was kept. Absent until the step audit ran.
          properties:
            anchor_seq: {type: integer, format: int64}
            last_seq: {type: integer, format: int64}
            rows: {type: integer, format: int64}
            original_head: {type: string, description: The head of the chain before it was pseudonymized, in hexadecimal.}
            resealed_head: {type: string, description: The head of the kept chain, in hexadecimal.}
        completion_seq: {type: integer, format: int64, description: The sequence number of the completion record in the platform's audit chain.}
    TenantDeletionList:
      type: object
      additionalProperties: false
      required: [items]
      properties:
        items:
          type: array
          items: {$ref: '#/components/schemas/TenantDeletion'}
        next_cursor: {type: string, description: Absent on the last page.}
    KEKVersion:
      type: object
      additionalProperties: false
      required: [id, provider, state, created_at, activated_at, erasures_pending]
      properties:
        id: {type: string}
        provider: {type: string}
        state: {type: string, enum: [active, retiring, retired]}
        created_at: {type: string, format: date-time}
        activated_at: {type: string, format: date-time}
        retired_at: {type: string, format: date-time}
        erasures_pending: {type: integer, description: Completed tenant deletions whose backups stay readable until this version is retired and its key destroyed.}
    KEKVersionList:
      type: object
      additionalProperties: false
      required: [items]
      properties:
        items:
          type: array
          items: {$ref: '#/components/schemas/KEKVersion'}
```

If apigen refuses a construct above (a `description` next to a `$ref`, `additionalProperties` with a
schema), write the member the way the nearest existing schema does and keep the names; do not extend
the generator for it.

## Appendix A1 — `internal/store/migrations/sql/0016_tenant_deletion_kek_rotation.sql`

```sql
-- 0016_tenant_deletion_kek_rotation: the states of key-encryption key versions and their
-- activation and retirement, the re-wrap grant of the worker, the guard that closes a tenant to
-- new rows once its deletion has begun, the deletion request and its progress, the erasure steps
-- run by the worker, and retention of the records kept for a deleted tenant (ADR 0067, ADR 0068).
-- Runs in one transaction as pandokos_owner. Every name is schema-qualified.

-- ---------------------------------------------------------------------------------------------
-- 1. Append-only, except for retention (DELETE, migration 0015) and erasure (UPDATE).
-- ---------------------------------------------------------------------------------------------

-- A row of an append-only table is removed only under pandokos.retention and rewritten only
-- under pandokos.erasure. Only functions that run as the owner set them around their own
-- statement: no runtime role holds DELETE or UPDATE on these tables, so the settings give a
-- runtime role nothing. TRUNCATE stays refused for everybody.
CREATE OR REPLACE FUNCTION pandokos.append_only() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'DELETE' AND pg_catalog.current_setting('pandokos.retention', true) = 'on' THEN
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE' AND pg_catalog.current_setting('pandokos.erasure', true) = 'on' THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
END
$$;

-- A started price version of a tenant is removed only with its tenant, under the same setting as
-- the rows of the append-only tables (one mechanism, 5.4 section 11).
CREATE OR REPLACE FUNCTION pandokos.tenant_price_version_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.created_at := now();
        IF NEW.valid_from < now() THEN
            RAISE EXCEPTION 'a price version cannot start in the past' USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'price versions are immutable' USING ERRCODE = 'restrict_violation';
    END IF;
    IF OLD.valid_from <= now() AND pg_catalog.current_setting('pandokos.retention', true) IS DISTINCT FROM 'on' THEN
        RAISE EXCEPTION 'a price version that has started cannot be removed' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN OLD;
END
$$;

-- ---------------------------------------------------------------------------------------------
-- 2. Key-encryption key versions: one is active, a replaced one is retiring until no data key is
--    wrapped by it, then retired (VLT-10). The key itself is never here.
-- ---------------------------------------------------------------------------------------------

ALTER TABLE pandokos.kek_version
    ADD COLUMN state        text        NOT NULL DEFAULT 'retiring',
    ADD COLUMN activated_at timestamptz,
    ADD COLUMN retired_at   timestamptz;

-- The version registered last was the one in use; any other row is a version that is retired as
-- soon as nothing is wrapped by it.
UPDATE pandokos.kek_version v
   SET state = 'active', activated_at = v.created_at
 WHERE v.id = (SELECT w.id FROM pandokos.kek_version w ORDER BY w.created_at DESC, w.id DESC LIMIT 1);
UPDATE pandokos.kek_version SET activated_at = created_at WHERE activated_at IS NULL;

ALTER TABLE pandokos.kek_version
    ALTER COLUMN activated_at SET NOT NULL,
    ALTER COLUMN activated_at SET DEFAULT now(),
    ADD CONSTRAINT kek_version_state_valid CHECK (state IN ('active', 'retiring', 'retired')),
    ADD CONSTRAINT kek_version_retired_at CHECK ((state = 'retired') = (retired_at IS NOT NULL));
CREATE UNIQUE INDEX kek_version_active_key ON pandokos.kek_version ((true)) WHERE state = 'active';

-- Versions are registered by kek_activate only.
REVOKE INSERT (id, provider) ON pandokos.kek_version FROM pandokos_admin;

-- The owner writes the lifecycle actions of this migration in the platform log, inside its
-- functions.
CREATE POLICY platform_audit_event_lifecycle_insert ON pandokos.platform_audit_event
    FOR INSERT TO pandokos_owner
    WITH CHECK (action IN ('kek.activate', 'kek.retire', 'tenant.delete_start', 'tenant.delete_complete',
                           'tenant.records_retention'));

-- In platform scope, at the start of serve: makes p_id the active version. An unknown version is
-- registered and replaces the active one, which becomes retiring; the active version is left as
-- it is; a retiring or retired version is refused (a replaced key never comes back). Returns
-- 'activated' or 'unchanged'.
CREATE FUNCTION pandokos.kek_activate(p_id text, p_provider text) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_state    text;
    v_provider text;
    v_previous text;
BEGIN
    IF NOT pandokos.platform_scope() THEN
        RAISE EXCEPTION 'kek_activate needs the platform scope' USING ERRCODE = 'insufficient_privilege';
    END IF;
    LOCK TABLE pandokos.kek_version IN SHARE ROW EXCLUSIVE MODE;
    SELECT v.state, v.provider INTO v_state, v_provider FROM pandokos.kek_version v WHERE v.id = p_id;
    IF FOUND THEN
        IF v_provider <> p_provider THEN
            RAISE EXCEPTION 'key-encryption key version % belongs to provider %', p_id, v_provider
                USING ERRCODE = 'object_not_in_prerequisite_state', CONSTRAINT = 'kek_version_provider';
        END IF;
        IF v_state <> 'active' THEN
            RAISE EXCEPTION 'key-encryption key version % is % and cannot become active again', p_id, v_state
                USING ERRCODE = 'object_not_in_prerequisite_state', CONSTRAINT = 'kek_version_replaced';
        END IF;
        RETURN 'unchanged';
    END IF;
    UPDATE pandokos.kek_version SET state = 'retiring' WHERE state = 'active' RETURNING id INTO v_previous;
    INSERT INTO pandokos.kek_version (id, provider, state) VALUES (p_id, p_provider, 'active');
    INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, resource_id, outcome, detail)
    VALUES ('host', 'serve', 'kek.activate', 'kek_version', p_id, 'success',
            pg_catalog.jsonb_build_object('provider', p_provider, 'previous', COALESCE(v_previous, '')));
    RETURN 'activated';
END
$$;

-- In platform scope, by the worker: retires every retiring version that wraps no data key. The
-- test is the foreign key of tenant_data_key, which sees the rows of every tenant and waits for
-- an insert in flight: the row is deleted and, when nothing refers to it, written again as
-- retired. Returns the number of versions retired.
CREATE FUNCTION pandokos.kek_retire() RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    r pandokos.kek_version%ROWTYPE;
    n integer := 0;
BEGIN
    IF NOT pandokos.platform_scope() THEN
        RAISE EXCEPTION 'kek_retire needs the platform scope' USING ERRCODE = 'insufficient_privilege';
    END IF;
    FOR r IN SELECT * FROM pandokos.kek_version v WHERE v.state = 'retiring' ORDER BY v.id FOR UPDATE LOOP
        BEGIN
            DELETE FROM pandokos.kek_version v WHERE v.id = r.id;
            INSERT INTO pandokos.kek_version (id, provider, created_at, state, activated_at, retired_at)
            VALUES (r.id, r.provider, r.created_at, 'retired', r.activated_at, pg_catalog.now());
        EXCEPTION WHEN foreign_key_violation THEN
            CONTINUE;
        END;
        INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, resource_id, outcome, detail)
        VALUES ('platform', 'rewrap', 'kek.retire', 'kek_version', r.id, 'success',
                pg_catalog.jsonb_build_object('provider', r.provider));
        n := n + 1;
    END LOOP;
    RETURN n;
END
$$;

REVOKE ALL ON FUNCTION pandokos.kek_activate(text, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.kek_retire() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.kek_activate(text, text) TO pandokos_admin;
GRANT EXECUTE ON FUNCTION pandokos.kek_retire() TO pandokos_worker;

-- A data key is created, and re-wrapped, under the active version only: an instance whose
-- configuration still names a replaced key fails closed.
CREATE FUNCTION pandokos.tenant_data_key_kek_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pandokos.kek_version v WHERE v.id = NEW.kek_version AND v.state = 'active') THEN
        RAISE EXCEPTION 'a data key is wrapped by the active key-encryption key version only'
            USING ERRCODE = 'restrict_violation', CONSTRAINT = 'tenant_data_key_kek_active';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER tenant_data_key_kek_guard BEFORE INSERT OR UPDATE OF kek_version ON pandokos.tenant_data_key
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_data_key_kek_guard();

-- The worker re-wraps data keys in the session of their tenant (ADR 0008).
GRANT SELECT ON pandokos.tenant_data_key TO pandokos_worker;
GRANT UPDATE (kek_version, wrapped_key) ON pandokos.tenant_data_key TO pandokos_worker;

-- ---------------------------------------------------------------------------------------------
-- 3. A tenant that is being deleted, or is deleted, takes no new row. Only the erasure functions
--    of this migration, which run as the owner and set pandokos.erasure, write rows for it.
-- ---------------------------------------------------------------------------------------------

-- The tenants that take no new row (global table): a tenant enters it when its deletion begins
-- and never leaves. It holds identifiers only. A runtime role reads the row of the tenant of its
-- session, or every row in platform scope; only the erasure function writes it.
CREATE TABLE pandokos.tenant_closed (
    tenant_id uuid        NOT NULL,
    closed_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tenant_closed_pkey PRIMARY KEY (tenant_id),
    CONSTRAINT tenant_closed_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id)
);

ALTER TABLE pandokos.tenant_closed ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.tenant_closed FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_closed_owner ON pandokos.tenant_closed
    FOR ALL TO pandokos_owner USING (true) WITH CHECK (true);
CREATE POLICY tenant_closed_read ON pandokos.tenant_closed
    FOR SELECT TO pandokos_gateway, pandokos_admin, pandokos_worker
    USING (tenant_id = (SELECT pandokos.current_tenant_id()) OR (SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.tenant_closed TO pandokos_gateway, pandokos_admin, pandokos_worker;

-- The owner reads and changes the row of a tenant inside the erasure functions only.
CREATE POLICY tenant_erasure_select ON pandokos.tenant
    FOR SELECT TO pandokos_owner
    USING (pg_catalog.current_setting('pandokos.erasure', true) = 'on');
CREATE POLICY tenant_erasure_update ON pandokos.tenant
    FOR UPDATE TO pandokos_owner
    USING (pg_catalog.current_setting('pandokos.erasure', true) = 'on')
    WITH CHECK (pg_catalog.current_setting('pandokos.erasure', true) = 'on');

-- For the tables whose policy binds every row to the tenant of the session: one look at that
-- tenant per statement. The setting lets a row through only for the owner, that is, inside a
-- function of this migration: a runtime role that sets it gains nothing.
CREATE FUNCTION pandokos.tenant_open() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t uuid := pandokos.current_tenant_id();
BEGIN
    IF t IS NULL THEN
        RETURN NULL;
    END IF;
    IF current_user = 'pandokos_owner' AND pg_catalog.current_setting('pandokos.erasure', true) = 'on' THEN
        RETURN NULL;
    END IF;
    IF EXISTS (SELECT 1 FROM pandokos.tenant_closed x WHERE x.tenant_id = t) THEN
        RAISE EXCEPTION 'the tenant is being deleted or is deleted' USING ERRCODE = 'restrict_violation', CONSTRAINT = 'tenant_open';
    END IF;
    RETURN NULL;
END
$$;

-- For the global tables that name a tenant in a row written in platform scope: one look per row.
CREATE FUNCTION pandokos.tenant_open_row() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t uuid := (pg_catalog.to_jsonb(NEW) ->> 'tenant_id')::uuid;
BEGIN
    IF t IS NULL THEN
        RETURN NEW;
    END IF;
    IF current_user = 'pandokos_owner' AND pg_catalog.current_setting('pandokos.erasure', true) = 'on' THEN
        RETURN NEW;
    END IF;
    IF EXISTS (SELECT 1 FROM pandokos.tenant_closed x WHERE x.tenant_id = t) THEN
        RAISE EXCEPTION 'the tenant is being deleted or is deleted' USING ERRCODE = 'restrict_violation', CONSTRAINT = 'tenant_open';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER audit_chain_tenant_open BEFORE INSERT ON pandokos.audit_chain
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER audit_event_tenant_open BEFORE INSERT ON pandokos.audit_event
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER idempotency_key_tenant_open BEFORE INSERT ON pandokos.idempotency_key
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER limit_event_tenant_open BEFORE INSERT ON pandokos.limit_event
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER limit_rule_tenant_open BEFORE INSERT ON pandokos.limit_rule
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER membership_tenant_open BEFORE INSERT ON pandokos.membership
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER membership_role_tenant_open BEFORE INSERT ON pandokos.membership_role
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER metadata_key_policy_tenant_open BEFORE INSERT ON pandokos.metadata_key_policy
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER provider_credential_tenant_open BEFORE INSERT ON pandokos.provider_credential
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER role_assignment_tenant_open BEFORE INSERT ON pandokos.role_assignment
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER service_account_tenant_open BEFORE INSERT ON pandokos.service_account
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER subject_mapping_tenant_open BEFORE INSERT ON pandokos.subject_mapping
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER tenant_data_key_tenant_open BEFORE INSERT ON pandokos.tenant_data_key
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER tenant_model_restriction_tenant_open BEFORE INSERT ON pandokos.tenant_model_restriction
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER tenant_price_rate_tenant_open BEFORE INSERT ON pandokos.tenant_price_rate
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER tenant_price_version_tenant_open BEFORE INSERT ON pandokos.tenant_price_version
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER tenant_role_tenant_open BEFORE INSERT ON pandokos.tenant_role
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER trusted_issuer_tenant_open BEFORE INSERT ON pandokos.trusted_issuer
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER usage_record_tenant_open BEFORE INSERT ON pandokos.usage_record
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER usage_rollup_tenant_open BEFORE INSERT ON pandokos.usage_rollup
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER usage_rollup_state_tenant_open BEFORE INSERT ON pandokos.usage_rollup_state
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER virtual_key_tenant_open BEFORE INSERT ON pandokos.virtual_key
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();

CREATE TRIGGER app_user_tenant_open BEFORE INSERT ON pandokos.app_user
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_open_row();
CREATE TRIGGER model_tenant_grant_tenant_open BEFORE INSERT ON pandokos.model_tenant_grant
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_open_row();
CREATE TRIGGER platform_issuer_tenant_tenant_open BEFORE INSERT ON pandokos.platform_issuer_tenant
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_open_row();
CREATE TRIGGER platform_limit_rule_tenant_open BEFORE INSERT ON pandokos.platform_limit_rule
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_open_row();
CREATE TRIGGER support_action_tenant_open BEFORE INSERT ON pandokos.support_action
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_open_row();

-- ---------------------------------------------------------------------------------------------
-- 4. The deletion of a tenant: the request and its progress (global table). A row is created by
--    the admin role in platform scope when a deletion is requested, removed by it while the
--    deletion is still scheduled (cancel), and from then on written by the erasure functions only.
-- ---------------------------------------------------------------------------------------------

CREATE TABLE pandokos.tenant_deletion (
    tenant_id         uuid        NOT NULL,
    state             text        NOT NULL DEFAULT 'scheduled',
    step              text        NOT NULL DEFAULT 'begin',
    reason            text        NOT NULL,
    requested_by_type text        NOT NULL,
    requested_by_id   text        NOT NULL DEFAULT '',
    requested_by_name text        NOT NULL DEFAULT '',
    request_id        text        NOT NULL DEFAULT '',
    requested_at      timestamptz NOT NULL DEFAULT now(),
    not_before        timestamptz NOT NULL,
    started_at        timestamptz,
    settle_at         timestamptz,
    completed_at      timestamptz,
    passes            integer     NOT NULL DEFAULT 0,
    salt              bytea,
    usage_started_at  timestamptz,
    usage_id          uuid,
    kek_versions      text[]      NOT NULL DEFAULT '{}',
    removed           jsonb       NOT NULL DEFAULT '{}',
    audit             jsonb       NOT NULL DEFAULT '{}',
    completion_seq    bigint,
    CONSTRAINT tenant_deletion_pkey PRIMARY KEY (tenant_id),
    CONSTRAINT tenant_deletion_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT tenant_deletion_state_valid CHECK (state IN ('scheduled', 'running', 'completed')),
    CONSTRAINT tenant_deletion_step_valid CHECK (step IN ('begin', 'access', 'secrets', 'configuration', 'usage', 'audit', 'complete', 'done')),
    CONSTRAINT tenant_deletion_state_step CHECK ((state = 'scheduled') = (step = 'begin') AND (state = 'completed') = (step = 'done')),
    CONSTRAINT tenant_deletion_reason_length CHECK (char_length(reason) BETWEEN 1 AND 500),
    CONSTRAINT tenant_deletion_requested_by_type_valid CHECK (requested_by_type IN ('platform', 'user')),
    CONSTRAINT tenant_deletion_times CHECK ((state = 'scheduled') = (started_at IS NULL) AND (state = 'completed') = (completed_at IS NOT NULL)),
    CONSTRAINT tenant_deletion_salt CHECK ((state = 'running') = (salt IS NOT NULL) AND (salt IS NULL OR octet_length(salt) = 32)),
    CONSTRAINT tenant_deletion_objects CHECK (jsonb_typeof(removed) = 'object' AND jsonb_typeof(audit) = 'object')
);
CREATE INDEX tenant_deletion_open_idx ON pandokos.tenant_deletion (not_before) WHERE state <> 'completed';

ALTER TABLE pandokos.tenant_deletion ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.tenant_deletion FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_deletion_owner ON pandokos.tenant_deletion
    FOR ALL TO pandokos_owner USING (true) WITH CHECK (true);
CREATE POLICY tenant_deletion_platform_select ON pandokos.tenant_deletion
    FOR SELECT TO pandokos_admin, pandokos_worker USING ((SELECT pandokos.platform_scope()));
CREATE POLICY tenant_deletion_platform_insert ON pandokos.tenant_deletion
    FOR INSERT TO pandokos_admin WITH CHECK ((SELECT pandokos.platform_scope()) AND state = 'scheduled');
CREATE POLICY tenant_deletion_platform_delete ON pandokos.tenant_deletion
    FOR DELETE TO pandokos_admin USING ((SELECT pandokos.platform_scope()) AND state = 'scheduled');

-- No runtime role reads the salt of the pseudonyms or the cursor of the ledger.
GRANT SELECT (tenant_id, state, step, reason, requested_by_type, requested_by_id, requested_by_name, request_id,
              requested_at, not_before, started_at, settle_at, completed_at, passes, kek_versions, removed, audit, completion_seq)
    ON pandokos.tenant_deletion TO pandokos_admin;
GRANT SELECT (tenant_id, state, step, request_id, not_before, started_at, kek_versions)
    ON pandokos.tenant_deletion TO pandokos_worker;
GRANT INSERT (tenant_id, reason, requested_by_type, requested_by_id, requested_by_name, request_id, not_before)
    ON pandokos.tenant_deletion TO pandokos_admin;
GRANT DELETE ON pandokos.tenant_deletion TO pandokos_admin;

-- A tenant with a deletion request is not reactivated: the request is cancelled first.
CREATE FUNCTION pandokos.tenant_deletion_holds() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NEW.state = 'active' AND OLD.state = 'suspended'
       AND EXISTS (SELECT 1 FROM pandokos.tenant_deletion d WHERE d.tenant_id = NEW.id) THEN
        RAISE EXCEPTION 'the deletion of the tenant is requested' USING ERRCODE = 'restrict_violation', CONSTRAINT = 'tenant_deletion_holds';
    END IF;
    RETURN NEW;
END
$$;
REVOKE ALL ON FUNCTION pandokos.tenant_deletion_holds() FROM PUBLIC;
CREATE TRIGGER tenant_deletion_holds BEFORE UPDATE OF state ON pandokos.tenant
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_deletion_holds();

-- A deletion is requested for a suspended tenant only (the request suspends it in the same
-- transaction).
CREATE FUNCTION pandokos.tenant_deletion_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pandokos.tenant x WHERE x.id = NEW.tenant_id AND x.state = 'suspended') THEN
        RAISE EXCEPTION 'a deletion is requested for a suspended tenant' USING ERRCODE = 'restrict_violation', CONSTRAINT = 'tenant_deletion_suspended';
    END IF;
    NEW.state := 'scheduled';
    NEW.step := 'begin';
    NEW.requested_at := pg_catalog.now();
    RETURN NEW;
END
$$;
REVOKE ALL ON FUNCTION pandokos.tenant_deletion_guard() FROM PUBLIC;
CREATE TRIGGER tenant_deletion_guard BEFORE INSERT ON pandokos.tenant_deletion
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_deletion_guard();

-- ---------------------------------------------------------------------------------------------
-- 5. Policies that let the erasure functions, and nothing else, remove a tenant's rows of the
--    global tables: the owner, in the session of that tenant, under pandokos.erasure.
-- ---------------------------------------------------------------------------------------------

CREATE POLICY model_tenant_grant_erasure ON pandokos.model_tenant_grant FOR ALL TO pandokos_owner
    USING (tenant_id = (SELECT pandokos.current_tenant_id()) AND pg_catalog.current_setting('pandokos.erasure', true) = 'on');
CREATE POLICY platform_limit_rule_erasure ON pandokos.platform_limit_rule FOR ALL TO pandokos_owner
    USING (tenant_id = (SELECT pandokos.current_tenant_id()) AND pg_catalog.current_setting('pandokos.erasure', true) = 'on');
CREATE POLICY platform_issuer_tenant_erasure ON pandokos.platform_issuer_tenant FOR DELETE TO pandokos_owner
    USING (tenant_id = (SELECT pandokos.current_tenant_id()) AND pg_catalog.current_setting('pandokos.erasure', true) = 'on');
CREATE POLICY support_action_erasure ON pandokos.support_action FOR ALL TO pandokos_owner
    USING (tenant_id = (SELECT pandokos.current_tenant_id()) AND pg_catalog.current_setting('pandokos.erasure', true) = 'on');
CREATE POLICY idempotency_key_erasure ON pandokos.idempotency_key FOR ALL TO pandokos_owner
    USING (tenant_id = (SELECT pandokos.current_tenant_id()) AND pg_catalog.current_setting('pandokos.erasure', true) = 'on');
CREATE POLICY app_user_erasure ON pandokos.app_user FOR DELETE TO pandokos_owner
    USING (tenant_id = (SELECT pandokos.current_tenant_id()) AND pg_catalog.current_setting('pandokos.erasure', true) = 'on');
CREATE POLICY local_account_erasure ON pandokos.local_account FOR DELETE TO pandokos_owner
    USING (pg_catalog.current_setting('pandokos.erasure', true) = 'on'
           AND EXISTS (SELECT 1 FROM pandokos.app_user u WHERE u.id = user_id AND u.tenant_id = (SELECT pandokos.current_tenant_id())));
CREATE POLICY user_identity_erasure ON pandokos.user_identity FOR ALL TO pandokos_owner
    USING (pg_catalog.current_setting('pandokos.erasure', true) = 'on'
           AND EXISTS (SELECT 1 FROM pandokos.app_user u WHERE u.id = user_id AND u.tenant_id = (SELECT pandokos.current_tenant_id())));
CREATE POLICY user_session_erasure ON pandokos.user_session FOR ALL TO pandokos_owner
    USING (pg_catalog.current_setting('pandokos.erasure', true) = 'on'
           AND EXISTS (SELECT 1 FROM pandokos.app_user u WHERE u.id = user_id AND u.tenant_id = (SELECT pandokos.current_tenant_id())));

-- ---------------------------------------------------------------------------------------------
-- 6. The erasure functions. The worker calls tenant_erasure_step in the session of the tenant
--    until it answers 'done'; each call is one transaction and one step, or one batch of a step.
--    The functions below it are the owner's alone.
-- ---------------------------------------------------------------------------------------------

-- A pseudonym of an identifier: the text of a UUID made of the first 16 bytes of
-- SHA-256(salt, value). The salt is destroyed when the deletion completes, so a pseudonym cannot
-- be recomputed from a guessed value. The empty text stays empty.
CREATE FUNCTION pandokos.pseudonym(p_salt bytea, p_value text) RETURNS text
    LANGUAGE sql IMMUTABLE
    SET search_path = pg_catalog, pg_temp
    RETURN CASE WHEN p_value = '' THEN ''
                ELSE pg_catalog.encode(pg_catalog.substring(pg_catalog.sha256(p_salt || pg_catalog.convert_to(p_value, 'UTF8')), 1, 16), 'hex')::uuid::text END;

-- The sum, key by key, of two objects of counts.
CREATE FUNCTION pandokos.jsonb_counts_add(a jsonb, b jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path = pg_catalog, pg_temp
    RETURN (SELECT COALESCE(pg_catalog.jsonb_object_agg(s.key, s.n), '{}'::jsonb)
              FROM (SELECT e.key, pg_catalog.sum(e.value::bigint) AS n
                      FROM (SELECT * FROM pg_catalog.jsonb_each_text(a) UNION ALL SELECT * FROM pg_catalog.jsonb_each_text(b)) e
                     GROUP BY e.key) s);

-- How many rows each table still holds for the tenant of the session, for the tables that a
-- deletion empties. Reads the directories and the global tables as the owner.
CREATE FUNCTION pandokos.tenant_residue() RETURNS jsonb
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t   uuid := pandokos.current_tenant_id();
    out jsonb := '{}';
    n   bigint;
    tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['limit_rule', 'subject_mapping', 'trusted_issuer', 'issuer_directory', 'platform_issuer_tenant',
                               'virtual_key', 'virtual_key_directory', 'role_assignment', 'idempotency_key', 'support_action',
                               'membership_role', 'membership', 'membership_directory', 'app_user', 'service_account',
                               'tenant_role', 'tenant_data_key', 'provider_credential', 'platform_limit_rule',
                               'model_tenant_grant', 'tenant_model_restriction', 'metadata_key_policy', 'tenant_price_rate',
                               'tenant_price_version', 'limit_event', 'usage_rollup', 'usage_rollup_state'] LOOP
        EXECUTE pg_catalog.format('SELECT pg_catalog.count(*) FROM pandokos.%I x WHERE x.tenant_id = $1', tab) INTO n USING t;
        IF n > 0 THEN
            out := out || pg_catalog.jsonb_build_object(tab, n);
        END IF;
    END LOOP;
    RETURN out;
END
$$;

-- Removal of access: every identity, key, mapping, registration, membership, session and role of
-- the tenant, in the order the foreign keys impose. Returns the rows removed by table.
CREATE FUNCTION pandokos.tenant_erasure_access() RETURNS jsonb
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t   uuid := pandokos.current_tenant_id();
    out jsonb := '{}';
    n   bigint;
BEGIN
    DELETE FROM pandokos.limit_rule;                                        GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('limit_rule', n);
    DELETE FROM pandokos.subject_mapping;                                   GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('subject_mapping', n);
    DELETE FROM pandokos.trusted_issuer;                                    GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('trusted_issuer', n);
    DELETE FROM pandokos.issuer_directory d WHERE d.tenant_id = t;
    DELETE FROM pandokos.platform_issuer_tenant p WHERE p.tenant_id = t;    GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('platform_issuer_tenant', n);
    DELETE FROM pandokos.virtual_key;                                       GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('virtual_key', n);
    DELETE FROM pandokos.virtual_key_directory d WHERE d.tenant_id = t;
    DELETE FROM pandokos.role_assignment;                                   GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('role_assignment', n);
    DELETE FROM pandokos.idempotency_key k WHERE k.tenant_id = t;           GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('idempotency_key', n);
    DELETE FROM pandokos.support_action a WHERE a.tenant_id = t;            GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('support_action', n);
    DELETE FROM pandokos.membership_role;                                   GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('membership_role', n);
    DELETE FROM pandokos.membership;                                        GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('membership', n);
    DELETE FROM pandokos.membership_directory d WHERE d.tenant_id = t;
    DELETE FROM pandokos.user_session s USING pandokos.app_user u WHERE u.id = s.user_id AND u.tenant_id = t;
    DELETE FROM pandokos.local_account a USING pandokos.app_user u WHERE u.id = a.user_id AND u.tenant_id = t;
    DELETE FROM pandokos.user_identity i USING pandokos.app_user u WHERE u.id = i.user_id AND u.tenant_id = t;
    DELETE FROM pandokos.app_user u WHERE u.tenant_id = t;                  GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('app_user', n);
    DELETE FROM pandokos.service_account;                                   GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('service_account', n);
    DELETE FROM pandokos.tenant_role;                                       GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('tenant_role', n);
    RETURN out;
END
$$;

-- Cryptographic erasure: the data keys of the tenant, then the sealed secrets they opened.
-- Returns the key-encryption key versions that wrapped a data key, and the rows removed.
CREATE FUNCTION pandokos.tenant_erasure_secrets(OUT kek_versions text[], OUT removed jsonb)
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    n bigint;
BEGIN
    SELECT COALESCE(pg_catalog.array_agg(DISTINCT k.kek_version ORDER BY k.kek_version), '{}') INTO kek_versions
      FROM pandokos.tenant_data_key k;
    DELETE FROM pandokos.tenant_data_key;     GET DIAGNOSTICS n = ROW_COUNT; removed := pg_catalog.jsonb_build_object('tenant_data_key', n);
    DELETE FROM pandokos.provider_credential; GET DIAGNOSTICS n = ROW_COUNT; removed := removed || pg_catalog.jsonb_build_object('provider_credential', n);
END
$$;

-- Removal of configuration and of derived data. The counts of limit events and of rollup rows
-- are not returned: they are figures of the tenant's usage (ADR 0054).
CREATE FUNCTION pandokos.tenant_erasure_configuration() RETURNS jsonb
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t   uuid := pandokos.current_tenant_id();
    out jsonb := '{}';
    n   bigint;
BEGIN
    DELETE FROM pandokos.platform_limit_rule r WHERE r.tenant_id = t;       GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('platform_limit_rule', n);
    DELETE FROM pandokos.model_tenant_grant g WHERE g.tenant_id = t;        GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('model_tenant_grant', n);
    DELETE FROM pandokos.tenant_model_restriction;                          GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('tenant_model_restriction', n);
    DELETE FROM pandokos.metadata_key_policy;                               GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('metadata_key_policy', n);
    PERFORM pg_catalog.set_config('pandokos.retention', 'on', true);
    DELETE FROM pandokos.tenant_price_version;                              GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('tenant_price_version', n);
    DELETE FROM pandokos.limit_event;
    PERFORM pg_catalog.set_config('pandokos.retention', 'off', true);
    DELETE FROM pandokos.usage_rollup;
    DELETE FROM pandokos.usage_rollup_state;
    RETURN out;
END
$$;

-- One batch of the ledger: the principal and the key of each record become pseudonyms and the
-- caller's metadata is emptied. Returns the last record of the batch, or nothing when no record
-- is left after the cursor.
CREATE FUNCTION pandokos.tenant_erasure_usage(p_salt bytea, p_started_at timestamptz, p_id uuid, p_batch integer,
                                             OUT last_started_at timestamptz, OUT last_id uuid)
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    WITH b AS (
        SELECT u.started_at, u.id
          FROM pandokos.usage_record u
         WHERE p_started_at IS NULL OR (u.started_at, u.id) > (p_started_at, p_id)
         ORDER BY u.started_at, u.id
         LIMIT p_batch
    ), w AS (
        UPDATE pandokos.usage_record u
           SET principal_id = pandokos.pseudonym(p_salt, u.principal_id),
               key_id       = pandokos.pseudonym(p_salt, u.key_id),
               metadata     = '{}'
          FROM b
         WHERE u.started_at = b.started_at AND u.id = b.id
        RETURNING u.started_at, u.id
    )
    SELECT w.started_at, w.id INTO last_started_at, last_id FROM w ORDER BY w.started_at DESC, w.id DESC LIMIT 1;
END
$$;

-- The audit log of the tenant is closed with a last row, then every row kept is pseudonymized
-- (the actor, the key and the resource become pseudonyms; the actor's name, the reason and the
-- detail are emptied) and the chain is sealed again from its anchor with the encoding of ADR
-- 0065. Returns the anchor, the last sequence number, the rows, and the head before and after.
CREATE FUNCTION pandokos.tenant_erasure_audit(p_salt bytea) RETURNS jsonb
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t        uuid := pandokos.current_tenant_id();
    r        pandokos.audit_event%ROWTYPE;
    v_anchor bigint;
    v_prev   bytea;
    v_before bytea;
    v_last   bigint;
    v_hash   bytea;
    v_rows   bigint := 0;
    v_actor  text;
    v_key    text;
    v_res    text;
BEGIN
    INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_name, action, resource_type, resource_id, outcome)
    VALUES (t, 'platform', 'deletion', 'tenant.delete', 'tenant', t::text, 'success');
    SELECT c.anchor_seq, c.anchor_hash, c.seq, c.hash INTO v_anchor, v_prev, v_last, v_before
      FROM pandokos.audit_chain c WHERE c.tenant_id = t FOR UPDATE;
    FOR r IN SELECT * FROM pandokos.audit_event e WHERE e.seq >= v_anchor ORDER BY e.seq LOOP
        v_actor := pandokos.pseudonym(p_salt, r.actor_id);
        v_key := pandokos.pseudonym(p_salt, r.key_id);
        v_res := CASE WHEN r.resource_id = t::text THEN r.resource_id ELSE pandokos.pseudonym(p_salt, r.resource_id) END;
        v_hash := pandokos.audit_row_hash('tenant:' || t::text, r.seq, r.id, r.occurred_at, r.actor_type, v_actor, '', v_key,
                                          r.action, r.resource_type, v_res, r.outcome, r.request_id, '',
                                          r.support_action_id, '{}'::jsonb, v_prev);
        UPDATE pandokos.audit_event e
           SET actor_id = v_actor, actor_name = '', key_id = v_key, resource_id = v_res, reason = '', detail = '{}',
               prev_hash = v_prev, hash = v_hash
         WHERE e.seq = r.seq;
        v_prev := v_hash;
        v_rows := v_rows + 1;
    END LOOP;
    UPDATE pandokos.audit_chain SET hash = v_prev WHERE tenant_id = t;
    RETURN pg_catalog.jsonb_build_object('anchor_seq', v_anchor, 'last_seq', v_last, 'rows', v_rows,
                                         'original_head', pg_catalog.encode(v_before, 'hex'),
                                         'resealed_head', pg_catalog.encode(v_prev, 'hex'));
END
$$;

-- The state of a tenant changes in platform scope (migration 0011): the scope is declared for the
-- one statement and put back.
CREATE FUNCTION pandokos.tenant_erasure_state(p_tenant uuid, p_state text) RETURNS void
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    prev text := pg_catalog.current_setting('app.scope', true);
BEGIN
    PERFORM pg_catalog.set_config('app.scope', 'platform', true);
    UPDATE pandokos.tenant SET state = p_state WHERE id = p_tenant;
    PERFORM pg_catalog.set_config('app.scope', COALESCE(prev, ''), true);
END
$$;

REVOKE ALL ON FUNCTION pandokos.tenant_erasure_state(uuid, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.pseudonym(bytea, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.jsonb_counts_add(jsonb, jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.tenant_residue() FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.tenant_erasure_access() FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.tenant_erasure_secrets() FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.tenant_erasure_configuration() FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.tenant_erasure_usage(bytea, timestamptz, uuid, integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.tenant_erasure_audit(bytea) FROM PUBLIC;

-- In the session of a tenant whose deletion is requested: runs the step the deletion is at and
-- answers the step that comes next: 'scheduled' (not due yet: nothing was done), 'access',
-- 'secrets', 'configuration', 'usage' (again while records are left), 'audit', 'complete',
-- 'settling' (the completion waits until two minutes after the start, or after a second pass, for
-- transactions that began before it) and 'done'. Every call is idempotent: a step that ran is not run again.
-- p_batch is the number of ledger records of one call of the step 'usage' (100 to 50000).
CREATE FUNCTION pandokos.tenant_erasure_step(p_batch integer) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t       uuid := pandokos.current_tenant_id();
    d       pandokos.tenant_deletion%ROWTYPE;
    v_name  text;
    v_state text;
    v_out   jsonb;
    v_keks  text[];
    v_at    timestamptz;
    v_id    uuid;
    v_seq   bigint;
BEGIN
    IF t IS NULL THEN
        RAISE EXCEPTION 'tenant_erasure_step needs a tenant session' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF p_batch IS NULL OR p_batch NOT BETWEEN 100 AND 50000 THEN
        RAISE EXCEPTION 'invalid batch' USING ERRCODE = 'check_violation';
    END IF;
    SELECT * INTO d FROM pandokos.tenant_deletion x WHERE x.tenant_id = t FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'no deletion is requested for the tenant' USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;
    IF d.step = 'done' THEN
        RETURN 'done';
    END IF;
    PERFORM pg_catalog.set_config('pandokos.erasure', 'on', true);
    SELECT x.name, x.state INTO v_name, v_state FROM pandokos.tenant x WHERE x.id = t FOR NO KEY UPDATE;
    CASE d.step
    WHEN 'begin' THEN
        IF d.not_before > pg_catalog.now() THEN
            PERFORM pg_catalog.set_config('pandokos.erasure', 'off', true);
            RETURN 'scheduled';
        END IF;
        IF v_state <> 'suspended' THEN
            RAISE EXCEPTION 'the tenant is not suspended' USING ERRCODE = 'object_not_in_prerequisite_state';
        END IF;
        PERFORM pandokos.tenant_erasure_state(t, 'deleting');
        INSERT INTO pandokos.tenant_closed (tenant_id) VALUES (t);
        UPDATE pandokos.tenant_deletion
           SET state = 'running', step = 'access', started_at = pg_catalog.now(), settle_at = pg_catalog.now() + interval '2 minutes',
               salt = pg_catalog.uuid_send(pg_catalog.gen_random_uuid()) || pg_catalog.uuid_send(pg_catalog.gen_random_uuid())
         WHERE tenant_id = t;
        INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, resource_id, outcome, request_id, detail)
        VALUES ('platform', 'deletion', 'tenant.delete_start', 'tenant', t::text, 'success', d.request_id,
                pg_catalog.jsonb_build_object('name', v_name, 'requested_at', d.requested_at, 'not_before', d.not_before));
    WHEN 'access' THEN
        v_out := pandokos.tenant_erasure_access();
        UPDATE pandokos.tenant_deletion SET step = 'secrets', removed = pandokos.jsonb_counts_add(removed, v_out), passes = passes + 1 WHERE tenant_id = t;
    WHEN 'secrets' THEN
        SELECT s.kek_versions, s.removed INTO v_keks, v_out FROM pandokos.tenant_erasure_secrets() s;
        UPDATE pandokos.tenant_deletion
           SET step = 'configuration', removed = pandokos.jsonb_counts_add(removed, v_out),
               kek_versions = ARRAY(SELECT DISTINCT k FROM pg_catalog.unnest(kek_versions || v_keks) k ORDER BY 1)
         WHERE tenant_id = t;
    WHEN 'configuration' THEN
        v_out := pandokos.tenant_erasure_configuration();
        -- A second pass (rows left by a transaction that began before the deletion) does not do
        -- the ledger and the log again: they took no row after the start.
        UPDATE pandokos.tenant_deletion
           SET step = CASE WHEN d.audit = '{}'::jsonb THEN 'usage' ELSE 'complete' END,
               removed = pandokos.jsonb_counts_add(removed, v_out)
         WHERE tenant_id = t;
    WHEN 'usage' THEN
        SELECT u.last_started_at, u.last_id INTO v_at, v_id
          FROM pandokos.tenant_erasure_usage(d.salt, d.usage_started_at, d.usage_id, p_batch) u;
        IF v_id IS NULL THEN
            UPDATE pandokos.tenant_deletion SET step = 'audit' WHERE tenant_id = t;
        ELSE
            UPDATE pandokos.tenant_deletion SET usage_started_at = v_at, usage_id = v_id WHERE tenant_id = t;
        END IF;
    WHEN 'audit' THEN
        v_out := pandokos.tenant_erasure_audit(d.salt);
        UPDATE pandokos.tenant_deletion SET step = 'complete', audit = v_out WHERE tenant_id = t;
    WHEN 'complete' THEN
        IF pg_catalog.now() < d.settle_at THEN
            PERFORM pg_catalog.set_config('pandokos.erasure', 'off', true);
            RETURN 'settling';
        END IF;
        v_out := pandokos.tenant_residue();
        IF v_out <> '{}'::jsonb THEN
            -- A transaction that began before the deletion left rows: the removal runs again.
            UPDATE pandokos.tenant_deletion SET step = 'access', settle_at = pg_catalog.now() + interval '2 minutes' WHERE tenant_id = t;
            PERFORM pg_catalog.set_config('pandokos.erasure', 'off', true);
            RETURN 'access';
        END IF;
        INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, resource_id, outcome, request_id, detail)
        VALUES ('platform', 'deletion', 'tenant.delete_complete', 'tenant', t::text, 'success', d.request_id,
                pg_catalog.jsonb_build_object('name', v_name, 'requested_at', d.requested_at, 'started_at', d.started_at,
                                              'passes', d.passes, 'removed', d.removed, 'kek_versions', pg_catalog.to_jsonb(d.kek_versions),
                                              'usage', 'pseudonymized', 'audit', d.audit, 'residue', 0));
        -- The head of the platform's chain is locked by this transaction: its number is this row's.
        SELECT c.seq INTO v_seq FROM pandokos.platform_audit_chain c WHERE c.id = 1;
        PERFORM pandokos.tenant_erasure_state(t, 'deleted');
        UPDATE pandokos.tenant_deletion
           SET state = 'completed', step = 'done', completed_at = pg_catalog.now(), salt = NULL, completion_seq = v_seq
         WHERE tenant_id = t;
    END CASE;
    PERFORM pg_catalog.set_config('pandokos.erasure', 'off', true);
    RETURN (SELECT x.step FROM pandokos.tenant_deletion x WHERE x.tenant_id = t);
END
$$;

-- In the session of a deleted tenant: removes the records kept for it that are older than the
-- retention of the installation — its rows of the default partition of the ledger (the monthly
-- partitions are dropped whole by platform_retention) and the oldest rows of its log, whose
-- anchor moves; the closed chain takes no new row, so the removal is recorded in the platform
-- log, without a figure of usage.
CREATE FUNCTION pandokos.deleted_tenant_retention(p_usage_months integer, p_audit_months integer,
                                                 OUT usage_rows bigint, OUT audit_rows bigint)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t       uuid := pandokos.current_tenant_id();
    v_month date := pg_catalog.date_trunc('month', pg_catalog.timezone('UTC', pg_catalog.now()))::date;
    v_cut   date;
    v_last  bigint;
    v_first bigint;
    v_hash  bytea;
BEGIN
    IF t IS NULL OR NOT EXISTS (SELECT 1 FROM pandokos.tenant_deletion x WHERE x.tenant_id = t AND x.state = 'completed') THEN
        RAISE EXCEPTION 'deleted_tenant_retention needs the session of a deleted tenant' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF p_usage_months IS NULL OR (p_usage_months <> 0 AND p_usage_months NOT BETWEEN 2 AND 120)
       OR p_audit_months IS NULL OR (p_audit_months <> 0 AND p_audit_months NOT BETWEEN 1 AND 1200) THEN
        RAISE EXCEPTION 'invalid retention' USING ERRCODE = 'check_violation';
    END IF;
    usage_rows := 0;
    audit_rows := 0;
    PERFORM pg_catalog.set_config('pandokos.retention', 'on', true);
    IF p_usage_months > 0 THEN
        v_cut := (v_month - pg_catalog.make_interval(months => p_usage_months))::date;
        DELETE FROM pandokos.usage_record_default u WHERE u.started_at < (v_cut::timestamp AT TIME ZONE 'UTC');
        GET DIAGNOSTICS usage_rows = ROW_COUNT;
    END IF;
    IF p_audit_months > 0 THEN
        v_cut := (v_month - pg_catalog.make_interval(months => p_audit_months))::date;
        SELECT c.anchor_seq INTO v_first FROM pandokos.audit_chain c WHERE c.tenant_id = t FOR UPDATE;
        SELECT pg_catalog.max(e.seq) INTO v_last FROM pandokos.audit_event e
         WHERE e.occurred_at < (v_cut::timestamp AT TIME ZONE 'UTC');
        IF v_last IS NOT NULL AND v_last >= v_first THEN
            SELECT e.hash INTO v_hash FROM pandokos.audit_event e WHERE e.seq = v_last;
            DELETE FROM pandokos.audit_event e WHERE e.seq <= v_last;
            GET DIAGNOSTICS audit_rows = ROW_COUNT;
            UPDATE pandokos.audit_chain SET anchor_seq = v_last + 1, anchor_hash = v_hash WHERE tenant_id = t;
        END IF;
    END IF;
    PERFORM pg_catalog.set_config('pandokos.retention', 'off', true);
    IF audit_rows > 0 THEN
        INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, resource_id, outcome, detail)
        VALUES ('platform', 'retention', 'tenant.records_retention', 'tenant', t::text, 'success',
                pg_catalog.jsonb_build_object('before', pg_catalog.to_char(v_cut, 'YYYY-MM-DD'), 'months', p_audit_months,
                                              'audit_events', audit_rows, 'first_seq', v_first, 'last_seq', v_last,
                                              'last_hash', pg_catalog.encode(v_hash, 'hex')));
    END IF;
END
$$;

REVOKE ALL ON FUNCTION pandokos.tenant_erasure_step(integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.deleted_tenant_retention(integer, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.tenant_erasure_step(integer) TO pandokos_worker;
GRANT EXECUTE ON FUNCTION pandokos.deleted_tenant_retention(integer, integer) TO pandokos_worker;
```
