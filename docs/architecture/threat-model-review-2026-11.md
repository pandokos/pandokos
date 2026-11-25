# Threat model review, November 2026

- Date: 2026-11-25
- Reviewed commit: `96cb793c0359b339c4c98785c29a0776e2dada0f`
- Closing commit: `f4414d9f0388a51d01d97cac60864d1ecf9231ba`
- Reviewed by: the maintainers who wrote the code, by reading each control and opening each test named as its proof, in sprint 12.2; no independent reviewer. The independent assessment is sprint 10.2 and takes this record as its map.
- Risk: high. A flaw in a control that keeps a tenant from reading or using what is not theirs, or a caller from reaching what is not served, is found by an attacker and not by a test that nobody wrote; and the reviewers share the blind spots of the authors.

This is the record the [security model](security-model.md) links in its status line. It states the status of every
identifier of the model after the sprint, the 21 findings with their decisions, the residual exposures with
the date each was accepted, the supply chain as checked, and what was not reviewed. The decisions that are
design decisions are in [ADR 0074](../adr/0074-threat-model-review-and-continuous-fuzzing.md).

The "Closing commit" is the commit at which the sprint's changes were complete when this file was written; it
is updated when the sprint is delivered.

## Method

1. The model was read row by row: the 8 trust boundaries, the 3 statements outside the list (the capability
   check, the two functions that cross the tenant boundary, the Compose reference) and the 57 threats. Each got a
   permanent identifier, `TB-1` to `TB-8`, `TS-1` to `TS-3` and `TM-01` to `TM-57`, in the order of the document.
   Three rows were added, `TM-58` to `TM-60`, for threats that the code answers and the model did not name.
2. For each row the code that implements the control was located (a file where one is the control, the package
   otherwise), and the test that asserts it was looked for in the index of the repository's test functions and in the
   test tables of the sprint designs.
3. Where a control is security-critical and small, the function was read and not only located: the body parser of
   the families, the address classes and the dial control of the guard, the key-service transport, the transit
   client's exchange and decoding, the key requirements, the key-set parser of `internal/jwks`, the token exchange of
   `internal/oidc`, the webhook URL validation, the cursor decoder of the admin API, the header allow-lists of
   both families, the listener limits of `internal/server`, the redaction patterns and the pipeline (`ci.yml`,
   `release.yml`, `dependabot.yml`, `Dockerfile`, `Makefile`, `.gitleaks.toml`).
4. Each test named as a proof was opened and its assertions read, and kept only if it would fail when the control is
   removed. A name that did not assert the control was replaced by the test that does: the review replaced the
   proof of three rows, added the test that proves the control to two more, and added tests to others for what the sprint changed (listed below). No row is left without a test.
5. A row is **proved** when the control was located and a test asserts it; **proved (wording corrected)** when the row
   said something the code does not do; **test added** when the control existed and no test would have failed if it
   had disappeared; **fixed** when the threat was not answered as the row said. A residual exposure that the
   maintainer accepts is added to the status.

What this method does not give: a test that is named as a proof is held by `TestThreatModelTraceability` to exist
and to be unique, not to assert what the row says; the opening of each test is a reading, and the next review reads
them again. The review is not a penetration test, and it is not independent (see "Not reviewed").

Proofs the review changed from what the first reading of the model suggested:

| Row | What the review found | What it did |
|---|---|---|
| TM-23 | `e2e.TestVault` asserts the output of the command line, not that the database holds only ciphertext | Added `vault.TestSealOpen`, which asserts the sealed value holds no plaintext |
| TM-28 | `isolation.TestCredentialGuards` tests the lifecycle triggers, not that a ciphertext does not open on another row | Replaced by `vault.TestSealNamed`; `vault.TestSealOpen` binds the tenant, the credential and the key version |
| TM-40 | `egress.TestProxy` tests that the proxy applies to the public transport only | Replaced by `egress.TestClassifyMetadata` and `outbox.TestValidateURL` |
| TB-5 | `repo.TestTransitClientDependencies` is about the key service's client, not the provider | Replaced by `repo.TestOutboundHTTPInOnePlace`, which also forbids switching certificate verification off |
| TM-05 | `isolation.TestIssuerTables` asserts row-level security, not the uniqueness of the pair | Added `store.TestIssuerSchema`, which asserts the pair is unique |

