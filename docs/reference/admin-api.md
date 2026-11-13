# Administration API

The administration API is described by `api/openapi/admin.v1.yaml`, the source of truth, which is
attached to every release. The Go package `internal/adminapi` (types, router, validation and
client) is generated from it by `make generate`; never edit a generated file.

## Conventions

- Base path `/v1`. Platform resources at the top (`/v1/tenants`, `/v1/platform-keys`), tenant
  resources under the tenant, the caller at `/v1/me`. Identifiers in paths are IDs (UUIDs, or the
  key ID of a key); names are attributes found with the `name` filter. The CLI accepts names.
- Authentication: `Authorization: Bearer <key>`. Nothing else is read.
- JSON only; members are `snake_case`; times are RFC 3339 in UTC. An absent optional member is left
  out of responses. Bodies and queries are strict: an unknown member or parameter is an error.
- Verb sub-resources (`suspend`, `reactivate`, `disable`, `enable`, `revoke`) take an optional
  `{"reason": "..."}`, return the resource and are idempotent.
- `PATCH` changes the members that are present; `valid_from` and `valid_until` accept `null`;
  `roles` replaces the set. Service accounts, keys and tenants are never deleted. A credential is
  revoked and then deleted: `DELETE` on a credential removes the row and returns the deleted
  resource (its last metadata).
- A secret is returned by the create response of a platform key or a virtual key only; no operation
  returns a hash. A provider credential's secret is write-only: it is sent by `createCredential` and
  `replaceCredentialSecret` and never returned, not even by create. A response shows its label,
  `fingerprint` and `hint` instead. For a request body that has a `secret` member, an unknown member
  is reported as the field `body` without naming the member, because the name could be a secret.
- Every response carries `X-Pandokos-Request-Id` and `Cache-Control: no-store`.

## Collections

`limit` (1 to 200, default 50), `cursor`, `sort` and the filters of the document. A response is
`{"items": [...], "next_cursor": "..."}`; `next_cursor` is absent on the last page. The cursor is
opaque and valid only with the sort that made it.

## Problems

Errors are RFC 9457 problems (`application/problem+json`) with a fixed sentence per code:

| `code` | Status | Raised when |
|---|---|---|
| `invalid_request` | 400 | malformed JSON, an unknown or missing member, a broken rule, an unknown or repeated query parameter, a bad cursor |
| `unauthenticated` | 401 | no, malformed, unknown, revoked or expired key, or a service account that is disabled or outside its validity; the same response in every case |
| `forbidden` | 403 | the principal lacks the permission |
| `tenant_suspended` | 403 | the principal's tenant is not active |
| `not_found` | 404 | no route, a malformed identifier, or a resource that does not exist or belongs to another tenant |
| `conflict` | 409 | the name is in use |
| `invalid_state` | 409 | the resource is in a state that does not allow the operation |
| `request_too_large` | 413 | the body exceeds 1048576 bytes |
| `unsupported_media_type` | 415 | the body is not `application/json` |
| `credential_rejected` | 422 | the provider refused the secret when `validate` was asked; nothing was stored |
| `validation_unavailable` | 502 | the provider could not be asked or gave no clear answer when `validate` was asked; nothing was stored |
| `internal` | 500 | an unexpected failure |
| `unavailable` | 503 | the database cannot be reached |

## The accepted subset of OpenAPI 3.1

`internal/apigen` accepts a deliberate subset and fails on anything else: `openapi: 3.1.0`; path
items with `get`, `post`, `patch`, `put`, `delete`; operations with a unique lower-camel-case
`operationId` and a mandatory `x-pandokos-permission`; path parameters that are strings with a
pattern, query parameters that are optional strings, enumerations or integers; flat JSON request
bodies; exactly one 2xx response; every other response `application/problem+json` with the schema
`Problem`; component objects with `additionalProperties: false`. A keyword it does not know is an
error, so the document cannot state a rule the generated code does not enforce.

## Commands

Every operation has one CLI command that only calls the generated client. `TENANT` and
`SERVICE_ACCOUNT` accept an ID or a name.

