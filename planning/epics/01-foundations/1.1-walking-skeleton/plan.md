# Sprint 1.1 — Implementation plan

- Design: [sprint.md](sprint.md) (frozen). Section numbers below ("§5.4") refer to it; test IDs
  ("T-26") refer to its section 9.
- This plan is followed literally and in order. If a step cannot be done as written, or the
  design and the plan disagree, stop and raise a question; do not improvise
  ([planning/README.md](../../../README.md)).

## Rules for the implementer

1. Work on a branch `feat/1.1-walking-skeleton` created from `main`. Sign off every commit
   (`git commit -s`).
2. Commits follow the parts of this plan: one commit for part A, one for part B (tests, failing),
   one per step of part C, one for part D, one for part E. Do not squash locally; the pull
   request is squashed at merge.
3. Module path: `github.com/pandokos/pandokos`. `go.mod` says `go 1.24.0` and has **no**
   `toolchain` line. The only direct dependency of the product module is
   `go.yaml.in/yaml/v3 v3.0.6`. Adding any other module is a design change: stop and ask.
4. Never write a real key, prompt or completion anywhere. The only secret-like and content-like
   strings in the repository are the four canary constants of `test/fixtures/canary.go`, and
   they are referenced by name everywhere else.
5. Never pass a header map, a request or response body, a raw URL, or a `secret.Secret`'s
   revealed value to a logger or into an error message.
6. Everything is written in English. Every exported identifier has a doc comment (the linter
   checks it).
7. All commands are run from the repository root unless a step says otherwise.
8. The pinned versions, commit SHAs and image digests in this plan were resolved on 2026-11-11.
   Before using each one, verify it with the command given next to it. If a value differs,
   stop and ask; do not substitute another.

## Part A — Scaffolding (commit: `chore: Go module, lint configuration, Makefile`)

### A1. `go.mod`

```
module github.com/pandokos/pandokos

go 1.24.0
```

Create it with `go mod init github.com/pandokos/pandokos`, then edit the `go` line to exactly
`go 1.24.0` and delete any `toolchain` line. The requirement on `go.yaml.in/yaml/v3 v3.0.6` is
added in step C3 with `go get go.yaml.in/yaml/v3@v3.0.6`, which also creates `go.sum`.

### A2. `.go-version`

One line, no trailing spaces: `1.27.4`. This is the release toolchain (§8 D-1, ADR 0025).

### A3. `.gitignore`

Replace the whole file (it is a Python template) with:

```
# Build output
/bin/
/dist/
*.test
*.out
coverage.*
sbom.*.json

# Go workspaces are local
go.work
go.work.sum

# Local configuration and secrets
.env
.envrc
/local/

# Editors and operating systems
.idea/
.vscode/
*.swp
.DS_Store
```

### A4. `.dockerignore`

```
*
!go.mod
!go.sum
!cmd/
!internal/
```

### A5. `.golangci.yml`

This configuration was validated with `golangci-lint config verify` (v2.5.0).

```yaml
version: "2"
run:
  timeout: 5m
  tests: true
linters:
  default: standard
  enable:
    - bodyclose
    - depguard
    - errorlint
    - forbidigo
    - gosec
    - misspell
    - nilerr
    - noctx
    - revive
    - unconvert
    - unparam
  settings:
    depguard:
      rules:
        all:
          list-mode: strict
          files:
            - $all
          allow:
            - $gostd
            - github.com/pandokos/pandokos
            - go.yaml.in/yaml/v3
        product-does-not-import-tests:
          list-mode: lax
          files:
            - "**/cmd/**"
            - "**/internal/**"
            - "!$test"
          deny:
            - pkg: github.com/pandokos/pandokos/test
              desc: product code must not import test helpers
        family-is-a-leaf:
          list-mode: lax
          files:
            - "**/internal/family/**"
          deny:
            - pkg: github.com/pandokos/pandokos/internal/gateway
              desc: families do not know the gateway
            - pkg: github.com/pandokos/pandokos/internal/server
              desc: families do not know the server
            - pkg: github.com/pandokos/pandokos/internal/config
              desc: families receive plain values
        gateway-takes-plain-values:
          list-mode: lax
          files:
            - "**/internal/gateway/**"
            - "!$test"
          deny:
            - pkg: github.com/pandokos/pandokos/internal/server
              desc: the gateway does not know the server
            - pkg: github.com/pandokos/pandokos/internal/config
              desc: the gateway receives plain values through Options
    forbidigo:
      forbid:
        - pattern: '^(fmt\.Print(|f|ln)|print|println)$'
          msg: write to the injected io.Writer or use the slog logger
        - pattern: '^log\.(Print|Fatal|Panic)(|f|ln)$'
          msg: use log/slog
        - pattern: '^httputil\.Dump(Request|RequestOut|Response)$'
          msg: dumps contain credentials and content
    revive:
      rules:
        - name: exported
        - name: package-comments
        - name: error-return
        - name: error-strings
        - name: context-as-argument
        - name: unused-parameter
  exclusions:
    presets:
      - std-error-handling
    rules:
      - path: _test\.go
        linters:
          - bodyclose
          - gosec
          - noctx
          - unparam
      - path: ^test/
        linters:
          - gosec
          - noctx
formatters:
  enable:
    - gofmt
    - goimports
  settings:
    goimports:
      local-prefixes:
        - github.com/pandokos/pandokos
```

If `golangci-lint config verify` rejects the file after you typed it, you made a typing error:
compare again. Do not remove linters to make the run pass; fix the code. A `//nolint` comment
needs a reason on the same line and is listed in the pull request description.

### A6. `Makefile`

Recipes are indented with a tab.

```make
SHELL := /bin/sh
RELEASE_GO := $(shell cat .go-version)
VERSION ?= dev
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
IMAGE ?= pandokos:dev
PKG := github.com/pandokos/pandokos
LDFLAGS := -s -w -buildid= -X $(PKG)/internal/buildinfo.Version=$(VERSION) -X $(PKG)/internal/buildinfo.Commit=$(COMMIT)
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.9.0
FUZZTIME ?= 30s

.PHONY: help build test test-sdk test-container fuzz bench fmt fmt-check vet lint tidy vuln image check clean

help: ## Show the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | sed 's/:.*## /\t/'

build: ## Build bin/pandokos
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/pandokos ./cmd/pandokos

test: ## Unit, integration, end-to-end and repository tests, with the race detector
	go test -race -count=1 ./...

test-sdk: ## Official SDK suite (nested module, release toolchain)
	cd test/sdk && GOTOOLCHAIN=go$(RELEASE_GO) go test -race -count=1 ./...

test-container: ## Tests against a built image: make image first
	PANDOKOS_TEST_IMAGE=$(IMAGE) go test -tags container -count=1 ./test/container/

fuzz: ## Run each fuzz target for FUZZTIME
	go test ./internal/family/openai/ -run '^$$' -fuzz '^FuzzPrepareRequest$$' -fuzztime $(FUZZTIME)
	go test ./internal/family/openai/ -run '^$$' -fuzz '^FuzzReadUsage$$' -fuzztime $(FUZZTIME)
	go test ./internal/family/openai/ -run '^$$' -fuzz '^FuzzStreamReader$$' -fuzztime $(FUZZTIME)

bench: ## Usage reader benchmark
	go test ./internal/family/openai/ -run '^$$' -bench . -benchmem

fmt: ## Format the code
	gofmt -w cmd internal test

fmt-check: ## Fail if a file is not formatted
	@test -z "$$(gofmt -l cmd internal test)" || { gofmt -l cmd internal test; exit 1; }

vet: ## go vet
	go vet ./...

lint: ## golangci-lint
	golangci-lint run

tidy: ## Check that go.mod and go.sum are tidy
	go mod tidy -diff

vuln: ## govulncheck with the release toolchain
	GOTOOLCHAIN=go$(RELEASE_GO) go run $(GOVULNCHECK) -test ./...
	cd test/sdk && GOTOOLCHAIN=go$(RELEASE_GO) go run $(GOVULNCHECK) -test ./...

image: ## Build the container image for the local platform
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t $(IMAGE) .

check: fmt-check vet lint tidy test test-sdk vuln ## Everything a pull request must pass, except the image

clean: ## Remove build output
	rm -rf bin dist
```

Verify `govulncheck`'s version exists: `go list -m golang.org/x/vuln@v1.9.0`.

**Checkpoint A.** `golangci-lint config verify` succeeds. `make help` lists the targets.

## Part B — Tests first (commit: `test: sprint 1.1 tests, failing`)

Write every file of this part before any file of part C. At the end of part B, run
`go vet ./... ; go test ./...` and save the output: every package that has tests must **fail**
(mostly with build errors, because the packages under test do not exist). Paste that output in
the pull request description. This is the tests-first record required by the definition of done.

General rules for tests:

- Package names: tests of `internal/...` are in the same package (white-box) unless stated;
  tests under `test/` are their own packages.
- Each test listed in sprint.md §9 is a top-level `Test…`, `Fuzz…` or `Benchmark…` function with
  exactly the name given there (sub-tests with `t.Run` inside it are free).
- No `time.Sleep` to wait for a condition: poll with a deadline (helper `eventually`, B3) or
  block on a channel with a timeout.
- Every HTTP client used in a test has a timeout or a context with a deadline (5 s unless
  stated). Response bodies are closed.
- Logs are captured by giving `telemetry.NewLogger` a concurrency-safe buffer (helper `LogBuffer`,
  B3) and decoded line by line as JSON.

### B1. `test/fixtures/` — package `fixtures`

Files:

- `test/fixtures/canary.go`

  ```go
  // Package fixtures holds recorded-shaped provider responses and the canary values that tests
  // plant and then search for in every output. Everything here is synthetic.
  package fixtures

  const (
      CanaryProviderKey = "pandokos-canary-provider-key-0d6f3a1b9c2e47aa"
      CanaryAccessToken = "pandokos-canary-access-token-4e8b2d7c61f0a935"
      CanaryPrompt      = "pandokos-canary-prompt-91c7e2"
      CanaryCompletion  = "pandokos-canary-completion-5f2a9c"
  )

  // Canaries returns the four values, for scanning outputs.
  func Canaries() []string
  ```

