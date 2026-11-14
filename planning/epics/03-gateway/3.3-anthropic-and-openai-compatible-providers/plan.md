# Sprint 3.3 — Implementation plan

- Design: [sprint.md](sprint.md). Section numbers (§) refer to it.
- Each part below is self-contained: an agent needs sprint.md and that part only. Parts run in
  order A, B, C1 … C7, D, E, F.
- Suggested grouping for implementers: **G1** = C1, C2 (the families: shared code, `openai`
  option, `anthropic`); **G2** = C4, C5, C6 (migration consumers, administration API, CLI, ledger
  nit); **G3** = C3 then C7 (gateway and wiring, then end to end, SDK suites and repository tests);
  **G4** = part D (Compose reference and CI). G1, G2 and G4 do not depend on each other after part A;
  G3 starts from the fakes of part B, needs G1 merged before C3's tests pass and G1 and G2 merged for
  C7. T-567 (Compose) passes only once G3 is merged. A, B, E and F are one agent each.

## Rules for the implementer

1. Branch `feat/3.3-anthropic-and-compatible-providers` from `main`; sign off every commit. One commit
   for A, one for B (tests, failing), one per step of C (message given with the step), one for D, one
   for E.
2. **No new Go dependency** in either `go.mod`. Python and Node packages are exactly those of §7,
   locked with the commands of part A; no version is typed by hand into a lock file.
3. **No secret, no content.** Fixtures are synthetic with the canaries of `test/fixtures` as their only
   strings (`synthetic-` for identifiers). No log line, span, error, record or control-server answer
   holds a header value, a body or a key. Provider keys in the Compose reference go through standard
   input only.
4. **The admin plane never opens a credential.** `internal/admin` reads `provider_credential_metadata`
   only and imports neither `internal/gateway`, `internal/resolver` nor calls the vault's `Open`.
5. **Anthropic bodies are never changed** except the value of `model` (D-6). `Prepare` returns the
   input slice when there is nothing to replace.
6. The moves of part A (framing, request scanning) are moves: no behaviour change; every existing test
   of `internal/family/openai` passes unmodified except for the two signature changes.
7. Appendices A1 (migration 0007) and A2 (list query) are copied byte for byte; both were run on
   PostgreSQL 16 and A2 was generated with sqlc v1.31.1. If one does not work as written, stop and ask.
8. Generated files are never edited by hand (`make generate`). SQL runs only as
   `queries.New(tx).<Method>` inside a `store` transaction helper (T-R10, T-R12).
9. Tests hold for root and non-root; nothing sleeps longer than 2 s; waiting is `testutil.Eventually`;
   a bound is never made to pass by lengthening it and a database test never by skipping it. A test
   behind a build tag fails, never skips, when what its tag promises is missing.
10. CI actions are pinned to the commit hashes given here; a hash is never typed from memory.
11. English everywhere; every exported identifier has a doc comment.

## Environment

- `make db-up` starts PostgreSQL 16 with TLS and PgBouncer (Docker) and writes `local/testdb/env`.
- `make test`, `make test-sdk`, `make image` then `make test-container`, `make fuzz`, `make generate`,
  `make generate-check`, `make migrations-sum`, `make lint`, `make vuln`, `make secrets`, `make check`.
- New in this sprint (part D): `make test-sdk-python`, `make test-sdk-ts`, `make test-compose`.
- Python 3.13 and Node 22 for the SDK suites; `openssl` and Docker Compose v2 for the Compose test.
- Before starting: `make db-up && make check` must pass on `main`.

---

## Part A — Scaffolding (commit: `chore: sprint 3.3 scaffolding: shared family code, migration 0007, API, SDK locks`)

After this part product code compiles, `make generate-check` passes and every existing test still
passes. Nothing changes behaviour: the `anthropic` adapter exists but is not registered.

**Files:** `internal/family/{family,adapter,jsonbody}.go`, `internal/family/sse/sse.go`,
`internal/family/openai/*.go`, `internal/family/anthropic/*.go`, `internal/gateway/{families,ports,handler,proxy,record}.go`,
`internal/server/server.go`, `internal/store/migrations/sql/0007_provider_stream_usage.sql`,
`internal/store/migrations/migrations.sum`, `internal/store/queries/sql/{catalog,usage}.sql`, generated
`internal/store/queries/*`, `api/openapi/admin.v1.yaml`, generated `internal/adminapi/*`,
`internal/admin/{capability,usage}.go` (stubs), `internal/authz/authz.go`, `internal/cli/*.go`,
`test/apitest/calls.go`, `.golangci.yml`, `Makefile`, `test/sdk/python/{requirements.in,requirements.txt}`,
`test/sdk/ts/{package.json,package-lock.json}`.

1. **Moves.** `internal/family/sse/sse.go`: `Framing` (the `framing` type of `openai/stream.go`, with
   `Max` exported and `Split`, `Take`, `Failed`), `Data` (`eventData`). `internal/family/jsonbody.go`:
   `Member`, `ScanObject`, `LastMember`, `ParseModel` (the model loop of `openai.ParseOperation` with its
   four reasons), `IsLiteral`, `Splice`, `EncodeString`. `openai` uses them; its unexported copies are
   deleted. `sse` has a doc comment saying it is a leaf.
2. **Family types** (§5.9): `Operation.NoUsage`, `Parts` and its constants, `Preparation`,
   `Request.Prepare(p Preparation)`, `StreamReader.Usage() (Usage, Parts)`, `Adapter.Recognizes`,
   `Adapter.UpstreamRequestID`. In `openai`: readers return `PartsAll` or 0; `Recognizes` is `Match`
   ok; `UpstreamRequestID` reads `X-Request-Id`; `Request.Prepare(p)` uses `p.UpstreamModel` and
   still always splices (the option comes in C1).
3. **Gateway call sites**: `Prepare(family.Preparation{UpstreamModel: target.UpstreamModel,
   StreamUsage: true})`; `providerUsage` treats `PartsAll` as today's `ok`; `modifyResponse` takes the
   upstream request ID from `st.adapter.UpstreamRequestID` with the existing bounds. `Target.StreamUsage`
   declared. `families.go`: `Families()` returning `openai` only for now; `Routes()` and
   `server.New` use it (`knownAdapters` removed).
4. **`internal/family/anthropic`**: files `route.go` (constants, `Match` returning false),
   `adapter.go` (the adapter, every method delegating to a package function), `request.go`,
   `usage.go`, `stream.go`, `estimate.go`, `errors.go` with the signatures of §5.9 and stub bodies
   (`ParseOperation` refuses everything with `not_object`; readers return zero; `WriteError` sets the
   status and `X-Pandokos-Error` only; the body comes in C2). Not registered.
5. **Migration and queries.** `0007_provider_stream_usage.sql` from appendix A1; `make migrations-sum`.
   `catalog.sql`: add `p.stream_usage_option` to the `SELECT` and `RETURNING` lists of `ListProviders`,
   `GetProvider`, `GetProviderForUpdate`, `CreateProvider`, `UpdateProvider`, `LoadProviders`; a
   `stream_usage_option` argument to `CreateProvider` and `set_stream_usage_option`/`stream_usage_option`
   to `UpdateProvider` (the `CASE` pattern of the other members). `usage.sql`: append appendix A2.
   `make generate`.
6. **OpenAPI** (appendix A3), then `make generate`. `internal/admin`: `GetCapability` and
   `ListUsageRecords` methods that `return nil, errors.New("admin: not implemented")` (answered 500
   `internal` until C5). Provider handlers pass the new member through with
   the default `add`.
7. **authz**: `UsageRead = "usage:read"`, scope tenant, text "Listing the tenant's usage records", added
   to `tenant-admin`.
8. **CLI**: commands `capability check` (`getCapability`) and `usage list` (`listUsageRecords`) in final
   form (flags of §5.10); `provider create|update --stream-usage-option`. `test/apitest/calls.go`:
   `getCapability` with query `provider=openai&model=gpt-4o-mini`, `listUsageRecords` without body.
9. **Linter** (`depguard`): rule `sse-is-a-leaf` (strict, `**/internal/family/sse/**`, allow `$gostd`);
   `anthropic-family` (strict, `**/internal/family/anthropic/**`, allow `$gostd`, `…/internal/family`,
   `…/internal/family/sse`); `admin-never-opens` (lax, `**/internal/admin/**`, deny `…/internal/gateway`,
   `…/internal/resolver`).
