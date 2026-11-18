# Pandokos

**LLM access for every tenant of your platform, isolated by design, not by convention.**

Pandokos is a self-hosted, multi-tenant LLM proxy for teams that build platforms. Each tenant brings
its own provider keys into a write-only vault, calls OpenAI and Anthropic APIs with the stock SDKs,
and authenticates with virtual keys or with tokens from its own identity provider; every call is
metered. Tenants are kept apart by PostgreSQL row-level security. All of it is in the Apache 2.0
core: there is no enterprise edition.

Federated identity is tested in CI against **Keycloak 26.8**, **Ory Hydra v26.2** and
**Authentik 2026.8** ([trusted issuers](docs/operations/tokens.md)).

[![CI](https://github.com/pandokos/pandokos/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/pandokos/pandokos/actions/workflows/ci.yml)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue)](LICENSE)
[![Release](https://img.shields.io/github/v/release/pandokos/pandokos)](https://github.com/pandokos/pandokos/releases)

## How it compares

Pandokos is younger and narrower than LiteLLM and Bifrost. It does fewer things, and puts tenant
isolation, custody of tenants' keys and federated identity in the open-source core.

✅ in the open-source core · 💼 enterprise edition only · 🗓️ on the Pandokos roadmap · ➖ not planned

| | Pandokos | LiteLLM | Bifrost |
|---|---|---|---|
| License | Apache 2.0, one edition | MIT core, commercial enterprise edition | Apache 2.0 core, commercial enterprise edition |
| Written in | Go | Python | Go |
| **Tenants and secrets** | | | |
| Unit of isolation | ✅ tenant, enforced by PostgreSQL row-level security and separate database roles | ✅ team · 💼 organization | ✅ customer and team · 💼 business unit |
| Tenants' own provider keys (BYOK) | ✅ write-only vault, envelope encryption with per-tenant keys | ✅ provider keys encrypted in the database · 💼 team-managed models | ✅ provider keys · 💼 secret managers |
| Key-encryption key in an external KMS or secret manager | 🗓️ v0.3 | 💼 | 💼 |
| **Identity and control** | | | |
| Calls authenticated with tokens from your IdP (JWT) | ✅ tested on Keycloak, Ory Hydra, Authentik | 💼 | 💼 OIDC and JWT (SSO) |
| People sign in with SSO or OIDC | 🗓️ v0.2 | 💼 free up to 5 users | 💼 |
| Roles (RBAC) | ✅ built-in roles · 🗓️ v0.2 custom roles | 💼 | 💼 |
| Audit log of administrative changes | ✅ atomic, append-only | 💼 | 💼 |
| **Spend** | | | |
| Usage and cost of every call | ✅ without prompts or completions | ✅ | ✅ |
| Rate limits, quotas, budgets | ✅ per instance · 🗓️ v0.4 shared across replicas | ✅ | ✅ |
| **Providers and traffic** | | | |
| Providers | OpenAI, Anthropic, any OpenAI-compatible API · 🗓️ v0.4 Gemini, Azure OpenAI | 100+ | 23+ |
| Fallbacks, load balancing, routing | ➖ a transparent proxy by design | ✅ | ✅ · 💼 adaptive |
| Caching | ➖ | ✅ | ✅ semantic |
| Guardrails and moderation | ➖ | ✅ custom, Presidio · 💼 built-in | 💼 |
| MCP gateway | ➖ | ✅ | ✅ |
| **Operations** | | | |
| Administration | API and CLI · ➖ web UI | ✅ web UI | ✅ web UI |
| Replicas behind a load balancer | 🗓️ v0.4 | ✅ | 💼 clustering |
| Ready for production | not yet (v0.1) | ✅ | ✅ |

Compared from the public documentation in November 2026: LiteLLM [enterprise
features](https://docs.litellm.ai/docs/enterprise) and [multi-tenancy](https://docs.litellm.ai/docs/proxy/multi_tenant_architecture);
Bifrost [repository](https://github.com/maximhq/bifrost), [governance](https://docs.getbifrost.ai/features/governance),
[enterprise governance](https://docs.getbifrost.ai/enterprise/advanced-governance) and
[secret management](https://docs.getbifrost.ai/deployment-guides/helm/secret-management). If a cell is
wrong or out of date, please open an issue. What Pandokos will not become is in
[What it is not](#what-it-is-not).

## Quick start

From nothing to a first OpenAI call through Pandokos in three commands, on your machine. You need
Docker with Compose v2, `openssl`, `curl` and an OpenAI API key. This is a local trial, **not a
production setup**: one instance, PostgreSQL in a container, a self-signed certificate, ports
published on `127.0.0.1` only.

https://github.com/user-attachments/assets/eb59d47f-a988-45dd-bb48-637374107999

**1. Get the deployment files.**

```bash
git clone --depth 1 https://github.com/pandokos/pandokos.git && cd pandokos/deploy/compose
```

**2. Run the guided setup.** It starts Pandokos and PostgreSQL with the published image
`ghcr.io/pandokos/pandokos:0.1`, creates a tenant `demo`, stores your OpenAI key in the tenant's vault
and issues a virtual key into `quickstart.key`. It explains each step and waits for Enter before it
(`--yes` does not wait). The OpenAI key is taken from `OPENAI_API_KEY` or asked for without echo; it
reaches Pandokos through standard input, is never written to disk and can never be read back.

```bash
./quickstart.sh
```

**3. Call it.** The request is the one you would send to OpenAI: only the address and the key change.

```bash
curl -sS --cacert secrets/tls_cert https://localhost:8080/openai/v1/chat/completions \
  -H "Authorization: Bearer $(cat quickstart.key)" -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Say hello from Pandokos."}]}'
```

`--cacert` makes curl trust the self-signed certificate. The official OpenAI SDKs work unchanged: set
`base_url` to `https://localhost:8080/openai/v1` and the API key to the content of `quickstart.key`.
Every call leaves a usage record with tokens and estimated cost, without content; the script prints
the command that lists them. Running the script again skips what is done; `docker compose down -v`
removes everything. Each step by hand, streaming, the SDKs in Python and TypeScript, Anthropic and the
details of each choice are in the [Docker Compose reference](docs/operations/compose.md).

## Status

> **v0.1.0 (Core BYOK) is complete and tagged from this commit onwards ([release page](docs/releases/v0.1.0.md)).** Unmodified OpenAI and Anthropic SDKs call models through Pandokos with a virtual key, streaming included, using the calling tenant's own stored credential (BYOK). Each authenticated request leaves one usage record with tokens and estimated cost ([docs/operations/usage.md](docs/operations/usage.md)) and two OpenTelemetry spans ([docs/operations/telemetry.md](docs/operations/telemetry.md)); every outbound connection passes an address-checking egress guard, and OpenAI-compatible providers are added by configuration ([docs/operations/providers.md](docs/operations/providers.md)). Callers authenticate with virtual keys; `pandokos init` issues the first platform key, and the CLI (a client of the administration API) creates tenants, service accounts and keys (first run: [docs/operations/keys.md](docs/operations/keys.md)). Tenants store provider credentials in a vault that nobody can read back ([docs/operations/vault.md](docs/operations/vault.md)), and the platform admin maintains a catalogue of providers, endpoints and models with grants and versioned prices ([docs/operations/catalogue.md](docs/operations/catalogue.md)). A Docker Compose reference takes you from nothing to a first call ([docs/operations/compose.md](docs/operations/compose.md)). Callers can also authenticate with a token from a registered trusted issuer, with the tenant taken from the registration or a claim ([docs/operations/tokens.md](docs/operations/tokens.md)). Requests per minute, tokens per minute and requests in flight are limited per tenant, service account and key, per gateway instance, with a documented bound on the overshoot, and quotas and budgets of requests, tokens and cost per day or month in a configured time zone are counted against the usage ledger; rules, scoped to a provider or model and with soft thresholds, are written through the administration API and the CLI ([docs/operations/limits.md](docs/operations/limits.md)). Sign-in for people and reports come later (v0.2 is not complete): **not for production**. Requirements, architecture and plan are written and open for review; see [Documentation](#documentation) and [Roadmap](#roadmap).

## What it is

Pandokos is a self-hostable proxy that lets an organization expose LLM capabilities to several independent tenants while keeping identities, permissions, credentials, quotas, usage and administrative data strictly separated.

It can run as an internal component of a SaaS platform or as a standalone service offered to multiple organizations.

Tenants use LLM models with **BYOK** (bring your own key): each tenant supplies its own provider credentials and bears the cost directly. Pandokos keeps the keys, injects them, and measures every call.

## Responsibilities

- authentication of callers and authorization of operations (RBAC);
- strict tenant data isolation, enforced down to the database (row-level security);
- custody and controlled use of provider credentials and secrets (vault, envelope encryption);
- forwarding of requests to LLM providers, including streaming;
- rate limiting, quotas and spend control;
- audit log, usage and cost ledger;
- distributed tracing and metrics with OpenTelemetry (OTLP);
- administration of tenants, identities, roles, providers and models;
- usage events for external systems, such as billing.

## What it is not

Pandokos is deliberately not an identity provider, an agent or workflow framework, a prompt manager or store, a semantic cache, a content-moderation or guardrail engine, a router that picks models, falls back or balances load, a translator between provider APIs, a complete billing system, a model hosting platform, or a general-purpose API gateway ([non-goals](docs/requirements/README.md#non-goals)).

## Design principles

1. Multi-tenancy by default, not an optional feature.
2. Defense in depth: authentication, authorization, database-level isolation and secret protection work together.
3. The tenant is never taken on trust from the client; it derives from an authenticated, authorized context.
4. Provider secrets are never returned in clear text after they are written.
5. Observability without indiscriminate content collection: prompts and completions are not stored by default.
6. Separation between control plane and data plane.
7. Operational portability: simple single-host installs and distributed deployments, without requiring Kubernetes.

## Documentation

- [Requirements](docs/requirements/README.md): what Pandokos must guarantee, by release.
- [Architecture](docs/architecture/overview.md), with the [data model](docs/architecture/data-model.md) and the [security model](docs/architecture/security-model.md).
- [Decision records](docs/adr/README.md): why it is built this way.
- [Configuration reference](docs/operations/configuration.md), the [database guide](docs/operations/database.md), [keys, `init` and revocation](docs/operations/keys.md), the [vault and the key-encryption key](docs/operations/vault.md), the [catalogue and prices](docs/operations/catalogue.md), the [gateway guide](docs/operations/gateway.md), [providers (OpenAI-compatible, Anthropic)](docs/operations/providers.md), the [Docker Compose reference](docs/operations/compose.md), [limits, quotas and budgets](docs/operations/limits.md), [usage and the ledger](docs/operations/usage.md), [trusted issuers and tokens](docs/operations/tokens.md), [telemetry](docs/operations/telemetry.md), [TLS](docs/operations/tls.md) and [running the container image](docs/operations/container.md), including how to verify a release.
- Reference: the [administration API](docs/reference/admin-api.md), the [permissions and built-in roles](docs/reference/permissions.md) and the [data flows](docs/reference/data-flows.md): what each destination receives.
- [History](docs/history/README.md): the record of each completed epic, with the designs of its sprints, most recently [limits and budgets](docs/history/epic-05-limits-and-budgets/README.md).
- [Planning](planning/roadmap.md): epics and sprints.

## Roadmap

| Release | Theme |
|---|---|
| v0.1 | Core BYOK: tenants, virtual keys, vault, OpenAI and Anthropic passthrough with streaming, usage ledger |
| v0.2 | Governance: rate limits and budgets, sign-in with local accounts or OIDC, roles, reports, tenant deletion |
| v0.3 | Ready to operate: usage events, external key-encryption key, threat model review, restore drill, upgrade policy |
| v0.4 | Scale-out and providers: replicas, Kubernetes, Gemini, Azure OpenAI |
| v1.0 | Hardening: independent security assessment, measured performance, dedicated cells, mutual TLS |

Details are in the [roadmap](planning/roadmap.md).

## The name

*Pandokos* is the Greek word for an inn that receives everyone. The same word gave the Arabic *funduq* and the Italian *fondaco*: a trading house where merchants from different places were lodged and their goods kept safe under the authority of the host. That is the idea behind the project: many tenants under one roof, each with its own space and its own keys.

## Contributing and security

- Contribution guidelines: [CONTRIBUTING.md](CONTRIBUTING.md)
- Reporting a vulnerability: [SECURITY.md](SECURITY.md)

## License

Licensed under the [Apache License 2.0](LICENSE).
