# 0056. Users, memberships, and how a session becomes a principal

- Status: proposed
- Date: 2026-11-22

Refines [ADR 0006](0006-human-authentication-external-oidc.md),
[ADR 0007](0007-authorization-rbac.md) and [ADR 0031](0031-authorization-core-and-provisioning.md).
Requirements IAM-08 to IAM-12, IAM-22, TEN-02, TEN-04, AZ-01, AZ-02, PRV-01.

## Context

ADR 0006 and ADR 0007 name users, memberships and role assignments without giving them a model.
Sprint 4.2 is the first to store a person. Three questions have to be answered together because each
constrains the others: what is a user and who manages it, where do roles of people live, and how does
a session, which names a person and no tenant, become the principal that `authz.Decide` takes.

## Decision

1. **D-1. A user is global and has one sign-in method and one managing side.** `app_user` is a
   person: display name, e-mail address, state (`active`, `disabled`), the sign-in method (`local` or
   `oidc`, exactly one, immutable) and who manages the account (`tenant_id` null: the platform; a
   value: that tenant; immutable). A `local_account` (username, Argon2id hash) or a `user_identity`
   (issuer, subject) hangs on the user; a composite foreign key on (`id`, `sign_in`) makes the two
   exclusive without a trigger. **The managing rule:** a user that a tenant manages is a member of
   that tenant only and holds no platform role (two triggers); only the administrators of the managing
   side reset its password, disable it, unlock it or end its sessions. A person who works in several
   tenants, or on the platform, is managed by the platform; a federated user always is, because the
   identity provider is the platform's.
2. **D-2. Roles of people live in tables of their own.** `membership` and `membership_role`
   (tenant-scoped, forced row-level security, the one policy `tenant_isolation`) say who is a member of
   a tenant and with which roles; `platform_role_assignment` holds the platform roles of users;
   `role_assignment` stays the table of service accounts, which the gateway reads. `membership_directory`
   (user, tenant), kept by a trigger and readable only through the definer function `user_tenants`, is
   the one path from a user to its tenants across the tenant policy. `platform_user_lookup` finds one
   platform-managed user for a tenant's session that wants to add it as a member. A user can hold
   `tenant-admin` in a tenant or `platform-admin` on the platform in this sprint; `inference` is refused
   for a member, because a user cannot call a model. Whoever assigns a role must hold every permission
   of it (`authz.Assignable`).
3. **D-3. The principal of a session is built for each request.** A session identifies a user, not a
   tenant. For a platform permission the principal is `Kind: user` without tenant and with the rows of
   `platform_role_assignment`; for a tenant permission the tenant is the one of the path, the member's
   roles are read in that tenant's session and no membership is `404 not_found`, the answer for a
   tenant that does not exist; for a `self` permission it is the user with its platform roles. `Role.Kind`
   becomes `Role.Scope`. Every user holds `identity:read` and `session:self` whatever its roles;
   `session:self` is refused for keys and tokens. A session whose account must change its password may
   call `getMe`, `changePassword` and `signOut` only (`403 password_change_required`), decided in the
   hook before `Decide`. The order of the five rules of ADR 0031 does not change.
4. **D-4. The reading of IAM-08 for users.** The subject that maps to a user is the subject of the ID
   token of the configured provider (ADR 0058), by an identity link that an administrator made or by a
   group rule; any other subject is refused. A bearer token of a trusted issuer never acts as a user: on
   the administration listener it maps to a service account, as on the inference listener.
5. **Visibility.** The platform scope sees every user (sign-in happens there, before any tenant is
   known). A tenant's session sees the users it manages and its members and writes only the former. An
   unscoped session sees nothing. The gateway and worker roles hold no privilege on any table of this
   sprint (IAM-22 as a property of the database). Platform operations show platform-managed users only.

## Consequences

- The membership is read on every request of a tenant operation, in that tenant's session: removing a
  member, disabling a user or removing a role takes effect with the next request.
- A tenant administrator cannot touch platform-managed members, and a platform administrator cannot
  touch a tenant's users before the support action of sprint 4.3 (or the host command).
- Usernames are unique in the installation, so a conflict tells a tenant administrator that a name is
  taken somewhere. The guide advises e-mail-shaped usernames.
- Sprint 4.3 adds roles, effective permissions per tenant in `/v1/me` and the support action at the one
  place in the hook where "no membership" is decided.
- A user is never deleted before sprint 6.3 (audit rows name user IDs).

## Alternatives considered

**One table of assignments for users and service accounts.** It puts role rows about people in a place
the gateway reads, and the isolation harness allows one policy per tenant table, so the user column
could not be hidden from the gateway.

**Users per tenant.** A person in two tenants would be two accounts, and a username would need the
tenant at sign-in.

**A tenant chosen at sign-in and stored in the session.** The tenant would stop being explicit in the
request, against IAM-10.

**Bearer tokens that act as users.** ADR 0006 rejected bearer tokens of people on the administration
API, and IAM-22 says only sessions carry people.

## History

- 2026-11-22: proposed with sprint 4.2. Maintainer decisions of 2026-11-21: a bearer token never acts
  as a user (O-1); sign-in events of a tenant-managed user go to its tenant's log (O-3).
