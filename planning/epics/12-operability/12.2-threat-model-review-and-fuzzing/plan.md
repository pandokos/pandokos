# Sprint 12.2 — Implementation plan

- Design: [sprint.md](sprint.md). Section numbers (§) refer to it; T-numbers are its tests, D-numbers
  its decisions, F-numbers its findings, `TB-`, `TS-` and `TM-` the identifiers of §5.2.
- Each part below is self-contained: an agent needs sprint.md and that part only. Parts run in order
  A, B, C1 … C5, D, E, F.
- Suggested grouping: **G1** = C1 (request members); **G2** = C2, C3 (egress table, listener tests);
  **G3** = C4 (the key service's key). After part B the three groups do not depend on each other.
  C5 exists only if a fuzz target finds something. A, B, D, E and F are one agent each. Because the
  risk class is high, C1, C2 and C4 are reviewed by an agent that did not write them, with §5.5,
  §5.6 and §5.7 as the checklist, and **part E1 is reviewed row by row against §5.2 by an agent
  that did not write it** (R-2).

## Rules for the implementer

1. Work on the branch the orchestrator names; do not create one. Every commit is signed off in the
   maintainer's name (`git commit -s`; the identity is the one of the repository's git
   configuration, never changed or overridden) and keeps the signature the configuration asks for
   (`commit.gpgsign`, SSH): never disable it. No co-author line, no attribution line, no tool
   footer, no link to a session and no mention of an assistant or of a model in any commit message,
   pull request title or description, comment, review, release text, footer or file. One commit for
   A, one for B, one per step of C (message given with the step), one for D, one for E, one for F
   if something is found.
2. **No migration, no change to `api/openapi/admin.v1.yaml`, no permission, no error code, no new
   module** in `go.mod` or in a nested module, **no GitHub Action that the workflows do not already
   use**. If something seems to need one, stop and ask.
3. **Never invent a commit hash, a digest, a version or an address.** Action pins are copied from
   `.github/workflows/ci.yml` as they stand when you write the workflow. The metadata addresses of
   C2 are added only after the check C2 describes. Where a value must be looked up, the command is
   given.
4. **A finding is never fixed silently.** Anything the tests or the fuzz targets show beyond F-1 to
   F-21 is written down as F-22 and following (what, which row of §5.2, how found), raised to the
   orchestrator, and fixed in its own commit with its own test or deferred with a target. It goes
   into the record (E2).
5. **Do not weaken** a check, a trigger or an existing test to make a new one pass. The changed
   tests are those of §9 only. Do not delete a row of the security model.
6. **Secrets.** No real token, key, password or signing secret in a test, a seed, a corpus file, a
   fixture or a document. Seeds use values generated at run time or the synthetic fixtures. A file
   written by the fuzzer under `testdata/fuzz/` is read before it is committed. Logs, metrics and
   spans gain nothing in this sprint except the value `member_case` of `invalid_reason`.
7. A request body is never re-serialized (§5.5 looks at names only). An installation without a key
   service, and a request without a member in another case, must run the code they ran before.
8. Tests hold for root and non-root; nothing sleeps longer than 2 s; waiting is polling with a
   deadline. Fuzz targets need no database, network or container.
9. Documents are in English, in the style of the existing ones, and name people by role
   ("the maintainer"), never a tool.

## Environment

- Go as in `go.mod` (`go 1.26.0`); the release toolchain is `.go-version`; the floor used by CI is
  the `go-version` of the `test` job's `floor` leg in `ci.yml`.
- The test database: `make db-up`, then `set -a; . local/testdb/env; set +a`.
- `make check` before every push of part F. `make fuzz FUZZTIME=10s` is what CI will run.
- The index of tests used by E1 and by the repository tests:
  `grep -rn '^func \(Test\|Fuzz\)' --include=*_test.go internal test cmd`.

## Part A — Scaffolding (commit: `chore: sprint 12.2 scaffolding: read sets, pure parsers, option fields, repository test helpers`)

Everything compiles and every existing test passes unchanged.

### A1. `internal/family`

```go
// jsonbody.go
// CaseConflict reports whether ms holds a member whose name equals one of read when case is
// ignored and is not spelled exactly so.
func CaseConflict(ms []Member, read ...string) bool { return false } // C1
```

