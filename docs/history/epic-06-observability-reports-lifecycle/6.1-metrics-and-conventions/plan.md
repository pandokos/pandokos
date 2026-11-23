# Sprint 6.1 — Implementation plan

- Design: [sprint.md](sprint.md). Section numbers (§) refer to it; M-numbers are the instruments of
  §5.3, T-numbers the tests of §9.
- Each part below is self-contained: an agent needs sprint.md and that part only. Parts run in order
  A, B, C1 … C8, D, E, F.
- Suggested grouping: **G1** = C1, C2, C3 (`internal/telemetry`: runtime, instruments, exposition);
  **G2** = C4 (the redacting handler); **G3** = C5, C6 (the gateway observation and the component
  snapshots); **G4** = C7 (the route, the permission, the wiring). After part B, G1, G2 and G3 do not
  depend on each other; G4 needs G1 and G3. C8 is one agent after them. A, B, D, E and F are one agent
  each. Because the risk class is medium-high, C5 (what leaves the gateway) and C7 (the route and
  its authorization) are reviewed by an agent that did not write them, with §5.2, §5.5 and §5.7 as
  the checklist.

## Rules for the implementer

1. Work on the branch the orchestrator names; do not create one. Sign off every commit
   (`git commit -s`) in the maintainer's name and keep the signature. No co-author or
   attribution lines, no tool footers and no session links in any commit message, pull request
   text, comment or file. One commit for A, one for B (tests, failing), one per step of C (message given with the
   step), one for D, one for E, one for F if something is found.
2. **Two new modules only**: `go.opentelemetry.io/otel/sdk/metric` and
   `go.opentelemetry.io/otel/exporters/otlp/otlpmetrichttp`, both **v1.47.0**;
   `go.opentelemetry.io/otel/metric` becomes direct at v1.47.0. No Prometheus module, no other module.
   No new error code, no migration, no `SECURITY DEFINER` function, no tenant-scoped permission.
   The only new permission is `metrics:read` (platform scope); the only new keys are the three of
   §5.12; the only new query is `CountOpenSessions` (appendix A6).
3. **Nothing of a tenant reaches a metric, a log line or a span.** `RequestObservation` and
   `telemetry.Request` have exactly the fields of §5.2.1; no code copies a principal, a request ID, a
   trace ID, metadata, a rule ID, a subject ID or a limit value into them. The one identifier is
   `CredentialID`, filled only when the upstream status is 429. No `Stats()` snapshot holds an
   identifier, a URL, a host or a name of a tenant's object.
4. **The gateway never waits for telemetry.** `Observe` is called once, after the request line, under
   a recover; the implementation does in-memory aggregation only. No lock of the gateway is held
   while recording.
5. Attribute values come from the closed sets of §5.3.1 or from the catalogue names of a resolved
   target. Where §5.3.1 says "the closed list of …", make it a list of constants in the owning package
   in part A and have the code use only those constants.
6. Components import neither `internal/telemetry` nor `go.opentelemetry.io/otel/metric`;
   `internal/telemetry` imports no product package but `internal/secret`. Adapters live in
   `cmd/pandokos/metrics.go` (T-R64).
7. Every log line added or renamed is a row of `docs/reference/logs.md` in the same commit (the
   reference is written in part B as a skeleton of the existing lines, and grows with each step;
   T-R66 and T-930 fail otherwise).
8. Tests hold for root and non-root; nothing sleeps longer than 2 s; waiting is
   `testutil.Eventually`; time-dependent logic takes a clock; a bound is never made to pass by
   lengthening it and a database test never by skipping it. Latency tests use medians and the bounds
   of §9 exactly.
9. No secret in code, tests, fixtures or logs: tests generate keys and tokens with the product's
   generators or the fake identity provider; canaries come from `test/fixtures`.
10. Generated files are never edited by hand (`make generate`).
11. Never write a version, tag or digest you did not obtain from the command given with it.
12. A gap in sprint.md or in this plan is a question to the orchestrator, not a decision.

## Environment

```sh
set -a; . local/testdb/env; set +a     # the test database runs in Docker; do not run make db-up or db-down
make generate                           # apigen, permdoc and sqlc
go test -race -count=1 ./...            # everything but the SDK and Compose suites
make check                              # what a pull request must pass
```

If the test database cannot be reached, stop and report; do not skip database tests. Compose and
container tests run in CI only (job `image`).

---

## Part A — Scaffolding (commit: `chore: sprint 6.1 scaffolding: metric modules, configuration keys, metrics:read, ports and snapshot types`)

### A1. Modules