- `test/fixtures/fixtures.go`: `//go:embed openai/*` into an `embed.FS`;
  `func Read(name string) []byte` returns `openai/<name>` and panics if it does not exist.

- `test/fixtures/openai/chat_completion.json` (exactly this, two-space indentation, final
  newline):

  ```json
  {
    "id": "chatcmpl-fixture0001",
    "object": "chat.completion",
    "created": 1700000000,
    "model": "fake-basic",
    "choices": [
      {
        "index": 0,
        "message": {
          "role": "assistant",
          "content": "pandokos-canary-completion-5f2a9c",
          "refusal": null
        },
        "logprobs": null,
        "finish_reason": "stop"
      }
    ],
    "usage": {
      "prompt_tokens": 9,
      "completion_tokens": 12,
      "total_tokens": 21
    },
    "system_fingerprint": "fp_fixture"
  }
  ```

  Expected usage: input 9, output 12, all else 0, `Extra` nil.

- `test/fixtures/openai/chat_completion_details.json`: the same document with
  `"id": "chatcmpl-fixture0003"`, `"model": "fake-details"` and this `usage`:

  ```json
  "usage": {
    "prompt_tokens": 11,
    "completion_tokens": 7,
    "total_tokens": 18,
    "prompt_tokens_details": {"cached_tokens": 3, "audio_tokens": 4},
    "completion_tokens_details": {
      "reasoning_tokens": 2,
      "audio_tokens": 0,
      "accepted_prediction_tokens": 1,
      "rejected_prediction_tokens": 0
    }
  }
  ```

  Expected usage: input 11, output 7, cache read 3, cache write 0, reasoning 2,
  `Extra = {"input_audio_tokens": 4, "accepted_prediction_tokens": 1}`.

- `test/fixtures/openai/chat_stream_usage.sse` — what the provider sends when usage is
  requested. Six events; each is one `data:` line followed by one empty line; line ending `\n`;
  the file ends with the empty line after `data: [DONE]`. `P` below stands for the common prefix
  `"id":"chatcmpl-fixture0002","object":"chat.completion.chunk","created":1700000000,"model":"fake-basic","system_fingerprint":"fp_fixture"`
  and must be written out in full on each line:

  ```
  data: {P,"choices":[{"index":0,"delta":{"role":"assistant","content":"","refusal":null},"logprobs":null,"finish_reason":null}],"usage":null}

  data: {P,"choices":[{"index":0,"delta":{"content":"pandokos-canary-completion-5f2a9c"},"logprobs":null,"finish_reason":null}],"usage":null}

  data: {P,"choices":[{"index":0,"delta":{"content":" end"},"logprobs":null,"finish_reason":null}],"usage":null}

  data: {P,"choices":[{"index":0,"delta":{},"logprobs":null,"finish_reason":"stop"}],"usage":null}

  data: {P,"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":12,"total_tokens":21}}

  data: [DONE]

  ```

- `test/fixtures/openai/chat_stream.sse` — what the provider sends when usage is not requested:
  the same file without the fifth event and without the `,"usage":null` member in the first
  four.

- `test/fixtures/openai/chat_stream_usage_on_last_chunk.sse` — an OpenAI-compatible server
  variant: events 1 to 3 of `chat_stream.sse`, then
  `data: {P,"choices":[{"index":0,"delta":{},"logprobs":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":12,"total_tokens":21}}`,
  then `data: [DONE]`.

- `test/fixtures/openai/error_401.json`:
  `{"error":{"message":"Incorrect API key provided.","type":"invalid_request_error","param":null,"code":"invalid_api_key"}}`
- `test/fixtures/openai/error_429.json`:
  `{"error":{"message":"Rate limit reached.","type":"requests","param":null,"code":"rate_limit_exceeded"}}`

- `test/fixtures/README.md`: states that the fixtures are synthetic, shaped after the provider's
  documented wire format, contain no real prompt, completion or key, and are to be replaced by
  scrubbed recordings when a key is available (sprint.md R-4).

- `test/fixtures/fixtures_test.go`: `TestFixturesAreWellFormed` — the three JSON files are valid
  JSON; every `data:` payload of the `.sse` files other than `[DONE]` is valid JSON; the `.sse`
  files end with `data: [DONE]\n\n`; the four canaries are distinct and the access token is at
  least 32 bytes.

### B2. `test/fakeprovider/` — package `fakeprovider`

One file `fakeprovider.go` (non-test file, so that other packages and the nested module can
import it) and `fakeprovider_test.go`.

```go
// Package fakeprovider is a fake OpenAI-compatible upstream for hermetic tests.
package fakeprovider

const (
    ModelBasic            = "fake-basic"
    ModelDetails          = "fake-details"
    ModelGatedStream      = "fake-gated-stream"
    ModelHang             = "fake-hang"
    ModelError429         = "fake-error-429"
    ModelError500Echo     = "fake-error-500-echo"
    ModelUsageOnLastChunk = "fake-usage-on-last-chunk"
)

// Request is a request the fake received.
type Request struct {
    Method, Path, RawQuery, Proto string
    Header                        http.Header
    Body                          []byte
}

type Server struct{ /* *httptest.Server, mutex, requests, gate, cancelled chan, in-flight counter */ }

func New(tb testing.TB) *Server    // plain HTTP/1.1 on 127.0.0.1; closed through tb.Cleanup
func NewTLS(tb testing.TB) *Server // TLS with HTTP/2 enabled (httptest EnableHTTP2 + StartTLS)
func (s *Server) URL() string      // scheme://127.0.0.1:port
func (s *Server) BaseURL() string  // URL() + "/v1"
func (s *Server) Transport() http.RoundTripper // a transport that trusts the server (httptest Client().Transport)
func (s *Server) Requests() []Request          // copy, in arrival order
func (s *Server) Release()                     // lets gated and hanging handlers continue; idempotent
func (s *Server) WaitCancelled(d time.Duration) bool // true if a handler saw its context cancelled within d
func (s *Server) InFlight() int                // handlers currently running
```

Behaviour of the handler, in this order:

1. Read the whole body; record the `Request`.
2. If the method and path are not `POST /v1/chat/completions` → 404 with body
   `{"error":{"message":"not found","type":"invalid_request_error","param":null,"code":null}}`.
3. If `Authorization` is not exactly `Bearer ` + `fixtures.CanaryProviderKey` → 401 with
   `error_401.json`.
4. Set on every further response: `Set-Cookie: fake=1`, `Server: fake`,
   `X-Request-Id: req_fake_0001`, `Openai-Processing-Ms: 1`,
   `X-Ratelimit-Remaining-Requests: 99`, `X-Pandokos-Error: spoofed`.
5. Decode the body into
   `struct{ Model string; Stream bool; StreamOptions *struct{ IncludeUsage bool `json:"include_usage"` } `json:"stream_options"` }`.
   Not JSON → 400 with an OpenAI-shaped error.
6. By model:
   - `fake-basic`: not streaming → 200, `Content-Type: application/json`,
     `chat_completion.json`. Streaming → 200, `Content-Type: text/event-stream`,
     the events of `chat_stream_usage.sse` if `include_usage` is true, else of
     `chat_stream.sse`; **each event is written and flushed separately**.
   - `fake-details`: 200 JSON `chat_completion_details.json`.
   - `fake-gated-stream`: as streaming `fake-basic`, but after writing and flushing the first
     event, wait for `Release()` or for the request context to be done. If the context is done,
     signal "cancelled" and return without writing more.
   - `fake-hang`: wait for `Release()` or the context before writing anything. Context done →
     signal "cancelled" and return. Released → answer as `fake-basic`.
   - `fake-error-429`: 429, `Retry-After: 7`, `Content-Type: application/json`, `error_429.json`.
   - `fake-error-500-echo`: 500, JSON body
     `{"error":{"message":"upstream failure echo: <CanaryPrompt> <CanaryCompletion>","type":"server_error","param":null,"code":null}}`
     with the two canary values substituted.
   - `fake-usage-on-last-chunk`: stream of `chat_stream_usage_on_last_chunk.sse`, whatever the
     options.
   - any other model → 404 with code `model_not_found` in the OpenAI shape.

"Split into events" means: split the fixture at each `\n\n`, keep the separator with the event.

`fakeprovider_test.go`: `TestFakeProvider` — one sub-test per model checking status and
content type; wrong key gives 401; a cancelled request to `fake-hang` makes `WaitCancelled`
true; streaming `fake-basic` with and without `include_usage` returns the two fixtures byte for
byte.

### B3. `test/testutil/` — package `testutil`

`testutil.go`, shared by tests of every level (root module only):

```go
// LogBuffer is a concurrency-safe io.Writer that collects log output.
type LogBuffer struct{ /* mutex, bytes.Buffer */ }
func (b *LogBuffer) Write(p []byte) (int, error)
func (b *LogBuffer) String() string
// Lines decodes every line as a JSON object; it fails the test on a line that is not one.
func (b *LogBuffer) Lines(tb testing.TB) []map[string]any
// Find returns the lines whose "msg" equals msg.
func (b *LogBuffer) Find(tb testing.TB, msg string) []map[string]any

// Eventually polls cond every 10 ms until it returns true or d elapses; it fails the test on timeout.
func Eventually(tb testing.TB, d time.Duration, what string, cond func() bool)

// WriteSecret writes value plus a newline to a new file in tb.TempDir() and returns its path.
func WriteSecret(tb testing.TB, name, value string) string

// ReadEvent reads one server-sent event (up to and including the empty line) from r.
func ReadEvent(r *bufio.Reader) (string, error)

// AssertNoCanary fails the test if text contains any of fixtures.Canaries(); it reports which
// canary and in which named output, never the surrounding text.
func AssertNoCanary(tb testing.TB, where, text string)
```

