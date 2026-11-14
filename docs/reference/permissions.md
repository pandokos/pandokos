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
| `model:invoke` | tenant | Calling a model through the inference listener: every operation of a family (`chat.completions`, `responses` and `embeddings` for `openai`) needs the same pair, `model:invoke` and `credential:use` |
| `catalog:read` | platform | Reading providers, endpoints, models, model grants, price versions and price imports |
| `catalog:write` | platform | Creating, changing and deleting providers, endpoints and models; granting models to tenants and revoking grants |
| `catalog:price` | platform | Setting and removing price overrides; running a price import |
| `model:read` | tenant | Listing the models available to the tenant, with their prices; reading the tenant's model restriction |
| `model:restrict` | tenant | Setting and clearing the tenant's model restriction |
| `usage:read` | tenant | Listing the tenant's usage records |

Scope *platform*: held by platform principals only. Scope *tenant*: held by principals of that
tenant only, and checked against the tenant of the request. Scope *self*: the caller itself.

## Built-in roles

Roles are defined in code and cannot be changed; `role_assignment.role` holds a name.

| Role | Held by | Permissions |
|---|---|---|
| `platform-admin` | platform principals | `catalog:price`, `catalog:read`, `catalog:write`, `identity:read`, `platform_key:read`, `platform_key:revoke`, `platform_key:write`, `tenant:read`, `tenant:suspend`, `tenant:write`, `tenant_credential:read`, `tenant_credential:revoke` |
| `tenant-admin` | service accounts | `credential:delete`, `credential:read`, `credential:rotate`, `credential:use`, `credential:write`, `identity:read`, `model:invoke`, `model:read`, `model:restrict`, `service_account:read`, `service_account:write`, `usage:read`, `virtual_key:read`, `virtual_key:revoke`, `virtual_key:write` |
| `inference` | service accounts | `credential:use`, `model:invoke` |

## Decision

The first rule that applies wins:

1. a permission that is not in the catalogue is denied;
2. a platform permission is allowed to a platform principal that holds it, whatever the target;
3. for a service account: no tenant is denied, and a tenant that is not `active` is refused with
   `tenant_suspended` (403);
4. a tenant permission is denied to a platform principal (a platform role implies no tenant
   permission); a request for another tenant is `not_found` (404), whether or not the caller holds
   the permission and whether or not that tenant exists; otherwise the permission must be held;
5. a self permission is allowed when held.

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
