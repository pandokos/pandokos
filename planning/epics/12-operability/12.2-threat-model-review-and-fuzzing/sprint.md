---
id: SPRINT-12.2
title: Threat model review and fuzzing
epic: EPIC-12
release: v0.3
status: in-progress
design: draft 2026-11-24
---

# Sprint 12.2 — Threat model review and fuzzing

- Epic: [12 — Operability](../epic.md)
- Implementation plan: [plan.md](plan.md)
- Previous sprint: [12.1 — External key-encryption key](../12.1-external-key-encryption-key/sprint.md)
- Scope: the epic file, section 12.2, with the items handed to 12.2 by sprint 12.1 (its §11, its
  open point O-13 and ADR 0073, "later sprints inherit"), and the items of the closed epics that
  touch the threat model, fuzzing or the supply chain (§5.12).
- Risk class: **high** (a security review: what it declares proved is relied upon afterwards; it
  changes what the gateway refuses in a request body, what the egress guard refuses to dial, what
  `serve` refuses at start with a key service, and the release workflow)
- Decisions implemented: [ADR 0018](../../../../docs/adr/0018-supply-chain.md) (supply chain),
  [0037](../../../../docs/adr/0037-request-pipeline-and-resolution.md) (the model rule),
  [0038](../../../../docs/adr/0038-egress-guard-upstream-tls-and-proxy.md) (address classes),
  [0073](../../../../docs/adr/0073-the-transit-key-provider.md) (key requirements),
  [0002](../../../../docs/adr/0002-implementation-language-go.md) (no new module: native fuzzing),
  [0020](../../../../docs/adr/0020-error-contract.md) (no new code).
- Decision records changed by this sprint: new **ADR 0074** (the review of the threat model, how it
  is kept true, and continuous fuzzing; **amends** ADR 0018 "Release" and "Dependencies", ADR 0037
  on the members of a request body, ADR 0038 point on forbidden addresses, ADR 0073 on an
  exportable key); history lines on 0018, 0037, 0038 and 0073. Text in [plan.md](plan.md), part E.

This document is the design **and the review**: the review of the threat model against the code was
done while designing, at commit `96cb793c0359b339c4c98785c29a0776e2dada0f` (2026-11-24), and its
result is §5.2 and §5.3. The implementer follows [plan.md](plan.md); a gap in either document is
raised as a question, not filled silently ([planning/README.md](../../../README.md)).

| Given by the epic and the closed sprints | Designed in |
|---|---|
| Review of the threat model against the code as built, trust boundary by trust boundary | §5.1, §5.2 |
| Fixes | §5.3, §5.5 to §5.8, plan part C |
| A dated record of the review | §5.4, `docs/architecture/threat-model-review-2026-11.md` |
| Exit: every threat names the control that answers it and the test that proves the control | §5.4, T-R89, T-R90 |
| Continuous fuzzing of the parsers that read from the network | §5.9, T-1105 to T-1118, T-R91 |
| Exit: the fuzz targets run in CI | §5.10, T-R92 |
| Review of the supply-chain controls against what the pipeline actually produces | §5.11, T-R93 to T-R97 |
| 12.1 §11: boundary 4 in its network form, five controls each with its test | §5.2 (TB-4) |
| 12.1 §11 and ADR 0073: the answer parser of the transit client joins the fuzz targets | T-1105 |
| 12.1 O-13: `exportable` may become a refusal with an opt-out | §5.7, D-5, O-1 |
| Epic risk: the review is done by the people who wrote the code | R-1; sprint 10.2 stays the outside check |

## 1. Outcome

Every threat and every trust boundary of the [security model](../../../../docs/architecture/security-model.md)
has an identifier, names the code that answers it and the test that proves it, and a repository test
fails when a named file or test no longer exists. A dated record states what was reviewed, at which
commit, what was found and what was decided for each finding.

The review found the model true to the code in 49 of its 57 rows as written; it corrects two rows
that named a control that is not there, adds tests to two controls that had none of ours, fixes two
gaps in code (a request member spelled in another case passes unseen to a provider that reads it;
the list of instance-metadata addresses lacks three), and refuses an exportable key of the key
service unless the operator says otherwise. Three threats the code already answers are added to the
model.

Every parser that reads bytes from the network has a fuzz target: fourteen new ones join the twenty
that exist. All of them run for a bounded time on every pull request and for longer every night.

The supply-chain controls are stated as the pipeline produces them: the image that is published is
the image that is scanned, verification is by digest, known vulnerabilities are looked for every
night and not only when someone pushes, every pinned thing in the repository is under automated
update and under a test, and the protection of the main branch and of version tags, found switched
off, is watched.

## 2. Scope

### In scope

- The review itself (§5.2, §5.3), written into `docs/architecture/security-model.md` (identifiers,
  the columns `Code` and `Proof`, three new rows, two corrected rows, the status line) and into the
  record `docs/architecture/threat-model-review-2026-11.md`.
- Code fixes: `internal/family` (members in another case), `internal/egress/classify.go` (metadata
  addresses), `internal/vault` and `internal/config` (an exportable key), and two tests of the
  listeners in `internal/server` that pin what the HTTP stack refuses.
- Fourteen fuzz targets and their seed corpora; `make fuzz` stays the one list.
- CI: a `fuzz` job in `ci.yml`; a new workflow `scheduled.yml` (longer fuzzing, `govulncheck`, a
  scan of the published image); a scan of the pushed digest in `release.yml`; `dependabot.yml`;
  two rules in `.gitleaks.toml`.
- Repository tests T-R89 to T-R97.
- ADR 0074, history lines, the container guide ("Verifying a release" and its stale statements),
  the gateway, external-key and configuration guides, the draft release notes of v0.3.0.

No migration, no change to the OpenAPI document, no permission, no error code, no new module, no
new GitHub Action (every action used is one the workflows already pin).

### Not in scope

| Item | Where |
|---|---|
| An independent assessment by people who did not write the code | sprint 10.2 (SEC-09); this review is its input |
| The gap of a membership or role gained while a platform-issued password is pending | sprint 10.2, as accepted on 2026-11-23; unchanged, restated in the record |
| A list of breached passwords, HSTS when the listener serves TLS itself, provider-initiated logout, step-up | sprint 10.2 (epic 4 history) |
| A strict switch that refuses a key file readable by others | epic 10 (epic 2 history, 2.1 O-2) |
| Scopes for platform keys, or a role holding `metrics:read` alone, for the scraper's credential | sprint 10.2 (§5.12, O-5); until then the telemetry guide's rule stands |
| Refusing the whole link-local range for private endpoints | open point O-3; needs a change of GW-06 |
| Sender-constrained credentials (IAM-15, IAM-16) | not built; the model says so after this sprint |
| OSS-Fuzz, ClusterFuzzLite, a fuzzing service | not used (D-8); revisited with the assessment of 10.2 (O-4) |
| A corpus that grows between nightly runs | not kept (§5.10); committed regression inputs only |
| Fuzzing of the W3C trace-context propagator, the YAML and JOSE libraries as such | they are dependencies; our use of them is bounded and tested (§5.9); their own fuzzing is upstream's |
| Fuzzing of `declare.Parse` and of the configuration loader | they read a local file given by the operator, not the network (§5.9) |
| Signature verification of the release tag in the workflow | open point O-2: needs the maintainer's public key in the repository |
| Signed release binaries | none are released: the image is the artefact; ADR 0018 is corrected (D-10) |
| Reproducible-build verification (two builds, one digest) | not planned; sprint 10.2 may ask |
| Repository settings (branch and tag protection, required checks, secret scanning, private reporting) as code | outside the repository; read on 2026-11-24 (F-17), read again by the maintainer with the commands of plan F4, and watched nightly for the rulesets |
| The restore drill, the upgrade policy | sprint 12.3 |

## 3. Requirements claimed

The meaning is quoted from [docs/requirements](../../../../docs/requirements/README.md).

| ID | Meaning | Delivered here | Extent |
|---|---|---|---|
| SEC-05 | "A threat model is maintained with the architecture and states the trust boundaries, including what an infrastructure operator with access to hosts, database, backups and keys can do" | The review against the code, the record, identifiers and proofs per row, the test that keeps them true (§5.2 to §5.4) | **R3 (reviewed)**: full |
| SEC-06 | "The build produces a minimal, non-root image, with pinned dependencies, vulnerability scanning, an SBOM, and signed artefacts with verifiable provenance" | Touched: the published digest is scanned; verification by digest; nightly vulnerability checks; every pin under a test and under automated update (§5.11) | touched |
| SEC-04 | "Defaults are secure. At start-up Pandokos validates its configuration and refuses or warns about weak settings" | Touched: an exportable key of the key service is refused by default (§5.7) | touched |
| SEC-09 | "An independent security review takes place before 1.0" | Not delivered; the record is written to be its input | none (R5) |
| CAT-06 | "A request for a model that is not allowed is rejected before it is forwarded, whatever identifier the caller uses" | Touched: a member in another case no longer reaches a provider that reads it (§5.5) | touched |
| GW-06 | "Outbound connections to private, loopback and link-local addresses are refused unless the endpoint is explicitly marked private, and the check is made on the address actually dialled" | Touched: three more instance-metadata addresses are forbidden for every class (§5.6) | touched |
| GW-07 | "Request headers are forwarded from an allow-list. Inbound credentials and hop-by-hop headers are removed. HTTP parsing is strict" | Touched: "HTTP parsing is strict" gets a test of ours (§5.8) | touched (test) |
| GW-16 | "Request size, header size, duration and per-tenant concurrency are limited" | Touched: the header size and header time limits get a test (§5.8) | touched (test) |
| VLT-12 | "The key-encryption key comes from a pluggable provider: a local key file by default, an external KMS or transit service optionally" | Touched: the key requirements of the transit provider (§5.7); its answer parser is fuzzed | touched |
| VLT-14 | "Secrets never appear in logs, traces, metrics, error messages, events or diagnostics. Automated tests plant canary secrets and search every output for them" | Touched: the fuzz targets of parsers that see secrets assert that no error repeats the input (§5.9); session tokens and signing secrets get a scanning rule (§5.11) | touched |

## 4. Repository layout

New: `docs/architecture/threat-model-review-2026-11.md`, `docs/adr/0074-threat-model-review-and-continuous-fuzzing.md`,
`.github/workflows/scheduled.yml`, `test/repo/threatmodel_test.go`, `test/repo/supplychain_test.go`,
the fuzz functions of §5.9 in the test files of their packages (new files `fuzz_test.go` where the
package has no natural place), `internal/server/strict_test.go`.

Changed: `docs/architecture/security-model.md`, `internal/family/jsonbody.go`,
`internal/family/openai/request.go`, `internal/family/anthropic/request.go`,
`internal/egress/classify.go`, `internal/vault/{transit,transit_client}.go`,
`internal/config/{config,keys,transit}.go`, `cmd/pandokos/{serve,kek}.go` (the option only),
small extractions of pure parsing functions in `internal/jwks`, `internal/oidc` (§5.9, no change of
behaviour), `Makefile` (fourteen lines of `fuzz`), `.github/workflows/{ci,release}.yml`,
`.github/dependabot.yml`, `.gitleaks.toml`, the tests of §9 "Changed tests", the documents of plan
part E.

## 5. Architecture

