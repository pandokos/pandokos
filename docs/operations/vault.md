# The vault: provider credentials and the key-encryption key

The vault stores the provider credentials (API keys) of tenants encrypted, and opens them for the
inference listener. This guide is for the operator: how the key is made, kept, backed up and
replaced, and what its loss means. Decisions: [ADR 0008](../adr/0008-vault-envelope-encryption.md),
[ADR 0033](../adr/0033-vault-formats-and-key-identifiers.md),
[ADR 0034](../adr/0034-credential-model-and-platform-oversight.md).

## 1. What is stored where

| Layer | What it is | Where it lives |
|---|---|---|
| Key-encryption key | 32 random bytes; wraps data keys | **A file** outside the database, read by `serve` at start (`vault.kek.local.key_file`) |
| Data key | one per tenant, created when the tenant registers its first credential | Wrapped, in the table `tenant_data_key`, with the version of the key that wrapped it |
| Secret | the provider API key | Sealed under the tenant's data key, in `provider_credential.secret` |

The database therefore never holds anything it can open by itself. A fingerprint and a four
character hint of each secret are kept in clear to tell credentials apart; the secret is never
returned by any API operation, logged or audited.

## 2. Creating the key

```
pandokos kek generate --out ./secrets/pandokos_kek
```

The command creates the file exclusively with mode 0600, writes a new random key and prints
`key-encryption key written to FILE; version local-…`. It refuses an existing file and cannot print
the key. Point the configuration at it with `vault.kek.local.key_file`
(`PANDOKOS_VAULT_KEK_LOCAL_KEY_FILE`); `vault.kek.provider` (`PANDOKOS_VAULT_KEK_PROVIDER`) is `local`, the only provider.

At start `serve` reads the file, checks it against the database and logs
`vault ready` with `kek_provider` and `kek_version`. The version is derived from the key, so a
wrong file is never taken for the right one. When the database holds a data key wrapped by a version
that the file does not provide, `serve` refuses to start and names the version.

## 3. Protecting it

- It is not in the image, not in the configuration file, not in the database and not in its backups.
- Mode 0400 or 0440. `serve` logs a `configuration warning` for `vault.kek.local.key_file` when the
  file grants any permission to other users; it starts anyway.
- Kubernetes: a `Secret` mounted as a file with `defaultMode: 0440` and `fsGroup` set to the group
  of the service user. Compose: a file-based secret. Docker: a read-only bind mount
  ([container](container.md)).
- Give the key to the process that runs `serve` only. `migrate` and `init` do not read it.

## 4. Backing it up

A database backup holds only sealed secrets and wrapped data keys: it cannot open anything. The key
file is backed up **separately**, once, when it is generated, to a store that the database
administrators cannot read. Test the copy: restore a database backup, start a spare instance with the
copy of the key and read `vault ready` in its log.

A limit on erasure (open point O-8): destroying a tenant's data key makes its credentials unreadable
only when the key that wrapped it is also gone. A backup taken earlier still holds the wrapped data
key, so it stays readable as long as someone holds both that backup and a key file of that version.
Until key rotation exists, treat old backups and their key copies as holding the credentials
of that time; limit how long backups are kept. The remedy comes with tenant deletion.

## 5. What its loss means

If the key file is lost and no copy exists, the sealed secrets can never be opened again, so
every tenant must re-enter its provider credentials. Nothing else is lost.

| Lost | Recoverable | Must be re-entered |
|---|---|---|
| Database, backup and key file exist | everything | nothing |
| Key file (no copy) | tenants, identities, keys, audit, credential metadata | every provider credential |
| One tenant's data key row | everything else | that tenant's credentials |
| Database without backup | nothing | everything |

Procedure after the loss of the key file:

1. Read the lost version from an earlier `vault ready` log line, or from the refusal of `serve`.
2. As a database superuser, in the Pandokos database:
   `psql -v kek_version=local-0123456789abcdef -f deploy/postgres/recover-lost-kek.sql`.
   [The script](../../deploy/postgres/recover-lost-kek.sql) revokes the credentials sealed under that
   version (state reason `key-encryption key lost`), and removes their data keys and the version. It
   writes no audit row: it is an act of the database administrator outside the product, so record it
   in your own log.
3. `pandokos kek generate --out ...` a new key, mount it and start `serve`.
4. Tell the tenants: they register their credentials again.

### Log lines when the key material fails

