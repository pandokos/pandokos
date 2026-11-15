# Roadmap

- Baseline: 2026-11-15 (first baseline 2026-11-11)
- Requirements: [docs/requirements](../docs/requirements/README.md) · Architecture:
  [docs/architecture](../docs/architecture/overview.md)

Ten epics, twenty-eight sprints, five releases. Epic numbers are identifiers, not an order, and
numbers 7 and 11 are not used. Sprints are ordered; an epic can start when the epics it depends on
are done, unless noted.

## Releases

A release is the tag `vMAJOR.MINOR.PATCH` on the commit of `main` that met its acceptance criteria
([requirements §7](../docs/requirements/README.md)).

| Tag | Theme | Epics | Sprints | Status |
|---|---|---|---|---|
| **v0.1.0** | Core BYOK | 1, 2, 3 and sprint 4.1 | 9 | 8 done |
| **v0.2.0** | Governance | 5, rest of 4, and 6 without sprint 6.4 | 7 | |
| **v0.3.0** | Ready to operate | sprint 6.4, 12 | 4 | |
| **v0.4.0** | Scale-out and providers | 8 without sprint 8.3, 9 | 4 | |
| **v1.0.0** | Hardening | 10, sprint 8.3 | 4 | |

```mermaid
flowchart LR
    E1[1 Foundations] --> E2[2 Vault and catalogue]
    E2 --> E3[3 Gateway]
    E1 --> E4[4 Identity and RBAC]
    E3 --> V01((v0.1.0))
    E4 -. sprint 4.1 .-> V01
    V01 --> E5[5 Limits and budgets]
    V01 --> E6[6 Observability, reports, lifecycle]
    E4 --> V02((v0.2.0))
    E5 --> V02
    E6 --> V02
    V02 --> S64[6.4 Events]
    V02 --> E12[12 Operability]
    S64 --> V03((v0.3.0))
    E12 --> V03
    V03 --> E8[8 Scale-out]
    V03 --> E9[9 More providers]
    E8 --> V04((v0.4.0))
    E9 --> V04
    V04 --> E10[10 Hardening]
    V04 -. sprint 8.3 .-> V10
    E10 --> V10((v1.0.0))
```

## Sprints

