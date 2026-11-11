# Pandokos — Product Requirements

- Version: 0.2 (2026-11-11)
- Status: baseline for design and planning
- Supersedes: the preliminary statement of work (SOW 0.1). Section 9 lists what changed and why.

This document says **what Pandokos must guarantee**. How it does so is described in the
[architecture](../architecture/overview.md) and justified in the [decision records](../adr/).
When the work is planned, each sprint names the requirement IDs it delivers
([planning](../../planning/README.md)).

## 1. Product in one paragraph

Pandokos is a self-hostable, multi-tenant LLM proxy. It sits between calling applications and
model providers and is the single place where callers are authenticated, the tenant of a request
is established, the provider credential is chosen and injected, limits are enforced, and usage is
measured. Tenants either bring their own provider keys (BYOK) or use keys paid for by the platform
operator. Success is not measured by the number of providers or features, but by whether the
product is **a secure, verifiable, multi-tenant boundary between callers and LLM infrastructure**.

## 2. Principles

These are constraints on every requirement and every design decision.

1. **Multi-tenant by default.** Isolation is a property of the product, not an option. A
   single-tenant installation is the same product with one tenant.
2. **Defence in depth.** Authentication, application authorization, database row-level security
   and secret encryption each hold on their own.
3. **The tenant is never taken on trust.** It derives from a verified credential or a server-side
   association, never from a header or parameter the caller controls.
4. **Not an identity provider.** Pandokos federates with external IdPs. Its own credentials are
   virtual keys for technical identities and minimal local accounts for signing in to its console.
   It never authenticates people on behalf of other systems.
5. **Secrets are write-only.** A provider credential can be written, replaced and revoked, never
   read back.
6. **Least privilege**, including for the platform administrator and for the product's own
   processes and database roles.
7. **Auditable.** Sensitive operations and model calls leave verifiable evidence.
8. **No content.** Prompts and completions are not stored, logged or exported. There is no
   switch that turns this on.
9. **Control plane and data plane are separate** concerns, with separate network exposure.
10. **Operable anywhere.** A single container with PostgreSQL is a complete installation.
    Kubernetes is supported, never required.
11. **Every call is measured and priced**, whoever pays the provider.
12. **Transparent proxy.** Provider APIs are forwarded as they are. Pandokos does not translate
    between providers, retry, fall back or balance load on the caller's behalf.

### Non-goals

Pandokos is not, and must not grow into: an identity provider; an agent, workflow or conversation
framework; a prompt manager or prompt store; a semantic cache; a content moderation or guardrail
engine; a router that picks models, falls back or load-balances; a translator between provider
APIs; a billing, invoicing or payment system; a model host; a general-purpose API gateway.

## 3. Glossary and actors

| Term | Meaning |
|---|---|
| Platform | One installation of Pandokos, run by an operator |
| Tenant | A customer organization, logically isolated from the others |
| Platform admin | Privileged identity of the platform; belongs to no tenant |
| User | A person, authenticated by an external IdP or by a local account, with roles in one or more tenants or in the platform |
| Local account | A username and password kept by Pandokos for one user, valid only for the console |
| Console | The web application for administering the platform and the tenants |
| Session | What a person obtains by signing in to the console. It is accepted only by the administration API |
| Service account | A non-human identity owned by one tenant |
| Virtual key | A credential issued by Pandokos to a service account. It is not a provider key |
| Trusted issuer | An external token issuer (IdP, workload identity) whose signed tokens Pandokos accepts |
| Provider | An external supplier of model APIs |
| API family | A wire protocol shared by one or more providers (for example the OpenAI API) |
| Provider credential | The secret Pandokos uses to authenticate to a provider |
| BYOK | The tenant supplies the provider credential and pays the provider directly |
| Platform-funded | The operator supplies the provider credential and may charge the tenant |
| Control plane | Administration and configuration |
| Data plane | Receiving, authorizing, forwarding and measuring inference requests |
| Vault | The function that stores and uses encrypted secrets |
| Cell | A group of data-plane instances with their database. Pool cells are shared; a silo cell serves one tenant |

Actors: platform admin, platform auditor, platform billing operator; tenant owner, tenant admin,
credential manager, usage viewer, auditor, developer; service accounts; and the external systems:
provider, identity provider, billing system, telemetry collector.

## 4. Releases

Requirements are assigned to the release that first delivers them.

| Release | Theme | What a user can do at the end |
|---|---|---|
| **R1 — v0.1** | Core BYOK | Run one container with PostgreSQL, create tenants and virtual keys, store BYOK credentials, call OpenAI and Anthropic through stock SDKs with streaming, and read a usage record with cost for every call |
| **R2 — v0.2** | Governance | Sign in to the web console with a local account or an external IdP, administer the platform and the tenants from it, manage roles, enforce rate limits and budgets, read reports, delete a tenant cleanly |
| **R3 — v0.3** | Platform-funded and billing | Offer operator-paid models with budgets and markup, and deliver billable events to an external system |
| **R4 — v0.4** | Scale-out | Run several replicas, deploy on Kubernetes, dedicate a cell to a tenant, add Gemini and Azure OpenAI |
| **R5 — v1.0** | Hardening | Rely on reviewed security, measured performance, tested recovery and a stable upgrade policy |

Priority: **M** must, **S** should, **C** could. The *Source* column traces each requirement to
SOW 0.1; *new* marks requirements added in this revision.

## 5. Requirements

### 5.1 Tenancy and isolation