| Command | Operation |
|---|---|
| `whoami` | `getMe` |
| `tenant list`, `create`, `get`, `update`, `suspend`, `reactivate` | `listTenants`, `createTenant`, `getTenant`, `updateTenant`, `suspendTenant`, `reactivateTenant` |
| `service-account list`, `create`, `get`, `update`, `disable`, `enable` | `listServiceAccounts`, `createServiceAccount`, `getServiceAccount`, `updateServiceAccount`, `disableServiceAccount`, `enableServiceAccount` |
| `key list`, `create`, `get`, `revoke` | `listVirtualKeys`, `createVirtualKey`, `getVirtualKey`, `revokeVirtualKey` |
| `platform-key list`, `create`, `get`, `revoke` | `listPlatformKeys`, `createPlatformKey`, `getPlatformKey`, `revokePlatformKey` |
| `credential list`, `create`, `get`, `update`, `replace`, `disable`, `enable`, `set-default`, `revoke`, `delete` | `listCredentials`, `createCredential`, `getCredential`, `updateCredential`, `replaceCredentialSecret`, `disableCredential`, `enableCredential`, `setDefaultCredential`, `revokeCredential`, `deleteCredential` |
| `tenant-credential list`, `get`, `disable`, `revoke` | `listTenantCredentials`, `getTenantCredential`, `disableTenantCredential`, `revokeTenantCredential` |
| `kek generate --out FILE` | none: a local command that writes a key-encryption key ([vault guide](../operations/vault.md)) |

### Credentials

The tenant operations are under `/v1/tenants/{tenant}/credentials`; they need the `credential:*`
permissions of the tenant. `PUT .../secret` (`replaceCredentialSecret`) replaces the secret; `POST`
`.../disable`, `.../enable`, `.../revoke` and `.../set-default` change the state. Revocation is final and
destroys the sealed secret. The platform operations are under `/v1/tenant-credentials/{tenant}`: they
list and read metadata and disable or revoke with a mandatory `reason`; no platform operation accepts
or returns a secret, and the audit row is written in the tenant's log.

| Operation | Method and path | Permission |
|---|---|---|
| `listCredentials` | `GET /v1/tenants/{tenant}/credentials` | `credential:read` |
| `createCredential` | `POST /v1/tenants/{tenant}/credentials` | `credential:write` |
| `getCredential` | `GET .../credentials/{credential}` | `credential:read` |
| `updateCredential` | `PATCH .../credentials/{credential}` | `credential:write` |
| `deleteCredential` | `DELETE .../credentials/{credential}` | `credential:delete` |
| `replaceCredentialSecret` | `PUT .../credentials/{credential}/secret` | `credential:rotate` |
| `disableCredential`, `enableCredential` | `POST .../{credential}/disable`, `/enable` | `credential:write` |
| `setDefaultCredential` | `POST .../{credential}/set-default` | `credential:write` |
| `revokeCredential` | `POST .../{credential}/revoke` | `credential:delete` |
| `listTenantCredentials` | `GET /v1/tenant-credentials/{tenant}` | `tenant_credential:read` |
| `getTenantCredential` | `GET /v1/tenant-credentials/{tenant}/{credential}` | `tenant_credential:read` |
| `disableTenantCredential`, `revokeTenantCredential` | `POST .../{credential}/disable`, `/revoke` | `tenant_credential:revoke` |

Connection flags: `--server`, `--key-file`, `--ca-file` (variables `PANDOKOS_CLI_SERVER`,
`PANDOKOS_CLI_KEY_FILE`, `PANDOKOS_CLI_CA_FILE`) and `-o text|json`. There is no flag or variable
that holds the key itself, and none that skips certificate verification. `credential create` and `replace` read the secret from `--secret-file FILE` and from nothing
else; no flag or variable holds it. `create` takes the label as an argument and `--provider`,
`--validate`, `--expires-at`, `--description`; the other credential commands take the credential ID and
`--tenant`. `tenant-credential disable` and `revoke` require `--reason` (exit 2 without it);
`credential enable` accepts an optional `--reason`. `key create --credential ID` pins a virtual key.
List commands take
`--state`, `--sort`, `--limit`, `--cursor` and `--all`.