`test/testutil` is added to the `depguard` picture implicitly: it is under `test/`, so product
code cannot import it.

### B4. `internal/secret/secret_test.go`

T-01, T-02, T-03 as specified in sprint.md §5.3 and §9. For T-01 use a table of
`fmt.Sprintf` verbs applied to a `Secret`, to a `*Secret`, and to a struct
`struct{ Name string; S secret.Secret }`; assert the secret's value is absent and `[REDACTED]`
present. Include `json.Marshal`, `slog.NewTextHandler` and `slog.NewJSONHandler` output of
`slog.Any("k", s)`.

### B5. `internal/config/config_test.go`

T-04 to T-16. Helpers in the test file: `validSource(t)` returns a `Source` with secret files
created by `testutil.WriteSecret` (access token `fixtures.CanaryAccessToken`, provider key
`fixtures.CanaryProviderKey`) and `Environ` setting only the two `*_FILE` variables.

Details that are easy to get wrong:

- T-05 walks `reflect.TypeOf(Config{})`, builds the dotted name of every leaf field from the
  `yaml` tags (skipping `yaml:"-"`), and compares the set with `Keys()`.
- T-07 is a loop over `Keys()`: for each key, a file that sets it to value A and an environment
  that sets it to value B (a table in the test gives two valid values per key); assert B.
- T-09: the three inputs are `nope: 1`, a file containing
  `skeleton:\n  openai:\n    api_key: <CanaryProviderKey>`, and `log:\n  level: info\n---\nlog:\n  level: debug\n`.
- T-10: input `skeleton: <CanaryProviderKey>` (a string where a mapping is expected) and
  `listeners:\n  inference:\n    max_header_bytes: <CanaryProviderKey>`.
- T-12 rows (key → input → expected problem key): `log.level`=`verbose`;
  `listeners.inference.address`=`nohost`, `:99999`, `host:port:extra`; each duration key=`0s`;
  `listeners.inference.max_header_bytes`=`100`, `2000000`; `limits.max_request_body_bytes`=`0`,
  `104857601`; `skeleton.openai.base_url`=`ftp://x/v1`, `https://user:pw@x/v1`, `https://x/v1?a=b`,
  `https://x/v1#f`, `/v1`, `https:///v1`. Also assert that `https://x/v1/` is accepted and
  `ParsedBaseURL.Path` is `/v1`.

### B6. `internal/telemetry/log_test.go`

T-17, T-18. T-18 table of keys that must be redacted: `authorization`, `Authorization`,
`proxy-authorization`, `cookie`, `Set-Cookie`, `x-api-key`, `api-key`, `apikey`, `password`,
`passwd`, `secret`, `token`, `credential`, `client_secret`, `db_password`, `access_token`,
`provider_key`, `api_key`; each also inside `slog.Group("g", …)`. Keys that must not be:
`input_tokens`, `output_tokens`, `cache_read_tokens`, `request_id`, `status`, `key_id`,
`tokens`.

### B7. `internal/health/health_test.go`

T-19, T-20, T-21. Use `httptest.NewRecorder` against `State.Handler()`. For the blocking check
in T-21 the check waits on its context; assert the handler returns 503 in under 3 s.

### B8. `internal/family/family_test.go`

T-22.

### B9. `internal/family/openai/*_test.go`

- `route_test.go`: T-23, T-24, T-25.
- `request_test.go`: T-26, T-27. T-26 table (input → output, stream, strip); `\n` and spaces are
  significant:

  | Input | Output | stream | strip |
  |---|---|---|---|
  | `{"model":"m","messages":[]}` | unchanged | false | false |
  | `{"model":"m","stream":false}` | unchanged | false | false |
  | `{"model":"m", "stream": true }` | `{"model":"m", "stream": true ,"stream_options":{"include_usage":true}}` | true | true |
  | `{"stream":true,"stream_options":{"include_usage":true},"x":1}` | unchanged | true | false |
  | `{"stream":true,"stream_options":{"include_usage": false},"x":1}` | `{"stream":true,"stream_options":{"include_usage": true},"x":1}` | true | true |
  | `{"stream":true,"stream_options": {"include_obfuscation":false},"x":1}` | `{"stream":true,"stream_options": {"include_usage":true,"include_obfuscation":false},"x":1}` | true | true |
  | `{"stream":true,"stream_options":{ }}` | `{"stream":true,"stream_options":{"include_usage":true }}` | true | true |
  | `{"stream":true,"stream_options":null,"x":"é<>"}` | `{"stream":true,"stream_options":{"include_usage":true},"x":"é<>"}` | true | true |
  | `{"stream":true,"stream_options":7}` | unchanged | true | false |
  | `{"stream":true,"stream":false}` | unchanged | false | false |
  | `{"stream":true,"stream_options":{"include_usage":true},"stream_options":{"a":1}}` | `{"stream":true,"stream_options":{"include_usage":true},"stream_options":{"include_usage":true,"a":1}}` | true | true |
  | `{"stream":"yes"}` | unchanged | false | false |
  | `[1,2]` | unchanged | false | false |
  | `{"stream":true` | unchanged | false | false |
  | `{"stream":true} x` | unchanged | false | false |
  | `{\n "stream": true,\n "n": 1.0e3\n}\n` | `{\n "stream": true,\n "n": 1.0e3\n,"stream_options":{"include_usage":true}}\n` | true | true |
  | empty body | unchanged | false | false |

  "unchanged" also means the returned slice is the input slice (`&out[0] == &in[0]` when
  non-empty).

  T-27 seeds: every input of the table.
- `usage_test.go`: T-28, T-29.
- `stream_test.go`: T-30 to T-35. T-31's expected output is computed in the test by removing
  from `chat_stream_usage.sse` the bytes of its fifth event. T-34 seeds: the three `.sse`
  fixtures; the fuzz function takes `(data []byte, chunk uint8)`, uses chunk size
  `int(chunk)%64 + 1`, and skips inputs of 1 MiB or more.
- `errors_test.go`: T-36.

### B10. `internal/gateway/gateway_test.go`

T-37 to T-57. A helper builds the system under test:

```go
type harness struct {
    up   *fakeprovider.Server
    srv  *httptest.Server   // serves the gateway.Handler
    logs *testutil.LogBuffer
}
func newHarness(t *testing.T, mutate func(*Options)) *harness
```

Defaults: `fakeprovider.New`, `BaseURL` = its base URL, `ProviderKey` and `AccessToken` the
canaries, `MaxRequestBodyBytes` 1 MiB, `RequestBodyReadTimeout` 2 s, `MaxRequestDuration` 10 s,
`Transport` = `NewTransport(2 * time.Second)`, logger at debug level into `logs`. For the
`http2` sub-test of T-41 use `fakeprovider.NewTLS` and `Transport: up.Transport()`.

A helper `chat(model string, stream bool) []byte` returns
`{"model":"<model>","messages":[{"role":"user","content":"<CanaryPrompt>"}],"stream":true}` (the
`stream` member is omitted when false).

Notes per test where the method matters:

- T-38: read the first event from the caller side with `testutil.ReadEvent`; only then call
  `up.Release()`; then read the rest. Before `Release`, assert `up.InFlight() == 1` (the
  provider is still inside its handler, so the response was not buffered).
- T-41/T-42: cancel the caller's request context; assert `up.WaitCancelled(2 * time.Second)`;
  then `Eventually` find a `request` log line with `outcome == "client_cancelled"`.
- T-44: open a raw TCP connection to the gateway, write
  `POST /openai/v1/chat/completions HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer <token>\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{"model":`
  and nothing more, with `RequestBodyReadTimeout` 200 ms; assert the connection is closed by the
  server within 3 s and `len(up.Requests()) == 0`.
- T-45: baseline `runtime.NumGoroutine()` after one warm-up request; 50 times: start a gated
  stream, read the first event, cancel; then close idle connections of the test client and of
  the gateway transport and `Eventually` (5 s) `runtime.NumGoroutine() <= baseline+3` and
  `up.InFlight() == 0`.
- T-46: `BaseURL` = `http://127.0.0.1:1/v1`.
- T-52: send `//openai/v1/chat/completions` with a raw `http.Request` whose `URL.Path` is set
  by hand, so the client does not clean it.
- T-56: `Transport` is a `RoundTripper` whose `RoundTrip` panics with a string containing
  `fixtures.CanaryPrompt`; assert the canary is not in the logs.
- T-57: the expected key set for a successful injected stream is exactly: `time`, `level`,
  `msg`, `request_id`, `method`, `route`, `status`, `outcome`, `duration_ms`, `provider`,
  `operation`, `stream`, `usage_injected`, `upstream_status`, `ttfb_ms`, `request_bytes`,
  `response_bytes`, `usage_source`, `usage`.

### B11. `internal/server/server_test.go`

T-58 to T-61. Helper `startServer(t, mutate func(*config.Config)) (*Server, *fakeprovider.Server, *testutil.LogBuffer, cancel func(), done <-chan error)`:
builds a `config.Config` from `config.Defaults()` with address `127.0.0.1:0`, the fake's base
URL parsed into `ParsedBaseURL`, the canary secrets, then `New`, `Listen`, and `Serve` in a
goroutine.

- T-59: start a gated stream, read the first event, call `cancel()`; `Eventually` a new TCP
  connection to the address is refused; call `up.Release()`; read the stream to `[DONE]`; assert
  `done` yields nil and the `shutdown complete` line has `forced == false`. Readiness during
  the drain is asserted through `srv.Health().Ready(ctx) == false` (the listener is closed, so
  it cannot be asked over HTTP).

### B12. `cmd/pandokos/main_test.go`

T-62, T-63, T-64, all through `run(ctx, args, &stdout, &stderr, environ)`.

### B13. `test/e2e/e2e_test.go` — package `e2e`

