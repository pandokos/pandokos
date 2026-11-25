# Sprint 12.1 — Implementation plan

- Design: [sprint.md](sprint.md). Section numbers (§) refer to it; T-numbers are its tests, D-numbers
  its decisions, M-numbers its instruments.
- Each part below is self-contained: an agent needs sprint.md and that part only. Parts run in order
  A, B, C1 … C8, D, E, F.
- Suggested grouping: **G1** = C1, C2, C3, C4 (the transport, the client, authentication, refusal:
  one agent, in order); **G2** = C5, C6 (several providers, the check, the cache); **G3** = C7
  (configuration). After part B the three groups do not depend on each other. C8 (wiring, start,
  command line, telemetry, end to end) is one agent after them. A, B, D, E and F are one agent each.
  Because the risk class is high, C2 to C5 (key material, identifiers, the check at start) are
  reviewed by an agent that did not write them, with §5.2, §5.3, §5.7, §5.8 and §5.13 as the
  checklist.

## Rules for the implementer

1. Work on the branch the orchestrator names; do not create one. Sign off every commit
   (`git commit -s`) in the maintainer's name, with the signature the repository's configuration
   asks for; never disable it. No co-author or attribution lines, no tool footers and no session
   links in any commit message, pull request text, comment or file. One commit for A, one for B
   (tests, failing), one per step of C (message given with the step), one for D, one for E, one for
   F if something is found.
2. **No migration, no change to `api/openapi/admin.v1.yaml`, no permission, no error code, no new
   module** in `go.mod` or in a nested module. If something seems to need one, stop and ask.
3. **The key service is spoken to by one file set** (`internal/vault/transit*.go`), through the
   transport of `egress.Guard.KeyService` and nothing else. No `http.Transport`, `net.Dialer`,
   `tls.Config` or default client outside `internal/egress`; no `InsecureSkipVerify` anywhere but in
   a test of a refusal.
4. **Only the seven requests of §5.1.** The provider never calls rotate, config, trim, delete,
   export, backup, create-key or any `sys/` path; the double's administrative functions are called
   by tests only.
5. **Secrets.** The token, the role ID and the secret ID are `secret.Secret` from the file to the
   header or the body. A data key in clear is a `[]byte` that is cleared, never a `string`: decode
   the answer's `plaintext` from the raw JSON bytes, and zero the request buffer that held its
   base64 and the answer buffer. Every type that holds a secret prints the redaction marker on every
   formatting path. **An error answer's body is read to the limit and dropped**: it never reaches a
   string, an error, a log attribute or a parser. Error texts are the fixed set of C2.
6. **No retry** inside the provider except the single re-send after a `403` (§5.5). No sleep in a
   request path. Everything that waits takes the injected clock.
7. Logs, metrics and spans hold no tenant data, address, key name, namespace or header (ADR 0055):
   only the lines and instruments of §5.14 are added. No real token or key in a test, fixture or
   document; canaries are generated per test (`fixtures.CanaryLabel`).
8. An installation without `vault.kek.transit.*` must run the code it ran before: `serve` builds a
   `Router` and an `UnwrapCache` only when a transit provider is configured. Do not weaken a check, a
   trigger or an existing test to make a new one pass; the changed tests are those of §9 only.
9. Tests hold for root and non-root; nothing sleeps longer than 2 s; waiting is polling with a
   deadline. Database tests use `dbtest` and skip without `PANDOKOS_TEST_DATABASE_URL` as the others
   do. Tests with the tag `transit` skip nothing: without their environment they fail.

## Environment

- Go as in `go.mod` (`go 1.26.0`), the toolchain of ADR 0025.
- The test database: `make db-up`, then `set -a; . local/testdb/env; set +a`.
- The real services (parts D and F only): Docker; `make transit-up VENDOR=openbao` (or `vault`),
  then `make test-transit`, then `make transit-down`.
- `make check` before every push of part F.

## Part A — Scaffolding (commit: `chore: sprint 12.1 scaffolding: transit configuration keys, provider types, the key service test double`)

Everything compiles; nothing behaves yet. Functions return `errors.New("not implemented")`.

### A1. Configuration — `internal/config/{config,keys}.go`

```go
type VaultKEK struct {
	Provider string          `yaml:"provider"`
	Local    VaultKEKLocal   `yaml:"local"`
	Transit  VaultKEKTransit `yaml:"transit"`
}
type VaultKEKTransit struct {
	Address               string              `yaml:"address"`
	Namespace             string              `yaml:"namespace"`
	Mount                 string              `yaml:"mount"`
	KeyName               string              `yaml:"key_name"`
	TLS                   VaultKEKTransitTLS  `yaml:"tls"`
	Auth                  VaultKEKTransitAuth `yaml:"auth"`
	RequestTimeout        Duration            `yaml:"request_timeout"`
	MaxConcurrentRequests int                 `yaml:"max_concurrent_requests"`
	StartTimeout          Duration            `yaml:"start_timeout"`
	URL                   *url.URL            `yaml:"-"` // filled by Load
}
type VaultKEKTransitTLS struct {
	CAFile  string         `yaml:"ca_file"`
	RootCAs *x509.CertPool `yaml:"-"` // filled by Load; nil means the guard's roots
}
type VaultKEKTransitAuth struct {
	Method    string                 `yaml:"method"`
	TokenFile string                 `yaml:"token_file"`
	AppRole   VaultKEKTransitAppRole `yaml:"approle"`
}
type VaultKEKTransitAppRole struct {
	Mount        string `yaml:"mount"`
	RoleIDFile   string `yaml:"role_id_file"`
	SecretIDFile string `yaml:"secret_id_file"`
}
func (t VaultKEKTransit) Configured() bool { return t.Address != "" }
```

Defaults: `Mount: "transit"`, `Auth.AppRole.Mount: "approle"`, `RequestTimeout: 2s`,
`MaxConcurrentRequests: 32`, `StartTimeout: 0`. The thirteen keys of §5.11 are added to the key
table with `serveDef` (so `migrate` and `init` do not read them), in the order of the table, with
the descriptions of that table shortened to one sentence each; `vault.kek.provider` changes its type
text to `local, transit` and its description to "Key-encryption key provider that wraps data keys:
local (a key file) or transit (an OpenBao or HashiCorp Vault transit key)". Validation is C7; until
then `validate` keeps refusing anything but `local`.

