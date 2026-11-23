# Epic 4 — Identity federation and RBAC: record

Sprint 4.1 completed with release v0.1.0 (2026-11-16); sprints 4.2 and 4.3 completed 2026-11-22, which
closes the epic. Epic file as closed: [epic.md](epic.md).

## Outcome

Workloads call with tokens from their own identity provider; people sign in with a local account or
through an external OpenID Connect provider and obtain a session; the full role model is in place and
the API tells a user what they are allowed to do, so that an interface built on it can offer only that.

Sprint 4.1 registered trusted token issuers, at platform level with an explicit tenant list or inside a
tenant. A token of a registered issuer is verified offline against keys fetched through the egress
guard and cached with bounds, mapped to a service account, and from there is the same principal as a
virtual key. Every refusal is `401`; keys that cannot be obtained are `503`.

Sprint 4.2 added people. Users have memberships in several tenants; a local account has an Argon2id
password, throttling, a lock and a forced first change; a session is a server-side record carried by a
cookie that scripts cannot read, with checks against forgery, and is accepted only on the
administration listener. Sign-in through an OpenID Connect provider maps groups to roles by rules in
the configuration. Tokens of trusted issuers can opt in on the administration listener, and create
operations take idempotency keys.

Sprint 4.3 completed the role model. There are ten built-in roles pinned by a golden file, custom roles
that a tenant writes itself, and a rule that nobody hands out a permission they do not hold. A platform
administrator acts inside a tenant only through a support action with a stated reason, recorded in the
tenant's log and bounded in time, which is also how a tenant that lost its administrators is recovered.
Nine operations require a reason. `GET /v1/me` reports the effective permissions per tenant. No
platform permission reads usage, limit events or price lists, even under a support action.

## Sprints