A use of a credential that cannot be opened logs `credential cannot be opened`; a write (create or
replace secret) that cannot be sealed logs `credential cannot be sealed` and answers 503
`unavailable`. Both carry `request_id`, `tenant_id`, `data_key_version`, `kek_version` and `cause`;
the line of a write carries `credential_id` and `operation` too, and leaves out what is not known
yet (the key versions when the data key cannot even be created). `cause` is one of:

| `cause` | Meaning |
|---|---|
| `kek_unavailable` | the key-encryption key version is unknown to the provider (for example a missing key file), or the provider cannot answer |
| `data_key_unwrap` | the stored data key does not unwrap: damaged, or copied from another tenant |
| `data_key_missing`, `authentication` | read only: no data key row, or the secret does not authenticate |

The error text of the provider is never logged: it can carry paths.

## 6. What its compromise means

The key alone opens nothing. With a copy of the database it opens every credential: tenants must
replace their keys at the provider. Rotation of the key arrives with a later sprint (VLT-10); until
then the remedy is the loss procedure above, after the tenants have replaced their keys.

## 7. How fast a change takes effect

The administration API reads the database and sees a change at the next request. The inference
listener caches opened credentials and unwrapped data keys.

| Setting | Default | Bound |
|---|---|---|
| `cache.credential_ttl` (`PANDOKOS_CACHE_CREDENTIAL_TTL`) | 30 s | 1 s to 60 s |
| `cache.data_key_ttl` (`PANDOKOS_CACHE_DATA_KEY_TTL`) | 5 m | 1 s to 15 m |

| Event | Inference listener |
|---|---|
| A credential is disabled, revoked, deleted or replaced; the default moves | at most `cache.credential_ttl` (30 s by default, never above 60 s) after the commit; normally under a second |
| A data key is removed or re-wrapped | at most `cache.data_key_ttl` plus `cache.credential_ttl` (5 m + 30 s by default, never above 15 m + 60 s) |

The database sends a notification (`vault:<tenant>`) that empties the cache of that tenant at once.
Without the listener (behind a pooler in transaction mode without `database.direct_address`, see
[keys](keys.md)) nothing else changes: the lifetime is the guarantee. A call that already holds the
secret completes. Both caches hold at most 10 000 entries and are emptied when full.

## 7a. Providers come from the catalogue

The providers a credential may name are the providers of the catalogue ([catalogue guide](catalogue.md)),
not a list in the program: `credential create --provider NAME` checks the provider in the catalogue
and refuses an unknown one with `invalid_request` on `provider`. A provider that is disabled still
accepts credentials, so a tenant can prepare one before the provider is enabled. A provider cannot be
deleted while a credential references it.

## 8. Validation

When a tenant asks (`validate`), a credential is checked once against the provider before it is
stored. The secret is presented to the default enabled endpoint of the credential's provider, in the way
of the provider's API family: `GET <base URL>/v1/models` with a bearer token for `openai`, and the
equivalent request with `x-api-key` for `anthropic`. The request goes through the egress guard: the
endpoint's class (public or private) decides the allowed scheme and addresses, redirects are not followed,
certificates are verified, and the request goes through `upstream.proxy.url` when it is configured
([gateway guide](gateway.md)). The limit is 10 s. A provider whose family has no validator, or a
provider with no enabled default endpoint, is refused with `invalid_request` on `validate`.

A 2xx answer stores the credential as validated; 401 or 403 returns 422 `credential_rejected`; anything
else, including a timeout, a refusal by the guard or a redirect, returns 502 `validation_unavailable`.
In both failures nothing is stored. The provider's answer is never logged. The log line `credential
validation` carries only the outcome and the status. Self-hosted servers that accept any key answer 2xx
to every secret: for them `validate` proves that the endpoint is reachable, not that the key is right.

## 9. Limits

- Memory: a running gateway holds up to 10 000 opened credentials and 10 000 data keys; a secret
  stays in its memory for the cache lifetime.
- One process (R-5): in v0.1 `serve` runs the administration and the inference plane together, so both
  database users share one memory space. That the admin plane cannot decrypt is enforced by database
  grants today and becomes a property of the deployment when the roles run apart.
- Erasure and old backups: see section 4 (O-8).
- No key rotation yet (R-6): one key file, one active version.
- The pin rule holds in the request path: a key pinned to a credential of the provider being called
  uses that credential; otherwise the tenant's default for that provider is used; nothing else is tried
  ([gateway guide](gateway.md)).
