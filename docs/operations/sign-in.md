# Signing people in

People administer Pandokos from a browser client or through their organisation's identity provider.
This page is for the operator who installs it and for the author of a browser client: the two sign-in
methods, the first administrator, who manages an account, passwords, throttling, sessions, how to build
a client, `auth.public_origin` and reverse proxies, the identity provider, tokens on the administration
listener, idempotency keys, and what is not built. It applies from sprint 4.2
([ADR 0056](../adr/0056-users-memberships-and-sessions-as-principals.md),
[ADR 0057](../adr/0057-local-accounts-and-sessions-formats-and-numbers.md),
[ADR 0058](../adr/0058-openid-connect-relying-party.md),
[ADR 0059](../adr/0059-idempotency-keys.md)). Every setting is in the
[configuration reference](configuration.md); the operations are in the
[administration API reference](../reference/admin-api.md).

Automation does not sign in. Scripts and the CLI use administrator keys
([keys](keys.md)); workloads may use tokens of a trusted issuer ([section 11](#11-tokens-on-the-administration-listener)).
A session is accepted by the administration listener only: it never authenticates a call to a model,
and a service account cannot sign in.

## 1. The two methods

| Method | What the person does | Needs |
|---|---|---|
| Local account | `POST /v1/auth/sign-in` with a username and a password | nothing: on by default (`auth.local.enabled`) |
| Identity provider | `GET /v1/auth/oidc/start`: the browser goes to the provider, signs in there and returns to Pandokos | `auth.oidc.enabled` and the settings of [section 9](#9-the-identity-provider) |

`GET /v1/auth/methods` tells a client which are on (and the display name of the provider), without a
credential. Both methods end in the same server-side session, carried by the same cookie; after sign-in
nothing tells the API which method was used except the `method` member of the session. With
`auth.local.enabled: false` (refused unless `auth.oidc.enabled` is true) no password signs in and no
local user can be created, whatever a host command did; the emergency entry is `init --recover` or
turning the switch on again.

## 2. The first administrator

```sh
pandokos init --key-out bootstrap.key --admin-username NAME [--password-out FILE]
```

`init --admin-username` creates, in the transaction of the bootstrap key, a platform-managed local user
with the role `platform-admin` and a **generated password**, printed once (`password: ...`) or written
to a new file of mode 0600. It is refused when a user with a platform role already exists. There is no
default username and no default password: without the flag `init` behaves as before and prints a hint
(`pandokos init --admin-username ...`), and `serve` warns at start while local accounts are on and no
active user holds a platform role.

The first sign-in has the generated password, and the session it opens may do three things only: read
who it is (`GET /v1/me`), change the password and sign out. Everything else is
`403 password_change_required` until the password is changed. The generated password also expires
(`auth.local.generated_password_ttl`, 72 hours): after that it is refused like a wrong one, and the
host command of [section 5](#5-passwords) makes another.

## 3. Who manages an account

A user is managed by the platform or by one tenant, and that never changes. This is the **managing
rule**:

- A platform administrator creates **platform-managed** users (`platform-user create`, `createUser`),
  gives them platform roles, and can make them members of any number of tenants. A federated user
  always is platform-managed, because the identity provider is the platform's.
- A tenant administrator creates **tenant-managed** local accounts for the people of its tenant
  (`member add --new --username NAME --role ROLE`, `createMember` with `account: new`), or makes an
  existing platform-managed user a member (`--user-id ID` or `--username NAME`). A user that a tenant
  manages is a member of that tenant only and holds no platform role.
- Only the administrators of the managing side reset the password of an account, disable it, unlock it
  or end its sessions. For a platform-managed member a tenant administrator gets `409 invalid_state`;
  platform administrators cannot act on a tenant's users until the support action of sprint 4.3 (the
  host commands work on any account).

The reason: without the rule the administrator of tenant A could reset the password of a person who is
also an administrator of tenant B, and sign in as them. A person who works in several tenants is
therefore a platform-managed user, which a tenant can only add as a member and never touch.

A user can hold `tenant-admin` in a tenant or `platform-admin`; whoever assigns a role holds every
permission of it. A user cannot disable itself or remove its own last platform role; a tenant administrator who is a
user cannot disable or remove its own membership either (`409 invalid_state`). Users are never
deleted in this release; disabling ends their sessions at once and enabling does not bring one back.
Usernames are **unique in the installation**, so `409 conflict` on a username tells a tenant
administrator that the name is taken somewhere (possibly in another tenant); choose e-mail-shaped
usernames to avoid collisions. Usernames are lower case.

## 4. What is stored about a person

A user record holds the display name and e-mail address, a local account a username and the hash of its
password, a federated user an issuer and a subject. No address, no user agent, no place of last sign-in.
Sign-in failures are audited without the typed username, because people type passwords into username
fields: a row names the user ID when the account exists and nothing otherwise. For a user that a
tenant manages, the sign-in, lock, password change and reset rows go to the tenant's log, which the
platform scope cannot write: they are written in a second transaction right after the commit of the
change itself, so a crash in between leaves the change without its row in the tenant's log (the host
commands then report it). Everything else is written in the transaction of the change.

## 5. Passwords

- **Storage.** Argon2id (version 19), 16 random bytes of salt, a 32-byte output, one PHC string that
  carries its parameters. Defaults `auth.local.argon2.memory_kib` 65536 (64 MiB),
  `auth.local.argon2.iterations` 3, `auth.local.argon2.parallelism` 1: about 190 ms on a current core.
  When the stored parameters differ from the configured ones the hash is replaced at the next
  successful sign-in. There is no pepper.
- **Memory.** At most `auth.local.max_concurrent_hashes` (4) computations run at once, so **256 MiB**
  with the defaults in the administration role. A request that waits more than 2 seconds gets
  `503 unavailable` with `Retry-After: 1` and counts as no attempt. Lower the memory (floor 19456 KiB)
  or the concurrency for a small installation; the server refuses lower values.
- **Policy.** At least `auth.local.password_min_length` characters (12 by default, 8 to 128; below 12
  warns) and at most 256 characters and 1024 bytes; no composition rule; any Unicode, never truncated;
  not equal to the username; a new password differs from the current one. A password is **not** checked
  against a list of breached passwords (the one unmet row of the ASVS table).
- **Generated passwords** (first password, reset): 24 characters of `a-z2-7`, 120 bits, shown once in the
  response or on standard output, never stored or logged. The account must change it at the next
  sign-in, and it expires after `auth.local.generated_password_ttl`.
- **Change.** `POST /v1/auth/password` takes the current and the new password. A wrong current password
  counts as a failure in the throttle. On success every other session of the user is deleted and the
  session in use gets a new token and a new cookie.
- **Reset.** An administrator of the managing side resets with `platform-user reset-password`,
  `member reset-password` (`resetUserPassword`, `resetMemberPassword`). On the host, with the database
  settings of `serve`:

  ```sh
  pandokos user reset-password USERNAME [--password-out FILE]
  pandokos user unlock USERNAME
  ```

  `user reset-password` makes a generated password with the must-change flag, ends the sessions and
  clears the throttle row; `user unlock` clears the throttle row. Both write an audit row with the actor
  type `host`, in the platform log and, for a tenant-managed user, in the tenant's log too. There is no
  recovery by e-mail and no self-registration.

## 6. Throttling and locks

`auth.local.max_failures` (10) wrong passwords within `auth.local.failure_window` (15 minutes) lock the
account for `auth.local.lock_duration` (15 minutes); while it is locked even the right password is
refused. `auth.local.source_max_failures` (50) failures from one source (the address prefix: `/32` for
IPv4, `/64` for IPv6) lock the source for every username. Before a lock there is a delay: none for two
failures, then 1 second doubling to 30 seconds.

What a client sees:

| Situation | Answer |
|---|---|
| Unknown username, wrong password, disabled user, expired generated password | `401 unauthenticated`, the same body and headers in all four cases |
| Delayed or locked (account or source) | `429 too_many_attempts` with `Retry-After`; no password is checked |
| The hasher is busy | `503 unavailable` with `Retry-After: 1` |
| Local accounts are off, or the origin is unknown ([section 8](#8-auth-public-origin-reverse-proxies-and-hsts)) | `403 sign_in_disabled` |

A row of the throttle exists for a typed username whether or not an account has it, so an unknown
username is delayed and locked exactly like a real one; the row is a digest of what was typed and lives
at most the window plus the lock. Each failure is an `auth.failure` audit row (within the existing cap
of 60 a minute per instance); the lock is one `user.lock` row, written outside the cap. Be aware of two
limits: concurrent attempts that passed the check together are all verified (up to
`max_concurrent_hashes` minus one guesses can exceed a threshold), and anyone who can reach the
listener can lock a **known username** out for the lock duration. The ways round are `pandokos user
unlock`, `pandokos user reset-password`, `init --recover` and the identity provider.

## 7. Sessions

- **Token and cookie.** `pds_` and 32 random bytes in base64url; only its SHA-256 is stored. The cookie
  is `__Host-pandokos_session=...; Path=/; Secure; HttpOnly; SameSite=Strict`, without `Domain` and
  without `Max-Age` (it ends with the browser session). The attributes never vary, loopback included.
- **Lifetimes.** Idle `auth.session.idle_timeout` (30 minutes) and absolute
  `auth.session.absolute_lifetime` (12 hours, at most 7 days), fixed when the session is created.
  `last_used_at` is written at most once a minute, so the idle limit can end a session up to one minute
  early, never late. At most `auth.session.max_per_user` (10) sessions per user: the oldest is deleted.
- **What ends a session.** Sign-out; idle or absolute expiry; a password change (the others) or reset;
  disabling the user; revocation by an administrator (`revokeUserSessions`, `revokeMemberSessions`) or
  by the user (`deleteOwnSession`, `listOwnSessions`); removal of the last membership of a tenant-managed
  user. Ending a session is deleting its row. The admin role purges expired rows every five minutes.
- **The bound of IAM-11.** Every request reads the session and its user from the database in one
  statement; there is no session cache. A disabled user, a deleted session and an expired lifetime are
  therefore effective **at the next request**, on every instance, and a request already running
  completes. A bad or expired cookie answers `401 unauthenticated` and clears the cookie.
- **Replay.** A session is a bearer credential until it ends: whoever holds the cookie value can use it.
  The cookie is not readable by scripts and travels over TLS only, which is why TLS must reach the
  browser ([section 8](#8-auth-public-origin-reverse-proxies-and-hsts)).
- Every response of the listener carries `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`,
  `Referrer-Policy: no-referrer` and `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'`.

Which credential a request carries is decided in order: a public operation (the four sign-in endpoints)
reads none; an `Authorization` header is a key (or a token, [section 11](#11-tokens-on-the-administration-listener))
and a cookie next to it is ignored; otherwise the cookie is a session; otherwise `401`.

## 8. `auth.public_origin`, reverse proxies and HSTS

Pandokos never answers a sign-in on a listener that it knows to be reachable in clear text unless the
operator named the HTTPS origin in front of it. The **expected origin** of the API is:

| Configuration | Expected origin |
|---|---|
| `auth.public_origin` set | exactly that value (`https://host[:port]`, or `http://` with a loopback host) |
| unset, listener with TLS | `https://` and the `Host` header of the request |
| unset, listener without TLS on a loopback address | `http://` and the `Host` header, whose host must be `127.0.0.1`, `[::1]` or `localhost` |
| unset, listener without TLS elsewhere (`listeners.admin.allow_plaintext`) | none: the four public operations and every session request answer `403 sign_in_disabled`, and a warning at start names `auth.public_origin` |

Behind a reverse proxy that ends TLS, set `auth.public_origin` to the URL browsers use, and
`listeners.admin.allow_plaintext` if the hop to Pandokos is in clear text on a private network. The
setting is required with `auth.oidc.enabled`, because it builds the redirect URI. The origin is also
what the `Origin` header of every state-changing browser request must equal.

`listeners.admin.trusted_proxies` lists the CIDR prefixes of the proxies whose `X-Forwarded-For` is
believed: for a peer in the list the source of a sign-in (for the throttle) is the right-most entry
that is not itself trusted; for any other peer the header is ignored. Without the setting every browser
behind the proxy shares the proxy's address and the **source** threshold would lock them all, so set it.

**HSTS is the proxy's duty.** When TLS ends in front of Pandokos the proxy sends
`Strict-Transport-Security`; Pandokos does not send it (it may serve plain HTTP on a private network).
The `__Host-` prefix and `Secure` already make the browser refuse the cookie over plain HTTP, except on
loopback origins, which browsers treat as secure.

Pandokos serves no page and sends no CORS header. The browser client is served by a reverse proxy on
the **same origin** as the administration listener.

## 9. The identity provider

Set `auth.oidc.enabled`, `auth.oidc.issuer` (https), `auth.oidc.client_id`, `auth.oidc.client_secret_file`
(a file of at least 16 bytes: the secret is never in the configuration) and `auth.public_origin`.
Pandokos is a **confidential client** with PKCE.

**Registering the client at the provider.**

| Item | Value |
|---|---|
| Client type | confidential, authorization code flow (standard flow), direct grants off |
| Redirect URI | `auth.public_origin` + `/v1/auth/oidc/callback`, exactly |
| PKCE | S256 (required where the provider lets you require it) |
| Client authentication | `client_secret_basic` (`auth.oidc.client_auth: basic`) or the secret in the body (`post`) |
| Scopes | `openid` and what the claims below need (`auth.oidc.scopes`, default `openid profile email`) |
| ID token signature | one of `auth.oidc.algorithms` (default `RS256 ES256`) |

**Claims.** The display name comes from `auth.oidc.name_claim` (`name`), the e-mail address from
`auth.oidc.email_claim` (`email`), the groups from `auth.oidc.groups_claim` (`groups`): an array of
strings or one string, or a dotted path of at most four object members such as `realm_access.roles`; any
other shape gives no group. At most 256 groups are read. Nothing else of the token is kept; the access,
ID and refresh tokens are dropped at once. `auth.oidc.display_name` (`Single sign-on`) is the label that
`getAuthMethods` shows. `auth.oidc.post_sign_in_path` (`/`) is where the browser goes afterwards: a
path of the same origin fixed by configuration, since a request cannot name its destination. A failure
returns to it with `?sign_in_error=sign_in_failed`, or `unavailable` when the provider or the database
could not be reached. `auth.oidc.private_hosts` lists hosts of the provider that may resolve to private
addresses, as `tokens.private_hosts`; the requests go through the egress guard with no redirect, 10
seconds and a 64 KiB answer.

**Who may sign in.** A subject that the provider asserts maps to a user in one of two ways, and any other
is refused (`unmapped_subject`, audited):

1. **A link made by an administrator**: `platform-user create --sign-in oidc --subject SUBJECT` (the
   issuer is always the configured one).
2. **A group rule**, with `auth.oidc.auto_provision: true`: at the first sign-in of an unknown subject
   the user is created if the rules give it at least one role. `auto_provision` without
   `auth.oidc.group_mappings` is refused; without both, every person must be linked by hand (a warning
   at start).

**Group rules.** `auth.oidc.group_mappings` is a list of `<target>=<group>`; the target is
`platform:<role>` or `tenant:<tenant name>:<role>`, the group is everything after the first `=` and is
compared exactly. A user can hold `platform-admin` and `tenant-admin` through rules. In the environment
the rules are separated by commas. At each sign-in the rules are **reconciled**: rows that a rule gave
(`source: idp`) and the groups no longer justify are removed, missing ones are added, each with its audit
row; rows an administrator made are never touched. A rule that names a role a user cannot hold is refused
at start, and so is one that names a tenant that does not exist once tenants exist. While no tenant
exists yet, such a rule is accepted; at a sign-in a rule whose tenant is not there (not yet created, or
deleted since) is **skipped** with a warning that gives its position in the list and nothing else, and
the other rules apply. A groups claim that is there with a shape that gives no group (an object, a
number, an array with other values than strings) is logged as a warning that names the shape only and
never the claim; the user then has no group, and reconciliation removes the roles that rules gave.

Keycloak, with the group membership mapper (claim name `groups`, full path off, in the ID token) and the
groups `pandokos-admins` and `tenant-a-admins`:

```yaml
auth:
  public_origin: https://admin.example.org
  oidc:
    enabled: true
    issuer: https://keycloak.example.org/realms/pandokos
    client_id: pandokos-admin
    client_secret_file: /run/secrets/oidc_client_secret
    auto_provision: true
    group_mappings:
      - platform:platform-admin=pandokos-admins
      - tenant:tenant-a:tenant-admin=tenant-a-admins
```

A provider with a nested claim, such as the realm roles of a default Keycloak client scope or the
groups of a provider that nests them (`realm_access.roles`):

```yaml
auth:
  oidc:
    groups_claim: realm_access.roles
    group_mappings:
      - platform:platform-admin=pandokos-operators
      - tenant:payments:tenant-admin=payments-leads
```

**When the provider removes a person.** Removing or disabling the person at the provider stops new
sign-ins at once, but a session already open lasts until its idle or absolute lifetime, and what a group
gave lasts until the user's next sign-in. To cut access immediately, disable the user in Pandokos
(`platform-user disable`): that ends every session at the next request, whatever the provider says.
There is no provider-initiated or back-channel logout and no refresh token.

Failures are audited as `auth.failure` with `method: oidc` and a reason (`state_mismatch`,
`state_unknown`, `provider_error`, `code_rejected`, `nonce_mismatch`, `unmapped_subject`, or a reason of
the token validation); the subject is recorded only after the token was validated, in the audit row of
the refusals that follow (`unmapped_subject`, `user_disabled`) and never in the operational log.
Local sign-in switched off (`auth.local.enabled: false`) is audited as `auth.failure` with the reason
`local_disabled`, within the same cap and without a user or a username.

## 10. Building a browser client

The contract is tested in headless Chrome with a minimal page (`test/browser/page/index.html`), which is
the example to copy: it signs in, reads `/v1/me`, creates a user, signs out, and uses `fetch` only.

1. **Same origin.** Serve the client from the origin of `auth.public_origin` (a reverse proxy sends the
   page to your files and `/v1/` to Pandokos). A page of another site cannot call the API with the
   cookie: `SameSite=Strict`, no CORS header, and the checks below.
2. **Every sign-in and every request that is not `GET` or `HEAD` and carries the cookie** needs:
   the header `X-Pandokos-Csrf: 1`; an `Origin` header equal to the expected origin (browsers send it
   on `fetch` with a body); and, when the browser sends it, `Sec-Fetch-Site: same-origin`. A failure is
   `403 csrf_rejected` before the body is read. Requests with an `Authorization` header need none of
   this. `GET` operations change nothing and need none of it.
3. **Sign in**: `POST /v1/auth/sign-in` with `{"username": "...", "password": "..."}`, `credentials:
   "same-origin"`. The answer is the session (`user`, `method`, `must_change_password`, `created_at`,
   `expires_at`, `idle_timeout_seconds`); the cookie is set by the response and cannot be read by the
   script. If `must_change_password` is true, show the change-password form (`POST /v1/auth/password`)
   before anything else. For the identity provider, navigate (do not `fetch`) to `/v1/auth/oidc/start`.
4. **Codes to handle.** `401 unauthenticated` (sign in again: also for an expired session), `403
   password_change_required` (change the password), `403 csrf_rejected` (the client sent a wrong header
   or origin: a bug), `403 sign_in_disabled` (the method is off, or the origin is not set),
   `429 too_many_attempts` (wait `Retry-After` seconds), `503 unavailable` (retry later).
5. **What `/v1/me` tells you**: the user, `must_change_password`, platform roles and permissions, the
   session, and `memberships` (each tenant with the role names). Use it to offer only what is allowed;
   it is a convenience, the API authorizes every call.
6. **What the client must not do**: keep a credential in `localStorage` or in the page (there is none to
   keep: the cookie is not readable); send the cookie from another origin; put a password in a URL;
   cache API answers (they say `no-store`); rely on hiding a control as a protection.

## 11. Tokens on the administration listener

A workload with a token of a trusted issuer may administer its tenant, when the operator sets
`tokens.admin_listener: true` (default `false`; it needs `tokens.enabled`). The selection, validation and
mapping are those of the inference listener ([tokens](tokens.md)), with no principal cache; the
principal is the mapped **service account** with all its roles, so a mapping to an account with
`tenant-admin` administers the tenant. A token never yields a platform principal or a user. The switch is
off by default because every subject that maps to an administrative service account becomes a way into the
control plane, and the administration role fetches the key sets of registered issuers, which tenant
administrators supply: decide it deliberately. Failures are audited with `listener: admin`.

## 12. Idempotency keys

Every operation that answers `201 Created` accepts an `Idempotency-Key` header (1 to 128 printable ASCII
characters). A retry with the same key, the same operation and the same request never creates a second
resource:

- the first request's response is stored and replayed with `Idempotency-Replayed: true`, **without** the
  text members that are shown once (a member named `key`, `secret`, `password`, `initial_password`, or
  with another name that the audit log treats as sensitive): nothing else is removed, so the body is
  the first one minus those members, and a retry after a lost response learns the ID of the resource
  and can revoke or reset it;
- the same key with another request: `422 idempotency_key_reused`;
- the first request still running: `409 idempotency_in_progress` with `Retry-After`;
- the first request succeeded but its response was not kept (the process stopped in between):
  `409 idempotency_response_lost`; the resource exists and its ID is in the audit log.

A key is scoped to the tenant of the path (or the platform) and the caller; a request body is stored as a
hash, never as text. Keys are kept for `idempotency.retention` (24 hours, 1 hour to 7 days), then can be
used again. A replay writes no audit row.

## 13. What is not built

Multi-factor authentication, recovery by e-mail, self-registration, password history, social login
(all excluded by [ADR 0022](../adr/0022-local-accounts-and-sessions.md)); a check of passwords against
breached-password lists; provider-initiated logout, refresh tokens and step-up authentication; more than
one identity provider, or one per tenant; a bearer token that acts as a user; deleting a user; a page
served by the binary; sessions in the CLI (it uses keys).

**Recommendation for production.** A local account has one factor and the throttle can be used to lock a
known username out. Prefer an identity provider that enforces your multi-factor policy, or keep the
administration listener on a private network (and use the host commands for emergencies), and do not
expose it to the public internet.

## 14. Troubleshooting

| Symptom | Likely cause |
|---|---|
| `403 sign_in_disabled` on every sign-in | no known HTTPS origin: set `auth.public_origin` (a listener without TLS outside loopback); or `auth.local.enabled` is false |
| `403 csrf_rejected` from a page | missing `X-Pandokos-Csrf: 1`, `Origin` not equal to `auth.public_origin`, or a cross-site request |
| Sign-in works but the next request is `401` | the cookie was not kept: the page is not on the origin of `auth.public_origin`, or it is served over plain HTTP outside loopback |
| `429 too_many_attempts` for the right password | the account or the source is locked; wait, or `pandokos user unlock USERNAME`; check `listeners.admin.trusted_proxies` |
| Everybody behind a proxy locked at once | the proxy's address is the source: set `listeners.admin.trusted_proxies` |
| `503 unavailable` at sign-in under load | the hasher is busy: raise `auth.local.max_concurrent_hashes` if memory allows |
| `?sign_in_error=sign_in_failed` after the provider | check the redirect URI at the provider, the client secret, the algorithms, and the audit log for the reason (`unmapped_subject`: link the subject or add a group rule) |
| `password_change_required` | the session is the first of a generated password: change it |

**Memory of the hasher.** `auth.local.max_concurrent_hashes` times `auth.local.argon2.memory_kib`: 4 times
64 MiB with the defaults. Count it in the memory of the administration role.
