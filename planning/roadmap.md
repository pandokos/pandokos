# Roadmap

- Baseline: 2026-11-11
- Requirements: [docs/requirements](../docs/requirements/README.md) · Architecture:
  [docs/architecture](../docs/architecture/overview.md)

Eleven epics, thirty sprints, five releases. Epic numbers are identifiers, not an order: epic 11
belongs to v0.2. Sprints are ordered; an epic can start when the
epics it depends on are done, unless noted.

## Releases

| Release | Theme | Epics | Sprints |
|---|---|---|---|
| **v0.1** | Core BYOK | 1, 2, 3 and sprint 4.1 | 9 |
| **v0.2** | Governance and console | rest of 4, 5, 6, 11 | 10 |
| **v0.3** | Platform-funded and billing | 7 | 3 |
| **v0.4** | Scale-out | 8, 9 | 5 |
| **v1.0** | Hardening | 10 | 3 |

```mermaid
flowchart LR
    E1[1 Foundations] --> E2[2 Vault and catalogue]
    E2 --> E3[3 Gateway]
    E1 --> E4[4 Identity and RBAC]
    E3 --> V01((v0.1))
    E4 -. sprint 4.1 .-> V01
    V01 --> E5[5 Limits and budgets]
    V01 --> E6[6 Observability, reports, lifecycle]
    E4 --> V02((v0.2))
    E5 --> V02
    E6 --> V02
    E4 --> E11[11 Console]
    E5 --> E11
    E6 --> E11
    E11 --> V02
    V02 --> E7[7 Platform-funded and billing]
    E7 --> V03((v0.3))
    V03 --> E8[8 Scale-out]
    V03 --> E9[9 More providers]
    E8 --> V04((v0.4))
    E9 --> V04
    V04 --> E10[10 Hardening]
    E10 --> V10((v1.0))
```

## Sprints

| # | Sprint | Outcome |
|---|---|---|
| **1** | **[Foundations](epics/01-foundations/epic.md)** | |
| 1.1 | Walking skeleton | A signed container forwards a streaming OpenAI call with a key from configuration and logs the usage it read. CI, scans and release pipeline exist |
| 1.2 | Persistence and tenant isolation | Tenants in PostgreSQL under forced row-level security, three runtime roles, the isolation test harness and schema lint |
| 1.3 | Service accounts, virtual keys, authorization core, audit | A virtual key authenticates a call and yields a tenant-bound principal; the admin API and CLI manage tenants and keys; changes are audited |
| **2** | **[Vault and catalogue](epics/02-vault-and-catalogue/epic.md)** | |
| 2.1 | Vault | BYOK credentials stored under per-tenant keys, write-only, with validation and canary tests |
| 2.2 | Catalogue and prices | Providers, endpoints, models, grants to tenants, versioned prices imported from the open dataset |
| **3** | **[Gateway](epics/03-gateway/epic.md)** | |
| 3.1 | Authenticated passthrough | The full pipeline for non-streaming OpenAI calls: principal, model check, credential from the vault, egress guard, error contract |
| 3.2 | Streaming, usage and ledger | Streaming with usage extraction; one usage record with cost per call; cancellation; a span per provider call |
| 3.3 | Anthropic and OpenAI-compatible providers | The Anthropic family; any OpenAI-compatible endpoint by configuration; SDK compatibility suite; Compose reference |
| **4** | **[Identity federation and RBAC](epics/04-identity-and-rbac/epic.md)** | |
| 4.1 | Trusted token issuers | JWTs from registered issuers authenticate calls; the tenant comes from the registration or from a claim — *in v0.1* |
| 4.2 | Users, local accounts and sessions | People sign in with a local account or through an external OIDC provider and obtain a session; memberships in several tenants; local disable |
| 4.3 | Roles | All built-in roles, custom roles, platform roles, support actions with reason; the API reports a user's effective permissions |
| **5** | **[Limits and budgets](epics/05-limits-and-budgets/epic.md)** | |
| 5.1 | Reserve and settle | The limiter interface with in-process counters; request and token rate limits; concurrency; the 429 contract |
| 5.2 | Quotas and budgets | Periodic quotas and monetary budgets with reservation; scopes; soft thresholds; reconciliation from the ledger |
| **6** | **[Observability, reports and lifecycle](epics/06-observability-reports-lifecycle/epic.md)** | |
| 6.1 | Metrics and conventions | OTLP metrics, GenAI attributes at a pinned version, Prometheus endpoint, cardinality policy |
| 6.2 | Reports and audit reading | Usage rollups and report API; audit reading with hash chain and verification; retention; exports |
| 6.3 | Tenant deletion and key rotation | Staged deletion with cryptographic erasure; rotation of the key-encryption key; backup and restore guide |
| **11** | **[Console](epics/11-console/epic.md)** — *in v0.2* | |
| 11.1 | Shell and sign-in | The console embedded in the binary: sign-in page, session, navigation built from permissions, tenant switch, security headers |
| 11.2 | Tenant area | Members and roles, service accounts and keys, credentials, model access, limits, usage, audit |
| 11.3 | Platform area | Tenants, catalogue and prices, users and platform roles, platform limits and audit; first-run experience |
| **7** | **[Platform-funded access and billing](epics/07-platform-funded-and-billing/epic.md)** | |
| 7.1 | Platform credentials and funding modes | Operator-owned credentials; explicit funding mode; mandatory budgets; rating rule |
| 7.2 | Outbox and delivery | Billable events through the outbox to a signed webhook; retries, dead letters, replay; export |
| 7.3 | Corrections and notifications | Correction and void events; limit notifications; tenant export |
| **8** | **[Scale-out and deployment](epics/08-scale-out/epic.md)** | |
| 8.1 | Shared counters | Valkey counter store; replicas behind a load balancer in Compose; consistency tests |
| 8.2 | Kubernetes and split roles | Helm chart; gateway, admin and worker as separate deployments; external key-encryption key provider |
| 8.3 | Cells and mutual TLS | Pinned silo cell; bridge routing guide; tenant move procedure; client certificates |
| **9** | **[More providers](epics/09-more-providers/epic.md)** | |
| 9.1 | Gemini | The Gemini family and the unified OpenAI-compatible endpoint |
| 9.2 | Azure OpenAI | The Azure variant and tenant-supplied endpoints under host patterns |
| **10** | **[Hardening](epics/10-hardening/epic.md)** | |
| 10.1 | Performance and resilience | Load tests against the targets; failure injection for every dependency; published figures |
| 10.2 | Security review | Threat model review, independent assessment, fixes; fuzzing in CI |
| 10.3 | Operations and 1.0 | Restore drill, upgrade policy, complete documentation, release |

## After 1.0

Not planned in detail: Bedrock, Vertex AI and Cohere; billing vendor adapters;
batch and file endpoints; identity providers configured per tenant for console sign-in; a Kubernetes operator for cells; certificate-bound tokens; sealing
credentials with public keys.

## What can change this plan

- **Sprint 1.1 is a deliberate test of the riskiest assumptions**: that the Go standard proxy
  handles provider streams, cancellation and usage extraction as designed. If it does not, ADRs
  0002, 0009 and 0010 are revisited before anything is built on them.
- **Sprint 1.2 tests the isolation model** with connection pooling and three roles. A problem
  there changes ADR 0004 and everything after.
- The first integrator needs v0.1 plus a spend cap. If that need becomes urgent, sprint 5.1 and
  the budget part of 5.2 can move ahead of epic 4's remaining sprints without other changes.
