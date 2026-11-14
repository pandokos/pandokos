# Sprint 3.2 — Implementation plan

- Design: [sprint.md](sprint.md). Section numbers (§) refer to it.
- Each part below is self-contained: an agent needs sprint.md and that part only. Parts run in
  order A, B, C1 … C8, D, E, F.
- Suggested grouping for implementers: **G1** = C1, C2 (family readers; the `usage` leaf);
  **G2** = C3 (ledger writer, partitions, isolation lint); **G3** = C4, C5 (traces;
  configuration); **G4** = C6, C7, C8 (gateway, wiring, end to end). G1, G2 and G3 do not
  depend on each other after part A; G4 starts from the fakes of part B and needs G1 to G3
  merged only for C7 and C8. A, B, D, E and F are one agent each.

## Rules for the implementer

1. Branch `feat/3.2-streaming-usage-and-ledger` from `main`; sign off every commit. One commit
   for A, one for B (tests, failing), one per step of C (message given with the step), one for
   D, one for E.
2. **Dependencies are exactly those of §7**, added in part A with the commands given there.
   No other module is added; no version is typed by hand into `go.mod`. `go.mod` keeps
   `go 1.25.0` and no `toolchain` line (amended: floor 1.26.0, SDK v1.47.0 — see O-1).
3. **No content anywhere.** Nothing of a request or response body, no header value (metadata
   values included), no secret and no unresolved model string goes to a log, a span, an error
   or the ledger. The log fields of §5.5, the span attributes of §5.9 and the columns of §5.5
   are complete lists. The only place caller-supplied text is stored is
   `usage_record.metadata`, after `usage.ParseMetadata`.
4. **The stream is the provider's.** In observe mode the gateway forwards the slice it read;
   no code path may copy the reader's output to the caller there. No sleep, timer or channel
   sits between a read from the provider and the write to the caller.
5. **One `Admit`, one `Record`** (§5.1). Do not add a second place that calls `Record`; do not
   call `Admit` before authentication.
6. Appendix A (migration) and appendix B (queries) are copied byte for byte; both were applied
   to PostgreSQL 16 and generated with sqlc v1.31.1 as written. If one does not work as
   written, stop and ask. Do not "fix" a permission error of the gateway role with a grant:
   it holds `INSERT` on the listed columns and nothing else, by design (in particular it has no
   `SELECT`, which is why the statement says `ON CONFLICT DO NOTHING` without a target).
7. The OpenTelemetry SDK, the exporter and `logr` are imported in `internal/telemetry` only;
   the gateway imports the API packages `trace`, `attribute`, `codes`, `propagation`. Every SDK
   and exporter setting is passed explicitly; nothing relies on an `OTEL_*` variable.
8. Outbound HTTP still exists in `internal/egress` only (T-R25): the exporter gets an
   `http.Client` whose transport is `guard.Transport(true)`; no `http.Transport` literal is
   written.
9. Generated files are never edited by hand (`make generate`). SQL runs only as
   `queries.New(tx).<Method>` inside a `store` transaction helper (T-R10, T-R12).
10. Tests hold for root and non-root; nothing sleeps longer than 2 s; waiting is
    `testutil.Eventually`; a bound is never made to pass by lengthening it and a database test
    never by skipping it. Test values that look like keys are the canaries of `test/fixtures`
    or start with `synthetic-`.
11. English everywhere; every exported identifier has a doc comment.

## Environment

- `make db-up` starts PostgreSQL 16 with TLS and PgBouncer (Docker) and writes
  `local/testdb/env`; `make db-down` discards them.
- `make test` (root module, race detector, needs the database), `make test-sdk`, `make image`
  then `make test-container`, `make fuzz`, `make bench`.
- `make generate` (`sqlc` needs the release toolchain of `.go-version`), `make generate-check`,
  `make migrations-sum`, `make lint`, `make vuln`, `make secrets`, `make check`.
- Before starting: `make db-up && make check` must pass on `main`.

---

## Part A — Scaffolding (commit: `chore: usage ledger scaffolding, migration 0006, telemetry dependencies`)

After this part product code compiles (`go build ./...`) and every existing test still passes.
It adds declarations and dependencies only: nothing changes behaviour.

**Files:** `go.mod`, `go.sum`, `test/sdk/go.sum`, `.golangci.yml`,
`internal/store/migrations/sql/0006_usage_ledger.sql`, `migrations.sum`,
`internal/store/queries/sql/usage.sql`, generated `internal/store/queries/*`,
`internal/family/{family,adapter,flatten}.go`, `internal/family/openai/{route,adapter,request,usage,estimate,stream_responses}.go`,
`internal/usage/{doc,record,id,metadata}.go`, `internal/ledger/{doc,writer,partitions}.go`,
`internal/telemetry/traces.go`, `internal/gateway/{ports,handler}.go`,
`internal/server/server.go`, `test/otlptest/otlptest.go`, `test/testutil/gateway.go`.

1. **Migration and queries.** Create `0006_usage_ledger.sql` from appendix A and
   `usage.sql` from appendix B, byte for byte; `make migrations-sum` (one new line);
   `make generate`; commit the generated files.
2. **Dependencies.**

   ```sh
   go list -m -versions go.opentelemetry.io/otel | tr ' ' '\n' | grep -x 'v1.46.0'   # must print v1.46.0
   go get go.opentelemetry.io/otel@v1.46.0 go.opentelemetry.io/otel/sdk@v1.46.0 \
          go.opentelemetry.io/otel/trace@v1.46.0 \
          go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp@v1.46.0
   # after steps 3 to 9 exist (they import the packages):
   go mod tidy && (cd test/sdk && GOTOOLCHAIN=go$(cat ../../.go-version) go mod tidy)
   grep -x 'go 1.25.0' go.mod && ! grep -q '^toolchain' go.mod
   go list -m go.opentelemetry.io/proto/otlp google.golang.org/protobuf github.com/go-logr/logr
   ```

   The last command printed `v1.11.0`, `v1.36.12` and `v1.4.4` when the design was written;
   whatever `go mod tidy` resolves from the four pins is what is committed. If `go.mod` no
   longer says `go 1.25.0` (amended: floor 1.26.0, SDK v1.47.0 — see O-1), a pin is wrong: stop and ask.
3. **Linter** (`.golangci.yml`, `depguard`): add to the allow-list of rule `all`
   `go.opentelemetry.io/otel`, `go.opentelemetry.io/proto/otlp`, `google.golang.org/protobuf`,
   `github.com/go-logr/logr`. New rules: `otel-sdk-in-telemetry` (lax; files `**/cmd/**`,
   `**/internal/**`, not `**/internal/telemetry/**`, not tests; deny
   `go.opentelemetry.io/otel/sdk`, `go.opentelemetry.io/otel/exporters`,
   `github.com/go-logr`, `go.opentelemetry.io/proto`, `google.golang.org/protobuf`);
   `usage-is-a-leaf` (strict; files `**/internal/usage/**`, not tests; allow `$gostd`,
   `…/internal/tenant`, `…/internal/family`, `…/internal/pricing`); `ledger-is-wiring` (lax;
   `**/internal/**` except `**/internal/ledger/**` and tests; deny `…/internal/ledger`).
   Extend `gateway-takes-plain-values` with a deny for `…/internal/ledger` and
   `…/internal/telemetry`.
