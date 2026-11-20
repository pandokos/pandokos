# 0055. Operational telemetry holds no tenant data

- Status: proposed
- Date: 2026-11-20

Refines [ADR 0054](0054-usage-data-is-the-tenants.md), [ADR 0039](0039-usage-record-admission-and-ledger-writer.md),
[ADR 0040](0040-traces.md) and [ADR 0015](0015-observability.md). Requirements OBS-03, OPS-12.

## Context

ADR 0054 took costs, currencies and token counts out of the log and the spans, behind
`telemetry.tenant_usage_details`, and kept the rest: the tenant, the caller and its key, the model,
the credential, the sizes, the usage source, the limit rules. Two code-scanning alerts ("clear-text
logging of sensitive information") on the request line and on the ledger writer led to a review of
2026-11-20. The key ID that the alerts follow is the public part of a virtual key, never its secret,
so the alerts are false positives as to credentials. The review found the real problem elsewhere:

- The request line, taken whole, is a copy of the tenant's usage record in a place the tenant does
  not control. From tenant, model, endpoint, sizes and times an operator rebuilds what a tenant uses
  and, with public prices, an estimate of what it spends, which ADR 0054 says the platform does not
  see.
- The ledger writer logged a record it could not write at shutdown in full, with the stated purpose
  of rebuilding it by hand with an `INSERT`. That makes the log a dead-letter queue of the ledger.
  OPS-12 asks for a graceful shutdown (drain the streams up to a deadline), not for the recovery of
  records. Pandokos is a proxy: when it stops, it stops.
- Spans carried the same identifiers, and the plan of the metrics sprint would have added model and
  token counts under the GenAI conventions.

## Decision

1. **The log and the traces are the operator's operational channel.** They answer whether the
   installation works and why one request failed, not what a tenant did. They are not a copy of the
   ledger.
2. **No line and no span names a tenant.** No tenant, service account, key, token registration,
   subject or credential identifier; no model; no size, usage source, token count, cost, currency or
   metadata count; no limit rule (subject, metric, scope, level, value, `Retry-After`, reservation).
   Principals of the platform stay named in the `admin request` line: their activity is the
   platform's.
3. **The request line** holds `request_id`, `trace_id`, method, route, status, outcome, error code,
   `limit_state`, `limits_unverified`, `invalid_reason`, durations, provider, endpoint, `stream`,
   `usage_injected`, the upstream status and error, the egress reason, `ledger` and `usage_reader`.
4. **A failed authentication** is logged with the listener, the request ID, the reason and whether
   it was audited. Key, tenant, registration and subject go to the tenant's audit log only.
5. **The ledger writer logs no row.** A record that cannot be written at shutdown is lost and
   counted (`ledger closed with records lost`); a record refused by the table or by `Validate` is
   logged with its request ID and the failure (SQLSTATE, constraint, column name). There is no
   recovery of lost records.
6. **Lines that repeated the tenant's own tables are removed**: `limit event recorded`,
   `unlimited traffic recorded`, `credential opened`. The remaining error lines of limits, vault and
   resolver keep the request ID and the error class, without the tenant.
7. **Spans carry only the attributes the OpenTelemetry semantic conventions mark Required, nothing
   else.** The server span follows the HTTP server span (`http.request.method`, `url.path`,
   `url.scheme`), the provider span the GenAI inference span (`gen_ai.operation.name`,
   `gen_ai.provider.name`). Conditionally Required, Recommended and Opt-In attributes are optional
   and not emitted; there is no `pandokos.*` attribute. Span names carry no model.
8. **`telemetry.tenant_usage_details` is removed.** No setting brings tenant data back into logs or
   traces.
9. **Correlation goes through the tenant.** The tenant holds the `request_id` (the response header
   `X-Pandokos-Request-Id` and a column of its usage records) and gives it when it reports a
   problem; the operator finds the line by it. A tenant dimension of operational telemetry, with
   pseudonymisation or encryption and a controlled re-identification, is issue #52.

## Consequences

- An operator cannot group lines by tenant until #52. Investigations that start from the tenant go
  through its `request_id`; the figures are in the tenant's tools (capability check, usage list,
  limit events, rules).
- Records lost at shutdown stay lost; the stop grace period of the container guide is what keeps
  them few.
- The two code-scanning alerts no longer have a sink: the key ID reaches no log line.
- The metrics sprint (6.1) applies the GenAI conventions to metrics, aggregated and without a
  tenant label; spans keep the Required attributes only. OBS-03 says so.

## Alternatives considered

**Keep the identifiers and remove only the figures (ADR 0054).** The line stays a usage record
under another name.

**Pseudonymise the tenant ID now.** It needs a key, its rotation and a re-identification procedure;
designed in #52, not improvised here.

**A durable spool of unwritten records instead of the log.** Same problem in another place: a proxy
does not keep a dead-letter queue of what it could not write.

**All the attributes of the GenAI conventions on spans.** Model and token counts per request are the
tenant's usage; the conventions do not require them.

## History

- 2026-11-20: proposed after the maintainer's review of the code-scanning alerts on the request line
  and on the ledger writer.
