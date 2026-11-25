# Security model

- Status: reviewed against the code on 2026-11-25 (sprint 12.2): [review record](threat-model-review-2026-11.md). Reviewed at every release and independently before 1.0 (SEC-05, SEC-09).
- Decisions: [ADR 0005](../adr/0005-data-plane-authentication.md),
  [ADR 0008](../adr/0008-vault-envelope-encryption.md),
  [ADR 0018](../adr/0018-supply-chain.md),
  [ADR 0056](../adr/0056-users-memberships-and-sessions-as-principals.md) to
  [ADR 0059](../adr/0059-idempotency-keys.md),
  [ADR 0074](../adr/0074-threat-model-review-and-continuous-fuzzing.md)
- Identifiers: every boundary (`TB-n`), statement outside the list (`TS-n`) and threat (`TM-nn`)
  has a permanent identifier. Each row names the code that implements its control and the tests
  that fail when the control is removed; the repository test `TestThreatModelTraceability` keeps
  both lists true. A threat or a control added to the model comes with its code path and its
  test. A review is a dated record that the status line links.

## What is protected

| Asset | Why it matters |
|---|---|
| Provider credentials of tenants | They are spendable money and access to the tenant's provider account. They cannot be recreated by the platform |
| Tenant separation | One tenant must not see or affect another's configuration, usage or spend |
| Usage ledger and audit log | They are the evidence for what was spent and for investigations |
| A tenant's usage records, limit events and price lists | They describe what a tenant consumes and what it pays. Through the product only the tenant reads them: row-level security on their tables, tenant-scoped permissions that no platform role holds, no platform operation or definer function that returns them, and a request log and traces that hold no tenant data: no tenant or caller identifier, model, usage, cost or limit rule ([ADR 0054](../adr/0054-usage-data-is-the-tenants.md), [ADR 0055](../adr/0055-operational-telemetry-holds-no-tenant-data.md)). The platform sees operational indicators of the installation, with no tenant and no cost. The metrics are aggregates per installation, model and currency with no tenant, caller, key or request attribute; the one identifier is the provider credential ID on the counter of the provider's 429 answers, which leads to a tenant only through the audited `tenant_credential:read`; and where a single tenant uses a model or a currency, the total for it is that tenant's, a residual the operator accepts ([ADR 0062](../adr/0062-metrics-instruments-attributes-cardinality-and-scrape.md)) |
| Webhook destinations and their signing secrets | A destination is a way out for a tenant's usage data, and its secret lets a sender be believed. A secret that leaks lets a third party forge events to the tenant's receiver; a destination that is redirected sends a tenant's events elsewhere |
| Content in transit | Prompts and completions pass through memory and must leave no trace |
| Availability and spend | A caller must not exhaust the service or a budget that is not theirs |

## Trust boundaries

```mermaid
flowchart LR
    caller[Caller] -- 1 --> gw[Gateway]
    admin_user[Administrator] -- 2 --> adm[Admin API]
    gw -- 3 --> db[(Database)]
    adm -- 3 --> db
    gw -- 4 --> kek[KEK provider]
    gw -- 5 --> provider[Provider]
    gw -- 8 --> otel[OTLP collector]
    idp[Identity provider] -- 6 --> gw
    infra[Infrastructure operator] -. 7 .-> gw
    infra -. 7 .-> db
    infra -. 7 .-> kek
```

1. **Caller to gateway.** Untrusted. Everything the caller sends is input: path, headers, body,
   metadata, trace context. Only a verified credential establishes identity and tenant.
