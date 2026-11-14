# 0040. Traces: SDK, span model, sampling and propagation

- Status: proposed
- Date: 2026-11-14

Refines [ADR 0015](0015-observability.md), [ADR 0018](0018-supply-chain.md) and
[ADR 0038](0038-egress-guard-upstream-tls-and-proxy.md).

## Context

ADR 0015 chooses the OpenTelemetry SDK, a server span and a client span joined to the caller's
trace, and no content. Building it needs a version that fits the Go floor, a rule for who can
cause spans, a sampling rule, and a way to reach the collector that obeys the outbound rules.

## Decision

1. The OpenTelemetry Go SDK and the OTLP/HTTP trace exporter are direct dependencies, at
   release v1.47.0 (the design had v1.46.0, the last line that keeps the Go floor of 1.25). The
   exporter brings `golang.org/x/net`, whose fixed release needs Go 1.26 and Go 1.25 is out of
   support, so the Go floor is raised to 1.26.0 ([ADR 0025](0025-go-toolchain.md)). Only
   `internal/telemetry` imports them; the gateway uses the API packages. OTLP over gRPC is not
   offered.
2. Every setting is passed explicitly from Pandokos's configuration; `OTEL_*` variables are
   not configuration. The SDK still adds `OTEL_RESOURCE_ATTRIBUTES` to the resource; this is
   documented and warned about.
3. The exporter reaches the collector through the egress guard's private transport: `http` or
   `https`, verified certificates, no proxy, no redirect, forbidden addresses refused.
4. Spans are created only for admitted requests. The server span is created after
   authentication with the request's start time; a valid `traceparent` of the caller is its
   parent. The provider span is its child and covers the call until the response body ends.
5. Sampling is parent-based over a configured ratio. Export is asynchronous through a bounded
   queue that drops.
6. Attributes are a fixed list under `http.*` and `pandokos.*`; token counts follow with the
   `gen_ai.*` names in the metrics sprint. No attribute holds content, a secret, a URL, caller
   metadata or an unresolved model name.
7. Trace context is sent to the provider only when the operator enables it, and it is
   Pandokos's own span context.
8. Without a collector endpoint nothing is exported; trace IDs still exist for the ledger and
   the log.

## Consequences

Twenty modules enter the dependency graph, gRPC among them through the HTTP exporter. An
authenticated caller can force sampling of its own requests. Pandokos now needs Go 1.26 to build,
which the release toolchain already exceeds.

## Alternatives considered

A hand-written OTLP/JSON exporter (no dependency, and a second telemetry stack when metrics
arrive); staying on v1.46.0 and the old floor (it keeps a vulnerable `golang.org/x/net` in the
graph); spans for every request (anonymous callers would drive
telemetry); ignoring the caller's sampled flag (breaks the caller's traces); the SDK's
environment configuration (settings outside ADR 0019).