4. **`internal/family`.** Add `Usage.Reported`, `StreamReader.Deltas`, `Evidence`,
   `Adapter.EstimateUsage`, `HeaderMetadataDropped`, and `FlattenUsage` (stub: returns nil).
5. **`internal/family/openai`.** Add the four constants and the functions of §5.10 with stub
   bodies: `ParseOperation` calls `Parse`; `ReadUsageOf` calls `ReadUsage`;
   `NewStreamReaderFor` returns `NewStreamReader(strip)`; `Estimate` returns the zero value;
   `(*StreamReader).Deltas` returns 0. The adapter's methods call the `…Of`/`…For` functions
   with `op.Name`. `operations` and `Match` are **not** changed yet.
6. **`internal/usage`.** `doc.go` and the declarations of §5.5 in their final form. Stubs:
   `NewID` returns `""`, `Validate` returns nil, `ParseMetadata` returns `nil, 0`.
7. **`internal/ledger`.** `Options`, `Writer`, `Stats`, `Partitioner` and their methods (§5.6).
   Stubs: `New` returns `errors.New("ledger: not implemented")`; `Admit` true; the rest no-ops.
8. **`internal/telemetry/traces.go`.** The declarations of §5.9. Stub: `NewTraces` returns a
   `Traces` whose provider is `sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))`
   and which imports the exporter and `logr` packages (blank imports are acceptable in this
   commit only), so that `go mod tidy` keeps the requirements.
9. **Gateway and server.** `ports.go`: `Recorder`, `Target.Prices`, `SpanAttributes` (the full
   list of §5.9). `Options` gains `Recorder`, `Tracer`, `PropagateTrace`; `New` accepts nil for
   the first two for now. `server.Options` gains `Recorder` and `Tracer`, passed through.
   `test/otlptest/otlptest.go`: the `Collector` type with its methods declared and a handler
   that decodes `ExportTraceServiceRequest` (final form: it is a helper, not product code).
   `test/testutil`: `MemoryRecorder` and `SpanRecorder` in final form.

**Verify:** `go build ./... && make generate-check && make lint && make test`.

---

## Part B — Tests first (commit: `test: sprint 3.2 tests, failing`)

Write every test of §9 and adapt every changed test before any file of part C, against the
**final** API of sprint.md. Product code still compiles (`go build ./...`); tests fail or, where
an option became required (`Recorder`, `Tracer`), are already adapted to pass it. Run
`go vet ./... ; make test ; make test-sdk` and keep the output for the pull request.

General rules: a test that sweeps "every operation", "every outcome", "every column" or "every
attribute" ranges over the product's own list (`Adapter.Operations()`, the constraint names read
from the migration file, `gateway.SpanAttributes`) and fails on an entry it does not know.
Canary assertions use `testutil.AssertNoCanary` and `testutil.AssertAbsent`. A streaming test
never infers "no buffering" from timing: it uses the fake's gate (the provider does not send
event *n+1* until the test has read event *n*).

### B1. Helpers and fixtures

- `test/fixtures/openai`: the seven files of §7, synthetic, canaries as the only content.
  `responses.json` has `usage` with `input_tokens_details.cached_tokens` and
  `output_tokens_details.reasoning_tokens`; `responses_stream.sse` uses `event:` and `data:`
  lines, `response.output_text.delta` events and a final `response.completed` whose `response`
  repeats the output and carries `usage`; `chat_stream_tools.sse` has `delta.tool_calls`
  chunks; `chat_stream_error.sse` ends with `data: {"error":{…}}` and no `[DONE]`;
  `chat_stream_keepalive_crlf.sse` has `: ping` events and CRLF endings.
- `test/fakeprovider`: routes and behaviours of §7; `Requests()` unchanged. Every stream obeys
  the gate; `fake-tiny-writes` flushes after each byte; `fake-cut` hijacks and closes.
- `test/testutil`: `TargetFor` sets `Prices` to one version (`synthetic` IDs, USD, input and
  output rates) with `ValidFrom` an hour ago; `TwoPrices(before, after time.Time)`.
- `test/e2e`: `usageRows(t, d, tenantID) []map[string]any` (superuser connection of
  `dbtest`, `SELECT to_jsonb(u) FROM pandokos.usage_record u WHERE tenant_id = $1 ORDER BY
  started_at, id`), `(*proc).collector(t)`; `opts` gains `ledgerBuffer`, `otlpEndpoint`,
  `propagate`.

### B2. Isolation

`test/isolation/spec.go`: `Grants["usage_record"]` = gateway `INSERT(<the 44 columns of the
grant of appendix A, sorted>)`, admin `SELECT`, worker `SELECT`; `Fixtures["usage_record"]` =

```sql
INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, funding,
                                   provider, api_family, operation, outcome, duration_ms, input_source,
                                   output_source, cost_status)
VALUES ($1, pandokos.uuid_v7(), now(), md5(random()::text), 'service_account', gen_random_uuid()::text, 'byok',
        'openai', 'openai', 'chat.completions', 'ok', 1, 'none', 'none', 'none') RETURNING id::text
```

with `Update: "model = 'changed'"`; `AppendOnly` += `usage_record`; `SecurityDefiner` +=
`ensure_usage_partitions`. Declare rule L-20 in `lint.go` as a function that returns no finding
yet (C3 implements it); T-481 expects its findings. T-480 to T-482 in
`test/isolation/usage_test.go`.

### B3. Where each test lives

| Tests | File |
|---|---|
| T-456 to T-462, T-465 | `internal/family/openai/{adapter,request,usage,stream,stream_responses,estimate}_test.go` |
| T-463, T-464 | `internal/family/flatten_test.go`; `FuzzReadUsage` extended in place |
| T-466 to T-469 | `internal/usage/{metadata,id,record}_test.go` |
| T-470, T-472, T-478 | `internal/ledger/writer_test.go` (package `ledger`, with an in-package fake of the store seam) |
| T-471, T-473 to T-477 | `internal/ledger/writer_db_test.go`, `partitions_db_test.go` |
| T-479 | `internal/store/queries/queries_db_test.go` |
| T-483 | `internal/resolver/resolver_db_test.go` |
| T-484 to T-498 | `internal/gateway/{record,usage,stream,metadata,spans}_test.go`; T-426, T-438, T-439 adapted in `pipeline_test.go` |
| T-499 to T-503 | `internal/telemetry/traces_test.go` |
| T-504, T-505 | `internal/config/{ledger,telemetry}_test.go`; T-04 to T-15 adapted |
| T-506, T-507 | `internal/server/server_test.go` |
| T-508 to T-512 | `test/e2e/ledger_test.go`, `traces_test.go`; T-451 adapted |
| T-513, T-514 | `test/sdk/sdk_test.go` |
| T-R28 to T-R30 | `test/repo/{repo,gateway,usage}_test.go` |

