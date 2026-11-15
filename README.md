# Pandokos

Open-source multi-tenant LLM proxy: governed access to AI providers with BYOK, secrets vault, quotas, audit and OpenTelemetry.

> **Status: gateway complete, v0.1 not yet released.** Unmodified OpenAI and Anthropic SDKs call models through Pandokos with a virtual key, streaming included, using the calling tenant's own stored credential (BYOK). Each authenticated request leaves one usage record with tokens and estimated cost ([docs/operations/usage.md](docs/operations/usage.md)) and two OpenTelemetry spans ([docs/operations/telemetry.md](docs/operations/telemetry.md)); every outbound connection passes an address-checking egress guard, and OpenAI-compatible providers are added by configuration ([docs/operations/providers.md](docs/operations/providers.md)). Callers authenticate with virtual keys; `pandokos init` issues the first platform key, and the CLI (a client of the administration API) creates tenants, service accounts and keys (first run: [docs/operations/keys.md](docs/operations/keys.md)). Tenants store provider credentials in a vault that nobody can read back ([docs/operations/vault.md](docs/operations/vault.md)), and the platform admin maintains a catalogue of providers, endpoints and models with grants and versioned prices ([docs/operations/catalogue.md](docs/operations/catalogue.md)). A Docker Compose reference takes you from nothing to a first call ([docs/operations/compose.md](docs/operations/compose.md)). Trusted token issuers (sprint 4.1) complete v0.1; rate limits and budgets (epic 5) and reports come later: **not for production**. Requirements, architecture and plan are written and open for review; see [Documentation](#documentation) and [Roadmap](#roadmap).

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

Pandokos is deliberately not an identity provider, an agent or workflow framework, a prompt manager or store, a content-moderation system, a complete billing system, or a model hosting platform.

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
- [Configuration reference](docs/operations/configuration.md), the [database guide](docs/operations/database.md), [keys, `init` and revocation](docs/operations/keys.md), the [vault and the key-encryption key](docs/operations/vault.md), the [catalogue and prices](docs/operations/catalogue.md), the [gateway guide](docs/operations/gateway.md), [providers (OpenAI-compatible, Anthropic)](docs/operations/providers.md), the [Docker Compose reference](docs/operations/compose.md), [usage and the ledger](docs/operations/usage.md), [telemetry](docs/operations/telemetry.md), [TLS](docs/operations/tls.md) and [running the container image](docs/operations/container.md), including how to verify a release.
- Reference: the [administration API](docs/reference/admin-api.md), the [permissions and built-in roles](docs/reference/permissions.md) and the [data flows](docs/reference/data-flows.md): what each destination receives.
- [History](docs/history/README.md): the record of each completed epic, with the designs of its sprints.
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
