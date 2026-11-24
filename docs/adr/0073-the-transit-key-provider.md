# 0073. The transit key provider: associated data, version identifiers, authentication, several providers and behaviour on failure

- Status: proposed
- Date: 2026-11-24

Refines [ADR 0008](0008-vault-envelope-encryption.md) (the external provider), [ADR 0033](0033-vault-formats-and-key-identifiers.md)
and [ADR 0068](0068-key-encryption-key-versions-rotation-and-erasure-in-backups.md), and **amends** ADR 0033 point 5
and [ADR 0038](0038-egress-guard-upstream-tls-and-proxy.md) point 7. Builds on [ADR 0019](0019-configuration.md),
[ADR 0055](0055-operational-telemetry-holds-no-tenant-data.md), [ADR 0062](0062-metrics-instruments-attributes-cardinality-and-scrape.md),
[ADR 0063](0063-the-log-redaction-and-the-log-reference.md), [ADR 0047](0047-identity-providers-in-the-test-suite.md) and
[ADR 0071](0071-webhook-destinations-signature-secrets-and-egress.md). Requirement VLT-12; sprint 12.1.

## Context

VLT-12 asks for a key-encryption key that comes from a pluggable provider, with an external service as an
option. ADR 0008 chose envelope encryption with a provider interface and ADR 0068 kept that interface to four
methods (`Wrap`, `Unwrap`, `Available`, `Name`) and gave the versions a state. Until now the only provider was a
file. The external service is the **transit secrets engine** of OpenBao and HashiCorp Vault: a service that
encrypts and decrypts values under a named key it never exports.

What had to be established before anything was designed is whether the service **binds associated data**. The
vault binds every wrapped data key to its tenant through associated data (ADR 0033 point 3); a service that
accepted the field and ignored it would silently drop that property. It was verified against real containers on
2026-11-24:

| Fact | Observed on |
|---|---|
| `encrypt` and `decrypt` bind `associated_data` for an `aes256-gcm96` key: other data, none, a changed ciphertext or a relabelled version are each `400` | OpenBao 2.7.3, HashiCorp Vault 2.2.2 |
| The binding holds from Vault 1.13.13 and OpenBao 2.0.0; **Vault 1.8.12 to 1.12.11 accept the field and ignore it without an error** | the same requests against those images |
| `encrypt` with `key_version: N` uses version N and answers it; a version above the latest or below `min_decryption_version` is `400` | both |
| `hmac/<key>/sha2-256` of a fixed input is the same at every call and differs per key version and per key | both |
| Every decrypt failure is `400`, whatever the cause; a missing key is a `404` with an empty error list; a sealed service answers `503` to everything | both |
| With a root token an `encrypt` on a missing key creates it; with a policy that grants `update` only it is `403` | both |
| A token created without the default policy cannot look itself up or renew itself unless its policy says so | both |
| `min_decryption_version` can be lowered again; `trim` needs `min_encryption_version` set and is not reversible | both |
| OpenBao implements namespaces; HashiCorp Vault community ignores the header | both |

## Decision

1. **Seven requests and a policy.** The provider `transit` sends encrypt, decrypt, a description of the key, an
   HMAC for the version identifier, the token's lookup and renewal, and the AppRole login, through a small client in
   `internal/vault` over `net/http`, with no module of either vendor. The policy that grants exactly these, for a mount
   `transit` and a key `pandokos`, is `deploy/transit/pandokos-policy.hcl`, and the conformance suite loads that file.
   The policy grants `update` and not `create` on encrypt, so a wrong key name is refused instead of creating a key.
2. **Associated data as a digest, proved at start.** The provider sends as `associated_data` the base64 of
   SHA-256 of a label, a zero byte and the vault's associated data, so no tenant identifier reaches the service or its
   audit log. The binding is **not assumed from a version**: `vault.Probe`, the part of `vault.Check` that needs no
   database, wraps and unwraps a random probe and then unwraps it with other associated data and requires `ErrUnwrap`;
   a provider that returns the probe is refused (`ErrAADNotBound`) and `serve` does not start. `Check` runs it at every
   start and `pandokos kek check` runs it on demand. The minimum versions of the facts above are an observation, not a
   check. The data key's own derivation (ADR 0033 point 2) binds the tenant even where the service's binding failed.
3. **The key's requirements and warnings.** The key must exist, be of type `aes256-gcm96`, not derived, able to
   encrypt and decrypt, with at least one version. A key that is not stops the start with a message that names the
   setting. Five settings are accepted with a warning, once per start: `exportable`, `plaintext_backup`,
   `deletion_allowed`, `auto_rotate` and a token with the `root` policy.
