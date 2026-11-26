# 0074. Threat model review and continuous fuzzing

- Status: accepted
- Date: 2026-11-25

**Amends** [ADR 0018](0018-supply-chain.md) (what a release is and how it is verified),
[ADR 0037](0037-request-pipeline-and-resolution.md) point 5 (the members of a request body),
[ADR 0038](0038-egress-guard-upstream-tls-and-proxy.md) point 2 (the forbidden addresses) and
[ADR 0073](0073-the-transit-key-provider.md) point 3 (the requirements of the key). Builds on the
[security model](../architecture/security-model.md) and on [ADR 0019](0019-configuration.md). Requirements SEC-05
(reviewed), SEC-06, SEC-04, VLT-12, CAT-06 and GW-06; sprint 12.2.

## Context

SEC-05 asks that the threat model is reviewed at every release and independently before 1.0. Until this
decision the model was a list of threats with a sentence of controls: nothing tied a row to the code that
implements its control or to the test that would fail if the control went away, and nothing said when it was
last read against the code. The review that closes epic 12 (operability) is the first one, and it is done by the
people who wrote the code: it can find what a careful reading finds, and it says what it cannot.

The review read the 8 boundaries, the 3 statements outside the list and the 57 threats of the model, located the
code of each control and opened every test that is named as its proof. It found 21 things to decide
(the findings of the [review record](../architecture/threat-model-review-2026-11.md)). Four of them are changes of
behaviour: a request body can carry a member that the gateway does not read but a provider does (a name in
another case); the table of forbidden addresses was short of metadata addresses; a key of the key service that
can be exported was accepted with a warning; and fourteen parsers of data that comes from the network had no fuzz
target while no workflow ran any. The others are tests that were missing, words of the model that were not true,
and a pipeline that did not check the thing it published.

## Decision

1. **The model is traceable (D-1).** Every boundary, statement and threat has a permanent identifier (`TB-n`,
   `TS-n`, `TM-nn`) and names, in the model itself, the code that implements its control (one to three paths)
   and the tests that fail when the control is removed (one to six, `package.Function`). A repository test
   (`TestThreatModelTraceability`) holds the tables: identifiers consecutive, every path existing, every proof one
   function that exists. A threat added without code and proof, or a test renamed or deleted, fails the build.
   A review is a dated record, `docs/architecture/threat-model-review-YYYY-MM.md`, that the model's status line
   links; a second test (`TestThreatModelReview`) holds the record: one status for each identifier of the model,
   one decision for each finding, a test that exists for each finding that is `fixed` or `test added`, a sprint
   or an issue for each `deferred`, the maintainer's acceptance and its date for each `accepted`. The model is
   reviewed at every release, by a new record (a short one when nothing changed), and independently before 1.0.
2. **A member in another case is refused (D-2).** A request body is refused with `invalid_request` when it holds,
   in an object the gateway scans, a member whose name is not spelled exactly as a name the gateway reads in that object
   but equals it when case is ignored by any of three comparisons: Unicode simple folding, upper casing or lower
   casing (they differ on the dotless i U+0131, the dotted capital I U+0130, the long s U+017F and the Kelvin sign
   U+212A, and the providers' readers differ among them). The names are the read set of each family (`ReadMembers`, and
   `ReadStreamOptions` for `stream_options`). The reason in the operator's log is `member_case`; the answer is
   the one of every invalid request (ADR 0020), so no SDK test and no client changes. The body is never
   re-serialized: refusing is the one answer that cannot change a byte of what is forwarded. **Amends ADR 0037
   point 5.**