Notes for particular tests:

- T-462: the fuzz input is `(data []byte, cuts []byte, strip bool)`; chunk boundaries come from
  `cuts`. The "events removed" property is checked by re-splitting input and output into events
  with the test's own splitter (blank line, LF or CRLF), not with the product's.
- T-469 and T-R29 read constraint and column names from the migration file with a regular
  expression; they do not connect to a database.
- T-473, T-509: use `(*dbtest.Database).SetAcceptingConnections`. T-509 sets
  `ledger.buffer_size` and `ledger.batch_size` to 100 and finishes its buffering phase within
  the cache lifetimes (30 s by default).
- T-474: wrap the writer's store seam so that the first call commits and then returns
  `io.ErrUnexpectedEOF`.
- T-484: build the cases from two lists (how the request is made to end; expected outcome and
  code) and assert `len(recorder.Records()) == 1` after `testutil.Eventually`, since a record
  of an aborted handler arrives after the client saw the connection close.
- T-490: register a test adapter in `Families` that embeds `openai.Adapter()` and overrides
  `NewStreamReader` and `ReadUsage`.
- T-500: "under 500 ms" is the design's bound for 10 000 span ends; do not raise it.
- T-512: the collector records raw bodies; the canary search runs on the raw bytes (protobuf
  strings are plain UTF-8) and on the decoded spans.

---

## Part C — Implementation

Each step ends with its tests green and `make lint` clean.

### C1. Family readers — `feat: responses and embeddings in the openai family, estimation, reported usage`

**Files:** `internal/family/flatten.go`, `internal/family/openai/*.go`. **Tests:** T-456 to
T-465; `make fuzz FUZZTIME=30s` for the three reader targets.

- `FlattenUsage`: decode with `json.Decoder` and `UseNumber`; walk objects to depth 3; keep a
  member when its value is a JSON number that `strconv.ParseInt` accepts and is not negative;
  key = path joined by `.`; skip keys that do not match `[a-z0-9_.]{1,64}`; stop at 32 entries
  (members are visited in input order). Anything that is not an object gives nil.
- `route.go`: three operations; `Match` exact on method and path, as today.
- `request.go`: `ParseOperation` scans once with the existing scanner. `responses`: after the
  model rules, the last top-level `background` member whose raw value is `true` gives
  `family.Invalid("background_not_supported")`. `embeddings`: the request's `Stream()` is
  false and `Prepare` never splices the usage option. Keep `Parse` and `PrepareRequest` and
  their fuzz targets meaning chat. Change the message of `CodeInvalidRequest` (§5.2).
- `usage.go`: one wire struct per operation; every reader sets `Reported =
  family.FlattenUsage(<raw usage object>)` (decode `usage` as `json.RawMessage` first). A
  negative count is an error, as today.
- `stream_responses.go`: same event framing as the chat reader (share `eventEnd` and the
  `data:` extraction; do not duplicate them). Bound 4 MiB. Decode a complete event's data into
  `{Type string; Response *struct{Usage json.RawMessage}}` only when the data contains the
  bytes `"usage"` or `.delta"`; `Filter` returns `p`; `Flush` returns nil.
- `stream.go`: `Deltas` counts chunks whose `choices` is non-empty. No other change to the
  chat reader's behaviour: its 1.1 tests stay green untouched.
- `estimate.go`: the table of §5.4; `ceil(n/4)` as `(n+3)/4`.

### C2. The `usage` package — `feat: usage record, record IDs, caller metadata`

**Files:** `internal/usage/*.go`. **Tests:** T-466 to T-469; fuzz `FuzzParseMetadata`.

- `NewID`: 6 bytes of `t.UnixMilli()`, 10 bytes of `crypto/rand`, version 7 and variant bits
  set; canonical lower-case form. A failure of `crypto/rand` panics (it does not fail on
  supported platforms).
- `ParseMetadata`: iterate the header map; a name qualifies when its canonical form starts with
  `X-Pandokos-Metadata-` and is not exactly `X-Pandokos-Metadata-Dropped`; apply the table of
  §5.3; sort keys; keep the first 8. It allocates nothing when no header qualifies.
- `Validate`: one check per constraint of appendix A that the gateway could violate (formats,
  lengths, sources against values, cost status against cost, metadata bounds, sizes of the two
  maps). Errors are constants per field.

### C3. Ledger writer, partitions, lint — `feat: usage ledger writer with admission; partition keeper; lint rule for partitions`

**Files:** `internal/ledger/*.go`, `test/isolation/lint.go`. **Tests:** T-470 to T-482, T-140.

- **Store seam** (unexported): `type sink interface { insert(ctx context.Context, t tenant.ID,
  records []usage.Record) error }`; the real one runs `db.WithTenant(ctx, t, …)` with
  `queries.New(tx).InsertUsageRecords(ctx, json)` under a 5 s context.
- **`rows.go`**: an unexported struct with one field per column of appendix B's record
  definition and matching `json` tags. Times as RFC 3339 with microseconds in UTC; durations as
  whole milliseconds; the cost as its decimal string (`Decimal.String()`), `null` unless
  known; optional values as `null`; the three maps as objects, `{}` when empty;
  `cost_fallbacks` as an array of category names. Provider columns are filled only when the
  source is `provider`, estimate columns only when `estimated` (`cache_*` follow the input
  source, `reasoning_tokens` the output source).
- **State**: a mutex guarding `inUse int`, the queue (a slice), `saturated`, `closing`; a
  buffered channel of size 1 to wake the loop. `Admit`: false when closing or `inUse ==
  BufferSize` (then `saturated = true`); else `inUse++`. `Record`: `Validate`; an invalid
  record is logged as rejected and its slot freed; else appended and the loop woken. A `Record`
  without a free-standing `Admit` (in-use count would not cover it) is logged once at error
  (`usage record without admission`) and still queued.
- **Loop**: as §5.6. Free slots only after the transaction that holds the rows commits. After
  each successful write, clear `saturated` when `inUse <= BufferSize/2`. Back-off timer and
  flush timer must stop when `Close` is called.
- **`Close`**: set `closing`; write until `inUse == 0` or the context ends; then log and drop
  what remains (queued records are logged in full; slots of handlers that never recorded are
  counted in the final line as `missing`); make `Run` return. Idempotent.
- **`Partitioner`**: `Ensure` runs `queries.New(tx).EnsureUsagePartitions(ctx, 3)` in
  `db.Unscoped` under 10 s; `Run` calls it every `Interval` until the context ends, logging a
  failure at warn and continuing.
- **Lint L-20** (`test/isolation/lint.go`): read relations with `relispartition` and their
  parent through `pg_inherits`; apply (a) to (d) of §5.7 with the existing readers for owner,
  privileges, policies and triggers. Update the doc comment (twenty rules).

### C4. Traces — `feat: traces over OTLP with the OpenTelemetry SDK`

