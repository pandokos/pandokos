---
id: SPRINT-4.2
title: Users, local accounts and sessions
epic: EPIC-4
release: v0.2
status: in-progress
design: draft 2026-11-21
---

# Sprint 4.2 — Users, local accounts and sessions

- Epic: [4 — Identity federation and RBAC](../epic.md)
- Implementation plan: [plan.md](plan.md)
- Previous sprint of the epic: [4.1 — Trusted token issuers](../4.1-trusted-token-issuers/sprint.md)
- Risk class: **high** (Pandokos starts to store passwords and to keep sessions; a third and a
  fourth way to authenticate on the administration listener; an OpenID Connect relying party that
  redirects browsers and redeems codes; the first cryptography module outside the standard
  library; a change of the principal model that sprint 4.3 builds on)
- Decisions implemented (in [docs/adr](../../../../docs/adr/README.md)): 0006 and 0022 (people, local
  accounts, sessions); 0005 and 0045 (tokens, now also on the administration listener); 0007 and
  0031 (five permissions, the principal kind `user`); 0017 and 0028 (twenty-seven operations; the
  subset grows); 0020 (seven codes); 0026 and 0027 (migration 0013: eleven tables, five definer
  functions); 0029; 0032 (fifteen audit actions); 0038 and 0046 (the admin role fetches the
  documents of the operator's identity provider); 0047 (Keycloak gains people); 0055; 0018 (one new
  module)
- New decision records written by this sprint: **0056** (users, memberships, and how a session
  becomes a principal; refines 0006, 0007, 0031), **0057** (local accounts and sessions: formats and
  numbers; refines 0022), **0058** (the OpenID Connect relying party; refines 0006, amends 0046),
  **0059** (idempotency keys); history lines on 0005, 0006, 0018, 0020, 0022, 0028, 0031, 0032,
  0045, 0046, 0047, 0055. Text in [plan.md](plan.md), part E.

This document is the design. The implementer follows [plan.md](plan.md); a gap in either document
is raised as a question, not filled silently ([planning/README.md](../../../README.md)).

Items carried into this sprint, and where each is decided:

| Item | From | Decided in |
|---|---|---|
| Tokens of trusted issuers on the administration listener | 4.1 O-1, epic | §5.10, D-5, O-2 |
| Mapping subjects to users (the "or users" of IAM-08) | 4.1 §2, epic | §5.9, D-4, **O-1** |
| The relying party reuses `internal/jwks` for ID tokens | 4.1 §11 | §5.8, D-12 |
| Idempotency keys (API-05) | 1.3 §2 | §5.11, D-16 |
| The principal kind `user`, platform role assignments, sessions on the admin listener only | 1.3 §11 | §5.2, §5.3, D-2, D-3 |
| `role_assignment` gains a user column | 1.3 §11 | not done: D-2 keeps the roles of members in a table of their own |
| The local account of `pandokos init` (IAM-14) | 1.3 §3 | §5.13, D-14 |
| Roles must be shaped so that 4.3 and issues #31, #35, #52 need no rework | epic, 4.3 note | §11 |

## 1. Outcome

A person administers Pandokos from a browser, with nothing but the container and the database, or
through the organisation's identity provider:

1. `pandokos init --admin-username NAME` creates the first platform administrator: a local account
   with a generated password, shown once. There is no default username and no default password.
2. The administrator signs in with `POST /v1/auth/sign-in` and receives a session in a cookie that
   scripts cannot read. The session says that the password must be changed, and until it is the
   API answers only the identity, the password change and the sign-out.
3. Administrators create further people: a platform administrator creates users that the platform
   manages; a tenant administrator creates local accounts for the people of its tenant and makes
   them members with a role. A generated password is shown once and must be changed.
4. Ten wrong passwords lock the account for fifteen minutes. The answer to a wrong password, an
   unknown username, a disabled user and an expired password is the same; every failure and the
   lock are in the audit log.
5. With `auth.oidc.enabled`, "sign in with the identity provider" is the authorization code flow
   with PKCE, state and nonce; it ends in the same session. Groups asserted by the provider can
   give memberships and roles by explicit rules of the configuration.
6. Disabling a user, changing a password, signing out or revoking ends sessions at once: every
   request reads the session and the user from the database.
7. A session cookie sent to the inference listener is not read: no code there reads cookies, and
   the gateway's database role has no privilege on any table of this sprint.
8. A workload may administer its tenant with a token of a trusted issuer, when the operator turns
   `tokens.admin_listener` on.
9. A `POST` that creates something accepts `Idempotency-Key`: a retry never creates a second
   resource.

**Exit** (epic): with no identity provider, a platform admin signs in over the API with a local
account and creates another user, who must change the generated password (T-831); ten wrong
passwords lock the account and appear in the audit log (T-832); a session cookie presented to the
inference port is ignored (T-833); with an identity provider configured, the same user flows work
through it (T-843); disabling a user ends their sessions within the bound (T-834).

## 2. Scope

### In scope

- Migration `0013_users_sessions`: `app_user`, `local_account`, `user_identity`, `user_session`,
  `platform_role_assignment`, `signin_throttle`, `oidc_login`, `idempotency_key` (global with
  policies), `membership_directory` (global, no grant), `membership`, `membership_role`
  (tenant-scoped); five definer functions; two `EXECUTE` grants for tokens; the actor type `host`
  in the tenant audit log.
- Packages `internal/password` (Argon2id), `internal/session`, `internal/account` (users, sign-in,
  throttling, reset), `internal/oidc` (relying party), `internal/idem` (idempotency keys).
- `internal/authz`: the kind `user`, five permissions, roles with a scope instead of a holder kind.
- Administration API, specification first: sign-in, sign-out, password change, own sessions, the
  sign-in methods, the two OIDC endpoints; platform users; tenant members (27 operations); the
  generator learns public operations, a redirect response, one header parameter and a lenient
  query. CLI commands for users and members.
- Host commands `pandokos user reset-password` and `pandokos user unlock`; `pandokos init
  --admin-username`.
- Tokens of trusted issuers on the administration listener, behind `tokens.admin_listener`.
- Idempotency keys on every operation whose success status is 201.
- Tests: units, database, isolation harness, in-process and end to end; an authorization-code
  issuer in `test/fakeidp`; Keycloak with people and groups; a minimal page in headless Chrome.
- Documentation: a sign-in guide, the threat model, the data model, the API, permission and
  configuration references.

### Not in scope

| Item | Where |
|---|---|
| The other built-in roles, custom roles, platform auditor, the no-escalation rule as a general mechanism, effective permissions per tenant in `/v1/me`, support actions, reasons (AZ-03 to AZ-06, AZ-09 to AZ-11, SEC-08) | 4.3 |
| A bearer token that acts as a user (O-1) | not built; see D-4 |
| Multi-factor authentication, recovery by email, self-registration, password history, social login | not built (ADR 0022) |
| Identity providers configured by a tenant for its own people; more than one provider | later (ADR 0006) |
| Provider-initiated logout, back-channel logout, refresh tokens, `userinfo`, `id_token_hint` at sign-out | later; the absolute lifetime bounds the exposure |
| Step-up authentication from provider claims (IAM-17) | R5 |
| Changing a user's sign-in method (local to federated) | not scheduled: the person gets a new user |
| Deleting or anonymising a user | 6.3, with tenant deletion (PRV; §11) |
| Reading the audit log (the failures written here) | 6.2 |
| A check of new passwords against a list of breached passwords (O-9) | not scheduled |
| A page served by the binary (O-6) | not built: Pandokos serves no pages |
| Sessions in the CLI | not built: automation uses keys or tokens (ADR 0006) |
| Metrics of sign-ins and sessions | 6.1 |

## 3. Requirements claimed

Meanings quoted from [docs/requirements](../../../../docs/requirements/README.md).

| ID | Requirement | Claim |
|---|---|---|
| **IAM-08** | "Token subjects map to service accounts or users by explicit rules. An unmapped subject is rejected" | the "or users" part, as decided in D-4: the subject of the identity provider's ID token maps to a user by an explicit link or an explicit group rule; any other subject is refused (T-814, T-816) |
| **IAM-09** | "People sign in to the administration API through an external OpenID Connect provider, with the authorization code flow and PKCE, or with a local account (IAM-18). Both lead to the same kind of user record, roles and session" | full |
| **IAM-10** | "A federated identity links to a local user record with memberships in one or more tenants. The active tenant of a request is explicit and validated against the memberships" | full: the tenant is the one of the path, checked on every request (§5.3) |
| **IAM-11** | "A user can be disabled locally whatever the IdP says. Their sessions end within a documented bound" | full: the bound is the request in flight (§5.6) |
| **IAM-12** | "Groups or roles asserted by the IdP can be mapped to Pandokos roles by explicit, optional rules" | full (§5.9) |
| **IAM-13** | "An installation without an IdP is fully usable: automation and the CLI use admin-scoped virtual keys, people use local accounts" | the accounts part |
| **IAM-14** | "The first platform admin is created by an explicit initialization command: an expiring admin key and, from R2, a local account whose generated password must be changed at first sign-in. There are no default credentials. Pandokos warns while the bootstrap key is still active" | the account part (§5.13) |
| **IAM-18** | "Pandokos keeps local accounts, a username and a password, so that people can sign in without an IdP. Accounts are created by an administrator. The mechanism is on by default and can be switched off when an IdP is configured" | full |
| **IAM-19** | "Passwords are stored only as a memory-hard salted hash. A minimum length applies. A generated or reset password must be changed at the next sign-in. A password is reset by an administrator or from the host CLI; there is no self-registration and no recovery by email" | full |
| **IAM-20** | "Failed sign-ins are throttled per account and per source and are audited. The response does not reveal whether the username exists" | full |
| **IAM-21** | "Signing in creates a server-side session carried by a cookie that scripts cannot read. Sessions have an idle and an absolute lifetime, can be revoked, end on sign-out, password change and disabling of the user, and are protected against cross-site request forgery. Local and IdP sign-in produce the same session" | full |
| **IAM-22** | "Only people sign in interactively, and a session is accepted only by the administration API. A session or a password never authenticates an inference request, and a service account cannot sign in interactively" | full |
| **API-05** | "Create operations accept an idempotency key" | full (§5.11) |
| **PRV-01** | "Personal data is minimal: a user record holds the IdP subject and the display attributes the IdP supplies" | full: a user holds a display name and an e-mail address, a local account a username, an identity an issuer and a subject; no address, no user agent, no last sign-in place (T-776) |

