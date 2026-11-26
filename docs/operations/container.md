# Running the container image

This page runs the Pandokos image. One container serves the inference listener, the administration
API and the command line of `pandokos`, for several tenants: callers present virtual keys or signed
tokens, people and automation use the administration API, and each tenant's provider credentials are
stored sealed in the database under a key-encryption key ([gateway guide](gateway.md),
[vault guide](vault.md)). Both listeners can serve TLS ([TLS guide](tls.md)). The image needs a
PostgreSQL 16 or later database, and nothing else: see the [database guide](database.md) to prepare it. What
is not built yet is in [Limits of this pre-release](#limits-of-this-pre-release); the
[Compose reference](compose.md) is the quickest first trial.

## The image

- Name: `ghcr.io/pandokos/pandokos`. Tags on release: the version without `v` (`0.3.0`); for a
  release that is not a pre-release also `0.3` and `latest`.
- Platforms: `linux/amd64` and `linux/arm64`, one image index.
- Base: `gcr.io/distroless/static-debian13:nonroot`. The image holds one file, `/pandokos`. No shell
  and no package manager.
- Runs as user `65532:65532`. The process writes nothing to disk, so the root file system can be
  read-only and all capabilities can be dropped.
- `ENTRYPOINT ["/pandokos"]`, `CMD ["serve"]`, `EXPOSE 8080`.

## Starting it

Put the secrets in files: the key-encryption key and the passwords of the database users. Starting takes two commands, because
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

To keep the key in OpenBao or HashiCorp Vault instead, set `PANDOKOS_VAULT_KEK_PROVIDER=transit` and the
`PANDOKOS_VAULT_KEK_TRANSIT_*` variables, and mount the token (or the role ID and secret ID) as files; no key
file is needed. The image does not change and no overlay is provided; the commands to prepare the service, the
policy and the move are in the [external key guide](external-key.md). A container that waits for the service at
start listens on nothing until it answers, so give its health check a start-up allowance.

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
| `POST /anthropic/v1/messages` | Anthropic messages, streaming and not streaming (provider `anthropic`) |
| `POST /anthropic/v1/messages/count_tokens` | Anthropic token counting; recorded without usage |

The path after the first segment is the provider's own, for any provider of the catalogue (`/<provider>/v1/...`).

Any other path or method gets 404 `endpoint_not_supported`. A caller presents a virtual key (or a
signed token, see the [tokens guide](tokens.md)) in the header its SDK uses for the provider's own
key ([gateway guide](gateway.md), section 1). An SDK needs only a different base URL and key:

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:8080/openai/v1", api_key="<virtual key>")
```

## What Pandokos changes

Provider responses pass through unchanged: status, body and an allow-list of headers per family
(`openai`: `Content-Type`, `Content-Length`, `Retry-After`, `X-Request-Id`, `X-Ratelimit-*`, `Openai-*`;
`anthropic`: `Content-Type`, `Content-Length`, `Retry-After`, `Request-Id`, `X-Should-Retry`, `Anthropic-*`),
for every status including errors ([ADR 0009](../adr/0009-native-passthrough-by-api-family.md)).
Towards the provider only the headers on the allow-list of the family are forwarded (`openai`: `Accept`,
`Content-Type`, `User-Agent`, `OpenAI-Organization`, `OpenAI-Project`, `OpenAI-Beta`; `anthropic`: `Accept`,
`Content-Type`, `User-Agent`, `Anthropic-Version`, `Anthropic-Beta`; the lists are `RequestHeaders` in the
`route.go` of each family), with the tenant's provider credential in the header the provider reads; the query string is not forwarded and redirects are not
followed.

For a streamed request that does not ask for usage, Pandokos adds `stream_options.include_usage`
by splicing bytes into the body (it is never re-serialized) and removes the usage-only event from
the response, unless the provider's `stream_usage_option` is `omit` ([catalogue guide](catalogue.md)). The `anthropic` family changes nothing but the model name. The provider also adds `"usage":null` to every other chunk when that option is on;
that member is left in place. If the caller asked for usage itself, nothing is added or removed
([ADR 0024](../adr/0024-openai-stream-usage-option.md)).

Pandokos adds the header `X-Pandokos-Request-Id` to every response of the proxied route and to every error it raises (the health endpoints carry none). Provider informational (1xx) responses and trailers are dropped, so no provider header reaches the caller outside the allow-list.

## Errors raised by Pandokos

The body is in the OpenAI error shape, with `"type":"pandokos_error"` and `code` equal to the
header `X-Pandokos-Error` ([ADR 0020](../adr/0020-error-contract.md)).

| Condition | Status | `X-Pandokos-Error` |
|---|---|---|
| Path or method not supported | 404 | `endpoint_not_supported` |
| Missing, malformed or wrong credential | 401 | `unauthenticated` |
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
| `provider` | proxied route | the provider segment of the route |
| `endpoint` | the model resolved | the endpoint of the catalogue the request was sent to |
| `stream`, `usage_injected` | after the body was read | whether a stream was asked for, and whether Pandokos added the usage option |
| `upstream_status` | when the provider answered | provider status |
| `ttfb_ms` | when a body byte arrived | time to the first byte of the response body |

The line names no caller, tenant, key, model, credential, size, usage, cost or limit rule: those are
the tenant's and are in its usage record ([ADR 0055](../adr/0055-operational-telemetry-holds-no-tenant-data.md)).
To follow one request, ask the tenant for its `X-Pandokos-Request-Id` and search the log for that
`request_id`. The other fields of the line are in the [gateway guide](gateway.md), section 10.

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
under `ledger.shutdown_flush_timeout` (default 10 s); records that cannot be written by then are lost,
and one line, `ledger closed with records lost`, gives their number (see the [usage guide](usage.md)). The process exits with code 0
within `shutdown.drain_timeout` plus, when requests were still running at the end of the drain, up to
5 s for the cancelled handlers to record, plus `ledger.shutdown_flush_timeout`, plus about a second.
**Give the container a stop grace period at least that long** plus a margin (worst case with the
defaults: 30 s + 5 s + 10 s + 1 s = 46 s: use
`docker stop -t 60`, `stop_grace_period: 60s` in Compose or `terminationGracePeriodSeconds: 60`);
otherwise the runtime kills the process and the records in memory are lost. A second signal during
the drain ends the process at once and loses them too.

## Verifying a release

Releases are built only by the release workflow, from an annotated tag on `main` or a release
branch (the maintainer signs the tag; the workflow checks that it is annotated and that its commit is on one of those branches). Each release publishes one image index and its digest. The artefacts are the image, its
SPDX SBOM and the API description; no binary is released. The image is signed with cosign
(keyless), and carries an SPDX SBOM and a SLSA provenance attestation. All of them are made on the
digest, not on a tag: a tag can be moved after the release, a digest cannot.

**Take the digest from the release page.** The page of each release on GitHub states
`Image: ghcr.io/pandokos/pandokos@sha256:<digest>`. Use that value. If you start from a tag, resolve it
and compare the result with the release page before you trust it:

```
docker buildx imagetools inspect ghcr.io/pandokos/pandokos:<version> --format '{{.Manifest.Digest}}'
```

Then verify the signature, the SBOM attestation and the build provenance of that digest:

```
cosign verify ghcr.io/pandokos/pandokos@sha256:<digest> \
  --certificate-identity "https://github.com/pandokos/pandokos/.github/workflows/release.yml@refs/tags/v<version>" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

cosign verify-attestation ghcr.io/pandokos/pandokos@sha256:<digest> --type spdxjson \
  --certificate-identity "https://github.com/pandokos/pandokos/.github/workflows/release.yml@refs/tags/v<version>" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

gh attestation verify oci://ghcr.io/pandokos/pandokos@sha256:<digest> --repo pandokos/pandokos
```

**Run by digest in production.** Name the image `ghcr.io/pandokos/pandokos@sha256:<digest>` in the
container command, the Compose file or the manifest, so that what runs is what you verified. The
tags (`0.3.0`, `0.3`, `latest`) are for a first trial: `0.3` moves at each patch release, which is
what the [Compose reference](compose.md) follows on purpose.

**The attested SBOM is the authoritative one.** The SBOM file on the release page is an unsigned copy
of it. To print the signed one, take the payload of the verified attestation (the second command above,
with its output piped to `jq`):

```
cosign verify-attestation ghcr.io/pandokos/pandokos@sha256:<digest> --type spdxjson \
  --certificate-identity "https://github.com/pandokos/pandokos/.github/workflows/release.yml@refs/tags/v<version>" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com | jq -r '.payload | @base64d | fromjson | .predicate'
```

The SBOM is generated from the amd64 variant and attested against the image index digest; the Go
dependencies are the same on both platforms.

**What the pipeline verified before it published.** The workflow refuses a tag that is not an
annotated tag, runs the whole test suite, builds the image, and then checks the pushed digest: the
signature, the SBOM attestation, the provenance, both platforms, the version the binary reports, and a
scan of that digest (a fixed vulnerability of severity high or above fails the release). The release
page is created only after all of them passed. The same scan runs again every night
against the image that `latest` names, together with a check of the Go vulnerability database over the
repository, so that a vulnerability published after a release is seen without a new push.

## Limits of this pre-release

The inference listener forwards chat completions, responses and embeddings of the `openai` family and messages of the `anthropic` family with the tenant's stored
credential; see the [gateway guide](gateway.md) for what a call needs and what reaches the provider. Both
listeners serve TLS when a certificate is configured ([TLS guide](tls.md)) and plain HTTP otherwise, and
then TLS has to end in front of them. It writes a usage record for every authenticated request ([usage guide](usage.md)) and exports traces when a collector is configured ([telemetry guide](telemetry.md)). It has no retries towards the provider, and the limits of a tenant are
counted per instance until the shared store of a later release exists ([limits guide](limits.md)). There is no
chart for Kubernetes yet. The [Compose reference](compose.md) runs one instance with PostgreSQL and TLS for a first trial. The administration API is not meant for the public internet; read
the [security model](../architecture/security-model.md) before you expose either listener.
