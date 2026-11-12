# Running the container image

This is a pre-release: one static access token, plain HTTP, one endpoint, no tenants in the
product yet. It is not for production. It needs a PostgreSQL 16 database, and nothing else: see the
[database guide](database.md) to prepare it.

## The image

- Name: `ghcr.io/pandokos/pandokos`. Tags on release: the version without `v` (`0.1.0`); for a
  release that is not a pre-release also `0.1` and `latest`.
- Platforms: `linux/amd64` and `linux/arm64`, one image index.
- Base: `gcr.io/distroless/static-debian13:nonroot`. The image holds one file, `/pandokos`. No shell
  and no package manager.
- Runs as user `65532:65532`. The process writes nothing to disk, so the root file system can be
  read-only and all capabilities can be dropped.
- `ENTRYPOINT ["/pandokos"]`, `CMD ["serve"]`, `EXPOSE 8080`.

## Starting it

Put the secrets in files: the access token (at least 32 bytes, different from the provider key),
the provider key, and the passwords of the database users. Starting takes two commands, because
the schema is created by `migrate` and `serve` refuses to start against a schema it does not
match:

```
docker run --rm --read-only --cap-drop ALL --security-opt no-new-privileges \
  -v "$PWD/secrets:/run/secrets:ro" \
  -e PANDOKOS_DATABASE_ADDRESS=db.internal:5432 \
  -e PANDOKOS_DATABASE_OWNER_USER=acme_owner \
  -e PANDOKOS_DATABASE_OWNER_PASSWORD_FILE=/run/secrets/db_owner \
  ghcr.io/pandokos/pandokos:<version> migrate

docker run --rm --read-only --cap-drop ALL --security-opt no-new-privileges \
  -p 8080:8080 \
  -v "$PWD/secrets:/run/secrets:ro" \
  -e PANDOKOS_DATABASE_ADDRESS=db.internal:5432 \
  -e PANDOKOS_DATABASE_GATEWAY_USER=acme_gateway \
  -e PANDOKOS_DATABASE_GATEWAY_PASSWORD_FILE=/run/secrets/db_gateway \
  -e PANDOKOS_DATABASE_ADMIN_USER=acme_admin \
  -e PANDOKOS_DATABASE_ADMIN_PASSWORD_FILE=/run/secrets/db_admin \
  -e PANDOKOS_DATABASE_WORKER_USER=acme_worker \
  -e PANDOKOS_DATABASE_WORKER_PASSWORD_FILE=/run/secrets/db_worker \
  -e PANDOKOS_SKELETON_ACCESS_TOKEN_FILE=/run/secrets/access_token \
  -e PANDOKOS_SKELETON_OPENAI_API_KEY_FILE=/run/secrets/openai_api_key \
  ghcr.io/pandokos/pandokos:<version>
```

`serve` never reads the owner's password file. TLS towards the database is on by default
(`database.tls.mode`); see the database guide to turn it off for a database on a private network.

The secret files must be readable by user 65532. All settings are described in the
[configuration reference](configuration.md); `docker run <image> version` prints the version.

## Endpoints

| Endpoint | Purpose |
|---|---|
| `GET /healthz` | Liveness: 200 whenever the process answers |
| `GET /readyz` | Readiness: 200 when the listener is bound, the process is not draining and every registered check passes, including one check per database role (`database-gateway`, `database-admin`, `database-worker`: the database answers and its schema still matches the binary); otherwise 503 |
| `POST /openai/v1/chat/completions` | OpenAI chat completions, streaming and not streaming |

