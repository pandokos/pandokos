# Pandokos

Open-source multi-tenant LLM proxy: governed access to AI providers with BYOK, secrets vault, quotas, audit and OpenTelemetry.

> **Status: walking skeleton on a tested database.** A container image forwards one OpenAI endpoint (chat completions, streaming included) with a provider key read from a file and a static access token, and logs the usage it reads. It needs PostgreSQL: the schema is created by `pandokos migrate`, and a database with tested tenant isolation (forced row-level security, three unprivileged database roles, checked against PostgreSQL directly and through a pooler) exists, but nothing is proxied through it yet and no tenant can be created through a product interface: **not for production**. Requirements, architecture and plan are written and open for review; see [Documentation](#documentation) and [Roadmap](#roadmap).

## What it is

Pandokos is a self-hostable proxy that lets an organization expose LLM capabilities to several independent tenants while keeping identities, permissions, credentials, quotas, usage and administrative data strictly separated.

It can run as an internal component of a SaaS platform or as a standalone service offered to multiple organizations.

Tenants can use LLM models through either:

- **BYOK** (bring your own key): the tenant supplies its own provider credentials and bears the cost directly;
- **platform-funded credentials**: the platform operator pays the provider and the tenant is metered.

## Responsibilities

- authentication of callers and authorization of operations (RBAC);
- strict tenant data isolation, enforced down to the database (row-level security);
- custody and controlled use of provider credentials and secrets (vault, envelope encryption);
- forwarding of requests to LLM providers, including streaming;
- rate limiting, quotas and spend control;
- audit log, usage and cost ledger;
- distributed tracing and metrics with OpenTelemetry (OTLP);
- administration of tenants, identities, roles, providers and models;
- consumption data prepared for external billing systems.

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
- [Configuration reference](docs/operations/configuration.md), the [database guide](docs/operations/database.md) and [running the container image](docs/operations/container.md), including how to verify a release.
- [Planning](planning/roadmap.md): epics and sprints.

## Roadmap

| Release | Theme |
|---|---|
| v0.1 | Core BYOK: tenants, virtual keys, vault, OpenAI and Anthropic passthrough with streaming, usage ledger |
| v0.2 | Governance and web console: sign-in with local accounts or OIDC, roles, rate limits and budgets, reports, tenant deletion |
| v0.3 | Platform-funded credentials and billing events |
| v0.4 | Scale-out: replicas, Kubernetes, dedicated cells, more providers |
| v1.0 | Hardening: security review, measured performance, tested recovery |

Details are in the [roadmap](planning/roadmap.md).

## The name

*Pandokos* is the Greek word for an inn that receives everyone. The same word gave the Arabic *funduq* and the Italian *fondaco*: a trading house where merchants from different places were lodged and their goods kept safe under the authority of the host. That is the idea behind the project: many tenants under one roof, each with its own space and its own keys.

## Contributing and security

- Contribution guidelines: [CONTRIBUTING.md](CONTRIBUTING.md)
- Reporting a vulnerability: [SECURITY.md](SECURITY.md)

## License

Licensed under the [Apache License 2.0](LICENSE).