| ID | Requirement | Pri | Rel | Source |
|---|---|---|---|---|
| TEN-01 | Every tenant-owned resource carries its tenant. Platform-global data is stored separately and governed by explicit policies | M | R1 | MT-01, MT-13 |
| TEN-02 | The tenant of a request derives only from the verified credential or a server-side association. A tenant identifier supplied by the caller neither establishes nor overrides it | M | R1 | MT-03, AUTHZ-03 |
| TEN-03 | A missing or unverifiable tenant context rejects the request. There is no default or global fallback | M | R1 | MT-02, MT-05 |
| TEN-04 | Tenant data is protected by row-level security in the database, in addition to application checks. A missing application filter must not be enough to expose another tenant's rows | M | R1 | MT-08, MT-10, MT-11, DB-05 |
| TEN-05 | Runtime database roles own no tables and cannot bypass row-level security. Schema changes use separate credentials | M | R1 | MT-09, DB-06 |
| TEN-06 | Isolation holds uniformly for APIs, vault, audit, usage, limit counters, caches, exports and background jobs. A job carries the tenant and the identity that originated it; there is no unscoped system identity | M | R1 | MT-04, MT-12, DB-10, DB-11 |
| TEN-07 | An automated suite proves isolation with negative tests for every tenant-scoped table and API route. The build fails when a tenant-scoped table has no policy or no tests | M | R1 | MT-06, MT-14 |
| TEN-08 | Responses never reveal whether a resource of another tenant exists, through status codes, errors, identifiers or metadata | M | R1 | API-16 |
| TEN-09 | A tenant has a lifecycle: provisioning, active, suspended, deleting, deleted. The platform admin can create, suspend and reactivate it | M | R1 | TEN-01, TEN-02, TEN-04, TEN-08 |
| TEN-10 | Suspension blocks inference and tenant administration within a documented maximum time, and preserves data | M | R1 | TEN-03 |
| TEN-11 | Deletion is a staged process: suspension, revocation of access credentials, cryptographic erasure of secrets, removal of tenant data according to the retention policy, and a verifiable completion record. It never touches global data or other tenants | M | R2 | TEN-05, TEN-06, TEN-09, DB-08, DB-12 |
| TEN-12 | A tenant's data can be exported in a documented format before deletion | S | R3 | TEN-07 |

### 5.2 Identity and authentication

| ID | Requirement | Pri | Rel | Source |
|---|---|---|---|---|
| IAM-01 | Pandokos is not an identity provider for other systems: it issues no tokens for third-party applications and offers no self-registration, no account recovery by email and no MFA of its own. Installations that need those use an external IdP | M | R1 | §6.1 |
| IAM-02 | A tenant can create service accounts with an identifier, status, roles and validity dates | M | R1 | AUTH-S-01 |
| IAM-03 | Pandokos issues virtual keys for service accounts. A key is shown once, stored only as a hash, identified by a non-secret key ID, and carries a recognizable prefix and checksum so secret scanners can detect it | M | R1 | AUTH-S-05, AUTH-S-10, new |
| IAM-04 | A virtual key always belongs to exactly one tenant and can be restricted by scope, model allow-list, expiry and limits | M | R1 | AUTH-S-05, AUTH-S-06 |
| IAM-05 | A service account can hold several active keys, so rotation needs no downtime. Each key can be revoked on its own; revocation takes effect within a documented bound | M | R1 | AUTH-S-08, AUTH-S-09 |
| IAM-06 | Pandokos accepts signed access tokens (JWT) from registered trusted issuers and validates signature, issuer, audience, expiry and required claims | M | R1 | AUTH-S-02, AUTH-H-03, SEC-DP-02 |
| IAM-07 | A trusted issuer is either bound to one tenant, or registered at platform level with the tenant read from a configured claim and limited to an explicit list of tenants. There is no implicit wildcard | M | R1 | AUTH-S-07 |
| IAM-08 | Token subjects map to service accounts or users by explicit rules. An unmapped subject is rejected | M | R1 | AUTH-H-04 |
| IAM-09 | People sign in to the console through an external OpenID Connect provider, with the authorization code flow and PKCE, or with a local account (IAM-18). Both lead to the same kind of user record, roles and session | M | R2 | AUTH-H-01, AUTH-H-02 |
| IAM-10 | A federated identity links to a local user record with memberships in one or more tenants. The active tenant of a request is explicit and validated against the memberships | M | R2 | AUTH-H-04, AUTH-H-05 |
| IAM-11 | A user can be disabled locally whatever the IdP says. Their sessions end within a documented bound | M | R2 | AUTH-H-06, AUTH-H-10 |
| IAM-12 | Groups or roles asserted by the IdP can be mapped to Pandokos roles by explicit, optional rules | S | R2 | AUTH-H-07 |
| IAM-13 | An installation without an IdP is fully usable: automation and the CLI use admin-scoped virtual keys, people use local accounts in the console | M | R1 (keys), R2 (accounts) | AUTH-H-08 |
| IAM-14 | The first platform admin is created by an explicit initialization command: an expiring admin key and, from R2, a local account whose generated password must be changed at first sign-in. There are no default credentials. Pandokos warns while the bootstrap key is still active | M | R1 | SEC-CP-03, SEC-CP-04 |
| IAM-15 | Callers can authenticate with mutual TLS; the certificate identity (including a SPIFFE ID) maps to a service account | S | R4 | AUTH-S-04 |
| IAM-16 | Access tokens can be bound to the client certificate (RFC 8705) | C | later | AUTH-S-03 |
| IAM-17 | Sensitive administrative operations can require a minimum authentication strength or freshness asserted by the IdP | C | R5 | AUTH-H-09 |
| IAM-18 | Pandokos keeps local accounts, a username and a password, so that people can sign in to the console without an IdP. Accounts are created by an administrator. The mechanism is on by default and can be switched off when an IdP is configured | M | R2 | AUTH-H-08 |
| IAM-19 | Passwords are stored only as a memory-hard salted hash. A minimum length applies. A generated or reset password must be changed at the next sign-in. A password is reset by an administrator or from the host CLI; there is no self-registration and no recovery by email | M | R2 | new |
| IAM-20 | Failed sign-ins are throttled per account and per source and are audited. The response does not reveal whether the username exists | M | R2 | AUD-01, new |
| IAM-21 | Signing in creates a server-side session carried by a cookie that scripts cannot read. Sessions have an idle and an absolute lifetime, can be revoked, end on sign-out, password change and disabling of the user, and are protected against cross-site request forgery. Local and IdP sign-in produce the same session | M | R2 | AUTH-H-10, new |
| IAM-22 | Only people sign in interactively, and a session is accepted only by the administration API on behalf of the console. A session or a password never authenticates an inference request, and a service account cannot sign in to the console | M | R2 | new |

