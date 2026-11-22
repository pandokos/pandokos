# Permissions and built-in roles

A permission is a `resource:action` pair defined by the product. Every operation of the
administration API and every route of the inference listener names exactly one permission. The
default is deny: a request is allowed only when a role of the caller grants the permission.

## Permissions

| Permission | Scope | Allows |
|---|---|---|
| `identity:read` | self | Reading one's own identity, roles and permissions |
| `tenant:read` | platform | Reading and listing tenants |
| `tenant:write` | platform | Creating a tenant with its first administrator; changing its name and display name |
| `tenant:suspend` | platform | Suspending and reactivating a tenant |
| `platform_key:read` | platform | Reading and listing platform keys (metadata) |
| `platform_key:write` | platform | Issuing a platform key |
| `platform_key:revoke` | platform | Revoking a platform key |
| `service_account:read` | tenant | Reading and listing service accounts and their roles |
| `service_account:write` | tenant | Creating, changing, disabling and enabling service accounts; assigning roles |
| `credential:read` | tenant | Reading and listing provider credentials (metadata, never the secret) |
| `credential:write` | tenant | Registering a credential; changing its label, description and expiry; disabling and enabling it; choosing the default |
| `credential:rotate` | tenant | Replacing the secret of a credential |
| `credential:delete` | tenant | Revoking and deleting a credential |
| `credential:use` | tenant | Having one's model calls authenticated to the provider with a credential of the tenant; the vault checks it when asked to open a credential |
| `tenant_credential:read` | platform | Reading and listing the credentials of a named tenant (metadata) |
| `tenant_credential:revoke` | platform | Disabling or revoking a credential of a named tenant, with a reason |
| `virtual_key:read` | tenant | Reading and listing virtual keys (metadata) |
| `virtual_key:write` | tenant | Issuing a virtual key |
| `virtual_key:revoke` | tenant | Revoking a virtual key |
| `model:invoke` | tenant | Calling a model through the inference listener: every operation of a family (`chat.completions`, `responses` and `embeddings` for `openai`; `messages` and `messages.count_tokens` for `anthropic`) needs the same pair, `model:invoke` and `credential:use` |
| `catalog:read` | platform | Reading providers, endpoints, models, model grants, price versions and price imports |
| `catalog:write` | platform | Creating, changing and deleting providers, endpoints and models; granting models to tenants and revoking grants |
| `catalog:price` | platform | Setting and removing price overrides; running a price import |
| `model:read` | tenant | Listing the models available to the tenant, with their prices; reading the tenant's model restriction; the capability check |
| `model:restrict` | tenant | Setting and clearing the tenant's model restriction |
| `usage:read` | tenant | Listing the tenant's usage records |
| `token_issuer:read` | tenant | Reading and listing the tenant's trusted token issuers, and the platform-level issuers that list the tenant |
| `token_issuer:write` | tenant | Registering a trusted token issuer; changing, disabling and enabling it |
| `token_issuer:delete` | tenant | Deleting a trusted token issuer together with its subject mappings |
| `subject_mapping:read` | tenant | Reading and listing the tenant's subject mappings |
| `subject_mapping:write` | tenant | Mapping a subject of a token issuer to a service account of the tenant. This is as sensitive as `virtual_key:write`: the mapping gives an external identity the roles of the service account |
| `subject_mapping:delete` | tenant | Deleting a subject mapping |
| `platform_issuer:read` | platform | Reading and listing platform-level token issuers and their tenant lists |
| `platform_issuer:write` | platform | Registering, changing, disabling and enabling a platform-level token issuer; adding and removing the tenants it may act for |
| `platform_issuer:delete` | platform | Deleting a platform-level token issuer with its tenant list and the subject mappings that name it |
| `limit:read` | tenant | Reading and listing the tenant's limit rules, the platform ceilings that apply to it and its limit events, and reading the tenant's time zone (`getTenantTimeZone`) |
| `limit:write` | tenant | Creating and changing a limit rule of the tenant, and setting the tenant's time zone (`setTenantTimeZone`: it decides when quotas and budgets reset, and a principal that may write rules may already remove them) |
| `limit:delete` | tenant | Deleting a limit rule of the tenant |
| `platform_limit:read` | platform | Reading and listing the ceilings the platform sets for tenants |
| `platform_limit:write` | platform | Creating and changing the ceilings the platform sets for tenants |
| `platform_limit:delete` | platform | Deleting a ceiling the platform sets for tenants |
| `price_list:read` | tenant | Reading the tenant's price lists and their versions, and the tenant's active currency |
| `price_list:write` | tenant | Adding a price version to one of the tenant's price lists; choosing the tenant's active currency |
| `price_list:delete` | tenant | Removing a price version of the tenant's lists that has not started |
| `user:read` | platform | Reading and listing the users that the platform manages, with their platform roles |
| `user:write` | platform | Creating and changing such users; disabling and enabling them; resetting their password; unlocking them; ending their sessions; assigning platform roles |
| `member:read` | tenant | Reading and listing the members of the tenant and their roles |
| `member:write` | tenant | Adding and removing members and setting their roles; creating local accounts that the tenant manages, and for those: disabling, enabling, resetting the password, unlocking, ending sessions |
| `session:self` | self | Signing out, changing one's own password, listing and ending one's own sessions |

