# Docker Compose reference

A reference deployment of Pandokos with Docker Compose: one instance and PostgreSQL 16, both with
TLS, on one host. It is the shortest way to see a streamed call to OpenAI and to Anthropic go through
Pandokos with a tenant's own key, and to read the usage record it leaves. It is **not** a production
deployment: one instance, no backups of the key-encryption key, a self-signed certificate and one host
(requirement OPS-02; the Kubernetes and multi-instance topologies come later).

The files are in `deploy/compose/`:

| File | Role |
|---|---|
| `compose.yaml` | the services `postgres`, `migrate` (runs once) and `pandokos` |
| `setup.sh` | creates `./secrets`: five database passwords, the key-encryption key (32 random bytes, base64) and a self-signed TLS certificate and key (P-256, one year, names `localhost`, `pandokos`, `postgres`, `127.0.0.1`, `::1`) with `openssl`; never overwrites a file |
| `initdb/10-pandokos.sh` | on the first start of the data directory: the privilege roles of `deploy/postgres/roles.sql` as login users with the generated passwords, and the database (the "Shortest" setup of the [database guide](database.md)) |
| `quickstart.sh` | the quick start below in one guided run: `setup.sh`, `docker compose up`, the tenant `demo` with an OpenAI credential and a virtual key written to `quickstart.key` (mode 0600). It explains each step, waits for Enter on a terminal (`--yes` does not), skips what is done, takes the OpenAI key from `OPENAI_API_KEY` or asks for it without echo |
| `.gitignore` | `secrets/`, `quickstart.key` |

## Prerequisites

- Docker with Compose v2, and `openssl` on the host.
- The Pandokos image. The default `ghcr.io/pandokos/pandokos:0.1` follows the latest patch release of the minor version
  (today `v0.1.1`): the release workflow moves `0.1` and `latest` at every release that is not a
  pre-release. To run exactly one release, set `PANDOKOS_IMAGE=ghcr.io/pandokos/pandokos:0.1.1`. To run your own
  build instead, build it with `make image` in the repository and prefix the `docker compose` commands
  with `PANDOKOS_IMAGE=pandokos:dev`.
- A provider key for each provider you want to call (OpenAI, Anthropic).

## Quick start

`./quickstart.sh` does the steps of this section for OpenAI and prints the call to make; the README
starts from it. By hand, for both providers:

```sh
cd deploy/compose
./setup.sh
docker compose up -d --wait
curl --cacert secrets/tls_cert https://localhost:8080/readyz    # repeat until 200
```

`docker compose up -d --wait` returns once PostgreSQL is healthy, `migrate` has finished and
`pandokos` runs. Then create the first platform key and the first administrator (a person, with a
generated password that must be changed at the first sign-in), a tenant and a key for an
application. The CLI runs inside the container, where `PANDOKOS_CLI_SERVER` and
`PANDOKOS_CLI_CA_FILE` are already set:

```sh
docker compose exec pandokos /pandokos init --key-out /home/nonroot/bootstrap.key \
  --admin-username admin --password-out /home/nonroot/admin.password
docker compose exec -e PANDOKOS_CLI_KEY_FILE=/home/nonroot/bootstrap.key pandokos \
  /pandokos tenant create demo --admin-key-out /home/nonroot/demo-admin.key
pdk() { docker compose exec -T -e PANDOKOS_CLI_KEY_FILE=/home/nonroot/demo-admin.key pandokos /pandokos "$@"; }
printf %s "$OPENAI_API_KEY"    | pdk credential create --provider openai --secret-file /dev/stdin openai-main
printf %s "$ANTHROPIC_API_KEY" | pdk credential create --provider anthropic --secret-file /dev/stdin anthropic-main
pdk service-account create --role inference app
export KEY=$(pdk key create --service-account app | awk '/^key:/ {print $2}')
pdk capability check --provider anthropic --model claude-sonnet-4-5
```

The provider keys go through standard input and are never written to disk by the reference. The
capability check answers whether the tenant can call that provider and model now; it reads
configuration and credential metadata only (never the key), so it does not prove that the provider
accepts the key (see the [administration API reference](../reference/admin-api.md)).

`key create` prints the new key as its last line (`key:` and the value) after the key's metadata;
`awk` keeps the value alone. The key is shown only once.

`--tenant` is left out because the demo-admin key belongs to the tenant and acts on it; resolving a
tenant by name needs a platform key.

Call both families with the generated certificate as the trusted authority. Python:

```python
# stream_anthropic.py: run with SSL_CERT_FILE=$PWD/secrets/tls_cert python stream_anthropic.py
import os
from anthropic import Anthropic

client = Anthropic(base_url="https://localhost:8080/anthropic", api_key=os.environ["KEY"])
with client.messages.stream(model="claude-sonnet-4-5", max_tokens=64,
                            messages=[{"role": "user", "content": "Say hello."}]) as stream:
    for text in stream.text_stream:
        print(text, end="", flush=True)
```

```python
# stream_openai.py: run with SSL_CERT_FILE=$PWD/secrets/tls_cert python stream_openai.py
import os
from openai import OpenAI

client = OpenAI(base_url="https://localhost:8080/openai/v1", api_key=os.environ["KEY"])
for chunk in client.chat.completions.create(model="gpt-4o-mini", stream=True,
                                            messages=[{"role": "user", "content": "Say hello."}]):
    if chunk.choices:
        print(chunk.choices[0].delta.content or "", end="", flush=True)
```

