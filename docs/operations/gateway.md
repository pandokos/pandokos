# Gateway guide

The inference listener forwards requests of a provider's own API to that provider, on behalf of a
tenant, with the tenant's stored credential. This page says what a caller sends, what a call needs,
what Pandokos changes in a request, which errors it answers with, and how outbound connections are
controlled. It applies from sprint 3.1; sprint 3.2 adds two operations, streaming details, caller metadata and the usage record.

## 1. Base URLs

The first path segment names a provider of the catalogue and the rest is the provider's own path.
Point an SDK at `/<provider>/v1`:

| SDK | Base URL |
|---|---|
| OpenAI SDK, provider `openai` | `https://pandokos.example.com/openai/v1` |
| any OpenAI-compatible client, provider `acme` | `https://pandokos.example.com/acme/v1` |

The key is a Pandokos virtual key, sent in the header the SDK uses for the provider's own key
(`Authorization: Bearer <key>` for the `openai` family).

Supported operations of the `openai` family, each a `POST` to the provider's own path:

| Operation | Path | Streams |
|---|---|---|
| `chat.completions` | `/<provider>/v1/chat/completions` | when the body has `stream: true` |
| `responses` | `/<provider>/v1/responses` | when the body has `stream: true` |
| `embeddings` | `/<provider>/v1/embeddings` | never |

A `responses` request that asks for background processing (`"background": true`) is refused with
`invalid_request`: the provider would answer without usage and the result would be fetched from a path
that is not served. A `responses` body must name `model`; a stored prompt that names the model at the
provider is refused for the same reason (the model check needs it).

Anything else answers 404 `endpoint_not_supported`: an unknown, disabled or reserved provider name, a
provider whose API family has no adapter, a path or method that is not on the list. These cases are
indistinguishable on purpose. Which provider names are served can be learned without a key.

## 2. What a call needs

- A virtual key in the native header of the family. Without one, or with a key that cannot be
  verified, the answer is `unauthenticated`, the same for every cause.
- Both permissions, `model:invoke` and `credential:use`, on the key's own tenant. Both are decided
  before the body is read. A key whose scopes leave only `model:invoke` authenticates and is refused
  with `forbidden`: it cannot call a model and cannot learn which models it would be allowed.
  The built-in roles `inference` and `tenant-admin` hold both.
- A model that the tenant may use, named as in the catalogue (`model_not_allowed` otherwise).
- A credential of the tenant for the provider. If the key is pinned to a credential of this
  provider, that credential is used; otherwise the tenant's default credential for the provider.
  Nothing else is tried. No credential, or one that is disabled or revoked, is `no_credential`.

## 3. What Pandokos changes in a request

The outbound request is built from an empty header. Only these headers of the caller are
forwarded for the `openai` family: `Accept`, `Content-Type`, `User-Agent`, `OpenAI-Organization`,
`OpenAI-Project`, `OpenAI-Beta`. A header named in `Connection` is dropped even when listed.