In `internal/family/openai/request.go` and `internal/family/anthropic/request.go` (and the file of
each that holds the output bound): exported `var ReadMembers = []string{…}` and, for `openai`,
`ReadStreamOptions`. Fill them **now**, from the string literals the package passes to
`family.ParseModel` (which reads `model`), `family.LastMember` and `family.PositiveInt`:
`grep -n 'LastMember\|PositiveInt\|ParseModel' internal/family/openai/*.go internal/family/anthropic/*.go`.
Replace each literal at those call sites by an unexported constant used in both the slice and the
call, so the two cannot drift. No behaviour changes.

### A2. Pure parsers (statements moved, not rewritten)

| New function | From | Moves |
|---|---|---|
| `jwks.parseMetadata(body []byte, issuer string) (Metadata, error)` | `(*Cache).fetchMetadata` in `metadata.go` | from `var doc struct` to the final `return` |
| `jwks.parseDiscovery(…)` | `(*Cache).discover` in `fetch.go` | the decoding of the discovery document and its checks, with the parameters those statements need |
| `oidc.parseTokenAnswer(body []byte) (idToken string, ok bool)` | `(*Provider).redeem` in `flow.go` | from `var answer struct` to the check of `answer.IDToken` |

The callers call them and map the result exactly as before. Verify: `go test ./internal/jwks/
./internal/oidc/ -count=1` passes with no test changed.

### A3. Option fields

- `internal/config/config.go`: `VaultKEKTransit.AllowExportableKey bool` with the tag
  `yaml:"allow_exportable_key"`. No entry in `keys.go` yet (C4).
- `internal/vault/transit.go`: `TransitOptions.AllowExportable bool`; unused yet.

### A4. Repository test helpers — `test/repo/threatmodel_test.go`, `test/repo/supplychain_test.go`

Helpers only, no test function yet:

```go
// mdTable returns the rows of the first Markdown table of text whose header row is exactly header
// (cells trimmed), without the header and the separator; nil when there is none.
func mdTable(text string, header ...string) [][]string
// backticked returns the texts between back quotes in a cell.
func backticked(cell string) []string
// testFunctions maps "package.Function" to the files that declare a top-level Test… or Fuzz…
// function of that name, for every _test.go file under internal, test and cmd; the package is the
// last element of the directory, "main" for cmd/pandokos. Build tags are ignored (go/parser).
func testFunctions(t *testing.T) map[string][]string
// composeImages returns, per Compose file tracked by git, its image: values.
func composeImages(t *testing.T) map[string][]string
```

`testFunctions` follows what T-R30 (`test/repo/usage_test.go`) does with `go/parser`; reuse `read`,
`root` and `workflows`.

Verify part A: `go build ./... && go vet ./... && go test ./internal/family/... ./internal/jwks/ ./internal/oidc/ ./internal/vault/ ./internal/config/ ./test/repo/ -count=1`.

## Part B — Tests first (commit: `test: sprint 12.2 tests: findings of the review, fuzz targets, repository tests`)

Write every test of §9 "New tests" under its name, in the file given, and change the tests of §9
"Changed tests". Then run them and note the state of each in the commit body.

| Tests | File | Expected before part C/D/E |
|---|---|---|
| T-1095 | `internal/family/jsonbody_test.go` | fails |
| T-1096, T-1097 | `internal/family/openai/request_test.go`, `internal/family/anthropic/request_test.go` | fail |
| T-1098 | `internal/gateway/pipeline_test.go` (next to `TestInvalidRequest`) | fails |
| T-1099 | `internal/egress/classify_test.go` | **written with the confirmed addresses only**: do C2's check first; fails |
| T-1100 | `internal/vault/transit_test.go` | fails |
| T-1101 | `internal/config/vault_test.go` (where `TestTransitKeys` is) | fails |
| T-1102 | `test/e2e/transit_test.go` | fails |
| T-1103, T-1104 | `internal/server/strict_test.go` | **pass** (they pin what exists): see C3 |
| T-1105 to T-1118 | the package's test file, or a new `fuzz_test.go` | **pass** on their seeds |
| T-R89, T-R90, T-R91 | `test/repo/threatmodel_test.go` | fail until E |
| T-R92 to T-R97 | `test/repo/supplychain_test.go` | fail until D (T-R95 also needs E) |

**Fuzz targets** (§5.9 gives parser, invariants and seeds for each):

- Signature `func FuzzX(f *testing.F)`; seeds with `f.Add`; the body bounds its input at 1 MiB
  (`t.Skip()` above) as the existing stream targets do.
