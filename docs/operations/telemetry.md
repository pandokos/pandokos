# Telemetry guide

Pandokos exports traces over OTLP/HTTP to a collector you run. Each authenticated request produces
two spans, joined to the caller's trace when the caller sent one. A span carries identifiers and
measurements, never content. This page says how to configure the export, what the spans hold, who
decides what is sampled, and what happens when the collector is away. It applies from sprint 3.2;
decision: [ADR 0040](../adr/0040-traces.md) (refining [ADR 0015](../adr/0015-observability.md)).
Metrics and token counts on spans come with a later release.

## 1. The keys

| Key | Purpose |
|---|---|
| `telemetry.otlp.endpoint` | base URL of the collector, `http` or `https`; `/v1/traces` is appended. Empty (the default) means nothing is exported |
| `telemetry.otlp.protocol` | `http/protobuf`, the only value |
| `telemetry.otlp.headers_file` | a file of `Name: value` lines sent with every export (an API token, a tenant header). The values are secrets: they are never logged and never shown |
| `telemetry.otlp.timeout` | time allowed for one export (default 10 s) |
| `telemetry.traces.sample_ratio` | share of new traces that is sampled, 0 to 1 (default 1) |
| `telemetry.traces.propagate` | send the trace context to the provider (default false) |
| `telemetry.resource.attributes` | extra resource attributes, `key=value` pairs separated by commas |

Each has an environment variable (`PANDOKOS_TELEMETRY_...`) as in the
[configuration reference](configuration.md). They are read by `serve` only.

Without an endpoint no exporter exists and nothing leaves the process, but trace IDs are still made:
the usage record and the request log line carry a `trace_id`, and propagation works.

The resource of every span has `service.name=pandokos`, `service.version`, a random
`service.instance.id` per process, and then `telemetry.resource.attributes`, which may replace
`service.name`. The instrumentation scope is `github.com/pandokos/pandokos`.

## 2. The collector connection

The exporter is an outbound connection like any other, made by the same component as provider calls
([gateway guide](gateway.md), section 6), as a private endpoint:

- `http` or `https`; a collector inside your network is normal, so private addresses are allowed;
  instance-metadata and other forbidden addresses are refused;
- certificates are verified against the system roots plus `upstream.tls.ca_file`, which is how a
  collector with a private CA is trusted; there is no way to turn verification off;
- **no proxy**: `upstream.proxy.url` and the `*_PROXY` variables are not used for the collector;
- **no redirect** is followed;
- traces are sent uncompressed, without retries inside the exporter.

## 3. The spans

Spans exist only for **admitted** requests, after authentication (a request that did not
authenticate produces no span and no header is read from it).

| Span | Name and kind | Parent | Runs from, to |
|---|---|---|---|
| server | `POST /{provider}/v1/chat/completions` (the route label), server | the caller's trace when its `traceparent` is valid, else a new trace | the start of the request to its end |
| provider | `<operation> <model>` (the catalogue model name), client | the server span | the call to the provider, to the end of the response body |

Attributes, the complete list:

| Span | Attributes |
|---|---|
| server | `http.request.method`, `http.route`, `http.response.status_code`, `pandokos.request.id`, `pandokos.record.id`, `pandokos.tenant.id`, `pandokos.principal.id`, `pandokos.key.id`, `pandokos.provider`, `pandokos.operation`, `pandokos.outcome`, `pandokos.error.code` |
| provider | `pandokos.provider`, `pandokos.endpoint`, `pandokos.model`, `pandokos.operation`, `pandokos.stream`, `pandokos.credential.id`, `pandokos.funding`, `http.response.status_code`, `pandokos.upstream.error`, `pandokos.ttfb_ms`, `pandokos.usage.source`, `pandokos.cost.status`, `pandokos.cost.amount`, `pandokos.cost.currency` |

`pandokos.error.code` and `pandokos.upstream.error` are set only when there is an error. The span
status is `Error` for the outcomes `upstream_error`, `timeout` and `internal_error` and unset
otherwise; there is no status description, no event and no link. `pandokos.record.id` and
`pandokos.request.id` join a span to its row in the [usage ledger](usage.md) and to the request log
line.

**Never on a span:** a request or response body, any header value, a secret, a URL or host, the
caller's metadata, a model string that did not resolve, token counts (they wait for the `gen_ai.*`
names of the metrics release). No setting enables any of it.

## 4. Sampling

Sampling is parent-based over the configured ratio. A request that arrives with a valid
`traceparent` follows the caller's sampled flag: **a caller that sets the flag forces its own
requests to be sampled** (and one that clears it stops them being), whatever `sample_ratio` says. A
request without context is sampled at `sample_ratio`. This is deliberate, since ignoring the flag
would break the caller's own traces; the cost is that an authenticated caller can raise the volume
of traces of its own requests. Use the collector to limit that if it matters. With no endpoint the
provider samples nothing.

## 5. Propagation

Off by default. With `telemetry.traces.propagate: true` the request to the provider carries
`traceparent` and `tracestate` made from Pandokos's own provider span, so a provider that
understands them can join its work to the trace. The caller's own `traceparent`, `tracestate` and
`baggage` headers are never copied to the provider, whatever the setting. Leave it off for a
provider outside your control unless you accept that the provider sees a trace ID.

## 6. The environment rule and its exception

Pandokos passes every setting explicitly; **`OTEL_*` environment variables are not configuration**:
`OTEL_EXPORTER_OTLP_ENDPOINT`, headers, sampler variables and `OTEL_SDK_DISABLED` have no effect. One
cannot be switched off inside the SDK: attributes in `OTEL_RESOURCE_ATTRIBUTES` are added to the
resource (they never replace those Pandokos sets). `serve` therefore logs a configuration warning,
naming the variable and never its value, when `OTEL_RESOURCE_ATTRIBUTES` is set, and when
`OTEL_EXPORTER_OTLP_ENDPOINT` or `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` is set while
`telemetry.otlp.endpoint` is empty (the variable is ignored).

## 7. When the collector is away

Ending a span never waits for the network. Spans go to a queue of 2048 spans, exported in batches of
512 or every 2 s. When the collector is slow or down, the queue fills and **spans are dropped**;
requests are not delayed and the ledger is not affected. The SDK's own diagnostics go to the
product log: `trace export failing` (warn, at most once a minute, with a count and the class of the
error, never a header value). At shutdown the traces are flushed under the same deadline as the
ledger (`ledger.shutdown_flush_timeout`); spans that cannot be sent are lost.

## 8. Content

No content is ever attached to a span, a log line of the telemetry package or an export, and no
setting enables it ([ADR 0015](../adr/0015-observability.md)). The data a destination receives is
summarised in [data flows](../reference/data-flows.md).