### 5.3 Authorization

| ID | Requirement | Pri | Rel | Source |
|---|---|---|---|---|
| AZ-01 | Every request is authorized after it is authenticated, on the inference API, the administration API and background jobs. The default is deny | M | R1 | AUTHZ-01, AUTHZ-04, AUTHZ-07 |
| AZ-02 | Permissions are `resource:action` pairs defined and documented by the product. A decision considers identity, tenant, permission, target resource and tenant state. Read, write, delete, rotate and use are distinct | M | R1 | AUTHZ-02, AUTHZ-05, AUTHZ-06 |
| AZ-03 | Pandokos ships immutable built-in tenant roles: owner, admin, credential manager, usage viewer, auditor, developer, inference, read-only | M | R1 (admin, inference), R2 (all) | RBAC-05, RBAC-06 |
| AZ-04 | A tenant can define custom roles from the permission catalogue. Nobody can grant a permission they do not hold | S | R2 | RBAC-02, RBAC-03, new |
| AZ-05 | A role that is still assigned cannot be deleted; roles can be deactivated | S | R2 | RBAC-07 |
| AZ-06 | Platform roles are separate from tenant roles and imply no tenant membership: platform admin, platform auditor, platform billing operator | M | R1 (admin), R2 (others) | SYS-01, SYS-13 |
| AZ-07 | The platform admin manages the tenant lifecycle, the provider and model catalogue, platform credentials and platform-wide limits | M | R1 | SYS-02, SYS-04 to SYS-07 |
| AZ-08 | No platform role can read, export or replace a tenant's secret. A platform admin can see credential metadata and can disable or revoke a tenant credential, with a reason, on the audit record | M | R1 | SYS-08, SYS-09, SYS-10 |
| AZ-09 | A platform admin acting inside a tenant (its users, roles or keys) performs an explicit support action that requires a reason, is audited in the tenant's log and can be disabled by configuration | S | R2 | SYS-03, SYS-11, SYS-12 |
| AZ-10 | Managing credentials, managing identities and viewing costs can be granted separately | M | R2 | §7.2 |
| AZ-11 | The administration API tells a signed-in user who they are, which tenants they belong to and which permissions they hold in each, so that a client can offer only what is allowed. Hiding a function in a client is never the control: the API checks every request | M | R2 | new |

### 5.4 Vault and provider credentials

| ID | Requirement | Pri | Rel | Source |
|---|---|---|---|---|
| VLT-01 | A tenant can register several labelled credentials per provider. Exactly one is the default for each provider; a virtual key can be pinned to a specific credential. Pandokos never chooses between credentials on its own | M | R1 | VAULT-01, new |
| VLT-02 | Credentials owned by a tenant (BYOK) and by the platform are distinguishable in ownership, cost responsibility and policy | M | R1 (BYOK), R3 (platform) | VAULT-02 |
| VLT-03 | A credential can be created, validated, replaced, disabled, revoked and deleted without recreating the tenant or its permissions | M | R1 | VAULT-03, VAULT-07 |
| VLT-04 | No API returns a secret after it is written. Only metadata is readable: provider, label, fingerprint, status, owner, timestamps of creation, validation and rotation, expiry if known | M | R1 | VAULT-05, VAULT-10 |
| VLT-05 | A credential can be checked against the provider when it is written, without being logged | S | R1 | VAULT-06 |
| VLT-06 | A secret is decrypted only by the data plane, only after the request is authenticated and authorized, and only for the time needed to call the provider plus a bounded cache lifetime | M | R1 | VAULT-04, SEC-DP-04 |
| VLT-07 | A credential cannot be used for another tenant: it is bound to its tenant cryptographically and by row-level security | M | R1 | VAULT-11 |
| VLT-08 | Disabling or revoking a credential takes effect within a documented maximum time | M | R1 | VAULT-08 |
| VLT-09 | Secrets are encrypted at rest with authenticated encryption under a per-tenant data key, itself wrapped by a key-encryption key that is never stored in the database | M | R1 | VAULT-14, VAULT-15, VAULT-16, VAULT-19 |
| VLT-10 | The key-encryption key can be rotated by re-wrapping data keys, with no action by tenants. Key versions are recorded | M | R2 | VAULT-17, VAULT-18 |
| VLT-11 | Deleting a tenant destroys its data key, so residual ciphertext, including in backups, can no longer be decrypted | M | R2 | VAULT-09 |
| VLT-12 | The key-encryption key comes from a pluggable provider: a local key file by default, an external KMS or transit service optionally | M (local) / S (external) | R1 / R4 | VAULT-20 |
| VLT-13 | Database backups contain only ciphertext and wrapped keys. The key-encryption key is backed up separately, and the procedure is documented | M | R1 | VAULT-21 |
| VLT-14 | Secrets never appear in logs, traces, metrics, error messages, events or diagnostics. Automated tests plant canary secrets and search every output for them | M | R1 | VAULT-13, SEC-SC-07 |
| VLT-15 | Each use of a credential is attributable to a request, tenant and caller | M | R1 | VAULT-12 |
| VLT-16 | The security limits of the vault and the recovery procedure for lost or compromised keys are documented, distinguishing recoverable secrets from those the owner must re-enter | M | R1 (draft), R5 (tested) | VAULT-22, VAULT-23 |

### 5.5 Provider and model catalogue