- T-1105 takes `(data []byte, op uint8)`; `op` selects the operation and with it the destination
  type the client decodes into. Call the same unexported functions the client calls
  (`decodeEnvelope`, then what `Wrap`, `Unwrap`, `describe`, `identify`, `lookup`, `renew` and
  `login` do with the decoded value). If that logic is inside methods that also send, move it to
  unexported functions in `transit_client.go` in this commit, statements unchanged, and say so in
  the commit body.
- T-1111: three operations whose request types cover a member that carries a secret, a `Nullable`
  member and a nested object; pick them from `adminapi.gen.go` and name them in the test's comment.
- "No substring of the input longer than 8 bytes in the error": implement once as a helper in each
  package that needs it (`vault`, `adminapi`, `outbox`), skipping inputs shorter than 9 bytes and
  substrings that are part of the fixed text itself.
- After writing each target run it for 60 s:
  `go test ./<pkg>/ -run '^$' -fuzz '^FuzzX$' -fuzztime 60s`. **If it fails, do not adjust the
  invariant to make it pass**: decide whether the invariant or the code is wrong; a wrong code is a
  finding (rule 4) and a step C5.

**Changed invariants**: `openai.FuzzParseRequest` and `anthropic.FuzzAnthropicParseRequest` get the
last row of the invariant table of §5.9 and seeds with members in another case; they fail until C1.

**Repository tests**, rules in §5.4 (T-R89, T-R90), §5.9 and §5.10 (T-R91, T-R92) and §9 (T-R93 to
T-R97). Notes:

- T-R89 reads the two tables by header (`mdTable`), never by position; an identifier is
  `T[BM]-\d+`; "consecutive" means `TB-1`…`TB-n` and `TM-01`…`TM-nn` without a hole. The three
  statements `TS-1` to `TS-3` are a third table of the same shape under the boundaries.
- T-R90 reads `docs/architecture/threat-model-review-*.md`, the lexically greatest being the newest.
- T-R93: the two allowed exceptions are the literal values `${PANDOKOS_IMAGE:-ghcr.io/pandokos/pandokos:<minor>}`
  (matched by a pattern on the variable name) and `${PANDOKOS_TEST_IMAGE:?}`.
- T-R96 extracts the three patterns from `redact.go` by the names of their variables
  (`keyPattern`, `sessionPattern`, `whsecPattern`) and compares them to the `regex` of the rules
  `pandokos-key`, `pandokos-session`, `pandokos-signing-secret`.
- Before changing T-R39 or any test that reads the model: `grep -rn 'security-model' test/`.

Verify part B: `go vet ./... && go test ./... -count=1 2>&1 | tail -40` shows exactly the failures
the table expects (list them in the commit body), and `make fuzz FUZZTIME=10s` runs after adding the
fourteen lines of D1 to the Makefile in this commit (T-R30 requires every fuzz function to be
listed).

## Part C — Implementation

### C1. Members in another case — `fix(gateway): refuse a request member that differs only in case from one Pandokos reads`

**Files:** `internal/family/jsonbody.go`, `internal/family/openai/request.go`,
`internal/family/anthropic/request.go`, the gateway guide (one value in the list of
`invalid_reason`; the rest of the documents is part E).

- `CaseConflict`: for each member, for each name of `read`: `m.Key != name &&
  strings.EqualFold(m.Key, name)`.
- Each parser calls it on the top-level members with its `ReadMembers` right after `ScanObject`
  succeeds and before `ParseModel`, and returns `family.Invalid("member_case")`; the `openai` parser
  also on the members of `stream_options`, when that member is an object, with `ReadStreamOptions`.
- Nothing else changes: `ParseModel` keeps `model_missing`, `model_duplicate`, `model_not_string`.

**Tests:** T-1095 to T-1098, the two changed fuzz targets, T-421, `family.TestInvalidReason`, the
gateway tests that list invalid reasons.
**Verify:** `go test ./internal/family/... ./internal/gateway/ -count=1 && go test ./internal/family/openai/ -run '^$' -fuzz '^FuzzParseRequest$' -fuzztime 60s && go test ./internal/family/anthropic/ -run '^$' -fuzz '^FuzzAnthropicParseRequest$' -fuzztime 60s`.

### C2. Metadata addresses — `fix(egress): forbid the remaining instance-metadata addresses for every class`

**Check first** (rule 3). For each candidate, find the address in the vendor's current
documentation and write the page title, its URL and the date read into your notes for ADR 0074:

| Candidate | Where the vendor documents it |
|---|---|
| The EKS Pod Identity agent, IPv4 (an address in `169.254.170.0/24` other than the one already in the table) and IPv6 (an address in `fd00:ec2::/32` other than the one already in the table) | Amazon EKS user guide, the pages on EKS Pod Identity and on its agent |
| The Google Cloud metadata server, IPv6 | Compute Engine documentation, the page on accessing VM metadata (the IPv6 endpoint of the metadata server) |

