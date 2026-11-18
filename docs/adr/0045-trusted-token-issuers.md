# 0045. Trusted token issuers: registration, selection and validation

- Status: accepted
- Date: 2026-11-16

Refines [ADR 0005](0005-data-plane-authentication.md).

## Context

ADR 0005 fixed the two kinds of registration (tenant-bound and platform-level) and the claims to
validate. It left open where registrations live, how a token finds its registration when several
tenants trust the same issuer, which algorithms are accepted and what a token principal is. A token
arrives on the inference listener from a caller that is not trusted, and its tenant derives from a
claim signed by a third party (TEN-02).

## Decision

1. **Inference listener only.** Tokens are validated inside `authn.Authenticator`, behind
   `gateway.Authenticator`. The administration listener does not accept them; sprint 4.2 decides for it
   together with users and sessions.
2. **The shape decides.** A value that starts with `pdk_` is a virtual key and is never retried as a
   token. Any other value of the shape of a JWT (three non-empty unpadded base64url segments, at most
   `tokens.max_token_bytes`) is a token. It is accepted in whichever header the API family reads:
   `Authorization: Bearer`, and `X-Api-Key` for the `anthropic` family.
3. **Two tables.** `trusted_issuer` (tenant-scoped, managed with `token_issuer:*`) and `platform_issuer`
   with `platform_issuer_tenant` (global, managed with `platform_issuer:*`). The tenant list maps a claim
   value to a tenant; the default value is the tenant's ID; there is no wildcard.
4. **One mandatory audience per registration.** There is no installation-wide audience and no way to
   accept any audience.
5. **Selection by issuer and audience.** The pair (`issuer`, `audience`) is unique in the installation
   over both kinds, enforced by `issuer_directory`, a table no runtime role can read, reached through
   the definer function `issuer_lookup`. A token selects the registration whose issuer equals its `iss`
   and whose audience is among its `aud` entries. A token that matches two registrations is refused
   (`ambiguous_audience`); the tenant is never chosen among candidates. A second registration with a
   taken pair is `409 conflict` with the same body as a name conflict.
6. **Algorithms.** An allow-list of ten asymmetric algorithms in code (`RS256 RS384 RS512 PS256 PS384
   PS512 ES256 ES384 ES512 EdDSA`), narrowed per registration (default `RS256`, `ES256`), applied before
   any key is looked up. `none` and `HS*` are never accepted. A key is used only if its type and size fit
   the algorithm (RSA of 2048 to 8192 bits, P-256, P-384, P-521, Ed25519), and the library is given
   exactly the algorithm of the header.
7. **The order of checks.** The fourteen checks of the [tokens guide](../operations/tokens.md) run in a
   fixed order, and nothing of a token is logged, audited or cached before its signature verified.
   `exp` is required; `nbf` and `iat` are honoured when present; skew 60 s; lifetime (`exp` minus `iat`)
   at most 24 h; the signature is verified on every request and no token or digest of one is stored.
8. **Exact subject mapping to a service account.** One row maps one subject of one registration, byte for
   byte, to one service account of the tenant; it is immutable. The subject claim is `sub` unless the
   registration names another. The principal is that service account with its roles; `KeyID` is the
   mapping's ID, and `IssuerID` and `Subject` are new fields. The usage record says `principal_kind`
   `token`, with `principal_id` the service account and `key_id` the mapping. A token principal has no
   scope list, no model list and no pin.
9. **Failures.** Every refusal is `401 unauthenticated`, byte for byte the answer to an unknown key; no
   new error code (ADR 0020). When the keys of the issuer cannot be obtained, or the database cannot
   answer, the answer is `503 unavailable` with `Retry-After: 5`. A failure that names a tenant is
   audited as `auth.failure` with `resource_type` `token_issuer` by a second definer function, with the
   bounds of ADR 0032; a failure before the tenant is known (checks 1 to 3, and those of a platform-level
   registration before the tenant claim is resolved) is logged only.
10. **Registrations are deleted for real**, with their mappings; `enabled` is the reversible switch. A
    registration has no secret, and the administration plane never fetches an issuer's documents.

The caches, their lifetimes and the notification payload `issuers` are in the
[overview](../architecture/overview.md), section 7; the retrieval of keys is [ADR 0046](0046-issuer-key-retrieval-and-caching.md).

## Consequences

- A shared issuer needs a tenant-specific audience. The operations guide requires an audience that
  names the installation and the tenant whenever the issuer is shared. If workloads present an audience
  that their tenant has not registered, or no longer registers, another tenant can register that pair
  and map the subject: those calls then run as that tenant's service account. The guide says to stop
  workloads before deleting a registration.
- A tenant learns that a pair is taken (the `409`), and a caller can learn the same from the time of a
  refusal. Both were accepted.
- One token cannot be revoked before `exp`: the mapping is deleted, or the token expires.
- `sub` may be opaque (Keycloak's is a UUID), hence `subject_claim`.
- The audit record of a platform-level deletion counts no mappings, because the platform session cannot
  see the tenants' rows; the cascade still deletes them.
- Nine permissions are added, six held by `tenant-admin` and three by `platform-admin`; a mapping is as
  sensitive as `virtual_key:write` because it gives an external identity the roles of a service account.

## Alternatives considered

**An installation-wide audience.** Cannot tell the tenants of a shared issuer apart.
**An audience assigned by Pandokos.** Excludes providers whose audience is fixed.
**The tenant chosen by the first mapping that matches.** Lets a tenant capture another's workload.
**Patterns or prefixes for subjects.** More surface for capture; exact match was confirmed.
**`principal_kind` `token` with the subject as `principal_id`.** Reports would no longer group a
service account's usage.
**Accepting tokens on the administration listener now.** Without users and sessions the mapping would
have nothing to map to there; deferred to sprint 4.2.

## History

Sprint 4.1. Maintainer decisions of 2026-11-16: tokens on the inference listener only; a token is
accepted wherever the family reads a credential; a free audience, unique installation-wide; exact
subject match only.
- 2026-11-18: accepted by the maintainer after review.
- 2026-11-18: sprint 5.3: deleting a mapping or an issuer (tenant-bound or platform-level) deletes the limit rules of its mappings, audited ([ADR 0049](0049-limit-rules.md), [ADR 0052](0052-administration-of-limits.md)).