10. **SDK locks** (Python 3.13 and Node 22 on the path):

    ```sh
    cd test/sdk/python
    printf 'openai==3.51.0\nanthropic==2.8.0\npytest==9.2.0\n' > requirements.in
    python3.13 -m venv /tmp/pt && /tmp/pt/bin/pip install pip-tools==7.6.2
    /tmp/pt/bin/pip-compile --generate-hashes --allow-unsafe --strip-extras --no-header \
        -o requirements.txt requirements.in
    cd ../ts
    # package.json as in appendix A4, then:
    npm install --ignore-scripts --no-audit --no-fund
    git add package.json package-lock.json   # node_modules is ignored (test/sdk/ts/.gitignore)
    ```

    The design produced 22 packages with 285 hashes and a lock of nine packages; whatever the
    commands produce from the pins is what is committed. If a pin no longer resolves, stop and ask.
11. **Makefile** targets (they only run tests; `check` is unchanged):

    ```make
    test-sdk-python: ## Python SDK suite (needs python3.13 and the locked packages: make sdk-python-env)
    	cd test/sdk && PANDOKOS_SDK_PYTHON=$(CURDIR)/local/sdk-venv/bin/python GOTOOLCHAIN=go$(RELEASE_GO) \
    		go test -tags sdkexternal -count=1 -run '^TestPythonSDK$$' ./...
    
    sdk-python-env: ## Create local/sdk-venv from the hash-locked requirements
    	python3.13 -m venv local/sdk-venv
    	local/sdk-venv/bin/pip install --require-hashes --no-deps --only-binary :all: -r test/sdk/python/requirements.txt
    
    test-sdk-ts: ## TypeScript SDK suite (needs node 22: npm ci runs first)
    	cd test/sdk/ts && npm ci --ignore-scripts --no-audit --no-fund
    	cd test/sdk && PANDOKOS_SDK_NODE=$$(command -v node) GOTOOLCHAIN=go$(RELEASE_GO) \
    		go test -tags sdkexternal -count=1 -run '^TestTypeScriptSDK$$' ./...
    
    test-compose: ## The Compose quick-start against a built image: make image first
    	PANDOKOS_TEST_IMAGE=$(IMAGE) go test -tags compose -count=1 -timeout 15m ./test/compose/
    ```

**Verify:** `go build ./... && make generate-check && make lint && make test && make test-sdk`.

### Appendix A1 — `internal/store/migrations/sql/0007_provider_stream_usage.sql`

```sql
-- 0007_provider_stream_usage: whether Pandokos adds the stream usage option to chat completion
-- streams sent to a provider of the openai family (ADR 0042). Runs in one transaction as
-- pandokos_owner. Every name is schema-qualified.

ALTER TABLE pandokos.provider
    ADD COLUMN stream_usage_option text NOT NULL DEFAULT 'add',
    ADD CONSTRAINT provider_stream_usage_option_valid CHECK (stream_usage_option IN ('add', 'omit'));

GRANT INSERT (stream_usage_option), UPDATE (stream_usage_option) ON pandokos.provider TO pandokos_admin;
```

The provider's notify trigger (statement level, 0004) already announces the change to the gateway's
catalogue cache. `omit` for an `anthropic` provider is refused by the administration API, not by the
table (the family is immutable and the option is inert there).

### Appendix A2 — appended to `internal/store/queries/sql/usage.sql`

```sql

-- name: ListUsageRecords :many
SELECT u.id, u.started_at, u.request_id, u.trace_id, u.principal_kind, u.principal_id, u.key_id, u.credential_id,
       u.funding, u.provider, u.api_family, u.operation, u.endpoint, u.model, u.model_id, u.upstream_model, u.stream,
       u.outcome, u.error_code, u.upstream_status, u.ttfb_ms, u.duration_ms, u.request_bytes, u.response_bytes,
       u.input_source, u.output_source, u.input_tokens, u.output_tokens, u.cache_read_tokens, u.cache_write_tokens,
       u.reasoning_tokens, u.other_units, u.provider_usage, u.estimated_input_tokens, u.estimated_output_tokens,
       u.cost_status, u.cost_reason, u.cost, u.currency, u.price_version_id, u.cost_fallbacks, u.metadata,
       u.metadata_dropped
  FROM pandokos.usage_record u
 WHERE u.started_at >= sqlc.arg(from_at)::timestamptz
   AND u.started_at <  sqlc.arg(to_at)::timestamptz
   AND (sqlc.arg(provider)::text = '' OR u.provider = sqlc.arg(provider)::text)
   AND (NOT sqlc.arg(after)::boolean
        OR (u.started_at, u.id) < (sqlc.arg(after_started_at)::timestamptz, sqlc.arg(after_id)::uuid))
 ORDER BY u.started_at DESC, u.id DESC
 LIMIT sqlc.arg(page_size)::integer;
```

Change the header comment of `usage.sql` to: "Usage ledger. Insert only for the gateway; the admin role
lists a tenant's records in its session (ListUsageRecords). No statement changes or removes a row."
The statement runs as the admin role in `store.WithTenant`; the policy supplies the tenant.

### Appendix A3 — OpenAPI changes (`api/openapi/admin.v1.yaml`)

Paths, after `/v1/tenants/{tenant}/model-restriction`:

```yaml
  /v1/tenants/{tenant}/capability:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: getCapability
      tags: [model-access]
      summary: Say whether the tenant can call a provider and model now
      description: >-
        Evaluates, on committed state, the rules the gateway applies to a call made with a key of
        the tenant that has no pin and no model list: the provider is available, the model is
        allowed, and the tenant has an active default credential for the provider. provider and
        model are required. A tenant that is not active is answered 403 tenant_suspended.
      x-pandokos-permission: model:read
      parameters:
        - {name: provider, in: query, description: The provider. Required., schema: {$ref: '#/components/schemas/ProviderName'}}
        - {name: model, in: query, description: The public model name. Required., schema: {$ref: '#/components/schemas/ModelName'}}
      responses:
        "200": {$ref: '#/components/responses/Capability'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
  /v1/tenants/{tenant}/usage-records:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: listUsageRecords
      tags: [usage]
      summary: List the tenant's usage records, newest first
      description: >-
        Records whose started_at is in [from, to). to defaults to now and from to 24 hours before to;
        the window is at most 31 days. A cursor is valid only with the same from, to and provider.
      x-pandokos-permission: usage:read
      parameters:
        - {$ref: '#/components/parameters/Limit'}
        - {$ref: '#/components/parameters/Cursor'}
        - {name: from, in: query, description: 'Start of the window, RFC 3339.', schema: {type: string, maxLength: 64}}
        - {name: to, in: query, description: 'End of the window (excluded), RFC 3339.', schema: {type: string, maxLength: 64}}
        - {name: provider, in: query, description: Only records of this provider., schema: {$ref: '#/components/schemas/ProviderName'}}
      responses:
        "200": {$ref: '#/components/responses/UsageRecordList'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
```

Tag `usage` added to `tags`. Responses:

```yaml
    Capability:
      description: Whether the tenant can call the provider and model now, and why not.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/Capability'}
    UsageRecordList:
      description: One page of usage records.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/UsageRecordList'}
```

Schemas (new):

