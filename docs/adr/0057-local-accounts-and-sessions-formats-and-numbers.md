# 0057. Local accounts and sessions: formats and numbers

- Status: accepted
- Date: 2026-11-22

Refines [ADR 0022](0022-local-accounts-and-sessions.md). Requirements IAM-18 to IAM-22, SEC-02 to
SEC-04, AUD-01 to AUD-03, API-08.

## Context

ADR 0022 decided that Pandokos keeps local accounts and server-side sessions, and left the formats
and numbers open. Sprint 4.2 fixes them, with OWASP ASVS 4.0.3 chapters V2 and V3 as the checklist.

## Decision

1. **D-6. Passwords.** Argon2id, version 19, 16 bytes of salt, 32 bytes of output, one PHC string that
   carries its parameters. Defaults `m=65536` (64 MiB), `t=3`, `p=1` (about 190 ms on the design
   machine); floors refused at start (`m < 19456`, `t < 2`, `p` outside 1 to 4); parameters read from a
   row are bounded before use. A hash made with other parameters is replaced at the next successful
   sign-in. At most `auth.local.max_concurrent_hashes` (4) computations run at once; a request that
   waits more than 2 s gets `503 unavailable` and counts as no attempt. No pepper: it would be a second
   secret to keep. Length from 12 (configurable, 8 to 128) to 256 characters and 1024 bytes, no
   composition rule, any Unicode, never truncated, not the username, a new password differs from the
   current one. Only `internal/password` imports `golang.org/x/crypto/argon2`.
2. **D-7. Generated passwords.** 24 characters of `a-z2-7` from `crypto/rand` (120 bits), shown once,
   never stored or logged; the account must change it and it expires after 72 hours. Until the change
   the session may call three operations (ADR 0056).
3. **D-8. Throttling.** Per account and per source, by digests of what was typed (SHA-256 of the
   lower-cased username, and of the source prefix: `/32` for IPv4, `/64` for IPv6), identical for a
   username that does not exist. No delay for two failures, then 1 s doubling to 30 s; at 10 failures
   the account is locked for 15 minutes, at 50 the source is; the window is 15 minutes. One Argon2id
   computation always runs, against a dummy hash when there is no account. Unknown user, wrong
   password, disabled user and expired generated password give the same `401` with the same body; a
   delay or a lock is `429 too_many_attempts` with `Retry-After` before any password is checked. Each
   failure is an `auth.failure` row, the lock one `user.lock` row; the typed username is never
   recorded.
4. **D-9. Sessions.** Token `pds_` and 32 random bytes in unpadded base64url; only its SHA-256 is
   stored. Cookie `__Host-pandokos_session`, `Secure; HttpOnly; SameSite=Strict; Path=/`, no `Domain`,
   no `Max-Age`; the attributes never vary, loopback included. Idle lifetime 30 minutes, absolute 12
   hours (at most 7 days), at most 10 sessions per user (the oldest is deleted). `last_used_at` is
   written at most once a minute. Every request reads the session and its user in one statement; there
   is no session cache, so the bound of IAM-11 is the request in flight. A session ends by sign-out,
   expiry, password change (the others), reset, disabling, revocation, or removal of the last membership
   of a tenant-managed user. The admin role purges expired rows every 5 minutes (D-20); the worker has
   no privilege on these tables.
5. **D-10. Forgery.** Every sign-in and every request with a session that is not `GET` or `HEAD` needs
   the header `X-Pandokos-Csrf: 1`, an `Origin` equal to the expected origin, and a `Sec-Fetch-Site` of
   `same-origin` when present; otherwise `403 csrf_rejected` before the body is read. The API sends no
   CORS header.
6. **D-11. The expected origin** is `auth.public_origin`, or derived from the listener: `https://` and
   the `Host` header with TLS, `http://` and a loopback `Host` without. A plain-text listener elsewhere
   answers `403 sign_in_disabled` on the sign-in operations and on every session request until the
   operator names the HTTPS origin in front of it.
7. **D-22. `auth.local.enabled: false`** refuses every password sign-in and the creation of local users
   and needs `auth.oidc.enabled`. The emergency entry is `init --recover` or turning the switch on.
8. **Audit and security headers.** The audit vocabulary of ADR 0032 gains the actions of people
   (history line there). Every response of the listener carries `X-Content-Type-Options: nosniff`,
   `Referrer-Policy: no-referrer` and `Content-Security-Policy: default-src 'none'; frame-ancestors
   'none'` besides `Cache-Control: no-store`.
9. **The ASVS table.** Chapters V2, V3, V4.2 and V7 are met as the table in the sprint design lists
   them, with one row **not met**: new passwords are not checked against a list of breached passwords,
   because no list is shipped (O-9). There is no multi-factor authentication (ADR 0022).

## Consequences

- Memory: 256 MiB for the hasher with the defaults, stated in the sign-in guide.
- A known username can be locked out for 15 minutes by anyone who can reach the listener; the host
  commands `user unlock` and `user reset-password`, and `init --recover`, are the ways round.
- A concurrent burst that passed the throttle check together is all checked: up to
  `max_concurrent_hashes` minus one guesses can exceed a threshold.
- A session is a bearer credential until it ends; HSTS is the proxy's duty when TLS ends there.

## Alternatives considered

**bcrypt or scrypt.** bcrypt truncates and is not memory-hard; scrypt is available in `x/crypto` but
Argon2id is the current recommendation.

**A pepper.** A second secret in the admin role, and the vault key belongs to the gateway.

**Server-side sleeping instead of refusals.** It holds a connection and a computation slot for an
attacker.

**A signed double-submit token for forgery protection.** It needs a server secret shared by instances;
the header and `Origin` need none.

**A lifetime in the cookie.** The browser's idea of time is not a control; the server enforces both
lifetimes.

**A session cache.** It would make the bound of IAM-11 a cache lifetime instead of a request.

## History

- 2026-11-22: proposed with sprint 4.2. Maintainer decisions of 2026-11-21: defaults confirmed (O-10);
  `auth.local.enabled: false` lets no password in (O-5); the breached-password list is not built (O-9).
- 2026-11-22: after review of sprint 4.2: the audit row of a tenant-managed user's sign-in, lock,
  password change or reset is written in the tenant's log in a second transaction right after the
  commit of the change (see ADR 0056), so the test of "the same transaction" holds for the platform log
  only. A sign-in that overlaps a password change, a reset or a disable creates no session: the success
  transaction locks the user and its account and requires the hash that was verified and the state
  `active`.
- 2026-11-23: accepted by the maintainer after review.
