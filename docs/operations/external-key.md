# The external key-encryption key: OpenBao and HashiCorp Vault

By default the key-encryption key is a file on the host ([vault guide](vault.md)). This guide is for the
operator who wants it to live in a **key service** instead: the transit secrets engine of OpenBao or
HashiCorp Vault. The key then never reaches a disk Pandokos can read; Pandokos asks the service to wrap and
unwrap the 32-byte data keys of tenants and holds a token that is good for that and nothing else.
Decisions: [ADR 0073](../adr/0073-the-transit-key-provider.md), with
[ADR 0008](../adr/0008-vault-envelope-encryption.md), [0033](../adr/0033-vault-formats-and-key-identifiers.md)
and [0068](../adr/0068-key-encryption-key-versions-rotation-and-erasure-in-backups.md). The behaviour of
`serve` when the service is slow or away is in the [failure reference](../reference/failure-behaviour.md#2a-the-key-service).

## 1. What changes

| | Key file (`local`, the default) | Key service (`transit`) |
|---|---|---|
| Where the key-encryption key is | A file read by `serve` | A named key in the transit engine of the service; it is not exported |
| What Pandokos holds | The key | A **token** (or the role ID and secret ID of an AppRole), each in a file |
| Who wraps and unwraps | The process | The service, on request, over HTTP |
| A copy of the database and the hosts' disks, without the token | Opens nothing without the key file | Opens nothing |
| A copy of the database plus the token | n/a | Unwraps every data key **while the token lives**: short token lifetimes matter (section 3) |
| Rotation | A new file and two restarts | A new version in the service and a restart ([section 7](#7-rotating)) |

The choice is made per installation with `vault.kek.provider` (`local` or `transit`). Nothing else changes:
the data keys, the sealed credentials, the version states and the worker's re-wrap are the ones of the
[vault guide](vault.md). An installation that keeps a key file runs the code it ran before and needs
nothing from this guide.

What the key service sees and does not see:

| The key service sees | It does not see |
|---|---|
| The token or the AppRole login, on every request | Any tenant, credential, caller or request of a model |
| A 32-byte value to encrypt or decrypt (a data key, or its wrapped form) | The associated data: it receives a SHA-256 digest of them, never the tenant ID |
| A 32-byte digest as associated data | A provider's API key, which is sealed under the data key and never leaves Pandokos |
| For the identifier of a version, a constant label | The database |

The rate of `unwrap` requests tells it how many tenants become active per cache lifetime, and nothing else.
Pandokos sends only seven requests (encrypt, decrypt, describe the key, an HMAC for the identifier, and
the token's lookup, renewal and AppRole login) and never rotates, configures, trims or deletes the key.

## 2. Requirements of the service

- **Tested** in every change against `openbao/openbao:2.7.3` and `hashicorp/vault:2.2.2`, in development
  mode, with the same suite. Expected to work from HashiCorp Vault 1.13 and OpenBao 2.0, where the service
  binds the associated data of a request; Vault 1.8 to 1.12 accept the field and ignore it. **The version
  is not what decides:** at every start, and in `pandokos kek check`, Pandokos wraps a probe and unwraps it
  with other associated data; a service that returns the probe is refused (`the key provider does not bind
  associated data`) and `serve` does not start.
- **The transit secrets engine**, mounted at a path you choose (`vault.kek.transit.mount`, default
  `transit`), with a key of type `aes256-gcm96`, not derived, that you create (section 3): Pandokos never
  creates it. A key that is missing, of another type, derived or soft-deleted stops the start with a
  message that names the setting.
- **The address of the active node** or of its load balancer. A standby node answers with a redirect;
  redirects are not followed and count as unavailable.
- **`https`** with a certificate Pandokos can verify. `http` is accepted for a loopback host only (an agent
  of the service on the same host) and logs a warning; the connection itself is refused unless it goes to a
  loopback address, whatever name the host has (`localhost` is fine while it resolves to loopback). Authorities for the service come from
  `vault.kek.transit.tls.ca_file` when set (they are then the only ones trusted for it), otherwise from the
  system roots and `upstream.tls.ca_file`. Proxy settings are not used for the key service, and the
  certificate is always verified.
- **A token or an AppRole** with the policy below. Kubernetes and other authentication methods are not
  supported yet.
- Namespaces (`vault.kek.transit.namespace`, sent as `X-Vault-Namespace`) are an OpenBao feature; the
  community edition of HashiCorp Vault ignores the header and answers `enterprise-only` to creating one.

## 3. Creating the key, the policy and the credentials

The commands are for the OpenBao CLI (`bao`); with HashiCorp Vault use `vault` and the same arguments. They
are run by the operator of the service, with a token of its own and never Pandokos's.

**The key.** Enable the engine and create the key with the algorithm Pandokos needs and none of the weak
settings:

```sh
bao secrets enable transit
bao write -f transit/keys/pandokos type=aes256-gcm96
```

Leave `exportable`, `allow_plaintext_backup`, `deletion_allowed` and `auto_rotate_period` at their
defaults (false, false, false and 0). **Pandokos refuses to start with a key that was created
`exportable`**, unless `vault.kek.transit.allow_exportable_key` is `true`: the setting cannot be undone in
the service, and any token whose policy allows export would read the key in clear, which is the exposure
this boundary exists to avoid. The message names the key's setting and `vault.kek.transit.allow_exportable_key`,
and `serve` ends with 1 before it listens on anything; the cause is one only a person can repair (create
a key that is not exportable and move to it, or accept the key knowingly with the option). With the option
the key starts and the warning below names `exportable`. The four other settings start with a warning (`key provider configuration
weak`, once per setting): a key that can be deleted weakens what the service is for, a
`plaintext_backup` puts the key in a backup in clear, and an `auto_rotate_period` rotates the key in a way
Pandokos takes up only at its next restart (section 7).

**The policy.** The file [`deploy/transit/pandokos-policy.hcl`](../../deploy/transit/pandokos-policy.hcl)
is the least privilege Pandokos needs for a mount `transit` and a key `pandokos`; change the two names if
yours differ. It grants `update` and not `create` on the encrypt path, so a wrong key name is refused
instead of creating a key, and nothing that rotates, configures, trims, deletes or exports. The
conformance suite loads this very file against both services.

```hcl
path "transit/encrypt/pandokos"       { capabilities = ["update"] }
path "transit/decrypt/pandokos"       { capabilities = ["update"] }
path "transit/hmac/pandokos/sha2-256" { capabilities = ["update"] }
path "transit/keys/pandokos"          { capabilities = ["read"] }
path "auth/token/lookup-self"         { capabilities = ["read"] }
path "auth/token/renew-self"          { capabilities = ["update"] }
```

```sh
bao policy write pandokos deploy/transit/pandokos-policy.hcl
```

**A token** (`vault.kek.transit.auth.method: token`). A periodic token is renewed by Pandokos for as long
as it runs; without the default policy, because the policy above already grants the lookup and the renewal:

```sh
bao token create -policy=pandokos -no-default-policy -orphan -period=24h -field=token > ./secrets/transit_token
chmod 600 ./secrets/transit_token
```

Pandokos reads the token file when it starts, looks the token up, renews it when two thirds of its lifetime
have passed, and reads the file again then: a different token in the file is adopted, which is how you
replace one without a restart. A token that never expires is accepted and has nothing to renew. A token that
cannot be renewed ends with its lifetime; section 9 says what the callers then get.

**An AppRole** (`vault.kek.transit.auth.method: approle`) is preferred where the secret ID can be delivered
by something other than a person: the tokens it gives are short, and Pandokos logs in again when one cannot
be extended.

```sh
bao auth enable approle
bao write auth/approle/role/pandokos token_policies=pandokos token_no_default_policy=true \
    token_ttl=15m token_max_ttl=1h
bao read  -field=role_id auth/approle/role/pandokos/role-id > ./secrets/transit_role_id
bao write -f -field=secret_id auth/approle/role/pandokos/secret-id > ./secrets/transit_secret_id
chmod 600 ./secrets/transit_role_id ./secrets/transit_secret_id
```

The role ID and the secret ID are read from their files at every login. Only the files are accepted: a
secret in the environment or in the configuration file is not (ADR 0019). Keep the files readable by the
service user only (mode 600 or 400); another user's permission produces a warning, as for the key file. A
secret ID with a limited lifetime or number of uses must be replaced in its file before it runs out.

## 4. The keys and `pandokos kek check`

All keys are read by `serve` and `pandokos kek check` only; `migrate`, `init` and the client commands read
none of them. The full table, with the rules and warnings, is in the
[configuration reference](configuration.md).

| Key | Environment variable | Default | Meaning |
|---|---|---|---|
| `vault.kek.provider` | `PANDOKOS_VAULT_KEK_PROVIDER` | `local` | The provider that wraps: `local` or `transit` |
| `vault.kek.transit.address` | `PANDOKOS_VAULT_KEK_TRANSIT_ADDRESS` | empty | `https` (or `http` for loopback); required with `transit`. With provider `local` it makes the service an unwrap-only provider |
| `vault.kek.transit.namespace` | `PANDOKOS_VAULT_KEK_TRANSIT_NAMESPACE` | empty | Sent as `X-Vault-Namespace` with every request |
| `vault.kek.transit.mount` | `PANDOKOS_VAULT_KEK_TRANSIT_MOUNT` | `transit` | Mount path of the engine |
| `vault.kek.transit.key_name` | `PANDOKOS_VAULT_KEK_TRANSIT_KEY_NAME` | empty | The key; it must exist |
| `vault.kek.transit.tls.ca_file` | `PANDOKOS_VAULT_KEK_TRANSIT_TLS_CA_FILE` | empty | The only authorities trusted for the service |
| `vault.kek.transit.auth.method` | `PANDOKOS_VAULT_KEK_TRANSIT_AUTH_METHOD` | empty | `token` or `approle` |
| `vault.kek.transit.auth.token_file` | `PANDOKOS_VAULT_KEK_TRANSIT_AUTH_TOKEN_FILE` | empty | With `token` |
| `vault.kek.transit.auth.approle.mount` | `PANDOKOS_VAULT_KEK_TRANSIT_AUTH_APPROLE_MOUNT` | `approle` | Mount of the AppRole method |
| `vault.kek.transit.auth.approle.role_id_file` | `PANDOKOS_VAULT_KEK_TRANSIT_AUTH_APPROLE_ROLE_ID_FILE` | empty | With `approle` |
| `vault.kek.transit.auth.approle.secret_id_file` | `PANDOKOS_VAULT_KEK_TRANSIT_AUTH_APPROLE_SECRET_ID_FILE` | empty | With `approle` |
| `vault.kek.transit.request_timeout` | `PANDOKOS_VAULT_KEK_TRANSIT_REQUEST_TIMEOUT` | `2s` | Time allowed to each request, 100 ms to 30 s |
| `vault.kek.transit.max_concurrent_requests` | `PANDOKOS_VAULT_KEK_TRANSIT_MAX_CONCURRENT_REQUESTS` | `32` | Requests in flight at most, 1 to 1024 |
| `vault.kek.transit.start_timeout` | `PANDOKOS_VAULT_KEK_TRANSIT_START_TIMEOUT` | `0` | How long `serve` waits for the service at start; `0` without limit, up to 1 h |
| `vault.kek.transit.allow_exportable_key` | `PANDOKOS_VAULT_KEK_TRANSIT_ALLOW_EXPORTABLE_KEY` | `false` | Accept a key that was created `exportable`; without it such a key is refused at start (section 3) |

A minimal configuration with an AppRole:

```yaml
vault:
  kek:
    provider: transit
    transit:
      address: https://bao.internal.example:8200
      mount: transit
      key_name: pandokos
      tls:
        ca_file: /run/secrets/bao_ca.pem
      auth:
        method: approle
        approle:
          role_id_file: /run/secrets/transit_role_id
          secret_id_file: /run/secrets/transit_secret_id
```

Any `vault.kek.transit.*` key set without an address is refused, as is a key file next to
`provider: transit` (the old file moves to `vault.kek.local.unwrap_key_files`, section 5), and no message
quotes the content of a file. `pandokos config check` applies these rules without contacting the service.

**`pandokos kek check`** loads the configuration as `serve` does, authenticates, describes the key,
and runs the round trip and the negative probe of section 2 on the provider that wraps and on the key
service when it is configured but not the one that wraps (step 0 of a move to the service: the service is
proved to encrypt, decrypt and bind the associated data before the restart that makes it the active one;
`serve` does the same at its start). It writes nothing to the database or the
service and ends with 0, or with 1 and the class of the failure. For a key that was created `exportable` it
ends with 1 and names the setting and `vault.kek.transit.allow_exportable_key`; with the option set it ends
with 0 and prints `weak: exportable`. Run it before the first start with a new
service, and after a change of its credentials:

```
$ pandokos kek check --config /etc/pandokos/pandokos.yaml
provider: transit
version: transit-5b2fee9a2463f150-v1
unwraps: v1 to v1
weak: auto_rotate
```

`version` is the identifier the key's latest version has (section 7); `unwraps` the range of versions the
service can still decrypt; `weak` appears only when a setting of section 3 is not at its default (an `exportable` key, accepted with
`vault.kek.transit.allow_exportable_key`, included) or the token's policies include `root`. The check does not wait for a service that does not answer: that is its
answer.

## 5. Moving to the key service

`vault.kek.provider` names the provider that **wraps**. The other one takes part, for unwrapping only,
exactly when it is configured: the key file when `vault.kek.local.unwrap_key_files` lists files, the service
when `vault.kek.transit.address` is set. A version identifier names its provider (`local-…`, `transit-…`),
so each unwrap goes to the one that owns the version.

| Step | Key file to key service | Key service to key file |
|---|---|---|
| 0, several instances only | every instance gets `vault.kek.transit.*` (provider still `local`) and is restarted: it can then unwrap what the first migrated instance will wrap | every instance gets the **new** key file in `unwrap_key_files` and is restarted |
| 1 | `provider: transit`; the old `key_file` moves to `vault.kek.local.unwrap_key_files`; restart. `vault ready` shows `kek_provider: transit` and `kek_activation: activated`; the local version is `retiring` | `provider: local` with a key file made by `pandokos kek generate` (a replaced local version cannot be activated again); `vault.kek.transit.*` stays; restart |
| 2 | calls continue; the worker re-wraps; `key rotation complete` names the old version; `pandokos kek-version list` shows it `retired` | the same, naming the transit version |
| 3 | remove `unwrap_key_files` and restart (the line `key provider no longer needed`, provider `local`, has said so at every start since step 2). Destroy the key file and its copies, or keep it offline only as long as a backup older than step 2 must still open credentials ([vault guide](vault.md#6a-rotating-the-key)) | remove `vault.kek.transit.*` and restart; revoke the token or the AppRole in the service |

Run `pandokos kek check` at step 0 and before step 1: with the service still configured for unwrapping only it
already proves the permission to encrypt and the binding of the associated data. A move is the rotation of the vault guide with the new key coming
from another provider: tenants do nothing, credentials are not touched, and an instance that has not been
restarted yet keeps opening credentials but answers `503` to a write (`credential cannot be sealed`, cause
`kek_replaced`). Take a new backup of the database when the old version is `retired` (section 10).

If the key of the service you move to, or the one you are leaving, was created `exportable`, set
`vault.kek.transit.allow_exportable_key` for the whole move, on every instance: a service that is
configured, even for unwrapping only, is started under the same rule. Remove the option together with the
`transit` keys at the last step, or earlier when the exportable key is no longer configured.

Moving to the service on one instance is steps 1 to 3. With several instances do step 0 first and every
instance together. A move from one transit service to another is not possible directly: one provider of each
name is configured, so it goes through a key file.

## 6. Moving back

The same table, read from the right-hand column. Make the new key file with `pandokos kek generate --out
FILE` (it is a new key: the service's key cannot be exported), set `provider: local`, keep
`vault.kek.transit.*` so the service still unwraps what is wrapped by it, restart, wait for `key rotation
complete` with the transit version, take a new backup, then remove the `transit` keys and restart. Revoke
the token or the AppRole in the service, and destroy the service's key version only by the rule of
section 8.

## 7. Rotating

**Pandokos never rotates the key, and its policy does not allow it.** A rotation with the service is:

1. Rotate the key in the service, with a token that is not Pandokos's: `bao write -f transit/keys/pandokos/rotate`.
2. Restart the instances. The first start registers the new latest version as active (`kek_activate`); the
   previous one becomes `retiring`; the worker re-wraps every data key and retires it (`key rotation
   complete`).

`serve` wraps with the version it found **at start** and refuses an answer for another one, so a key rotated
while Pandokos runs changes nothing until the instance restarts: the database accepts data keys under the
active version only, and an instance that wrapped under the newest version by itself would be refused for
every new tenant. An instance not yet restarted keeps opening credentials and creates no data key (`503`,
cause `kek_replaced`), as with key files. It also opens the data keys that the worker of an instance already
restarted re-wraps under the new version: an identifier above the latest version it described makes it read
the key's description again (at most once a second), and the service's fingerprint is checked as for any
version. No step 0 is needed, since the service holds every version. Several instances: restart them all.

**Take care with `auto_rotate_period`.** A key that rotates by itself puts a new version in the service
that nobody asked for. Nothing breaks, since wrapping is pinned and every version stays decryptable, but the
next restart of any instance, a crash included, activates it while other instances run on the old one. If
you want rotation on a schedule, rotate and restart deliberately instead; Pandokos warns about a non-zero
`auto_rotate_period` (`key provider configuration weak`, setting `auto_rotate`).

The identifier of a version is `transit-<16 hex digits>-v<N>`. The 16 digits come from an HMAC the service
computes under that very version, so an identifier cannot be attached to another key: another service, a
key deleted and created again, or another key of the same name computes another fingerprint, and Pandokos
then refuses the version instead of unwrapping. The identifier does not depend on the address, the
namespace, the mount or the key name, so moving or renaming them changes nothing.

## 8. Erasure in backups

Deleting a tenant removes its data key from the database at once. A backup taken before still holds the
wrapped key, and whoever has the backup **and** the key version that wrapped it can open it
([deletion guide](tenant-deletion.md#6-erasure-and-backups-the-operators-rule)). With the service the key
version is not a file you can shred; you make it undecryptable in the service. The order, within the time
you promise erasure in:

1. Rotate the key in the service (section 7) and restart the instances.
2. Wait for `key rotation complete` naming the old version, and for `erasures_pending: 0` on it in
   `pandokos kek-version list`.
3. Take a new backup of the database.
4. Raise `min_decryption_version` above the old version: `bao write transit/keys/pandokos/config
   min_decryption_version=2`. This is **reversible**: lowering it again makes the old version decrypt.
   Pandokos refuses to start while a version the database still needs is out of reach, and names it.
5. When the old backups have expired or you accept that they open nothing, set `min_encryption_version` to
   the new version and trim: `bao write transit/keys/pandokos/config min_decryption_version=2
   min_encryption_version=2`, then `bao write transit/keys/pandokos/trim min_available_version=2`. A trim
   is **not reversible**: the old version's key material is gone, and so is every credential still wrapped
   by it, in any backup.

A version raised out of reach too early is found at the next start and, while running, by `credential
cannot be opened` for the tenants whose data key has not been re-wrapped yet; lowering
`min_decryption_version` again repairs it until the version is trimmed. Trimmed is the lost-key case
(section 11). The product cannot see what you did in the service: "retired" is a fact of the database,
"trimmed" is yours. The price is the one of the key file: a backup older than the rotation opens no
credential once its version is trimmed.

## 9. When the service is slow or away

Pandokos sends each request once, with these bounds. They are constants of the design except the first two,
which are configuration:

- **A deadline of 2 s** (`vault.kek.transit.request_timeout`, 100 ms to 30 s) from the wait for a slot to
  the last byte of the answer, or the caller's deadline if that ends first. There is no retry inside the
  provider: the next request of the client or the next pass of a job is the retry, and nothing sleeps in the
  path of a request. The single exception is one new authentication after an answer `403`, at most one per
  5 s.
- **At most 32 requests in flight** (`vault.kek.transit.max_concurrent_requests`, 1 to 1024). A call that
  gets no slot before its deadline fails as unavailable. Identical concurrent unwraps (same version, wrapped
  key and associated data) share one request.
- **A refusal.** After five consecutive requests that ended as unavailable the provider refuses every call
  at once for 5 s without sending anything, then lets one request through. A success ends the refusal; a
  failure renews it. Answers that are not about availability (`400`, `403`, `404`) do not count.
- **Answers are classed by status only** (`ok`, `unavailable`, `denied`, `not_found`, `rejected`,
  `refused`): `429`, `5xx` (a sealed service answers `503`), a redirect, a timeout, a refused connection or a
  failed handshake are unavailable. An answer longer than 64 KiB is an error, and the body of an error
  answer is read, dropped and never logged.

What the callers get:

| During an outage of the key service | Behaviour |
|---|---|
| Inference, credential or data key cached | continues until `cache.credential_ttl` and `cache.data_key_ttl` end (30 s and 5 m by default, 15 m at most for the data key) |
| Inference, nothing cached | `503 unavailable` with `Retry-After: 5`, after at most the deadline (at once while the refusal is in force); nothing is forwarded; the line `credential cannot be opened` has cause `kek_provider_unavailable` |
| Writing a credential or a signing secret, creating a tenant's first secret | `503 unavailable`; nothing is stored; `credential cannot be sealed` |
| Every other administration operation | unaffected |
| Event relay | a round whose secret cannot be opened fails without consuming an attempt; events wait in order; the relay opens signing secrets through a cache of unwrapped keys, so one request per destination and lifetime reaches the service |
| Re-wrap job | `key rewrap failing` with cause `kek_provider_unavailable`; no data key is half done; the next pass continues |
| A restart | `serve` waits, as below |
| The token expires and cannot be renewed | as an outage for the requests that follow, class `denied`; `key provider authentication failing` |
| A version the database needs is no longer decryptable in the service | `serve` does not start and names the version; while running the tenants under it get `503`; trimmed, those data keys are lost (section 11) |

**A start during an outage waits.** `serve` authenticates and describes the key before it binds a port. When
the service does not answer it logs `key provider unavailable` (phase `start`, at most every 30 s), tries
again after 1 s, 2 s, 4 s up to 15 s, and when the service answers logs `key provider recovered` and then
`vault ready`. While it waits the process **listens on no port and serves nothing**, `/healthz` and
`/readyz` included, and it does not exit. By default it waits without limit
(`vault.kek.transit.start_timeout: 0`); a limit makes `serve` end with `startup failed` instead, for a
supervisor that prefers to restart. Anything else that fails at start (a refused token, a missing key, a key
of the wrong kind, associated data not bound, a version the database needs that the configuration does not
provide, a replaced version) ends `serve` at once.

**An orchestrator that probes a waiting process needs a start-up allowance** longer than the outage you want
to ride out: a `startupProbe` with a failure threshold times period beyond it in Kubernetes, `start_period`
in a Docker health check, or no restart policy that kills a process that is not yet ready. Otherwise the
orchestrator restarts it in a loop.

**Once started, `/readyz` does not depend on the service.** All instances share the service, so a failing
check would withdraw every instance at once, and with them the calls that cached keys still serve and the
administration API, most of which needs no key. The outage shows as the line `key provider unavailable`
(phase `run`, when the refusal begins and every 30 s while it lasts), the instrument
`pandokos.vault.provider.up` and the outcomes of `pandokos.vault.provider.requests`.

**Alert on**, in the Prometheus names ([telemetry guide](telemetry.md)):

| Metric | Alert when |
|---|---|
| `pandokos_vault_provider_up` | it is 0: requests are refused; the callers without a cached key get `503` |
| `pandokos_vault_provider_token_ttl_seconds` | it falls below a few renewal periods and keeps falling: the token is not renewed (also the line `key provider token expiring`, every 30 s at most) |

## 10. Backups

The service has its own backup procedure; it is yours and Pandokos cannot see it. What matters here:

- **A database backup needs the key versions that wrapped its data keys.** A backup taken before a rotation
  opens credentials only while that version still decrypts in the service. Restoring it into an installation
  with the service needs the same key, with the version in range, and the credentials of the AppRole or a
  token.
- A key service restored from its own backup must contain the versions the database needs. Pandokos finds a
  missing one at start (`startup failed`, naming the version).
- Back the key service up as you would the key file: **apart from the database**, by someone the database's
  administrators are not. The [backup guide](backup-and-restore.md) lists the four cases of the key; with a
  service, "the key file" reads "the key versions in the service".

## 11. Loss and compromise

**The key or a version lost.** A key deleted in the service, or a version trimmed, is the lost-key case of
the [vault guide](vault.md#5-what-its-loss-means): every credential wrapped under that version can never be
opened. Run `deploy/postgres/recover-lost-kek.sql` with the lost version, activate a new key (a new version
in the service, or `pandokos kek generate`), and every tenant enters its credentials again. Nothing else is
lost. The key is protected in the service by `deletion_allowed: false`; keep it that way.

**The token or the secret ID leaked.** With the database, it unwraps every data key while it lives.

1. Revoke it in the service (`bao token revoke`, or revoke the secret ID accessor and the tokens of the
   role) and give Pandokos new credentials in the files; a token file read again is adopted at the next
   renewal point or after a `403`.
2. Rotate the key (section 7) and restart, so data keys are wrapped under a version the leaked token never
   saw in clear; the worker re-wraps them.
3. When re-wrap is complete and a backup is taken, raise `min_decryption_version` and trim the old versions
   (section 8). Without that, a copy of the database taken while the token was valid and the token itself
   still open what they could before.

**The service compromised.** Whoever controls it can unwrap every data key it sees. Treat it as the loss of
every credential: replace the service and its key, and every tenant enters its credentials again. What was
exposed in clear is the 32-byte data keys of the tenants that called while the service was observed; the
provider credentials themselves are sealed under the data keys, which an attacker needs the database for.

## 12. Limits

- **One key, on one service, at a time.** There is one `transit` block. A move between two transit services
  goes through a key file.
- **No Kubernetes, certificate, JWT or cloud authentication** yet: a token or an AppRole. Cloud key
  services are not provided (issue #75).
- **No namespaces in the community edition of HashiCorp Vault**: it ignores the header. OpenBao implements
  them.
- **A standby node is not followed**: a redirect is an unavailable service, so the address names the active
  node or its load balancer.
- **Pandokos does not rotate, trim or delete**, and does not follow a rotation without a restart.
- **A data key is never used past its lifetime** while the service is away: raise `cache.data_key_ttl` (15 m
  at most) if that is too short for your outages.
- **No Compose overlay runs a key service**: a development-mode service loses its keys at a restart.
- Versions older than those tested are enforced by the probe, not by a version check.
