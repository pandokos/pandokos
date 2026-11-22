---
id: SPRINT-4.3
title: Roles
epic: EPIC-4
release: v0.2
status: in-progress
design: draft 2026-11-22
---

# Sprint 4.3 — Roles

- Epic: [4 — Identity federation and RBAC](../epic.md)
- Implementation plan: [plan.md](plan.md)
- Previous sprint of the epic: [4.2 — Users, local accounts and sessions](../4.2-users-local-accounts-sessions/sprint.md)
- Risk class: **high** (the sprint decides who may do what: ten built-in roles, roles that tenants
  write themselves and that the gateway must honour, the rule that stops a principal from handing out
  more than it holds, and the one door through which the platform acts inside a tenant)
- Decisions implemented (in [docs/adr](../../../../docs/adr/README.md)): 0007 and 0031 (catalogue,
  roles, one decision function; eight permissions, seven roles); 0056 (the principal of a session;
  its "no membership" branch gains the support action); 0030 (the principal cache now holds the
  permissions of custom roles); 0017 and 0028 (sixteen operations; one construct); 0020 (three
  codes); 0026 and 0027 (migration 0014: two tables, no definer function; a transaction that is the
  platform's and one tenant's); 0029; 0032 (eight audit actions, one column); 0034 and 0054 (what
  the platform never reads); 0055
- New decision records written by this sprint: **0060** (built-in roles, custom roles and the rule
  against escalation; refines 0007, 0031, 0056), **0061** (support actions and reasons: how the
  platform acts inside a tenant; refines 0007, 0034, 0054); history lines on 0007, 0020, 0027, 0028,
  0030, 0031, 0032, 0034, 0045, 0052, 0053, 0054, 0055, 0056. Text in [plan.md](plan.md), part E.

This document is the design. The implementer follows [plan.md](plan.md); a gap in either document
is raised as a question, not filled silently ([planning/README.md](../../../README.md)).

Items carried into this sprint, and where each is decided:

| Item | From | Decided in |
|---|---|---|
| The other built-in roles (AZ-03 R2); platform auditor (AZ-06) | 1.3 §2, epic | §5.2, D-2, D-3, O-1, O-6 |
| Custom roles, deactivation, deletion guard (AZ-04, AZ-05) | 1.3 §2, 5.2 §2 | §5.3, D-4, D-5 |
| The no-escalation rule as a general mechanism | 1.3 §2, 4.2 §11 (`authz.Assignable`) | §5.4, D-6, D-7 |
| Support actions with a reason (AZ-09); recovery of a tenant that lost every administrator key | 1.3 O-2, epic-01 history | §5.6, §5.7, D-8 to D-11, O-2, O-3 |
| *Credential manager* from the five credential permissions (AZ-10) | 2.1 §11, epic-02 history | §5.2: four of the five (O-6) |
| The six limit permissions available to custom roles; a "limits manager" | 5.2 §11, epic-05 history | §5.3: every tenant permission is available; a limits manager is a custom role of the guide, not a built-in |
| `limit:write` sets the time zone, `price_list:write` the active currency: "4.3 can split it" | 5.3 O-4, 5.4 O-7 | §5.1, D-1: split into `tenant_setting:write` (O-5) |
| A custom role may hold `price_list:read` alone; no platform usage viewer | 5.4 §11 | §5.2, §5.6, T-R62 |
| Scopes per subject mapping, "revisit with custom roles" | 4.1 O-5 | not built: D-7 (a mapping is a grant and is guarded; a custom role on a dedicated service account restricts it) |
| Effective permissions per tenant in `/v1/me` (AZ-11) | 4.2 §5.3, §11 | §5.5 |
| Custom roles are rows that "all three" assignment tables reference | 4.2 §11 | departure: two, not three (§5.3, D-4) |
| A platform administrator acts on a tenant's users | 4.2 §5.2, ADR 0056 | §5.6: the member operations under a support action |
| The catalogue, the reasons and the support action extensible to issues #31, #35, #52 | epic, 4.3 note | §5.8, D-12, §11 |

## 1. Outcome

A tenant gives each person and each workload exactly the role it needs, and the platform cannot
step into a tenant without leaving a record there:

1. Pandokos ships eight tenant roles (`owner`, `tenant-admin`, `credential-manager`,
   `usage-viewer`, `auditor`, `developer`, `inference`, `read-only`) and two platform roles
   (`platform-admin`, `platform-auditor`). They are defined in code and cannot be changed.
2. A tenant defines its own roles from the permission catalogue, gives them to members and to
   service accounts, deactivates them, and deletes them once nobody holds them. A key of a service
   account with a custom role is honoured on the inference listener too.
3. Nobody hands out more than they hold: assigning a role, defining a role, issuing a key, mapping
   a token subject and resetting a password are refused when they would give somebody a
   permission the caller lacks (`403 escalation_refused`).
4. `GET /v1/me` tells a signed-in person who they are, the tenants they belong to and the exact
   permissions they hold in each; the same permissions decide every request.
5. A platform administrator has no permission inside a tenant. To act on a tenant's identities,
   roles or keys it opens a **support action**: an explicit platform permission, a mandatory
   reason, a bounded list of permissions and a bounded time, recorded in the tenant's audit log
   and visible to the tenant, whose owner can end it. `support.enabled: false` removes the door.
   A support action never carries a permission that reads usage, costs, prices or secrets.
6. Nine high-impact operations refuse to run without a reason.
7. The permission reference of the documentation is generated from the catalogue, the roles and
   the API document.

**Exit** (epic): for each built-in role a test matrix shows which routes are allowed and denied
(T-867), and `/v1/me` reports exactly the permissions the matrix expects (T-868, T-890); a tenant
admin cannot grant a permission it lacks (T-863, T-890); a platform admin cannot read a tenant's
usage or audit without a recorded support action (T-872, T-891; the precise reading of this
condition is O-2).

## 2. Scope

### In scope

- `internal/authz`: eight permissions, two attributes of a permission, seven roles, permissions
  that come from data (`Principal.Granted`), the support principal, `CanGrant` in place of
  `Assignable`.
- Migration `0014_roles_support`: `tenant_role` (tenant-scoped), `support_action` (global with
  policies), `audit_event.support_action_id`, two indexes, the role `owner` for the identities
  that held `tenant-admin`.
- Administration API, specification first: the catalogue and the built-in roles, custom roles,
  support actions on the platform side and on the tenant side (16 operations); `/v1/me`; the
  construct `x-pandokos-reason`; nine operations that require a reason; `createPlatformKey` takes a
  role. CLI commands for all of it.
- The grant guard on every operation that confers power (§5.4), the support principal in the
  hook (§5.6), the transaction `store.ActInTenant`.
- The gateway side: the three principal queries return the permissions of custom roles; the cache
  is flushed when a role changes.
- The generator of `docs/reference/permissions.md` (`internal/permdoc`).
- Tests: units, database, isolation harness, in-process, end to end, one test with Keycloak.
- Documentation: a guide to roles and support actions, the threat model, the data model, the API,
  permission and configuration references, two decision records.

### Not in scope

