# Telemetry guide

Pandokos exports traces and metrics over OTLP/HTTP to a collector you run, and can serve the metrics
to Prometheus itself. Each authenticated request produces two spans, joined to the caller's trace when
the caller sent one, and one observation of the metrics. A span carries only the attributes the
OpenTelemetry semantic conventions make Required; a metric carries only values of closed lists and
the names of your catalogue; neither holds content or anything of the tenant. This page says how to
configure the export, what the spans and the metrics hold, who decides what is sampled, what happens
when the collector is away, how to scrape the metrics and how to look at them. Decisions:
[ADR 0040](../adr/0040-traces.md) and [ADR 0062](../adr/0062-metrics-instruments-attributes-cardinality-and-scrape.md)
(refining [ADR 0015](../adr/0015-observability.md)) and
[ADR 0055](../adr/0055-operational-telemetry-holds-no-tenant-data.md). The log is described in the
[log reference](../reference/logs.md).

## 1. The keys

| Key | Purpose |
|---|---|
| `telemetry.otlp.endpoint` | base URL of the collector, `http` or `https`; `/v1/traces` and `/v1/metrics` are appended. Empty (the default) means nothing is exported |
| `telemetry.otlp.protocol` | `http/protobuf`, the only value |
| `telemetry.otlp.headers_file` | a file of `Name: value` lines sent with every export (an API token, a tenant header). The values are secrets: they are never logged and never shown |
| `telemetry.otlp.timeout` | time allowed for one export, of traces or of metrics (default 10 s) |
| `telemetry.traces.sample_ratio` | share of new traces that is sampled, 0 to 1 (default 1) |
| `telemetry.traces.propagate` | send the trace context to the provider (default false) |
| `telemetry.resource.attributes` | extra resource attributes, `key=value` pairs separated by commas |

The three keys of the metrics are in section 9. Each key has an environment variable
(`PANDOKOS_TELEMETRY_...`) as in the [configuration reference](configuration.md). They are read by
`serve` only.

Logs, spans and metrics hold nothing of a tenant: no tenant, caller, key, model, credential, size, usage,
cost or limit rule ([ADR 0055](../adr/0055-operational-telemetry-holds-no-tenant-data.md)). There is
no setting that brings them back. The key `telemetry.tenant_usage_details` of earlier development
builds no longer exists; a file that has it is refused as an unknown key.

Without an endpoint no exporter exists and nothing leaves the process, but trace IDs are still made:
the usage record and the request log line carry a `trace_id`, and propagation works.

The resource of every span and metric has `service.name=pandokos`, `service.version`, a random
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
- traces and metrics are sent uncompressed, without retries inside the exporter.

## 3. The spans

Spans exist only for **admitted** requests, after authentication (a request that did not
authenticate produces no span and no header is read from it).

| Span | Name and kind | Parent | Runs from, to |
|---|---|---|---|
| server | `POST /{provider}/v1/chat/completions` (the route label), server | the caller's trace when its `traceparent` is valid, else a new trace | the start of the request to its end |
| provider | `<operation> <model>` (the catalogue model name), client | the server span | the call to the provider, to the end of the response body |

Attributes, the complete list: only those the OpenTelemetry semantic conventions mark **Required**,
nothing else. The server span follows the HTTP server span, the provider span the GenAI inference span
(`gen_ai.client.inference`).

| Span | Attributes |
|---|---|
| server | `http.request.method`, `url.path` (`/<provider>/<native path>`, never the query string), `url.scheme` |
| provider | `gen_ai.operation.name` (`chat` for chat completions, responses and messages, `embeddings`, or `messages.count_tokens`), `gen_ai.provider.name` (the provider segment of the route) |

The span status is `Error` for the outcomes `upstream_error`, `timeout` and `internal_error` and unset
otherwise; there is no status description, no event and no link. The span names carry no model: the
server span is named after the method, the provider span after `gen_ai.operation.name`. A span is
joined to the request log line and to the tenant's usage record by its trace ID (`trace_id`).

**Never on a span:** the tenant, the caller, the key, the token registration or the subject; the model,
the credential, the endpoint; token counts, costs, usage sources; a request or response body, any
header value, a secret, the caller's metadata. Attributes the conventions mark Conditionally
Required, Recommended or Opt-In are not emitted. No setting enables any of it. Whether and how the
platform gets a tenant dimension in its telemetry is issue #52.

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
error, never a header value). Failures that belong to no single signal are reported by the SDK's own
line, `telemetry error`, under the same rule. Earlier builds wrote every SDK error as
`trace export failing`; now that line means a failed export of spans, and `metric export failing` a
failed export of metrics. At shutdown the traces are flushed under the same deadline as the
ledger (`ledger.shutdown_flush_timeout`); spans that cannot be sent are lost.

