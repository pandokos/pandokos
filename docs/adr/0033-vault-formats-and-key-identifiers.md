# 0033. Vault: formats, derived keys, key identifiers, caches

- Status: proposed
- Date: 2026-11-13

Refines [ADR 0008](0008-vault-envelope-encryption.md) and [ADR 0030](0030-keys-format-storage-lookup.md).

## Context

ADR 0008 fixes envelope encryption with AES-256-GCM, a per-tenant data key and a pluggable
key-encryption key. Building it needs exact byte formats, a way to tell key versions apart, a
fingerprint that does not help an attacker who holds the database, and numbers for the caches.

## Decision

1. Sealed values are `0x01 ‖ nonce(12) ‖ ciphertext ‖ tag(16)` with a random nonce per seal
   (`cipher.NewGCMWithRandomNonce`). A key seals far fewer than 2^32 values.
2. The data key is a 32-byte root. HKDF-SHA-256 derives an encryption key (`info`
   `pandokos/vault/v1/credential-encryption`) and a fingerprint key
   (`pandokos/vault/v1/credential-fingerprint`).
3. Associated data are a versioned label, a zero byte and fixed-length fields: for a wrapped
   data key the tenant ID and the data key version; for a credential the tenant ID, the
   credential ID and the data key version. The version of the key-encryption key is stored
   next to the wrapped data key and bound to nothing else, so re-wrapping leaves credentials
   untouched.
4. The fingerprint is the first 16 bytes of HMAC-SHA-256 under the fingerprint key; the hint
   is the last four characters, withheld for secrets shorter than 16 characters.
5. `KeyProvider` has `Wrap`, `Unwrap`, `Available` and `Name`. The local provider reads one
   base64 line of 32 bytes; the version identifier is derived from the key (`local-` and 16
   hexadecimal characters of a SHA-256). Versions that wrapped a data key are registered in
   the global table `kek_version`; `serve` refuses to start when one of them is not available.
6. The gateway caches opened credentials for `cache.credential_ttl` (30 s, at most 60 s) and
   derived data keys for `cache.data_key_ttl` (5 m, at most 15 m), each bounded to 10 000
   entries, invalidated by the payload `vault:<tenant>` on the notification channel of ADR
   0030. The lifetime is the guarantee; notifications shorten it.

## Consequences

A change of algorithm is a new format byte and a new label. A wrong key file stops the
process at start with the missing version named. A revoked credential can be used for up to
the credential lifetime; a removed data key for up to the sum of both. Fingerprints of one
secret differ between tenants and cannot be compared across them.

## Alternatives considered

A counter nonce (needs durable state per key); the data key used directly for both purposes
(one key, two primitives); an unkeyed or globally keyed hash as fingerprint (offline guessing,
cross-tenant correlation); operator-chosen version labels (a label can be attached to the
wrong file); no registry of versions (a wrong file would fail tenant by tenant).