4. **The identifier comes from the service's HMAC.** A transit version number cannot identify a key version: version 1
   of another key, of the same name on another service, or of a key deleted and created again carries the same number.
   The identifier is `transit-` and 16 hexadecimal characters of the HMAC-SHA-256 of a fixed label under that very key
   version, computed by the service, then `-v` and the number. It satisfies the existing format check, reveals nothing
   about the key, and does not depend on the address, the namespace, the mount or the key name. **For any future provider
   the rule is: the identifier is derived from the key, and prefixed by the provider's name**, so the router can send a
   version to its owner and an identifier cannot be attached to the wrong key. This **amends ADR 0033 point 5**: an
   external provider's identifier is computed by the service from the key, not read from a file.
5. **The wrapped format `0x02`, the pinned version, rotation by restart.** A wrapped key is `0x02` and the decoded
   payload of the service's ciphertext; the number of the version is not stored twice. `Wrap` sends the version found at
   start and refuses an answer for another one. A rotation in the service therefore takes effect when an instance
   restarts, as ADR 0068 point 7 says for key files: the database accepts a data key under the active version only. The
   unwrap accepts only a plaintext of 32 bytes; any other length is a rejected answer.
6. **Pandokos never rotates, configures, trims or deletes the key.** The policy forbids it, and the conformance suite
   proves the refusals. Erasure in backups, which ADR 0068 point 6 ties to retiring a version and destroying its key,
   is done in the service by the operator: rotate, restart, wait for `key rotation complete` and no pending erasure,
   back up, raise `min_decryption_version`, then set `min_encryption_version` and trim. Raising is reversible; a trim is
   not. Whether to automate it (open point O-8 of sprint 6.3) is answered conservatively: the service makes it
   possible, and Pandokos does not do it.
7. **Authentication and the token's lifetime.** `token` or `approle`, with every secret (the token, the role ID, the
   secret ID) a path to a file (ADR 0019), a `secret.Secret` from the file to the header or the body. Lifetimes are
   counted from the local monotonic clock at the moment of the answer. A token is looked up at start, renewed at two
   thirds of its lifetime, and replaced when the file holds a different one; an AppRole logs in again when the renewal is
   refused. An answer `403` triggers one new authentication and one re-send, at most one authentication per 5 s. A
   failed renewal or login leaves the current token in use and is tried again with a backoff of 1 s to 60 s.
8. **Several providers at once.** `vault.kek.provider` names the provider that wraps; the other takes part for
   unwrapping exactly when it is configured. `vault.Router` is itself a `KeyProvider` and sends each version to the
   provider whose name its identifier starts with. `Check` asks the router who owns an identifier. A move between the
   key file and the key service is the re-wrap of ADR 0068, in either direction, for one instance or several (step 0:
   every instance learns the new provider before any instance wraps with it). With one provider `serve` uses it
   directly and builds neither the router nor the cache.
9. **The connection.** The client is the fourth consumer of the egress guard, on a transport of its own:
   `https` and TLS 1.2 or later, the certificate always verified, `vault.kek.transit.tls.ca_file` the only set of
   authorities when it is set, `http` only for a loopback host and with a warning (the connection is refused unless the address it reaches is loopback, whatever the name), no proxy and no proxy variable, no
   redirect. A forbidden address (instance metadata, multicast, unspecified) is refused; a private one is accepted,
   because the address is the operator's configuration. This **amends ADR 0038 point 7**: the key service is a fourth
   consumer of the guard's transports, a dedicated one.
10. **One attempt, a deadline, a bound, a refusal.** `Wrap`, `Unwrap`, the description and the identification are sent
    once; the one exception is the re-send after `403`, which is a request of its own: the slot is let go while the new
    authentication runs (it needs slots too) and taken again for the re-send, with a time of its own. Each request has `vault.kek.transit.request_timeout` (2 s)
    from the wait for a slot to the last byte, at most `max_concurrent_requests` (32) are in flight, a slot that frees
    after nine tenths of the call's time counts as none, and identical concurrent unwraps share one request. After five
    consecutive unavailable answers every call is refused for 5 s without a request, then one is let through; a success
    closes the refusal. Answers are **classed by status only** (`ok`, `unavailable`, `denied`, `not_found`, `rejected`,
    `refused`) and **the body of an error answer is read to a limit and dropped**: it never reaches a string, an error, a
    log attribute or a parser. Error texts are a fixed set built from the operation and the class. No error code is
    added (ADR 0020): the caller gets `503 unavailable`, with `Retry-After: 5` on the inference listener when opening
    the credential fails.
