# 0062. Metrics: instruments, attributes, cardinality and the scrape endpoint

- Status: accepted
- Date: 2026-11-22

Refines [ADR 0015](0015-observability.md), [ADR 0040](0040-traces.md),
[ADR 0054](0054-usage-data-is-the-tenants.md) and
[ADR 0055](0055-operational-telemetry-holds-no-tenant-data.md). Requirements OBS-01, OBS-03, OBS-06,
OBS-07.

## Context

ADR 0015 asks for metrics under the GenAI conventions, pinned, with a Prometheus endpoint on the
admin port and a tenant dimension as an opt-in. ADR 0054 and 0055 then decided that operational
telemetry holds no tenant data and that cost and token figures exist per installation only. The
conventions are in Development and, since 2026, outside the Go semantic-convention packages.

## Decision

1. Metrics use the OpenTelemetry Go SDK with the traces' resource. They are exported over
   OTLP/HTTP to the traces' collector (`/v1/metrics`, the same transport, headers and timeout,
   cumulative, no retry) and, when enabled, served at `GET /metrics` on the administration
   listener. With neither, no meter provider exists.
2. The GenAI conventions are pinned at **1.41.0** by the import path
   `go.opentelemetry.io/otel/semconv/v1.41.0` and the meter's schema URL. The GenAI metrics carry
   the Required attributes and the Conditionally Required model (the catalogue name) and
   `error.type`; no response model, server address or port. Token usage holds what the provider
   reported; estimates have their own counter. The HTTP server duration uses the GenAI boundaries.
3. The instruments are a closed catalogue, published in the metrics reference. Every attribute value
   is from a closed list or the platform catalogue; each instrument is capped at 2000 attribute sets;
   exemplars are off.
4. **No metric names a tenant, caller, key, request or trace, and no setting adds one.** The
   opt-in tenant dimension of ADR 0015 is withdrawn; a dimension without the identity in clear is
   issue #52.
5. **One exception**: the provider's 429 answers are counted per provider credential ID (the
   attribute `pandokos.credential.id`) and provider, on that counter alone (issue #32), so that an
   operator sees which credential is saturated. The ID says nothing of usage, cost or caller and is
   resolved only through the audited `tenant_credential:read` on a named tenant.
6. Cost is the recorded cost of each request, with its currency, summed per model and currency and
   never across currencies; there is no price-source dimension.
7. The scrape endpoint requires a bearer credential of a platform principal holding the platform
   permission `metrics:read` (held by `platform-admin` and `platform-auditor`). Pandokos writes the
   Prometheus text format itself; names follow Prometheus's own OTLP translation with suffixes, so
   one dashboard serves both paths.
8. The gateway reports one observation per request through a port whose type has no tenant field;
   other components expose snapshots read at collection. Recording never waits for the network.

## Consequences

- Operators get latency, tokens and cost by model without seeing any tenant; where a tenant alone
  uses a model or a currency, the aggregate is that tenant's, which is stated.
- A credential ID appears in the operator's time series when its provider answers 429.
- A change of convention version is a change of import path and a release-notes line; after
  1.41.0 the source of names must be found again.
- Two modules enter the graph; no Prometheus client.

## Alternatives considered

**Tenant labels as an opt-in (ADR 0015).** Contradicts ADR 0055 §8. **The OpenTelemetry Prometheus
exporter.** Seven modules, pre-1.0, for a format of a few hundred lines. **An unauthenticated scrape
endpoint.** The admin listener carries no anonymous data path today. **A keyed pseudonym for the
credential.** Needs the key management of #52. **Estimates inside `gen_ai.client.token.usage`.**
The conventions reserve it for counts obtained from the system.

## History

- 2026-11-22: proposed with sprint 6.1.
- 2026-11-23: sprint 6.2: five instruments, M42 to M46, with no tenant attribute
- 2026-11-23: sprint 6.3: five instruments, M47 to M51 (51 in all), with no tenant attribute: deletion steps and open deletions, re-wraps, key versions by state and pending erasures; two new values of `pandokos.retention.kind` and one of `pandokos.export.kind`.
- 2026-11-23: accepted by the maintainer after review.
- 2026-11-23: sprint 6.4 adds seven instruments, `pandokos.events.*` (M52 to M58), and the values `outbox_event`, `limit_event`, `event` and `platform_event` of two attributes, with no tenant attribute ([metrics reference](../reference/metrics.md)).
- 2026-11-24: sprint 12.1 adds four instruments, `pandokos.vault.provider.*` (M59 to M62), with no tenant, address, name or version as an attribute ([ADR 0073](0073-the-transit-key-provider.md), [metrics reference](../reference/metrics.md)).
