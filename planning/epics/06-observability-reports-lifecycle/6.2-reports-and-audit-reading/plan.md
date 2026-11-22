# Sprint 6.2 — Implementation plan

- Design: [sprint.md](sprint.md). Section numbers (§) refer to it; T-numbers are its tests, D-numbers
  its decisions, M-numbers its instruments.
- Each part below is self-contained: an agent needs sprint.md and that part only. Parts run in order
  A, B, C1 … C10, D, E, F.
- Suggested grouping: **G1** = C1, C2 (the chain and the audit read path); **G2** = C3, C4, C6 (the
  rollup, the reader, reports, retention); **G3** = C5 (streamed answers and exports); **G4** = C7
  (metadata keys); **G5** = C8, C9 (CLI, verify, export and apply). After part B the groups do not
  depend on each other, except that C8 needs the generated client of C5. C10 is one agent after them.
  A, B, D, E and F are one agent each. Because the risk class is high, A1 (the migration), C3 (the
  reader the limits depend on) and C2's withholding are reviewed by an agent that did not write them,
  with §5.2, §5.6, §5.8 and §5.10 as the checklist.

## Rules for the implementer

1. Work on the branch the orchestrator names; do not create one. Sign off every commit
   (`git commit -s`) in the maintainer's name. No co-author or attribution lines, no tool footers and
   no session links in any commit message, pull request text, comment or file. One commit for A, one
   for B (tests, failing), one per step of C (message given with the step), one for D, one for E, one
   for F if something is found.
2. **No new Go module, no new error code.** One migration (A1), copied **byte for byte**: its SHA-256
   as a file is given with it; a different digest means a wrong copy. A merged migration is immutable
   (ADR 0026); until the pull request merges, a correction changes the file and its line in
   `migrations.sum` together. If a statement of A1 or A2 does not work as written, stop and ask.
3. The only new permissions are `audit:read` and `platform_audit:read`; the only new keys the three of
   §5.16; the only new definer functions the four of A1.
4. **The platform reads no usage of a tenant** (ADR 0054). No platform-scoped handler, and no code in
   `db.Platform` or `inPlatform`, runs a statement of `reports.sql` but `ListTenantsForWorker` and
   `PlatformRetention`, nor a tenant audit read statement (T-R71). Background jobs read usage only in
   the session of the tenant they work for.
5. **One definition each**: the canonical encoding and the hash in `internal/auditchain` (Go) and in
   `audit_row_hash` (SQL) — nowhere else (T-R72); `limits.Sum` for what a limit counts; the exact reader
   of A2 for every aggregate; `admin.PriceDetailActions` for withheld details.
6. **Never convert, never add two currencies or two price sources** — in code, tests, fixtures and
   documentation.
7. **The database is the guard.** Do not weaken a trigger, a policy, a grant or a check to make a test
   pass. Retention deletes only inside the two functions of A1; nothing else sets `pandokos.retention`.
8. Every statement runs as `queries.New(tx).<Method>` inside a `store` transaction helper (T-R10, T-R12).
   Generated files are never edited by hand (`make generate`). Every new route declares its permission
   in the specification and gets its negative tests in the same commit as its handler. Every change
   writes its audit row in its transaction.
9. Logs, metrics and spans hold no tenant data (ADR 0055): only the lines of §5.15 and the instruments
   M42–M46 are added. No body, header value, credential, key secret or real key in a test, fixture or
   document; canaries from `fixtures.Canaries()`.
10. Tests hold for root and non-root; nothing sleeps longer than 2 s; waiting is done by polling with a
    deadline. Database tests use `dbtest` and skip without `PANDOKOS_TEST_DATABASE_URL` as the others do.

## Environment

- Go as in `go.mod` (`go 1.26.0`), the toolchain of ADR 0025; `make generate` runs apigen, permdoc and
  `sqlc` v1.31.1 (pinned in the `Makefile`).
- The test database: `make db-up`, then `set -a; . local/testdb/env; set +a`.
- `make check` before every push of part F.

## Part A — Scaffolding (commit: `chore: sprint 6.2 scaffolding: migration 0015, queries, API specification, streamed answers, permissions, configuration keys`)

### A1. Migration — `internal/store/migrations/sql/0015_reports_audit.sql`

Copy appendix A1 exactly, then add its line to `internal/store/migrations/migrations.sum`
(`make migrations-sum`). Check the copy:

```
sha256sum internal/store/migrations/sql/0015_reports_audit.sql
# 61a4b0ed64a34eea634d52ad752269738fbbe6bfbd13f142ad318de956049d74
```

The file was applied after 0001–0014 on PostgreSQL 16.15 while designing (sprint.md §12).

### A2. Queries

- New file `internal/store/queries/sql/reports.sql`: appendix A2, exactly.
- `internal/store/queries/sql/audit.sql`: replace the first comment line block with the one of
  appendix A2b and append the six statements of A2b after `RecordAuthFailure`.
- `internal/store/queries/sql/limits.sql`: the statement `AggregateUsageForLimits` is replaced by the
  one of appendix A2c; the old statement, with its comment, follows it unchanged but renamed
  `AggregateUsageForLimitsFromLedger` (used by T-947 only, T-R74).
- `make generate`; `go build ./...` must pass (the generated `AggregateUsageForLimitsRow` is unchanged).

### A3. API specification — `api/openapi/admin.v1.yaml`

