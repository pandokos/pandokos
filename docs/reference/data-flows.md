# Data flows

What leaves the Pandokos process, to whom, and what never does (PRV-02). The first table is the
summary; the sections after it add the detail. It describes the inference listener as of sprint 4.1.

| Destination | Receives | Never receives |
|---|---|---|
| LLM provider (the endpoint of the catalogue) | the request body as the caller sent it, with the two documented changes (the usage option added to a chat stream of the `openai` family unless the provider's `stream_usage_option` is `omit`, and the model name replaced by the upstream identifier); the allow-listed request headers; the tenant's provider credential in the provider's own authentication header (`Authorization: Bearer` for the `openai` family, `X-Api-Key` for `anthropic`); for `anthropic` the caller's `Anthropic-Version` and `Anthropic-Beta` headers, as sent; optionally `traceparent` and `tracestate` of Pandokos's own span, when `telemetry.traces.propagate` is on | the Pandokos virtual key; the caller's other headers (`Cookie`, `Forwarded`, `X-Forwarded-*`, the caller's own `traceparent`, `tracestate`, `baggage` and idempotency headers); caller metadata (`X-Pandokos-Metadata-*`); tenant, principal, key and request identifiers; the caller's address; any other tenant's data |
| Identity provider of a registered issuer (the host of an `issuer` or `jwks_uri` that a tenant or platform administrator registered; only when a token arrives) | one `GET` of the discovery document and one of the key set, at most every 10 seconds per source (`tokens.jwks.min_refetch_interval`), with no body, no cookie, no credential and no header of the caller | the token, any claim, the caller's address, tenant, principal and request identifiers, and every other tenant's data |
| OTLP trace collector (`telemetry.otlp.endpoint`, when set) | two spans per authenticated request carrying only the attributes the OpenTelemetry conventions make Required (HTTP method, path without query string and scheme of the server span; GenAI operation name and provider name of the provider span), the span status and the trace and span IDs; resource attributes; the headers of `telemetry.otlp.headers_file`, which are for the collector | tenant, principal, key, credential and request identifiers; model names; usage, token counts and costs; request and response bodies; the provider credential; the Pandokos virtual key; any header value of the caller; caller metadata; anything of an unauthenticated request |
| Database (PostgreSQL) | configuration; the registrations of trusted issuers and subject mappings; the sealed provider credentials (ciphertext); the usage ledger, one content-free row per authenticated request; the audit log, which for a refused token holds the reason, the registration's ID and, once the signature verified, the subject | request and response bodies; the provider's text; a provider credential or virtual-key secret in clear (keys are stored as hashes, credentials as ciphertext); a token, a segment of one or a digest of one |
| Standard output (the structured log) | one line per request with the request and trace IDs, route, status, outcome, error code, provider, endpoint, durations and limit state; warnings and errors of the server; for a refused credential, the reason | tenant, principal, key, credential and registration identifiers; the subject of a token; model names; sizes, usage, token counts, costs and limit rules; usage records; bodies; a token or any part of one; any header value; query strings; secrets of any kind (values of sensitive keys are replaced); caller metadata; the text of an upstream error |

## The provider

Pandokos builds the outbound request from an empty header and copies only the allow-list of the
family ([gateway guide](../operations/gateway.md), section 3). The provider therefore sees what a
direct call from your network would show it, plus the tenant's credential and, only if you enable
it, a trace context that belongs to Pandokos. It does not see which tenant or principal called.
Caller metadata is stored in the ledger and is on no forwarding list.

## The identity provider

The gateway fetches the documents of an issuer that an administrator registered: `https` only, through
the egress guard (the proxy of `upstream.proxy.url` for public hosts, forbidden addresses refused, no
redirect), and a host that resolves to a private address only if the operator listed it in
`tokens.private_hosts`. The requests carry nothing of the caller or of the token, so the provider
learns that a Pandokos instance verifies tokens of its issuer, and when. Nothing of an answer is
logged or returned to anyone. The administration API makes no such request. Because the address is
supplied by a tenant administrator (or a platform administrator), the egress controls and the
operator's host list, not the registration, decide where the gateway may connect.

## Limits

Limits add no outbound connection. The counters are in the memory of each instance, and the rules are read from the database, which the gateway already uses. Nothing of a request, a header or a credential is passed to the limiter: it sees tenant, service account and key identifiers, metric names and token counts ([limits guide](../operations/limits.md)).

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

The log is the operator's and holds nothing of a tenant ([ADR 0055](../adr/0055-operational-telemetry-holds-no-tenant-data.md)):
no tenant, principal, key, credential or registration identifier, no subject, no model, no size,
usage, cost or limit rule, and no usage record (a record that cannot be written at shutdown is
counted, not logged). It is not a copy of the ledger. An operator reaches one request by its
`request_id`, which the tenant holds. Platform principals are named in the `admin request` lines,
since their activity is the platform's; the issuer's host appears in the key-retrieval lines, with an
address literal shown as `(address)`.
