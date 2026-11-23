# Data flows

What leaves the Pandokos process, to whom, and what never does (PRV-02). The first table is the
summary; the sections after it add the detail. It describes the inference listener as of sprint 4.1, with the metrics of sprint 6.1.

| Destination | Receives | Never receives |
|---|---|---|
| LLM provider (the endpoint of the catalogue) | the request body as the caller sent it, with the two documented changes (the usage option added to a chat stream of the `openai` family unless the provider's `stream_usage_option` is `omit`, and the model name replaced by the upstream identifier); the allow-listed request headers; the tenant's provider credential in the provider's own authentication header (`Authorization: Bearer` for the `openai` family, `X-Api-Key` for `anthropic`); for `anthropic` the caller's `Anthropic-Version` and `Anthropic-Beta` headers, as sent; optionally `traceparent` and `tracestate` of Pandokos's own span, when `telemetry.traces.propagate` is on | the Pandokos virtual key; the caller's other headers (`Cookie`, `Forwarded`, `X-Forwarded-*`, the caller's own `traceparent`, `tracestate`, `baggage` and idempotency headers); caller metadata (`X-Pandokos-Metadata-*`); tenant, principal, key and request identifiers; the caller's address; any other tenant's data |
| Identity provider of a registered issuer (the host of an `issuer` or `jwks_uri` that a tenant or platform administrator registered; only when a token arrives) | one `GET` of the discovery document and one of the key set, at most every 10 seconds per source (`tokens.jwks.min_refetch_interval`), with no body, no cookie, no credential and no header of the caller | the token, any claim, the caller's address, tenant, principal and request identifiers, and every other tenant's data |
| OTLP trace collector (`telemetry.otlp.endpoint`, when set; the same collector receives the metrics, next row) | two spans per authenticated request carrying only the attributes the OpenTelemetry conventions make Required (HTTP method, path without query string and scheme of the server span; GenAI operation name and provider name of the provider span), the span status and the trace and span IDs; resource attributes; the headers of `telemetry.otlp.headers_file`, which are for the collector | tenant, principal, key, credential and request identifiers; model names; usage, token counts and costs; request and response bodies; the provider credential; the Pandokos virtual key; any header value of the caller; caller metadata; anything of an unauthenticated request |
| OTLP metrics collector (`telemetry.otlp.endpoint` with `telemetry.metrics.otlp`, when set) | the 46 instruments of the [metrics reference](metrics.md), aggregated in memory and sent every `telemetry.metrics.export_interval`: counts and distributions of requests by outcome, error code and limit state; latency, time to first chunk, tokens, estimated tokens and cost by provider, catalogue model and currency; limit refusals by code, metric, level, subject kind and scope; provider answers of 429 by provider and provider credential ID; state of the ledger, authentication, issuer keys, sign-in, limiter and telemetry; resource attributes; the headers of `telemetry.otlp.headers_file` | any tenant, caller, key, request or trace identifier (the one identifier is the provider credential ID on the counter of 429 answers); the model name a caller sent; request and response bodies; headers; metadata; URLs and hosts; a content-bearing value |
| Scraper of `GET /metrics` (Prometheus or another client of a platform key holding `metrics:read`, when `telemetry.metrics.prometheus` is on) | the same metrics, in the Prometheus text format, on the administration listener, to the holder of a platform key (or a platform-level issuer's token) whose role holds `metrics:read`; the scraper presents that credential | the same as the metrics collector: no tenant, caller, key, request or trace identifier but the provider credential ID of the 429 counter, no content; and nothing to a tenant principal, a session cookie or a request without a credential |
| Caller of a usage or audit export (the administration listener, `usage:read`, `audit:read` or `platform_audit:read`) | the tenant's usage records of a window of at most 31 days, or the rows of its audit log or of the platform's, as NDJSON or CSV, streamed to the authenticated caller; audit details of prices are withheld from a reader without `price_list:read` | the records or events of any other tenant (the platform's usage reads nothing of a tenant); content, keys, secrets |
| Operator running `pandokos export` | the declarative document of the platform or of one tenant, written to standard output or a file the operator names | any secret, key, credential, person or usage figure, and the rules of subject mappings |
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
logged or returned to anyone. The administration API makes such requests only with sign-in through an identity provider (below) or, with `tokens.admin_listener`, for the issuers that administrators registered. Because the address is
supplied by a tenant administrator (or a platform administrator), the egress controls and the
operator's host list, not the registration, decide where the gateway may connect.

### Sign-in of people

With `auth.oidc.enabled` the administration role sends the browser of a person to the provider and
receives it back with a code. It then fetches the provider's metadata and key set and redeems the code at
its token endpoint, through the egress guard (no redirect, 10 seconds, 64 KiB; a private address only for a
host in `auth.oidc.private_hosts`). The redeem carries the client ID and secret, the code, the redirect URI
and the PKCE verifier. The provider learns that this instance signs people in and when, and sees the
person at its own login page. Pandokos receives an ID token, reads the subject, the display name, the e-mail
address and the groups from it, and drops the access, ID and refresh tokens at once: they are never stored,
logged or sent to the browser. Passwords of local accounts reach no other system.

## Limits

Limits add no outbound connection. The counters are in the memory of each instance, and the rules are read from the database, which the gateway already uses. Nothing of a request, a header or a credential is passed to the limiter: it sees tenant, service account and key identifiers, metric names and token counts ([limits guide](../operations/limits.md)).

## The collector

Nothing is sent without an endpoint. The connection is made with the egress guard: certificates
verified, no proxy, no redirect. A collector receives, for traces and for metrics, what the
[telemetry guide](../operations/telemetry.md) and the [metrics reference](metrics.md) list and nothing
else; no setting turns content or a tenant identifier on.

## The scrape endpoint

`GET /metrics` sends nothing out: it answers a request on the administration listener that carries a
platform credential with `metrics:read`, with the same metrics as the collector receives. What the
scraper then does with them is its own; the [telemetry guide](../operations/telemetry.md#11-what-a-metric-may-say)
says what they may reveal.

## The database

The [usage guide](../operations/usage.md) lists the columns of a usage record. The only text a caller
controls in a row is `metadata`, which the caller is told to keep free of personal data. A
provider's usage object reaches the row only as non-negative integers under keys of a fixed
alphabet.

## Exports and the declarative document

An export is the answer to an authenticated request on the administration listener: the tenant's own
records (`usage:read`) or log (`audit:read`), or the platform's log (`platform_audit:read`), streamed to
that caller and to nobody else; Pandokos stores no copy and sends nothing to another system. What the
caller does with the file is outside the product: a usage export holds the tenant's caller metadata and
costs, an audit export holds the actors and details of the log, and neither is protected once saved. The
declarative document of `pandokos export` holds configuration only (names, limits, price lists, roles,
permissions) and no secret, key, credential, person or usage figure; `pandokos apply` sends it back
through the same API.

## The log

The log is the operator's and holds nothing of a tenant ([ADR 0055](../adr/0055-operational-telemetry-holds-no-tenant-data.md)); every message and field is in the [log reference](logs.md), and values with the shape of a secret are redacted ([ADR 0063](../adr/0063-the-log-redaction-and-the-log-reference.md)):
no tenant, principal, key, credential or registration identifier, no subject, no model, no size,
usage, cost or limit rule, and no usage record (a record that cannot be written at shutdown is
counted, not logged). It is not a copy of the ledger. An operator reaches one request by its
`request_id`, which the tenant holds. Platform principals are named in the `admin request` lines,
since their activity is the platform's; the issuer's host appears in the key-retrieval lines, with an
address literal shown as `(address)`.
