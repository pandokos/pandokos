# 0006. People sign in to the console only, through an external OIDC provider or a local account

- Status: accepted
- Date: 2026-11-11

## Context

People administer Pandokos through its web console ([ADR 0021](0021-web-console.md)). Pandokos
must not become an identity provider (principle 4), yet it has to be usable where no identity
provider exists. The maintainer's rule frames the decision: people are the only ones who sign in
interactively, and signing in enables the console's functions and nothing else.

## Decision

- **Interactive sign-in exists for people and for the console only.** Its result is a session
  that the administration API accepts on the console's behalf. A session never authenticates an
  inference request, and a service account or virtual key cannot sign in.
- **Two ways to sign in**, leading to the same user record, roles and session:
  1. *An external OpenID Connect provider.* Pandokos acts as relying party: it redirects the
     browser to the provider, receives the authorization code, redeems it with PKCE, validates
     the ID token (signature, issuer, audience, nonce, expiry) and opens a session. The
     provider's tokens are used for this exchange and then discarded; they never reach the
     browser.
  2. *A local account*, username and password, described in
     [ADR 0022](0022-local-accounts-and-sessions.md).
- **Authorization is always local.** The provider says who someone is; Pandokos decides what they
  can do. A user record is keyed by issuer and subject (or by username for a local account) and
  holds display attributes, status, tenant memberships and platform roles. Mapping the
  provider's groups to roles is optional and explicit.
- **Identity providers for people are configured at platform level** in this design. Providers
  configured by a single tenant for its own people are left for later.
- A user who belongs to several tenants names the tenant in the request path; the membership is
  checked on every request.
- Disabling a user ends their sessions within the cache bound, whatever the identity provider
  says.
- **Automation does not sign in.** The CLI, back ends and scripts use admin-scoped virtual keys
  or tokens from trusted issuers ([ADR 0005](0005-data-plane-authentication.md)).

## Consequences

- One session mechanism serves both sign-in methods, and the console never handles tokens.
- Because provider tokens are discarded after sign-in, a user removed at the identity provider
  keeps an open session until it expires or is revoked in Pandokos. The absolute session lifetime
  bounds this. Provider-initiated logout can be added later.
- Step-up authentication reduces to checking claims of the ID token at sign-in (IAM-17).
- Pandokos now serves a sign-in page and keeps sessions, which it did not in the first draft of
  this record. The scope of that responsibility is fixed in ADR 0022.

## Alternatives considered

**The console as a public OIDC client holding tokens in the browser.** No server-side session to
build, but tokens in the browser are exposed to script injection, and local accounts would need a
second mechanism anyway.

**Bearer tokens of people accepted directly by the administration API.** It would let people
drive the API from scripts with their own identity, which contradicts the rule that sign-in
enables only the console. Automation has keys for that.

**An external identity provider as the only way in**, with one bundled in the reference
deployment. It keeps Pandokos free of passwords, at the price of a second product to run and
understand for the smallest installation. Rejected by the maintainer in favour of local accounts.