| ID | Requirement | Pri | Rel | Source |
|---|---|---|---|---|
| CAT-01 | The platform admin maintains a catalogue of providers. Provider, API family, endpoint, model and credential are distinct concepts. A provider can be enabled or disabled | M | R1 | PROV-01, PROV-03, PROV-04, PROV-06 |
| CAT-02 | Provider support is tiered. Tier 1: OpenAI, Anthropic, and any endpoint that speaks the OpenAI API (which covers xAI, Mistral, DeepSeek, Groq, Together, Fireworks, Perplexity and self-hosted servers). Tier 2: Google Gemini, Azure OpenAI. Tier 3: Amazon Bedrock, Vertex AI, Cohere | M (tier 1) / S (tier 2) / C (tier 3) | R1 / R4 / later | PROV-02 |
| CAT-03 | Endpoints, including regional and private ones, are registered by the platform admin. A tenant can supply its own endpoint only for providers whose catalogue entry declares an allowed host pattern | M / S | R1 / R4 | PROV-05, PROV-07, PROXY-06 |
| CAT-04 | The model catalogue records, when known: public name, upstream identifier, provider, API family, capabilities, status, context window and limits | M | R1 | MODEL-01, MODEL-02 |
| CAT-05 | The platform admin makes a model available to all tenants or to selected ones. A tenant admin can restrict it further for the tenant or for individual keys | M | R1 | MODEL-03, MODEL-04, SYS-05 |
| CAT-06 | A request for a model that is not allowed is rejected before it is forwarded, whatever identifier the caller uses | M | R1 | MODEL-05 |
| CAT-07 | A logical alias can point to a concrete model version | S | R4 | MODEL-06 |
| CAT-08 | Prices are versioned in time, with a currency. The cost of a request uses the price in force when it was made. When no price is known the usage is recorded with an unknown cost, and platform-funded requests are blocked unless configured otherwise | M | R1 | MODEL-07, §11.3 |
| CAT-09 | The price list can be loaded and refreshed from an open dataset and overridden by the operator. Every change creates a new version and an audit record | S | R1 | new |
| CAT-10 | The catalogue can be exported and imported declaratively | S | R2 | NFR-MAINT-02 |

### 5.6 Gateway (inference data plane)

| ID | Requirement | Pri | Rel | Source |
|---|---|---|---|---|
| GW-01 | Provider-native endpoints are forwarded as they are, preserving the semantics of request and response | M | R1 | PROXY-01, PROXY-09, API-02 |
| GW-02 | Stock provider SDKs work by changing only the base URL and the API key. The Pandokos credential is accepted in the authentication header native to each API family | M | R1 | new |
| GW-03 | Streaming responses are forwarded event by event, without buffering the whole response | M | R1 | PROXY-02, PROXY-03 |
| GW-04 | When the caller disconnects the upstream request is cancelled. Timeouts are configurable. Nothing stays reserved or locked afterwards | M | R1 | API-04, NFR-AV-04 |
| GW-05 | Requests go only to destinations in the catalogue. The caller cannot influence host or URL, redirects are not followed, and a provider credential is sent only to the host it is registered for | M | R1 | PROXY-05, PROXY-06, PROXY-07 |
| GW-06 | Outbound connections to private, loopback and link-local addresses are refused unless the endpoint is explicitly marked private, and the check is made on the address actually dialled | M | R1 | SEC-DP-07, new |
| GW-07 | Request headers are forwarded from an allow-list. Inbound credentials and hop-by-hop headers are removed. HTTP parsing is strict | M | R1 | PROXY-07, SEC-DP-07 |
| GW-08 | The provider credential is injected by the proxy and never reaches the caller, including through errors and echoed headers | M | R1 | PROXY-08 |
| GW-09 | Each API family exposes an allow-list of endpoints that Pandokos can meter. Other paths are rejected with a clear error | M | R1 | new |
| GW-10 | Provider errors are returned unchanged, except for sensitive headers. Errors raised by Pandokos use the error shape of the API family being called, plus a stable machine-readable code in a response header | M | R1 | PROXY-04, API-05, RATE-12 |
| GW-11 | Pandokos does not retry, fall back, or balance across providers, models or credentials | M | R1 | §4.2 |
| GW-12 | Every request gets a request ID, returned to the caller. W3C Trace Context from the caller is honoured | M | R1 | API-06, PROXY-11 |
| GW-13 | The caller can attach a bounded set of metadata to a request for attribution. Metadata is recorded with the usage, never used in a security decision and never forwarded to the provider | S | R1 | API-07, USAGE-06 |
| GW-14 | Usage is extracted from streaming and non-streaming responses. Any change to a request needed for this is documented and invisible to the caller | M | R1 | PROXY-12 |
| GW-15 | A unified OpenAI-compatible endpoint routes by model to any provider that itself offers an OpenAI-compatible API. Pandokos performs no semantic translation | S | R4 | API-03, PROXY-10 |
| GW-16 | Request size, header size, duration and per-tenant concurrency are limited | M | R1 (size, duration), R2 (concurrency) | SEC-DP-06, RATE-14 |
| GW-17 | Performance targets are stated, measured and published: latency added by the proxy, concurrent streams per instance, memory. Initial targets, to be validated: added latency p50 ≤ 5 ms and p99 ≤ 20 ms; 5,000 concurrent streams per instance | S | R5 | NFR-PERF-01 to 03 |

### 5.7 Funding modes

| ID | Requirement | Pri | Rel | Source |
|---|---|---|---|---|
| FND-01 | The funding mode is explicit for each tenant and provider: BYOK or platform-funded. Resolution is deterministic and never falls back from one mode to the other | M | R1 (BYOK), R3 | BILL-01 to 03, BILL-07 to 09, new |
| FND-02 | Under BYOK only the tenant's own credential is used. Costs are estimates, marked informational and non-billable | M | R1 | BILL-02 to 06, BILLING-12 |
| FND-03 | The platform admin enables platform-funded access per provider, model and tenant. The tenant never obtains the upstream credential | M | R3 | BILL-07 to 10 |
| FND-04 | A platform-funded request requires an applicable budget. Nothing is unlimited by default | M | R3 | BILL-11, new |
| FND-05 | The amount chargeable to a tenant is the upstream cost with a versioned rating rule applied: a percentage markup or a per-model price. Upstream cost, rule version and chargeable amount stay distinct. Plans, discounts, taxes and invoices belong to the external billing system | S | R3 | BILL-12 to 14, BILLING-11 |

