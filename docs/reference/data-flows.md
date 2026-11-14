# Data flows

What leaves the Pandokos process, to whom, and what never does (PRV-02). The first table is the
summary; the sections after it add the detail. It describes the inference listener as of sprint 3.2.

| Destination | Receives | Never receives |
|---|---|---|
| LLM provider (the endpoint of the catalogue) | the request body as the caller sent it, with the two documented changes (the usage option added to a chat stream, and the model name replaced by the upstream identifier); the allow-listed request headers; the tenant's provider credential in the provider's own authentication header; optionally `traceparent` and `tracestate` of Pandokos's own span, when `telemetry.traces.propagate` is on | the Pandokos virtual key; the caller's other headers (`Cookie`, `Forwarded`, `X-Forwarded-*`, the caller's own `traceparent`, `tracestate`, `baggage` and idempotency headers); caller metadata (`X-Pandokos-Metadata-*`); tenant, principal, key and request identifiers; the caller's address; any other tenant's data |
| OTLP trace collector (`telemetry.otlp.endpoint`, when set) | two spans per authenticated request carrying identifiers and measurements (request, record, tenant, principal, key and credential IDs, provider, endpoint and model names, operation, outcome, status codes, first-byte time, usage source, cost status and amount); resource attributes; the headers of `telemetry.otlp.headers_file`, which are for the collector | request and response bodies; the provider credential; the Pandokos virtual key; any header value of the caller; caller metadata; URLs; token counts; a model string that did not resolve; anything of an unauthenticated request |
| Database (PostgreSQL) | configuration; the sealed provider credentials (ciphertext); the usage ledger, one content-free row per authenticated request; the audit log | request and response bodies; the provider's text; a provider credential or virtual-key secret in clear (keys are stored as hashes, credentials as ciphertext) |
| Standard output (the structured log) | one line per request with identifiers, route, status, outcome, durations, byte counts, usage and cost figures, counts of metadata entries; warnings and errors of the server | bodies; any header value; query strings; secrets of any kind (values of sensitive keys are replaced); the values of caller metadata; the text of an upstream error |

## The provider

Pandokos builds the outbound request from an empty header and copies only the allow-list of the
family ([gateway guide](../operations/gateway.md), section 3). The provider therefore sees what a
direct call from your network would show it, plus the tenant's credential and, only if you enable
it, a trace context that belongs to Pandokos. It does not see which tenant or principal called.
Caller metadata is stored in the ledger and is on no forwarding list.

## The collector

Nothing is sent without an endpoint. The connection is made with the egress guard: certificates
verified, no proxy, no redirect. A collector receives what the [telemetry guide](../operations/telemetry.md)
lists and nothing else; no setting turns content on.

## The database

The [usage guide](../operations/usage.md) lists the columns of a usage record. The only text a caller
controls in a row is `metadata`, which the caller is told to keep free of personal data. A
provider's usage object reaches the row only as non-negative integers under keys of a fixed
alphabet.

## The log

The log is the operator's. It holds identifiers and measurements, and, when the ledger cannot write
a record at shutdown, the row of that record as JSON without its metadata values
(`usage record not written`). Treat it as operational data: it names tenants, keys and request IDs.
