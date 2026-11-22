# 0058. The OpenID Connect relying party

- Status: accepted
- Date: 2026-11-22

Refines [ADR 0006](0006-human-authentication-external-oidc.md); amends
[ADR 0046](0046-issuer-key-retrieval-and-caching.md). Requirements IAM-08, IAM-09, IAM-12, IAM-21.

## Context

ADR 0006 chose an external OpenID Connect provider for people. Sprint 4.2 builds the relying party:
the authorization code flow with PKCE, the validation of the ID token, the mapping of a subject to a
user and the optional mapping of groups to roles. Sprint 4.1 already verifies JWS signatures against
keys fetched from a registered issuer.

## Decision

1. **D-12. One relying party written on what the repository has.** `internal/oidc` uses
   `internal/jwks` (now with provider metadata: `authorization_endpoint`, `token_endpoint`, `jwks_uri`,
   all `https`, the `issuer` of the document equal to the configured one) and the exported
   `authn.VerifyJWT`. No OpenID Connect or OAuth library is added. One provider, configured by the
   operator (`auth.oidc.*`); Pandokos is a confidential client (`client_secret_basic`, or `_post` by
   configuration) and uses PKCE as well.
   - **Start** (`GET /v1/auth/oidc/start`): a 32-byte random seed; `state`, `nonce` and the PKCE
     verifier are HMAC-SHA-256 functions of it, so nothing secret is stored on the server. One row of
     `oidc_login` holds SHA-256(`state`) for ten minutes (over 10 000 live rows: `429`). The seed goes
     to the browser in `__Host-pandokos_oidc`, `Secure; HttpOnly; SameSite=Lax; Max-Age=600` (`Lax`,
     because the callback is a navigation from the provider's site), and the answer is a `303` to the
     provider.
   - **Callback** (`GET /v1/auth/oidc/callback`): the cookie must exist and its derived state equal the
     `state` (constant time); the row is deleted and must have existed; an `error` parameter or a wrong
     `iss` (RFC 9207) is a failure; the code is redeemed through the egress guard (no redirect, 10 s,
     64 KiB); the ID token is validated (one allowed asymmetric algorithm, key by `kid`, `iss`, `aud`,
     `exp`, `nbf`, `iat` with 60 s of skew, a lifetime of at most 24 h, `nonce`, `azp`, `sub` of 1 to
     255 characters). The access, ID and refresh tokens are dropped at once and never stored, logged or
     sent to the browser. The session is the same as a local sign-in's, with method `oidc`; the answer is
     a `303` to `auth.oidc.post_sign_in_path`, a path fixed by configuration. A failure redirects with
     `?sign_in_error=sign_in_failed` (or `unavailable`) and sets no session.
   - The admin role now owns a key cache and fetches documents, but only of the configured issuer, and
     of hosts that may be private only when listed in `auth.oidc.private_hosts` (amends ADR 0046).
2. **D-13. Federated users and group rules.** A federated user is platform-managed. A subject is linked
   by an administrator (`createUser` with `sign_in: oidc`) or, with `auth.oidc.auto_provision`, created
   at the first sign-in when the rules give it a role; otherwise it is refused (`unmapped_subject`).
   Display name and e-mail are taken from the configured claims at each sign-in and nothing else of the
   token is kept. **Group rules** are configuration: `auth.oidc.group_mappings`, strings
   `<target>=<group>`, the target `platform:<role>` or `tenant:<tenant name>:<role>`, compared exactly
   against the claim `auth.oidc.groups_claim` (an array of strings or one string, a dotted path of at
   most four members, at most 256 groups). At each sign-in the rules are reconciled: rows with source
   `idp` that the groups no longer justify are removed and missing ones added, each in the tenant's own
   transaction with its audit row; rows with source `manual` are never touched. A rule that names a role
   a user cannot hold is refused at start, and so is one that names an unknown tenant once tenants exist.

## Consequences

- What a group gave lasts until the next sign-in of the user, so at most the absolute session lifetime
  after the provider withdrew it; disabling the user in Pandokos is immediate.
- There is no provider-initiated or back-channel logout, no refresh token and no `userinfo` call.
- Failures are audited as `auth.failure` with `method: oidc` and a reason; the subject is recorded only
  after the signature verified.
- The provider learns that a Pandokos instance signs people in, and when (data flows).
- Tenant-configured providers can reuse `internal/oidc` later, one `Provider` per tenant.

## Alternatives considered

**`go-oidc` and `x/oauth2`.** Two modules with their own HTTP client and cache, for one request and one
validation that the repository already has.

**State, nonce and verifier in a table.** Secrets at rest for ten minutes; derived from a cookie they
exist only in the browser.

**A public client.** No secret, but the provider cannot tell Pandokos from a copy of it.

**Group rules as API resources.** They are the operator's statement about its identity provider and are
reviewed with it; resources can follow if tenants get their own providers.

**Just-in-time users without a rule.** Every person of the provider would get an account.

## History

- 2026-11-22: proposed with sprint 4.2. Maintainer decisions of 2026-11-21: group rules stay in
  configuration (O-7); Keycloak for the full browser flow and Authentik for the claim shape (O-8).
- 2026-11-23: accepted by the maintainer after review.