### 5.8 Limits, quotas and budgets

| ID | Requirement | Pri | Rel | Source |
|---|---|---|---|---|
| LIM-01 | Rate limits in requests per minute and tokens per minute | M | R2 | RATE-01, RATE-02 |
| LIM-02 | Cumulative quotas of requests and tokens per day or month, in a configurable time zone (UTC by default) | S | R2 | RATE-03 |
| LIM-03 | Monetary budgets per period | M | R2 | RATE-04 |
| LIM-04 | Limits apply at platform, tenant and virtual-key level, optionally per provider or model. A lower level can only restrict | M | R2 | RATE-05, RATE-11 |
| LIM-05 | Hard limits block. Soft thresholds raise an event | M / S | R2 | RATE-06 |
| LIM-06 | Limits are checked before forwarding and settled with the actual usage afterwards | M | R2 | RATE-07, RATE-08 |
| LIM-07 | Concurrent requests cannot overshoot a budget without bound: capacity is reserved from an estimate before forwarding and settled afterwards. The maximum overshoot is documented | M | R2 | RATE-09 |
| LIM-08 | The treatment of failed, cancelled, refused and usage-less requests is defined and documented | M | R2 | RATE-10 |
| LIM-09 | A limit rejection returns HTTP 429 with a stable code, `Retry-After` and remaining-allowance headers, and is distinguishable from a provider's own 429 | M | R2 | RATE-12, new |
| LIM-10 | Limits stay consistent when several instances serve the same tenant | M | R4 | RATE-15 |
| LIM-11 | Behaviour when the counter store is unavailable is configurable; the default is to refuse | M | R2 | NFR-PERF-05 |
| LIM-12 | The documentation states which limits are strict and which are best effort, and how reservations, resets and missing prices are handled | M | R2 | §11.3 |
| LIM-13 | Threshold and exhaustion events can be delivered to external systems | S | R3 | RATE-13 |

### 5.9 Usage ledger

| ID | Requirement | Pri | Rel | Source |
|---|---|---|---|---|
| USE-01 | Every authenticated inference request produces exactly one usage record in both funding modes, whether it succeeded, failed, was cancelled, was refused for a limit or was never forwarded | M | R1 | USAGE-01, USAGE-05, BILL-04 |
| USE-02 | The ledger holds no prompt, completion or media content | M | R1 | USAGE-02, NFR-PRIV-02 |
| USE-03 | A record holds: record ID, request ID, trace ID, tenant, caller, virtual key or token subject, credential ID, funding mode, provider, model, endpoint, streaming flag, timestamps and duration, outcome, token counts by category (input, output, cached, provider-specific), other billed units, estimated cost, currency, price version and caller metadata | M | R1 | USAGE-03, USAGE-04, §10.3 |
| USE-04 | Values reported by the provider, values estimated by Pandokos and values produced by rating rules are stored separately | M | R1 | §10.3, BILL-05 |
| USE-05 | The ledger is append-only. A correction is a new record that references the original | M / S | R1 / R3 | USAGE-08, BILLING-09 |
| USE-06 | Aggregated reports are available by tenant, period, provider, model, caller and funding mode | M | R2 | USAGE-07 |
| USE-07 | Tenant roles read their tenant's usage. Platform roles read the aggregates they are allowed to see | M | R2 | USAGE-09 |
| USE-08 | Retention is configurable and usage can be exported in an open format | S | R2 | NFR-PRIV-03, BILLING-06 |

### 5.10 Audit

| ID | Requirement | Pri | Rel | Source |
|---|---|---|---|---|
| AUD-01 | Pandokos audits authentication failures and every creation, change, disabling, revocation and deletion of tenants, identities, keys, roles, catalogue entries, credentials and limits | M | R1 | AUD-01, AUD-02, RBAC-08 |
| AUD-02 | An audit record holds: actor and actor type (user, service account, platform), tenant or platform scope, action, resource, time, outcome, request ID, the reason when one is required, and a redacted description of what changed | M | R1 | AUD-03, AUD-04, AUD-05, SYS-12 |
| AUD-03 | The audit record is written atomically with the change it describes | M | R1 | DB-09, new |
| AUD-04 | Audit records cannot be changed or deleted by runtime identities, and tampering is detectable | M / S | R1 / R2 | AUD-06 |
| AUD-05 | Tenant roles read their tenant's audit log. Platform auditors read platform events | M | R2 | AUD-08 |
| AUD-06 | Audit retention is configurable, and the log can be exported or forwarded to an external system | S | R2 | AUD-07 |
| AUD-07 | Audit log, usage ledger and telemetry are three separate records with shared correlation IDs. Telemetry is never the source of accounting | M | R1 | §12.3 |

### 5.11 Billing export

| ID | Requirement | Pri | Rel | Source |
|---|---|---|---|---|
| BIL-01 | Platform-funded usage is published as billable events for external systems. BYOK usage is never published as billable | M | R3 | BILLING-01, BILLING-12, BILL-15 |
| BIL-02 | An event has a stable unique ID usable as idempotency key, the tenant, the external customer reference, quantities, prices, currency and the originating request | M | R3 | BILLING-02, BILLING-10 |
| BIL-03 | Delivery is asynchronous and at least once, with retries, a dead-letter state and replay. No event is lost when Pandokos or the destination fails | M | R3 | BILLING-03 to 05, DB-09 |
| BIL-04 | Generic destinations come first: a signed webhook and a file export, both in a documented open format | M | R3 | BILLING-06 |
| BIL-05 | Adapters for specific billing vendors are optional modules outside the core | C | later | BILLING-07 |
| BIL-06 | Corrections and voids are published as events that reference the original | S | R3 | BILLING-09 |
| BIL-07 | Metering and rating are separate. Rating never rewrites usage | M | R3 | BILLING-08, BILLING-11 |

