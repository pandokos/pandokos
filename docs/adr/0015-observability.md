# 0015. Observability: OpenTelemetry, pinned GenAI conventions, no content

- Status: accepted
- Date: 2026-11-11

## Context

Operators need to see latency, errors, tokens and cost; callers need the provider call to appear
inside their own traces (OBS-01 to OBS-08). The OpenTelemetry GenAI semantic conventions are the
shared vocabulary, but they are still in *Development* status: in 2026 they moved to their own
repository, and attribute names can still change.

## Decision

- **Instrumentation** uses the OpenTelemetry Go SDK and exports over OTLP (gRPC or HTTP). The
  exporter is asynchronous with a bounded queue; when the collector is slow or absent, telemetry
  is dropped and inference is unaffected. With no endpoint configured, no exporter runs.
- **Traces.** A server span for the inbound request and one client span for the provider call,
  joined to the caller's trace through W3C Trace Context. Trace context is propagated to the
  provider only if the operator enables it.
- **GenAI attributes.** Pandokos emits the `gen_ai.*` attributes for operation, provider, request
  and response model, token usage and finish information. The version of the conventions it
  follows is pinned, documented and reported in the instrumentation scope. Because the conventions
  are not stable, a change of emitted names is announced in release notes and, where practical,
  both old and new names are emitted for one minor release.
- **Pandokos attributes** use the `pandokos.*` namespace: tenant ID, principal ID, funding mode,
  credential ID, estimated cost, limit outcome. Tenant and principal appear on spans, not on
  metrics.
- **Metrics.** Request count, error count, time to first byte, total duration, tokens by category,
  estimated cost, limit refusals; dimensions are provider, model, operation and outcome. A tenant
  dimension is an opt-in intended for installations with few tenants. A Prometheus scrape endpoint
  is available on the admin port for those who do not run a collector.
- **Logs.** Structured JSON on standard output through `log/slog`, with a handler that redacts
  known sensitive keys. Bodies and credential headers are never passed to a logger; there is no
  debug level that changes this.
- **No content.** No prompt, completion or tool payload is attached to any span, metric or log,
  and no setting enables it.

## Consequences

- Callers that own the "why" of a model call keep it in their own spans; Pandokos contributes the
  "how much" measured at the point where the call leaves.
- Following a moving convention has a maintenance cost and can break dashboards between releases.
  Pinning and announcing is the mitigation.
- Debugging a bad model answer cannot be done from Pandokos telemetry. That is intended.

## Alternatives considered

**Custom attribute names only.** Stable, and incompatible with every GenAI-aware backend.

**Optional content capture**, as the conventions allow. It would turn the telemetry pipeline into
a conversation store outside tenant isolation.

**Prometheus only.** No traces, so no correlation with the caller.