### A2. Vault types — `internal/vault/`

`provider.go` gains:

```go
// ErrProviderUnavailable means the key provider could not be reached or refused to answer for a
// reason that may pass: the caller tries again later. It is also an ErrUnavailable.
var ErrProviderUnavailable = errors.New("vault: the key provider cannot be reached")
// ErrAADNotBound means the provider unwrapped a key under other associated data than it was
// wrapped with.
var ErrAADNotBound = errors.New("vault: the key provider does not bind associated data")
```

`check.go`: `func Probe(ctx context.Context, p KeyProvider) (version string, err error)`.

`router.go`:

```go
type Router struct{ /* active KeyProvider; byName map[string]KeyProvider */ }
// NewRouter refuses a nil provider and two providers of one name.
func NewRouter(active KeyProvider, others ...KeyProvider) (*Router, error)
func (r *Router) Name() string
func (r *Router) Wrap(ctx context.Context, plaintext, aad []byte) (string, []byte, error)
func (r *Router) Unwrap(ctx context.Context, version string, wrapped, aad []byte) ([]byte, error)
func (r *Router) Available(ctx context.Context, version string) error
// ProviderOf returns the name of the provider that owns version, or "".
func (r *Router) ProviderOf(version string) string
```

`unwrapcache.go`:

```go
// NewUnwrapCache refuses a nil provider and a lifetime outside 1 s to 15 m.
func NewUnwrapCache(p KeyProvider, ttl time.Duration, now func() time.Time) (*UnwrapCache, error)
// the four methods of KeyProvider; maxUnwrapEntries = 10000
```

`transit.go`:

```go
const (
	transitName       = "transit"
	transitWrapFormat = 0x02
	transitIDLabel    = "pandokos kek id v1"
	transitAADLabel   = "pandokos/transit-aad/v1"
	transitMaxAnswer  = 64 << 10
	transitTrip       = 5               // consecutive unavailable answers that start the refusal
	transitCooldown   = 5 * time.Second // length of the refusal
	transitWarnEvery  = 30 * time.Second
	transitDescribeMaxAge = 10 * time.Second
)

type TransitOptions struct {
	Transport      http.RoundTripper // egress.Guard.KeyService; required
	Address        *url.URL          // required
	Namespace      string
	Mount, KeyName string            // required
	AuthMethod     string            // "token" or "approle"
	TokenFile      string
	AppRoleMount   string
	RoleIDFile     string
	SecretIDFile   string
	RequestTimeout time.Duration     // 100 ms to 30 s
	MaxConcurrent  int               // 1 to 1024
	UserAgent      string
	Logger         *slog.Logger      // required
	Now            func() time.Time
	OnRequest      func(operation string, d time.Duration) // M60; may be nil
}

// NewTransitProvider checks the options and contacts nothing.
func NewTransitProvider(o TransitOptions) (*TransitProvider, error)
// Start authenticates, describes the key and identifies its latest version. It may be called
// again after a failure; it resets the refusal.
func (p *TransitProvider) Start(ctx context.Context) error
// Run keeps the token alive until ctx ends (§5.5).
func (p *TransitProvider) Run(ctx context.Context)
func (p *TransitProvider) Name() string // "transit"
func (p *TransitProvider) Wrap(ctx context.Context, plaintext, aad []byte) (string, []byte, error)
func (p *TransitProvider) Unwrap(ctx context.Context, version string, wrapped, aad []byte) ([]byte, error)
func (p *TransitProvider) Available(ctx context.Context, version string) error
func (p *TransitProvider) Info() TransitInfo
func (p *TransitProvider) Stats() TransitStats

type TransitInfo struct {
	Version        string   // the identifier Wrap returns
	Oldest, Newest int      // min_decryption_version and latest_version
	AuthMethod     string
	Weak           []string // from: exportable, plaintext_backup, deletion_allowed, auto_rotate, root_token
}
type TransitStats struct {
	Requests     map[TransitCount]int64 // by operation and outcome (§5.14)
	Up           bool
	TokenExpires bool
	TokenTTL     time.Duration
}
type TransitCount struct{ Operation, Outcome string }

// TransitError is a failure of a request to the key service. Its text is
// "vault: key service: <operation>: <class>" and nothing else.
type TransitError struct{ Operation, Class string }
```

`TransitError.Is` reports `ErrUnavailable` always and `ErrProviderUnavailable` for the classes
`unavailable` and `refused`. The redaction methods (`String`, `GoString`, `Format`, `LogValue`,
`MarshalJSON`, `MarshalText`) are written now for `TransitProvider`, `TransitOptions`, `Router` and
`UnwrapCache`, as `LocalProvider` has them.

### A3. Egress — `internal/egress/guard.go`

```go
// KeyService returns a transport for the key service of the vault: its own connection pool,
// the private class, no proxy, no redirect, TLS 1.2 or later. roots, when not nil, are the only
// authorities trusted; nil means the roots of the guard.
func (g *Guard) KeyService(roots *x509.CertPool) http.RoundTripper
```

Stub: returns the private transport (replaced in C1).

### A4. The double — `internal/vault/transittest/` (complete in this part)

Package `transittest`, used by tests only (no production file imports it; T-R88 checks). It is real
enough to be held to the conformance suite (T-1091): AES-256-GCM with `associated_data` as
additional data, a key per version, an HMAC key per version, ciphertexts `vault:v<N>:<base64(nonce ‖
ciphertext ‖ tag)>`, and the statuses and bodies of sprint.md §12.

