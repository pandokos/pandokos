# 0022. Local accounts and console sessions

- Status: proposed
- Date: 2026-11-11

## Context

An installation without an identity provider needs a way for people to sign in to the console
(IAM-13, IAM-18). The common answer among self-hosted products is a small built-in user store
next to optional single sign-on: RabbitMQ, Grafana, MinIO, Gitea and Argo CD all work this way.
The risk is scope: password handling tends to grow into an identity product. This record fixes
what is built and what is not.

## Decision

### Local accounts

- A local account is a username and a password attached to one user record. Usernames are unique
  in the installation and compared without regard to case.
- **Accounts are created by administrators**: a platform admin for platform users, a tenant
  admin (with the permission to manage members) for people of the tenant. There is no
  self-registration.
- **Passwords** are stored as Argon2id hashes with a per-account salt; the parameters are stored
  with the hash, and a hash is upgraded at the next successful sign-in when the configured
  parameters change. Policy: a minimum length (12 by default), a generous maximum, no composition
  rules.
- **Initial and reset passwords are generated**, shown once to the administrator, and must be
  changed at the next sign-in. A user changes their own password in the console by giving the
  current one.
- **Reset** is done by an administrator in the console, or from the host with
  `pandokos user reset-password`, which is also the way back in when the last platform admin is
  locked out. Both are audited. There is no email and therefore no recovery by email.
- **Throttling.** Failed attempts add a growing delay per account and per source address, and a
  temporary lock after a configured number. An unknown username takes the same time and returns
  the same answer as a wrong password. Failures and locks are audited.
- **The first account** is created by `pandokos init` for the first platform admin, with a
  generated password. Pandokos has no default username or password.
- **Switch.** `auth.local.enabled` is on by default. With an identity provider configured, an
  operator can turn it off; the host command above remains as the emergency entry.

### Not built

Multi-factor authentication, recovery by email or questions, self-registration, social login,
password history, issuing tokens to other applications. An installation that needs any of these
connects an identity provider ([ADR 0006](0006-human-authentication-external-oidc.md)).

### Sessions

Local and identity-provider sign-in end the same way.

- The server creates a random 256-bit session token, stores its SHA-256 hash with the user and
  timestamps, and sends the token in a cookie with the `__Host-` prefix: `Secure`, `HttpOnly`,
  `SameSite=Strict`, no domain. Scripts cannot read it and other sites cannot send it.
- **Lifetimes**: idle 30 minutes, absolute 12 hours, both configurable.
- **End of session**: sign-out, expiry, password change (all other sessions of the user),
  disabling of the user, or revocation by an administrator.
- **Cross-site request forgery**: besides the same-site cookie, every state-changing request must
  carry a custom header and an `Origin` that matches the console.
- **Scope**: sessions are accepted only by the administration listener. The gateway has no code
  that reads the cookie and no database grant on the session or account tables.
- **Transport**: sign-in is refused over plain HTTP, except on the loopback address for local
  trials.
- Sessions live in PostgreSQL, so they work unchanged with several instances.

### Where it runs

All of this belongs to the `admin` role. Password verification is deliberately expensive; it is
rate-limited and never runs on the data plane.

## Consequences

- A new installation is usable by people with nothing but the container and the database.
- Pandokos stores passwords. That brings obligations this record accepts: careful hashing,
  throttling, audit, and a clear answer to "what if the database leaks" (hashes that are costly to
  attack, no reusable tokens).
- One more dependency, the Argon2 implementation from the Go extended cryptography module.
- Without multi-factor authentication, local accounts are weaker than a good identity provider.
  The documentation says so and recommends an identity provider, or at least a private
  administration network, for production.
- The scope list above is the line to hold. A request to add one of the excluded features is a
  request for a new decision record.

## Alternatives considered

**No local accounts; bundle an identity provider in the reference deployment.** Keeps passwords
out of Pandokos, and makes the smallest installation two products.

**Authentication delegated to a reverse proxy** (trusted headers). Common and easy to
misconfigure into a full bypass; it can be offered later as an explicit, opt-in mode.

**HTTP basic authentication**, as older management consoles do. Sends the password with every
request and has no sign-out.

**Stateless signed tokens as sessions.** No table, but no real revocation either, which IAM-21
requires.
