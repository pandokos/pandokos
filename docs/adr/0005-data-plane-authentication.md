# 0005. Data-plane authentication: virtual keys and trusted token issuers

- Status: accepted
- Date: 2026-11-11

## Context

Callers are services and agents, not people. Two situations must both work: an installation with
no identity provider at all, and a platform whose workloads already obtain tokens from an identity
provider that knows which tenant they act for. In both, the tenant must come from the credential
(TEN-02), and unmodified provider SDKs must be able to present the credential (GW-02).

## Decision

Every mechanism produces the same internal **principal**: tenant, identity, roles, scopes,
constraints. The rest of the pipeline does not know which mechanism was used.

**1. Virtual keys (always available).** Issued by Pandokos to a service account.

- Format: `pdk_<key id>_<secret><checksum>`. The key ID is public; the secret has 256 bits of
  entropy.
- Stored as SHA-256 of the secret. A slow password hash is unnecessary for a random 256-bit value
  and would cost latency on every cache miss.
- Always bound to one tenant. Optional scopes, model allow-list, expiry, limits and a pinned
  provider credential.

**2. Tokens from trusted issuers.** A JWT is verified against the issuer's published keys:
signature, issuer, audience (which must name this installation), expiry, and the claims the
registration requires. Two kinds of registration:

- *tenant-bound*: created by a tenant admin; every token maps to that tenant;
- *platform-level*: created by a platform admin; the tenant is read from a named claim and must
  be in the registration's explicit list of tenants.

The token subject is mapped to a service account by explicit rules; unmapped subjects are refused.
This also covers SPIFFE JWT-SVIDs and tokens produced by token exchange at the identity provider:
to Pandokos they are tokens from a registered issuer.

**3. Mutual TLS (later release).** The certificate identity maps to a service account.

**Where the credential travels.** In the authentication header native to the API family being
called: `Authorization: Bearer` for the OpenAI family, `x-api-key` for Anthropic,
`x-goog-api-key` for Gemini. Credentials in query strings are refused. The inbound credential is
always removed before forwarding.

**Caching.** Principals are cached in memory for a short time and invalidated by database
notifications; the cache lifetime is the upper bound on revocation (60 seconds by default).

## Consequences

- A stock SDK works by setting its base URL and using a virtual key or token as its "API key".
- The contract is identical whether one shared service calls for many tenants with tokens that
  carry the tenant, or one workload per tenant calls with its own identity. Moving between those
  models changes nothing in Pandokos.
- With a platform-level issuer, the identity provider is trusted to assert the tenant. The
  registration limits which tenants it can assert, and this trust is stated in the security model.
- Virtual keys are bearer secrets. Their exposure is limited by scope, limits, expiry, fast
  revocation and a format that secret scanners detect.

## Alternatives considered

**Only virtual keys.** The common choice in this product category. It forces a platform with an
identity provider to mint and distribute one more long-lived secret per tenant, and puts the
tenant binding in a secret instead of in a signed, short-lived assertion.

**Only identity-provider tokens.** Excludes the simplest installations.

**A dedicated `X-Pandokos-Key` header.** Unambiguous, but unmodified SDKs cannot send it without
custom configuration, and some cannot at all.

**DPoP or HTTP message signatures.** Stronger against replay, but no provider SDK produces them.
Mutual TLS covers the same need for callers that want it.
