# Pandokos

Open-source multi-tenant LLM proxy: governed access to AI providers with BYOK, secrets vault, quotas, audit and OpenTelemetry.

## Quick start

From nothing to a first OpenAI call through Pandokos in three steps, on your machine. You need Docker
with Compose v2, `openssl`, Python 3 and an OpenAI API key in `OPENAI_API_KEY`. Run every step in the
same shell. This is a local trial, **not a production setup**: one instance, PostgreSQL in a container,
a self-signed certificate, ports published on `127.0.0.1` only.

**1. Start Pandokos and PostgreSQL** with the published image `ghcr.io/pandokos/pandokos:0.1`:

```bash
git clone --branch v0.1.0 https://github.com/pandokos/pandokos.git && cd pandokos/deploy/compose
./setup.sh && docker compose up -d --wait
```

`setup.sh` generates the database passwords, the vault's encryption key and a TLS certificate in
`./secrets`; `--wait` returns once the database is migrated and Pandokos is healthy.

**2. Create a tenant and a virtual key.** The OpenAI key goes into the tenant's vault through standard
input and can never be read back; the application only ever sees a Pandokos key.

```bash
docker compose exec -T pandokos /pandokos init --key-out /home/nonroot/bootstrap.key
docker compose exec -T -e PANDOKOS_CLI_KEY_FILE=/home/nonroot/bootstrap.key pandokos \
  /pandokos tenant create demo --admin-key-out /home/nonroot/demo-admin.key
pdk() { docker compose exec -T -e PANDOKOS_CLI_KEY_FILE=/home/nonroot/demo-admin.key pandokos /pandokos "$@"; }
printf %s "$OPENAI_API_KEY" | pdk credential create --provider openai --secret-file /dev/stdin openai-main
pdk service-account create --role inference app
export PANDOKOS_KEY=$(pdk key create --service-account app | awk '/^key:/ {print $2}')
```

**3. Call it with the official OpenAI SDK.** Only `base_url` and the key change. Save this as
`hello.py`:

```python
import os
from openai import OpenAI

client = OpenAI(
    base_url="https://localhost:8080/openai/v1",  # Pandokos instead of api.openai.com
    api_key=os.environ["PANDOKOS_KEY"],           # the virtual key, not the OpenAI key
)
reply = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "Say hello from Pandokos."}],
)
print(reply.choices[0].message.content)
```

```bash
python3 -m venv .venv && .venv/bin/pip install -q openai
SSL_CERT_FILE=$PWD/secrets/tls_cert .venv/bin/python hello.py
```

`SSL_CERT_FILE` makes the SDK trust the self-signed certificate. Every call leaves a usage record with
tokens and estimated cost, without content: `pdk usage list`. `docker compose down -v` removes
everything. Streaming, the Anthropic SDK, curl and the details of each choice are in the
[Docker Compose reference](docs/operations/compose.md).

## Status

> **v0.1.0 (Core BYOK) is complete and tagged from this commit onwards ([release page](docs/releases/v0.1.0.md)).** Unmodified OpenAI and Anthropic SDKs call models through Pandokos with a virtual key, streaming included, using the calling tenant's own stored credential (BYOK). Each authenticated request leaves one usage record with tokens and estimated cost ([docs/operations/usage.md](docs/operations/usage.md)) and two OpenTelemetry spans ([docs/operations/telemetry.md](docs/operations/telemetry.md)); every outbound connection passes an address-checking egress guard, and OpenAI-compatible providers are added by configuration ([docs/operations/providers.md](docs/operations/providers.md)). Callers authenticate with virtual keys; `pandokos init` issues the first platform key, and the CLI (a client of the administration API) creates tenants, service accounts and keys (first run: [docs/operations/keys.md](docs/operations/keys.md)). Tenants store provider credentials in a vault that nobody can read back ([docs/operations/vault.md](docs/operations/vault.md)), and the platform admin maintains a catalogue of providers, endpoints and models with grants and versioned prices ([docs/operations/catalogue.md](docs/operations/catalogue.md)). A Docker Compose reference takes you from nothing to a first call ([docs/operations/compose.md](docs/operations/compose.md)). Callers can also authenticate with a token from a registered trusted issuer, with the tenant taken from the registration or a claim ([docs/operations/tokens.md](docs/operations/tokens.md)). Rate limits and budgets (epic 5), sign-in for people and reports come later: **not for production**. Requirements, architecture and plan are written and open for review; see [Documentation](#documentation) and [Roadmap](#roadmap).

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