T-65, T-66.

- `TestMain` builds the binary once:
  `go build -o <tempdir>/pandokos github.com/pandokos/pandokos/cmd/pandokos`, with the working
  directory set to the module root (found with `go list -m -f '{{.Dir}}'`).
- Helper `startBinary(t, level string) *proc`: writes a YAML file with `log.level`,
  `listeners.inference.address: 127.0.0.1:0`, `shutdown.drain_timeout: 5s`,
  `skeleton.access_token_file`; passes the provider settings by environment
  (`PANDOKOS_SKELETON_OPENAI_BASE_URL`, `PANDOKOS_SKELETON_OPENAI_API_KEY_FILE`); starts
  `pandokos serve --config <file>` with a clean environment (only those variables plus `PATH`);
  captures stdout and stderr in two `LogBuffer`s; learns the port from the `address` field of
  the `listening` log line (`Eventually`, 10 s); waits for `/readyz` 200.
- `proc.stop(t)` sends `SIGTERM`, waits up to 10 s for exit, returns the exit code.
- T-66 scenario, run in sub-tests `info` and `debug`: (1) non-streaming `fake-basic`;
  (2) streaming `fake-basic`, read to the end; (3) no `Authorization`; (4) `GET /nope?token=<CanaryAccessToken>`;
  (5) a body larger than a 1024-byte limit (start this process with
  `PANDOKOS_LIMITS_MAX_REQUEST_BODY_BYTES=1024` and send the prompt canary repeated);
  (6) `fake-error-500-echo`; (7) stop; then a second process whose base URL is
  `http://127.0.0.1:1/v1`, one call, stop; (8) `pandokos config check` with the same
  configuration. Collect every response (status line, headers, body) with a label. Assertions:
  `AssertNoCanary` on stdout and stderr of every process run; no response contains
  `CanaryProviderKey`; the responses of (3), (4), (5) and of the unreachable call contain no
  canary at all. Response (6) is expected to contain the prompt and completion canaries (it is
  the provider's body, passed through): assert that it does, so the test proves the scan would
  have found them in the logs.

### B14. `test/repo/repo_test.go` — package `repo`

T-R1 to T-R7. The repository root is found by walking up from the working directory to the
directory containing `go.mod` with module `github.com/pandokos/pandokos`. Files are read as
text; workflows are checked line by line with regular expressions (no YAML parser is needed):

- T-R1: every line matching `^\s*-?\s*uses:\s*(\S+)` must match
  `^(\./\S+|[\w.-]+/[\w./-]+@[0-9a-f]{40})\s+# v\d+\.\d+\.\d+$` after `uses:`.
- T-R2: parse `go.mod` by lines.
- T-R7: walk `cmd/` and `internal/`.

### B15. `test/container/container_test.go` — package `container`, first line `//go:build container`

T-72. Skips with a clear message if `PANDOKOS_TEST_IMAGE` is empty. Uses `os/exec` with the
`docker` CLI only:

1. `docker image inspect -f '{{.Config.User}}|{{json .Config.Entrypoint}}|{{json .Config.Cmd}}' $IMAGE`
   must be `65532:65532|["/pandokos"]|["serve"]`.
2. Start `fakeprovider.New(t)`. Create a directory with mode 0755 holding the two secret files
   with mode 0644. Pick a free TCP port (listen on `127.0.0.1:0`, read the port, close).
3. `docker run -d --network host --read-only --cap-drop ALL --security-opt no-new-privileges
   -v <dir>:/run/secrets:ro
   -e PANDOKOS_LISTENERS_INFERENCE_ADDRESS=127.0.0.1:<port>
   -e PANDOKOS_SKELETON_ACCESS_TOKEN_FILE=/run/secrets/access_token
   -e PANDOKOS_SKELETON_OPENAI_API_KEY_FILE=/run/secrets/openai_api_key
   -e PANDOKOS_SKELETON_OPENAI_BASE_URL=<fake base URL> $IMAGE`
4. `Eventually` (20 s) `/readyz` is 200; one streaming call returns the events ending in
   `[DONE]`.
5. `docker stop -t 10 <id>`; `docker inspect -f '{{.State.ExitCode}}' <id>` is `0`;
   `docker logs <id>` passes `AssertNoCanary`; `docker rm <id>` in cleanup.
6. `docker run --rm $IMAGE version` starts with `pandokos `.

### B16. `test/sdk/` — nested module (commit it with part B; it fails until part C)

- `test/sdk/go.mod`:

  ```
  module github.com/pandokos/pandokos/test/sdk

  go 1.26.0

  require (
      github.com/openai/openai-go/v3 v3.98.0
      github.com/pandokos/pandokos v0.0.0
  )

  replace github.com/pandokos/pandokos => ../..
  ```

  Then, in `test/sdk`, run `GOTOOLCHAIN=go1.27.4 go mod tidy` to add the indirect requirements
  and create `go.sum`. Expected indirect modules: `github.com/tidwall/gjson`,
  `github.com/tidwall/match`, `github.com/tidwall/pretty`, `github.com/tidwall/sjson`,
  `github.com/coder/websocket`, and `go.yaml.in/yaml/v3` (through the product). If `go mod tidy`
  wants anything else, stop and ask.

- `test/sdk/sdk_test.go` — package `sdk`. T-67 to T-71. It imports
  `github.com/pandokos/pandokos/internal/config`, `internal/server`, `internal/telemetry`,
  `test/fakeprovider`, `test/fixtures`, `test/testutil` (allowed: the nested module's path is
  inside the product's path, so `internal` packages are visible to it).

  Helper `start(t) (baseURL string, up *fakeprovider.Server)`: same wiring as B11, returns
  `"http://" + srv.InferenceAddr().String() + "/openai/v1"`.

  The SDK client is always built like this, and in no other way (no environment variable is
  read by the test):

  ```go
  client := openai.NewClient(
      option.WithBaseURL(baseURL),
      option.WithAPIKey(fixtures.CanaryAccessToken),
      option.WithMaxRetries(0),
  )
  ```

  Calls (this API was compiled and run against v3.98.0):

  ```go
  params := openai.ChatCompletionNewParams{
      Model:    fakeprovider.ModelBasic,
      Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage(fixtures.CanaryPrompt)},
  }
  completion, err := client.Chat.Completions.New(ctx, params)            // T-67

  stream := client.Chat.Completions.NewStreaming(ctx, params)            // T-68
  acc := openai.ChatCompletionAccumulator{}
  for stream.Next() {
      chunk := stream.Current()
      acc.AddChunk(chunk)
  }
  err = stream.Err()

  var apiErr *openai.Error                                               // T-70
  errors.As(err, &apiErr) // apiErr.StatusCode, apiErr.Code
  ```

  T-68 asserts: 4 chunks; every chunk has `len(chunk.Choices) == 1`;
  `acc.Choices[0].Message.Content == fixtures.CanaryCompletion + " end"`. T-69 sets
  `params.StreamOptions = openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)}`
  and asserts 5 chunks, the last with empty choices and `Usage.TotalTokens == 21`. (If these two
  identifiers do not exist under those names in v3.98.0, find the equivalents in the SDK's
  `chatcompletion.go` and note the change in the pull request; do not change the SDK version.)
  T-71 cancels the context after the first `stream.Next()` on `ModelGatedStream` and asserts
  `up.WaitCancelled(2 * time.Second)`.

**Checkpoint B.** `go vet ./...` and `go test ./...` fail. Save the output. Commit.

## Part C — Implementation

After each step run the stated command; it must pass before the next step. Commit after each
step with the message given.

### C1. `internal/buildinfo/buildinfo.go` — `feat: build information`

Contents per §5.2. No test file (covered by T-62).

### C2. `internal/secret/secret.go` — `feat: secret type and secret files`

Contents per §5.3. `Format` is
`func (s Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, Redacted) }`.
`Equal`: `a := sha256.Sum256([]byte(s.v)); b := sha256.Sum256([]byte(candidate)); return subtle.ConstantTimeCompare(a[:], b[:]) == 1`.

Verify: `go test -race ./internal/secret/` (T-01 to T-03).

### C3. `internal/config/` — `feat: static configuration`

Run `go get go.yaml.in/yaml/v3@v3.0.6` first.

- `config.go`: the types of §5.4, `Duration` with `UnmarshalYAML(*yaml.Node) error` (accept only
  `node.Kind == yaml.ScalarNode && node.Tag == "!!str"`), `Defaults()`.
- `keys.go`: `Keys()` and an unexported table that pairs each key with a setter
  `func(c *Config, v string) error` used for environment values (strings assigned, durations
  with `time.ParseDuration`, integers with `strconv`). The `Env` name is computed from the key
  name by the rule of §5.4, not typed by hand.
- `load.go`: `Load`, `FileFromEnviron`. Order inside `Load`: defaults; file (decoder with
  `KnownFields(true)`; `io.EOF` on the first `Decode` means an empty file; a second `Decode`
  must return `io.EOF`, anything else is the "second document" problem); environment; validate;
  read secret files; cross-checks; return. Decoder and environment-parsing problems are
  collected as `Problem`s and validation still runs on what was loaded, so that all problems
  are reported together.
- `validate.go`: the rules and warnings of §5.4. Loopback test for warnings: host is
  `localhost`, or parses as an IP for which `IsLoopback()` is true.
- YAML error hygiene (§5.4): if the error is a `*yaml.TypeError`, handle each of its `Errors`
  strings with two regular expressions: `^line (\d+): field (\S+) not found in type ` →
  `line N: unknown key X`; `^line (\d+): ` → `line N: invalid value`; otherwise `invalid YAML`.
  For any other error type: `invalid YAML`.

Verify: `go test -race ./internal/config/` (T-04 to T-16).

### C4. `internal/telemetry/log.go` — `feat: structured logging with redaction`

Per §5.5. `ReplaceAttr(groups, a)`: if `a.Key == slog.TimeKey` and `len(groups) == 0`, convert
the time to UTC; if `IsSensitiveKey(a.Key)`, return `slog.String(a.Key, secret.Redacted)`.
An unknown level string falls back to info (configuration validation has already rejected it).

Verify: `go test -race ./internal/telemetry/` (T-17, T-18).

### C5. `internal/health/health.go` — `feat: liveness and readiness`

Per §5.6.

Verify: `go test -race ./internal/health/` (T-19 to T-21).

### C6. `internal/family/family.go` — `feat: usage form and error codes`

Per §5.7.

Verify: `go test -race ./internal/family/` (T-22).

### C7. `internal/family/openai/` — `feat: OpenAI family for chat completions`

Files: `doc.go` (package comment), `route.go` (`Name`, operation and path constants, `Match`,
`InboundCredential`, `AttachCredential`, `RequestHeaders`, `AllowResponseHeader`), `request.go`,
`usage.go` (`wireUsage`, its conversion to `family.Usage` with the mapping table of §5.7 and the
negative-value check, `ReadUsage`), `stream.go`, `errors.go` (`WriteError`).

`request.go` — use this implementation. It was tested against the table of B9 and passes the
linters of A5.

```go
const includeUsage = `"include_usage":true`

// member is one member of a JSON object: its key and the byte range of its value.
type member struct {
	key        string
	start, end int
}

// scanObject returns the members of the JSON object in b, in order, and the index of its
// closing brace. It fails if b is not exactly one JSON object.
func scanObject(b []byte) (members []member, closeIdx int, err error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return nil, 0, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, 0, errors.New("not a JSON object")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, 0, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, 0, errors.New("object key is not a string")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, 0, err
		}
		end := int(dec.InputOffset())
		members = append(members, member{key: key, start: end - len(raw), end: end})
	}
	if _, err := dec.Token(); err != nil {
		return nil, 0, err
	}
	closeIdx = int(dec.InputOffset()) - 1
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, 0, errors.New("data after the JSON object")
	}
	return members, closeIdx, nil
}

// last returns the last member with the given key: a repeated key resolves to its last
// occurrence, as in encoding/json.
func last(members []member, key string) (member, bool) {
	for i := len(members) - 1; i >= 0; i-- {
		if members[i].key == key {
			return members[i], true
		}
	}
	return member{}, false
}

func splice(b []byte, start, end int, ins string) []byte {
	out := make([]byte, 0, len(b)-(end-start)+len(ins))
	out = append(out, b[:start]...)
	out = append(out, ins...)
	return append(out, b[end:]...)
}

// PrepareRequest: see the doc comment in sprint.md §5.7.
func PrepareRequest(body []byte) (out []byte, stream, strip bool) {
	members, closeIdx, err := scanObject(body)
	if err != nil {
		return body, false, false
	}
	sm, ok := last(members, "stream")
	if !ok || !bytes.Equal(body[sm.start:sm.end], []byte("true")) {
		return body, false, false
	}
	so, ok := last(members, "stream_options")
	if !ok {
		return splice(body, closeIdx, closeIdx, `,"stream_options":{`+includeUsage+`}`), true, true
	}
	val := body[so.start:so.end]
	if bytes.Equal(val, []byte("null")) {
		return splice(body, so.start, so.end, `{`+includeUsage+`}`), true, true
	}
	inner, _, err := scanObject(val)
	if err != nil {
		return body, true, false
	}
	iu, ok := last(inner, "include_usage")
	if !ok {
		ins := includeUsage
		if len(inner) > 0 {
			ins += ","
		}
		return splice(body, so.start+1, so.start+1, ins), true, true
	}
	if bytes.Equal(val[iu.start:iu.end], []byte("true")) {
		return body, true, false
	}
	return splice(body, so.start+iu.start, so.start+iu.end, "true"), true, true
}
```

`stream.go` — use this core. It was tested for every chunk size and fuzzed.

```go
const maxEventBytes = 1 << 20

type chunk struct {
	Choices []struct{} `json:"choices"`
	Usage   *wireUsage `json:"usage"`
}

// StreamReader: see sprint.md §5.7.
type StreamReader struct {
	strip    bool
	failed   bool
	buf      []byte // the incomplete event being assembled, nothing else
	scan     int    // index in buf of the first line not examined yet
	out      []byte // reused output buffer (strip mode)
	usage    family.Usage
	hasUsage bool
}

func NewStreamReader(strip bool) *StreamReader { return &StreamReader{strip: strip} }

func (r *StreamReader) Filter(p []byte) []byte {
	if r.failed {
		return p
	}
	r.out = r.out[:0]
	r.buf = append(r.buf, p...)
	start := 0
	for {
		end, next := eventEnd(r.buf, r.scan)
		r.scan = next
		if end < 0 {
			break
		}
		ev := r.buf[start:end]
		usageOnly := r.inspect(ev)
		if r.strip && !usageOnly {
			r.out = append(r.out, ev...)
		}
		start = end
	}
	rest := r.buf[start:]
	if len(rest) > maxEventBytes {
		r.failed = true
		if r.strip {
			r.out = append(r.out, rest...)
		}
		r.buf, r.scan = nil, 0
	} else {
		r.buf = append(r.buf[:0], rest...)
		r.scan -= start
	}
	if !r.strip {
		return p
	}
	return r.out
}

func (r *StreamReader) Flush() []byte {
	if !r.strip || r.failed {
		return nil
	}
	b := r.buf
	r.buf, r.scan = nil, 0
	return b
}

// eventEnd looks for the empty line that ends the current event. scan is the index of the
// first line not examined yet. It returns the index just after the empty line, or -1, and the
// index from which the next call must resume.
func eventEnd(b []byte, scan int) (end, next int) {
	i := scan
	for {
		j := bytes.IndexByte(b[i:], '\n')
		if j < 0 {
			return -1, i
		}
		line := b[i : i+j]
		i += j + 1
		if len(line) == 0 || (len(line) == 1 && line[0] == '\r') {
			return i, i
		}
	}
}

// inspect records the usage carried by a complete event and reports whether the event is a
// usage-only event.
func (r *StreamReader) inspect(ev []byte) bool {
	var data []byte
	n := 0
	for _, line := range bytes.Split(ev, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		v := bytes.TrimPrefix(line[len("data:"):], []byte(" "))
		if n == 0 {
			data = v
		} else {
			data = append(append(append([]byte(nil), data...), '\n'), v...)
		}
		n++
	}
	if len(data) == 0 || data[0] != '{' {
		return false
	}
	var c chunk
	if err := json.Unmarshal(data, &c); err != nil || c.Usage == nil {
		return false
	}
	u, err := c.Usage.normalize() // wireUsage → family.Usage; error on a negative count
	if err != nil {
		return false
	}
	r.usage, r.hasUsage = u, true
	return c.Choices != nil && len(c.Choices) == 0
}
```

Add `Usage()` (returns `r.usage, r.hasUsage`) and `Failed()`. Note for T-33: in tee mode the
reader also stops parsing after an oversized event and `Failed()` is true.

`errors.go`: marshal
`struct{ Error struct{ Message string `json:"message"`; Type string `json:"type"`; Param *string `json:"param"`; Code string `json:"code"` } `json:"error"` }`
with `Type: "pandokos_error"`, nil `Param`; set `Content-Type: application/json`,
`X-Pandokos-Error: <code>`; `WriteHeader(code.Status())`; write the bytes.

Verify: `go test -race ./internal/family/...` (T-23 to T-36), then
`make fuzz FUZZTIME=20s` (must find nothing) and `make bench` (keep the output for the review).

### C8. `internal/gateway/` — `feat: inference handler with streaming proxy`

Files:

- `doc.go`.
- `transport.go`: `NewTransport` per §5.8.
- `requestid.go`: `newRequestID() string` (16 bytes of `crypto/rand`, `hex.EncodeToString`).
- `writer.go`: `statusWriter` wrapping `http.ResponseWriter`: records the first status
  (`WriteHeader`, or 200 at the first `Write`), `wroteHeader() bool`, `status() int`,
  `Unwrap() http.ResponseWriter`. It must also implement `http.Flusher` by delegating
  (`ReverseProxy` flushes through `http.NewResponseController`, which uses `Unwrap`; the
  explicit `Flush` method is for safety and is harmless).
- `body.go`: `streamBody` (the stream filter of §5.8, with a 32 KiB internal buffer and a byte
  counter), `teeBody` (bounded 4 MiB buffer for `ReadUsage`, byte counter; once the limit is
  passed the buffer is released and marked overflowed), `countBody`. Each has `Close` that
  closes the provider body.
- `handler.go`: `Options`, `New`, `Handler`, `ServeHTTP`, the per-request state struct, the
  deferred outcome function, `logRequest`.
- `proxy.go`: construction of the `httputil.ReverseProxy` (`Rewrite`, `ModifyResponse`,
  `ErrorHandler`, `FlushInterval`, `ErrorLog`).

Implementation notes (all are consequences of §5.8; none adds behaviour):

1. The per-request state is a pointer stored in the request context under an unexported key
   type, so that `Rewrite`, `ModifyResponse` and `ErrorHandler` reach it through
   `req.Context()` (`resp.Request.Context()` in `ModifyResponse`).
2. Keep **two** contexts: `callerCtx := r.Context()` (before step 8) and the outbound context
   with the deadline. The outcome rules test `callerCtx.Err()`, `context.Cause(callerCtx)` and
   the outbound context's `Err()` separately. Call the outbound context's cancel function in a
   `defer`.
3. `Rewrite` signature is `func(pr *httputil.ProxyRequest)`. Build the URL as
   `u := *h.baseURL; u.Path = h.baseURL.Path + openai.PathChatCompletions; u.RawPath = ""; u.RawQuery = ""; pr.Out.URL = &u; pr.Out.Host = h.baseURL.Host`.
   Build the header as a new `http.Header`, copy with `pr.In.Header.Values(name)` for each name
   of `openai.RequestHeaders`, call `openai.AttachCredential(hdr, h.providerKey.Reveal())`,
   assign `pr.Out.Header = hdr`. This is the only call of `Reveal()` on the provider key in the
   product code.
4. In `ModifyResponse`, replace headers by building a new `http.Header` with the allowed
   entries and assigning it to `resp.Header`. Always return nil.
5. The forced-shutdown cause is compared with `errors.Is(context.Cause(callerCtx), ErrShutdown)`
   where `ErrShutdown` is an exported variable of package `gateway`
   (`var ErrShutdown = errors.New("forced shutdown")`); `internal/server` cancels its base
   context with this value. (§5.9 calls it `ErrForcedShutdown` in `server`; define it once, in
   `gateway`, as `gateway.ErrShutdown`, and have `server` use it. `gateway` cannot import
   `server`.)
6. `method` for the log: a `switch` over the seven method constants, default `OTHER`.
7. The `usage` log group: `slog.Group("usage", …)` built from the `family.Usage` fields; `Extra`
   keys are added in sorted order.
8. Step 6 of the request handling: obtain the controller with
   `rc := http.NewResponseController(w)` using the **original** writer or the `statusWriter`
   (both work, because of `Unwrap`). Ignore the error of `SetReadDeadline` only if it is
   `http.ErrNotSupported` (as with `httptest.NewRecorder`); any other error is treated as a
   body read failure.
9. `New` validates `Options` and returns an error naming the missing field.

Verify: `go test -race ./internal/gateway/` (T-37 to T-57).

### C9. `internal/server/server.go` — `feat: listener, wiring and graceful shutdown`

Per §5.9. `New` builds: `health.New(logger)`; `gateway.New` with values copied from the
configuration and `gateway.NewTransport(cfg.Upstream.ConnectTimeout)`; the router function of
§5.8; the `http.Server`. `Listen` uses `net.Listen("tcp", address)`. `Serve` starts
`http.Server.Serve(listener)` in a goroutine, waits for either its error (other than
`http.ErrServerClosed`) or `ctx.Done()`, and runs the seven-step shutdown sequence.

Verify: `go test -race ./internal/server/` (T-58 to T-61).

### C10. `cmd/pandokos/` — `feat: serve, config check and version commands`

- `main.go`: `main` and `run` (dispatch on `args[0]`: `serve`, `config`, `version`, `help`,
  `-h`, `--help`).
- `serve.go`: flag set `serve` with `--config`; `config.Load`; bootstrap logger for
  configuration errors; `telemetry.NewLogger(stdout, cfg.Log.Level)`; `starting`; warnings;
  `server.New`, `Listen`, `listening`; `signal.NotifyContext`; `Serve`; exit code.
- `configcheck.go`: `config` requires the sub-command `check`; flag `--config`; output per §5.1.
- `usage.go`: the usage text (a constant).

The `forbidigo` rule forbids `fmt.Print*`: write with `fmt.Fprintf(stdout, …)` and
`fmt.Fprintln(stderr, …)`.

Verify: `go test -race ./cmd/...` (T-62 to T-64), then `go build ./...`,
then `go test -race -count=1 ./...` — everything in the root module passes now, including
`test/e2e` (T-65, T-66), except `test/repo` (its files come in parts D and E).

### C11. Official SDK suite — `test: official OpenAI SDK suite passes`

`make test-sdk` (T-67 to T-71). No product code changes are expected in this step. If a test
fails because of product behaviour, fix the product and add a test at the lowest level that
reproduces it.

## Part D — Container, CI, release (commit: `build: container image, CI and release workflows`)

### D1. `Dockerfile`

```dockerfile
FROM --platform=$BUILDPLATFORM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-mod=readonly
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
    -ldflags "-s -w -buildid= -X github.com/pandokos/pandokos/internal/buildinfo.Version=$VERSION -X github.com/pandokos/pandokos/internal/buildinfo.Commit=$COMMIT" \
    -o /out/pandokos ./cmd/pandokos

FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
COPY --from=build /out/pandokos /pandokos
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/pandokos"]
CMD ["serve"]
```

Verify the two digests (each command prints a `Digest:` line that must equal the value above):

```
docker buildx imagetools inspect golang:1.27.4-bookworm
docker buildx imagetools inspect gcr.io/distroless/static-debian13:nonroot
```

The distroless `nonroot` tag moves when the base is rebuilt: a different digest there is
expected over time. In that case use the digest the command prints **today** and say so in the
pull request. A different digest for the versioned `golang` tag is not expected: stop and ask.

Verify: `make image && make test-container` (T-72).

### D2. `.github/dependabot.yml`

```yaml
version: 2
updates:
  - package-ecosystem: gomod
    directory: /
    schedule:
      interval: weekly
  - package-ecosystem: gomod
    directory: /test/sdk
    schedule:
      interval: weekly
  - package-ecosystem: github-actions
    directory: /
    schedule:
      interval: weekly
  - package-ecosystem: docker
    directory: /
    schedule:
      interval: weekly
```

### D3. Action pins

Use exactly these. Verify each with
`git ls-remote https://github.com/<owner>/<repo> 'refs/tags/<tag>*'`: for a lightweight tag the
SHA is on the `refs/tags/<tag>` line; for an annotated tag it is on the `refs/tags/<tag>^{}`
line.

| Action | Tag | Commit SHA |
|---|---|---|
| `actions/checkout` | v7.0.1 | `3d3c42e5aac5ba805825da76410c181273ba90b1` |
| `actions/setup-go` | v7.0.0 | `b7ad1dad31e06c5925ef5d2fc7ad053ef454303e` |
| `golangci/golangci-lint-action` | v9.3.0 | `ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a` (annotated tag) |
| `docker/setup-buildx-action` | v4.4.1 | `f87e5991a6d7451dcb8d9637bfbc97413f497069` |
| `docker/build-push-action` | v7.4.0 | `c3c9e263c25d99ce0380d002d59b67737d91b0dc` |
| `docker/login-action` | v4.8.0 | `8aa5831fb8d48d8f76594c7ddde61ae58019f550` |
| `sigstore/cosign-installer` | v4.1.3 | `756fe2d284c7292c4e9b62c8a28f1e5629c59532` |
| `anchore/sbom-action` | v0.24.3 | `66cbf4bc1f1c0d2edc94016e65bc221b6bb0ad6c` (annotated tag) |
| `anchore/scan-action` | v7.4.2 | `27805bf3b4e84b4a5c980df22ed233c00390a439` (annotated tag) |
| `actions/attest-build-provenance` | v4.2.2 | `4d101475d8b20a2381f78447822ac1eab6504dd8` |

The inputs used below were checked against each action's `action.yml` at these commits on
2026-11-11. The behaviour of these versions on GitHub's runners could not be exercised from the
design environment (sprint.md R-3): the first pull request run and the first tag are the test.

### D4. `.github/workflows/ci.yml`

```yaml
name: ci

on:
  pull_request:
  push:
    branches: [main]
  workflow_call:

permissions:
  contents: read

concurrency:
  group: ci-${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: ${{ github.event_name == 'pull_request' }}

env:
  GOTOOLCHAIN: local

jobs:
  lint:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version: "1.24.7"
      - run: make fmt-check
      - run: make vet
      - run: make tidy
      - uses: golangci/golangci-lint-action@ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a # v9.3.0
        with:
          version: v2.5.0

  test:
    runs-on: ubuntu-24.04
    strategy:
      fail-fast: false
      matrix:
        include:
          - name: floor
            go-version: "1.24.7"
            go-version-file: ""
          - name: release
            go-version: ""
            go-version-file: .go-version
    name: test (${{ matrix.name }})
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version: ${{ matrix.go-version }}
          go-version-file: ${{ matrix.go-version-file }}
      - run: go build ./...
      - run: go test -race -count=1 ./...

  sdk:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version-file: .go-version
          cache-dependency-path: test/sdk/go.sum
      - run: go vet ./...
        working-directory: test/sdk
      - run: go mod tidy -diff
        working-directory: test/sdk
      - run: go test -race -count=1 ./...
        working-directory: test/sdk

  vuln:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version-file: .go-version
      - run: go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -test ./...
      - run: go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -test ./...
        working-directory: test/sdk

  image:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version-file: .go-version
      - uses: docker/setup-buildx-action@f87e5991a6d7451dcb8d9637bfbc97413f497069 # v4.4.1
      - name: Build both platforms
        uses: docker/build-push-action@c3c9e263c25d99ce0380d002d59b67737d91b0dc # v7.4.0
        with:
          context: .
          platforms: linux/amd64,linux/arm64
          push: false
          provenance: false
          sbom: false
          build-args: |
            VERSION=ci
            COMMIT=${{ github.sha }}
      - name: Load the amd64 image
        uses: docker/build-push-action@c3c9e263c25d99ce0380d002d59b67737d91b0dc # v7.4.0
        with:
          context: .
          platforms: linux/amd64
          load: true
          tags: pandokos:ci
          provenance: false
          sbom: false
          build-args: |
            VERSION=ci
            COMMIT=${{ github.sha }}
      - name: Container tests
        run: go test -tags container -count=1 ./test/container/
        env:
          PANDOKOS_TEST_IMAGE: pandokos:ci
      - name: SBOM
        uses: anchore/sbom-action@66cbf4bc1f1c0d2edc94016e65bc221b6bb0ad6c # v0.24.3
        with:
          image: pandokos:ci
          format: spdx-json
          output-file: sbom.spdx.json
          artifact-name: sbom.spdx.json
          upload-artifact: true
          upload-release-assets: false
      - name: Scan
        uses: anchore/scan-action@27805bf3b4e84b4a5c980df22ed233c00390a439 # v7.4.2
        with:
          image: pandokos:ci
          fail-build: true
          severity-cutoff: high
          only-fixed: true
          output-format: table
```

`make tidy`, `make vet` and `make fmt-check` need no release toolchain. `GOTOOLCHAIN: local`
guarantees that each job really uses the Go version it installed.

### D5. `.github/workflows/release.yml`

```yaml
name: release

on:
  push:
    tags: ["v*"]

permissions:
  contents: read

env:
  IMAGE: ghcr.io/pandokos/pandokos

jobs:
  check-tag:
    if: github.repository == 'pandokos/pandokos'
    runs-on: ubuntu-24.04
    outputs:
      version: ${{ steps.v.outputs.version }}
      prerelease: ${{ steps.v.outputs.prerelease }}
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          fetch-depth: 0
          persist-credentials: false
      - id: v
        env:
          TAG: ${{ github.ref_name }}
        run: |
          set -eu
          if ! printf '%s' "$TAG" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$'; then
            echo "not a semantic version tag: $TAG"; exit 1
          fi
          ok=false
          for ref in $(git for-each-ref --format='%(refname)' refs/remotes/origin/main 'refs/remotes/origin/release/*'); do
            if git merge-base --is-ancestor "$GITHUB_SHA" "$ref"; then ok=true; fi
          done
          if [ "$ok" != true ]; then echo "the tagged commit is not on main or a release branch"; exit 1; fi
          echo "version=${TAG#v}" >> "$GITHUB_OUTPUT"
          case "$TAG" in
            *-*) echo "prerelease=true" >> "$GITHUB_OUTPUT" ;;
            *)   echo "prerelease=false" >> "$GITHUB_OUTPUT" ;;
          esac

  ci:
    needs: check-tag
    uses: ./.github/workflows/ci.yml

  image:
    needs: [check-tag, ci]
    runs-on: ubuntu-24.04
    permissions:
      contents: read
      packages: write
      id-token: write
      attestations: write
    outputs:
      digest: ${{ steps.build.outputs.digest }}
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: docker/setup-buildx-action@f87e5991a6d7451dcb8d9637bfbc97413f497069 # v4.4.1
      - uses: docker/login-action@8aa5831fb8d48d8f76594c7ddde61ae58019f550 # v4.8.0
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - id: tags
        env:
          VERSION: ${{ needs.check-tag.outputs.version }}
          PRERELEASE: ${{ needs.check-tag.outputs.prerelease }}
        run: |
          set -eu
          {
            echo "tags<<EOF"
            echo "${IMAGE}:${VERSION}"
            if [ "$PRERELEASE" = false ]; then
              echo "${IMAGE}:${VERSION%.*}"
              echo "${IMAGE}:latest"
            fi
            echo "EOF"
          } >> "$GITHUB_OUTPUT"
      - id: build
        uses: docker/build-push-action@c3c9e263c25d99ce0380d002d59b67737d91b0dc # v7.4.0
        with:
          context: .
          platforms: linux/amd64,linux/arm64
          push: true
          provenance: false
          sbom: false
          tags: ${{ steps.tags.outputs.tags }}
          labels: |
            org.opencontainers.image.title=pandokos
            org.opencontainers.image.description=Multi-tenant LLM proxy
            org.opencontainers.image.source=https://github.com/pandokos/pandokos
            org.opencontainers.image.licenses=Apache-2.0
            org.opencontainers.image.version=${{ needs.check-tag.outputs.version }}
            org.opencontainers.image.revision=${{ github.sha }}
          build-args: |
            VERSION=${{ needs.check-tag.outputs.version }}
            COMMIT=${{ github.sha }}
      - uses: sigstore/cosign-installer@756fe2d284c7292c4e9b62c8a28f1e5629c59532 # v4.1.3
        with:
          cosign-release: v3.1.4
      - name: Sign the image
        env:
          DIGEST: ${{ steps.build.outputs.digest }}
        run: cosign sign --yes "${IMAGE}@${DIGEST}"
      - name: SBOM
        uses: anchore/sbom-action@66cbf4bc1f1c0d2edc94016e65bc221b6bb0ad6c # v0.24.3
        with:
          image: ${{ env.IMAGE }}@${{ steps.build.outputs.digest }}
          format: spdx-json
          output-file: sbom.spdx.json
          artifact-name: sbom.spdx.json
          upload-artifact: true
          upload-release-assets: false
      - name: Attest the SBOM
        env:
          DIGEST: ${{ steps.build.outputs.digest }}
        run: cosign attest --yes --type spdxjson --predicate sbom.spdx.json "${IMAGE}@${DIGEST}"
      - name: Attest build provenance
        uses: actions/attest-build-provenance@4d101475d8b20a2381f78447822ac1eab6504dd8 # v4.2.2
        with:
          subject-name: ${{ env.IMAGE }}
          subject-digest: ${{ steps.build.outputs.digest }}
          push-to-registry: true

  verify:
    needs: [check-tag, image]
    runs-on: ubuntu-24.04
    permissions:
      contents: read
      packages: read
      attestations: read
    env:
      DIGEST: ${{ needs.image.outputs.digest }}
      VERSION: ${{ needs.check-tag.outputs.version }}
      IDENTITY: https://github.com/${{ github.repository }}/.github/workflows/release.yml@${{ github.ref }}
      ISSUER: https://token.actions.githubusercontent.com
    steps:
      - uses: docker/login-action@8aa5831fb8d48d8f76594c7ddde61ae58019f550 # v4.8.0
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - uses: sigstore/cosign-installer@756fe2d284c7292c4e9b62c8a28f1e5629c59532 # v4.1.3
        with:
          cosign-release: v3.1.4
      - name: Verify the signature
        run: cosign verify "${IMAGE}@${DIGEST}" --certificate-identity "$IDENTITY" --certificate-oidc-issuer "$ISSUER" > /dev/null
      - name: Verify the SBOM attestation
        run: cosign verify-attestation "${IMAGE}@${DIGEST}" --type spdxjson --certificate-identity "$IDENTITY" --certificate-oidc-issuer "$ISSUER" > /dev/null
      - name: Verify the build provenance
        env:
          GH_TOKEN: ${{ github.token }}
        run: gh attestation verify "oci://${IMAGE}@${DIGEST}" --repo "$GITHUB_REPOSITORY"
      - name: Verify the platforms
        run: |
          set -eu
          docker buildx imagetools inspect "${IMAGE}@${DIGEST}" > platforms.txt
          grep -q 'linux/amd64' platforms.txt
          grep -q 'linux/arm64' platforms.txt
      - name: Verify the version
        run: |
          set -eu
          out=$(docker run --rm "${IMAGE}@${DIGEST}" version)
          echo "$out"
          test "$(printf '%s' "$out" | cut -d' ' -f2)" = "$VERSION"

  publish:
    needs: [check-tag, image, verify]
    runs-on: ubuntu-24.04
    permissions:
      contents: write
      actions: read
    env:
      GH_TOKEN: ${{ github.token }}
      TAG: ${{ github.ref_name }}
      PRERELEASE: ${{ needs.check-tag.outputs.prerelease }}
      DIGEST: ${{ needs.image.outputs.digest }}
    steps:
      - run: gh run download "$GITHUB_RUN_ID" --repo "$GITHUB_REPOSITORY" --name sbom.spdx.json
      - run: |
          set -eu
          flag=""
          if [ "$PRERELEASE" = true ]; then flag="--prerelease"; fi
          gh release create "$TAG" --repo "$GITHUB_REPOSITORY" --verify-tag --title "$TAG" $flag \
            --notes "Image: \`${IMAGE}@${DIGEST}\`. Verification: docs/operations/container.md." \
            sbom.spdx.json
```

Notes:

- The CI job called from the release also uploads an artifact named `sbom.spdx.json` (from the
  `image` job of `ci.yml`). Two artifacts with one name in one run conflict. Therefore in
  `ci.yml` set the SBOM step's `artifact-name` to `sbom-ci.spdx.json` (keep `output-file`
  as it is). The `ci.yml` listing above must be written with that name.
- Do not add `docker/metadata-action`, `actions/download-artifact`, QEMU or any other action.
- Do not run or simulate a release. The workflow is exercised by the maintainer's first tag.

### D6. Verification of part D

`go test -race ./test/repo/` now passes T-R1, T-R2, T-R3, T-R6 (T-R4, T-R5 pass after part E).

## Part E — Documentation and decision records (commit: `docs: sprint 1.1 documentation and ADRs`)

### E1. `docs/operations/configuration.md`

Sections: sources and precedence; the file path (`--config`, `PANDOKOS_CONFIG`); the table of
every key with type, default, environment variable and meaning (same rows as `config.Keys()`:
T-R5 checks it); durations and sizes; secret files (trimming, limits, read once at start,
Docker and Kubernetes secret mounts as examples); validation rules; warnings; unknown
`PANDOKOS_` variables and the Kubernetes exception; `pandokos config check` with exit codes; a
complete example file; a paragraph stating that the `skeleton` section is temporary and will be
removed in later sprints.

### E2. `docs/operations/container.md`

Sections: the image (name, platforms, user 65532, read-only root file system, no shell); a
`docker run` example with two secret files mounted read-only and the three environment
variables; the endpoints (`/healthz`, `/readyz`, `POST /openai/v1/chat/completions`) and an SDK
example that changes only base URL and key; what Pandokos changes in a request and a response
(reference to ADR 0009 and ADR 0024, including the `"usage":null` remark); the error contract
table of sprint.md §5.8 and the two headers; logs (the `request` line field table); timeouts;
shutdown and draining; **verifying a release**:

```
cosign verify ghcr.io/pandokos/pandokos:<version> \
  --certificate-identity "https://github.com/pandokos/pandokos/.github/workflows/release.yml@refs/tags/v<version>" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

cosign verify-attestation ghcr.io/pandokos/pandokos:<version> --type spdxjson \
  --certificate-identity "https://github.com/pandokos/pandokos/.github/workflows/release.yml@refs/tags/v<version>" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

gh attestation verify oci://ghcr.io/pandokos/pandokos:<version> --repo pandokos/pandokos
```

and a statement of the limits of this pre-release: one static token, plain HTTP, one endpoint,
no tenants; not for production.

### E3. `SECURITY.md`

Keep the existing text. Add a section `## Response process` between "Reporting a vulnerability"
and "Scope" with this content (wording may be polished, facts may not change):

- acknowledgement of a report within 3 working days;
- triage (confirmed or not, severity, affected versions) within 10 working days;
- the fix is prepared in a private fork attached to a GitHub security advisory, with a test
  that reproduces the problem;
- the fix is released as a new version from `main` and, for the supported minor version, from
  its maintenance branch (CONTRIBUTING.md); tags are never moved;
- the advisory is published with the release, with a CVE requested through GitHub when the
  problem affects released versions; the reporter is credited unless they prefer otherwise;
- if a fix needs longer than 90 days the reporter is told why and when.

Rename nothing; the heading "Supported versions" stays as it is.

### E4. `README.md`

Replace the status note with one that says a walking skeleton exists (one endpoint, static
configuration, not for production) and link the two operations documents. Add them to the
"Documentation" list.

### E5. `docs/architecture/overview.md` §11

Add to the layout block the lines for `buildinfo/`, `secret/`, `health/`, `server/`, and under
`test/` mention `fakeprovider`, `e2e`, `repo`, `container` and the nested `sdk` module. Add
`docs/operations/`. Change nothing else in the document.

### E6. `docs/adr/0023-test-dependencies-in-nested-modules.md`

Status `proposed`, date of the commit. Use the template headings.

- **Context.** GW-02 and the release acceptance require tests with the official provider SDKs.
  The OpenAI Go SDK brings five transitive modules and, at the version current when this was
  written, requires a newer Go than the product's floor. ADR 0018 limits the product's
  dependencies to what the product needs; a `require` in the product's `go.mod` enters its
  `go.sum`, its dependency review, its vulnerability scope and source-based SBOMs, even if only
  tests import it.
- **Decision.** Tests that need a third-party module the product does not need live in a nested
  Go module under `test/` (first: `test/sdk`), with its own `go.mod` and `go.sum`, linked to the
  product by a `replace` directive to the repository root. The nested module may import the
  product's `internal` packages, since its path is inside the product's. The product module's
  direct dependencies stay on an allow-list checked by a test. The nested module may require a
  newer Go than the product. CI runs it as a separate job and runs `govulncheck` on it; it is
  never part of a release artefact. No `go.work` file is committed.
- **Consequences.** `go test ./...` at the root does not run the SDK suite: `make test-sdk` and
  the CI job do. Two `go.sum` files to keep current (Dependabot covers both). Refactoring an
  internal package can break the nested module without the root build noticing; the CI job
  catches it.
- **Alternatives considered.** Requiring the SDK in the product module (rejected: pollutes the
  graph and raises the Go floor); calling the SDK from another language's test suite (rejected:
  a second toolchain in CI for no gain now; may return for the Python SDKs in 3.3 under the same
  rule); hand-written HTTP requests instead of the SDK (rejected: does not prove GW-02).