**Files:** `internal/telemetry/traces.go`. **Tests:** T-499 to T-503.

Build, in this order: resource with `resource.NewSchemaless` (§5.9; `service.instance.id` from
`crypto/rand`); sampler; span limits passed with `WithRawSpanLimits` (attribute count 64,
attribute value length 256, events and links 0); when `Endpoint` is set, the exporter with
`WithEndpointURL(endpoint + "/v1/traces")` (join paths without doubling the slash),
`WithHeaders`, `WithTimeout`, `WithCompression(NoCompression)`, `WithRetry(RetryConfig{Enabled:
false})`, `WithHTTPClient(&http.Client{Transport: o.Transport, CheckRedirect: <return
http.ErrUseLastResponse>})`, and a batch processor with queue 2048, batch 512, delay 2 s,
export timeout `o.Timeout`. Without an endpoint: sampler `NeverSample`, no processor. Do not
call `otel.SetTracerProvider`: the provider is passed, not global. `otel.SetLogger` and
`otel.SetErrorHandler` are process-wide: set them in `NewTraces`, routed to `o.Logger`; the
error handler counts and logs `trace export failing` at most once a minute with `count` and
`error_class` (`timeout`, `connection`, `status`, `other`), never the error text. `Shutdown`
calls the provider's `Shutdown` under the given context and returns its error.

### C5. Configuration — `feat: ledger and telemetry settings`

**Files:** `internal/config/{config,keys,validate,load}.go`, `docs/operations/configuration.md`
(the generated reference, if the repository test requires it in the same commit).
**Tests:** T-504, T-505, T-04 to T-15, T-R5.

The eleven keys of §5.11 with their defaults, validation and parsed forms; the headers file is
read like the other secret files and never quoted in a problem; the two warnings. All eleven
are read for *serve* only.

### C6. Gateway — `feat: usage record for every admitted request; metadata; spans; soft-failing stream wrapper`

**Files:** `internal/gateway/{handler,proxy,body,ports,record,spans}.go`. **Tests:** T-484 to
T-498, T-426, T-438, T-439 and every 1.1 and 3.1 gateway test.

- `New`: `Recorder` and `Tracer` are required. The tracer is
  `o.Tracer.Tracer("github.com/pandokos/pandokos",
  trace.WithInstrumentationVersion(buildinfo.Version))`; the propagator is a
  `propagation.TraceContext{}` value held by the handler (no global).
- `authenticate`: after a principal exists and before the decisions: `Admit` (false: `fail`
  with `unavailable`, mark `ledger: full`, return); `usage.ParseMetadata`; set
  `X-Pandokos-Metadata-Dropped` when not zero; `usage.NewID(st.start)`; extract the trace
  context from the inbound header and start the server span with `trace.WithTimestamp(st.start)`
  and `trace.WithSpanKind(trace.SpanKindServer)`. The server span's context replaces the
  request context for the rest of the handler.
- Step 9: start the client span just before `h.proxy.ServeHTTP`; in `rewrite`, when
  `PropagateTrace`, inject with the propagator into the outbound header from the client span's
  context. Nothing else about `traceparent` changes: it is not on the allow-list.
- `body.go`: observe mode reads from the provider **into the caller's buffer** and hands the
  same slice to the reader inside a function that recovers; strip mode keeps today's path with
  the recover added. All three wrappers note the time of the first byte. `Deltas`, `Usage` and
  `ReadUsage` are called through the same recovering helper in `finish`.
- `record.go`: `outcomeOf` (the table of §5.5, from the state and the log outcome),
  `sourcesOf` (the first table of §5.4), the estimate (`Evidence` from `st.requestBytes`, the
  wrapper's byte count and `Deltas`), the cost (`pricing.VersionAt(st.target.Prices,
  st.start)`, then `pricing.CostOf`), and the record. `Stream`, bytes, endpoint, model and
  credential are set only when their step passed.
- `finish`: when the request was admitted: compute the record inside a recovering function
  (fallback: the minimal record); end the client span, `Record`, end the server span; then the
  log line. The existing re-panic at the end stays last.
- `spans.go`: attribute keys as constants, `SpanAttributes` built from them; status per §5.9.
- The log line: fields of §5.5; `ttfb_ms` from the first-byte time.

### C7. Wiring — `feat: serve writes the usage ledger and exports traces`

**Files:** `internal/resolver/catalogue.go`, `internal/server/server.go`,
`cmd/pandokos/serve.go`. **Tests:** T-483, T-506, T-507, the server tests.

`resolver`: take the snapshot once per `Resolve` and fill `Prices` from it. `server`: require
`Recorder` and `Tracer`; pass `PropagateTrace`. `serve`: the sequence of §5.12; reveal the
collector headers only when building `TracesOptions`; after `srv.Serve` returns run
`writer.Close` and `traces.Shutdown` concurrently under one context of
`ledger.shutdown_flush_timeout`, log `ledger flushed` with `unwritten`, then let the deferred
cleanup run. `writer.Run` gets `context.Background()`-derived context cancelled only after
`Close` returned.

### C8. Everything together — `test: end-to-end, SDK and repository suites pass`

**Files:** whatever the remaining failures point at; no design change. **Tests:** T-508 to
T-514, T-R28 to T-R30, the whole of `make check`.

---

## Part D — CI and container (commit: `test: container test leaves a usage row; fuzz and bench targets`)

**Files:** `Makefile`, `test/container/container_test.go`, `docs/operations/container.md` (the
shutdown paragraph).

1. `Makefile`, target `fuzz`: add lines for `FuzzResponsesStreamReader`
   (`./internal/family/openai/`), `FuzzFlattenUsage` (`./internal/family/`),
   `FuzzParseMetadata` (`./internal/usage/`). Target `bench` is unchanged (`-bench .` runs the
   new benchmark).
2. Container test (T-72): after the streamed call, one row exists for the tenant in
   `usage_record` with `outcome = 'ok'` and `input_source = 'provider'`; `docker stop` with the
   default grace of the test still exits 0.
3. No workflow change: both toolchains already run `make test`; Dependabot already covers
   `go.mod`. Confirm with `git diff main -- .github` (prints nothing).

**Verify:** `make image && make test-container && make fuzz FUZZTIME=30s && make bench`.

---

## Part E — Documentation and decision records (commit: `docs: sprint 3.2 documentation and ADRs`)

### E1. New pages

- **`docs/operations/usage.md`**: what a usage record is and when one is written (every
  admitted request; the two cases without one: not authenticated, buffer full); every column
  with its meaning; outcomes, sources and cost statuses; the estimation rules and their
  coarseness; cost is informational under BYOK, in the price's currency; metadata headers with
  their bounds, the dropped counter, the warning about personal data; the writer (batching,
  the four keys, sizing `ledger.buffer_size`, the loss window, what `ledger write failing`,
  `usage record rejected` and `usage record not written` mean and how to rebuild rows from the
  last); readiness; partitions (names, the hourly job, the default partition and its warning);
  inspecting rows with SQL as the owner or in a tenant session; that no API reads usage yet.
- **`docs/operations/telemetry.md`**: the keys; the collector connection (through the egress
  rules, `upstream.tls.ca_file`, no proxy, no redirect); both spans with every attribute of
  `gateway.SpanAttributes`; sampling and what a caller's flag does; propagation; the
  environment rule and the one exception; what is dropped when the collector is away; that no
  content is ever attached and no setting enables it.
- **`docs/reference/data-flows.md`** (PRV-02): a table with one row per destination (provider,
  OTLP collector, database, standard output) and the columns "receives" and "never receives";
  for providers: the body as sent by the caller with the two documented changes, the allowed
  headers, the tenant's credential, optionally `traceparent`; never the Pandokos key, metadata,
  tenant or principal identifiers.

### E2. Changed pages

`docs/operations/gateway.md` (three operations; `background`; metadata headers; streaming
behaviour and the one observable difference of ADR 0024; `ttfb_ms`; 503 when the ledger is
full; log fields); `configuration.md` (eleven keys, two warnings); `container.md` (readiness
check `ledger`; shutdown bound and stop grace period); `database.md` (new section "Grants of
migration 0006" and "Usage partitions"; the function in the list of definer functions);
`docs/architecture/data-model.md` (the usage ledger section rewritten from §5.5 and §5.7;
`upstream_model` instead of "model as reported by the provider"); `overview.md` (pipeline step
names, failure table row for PostgreSQL with the admission rule, package list: `usage`,
`ledger`, `telemetry`); `security-model.md` (rows: metadata and trace context are recorded as
declared and read only after authentication; the collector as a destination; the ledger's
`INSERT`-only gateway role); `docs/reference/permissions.md` (the two new operations need the
same pair); `README.md` links.

### E3. Decision records (status `proposed`, date of the commit)

**`docs/adr/0039-usage-record-admission-and-ledger-writer.md`**

```markdown
# 0039. Usage record, admission and the ledger writer

