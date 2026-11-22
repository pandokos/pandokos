# 0061. Support actions and reasons: how the platform acts inside a tenant

- Status: accepted
- Date: 2026-11-22

Refines [ADR 0007](0007-authorization-rbac.md),
[ADR 0034](0034-credential-model-and-platform-oversight.md) and
[ADR 0054](0054-usage-data-is-the-tenants.md).
Requirements AZ-06, AZ-09, SEC-08.

## Context

A platform administrator has no permission in a tenant (ADR 0031) and usage is the tenant's (ADR 0054),
yet a tenant that lost its administrators must be recoverable, and the planned issues #31 (budget
exceptions), #35 (exposure report and containment) and #52 (tenant correlation in telemetry) need the same
ingredients: a reason, a record in the tenant's log, a bounded time, a switch.

## Decision

1. **The support action.** A platform principal with `tenant_support:open` opens an action in one tenant
   with a reason (1 to 500 characters), 1 to 11 permissions from a closed set (the tenant permissions with
   the attribute `Support`) and a bounded time (60 s to `support.max_duration`, default 1 h, at most 24 h
   by constraint). One transaction writes the row and both audit events (the tenant's log and the
   platform's). There is one open action per actor and tenant. The action is honoured at the place where
   the request hook decides that a platform principal has no membership in the tenant, only while
   `support.enabled` is true and the actor still holds `tenant_support:open`; without an action the
   answers are those of sprint 4.2. The tenant lists and reads its actions, and its owner can end one.
   The platform closes any open action of the tenant.
2. **Grants with the tenant's authority.** Under an action the platform may give any tenant role and issue
   a key for any service account, when the operation's permission is in the action: this is the recovery.
   Two limits: a user cannot make itself a member of the tenant under its own action, and no platform
   permission is grantable this way. Every write names the action in the tenant's log
   (`audit_event.support_action_id`).
3. **The residual path, stated.** No permission that reads usage, limit events, prices or credentials
   has the attribute `Support`, and opening refuses them. A platform administrator reaches a tenant's
   data only by making an identity in the tenant for somebody, which needs an open action and leaves rows
   that the tenant reads. `support.enabled: false` removes this path together with the recovery.
   `resetUserPassword` is not a second path: it refuses (`403 escalation_refused`, with or without an
   action) a platform user that holds permissions in a tenant, since the new password would be a
   session of a member; such a user is recovered with the host command `pandokos user reset-password`.
   One residual path is accepted rather than closed: `updatePlatformIssuer` is not guarded, so a
   platform administrator can change what a platform-level issuer trusts (its issuer, key set or claims)
   and act as the service accounts that tenants mapped under it. The platform runs the installation
   (its database, its keys, the host), so the guard would not bound it; the change is in the platform's
   audit log, and a tenant that does not accept it maps its subjects under its own issuer.
4. **The construct `x-pandokos-reason: required`.** The generator demands a required `reason` in the body
   and the generated handler refuses an empty one. Nine operations declare it, by three criteria: the
   platform acts on a tenant (`suspendTenant`, `reactivateTenant`, `disableTenantCredential`,
   `revokeTenantCredential`, `openSupportAction`), the caller obtains another person's credential
   (`resetUserPassword`, `resetMemberPassword`), one call changes what many identities may do
   (`deactivateRole`, `activateRole`). Limit-rule writes take an optional reason. For `suspendTenant` and
   `reactivateTenant` this changes the v0.1 contract, allowed before 1.0 and recorded in the release notes
   of v0.2.0.
5. **The mechanism later work reuses.** The attributes `InTenant` (a platform permission whose operations
   read or change one tenant, whose changes require a reason and are written to the tenant's log) and
   `Support`, `store.ActInTenant` (one transaction that is the platform's and one tenant's) and
   `audit.RecordIntervention` (the same event in both logs), and one configuration switch per capability.
6. **Operational telemetry** (ADR 0055): the request line of a support principal says `support: true` and
   names no tenant, role, reason or action.

## Consequences

- With `support.enabled: false` a tenant without administrators is recovered only from the host's
  database; `serve` warns at start.
- Whether a support action may read a tenant's audit log is decided with `audit:read` in sprint 6.2, by
  giving that permission the attribute `Support` or not.
- Nine operations need a reason: clients and scripts of v0.1 that suspend or reactivate a tenant must send
  one.
- Support actions are never deleted before tenant deletion (sprint 6.3).

## Alternatives considered

**A platform role with tenant permissions.** Against AZ-06.

**A reason header on every request of a platform principal in a tenant.** Reads would need an audit write
each, and nothing bounds the time.

**A dedicated recovery operation only.** AZ-09 names users, roles and keys, not only a key for an owner.

**A support action that can carry any tenant permission.** Usage and secrets would be one request away.

**Making the platform user a temporary member.** It would hold a role, and the tenant's member list would
lie.

**Reasons required on every state change.** Boilerplate reasons devalue the ones that matter.

## History

- 2026-11-22: proposed with sprint 4.3. Maintainer decisions of 2026-11-22: no platform role carries
  `usage:read` (O-2); grants with the tenant's authority (O-3); the nine operations, including the required
  reason on `suspendTenant` and `reactivateTenant` (O-4); `support.enabled` true by default, 1 h, 24 h cap
  (O-7); the owner ends an action (O-8); refused grants are logged, not audited (O-12).
- 2026-11-23: accepted by the maintainer after review.
