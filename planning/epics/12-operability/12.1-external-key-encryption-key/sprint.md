---
id: SPRINT-12.1
title: External key-encryption key
epic: EPIC-12
release: v0.3
status: in-progress
design: draft 2026-11-24
---

# Sprint 12.1 — External key-encryption key

- Epic: [12 — Operability](../epic.md)
- Implementation plan: [plan.md](plan.md)
- Previous sprint: [6.4 — Events for external systems](../../06-observability-reports-lifecycle/6.4-events-for-external-systems/sprint.md)
- Scope: the epic file, section 12.1, with the items carried to 12.1 by the closed epic 6
  ([history](../../../../docs/history/epic-06-observability-reports-lifecycle/README.md), "Carried
  forward"), section 11 of sprints 6.3 and 6.4, open point O-8 of 6.3 and O-5 of 6.4, and the
  maintainer decisions of 2026-11-24 listed below.
- Risk class: **high** (the key that wraps every tenant's data key moves to a remote service; a
  second key provider wraps and unwraps next to the first during a migration; a secret of the
  installation, the token of the key service, is held and renewed by the process; a network
  dependency enters the path that opens credentials; `serve` starts differently)
- Decisions implemented: [ADR 0008](../../../../docs/adr/0008-vault-envelope-encryption.md) (the
  external provider), [0033](../../../../docs/adr/0033-vault-formats-and-key-identifiers.md) (formats,
  identifiers, cache numbers), [0068](../../../../docs/adr/0068-key-encryption-key-versions-rotation-and-erasure-in-backups.md)
  (versions, activation, re-wrap, erasure in backups), [0038](../../../../docs/adr/0038-egress-guard-upstream-tls-and-proxy.md)
  (every outbound connection is made by one package), [0019](../../../../docs/adr/0019-configuration.md)
  (secrets are paths to files; thirteen keys), [0002](../../../../docs/adr/0002-implementation-language-go.md)
  (no new module), [0020](../../../../docs/adr/0020-error-contract.md) (no new code),
  [0055](../../../../docs/adr/0055-operational-telemetry-holds-no-tenant-data.md),
  [0062](../../../../docs/adr/0062-metrics-instruments-attributes-cardinality-and-scrape.md) (four
  instruments), [0063](../../../../docs/adr/0063-the-log-redaction-and-the-log-reference.md) (seven
  lines), [0047](../../../../docs/adr/0047-identity-providers-in-the-test-suite.md) (the model for real
  services in the suite), [0071](../../../../docs/adr/0071-webhook-destinations-signature-secrets-and-egress.md)
  (signing secrets need a data key cache with a remote key service).
- Decision records changed by this sprint: new **ADR 0073** (the transit key provider: the interface
  of the service that is used, associated data, version identifiers, authentication, several
  providers at once, behaviour on failure, what is not automated; **amends** ADR 0033 point 5 and
  ADR 0038 point 7); history lines on 0008, 0019, 0033, 0038, 0062, 0063, 0068 and 0071. Text in
  [plan.md](plan.md), part E.

This document is the design. The implementer follows [plan.md](plan.md); a gap in either document is
raised as a question, not filled silently ([planning/README.md](../../../README.md)).

Maintainer decisions taken as given (2026-11-24): the provider speaks the transit secrets engine
HTTP API, and both OpenBao (the open-source reference) and HashiCorp Vault are tested in CI against
real containers with pinned images; Pandokos authenticates with a static token or with AppRole and
handles the token's lifetime itself; Kubernetes authentication is out; namespaces are an optional
key sent as `X-Vault-Namespace`; whether the service binds associated data is verified for real
before anything else is designed (§12, D-2); cloud key services, PKCS#11 and KMIP are out (issue
#75); cryptography from the standard library and no new module for the client.

| Given by the epic and the closed sprints | Designed in |
|---|---|
| External provider over a transit API, behind the provider interface of the vault, which keeps its four methods (6.3) | §5.1 to §5.5, D-1 to D-6 |
| Associated data are bound on every wrap and unwrap (maintainer decision 4) | §5.2, D-2, §12 "verified" |
| What a transit key version is, against `kek_version` and the rules of ADR 0033 | §5.3, D-4, D-5 |
| Migration from the key file to the service and back, by re-wrapping (6.3 machinery) | §5.7, D-8, T-1083 |
| The start check with versions registered by another provider (T-328) | §5.7, D-8, T-1072 |
| Slow or unavailable provider: bounded caches, refusal, readiness | §5.8 to §5.10, D-11 to D-14 |
| Static token and AppRole, with the token's lifetime; namespaces | §5.5, D-7, D-9 |
| TLS to the service, the egress guard (ADR 0038) | §5.6, D-10 |
| A transit provider maps its versions onto `kek_version` and `kek_activate`; destroying a key file becomes trimming a version; a deletion may then trigger a rotation by itself (6.3 §11, O-8; ADR 0068) | §5.4, D-6 (made possible, not automated), O-3 |
| Opening signing secrets needs a data key cache with a remote key service (6.4 §11, ADR 0071) | §5.9, D-12, T-1076 |
| A platform webhook, deferred because the vault seals under tenant data keys only (6.4 O-5) | §2 (kept deferred), D-19, O-5 |
| The provider becomes a dependency of the data plane; the failure reference gains its rows (ADR 0008, OPS-11) | §5.10, §5.14, T-R86 |
| Canary tests extended to the provider's requests and errors | §5.13, T-1068, T-1086 |
| Exit: with the transit provider, no key-encryption key exists on the Pandokos hosts | T-1082, T-1083 (last start: no key file configured), T-1094 |
| Exit: a restart during an outage of the provider serves nothing and recovers alone | T-1084, T-1094 |
| Exit: moving between providers needs no action by tenants | T-1083, T-1094 |

## 1. Outcome

An operator sets `vault.kek.provider: transit` and names a key of the transit secrets engine of an
OpenBao or HashiCorp Vault service. From then on every tenant data key is wrapped and unwrapped by
that service: Pandokos sends a data key and receives it wrapped, sends it wrapped and receives it
back, and never holds, reads or stores the key-encryption key. The token it presents can encrypt,
decrypt and identify with that one key and do nothing else; it can neither export, rotate, trim nor
delete it.

An installation that runs with a key file moves to the service, and back, by configuration and
restarts: the new provider's version becomes the active one, the worker re-wraps every data key with
the job of sprint 6.3, the old version is retired, and the key file leaves the hosts. Tenants do
nothing and their sealed credentials are not touched.

When the service is slow or away, calls whose data key is cached continue until the cache lifetime
ends; every other call, and every write of a secret, is refused with `503 unavailable` after a
bounded wait, and nothing is forwarded that could not be opened. A process that starts while the
service is away listens on no port, waits, and starts by itself when the service answers. No token,
secret ID, data key or wrapped key appears in a log line, an error, a metric, an audit row or a
response.

## 2. Scope

### In scope

- `internal/vault`: the transit provider and its client (`transit*.go`), a router over several
  providers (`router.go`), a cache of unwrapped keys for the worker (`unwrapcache.go`), the test
  double of the service (`transittest/`); changes to `check.go`, `activate.go`, `opener.go`,
  `sealer.go`, `rewrap.go` (one new cause).
- `internal/egress` (a dedicated transport for the key service), `internal/config` (thirteen keys),
  `internal/telemetry` (four instruments, the log reference), `cmd/pandokos` (`serve` builds the
  providers and waits for the service; `kek check`), `internal/outbox` wiring (the relay opens through
  the cache).
- `test/transit` (one conformance suite for the test double, OpenBao and Vault; build tag `transit`
  for the real services), a `transit` job in CI with a matrix of the two services, `make transit-up`,
  `transit-down`, `test-transit`.
- The guide `docs/operations/external-key.md`, the policy `deploy/transit/pandokos-policy.hcl`, the
  references and ADR 0073 of plan part E, the draft release notes.

No migration, no change to the OpenAPI document, no permission, no error code, no new module.

### Not in scope

| Item | Where |
|---|---|
| Google Cloud KMS, AWS KMS, Azure Key Vault and other cloud key services | issue #75, after v1.0; a provider implements `KeyProvider`, the identifier rule of D-4 and passes `vault.Check` |
| PKCS#11, KMIP | not planned |
| Kubernetes authentication to the key service | sprint 8.2 (Kubernetes deployment); until then an agent that writes a token file works (`auth.token_file` is read again when the token is refused, §5.5) |
| TLS client certificates, JWT/OIDC, cloud IAM and other authentication methods of the service; response-wrapped secret IDs | not planned; on demand |
| Rotation started by Pandokos: after a tenant deletion, on a schedule, or by an API operation; `min_decryption_version`, `trim` and key deletion done by Pandokos | not done here on purpose (D-6): the token Pandokos holds must not be able to destroy a key version. The operator's scheduler or the service's `auto_rotate_period`, and a restart, do it; O-3 |
| Following a rotation of the service without a restart | not planned; ADR 0068 point 7 holds (a rotation is an act and a restart); O-6 |
| Using a data key after its cache lifetime while the service is away ("stale" keys) | not planned: the lifetime is the guarantee (ADR 0033 point 6); `cache.data_key_ttl` can be raised to 15 m; O-4 |
| Two transit services, or two keys of one service, at the same time (a direct move from one service to another) | through the key file (service A → file → service B), each step a re-wrap; a second transit block on demand; O-11 |
| A webhook destination of the platform (for the platform's events), which needs a secret of the platform | still deferred: an external key-encryption key does not give the platform a data key; it needs a platform data key, a table and associated data of its own (D-19, O-5). Pull export serves the platform's events (ADR 0072) |
| A supported Compose overlay that runs a key service | not planned: a key service run in development mode loses its keys at a restart, and a production one is unsealed and backed up by its operator; the guide gives the commands for both services |
| Running, unsealing, backing up and restoring the key service | the operator's; the guide says what Pandokos needs from it. The restore drill with a key service is sprint 12.3 |
| Failure injection for every dependency as a suite; latency figures with a remote key service | sprint 10.1 |
| The threat model reviewed against this code; a fuzz target for the client's response parser | sprint 12.2 (§11) |
| Sealing with a public key so that the admin role cannot unwrap | open question of the architecture overview; unchanged |

## 3. Requirements claimed

The meaning is quoted from [docs/requirements](../../../../docs/requirements/README.md).

| ID | Meaning | Delivered here | Extent |
|---|---|---|---|
| VLT-12 | "The key-encryption key comes from a pluggable provider: a local key file by default, an external KMS or transit service optionally" | The provider `transit` behind `vault.KeyProvider`, tested against OpenBao and HashiCorp Vault; moving between the two providers by re-wrapping (§5.1 to §5.7) | the external part (S, R3): full for a transit service; cloud key services are issue #75 |
| VLT-10 | "The key-encryption key can be rotated by re-wrapping data keys, with no action by tenants. Key versions are recorded" | Touched: a rotation of the service's key and a change of provider are both a new active version and the re-wrap of 6.3 (§5.4, §5.7) | touched |
| VLT-11 | "Deleting a tenant destroys its data key, so residual ciphertext can no longer be decrypted: in the database at once, and in backups once the key-encryption key version that wrapped the data key is retired" | Touched: with the service, "its file destroyed" becomes "the version made undecryptable and trimmed in the service"; the guide gives the rule (§5.4) | touched (documentation) |
| VLT-06 | "A secret is decrypted only by the data plane, only after the request is authenticated and authorized, and only for the time needed to call the provider plus a bounded cache lifetime" | Touched: no key is used past `cache.data_key_ttl` when the service is away; the worker's cache of unwrapped keys has the same bound (§5.9) | touched |
| VLT-09 | "… a per-tenant data key, itself wrapped by a key-encryption key that is never stored in the database" | Touched: with the service the key-encryption key is not on the hosts either | touched |
| VLT-13 | "Database backups contain only ciphertext and wrapped keys. The key-encryption key is backed up separately, and the procedure is documented" | Touched: the key is backed up by the key service's own backup; the guides say which versions a database backup needs (§5.14) | touched (documentation) |
| VLT-14 | "Secrets never appear in logs, traces, metrics, error messages, events or diagnostics. Automated tests plant canary secrets and search every output for them" | Touched: the token, the secret ID, data keys and wrapped keys join the canaries; the requests sent to the service are searched too (§5.13) | touched |
| VLT-16 | "The security limits of the vault and the recovery procedure for lost or compromised keys are documented, distinguishing recoverable secrets from those the owner must re-enter" | Touched: the limits with a key service and the loss or compromise of its key or of the token (§5.14) | touched (documentation; tested in 12.3) |
| OPS-08 | "Static settings come from a file and environment variables; secrets are referenced as files. …" | Touched: thirteen keys; the token, the role ID and the secret ID are files (§5.11) | touched |
| OPS-11 | "Behaviour on failure of each dependency is defined. If authorization or the credential cannot be verified the request is not forwarded" | Touched: the key service is a dependency with its rows in the failure reference, each citing its test (§5.10, §5.14) | touched |
| SEC-01 | "… Certificates of upstream providers are always verified" | Touched: the certificate of the key service is always verified; there is no switch (§5.6) | touched |
| SEC-03 | "Error messages reveal no secrets, internal configuration or data of other tenants" | Touched: no error quotes an answer of the service, an address or a path's content (§5.8) | touched |
| SEC-04 | "Defaults are secure. At start-up Pandokos validates its configuration and refuses or warns about weak settings" | Touched: refusals and warnings of §5.3 and §5.11 | touched |

## 4. Repository layout

New: `internal/vault/{transit,transit_client,transit_auth,router,unwrapcache}.go` and their tests,
`internal/vault/transittest/` (the test double), `test/transit/` (`doc.go`, `conformance.go`,
`fake_test.go`, `real_test.go`, `binary_test.go`, `compose.yaml`, `up.sh`, `down.sh`),
`test/e2e/transit_test.go`, `test/repo/transit_test.go`, `deploy/transit/pandokos-policy.hcl`,
`docs/operations/external-key.md`, `docs/adr/0073-the-transit-key-provider.md`.

Changed: `internal/vault/{provider,check,activate,opener,sealer,rewrap,doc}.go`,
`internal/egress/guard.go`, `internal/config/{config,keys,validate,lifecycle}.go`,
`internal/telemetry/{instruments,sources,metrics}.go`, `cmd/pandokos/{serve,kek,main,metrics}.go`,
`test/e2e/e2e_test.go` (options of the harness), `test/vaulttest`, `Makefile`,
`.github/workflows/ci.yml`, `.gitignore` (`local/transit`), the tests of §9 "Changed tests", the
documents of plan part E.

## 5. Architecture

### 5.1 The part of the transit API that is used (D-1)

The provider is `vault.TransitProvider`, name `transit`. It uses seven requests and no other
(`<m>` is the mount, `<k>` the key name):

| Operation (metric value) | Request | Used for |
|---|---|---|
| `wrap` | `POST /v1/<m>/encrypt/<k>` with `plaintext`, `associated_data`, `key_version` | `Wrap` |
| `unwrap` | `POST /v1/<m>/decrypt/<k>` with `ciphertext`, `associated_data` | `Unwrap` |
| `describe` | `GET /v1/<m>/keys/<k>` | type and settings of the key, `latest_version`, `min_decryption_version` |
| `identify` | `POST /v1/<m>/hmac/<k>/sha2-256` with `input`, `key_version` | the identifier of a version (§5.3) |
| `lookup` | `GET /v1/auth/token/lookup-self` | lifetime of the token |
| `renew` | `POST /v1/auth/token/renew-self` | renewal |
| `login` | `POST /v1/auth/<approle mount>/login` with `role_id`, `secret_id` | AppRole |

Every request carries `X-Vault-Token` (not the login), `X-Vault-Request: true`,
`X-Vault-Namespace` when `vault.kek.transit.namespace` is set, `User-Agent: pandokos/<version>` and
a JSON body built with `encoding/json`. An answer is read up to 64 KiB; a longer one is an error.
The client is a file of `internal/vault` over `net/http` with a transport given by the egress
guard (§5.6). No module of either vendor is imported (ADR 0002, T-R85).

The key must exist: Pandokos never creates it. Its policy grants `update` and not `create` on the
encrypt path, so a wrong key name is refused by the service instead of creating a key (observed: with
a root token an encrypt on a missing key creates it; with `update` only it is `403`).

The least-privilege policy the operator loads is a file of the repository,
`deploy/transit/pandokos-policy.hcl` (for a mount `transit` and a key `pandokos`), and the conformance
suite loads that very file (T-1090):

```hcl
path "transit/encrypt/pandokos"       { capabilities = ["update"] }
path "transit/decrypt/pandokos"       { capabilities = ["update"] }
path "transit/hmac/pandokos/sha2-256" { capabilities = ["update"] }
path "transit/keys/pandokos"          { capabilities = ["read"] }
path "auth/token/lookup-self"         { capabilities = ["read"] }
path "auth/token/renew-self"          { capabilities = ["update"] }
```

### 5.2 Associated data (D-2)

`Wrap` and `Unwrap` receive the associated data of ADR 0033 point 3 (a label, a zero byte, the
tenant ID and the data key version). The provider sends as `associated_data` the base64 of

```
SHA-256( "pandokos/transit-aad/v1" ‖ 0x00 ‖ aad )
```

and never the associated data themselves. The binding is the same (two different inputs give two
different digests), the value has a fixed length, and **no tenant identifier reaches the key service
or its audit log**. The service passes the value to AES-GCM as additional authenticated data: a
wrapped key presented with other associated data, with none, or moved to another tenant's row is
refused by the service (`400`, "message authentication failed").

This holds on the pinned versions of both services and was observed to hold from HashiCorp Vault
1.13 and OpenBao 2.0.0; **Vault 1.8 to 1.12 accept the field and ignore it without an error** (§12).
A version number is therefore not what is trusted. `vault.Check`, which runs at every start and in
`pandokos kek check`, gains a step for every provider: after wrapping and unwrapping its random
probe under `pandokos/check/v1`, it unwraps the same value under `pandokos/check/v1/other` and
requires `ErrUnwrap` (the two steps are `vault.Probe`, which `Check` calls first). A provider that
returns the probe is refused with `ErrAADNotBound` ("vault: the
key provider does not bind associated data") and `serve` does not start. The local provider passes
by construction.

No inner envelope is added (a second layer encrypted or bound locally): it would need a key on the
host, or a format of our own inside the service's ciphertext, for a property the service provides
and the start check proves. The data key itself is further bound to its tenant by its derivation
(ADR 0033 point 2), so a swapped wrapped key opens nothing even where the binding failed.

### 5.3 The key, its versions and their identifiers (D-3, D-4)

**The key.** `describe` must show `type: aes256-gcm96`, `derived: false`, `supports_encryption` and
`supports_decryption` true, and `latest_version ≥ 1`; a key that is missing, soft-deleted, of
another type or derived stops the start with a message that names the setting and not the key's
content. Five settings are accepted with a warning, once at start, in the line `key provider
configuration weak` with `setting` from a closed list: `exportable`, `plaintext_backup`
(`allow_plaintext_backup`), `deletion_allowed`, `auto_rotate` (`auto_rotate_period` not zero: a
rotation the operator did not start takes effect at the next restart, §5.4) and `root_token` (the
token's policies include `root`).

**A transit key version is a number** (1, 2, …) inside a named key; a ciphertext is
`vault:v<N>:<base64>`. A number alone cannot be a `kek_version`: version 1 of another key, of the
same key name on another service, or of a key deleted and created again, would carry the same
number, and data keys would be wrapped under a key that is not the one registered. ADR 0033 rejects
identifiers that can be attached to the wrong key.

**The identifier is derived from the key version by the service itself.** The service computes an
HMAC-SHA-256 of a fixed label under the version (`identify`, with `key_version: N`); each version of
a transit key has an HMAC key of its own, so the result is the same at every call, and differs for
every version and every key (observed on both services, §12):

```
id(N) = "transit-" ‖ hex( first 8 bytes of HMAC_N("pandokos kek id v1") ) ‖ "-v" ‖ N
        e.g.  transit-5b2fee9a2463f150-v1
```

It satisfies `kek_version_id_format` (migration 0003), reveals nothing about the key, and does not
depend on the address, the namespace, the mount or the key name, so moving or renaming them changes
no identifier. Like the local provider's, **an identifier cannot be attached to the wrong key**: a
service that does not hold that key version computes another fingerprint.

- At start the provider describes the key, takes `N = latest_version`, computes `id(N)`: that is the
  version `Wrap` returns and `kek_activate` registers with the provider `transit`.
- `Available(id)` and `Unwrap(id, …)` parse `-v<N>`, require `min_decryption_version ≤ N ≤
  latest_version` in a description no older than 10 s (it is read again otherwise), and verify the
  fingerprint by `identify` with `key_version: N` the first time that identifier is seen (the result
  is kept for the life of the process). A mismatch, a version out of range, a version the service
  refuses to identify (`min_encryption_version` above it) or an identifier that does not parse is
  `ErrKeyVersionUnknown`, and no decrypt request is sent.
- The wrapped key stored in `tenant_data_key.wrapped_key` is `0x02 ‖ payload`, where the payload is
  the base64-decoded third field of the ciphertext (60 bytes for a 32-byte data key; the check of
  migration 0003 allows 32 to 1024). `0x02` names the format "transit, payload of the service"; the
  local provider keeps `0x01`. The version number is not stored twice: `Unwrap` builds
  `vault:v<N>:<base64(payload)>` from the identifier.

### 5.4 Wrap is pinned; a rotation is the operator's act (D-5, D-6)

`Wrap` sends `key_version: N` with the N of the start and refuses an answer whose `key_version` or
ciphertext prefix is another one. A key rotated in the service while Pandokos runs therefore
changes nothing until a restart: the database accepts a data key under the active version only
(ADR 0068 point 1), and an instance that wrapped under the newest version by itself would be refused
by the trigger for every new tenant.

A rotation with the service is: rotate the key in the service (`POST <m>/keys/<k>/rotate`, by the
operator or its scheduler, with a token that is not Pandokos's), restart the instances; the first
start registers `id(N+1)` as active through `kek_activate`, the previous one becomes `retiring`, the
worker re-wraps and retires it (`key rotation complete`). This is ADR 0068 point 7 unchanged, with
"a new key file" replaced by "a new version in the service". An instance not yet restarted keeps
opening credentials and creates no data key (`503`, cause `kek_replaced`), as with key files.

**Pandokos never rotates, configures, trims or deletes the key**, and its policy does not allow it
(T-1090 proves the refusals). Destroying a retired version, which is what completes the erasure of a
deleted tenant in old backups (ADR 0068 point 6), is done in the service by the operator: raise
`min_decryption_version` above it, then set `min_encryption_version` and `trim`. The guide gives the
rule: after a deletion, rotate within the time erasure is promised in, restart, wait for `key
rotation complete` and `erasures_pending: 0` on the old version, take a new backup, then trim. A
version raised out of reach too early is found at the next start (`Available` fails, the message
names the version) and, while running, by `credential cannot be opened` for the tenants not yet
re-wrapped; lowering `min_decryption_version` again repairs it until the version is trimmed.

What 6.3 left open (O-8) is answered conservatively: the external service **makes automation
possible** and this sprint does not automate it inside Pandokos. A product that could trim versions
could destroy every credential by a bug or by a stolen token; the operator's scheduler can do the
same four requests with a token of its own.

### 5.5 Authentication and the token's lifetime (D-7, D-9)

`vault.kek.transit.auth.method` is `token` or `approle`. Secrets are paths to files (ADR 0019),
never values in the file or the environment.

| | `token` | `approle` |
|---|---|---|
| At start | read `auth.token_file`; `lookup` | read `auth.approle.role_id_file` and `secret_id_file`; `login` at `auth/<auth.approle.mount>/login` |
| Lifetime known from | `ttl` and `renewable` of `lookup` (`ttl: 0` never expires) | `lease_duration` and `renewable` of the login |
| At two thirds of the lifetime | read the file again: a different token is looked up and adopted; otherwise `renew` when renewable | `renew` when renewable; a new `login` (the files are read again) when the renewal is refused or the lifetime granted is a third of the first one or less |
| When nothing extends it | `key provider token expiring` (warn, with `expires_in_seconds`) at each attempt, every 30 s at most | the same, and `key provider authentication failing` while the login is refused |
| A request answered `403` | the file is read again; a different token is adopted and the request sent once more | one `login` (shared by concurrent requests, one per 5 s at most) and the request sent once more |

Lifetimes are counted from the local monotonic clock at the moment of the answer, never from
`expire_time`. An old token is not revoked; it expires. A failed renewal or login leaves the current
token in use until it stops working and is tried again with a backoff of 1 s to 60 s. A `lookup`
that is refused stops the start (`denied`): the shipped policy grants it. The lifecycle runs in one
goroutine (`TransitProvider.Run`) that ends with the process.

The token, the role ID and the secret ID are `secret.Secret` values from the file to the header or
the body; the provider and its options print the redaction marker on every formatting path (T-1067).

`vault.kek.transit.namespace`, when set, is sent as `X-Vault-Namespace` with every request, the login
included. OpenBao 2.7 implements namespaces (a key in a namespace is found only with the header);
the community edition of HashiCorp Vault ignores the header (§12).

### 5.6 The connection (D-10)

The transit client is the fourth consumer of the egress guard (ADR 0038 point 1: no other package
builds a transport). `egress.Guard.KeyService(roots)` returns a transport of its own, with its own
connection pool:

- the address is the operator's configuration, so private addresses are accepted (the class of an
  endpoint marked private); forbidden addresses (instance metadata, multicast, unspecified) are
  refused as everywhere;
- `https` only, TLS 1.2 or later, the certificate always verified, no switch to turn it off.
  `vault.kek.transit.tls.ca_file`, when set, is the **only** set of authorities trusted for the key
  service; empty means the system roots and `upstream.tls.ca_file`. `http` is accepted by the
  configuration only for a loopback host (an agent or proxy of the key service on the same host), with
  a warning (O-8);
- never through `upstream.proxy.url`, and proxy variables of the environment are not read;
- redirects are not followed: a standby node that answers `307` is an unavailable service, so the
  address names the active node or its load balancer;
- 32 idle connections at most; the dial and the TLS handshake are bounded by
  `upstream.connect_timeout` and by the deadline of the request.

### 5.7 Several providers, and moving between them (D-8)

`vault.kek.provider` names the provider that **wraps**. The other provider takes part, for
unwrapping only, exactly when it is configured: the local one when
`vault.kek.local.unwrap_key_files` lists files, the transit one when `vault.kek.transit.address` is
set. With `provider: transit`, `vault.kek.local.key_file` must be empty (the old key file moves to
`unwrap_key_files`), so nothing can wrap with a key on the host by mistake.

`vault.Router` holds the active provider and the others and is itself a `KeyProvider`: `Name` is the
active one's, `Wrap` goes to the active one, `Unwrap` and `Available` go to the provider whose name
is the part of the version identifier before its first `-` (`local-…`, `transit-…`); an identifier
of no configured provider is `ErrKeyVersionUnknown`. A version returned by `Wrap` that does not
start with the active provider's name is refused. With no second provider configured `serve` uses
the single provider directly: an installation with a key file runs the code it ran before.

`vault.Check` compared the provider recorded with each version to `p.Name()`; it now asks the
router which provider owns the identifier (`ProviderOf`), so versions registered by two providers
pass when both are configured, and a version whose provider is not configured, or whose recorded
provider is not the one its identifier names, fails with the version named (T-328 extended, T-1072).
Nothing else of activation changes: `kek_activate(id, provider)` registers the active version with
the active provider's name, and a replaced version never comes back, whatever its provider.

| Step | Key file → key service | Key service → key file |
|---|---|---|
| 0, several instances only | every instance gets `vault.kek.transit.*` (provider still `local`) and is restarted: it can unwrap what the first migrated instance will wrap | every instance gets the **new** key file in `unwrap_key_files` and is restarted |
| 1 | `provider: transit`; the old `key_file` moves to `unwrap_key_files`; restart. `vault ready` shows `kek_provider: transit`, `kek_activation: activated`; the local version is `retiring` | `provider: local` with a key file made by `pandokos kek generate` (a replaced local version cannot be activated again); `vault.kek.transit.*` stays; restart |
| 2 | calls continue; the worker re-wraps; `key rotation complete` names the local version; `kek-version list` shows it `retired` | the same, naming the transit version |
| 3 | remove `unwrap_key_files` and restart; `key provider no longer needed` (provider `local`) said so at every start since step 2. The key file is destroyed on the hosts — kept offline only as long as a backup older than step 2 must still open credentials (ADR 0068 point 6) | remove `vault.kek.transit.*` and restart; the token and the AppRole are revoked in the service |

`pandokos kek check` (§5.12) proves the service's configuration before step 1. The message of
`ErrKeyReplaced` no longer assumes a key file: it names the configured and the active versions and
says to configure the provider of the active one.

### 5.8 One attempt, bounded, and refusal (D-11, D-15)

- **One attempt.** `Wrap`, `Unwrap`, `describe` and `identify` are sent once. There is no retry in
  the provider: a retry multiplies the latency of a call that is already waiting, and the callers
  (the next request of the client, the next pass of a job) are the retry. The single exception is
  the `403` of §5.5.
- **A deadline.** Each request, from waiting for a slot to the last byte of the answer, has
  `vault.kek.transit.request_timeout` (2 s; 100 ms to 30 s) or the caller's deadline, whichever ends
  first.
- **A bound on concurrency.** At most `vault.kek.transit.max_concurrent_requests` (32; 1 to 1024)
  requests are in flight; a call that gets no slot before its deadline fails as unavailable. Identical
  concurrent `Unwrap` calls (same version, wrapped key and associated data) share one request and
  each receives its own copy.
- **Refusal.** After 5 consecutive requests that ended as unavailable, the provider refuses every
  call at once for 5 s without sending anything, then lets one request through; a success closes the
  refusal, a failure renews it. Answers that are not about availability (`400`, `403`, `404`) do not
  count. The numbers are constants, documented in the guide.

Answers are classed by status only; **the body of an error answer is read, discarded and never
quoted, logged or parsed**, because it can repeat what was sent.

| Answer | Class (`pandokos.kek.outcome`) | Error |
|---|---|---|
| `200` with the expected fields | `ok` | none |
| `400` to `decrypt` for a version that is available | `rejected` | `ErrUnwrap` (wrong associated data, damaged bytes) |
| `400` to `decrypt` for a version that a fresh description puts out of range | `rejected` | `ErrKeyVersionUnknown` |
| `400` elsewhere; `200` with missing or malformed fields | `rejected` | a `*TransitError` |
| `403` (after the single new authentication of §5.5) | `denied` | a `*TransitError` |
| `404` (mount, key or namespace) | `not_found` | a `*TransitError` |
| `429`, `5xx` (a sealed service answers `503`), a redirect, a timeout, a refused or reset connection, a failed TLS handshake | `unavailable` | a `*TransitError` that is `ErrProviderUnavailable` |
| not sent: no slot, or the refusal is in force | `refused` | the same |

`*TransitError` holds the operation and the class; its text is built from those two words and
nothing else. It is `ErrUnavailable` in every class (the callers answer `503`), and
`ErrProviderUnavailable` (new) when the service may answer later. The opener, the sealer and the
re-wrap job say `cause: kek_provider_unavailable` for it, next to `kek_unavailable` (a version the
configuration does not provide), `data_key_unwrap` and `kek_replaced`. No error code is added
(ADR 0020): the caller gets `503 unavailable` with `Retry-After: 5`, as for a key file out of reach.

### 5.9 Caches (D-12)

- **The gateway** is unchanged: the opener keeps derived data keys for `cache.data_key_ttl` (5 m, at
  most 15 m, 10 000 entries) and opened credentials for `cache.credential_ttl`. One `unwrap` per
  tenant and lifetime reaches the service, and concurrent first calls of a tenant share it (§5.8). An
  entry is never used after its lifetime, service away or not.
- **The worker** gets `vault.UnwrapCache`, a `KeyProvider` that wraps another: `Wrap`, `Available`
  and `Name` pass through; `Unwrap` keeps the result under SHA-256 of the version, the wrapped key and
  the associated data, for `cache.data_key_ttl`, 10 000 entries at most (emptied when full, as the
  others), and returns a copy to each caller. Entries are cleared when they expire or are dropped;
  errors are not kept. The event relay opens signing secrets through it (6.4 §11): one request to the
  service per destination and lifetime instead of one per round and secret. It is built only when a
  transit provider is configured.
  The cache needs no invalidation to be correct: an entry can only be reached by presenting the same
  wrapped bytes, which the database stops returning when the data key is erased or re-wrapped. The
  bound in time of ADR 0068 point 5 holds for the worker too.
- **The sealer and the re-wrap job** use the provider directly: a write of a secret is rare, and the
  job must see the service.

### 5.10 Start, readiness and behaviour during an outage (D-13, D-14)

**Start.** `serve` builds the egress guard, then the providers, then runs this sequence before any
listener is bound:

1. `TransitProvider.Start`: authenticate, describe the key, identify its latest version;
2. `vault.Activate` (retire what can be retired, `Check` with the probe of §5.2, `kek_activate`).

A failure that is `ErrProviderUnavailable` is logged (`key provider unavailable`, `phase: start`,
every 30 s at most) and the sequence is tried again after 1 s, 2 s, 4 s … 15 s, until it succeeds,
the process is told to stop, or `vault.kek.transit.start_timeout` has passed (default `0`: no limit;
up to 1 h), in which case `serve` ends with `startup failed`. Any other failure — a refused token, a
missing key, a key of the wrong kind, associated data not bound, a version the database needs and
the configuration does not provide, a replaced version — ends `serve` at once, as today. While it
waits the process listens on no port: **it serves nothing**, `/healthz` and `/readyz` included, and
it starts by itself when the service answers (`key provider recovered`, then `vault ready`). An
orchestrator that probes a waiting process needs a start-up allowance, which the guide states.

**Running.** `/readyz` does not depend on the key service once the process has started. All
instances share the service, so a failing check would withdraw every instance at once and with them
the calls that cached keys still serve, and the administration API, most of which needs no key. The
outage is refused per request and shown by the line `key provider unavailable`, the gauge
`pandokos.vault.provider.up` and the outcome counter (O-1).

| During an outage of the key service | Behaviour |
|---|---|
| Inference, credential or data key cached | continues until `cache.credential_ttl` and `cache.data_key_ttl` end |
| Inference, nothing cached | `503 unavailable`, `Retry-After: 5`, after at most `request_timeout` (at once while the refusal is in force); nothing is forwarded; `credential cannot be opened`, cause `kek_provider_unavailable` |
| Writing or replacing a credential or a signing secret, creating a tenant's first secret | `503 unavailable`; nothing is stored; `credential cannot be sealed` |
| Every other administration operation | unaffected |
| Event relay | a round whose secret cannot be opened fails without consuming an attempt (6.4); events wait in order |
| Re-wrap job | `key rewrap failing`, cause `kek_provider_unavailable`; no data key is half done; the next pass continues |
| A restart | waits as above |
| The token expires and cannot be renewed | as an outage for the requests that follow, class `denied`; `key provider authentication failing` |
| A version the database needs is no longer decryptable in the service | `serve` does not start, naming the version; while running, the tenants under it get `503`. Trimmed: those data keys are lost (lost-key procedure, tenants enter their credentials again) |

### 5.11 Configuration

All keys are read by `serve` and `kek check` only. Environment names follow the rule
(`PANDOKOS_VAULT_KEK_TRANSIT_ADDRESS`, `…_AUTH_APPROLE_SECRET_ID_FILE`).

| Key | Type | Default | Rule |
|---|---|---|---|
| `vault.kek.provider` | `local`, `transit` | `local` | the provider that wraps |
| `vault.kek.transit.address` | URL | empty | `https`; `http` only for a loopback host; a host, an optional port, no user information, path, query or fragment. Required with `provider: transit`; set with `provider: local` it makes the service an unwrap-only provider |
| `vault.kek.transit.namespace` | string | empty | segments of `[A-Za-z0-9_-]` joined by `/`, 255 characters at most |
| `vault.kek.transit.mount` | string | `transit` | the same grammar, 128 at most |
| `vault.kek.transit.key_name` | string | empty | `^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`; required with an address |
| `vault.kek.transit.tls.ca_file` | path | empty | a PEM file with at least one certificate; the only authorities trusted for the service |
| `vault.kek.transit.auth.method` | `token`, `approle` | empty | required with an address |
| `vault.kek.transit.auth.token_file` | path | empty | required with `token`, refused with `approle` |
| `vault.kek.transit.auth.approle.mount` | string | `approle` | the grammar of the mount |
| `vault.kek.transit.auth.approle.role_id_file` | path | empty | required with `approle`, refused with `token` |
| `vault.kek.transit.auth.approle.secret_id_file` | path | empty | the same |
| `vault.kek.transit.request_timeout` | duration | `2s` | 100 ms to 30 s |
| `vault.kek.transit.max_concurrent_requests` | integer | `32` | 1 to 1024 |
| `vault.kek.transit.start_timeout` | duration | `0` | 0 to 1 h; 0 waits without limit |

`vault.kek.provider` exists already; the other thirteen are new. Problems, each naming its key and
never the content of a file: any `vault.kek.transit.*` key set without an address; `provider:
transit` with `vault.kek.local.key_file` set ("must be empty when vault.kek.provider is transit;
list the file in vault.kek.local.unwrap_key_files while data keys are re-wrapped"); `provider:
local` without a key file (as today); a secret file that is missing, empty, too large or holds a
control character (the rules of `secret.ReadFile`). The files are read at load only to be checked;
the provider reads them itself, each time it authenticates. Warnings: a secret file that other
users can access (as for the key file); an `http` address ("the connection to the key service is not
encrypted").

### 5.12 Command line (D-17)

- `pandokos kek check` — new. Loads the configuration as `serve` does, builds the providers,
  authenticates, and runs `vault.Probe`, the part of `vault.Check` that needs no database (the round
  trip and the negative probe of §5.2), after each provider's own description. It prints the provider that wraps, its version identifier, for a
  transit provider the range of versions it can unwrap and the weak settings of §5.3, and ends with
  0; any failure prints its class and ends with 1. It writes nothing anywhere.
- `pandokos kek generate` — unchanged; it makes key files only.
- `pandokos kek-version list` — unchanged; `provider` shows `transit`.
- `serve` — §5.10. `migrate`, `init`, `config check` and the client commands do not contact the key
  service; `config check` applies the rules of §5.11.

### 5.13 Secrets and canaries

What is secret here: the token, the secret ID (and the role ID, treated alike), every data key in
clear, and — because a wrapped key plus the service is the data key — the content of wrapped keys.
Rules for the code: a data key in clear never becomes a Go string (the answer's `plaintext` is
decoded from the raw JSON into a byte slice that the caller clears; the request buffer that held its
base64 is cleared after the request); no log line, error, metric attribute, span or audit row holds
an address with a path, a header, a body, a key name, a namespace or a version number of the
service other than inside a `kek_version` identifier.

The test double records every request it receives and every plaintext and ciphertext it handled.
The canary tests (T-1068 in process, T-1086 with the binary, T-1094 against the real services) plant
a canary token, role ID and secret ID, make the double answer errors whose bodies repeat each of
them and the bytes it was sent, drive every path of §5.10, and then search: every log line at
`debug`, standard error, every error string, `/metrics`, the spans received by the collector double,
the rows of both audit logs, the problem responses of both listeners, the pull export of events,
and the output of `kek check` — for the token, the IDs, each data key (raw, hexadecimal, both base64
alphabets) and each wrapped payload. They also search **the requests received by the service** for
tenant IDs, credential IDs and the existing canaries of `fixtures.Canaries()`: nothing but the
fields of §5.1 leaves.

### 5.14 Metrics, logs and documents (D-16)

| # | Instrument | Kind, unit | Attributes | Meaning |
|---|---|---|---|---|
| M59 | `pandokos.vault.provider.requests` | OC, `{request}` | `pandokos.kek.operation` (`wrap`, `unwrap`, `describe`, `identify`, `lookup`, `renew`, `login`), `pandokos.kek.outcome` (`ok`, `unavailable`, `denied`, `not_found`, `rejected`, `refused`) | Requests to the key service by operation and outcome; `refused` were not sent |
| M60 | `pandokos.vault.provider.request.duration` | H, `s` | `pandokos.kek.operation` | Time of one request that was sent, waiting for a slot included |
| M61 | `pandokos.vault.provider.up` | G, `{provider}` | none | 1 while requests are sent, 0 while the refusal of §5.8 is in force; not reported without a transit provider |
| M62 | `pandokos.vault.provider.token.ttl` | G, `s` | none | Seconds until the token of the key service expires; not reported for a token that does not expire |

No attribute names a tenant, an address, a namespace, a key or a version (ADR 0055, 0062): 42
attribute sets at most for M59, 7 for M60.

| Log line | Level | Attributes | When |
|---|---|---|---|
| `key provider ready` | info | `kek_provider`, `kek_version`, `auth_method`, `oldest_version`, `newest_version` | `Start` succeeded |
| `key provider configuration weak` | warn | `kek_provider`, `setting` | once per weak setting at start (§5.3) |
| `key provider unavailable` | warn | `kek_provider`, `phase` (`start`, `run`), `operation`, `error_class` (`timeout`, `connection`, `tls`, `status`, `response`), `failures` | at start while waiting; running, when the refusal begins and every 30 s while it lasts |
| `key provider recovered` | info | `kek_provider`, `phase`, `outage_seconds` | the first success after that line |
| `key provider token expiring` | warn | `kek_provider`, `auth_method`, `expires_in_seconds` | §5.5 |
| `key provider authentication failing` | error | `kek_provider`, `auth_method`, `error_class` (`denied`, `not_found`, `rejected`, or one of the above), `failures` | a login, renewal or adopted token refused; every 30 s at most |
| `key provider no longer needed` | info | `kek_provider` | at start: a provider configured for unwrapping only owns no `active` or `retiring` version |

`vault ready` keeps its attributes and gains `kek_unwrap_provider` (the name of the provider
configured for unwrapping only, or empty). `cause` gains the value `kek_provider_unavailable`.
Logins, adoptions and renewals that succeed are counted by M59 and not logged.

Documents (texts in plan part E): the new guide `docs/operations/external-key.md` (creating the key,
the policy, a token or an AppRole for both services; the keys; `kek check`; moving to the service
and back for one and for several instances; rotating; erasure and trimming; outage behaviour and the
numbers of §5.8; backups; loss and compromise of the key, the token or the secret ID; limits); a new
section "2a. The key service" in `docs/reference/failure-behaviour.md` with the rows of §5.10, each
citing its test; the vault, backup, tenant deletion, events, telemetry and configuration guides; the
log and metrics references; the overview (the dependency row and §9) and the security model
(boundary 4, the threats "the token of the key service leaks" and "the key service is compromised or
lost", the hierarchy figure); the requirements page (VLT-12 delivered for a transit service); the
draft release notes.

## 6. Isolation and permissions

No table, policy, grant, route or permission changes. The provider reads and writes no row: the
database is touched only by `vault.Check` and `vault.Activate` through the queries that exist
(`ListKEKVersions`, `RetireKEKVersions`, `ActivateKEKVersion`), in the roles and scopes they use
today. `kek_activate` is audited as before (`kek.activate`, actor `host`), with `provider: transit`
in its detail, and the event `kek.version_activated` follows.

The key service sees, per request: the token, a 32-byte value to encrypt or decrypt, a 32-byte
digest, and for `identify` a constant label. It learns no tenant, no credential and no caller; the
rate of `unwrap` requests reveals how many tenants become active per cache lifetime, nothing else.
The admin role's path to unwrap (ADR 0008) and what a memory dump shows are unchanged; what changes
is that a copy of the hosts' disks and of the database, without the token, opens nothing — and that
whoever holds the token and the database can unwrap every data key for as long as the token lives
(security model, plan E).

## 7. Test infrastructure and CI

- **`internal/vault/transittest`**: an in-process double of the service, with real AES-256-GCM,
  associated data, key versions, `min_decryption_version`, `min_encryption_version`, trim, HMAC,
  tokens with lifetimes, AppRole, namespaces and the answers observed in §12 (statuses and the shape
  `{"errors":[…]}`), plus the faults a test needs: sealed, a status, a delay, a hang, a dropped
  connection, a redirect, error bodies that repeat canaries, and "ignores `associated_data`" (what
  Vault before 1.13 does). It sits under `internal/vault/` because the repository allows AES there
  only (T-R21). It can stop and start again on the same address with its state.
- **`test/transit`**: `conformance.go` holds the suite as functions of a `Target` (address, vendor,
  an administrator's token to create keys, policies, tokens and roles). `fake_test.go` runs it against
  the double in the ordinary `test` job, so the double is held to the behaviour of the real services;
  `real_test.go` and `binary_test.go` (build tag `transit`) run it, and the exit scenarios with the
  built binary, against the service of `PANDOKOS_TEST_TRANSIT_ADDRESS`. Outages of a real service
  are made by a TCP relay inside the test that stops accepting, drops or delays; a real service is
  never sealed.
- **`test/transit/compose.yaml`**, project `pandokos-testtransit`: the two services in development
  mode on loopback ports, images pinned by tag and digest (§12 gives the ones observed and the
  command that proves them). `up.sh` takes the vendor, generates the root token in the git-ignored
  `local/transit/`, and writes `local/transit/env`; `down.sh` removes everything.
- **CI**: a job `transit` with `strategy.matrix.vendor: [openbao, vault]`: database up, `make
  transit-up VENDOR=…`, `make test-transit`, logs on failure, down. The ordinary `test` job runs
  everything else, the conformance suite against the double included. The HashiCorp Vault image is
  used as a test service only; nothing of it is linked or redistributed.
- Repository tests T-R85 to T-R88 hold the rules of this section and of §5.1, §5.6 and §5.14.

## 8. Decisions

Each is recorded in ADR 0073 unless another record is named.

| # | Decision | Reason |
|---|---|---|
| D-1 | The provider `transit` uses seven requests of the transit and token APIs through a small client in `internal/vault` over `net/http`; no vendor module | Decision 6 of the maintainer; the surface is small enough to own and to test against both services (ADR 0002) |
| D-2 | Associated data are sent as `associated_data`, as the SHA-256 of a label and the vault's associated data. The binding is proved at every start by a negative probe in `vault.Check` (`ErrAADNotBound`), not assumed from a version; observed to hold from Vault 1.13 and OpenBao 2.0 and to be silently ignored by Vault 1.8 to 1.12. No inner envelope | The service's AEAD does the binding on the pinned versions of both; the digest keeps tenant identifiers out of the key service; the probe catches the one dangerous case, a field accepted and ignored |
| D-3 | The key must be `aes256-gcm96`, not derived, and exist; exportable, plaintext backup, deletion allowed, automatic rotation and a root token are warnings | One algorithm, the one the vault uses; the warnings name settings that weaken "the key never leaves the service" without stopping an operator who chose them (O-13) |
| D-4 | A version identifier is `transit-<16 hex>-v<N>`: the fingerprint is an HMAC computed by the service under that version. **Amends ADR 0033 point 5** (an external provider's identifier is derived from the key by the service) | The rule of ADR 0033 — an identifier cannot be attached to the wrong key — kept for a key that cannot be read; independent of address, namespace, mount and name |
| D-5 | A wrapped key is `0x02 ‖ payload`; `Wrap` pins `key_version` to the version found at start; a rotation in the service takes effect at the next start | The database accepts data keys under the active version only (ADR 0068 point 1); ADR 0068 point 7 stays true |
| D-6 | Pandokos never rotates, configures, trims or deletes the service's key and ships a policy that forbids it; no rotation after a deletion or on a schedule. Answers O-8 of 6.3: possible, not automated here | A token that can destroy key versions turns a bug or a theft into the loss of every credential; four requests by the operator's scheduler do the same |
| D-7 | Authentication by a token file or by AppRole files; Pandokos renews, logs in again and adopts a replaced token file; secrets are files only | Decision 2 of the maintainer; ADR 0019 (no secret is a value); a token file re-read on refusal lets an agent of the key service feed it |
| D-8 | Several providers at once through `vault.Router`, routed by the identifier's prefix; a provider that does not wrap takes part when it is configured; `Check` asks which provider owns a version; a move between providers is an activation and the re-wrap of 6.3; back to a key file needs a new key file | Reuses ADR 0068 whole; the several-instances procedure is the one of key files; a single-provider installation runs unchanged code |
| D-9 | `vault.kek.transit.namespace` is sent as `X-Vault-Namespace` on every request | Decision 3 of the maintainer |
| D-10 | The client connects through a dedicated transport of the egress guard: private addresses allowed, forbidden ones refused, `https` with verification always, `http` for loopback only, an exclusive `ca_file`, no proxy, no redirect. **Amends ADR 0038 point 7** (a fourth consumer and a third transport) | ADR 0038 point 1; the key service is the operator's host, like the collector; pinning the authority narrows who can stand in for the service |
| D-11 | One attempt per call, a deadline of 2 s by default, 32 concurrent requests, identical unwraps shared, refusal for 5 s after 5 consecutive unavailable answers | No unbounded latency in the path that opens credentials; no retry storm on a service that is failing; a cold start of many tenants cannot flood the service |
| D-12 | The gateway's caches are unchanged and never used past their lifetime; the worker opens signing secrets through `vault.UnwrapCache` (content-addressed, `cache.data_key_ttl`, 10 000 entries), built only with a transit provider | VLT-06 and ADR 0033 point 6; delivers 6.4 §11 without a second copy of keys in installations that do not need it |
| D-13 | `serve` waits for the key service before it binds any listener, without limit by default (`start_timeout`), and ends at once for causes only a person can repair | The exit criterion: serves nothing, recovers alone, with or without a supervisor (O-2) |
| D-14 | `/readyz` does not depend on the key service once started; an outage is refused per request | The overview's rule "continues while data keys are cached, then refuses"; a shared dependency in readiness withdraws every instance and the cached calls with them (O-1) |
| D-15 | `ErrProviderUnavailable` and the cause `kek_provider_unavailable`; error texts are built from an operation and a class; error bodies are never read into anything. No new problem code | ADR 0020; SEC-03; an answer of the service can repeat a secret |
| D-16 | Four instruments (M59 to M62) and seven log lines, none naming a tenant, an address, a key or a namespace | ADR 0055, 0062, 0063 |
| D-17 | `pandokos kek check` proves a provider's configuration without a database and without writing | A move between providers should fail before the restart, not during it |
| D-18 | A test double under `internal/vault/transittest`, one conformance suite run against the double, OpenBao and Vault, a CI job with a matrix of the two real services, images pinned by digest | Decision 1 of the maintainer; ADR 0047's model; the double is only trusted because the same suite passes on the real services |
| D-19 | A webhook of the platform stays deferred (6.4 O-5) | It needs a platform data key and a store for platform secrets, which an external key-encryption key does not provide (O-5) |
| D-20 | No migration, no OpenAPI change, no permission | `kek_version` and `kek_activate` already carry a provider name; a check value stored with a version was considered and replaced by D-4, which needs no schema |
| D-21 | Cloud key services, PKCS#11, KMIP, Kubernetes authentication and two transit services at once are out | Decisions 2 and 5 of the maintainer; §2 |

## 9. Tests

Levels: **U** unit (the double of the service in process); **D** needs the test database; **E** the
built binary, end to end, with the double; **X** the real OpenBao and HashiCorp Vault services (build
tag `transit`, both legs of the CI matrix); **R** repository test. IDs continue from T-1056 and
T-R84.

### New tests

| ID | Test | Level | Asserts | Requirement |
|---|---|---|---|---|
| T-1057 | `vault.TestTransitWrapUnwrap` | U | A data key wraps and unwraps to the same bytes; the requests are exactly those of §5.1 (method, path with a mount and a key that need escaping, headers, the fields of the body and no other); `key_version` is the version of the start, also after the double rotated; an answer with another `key_version` or prefix is refused; the wrapped value is `0x02` and the decoded payload; a value starting with another byte, shorter than 29 bytes or longer than 1024 is `ErrUnwrap` without a request; an answer over 64 KiB is refused | VLT-12 |
| T-1058 | `vault.TestTransitAssociatedData` | U | `associated_data` is the base64 of SHA-256 of the label, a zero byte and the vault's associated data (a fixed vector); the raw associated data and the tenant ID appear in no request; unwrapping under other associated data, under none, with a flipped bit, or a payload of another tenant's row is `ErrUnwrap`; nil associated data are accepted and bound | VLT-12, VLT-14 |
| T-1059 | `vault.TestTransitVersionIdentifier` | U | The identifier is `transit-`, 16 hexadecimal characters of the HMAC of the label under the version, `-v` and the number, and matches the format check of `kek_version`; it is the same after a restart of the provider and with another mount, namespace or address for the same key, and differs for another version and for a key of the same name created again; `Available` and `Unwrap` answer `ErrKeyVersionUnknown`, and send no `decrypt`, for: a wrong fingerprint with a valid number, a number above the latest or below `min_decryption_version`, a version the service refuses to identify, and each malformed identifier of a table; a fingerprint is verified by one `identify` and then kept | VLT-12 |
| T-1060 | `vault.TestTransitKeyRequirements` | U | `Start` fails, with a text naming the setting, for a missing key, a soft-deleted key, `chacha20-poly1305`, `rsa-2048`, a derived key and a key that does not support decryption; it succeeds and reports each weak setting of §5.3 from the closed list, one line each, for exportable, plaintext backup, deletion allowed, automatic rotation and a root token | VLT-12, SEC-04 |
| T-1061 | `vault.TestTransitErrorClasses` | U | The table of §5.8, per operation: the class, `errors.Is` for `ErrUnwrap`, `ErrKeyVersionUnknown`, `ErrUnavailable` and `ErrProviderUnavailable`; the text of every error is one of a fixed set and holds nothing of the answer's body, the address, the key name, the namespace or the token, with the double repeating canaries in its error bodies | VLT-12, SEC-03 |
| T-1062 | `vault.TestTransitOneAttempt` | U | A failing or hanging service receives exactly one request per call; the call returns within `request_timeout` plus 200 ms, and earlier when the caller's context ends; a redirect is not followed and is `unavailable`; the request buffer and the answer buffer that held a data key are zeroed after the call | OPS-11, VLT-06 |
| T-1063 | `vault.TestTransitRefusal` | U | With a test clock: five consecutive unavailable answers start the refusal, calls are then refused without a request for 5 s, one request passes after it, a success ends it and a failure renews it; `400`, `403` and `404` never start it; with every slot busy a further call waits and fails as `refused` at its deadline; 50 concurrent identical `Unwrap` calls make one request and each gets a copy that survives the others clearing theirs; `Stats` counts every outcome of §5.14 | OPS-11 |
| T-1064 | `vault.TestTransitTokenAuth` | U | The token comes from the file; `lookup` gives its lifetime; with a test clock it is renewed at two thirds; a token that cannot be renewed logs `key provider token expiring`; a file replaced on disk is looked up and adopted at the next tick and on a `403`, and the refused request is sent once more and no more; a refused `lookup` fails `Start` as `denied`; a token that does not expire is never renewed; a file with a control character or an empty file is refused without quoting it | VLT-12 |
| T-1065 | `vault.TestTransitAppRoleAuth` | U | Login with the two files at the configured mount; renewal, then a new login when the lifetime granted shrinks to a third; a `403` on a request makes one login shared by 20 concurrent calls and each is sent once more; a login refused while running keeps the current token, logs `key provider authentication failing` and backs off from 1 s to 60 s; the secret ID file is read again at every login; a login answered `503` is `unavailable` | VLT-12 |
| T-1066 | `vault.TestTransitNamespace` | U | With the key set, every request, the login included, carries `X-Vault-Namespace`; without it none does; a key in a namespace of the double is not found without the key | VLT-12 |
| T-1067 | `vault.TestTransitPrint` | U | `TransitProvider`, `TransitOptions`, `Router` and `UnwrapCache` print the redaction marker with every verb, as a log value, as JSON and as text | VLT-14 |
| T-1068 | `vault.TestTransitCanary` | U | §5.13 in process: with canary token, role ID and secret ID and every fault of the double, no log line at `debug`, error string (with `%v`, `%+v`, `%#v` and every wrapped error), or `Stats` holds a canary, a data key in any encoding or a wrapped payload; the requests the double received hold no tenant ID | VLT-14 |
| T-1069 | `vault.TestRouter` | U | `Name` and `Wrap` are the active provider's; `Unwrap` and `Available` go to the provider named by the identifier's prefix; an identifier of no provider is `ErrKeyVersionUnknown`; `ProviderOf`; two providers of one name, or none, are refused; a version returned by the active provider with a foreign prefix is refused | VLT-12 |
| T-1070 | `vault.TestUnwrapCache` | U | A second identical `Unwrap` reaches no provider; a difference in version, wrapped key or associated data misses; each result is a copy; an entry is gone after the lifetime and its bytes are zeroed; the cache is emptied at its bound; an error is not kept; `Wrap`, `Available` and `Name` pass through | VLT-06 |
| T-1071 | `vault.TestCheckAssociatedDataBound` | D | `Check` and `Activate` refuse, with `ErrAADNotBound` and before any write to `kek_version`, a provider whose service ignores `associated_data`; they pass for the local provider and for the double; a provider that is unavailable during the negative probe is `ErrProviderUnavailable`, not a pass | VLT-12 |
| T-1072 | `vault.TestCheckSeveralProviders` | D | With versions registered by `local` and `transit` (`active` and `retiring` either way round): the router with both passes; without the unwrap key file, or without the transit provider, the error names the missing version and no key; a row whose `provider` is not the owner of its identifier fails; a `retired` version of an unconfigured provider does not block (T-328 extended) | VLT-12, VLT-16 |
| T-1073 | `vault.TestActivateTransit` | D | An empty database: `activated`, provider `transit`, audited with `provider: transit`; again: `unchanged`; after a rotation in the double: the new identifier is active and the previous one `retiring`; a local active version and a transit provider: the local one becomes `retiring`; a configuration whose version was replaced is `ErrKeyReplaced` with a text that names both versions and no configuration key of the wrong provider | VLT-10, VLT-12 |
| T-1074 | `vault.TestRewrapAcrossProviders` | D | Key file → service, service → a new key file, and version 1 → 2 of the service: every data key ends under the active version with the format byte of its provider, every credential and signing secret opens before, during and after, sealed credentials are byte for byte unchanged, the old version is `retired` and `key rotation complete` names it; with the service away in the middle of a pass: `key rewrap failing`, cause `kek_provider_unavailable`, no row half written, and the next pass completes | VLT-10, VLT-12 |
| T-1075 | `vault.TestOpenerWithTransit` | D | One `unwrap` per tenant per `cache.data_key_ttl`; 50 concurrent first calls of a tenant make one; with the service away a cached tenant opens until the lifetime ends and not after, a tenant not cached gets `ErrUnavailable` and the line `credential cannot be opened` with `kek_provider_unavailable`; when the service is back the next call opens; a data key erased in the database is not opened from any cache after the lifetimes | VLT-06, OPS-11 |
| T-1076 | `outbox.TestRelayUnwrapCache` | D | With a transit provider, ten rounds for a destination with a current and a previous secret make one `unwrap`; after the lifetime, one more; with the service away a round fails without consuming an attempt and the events are delivered in order when it returns | VLT-06, OPS-11 |
| T-1077 | `egress.TestKeyServiceTransport` | U | The transport of `KeyService` has its own pool; with `roots` given it accepts only a certificate of those authorities (one signed by another authority that `upstream.tls.ca_file` trusts is refused); a certificate for another name, an expired one and TLS 1.1 are refused; it uses no proxy when `upstream.proxy.url` is set and ignores proxy variables; it follows no redirect; it connects to loopback and private addresses and refuses the metadata address | SEC-01 |
| T-1078 | `config.TestTransitKeys` | U | The defaults, environment names, bounds and every problem and warning of §5.11 as a table (among them: `provider: transit` with a key file; `http` to a host that is not loopback; user information, a path, a query; a method without its files and with the other method's; a namespace, mount and key name outside their grammar; each duration and integer at and beyond its bounds); `Keys()` lists the thirteen keys; `migrate` and `init` read none of them; no message quotes a file's content | OPS-08, SEC-04 |
| T-1079 | `telemetry.TestKeyProviderInstruments` | U | M59 to M62 exist with their kinds, units and Prometheus names; every attribute value is from the lists of §5.14 and the attribute sets stay under the cap; M61 and M62 are absent without a transit provider and M62 for a token that does not expire | OBS (ADR 0062) |
| T-1080 | `main.TestKEKCheck` | U | `pandokos kek check` against the double prints the provider, the identifier, the range of versions and the weak settings and ends with 0; for a refused token, a missing key, associated data not bound and a service away it prints the class and ends with 1 within the request timeout; it opens no database connection; its output holds no canary | VLT-12 |
| T-1081 | `main.TestServeWaitsForKeyProvider` | D | The start sequence of §5.10 with a test clock: unavailable → waits with the backoff of 1 s to 15 s and one warning per 30 s; the service returns → proceeds; `start_timeout` passed → the error; each permanent cause → the error at once, no wait; a stop signal ends the wait | OPS-11 |
| T-1082 | `e2e.TestTransitFreshInstall` | E | **Exit (no key on the hosts).** The binary with `provider: transit`, AppRole and no `vault.kek.local.*` key at all: `key provider ready`, `vault ready` with `kek_provider: transit` and `activated`; a tenant registers a credential and calls a model; the data key row has a `transit-` version and a first byte `0x02`; `kek-version list` and the platform's audit log show the provider; the event `kek.version_activated` is exported | VLT-12 |
| T-1083 | `e2e.TestMoveBetweenProviders` | E | **Exit (no action by tenants).** Two tenants on a key file; restart with the service and the old key as unwrap key: calls of both succeed at every poll while the worker re-wraps, `key rotation complete` names the local version, `key provider no longer needed` appears at the next start; restart with **no key file configured**: every credential works; then back to a new key file with the service as unwrap provider, to `retired`, and a last start without the service configured and with the double stopped. Throughout, the rows of `tenant_credential` and of the destinations' secrets are unchanged (bytes and `updated_at`) and no tenant operation was called. A start with the old key file as `key_file` is refused, naming both versions | VLT-10, VLT-12 |
| T-1084 | `e2e.TestTransitOutage` | E | **Exit (serves nothing, recovers alone).** Running, with short cache lifetimes, the double stopped: a cached tenant is served until the lifetime and then gets `503`; a tenant not cached gets `503 unavailable` with `Retry-After` within `request_timeout`; a credential write gets `503` and stores nothing; `/readyz` stays 200 and administration reads work; M61 is 0. The binary is restarted during the outage: it does not exit, both listeners refuse connections, `key provider unavailable` with `phase: start` is logged; the double starts again: `key provider recovered`, `vault ready`, calls succeed, with no action. With `start_timeout: 3s` the binary ends with 1 and `startup failed` | OPS-11, VLT-12 |
| T-1085 | `e2e.TestTransitStartRefusals` | E | The binary ends with 1 within 10 s, with a constant message, for: a refused token; a missing key; a key of another type; a service that ignores associated data; a key of the same name created again (the active identifier is not provided); `min_decryption_version` above a `retiring` version; a transit version needed with no transit provider configured | VLT-12, VLT-16 |
| T-1086 | `e2e.TestTransitCanary` | E | §5.13 with the binary: a run that registers credentials and destinations, calls, moves between providers, lives through an outage, an expired token and a refused login; then every output listed in §5.13 is searched for the token, the IDs, each data key and wrapped payload the double handled, and the requests the double received are searched for tenant and credential IDs and the fixtures' canaries | VLT-14 |
| T-1087 | `e2e.TestTransitRotation` | E | A rotation in the double while the binary runs changes nothing (a new tenant's data key is under the active version); a restart activates the new version and the old one is retired; a second instance not restarted still opens and answers `503` with `kek_replaced` for a new tenant's first secret; after the retirement, `min_decryption_version` raised above the retired version does not disturb a start | VLT-10 |
| T-1088 | `e2e.TestTransitTokenLifetime` | E | With AppRole tokens of 3 s and a data key lifetime of 1 s, 12 s of calls for several tenants never fail, and the double saw at least two renewals or logins; the same with a renewable token file | VLT-12 |
| T-1089 | `e2e.TestTransitTelemetry` | E | After traffic, an outage and a refused login: `/metrics` shows M59 to M62 with values consistent with the requests the double counted; each line of §5.14 was written at least once with exactly its attributes, and the log reference describes it | OBS (ADR 0062, 0063) |
| T-1090 | `transit.TestConformance` | X | Against each real service, with the policy file of the repository loaded as it is: wrap and unwrap; other associated data, none, and a flipped bit are `ErrUnwrap`; `vault.Check` passes with its negative probe; the version stays pinned after a rotation; identifiers are stable across provider restarts and distinct per key and version; `min_decryption_version` and trim make a version `ErrKeyVersionUnknown`; a wrong key name is `not_found` or `denied` and creates no key; with the policy's token, rotate, configure, trim, delete, export, read another key and encrypt with another key are each refused by the service | VLT-12 |
| T-1091 | `transit.TestConformanceDouble` | U | The same functions as T-1090 against the double, in the ordinary suite | VLT-12 |
| T-1092 | `transit.TestAuthMethods` | X | A renewable token with a lifetime of 4 s and AppRole tokens of 4 s with a maximum of 8 s: 20 s of wraps and unwraps never fail; a wrong secret ID is `denied`; a token that expired is `denied` and a replaced token file is adopted | VLT-12 |
| T-1093 | `transit.TestNamespace` | X | OpenBao: a key in a namespace created by the test is used with the namespace key and not found without it. HashiCorp Vault community edition: skipped, saying namespaces are an Enterprise feature; T-1066 covers the header | VLT-12 |
| T-1094 | `transit.TestBinaryExit` | X | **The three exit criteria against each real service**, through the test's TCP relay: a fresh install with no key file; a move from a key file to the service with calls succeeding throughout and a last start without a key file; an outage, a restart during it that listens on nothing and does not exit, and recovery when the relay accepts again; the logs hold neither the token nor the secret ID | VLT-12, OPS-11, VLT-14 |
| T-R85 | `repo.TestTransitClientDependencies` | R | `go.mod` and `go.sum` name no module of the two vendors; the files `internal/vault/transit*.go`, `router.go` and `unwrapcache.go` import the standard library and `internal/` only, and construct no `http.Transport`, `net.Dialer`, `tls.Config` or `http.Client` with a default transport; `InsecureSkipVerify` appears nowhere outside tests | SEC-01 |
| T-R86 | `repo.TestTransitDocuments` | R | The guide has the sections of §5.14 and the constants of §5.8; the failure reference has section 2a with a row per line of §5.10's table, and every test, log line, instrument and key it cites exists (T-R80's rule); the configuration reference lists the thirteen keys and their environment names; the log and metrics references have the seven lines and M59 to M62; ADR 0073 is indexed and 0033 and 0038 carry their history lines; the release notes name the provider; the policy in the guide is the file `deploy/transit/pandokos-policy.hcl` byte for byte, and that file is the one T-1090 loads | OPS-11, VLT-16 |
| T-R87 | `repo.TestTransitSuitePinned` | R | Each image of `test/transit/compose.yaml` has a tag and a `sha256` digest; the workflow has the job `transit` with both vendors and no `continue-on-error`; the `Makefile` has the three targets; `local/transit` is ignored by git | SEC-06 |
| T-R88 | `repo.TestKeyServiceOneClient` | R | The header name `X-Vault-Token` and the paths of §5.1 appear in `internal/vault` only; `NewTransitProvider` is called from `cmd/pandokos` and tests only; `Wrap` and `Unwrap` of a key provider are still called from `internal/vault` only, with `cmd/pandokos/kek.go` as the listed exception (T-R78 updated) | VLT-14 |

### Changed tests of earlier sprints

| Test | Change |
|---|---|
| T-328 `vault.TestCheck` | "a registered version of another provider name fails" becomes "fails unless the router owns it"; the negative probe is part of the check |
| `vault.TestActivateAtStart` (6.3) | the text of `ErrKeyReplaced` no longer names `vault.kek.local.key_file` when the active version is not local |
| T-R78 `repo.TestKEKOneWriter` | lists `kek check` with `kek generate` |
| T-R80 `repo.TestLifecycleDocuments`, T-R84 `repo.TestEventDocs` | the failure reference has one more section; the re-wrap and relay rows cite the key service |
| `config` tests of `vault.kek.provider` ("must be local") and of the count of `Keys()` | `transit` is accepted; thirteen more keys |
| `telemetry` catalogue, cardinality and reference tests; `repo.TestMetricsReference`, `repo.TestLogReference` | four instruments, seven lines, one attribute value of `cause`, two attributes |
| `e2e` tests reading `vault ready` | one more attribute |
| `egress` statistics tests | a third transport |

### Requirement coverage

| Requirement | Tests |
|---|---|
| VLT-12 (external) | T-1057 to T-1066, T-1069, T-1071 to T-1074, T-1080, T-1082 to T-1085, T-1088, T-1090 to T-1094 |
| VLT-10 (touched) | T-1073, T-1074, T-1083, T-1087 |
| VLT-06 (touched) | T-1062, T-1070, T-1075, T-1076 |
| VLT-14 (touched) | T-1058, T-1067, T-1068, T-1086, T-1094, T-R88 |
| VLT-11, VLT-13, VLT-16 (documentation) | T-1072, T-1085, T-R86 |
| OPS-08, SEC-04 | T-1078, T-1060 |
| OPS-11 | T-1062, T-1063, T-1075, T-1076, T-1081, T-1084, T-1094, T-R86 |
| SEC-01, SEC-03 | T-1077, T-R85, T-1061 |

## 10. Definition of done for this sprint

- [ ] Every test of §9 exists under its name, failed before its implementation, and passes; the
      changed tests are changed as listed.
- [ ] VLT-12 is met for a transit service and covered; the three exit tests pass with the double
      (T-1082 to T-1084) and against OpenBao and HashiCorp Vault (T-1094), in CI.
- [ ] `vault.Check` refuses a service that does not bind associated data (T-1071, T-1085), and the
      result of §12 is recorded in ADR 0073 with the versions observed.
- [ ] No token, role ID, secret ID, data key or wrapped key appears in any output, and no tenant
      identifier in any request to the key service (T-1068, T-1086, T-1094).
- [ ] An installation with a key file and no `vault.kek.transit.*` key behaves as before: the tests
      of sprints 2.1, 6.3 and 6.4 pass with the changes of §9 only.
- [ ] `go.mod` gains no module (T-R85); the policy of the guide is the tested file (T-R86).
- [ ] `make check` passes, and the `transit` job passes on both legs.
- [ ] The external-key guide, the failure reference, the vault, backup, tenant deletion, events,
      telemetry and configuration guides, the overview, the security model, the log and metrics
      references, the requirements page, ADR 0073 with the history lines, and the draft release notes
      of v0.3.0 are current.

## 11. Consequences for later sprints

- **12.2 (threat model review and fuzzing).** Boundary 4 (process to key provider) has a network
  form: the review takes the token's reach (whoever holds it and the database unwraps every data
  key while it lives), the policy file, the exclusive `ca_file`, the loopback `http` exception and
  the digest of associated data as controls, each with its test from §9. The client's answer parser
  (`internal/vault/transit_client.go`) reads from the network and joins the fuzz targets.
- **12.3 (restore drill, upgrade, configuration alone).** The drill runs once with a key service:
  a database backup needs the service's key versions that wrapped its data keys, so the service's own
  backup and the rule "do not trim what a kept backup needs" are part of it; the four cases of the
  key gain the service's column, with the loss of a trimmed version as the unrecoverable one. The two
  instances started from two configurations include one with `provider: transit`. The upgrade test
  from the previous minor needs nothing for this sprint (no migration).
- **8.1 and 8.2 (replicas, Kubernetes).** Step 0 of §5.7 is the procedure for replicas; the chart
  carries the thirteen keys, mounts the token or AppRole files as secrets, gives the pods a start-up
  allowance for the wait of §5.10, and adds Kubernetes authentication (a second `auth.method`, or an
  agent that writes `auth.token_file`, which works today).
- **10.1 (failure injection, figures).** The rows of section 2a of the failure reference are its
  list for the key service; the added latency of a first call of a tenant with a remote key service
  and the numbers of §5.8 are measured there.
- **Issue #75 (cloud key services).** A further provider implements `KeyProvider`, derives its
  identifiers from the key as D-4 asks (or documents why it cannot), starts its identifiers with its
  name, and passes `vault.Check` with the negative probe; `Router` takes it without change. A direct
  move between two external providers needs `Router` to hold two of them, which it can; only the
  configuration has one block today (O-11).
- **A platform webhook (6.4 O-5)** needs a platform data key and its own associated data; it is not
  nearer after this sprint and is tracked as an issue, not as a sprint (O-5).

## 12. Risks and open points

### What was verified while designing

Run on 2026-11-24 with Docker against `openbao/openbao:2.7.3` and `hashicorp/vault:2.2.2` in
development mode, and against older images where a version is named; the containers were removed.

| Fact | How |
|---|---|
| **Associated data are bound** by `encrypt` and `decrypt` for an `aes256-gcm96` key on OpenBao 2.7.3 and Vault 2.2.2: decrypting with other `associated_data`, with none, with a changed ciphertext, a ciphertext encrypted without the field, or a version-1 ciphertext relabelled `v2`, each answers `400` `cipher: message authentication failed` | `curl` against both |
| **Minimum versions**: bound on Vault 1.13.13, 1.21.4 and OpenBao 2.0.0; **accepted and ignored** (the decrypt succeeds with other or no associated data) on Vault 1.8.12, 1.9.10, 1.10.11, 1.11.12 and 1.12.11 | the same requests against those images |
| `associated_data` is refused for a non-AEAD key (`rsa-2048`, `400`) and accepted for `chacha20-poly1305` and for a derived key | the same |
| `encrypt` with `key_version: N` uses version N after a rotation and answers `key_version`; a version above the latest, or below `min_decryption_version`, is `400` | both |
| A ciphertext of a 32-byte value is 89 characters, `vault:v<N>:` and 80 of base64 (60 bytes), on both; OpenBao also writes the prefix `vault:` | both |
| `hmac/<k>/sha2-256` of a fixed input is the same at every call, differs per key version and per key, and accepts `key_version`; it is refused for a version above the latest, below `min_decryption_version`, or below `min_encryption_version` | Vault 2.2.2 and 1.13.13, OpenBao 2.7.3 and 2.0.0 |
| The `keys` map of a key description holds a creation time in seconds per version and loses the versions below `min_decryption_version`; two versions rotated within a second share the time (so a creation time is not an identity; D-4 uses the HMAC) | both |
| `min_decryption_version` can be lowered again and the old ciphertext then decrypts; `trim` needs `min_encryption_version` set and is not reversible (`min_decryption_version` can no longer go below it); a key with `deletion_allowed: false` cannot be deleted | both |
| Every decrypt failure is `400`: wrong associated data, a version too old ("disallowed by policy"), too new, a missing key ("encryption key not found"); a description of a missing key is `404` with an empty error list; a wrong mount is `404`. Hence classes by status and a fresh description, never by text (§5.8) | both |
| With a root token, `encrypt` on a missing key **creates the key**; with a policy granting `update` only it is `403` | both |
| The policy of §5.1 is enough for encrypt, decrypt, describe and identify, and rotate, configure, export and another key are `403`; a token created without the `default` policy cannot `lookup-self` or `renew-self` unless the policy grants them | both |
| A bad, missing or expired token is `403` (`permission denied`; Vault adds `invalid token`); a token is renewed up to its maximum lifetime and the capped renewal answers a shorter `lease_duration` with a warning; a non-renewable token's renewal is `400`; a batch token is not renewable | both |
| AppRole: the login answers `client_token`, `lease_duration`, `renewable`; a wrong role or secret ID is `400` `invalid role or secret ID`; a login at an auth mount that does not exist is `403` | both |
| A sealed service answers `503` `{"errors":["Vault is sealed"]}` to every request, the login and the health endpoint included | both, sealed through the API |
| Namespaces: OpenBao 2.7.3 creates a namespace, mounts transit in it, and finds its key only with `X-Vault-Namespace` (or the path prefix); Vault 2.2.2 community answers `enterprise-only feature` to the creation and ignores the header | both |
| Image digests as pulled: `openbao/openbao:2.7.3@sha256:ba4b133eb9586630024e71a617e66f37f29cc3534cdd46a6233e098ceded8dc9`, `hashicorp/vault:2.2.2@sha256:dfa48c2fa2b0099ccb47681ec5c4804bf5aec9d6879ecfee07ad4b92a5427ad6` | `docker pull <image>:<tag> && docker image inspect --format '{{.RepoDigests}}' <image>:<tag>`; the implementer runs it again before pinning (plan D1) |
| `go.mod` holds no module of either vendor and none is needed; `internal/vault` already imports `net/http`; the repository forbids AES outside `internal/vault/**` (T-R21 and the linter), which the double's location respects | reading the repository |
| `vault.Check` compares each needed version's provider to `p.Name()` and `serve` calls `Activate` before the egress guard exists and before any listener; the opener unwraps outside its transaction, the sealer, `OpenNamed` and the re-wrap job inside theirs | reading `internal/vault` and `cmd/pandokos/serve.go` |

Not verified: any Go code of this design; the two services under load and their latency; a real
standby node (its `307` and `429` are classed from documentation, as unavailable); HashiCorp Vault
Enterprise namespaces; an agent of the key service writing the token file; versions between those
named; the behaviour of a service restored from its backup (12.3).

### Risks

| # | Risk | Mitigation |
|---|---|---|
| R-1 | A service that accepts `associated_data` and ignores it (Vault before 1.13, or a compatible service) | The negative probe at every start and in `kek check` (T-1071, T-1085); the data key's own derivation binds the tenant |
| R-2 | The configuration points at another service or another key of the same name; data keys would be wrapped under a key nobody expects | Identifiers come from the key itself (D-4): `Check` finds the active version not provided and `serve` does not start (T-1085) |
| R-3 | The operator trims or raises `min_decryption_version` above a version still needed | Start refuses and names the version; reversible until trimmed; the guide's order (wait for `key rotation complete` and `erasures_pending`); trimmed is the lost-key case, stated as such |
| R-4 | The token leaks: with the database it unwraps every data key while it lives | Least-privilege policy; short lifetimes with AppRole; files with restricted modes and a warning; never logged (canaries); the guide's response: revoke, rotate the key, re-wrap |
| R-5 | The key service becomes a single point of failure of the data plane | Cached calls continue; refusal is bounded and fast; `cache.data_key_ttl` up to 15 m; the service's own high availability is the operator's; stale use is refused on purpose (O-4) |
| R-6 | A latency of the service slows every first call of a tenant, and holds a database transaction in the sealer, the relay and the re-wrap job | The deadline of 2 s; one attempt; the opener unwraps outside its transaction; the relay's cache; measured in 10.1 |
| R-7 | The two services, or two versions of one, drift apart | One suite on both in CI, images pinned and raised deliberately; answers classed by status, never by text; a double held to the suite |
| R-8 | The double is wrong and the tests pass against a fiction | T-1091 runs the real suite on it; T-1094 runs the exit scenarios on the real services |
| R-9 | Several instances and a move made on one only: the others cannot unwrap what was re-wrapped | Step 0 of §5.7, as for key files (ADR 0068 point 7); the instance that lags answers `503`, never a wrong key |
| R-10 | `auto_rotate_period` rotates the key and a crash restart activates the new version while other instances run | The warning `auto_rotate`; pinned wrap means nothing changes without a restart; the lagging instances still open; the guide advises a deliberate rotation |
| R-11 | A process that waits for ever at start goes unnoticed | A warning every 30 s; `start_timeout` for operators who prefer an exit; the guide's probe allowance |
| R-12 | An error body or a debug line carries a secret | Bodies are never read into strings; error texts are a fixed set (T-1061); the double repeats canaries in its errors (T-1068, T-1086) |
| R-13 | The image of HashiCorp Vault is under a licence that is not open source | It is a test service in CI only, never linked, bundled or redistributed; OpenBao is the reference and both legs must pass |

### Open points (raised to the maintainer; none blocks the sprint)

| # | Question | Recommendation |
|---|---|---|
| O-1 | Once started, should `/readyz` fail while the key service is away? | No (D-14): the outage is the same for every instance, cached calls and most of the administration API still work, and the caller gets the contract's `503` instead of a load balancer's. The alternative, failing after the refusal has lasted `cache.data_key_ttl`, is easy to add if operators ask |
| O-2 | A start during an outage waits without limit by default (`start_timeout: 0`). Or exit after a bounded time and rely on the supervisor? | Wait without limit: it recovers alone with any supervisor or none; the key lets an operator choose the exit |
| O-3 | Should Pandokos rotate the service's key by itself after a tenant deletion or on a schedule, and trim retired versions (6.3 O-8)? | No (D-6): it would need a token able to destroy key versions. The guide gives the operator's rule and the four requests; an opt-in could be designed later behind a separate token |
| O-4 | Should a data key be usable past its lifetime while the service is away? | No: the lifetime is the guarantee of VLT-06 and of erasure; raise `cache.data_key_ttl` (up to 15 m) instead |
| O-5 | The platform webhook stays deferred (6.4 O-5). Confirm, and track it as an issue rather than a sprint? | Confirm: it needs a platform data key and store, which this sprint does not bring nearer |
| O-6 | A rotation in the service takes effect at the next restart of each instance. Or follow the database's active version while running? | Restart, as ADR 0068 point 7: one rule for both providers, and an act the operator sees |
| O-7 | The token, role ID and secret ID are files only, although the decision allowed "a file and/or the environment". Confirm? | Confirm: ADR 0019 and OPS-08 make every secret a file; an environment value would be the first exception and shows in process listings |
| O-8 | `http` is accepted for a loopback address (an agent or proxy of the key service on the host), with a warning. Or `https` always? | Accept loopback only: the token and the data keys do not leave the host in clear; anything else is refused |
| O-9 | Which versions are "supported"? | State: tested on the pinned versions of OpenBao and HashiCorp Vault; expected from Vault 1.13 and OpenBao 2.0 by the observation of §12; enforced by the probe, not by a version check. No CI leg for the minimum versions |
| O-10 | The matrix runs the HashiCorp Vault image, whose licence is not open source, as a test service. Confirm that this is acceptable for the repository's CI? | Confirm (R-13); it is the maintainer's decision 1 |
| O-11 | A direct move between two transit services is not possible (one `transit` block); it goes through a key file. Accept? | Accept for now; a second block is a configuration change only (`Router` already takes several providers) |
| O-12 | No Compose overlay runs a key service. Accept? | Accept: a development-mode service loses its keys at a restart, which is the worst possible example; the guide gives the commands |
| O-13 | An exportable key, a key that allows plaintext backup or deletion, automatic rotation and a root token are warnings. Or refuse to start? | Warn: some operators need exportable keys for the service's own backup procedure; a refusal would block them for a choice that is theirs. `exportable` could become a refusal with an opt-out if the review of 12.2 asks |

### Maintainer decisions on the open points (2026-11-24)

| # | Decision |
|---|---|
| O-1 | Confirmed: `/readyz` does not fail during a runtime outage of the key service; calls that need an unwrap are refused with `503` per request |
| O-2 | Confirmed: `serve` waits for the key service without limit by default; `start_timeout` sets a bound |
| O-3 | Confirmed: no automatic rotation or trim after a deletion or on a schedule; the guide gives the operator's procedure |
| O-4 | Confirmed: data keys are never used past their lifetime during an outage; `cache.data_key_ttl` (up to 15 m) is the operator's lever |
| O-5 | Confirmed: the platform webhook stays deferred; it needs a platform data key, which this sprint does not provide |
| O-6 | Confirmed: a rotation in the key service takes effect at the next restart, as ADR 0068 |
| O-7 | Confirmed: token, role ID and secret ID are read from files only (ADR 0019, OPS-08), not from environment variables |
| O-8 | Confirmed: `http` is allowed for a loopback address only, with a warning |
| O-9 | Confirmed: tested on the pinned versions; expected from HashiCorp Vault 1.13 and OpenBao 2.0; enforced by the probe at start, not by a version check |
| O-10 | Confirmed: the HashiCorp Vault image runs as a CI test service only; it is never linked or redistributed |
| O-11 | Accepted for now: a move between two transit services goes through a key file |
| O-12 | Accepted: no Compose overlay runs a key service; the guide gives the commands |
| O-13 | Confirmed: weak key settings and a root token are warnings, not refusals; 12.2 may revisit `exportable` |