### 5.12 Observability

| ID | Requirement | Pri | Rel | Source |
|---|---|---|---|---|
| OBS-01 | Pandokos is instrumented with OpenTelemetry and exports traces and metrics over OTLP. Logs are structured and written to standard output | M | R1 (traces), R2 (metrics) | OTEL-01, OTEL-02 |
| OBS-02 | Each provider call produces one span, attached to the caller's trace through W3C Trace Context. Trace headers are never used for authentication or authorization | M | R1 | OTEL-03 to 05 |
| OBS-03 | Spans and metrics follow the OpenTelemetry GenAI semantic conventions. The adopted version is pinned and documented, and changes are announced in release notes | M | R2 | OTEL-06, OTEL-12 |
| OBS-04 | Telemetry holds no content and no secrets | M | R1 | OTEL-07 |
| OBS-05 | Endpoint, protocol, headers, sampling and resource attributes are configurable. A failing collector never blocks or slows inference | M | R1 | OTEL-08, OTEL-09, NFR-AV-03 |
| OBS-06 | Metrics cover requests, errors, time to first byte, total latency, tokens, estimated cost and limit rejections, by provider, model and outcome | M | R2 | OTEL-10 |
| OBS-07 | Tenant and request identifiers are not metric labels by default. Tenant labels are an opt-in for small installations | M | R2 | OTEL-11 |
| OBS-08 | Liveness and readiness endpoints reflect the state of critical dependencies | M | R1 | NFR-MAINT-04 |

### 5.13 Administration API

| ID | Requirement | Pri | Rel | Source |
|---|---|---|---|---|
| API-01 | The administration API listens separately from the inference API and can be bound to a private network | M | R1 | API-08, SEC-CP-01, SEC-CP-05 |
| API-02 | The administration API is versioned, and its OpenAPI specification is the source of truth, published with every release | M | R1 | API-13, NFR-MAINT-01 |
| API-03 | Everything is achievable through the API. A CLI wraps the API; no web console is required | M | R1 | API-09, API-10 |
| API-04 | Collections support pagination, filtering and sorting. Errors share one documented format | M | R1 | API-11 |
| API-05 | Create operations accept an idempotency key | S | R2 | API-12 |
| API-06 | A tenant can be administered by an external system through a tenant-scoped service account that holds no platform privilege | M | R1 | API-14, API-15 |
| API-07 | An integrator can ask whether a tenant is currently able to call a given provider and model (credential present and valid, model allowed, tenant active, budget available) | S | R1 | new |
| API-08 | The web console uses only the documented administration API, which includes the sign-in and session endpoints. It has no private or privileged path | M | R2 | §22 |

### 5.14 Security

| ID | Requirement | Pri | Rel | Source |
|---|---|---|---|---|
| SEC-01 | Every listener can serve TLS natively or run behind a terminating proxy. Certificates of upstream providers are always verified | M | R1 | SEC-DP-01 |
| SEC-02 | The replay exposure of each authentication mechanism is documented. Bearer credentials rely on TLS and short lifetimes; sender-constrained options are IAM-15 and IAM-16 | M | R1 | SEC-DP-03 |
| SEC-03 | Error messages reveal no secrets, internal configuration or data of other tenants | M | R1 | SEC-DP-08 |
| SEC-04 | Defaults are secure. At start-up Pandokos validates its configuration and refuses or warns about weak settings | M | R1 | SEC-SC-08 |
| SEC-05 | A threat model is maintained with the architecture and states the trust boundaries, including what an infrastructure operator with access to hosts, database, backups and keys can do | M | R1 (initial), R5 (reviewed) | SEC-SC-01, §7.4 |
| SEC-06 | The build produces a minimal, non-root image, with pinned dependencies, vulnerability scanning, an SBOM, and signed artefacts with verifiable provenance | M | R1 | SEC-SC-02 to 05 |
| SEC-07 | A vulnerability disclosure policy and a response process exist | M | R1 | SEC-SC-06 |
| SEC-08 | High-impact administrative operations require a reason | S | R2 | SEC-CP-02 |
| SEC-09 | An independent security review takes place before 1.0 | S | R5 | §22 |

### 5.15 Deployment and operations

| ID | Requirement | Pri | Rel | Source |
|---|---|---|---|---|
| OPS-01 | Pandokos ships as one multi-architecture container image that runs without an orchestrator. PostgreSQL is the only mandatory dependency | M | R1 | DEP-03, NFR-MAINT-03 |
| OPS-02 | A supported Docker Compose setup runs a single instance with PostgreSQL | M | R1 | DEP-01 |
| OPS-03 | A supported Docker Compose setup runs several replicas behind a load balancer | S | R4 | DEP-01 |
| OPS-04 | A supported Kubernetes deployment covers configuration, secrets, probes and rolling updates | S | R4 | DEP-02 |
| OPS-05 | Gateway, administration and background work can run in one process or separately. The API contract does not depend on the topology | M | R1 (one process), R4 (separate) | DEP-05 |
| OPS-06 | Isolation tiers: *pool* (shared cell, the default), *silo* (a cell that serves exactly one tenant and refuses any other), *bridge* (a platform with both). The API contract is the same in every tier | S | R4 | §5.3 |
| OPS-07 | Moving a tenant between pool and silo is a documented operator procedure | C | R4 | §5.3 |
| OPS-08 | Static settings come from a file and environment variables; secrets are referenced as files. Dynamic settings live in the database and are managed through the API | M | R1 | NFR-MAINT-02 |
| OPS-09 | Schema migrations are versioned and run by an explicit command. An instance refuses to start against a schema it does not match | M | R1 | DB-03, NFR-MAINT-05 |
| OPS-10 | Backup and restore, including keys, are documented, and a restore is tested | M | R2 (documented), R5 (tested) | NFR-AV-05 |
| OPS-11 | Behaviour on failure of each dependency is defined. If authorization or the credential cannot be verified the request is not forwarded | M | R2 | NFR-PERF-05, NFR-AV-01, NFR-AV-02 |
| OPS-12 | On shutdown, in-flight streams are drained up to a configurable deadline | M | R1 | new |
| OPS-13 | Releases follow semantic versioning with upgrade notes, and upgrading from the previous minor version is supported | S | R5 | NFR-MAINT-05 |
| OPS-14 | Several instances scale horizontally with consistent authentication, limits, usage and revocation | M | R4 | NFR-PERF-04 |