```go
func New(tb testing.TB) *Server            // plain HTTP on 127.0.0.1, closed with the test
func NewTLS(tb testing.TB) (*Server, string) // HTTPS with test/testcert; returns the CA file
func (s *Server) URL() string
func (s *Server) Stop()                    // closes the listener and the connections; state kept
func (s *Server) Start()                   // listens again on the same address

// Administration (what an operator does with another token).
func (s *Server) Namespace(path string) *Scope // "" is the root
func (c *Scope) Mount(path string)
func (c *Scope) CreateKey(mount, name string, o KeyOptions) // Type, Derived, Exportable, PlaintextBackup, DeletionAllowed, AutoRotate
func (c *Scope) Rotate(mount, name string)
func (c *Scope) Configure(mount, name string, minDecryption, minEncryption int)
func (c *Scope) Trim(mount, name string, minAvailable int)
func (c *Scope) DeleteKey(mount, name string)  // CreateKey again makes a new key with the same name
func (c *Scope) Token(o TokenOptions) string   // TTL, MaxTTL, Renewable, Policies (Root, NoLookup), paths allowed
func (c *Scope) RevokeToken(token string)
func (c *Scope) AppRole(mount string, o RoleOptions) (roleID, secretID string) // TokenTTL, TokenMaxTTL
func (c *Scope) NewSecretID(mount, roleID string) string // the old one stops working

// Faults, each until Clear.
func (s *Server) Sealed()                  // 503 {"errors":["Vault is sealed"]} to everything
func (s *Server) Status(code int)          // that status with an error body
func (s *Server) Delay(d time.Duration)
func (s *Server) Hang()                    // accepts, never answers
func (s *Server) Drop()                    // closes the connection after reading the request
func (s *Server) Redirect(to string)       // 307
func (s *Server) IgnoreAssociatedData()    // what Vault before 1.13 does
func (s *Server) EchoInErrors(extra ...string) // every error body repeats the request's token, body and extra
func (s *Server) OversizeAnswers()         // 200 with more than 64 KiB
func (s *Server) Clear()
func (s *Server) Advance(d time.Duration)  // the double's clock for token lifetimes

// What it saw.
func (s *Server) Requests() []Request      // Method, Path, Header, Body, in order
func (s *Server) Count(operation string) int // by the operations of §5.1
func (s *Server) Plaintexts() [][]byte     // every value it encrypted or decrypted
func (s *Server) Payloads() [][]byte       // every decoded ciphertext payload it produced or received
func (s *Server) Reset()                   // forgets what it saw, keeps its state
```

Policies are a list of exact paths with the capability the request needs; the default token of
`Token` has exactly the paths of the policy file for the mount and key it is given. A request
without a token, with an unknown or expired one is `403 {"errors":["permission denied"]}`.
`internal/vault/transittest/transittest_test.go` tests the double against the facts of §12 it
claims (one sub-test per row that names a status).

### A5. Harness — `test/vaulttest`, `test/e2e/e2e_test.go`

- `vaulttest.Transit(tb, s *transittest.Server, o TransitSetup) TransitFiles`: creates the mount and
  the key, a token or an AppRole, and writes the files (mode 0600) in `tb.TempDir()`; returns their
  paths and the mount and key names.
- `vaulttest.TransitProvider(tb, …) *vault.TransitProvider`: built with a transport that reaches the
  double (the guard with the private class), started.
- e2e `opts` gains `transit *transitOpts` (address, namespace, mount, key, auth files, request
  timeout, start timeout) and `kekProvider string`; `startBinary` turns them into the environment
  variables of §5.11 and, when `kekProvider` is `transit`, sets no
  `PANDOKOS_VAULT_KEK_LOCAL_KEY_FILE`. A helper `startBinaryNoWait` returns as soon as the process
  runs, for the tests that start during an outage.

Verification of part A: `go build ./... && go vet ./... && go test ./internal/vault/transittest/ ./internal/config/ ./internal/vault/`
(the existing tests pass; `TestKeys` style tests that count the key table are updated in part B).

## Part B — Tests first (commit: `test: sprint 12.1 tests, failing`)

Every test of §9 "New tests" is written under its name and ID (a comment `// TestX is T-NNNN: …` as
the existing ones), and the changed tests are changed. They compile and fail (or, for the tag
`transit`, are not run yet). Verification: `go vet ./... && go vet -tags transit ./test/transit/`,
then `go test ./... 2>&1 | grep -E '^(FAIL|ok)'` lists as failing exactly the packages below.

| Tests | File |
|---|---|
| T-1057 to T-1063, T-1066, T-1068 | `internal/vault/transit_test.go` |
| T-1064, T-1065 | `internal/vault/transit_auth_test.go` |
| T-1067 | `internal/vault/print_test.go` (extended) |
| T-1069 | `internal/vault/router_test.go` |
| T-1070 | `internal/vault/unwrapcache_test.go` |
| T-1071 to T-1075 | `internal/vault/transit_db_test.go` |
| T-1076 | `internal/outbox/relay_transit_db_test.go` |
| T-1077 | `internal/egress/keyservice_test.go` |
| T-1078 | `internal/config/vault_test.go` (extended) |
| T-1079 | `internal/telemetry/keyprovider_test.go` |
| T-1080, T-1081 | `cmd/pandokos/kek_test.go` (extended), `cmd/pandokos/serve_vault_db_test.go` |
| T-1082 to T-1089 | `test/e2e/transit_test.go` |
| T-1090, T-1092, T-1093 | `test/transit/real_test.go` (tag `transit`), calling `conformance.go` |
| T-1091 | `test/transit/fake_test.go` |
| T-1094 | `test/transit/binary_test.go` (tag `transit`) |
| T-R85 to T-R88 | `test/repo/transit_test.go` |

Fixed vectors and helpers:

- T-1058: for the associated data `pandokos/check/v1` (17 ASCII bytes) the field is
  `o6w256dJ035gKI9gdwGFa89SdQNDk8zNg+jxJsCIDPg=` (hexadecimal
  `a3ac36e7a749d37e60288f607701856bcf5275034393cccd83e8f126c0880cf8`).
- T-1059: the expected identifier is computed by the test from the double's own HMAC key (the
  double exposes `(*Scope).HMACKey(mount, name string, version int) []byte` for this only).
- Canary scans use one helper, `transittest.Secrets(s, extra ...string) []string`, which returns
  every plaintext and payload the double handled in raw, hexadecimal, standard and URL base64 form,
  plus `extra` (the token and IDs); `testutil` already has the scanner over outputs.
- `test/transit/conformance.go` (no build tag, package `transit`): `type Target interface { Address()
  string; Vendor() string; Namespaces() bool; Admin(tb, method, path string, body any) (status int,
  data map[string]any) }`, `func NewKey(tb, t Target) (mount, key string)` (a fresh mount per test,
  `pdk-<random>`), `func PolicyToken(tb, t Target, mount, key string, ttl string) string` — which
  **loads `deploy/transit/pandokos-policy.hcl`** and replaces `transit/` and `pandokos` by the test's
  mount and key — `func AppRole(…)`, and `func RunConformance(t *testing.T, target Target)`. The
  double's `Target` maps `Admin` calls onto its `Scope` functions for the dozen administrative paths
  the suite uses.