### E7. `docs/adr/0024-openai-stream-usage-option.md`

Status `proposed`. Refines ADR 0010; does not supersede it.

- **Context.** ADR 0010 says that for OpenAI chat streams Pandokos adds the option that makes
  the provider report usage and removes the resulting usage-only event, so that the caller sees
  what it would have seen. Building it showed three points the ADR leaves open: how the request
  body is changed, what exactly is removed, and where the reader runs.
- **Decision.**
  1. The request body is changed by splicing bytes at the top-level `stream_options` member
     (the cases are the table of sprint.md §5.7, to be copied into the ADR). It is never parsed
     and re-serialized. A body that is not a JSON object, or a stream that already asks for
     usage, is forwarded untouched.
  2. In the response only the usage-only event (non-null `usage`, empty `choices`) is removed.
     When the option is on, the provider also adds `"usage":null` to every other chunk; that
     member is left in place. This is the one observable difference for a caller whose request
     was modified, and it is documented in the operations guide.
  3. The usage reader runs in line with the copy of the response, not in a parallel goroutine.
     It is bounded (one incomplete event of at most 1 MiB), fails open (on overflow it forwards
     everything unchanged and stops reading), and when nothing has to be removed it returns the
     provider's bytes untouched without waiting for event boundaries.
  4. When a caller asked for usage itself, nothing is added and nothing is removed.
