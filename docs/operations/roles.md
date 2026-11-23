# Roles, custom roles and support actions

Pandokos decides every request by permissions, and a role is a list of permissions. This page is for
the operator and for a tenant's administrator: the ten built-in roles, the first owner, custom roles
that a tenant writes itself, the rule that stops anybody from handing out a permission they do not
hold, what `GET /v1/me` tells a client, the support actions through which the platform acts inside a
tenant, how to recover a tenant that lost its administrators, the nine operations that need a reason,
and group rules that name roles. It applies from sprint 4.3
([ADR 0060](../adr/0060-built-in-roles-custom-roles-and-the-rule-against-escalation.md),
[ADR 0061](../adr/0061-support-actions-and-reasons-how-the-platform-acts-inside-a-tenant.md)).
The tables of permissions and roles are generated from the code and are in the
[permission reference](../reference/permissions.md); the operations are in the
[administration API reference](../reference/admin-api.md); the settings are in the
[configuration reference](configuration.md).

## 1. The ten roles

Eight roles are for a tenant and two for the platform. They are defined in code and cannot be changed.
Every role also holds `identity:read`. A person can hold every role but `inference`; every tenant
role can be given to a service account.

| Role | Scope | What it is for |
|---|---|---|
| `owner` | tenant | Everything in the tenant, now and when permissions are added; the only role that ends a support action of the platform |
| `tenant-admin` | tenant | Day-to-day administration: every tenant permission except those kept for owners (today `support_action:end`) |
| `credential-manager` | tenant | Provider credentials: read, create, rotate, delete; reads the models. Does not hold `credential:use` |
| `usage-viewer` | tenant | Reads usage, prices, limits and models |
| `auditor` | tenant | Reads how the tenant is configured, who may do what and what the platform did in it, and the tenant's audit log (`audit:read`); reads neither usage nor prices, so the rates in the audit details are withheld from it |
| `developer` | tenant | Issues and revokes virtual keys and calls models; reads usage and limits |
| `inference` | tenant | Calls models: `model:invoke` and `credential:use`. A workload role; a person cannot hold it |
| `read-only` | tenant | Every tenant permission whose action is `read` |
| `platform-admin` | platform | Administers the installation: every platform permission |
| `platform-auditor` | platform | Reads the installation: tenants, keys, catalogue, issuers, limits, users, support actions and the platform's audit log (`platform_audit:read`); reads no credential of a tenant |

`credential-manager` has no `credential:use` because that permission is for callers and has an
effect only together with `model:invoke`. `developer` issues keys, but only for service accounts that
hold no more than it does (section 4). A member that holds `developer` or a custom role with
`model:invoke` gains nothing from it in a session: the inference listener never reads sessions.

**Reading the audit logs** ([audit guide](audit.md)). **Events and destinations** ([events guide](events.md)). `event_destination:read` is held by `owner`, `tenant-admin`,
`auditor` and `read-only`; `event_destination:write` and `event_destination:delete` by `owner` and `tenant-admin`;
`event:read`, the pull export of the tenant's events (usage data), by `owner`, `tenant-admin`, `usage-viewer` and
`read-only`. `developer` and `credential-manager` hold none. `platform_event:read`, the export of the platform's
events, is held by `platform-admin` and `platform-auditor`; no platform role reads a tenant's event.

`audit:read` (tenant scope) is held by `owner`,
`tenant-admin` and `auditor`; it is not held by `read-only` (which excludes it explicitly), by
`usage-viewer`, `developer`, `credential-manager` or `inference`, and **it cannot be carried by a support
action**: the log records price rates and budget amounts, so what a platform administrator did inside a
tenant is read in the platform's own log. `platform_audit:read` (platform scope) is held by
`platform-admin` and `platform-auditor`. A custom role may hold `audit:read`. Without `price_list:read`
the detail of a price action is withheld from a reader.

A platform key may hold `platform-auditor`: `platform-key create --role platform-auditor` makes a
read-only key for an inventory job. The default is `platform-admin`.

### Owner and administrator