- `test/transit/relay.go` (tag `transit`): a TCP relay `Listen(tb, upstream string) *Relay` with
  `Refuse()`, `Blackhole()`, `Delay(d)`, `Open()`.
- The real target reads `PANDOKOS_TEST_TRANSIT_ADDRESS`, `PANDOKOS_TEST_TRANSIT_VENDOR`
  (`openbao`, `vault`) and `PANDOKOS_TEST_TRANSIT_ROOT_TOKEN_FILE` and fails the test when one is
  missing.

## Part C — Implementation

### C1. The transport — `feat: a dedicated egress transport for the key service`

Files: `internal/egress/guard.go` (+ `stats` if the counters are per transport).

- A third `http.Transport` per call of `KeyService` (it is called once): the dialer and control hook
  of the private class, `Proxy: nil`, `TLSClientConfig{MinVersion: tls.VersionTLS12, RootCAs: roots
  or the guard's}`, `MaxIdleConns` and `MaxIdleConnsPerHost` 32, the other fields as the private
  transport; wrapped in `guardedTransport` so literals are pre-checked and blocked attempts are
  counted and logged as `egress blocked`. `CloseIdleConnections` closes it too.
- Redirects: the transport never follows; the client of C2 uses `CheckRedirect` returning
  `http.ErrUseLastResponse`.

Verification: `go test ./internal/egress/` (T-1077 and the existing tests).

### C2. The client and the provider — `feat: transit key provider: wrap, unwrap, version identifiers`

Files: `internal/vault/{transit,transit_client}.go`.

1. **`transit_client.go`**: `do(ctx, operation, method, path string, in, out any) error`.
   - URL: `Address` + `/v1/` + path, each segment of mount, key and auth mount escaped with
     `url.PathEscape`.
   - Headers of §5.1. The body is `json.Marshal(in)` into a buffer that is zeroed after the round
     trip when `operation` is `wrap`.
   - `ctx` gets `RequestTimeout` (the earlier deadline wins); the slot (C4) is taken inside it.
   - The answer: read `transitMaxAnswer + 1` bytes; over the limit is class `rejected`. Status `200`
     (and `204` where no body is expected): decode `{"data":{…}}` or `{"auth":{…}}` into `out`,
     unknown fields ignored; a field that is missing or of the wrong type is `rejected`. Any other
     status: the body is discarded, the class is §5.8's. The buffer is zeroed when `operation` is
     `unwrap`.
   - Transport errors: `context.DeadlineExceeded` and timeouts → `unavailable`, log class
     `timeout`; `x509`/`tls` errors → `unavailable`, `tls`; anything else → `unavailable`,
     `connection`. An `egress.BlockedError` is `rejected` (a configuration error, not an outage).
   - Returns `nil` or `*TransitError{Operation, Class}`; records the count and calls `OnRequest`.
2. **Decoding without strings**: `plaintext` and `ciphertext` are taken as `json.RawMessage`,
   unquoted in place (they are base64: no escape can appear; a backslash is `rejected`) and
   base64-decoded into a fresh `[]byte`.
3. **`Start`**: authenticate (C3) → `describe` → requirements and weak settings of §5.3 → `N =
   latest_version` → `identify(N)` → `p.version = id(N)`; store `oldest`, `newest`, the description
   time, and `verified[id(N)] = N`. Field names read from the description: `type`, `derived`,
   `exportable`, `allow_plaintext_backup`, `deletion_allowed`, `auto_rotate_period` (a number of
   seconds or a duration string: anything but `0`, `"0"`, `"0s"` or absent counts), `soft_deleted`,
   `supports_encryption`, `supports_decryption`, `latest_version`, `min_decryption_version`.
4. **`identify(n)`**: `POST hmac/<k>/sha2-256` with `{"input": base64(transitIDLabel),
   "key_version": n}`; the answer `hmac` must be `vault:v<n>:` and 32 bytes of standard base64;
   `id = "transit-" + hex(mac[:8]) + "-v" + strconv.Itoa(n)`.
5. **`resolve(ctx, version) (n int, err error)`** used by `Unwrap` and `Available`: parse with
   `^transit-([0-9a-f]{16})-v([1-9][0-9]{0,8})$`; `verified` hit → range check on a description no
   older than `transitDescribeMaxAge` (describe again otherwise; a `404` description makes every
   version unknown); miss → range check, then `identify(n)` and compare in constant time; a `400` or
   mismatch is `ErrKeyVersionUnknown`; an unavailable `identify` is returned as it is (not "unknown").
6. **`Wrap`**: `{"plaintext": base64(dk), "associated_data": aadField(aad), "key_version": N}`;
   answer `ciphertext` = `vault:v<N>:<b64>` and `key_version == N`, else `rejected`; returns
   `p.version`, `0x02 ‖ decode(b64)`. `aadField(aad) = base64(sha256(transitAADLabel ‖ 0x00 ‖ aad))`.
7. **`Unwrap`**: `len(wrapped)` in 29..1024 and `wrapped[0] == 0x02`, else `ErrUnwrap` with no
   request; `n = resolve`; `{"ciphertext": "vault:v<n>:" + base64(wrapped[1:]), "associated_data":
   …}`; `200` → the decoded plaintext (any length; the caller checks it); `400` → describe again (at
   most once per `transitDescribeMaxAge`): out of range → `ErrKeyVersionUnknown`, else `ErrUnwrap`.
8. Error texts, the complete set: `vault: key service: <operation>: <class>` for the 7 × 6
   combinations; `vault: the transit key must be of type aes256-gcm96`; `… must not be derived`;
   `… does not exist`; `… does not support encryption and decryption`; plus the sentinels.

Verification: `go test ./internal/vault/ -run 'TestTransit(WrapUnwrap|AssociatedData|VersionIdentifier|KeyRequirements|ErrorClasses|OneAttempt|Namespace)|TestTransitPrint'`
with a static token; `go test ./test/transit/ -run TestConformanceDouble`.

### C3. Authentication — `feat: transit key provider: token and AppRole authentication with renewal`

File: `internal/vault/transit_auth.go`.

- State under one mutex: the token (`secret.Secret`), `granted time.Duration`, `first
  time.Duration` (the lifetime of the first grant of this token), `obtained time.Time`, `renewable`,
  `expires bool`.