### 5.1 How the review was done

1. The model was read row by row: the eight trust boundaries, the three unnumbered statements
   (capability check, the two functions that cross the tenant boundary, the Compose reference) and
   the 57 rows of "Threats and controls". Each got an identifier: `TB-1` to `TB-8`, `TS-1` to
   `TS-3`, `TM-01` to `TM-57`, in the order of the document.
2. For each, the code that implements the control was located (package and file), and the test that
   asserts it was looked for in the index of the 1160 test functions of the repository and the test
   tables of the sprint designs.
3. Where a control is security-critical and small, the function was read, not only located: the
   body parser of the families (`family.ScanObject`, `ParseModel`, `LastMember`), the address
   classes (`egress.Classify`) and the dial control of the guard, the key-service transport
   (`egress.Guard.KeyService`, its loopback rule), the transit client's exchange and decoding, the
   key requirements and weak settings, the key-set parser of `internal/jwks`, the token endpoint
   exchange of `internal/oidc`, the webhook URL validation, the cursor decoder of the admin API, the
   header allow-lists of both families, the listener limits of `internal/server`, the redaction
   patterns, and the three workflows' worth of pipeline (`ci.yml`, `release.yml`, `dependabot.yml`,
   `Dockerfile`, `Makefile`, `.gitleaks.toml`).
4. A row is **proved** when the control was located and a test named in §5.2 asserts it; **wording**
   when the control is proved but the row says something the code does not do; **untested** when the
   control exists and no test of the repository would fail if it disappeared; **gap** when the
   threat is not answered as the row says.

What this method does not give: the body of each of the tests named in §5.2 was not re-read line by
line. The names come from the test index and from the designs that specified them; plan step E1
makes the implementer open each one before writing it into the `Proof` column, and replace it or
raise a question if it does not assert the control. §12 lists what was and was not verified.

### 5.2 Traceability

Status: **P** proved; **W** proved, wording of the row corrected; **U** control without a test, test
added in this sprint; **G** gap, fixed in this sprint; **A** carries a residual exposure that stays
accepted and is restated in the record. A finding is `F-n` of §5.3. Paths are files or package
directories that exist at the reviewed commit; tests are named `package.Function`.

**Trust boundaries**