Add a candidate only when the documentation states the address. Also read the same pages of Azure,
Oracle Cloud, DigitalOcean, Hetzner and OpenStack for a metadata address that is not
`169.254.169.254`; one that is documented and missing is added the same way and listed in the
commit body. A candidate that cannot be confirmed is not added and is named in the record as not
confirmed.

**Files:** `internal/egress/classify.go` (`forbiddenPrefixes`, each as a single-address prefix,
with a comment naming the service), the tables of `egress.TestClassify`, `outbox.TestValidateURL`
and `jwks.TestTransportClass`.
**Tests:** T-1099, T-1115.
**Verify:** `go test ./internal/egress/ ./internal/outbox/ ./internal/jwks/ ./internal/gateway/ -count=1 && go test ./internal/egress/ -run '^$' -fuzz '^FuzzClassify$' -fuzztime 60s`.

### C3. The listeners — `test(server): what the listeners refuse: framing and header limits`

No production code. Run T-1103 and T-1104 on both toolchains:

```
go test ./internal/server/ -run 'TestStrictParsing|TestHeaderLimits' -count=1
GOTOOLCHAIN=go<floor> go test ./internal/server/ -run 'TestStrictParsing|TestHeaderLimits' -count=1
```

`<floor>` is the `go-version` of the `floor` leg of the `test` job in `ci.yml` (`1.26.11` on
2026-11-24). Fix the expected answer of each case of T-1103 from these runs and write it in the table of the
test. **Stop and ask** if: the two toolchains differ on a case; a case reaches the handler with a
body other than the one RFC 9112 gives; a second request hidden in a body reaches the handler; an
oversized header block is accepted. Those are findings (rule 4), and the fix is then a decision,
not an adjustment of the test.

### C4. An exportable key — `feat(vault): refuse an exportable key of the key service unless allowed`

Only if O-1 is confirmed by the maintainer; otherwise skip this step, delete T-1100 to T-1102 and
the changes to the tests of 12.1, and E2 records F-8 as accepted.

**Files:** `internal/vault/transit_client.go` (`checkKey`, `weakSettings`), `internal/vault/transit.go`
(a fixed error next to `errTransitType`), `internal/config/keys.go` and `transit.go`,
`cmd/pandokos/vaultstart.go` (pass the option), `cmd/pandokos/kek.go` if it builds options itself.

- `keys.go`: `serveDef("vault.kek.transit.allow_exportable_key", "boolean", "false", "Accept a key
  of the key service that was created exportable; without it such a key is refused at start", …)`
  placed with the other transit keys; environment name by the rule of the table.
- `checkKey`: after the existing cases, `k.Exportable && !p.opts.AllowExportable` returns the new
  fixed error. Its text names the setting `exportable` and the key
  `vault.kek.transit.allow_exportable_key`, and nothing of the answer, the address or the key name.
  It is a permanent cause in the sense of 12.1 §5.10 (same class as `errTransitType`): follow that
  error through `Start`, `serve` and `kek check` and give the new one the same treatment.
- `weakSettings` is unchanged: with the option, `exportable` is still reported.

**Tests:** T-1100, T-1101, T-1102; changed T-1060, T-1078, T-1080, T-1085, T-R86 (fourteen keys:
`grep -rn -i 'thirteen' docs internal test cmd` finds the places that count them).
**Verify:** `go test ./internal/vault/... ./internal/config/ ./cmd/pandokos/ ./test/transit/ -count=1 && go test ./test/e2e/ -run 'Transit' -count=1`; with Docker, `make transit-up VENDOR=openbao && make test-transit && make transit-down`, then the same with `VENDOR=vault` (the keys the suite creates are not exportable; nothing should change).

### C5. Findings of the fuzz targets — one commit each, `fix(<package>): <what>`

Only if part B found something (rule 4). Each: the minimal fix, the failing input committed under
`testdata/fuzz/<Target>/` of the package after reading it (rule 6), a line for the record (E2), and
`go test ./<pkg>/ -count=1` plus 60 s of the target.

## Part D — CI, pins and repository tests (commit: `ci: sprint 12.2 fuzzing on every pull request and nightly, scheduled vulnerability checks, the published digest scanned`)

### D1. `Makefile`

Fourteen lines appended to `fuzz`, in the exact form of the existing ones (T-R30 parses them):