- `authenticate(ctx)`: `token` → `secret.ReadFile(TokenFile)`, `lookup` (`data.ttl` seconds,
  `data.renewable`, `data.policies`); `approle` → read both files, `login`
  (`auth.client_token`, `auth.lease_duration`, `auth.renewable`, `auth.policies`).
- `Run`: a timer at `obtained + 2/3·granted` (never less than 1 s ahead); the actions of the table
  of §5.5; failures retried after 1, 2, 4 … 60 s; the warnings rate-limited to `transitWarnEvery`.
- `reauth(ctx, refused secret.Secret) (changed bool)`: called by `do` on a `403` of a non-auth
  operation; coalesced (one in flight, callers wait within their own deadlines); at most one per
  5 s; `approle` logs in; `token` re-reads the file and adopts a different token after `lookup`.
  `do` re-sends once when `changed`, with the remaining deadline.
- `Stats`: `TokenExpires`, `TokenTTL = granted − (now − obtained)`, never negative.
- `root_token` joins `Info().Weak` when the policies contain `root`.

Verification: `go test ./internal/vault/ -run 'TestTransit(TokenAuth|AppRoleAuth)'`;
`go test -race ./internal/vault/ -run TestTransit`.

### C4. Refusal — `feat: transit key provider: deadline, concurrency bound, shared unwraps, refusal`

File: `internal/vault/transit.go` (a small `gate` type in it).

- Slots: a buffered channel of `MaxConcurrent`; acquired with the request's context; no slot before
  the deadline → `*TransitError{op, "refused"}` (counted, not sent).
- The refusal: `consecutive` counts answers of class `unavailable` of every operation; at
  `transitTrip` it sets `openUntil = now + transitCooldown`, logs `key provider unavailable`
  (`phase: run`) and M61 reads 0. While `now < openUntil` every call is `refused` at once. After it,
  exactly one call is let through (`probing`); its success resets everything and logs `key provider
  recovered` with the seconds since the refusal began; its failure sets `openUntil` again. A success
  of any request resets `consecutive`.
- Shared unwraps: a map from `sha256(version ‖ 0x00 ‖ uint32(len(wrapped)) ‖ wrapped ‖ aad)` to an
  in-flight call; followers wait for it within their own context and receive a copy; the leader's
  buffer is cleared when the last follower has copied. Errors are shared too.
- `Start` resets the gate.

Verification: `go test -race ./internal/vault/ -run 'TestTransit(Refusal|OneAttempt|Canary)'`.

### C5. Several providers and the check — `feat: several key providers at once; the start check proves associated data are bound`

Files: `internal/vault/{router,check,activate,opener,sealer,rewrap}.go`.

- `Router`: routing by `version[:strings.IndexByte(version, '-')]`; `Wrap` checks the prefix of
  what the active provider returned.
- `Probe`: the existing round trip of `Check`, then `Unwrap(version, wrapped,
  []byte("pandokos/check/v1/other"))`: `nil` error → clear and `ErrAADNotBound`; `ErrUnwrap` → pass;
  any other error → returned wrapped ("the key provider cannot prove that it binds associated data").
  `Check` calls `Probe` first.
- `Check`, the loop over needed versions: `owner := p.Name()`; when `p` implements
  `interface{ ProviderOf(string) string }`, `owner = p.ProviderOf(r.ID)`; the condition stays
  `r.Provider != owner || p.Available(ctx, r.ID) != nil` → the existing message. An `Available`
  error that is `ErrProviderUnavailable` is returned as such (wrapped), so the start waits instead
  of naming a version as missing.
- `replacedError.Error`: "the configured key-encryption key version %s was replaced by %s;
  configure the provider of %s as vault.kek.provider" (and the form without an active version); when
  both identifiers start with `local-` the old sentence about `vault.kek.local.key_file` is kept.
- The cause: `sealCause` and the opener's switch return `kek_provider_unavailable` when
  `errors.Is(err, ErrProviderUnavailable)`, before the default `kek_unavailable`; the re-wrap job
  uses `sealCause` already.

Verification: `go test ./internal/vault/ -run 'TestRouter|TestCheck|TestActivate|TestRewrap|TestOpener'`
(T-1069, T-1071 to T-1075 and the changed T-328).

### C6. The cache of the worker — `feat: the event relay opens signing secrets through a cache of unwrapped keys`

Files: `internal/vault/unwrapcache.go`; no change in `internal/outbox` beyond what `serve` passes as
`Relay.Provider` (C8).

- Key: `sha256(version ‖ 0x00 ‖ uint32(len(wrapped)) ‖ wrapped ‖ aad)`. Value: a copy of the
  plaintext and its expiry. `Unwrap` returns `bytes.Clone`. Expired entries are zeroed and removed
  when met; at `maxUnwrapEntries` every entry is zeroed and the map replaced. Concurrent misses may
  both call the provider (the transit provider shares them).

Verification: `go test -race ./internal/vault/ -run TestUnwrapCache`; `go test ./internal/outbox/ -run TestRelayUnwrapCache`.

### C7. Configuration — `feat: configuration of the transit key provider`

Files: `internal/config/{validate,lifecycle,load}.go`.

- `validate`: `vault.kek.provider` is `local` or `transit`; the rules of §5.11 in a function
  `validateTransit(c)` run for the purposes that load secrets for `serve`. Address: `url.Parse`,
  scheme `https` or (`http` and `isLoopback(host)`), `User == nil`, `Path` empty or `/`, no query, no
  fragment, a host; stored in `Transit.URL`.
