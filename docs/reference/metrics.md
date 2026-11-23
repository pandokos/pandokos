# Metrics reference

Every metric Pandokos exports, with its OpenTelemetry name, kind, unit, attributes and Prometheus name, and when it is recorded. How to export and scrape them is in the [telemetry guide](../operations/telemetry.md); the decisions are in [ADR 0062](../adr/0062-metrics-instruments-attributes-cardinality-and-scrape.md).

## Version

The metrics follow the OpenTelemetry semantic conventions **1.41.0**, pinned by the import path `go.opentelemetry.io/otel/semconv/v1.41.0` and by the schema URL of the meter. Version 1.41.0 is the last that the Go semantic-convention packages ship with the GenAI conventions, which are in Development: names, units and attributes of `gen_ai.client.operation.duration`, `gen_ai.client.operation.time_to_first_chunk`, `gen_ai.client.token.usage` and `http.server.request.duration` come from that package. A change of version is a change of the import path and a line in the release notes. The instruments of the `pandokos.` namespace are the product's own.

The set is closed: 51 instruments, no others, and this page is checked against the code by a test. The `Id` column is the number the guide and the dashboards use.

## Instruments

Kinds: `H` histogram, `C` counter, `G` gauge, `OC` counter read at collection. An attribute followed by `?` is present only when it applies. Every value of an attribute comes from the closed lists below or from the platform's catalogue (the provider and model names); no value is a tenant, caller, key, request or trace.

