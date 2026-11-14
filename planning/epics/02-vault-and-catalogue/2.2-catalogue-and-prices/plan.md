# Sprint 2.2 — Implementation plan

- Design: [sprint.md](sprint.md). Section numbers (§) refer to it.
- Each part below is self-contained: an agent needs sprint.md and that part only. Parts run in
  order A, B, C1 … C11, D, E, F.

## Rules for the implementer

1. Branch `feat/2.2-catalogue` from `main`; sign off every commit. One commit for A, one for B
   (tests, failing), one per step of C (message given with the step), one for D, one for E.
2. **No Go dependency is added.** `git diff main -- go.mod go.sum` prints nothing at the end.
   Decimal arithmetic is `math/big`; regular expressions are `regexp`.
3. **No floating point for money.** `internal/pricing` and `internal/pricing/genaiprices` never
   name `float32` or `float64` outside tests (T-R23); JSON numbers of the dataset are decoded with
   `json.Decoder.UseNumber`; rates cross SQL and the API as decimal text.
4. Appendix A (migration), B (OpenAPI additions) and C (query files and `sqlc.yaml`) are copied
   exactly. They were applied to PostgreSQL 16, generated with `sqlc` v1.31.1, and appendix B was
   run through `internal/apigen` (it passes except for the nested `rates`, which step C6 makes
   possible). If one of them does not work as written, stop and ask.
5. The embedded snapshot is the upstream file byte for byte; never edit, reformat or trim it.
   Its SHA-256 is written in `SOURCE` by the command of step A6, not typed by hand.
6. Generated files are never edited by hand: change the source, run `make generate`.
7. SQL runs only as `queries.New(tx).<Method>` inside a `store` helper (T-R10, T-R12).
8. A handler that changes data writes its audit row in the same transaction. A remote download
   happens **before** the import transaction is opened, never inside it.
9. Catalogue writes run in `db.Platform`. Do not "fix" a row-level security error by adding a
   policy or a grant: the matrix of B2 is the design.
10. Tests hold for root and for a non-root user (CI runs as non-root; local runs are often root):
    no assertion depends on file permissions, on a file being unreadable, or on the user's home.
11. Test values that look like keys start with `synthetic-` (gitleaks allow-list). A bound is
    never made to pass by lengthening it, a database test never by skipping it.
12. English everywhere; every exported identifier has a doc comment.

## Environment

- `make db-up` starts PostgreSQL 16 with TLS and PgBouncer in transaction mode (Docker) and
  writes `local/testdb/env`; `make db-down` discards them.
- `make test` (root module, race detector, needs the database), `make test-sdk`, `make image`
  then `make test-container`.
- `make generate` (apigen and `sqlc`; `sqlc` needs the release toolchain of `.go-version`),
  `make generate-check`, `make migrations-sum`.
- `make lint`, `make vuln`, `make secrets`, and `make check` for everything a pull request must
  pass except the image.
- Before starting: `make db-up && make check` must pass on `main`.

---

## Part A — Scaffolding (commit: `chore: catalogue scaffolding, snapshot, generation inputs`)

**Files:** `internal/store/migrations/sql/0004_catalogue.sql`, `migrations.sum`, `sqlc.yaml`,
`internal/store/queries/sql/{catalog,prices,virtual_keys}.sql`, `api/openapi/admin.v1.yaml`,
`internal/pricing/genaiprices/snapshot/*`, `test/fixtures/prices/*`, `.golangci.yml`, package
`doc.go` files, `internal/admin/catalog_todo.go`.

1. **Migration.** Create `0004_catalogue.sql` from appendix A, byte for byte; `make
   migrations-sum` adds one line and changes no other.
2. **sqlc.** Add the two `numeric` overrides of appendix C to `sqlc.yaml`. Create `catalog.sql`
   and `prices.sql`; apply the changes to `virtual_keys.sql`.
3. **OpenAPI.** Insert appendix B at the places it names.
4. **Generator, minimal.** `internal/apigen` refuses the nested `rates` of `PriceOverride`
   ("request bodies are flat"). Do the generator part of step C6 now (it is a prerequisite of
   `make generate`), with its tests T-384 written first in this commit; the rest of C6 (the
   handlers that use `Optional`) stays in C6.
5. `make generate`; commit the generated files. Make `go build ./...` pass: add
   `internal/admin/catalog_todo.go` with the 29 new `adminapi.Server` methods returning
   `errors.New("not implemented")`, pass `Models: nil, ModelsRestricted: false` where
   `CreateVirtualKeyParams` gained members, and adapt the three update handlers and the CLI to
   `Optional` (mechanical: `b.X != ""` becomes `b.X.Set`). `catalog_todo.go` is deleted in C8.
6. **Snapshot.** In `internal/pricing/genaiprices/snapshot/`:

   ```sh
   C=$(git ls-remote https://github.com/pydantic/genai-prices HEAD | cut -f1)   # the design saw 5acfa1123141a4216ef77dc6b1c8a6aa948f9e81
   curl -fsS -o data.json "https://raw.githubusercontent.com/pydantic/genai-prices/$C/prices/new_data/v2/data.json"
   curl -fsS -o LICENSE   "https://raw.githubusercontent.com/pydantic/genai-prices/$C/LICENSE"
   head -1 LICENSE    # must read: The MIT License (MIT)
   printf 'source: https://raw.githubusercontent.com/pydantic/genai-prices/%s/prices/new_data/v2/data.json\ncommit: %s\nretrieved: %s\nsha256: %s\nlicense: MIT (LICENSE in this directory)\n' \
     "$C" "$C" "$(date -u +%Y-%m-%d)" "$(sha256sum data.json | cut -d' ' -f1)" > SOURCE
   ```

   Use the commit the design saw unless the maintainer asks for a newer one; with it the
   SHA-256 is `1aa26e77c74c171fe66e4215ecd5ecc22a378e4568cb4522984b1c048c0b3970`. Then run
   `make secrets` and `go test ./test/repo/ -run 'ForbiddenStrings|KeyMaterial'`: both must pass
   with the file present (the design checked it with gitleaks v8.30.1). Add the line
   `internal/pricing/genaiprices/snapshot/data.json -diff linguist-generated` to
   `.gitattributes` (create it if absent) so reviews are not flooded.
7. **Fixtures** `test/fixtures/prices/`: `base.json` (providers `openai` with models matching
   `gpt-4o` and `o4-mini`, and `synthetic` with `synthetic-chat` and `synthetic-tiered` using
   `{base, tiers}`); `changed.json` (equal to `base.json` except one rate of `gpt-4o`);
   `shapes.json` (one model per row of the extraction table of §5.6, including a regex with
   `(?<=x)`); `invalid.json` (`{"not": "an array"}`). All values are invented; every model
   identifier not shared with the seed begins with `synthetic-`.
8. **Linter.** `depguard` rules: `internal/pricing/**` denies `internal/store`, `net`, `net/http`,
   `os` (test files excepted); `internal/catalog/**` denies `internal/admin`, `internal/adminapi`,
   `internal/gateway`, `internal/server`, `internal/cli`, `internal/config`, `internal/vault`.
9. `doc.go` of `internal/pricing`, `internal/pricing/genaiprices`, `internal/catalog`: package
   comments naming ADR 0011, 0035, 0036.

**Verify:** `go build ./... && make generate-check && make lint`; `make test` passes except the
tests that enumerate operations, permissions, the lint spec or the configuration keys.

---

## Part B — Tests first (commit: `test: sprint 2.2 tests, failing`)

Write every test of §9 before any file of part C (T-384 already exists from A4). Then run
`go vet ./... ; make test` and keep the output for the pull request.

General rules: tests over "every operation" range over `adminapi.Operations` and fail on an
operation they do not know; time-dependent database tests create versions in sequence with real
time and assert relative order, never wall-clock values; nothing sleeps longer than 2 s.

### B1. Helpers

- `test/apitest`: `Params` gains `Provider` (name), `Endpoint` (name), `Model`, `PriceVersion`,
  `PriceImport`; `CallFor` knows the 29 operations (bodies: `createProvider`
  `{"name": UniqueName("synthetic"), "api_family": "openai"}`; `createEndpoint` `{"name":
  UniqueName("ep"), "base_url": "https://synthetic.invalid"}`; `createModel` `{"provider":
  p.Provider, "name": UniqueName("synthetic-model")}`; update bodies `{"description": "changed by a
  test"}`; `createPriceOverride` `{"currency": "USD", "rates": [{"category": "input",
  "threshold": 0, "rate": "1"}]}`; `runPriceImport` `{"source": "embedded", "dry_run": true}`;
  `setModelRestriction` `{"models": []}`; `clearPriceOverride` `{}`). `API.Catalog(tb)` creates a
  provider, an endpoint and two models (one `selected`) on the platform key and returns their
  identifiers. `apitest.New` sets `admin.Options.Snapshot` to `genaiprices.Snapshot()`.
- `test/isolation/api_test.go`: `platformAndSelf` gains the 25 platform operations; the
  second-parameter check covers `{model}`, `{price_version}`, `{price_import}`; the body-member
  check covers `models` of `createVirtualKey` and of `setModelRestriction` (a model granted only
  to B: 400, identical to an unknown reference). `fresh` creates a model of its own for the
  operations that delete or change one.
- `test/testutil`: `PriceServer(tb, body []byte, behaviour string) (url string)` on
  `httptest.NewUnstartedServer` with a certificate of `test/testcert`; `SSLCertFile(tb) string`
  returns a PEM file of the test CA for `SSL_CERT_FILE`.
- `test/dbtest`: `Env` sets `PANDOKOS_PRICING_IMPORT_AT_START=false` unless a test asks
  otherwise (`d.EnvWith(map)`), so existing audit-counting tests are not disturbed.

### B2. Lint spec (`test/isolation/spec.go` and tests)

- `Global` (all `Restricted: true`):

  ```go
  "provider":           {Reason: "providers of the catalogue; read by every runtime role, written in platform scope", Restricted: true},
  "provider_endpoint":  {Reason: "endpoints of providers; read by the gateway and the admin role, written in platform scope", Restricted: true},
  "model":              {Reason: "models of the catalogue; read by every runtime role, written in platform scope", Restricted: true},
  "model_tenant_grant": {Reason: "which tenants a model is granted to; a tenant's session sees its own rows; protected by its own policies", Restricted: true},
  "price_import":       {Reason: "runs of the price importer; platform scope only", Restricted: true},
  "price_version":      {Reason: "price versions; read by every runtime role, inserted in platform scope", Restricted: true},
  "price_rate":         {Reason: "rates of price versions; read by every runtime role, inserted in platform scope", Restricted: true},
  ```

- `AppendOnly`: add `"price_import"`.
- `Grants` (as read back from the database while designing):

  ```
  provider:
    pandokos_gateway: SELECT
    pandokos_admin:   DELETE, INSERT(api_family,dataset_provider,description,display_name,enabled,name), SELECT, UPDATE(dataset_provider,description,display_name,enabled)
    pandokos_worker:  SELECT
  provider_endpoint:
    pandokos_gateway: SELECT
    pandokos_admin:   DELETE, INSERT(base_url,description,enabled,is_default,name,private,provider_id,region), SELECT, UPDATE(base_url,description,enabled,is_default,private,region)
  model:
    pandokos_gateway: SELECT
    pandokos_admin:   DELETE, INSERT(availability,capabilities,context_window,description,display_name,max_output_tokens,name,provider_id,status,upstream_id), SELECT, UPDATE(availability,capabilities,context_window,description,display_name,max_output_tokens,price_override,status,upstream_id)
    pandokos_worker:  SELECT
  model_tenant_grant:
    pandokos_gateway: SELECT
    pandokos_admin:   DELETE, INSERT(model_id,tenant_id), SELECT
  tenant_model_restriction:
    pandokos_gateway: SELECT
    pandokos_admin:   DELETE, INSERT(models,tenant_id), SELECT, UPDATE(models)
  price_import:
    pandokos_admin:   INSERT(actor_id,actor_type,dataset_format,digest,error_class,models_seen,outcome,overridden,report,skipped,source,source_ref,started_at,unchanged,unmatched,versions_created), SELECT
    pandokos_worker:  (the same as pandokos_admin)
  price_version:
    pandokos_gateway: SELECT
    pandokos_admin:   DELETE, INSERT(currency,import_id,model_id,note,source,valid_from), SELECT
    pandokos_worker:  INSERT(currency,import_id,model_id,note,source,valid_from), SELECT
  price_rate:
    pandokos_gateway: SELECT
    pandokos_admin:   INSERT(category,price_version_id,rate,threshold), SELECT
    pandokos_worker:  INSERT(category,price_version_id,rate,threshold), SELECT
  platform_audit_event (changed):
    pandokos_worker:  INSERT(action,actor_id,actor_name,actor_type,detail,key_id,outcome,reason,request_id,resource_id,resource_type)
  virtual_key (changed):
    pandokos_admin:   INSERT(credential_id,description,expires_at,key_id,models,scopes,secret_hash,service_account_id,tenant_id), SELECT, UPDATE(revoked_at)
  ```

- `Fixtures`:

  ```go
  "tenant_model_restriction": {
      Insert: `INSERT INTO pandokos.tenant_model_restriction (tenant_id, models) VALUES ($1::uuid, ARRAY['openai/gpt-4o']) RETURNING id::text`,
      Update: `models = ARRAY['openai/gpt-4o-mini']`,
  },
  ```

  The fixture inserts one row per tenant; if T-143 inserts twice for a tenant, give the fixture
  `ON CONFLICT (tenant_id) DO UPDATE SET models = EXCLUDED.models` and keep `RETURNING id::text`.

### B3. Unit tests

T-370 to T-386 as §9 describes. Notes:

- T-371's table lists `family.Usage` inputs and the expected `Cost` in decimal text; write the
  expected amounts by hand from §5.4, never by calling the code under test.
- T-377 must not assert prices of real models (they change with the snapshot); it asserts that
  extraction succeeds with an `input` rate and that the context windows of the migration seed
  equal the snapshot's `context_window` (a mismatch after a snapshot update is a reminder, not a
  bug: the test message says to update neither file silently but to raise it).
- T-380 drives `Cache` through an unexported `loader` interface (`snapshot(ctx)`,
  `access(ctx, tenant.ID)`) with a fake; the exported constructor wires the database.
- T-386 sets `HTTPS_PROXY` and `HTTP_PROXY` with `t.Setenv` to a listener that fails the test when
  contacted; it trusts the test CA through `Fetch`'s unexported `roots` option, not
  `SSL_CERT_FILE` (that is for e2e).