`owner` holds every tenant permission by construction. `tenant-admin` is a list that differs from it by
the permissions kept for owners. Because of the rule of section 4 the difference protects itself: an
administrator cannot make an owner, cannot take the role from one, cannot reset an owner's password and
cannot issue a key for an owner's service account.

`createTenant` gives the first service account of a tenant the role `owner`. Migration 0014 gives
`owner` to every service account and every member that holds `tenant-admin` through an administrator's
hand, so that every existing tenant has an owner and nobody holds less than before: until this sprint
`tenant-admin` held every tenant permission. They keep `tenant-admin`. Rows made by group rules are left alone (the rules decide
them again at the next sign-in). The one permission that is new to those administrators, now owners, is
`support_action:end`. Whoever runs the upgrade should read the [database guide](database.md#19-grants-of-migration-0014).

## 2. Custom roles

A tenant defines its own roles from the permission catalogue. Every tenant permission is available,
the six limit permissions and `price_list:read` alone included. A platform permission, a self
permission or an unknown name is refused (`400 invalid_request` on `permissions`).

| Rule | Value |
|---|---|
| Name | `^[a-z][a-z0-9-]{0,62}$`, unique in the tenant, immutable |
| Reserved | the ten built-in names and anything starting with `platform-` or `pandokos-` |
| Permissions | 1 to 64 tenant permissions, stored sorted and without duplicates |
| Number | at most 100 per tenant (`409 conflict` beyond) |

```sh
pandokos role create --tenant acme identity-manager \
  --description "Manages members and the roles they hold" \
  --permission member:read --permission member:write \
  --permission role:read --permission role:write
pandokos role list --tenant acme
pandokos role get --tenant acme identity-manager
```

A role is addressed by its name. `getRole` returns it with the number of members and of service
accounts that hold it. `updateRole` changes the description and the permissions (the whole set is
replaced). `createRole` and `openSupportAction` answer 201 and take an `Idempotency-Key`.

**Giving a role.** A role named in `roles` of a service account or of a member is a built-in tenant
role (not `inference` for a member) or an **active** custom role of the tenant; anything else is
`400 invalid_request` on `roles`. Custom roles go to members and to service accounts alike:

```sh
pandokos member update --tenant acme alice --role identity-manager
pandokos service-account update --tenant acme billing-bot --role price-reader
```

**Deactivating and activating** (`deactivateRole`, `activateRole`, both with a required reason). An
inactive role gives its holders nothing and cannot be assigned; the assignments stay. For a member the
change holds from the next request. For a key or a token it holds when the gateway's cache is flushed by
the notification that every change of a role sends, and at the latest at the bound of that cache
([ADR 0030](../adr/0030-keys-format-storage-lookup.md)). A role in the target state is answered
`200` unchanged.

**Deleting** (`deleteRole`, `204`). A role that a member or a service account still holds is refused
with `409 role_in_use`: take it from them or deactivate it first. A role that nobody holds is deleted
and its name is free again.

**The bound of propagation to keys.** The permissions of a custom role reach the gateway through its
cache of key principals. A change of the role is visible on the inference listener within the cache's
own bound; a key that was cached when the role was edited keeps what it had until then. A role that
is missing is not an error: a name without a role gives nothing, because the default is deny.

### Three worked roles

**An identity manager** handles who is in the tenant and what they hold, and nothing else:
`member:read`, `member:write`, `service_account:read`, `service_account:write`, `role:read`,
`role:write`. It can only give what it holds (section 4): it cannot make an owner or an administrator.

**A limits manager** sets the tenant's budgets and rate limits and reads usage against them:
`limit:read`, `limit:write`, `limit:delete`.

```sh
pandokos role create --tenant acme limits-manager \
  --permission limit:read --permission limit:write --permission limit:delete
```

**A price reader** reads the tenant's price lists and nothing else: `price_list:read`.

```sh
pandokos role create --tenant acme price-reader --permission price_list:read
```

## 3. Splitting the two tenant settings

The time zone of the tenant (`setTenantTimeZone`) and its active currency (`setActiveCurrency`) are
set with the permission `tenant_setting:write`. They were set with `limit:write` and `price_list:write`.
Reading stays under `limit:read` and `price_list:read`. A principal that may write limit rules or prices
does not also decide when every budget resets and in which currency it is counted. `owner` and
`tenant-admin` hold the new permission. **A key whose scope list names `limit:write` or
`price_list:write` and not `tenant_setting:write` can no longer change the two settings**; add the
permission to its scopes (a new key; scopes are fixed at creation).

## 4. The rule against escalation

Nobody gives, or takes over, a permission they do not hold. One function decides: the caller must hold
every permission of what it confers. The scope list of a key is part of what it holds, so a restricted
key grants only inside its restriction. A request that would give or take a permission the caller does
not hold is refused with `403 escalation_refused`, nothing is changed and no audit row is written (the
refusal is logged without a tenant, role or permission).

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
| `updateTokenIssuer` | when it changes `issuer`, `audience`, `jwks_uri`, `algorithms`, `subject_claim` or `required_claims`, or enables the issuer: the permissions of every service account mapped under it |
| `resetMemberPassword`, `resetUserPassword` | the permissions of the person whose password is reset; for `resetUserPassword`, its platform roles and its permissions in every tenant it is a member of |

- **Roles that stay are not checked.** `roles` replaces the whole set; only the difference with the
  current set is conferred or removed by the caller, so an administrator can edit a member that also
  holds a role it lacks, without touching that role.
- **Removal is covered.** Taking a role needs the permissions of giving it: an administrator cannot
  strip an owner. A departed owner is removed by another owner, or under a support action (section 7).
- **Containment is free.** Disabling, enabling and unlocking an identity, ending sessions, revoking a
  key and deleting a subject mapping give nobody anything. Whoever holds their permission may use them
  on any identity of the tenant, so a principal with `virtual_key:revoke` alone can revoke a leaked
  key of an administrator.
- **An unknown role is still `400`.** `escalation_refused` is the answer for a role that exists and that
  the caller may not hand out.
- **A password is a grant.** A new password gives the caller a sign-in as that person. A platform
  user that is a member of a tenant holds that tenant's permissions, which a platform administrator
  does not, so `resetUserPassword` refuses such a user with `403 escalation_refused`, with or without
  a support action. The user's own recovery is the host command `pandokos user reset-password`, run
  by whoever operates the host. **Known gap:** the check is made when the password is issued. A
  membership, a role or a reactivated custom role that the user gains in a tenant while a
  platform-issued password (from `resetUserPassword` or `createUser`) is still pending, until it is
  changed or expires, is not checked; whoever received that password could sign in with what was
  gained. It is to be closed in sprint 10.2. Until then, give a membership or a role to a platform user
  only after its platform-issued password has been changed.
- **Token mappings are grants.** A subject mapping gives a subject every role of a service account, so
  a principal cannot map a subject to an account stronger than itself. To give a token less, put a
  custom role on a dedicated service account. For the same reason, changing what an issuer trusts is a
  grant to the accounts mapped under it; its name, its description and disabling it are free. A
  platform-level issuer is the platform's: its changes are not guarded, an accepted residual exposure
  stated in the [security model](../architecture/security-model.md).

For a client, `escalation_refused` means: do not retry; the caller is not allowed to hand out that
role or key. Ask somebody who holds more, or change what is requested.

## 5. What `/v1/me` returns, and how a client uses it

For a user, each member of `memberships` carries `permissions`: the effective permissions in that
tenant. They come from the function that builds the principal of every tenant request, so what the API
reports and what it enforces cannot differ. A suspended tenant is listed with its state and with the
permissions the member would have: the state is what blocks, and the client sees both. For a platform
principal `support_actions` lists its open support actions (tenant, permissions, expiry); `permissions`
at the top level is what the caller holds outside any tenant. `GET /v1/permissions` (`listPermissions`)
and `GET /v1/roles` (`listBuiltinRoles`) return the catalogue and the ten roles to any authenticated
caller, which is what a client needs to draw a role editor.

A client uses `permissions` to decide what to show: a button for creating a role appears when
`role:write` is there. **Hiding is not the control.** The server decides every request again, whatever
the client showed; a client that shows a button the caller may not use gets `403 forbidden` or
`403 escalation_refused`, and a client that hides one the caller may use costs only convenience.

## 6. Support actions

A platform administrator holds no tenant permission. A **support action** is the one door through which
the platform acts inside a tenant, for a stated reason, for a bounded time, in view of the tenant.

**Opening.** `openSupportAction` (platform permission `tenant_support:open`):

```sh
pandokos tenant-support open --tenant acme \
  --reason "ticket 4711: the tenant lost its administrators" \
  --permission service_account:read --permission virtual_key:write \
  --duration 30m
```

| Member | Rule |
|---|---|
| `reason` | required, 1 to 500 characters, not only white space |
| `permissions` | 1 to 11 names from the list below |
| `duration_seconds` | 60 to `support.max_duration`; absent or `0` is the default, `support.max_duration` (1 h); 1 to 59 is refused |

One transaction writes the action, the event `support_action.open` in the tenant's audit log and the
same event in the platform's. An open action of the same actor in the same tenant is closed first: there
is one per actor and tenant (two openings of one actor at the same moment: one of them is `409
conflict`; the audit detail `replaces` names only an action that was open, not one that had expired).
With `support.enabled: false` the answer is `403 support_disabled` and
nothing is written.

**Permissions a support action can carry**: `service_account:read`, `service_account:write`,
`virtual_key:read`, `virtual_key:write`, `virtual_key:revoke`, `member:read`, `member:write`,
`role:read`, `role:write`, `role:delete`, `support_action:read`, `event_destination:read`. **It never carries** a permission that
reads usage, limit events, events or prices (`event:read`; the only event permission it carries is `event_destination:read`: the state, the URL and the dead letters of a destination, which is what an operator needs to help with a delivery, and no usage), that reads or writes a credential, a limit, a price, an issuer, a
mapping, a model restriction or the tenant's settings, or that calls a model; `openSupportAction`
refuses them before anything is written.

**Using.** While the action is open, the caller's requests for tenant operations in that tenant run as
a support principal with the permissions of the action. Without an open action the answers are those
they always were: `403 forbidden` for a platform key and `404 not_found` for a user that is not a
member. A user that is a member of the tenant keeps its membership: its own roles win. The action is
honoured only while `support.enabled` is true and the actor still holds `tenant_support:open`; an
operation outside the action's list is `403 forbidden`; a suspended tenant is `403 tenant_suspended`
as for everybody.

Under an action the platform acts with the tenant's authority: it may give any tenant role and issue a
key for any service account, when the operation's permission is in the action. That is what recovery
needs. Two limits: a user cannot make **itself** a member of the tenant under its own action
(`409 invalid_state`), and no platform permission can be granted this way. Every write under an action
carries the action's ID in the tenant's audit log (`support_action_id`).

**Closing and ending.** An action ends by expiry (at most 24 h, by constraint), by `closeSupportAction`
(the platform, optional reason, `support_action.close` in both logs), or by `endSupportAction` (the
tenant's owner, `support_action:end`, optional reason, `support_action.end` in the tenant's log).
There is no background job: the next request sees the end. A row is never deleted.

**What the tenant sees.** With `support_action:read` the tenant lists and reads the actions that the
platform opened in it: who (kind, ID, name), why, which permissions, from when to when and how each
ended. The platform reads the same per tenant with `tenant_support:read`. A tenant ends an action it
does not want with `support-action end`. The operations `listSupportActions`, `getSupportAction`,
`endSupportAction`, `listTenantSupportActions` and `getTenantSupportAction` are in the
[reference](../reference/admin-api.md).

**What this means for the platform administrator's role.** A platform administrator reaches a tenant's
data only by making an identity in the tenant for somebody (a key or a member); there is no other path
(the password of a platform user that is a member of a tenant is not reset through the API, see section 4,
apart from the known gap of a pending platform-issued password stated there),
it needs an open action, and each step is a row in the tenant's log with the action and its reason. The
[security model](../architecture/security-model.md) states this as the residual exposure of the role.
`support.enabled: false` removes it together with the recovery (`serve` warns at start).

## 7. Recovering a tenant that lost its administrators

There is no dedicated operation. A platform administrator recovers a tenant in three steps:

1. **Open an action** with the two permissions that recovery needs and the reason:

   ```sh
   pandokos tenant-support open --tenant acme --reason "ticket 4711: recover the owner" \
     --permission service_account:read --permission virtual_key:write
   ```

2. **Give the tenant access again.** List the service accounts (`service-account list --tenant acme`;
   needs `service_account:read`) and issue a key for the owner's account with `key create` (needs
   `virtual_key:write`); the key is shown once, to the administrator, who hands it over. Or add
   `member:write` to the action and make a member with the role `owner` (`member add --new
   --tenant acme --username ... --role owner`): its generated password must be changed at first
   sign-in.