| Name | Id | Kind | Unit | Attributes | Prometheus name | Recorded |
|---|---|---|---|---|---|---|
| `http.server.request.duration` | M1 | H | `s` | `http.request.method`, `url.scheme`, `http.response.status_code`?, `http.route`?, `error.type`? | `http_server_request_duration_seconds` | Every request of the inference listener except `/healthz` and `/readyz`, when the response ends. `error.type` is the status as text when it is 500 or more, or the outcome when no status was written. |
| `pandokos.gateway.requests` | M2 | C | `{request}` | `pandokos.outcome`, `pandokos.error_code`?, `pandokos.limit_state`, `gen_ai.provider.name`?, `pandokos.api_family`?, `gen_ai.operation.name`?, `gen_ai.request.model`? | `pandokos_gateway_requests_total` | Every request of the inference listener. Provider, API family and operation are set once the route matched; the model once the catalogue resolved it. |
| `gen_ai.client.operation.duration` | M3 | H | `s` | `gen_ai.operation.name`, `gen_ai.provider.name`, `gen_ai.request.model`, `error.type`? | `gen_ai_client_operation_duration_seconds` | Every call forwarded to a provider, from the call to the end of the response. `error.type` is the status (400 or more), `timeout`, `upstream_unreachable`, `upstream_aborted`, `cancelled` or `_OTHER`. |
| `gen_ai.client.operation.time_to_first_chunk` | M4 | H | `s` | `gen_ai.operation.name`, `gen_ai.provider.name`, `gen_ai.request.model` | `gen_ai_client_operation_time_to_first_chunk_seconds` | Streams only, from the call to the first chunk received. |
| `gen_ai.client.token.usage` | M5 | H | `{token}` | `gen_ai.operation.name`, `gen_ai.provider.name`, `gen_ai.token.type`, `gen_ai.request.model` | `gen_ai_client_token_usage` | Once for the input and once for the output of a request, when the provider reported the counts. Estimated counts are not here. |
| `pandokos.gen_ai.client.estimated_tokens` | M6 | C | `{token}` | `gen_ai.operation.name`, `gen_ai.provider.name`, `gen_ai.token.type`, `gen_ai.request.model` | `pandokos_gen_ai_client_estimated_tokens_total` | Once for each side of a request whose count was estimated by Pandokos because the provider gave none; the estimate is added. |
| `pandokos.gen_ai.client.cost` | M7 | C | `{cost}` | `gen_ai.operation.name`, `gen_ai.provider.name`, `gen_ai.request.model`, `pandokos.currency` | `pandokos_gen_ai_client_cost_total` | The recorded cost of each request that has a known price, in the currency of the price. |
| `pandokos.gen_ai.client.cost_unknown` | M8 | C | `{request}` | `gen_ai.operation.name`, `gen_ai.provider.name`, `gen_ai.request.model`, `pandokos.cost.reason` | `pandokos_gen_ai_client_cost_unknown_total` | Each request with usage whose cost could not be computed. |
| `pandokos.provider.rate_limited` | M9 | C | `{response}` | `gen_ai.provider.name`, `pandokos.credential.id` | `pandokos_provider_rate_limited_total` | Each answer of 429 from a provider. The one metric that names an identifier: the provider credential ID. |
| `pandokos.limits.refusals` | M10 | C | `{request}` | `pandokos.limit.code`, `pandokos.limit.metric`, `pandokos.limit.level`, `pandokos.limit.subject`, `pandokos.limit.scope` | `pandokos_limits_refusals_total` | Each request refused by a limit. The subject is its kind (`tenant`, `service_account`, `key`), never an ID. |
| `pandokos.limits.estimate_ratio` | M11 | H | `1` | `pandokos.limit.quantity` | `pandokos_limits_estimate_ratio` | For each request with an estimate: settled divided by reserved, per quantity, when something was reserved. |
| `pandokos.ledger.records` | M12 | OC | `{record}` | `pandokos.result` | `pandokos_ledger_records_total` | Read at collection: usage records written, rejected by the database, and lost at shutdown. |
| `pandokos.ledger.admission_refused` | M13 | OC | `{request}` | none | `pandokos_ledger_admission_refused_total` | Read at collection: requests the ledger refused because its buffer was full. |
| `pandokos.ledger.buffer.usage` | M14 | G | `{record}` | `pandokos.ledger.state` | `pandokos_ledger_buffer_usage` | Read at collection: slots of the buffer in use and queued. |
| `pandokos.ledger.buffer.limit` | M15 | G | `{record}` | none | `pandokos_ledger_buffer_limit` | Read at collection: the size of the buffer (`ledger.buffer_size`). |
| `pandokos.egress.blocked` | M16 | OC | `{connection}` | `pandokos.egress.reason` | `pandokos_egress_blocked_total` | Read at collection: connections and URLs the egress guard refused. |
| `pandokos.authn.attempts` | M17 | OC | `{attempt}` | `pandokos.listener`, `pandokos.authn.kind`, `pandokos.authn.result`, `pandokos.authn.reason`? | `pandokos_authn_attempts_total` | Read at collection: authentication attempts by listener, credential kind, result and failure reason. |
| `pandokos.jwks.fetches` | M18 | OC | `{fetch}` | `pandokos.jwks.document`, `pandokos.result` | `pandokos_jwks_fetches_total` | Read at collection: fetches of the keys and the metadata of token issuers. `ok` or a failure class. |
| `pandokos.jwks.sources` | M19 | G | `{source}` | `pandokos.jwks.state` | `pandokos_jwks_sources` | Read at collection: issuer key sources by state. |
| `pandokos.jwks.staleness` | M20 | G | `s` | none | `pandokos_jwks_staleness_seconds` | Read at collection: the age of the oldest last success among stale sources; 0 when none is stale. |
| `pandokos.signin.attempts` | M21 | OC | `{attempt}` | `pandokos.signin.method`, `pandokos.result` | `pandokos_signin_attempts_total` | Read at collection: sign-in attempts by method and result. |
| `pandokos.account.locks` | M22 | OC | `{lock}` | none | `pandokos_account_locks_total` | Read at collection: accounts locked after failed sign-ins. |
| `pandokos.sessions.open` | M23 | G | `{session}` | none | `pandokos_sessions_open` | The count of open sessions taken by the last purge round; empty until the first round has run. |
| `pandokos.password.hashes` | M24 | G | `{hash}` | `pandokos.password.state` | `pandokos_password_hashes` | Read at collection: password hash computations running and waiting for a slot. |
| `pandokos.idempotency.replays` | M25 | OC | `{request}` | none | `pandokos_idempotency_replays_total` | Read at collection: requests answered from a stored idempotent response. |
| `pandokos.authz.grants_refused` | M26 | OC | `{request}` | none | `pandokos_authz_grants_refused_total` | Read at collection: grants the authorization refused (the line `grant refused`). |
| `pandokos.support_actions.opened` | M27 | OC | `{action}` | none | `pandokos_support_actions_opened_total` | Read at collection: support actions opened. |
| `pandokos.support_actions.ended` | M28 | OC | `{action}` | `pandokos.support.cause` | `pandokos_support_actions_ended_total` | Read at collection: support actions ended, closed by the platform or ended by the tenant. |
| `pandokos.limits.reservations` | M29 | G | `{reservation}` | none | `pandokos_limits_reservations` | Read at collection: reservations outstanding in memory. |
| `pandokos.limits.counters` | M30 | G | `{counter}` | none | `pandokos_limits_counters` | Read at collection: counters held in memory. |
| `pandokos.limits.reservations_expired` | M31 | OC | `{reservation}` | none | `pandokos_limits_reservations_expired_total` | Read at collection: reservations that expired without being settled. It must stay at zero. |
| `pandokos.limits.reconciliations` | M32 | OC | `{run}` | `pandokos.result` | `pandokos_limits_reconciliations_total` | Read at collection: reconciliation runs, one per tenant reconciled. |
| `pandokos.limits.reconciliation.last_duration` | M33 | G | `s` | none | `pandokos_limits_reconciliation_last_duration_seconds` | Read at collection: the duration of the last reconciliation tick. |
| `pandokos.limits.cut_lag` | M34 | G | `s` | none | `pandokos_limits_cut_lag_seconds` | Read at collection: the time since the last cut of the reconciler; a value that keeps growing is an alert. |
| `pandokos.limits.events` | M35 | OC | `{event}` | `pandokos.result` | `pandokos_limits_events_total` | Read at collection: limit events recorded, dropped (the queue was full) or failed (the insert failed). |
| `pandokos.limits.rule_loads` | M36 | OC | `{load}` | `pandokos.result` | `pandokos_limits_rule_loads_total` | Read at collection: loads of the limit rules. |
| `pandokos.limits.admitted_unpriced` | M37 | OC | `{request}` | none | `pandokos_limits_admitted_unpriced_total` | Read at collection: requests admitted with an unknown estimated cost under `limits.on_unknown_price: allow`. |
| `pandokos.limits.unlimited_traffic` | M38 | OC | `{row}` | `pandokos.result` | `pandokos_limits_unlimited_traffic_total` | Read at collection: rows of the audit of traffic without limits. |
| `pandokos.telemetry.spans` | M39 | OC | `{span}` | `pandokos.telemetry.state` | `pandokos_telemetry_spans_total` | Read at collection: spans ended, exported and failed. Ended minus exported minus failed is what the queue dropped. |
| `pandokos.telemetry.export_failures` | M40 | OC | `{export}` | `pandokos.telemetry.signal`, `error.type` | `pandokos_telemetry_export_failures_total` | Read at collection: exports that failed, by signal and class. |
| `pandokos.log.redactions` | M41 | OC | `{value}` | none | `pandokos_log_redactions_total` | Read at collection: log values rewritten because they had the shape of a secret. Any non-zero value is a bug to report. |
| `pandokos.reports.rollups` | M42 | OC | `{run}` | `pandokos.result` | `pandokos_reports_rollups_total` | Read at collection: passes of the usage rollup, one per tenant, that succeeded or failed. |
| `pandokos.reports.rollup.lag` | M43 | G | `s` | none | `pandokos_reports_rollup_lag_seconds` | Read at collection: the largest time since the watermark among the tenants of the last pass; 0 before the first pass. |
| `pandokos.retention.removed` | M44 | OC | `{item}` | `pandokos.retention.kind` | `pandokos_retention_removed_total` | Read at collection: what retention removed, summed over tenants: usage partitions, usage records, rollup rows, audit events and platform audit events, and the usage records and audit events kept for deleted tenants. |
| `pandokos.retention.runs` | M45 | OC | `{run}` | `pandokos.result` | `pandokos_retention_runs_total` | Read at collection: runs of the retention job that succeeded or failed. |
| `pandokos.exports` | M46 | OC | `{export}` | `pandokos.export.kind`, `pandokos.result` | `pandokos_exports_total` | Read at collection: usage and audit exports, and the export of the log kept for a deleted tenant, that completed or were aborted after their first byte. |
| `pandokos.tenant_deletion.steps` | M47 | OC | `{step}` | `pandokos.deletion.step`, `pandokos.result` | `pandokos_tenant_deletion_steps_total` | Read at collection: steps of tenant deletions that ran or failed, summed over tenants. |
| `pandokos.tenant_deletion.open` | M48 | G | `{deletion}` | `pandokos.deletion.state` | `pandokos_tenant_deletion_open` | Refreshed at each tick of the deletion job: deletions not completed, scheduled or running. |
| `pandokos.vault.rewraps` | M49 | OC | `{key}` | `pandokos.result` | `pandokos_vault_rewraps_total` | Read at collection: data keys the re-wrap job re-wrapped under the active key-encryption key, or failed to. |
| `pandokos.vault.kek.versions` | M50 | G | `{version}` | `pandokos.kek.state` | `pandokos_vault_kek_versions` | Refreshed at each tick of the re-wrap job: key-encryption key versions by state. |
| `pandokos.vault.erasures.pending` | M51 | G | `{deletion}` | none | `pandokos_vault_erasures_pending` | Refreshed at each tick of the re-wrap job: completed deletions whose backups stay readable until a key version is retired. |

