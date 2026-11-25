# Docker Compose reference

A reference deployment of Pandokos with Docker Compose: one instance and PostgreSQL 18, both with
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
- The Pandokos image. The default `ghcr.io/pandokos/pandokos:0.2` follows the latest patch release of the minor version
  (today `v0.2.0`): the release workflow moves `0.2` and `latest` at every release that is not a
  pre-release. The reference follows the minor tag on purpose, so that a first trial gets the patches; production pins the digest printed on the release page (`PANDOKOS_IMAGE=ghcr.io/pandokos/pandokos@sha256:<digest>`, after the verification of the [container guide](container.md#verifying-a-release)). To run exactly one release by its tag, set `PANDOKOS_IMAGE=ghcr.io/pandokos/pandokos:0.2.0`. To run your own
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
| Database | PostgreSQL 18, pinned by digest, on a network marked `internal` (no route out, not published); TLS on and `database.tls.mode: verify-full` with the generated certificate as the authority |
| Egress | `pandokos` is also on a second, ordinary network, to reach the providers; `migrate` and PostgreSQL are not |
| Hardening | `read_only`, `cap_drop: [ALL]`, `no-new-privileges`, the image's user (65532); secrets are files under `/run/secrets`, never environment values |
| State | the volumes `pgdata` (the database, mounted at `/var/lib/postgresql` as the PostgreSQL 18 image wants) and `cli` (the home of the image user, where the CLI writes keys) |
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
- **Upgrade.** From an installation that runs PostgreSQL 16, first [move the database to 18](#moving-an-installation-from-postgresql-16-to-18). Use the new image (`PANDOKOS_IMAGE`) and run `docker compose up -d --wait`: `migrate`
  runs first and `pandokos` starts after it.
- **Stop.** `docker compose stop` drains requests and writes the ledger; `docker compose start` resumes.
- **Remove everything.** `docker compose down -v` removes the containers and the volumes: the
  database and the CLI keys are destroyed.

- **A key service instead of the key file.** The Compose files run no key service: a development-mode
  OpenBao or HashiCorp Vault loses its keys at a restart, which is the worst example to ship. To use one you
  run, set the `vault.kek.transit.*` settings and mount the token files; the steps are in the [external key
  guide](external-key.md).

Compose, PostgreSQL and the images are pinned or updated by Dependabot; the Compose reference is
exercised by CI (`go test -tags compose ./test/compose/`, which needs `PANDOKOS_TEST_IMAGE`).

## Upgrading

An installation moves to a newer version by stop, migrate, start: back up, `docker compose stop`, run `migrate` of the new image once, `docker compose up -d`. The procedure, the supported upgrades, the measured interval and the way back are in the [upgrade guide](upgrade.md).

## Moving an installation from PostgreSQL 16 to 18

Releases before v0.3.0 ran the reference deployment on PostgreSQL 16; this one runs it on 18. PostgreSQL 16 stays
supported by Pandokos (the minimum is 16, [database guide](database.md)): the move is only for this reference
deployment.

> **Warning.** Do not pull the new `compose.yaml` and run `docker compose up` on an installation that has a
> `pgdata` volume made by PostgreSQL 16. The data directory of one major version cannot be opened by another, and
> the 18 image mounts the volume one level up (`/var/lib/postgresql`, not `/var/lib/postgresql/data`): it finds
> the old data there, refuses to start (the log of `postgres` says so), and `docker compose up --wait` fails. Nothing is lost, because nothing is written; put
> the old file back and the installation starts as it was. Do the steps below instead, in order, **with the
> old `compose.yaml` still in place until step 4**.

It is a logical dump with the 16 server and a restore into a new 18 server: the restore of a dump into a later
major is the case the restore drill ran and verified, with both audit chains
(`TestRestoreOnLaterMajor`, [drill record](restore-drill-2026-11.md); a physical backup cannot be restored on
another major). The commands run in `deploy/compose`, and the dump and restore follow the
[backup and restore guide](backup-and-restore.md) (steps 1.a, 4.2 and 4.6). Expect an interval without service
that grows with the size of the database.

1. **Keep the audit heads.** While the installation runs, record the head of every tenant's chain and of the
   platform's, with their `seq` (the commands of the [audit guide](audit.md#7-anchoring-the-head-elsewhere); the
   platform's chain with the bootstrap key of the quick start, a tenant's with the key of its administrator,
   `demo-admin.key` for the tenant of the quick start):

   ```sh
   docker compose exec -e PANDOKOS_CLI_KEY_FILE=/home/nonroot/bootstrap.key pandokos /pandokos platform-audit head -o json
   docker compose exec -e PANDOKOS_CLI_KEY_FILE=/home/nonroot/demo-admin.key pandokos /pandokos audit head --tenant <tenant ID> -o json
   ```

   Also keep `secrets/` (it is not touched by the move; `pandokos_kek` is still the key of the credentials) and
   note the version of the Pandokos image.
2. **Stop Pandokos and dump with the 16 server**, which keeps running:

   ```sh
   docker compose stop pandokos
   docker compose exec -T postgres pg_dump -U postgres -d pandokos -Fc > pandokos-pg16.dump
   docker compose exec -T postgres pg_restore -U postgres --list < pandokos-pg16.dump > /dev/null
   ls -l pandokos-pg16.dump
   ```

   The dump is made by the superuser, in the custom format, as in step 1.a of the backup guide; the second command
   reads it back as a check. Copy it off the host: it is your way back.
3. **Remove the 16 server and its volume.** The volume is the one this deployment creates, `pandokos_pgdata`
   (`docker volume ls` lists it). Once the dump is checked and kept:

   ```sh
   docker compose down
   docker volume rm pandokos_pgdata
   ```

   `docker compose down` does not remove volumes; the `cli` volume (the CLI's keys) stays. To keep the old
   volume too, copy it before removing it: `docker run --rm -v pandokos_pgdata:/from -v "$PWD":/to alpine tar czf
   /to/pgdata-pg16.tar.gz -C /from .`.
4. **Take the new files** (the new `compose.yaml` and, with it, the new image of Pandokos if you move to this
   release at the same time; the migration of an upgrade is a separate, later step: [upgrade guide](upgrade.md)),
   and start only the new database:

   ```sh
   docker compose up -d --wait postgres
   ```

   It creates a new, empty volume, and `initdb/10-pandokos.sh` runs once on it: the privilege roles with the
   passwords of `secrets/`, and the empty database `pandokos` with its grants (the work of steps 4.1 and 4.2 of the
   backup guide).
5. **Restore** the dump into that database, stopping at the first error. Do not use `--data-only`,
   `--disable-triggers`, `--no-owner` or `--no-acl` (step 4.2 of the backup guide says why):

   ```sh
   docker compose exec -T postgres pg_restore -U postgres -d pandokos --exit-on-error < pandokos-pg16.dump
   ```

6. **Start Pandokos.** The schema comes back as it was, so `migrate` has nothing to apply when the image is the same:

   ```sh
   docker compose up -d --wait
   ```

7. **Verify** as step 4.6 of the backup guide does: `curl --cacert secrets/tls_cert https://localhost:8080/readyz`
   answers 200; `docker compose logs pandokos` has `vault ready` with the `kek_version` you had; and the chains
   verify against the heads of step 1:

   ```sh
   docker compose exec -e PANDOKOS_CLI_KEY_FILE=/home/nonroot/demo-admin.key pandokos \
     /pandokos audit export --tenant <tenant ID> --to-seq <seq kept with the head> --out /home/nonroot/tenant.ndjson
   docker compose exec -e PANDOKOS_CLI_KEY_FILE=/home/nonroot/demo-admin.key pandokos \
     /pandokos audit verify --tenant <tenant ID> --file /home/nonroot/tenant.ndjson --expect-head <hash kept outside>
   docker compose exec -e PANDOKOS_CLI_KEY_FILE=/home/nonroot/bootstrap.key pandokos \
     /pandokos platform-audit export --to-seq <seq kept with the head> --out /home/nonroot/platform.ndjson
   docker compose exec -e PANDOKOS_CLI_KEY_FILE=/home/nonroot/bootstrap.key pandokos \
     /pandokos platform-audit verify --file /home/nonroot/platform.ndjson --expect-head <hash kept outside>
   ```

   Then a call with a tenant's key reaches the provider and `pdk usage list` shows the records of before the move.

**Going back** is the same with the old file: remove the 18 server and its volume, put the old `compose.yaml` back
and restore the dump into a 16 server the same way (the dump of a 16 server restores into 16).