Touched, claimed elsewhere, and kept true: **API-08** (the sign-in endpoints are operations of the
document; T-844 drives them from a page), **TEN-02, TEN-04** (a user's tenant is the path's, valid
only with a membership read in that tenant's session; two tenant-scoped tables with fixtures),
**AZ-01, AZ-02** (five permissions; public operations are an explicit, tested list of four),
**AZ-06** (a platform role is a row of its own table; a user acting on the platform has no
tenant), **AUD-01 to AUD-03** (fifteen actions, sign-in failures, in the transaction of the
change), **SEC-02 to SEC-04** (replay exposure of sessions documented; uniform sign-in answers,
with one accepted disclosure, R-6; §5.15), **AZ-11** (`/v1/me` names the user and its tenants with
their roles; effective permissions per tenant are 4.3's; not claimed).

## 4. Repository layout

New:

```
internal/password/               Argon2id PHC hashing, policy, generator (the only importer of golang.org/x/crypto)
internal/session/                token, cookie, store, purge
internal/account/                users, local sign-in, throttling, password change and reset, bootstrap account
internal/oidc/                   relying party: metadata, authorization URL, code exchange, ID token, groups
internal/idem/                   idempotency keys: claim, commit mark, completion, replay
internal/admin/auth.go           sign-in, sign-out, password, sessions, methods, OIDC start and callback
internal/admin/origin.go         transport rule, origin and CSRF checks, source address
internal/admin/users.go          platform users
internal/admin/members.go        tenant members
internal/cli/users.go            platform-user and member commands
cmd/pandokos/user.go             host commands: user reset-password, user unlock
internal/store/migrations/sql/0013_users_sessions.sql
internal/store/queries/sql/users.sql, sessions.sql, idempotency.sql
test/browser/                    the minimal page and its test (build tag browser)
docs/operations/sign-in.md
docs/adr/0056-…, 0057-…, 0058-…, 0059-…
```

Changed: `internal/authz/authz.go`, `internal/authn/{authn,token,tokens}.go` (an exported
verifier; the admin role may accept tokens), `internal/jwks/{jwks,fetch}.go` (provider metadata),
`internal/admin/{admin,me,problem,authfail}.go`, `internal/audit/audit.go`, `internal/apigen`,
`internal/adminapi/runtime.go`, `internal/config/*`, `cmd/pandokos/{init,serve,main}.go`,
`api/openapi/admin.v1.yaml`, generated code, `test/fakeidp`, `test/idp`, `test/apitest`,
`test/isolation/spec.go`, `test/repo/*`, `.golangci.yml`, `Makefile`, `.github/workflows/ci.yml`,
`go.mod`, `go.sum`, documentation.

## 5. Architecture

### 5.1 Which credential a request of the administration listener carries (D-3, D-5)

The `Before` hook decides in this order; the first row that applies wins.

| # | The request | Path |
|---|---|---|
| 1 | matches a **public** operation (`getAuthMethods`, `signIn`, `startOidcSignIn`, `completeOidcSignIn`) | no credential is read; the origin rule of §5.7 applies to all four, its checks to `signIn` |
| 2 | has an `Authorization` header | key, or token when `tokens.admin_listener` (§5.10); a cookie is ignored; no CSRF check, because no browser attaches this header by itself |
| 3 | has the cookie `__Host-pandokos_session` | session (§5.6), after the checks of §5.7 |
| 4 | otherwise | `401 unauthenticated` |

The inference listener is not changed: `internal/gateway` reads no cookie, and T-833 and T-R55 keep
it so.

### 5.2 Users, accounts, memberships (D-1, D-2)

- **`app_user`** is a person: display name, e-mail address, state (`active`, `disabled`), the sign-in
  method (`local` or `oidc`, exactly one, immutable) and **who manages the account**: `tenant_id`
  NULL means the platform, a value means that tenant. Both are immutable.
- **`local_account`** (username, Argon2id hash, must-change flag, expiry of a generated password) or
  **`user_identity`** (issuer, subject) hangs on the user; a composite foreign key on
  (`id`, `sign_in`) makes the two exclusive without a trigger.
- **The managing rule.** A user that a tenant manages is a member of that tenant only and holds no
  platform role (two triggers). Only the administrators of the managing side reset its password,
  disable it, unlock it or end its sessions. Without this rule the administrator of tenant A could
  reset the password of a person who is also an administrator of tenant B and sign in as them. A
  person who works in several tenants, or on the platform, is managed by the platform; a
  federated user always is, because the identity provider is the platform's (ADR 0006).
- **`membership`** (tenant-scoped) says that a user is a member of a tenant; **`membership_role`**
  holds the member's roles. Both carry `source`: `manual`, or `idp` for rows that the group rules
  keep (§5.9). The gateway role has no privilege on either. `role_assignment` stays the table of
  service accounts, which the gateway reads.
- **`platform_role_assignment`** holds the platform roles of users.
- **Roles a user can hold in this sprint**: `tenant-admin` in a tenant, `platform-admin` on the
  platform. `inference` is refused for a member (a user cannot call a model). 4.3 adds the rest.
  Whoever assigns a role must hold every permission of it (`authz.Assignable`), which is the
  no-escalation rule for the two roles that exist.
- **`membership_directory`** (user, tenant) is kept by a trigger and read only by `user_tenants`, a
  definer function that answers in the platform scope: the one path from a user to its tenants
  across the tenant policy. `platform_user_lookup` is its counterpart for a tenant's session: it
  finds one platform-managed user by ID or username so that it can be made a member.
- **Visibility.** The platform scope sees every user (sign-in happens there, before any tenant is
  known). A tenant's session sees the users it manages and its members, and writes only the
  former. An unscoped session sees nothing. Platform operations show platform-managed users only:
  a platform administrator who needs to act on a tenant's user waits for the support action of 4.3
  or uses the host command.

Full SQL: plan.md, appendix A1 (applied and exercised while designing, §12).

### 5.3 From a session to a principal (D-3)

A session identifies a user, not a tenant. The principal is built **for each request** from the
operation's permission:

| Scope of the permission | Principal |
|---|---|
| `platform` | `Kind: user`, no tenant, `Roles` = rows of `platform_role_assignment` |
| `tenant` | the tenant of the path; the member's roles are read in that tenant's session. No membership: `404 not_found`, the answer for a tenant that does not exist. Otherwise `Kind: user`, `TenantID`, `TenantName`, `TenantState`, `Roles` = rows of `membership_role` |
| `self` | `Kind: user`, no tenant, platform roles |

Changes to `authz`:

- `KindUser`. `Principal` gains `SessionID` and `MustChangePassword`; for a user `ID` is the user
  ID, `Name` the username (or the display name of a federated user) and `KeyID` is empty.
- `Role.Kind` becomes `Role.Scope` (`platform`, `tenant`). A role counts for a principal when the
  scope fits: a platform role for a principal without a tenant that is not a service account; a
  tenant role for a principal with a tenant that is not a platform key. The three roles keep
  their permissions; `tenant-admin` gains `member:read` and `member:write`, `platform-admin`
  gains `user:read` and `user:write`.
- A user principal holds the two self permissions `identity:read` and `session:self` whatever its
  roles. `session:self` is refused for keys and tokens (they have no session).
- `Decide`: a platform permission is allowed for `platform` and for `user` without a tenant; the
  tenant-state rule and the tenant rule apply to `user` with a tenant as to a service account.
  The order of the five rules of ADR 0031 does not change.
- **The must-change gate** is in the hook, before `Decide`: a session whose account has
  `must_change` may call `getMe`, `changePassword`, `signOut` and nothing else:
  `403 password_change_required`.

`GET /v1/me` for a user returns `principal_type: user`, the ID, the name, `must_change_password`,
the platform roles and permissions, the session (`method`, `expires_at`) and `memberships`: for
each tenant of `user_tenants`, read in that tenant's session, the tenant reference and the role
names. Effective permissions per tenant are added by 4.3 to the same members.

### 5.4 Passwords (D-6, D-7)

- **Hash.** Argon2id, version 19, 16 random bytes of salt, 32 bytes of output, stored as one PHC
  string `$argon2id$v=19$m=…,t=…,p=…$salt$hash` (unpadded standard base64), so the parameters are
  stored with the hash. Defaults `m=65536` (64 MiB), `t=3`, `p=1`: about 190 ms on the design
  machine (§12). Floors refused at start: `m < 19456`, `t < 2`, `p` outside 1 to 4. When the
  stored parameters differ from the configured ones, the hash is replaced at the next successful
  sign-in, in the transaction of the sign-in. Parameters read from a row are bounded
  (`m ≤ 1048576`, `t ≤ 10`, `p ≤ 16`) before they are used. There is no pepper: it would be a
  second secret to keep, and the vault key belongs to the gateway.
- **Concurrency.** At most `auth.local.max_concurrent_hashes` (4) computations at once, so 256 MiB
  with the defaults; a request that waits more than 2 s gets `503 unavailable` with
  `Retry-After: 1` and counts as no attempt. Only `internal/password` computes hashes; it runs in
  the admin role only (T-R54).
- **Policy.** Length from `auth.local.password_min_length` (12; the configuration accepts 8 to
  128) to 256 characters and 1024 bytes; no composition rule; any Unicode, compared as bytes, never
  truncated; not the username; a new password differs from the current one. A refused password is
  `400 invalid_request` on the member `new_password` with a fixed sentence.
- **Generated passwords** (first password, reset): 24 characters of `a-z2-7` from `crypto/rand`
  (120 bits), returned once in the response, never stored or logged. The account gets
  `must_change` and `password_expires_at` = now + `auth.local.generated_password_ttl` (72 h);
  after that the password is refused like a wrong one.
- **Change.** `changePassword` takes the current and the new password. A wrong current password
  is `400 invalid_request` on `current_password` and counts in the throttle of §5.5. On success
  the flag and the expiry are cleared, every other session of the user is deleted, and the
  session in use is replaced by a new one (new token, new cookie).
- **Reset** by an administrator (`resetUserPassword`, `resetMemberPassword`) or from the host:
  a generated password, `must_change`, every session deleted, the account's throttle row deleted.

### 5.5 Sign-in with a local account: throttling, lock, uniform answers (D-8)

`POST /v1/auth/sign-in` with `{"username", "password"}`:

1. §5.7. `auth.local.enabled` false: `403 sign_in_disabled`.
2. The username is lower-cased; the **account key** is SHA-256(`account`, NUL, the first 64 bytes of
   it), the **source key** SHA-256(`source`, NUL, the address prefix: `/32` for IPv4, `/64` for
   IPv6). The source address is the peer's, or, when the peer is in
   `listeners.admin.trusted_proxies`, the right-most entry of `X-Forwarded-For` that is not.
3. Both rows of `signin_throttle` are read. `locked_until` in the future, or less than the delay
   since `last_failure_at`: `429 too_many_attempts` with `Retry-After`, and no password is
   checked. The delay after *n* failures in the window is 0 for *n* < 3, then 1 s doubling to at
   most 30 s. A row exists for a username **whether or not an account has it**, so an unknown
   username is delayed and locked exactly like a real one.
4. The account is read in the platform scope. One Argon2id computation always runs: against the
   stored hash, or against a dummy hash of the configured parameters when there is no account.
5. **Failure**: no account, wrong password, user disabled, generated password expired. All four
   give `401 unauthenticated` with the same body and headers. Both rows count one failure
   (`failures` restarts when `auth.local.failure_window`, 15 min, has passed). At
   `auth.local.max_failures` (10) the account row is locked for `auth.local.lock_duration`
   (15 min); at `auth.local.source_max_failures` (50) the source row is. While an account is
   locked even the right password is refused.
6. **Success**: the account row is deleted, the hash upgraded if needed, a session presented with
   the request is deleted (no fixation), the oldest sessions beyond `auth.session.max_per_user`
   (10) are deleted, a session is created and its cookie set. The body is `Session`:
   user, method, `must_change_password`, `created_at`, `expires_at`, `idle_timeout_seconds`.

Audit (§5.14): each failure of step 5 is an `auth.failure` row, within the existing cap of 60 a
minute per instance; the lock is one `user.lock` row, written outside the cap; a success is
`auth.sign_in`. The presented username is never written anywhere: a row names the user ID when
the account exists and nothing otherwise, because people type passwords into username fields.

What throttling does not do: concurrent attempts that passed step 3 together are all checked, so
up to `max_concurrent_hashes` − 1 guesses can exceed a threshold; an attacker can lock a known
username out for the lock duration (the host command and the identity provider are the ways
round, R-4); a throttle row is a digest of what was typed and lives at most window + lock.

### 5.6 Sessions (D-9)

- **Token**: `pds_` followed by 32 random bytes in unpadded base64url (47 characters). The table
  stores its SHA-256; the lookup is by that hash, so nothing is compared in variable time.
- **Cookie**: `__Host-pandokos_session=<token>; Path=/; Secure; HttpOnly; SameSite=Strict`, no
  `Domain`, no `Max-Age` (it ends with the browser session; the server enforces the lifetimes).
  The attributes never vary, loopback included (browsers accept `Secure` cookies from loopback
  origins; verified with Chrome, §12).
- **Lifetimes**: idle `auth.session.idle_timeout` (30 min), absolute
  `auth.session.absolute_lifetime` (12 h, at most 7 days), the second fixed in `expires_at` when
  the session is created. `last_used_at` is written at most once a minute per session, so the
  idle limit can end a session up to one minute early, never late.
- **Every request** reads the session by hash and its user in one statement in the platform
  scope: no session, expired, idle, or a user that is not `active` is `401 unauthenticated` and
  the cookie is cleared. There is no session cache. The bound of IAM-11 is therefore the request
  in flight.
- **A session ends** by sign-out, expiry, password change (the others), reset, disabling of the
  user, revocation by an administrator (`revokeUserSessions`, `revokeMemberSessions`) or by the
  user (`deleteOwnSession`), removal of the last membership of a tenant-managed user. Ending is
  deleting the row.
- **Purge**: the admin role deletes expired and idle sessions, old throttle rows, expired
  sign-in transactions and expired idempotency keys every 5 minutes. The worker has no privilege
  on these tables.
- The response of every operation of the listener already carries `Cache-Control: no-store`; this
  sprint adds `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer` and
  `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'` to all of them.

### 5.7 Transport, origin and cross-site request forgery (D-10, D-11)

**The expected origin** of the administration API:

| Configuration | Expected origin |
|---|---|
| `auth.public_origin` set | exactly that value |
| unset, listener with TLS | `https://` + the `Host` header of the request |
| unset, listener without TLS on a loopback address | `http://` + the `Host` header, whose host must be `127.0.0.1`, `[::1]` or `localhost` |
| unset, listener without TLS elsewhere (`listeners.admin.allow_plaintext`) | none: the four public operations and every session request answer `403 sign_in_disabled`; a warning at start names `auth.public_origin` |

`auth.public_origin` must be `https://host[:port]`, or `http://` with a loopback host; with
`allow_plaintext` it states that TLS ends in front. This is the refusal of sign-in over plain HTTP
outside loopback: Pandokos never answers a sign-in on a listener that it knows to be reachable in
clear text without the operator having named the HTTPS origin in front of it.

**Checks** on `signIn`, and on every request with a session whose method is not `GET` or `HEAD`:

1. the header `X-Pandokos-Csrf: 1` is present (a cross-site form cannot set it, a cross-origin
   script needs a preflight that the API never grants: it sends no CORS header);
2. `Origin` is present and equals the expected origin;
3. `Sec-Fetch-Site`, when present, is `same-origin`.

A failure is `403 csrf_rejected`, before the body is read and before any credential is checked.
The two OIDC endpoints are navigations and are protected by the state and the binding cookie
instead (§5.8). `GET` operations change nothing (the only write a `GET` causes is `last_used_at`).

### 5.8 Sign-in through the identity provider (D-12)

One provider, configured by the operator (`auth.oidc.*`); Pandokos is a confidential client and
uses PKCE as well. The relying party is written on `internal/jwks` and `authn.VerifyJWT`; no
OpenID Connect library is added (as 4.1 D-8).

**Start** — `GET /v1/auth/oidc/start` (a navigation):

1. A 32-byte random **seed**. From it: `state` = HMAC-SHA-256(seed, `state`),
   `nonce` = HMAC(seed, `nonce`), `code_verifier` = HMAC(seed, `pkce`), each in base64url;
   `code_challenge` = SHA-256 of the verifier.
2. One row of `oidc_login`: SHA-256(`state`) and an expiry of 10 minutes. Over 10 000 live rows
   the start answers `429 too_many_attempts`.
3. The seed goes to the browser in `__Host-pandokos_oidc=<seed>; Path=/; Secure; HttpOnly;
   SameSite=Lax; Max-Age=600`. `Lax`, not `Strict`: the callback is a navigation that comes from
   the provider's site, and a `Strict` cookie would not be sent with it.
4. `303` to the provider's `authorization_endpoint` with `response_type=code`, `client_id`,
   `redirect_uri` = expected origin + `/v1/auth/oidc/callback`, `scope`, `state`, `nonce`,
   `code_challenge`, `code_challenge_method=S256`.

Nothing secret is stored on the server: nonce and verifier exist only as functions of a cookie
that scripts cannot read. The row makes a state usable once and for ten minutes; the cookie binds
the flow to the browser that started it, which is what stops a sign-in forged with another
person's code.

**Callback** — `GET /v1/auth/oidc/callback?code=…&state=…` (unknown query parameters are
tolerated here and only here: providers add their own):

1. No seed cookie, or `state` different from the one derived from it (constant time):
   failure `state_mismatch`. The cookie is cleared in every case.
2. The row of SHA-256(`state`) is deleted and must have existed and not expired:
   else `state_unknown`. An `error` parameter of the provider: `provider_error`. An `iss`
   parameter, when present, must equal the issuer (RFC 9207).
3. The code is redeemed at the `token_endpoint`: `grant_type=authorization_code`, `code`,
   `redirect_uri`, `code_verifier`, client authentication `client_secret_basic` (or `_post` by
   configuration); through the egress guard, no redirect, 10 s, an answer of at most 64 KiB.
4. The **ID token** is validated by `authn.VerifyJWT`: one allowed asymmetric algorithm of
   `auth.oidc.algorithms`, key from the provider's key set by `kid`, signature, `iss` equal to the
   configured issuer, `aud` contains the client ID, `exp` (required), `nbf` and `iat` with
   60 s of skew, a lifetime of at most 24 h. Then by `internal/oidc`: `nonce` equal to the derived
   one (constant time); `azp`, when present, equal to the client ID, and required when `aud` has
   more than one value; `sub` of 1 to 255 characters without control characters.
5. Access token, ID token and any refresh token are dropped here; they are never stored, logged
   or sent to the browser.
6. The user is found or made (§5.9), must be `active`, the group rules are applied, a session is
   created (`method: oidc`) and its cookie set.
7. `303` to `auth.oidc.post_sign_in_path` (default `/`, a path of the same origin fixed by
   configuration; the request cannot name a destination). On a failure of steps 1 to 6 the
   redirect carries `?sign_in_error=sign_in_failed` (or `unavailable` when the provider or the
   database could not be reached) and no session is set.

Provider metadata (`authorization_endpoint`, `token_endpoint`, `jwks_uri`, all `https`, the
`issuer` of the document equal to the configured one) comes from `jwks.Cache.Metadata`, cached as
keys are (ADR 0046). The admin role now owns a key cache and fetches documents, but only of the
issuer the operator configured, and of hosts that may be private only when listed in
`auth.oidc.private_hosts`.

Failures are audited as `auth.failure` with `detail.method: oidc` and the reason; the subject is
recorded only after step 4 succeeded.

### 5.9 Federated users and group rules (D-4, D-13)

**D-4, the reading of IAM-08 for users.** The subject that maps to a user is the subject of the
ID token of the configured provider, and the mapping is `user_identity` (issuer, subject → user).
A bearer token of a trusted issuer never acts as a user: on the administration listener it maps
to a service account, as on the inference listener. ADR 0006 already rejected "bearer tokens of
people accepted directly by the administration API"; this sprint does not reopen it (O-1).

A subject maps to a user in one of two explicit ways, and is refused (`unmapped_subject`) otherwise:

1. **A link made by an administrator**: `createUser` with `sign_in: oidc` and the subject. The
   issuer is always the configured one.
2. **A group rule**, when `auth.oidc.auto_provision` is true: at the first sign-in of an unknown
   subject, if the rules below give it at least one role, the user is created with the link.

**Display attributes** (PRV-01): at each sign-in `display_name` and `email` are taken from the
claims `auth.oidc.name_claim` (`name`) and `auth.oidc.email_claim` (`email`), truncated to the
column limits; nothing else of the ID token is kept.

**Group rules** (IAM-12) are configuration, `auth.oidc.group_mappings`, a list of strings
`<target>=<group>`: the target is `platform:<role>` or `tenant:<tenant name>:<role>`, the group is
everything after the first `=`, compared exactly. The groups of a user are the claim
`auth.oidc.groups_claim` (`groups`; a dotted path of at most four object members, such as
`realm_access.roles`), which must be an array of strings or one string; any other shape gives no
group and a log line. At most 256 groups are read.

At each sign-in the rules are **reconciled**: rows with `source: idp` that the groups no longer
justify are removed, missing ones are added (membership, role, platform role), each in the
tenant's own transaction with its audit row; rows with `source: manual` are never touched, and a
role that exists as `manual` is not duplicated. A rule that names an unknown tenant or a role a
user cannot hold is refused at start. Without `auth.oidc.group_mappings` nothing is reconciled.
What a group gave lasts until the next sign-in of the user, so at most the absolute session
lifetime after the provider withdrew it; disabling the user in Pandokos is immediate.

### 5.10 Tokens of trusted issuers on the administration listener (D-5)

With `tokens.admin_listener: true` (default false) and `tokens.enabled`, the admin authenticator
accepts `KindToken`: the same selection, validation and mapping as on the inference listener
(ADR 0045), with the admin pool, the shared key cache and no principal cache. The principal is the
mapped **service account** with all its roles, so a mapping to an account with `tenant-admin`
administers the tenant (API-06). A token never yields a platform principal or a user. Migration
0013 grants the admin role `EXECUTE` on `issuer_lookup` and `platform_issuer_tenant_lookup`.
Failures are audited by the admin role directly, with the row of ADR 0032 and
`detail.listener: admin`. The switch is off by default because it makes every subject mapping to
an administrative service account a way into the control plane, and that should be an operator's
decision (O-2).

### 5.11 Idempotency keys (D-16)

Every operation whose success status is 201 accepts the header `Idempotency-Key` (1 to 128
printable ASCII characters). Without the header nothing changes.

- **Scope of a key**: the tenant of the path (or the platform), the principal (kind and ID) and
  the key. The row stores the operation and SHA-256(salt, operation, path, body) with a random
  salt, never the body.
- **Claim** (in the hook, after authorization, in a transaction of the operation's scope): a new
  row `pending`, locked for 60 s. An existing row: another operation or another request hash is
  `422 idempotency_key_reused`; a live lock is `409 idempotency_in_progress` with `Retry-After`;
  a stale lock without a commit mark is taken over (`attempt` + 1).
- **Commit mark**: `audit.RecordTenant` and `audit.RecordPlatform`, which every create operation
  calls inside its transaction (AUD-03), also set `committed_at`, the resource type and ID on the
  claim carried by the context, with `WHERE attempt = $n`. No row updated is an error: the
  transaction of a superseded attempt rolls back. This is what makes the creation and its memory
  one atomic fact, without a change to any handler.
- **Completion** (after the handler): on 2xx the response status and body are stored, **without
  the members that are shown once** (`key`, `secret`, `password`, `initial_password`: the
  sensitive-member list of `internal/audit`); otherwise the row is deleted unless it carries the
  commit mark.
- **Replay**: a completed row answers with the stored status and body and the header
  `Idempotency-Replayed: true`; the handler does not run and nothing is audited. A member shown
  once is absent from a replay, so the create schemas no longer require it. A row with the commit
  mark and no stored response (the process died between commit and completion) answers
  `409 idempotency_response_lost`: the resource exists, its ID is in the audit log.
- **Retention**: `idempotency.retention` (24 h), then the key can be used again.

### 5.12 Administration API (specification first)

The full additions to the document are in plan.md, appendix A3. Operations (permission in
brackets; `public` means none):

| Group | Operations |
|---|---|
| sign-in | `getAuthMethods` `GET /v1/auth/methods` [public]; `signIn` `POST /v1/auth/sign-in` [public]; `startOidcSignIn` `GET /v1/auth/oidc/start` [public]; `completeOidcSignIn` `GET /v1/auth/oidc/callback` [public] |
| own session | `signOut` `POST /v1/auth/sign-out`; `changePassword` `POST /v1/auth/password`; `listOwnSessions` `GET /v1/auth/sessions`; `deleteOwnSession` `DELETE /v1/auth/sessions/{session}` [all `session:self`] |
| platform users | `listUsers`, `getUser` [`user:read`]; `createUser`, `updateUser`, `disableUser`, `enableUser`, `resetUserPassword`, `unlockUser`, `revokeUserSessions` [`user:write`], under `/v1/users` |
| tenant members | `listMembers`, `getMember` [`member:read`]; `createMember`, `updateMember`, `deleteMember`, `disableMember`, `enableMember`, `resetMemberPassword`, `unlockMember`, `revokeMemberSessions` [`member:write`], under `/v1/tenants/{tenant}/members` |

Rules that the schemas cannot say:

- `createUser`: `sign_in: local` needs `username` and returns `initial_password` once;
  `sign_in: oidc` needs `subject` and `auth.oidc.enabled`. `platform_roles` are checked with
  `authz.Assignable`. With `auth.local.enabled` false a local user cannot be created
  (`409 invalid_state`).
- `createMember`: `account: new` creates a local account that the tenant manages (`username`,
  optional display attributes) and returns `initial_password`; `account: existing` names a
  platform-managed user by exactly one of `user_id` and `username`, and answers `404 not_found`
  when there is none. `roles` has at least one role.
- `deleteMember` removes the membership and its roles; for a user the tenant manages it also
  disables the account and ends its sessions (it has no other place to act).
- `disableMember`, `enableMember`, `resetMemberPassword`, `unlockMember`, `revokeMemberSessions`
  act on users the tenant manages only; for a platform-managed member: `409 invalid_state`.
- A user cannot disable itself or remove its own last platform role (`409 invalid_state`); the
  way back from a lost platform is `pandokos init --recover`.
- Users and accounts are never deleted (§11).
- Usernames are unique in the installation (ADR 0022), so `409 conflict` on a username tells a
  tenant administrator that the name is taken somewhere (R-6).

The generator's subset grows by four constructs (ADR 0028, history line): `x-pandokos-public:
true` in place of a permission; a `303` response with a `Location` header as the only response of
a public `GET`; the header parameter `IdempotencyKey`; `x-pandokos-lenient-query: true`. It
accepts and ignores `headers` on responses (`Set-Cookie`, `Retry-After`, `Idempotency-Replayed`)
and a second security scheme `session`. Cookies and extra headers are set by `internal/admin`
through the request state, not by generated code.

CLI (API-03): `platform-user list|create|get|update|disable|enable|reset-password|unlock|revoke-sessions`
and `member list|add|get|update|remove|disable|enable|reset-password|unlock|revoke-sessions`.
The eight sign-in and session operations have no command: the CLI authenticates with a key
(ADR 0006), and the coverage test exempts public operations and `session:self`.

### 5.13 Host commands and `init` (D-14)

- `pandokos init --admin-username NAME [--password-out FILE]` creates, in the transaction of the
  bootstrap key, a platform-managed local user with the role `platform-admin` and a generated
  password, printed once or written to a new file of mode 0600. It is refused when a user with a
  platform role already exists. Without the flag `init` behaves as before and prints a hint; the
  username is never defaulted (ADR 0022; O-4).
- `pandokos user reset-password USERNAME [--password-out FILE]` and `pandokos user unlock
  USERNAME` work through the database with the admin role, like `init`: a generated password with
  `must_change`, sessions ended, throttle row removed. They are audited with the actor type
  `host` in the platform log, and in the tenant's log too for a user that a tenant manages.
- With `auth.local.enabled: false` no password signs in, whatever the host command did; the
  emergency entry is then `init --recover` (a key), or turning the switch on (O-5).

### 5.14 Audit, logs, errors (D-15, D-17, D-21)

**Audit actions** added to the vocabulary of ADR 0032. The log is the tenant's for a user that a
tenant manages and for members, the platform's otherwise.

| Action | Written when |
|---|---|
| `user.create`, `user.update`, `user.disable`, `user.enable` | changes of a user (detail: changed members; never a password or a hash) |
| `user.password_reset`, `user.password_change`, `user.unlock`, `user.sessions_revoke` | as named; `password_reset` by the API or the host |
| `user.lock` | an account reached the failure limit (actor `anonymous`) |
| `platform_role.assign`, `platform_role.remove` | platform log; detail `source` |
| `member.add`, `member.update`, `member.remove` | tenant log; detail roles and `source` |
| `auth.sign_in` | a session was created (detail `method`) |
| `auth.failure` with `resource_type: user` | a refused sign-in: `resource_id` the user ID or empty, detail `reason` (`unknown_user`, `wrong_password`, `user_disabled`, `password_expired`, `local_disabled`, and for OIDC `state_mismatch`, `state_unknown`, `provider_error`, `code_rejected`, the reasons of token validation, `nonce_mismatch`, `unmapped_subject`), `method`, `listener: admin` |

The actor of a user's act is `user` with the user ID and name; `key_id` is empty. An idempotent
replay writes no row.

**Problem codes** added (ADR 0020, history line):

| `code` | Status | Raised when |
|---|---|---|
| `too_many_attempts` | 429 | sign-in or password check delayed or locked; too many sign-in transactions (`Retry-After`) |
| `password_change_required` | 403 | the session must change its password first |
| `csrf_rejected` | 403 | the checks of §5.7 failed |
| `sign_in_disabled` | 403 | the sign-in method is switched off, or the origin of the listener is not known |
| `idempotency_key_reused` | 422 | the key was used with another request |
| `idempotency_in_progress` | 409 | the first request with the key is still running (`Retry-After`) |
| `idempotency_response_lost` | 409 | the first request succeeded and its response was not kept |

**Logs** (ADR 0055): the line `authentication failed` keeps listener, request ID, reason and
`audited`; no username, user ID, subject, group or address is logged. The `admin request` line
names a user (`principal_id`) only when it acts on the platform; in a tenant it says
`principal_type: user` and nothing else. No span attribute is added.

### 5.15 Configuration

| Key | Default | Rule |
|---|---|---|
| `auth.public_origin` | empty | §5.7; required with `auth.oidc.enabled`, and with `allow_plaintext` for any sign-in |
| `auth.local.enabled` | `true` | false is refused unless `auth.oidc.enabled` |
| `auth.local.password_min_length` | `12` | 8 to 128; below 12 warns |
| `auth.local.generated_password_ttl` | `72h` | 1 h to 30 d |
| `auth.local.max_failures`, `failure_window`, `lock_duration` | `10`, `15m`, `15m` | 3 to 100; 1 min to 24 h |
| `auth.local.source_max_failures` | `50` | at least `max_failures` |
| `auth.local.argon2.memory_kib`, `iterations`, `parallelism` | `65536`, `3`, `1` | §5.4 |
| `auth.local.max_concurrent_hashes` | `4` | 1 to 64 |
| `auth.session.idle_timeout`, `absolute_lifetime` | `30m`, `12h` | 1 min to 24 h; idle ≤ absolute ≤ 168 h |
| `auth.session.max_per_user` | `10` | 1 to 100 |
| `auth.oidc.enabled` | `false` | true needs `issuer`, `client_id`, `client_secret_file`, `auth.public_origin` |
| `auth.oidc.issuer`, `client_id`, `client_secret_file`, `client_auth` | —, —, —, `basic` | issuer `https`, as a registration's; the secret is read as `secret.Secret` and is at least 16 bytes |
| `auth.oidc.scopes` | `openid profile email` | contains `openid` |
| `auth.oidc.algorithms` | `RS256 ES256` | from the product's ten |
| `auth.oidc.display_name` | `Single sign-on` | shown by `getAuthMethods` |
| `auth.oidc.name_claim`, `email_claim`, `groups_claim` | `name`, `email`, `groups` | claim names |
| `auth.oidc.group_mappings`, `auto_provision` | empty, `false` | §5.9; `auto_provision` without mappings is refused |
| `auth.oidc.private_hosts` | empty | as `tokens.private_hosts` |
| `auth.oidc.post_sign_in_path` | `/` | starts with one `/`, no `//`, no `\`, no control character |
| `listeners.admin.trusted_proxies` | empty | CIDR prefixes |
| `tokens.admin_listener` | `false` | needs `tokens.enabled` |
| `idempotency.retention` | `24h` | 1 h to 7 d |

Warnings at start: local accounts enabled and no user with a platform role exists (hint to
`init --admin-username`); `allow_plaintext` without `auth.public_origin`; OIDC without group
mappings and without `auto_provision` (every user must be linked by hand).

### 5.16 Interfaces

```go
// internal/password
type Params struct{ MemoryKiB, Iterations uint32; Parallelism uint8 }
func New(p Params, maxConcurrent int) (*Hasher, error)
func (h *Hasher) Hash(ctx context.Context, pw secret.Secret) (phc string, err error)
// Verify reports whether pw matches phc, and whether phc was made with other parameters.
func (h *Hasher) Verify(ctx context.Context, phc string, pw secret.Secret) (ok, outdated bool, err error)
func (h *Hasher) Dummy(ctx context.Context, pw secret.Secret) error // the cost of Verify, no result
var ErrBusy = errors.New("password: too many computations")       // -> 503
func Generate() (secret.Secret, error)                             // 24 characters of a-z2-7
func Check(pw secret.Secret, username string, minLength int) (violation string) // "" when acceptable

// internal/session
const CookieName, TokenPrefix = "__Host-pandokos_session", "pds_"
func NewToken() (token secret.Secret, hash [32]byte, err error)
func ParseToken(presented string) (hash [32]byte, ok bool)
type Session struct{ ID, UserID, Method string; CreatedAt, LastUsedAt, ExpiresAt time.Time }
type Store struct{ /* db, now, idle, absolute, maxPerUser */ }
func (s *Store) Create(ctx context.Context, tx store.Tx, userID, method string) (secret.Secret, Session, error)
func (s *Store) Lookup(ctx context.Context, presented string) (Session, account.User, error) // ErrNoSession
func (s *Store) Delete(ctx context.Context, tx store.Tx, id string) error
func (s *Store) DeleteOfUser(ctx context.Context, tx store.Tx, userID, exceptID string) (int64, error)
func (s *Store) Purge(ctx context.Context) error
func Cookie(token secret.Secret) *http.Cookie
func ClearCookie() *http.Cookie

// internal/account
type User struct{ ID, SignIn, DisplayName, Email, State, Username string; TenantID tenant.ID; MustChange bool }
type SignInResult struct{ User User; Token secret.Secret; Session session.Session }
type Refusal struct{ Reason string; UserID string; TenantID tenant.ID; RetryAfter time.Duration } // wraps authz.ErrUnauthenticated, or ErrThrottled
func (s *Service) SignIn(ctx context.Context, username string, pw secret.Secret, source netip.Addr, presented string) (SignInResult, error)
func (s *Service) ChangePassword(ctx context.Context, sess session.Session, current, next secret.Secret, source netip.Addr) (SignInResult, error)
func (s *Service) ResetPassword(ctx context.Context, tx store.Tx, userID string) (secret.Secret, error)
func (s *Service) Unlock(ctx context.Context, tx store.Tx, username string) error
func BootstrapAdmin(ctx context.Context, tx store.Tx, h *password.Hasher, username string, ttl time.Duration) (secret.Secret, string, error)

// internal/oidc
type Provider struct{ /* config, metadata source, key source, HTTP client of the guard */ }
func (p *Provider) Start(ctx context.Context) (redirect string, seed secret.Secret, stateHash [32]byte, err error)
func (p *Provider) Complete(ctx context.Context, seed secret.Secret, query url.Values) (Identity, error) // *Failure for a refusal
type Identity struct{ Subject, Name, Email string; Groups []string }
func Grants(rules []Rule, groups []string) (platformRoles []string, tenantRoles map[string][]string)

// internal/idem
type Claim struct{ /* row ID, attempt, tenant */ }
func (s *Store) Begin(ctx context.Context, scope Scope, operation, path, key string, body []byte) (ctx2 context.Context, replay *Replay, err error)
func Mark(ctx context.Context, tx store.Tx, resourceType, resourceID string) error // called by internal/audit; no claim: nil
func (s *Store) Finish(ctx context.Context, status int, body []byte) error

// internal/authn (exported from the existing validation; behaviour unchanged for tokens)
type JWTCheck struct{ Issuer, Audience, JWKSURI string; Algorithms []string; ClockSkew, MaxLifetime time.Duration; MaxBytes int }
func VerifyJWT(ctx context.Context, keys KeySource, now time.Time, token string, c JWTCheck) (map[string]json.RawMessage, error)

// internal/jwks
type Metadata struct{ Issuer, JWKSURI, AuthorizationEndpoint, TokenEndpoint string }
func (c *Cache) Metadata(ctx context.Context, issuer string) (Metadata, error)

// internal/authz
const KindUser Kind = "user"
func Assignable(p Principal, role string, scope Scope) bool
```

Import rules (T-R54, T-R55): `golang.org/x/crypto/argon2` only in `internal/password`;
`internal/password`, `internal/session`, `internal/account`, `internal/oidc`, `internal/idem` are
imported by `internal/admin` and `cmd/pandokos` only (and by each other as the signatures show),
never by `internal/gateway`, `internal/resolver` or `internal/authn`; go-jose stays in
`internal/authn`, `internal/jwks`, `test/fakeidp`.

### 5.17 Threat notes and the ASVS checklist

OWASP ASVS 4.0.3, chapters V2 (authentication) and V3 (session management), by section; the
reviewer reads this table against the code.

| Section | How it is met | Tests |
|---|---|---|
| V2.1 password security | minimum 12, at least 64 allowed (256), no truncation, any character, no composition rule, no periodic change, change needs the current password | T-768, T-787 |
| V2.1 breached-password check | **not met**: no list is shipped (O-9) | — |
| V2.2 general authenticator | throttling and lock (§5.5); uniform answers; no weak authenticator; no MFA (ADR 0022, stated in the guide) | T-781 to T-785 |
| V2.3 authenticator lifecycle | initial and reset passwords are random, 120 bits, single-purpose (must change) and expire in 72 h | T-769, T-786, T-789 |
| V2.4 credential storage | Argon2id with stored parameters, 16-byte salt, upgrade at sign-in | T-766, T-767 |
| V2.5 credential recovery | no recovery by e-mail or questions; reset by an administrator or the host only; no default account | T-789, T-828, T-829 |
| V2.10 service authentication | the client secret of the relying party is a file, held as `secret.Secret`, never logged | T-812, T-837 |
| V3.1 session management | the token is never in a URL: cookie only | T-837 |
| V3.2 session binding | a new token at every sign-in and password change; 256 random bits; only its hash stored | T-771, T-790, T-792 |
| V3.3 session termination | sign-out, idle and absolute lifetimes, end on password change and disabling, own sessions listed and ended | T-793 to T-797 |
| V3.4 cookie-based session management | `Secure`, `HttpOnly`, `SameSite=Strict`, `__Host-` prefix, `Path=/` | T-772, T-844 |
| V3.5 token-based session management | no stateless session tokens (ADR 0022); bearer tokens are workloads' (ADR 0045) | T-823 |
| V3.7 defences against session exploits | a restricted session until the password is changed; changing a password needs the current one | T-788, T-787 |
| V4.2 / V13.2 anti-forgery | custom header, `Origin`, `Sec-Fetch-Site`, no CORS | T-798 to T-800, T-845 |
| V7 logging | sign-in success and failure audited; no credential in any output | T-803, T-837 |

Residual exposures, stated in the threat model: local accounts have one factor; a session is a
bearer credential until it ends; an operator of the database reads user records; a throttle row is
a digest of a typed username; what a group gave lasts until the next sign-in.

## 6. Isolation harness

- `DefaultSpec`: nine entries in `Global` (eight restricted, `membership_directory` without
  grants), the grant matrix of appendix A1 (every new table: `pandokos_admin` only), fixtures for
  `membership` and `membership_role`, five functions in `SecurityDefiner`.
- T-777: the negative tests on the two tenant-scoped tables; lint reports nothing.
- T-778: the gateway and worker roles are refused on every table and function of migration 0013,
  in any session (IAM-22 as a property of the database).
- T-779: visibility of the global tables per session (platform, managing tenant, another tenant,
  none), and the two guards.
- T-780: the definer functions return only what they are given a name for.
- T-826: the API: every member operation with tenant B's credential on tenant A's path is 404; a
  session of a user without a membership in A is 404 on A; a tenant administrator cannot reset,
  disable or unlock a platform-managed member, nor see a user of another tenant.

## 7. Test infrastructure and CI

- **`test/fakeidp`** gains the authorization code flow: `/authorize` answers at once with a code
  for the user the test configured; `/token` checks client authentication, `redirect_uri`, the
  PKCE verifier and that a code is used once, and returns an ID token whose claims the test
  shapes; faults for each check of §5.8. Keys are made at run time, as today.
- **`test/apitest`** gains a browser-like client: cookie jar, the CSRF header, an `Origin`.
- **Keycloak** (ADR 0047, history line): the realm template gains a confidential client with the
  standard flow and PKCE required, two users with generated passwords, two groups and a group
  membership mapper for the ID token. The X tests drive the provider's login form with an HTTP
  client. **Authentik** is used for the shape of its `groups` claim only (T-842). Hydra is not
  used: it has no people without a login application.
- **Headless Chrome** (`test/browser`, build tag `browser`, `make test-browser`, CI job
  `browser`): the minimal page served by a test reverse proxy on the same origin as the
  administration listener signs in and calls the API; a page of another site cannot. The test
  runs `google-chrome --headless --dump-dom` and needs no new module.
- CI: the `idp` job runs the new X tests; the `browser` job is new; `make check` is unchanged in
  shape. Fuzz targets: `password.FuzzParsePHC`, `oidc.FuzzGroups`, `session.FuzzParseToken`.

## 8. Decisions

| # | Decision | Record |
|---|---|---|
| D-1 | A user is global, with one sign-in method and one managing side (a tenant or the platform), both immutable; a tenant-managed user is a member of its tenant only (§5.2) | 0056 |
| D-2 | `membership` and `membership_role` are tables of their own, not columns of `role_assignment`; platform roles of users in `platform_role_assignment`; users hold `tenant-admin` or `platform-admin` for now (§5.2) | 0056 |
| D-3 | The principal of a session is built per request from the operation's scope and the path's tenant; `Role.Kind` becomes `Role.Scope`; two self permissions for every user (§5.3) | 0056 |
| D-4 | IAM-08 "or users": the ID token's subject maps to a user by a link or a group rule; a bearer token never acts as a user (§5.9, O-1) | 0056 |
| D-5 | Tokens on the administration listener behind `tokens.admin_listener` (default false), service accounts only (§5.10, O-2) | 0045 history |
| D-6 | Argon2id PHC, 64 MiB, 3 passes, 1 lane; upgrade at sign-in; 4 computations at once; no pepper; length 12 to 256 (§5.4) | 0057 |
| D-7 | Generated passwords: 24 base32 characters, shown once, must be changed, valid 72 h; a restricted session until the change (§5.4, §5.3) | 0057 |
| D-8 | Throttling by digests of the typed username and of the source prefix, identical for unknown usernames; delay from the third failure; lock 15 min at 10 and 50; `401` with one body, or `429` before any check (§5.5) | 0057 |
| D-9 | Session token `pds_` + 32 random bytes stored as SHA-256; cookie `__Host-pandokos_session`; idle 30 min, absolute 12 h; 10 per user; read on every request, no cache (§5.6) | 0057 |
| D-10 | Forgery checks: `X-Pandokos-Csrf`, `Origin`, `Sec-Fetch-Site`; no CORS (§5.7) | 0057 |
| D-11 | The expected origin is `auth.public_origin` or derived from the listener; no sign-in on a clear-text listener outside loopback until an HTTPS origin is named (§5.7) | 0057 |
| D-12 | The relying party is written on `internal/jwks` and the exported verifier; one provider; confidential client with PKCE; state, nonce and verifier derived from a seed in a `Lax` cookie; one-use state rows; provider tokens dropped (§5.8) | 0058 |
| D-13 | Federated users are platform-managed, linked by an administrator or created by a group rule; rules are configuration, reconciled at each sign-in; display name and e-mail only (§5.9) | 0058 |
| D-14 | Sign-in endpoints are operations of the document; the generator learns four constructs; `init --admin-username`; two host commands (§5.12, §5.13) | 0028 history, 0057 |
| D-15 | Sign-in events of a tenant-managed user go to its tenant's log, the others to the platform's; a typed username is never recorded; host commands write as `host` (§5.14, O-3) | 0032 history |
| D-16 | Idempotency keys on every 201 operation: claim, commit mark through `internal/audit`, stored response without one-time members, replay (§5.11) | 0059 |
| D-17 | Seven problem codes (§5.14) | 0020 history |
| D-18 | One new module, `golang.org/x/crypto`, for `argon2`, in `internal/password` only; no OpenID Connect or OAuth library | 0018 history |
| D-19 | The binary serves no page; the browser contract is tested behind a same-origin proxy in headless Chrome (O-6) | 0047 history |
| D-20 | The admin role purges sessions, throttle rows, sign-in transactions and idempotency keys every 5 minutes; the worker gets no privilege (§5.6) | 0057 |
| D-21 | Logs and traces name no user, username, subject, group or address; a user is named in the `admin request` line only on the platform (§5.14) | 0055 history |
| D-22 | `auth.local.enabled: false` refuses every password sign-in and the creation of local users, and needs OIDC (§5.13, O-5) | 0057 |

## 9. Tests

Levels: **U** unit; **D** needs the test database; **I** in-process admin handler (with the fake
issuer where named); **E** the built binary; **X** external identity providers (tag `idp`);
**B** headless Chrome (tag `browser`); **R** repository test. IDs continue from T-765 and T-R52.

### New tests

| ID | Test | Level | Proves | Requirement |
|---|---|---|---|---|
| T-766 | `password.TestHashVerify` | U | A hash verifies its password and no other; the PHC string has the configured parameters, a 16-byte salt and a 32-byte output; two hashes of one password differ; `outdated` when any parameter differs; a known-answer vector of RFC 9106 | **IAM-19** |
| T-767 | `password.TestParsePHC`, `password.FuzzParsePHC` | U | Wrong variant, version, order, missing part, padded base64, parameters over the bounds, an empty hash: refused without computing; fuzz: no panic, no computation over the bounds | IAM-19 |
| T-768 | `password.TestPolicy` | U | Length at each boundary in characters and bytes; equal to the username in any letter case; multi-byte characters; nothing truncated (two passwords equal in their first 72 bytes hash differently); the fixed sentences | **IAM-19** |
| T-769 | `password.TestGenerate` | U | 24 characters of the alphabet, 10 000 distinct, read from `crypto/rand` | IAM-19 |
| T-770 | `password.TestConcurrency` | U | With a limit of 2, a third computation waits; a cancelled context returns `ErrBusy` and computes nothing; `Dummy` costs one computation with the configured parameters | IAM-20 |
| T-771 | `session.TestToken`, `session.FuzzParseToken` | U | Prefix, length, alphabet; 10 000 distinct; the hash is SHA-256 of the whole token; malformed values are refused without a lookup | **IAM-21** |
| T-772 | `session.TestCookie` | U | The exact `Set-Cookie` strings for setting and clearing, with and without TLS on the listener | **IAM-21** |
| T-773 | `store.TestUserSchema` | D | Constraints of migration 0013: formats, the username in lower case, the exclusive sign-in method, immutable `sign_in` and `tenant_id`, the PHC check, expiry only with `must_change`, session lifetime bounds, unique token hash, cascade from membership to roles, the directory kept by the trigger | IAM-18 |
| T-774 | `store.TestManagingRule` | D | A tenant-managed user cannot become a member of another tenant nor hold a platform role; a platform-managed user can be a member of two tenants; a federated user cannot be tenant-managed | IAM-10 |
| T-775 | `migrations.TestUpgrade0013` | D | Migration 0013 on a database with data of 0012: existing rows untouched, the audit constraint accepts `host`, the admin role executes the two issuer functions | — |
| T-776 | `store.TestPersonalDataIsMinimal` | D | The columns of the six tables about people are exactly the list of the data model; none holds an address, a user agent or a token | **PRV-01** |
| T-777 | `isolation.TestMembershipTables` | D | The harness's negative tests on `membership` and `membership_role` for every runtime role; lint (T-140) reports nothing with migration 0013 | **TEN-04** |
| T-778 | `isolation.TestPeopleTablesAreAdminOnly` | D | As gateway and as worker, in a tenant's session, in the platform scope where the role has one, and unscoped: every statement on each of the eleven tables and each of the five functions is `permission denied` | **IAM-22** |
| T-779 | `isolation.TestUserVisibility` | D | As admin role: the platform scope sees all users; tenant A's session sees and writes the users A manages, sees its platform-managed members and cannot write them, sees no session, identity, throttle row or platform role of others; tenant B's session and an unscoped session see none of A's | TEN-04 |
| T-780 | `isolation.TestPeopleFunctions` | D | `user_tenants` fails outside the platform scope and lists one user's tenants; `platform_user_lookup` needs a tenant session and exactly one argument, never returns a tenant-managed user and cannot list | TEN-04 |
| T-781 | `account.TestSignIn` | D | Right password: a session, the throttle row gone; the hash is upgraded when the parameters changed; a presented session is deleted; the eleventh session of a user removes the oldest | **IAM-18**, **IAM-21** |
| T-782 | `account.TestSignInUniform` | D | Unknown username, wrong password, disabled user, expired generated password: the same error value, one hash computation each (counted), the same rows written to `signin_throttle` | **IAM-20** |
| T-783 | `account.TestThrottle` | D | With a fake clock: no delay for two failures, 1 s after the third, doubling to 30 s; the tenth locks for the lock duration and the right password is refused until it passes; the window restarts the count; the same for an unknown username; a refused attempt computes no hash | **IAM-20** |
| T-784 | `account.TestSourceThrottle` | D | Fifty failures over many usernames from one `/32` lock the source for every username; another source is unaffected; IPv6 addresses of one `/64` share a row | **IAM-20** |
| T-785 | `admin.TestSourceAddress` | U | The peer address; a trusted proxy with one, several and forged `X-Forwarded-For` entries; an untrusted peer's header is ignored; malformed values fall back to the peer | IAM-20 |
| T-786 | `account.TestGeneratedPasswordExpiry` | D | A generated password signs in before the expiry with `must_change`, and is refused after it like a wrong one | **IAM-19** |
| T-787 | `account.TestChangePassword` | D | Wrong current password is refused and counted; policy violations; success clears the flag and the expiry, deletes the other sessions and replaces the one in use; the old token no longer works | **IAM-19**, **IAM-21** |
| T-788 | `admin.TestMustChangeGate` | D | With a session that must change its password every operation of the document except the three allowed is `403 password_change_required` (the list is derived from `adminapi.Operations`); after the change they work | **IAM-19** |
| T-789 | `admin.TestResetPassword` | D | `resetUserPassword` and `resetMemberPassword`: a new password once, `must_change`, sessions ended, throttle row removed, audit row; refused for a federated user and across the managing rule | **IAM-19** |
| T-790 | `admin.TestSignInEndpoint` | D | The contract of §5.5: body of `Session`, the cookie, `401` bodies byte-identical across the four refusals, `429` with `Retry-After`, `503` when the hasher is busy, `403 sign_in_disabled` with local accounts off; unknown members of the body are reported without echoing them | **IAM-09**, **IAM-20** |
| T-791 | `admin.TestSessionPrincipal` | D | The table of §5.3: platform operation with and without a platform role; tenant operation as member, as non-member (404, identical to an unknown tenant), in a suspended tenant; `getMe` for a user with two memberships; `session:self` refused for a key | **IAM-10**, AZ-01 |
| T-792 | `admin.TestCredentialPrecedence` | D | An `Authorization` header with a session cookie uses the key and ignores the cookie; a bad key with a good cookie is 401; a cookie alone is a session; neither is 401 | IAM-22 |
| T-793 | `admin.TestSessionLifetimes` | D | With a fake clock: idle expiry, absolute expiry although used, `last_used_at` written at most once a minute; an expired session clears the cookie | **IAM-21** |
| T-794 | `admin.TestSignOutAndOwnSessions` | D | Sign-out deletes the session and clears the cookie; the token no longer works; a user lists its sessions and ends another of them, and cannot end one of another user (404) | **IAM-21** |
| T-795 | `admin.TestDisableEndsSessions` | D | Disabling a user, a member, and deleting the membership of a tenant-managed user: the next request with each of its sessions is 401; enabling does not bring a session back | **IAM-11** |
| T-796 | `admin.TestRevokeSessions` | D | `revokeUserSessions` and `revokeMemberSessions` end every session of the user and of no other | **IAM-21** |
| T-797 | `session.TestPurge` | D | Expired and idle sessions, throttle rows older than window + lock, expired sign-in transactions and idempotency keys are removed; live ones stay | IAM-21 |
| T-798 | `admin.TestCSRF` | D | For every operation that is not `GET` with a session: without the header, without `Origin`, with another origin, with `Sec-Fetch-Site: cross-site` or `same-site`: `403 csrf_rejected` and nothing changed; with a key none of this is asked; `GET` with a session needs none | **IAM-21** |
| T-799 | `admin.TestExpectedOrigin` | U | The table of §5.7: configured origin, TLS with the `Host` header, loopback names and a non-loopback `Host` on a loopback listener, plain listener elsewhere; scheme, port and case | IAM-21 |
| T-800 | `admin.TestSignInTransport` | D | A listener without TLS outside loopback and no `auth.public_origin`: the four public operations and a session request answer `403 sign_in_disabled`; with the origin set they work | **IAM-21** |
| T-801 | `admin.TestSecurityHeaders` | D | Every response, problems included, carries the three headers of §5.6 and `Cache-Control: no-store` | — |
| T-802 | `admin.TestUsers`, `admin.TestMembers` | D | Every operation of the two groups: creation of each kind, the rules of §5.12 with their codes, pagination, sorting and filters, role replacement, self-disable and last-role guards, `Assignable` | **IAM-18**, IAM-10 |
| T-803 | `admin.TestPeopleAudit` | D | Each action of §5.14 writes one row in the right log, in the transaction of the change, with actor, resource and detail; no row holds a password, a hash, a token or a typed username | **AUD-01**, AUD-03 |
| T-804 | `admin.TestSignInAudit` | D | Ten wrong passwords: ten `auth.failure` rows and one `user.lock`; an unknown username writes rows without a resource ID; a tenant-managed user's rows are in its tenant's log; beyond the cap failures are logged only and the lock is still audited | **IAM-20**, AUD-01 |
| T-805 | `authz.TestUserPrincipal` | U | `Decide` for `KindUser` over the permission catalogue and the three scopes; roles by scope; the self permissions; keys and tokens decide as before (the 1.3 matrix unchanged) | AZ-01 |
| T-806 | `authz.TestPeoplePermissions` | U | Five permissions with their scopes; `tenant-admin` and `platform-admin` hold theirs; `Assignable` for both roles, for `inference`, for an unknown role and for a principal that lacks one permission; `permissions.md` lists them | AZ-02 |
| T-807 | `oidc.TestStart` | U | The authorization URL has every parameter of §5.8; state, nonce and challenge derive from the seed; two starts share nothing; the cookie string | **IAM-09** |
| T-808 | `oidc.TestCallbackState` | I | No cookie; another flow's cookie; a state used twice; a state older than ten minutes; an `error` parameter; an `iss` parameter that differs: each fails, deletes the cookie, creates no session and redeems no code (the fake issuer received no token request) | **IAM-09** |
| T-809 | `oidc.TestCodeExchange` | I | The token request carries the verifier, the redirect URI and client authentication in both modes; a refused code, an answer over 64 KiB, a redirect, a slow answer, no `id_token`: failure or `unavailable` as §5.8 says | IAM-09 |
| T-810 | `oidc.TestIDToken` | I | Signed by another key; `alg` `none` and HMAC; wrong issuer; audience without the client ID; expired; `nonce` missing or of another flow; several audiences without `azp` and with a foreign `azp`; subject missing or with a control character: each refused with its reason | **IAM-09** |
| T-811 | `oidc.TestMetadata`, `jwks.TestMetadata` | U | Endpoints must be `https` and the document's issuer the configured one; private hosts only when listed; cached and refreshed as keys; unreachable gives `unavailable` | IAM-09 |
| T-812 | `oidc.TestTokensAreDropped` | I | After a sign-in with canary values for code, client secret, ID token, access token and refresh token, none of them is in the database, the audit rows, the log at `debug`, a response body or header | **IAM-09** |
| T-813 | `oidc.TestGroups`, `oidc.FuzzGroups` | U | The claim as array, as one string, at a dotted path, missing, of another type, with 300 entries; rule strings with `=` and `:` inside the group; `Grants` for platform and tenant targets | **IAM-12** |
| T-814 | `admin.TestOidcSignIn` | I | A linked subject signs in and gets the same `Session` and cookie as a local sign-in; display attributes are updated; a disabled user is refused; an unlinked subject without a rule is refused with `unmapped_subject`; the redirect targets of success and failure | **IAM-09**, **IAM-08**, **IAM-10** |
| T-815 | `admin.TestGroupReconciliation` | I | A first sign-in adds memberships, roles and a platform role marked `idp`, each audited in its log; a later sign-in without a group removes exactly those; `manual` rows survive both; an unknown tenant in a rule stops the start-up | **IAM-12** |
| T-816 | `admin.TestAutoProvision` | I | With `auto_provision` an unknown subject with a mapped group becomes a platform-managed federated user; without a mapped group, or with the switch off, it is refused and no row is created | **IAM-08**, IAM-12 |
| T-817 | `admin.TestOidcRateBound` | D | 10 001 live transactions: the next start is `429`; expired ones are purged and it works again | SEC-04 |
| T-818 | `idem.TestClaim` | D | New key; same key and request while locked (`409`); after completion (replay); another body, another operation (`422`); another principal, another tenant (independent); a stale lock is taken over and the old attempt's transaction fails at the commit mark | **API-05** |
| T-819 | `admin.TestIdempotencyEveryCreate` | D | For every operation of the document with status 201: it declares the header parameter; two requests with one key create one resource and return the same status and body, the second with `Idempotency-Replayed: true` and without one-time members; one audit row | **API-05** |
| T-820 | `admin.TestIdempotencyFailures` | D | A request refused with 400, 404 or 409 leaves no row and the key can be used again; a handler that fails after its transaction committed leaves the commit mark and answers `409 idempotency_response_lost` on retry; a key outside the format is `400` | **API-05** |
| T-821 | `admin.TestIdempotencyStoresNoSecret` | D | After `createVirtualKey`, `createPlatformKey`, `createTenant`, `createCredential`, `createUser` and `createMember` with canary secrets, `idempotency_key` holds none of them, nor the request body | API-05, SEC-03 |
| T-822 | `adminapi.TestSubsetAdditions`, `apigen.TestRejectsOutsideTheSubset` | U | The four constructs generate what §5.12 says; a public operation with a permission, a 303 beside a 2xx, the header parameter on a non-201 operation, a lenient query without `x-pandokos-public`: each rejected | API-02 |
| T-823 | `admin.TestTokensOnAdminListener` | I | With the switch on: a token of a tenant-bound and of a platform-level registration acts as its service account on its tenant's operations and is 404 on another's; every refusal reason is the same 401; a failure writes the audit row with `listener: admin`; with the switch off a token is `401`; a token never passes for a session or a user | **IAM-08** (service accounts, unchanged), API-06 |
| T-824 | `authn.TestVerifyJWT` | U | The exported verifier gives the claims of a valid token and each refusal reason; T-569 to T-573 pass unchanged on the rewritten token path | IAM-06 |
| T-825 | `isolation.TestRoutesArePublicOrPermitted` | D | Every operation declares a permission or is one of exactly four public operations; without a credential every non-public operation is 401 | AZ-01 |
| T-826 | `isolation.TestMembersAPI` | D | §6, last item | **TEN-04**, AZ-01 |
| T-827 | `cli.TestPeopleCommands` | U | Every operation of the two groups has a command; flags map to the bodies; a password is printed once and only on standard output | API-03 |
| T-828 | `main.TestInitAdminUser` | D | `init --admin-username`: the account, its role, the password once (standard output or file of mode 0600), `must_change`, the audit row; refused when a platform user exists; without the flag nothing is created and the hint is printed; a failure rolls the key back too | **IAM-14** |
| T-829 | `main.TestUserHostCommands` | D | `user reset-password` and `user unlock`: effect, output, audit rows in the platform log and, for a tenant-managed user, in the tenant's with the actor `host`; unknown username exits 1 without saying more | **IAM-19** |
| T-830 | `config.TestAuth` | U | Defaults, every bound and cross rule of §5.15, the origin syntax, the mapping syntax, the three warnings; the client secret is read as a secret and never printed by `config check` | SEC-04 |
| T-831 | `e2e.TestLocalAccountExit` | E | **Exit.** The binary and PostgreSQL only: `init --admin-username`; sign-in; forced change; the administrator creates a tenant, a platform user and, as tenant administrator through a member it created, a member; the new user must change the generated password and then administers its tenant | **IAM-13**, **IAM-18**, IAM-09 |
| T-832 | `e2e.TestLockExit` | E | **Exit.** Ten wrong passwords: the eleventh attempt is `429` although the password is right; the audit log holds ten failures and the lock; `user unlock` on the host restores sign-in | **IAM-20** |
| T-833 | `e2e.TestSessionNotOnInference` | E | **Exit.** A valid session cookie on an inference route, alone and beside a wrong key: `401` in the family's shape, nothing forwarded; a session token in `Authorization` or `X-Api-Key` is `401` on both listeners | **IAM-22** |
| T-834 | `e2e.TestDisableExit` | E | **Exit.** A signed-in user is disabled by an administrator: its next request is `401`; the same through PgBouncer in transaction mode | **IAM-11** |
| T-835 | `e2e.TestLocalDisabled` | E | With `auth.local.enabled: false` and the fake issuer: password sign-in is refused, OIDC works, a local user cannot be created, `getAuthMethods` says so | **IAM-18** |
| T-836 | `e2e.TestOidcExit` | E | The binary and the fake issuer on a listed private host: start, callback, session, a member administering its tenant; group rules; the provider stopped gives the failure redirect with `unavailable` and existing sessions keep working | **IAM-09**, IAM-12 |
| T-837 | `e2e.TestPeopleCanary`, `admin.TestPeopleCanary` | E, I | For accepted and refused sign-ins of both methods at log level `debug`: no password (typed, generated, current or new), no hash, no session token, no seed, no code, no client secret and no typed username appears in the process output, a response other than the one that shows it once, an audit row, an exported span, an idempotency row or CLI output other than the documented one | SEC-03 |
| T-838 | `e2e.TestTokenAdministers` | E | With `tokens.admin_listener`, a token of the fake issuer creates a virtual key through the administration API with an idempotency key, twice, and one key exists | API-05, API-06 |
| T-839 | `e2e.TestUpgradeWithPeople` | E | A database of the previous release, migrated: keys and tokens work as before on both listeners, `init --admin-username` adds the first user | — |
| T-840 | `idp.TestKeycloakSignIn` | X | **Exit.** A Keycloak user signs in through the provider's form with PKCE; the session is the same as a local one; `getMe` shows the user | **IAM-09** |
| T-841 | `idp.TestKeycloakGroups` | X | The group claim of Keycloak gives a membership with `tenant-admin` and the user administers that tenant; removed from the group, the next sign-in removes the membership | **IAM-12** |
| T-842 | `idp.TestGroupClaimShapes` | X | The group claims of Keycloak (ID token) and Authentik (token with the `profile` scope) at the pinned versions are read by `oidc.Groups` with the default claim name | IAM-12 |
| T-843 | `idp.TestKeycloakUserFlows` | X | **Exit.** The flows of T-831 through Keycloak: a linked administrator signs in and creates a member; a disabled user is refused although Keycloak accepts it; Keycloak stopped: the open session keeps working, a new sign-in fails cleanly | **IAM-09**, **IAM-11** |
| T-844 | `browser.TestMinimalPage` | B | The page on the listener's origin signs in with `fetch`, the browser stores the `__Host-` cookie, the script cannot read it, `getMe` and a `POST` with the header succeed, sign-out ends it | **IAM-21**, API-08 |
| T-845 | `browser.TestCrossSite` | B | A page on another site: a form `POST` and a `fetch` with credentials to a state-changing operation change nothing, with the victim signed in | **IAM-21** |
| T-R53 | `repo.TestDependencies` (changed), `repo.TestArgon2InOnePlace` | R | Exactly one more direct requirement, `golang.org/x/crypto`; only `internal/password` imports it, and only `argon2`; depguard says the same | SEC-06 |
| T-R54 | `repo.TestPeopleImports` | R | The import rules of §5.16; no package under `internal/gateway`, `internal/resolver`, `internal/family` imports a people package | IAM-22 |
| T-R55 | `repo.TestGatewayReadsNoCookie` | R | No file of the gateway plane names `Cookie`, `SetCookie` or `Set-Cookie` | **IAM-22** |
| T-R56 | `repo.TestSignInGuide` | R | `docs/operations/sign-in.md` names every configuration key of §5.15, every code of §5.14, the cookie, the header and the bound of IAM-11; the four records exist and are indexed | — |
| T-R57 | `repo.TestBrowserJobInCI` | R | The `browser` job runs `make test-browser` and fails, never skips, without Chrome | — |
| T-R58 | `repo.TestNoPeopleInTelemetry` | R | No log call or span attribute in `internal/` names `username`, `user_id`, `subject`, `email`, `group` or a source address, outside the `admin request` line's platform branch | ADR 0055 |

### Changed tests of earlier sprints

| Test | Change |
|---|---|
| T-140 (schema lint), `DefaultSpec` | eleven tables, five functions, their grants |
| T-R21 (`TestCryptographyInOnePlace`) | allows `golang.org/x/crypto/argon2` in `internal/password` and one `crypto` line in `go.mod` |
| T-R36 (`TestJOSEImports`) | unchanged list; `internal/oidc` must not import go-jose |
| `isolation.TestEveryRouteDeclaresAPermission` | becomes T-825 |
| the permission matrix, `permissions.md`, `TestTenantAdminIsEnoughAndNoMore` | five permissions; `Role.Scope` |
| the OpenAPI, reference and CLI coverage tests | twenty-seven operations; the exemption of §5.12 |
| the create tests of key-bearing responses | the one-time member is optional in the schema, still present on a first response |
| `authn` token tests | `checkTokens` accepts the admin pool |
| `config` default and key-list tests | the keys of §5.15 |
| `main.TestInit` | the hint line |

### Requirement coverage

| Requirement | Tests |
|---|---|
| IAM-08 (users) | T-814, T-816 |
| IAM-09 | T-790, T-807 to T-812, T-814, T-836, T-840, T-843 |
| IAM-10 | T-774, T-791, T-802, T-814 |
| IAM-11 | T-795, T-834, T-843 |
| IAM-12 | T-813, T-815, T-816, T-841, T-842 |
| IAM-13, IAM-14 | T-831, T-828 |
| IAM-18 | T-781, T-802, T-831, T-835 |
| IAM-19 | T-766 to T-769, T-786 to T-789, T-829 |
| IAM-20 | T-782 to T-785, T-790, T-804, T-832 |
| IAM-21 | T-771, T-772, T-787, T-793 to T-800, T-844, T-845 |
| IAM-22 | T-778, T-792, T-833, T-R54, T-R55 |
| API-05 | T-818 to T-821, T-838 |
| PRV-01 | T-776 |

## 10. Definition of done for this sprint

**Requirements and tests**

- [ ] Every test of §9 exists under the stated name and passes; the changed tests pass; `make
      check`, `make test-idp`, `make test-browser`, `make test-container` and `make test-compose`
      pass.
- [ ] Each of the five exit conditions is demonstrated by its test (T-831 to T-834, T-840, T-843).

**Isolation and authorization**

- [ ] `membership` and `membership_role` have the canonical policy and fixtures; the lint reports
      nothing; the gateway and worker roles hold no privilege on any object of migration 0013.
- [ ] Every new route declares a permission or is one of the four public operations; every member
      and user route has a cross-tenant negative test (T-826).

**Secrets**

- [ ] The canary tests pass (T-812, T-821, T-837): no password, hash, session token, seed, code or
      client secret in any output; `make secrets` passes.
- [ ] No fixture, test or document holds a password or a token: test passwords are generated at
      run time.

**Security review**

- [ ] The table of §5.17 was checked row by row by a reviewer who did not write the code, and the
      review is recorded in the pull request.
- [ ] `govulncheck` reports nothing reachable with `golang.org/x/crypto` at the pinned version.

**Documentation and records**

- [ ] `docs/operations/sign-in.md`; the threat model, the data model, the overview, the API,
      permission and configuration references and the Compose guide are current; the OpenAPI
      document is current and `make generate-check` passes.
- [ ] ADR 0056 to 0059 exist as `proposed` and are indexed; the twelve history lines are written.
- [ ] The epic and the roadmap show the sprint as done; §11 is copied into the epic where it
      changes 4.3.

## 11. Consequences for later sprints

- **4.3 (roles).** The role model it extends is: a role has a scope; a principal is built per
  request; a member's roles are rows of `membership_role`, a service account's of
  `role_assignment`, a platform user's of `platform_role_assignment`. Custom roles are rows that
  all three reference by name, and the deletion guard looks at the three. `authz.Assignable` is
  the seed of the no-escalation rule and becomes "holds every permission of the role" for any
  role. `/v1/me` gains effective permissions per tenant in the `memberships` members that exist
  now. The **support action** is a platform user's principal acting in a tenant where it has no
  membership: §5.3 has one place, the hook, where "no membership → 404" is decided, and that is
  where a recorded support action with a reason grants a bounded principal instead; the audit
  row goes to the tenant's log with the actor `user`. Nothing in this sprint lets a platform
  role read a tenant's users, members or sessions. The permissions asked by issues #31, #35 and
  #52 are catalogue entries like the five added here; reasons are a member of request bodies and
  need nothing from this sprint. The platform auditor is a second platform role for users.
- **6.1 (metrics)**: sign-ins by method and outcome, lock events, sessions open, hash
  computations waiting, idempotent replays; without a user or tenant label.
- **6.2 (audit reading)**: `auth.failure`, `auth.sign_in` and `user.lock` rows of people are in
  the platform log or in a tenant's (D-15); rows written by the host carry the actor `host` in
  both logs.
- **6.3 (tenant deletion, privacy)**: a tenant's deletion removes its memberships, member roles,
  idempotency keys and the users it manages with their accounts and sessions; deleting or
  anonymising a platform-managed user is decided there, since audit rows name user IDs. Users
  are never deleted until then.
- **8.2 (separate processes)**: the admin process holds the hasher (256 MiB with the defaults),
  the purge ticker and a key cache of its own for the identity provider and, with
  `tokens.admin_listener`, for trusted issuers. Each admin instance throttles through the same
  table, so the thresholds hold across instances.
- **12 (operability, Helm)**: the chart sets `auth.public_origin` from the ingress host and
  mounts the client secret; `init --admin-username` is a job.
- **Epic 10 (hardening)**: provider-initiated logout; step-up from `acr` and `auth_time`
  (IAM-17); a breached-password list (O-9); HSTS when the listener serves TLS itself.
- **Later**: tenant-configured identity providers reuse `internal/oidc` with one `Provider` per
  tenant and tenant-managed federated users, which the check `app_user_federated_is_platform`
  forbids today on purpose.

## 12. Risks and open points

### What was verified while designing

- `golang.org/x/crypto` v0.59.0 (published 2026-11-11) is the latest version on the Go module
  proxy (`go list -m -versions golang.org/x/crypto`); its `go.mod` declares `go 1.26.0` and
  requires `golang.org/x/sys` v0.50.0 (the repository has v0.49.0 as an indirect requirement, so
  it moves). A module that imports only `golang.org/x/crypto/argon2` ends with exactly
  `golang.org/x/crypto` direct and `golang.org/x/sys` indirect.
- Argon2id is not in the standard library of Go 1.26 (`go doc crypto/argon2`).
- Cost on the design machine (4 cores, one lane): `m=65536,t=3` 189 ms; `m=65536,t=2` 138 ms;
  `m=47104,t=1` 70 ms; `m=19456,t=2` 47 ms.
- **Migration 0013 as written in the plan was applied after 0001 to 0012 on PostgreSQL 16** and
  exercised as the admin, gateway and worker roles: the visibility of §5.2 in the platform
  scope, in the managing tenant, in another tenant and unscoped; both guards; the exclusive
  sign-in method; the two functions; the cascade and the directory; the audit constraint with
  `host`; `permission denied` for the gateway and worker roles. `sqlc` and the schema lint were
  not run on it.
- Headless Chrome (153) stores a `__Host-` cookie with `Secure; HttpOnly; SameSite=Strict` set by
  a `fetch` to `http://127.0.0.1` and to `http://localhost`, sends it on the next `fetch`, and
  `--headless --dump-dom --virtual-time-budget` prints the page after its script finished.
- The administration authenticator has no principal cache (`serve.go` gives it no lifetime), so
  a session read per request adds one statement and no staleness.
- `test/isolation` lint rule L-09 allows no policy besides `tenant_isolation` on a tenant-scoped
  table, which is one reason for D-2.

**Not verified** (the implementer verifies in the part named, and stops if it does not hold):

- Keycloak 26.8.2: import of users with credentials and groups from the realm file, the group
  membership mapper's claim shape, the `pkce.code.challenge.method` client attribute, and the
  markup of the login form the X tests post to (part D).
- That Authentik's token for a service account carries `groups` with the `profile` scope (part D;
  T-842 is dropped to Keycloak only, with a note in ADR 0047, if it does not).
- That GitHub's `ubuntu-latest` runner has `google-chrome` (part D:
  `google-chrome --version` in the job).
- `sqlc` generation for the new queries; the generator changes of appendix A3; the fixtures of
  the two tenant-scoped tables under the harness (part A).
- That every 201 operation writes a success audit row inside its transaction (T-819 proves it;
  an operation that does not is a finding against AUD-03, not a reason to weaken D-16).
- The size of the binary and the memory of the admin role under load.

### Risks

| # | Risk | Answer |
|---|---|---|
| R-1 | A mistake in password or session code becomes a vulnerability | Small packages with one job each; the ASVS table of §5.17 as the review checklist; canaries; fuzzing of every parser; review by a second pair of eyes (definition of done) |
| R-2 | The principal built per request gives a user a role in the wrong tenant | The membership is read in the tenant's session under row-level security, so a wrong tenant finds no row; one function builds the principal; T-791, T-826 |
| R-3 | A tenant administrator takes over a person who is powerful elsewhere | The managing rule is in the database (two triggers) and in the handlers; T-774, T-789, T-826 |
| R-4 | Throttling locks legitimate users out, or the administrator of the installation | The lock is temporary and per username; the source threshold is five times higher; `trusted_proxies` keeps a proxy from being the one source; `user unlock`, `user reset-password` and `init --recover` on the host |
| R-5 | Argon2id exhausts the memory of a small installation | A bound on concurrent computations, `503` beyond it, parameters configurable down to a floor; the guide states the memory (256 MiB by default) |
| R-6 | A username conflict tells a tenant administrator that a name exists in another tenant | Accepted: ADR 0022 makes usernames unique in the installation. The guide advises e-mail-shaped usernames. Per-tenant usernames would need the tenant at sign-in |
| R-7 | The relying party accepts a forged or replayed answer | State bound to the browser, one-use rows, PKCE, nonce, the verifier that 4.1 tested, fixed redirect; T-808 to T-810 |
| R-8 | Login forms of providers change and the X tests break | Three X tests post a form; the fake issuer covers every check; the image is pinned by digest, so a change arrives as a reviewed update |
| R-9 | Group rules give a role by mistake (a group renamed, a rule too broad) | Exact match only; rules in configuration reviewed by the operator; each grant audited with `source: idp`; removed at the next sign-in |
| R-10 | The idempotency mechanism hides a second, different request | The request hash; `422` on a mismatch; keys scoped by principal and tenant; T-818 |
| R-11 | The generator changes break existing operations | Generated code is committed and compared in CI; T-822 pins the new constructs and their refusals |
| R-12 | The sprint is large (eleven tables, twenty-seven operations, two sign-in methods) | The plan orders C1 to C10 so that local accounts and sessions are complete and reviewable before OIDC, tokens and idempotency start |

### Open points (raised to the maintainer; none blocks the sprint)

| # | Question | Recommendation |
|---|---|---|
| O-1 | 4.1 moved "subject mappings to users" here. Should a bearer token of a trusted issuer be able to act as a **user** on the administration listener (a nullable `user_id` on `subject_mapping`)? | No (D-4). ADR 0006 rejected bearer tokens of people on the administration API, and IAM-22 says only sessions carry people. IAM-08 is met for users by the identity link and the group rules. If wanted later it is one column, one check and an amendment of ADR 0006 |
| O-2 | Tokens on the administration listener: on by default? | Off (`tokens.admin_listener: false`). It turns subject mappings into control-plane access and makes the admin role fetch tenant-supplied addresses; an operator should choose it |
| O-3 | Are sign-in events of a tenant-managed user the tenant's (its audit log) or the platform's? | The tenant's (D-15), as failures of a tenant's keys are; unknown usernames and platform-managed users go to the platform log |
| O-4 | `init`: a mandatory `--admin-username`, a default name, or an optional flag? | Optional flag: no default username (ADR 0022), and existing automation that runs `init` for the key keeps working. The quick-start passes it |
| O-5 | With `auth.local.enabled: false`, should a password reset from the host still let that account in, as ADR 0022's "emergency entry" can be read? | No: off means no password signs in. The emergency entry is `init --recover` or turning the switch on; the record gets a history line that says so |
| O-6 | "Served by the binary or by a reverse proxy": should the binary be able to serve a directory of static files on the administration listener? | Not in this sprint: Pandokos serves no pages (threat model). The contract is tested behind a same-origin proxy in a real browser |
| O-7 | Group rules in the configuration file, or as resources of the API? | Configuration: they are the operator's statement about its identity provider, reviewed with it; no new tables or operations. Resources can follow if tenants get their own providers |
| O-8 | A second real provider for the full browser flow (Authentik), or Keycloak for the flow and Authentik for the claim shape only? | The latter: Authentik's flow executor would need a scripted client of its own; the fake issuer covers the checks |
| O-9 | ASVS asks to check new passwords against breached-password lists. Ship a list? | Not now: it is data with a provenance and an update duty of its own. With a minimum of 12 the common lists shrink to little. Recorded as the one unmet row of §5.17 |
| O-10 | Defaults: Argon2id 64 MiB, 3 passes, 4 at once; lock at 10 failures for 15 min; idle 30 min, absolute 12 h; 10 sessions per user; generated passwords valid 72 h; idempotency 24 h | Confirm |
| O-11 | A replayed create answers without the one-time secret (D-16), instead of an error, so the create schemas make that member optional | Confirm: a retry after a lost response then learns the resource's ID and can revoke it; an error would hide the ID |
| O-12 | The headless Chrome job as a required check | Yes: it is the only test of what browsers do with the cookie. If it proves unstable on the runner it becomes a nightly job, by a later decision |

### Maintainer decisions on the open points (2026-11-21)

| # | Decision |
|---|---|
| O-1 | No: a bearer token never acts as a user on the administration listener (ADR 0006 stands). Subject mappings to users are delivered as the ID-token identity link. Revisit with an ADR amendment if a need appears |
| O-2 | Opt-in: `tokens.admin_listener` defaults to false, service accounts only |
| O-3 | Sign-in events of a tenant-managed user go to that tenant's audit log; others to the platform's |
| O-4 | `init --admin-username` is an optional flag with no default username; the quick-start passes it |
| O-5 | No: with local accounts disabled a host reset does not let the account in; the emergency entry is `init --recover` or turning the switch on. ADR 0022 gets a history line |
| O-6 | The binary serves no static files in 4.2 |
| O-7 | Group rules stay in configuration |
| O-8 | Keycloak for the full browser flow, Authentik for the group-claim shape, the fake issuer for every check |
| O-9 | No breached-password list now; recorded as the one unmet ASVS row |
| O-10 | Defaults confirmed as designed |
| O-11 | Confirmed: a replayed create answers without the one-time secret; those schema members become optional |
| O-12 | Yes, the headless Chrome job is a required check; demoted to nightly only if it proves unstable |