```yaml
    StreamUsageOption:
      type: string
      description: add (default) adds the stream usage option to chat completion streams that do not ask for usage; omit never adds it. Only add is accepted for a provider of family anthropic.
      enum: [add, omit]
    CredentialAvailability:
      type: string
      enum: [active, missing, disabled, revoked]
    BudgetAvailability:
      type: string
      description: not_enforced until limits exist.
      enum: [not_enforced]
    CapabilityChecks:
      type: object
      additionalProperties: false
      required: [provider_available, model_allowed, credential, credential_validated, budget]
      properties:
        provider_available: {type: boolean}
        model_allowed: {type: boolean}
        credential: {$ref: '#/components/schemas/CredentialAvailability'}
        credential_id: {type: string, description: The default credential of the tenant for the provider; absent when there is none.}
        credential_validated: {type: boolean, description: The credential was accepted by the provider when it was stored or replaced. Informational.}
        budget: {$ref: '#/components/schemas/BudgetAvailability'}
    Capability:
      type: object
      additionalProperties: false
      required: [provider, model, callable, reasons, checks, checked_at]
      properties:
        provider: {$ref: '#/components/schemas/ProviderName'}
        model: {$ref: '#/components/schemas/ModelName'}
        callable: {type: boolean}
        reasons:
          type: array
          description: 'Every failing check, in this order: provider_unavailable, model_not_allowed, no_credential, credential_disabled, credential_revoked. Empty when callable.'
          items: {type: string}
        checks: {$ref: '#/components/schemas/CapabilityChecks'}
        checked_at: {type: string, format: date-time}
    UsageCounter:
      type: object
      additionalProperties: false
      required: [name, value]
      properties:
        name: {type: string}
        value: {type: integer}
    MetadataEntry:
      type: object
      additionalProperties: false
      required: [key, value]
      properties:
        key: {type: string}
        value: {type: string}
    UsageRecord:
      type: object
      additionalProperties: false
      required: [id, request_id, trace_id, started_at, duration_ms, principal_kind, principal_id, key_id, credential_id,
                 funding, provider, api_family, operation, endpoint, model, model_id, upstream_model, stream, outcome,
                 error_code, upstream_status, ttfb_ms, request_bytes, response_bytes, input_source, output_source,
                 input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
                 estimated_input_tokens, estimated_output_tokens, other_units, provider_usage, cost_status, cost_reason,
                 cost, currency, price_version_id, cost_fallbacks, metadata, metadata_dropped]
      properties:
        id: {$ref: '#/components/schemas/ID'}
        request_id: {type: string}
        trace_id: {type: string, description: Empty when the request had no trace.}
        started_at: {type: string, format: date-time}
        duration_ms: {type: integer}
        principal_kind: {type: string}
        principal_id: {type: string}
        key_id: {type: string}
        credential_id: {type: string, description: Empty when no credential was opened.}
        funding: {type: string}
        provider: {type: string}
        api_family: {type: string}
        operation: {type: string}
        endpoint: {type: string}
        model: {type: string}
        model_id: {type: string}
        upstream_model: {type: string}
        stream: {type: boolean, description: False also when the request was refused before its body was read.}
        outcome: {type: string}
        error_code: {type: string}
        upstream_status: {type: integer, description: 0 when the provider did not answer.}
        ttfb_ms: {type: integer, description: 0 when no body byte arrived.}
        request_bytes: {type: integer}
        response_bytes: {type: integer}
        input_source: {type: string, description: 'provider, estimated or none.'}
        output_source: {type: string, description: 'provider, estimated or none.'}
        input_tokens: {type: integer, description: Reported by the provider; 0 unless input_source is provider. Includes cache_read_tokens and cache_write_tokens.}
        output_tokens: {type: integer, description: Reported by the provider; 0 unless output_source is provider. Includes reasoning_tokens.}
        cache_read_tokens: {type: integer}
        cache_write_tokens: {type: integer}
        reasoning_tokens: {type: integer}
        estimated_input_tokens: {type: integer, description: 0 unless input_source is estimated.}
        estimated_output_tokens: {type: integer, description: 0 unless output_source is estimated.}
        other_units:
          type: array
          items: {$ref: '#/components/schemas/UsageCounter'}
        provider_usage:
          type: array
          description: The provider's own integer counters, by path.
          items: {$ref: '#/components/schemas/UsageCounter'}
        cost_status: {type: string, description: 'known, unknown or none.'}
        cost_reason: {type: string}
        cost: {type: [string, "null"], description: Decimal amount in currency; null unless cost_status is known. Informational under BYOK.}
        currency: {type: string}
        price_version_id: {type: string}
        cost_fallbacks:
          type: array
          items: {type: string}
        metadata:
          type: array
          items: {$ref: '#/components/schemas/MetadataEntry'}
        metadata_dropped: {type: integer}
    UsageRecordList:
      type: object
      additionalProperties: false
      required: [items]
      properties:
        items:
          type: array
          items: {$ref: '#/components/schemas/UsageRecord'}
        next_cursor: {type: string, description: Absent on the last page.}
```

Changed: `Provider` gains required `stream_usage_option: {$ref: '#/components/schemas/StreamUsageOption'}`;
`ProviderCreate` and `ProviderUpdate` gain it as optional. `ProviderName`'s description becomes
"Identifier of a provider of the catalogue." Every member type above is one the generator accepts
(`internal/apigen`: component enums, plain strings, integers, booleans, date-times, inline nullable
strings, arrays of component objects or plain strings); if `make generate` refuses one, stop and ask.

### Appendix A4 — `test/sdk/ts/package.json`

```json
{
  "name": "pandokos-sdk-ts",
  "private": true,
  "type": "module",
  "engines": { "node": ">=22" },
  "scripts": { "test": "node --test --test-concurrency=1" },
  "devDependencies": {
    "@anthropic-ai/sdk": "1.8.0",
    "openai": "7.57.0"
  }
}
```

`test/sdk/ts/.gitignore`: `node_modules/`. `test/sdk/python/.gitignore`: `__pycache__/`, `.venv/`.

---

## Part B — Tests first (commit: `test: sprint 3.3 tests, failing`)

Write every test of §9 and adapt every changed test before any file of part C, against the **final**
API of sprint.md. Product code still compiles; run `go vet ./... ; make test ; make test-sdk ;
make test-sdk-python ; make test-sdk-ts` and keep the output for the pull request (the last two, added in
part A, fail because the Anthropic routes answer 404).

General rules: sweeps range over the product's own lists (`gateway.Routes()`, `family.ErrorCode`
constants through a table the test owns and checks for completeness against `family.go`,
`anthropic.RequestHeaders`, the operations of `adminapi.Operations`). Canary assertions use
`testutil.AssertNoCanary` and `testutil.AssertAbsent`. Streaming tests use the fake's gate, never
timing.

### B1. Fixtures and helpers

- **`test/fixtures/anthropic`** (the files of §7). Shapes:
  - `message.json`: `{"id":"msg_synthetic-1","type":"message","role":"assistant","model":"claude-sonnet-4-5",
    "content":[{"type":"text","text":<CanaryCompletion>}],"stop_reason":"end_turn","stop_sequence":null,
    "usage":{"input_tokens":12,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":7}}`.
  - `message_cache.json`: `usage` with `input_tokens` 10, `cache_creation_input_tokens` 300,
    `cache_read_input_tokens` 2000, `cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":200}`,
    `output_tokens` 50, `server_tool_use":{"web_search_requests":2}`, `service_tier":"standard"` (a string:
    dropped from `Reported`).
  - `message_stream.sse`: `event: message_start` (usage `input_tokens` 12, `output_tokens` 1),
    `content_block_start`, `ping`, three `content_block_delta` (`text_delta` with parts of the canary),
    `content_block_stop`, `message_delta` (`usage":{"output_tokens":7}`), `message_stop`; LF endings.
  - `message_stream_cumulative.sse`: as above with `message_start` usage including cache members and a
    `message_delta` usage repeating `input_tokens`, `cache_read_input_tokens` and `output_tokens`.
  - `message_stream_tools.sse`: a `tool_use` block with `input_json_delta` deltas.
  - `message_stream_error.sse`: after two deltas, `event: error` with
    `{"type":"error","error":{"type":"overloaded_error","message":<CanaryCompletion>}}`, no `message_delta`.
  - `message_stream_ping_crlf.sse`: CRLF endings and several `ping` events.
  - `count_tokens.json`: `{"input_tokens":42}`. `error_401.json`, `error_429.json`, `error_529.json`:
    the provider's shape with `"request_id":"req_synthetic-…"`.
- **`test/fixtures/compat`** (the files of §7): `chat_stream_usage_last_content_chunk.sse` (the last chunk
  has non-empty `choices` with `finish_reason` and a `usage` object, no usage-only chunk);
  `chat_stream_extra_usage.sse` (usage-only chunk whose usage adds `"prompt_cache_hit_tokens":3`,
  `"queue_time":0.012`, `"x_vendor":{"total_time":0.3}`); `chat_stream_no_usage.sse`;
  `chat_completion_extra_usage.json`; `error_400_stream_options.json` (`{"error":{"message":<Canary…>,
  "type":"invalid_request_error","code":"unsupported_parameter"}}`).
- **`test/fakeprovider`**: the routes and behaviours of §7; `NewOn(tb, addr)` listens on a given
  address (T-567 uses `0.0.0.0:0`, so that containers reach it through the host gateway). The Anthropic routes check `X-Api-Key`
  against `Accept` and answer `error_401.json` otherwise; they set `request-id: req_synthetic-<n>`.
  `compat-refuses-option` answers `error_400_stream_options.json` with 400 when the body has a top-level
  `stream_options` member, and `chat_stream_no_usage.sse` otherwise; `compat-ignores-option` always
  streams `chat_stream_no_usage.sse`; `compat-usage-last-chunk` and `compat-extra-usage` stream their
  files. `SetBasePath(p)`: every route also answers under `p`. `Requests()` records, per request, the
  method, path, header names and values with the credential replaced by `"<credential>"` and the body.