3. **Close the action** with `tenant-support close --tenant acme ACTION`.

The tenant's log then holds `support_action.open`, `virtual_key.create` (or `member.add`) with the
action's ID, and `support_action.close`. The same action with `member:write` is how the platform acts on
the users that a tenant manages (reset, unlock, disable), which the platform could not do before. With
`support.enabled: false` the only way back for a tenant without administrators is the host's database.

## 8. The nine operations that need a reason

The request body must carry a `reason`, 1 to 500 characters and not empty after trimming; it is
recorded in the audit log. A missing or blank reason is `400 invalid_request` on `reason`, from the
generated handler. The CLI makes `--reason` a required flag and makes no call without it.

| Criterion | Operations |
|---|---|
| the platform acts on a tenant | `suspendTenant`, `reactivateTenant`, `disableTenantCredential`, `revokeTenantCredential`, `openSupportAction` |
| the caller obtains another person's credential | `resetUserPassword`, `resetMemberPassword` |
| one call changes what many identities may do | `deactivateRole`, `activateRole` |

Every other operation that takes a reason keeps it optional. The bodies of `createLimit`, `updateLimit`,
`createPlatformLimit` and `updatePlatformLimit` gain an optional `reason`, recorded in the audit row,
so that integrators can write limits from their back ends without boilerplate. A reason is written
to the audit log and to the support action and to nothing else: no log line and no span names it.