3. **The forbidden table is extended with the metadata addresses the vendors document (D-3).** Added, each as a
   single address and each after the vendor's documentation was read on 2026-11-24:

   | Address | Service | Source |
   |---|---|---|
   | `169.254.170.23` and `fd00:ec2::23` | Amazon EKS Pod Identity agent, IPv4 and IPv6 | docs.aws.amazon.com/eks/latest/userguide/pod-id-agent-setup.html |
   | `fd20:ce::254` | Google Compute Engine metadata server, IPv6 | cloud.google.com/compute/docs/metadata/overview |
   | `fe80::a9fe:a9fe` | OpenStack metadata service, IPv6 | docs.openstack.org/nova/latest/user/metadata.html |

   The OpenStack address is inside `fe80::/10`, which is private; forbidding it moves it out of that range, so a
   private endpoint cannot be on it either. Not added, because this review did not confirm them against the
   vendor's documentation: the Azure WireServer address `168.63.129.16`, the Oracle Cloud address `169.254.2.2`
   (an iSCSI address, not confirmed as a credential service), and DigitalOcean and Hetzner (no address other than the shared
   `169.254.169.254` was found). Forbidding every link-local address for every class (open point O-3) is the
   alternative of a different decision and is left to the assessment of sprint 10.2. **Amends ADR 0038 point 2.**
4. **The two controls the HTTP stack provides get tests of ours (D-4).** What the listeners refuse (a request that
   hides a second request, a conflicting framing, a header block over `max_header_bytes`, headers that arrive
   slower than `read_header_timeout`) is tested on both listeners and on both toolchains the suite runs on; no
   code changes.
5. **An exportable key is refused unless allowed (D-5).** A key of the key service that was created `exportable`
   is refused at start, with a fixed text that names the setting and `vault.kek.transit.allow_exportable_key`, as a
   cause only a person can repair. With the new boolean key, default `false`, the key is accepted and the start
   logs the warning that was logged before. The four other weak settings (`plaintext_backup`,
   `deletion_allowed`, `auto_rotate`, a token with the `root` policy) stay warnings. The reason: the setting
   cannot be undone in the service and lets any token whose policy allows export read the key in clear; v0.3.0 is
   not released, so the safe default costs nobody an upgrade. **Amends ADR 0073 point 3** (the maintainer
   confirmed the refusal on 2026-11-24, O-1).
6. **Every parser of network input has a fuzz target and the model lists them (D-6).** "From the network" means
   bytes whose author is not the operator. Where such a parser was inline in a function that also does I/O (the
   key-set and discovery decoding of `jwks`, the token exchange of `oidc`), the pure part was separated with its
   statements moved, not rewritten. The model's section "Parsers that read from the network" is held against
   the code and the Makefile by `TestNetworkParsersAreFuzzed`: a parser without a target, or a target missing from
   the table, fails the build.
7. **Fuzzing runs natively, in two depths (D-7).** `go test -fuzz` through one list, `make fuzz`, that both
   workflows call and never name a target: 10 seconds per target on every pull request and push to `main`, in a
   job of its own, and 3 minutes per target every night. A crash fails the job with Go's own report and nothing
   else: the input is neither printed nor uploaded. An input that made a target fail is committed under
   `testdata/fuzz/` with the fix, where the ordinary tests run it for ever after. No generated corpus is kept
   between runs.
8. **No fuzzing service and no third-party fuzzing action (D-8).** ADR 0018: nothing is added that the toolchain
   already does. A service that reports crashes privately (OSS-Fuzz) is reconsidered with the assessment of
   sprint 10.2 (O-4); until then a crash that a caller or a provider can cause is handled as a vulnerability
   (`SECURITY.md`).
9. **The nightly checks (D-9).** A workflow `scheduled.yml` runs every night, only in `pandokos/pandokos`:
   the deep fuzz run, `govulncheck` over both modules, a scan of the image that `latest` names, and a reading of
   the repository's rulesets that fails unless the ruleset of the branches and the ruleset of the version tags are
   `active`. A vulnerability is published on its own calendar and a protection can be switched off without a trace
   in the repository; both now turn something red. Nothing in it writes. It runs on the default branch only, so its
   first run is the maintainer's, after the merge.
10. **The release is verified by digest (D-10).** The digest is what the pipeline signs, attests, scans and
    verifies, and what the guide tells the operator to verify and to run: the release page prints
    `ghcr.io/pandokos/pandokos@sha256:<digest>`. A tag can be moved after a release; a digest cannot. The
    `verify` job scans the pushed digest with the thresholds of the scan in `ci.yml` before `publish`, which needs
    it. The tag must be an annotated tag object. The attested SBOM is the authoritative one; the file attached to
    the release is an unsigned copy. No binary is a release artefact: the artefacts are the image, its SBOM and
    the API description. The workflow verifies the signature of the tag against the maintainer's public key in
    `.github/allowed_signers` (O-2, done on 2026-11-26). **Amends ADR 0018.**