- **`test/testutil`**: `TargetFor(up, provider, model)` unchanged; `TargetForFamily(up, provider,
  family, model)`; `StaticCatalogue` accepts any provider→family map (it already does); a
  `StreamUsage` field defaulting to true in `TargetFor`.
- **`test/e2e`**: helpers to create a provider, an endpoint, a model and a grant through the CLI.
- **`test/sdk/external_test.go`** (build tag `sdkexternal`), in final form, because it is test
  infrastructure: `startExternal(t)` uses `startRecording` of `sdk_test.go` with three providers
  (`openai`, `anthropic` with family `anthropic`, `compat` with family `openai`, base path
  `/compat-base` and `StreamUsage` false) and a control server (`httptest.NewServer` on loopback) with:
  - `GET /records?request_id=<id>`: `[{"provider","api_family","operation","outcome","stream",
    "input_source","output_source","input_tokens","output_tokens","cache_read_tokens","cache_write_tokens",
    "estimated_input_tokens","estimated_output_tokens","cost_status","metadata":{…},"metadata_dropped"}]`
    built from `MemoryRecorder.Records()` (metadata values are the test's own labels).
  - `GET /upstream/last`: `{"path", "header_names", "body_has_stream_options": bool, "model"}` of the
    last request the fake received.
  - `GET /upstream/cancelled?wait=5s`: `{"cancelled": bool}` from `WaitCancelled`.
  `TestPythonSDK` runs `$PANDOKOS_SDK_PYTHON -m pytest -q -p no:cacheprovider .` in the module's
  directory `python`, `TestTypeScriptSDK` runs `$PANDOKOS_SDK_NODE --test
  --test-concurrency=1` in the module's directory `ts`; both with a 5-minute context, the environment of §7 and
  `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY` (both cases) removed; output copied to `t.Log`; `t.Fatal`
  when the variable is unset or the process exits non-zero.

### B2. The Python and TypeScript suites

One file per area in each language, the test ID in each test's name or description:

| Area | Python | TypeScript |
|---|---|---|
| T-556, T-557, T-558, T-564 | `test_openai.py` | `openai.test.mjs` |
| T-559, T-560, T-565 | `test_anthropic.py` | `anthropic.test.mjs` |
| T-561 | `test_errors.py` | `errors.test.mjs` |
| T-562, T-563 | `test_metadata_cancel.py` | `metadata_cancel.test.mjs` |
| T-566 | `test_base_urls.py` | `base_urls.test.mjs` |
| helpers | `conftest.py` (clients, `records(request_id)` polling the control server for at most 5 s) | `helpers.mjs` |

Conventions:
- Clients are built with `max_retries=0` / `maxRetries: 0` and the base URLs of the gateway guide:
  OpenAI `f"{PANDOKOS_BASE_URL}/openai/v1"`, Anthropic `f"{PANDOKOS_BASE_URL}/anthropic"`, compatible
  `f"{PANDOKOS_BASE_URL}/compat/v1"`. T-566 reads the base URL column of the guide's table and asserts
  these strings.
- The request ID is taken from the raw response: Python `client.….with_raw_response.create(…)` →
  `.headers["x-pandokos-request-id"]`, streams with `with_streaming_response`; TypeScript
  `.withResponse()` → `response.headers.get("x-pandokos-request-id")`. For an SDK error, from
  `err.response.headers` (Python) / `err.headers` (TypeScript).
- Models are the fake's (`fake-basic`, `fake-cache`, `fake-stall-after-first`, `fake-error-429`,
  `fake-stream-error`, …); prompts are `CANARY_PROMPT` from the environment (the harness passes the
  canary values so the scripts hold none of their own).
- Cancellation: Python closes the stream (`stream.close()` / leaving the `with` block) after the first
  event; TypeScript calls `stream.controller.abort()` after the first event. Then
  `/upstream/cancelled?wait=5s` must say true and the record's outcome is `cancelled`.

### B3. Isolation

`test/isolation/spec.go`: the admin role's grants on `provider` gain `INSERT (…, stream_usage_option)`
and `UPDATE (…, stream_usage_option)`. No new table, no new fixture.

### B4. Where each test lives

| Tests | File |
|---|---|
| T-515 | `internal/family/sse/sse_test.go` |
| T-516 | `internal/family/jsonbody_test.go` |
| T-517 to T-526 | `internal/family/anthropic/{adapter,request,usage,stream,estimate,errors}_test.go` |
| T-527, T-528 | `internal/family/openai/{request,compat}_test.go` |
| T-529 to T-539 | `internal/gateway/{families,anthropic,shape,option,compat}_test.go`; changed tests in place |
| T-540 | `internal/resolver/resolver_db_test.go` |
| T-541, T-550 | `internal/store/queries/queries_db_test.go` |
| T-542 to T-544, T-546, T-547 | `internal/admin/{providers,capability,usage}_db_test.go` |
| T-548 | `internal/authz/authz_test.go` |
| T-549 | `internal/cli/{capability,usage}_test.go` |
| T-551 | `internal/ledger/writer_test.go` |
| T-545, T-552 to T-554 | `test/e2e/{capability,anthropic,compat}_test.go` |
| T-555 to T-566 | `test/sdk/external_test.go`, `test/sdk/python/`, `test/sdk/ts/` |
| T-567 | `test/compose/compose_test.go` (part D writes the files it uses; the test is written here) |
| T-R31 to T-R35 | `test/repo/{gateway,compose,sdk,fixtures,imports}_test.go` |

Notes:
- T-522: the "every split offset" property uses the test's own event splitter, not `sse.Framing`.
- T-532: the catalogue-away case uses `testutil.StaticCatalogue` with an error injected for
  `Provider`.
- T-533: the cut case uses `fake-cut-after-start`; assert `len(Records()) == 1` after
  `testutil.Eventually`.
- T-543: the handler runs on the admin pool, whose role has no privilege on
  `provider_credential.secret` (migration 0003; `Spec.Grants`); the test also asserts that
  `capability.go` calls no query of `credentials.sql` other than `ListCredentials` (a `go/ast` scan of
  the file for `queries.` selectors).
- T-545: states are produced with the administration API; the gateway call uses a fresh tenant
  key per state and the catalogue cache is flushed (`Cache.Flush`) between states, so no lifetime is
  waited for.
- T-567: needs `PANDOKOS_TEST_IMAGE`; fails (does not skip) when it is unset under the `compose` tag.

---

## Part C — Implementation

Each step ends with its tests green and `make lint` clean.

### C1. Shared family code and the openai option — `feat: stream usage option honoured by the openai family; compatible profiles`

**Files:** `internal/family/openai/request.go`, `internal/family/openai/*_test.go` only where B left
signature adaptations. **Tests:** T-515, T-516, T-527, T-528, every existing openai test and fuzz target.

1. `Request.Prepare(p)`: the usage edit is applied only when `p.StreamUsage` is true; `strip` is
   true only then. Model replacement unchanged.
2. Run the compat fixtures through the readers; no reader change is expected (T-528 documents the
   behaviour). If a profile needs a change, stop and ask: it would change 3.2 behaviour.

### C2. The anthropic family — `feat: anthropic API family: credentials, headers, usage across stream events, error shape`

**Files:** `internal/family/anthropic/*.go`. **Tests:** T-517 to T-526; `make fuzz FUZZTIME=30s` for the
three new targets.

1. `route.go`: `Match` for the two pairs; `InboundCredential` by the table of §5.2;
   `AttachCredential` sets `X-Api-Key`; `RequestHeaders`; `AllowResponseHeader`; `Recognizes`
   (`Match` ok, or `h.Get(HeaderVersion) != ""`); `UpstreamRequestID` (`Request-Id`).
2. `request.go`: `ParseOperation` with `family.ScanObject` and `family.ParseModel`; `Stream()` per
   §5.2; `Prepare` replaces the model only (`family.Splice`, `family.EncodeString`).
3. `usage.go`: the wire struct of §5.4 with `*int64` members (null and absent are 0), `normalize`,
   `ReadUsageOf` (counting: `ok` false, never called by the gateway), `Reported` via
   `family.FlattenUsage` of the object.