The six limit permissions are held by two roles: `tenant-admin` has `limit:read`, `limit:write` and
`limit:delete`; `platform-admin` has `platform_limit:read`, `platform_limit:write` and
`platform_limit:delete`. The role `inference` has none. A platform principal cannot write a tenant's own
rules and a tenant principal can neither read nor write platform rules beyond the ceilings that apply to
its own tenant (`listTenantPlatformLimits`, under `limit:read`). Custom roles are not yet possible
(sprint 4.3), so these are the only holders ([limits guide](../operations/limits.md)). The platform sets a tenant's time zone with `tenant:write` (`createTenant`, `updateTenant`). No permission was added in sprint 5.3. Sprint 5.4 adds three, `price_list:read`, `price_list:write` and `price_list:delete`, all tenant-scoped and held by `tenant-admin`; the active currency is chosen with `price_list:write`. **No platform permission reads or writes a tenant's usage records, limit events or price lists**, and no platform role is a usage viewer ([ADR 0054](../adr/0054-usage-data-is-the-tenants.md)): what a platform principal reads of a tenant is its setting (the active currency on the `Tenant` resource), never what it consumes or pays.

Scope *platform*: held by platform principals only. Scope *tenant*: held by principals of that
tenant only, and checked against the tenant of the request. Scope *self*: the caller itself.

## Built-in roles

Roles are defined in code and cannot be changed; `role_assignment.role` holds a name.

| Role | Held by | Permissions |
|---|---|---|
| `platform-admin` | platform principals and users without a tenant | `catalog:price`, `catalog:read`, `catalog:write`, `identity:read`, `platform_issuer:delete`, `platform_issuer:read`, `platform_issuer:write`, `platform_key:read`, `platform_key:revoke`, `platform_key:write`, `platform_limit:delete`, `platform_limit:read`, `platform_limit:write`, `tenant:read`, `tenant:suspend`, `tenant:write`, `tenant_credential:read`, `tenant_credential:revoke`, `user:read`, `user:write` |
| `tenant-admin` | service accounts and members of a tenant | `credential:delete`, `credential:read`, `credential:rotate`, `credential:use`, `credential:write`, `identity:read`, `limit:delete`, `limit:read`, `limit:write`, `member:read`, `member:write`, `model:invoke`, `model:read`, `model:restrict`, `price_list:delete`, `price_list:read`, `price_list:write`, `service_account:read`, `service_account:write`, `subject_mapping:delete`, `subject_mapping:read`, `subject_mapping:write`, `token_issuer:delete`, `token_issuer:read`, `token_issuer:write`, `usage:read`, `virtual_key:read`, `virtual_key:revoke`, `virtual_key:write` |
| `inference` | service accounts | `credential:use`, `model:invoke` |

## Decision

The first rule that applies wins:

1. a permission that is not in the catalogue is denied;
2. a platform permission is allowed to a platform principal, or a user without a tenant, that holds it,
   whatever the target;
3. for a service account, and for a user with a tenant: no tenant is denied, and a tenant that is not
   `active` is refused with `tenant_suspended` (403);
4. a tenant permission is denied to a platform principal (a platform role implies no tenant
   permission); a request for another tenant is `not_found` (404), whether or not the caller holds
   the permission and whether or not that tenant exists; otherwise the permission must be held;
5. a self permission is allowed when held. Every user holds `identity:read` and `session:self`
   whatever its roles; `session:self` is never held by a key or a token, which have no session.

A user is identified by its session, not by a tenant: the principal is built for each request, from the
platform roles of the user for a platform permission and from its membership of the tenant of the path
for a tenant permission (no membership is `not_found`). A role counts for a principal when its scope
fits: a platform role for a principal without a tenant that is not a service account, a tenant role for a
principal with a tenant that is not a platform key. A role may be given to a user only by a principal
that holds every permission of it.

The permissions a principal holds are the union of its roles' permissions, narrowed to the scopes
of the key when the key has any. A role that is unknown, or of the other kind, grants nothing.

A platform administrator holds no tenant permission, `credential:*` included, and no permission that
returns or replaces a secret: the platform credential operations name a tenant, return metadata only
and disable or revoke with a reason. A call to the inference listener needs both `model:invoke` and `credential:use`, and both are decided
before the request body is read: a virtual key whose `scopes` leave only `model:invoke` authenticates and
is refused with `forbidden` (see the [gateway guide](../operations/gateway.md)).

Issuing a virtual key with a model list (`models`) needs only `virtual_key:write`. The catalogue is
administered by platform principals only; a tenant sees the models available to it with `model:read`
and narrows itself with `model:restrict`, and neither lets it read or change another tenant's grants.

Token issuers follow the same split. A tenant administrator registers issuers for its own tenant and
maps their subjects to its own service accounts, and nothing else: an issuer, a mapping or a service
account of another tenant, and a platform-level issuer that does not list the tenant, are
indistinguishable from one that does not exist. Platform-level issuers, which let one issuer act for
several tenants, are administered by platform principals only, and a platform principal holds no
`subject_mapping:*` permission: the tenant decides which of its service accounts a token subject may
act as.