### B4. Database and API tests

T-390 to T-398 (`test/isolation`, `internal/catalog`, `internal/store/queries`), T-400 to
T-407 (`internal/admin`). Notes:

- T-391 is table-driven over (table, role, session, statement) and asserts SQLSTATE `42501`
  (privilege or row-level security) or the row count; the expectations are the matrix of §5.1 and
  §12 written in the test, not derived from the catalogue.
- T-394's concurrency case runs two `Import` calls on two goroutines with the same data and
  asserts exactly one set of versions; the lock is `LockPriceImports`.
- T-395 creates v1, v2, v3 by `createPriceOverride` through `catalog` helpers (no trigger is
  disabled, no clock is faked in the database), records their `valid_from`, and evaluates
  `PriceAt` at `v1.ValidFrom − 1µs`, `v1.ValidFrom`, midpoints, each later boundary and `now`.
- T-397 follows the 1.3 and 2.1 cache tests (T-262 to T-266, T-326, T-327).
- T-404's remote cases construct `admin.Options.Fetch` with the test server's CA.

### B5. End-to-end, container, repository

T-410, T-411 (`test/e2e`), T-412 (`test/container`), T-R23, T-R24 (`test/repo`). T-411 sets
`SSL_CERT_FILE` for the child process and `PANDOKOS_TEST_REFRESH_DELAY=1s`; `serve` reads that
variable only when it is set and only to shorten the first delay (document it in the code as a
test hook; it is not a configuration key and is not in the reference).

**Verify (part B):** `go vet ./...` reports only missing symbols of part C; the saved `make test`
output shows the new tests failing or not compiling.

---

## Part C — Implementation

After each step: `go build ./... && go vet ./...`, and the tests named with the step pass.

### C1. `internal/pricing` — `feat: decimal arithmetic, price versions, cost`

**Files:** `decimal.go`, `price.go`, `cost.go`, `at.go`. Signatures in §5.4.

- `Decimal`: unexported `unscaled *big.Int` (nil means zero) and `scale int32`; values are
  immutable (every method returns a new value). `ParseDecimal` matches
  `^-?(0|[1-9][0-9]*)(\.[0-9]+)?$` before `big.Int.SetString` on the digits. `String` strips
  trailing fraction zeros. `Round(places)` adds or subtracts half a unit then truncates toward
  zero.
- `CostOf` follows the table of §5.4 in that order; `Lines` are in the category order of the
  `Category` constants; `Amount` is the sum of the lines.
- `Version.Validate` errors are constants naming the rule (no value).

**Verify:** T-370 to T-373, T-R23.

### C2. `internal/pricing/genaiprices` — `feat: price dataset parser and extractor`

**Files:** `parse.go`, `match.go`, `extract.go`, `snapshot.go`. §5.6.

- Types mirror the v2 schema for the members used: `Provider{ID string; Models []Model}`,
  `Model{ID string; Match Clause; Prices json.RawMessage; PriceVariants []Variant;
  ContextWindow *json.Number; Deprecated bool}`; `Prices` is decoded lazily into either an object
  or a list (first non-space byte `{` or `[`).
- `Clause` is a struct with one of `Equals`, `StartsWith`, `EndsWith`, `Contains`, `Regex`,
  `Or []Clause`, `And []Clause`; regexes are compiled once per `Parse` (cache by pattern).
- `Extract(m, today)` takes `today` as a `time.Time` truncated to the UTC day; `start_date` is
  parsed with `time.DateOnly`.
- `snapshot.go`: `//go:embed snapshot/data.json` and `snapshot/SOURCE`; `SnapshotInfo` parses
  `SOURCE`; `init` does not parse the dataset (lazy, `sync.OnceValues`).

**Verify:** T-374 to T-377.

### C3. Migration and lint spec — `feat: migration 0004, catalogue in the isolation spec`

The migration exists since part A. Apply the spec of B2.

**Verify:** `go test ./internal/store/... ./test/isolation/ -run
'Lint|Spec|Migrat|CrossTenant|Positive|CatalogPolicies|PriceGuards'` (T-140, T-142, T-143,
T-390 to T-393).

### C4. `internal/catalog`: names, access, cache — `feat: catalogue access rule and gateway cache`

**Files:** `names.go`, `access.go`, `load.go`, `cache.go`. §5.2, §5.3, §5.7.

- `ReservedProviderNames = []string{"healthz", "readyz", "v1"}`; `ValidateProviderName`,
  `ValidateEndpointURL(raw string, private bool) (normalized string, err error)` (strips one
  trailing slash, lower-cases scheme and host), `ValidateModelName`, `ValidateUpstreamID`,
  `ParseRef`.
- `LoadSnapshot(ctx, db)` runs `LoadProviders`, `LoadEndpoints`, `LoadModels`,
  `LoadPriceVersions` in one `db.Unscoped` transaction; `LoadTenantAccess(ctx, db, id)` runs
  `ListTenantGrants` and `GetModelRestriction` (`store.ErrNoRows`: unrestricted) in
  `db.WithTenant`. `PriceAt(ctx, tx, modelID, at)` wraps the statement and converts rows to
  `pricing.Version` (rates parsed with `ParseDecimal`; a parse failure is an internal error).
- `Cache`: constants `maxTenantEntries = 10000`; `NewCache` refuses a TTL outside 1 s to 5 m, a nil
  database or logger. `Invalidate` per §5.7.
- `internal/authn/cache.go` `invalidate` and `internal/vault/opener.go` `Invalidate`: add
  `payload == "catalog"`, `strings.HasPrefix(payload, "catalog:")` to the ignored cases.

**Verify:** T-378 to T-381, T-397, T-398.

### C5. Importer, fetch, refresh — `feat: price importer, remote fetch, refresh job`

**Files:** `importer.go`, `fetch.go`, `refresh.go`. §5.6.

- `Import`: digest = SHA-256 of `src.Data`; parse; then one `db.Platform` transaction:
  `LockPriceImports`, `LoadProviders`, `LoadModels` (via the same queries), per model `PriceAt`
  (now), compare, `CreatePriceVersion` + `CreatePriceRates`, audit `price.create`; then
  `CreatePriceImport` and, when §5.6 says, `price_import.run`; `price.create` rows name
  `import_id`, so insert the import row first with the final counts computed in memory before any
  write (compute the whole plan, then write: import row, versions, rates, audits).
- A failed parse or fetch writes a `failure` import row in its own platform transaction (no
  version), then returns `ErrDatasetInvalid` or `*FetchError{Class}`.
- `ImportAtStart(ctx, db, logger)`: `PriceImportHistory(digest, SnapshotInfo().retrieved)`;
  skip (log `price import skipped`) or `Import`; returns nothing; logs per §5.11.
- `Fetch(ctx, url, opts)`: the transport of §5.6; `io.LimitReader(body, 16<<20+1)` and a length
  check; `FetchError` classes as listed. The user agent is `"pandokos/" + buildinfo.Version`.
- `Refresher{DB, Logger, URL, Interval, FirstDelay}` with `Run(ctx)`: sleep `FirstDelay`
  (default 1 m), then loop: `PriceImportHistory` → skip when `last_remote` is younger than
  `Interval/2`; else fetch (no transaction open), then `Import` with source `remote`; wait
  `Interval`. Every run logs one line.

**Verify:** T-386, T-394 to T-396.

### C6. Generator and update bodies — `feat: optional members of update bodies, nested request objects`

The generator part was done in A4 (T-384). Here:

- `internal/adminapi/runtime.go`: `Optional[T]`, `Some`; delete `EmptyDescription` and
  `CredentialUpdate.MarshalJSON`.
- `internal/admin`: delete `peekUpdate` and `descriptionGiven`; the three existing update
  handlers use `.Set`; `internal/cli` sends `adminapi.Some("")` where it sent the sentinel.

**Verify:** T-384, T-407 for the three existing operations, the credential and CLI tests.

### C7. Permissions and principal — `feat: catalogue permissions, model list on the principal`

`internal/authz`: five constants and catalogue rows of §5.9 appended in order; roles updated;
`Principal.Models []string`. `internal/authn`: `Models` from `GetKeyPrincipalRow` (`nil` unless
`ModelsRestricted`).

**Verify:** T-382, the `authn` database tests, T-203, T-204.

### C8. Admin handlers — `feat: catalogue, price and model access operations`

**Files:** `internal/admin/{providers,endpoints,models,model_grants,prices,price_imports,
model_access}.go`, `admin.go`, `problem.go`, `convert.go`, `virtual_keys.go`, `credentials.go`;
delete `catalog_todo.go`.

- `Options` gains `Snapshot []byte` (the embedded dataset; required), `Refresh struct{ Enabled
  bool; URL string; Fetch func(ctx, url) ([]byte, error) }`.
- `problem.go`: the two codes of §5.8; `fail` maps `catalog.ErrDatasetInvalid` and
  `*catalog.FetchError`.
- Errors from the database: `store.ConstraintViolation` with the constraint names of §5.8;
  SQLSTATE `23503` → `invalid_state` "still referenced"; the price trigger's `restrict_violation`
  on delete → `invalid_state`.
- Converters produce decimal strings with `Decimal.String()`; `Model.ref` is `provider/name`;
  `context_window` 0 for `NULL`.
- `updateEndpoint` with `is_default: true`: `ClearDefaultEndpoint` then `UpdateEndpoint`;
  `is_default: false` on the default → `invalid_request` on `is_default`. `deleteEndpoint` of the
  default while others exist → `invalid_state`.
- `createPriceOverride`: `LockModel`, `Validate`, `CreatePriceVersion` + `CreatePriceRates`,
  `SetModelPriceOverride(true)`, audit; `valid_from` before the request's start →
  `invalid_request`.
- `runPriceImport`: `embedded` → `catalog.Import` with `Options.Snapshot`; `remote` → refused
  unless `Refresh.Enabled`, else fetch then import; `dry_run` passes through.
- `listTenantModels`: `ListVisibleModels`, then `GetModelRestriction`, then for each model
  `PriceAt` (now) — one tenant transaction; pages of at most the requested limit.
- `setModelRestriction` and `createVirtualKey` with `models`: each reference is checked against
  `ListVisibleModels`-equivalent visibility in the tenant transaction (a helper
  `visibleRefs(ctx, tx) map[string]bool` reading all visible models once).
- `createCredential`: replace `vault.KnownProvider` by `GetProvider`; delete `vault.Providers`
  and `vault.KnownProvider` and their tests.

**Verify:** T-400 to T-407, T-270 to T-275, T-281, T-282, T-340.

### C9. Configuration — `feat: catalogue and pricing configuration`

`internal/config`: `Cache.CatalogTTL`, `Pricing{ImportAtStart bool; Refresh{Enabled bool; URL
string; ParsedURL *url.URL; Interval Duration}}`; five `keyTable` rows; defaults; validation of
§5.10 (`https` only, no user information, bounds). The keys are read for `serve` and `config
check --for serve`; `import-prices` needs none of them.

**Verify:** T-383, the configuration reference test after part E.

### C10. Commands and wiring — `feat: import-prices, serve wiring, catalogue commands`