Tests added to a row for what the sprint changed: `family.TestCaseConflict`, `openai.TestParseMemberCase`,
`anthropic.TestParseMemberCase` and `gateway.TestMemberCaseRefused` (TM-41, TM-48), `egress.TestClassifyMetadata`
(TM-39, TM-40), `vault.TestTransitExportable` (TB-4), `server.TestStrictParsing` (TM-44), `server.TestHeaderLimits`
(TM-49), and the fuzz targets of TM-42, TM-50, TM-52, TM-54, TM-58 and TM-59.

## Findings

Decisions are `fixed`, `test added`, `wording`, `accepted`, `deferred` or `maintainer action`. "Shown by" is the test
or the document that a reader opens to see the decision hold. "Accepted by" is the maintainer's acceptance, with its
date: the maintainer accepted the residual exposures as restated, F-18, F-19 and F-21 among them, on 2026-11-24
(open point O-8 of the sprint, and O-4 for F-21).

| Finding | Decision | Shown by | Deferred to | Accepted by | What was found |
|---|---|---|---|---|---|
| F-1 | fixed | `family.TestCaseConflict`, `openai.TestParseMemberCase`, `anthropic.TestParseMemberCase`, `gateway.TestMemberCaseRefused` | - | - | `ParseModel` and `LastMember` compare member names exactly; a provider whose JSON decoder ignores case reads `Model` beside `model`, or `Stream` beside `stream`. A member that differs only in case from one Pandokos reads is now refused (`member_case`); TM-41, TM-48 |
| F-2 | fixed | `egress.TestClassifyMetadata`, `outbox.TestValidateURL` | - | - | The forbidden table lacked the EKS Pod Identity agent (IPv4 and IPv6), the Google Compute Engine metadata server over IPv6 and the OpenStack metadata service over IPv6; added after the vendors' documentation (ADR 0074); TM-39, TM-40, TM-16 |
| F-3 | fixed | `repo.TestSecretShapesAreScanned` | - | - | The log redaction knows three secret shapes that Pandokos issues; the secret scan knew one. Two rules added to `.gitleaks.toml`; TM-10, TM-17 |
| F-4 | test added | `server.TestStrictParsing` | - | - | "Strict parsing by the Go HTTP stack" was exercised by no test of ours; it now is, on both listeners, and holds on the three toolchains tried (go 1.26.0, 1.26.11 and 1.27.4); TM-44 |
| F-5 | test added | `server.TestHeaderLimits` | - | - | `max_header_bytes` and `read_header_timeout` were validated as configuration and never exercised on a listener; TM-49 |
| F-6 | wording | The row TM-04 of the security model | - | - | The row named mutual TLS (IAM-15, IAM-16) as a control; neither is built. The row now says so |
| F-7 | wording | The row TM-20 of the security model | - | - | "No API response carries a credential a script could keep" is not true of the responses that show a key, a password or a signing secret once. The row now separates the session token, which no response body holds, from a secret shown once, which is the client's to drop |
| F-8 | fixed | `vault.TestTransitExportable`, `config.TestTransitAllowExportable`, `e2e.TestTransitExportableRefused` | - | - | A key created `exportable` was accepted with a warning and cannot be made otherwise. It is refused at start unless `vault.kek.transit.allow_exportable_key` is true (confirmed by the maintainer, O-1); TB-4 |
| F-9 | fixed | `repo.TestNetworkParsersAreFuzzed`, `repo.TestFuzzRunsInCI` | - | - | No workflow ran a fuzz target and fourteen parsers of network input had none. All 34 targets run on every pull request (10 s each) and every night (3 min each); the model lists the parsers. The new targets ran 60 s each locally and found nothing |
| F-10 | wording | `docs/operations/container.md` (opening, "What Pandokos changes", "Limits of this pre-release") | - | - | The container guide opened with "one static access token, plain HTTP, one endpoint, no tenants in the product yet" and listed three forwarded request headers where the families forward five or six. Rewritten; the model's status line no longer says "initial" |
| F-11 | fixed | `repo.TestReleaseVerifiesWhatItPublishes` | - | - | ADR 0018 said that images and binaries are signed; no binary is released. ADR 0018 is corrected and amended by ADR 0074 |
| F-12 | fixed | `repo.TestReleaseVerifiesWhatItPublishes` | - | - | The guide verified `ghcr.io/pandokos/pandokos:<version>`, a tag that can be moved. The guide now resolves and verifies the digest of the release page and runs by digest; TM-60 |
| F-13 | fixed | `repo.TestDependabotCoverage` | - | - | `dependabot.yml` had no entry for `test/transit`, which pins two images by digest. Added; the test finds every directory that pins something |
| F-14 | fixed | `repo.TestReleaseVerifiesWhatItPublishes` | - | - | The image scanned in CI is not the one the release workflow publishes. The `verify` job now scans the pushed digest before `publish`; TM-51, TM-60 |
| F-15 | fixed | `repo.TestScheduledChecks` | - | - | `govulncheck` and the image scan ran on a push only, and never on the released image. They run every night, in `scheduled.yml` |
| F-16 | fixed | `repo.TestReleaseVerifiesWhatItPublishes` | - | - | CONTRIBUTING asks for a signed annotated tag; the workflow checked the branch and not the tag. It now refuses a tag that is not annotated. Partly: the signature is still checked by the maintainer, because verifying it in the workflow needs the maintainer's public key in the repository (O-2, a follow-up) |
| F-17 | maintainer action | The maintainer enabled both rulesets and Dependabot security updates on 2026-11-24 (O-9); the nightly `settings` job fails when either ruleset is not `active` (`repo.TestScheduledChecks`) | - | - | ADR 0018 states a protected main branch. Read on 2026-11-24, `main` had no branch protection and both rulesets were disabled (last changed on 2026-11-23); secret scanning, push protection and private vulnerability reporting were enabled and Dependabot security updates were not. The rulesets are the right ones; enabling them was the maintainer's |
| F-18 | accepted | `repo.TestLatestReleaseIsPinned`, and the sentence of the Compose guide that production pins the digest | - | the maintainer, 2026-11-24 (O-8) | The Compose reference runs `ghcr.io/pandokos/pandokos:0.2`, a tag the release workflow moves at each patch. A first trial follows patches; as designed and tested |
| F-19 | accepted | The section "Verifying a release" of the container guide | - | the maintainer, 2026-11-24 (O-8) | The SBOM on the GitHub release is an unsigned copy; the signed one is the attestation on the image. The guide says which is authoritative and how to print it |
| F-20 | deferred | The row TB-2 of the security model restates it | sprint 10.2 (O-5) | the maintainer, 2026-11-24 (O-5) | The scraper's `platform-auditor` key reads tenants, users, keys, issuers and support actions, not only metrics. A role that holds `metrics:read` alone is the smaller change; before sprint 8.2 ships a `ServiceMonitor` at the latest |
| F-21 | accepted | The paragraph on fuzzing in `SECURITY.md` | - | the maintainer, 2026-11-24 (O-4, O-8) | A crash found by fuzzing in a public repository shows its stack trace in a public log. The input is neither printed nor uploaded; a crash that a caller or a provider can cause is handled as a vulnerability. Reconsidered with the assessment of sprint 10.2 |

