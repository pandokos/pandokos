# 0047. Identity providers in the test suite

- Status: proposed
- Date: 2026-11-16

Refines [ADR 0018](0018-supply-chain.md). [ADR 0023](0023-test-dependencies-in-nested-modules.md) does not
apply: no third-party Go module is needed.

## Context

IAM-06 asks that tokens of real identity providers are accepted, and the epic asks for tests with two
open-source providers. Faults (an outage, a rotated key, an oversized key set) and forged tokens cannot
be produced by a real provider, and tokens and keys must not be stored in the repository.

## Decision

- **An in-process issuer**, `test/fakeidp`, with keys generated when the test runs, serves everything that
  needs faults or forged tokens: discovery, key set, a signer for arbitrary headers and claims, refusal,
  delay, oversize, key rotation, request counting.
- **Keycloak and Ory Hydra in containers**, behind the build tag `idp` in the root module, in one CI job
  `idp` (built from the steps of the `test` job that start the database, then `make idp-up` and
  `make test-idp`). A test behind the tag fails, never skips, when a provider is missing. Pinned by digest,
  so that a Dependabot update is a reviewed change:
  - `quay.io/keycloak/keycloak:26.8.2@sha256:c68e286cacd87b942ac1939a0a1481461109d6acd3a57dc496969a319a76bbf0`
  - `oryd/hydra:v26.2.0@sha256:ff67c7fb5f95074fa53374d41151713554960504b340cd3f95b09e65deaea2a9`
- **Keycloak** carries the platform-level scenario. It starts with `--http-enabled=false`; the realm is
  rendered by `up.sh` to `local/idp/keycloak-realm.json` and mounted at
  `/opt/keycloak/data/import/pandokos.json`. Clients have `fullScopeAllowed=false` and
  `directAccessGrantsEnabled=false`. Tokens are RS256, `aud` is a string, `sub` a generated UUID, `azp` and
  `client_id` carry the client ID, the lifetime is 300 s, there is no `nbf`, and a claim mapper adds the
  organisation as `pandokos_org`. Its key set includes an RSA-OAEP key with `use: enc`, which `internal/jwks`
  drops.
- **Hydra** carries the tenant-bound scenario. It is configured by environment: `DSN=memory`,
  `SECRETS_SYSTEM`, `URLS_SELF_ISSUER` (with a trailing slash), `URLS_LOGIN`, `URLS_CONSENT`,
  `SERVE_PUBLIC_PORT`, `SERVE_PUBLIC_TLS_ENABLED=true`, `SERVE_PUBLIC_TLS_CERT_PATH`,
  `SERVE_PUBLIC_TLS_KEY_PATH` and `STRATEGIES_ACCESS_TOKEN=jwt`. Its admin API is plain HTTP on port 4445
  inside the container and is not published. Clients are created with `docker compose exec hydra hydra
  create client --endpoint http://127.0.0.1:4445 --id ... --secret ... --token-endpoint-auth-method
  client_secret_post --audience ...`. Tokens are RS256 (its key set has two `use: sig` keys), `typ` is `JWT`,
  `aud` is an array, `sub` equals the client ID, the lifetime is 3599 s and `nbf` and `iat` are present. A
  request without the `audience` parameter gives `aud: []`, which Pandokos refuses; an audience outside the
  client's list is refused by Hydra (`invalid_request`).
- **Mechanics.** Neither image has a shell or curl, so the compose file has no healthcheck and `up.sh` polls
  with curl from the host. `up.sh` chooses free ports with `python3`, generates a certificate (the
  directory `local/idp` is mode 0700 and git-ignored, and `server.key` is mode 0644 inside it because the
  containers run as other users) and per-client secrets, writes `local/idp/env`, and runs `docker compose
  down -v` first, because Hydra's store is in memory. `make idp-up`, `make test-idp` and `make idp-down`
  follow the pattern of `make db-up`.
- **No key, token or client secret in the repository.** A test (T-R37) scans for token-shaped strings and
  private key blocks.

## Consequences

- Each update of either provider runs against Pandokos before it is merged, and the documents of each
  pinned version are tested unchanged against `internal/jwks`.
- Two containers more in one CI job; `make check` does not run them.
- Providers other than the two are not tested; the guide marks them so.

## Alternatives considered

**Dex.** No client-credentials grant in v2.45.1.
**Zitadel.** Needs its own database.
**Recorded tokens.** They expire, and they would be secrets in fixtures.
**A nested module with a Go OIDC client.** Not needed: the tests are plain HTTP against the binary.

## History

Sprint 4.1.