Refines [ADR 0010](0010-usage-extraction.md), [ADR 0013](0013-ledger-and-audit.md) and
[ADR 0037](0037-request-pipeline-and-resolution.md).

## Context

ADR 0013 fixes one row per request, a batching writer, a bounded buffer and readiness tied to
it; ADR 0010 fixes provider values, estimates and their marking. Building them needs a record
layout that cannot hold content, a rule for when a value is estimated, a way to keep "exactly
one record" true while the buffer is bounded, and a way to write rows of many tenants under
row-level security and to create partitions with roles that own nothing.

## Decision

1. A request is **admitted** after authentication by taking a slot of the ledger buffer; the
   slot is freed when its row is committed. Without a free slot the request is refused with
   `unavailable` and has no record. Every admitted request produces exactly one record,
   whatever its outcome. Anonymous requests produce none.
2. The record has separate columns for provider-reported and estimated token counts and a
   source per total (`provider`, `estimated`, `none`), enforced by check constraints. The
   provider's own counters are kept as a flat map of integers. Every text column is
   format-checked; no column can hold a body.
3. Usage is estimated only when the provider answered 2xx and no usage could be read. No
   answer and error statuses are recorded without usage. For the `openai` family the input
   estimate is the request size divided by four, the output estimate one token per content
   event of a stream or the response size divided by four.
4. Cost is computed when the request ends, with the price version in force at its start, from
   the versions delivered with the resolved model; the status is known, unknown with the
   reason, or none.
5. The writer groups waiting records by tenant and inserts each group in a transaction of
   that tenant; the gateway role holds `INSERT` only. Record IDs are made by the gateway and
   the insert ignores conflicts, so a repeated batch writes each row once. A record the
   database refuses is logged and dropped without holding up the others.
6. On shutdown the writer flushes under its own deadline after the drain; records it cannot
   write are logged in full.
7. The table is partitioned by UTC month with a default partition. A `SECURITY DEFINER`
   function, executable by the worker role, creates partitions ahead of time; partitions carry
   no grants and the same forced policy as the parent.
8. Callers attach metadata as `X-Pandokos-Metadata-<key>` headers, at most eight short ASCII
   entries, read after authentication, stored on the record, never forwarded; entries outside
   the bounds are dropped and counted.
9. No API reads the ledger yet; the admin and worker roles hold `SELECT` under the tenant
   policy.

## Consequences

The buffer size is also the number of requests an instance admits at once. A request refused
for a full buffer is the one authenticated request without a record. Failed provider calls
show no consumption. Estimates are coarse and marked. A process that is killed loses the
records in memory, as ADR 0013 states.

## Alternatives considered

Bounding only finished records (in-flight requests could then exceed the bound or lose their
record); a definer function inserting whole batches across tenants (a special path around the
policy); estimating every forwarded request (errors would look like consumption); storing the
provider's usage object as received (a channel for text); no default partition (a missing
partition would reject records); metadata in one JSON header (parsing ambiguity).
```

**`docs/adr/0040-traces.md`**

```markdown
# 0040. Traces: SDK, span model, sampling and propagation

Refines [ADR 0015](0015-observability.md), [ADR 0018](0018-supply-chain.md) and
[ADR 0038](0038-egress-guard-upstream-tls-and-proxy.md).

## Context

ADR 0015 chooses the OpenTelemetry SDK, a server span and a client span joined to the caller's
trace, and no content. Building it needs a version that fits the Go floor, a rule for who can
cause spans, a sampling rule, and a way to reach the collector that obeys the outbound rules.

## Decision

1. The OpenTelemetry Go SDK and the OTLP/HTTP trace exporter are direct dependencies, pinned
   to the newest release line that supports the Go floor of `go.mod`. Only
   `internal/telemetry` imports them; the gateway uses the API packages. OTLP over gRPC is not
   offered.
2. Every setting is passed explicitly from Pandokos's configuration; `OTEL_*` variables are
   not configuration. The SDK still adds `OTEL_RESOURCE_ATTRIBUTES` to the resource; this is
   documented and warned about.
3. The exporter reaches the collector through the egress guard's private transport: `http` or
   `https`, verified certificates, no proxy, no redirect, forbidden addresses refused.
4. Spans are created only for admitted requests. The server span is created after
   authentication with the request's start time; a valid `traceparent` of the caller is its
   parent. The provider span is its child and covers the call until the response body ends.
5. Sampling is parent-based over a configured ratio. Export is asynchronous through a bounded
   queue that drops.
6. Attributes are a fixed list under `http.*` and `pandokos.*`; token counts follow with the
   `gen_ai.*` names in the metrics sprint. No attribute holds content, a secret, a URL, caller
   metadata or an unresolved model name.
7. Trace context is sent to the provider only when the operator enables it, and it is
   Pandokos's own span context.
8. Without a collector endpoint nothing is exported; trace IDs still exist for the ledger and
   the log.

## Consequences

Twenty modules enter the dependency graph, gRPC among them through the HTTP exporter. An
authenticated caller can force sampling of its own requests. Upgrading the SDK past the pinned
line needs a higher Go floor.