2. **Administrator to admin API.** People hold a session, obtained through the identity
   provider or a local account: a server-side session in a cookie that scripts cannot read, read from
   the database at every request, accepted on this listener only ([sign-in guide](../operations/sign-in.md),
   [ADR 0056](../adr/0056-users-memberships-and-sessions-as-principals.md),
   [ADR 0057](../adr/0057-local-accounts-and-sessions-formats-and-numbers.md)). Automation uses
   administrator keys; tokens of trusted issuers are accepted here too when the operator sets
   `tokens.admin_listener`, as service accounts only. A request carries one credential, chosen in a fixed
   order (a public sign-in operation reads none; an `Authorization` header is a key or a token and a cookie
   beside it is ignored; otherwise the cookie). A state-changing request with a session needs a custom
   header and an `Origin` equal to the expected origin, and a listener known to be in clear text outside
   loopback refuses to sign anyone in until the operator names the HTTPS origin in front of it
   (`auth.public_origin`). Every operation is authorized. A client that runs in the user's browser is
   untrusted: what it hides is not a control. The admin port is not meant for the public internet.
   The optional scrape endpoint `GET /metrics` (`telemetry.metrics.prometheus`) is on this listener, outside
   the API document, and is never anonymous: it needs a platform key (or a platform-level issuer's
   token) whose role holds the platform permission `metrics:read`, held by `platform-admin` and
   `platform-auditor`; tenant principals and sessions are refused. Platform keys carry no scopes, so a scraper's
   `platform-auditor` key can also read tenants, users, platform keys, issuers and support actions and is
   protected as an auditor credential ([telemetry guide](../operations/telemetry.md#10-the-scrape-endpoint)).
3. **Process to database.** Each role connects with its own least-privileged database role. The
   database enforces tenant isolation independently of the application.
4. **Process to KEK provider.** Whoever holds the key-encryption key and the database can decrypt
   every credential. The two are stored and backed up apart. With a key service this boundary is a network
   one: Pandokos holds a token or an AppRole in files, not the key, and sends the service 32-byte values to wrap
   and unwrap with a digest of the associated data, over `https` with the certificate verified, through a
   transport of its own and no proxy. Whoever holds the token and the database can unwrap every data key
   **while the token lives**, and the service sees no tenant, credential or caller
   A key that was created `exportable` in the service, which any token allowed to export can read in clear
   and which cannot be made otherwise afterwards, is refused at start unless the operator sets
   `vault.kek.transit.allow_exportable_key`
   ([external key guide](../operations/external-key.md), [ADR 0073](../adr/0073-the-transit-key-provider.md),
   [ADR 0074](../adr/0074-threat-model-review-and-continuous-fuzzing.md)).
5. **Gateway to provider.** Outbound only, to hosts in the catalogue, with certificate
   verification.
6. **Identity provider.** Trusted to assert identities, and, for platform-level issuers, tenants.
   A compromised identity provider can impersonate what it is trusted to assert, which is why
   issuers are scoped (IAM-07). A tenant-bound issuer is trusted for one tenant only. A platform-level
   issuer is trusted for the tenants of its explicit list, by the claim value listed for each, and for
   nothing else; only a platform administrator edits the list, and every edit is audited. A token is
   verified offline with keys that the gateway fetches from an address an administrator registered:
   `https` only, through the egress guard, public addresses unless the operator lists the host in
   `tokens.private_hosts`, with size, time and rate limits, and nothing of the answer returned or
   logged. The two things a registration cannot protect are named here. First, a shared issuer is
   separated between tenants by the audience alone: the pair (issuer, audience) is unique in the
   installation, so a tenant cannot register a pair another tenant uses, but if the workloads of
   tenant A present an audience that A has not registered or no longer registers, another tenant can
   register that pair and its mapping would then receive those calls (the residual case of the
   [tokens guide](../operations/tokens.md), section 5; the guide requires an audience naming the
   installation and the tenant). Second, a token cannot be revoked before it expires. Changing what a
   tenant's issuer trusts (issuer, audience, key set, algorithms, subject or required claims) or enabling
   it again is a grant: the caller must hold what the mapped service accounts can do (`403
   escalation_refused` otherwise, [ADR 0060](../adr/0060-built-in-roles-custom-roles-and-the-rule-against-escalation.md)).
   A platform-level issuer is not guarded this way: a platform administrator can repoint one that
   tenants map subjects to and act as those service accounts. This is an accepted residual exposure,
   since the platform runs the installation and already holds its database and keys
   ([ADR 0061](../adr/0061-support-actions-and-reasons-how-the-platform-acts-inside-a-tenant.md)); every
   change is in the platform's audit log. Accepted by the maintainer on 2026-11-23.

   **The identity provider for people** is a second use of this boundary. The operator configures one
   provider (`auth.oidc.*`); Pandokos is its confidential client with PKCE, state and nonce, and trusts it
   to assert who a person is and, through explicit group rules in the configuration, which role the person
   holds. A compromised provider can sign in as any person it knows, and the rules give each group no more
   than the operator wrote: `platform-admin` and `tenant-admin` only, in tenants that exist. The
   administration role fetches the provider's documents through the egress guard, of the one configured
   issuer only; the ID token is validated, its subject read and every provider token dropped at once
   ([ADR 0058](../adr/0058-openid-connect-relying-party.md)).
7. **Infrastructure operator.** See the next section.
8. **Gateway to trace and metrics collector.** Outbound only, to the one URL the operator configured, through
   the same guard as provider calls (certificates verified, no proxy, no redirect, forbidden
   addresses refused). It receives spans that carry only the attributes the OpenTelemetry conventions make Required, never a tenant identifier or content,
   a header value or caller metadata, and the metrics of the [metrics reference](../reference/metrics.md), which
   name no tenant, caller, key, request or trace ([telemetry guide](../operations/telemetry.md),
   [ADR 0040](../adr/0040-traces.md), [ADR 0062](../adr/0062-metrics-instruments-attributes-cardinality-and-scrape.md)); what each destination receives is listed in
   [data flows](../reference/data-flows.md).

| ID | Boundary | Controls | Code | Proof |
|---|---|---|---|---|
| TB-1 | Caller to gateway | Everything the caller sends is input until a credential verifies: the pipeline authenticates before it reads anything else of the request | `internal/gateway/handler.go`, `internal/authn/authn.go` | `gateway.TestUnauthenticated`, `gateway.TestPipelineOrder`, `gateway.TestNothingBeforeAuthentication` |
| TB-2 | Administrator to admin API | One credential per request, sessions on this listener only, CSRF checks before the body, a plain-text refusal on a listener in clear text, every operation authorized, `/metrics` never anonymous. Residual: the scraper's `platform-auditor` key reads more than metrics (accepted, deferred to sprint 10.2) | `internal/admin/admin.go`, `internal/admin/origin.go`, `internal/server/server.go` | `admin.TestEveryOperationNeedsACredential`, `admin.TestCredentialPrecedence`, `admin.TestCSRF`, `admin.TestSignInTransport`, `admin.TestTokensOnAdminListener`, `e2e.TestScrapeEndpoint` |
| TB-3 | Process to database | One least-privileged role per plane; forced row-level security that binds the owner too | `internal/store`, `internal/store/migrations` | `isolation.TestRuntimeRolesCannotEscape`, `isolation.TestOwnerIsSubjectToPolicies`, `e2e.TestServeRefusesPrivilegedUser`, `store.TestCheckRefusesWrongUser`, `store.TestTenantContextDoesNotLeak` |
| TB-4 | Process to key provider (network form) | (a) the token's reach is bounded by the policy file shipped in `deploy/transit` and by short tokens; (b) `ca_file` is exclusive; (c) `http` only to a loopback address, checked on the address dialled; (d) only a digest of the associated data leaves; (e) the binding of associated data is proved at start; (f) a key created `exportable` is refused unless `vault.kek.transit.allow_exportable_key` is true. Residual: whoever holds the token and the database unwraps every data key while the token lives (accepted) | `internal/egress/guard.go`, `internal/vault/transit.go`, `internal/vault/check.go` | `transit.TestConformance`, `e2e.TestTransitTokenLifetime`, `egress.TestKeyServiceTransport`, `vault.TestTransitAssociatedData`, `vault.TestCheckAssociatedDataBound`, `vault.TestTransitExportable` |
| TB-5 | Gateway to provider | Destination from the catalogue, certificates always verified, no redirect, no proxy read from the environment | `internal/egress/guard.go`, `internal/gateway/proxy.go` | `egress.TestTLS`, `gateway.TestRouteByProvider`, `gateway.TestRedirectNotFollowed`, `e2e.TestUpstreamCAAndProxy`, `repo.TestOutboundHTTPInOnePlace` |
| TB-6 | Identity provider (workloads and people) | Issuers scoped; keys fetched through the guard; a tenant issuer's change is a grant; the people's provider is one configured issuer and its tokens are dropped. Residuals: a shared issuer is separated by audience alone; a token cannot be revoked; a platform administrator can repoint a platform-level issuer (accepted 2026-11-23) | `internal/authn/tokens.go`, `internal/jwks/fetch.go`, `internal/oidc/flow.go` | `authn.TestTokenPrincipalTenantBound`, `isolation.TestPlatformIssuerVisibility`, `admin.TestNoEscalationCredentials`, `admin.TestIssuerAudit`, `oidc.TestTokensAreDropped`, `oidc.TestMetadata` |
| TB-7 | Infrastructure operator | Not a control but the statement of what is not protected. What reduces it: a bounded lifetime of plaintext and a key held outside the database. Residual: out of scope (issue #33) | `internal/vault/opener.go`, `internal/vault/unwrapcache.go` | `vault.TestOpenerCache`, `vault.TestUnwrapCache`, `e2e.TestBackupBeforeDeletionYieldsNothing` |
| TB-8 | Gateway to trace and metrics collector | One operator URL, through the guard; closed attribute lists | `internal/telemetry/traces.go`, `internal/telemetry/metrics.go` | `telemetry.TestExport`, `telemetry.TestCollectorNeverBlocks`, `telemetry.TestAttributesAreClosed`, `e2e.TestMetricsHoldNoTenantData`, `e2e.TestNoContentAnywhere` |

**The capability check** (`getCapability`, [ADR 0043](../adr/0043-tenant-capability-check-and-usage-list.md))
runs in the administration plane and reads only catalogue state and credential metadata
(`provider_credential_metadata`); it never opens a credential, holds no vault or key material and
audits nothing.

**The two functions that cross the tenant boundary** (sprint 5.3, [database guide](../operations/database.md),
section 16) are the only places where a database role acts across tenants for limit rules, and neither
reads a rule across tenants. `delete_platform_issuer_limit_rules`, executable by the admin role, is what the
platform session needs to delete a platform-level issuer: it refuses a session outside the platform scope,
works tenant by tenant as that tenant's session, can delete nothing but rules of mappings of the issuer
it is given, and writes the audit row of each in the tenant's log. `record_unlimited_traffic`, executable
by the gateway role, which has no privilege on either audit log, writes one fixed kind of row
(`limit.unlimited_traffic`) for the tenant of its session, with every argument checked. A tenant's
session may also update its own tenant row (policy `tenant_self_update`), and a trigger leaves it the time
zone and nothing else. Limit rules reference their subject, provider and model with foreign keys that
ignore row-level security, so the deletion of a provider or a model is refused for a rule of any tenant
without the deleting session seeing it, and without a function that reads rules across tenants.

**The Compose reference** ([guide](../operations/compose.md)) makes these trust choices: both listeners
use TLS from a self-signed certificate that `setup.sh` generates; ports are published on the host's
loopback only; PostgreSQL is on a network with no route out and is not published, and the server
verifies its certificate (`verify-full`); the Pandokos containers are read-only, drop all capabilities
and cannot gain privileges; secrets are files, never environment values. The secret files are
readable by every user of the host that can enter the `secrets` directory (the directory is mode 0700,
the files 0444, because the containers run as other users), so the vault reports the key-file
permission warning, which is expected there. The reference is for a first trial, not for production.

| ID | Statement | Controls | Code | Proof |
|---|---|---|---|---|
| TS-1 | The capability check opens no credential and audits nothing | It reads catalogue state and credential metadata only | `internal/admin/capability.go` | `admin.TestCapability`, `admin.TestCapabilityIsolation`, `e2e.TestCapabilityAgreesWithGateway` |
| TS-2 | The two functions that cross the tenant boundary read no rule across tenants | Definer functions that work tenant by tenant, with every argument checked; foreign keys that refuse a deletion without a function that reads rules | `internal/store/migrations` | `isolation.TestDeletePlatformIssuerLimitRules`, `isolation.TestRecordUnlimitedTraffic`, `isolation.TestRuleForeignKeys` |
| TS-3 | The trust choices of the Compose reference | Loopback ports, an isolated database network with a verified certificate, read-only containers, secrets as files. Residual: the Pandokos image follows a moving minor tag (accepted, F-18) | `deploy/compose/compose.yaml` | `repo.TestComposeReference`, `compose.TestQuickStart` |

## What Pandokos does not protect against

A person or process that controls the host, the container runtime, the database, the backups and
the key-encryption key can read tenant credentials: by decrypting them, or by reading the memory
of a running gateway. The rule that platform administrators cannot read tenant secrets is an
application and database control. It removes the capability from the product's interfaces and
makes misuse visible; it is not cryptographic separation from the organization that runs the
installation.

**Confidentiality of a tenant's data against whoever administers the database, the hosts or the backups is
out of scope (issue #33).** The owner role, a superuser, a backup or a host reads every table, the
usage records, limit events and price lists of a tenant among them. Row-level security separates
tenants from each other and from the platform roles of the product, not from the operator of the
installation. This sprint narrows what the product itself exposes; it does not encrypt usage data.

A tenant that needs separation from the operator's infrastructure needs its own cell, with its
own key-encryption key held where it decides.

Pandokos reduces this exposure by keeping plaintext in memory for a bounded time, by not storing
the key-encryption key with the data, by supporting a transit key service (OpenBao or HashiCorp Vault) so the key never reaches a
disk the operator's database administrators can read, and by auditing every administrative act.
With a key service, an operator of the hosts and the database who does not hold the service's token (or its
AppRole files) opens nothing: a copy of the disks, the backups and the database is ciphertext. The one who
holds the token and the database can unwrap every data key for as long as the token lives, and the one who
runs the service holds the key itself; a token that is short-lived and renewed, a policy that cannot export the
key and a key that is not exportable, which Pandokos requires by default, and the service's own audit log are what narrows that.

## Threats and controls

| ID | Threat | Controls | Code | Proof |
|---|---|---|---|---|
| TM-01 | A caller claims another tenant | The tenant derives only from the credential (TEN-02). Tenant headers and parameters are ignored | `internal/authn/authn.go` | `authn.TestTenantComesFromTheKey`, `isolation.TestCallerCannotNameTheTenant` |
| TM-02 | A bug omits a tenant filter | Forced row-level security under non-owner roles (TEN-04, TEN-05). The isolation suite (TEN-07) | `internal/store/migrations`, `internal/store` | `isolation.TestSchemaLint`, `isolation.TestCrossTenantNegative`, `isolation.TestOwnerIsSubjectToPolicies` |
| TM-03 | A stolen virtual key | Keys are scoped, can expire, carry limits, are revocable within a bound, and have a detectable format so scanners find leaks (IAM-03 to 05) | `internal/authn` | `authn.TestAuthenticate`, `authn.TestRevocationBoundWithoutNotifications`, `authn.TestRevocationByNotification`, `authn.TestParseRejects`, `repo.TestSecretScanning` |
| TM-04 | A stolen bearer token | The token is accepted only if it verifies against the registration's keys with an allowed asymmetric algorithm, its exact issuer and audience, a lifetime of at most `tokens.max_lifetime` (24 h by default) and a mapped subject; `exp` is required and the signature is checked on every request. It can be replayed until `exp`, and one token cannot be revoked: delete the mapping. Mutual TLS (IAM-15, IAM-16) would bind a token to its sender and is not built | `internal/authn/token.go` | `authn.TestTokenClaims`, `authn.TestSignatureOnEveryRequest`, `e2e.TestTokenRevocation` |
| TM-05 | A tenant registers the issuer another tenant uses | The pair (issuer, audience) is unique in the installation; a token selects its registration by that pair, so another tenant's registration with another audience never receives it. The tenant is never chosen among candidates: a token that matches two registrations is refused. The guide requires an audience that names the installation and the tenant whenever an issuer is shared, and to stop workloads before deleting a registration; the residual case is in boundary 6 | `internal/authn/tokens.go` | `authn.TestIssuerSelection`, `store.TestIssuerSchema`, `isolation.TestIssuerTables` |
| TM-06 | A token of another relying party, or a forged algorithm or key (`none`, HMAC keyed with the public key, `jwk`, `jku`, a `kid` of another source) | The audience is mandatory and compared exactly; the algorithm is on an allow-list of ten asymmetric algorithms before any key is looked up, and the library is given exactly that one; keys come only from the registration's source, and a key is used only if its type and size fit the algorithm | `internal/authn/token.go`, `internal/jwks/keys.go` | `authn.TestTokenAlgorithmAttacks`, `jwks.TestKeySelection`, `repo.TestJOSEImports` |
| TM-07 | A tenant administrator makes the gateway fetch an internal address through `issuer` or `jwks_uri` | `https` only through the egress guard; internal addresses unreachable unless the operator lists the host; a fixed method, no redirect, 5 s, 64 KiB and 256 KiB limits, at most one fetch per source per 10 s; nothing of the answer observable except whether tokens verify | `internal/jwks/fetch.go` | `e2e.TestTokenSSRF`, `jwks.TestLimits`, `jwks.TestTransportClass`, `jwks.TestRefreshAndRefetch` |
| TM-08 | Forged tokens flood the log, the audit log or the identity provider | Failures before the registration is known are logged only; audited failures are bounded (one row per registration, reason and subject in ten minutes, 60 a minute per instance); one fetch per source per 10 s | `internal/resolver` | `gateway.TestTokenFailureAudit`, `resolver.TestTokenFailureAuditor`, `jwks.TestRefreshAndRefetch` |
| TM-09 | Password guessing against local accounts | Ten failures lock an account for 15 minutes and fifty lock a source (a delay from the third failure), by digests of what was typed, so an unknown username behaves exactly like a real one; one hash computation always runs and the four refusals (unknown, wrong password, disabled, expired generated password) are one byte-identical `401`; failures and locks are audited, without the typed username (IAM-20). Argon2id with stored parameters, 16-byte salt, 64 MiB, at most four computations at once (IAM-19). No default credentials; a generated password is 120 bits, expires in 72 hours and must be changed (IAM-14). Local accounts can be switched off (IAM-18). Accepted: a known username can be locked out for the lock duration (the host commands and the identity provider are the way round) | `internal/account`, `internal/password` | `account.TestThrottle`, `account.TestSourceThrottle`, `account.TestSignInUniform`, `password.TestConcurrency`, `e2e.TestLockExit`, `e2e.TestNoDefaultCredentials` |
| TM-10 | A stolen or forged session | Random 256-bit tokens `pds_...` stored as SHA-256 only; cookie `__Host-` with `Secure; HttpOnly; SameSite=Strict`, no `Domain`; idle (30 minutes) and absolute (12 hours) lifetimes; at most ten sessions per user; a new token at sign-in and at password change; sign-out, revocation and the end of a session on disabling a user (IAM-21). The session and its user are read at every request, so the bound of IAM-11 is the request in flight | `internal/session/session.go` | `session.TestStoreStoresOnlyTheHash`, `session.TestCookie`, `session.TestMaxPerUser`, `admin.TestSessionLifetimes`, `admin.TestDisableEndsSessions` |
| TM-11 | Cross-site request forgery against a signed-in user | `SameSite=Strict`, plus on every sign-in and every state-changing session request the header `X-Pandokos-Csrf: 1`, an `Origin` equal to the expected origin and `Sec-Fetch-Site: same-origin` when present, before the body is read; the API sends no CORS header (IAM-21). The two OpenID Connect endpoints are navigations, protected by state and a binding cookie | `internal/admin/origin.go`, `internal/admin/admin.go` | `admin.TestCSRF`, `admin.TestExpectedOrigin`, `admin.TestSecurityHeaders`, `browser.TestCrossSite` |
| TM-12 | A sign-in forged with another person's code, or a replayed callback | State, nonce and PKCE verifier derived from a seed that only the starting browser holds (`SameSite=Lax` cookie); a one-use state row valid for ten minutes; `iss`, `aud`, `nonce`, `azp` and signature checked; a fixed redirect destination (IAM-09) | `internal/oidc/flow.go`, `internal/oidc/idtoken.go` | `oidc.TestCallbackState`, `oidc.TestIDToken`, `oidc.TestCompleteQuery`, `e2e.TestOidcExit` |
| TM-13 | Group rules give a role by mistake | Exact match on the group; rules are configuration reviewed by the operator; only two roles can be given; each grant is audited with `source: idp` and removed at the next sign-in that the groups no longer justify; manual rows are never touched | `internal/oidc/groups.go` | `oidc.TestRules`, `oidc.TestGroups`, `admin.TestGroupReconciliation` |
| TM-14 | A tenant administrator takes over a person who is powerful elsewhere | The managing rule: a user that a tenant manages is a member of that tenant only and holds no platform role (two triggers); a platform-managed member can be added but not reset, disabled, unlocked or signed out by a tenant administrator (`409 invalid_state`); federated users are always platform-managed | `internal/store/migrations` | `store.TestManagingRule`, `admin.TestMembers` |
| TM-15 | A retry creates a second resource, or leaks the first one's secret | Idempotency keys on every create: scoped by tenant, principal and key; a request hash with a random salt; the creation and its memory are one atomic fact through the audit helper; the stored response never holds a one-time member ([ADR 0059](../adr/0059-idempotency-keys.md)) | `internal/idem/idem.go` | `idem.TestClaim`, `admin.TestIdempotencyEveryCreate`, `admin.TestIdempotencyStoresNoSecret` |
| TM-16 | A tenant points a webhook at an internal address (SSRF), or uses the test request to probe | Requests leave from the worker only, through the egress guard ([ADR 0038](../adr/0038-egress-guard-upstream-tls-and-proxy.md)): `https`, public addresses checked on the address dialled for every attempt, no redirect followed, one deadline, a response body discarded; only the outcome and the status code come back. A private or `http` destination needs a host the operator lists in `events.webhook.private_hosts`. The test request is recorded by the administration and sent by the worker | `internal/outbox/deliver.go` | `outbox.TestValidateURL`, `outbox.TestDeliver`, `repo.TestOutboxEgress`, `admin.TestEventDestinationOperations` |
| TM-17 | A webhook signing secret leaks | The server generates it (32 random bytes, shown once), a caller cannot choose a weak one, and it is sealed under the tenant's data key with its own associated data (a credential's ciphertext does not open as a secret, nor one destination's as another's); the worker opens it for one round and clears it; it is a `secret.Secret` end to end and the shape `whsec_` is redacted from logs; it appears in no audit row, error, export or response but the two that show it once; rotation keeps the previous one valid for a bounded time (24 h by default, 7 days at most); destroying the tenant's data key destroys it. A secret leak is a forged event, never a read of the tenant's data | `internal/vault/named.go`, `internal/outbox/sign.go`, `internal/telemetry/redact.go` | `vault.TestSealNamed`, `admin.TestEventSecretCanary`, `outbox.TestRelaySecretRotation`, `telemetry.TestSigningSecretShape`, `repo.TestSecretShapesAreScanned` |
| TM-18 | A tenant's events are redirected, read or forged by a role | The worker holds no `UPDATE` on `url`, `event_types`, `enabled` or a secret; the admin role cannot read a sealed secret and cannot update delivery history; no runtime role writes an event (the database does), and the gateway role cannot read one. A receiver verifies the signature and the five-minute timestamp, and deduplicates by event ID ([events guide](../operations/events.md#5-the-request-and-how-to-verify-it)) | `internal/store/migrations` | `isolation.TestEventDestinationGuards`, `isolation.TestOutboxTables`, `outbox.TestSign` |
| TM-19 | A URL with a token in its path is read by other administrators | Documented: the URL is configuration, readable by every holder of `event_destination:read`, and the signature, not the URL, authenticates a delivery; the audit log keeps the host only | `internal/admin` | `admin.TestEventDestinationAPI` |
| TM-20 | Script injection in a browser client | The session cookie cannot be read by scripts, and the session token is in no response body and in no place a script reads (IAM-21). A secret that an operation shows once (a key, a password, a signing secret) is in that response and is the client's to drop. The rest is the client's own duty: Pandokos serves no pages | `internal/session/session.go` | `session.TestCookie`, `admin.TestSignInEndpoint` |
| TM-21 | A signed-in user reaches functions beyond their role | The API authorizes every call. What `/v1/me` reports comes from the same permissions and is tested against them (AZ-11) | `internal/authz`, `internal/admin` | `admin.TestRoleMatrix`, `admin.TestMeMatrix`, `isolation.TestRoutesArePublicOrPermitted` |
| TM-22 | A session is used to call models | Sessions are accepted only on the administration listener; no code of the gateway reads a cookie and its database role has no privilege on any table of people or sessions, in any session (IAM-22) | `internal/gateway`, `internal/store/migrations` | `e2e.TestSessionNotOnInference`, `repo.TestGatewayReadsNoCookie`, `isolation.TestPeopleTablesAreAdminOnly`, `server.TestListenersAreSeparate` |
| TM-23 | The database or a backup is copied | Credentials are ciphertext under per-tenant keys wrapped by a key that is not in the database (VLT-09, VLT-13). Virtual keys are stored as hashes | `internal/vault/envelope.go` | `vault.TestSealOpen`, `e2e.TestVault`, `isolation.TestVaultColumnPrivileges`, `isolation.TestKeyTables` |
| TM-24 | The token of the key service leaks | With the database it unwraps every data key while it lives, and by itself it opens nothing. The policy grants encrypt, decrypt, the HMAC for identifiers, a description of the key and the token's own lookup and renewal, and nothing that rotates, exports, trims or deletes; an AppRole gives short tokens; secrets are files and are never logged or put in an error (canary tests search every output and the requests the service received). The response is to revoke it, give new credentials, rotate the key, let the worker re-wrap and trim the old versions ([external key guide](../operations/external-key.md#11-loss-and-compromise)) | `deploy/transit/pandokos-policy.hcl`, `internal/vault/transit_auth.go` | `transit.TestConformance`, `vault.TestTransitAppRoleAuth`, `e2e.TestTransitTokenLifetime`, `vault.TestTransitCanary`, `e2e.TestTransitCanary`, `repo.TestTransitDocuments` |
| TM-25 | A key version is destroyed too early | Raising `min_decryption_version` or trimming a version the database still needs: `serve` refuses to start and names the version, and while running the tenants under it get `503`; raising is reversible until a trim. Pandokos never does either; the guide's order is rotate, restart, wait for `key rotation complete` and no pending erasure, back up, then raise and trim ([external key guide](../operations/external-key.md#8-erasure-in-backups)). A version trimmed too early is the lost-key case | `internal/vault/check.go` | `e2e.TestTransitStartRefusals`, `e2e.TestTransitRotation`, `vault.TestCheckSeveralProviders` |
| TM-26 | The key-encryption key leaks | Rotation by re-wrapping (VLT-10): a new key file becomes the active version at start, the worker re-wraps every data key and retires the old version, and the old file is destroyed ([vault guide](../operations/vault.md#6a-rotating-the-key)). The leak alone exposes nothing without the database; a backup taken before the rotation stays readable with the leaked key until the backups expire or are destroyed | `internal/vault/rewrap.go`, `internal/vault/activate.go` | `e2e.TestKEKRotationEndToEnd`, `vault.TestRewrap`, `vault.TestRewrapAcrossProviders` |
| TM-27 | A tenant leaves | A staged deletion run by the worker: the tenant is suspended during a grace period, then closed to new rows, its access removed, its data key and credentials destroyed, its configuration removed, its ledger and audit log kept with every identifier of a person or workload replaced by a pseudonym, and a completion record written in the platform's log that `pandokos tenant deletion verify` checks. Nothing global and nothing of another tenant is touched, and the platform reads no usage ([deletion guide](../operations/tenant-deletion.md), [ADR 0067](../adr/0067-tenant-deletion-stages-closed-tenants-and-kept-records.md)) | `internal/lifecycle` | `e2e.TestDeletionEndToEnd`, `isolation.TestTenantErasureSteps`, `isolation.TestErasurePseudonymizesLedger` |
| TM-28 | A ciphertext is copied onto another tenant's row | The tenant and credential IDs are bound into the ciphertext as associated data, so decryption fails (VLT-07) | `internal/vault/envelope.go` | `vault.TestSealOpen`, `vault.TestSealNamed` |
| TM-29 | An attacker with the database guesses a credential from its fingerprint or hint | The fingerprint is a keyed hash under a key derived from the tenant's data key, which needs the key-encryption key; fingerprints differ between tenants. The hint is four characters and absent for short secrets | `internal/vault/envelope.go` | `vault.TestFingerprintAndHint` |
| TM-30 | A platform administrator abuses the role | No read path for secrets, usage, limit events or prices in API or database grants (AZ-08, ADR 0054). The platform holds no tenant permission. It acts inside a tenant only under a **support action**: a reason, a closed set of eleven permissions that read no usage, price or secret, at most 24 h (1 h by default), one transaction for the row and both audit events, visible to the tenant and ended by its owner, and switched by `support.enabled` (AZ-09, [ADR 0061](../adr/0061-support-actions-and-reasons-how-the-platform-acts-inside-a-tenant.md)). **The residual path:** under an action the platform grants with the tenant's authority, so an administrator can make an identity in the tenant (a key, a member) and act as it; there is no other path (`resetUserPassword` refuses a platform user that holds permissions in a tenant, whose recovery is the host command `pandokos user reset-password`) but the known gap below, a membership or role gained while a platform-issued password is pending, each step names the action in the tenant's log, and `support.enabled: false` removes the path together with the recovery | `internal/authz`, `internal/admin` | `isolation.TestPlatformSeesNoTenantUsage`, `isolation.TestPlatformKeyHasNoTenantPermission`, `admin.TestSupportNeverReadsUsage`, `admin.TestSupportAudit`, `repo.TestPlatformReadsNoTenantUsage` |
| TM-31 | A principal hands out more than it holds | One function, `CanGrant`, over a closed table of operations that confer power: defining roles, giving and taking roles, issuing keys, mapping subjects, resetting passwords; removal is covered, containment is free; `escalation_refused` ([ADR 0060](../adr/0060-built-in-roles-custom-roles-and-the-rule-against-escalation.md)) | `internal/authz/grant.go` | `authz.TestCanGrant`, `admin.TestGrantSurfaceIsClosed`, `admin.TestNoEscalationRoles`, `admin.TestNoEscalationAssignments` |
| TM-32 | A custom role smuggles a platform permission | Refused at write; dropped when the permissions of a principal are computed | `internal/authz` | `authz.TestEffectiveGranted`, `authz.FuzzEffective`, `admin.TestRoles` |
| TM-33 | A deactivated or deleted role keeps working on the inference listener | `tenant_role` notifies the gateways' cache on every change; the cache's own bound (ADR 0030) | `internal/authn` | `admin.TestRoleChangeBound`, `authn.TestCustomRolePrincipal` |
| TM-34 | An administrator removes the owners and takes their place | Removal needs the role's permissions, and so do password resets and key issuing for an owner's account | `internal/admin` | `admin.TestSeparationOfDuties`, `admin.TestNoEscalationAssignments` |
| TM-35 | A support action outlives its need | Expiry by constraint; closed by the platform; ended by the tenant; honoured only while the actor still holds the permission and the switch is on | `internal/store/migrations`, `internal/admin` | `admin.TestSupportPrincipal`, `admin.TestTenantSeesAndEndsSupport`, `isolation.TestSupportActionVisibility` |
| TM-36 | The answer for "no membership" reveals which tenants exist | Without an open action the answers are those of sprint 4.2, byte for byte | `internal/admin` | `admin.TestSupportPrincipal`, `isolation.TestCrossTenantIndistinguishable` |
| TM-37 | What `GET /v1/me` says differs from what is enforced | One function builds the principal for both; hiding a control in a client is never the control | `internal/admin` | `admin.TestMeMatchesTheHook` |
| TM-38 | A future built-in role takes the name of a tenant's custom role | Built-in names and the prefixes `platform-` and `pandokos-` are refused for custom roles | `internal/store/migrations` | `repo.TestBuiltinNamesInMigration`, `admin.TestRoles` |
| TM-39 | The proxy is used to reach internal services (SSRF) | Destinations come from the catalogue only: scheme, host, port and path of the outbound request are catalogue values. Redirects are not followed. The address actually dialled is classified in the dialer's control hook, after name resolution, for every connection attempt; forbidden addresses (unspecified, multicast, reserved, and the instance and pod credential services: the shared link-local metadata address of the large clouds, the ECS task endpoint, the EKS Pod Identity agent and the IPv6 metadata address of Amazon Web Services, the IPv6 metadata server of Google Compute Engine, the metadata address of Alibaba Cloud and the IPv6 metadata service of OpenStack) are refused for every endpoint. Behind `upstream.proxy.url` the connection checked is the one to the proxy and target names are resolved by the proxy, which is then the control point (GW-05, GW-06, [ADR 0038](../adr/0038-egress-guard-upstream-tls-and-proxy.md)) | `internal/egress/guard.go`, `internal/egress/classify.go` | `egress.TestDialControl`, `egress.TestDialledAddressIsChecked`, `egress.TestNoRedirectNoEnvironmentProxy`, `egress.TestClassifyMetadata`, `e2e.TestEgressAttacks`, `repo.TestOutboundHTTPInOnePlace` |
| TM-40 | A private endpoint is used to reach internal services | An endpoint may use `http` or a private address only when an administrator declared it `private`; a public endpoint is refused any private address when it is dialled, whatever its name resolved to when it was written. Private endpoints never use the proxy and can never be on a metadata address, the services named in the previous row included; a private webhook host or private token source cannot be on one either | `internal/egress/classify.go`, `internal/gateway` | `gateway.TestEgressClassFollowsEndpoint`, `egress.TestClassify`, `egress.TestClassifyMetadata`, `outbox.TestValidateURL` |
| TM-41 | A body with two `model` members is used to pass the model check | Refused with `invalid_request`: exactly one top-level string member `model` is accepted, so the model that is authorized is the one the provider sees. A member whose name differs only in case from a member Pandokos reads (for example `Model`, or `Stream` beside `stream`) is refused too, with the reason `member_case` in the operator's log, because a provider that matches names without regard to case would read the other one; the body is never re-written ([ADR 0037](../adr/0037-request-pipeline-and-resolution.md), [ADR 0074](../adr/0074-threat-model-review-and-continuous-fuzzing.md)) | `internal/family/jsonbody.go` | `family.TestParseModel`, `family.TestCaseConflict`, `openai.TestParseMemberCase`, `anthropic.TestParseMemberCase`, `gateway.TestMemberCaseRefused`, `openai.FuzzParseRequest` |
| TM-42 | A poisoned or hijacked price dataset (R-3) | The dataset is data, never code: it is parsed with a size limit, its regular expressions run in RE2, and it can only price models the catalogue already holds, never create one or name a destination. The embedded snapshot is pinned to a commit with its checksum. The download is off by default, goes only to an operator-configured `https` URL (no proxy, no redirects, bounded in size and time) and no request can set that URL. A changed price becomes a new immutable version with an audit record, and an operator override is never touched by an import. The worst a bad dataset does is wrong cost estimates, which the operator can see in the import report and correct with an override | `internal/pricing/genaiprices/parse.go`, `internal/catalog/fetch.go` | `genaiprices.TestParse`, `genaiprices.FuzzParse`, `catalog.TestFetch`, `catalog.TestImportDB`, `repo.TestPriceSnapshot` |
| TM-43 | The provider credential is sent elsewhere | It is attached only to requests for the registered host (GW-05) | `internal/gateway/proxy.go` | `gateway.TestCredentialSwap`, `gateway.TestCredentialNeverReachesCaller`, `openai.TestAttachCredential` |
| TM-44 | Request smuggling, header injection | Strict parsing by the Go HTTP stack, a header allow-list, removal of hop-by-hop headers (GW-07) | `internal/gateway/proxy.go`, `internal/server/server.go` | `gateway.TestRequestHeaders`, `gateway.TestResponseHeaderAllowList`, `gateway.TestInformationalResponsesAreDropped`, `gateway.TestProviderTrailersAreDropped`, `server.TestStrictParsing` |
| TM-45 | Secrets or content end up in logs or traces | No code path logs bodies or credential headers. Canary tests search every output (VLT-14, OBS-04) | `internal/gateway`, `internal/telemetry` | `e2e.TestCanaries`, `e2e.TestGatewayCanaries`, `e2e.TestNoContentAnywhere`, `gateway.TestTelemetryCanaries` |
| TM-46 | A tenant overspends through concurrency | Reservation before forwarding, settlement after (LIM-07). Enforced now, **per gateway instance**: requests per minute and requests in flight are strict, tokens per minute are strict on what is reserved and bounded on what is used; each instance counts alone until the shared store of epic 8, so *N* instances admit up to *N* times a limit, and there is no limit until a rule exists ([limits guide](../operations/limits.md), [ADR 0048](../adr/0048-reserve-and-settle.md)) | `internal/limits` | `limits.TestReserveAtomic`, `limits.TestPropertyConservation`, `gateway.TestParallelLoad` |
| TM-47 | A tenant spends more than its budget (LIM-03, LIM-12) | A budget is an amount in one currency per day or month, reserved before forwarding and settled after. **What it guarantees:** it is strict on what was reserved and bounded on what was spent (the excess of one request is at most the cost of a full context window and output at the highest rates of the price in force, times the requests in flight), and once passed nothing is admitted until the period ends; a request a budget cannot price is refused (409 `no_price`) unless the operator allowed it. **What it does not:** it counts known costs in its own currency only and never converts; it is per gateway instance, so *N* instances admit up to *N* times a budget; after a restart or while the ledger cannot be read it counts what the ledger holds, and with `limits.on_store_failure` `allow` it can be exceeded while counters are unavailable; records that never reach the ledger are not counted; the figures are estimates, not invoices (BYOK: the provider's invoice is the authority) ([limits guide](../operations/limits.md), [ADR 0050](../adr/0050-quotas-and-budgets-periods-attribution-currencies.md), [ADR 0051](../adr/0051-period-counters-and-the-ledger.md)) | `internal/limits`, `internal/gateway/limits.go` | `gateway.TestParallelBudget`, `limits.TestPropertyPeriods`, `gateway.TestNoPrice`, `e2e.TestMonthlyBudget` |
| TM-48 | Usage is evaded through unmetered endpoints | Only endpoints Pandokos can meter are exposed (GW-09) | `internal/gateway`, `internal/family` | `gateway.TestEndpointNotSupported`, `e2e.TestUnsupportedAnthropicPaths`, `gateway.TestRouteDeclaresAPermission`, `gateway.TestMemberCaseRefused` |
| TM-49 | Resource exhaustion | Limits on body size, duration and concurrency (GW-16). Bounded buffers everywhere. Rate, token, cost and concurrency limits and quotas per tenant, service account and key are enforced in each instance from sprint 5.1, when a rule exists; a request whose limits cannot be verified is refused with 503, not forwarded | `internal/gateway/handler.go`, `internal/server/server.go` | `gateway.TestRequestTooLarge`, `gateway.TestMaxRequestDuration`, `gateway.TestConcurrencyLimit`, `gateway.TestStoreFailure`, `openai.TestStreamReaderBounded`, `server.TestHeaderLimits` |
| TM-50 | The audit log is altered | No update or delete grants to any runtime role. Every row is sealed in a hash chain per tenant and for the platform (AUD-04, [ADR 0065](../adr/0065-the-audit-hash-chain-reading-and-retention.md)): a row changed, removed, inserted or moved is detected by `pandokos audit verify`, which runs on the client over an export and checks the head and an expected head kept elsewhere ([audit guide](../operations/audit.md)). The residual: whoever can write the tables (the owner role, a superuser) can rewrite a whole chain and its head, which only a head anchored off the installation detects. Retention (`retention.audit_months`, off by default) deletes rows only through two audited functions under a switch (`pandokos.retention`) that no runtime role can set, and moves the chain's anchor, recording the removal in the chain | `internal/auditchain/auditchain.go` | `isolation.TestAuditIsAppendOnly`, `isolation.TestAuditChainSeal`, `e2e.TestAuditVerifyDetectsTampering`, `isolation.TestAuditRetention`, `auditchain.FuzzCanonical` |
| TM-51 | A dependency or the build is compromised | A small dependency set, pinned with checksums, updated by reviewed pull requests and scanned on every change and every night. Every action, image, tool and module directory is pinned and under automated update. The image published by the release workflow is signed by digest, carries provenance and an attested SBOM, and is scanned and verified by digest before it is published; the nightly checks also read the repository's rulesets. No binary is released: the artefacts are the image, its SBOM and the API description (SEC-06, [ADR 0018](../adr/0018-supply-chain.md), [ADR 0074](../adr/0074-threat-model-review-and-continuous-fuzzing.md)) | `go.mod`, `Dockerfile`, `.github/workflows` | `repo.TestDependencies`, `repo.TestActionsPinned`, `repo.TestEverythingPinned`, `repo.TestDependabotCoverage`, `repo.TestScheduledChecks`, `repo.TestDockerfile` |
| TM-52 | A caller forges trace or metadata to mislead | Metadata headers and trace context are read only after authentication, so an anonymous caller causes no span and no stored value. Both are recorded as declared by the caller, within bounds (eight short ASCII entries; a sampled flag only makes the caller's own requests sampled), and never used in a decision or forwarded (GW-13, OBS-02, [ADR 0039](../adr/0039-usage-record-admission-and-ledger-writer.md)) | `internal/usage/metadata.go` | `gateway.TestNothingBeforeAuthentication`, `usage.TestParseMetadata`, `gateway.TestPropagation`, `usage.FuzzParseMetadata` |
| TM-53 | The gateway is used to rewrite or read the usage ledger | The gateway role holds `INSERT` on the listed columns of `usage_record` and nothing else: it cannot read, change or delete a row, even one it wrote; triggers stop the owner too. Rows of every tenant are written in that tenant's session under forced row-level security | `internal/store/migrations` | `isolation.TestUsageAppendOnly`, `isolation.TestUsageRecord` |
| TM-54 | A provider smuggles text into the ledger through its usage object | Only non-negative integer members, at most 32, are kept, under keys of a fixed alphabet; every text column of a record is format-checked by the database | `internal/family/family.go` | `family.TestFlattenUsage`, `family.FuzzFlattenUsage`, `isolation.TestUsageRecord` |
| TM-55 | Someone reads the metrics without a right to | `GET /metrics` is off by default and, when on, needs a platform credential holding `metrics:read`; a tenant principal, a session and an anonymous request are refused, and the authentication failures are counted and logged | `internal/authz`, `internal/admin` | `e2e.TestScrapeEndpoint`, `authz.TestMetricsPermission` |
| TM-56 | A secret reaches the log inside an error text | One handler builds every logger and redacts values of sensitive keys and anything with the shape of a Pandokos secret; each redaction is counted and a test keeps the log reference and the shapes current ([ADR 0063](../adr/0063-the-log-redaction-and-the-log-reference.md)) | `internal/telemetry/redact.go` | `telemetry.TestRedactingHandler`, `telemetry.TestRedaction`, `repo.TestLogReference` |
| TM-57 | The trace or metrics collector is used as a way out | The exporter uses the egress guard: one operator-configured URL, certificates verified, no proxy, no redirect, forbidden and metadata addresses refused | `internal/telemetry/traces.go`, `internal/egress/guard.go` | `telemetry.TestExport`, `telemetry.TestCollectorNeverBlocks` |
| TM-58 | A provider, or whoever answers in its place, sends an answer made to crash or exhaust the gateway | Readers are bounded and fail soft; usage is read from a copy and never decides the answer | `internal/family/openai/stream.go`, `internal/family/anthropic/stream.go`, `internal/family/sse` | `openai.TestStreamReaderBounded`, `anthropic.TestStreamReaderBounds`, `gateway.TestReaderFailsSoft`, `openai.FuzzStreamReader`, `anthropic.FuzzAnthropicStreamReader`, `sse.FuzzFraming` |
| TM-59 | The key service, an identity provider or the price source answers with crafted data | Answers are bounded in size, error bodies are never parsed, error texts are fixed, and keys that do not fit are skipped; each parser has a fuzz target (see Parsers that read from the network) | `internal/vault/transit_client.go`, `internal/jwks/keys.go`, `internal/oidc/flow.go` | `vault.TestTransitErrorClasses`, `jwks.TestLimits`, `oidc.TestCodeExchange`, `vault.FuzzTransitAnswer`, `jwks.FuzzParseKeySet`, `oidc.FuzzProviderAnswers` |
| TM-60 | A published image or its tag is replaced after the release, or the pipeline publishes something other than what it checked | The digest is what is signed, attested, scanned and verified, and verification is by digest, by the pipeline before it publishes and by the operator in the guide | `.github/workflows/release.yml`, `docs/operations/container.md` | `repo.TestReleaseWorkflowShape`, `repo.TestReleaseVerifiesWhatItPublishes` |

## Parsers that read from the network

"From the network" means bytes whose author is not the operator: a caller, an administrator's client, a provider, an identity provider, the key service, the price source, a webhook receiver. Each parser has a fuzz target that `make fuzz` runs on every pull request (10 seconds each) and every night (3 minutes each); `TestNetworkParsersAreFuzzed` holds this table against the code and the Makefile, so a parser without a target or a target missing here fails the build. The seeds of a target run as ordinary tests, and an input that once made a target fail is committed under `testdata/fuzz/` of its package.

| Input | Parser | Fuzz target |
|---|---|---|
| Inference credential header | `openai.InboundCredential`, `anthropic.InboundCredential` | `openai.FuzzInboundCredential`, `anthropic.FuzzInboundCredential` |
| Virtual key | `authn.Parse` | `authn.FuzzParse` |
| Bearer token (JWS and claims) | `authn` (`peek`, `verifyJWT`) | `authn.FuzzParseToken` |
| Request body, `openai` family | `openai` (parse, prepare, output bound), `family.ScanObject` | `openai.FuzzParseRequest`, `openai.FuzzPrepareRequest`, `openai.FuzzOutputBound` |
| Request body, `anthropic` family | `anthropic` (parse, output bound) | `anthropic.FuzzAnthropicParseRequest`, `anthropic.FuzzOutputBound` |
| Caller metadata headers | `usage.ParseMetadata` | `usage.FuzzParseMetadata` |
| Provider answer, usage object | `openai.ReadUsage`, `anthropic` (usage reader), `family.FlattenUsage` | `openai.FuzzReadUsage`, `anthropic.FuzzAnthropicReadUsage`, `family.FuzzFlattenUsage` |
| Provider stream | `openai` and `anthropic` stream readers, `sse` framing | `openai.FuzzStreamReader`, `openai.FuzzResponsesStreamReader`, `anthropic.FuzzAnthropicStreamReader`, `sse.FuzzFraming` |
| Key service answers (encrypt, decrypt, describe, identify, lookup, renew, login) | `vault` (`decodeEnvelope`, `transitCiphertext`, `transitUnquote`, `transitDecode` and the grant and description types) | `vault.FuzzTransitAnswer` |
| Key set of an issuer | `jwks.parseKeySet` | `jwks.FuzzParseKeySet` |
| Discovery document of an issuer | `jwks` (the decoding of `discover` and `fetchMetadata`) | `jwks.FuzzMetadata` |
| Token endpoint answer and ID token claims of the people's provider | `oidc` (the decoding of `redeem`; `stringClaim`, `audienceCount`, `readGroups`) | `oidc.FuzzProviderAnswers`, `oidc.FuzzGroups` |
| Price dataset (embedded or downloaded) | `genaiprices.Parse`, `genaiprices.Extract` | `genaiprices.FuzzParse` |
| Decimal amounts of the admin API | `pricing.ParseDecimal` | `pricing.FuzzParseDecimal` |
| Cursor of a list request | `admin.decodeFullCursor` | `admin.FuzzDecodeCursor` |
| Request body of the admin API | `adminapi.decodeBody` | `adminapi.FuzzDecodeBody` |
| Path of the admin API | `adminapi.Match` | `adminapi.FuzzMatch` |
| `Host`, `Origin` and forwarding headers of the admin API | `admin` (`splitHost`, `expectedOrigin`, `sourceAddr`) | `admin.FuzzOrigin` |
| Session cookie | `session.ParseToken` | `session.FuzzParseToken` |
| Webhook URL given by a tenant | `outbox.ValidateURL` | `outbox.FuzzValidateURL` |
| Address a name resolved to | `egress.Classify` | `egress.FuzzClassify` |
| Audit export read by `pandokos audit verify` | `auditchain.Canonical` | `auditchain.FuzzCanonical` |
| Roles and grants (data of the database, reached by the API) | `authz` (`Effective`) | `authz.FuzzEffective` |
| Stored password hash (not network input; kept) | `password.parse` (reached through the hasher's verification) | `password.FuzzParsePHC` |

Not fuzzed, with the reason: a provider's error body and a webhook receiver's answer (never parsed: passed through or discarded); the answer of a credential validation (the status only); the trace context (parsed by the OpenTelemetry propagator after authentication and never used in a decision); the declarative file and the configuration (a local file of the operator, bounded by `declare.checkNode`); multipart bodies (no route accepts one).

## Replay exposure

A virtual key is a bearer credential (SEC-02): whoever holds it can use it. It is protected in transit
by TLS on the listener or in front of it. A captured key can be replayed until it expires or is
revoked, within the bound of the principal cache ([ADR 0030](../adr/0030-keys-format-storage-lookup.md)).
There is no nonce and no request signature. Scopes, the tenant's model list and limits bound what a
replay can do. Sender-constrained mechanisms, which would make a captured key useless to anyone else,
are IAM-15 and IAM-16 and are not built yet.

A token of a trusted issuer is a bearer credential too, valid until its `exp`. TLS on the listener, the short lifetimes the identity provider issues, and `tokens.max_lifetime` (24 hours by default, any longer token is refused) bound the exposure; the signature is checked on every request, so there is no cache of tokens to outlive one. A single token cannot be revoked; deleting its subject mapping or disabling its registration stops the subject within `cache.principal_ttl`. A refusal before the signature check is slightly faster than one after it; this reveals at most that an issuer and audience pair is registered, which a tenant administrator already learns from a conflict.

A session is a bearer credential as well: whoever holds the cookie value can use it until it ends. It is
not readable by scripts, is sent over TLS only (the `__Host-` prefix and `Secure`; HSTS is the duty of a
proxy that ends TLS) and never cross-site, and it ends within the idle and absolute lifetimes, by sign-out,
by a password change or reset, by the disabling of the user and by revocation, all effective at the next
request because nothing is cached. There is no binding of a session to an address or a device. What a
group gave through an identity provider lasts until the user's next sign-in.

**Residual exposures of people.** Local accounts have one factor and no multi-factor authentication
(ADR 0022): production use should prefer an identity provider or a private administration network. A
session is a bearer credential until it ends. An operator of the database reads user records (display
names, e-mail addresses, usernames, hashes). A throttle row is a digest of a typed username and lives for
at most the window plus the lock. New passwords are not checked against a list of breached passwords (the
one unmet row of the ASVS 4.0.3 chapters V2 and V3 table of the sprint design, which a reviewer reads
against the code). Concurrent attempts that passed the throttle together are all verified. What a group
gave lasts until the next sign-in. A membership or role gained while a platform-issued password
(`resetUserPassword` or `createUser`) is still pending, until it is changed or expires, is not checked
against the caller that obtained the password: a known gap, to be closed in sprint 10.2, for example by
refusing or invalidating a pending platform-issued password when a membership or role is added. The
maintainer accepted it until then on 2026-11-23.

## Key hierarchy

```mermaid
flowchart TB
    kek[Key-encryption key<br/>outside the database<br/>file or transit key service] --> dek1[Tenant A data key<br/>wrapped, in the database]
    kek --> dek2[Tenant B data key<br/>wrapped, in the database]
    dek1 --> c1[Credential ciphertexts of A]
    dek2 --> c2[Credential ciphertexts of B]
```

- The data key is a 32-byte root and is never used directly: HKDF-SHA-256 derives from it an
  encryption key (AES-256-GCM) and a separate fingerprint key. A credential is sealed with a random
  nonce and with associated data that name the tenant, the credential and the data key version.
- A credential shows a fingerprint (the first 16 bytes of an HMAC under the fingerprint key) and a
  hint (the last four characters, withheld for secrets shorter than 16 characters). The fingerprint
  is keyed per tenant: it identifies a secret within a tenant, and the fingerprints of one secret
  in two tenants cannot be compared. Formats and numbers: [ADR 0033](../adr/0033-vault-formats-and-key-identifiers.md).
- One data key per tenant limits the effect of a leaked data key to one tenant and makes
  cryptographic erasure a single deletion.
- Rotating the key-encryption key re-wraps the data keys; credentials are not touched. Versions have a state (`active`, `retiring`, `retired`), exactly one is active, and the database refuses a data key under any other.
- Losing the key-encryption key makes every tenant credential unrecoverable. Tenants must then
  re-enter them. This is stated in the operations guide and is the reason the key has its own
  backup procedure.
- Backups and erasure, stated exactly ([ADR 0068](../adr/0068-key-encryption-key-versions-rotation-and-erasure-in-backups.md)).
  When a tenant is deleted, its wrapped data keys and sealed secrets are gone from the database at
  once; the gateways forget the data key within `cache.data_key_ttl` plus `cache.credential_ttl`. A
  **backup taken before** still holds them: whoever has that backup and a key file of the version
  that wrapped the data key can open them, and deleting the tenant does not change that. Only two
  things do: the backup expires, or the key version is retired and every copy of its file destroyed.
  The deletion records the versions concerned; `getTenantDeletion` shows `backup_erasure` as `pending`
  until they are retired, `listKEKVersions` shows how many completed deletions wait for each, and the
  gauge `pandokos.vault.erasures.pending` counts them. The product cannot see whether the file was
  destroyed: that is the operator's act. The rule is to rotate the key within the time erasure is
  promised in, wait for `key rotation complete`, take a new backup and destroy the old file; the price
  is that backups older than the rotation open no credential afterwards. A restored backup also brings
  deleted tenants back and takes the platform's log with it: the backup guide says to keep the list of
  completed deletions outside the database. The [vault guide](../operations/vault.md) and the
  [backup guide](../operations/backup-and-restore.md) state the procedure.

## Credentials issued by Pandokos

A virtual key looks like `pdk_<key id>_<secret><checksum>`. The key ID is public and is how the
key is found and named in logs. The secret has 256 bits of entropy, which is why a plain SHA-256
hash is sufficient storage: there is nothing to brute-force. The checksum lets scanners and the
gateway reject malformed strings without a lookup. The layout is 81 characters: `pdk_`, a key ID of
17 characters (kind letter `t` or `p`, 16 characters), `_`, a 52-character secret and a 7-character
CRC-32 checksum, in lower-case base32; the pattern is in [keys](../operations/keys.md). Only the
SHA-256 of the secret is stored, and only a key ID can be looked up without a tenant: the lookup
function returns the tenant of a key ID and nothing else.

A local account's password is chosen by a person and is therefore guessable: it is stored with
Argon2id, a deliberately slow, memory-hard hash, with a per-account salt. This is the opposite
trade-off from virtual keys, and the reason the two are never verified by the same code path.

## Reviews

- [Review record, 2026-11](threat-model-review-2026-11.md): sprint 12.2, against the commit `96cb793c0359b339c4c98785c29a0776e2dada0f`. Identifiers, code and proof for every row, 21 findings with their decisions, the residual exposures with their dates, the supply chain as checked and what was not reviewed.

A review is a new file `threat-model-review-YYYY-MM.md` in this directory, written at every release (a short one when nothing changed) and before 1.0 by someone who did not write the code (SEC-05, SEC-09). The status line above links the newest one.