No finding beyond F-21 was made while implementing. Two observations are not findings: in the fuzz runs, the
admin API's `decodeBody` returns a field error with an empty field name for a body such as `{"":"0"}` (cosmetic;
the answer is still `invalid_request`); and the scheduled workflow cannot be dispatched before it is on the default
branch, so its first run is after the merge.

## Status of every identifier

Every identifier of the [security model](security-model.md) has one row. The status is after the sprint.

| ID | Status | Note |
|---|---|---|
| TB-1 | proved |  |
| TB-2 | proved; residual accepted | F-20: the scraper key reads more than metrics; deferred to sprint 10.2 |
| TB-3 | proved |  |
| TB-4 | fixed; residual accepted | F-8: an exportable key is refused unless allowed; whoever holds the token and the database unwraps every data key while the token lives |
| TB-5 | proved |  |
| TB-6 | proved; residual accepted | a shared issuer is separated by audience alone; a token cannot be revoked; a platform administrator can repoint a platform-level issuer |
| TB-7 | proved; residual accepted | out of scope (issue #33) |
| TB-8 | proved |  |
| TS-1 | proved |  |
| TS-2 | proved |  |
| TS-3 | proved; residual accepted | F-18: the Compose reference follows a moving minor tag |
| TM-01 | proved |  |
| TM-02 | proved |  |
| TM-03 | proved |  |
| TM-04 | proved (wording corrected) | F-6: mutual TLS is not built |
| TM-05 | proved |  |
| TM-06 | proved |  |
| TM-07 | proved |  |
| TM-08 | proved |  |
| TM-09 | proved; residual accepted | a known username can be locked out |
| TM-10 | proved | F-3: the shape `pds_` is now a rule of the secret scan |
| TM-11 | proved |  |
| TM-12 | proved |  |
| TM-13 | proved |  |
| TM-14 | proved |  |
| TM-15 | proved |  |
| TM-16 | proved |  |
| TM-17 | proved | F-3: the shape `whsec_` is now a rule of the secret scan |
| TM-18 | proved |  |
| TM-19 | proved; residual accepted | documented; the audit log keeps the host only |
| TM-20 | proved (wording corrected) | F-7: the row now says that a secret shown once is in its response |
| TM-21 | proved |  |
| TM-22 | proved |  |
| TM-23 | proved |  |
| TM-24 | proved; residual accepted | the token leaks: bounded by the policy and by its short life |
| TM-25 | proved |  |
| TM-26 | proved; residual accepted | a backup older than the rotation |
| TM-27 | proved |  |
| TM-28 | proved |  |
| TM-29 | proved |  |
| TM-30 | proved; residual accepted | the residual path and the pending-password gap, sprint 10.2 |
| TM-31 | proved |  |
| TM-32 | proved |  |
| TM-33 | proved |  |
| TM-34 | proved |  |
| TM-35 | proved |  |
| TM-36 | proved |  |
| TM-37 | proved |  |
| TM-38 | proved |  |
| TM-39 | fixed | F-2: four metadata addresses added |
| TM-40 | fixed | F-2 |
| TM-41 | fixed | F-1: a member in another case is refused |
| TM-42 | proved | a fuzz target was added |
| TM-43 | proved |  |
| TM-44 | test added | F-4: the framing of the HTTP stack now has a test of ours |
| TM-45 | proved |  |
| TM-46 | proved; residual accepted | per instance until epic 8 |
| TM-47 | proved; residual accepted | as the row states |
| TM-48 | proved | F-1 also closes a `stream` member in another case |
| TM-49 | test added | F-5: the header limits now have a test |
| TM-50 | proved; residual accepted | the owner role can rewrite a chain; the chain shows it |
| TM-51 | fixed | F-11 to F-17, supply chain below |
| TM-52 | proved |  |
| TM-53 | proved |  |
| TM-54 | proved |  |
| TM-55 | proved |  |
| TM-56 | proved |  |
| TM-57 | proved |  |
| TM-58 | proved | new row |
| TM-59 | test added | new row; fuzz targets added |
| TM-60 | fixed | new row; F-12, F-14 |

Of the 8 boundaries, 7 were proved or stated and one (TB-4) carries a decision, the refusal of an exportable key.
Of the 57 threats as the model stood: 49 were proved, 2 had their wording corrected (TM-04, TM-20), 2 had a control
without a test (TM-44, TM-49) and 4 were gaps (TM-39 and TM-40 by one finding, TM-41, TM-51). TM-58 to TM-60 are
new rows.

## Residual exposures

The residual exposures that the model accepts, restated, each with the date the maintainer accepted it. None is
new. All were confirmed as restated by the maintainer on 2026-11-24 (open point O-8).

| Exposure | Where | Accepted by the maintainer |
|---|---|---|
| The scraper's `platform-auditor` key reads more than metrics (F-20); target: sprint 10.2 | TB-2 | 2026-11-24 (O-5, O-8) |
| Whoever holds the key service's token and the database unwraps every data key while the token lives | TB-4, TM-24 | 2026-11-24 (O-8) |
| A shared issuer is separated between tenants by the audience alone | TB-6 | 2026-11-23, restated 2026-11-24 |
| A token cannot be revoked before it expires | TB-6, TM-04 | 2026-11-23, restated 2026-11-24 |
| A platform administrator can repoint a platform-level issuer and act as the service accounts mapped to it | TB-6 | 2026-11-23, restated 2026-11-24 |
| Confidentiality of a tenant's data against whoever runs the hosts, the database or the backups (issue #33) | TB-7 | 2026-11-24 (O-8) |
| The Pandokos image of the Compose reference follows a moving minor tag (F-18) | TS-3 | 2026-11-24 (O-8) |
| A known username can be locked out for the lock duration | TM-09 | 2026-11-24 (O-8) |
| A URL with a token in its path is readable by every holder of `event_destination:read` | TM-19 | 2026-11-24 (O-8) |
| A backup older than a rotation still opens credentials until its key version is destroyed | TM-26 | 2026-11-24 (O-8) |
| The residual path of a platform administrator, and the pending-password gap; target: sprint 10.2 | TM-30 | 2026-11-24 (O-8) |
| Limits are counted per instance until the shared store of epic 8 | TM-46, TM-47 | 2026-11-24 (O-8) |
| The database owner role can rewrite an audit chain; the chain shows it | TM-50 | 2026-11-24 (O-8) |
| The SBOM on the release page is an unsigned copy (F-19) | TM-60 | 2026-11-24 (O-8) |
| A fuzz crash shows its stack trace in a public log before it is fixed (F-21) | process | 2026-11-24 (O-4, O-8) |
| The tag's signature is checked by the maintainer and not by the workflow (F-16, O-2) | TM-60 | 2026-11-24 (O-2) |

Carried forward, not accepted here: forbidding every link-local address for every class (O-3) and a fuzzing
service that reports privately (O-4) are for the assessment of sprint 10.2.

## Supply chain

What the project states about its supply chain, read against what the pipeline does. "Before" is the result of the
reading; "After" is what this sprint changed. The files read are ADR 0018, `SECURITY.md`, `CONTRIBUTING.md`
("Releasing"), the container guide, `Dockerfile`, `Makefile`, `ci.yml`, `release.yml`, `dependabot.yml`,
`.gitleaks.toml`, `go.mod` and the repository tests.

| Stated control | What the pipeline does | Before | After |
|---|---|---|---|
| Few direct dependencies, each justified | `repo.TestDependencies` fixes the list of fourteen | holds | holds |
| Versions pinned with checksums; module proxy and checksum database | `go.sum`; `GOFLAGS=-mod=readonly` in the image build; tools run as `module@version` | holds | holds; `repo.TestEverythingPinned` also covers every `go run` and `go install` |
| Updates as reviewed pull requests from an automated tool | `dependabot.yml`: now eleven entries | F-13, `test/transit` missing | fixed; `repo.TestDependabotCoverage` |
| `govulncheck` and a linter on every pull request | jobs `vuln` and `lint` | holds on pushes; F-15, nothing between pushes | fixed: `vuln` also runs every night |
| A static binary, CGO disabled, reproducible flags | `Dockerfile` (`-trimpath`, `-buildid=`, `-buildvcs=false`) | holds; two builds are not compared (not claimed) | holds |
| Distroless base, non-root, no shell | `Dockerfile`; `repo.TestDockerfile`, `container.TestImage` | holds | holds |
| Multi-architecture | `linux/amd64,linux/arm64`; the `verify` job checks the index | holds | holds |
| The image is scanned in CI | the `image` job scans `pandokos:ci` (a fixed vulnerability of severity high and above fails) | F-14, not the published digest | fixed: the `verify` job scans the pushed digest; the nightly `image` job scans what `latest` names |
| Releases are built only by the pipeline from a tagged commit | `release.yml` on `v*`; the commit must be on `main` or a release branch; the CI runs first | holds; F-16, the tag's signature | the tag must be an annotated tag object; the signature remains the maintainer's check (O-2) |
| An SBOM | SPDX from the pushed digest, attested with cosign, and a copy on the release | holds; F-19 | holds; the guide says which is authoritative |
| Signs images and binaries with keyless signatures | `cosign sign` on the digest | holds for the image; F-11, no binaries | fixed: ADR 0018 says what is produced |
| Build provenance | `actions/attest-build-provenance`, pushed to the registry | holds | holds |
| Verification instructions in the installation guide | three commands, by tag | F-12 | fixed: by digest, with the SBOM and what the pipeline verified |
| The pipeline verifies before it publishes | job `verify` (signature, SBOM attestation, provenance, platforms, version) precedes `publish` | holds | holds, and the scan is added to it |
| Actions pinned to commit hashes; minimal permissions | every `uses:` is a 40-character commit with a version comment; `contents: read` at the top; `repo.TestActionsPinned` | holds | holds for every workflow file, `scheduled.yml` included |
| Images pinned by digest | `Dockerfile`, the test Compose files and the Compose reference's services | holds; one test per file | `repo.TestEverythingPinned` holds every one |
| Hash-locked Python and npm packages for the SDK suite | `--require-hashes`; `repo.TestSDKLockfiles` | holds | holds |
| Protected main branch, required reviews and checks, secret scanning, private reporting | repository settings | F-17: both rulesets disabled on 2026-11-24; secret scanning and private reporting enabled | the maintainer enabled both rulesets and Dependabot security updates on 2026-11-24 (O-9); the nightly `settings` job fails if either ruleset is not `active` |
| A gitleaks scan of the whole history with the `pdk_` rule | job `secrets`, `fetch-depth: 0` | holds; F-3 | rules for `pds_` and `whsec_` added from `redact.go`; `repo.TestSecretShapesAreScanned` |

The answers of the maintainer's checks of the repository settings (plan F4) are recorded here when the sprint is
delivered: the settings of 2026-11-24 are the ones above. The newest release (`v0.2.0`) is verified by digest by the
maintainer with the guide as rewritten, and its digest and the date are added to this record then. The first run of
`scheduled.yml` is the maintainer's, after the merge, because a scheduled workflow can be dispatched only from the
default branch; its result is added here by a follow-up commit.

## Not reviewed

- Any independent review: this one is by the authors. Sprint 10.2 is the independent assessment.
- The dependencies' own code, cryptographic code beyond its tests, and timing side channels.
- The browser client: it does not exist, and what a client hides is not a control.
- A member-case difference against a real provider: the rule is tested against the parser, not against a provider
  that matches names without regard to case.
- The time the `fuzz` job really takes on a runner, and whether the hosted runners keep the 10 s per target stable:
  both are known after the first pull request.
- The Actions settings of the repository (default workflow permissions) are not readable from the review; the
  workflows state their own `permissions`.
- That the history of the repository holds no session token or signing secret in the shapes of the two new
  rules: this is the first run of `make secrets` with them and is part of the delivery checks.
- What the maintainer's repository settings were between the commits of the sprint: only the readings of
  2026-11-24 are recorded.
- The metadata addresses of clouds this review did not confirm against the vendor's documentation: Azure (the
  WireServer address `168.63.129.16`), Oracle Cloud (`169.254.2.2`), DigitalOcean and Hetzner. They are not on the
  forbidden list; a `private` endpoint or a private host can still reach them until the decision on link-local
  addresses (O-3) is taken.
- Each test is read against its row, not run against a mutant: the review asserts that a proof would fail if the
  control went away, by reading its assertions.