| Sprint | Design and plan | Delivered | Review |
|---|---|---|---|
| 4.1 Trusted token issuers | [sprint](4.1-trusted-token-issuers/sprint.md), [plan](4.1-trusted-token-issuers/plan.md) | [#18](https://github.com/pandokos/pandokos/pull/18), merged as 61f4d2e; tests against Authentik in [#21](https://github.com/pandokos/pandokos/pull/21) | Review rounds were not recorded in the design or the history |
| 4.2 Users, local accounts and sessions | [sprint](4.2-users-local-accounts-sessions/sprint.md), [plan](4.2-users-local-accounts-sessions/plan.md) | [#54](https://github.com/pandokos/pandokos/pull/54), merged as f7d8eff | 2 rounds: round 1, 2 major and 5 minor; round 2, none |
| 4.3 Roles | [sprint](4.3-roles/sprint.md), [plan](4.3-roles/plan.md) | [#55](https://github.com/pandokos/pandokos/pull/55) | 4 rounds: round 1, 3 major and 5 minor; round 2, 2 minor and 1 nit; round 3, 1 minor; round 4, none |

Each sprint followed the cycle of the earlier epics: a frozen design, tests committed failing before
the implementation, a review against the design repeated until it reported no findings, a pull
request with green CI. Open points raised by each design and the maintainer's decisions on them are
at the end of section 12 of each `sprint.md`; for sprint 4.3 the decisions on the review findings are
there too.

## Decisions recorded

ADR 0045–0047 (sprint 4.1: trusted token issuers, issuer key retrieval and caching, identity providers
in the test suite), ADR 0056–0059 (sprint 4.2: users, memberships and sessions as principals; formats
and numbers of local accounts and sessions; the OpenID Connect relying party; idempotency keys) and
ADR 0060–0061 (sprint 4.3: built-in roles, custom roles and the rule against escalation; support
actions and reasons). Those of 4.2 and 4.3 were accepted by the maintainer on 2026-11-23, with history
lines on earlier records (among them 0005, 0006, 0018, 0020, 0022, 0026, 0027, 0028, 0031, 0032 and
0055). See the [ADR index](../../adr/README.md).

## Carried forward

- Sprint 6.2 (audit reading): `audit:read` is one tenant permission held by `owner`, `tenant-admin`
  and `auditor`; whether a support action may carry it is decided there through the attribute
  `Support`. Refused grants and refused support attempts are logged, not audited, and are revisited
  there as events. The read API can filter by `support_action_id`. `auth.failure`, `auth.sign_in` and
  `user.lock` rows of people are in the platform log or a tenant's; rows written by the host carry the
  actor `host`.
- Sprint 10.2 (hardening): a membership or role gained while a platform-issued password is still
  pending (until changed or expired) is not checked against the caller's permissions; close it by
  refusing or invalidating a pending platform-issued password when a membership or role is added.
  Also: provider-initiated logout, step-up from `acr` and `auth_time` (IAM-17), a breached-password
  list, HSTS when the listener serves TLS itself.
- Issues #31 (budget exceptions), #35 (exposure report and containment) and #52 (tenant correlation in
  telemetry) are ready on the general mechanism: each is a permission placed in the catalogue, with a
  required reason and, for the platform ones, `InTenant` and an entry in the tenant's log.
- Sprint 6.1 (metrics): sign-ins by method and outcome, lock events, sessions open, hash computations
  waiting, idempotent replays, support actions opened, open and ended by cause, refused grants, token
  authentications by reason, key fetches by class and the staleness of each source; no user or tenant
  label.
- Sprint 6.3 (tenant deletion and privacy): removes a tenant's memberships, custom roles, support
  actions, idempotency keys, registrations and mappings, and the users it manages; deleting or
  anonymising a platform-managed user and the actor of a support action are decided there.
- Sprint 8.2 (separate processes): the admin process holds the password hasher, the purge ticker and
  a key cache; the gateway reads `tenant_role` and needs the notification of its changes; only the
  gateway holds `internal/jwks`.
- Sprint 12 (operability, Helm): `auth.public_origin` from the ingress host, the client secret,
  `support.enabled`, `support.max_duration`, `init --admin-username` as a job.
- Later: tenant-configured identity providers, mutual TLS (IAM-15), a new built-in role or permission
  (the steps are in section 11 of the design of 4.3).
- Breaking changes for the release notes of v0.2.0: `suspendTenant` and `reactivateTenant` require a
  reason; keys scoped to `limit:write` or `price_list:write` no longer set the tenant settings, which
  need `tenant_setting:write`; the first service account of a tenant has the role `owner`, and
  migration 0014 gives `owner` to every holder of `tenant-admin`.

## Maintainer actions

- Review ADR 0056 to 0061 (done on 2026-11-23, see below).
- Sprint 4.2 adds one dependency, `golang.org/x/crypto` (for `argon2`, imported only by
  `internal/password`); check `govulncheck` at the pinned version.
- Sprint 4.2 adds the CI job `browser` (headless Chrome, `make test-browser`); it must be a required
  check to protect the browser contract.
- The gitleaks allowlist gained the package list of the people imports test, a false positive of the
  generic rule.
- The race test runs of the root module and of CI may take thirty minutes (`-timeout 30m`).
- Migrations `0013` (people, sessions and idempotency) and `0014` (custom roles and support actions,
  and the owner role) are applied by `pandokos migrate` before the new binary starts.
- New configuration keys, all optional ([configuration reference](../../operations/configuration.md)):
  `auth.*` (local accounts, sessions, public origin, OpenID Connect and group rules),
  `tokens.admin_listener`, `support.enabled` (true) and `support.max_duration` (`1h`, at most 24 h).
  Set `auth.public_origin` when the listener has no TLS outside loopback.
- Write the release notes of v0.2.0 with the three breaking changes above and the owner migration; the
  guides are [signing people in](../../operations/sign-in.md) and
  [roles](../../operations/roles.md).
- Browser, identity provider and container tests run only in CI, not in the sandbox of the
  implementation.

### Done after the close (2026-11-23)

- ADR 0056 to 0061 accepted by the maintainer, with ADR 0053 to 0055 left proposed by epic 5.
- The two residual exposures accepted: a platform-level issuer is not guarded; a membership or role
  gained while a platform-issued password is pending is not checked until sprint 10.2.
- The maintainer ran the container and Compose tests, the quick start, the sign-in flows, roles and
  support actions, and the upgrade of a v0.1 installation by hand. The run found that the role and
  support commands were not routed by the binary, and that the Compose test copied a local `secrets`
  directory; both fixed in [#56](https://github.com/pandokos/pandokos/pull/56), with a test of the
  routing of every client command.
- The draft of the v0.2.0 page became the release page [`docs/releases/v0.2.0.md`](../../releases/v0.2.0.md).
- Open: make the CI job `browser` a required check of `main`.