```
	go test ./internal/vault/ -run '^$$' -fuzz '^FuzzTransitAnswer$$' -fuzztime $(FUZZTIME)
	go test ./internal/jwks/ -run '^$$' -fuzz '^FuzzParseKeySet$$' -fuzztime $(FUZZTIME)
	go test ./internal/jwks/ -run '^$$' -fuzz '^FuzzMetadata$$' -fuzztime $(FUZZTIME)
	go test ./internal/oidc/ -run '^$$' -fuzz '^FuzzProviderAnswers$$' -fuzztime $(FUZZTIME)
	go test ./internal/pricing/genaiprices/ -run '^$$' -fuzz '^FuzzParse$$' -fuzztime $(FUZZTIME)
	go test ./internal/admin/ -run '^$$' -fuzz '^FuzzDecodeCursor$$' -fuzztime $(FUZZTIME)
	go test ./internal/adminapi/ -run '^$$' -fuzz '^FuzzDecodeBody$$' -fuzztime $(FUZZTIME)
	go test ./internal/adminapi/ -run '^$$' -fuzz '^FuzzMatch$$' -fuzztime $(FUZZTIME)
	go test ./internal/admin/ -run '^$$' -fuzz '^FuzzOrigin$$' -fuzztime $(FUZZTIME)
	go test ./internal/outbox/ -run '^$$' -fuzz '^FuzzValidateURL$$' -fuzztime $(FUZZTIME)
	go test ./internal/egress/ -run '^$$' -fuzz '^FuzzClassify$$' -fuzztime $(FUZZTIME)
	go test ./internal/auditchain/ -run '^$$' -fuzz '^FuzzCanonical$$' -fuzztime $(FUZZTIME)
	go test ./internal/family/openai/ -run '^$$' -fuzz '^FuzzInboundCredential$$' -fuzztime $(FUZZTIME)
	go test ./internal/family/anthropic/ -run '^$$' -fuzz '^FuzzInboundCredential$$' -fuzztime $(FUZZTIME)
```

`internal/admin` tests that need the database must not be pulled in by `-run '^$$'`; the two admin
targets must not need it (if the package's `TestMain` requires it, put the two targets where they
can run without, and say so). `FUZZTIME ?= 30s` stays the default.

### D2. `.github/workflows/ci.yml` and `.github/workflows/scheduled.yml`

`ci.yml`, a job after `vuln`, the two `uses:` lines copied from the `vuln` job:

```yaml
  fuzz:
    runs-on: ubuntu-24.04
    timeout-minutes: 30
    steps:
      - uses: actions/checkout@<as in the vuln job> # <its comment>
        with:
          persist-credentials: false
      - uses: actions/setup-go@<as in the vuln job> # <its comment>
        with:
          go-version-file: .go-version
      - run: make fuzz FUZZTIME=10s
```

`scheduled.yml`, new; `uses:` lines copied from `ci.yml` (`checkout`, `setup-go` from `vuln`;
`anchore/scan-action` and its `with:` from the `image` job):

```yaml
name: scheduled

on:
  schedule:
    - cron: "23 3 * * *"
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: scheduled
  cancel-in-progress: false

jobs:
  fuzz:
    if: github.repository == 'pandokos/pandokos'
    runs-on: ubuntu-24.04
    timeout-minutes: 180
    steps:
      # checkout (persist-credentials: false), setup-go (.go-version)
      - run: make fuzz FUZZTIME=3m

  vuln:
    if: github.repository == 'pandokos/pandokos'
    runs-on: ubuntu-24.04
    steps:
      # checkout, setup-go
      - run: make vuln

  image:
    if: github.repository == 'pandokos/pandokos'
    runs-on: ubuntu-24.04
    steps:
      - name: Resolve the released image
        id: img
        run: |
          set -eu
          docker pull ghcr.io/pandokos/pandokos:latest
          ref=$(docker image inspect --format '{{index .RepoDigests 0}}' ghcr.io/pandokos/pandokos:latest)
          echo "$ref"
          echo "ref=$ref" >> "$GITHUB_OUTPUT"
      # anchore/scan-action as in ci.yml, with image: ${{ steps.img.outputs.ref }} and the same
      # fail-build, severity-cutoff, only-fixed and output-format

  settings:
    if: github.repository == 'pandokos/pandokos'
    runs-on: ubuntu-24.04
    env:
      GH_TOKEN: ${{ github.token }}
    steps:
      - name: Rulesets are active
        run: |
          set -eu
          gh api "repos/$GITHUB_REPOSITORY/rulesets" --jq '.[] | "\(.target) \(.enforcement) \(.name)"' | tee rulesets.txt
          grep -q '^branch active ' rulesets.txt
          grep -q '^tag active ' rulesets.txt
```