- `cmd/pandokos/importprices.go`: §5.10; `usage.go` gains the command and the API command groups.
- `cmd/pandokos/serve.go`, after `vault ready`: `catalog.NewCache` on the gateway pool; the
  listener's `OnNotify` calls `gatewayAuth.Invalidate(p); opener.Invalidate(p);
  catalogCache.Invalidate(p)` and `OnReset` the three `Flush`; `admin.Options{…, Snapshot:
  genaiprices.Snapshot(), Refresh: …}`; when `pricing.import_at_start`, `catalog.ImportAtStart`
  on the worker pool (60 s timeout) before `srv.Listen`; when `pricing.refresh.enabled`, a
  `Refresher` goroutine on the worker pool joined to the existing `wg`. The cache is not passed
  to `server.New`.
- `internal/cli`: the commands of §5.10 in the style of the existing ones; model references
  resolved with `ListModels{Provider, Name}`; `key create --model`.

**Verify:** T-385, T-410, T-411.

### C11. Everything together — `test: end-to-end, container and SDK suites pass`

`make check`, `make image`, `make test-container`. Fix what the suites show; no new behaviour.

---

## Part D — CI and container (commit: `test: price import in the container test`)

- `test/container/container_test.go`: assert the `prices imported` line with `source=embedded`.
  No network is needed (the snapshot is embedded); `pricing.refresh.enabled` stays false.
- No new job, service or secret in CI. The `Dockerfile` does not change.

**Verify:** `make image && make test-container`; CI green on the branch.

---

## Part E — Documentation and decision records (commit: `docs: sprint 2.2 documentation and ADRs`)

### E1. New: `docs/operations/catalogue.md`

Sections, in this order (T-R24 checks the marked facts):

1. **Concepts**: provider, API family, endpoint (default, private), model (name, upstream
   identifier, status, availability), credential (vault) — CAT-01's five concepts in one table.
2. **The seed** and how to add an OpenAI-compatible provider (`provider create`, `endpoint
   create`, `model create`, `credential create`), with a self-hosted private endpoint example.
3. **Who may use a model** *(§5.3 rule)*: availability, grants, tenant restriction, key list;
   one refusal for all; the CLI for each.
4. **Prices**: versions, currency, "in force at t", categories and the formula table of §5.4,
   tiers, fallbacks, unknown cost; rates as decimal strings.
5. **The dataset** *(name, URL, MIT licence and copyright line, v2 format, embedded commit)*;
   what is imported and the issue list of §5.6; `import-prices --file`; the start import and its
   two skip rules; the refresh *(the five keys)*, no proxy until 3.1, pinning the URL to a
   commit; "imports never change an override" *(exact phrase)*.
6. **Overrides**: create, schedule, delete before start, clear.
7. **How fast a change takes effect** *(`cache.catalog_ttl`, 30 s default, 5 m maximum)*.
8. **Limits**: estimates, not invoices (ADR 0011); variants and time-of-day prices not
   represented; currencies not converted.

### E2. Changed pages

| Page | Change |
|---|---|
| `docs/operations/configuration.md` | the five keys (generated table), the example |
| `docs/operations/database.md` | section "Grants of migration 0004" (the matrix of B2 in prose); the worker's new privileges |
| `docs/operations/keys.md` | `--model` and the key's model list; restriction of the tenant |
| `docs/operations/vault.md` | providers come from the catalogue; a disabled provider still accepts credentials |
| `docs/operations/container.md` | the start import; outbound access only with refresh |
| `docs/reference/permissions.md` | five permissions, the roles |
| `docs/reference/admin-api.md` | the two problem codes; `Optional` members of update bodies and "an empty string clears"; rates are strings; commands table including `import-prices` |
| `docs/architecture/data-model.md` | catalogue tables in the platform group with their policies (restricted, read by all roles); `model_tenant_grant` replaces `model_grant` and moves to the platform group (D-2); `tenant_model_restriction` in the policy group; `virtual_key.models`; the worker's role row gains "import prices in platform scope" |
| `docs/architecture/security-model.md` | trust in the price dataset (R-3); private endpoints and the 3.1 guard |
| `docs/architecture/overview.md` | §11: `catalog/`, `pricing/` lines |
| `docs/adr/README.md` | 0035, 0036 |
| `README.md`, `planning/roadmap.md`, epic file | status only, at close |

### E3. Decision records (copy; status `proposed`, date of the commit)

**`docs/adr/0035-catalogue-and-model-access.md`**

> # 0035. Catalogue and model access
>
> Refines [ADR 0009](0009-native-passthrough-by-api-family.md) and
> [ADR 0027](0027-database-roles-and-session-context.md).
>
> ## Context
>
> CAT-01 to CAT-06 ask for providers, families, endpoints and models as distinct concepts, for
> models available to all or selected tenants and narrowed by tenants and keys, and for one
> refusal of a model that is not allowed whatever identifier is used. The gateway reads the
> catalogue without platform scope; the platform must still see which tenants a model is granted
> to.
>
> ## Decision
>
> 1. Catalogue tables are global with forced row-level security: every runtime role that needs
>    them reads them in any session; only the admin role in platform scope writes them (the
>    worker inserts prices). Grants of models to tenants are a global table whose rows a tenant's
>    session sees only for itself.
> 2. A provider has an immutable name (the first path segment; `healthz`, `readyz` and `v1` are
>    reserved) and an immutable API family from a list in code. It has endpoints; one is the
>    default the gateway uses. An endpoint declared private may use `http`; a public one may not
>    name a private address.
> 3. A model has a public name (immutable) and an upstream identifier, each unique per provider;
>    it is referenced as `provider/name`; status `active`, `deprecated` or `retired`;
>    availability `all` or `selected`.
> 4. A principal may use a model when the provider is enabled with an enabled default endpoint,
>    the model is not retired, it is available to the tenant (all, or granted), and neither the
>    tenant's restriction nor the key's immutable model list leaves it out. Every other case is
>    the same refusal.
> 5. The gateway caches the catalogue and each tenant's access for `cache.catalog_ttl`,
>    invalidated by the payloads `catalog` and `catalog:<tenant>`.
>
> ## Consequences
>
> A tenant cannot learn which models other tenants were granted. Catalogue writes cannot happen
> from a tenant's session even through a defective handler. A change reaches the gateway within
> the cache lifetime.
>
> ## Alternatives considered
>
> Open global tables (writes would rest on handler code); a tenant-scoped grant table (the
> platform could not list a model's tenants); matching callers' model names against upstream
> identifiers or patterns (a second way in, which CAT-06 forbids); a mutable key model list (a
> second invalidation path).

**`docs/adr/0036-prices-costs-and-importer.md`**

> # 0036. Prices, costs and the price importer
>
> Refines [ADR 0011](0011-pricing-source.md).
>
> ## Context
>
> ADR 0011 fixes versioned prices imported from `genai-prices`, operator overrides and decimal
> arithmetic. Building it needs a storage format, a cost formula over the usage of ADR 0010, a
> mapping from the dataset, whose v1 files are frozen upstream, and rules for imports at start and
> by refresh.
>
> ## Decision
>
> 1. A price version belongs to a model, starts at `valid_from` (never in the past), has a
>    currency and rates per million units of a category (`input`, `output`, `cache_read`,
>    `cache_write`, `cache_write_1h`, `input_audio`, `output_audio`), with optional tiers whose
>    threshold is compared with the request's input tokens and applies to every unit (cliff).
>    Versions never change; one that has not started may be deleted. The version in force at t
>    has the greatest `valid_from ≤ t`.
> 2. Amounts are exact decimals (`math/big`; `numeric(24,12)` for rates; decimal text in SQL and
>    JSON); costs are not rounded except for display. A cost carries its currency and is unknown,
>    with a reason, when no version is in force or a category with units has no rate after the
>    documented fallbacks.
> 3. The importer reads the `genai-prices` v2 format, prices catalogue models only by applying
>    the dataset's match clauses to their upstream identifiers, imports the price in force today,
>    and reports per model what it cannot represent (scheduled and time-of-day prices, price
>    variants, other units). It never creates models. A changed price is a new version starting at
>    the import, with an audit record; a model with an operator override is left alone.
> 4. A release embeds the v2 file of a pinned commit with its licence; `serve` imports it once per
>    digest and never over a newer file or download. The download is off by default, goes to an
>    operator-configured `https` URL without proxy or redirects, bounded in size and time, and
>    runs with the worker role.
>
> ## Consequences
>
> Past costs are reproducible from immutable versions. Discounted tiers and time-of-day prices are
> priced at the standard rate until represented. A format change upstream breaks imports, not
> installations.
>
> ## Alternatives considered
>
> Floating point or a decimal dependency; prices in a JSON column; backdated versions;
> importing the v1 files (frozen); creating models from the dataset (its identifiers include
> prefixes and pseudo-models); importing scheduled prices as future versions (upstream withdraws
> them).

**History lines** to append: ADR 0009 — "2026-11: sprint 2.2 reserves `healthz`, `readyz` and
`v1` as provider names and stores the family per provider (ADR 0035)"; ADR 0011 — "2026-11:
sprint 2.2 implements it with the v2 format of the dataset, which replaced the frozen v1 files
(ADR 0036)"; ADR 0027 — "2026-11: sprint 2.2 adds restricted global catalogue tables read in any
session, and insert grants for the worker on prices and the platform audit log"; ADR 0028 —
"2026-11: sprint 2.2 adds presence-aware optional members of update bodies and nested request
objects; the description sentinel of 2.1 is removed"; ADR 0030 — "2026-11: sprint 2.2 adds the
payloads `catalog` and `catalog:<tenant>`, which the principal cache ignores".

### E4. Planning files

`sprint.md` front matter `status: done` at close; the epic stays `in-progress` until the epic is
closed; the roadmap row is unchanged.

**Verify (part E):** `go test ./test/repo/` (T-R14, T-R23, T-R24, the configuration reference
test).

---

## Part F — Verification

Run in order and paste the summary lines in the pull request:

1. `make db-down; make db-up`
2. `make check`
3. `make image && make test-container`
4. `git diff main -- go.mod go.sum` — empty.
5. `go test ./test/isolation/ -run 'TestSchemaLint$' -v` — zero findings.
6. `go test ./internal/catalog/ ./internal/admin/ ./test/e2e/ -run 'ResolveDB|ModelVisibility|PriceAtDB|ImportDB|TestCatalogue' -v`
   — the three exit conditions of the epic.
7. `cd internal/pricing/genaiprices/snapshot && sha256sum data.json && grep sha256 SOURCE` — equal.
8. By hand, once: `serve` on an empty database and read `prices imported`; `bin/pandokos
   import-prices --file test/fixtures/prices/changed.json --dry-run`; `bin/pandokos model list
   --provider openai`.
9. Tick the definition of done (§10) item by item in the pull request, with the coverage table
   and the tests-first output of part B.

## What the implementer does not do

- Does not call `Resolve` or `CostOf` from the inference route, and does not remove `skeleton.*`
  (3.1, 3.2).
- Does not make the download honour `HTTPS_PROXY`, follow redirects, or take a URL from a request.
- Does not create models from the dataset, import scheduled or time-of-day prices, or edit,
  backdate or update a price version.
- Does not add a policy, a grant or a lint exception beyond appendix A and B2.
- Does not change merged migrations or the listener of `internal/store`.

---

## Appendix A — `internal/store/migrations/sql/0004_catalogue.sql`

```sql
-- 0004_catalogue: providers, endpoints, models, model grants to tenants, the model restriction of
-- a tenant and of a key, price imports, price versions and their rates, catalogue notifications,
-- the seed rows of the providers openai and anthropic. Runs in one transaction as pandokos_owner.
-- Every name is schema-qualified.