## Alternatives considered

A hand-written OTLP/JSON exporter (no dependency, and a second telemetry stack when metrics
arrive); raising the Go floor now; spans for every request (anonymous callers would drive
telemetry); ignoring the caller's sampled flag (breaks the caller's traces); the SDK's
environment configuration (settings outside ADR 0019).
```

**History lines** (append to the `## History` section, creating it where missing):

- 0009: `2026-11: sprint 3.2 adds the operations responses and embeddings to the openai family; a responses request that asks for background processing is refused.`
- 0010: `2026-11: sprint 3.2 estimates only for a 2xx answer without readable usage, stores the provider's counters as a flat map of integers, and makes the forwarder independent of the reader's output when nothing is removed ([ADR 0039](0039-usage-record-admission-and-ledger-writer.md)).`
- 0013: `2026-11: sprint 3.2 builds the ledger: admission by buffer slots, one transaction per tenant, idempotent inserts, a flush deadline of its own, monthly partitions with a default ([ADR 0039](0039-usage-record-admission-and-ledger-writer.md)).`
- 0015: `2026-11: sprint 3.2 builds traces over OTLP/HTTP; spans exist for admitted requests only ([ADR 0040](0040-traces.md)).`
- 0018: `2026-11: sprint 3.2 adds the OpenTelemetry SDK, the OTLP/HTTP exporter and logr as direct dependencies ([ADR 0040](0040-traces.md)).`
- 0019: `2026-11: sprint 3.2: OTEL_* variables are not configuration; the SDK still reads OTEL_RESOURCE_ATTRIBUTES, which is warned about.`
- 0020: `2026-11: sprint 3.2: invalid_request also covers a responses request that asks for background processing; unavailable also covers a full ledger buffer.`
- 0024: `2026-11: sprint 3.2 confirms the decision for chat streams; the responses and embeddings operations need no option.`
- 0027: `2026-11: sprint 3.2 adds the definer function ensure_usage_partitions, executable by the worker role only, and lint rule L-20 for partitions.`
- 0037: `2026-11: sprint 3.2 adds admission and attribution after authentication, and the usage record and spans at the end of every admitted request ([ADR 0039](0039-usage-record-admission-and-ledger-writer.md), [ADR 0040](0040-traces.md)).`

`docs/adr/README.md`: two index lines.

### E4. Planning files

`planning/epics/03-gateway/epic.md`: "Decisions it implements" gains 0024, 0039, 0040;
`planning/roadmap.md` unchanged. At close: `status: done` in sprint.md and the maintainer's
answers under §12.

**Verify:** `go test ./test/repo/ -run 'TestUsageAndTelemetryGuides|TestGatewayGuide|TestReferenceDocuments|TestConfigurationReference|TestDatabaseGuide' -count=1`.

---

## Part F — Verification

Run in this order and attach the output to the pull request:

```sh
make db-up
make check                      # fmt, vet, lint, tidy, generate-check, secrets, tests, SDK suite, vuln
make image && make test-container
make fuzz FUZZTIME=30s && make bench
grep -x 'go 1.25.0' go.mod; git diff main -- go.mod | grep '^[+-]' # the requirements of §7 and nothing else
git grep -n -e 'go.opentelemetry.io/otel/sdk' -e otlptrace -e go-logr -- cmd internal   # internal/telemetry only
git grep -n -e InsecureSkipVerify -e ProxyFromEnvironment -- cmd internal               # prints nothing
tail -1 internal/store/migrations/migrations.sum                                        # 0006_usage_ledger.sql
git worktree add -q ../pandokos-main main && (cd ../pandokos-main && make build && ls -l bin/pandokos); git worktree remove ../pandokos-main
make build && ls -l bin/pandokos                                    # binary size before and after
go test -race -count=1 -run 'TestLedgerExitScenario|TestLedgerDatabaseDown|TestLedgerAtShutdown|TestTraces|TestNoContentAnywhere' ./test/e2e/ -v
go test -race -count=20 -run 'TestOneRecordPerOutcome|TestCancellation|TestConcurrency' ./internal/gateway/ ./internal/ledger/
```

Then check the definition of done of §10 line by line, and fill the coverage table of §9 with
the test names that ran.

## What the implementer does not do

- No route, CLI command or console page that reads usage; no `usage:read` permission.
- No retention, no function that drops or detaches partitions, no `usage_correction`.
- No metric, no `gen_ai.*` attribute, no token count on a span, no gRPC exporter, no global
  tracer provider.
- No tokenizer, no parsing of prompt or completion text, no change to the chat reader's
  stripping behaviour, no switch for the usage option.
- No grant beyond appendix A; no second policy; no foreign key from `usage_record` other than
  the tenant's.
- No retry of a provider call, no buffering of a stream, no timer between read and write.
- No new error code; no change to the 3.1 pipeline order other than steps 3a and 3b.

---

## Appendix A — `internal/store/migrations/sql/0006_usage_ledger.sql`