| Item | Where |
|---|---|
| Reading the audit log (`audit:read`), also under a support action; platform auditors reading platform events (AUD-05) | 6.2 |
| Time-boxed budget exceptions and their approval permission (issue #31) | later; §11 says how it plugs in |
| Exposure report and bulk containment across tenants (issue #35) | later; §11 |
| Re-identification of a tenant from operational telemetry (issue #52) | later; §11 |
| Custom platform roles | not built: AZ-04 gives custom roles to tenants; platform roles stay the two of AZ-06 |
| Scopes, model lists or pins per subject mapping (4.1 O-5) | not built (D-7) |
| Role hierarchies, deny rules, conditions on a permission, permissions on single resources | not built (ADR 0007) |
| Group rules as resources of the API | later (4.2 O-7) |
| A guard that keeps the last owner of a tenant | not built: a tenant without an owner is fully administered by `tenant-admin`; an owner is appointed again through a support action (§5.2) |
| Auditing refused grants and refused support attempts as events | 6.2, with the read side (O-12) |
| Metrics of authorization decisions and support actions | 6.1 |
| Deleting support actions and custom roles with their tenant | 6.3 |

## 3. Requirements claimed

Meanings quoted from [docs/requirements](../../../../docs/requirements/README.md).

| ID | Requirement | Claim |
|---|---|---|
| **AZ-03** | "Pandokos ships immutable built-in tenant roles: owner, admin, credential manager, usage viewer, auditor, developer, inference, read-only" | full: the six missing roles; no operation changes a built-in role, and their names cannot be taken by a custom role (§5.2) |
| **AZ-04** | "A tenant can define custom roles from the permission catalogue. Nobody can grant a permission they do not hold" | full (§5.3, §5.4) |
| **AZ-05** | "A role that is still assigned cannot be deleted; roles can be deactivated" | full (§5.3) |
| **AZ-06** | "Platform roles are separate from tenant roles and imply no tenant membership: platform admin, platform auditor. No platform role reads the usage or the costs of a tenant" | the R2 part: `platform-auditor`; the second sentence as a property of the catalogue and of the support action (§5.6, T-847, T-872, T-R62). The requirement no longer lists a platform usage viewer and none is built |
| **AZ-09** | "A platform admin acting inside a tenant (its users, roles or keys) performs an explicit support action that requires a reason, is audited in the tenant's log and can be disabled by configuration" | full (§5.6, §5.7) |
| **AZ-10** | "Managing credentials, managing identities and viewing costs can be granted separately" | full: `credential-manager`, `usage-viewer`, and an identity manager as a custom role; none of the three can do what the other two do (T-895) |
| **AZ-11** | "The administration API tells a signed-in user who they are, which tenants they belong to and which permissions they hold in each, so that a client can offer only what is allowed. Hiding a function in a client is never the control: the API checks every request" | full (§5.5): 4.2 delivered the identity and the tenants |
| **SEC-08** | "High-impact administrative operations require a reason" | full for the nine operations of §5.8, by a construct of the API document that later operations reuse |

Touched, claimed elsewhere, and kept true: **AZ-01, AZ-02** (every new route declares a
permission; the decision function keeps its five rules), **AZ-07, AZ-08** (the platform
administrator's own permissions do not change; credential oversight stays what it is and is not
behind the support switch), **TEN-02, TEN-04** (one tenant-scoped table with fixtures; a support
principal is bound to one tenant and the path's tenant is checked as for any principal),
**AUD-01 to AUD-03** (roles and support actions are audited in the transaction of the change),
**USE-06, USE-07** and ADR 0054 (no platform permission reads usage, limit events or price lists,
and no support action carries one), **IAM-12** (group rules may name any role a user can hold),
**API-03, API-05** (commands for every operation; the two new creates take an idempotency key),
**UC-08, UC-16** (T-890, T-893).

## 4. Repository layout

New:

```
internal/authz/roles.go           the ten built-in roles
internal/authz/grant.go           CanGrant, the support principal
internal/authz/testdata/roles.golden   role -> permissions, the reviewed matrix
internal/admin/roles.go           catalogue, built-in roles, custom roles
internal/admin/grant.go           the grant guard and the resolution of role names
internal/admin/support.go         support actions, both sides; the support principal of the hook
internal/cli/roles.go             role, permission and support commands
internal/permdoc/                 generator of docs/reference/permissions.md
internal/store/migrations/sql/0014_roles_support.sql
internal/store/queries/sql/roles.sql, support.sql
docs/operations/roles.md
docs/adr/0060-…, 0061-…
```

Changed: `internal/authz/authz.go`, `internal/authn/{authn,tokens,cache}.go` (the row gains
`granted`), `internal/admin/{admin,me,problem,service_accounts,members,users,virtual_keys,subject_mappings,platform_keys,tenants,time_zone,active_currency,auth}.go`,
`internal/audit/audit.go`, `internal/store/tx.go`, `internal/store/queries/sql/{virtual_keys,issuers,sessions,audit}.sql`,
`internal/apigen`, `internal/adminapi/runtime.go`, `internal/config/*`, `cmd/pandokos/serve.go`,
`api/openapi/admin.v1.yaml`, generated code, `test/apitest`, `test/dbtest`, `test/isolation/spec.go`,
`test/repo/*`, `Makefile`, documentation.

## 5. Architecture

### 5.1 The catalogue (D-1, D-12)

Eight permissions are added; the catalogue then has 57: 34 of scope tenant, 21 of scope platform,
2 of scope self.

| Permission | Scope | Allows |
|---|---|---|
| `role:read` | tenant | Reading and listing the custom roles of the tenant |
| `role:write` | tenant | Defining and changing a custom role; deactivating and activating it |
| `role:delete` | tenant | Deleting a custom role that nobody holds |
| `support_action:read` | tenant | Reading the support actions that the platform opened in the tenant |
| `support_action:end` | tenant | Ending a support action of the platform in the tenant |
| `tenant_setting:write` | tenant | Setting the tenant's time zone and its active currency |
| `tenant_support:open` | platform | Opening a support action in a tenant, with a reason, and closing support actions |
| `tenant_support:read` | platform | Reading and listing the support actions of a tenant |

`setTenantTimeZone` moves from `limit:write` and `setActiveCurrency` from `price_list:write` to
`tenant_setting:write` (5.3 O-4, 5.4 O-7). Reading stays where it is (`limit:read`,
`price_list:read`). With custom roles a principal that may write limit rules, or prices, would
otherwise also decide when every budget of the tenant resets and in which currency it is counted;
splitting after tenants have written roles would change what those roles mean, so it is done
before the first custom role exists. No principal loses anything by its roles, because both roles
that held the two permissions hold the new one; a **key whose scope list** names `limit:write` or
`price_list:write` and not the new permission can no longer change the two settings (release
note, R-9).

A permission gains two attributes, both false unless said:

| Attribute | Meaning | True for |
|---|---|---|
| `InTenant` | a platform permission whose operations read or change one tenant | `tenant_credential:read`, `tenant_credential:revoke`, `tenant_support:open`, `tenant_support:read` |
| `Support` | a tenant permission that a support action may carry (§5.6) | `service_account:read`, `service_account:write`, `virtual_key:read`, `virtual_key:write`, `virtual_key:revoke`, `member:read`, `member:write`, `role:read`, `role:write`, `role:delete`, `support_action:read` |

The two lists are closed by tests (T-846, T-R62): no permission whose resource is `usage`,
`price_list`, `limit`, `credential`, `model`, `token_issuer`, `subject_mapping` or
`tenant_setting` is `Support`, and no platform permission has such a resource except the two
credential-oversight permissions of AZ-08.

`GET /v1/permissions` (`listPermissions`) and `GET /v1/roles` (`listBuiltinRoles`) return the
catalogue and the ten roles to any authenticated caller (`identity:read`): they are what the
documentation already publishes, and a client needs them to draw a role editor.

### 5.2 Built-in roles (D-2, D-3)

Every role also holds `identity:read`. "Members" says whether a person can hold the role; every
tenant role can be given to a service account.

| Role | Scope | Members | Permissions |
|---|---|---|---|
| `owner` | tenant | yes | every tenant permission of the catalogue (34), by construction |
| `tenant-admin` | tenant | yes | every tenant permission but those kept for owners: today `support_action:end` (33) |
| `credential-manager` | tenant | yes | `credential:read`, `credential:write`, `credential:rotate`, `credential:delete`, `model:read` |
| `usage-viewer` | tenant | yes | `usage:read`, `price_list:read`, `limit:read`, `model:read` |
| `auditor` | tenant | yes | `service_account:read`, `virtual_key:read`, `credential:read`, `member:read`, `role:read`, `token_issuer:read`, `subject_mapping:read`, `limit:read`, `model:read`, `support_action:read` |
| `developer` | tenant | yes | `model:invoke`, `credential:use`, `model:read`, `credential:read`, `service_account:read`, `virtual_key:read`, `virtual_key:write`, `virtual_key:revoke`, `usage:read`, `limit:read` |
| `inference` | tenant | no | `model:invoke`, `credential:use` (unchanged) |
| `read-only` | tenant | yes | every tenant permission whose action is `read` (12), by construction |
| `platform-admin` | platform | yes | every platform permission (21) |
| `platform-auditor` | platform | yes | `tenant:read`, `platform_key:read`, `catalog:read`, `platform_issuer:read`, `platform_limit:read`, `user:read`, `tenant_support:read` |

Notes that the table does not carry:

- **Owner and administrator.** `owner` is the role that holds everything, now and when
  permissions are added; `tenant-admin` holds what an administrator needs day to day and is a list
  that a reviewer extends on purpose. They differ today by one permission: only an owner ends a
  support action of the platform. Because of §5.4 the difference protects itself: an
  administrator cannot make an owner, cannot take the role from one, cannot reset an owner's
  password and cannot issue a key for an owner's service account. Permissions that later sprints
  keep for owners (approving a budget exception, asking for the tenant's deletion) get the same
  protection without new code.
- **The first owner.** `createTenant` gives the first service account the role `owner` instead of
  `tenant-admin`. Migration 0014 gives `owner` to every service account and every member that
  holds `tenant-admin` by an administrator's hand, so that every existing tenant has an owner and
  nobody holds less than before: until this sprint `tenant-admin` was "every tenant permission"
  (ADR 0031, which gets a history line).
- `credential-manager` does not hold `credential:use`: that permission is for callers and has an
  effect only together with `model:invoke` (O-6).
- `auditor` reads how the tenant is configured and who may do what, and what the platform did in
  it; it does not read usage or prices (AZ-10 keeps costs apart). Until 6.2 adds `audit:read` it is
  a subset of `read-only`; 6.2 gives `audit:read` to `owner`, `tenant-admin` and `auditor` and not
  to `read-only`, which a test of this sprint already allows for (T-847 checks `read-only` by
  construction and the others by the golden file).
- `developer` issues keys, but §5.4 lets it issue them only for service accounts that hold no more
  than it does: for an `inference` or a `developer` account, not for an administrator's.
- `platform-auditor` does not read the credentials of tenants: AZ-08 gives that to the
  administrator. It reads the support actions, which are the platform's own acts.
- A member cannot hold `inference` (4.2: a person cannot call a model). A member may hold
  `developer` or a custom role with `model:invoke`; the permission has no effect in a session,
  which the inference listener never reads.
- A platform key may hold `platform-auditor` (`createPlatformKey` takes `role`, default
  `platform-admin`): a read-only key for an inventory job (O-11).

The roles are the content of `internal/authz/testdata/roles.golden`, one line per role with its
sorted permissions. The file is the matrix of the exit condition: the code is compared with it
(T-847), the routes with it (T-867), `/v1/me` with it (T-868) and the generated reference with it
(T-887). Changing a role is a change of that file, in review.

### 5.3 Custom roles (D-4, D-5)

`tenant_role` (tenant-scoped, canonical policy): `name`, `description`, `permissions`, `state`
(`active`, `inactive`).

- **Name**: the format of a role name (`^[a-z][a-z0-9-]{0,62}$`), unique in the tenant, immutable
  (assignments hold it), not the name of a built-in role and not starting with `platform-` or
  `pandokos-`, the two prefixes kept for roles the product adds later (R-6). The check is in the
  table as well as in the handler; T-R60 keeps the list of the migration equal to the code.
- **Permissions**: 1 to 64 names of **tenant** permissions of the catalogue, stored sorted and
  without duplicates. A platform permission, a self permission and an unknown name are refused
  (`400 invalid_request` on `permissions`, naming the first offender). Every tenant permission is
  available, the six limit permissions and `price_list:read` alone included.
- **At most 100** custom roles in a tenant (`409 conflict` beyond; counted without a lock, so two
  concurrent creates can exceed it by one).
- **Who holds a role**: `role_assignment.role` and `membership_role.role` hold the name of a
  built-in tenant role or of a custom role of the tenant, as today. `platform_role_assignment`
  holds built-in platform roles only: custom roles are the tenant's (AZ-04). This departs from
  4.2 §11, which expected three tables to reference custom roles.
- **Assigning**: a role named in `roles` must be a built-in tenant role (not `inference` for a
  member) or an **active** custom role of the tenant, else `400 invalid_request` on `roles`. The
  handler reads the custom rows `FOR SHARE` in the transaction of the assignment.
- **Deactivating** (`deactivateRole`, `activateRole`): an inactive role gives its holders nothing
  and cannot be assigned; assignments stay. For a member the change holds from the next request;
  for a key or a token, when the gateway's cache is flushed by the notification of the change, and
  at the latest at the bound of that cache (ADR 0030).
- **Deleting** (`deleteRole`): refused with `409 role_in_use` while a member or a service account
  holds the role (AZ-05). The handler locks the row `FOR UPDATE`, counts the assignments, then
  deletes; a concurrent assignment waits on its `FOR SHARE` and then finds no role. A trigger on
  the table refuses the deletion of an assigned role as a backstop.
- **A name without a role** (an assignment whose custom role is gone, which only a fault could
  leave) gives nothing: the default is deny.
- **Group rules** (`auth.oidc.group_mappings`) may name any built-in role a user can hold in the
  scope, and for a tenant target a custom role. At start a tenant rule's role must be built-in or
  exist in the tenant; at sign-in a custom role that is missing or inactive is not given, with one
  log line that names the rule's position only. The operator's configuration is the grantor here;
  §5.4 does not apply to it.

**How the permissions reach a decision.** A principal carries `Roles` (names) and now `Granted`:
the union of the permissions of the active custom roles among them, read by the same statement
that reads the roles (`GetKeyPrincipal`, `GetTokenPrincipal`, `GetMemberPrincipal`). The gateway
role gets `SELECT` on `tenant_role` and nothing else; a trigger on the table sends the tenant's
cache notification. `Principal.Effective()` is:

1. the permissions of the built-in roles among `Roles` whose scope counts for the principal;
2. for a principal that holds tenant roles, each name of `Granted` that is a tenant permission of
   the catalogue (anything else is dropped, so a permission removed from the catalogue later, or a
   row written around the API, cannot give a platform permission);
3. for a user, `identity:read` and `session:self`;
4. intersected with `Scopes` when the key has a scope list.

For a support principal steps 1 and 2 are replaced by §5.6.

### 5.4 The rule against escalation (D-6, D-7)

**Nobody gives, or takes over, a permission they do not hold.** One function decides:
`Principal.CanGrant(permissions)` is true when every named permission of scope tenant or platform
is in `Effective()`. The scope list of a key is part of `Effective()`, so a restricted key grants
only inside its restriction. `authz.Assignable` is removed.

The operations that confer power, and what each must hold (`403 escalation_refused`, nothing
changed, when it does not):

| Operation | The caller must hold every permission of |
|---|---|
| `createRole` | the new role |
| `updateRole`, `activateRole`, `deactivateRole`, `deleteRole` | the role as it is, and as it will be |
| `createServiceAccount`, `updateServiceAccount` | each role that is added or removed |
| `createMember`, `updateMember` | each role that is added or removed |
| `deleteMember` | the roles of the member |
| `createUser`, `updateUser` | each platform role that is added or removed |
| `createPlatformKey` | the role of the key |
| `createVirtualKey` | what the key will be able to do: the permissions of the service account, intersected with the key's scope list |
| `createSubjectMapping` | the permissions of the service account the subject will act as |
| `resetMemberPassword`, `resetUserPassword` | the permissions of the person whose password is reset |

- **Roles that stay are not checked.** `roles` replaces the whole set (4.2); only the difference
  with the current set is conferred or removed by the caller. In 4.2 the whole set was checked.
- **Removal is covered.** Taking a role from somebody needs the same permissions as giving it.
  Without this an administrator could strip an owner and nothing of `owner` would be protected.
- **Containment is not covered.** Disabling, enabling and unlocking an identity, ending sessions,
  revoking a key and deleting a subject mapping give nobody anything; whoever holds their
  permission may use them on any identity of the tenant, so that a principal with
  `virtual_key:revoke` alone can revoke a leaked key of an administrator.
- **An unknown role is still `400`.** `escalation_refused` is the answer for a role that exists
  and that the caller may not hand out; 4.2 answered `400` for both.
- **The table is closed by a test.** T-865 holds a classification of every operation of the API
  document (`confers`, `removes`, `contains`, `none`) and fails when an operation is missing from
  it, so a new operation cannot ship without the question being asked.
- **Token mappings** (4.1 O-5): a mapping gives a subject every role of the service account. With
  the guard a principal cannot map a subject to an account stronger than itself, and a custom role
  on a dedicated service account is the way to give a token less. Scopes per mapping are not
  added.

Under a support action the rule is different, on purpose (§5.6, D-9).

### 5.5 `GET /v1/me` (AZ-11)

For a user the response of 4.2 gains, in each member of `memberships`, **`permissions`**: the
effective permissions in that tenant. They are computed by the function that builds the principal
of a tenant request (`memberPrincipal`, one place, used by the hook and by `getMe`), so what the
API reports and what it enforces cannot differ; T-869 checks the two against each other over
random combinations of roles. A suspended tenant is listed with its state and with the
permissions the member would have: the state is what blocks, and the client sees both.

For a platform principal (a platform key, or a user with a platform role) the response gains
**`support_actions`**: the open support actions of the caller, each with the tenant, the
permissions and the expiry. `permissions` at the top level stays what the caller holds outside any
tenant.

For a service account nothing changes in shape; `permissions` now includes those of custom roles.

### 5.6 The support action (D-8 to D-10)

A platform principal holds no tenant permission, with or without this sprint. What changes is the
one place where the hook decides that a platform principal has nothing to do in a tenant.

**Opening.** `POST /v1/tenant-support/{tenant}/actions` (`openSupportAction`, platform permission
`tenant_support:open`) with:

| Member | Rule |
|---|---|
| `reason` | required, 1 to 500 characters, not only white space |
| `permissions` | required, 1 to 11 names, each a permission with the attribute `Support`; any other name is `400 invalid_request` |
| `duration_seconds` | optional, 60 to `support.max_duration`; default `support.max_duration` (1 h) |

One transaction that is the platform's and the tenant's (`store.ActInTenant`) writes the row of
`support_action`, the event `support_action.open` in the **tenant's** audit log and the same event
in the platform's. An open action of the same actor in the same tenant is closed first (one open
action per actor and tenant, a unique index). The response is the action. With
`support.enabled: false` the answer is `403 support_disabled` and nothing is written.

**Using.** The hook, for an operation whose permission has the scope tenant:

| The principal | Before | Now |
|---|---|---|
| a platform key | `403 forbidden` | looks for an open action of this key in the path's tenant |
| a user that is a member of the tenant | the member's principal | unchanged: the membership wins, no action is looked for |
| a user that is not a member | `404 not_found` | looks for an open action of this user in the path's tenant |

The look-up is one statement in the platform scope: the row of `support_action` for (tenant, actor
type, actor ID) that is not closed and not expired, with the tenant's name and state. It is made
only when `support.enabled` and when the caller **still holds** `tenant_support:open`; otherwise,
and when there is no row, the answer is the one of the "Before" column, byte for byte.

With a row the request runs as a **support principal**: the caller's kind, ID and name, the
tenant of the path, no role, `SupportActionID`, and `Granted` = the permissions of the action.
`Effective()` for it is `Granted` intersected with the permissions that have the attribute
`Support` (and with the scope list, and the two self permissions for a user). `Decide` then
applies the tenant-state rule and the tenant rule to it as to any principal of that tenant; a
platform permission is never allowed to a principal that has a tenant. An operation outside the
action's list is `403 forbidden`.

**What a support action can never do**, whatever is asked: read usage records, limit events,
price lists, the capability check or a credential; write credentials, limits, prices, issuers,
mappings, model restrictions or the tenant's settings; call a model. None of these permissions has
the attribute, and `openSupportAction` refuses them before anything is written.

**Grants under a support action (D-9).** The platform acts with the tenant's authority, not its
own: a support principal may give any tenant role and issue a key for any service account, when
the operation's permission is in the action. `CanGrant` is true for it for any set of tenant
permissions. This is what makes the recovery of §5.7 possible, and it is the reason why every
write of a support principal names the action in the tenant's log. Two limits: a user cannot make
**itself** a member of the tenant under its own support action (`409 invalid_state`), and a
platform permission is never grantable this way.

**What this means for AZ-06.** A platform administrator can reach a tenant's data only by making
an identity in the tenant for somebody (a key, a member): there is no other path, the path needs
an open support action, and each step is a row in the tenant's log that the tenant reads and
that names the action and its reason. The threat model states this as the residual exposure of
the platform administrator's role; `support.enabled: false` removes it together with the recovery.

**Ending.** An action ends by its expiry; by `closeSupportAction` (platform, `tenant_support:open`,
any open action of the tenant, optional reason); by `endSupportAction` (the tenant,
`support_action:end`, optional reason). Closing writes `support_action.close` to both logs in one
transaction; ending writes `support_action.end` to the tenant's log. There is no background job:
an action is open while `closed_at` is null and `expires_at` is in the future, and the next
request sees the end. A row is never deleted in this sprint.

**Seeing.** The tenant lists and reads the support actions of its tenant
(`listSupportActions`, `getSupportAction`, `support_action:read`): who (actor type, ID and name),
why, which permissions, from when to when, how it ended. The platform reads the same per tenant
(`listTenantSupportActions`, `getTenantSupportAction`, `tenant_support:read`).

**Audit of what is done.** `audit_event` gains the nullable column `support_action_id`.
`audit.RecordTenant` fills it from the principal of the context, so every handler that writes an
audit row writes it without a change. The actor of the row is the platform principal (`platform`
with the key ID, or `user`), as for the credential oversight of 2.1.

### 5.7 Recovery of a tenant that lost its administrators (1.3 O-2)

No new operation. A platform administrator:

1. opens a support action in the tenant with `service_account:read` and `virtual_key:write`, and
   the reason;
2. lists the service accounts and issues a key for the owner's account (`createVirtualKey`; the
   key is shown once, to the administrator, who hands it over), or with `member:write` creates a
   member with the role `owner`, whose generated password must be changed at first sign-in;
3. closes the action.

The tenant's log then holds `support_action.open`, `virtual_key.create` (or `member.add`) with the
action's ID, and `support_action.close`. T-874 is this sequence. It also covers what 4.2 left
open: acting on the users a tenant manages (reset, unlock, disable) is the member operations under
a support action with `member:write`.

### 5.8 Reasons, and the general mechanism (D-11, D-12)

**The construct.** An operation of the API document may declare `x-pandokos-reason: required`.
The generator then demands a request body whose schema has a required string member `reason`, and
the generated handler refuses a reason that is empty after trimming white space
(`400 invalid_request` on `reason`), so no handler has to remember it. `Operation` gains
`ReasonRequired`. The reason goes to the `reason` column of the audit row, as optional
reasons do today.

**The nine operations**, by three criteria:

| Criterion | Operations |
|---|---|
| the platform acts on a tenant | `suspendTenant`, `reactivateTenant`, `disableTenantCredential`, `revokeTenantCredential` (the last two since 2.1), `openSupportAction` |
| the caller obtains another person's credential | `resetUserPassword`, `resetMemberPassword` |
| one call changes what many identities may do | `deactivateRole`, `activateRole` |

Every other operation that takes a reason keeps it optional. **Limit rules** join them: the
bodies of `createLimit`, `updateLimit`, `createPlatformLimit` and `updatePlatformLimit` gain an
optional `reason`, recorded in the audit row of the change (the epic's note on issue #31); the
approval of an overrun, when #31 is built, declares `required` on its own operation. `DELETE`
operations have no body and no reason. For `suspendTenant` and `reactivateTenant` this is
a **change of a v0.1 contract** (the reason was optional): release note, O-4.

**What later work reuses** (issues #31, #35, #52; none is built here):

| Piece | Where | Reused by |
|---|---|---|
| A permission is one line of the catalogue with a scope and two attributes; roles are lists; `owner` gets every tenant permission by construction | §5.1, §5.2 | #31: `budget_exception:approve`, tenant scope, held by `owner` and by whichever roles the sprint decides; a custom role can hold it alone |
| `x-pandokos-reason: required` | this section | #31 (approval), #35 (containment), #52 (re-identification) |
| `InTenant` on a platform permission, with the rule that every operation under it that changes something requires a reason and writes its audit row to the **tenant's** log (T-878) | §5.1 | #35 and #52: their platform permissions get the attribute and inherit the test |
| `store.ActInTenant`: one transaction that is the platform's and one tenant's; `audit.RecordIntervention`: the same event in the tenant's log and in the platform's | §5.6 | #35: one such transaction per affected tenant; #52: the entry the tenant sees |
| A configuration switch per capability, read at the one place where the capability starts (`support.enabled`) | §5.6 | #35, #52: a switch each |
| The support action as a time-boxed, reasoned, visible grant that the tenant can end | §5.6 | 6.2: `audit:read` gets the attribute `Support` if the maintainer wants operators to read a tenant's log under a support action |

### 5.9 Administration API (specification first)

The full additions are in plan.md, appendix A3. Sixteen operations are added; the document then
has 148, of which 144 name a permission.

| Group | Operations |
|---|---|
| catalogue | `listPermissions` `GET /v1/permissions`, `listBuiltinRoles` `GET /v1/roles` [`identity:read`] |
| custom roles | `listRoles`, `getRole` [`role:read`]; `createRole`, `updateRole`, `deactivateRole`, `activateRole` [`role:write`]; `deleteRole` [`role:delete`], under `/v1/tenants/{tenant}/roles`, a role addressed by its name |
| support, platform side | `openSupportAction`, `closeSupportAction` [`tenant_support:open`]; `listTenantSupportActions`, `getTenantSupportAction` [`tenant_support:read`], under `/v1/tenant-support/{tenant}/actions` |
| support, tenant side | `listSupportActions`, `getSupportAction` [`support_action:read`]; `endSupportAction` [`support_action:end`], under `/v1/tenants/{tenant}/support-actions` |

Rules that the schemas cannot say:

- `getRole` returns the role with `assignments`: the number of members and of service accounts
  that hold it. `listRoles` filters by `state` and sorts and pages as other collections.
- `updateRole` changes `description` and `permissions`; an optional `reason` is recorded.
- `createRole` and `openSupportAction` answer 201 and therefore take `Idempotency-Key` (4.2).
- `createPlatformKey` takes an optional `role` (a platform role; default `platform-admin`).
- `LimitRuleCreate`, `LimitRuleUpdate` and `PlatformLimitRuleCreate` gain an optional `reason`.
- `Membership` gains the required member `permissions`; `Me` gains `support_actions`.
- The platform-side support operations answer `404 not_found` for a tenant that does not exist;
  the tenant-side ones follow the rules of any tenant operation.
- `support_disabled` is answered by `openSupportAction` only. Reading and closing work with the
  switch off, so that what was done stays visible and can be tidied.

**Problem codes** added (ADR 0020, history line):

| `code` | Status | Raised when |
|---|---|---|
| `escalation_refused` | 403 | the operation would give, or take, a permission the caller does not hold (§5.4) |
| `support_disabled` | 403 | `support.enabled` is false |
| `role_in_use` | 409 | a custom role that is still assigned was to be deleted |

CLI (API-03): `permission list`, `role builtin`, `role list|create|get|update|deactivate|activate|delete`,
`tenant-support open|list|get|close` and `support-action list|get|end`; `--reason` on the nine
operations is a required flag; `platform-key create --role`.

### 5.10 Audit, logs, configuration

**Audit actions** added to the vocabulary of ADR 0032:

| Action | Log | Written when |
|---|---|---|
| `role.create`, `role.update`, `role.deactivate`, `role.activate`, `role.delete` | tenant | a custom role changes; detail: the name, the permissions or their change (added, removed), the state |
| `support_action.open` | tenant and platform | opened; reason; detail: permissions, expiry, the action closed by this opening if any; in the platform's row also the tenant ID |
| `support_action.close` | tenant and platform | closed by the platform |
| `support_action.end` | tenant | ended by the tenant |

Existing actions are unchanged; their rows carry `support_action_id` when a support principal
acted. A refused grant writes no audit row (O-12).

**Logs** (ADR 0055). The `admin request` line of a support principal names the platform principal
as it does on the platform, adds `support: true`, and names neither the tenant nor the action.
No line and no span attribute names a role, a custom role, a reason or an action ID. A refused
grant logs `grant refused` with the request ID and the operation.

**Configuration**:

| Key | Default | Rule |
|---|---|---|
| `support.enabled` | `true` | false: no support action can be opened and none is honoured |
| `support.max_duration` | `1h` | 1 min to 24 h |

`serve` warns at start when `support.enabled` is false ("a tenant that loses its administrators
cannot be recovered through the API").

### 5.11 Interfaces

```go
// internal/authz
type Permission struct {
	Name        string
	Scope       Scope
	Description string
	InTenant    bool // a platform permission whose operations read or change one tenant
	Support     bool // a tenant permission that a support action may carry
}

type Role struct {
	Name        string
	Scope       Scope    // ScopePlatform or ScopeTenant
	Members     bool     // a person can hold it
	Permissions []string // sorted
	Description string
}

const (
	RoleOwner, RoleCredentialManager, RoleUsageViewer = "owner", "credential-manager", "usage-viewer"
	RoleAuditor, RoleDeveloper, RoleReadOnly          = "auditor", "developer", "read-only"
	RolePlatformAuditor                               = "platform-auditor"
)

func Roles() []Role                                   // the ten, in a fixed order
func TenantPermissions() []string                     // names of scope tenant, sorted
func SupportPermissions() []string                    // names with the attribute Support, sorted
func CheckCustom(permissions []string) (normalized []string, offender string) // sorted and unique; the first name that is not a tenant permission

type Principal struct {
	// ... as today, plus:
	Granted         []string // permissions that come from data: active custom roles, or a support action
	SupportActionID string   // the support action the request runs under; empty otherwise
}

func (p Principal) Effective() []string
func (p Principal) CanGrant(permissions []string) bool

// internal/store
// ActInTenant runs fn in one transaction in platform scope that is also the session of tenant id.
// Admin role only (ErrNotAdmin). It exists for what the platform does inside a tenant.
func (db *DB) ActInTenant(ctx context.Context, id tenant.ID, fn func(ctx context.Context, tx Tx) error) error

// internal/audit
type Event struct{ /* as today, plus */ SupportActionID string } // filled from the principal of ctx when empty
// RecordIntervention writes e to the audit log of tenant id and to the platform's, with the tenant
// ID added to the detail of the platform's row. tx comes from ActInTenant.
func RecordIntervention(ctx context.Context, tx store.Tx, id tenant.ID, e Event) error

// internal/admin (unexported; the names the plan uses)
func (s *server) memberPrincipal(ctx context.Context, u account.User, sess session.Session, tid tenant.ID) (authz.Principal, error) // errNotFound: no membership
func (s *server) supportPrincipal(ctx context.Context, p authz.Principal, tid tenant.ID) (authz.Principal, bool, error)
func (s *server) resolveRoles(ctx context.Context, q *queries.Queries, names []string, member bool) (map[string][]string, error) // role -> permissions; invalid("roles", …) for a name that cannot be assigned
func guardGrant(p authz.Principal, permissions ...[]string) error // errEscalation

// internal/adminapi
type Operation struct{ /* as today, plus */ ReasonRequired bool }
```

`Decide` keeps its five rules; two conditions change:

- rule 2 (platform permission): allowed only to a principal **without a tenant** that holds it (a
  platform key without a support action, or a user without a tenant);
- rules 3 and 4 (tenant state, tenant): a platform key **with** `SupportActionID` is treated as a
  principal of its tenant; without one it is denied as before.

Import rules (T-R61): only `internal/authz` knows what a role contains. Outside it a role
constant is named only where an identity is born with a fixed role: `createTenant`
(`RoleOwner`), and for `RolePlatformAdmin` the bootstrap key, the first administrator of
`pandokos init` and the default of `createPlatformKey`. Whether a person can hold a role is
`Role.Members`; nothing decides by comparing role names.

### 5.12 Threat notes

| Threat | Answer | Tests |
|---|---|---|
| A principal gives itself or a friend more than it holds, through a role, a key, a mapping or a reset | §5.4: one function, a closed table of operations, the difference of role sets | T-850, T-862 to T-865 |
| A custom role smuggles a platform permission | refused at write; dropped at read (`Effective` step 2) | T-848, T-859 |
| A deactivated or deleted role keeps working on the inference listener | notification on every change of `tenant_role`; the cache's own bound | T-857, T-858, T-896 |
| An administrator removes the owners and takes their place | removal needs the role's permissions; reset and key issue need the target's | T-863, T-864 |
| A platform administrator reads a tenant's usage | no platform permission, no `Support` attribute, refusal at opening; the only path makes an identity under a recorded action | T-847, T-872, T-R62 |
| A support action outlives its need | expiry at most 24 h by constraint, 1 h by default; closed by the platform, ended by the tenant; honoured only while the actor holds the permission and the switch is on | T-871, T-876 |
| A support action is opened and its record is lost | one transaction for the row and both audit events | T-870, T-856 |
| The answer for "no membership" starts to reveal which tenants exist | without an open action the answers are those of 4.2, unchanged | T-871 |
| What `/v1/me` says differs from what is enforced | one function builds both | T-869 |
| A future built-in role takes the name of a tenant's custom role | reserved names and prefixes; R-6 | T-859, T-R60 |

## 6. Isolation harness

- `DefaultSpec`: `support_action` in `Global` (restricted); the grant matrix of appendix A1
  (`tenant_role`: gateway `SELECT`, admin as granted; `support_action`: admin only;
  `audit_event`: one more insert column for the admin role); a fixture for `tenant_role`; no new
  definer function.
- T-854: the harness's negative tests on `tenant_role` for every runtime role; lint reports
  nothing with migration 0014.
- T-855: `support_action` per session: the platform scope sees all and writes; a tenant's session
  sees and ends its own and cannot open; another tenant and an unscoped session see none; the
  gateway and worker roles are refused; the gateway role cannot write `tenant_role`.
- T-888: the API: every role and tenant-side support operation with tenant B's credential on
  tenant A's path is 404; a support action in A gives nothing in B; the platform-side operations
  with a tenant's credential are 403.

## 7. Test infrastructure and CI

- **`test/apitest`** gains `AsRole(t, tenant, role)` (a service account with the role and a key),
  `MemberAs(t, tenant, role)` (a member with a session), `PlatformAs(t, role)` (key and user), and
  the call table gains the sixteen operations, so that the tests that iterate
  `adminapi.Operations` (T-788, T-798, T-819, T-825 of 4.2, and T-865, T-867 here) cover them.
- **`test/dbtest`** gains `CustomRole`, `SupportAction` (seeds, as the admin role) and `Age` for
  the expiry of an action (the row's times moved as the owner with the guard trigger off in the
  seed's transaction).
- No container is added. The `idp` job runs one more test (T-893) with the realm of 4.2 and a
  group rule that names `usage-viewer`.
- `make generate` also writes `docs/reference/permissions.md`; `make generate-check` compares it.
  One fuzz target, `authz.FuzzEffective`.

## 8. Decisions

| # | Decision | Record |
|---|---|---|
| D-1 | Eight permissions; `tenant_setting:write` takes the time zone and the active currency from `limit:write` and `price_list:write` (§5.1, O-5) | 0060; 0052, 0053 history |
| D-2 | Ten built-in roles with the contents of §5.2, pinned by a golden file; `owner` is every tenant permission by construction, `tenant-admin` a reviewed list, `read-only` every `read` by construction (O-1, O-6) | 0060 |
| D-3 | The first service account of a tenant is its `owner`; migration 0014 makes the holders of `tenant-admin` owners as well (§5.2, O-1) | 0060; 0031 history |
| D-4 | Custom roles are rows of a tenant-scoped table that assignments reference by name; tenant permissions only; not for the platform; the gateway role reads them (§5.3) | 0060; 0030 history |
| D-5 | A custom role is deactivated by a state and deleted only when unassigned, checked under a row lock with a trigger as backstop; reserved names and prefixes (§5.3) | 0060 |
| D-6 | The rule against escalation is `CanGrant` over a closed table of operations: defining roles, assigning and removing roles, issuing keys, mapping subjects, resetting passwords; containment is free (§5.4, O-10) | 0060; 0056 history |
| D-7 | No scopes per subject mapping; a mapping is a guarded grant (§5.4; 4.1 O-5) | 0045 history |
| D-8 | The support action: a platform permission, a mandatory reason, a list of permissions from a closed set, a bounded time, one row and two audit events in one transaction, honoured at the "no membership" place of the hook, switched by `support.enabled` (§5.6, O-2, O-7) | 0061 |
| D-9 | Under a support action the platform grants with the tenant's authority; a user cannot make itself a member; every write names the action (§5.6, O-3) | 0061 |
| D-10 | The tenant sees the support actions and its owner can end one (§5.6, O-8) | 0061 |
| D-11 | `x-pandokos-reason: required` on nine operations, by three criteria (§5.8, O-4) | 0061; 0028 history |
| D-12 | Two attributes of a permission (`InTenant`, `Support`), `store.ActInTenant` and `audit.RecordIntervention` are the general mechanism for what the platform does inside a tenant (§5.8) | 0061; 0027, 0032 history |
| D-13 | `/v1/me` and the hook build a member's principal with one function; platform principals see their open support actions (§5.5) | 0060 |
| D-14 | Three problem codes (§5.9) | 0020 history |
| D-15 | The permission reference is generated by `internal/permdoc` from the catalogue, the roles and the API document, and checked by `make generate-check` | 0060 |
| D-16 | A platform key may hold `platform-auditor` (§5.2, O-11) | 0060 |
| D-17 | Logs name no role, reason, tenant or action; a support principal's line says `support: true` (§5.10) | 0055 history |

## 9. Tests

Levels: **U** unit; **D** needs the test database; **I** in-process admin handler with the fake
issuer; **E** the built binary; **X** external identity providers (tag `idp`); **R** repository
test. IDs continue from T-845 and T-R58.

### New tests

| ID | Test | Level | Proves | Requirement |
|---|---|---|---|---|
| T-846 | `authz.TestCatalogueAdditions` | U | The eight permissions with their scopes; 34, 21 and 2 by scope; the exact lists of `InTenant` and `Support`; no `Support` permission and no platform permission has a resource of the forbidden list of §5.1 | AZ-02, **AZ-06** |
| T-847 | `authz.TestBuiltinRolesGolden` | U | `Roles()` equals `testdata/roles.golden`; `owner` equals `TenantPermissions()`; `tenant-admin` is `owner` without `support_action:end`; `read-only` is every tenant permission ending in `:read`; no platform role holds a tenant permission and the reverse; only `inference` has `Members` false | **AZ-03**, **AZ-06** |
| T-848 | `authz.TestEffective` | U | `Granted` counts for a principal that holds tenant roles and not for a platform principal; an unknown name, a platform and a self permission in `Granted` are dropped; the scope list intersects; a user's two self permissions; a support principal holds `Granted` ∩ `Support` and nothing from `Roles` | **AZ-04** |
| T-849 | `authz.TestDecideSupport` | U | `Decide` over the catalogue for support principals of both kinds: own tenant, another tenant (404), suspended tenant, a permission outside the action, every platform permission denied; the matrices of 1.3 and 4.2 pass unchanged | **AZ-09**, AZ-01 |
| T-850 | `authz.TestCanGrant` | U | Subsets and one missing permission; a scoped key; self permissions ignored; a support principal grants any tenant set and no platform permission; a property over random sets: `CanGrant` is true exactly when the set is inside `Effective()` | **AZ-04** |
| T-851 | `authz.FuzzEffective` | U | For arbitrary roles, `Granted`, scopes and kinds: `Effective()` is sorted, inside the catalogue, never holds a platform permission for a principal with a tenant nor a tenant permission for one without, and never panics | AZ-04 |
| T-852 | `store.TestRolesSchema` | D | Constraints of migration 0014: role name format, built-in names and the two prefixes refused, immutable name, permissions format and bounds, state; support action: actor, reason not blank, lifetime at most 24 h, one open action per actor and tenant, opened at the database's time, immutable but for its closing, closed once, closed as expired only after the expiry, never deleted | AZ-05 |
| T-853 | `migrations.TestUpgrade0014` | D | On a database of 0013 with data: service accounts and manual members with `tenant-admin` also hold `owner`, rows of source `idp` do not; nothing else of those tables changed; both tables are forced again; `audit_event` kept its rows and accepts the new column | — |
| T-854 | `isolation.TestTenantRoleTable` | D | The harness's negative tests on `tenant_role` for every runtime role; lint (T-140) reports nothing with migration 0014 | **TEN-04** |
| T-855 | `isolation.TestSupportActionVisibility` | D | §6, second item | TEN-04, **AZ-09** |
| T-856 | `store.TestActInTenant` | D | The transaction has the platform scope and the tenant; the admin role only; a failure after the first of two audit inserts leaves neither row and no support action | AUD-03 |
| T-857 | `authn.TestCustomRolePrincipal` | D | A key and a token of a service account with a custom role carry its permissions in `Granted`; an inactive role gives none; a name without a role gives none; after a change of the role the cached principal is dropped by the notification | **AZ-04** |
| T-858 | `gateway.TestCustomRoleInvokes` | D | A key whose account holds a custom role with `model:invoke` and `credential:use` calls a model; with one of the two it gets 403 in the family's shape; after `deactivateRole` the next call past the notification is 403 | **AZ-04**, AZ-01 |
| T-859 | `admin.TestRoles` | D | Every role operation: creation, the refusals of §5.3 with their members and codes (platform, self and unknown permission, duplicate normalised, built-in name, reserved prefix, 65 permissions, the 101st role), pagination, sorting, the state filter, `assignments` on `getRole`, `updateRole`, the two catalogue operations | **AZ-04** |
| T-860 | `admin.TestRoleDeactivation` | D | An inactive role gives a member nothing at the next request and cannot be assigned (`400`); assignments stay; `activateRole` restores; both need a reason | **AZ-05** |
| T-861 | `admin.TestRoleDeletionGuard` | D | Deleting a role held by a member, by a service account: `409 role_in_use`; after the last holder lost it: 204; fifty rounds of a concurrent assignment and deletion never end with an assignment of a missing role | **AZ-05** |
| T-862 | `admin.TestNoEscalationRoles` | D | A principal with `role:write` and without `usage:read` cannot create a role with it, add it to a role, activate, deactivate or delete a role that has it: `403 escalation_refused`, nothing changed; the same through a key whose scope list lacks it | **AZ-04** |
| T-863 | `admin.TestNoEscalationAssignments` | D | **Exit.** For each of `createServiceAccount`, `updateServiceAccount`, `createMember`, `updateMember`, `deleteMember`, `createUser`, `updateUser`, `createPlatformKey`: adding, and removing, a role with a permission the caller lacks is refused; a role that stays is not checked; a tenant administrator cannot assign `owner`, nor a custom role with `support_action:end`; an unknown role is `400` | **AZ-04** |
| T-864 | `admin.TestNoEscalationCredentials` | D | `createVirtualKey` for a stronger service account is refused and allowed with a scope list inside the caller's permissions; `createSubjectMapping` to a stronger account is refused; `resetMemberPassword` and `resetUserPassword` of a stronger person are refused; the containment operations work on a stronger identity | **AZ-04** |
| T-865 | `admin.TestGrantSurfaceIsClosed` | D | Every operation of `adminapi.Operations` is in the classification table of §5.4; each operation classified `confers` or `removes` answers `escalation_refused` when driven by a weaker principal | **AZ-04** |
| T-866 | `admin.TestAssignableRoles` | D | A member cannot hold `inference`; a service account can hold each tenant role; a platform role in a tenant and the reverse, an inactive custom role, a custom role of another tenant: `400` | AZ-03 |
| T-867 | `admin.TestRoleMatrix` | D | **Exit.** For each of the ten roles and every operation of the document: with a key of a service account (tenant roles) or a platform key (platform roles), and with a session of a member or platform user for the roles a person can hold, the answer is `403 forbidden` exactly when the role's line of the golden file lacks the operation's permission | **AZ-03**, **AZ-06**, AZ-01 |
| T-868 | `admin.TestMeMatrix` | D | **Exit.** For each role `getMe` reports exactly the line of the golden file: at the top level for keys and platform users, in `memberships` for members; a user in two tenants with different roles, one of them custom, sees each tenant's own set; a platform principal sees its open support actions | **AZ-11** |
| T-869 | `admin.TestMeMatchesTheHook` | D | For 200 random sets of built-in and custom roles of a member: the permissions `getMe` reports for the tenant are exactly the permissions of the operations the member is not refused | **AZ-11** |
| T-870 | `admin.TestSupportOpen` | D | The contract of §5.6: reason missing, blank; a permission without the attribute; duration bounds and default; the response; the row, the tenant's and the platform's audit event; a second opening closes the first; the idempotency key; `403 support_disabled`; a `platform-auditor` is `403`; an unknown tenant is `404` | **AZ-09**, **SEC-08** |
| T-871 | `admin.TestSupportPrincipal` | D | Without an action every tenant operation answers a platform key `403` and a platform user `404`, with bodies equal to those of 4.2; with an action exactly the listed permissions are allowed; after expiry, close, end by the tenant, the switch off, the loss of the platform role and the disabling of the user the refusal is back at the next request; a member's own roles win over an action; a suspended tenant is `403 tenant_suspended`; another tenant is unaffected | **AZ-09** |
| T-872 | `admin.TestSupportNeverReadsUsage` | D | **Exit.** A platform administrator, by key and by session: every operation under `usage:read`, `limit:*`, `price_list:*`, `credential:*`, `model:*`, `token_issuer:*`, `subject_mapping:*`, `tenant_setting:write` is refused without an action and with an action that holds all eleven permissions; opening an action that names one of them is `400`; nothing of the platform's own operations returns a usage record, a limit event or a price of the tenant | **AZ-06**, **AZ-09** |
| T-873 | `admin.TestSupportAudit` | D | Every write under an action is one row in the tenant's log with the platform actor and the action's ID; reads write none; open and close are in both logs with the reason, end in the tenant's; rows written by the tenant's own principals have no action ID | **AZ-09**, AUD-01 |
| T-874 | `admin.TestSupportRecovery` | D | §5.7: a tenant whose every key is revoked and that has no member; the platform opens an action, issues a key for the owner's account, closes; the tenant administers itself again; the tenant's log holds the three rows in order | **AZ-09** |
| T-875 | `admin.TestSupportGrants` | D | Under an action with `member:write` the platform creates a member with `owner`; a user cannot add itself (`409 invalid_state`); without `member:write` in the action the same call is `403 forbidden` | AZ-09 |
| T-876 | `admin.TestTenantSeesAndEndsSupport` | D | `auditor` and `read-only` list and read the actions, `usage-viewer` does not; `tenant-admin` cannot end one (`403 forbidden`), an owner can; the ended action stops at the next request; another tenant's action is `404` | **AZ-09** |
| T-877 | `admin.TestReasonRequired` | D | The operations with `ReasonRequired` are exactly the nine; each without a body, without the member, with an empty and a blank reason answers `400` on `reason` and changes nothing; with a reason the audit row carries it; the operations with an optional reason still accept none; the four limit-rule operations record a reason when one is given | **SEC-08** |
| T-878 | `admin.TestInTenantRule` | D | Every operation that is not a `GET` and whose permission has `InTenant` requires a reason, `closeSupportAction` excepted by name, and writes its audit row to the tenant's log | **SEC-08**, AZ-09 |
| T-879 | `admin.TestSettingSplit` | D | `setTenantTimeZone` and `setActiveCurrency` need `tenant_setting:write`; a custom role with `limit:write` or `price_list:write` alone is `403`; reading them is unchanged | AZ-02 |
| T-880 | `admin.TestPlatformAuditor` | D | A key and a user with `platform-auditor`: every read of its line works, every write is `403`, tenant credentials are `403`; `createPlatformKey` with the role, with an unknown role (`400`) and with a tenant role (`400`) | **AZ-06** |
| T-881 | `admin.TestProvisioningOwner` | D | `createTenant` gives the first service account `owner`; its key is refused by no tenant operation | AZ-03 |
| T-882 | `admin.TestGroupRulesWithRoles` | I | A rule that names `usage-viewer` and one that names a custom role give them at sign-in; an inactive or missing custom role is skipped with a log line that names no tenant, role or group; a rule that names `inference`, or an unknown role, stops the start | IAM-12 |
| T-883 | `adminapi.TestReasonConstruct`, `apigen.TestRejectsOutsideTheSubset` | U | `x-pandokos-reason: required` generates the trimmed check and sets `ReasonRequired`; rejected: another value, an operation without a body, a body whose `reason` is not a required string, a `GET` | API-02 |
| T-884 | `audit.TestSupportAction` | D | `RecordTenant` writes the action ID of a support principal and none otherwise; `RecordIntervention` writes the same event to both logs, the platform's with the tenant ID in its detail | AUD-02 |
| T-885 | `cli.TestRoleAndSupportCommands` | U | Every new operation has a command; flags map to the bodies; `--reason` is required on the nine | API-03 |
| T-886 | `config.TestSupport` | U | Defaults, bounds, environment names, the warning with the switch off | AZ-09 |
| T-887 | `permdoc.TestGenerate` | U | The output is deterministic; it holds every permission with its scope, attributes, operations and holders, every role's line equal to the golden file, the role-by-permission matrix and the nine operations | AZ-02 |
| T-888 | `isolation.TestRolesAndSupportAPI` | D | §6, last item | **TEN-04**, AZ-01 |
| T-889 | `admin.TestRolesTelemetry` | D | At log level `debug`, over the calls of T-859, T-870 and T-873: no line and no span attribute holds a tenant ID or name, a role name, a custom role's permissions, a reason or an action ID; the support line says `support: true` | ADR 0055 |
| T-890 | `e2e.TestRolesExit` | E | **Exit.** The binary: an owner creates a member with `usage-viewer` and a custom role; each signs in; `getMe` shows exactly its permissions; the usage viewer lists usage and is refused a credential and a member operation (UC-16); a tenant administrator is refused when it assigns `owner` | **AZ-03**, **AZ-04**, **AZ-11** |
| T-891 | `e2e.TestSupportExit` | E | **Exit.** A platform administrator, by session and by key, is refused the tenant's usage; opens a support action; is still refused the usage; issues a key for the tenant under the action and closes; the tenant's owner sees the action; with `support.enabled: false` the opening is refused and an open action stops working; the same through PgBouncer | **AZ-09**, **AZ-06** |
| T-892 | `e2e.TestUpgradeWithRoles` | E | A database of the previous release, migrated: every key works as before, the first service account is an owner, a key scoped to `limit:write` no longer sets the time zone | — |
| T-893 | `idp.TestKeycloakLimitedRole` | X | A Keycloak user whose group maps to `usage-viewer` signs in; `getMe` reports the role's permissions for the tenant and nothing else; a write is `403` (UC-08) | **AZ-11**, IAM-12 |
| T-894 | `admin.TestRolesIdempotency` | D | `createRole` and `openSupportAction` with one key twice: one role, one action, one audit row each; the replay of the opening does not extend the action | API-05 |
| T-895 | `admin.TestSeparationOfDuties` | D | A `credential-manager`, a custom identity manager (`service_account:*`, `virtual_key:*`, `member:*`, `role:read`) and a `usage-viewer`: each does its own operations and is refused every operation of the other two | **AZ-10** |
| T-896 | `admin.TestRoleChangeBound` | D | A member's role removed, a custom role's permission removed, a role deactivated: the member's next request is refused; a service account's key is refused on the administration listener at the next request | AZ-04 |
| T-R59 | `repo.TestPermissionReferenceIsGenerated` | R | `docs/reference/permissions.md` between its markers equals the output of `internal/permdoc`; `GENERATED` of the Makefile names it; nothing outside the markers contradicts it (no hand-written role table) | AZ-02 |
| T-R60 | `repo.TestBuiltinNamesInMigration` | R | The names in `tenant_role_name_not_builtin` are exactly the names of `authz.Roles()` | AZ-03 |
| T-R61 | `repo.TestNoDecisionByRoleName` | R | Outside `internal/authz`, a role constant is named only in the places of §5.11; no file compares `Principal.Roles` with a name | AZ-02 |
| T-R62 | `repo.TestPlatformReadsNoTenantUsage` | R | The repository test of 5.4, extended: no handler of an operation whose permission is of scope platform, and no handler reachable under a `Support` permission, calls a query of `usage.sql`, of limit events or of `tenant_prices.sql` | **AZ-06**, USE-06 |
| T-R63 | `repo.TestRolesGuide` | R | `docs/operations/roles.md` names the ten roles, the two configuration keys, the three codes, the nine operations and the recovery steps; ADR 0060 and 0061 exist and are indexed | — |

### Changed tests of earlier sprints

| Test | Change |
|---|---|
| T-140 (schema lint), `DefaultSpec` | two tables, the grants, one fixture |
| T-805, T-806 (4.2), the 1.3 authorization matrix | `Assignable` gone; the role count; the matrices themselves unchanged for the three old roles except `tenant-admin` gaining five permissions and `platform-admin` two |
| `TestTenantAdminIsEnoughAndNoMore` | runs for `owner`; `tenant-admin` is enough for everything but `endSupportAction` |
| T-802 (4.2) | a role the caller may not hand out is `403 escalation_refused`, not `400` |
| T-272 (1.3) | unchanged in outcome: a platform key without a support action gets 403 on every tenant operation |
| the tests of `suspendTenant`, `reactivateTenant`, `resetUserPassword`, `resetMemberPassword` | send a reason |
| the tests of `setTenantTimeZone`, `setActiveCurrency` (5.3, 5.4) | the permission |
| the tests that assert the roles of the first service account | `owner` |
| the OpenAPI, reference, CLI coverage and idempotency tests | sixteen operations |
| T-R58 (4.2) | the new log line and its `support` attribute |
| `repo` tests that read `permissions.md` by hand-written sentences | read the generated section, or are replaced by T-R59 |
| `config` default and key-list tests | two keys |

### Requirement coverage

| Requirement | Tests |
|---|---|
| AZ-03 | T-847, T-866, T-867, T-881, T-890 |
| AZ-04 | T-848, T-850, T-857 to T-859, T-862 to T-865, T-890 |
| AZ-05 | T-852, T-860, T-861 |
| AZ-06 | T-846, T-847, T-867, T-872, T-880, T-891, T-R62 |
| AZ-09 | T-849, T-855, T-870 to T-876, T-891 |
| AZ-10 | T-895 |
| AZ-11 | T-868, T-869, T-890, T-893 |
| SEC-08 | T-870, T-877, T-878 |

## 10. Definition of done for this sprint

**Requirements and tests**

- [ ] Every test of §9 exists under the stated name and passes; the changed tests pass; `make
      check`, `make test-idp`, `make test-browser`, `make test-container` and `make test-compose`
      pass.
- [ ] Each exit condition is demonstrated by its tests (T-867, T-868, T-863, T-872, T-890, T-891).

**Isolation and authorization**

- [ ] `tenant_role` has the canonical policy and a fixture; `support_action` has its three
      policies; the lint reports nothing; the gateway role holds `SELECT` on `tenant_role` and
      nothing else of migration 0014, the worker role nothing.
- [ ] Every new route declares a permission and has a cross-tenant negative test (T-888).
- [ ] The classification of T-865 lists every operation, and the golden file was read line by
      line in review.

**Secrets and telemetry**

- [ ] The canary tests of earlier sprints pass with the new operations in the call table; `make
      secrets` passes.
- [ ] No log line or span names a tenant, a role, a reason or a support action (T-889, T-R58).

**Security review**

- [ ] The table of §5.12 and the table of §5.4 were checked row by row by a reviewer who did not
      write the code, and the review is recorded in the pull request.

**Documentation and records**

- [ ] `docs/operations/roles.md`; the threat model (the platform administrator's residual
      exposure), the data model, the overview, the API and configuration references are current;
      `permissions.md` is generated; the OpenAPI document is current and `make generate-check`
      passes; the release notes of v0.2 carry the three contract changes (R-9).
- [ ] ADR 0060 and 0061 exist as `proposed` and are indexed; the fourteen history lines are
      written.
- [ ] The epic and the roadmap show the sprint as done; §11 is copied into the epics it changes.

## 11. Consequences for later sprints

- **6.2 (audit reading).** `audit:read` is one tenant permission: `owner`, `tenant-admin` and
  `auditor` get it, `read-only` does not. Whether a support action may carry it is decided there
  by giving it the attribute `Support` or not (O-2); nothing else is needed. The read API can
  filter by `support_action_id`. A platform permission to read the platform's log goes to both
  platform roles. Refused grants and refused support attempts can become events there (O-12).
- **Issue #31 (budget exceptions).** `budget_exception:approve` is a tenant permission distinct
  from `limit:write`; `owner` holds it by construction, other roles by decision; the operation
  declares `x-pandokos-reason: required`; a time-boxed exception can follow the shape of
  `support_action` (opened, expiring, closed once, immutable otherwise).
- **Issue #35 (exposure report and containment).** A platform permission with `InTenant`; the
  bulk action loops over the affected tenants with `store.ActInTenant` and
  `audit.RecordIntervention`, so each tenant's log holds its row and the platform's log one per
  tenant with the same request ID and reason; T-878 covers it without a change.
- **Issue #52 (tenant correlation in telemetry).** A platform permission with `InTenant` and a
  required reason; the entry the tenant sees is an intervention event in its log; its own switch.
- **6.1 (metrics)**: support actions opened, open and ended by cause; refused grants; without a
  tenant label.
- **6.3 (tenant deletion)**: removes the tenant's custom roles and support actions; the actor of
  a support action is a platform identity and is kept or anonymised with the platform's users.
- **8.2 (separate processes)**: the gateway reads `tenant_role` and needs the notification of its
  changes; nothing else of this sprint is on the gateway.
- **12 (operability)**: the chart exposes `support.enabled` and `support.max_duration`; the
  upgrade notes of v0.2 name the owner migration and the three contract changes.
- **A new built-in role** needs: a line of the golden file, a name added to the check of
  `tenant_role` by migration, and in that migration a rename of any custom role that has the name
  (the role and its assignments, in each tenant), unless the name starts with a reserved prefix.
- **A new permission** must be placed on purpose: T-847 fails until the golden file says which
  roles hold it (`owner` and, for a `read`, `read-only` get it by construction and the file must
  agree), and T-846 until its attributes are stated.

## 12. Risks and open points

### What was verified while designing

- **Migration 0014 as written in the plan was applied after 0001 to 0013 on PostgreSQL 16.15**, on
  a database with two tenants, service accounts and a member, and exercised as the admin, gateway
  and worker roles: the owner rows (the first draft looped over tenants with the tenant setting
  and wrote nothing, because the owner role sees no row of `tenant` under its policies; the plan's
  version lifts `FORCE` on the two tables as migration 0011 does, and both are forced again);
  every check of `tenant_role` and its guard; `FOR SHARE` and `FOR UPDATE` with the admin role's
  column privileges; the `granted` aggregate as the gateway role; `permission denied` for the
  gateway role on writes and for the worker role on everything; the three policies of
  `support_action` in the platform scope, in the tenant's session, in another tenant's and
  unscoped; a transaction with both settings inserting the action and both audit rows; the guard
  (immutability, one closing, the closing time, closing as expired only after the expiry); the
  unique open action; the insert into `audit_event` with the new column.
- `audit_event` has no `SELECT` grant for any runtime role, so nothing of this sprint can read a
  tenant's log; tests read it as the owner.
- The 132 operations of the API document: 128 name a permission and 4 are public; 23 take a
  reason today, 2 of them required.
- The administration authenticator has no principal cache; the gateway's cache is flushed per
  tenant by `pandokos_cache` notifications, which `notify_cache('scoped')` sends for any table
  with a `tenant_id`.
- `store.Provision` already runs a transaction with both settings; `ActInTenant` is the same
  transaction with the tenant known from the start.

**Not verified** (the implementer verifies in the part named, and stops if it does not hold):

- `sqlc` generation of the new queries, in particular the lateral `unnest` of the `granted`
  aggregate (part A); if `sqlc` refuses it, the aggregate becomes a SQL function without
  `SECURITY DEFINER`, not a string in Go.
- That every handler of a `confers` or `removes` operation can read the current roles of its
  target before it writes (part C3; `createSubjectMapping` reads the service account it names).
- That no test or document outside §9's list depends on the first service account holding
  `tenant-admin` by name (part B: `git grep -n '"tenant-admin"'`).
- The schema lint and the isolation harness with a global table that has three policies of which
  two are for a tenant's session (`app_user` of 4.2 is the precedent).

### Risks

| # | Risk | Answer |
|---|---|---|
| R-1 | An operation that confers power is missing from the guard | The closed classification of T-865 over the API document; the reviewer's row-by-row check; a new operation fails the test until it is classified |
| R-2 | The support action becomes a general way into tenants | A closed set of eleven permissions, none of which reads usage, costs or secrets; a required reason; at most 24 h; one open action per actor and tenant; visible to the tenant and ended by its owner; a switch; the residual path (making an identity) is stated in the threat model |
| R-3 | D-9 (grants with the tenant's authority) is the escalation the sprint otherwise forbids | It is the recovery, and it is the only place; every such write names the action in the tenant's log; self-membership is refused; O-3 offers the narrower alternative |
| R-4 | Custom roles on the hot path: one more join for every key that is not cached | The aggregate is part of the statement that already reads the roles, so a cache miss costs the same number of round trips as before; it joins on an indexed pair inside one tenant. T-857 counts the statements |
| R-5 | A stale principal keeps a permission that a role lost | Members: read per request. Keys: notification on every change of `tenant_role`, and the cache's own bound (ADR 0030); T-857, T-858 |
| R-6 | A later built-in role collides with a tenant's custom role | Names of built-in roles and two prefixes are refused now; §11 says what the migration of a new role does |
| R-7 | The owner migration gives more than intended | It gives `owner` only where `tenant-admin` was held, which until now meant every tenant permission; the one permission that is new to them is `support_action:end`; rows of group rules are left alone; T-853 |
| R-8 | The golden file and the code drift, or the file is edited to make a test pass | The file is the thing reviewed; T-847 states the three roles that are checked by construction, so the file cannot quietly drop a permission from `owner` |
| R-9 | Three contract changes inside v0.2: a required reason on `suspendTenant` and `reactivateTenant`; `tenant_setting:write` for scoped keys; the first service account's role name | Release notes and the upgrade guide; `owner` holds everything `tenant-admin` did; T-892 |
| R-10 | The generated reference loses the explanations of the hand-written page | The generator writes the tables between markers; the prose around them stays and is checked by T-R59 for contradictions only |
| R-11 | Removal counted as a grant blocks a legitimate clean-up (an administrator cannot remove a departed owner) | Disabling is containment and is free; another owner or a support action removes the role; stated in the guide |

### Open points (raised to the maintainer; none blocks the sprint)

| # | Question | Recommendation |
|---|---|---|
| O-1 | What distinguishes `owner` from `tenant-admin`, and who is the first owner? | As designed (D-2, D-3): `owner` is everything by construction, `tenant-admin` everything but the owner-only permissions (today `support_action:end`); `createTenant` makes an owner; the migration makes current administrators owners. The alternative, two roles with the same permissions, meets AZ-03 by name only |
| O-2 | The exit says "cannot read a tenant's usage or audit without a recorded support action". Should a support action be able to carry `usage:read`, and later `audit:read`? | `usage:read`: never (ADR 0054, AZ-06); the condition is tested in its strong form: not without an action, and not with one. `audit:read`: decide in 6.2, where the permission is born; the attribute `Support` is the switch. The one path to a tenant's data is making an identity under a recorded action, which is the recovery of AZ-09 |
| O-3 | Under a support action, may the platform give any tenant role and issue a key for any service account (D-9)? | Yes: it is what recovery needs, and it is recorded in the tenant's log with the action. The alternative is a dedicated operation that can only issue a key for an owner's account; it is narrower, and leaves "its users, roles or keys" of AZ-09 unmet for anything but recovery |
| O-4 | Which operations require a reason? The design names nine (§5.8); two of them change a v0.1 contract | Confirm the nine. A wider list (every disable and revoke) makes automation carry boilerplate reasons, which devalues them; the construct lets any operation be added later. Limit-rule writes take an optional reason, not a required one: integrators write limits from their back ends (API-06) |
| O-5 | Split the time zone and the active currency into `tenant_setting:write` (5.3 O-4, 5.4 O-7)? | Yes, now: after custom roles exist the split would change what tenants' roles mean. Cost: keys scoped to `limit:write` or `price_list:write` lose the two settings |
| O-6 | Contents of the roles the requirements only name: `credential-manager` without `credential:use`; `auditor` without usage and prices; `developer` with key issuing and usage; `platform-auditor` without tenant credentials | Confirm the table of §5.2; each line is one line of the golden file and can change without a design change |
| O-7 | `support.enabled` on by default; default and maximum duration 1 h; the absolute cap of 24 h in the table | Confirm. Off by default would leave a new installation without a recovery path, and AZ-09 words the switch as a way to disable |
| O-8 | The tenant's owner can end a support action (D-10) | Yes: the platform asked to come in; the tenant can say that it is enough. It costs one operation |
| O-9 | Custom role names: built-in names and the prefixes `platform-` and `pandokos-` are refused | Confirm; it is what lets the product add roles later without a silent collision |
| O-10 | Removing a role from somebody needs the same permissions as giving it; containment (disable, revoke) needs none | Confirm (R-11) |
| O-11 | A platform key may hold `platform-auditor` | Yes: a read-only key for inventory jobs; one member of one schema |
| O-12 | Refused grants are logged, not audited | Not now: AUD-01 lists authentication failures and changes, and the tenant cannot read its log before 6.2; decide with the read side |
| O-13 | 4.1 O-5, scopes per subject mapping | Closed without building: a custom role on a dedicated service account, and the guard on `createSubjectMapping` |

### Maintainer decisions on the open points (2026-11-22)

| # | Decision |
|---|---|
| O-1 | As D-2/D-3: `owner` holds every tenant permission, `tenant-admin` is that minus owner-only permissions; the first service account of a tenant is `owner` |
| O-2 | Strong form: no platform role carries `usage:read`, not even under a support action; `audit:read` under support is decided in 6.2 through the `Support` attribute |
| O-3 | Yes: under a support action the platform grants with the tenant's authority, so a tenant without administrators can be recovered |
| O-4 | Confirmed, including the required reason on `suspendTenant` and `reactivateTenant`: a breaking change of the v0.1 contract, allowed before 1.0 and recorded in the release notes of v0.2.0. Limit-rule reasons stay optional |
| O-5 | Yes: `tenant_setting:write` now; keys scoped to `limit:write` or `price_list:write` lose the two settings (release notes) |
| O-6 | Role contents confirmed as designed |
| O-7 | Confirmed: `support.enabled` true by default, 1 h default duration, 24 h hard cap |
| O-8 | Yes: the owner can end a support action |
| O-9 | Reserved names and prefixes confirmed |
| O-10 | Confirmed: removing a role needs the permissions of giving it; containment is free |
| O-11 | Yes: platform keys may hold `platform-auditor` |
| O-12 | Refused grants are logged, not audited; revisited in 6.2 |
| O-13 | Closed without building: no scopes per subject mapping |

### Maintainer decisions on the review findings (2026-11-22)

| Finding | Decision |
|---|---|
| A platform password reset gives a session of a tenant member | Refused with `escalation_refused` when the user's permissions in any of its tenants go beyond the caller's; the way to recover such an account is the host command `pandokos user reset-password` |
| `updateTokenIssuer` changes what a tenant issuer trusts | A grant: changes of `issuer`, `audience`, `jwks_uri`, `algorithms`, `subject_claim`, `required_claims` and re-enabling need the permissions of every service account mapped under it. `updatePlatformIssuer` stays unguarded, an accepted residual risk recorded in ADR 0061 |
| A missing custom role of a group rule stops the start | Skipped with one start warning naming the rule position; malformed, wrong-scope and `inference` rules still stop the start |
| `duration_seconds: 0` | Absent or 0 is the default, `support.max_duration`; 1 to 59 is refused |
| Known gap | A membership or role gained while a platform-issued password is still pending (until changed or expired) is not checked; to be closed in sprint 10.2, e.g. by refusing or invalidating a pending platform-issued password when a membership or role is added |