`make vuln` uses `GOTOOLCHAIN=go$(RELEASE_GO)`; the workflow has no `GOTOOLCHAIN: local`, so it
works as it does locally. The `settings` job: on the first dispatch after the merge (F3), see
whether the job's token can read the rulesets. If it cannot (403 or an empty list while F4 shows
two), remove the job, add the same two commands to the release check list of CONTRIBUTING
("Releasing"), set the constant of T-R97 that says so, and record it in E2. Do not give the job a
personal token.

### D3. `.github/workflows/release.yml`

- `check-tag`, in the step `v`, after the format check and with `GH_TOKEN: ${{ github.token }}` in
  its `env`:

  ```sh
  kind=$(gh api "repos/$GITHUB_REPOSITORY/git/ref/tags/$TAG" --jq .object.type)
  if [ "$kind" != tag ]; then echo "the tag is not an annotated tag: $TAG"; exit 1; fi
  ```

  The API is used, not `git cat-file`, because the checkout of a tag push can leave a lightweight
  ref locally. Checked on 2026-11-24: the same call for `v0.2.0` answers `tag`.
- `verify`, after "Verify the build provenance": a step `Scan the published image` with
  `anchore/scan-action` copied from `ci.yml` (same pin, same thresholds), `image:
  ${{ env.IMAGE }}@${{ env.DIGEST }}`. The job is logged in to the registry already.
- Nothing else changes; `publish` keeps `needs: [check-tag, image, verify]`.

### D4. `.github/dependabot.yml`, `.gitleaks.toml`

- `dependabot.yml`: an entry `package-ecosystem: docker-compose`, `directory: /test/transit`,
  weekly, after `/test/idp`.
- `.gitleaks.toml`: two rules after `pandokos-key`, with the patterns of
  `internal/telemetry/redact.go` character for character:

  ```toml
  [[rules]]
  id = "pandokos-session"
  description = "Pandokos session token"
  regex = '''<sessionPattern>'''
  keywords = ["pds_"]

  [[rules]]
  id = "pandokos-signing-secret"
  description = "Pandokos webhook signing secret"
  regex = '''<whsecPattern>'''
  keywords = ["whsec_"]
  ```

  Then `make secrets` (the whole history). A hit is examined first: a real value is a finding to
  raise at once (rule 4) and is never allow-listed; a synthetic one gets an allow-list entry limited
  to its file and line, in the form of the existing entries, with the commit named. Also run
  `go run <the value of GITLEAKS in the Makefile> dir --config .gitleaks.toml --redact --no-banner planning docs`
  so that the documents of this sprint do not trip the generic rules; a false positive there gets
  the same kind of narrow entry.

### D5. Repository tests

T-R92 to T-R97 pass (T-R95 after E4). `repo.TestActionsPinned` and `repo.TestReleaseWorkflowShape`
pass unchanged with the new file and step.

Verify part D: `go test ./test/repo/ -count=1` (T-R89 to T-R91 and T-R95 still fail until E),
`make secrets`, `make fuzz FUZZTIME=10s` and its wall time noted in the commit body; if it exceeds
20 minutes on your machine, raise it before pushing (R-5).

## Part E — Documentation and decision records (commit: `docs: sprint 12.2 threat model reviewed against the code: identifiers, proofs, the record, ADR 0074`)

### E1. `docs/architecture/security-model.md`

Shape in §5.4. Procedure for the `Code` and `Proof` columns, **for every row of §5.2**:

1. Open the control's file and confirm the function is there; write one to three paths (a file
   where §5.2 names one, the package directory otherwise).
2. Open each test §5.2 names and read what it asserts. Keep it if it would fail when the control is
   removed. If it would not: find the test that does (the index of the Environment section, the
   test tables of the sprint designs under `docs/history`), or stop and raise it as a finding
   "control without test" (rule 4). Do not keep a name because it sounds right.
3. For a row marked A in §5.2, the `Controls` text keeps or gains the residual exposure in words;
   the proof is still a test of the control that exists.