## 9. Group rules with roles

`auth.oidc.group_mappings` rules (`<target>=<group>`, see the [sign-in guide](sign-in.md)) may name any
built-in role a user can hold in the scope of the target, and, for a tenant target, a custom role of
that tenant:

```yaml
auth:
  oidc:
    group_mappings:
      - platform:platform-auditor=pandokos-auditors
      - tenant:acme:usage-viewer=acme-finance
      - tenant:acme:identity-manager=acme-people
```

At start `inference`, roles of the wrong scope and malformed names are refused. A rule that names a
custom role its tenant does not have is not refused, since a tenant that deletes the role would
otherwise stop the installation at its next start: `serve` logs `group rule skipped` with the position
of the rule only, and the rule gives nothing until the role exists. At sign-in a custom role that is
missing or inactive is not given, with the same line; the role is read in the transaction that
assigns it, so a role deleted meanwhile is not given either. The operator's configuration
is the grantor here: the rule against escalation does not apply to it.

## 10. What changes for an installation of v0.1

Three contract changes of v0.1 are decided for v0.2.0 and belong in its release notes; the upgrade
notes of the [database guide](database.md#19-grants-of-migration-0014) carry them too:

1. **`suspendTenant` and `reactivateTenant` require a `reason`.** It was optional. A client that sent
   none receives `400 invalid_request` on `reason`; `tenant suspend` and `tenant reactivate` need
   `--reason`.
2. **`tenant_setting:write` sets the time zone and the active currency.** A key scoped to `limit:write`
   or `price_list:write` and not to the new permission loses the two settings (section 3).
3. **The first service account of a tenant is `owner`, not `tenant-admin`**, and existing
   administrators by hand become owners at the migration (section 1).

The other changes are additions: three problem codes (`escalation_refused`, `support_disabled`,
`role_in_use`), sixteen operations, the member `permissions` of `GET /v1/me` and the optional `role`
of `createPlatformKey`.
