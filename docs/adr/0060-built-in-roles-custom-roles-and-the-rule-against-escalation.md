# 0060. Built-in roles, custom roles and the rule against escalation

- Status: accepted
- Date: 2026-11-22

Refines [ADR 0007](0007-authorization-rbac.md), [ADR 0031](0031-authorization-core-and-provisioning.md)
and [ADR 0056](0056-users-memberships-and-sessions-as-principals.md).
Requirements AZ-03 to AZ-06, AZ-10, AZ-11.

## Context

ADR 0007 names roles, custom roles and "no escalation" without giving them contents. Sprint 4.2 left
two roles for people and a stub (`authz.Assignable`) that checked a whole role set against the caller's
own. A tenant needs to give each person and each workload the role it needs, to write roles of its own,
and to be sure that nobody can hand out more than they hold.

## Decision

1. **Eight permissions, and the setting split.** `role:read|write|delete`, `support_action:read|end`,
   `tenant_setting:write`, `tenant_support:open|read`. `tenant_setting:write` takes the tenant's time zone
   and its active currency from `limit:write` and `price_list:write`, so that a principal that writes
   limit rules or prices does not also decide when budgets reset and in which currency they are counted.
   The split is made before the first custom role exists, so that no tenant's role changes meaning.
2. **Ten built-in roles**, defined in code and pinned by the golden file
   `internal/authz/testdata/roles.golden`: `owner`, `tenant-admin`, `credential-manager`, `usage-viewer`,
   `auditor`, `developer`, `inference`, `read-only`, `platform-admin` and `platform-auditor`.
3. **`owner` is every tenant permission by construction**; `tenant-admin` is a reviewed list that differs
   from it by the permissions kept for owners (today `support_action:end`); `read-only` is every `read`
   by construction. `createTenant` gives the first service account the role `owner`, and migration 0014
   adds `owner` to every identity that held `tenant-admin` by an administrator's hand.
4. **Custom roles are rows of a tenant-scoped table** (`tenant_role`) referenced by name from the two
   assignment tables, with tenant permissions only. They are not for the platform. The gateway role reads
   them (`SELECT` and nothing else), so a key's permissions include those of its active custom roles. A
   role is deactivated by state, which gives its holders nothing, and deleted only when nobody holds it,
   checked under a row lock with a trigger as backstop. The names of built-in roles and the prefixes
   `platform-` and `pandokos-` are reserved.
5. **`CanGrant` and a closed table of operations.** A principal gives, or takes over, only permissions it
   holds. The operations that confer power are classified in a test that fails when an operation is
   missing from it. Removal is covered (taking a role needs the permissions of giving it); containment
   (disabling, revoking, ending sessions, deleting a mapping) is free; only the difference between the
   current and the new role set is checked. A refusal is `403 escalation_refused`. A subject mapping is a
   guarded grant and there are no scopes per mapping (this closes open point 5 of sprint 4.1). A new
   password is a grant too: `resetUserPassword` checks the platform roles of the user and its
   effective permissions in every tenant it is a member of, so the platform cannot obtain through the
   API the sign-in of a user that is a member when the password is issued; the host command
   `pandokos user reset-password` remains the recovery. A membership or role gained while a
   platform-issued password (`resetUserPassword` or `createUser`) is still pending, until it is
   changed or expires, is not checked: a known gap, to be closed in sprint 10.2.
   Changing what a tenant's trusted issuer accepts (issuer, audience, key set, algorithms, subject or
   required claims) or enabling it again needs what the service accounts mapped under it can do.
6. **One function builds a member's principal**, for the request hook and for `GET /v1/me`, so that what
   the API reports and what it enforces cannot differ.
7. **The permission reference is generated** by `internal/permdoc` from the catalogue, the roles and the
   API document, and `make generate-check` fails when it is not current.
8. A platform key may hold `platform-auditor`; `createPlatformKey` takes a platform role.

## Consequences

- A new permission must be placed in the golden file on purpose; a new built-in role needs a migration
  that handles a colliding custom role (rename it in each tenant) and a line of the golden file.
- An administrator cannot act on an owner beyond containment: it cannot make one, take the role from one,
  reset an owner's password or issue a key for an owner's account.
- Keys scoped to `limit:write` or `price_list:write` lose the two settings; this and the first owner are
  changes of the v0.1 contract, stated in the release notes of v0.2.
- Removing a departed owner needs another owner or a support action
  ([ADR 0061](0061-support-actions-and-reasons-how-the-platform-acts-inside-a-tenant.md)).
- The gateway's cache must be flushed when a role changes: `tenant_role` sends the tenant's notification.

## Alternatives considered

**`owner` and `tenant-admin` with the same permissions.** The requirement would be met by name only.

**Role rows for built-in roles in the database.** Two sources of truth for what a role holds, and a
migration for every change.

**A foreign key from assignments to a roles table.** Built-in roles would have to be rows in every
tenant.

**Checking the whole role set on assignment instead of the difference.** An administrator could not edit a
member that also holds a role it lacks.

**Scopes per subject mapping.** A second place where a principal's power is defined; a custom role on a
dedicated service account does the same.

**A guard on containment operations.** A holder of `virtual_key:revoke` could not revoke a stronger key in
an incident.

## History

- 2026-11-22: proposed with sprint 4.3. Maintainer decisions of 2026-11-22: `owner` and `tenant-admin` as
  designed (O-1); role contents confirmed (O-6); the split of the two settings (O-5); reserved names (O-9);
  removal covered and containment free (O-10); platform keys may hold `platform-auditor` (O-11); no scopes
  per mapping (O-13).
- 2026-11-23: accepted by the maintainer after review.
- 2026-11-22: sprint 6.1: the platform permission `metrics:read`, held by `platform-admin` and `platform-auditor` ([ADR 0062](0062-metrics-instruments-attributes-cardinality-and-scrape.md)).