11. **Every pinned reference is under one test and under automated update; every secret shape that the log
    redacts is a rule of the secret scan (D-11).** One test reads every Compose file, every Dockerfile, every
    `go run` and `go install` and every workflow for what is not pinned; one test finds every directory that pins
    something and requires its entry in `dependabot.yml`; one test holds the patterns of `redact.go` against the
    rules of `.gitleaks.toml`, so the two shapes of secret that Pandokos issues and the scan did not know
    (`pds_` session tokens and `whsec_` signing secrets) are found in the history and in a pull request.
12. **Residual exposures are not decided again (D-12).** A review states what is. Each exposure that the model
    accepts is restated in the record with the date it was accepted by the maintainer; the pending-password gap
    and the scraper's credential keep sprint 10.2 as their target.

## Consequences

- Every later sprint: a threat or a control added to the model comes with its code path and its test, or the
  build fails; a parser of network input comes with its fuzz target and its row; a new workflow, Compose file,
  Dockerfile or nested module comes pinned and with its dependabot entry; a test named as a proof cannot be
  renamed without the model. The first limit of this is stated in the record: the test holds that the named
  tests exist, not that they still assert what the row says, which a reviewer reads again at every review.
- A request body with a member in another case is refused (a body without one is forwarded byte for byte as
  before), an exportable key of the key service is refused unless allowed, and a release is verified by digest.
  The notes of v0.3.0 say so.
- The restore drill and the upgrade policy (ADR 0075 and 0076) use a key that is not exportable, or set the option
  and say why; the policy states that the threat model is reviewed at every release. The assessment of
  sprint 10.2 takes the record as its map: identifiers, code, proofs, residual exposures with their dates, and the
  list of what this review did not verify.
- A fuzz crash in a public repository shows its stack trace in a public log before it is fixed (R-4, accepted for
  now, F-21): the input is not printed.
- The nightly workflow can fail for a reason that is not ours (a new vulnerability in a base image, a setting
  changed); it notifies the maintainer who last changed its schedule, and no issue is opened automatically.

## Alternatives considered

**A fuzzing service or action (OSS-Fuzz, a cloud fuzzer):** a new party that receives the code and, for the
service, reports privately; ADR 0018 asks for nothing added that the toolchain does. Reconsidered with the
assessment (O-4).

**Verifying the release by tag:** the only thing the pipeline signs is a digest; a tag that is moved after the
release verifies against nothing the operator checked.

**Forbidding every link-local address for every class:** every cloud puts instance credentials there and a model
server there is implausible, but it changes the wording of GW-06 and the contract of a `private` endpoint, so it is
decided with the assessment (O-3), and the list is extended now.

**Keeping `exportable` a warning:** the setting cannot be undone, and a warning is read once; a refusal with an
opt-out costs nobody an upgrade before the first release that has the key service.

**A traceability document separate from the model:** two documents that say the same thing drift apart; the test
holds the model itself.

**Normalizing the case of members instead of refusing:** it re-serializes the body, which Pandokos never does
(the splice of ADR 0024 is the only change to the bytes).

## History

- 2026-11-24: the maintainer confirmed the open points of the design: O-1 (D-5), O-2 (the tag's signature as a
  follow-up), O-3 (the list extended now), O-4 (F-21 accepted for now), O-5 to O-8, and O-9 (both rulesets and
  Dependabot security updates enabled by the maintainer the same day).
- 2026-11-25: proposed with sprint 12.2.
- 2026-11-25: sprint 12.3: the threat model is reviewed at every release as part of the upgrade policy ([ADR 0075](0075-upgrade-policy-and-the-tested-upgrade.md)).
- 2026-11-26: accepted by the maintainer after review.
- 2026-11-26: O-2 done: the maintainer's public signing key is in `.github/allowed_signers` and `check-tag` runs `git verify-tag` against it.