4. `stream.go`: `StreamReader` over `sse.Framing{Max: 1 << 20}`; a quick byte check
   (`"usage"` or `"content_block_delta"`) before decoding an event, as the responses reader does; the
   current usage object kept as `map[string]json.RawMessage` (bounded by the event size); the merge
   rule of §5.4; `Usage()` normalizes the current object and flattens it for `Reported`.
5. `estimate.go`, `errors.go` (`ErrorType` table of §5.2; `WriteError` with
   `{"type":"error","error":{"type":…,"message":…}}`).
6. `adapter.go`: operations with `NoUsage` on counting; the adapter's `NewStreamReader` ignores
   `strip`.

### C3. Gateway and wiring — `feat: anthropic routes, error shape of unrouted requests, mixed usage sources`

**Files:** `internal/gateway/{families,handler,proxy,record,spans}.go`, `internal/resolver/catalogue.go`
(fill `Target.StreamUsage` once C4's `catalog.Provider.StreamUsageOption` exists; until then from
`true`), `internal/server/server.go`. **Tests:** T-529 to T-539 and the changed tests of §9.

1. `Families()` returns both adapters.
2. `route()`: when no family is selected, the adapter for the error is chosen by §5.3 (iterate
   `Families()` names in order, `Recognizes(r.Method, native, r.Header)`), set as `st.adapter` without
   setting `st.matched`. Nothing else of the request is read.
3. `Prepare(family.Preparation{UpstreamModel: target.UpstreamModel, StreamUsage: target.StreamUsage})`.
4. `record.go`: `providerUsage` returns `(Usage, Parts, gaveUp)`; the source table of §5.4 (an
   input-only report takes the output from `EstimateUsage(...).OutputTokens` into `Estimated` and into
   `costUsage`); `st.op.NoUsage` skips the usage block and the cost. A reader that gave up still
   discards everything (3.2 rule).
5. Log field `usage_output_source` (only when it differs from `usage_source`); span attribute
   `pandokos.usage.output_source` added to `SpanAttributes` and set on the client span.

### C4. The provider's stream usage option — `feat: per-provider stream usage option`

**Files:** `internal/catalog/{access,load}.go`, `internal/resolver/catalogue.go`,
`internal/admin/{providers,convert_catalog}.go`, `internal/cli/catalogue.go`. **Tests:** T-540 to T-542.

1. `catalog.Provider.StreamUsageOption` loaded by `LoadSnapshot`; resolver sets
   `Target.StreamUsage = p.StreamUsageOption != "omit"` from the snapshot that resolved the model.
2. Admin create and update: default `add`; `omit` with family `anthropic` →
   `invalid("stream_usage_option", "must be add for a provider of family anthropic")`; the audit detail
   map gains the member; `providerOut` returns it.
3. CLI flag `--stream-usage-option`.

### C5. Capability and usage list — `feat: tenant capability check and usage record list`

**Files:** `internal/admin/{capability,usage}.go`, `internal/cli/*` (if A left anything), docs untouched.
**Tests:** T-543, T-544, T-546 to T-550.

1. `GetCapability`: require `provider` and `model` (`invalid` naming each missing one); `snap, err :=
   catalog.LoadSnapshot(ctx, s.db)`; `ta, err := catalog.LoadTenantAccess(ctx, s.db, tid)`; a principal
   `authz.Principal{Kind: authz.KindServiceAccount, TenantID: tid}` (no key list); `snap.Resolve` maps
   to the two checks; then in `WithTenant`, `ListCredentials(provider, state "")` pages until the row
   with `is_default` (at most one exists; a page of 200 covers any tenant in practice, but loop to the
   end); state → `credential`; `validated_at` → `credential_validated`. Reasons in table order.
   Database errors → 503 `unavailable`.
2. `ListUsageRecords`: parse `from`/`to` with `time.Parse(time.RFC3339Nano, …)`, defaults, bounds;
   the cursor: `encodeCursor(sort, name, id)` with `sort` = `"usage:" + from + "|" + to + "|" +
   provider` (UTC, RFC3339Nano) and `name` = `started_at` in RFC3339Nano, so `decodeCursor` refuses a
   cursor of another window or filter; `queries.ListUsageRecords` in `WithTenant` with `PageSize`
   `limit+1`; map each row by §5.7a (`other_units`, `provider_usage`, `metadata` decoded from their JSON
   text and sorted; nullable columns to zero values).
3. Both handlers write no audit row and log nothing beyond the request line.

### C6. The ledger nit — `fix: the usage record logged after Close omits the fields that failed`

**Files:** `internal/ledger/writer.go`. **Tests:** T-551.

`logNotWritten(r *usage.Record, omit ...string)` passes `omit` to `rowJSON`; the after-`Close` branch of
`Record` calls it with `r.Failed()...` when `Validate` failed; every other caller is unchanged.

### C7. Everything together — `test: end-to-end, SDK suites in Python and TypeScript, repository tests pass`

**Files:** whatever the remaining failures point at; no design change. **Tests:** T-545, T-552 to
T-566, T-R31 to T-R35, the whole of `make check`, `make test-sdk-python`, `make test-sdk-ts`.

---

## Part D — CI, container and Compose (commit: `ci: Python and TypeScript SDK jobs; Compose reference and its test`)

**Files:** `deploy/compose/{compose.yaml,setup.sh,initdb/10-pandokos.sh,.gitignore}`,
`test/compose/{compose_test.go,override.yaml}`, `Makefile`, `.github/workflows/ci.yml`,
`.github/dependabot.yml`.

### D1. `deploy/compose/compose.yaml`

```yaml
# Pandokos reference deployment: one instance and PostgreSQL 16 (OPS-02). Prepare with ./setup.sh,
# then docker compose up -d --wait. Guide: docs/operations/compose.md.
name: pandokos

x-database: &database
  PANDOKOS_DATABASE_ADDRESS: postgres:5432
  PANDOKOS_DATABASE_NAME: pandokos
  PANDOKOS_DATABASE_TLS_MODE: verify-full
  PANDOKOS_DATABASE_TLS_CA_FILE: /run/secrets/tls_cert

services:
  postgres:
    image: postgres:16-alpine@sha256:aa6cb33cf4c19c765cefcf48d96176258800870a9a1e930ad126200f9d825139
    entrypoint:
      - sh
      - -c
      - |
        set -eu
        mkdir -p /run/pandokos-tls
        install -o postgres -g postgres -m 0644 /run/secrets/tls_cert /run/pandokos-tls/server.pem
        install -o postgres -g postgres -m 0600 /run/secrets/tls_key /run/pandokos-tls/server.key
        exec docker-entrypoint.sh "$$@"
      - --
    command: [postgres, -c, ssl=on, -c, ssl_cert_file=/run/pandokos-tls/server.pem, -c, ssl_key_file=/run/pandokos-tls/server.key]
    environment:
      POSTGRES_PASSWORD_FILE: /run/secrets/db_superuser
    secrets: [db_superuser, db_owner, db_gateway, db_admin, db_worker, tls_cert, tls_key]
    volumes:
      - pgdata:/var/lib/postgresql/data
      - ./initdb:/docker-entrypoint-initdb.d:ro
      - ../postgres/roles.sql:/pandokos/roles.sql:ro
    networks: [database]
    healthcheck:
      test: ["CMD", "pg_isready", "-h", "127.0.0.1", "-U", "postgres"]
      interval: 2s
      timeout: 3s
      retries: 30
    restart: unless-stopped

  migrate:
    image: ${PANDOKOS_IMAGE:-ghcr.io/pandokos/pandokos:0.1}
    command: [migrate]
    read_only: true
    cap_drop: [ALL]
    security_opt: [no-new-privileges:true]
    environment:
      <<: *database
      PANDOKOS_DATABASE_OWNER_USER: pandokos_owner
      PANDOKOS_DATABASE_OWNER_PASSWORD_FILE: /run/secrets/db_owner
    secrets: [db_owner, tls_cert]
    networks: [database]
    depends_on:
      postgres: {condition: service_healthy}
    restart: "no"

  pandokos:
    image: ${PANDOKOS_IMAGE:-ghcr.io/pandokos/pandokos:0.1}
    command: [serve]
    read_only: true
    cap_drop: [ALL]
    security_opt: [no-new-privileges:true]
    stop_grace_period: 45s
    environment:
      <<: *database
      PANDOKOS_LISTENERS_INFERENCE_ADDRESS: 0.0.0.0:8080
      PANDOKOS_LISTENERS_INFERENCE_TLS_CERT_FILE: /run/secrets/tls_cert
      PANDOKOS_LISTENERS_INFERENCE_TLS_KEY_FILE: /run/secrets/tls_key
      PANDOKOS_LISTENERS_ADMIN_ADDRESS: 0.0.0.0:8081
      PANDOKOS_LISTENERS_ADMIN_TLS_CERT_FILE: /run/secrets/tls_cert
      PANDOKOS_LISTENERS_ADMIN_TLS_KEY_FILE: /run/secrets/tls_key
      PANDOKOS_DATABASE_GATEWAY_USER: pandokos_gateway
      PANDOKOS_DATABASE_GATEWAY_PASSWORD_FILE: /run/secrets/db_gateway
      PANDOKOS_DATABASE_ADMIN_USER: pandokos_admin
      PANDOKOS_DATABASE_ADMIN_PASSWORD_FILE: /run/secrets/db_admin
      PANDOKOS_DATABASE_WORKER_USER: pandokos_worker
      PANDOKOS_DATABASE_WORKER_PASSWORD_FILE: /run/secrets/db_worker
      PANDOKOS_VAULT_KEK_LOCAL_KEY_FILE: /run/secrets/pandokos_kek
      PANDOKOS_CLI_SERVER: https://localhost:8081
      PANDOKOS_CLI_CA_FILE: /run/secrets/tls_cert
    secrets: [db_gateway, db_admin, db_worker, pandokos_kek, tls_cert, tls_key]
    volumes:
      - cli:/home/nonroot
    ports:
      - "127.0.0.1:8080:8080"
      - "127.0.0.1:8081:8081"
    networks: [database, egress]
    depends_on:
      migrate: {condition: service_completed_successfully}
    restart: unless-stopped

secrets:
  db_superuser: {file: ./secrets/db_superuser}
  db_owner: {file: ./secrets/db_owner}
  db_gateway: {file: ./secrets/db_gateway}
  db_admin: {file: ./secrets/db_admin}
  db_worker: {file: ./secrets/db_worker}
  pandokos_kek: {file: ./secrets/pandokos_kek}
  tls_cert: {file: ./secrets/tls_cert}
  tls_key: {file: ./secrets/tls_key}

volumes:
  pgdata: {}
  cli: {}

networks:
  database: {internal: true}
  egress: {}
```