Any other path or method gets 404 `endpoint_not_supported`. A caller presents the static access
token as `Authorization: Bearer <token>`. An SDK needs only a different base URL and key:

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:8080/openai/v1", api_key="<access token>")
```

## What Pandokos changes

Provider responses pass through unchanged: status, body and an allow-list of headers
(`Content-Type`, `Content-Length`, `Retry-After`, `X-Request-Id`, `X-Ratelimit-*`, `Openai-*`),
for every status including errors ([ADR 0009](../adr/0009-native-passthrough-by-api-family.md)).
Towards the provider only `Accept`, `Content-Type` and `User-Agent` are forwarded, with the
provider key in `Authorization`; the query string is not forwarded and redirects are not followed.

For a streamed request that does not ask for usage, Pandokos adds `stream_options.include_usage`
by splicing bytes into the body (it is never re-serialized) and removes the usage-only event from
the response. The provider also adds `"usage":null` to every other chunk when that option is on;
that member is left in place. If the caller asked for usage itself, nothing is added or removed
([ADR 0024](../adr/0024-openai-stream-usage-option.md)).

Pandokos adds the header `X-Pandokos-Request-Id` to every response of the proxied route and to every error it raises (the health endpoints carry none). Provider informational (1xx) responses and trailers are dropped, so no provider header reaches the caller outside the allow-list.

## Errors raised by Pandokos

The body is in the OpenAI error shape, with `"type":"pandokos_error"` and `code` equal to the
header `X-Pandokos-Error` ([ADR 0020](../adr/0020-error-contract.md)).

| Condition | Status | `X-Pandokos-Error` |
|---|---|---|
| Path or method not supported | 404 | `endpoint_not_supported` |
| Missing, malformed or wrong access token | 401 | `unauthenticated` |
| Body over `limits.max_request_body_bytes` | 413 | `request_too_large` |
| Provider not reachable | 502 | `upstream_unreachable` |
| `limits.max_request_duration` elapsed before the provider answered | 504 | `upstream_timeout` |
| Unexpected failure before anything was sent | 500 | `internal` |

When the request body is never finished, the caller disconnects, or a stream is cut by timeout,
provider failure or forced shutdown, the connection is closed without a response.

## Logs

One JSON object per line on standard output. Nothing is logged from request or response content;
header values, bodies and query strings never appear. Values of sensitive keys are replaced by
`[REDACTED]`. Each request produces one `request` line:

| Field | Present | Meaning |
|---|---|---|
| `request_id` | always | 32 hexadecimal characters; the same value as the response header wherever that header is sent (not on the health endpoints) |
| `method` | always | the HTTP method, or `OTHER` |
| `route` | always | `POST /openai/v1/chat/completions`, `GET /healthz`, `GET /readyz` or `unmatched` |
| `status` | always | status sent to the caller, 0 if none |
| `outcome` | always | `ok`, `pandokos_error`, `client_cancelled`, `client_error`, `timeout`, `upstream_aborted`, `shutdown_aborted` |
| `error_code` | when Pandokos raised the error | the `X-Pandokos-Error` code |
| `duration_ms` | always | wall time of the handler |
| `provider`, `operation` | proxied route | `openai`, `chat.completions` |
| `stream`, `usage_injected` | after the body was read | whether a stream was asked for, and whether Pandokos added the usage option |
| `upstream_status`, `ttfb_ms` | when the provider answered | provider status, and time to its response headers |
| `request_bytes`, `response_bytes` | when known | body bytes received and sent |
| `usage_source` | after the body was read | `provider` or `none` |
| `usage` | when `usage_source` is `provider` | `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`, `reasoning_tokens`, plus one entry per non-zero provider-specific category |

Health requests are logged at level debug, everything else at info.

## Timeouts

| Key | What it bounds |
|---|---|
| `listeners.inference.read_header_timeout` | reading request headers |
| `limits.request_body_read_timeout` | reading the request body |
| `limits.max_request_duration` | one proxied request, stream included |
| `upstream.connect_timeout` | TCP connect and TLS handshake towards the provider |
| `listeners.inference.idle_timeout` | idle keep-alive connections |
| `shutdown.drain_timeout` | draining at shutdown |

## Shutdown

On SIGTERM or SIGINT readiness goes to 503, the listener closes and in-flight requests, streams
included, continue until they finish or `shutdown.drain_timeout` passes. Requests still running
then are cancelled, which releases their provider connections. The process exits with code 0. A
second signal during the drain ends it at once.

## Verifying a release

Releases are built only by the release workflow. Each image is signed with cosign (keyless) and
carries an SPDX SBOM and a SLSA provenance attestation.

```
cosign verify ghcr.io/pandokos/pandokos:<version> \
  --certificate-identity "https://github.com/pandokos/pandokos/.github/workflows/release.yml@refs/tags/v<version>" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

cosign verify-attestation ghcr.io/pandokos/pandokos:<version> --type spdxjson \
  --certificate-identity "https://github.com/pandokos/pandokos/.github/workflows/release.yml@refs/tags/v<version>" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

gh attestation verify oci://ghcr.io/pandokos/pandokos:<version> --repo pandokos/pandokos
```

The SBOM is generated from the amd64 variant and attested against the image index digest; the Go
dependencies are the same on both platforms.

## Limits of this pre-release

One static access token shared by all callers, no rotation; plain HTTP, so terminate TLS in front
of it; one endpoint; the database holds tenants and service accounts with tested isolation, but
nothing is proxied through it yet. Do not expose it to the internet and do not use it for
production.