```sh
KEY=$KEY SSL_CERT_FILE=$PWD/secrets/tls_cert python stream_anthropic.py
KEY=$KEY SSL_CERT_FILE=$PWD/secrets/tls_cert python stream_openai.py
```

TypeScript is the same with `NODE_EXTRA_CA_CERTS=$PWD/secrets/tls_cert node stream.mjs`, the same
base URLs (`@anthropic-ai/sdk` and `openai`) and `apiKey` set to the key. With curl, pass
`--cacert secrets/tls_cert`:

```sh
curl --cacert secrets/tls_cert https://localhost:8080/anthropic/v1/messages \
  -H "X-Api-Key: $KEY" -H "Anthropic-Version: 2026-10-21" -H "Content-Type: application/json" \
  -d '{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"user","content":"Say hello."}]}'
```

## Reading the usage records

Every authenticated call leaves one record without content ([usage guide](usage.md)). List them:

```sh
pdk usage list
```

A cut Anthropic stream, for example, shows the provider's input figures and an estimated output. The
capability check and the list need no SQL and no database password.

## The observability overlay

`deploy/compose/observability/` adds an OpenTelemetry Collector, Prometheus and Grafana to the
reference deployment, with two dashboards, to look at the [metrics](telemetry.md#9-metrics). It is an
example for trying and for learning what the metrics show, not a hardened monitoring stack: it has
no authentication of its own beyond Grafana's anonymous viewer, no alerting and two days of retention.

```sh
./setup.sh        # also creates secrets/grafana_admin; never overwrites a secret that exists
docker compose -f compose.yaml -f observability/compose.yaml up -d --wait
```

- **Ports.** Grafana at `http://127.0.0.1:3000` and Prometheus at `http://127.0.0.1:9090`, on the
  host's loopback only. The collector publishes nothing.
- **What it does.** `pandokos` gets three settings: the collector as `telemetry.otlp.endpoint`, an
  export interval of 10 s and `telemetry.metrics.prometheus`. The collector, which is on the `egress`
  network so that Pandokos reaches it through the egress guard, forwards the metrics to Prometheus and
  writes a summary of the traces to its own log. Prometheus and Grafana are on a network marked
  `internal` that no one else joins; they also join a second network because Docker publishes a port
  only for a container with a route out.
- **Secrets.** The admin password of Grafana is `secrets/grafana_admin`, made by `setup.sh` like the
  others. Anonymous visitors are viewers.
- **Images** are pinned by digest, run with a read-only root file system, no capabilities and
  `no-new-privileges`; Prometheus and Grafana write to the named volumes `prometheus-data` and
  `grafana-data`.
- **Stop.** `docker compose -f compose.yaml -f observability/compose.yaml down` (add `-v` to remove the
  volumes too). Stopping the collector only is harmless: Pandokos logs `metric export failing` and goes
  on.

## What the choices are, and why

| Concern | Choice |
|---|---|
| Listeners | `0.0.0.0:8080` (inference) and `0.0.0.0:8081` (administration) inside the container, both with TLS from the generated certificate, published on `127.0.0.1` only. Do not set `allow_plaintext` |
| Database | PostgreSQL 16, pinned by digest, on a network marked `internal` (no route out, not published); TLS on and `database.tls.mode: verify-full` with the generated certificate as the authority |
| Egress | `pandokos` is also on a second, ordinary network, to reach the providers; `migrate` and PostgreSQL are not |
| Hardening | `read_only`, `cap_drop: [ALL]`, `no-new-privileges`, the image's user (65532); secrets are files under `/run/secrets`, never environment values |
| State | the volumes `pgdata` (the database) and `cli` (the home of the image user, where the CLI writes keys) |
| Order | `migrate` after PostgreSQL is healthy; `pandokos` after `migrate` finished successfully |
| Stop | `stop_grace_period: 45s`: the drain of 30 s plus the flush of the usage ledger |

The secret files are mode 0444 in a directory of mode 0700, because the containers run as other users
(`postgres` 70, `pandokos` 65532) and read them through bind mounts. The vault therefore logs the
expected `configuration warning` about the permissions of `vault.kek.local.key_file`
([vault guide](vault.md)); anyone who can read the directory as the host's owner can read the key.

## Operating it

- **Replace the certificate.** The generated one is self-signed and for these names. To serve other
  hosts, replace `secrets/tls_cert` and `secrets/tls_key` with a certificate of a real authority, publish
  the ports on another address in `compose.yaml` and restart. Clients then need no `SSL_CERT_FILE`.
- **Back up.** The `pgdata` volume holds the database; `secrets/pandokos_kek` is the key that opens the
  credentials in it. Keep the key apart from the database backup ([vault guide](vault.md)): the
  database without the key cannot be read, and the key without the database is useless. Losing the key
  loses every stored credential.
- **Upgrade.** Use the new image (`PANDOKOS_IMAGE`) and run `docker compose up -d --wait`: `migrate`
  runs first and `pandokos` starts after it.
- **Stop.** `docker compose stop` drains requests and writes the ledger; `docker compose start` resumes.
- **Remove everything.** `docker compose down -v` removes the containers and the volumes: the
  database and the CLI keys are destroyed.

Compose, PostgreSQL and the images are pinned or updated by Dependabot; the Compose reference is
exercised by CI (`go test -tags compose ./test/compose/`, which needs `PANDOKOS_TEST_IMAGE`).