`target_info` (the resource of the process as labels, value 1) is added by the scrape endpoint and by the OTLP receiver of Prometheus.

The metrics named `gen_ai.*` and `http.server.*` are the conventional ones. Token usage holds only counts the provider reported; the counts Pandokos estimated are in `pandokos.gen_ai.client.estimated_tokens`, so that the sum of both is the whole. The model is the catalogue name of the resolved target, never the model name the caller sent or the one the provider answered with; the response model, the server address and the server port are not attributes of any metric.

## Prometheus names

Both ways out give the same names, so one dashboard serves both. The rule (rule R) is the translation that Prometheus applies to OTLP metrics with `translation_strategy: UnderscoreEscapingWithSuffixes`:

- Characters outside `[a-zA-Z0-9_:]` in a name or an attribute key become `_` (`gen_ai.request.model` is `gen_ai_request_model`).
- The unit `s` adds `_seconds`; a unit in braces (`{token}`, `{request}`) and the unit `1` add nothing; the suffix is not added when the name already ends with it.
- Counters end in `_total`.
- Histograms are exposed as `_bucket`, `_sum` and `_count`.

## Buckets

| Set | Instruments | Boundaries |
|---|---|---|
| Durations | `http.server.request.duration`, `gen_ai.client.operation.duration`, `gen_ai.client.operation.time_to_first_chunk` | 0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92 (seconds) |
| Token counts | `gen_ai.client.token.usage` | 1, 4, 16, 64, 256, 1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216, 67108864 |
| Ratios | `pandokos.limits.estimate_ratio` | 0.1, 0.25, 0.5, 0.75, 0.9, 1, 1.1, 1.25, 1.5, 2, 4 |