- **Consequences.** "Byte for byte" (ADR 0009) holds outside the spliced range. A removed event
  means the reader holds an event until it is complete when stripping: the stream is forwarded
  event by event, not byte by byte, in that mode. Strict clients that reject unknown or null
  members would see `"usage":null`; the official SDK does not mind.
- **Alternatives considered.** Re-serializing the body (reorders keys, reformats numbers);
  rewriting every chunk to drop `"usage":null` (a JSON rewrite of every event of every stream);
  a parallel reader fed through a channel (needs a bounded queue and a drop policy, and cannot
  remove an event); not adding the option and estimating instead (loses the provider's exact
  count for the most common call shape).

### E8. `docs/adr/0025-go-toolchain.md`

Status `proposed`. Refines ADR 0002 ("current stable release").

- **Context.** The Go project supports the two most recent minor releases. A binary built with
  an unsupported release carries known standard-library vulnerabilities in exactly the packages
  this product is made of (`net/http`, `crypto/tls`); `govulncheck` reports them and SEC-06
  makes that a build failure. Contributors and development environments may have an older
  toolchain installed.
- **Decision.** Two separate pins. (1) The `go` directive in `go.mod` is the language floor:
  the oldest Go that can compile the product; it is raised deliberately, by a pull request, when
  a newer language or library feature is wanted. `go.mod` has no `toolchain` line. (2) The
  *release toolchain* is named in `.go-version` and is always a supported Go release, normally
  the current stable one. The container build, the vulnerability gate and the SDK suite use it;
  the Dockerfile's builder image must match it (a test checks). CI runs the test suite with both
  the floor and the release toolchain. A security release of Go is adopted by a pull request
  that changes `.go-version` and the Dockerfile.