- Add the tag `- name: audit` (description "The audit logs of a tenant and of the platform, their
  chains and exports") after `usage`.
- Paths: appendix A3a, placed after `/v1/tenants/{tenant}/usage-records` (usage report, usage export,
  limit usage after the limit paths, metadata keys, the three tenant audit paths) and after
  `/v1/platform-limits/{rule}` (the three platform audit paths), in path order.
- Components: appendix A3b (parameters, responses, schemas, each under its section, in the order of
  the file's own grouping).
- `tenant_setting:write`'s description becomes "Setting the tenant's time zone, its active currency
  and the metadata keys it accepts" (in `internal/authz`, A5; the specification refers to it).

**The generator** (`internal/apigen/main.go` and the runtime support it writes into
`internal/adminapi`), two constructs (ADR 0064 and a history line on 0028):

1. **Streamed answers.** A `200` response whose content has `application/x-ndjson` (schema: a
   component object, the line) and optionally `text/csv` (schema: `{type: string}`), nothing else.
   It is the only success response; the operation is a `GET` without body. With two contents the
   operation must have a query parameter `format` whose schema is `ExportFormat`. Generated:
   - server method `Name(ctx, in) (*Stream, error)`; `type Stream struct { ContentType, Filename
     string; Write func(ctx context.Context, w io.Writer) error }`;
   - dispatch `writeStream(w, r, op, out)`: sets `Content-Type` and `Content-Disposition: attachment;
     filename="…"` (the filename is restricted to `[A-Za-z0-9._-]`), status 200, then calls `Write`
     with a writer that buffers until 32 KiB or the first `Flush`, flushes through
     `http.ResponseController` and extends the write deadline by 60 s before each flush. If `Write`
     returns an error before any byte was sent, the answer is the problem of that error; after, the
     handler panics with `http.ErrAbortHandler` (the admin handler's recover lets it through; the
     server closes the connection without the chunk terminator). A `Write` that runs past 15 minutes
     has its context cancelled;
   - client method `Name(ctx, in) (*StreamBody, error)`; `type StreamBody struct { ContentType
     string; Body io.ReadCloser }`; a non-2xx answer is decoded as a problem and returned as the error.
2. **Raw JSON objects.** A component schema `{type: object, x-pandokos-raw-json: true}` is a member of
   Go type `json.RawMessage`, written and read without decoding. It is allowed in responses only.

`make generate`; the server does not compile until the twelve methods exist: add them in A7 as stubs
that return an internal error (answered 500), so that part B compiles.

### A4. Isolation harness — `test/isolation/spec.go`

- `Global`: `"platform_audit_chain": {Reason: "the head and anchor of the platform's audit chain; read
  by the admin role in platform scope, written by the seal and retention functions only", Restricted: true}`.
- `Grants`:
  - `audit_event` admin: add `"SELECT"`; `platform_audit_event` admin: add `"SELECT"`;
  - `audit_chain`: `pandokos_admin: {"SELECT"}`; `platform_audit_chain`: `pandokos_admin: {"SELECT"}`;
  - `usage_rollup`: `pandokos_admin: {"SELECT"}`, `pandokos_worker: {"DELETE", "INSERT", "SELECT", "UPDATE"}`;
  - `usage_rollup_state`: `pandokos_admin: {"SELECT"}`, `pandokos_worker: {"INSERT(tenant_id,watermark)", "SELECT", "UPDATE(updated_at,watermark)"}`;
  - `metadata_key_policy`: `pandokos_gateway: {"SELECT"}`, `pandokos_admin: {"DELETE", "INSERT(keys,tenant_id)", "SELECT", "UPDATE(keys)"}`.
- `Fixtures`:
  - `audit_chain`: `INSERT INTO pandokos.audit_chain (tenant_id) VALUES ($1::uuid) RETURNING tenant_id::text`, `Key: "tenant_id"`, `Update: "anchor_seq = 1"`;
  - `usage_rollup`: one row with every column (`hour` = `date_bin('1 hour', now(), '2000-01-01 00:00:00+00')`, `requests` 1, the rest 0 or `''`, `credential` true), `RETURNING tenant_id::text`, `Key: "tenant_id"`, `Update: "requests = 2"`;
  - `usage_rollup_state`: `INSERT … (tenant_id, watermark) VALUES ($1::uuid, now()) RETURNING tenant_id::text`, `Key: "tenant_id"`, `Update: "watermark = now()"`;
  - `metadata_key_policy`: `INSERT … (tenant_id, keys) VALUES ($1::uuid, ARRAY['team']) RETURNING id::text`, `Update: "keys = ARRAY['project']"`.
- `SecurityDefiner`: add `"audit_seal_tenant", "audit_seal_platform", "platform_retention", "tenant_retention"`.
- `AppendOnly` unchanged.

`go test ./test/isolation -run 'TestSchemaLint$'` passes after A1 and A4.

### A5. Permissions — `internal/authz/{authz,roles}.go`, `testdata/roles.golden`

- Constants `AuditRead = "audit:read"` and `PlatformAuditRead = "platform_audit:read"`; catalogue rows
  `{AuditRead, ScopeTenant, "Reading the tenant's audit log, the head of its chain and its export", false, false}`
  and `{PlatformAuditRead, ScopePlatform, "Reading the platform's audit log, the head of its chain and its export", false, false}`.
- `readOnly` excludes the names of a new list `notReadOnly = []string{AuditRead}`; `auditor` gains
  `AuditRead`; `platform-auditor` gains `PlatformAuditRead` (`platform-admin` holds it by construction);
  the descriptions of `auditor` and `platform-auditor` mention the log.
- `make generate` (permdoc); update the golden file with the test's `-update` flag and read the diff:
  exactly the two permissions on the five roles.

### A6. Configuration — `internal/config/{config,keys}.go`

`serveDef` rows of §5.16; types `Reports.RollupInterval` (`Duration`), `Retention.UsageMonths`,
`Retention.AuditMonths` (`int`); validation and the warning below 12 months (the existing
`configuration warning` line with `key`).

### A7. Types and stubs (no behaviour)

- `internal/auditchain`: `type Row struct { Scope string; Seq int64; ID string; OccurredAt time.Time;
  ActorType, ActorID, ActorName, KeyID, Action, ResourceType, ResourceID, Outcome, RequestID, Reason,
  SupportActionID string; Detail json.RawMessage; DetailDigest []byte; PrevHash, Hash []byte }`;
  `func Canonical(detail json.RawMessage) ([]byte, error)`, `func DetailDigest(detail
  json.RawMessage) ([32]byte, error)`, `func Hash(r Row) ([32]byte, error)` (uses `r.DetailDigest` when
  set, else the digest of `r.Detail`), `type Head struct { Seq int64; Hash []byte; AnchorSeq int64;
  AnchorHash []byte }`, `func Verify(rows iter.Seq2[Row, error], head *Head, expect []byte) Result`,
  `type Break struct { Seq int64; Reason string }`, `type Result struct { Rows int64; First, Last int64;
  LastHash []byte; Breaks []Break; LinkUnverified bool }`. The reasons are constants of §5.9.
- `internal/reports`: `Rollup{DB *store.DB; Interval time.Duration; Logger *slog.Logger; Now
  func() time.Time}` with `Run(ctx)`, `Pass(ctx) error`, `Stats() RollupStats{OK, Failed int64; Lag
  time.Duration}`; `Retention{DB; UsageMonths, AuditMonths int; Logger}` with `Run`, `Pass`, `Stats()
  RetentionStats{Runs, Failed int64; Removed map[string]int64}`.
- `internal/limits`: `func (c TenantCalendar) Bounds(k PeriodKind, from, to time.Time) []time.Time`
  (from, every period start strictly inside, to) and `func (c TenantCalendar) CivilStart(y int, m
  time.Month, d int) time.Time` (midnight of that date in the calendar in force then); `PeriodTotal`
  for a single period.
- `internal/catalog.TenantAccess.MetadataKeys map[string]bool` (nil: no policy).
- `internal/declare`: `type Document struct{…}` with the sections of §5.12 (YAML tags, `KnownFields`
  decoding), `func Export(ctx, c *adminapi.Client, target Target) (*Document, error)`, `func Plan(ctx,
  c, target, doc) (Plan, error)`, `func (p Plan) Apply(ctx, c) (Result, error)`.
- `internal/admin`: the twelve methods as stubs; `var PriceDetailActions = map[string]bool{
  "tenant_price.create": true, "tenant_price.delete": true, "tenant.active_currency_update": true}`.
- `internal/telemetry`: `Sources.Reports func() ReportsStats`, the attribute keys `pandokos.retention.kind`
  and `pandokos.export.kind` with their closed values.

## Part B — Tests first (commit: `test: sprint 6.2 tests, failing`)

### B1. Helpers and fixtures

- `test/dbtest`: `SealedRow(t, tenant, occurredAt, action)` and `SealedPlatformRow(t, occurredAt,
  action)` (owner: disable `append_only` on the table, insert with the seal disabled, set `seq`,
  `prev_hash`, `hash` with `audit_row_hash` from the head, update the head, enable both triggers);
  `Tamper(t, sql string, args…)` (owner, `append_only` disabled for one statement);
  `OldPartition(t, month)` (owner: create and protect a partition of a past month).
- `test/e2e`: `startBinary` options `rollupInterval`, `retentionUsageMonths`, `retentionAuditMonths`;
  `insertUsage(t, tenant, rows)` as the gateway role through `queries.InsertUsageRecords`.
- `internal/declare/testdata/{tenant,platform}.golden.yaml` and a fake `adminapi` server (an
  `httptest` server over a map of state, counting write calls).
- `internal/auditchain/testdata/canonical.golden` (hex lines; written by the test with `-update` from
  the SQL function, reviewed by hand once).

### B2. Where each test lives

| Tests | File |
|---|---|
| T-933, T-934, T-935, T-942, T-952, T-960, T-961, T-962, T-975 | `test/isolation/{audit_chain_test.go,reports_test.go}` |
| T-936, T-939 | `internal/auditchain/{chain_test.go,verify_test.go}` (T-936's SQL half: `chain_db_test.go`) |
| T-937 | `internal/store/migrations/upgrade_0015_db_test.go` |
| T-940, T-941, T-944, T-945, T-946, T-954, T-956, T-957, T-965 | `internal/admin/{audit_read,reports,exports,metadata_keys}_db_test.go` |
| T-943 | `internal/authz/audit_test.go` |
| T-947, T-948, T-949, T-950, T-953, T-963 | `internal/reports/*_test.go` |
| T-951 | `internal/limits/reader_db_test.go` |
| T-958 | `internal/apigen/apigen_test.go` |
| T-964 | `internal/gateway/metadata_policy_test.go` |
| T-966 to T-969 | `internal/declare/*_test.go` |
| T-970 | `internal/cli/reports_test.go` |
| T-972 | `internal/config/reports_test.go` |
| T-973 | `internal/telemetry/instruments_test.go` (extended) |
| T-938, T-955, T-959, T-971, T-974, T-976 | `test/e2e/{audit,reports,declare}_test.go` |
| T-R71 to T-R76 | `test/repo/reports_test.go` |

Change the tests listed under "Changed tests" of §9 in this commit. Every new test fails for the
reason it names (a stub, a missing route), not for a compile error.

## Part C — Implementation

### C1. The chain in Go — `feat: the canonical encoding and the hash of audit rows, and their verifier`

`internal/auditchain`: `Canonical` decodes with `json.Decoder.UseNumber()` and writes the tags of §5.6
(objects: keys sorted with `sort.Strings`, which orders UTF-8 bytes; numbers: `json.Number`'s text);
duplicate keys are an error. `Hash` writes `F("pandokos-audit-v1")`, `PrevHash`, the fields in the order
of §5.6 (`OccurredAt.UTC().Format("2006-01-02T15:04:05.000000Z")`, `strconv.FormatInt(Seq, 10)`), then the
digest. `Verify` implements §5.9. Nothing here reads a database. T-936 (Go half), T-939.

Verify: `go test ./internal/auditchain/...`, then with the database T-933 to T-937 (the SQL is A1's).

### C2. Reading the audit logs — `feat: the audit read API: lists with filters, heads and the withheld price details`

`internal/admin/audit_read.go`: `ListAuditEvents` (tenant session; cursor `{seq, filters}` with
`encodeFullCursor`), `GetAuditHead`, the platform pair (`db.Platform`). Projection `auditEvent(row,
canReadPrices bool)`: hex of the hashes, `detail` as `json.RawMessage(row.Detail)`; when
`PriceDetailActions[row.Action] && !canReadPrices`: `detail` `{}`, `detail_withheld` true,
`detail_digest` hex of `auditchain.DetailDigest`. `canReadPrices` is `authz.Decide(p, PriceListRead,
target) == Allow`. Validation of the filters as listed in A3; `from`/`to` parsed as RFC 3339.
T-940 to T-944, T-946.

### C3. The rollup and the reader — `feat: the usage rollup, maintained by the worker in each tenant's session`

- `internal/reports/rollup.go`: `Pass` lists tenants (`db.Platform`, `ListTenantsForWorker`), then per
  tenant `db.WithTenant`: `LockUsageRollup` then `RollUpUsage` in one transaction; repeat while
  `now − watermark > 2 min` and the tenant's budget (30 s) lasts. Errors are counted per tenant and
  the pass goes on; the lines and rate limits of §5.15. `Lag` is the largest `now − watermark` seen.
- `cmd/pandokos/serve.go`: with the worker role, start `Rollup.Run` after the partitioner; stop it in
  the shutdown sequence before the database pools close.
- `internal/limits` is not changed beyond A7: the reader is the SQL of A2c.
T-947 to T-951, T-953.

### C4. Reports and limit usage — `feat: usage reports by period, provider, model, caller and key; usage against a limit rule`

- `internal/admin/reports.go`: parse `from`/`to` (a date: `CivilStart`; an instant: RFC 3339; defaults
  of §5.3); load the tenant's calendar (`limits` helpers over `GetTenantTimeZone`, as the time-zone
  handlers do); `Bounds(period, from, to)`; `UsageReport` with `MaxRows: 10000`; more rows than that is
  the 400 of §5.3; map `period` index i to `[bounds[i-1], bounds[i])`; `cost` null when `CostKnown` is 0.
- `internal/admin/limit_usage.go`: load the rule (`GetLimitRule`), refuse a per-minute metric (409),
  compute the period with the calendar, call `s.limits.Ledger.Usage(ctx, tid, start, now, now)` and
  `limits.Sum` with the rule as a `limits.Limit`; answer per §5.4.
T-954 to T-956.

### C5. Streamed answers and exports — `feat: streamed answers in the generator; usage and audit exports as NDJSON and CSV`

- The generator constructs of A3, with T-958.
- `internal/admin/exports.go`: `ExportUsageRecords` validates like `ListUsageRecords` (the same helpers,
  ascending order) and returns a `Stream` whose `Write` loops pages of 1000 of `ExportUsageRecords`, each
  in its own `WithTenant`, encoding each item exactly as `listUsageRecords` does (`usageRecordOut`), as a
  JSON line or a CSV line (`encoding/csv`, the column order of the `UsageRecord` schema, `csvSafe` for
  text cells). `ExportAuditEvents`/`ExportPlatformAuditEvents`: read the head first (`to_seq` default),
  then pages of 1000 of `ExportAuditEvents` with the projection of C2. Counters for M46; the line
  `export aborted` on a failure after the first byte.
T-945, T-957, T-959 (exports part).

### C6. Retention — `feat: retention of usage and audit through two audited functions`

`internal/reports/retention.go`: `Run` runs a `Pass` at once and then every hour; with both months 0
it returns at once (and `retention ready` says so at start). `serve` starts it after the first
`ensurePartitions`. Per tenant
(`WithTenant`) `TenantRetention`; then `db.Platform` `PlatformRetention`; sums into `Removed`; `retention
applied` when a total is not zero; `retention failing` once an hour with `failures`. `serve` starts it
with the worker role. T-960 to T-963, T-976.

### C7. Metadata keys — `feat: a tenant restricts the caller metadata keys it accepts`

- `internal/admin/metadata_keys.go`: the three operations (tenant session), validation (≤ 64, the key
  pattern, no duplicates: 400 with the field), audit rows `tenant.metadata_keys_set` (detail `keys`) and
  `tenant.metadata_keys_clear`.
- `internal/catalog/load.go`: `GetMetadataKeyPolicy` in `LoadTenantAccess`; `ErrNoRows` is no policy.
- `internal/usage/metadata.go`: `func Restrict(kept map[string]string, dropped int, allowed map[string]bool)
  (map[string]string, int)` (nil `allowed`: unchanged; saturating count).
- `internal/gateway/handler.go`: at step 7, after the tenant view is read, `st.metadata,
  st.metadataDropped = usage.Restrict(...)` and `metadataHeader` again; a request that ends before step 7
  passes `allowed = map[string]bool{}` when the record is built (records no metadata).
T-964, T-965, T-975.

### C8. Command line — `feat: CLI for reports, exports, limit usage, metadata keys and the audit logs, with audit verify`

`internal/cli/reports.go` and `audit.go`: the rows of §5.14 in `commands`; `composites` (new table
dispatched before `commands`) for `audit verify`, `platform-audit verify`; `cmd/pandokos/main.go` routes
`audit`, `platform-audit`, `metadata-keys`, `export`, `apply` to `runClient`; the usage text of
`cmd/pandokos/usage.go` gains the lines. `audit verify` reads `getAuditHead` then
`exportAuditEvents`, decodes lines into `auditchain.Row` (hex decoding, `json.RawMessage` detail,
`detail_digest` when withheld) and prints §5.9's output. `usage export --out` writes `FILE.partial` and
renames it after the last byte. T-970.

### C9. Export and apply — `feat: pandokos export and pandokos apply for declarative configuration`

`internal/declare`: `Export` lists through the client (all pages) and builds the document of §5.12
(sorted; key-scoped rules keep the key ID; a rule on a subject mapping goes to `LeftOut`, written as
comment lines by the CLI); `Plan` diffs by natural key with the rules of §5.12 (mutable fields: the
members of each resource's update body; anything else is a conflict); `Apply` runs the plan in the
order of §5.12 and stops at the first failed call. The YAML decoder uses `KnownFields(true)`, refuses
anchors and aliases and documents over 4 MiB. `internal/cli/declare.go`: the two composites, output and
exit codes of §5.12. T-966 to T-969, T-971.

### C10. Metrics, logs, wiring and end to end — `feat: report and retention metrics and log lines; end-to-end tests`

- `internal/telemetry`: M42–M46 (§5.15) in the catalogue and `sources.go`; `cmd/pandokos/metrics.go`
  adapters from `reports.Rollup.Stats`, `reports.Retention.Stats` and the admin export counters.
- `serve`: `retention ready` at start with the worker role.
- The end-to-end tests of B2 pass; T-930 (log reference) passes with the new lines.
T-938, T-955, T-959, T-971, T-973, T-974, T-976.

## Part D — Repository tests and CI (commit: `ci: sprint 6.2 repository tests`)

- `test/repo/reports_test.go`: T-R71 to T-R76.
- No new CI job: the new tests run in the existing jobs (`test`, `e2e`); `make check` covers them.
- The container image does not change (no new file outside the binary); `make image` and the quick-start
  Compose test still pass (`make test-compose`).
- Verify: `make check`; `make test-compose`.

## Part E — Documentation and decision records (commit: `docs: sprint 6.2 reports, audit and declarative guides, ADR 0064 to 0066`)

### E1. Guides and references

| File | Change |
|---|---|
| `docs/operations/usage.md` | Section 7 (partitions): retention replaces "never dropped"; new sections 9 *Reports* (the operation, periods in the tenant's calendar, currencies and price sources apart, `rejected_limit` apart, the CLI), 10 *Export* (formats, columns, the CSV guard, aborted transfers, the 31-day split), 11 *Rollup* (what it holds, the watermark, the one-minute delay, what happens when it stops: slower, never wrong), 12 *Metadata keys* |
| `docs/operations/audit.md` (new) | Who reads which log; filters; the chain (what is hashed, §5.6 in prose, the version), the head and the anchor; `audit verify` with its output and reasons; anchoring the head elsewhere (`--expect-head`, a daily `audit head` kept off the installation); withheld details; retention of the logs; what the chain proves and what it does not |
| `docs/operations/declarative.md` (new) | The document schema (every section and member of §5.12 with its natural key), what is never exported, `export` and `apply` with `--dry-run`, the rules (create, update, conflict, left alone, versions only added, settings that take effect later), output and exit codes, an example of each kind |
| `docs/operations/database.md` | Grants of migration 0015; the two retention functions; the index procedure for a large ledger: for each partition `CREATE INDEX CONCURRENTLY usage_record_yYYYYmMM_tenant_id_recorded_at_idx ON pandokos.usage_record_yYYYYmMM (tenant_id, recorded_at)` as the owner before `pandokos migrate` (the migration then attaches them); manual inserts into the ledger are not supported |
| `docs/operations/configuration.md` | The three keys |
| `docs/reference/logs.md` | The six rows of §5.15 and the fields `partitions`, `usage_records`, `audit_events`, `platform_audit_events`, `usage_months`, `audit_months` in the field table |
| `docs/reference/metrics.md` | M42–M46 with their Prometheus names (rule R) |
| `docs/reference/admin-api.md` | Streamed answers and raw JSON members (the two generator constructs) |
| `docs/reference/data-flows.md` | Rows: usage and audit exports (to the caller, over the admin listener), the declarative document (no secret) |
| `docs/architecture/data-model.md` | The rollup and its state; the chain columns and heads; retention as built; `metadata_key_policy`; the sentence "Aggregated reports over the ledger come with sprint 6.2" replaced |
| `docs/architecture/security-model.md` | The row "The audit log is altered": the sealed chain, client-side verification, anchoring, the residual (a whole-chain rewrite); retention under the switch of D-17 |
| `docs/operations/roles.md` | `audit:read` on the three roles, not `read-only`, not under support; `platform_audit:read` |
| `docs/releases/v0.2.0-draft.md` | Under Epic 6, a 6.2 entry (E1b); under "Breaking changes", the sealing of existing rows and the index of migration 0015 with the procedure; under "Known limits", installation-wide retention, `apply` without prune, the 31-day export window |
| `planning/epics/06-observability-reports-lifecycle/epic.md` | 6.2's note on refused grants: "stay log lines (D-23); 6.4 decides whether they become events"; the deferrals of sprint.md §2 noted under 6.3 and 12.3 |

#### E1b. Release notes entry (text to add under Epic 6)

```markdown
- **Reports and audit reading** (6.2): a usage rollup kept by the worker in each tenant's session; a
  report by the tenant's day or month, provider, model, caller and key, with costs per currency and
  price source, never converted (`usage:read`); how much of a daily or monthly limit rule is used;
  usage export as NDJSON and CSV. Every audit row is sealed in a hash chain per tenant and for the
  platform; the logs are read and exported with filters (`audit:read` for `owner`, `tenant-admin` and
  `auditor`; `platform_audit:read` for both platform roles) and `pandokos audit verify` checks a chain.
  Retention of usage and audit (`retention.usage_months`, `retention.audit_months`, off by default).
  A tenant can restrict the caller metadata keys it accepts. `pandokos export` and `pandokos apply`
  for declarative configuration of the catalogue and of a tenant.
- **Migration 0015** seals every existing audit row once and builds an index over the usage ledger; on
  a large ledger build the index beforehand (database guide, the section on migration 0015).
- **Three new keys**, all optional: `reports.rollup_interval` (`1m`), `retention.usage_months` (`0`),
  `retention.audit_months` (`0`).
```

### E2. Decision records

Write the three ADRs below (status `proposed`, date of the commit), add them to `docs/adr/README.md`,
and add the history lines of E2d.

#### E2a. `docs/adr/0064-usage-rollup-reports-and-exports.md`

```markdown
# 0064. Usage rollup, reports and exports

- Status: proposed
- Date: 2026-11-22

Refines [ADR 0013](0013-ledger-and-audit.md), [ADR 0051](0051-period-counters-and-the-ledger.md),
[ADR 0054](0054-usage-data-is-the-tenants.md) and [ADR 0028](0028-admin-api-generator.md).
Requirements USE-06, USE-07, USE-08, PRV-03.

## Context

Reports, the limit counters and the capability check aggregate the usage ledger. Read from raw rows
they grow with traffic; ADR 0013 says reports read rollups maintained by the worker. Counters must keep
the figures of `limits.Sum`, and costs stay per currency and price source (ADR 0053).

## Decision

1. A rollup per tenant by UTC hour of the start and by caller, key, provider, model, model ID,
   currency, price source, outcome and whether a credential was opened, with token and cost sums as
   recorded. The worker adds to it in the tenant's session by time of recording, behind a watermark,
   one minute behind the clock, at most seven days per transaction.
2. One exact reader: whole hours that no period boundary cuts come from the rollup plus the records of
   those hours recorded since the watermark; everything else from the ledger; in one statement. The
   counters' aggregate keeps its contract and becomes this reader.
3. Reports for the tenant only (`usage:read`): by the tenant's day, month or window, by provider, model,
   caller and key, always split by currency and price source, `rejected_limit` apart; at most 400 days
   and 10 000 rows. Usage against a daily or monthly rule from the same reader.
4. Exports as NDJSON (the record's API schema) and CSV, streamed in pages, at most 31 days, a CSV
   formula guard, a transfer aborted on failure. The generator gains streamed answers
   (`application/x-ndjson`, `text/csv`) and raw JSON members.
5. A tenant can restrict the metadata keys it accepts; the gateway applies the list with the tenant's
   catalogue view.

## Consequences

- A stopped rollup makes reads slower, never wrong.
- The ledger gains an index by time of recording; building it on a large ledger is an operator step.
- Platform roles read no report and no export, as ADR 0054 decides.

## Alternatives considered

**Rollup by the tenant's day.** Breaks on a change of time zone and serves the counters badly; the
hour, with boundaries read from the ledger, works for every zone whose offsets are whole minutes.

**A rollup by start time with a fixed lateness.** Misses records written after an outage.

**Materialized views.** Not maintainable per tenant under forced row-level security; forbidden by
the schema lint (L-02).

## History

- 2026-11-22: proposed with sprint 6.2.
```

#### E2b. `docs/adr/0065-the-audit-hash-chain-reading-and-retention.md`

```markdown
# 0065. The audit hash chain, reading and retention

- Status: proposed
- Date: 2026-11-22

Refines [ADR 0013](0013-ledger-and-audit.md) and [ADR 0032](0032-audit-log-first-version.md); amends
ADR 0013 on how audit rows are retained. Requirements AUD-04, AUD-05, AUD-06, AZ-10.

## Context

ADR 0013 asks for a hash chain per tenant and for the platform, a verification command, reading and
retention through an audited function. Rows are written by the admin and worker roles, by three
functions of the gateway and by migrations; the tables are append-only for every role.

## Decision

1. A `BEFORE INSERT` trigger that runs as the owner seals every row: ID, time, a sequence number per
   chain, the previous hash and the row's hash, under a lock of the chain's head row. A row that is
   not inserted leaves the head as it was. The existing rows were sealed once by migration 0015.
2. Canonical encoding version 1: length-prefixed UTF-8 fields in a fixed order and the SHA-256 of a
   tagged, key-sorted encoding of the detail, defined in SQL and in Go and tested against each other.
3. Verification is client-side, over an export: `pandokos audit verify`, with the head, the anchor
   and an expected head kept elsewhere.
4. Reading: `audit:read` (tenant; `owner`, `tenant-admin`, `auditor`; not `read-only`, not `Support`)
   and `platform_audit:read` (both platform roles). The detail of a price action is withheld from a
   reader without `price_list:read`, with its digest, so the chain still verifies.
5. Retention removes the oldest rows of a chain as a prefix and moves its anchor, recording the removal
   in the chain, instead of dropping partitions: the audit tables are not partitioned, partitioning
   would lose the unique keys of `limit.unlimited_traffic`, and a chain needs an anchor in any case. The
   ledger keeps partition dropping. Both go through two functions run by the worker; `append_only`
   admits a delete only under a setting those functions alone set.

## Consequences

- Audit writes of one tenant, and every write of the platform log, are serialized by the head lock.
- A rewrite of a whole chain by whoever can write the tables is detected only against an anchored head.
- Tenant deletion (sprint 6.3) reuses the switch of point 5 and decides how a pseudonymized chain is
  re-sealed.

## Alternatives considered

**Hashing in the application.** Misses the rows written by the gateway's functions and by migrations.

**RFC 8785 canonical JSON.** Not available in PostgreSQL; its number rules differ from `jsonb`'s.

**Verification in the database.** The tool that checks would run where the tampering happens.

**`audit:read` with `Support`.** The log holds price rates and budget amounts (ADR 0061, point 3).

## History

- 2026-11-22: proposed with sprint 6.2.
```

#### E2c. `docs/adr/0066-declarative-export-and-apply.md`

```markdown
# 0066. Declarative export and apply

- Status: proposed
- Date: 2026-11-22

Refines [ADR 0019](0019-configuration.md). Requirement CAT-10.

## Context

ADR 0019 promises `pandokos export` and `pandokos apply`, idempotent, through the API, leaving
resources absent from the document alone and reporting them.

## Decision

1. A YAML document, `pandokos: v1`, of kind `platform` (providers with endpoints, models, model grants
   by tenant name, price overrides, platform ceilings) or `tenant` (time zone, active currency,
   metadata keys, model restriction, custom roles, service accounts with roles, limit rules, price
   lists). Resources are named by natural keys; no secret, key, credential, person or usage figure.
2. `export` writes it deterministically; `apply` plans by natural key, creates and updates through the
   API, reports conflicts on immutable fields and what it leaves alone, never deletes, and only adds
   price versions, never in the past. The plan of a document just exported is empty.
3. Platform-level and tenant token issuers and subject mappings join in sprint 12.3.

## Consequences

- Configuration can be reviewed and repeated without the database being a second source of truth.
- Removing a resource stays an explicit API call.

## Alternatives considered

**Apply in the server, as one transaction.** A second write path beside the API, with its own
authorization.

**Deleting what the document omits.** One wrong document would remove a tenant's configuration.

## History

- 2026-11-22: proposed with sprint 6.2.
```

#### E2d. History lines

| ADR | Line |
|---|---|
| 0013 | 2026-11-22: sprint 6.2 builds the rollup and reports ([ADR 0064](0064-usage-rollup-reports-and-exports.md)), the hash chain, reading and retention; audit retention removes a chain's prefix instead of partitions ([ADR 0065](0065-the-audit-hash-chain-reading-and-retention.md)) |
| 0019 | 2026-11-22: sprint 6.2: `pandokos export` and `pandokos apply` ([ADR 0066](0066-declarative-export-and-apply.md)); keys `reports.rollup_interval`, `retention.usage_months`, `retention.audit_months` |
| 0028 | 2026-11-22: sprint 6.2: streamed answers and raw JSON members ([ADR 0064](0064-usage-rollup-reports-and-exports.md)) |
| 0032 | 2026-11-22: sprint 6.2: the chain, the read grants and four actions (`usage.retention`, `audit.retention`, `tenant.metadata_keys_set`, `tenant.metadata_keys_clear`) ([ADR 0065](0065-the-audit-hash-chain-reading-and-retention.md)) |
| 0039 | 2026-11-22: sprint 6.2: an index of the ledger by `recorded_at`; partitions dropped by retention ([ADR 0064](0064-usage-rollup-reports-and-exports.md)) |
| 0043 | 2026-11-22: sprint 6.2: the capability check reads through the exact reader ([ADR 0064](0064-usage-rollup-reports-and-exports.md)) |
| 0051 | 2026-11-22: sprint 6.2: the counters' aggregate keeps its contract and reads the rollup ([ADR 0064](0064-usage-rollup-reports-and-exports.md)) |
| 0054 | 2026-11-22: sprint 6.2: reports, limit usage and exports are tenant operations; the retention functions return counts ([ADR 0064](0064-usage-rollup-reports-and-exports.md)) |
| 0061 | 2026-11-22: sprint 6.2: `audit:read` does not have the attribute `Support` ([ADR 0065](0065-the-audit-hash-chain-reading-and-retention.md)) |
| 0062 | 2026-11-22: sprint 6.2: five instruments, M42 to M46, with no tenant attribute |

### E3. Planning files and traceability

- `planning/roadmap.md`: 6.2's row unchanged until the sprint closes (the orchestrator marks it).
- The front matter of sprint.md stays `todo` until the orchestrator changes it.

## Part F — Verification (no commit unless something is found)

1. `make check`; `make test-compose`.
2. The three exit tests by name: `go test ./test/e2e -run 'TestReportLastMonthByModel|TestAuditVerifyDetectsTampering|TestExportApplyRoundTrip' -count=1`.
3. Upgrade from data: on a scratch database, check out the v0.1.x tag (`git tag -l 'v0.1.*'`), run its
   `pandokos migrate`, create tenants and calls with its binary, then this branch's `pandokos migrate`
   and `pandokos audit verify` for two tenants and the platform. Record the commands and the output in
   the pull request.
4. `EXPLAIN (ANALYZE, BUFFERS)` of `UsageReport` (a month by day, in `Asia/Kolkata`) and of
   `AggregateUsageForLimits` on 10⁶ generated records of one tenant with a current rollup: the plan uses
   `usage_rollup_pkey`, the primary key range of the partitions and `usage_record_recorded_idx`, and no
   sequential scan of a partition; record the timings in the pull request.
5. `grep -rn "pandokos.retention" internal/store/migrations/sql/` shows A1 only; `git diff --stat`
   against the base shows no edited migration but 0015.
6. Read the diff of `internal/authz/testdata/roles.golden` and of `docs/reference/permissions.md`: the
   two permissions on the five roles, nothing else.

## What the implementer does not do

- No change to migrations 0001–0014, to `limits.Sum`, to the reconciler or to the capability check's
  logic; no new path that reads usage in platform scope.
- No `--prune`, no export of secrets, keys, credentials or people, no conversion of currencies.
- No retention of `limit_event` or `price_import`, no pseudonymization (6.3).
- No events or outbox (6.4).

## Appendix A1 — `internal/store/migrations/sql/0015_reports_audit.sql`

```sql
-- 0015_reports_audit: the hash chain of the two audit logs and its heads, the audit read grants,
-- retention of usage and audit through two audited functions, the usage rollup and its state,
-- the index of the ledger by time of recording, and the metadata keys a tenant accepts
-- (ADR 0064, ADR 0065). Runs in one transaction as pandokos_owner. Every name is
-- schema-qualified. Existing audit rows are given their place in the chain once, here.

-- ---------------------------------------------------------------------------------------------
-- 1. Append-only, except for the retention functions.
-- ---------------------------------------------------------------------------------------------

-- A row of an append-only table is never changed. It is removed only by a function of this
-- migration that runs as the owner and sets pandokos.retention for the statement that deletes: no
-- runtime role holds DELETE on these tables, so the setting gives a runtime role nothing.
CREATE OR REPLACE FUNCTION pandokos.append_only() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'DELETE' AND pg_catalog.current_setting('pandokos.retention', true) = 'on' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
END
$$;

-- ---------------------------------------------------------------------------------------------
-- 2. The canonical encoding of an audit row and its hash (ADR 0065).
-- ---------------------------------------------------------------------------------------------

-- A text as its UTF-8 bytes after their length as four bytes, big-endian.
CREATE FUNCTION pandokos.audit_field(p text) RETURNS bytea
    LANGUAGE sql IMMUTABLE STRICT
    SET search_path = pg_catalog, pg_temp
    RETURN pg_catalog.int4send(pg_catalog.octet_length(pg_catalog.convert_to(p, 'UTF8')))
           || pg_catalog.convert_to(p, 'UTF8');

-- A JSON value as bytes: a tag, then for an object its member count and the members sorted by the
-- bytes of their keys (key, value), for an array its length and its elements in order, for a
-- string or a number its text as a field (a number as PostgreSQL writes it), and nothing more for
-- true, false and null.
CREATE FUNCTION pandokos.audit_canonical(j jsonb) RETURNS bytea
    LANGUAGE plpgsql IMMUTABLE STRICT
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    out bytea;
    k   text;
    v   jsonb;
BEGIN
    CASE pg_catalog.jsonb_typeof(j)
    WHEN 'object' THEN
        out := '\x6f'::bytea || pg_catalog.int4send((SELECT pg_catalog.count(*)::integer FROM pg_catalog.jsonb_object_keys(j)));
        FOR k, v IN SELECT e.key, e.value FROM pg_catalog.jsonb_each(j) AS e ORDER BY e.key COLLATE "C" LOOP
            out := out || pandokos.audit_field(k) || pandokos.audit_canonical(v);
        END LOOP;
    WHEN 'array' THEN
        out := '\x61'::bytea || pg_catalog.int4send(pg_catalog.jsonb_array_length(j));
        FOR v IN SELECT a.e FROM pg_catalog.jsonb_array_elements(j) WITH ORDINALITY AS a(e, n) ORDER BY a.n LOOP
            out := out || pandokos.audit_canonical(v);
        END LOOP;
    WHEN 'string' THEN
        out := '\x73'::bytea || pandokos.audit_field(j #>> '{}');
    WHEN 'number' THEN
        out := '\x6e'::bytea || pandokos.audit_field(j #>> '{}');
    WHEN 'boolean' THEN
        out := CASE WHEN j = 'true'::jsonb THEN '\x74'::bytea ELSE '\x66'::bytea END;
    ELSE
        out := '\x7a'::bytea;
    END CASE;
    RETURN out;
END
$$;

-- The hash of a row: SHA-256 over the version, the hash of the previous row, the fields in this
-- order and the SHA-256 of the canonical detail. scope is 'tenant:<id>' or 'platform'; the time
-- is UTC with six fraction digits; an absent support action is the empty text.
CREATE FUNCTION pandokos.audit_row_hash(p_scope text, p_seq bigint, p_id uuid, p_occurred_at timestamptz,
                                        p_actor_type text, p_actor_id text, p_actor_name text, p_key_id text,
                                        p_action text, p_resource_type text, p_resource_id text, p_outcome text,
                                        p_request_id text, p_reason text, p_support_action_id uuid, p_detail jsonb,
                                        p_prev_hash bytea) RETURNS bytea
    LANGUAGE sql STABLE
    SET search_path = pg_catalog, pg_temp
    RETURN pg_catalog.sha256(
        pandokos.audit_field('pandokos-audit-v1') || p_prev_hash
        || pandokos.audit_field(p_scope) || pandokos.audit_field(p_seq::text) || pandokos.audit_field(p_id::text)
        || pandokos.audit_field(pg_catalog.to_char(p_occurred_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'))
        || pandokos.audit_field(p_actor_type) || pandokos.audit_field(p_actor_id) || pandokos.audit_field(p_actor_name)
        || pandokos.audit_field(p_key_id) || pandokos.audit_field(p_action) || pandokos.audit_field(p_resource_type)
        || pandokos.audit_field(p_resource_id) || pandokos.audit_field(p_outcome) || pandokos.audit_field(p_request_id)
        || pandokos.audit_field(p_reason) || pandokos.audit_field(COALESCE(p_support_action_id::text, ''))
        || pg_catalog.sha256(pandokos.audit_canonical(p_detail)));

-- ---------------------------------------------------------------------------------------------
-- 3. The heads of the chains.
-- ---------------------------------------------------------------------------------------------

-- The head of a tenant's chain (tenant-scoped table): the last sequence number and its hash, and
-- the anchor, from which the chain is verified: the first row kept by retention and the hash of the
-- row before it. Only the seal and retention functions write it; the admin role reads it.
CREATE TABLE pandokos.audit_chain (
    tenant_id   uuid   NOT NULL,
    seq         bigint NOT NULL DEFAULT 0,
    hash        bytea  NOT NULL DEFAULT pg_catalog.decode(pg_catalog.repeat('00', 32), 'hex'),
    anchor_seq  bigint NOT NULL DEFAULT 1,
    anchor_hash bytea  NOT NULL DEFAULT pg_catalog.decode(pg_catalog.repeat('00', 32), 'hex'),
    CONSTRAINT audit_chain_pkey PRIMARY KEY (tenant_id),
    CONSTRAINT audit_chain_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT audit_chain_valid CHECK (seq >= 0 AND anchor_seq BETWEEN 1 AND seq + 1
                                        AND octet_length(hash) = 32 AND octet_length(anchor_hash) = 32)
);

ALTER TABLE pandokos.audit_chain ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.audit_chain FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.audit_chain
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.audit_chain TO pandokos_admin;

-- The head of the platform's chain (global table, one row).
CREATE TABLE pandokos.platform_audit_chain (
    id          smallint NOT NULL DEFAULT 1,
    seq         bigint   NOT NULL DEFAULT 0,
    hash        bytea    NOT NULL DEFAULT pg_catalog.decode(pg_catalog.repeat('00', 32), 'hex'),
    anchor_seq  bigint   NOT NULL DEFAULT 1,
    anchor_hash bytea    NOT NULL DEFAULT pg_catalog.decode(pg_catalog.repeat('00', 32), 'hex'),
    CONSTRAINT platform_audit_chain_pkey PRIMARY KEY (id),
    CONSTRAINT platform_audit_chain_one CHECK (id = 1),
    CONSTRAINT platform_audit_chain_valid CHECK (seq >= 0 AND anchor_seq BETWEEN 1 AND seq + 1
                                                 AND octet_length(hash) = 32 AND octet_length(anchor_hash) = 32)
);

ALTER TABLE pandokos.platform_audit_chain ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.platform_audit_chain FORCE ROW LEVEL SECURITY;

CREATE POLICY platform_audit_chain_owner ON pandokos.platform_audit_chain
    FOR ALL TO pandokos_owner USING (true) WITH CHECK (true);
CREATE POLICY platform_audit_chain_select ON pandokos.platform_audit_chain
    FOR SELECT TO pandokos_admin USING ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.platform_audit_chain TO pandokos_admin;

-- ---------------------------------------------------------------------------------------------
-- 4. The chain columns, and the rows written before this migration.
-- ---------------------------------------------------------------------------------------------

ALTER TABLE pandokos.audit_event          ADD COLUMN seq bigint, ADD COLUMN prev_hash bytea, ADD COLUMN hash bytea;
ALTER TABLE pandokos.platform_audit_event ADD COLUMN seq bigint, ADD COLUMN prev_hash bytea, ADD COLUMN hash bytea;

-- Forced row-level security hides every tenant's rows from the owner (ADR 0026), and the rows are
-- append-only: both are lifted for the statements below and put back before the migration ends.
ALTER TABLE pandokos.audit_event          NO FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.platform_audit_event NO FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.audit_chain          NO FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.audit_event          DISABLE TRIGGER audit_event_append_only;
ALTER TABLE pandokos.platform_audit_event DISABLE TRIGGER platform_audit_event_append_only;

-- Every existing row in the order it was written (time, then the time-ordered ID), chain by chain.
DO $$
DECLARE
    r     record;
    cur   uuid;
    n     bigint;
    prev  bytea;
    zero  bytea := pg_catalog.decode(pg_catalog.repeat('00', 32), 'hex');
BEGIN
    cur := NULL;
    FOR r IN SELECT * FROM pandokos.audit_event ORDER BY tenant_id, occurred_at, id LOOP
        IF cur IS DISTINCT FROM r.tenant_id THEN
            IF cur IS NOT NULL THEN
                INSERT INTO pandokos.audit_chain (tenant_id, seq, hash) VALUES (cur, n, prev);
            END IF;
            cur := r.tenant_id; n := 0; prev := zero;
        END IF;
        n := n + 1;
        UPDATE pandokos.audit_event e
           SET seq = n, prev_hash = prev,
               hash = pandokos.audit_row_hash('tenant:' || r.tenant_id::text, n, r.id, r.occurred_at, r.actor_type,
                                              r.actor_id, r.actor_name, r.key_id, r.action, r.resource_type,
                                              r.resource_id, r.outcome, r.request_id, r.reason, r.support_action_id,
                                              r.detail, prev)
         WHERE e.tenant_id = r.tenant_id AND e.id = r.id
        RETURNING e.hash INTO prev;
    END LOOP;
    IF cur IS NOT NULL THEN
        INSERT INTO pandokos.audit_chain (tenant_id, seq, hash) VALUES (cur, n, prev);
    END IF;

    n := 0; prev := zero;
    FOR r IN SELECT * FROM pandokos.platform_audit_event ORDER BY occurred_at, id LOOP
        n := n + 1;
        UPDATE pandokos.platform_audit_event e
           SET seq = n, prev_hash = prev,
               hash = pandokos.audit_row_hash('platform', n, r.id, r.occurred_at, r.actor_type, r.actor_id,
                                              r.actor_name, r.key_id, r.action, r.resource_type, r.resource_id,
                                              r.outcome, r.request_id, r.reason, NULL, r.detail, prev)
         WHERE e.id = r.id
        RETURNING e.hash INTO prev;
    END LOOP;
    INSERT INTO pandokos.platform_audit_chain (id, seq, hash) VALUES (1, n, prev);
END
$$;

ALTER TABLE pandokos.audit_event          ENABLE TRIGGER audit_event_append_only;
ALTER TABLE pandokos.platform_audit_event ENABLE TRIGGER platform_audit_event_append_only;
ALTER TABLE pandokos.audit_event          FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.platform_audit_event FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.audit_chain          FORCE ROW LEVEL SECURITY;

ALTER TABLE pandokos.audit_event
    ALTER COLUMN seq SET NOT NULL, ALTER COLUMN prev_hash SET NOT NULL, ALTER COLUMN hash SET NOT NULL,
    ADD CONSTRAINT audit_event_seq_key UNIQUE (tenant_id, seq),
    ADD CONSTRAINT audit_event_chain_valid CHECK (seq >= 1 AND octet_length(prev_hash) = 32 AND octet_length(hash) = 32);
ALTER TABLE pandokos.platform_audit_event
    ALTER COLUMN seq SET NOT NULL, ALTER COLUMN prev_hash SET NOT NULL, ALTER COLUMN hash SET NOT NULL,
    ADD CONSTRAINT platform_audit_event_seq_key UNIQUE (seq),
    ADD CONSTRAINT platform_audit_event_chain_valid CHECK (seq >= 1 AND octet_length(prev_hash) = 32 AND octet_length(hash) = 32);

-- ---------------------------------------------------------------------------------------------
-- 5. Every new row is sealed: its ID, its time, its place in the chain and its hash are the
--    database's, whoever writes it (the admin and worker roles, the functions of the gateway, a
--    migration). The head row is locked until the transaction ends, so the rows of one chain are
--    numbered in the order they commit. A row that is not inserted (a failed statement, a rolled
--    back transaction or subtransaction) takes its change of the head with it.
-- ---------------------------------------------------------------------------------------------

CREATE FUNCTION pandokos.audit_seal_tenant() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    prev   text := pg_catalog.current_setting('app.tenant_id', true);
    h_seq  bigint;
    h_hash bytea;
BEGIN
    NEW.id := pandokos.uuid_v7();
    NEW.occurred_at := pg_catalog.now();
    -- The head is read and written as the session of the row's tenant, whoever inserts (a
    -- migration or the owner may insert outside a tenant session). A row for another tenant than
    -- the session's is still refused by the policy of audit_event, after this trigger: the
    -- statement fails and its change of that head is rolled back with it.
    PERFORM pg_catalog.set_config('app.tenant_id', NEW.tenant_id::text, true);
    INSERT INTO pandokos.audit_chain (tenant_id) VALUES (NEW.tenant_id) ON CONFLICT (tenant_id) DO NOTHING;
    SELECT c.seq, c.hash INTO h_seq, h_hash FROM pandokos.audit_chain c WHERE c.tenant_id = NEW.tenant_id FOR UPDATE;
    NEW.seq := h_seq + 1;
    NEW.prev_hash := h_hash;
    NEW.hash := pandokos.audit_row_hash('tenant:' || NEW.tenant_id::text, NEW.seq, NEW.id, NEW.occurred_at,
                                        NEW.actor_type, NEW.actor_id, NEW.actor_name, NEW.key_id, NEW.action,
                                        NEW.resource_type, NEW.resource_id, NEW.outcome, NEW.request_id, NEW.reason,
                                        NEW.support_action_id, NEW.detail, NEW.prev_hash);
    UPDATE pandokos.audit_chain SET seq = NEW.seq, hash = NEW.hash WHERE tenant_id = NEW.tenant_id;
    PERFORM pg_catalog.set_config('app.tenant_id', COALESCE(prev, ''), true);
    RETURN NEW;
END
$$;

CREATE FUNCTION pandokos.audit_seal_platform() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    h_seq  bigint;
    h_hash bytea;
BEGIN
    NEW.id := pandokos.uuid_v7();
    NEW.occurred_at := pg_catalog.now();
    SELECT c.seq, c.hash INTO h_seq, h_hash FROM pandokos.platform_audit_chain c WHERE c.id = 1 FOR UPDATE;
    NEW.seq := h_seq + 1;
    NEW.prev_hash := h_hash;
    NEW.hash := pandokos.audit_row_hash('platform', NEW.seq, NEW.id, NEW.occurred_at, NEW.actor_type, NEW.actor_id,
                                        NEW.actor_name, NEW.key_id, NEW.action, NEW.resource_type, NEW.resource_id,
                                        NEW.outcome, NEW.request_id, NEW.reason, NULL, NEW.detail, NEW.prev_hash);
    UPDATE pandokos.platform_audit_chain SET seq = NEW.seq, hash = NEW.hash WHERE id = 1;
    RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION pandokos.audit_seal_tenant() FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.audit_seal_platform() FROM PUBLIC;

DROP TRIGGER audit_event_stamp ON pandokos.audit_event;
DROP TRIGGER platform_audit_event_stamp ON pandokos.platform_audit_event;
DROP FUNCTION pandokos.audit_stamp();

CREATE TRIGGER audit_event_seal BEFORE INSERT ON pandokos.audit_event
    FOR EACH ROW EXECUTE FUNCTION pandokos.audit_seal_tenant();
CREATE TRIGGER platform_audit_event_seal BEFORE INSERT ON pandokos.platform_audit_event
    FOR EACH ROW EXECUTE FUNCTION pandokos.audit_seal_platform();

-- A sealed row's BEFORE INSERT trigger runs before ON CONFLICT is decided, so the one function
-- that relied on ON CONFLICT ... DO NOTHING (migration 0011) now inserts in a subtransaction and
-- treats a duplicate as the row already written. Same signature, same answer.
CREATE OR REPLACE FUNCTION pandokos.record_unlimited_traffic(p_day date, p_time_zone text, p_provider text, p_model text,
                                                            p_request_id text) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t       uuid := pandokos.current_tenant_id();
    d       jsonb;
    written boolean := false;
BEGIN
    IF t IS NULL OR p_day IS NULL
       OR p_day NOT BETWEEN (pg_catalog.timezone('UTC', pg_catalog.now()))::date - 2
                        AND (pg_catalog.timezone('UTC', pg_catalog.now()))::date + 2
       OR p_time_zone IS NULL OR p_time_zone !~ '^[A-Za-z][A-Za-z0-9_+-]{0,31}(/[A-Za-z0-9_+-]{1,31}){0,2}$'
       OR p_provider IS NULL OR p_provider !~ '^[a-z][a-z0-9-]{0,62}$'
       OR p_model IS NULL OR p_model !~ '^([A-Za-z0-9][A-Za-z0-9._:@/+-]{0,127})?$'
       OR p_request_id IS NULL OR p_request_id !~ '^[0-9a-f]{32}$' THEN
        RAISE EXCEPTION 'invalid unlimited traffic record' USING ERRCODE = 'check_violation';
    END IF;
    d := pg_catalog.jsonb_build_object('day', pg_catalog.to_char(p_day, 'YYYY-MM-DD'), 'time_zone', p_time_zone,
                                       'provider', p_provider, 'model', p_model);
    BEGIN
        INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_name, action, resource_type, resource_id,
                                          outcome, request_id, detail)
        VALUES (t, 'platform', 'gateway', 'limit.unlimited_traffic', 'tenant', t::text, 'success', p_request_id, d);
        written := true;
    EXCEPTION WHEN unique_violation THEN
        NULL;
    END;
    BEGIN
        INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, resource_id,
                                                   outcome, request_id, detail)
        VALUES ('platform', 'gateway', 'limit.unlimited_traffic', 'tenant', t::text, 'success', p_request_id, d);
    EXCEPTION WHEN unique_violation THEN
        NULL;
    END;
    RETURN written;
END
$$;

-- ---------------------------------------------------------------------------------------------
-- 6. Reading the logs (AUD-05). A tenant's session reads its own log; the platform scope of the
--    admin role reads the platform's.
-- ---------------------------------------------------------------------------------------------

GRANT SELECT ON pandokos.audit_event TO pandokos_admin;

CREATE POLICY platform_audit_event_select ON pandokos.platform_audit_event
    FOR SELECT TO pandokos_admin
    USING ((SELECT pandokos.platform_scope()));
GRANT SELECT ON pandokos.platform_audit_event TO pandokos_admin;

-- ---------------------------------------------------------------------------------------------
-- 7. The usage rollup (ADR 0064): what the ledger holds, by hour (UTC) of the request's start and
--    by every dimension a report or a limit can name, for the records recorded before the
--    watermark of the tenant. Derived and rebuilt from the ledger, so not append-only: the worker
--    adds to it and removes what retention removed from the ledger.
-- ---------------------------------------------------------------------------------------------

CREATE TABLE pandokos.usage_rollup (
    tenant_id               uuid           NOT NULL,
    hour                    timestamptz    NOT NULL,
    principal_kind          text           NOT NULL,
    principal_id            text           NOT NULL,
    key_id                  text           NOT NULL,
    provider                text           NOT NULL,
    model                   text           NOT NULL,
    model_id                text           NOT NULL,
    currency                text           NOT NULL,
    price_source            text           NOT NULL,
    outcome                 text           NOT NULL,
    credential              boolean        NOT NULL,
    requests                bigint         NOT NULL,
    input_tokens            bigint         NOT NULL,
    output_tokens           bigint         NOT NULL,
    cache_read_tokens       bigint         NOT NULL,
    cache_write_tokens      bigint         NOT NULL,
    reasoning_tokens        bigint         NOT NULL,
    estimated_input_tokens  bigint         NOT NULL,
    estimated_output_tokens bigint         NOT NULL,
    cost                    numeric(38,18) NOT NULL,
    cost_known              bigint         NOT NULL,
    cost_unknown            bigint         NOT NULL,
    CONSTRAINT usage_rollup_pkey PRIMARY KEY (tenant_id, hour, principal_kind, principal_id, key_id, provider, model,
                                              model_id, currency, price_source, outcome, credential),
    CONSTRAINT usage_rollup_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT usage_rollup_hour_valid CHECK (hour = pg_catalog.date_bin('1 hour', hour, '2000-01-01 00:00:00+00')),
    CONSTRAINT usage_rollup_currency_format CHECK (currency ~ '^([A-Z]{3})?$'),
    CONSTRAINT usage_rollup_price_source_valid CHECK (price_source IN ('', 'catalogue', 'tenant')),
    CONSTRAINT usage_rollup_values_valid CHECK (
        requests >= 1 AND input_tokens >= 0 AND output_tokens >= 0 AND cache_read_tokens >= 0
        AND cache_write_tokens >= 0 AND reasoning_tokens >= 0 AND estimated_input_tokens >= 0
        AND estimated_output_tokens >= 0 AND cost >= 0 AND cost_known BETWEEN 0 AND requests
        AND cost_unknown BETWEEN 0 AND requests)
);

ALTER TABLE pandokos.usage_rollup ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.usage_rollup FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.usage_rollup
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.usage_rollup TO pandokos_admin, pandokos_worker;
GRANT INSERT, UPDATE, DELETE ON pandokos.usage_rollup TO pandokos_worker;

-- How far the rollup of a tenant has gone: every record recorded before watermark is in it, none
-- recorded at or after it.
CREATE TABLE pandokos.usage_rollup_state (
    tenant_id  uuid        NOT NULL,
    watermark  timestamptz NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT usage_rollup_state_pkey PRIMARY KEY (tenant_id),
    CONSTRAINT usage_rollup_state_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id)
);

ALTER TABLE pandokos.usage_rollup_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.usage_rollup_state FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.usage_rollup_state
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.usage_rollup_state TO pandokos_admin, pandokos_worker;
GRANT INSERT (tenant_id, watermark), UPDATE (watermark, updated_at) ON pandokos.usage_rollup_state TO pandokos_worker;

-- The rollup reads the records of a tenant by the time they were recorded. On a large ledger this
-- statement builds one index per partition while the table takes no writes; the usage guide shows
-- how to build them beforehand without blocking (they are then attached, not rebuilt).
CREATE INDEX usage_record_recorded_idx ON pandokos.usage_record (tenant_id, recorded_at);

-- ---------------------------------------------------------------------------------------------
-- 8. Retention (USE-08, AUD-06). Two functions run by the worker role, both audited.
-- ---------------------------------------------------------------------------------------------

-- The owner removes rows of the platform log only inside platform_retention, and writes the two
-- retention actions there.
CREATE POLICY platform_audit_event_retention_select ON pandokos.platform_audit_event
    FOR SELECT TO pandokos_owner
    USING (pg_catalog.current_setting('pandokos.retention', true) = 'on');
CREATE POLICY platform_audit_event_retention_delete ON pandokos.platform_audit_event
    FOR DELETE TO pandokos_owner
    USING (pg_catalog.current_setting('pandokos.retention', true) = 'on');
CREATE POLICY platform_audit_event_retention_insert ON pandokos.platform_audit_event
    FOR INSERT TO pandokos_owner
    WITH CHECK (action IN ('usage.retention', 'audit.retention'));

-- In platform scope: drops the monthly partitions of the ledger that end before the start of the
-- current UTC month minus p_usage_months months, and removes the rows of the platform log written
-- before the start of the current UTC month minus p_audit_months months, from the start of the
-- chain: the anchor moves past them. 0 keeps everything of that kind. Each removal is recorded in
-- the platform log. Takes no name and no date from its caller.
CREATE FUNCTION pandokos.platform_retention(p_usage_months integer, p_audit_months integer,
                                           OUT partitions_dropped integer, OUT audit_rows bigint)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_month date := pg_catalog.date_trunc('month', pg_catalog.timezone('UTC', pg_catalog.now()))::date;
    v_cut   date;
    v_names text[] := '{}';
    v_name  text;
    v_last  bigint;
    v_first bigint;
    v_hash  bytea;
BEGIN
    IF NOT pandokos.platform_scope() THEN
        RAISE EXCEPTION 'platform_retention needs the platform scope' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF p_usage_months IS NULL OR (p_usage_months <> 0 AND p_usage_months NOT BETWEEN 2 AND 120)
       OR p_audit_months IS NULL OR (p_audit_months <> 0 AND p_audit_months NOT BETWEEN 1 AND 1200) THEN
        RAISE EXCEPTION 'invalid retention' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM pg_catalog.set_config('lock_timeout', '5s', true);
    partitions_dropped := 0;
    audit_rows := 0;
    IF p_usage_months > 0 THEN
        v_cut := (v_month - pg_catalog.make_interval(months => p_usage_months))::date;
        PERFORM pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('pandokos.usage_record.partitions', 0));
        FOR v_name IN
            SELECT c.relname::text
              FROM pg_catalog.pg_inherits i
              JOIN pg_catalog.pg_class c ON c.oid = i.inhrelid
             WHERE i.inhparent = 'pandokos.usage_record'::pg_catalog.regclass
               AND c.relname ~ '^usage_record_y[0-9]{4}m[0-9]{2}$'
               AND (pg_catalog.to_date(pg_catalog.substr(c.relname, 14), '"y"YYYY"m"MM')
                    + pg_catalog.make_interval(months => 1))::date <= v_cut
             ORDER BY 1
        LOOP
            EXECUTE pg_catalog.format('DROP TABLE pandokos.%I', v_name);
            v_names := v_names || v_name;
        END LOOP;
        partitions_dropped := pg_catalog.cardinality(v_names);
        IF partitions_dropped > 0 THEN
            INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, outcome, detail)
            VALUES ('platform', 'retention', 'usage.retention', 'usage_record', 'success',
                    pg_catalog.jsonb_build_object('before', pg_catalog.to_char(v_cut, 'YYYY-MM-DD'),
                                                  'months', p_usage_months, 'partitions', pg_catalog.to_jsonb(v_names)));
        END IF;
    END IF;
    IF p_audit_months > 0 THEN
        v_cut := (v_month - pg_catalog.make_interval(months => p_audit_months))::date;
        PERFORM pg_catalog.set_config('pandokos.retention', 'on', true);
        SELECT c.anchor_seq INTO v_first FROM pandokos.platform_audit_chain c WHERE c.id = 1 FOR UPDATE;
        SELECT pg_catalog.max(e.seq) INTO v_last FROM pandokos.platform_audit_event e
         WHERE e.occurred_at < (v_cut::timestamp AT TIME ZONE 'UTC');
        IF v_last IS NOT NULL AND v_last >= v_first THEN
            SELECT e.hash INTO v_hash FROM pandokos.platform_audit_event e WHERE e.seq = v_last;
            DELETE FROM pandokos.platform_audit_event e WHERE e.seq <= v_last;
            GET DIAGNOSTICS audit_rows = ROW_COUNT;
            UPDATE pandokos.platform_audit_chain SET anchor_seq = v_last + 1, anchor_hash = v_hash WHERE id = 1;
        END IF;
        PERFORM pg_catalog.set_config('pandokos.retention', 'off', true);
        IF audit_rows > 0 THEN
            INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, outcome, detail)
            VALUES ('platform', 'retention', 'audit.retention', 'platform_audit_event', 'success',
                    pg_catalog.jsonb_build_object('before', pg_catalog.to_char(v_cut, 'YYYY-MM-DD'),
                                                  'months', p_audit_months, 'deleted', audit_rows,
                                                  'first_seq', v_first, 'last_seq', v_last,
                                                  'last_hash', pg_catalog.encode(v_hash, 'hex')));
        END IF;
    END IF;
END
$$;

-- In the session of a tenant: removes the tenant's rollup rows and the tenant's rows of the default
-- partition of the ledger whose hour or start is before the cut of p_usage_months, and the rows of
-- the tenant's log before the cut of p_audit_months, from the start of the chain. The removal of
-- ledger or log rows is recorded in the tenant's log.
CREATE FUNCTION pandokos.tenant_retention(p_usage_months integer, p_audit_months integer,
                                         OUT usage_rows bigint, OUT rollup_rows bigint, OUT audit_rows bigint)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t       uuid := pandokos.current_tenant_id();
    v_month date := pg_catalog.date_trunc('month', pg_catalog.timezone('UTC', pg_catalog.now()))::date;
    v_cut   date;
    v_at    timestamptz;
    v_last  bigint;
    v_first bigint;
    v_hash  bytea;
BEGIN
    IF t IS NULL THEN
        RAISE EXCEPTION 'tenant_retention needs a tenant session' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF p_usage_months IS NULL OR (p_usage_months <> 0 AND p_usage_months NOT BETWEEN 2 AND 120)
       OR p_audit_months IS NULL OR (p_audit_months <> 0 AND p_audit_months NOT BETWEEN 1 AND 1200) THEN
        RAISE EXCEPTION 'invalid retention' USING ERRCODE = 'check_violation';
    END IF;
    usage_rows := 0;
    rollup_rows := 0;
    audit_rows := 0;
    IF p_usage_months > 0 THEN
        v_cut := (v_month - pg_catalog.make_interval(months => p_usage_months))::date;
        v_at := v_cut::timestamp AT TIME ZONE 'UTC';
        DELETE FROM pandokos.usage_rollup r WHERE r.hour < v_at;
        GET DIAGNOSTICS rollup_rows = ROW_COUNT;
        PERFORM pg_catalog.set_config('pandokos.retention', 'on', true);
        DELETE FROM pandokos.usage_record_default u WHERE u.started_at < v_at;
        GET DIAGNOSTICS usage_rows = ROW_COUNT;
        PERFORM pg_catalog.set_config('pandokos.retention', 'off', true);
        IF usage_rows > 0 THEN
            INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_name, action, resource_type, outcome, detail)
            VALUES (t, 'platform', 'retention', 'usage.retention', 'usage_record', 'success',
                    pg_catalog.jsonb_build_object('before', pg_catalog.to_char(v_cut, 'YYYY-MM-DD'),
                                                  'months', p_usage_months, 'deleted', usage_rows));
        END IF;
    END IF;
    IF p_audit_months > 0 THEN
        v_cut := (v_month - pg_catalog.make_interval(months => p_audit_months))::date;
        INSERT INTO pandokos.audit_chain (tenant_id) VALUES (t) ON CONFLICT (tenant_id) DO NOTHING;
        SELECT c.anchor_seq INTO v_first FROM pandokos.audit_chain c WHERE c.tenant_id = t FOR UPDATE;
        SELECT pg_catalog.max(e.seq) INTO v_last FROM pandokos.audit_event e
         WHERE e.occurred_at < (v_cut::timestamp AT TIME ZONE 'UTC');
        IF v_last IS NOT NULL AND v_last >= v_first THEN
            SELECT e.hash INTO v_hash FROM pandokos.audit_event e WHERE e.seq = v_last;
            PERFORM pg_catalog.set_config('pandokos.retention', 'on', true);
            DELETE FROM pandokos.audit_event e WHERE e.seq <= v_last;
            GET DIAGNOSTICS audit_rows = ROW_COUNT;
            PERFORM pg_catalog.set_config('pandokos.retention', 'off', true);
            UPDATE pandokos.audit_chain SET anchor_seq = v_last + 1, anchor_hash = v_hash WHERE tenant_id = t;
            INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_name, action, resource_type, outcome, detail)
            VALUES (t, 'platform', 'retention', 'audit.retention', 'audit_event', 'success',
                    pg_catalog.jsonb_build_object('before', pg_catalog.to_char(v_cut, 'YYYY-MM-DD'),
                                                  'months', p_audit_months, 'deleted', audit_rows,
                                                  'first_seq', v_first, 'last_seq', v_last,
                                                  'last_hash', pg_catalog.encode(v_hash, 'hex')));
        END IF;
    END IF;
END
$$;

REVOKE ALL ON FUNCTION pandokos.platform_retention(integer, integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.tenant_retention(integer, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.platform_retention(integer, integer) TO pandokos_worker;
GRANT EXECUTE ON FUNCTION pandokos.tenant_retention(integer, integer) TO pandokos_worker;

-- ---------------------------------------------------------------------------------------------
-- 9. The metadata keys a tenant accepts (PRV-03), as tenant_model_restriction does for models
--    (tenant-scoped table). No row: every key that has the format; a row: only the keys it lists.
--    The gateway reads it with the tenant's catalogue view and is told when it changes.
-- ---------------------------------------------------------------------------------------------

CREATE TABLE pandokos.metadata_key_policy (
    tenant_id  uuid        NOT NULL,
    id         uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    keys       text[]      NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT metadata_key_policy_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT metadata_key_policy_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT metadata_key_policy_tenant_key UNIQUE (tenant_id),
    CONSTRAINT metadata_key_policy_keys_valid CHECK (
        cardinality(keys) <= 64 AND array_position(keys, NULL) IS NULL
        AND array_to_string(keys, ' ') ~ '^([a-z0-9][a-z0-9_-]{0,31}( [a-z0-9][a-z0-9_-]{0,31})*)?$')
);

CREATE TRIGGER metadata_key_policy_guard BEFORE INSERT OR UPDATE ON pandokos.metadata_key_policy
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();
CREATE TRIGGER metadata_key_policy_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.metadata_key_policy
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_catalog('scoped');

ALTER TABLE pandokos.metadata_key_policy ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.metadata_key_policy FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.metadata_key_policy
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.metadata_key_policy TO pandokos_gateway, pandokos_admin;
GRANT INSERT (tenant_id, keys) ON pandokos.metadata_key_policy TO pandokos_admin;
GRANT UPDATE (keys) ON pandokos.metadata_key_policy TO pandokos_admin;
GRANT DELETE ON pandokos.metadata_key_policy TO pandokos_admin;
```

## Appendix A2 — `internal/store/queries/sql/reports.sql`

```sql
-- Usage rollup, reports and exports (ADR 0064). Every statement runs in the session of one tenant,
-- except ListTenantsForWorker (platform scope, identifiers only) and PlatformRetention. The rollup
-- is written by the worker role only; the admin role reads it.

-- name: ListTenantsForWorker :many
SELECT t.id FROM pandokos.tenant t WHERE t.state IN ('active', 'suspended', 'deleting') ORDER BY t.id;

-- name: LockUsageRollup :one
INSERT INTO pandokos.usage_rollup_state AS s (tenant_id, watermark)
VALUES (pandokos.current_tenant_id(),
        COALESCE((SELECT min(u.recorded_at) FROM pandokos.usage_record u), now() - interval '1 minute'))
ON CONFLICT (tenant_id) DO UPDATE SET watermark = s.watermark
RETURNING s.watermark;

-- The records recorded in [watermark, upto) are added to the rollup and the watermark moves to
-- upto: at most seven days of recording at a time, never past one minute before now (longer than
-- any transaction of the ledger writer, which ends within writeTimeout).

-- name: RollUpUsage :one
WITH bound AS (
    SELECT GREATEST(sqlc.arg(watermark)::timestamptz,
                    LEAST(now() - interval '1 minute', sqlc.arg(watermark)::timestamptz + interval '7 days')) AS upto
), added AS (
    INSERT INTO pandokos.usage_rollup AS r (tenant_id, hour, principal_kind, principal_id, key_id, provider, model, model_id,
                                            currency, price_source, outcome, credential, requests, input_tokens,
                                            output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
                                            estimated_input_tokens, estimated_output_tokens, cost, cost_known, cost_unknown)
    SELECT pandokos.current_tenant_id(), date_bin('1 hour', u.started_at, '2000-01-01 00:00:00+00'), u.principal_kind,
           u.principal_id, u.key_id, u.provider, u.model, COALESCE(u.model_id::text, ''), COALESCE(u.currency, ''),
           COALESCE(u.price_source, CASE WHEN u.price_version_id IS NOT NULL THEN 'catalogue' END, ''), u.outcome,
           u.credential_id IS NOT NULL, count(*), COALESCE(sum(u.input_tokens), 0), COALESCE(sum(u.output_tokens), 0),
           COALESCE(sum(u.cache_read_tokens), 0), COALESCE(sum(u.cache_write_tokens), 0),
           COALESCE(sum(u.reasoning_tokens), 0), COALESCE(sum(u.estimated_input_tokens), 0),
           COALESCE(sum(u.estimated_output_tokens), 0), COALESCE(sum(u.cost), 0), count(u.cost),
           count(*) FILTER (WHERE u.cost_status = 'unknown')
      FROM pandokos.usage_record u, bound b
     WHERE u.recorded_at >= sqlc.arg(watermark)::timestamptz AND u.recorded_at < b.upto
     GROUP BY 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12
    ON CONFLICT ON CONSTRAINT usage_rollup_pkey DO UPDATE SET
           requests = r.requests + EXCLUDED.requests, input_tokens = r.input_tokens + EXCLUDED.input_tokens,
           output_tokens = r.output_tokens + EXCLUDED.output_tokens,
           cache_read_tokens = r.cache_read_tokens + EXCLUDED.cache_read_tokens,
           cache_write_tokens = r.cache_write_tokens + EXCLUDED.cache_write_tokens,
           reasoning_tokens = r.reasoning_tokens + EXCLUDED.reasoning_tokens,
           estimated_input_tokens = r.estimated_input_tokens + EXCLUDED.estimated_input_tokens,
           estimated_output_tokens = r.estimated_output_tokens + EXCLUDED.estimated_output_tokens,
           cost = r.cost + EXCLUDED.cost, cost_known = r.cost_known + EXCLUDED.cost_known,
           cost_unknown = r.cost_unknown + EXCLUDED.cost_unknown
    RETURNING 1
)
UPDATE pandokos.usage_rollup_state s SET watermark = b.upto, updated_at = now()
  FROM bound b
 WHERE s.tenant_id = pandokos.current_tenant_id()
RETURNING s.watermark, (SELECT count(*) FROM added)::bigint AS groups;

-- The report: the records whose start lies in [bounds[1], bounds[n]), grouped by the period
-- [bounds[i], bounds[i+1]) they started in and by the dimensions asked for, always by currency and
-- price source. Whole hours that no boundary cuts come from the rollup for the records recorded
-- before the watermark; everything else from the ledger. See ADR 0064 for why the sum is exact.

-- name: UsageReport :many
WITH w0 AS (
    SELECT sqlc.arg(bounds)::timestamptz[] AS bounds,
           COALESCE((SELECT s.watermark FROM pandokos.usage_rollup_state s), '-infinity'::timestamptz) AS wm
), w1 AS (
    SELECT w0.*, w0.bounds[1] AS f, w0.bounds[cardinality(w0.bounds)] AS t,
           CASE WHEN date_bin('1 hour', w0.bounds[1], '2000-01-01 00:00:00+00') = w0.bounds[1] THEN w0.bounds[1]
                ELSE date_bin('1 hour', w0.bounds[1], '2000-01-01 00:00:00+00') + interval '1 hour' END AS lo0,
           date_bin('1 hour', w0.bounds[cardinality(w0.bounds)], '2000-01-01 00:00:00+00') AS hi0
      FROM w0
), w AS (
    SELECT w1.bounds, w1.wm, w1.f, w1.t,
           CASE WHEN w1.hi0 > w1.lo0 THEN w1.lo0 ELSE w1.f END AS lo,
           CASE WHEN w1.hi0 > w1.lo0 THEN w1.hi0 ELSE w1.f END AS hi
      FROM w1
), d AS (
    SELECT COALESCE(array_agg(DISTINCT date_bin('1 hour', b, '2000-01-01 00:00:00+00')), '{}')::timestamptz[] AS dirty
      FROM w, unnest(w.bounds) AS b
     WHERE date_bin('1 hour', b, '2000-01-01 00:00:00+00') <> b
       AND date_bin('1 hour', b, '2000-01-01 00:00:00+00') >= w.lo
       AND date_bin('1 hour', b, '2000-01-01 00:00:00+00') < w.hi
), parts AS (
    SELECT r.hour AS at, r.provider, r.model, r.model_id, r.principal_kind, r.principal_id, r.key_id, r.currency,
           r.price_source, r.outcome, r.requests, r.input_tokens, r.output_tokens, r.cache_read_tokens,
           r.cache_write_tokens, r.reasoning_tokens, r.estimated_input_tokens, r.estimated_output_tokens, r.cost,
           r.cost_known, r.cost_unknown
      FROM pandokos.usage_rollup r, w, d
     WHERE r.hour >= w.lo AND r.hour < w.hi AND r.hour <> ALL (d.dirty)
    UNION ALL
    SELECT u.started_at, u.provider, u.model, COALESCE(u.model_id::text, ''), u.principal_kind, u.principal_id,
           u.key_id, COALESCE(u.currency, ''),
           COALESCE(u.price_source, CASE WHEN u.price_version_id IS NOT NULL THEN 'catalogue' END, ''), u.outcome, 1,
           COALESCE(u.input_tokens, 0), COALESCE(u.output_tokens, 0), COALESCE(u.cache_read_tokens, 0),
           COALESCE(u.cache_write_tokens, 0), COALESCE(u.reasoning_tokens, 0), COALESCE(u.estimated_input_tokens, 0),
           COALESCE(u.estimated_output_tokens, 0), COALESCE(u.cost, 0), (u.cost IS NOT NULL)::integer,
           (u.cost_status = 'unknown')::integer
      FROM pandokos.usage_record u, w, d
     WHERE (u.started_at >= w.f AND u.started_at < w.lo)
        OR (u.started_at >= w.hi AND u.started_at < w.t)
        OR (u.started_at >= w.lo AND u.started_at < w.hi
            AND (u.recorded_at >= w.wm OR date_bin('1 hour', u.started_at, '2000-01-01 00:00:00+00') = ANY (d.dirty)))
)
SELECT width_bucket(p.at, w.bounds)::integer AS period,
       (CASE WHEN sqlc.arg(by_provider)::boolean THEN p.provider ELSE '' END)::text AS provider,
       (CASE WHEN sqlc.arg(by_model)::boolean THEN p.model ELSE '' END)::text AS model,
       (CASE WHEN sqlc.arg(by_model)::boolean THEN p.model_id ELSE '' END)::text AS model_id,
       (CASE WHEN sqlc.arg(by_caller)::boolean THEN p.principal_kind ELSE '' END)::text AS principal_kind,
       (CASE WHEN sqlc.arg(by_caller)::boolean THEN p.principal_id ELSE '' END)::text AS principal_id,
       (CASE WHEN sqlc.arg(by_key)::boolean THEN p.key_id ELSE '' END)::text AS key_id,
       p.currency, p.price_source,
       COALESCE(sum(p.requests) FILTER (WHERE p.outcome <> 'rejected_limit'), 0)::bigint AS requests,
       COALESCE(sum(p.requests) FILTER (WHERE p.outcome = 'ok'), 0)::bigint AS requests_ok,
       COALESCE(sum(p.requests) FILTER (WHERE p.outcome = 'rejected_limit'), 0)::bigint AS rejected_limit,
       sum(p.input_tokens)::bigint AS input_tokens, sum(p.output_tokens)::bigint AS output_tokens,
       sum(p.cache_read_tokens)::bigint AS cache_read_tokens, sum(p.cache_write_tokens)::bigint AS cache_write_tokens,
       sum(p.reasoning_tokens)::bigint AS reasoning_tokens,
       sum(p.estimated_input_tokens)::bigint AS estimated_input_tokens,
       sum(p.estimated_output_tokens)::bigint AS estimated_output_tokens,
       sum(p.cost)::numeric AS cost, sum(p.cost_known)::bigint AS cost_known, sum(p.cost_unknown)::bigint AS cost_unknown
  FROM parts p, w
 WHERE (sqlc.arg(provider)::text = '' OR p.provider = sqlc.arg(provider)::text)
   AND (sqlc.arg(model)::text = '' OR p.model = sqlc.arg(model)::text)
 GROUP BY 1, 2, 3, 4, 5, 6, 7, 8, 9
 ORDER BY 1, 2, 3, 4, 5, 6, 7, 8, 9
 LIMIT sqlc.arg(max_rows)::integer + 1;

-- name: ExportUsageRecords :many
SELECT u.id, u.started_at, u.request_id, u.trace_id, u.principal_kind, u.principal_id, u.key_id, u.credential_id,
       u.provider, u.api_family, u.operation, u.endpoint, u.model, u.model_id, u.upstream_model, u.stream,
       u.outcome, u.error_code, u.upstream_status, u.ttfb_ms, u.duration_ms, u.request_bytes, u.response_bytes,
       u.input_source, u.output_source, u.input_tokens, u.output_tokens, u.cache_read_tokens, u.cache_write_tokens,
       u.reasoning_tokens, u.other_units, u.provider_usage, u.estimated_input_tokens, u.estimated_output_tokens,
       u.cost_status, u.cost_reason, u.cost, u.currency, u.price_version_id, u.cost_fallbacks, u.metadata,
       u.metadata_dropped, COALESCE(u.limit_state, '')::text AS limit_state,
       COALESCE(u.price_source, CASE WHEN u.price_version_id IS NOT NULL THEN 'catalogue' END, '')::text AS price_source
  FROM pandokos.usage_record u
 WHERE u.started_at >= sqlc.arg(from_at)::timestamptz
   AND u.started_at <  sqlc.arg(to_at)::timestamptz
   AND (sqlc.arg(provider)::text = '' OR u.provider = sqlc.arg(provider)::text)
   AND (NOT sqlc.arg(after)::boolean
        OR (u.started_at, u.id) > (sqlc.arg(after_started_at)::timestamptz, sqlc.arg(after_id)::uuid))
 ORDER BY u.started_at, u.id
 LIMIT sqlc.arg(page_size)::integer;

-- name: TenantRetention :one
SELECT usage_rows::bigint AS usage_rows, rollup_rows::bigint AS rollup_rows, audit_rows::bigint AS audit_rows
  FROM pandokos.tenant_retention(sqlc.arg(usage_months)::integer, sqlc.arg(audit_months)::integer);

-- name: PlatformRetention :one
SELECT partitions_dropped::integer AS partitions_dropped, audit_rows::bigint AS audit_rows
  FROM pandokos.platform_retention(sqlc.arg(usage_months)::integer, sqlc.arg(audit_months)::integer);

-- name: GetMetadataKeyPolicy :one
SELECT p.keys, p.updated_at FROM pandokos.metadata_key_policy p;

-- name: SetMetadataKeyPolicy :one
INSERT INTO pandokos.metadata_key_policy AS p (tenant_id, keys)
VALUES (pandokos.current_tenant_id(), sqlc.arg(keys)::text[])
ON CONFLICT (tenant_id) DO UPDATE SET keys = EXCLUDED.keys
RETURNING p.keys, p.updated_at;

-- name: ClearMetadataKeyPolicy :execrows
DELETE FROM pandokos.metadata_key_policy;
```

## Appendix A2b — `internal/store/queries/sql/audit.sql` (new header and the statements added)

```sql
-- Audit. Inserts (InsertAuditEvent, InsertPlatformAuditEvent, RecordAuthFailure) and, since 0015,
-- reads: a tenant's session reads its log and the head of its chain, the platform scope of the admin
-- role the platform's. detail is read as text: its canonical encoding is computed from it (ADR 0065).

-- name: ListAuditEvents :many
SELECT e.seq, e.id, e.occurred_at, e.actor_type, e.actor_id, e.actor_name, e.key_id, e.action, e.resource_type,
       e.resource_id, e.outcome, e.request_id, e.reason, COALESCE(e.support_action_id::text, '')::text AS support_action_id,
       e.detail::text AS detail, e.prev_hash, e.hash
  FROM pandokos.audit_event e
 WHERE e.seq < sqlc.arg(before_seq)::bigint
   AND (sqlc.narg(from_at)::timestamptz IS NULL OR e.occurred_at >= sqlc.narg(from_at)::timestamptz)
   AND (sqlc.narg(to_at)::timestamptz IS NULL OR e.occurred_at < sqlc.narg(to_at)::timestamptz)
   AND (sqlc.arg(action)::text = '' OR e.action = sqlc.arg(action)::text
        OR (right(sqlc.arg(action)::text, 1) = '.' AND starts_with(e.action, sqlc.arg(action)::text)))
   AND (sqlc.arg(resource_type)::text = '' OR e.resource_type = sqlc.arg(resource_type)::text)
   AND (sqlc.arg(resource_id)::text = '' OR e.resource_id = sqlc.arg(resource_id)::text)
   AND (sqlc.arg(actor_type)::text = '' OR e.actor_type = sqlc.arg(actor_type)::text)
   AND (sqlc.arg(actor_id)::text = '' OR e.actor_id = sqlc.arg(actor_id)::text)
   AND (sqlc.arg(outcome)::text = '' OR e.outcome = sqlc.arg(outcome)::text)
   AND (sqlc.arg(request_id)::text = '' OR e.request_id = sqlc.arg(request_id)::text)
   AND (sqlc.arg(support_action_id)::text = '' OR e.support_action_id = sqlc.arg(support_action_id)::text::uuid)
 ORDER BY e.seq DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: ExportAuditEvents :many
SELECT e.seq, e.id, e.occurred_at, e.actor_type, e.actor_id, e.actor_name, e.key_id, e.action, e.resource_type,
       e.resource_id, e.outcome, e.request_id, e.reason, COALESCE(e.support_action_id::text, '')::text AS support_action_id,
       e.detail::text AS detail, e.prev_hash, e.hash
  FROM pandokos.audit_event e
 WHERE e.seq > sqlc.arg(after_seq)::bigint AND e.seq <= sqlc.arg(to_seq)::bigint
 ORDER BY e.seq
 LIMIT sqlc.arg(page_size)::integer;

-- name: GetAuditHead :one
SELECT c.seq, c.hash, c.anchor_seq, c.anchor_hash FROM pandokos.audit_chain c
 WHERE c.tenant_id = pandokos.current_tenant_id();

-- name: ListPlatformAuditEvents :many
SELECT e.seq, e.id, e.occurred_at, e.actor_type, e.actor_id, e.actor_name, e.key_id, e.action, e.resource_type,
       e.resource_id, e.outcome, e.request_id, e.reason, e.detail::text AS detail, e.prev_hash, e.hash
  FROM pandokos.platform_audit_event e
 WHERE e.seq < sqlc.arg(before_seq)::bigint
   AND (sqlc.narg(from_at)::timestamptz IS NULL OR e.occurred_at >= sqlc.narg(from_at)::timestamptz)
   AND (sqlc.narg(to_at)::timestamptz IS NULL OR e.occurred_at < sqlc.narg(to_at)::timestamptz)
   AND (sqlc.arg(action)::text = '' OR e.action = sqlc.arg(action)::text
        OR (right(sqlc.arg(action)::text, 1) = '.' AND starts_with(e.action, sqlc.arg(action)::text)))
   AND (sqlc.arg(resource_type)::text = '' OR e.resource_type = sqlc.arg(resource_type)::text)
   AND (sqlc.arg(resource_id)::text = '' OR e.resource_id = sqlc.arg(resource_id)::text)
   AND (sqlc.arg(actor_type)::text = '' OR e.actor_type = sqlc.arg(actor_type)::text)
   AND (sqlc.arg(actor_id)::text = '' OR e.actor_id = sqlc.arg(actor_id)::text)
   AND (sqlc.arg(outcome)::text = '' OR e.outcome = sqlc.arg(outcome)::text)
   AND (sqlc.arg(request_id)::text = '' OR e.request_id = sqlc.arg(request_id)::text)
 ORDER BY e.seq DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: ExportPlatformAuditEvents :many
SELECT e.seq, e.id, e.occurred_at, e.actor_type, e.actor_id, e.actor_name, e.key_id, e.action, e.resource_type,
       e.resource_id, e.outcome, e.request_id, e.reason, e.detail::text AS detail, e.prev_hash, e.hash
  FROM pandokos.platform_audit_event e
 WHERE e.seq > sqlc.arg(after_seq)::bigint AND e.seq <= sqlc.arg(to_seq)::bigint
 ORDER BY e.seq
 LIMIT sqlc.arg(page_size)::integer;

-- name: GetPlatformAuditHead :one
SELECT c.seq, c.hash, c.anchor_seq, c.anchor_hash FROM pandokos.platform_audit_chain c WHERE c.id = 1;
```

## Appendix A2c — `internal/store/queries/sql/limits.sql`, the new `AggregateUsageForLimits`

The comment above the statement becomes: "Worker pool (counters and capability check), session of the
tenant: the exact reader (ADR 0064). What the ledger holds for the requests that started in
[from_time, to_time), by the dimensions a rule can name, with the result of
AggregateUsageForLimitsFromLedger below: whole hours of [from_time, min(live_from, to_time)) from the
rollup and the records of those hours recorded since the watermark; the rest from the ledger."

```sql
-- name: AggregateUsageForLimits :many
WITH p AS (
    SELECT sqlc.arg(from_time)::timestamptz AS f, sqlc.arg(live_from)::timestamptz AS l, sqlc.arg(to_time)::timestamptz AS t,
           COALESCE((SELECT s.watermark FROM pandokos.usage_rollup_state s), '-infinity'::timestamptz) AS wm
), w AS (
    SELECT p.*, CASE WHEN b.hi > b.lo THEN b.lo ELSE p.f END AS lo, CASE WHEN b.hi > b.lo THEN b.hi ELSE p.f END AS hi
      FROM p, LATERAL (
        SELECT CASE WHEN date_bin('1 hour', p.f, '2000-01-01 00:00:00+00') = p.f THEN p.f
                    ELSE date_bin('1 hour', p.f, '2000-01-01 00:00:00+00') + interval '1 hour' END AS lo,
               date_bin('1 hour', LEAST(p.l, p.t), '2000-01-01 00:00:00+00') AS hi) b
), parts AS (
    SELECT 0::bigint AS minute, r.principal_id, r.key_id, r.provider, r.model_id, r.currency, r.requests,
           (r.input_tokens + r.estimated_input_tokens + r.output_tokens + r.estimated_output_tokens)::numeric AS tokens,
           r.cost
      FROM pandokos.usage_rollup r, w
     WHERE r.credential AND r.hour >= w.lo AND r.hour < w.hi
    UNION ALL
    SELECT CASE WHEN u.started_at >= w.l THEN floor(extract(epoch FROM u.started_at) / 60)::bigint ELSE 0 END,
           u.principal_id, u.key_id, u.provider, COALESCE(u.model_id::text, ''), COALESCE(u.currency, ''), 1,
           (COALESCE(u.input_tokens, u.estimated_input_tokens, 0) + COALESCE(u.output_tokens, u.estimated_output_tokens, 0))::numeric,
           COALESCE(u.cost, 0)
      FROM pandokos.usage_record u, w
     WHERE u.credential_id IS NOT NULL
       AND ((u.started_at >= w.f AND u.started_at < w.lo)
            OR (u.started_at >= w.hi AND u.started_at < w.t)
            OR (u.recorded_at >= w.wm AND u.started_at >= w.lo AND u.started_at < w.hi))
)
SELECT p.minute::bigint AS minute, p.principal_id, p.key_id, COALESCE(pr.id::text, '')::text AS provider_id, p.model_id,
       p.currency, sum(p.requests)::bigint AS requests, sum(p.tokens)::numeric AS tokens, sum(p.cost)::numeric AS cost
  FROM parts p
  LEFT JOIN pandokos.provider pr ON pr.name = p.provider
 GROUP BY p.minute, p.principal_id, p.key_id, pr.id, p.model_id, p.currency;
```

## Appendix A3a — paths of `api/openapi/admin.v1.yaml`

A line `# errors: <codes>` stands for one line per code, `"<code>": {$ref: '#/components/responses/<Name>'}`,
with the file's names: 400 `InvalidRequest`, 401 `Unauthenticated`, 403 `Forbidden`, 404 `NotFound`,
409 `Conflict`, 413 `RequestTooLarge`, 415 `UnsupportedMediaType`, 500 `Internal`, 503 `Unavailable`.

```yaml
  /v1/tenants/{tenant}/usage-report:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: getUsageReport
      tags: [usage]
      summary: Report the tenant's usage by period, provider, model, caller and key
      description: >-
        Sums over the records whose started_at is in [from, to), by the tenant's day or month or over the whole
        window, and by the dimensions of group_by (model when absent). Rows are always split by currency and price
        source: costs are never converted, nor added across currencies or price sources. Requests refused for a
        limit are counted apart. from and to are dates (the start of that day in the tenant's time zone) or RFC 3339
        instants; from defaults to the start of the current month of the tenant and to to now. The window is at most
        400 days and the report at most 10000 rows.
      x-pandokos-permission: usage:read
      parameters:
        - {name: from, in: query, description: 'Start: YYYY-MM-DD or an RFC 3339 instant.', schema: {type: string, minLength: 10, maxLength: 64}}
        - {name: to, in: query, description: 'End (excluded): YYYY-MM-DD or an RFC 3339 instant.', schema: {type: string, minLength: 10, maxLength: 64}}
        - {name: period, in: query, description: The periods of the report., schema: {$ref: '#/components/schemas/ReportPeriod'}}
        - {name: group_by, in: query, description: 'Comma-separated subset of provider, model, caller, key.', schema: {type: string, pattern: '^(provider|model|caller|key)(,(provider|model|caller|key)){0,3}$'}}
        - {name: provider, in: query, description: Only records of this provider., schema: {$ref: '#/components/schemas/ProviderName'}}
        - {name: model, in: query, description: Only records of this model name., schema: {$ref: '#/components/schemas/ModelName'}}
      responses:
        "200": {$ref: '#/components/responses/UsageReport'}
        # errors: 400 401 403 404 500 503
  /v1/tenants/{tenant}/usage-records/export:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: exportUsageRecords
      tags: [usage]
      summary: Export the tenant's usage records, oldest first, as NDJSON or CSV
      description: >-
        Records whose started_at is in [from, to), as listUsageRecords selects them (at most 31 days), oldest first,
        streamed. A failure after the first byte ends the transfer without its terminator: a client that does not
        receive the whole body has an incomplete export. In CSV a text cell that starts with =, +, -, @, a tab or a
        carriage return is prefixed with a single quote.
      x-pandokos-permission: usage:read
      parameters:
        - {name: from, in: query, description: 'Start of the window, RFC 3339.', schema: {type: string, maxLength: 64}}
        - {name: to, in: query, description: 'End of the window (excluded), RFC 3339.', schema: {type: string, maxLength: 64}}
        - {name: provider, in: query, description: Only records of this provider., schema: {$ref: '#/components/schemas/ProviderName'}}
        - {name: format, in: query, description: The format of the body., schema: {$ref: '#/components/schemas/ExportFormat'}}
      responses:
        "200": {$ref: '#/components/responses/UsageRecordExport'}
        # errors: 400 401 403 404 500 503
  /v1/tenants/{tenant}/limits/{rule}/usage:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
      - {$ref: '#/components/parameters/LimitRule'}
    get:
      operationId: getLimitUsage
      tags: [limits]
      summary: Read how much of a daily or monthly limit rule is used in its current period
      description: >-
        What the usage ledger holds for the rule in its period at now, in the tenant's time zone, counted as the
        counters count it. Requests in flight are not included. A rule of a per-minute metric is invalid_state.
      x-pandokos-permission: usage:read
      responses:
        "200": {$ref: '#/components/responses/LimitUsage'}
        # errors: 401 403 404 409 500 503
  /v1/tenants/{tenant}/metadata-keys:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: getMetadataKeys
      tags: [usage]
      summary: Read the caller metadata keys the tenant accepts
      x-pandokos-permission: usage:read
      responses:
        "200": {$ref: '#/components/responses/MetadataKeys'}
        # errors: 401 403 404 500 503
    put:
      operationId: setMetadataKeys
      tags: [usage]
      summary: Accept only the caller metadata keys named
      description: >-
        From then on a metadata header whose key is not listed is dropped and counted in X-Pandokos-Metadata-Dropped.
        An empty list accepts no key.
      x-pandokos-permission: tenant_setting:write
      requestBody: {required: true, content: {application/json: {schema: {$ref: '#/components/schemas/MetadataKeysSet'}}}}
      responses:
        "200": {$ref: '#/components/responses/MetadataKeys'}
        # errors: 400 401 403 404 413 415 500 503
    delete:
      operationId: clearMetadataKeys
      tags: [usage]
      summary: Accept every caller metadata key again
      x-pandokos-permission: tenant_setting:write
      responses:
        "200": {$ref: '#/components/responses/MetadataKeys'}
        # errors: 401 403 404 500 503
  /v1/tenants/{tenant}/audit-events:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: listAuditEvents
      tags: [audit]
      summary: List the events of the tenant's audit log, newest first
      description: >-
        A cursor is valid only with the same filters. The detail of an event that records a price or the active
        currency is withheld from a caller without price_list:read; its digest is given instead.
      x-pandokos-permission: audit:read
      parameters:
        - {$ref: '#/components/parameters/Limit'}
        - {$ref: '#/components/parameters/Cursor'}
        - {$ref: '#/components/parameters/AuditFrom'}
        - {$ref: '#/components/parameters/AuditTo'}
        - {$ref: '#/components/parameters/AuditAction'}
        - {$ref: '#/components/parameters/AuditResourceType'}
        - {$ref: '#/components/parameters/AuditResourceID'}
        - {$ref: '#/components/parameters/AuditActorType'}
        - {$ref: '#/components/parameters/AuditActorID'}
        - {$ref: '#/components/parameters/AuditOutcome'}
        - {$ref: '#/components/parameters/AuditRequestID'}
        - {name: support_action_id, in: query, description: Only events written under this support action., schema: {$ref: '#/components/schemas/ID'}}
      responses:
        "200": {$ref: '#/components/responses/AuditEventList'}
        # errors: 400 401 403 404 500 503
  /v1/tenants/{tenant}/audit-events/export:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: exportAuditEvents
      tags: [audit]
      summary: Export the tenant's audit chain, oldest first, as NDJSON
      description: >-
        The events from from_seq (the anchor of the chain by default) to to_seq (the head read when the export
        starts, by default), one per line. pandokos audit verify checks such an export.
      x-pandokos-permission: audit:read
      parameters:
        - {$ref: '#/components/parameters/AuditFromSeq'}
        - {$ref: '#/components/parameters/AuditToSeq'}
      responses:
        "200": {$ref: '#/components/responses/AuditEventExport'}
        # errors: 400 401 403 404 500 503
  /v1/tenants/{tenant}/audit-head:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: getAuditHead
      tags: [audit]
      summary: Read the head and the anchor of the tenant's audit chain
      x-pandokos-permission: audit:read
      responses:
        "200": {$ref: '#/components/responses/AuditHead'}
        # errors: 401 403 404 500 503
  /v1/platform-audit-events:
    get:
      operationId: listPlatformAuditEvents
      tags: [audit]
      summary: List the events of the platform's audit log, newest first
      x-pandokos-permission: platform_audit:read
      parameters:
        - {$ref: '#/components/parameters/Limit'}
        - {$ref: '#/components/parameters/Cursor'}
        - {$ref: '#/components/parameters/AuditFrom'}
        - {$ref: '#/components/parameters/AuditTo'}
        - {$ref: '#/components/parameters/AuditAction'}
        - {$ref: '#/components/parameters/AuditResourceType'}
        - {$ref: '#/components/parameters/AuditResourceID'}
        - {$ref: '#/components/parameters/AuditActorType'}
        - {$ref: '#/components/parameters/AuditActorID'}
        - {$ref: '#/components/parameters/AuditOutcome'}
        - {$ref: '#/components/parameters/AuditRequestID'}
      responses:
        "200": {$ref: '#/components/responses/AuditEventList'}
        # errors: 400 401 403 500 503
  /v1/platform-audit-events/export:
    get:
      operationId: exportPlatformAuditEvents
      tags: [audit]
      summary: Export the platform's audit chain, oldest first, as NDJSON
      x-pandokos-permission: platform_audit:read
      parameters:
        - {$ref: '#/components/parameters/AuditFromSeq'}
        - {$ref: '#/components/parameters/AuditToSeq'}
      responses:
        "200": {$ref: '#/components/responses/AuditEventExport'}
        # errors: 400 401 403 500 503
  /v1/platform-audit-head:
    get:
      operationId: getPlatformAuditHead
      tags: [audit]
      summary: Read the head and the anchor of the platform's audit chain
      x-pandokos-permission: platform_audit:read
      responses:
        "200": {$ref: '#/components/responses/AuditHead'}
        # errors: 401 403 500 503
```

## Appendix A3b — components of `api/openapi/admin.v1.yaml`

```yaml
# components.parameters
    AuditFrom: {name: from, in: query, description: 'Only events at or after this RFC 3339 instant.', schema: {type: string, minLength: 1, maxLength: 64}}
    AuditTo: {name: to, in: query, description: 'Only events before this RFC 3339 instant.', schema: {type: string, minLength: 1, maxLength: 64}}
    AuditAction: {name: action, in: query, description: 'Only this action, or the actions that start with it when it ends with a dot (member.).', schema: {type: string, pattern: '^[a-z_]+\.([a-z_]+)?$'}}
    AuditResourceType: {name: resource_type, in: query, description: Only events on this type of resource., schema: {type: string, pattern: '^[a-z_]{1,64}$'}}
    AuditResourceID: {name: resource_id, in: query, description: Only events on the resource with this ID., schema: {type: string, minLength: 1, maxLength: 128}}
    AuditActorType: {name: actor_type, in: query, description: Only events of this type of actor., schema: {$ref: '#/components/schemas/AuditActorType'}}
    AuditActorID: {name: actor_id, in: query, description: Only events of the actor with this ID., schema: {type: string, pattern: '^[A-Za-z0-9_-]{1,64}$'}}
    AuditOutcome: {name: outcome, in: query, description: Only events with this outcome., schema: {$ref: '#/components/schemas/AuditOutcome'}}
    AuditRequestID: {name: request_id, in: query, description: Only events of this request., schema: {type: string, pattern: '^[0-9a-f]{32}$'}}
    AuditFromSeq: {name: from_seq, in: query, description: First sequence number; the anchor of the chain by default., schema: {type: integer, minimum: 1}}
    AuditToSeq: {name: to_seq, in: query, description: Last sequence number; the head when the export starts by default., schema: {type: integer, minimum: 1}}

# components.responses
    UsageReport:
      description: The report.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/UsageReport'}
    UsageRecordExport:
      description: The records, oldest first, one JSON object per line, or as CSV with a header line.
      content:
        application/x-ndjson:
          schema: {$ref: '#/components/schemas/UsageRecord'}
        text/csv:
          schema: {type: string}
    LimitUsage:
      description: How much of the rule is used in its current period.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/LimitUsage'}
    MetadataKeys:
      description: The metadata keys the tenant accepts.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/MetadataKeys'}
    AuditEventList:
      description: One page of audit events.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/AuditEventList'}
    AuditEventExport:
      description: The events of the chain, oldest first, one JSON object per line.
      content:
        application/x-ndjson:
          schema: {$ref: '#/components/schemas/AuditEvent'}
    AuditHead:
      description: The head and the anchor of the chain.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/AuditHead'}

# components.schemas
    ReportPeriod: {type: string, enum: [day, month, total], default: month}
    ExportFormat: {type: string, enum: [ndjson, csv], default: ndjson}
    AuditActorType: {type: string, enum: [platform, service_account, user, anonymous, host]}
    AuditOutcome: {type: string, enum: [success, failure]}
    UsageReport:
      type: object
      additionalProperties: false
      required: [from, to, period, time_zone, group_by, rows]
      properties:
        from: {type: string, format: date-time}
        to: {type: string, format: date-time}
        period: {$ref: '#/components/schemas/ReportPeriod'}
        time_zone: {type: string, description: The time zone in force at to; periods follow the zone in force at each instant.}
        group_by:
          type: array
          items: {type: string}
        rows:
          type: array
          items: {$ref: '#/components/schemas/UsageReportRow'}
    UsageReportRow:
      type: object
      additionalProperties: false
      required: [period_start, period_end, provider, model, model_id, principal_kind, principal_id, key_id, currency,
                 price_source, requests, requests_ok, rejected_limit, input_tokens, output_tokens, cache_read_tokens,
                 cache_write_tokens, reasoning_tokens, estimated_input_tokens, estimated_output_tokens, cost,
                 cost_unknown]
      properties:
        period_start: {type: string, format: date-time}
        period_end: {type: string, format: date-time}
        provider: {type: string, description: Empty unless grouped by provider.}
        model: {type: string, description: Empty unless grouped by model.}
        model_id: {type: string, description: Empty unless grouped by model, and for records without a model ID.}
        principal_kind: {type: string, description: Empty unless grouped by caller.}
        principal_id: {type: string, description: Empty unless grouped by caller.}
        key_id: {type: string, description: Empty unless grouped by key.}
        currency: {type: string, description: Empty for records without a cost.}
        price_source: {type: string, description: 'catalogue, tenant, or empty for records without a price version.'}
        requests: {type: integer, description: Records of the row, those refused for a limit excepted.}
        requests_ok: {type: integer}
        rejected_limit: {type: integer, description: Requests refused for a limit, counted apart.}
        input_tokens: {type: integer, description: Reported by the provider.}
        output_tokens: {type: integer, description: Reported by the provider.}
        cache_read_tokens: {type: integer}
        cache_write_tokens: {type: integer}
        reasoning_tokens: {type: integer}
        estimated_input_tokens: {type: integer, description: Estimated by Pandokos.}
        estimated_output_tokens: {type: integer, description: Estimated by Pandokos.}
        cost: {type: [string, "null"], description: 'Decimal amount in currency, the sum of the known costs; null when none is known. Informational under BYOK.'}
        cost_unknown: {type: integer, description: Records whose cost could not be computed.}
    LimitUsage:
      type: object
      additionalProperties: false
      required: [rule_id, metric, currency, period_start, period_end, as_of, used, used_amount]
      properties:
        rule_id: {$ref: '#/components/schemas/ID'}
        metric: {type: string}
        currency: {type: string, description: The currency of a cost rule; empty otherwise.}
        period_start: {type: string, format: date-time}
        period_end: {type: string, format: date-time}
        as_of: {type: string, format: date-time}
        used: {type: integer, description: Requests or tokens; 0 for a cost rule.}
        used_amount: {type: string, description: The amount in currency for a cost rule; empty otherwise.}
    MetadataKey: {type: string, pattern: '^[a-z0-9][a-z0-9_-]{0,31}$'}
    MetadataKeys:
      type: object
      additionalProperties: false
      required: [restricted, keys]
      properties:
        restricted: {type: boolean, description: False when every key with the format is accepted.}
        keys:
          type: array
          items: {type: string}
        updated_at: {type: string, format: date-time, description: Absent when not restricted.}
    MetadataKeysSet:
      type: object
      additionalProperties: false
      required: [keys]
      properties:
        keys:
          type: array
          maxItems: 64
          items: {$ref: '#/components/schemas/MetadataKey'}
    RawObject:
      type: object
      x-pandokos-raw-json: true
      description: A JSON object passed through as the database holds it.
    AuditEvent:
      type: object
      additionalProperties: false
      required: [seq, id, occurred_at, actor_type, actor_id, actor_name, key_id, action, resource_type, resource_id,
                 outcome, request_id, reason, support_action_id, detail, detail_withheld, detail_digest, prev_hash, hash]
      properties:
        seq: {type: integer}
        id: {$ref: '#/components/schemas/ID'}
        occurred_at: {type: string, format: date-time, description: 'UTC, six fraction digits.'}
        actor_type: {type: string}
        actor_id: {type: string}
        actor_name: {type: string}
        key_id: {type: string}
        action: {type: string}
        resource_type: {type: string}
        resource_id: {type: string}
        outcome: {type: string}
        request_id: {type: string}
        reason: {type: string}
        support_action_id: {type: string, description: Empty when the event was not written under a support action; always empty in the platform log.}
        detail: {$ref: '#/components/schemas/RawObject'}
        detail_withheld: {type: boolean, description: 'True when detail is withheld from the caller and replaced by an empty object.'}
        detail_digest: {type: string, description: 'Lower-case hex SHA-256 of the canonical encoding of the detail; set when detail_withheld, empty otherwise.'}
        prev_hash: {type: string, description: Lower-case hex.}
        hash: {type: string, description: Lower-case hex.}
    AuditEventList:
      type: object
      additionalProperties: false
      required: [items]
      properties:
        items:
          type: array
          items: {$ref: '#/components/schemas/AuditEvent'}
        next_cursor: {type: string, description: Absent on the last page.}
    AuditHead:
      type: object
      additionalProperties: false
      required: [seq, hash, anchor_seq, anchor_hash]
      properties:
        seq: {type: integer, description: The last sequence number; 0 for an empty chain.}
        hash: {type: string, description: Lower-case hex of the last row's hash.}
        anchor_seq: {type: integer, description: The first row kept; 1 until retention removes rows.}
        anchor_hash: {type: string, description: Lower-case hex of the hash of the row before the anchor; zeros at the start.}
```
