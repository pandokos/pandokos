# Keys, `init` and revocation

Pandokos issues two kinds of key. Both are bearer credentials: whoever holds the key acts as its
owner, so keep it in a file with restricted access and never in a command line or a log.

- A **platform key** belongs to the installation. It has the role `platform-admin` and administers
  tenants and platform keys. It has no permission inside a tenant.
- A **virtual key** belongs to one service account of one tenant. What it may do follows from the
  roles of the service account (`tenant-admin`, `inference`) and from its optional scopes.

## Format

```
pdk_<key id>_<secret><checksum>
```

A key is 81 characters: the prefix `pdk_`, a 17-character key ID (a kind letter, `t` for a virtual
key and `p` for a platform key, then 16 characters), a separator, a 256-bit secret and a CRC-32
checksum, in the lower-case base32 alphabet `a-z2-7`. The key ID is public: it names the key in the
API, in logs and in audit records. Only the SHA-256 of the secret is stored. The expression for
secret scanners is:

```
pdk_[tp][a-z2-7]{16}_[a-z2-7]{59}
```

Use it in the secret scanner of your repositories: as a custom pattern in GitHub secret scanning
where your plan offers custom patterns, or as a rule of a scanner run in CI. The Pandokos
repository itself scans its whole history with gitleaks in CI (`make secrets`, rule in
`.gitleaks.toml`); copy that rule if you use gitleaks. A key found in a repository must be
revoked: removing it from the history is not enough. A key is shown once, when it is created;
there is no way to read it back.

## First run

```
pandokos migrate
pandokos init --key-out ./bootstrap.key
pandokos serve &
export PANDOKOS_CLI_KEY_FILE=./bootstrap.key
pandokos platform-key create --key-out ./platform.key      # a key that is not the bootstrap key
pandokos tenant create acme --admin-key-out ./acme-admin.key
```

With the key of the tenant's administrator the tenant creates its own service accounts and keys:

```
export PANDOKOS_CLI_KEY_FILE=./acme-admin.key
pandokos service-account create --role inference caller
pandokos key create --service-account caller --key-out ./caller.key
```

`pandokos init` runs on the host with the configuration of the installation and the admin database
user. It issues the **bootstrap key**, a platform key that always expires (`--ttl`, 5 minutes to
720 hours, default 24 hours). Without `--key-out` it prints the key once on standard output; with
it the key is written to a new file with mode 0600. A fresh installation accepts nothing until
`init` runs: there are no default credentials. If a usable platform key already exists `init`
refuses; `init --recover` issues a new one when every platform key is lost, and the audit record
says so.

While a bootstrap key is usable, `serve` logs `bootstrap key active` at start and every hour, every
response to that key carries `X-Pandokos-Warning: bootstrap-key-active`, and the CLI prints a
warning. Create a platform key and revoke the bootstrap key.

## Rotation

A service account can hold several active keys. To rotate: create a second key, deploy it, revoke
the first. Nothing needs a restart.

## How fast a revocation takes effect

| Event | Admin listener | Inference listener |
|---|---|---|
| A virtual key is revoked or expires; a service account is disabled or leaves its validity; a role is removed | the next request | at most `cache.principal_ttl` after the commit (default 30 s; at most 60 s); normally under one second when notifications arrive. Expiry and validity are checked at every request and are exact |
| A tenant is suspended | the next request | the same |
| A platform key is revoked | the next request | not accepted there at all |

The inference listener keeps the principal of a key for `cache.principal_ttl`. PostgreSQL
notifications shorten that: the gateway holds one connection that listens for them. A notification
cannot cross a pooler in transaction mode; when `database.address` is such a pooler, set
`database.direct_address` to PostgreSQL itself, or the listener logs `cache invalidation
unavailable` and the bound is the full 30 s. The bound holds with no notification at all. A
request that was authenticated before the event is not interrupted: a stream that is running
finishes. The administration API does not cache.

## What suspension does

Suspending a tenant refuses every key of the tenant, on both listeners, with `tenant_suspended`
(403). No row is deleted; reactivating the tenant restores everything.

## Things to know

- The first administrator key of a tenant is created by the platform administrator and passes
  through the platform administrator's hands; the tenant can issue a new key and revoke it, and
  both acts are in its audit log.
- A bearer key that is seen can be replayed until it is revoked or expires. Serve both listeners
  with TLS, or terminate TLS in front of them ([TLS](tls.md)).
- Platform administrators have no permission inside a tenant. A tenant that loses its last
  `tenant-admin` key cannot be administered until the support actions of a later sprint.