- **Consequences.** Behaviour differences between Go versions surface in CI. `make vuln` and the
  SDK suite need the release toolchain locally; Go downloads it on demand
  (`GOTOOLCHAIN=go<version>`) where the network allows. Dependabot updates the Dockerfile but
  not `.go-version`; the test that compares them makes the pull request fail until both agree.
- **Alternatives considered.** One pin at the floor (cannot pass the vulnerability gate once
  the floor is out of support); one pin at the current release with a `toolchain` line (every
  developer command silently downloads a toolchain, and offline environments stop working);
  raising the floor to the current release at once (correct in the long run; deferred because
  the baseline development environment has go1.24.7 — see sprint.md O-1).

### E9. `docs/adr/README.md`

Add three rows to the table: 0023 "Test-only dependencies live in nested modules", 0024 "OpenAI
chat streams: how usage is requested and what is removed", 0025 "Go toolchain: language floor
and release toolchain"; status `proposed`.

### E10. Planning files

- `planning/epics/01-foundations/epic.md`: `status: in-progress`.
- This sprint's `sprint.md`: `status: in-progress` at the start of implementation. It is set to
  `done` by the review, not by the implementer.
- Open points O-1 to O-6 of sprint.md are copied into the pull request description under the
  heading "Questions for the maintainer".