```sql
-- 0006_usage_ledger: the usage ledger (one row per authenticated inference request, no content),
-- partitioned by month on the request start, append-only, and the function that creates its
-- partitions. Runs in one transaction as pandokos_owner. Every name is schema-qualified.

-- Usage ledger (tenant-scoped table, append-only, partitioned). The gateway generates id and
-- started_at; a batch that is sent twice inserts each row once (the primary key).
CREATE TABLE pandokos.usage_record (
    tenant_id               uuid        NOT NULL,
    id                      uuid        NOT NULL,
    started_at              timestamptz NOT NULL,
    recorded_at             timestamptz NOT NULL DEFAULT now(),
    request_id              text        NOT NULL,
    trace_id                text        NOT NULL DEFAULT '',
    principal_kind          text        NOT NULL,
    principal_id            text        NOT NULL,
    key_id                  text        NOT NULL DEFAULT '',
    credential_id           uuid,
    funding                 text        NOT NULL,
    provider                text        NOT NULL,
    api_family              text        NOT NULL,
    operation               text        NOT NULL,
    endpoint                text        NOT NULL DEFAULT '',
    model                   text        NOT NULL DEFAULT '',
    model_id                uuid,
    upstream_model          text        NOT NULL DEFAULT '',
    stream                  boolean,
    outcome                 text        NOT NULL,
    error_code              text        NOT NULL DEFAULT '',
    upstream_status         integer,
    ttfb_ms                 bigint,
    duration_ms             bigint      NOT NULL,
    request_bytes           bigint,
    response_bytes          bigint,
    input_source            text        NOT NULL,
    output_source           text        NOT NULL,
    input_tokens            bigint,
    output_tokens           bigint,
    cache_read_tokens       bigint,
    cache_write_tokens      bigint,
    reasoning_tokens        bigint,
    other_units             jsonb       NOT NULL DEFAULT '{}'::jsonb,
    provider_usage          jsonb       NOT NULL DEFAULT '{}'::jsonb,
    estimated_input_tokens  bigint,
    estimated_output_tokens bigint,
    cost_status             text        NOT NULL,
    cost_reason             text        NOT NULL DEFAULT '',
    cost                    numeric(38,18),
    currency                text,
    price_version_id        uuid,
    cost_fallbacks          text[]      NOT NULL DEFAULT '{}'::text[],
    metadata                jsonb       NOT NULL DEFAULT '{}'::jsonb,
    metadata_dropped        integer     NOT NULL DEFAULT 0,
    CONSTRAINT usage_record_pkey PRIMARY KEY (tenant_id, started_at, id),
    CONSTRAINT usage_record_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT usage_record_request_id_format CHECK (request_id ~ '^[0-9a-f]{32}$'),
    CONSTRAINT usage_record_trace_id_format CHECK (trace_id ~ '^([0-9a-f]{32})?$'),
    CONSTRAINT usage_record_principal_kind_valid CHECK (principal_kind IN ('service_account')),
    CONSTRAINT usage_record_principal_id_format CHECK (principal_id ~ '^[0-9a-f-]{36}$'),
    CONSTRAINT usage_record_key_id_format CHECK (key_id ~ '^[A-Za-z0-9_-]{0,64}$'),
    CONSTRAINT usage_record_funding_valid CHECK (funding IN ('byok')),
    CONSTRAINT usage_record_provider_format CHECK (provider ~ '^[a-z0-9-]{1,63}$'),
    CONSTRAINT usage_record_api_family_format CHECK (api_family ~ '^[a-z0-9-]{1,63}$'),
    CONSTRAINT usage_record_operation_format CHECK (operation ~ '^[a-z0-9._-]{1,63}$'),
    CONSTRAINT usage_record_endpoint_length CHECK (char_length(endpoint) <= 63),
    CONSTRAINT usage_record_model_length CHECK (char_length(model) <= 200 AND char_length(upstream_model) <= 200),
    CONSTRAINT usage_record_outcome_valid CHECK (outcome IN ('ok', 'upstream_error', 'cancelled', 'timeout',
                                                             'rejected_limit', 'rejected_policy', 'internal_error')),
    CONSTRAINT usage_record_error_code_format CHECK (error_code ~ '^[a-z_]{0,40}$'),
    CONSTRAINT usage_record_upstream_status_range CHECK (upstream_status BETWEEN 100 AND 599),
    CONSTRAINT usage_record_times_not_negative CHECK (duration_ms >= 0 AND COALESCE(ttfb_ms, 0) >= 0
                                                      AND COALESCE(request_bytes, 0) >= 0 AND COALESCE(response_bytes, 0) >= 0),
    CONSTRAINT usage_record_source_valid CHECK (input_source IN ('provider', 'estimated', 'none')
                                                AND output_source IN ('provider', 'estimated', 'none')),
    -- Values reported by the provider and values estimated by Pandokos never share a column (USE-04).
    CONSTRAINT usage_record_input_by_source CHECK (
        (input_source = 'provider') = (input_tokens IS NOT NULL)
        AND (input_source = 'estimated') = (estimated_input_tokens IS NOT NULL)),
    CONSTRAINT usage_record_output_by_source CHECK (
        (output_source = 'provider') = (output_tokens IS NOT NULL)
        AND (output_source = 'estimated') = (estimated_output_tokens IS NOT NULL)),
    CONSTRAINT usage_record_detail_needs_provider CHECK (
        (cache_read_tokens IS NULL AND cache_write_tokens IS NULL) OR input_source = 'provider'),
    CONSTRAINT usage_record_reasoning_needs_provider CHECK (reasoning_tokens IS NULL OR output_source = 'provider'),
    CONSTRAINT usage_record_tokens_not_negative CHECK (
        COALESCE(input_tokens, 0) >= 0 AND COALESCE(output_tokens, 0) >= 0 AND COALESCE(cache_read_tokens, 0) >= 0
        AND COALESCE(cache_write_tokens, 0) >= 0 AND COALESCE(reasoning_tokens, 0) >= 0
        AND COALESCE(estimated_input_tokens, 0) >= 0 AND COALESCE(estimated_output_tokens, 0) >= 0),
    CONSTRAINT usage_record_other_units_valid CHECK (
        jsonb_typeof(other_units) = 'object' AND pg_column_size(other_units) <= 2048
        AND NOT jsonb_path_exists(other_units, '$.* ? (@.type() != "number")')),
    CONSTRAINT usage_record_provider_usage_valid CHECK (
        jsonb_typeof(provider_usage) = 'object' AND pg_column_size(provider_usage) <= 4096
        AND NOT jsonb_path_exists(provider_usage, '$.* ? (@.type() != "number")')),
    CONSTRAINT usage_record_cost_status_valid CHECK (cost_status IN ('known', 'unknown', 'none')),
    CONSTRAINT usage_record_cost_reason_valid CHECK (
        (cost_status = 'unknown') = (cost_reason IN ('no_price', 'category_unpriced', 'inconsistent_usage'))
        AND (cost_status <> 'unknown') = (cost_reason = '')),
    CONSTRAINT usage_record_cost_by_status CHECK (
        (cost_status = 'known') = (cost IS NOT NULL)
        AND (cost IS NULL OR (cost >= 0 AND currency IS NOT NULL AND price_version_id IS NOT NULL))),
    CONSTRAINT usage_record_currency_format CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT usage_record_cost_fallbacks_bound CHECK (cardinality(cost_fallbacks) <= 8),
    CONSTRAINT usage_record_metadata_valid CHECK (
        jsonb_typeof(metadata) = 'object' AND pg_column_size(metadata) <= 4096
        AND NOT jsonb_path_exists(metadata, '$.* ? (@.type() != "string")')),
    CONSTRAINT usage_record_metadata_dropped_range CHECK (metadata_dropped BETWEEN 0 AND 1000)
) PARTITION BY RANGE (started_at);

CREATE INDEX usage_record_request_idx ON pandokos.usage_record (tenant_id, request_id);

CREATE TRIGGER usage_record_append_only BEFORE UPDATE OR DELETE ON pandokos.usage_record
    FOR EACH ROW EXECUTE FUNCTION pandokos.append_only();
CREATE TRIGGER usage_record_no_truncate BEFORE TRUNCATE ON pandokos.usage_record
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only();

ALTER TABLE pandokos.usage_record ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.usage_record FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.usage_record
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

-- The gateway appends and reads nothing. The admin and worker roles read; nobody changes a row.
GRANT INSERT (tenant_id, id, started_at, request_id, trace_id, principal_kind, principal_id, key_id, credential_id,
              funding, provider, api_family, operation, endpoint, model, model_id, upstream_model, stream, outcome,
              error_code, upstream_status, ttfb_ms, duration_ms, request_bytes, response_bytes, input_source,
              output_source, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
              other_units, provider_usage, estimated_input_tokens, estimated_output_tokens, cost_status, cost_reason,
              cost, currency, price_version_id, cost_fallbacks, metadata, metadata_dropped)
    ON pandokos.usage_record TO pandokos_gateway;
GRANT SELECT ON pandokos.usage_record TO pandokos_admin, pandokos_worker;

-- Gives a partition the protection of its parent for anyone who names it directly: forced
-- row-level security with the canonical policy, and no TRUNCATE. No runtime role holds a
-- privilege on a partition; rows are reached through pandokos.usage_record only.
CREATE FUNCTION pandokos.protect_usage_partition(p_name text) RETURNS void
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    EXECUTE pg_catalog.format('ALTER TABLE pandokos.%I ENABLE ROW LEVEL SECURITY', p_name);
    EXECUTE pg_catalog.format('ALTER TABLE pandokos.%I FORCE ROW LEVEL SECURITY', p_name);
    EXECUTE pg_catalog.format(
        'CREATE POLICY tenant_isolation ON pandokos.%I USING (tenant_id = (SELECT pandokos.current_tenant_id())) '
        'WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()))', p_name);
    EXECUTE pg_catalog.format(
        'CREATE TRIGGER %I BEFORE TRUNCATE ON pandokos.%I FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only()',
        p_name || '_no_truncate', p_name);
    EXECUTE pg_catalog.format('REVOKE ALL ON pandokos.%I FROM PUBLIC', p_name);
END
$$;

-- Rows whose month has no partition land here instead of being refused. It is expected to stay
-- empty: ensure_usage_partitions reports when it is not.
CREATE TABLE pandokos.usage_record_default PARTITION OF pandokos.usage_record DEFAULT;
SELECT pandokos.protect_usage_partition('usage_record_default');

-- Creates the monthly partitions (UTC) from the month before the current one to p_ahead months
-- after it, and reports how many it created and whether the default partition has ever held a
-- row. A month whose rows are already in the default partition is skipped. Safe to call
-- in parallel and repeatedly. It runs with the rights of the owner, because runtime roles own
-- nothing and cannot create tables; the worker role is its only caller.
CREATE FUNCTION pandokos.ensure_usage_partitions(p_ahead integer, OUT created integer, OUT default_in_use boolean)
    LANGUAGE plpgsql
    SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_month date;
    v_name  text;
BEGIN
    IF p_ahead IS NULL OR p_ahead < 1 OR p_ahead > 12 THEN
        RAISE EXCEPTION 'p_ahead must be between 1 and 12' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM pg_catalog.set_config('lock_timeout', '5s', true);
    PERFORM pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('pandokos.usage_record.partitions', 0));
    created := 0;
    FOR i IN -1 .. p_ahead LOOP
        v_month := (pg_catalog.date_trunc('month', pg_catalog.timezone('UTC', pg_catalog.now()))
                    + pg_catalog.make_interval(months => i))::date;
        v_name := 'usage_record_' || pg_catalog.to_char(v_month, '"y"YYYY"m"MM');
        IF pg_catalog.to_regclass('pandokos.' || pg_catalog.quote_ident(v_name)) IS NULL THEN
            BEGIN
                EXECUTE pg_catalog.format(
                    'CREATE TABLE pandokos.%I PARTITION OF pandokos.usage_record FOR VALUES FROM (%L) TO (%L)',
                    v_name,
                    pg_catalog.to_char(v_month, 'YYYY-MM-DD') || ' 00:00:00+00',
                    pg_catalog.to_char((v_month + pg_catalog.make_interval(months => 1))::date, 'YYYY-MM-DD')
                        || ' 00:00:00+00');
                PERFORM pandokos.protect_usage_partition(v_name);
                created := created + 1;
            EXCEPTION WHEN check_violation THEN
                -- The default partition already holds rows of that month: they stay there, and
                -- the month keeps going to the default partition.
                NULL;
            END;
        END IF;
    END LOOP;
    default_in_use := pg_catalog.pg_relation_size('pandokos.usage_record_default'::pg_catalog.regclass) > 0;
END
$$;

GRANT EXECUTE ON FUNCTION pandokos.ensure_usage_partitions(integer) TO pandokos_worker;

-- The partitions of a new installation: last month to three months ahead.
SELECT pandokos.ensure_usage_partitions(3);
```

