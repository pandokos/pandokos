# Sprint 3.1 — Implementation plan

- Design: [sprint.md](sprint.md). Section numbers (§) refer to it.
- Each part below is self-contained: an agent needs sprint.md and that part only. Parts run in
  order A, B, C1 … C8, D, E, F.
- Suggested grouping for implementers: **G1** = C1, C2 (two leaf packages, independent of each
  other); **G2** = C3, C4 (validation and download behind the guard; audit and resolver);
  **G3** = C5, C6 (gateway, then server and wiring); **G4** = C7, C8 (configuration, end to
  end). A, B, D, E and F are one agent each.

## Rules for the implementer

1. Branch `feat/3.1-authenticated-passthrough` from `main`; sign off every commit. One commit
   for A, one for B (tests, failing), one per step of C (message given with the step), one for
   D, one for E.
2. **No Go dependency is added.** `git diff main -- go.mod go.sum test/sdk/go.mod test/sdk/go.sum`
   prints nothing at the end.
3. **Outbound HTTP exists in `internal/egress` only** (and in the CLI's client, untouched). No
   other package under `cmd/` or `internal/` builds an `http.Transport` or a `net.Dialer`, or
   names `http.DefaultClient`, `http.DefaultTransport`, `http.Get`, `http.Post`, `http.Head`
   (T-R25). `InsecureSkipVerify` and `ProxyFromEnvironment` are never written.
4. **Nothing of a request body, a header value or a secret is logged or put in an error.** The
   log fields of §5.9 are the complete list. `secret.Secret.Reveal()` is called in exactly two
   places of this sprint: `Adapter.AttachCredential`'s caller in the gateway and the validator.
   An error returned to a caller of the product is a constant; an error of `net/http` (it can
   quote a URL) is never wrapped into a response, a `BlockedError.Error()` never holds an
   address.
5. The order of §5.1 is the design. Do not move a step to make a test simpler.
6. Appendix A (migration) and appendix B (query) are copied exactly; appendix A was applied to
   PostgreSQL 16 as written. If one does not work as written, stop and ask.
7. Generated files are never edited by hand: change the source, run `make generate`.
8. SQL runs only as `queries.New(tx).<Method>` inside a `store` helper (T-R10, T-R12).
9. Do not "fix" a permission error of the gateway role by a grant or a policy: it holds no
   privilege on `audit_event`, by design (§5.10).
10. Tests hold for root and for a non-root user; no assertion depends on file permissions.
    Tests that need a second local address use what `test/testutil` already offers
    (`NonLoopbackHost`) and skip, with a message, only where that helper says none exists.
11. Test values that look like keys start with `synthetic-` or are the canaries of
    `test/fixtures`. A bound is never made to pass by lengthening it, a database test never by
    skipping it. Nothing sleeps longer than 2 s; waiting is `testutil.Eventually`.
12. English everywhere; every exported identifier has a doc comment.

## Environment

- `make db-up` starts PostgreSQL 16 with TLS and PgBouncer (Docker) and writes
  `local/testdb/env`; `make db-down` discards them.
- `make test` (root module, race detector, needs the database), `make test-sdk`, `make image`
  then `make test-container`, `make fuzz`.
- `make generate` (`sqlc` needs the release toolchain of `.go-version`), `make generate-check`,
  `make migrations-sum`.
- `make lint`, `make vuln`, `make secrets`, and `make check` for everything a pull request must
  pass except the image.
- Before starting: `make db-up && make check` must pass on `main`.

---

## Part A — Scaffolding (commit: `chore: gateway scaffolding, migration 0005, declarations`)

After this part product code compiles (`go build ./...`) and every existing test still passes.
It adds declarations only: nothing changes behaviour.

**Files:** `internal/store/migrations/sql/0005_gateway_auth_failures.sql`, `migrations.sum`,
`internal/store/queries/sql/audit.sql`, generated `internal/store/queries/*`,
`internal/egress/{doc,classify,guard}.go`, `internal/family/adapter.go`,
`internal/family/family.go`, `internal/family/openai/{adapter,request}.go`,
`internal/gateway/ports.go`, `internal/resolver/{doc,catalogue,credentials,failures}.go`,
`internal/audit/audit.go`, `.golangci.yml`.

1. **Migration.** Create `0005_gateway_auth_failures.sql` from appendix A, byte for byte;
   `make migrations-sum` adds one line and changes no other.
2. **Query.** Append appendix B to `audit.sql`; `make generate`; commit the generated files.
   Add `audit.RecordAuthFailure(ctx context.Context, tx store.Tx, keyID, reason, requestID string) error`
   calling the generated method.
3. **`internal/egress`.** `doc.go` (package comment naming ADR 0038, GW-05, GW-06, SEC-01) and
   the declarations of §5.5 (`AddrClass` and its three constants, `Classify`, `Options`,
   `Guard`, `New`, `Transport`, `CloseIdleConnections`, `BlockedError` with `Error`,
   `BlockedReason` and `BlockedAddr`, the last three implemented now). Bodies:
   `New` returns `errors.New("egress: not implemented")`; `Classify` returns `AddrForbidden`.
4. **`internal/family`.** Add the four codes as constants (`CodeModelNotAllowed`,
   `CodeNoCredential`, `CodeInvalidRequest`, `CodeRequestTimeout`) without touching `Status`
   and `Message` (they fall to the default until C2). Create `adapter.go` in its final form:

   ```go
   // Operation is one metered endpoint of a family.
   type Operation struct {
       Name   string // "chat.completions"
       Method string // "POST"
       Path   string // native path appended to the endpoint base URL: "/v1/chat/completions"
   }
   // Request is what a family read from a request body.
   type Request interface {
       Model() string  // as the caller wrote it, JSON-decoded
       Stream() bool
       // Prepare returns the body to forward: the caller's bytes, with the usage option added
       // when the family needs it (strip reports that) and the model replaced by upstreamModel
       // when that is not empty and differs from Model().
       Prepare(upstreamModel string) (body []byte, strip bool)
   }
   // StreamReader reads usage from a response stream and says what to forward.
   type StreamReader interface {
       Filter(p []byte) []byte
       Flush() []byte
       Usage() (Usage, bool)
       Failed() bool
   }
   // Adapter is everything the gateway needs to know about one wire protocol (ADR 0009).
   type Adapter interface {
       Name() string
       Operations() []Operation
       Match(method, path string) (Operation, bool)
       InboundCredential(h http.Header) (string, bool)
       AttachCredential(h http.Header, secret string)
       RequestHeaders() []string
       AllowResponseHeader(canonicalName string) bool
       ParseRequest(op Operation, body []byte) (Request, error) // wraps ErrInvalidRequest
       ReadUsage(op Operation, body []byte) (Usage, bool, error)
       NewStreamReader(op Operation, strip bool) StreamReader
       WriteError(w http.ResponseWriter, code ErrorCode)
   }
   // ErrInvalidRequest is wrapped by every refusal of ParseRequest.
   var ErrInvalidRequest = errors.New("family: invalid request")
   // InvalidReason returns the reason of a refusal of ParseRequest ("" when err is not one).
   func InvalidReason(err error) string
   ```

   `InvalidReason` is implemented now (an unexported error type `invalid{reason string}` with
   `Unwrap() error { return ErrInvalidRequest }` and the constructor
   `Invalid(reason string) error`).
5. **`internal/family/openai`.** Declare `Parse`, `Request` with `Model`, `Stream`, `Prepare`,
   and `Adapter() family.Adapter` (§5.8). Stub bodies: `Parse` returns
   `family.Invalid("not_implemented")`; `Adapter()` returns a value whose methods delegate to
   the existing free functions and whose `ParseRequest` calls `Parse`. Do not change `route.go`
   yet.
6. **`internal/gateway/ports.go`.** The declarations of §5.4 in their final form (`Target`,
   `Catalogue`, `Credential`, `Credentials`, `AuthFailure`, `FailureRecorder`, the four
   errors, `Route` and `Routes()` returning `nil`). Add the new fields to `Options` next to the
   old ones; `New` ignores them.
7. **`internal/resolver`.** `doc.go` and the three constructors of §5.4 returning types whose
   methods return `errors.New("resolver: not implemented")` (`Record` returns false).
8. **Linter** (`.golangci.yml`, `depguard`): a rule `egress-is-a-leaf` (strict, `$gostd` only,
   files `**/internal/egress/**`, tests excepted); extend `gateway-takes-plain-values` with
   deny entries for `internal/resolver`, `internal/egress`, `internal/vault`,
   `internal/catalog`; a rule `resolver-is-wiring` denying `internal/resolver` in
   `**/internal/**` except `**/internal/resolver/**` and tests.

**Verify:** `go build ./... && make generate-check && make lint && make test`.

---

## Part B — Tests first (commit: `test: sprint 3.1 tests, failing`)

Write every test of §9 and adapt every changed test before any file of part C. Tests are written
against the **final** API of sprint.md. Where that API replaces an existing one incompatibly
(`vault.Validator`, `catalog.FetchOptions`, `gateway.Options`, `server.Options`,
`config.Config` without `Skeleton`, `fakeprovider.URL`), the test package does not compile until
its C step: that is its failing state. Product code still compiles: `go build ./...` passes.
Then run `go vet ./... ; make test ; make test-sdk` and keep the output for the pull request.

General rules: a test that sweeps "every code", "every operation" or "every header" ranges over
the product's own list (`family` codes, `Adapter.Operations()`, `RequestHeaders()`) and fails
on an entry it does not know. Canary assertions use `testutil.AssertNoCanary` and
`testutil.AssertAbsent` with `KeyForms` and `SecretForms`.

### B1. Helpers

- `test/fakeprovider`: remove `BaseURL()` (callers use `URL()` as the endpoint base URL). Add
  `RedirectTo(url string)`; models `fake-redirect` (answers the status given in the request
  header `X-Fake-Status`, default 307, with `Location` set by `RedirectTo`),
  `fake-echo-auth` (200 with the received `Authorization` copied into `X-Echo-Authorization`
  and `Www-Authenticate`), `fake-close` (hijacks and closes the connection without a response,
  after having answered one earlier request on the same connection normally);
  `Connections() int` (accepted connections, through `httptest.Server.Config.ConnState`).
  `handleModels` accepts either `Authorization: Bearer <key>` or, when the request carries
  `anthropic-version`, `x-api-key: <key>`. `Requests()` entries expose method, path, raw query,
  header and body, as today.
- `test/testutil`:
  - `StaticCatalogue(providers map[string]string, targets map[string]gateway.Target) *Catalogue`
    (`providers`: name → family; `targets`: `provider/model` → target; a missing key is
    `gateway.ErrModelNotAllowed`); fields `ProviderErr`, `ResolveErr`, `Block chan struct{}`
    (when non-nil `Resolve` waits for it or for the context), counters `Providers`, `Resolves`.
  - `StaticCredentials(secrets map[string]string) *Credentials` (provider → secret; missing:
    `gateway.ErrNoCredential`); field `Err`; `Calls()` returning the `(requestID, provider)`
    pairs.
  - `TargetFor(up *fakeprovider.Server, provider, model string) gateway.Target` (private,
    upstream model equal to the name, IDs `synthetic-…`).
  - `TestGuard(tb) *egress.Guard` (connect timeout 2 s, system roots, no proxy; idle
    connections closed at cleanup).
  - `ConnectProxy(tb) (proxyURL *url.URL, requests func() []*http.Request)`: an HTTP/1.1
    server on loopback that answers `CONNECT` by dialling the target and splicing, and records
    each request line and header it received.
- `test/e2e`: `(*proc).useFakeProvider(t, platformFile, provider string, up *fakeprovider.Server)`
  (CLI `endpoint update` of the provider's `default` endpoint: `--base-url up.URL() --private`;
  read the flags in `internal/cli/catalogue.go`), `(*proc).addCredential(t, adminFile, provider,
  secret string) (id string)` (CLI `credential create` with the secret from a file),
  `(*proc).inferenceKey` extended so that the tenant it creates has a credential for `openai`
  equal to `fixtures.CanaryCredential`, and `up.Accept(fixtures.CanaryCredential)`. `baseEnv`
  loses the two `PANDOKOS_SKELETON_…` variables and its `baseURL` parameter.

### B2. Isolation spec

`test/isolation/spec.go`: add `record_auth_failure` to `SecurityDefiner`. Grants unchanged.
T-443 in `test/isolation/audit_test.go`, for each of the three runtime roles and for a login
with no role membership (`d.CreateLogin`).

### B3. Unit and local-socket tests

| Tests | File |
|---|---|
| T-413, T-414, T-416 | `internal/egress/classify_test.go`, `guard_test.go` |
| T-415, T-417 to T-420 | `internal/egress/guard_local_test.go` |
| T-421, T-422, T-423 | `internal/family/openai/request_test.go` (existing T-26, T-27 stay) |
| T-424 | `internal/family/openai/adapter_test.go`; T-22, T-23, T-36 adapted in place |
| T-444 | `internal/vault/validate_test.go` (replaces `TestOpenAIValidator` and `TestProxyChild`) |
| T-446 | `internal/catalog/access_test.go`, `fetch_test.go` |
| T-447, T-448 | `internal/config/upstream_test.go`; T-04 to T-15 adapted in `config_test.go` |

Notes:

- T-415/T-417/T-419 need the *public* transport to reach a local server. Production code has
  no switch for that. `internal/egress` keeps an unexported field `classify func(netip.Addr)
  AddrClass` on the guard (default `Classify`); the tests, in package `egress`, build a guard
  with `newGuard(o, classify)` whose function calls loopback public. Nothing outside the
  package can set it.
- T-417: certificates from `test/testcert`; the TLS 1.1 server is an `httptest` server with
  `MaxVersion: tls.VersionTLS11` (and `MinVersion: tls.VersionTLS10`).
- T-418: `t.Setenv` for the three variables in both letter cases.
- T-420: host H is the loopback listener's `127.0.0.1:port`; the private transport succeeds,
  then the public one (default classification) must fail with `private_address` — if it
  succeeded it would have reused the pooled connection.
- T-423: seed with the bodies of `request_test.go`; add the line to `make fuzz` in part D.
- T-447: write a CA with `testutil.WriteCA`; the "no certificate" case is a PEM file whose
  only block has another type (`-----BEGIN X509 CRL-----` around synthetic bytes). Never write
  a private-key block into a fixture (T-R19).

### B4. Gateway and server tests

`internal/gateway/gateway_test.go`: rebuild `newHarness` on `Options{Catalogue:
testutil.StaticCatalogue(…), Credentials: testutil.StaticCredentials(…), Families:
{"openai": openai.Adapter()}, DefaultFamily: "openai", Public/Private: from
testutil.TestGuard}`; the default target is `testutil.TargetFor(up, "openai", "fake-model")`
and the default secret `fixtures.CanaryProviderKey`. Keep the names and assertions of T-37 to
T-56 and T-267; change only what §9 lists (paths, T-46's closed port as a private target,
T-52's list). Write T-425 to T-439 in `internal/gateway/pipeline_test.go` and
`headers_test.go`.

- T-426: the body is an `io.Reader` that calls `t.Error` on `Read`, sent with a
  `Content-Length`; build the request with `httptest.NewRequest` and call `ServeHTTP` directly
  for the steps before 5, so that no client buffers the body.
- T-432: force a transport error whose text contains the URL with a `RoundTripper` that returns
  `&url.Error{Op: "Post", URL: target, Err: errors.New(fixtures.CanaryProviderKey)}`.
- T-434: targets `http://127.0.0.1:<port>` and `http://localhost:<port>` are not usable with
  `Private: false` (the scheme is refused first): use an `httptest` TLS listener and
  `https://…` so that the refusal is the address; assert `egress_reason` is `private_address`
  and `fakeprovider.Connections()` is zero.
- T-436: raw TCP client as in the existing T-44.
- T-437: a `FailureRecorder` that stores its calls; authenticator returning the `reasoned`
  error type of the existing tests with and without a tenant ID.

`internal/server/server_test.go`: `newOptions` passes `Catalogue`, `Credentials`, `Egress:
testutil.TestGuard(t)`; T-450 next to T-59 and T-60. `test/sdk/sdk_test.go`: `start` builds
the same options; the base URL stays `http://<addr>/openai/v1`.

### B5. Database and API tests

| Tests | File |
|---|---|
| T-440, T-441, T-442 | `internal/resolver/resolver_db_test.go` (package `resolver_test`; `dbtest.NewMigrated`, `apitest` or direct `admin` calls to create tenants, keys, credentials; principals from `authn` on the gateway pool) |
| T-449 | `internal/audit/audit_db_test.go` |
| T-445 | `internal/admin/credentials_ops_db_test.go` (next to `TestValidationAtWrite`, which is adapted: `apitest.New` passes `Validator: vault.NewValidator(guardPublic, guardPrivate, 2*time.Second)` and the tests point the provider's endpoint at the fake with `private: true`) |

- T-440 "endpoint URL fails validation": the check constraints of 2.2 refuse such a row, so
  build the `catalog.Cache` over a snapshot with `catalog.NewSnapshot` holding an endpoint
  `http://synthetic.invalid` with `Private: false`; if `resolver.NewCatalogue` can only take a
  `*catalog.Cache`, the resolver keeps an unexported interface for what it uses (`Snapshot`,
  `Resolve`) and the test, in package `resolver`, injects a fake.
- T-442: clock injected through `NewFailureAuditor`'s `now`; "database stopped" with
  `d.SetAcceptingConnections(t, false)`.

### B6. End-to-end, SDK, container, repository

- `test/e2e/gateway_test.go`: T-451 to T-455. T-452 (a) needs a listener the binary would
  reach as `localhost`: an `httptest` TLS server on loopback with a certificate for
  `localhost` from the test CA, `upstream.tls.ca_file` set, endpoint `https://localhost:<port>`
  with `private: false`; assert `Connections()` stays zero. T-454's proxy case uses the same
  listener through `testutil.ConnectProxy`: there the call succeeds, which is the documented
  delegation to the proxy (§5.5, rule 3).
- Adapt T-65, T-66, T-162 and the 1.3, 2.1 and 2.2 e2e tests to the helpers of B1; their
  canary lists gain the credential secret where a call is made.
- `test/container/container_test.go` is adapted in part D (it needs the image).
- `test/repo`: T-R25 (`TestPlanesDoNotImportEachOther` rules: `gateway` banned list gains
  `resolver`, `egress`; a block "what egress imports" as for the vault; `resolver` reached by
  `cmd/pandokos` only; new `TestOutboundHTTPInOnePlace` with `go/ast` over `cmd/` and
  `internal/`, non-test files), T-R26, T-R27; T-R14 and T-R22 expectations updated.

**Verify:** `go build ./...` passes; `go vet ./... ; make test ; make test-sdk` fail, and every
failure is a test of §9 or a changed test (attach the output).

---

## Part C — Implementation

Each step ends with the tests it names passing and `go build ./... && make lint` clean.

### C1. `internal/egress` — `feat: egress guard`

**Files:** `internal/egress/classify.go`, `guard.go`. **Tests:** T-413 to T-420.

- `Classify`: `a = a.Unmap().WithZone("")`; tables of `netip.Prefix` built once with
  `netip.MustParsePrefix`; forbidden checked first, then embedded prefixes (NAT64: bytes 12 to
  15; 6to4: bytes 2 to 5 → recurse once on the IPv4 address), then private, else public. An
  invalid `Addr` is forbidden.
- `New`: `ConnectTimeout > 0`; `Proxy`, when set, has scheme `http` or `https` and a host. Two
  `*http.Transport` values with: `DialContext` of a `net.Dialer{Timeout, KeepAlive: 30 s,
  Control}`; `TLSHandshakeTimeout: ConnectTimeout`; `TLSClientConfig{MinVersion: TLS 1.2,
  RootCAs}`; `ForceAttemptHTTP2: true`; `MaxIdleConns: 256`, `MaxIdleConnsPerHost: 256`,
  `IdleConnTimeout: 90 s`, `ExpectContinueTimeout: 1 s`; `DisableCompression: true`;
  `MaxResponseHeaderBytes: 262144`; `Proxy`: `http.ProxyURL(o.Proxy)` on the public transport
  when set, otherwise `nil` on both.
- Control hooks: *public, direct* accepts `AddrPublic`; *private* accepts public and private;
  *public, proxied* accepts public and private (the dial is to the proxy). All refuse a network
  other than `tcp4`/`tcp6` (`not_tcp`), an address `netip.ParseAddrPort` rejects
  (`unparsable`), and forbidden addresses.
- `Transport(private)` returns a small `http.RoundTripper` that runs the pre-flight of §5.5
  rule 1 and then calls the transport. Pre-flight failures and control failures are
  `*BlockedError`; `Error()` is `"egress: blocked: " + Reason`.
- `CloseIdleConnections` closes both pools.

**Verify:** `go test -race ./internal/egress/`.

### C2. Family adapter and request parsing — `feat: family adapter, model parsing for the openai family`

**Files:** `internal/family/family.go`, `internal/family/openai/{adapter,request,route,errors}.go`.
**Tests:** T-421 to T-424, T-22, T-23 to T-36.

- `family`: `Status` and `Message` for the four codes (§5.8).
- `route.go`: `PathChatCompletions = "/v1/chat/completions"`; `Match` compares the native path;
  `RequestHeaders` per §5.3.
- `request.go`: `Parse` uses `scanObject` once, keeps the members; counts top-level keys equal
  to `model` (the decoder already unescapes keys); decodes the value with `json.Unmarshal` into
  a `string` after checking its first byte is `"`. `Prepare` computes the usage edit exactly as
  `PrepareRequest` does today and the model edit, applies them from the higher offset to the
  lower, and returns the input slice when there is no edit. The upstream identifier is encoded
  with a `json.Encoder` with `SetEscapeHTML(false)` (trailing newline removed).
  `PrepareRequest` becomes the wrapper of §5.8; T-26 and T-27 must pass unchanged.
- `adapter.go`: one operation; `ReadUsage` and `NewStreamReader` return the existing
  implementations (`*StreamReader` already has the four methods).

**Verify:** `go test -race ./internal/family/...`; `go test ./internal/family/openai/ -run '^$'
-fuzz '^FuzzParseRequest$' -fuzztime 30s`.

### C3. Validation and price download behind the guard — `feat: credential validation and price download through the egress guard`

**Files:** `internal/vault/validate.go`, `internal/admin/{admin,credentials}.go`,
`internal/catalog/{access,fetch,refresh}.go`, `cmd/pandokos/serve.go`, `test/apitest/apitest.go`.
**Tests:** T-444, T-445, T-446, the adapted 2.1 and 2.2 tests.

- `vault`: the API of §5.6. One `Validate`: unknown family → `Indeterminate, 0` without a
  request; build `GET` on `strings.TrimRight(BaseURL, "/") + "/v1/models"`; set the family's
  headers; `ctx` with the timeout; `RoundTrip` on the class's round tripper; close the body
  after discarding at most 64 KiB. Remove `NewOpenAIValidator`.
- `catalog`: `Snapshot.Provider`, `Snapshot.DefaultEndpoint` (and `Resolve` uses the latter);
  `FetchOptions.Transport` required (`Fetch` returns an error naming no URL when nil), the
  private `roots` field and the transport literal removed; `Refresher.Fetch` stays injectable.
- `admin`: `Options.Validator`; `validatorFor` becomes `validationTarget(ctx, provider, validate)
  (*vault.ValidationTarget, *adminapi.FieldError, error)` per §5.6 (load failure → the
  handler's `unavailable` path); `validate` passes the target. The log line `credential
  validation` keeps its fields.
- `serve.go`: build the guard here (`egress.New(egress.Options{ConnectTimeout:
  cfg.Upstream.ConnectTimeout})` for now; C7 adds roots and proxy), pass
  `vault.NewValidator(...)` and a `Fetch` closure with `guard.Transport(false)` to the admin
  handler and the refresher. The gateway still gets its own transport until C6.

**Verify:** `go test -race ./internal/vault/ ./internal/catalog/ ./internal/admin/`.

### C4. Audit function and resolver — `feat: resolver, audit of inference authentication failures`

**Files:** `internal/resolver/*.go`, `internal/audit/audit.go`, `test/isolation/spec.go` (B).
**Tests:** T-440 to T-443, T-449, T-140.

- `Catalogue`, `Credentials`: §5.4. `Credentials.Open` returns `gateway.Credential{ID, Pinned,
  Secret}` from `vault.Opened`.
- `FailureAuditor`: fields: token bucket (60, 1/s, the algorithm of `internal/admin/authfail.go`
  — copy it, do not import `admin`), `seen map[string]time.Time` keyed by `keyID + "\x00" +
  reason` (window 10 min, emptied at 4096 entries), mutex, `now`. `Record`: parse the tenant
  with `tenant.ParseID` (failure → false); suppression check; bucket; then
  `context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)` and `db.WithTenant` →
  `audit.RecordAuthFailure`. The `seen` entry is written only after a successful insert. A
  failed insert logs `authentication failure not recorded` at debug with the request ID.

**Verify:** `go test -race ./internal/resolver/ ./internal/audit/ ./test/isolation/`.

### C5. Gateway pipeline — `feat: request pipeline over catalogue, vault and egress`

**Files:** `internal/gateway/{handler,proxy,ports,body,writer}.go`. **Tests:** T-425 to T-439,
T-37 to T-56, T-267, T-274.

- `New` requires the new options; the old fields (`BaseURL`, `ProviderKey`, `Transport`) stay
  declared and ignored until C6 so that `internal/server` compiles.
- `ServeHTTP` follows §5.1 step by step. `reqState` gains `adapter`, `op`, `provider`,
  `target`, `credentialID`, `pinned`, `invalidReason`, `upstreamErr`, `egressReason`,
  `upstreamRequestID`; `fail(st, code)` writes through `st.adapter` (the default family's
  adapter until step 2 found one).
- Step 2: `path := r.URL.Path`; require a leading `/`, cut the first segment at the next `/`;
  an empty segment or no rest → 404. Call `Catalogue.Provider` only for a segment of 1 to 63
  characters of `[a-z0-9-]` (anything else is 404 without a lookup).
- Step 5: `errors.Is(err, os.ErrDeadlineExceeded)` (or a `net.Error` with `Timeout()`) → set
  `Connection: close`, `fail(request_timeout)`; `*http.MaxBytesError` → 413; anything else
  → the abort of 1.1.
- Step 9 lives in `rewrite`: URL from `target.BaseURL` (copy; `Path = base.Path + op.Path`,
  `RawPath`, `RawQuery`, `Fragment` empty), `Out.Host = base.Host`, header per §5.3,
  `AttachCredential(hdr, credential.Secret.Reveal())`. The proxy's `Transport` is one
  `http.RoundTripper` of the gateway that picks `Private` or `Public` from the request state.
  `req.GetBody = nil` stays.
- `modifyResponse`: a status 300 to 399 → record `upstreamStatus`, `upstreamErr = "redirect"`,
  drain nothing, close the body, and return an error value the `errorHandler` recognizes to
  write 502 `upstream_unreachable`. Otherwise as today, with `adapter.AllowResponseHeader`,
  `adapter.NewStreamReader(op, strip)` and `adapter.ReadUsage(op, …)`; record
  `X-Request-Id` per §5.9.
- `errorHandler`: the gateway does not import `egress`, so it recognizes a guard refusal with
  `errors.As` on an interface it declares itself, `interface{ BlockedReason() string;
  BlockedAddr() string }` (§5.5). It then logs `egress blocked` (§5.9) and sets `egressReason`;
  classification of `upstream_error`: `egress`, `timeout` (deadline), `redirect`, else
  `connect`.
- Authentication failure: when the error exposes `Detail()` with a tenant ID and
  `Options.Failures` is set, call `Record` and log `audited`.
- `Routes()` from the families' operations with both permissions.
- `logRequest`: §5.9, in the order: 1.1's fields, then the new ones where the table places
  them.

**Verify:** `go test -race ./internal/gateway/`.

### C6. Server and wiring — `feat: serve wires resolver and guard into the gateway`

**Files:** `internal/server/server.go`, `cmd/pandokos/serve.go`, `internal/gateway/handler.go`
(old option fields removed), `internal/gateway/transport.go` (deleted), `test/sdk/sdk_test.go`
(B). **Tests:** T-450, T-58 to T-61, T-67 to T-71.

- `server.Options` per §5.12; `server.New` no longer builds a transport and no longer reads
  `cfg.Skeleton`; shutdown calls `Egress.CloseIdleConnections()`.
- `serve.go`: `resolver.NewCatalogue(catalogCache, logger)`, `resolver.NewCredentials(opener)`,
  `resolver.NewFailureAuditor(gatewayDB, logger, nil)`; one guard for everything.
- `cfg.Skeleton` is now read by nothing but `internal/config` itself.

**Verify:** `go test -race ./internal/server/ ./cmd/... && make test-sdk`.

### C7. Configuration — `feat: upstream CA and proxy settings; the skeleton section is removed`

**Files:** `internal/config/{config,keys,validate,load}.go`, `cmd/pandokos/serve.go`,
`docs/operations/configuration.md` (the key table and the validation tables only; T-R5).
**Tests:** T-447, T-448, T-04 to T-15, T-63, T-64, T-R5.

- Remove `Skeleton`, `SkeletonOpenAI`, their keys, validation, warning and secret loading
  (`loadSecrets` reads the key-encryption key only).
- Add the two keys of §5.11 to `keyTable` (after `upstream.connect_timeout`), their
  validation, and for the purpose *serve*: read the CA file (problem messages: "cannot be
  read", "holds no certificate"; never content), build `RootCAs` from `x509.SystemCertPool()`
  (an error or nil pool → a new pool) plus the file. The environment warning scans
  `Source.Environ`.
- `serve.go`: pass `RootCAs` and `ParsedURL` to `egress.New`.

**Verify:** `go test -race ./internal/config/ ./cmd/... ./test/repo/ -run 'Config|Skeleton|Serve'`.

### C8. Everything together — `test: end-to-end, SDK and repository suites pass`

**Files:** whatever the remaining failures point at; no design change. **Tests:** T-451 to
T-455, T-65, T-66, every adapted e2e test, T-R25, T-R27.

Run `make test && make test-sdk`. A failure that needs a change of behaviour not described in
sprint.md is a question for the maintainer, not a fix. T-R27 also asks for comments and package
documentation that still say "skeleton" (`internal/gateway/doc.go`, `internal/family/family.go`
and others `git grep -n -i skeleton -- cmd internal test deploy Dockerfile` lists) to be
reworded to describe what the code is now.

**Verify:** `make check`.

---

## Part D — CI and container (commit: `test: container test on the catalogue route; fuzz target`)

**Files:** `test/container/container_test.go`, `Makefile`, `docs/operations/container.md` (the
`docker run` example only).

1. `container_test.go`: drop the two `PANDOKOS_SKELETON_…` variables and the provider key
   secret. After `init`, with the CLI inside the container (as the test already does for
   tenants and credentials): update the `default` endpoint of `openai` to the fake provider's
   address as seen from the container, `--private`; create a credential for the test tenant;
   then make the inference call. Add: a call while the endpoint is the same address with
   `--private=false` answers 502 with `X-Pandokos-Error: upstream_unreachable` (the address is
   private).
2. `Makefile`, target `fuzz`: add
   `go test ./internal/family/openai/ -run '^$$' -fuzz '^FuzzParseRequest$$' -fuzztime $(FUZZTIME)`.
3. No workflow changes: `ci.yml` already runs the root tests, the SDK module, the container
   test and the scans. Check that `repo.TestActionsPinned` and `TestCIRunsDatabaseSuite` still
   pass.

**Verify:** `make image && make test-container && make fuzz FUZZTIME=10s`.

---

## Part E — Documentation and decision records (commit: `docs: sprint 3.1 documentation and ADRs`)

### E1. New: `docs/operations/gateway.md`

Sections, in this order (T-R26 checks the items in bold):

1. **Base URLs**: `/<provider>/v1` per SDK; the list of supported operations (from
   `Adapter.Operations()`); what answers 404.
2. **What a call needs**: a virtual key in the native header; both permissions (§5.2) and what
   a key scoped to `model:invoke` alone gets; an allowed model; a credential of the tenant for
   the provider (default or pin, the rule of D-6 in one paragraph).
3. **What Pandokos changes in a request**: the header lists of §5.3 (every **forwarded
   header** and the dropped groups), the two body changes (usage option, ADR 0024; upstream
   model, with the note that the response shows the upstream identifier), what reaches the
   provider and what never does (PRV-02).
4. **Errors**: the table of §5.8 with **every code and status**, the two headers, an example
   body, how to tell a Pandokos error from a provider error; the note on SDK retries of 408 and
   409.
5. **Limits**: the table of §5.7.
6. **Outbound connections**: the two endpoint classes, the **classification table** of §5.5
   (every range), what `private` means, redirects, the `egress blocked` log line and how to
   read it.
7. **TLS and proxy**: **`upstream.tls.ca_file`**, **`upstream.proxy.url`**, that proxy
   variables of the environment are ignored (and the warning), what is and is not checked
   through a proxy, that private endpoints never use it.
8. **Authentication failures**: what is logged, what is audited, the bounds (§5.10).
9. **Upgrading from the `skeleton` configuration**: the note of §5.11.

### E2. Changed pages

- `docs/operations/configuration.md`: remove the `skeleton` rows, example and section; the
  two new keys (done in C7 for the tables), the warning, the purpose table.
- `docs/operations/container.md`: example without the provider key; the paragraph that said
  the inference listener ignores stored credentials is replaced by a pointer to the gateway
  guide.
- `docs/operations/vault.md` §8: validation per family against the provider's default
  endpoint, behind the guard, through the proxy when configured; self-hosted servers that
  accept any key; `validate` unavailable cases. §9: the note on the pin rule confirmed.
- `docs/operations/catalogue.md`: endpoints — `private` is now enforced when connecting; base
  URL + native path with examples (OpenAI, an OpenAI-compatible host with a path prefix); the
  price download uses the guard and the proxy; `import-prices --file` remains for isolated
  hosts.
- `docs/operations/tls.md`: a section "Towards providers" (verification always on, TLS 1.2,
  the CA file, restart to reload).
- `docs/reference/permissions.md`: under `model:invoke` and `credential:use`, the pair
  (§5.2).
- `docs/architecture/overview.md`: §4 the resolver and the egress guard in the component
  list; §5 steps brought in line with §5.1 (authorization before the body; limits still
  later); the layout tree (`internal/egress`, `internal/resolver`).
- `docs/architecture/security-model.md`: the two SSRF rows now describe what exists (dial-time
  check, classes, forbidden addresses, proxy caveat); a row "A body with two `model` members
  is used to pass the model check" → refused; a section **"Replay exposure"** (SEC-02): a
  virtual key is a bearer credential; it is protected in transit by TLS on the listener or in
  front of it; a captured key can be replayed until it expires or is revoked (bound: ADR
  0030); there is no nonce or request signature; scopes, the model list and limits bound what
  a replay can do; sender-constrained mechanisms are IAM-15 and IAM-16.
- `docs/architecture/data-model.md`: the role table — the gateway role executes
  `record_auth_failure` and holds no privilege on the audit tables.
- `README.md` and `docs/operations` index, if they list guides: add the gateway guide.

### E3. Decision records (status `proposed`, date of the commit)

**`docs/adr/0037-request-pipeline-and-resolution.md`**

```markdown
# 0037. Request pipeline and resolution

- Status: proposed
- Date: <date of the commit>

Refines [ADR 0009](0009-native-passthrough-by-api-family.md), [ADR 0020](0020-error-contract.md),
[ADR 0034](0034-credential-model-and-platform-oversight.md) and
[ADR 0035](0035-catalogue-and-model-access.md).

## Context

The catalogue and the vault exist; the inference route still forwarded with one key from the
configuration. Joining them needs an order of steps that bounds what an unauthenticated or
unauthorized caller costs, a place for the knowledge of each wire protocol, and answers to
points earlier sprints left open: which permissions a call needs, what happens to a key's pin,
whether the model name is rewritten, and what is done with a body the proxy cannot read.

## Decision

1. One pipeline, in this order: request ID; route (the first path segment names an enabled
   catalogue provider, whose API family has an adapter and whose allow-list has the path);
   authentication from the family's native header; authorization; body read within the size
   and time limits; parsing of model and stream flag; model check; credential; forward. The
   body is not read before authorization and no credential is opened before the model check
   passed.
2. An API family is an adapter (operations, credential header, header lists, request parser,
   usage readers, error shape). A provider uses the adapter of its catalogue family. A name
   that is unknown, disabled, reserved or of a family without adapter is the same 404.
3. Under BYOK a call needs `model:invoke` and `credential:use`; both are decided before the
   body is read. A key limited to the first is refused with `forbidden`.
4. The credential is the pin of the key when the pin is of the provider called, otherwise the
   tenant's default for that provider; nothing else is tried. No credential, or one that is
   disabled or revoked, is `no_credential`.
5. A request body must be one JSON object with exactly one top-level string member `model`;
   anything else is refused with `invalid_request`, because parsers disagree on duplicates
   and the model check must see what the provider will see. When a model's upstream
   identifier differs from its public name, the value is replaced by a byte splice; the
   response is not rewritten.
6. The request allow-list of the `openai` family is `Accept`, `Content-Type`, `User-Agent`,
   `OpenAI-Organization`, `OpenAI-Project`, `OpenAI-Beta`. A header named in `Connection` is
   dropped even when listed. `Idempotency-Key` is not forwarded and the outbound request
   cannot be replayed by the transport.
7. A redirect from a provider is not followed and not passed on: it is answered as
   `upstream_unreachable`.
8. The gateway package depends on interfaces for the catalogue, the vault and the audit
   record; a wiring package adapts the real ones.

## Consequences

A second family is a second adapter. Limits, the ledger and traces attach at fixed points of
the pipeline. An unauthenticated caller can learn which provider names are served. A client
that sends duplicate `model` members is refused. Callers see the upstream identifier in
responses when names differ.

## Alternatives considered

Authenticating before routing (needs every family's header tried in turn); checking
`credential:use` only in the vault (a key without it would learn its model access); rewriting
the model in responses (a JSON rewrite of every response and stream event); passing redirects
through; a pinned key limited to its credential's provider.
```

**`docs/adr/0038-egress-guard-upstream-tls-and-proxy.md`**

```markdown
# 0038. Egress guard, upstream TLS and proxy

- Status: proposed
- Date: <date of the commit>

Refines [ADR 0009](0009-native-passthrough-by-api-family.md),
[ADR 0019](0019-configuration.md), [ADR 0035](0035-catalogue-and-model-access.md) and
[ADR 0036](0036-prices-costs-and-importer.md).

## Context

GW-05 and GW-06 ask that requests go only to catalogue destinations, that redirects are not
followed, and that private, loopback and link-local addresses are refused unless an endpoint
is marked private, with the check made on the address actually dialled. Endpoints are written
as names, which resolve later and can be made to resolve anywhere. Three parts of the product
connect outwards: provider calls, credential validation and the price download. Enterprises
route outbound traffic through a proxy.

## Decision

1. Every outbound HTTP connection of the server is made by one package. No other package
   builds a transport or a dialer.
2. Addresses are classified as public, private (loopback, RFC 1918, carrier-grade NAT,
   link-local, unique-local, documentation and benchmarking ranges) or forbidden (unspecified,
   multicast, reserved, and the instance-metadata addresses). IPv4-mapped addresses are
   unmapped; NAT64 and 6to4 addresses take the class of the IPv4 address they embed.
3. There are two transports with separate connection pools. The one for public endpoints
   accepts `https` only and connects to public addresses only. The one for endpoints marked
   private also accepts `http` and private addresses. Forbidden addresses are refused by both.
4. The class is checked in the dialer's control hook, after name resolution, for every
   connection attempt, on the literal address about to be connected; address literals are
   also refused before dialling.
5. Redirects are never followed. Certificates are always verified, with TLS 1.2 or later;
   there is no switch to turn verification off. `upstream.tls.ca_file` adds certificate
   authorities to the system roots.
6. Proxy variables of the environment are never used. `upstream.proxy.url` sends the traffic
   of public endpoints through an HTTP proxy with `CONNECT`; private endpoints always connect
   directly. Through a proxy, the connection that is checked is the one to the proxy; target
   literals are still classified, target names are resolved by the proxy.
7. Credential validation and the price download use the same transports.

## Consequences

A name that resolves to an internal address is refused when it is dialled, whatever it
resolved to when the endpoint was written. A provider reached over private addressing must be
declared private. Behind a proxy the operator's proxy is the control point for name-based
destinations. A private endpoint cannot be on a metadata address.

## Alternatives considered

Resolving and checking before dialling, then dialling the name (the second resolution can
differ); pinning the resolved address into the connection by hand (duplicates the dialer and
breaks TLS server names and happy eyeballs); one transport with a per-request policy in the
context (a pooled connection made under the private rule could serve a public endpoint of the
same host); honouring `HTTPS_PROXY` and `NO_PROXY` (ambient settings would move credentials
without a validated key); a per-endpoint CA (a schema change nobody asked for).
```

**Amendment of `docs/adr/0020-error-contract.md`** — exact changes:

1. In the table of codes, after the row of `no_credential`, insert:

   ```
   | `invalid_request` | 400 | The request body cannot be read as the endpoint requires (for the model: one JSON object with exactly one string member `model`) |
   | `request_timeout` | 408 | The request body was not received within the configured time |
   ```

2. Change the row of `upstream_unreachable` to: `The provider could not be reached, the
   connection was refused by the egress guard, or the provider answered with a redirect`.
3. Append at the end of the file:

   ```markdown
   ## History

   - 2026-11: sprint 3.1 adds `invalid_request` and `request_timeout`, extends
     `upstream_unreachable` to guard refusals and redirects, and puts `model_not_allowed` and
     `no_credential` into use ([ADR 0037](0037-request-pipeline-and-resolution.md),
     [ADR 0038](0038-egress-guard-upstream-tls-and-proxy.md)).
   ```

**History lines** (append to the `## History` section, creating it at the end when absent):

- `0009`: `- 2026-11: sprint 3.1 routes by catalogue provider with one adapter per family,
  appends the native path (which includes /v1) to the endpoint base URL, extends the request
  allow-list of the openai family and replaces the model name by the upstream identifier when
  they differ ([ADR 0037](0037-request-pipeline-and-resolution.md)).`
- `0027`: `- 2026-11: sprint 3.1 adds the definer function record_auth_failure, executable by
  the gateway role only; the role still holds no privilege on the audit tables.`
- `0032`: `- 2026-11: sprint 3.1 records authentication failures of the inference listener
  that name a key of a tenant, in that tenant's log (detail.listener "inference"), at most
  once per key and reason in ten minutes and within 60 a minute per instance; failures that
  name no tenant are logged only.`
- `0034`: `- 2026-11: sprint 3.1 replaces point 6: validation presents the secret to the
  default enabled endpoint of the credential's catalogue provider, in the way of its API
  family (openai, anthropic), through the egress guard
  ([ADR 0038](0038-egress-guard-upstream-tls-and-proxy.md)); it shares the guard's connection
  pools. The pin rule of point 2 is confirmed with the model check in the request path.`
- `0036`: `- 2026-11: sprint 3.1 sends the download through the egress guard: it obeys
  upstream.proxy.url and upstream.tls.ca_file; "without proxy" in point 4 now means "without
  a proxy from the environment".`

`docs/adr/README.md`: add 0037 and 0038 to the index, in the style of the existing rows.

### E4. Planning files

- `planning/epics/03-gateway/epic.md`: `status: in-progress`; under 3.1, requirements line
  becomes `GW-01, GW-02, GW-05 to GW-12, GW-16 (size, duration), OPS-12, FND-01 and FND-02
  (BYOK), SEC-01, SEC-02`; under 3.2, remove `OPS-12` from the requirements line and add the
  bullet `The operations responses and embeddings of the openai family, with their usage
  readers.` (only if the maintainer confirmed O-6 and O-9; otherwise leave the epic file and
  note it in the pull request).
- `planning/roadmap.md`: no change of text; the outcome of 3.1 stays as written.
- This sprint's `sprint.md`: `status:` is set by the orchestrator, not by the implementer.

**Verify:** `go test ./test/repo/ && make lint`.

---

## Part F — Verification

Run in this order and attach the output to the pull request:

```sh
make db-up
make check                      # fmt, vet, lint, tidy, generate-check, secrets, tests, SDK suite, vuln
make image && make test-container
make fuzz FUZZTIME=30s
git diff main -- go.mod go.sum test/sdk/go.mod test/sdk/go.sum          # prints nothing
git grep -n -e InsecureSkipVerify -e ProxyFromEnvironment -- cmd internal # prints nothing
git grep -n -i skeleton -- cmd internal deploy Dockerfile                # only the two removal tests
tail -1 internal/store/migrations/migrations.sum                         # 0005_gateway_auth_failures.sql
go test -race -count=1 -run 'TestGatewayExitScenario|TestEgressAttacks|TestGatewayCanaries' ./test/e2e/ -v
```

Then check the definition of done of §10 line by line, and fill the coverage table of §9 with
the test names that ran.

## What the implementer does not do

- No retry, fallback, load balancing, response caching or response rewriting.
- No new endpoint on the allow-list beyond chat completions; no `anthropic` adapter.
- No option, variable or build tag that relaxes the guard, skips certificate verification or
  reads proxy variables; no test hook reachable from outside `internal/egress`.
- No grant or policy for the gateway role on the audit tables; no change to migrations 0001
  to 0004.
- No usage ledger, no spans, no metrics, no caller metadata headers (3.2, 6.1).
- No change to the administration API's document; no new permission, role or audit action.
- No edit of `status:` fields, of the roadmap's text, or of a closed sprint's files.

---

## Appendix A — `internal/store/migrations/sql/0005_gateway_auth_failures.sql`

```sql
-- 0005_gateway_auth_failures: the gateway records authentication failures of the inference
-- listener in the audit log of the tenant whose key was presented (sprint 3.1, section 5.10).
--
-- The gateway role gets no privilege on audit_event. It may execute one function, which writes
-- one kind of row, for the tenant of the session, with fixed columns. Outside a tenant session
-- the forced policy of audit_event refuses the row.
CREATE FUNCTION pandokos.record_auth_failure(p_key_id text, p_reason text, p_request_id text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF p_key_id !~ '^t[a-z2-7]{16}$' OR p_reason !~ '^[a-z_]{1,40}$' OR p_request_id !~ '^[0-9a-f]{32}$' THEN
        RAISE EXCEPTION 'invalid authentication failure record' USING ERRCODE = 'check_violation';
    END IF;
    INSERT INTO pandokos.audit_event (tenant_id, actor_type, key_id, action, resource_type, resource_id,
                                      outcome, request_id, detail)
    VALUES ((SELECT pandokos.current_tenant_id()), 'anonymous', p_key_id, 'auth.failure', 'virtual_key', p_key_id,
            'failure', p_request_id, pg_catalog.jsonb_build_object('reason', p_reason, 'listener', 'inference'));
END
$$;

REVOKE ALL ON FUNCTION pandokos.record_auth_failure(text, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.record_auth_failure(text, text, text) TO pandokos_gateway;
```

The key ID pattern is the check constraint of `virtual_key.key_id` in `0002_identity_audit.sql`
(verified while designing: `grep -n "key_id ~" internal/store/migrations/sql/0002_identity_audit.sql`).

## Appendix B — addition to `internal/store/queries/sql/audit.sql`

```sql

-- name: RecordAuthFailure :exec
SELECT pandokos.record_auth_failure(sqlc.arg(key_id)::text, sqlc.arg(reason)::text, sqlc.arg(request_id)::text);
```

`sqlc` v1.31.1 generates from it `RecordAuthFailure(ctx, RecordAuthFailureParams{KeyID, Reason,
RequestID}) error` (verified while designing).