### 5.16 Privacy

| ID | Requirement | Pri | Rel | Source |
|---|---|---|---|---|
| PRV-01 | Personal data is minimal: a user record holds the IdP subject and the display attributes the IdP supplies | M | R2 | NFR-PRIV-01 |
| PRV-02 | The documentation states what data is sent to providers, telemetry collectors and billing destinations | M | R1 | NFR-PRIV-05 |
| PRV-03 | Caller metadata is bounded in number and size, and a tenant can restrict the accepted keys | S | R2 | NFR-PRIV-04 |

### 5.17 Console

| ID | Requirement | Pri | Rel | Source |
|---|---|---|---|---|
| CON-01 | Pandokos ships a web console, embedded in the product and served on the administration listener. It can be switched off | M | R2 | §22 |
| CON-02 | Tenant area, for tenant roles: members and roles, service accounts and virtual keys, provider credentials, model access, limits, usage and cost, audit log | M | R2 | new |
| CON-03 | Platform area, for platform roles: tenants and their lifecycle, providers, models and prices, users and platform roles, platform limits, platform audit log; platform credentials and funding modes when they exist | M | R2, R3 (funding) | new |
| CON-04 | Every function of the console is profiled by roles and permissions: an area, a page or an action appears only when the user's effective permissions (AZ-11) include the permission that the underlying API call requires. The map from console functions to permissions is documented and tested | M | R2 | new |
| CON-05 | A user who belongs to several tenants chooses the tenant to work in. A user with a platform role reaches the platform area. Each area shows only its own scope, and a platform role alone gives no view into a tenant's data | M | R2 | AUTH-H-05 |
| CON-06 | The console never displays a stored secret. A new virtual key is shown once; a provider credential is a write-only field | M | R2 | VAULT-05 |
| CON-07 | The console is served with a strict content security policy, loads nothing from third parties and keeps no credential in browser storage | M | R2 | new |
| CON-08 | From R3 on, a feature with an administrative surface ships its console page in the same release | S | R3 | new |
| CON-09 | Interface text is kept apart from code so it can be translated; English comes first. The console is usable from the keyboard | C | R2 | new |

## 6. Use cases

| ID | Use case | Release | Main requirements |
|---|---|---|---|
| UC-01 | A platform admin creates a tenant | R1 | TEN-09, AZ-07, AUD-01 |
| UC-02 | A tenant admin creates a service account and a virtual key | R1 | IAM-02 to 05 |
| UC-03 | A tenant registers a BYOK credential | R1 | VLT-01 to 05 |
| UC-04 | A service calls a model with BYOK, streaming | R1 | GW-01 to 14, VLT-06, USE-01 |
| UC-05 | A workload calls with a token from a trusted issuer that carries the tenant | R1 | IAM-06 to 08, TEN-02 |
| UC-06 | A tenant rotates a provider credential or a virtual key without downtime | R1 | VLT-03, IAM-05 |
| UC-07 | A caller of tenant A tries to reach a resource of tenant B | R1 | TEN-02 to 08 |
| UC-08 | A user signs in to the console through the IdP and administers a tenant with the functions their role allows | R2 | IAM-09 to 12, AZ-03, CON-02, CON-04 |
| UC-09 | A request exceeds a rate limit or a budget | R2 | LIM-01 to 09 |
| UC-10 | A platform admin inspects the credential metadata of a tenant | R1 | AZ-08, VLT-04 |
| UC-11 | A platform admin deletes a tenant | R2 | TEN-11, VLT-11 |
| UC-12 | A tenant calls a platform-funded model | R3 | FND-03 to 05, LIM-03 |
| UC-13 | Usage is delivered to an external billing system | R3 | BIL-01 to 04 |
| UC-14 | An operator dedicates a cell to a tenant | R4 | OPS-06, OPS-07 |
| UC-15 | With no IdP, a platform admin signs in with a local account, creates a tenant and a local account for its administrator | R2 | IAM-18 to 22, CON-03 |
| UC-16 | A user with a limited role (for example usage viewer) signs in and finds only the pages of that role | R2 | AZ-11, CON-04 |

## 7. Acceptance per release

**R1 — v0.1.** (1) `docker compose up` gives a working installation with a persistent database.
(2) Two tenants coexist; a negative test suite shows neither can read, change or delete the
other's data through the API, and that the runtime database roles cannot do so with plain SQL.
(3) A virtual key and a token from a trusted issuer both authenticate a call, with the tenant
derived from the credential. (4) A BYOK credential is stored encrypted and never returned.
(5) The official OpenAI and Anthropic SDKs call through Pandokos, streaming and non-streaming,
with only base URL and key changed. (6) Each call produces one usage record with tokens and
estimated cost. (7) Administrative changes are audited. (8) A span per provider call reaches an
OTLP collector. (9) Revocation times of keys and credentials are documented and tested. (10) The
image is minimal, scanned, signed and ships an SBOM. (11) The threat model states the limits of
protection against whoever controls host, database and keys.

