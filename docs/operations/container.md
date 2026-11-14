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
  -e PANDOKOS_VAULT_KEK_LOCAL_KEY_FILE=/run/secrets/pandokos_kek \
  ghcr.io/pandokos/pandokos:<version>
```

`serve` never reads the owner's password file.

**The key-encryption key.** `serve` needs the key file of the vault ([vault guide](vault.md)): create it
once with `pandokos kek generate --out ./secrets/pandokos_kek` and mount it like the other secrets. The
image runs as user 65532, so the file must be readable by that user: mode 0444 in a read-only mount
that only you can reach, or mode 0440 with the group set through `--group-add` or `fsGroup`. Any
permission for other users makes `serve` log the warning `configuration warning` for
`vault.kek.local.key_file`; it still starts. The key is never in the image and never in a
configuration file. Keep a copy of it separately from the database backups.

## Prices at start

At its first start against a database, and whenever the image carries a newer price list than the one
last imported, `serve` imports the price list embedded in the image (a snapshot of the `genai-prices`
dataset, see the [catalogue guide](catalogue.md)) and logs `prices imported` with `source` `embedded`.
It needs no network access and no setting. It never replaces a newer file or download, and a failure
only logs `price import failed`; the server starts either way. Outbound access from the container is
needed only when `pricing.refresh.enabled` is true: then `serve` downloads the dataset from
`pricing.refresh.url` over HTTPS, without proxy. Without it the container makes no outbound request
except to the providers and the database. Set `pricing.import_at_start` to `false` to import nothing at
start.

## The administration API

The administration API listens on port 8081. By default it listens on `127.0.0.1` **inside the
container**, so it is not reachable from outside, and `docker exec` is the way in:

```
docker run --rm ... ghcr.io/pandokos/pandokos:<version> init          # the first key, once
docker exec <container> /pandokos tenant create acme                   # with PANDOKOS_CLI_KEY_FILE set
```

`init` runs with the same environment as `serve` (it needs the admin database password) and
prints the bootstrap key once; the API commands read the key from a mounted file
(`PANDOKOS_CLI_KEY_FILE`). To expose the API, set `PANDOKOS_LISTENERS_ADMIN_ADDRESS=0.0.0.0:8081`
and either TLS (`listeners.admin.tls.cert_file` and `key_file`, see [TLS](tls.md)) or, when TLS
ends in front of it, `PANDOKOS_LISTENERS_ADMIN_ALLOW_PLAINTEXT=true`. The image declares
`EXPOSE 8080 8081`. TLS towards the database is on by default
(`database.tls.mode`); see the database guide to turn it off for a database on a private network.

The secret files must be readable by user 65532. All settings are described in the
[configuration reference](configuration.md); `docker run <image> version` prints the version.

## Endpoints

| Endpoint | Purpose |
|---|---|
| `GET /healthz` | Liveness: 200 whenever the process answers |
| `GET /readyz` | Readiness: 200 when the listener is bound, the process is not draining and every registered check passes, including one check per database role (`database-gateway`, `database-admin`, `database-worker`: the database answers and its schema still matches the binary) and the check `ledger` (fails with `ledger: buffer full` while the usage ledger buffer is full, see the [usage guide](usage.md)); otherwise 503 |
| `POST /openai/v1/chat/completions` | OpenAI chat completions, streaming and not streaming |
| `POST /openai/v1/responses` | OpenAI responses, streaming and not streaming; background processing is refused |
| `POST /openai/v1/embeddings` | OpenAI embeddings |

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
| `route` | always | `POST /openai/v1/chat/completions` (or the route of another operation), `GET /healthz`, `GET /readyz` or `unmatched` |
| `status` | always | status sent to the caller, 0 if none |
| `outcome` | always | `ok`, `pandokos_error`, `client_cancelled`, `client_error`, `timeout`, `upstream_aborted`, `shutdown_aborted` |
| `error_code` | when Pandokos raised the error | the `X-Pandokos-Error` code |
| `duration_ms` | always | wall time of the handler |
| `provider`, `operation` | proxied route | `openai`, `chat.completions`, `responses` or `embeddings` |
| `stream`, `usage_injected` | after the body was read | whether a stream was asked for, and whether Pandokos added the usage option |
| `upstream_status` | when the provider answered | provider status |
| `ttfb_ms` | when a body byte arrived | time to the first byte of the response body |
| `request_bytes`, `response_bytes` | when known | body bytes received and sent |
| `usage_source` | when the provider answered | `provider`, `estimated` or `none` |
| `usage` | when `usage_source` is `provider` | `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`, `reasoning_tokens`, plus one entry per non-zero provider-specific category |

The usage record adds `record_id`, `trace_id`, `cost_status`, `cost`, `currency`, `metadata_count`, `metadata_dropped`, `usage_reader` and `ledger` (see the [gateway guide](gateway.md), section 10).

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
| `ledger.shutdown_flush_timeout` | writing the usage records still in memory, after the drain |

## Shutdown

On SIGTERM or SIGINT readiness goes to 503, the listener closes and in-flight requests, streams
included, continue until they finish or `shutdown.drain_timeout` passes. Requests still running
then are cancelled, which releases their provider connections. The usage records of all requests,
the cancelled ones included, are then written to the database and the traces are flushed, together
under `ledger.shutdown_flush_timeout` (default 10 s); records that cannot be written are logged in
full (`usage record not written`, see the [usage guide](usage.md)). The process exits with code 0
within `shutdown.drain_timeout` plus, when requests were still running at the end of the drain, up to
5 s for the cancelled handlers to record, plus `ledger.shutdown_flush_timeout`, plus about a second.
**Give the container a stop grace period at least that long** plus a margin (worst case with the
defaults: 30 s + 5 s + 10 s + 1 s = 46 s: use
`docker stop -t 60`, `stop_grace_period: 60s` in Compose or `terminationGracePeriodSeconds: 60`);
otherwise the runtime kills the process and the records in memory are lost. A second signal during
the drain ends the process at once and loses them too.

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

The inference listener forwards chat completions, responses and embeddings of the `openai` family with the tenant's stored
credential; see the [gateway guide](gateway.md) for what a call needs and what reaches the provider. It
speaks plain HTTP, so terminate TLS in front of it. It writes a usage record for every authenticated request ([usage guide](usage.md)) and exports traces when a collector is configured ([telemetry guide](telemetry.md)). It has no retries. Do not
expose it to the internet and do not use it for production.