Check with `docker compose -f deploy/compose/compose.yaml config -q` after `setup.sh` ran. If
`/home/nonroot` is not owned by 65532 in the image (`docker run --rm --entrypoint /pandokos <image>
version` works; ownership is checked by T-567's `init --key-out`), stop and ask.

### D2. `deploy/compose/setup.sh` (mode 0755)

```sh
#!/bin/sh
# Prepares ./secrets for the Compose reference deployment: database passwords, the vault's
# key-encryption key and a self-signed TLS certificate. Run once from this directory. Needs sh,
# head, base64, tr and openssl. Never overwrites a file that exists. Guide: docs/operations/compose.md.
set -eu
umask 077
dir=${PANDOKOS_COMPOSE_SECRETS:-./secrets}
command -v openssl >/dev/null 2>&1 || { echo "setup.sh: openssl is required to create the TLS certificate" >&2; exit 1; }
mkdir -p "$dir"
random() { head -c 32 /dev/urandom | base64 | tr -d '\n'; }
for name in db_superuser db_owner db_gateway db_admin db_worker pandokos_kek; do
  if [ ! -e "$dir/$name" ]; then
    random > "$dir/$name"
    echo >> "$dir/$name"
  fi
done
if [ ! -e "$dir/tls_cert" ]; then
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 365 \
    -subj "/CN=pandokos-compose" \
    -addext "subjectAltName=DNS:localhost,DNS:pandokos,DNS:postgres,IP:127.0.0.1,IP:::1" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -keyout "$dir/tls_key" -out "$dir/tls_cert" 2>/dev/null
fi
# The containers run as other users (postgres 70, pandokos 65532) and read the files through bind
# mounts, so the files are readable by everyone and the directory by its owner only.
chmod 0700 "$dir"
chmod 0444 "$dir"/*
echo "secrets ready in $dir; the TLS certificate to trust is $dir/tls_cert"
```

### D3. `deploy/compose/initdb/10-pandokos.sh` (mode 0755)

```sh
#!/bin/sh
# Runs once, when the data directory is created: the privilege roles of Pandokos as login users with
# the passwords of ./secrets, and the database ("Shortest" setup of docs/operations/database.md).
set -eu
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres -f /pandokos/roles.sql
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres <<'SQL'
\set owner_pw `cat /run/secrets/db_owner`
\set gateway_pw `cat /run/secrets/db_gateway`
\set admin_pw `cat /run/secrets/db_admin`
\set worker_pw `cat /run/secrets/db_worker`
ALTER ROLE pandokos_owner   LOGIN PASSWORD :'owner_pw';
ALTER ROLE pandokos_gateway LOGIN PASSWORD :'gateway_pw';
ALTER ROLE pandokos_admin   LOGIN PASSWORD :'admin_pw';
ALTER ROLE pandokos_worker  LOGIN PASSWORD :'worker_pw';
CREATE DATABASE pandokos OWNER pandokos_owner;
REVOKE ALL ON DATABASE pandokos FROM PUBLIC;
GRANT CONNECT ON DATABASE pandokos TO pandokos_gateway, pandokos_admin, pandokos_worker;
SQL
```

`deploy/compose/.gitignore`: `secrets/`. The design ran D2, D3 and the `postgres` service of D1 (§12).

### D4. `test/compose`

`override.yaml`:

```yaml
services:
  migrate:
    image: ${PANDOKOS_TEST_IMAGE:?}
  pandokos:
    image: ${PANDOKOS_TEST_IMAGE:?}
    extra_hosts: ["host.docker.internal:host-gateway"]
```

`compose_test.go` (tag `compose`): copies `deploy/compose` and `deploy/postgres` into a temporary
directory with the same relative layout; runs `setup.sh` twice (the second run changes nothing);
`docker compose -p pdk-<random> -f compose.yaml -f <override> up -d --wait` (verified to return once
`migrate` exited 0 and `pandokos` runs), then polls `https://127.0.0.1:8080/readyz` until 200; starts
the fake provider with `fakeprovider.NewOn(t, "0.0.0.0:0")` (TLS off), and through the CLI in the container updates the `default` endpoints of
`openai` and `anthropic` to `http://host.docker.internal:<port>` with `--private`; then the steps of
§5.8 with `printf <canary credential>` as provider keys; makes one streamed call per family with
`net/http` and a pool holding only `secrets/tls_cert`; asserts the rest of T-567; `docker compose
down -v` in `t.Cleanup`, also on failure, after printing `docker compose logs --no-color` on failure.

### D5. `Makefile`

The targets `test-sdk-python`, `sdk-python-env`, `test-sdk-ts` and `test-compose` were added in part A
(step 11). Part D adds to `fuzz` the four lines of §7:

```make
	go test ./internal/family/anthropic/ -run '^$$' -fuzz '^FuzzAnthropicStreamReader$$' -fuzztime $(FUZZTIME)
	go test ./internal/family/anthropic/ -run '^$$' -fuzz '^FuzzAnthropicReadUsage$$' -fuzztime $(FUZZTIME)
	go test ./internal/family/anthropic/ -run '^$$' -fuzz '^FuzzAnthropicParseRequest$$' -fuzztime $(FUZZTIME)
	go test ./internal/family/sse/ -run '^$$' -fuzz '^FuzzFraming$$' -fuzztime $(FUZZTIME)
```

`check` is unchanged (it needs neither Python nor Node).

### D6. `.github/workflows/ci.yml` (new jobs; existing jobs unchanged except `image`)

```yaml
  sdk-python:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version-file: .go-version
          cache-dependency-path: test/sdk/go.sum
      - uses: actions/setup-python@6d4fd22b20465ccc0f7354476588e8df7c9ca0e0 # v7.1.0
        with:
          python-version: "3.13"
      - name: Install the locked packages
        run: |
          python -m venv local/sdk-venv
          local/sdk-venv/bin/pip install --require-hashes --no-deps --only-binary :all: -r test/sdk/python/requirements.txt
      - run: make test-sdk-python

  sdk-ts:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version-file: .go-version
          cache-dependency-path: test/sdk/go.sum
      - uses: actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1 # v7.1.0
        with:
          node-version: "22"
      - run: make test-sdk-ts
```

`make test-sdk-python` in CI uses the venv created by the install step (the target reads
`local/sdk-venv`, not `sdk-python-env`). In the `image` job, after "Container tests":

```yaml
      - name: Compose quick-start
        run: go test -tags compose -count=1 -timeout 15m ./test/compose/
        env:
          PANDOKOS_TEST_IMAGE: pandokos:ci
```

Network needs: `pypi.org`, `files.pythonhosted.org` and `registry.npmjs.org` during installation; Docker
Hub for the PostgreSQL image (already pulled by the test database). The tests themselves use loopback
and the host gateway only.

Before committing, re-verify the two new pins: `git ls-remote --tags https://github.com/actions/setup-python
refs/tags/v7.1.0` and the same for `actions/setup-node` must print the hashes above.

### D7. `.github/dependabot.yml`

```yaml
  - package-ecosystem: pip
    directory: /test/sdk/python
    schedule:
      interval: weekly
  - package-ecosystem: npm
    directory: /test/sdk/ts
    schedule:
      interval: weekly
  - package-ecosystem: docker-compose
    directory: /deploy/compose
    schedule:
      interval: weekly
```

**Verify:** `sh -n deploy/compose/setup.sh deploy/compose/initdb/10-pandokos.sh && make image &&
make test-compose && make sdk-python-env && make test-sdk-python && make test-sdk-ts`.

---

## Part E — Documentation and decision records (commit: `docs: sprint 3.3 documentation and ADRs`)

### E1. New pages

- **`docs/operations/compose.md`**: what the reference is (single instance, OPS-02) and is not
  (production at scale); prerequisites (Docker Compose v2, `openssl`, provider keys); the files of §5.8;
  the quick-start of §5.8 step by step, with the Python and TypeScript snippets for a streamed call to
  each family (`SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`) and curl (`--cacert`); reading the usage
  records (`usage list`) and the capability check; the security choices of §5.8 and why; how to
  replace the certificate, publish on another address, back up (`pgdata` volume, the KEK file kept
  apart), upgrade (new image: `docker compose up -d` runs `migrate` first), stop, and remove
  everything (`down -v` destroys the data); the expected KEK mode warning.
- **`docs/operations/providers.md`**: tier 1 (CAT-02); an OpenAI-compatible provider by configuration
  (the commands of §5.5), base URLs with a path, private endpoints, a placeholder credential for a server
  without keys, models and prices (unknown cost without a price); `stream_usage_option` and when to use
  `omit`; what is estimated then; the compatibility profiles CI proves (the table of §5.5); the manual
  check procedure (create the provider, store a real key, one plain and one streamed call through the
  SDKs with and without `include_usage`, read the record, record the result) and the table
  "Verified providers" (provider, server version, date, family, base URL, stream usage option, result,
  checked by) with rows for Groq, Mistral and Ollama marked "not yet run" until a maintainer runs them.

### E2. Changed pages

- `docs/operations/gateway.md`: §1 base URLs per SDK (table: OpenAI Python/TypeScript/Go
  `https://host/openai/v1`; Anthropic Python/TypeScript `https://host/anthropic`; compatible
  `https://host/<provider>/v1`), key headers per family, both families' operations; §2 unchanged; §3
  "What Pandokos changes" per family (the Anthropic lists of §5.2; no body change except the model; the
  version header never added; the stream usage option per provider); §4 the Anthropic error shape and
  type table, the shape of unrouted requests (§5.3); §4a streaming of Anthropic (observe only); a note
  on the 500 header values limit (D-19: Go's `net/http` answers `431 Request Header Fields Too Large`
  before Pandokos, without a Pandokos error and without a usage record); §10 the field
  `usage_output_source`.
- `docs/operations/usage.md`: Anthropic normalization (§5.4), mixed sources and what they mean for
  cost, `messages.count_tokens` records, `listUsageRecords` with its rules and representation, the
  permission.
- `docs/operations/container.md`: the endpoints table (Anthropic routes), the pre-release paragraph
  (link to the Compose guide), the response header list per family.
- `docs/operations/catalogue.md`: `stream_usage_option`; compatible providers (link).
- `docs/operations/keys.md`: the Anthropic key headers.
- `docs/reference/admin-api.md`: the two operations, the provider member, the CLI commands.
- `docs/reference/permissions.md`: `usage:read`; `model:read` also allows the capability check; the
  route table gains the two Anthropic operations.
- `docs/reference/data-flows.md`: Anthropic as a destination (`X-Api-Key`, the forwarded headers).
- `docs/architecture/overview.md` (families list as built: `openai`, `anthropic`; the admin API reads
  usage for the first time), `security-model.md` (the capability check reads metadata only; the
  Compose reference's trust choices), `data-model.md` (`provider.stream_usage_option`; the admin read of
  `usage_record`).
- `README.md`: links to the Compose and providers guides.

### E3. Decision records (status `proposed`, date of the commit)

**`docs/adr/0041-anthropic-api-family.md`**

```markdown
# 0041. The anthropic API family

- Status: proposed
- Date: <date of the commit>

Refines [ADR 0009](0009-native-passthrough-by-api-family.md), [ADR 0010](0010-usage-extraction.md)
and [ADR 0020](0020-error-contract.md).

## Context

ADR 0009 makes the API family the unit of implementation and names `anthropic` second, with messages
and token counting. Its SDKs send the key differently from OpenAI's (and changed how recently), its
streams report input usage in the first event and output in the last, its error body has its own shape,
and a request that reaches no provider must still get an error its SDK can parse.

## Decision

- Operations `messages` (`POST /v1/messages`) and `messages.count_tokens` (`POST
  /v1/messages/count_tokens`). Token counting consumes nothing and is recorded without usage or cost.
- The caller's key is read from `X-Api-Key` or `Authorization: Bearer`; when both are present they must
  be equal. The provider's key is sent as `X-Api-Key`. `Anthropic-Version` and `Anthropic-Beta` are
  forwarded as sent and never added.
- Forwarded request headers: `Accept`, `Content-Type`, `User-Agent`, `Anthropic-Version`,
  `Anthropic-Beta`. Passed response headers: `Content-Type`, `Content-Length`, `Retry-After`,
  `Request-Id`, `X-Should-Retry`, `Anthropic-*`. The provider's request ID is `Request-Id`.
- The body is forwarded as received except the model alias; nothing is spliced, because the provider
  reports usage in every stream.
- Usage: input includes both cache counts; the 1-hour cache write is `cache_write_1h_tokens`; server
  tool requests go to the other units. In a stream, `message_start` reports the input side and each
  `message_delta` replaces the members it carries. A stream that ends after `message_start` is recorded
  with the provider's input and an estimated output.
- A Pandokos error is `{"type":"error","error":{"type":<the provider's type for the status>,
  "message":"Pandokos: …"}}` with the headers of ADR 0020; no new code.
- A request that reaches no family gets the shape of the first family that recognizes its path or a
  header only that family's clients send (`Anthropic-Version`), else the `openai` shape. Status, code and
  message do not depend on the choice.

## Consequences

- The stock Anthropic SDKs work with a base URL and a key; records can mix provider and estimated
  sources, which the ledger already allows.
- A key change in the SDKs (header or scheme) is absorbed by accepting both headers; the SDK suite
  detects the next one.
- Every family adapter has two more small methods (`Recognizes`, `UpstreamRequestID`).

## Alternatives considered

**`X-Api-Key` only.** What the API documented first; refuses the current SDKs' default.
**Estimating the whole record when the stream is cut.** Simpler, and discards an exact input figure.
**Always the OpenAI shape for unrouted requests.** An Anthropic SDK would report a parse failure.
```

**`docs/adr/0042-openai-compatible-providers-and-stream-usage-option.md`**

```markdown
# 0042. OpenAI-compatible providers and the per-provider stream usage option

- Status: proposed
- Date: <date of the commit>

Amends [ADR 0024](0024-openai-stream-usage-option.md); refines [ADR 0009](0009-native-passthrough-by-api-family.md).

## Context

ADR 0009 says a provider that speaks an existing family is configuration, not code. Compatible servers
differ in small ways: where usage appears in a stream, extra usage members, and some refuse unknown
request members such as `stream_options`, which ADR 0024 adds to every chat stream that does not ask
for usage. CI can reach no provider and holds no key.

## Decision

- A compatible provider is a provider row of family `openai`, its endpoints (a base URL may carry a
  path; a self-hosted server is a private endpoint), models and the tenant's credential. No code is
  per vendor.
- Each provider has `stream_usage_option`: `add` (default; ADR 0024 unchanged) or `omit` (Pandokos never
  adds the option; streams whose caller did not ask for usage are read in observe mode and estimated
  when the server reports nothing). `omit` is refused for a provider of another family.
- CI proves the family against named compatibility profiles replayed by the fake provider. Real servers
  are checked by hand, and the result, with date and server version, is kept in the providers guide.

## Consequences

- An operator can serve a server that refuses the option without losing streaming.
- With `omit`, usage of such streams is estimated unless the caller asks for it; the record says so.
- The verification of real servers is documentation, not a test; it is repeated when an SDK or a
  provider's API changes.

## Alternatives considered

**One global switch.** Costs every other provider its exact stream usage.
**Per endpoint.** Splits one server's behaviour across its addresses for no gain.
**Retrying without the option after a refusal.** A retry (GW-11) and a second request the caller did not make.
```

**`docs/adr/0043-tenant-capability-check-and-usage-list.md`**

```markdown
# 0043. The tenant capability check and the minimal usage list

- Status: proposed
- Date: <date of the commit>

Refines [ADR 0035](0035-catalogue-and-model-access.md), [ADR 0037](0037-request-pipeline-and-resolution.md)
and [ADR 0039](0039-usage-record-admission-and-ledger-writer.md).

## Context

API-07 asks that an integrator can learn whether a tenant can call a provider and model now. The
gateway decides this at steps 7 and 8 with the catalogue cache and the vault, which opens the
credential. The administration plane must never decrypt a credential. The quick-start must also show
a usage record without SQL, while reports belong to a later release.

## Decision

- `GET /v1/tenants/{tenant}/capability?provider=&model=` (permission `model:read`) evaluates, on
  committed state, the gateway's rules for a key of the tenant without pin or model list: the catalogue
  rule (`catalog.Snapshot.Resolve`) and the credential resolution rule applied to credential metadata.
  It answers `callable`, the failing checks in a fixed order, and `budget: not_enforced` until limits
  exist. A suspended tenant is answered 403 `tenant_suspended` by the authorization rule. Nothing is
  opened, cached or audited.
- `GET /v1/tenants/{tenant}/usage-records` (new permission `usage:read`, held by `tenant-admin`) lists the
  tenant's records newest first in a window of at most 31 days, with a keyset cursor bound to the window
  and filter, read as the admin role in the tenant's session.

## Consequences

- The answer can differ from a call made at the same moment by at most the gateway's cache lifetimes.
- A key's pin and model list can only narrow the answer; they are not evaluated.
- The admin role's `SELECT` on the ledger, granted in 0006, is now used by a route.

## Alternatives considered

**An endpoint on the inference listener that runs steps 3 to 8 for the caller's key.** Exact for that
key and its caches, but an application concern rather than an integrator's, and it would open the
credential to answer a question.
**No list until reports exist.** The quick-start would need SQL as the database owner.
```

**`docs/adr/0044-sdk-compatibility-suite-python-typescript.md`**

```markdown
# 0044. The SDK compatibility suite in Python and TypeScript

- Status: proposed
- Date: <date of the commit>

Refines [ADR 0018](0018-supply-chain.md) and [ADR 0023](0023-test-dependencies-in-nested-modules.md).

## Context

GW-02 promises that stock SDKs work. Most callers use the Python and TypeScript SDKs of OpenAI and
Anthropic, which a Go test cannot import. Running them brings two package ecosystems into CI.

## Decision

- The suites live in `test/sdk/python` and `test/sdk/ts` and are run as subprocesses by Go tests of the
  `test/sdk` module behind the build tag `sdkexternal`, against the in-process server and the fake
  provider, with a loopback control server for assertions. Without the tag the module's tests are Go only.
- Dependencies are exact pins, locked with hashes (`requirements.txt` from `pip-compile
  --generate-hashes`; `package-lock.json`), installed with `--require-hashes --no-deps --only-binary`
  and `npm ci --ignore-scripts`, updated by Dependabot, used by CI only and never shipped.
- Tests are plain Python and ES modules; no TypeScript compiler.
- Network is needed only to install packages; the tests reach nothing but loopback.

## Consequences

- Each SDK update runs against Pandokos before it is merged.
- Two more CI jobs and two lock files to review.

## Alternatives considered

**Go SDKs only.** Misses the clients most callers use and their header behaviour (the Anthropic SDKs'
switch to Bearer was found this way).
**A container per language.** More to maintain than a pinned interpreter on the runner.
```

**History lines** (append to each ADR's "History"):

- 0009: `2026-11: sprint 3.3 adds the anthropic family and compatible providers by configuration ([ADR 0041](0041-anthropic-api-family.md), [ADR 0042](0042-openai-compatible-providers-and-stream-usage-option.md)).`
- 0010: `2026-11: sprint 3.3: a stream reader reports which totals it read; a record can have provider input and estimated output ([ADR 0041](0041-anthropic-api-family.md)).`
- 0018: `2026-11: sprint 3.3 adds hash-locked Python and npm packages for the SDK suite, CI only ([ADR 0044](0044-sdk-compatibility-suite-python-typescript.md)).`
- 0020: `2026-11: sprint 3.3 adds the Anthropic error shape and the rule that chooses the shape of a request no family serves; no new code ([ADR 0041](0041-anthropic-api-family.md)).`
- 0023: `2026-11: sprint 3.3: the nested module also runs the Python and TypeScript suites behind a build tag ([ADR 0044](0044-sdk-compatibility-suite-python-typescript.md)).`
- 0024: `2026-11: amended by [ADR 0042](0042-openai-compatible-providers-and-stream-usage-option.md): a provider can set stream_usage_option omit.`
- 0035: `2026-11: sprint 3.3: providers gain stream_usage_option; the capability check reuses the access rule ([ADR 0042](0042-openai-compatible-providers-and-stream-usage-option.md), [ADR 0043](0043-tenant-capability-check-and-usage-list.md)).`
- 0037: `2026-11: sprint 3.3: the shape of an unrouted request comes from the family that recognizes it; the upstream request ID comes from the adapter ([ADR 0041](0041-anthropic-api-family.md)).`
- 0039: `2026-11: sprint 3.3: the admin role lists a tenant's records ([ADR 0043](0043-tenant-capability-check-and-usage-list.md)).`
- 0040: `2026-11: sprint 3.3 adds the span attribute pandokos.usage.output_source.`

`docs/adr/README.md`: four index lines.

### E4. Planning files

`planning/epics/03-gateway/epic.md`: "Decisions it implements" gains 0041 to 0044. At close:
`status: done` in sprint.md and the maintainer's answers under §12.

**Verify:** `go test ./test/repo/ -count=1` (T-R26, T-R31, T-R32 read the guides).

---

## Part F — Verification

Run in this order and attach the output to the pull request:

```sh
make db-up
make check
make image && make test-container && make test-compose
make sdk-python-env && make test-sdk-python && make test-sdk-ts
make fuzz FUZZTIME=30s
git diff main -- go.mod test/sdk/go.mod                                  # prints nothing
git grep -n -e 'internal/gateway' -e 'internal/resolver' -- internal/admin   # prints nothing
git grep -n -e InsecureSkipVerify -e ProxyFromEnvironment -- cmd internal   # prints nothing
tail -1 internal/store/migrations/migrations.sum                           # 0007_provider_stream_usage.sql
docker compose -f deploy/compose/compose.yaml config -q                    # after deploy/compose/setup.sh
go test -race -count=20 -run 'TestAnthropicCancellation|TestAnthropicUsageRecord|TestUnroutedErrorShape' ./internal/gateway/
go test -count=1 -run 'TestCapabilityAgreesWithGateway|TestAnthropicEndToEnd|TestCompatibleProviderByConfiguration' ./test/e2e/ -v
```

Then check the definition of done of §10 line by line and fill the coverage table of §9 with the test
names that ran. Record in the pull request whether the manual check of `providers.md` was run.

## What the implementer does not do

- No other Anthropic or OpenAI path; no batch, file or model listing route.
- No change to an Anthropic body other than the model; no `Anthropic-Version` default.
- No retry, no fallback to `omit` after a provider refusal.
- No decryption, cache or audit in the capability check; no key-level evaluation.
- No report, rollup or export of usage; no usage read for platform roles.
- No new Go module, no TypeScript compiler, no CI step that reaches a provider or uses a provider key.
- No `allow_plaintext`, no port published beyond `127.0.0.1` in the Compose reference.
- No setting of `http.Server.MaxHeaderValueCount`.