## Appendix B — `internal/store/queries/sql/usage.sql`

```sql
-- Usage ledger. Insert only for the gateway: no statement reads, changes or removes a usage row
-- in the request path. records is a JSON array passed as text and cast (ADR 0027).

-- name: InsertUsageRecords :execrows
INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, trace_id, principal_kind, principal_id, key_id,
                                   credential_id, funding, provider, api_family, operation, endpoint, model, model_id,
                                   upstream_model, stream, outcome, error_code, upstream_status, ttfb_ms, duration_ms,
                                   request_bytes, response_bytes, input_source, output_source, input_tokens,
                                   output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, other_units,
                                   provider_usage, estimated_input_tokens, estimated_output_tokens, cost_status,
                                   cost_reason, cost, currency, price_version_id, cost_fallbacks, metadata,
                                   metadata_dropped)
SELECT pandokos.current_tenant_id(), r.id, r.started_at, r.request_id, r.trace_id, r.principal_kind, r.principal_id,
       r.key_id, r.credential_id, r.funding, r.provider, r.api_family, r.operation, r.endpoint, r.model, r.model_id,
       r.upstream_model, r.stream, r.outcome, r.error_code, r.upstream_status, r.ttfb_ms, r.duration_ms,
       r.request_bytes, r.response_bytes, r.input_source, r.output_source, r.input_tokens, r.output_tokens,
       r.cache_read_tokens, r.cache_write_tokens, r.reasoning_tokens, r.other_units, r.provider_usage,
       r.estimated_input_tokens, r.estimated_output_tokens, r.cost_status, r.cost_reason, r.cost, r.currency,
       r.price_version_id, COALESCE(r.cost_fallbacks, '{}'::text[]), r.metadata, r.metadata_dropped
  FROM jsonb_to_recordset(sqlc.arg(records)::text::jsonb) AS r(
       id uuid, started_at timestamptz, request_id text, trace_id text, principal_kind text, principal_id text,
       key_id text, credential_id uuid, funding text, provider text, api_family text, operation text, endpoint text,
       model text, model_id uuid, upstream_model text, stream boolean, outcome text, error_code text,
       upstream_status integer, ttfb_ms bigint, duration_ms bigint, request_bytes bigint, response_bytes bigint,
       input_source text, output_source text, input_tokens bigint, output_tokens bigint, cache_read_tokens bigint,
       cache_write_tokens bigint, reasoning_tokens bigint, other_units jsonb, provider_usage jsonb,
       estimated_input_tokens bigint, estimated_output_tokens bigint, cost_status text, cost_reason text,
       cost numeric, currency text, price_version_id uuid, cost_fallbacks text[], metadata jsonb,
       metadata_dropped integer)
ON CONFLICT DO NOTHING;

-- name: EnsureUsagePartitions :one
SELECT created::integer AS created, default_in_use::boolean AS default_in_use
  FROM pandokos.ensure_usage_partitions(sqlc.arg(ahead)::integer);
```
