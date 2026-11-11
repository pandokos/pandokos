# 0006. People authenticate through an external OIDC provider only

- Status: proposed
- Date: 2026-11-11

## Context

Pandokos must not become an identity provider (principle 4), yet people need to administer it, and
some installations have no identity provider.

## Decision

- The administration API accepts bearer tokens. For people, these are tokens issued by a
  configured OpenID Connect provider and validated like any trusted-issuer token
  ([ADR 0005](0005-data-plane-authentication.md)).
- Pandokos keeps a local `user` record keyed by issuer and subject, holding only display
  attributes, status and tenant memberships. Authorization is always local: the identity provider
  says who someone is, Pandokos decides what they can do. Mapping the provider's groups to roles
  is optional and explicit.
- A user who belongs to several tenants names the tenant in the request path; the membership is
  checked on every request. Tokens are not minted per tenant.
- Disabling a user locally takes effect within the cache bound, whatever the remaining lifetime of
  their token.
- Pandokos runs no login pages, sessions or password storage. Interactive clients (the CLI, a
  future console) perform the authorization code flow with PKCE against the identity provider
  themselves.
- **Without an identity provider**, administration uses admin-scoped virtual keys through the CLI
  or API. The first one is created by `pandokos init`, expires, and exists to create the real
  configuration.
- The reference Compose files include an optional profile with a small open-source identity
  provider for those who want interactive sign-in and have none.

## Consequences

- No password, recovery or MFA code to write, audit or get wrong.
- Step-up authentication reduces to checking claims the identity provider asserts (IAM-17).
- An installation without an identity provider has no per-person identity for administrators
  beyond named keys; the audit log records the key. This is a stated limitation.

## Alternatives considered

**Built-in local accounts with passwords.** Convenient for a first install, and the beginning of
an identity product: reset flows, lockout, MFA, breach handling.

**Sessions issued by Pandokos after an OIDC login.** Needed only for a browser application served
by Pandokos itself; deferred with the console.