- `loadSecrets`: with `provider: local` the key file is required as today; with `transit` it must be
  empty and `loadKEK` is not called; `loadUnwrapKEKs` runs for both. For a configured transit
  provider: `ca_file` is read and parsed (at least one certificate, else "must hold at least one PEM
  certificate") into `RootCAs`; each secret file of the chosen method is read with
  `secret.ReadFile` to be checked and the value dropped.
- Warnings: `keyFileWarning` covers the three secret files; an `http` address.
- The message for `provider: transit` without an address: `vault.kek.transit.address` "must be set
  when vault.kek.provider is transit".

Verification: `go test ./internal/config/`; `go run ./cmd/pandokos config check` with a sample
environment prints the warnings.

### C8. Wiring, start, command line, telemetry, end to end — `feat: serve uses the transit key provider and waits for it at start; kek check; key provider instruments and log lines`

Files: `cmd/pandokos/{serve,kek,main,metrics}.go`, `internal/telemetry/{instruments,sources,metrics}.go`,
`test/e2e/*` only where the harness needs it.

1. **Order in `serve`**: the egress guard is built before the vault (it needs nothing of it).
2. **`buildKeyProviders(cfg, guard, logger, metrics) (kp vault.KeyProvider, tp *vault.TransitProvider, local *vault.LocalProvider, err error)`**:
   - local: with `provider: local`, `NewLocalProvider(Key, UnwrapKeys...)`; with `transit` and
     unwrap files, `NewLocalProvider(UnwrapKeys[0], UnwrapKeys[1:]...)` used for unwrapping only (the
     router never calls its `Wrap`);
   - transit: when `Transit.Configured()`, `NewTransitProvider` with
     `guard.KeyService(cfg.Vault.KEK.Transit.TLS.RootCAs)`;
   - one provider → it is `kp`; two → `NewRouter(active, other)`.
3. **`startVault(ctx, …) (version, activation string, err error)`** — §5.10: loop { `tp.Start` (when
   there is one) ; `vault.Activate` with a 30 s context }; on `ErrProviderUnavailable` log, wait
   `min(1s·2^n, 15s)` with the injected clock, stop at `StartTimeout` (when not 0) or when `ctx`
   ends; anything else returns. `Activate`'s first statement (`RetireKEKVersions`) may run more than
   once: it is idempotent.
4. After it: `key provider ready` (from `tp.Info()`), one `key provider configuration weak` per
   entry of `Weak`, `vault ready` with `kek_unwrap_provider`, and `key provider no longer needed` for
   a provider configured for unwrapping only when no `active` or `retiring` row of
   `ListKEKVersions` has its name.
5. `sealer`, `opener` and `rewrapper` get `kp`; the relay gets `vault.NewUnwrapCache(kp,
   cfg.Cache.DataKeyTTL, nil)` when `tp != nil`, else `kp`. `tp.Run` joins the background group
   (the group's count goes up by one).
6. **`kek check`** in `cmd/pandokos/kek.go`: `config.Load` with the purpose of `serve`, the guard,
   `buildKeyProviders`, `tp.Start`, `vault.Probe`, the output of §5.12 (plain lines `provider:`,
   `version:`, `unwraps: v<a> to v<b>`, `weak: …`), exit codes 0 and 1; usage text updated in
   `main.go`.
7. **Telemetry**: M59 to M62 added to the catalogue (`spec(...)` entries with the new attributes
   `pandokos.kek.operation` and `pandokos.kek.outcome` and their closed lists), read at collection
   from a source `func() vault.TransitStats` (nil without a transit provider: the observable
   instruments report nothing); M60 recorded through `OnRequest`. Prometheus names:
   `pandokos_vault_provider_requests_total`, `pandokos_vault_provider_request_duration_seconds`,
   `pandokos_vault_provider_up`, `pandokos_vault_provider_token_ttl_seconds`.
8. The end-to-end tests T-1082 to T-1089 pass with the harness of A5.

Verification: `go test ./cmd/pandokos/ ./internal/telemetry/`; `go test ./test/e2e/ -run 'TestTransit|TestMoveBetweenProviders|TestKEKRotation'`;
then `make test`.

## Part D — The real services, CI and repository tests (commit: `ci: sprint 12.1 conformance against OpenBao and HashiCorp Vault; repository tests`)

### D1. `test/transit/compose.yaml`, `up.sh`, `down.sh`

```yaml
# OpenBao and HashiCorp Vault in development mode for the transit conformance suite (build tag
# transit). Started by test/transit/up.sh. Not for any other use: a development-mode service
# keeps its keys in memory.
name: pandokos-testtransit
services:
  openbao:
    profiles: [openbao]
    image: openbao/openbao:2.7.3@sha256:<digest>
    command: ["server", "-dev", "-dev-listen-address=0.0.0.0:8200"]
    environment:
      BAO_DEV_ROOT_TOKEN_ID: ${PANDOKOS_TEST_TRANSIT_ROOT_TOKEN:?run test/transit/up.sh}
    cap_add: [IPC_LOCK]
    ports: ["127.0.0.1:${PANDOKOS_TEST_TRANSIT_PORT:?run test/transit/up.sh}:8200"]
  vault:
    profiles: [vault]
    image: hashicorp/vault:2.2.2@sha256:<digest>
    command: ["server", "-dev", "-dev-listen-address=0.0.0.0:8200"]
    environment:
      VAULT_DEV_ROOT_TOKEN_ID: ${PANDOKOS_TEST_TRANSIT_ROOT_TOKEN:?run test/transit/up.sh}
    cap_add: [IPC_LOCK]
    ports: ["127.0.0.1:${PANDOKOS_TEST_TRANSIT_PORT}:8200"]
```

**The digests are not copied from this plan.** Obtain each with

```sh
docker pull openbao/openbao:2.7.3 && docker image inspect --format '{{index .RepoDigests 0}}' openbao/openbao:2.7.3
docker pull hashicorp/vault:2.2.2 && docker image inspect --format '{{index .RepoDigests 0}}' hashicorp/vault:2.2.2
```

and compare with the values recorded in sprint.md §12; a difference is reported in the pull
request, not silently accepted. If a newer patch release of either exists, keep the versions above
(the facts of §12 were observed on them) and note it.

`up.sh VENDOR`: as `test/idp/up.sh` — `local/transit/` with mode 0700, a random root token written
to `local/transit/root_token` (0600), a free port, `docker compose --profile "$VENDOR" up -d`, a
wait of at most 60 s for `GET /v1/sys/health` to answer `200`, and `local/transit/env` with
`PANDOKOS_TEST_TRANSIT_ADDRESS=http://127.0.0.1:<port>`, `PANDOKOS_TEST_TRANSIT_VENDOR` and
`PANDOKOS_TEST_TRANSIT_ROOT_TOKEN_FILE`. `down.sh`: `docker compose -p pandokos-testtransit down -v
--remove-orphans` for both profiles. The suite enables the transit and AppRole mounts and, on
OpenBao, creates its namespace itself.

### D2. `Makefile`

```make
transit-up: ## Start OpenBao or HashiCorp Vault for the key service tests (Docker; VENDOR=openbao|vault)
	sh test/transit/up.sh $(VENDOR)
transit-down: ## Stop it and discard its data
	sh test/transit/down.sh
test-transit: ## Key service conformance and exit tests against the running service (needs make db-up and make transit-up)
	@$(DBENV) set -a; . ./local/transit/env; set +a; go test -tags transit -count=1 -timeout 20m ./test/transit/...
```

`VENDOR ?= openbao`; the three names join `.PHONY`.

### D3. `.github/workflows/ci.yml`

A job after `idp`, with the same pinned actions as the other jobs:

```yaml
  transit:
    runs-on: ubuntu-24.04
    strategy:
      fail-fast: false
      matrix:
        vendor: [openbao, vault]
    name: transit (${{ matrix.vendor }})
    steps:
      - checkout (persist-credentials: false) and setup-go (.go-version), as the job idp
      - name: Start PostgreSQL and PgBouncer      # as the job idp
      - name: Start the key service
        run: |
          make transit-up VENDOR=${{ matrix.vendor }}
          echo "::add-mask::$(cat local/transit/root_token)"
      - name: Key service tests
        run: make test-transit
      - name: Key service logs
        if: failure()
        run: docker compose -p pandokos-testtransit logs --no-color --tail 200
      - name: Stop the key service
        if: always()
        run: make transit-down
      - name: Database logs                        # as the job idp
```

If the workflow has an aggregate job that lists required jobs, `transit` joins it. The container
image and the Compose files of `deploy/` do not change (no new binary dependency, no overlay: §2).

### D4. Repository tests — `test/repo/transit_test.go`

T-R85 to T-R88 as §9 says; T-R78, T-R80 and T-R84 updated. T-R86 can only pass after part E: it is
committed here and passes at the end of E.

Verification of part D: `make transit-up VENDOR=openbao && make test-transit && make transit-down`,
the same with `VENDOR=vault`; `go test ./test/repo/` (T-R86 failing until E).

## Part E — Documentation and decision records (commit: `docs: sprint 12.1 external key-encryption key: guide, failure reference, ADR 0073`)

### E1. Guides and references

- **`docs/operations/external-key.md`** (new), sections: 1. What changes (who holds what; the
  table "the key service sees / does not see" of §6). 2. Requirements of the service (tested
  versions; expected from Vault 1.13 and OpenBao 2.0; the start check is what decides). 3. Creating
  the key, the policy, and a token or an AppRole — commands for `bao` and for `vault`, the policy
  included from `deploy/transit/pandokos-policy.hcl` byte for byte, the key created with `type=aes256-gcm96`
  and none of the weak settings, short token lifetimes with AppRole, file modes. 4. The keys (table
  of §5.11) and `pandokos kek check`. 5. Moving to the key service, one instance and several (the
  table of §5.7), and when the key file may be destroyed. 6. Moving back. 7. Rotating (§5.4), with
  the warning about `auto_rotate_period`. 8. Erasure in backups: the order rotate, restart, wait,
  backup, `min_decryption_version`, `trim`; what is reversible and what is not. 9. When the service
  is slow or away (the table of §5.10, the constants of §5.8, the start-up allowance for probes, the
  alert on `pandokos_vault_provider_up` and on `pandokos_vault_provider_token_ttl_seconds`). 10.
  Backups: the service's own backup, and "a database backup needs the versions that wrapped its data
  keys". 11. Loss and compromise: the key or a version lost (lost-key procedure), the token or the
  secret ID leaked (revoke, new credentials, rotate, re-wrap, trim), the service compromised (every
  tenant enters its credentials again). 12. Limits (one key, one service at a time, no Kubernetes
  authentication yet, no namespaces in the community edition of HashiCorp Vault, a standby node is
  not followed).
- **`docs/reference/failure-behaviour.md`**: section "2a. The key service", one row per line of
  the table of §5.10 with its log line, instrument, recovery and test (T-1084's `TestTransitOutage`,
  `TestOpenerWithTransit`, `TestRelayUnwrapCache`, `TestRewrapAcrossProviders`,
  `TestTransitTokenLifetime`, `TestTransitStartRefusals`); the rows "Re-wrap" and "Event relay" of
  section 6 name the key service next to the key file; the introduction of section 2 says it covers
  the provider `local`.
- **`docs/operations/vault.md`**: §1 (where the key is, per provider), §6a (a pointer to the
  guide for rotation with the service; the sentence "An external key service (sprint 12.1) can
  automate it" becomes what D-6 decided), §7 (the worker's cache), §9 limits.
- **`docs/operations/backup-and-restore.md`**: §1 and §6 (the four cases with a key service), §7
  (what 12.3 adds). **`tenant-deletion.md`**: the erasure rule with the service. **`events.md`**:
  signing secrets and the key service. **`telemetry.md`**: the four instruments and two alerts.
  **`configuration.md`**: the thirteen keys (kept in step with `Keys()` by its test).
  **`container.md`** and **`compose.md`**: one paragraph each pointing to the guide (no overlay).
- **`docs/reference/logs.md`** (seven lines, the attributes `auth_method`, `phase`, `setting`,
  `oldest_version`, `newest_version`, `expires_in_seconds`, `outage_seconds`, `kek_unwrap_provider`,
  the value `kek_provider_unavailable`), **`metrics.md`** (M59 to M62).
- **`docs/architecture/overview.md`**: the dependency row ("A local key file by default; an OpenBao
  or HashiCorp Vault transit key optionally; cloud key services: issue #75"), §9 row "KEK provider"
  (start waits; readiness unchanged). **`security-model.md`**: boundary 4 in its network form, the
  threats "the token of the key service leaks" and "a key version is destroyed too early", the
  hierarchy figure's label, the infrastructure-operator paragraph (hosts and database without the
  token open nothing). **`data-model.md`**: `wrapped_key` formats `0x01` and `0x02`, identifier forms.
- **`docs/requirements/README.md`**: VLT-12's status note (external: transit, sprint 12.1).
- **`docs/releases/v0.3.0-draft.md`**: a section "External key-encryption key (sprint 12.1)": the
  provider, the two services, the thirteen keys, `kek check`, four instruments, seven lines, no
  migration; upgrade notes: nothing to do for installations with a key file.

### E2. Decision records

**`docs/adr/0073-the-transit-key-provider.md`** — "The transit key provider: associated data,
version identifiers, authentication, several providers and behaviour on failure". Status
`proposed`, date of the commit, indexed in `docs/adr/README.md` (the row "21. Backup, recovery,
cryptographic erasure" and the vault's rows gain 0073). Refines ADR 0008, 0033, 0068; **amends**
ADR 0033 point 5 and ADR 0038 point 7.

- *Context*: VLT-12 external; the interface keeps four methods (0068); the service is the transit
  secrets engine of OpenBao and HashiCorp Vault; what had to be established first (associated data);
  the observations of sprint.md §12 as a table with versions and the date.
- *Decision*, numbered: 1. the seven requests and the policy (D-1); 2. associated data as a digest,
  proved at start by `vault.Probe`, minimum versions as observation not as a check (D-2); 3. the
  key's requirements and warnings (D-3); 4. the identifier from the service's HMAC, and the rule for
  any future provider: derived from the key, prefixed by the provider's name (D-4); 5. the wrapped
  format `0x02`, the pinned version, rotation by restart (D-5); 6. Pandokos never rotates, trims or
  deletes; erasure in backups with the service (D-6); 7. authentication and the token's lifetime;
  secrets as files (D-7, D-9); 8. several providers through the router and what a move is (D-8); 9.
  the connection (D-10); 10. one attempt, deadline, concurrency, refusal, classes by status, error
  bodies never read (D-11, D-15); 11. caches (D-12); 12. start waits, readiness does not depend on
  the service (D-13, D-14); 13. telemetry without addresses or names (D-16); 14. `kek check` (D-17);
  15. the double, the one suite, the two services in CI (D-18).
- *Consequences*: the key service is a dependency of the data plane and of the start; a token plus
  the database unwraps while the token lives; an outage longer than the data key lifetime stops BYOK
  calls; rotation and trimming stay acts of the operator; a move back needs a new key file; §11.
- *Alternatives considered*: a vendor client module (dependencies for seven requests); sending the
  associated data in clear (tenant identifiers in the key service); an inner envelope (a key on the
  host or a format inside a format); trusting a version number instead of probing; the transit
  version number, a creation time, or the address and key name as identifier (each can name the
  wrong key); a check value stored with the version (a migration for what the HMAC gives without
  one); following the service's latest version while running (refused by the database's trigger for
  the other instances); automatic rotation and trimming (a token able to destroy keys); stale data
  keys during an outage (the lifetime is the guarantee); readiness tied to the service (withdraws
  every instance); retries in the provider; exit instead of waiting at start.

History lines (one each, dated, naming 0073): **0008** (the external provider is built for the
transit API; the consequence "softened by the DEK cache" is specified by 0073 points 10 to 12);
**0033** (point 5 amended: an external provider's identifier is computed by the service from the
key; wrapped format `0x02`); **0038** (point 7 amended: the key service is a fourth consumer, on a
dedicated transport); **0068** (the consequence about automation: possible, not done by the product;
"its file destroyed" reads "made undecryptable and trimmed" for a key service); **0019** (thirteen
keys; the secrets of the key service are files); **0062** (M59 to M62); **0063** (seven lines);
**0071** (the consequence about the data key cache is delivered by `vault.UnwrapCache`).

### E3. Planning files and traceability

`planning/epics/12-operability/epic.md`: under 12.1, a "*Deferred from 12.1*" note listing the rows
of §2 that name a later sprint (12.2: parser fuzz target; 12.3: restore drill with a key service;
8.2: Kubernetes authentication) — the orchestrator sets statuses in the epic, the roadmap and the
front matter. The policy file `deploy/transit/pandokos-policy.hcl` is created in this part with the
content of §5.1.

Verification of part E: `go test ./test/repo/` (all of it, T-R86 included); every relative link of
the new and changed documents resolves (`repo.TestDocs…` tests).

## Part F — Verification (no commit unless something is found)

1. `make check`. Then both legs: `make transit-up VENDOR=openbao && make test-transit && make
   transit-down`, and the same with `VENDOR=vault`. Record the versions the two services report
   (`GET /v1/sys/health`) and the image digests in the pull request.
2. **The first design question, once more, by hand** against both running services: encrypt with
   `associated_data`, decrypt with another value and with none, and see `400`; then start the binary
   against the double in the mode `IgnoreAssociatedData` and against `hashicorp/vault:1.12.11` (pulled
   for this check only, not added to the repository) and see `startup failed` with `ErrAADNotBound`'s
   text. Record the outputs.
3. **The exit criteria by hand**, once, against OpenBao with AppRole: (a) a fresh installation with
   no key file anywhere in the configuration or on the host; (b) an installation of the previous
   commit with a key file and data, moved to the service and back, with a client calling a model in
   a loop the whole time and not one failure; (c) pause the service with `docker pause` (a development-mode service must not be stopped: it
   would lose its keys), restart Pandokos, see it listen on nothing and not exit, `docker unpause`,
   and see it start by itself.
4. Read every log line of a run of T-1086 and T-1094 at `debug`, and the requests the double
   recorded, for a token, an ID, a key, a tenant, an address with a path or a namespace.
5. `go list -m all` before and after the sprint differ by nothing; `git diff --stat` touches no
   migration and not `api/openapi/`.
6. Review by an agent that did not write C2 to C5, with the checklist: the provider sends only the
   seven requests; no error or log can carry an answer's body; a data key in clear is never a string
   and is cleared on every path, the error paths included; an identifier that does not verify never
   leads to a decrypt request; `Probe` cannot pass when the negative unwrap did not run; a provider
   that is unavailable is never reported as "version missing"; no path uses a cached key past its
   lifetime; a single-provider installation builds neither the router nor the cache.

## What the implementer does not do

- No rotation, configuration, trim, deletion or creation of a key by the product, and no
  administrative path of the service in non-test code.
- No Kubernetes, certificate, JWT or cloud authentication; no cloud key service; no second transit
  block; no Compose overlay; no platform webhook or platform data key.
- No stale use of a data key, no readiness check on the key service, no retry in the provider, no
  following of a rotation without a restart (O-1, O-4, O-6): if the maintainer decides otherwise on
  an open point, the design is amended first.
- No change to migrations, to the OpenAPI document, to `kek_activate` or to the states of key
  versions; no weakening of the trigger that accepts data keys under the active version only.
- No vendor module, no copied vendor code, and no digest, version or checksum that was not obtained
  by the command the plan gives.