**R2 — v0.2.** A person signs in to the console with a local account and, where one is configured,
through an external OIDC provider; the console offers each user only the functions of their roles,
and the API refuses the others whatever the client sends; roles gate every administrative route; rate limits and budgets hold under concurrent load within the documented bound; reports
answer who spent what; a tenant is deleted with cryptographic erasure and a completion record; the
key-encryption key is rotated without tenant action; backup and restore are documented.

**R3 — v0.3.** A tenant uses a platform-funded model without ever seeing the credential; every
such call yields a rated, billable event that reaches a webhook exactly once from the receiver's
point of view, including across restarts and receiver outages.

**R4 — v0.4.** Three replicas enforce one tenant's limits consistently; the Helm chart installs a
working system; a silo cell refuses every tenant but its own; Gemini and Azure OpenAI pass the
compatibility suite.

**R5 — v1.0.** Published performance figures meet the targets or the targets are revised; a
restore drill succeeds; the security review has no open high-severity finding; the upgrade policy
is in force.

## 8. Reference integration scenario

The first intended integrator is a multi-tenant agentic SaaS platform whose agent service calls
models on behalf of many customer organizations, each paying with its own provider key. The
requirements above must let that platform:

- remove every customer secret from its agent service, so the agent needs no key store and no
  direct route to the internet (VLT-*, GW-05);
- authenticate the agent with tokens from its own identity provider that carry the tenant, so the
  same contract holds whether one shared agent serves all tenants or each tenant has its own agent
  (IAM-06, IAM-07, OPS-06);
- keep using provider-native client libraries, pointed at Pandokos (GW-01, GW-02);
- attribute each call to its own business concepts, such as a conversation turn and a purpose,
  through metadata and trace context (GW-12, GW-13, OBS-02);
- learn whether a tenant can use AI at this moment (API-07);
- write keys and limits and read usage from its back end through a tenant-scoped API (API-06);
- propagate the deletion of a customer, including its keys (TEN-11, VLT-11);
- enforce a spend cap in one place (LIM-03, LIM-07).

## 9. What changed from SOW 0.1

SOW 0.1 listed 267 requirements in a flat list with no priorities. This revision has 179, each with a priority and a release, and 16 use cases: it merges
duplicates, assigns every requirement a priority and a release, and aligns the scope with what
products of this class deliver.

**Merged.** The tenant-context rule appeared under tenancy, authorization and data-plane security;
row-level security under tenancy and persistence; the no-content rule under usage, telemetry and
privacy; SSRF protections under proxy and security. Each is now stated once.

**Removed or replaced.**

| SOW 0.1 | Now | Why |
|---|---|---|
| Optional local authentication for users (AUTH-H-08) | Kept and made precise: minimal local accounts, valid for the console only (IAM-18 to IAM-22), with no self-registration, recovery by email or MFA | Without an IdP people still need a way to sign in. The stated limits keep the feature short of an identity provider |
| DPoP, HTTP message signatures, token exchange inside the proxy (§6.4) | Not implemented. Token exchange happens at the IdP and Pandokos validates the resulting token | Sender-constrained tokens are covered by mutual TLS (IAM-15, IAM-16); the others add protocol surface with no caller that needs them |
| SCIM provisioning | Out of scope | Users are provisioned on first mapped sign-in or through the API |
| Rate limits per role (§11.1) | Removed | Limits attach to tenant, key, provider and model; roles are an authorization concept |
| Telemetry failure may block inference (NFR-AV-03) | Removed: it never blocks (OBS-05) | Telemetry is not an accounting source, so nothing is lost that justifies an outage |
| Automatic provisioning and autoscaling of silos (§5.4) | Out of scope. A silo is a pinned cell deployed by the operator (OPS-06) | It requires an orchestrator-specific controller; the product must not require Kubernetes |
| Pricing plans, discounts and a vendor billing adapter in the core (BILL-13, BILLING-07) | One rating rule (FND-05); vendor adapters outside the core (BIL-05) | Anything more is a billing system |
| Opt-in storage of content (implied by "by default" in USAGE-02, OTEL-07) | No such option (principle 8) | An archive of conversations changes the product's risk class |
| Step-up authentication as a general requirement (AUTH-H-09) | Could-have, limited to checking IdP claims (IAM-17) | Step-up is performed by the IdP |

**Added.** Compatibility with stock SDKs through native authentication headers (GW-02); an
allow-list of metered endpoints (GW-09); errors in the shape of the called API (GW-10); an
explicit rule against silent fallback between funding modes (FND-01) and between credentials
(VLT-01); mandatory budgets for platform-funded use (FND-04); limit headers (LIM-09); an
anti-escalation rule for role grants (AZ-04); detectable key format (IAM-03); canary-secret tests
(VLT-14); egress address checks (GW-06); atomic audit (AUD-03); a tenant capability check for
integrators (API-07); graceful draining (OPS-12); refreshing prices from an open dataset (CAT-09). A web console with a platform area and a tenant
area, profiled by permissions (5.17, AZ-11), and the sessions behind it (IAM-21, IAM-22): SOW 0.1
mentioned a dashboard only in passing.

**Rescoped.** The fifteen named providers became three tiers by API family (CAT-02): supporting
the OpenAI wire protocol covers most of the list with one adapter. The unified OpenAI-compatible
endpoint stays, but only as routing to providers that already speak that protocol (GW-15).
Pool, silo and bridge became deployment tiers built on cells (OPS-06).

## 10. Standards

OpenID Connect Core 1.0; OAuth 2.0 (RFC 6749, 6750), security best current practice (RFC 9700),
PKCE (RFC 7636), JWT access tokens (RFC 9068), mutual TLS (RFC 8705); SPIFFE for workload
identity; problem details for HTTP APIs (RFC 9457); CloudEvents 1.0; OpenTelemetry, OTLP, W3C
Trace Context and the OpenTelemetry GenAI semantic conventions; OWASP ASVS and API Security Top
10; NIST SSDF; SLSA; SBOM in SPDX or CycloneDX.
