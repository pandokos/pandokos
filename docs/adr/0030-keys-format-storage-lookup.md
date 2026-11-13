# 0030. Keys: format, storage, lookup, platform keys, revocation bound

- Status: proposed
- Date: 2026-11-12

Refines [ADR 0005](0005-data-plane-authentication.md), [ADR 0004](0004-postgresql-and-rls.md) and [ADR 0003](0003-single-binary-with-roles.md).

## Context

IAM-03 asks for a key that is shown once, stored as a hash, identified by a non-secret ID and recognizable by scanners. Forced row-level security binds the owner, so a definer function cannot read `virtual_key` before a tenant is known. Revocation must take effect within a documented bound on the data plane, where principals are cached.

## Decision

The key is `pdk_<key id>_<secret><checksum>`, 81 characters of lower-case base32: a 17-character key ID with a kind letter, a 256-bit secret, a CRC-32 checksum; only the SHA-256 of the secret is stored. The lookup function reads a directory of (key ID, tenant) and returns the tenant only; everything else is read in that tenant's session. Platform keys are their own global table and carry their role; there is no platform account. The admin API does not cache principals; the gateway does, for `cache.principal_ttl` (default 30 s, at most 60 s), invalidated by PostgreSQL notifications received on one dedicated direct connection that proves itself with probes sent from a second connection; the lifetime is the guarantee. `init` connects as the admin database user, not as the owner.


The bound on revocation, per event:

| Event | Admin listener | Inference listener |
|---|---|---|
| A virtual key is revoked or expires; a service account is disabled or leaves its validity; a role is removed | the next request | at most `cache.principal_ttl` after the commit (default 30 s; cannot be configured above 60 s); normally under one second when notifications arrive. Expiry and validity are checked at every request and are exact |
| A tenant is suspended | the next request | the same |
| A platform key is revoked | the next request | not accepted there at all |

## Consequences

Two round trips on a cache miss; behind a transaction pooler the listener needs `database.direct_address`; `init` needs the admin database password on the host.

## Alternatives considered

The tenant ID inside the key (no directory, longer key, tenant visible in the key); a policy for the owner on `virtual_key`; one key table with a nullable tenant; a cache on the admin API.