## 8. Content

No content is ever attached to a span, a log line of the telemetry package or an export, and no
setting enables it ([ADR 0015](../adr/0015-observability.md)). The data a destination receives is
summarised in [data flows](../reference/data-flows.md).

## 9. Metrics

Pandokos measures every request of the inference listener and the parts that keep it running, with
46 instruments listed in the [metrics reference](../reference/metrics.md) (`docs/reference/metrics.md`): name, kind, unit,
attributes, Prometheus name and when each is recorded. The latency, token and operation metrics follow
the OpenTelemetry GenAI semantic conventions, pinned at **1.41.0** (the conventions are in
Development, and the Go package that carries them ends at that version; a change is announced in the
release notes). The metrics of the `pandokos.` namespace are the product's own.

| Key | Default | Purpose |
|---|---|---|
| `telemetry.metrics.otlp` | `true` | export the metrics over OTLP to `telemetry.otlp.endpoint`, when that is set |
| `telemetry.metrics.export_interval` | `60s` | time between two exports, 5 s to 10 min and at least `telemetry.otlp.timeout`; the exporter waits for the collector no longer than that timeout |
| `telemetry.metrics.prometheus` | `false` | serve `GET /metrics` on the administration listener (section 10) |

With a transit key provider four more instruments are read at collection, M59 to M62 (requests to the key
service by operation and outcome, the duration of a request, whether requests are sent, and the token's
remaining lifetime); none names a tenant, an address, a namespace, a key or a version, and an installation
with a key file does not report them ([external key guide](external-key.md#9-when-the-service-is-slow-or-away)).

The metrics use the resource of the traces (section 1) and the collector connection of section 2:
the same endpoint, headers, certificates and timeout, to `/v1/metrics`, cumulative, without retry. With
neither the OTLP export nor the scrape endpoint on, no measurement is made. A request pays for one
observation made in memory after it ended; it never waits for the network, and no lock of the
gateway is held while it is recorded. The counters of the metrics are cumulative, so a collector that
was away for a while shows a gap but not a loss of counts.

When the collector is away the export fails after `telemetry.otlp.timeout`; the failure is logged as
`metric export failing` (warn, at most once a minute, with a count and a class, never the error
text) and counted by `pandokos.telemetry.export_failures`. Requests are not delayed and nothing is
queued beyond the next export. At shutdown the metrics are flushed under the deadline of the ledger.

## 10. The scrape endpoint

With `telemetry.metrics.prometheus: true`, `GET /metrics` on the administration listener answers in
the Prometheus text format (version 0.0.4), with the same names as the OTLP path, so that one dashboard
serves both. It is off by default, and then the path gets the administration API's 404. It is not
served on the inference listener, and it is not part of the OpenAPI document. `HEAD` is served as
`GET`; other methods get 405.

**It always requires a credential.** The request carries `Authorization: Bearer` with a platform key
(or, when `tokens.admin_listener` is on, a token of a platform-level issuer), and the principal must
hold the platform permission `metrics:read`, which `platform-admin` and `platform-auditor` hold. A
tenant principal of any role and a session cookie are refused (401 `unauthenticated`, 403
`forbidden`; 503 `unavailable` when the credential cannot be verified). Platform keys carry no scopes,
so the scraper gets a key of the narrower role:

```sh
pandokos platform-key create --role platform-auditor --description "prometheus" --key-out ./scraper.key
```

A key without `--role` has `platform-admin`, which is more than a scraper needs. The auditor role is
narrower, not narrow: because platform keys carry no scopes, the scraper key can also read tenants,
users, platform keys, token issuers and support actions through the administration API. Protect it as
an auditor credential (a file the Prometheus user alone can read, no copy elsewhere, revoked and
replaced like any platform key), and keep the administration listener off the networks that do not
need it. A scope on platform keys, or a dedicated scraper role, is a follow-up. Give the file to
Prometheus (mode 0600, readable by the Prometheus user), and its CA if the listener's certificate is
not public:

```yaml
scrape_configs:
  - job_name: pandokos
    scheme: https
    metrics_path: /metrics
    authorization: {type: Bearer, credentials_file: /etc/prometheus/pandokos-scraper.key}
    tls_config: {ca_file: /etc/prometheus/pandokos-ca.pem}
    static_configs:
      - targets: ["pandokos.example.net:8081"]
```

Pandokos writes the format itself, without a Prometheus library: one `# HELP` and `# TYPE` per family,
counters ending in `_total`, histograms as `_bucket`, `_sum` and `_count`, and `target_info` for the
resource. No timestamps and no exemplars. The names are those of the
[metrics reference](../reference/metrics.md); label values are escaped as the format requires. Each
authentication failure of a scrape is counted and logged as on the rest of the administration API.

## 11. What a metric may say

In operator words ([ADR 0055](../adr/0055-operational-telemetry-holds-no-tenant-data.md) and
[ADR 0062](../adr/0062-metrics-instruments-attributes-cardinality-and-scrape.md)):

- **No metric names a tenant, a caller, a key, a request or a trace, and no setting adds one.** The
  opt-in tenant dimension that an earlier plan allowed is withdrawn; how to give the platform a tenant
  dimension without the tenant's identity in clear is issue #52. Nothing in a metric is content, a
  header, a URL, a host or a metadata value.
- Every attribute value is one of a closed list of the [reference](../reference/metrics.md#closed-values)
  or a name of your catalogue: the provider and the model are set only after the catalogue resolved
  them, so a path or a body cannot create a series. The model is the catalogue name, never the name the
  caller sent. Each instrument holds at most 2000 series; beyond that, measurements go to one series
  marked `otel.metric.overflow=true`. The number of series is bounded by your catalogue: provider,
  operation, model and token type multiply.
- **One exception**: the answers of 429 of a provider are counted per provider and provider credential
  ID (`pandokos.provider.rate_limited`), so that you see which credential is saturated. The ID says
  nothing of usage, cost or caller, and leads to a tenant only through the audited permission
  `tenant_credential:read` on a named tenant; the platform cannot search credentials across tenants.
- Cost and tokens are per installation, model and currency, never split by tenant. Cost is the recorded
  cost of each request, summed within a currency and never across currencies; it includes costs at
  tenants' own prices, with no attribute for the price source.
- **A residual you accept:** where one tenant alone uses a model or a currency, the total for that model
  or currency is that tenant's. It is the same exposure as the catalogue you configure; if it matters,
  do not publish dashboards of per-model figures to people who must not know it. A scrape credential
  reads all of it.

## 12. Dashboards

`deploy/compose/observability/` is an overlay for the [Compose reference deployment](compose.md#the-observability-overlay):
an OpenTelemetry Collector, Prometheus and Grafana, with two dashboards provisioned. It is an example
that shows the metrics, not a hardened monitoring stack. Start it from `deploy/compose`:

```sh
./setup.sh
docker compose -f compose.yaml -f observability/compose.yaml up -d --wait
```

Open Grafana at `http://127.0.0.1:3000` (anonymous viewer; the admin password is in
`secrets/grafana_admin`) and Prometheus at `http://127.0.0.1:9090`. Pandokos sends its metrics every
10 s to the collector, which forwards them to Prometheus, and also serves `/metrics`.

- **Pandokos - Inference**: requests per second by outcome and by error code and by limit state;
  latency of the whole request and of the provider call by model (p50, p95, p99), calls that failed,
  time to first chunk; tokens per second by model and the estimated share; cost per hour by model and
  currency and requests of unknown cost; limit refusals by code and metric; provider 429 by credential.
  Variables select the model and the provider.
- **Pandokos - Internals**: the usage ledger and its buffer, egress blocks, authentication by kind and
  result and failure reason, sign-ins, locks, sessions and password hashes, issuer keys, the limiter and
  the reconciler, and the telemetry itself.

Signals worth an alert:

| Metric | Alert when |
|---|---|
| `pandokos_limits_reservations_expired_total` (M31) | above zero: a reservation expired unsettled, a bug to report |
| `pandokos_limits_cut_lag_seconds` (M34) | it keeps growing: the reconciler does not run |
| `pandokos_ledger_records_total{pandokos_result="lost"}` (M12) | above zero: usage records were lost |
| `pandokos_ledger_admission_refused_total` (M13) | above zero: the ledger buffer was full and requests were refused |
| `pandokos_egress_blocked_total` (M16) | it grows: a provider endpoint or issuer resolves to a refused address |
| `pandokos_vault_provider_up` (M61) | it is 0: the key service is being refused after repeated failures; calls that need an unwrap and have nothing cached get `503` (absent without a transit provider) |
| `pandokos_vault_provider_token_ttl_seconds` (M62) | it falls and is not renewed: the token of the key service is about to expire (absent for a token that never expires) |
| `pandokos_telemetry_export_failures_total` (M40) | it grows: the collector is not reached |
| `pandokos_log_redactions_total` (M41) | above zero: a secret reached the log by mistake, a bug to report |