Text changes: the status line; TM-04 ("mutual TLS (IAM-15, IAM-16) would bind a token to its
sender and is not built"); TM-20 (F-7); TM-39 and TM-40 (the metadata addresses, by service name);
TM-41 (members in another case, the reason `member_case`); TM-51 rewritten from §5.11 as it stands
after part D; the three new rows TM-58 to TM-60; boundary 4 (an exportable key is refused unless
allowed); the paragraph "What Pandokos does not protect against" where it says "a policy that
cannot export the key" gains "and a key that is not exportable, which Pandokos requires by
default"; the section "Parsers that read from the network" (§5.9, three columns); the section
"Reviews". Everything else in the document stays word for word.

### E2. `docs/architecture/threat-model-review-2026-11.md`

Contents in §5.4, items 1 to 6. Sources: §5.1 (method), §5.2 (statuses, updated to what the sprint
did: G becomes `fixed`, U becomes `test added`, W becomes `proved (wording corrected)`), §5.3
(findings and decisions, with F-22 and following if any), §5.11 (the supply-chain table with its
"after" column), §12 "Not verified" updated with what part E1 and part C did verify. The settings
read on 2026-11-24 (F-17) are stated with their date; the answers of F4 are added at the close.
Leave the cells "accepted by the maintainer on" with the text `pending (plan F5)`: T-R90 treats that
exact text as empty, so the test stays red until F5.

### E3. Decision records

`docs/adr/0074-threat-model-review-and-continuous-fuzzing.md`, from `0000-template.md`, status
`proposed` (the maintainer accepts it), date of the commit. Content: context (SEC-05 reviewed; the
epic's risk); decisions D-1 to D-12 of §8 in the record's own words; the metadata addresses added
with source and date (C2); consequences (§11, first bullet); alternatives considered: a fuzzing
service or action (D-8, O-4), verifying by tag, forbidding all link-local addresses (O-3), keeping
`exportable` a warning (O-1), a traceability document separate from the model.

Amendments, each as the repository does it (`test/repo/adr_test.go`, `TestAmendedRecords`, shows
the form: read it first): ADR 0018 (no binaries are released; verification by digest; the published
digest is scanned; nightly checks; fuzzing in CI), ADR 0037 (D-2), ADR 0038 (D-3), ADR 0073 (D-5;
the list of key requirements and of warnings). A history line dated with the commit on each of the
four. `docs/adr/README.md`: the index line for 0074, and row 22 of the table of open questions
names the review record beside the security model.

### E4. Guides and references

| Document | Change |
|---|---|
| `docs/operations/container.md` | The opening paragraph and "Limits of this pre-release" say what the image is today (tenants, keys, TLS on both listeners, what is not built) without the stale sentences of F-10; "What Pandokos changes": the forwarded request headers are the allow-list of each family (`RequestHeaders` of both `route.go`). "Verifying a release": take the digest from the release page (`Image: ghcr.io/pandokos/pandokos@sha256:…`), or resolve it with `docker buildx imagetools inspect ghcr.io/pandokos/pandokos:<version> --format '{{.Manifest.Digest}}'` and compare it with the release page; the three commands on `ghcr.io/pandokos/pandokos@sha256:<digest>`; run by digest in production; the attested SBOM is the authoritative one and how to print it from `cosign verify-attestation` (F-19); what the pipeline itself verified before publishing, the scan included |
| `docs/operations/gateway.md` | Section 3 or 4: the rule of §5.5 in two sentences and the value `member_case` in the list of `invalid_reason` (section 10); section 6: the added forbidden addresses |
| `docs/operations/external-key.md` | The section on the key: an exportable key is refused unless `allow_exportable_key`; the four other settings warn; leaving an exportable key (set the option for the move); the output of `kek check` |
| `docs/operations/configuration.md` | The key and its environment name (generated or hand-kept as the page is: follow `repo.TestConfigurationReference`) |
| `docs/operations/compose.md` | One sentence: the reference follows the minor tag on purpose; production pins the digest of the release page (F-18) |
| `docs/operations/events.md`, `docs/operations/providers.md` | Only if they list forbidden addresses: the same additions |
| `docs/reference/failure-behaviour.md` | Section 2a: the row of start refusals gains an exportable key, citing T-1102 |
| `docs/reference/logs.md` | Only if it lists the values of `invalid_reason` |
| `CONTRIBUTING.md`, "Releasing" | Two lines of the check list: the last run of the `scheduled` workflow is green; a review record of the threat model exists for the release (a short one when nothing changed) |
| `SECURITY.md` | One sentence under "Reporting": a crash found by the project's own fuzzing that a caller or a provider can cause is handled as a report |
| `docs/releases/v0.3.0-draft.md` | The three behaviour changes of §11 "Release v0.3.0"; the new key; fuzzing and the nightly checks in one line |
| `docs/requirements/README.md` | SEC-05: if the page marks delivery per release anywhere, mark "reviewed" as delivered in R3 by this sprint; otherwise no change |
| `docs/architecture/overview.md` | Only where it names the security model's status or the supply chain: align the wording |

### E5. Planning files

`planning/epics/12-operability/epic.md` (a line under 12.2, "Deferred from 12.2": F-20 and O-3 to
sprint 10.2; O-2 and O-4 as open), `planning/epics/10-hardening/epic.md` (the assessment takes the
review record as input; F-20, O-3 and the pending-password gap listed), `planning/roadmap.md` (the
row of 12.2 at the close, by the orchestrator). The front matter `status:` of sprint.md is changed
by the orchestrator, not by this part.

Verify part E: `go test ./test/repo/ -count=1` (everything but the acceptances of T-R90),
`make generate-check`, links: `grep -rn '](' docs/architecture/threat-model-review-2026-11.md
docs/adr/0074-*.md | grep -v http` and open each target.

## Part F — Verification (no commit unless something is found)

- **F1.** `make check`; `go test -tags container` and `-tags compose` run in CI only.
- **F2.** `make fuzz FUZZTIME=3m` once, locally (about 100 minutes). Anything found: rule 4, C5.
- **F3.** Push; the pull request's `fuzz` job is green and its duration is noted in the pull
  request. Before the merge, the commands of the scheduled jobs are run locally: `make vuln`; the
  image scan with the released image if a scanner is at hand (otherwise noted as not run); the two
  `gh api` commands of the `settings` job. A scheduled workflow can be dispatched only once it is
  on the default branch: **after the merge** the maintainer runs `gh workflow run scheduled.yml` and
  the result goes into the record (a follow-up commit on `main` if a line must change).
- **F4. Maintainer actions** (outside the repository; the summary of each answer and the date go
  into the record, section 5):

  ```
  gh api repos/pandokos/pandokos/rulesets --jq '.[] | {name, target, enforcement}'
  gh api repos/pandokos/pandokos/rulesets/<id>           # for each: rules, conditions, bypass actors
  gh api repos/pandokos/pandokos/branches/main/protection
  gh api repos/pandokos/pandokos --jq .security_and_analysis
  gh api repos/pandokos/pandokos/private-vulnerability-reporting
  gh api repos/pandokos/pandokos/actions/permissions/workflow
  gh api orgs/pandokos/packages/container/pandokos --jq '{visibility, updated_at}'
  ```

  Decide O-9: enable the two rulesets (and add the `fuzz` job to the required checks after a week
  of green runs, O-6), enable Dependabot security updates. Verify the newest release by digest with
  the guide as rewritten in E4, and note the digest and the date. Decide O-1 to O-8.
- **F5. Close the record.** The maintainer's acceptance of each residual exposure and of each
  finding decided `accepted`, with the date, replaces `pending (plan F5)`; T-R90 passes. Commit:
  `docs: sprint 12.2 review record: maintainer decisions`.
- **F6. Review checklist** for the agent that reviews the sprint:
  1. Every test of §9 exists under its name:
     `grep -rhoE 'func (Test|Fuzz)[A-Za-z0-9_]+' cmd internal test | sort` against the tables.
  2. Ten rows of the model drawn at random, plus TB-4, TM-39, TM-41, TM-44 and TM-51: the code is
     where the row says and each proof fails when the control is removed (reason about it from the
     test's assertions; do not edit the code).
  3. `git diff` of `security-model.md`: no row lost, no control text changed beyond E1's list.
  4. No seed, corpus file or document holds a secret; `make secrets` is green.
  5. The workflows: every `uses:` pinned as before, no permission added beyond `contents: read` in
     the two files touched except what `release.yml` already had, no `continue-on-error`.
  6. Each finding F-1 to F-21 (and any later) has its outcome in the record, and the record's
     statuses match the model.

## What the implementer does not do

- Accept a residual exposure, change a repository setting on GitHub, or enable a ruleset: those are
  the maintainer's (F4, F5).
- Add a threat, a control or a refusal beyond §5: a finding is raised (rule 4).
- Change the message or the status of `invalid_request`, the classes of the egress guard, the
  warnings of the key service other than `exportable`, or the policy file.
- Add a fuzzing service, a new action, a cache of generated corpora, or an upload of failing inputs.
- Verify the tag's signature in the workflow (O-2) or forbid link-local addresses (O-3).
- Push a tag, cut a release, or change `status:` of the planning files.