## Part F — Verification

Run all of these from the repository root before opening the pull request. Each must end
successfully; paste the final lines of each in the pull request description.

```
gofmt -l cmd internal test          # prints nothing
go mod tidy -diff                   # prints nothing
go build ./...
go vet ./...
go test ./...
go test -race -count=1 ./...
golangci-lint config verify
golangci-lint run
GOTOOLCHAIN=go1.27.4 go test -race -count=1 ./...
( cd test/sdk && GOTOOLCHAIN=go1.27.4 go vet ./... && GOTOOLCHAIN=go1.27.4 go test -race -count=1 ./... )
GOTOOLCHAIN=go1.27.4 go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -test ./...
( cd test/sdk && GOTOOLCHAIN=go1.27.4 go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -test ./... )
make fuzz FUZZTIME=30s
make bench
make build && ./bin/pandokos version
make image && make test-container
docker run --rm pandokos:dev version
```

Expected and **not** a failure: `govulncheck ./...` run with the local go1.24.7 (that is,
without `GOTOOLCHAIN=go1.27.4`) reports standard-library vulnerabilities. That is the reason
for ADR 0025; the gate is the command above with the release toolchain. If the release toolchain
cannot be downloaded in your environment, say so in the pull request and rely on CI for the
`vuln` and `sdk` jobs; do not change the gate.

Manual smoke test (optional, no real key needed): start `test/fakeprovider` is not a binary, so
use the e2e test instead: `go test -run TestServeStreamAndSigterm -v ./test/e2e/`.

Final checks against the design:

1. Every test ID of sprint.md §9 exists: `grep -rhoE 'func (Test|Fuzz|Benchmark)[A-Za-z0-9_]+' cmd internal test | sort`
   and compare with the table.
2. `grep -rn -e 'pandokos-canary' cmd internal` prints nothing.
3. `grep -rn 'Reveal()' cmd internal --include='*.go' | grep -v _test.go` shows exactly two
   product call sites: the provider key in `internal/gateway/proxy.go` and none other outside
   `internal/secret` and `internal/config` (the token comparison uses `Equal`; `config` uses
   `Reveal` only to compare the two secrets and to check the token length).
4. Go through the definition of done (sprint.md §10) line by line and tick each box in the pull
   request description, with the evidence.

## What the implementer does not do

- Does not push tags, create releases or change repository settings: those are the maintainer's
  steps listed in the definition of done.
- Does not add packages, configuration keys, routes, error codes, log fields, dependencies or
  actions beyond those named here.
- Does not weaken a test, a linter or a gate to make a step pass.