| ID | Boundary | Control (code) | Proof | Status |
|---|---|---|---|---|
| TB-1 | Caller to gateway | Everything is input until a credential verifies: `internal/gateway/handler.go` (pipeline order), `internal/authn/authn.go` | `gateway.TestUnauthenticated`, `gateway.TestPipelineOrder`, `gateway.TestNothingBeforeAuthentication` | P |
| TB-2 | Administrator to admin API | One credential per request, sessions on this listener only, CSRF, plain-text refusal, every operation authorized, `/metrics` never anonymous: `internal/admin/admin.go`, `internal/admin/origin.go`, `internal/server/server.go` | `admin.TestEveryOperationNeedsACredential`, `admin.TestCredentialPrecedence`, `admin.TestCSRF`, `admin.TestSignInTransport`, `admin.TestTokensOnAdminListener`, `server.TestListenersAreSeparate`, `e2e.TestScrapeEndpoint` | P, A (the scraper's `platform-auditor` key reads more than metrics: F-20) |
| TB-3 | Process to database | One least-privileged role per plane; forced row-level security: `internal/store`, `internal/store/migrations` | `isolation.TestRuntimeRolesCannotEscape`, `isolation.TestOwnerIsSubjectToPolicies`, `e2e.TestServeRefusesPrivilegedUser`, `store.TestCheckRefusesWrongUser`, `store.TestTenantContextDoesNotLeak` | P |
| TB-4 | Process to key provider (network form, 12.1 §11) | (a) the token's reach is bounded by the policy `deploy/transit/pandokos-policy.hcl` and by short tokens; (b) `ca_file` is exclusive; (c) `http` only to a loopback address, checked on the address dialled: `internal/egress/guard.go` (`KeyService`, `loopbackControl`), `internal/config/transit.go`; (d) only a digest of the associated data leaves: `internal/vault/transit.go` (`aadField`); (e) binding proved at start: `internal/vault/check.go` | (a) `transit.TestConformance` (the policy file as shipped), `vault.TestTransitAppRoleAuth`, `e2e.TestTransitTokenLifetime`, `vault.TestTransitCanary`, `e2e.TestTransitCanary`; (b), (c) `egress.TestKeyServiceTransport`, `config.TestTransitKeys`; (d) `vault.TestTransitAssociatedData`; (e) `vault.TestCheckAssociatedDataBound` | P, A (whoever holds the token and the database unwraps every data key while the token lives), G for an exportable key (F-8) |
| TB-5 | Gateway to provider | Destination from the catalogue, certificates always verified, no redirect: `internal/egress/guard.go`, `internal/gateway/proxy.go` | `egress.TestTLS`, `gateway.TestRouteByProvider`, `gateway.TestRedirectNotFollowed`, `e2e.TestUpstreamCAAndProxy`, `repo.TestTransitClientDependencies` (no `InsecureSkipVerify`) | P |
| TB-6 | Identity provider (workloads and people) | Issuers scoped; keys fetched through the guard; a tenant issuer's change is a grant; the people's provider is one configured issuer, tokens dropped: `internal/authn/tokens.go`, `internal/jwks/fetch.go`, `internal/oidc/flow.go`, `internal/oidc/idtoken.go` | `authn.TestTokenPrincipalTenantBound`, `authn.TestTokenPrincipalPlatform`, `isolation.TestPlatformIssuerVisibility`, `admin.TestNoEscalationCredentials`, `admin.TestIssuerAudit`, `oidc.TestTokensAreDropped`, `oidc.TestMetadata` | P, A (shared issuer separated by audience alone; a token cannot be revoked; a platform administrator can repoint a platform-level issuer: accepted 2026-11-23) |
| TB-7 | Infrastructure operator | Not a control: the statement of what is not protected. What reduces it: bounded lifetime of plaintext, key outside the database: `internal/vault/opener.go`, `internal/vault/unwrapcache.go` | `vault.TestOpenerCache`, `vault.TestUnwrapCache`, `e2e.TestBackupBeforeDeletionYieldsNothing` | A (out of scope, issue #33) |
| TB-8 | Gateway to trace and metrics collector | One operator URL through the guard; closed attribute lists: `internal/telemetry/traces.go`, `internal/telemetry/metrics.go` | `telemetry.TestExport`, `telemetry.TestCollectorNeverBlocks` (a metadata address is refused), `telemetry.TestAttributesAreClosed`, `e2e.TestMetricsHoldNoTenantData`, `e2e.TestNoContentAnywhere` | P |

**Statements outside the table**

| ID | Statement | Control (code) | Proof | Status |
|---|---|---|---|---|
| TS-1 | The capability check opens no credential and audits nothing | `internal/admin` (capability) | `admin.TestCapability`, `admin.TestCapabilityIsolation`, `e2e.TestCapabilityAgreesWithGateway` | P |
| TS-2 | The two functions that cross the tenant boundary read no rule across tenants | `internal/store/migrations` | `isolation.TestDeletePlatformIssuerLimitRules`, `isolation.TestRecordUnlimitedTraffic`, `isolation.TestRuleForeignKeys` | P |
| TS-3 | The trust choices of the Compose reference | `deploy/compose/compose.yaml` | `repo.TestComposeReference`, `compose.TestQuickStart` | P, A (a moving minor tag for the Pandokos image: F-18) |

**Threats and controls**

| ID | Threat (short) | Control (code) | Proof | Status |
|---|---|---|---|---|
| TM-01 | A caller claims another tenant | Tenant from the key row only: `internal/authn/authn.go` | `authn.TestTenantComesFromTheKey`, `isolation.TestCallerCannotNameTheTenant` | P |
| TM-02 | A bug omits a tenant filter | Forced row-level security, non-owner roles: `internal/store/migrations`, `internal/store` | `isolation.TestSchemaLint`, `isolation.TestCrossTenantNegative`, `isolation.TestOwnerIsSubjectToPolicies` | P |
| TM-03 | A stolen virtual key | Scopes, expiry, revocation within a bound, detectable format: `internal/authn` | `authn.TestAuthenticate`, `authn.TestRevocationBoundWithoutNotifications`, `authn.TestRevocationByNotification`, `authn.TestParseRejects`, `repo.TestSecretScanning` | P |
| TM-04 | A stolen bearer token | Signature, issuer, audience, lifetime, mapped subject on every request: `internal/authn/token.go` | `authn.TestTokenClaims`, `authn.TestSignatureOnEveryRequest`, `e2e.TestTokenRevocation` | W (F-6: the row names mutual TLS, which is not built) |
| TM-05 | A tenant registers the issuer another tenant uses | The pair (issuer, audience) is unique; a token matching two registrations is refused: `internal/authn/tokens.go` | `authn.TestIssuerSelection`, `isolation.TestIssuerTables` | P |
| TM-06 | A token of another relying party, a forged algorithm or key | Allow-list before any key lookup; key fits the algorithm: `internal/authn/token.go`, `internal/jwks/keys.go` | `authn.TestTokenAlgorithmAttacks`, `jwks.TestKeySelection`, `repo.TestJOSEImports` | P |
| TM-07 | SSRF through `issuer` or `jwks_uri` | `https` through the guard, bounds, one fetch per source per 10 s: `internal/jwks/fetch.go` | `e2e.TestTokenSSRF`, `jwks.TestLimits`, `jwks.TestTransportClass`, `jwks.TestRefreshAndRefetch` | P |
| TM-08 | Forged tokens flood the log, the audit log or the identity provider | Bounded audited failures: `internal/resolver` | `gateway.TestTokenFailureAudit`, `resolver.TestTokenFailureAuditor`, `jwks.TestRefreshAndRefetch` | P |
| TM-09 | Password guessing | Throttle by digests, uniform refusal, Argon2id with a bound on concurrency: `internal/account`, `internal/password` | `account.TestThrottle`, `account.TestSourceThrottle`, `account.TestSignInUniform`, `password.TestConcurrency`, `e2e.TestLockExit`, `e2e.TestNoDefaultCredentials` | P, A (a known username can be locked out) |
| TM-10 | A stolen or forged session | Hash only, cookie attributes, lifetimes, ten per user: `internal/session/session.go` | `session.TestStoreStoresOnlyTheHash`, `session.TestCookie`, `session.TestMaxPerUser`, `admin.TestSessionLifetimes`, `admin.TestDisableEndsSessions` | P (F-3: the token's shape has no scanning rule) |
| TM-11 | Cross-site request forgery | Header, `Origin`, `Sec-Fetch-Site`, before the body: `internal/admin/origin.go`, `internal/admin/admin.go` | `admin.TestCSRF`, `admin.TestExpectedOrigin`, `admin.TestSecurityHeaders`, `browser.TestCrossSite` | P |
| TM-12 | A sign-in forged with another person's code, a replayed callback | State, nonce, PKCE from a seed; one-use state row: `internal/oidc/flow.go`, `internal/oidc/idtoken.go` | `oidc.TestCallbackState`, `oidc.TestIDToken`, `oidc.TestCompleteQuery`, `e2e.TestOidcExit` | P |
| TM-13 | Group rules give a role by mistake | Exact match, two roles, audited, reconciled: `internal/oidc/groups.go` | `oidc.TestRules`, `oidc.TestGroups`, `admin.TestGroupReconciliation` | P |
| TM-14 | A tenant administrator takes over a person powerful elsewhere | The managing rule (two triggers): `internal/store/migrations` | `store.TestManagingRule`, `admin.TestMembers` | P |
| TM-15 | A retry creates a second resource or leaks the first one's secret | `internal/idem/idem.go` | `idem.TestClaim`, `admin.TestIdempotencyEveryCreate`, `admin.TestIdempotencyStoresNoSecret` | P |
| TM-16 | A webhook points at an internal address | Worker only, through the guard; URL rule: `internal/outbox/deliver.go` | `outbox.TestValidateURL`, `outbox.TestDeliver`, `repo.TestOutboxEgress`, `admin.TestEventDestinationOperations` | P |
| TM-17 | A webhook signing secret leaks | Server-generated, sealed with its own associated data, redacted: `internal/vault/named.go`, `internal/outbox/sign.go`, `internal/telemetry/redact.go` | `vault.TestSealNamed`, `admin.TestEventSecretCanary`, `outbox.TestRelaySecretRotation`, `telemetry.TestSigningSecretShape` | P (F-3) |
| TM-18 | A tenant's events are redirected, read or forged by a role | Column grants: `internal/store/migrations` | `isolation.TestEventDestinationGuards`, `isolation.TestOutboxTables`, `outbox.TestSign` | P |
| TM-19 | A URL with a token in its path is read by other administrators | Documented; the audit log keeps the host only: `internal/admin` | `admin.TestEventDestinationAPI` | P, A |
| TM-20 | Script injection in a browser client | The cookie is `HttpOnly`; the sign-in answer holds no token: `internal/session/session.go` | `session.TestCookie`, `admin.TestSignInEndpoint` | W (F-7: "no API response carries a credential" is not true of the responses that show a key once) |
| TM-21 | A signed-in user reaches functions beyond their role | Every call authorized: `internal/authz`, `internal/admin` | `admin.TestRoleMatrix`, `admin.TestMeMatrix`, `isolation.TestRoutesArePublicOrPermitted` | P |
| TM-22 | A session is used to call models | No cookie read by the gateway; no privilege on people tables | `e2e.TestSessionNotOnInference`, `repo.TestGatewayReadsNoCookie`, `isolation.TestPeopleTablesAreAdminOnly` | P |
| TM-23 | The database or a backup is copied | Envelope encryption, hashes for keys: `internal/vault/envelope.go` | `e2e.TestVault`, `isolation.TestVaultColumnPrivileges`, `isolation.TestKeyTables` | P |
| TM-24 | The token of the key service leaks | Policy, short tokens, never logged: `deploy/transit/pandokos-policy.hcl`, `internal/vault/transit_auth.go` | as TB-4 (a), `repo.TestTransitDocuments` | P, A |
| TM-25 | A key version is destroyed too early | The start check names the version: `internal/vault/check.go` | `e2e.TestTransitStartRefusals`, `e2e.TestTransitRotation`, `vault.TestCheckSeveralProviders` | P |
| TM-26 | The key-encryption key leaks | Rotation by re-wrapping: `internal/vault/rewrap.go`, `internal/vault/activate.go` | `e2e.TestKEKRotationEndToEnd`, `vault.TestRewrap`, `vault.TestRewrapAcrossProviders` | P, A (a backup older than the rotation) |
| TM-27 | A tenant leaves | Staged deletion: `internal/lifecycle` | `e2e.TestDeletionEndToEnd`, `isolation.TestTenantErasureSteps`, `isolation.TestErasurePseudonymizesLedger` | P |
| TM-28 | A ciphertext is copied onto another tenant's row | Associated data: `internal/vault/envelope.go` | `vault.TestSealOpen`, `isolation.TestCredentialGuards` | P |
| TM-29 | A credential is guessed from its fingerprint or hint | Keyed per tenant: `internal/vault/envelope.go` | `vault.TestFingerprintAndHint` | P |
| TM-30 | A platform administrator abuses the role | No read path; support actions: `internal/authz`, `internal/admin` | `isolation.TestPlatformSeesNoTenantUsage`, `isolation.TestPlatformKeyHasNoTenantPermission`, `admin.TestSupportNeverReadsUsage`, `admin.TestSupportAudit`, `repo.TestPlatformReadsNoTenantUsage` | P, A (the residual path and the pending-password gap, sprint 10.2) |
| TM-31 | A principal hands out more than it holds | `CanGrant`: `internal/authz` | `authz.TestCanGrant`, `admin.TestGrantSurfaceIsClosed`, `admin.TestNoEscalationRoles`, `admin.TestNoEscalationAssignments` | P |
| TM-32 | A custom role smuggles a platform permission | Refused at write, dropped at computation: `internal/authz` | `authz.TestEffectiveGranted`, `authz.FuzzEffective`, `admin.TestRoles` | P |
| TM-33 | A deactivated or deleted role keeps working | Notification and cache bound | `admin.TestRoleChangeBound`, `authn.TestCustomRolePrincipal` | P |
| TM-34 | An administrator removes the owners | Removal needs the role's permissions | `admin.TestSeparationOfDuties` | P |
| TM-35 | A support action outlives its need | Expiry by constraint, ended by the tenant | `admin.TestSupportPrincipal`, `admin.TestTenantSeesAndEndsSupport`, `isolation.TestSupportActionVisibility` | P |
| TM-36 | "No membership" reveals which tenants exist | Answers of 4.2 byte for byte | `admin.TestSupportPrincipal`, `isolation.TestCrossTenantIndistinguishable` | P |
| TM-37 | `GET /v1/me` differs from what is enforced | One function for both | `admin.TestMeMatchesTheHook` | P |
| TM-38 | A future built-in role takes a custom role's name | Reserved names and prefixes | `repo.TestBuiltinNamesInMigration`, `admin.TestRoles` | P |
| TM-39 | The proxy is used to reach internal services | Catalogue destinations, no redirect, the dialled address classified: `internal/egress/guard.go`, `internal/egress/classify.go` | `egress.TestDialControl`, `egress.TestDialledAddressIsChecked`, `egress.TestNoRedirectNoEnvironmentProxy`, `e2e.TestEgressAttacks`, `repo.TestOutboundHTTPInOnePlace` | G (F-2) |
| TM-40 | A private endpoint is used to reach internal services | Class follows the endpoint; never a metadata address | `gateway.TestEgressClassFollowsEndpoint`, `egress.TestClassify`, `egress.TestProxy` | G (F-2) |
| TM-41 | Two `model` members pass the model check | Exactly one member `model`: `internal/family/jsonbody.go` (`ParseModel`) | `family.TestParseModel`, `gateway.TestInvalidRequest`, `openai.FuzzParseRequest` | G (F-1) |
| TM-42 | A poisoned price dataset | Data never code, bounded, RE2, off by default: `internal/pricing/genaiprices/parse.go`, `internal/catalog/fetch.go` | `genaiprices.TestParse`, `catalog.TestFetch`, `catalog.TestImportDB`, `repo.TestPriceSnapshot` | P (fuzz target added) |
| TM-43 | The provider credential is sent elsewhere | Attached for the registered host only: `internal/gateway/proxy.go` | `gateway.TestCredentialSwap`, `gateway.TestCredentialNeverReachesCaller`, `openai.TestAttachCredential` | P |
| TM-44 | Request smuggling, header injection | Allow-lists, hop-by-hop removal: `internal/gateway/proxy.go`; strict parsing by the HTTP stack: `internal/server/server.go` | `gateway.TestRequestHeaders`, `gateway.TestResponseHeaderAllowList`, `gateway.TestInformationalResponsesAreDropped`, `gateway.TestProviderTrailersAreDropped` | U (F-4: nothing of ours tests what the stack refuses) |
| TM-45 | Secrets or content in logs or traces | No path logs bodies; canaries | `e2e.TestCanaries`, `e2e.TestGatewayCanaries`, `e2e.TestNoContentAnywhere`, `gateway.TestTelemetryCanaries` | P |
| TM-46 | A tenant overspends through concurrency | Reserve before forwarding: `internal/limits` | `limits.TestReserveAtomic`, `limits.TestPropertyConservation`, `gateway.TestParallelLoad` | P, A (per instance until epic 8) |
| TM-47 | A tenant spends more than its budget | `internal/limits`, `internal/gateway/limits.go` | `gateway.TestParallelBudget`, `limits.TestPropertyPeriods`, `gateway.TestNoPrice`, `e2e.TestMonthlyBudget` | P, A (as the row states) |
| TM-48 | Usage is evaded through unmetered endpoints | Only metered routes exist | `gateway.TestEndpointNotSupported`, `e2e.TestUnsupportedAnthropicPaths`, `gateway.TestRouteDeclaresAPermission` | P (F-1 also closes a `stream` member in another case) |
| TM-49 | Resource exhaustion | Body size, duration, concurrency, bounded readers: `internal/gateway/handler.go`, `internal/server/server.go` | `gateway.TestRequestTooLarge`, `gateway.TestMaxRequestDuration`, `gateway.TestConcurrencyLimit`, `gateway.TestStoreFailure`, `openai.TestStreamReaderBounded`, `anthropic.TestStreamReaderBounds` | U (F-5: the header size and header time limits have no test) |
| TM-50 | The audit log is altered | No update or delete grant; hash chain: `internal/auditchain/auditchain.go` | `isolation.TestAuditIsAppendOnly`, `isolation.TestAuditChainSeal`, `e2e.TestAuditVerifyDetectsTampering`, `isolation.TestAuditRetention` | P, A (the owner role can rewrite a chain) |
| TM-51 | A dependency or the build is compromised | Pinned and scanned; signed image, provenance, SBOM: `go.mod`, `Dockerfile`, `.github/workflows` | `repo.TestDependencies`, `repo.TestActionsPinned`, `repo.TestDockerfile`, `repo.TestReleaseWorkflowShape`, `container.TestImage` | G (F-11 to F-17, §5.11) |
| TM-52 | A caller forges trace or metadata | Read after authentication, bounded, never used in a decision: `internal/usage/metadata.go` | `gateway.TestNothingBeforeAuthentication`, `usage.TestParseMetadata`, `gateway.TestPropagation` | P |
| TM-53 | The gateway is used to rewrite or read the ledger | `INSERT` on listed columns only | `isolation.TestUsageAppendOnly`, `isolation.TestUsageRecord` | P |
| TM-54 | A provider smuggles text into the ledger | Integers under fixed keys: `internal/family/family.go` (`FlattenUsage`) | `family.TestFlattenUsage`, `family.FuzzFlattenUsage`, `isolation.TestUsageRecord` | P |
| TM-55 | Someone reads the metrics without a right to | `metrics:read`, off by default | `e2e.TestScrapeEndpoint`, `authz.TestMetricsPermission` | P |
| TM-56 | A secret reaches the log inside an error text | The redacting handler: `internal/telemetry/redact.go` | `telemetry.TestRedactingHandler`, `telemetry.TestRedaction`, `repo.TestLogReference` | P |
| TM-57 | The collector is used as a way out | The exporter uses the guard | as TB-8 | P |

**Rows the review adds** (threats the code answers and the model did not name)

| ID | Threat | Control (code) | Proof |
|---|---|---|---|
| TM-58 | A provider, or whoever answers in its place, sends an answer made to crash or exhaust the gateway | Readers are bounded and fail soft; usage is read from a copy and never decides the answer: `internal/family/openai/stream.go`, `internal/family/anthropic/stream.go`, `internal/family/sse` | `openai.TestStreamReaderBounded`, `anthropic.TestStreamReaderBounds`, `gateway.TestReaderFailsSoft`, the fuzz targets of both families |
| TM-59 | The key service, an identity provider or the price source answers with crafted data | Answers bounded in size, error bodies never parsed, fixed error texts, keys that do not fit are skipped: `internal/vault/transit_client.go`, `internal/jwks/keys.go`, `internal/oidc/flow.go`, `internal/pricing/genaiprices/parse.go` | `vault.TestTransitErrorClasses`, `jwks.TestLimits`, `oidc.TestCodeExchange`, T-1105 to T-1109 |
| TM-60 | A published image or its tag is replaced after the release, or the pipeline publishes something other than what it checked | The digest is what is signed, attested, scanned and verified; verification is by digest: `.github/workflows/release.yml`, `docs/operations/container.md` | `repo.TestReleaseWorkflowShape`, T-R95 |

Count over the 57 rows as the model stood: 49 proved, 2 proved with the wording corrected (TM-04,
TM-20), 2 controls without a test (TM-44, TM-49), 4 gaps (TM-39 and TM-40 by one finding, TM-41,
TM-51). Of the 8 boundaries: 7 proved or stated, one with a decision (TB-4, an exportable key).

### 5.3 Findings and decisions

| # | Finding | Rows | Decision |
|---|---|---|---|
| F-1 | `family.ParseModel` and `LastMember` compare member names exactly. A body with `"model"` and `"Model"` passes the check on the first, and a provider whose JSON decoder matches names without regard to case (an OpenAI-compatible server written with Go's `encoding/json`, among others) reads the second. The same holds for `stream` (the answer is then read as the wrong kind and its usage is estimated, not read), `stream_options`, `background` and the output-limit members. Listed as "not scheduled" in the history of epic 3 | TM-41, TM-48 | **Fix** (C1, D-2): refuse a body with a member that equals a member Pandokos reads except for case. T-1095 to T-1098 |
| F-2 | The forbidden table of `egress.Classify` names four instance-metadata addresses. Candidates it lacks, which stay reachable by a `private` endpoint or a private webhook host: the address of the EKS Pod Identity agent in IPv4 and IPv6, and the IPv6 address of the Google Cloud metadata server | TM-39, TM-40, TM-16 | **Fix** (C2, D-3): the implementer confirms each candidate against the vendor's documentation (plan C2 gives where), adds the confirmed ones, and records source and date in ADR 0074. T-1099, T-1115. The wider question is O-3 |
| F-3 | The log redaction knows three shapes of secret that Pandokos issues (`pdk_`, `pds_`, `whsec_`); the secret scanning of the repository knows one | TM-10, TM-17 | **Fix** (D4, D-11): two rules in `.gitleaks.toml`; T-R96 holds the two lists together |
| F-4 | "Strict parsing by the Go HTTP stack" is a control no test of the repository exercises; the suite runs on two toolchains and neither run would notice a change | TM-44 | **Test added**: T-1103 (C3). If it fails on either toolchain, stop and ask |
| F-5 | `max_header_bytes` and `read_header_timeout` are validated as configuration and never exercised on a listener | TM-49 | **Test added**: T-1104 (C3) |
| F-6 | The row of a stolen bearer token ends "Mutual TLS for stronger binding (IAM-15, IAM-16)"; neither is built ("Replay exposure" says so two sections later) | TM-04 | **Wording** (E1): "not built" in the row |
| F-7 | "No API response carries a credential a script could keep": the responses that create a key, a password or a signing secret show it once | TM-20 | **Wording** (E1): the session token is in no response body and in no place a script reads; a secret shown once is in the response and is the client's to drop |
| F-8 | A key of the key service created `exportable` can be read in clear by any token of the service whose policy allows export, and the setting cannot be undone. Today a warning (12.1 D-3, O-13) | TB-4 | **Fix** (C4, D-5): refused at start unless `vault.kek.transit.allow_exportable_key` is true. T-1100 to T-1102. Raised as O-1 |
| F-9 | No workflow runs a fuzz target; fourteen parsers of network input have none (§5.9) | exit criterion | **Fix** (B, C5, D1, D2): T-1105 to T-1118, T-R91, T-R92 |
| F-10 | The security model's status line says "initial"; the container guide opens with "one static access token, plain HTTP, one endpoint, no tenants in the product yet" and ends with "It speaks plain HTTP", and lists three forwarded request headers where the families forward five or six | documents | **Fix** (E1, E3) |
| F-11 | ADR 0018: "signs images and binaries". No binary is released; the artefacts are the image, its SBOM and the API description | TM-51 | **Fix** (E2, D-10): ADR 0018 amended by ADR 0074 |
| F-12 | The guide verifies `ghcr.io/pandokos/pandokos:<version>`. A tag can be moved after the release; the signature is on a digest | TM-60 | **Fix** (E3, D-10): the guide resolves and verifies the digest printed on the release page, and runs by digest; T-R95 |
| F-13 | `dependabot.yml` has no entry for `test/transit` (two images pinned by digest since 12.1) | TM-51 | **Fix** (D4): entry added; T-R94 finds every directory that pins something |
| F-14 | The image scanned is the one built by the `image` job of `ci.yml`; the one published is built again by `release.yml` and is not scanned | TM-51, TM-60 | **Fix** (D3): the `verify` job scans the pushed digest before `publish`; T-R95 |
| F-15 | `govulncheck` and the image scan run on a push or a pull request only: a vulnerability published while nobody pushes is not seen, and never for the released image | TM-51 | **Fix** (D2, D-9): nightly in `scheduled.yml`; T-R97 |
| F-16 | CONTRIBUTING asks for a signed, annotated tag; the workflow checks the commit's branch and not the tag's signature | TM-60 | **Partly** (D3): the workflow refuses a tag that is not annotated. Verifying the signature needs the maintainer's public key in the repository: O-2 |
| F-17 | ADR 0018 states a protected main branch with required reviews and status checks. Read through the GitHub API on 2026-11-24: `main` has no branch protection; the two rulesets that would give it ("Protect main and release branches": pull request, status checks, linear history, no deletion, no force push; "Protect version tags": no deletion, update or force push of `v*`) exist with `enforcement: disabled`, last changed on 2026-11-23; secret scanning, push protection and private vulnerability reporting are enabled; Dependabot security updates are disabled. A setting can be switched off without a trace in the repository | TM-51, TM-60 | **Maintainer action** (plan F4, O-9): enable both rulesets, or state in the record why not and until when. **Fix** (D2, D-9): a nightly job reads the rulesets and fails when either is not `active`; T-R97 |
| F-18 | The Compose reference runs `ghcr.io/pandokos/pandokos:0.2`, a tag the release workflow moves at each patch | TS-3 | **Accepted**, as designed and tested (`repo.TestLatestReleaseIsPinned`): a first trial follows patches. The Compose guide gains the sentence that production pins the digest (E3) |
| F-19 | The SBOM attached to the GitHub release is an unsigned copy; the signed one is the attestation on the image | TM-60 | **Accepted, documented** (E3): the guide says which one is authoritative and how to read it from the attestation |
| F-20 | The scraper's `platform-auditor` key reads tenants, users, keys, issuers and support actions (carried forward by epic 6) | TB-2 | **Deferred** to sprint 10.2 (O-5); the model already states it |
| F-21 | A crash found by fuzzing in a public repository shows its stack trace in a public log | process | **Accepted** for now (R-4, O-4): the input is not printed or uploaded; a crash that a caller or a provider can cause is handled as a vulnerability (SECURITY.md) |

### 5.4 The model after the sprint, and the record

**`docs/architecture/security-model.md`.**

- Status line: `Status: reviewed against the code on <date of the closing commit> (sprint 12.2):
  [review record](threat-model-review-2026-11.md). Reviewed at every release and independently
  before 1.0 (SEC-05, SEC-09).`
- After the numbered list of boundaries, a table `| ID | Boundary | Controls | Code | Proof |` with
  `TB-1` to `TB-8`, and after the three unnumbered statements one of the same shape with `TS-1` to
  `TS-3`; the prose of the list is unchanged except boundary 4, which names the refusal
  of an exportable key.
- "Threats and controls" becomes `| ID | Threat | Controls | Code | Proof |`. `Threat` and
  `Controls` keep today's text except TM-04, TM-20, TM-39 and TM-40 (the added addresses), TM-41
  (members in another case), TM-51 (what the pipeline produces) and the three new rows. `Code` is
  one to three paths in backticks. `Proof` is one to six tests in backticks, `package.Function`.
- A new section "Parsers that read from the network": the table of §5.9, `| Input | Parser | Fuzz
  target |`.
- A new last section "Reviews": one line per review record, newest first.

**`docs/architecture/threat-model-review-2026-11.md`** (the dated record):

1. Header: date, the commit reviewed (`96cb793c0359b339c4c98785c29a0776e2dada0f`) and the commit
   that closed the sprint; who reviewed (the maintainers who wrote the code) and the statement of
   the epic's risk; the method of §5.1 with what it does not give.
2. One row per identifier (`TB-`, `TS-`, `TM-`): the status after the sprint, from the closed set
   `proved`, `proved (wording corrected)`, `test added`, `fixed`, each optionally followed by
   `; residual accepted`.
3. The findings of §5.3, each with its decision from the closed set `fixed`, `test added`,
   `wording`, `accepted`, `deferred`, `maintainer action`, the test or document that shows it, and
   for `deferred` the sprint or issue; for `accepted` the reason and the maintainer's acceptance
   with its date (filled at the close, plan F5).
4. Residual exposures restated in one list, each with the date it was accepted.
5. The supply-chain table of §5.11 as checked, and the answers to the commands of plan F4.
6. What was not reviewed (§12 "Not verified").

**T-R89** holds the model: both tables have their header; identifiers are unique and consecutive;
every path of `Code` exists; every name of `Proof` resolves to exactly one `Test…` or `Fuzz…`
function declared in a `_test.go` file of a directory whose last element is the package name (`main`
is `cmd/pandokos`), build tags ignored; every row of the parser table names a function that exists
and a fuzz target that exists; the status line links the newest record. **T-R90** holds the record:
the header fields, one row per identifier of the model and none more, the closed sets, a test that
exists for every `fixed` and `test added`, a sprint or an issue for every `deferred`, a non-empty
acceptance for every `accepted` (the placeholder `pending (plan F5)` counts as empty, so the test is
red until the maintainer has decided).

A threat added later without code and proof fails T-R89; a test renamed or deleted fails it; a
review is a new record file, and the model must link the newest.

### 5.5 Members of a request body in another case (F-1, D-2)

Each family reads a closed set of top-level members of the request body, and the `openai` family
one member of the object `stream_options`. The rule added:

> In an object Pandokos scans, a member whose name equals a name Pandokos reads in that object when
> case is ignored (`strings.EqualFold`), and is not spelled exactly so, makes the request invalid.

- `family.CaseConflict(ms []Member, read ...string) bool` in `internal/family/jsonbody.go`.
- Each family holds its read set as one exported slice next to its parser (`openai.ReadMembers`,
  `openai.ReadStreamOptions`, `anthropic.ReadMembers`), and the parser takes the names it passes to
  `ParseModel`, `LastMember` and `PositiveInt` from nowhere else. The sets are what the code reads
  today; the implementer derives them from the calls and adds nothing.
- The refusal is `400 invalid_request` (the existing code and message of ADR 0020), with the
  invalid reason `member_case`, at the place where `model_duplicate` is raised: before the model
  check, before any credential is opened, nothing forwarded, one usage record of the kind an
  invalid request writes today. The reason joins the values of `invalid_reason` in the gateway
  guide.
- Exact duplicates keep today's rule (the last one decides, `model` excepted, which must be single).
  A body that a provider would have accepted with `"Model"` alone is refused too: Pandokos cannot
  authorize a model it does not read.
- Members Pandokos does not read are not looked at: the body is still never re-serialized.

### 5.6 Instance-metadata addresses (F-2, D-3)

`forbiddenPrefixes` of `internal/egress/classify.go` gains the confirmed addresses among: the EKS
Pod Identity agent (one IPv4 address in `169.254.170.0/24`, one IPv6 address in `fd00:ec2::/32`)
and the Google Cloud metadata server's IPv6 address. Nothing else changes in the table: the classes
of sprint 3.1 stand, and `private` endpoints keep link-local addresses (GW-06) until O-3 is
answered. The gateway guide's list of forbidden addresses and ADR 0038 are amended with the same
addresses and their sources.

### 5.7 An exportable key of the key service (F-8, D-5)

- New key `vault.kek.transit.allow_exportable_key`, boolean, default `false`, environment name by
  the usual rule; it is the fourteenth key of the block.
- `TransitProvider.Start` (through `checkKey`): a key whose description says `exportable: true` is
  refused with a fixed text that names the setting and the key that allows it, unless the option is
  true. The refusal is of the permanent kind of 12.1 §5.10: `serve` ends at once and does not wait;
  `pandokos kek check` prints the class and ends with 1.
- With the option true the behaviour is today's: the warning `key provider configuration weak`,
  setting `exportable`. `plaintext_backup`, `deletion_allowed`, `auto_rotate` and `root_token` stay
  warnings: none of them hands the key out by itself, and each has a legitimate use the guide names.
- It applies to the provider whatever it is used for, an unwrap-only provider during a move back to
  a key file included: an operator leaving an exportable key sets the option for the time of the
  move, and the guide says so.
- v0.3.0 is not released, so no installation changes behaviour on upgrade; the draft release notes
  state the rule.

### 5.8 What the listeners refuse (F-4, F-5)

Two tests over raw TCP against both listeners built by `internal/server`, each with a handler that
records whether it ran:

- **T-1103.** A request with `Content-Length` and `Transfer-Encoding: chunked` together; two
  different `Content-Length`; a header name with a space; a folded header line; a bare line feed in
  the request line; a request line with an absolute URI of another host; `Transfer-Encoding` other
  than `chunked`. Expected for each: the handler does not run with a body other than the one the
  framing rule of RFC 9112 gives, or the connection gets `400`; in no case does a second request
  smuggled in the body reach the handler. The table states the expected answer per case, taken from
  a first run on both toolchains (floor and release) and then fixed; a case that differs between
  the two is raised as a question.
- **T-1104.** A header block of `max_header_bytes` plus 8 KiB gets `431` and the handler does not
  run; headers sent one byte at a time past `read_header_timeout` get the connection closed and the
  handler does not run. Configuration values are set small for the test.

No production code changes for these; if a case shows the stack accepting what the model says it
refuses, the implementer stops and asks.

### 5.9 Parsers that read from the network, and their fuzz targets

"From the network" means bytes whose author is not the operator: a caller, an administrator's
client, a provider, an identity provider, the key service, the price source, a webhook receiver.

| Input | Parser | Fuzz target | State |
|---|---|---|---|
| Inference credential header | `openai.InboundCredential`, `anthropic.InboundCredential` | T-1117 `openai.FuzzInboundCredential`, T-1118 `anthropic.FuzzInboundCredential` | **new** |
| Virtual key | `authn.Parse` | `authn.FuzzParse` | exists |
| Bearer token (JWS, claims) | `authn` (`peek`, `verifyJWT`) | `authn.FuzzParseToken` | exists |
| Request body, `openai` | `openai` (parse, prepare, output bound), `family.ScanObject` | `openai.FuzzParseRequest`, `FuzzPrepareRequest`, `FuzzOutputBound` | exist; invariant added (§9) |
| Request body, `anthropic` | `anthropic` (parse, output bound) | `anthropic.FuzzAnthropicParseRequest`, `FuzzOutputBound` | exist; invariant added |
| Caller metadata headers | `usage.ParseMetadata` | `usage.FuzzParseMetadata` | exists |
| Provider answer, usage object | `openai.ReadUsage`, `anthropic` usage reader, `family.FlattenUsage` | `openai.FuzzReadUsage`, `anthropic.FuzzAnthropicReadUsage`, `family.FuzzFlattenUsage` | exist |
| Provider stream | the stream readers, `sse` framing | `openai.FuzzStreamReader`, `FuzzResponsesStreamReader`, `anthropic.FuzzAnthropicStreamReader`, `sse.FuzzFraming` | exist |
| Key service answers (encrypt, decrypt, describe, identify, lookup, renew, login) | `vault` (`decodeEnvelope`, `transitCiphertext`, `transitUnquote`, `transitDecode`, the grant and description types) | T-1105 `vault.FuzzTransitAnswer` | **new** |
| Key set of an issuer | `jwks.parseKeySet` | T-1106 `jwks.FuzzParseKeySet` | **new** |
| Discovery document of an issuer | `jwks` (the decoding inside `discover` and `fetchMetadata`) | T-1107 `jwks.FuzzMetadata` | **new** |
| Token endpoint answer and ID token claims of the people's provider | `oidc` (the decoding inside `redeem`; `stringClaim`, `audienceCount`, `readGroups`) | T-1108 `oidc.FuzzProviderAnswers`; `oidc.FuzzGroups` exists | **new** |
| Price dataset (embedded or downloaded) | `genaiprices.Parse`, `Extract` | T-1109 `genaiprices.FuzzParse` | **new** |
| Decimal amounts of the admin API | `pricing.ParseDecimal` | `pricing.FuzzParseDecimal` | exists |
| Cursor of a list request | `admin.decodeFullCursor` | T-1110 `admin.FuzzDecodeCursor` | **new** |
| Request body of the admin API | `adminapi.decodeBody` | T-1111 `adminapi.FuzzDecodeBody` | **new** |
| Path of the admin API | `adminapi.Match` | T-1112 `adminapi.FuzzMatch` | **new** |
| `Host`, `Origin` and forwarding headers of the admin API | `admin` (`splitHost`, `expectedOrigin`, `sourceAddr`) | T-1113 `admin.FuzzOrigin` | **new** |
| Session cookie | `session.ParseToken` | `session.FuzzParseToken` | exists |
| Webhook URL given by a tenant | `outbox.ValidateURL` | T-1114 `outbox.FuzzValidateURL` | **new** |
| Address a name resolved to | `egress.Classify` | T-1115 `egress.FuzzClassify` | **new** |
| Audit export read by `pandokos audit verify` | `auditchain.Canonical` | T-1116 `auditchain.FuzzCanonical` | **new** |
| Roles and grants (data of the database, reached by the API) | `authz` | `authz.FuzzEffective` | exists |
| Stored password hash | `password.ParsePHC` | `password.FuzzParsePHC` | exists (not network; kept) |

Not fuzzed, with the reason: a provider's error body and a webhook receiver's answer (never parsed:
passed through or discarded); the answer of a credential validation (the status only); the trace
context (parsed by the OpenTelemetry propagator after authentication, never used in a decision);
`declare.Parse` and the configuration (a local file of the operator; bounded by
`declare.checkNode`); multipart bodies (no route accepts one).

**Invariants.** Every target: no panic, and returns within the test's default deadline for an input
of at most 1 MiB. In addition:

| Target | Invariants |
|---|---|
| T-1105 | For each operation (selected by a second argument): an accepted plaintext is exactly 32 bytes; an accepted ciphertext has the prefix of the pinned version; a lifetime is not negative; the error, when there is one, is one of the fixed texts of 12.1 and contains no substring of the input longer than 8 bytes |
| T-1106 | At most `maxKeys` keys; each is public, of a type and size that fit an algorithm of the allow-list; never a symmetric key; no key ID twice |
| T-1107 | A document is accepted only when its `issuer` equals the one asked for, byte for byte; every endpoint it yields is an `https` URL without user information (or what the function's own rule allows: the implementer states it from the code) |
| T-1108 | An ID token string is returned only for a JSON object with a non-empty string `id_token`; `audienceCount` agrees with a plain decoding; groups never exceed their bound |
| T-1109 | `Parse` then `Extract` for every model: rates are non-negative decimals within the column's digits; the issue list is bounded; no regular expression of the dataset is compiled outside RE2 (by construction; asserted by no input taking more than the deadline) |
| T-1110 | An accepted cursor encodes back to a cursor that decodes to the same value; every field is within its bound; its ID parses |
| T-1111 | For three operations chosen to cover a secret member, a nullable member and a nested object: a failure is a `RequestError` with a code of the closed set; when the destination carries a secret, no field message contains a substring of the body longer than 8 bytes |
| T-1112 | A match yields an operation of the table and parameters that are non-empty and hold no `/` |
| T-1113 | The source address is always a valid address; when the peer is not a trusted proxy it is the peer, whatever the headers say; an expected origin has scheme, host and no path |
| T-1114 | An accepted URL parses, is `https` (or `http` with a listed host), has no user information or fragment, and a literal address in it is not of a class the guard refuses; the error never contains the input |
| T-1115 | Total over 16 bytes and a flag for IPv4: the class of an IPv4-mapped address is the class of the IPv4 address; a NAT64 or 6to4 address of a forbidden or private IPv4 address is not public; every address inside a forbidden prefix is forbidden |
| T-1116 | Deterministic; an input that is not one JSON value fails; the output of an accepted input does not depend on insignificant white space |
| T-1117, T-1118 | A credential returned is a substring of a header value of the request; no credential for an empty or malformed header |
| changed: `openai.FuzzParseRequest`, `anthropic.FuzzAnthropicParseRequest` | A body accepted has exactly one member equal to `model` when case is ignored, and no member that differs only in case from a member of the family's read set |

**Seed corpora.** Seeds are added in code with `f.Add`, as the existing targets do: the fixtures of
the package's tests (the answers the test double of the key service gives, the key sets of
`test/fakeidp`, the embedded price snapshot cut to one provider, cursors made by `encodeFullCursor`)
and one malformed variant of each. No seed holds a secret: tokens, keys and data keys in seeds are
generated at run time or are the synthetic values of the fixtures (`synthetic-…`). An input that
made a target fail is committed under `testdata/fuzz/<Target>/` of the package, where
`go test ./...` runs it for ever after; such a file is reviewed for content before it is committed.

**Small extractions.** Three parsers are inline in functions that also do I/O (`jwks.discover` and
`fetchMetadata`, `oidc.redeem`). Each gets a pure function with the same statements moved, not
rewritten, and the caller calls it; no behaviour changes and the existing tests of the three stay
green without change.

### 5.10 Fuzzing in CI (D-7, D-8)

- **One list.** `make fuzz` stays the list of targets, one line each, in the form T-R30 reads; the
  workflows call `make fuzz` and never name a target.
- **Every pull request and push to `main`**: a job `fuzz` in `ci.yml`, `make fuzz FUZZTIME=10s`,
  with the release toolchain, `timeout-minutes: 30`. Thirty-four targets: about six minutes of
  fuzzing and the builds. It runs through `workflow_call` before a release as the rest of `ci.yml`.
- **Every night**: a workflow `scheduled.yml` (`schedule` and `workflow_dispatch`), job `fuzz`,
  `make fuzz FUZZTIME=3m`, `timeout-minutes: 180`, only in the repository `pandokos/pandokos`.
- **A crash** fails the job with Go's own report: the target, the stack trace and the name of the
  file it wrote. The workflow does not print or upload the input. The maintainer runs the target
  locally, commits the input under `testdata/fuzz/` with the fix, and the ordinary `test` job then
  proves the fix on both toolchains.
- **The seed corpus and every committed input** already run in the `test` job as ordinary tests.
- No generated corpus is kept between runs: each run starts from the seeds and the committed
  inputs. Known and accepted (the cache of `setup-go` is keyed on `go.sum` and is not a corpus
  store).
- **T-R92**: `ci.yml` has a job `fuzz` that runs `make fuzz` with a `FUZZTIME`, without
  `continue-on-error`; `scheduled.yml` has a `schedule` trigger and a job that runs `make fuzz` with
  a longer one. T-R30 is extended to fuzz functions anywhere in the root module, and **T-R91** holds
  the parser table of the model against the code and the Makefile.

### 5.11 Supply chain: what is stated and what the pipeline produces

Read: ADR 0018, `SECURITY.md`, `CONTRIBUTING.md` ("Releasing"), `docs/operations/container.md`,
`Dockerfile`, `Makefile`, `.github/workflows/ci.yml` and `release.yml`, `.github/dependabot.yml`,
`.gitleaks.toml`, `go.mod`, the repository tests of `test/repo`.

| Stated control | What the pipeline does | Result |
|---|---|---|
| Few direct dependencies, each justified | `repo.TestDependencies` fixes the list of fourteen | holds |
| Versions pinned with checksums; module proxy and checksum database | `go.sum`; `GOFLAGS=-mod=readonly` in the image build; tools run as `module@version` | holds |
| Updates as reviewed pull requests from an automated tool | `dependabot.yml`: ten entries | **F-13** (`test/transit` missing) |
| `govulncheck` and a linter on every pull request | jobs `vuln` and `lint` | holds on pushes; **F-15** (nothing between pushes) |
| A static binary, CGO disabled, reproducible flags | `Dockerfile` (`-trimpath`, `-buildid=`, `-buildvcs=false`) | holds; two builds are not compared (not claimed) |
| Distroless base, non-root, no shell | `Dockerfile`; `repo.TestDockerfile`, `container.TestImage` | holds |
| Multi-architecture | `linux/amd64,linux/arm64`; the `verify` job checks the index | holds |
| The image is scanned in CI | the `image` job scans `pandokos:ci` (fixed vulnerabilities of severity high and above fail) | **F-14** (not the published digest) |
| Releases are built only by the pipeline from a tagged commit | `release.yml` on `v*`, the commit must be on `main` or a release branch, CI runs first | holds; **F-16** (the tag's signature) |
| An SBOM | SPDX from the pushed digest, attested with cosign, and a copy on the release | holds; **F-19** |
| Signs images and binaries with keyless signatures | `cosign sign` on the digest | holds for the image; **F-11** (no binaries) |
| Build provenance | `actions/attest-build-provenance`, pushed to the registry | holds |
| Verification instructions in the installation guide | three commands, by tag | **F-12** |
| The pipeline verifies before it publishes | job `verify` (signature, SBOM attestation, provenance, platforms, version) precedes `publish` | holds |
| Actions pinned to commit hashes; minimal permissions | every `uses:` is a 40-character commit with a version comment; `contents: read` at the top; `repo.TestActionsPinned` | holds, for every workflow file, a new one included |
| Images pinned by digest | `Dockerfile`, the three test Compose files, the Compose reference's services, each under a test of its own | holds; one test per file, none for "every file" (T-R93) |
| Hash-locked Python and npm packages for the SDK suite | `--require-hashes`; `repo.TestSDKLockfiles` | holds |
| Protected main branch, required reviews and checks, secret scanning, private reporting | repository settings: both rulesets disabled on 2026-11-24; secret scanning and private reporting enabled | **F-17** (does not hold today, and nothing in the repository notices) |
| A gitleaks scan of the whole history with the `pdk_` rule | job `secrets`, `fetch-depth: 0` | holds; **F-3** |

Fixes, each a step of plan part D or E:

- `release.yml`, job `check-tag`: the tag must be an annotated tag object. Job `verify`: a scan of
  `${IMAGE}@${DIGEST}` with the scan action already pinned in `ci.yml` and the same thresholds,
  after the provenance check; `publish` already needs `verify`.
- `scheduled.yml`: jobs `fuzz` (§5.10), `vuln` (`govulncheck` for both modules, as the `vuln` job),
  `image` (pull the newest released image by its `latest` tag, print its digest, scan it with
  the same action and thresholds) and `settings` (read the repository's rulesets through the API
  with the job's token and fail unless the two named above are `active`; if the job's token cannot
  read them, which the first run shows, the check moves to the release check list of CONTRIBUTING
  and the job is not added). Top-level `permissions: contents: read`; nothing writes.
- `dependabot.yml`: `docker-compose` for `/test/transit`.
- `.gitleaks.toml`: rules `pandokos-session` and `pandokos-signing-secret` with the patterns of
  `internal/telemetry/redact.go`; `make secrets` must still pass on the whole history (if it does
  not, the hit is examined before any allow-list line is written, and a real value is a finding to
  raise at once).
- `docs/operations/container.md`: "Verifying a release" by digest; the three stale statements.
- ADR 0018 amended by ADR 0074: no binaries; verification by digest; the published digest is
  scanned; nightly checks; fuzzing in CI.

### 5.12 Items carried forward, each with its decision

| Item | From | Decision |
|---|---|---|
| Boundary 4 in its network form: the token's reach, the policy file, the exclusive `ca_file`, the loopback `http` exception, the digest of associated data, each with its test | 12.1 §11 | Done in the review: TB-4, (a) to (e), each with its test of 12.1 §9; the token's reach is a residual exposure restated in the record |
| The transit client's answer parser joins the fuzz targets | 12.1 §11, ADR 0073 | T-1105 |
| `exportable` may become a refusal with an opt-out | 12.1 O-13 | Yes: §5.7, D-5; the maintainer confirms at O-1 |
| Exact-case member matching in both families | epic 3 history, "not scheduled" | Fixed here as F-1: the review found it reachable with compatible providers |
| The scraper's credential reads more than metrics | epic 6 history, "to be scheduled" | Deferred to sprint 10.2 (F-20, O-5) |
| A strict switch for a key file readable by others | epic 2 history | Stays in epic 10; not a finding of this review (a warning exists and is tested) |
| The pending platform-issued password gap; breached passwords; HSTS | epic 4 history, security model | Stay in sprint 10.2; restated in the record with their acceptance dates |
| "Verify a tagged release (signature, SBOM, provenance)" as a maintainer action outside the repository | epic 1 history | The `verify` job does it at every release since; the maintainer's own verification of the newest release by digest is plan F4 and goes into the record |
| `limits.time_zone` equal on every instance; settings for a chart | epic 4 and 5 history, "epic 12 (Helm)" | Not this sprint: sprint 8.2 (the chart) and 12.3 (an instance defined by configuration alone) |

## 6. Isolation and permissions

No table, policy, route or permission is added or changed. The cross-tenant suite is unchanged; the
review names its tests as proofs (TM-02, TB-3). The two code fixes that touch a request path are on
the inference listener before any tenant data is read (§5.5) and in the egress guard (§5.6).

## 7. Test infrastructure and CI

- Fuzz functions live in the `_test.go` files of their packages and need no database, no network
  and no container; each also runs its seeds under `go test ./...`.
- `test/repo/threatmodel_test.go` parses the two Markdown tables of the model and the record with
  the helpers the repository tests already use (`read`, `root`), and finds test functions with
  `go/parser` as T-R30 does. `test/repo/supplychain_test.go` reads the workflows, the Compose files,
  `dependabot.yml` and `.gitleaks.toml` as text; no YAML library is added to the repository tests
  beyond what they import today.
- CI gains the job `fuzz` in `ci.yml` and the workflow `scheduled.yml` (§5.10, §5.11). Every action
  used in them is already pinned in `ci.yml`; the pins are copied, not looked up.
- The scheduled workflow fails red on a finding; GitHub notifies the maintainer who last changed its
  schedule. No issue is opened automatically (F-21).

## 8. Decisions

Each is recorded in ADR 0074 unless another record is named.

| # | Decision | Reason |
|---|---|---|
| D-1 | Every threat, boundary and statement of the model has a permanent identifier and names code and proof in the model itself; a repository test holds both; a review is a dated record file linked from the model | The exit criterion, made impossible to rot: a renamed test or a threat without proof fails CI |
| D-2 | A member that differs only in case from a member Pandokos reads makes a request invalid (`invalid_request`, reason `member_case`). **Amends ADR 0037** | The model that is authorized must be the one the provider sees, for providers that match names without case too; refusing is the only answer that never re-serializes the body |
| D-3 | The forbidden addresses gain the metadata addresses confirmed in plan C2. **Amends ADR 0038** | "A private endpoint cannot be on a metadata address" is a list, and the list was short |
| D-4 | The two controls the HTTP stack provides (strict framing, header limits) get tests of ours and no code | A control the model names must fail a test when it goes; the suite runs on two toolchains |
| D-5 | An exportable key of the key service is refused at start unless `vault.kek.transit.allow_exportable_key` is true; the other weak settings stay warnings. **Amends ADR 0073** (12.1 D-3) | The setting cannot be undone in the service and lets another token read the key in clear; v0.3.0 is not released, so the default can be the safe one at no cost to anyone (O-1) |
| D-6 | A parser of network input has a fuzz target, and the model lists them; pure parsing is separated from I/O where it was inline | The epic; a list in the model under a test, so a new parser without a target is seen |
| D-7 | Fuzzing runs natively (`go test -fuzz`) from `make fuzz`: 10 s per target on every pull request, 3 min per target every night; a crash fails the job; inputs that failed are committed as regression seeds; no corpus is kept between runs | Bounded cost on a pull request, depth at night, one list of targets; nothing new to trust |
| D-8 | No fuzzing service or third-party fuzzing action | ADR 0018: nothing is added that can be done with the toolchain; a service is reconsidered with the assessment (O-4) |
| D-9 | `govulncheck`, a scan of the released image and a reading of the repository's rulesets run every night, in the same scheduled workflow | A vulnerability is published on its own calendar, not on the repository's; a protection that is switched off must turn something red |
| D-10 | The release is verified by digest, by the pipeline and by the operator; the digest pushed is the digest scanned; the tag must be annotated; no binary is an artefact. **Amends ADR 0018** | State what is produced, and check the thing that is published |
| D-11 | Every pinned reference (action, image, tool, module directory) is under one repository test and under automated update; every secret shape the log redacts is a rule of the secret scan | One rule instead of one test per file added so far |
| D-12 | Residual exposures are not re-decided here: each is restated in the record with the date it was accepted, and the pending-password gap and the scraper's credential keep sprint 10.2 as their target | A review states what is; acceptance is the maintainer's and already given |
| D-13 | No migration, no OpenAPI change, no permission, no error code, no new module, no new action | Nothing found needs one |

## 9. Tests

Levels: **U** unit; **F** fuzz target (its seeds also run as a unit test); **D** needs the test
database; **E** the built binary, end to end; **R** repository test. IDs continue from T-1094 and
T-R88.

### New tests

| ID | Test | Level | Asserts | Requirement |
|---|---|---|---|---|
| T-1095 | `family.TestCaseConflict` | U | For a read set: exact names pass; `Model`, `MODEL`, `sTream` and a name with a non-ASCII letter that folds to an ASCII one are conflicts; an escaped spelling of the exact name is the exact name; names outside the set and nested members are ignored | CAT-06 |
| T-1096 | `openai.TestParseMemberCase` | U | Each member of `ReadMembers` and of `ReadStreamOptions` in another case, alone and beside the exact one, is `invalid` with reason `member_case`; the read sets equal the names the parser passes to the `family` helpers (the test reads the package's source) | CAT-06 |
| T-1097 | `anthropic.TestParseMemberCase` | U | The same for the `anthropic` family | CAT-06 |
| T-1098 | `gateway.TestMemberCaseRefused` | U | Through the handler, both families: `400 invalid_request`, nothing reaches the provider, no credential is opened, one record with the invalid reason; the request line holds `invalid_reason: member_case` and no member name or value | CAT-06, GW-09 |
| T-1099 | `egress.TestClassifyMetadata` | U | Every instance-metadata address of the table, the added ones included, is forbidden, also IPv4-mapped; a `private` endpoint and a private webhook host on each are refused at dial and at validation | GW-06 |
| T-1100 | `vault.TestTransitExportable` | U | With the double: an exportable key fails `Start` with the fixed text, as a permanent cause, and sends no wrap; with the option it starts and reports `exportable` among the weak settings; the four other weak settings still start without the option | SEC-04, VLT-12 |
| T-1101 | `config.TestTransitAllowExportable` | U | The key, its default, its environment name, its place in the reference; fourteen keys in the block | OPS-08 |
| T-1102 | `e2e.TestTransitExportableRefused` | E | The binary with an exportable key of the double ends with 1 within 10 s and a constant message, listening on nothing; with the option it serves and logs the warning; `kek check` prints the class and ends with 1, then 0 with the option | SEC-04 |
| T-1103 | `server.TestStrictParsing` | U | §5.8, first bullet, on both listeners | GW-07 |
| T-1104 | `server.TestHeaderLimits` | U | §5.8, second bullet, on both listeners | GW-16 |
| T-1105 | `vault.FuzzTransitAnswer` | F | §5.9 | VLT-12, VLT-14 |
| T-1106 | `jwks.FuzzParseKeySet` | F | §5.9 | IAM-06 |
| T-1107 | `jwks.FuzzMetadata` | F | §5.9 | IAM-06 |
| T-1108 | `oidc.FuzzProviderAnswers` | F | §5.9 | IAM-09 |
| T-1109 | `genaiprices.FuzzParse` | F | §5.9 | — |
| T-1110 | `admin.FuzzDecodeCursor` | F | §5.9 | — |
| T-1111 | `adminapi.FuzzDecodeBody` | F | §5.9 | SEC-03 |
| T-1112 | `adminapi.FuzzMatch` | F | §5.9 | — |
| T-1113 | `admin.FuzzOrigin` | F | §5.9 | IAM-20, IAM-21 |
| T-1114 | `outbox.FuzzValidateURL` | F | §5.9 | — |
| T-1115 | `egress.FuzzClassify` | F | §5.9 | GW-06 |
| T-1116 | `auditchain.FuzzCanonical` | F | §5.9 | AUD-04 |
| T-1117 | `openai.FuzzInboundCredential` | F | §5.9 | — |
| T-1118 | `anthropic.FuzzInboundCredential` | F | §5.9 | — |
| T-R89 | `repo.TestThreatModelTraceability` | R | §5.4: headers, identifiers, existing paths, proofs that resolve to one test each, the status line and the newest record | SEC-05 |
| T-R90 | `repo.TestThreatModelReview` | R | §5.4: the record's header, one row per identifier of the model, the closed sets, tests that exist, targets for what is deferred, acceptances that are not empty | SEC-05 |
| T-R91 | `repo.TestNetworkParsersAreFuzzed` | R | Every row of the model's parser table names a function that exists in the named package and a fuzz target that exists and is in `make fuzz`; every fuzz target of the Makefile is in the table | SEC-05 |
| T-R92 | `repo.TestFuzzRunsInCI` | R | §5.10, last bullet | SEC-05 |
| T-R93 | `repo.TestEverythingPinned` | R | Every `image:` of every Compose file in the repository and every `FROM` of every Dockerfile is `name:tag@sha256:<64 hex>`, except the two variables for the Pandokos image named in the test; every `go run` and `go install` of a module in a workflow or the Makefile names an exact version; no workflow uses `@latest`, `@main` or `@master` anywhere | SEC-06 |
| T-R94 | `repo.TestDependabotCoverage` | R | Every directory that holds a `go.mod`, a Compose file with a pinned image, a `Dockerfile`, a `requirements.txt` or a `package.json`, and `.github/workflows`, has an entry of the right ecosystem in `dependabot.yml`, and no entry names a directory that does not exist | SEC-06 |
| T-R95 | `repo.TestReleaseVerifiesWhatItPublishes` | R | `release.yml`: `check-tag` tests the tag's object type; every `cosign` and `gh attestation` command names `${IMAGE}@${DIGEST}`; `verify` scans that reference and `publish` needs `verify`. The container guide: each command of "Verifying a release" names `@sha256:` and none names a tag alone; each verification of the workflow has its command in the guide. ADR 0018 no longer says binaries are signed | SEC-06 |
| T-R96 | `repo.TestSecretShapesAreScanned` | R | Each pattern of a secret Pandokos issues in `internal/telemetry/redact.go` (key, session token, signing secret) is the `regex` of a rule of `.gitleaks.toml`, character for character | VLT-14 |
| T-R97 | `repo.TestScheduledChecks` | R | `scheduled.yml` has a `schedule` trigger, the jobs `fuzz`, `vuln`, `image` and (unless plan D2 recorded that the token cannot read rulesets) `settings`, the repository condition, no `continue-on-error`, no permission beyond `contents: read`; the `govulncheck` version equals the Makefile's | SEC-06 |

### Changed tests of earlier sprints

| Test | Change |
|---|---|
| `openai.FuzzParseRequest`, `anthropic.FuzzAnthropicParseRequest` | the invariant of §5.9, last row; seeds with members in another case |
| T-421 `openai.TestParse`, `anthropic.TestParseRequest`, `family.TestInvalidReason` | the reason `member_case` |
| `gateway.TestInvalidRequest`, the record tests that list invalid reasons | one more reason |
| `egress.TestClassify`, `outbox.TestValidateURL`, `jwks.TestTransportClass` | the added addresses in their tables |
| T-1060 `vault.TestTransitKeyRequirements` | `exportable` moves from the warnings that start to the refusals, and stays a warning with the option |
| T-1078 `config.TestTransitKeys`, T-1080 `main.TestKEKCheck`, T-1085 `e2e.TestTransitStartRefusals` | fourteen keys; one more refusal |
| T-R86 `repo.TestTransitDocuments` | the configuration reference lists fourteen keys; the guide's section on the key names the refusal |
| T-R30 `repo.TestFuzzTargetsAreRun` | fuzz functions anywhere in the root module, not only under `internal/` |
| T-R39 `repo.TestReleaseR1` and the tests that read the security model's table by position | read the new columns (the implementer greps `security-model.md` in `test/` first) |
| `repo.TestActionsPinned`, `repo.TestReleaseWorkflowShape` | unchanged in rule; they now also cover `scheduled.yml` and the scan step |

### Requirement coverage

| Requirement | Tests |
|---|---|
| SEC-05 (reviewed) | T-R89, T-R90, T-R91, T-R92 |
| SEC-06 (touched) | T-R93, T-R94, T-R95, T-R97 |
| SEC-04, VLT-12 (touched) | T-1100, T-1101, T-1102 |
| CAT-06 (touched) | T-1095 to T-1098 |
| GW-06 (touched) | T-1099, T-1115 |
| GW-07, GW-16 (tests) | T-1103, T-1104 |
| VLT-14 (touched) | T-1105, T-1111, T-R96 |

## 10. Definition of done for this sprint

- [ ] Every test of §9 exists under its name and passes; T-1095 to T-1102 and T-R89 to T-R97 failed
      before their implementation; the changed tests are changed as listed.
- [ ] **Exit:** every row of the two tables of the security model names code and proof, and T-R89
      passes; the record exists with a row per identifier and a decision per finding, and T-R90
      passes.
- [ ] **Exit:** the 34 fuzz targets run in the `fuzz` job of `ci.yml` on the pull request of this
      sprint. The commands of the jobs of `scheduled.yml` were run locally before the merge; the
      workflow itself can be dispatched only from the default branch, so its first run is the
      maintainer's, right after the merge, and its result is added to the record (plan F3).
- [ ] `make fuzz FUZZTIME=3m` was run once locally and found nothing, or what it found is fixed and
      its input committed.
- [ ] Each finding F-1 to F-21 has the outcome of §5.3; nothing was fixed silently beyond them, and
      anything else found while implementing is in the record as a finding with a decision.
- [ ] No secret, token or key in any seed, corpus file, fixture or document; `make secrets` passes
      with the two new rules on the whole history.
- [ ] An installation without a key service, and one with a key that is not exportable, behave as
      before; a request body without a member in another case is forwarded byte for byte as before.
- [ ] `make check` passes; the image job and the transit job pass on both legs.
- [ ] The security model, the record, ADR 0074 and the history lines of 0018, 0037, 0038 and 0073,
      the container, gateway, external-key, Compose and configuration guides, the requirements page
      (SEC-05 "reviewed"), the roadmap, the epic file and the draft release notes of v0.3.0 are
      current.
- [ ] The maintainer has run the commands of plan F4 and their summary is in the record; the two
      rulesets are `active` or the record says why not and until when (O-9); the acceptances of the
      record carry the maintainer's date.

## 11. Consequences for later sprints

- **Every later sprint.** A threat or a control added to the model comes with its code path and its
  test, or T-R89 fails; a parser of network input comes with its fuzz target and its row, or T-R91
  fails; a new workflow, Compose file, Dockerfile or nested module comes pinned and with its
  dependabot entry, or T-R93 and T-R94 fail. A test named as a proof cannot be renamed without the
  model.
- **12.3 (restore drill, upgrade policy).** The drill with a key service uses a key that is not
  exportable, or sets the option and says why. The upgrade policy states that the threat model is
  reviewed at every release: a new record file per release (`threat-model-review-YYYY-MM.md`), which
  may be short when nothing changed. The two instances started from two configurations may differ
  in `allow_exportable_key`.
- **10.2 (assessment and privacy).** The record is the assessor's map: identifiers, code, proofs,
  residual exposures with their dates, and the list of what this review did not verify (§12). It
  inherits F-20 (the scraper's credential), the pending-password gap, O-2 if the tag's signature is
  not verified by then, O-3 (link-local for private endpoints) and O-4 (a fuzzing service with
  private reports). Its findings become a new record.
- **9.x (more providers).** A new family declares its read set (§5.5) and gets the fuzz targets of
  its request parser, usage reader, stream reader and credential header, with their rows. A provider
  known to match member names without case is the reason for F-1 and needs nothing more.
- **8.2 (Kubernetes).** The chart carries `allow_exportable_key`; images in the chart and its tests
  are pinned by digest (T-R93 reads every Compose file and Dockerfile; a chart's values need their
  own line in that test). The scraper's credential is decided before the chart ships a
  `ServiceMonitor` (F-20).
- **8.1 (shared counters).** The Valkey client's answer parser is a library's; our decoding of
  counter values is a parser of network input and joins the table.
- **Release v0.3.0.** The notes say: a body with a member in another case is refused; an exportable
  key of the key service is refused unless allowed; verification is by digest.

## 12. Risks and open points

### What was verified while designing

At commit `96cb793c0359b339c4c98785c29a0776e2dada0f`, by reading the repository:

| Fact | How |
|---|---|
| The model has 8 boundaries, 3 unnumbered statements and 57 threat rows | reading `docs/architecture/security-model.md` |
| Every test named in §5.2 exists as a function of the named package | the index of test functions (`grep -rn '^func \(Test\|Fuzz\)' --include=*_test.go internal test cmd`) |
| `ParseModel` and `LastMember` compare names with `==`; names are decoded (an escaped `model` is `model`) | reading `internal/family/jsonbody.go` |
| The request header allow-lists hold no `Accept-Encoding`, and the transports disable compression | `internal/family/*/route.go`, `internal/egress/guard.go` |
| `Classify` unmaps IPv4-mapped addresses, follows NAT64 and 6to4, and forbids four metadata addresses | reading `internal/egress/classify.go` |
| The `http` exception of the key service is enforced on the address dialled, not on the name | `egress.Guard.KeyService`, `loopbackControl` |
| An error answer of the key service is read to a limit and dropped; a good one is bounded and its buffer cleared | `internal/vault/transit_client.go` (`exchange`) |
| `weakSettings` lists five settings; `checkKey` refuses four conditions; `exportable` is among the warnings | the same file |
| The policy file grants six paths and nothing that rotates, exports, trims or deletes | `deploy/transit/pandokos-policy.hcl` |
| Both listeners set header and idle timeouts and a header size limit from configuration; the admin listener also read and write timeouts | `internal/server/server.go` |
| Twenty fuzz functions exist, all in `make fuzz` (T-R30); no workflow runs `make fuzz`; one committed regression input exists | `Makefile`, `.github/workflows`, `git ls-files` |
| `declare.Parse` is called by the command line only | `grep -rn 'declare\.Parse' internal cmd` |
| `internal/adminapi/runtime.go` and its tests are hand-written; only `adminapi.gen.go` is generated | the file's header, `make generate` |
| The workflows: two files, every action pinned by commit, top-level `contents: read`; `dependabot.yml` has ten entries and none for `test/transit`; no schedule exists anywhere | reading them |
| The release workflow signs, attests, verifies and publishes by digest; it scans nothing itself; it does not look at the tag object | reading `release.yml` |
| The guide's three verification commands name the tag | `docs/operations/container.md` |
| On GitHub, on 2026-11-24: `main` is not protected; two rulesets with the expected rules have `enforcement: disabled` (changed 2026-11-23); secret scanning and push protection are enabled; private vulnerability reporting is enabled; Dependabot security updates are disabled; no open Dependabot alert; the tag `v0.2.0` is an annotated tag | `gh api repos/pandokos/pandokos/branches/main/protection`, `…/rulesets` and each ruleset, `…/private-vulnerability-reporting`, `gh api repos/pandokos/pandokos --jq .security_and_analysis`, `…/dependabot/alerts`, `…/git/ref/tags/v0.2.0 --jq .object.type` |
| `.gitleaks.toml` has one custom rule (`pdk_`); no file of the repository matches the session or signing-secret patterns today | `git grep -E` with the two patterns of `redact.go` |
| The highest identifiers in use are T-1094 and T-R88; the next ADR number is 0074 | `grep -rho` over `planning`, `docs`, `test`, `internal`, `cmd`; `docs/adr` |

Not verified: the body of each test named in §5.2 (plan E1 opens each); any Go code of this design;
that the three candidate metadata addresses are what the vendors document today (plan C2); how the
HTTP stack of each toolchain answers the cases of T-1103 (first run of the test); a member-case
difference against a real provider; the Actions settings of the repository (default workflow permissions; not readable from this session); that the history
holds no session token or signing secret (the first run of `make secrets` with the new rules);
the time the `fuzz` job really takes on a runner; cryptographic code beyond its tests; the
dependencies' own code; timing side channels; the browser client, which does not exist.

### Risks

| # | Risk | Mitigation |
|---|---|---|
| R-1 | The review is done by the people who wrote the code and shares their blind spots | The record says so and lists what was not verified; proofs are tests, not opinions; sprint 10.2 is the independent assessment and reads the record |
| R-2 | A test named as proof does not assert what the row says: the name was right and the content was not | Plan E1 opens each before writing it; the reviewer of the sprint samples the table against §5.2; T-R89 holds existence only, and the record says that |
| R-3 | The member-case rule refuses a client that works today | Such a body is refused or misread by the real providers too; the reason is logged as `invalid_reason`; the release notes say it; the rule looks only at names Pandokos reads |
| R-4 | A fuzz crash shows a weakness in a public log before it is fixed | The input is not printed or uploaded; the nightly run is on `main` only; SECURITY.md process for an exploitable one; O-4 |
| R-5 | The `fuzz` job makes pull requests slow or flaky | 10 s per target, its own job in parallel with the others; a target that cannot hold its deadline on a runner is fixed, not skipped; no `continue-on-error` (T-R92) |
| R-6 | A fuzz target finds a panic in a dependency (JOSE, YAML is not fuzzed, JSON) and blocks the sprint | It is a finding: bounded by our own checks where possible, reported upstream, recorded; a dependency's panic reachable from the network is exactly what the sprint is for |
| R-7 | Refusing an exportable key stops an installation that already uses one | None is released; the option exists; the message names it |
| R-8 | The traceability test makes ordinary refactoring heavier (a renamed test fails a document test) | That is its purpose; the failure message names the row and the missing function |
| R-9 | The scheduled workflow fails and nobody looks | It notifies the maintainer; the release page's check list (CONTRIBUTING, "Releasing") gains "the last scheduled run is green"; F4 runs it once before the close |
| R-10 | An added metadata address is wrong and blocks a legitimate private endpoint | Only addresses confirmed in the vendor's documentation, each a single address, each with its source in ADR 0074 |
| R-11 | The extraction of the three inline parsers changes behaviour | Statements are moved, not rewritten; the existing tests of the three functions pass unchanged, which is the check |

### Open points (raised to the maintainer; none blocks the sprint)

| # | Question | Recommendation |
|---|---|---|
| O-1 | 12.1 O-13 was confirmed as "warnings, not refusals; 12.2 may revisit `exportable`". The review revisits it: refuse an exportable key by default, with `vault.kek.transit.allow_exportable_key`? | Yes (D-5): the setting is irreversible in the service and hands the key to any token allowed to export; nothing is released yet, so the safe default costs nobody an upgrade. If declined, drop plan C4, T-1100 to T-1102 and the changed tests of 12.1, and the record keeps F-8 as accepted |
| O-2 | Should the release workflow verify the signature of the tag? It needs the maintainer's public signing key committed (an allowed-signers file) | Yes, as a follow-up the maintainer starts by providing the key: one file and one `git verify-tag` step. Until then the workflow checks that the tag is annotated and on a protected branch's history, and the record says the signature is checked by the maintainer |
| O-3 | Should link-local addresses be forbidden for every class, instead of listing metadata addresses one by one? Every cloud puts instance credentials there; a model server there is implausible | Yes, but not in this sprint: it changes GW-06's wording and a `private` endpoint's contract. Decide for 10.2; the list is extended now (D-3) |
| O-4 | A fuzz crash is visible in a public log. Use a service that reports privately (OSS-Fuzz), or run the nightly job in a private mirror? | Accept for now (F-21); reconsider with the assessment of 10.2, when the project may meet OSS-Fuzz's criteria |
| O-5 | The scraper's credential (epic 6, carried forward): when? | Sprint 10.2 at the latest, and before sprint 8.2 ships a `ServiceMonitor`; a role holding `metrics:read` alone is the smaller change |
| O-6 | The `fuzz` job as a required check on `main`? | Yes, after a week of green runs; it is a repository setting (plan F4 lists it) |
| O-7 | The message of `invalid_request` names `model` and background processing only; a body refused for `member_case` gets the same text | Keep the text (ADR 0020 fixes it and SDK tests read it); the reason is in the operator's log. Changing the message is a contract change for a later release |
| O-8 | The record's acceptances need the maintainer's date. Accept the residual exposures as restated, without change? | Yes: none is new; the list is §5.2's "A" entries and F-18, F-19, F-21 |
| O-9 | The two rulesets (main and release branches; version tags) are disabled since 2026-11-23, so ADR 0018's "protected main branch, required reviews and status checks" does not hold. Enable them? | Yes, both, before the close of this sprint: the rules are already the right ones and the administrator role keeps its bypass. If they must stay off for a time, the record says why and until when, and the nightly `settings` job stays red on purpose. Also enable Dependabot security updates |

### Maintainer decisions on the open points (2026-11-24)

| # | Decision |
|---|---|
| O-1 | Confirmed: an exportable key of the key service is refused by default; `vault.kek.transit.allow_exportable_key` opts out (D-5, plan C4) |
| O-2 | Confirmed as a follow-up: until the maintainer commits the public signing key, the workflow checks that the tag is annotated and reachable from a protected branch, and the record says the signature is checked by the maintainer |
| O-3 | Confirmed: the metadata list is extended now (D-3); forbidding all link-local addresses is decided in 10.2 |
| O-4 | Accepted for now (F-21); reconsidered with the assessment of 10.2 |
| O-5 | Confirmed: the scraper's credential by sprint 10.2 at the latest, and before 8.2 ships a `ServiceMonitor` |
| O-6 | Confirmed: `fuzz` becomes a required check after a week of green runs (maintainer action) |
| O-7 | Confirmed: the `invalid_request` message is unchanged; the reason `member_case` is in the operator's log |
| O-8 | Confirmed: the residual exposures are accepted as restated; none is new |
| O-9 | Confirmed: both rulesets and Dependabot security updates are to be enabled by the maintainer before the close of this sprint (repository settings; maintainer action) |
