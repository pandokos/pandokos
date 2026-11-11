# Pandokos

Open-source multi-tenant LLM proxy: governed access to AI providers with BYOK, secrets vault, quotas, audit and OpenTelemetry.

> **Status: early design phase.** There is no runnable release yet. The scope and requirements are being defined; see [Roadmap](#roadmap).

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

## Roadmap

1. Secure foundations: tenant model, row-level security, authentication, RBAC, audit.
2. Proxy and vault: first provider, streaming, encrypted vault, BYOK.
3. Governance and metering: usage ledger, pricing, quotas, OpenTelemetry.
4. Platform-funded credentials and billing events.
5. Advanced deployments: pool, silo and bridge models, Kubernetes.
6. Hardening and open-source maturity: threat model, SBOM, signed releases.

## The name

*Pandokos* is the Greek word for an inn that receives everyone. The same word gave the Arabic *funduq* and the Italian *fondaco*: a trading house where merchants from different places were lodged and their goods kept safe under the authority of the host. That is the idea behind the project: many tenants under one roof, each with its own space and its own keys.

## Contributing and security

- Contribution guidelines: [CONTRIBUTING.md](CONTRIBUTING.md)
- Reporting a vulnerability: [SECURITY.md](SECURITY.md)

## License

Licensed under the [Apache License 2.0](LICENSE).