```sh
go get go.opentelemetry.io/otel/sdk/metric@v1.47.0 \
       go.opentelemetry.io/otel/exporters/otlp/otlpmetrichttp@v1.47.0 \
       go.opentelemetry.io/otel/metric@v1.47.0
go mod tidy
go list -m go.opentelemetry.io/otel/sdk/metric go.opentelemetry.io/otel/exporters/otlp/otlpmetrichttp
git diff go.mod    # require block: exactly the three lines above added or moved to direct
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

The design ran the same `go get` and `go mod tidy` on a copy: no other module entered. If
`govulncheck` reports a vulnerability reachable through the new modules, stop and report.

### A2. Configuration keys — `internal/config/{config,keys,telemetry}.go`

- `Telemetry` gains `Metrics TelemetryMetrics` with `OTLP bool` (`yaml:"otlp"`, default `true`),
  `ExportInterval Duration` (`export_interval`, default `60s`), `Prometheus bool` (`prometheus`,
  default `false`).
- `keys.go`: three `serveDef` entries, with the descriptions of §5.12.
- `telemetry.go`: `export_interval` between 5 s and 10 min and not below `telemetry.otlp.timeout`
  ("must be at least telemetry.otlp.timeout"); warnings: the interval set while no OTLP export runs
  ("has no effect without telemetry.otlp.endpoint and telemetry.metrics.otlp"); the environment
  variables of §5.1 (`OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` set while `telemetry.otlp.endpoint` is
  empty; `OTEL_METRIC_EXPORT_INTERVAL`, `OTEL_METRIC_EXPORT_TIMEOUT` set), each naming the variable,
  never its value, attached to the key they concern, in the style of the existing
  `OTEL_EXPORTER_OTLP_ENDPOINT` warning.

### A3. Permission — `internal/authz/{authz,roles}.go`, `internal/authz/testdata/roles.golden`, `internal/permdoc`

- `MetricsRead = "metrics:read"`, platform scope, no attribute, description "Reading the
  installation's metrics on the scrape endpoint", in the catalogue next to `PlatformKeyRead`.
- Added to `RolePlatformAdmin` and `RolePlatformAuditor`; the golden file says so.
- `internal/permdoc`: a permission with no operation of the OpenAPI document may name routes
  outside it; give `metrics:read` the route `GET /metrics (administration listener)` from a small
  table in permdoc (`outsideAPI = map[string][]string{authz.MetricsRead: {"GET /metrics (administration listener)"}}`),
  printed in the operations column. `make generate` regenerates `docs/reference/permissions.md`.
- Platform keys accept the new scope with no change (scopes are permission names); verify with
  `pandokos platform-key create --scope metrics:read` in part C7's test.

### A4. Closed lists

- `internal/authn/reasons.go`: one constant per failure reason now written as a literal in
  `authn.go`, `token.go`, `tokens.go`, `jwt.go` (find them with
  `grep -n 'Reason: *"' internal/authn/*.go` and the `fail("…")` calls of `token.go`), and
  `var Reasons = []string{…}` in a fixed order; the literals are replaced by the constants (no change
  of value).
- `internal/account`: `const` results of a sign-in (`ok`, `failed`, `locked`, `disabled`,
  `unavailable`, `refused`) and `var SignInResults`. Map the service's existing errors to them in
  C6; do not change what the API answers.
- `internal/egress`: `var BlockReasons = []string{"not_tcp", "unparsable", "forbidden_address", "private_address", "scheme", "user_info"}`
  (the values of `BlockedError.Reason` in `guard.go`); a test in C6 checks no other value is used.
- `internal/jwks/fetch.go`: `var FetchClasses` from the eight class constants.

### A5. Types and ports (no behaviour)

- `internal/gateway/ports.go`: the interface `Metrics` and the types `RequestObservation`,
  `LimitRefusalObservation`, `EstimateObservation` exactly as §5.2.1; `Options.Metrics Metrics`
  (optional).
- `internal/telemetry/request.go`: `type Request struct` with the same fields and nested types
  (`RefusalObservation`, `EstimateObservation` in package telemetry).
- `internal/telemetry/metrics.go`:

  ```go
  // MetricsOptions configure the meter provider.
  type MetricsOptions struct {
      Resource       *Resource         // NewResource; shared with TracesOptions.Resource
      Endpoint       *url.URL          // nil: no OTLP export
      OTLP           bool              // telemetry.metrics.otlp
      Headers        map[string]string // values are secrets
      Timeout        time.Duration
      Interval       time.Duration
      Prometheus     bool              // telemetry.metrics.prometheus
      ServiceVersion string
      Transport      http.RoundTripper // required with Endpoint and OTLP
      Logger         *slog.Logger      // required
      Sources        Sources
      Now            func() time.Time  // optional clock for staleness and lag (default time.Now)
  }

  // Sources are read at each collection; a nil function leaves its instruments out.
  type Sources struct {
      Ledger      func() LedgerStats
      LedgerLimit int
      Egress      func() EgressStats
      Authn       map[string]func() AuthnStats // by listener: "inference", "admin"
      JWKS        func() JWKSStats
      SignIn      func() SignInStats
      Sessions    func() (open int64, ok bool)
      Hashes      func() HashStats
      Idempotency func() int64
      Admin       func() AdminStats      // grants refused, support actions opened and ended by cause
      Limits      func() LimitsStats     // memory, reconciler, events, rule cache
      Resolver    func() ResolverStats   // admitted unpriced, unlimited traffic by result
  }

  func NewMetrics(o MetricsOptions) (*Metrics, error)
  func (m *Metrics) ObserveRequest(r Request)
  func (m *Metrics) Handler() http.Handler          // nil unless Prometheus
  func (m *Metrics) Shutdown(ctx context.Context) error
  func Catalogue() []InstrumentSpec                  // the table of §5.3, for tests and the reference
  ```

  `InstrumentSpec{Name, Kind, Unit, Description, Attributes []string, PrometheusName string}`.
  The snapshot structs hold counts keyed by the closed values (`map[string]int64` keyed by a value
  of §5.3.1, or fixed fields), never an identifier.
- `internal/telemetry/traces.go`: `NewResource(attrs map[string]string, version string) (*Resource, error)`
  extracted from `newResource`; `TracesOptions.Resource` (required); `serve` builds one resource and
  passes it to both.

### A6. Query — `internal/store/queries/sql/sessions.sql`

Append, then `make generate`:

```sql
-- name: CountOpenSessions :one
-- Platform scope. The sessions PurgeSessions would keep; counted by the purge loop (sprint 6.1, D-19).
SELECT count(*)::bigint AS sessions
  FROM pandokos.user_session
 WHERE expires_at > now() AND last_used_at > now() - sqlc.arg(idle)::interval;
```

`session.Store` gains `CountOpen(ctx) (int64, error)` running it in the same transaction helper as
`Purge` (platform scope), with the store's idle timeout.

### A7. Test collector — `test/otlptest/otlptest.go`

Route by path: `…/v1/traces` as today; `…/v1/metrics` decodes
`collectormetricspb.ExportMetricsServiceRequest`
(`go.opentelemetry.io/proto/otlp/collector/metrics/v1`) into a kept slice; `Metrics()` returns the
`ResourceMetrics` received; `Requests()` keeps both. `Stall`, `Fail` and `Close` act on both.

### A8. `pricing.Decimal.Float64()`

Through `new(big.Rat).SetFrac(d.num(), 10^scale).Float64()`; a unit test with three values
(0, a fraction with 18 digits, a large integer).

Verification: `go build ./... && go vet ./... && make generate-check && go test -count=1 ./internal/config/ ./internal/authz/ ./internal/pricing/ ./test/repo/`
(T-846/T-847 pass once the golden file has the permission; `TestConfigurationReference` may fail
until part E: list it in the commit message).

---

## Part B — Tests first (commit: `test: sprint 6.1 tests, failing`)

Every test of §9 is written under its stated name, compiles, and fails for the reason it tests. The
changed tests of §9 are changed here. The pull request quotes the failing output of this commit.

### B1. Helpers and fixtures

- `internal/telemetry/helpers_test.go`: `newTestMetrics(t, opts)` with a `ManualReader` injected
  through an unexported option (`withReader`) so that tests collect `metricdata.ResourceMetrics`
  directly; helpers `points(rm, name)` returning attribute maps and values, `histogram(rm, name)`.
- `telemetry_test` (external package) for T-907, which needs `authn.NewKey`, `session.NewToken`
  and `test/fakeidp` (an internal test would cycle: `session` → `account` → `audit` → `telemetry`).
- `test/e2e`: `opts` gains `metricsPrometheus bool`, `exportInterval string`; `proc` keeps its
  standard output (it does already for the log assertions) and its fixtures' IDs; `p.stop` calls
  `checkLogReference(t, p)` (T-930). `checkLogReference` parses `docs/reference/logs.md`: each
  message is a level-3 heading `` ### `message` `` followed by a line `Level: …` and a line
  `Fields: `a`, `b`, …`` (the format E1 writes); the glossary is the table under `## Fields`.
- `docs/reference/logs.md` **skeleton**, committed here: every message of the inventory below with
  its fields as the code writes them today, so that T-930 and T-R66 fail only for the lines this
  sprint adds or renames. Build the inventory with
  `grep -rnE '\.(Debug|Info|Warn|Error)(Context)?\("' --include=*.go cmd internal | grep -v _test`
  and read each call's fields; part E completes the descriptions.
- `test/fakeprovider`: the provider 429 is `ModelError429` (exists); `ModelStallAfterFirst`,
  `ModelHang`, `ModelCut` and `ModelStreamError` give the timeout, abort and error paths of T-910.
  Add `ModelTimed` (`fake-timed`): headers at once, the first body byte after 200 ms, the end after
  400 ms, stream and non-stream, for T-912.
- Canary IDs for T-911, T-925: tenant, service account and key names from
  `fixtures.CanaryName(…)` style helpers (add `fixtures.CanaryLabel(prefix)` returning
  `prefix + "-canary-" + 8 random hex`), and the generated IDs collected from the API answers.

### B2. Where each test lives

| Tests | File |
|---|---|
| T-897 to T-906, T-909, T-913, T-923 | `internal/telemetry/{catalogue,genai,buckets,attributes,cardinality,prometheus,export,collector,noop,errors,request,selfobs}_test.go` |
| T-907, T-908 | `internal/telemetry/redact_test.go` (package `telemetry_test`) |
| T-910 to T-912, T-914, T-915 | `internal/gateway/{observation,observation_fields,observation_times,refusal_observation,metrics_alloc}_test.go` (T-915's benchmark in the last) |
| T-916 | `internal/ledger/writer_test.go`, `internal/telemetry/sources_test.go` |
| T-917 | `internal/egress/stats_test.go` |
| T-918 | `internal/authn/stats_db_test.go` |
| T-919 | `internal/jwks/stats_test.go` |
| T-920 | `internal/account/stats_db_test.go`, `internal/password/stats_test.go`, `internal/idem/stats_db_test.go`, `cmd/pandokos/sessions_db_test.go` |
| T-921 | `internal/admin/stats_db_test.go` |
| T-922 | `internal/limits/stats_test.go`, `internal/resolver/stats_test.go` |
| T-924, T-925, T-928, T-929 | `test/e2e/metrics_test.go` |
| T-926 | `internal/authz/authz_test.go` |
| T-927 | `internal/config/telemetry_test.go` |
| T-930 | `test/e2e/logref_test.go` (the check) and every e2e test through `p.stop` |
| T-931, T-932 | `test/compose/observability_test.go` (tag `compose`) |
| T-R64 to T-R70 | `test/repo/telemetry_test.go` |

Notes for some tests:

- **T-905**: the 1 ms per call is the p99 of 10,000 calls measured with `time.Now` around each
  call; the comparison run uses a healthy collector in the same test. Use an export interval of 1 s
  and an export timeout of 1 s so that several exports stall during the run.
- **T-915**: the harness of `internal/gateway` (`newHarness`) with `o.Metrics` set to an adapter over
  a real `telemetry.Metrics` with a manual reader (the adapter type is test-local: copying fields, as
  `cmd/pandokos/metrics.go` will), against the same harness with a no-op `Metrics`; 1,000 warm-up
  requests then `testing.AllocsPerRun(200, …)`. The bound is 8.
- **T-923**: dropped spans are provoked with the collector stalled and more spans than the queue of
  2048; after `Shutdown`, ended − exported − failed is compared with the number the test emitted
  minus those the collector received.
- **T-929**: copy the shape of T-511 (`test/e2e/traces_test.go`), with metrics on and the scrape
  endpoint on; scrape once per run during the calls with a platform key scoped to `metrics:read`.
- **T-931/T-932**: see part D3.

Verification: `go vet ./... && go vet -tags compose ./test/compose/`;
`go test -count=1 ./... 2>&1 | tee /tmp/b.log` fails in the new and changed tests only (list them in
the commit message).

---

## Part C — Implementation

### C1. The telemetry runtime — `feat: metrics over OTLP/HTTP with the traces' resource and collector connection`

Files: `internal/telemetry/{metrics,traces,selfobs}.go`.

1. `NewMetrics`: validate (logger required; transport required when `Endpoint != nil && OTLP`;
   interval > 0). With neither OTLP nor Prometheus, return a `Metrics` holding
   `noop.NewMeterProvider()` and nil handler (T-906).
2. Otherwise build `sdkmetric.NewMeterProvider` with: `WithResource` (the shared resource), the
   views of C2, `WithCardinalityLimit(2000)`, `WithExemplarFilter(exemplar.AlwaysOffFilter)`, and
   the readers:
   - OTLP: `otlpmetrichttp.New(ctx, WithEndpointURL(<endpoint path>/v1/metrics), WithHeaders,
     WithTimeout, WithCompression(NoCompression), WithRetry(RetryConfig{Enabled: false}),
     WithHTTPClient(&http.Client{Transport: o.Transport, CheckRedirect: lastResponse}))`, wrapped in
     `reportingMetricExporter` (its `Export` reports a failure to the metrics handler and returns
     nil), in `sdkmetric.NewPeriodicReader(exp, WithInterval(o.Interval), WithTimeout(o.Timeout))`.
     The path is built as in `NewTraces` (trim the trailing `/`, append, clear `RawPath`). An error
     from `otlpmetrichttp.New` is returned as "telemetry: the metric exporter could not be created",
     without its text.
   - Prometheus: `sdkmetric.NewManualReader()`; kept for `Handler()` (C3).
3. Handlers: generalise `errorHandler` with a message and a signal: the traces' reporting exporter
   uses `trace export failing`, the metrics' `metric export failing`, both increment M40 with their
   signal and class; the process-wide handler set by `otel.SetErrorHandler` logs `telemetry error`.
   The process-wide handler and logger are installed once by whichever of `NewTraces`/`NewMetrics`
   runs first (`sync.Once`), always routing to the logger given (T-909; T-503 changed).
4. Self-observation (D-14): `countingProcessor` (an `sdktrace.SpanProcessor` whose `OnEnd`
   increments `ended`; registered before the batcher in `NewTraces` when an endpoint is set) and the
   span reporting exporter counting `exported` (spans of a successful batch) and `failed`. The
   counters live in a package-level `selfStats` read by M39/M40; they are atomics.
5. `Shutdown`: the provider's `Shutdown(ctx)`; the no-op returns nil.

Verification: `go test -race -count=1 ./internal/telemetry/ -run 'TestMetricExport|TestCollectorAwayNeverBlocks|TestNoReaderNoProvider|TestSDKErrorLine|TestSelfObservation|TestDiagnosticsGoToTheLogger'`.

### C2. The instruments — `feat: the instrument catalogue with the GenAI conventions v1.41.0`

Files: `internal/telemetry/{instruments,request,sources}.go`.

1. `Catalogue()` returns the 41 specs of §5.3 in order, with the Prometheus names of rule R (C3
   computes them with the same function; T-903 compares with the reference).
2. The meter: `mp.Meter("github.com/pandokos/pandokos", metric.WithInstrumentationVersion(o.ServiceVersion), metric.WithSchemaURL(semconv.SchemaURL))`
   with `semconv "go.opentelemetry.io/otel/semconv/v1.41.0"`.
3. M1 with `httpconv.NewServerRequestDuration(meter)`, M3/M4/M5 with
   `genaiconv.NewClientOperationDuration`, `NewClientOperationTimeToFirstChunk`,
   `NewClientTokenUsage`; the other push instruments with the meter's `Int64Counter`,
   `Float64Counter`, `Float64Histogram` and the names, units and descriptions of the catalogue.
4. Views (`sdkmetric.NewView(Instrument{Name: …}, Stream{Aggregation: AggregationExplicitBucketHistogram{Boundaries: …}})`)
   for M1, M3, M4 (duration boundaries), M5 (token boundaries), M11 (ratio boundaries), §5.4.
5. `ObserveRequest(r Request)` (T-913):
   - M2 always; M1 for every request (the adapter does not call for health: health never reaches the
     gateway);
   - when `r.Forwarded`: M3 with `error.type` = `r.CallError` when not empty; M4 when
     `r.Stream && r.FirstChunk > 0`;
   - M5 per side with source `provider` and N > 0; M6 per side with source `estimated`;
   - M7 when `CostKnown` (add `CostAmount`, attribute `pandokos.currency`); M8 when a side has a
     source other than `none` and the cost is unknown;
   - M9 when `UpstreamStatus == 429` (attributes provider and `pandokos.credential.id`);
   - M10 when `Refusal != nil`; M11 when `Estimate != nil` and the reserved quantity > 0.
   - Durations in seconds as `float64` (`d.Seconds()`).
6. **Attribute sets are cached** (D-20): a `sync.Map` keyed by a small comparable struct (provider,
   operation, model, and the closed values each instrument needs) holding `attribute.Set` values;
   record with `RecordSet`/`metric.WithAttributeSet`. Values outside §5.3.1 are recorded as `_OTHER`
   (a defensive guard; T-900 checks none occurs in the table of observations).
7. Sources (`sources.go`): one `RegisterCallback` per non-nil source over its observable
   instruments; each callback calls the function once and observes every value; gauges M20 and M34
   compute their seconds from `o.Now`. `LedgerLimit` observed as a constant gauge. M41 and M39/M40
   read `selfStats` and the redaction counter of C4 (an exported `telemetry.Redactions() int64`
   read by the callback; no source function needed).

Verification: `go test -race -count=1 ./internal/telemetry/ -run 'TestInstrumentCatalogue|TestGenAIPin|TestBucketBoundaries|TestAttributesAreClosed|TestCardinality|TestRequestRecording|TestLedgerSource'`.

### C3. The scrape format — `feat: Prometheus text exposition of the metrics`

File: `internal/telemetry/prometheus.go`.

1. `promName(spec)` implements rule R (§5.7); `promLabel(key)` the character rule.
2. `Handler()` returns an `http.Handler` that: allows `GET` and `HEAD` (405 with `Allow` otherwise);
   takes a mutex; `reader.Collect(r.Context(), &rm)`; writes the exposition of §5.7 into a buffer,
   then the response with the content type; on a collect error answers 503 with no body and counts
   it as an export failure of signal `metrics`. Families come from `rm.ScopeMetrics[*].Metrics`; the
   kind of each family from the data type (`Sum` monotonic → counter, non-monotonic or `Gauge` →
   gauge, `Histogram` → histogram). `target_info` holds the resource attributes (rule R on keys).
3. Authentication is not here: it is the server's (C7). The handler is never mounted elsewhere.

Verification: `go test -race -count=1 ./internal/telemetry/ -run 'TestPrometheusExposition|TestPrometheusNames'`.

### C4. The redacting handler — `feat: the log handler redacts secret shapes in any value`

Files: `internal/telemetry/{log,redact}.go`.

1. `redactingHandler{next slog.Handler}` implementing `Enabled`, `Handle`, `WithAttrs`, `WithGroup`;
   `NewLogger` builds the JSON handler as today (its `ReplaceAttr` keeps the time and key rules) and
   wraps it.
2. `Handle`: rewrite `r.Message` and every attribute (recursively through groups; `Any` values
   resolved with `Value.Resolve()`; `error` and `fmt.Stringer` turned into their text before
   matching) with `redactValue(s) (string, bool)`: the prefilter of §5.8, then the four patterns
   (compiled once). Attributes given through `WithAttrs` are rewritten once, when given.
3. A rewritten value increments a package atomic (`Redactions()`), read by M41.
4. No other handler: check the call sites of `slog.New` in product code (the discard handlers stay).
   The boot logger of `serve` already uses `telemetry.NewLogger`.

Verification: `go test -race -count=1 ./internal/telemetry/ -run 'TestRedact|TestLoggerShape|TestRedaction'`.

### C5. The gateway observation — `feat: one metrics observation per inference request`

Files: `internal/gateway/{handler,ports,limits,record}.go`.

1. `reqState` gains `forwardAt time.Time` (set at the start of step 9, before the client span
   starts) and `callEnd time.Time` (set where the client span ends, `endClient`, or at finish when
   the call never produced a body).
2. `observation(st, outcome) RequestObservation` builds the struct from `st` and `st.rec` only:
   - `Method` from `st.method` (`OTHER` becomes `_OTHER`); `Scheme` from the request; `Route` from
     `routeLabelOf` when matched; `Status` `st.sw.status()` when a header was written.
   - `Provider`, `Family`, `Operation` (`st.op.GenAI`) when `st.matched`; `Model`
     (`st.target.Model`) when `st.resolved`.
   - `Forwarded` when `!st.forwardAt.IsZero()`; `Call` = `callEnd − forwardAt`; `FirstChunk` =
     `firstByte − forwardAt` when stream and set.
   - `CallError` per §5.3.1 A3: upstream status ≥ 400 → `strconv.Itoa`; outcome `timeout` or error
     code `upstream_timeout` → `timeout`; error code `upstream_unreachable` → `upstream_unreachable`;
     outcome `upstream_aborted` → `upstream_aborted`; outcomes `client_cancelled`,
     `shutdown_aborted`, `client_error` → `cancelled`; any other error after forwarding → `_OTHER`.
   - Tokens, sources and cost from `st.rec` (provider side from `rec.Usage`, estimated from
     `rec.Estimated`, as the record does); `CostAmount` via `Decimal.Float64()`; `CostReason` from
     `rec.Cost.Reason`.
   - `CredentialID` = `st.credentialID` only when `st.upstreamStatus == 429`.
   - `Refusal` from `st.refusal`: `Code`, `Metric`, `Level`, `SubjectKind` (the kind: verify that
     `LimitRefusal.Subject` is the subject kind and never an ID — `resolver/limits.go` fills it with
     `string(ref.Limit.Subject)`; if it can hold an ID, add `SubjectKind` to `LimitRefusal` and fill
     it from the rule's kind), `Scope`.
   - `Estimate` when a reservation was settled (`settleLimit` with `counted`): reserved tokens
     (`st.reservedTokens`), settled tokens and costs (the reserved cost from the estimate, kept on
     `st` in `reserve`), `CostComparable` when both costs are known.
3. `finish`: after `h.logRequest`, if `h.metrics != nil`, call `h.observe(st, outcome)` inside a
   recover that logs `metrics observation failed` (`request_id`, `panic_type`). The settle of the
   reservation stays deferred as today; `Estimate` is filled by making `settleLimit` return the
   settled figures it used, called before the observation (keep its never-panics property).
4. Nothing else changes in the request path; no field of `st` is added for metrics but
   `forwardAt`, `callEnd` and the reserved cost.

Verification: `go test -race -count=1 ./internal/gateway/ -run 'TestObservation|TestRefusalObservation|TestMetricsAllocations|TestRequestLineIsOperational'`
and `go test -run '^$' -bench BenchmarkRequestWithMetrics -benchmem ./internal/gateway/`.

### C6. The component snapshots — `feat: counters and snapshots of the components for metrics`

Each item: atomic counters in a fixed array or a map built at construction from the closed list,
incremented where the event happens, and a `Stats()` returning a copy. No log line changes.

| Package | Counted | Where |
|---|---|---|
| `ledger` | `Refused` | `Writer.Admit` returning false |
| `egress` | blocks by reason | where a `BlockedError` is returned (`guard.go`, the dial check and the URL check) |
| `authn` | attempts by kind (`key`, `token`, `unknown` for a malformed value), result (`ok`, `failed`, `unavailable`) and reason | `Authenticator.Authenticate`, one count per call, at its return |
| `jwks` | fetches by document (`keys`, `metadata`) and result; per source the time of its last success and whether it holds keys, to derive the states and the staleness at `Stats()` | `jwks.go` and `metadata.go` around the fetch; states: `fresh` (last success within `refresh_interval` × 2), `stale` (keys held, older), `missing` (never fetched or no usable key) |
| `account` | sign-ins by method `password` and result; locks | the local sign-in path and the lock path of `account.Service` |
| `admin` | OIDC sign-ins by result (`ok`, `refused`, `unavailable`) into the same snapshot type as `account` (method `oidc`); grants refused; support actions opened, closed, ended | `auth.go` (callback), `grant.go` (where `grant refused` is logged), the support handlers **after commit** |
| `password` | active (slots taken) and waiting (callers blocked in `acquire`) | `Hasher.acquire`/`release` (an atomic `waiting` around the blocking select) |
| `idem` | replays | where a stored response is returned |
| `limits` | `Memory`: expired (in `Sweep`); `Reconciler`: runs ok/failed per tenant reconciled, last `Tick` duration, last cut time; `EventWriter`: recorded, dropped (`Offer` false), failed (insert error) and `Close`'s unwritten added to dropped; `RuleCache`: loads ok/failed | `memory.go`, `reconcile.go`, `events.go`, `cache.go` |
| `resolver` | admitted unpriced (the `allow` branch of `on_unknown_price` with an unknown estimate cost); unlimited traffic written, skipped, failed | `limits.go`, `unlimited.go` |
| `cmd/pandokos` | sessions open: after each purge round, `sessions.CountOpen`, kept in an atomic with an "ok" flag (a failed count keeps the last value and is not logged beyond the existing `purge failed` pattern: log `session count failed` at warn with `sqlstate` when there is one, a new row of the log reference) | `people.go`, `runPurge` |

Verification: `go test -race -count=1 ./internal/ledger/ ./internal/egress/ ./internal/authn/ ./internal/jwks/ ./internal/account/ ./internal/password/ ./internal/idem/ ./internal/admin/ ./internal/limits/ ./internal/resolver/ ./cmd/pandokos/ -run 'Stats|SessionsOpen'`.

### C7. The scrape route and the wiring — `feat: GET /metrics on the administration listener for metrics:read`

Files: `internal/server/server.go`, `cmd/pandokos/{serve,metrics}.go`.

1. `server.Options` gains `Metrics gateway.Metrics` (passed to the gateway), `MetricsHandler
   http.Handler` (nil: no route) and `MetricsAuth gateway.Authenticator` (required when
   `MetricsHandler` is set).
2. `routeAdmin`: when `r.URL.Path == "/metrics"` and `MetricsHandler != nil`:
   - methods other than `GET`/`HEAD`: 405, `Allow: GET, HEAD`, problem body of the administration
     API (reuse its writer; if it is not reachable from `server`, a minimal `application/problem+json`
     with `code` as ADR 0020 defines);
   - credential: `Authorization: Bearer <v>` only; missing or not bearer → 401 `unauthenticated`
     with `WWW-Authenticate: Bearer`; `MetricsAuth.Authenticate`: `authz.ErrUnavailable` → 503
     `unavailable` with `Retry-After: 5`; other error → 401 (the failure is counted by the
     authenticator's stats and logged `authentication failed` with `listener: admin`, `request_id`,
     `reason`, as the API does);
   - `authz.Decide(p, authz.MetricsRead, authz.Target{})` not `Allow` → 403 `forbidden`;
   - then `MetricsHandler.ServeHTTP`; the line `admin request` at debug with `operation: metrics`,
     `status`, `duration_ms`, `request_id` (as health).
   When `MetricsHandler` is nil the path goes to the administration API (404).
3. `serve.go`: build the resource once (`telemetry.NewResource`); `TracesOptions.Resource`; build
   `telemetry.NewMetrics` after the components exist, with the options of the configuration and the
   sources of `metrics.go`; pass `gatewayMetrics{m}`, `m.Handler()` and `adminAuth` to `server.New`;
   at shutdown run `metrics.Shutdown(flushCtx)` in the same `flush` wait group as the traces (three
   goroutines).
4. `cmd/pandokos/metrics.go`: `gatewayMetrics` (field copy of the observation, no allocation) and
   one function per source converting the component snapshot to the telemetry type.

Verification: `go test -race -count=1 ./internal/server/ ./cmd/pandokos/` and
`go test -race -count=1 ./test/e2e/ -run 'TestScrapeEndpoint|TestMetricsHoldNoTenantData'`.

### C8. End to end — `test: metrics, the scrape endpoint and the log reference end to end`

Make T-924, T-925, T-928, T-929 and T-930 pass; run the whole e2e suite (`go test -race -count=1
./test/e2e/`), which now checks every process's log against the reference. A line that fails the
check is either a new line missing from the reference (add the row) or a field that ADR 0055 forbids
(fix the code, and report it in the commit message).

Verification: `go test -race -count=1 ./...`.

---

## Part D — Overlay, dashboards, Compose tests and CI (commit: `ci: sprint 6.1 observability overlay, dashboards and Compose exit tests`)

### D1. Images

Resolve, for each of `otel/opentelemetry-collector` (the core distribution), `prom/prometheus` (a
v3 release, for `--web.enable-otlp-receiver` and `otlp.translation_strategy`) and
`grafana/grafana`, the most recent release tag that is at least two weeks old and its index digest:

```sh
docker buildx imagetools inspect otel/opentelemetry-collector:<tag> --format '{{json .Manifest.Digest}}'
docker buildx imagetools inspect prom/prometheus:<tag>             --format '{{json .Manifest.Digest}}'
docker buildx imagetools inspect grafana/grafana:<tag>             --format '{{json .Manifest.Digest}}'
```

The tag's publication date comes from the project's release page (GitHub releases of
`open-telemetry/opentelemetry-collector-releases`, `prometheus/prometheus`, `grafana/grafana`).
Write `image: <name>:<tag>@sha256:<digest>`; record the three commands, their output and the date in
the pull request. Check that the collector image of the chosen tag has the `otlp` receiver, the
`batch` processor and the `otlphttp` and `debug` exporters (`otelcol components` in the image) and
that the Prometheus tag accepts `--web.enable-otlp-receiver`; if not, stop and report.

### D2. Files — `deploy/compose/observability/`

- `compose.yaml` (overlay): services `otel-collector`, `prometheus`, `grafana` as §5.11, each
  `read_only: true` where the image allows it (Prometheus and Grafana write to a named volume),
  `cap_drop: [ALL]`, `security_opt: [no-new-privileges:true]`, on a network `observability`
  (internal) plus the `egress` network for the collector only so that Pandokos reaches it; the
  `pandokos` service gets the three environment variables of §5.11. Published ports:
  `127.0.0.1:9090:9090`, `127.0.0.1:3000:3000`.
- `otel-collector.yaml`:

  ```yaml
  receivers:
    otlp:
      protocols:
        http: {endpoint: 0.0.0.0:4318}
  processors:
    batch: {}
  exporters:
    otlphttp/prometheus:
      endpoint: http://prometheus:9090/api/v1/otlp
      tls: {insecure: true}
    debug:
      verbosity: basic
  service:
    pipelines:
      metrics: {receivers: [otlp], processors: [batch], exporters: [otlphttp/prometheus]}
      traces: {receivers: [otlp], processors: [batch], exporters: [debug]}
  ```

- `prometheus.yaml`:

  ```yaml
  global: {scrape_interval: 15s}
  otlp:
    translation_strategy: UnderscoreEscapingWithSuffixes
    promote_resource_attributes: [service.instance.id]
  ```

  with the command `--config.file=/etc/prometheus/prometheus.yaml --web.enable-otlp-receiver
  --storage.tsdb.retention.time=2d`.
- `grafana/provisioning/datasources/prometheus.yaml` (uid `prometheus`, URL
  `http://prometheus:9090`), `grafana/provisioning/dashboards/pandokos.yaml` (file provider over
  `/var/lib/grafana/dashboards`), environment `GF_AUTH_ANONYMOUS_ENABLED=true`,
  `GF_AUTH_ANONYMOUS_ORG_ROLE=Viewer`, `GF_SECURITY_ADMIN_PASSWORD__FILE=/run/secrets/grafana_admin`,
  `GF_ANALYTICS_REPORTING_ENABLED=false`, `GF_ANALYTICS_CHECK_FOR_UPDATES=false`.
- `deploy/compose/setup.sh` creates `secrets/grafana_admin` (random, as the other secrets) — only
  this addition.
- `grafana/dashboards/pandokos-inference.json` and `pandokos-internals.json`: the panels of §5.11.
  Queries use only Prometheus names of `docs/reference/metrics.md`. Examples (shape, not to copy
  blindly): p95 of the provider call by model
  `histogram_quantile(0.95, sum by (le, gen_ai_request_model) (rate(gen_ai_client_operation_duration_seconds_bucket{gen_ai_request_model=~"$model"}[$__rate_interval])))`;
  tokens per second
  `sum by (gen_ai_request_model, gen_ai_token_type) (rate(gen_ai_client_token_usage_sum[$__rate_interval]))`;
  cost per hour
  `sum by (gen_ai_request_model, pandokos_currency) (increase(pandokos_gen_ai_client_cost_total[1h]))`.
  The names are confirmed against the running stack (D3) before the commit.

### D3. Compose tests — `test/compose/observability_test.go`

Reuse `stack` of `compose_test.go` (copy of the reference, `setup.sh`, the override with
`PANDOKOS_TEST_IMAGE` and the fake provider on the host) and add `-f observability/compose.yaml`.

- **T-931**: create a tenant, two catalogue models of the fake provider (as the quick-start test
  does), a key; run 40 calls (half streams) alternating the models; wait (`Eventually`, 60 s) until
  Prometheus answers `gen_ai_client_operation_duration_seconds_count` for both models; then for every
  panel of both dashboards: expand `$model`, `$provider` to `.*`, `$__rate_interval` to `1m`,
  `$datasource` away, and run the query through `GET /api/v1/query` on `127.0.0.1:9090`; assert at
  least one series, and for the latency, token and cost panels of *Inference* one series per model
  with `gen_ai_request_model` in {the two names}. `GET /api/search?query=Pandokos` on Grafana lists
  both. Create a platform key with `--scope metrics:read` through the CLI in the container, scrape
  `https://127.0.0.1:8081/metrics` (the CA of the reference), take the metric family names;
  compare with `GET /api/v1/label/__name__/values` restricted to names starting with `gen_ai_`,
  `http_server_`, `pandokos_` (the OTLP path), after stripping `_bucket`, `_sum`, `_count`: equal.
- **T-932**: 30 calls timed (medians and p95 as §9), `docker compose stop otel-collector`, 30 calls
  timed, assertions of §9; the Pandokos log (`docker compose logs pandokos`) has `metric export
  failing` within 20 s and no `"level":"ERROR"`; `docker compose start otel-collector`, and the
  series of M2 increases again within 60 s.

### D4. CI — `.github/workflows/ci.yml`

The job `image` already runs `go test -tags compose -count=1 -timeout 15m ./test/compose/`; raise the
timeout to `25m` if the run with the overlay needs it (measure in the pull request). No new job. No
action is added or re-pinned.

Verification (in CI; locally with Docker): `make image && make test-compose`.

---

## Part E — Documentation and decision records (commit: `docs: sprint 6.1 telemetry guide, metrics and log references, ADR 0062 and 0063, OBS-07`)

### E1. Guides and references

- `docs/operations/telemetry.md`: rewrite the introduction (traces and metrics); keep sections 1 to 8
  for traces with the shared keys; add **9. Metrics** (what is exported, the pinned version, the
  three keys, the interval, the collector away), **10. The scrape endpoint** (enabling it, the
  permission, creating a scoped platform key, a Prometheus scrape example with
  `authorization: {type: Bearer, credentials_file: …}` and the CA, the format and names), **11. What a
  metric may say** (§5.5 in operator words, the credential exception, the residual of R-7),
  **12. Dashboards** (the overlay, how to open Grafana, what each dashboard shows, the signals worth
  an alert: M31 above zero, M34 growing, M12 `lost`, M13, M16, M40, M41 above zero), and in section 7
  the renamed line `telemetry error`. Remove "Metrics come with a later release".
- `docs/reference/metrics.md` (new): the pinned version and where it comes from; the table of §5.3
  with, per instrument, the OpenTelemetry name, kind, unit, attributes, Prometheus name and when it
  is recorded; the closed values of §5.3.1; rule R; the bucket boundaries. T-R65 reads it.
- `docs/reference/logs.md` (completed from the skeleton of B1): an introduction (JSON on standard
  output, the fields `time`, `level`, `msg`; the redaction of §5.8; the rule of ADR 0055 and the only
  identifiers that appear), `## Fields` (the glossary table: field, type, meaning), then one section
  per component, each message as `` ### `message` `` with `Level:`, `Fields:` and one sentence on when
  it is written and what to do. Format fixed by B1's parser.
- `docs/operations/configuration.md`: the three keys in the tables of keys, rules and warnings.
- `docs/operations/compose.md`: a section on the observability overlay (start, ports, secrets,
  stopping it; that it is an example, not a hardened monitoring stack).
- `docs/reference/data-flows.md`: the collector row covers metrics (what M1 to M41 carry and do not
  carry); a new row for the scraper of `GET /metrics` (receives the same, with the platform key it
  presents).
- `docs/architecture/security-model.md`: the metrics in the row of a tenant's usage data (aggregates
  per installation, the credential ID on M9, the residual of R-7); the scrape endpoint under the
  administration listener.
- `docs/architecture/overview.md`: the telemetry component builds traces and metrics and the scrape
  format; the redacting handler; the table of technologies.
- `docs/requirements/README.md`: OBS-07 as quoted in §3.
- `docs/releases/v0.2.0-draft.md`: metrics (GenAI conventions v1.41.0 pinned), the three keys, the
  permission `metrics:read` in both platform roles, the renamed line `trace export failing` →
  `telemetry error` for SDK errors, the observability overlay, the log reference.
- `docs/reference/permissions.md`: generated (part A).

### E2. Decision records

Add both to `docs/adr/README.md`. Texts:

```markdown
# 0062. Metrics: instruments, attributes, cardinality and the scrape endpoint

- Status: proposed
- Date: 2026-11-22

Refines [ADR 0015](0015-observability.md), [ADR 0040](0040-traces.md),
[ADR 0054](0054-usage-data-is-the-tenants.md) and
[ADR 0055](0055-operational-telemetry-holds-no-tenant-data.md). Requirements OBS-01, OBS-03, OBS-06,
OBS-07.

## Context

ADR 0015 asks for metrics under the GenAI conventions, pinned, with a Prometheus endpoint on the
admin port and a tenant dimension as an opt-in. ADR 0054 and 0055 then decided that operational
telemetry holds no tenant data and that cost and token figures exist per installation only. The
conventions are in Development and, since 2026, outside the Go semantic-convention packages.

## Decision

1. Metrics use the OpenTelemetry Go SDK with the traces' resource. They are exported over
   OTLP/HTTP to the traces' collector (`/v1/metrics`, the same transport, headers and timeout,
   cumulative, no retry) and, when enabled, served at `GET /metrics` on the administration
   listener. With neither, no meter provider exists.
2. The GenAI conventions are pinned at **1.41.0** by the import path
   `go.opentelemetry.io/otel/semconv/v1.41.0` and the meter's schema URL. The GenAI metrics carry
   the Required attributes and the Conditionally Required model (the catalogue name) and
   `error.type`; no response model, server address or port. Token usage holds what the provider
   reported; estimates have their own counter. The HTTP server duration uses the GenAI boundaries.
3. The instruments are a closed catalogue, published in the metrics reference. Every attribute value
   is from a closed list or the platform catalogue; each instrument is capped at 2000 attribute sets;
   exemplars are off.
4. **No metric names a tenant, caller, key, request or trace, and no setting adds one.** The
   opt-in tenant dimension of ADR 0015 is withdrawn; a dimension without the identity in clear is
   issue #52.
5. **One exception**: the provider's 429 answers are counted per provider credential ID and
   provider, on that counter alone (issue #32), so that an operator sees which credential is
   saturated. The ID says nothing of usage, cost or caller and is resolved only through the audited
   `tenant_credential:read` on a named tenant.
6. Cost is the recorded cost of each request, with its currency, summed per model and currency and
   never across currencies; there is no price-source dimension.
7. The scrape endpoint requires a bearer credential of a platform principal holding the platform
   permission `metrics:read` (held by `platform-admin` and `platform-auditor`). Pandokos writes the
   Prometheus text format itself; names follow Prometheus's own OTLP translation with suffixes, so
   one dashboard serves both paths.
8. The gateway reports one observation per request through a port whose type has no tenant field;
   other components expose snapshots read at collection. Recording never waits for the network.

## Consequences

- Operators get latency, tokens and cost by model without seeing any tenant; where a tenant alone
  uses a model or a currency, the aggregate is that tenant's, which is stated.
- A credential ID appears in the operator's time series when its provider answers 429.
- A change of convention version is a change of import path and a release-notes line; after
  1.41.0 the source of names must be found again.
- Two modules enter the graph; no Prometheus client.

## Alternatives considered

**Tenant labels as an opt-in (ADR 0015).** Contradicts ADR 0055 §8. **The OpenTelemetry Prometheus
exporter.** Seven modules, pre-1.0, for a format of a few hundred lines. **An unauthenticated scrape
endpoint.** The admin listener carries no anonymous data path today. **A keyed pseudonym for the
credential.** Needs the key management of #52. **Estimates inside `gen_ai.client.token.usage`.**
The conventions reserve it for counts obtained from the system.

## History

- 2026-11-22: proposed with sprint 6.1.
```

```markdown
# 0063. The log: redaction by key and by shape, and the log reference

- Status: proposed
- Date: 2026-11-22

Refines [ADR 0015](0015-observability.md) and [ADR 0055](0055-operational-telemetry-holds-no-tenant-data.md).
Requirements OBS-01, OBS-04.

## Context

ADR 0015 asks for a handler that redacts known sensitive keys; ADR 0055 decides what a line may
say about a tenant. Keys catch a secret under a known name, not a secret inside an error text, and
nothing listed what lines and fields exist, so a new field could carry tenant data unnoticed.

## Decision

1. Every logger of the product is built on one handler that redacts the values of sensitive keys
   and, in the message and every value, the shapes of Pandokos secrets: virtual and platform keys,
   session tokens, bearer values, compact JWS. Each redaction by shape is counted in a metric.
2. `docs/reference/logs.md` lists every message with its level and fields. A test over the code
   fails on a message or a field that is not in it, and on tenant-identifying keys outside the
   lines it marks as platform identities; every end-to-end process has its output checked against
   it, with the fixtures' tenant identifiers as canaries.
3. The process-wide error line of the OpenTelemetry SDK is `telemetry error`; export failures
   are `trace export failing` and `metric export failing`.

## Consequences

Adding a log line means adding a row to the reference. A secret that reaches a logger by mistake is
redacted and shows up in the redaction counter. The shapes are checked against the generators, so a
change of a key format fails a test.

## Alternatives considered

**Key-based redaction only.** Misses secrets inside error texts. **A field allow-list in the
handler.** Couples the handler to every component; the reference with tests gives the same guarantee
without a run-time cost. **Removing lines instead of documenting them.** The lines are needed to run
the installation.

## History

- 2026-11-22: proposed with sprint 6.1.
```

History lines (append to `## History`, dated 2026-11-22):

- 0015: "sprint 6.1: metrics over OTLP and at `GET /metrics` for `metrics:read`, GenAI conventions
  1.41.0; the tenant dimension as an opt-in is withdrawn; the redacting handler redacts secret
  shapes too ([ADR 0062](0062-metrics-instruments-attributes-cardinality-and-scrape.md),
  [ADR 0063](0063-the-log-redaction-and-the-log-reference.md))."
- 0018: "sprint 6.1 adds `go.opentelemetry.io/otel/sdk/metric` and
  `go.opentelemetry.io/otel/exporters/otlp/otlpmetrichttp` (v1.47.0) as direct dependencies, and
  writes the Prometheus format instead of adding a Prometheus client ([ADR 0062](…))."
- 0019: "sprint 6.1: `telemetry.metrics.otlp`, `telemetry.metrics.export_interval`,
  `telemetry.metrics.prometheus` ([ADR 0062](…))."
- 0040: "sprint 6.1: the resource and the collector connection are shared with metrics; the
  process-wide SDK error line is `telemetry error` ([ADR 0062](…), [ADR 0063](…))."
- 0054: "sprint 6.1: cost and token metrics are per installation, model and currency, with no
  tenant dimension ([ADR 0062](…))."
- 0055: "sprint 6.1: metrics carry the catalogue model and no tenant data; the provider credential
  ID on the provider-429 counter is the one identifier on a metric; the log reference enforces §2
  and §3 ([ADR 0062](…), [ADR 0063](…))."
- 0060: "sprint 6.1: the platform permission `metrics:read`, held by `platform-admin` and
  `platform-auditor` ([ADR 0062](…))."

(Write the full relative links in place of `(…)`.)

### E3. Planning files and traceability

- `planning/epics/06-observability-reports-lifecycle/epic.md`: no change in this sprint.
- The sprint's status stays `todo` in this commit; it becomes `done` at the close, with the roadmap
  row, by the orchestrator.

Verification: `go test -count=1 ./test/repo/` (T-R65 to T-R70 pass; `TestConfigurationReference`,
`TestReferenceDocuments` pass); `make generate-check`.

---

## Part F — Verification (no commit unless something is found)

```sh
make check
go test -race -count=1 ./...
go test -run '^$' -bench 'BenchmarkRequestWithMetrics' -benchmem ./internal/gateway/
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -test ./...
grep -rnE '"(tenant_id|key_id|principal_id|service_account_id|credential_id|issuer_id)"' --include=*.go internal/telemetry cmd/pandokos/metrics.go
git diff --stat origin/main -- go.mod   # three direct lines, nothing else
```

Check by reading, with §5.5 as the list: `telemetry.Request`, every `Stats()` snapshot, every
attribute key in `instruments.go`; the dashboards' queries; that the overlay publishes only
`127.0.0.1` ports. In the pull request: the allocation figure of T-915, the benchmark output, the
digests of D1 with their commands and date, the CI duration of the `image` job, and the list of open
points of §12 with the maintainer's decisions when given.

## What the implementer does not do

- Add a tenant, caller, key, request or trace attribute to any metric, a setting that would, or an
  attribute to a span.
- Add a Prometheus client or any module beyond the two of A1.
- Mount the scrape handler without authentication, on the inference listener, or inside the OpenAPI
  document.
- Change the request line's fields, the usage record, the limits' decisions or the error contract.
- Pin an image by tag alone or write a digest that was not obtained by the commands of D1.