-- Tells the instances that the catalogue changed. TG_ARGV[0]: 'global' (payload 'catalog', for a
-- statement trigger) or 'scoped' (payload 'catalog:<tenant>', from the row's tenant_id). The
-- payload never names a model, a price or a key.
CREATE FUNCTION pandokos.notify_catalog() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    rec jsonb;
BEGIN
    IF TG_ARGV[0] = 'global' THEN
        PERFORM pg_catalog.pg_notify('pandokos_cache', 'catalog');
        RETURN NULL;
    END IF;
    IF TG_OP = 'DELETE' THEN
        rec := to_jsonb(OLD);
    ELSE
        rec := to_jsonb(NEW);
    END IF;
    PERFORM pg_catalog.pg_notify('pandokos_cache', 'catalog:' || (rec ->> 'tenant_id'));
    RETURN NULL;
END
$$;

-- Rows that nobody changes once written.
CREATE FUNCTION pandokos.immutable_row() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    RAISE EXCEPTION 'rows of % are immutable', TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
END
$$;

-- Providers (global table with explicit policies). The name is the first path segment of the
-- inference listener and what credentials refer to: it never changes, and the names the
-- listener uses itself are refused.
CREATE TABLE pandokos.provider (
    id               uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    name             text        NOT NULL,
    display_name     text        NOT NULL DEFAULT '',
    description      text        NOT NULL DEFAULT '',
    api_family       text        NOT NULL,
    enabled          boolean     NOT NULL DEFAULT true,
    dataset_provider text        NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT provider_pkey PRIMARY KEY (id),
    CONSTRAINT provider_name_key UNIQUE (name),
    CONSTRAINT provider_name_format CHECK (name ~ '^[a-z][a-z0-9-]{0,62}$'),
    CONSTRAINT provider_name_reserved CHECK (name NOT IN ('healthz', 'readyz', 'v1')),
    CONSTRAINT provider_display_name_length CHECK (char_length(display_name) <= 200),
    CONSTRAINT provider_description_length CHECK (char_length(description) <= 500),
    CONSTRAINT provider_api_family_valid CHECK (api_family IN ('openai', 'anthropic')),
    CONSTRAINT provider_dataset_provider_format CHECK (dataset_provider ~ '^([a-z0-9][a-z0-9_.-]{0,62})?$')
);

CREATE FUNCTION pandokos.provider_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NEW.name <> OLD.name OR NEW.api_family <> OLD.api_family THEN
        RAISE EXCEPTION 'the name and the API family of a provider are immutable' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

-- Endpoints of providers (global table with explicit policies). base_url is what the native path
-- is appended to. A private endpoint is one the platform administrator declares to be on a
-- private network: only such an endpoint may use http, and only such an endpoint will be allowed
-- to reach a private address by the egress guard of sprint 3.1.
CREATE TABLE pandokos.provider_endpoint (
    id          uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    provider_id uuid        NOT NULL,
    name        text        NOT NULL,
    base_url    text        NOT NULL,
    region      text        NOT NULL DEFAULT '',
    private     boolean     NOT NULL DEFAULT false,
    enabled     boolean     NOT NULL DEFAULT true,
    is_default  boolean     NOT NULL DEFAULT false,
    description text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT provider_endpoint_pkey PRIMARY KEY (id),
    CONSTRAINT provider_endpoint_provider_fkey FOREIGN KEY (provider_id) REFERENCES pandokos.provider (id),
    CONSTRAINT provider_endpoint_name_key UNIQUE (provider_id, name),
    CONSTRAINT provider_endpoint_name_format CHECK (name ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    CONSTRAINT provider_endpoint_base_url_format CHECK (char_length(base_url) <= 2048
        AND base_url ~ '^https?://[^/?#@[:space:]]+(/[^?#[:space:]]*)?$' AND base_url !~ '/$'),
    CONSTRAINT provider_endpoint_plain_http_private CHECK (base_url ~ '^https://' OR private),
    CONSTRAINT provider_endpoint_region_format CHECK (region ~ '^([a-z0-9][a-z0-9-]{0,62})?$'),
    CONSTRAINT provider_endpoint_description_length CHECK (char_length(description) <= 500)
);
-- At most one default endpoint per provider: the one the gateway uses.
CREATE UNIQUE INDEX provider_endpoint_default_key ON pandokos.provider_endpoint (provider_id) WHERE is_default;

CREATE FUNCTION pandokos.provider_endpoint_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NEW.provider_id <> OLD.provider_id OR NEW.name <> OLD.name THEN
        RAISE EXCEPTION 'the provider and the name of an endpoint are immutable' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

-- Models (global table with explicit policies). name is what callers send; upstream_id is what
-- the provider receives. A model is available to every tenant, or to the tenants granted it.
CREATE TABLE pandokos.model (
    id                uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    provider_id       uuid        NOT NULL,
    name              text        NOT NULL,
    upstream_id       text        NOT NULL,
    display_name      text        NOT NULL DEFAULT '',
    description       text        NOT NULL DEFAULT '',
    status            text        NOT NULL DEFAULT 'active',
    availability      text        NOT NULL DEFAULT 'selected',
    capabilities      text[]      NOT NULL DEFAULT '{}',
    context_window    bigint,
    max_output_tokens bigint,
    price_override    boolean     NOT NULL DEFAULT false,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT model_pkey PRIMARY KEY (id),
    CONSTRAINT model_provider_fkey FOREIGN KEY (provider_id) REFERENCES pandokos.provider (id),
    CONSTRAINT model_name_key UNIQUE (provider_id, name),
    CONSTRAINT model_upstream_id_key UNIQUE (provider_id, upstream_id),
    CONSTRAINT model_name_format CHECK (name ~ '^[A-Za-z0-9][A-Za-z0-9._:@/+-]{0,127}$'),
    CONSTRAINT model_upstream_id_format CHECK (char_length(upstream_id) <= 256 AND upstream_id ~ '^[!-~]+$'),
    CONSTRAINT model_display_name_length CHECK (char_length(display_name) <= 200),
    CONSTRAINT model_description_length CHECK (char_length(description) <= 1000),
    CONSTRAINT model_status_valid CHECK (status IN ('active', 'deprecated', 'retired')),
    CONSTRAINT model_availability_valid CHECK (availability IN ('all', 'selected')),
    CONSTRAINT model_capabilities_valid CHECK (capabilities <@ ARRAY['text', 'vision', 'audio', 'tools', 'reasoning',
        'embeddings', 'structured_output', 'streaming']::text[]),
    CONSTRAINT model_context_window_range CHECK (context_window IS NULL OR context_window BETWEEN 1 AND 100000000),
    CONSTRAINT model_max_output_tokens_range CHECK (max_output_tokens IS NULL OR max_output_tokens BETWEEN 1 AND 100000000)
);

CREATE FUNCTION pandokos.model_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NEW.provider_id <> OLD.provider_id OR NEW.name <> OLD.name THEN
        RAISE EXCEPTION 'the provider and the name of a model are immutable' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

-- Seed: the two providers of tier 1 that have their own API family, their public endpoints and
-- a first set of their models, available to every tenant. Inserted before row-level security is
-- enabled, because the owner has no policy.
INSERT INTO pandokos.provider (name, display_name, api_family, dataset_provider) VALUES
    ('openai', 'OpenAI', 'openai', 'openai'),
    ('anthropic', 'Anthropic', 'anthropic', 'anthropic');

INSERT INTO pandokos.provider_endpoint (provider_id, name, base_url, is_default)
SELECT p.id, 'default', v.base_url, true
  FROM pandokos.provider p
  JOIN (VALUES ('openai', 'https://api.openai.com'), ('anthropic', 'https://api.anthropic.com')) AS v (provider, base_url)
    ON v.provider = p.name;

INSERT INTO pandokos.model (provider_id, name, upstream_id, availability, capabilities, context_window)
SELECT p.id, v.name, v.name, 'all', v.capabilities::text[], v.context_window
  FROM pandokos.provider p
  JOIN (VALUES
        ('openai', 'gpt-4o', '{text}', 128000),
        ('openai', 'gpt-4o-mini', '{text}', 128000),
        ('openai', 'gpt-4.1', '{text}', 1000000),
        ('openai', 'gpt-4.1-mini', '{text}', 1000000),
        ('openai', 'gpt-4.1-nano', '{text}', 1000000),
        ('openai', 'gpt-5', '{text}', 400000),
        ('openai', 'gpt-5-mini', '{text}', 400000),
        ('openai', 'gpt-5-nano', '{text}', 400000),
        ('openai', 'o3', '{text}', 200000),
        ('openai', 'o4-mini', '{text}', 200000),
        ('openai', 'text-embedding-3-small', '{embeddings}', 8192),
        ('openai', 'text-embedding-3-large', '{embeddings}', 8192),
        ('anthropic', 'claude-haiku-4-5', '{text}', 200000),
        ('anthropic', 'claude-sonnet-4-5', '{text}', 200000),
        ('anthropic', 'claude-opus-4-5', '{text}', 200000),
        ('anthropic', 'claude-sonnet-4-6', '{text}', 1000000),
        ('anthropic', 'claude-opus-4-6', '{text}', 1000000)
       ) AS v (provider, name, capabilities, context_window)
    ON v.provider = p.name;

CREATE TRIGGER provider_row_guard BEFORE INSERT OR UPDATE ON pandokos.provider
    FOR EACH ROW EXECUTE FUNCTION pandokos.global_row_guard();
CREATE TRIGGER provider_guard BEFORE UPDATE ON pandokos.provider
    FOR EACH ROW EXECUTE FUNCTION pandokos.provider_guard();
CREATE TRIGGER provider_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.provider
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.notify_catalog('global');

CREATE TRIGGER provider_endpoint_row_guard BEFORE INSERT OR UPDATE ON pandokos.provider_endpoint
    FOR EACH ROW EXECUTE FUNCTION pandokos.global_row_guard();
CREATE TRIGGER provider_endpoint_guard BEFORE UPDATE ON pandokos.provider_endpoint
    FOR EACH ROW EXECUTE FUNCTION pandokos.provider_endpoint_guard();
CREATE TRIGGER provider_endpoint_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.provider_endpoint
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.notify_catalog('global');

CREATE TRIGGER model_row_guard BEFORE INSERT OR UPDATE ON pandokos.model
    FOR EACH ROW EXECUTE FUNCTION pandokos.global_row_guard();
CREATE TRIGGER model_guard BEFORE UPDATE ON pandokos.model
    FOR EACH ROW EXECUTE FUNCTION pandokos.model_guard();
CREATE TRIGGER model_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.model
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.notify_catalog('global');

-- Every runtime role reads the catalogue in any session; only the admin role in platform scope
-- writes it.
ALTER TABLE pandokos.provider ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.provider FORCE ROW LEVEL SECURITY;
CREATE POLICY provider_read ON pandokos.provider
    FOR SELECT TO pandokos_gateway, pandokos_admin, pandokos_worker USING (true);
CREATE POLICY provider_platform_insert ON pandokos.provider
    FOR INSERT TO pandokos_admin WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY provider_platform_update ON pandokos.provider
    FOR UPDATE TO pandokos_admin USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY provider_platform_delete ON pandokos.provider
    FOR DELETE TO pandokos_admin USING ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.provider TO pandokos_gateway, pandokos_admin, pandokos_worker;
GRANT INSERT (name, display_name, description, api_family, enabled, dataset_provider) ON pandokos.provider TO pandokos_admin;
GRANT UPDATE (display_name, description, enabled, dataset_provider) ON pandokos.provider TO pandokos_admin;
GRANT DELETE ON pandokos.provider TO pandokos_admin;

ALTER TABLE pandokos.provider_endpoint ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.provider_endpoint FORCE ROW LEVEL SECURITY;
CREATE POLICY provider_endpoint_read ON pandokos.provider_endpoint
    FOR SELECT TO pandokos_gateway, pandokos_admin USING (true);
CREATE POLICY provider_endpoint_platform_insert ON pandokos.provider_endpoint
    FOR INSERT TO pandokos_admin WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY provider_endpoint_platform_update ON pandokos.provider_endpoint
    FOR UPDATE TO pandokos_admin USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY provider_endpoint_platform_delete ON pandokos.provider_endpoint
    FOR DELETE TO pandokos_admin USING ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.provider_endpoint TO pandokos_gateway, pandokos_admin;
GRANT INSERT (provider_id, name, base_url, region, private, enabled, is_default, description)
    ON pandokos.provider_endpoint TO pandokos_admin;
GRANT UPDATE (base_url, region, private, enabled, is_default, description) ON pandokos.provider_endpoint TO pandokos_admin;
GRANT DELETE ON pandokos.provider_endpoint TO pandokos_admin;

ALTER TABLE pandokos.model ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.model FORCE ROW LEVEL SECURITY;
CREATE POLICY model_read ON pandokos.model
    FOR SELECT TO pandokos_gateway, pandokos_admin, pandokos_worker USING (true);
CREATE POLICY model_platform_insert ON pandokos.model
    FOR INSERT TO pandokos_admin WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY model_platform_update ON pandokos.model
    FOR UPDATE TO pandokos_admin USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY model_platform_delete ON pandokos.model
    FOR DELETE TO pandokos_admin USING ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.model TO pandokos_gateway, pandokos_admin, pandokos_worker;
GRANT INSERT (provider_id, name, upstream_id, display_name, description, status, availability, capabilities,
              context_window, max_output_tokens)
    ON pandokos.model TO pandokos_admin;
GRANT UPDATE (upstream_id, display_name, description, status, availability, capabilities, context_window,
              max_output_tokens, price_override)
    ON pandokos.model TO pandokos_admin;
GRANT DELETE ON pandokos.model TO pandokos_admin;

-- Grants of models to tenants (global table with explicit policies: it describes tenants). A
-- tenant's session sees its own rows; the platform scope sees and changes all of them.
CREATE TABLE pandokos.model_tenant_grant (
    model_id   uuid        NOT NULL,
    tenant_id  uuid        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT model_tenant_grant_pkey PRIMARY KEY (model_id, tenant_id),
    CONSTRAINT model_tenant_grant_model_fkey FOREIGN KEY (model_id) REFERENCES pandokos.model (id) ON DELETE CASCADE,
    CONSTRAINT model_tenant_grant_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id)
);
CREATE INDEX model_tenant_grant_tenant_idx ON pandokos.model_tenant_grant (tenant_id);

CREATE TRIGGER model_tenant_grant_notify AFTER INSERT OR DELETE ON pandokos.model_tenant_grant
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_catalog('scoped');

ALTER TABLE pandokos.model_tenant_grant ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.model_tenant_grant FORCE ROW LEVEL SECURITY;
CREATE POLICY model_tenant_grant_self ON pandokos.model_tenant_grant
    FOR SELECT TO pandokos_gateway, pandokos_admin
    USING (tenant_id = (SELECT pandokos.current_tenant_id()));
CREATE POLICY model_tenant_grant_platform_select ON pandokos.model_tenant_grant
    FOR SELECT TO pandokos_admin USING ((SELECT pandokos.platform_scope()));
CREATE POLICY model_tenant_grant_platform_insert ON pandokos.model_tenant_grant
    FOR INSERT TO pandokos_admin WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY model_tenant_grant_platform_delete ON pandokos.model_tenant_grant
    FOR DELETE TO pandokos_admin USING ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.model_tenant_grant TO pandokos_gateway, pandokos_admin;
GRANT INSERT (model_id, tenant_id) ON pandokos.model_tenant_grant TO pandokos_admin;
GRANT DELETE ON pandokos.model_tenant_grant TO pandokos_admin;

-- The models a tenant restricts itself to (tenant-scoped table). No row: no restriction. A row:
-- only the models it names, as "<provider>/<model>".
CREATE TABLE pandokos.tenant_model_restriction (
    tenant_id  uuid        NOT NULL,
    id         uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    models     text[]      NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tenant_model_restriction_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT tenant_model_restriction_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT tenant_model_restriction_tenant_key UNIQUE (tenant_id),
    CONSTRAINT tenant_model_restriction_models_valid CHECK (cardinality(models) <= 512 AND array_position(models, NULL) IS NULL)
);

CREATE TRIGGER tenant_model_restriction_guard BEFORE INSERT OR UPDATE ON pandokos.tenant_model_restriction
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();
CREATE TRIGGER tenant_model_restriction_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.tenant_model_restriction
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_catalog('scoped');

ALTER TABLE pandokos.tenant_model_restriction ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.tenant_model_restriction FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.tenant_model_restriction
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.tenant_model_restriction TO pandokos_gateway, pandokos_admin;
GRANT INSERT (tenant_id, models) ON pandokos.tenant_model_restriction TO pandokos_admin;
GRANT UPDATE (models) ON pandokos.tenant_model_restriction TO pandokos_admin;
GRANT DELETE ON pandokos.tenant_model_restriction TO pandokos_admin;

-- The model allow-list of a virtual key (IAM-04): NULL, no restriction. Set when the key is
-- created and never changed, like its scopes.
ALTER TABLE pandokos.virtual_key ADD COLUMN models text[];
ALTER TABLE pandokos.virtual_key ADD CONSTRAINT virtual_key_models_valid
    CHECK (models IS NULL OR (cardinality(models) <= 64 AND array_position(models, NULL) IS NULL));

CREATE FUNCTION pandokos.virtual_key_models_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NEW.models IS DISTINCT FROM OLD.models THEN
        RAISE EXCEPTION 'the models of a key are set when the key is created' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER virtual_key_models_guard BEFORE UPDATE ON pandokos.virtual_key
    FOR EACH ROW EXECUTE FUNCTION pandokos.virtual_key_models_guard();

GRANT INSERT (models) ON pandokos.virtual_key TO pandokos_admin;

-- Runs of the price importer (global table with explicit policies, append-only). report holds
-- what could not be represented, per catalogue model; it never holds dataset text beyond
-- identifiers.
CREATE TABLE pandokos.price_import (
    id               uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    started_at       timestamptz NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    source           text        NOT NULL,
    source_ref       text        NOT NULL DEFAULT '',
    dataset_format   text        NOT NULL,
    digest           text        NOT NULL DEFAULT '',
    outcome          text        NOT NULL,
    error_class      text        NOT NULL DEFAULT '',
    actor_type       text        NOT NULL,
    actor_id         text        NOT NULL DEFAULT '',
    models_seen      integer     NOT NULL DEFAULT 0,
    versions_created integer     NOT NULL DEFAULT 0,
    unchanged        integer     NOT NULL DEFAULT 0,
    overridden       integer     NOT NULL DEFAULT 0,
    unmatched        integer     NOT NULL DEFAULT 0,
    skipped          integer     NOT NULL DEFAULT 0,
    report           jsonb       NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT price_import_pkey PRIMARY KEY (id),
    CONSTRAINT price_import_source_valid CHECK (source IN ('embedded', 'file', 'remote')),
    CONSTRAINT price_import_source_ref_length CHECK (char_length(source_ref) <= 2048),
    CONSTRAINT price_import_dataset_format_valid CHECK (dataset_format IN ('genai-prices-v2')),
    CONSTRAINT price_import_digest_format CHECK (digest ~ '^([0-9a-f]{64})?$'),
    CONSTRAINT price_import_outcome_valid CHECK (outcome IN ('success', 'failure')),
    CONSTRAINT price_import_error_class_format CHECK (error_class ~ '^[a-z_]{0,64}$'),
    CONSTRAINT price_import_actor_type_valid CHECK (actor_type IN ('platform', 'host')),
    CONSTRAINT price_import_counts CHECK (LEAST(models_seen, versions_created, unchanged, overridden, unmatched, skipped) >= 0),
    CONSTRAINT price_import_report_object CHECK (jsonb_typeof(report) = 'object' AND pg_column_size(report) <= 65536)
);
CREATE INDEX price_import_digest_idx ON pandokos.price_import (digest);

CREATE TRIGGER price_import_append_only BEFORE UPDATE OR DELETE ON pandokos.price_import
    FOR EACH ROW EXECUTE FUNCTION pandokos.append_only();
CREATE TRIGGER price_import_no_truncate BEFORE TRUNCATE ON pandokos.price_import
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only();

ALTER TABLE pandokos.price_import ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.price_import FORCE ROW LEVEL SECURITY;
CREATE POLICY price_import_platform_select ON pandokos.price_import
    FOR SELECT TO pandokos_admin, pandokos_worker USING ((SELECT pandokos.platform_scope()));
CREATE POLICY price_import_platform_insert ON pandokos.price_import
    FOR INSERT TO pandokos_admin, pandokos_worker WITH CHECK ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.price_import TO pandokos_admin, pandokos_worker;
GRANT INSERT (started_at, source, source_ref, dataset_format, digest, outcome, error_class, actor_type, actor_id,
              models_seen, versions_created, unchanged, overridden, unmatched, skipped, report)
    ON pandokos.price_import TO pandokos_admin, pandokos_worker;

-- Price versions (global table with explicit policies). A version never changes; it starts now
-- or later, never in the past; one that has not started yet can be removed. The version in force
-- at a time t is the one of the model with the greatest valid_from <= t.
CREATE TABLE pandokos.price_version (
    id         uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    model_id   uuid        NOT NULL,
    valid_from timestamptz NOT NULL DEFAULT now(),
    currency   text        NOT NULL,
    source     text        NOT NULL,
    import_id  uuid,
    note       text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT price_version_pkey PRIMARY KEY (id),
    CONSTRAINT price_version_model_fkey FOREIGN KEY (model_id) REFERENCES pandokos.model (id),
    CONSTRAINT price_version_import_fkey FOREIGN KEY (import_id) REFERENCES pandokos.price_import (id),
    CONSTRAINT price_version_valid_from_key UNIQUE (model_id, valid_from),
    CONSTRAINT price_version_currency_format CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT price_version_source_valid CHECK (source IN ('import', 'override')),
    CONSTRAINT price_version_import_presence CHECK ((source = 'import') = (import_id IS NOT NULL)),
    CONSTRAINT price_version_note_length CHECK (char_length(note) <= 500)
);

CREATE FUNCTION pandokos.price_version_guard() RETURNS trigger
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
    IF OLD.valid_from <= now() THEN
        RAISE EXCEPTION 'a price version that has started cannot be removed' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN OLD;
END
$$;

CREATE TRIGGER price_version_guard BEFORE INSERT OR UPDATE OR DELETE ON pandokos.price_version
    FOR EACH ROW EXECUTE FUNCTION pandokos.price_version_guard();
CREATE TRIGGER price_version_no_truncate BEFORE TRUNCATE ON pandokos.price_version
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only();
CREATE TRIGGER price_version_notify AFTER INSERT OR DELETE ON pandokos.price_version
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.notify_catalog('global');

-- Rates of a price version, per million units of a category. threshold is the start of a tier:
-- when the input tokens of a request reach it, its rate applies to every unit of the category.
CREATE TABLE pandokos.price_rate (
    price_version_id uuid           NOT NULL,
    category         text           NOT NULL,
    threshold        bigint         NOT NULL DEFAULT 0,
    rate             numeric(24, 12) NOT NULL,
    CONSTRAINT price_rate_pkey PRIMARY KEY (price_version_id, category, threshold),
    CONSTRAINT price_rate_version_fkey FOREIGN KEY (price_version_id) REFERENCES pandokos.price_version (id) ON DELETE CASCADE,
    CONSTRAINT price_rate_category_valid CHECK (category IN ('input', 'output', 'cache_read', 'cache_write',
        'cache_write_1h', 'input_audio', 'output_audio')),
    CONSTRAINT price_rate_threshold_range CHECK (threshold BETWEEN 0 AND 100000000),
    CONSTRAINT price_rate_rate_range CHECK (rate >= 0 AND rate <= 1000000)
);

CREATE TRIGGER price_rate_immutable BEFORE UPDATE ON pandokos.price_rate
    FOR EACH ROW EXECUTE FUNCTION pandokos.immutable_row();
CREATE TRIGGER price_rate_no_truncate BEFORE TRUNCATE ON pandokos.price_rate
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only();

ALTER TABLE pandokos.price_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.price_version FORCE ROW LEVEL SECURITY;
CREATE POLICY price_version_read ON pandokos.price_version
    FOR SELECT TO pandokos_gateway, pandokos_admin, pandokos_worker USING (true);
CREATE POLICY price_version_platform_insert ON pandokos.price_version
    FOR INSERT TO pandokos_admin, pandokos_worker WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY price_version_platform_delete ON pandokos.price_version
    FOR DELETE TO pandokos_admin USING ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.price_version TO pandokos_gateway, pandokos_admin, pandokos_worker;
GRANT INSERT (model_id, valid_from, currency, source, import_id, note) ON pandokos.price_version TO pandokos_admin, pandokos_worker;
GRANT DELETE ON pandokos.price_version TO pandokos_admin;

ALTER TABLE pandokos.price_rate ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.price_rate FORCE ROW LEVEL SECURITY;
CREATE POLICY price_rate_read ON pandokos.price_rate
    FOR SELECT TO pandokos_gateway, pandokos_admin, pandokos_worker USING (true);
CREATE POLICY price_rate_platform_insert ON pandokos.price_rate
    FOR INSERT TO pandokos_admin, pandokos_worker WITH CHECK ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.price_rate TO pandokos_gateway, pandokos_admin, pandokos_worker;
GRANT INSERT (price_version_id, category, threshold, rate) ON pandokos.price_rate TO pandokos_admin, pandokos_worker;

-- The worker imports prices in platform scope and audits what it changed.
CREATE POLICY platform_audit_event_worker_insert ON pandokos.platform_audit_event
    FOR INSERT TO pandokos_worker
    WITH CHECK ((SELECT pandokos.platform_scope()));
GRANT INSERT (actor_type, actor_id, actor_name, key_id, action, resource_type, resource_id, outcome, request_id, reason, detail)
    ON pandokos.platform_audit_event TO pandokos_worker;

-- A credential names a provider of the catalogue (sprint 2.1, section 11).
ALTER TABLE pandokos.provider_credential ADD CONSTRAINT provider_credential_provider_fkey
    FOREIGN KEY (provider) REFERENCES pandokos.provider (name);
```

## Appendix B — additions to `api/openapi/admin.v1.yaml`

**Tags**: insert `- name: catalog`, `- name: prices`, `- name: model-access` before
`- name: platform-keys`.
**`ProblemCode.enum`**: append `price_source_unavailable`, `price_dataset_invalid`.
**`VirtualKey.properties`**: after `credential_id`, add

```yaml
        models:
          type: array
          description: Models the key is limited to, as provider/name. Absent means every model available to the tenant.
          items: {$ref: '#/components/schemas/ModelRef'}
```

**`VirtualKeyCreate.properties`**: after `credential_id`, add

```yaml
        models:
          type: array
          minItems: 1
          maxItems: 64
          uniqueItems: true
          items: {$ref: '#/components/schemas/ModelRef'}
```

**`components.parameters`**, inserted before `Limit:`

```yaml
    Provider:
      name: provider
      in: path
      required: true
      description: Name of a provider.
      schema: {$ref: '#/components/schemas/ProviderName'}
    Endpoint:
      name: endpoint
      in: path
      required: true
      description: Name of an endpoint of the provider.
      schema: {$ref: '#/components/schemas/Name'}
    Model:
      name: model
      in: path
      required: true
      description: Model ID.
      schema: {$ref: '#/components/schemas/ID'}
    PriceVersion:
      name: price_version
      in: path
      required: true
      description: Price version ID.
      schema: {$ref: '#/components/schemas/ID'}
    PriceImport:
      name: price_import
      in: path
      required: true
      description: Price import ID.
      schema: {$ref: '#/components/schemas/ID'}
```

**`components.responses`**, inserted before `schemas:`

```yaml
    Provider:
      description: The provider.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/Provider'}
    ProviderCreated:
      description: The new provider.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/Provider'}
    ProviderList:
      description: One page of providers.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/ProviderList'}
    Endpoint:
      description: The endpoint.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/Endpoint'}
    EndpointCreated:
      description: The new endpoint.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/Endpoint'}
    EndpointList:
      description: One page of endpoints.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/EndpointList'}
    Model:
      description: The model.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/Model'}
    ModelCreated:
      description: The new model.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/Model'}
    ModelList:
      description: One page of models.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/ModelList'}
    ModelGrant:
      description: The grant.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/ModelGrant'}
    ModelGrantList:
      description: One page of grants.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/ModelGrantList'}
    PriceVersion:
      description: The price version.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/PriceVersion'}
    PriceVersionCreated:
      description: The new price version.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/PriceVersion'}
    PriceVersionList:
      description: One page of price versions.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/PriceVersionList'}
    PriceImport:
      description: The import, with its report.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/PriceImport'}
    PriceImportCreated:
      description: The import that ran, with its report.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/PriceImport'}
    PriceImportList:
      description: One page of imports (without reports).
      content:
        application/json:
          schema: {$ref: '#/components/schemas/PriceImportList'}
    TenantModelList:
      description: One page of the models available to the tenant.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/TenantModelList'}
    ModelRestriction:
      description: The model restriction of the tenant.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/ModelRestriction'}
    PriceSourceUnavailable:
      description: 'The price dataset could not be downloaded; nothing was imported. Code: price_source_unavailable.'
      content:
        application/problem+json:
          schema: {$ref: '#/components/schemas/Problem'}
    PriceDatasetInvalid:
      description: 'The price dataset could not be read; nothing was imported. Code: price_dataset_invalid.'
      content:
        application/problem+json:
          schema: {$ref: '#/components/schemas/Problem'}
```

**`components.schemas`**, inserted before `ID:`

```yaml
    ApiFamily:
      type: string
      description: The wire protocol of a provider.
      enum: [openai, anthropic]
    ModelName:
      type: string
      description: 'The public name callers send: letters, digits and . _ : @ / + -, 1 to 128 characters.'
      pattern: '^[A-Za-z0-9][A-Za-z0-9._:@/+-]{0,127}$'
    ModelRef:
      type: string
      description: A model named as provider/name.
      pattern: '^[a-z][a-z0-9-]{0,62}/[A-Za-z0-9][A-Za-z0-9._:@/+-]{0,127}$'
    ModelStatus:
      type: string
      description: active; deprecated (still callable, flagged); retired (callable by nobody).
      enum: [active, deprecated, retired]
    ModelAvailability:
      type: string
      description: all tenants, or the tenants the model is granted to.
      enum: [all, selected]
    PriceSource:
      type: string
      enum: [import, override]
    PriceCategory:
      type: string
      description: The units a rate applies to.
      enum: [input, output, cache_read, cache_write, cache_write_1h, input_audio, output_audio]
    PriceImportSource:
      type: string
      enum: [embedded, file, remote]
    PriceImportRunSource:
      type: string
      enum: [embedded, remote]
    Provider:
      type: object
      additionalProperties: false
      required: [id, name, display_name, description, api_family, enabled, dataset_provider, created_at, updated_at]
      properties:
        id: {$ref: '#/components/schemas/ID'}
        name: {$ref: '#/components/schemas/ProviderName'}
        display_name: {type: string}
        description: {type: string}
        api_family: {$ref: '#/components/schemas/ApiFamily'}
        enabled: {type: boolean}
        dataset_provider: {type: string, description: Identifier of the provider in the price dataset; empty when its prices are not imported.}
        created_at: {type: string, format: date-time}
        updated_at: {type: string, format: date-time}
    ProviderCreate:
      type: object
      additionalProperties: false
      required: [name, api_family]
      properties:
        name: {$ref: '#/components/schemas/ProviderName'}
        api_family: {$ref: '#/components/schemas/ApiFamily'}
        display_name: {type: string, maxLength: 200}
        description: {type: string, maxLength: 500}
        dataset_provider: {type: string, pattern: '^[a-z0-9][a-z0-9_.-]{0,62}$'}
    ProviderUpdate:
      type: object
      additionalProperties: false
      properties:
        display_name: {type: string, maxLength: 200}
        description: {type: string, maxLength: 500}
        enabled: {type: boolean}
        dataset_provider: {type: string, pattern: '^([a-z0-9][a-z0-9_.-]{0,62})?$', description: Empty stops imports for the provider.}
    ProviderList:
      type: object
      additionalProperties: false
      required: [items]
      properties:
        items:
          type: array
          items: {$ref: '#/components/schemas/Provider'}
        next_cursor: {type: string, description: Absent on the last page.}
    Endpoint:
      type: object
      additionalProperties: false
      required: [id, provider, name, base_url, region, private, enabled, is_default, description, created_at, updated_at]
      properties:
        id: {$ref: '#/components/schemas/ID'}
        provider: {$ref: '#/components/schemas/ProviderName'}
        name: {$ref: '#/components/schemas/Name'}
        base_url: {type: string, description: The native path of a request is appended to it.}
        region: {type: string}
        private: {type: boolean, description: Declared by the platform administrator to be on a private network.}
        enabled: {type: boolean}
        is_default: {type: boolean, description: The endpoint the gateway uses for the provider.}
        description: {type: string}
        created_at: {type: string, format: date-time}
        updated_at: {type: string, format: date-time}
    EndpointCreate:
      type: object
      additionalProperties: false
      required: [name, base_url]
      properties:
        name: {$ref: '#/components/schemas/Name'}
        base_url: {type: string, minLength: 8, maxLength: 2048, pattern: '^https?://[^/?#@\s]+(/[^?#\s]*)?$'}
        region: {type: string, pattern: '^[a-z0-9][a-z0-9-]{0,62}$'}
        private: {type: boolean}
        is_default: {type: boolean}
        description: {type: string, maxLength: 500}
    EndpointUpdate:
      type: object
      additionalProperties: false
      properties:
        base_url: {type: string, minLength: 8, maxLength: 2048, pattern: '^https?://[^/?#@\s]+(/[^?#\s]*)?$'}
        region: {type: string, pattern: '^([a-z0-9][a-z0-9-]{0,62})?$'}
        private: {type: boolean}
        enabled: {type: boolean}
        is_default: {type: boolean, description: true makes this the default; false is refused for the default (make another one the default instead).}
        description: {type: string, maxLength: 500}
    EndpointList:
      type: object
      additionalProperties: false
      required: [items]
      properties:
        items:
          type: array
          items: {$ref: '#/components/schemas/Endpoint'}
        next_cursor: {type: string, description: Absent on the last page.}
    Model:
      type: object
      additionalProperties: false
      required: [id, provider, api_family, name, ref, upstream_id, display_name, description, status, availability, capabilities, context_window, max_output_tokens, price_override, created_at, updated_at]
      properties:
        id: {$ref: '#/components/schemas/ID'}
        provider: {$ref: '#/components/schemas/ProviderName'}
        api_family: {$ref: '#/components/schemas/ApiFamily'}
        name: {$ref: '#/components/schemas/ModelName'}
        ref: {$ref: '#/components/schemas/ModelRef'}
        upstream_id: {type: string, description: The identifier the provider receives.}
        display_name: {type: string}
        description: {type: string}
        status: {$ref: '#/components/schemas/ModelStatus'}
        availability: {$ref: '#/components/schemas/ModelAvailability'}
        capabilities:
          type: array
          items: {type: string}
        context_window: {type: integer, description: In tokens; 0 when not known.}
        max_output_tokens: {type: integer, description: In tokens; 0 when not known.}
        price_override: {type: boolean, description: An operator price is in place and imports leave the model alone.}
        created_at: {type: string, format: date-time}
        updated_at: {type: string, format: date-time}
    ModelCreate:
      type: object
      additionalProperties: false
      required: [provider, name]
      properties:
        provider: {$ref: '#/components/schemas/ProviderName'}
        name: {$ref: '#/components/schemas/ModelName'}
        upstream_id: {type: string, maxLength: 256, pattern: '^[!-~]+$'}
        display_name: {type: string, maxLength: 200}
        description: {type: string, maxLength: 1000}
        status: {$ref: '#/components/schemas/ModelStatus'}
        availability: {$ref: '#/components/schemas/ModelAvailability'}
        capabilities:
          type: array
          maxItems: 8
          uniqueItems: true
          items: {type: string, pattern: '^(text|vision|audio|tools|reasoning|embeddings|structured_output|streaming)$'}
        context_window: {type: integer, minimum: 0, maximum: 100000000}
        max_output_tokens: {type: integer, minimum: 0, maximum: 100000000}
    ModelUpdate:
      type: object
      additionalProperties: false
      properties:
        upstream_id: {type: string, maxLength: 256, pattern: '^[!-~]+$'}
        display_name: {type: string, maxLength: 200}
        description: {type: string, maxLength: 1000}
        status: {$ref: '#/components/schemas/ModelStatus'}
        availability: {$ref: '#/components/schemas/ModelAvailability'}
        capabilities:
          type: array
          maxItems: 8
          uniqueItems: true
          items: {type: string, pattern: '^(text|vision|audio|tools|reasoning|embeddings|structured_output|streaming)$'}
        context_window: {type: integer, minimum: 0, maximum: 100000000, description: 0 clears it.}
        max_output_tokens: {type: integer, minimum: 0, maximum: 100000000, description: 0 clears it.}
    ModelList:
      type: object
      additionalProperties: false
      required: [items]
      properties:
        items:
          type: array
          items: {$ref: '#/components/schemas/Model'}
        next_cursor: {type: string, description: Absent on the last page.}
    ModelGrant:
      type: object
      additionalProperties: false
      required: [model_id, tenant_id, tenant_name, created_at]
      properties:
        model_id: {$ref: '#/components/schemas/ID'}
        tenant_id: {$ref: '#/components/schemas/ID'}
        tenant_name: {$ref: '#/components/schemas/Name'}
        created_at: {type: string, format: date-time}
    ModelGrantList:
      type: object
      additionalProperties: false
      required: [items]
      properties:
        items:
          type: array
          items: {$ref: '#/components/schemas/ModelGrant'}
        next_cursor: {type: string, description: Absent on the last page.}
    PriceRate:
      type: object
      additionalProperties: false
      required: [category, threshold, rate]
      properties:
        category: {$ref: '#/components/schemas/PriceCategory'}
        threshold: {type: integer, minimum: 0, maximum: 100000000, description: 'Input tokens of a request from which this rate applies to every unit of the category; 0 for the base rate.'}
        rate: {type: string, pattern: '^(0|[1-9][0-9]{0,6})(\.[0-9]{1,12})?$', description: 'Price per million units, as a decimal string, in the currency of the version.'}
    PriceVersion:
      type: object
      additionalProperties: false
      required: [id, model_id, valid_from, currency, source, note, rates, created_at]
      properties:
        id: {$ref: '#/components/schemas/ID'}
        model_id: {$ref: '#/components/schemas/ID'}
        valid_from: {type: string, format: date-time}
        currency: {type: string, description: ISO 4217 code.}
        source: {$ref: '#/components/schemas/PriceSource'}
        import_id: {$ref: '#/components/schemas/ID'}
        note: {type: string}
        rates:
          type: array
          items: {$ref: '#/components/schemas/PriceRate'}
        created_at: {type: string, format: date-time}
    PriceVersionList:
      type: object
      additionalProperties: false
      required: [items]
      properties:
        items:
          type: array
          items: {$ref: '#/components/schemas/PriceVersion'}
        next_cursor: {type: string, description: Absent on the last page.}
    PriceOverride:
      type: object
      additionalProperties: false
      required: [currency, rates]
      properties:
        currency: {type: string, pattern: '^[A-Z]{3}$'}
        valid_from: {type: string, format: date-time, description: Now when absent; never in the past.}
        note: {type: string, maxLength: 500}
        rates:
          type: array
          minItems: 1
          maxItems: 64
          items: {$ref: '#/components/schemas/PriceRate'}
    PriceImportIssue:
      type: object
      additionalProperties: false
      required: [reason]
      properties:
        reason: {type: string}
        detail: {type: string}
    PriceImportItem:
      type: object
      additionalProperties: false
      required: [provider, model, outcome]
      properties:
        provider: {$ref: '#/components/schemas/ProviderName'}
        model: {$ref: '#/components/schemas/ModelName'}
        dataset_model: {type: string}
        outcome: {type: string, description: 'created, unchanged, overridden, unmatched or skipped.'}
        issues:
          type: array
          items: {$ref: '#/components/schemas/PriceImportIssue'}
    PriceImport:
      type: object
      additionalProperties: false
      required: [source, dataset_format, outcome, models_seen, versions_created, unchanged, overridden, unmatched, skipped, started_at]
      properties:
        id: {$ref: '#/components/schemas/ID'}
        source: {$ref: '#/components/schemas/PriceImportSource'}
        source_ref: {type: string}
        dataset_format: {type: string}
        digest: {type: string, description: SHA-256 of the dataset, hexadecimal.}
        outcome: {type: string, description: success or failure.}
        error_class: {type: string}
        actor_type: {type: string}
        models_seen: {type: integer}
        versions_created: {type: integer}
        unchanged: {type: integer}
        overridden: {type: integer}
        unmatched: {type: integer}
        skipped: {type: integer}
        dry_run: {type: boolean}
        items:
          type: array
          description: The report; absent in lists.
          items: {$ref: '#/components/schemas/PriceImportItem'}
        truncated: {type: integer, description: Report items left out because the report reached its size limit.}
        started_at: {type: string, format: date-time}
        created_at: {type: string, format: date-time}
    PriceImportRun:
      type: object
      additionalProperties: false
      required: [source]
      properties:
        source: {$ref: '#/components/schemas/PriceImportRunSource'}
        dry_run: {type: boolean}
    PriceImportList:
      type: object
      additionalProperties: false
      required: [items]
      properties:
        items:
          type: array
          items: {$ref: '#/components/schemas/PriceImport'}
        next_cursor: {type: string, description: Absent on the last page.}
    TenantModel:
      type: object
      additionalProperties: false
      required: [model, allowed]
      properties:
        model: {$ref: '#/components/schemas/Model'}
        allowed: {type: boolean, description: false when the tenant's restriction leaves the model out.}
        price: {$ref: '#/components/schemas/PriceVersion'}
    TenantModelList:
      type: object
      additionalProperties: false
      required: [items]
      properties:
        items:
          type: array
          items: {$ref: '#/components/schemas/TenantModel'}
        next_cursor: {type: string, description: Absent on the last page.}
    ModelRestriction:
      type: object
      additionalProperties: false
      required: [restricted, models]
      properties:
        restricted: {type: boolean, description: false when the tenant has no restriction.}
        models:
          type: array
          items: {$ref: '#/components/schemas/ModelRef'}
        updated_at: {type: string, format: date-time}
    ModelRestrictionSet:
      type: object
      additionalProperties: false
      required: [models]
      properties:
        models:
          type: array
          maxItems: 512
          uniqueItems: true
          items: {$ref: '#/components/schemas/ModelRef'}
```

**Paths**, inserted before `/v1/platform-keys:`. To keep this appendix short each operation shows
its success response and, in the comment, the other codes; the implementer writes the full
mapping, every code referring to the response of the same meaning that the document already has
(`400 InvalidRequest`, `401 Unauthenticated`, `403 Forbidden`, `404 NotFound`, `409 Conflict`,
`413 RequestTooLarge`, `415 UnsupportedMediaType`, `500 Internal`, `503 Unavailable`, and
`422 PriceDatasetInvalid`, `502 PriceSourceUnavailable` for `runPriceImport`) and removes the
comment. The expanded document was generated by `internal/apigen` while designing (64 operations).

```yaml
  /v1/providers:
    get:
      operationId: listProviders
      tags: [catalog]
      summary: List the providers of the catalogue, by name
      x-pandokos-permission: catalog:read
      parameters:
        - {$ref: '#/components/parameters/Limit'}
        - {$ref: '#/components/parameters/Cursor'}
      responses: {"200": {$ref: '#/components/responses/ProviderList'}}  # + 400, 401, 403, 404, 500, 503
    post:
      operationId: createProvider
      tags: [catalog]
      summary: Add a provider
      description: >-
        The name is the first path segment of the inference listener and never changes; healthz, readyz and v1 are refused. The provider is created enabled.
      x-pandokos-permission: catalog:write
      requestBody: {required: true, content: {application/json: {schema: {$ref: '#/components/schemas/ProviderCreate'}}}}
      responses: {"201": {$ref: '#/components/responses/ProviderCreated'}}  # + 400, 409, 413/415, 401, 403, 404, 500, 503
  /v1/providers/{provider}:
    parameters:
      - {$ref: '#/components/parameters/Provider'}
    get:
      operationId: getProvider
      tags: [catalog]
      summary: Read a provider
      x-pandokos-permission: catalog:read
      responses: {"200": {$ref: '#/components/responses/Provider'}}  # + 401, 403, 404, 500, 503
    patch:
      operationId: updateProvider
      tags: [catalog]
      summary: Change, enable or disable a provider
      x-pandokos-permission: catalog:write
      requestBody: {required: true, content: {application/json: {schema: {$ref: '#/components/schemas/ProviderUpdate'}}}}
      responses: {"200": {$ref: '#/components/responses/Provider'}}  # + 400, 409, 413/415, 401, 403, 404, 500, 503
    delete:
      operationId: deleteProvider
      tags: [catalog]
      summary: Delete a provider that nothing references
      description: >-
        Refused with invalid_state while an endpoint, a model or a credential of any tenant references the provider. The response is the provider as it was.
      x-pandokos-permission: catalog:write
      responses: {"200": {$ref: '#/components/responses/Provider'}}  # + 409, 401, 403, 404, 500, 503
  /v1/providers/{provider}/endpoints:
    parameters:
      - {$ref: '#/components/parameters/Provider'}
    get:
      operationId: listEndpoints
      tags: [catalog]
      summary: List the endpoints of a provider, by name
      x-pandokos-permission: catalog:read
      parameters:
        - {$ref: '#/components/parameters/Limit'}
        - {$ref: '#/components/parameters/Cursor'}
      responses: {"200": {$ref: '#/components/responses/EndpointList'}}  # + 400, 401, 403, 404, 500, 503
    post:
      operationId: createEndpoint
      tags: [catalog]
      summary: Add an endpoint to a provider
      description: >-
        The first endpoint of a provider becomes its default. http is accepted only for an endpoint marked private, and a public endpoint cannot name a private IP address.
      x-pandokos-permission: catalog:write
      requestBody: {required: true, content: {application/json: {schema: {$ref: '#/components/schemas/EndpointCreate'}}}}
      responses: {"201": {$ref: '#/components/responses/EndpointCreated'}}  # + 400, 409, 413/415, 401, 403, 404, 500, 503
  /v1/providers/{provider}/endpoints/{endpoint}:
    parameters:
      - {$ref: '#/components/parameters/Provider'}
      - {$ref: '#/components/parameters/Endpoint'}
    get:
      operationId: getEndpoint
      tags: [catalog]
      summary: Read an endpoint
      x-pandokos-permission: catalog:read
      responses: {"200": {$ref: '#/components/responses/Endpoint'}}  # + 401, 403, 404, 500, 503
    patch:
      operationId: updateEndpoint
      tags: [catalog]
      summary: Change an endpoint or make it the default
      x-pandokos-permission: catalog:write
      requestBody: {required: true, content: {application/json: {schema: {$ref: '#/components/schemas/EndpointUpdate'}}}}
      responses: {"200": {$ref: '#/components/responses/Endpoint'}}  # + 400, 409, 413/415, 401, 403, 404, 500, 503
    delete:
      operationId: deleteEndpoint
      tags: [catalog]
      summary: Delete an endpoint
      description: >-
        Refused with invalid_state for the default endpoint while the provider has another endpoint. The response is the endpoint as it was.
      x-pandokos-permission: catalog:write
      responses: {"200": {$ref: '#/components/responses/Endpoint'}}  # + 409, 401, 403, 404, 500, 503
  /v1/models:
    get:
      operationId: listModels
      tags: [catalog]
      summary: List the models of the catalogue
      x-pandokos-permission: catalog:read
      parameters:
        - {$ref: '#/components/parameters/Limit'}
        - {$ref: '#/components/parameters/Cursor'}
        - {$ref: '#/components/parameters/Sort'}
        - {name: provider, in: query, description: Only models of this provider., schema: {$ref: '#/components/schemas/ProviderName'}}
        - {name: name, in: query, description: Only the model with this public name., schema: {$ref: '#/components/schemas/ModelName'}}
        - {name: status, in: query, description: Only models with this status., schema: {$ref: '#/components/schemas/ModelStatus'}}
        - {name: availability, in: query, description: Only models with this availability., schema: {$ref: '#/components/schemas/ModelAvailability'}}
      responses: {"200": {$ref: '#/components/responses/ModelList'}}  # + 400, 401, 403, 404, 500, 503
    post:
      operationId: createModel
      tags: [catalog]
      summary: Add a model
      description: >-
        availability defaults to selected: nobody may use the model until it is granted or made available to all. upstream_id defaults to the name.
      x-pandokos-permission: catalog:write
      requestBody: {required: true, content: {application/json: {schema: {$ref: '#/components/schemas/ModelCreate'}}}}
      responses: {"201": {$ref: '#/components/responses/ModelCreated'}}  # + 400, 409, 413/415, 401, 403, 404, 500, 503
  /v1/models/{model}:
    parameters:
      - {$ref: '#/components/parameters/Model'}
    get:
      operationId: getModel
      tags: [catalog]
      summary: Read a model
      x-pandokos-permission: catalog:read
      responses: {"200": {$ref: '#/components/responses/Model'}}  # + 401, 403, 404, 500, 503
    patch:
      operationId: updateModel
      tags: [catalog]
      summary: Change a model
      x-pandokos-permission: catalog:write
      requestBody: {required: true, content: {application/json: {schema: {$ref: '#/components/schemas/ModelUpdate'}}}}
      responses: {"200": {$ref: '#/components/responses/Model'}}  # + 400, 409, 413/415, 401, 403, 404, 500, 503
    delete:
      operationId: deleteModel
      tags: [catalog]
      summary: Delete a model that has no price
      description: >-
        Refused with invalid_state once a price version exists: retire the model instead. Grants are removed with it. The response is the model as it was.
      x-pandokos-permission: catalog:write
      responses: {"200": {$ref: '#/components/responses/Model'}}  # + 409, 401, 403, 404, 500, 503
  /v1/models/{model}/grants:
    parameters:
      - {$ref: '#/components/parameters/Model'}
    get:
      operationId: listModelGrants
      tags: [catalog]
      summary: List the tenants a model is granted to
      x-pandokos-permission: catalog:read
      parameters:
        - {$ref: '#/components/parameters/Limit'}
        - {$ref: '#/components/parameters/Cursor'}
      responses: {"200": {$ref: '#/components/responses/ModelGrantList'}}  # + 400, 401, 403, 404, 500, 503
  /v1/models/{model}/grants/{tenant}:
    parameters:
      - {$ref: '#/components/parameters/Model'}
      - {$ref: '#/components/parameters/Tenant'}
    put:
      operationId: grantModel
      tags: [catalog]
      summary: Grant a model to a tenant
      description: >-
        Idempotent. The grant matters while the availability of the model is selected.
      x-pandokos-permission: catalog:write
      responses: {"200": {$ref: '#/components/responses/ModelGrant'}}  # + 401, 403, 404, 500, 503
    delete:
      operationId: revokeModelGrant
      tags: [catalog]
      summary: Revoke the grant of a model to a tenant
      description: >-
        The response is the grant as it was; not_found when there is none.
      x-pandokos-permission: catalog:write
      responses: {"200": {$ref: '#/components/responses/ModelGrant'}}  # + 401, 403, 404, 500, 503
  /v1/models/{model}/prices:
    parameters:
      - {$ref: '#/components/parameters/Model'}
    get:
      operationId: listPriceVersions
      tags: [prices]
      summary: List the price versions of a model, newest first
      x-pandokos-permission: catalog:read
      parameters:
        - {$ref: '#/components/parameters/Limit'}
        - {$ref: '#/components/parameters/Cursor'}
        - {name: at, in: query, description: 'An RFC 3339 time: only the version in force then (at most one item).', schema: {type: string, minLength: 1, maxLength: 64}}
      responses: {"200": {$ref: '#/components/responses/PriceVersionList'}}  # + 400, 401, 403, 404, 500, 503
    post:
      operationId: createPriceOverride
      tags: [prices]
      summary: Set an operator price for a model
      description: >-
        Adds a version with source override that starts at valid_from (now when absent; never in the past). From then on imports leave the model alone until clearPriceOverride.
      x-pandokos-permission: catalog:price
      requestBody: {required: true, content: {application/json: {schema: {$ref: '#/components/schemas/PriceOverride'}}}}
      responses: {"201": {$ref: '#/components/responses/PriceVersionCreated'}}  # + 400, 409, 413/415, 401, 403, 404, 500, 503
  /v1/models/{model}/prices/{price_version}:
    parameters:
      - {$ref: '#/components/parameters/Model'}
      - {$ref: '#/components/parameters/PriceVersion'}
    delete:
      operationId: deletePriceVersion
      tags: [prices]
      summary: Remove a price version that has not started
      description: >-
        Refused with invalid_state once the version is in force or past: versions are immutable. The response is the version as it was.
      x-pandokos-permission: catalog:price
      responses: {"200": {$ref: '#/components/responses/PriceVersion'}}  # + 401, 403, 404, 500, 503
  /v1/models/{model}/clear-price-override:
    parameters:
      - {$ref: '#/components/parameters/Model'}
    post:
      operationId: clearPriceOverride
      tags: [prices]
      summary: Let imports price the model again
      description: >-
        Refused with invalid_state while a version of the model has not started. The override in force stays in force until an import creates a version.
      x-pandokos-permission: catalog:price
      requestBody: {required: false, content: {application/json: {schema: {$ref: '#/components/schemas/Reason'}}}}
      responses: {"200": {$ref: '#/components/responses/Model'}}  # + 400, 413/415, 401, 403, 404, 500, 503
  /v1/price-imports:
    get:
      operationId: listPriceImports
      tags: [prices]
      summary: List price imports, newest first
      x-pandokos-permission: catalog:read
      parameters:
        - {$ref: '#/components/parameters/Limit'}
        - {$ref: '#/components/parameters/Cursor'}
      responses: {"200": {$ref: '#/components/responses/PriceImportList'}}  # + 400, 401, 403, 404, 500, 503
    post:
      operationId: runPriceImport
      tags: [prices]
      summary: Import prices now
      description: >-
        embedded imports the snapshot of this release; remote downloads pricing.refresh.url and is refused unless pricing.refresh.enabled. With dry_run nothing is written and the response has no id.
      x-pandokos-permission: catalog:price
      requestBody: {required: true, content: {application/json: {schema: {$ref: '#/components/schemas/PriceImportRun'}}}}
      responses: {"201": {$ref: '#/components/responses/PriceImportCreated'}}  # + 400, 413/415, 422 PriceDatasetInvalid, 502 PriceSourceUnavailable, 401, 403, 404, 500, 503
  /v1/price-imports/{price_import}:
    parameters:
      - {$ref: '#/components/parameters/PriceImport'}
    get:
      operationId: getPriceImport
      tags: [prices]
      summary: Read a price import with its report
      x-pandokos-permission: catalog:read
      responses: {"200": {$ref: '#/components/responses/PriceImport'}}  # + 401, 403, 404, 500, 503
  /v1/tenants/{tenant}/models:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: listTenantModels
      tags: [model-access]
      summary: List the models available to the tenant
      description: >-
        Models the platform made available to the tenant, with allowed false for those outside the tenant's restriction, and the price in force now.
      x-pandokos-permission: model:read
      parameters:
        - {$ref: '#/components/parameters/Limit'}
        - {$ref: '#/components/parameters/Cursor'}
        - {name: provider, in: query, description: Only models of this provider., schema: {$ref: '#/components/schemas/ProviderName'}}
      responses: {"200": {$ref: '#/components/responses/TenantModelList'}}  # + 400, 401, 403, 404, 500, 503
  /v1/tenants/{tenant}/model-restriction:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: getModelRestriction
      tags: [model-access]
      summary: Read the model restriction of the tenant
      x-pandokos-permission: model:read
      responses: {"200": {$ref: '#/components/responses/ModelRestriction'}}  # + 401, 403, 404, 500, 503
    put:
      operationId: setModelRestriction
      tags: [model-access]
      summary: Restrict the tenant to the models named
      description: >-
        Every reference must name a model available to the tenant. An empty list allows no model.
      x-pandokos-permission: model:restrict
      requestBody: {required: true, content: {application/json: {schema: {$ref: '#/components/schemas/ModelRestrictionSet'}}}}
      responses: {"200": {$ref: '#/components/responses/ModelRestriction'}}  # + 400, 413/415, 401, 403, 404, 500, 503
    delete:
      operationId: clearModelRestriction
      tags: [model-access]
      summary: Remove the model restriction of the tenant
      x-pandokos-permission: model:restrict
      responses: {"200": {$ref: '#/components/responses/ModelRestriction'}}  # + 401, 403, 404, 500, 503
```

## Appendix C — query files and `sqlc.yaml`

### `sqlc.yaml` (changed): append to `overrides`

```yaml
          - db_type: pg_catalog.numeric
            go_type: string
          - db_type: pg_catalog.numeric
            nullable: true
            go_type: {type: string, pointer: true}
```

### `internal/store/queries/sql/catalog.sql` (new)

```sql
-- The catalogue: providers, endpoints, models, grants of models to tenants, the model restriction
-- of a tenant. Statements that write providers, endpoints, models and grants run in platform
-- scope (store.DB.Platform); the Load statements and the reads of providers and models run in
-- any session; ListTenantGrants, ListVisibleModels and the restriction statements run in a
-- tenant session.

-- name: ListProviders :many
SELECT p.id, p.name, p.display_name, p.description, p.api_family, p.enabled, p.dataset_provider, p.created_at, p.updated_at
  FROM pandokos.provider p
 WHERE (NOT sqlc.arg(after)::boolean OR p.name > sqlc.arg(after_name)::text)
 ORDER BY p.name
 LIMIT sqlc.arg(page_size)::integer;

-- name: GetProvider :one
SELECT p.id, p.name, p.display_name, p.description, p.api_family, p.enabled, p.dataset_provider, p.created_at, p.updated_at
  FROM pandokos.provider p
 WHERE p.name = sqlc.arg(name)::text;

-- name: CreateProvider :one
INSERT INTO pandokos.provider (name, display_name, description, api_family, enabled, dataset_provider)
VALUES (sqlc.arg(name)::text, sqlc.arg(display_name)::text, sqlc.arg(description)::text, sqlc.arg(api_family)::text,
        sqlc.arg(enabled)::boolean, sqlc.arg(dataset_provider)::text)
RETURNING id, name, display_name, description, api_family, enabled, dataset_provider, created_at, updated_at;

-- name: UpdateProvider :one
UPDATE pandokos.provider
   SET display_name     = CASE WHEN sqlc.arg(set_display_name)::boolean THEN sqlc.arg(display_name)::text ELSE display_name END,
       description      = CASE WHEN sqlc.arg(set_description)::boolean THEN sqlc.arg(description)::text ELSE description END,
       enabled          = CASE WHEN sqlc.arg(set_enabled)::boolean THEN sqlc.arg(enabled)::boolean ELSE enabled END,
       dataset_provider = CASE WHEN sqlc.arg(set_dataset_provider)::boolean THEN sqlc.arg(dataset_provider)::text ELSE dataset_provider END
 WHERE name = sqlc.arg(name)::text
RETURNING id, name, display_name, description, api_family, enabled, dataset_provider, created_at, updated_at;

-- name: DeleteProvider :one
DELETE FROM pandokos.provider
 WHERE name = sqlc.arg(name)::text
RETURNING id, name, display_name, description, api_family, enabled, dataset_provider, created_at, updated_at;

-- name: ListEndpoints :many
SELECT e.id, e.provider_id, e.name, e.base_url, e.region, e.private, e.enabled, e.is_default, e.description, e.created_at, e.updated_at
  FROM pandokos.provider_endpoint e
 WHERE e.provider_id = sqlc.arg(provider_id)::uuid
   AND (NOT sqlc.arg(after)::boolean OR e.name > sqlc.arg(after_name)::text)
 ORDER BY e.name
 LIMIT sqlc.arg(page_size)::integer;

-- name: GetEndpoint :one
SELECT e.id, e.provider_id, e.name, e.base_url, e.region, e.private, e.enabled, e.is_default, e.description, e.created_at, e.updated_at
  FROM pandokos.provider_endpoint e
 WHERE e.provider_id = sqlc.arg(provider_id)::uuid AND e.name = sqlc.arg(name)::text;

-- name: CreateEndpoint :one
INSERT INTO pandokos.provider_endpoint (provider_id, name, base_url, region, private, enabled, is_default, description)
VALUES (sqlc.arg(provider_id)::uuid, sqlc.arg(name)::text, sqlc.arg(base_url)::text, sqlc.arg(region)::text,
        sqlc.arg(private)::boolean, sqlc.arg(enabled)::boolean,
        sqlc.arg(is_default)::boolean OR NOT EXISTS (SELECT 1 FROM pandokos.provider_endpoint d
                                                     WHERE d.provider_id = sqlc.arg(provider_id)::uuid AND d.is_default),
        sqlc.arg(description)::text)
RETURNING id, provider_id, name, base_url, region, private, enabled, is_default, description, created_at, updated_at;

-- name: ClearDefaultEndpoint :execrows
UPDATE pandokos.provider_endpoint
   SET is_default = false
 WHERE provider_id = sqlc.arg(provider_id)::uuid AND is_default AND name <> sqlc.arg(keep)::text;

-- name: UpdateEndpoint :one
UPDATE pandokos.provider_endpoint
   SET base_url    = CASE WHEN sqlc.arg(set_base_url)::boolean THEN sqlc.arg(base_url)::text ELSE base_url END,
       region      = CASE WHEN sqlc.arg(set_region)::boolean THEN sqlc.arg(region)::text ELSE region END,
       private     = CASE WHEN sqlc.arg(set_private)::boolean THEN sqlc.arg(private)::boolean ELSE private END,
       enabled     = CASE WHEN sqlc.arg(set_enabled)::boolean THEN sqlc.arg(enabled)::boolean ELSE enabled END,
       is_default  = CASE WHEN sqlc.arg(set_is_default)::boolean THEN sqlc.arg(is_default)::boolean ELSE is_default END,
       description = CASE WHEN sqlc.arg(set_description)::boolean THEN sqlc.arg(description)::text ELSE description END
 WHERE provider_id = sqlc.arg(provider_id)::uuid AND name = sqlc.arg(name)::text
RETURNING id, provider_id, name, base_url, region, private, enabled, is_default, description, created_at, updated_at;

-- name: DeleteEndpoint :one
DELETE FROM pandokos.provider_endpoint
 WHERE provider_id = sqlc.arg(provider_id)::uuid AND name = sqlc.arg(name)::text
RETURNING id, provider_id, name, base_url, region, private, enabled, is_default, description, created_at, updated_at;

-- name: ListModels :many
SELECT m.id, p.name AS provider, p.api_family, m.name, m.upstream_id, m.display_name, m.description, m.status,
       m.availability, m.capabilities, m.context_window, m.max_output_tokens, m.price_override, m.created_at, m.updated_at
  FROM pandokos.model m
  JOIN pandokos.provider p ON p.id = m.provider_id
 WHERE (sqlc.arg(provider)::text = '' OR p.name = sqlc.arg(provider)::text)
   AND (sqlc.arg(name)::text = '' OR m.name = sqlc.arg(name)::text)
   AND (sqlc.arg(status)::text = '' OR m.status = sqlc.arg(status)::text)
   AND (sqlc.arg(availability)::text = '' OR m.availability = sqlc.arg(availability)::text)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN m.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN m.id < sqlc.arg(after_id)::uuid
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN m.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN m.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: GetModel :one
SELECT m.id, p.name AS provider, p.api_family, m.name, m.upstream_id, m.display_name, m.description, m.status,
       m.availability, m.capabilities, m.context_window, m.max_output_tokens, m.price_override, m.created_at, m.updated_at
  FROM pandokos.model m
  JOIN pandokos.provider p ON p.id = m.provider_id
 WHERE m.id = sqlc.arg(id)::uuid;

-- name: LockModel :one
SELECT m.id, m.name, m.price_override
  FROM pandokos.model m
 WHERE m.id = sqlc.arg(id)::uuid
   FOR UPDATE;

-- name: CreateModel :one
INSERT INTO pandokos.model (provider_id, name, upstream_id, display_name, description, status, availability, capabilities,
                            context_window, max_output_tokens)
VALUES (sqlc.arg(provider_id)::uuid, sqlc.arg(name)::text, sqlc.arg(upstream_id)::text, sqlc.arg(display_name)::text,
        sqlc.arg(description)::text, sqlc.arg(status)::text, sqlc.arg(availability)::text, sqlc.arg(capabilities)::text[],
        sqlc.narg(context_window)::bigint, sqlc.narg(max_output_tokens)::bigint)
RETURNING id;

-- name: UpdateModel :execrows
UPDATE pandokos.model
   SET upstream_id       = CASE WHEN sqlc.arg(set_upstream_id)::boolean THEN sqlc.arg(upstream_id)::text ELSE upstream_id END,
       display_name      = CASE WHEN sqlc.arg(set_display_name)::boolean THEN sqlc.arg(display_name)::text ELSE display_name END,
       description       = CASE WHEN sqlc.arg(set_description)::boolean THEN sqlc.arg(description)::text ELSE description END,
       status            = CASE WHEN sqlc.arg(set_status)::boolean THEN sqlc.arg(status)::text ELSE status END,
       availability      = CASE WHEN sqlc.arg(set_availability)::boolean THEN sqlc.arg(availability)::text ELSE availability END,
       capabilities      = CASE WHEN sqlc.arg(set_capabilities)::boolean THEN sqlc.arg(capabilities)::text[] ELSE capabilities END,
       context_window    = CASE WHEN sqlc.arg(set_context_window)::boolean THEN sqlc.narg(context_window)::bigint ELSE context_window END,
       max_output_tokens = CASE WHEN sqlc.arg(set_max_output_tokens)::boolean THEN sqlc.narg(max_output_tokens)::bigint ELSE max_output_tokens END
 WHERE id = sqlc.arg(id)::uuid;

-- name: SetModelPriceOverride :execrows
UPDATE pandokos.model
   SET price_override = sqlc.arg(price_override)::boolean
 WHERE id = sqlc.arg(id)::uuid;

-- name: DeleteModel :execrows
DELETE FROM pandokos.model
 WHERE id = sqlc.arg(id)::uuid;

-- name: ListModelGrants :many
SELECT g.model_id, g.tenant_id, t.name AS tenant_name, g.created_at
  FROM pandokos.model_tenant_grant g
  JOIN pandokos.tenant t ON t.id = g.tenant_id
 WHERE g.model_id = sqlc.arg(model_id)::uuid
   AND (NOT sqlc.arg(after)::boolean OR g.tenant_id > sqlc.arg(after_id)::uuid)
 ORDER BY g.tenant_id
 LIMIT sqlc.arg(page_size)::integer;

-- name: GrantModel :execrows
INSERT INTO pandokos.model_tenant_grant (model_id, tenant_id)
VALUES (sqlc.arg(model_id)::uuid, sqlc.arg(tenant_id)::uuid)
ON CONFLICT (model_id, tenant_id) DO NOTHING;

-- name: GetModelGrant :one
SELECT g.model_id, g.tenant_id, t.name AS tenant_name, g.created_at
  FROM pandokos.model_tenant_grant g
  JOIN pandokos.tenant t ON t.id = g.tenant_id
 WHERE g.model_id = sqlc.arg(model_id)::uuid AND g.tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: RevokeModelGrant :execrows
DELETE FROM pandokos.model_tenant_grant
 WHERE model_id = sqlc.arg(model_id)::uuid AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ListTenantGrants :many
SELECT g.model_id
  FROM pandokos.model_tenant_grant g
 ORDER BY g.model_id;

-- name: ListVisibleModels :many
SELECT m.id, p.name AS provider, p.api_family, m.name, m.upstream_id, m.display_name, m.description, m.status,
       m.availability, m.capabilities, m.context_window, m.max_output_tokens, m.price_override, m.created_at, m.updated_at
  FROM pandokos.model m
  JOIN pandokos.provider p ON p.id = m.provider_id
 WHERE p.enabled
   AND m.status <> 'retired'
   AND (m.availability = 'all' OR EXISTS (SELECT 1 FROM pandokos.model_tenant_grant g WHERE g.model_id = m.id))
   AND (sqlc.arg(provider)::text = '' OR p.name = sqlc.arg(provider)::text)
   AND (NOT sqlc.arg(after)::boolean OR m.id > sqlc.arg(after_id)::uuid)
 ORDER BY m.id
 LIMIT sqlc.arg(page_size)::integer;

-- name: GetModelRestriction :one
SELECT r.models, r.updated_at
  FROM pandokos.tenant_model_restriction r;

-- name: SetModelRestriction :one
INSERT INTO pandokos.tenant_model_restriction (tenant_id, models)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(models)::text[])
ON CONFLICT (tenant_id) DO UPDATE SET models = EXCLUDED.models
RETURNING models, updated_at;

-- name: ClearModelRestriction :execrows
DELETE FROM pandokos.tenant_model_restriction;

-- name: LoadProviders :many
SELECT p.id, p.name, p.api_family, p.enabled, p.dataset_provider
  FROM pandokos.provider p
 ORDER BY p.name;

-- name: LoadEndpoints :many
SELECT e.provider_id, e.name, e.base_url, e.region, e.private, e.enabled, e.is_default
  FROM pandokos.provider_endpoint e
 ORDER BY e.provider_id, e.name;

-- name: LoadModels :many
SELECT m.id, m.provider_id, m.name, m.upstream_id, m.status, m.availability, m.context_window, m.max_output_tokens,
       m.price_override
  FROM pandokos.model m
 ORDER BY m.id;
```

### `internal/store/queries/sql/prices.sql` (new)

```sql
-- Prices: imports, versions and their rates. Writes run in platform scope (admin or worker
-- role); the reads of versions run in any session; the reads of imports in platform scope. A
-- version's rates come back as three parallel arrays ordered by category and threshold; rates
-- travel as decimal text, never as floating point.

-- name: LockPriceImports :exec
SELECT pg_catalog.pg_advisory_xact_lock(7203391045122417921);

-- name: PriceImportHistory :one
SELECT EXISTS (SELECT 1 FROM pandokos.price_import i
                WHERE i.digest = sqlc.arg(digest)::text AND i.outcome = 'success')::boolean AS digest_seen,
       EXISTS (SELECT 1 FROM pandokos.price_import i
                WHERE i.source IN ('file', 'remote') AND i.outcome = 'success'
                  AND i.created_at > sqlc.arg(since)::timestamptz)::boolean AS newer_import,
       COALESCE((SELECT max(i.created_at) FROM pandokos.price_import i
                  WHERE i.source = 'remote' AND i.outcome = 'success'), '-infinity'::timestamptz)::timestamptz AS last_remote;

-- name: CreatePriceImport :one
INSERT INTO pandokos.price_import (started_at, source, source_ref, dataset_format, digest, outcome, error_class, actor_type,
                                   actor_id, models_seen, versions_created, unchanged, overridden, unmatched, skipped, report)
VALUES (sqlc.arg(started_at)::timestamptz, sqlc.arg(source)::text, sqlc.arg(source_ref)::text, sqlc.arg(dataset_format)::text,
        sqlc.arg(digest)::text, sqlc.arg(outcome)::text, sqlc.arg(error_class)::text, sqlc.arg(actor_type)::text,
        sqlc.arg(actor_id)::text, sqlc.arg(models_seen)::integer, sqlc.arg(versions_created)::integer,
        sqlc.arg(unchanged)::integer, sqlc.arg(overridden)::integer, sqlc.arg(unmatched)::integer,
        sqlc.arg(skipped)::integer, sqlc.arg(report)::jsonb)
RETURNING id, created_at;

-- name: ListPriceImports :many
SELECT i.id, i.started_at, i.created_at, i.source, i.source_ref, i.dataset_format, i.digest, i.outcome, i.error_class,
       i.actor_type, i.actor_id, i.models_seen, i.versions_created, i.unchanged, i.overridden, i.unmatched, i.skipped
  FROM pandokos.price_import i
 WHERE (NOT sqlc.arg(after)::boolean OR i.id < sqlc.arg(after_id)::uuid)
 ORDER BY i.id DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: GetPriceImport :one
SELECT i.id, i.started_at, i.created_at, i.source, i.source_ref, i.dataset_format, i.digest, i.outcome, i.error_class,
       i.actor_type, i.actor_id, i.models_seen, i.versions_created, i.unchanged, i.overridden, i.unmatched, i.skipped,
       i.report
  FROM pandokos.price_import i
 WHERE i.id = sqlc.arg(id)::uuid;

-- name: CreatePriceVersion :one
INSERT INTO pandokos.price_version (model_id, valid_from, currency, source, import_id, note)
VALUES (sqlc.arg(model_id)::uuid, COALESCE(sqlc.narg(valid_from)::timestamptz, now()), sqlc.arg(currency)::text,
        sqlc.arg(source)::text, sqlc.narg(import_id)::uuid, sqlc.arg(note)::text)
RETURNING id, valid_from, created_at;

-- name: CreatePriceRates :execrows
INSERT INTO pandokos.price_rate (price_version_id, category, threshold, rate)
SELECT sqlc.arg(price_version_id)::uuid, (sqlc.arg(categories)::text[])[i.n], (sqlc.arg(thresholds)::bigint[])[i.n],
       ((sqlc.arg(rates)::text[])[i.n])::numeric
  FROM generate_series(1, cardinality(sqlc.arg(categories)::text[])) AS i (n);

-- name: PriceAt :one
SELECT v.id, v.model_id, v.valid_from, v.currency, v.source, v.import_id, v.note, v.created_at,
       COALESCE(array_agg(r.category ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS categories,
       COALESCE(array_agg(r.threshold ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::bigint[] AS thresholds,
       COALESCE(array_agg(r.rate::text ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS rates
  FROM pandokos.price_version v
  LEFT JOIN pandokos.price_rate r ON r.price_version_id = v.id
 WHERE v.id = (SELECT w.id FROM pandokos.price_version w
                WHERE w.model_id = sqlc.arg(model_id)::uuid
                  AND w.valid_from <= COALESCE(sqlc.narg(at)::timestamptz, now())
                ORDER BY w.valid_from DESC
                LIMIT 1)
 GROUP BY v.id;

-- name: GetPriceVersion :one
SELECT v.id, v.model_id, v.valid_from, v.currency, v.source, v.import_id, v.note, v.created_at,
       COALESCE(array_agg(r.category ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS categories,
       COALESCE(array_agg(r.threshold ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::bigint[] AS thresholds,
       COALESCE(array_agg(r.rate::text ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS rates
  FROM pandokos.price_version v
  LEFT JOIN pandokos.price_rate r ON r.price_version_id = v.id
 WHERE v.id = sqlc.arg(id)::uuid AND v.model_id = sqlc.arg(model_id)::uuid
 GROUP BY v.id;

-- name: ListPriceVersions :many
SELECT v.id, v.model_id, v.valid_from, v.currency, v.source, v.import_id, v.note, v.created_at,
       COALESCE(array_agg(r.category ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS categories,
       COALESCE(array_agg(r.threshold ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::bigint[] AS thresholds,
       COALESCE(array_agg(r.rate::text ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS rates
  FROM pandokos.price_version v
  LEFT JOIN pandokos.price_rate r ON r.price_version_id = v.id
 WHERE v.model_id = sqlc.arg(model_id)::uuid
   AND (NOT sqlc.arg(after)::boolean OR v.valid_from < sqlc.arg(after_valid_from)::timestamptz)
 GROUP BY v.id
 ORDER BY v.valid_from DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: HasFuturePriceVersion :one
SELECT EXISTS (SELECT 1 FROM pandokos.price_version v
                WHERE v.model_id = sqlc.arg(model_id)::uuid AND v.valid_from > now())::boolean AS future;

-- name: DeletePriceVersion :execrows
DELETE FROM pandokos.price_version
 WHERE id = sqlc.arg(id)::uuid AND model_id = sqlc.arg(model_id)::uuid;

-- name: LoadPriceVersions :many
SELECT v.id, v.model_id, v.valid_from, v.currency,
       COALESCE(array_agg(r.category ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS categories,
       COALESCE(array_agg(r.threshold ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::bigint[] AS thresholds,
       COALESCE(array_agg(r.rate::text ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS rates
  FROM pandokos.price_version v
  LEFT JOIN pandokos.price_rate r ON r.price_version_id = v.id
 GROUP BY v.id
 ORDER BY v.model_id, v.valid_from;
```

### `internal/store/queries/sql/virtual_keys.sql` (changed)

- `GetKeyPrincipal`: after `k.credential_id,` add the line
  `COALESCE(k.models, '{}')::text[] AS models, (k.models IS NOT NULL)::boolean AS models_restricted,`.
- `CreateVirtualKey`: the column list ends with `credential_id, models)`; the values end with
  `sqlc.narg(credential_id)::uuid,` and a new line
  `CASE WHEN sqlc.arg(models_restricted)::boolean THEN sqlc.arg(models)::text[] END)`; `RETURNING`
  gains, after `credential_id,`, `COALESCE(models, '{}')::text[] AS models, (models IS NOT NULL)::boolean AS models_restricted,`.
- `GetVirtualKey`, `ListVirtualKeys`: after `k.credential_id,` add
  `COALESCE(k.models, '{}')::text[] AS models, (k.models IS NOT NULL)::boolean AS models_restricted,`.