Dropped, and never reaching the provider: `Authorization` (replaced by the tenant's credential),
`Cookie`, `X-Api-Key`, `Proxy-Authorization`, `Forwarded`, `X-Forwarded-*`, `Accept-Encoding`
(compression is not requested, so that usage can be read), `Idempotency-Key` (the outbound request
is never replayed by the transport), `X-Stainless-*`, `Traceparent`, `Tracestate`, and every
hop-by-hop header (`Connection`, `Keep-Alive`, `Proxy-Connection`, `Te`, `Trailer`,
`Transfer-Encoding`, `Upgrade`). `Host` is the endpoint's host; the URL is the endpoint's base URL plus
the native path, without the caller's query.

Two changes to the body:

1. For streaming `chat.completions` requests the option that makes the provider send usage in the
   stream is added ([ADR 0024](../adr/0024-openai-stream-usage-option.md)). Every other byte is
   forwarded as received. The `responses` and `embeddings` operations need no option: the provider
   always reports usage there.
2. When the model's upstream identifier differs from its public name, the value of `model` is
   replaced by the upstream identifier. The response is not rewritten: the provider's `model` member
   shows the upstream identifier.

The body must be one JSON object with exactly one top-level string member `model`. Duplicates are
refused (`invalid_request`), because parsers disagree on which occurrence counts.

The provider sees the tenant's credential, the allow-listed headers and the body. It never sees the
virtual key, the caller's other headers, the caller metadata, the caller's address or the tenant's
identity (PRV-02; see [data flows](../reference/data-flows.md)). The one exception to the rule on
`Traceparent` and `Tracestate`: with `telemetry.traces.propagate` on, Pandokos sets them itself from
its own span ([telemetry guide](telemetry.md)); the caller's are never copied.

Response headers pass through an allow-list (`Content-Type`, `Content-Length`, `Retry-After`,
`X-Request-Id`, `X-Ratelimit-*`, `Openai-*`); status and body pass unchanged.

## 4. Errors

| Code | Status | Raised when |
|---|---|---|
| `unauthenticated` | 401 | the key is missing, malformed, unknown, expired, revoked or wrong |
| `tenant_suspended` | 403 | the tenant is not active |
| `forbidden` | 403 | the key lacks `model:invoke` or `credential:use`, or the credential cannot be used |
| `model_not_allowed` | 403 | the model is not available to this tenant |
| `endpoint_not_supported` | 404 | the provider or operation is not served |
| `no_credential` | 409 | the tenant has no usable credential for the provider |
| `invalid_request` | 400 | the body is not one JSON object with one string member `model`, or a `responses` request asks for background processing |
| `request_timeout` | 408 | the body was not received within `limits.request_body_read_timeout` |
| `request_too_large` | 413 | the body is over `limits.max_request_body_bytes` |
| `upstream_unreachable` | 502 | the provider could not be reached, the egress guard refused the connection, or the provider answered with a redirect |
| `upstream_timeout` | 504 | the provider did not answer headers within `limits.max_request_duration` |
| `unavailable` | 503 | the catalogue, the key or the credential could not be verified, or the usage ledger buffer is full (the log line has `ledger: full`; no usage record is written for that request) |
| `internal` | 500 | an unexpected failure |

Every Pandokos error carries the headers `X-Pandokos-Error` (the code) and `X-Pandokos-Request-Id`,
and a body in the family's shape:

```json
{"error":{"message":"Pandokos: no usable provider credential for this provider.","type":"pandokos_error","param":null,"code":"no_credential"}}
```

A response with `X-Pandokos-Error` comes from Pandokos. A provider's error passes unchanged and never
carries that header. Messages are constants; none holds a model name, a provider name, an address or
the text of an underlying error.

SDKs retry 408 and 409 by default. `request_timeout` and `no_credential` can therefore be retried
by the client; the second one will keep failing until a credential exists.

## 4a. Streaming

A stream is forwarded as the provider sends it: each read from the provider is written to the caller
and flushed before the next read, with no timer or buffer of Pandokos's in between. Two modes:

- **Observe**: the caller asked for usage itself, or the operation is `responses`. The caller gets
  exactly the bytes the provider sent, in the same writes. Pandokos reads a copy for usage.
- **Strip**: a `chat.completions` stream whose usage option Pandokos added. Every complete event is
  forwarded as soon as its terminating blank line arrives, except the usage-only event, which is
  removed. In this mode events are forwarded whole, not byte by byte. **The one observable
  difference** ([ADR 0024](../adr/0024-openai-stream-usage-option.md)): the provider also puts
  `"usage":null` in every other chunk when the option is on, and that member is left in place.

If the usage reader fails (an event over its bound, a malformed answer, an internal error) it is
abandoned: the rest of the stream is forwarded unchanged, the usage of the request is estimated, and
`usage reader failed` is logged without data. In strip mode the bytes of an incomplete event it held
are lost.

**`ttfb_ms`** is the time from the start of the request to the first byte of the provider's response
body, for streams and plain answers alike; it is absent when no body byte arrived.

## 4b. Caller metadata and the usage record

Headers named `X-Pandokos-Metadata-<key>` label a request. After authentication, at most eight short
ASCII entries are read and stored in the usage record, never forwarded; entries outside the bounds
are dropped and their number is returned in the response header `X-Pandokos-Metadata-Dropped` when not
zero. The bounds and the warning about personal data are in the [usage guide](usage.md), which also
describes the usage record every authenticated request leaves.

## 5. Limits

| Limit | Key (default) | When exceeded |
|---|---|---|
| Request headers | `listeners.inference.max_header_bytes` (65536), `read_header_timeout` (10 s) | 431 or closed connection |
| Request body size | `limits.max_request_body_bytes` (10485760) | 413 `request_too_large` |
| Request body time | `limits.request_body_read_timeout` (30 s) | 408 `request_timeout` |
| Connect and TLS handshake | `upstream.connect_timeout` (10 s) | 502 `upstream_unreachable` |
| Whole forwarded request, stream included | `limits.max_request_duration` (10 m) | 504 before the provider's headers; the response is cut after |
| Provider response headers | 262144 bytes | 502 `upstream_unreachable` |

On shutdown readiness goes down, in-flight requests continue for `shutdown.drain_timeout`, and a
request that has not been forwarded by then is not forwarded. The usage records are then written
under `ledger.shutdown_flush_timeout` ([usage guide](usage.md)).

When the ledger buffer (`ledger.buffer_size`) is full, the instance answers 503 `unavailable` to new
authenticated requests and `/readyz` fails with the check `ledger`, until the slots in use fall to half
of the buffer.

## 6. Outbound connections

Every outbound connection of the server (provider calls, credential validation, price download) is
made by one component, with two classes of endpoint:

- **public** endpoints (`private` false): `https` only, public addresses only;
- **private** endpoints (`private` true): `https` or `http`, public or private addresses. Mark an
  endpoint private when the provider lives on your network, such as a self-hosted server.

The class of the address is checked when connecting, after name resolution, on the address about to
be used, so a name that resolves somewhere else later is refused. Forbidden addresses are refused
for both classes.

| Class | Addresses |
|---|---|
| forbidden | `0.0.0.0/8`, `::`, `224.0.0.0/4`, `ff00::/8`, `240.0.0.0/4`, and the instance-metadata addresses `169.254.169.254`, `169.254.170.2`, `100.100.100.200`, `fd00:ec2::254`; the deprecated IPv4-compatible `::/96` and IPv4-translated `::ffff:0:0:0/96` ranges (`::1` stays loopback) |
| private | `127.0.0.0/8`, `::1`, `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `100.64.0.0/10`, `169.254.0.0/16`, `fe80::/10`, `fc00::/7`, `fec0::/10`, `192.0.0.0/24`, `198.18.0.0/15`, `192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`, `2001:db8::/32`, `2001::/32`, `64:ff9b:1::/48` |
| embedded | `64:ff9b::/96` and `2002::/16` take the class of the IPv4 address they embed; IPv4-mapped addresses are unmapped first |
| public | everything else |

Redirects are never followed and never passed on: a provider that answers with a redirect is
`upstream_unreachable`. Certificates are always verified.

A refusal by the guard logs a warning `egress blocked` with `request_id`, `provider`, `endpoint`,
`reason` (`scheme`, `user_info`, `private_address`, `forbidden_address`, ...) and `address`. The
address is in the operator's log only, never in a response. A `private_address` refusal on a
provider you run yourself means the endpoint should be marked private
(`pandokos endpoint update --private <provider> <endpoint>`).

## 7. TLS and proxy

- `upstream.tls.ca_file` (`PANDOKOS_UPSTREAM_TLS_CA_FILE`): a PEM file whose certificates are added
  to the system roots for every outbound TLS connection (providers, validation, price download, an
  `https` proxy). It is read at start; restart to reload. There is no way to turn verification off.
- `upstream.proxy.url` (`PANDOKOS_UPSTREAM_PROXY_URL`): an `http` or `https` proxy URL, without user
  information, used with `CONNECT` for public endpoints, validation and the price download.
- The variables `HTTPS_PROXY`, `HTTP_PROXY`, `ALL_PROXY` and `NO_PROXY` are ignored. When one is set
  and `upstream.proxy.url` is not, `serve` logs a configuration warning that names the variable (not
  its value).
- Through a proxy the connection that is checked is the one to the proxy. Address literals in the
  target are still classified; target names are resolved by the proxy, so the proxy is the control
  point for them. Neither the caller's headers nor a credential is in the `CONNECT` request.
- Private endpoints never use the proxy.

## 8. Authentication failures

Every failure is logged as `authentication failed` (warn) with the reason, never the secret. A failure
that names a key of a tenant (wrong secret, revoked, expired, service account disabled or not valid)
is also recorded in that tenant's audit log as `auth.failure` (`detail.listener` `inference`). The
record is bounded: at most one per key and reason in ten minutes and 60 a minute per instance.
Failures that name no tenant (malformed, unknown key, kind not accepted) are logged only. The answer
to the caller is the same whether or not a row was written.

## 9. Upgrading from the `skeleton` configuration

The single provider key of the `skeleton` section no longer exists. Before upgrading, remove
`skeleton` from the configuration file: a file that still has it is refused with `unknown key
skeleton`, and the two environment variables are reported as unknown and ignored. After
`pandokos migrate`, if `skeleton.openai.base_url` was not the default, point the endpoint of `openai`
elsewhere with `pandokos endpoint update` (the base URL is now written without `/v1`), and have each
tenant register its own credential with `pandokos credential create --provider openai`.

## 10. Log fields of a request

The `request` line of the [container guide](container.md) gains these fields from sprint 3.2:

| Field | Present | Meaning |
|---|---|---|
| `record_id` | the request was admitted | the ID of its usage record |
| `trace_id` | the request has a trace | 32 hexadecimal characters |
| `cost_status` | the request was admitted | `known`, `unknown` or `none` |
| `cost`, `currency` | the cost is known | the amount and its currency |
| `usage_source` | the provider answered | `provider`, `estimated` or `none` |
| `metadata_count`, `metadata_dropped` | metadata was sent / entries were dropped | counts, never values |
| `usage_reader` | the reader gave up | `failed` |
| `ledger` | step 3a refused the request | `full` (written `ledger: full` in this guide) |

`ttfb_ms` now means the first body byte (section 4a).