| # | Sprint | Outcome | Status |
|---|---|---|---|
| | **v0.1.0 — Core BYOK** | | |
| **1** | **[Foundations](epics/01-foundations/epic.md)** | | done |
| 1.1 | Walking skeleton | A signed container forwards a streaming OpenAI call with a key from configuration and logs the usage it read. CI, scans and release pipeline exist | done |
| 1.2 | Persistence and tenant isolation | Tenants in PostgreSQL under forced row-level security, three runtime roles, the isolation test harness and schema lint | done |
| 1.3 | Service accounts, virtual keys, authorization core, audit | A virtual key authenticates a call and yields a tenant-bound principal; the admin API and CLI manage tenants and keys; changes are audited | done |
| **2** | **[Vault and catalogue](epics/02-vault-and-catalogue/epic.md)** | | done |
| 2.1 | Vault | BYOK credentials stored under per-tenant keys, write-only, with validation and canary tests | done |
| 2.2 | Catalogue and prices | Providers, endpoints, models, grants to tenants, versioned prices imported from the open dataset | done |
| **3** | **[Gateway](epics/03-gateway/epic.md)** | | done |
| 3.1 | Authenticated passthrough | The full pipeline for non-streaming OpenAI calls: principal, model check, credential from the vault, egress guard, error contract | done |
| 3.2 | Streaming, usage and ledger | Streaming with usage extraction; one usage record with cost per call; cancellation; a span per provider call | done |
| 3.3 | Anthropic and OpenAI-compatible providers | The Anthropic family; any OpenAI-compatible endpoint by configuration; SDK compatibility suite; Compose reference | done |
| **4** | **[Identity federation and RBAC](epics/04-identity-and-rbac/epic.md)** | | |
| 4.1 | Trusted token issuers | JWTs from registered issuers authenticate calls; the tenant comes from the registration or from a claim | |
| | **v0.2.0 — Governance** | | |
| **5** | **[Limits and budgets](epics/05-limits-and-budgets/epic.md)** | | |
| 5.1 | Reserve and settle | The limiter interface with in-process counters; request and token rate limits; concurrency; the 429 contract | |
| 5.2 | Quotas and budgets | Periodic quotas and monetary budgets with reservation; scopes; soft thresholds; reconciliation from the ledger | |
| **4** | **Identity federation and RBAC** (continued) | | |
| 4.2 | Users, local accounts and sessions | People sign in to the administration API with a local account or through an external OIDC provider and obtain a session; memberships in several tenants; local disable | |
| 4.3 | Roles | All built-in roles, custom roles, platform roles, support actions with reason; the API reports a user's effective permissions | |
| **6** | **[Observability, reports and lifecycle](epics/06-observability-reports-lifecycle/epic.md)** | | |
| 6.1 | Metrics and conventions | OTLP metrics, GenAI attributes at a pinned version, Prometheus endpoint, cardinality policy | |
| 6.2 | Reports and audit reading | Usage rollups and report API; audit reading with hash chain and verification; retention; exports | |
| 6.3 | Tenant deletion and key rotation | Staged deletion with cryptographic erasure; tenant export; rotation of the key-encryption key; backup and restore guide | |
| | **v0.3.0 — Ready to operate** | | |
| 6.4 | Events for external systems | Usage events and limit notifications through the outbox to a signed webhook; retries, dead letters, replay; export | |
| **12** | **[Operability](epics/12-operability/epic.md)** | | |
| 12.1 | External key-encryption key | The key-encryption key held by an external service over a transit API; none on the Pandokos hosts | |
| 12.2 | Threat model review and fuzzing | The threat model checked against the code as built; fuzzing of the network parsers in CI | |
| 12.3 | Restore drill and upgrade policy | A restore from backups, including loss of the key-encryption key; upgrade policy and tested upgrade; an instance defined by configuration alone | |
| | **v0.4.0 — Scale-out and providers** | | |
| **8** | **[Scale-out and deployment](epics/08-scale-out/epic.md)** | | |
| 8.1 | Shared counters | Valkey counter store; replicas behind a load balancer in Compose; consistency tests | |
| 8.2 | Kubernetes and split roles | Helm chart; gateway, admin and worker as separate deployments | |
| **9** | **[More providers](epics/09-more-providers/epic.md)** | | |
| 9.1 | Gemini | The Gemini family and the unified OpenAI-compatible endpoint | |
| 9.2 | Azure OpenAI | The Azure variant and tenant-supplied endpoints under host patterns | |
| | **v1.0.0 — Hardening** | | |
| **10** | **[Hardening](epics/10-hardening/epic.md)** | | |
| 10.1 | Performance and resilience | Load tests against the targets; failure injection for every dependency; published figures | |
| 10.2 | Independent security assessment | Independent assessment and fixes; authentication-strength checks | |
| 8.3 | Cells and mutual TLS | Pinned silo cell; bridge routing guide; tenant move procedure; client certificates | |
| 10.3 | Documentation and 1.0 | Complete documentation, release | |

## After 1.0

Not planned in detail: Bedrock, Vertex AI and Cohere; batch and file endpoints; identity providers
configured per tenant for interactive sign-in; a Kubernetes operator for cells; certificate-bound
tokens; sealing credentials with public keys.

## What can change this plan

- **Sprint 1.1 was a deliberate test of the riskiest assumptions**: that the Go standard proxy
  handles provider streams, cancellation and usage extraction as designed. It held.
- **Sprint 1.2 tested the isolation model** with connection pooling and three roles. It held.
- The first integrator needs v0.1.0 plus a spend cap. Epic 5 therefore opens v0.2.0; if the need
  becomes urgent it can be released on its own before the rest of the release.
- Whoever runs Pandokos for others holds their provider keys. That is why the external
  key-encryption key, the threat model review and the restore drill come before scale-out.
