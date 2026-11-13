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
| `virtual_key:read` | tenant | Reading and listing virtual keys (metadata) |
| `virtual_key:write` | tenant | Issuing a virtual key |
| `virtual_key:revoke` | tenant | Revoking a virtual key |
| `model:invoke` | tenant | Calling a model through the inference listener |

Scope *platform*: held by platform principals only. Scope *tenant*: held by principals of that
tenant only, and checked against the tenant of the request. Scope *self*: the caller itself.

## Built-in roles

Roles are defined in code and cannot be changed; `role_assignment.role` holds a name.

| Role | Held by | Permissions |
|---|---|---|
| `platform-admin` | platform principals | `identity:read`, `platform_key:read`, `platform_key:revoke`, `platform_key:write`, `tenant:read`, `tenant:suspend`, `tenant:write` |
| `tenant-admin` | service accounts | `identity:read`, `model:invoke`, `service_account:read`, `service_account:write`, `virtual_key:read`, `virtual_key:revoke`, `virtual_key:write` |
| `inference` | service accounts | `model:invoke` |

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