11. **Caches.** The gateway's opener keeps derived data keys and opened credentials for their lifetimes as before; one
    unwrap per tenant and lifetime reaches the service. The worker gets `vault.UnwrapCache`, a provider that wraps
    another and keeps the result of an unwrap under a digest of the version, the wrapped key and the associated data,
    for `cache.data_key_ttl`, so that the event relay opens signing secrets (ADR 0071) with one request per destination
    and lifetime. An entry is never used past its lifetime. The sealer and the re-wrap job use the provider directly.
12. **Start waits; readiness does not depend on the service.** `serve` starts the provider and activates the version
    before it binds any listener. When the service is unavailable it logs, tries again after 1 s, 2 s, 4 s up to 15 s,
    and serves nothing meanwhile, until it answers, the process is told to stop, or `vault.kek.transit.start_timeout`
    has passed. Any other failure ends `serve` at once. Once started, `/readyz` does not depend on the service: all
    instances share it, so a failing check would withdraw every instance and with them the calls that cached keys
    still serve.
13. **Telemetry without addresses or names.** Four instruments (`pandokos.vault.provider.requests`,
    `.request.duration`, `.up`, `.token.ttl`), seven log lines (`key provider ready`, `configuration weak`,
    `unavailable`, `recovered`, `token expiring`, `authentication failing`, `no longer needed`), the field
    `kek_unwrap_provider` on `vault ready` and the value `kek_provider_unavailable` of `cause`. None carries a tenant,
    an address, a namespace, a key name, a header or a version number of the service (ADR 0055).
14. **`pandokos kek check`.** It loads the configuration as `serve` does, authenticates, describes the key and runs
    `vault.Probe`, prints the provider, the version, the range of versions the service can unwrap and the weak
    settings, and writes nothing. It is the one place outside `internal/vault` that calls a provider's `Wrap` and
    `Unwrap`.
15. **One test double, one suite, two services in CI.** `internal/vault/transittest` is an in-process double of the
    service, with real AES-256-GCM, versions, minimum versions, trim, HMAC, tokens with lifetimes, AppRole, namespaces and
    the faults a test needs. One conformance suite runs against the double, OpenBao and HashiCorp Vault, so the double is
    only trusted because the same suite passes on the real services; a CI job runs the two real services as a matrix,
    each pinned by digest, as ADR 0047 does for identity providers. The HashiCorp Vault image is a test service only;
    nothing of it is linked or redistributed.

## Consequences

- **The key service is a dependency of the data plane and of the start.** A call that needs an unwrap and has nothing
  cached fails with `503` while the service is away, and a restart waits for it.
- **A token plus the database unwraps every data key while the token lives.** Short lifetimes with AppRole, a policy
  that cannot export, files with restricted modes and the service's own audit log are what narrows it.
- **An outage longer than the data key lifetime** (`cache.data_key_ttl`, 15 minutes at most) stops the calls that need
  an unwrap. A data key is never used past its lifetime.
- **Rotation and trimming stay acts of the operator**, in the service. A version raised out of reach or trimmed too
  early is found at the next start; a trimmed version is the lost-key case.
- **A move back needs a new key file:** the service's key cannot be exported, and a replaced local version cannot be
  activated again.
- **One provider of each name.** A direct move between two transit services goes through a key file.
- Later sprints inherit: Kubernetes authentication (sprint 8.2), the parser fuzz target (12.2) and the restore drill
  with a key service (12.3).

## Alternatives considered

**A vendor client module:** dependencies and a transport outside the egress guard, for seven requests.

**Sending the associated data in clear:** tenant identifiers would reach the key service and its audit log.

**An inner envelope** (a second layer encrypted or bound locally): a key on the host, or a format inside a format,
for a property the service provides and the start check proves.

**Trusting a version number instead of probing:** the one dangerous case, a service that accepts the field and
ignores it, is silent.

**The transit version number, a creation time, or the address and key name as the identifier:** each can name the wrong
key (the same number in another key, two versions rotated within a second, a key deleted and created again).

**A check value stored with the version:** a migration for what the HMAC gives without one.

**Following the service's latest version while running:** refused by the database's trigger for the other instances.

**Automatic rotation and trimming:** a token able to destroy key versions.

**Stale data keys during an outage:** the lifetime is the guarantee of VLT-06 and of erasure.

**Readiness tied to the service:** it withdraws every instance at once.

**Retries in the provider:** they multiply the latency of a call that is already waiting.

**Exit instead of waiting at start:** a supervisor restarts in a loop; waiting recovers alone, and
`start_timeout` gives the exit to operators who prefer it.

## History

- 2026-11-24: proposed with sprint 12.1.