The duration of the HTTP server uses the boundaries of the GenAI durations, not those of the HTTP conventions, which end at 10 seconds where an inference call can last a minute.

## Closed values

| Attribute | Values |
|---|---|
| `pandokos.outcome` | `ok`, `pandokos_error`, `client_error`, `client_cancelled`, `shutdown_aborted`, `timeout`, `upstream_aborted` |
| `pandokos.error_code` | the error codes of the API ([ADR 0020](../adr/0020-error-contract.md)) |
| `pandokos.limit_state` | `not_evaluated`, `unlimited`, `limited`, `unverified` |
| `pandokos.api_family` | `openai`, `anthropic` |
| `gen_ai.operation.name` | `chat`, `embeddings`, `messages.count_tokens` |
| `gen_ai.provider.name` | the provider name of the catalogue, set once the route matched |
| `gen_ai.request.model` | the model name of the catalogue, set once the model resolved |
| `gen_ai.token.type` | `input` (cached tokens included), `output` (reasoning tokens included) |
| `error.type` of `gen_ai.client.operation.duration` | the provider's status as three digits when 400 or more, `timeout`, `upstream_unreachable`, `upstream_aborted`, `cancelled`, `_OTHER` |
| `error.type` of `http.server.request.duration` | the status as three digits when 500 or more; when no status was written, the outcome |
| `error.type` of `pandokos.telemetry.export_failures` | `timeout`, `status`, `connection`, `other` |
| `pandokos.currency` | the ISO 4217 codes of the recorded costs |
| `pandokos.cost.reason` | `no_price`, `category_unpriced`, `inconsistent_usage` |
| `pandokos.credential.id` | the ID of a provider credential, only on the 429 counter |
| `pandokos.limit.code` | `rate_limited`, `quota_exceeded`, `no_price` |
| `pandokos.limit.metric` | `concurrent_requests`, `requests_per_minute`, `tokens_per_minute`, `requests_per_day`, `requests_per_month`, `tokens_per_day`, `tokens_per_month`, `cost_per_day`, `cost_per_month` |
| `pandokos.limit.level` | `platform`, `tenant` |
| `pandokos.limit.subject` | the kind of the subject: `tenant`, `service_account`, `key` |
| `pandokos.limit.scope` | `all`, `provider`, `model` |
| `pandokos.limit.quantity` | `tokens`, `cost` |
| `pandokos.retention.kind` | `usage_partition`, `usage_record`, `usage_rollup`, `audit_event`, `platform_audit_event`, `deleted_tenant_usage_record`, `deleted_tenant_audit_event` |
| `pandokos.export.kind` | `usage`, `audit`, `platform_audit`, `deleted_tenant_audit` |
| `pandokos.deletion.step` | `begin`, `access`, `secrets`, `configuration`, `usage`, `audit`, `complete` |
| `pandokos.deletion.state` | `scheduled`, `running` |
| `pandokos.kek.state` | `active`, `retiring`, `retired` |
| `pandokos.result` | `written`, `rejected`, `lost` (ledger); `ok`, `blocked`, `timeout`, `status`, `too_large`, `invalid_document`, `issuer_mismatch`, `no_usable_key`, `network` (issuer fetches); `ok`, `failed`, `locked`, `disabled`, `unavailable`, `refused` (sign-in); `ok`, `failed` (reconciliations and rule loads); `recorded`, `dropped`, `failed` (limit events); `written`, `skipped`, `failed` (unlimited traffic); `ok`, `failed` (usage rollup passes, retention runs, deletion steps and re-wraps); `complete`, `aborted` (exports) |
| `pandokos.ledger.state` | `in_use`, `queued` |
| `pandokos.egress.reason` | `not_tcp`, `unparsable`, `forbidden_address`, `private_address`, `scheme`, `user_info` |
| `pandokos.listener` | `inference`, `admin` |
| `pandokos.authn.kind` | `key`, `token`, `unknown` |
| `pandokos.authn.result` | `ok`, `failed`, `unavailable` |
| `pandokos.authn.reason` | `malformed`, `kind_not_accepted`, `unknown_key`, `wrong_secret`, `revoked`, `expired`, `service_account_disabled`, `service_account_not_valid`, `algorithm_not_allowed`, `unknown_signing_key`, `bad_signature`, `issuer_mismatch`, `wrong_audience`, `not_yet_valid`, `lifetime_too_long`, `issuer_disabled`, `required_claim_missing`, `subject_missing`, `tenant_claim_missing`, `unknown_issuer`, `ambiguous_audience`, `tenant_not_listed`, `subject_not_mapped` |
| `pandokos.jwks.document` | `keys`, `metadata` |
| `pandokos.jwks.state` | `fresh`, `stale`, `missing` |
| `pandokos.signin.method` | `password`, `oidc` |
| `pandokos.password.state` | `active`, `waiting` |
| `pandokos.support.cause` | `closed`, `ended` |
| `pandokos.telemetry.state` | `ended`, `exported`, `failed` |
| `pandokos.telemetry.signal` | `traces`, `metrics` |

## Cardinality

Each instrument holds at most 2000 attribute sets; beyond that, measurements are added to one set marked `otel.metric.overflow=true`, so a bug or an abuse cannot grow the memory of the process. Exemplars are off. A request cannot create a series by its path or its body: the provider and the model are set only after the catalogue resolved them.
