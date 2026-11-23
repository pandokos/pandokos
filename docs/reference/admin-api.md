# Administration API

The administration API is described by `api/openapi/admin.v1.yaml`, the source of truth, which is
attached to every release. The Go package `internal/adminapi` (types, router, validation and
client) is generated from it by `make generate`; never edit a generated file.

`info.version` of the document is the version of the release it is attached to. It is set by hand in
`api/openapi/admin.v1.yaml`, in the pull request that adds the release page (see
[CONTRIBUTING.md](../../CONTRIBUTING.md#releasing)); during the development of a release it already
carries that release's version. It is not the `/v1` of the base path, which changes only when the API
breaks, and it is not the version the binary reports (`internal/buildinfo`, set from the tag at build
time).

## Conventions

- Base path `/v1`. Platform resources at the top (`/v1/tenants`, `/v1/platform-keys`), tenant
  resources under the tenant, the caller at `/v1/me`. Identifiers in paths are IDs (UUIDs, or the
  key ID of a key); names are attributes found with the `name` filter. The CLI accepts names.
- Authentication: `Authorization: Bearer <key>` (a platform key or a virtual key; with `tokens.admin_listener`
  also a token of a trusted issuer), or the session cookie `__Host-pandokos_session` that people get by
  signing in. The four sign-in operations read no credential. A request with both uses the header and
  ignores the cookie. A session request that is not `GET` or `HEAD` also needs the header
  `X-Pandokos-Csrf: 1` and an `Origin` equal to the expected origin. The sign-in guide
  ([sign-in.md](../operations/sign-in.md)) describes the methods, the lifetimes and how to build a
  browser client.
- JSON only; members are `snake_case`; times are RFC 3339 in UTC. An absent optional member is left
  out of responses. Bodies and queries are strict: an unknown member or parameter is an error.
- Verb sub-resources (`suspend`, `reactivate`, `disable`, `enable`, `revoke`) take an optional
  `{"reason": "..."}` (required where the operation declares `x-pandokos-reason`, see below), return the resource and are idempotent.
- `PATCH` changes the members that are present (an optional member is *presence-aware*: absent leaves
  the value alone, a value sets it, and `null` is refused as `must not be null` except where the
  document allows it); `valid_from` and `valid_until` accept `null`; `roles` replaces the set. For a
  description, an empty string clears it; the CLI clears with `--flag ""`. An update that changes
  nothing returns 200 and writes no audit row. Service accounts, keys and tenants are never deleted. A credential is
  revoked and then deleted: `DELETE` on a credential removes the row and returns the deleted
  resource (its last metadata).
- Rates and amounts of money are decimal **strings** (`"2.5"`, never a JSON number): a rate is a price
  per million units with at most 12 fraction digits.
- A secret is returned by the create response of a platform key or a virtual key only; no operation
  returns a hash. A provider credential's secret is write-only: it is sent by `createCredential` and
  `replaceCredentialSecret` and never returned, not even by create. A response shows its label,
  `fingerprint` and `hint` instead. For a request body that has a `secret` member, an unknown member
  is reported as the field `body` without naming the member, because the name could be a secret.
  Only a member named `secret` makes a body count as carrying one (`carriesSecret`); in any other body
  an unknown member is named in the problem, because the name is what the caller must fix. The value of
  an unknown member is never echoed in either case.
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
| `price_dataset_invalid` | 422 | `runPriceImport` with source `remote`: the downloaded price dataset could not be read; nothing was imported (fixed text, the response bytes are never quoted) |
| `price_source_unavailable` | 502 | `runPriceImport` with source `remote`: the price dataset could not be downloaded; nothing was imported (fixed text) |
| `too_many_attempts` | 429 | sign-in or a password check is delayed or locked, or there are too many sign-in transactions (`Retry-After`) |
| `password_change_required` | 403 | the session must change its password first |
| `csrf_rejected` | 403 | the checks of origin and request forgery failed |
| `sign_in_disabled` | 403 | the sign-in method is switched off, or the origin of the listener is not known |
| `escalation_refused` | 403 | the operation would give, or take, a permission the caller does not hold ([roles guide](../operations/roles.md#4-the-rule-against-escalation)) |
| `support_disabled` | 403 | `openSupportAction` while `support.enabled` is false |
| `role_in_use` | 409 | `deleteRole` for a custom role that a member or a service account still holds |
| `idempotency_key_reused` | 422 | the `Idempotency-Key` was used with another request |
| `idempotency_in_progress` | 409 | the first request with the key is still running (`Retry-After`) |
| `idempotency_response_lost` | 409 | the first request succeeded and its response was not kept |
| `internal` | 500 | an unexpected failure |
| `unavailable` | 503 | the database cannot be reached |

## Users, sessions and members (sprint 4.2)

A person calls the administration API with the session cookie that sign-in sets, in addition to a
key (the document's `security` lists both). Four operations need no credential: `getAuthMethods`,
`signIn`, `startOidcSignIn` and `completeOidcSignIn`. The caller's own sessions are managed with
`signOut`, `changePassword`, `listOwnSessions` and `deleteOwnSession` (permission `session:self`).
The users that the platform manages are `listUsers`, `createUser`, `getUser`, `updateUser`,
`disableUser`, `enableUser`, `unlockUser`, `resetUserPassword` and `revokeUserSessions`; the members
of a tenant are `listMembers`, `createMember`, `getMember`, `updateMember`, `deleteMember`,
`disableMember`, `enableMember`, `unlockMember`, `resetMemberPassword` and `revokeMemberSessions`.
Every operation that answers 201 takes an `Idempotency-Key` header.

A user is global, with one sign-in method and one managing side. The platform operations show the
users that the platform manages only; a tenant's members are shown by the tenant's operations, and a
user that a tenant manages is reset, disabled, unlocked or signed out by that tenant's administrators
only (a platform-managed member: `409 invalid_state`). A generated password is shown once, in the
response of the creation or the reset; a user cannot disable itself or remove its own last platform
role (`409 invalid_state`).

The behaviour that the schemas cannot say, and the settings behind it, are in the
[sign-in guide](../operations/sign-in.md).

## Roles and support actions (sprint 4.3)

Sixteen operations are added. `listPermissions` (`GET /v1/permissions`) and `listBuiltinRoles`
(`GET /v1/roles`) return the catalogue and the ten built-in roles to any authenticated caller
(`identity:read`). A tenant's custom roles are `listRoles`, `getRole` (`role:read`), `createRole`,
`updateRole`, `deactivateRole`, `activateRole` (`role:write`) and `deleteRole` (`role:delete`), under
`/v1/tenants/{tenant}/roles`, a role addressed by its name. `deleteRole` answers `204` and
`409 role_in_use` while the role is held. The platform opens and closes support actions with
`openSupportAction` and `closeSupportAction` (`tenant_support:open`) and reads them with
`listTenantSupportActions` and `getTenantSupportAction` (`tenant_support:read`), under
`/v1/tenant-support/{tenant}/actions`; the platform-side operations answer `404 not_found` for a tenant that
does not exist. The tenant reads the actions of its tenant with `listSupportActions` and
`getSupportAction` (`support_action:read`) and ends one with `endSupportAction` (`support_action:end`), under
`/v1/tenants/{tenant}/support-actions`. `createRole` and `openSupportAction` answer 201 and take an
`Idempotency-Key`. `support_disabled` is answered by `openSupportAction` only: reading and closing work
with `support.enabled` off.

`createPlatformKey` takes an optional `role`, a platform role (default `platform-admin`). The bodies of
the limit-rule operations take an optional `reason`. In `Me`, each `Membership` carries `permissions` and
a platform caller's `support_actions` lists its open actions. Under a support action the platform
principal acts with the tenant's authority, within the permissions of the action, and the guard
`escalation_refused` does not apply to it.

**The construct `x-pandokos-reason: required`.** An operation of the API document may declare it. The
generator then demands a request body with a required string member `reason`, and the generated handler
refuses a reason that is empty after trimming (`400 invalid_request` on `reason`). Nine operations declare
it: `suspendTenant`, `reactivateTenant`, `disableTenantCredential`, `revokeTenantCredential`,
`openSupportAction`, `resetUserPassword`, `resetMemberPassword`, `deactivateRole` and `activateRole`.
The required reason on `suspendTenant` and `reactivateTenant` changes the v0.1 contract, as does
`tenant_setting:write` for `setTenantTimeZone` and `setActiveCurrency`
([roles guide](../operations/roles.md#10-what-changes-for-an-installation-of-v01)). The permissions and
roles are in the [permission reference](permissions.md).

## Reports, exports and audit reading (sprint 6.2)

Twelve operations ([usage guide](../operations/usage.md), [audit guide](../operations/audit.md)):

| Operation | Method and path | Permission |
|---|---|---|
| `getUsageReport` | `GET /v1/tenants/{tenant}/usage-report` | `usage:read` |
| `exportUsageRecords` | `GET /v1/tenants/{tenant}/usage-records/export` | `usage:read` |
| `getLimitUsage` | `GET /v1/tenants/{tenant}/limits/{rule}/usage` | `usage:read` |
| `getMetadataKeys` | `GET /v1/tenants/{tenant}/metadata-keys` | `usage:read` |
| `setMetadataKeys`, `clearMetadataKeys` | `PUT`, `DELETE /v1/tenants/{tenant}/metadata-keys` | `tenant_setting:write` |
| `listAuditEvents`, `getAuditHead`, `exportAuditEvents` | `GET /v1/tenants/{tenant}/audit-events`, `/audit-head`, `/audit-events/export` | `audit:read` |
| `listPlatformAuditEvents`, `getPlatformAuditHead`, `exportPlatformAuditEvents` | `GET /v1/platform-audit-events`, `/v1/platform-audit-head`, `/v1/platform-audit-events/export` | `platform_audit:read` |

No platform role reads a tenant's usage report or export ([ADR 0054](../adr/0054-usage-data-is-the-tenants.md)).

**Two constructs of the generator** ([ADR 0028](../adr/0028-admin-api-generator.md),
[ADR 0064](../adr/0064-usage-rollup-reports-and-exports.md)):

- **Streamed answers.** A `200` whose content is `application/x-ndjson`, optionally also `text/csv`, with
  the schema of one line. The server method returns `*adminapi.Stream` (content type, file name and a
  function that writes), the client method `*adminapi.StreamBody` (content type and a body to read and
  close); a `format` query parameter picks the content when there are two. The three exports use it.
  The answer is `Content-Disposition: attachment`; an error before the first byte is the usual problem,
  and after it the connection is aborted, so a client sees an incomplete transfer (a chunked response
  without its terminator).
- **Raw JSON members.** A member that holds a JSON value the server passes through without decoding it
  (the `detail` of an audit event), so that its numbers keep the text the database stores and the chain
  can be verified from the response.

An optional integer query parameter is checked against its `minimum` only when it is given
(`from_seq`, `to_seq` of the audit exports; an explicit `from_seq=0` is `400`).

## Tenant deletion and key versions (sprint 6.3)

Seven operations ([deletion guide](../operations/tenant-deletion.md), [vault guide](../operations/vault.md),
[ADR 0067](../adr/0067-tenant-deletion-stages-closed-tenants-and-kept-records.md),
[ADR 0068](../adr/0068-key-encryption-key-versions-rotation-and-erasure-in-backups.md)); the document
has 167 operations. The first six are under the tag `tenants`, `listKEKVersions` under the new tag
`vault`.

| Operation | Method and path | Permission | Answer |
|---|---|---|---|
| `requestTenantDeletion` | `POST /v1/tenants/{tenant}/deletion` | `tenant:delete`, reason required | `200` `TenantDeletion` |
| `getTenantDeletion` | `GET /v1/tenants/{tenant}/deletion` | `tenant:read` | `200` `TenantDeletion`; `404` when none was requested |
| `cancelTenantDeletion` | `POST /v1/tenants/{tenant}/deletion/cancel` | `tenant:delete`, reason required | `200` `Tenant` |
| `listTenantDeletions` | `GET /v1/tenant-deletions` | `tenant:read` | a page, newest request first; filter `state` |
| `getDeletedTenantAuditHead` | `GET /v1/tenants/{tenant}/deletion/audit-head` | `platform_audit:read` | `200` `AuditHead` |
| `exportDeletedTenantAuditEvents` | `GET /v1/tenants/{tenant}/deletion/audit-events/export` | `platform_audit:read` | NDJSON stream of `AuditEvent` |
| `listKEKVersions` | `GET /v1/kek-versions` | `kek:read` | every version: `id`, `provider`, `state`, `created_at`, `activated_at`, `retired_at`, `erasures_pending` |

- **`tenant:delete`** is held by `platform-admin` only; `kek:read` by both platform roles. No tenant
  role holds either, and no support action can carry them.
- **Request.** The body is `reason` (1 to 500 characters, required) and `confirm_name`, which must equal
  the tenant's name (`400 invalid_request` on `confirm_name` otherwise). An `active` tenant becomes
  `suspended`; the deletion is scheduled after `tenant_deletion.grace_period`. A tenant that is
  `provisioning`, `deleting` or `deleted` answers `409 invalid_state`. A repeated request while the
  deletion is scheduled answers `200` with the existing deletion and writes nothing; once it is running
  the tenant is `deleting`, so a request answers `409 invalid_state`.
- **Cancel** removes a `scheduled` deletion and leaves the tenant `suspended`. A deletion that started
  answers `409 invalid_state`. While a deletion is scheduled, `reactivateTenant` answers
  `409 invalid_state` with one `errors` entry on `state`.
- **`TenantDeletion`** has `tenant_id`, `state` (`scheduled`, `running`, `completed`), `step`, `reason`,
  `requested_by` (`type`, `id`, `name`), `request_id`, the times `requested_at`, `not_before`,
  `started_at` and `completed_at`, `passes`, `removed` (counts by table), `kek_versions`,
  `backup_erasure` (`state`: `not_applicable` before the step `secrets` ran, then `pending` or
  `complete`; and `pending_kek_versions`),
  `audit` (`anchor_seq`, `last_seq`, `rows`, `original_head`, `resealed_head`; absent before the step
  `audit` ran) and `completion_seq`. The salt of the pseudonyms is in no answer. After a restart of the
  worker `step` can read `unknown` until the next step runs.
- **The kept log.** The two operations of `platform_audit:read` answer `409 invalid_state` until the
  tenant is `deleted`. They read the rewritten log, which holds pseudonyms and no detail, no price and no
  person; the schema of the export is the one of `exportAuditEvents`.
- **Writes for a closed tenant.** Every platform operation that would write a row for a tenant being
  deleted or deleted (`grantModel`, `createPlatformLimit`, `putPlatformIssuerTenant`,
  `openSupportAction`, creating a user that tenant manages, and the others) answers `409 invalid_state`
  with one `errors` entry on `tenant` ("the tenant is being deleted or is deleted"). A principal of the
  tenant gets `403 tenant_suspended`, then `401` once its identities are removed. No new error code was added.

## The accepted subset of OpenAPI 3.1

`internal/apigen` accepts a deliberate subset and fails on anything else: `openapi: 3.1.0`; path
items with `get`, `post`, `patch`, `put`, `delete`; operations with a unique lower-camel-case
`operationId` and a mandatory `x-pandokos-permission` (or `x-pandokos-public: true` with `security: []` for the four sign-in operations; a `303` response with a `Location` header as the only response of a public `GET`; the header parameter `IdempotencyKey`; `x-pandokos-lenient-query: true`; `headers` on responses are accepted and ignored); path parameters that are strings with a
pattern, query parameters that are optional strings, enumerations or integers; flat JSON request
bodies; exactly one 2xx response; every other response `application/problem+json` with the schema
`Problem`; component objects with `additionalProperties: false`. A keyword it does not know is an
error, so the document cannot state a rule the generated code does not enforce.

## Commands

Every operation has one CLI command that only calls the generated client, except the public
operations and the operations on the caller's own session: the CLI authenticates with a key.
`TENANT` and `SERVICE_ACCOUNT` accept an ID or a name; a user or a member also a username. The
commands that return a generated password print it once on standard output, or write it with
`--password-out FILE` to a new file of mode 0600.

| Command | Operation |
|---|---|
| `whoami` | `getMe` |
| `tenant list`, `create` (`--time-zone`), `get` (shows `active_currency`, `active_currency_in_force`, `active_currency_effective_from`), `update` (`--time-zone`), `suspend` and `reactivate` (`--reason` required) | `listTenants`, `createTenant`, `getTenant`, `updateTenant`, `suspendTenant`, `reactivateTenant` |
| `service-account list`, `create`, `get`, `update`, `disable`, `enable` | `listServiceAccounts`, `createServiceAccount`, `getServiceAccount`, `updateServiceAccount`, `disableServiceAccount`, `enableServiceAccount` |
| `key list`, `create`, `get`, `revoke` | `listVirtualKeys`, `createVirtualKey`, `getVirtualKey`, `revokeVirtualKey` |
| `platform-key list`, `create` (`--role platform-admin\|platform-auditor`), `get`, `revoke` | `listPlatformKeys`, `createPlatformKey`, `getPlatformKey`, `revokePlatformKey` |
| `credential list`, `create`, `get`, `update`, `replace`, `disable`, `enable`, `set-default`, `revoke`, `delete` | `listCredentials`, `createCredential`, `getCredential`, `updateCredential`, `replaceCredentialSecret`, `disableCredential`, `enableCredential`, `setDefaultCredential`, `revokeCredential`, `deleteCredential` |
| `tenant-credential list`, `get`, `disable`, `revoke` (`--reason` required on the last two) | `listTenantCredentials`, `getTenantCredential`, `disableTenantCredential`, `revokeTenantCredential` |
| `provider list`, `create`, `get`, `update`, `delete` | `listProviders`, `createProvider`, `getProvider`, `updateProvider`, `deleteProvider` |
| `endpoint list`, `create`, `get`, `update`, `delete` (after `PROVIDER`) | `listEndpoints`, `createEndpoint`, `getEndpoint`, `updateEndpoint`, `deleteEndpoint` |
| `model list`, `create`, `get`, `update`, `delete`, `grants`, `grant`, `revoke-grant` | `listModels`, `createModel`, `getModel`, `updateModel`, `deleteModel`, `listModelGrants`, `grantModel`, `revokeModelGrant` |
| `price list`, `override`, `delete`, `clear-override` (after `MODEL`) | `listPriceVersions`, `createPriceOverride`, `deletePriceVersion`, `clearPriceOverride` |
| `price-import list`, `get`, `run` | `listPriceImports`, `getPriceImport`, `runPriceImport` |
| `tenant-model list` | `listTenantModels` |
| `model-restriction get`, `set`, `clear` | `getModelRestriction`, `setModelRestriction`, `clearModelRestriction` |
| `capability check` | `getCapability` |
| `usage list` | `listUsageRecords` |
| `usage report` (`--month`, `--from`, `--to`, `--period`, `--group-by`, `--provider`, `--model`) | `getUsageReport` |
| `usage export` (`--from`, `--to`, `--provider`, `--format ndjson\|csv`, `--out`; a window over 31 days is split into requests) | `exportUsageRecords` |
| `limit usage RULE` | `getLimitUsage` |
| `metadata-keys get`, `set`, `clear` (with `--tenant`; keys as arguments) | `getMetadataKeys`, `setMetadataKeys`, `clearMetadataKeys` |
| `audit list`, `head`, `export` (with `--tenant`; filters `--from`, `--to`, `--action`, `--resource-type`, `--resource-id`, `--actor-type`, `--actor-id`, `--outcome`, `--request-id`, `--support-action-id`; `--from-seq`, `--to-seq` on `export`) | `listAuditEvents`, `getAuditHead`, `exportAuditEvents` |
| `platform-audit list`, `head`, `export` (as above, without `--tenant` and `--support-action-id`) | `listPlatformAuditEvents`, `getPlatformAuditHead`, `exportPlatformAuditEvents` |
| `audit verify`, `platform-audit verify` (`--expect-head`, `--from-seq`, `--file`) | none: a composite command that reads the head and the export and checks the chain ([audit guide](../operations/audit.md)) |
| `export (--platform \| --tenant ID) [--out FILE]`, `apply --file FILE (--platform \| --tenant ID) [--dry-run]` | none: composite commands over the API ([declarative guide](../operations/declarative.md)) |
| `price-list lists`, `list`, `get`, `set`, `delete`, `currency get`, `currency set` | `listPriceLists`, `listPriceListVersions`, `getPriceListVersion`, `createPriceListVersion`, `deletePriceListVersion`, `getActiveCurrency`, `setActiveCurrency` |
| `token-issuer list`, `create`, `get`, `update`, `delete`, `platform` | `listTokenIssuers`, `createTokenIssuer`, `getTokenIssuer`, `updateTokenIssuer`, `deleteTokenIssuer`, `listTenantPlatformIssuers` |
| `subject-mapping list`, `create`, `get`, `delete` | `listSubjectMappings`, `createSubjectMapping`, `getSubjectMapping`, `deleteSubjectMapping` |
| `platform-issuer list`, `create`, `get`, `update`, `delete`, `tenants`, `add-tenant`, `remove-tenant` | `listPlatformIssuers`, `createPlatformIssuer`, `getPlatformIssuer`, `updatePlatformIssuer`, `deletePlatformIssuer`, `listPlatformIssuerTenants`, `putPlatformIssuerTenant`, `deletePlatformIssuerTenant` |
| `platform-user list`, `create`, `get`, `update`, `disable`, `enable`, `reset-password` (`--reason` required), `unlock`, `revoke-sessions` | `listUsers`, `createUser`, `getUser`, `updateUser`, `disableUser`, `enableUser`, `resetUserPassword`, `unlockUser`, `revokeUserSessions` |
| `member list`, `add`, `get`, `update`, `remove`, `disable`, `enable`, `reset-password` (`--reason` required), `unlock`, `revoke-sessions` (with `--tenant`) | `listMembers`, `createMember`, `getMember`, `updateMember`, `deleteMember`, `disableMember`, `enableMember`, `resetMemberPassword`, `unlockMember`, `revokeMemberSessions` |
| `permission list` | `listPermissions` |
| `role builtin`, `role list`, `create`, `get`, `update`, `deactivate`, `activate`, `delete` (with `--tenant`; `--permission` repeatable, on `update` it replaces the set; `--reason` required on `deactivate` and `activate`) | `listBuiltinRoles`, `listRoles`, `createRole`, `getRole`, `updateRole`, `deactivateRole`, `activateRole`, `deleteRole` |
| `tenant-support open` (`--reason` required, `--permission` repeatable, `--duration`), `list`, `get`, `close` (with `--tenant`) | `openSupportAction`, `listTenantSupportActions`, `getTenantSupportAction`, `closeSupportAction` |
| `support-action list`, `get`, `end` (with `--tenant`) | `listSupportActions`, `getSupportAction`, `endSupportAction` |
| `import-prices (--file FILE \| --embedded) [--dry-run]` | none: a host command that reads a price dataset and writes through the database ([catalogue guide](../operations/catalogue.md)) |
| `tenant delete TENANT --reason R --confirm-name NAME` | `requestTenantDeletion` |
| `tenant deletion get TENANT`, `tenant deletion cancel TENANT --reason R`, `tenant deletion list` (`--state`) | `getTenantDeletion`, `cancelTenantDeletion`, `listTenantDeletions` |
| `tenant deletion audit-head TENANT`, `tenant deletion audit-export TENANT` (`--out FILE`) | `getDeletedTenantAuditHead`, `exportDeletedTenantAuditEvents` |
| `tenant deletion verify TENANT` | none: a composite command that checks the completion record against both chains ([deletion guide](../operations/tenant-deletion.md)) |
| `kek-version list` | `listKEKVersions` |
| `tenant-export --tenant ID --out DIR` (`--usage-from`), `tenant-export schema` | none: composite commands over the API; the second needs no server ([export guide](../operations/tenant-export.md)) |
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
`credential enable` accepts an optional `--reason`. `credential update --description ""` clears the
description (an absent `--description` leaves it alone). `key create --credential ID` pins a virtual key.
List commands take
`--state`, `--sort`, `--limit`, `--cursor` and `--all`.

### Token issuers and subject mappings

A tenant registers the identity providers whose signed tokens it trusts and maps their subjects to
its service accounts; a platform administrator can register an issuer that acts for several
tenants ([tokens guide](../operations/tokens.md)). The operations are:

| Operation | Method and path | Permission |
|---|---|---|
| `listTokenIssuers`, `createTokenIssuer` | `GET`, `POST /v1/tenants/{tenant}/token-issuers` | `token_issuer:read`, `token_issuer:write` |
| `getTokenIssuer`, `updateTokenIssuer`, `deleteTokenIssuer` | `GET`, `PATCH`, `DELETE .../token-issuers/{issuer}` | `token_issuer:read`, `token_issuer:write`, `token_issuer:delete` |
| `listTenantPlatformIssuers` | `GET /v1/tenants/{tenant}/platform-issuers` | `token_issuer:read` |
| `listSubjectMappings`, `createSubjectMapping` | `GET`, `POST /v1/tenants/{tenant}/subject-mappings` | `subject_mapping:read`, `subject_mapping:write` |
| `getSubjectMapping`, `deleteSubjectMapping` | `GET`, `DELETE .../subject-mappings/{mapping}` | `subject_mapping:read`, `subject_mapping:delete` |
| `listPlatformIssuers`, `createPlatformIssuer` | `GET`, `POST /v1/platform-issuers` | `platform_issuer:read`, `platform_issuer:write` |
| `getPlatformIssuer`, `updatePlatformIssuer`, `deletePlatformIssuer` | `GET`, `PATCH`, `DELETE /v1/platform-issuers/{issuer}` | `platform_issuer:read`, `platform_issuer:write`, `platform_issuer:delete` |
| `listPlatformIssuerTenants` | `GET /v1/platform-issuers/{issuer}/tenants` | `platform_issuer:read` |
| `putPlatformIssuerTenant`, `deletePlatformIssuerTenant` | `PUT`, `DELETE /v1/platform-issuers/{issuer}/tenants/{tenant}` | `platform_issuer:write` |

- `createTokenIssuer` and `createPlatformIssuer` take `name`, `issuer` and `audience` (and
  `tenant_claim` for a platform-level one); `algorithms` defaults to `["RS256", "ES256"]`,
  `subject_claim` to `sub`, `required_claims` to `{}` and `enabled` to `true`; `enabled: false` is
  honoured. `issuer` and a non-empty `jwks_uri` are `https` URLs with a host and without user
  information or fragment (and `issuer` without a query). The names of `subject_claim`,
  `tenant_claim` and of the members of `required_claims` are claim names that are not `iss`, `aud`,
  `exp`, `nbf` or `iat`. The stored size of `required_claims` is bounded (4096 bytes): a set that
  does not fit is `invalid_request` on `required_claims`.
- The pair of `issuer` and `audience` is unique in the installation, over both kinds of
  registration. A taken pair, a taken name, a subject that is already mapped and a claim value that
  is already in use in a registration are all `conflict`, with the same body and nothing that says
  what holds them.
- `PATCH` changes the members that are present; `algorithms` and `required_claims` replace the whole
  value; `{"enabled": false}` disables a registration and `{"enabled": true}` enables it. A
  registration has no secret, and the administration plane never fetches the documents of an issuer.
- `deleteTokenIssuer`, `deletePlatformIssuer`, `deleteSubjectMapping` and `deletePlatformIssuerTenant`
  return the resource as it was. Deleting a registration deletes its subject mappings, and deleting a
  mapping or a registration deletes the limit rules of those mappings, audited (see Limits).
- A mapping names a registration (`issuer`: `{scope: tenant|platform, id}`), a `subject` of 1 to 255
  characters, compared byte for byte, and a service account of the tenant. A registration of another
  tenant, a platform-level one that does not list the tenant, a service account of another tenant and
  an ID that exists nowhere are the same `invalid_request` on `issuer` or `service_account_id`: the
  answer never says whether the ID exists elsewhere. A mapping cannot be changed: delete it and
  create it again. A mapping to a platform-level registration stays, inert, when the tenant is
  removed from its list.
- `listTenantPlatformIssuers` shows a tenant the platform-level registrations that list it, with
  its own `claim_value` and nothing of the other tenants of the list.
- `putPlatformIssuerTenant` is idempotent. The tenant must exist and not be deleted (`not_found`
  otherwise); `claim_value` defaults to the tenant's ID and is unique in the registration. A `PUT` that changes nothing writes no audit row. A `subject_claim` or `claim_value` that is present must not be empty.
- Audit: `token_issuer.create|update|delete`, `subject_mapping.create|delete` are in the tenant's
  log; `platform_issuer.create|update|delete|tenant_add|tenant_remove` are in the platform log
  only. A creation records the members of the registration; an update records `changed` (the sorted
  names of the members that changed) and their new values, and nothing when nothing changed; a deletion
  records the name and `mappings_deleted`; a platform-level deletion counts the mappings of every tenant
  that the cascade removes (a database function counts them without returning rows).

Flags of the issuer commands: `token-issuer create --tenant T --name N --issuer URL --audience A
[--jwks-uri U] [--algorithm X]... [--subject-claim C] [--require CLAIM=VALUE]... [--description D]
[--disabled]`; `token-issuer update ISSUER` takes the same flags (and `--no-require`, `--enable`,
`--disable`) and sends only the flags given, an empty value clearing `--jwks-uri` and
`--description`; `token-issuer list [--name N] [--enabled true|false]`; `token-issuer platform --tenant T`
lists the platform-level issuers that list the tenant. `subject-mapping create --tenant T --issuer ID
[--platform] --subject S --service-account SA [--description D]`, with `SA` an ID or a name;
`subject-mapping list [--issuer ID] [--service-account SA] [--subject S]`. `platform-issuer` takes the
flags of `token-issuer` without `--tenant` and with `--tenant-claim` (required on `create`);
`platform-issuer tenants ISSUER`, `add-tenant [--claim-value V] ISSUER TENANT` and
`remove-tenant ISSUER TENANT` (a tenant by ID or name).

### Limits

A tenant administrator writes the tenant's limit rules and reads the platform ceilings that apply to
it; a platform administrator writes ceilings for every tenant or for one. What the rules mean and how
they are enforced is in the [limits guide](../operations/limits.md).

| Operation | Method and path | Permission |
|---|---|---|
| `listLimits`, `createLimit` | `GET`, `POST /v1/tenants/{tenant}/limits` | `limit:read`, `limit:write` |
| `getLimit`, `updateLimit`, `deleteLimit` | `GET`, `PATCH`, `DELETE .../limits/{rule}` | `limit:read`, `limit:write`, `limit:delete` |
| `listTenantPlatformLimits` | `GET /v1/tenants/{tenant}/platform-limits` | `limit:read` |
| `listLimitEvents` | `GET /v1/tenants/{tenant}/limit-events` | `limit:read` |
| `getTenantTimeZone`, `setTenantTimeZone` | `GET`, `PUT /v1/tenants/{tenant}/time-zone` | `limit:read`, `limit:write` |
| `listPriceLists` | `GET /v1/tenants/{tenant}/price-lists` | `price_list:read` |
| `listPriceListVersions`, `createPriceListVersion` | `GET`, `POST /v1/tenants/{tenant}/prices` | `price_list:read`, `price_list:write` |
| `getPriceListVersion`, `deletePriceListVersion` | `GET`, `DELETE /v1/tenants/{tenant}/prices/{price}` | `price_list:read`, `price_list:delete` |
| `getActiveCurrency`, `setActiveCurrency` | `GET`, `PUT /v1/tenants/{tenant}/active-currency` | `price_list:read`, `price_list:write` |
| `listPlatformLimits`, `createPlatformLimit` | `GET`, `POST /v1/platform-limits` | `platform_limit:read`, `platform_limit:write` |
| `getPlatformLimit`, `updatePlatformLimit`, `deletePlatformLimit` | `GET`, `PATCH`, `DELETE /v1/platform-limits/{rule}` | `platform_limit:read`, `platform_limit:write`, `platform_limit:delete` |

- **A rule** has `id`, `subject_kind` (`tenant`, `service_account`, `key`), `subject_id`, `provider`,
  `model`, `provider_id` and `model_id` (the identifiers the rule holds; empty when it is not scoped; names
  are read through them at the time of the call), `metric` (one of the nine), `limit` (an integer; 0 for a cost metric), `amount` (a decimal
  string with at most six fraction digits; empty unless a cost metric), `currency`, `enforce`,
  `thresholds` (up to four percentages, ascending), `active` (a cost rule counts only while its currency is the
  tenant's active currency in force; `false` otherwise. On a platform ceiling the member is always present,
  `true` or `false`, in the answer of `listTenantPlatformLimits` (the tenant's view), and always absent from
  `listPlatformLimits` and the other platform operations, which compare with `active_currency_in_force` of the
  tenant instead), `created_at` and `updated_at`. A platform rule has
  `tenant` (an ID; empty for a default of every tenant) in place of the subject. `listLimits` filters
  by `subject_kind`, `subject_id`, `metric`, `provider` and `model`; `listPlatformLimits` by the tenant
  it applies to and `metric`.
- **Validation** (400 `invalid_request` naming the field; 409 `conflict` when a rule of the same
  subject, scope, metric and currency exists). A count metric needs `limit` (1 to 1,000,000,000,000) and takes no
  `amount` or `currency`; a cost metric needs `amount` (greater than 0, at most 1,000,000,000, at most
  six fraction digits) and `currency` (three capital letters) and takes no `limit`. The three metrics
  `concurrent_requests`, `requests_per_minute` and `tokens_per_minute` take `enforce: true` and no
  thresholds; `enforce: false` needs at least one threshold; a repeated threshold is refused.
  `subject_kind` `tenant` takes no `subject_id`; the other kinds need one that exists **in the
  tenant**: a service account by ID, or for `key` a virtual key by key ID (not a revoked one) or a subject
  mapping by ID. The rule stores identifiers, and the database holds them with foreign keys.
  `model` needs `provider`; the provider must exist and the model must be one the tenant can see (for
  a platform rule, one that exists). The message is the same whether an ID exists in another tenant or
  nowhere. A platform rule with a `tenant` needs an existing tenant. **A budget's currency** must be one
  that has a list for its scope: for a tenant rule, a currency of the tenant's own prices or of the
  catalogue's for a model of the scope; for a ceiling of one tenant, the catalogue's or the tenant's active or
  pending currency; for a default ceiling, the catalogue's (400 on `currency`: `must be a currency the prices
  of this scope use: EUR, USD`, sorted, or `no model of this scope has a price`). The check applies to the
  creation and the update of tenant and platform budgets, and not to count metrics; it does not ask that the
  currency be the active one ([limits guide](../operations/limits.md), section 5).
- **At most 5,000 rules** per tenant: the count is checked in the transaction of the creation, under a
  lock per tenant, and the 5,001st is refused with 409 `invalid_state`.
- `PATCH` takes `limit` or `amount`, `enforce` and `thresholds` (at least one, and the result must
  satisfy the rules above; `limit: 0` counts as absent). The subject, scope, metric and currency never
  change: delete the rule and create another. `DELETE` returns the rule as it was.
- `listTenantPlatformLimits` shows a tenant the defaults and its own platform rows, and nothing of
  another tenant. `listLimitEvents` returns the events of the tenant newest first, with the filters
  `kind` (`threshold_reached` or `exhausted`) and `rule`; an event holds the rule, its level, the
  subject, scope, metric, currency, the period, the value of the rule, `observed` and `percent`.
- **Audit.** Every write is audited in the transaction of the change; a refused write records
  nothing. Six actions: `limit.create`, `limit.update` and `limit.delete` in the tenant's log
  (resource `limit_rule`) and `platform_limit.create`, `platform_limit.update` and
  `platform_limit.delete` in the platform log (resource `platform_limit_rule`). A create or delete
  records every field of the rule, an update the `from` and `to` of each changed field and the rule's
  subject, scope and metric; platform records also hold the `tenant`. `default_output_tokens` appears
  in the detail of `model.create` and `model.update`.
- **Rules go with their subject.** `revokeVirtualKey`, `deleteSubjectMapping`, `deleteTokenIssuer` and
  `deletePlatformIssuer` delete the rules of the subjects they remove, in the same transaction, one
  `limit.delete` audit row each with the actor of the operation, the request ID, the `reason` of the
  request when it has one and, in `detail`, `cause` (`key_revoked`, `subject_mapping_deleted`,
  `token_issuer_deleted` or `platform_issuer_deleted`) and `cause_id`; the audit row of the operation gains
  `limit_rules_deleted`. Their responses do not change. `deleteProvider` and `deleteModel` are 409
  `invalid_state` while a rule of any tenant or of the platform names the entry, with one `errors` entry
  on the field `limit_rules`. Rules removed by the upgrade carry the actor `migration 0011` and a `cause`
  `migration_subject_missing`, `migration_key_revoked` or `migration_scope_missing`.
- **The time zone of a tenant.** `getTenantTimeZone` (`limit:read`) and `setTenantTimeZone`
  (`limit:write`) read and set it: `TenantTimeZone` has `time_zone` (as set; empty for the default of
  `limits.time_zone`), `in_force` (the name of the zone in force now) and `effective_from` (RFC 3339 UTC,
  present while a change is pending); `PUT` takes `{"time_zone": "Europe/Rome"}`, an empty string is the
  default, and a name the embedded zone database does not know or whose offsets are not whole minutes is
  400 on `time_zone`. A change takes effect at the start of the next month in the zone in force; setting
  the value already set changes and audits nothing. `createTenant` and `updateTenant` take `time_zone`
  too (`tenant:write`; on creation it is in force at once), and `Tenant` shows `time_zone`,
  `time_zone_in_force` and `time_zone_effective_from`. A tenant's key on another tenant's route gets the
  404 of a tenant that does not exist. Audit: `tenant.time_zone_update` in the tenant's log (resource
  `tenant`, detail `from`, `to`, `in_force`, `effective_from`) and, for the platform's operations,
  `tenant.update` with `time_zone` (`from`, `to`) and `time_zone_effective_from`.
- **Audit actions of sprint 5.3.** Besides the six above, `limit.delete` gains the causes listed, and two
  actions are new: `tenant.time_zone_update` (tenant log) and `limit.unlimited_traffic` (tenant log and
  platform log, resource `tenant`, actor type `platform`, actor name `gateway`, detail `day`, `time_zone`,
  `provider`, `model`), written by the gateway, once per tenant and day, for the first request that no rule
  covered ([limits guide](../operations/limits.md), section 15).
- A change reaches the gateway at once through a notification and at the latest after
  `cache.limits_ttl`; counters are not reset.
- CLI: `limit list|get|create|update|delete`, `limit platform`, `limit events` and
  `platform-limit list|get|create|update|delete`, each with `--tenant` where the route has one.
  `limit create` takes `--metric`, `--limit` or `--amount` with `--currency`, `--subject
  tenant|service-account|key` with `--service-account ID_OR_NAME` or `--key KEY_ID`, `--provider`,
  `--model`, `--soft` and a repeatable `--threshold`; `platform-limit create` takes `--for-tenant`;
  `update` takes `--limit`, `--amount`, `--soft`, `--enforce`, `--threshold` and `--no-thresholds`.
  `limit events` takes `--kind` and `--rule`. `limit time-zone get --tenant T` and `limit time-zone set --tenant T ZONE|--default` call `getTenantTimeZone` and `setTenantTimeZone` (a zone and `--default` are exclusive).

### Price lists and the active currency

A tenant administrator writes the tenant's own price lists and chooses its active currency; **only the
tenant reads and writes them**, and no platform principal can: the three permissions are tenant-scoped and
no platform role holds one. What they mean is in the [catalogue guide](../operations/catalogue.md),
section 7 ([ADR 0053](../adr/0053-tenant-price-lists-and-active-currency.md)).

- `listPriceLists`: one `TenantPriceList` per currency in which the tenant has a version: `currency`,
  `versions`, `models_priced` (models with a price in force now) and `active` (its currency is the one in
  force).
- `listPriceListVersions`: newest first; filters `currency`, `provider`, `model` (needs `provider`) and `at`
  (RFC 3339: the versions in force then, one per list and model); cursor on the ID.
- `createPriceListVersion` (`provider`, `model` and `currency` required; `kind` `price` (default) or `end`,
  `rates` (as `PriceRate`; none for an end), `valid_from`, `note`): 201 with the `TenantPriceVersion` (`id`,
  `model_id`, `provider`, `model`, `currency`, `kind`, `valid_from`, `rates`, `note`, `in_force`,
  `created_at`). 400 on `model` (`must name a model available to this tenant`: the same for a model that is
  elsewhere and one that is nowhere), on `rates` (the rules of `createPriceOverride`) and on `valid_from`
  (in the past); 409 `invalid_state` for an `end` when the list has no price for the model at that start
  (field `kind`) and beyond 50,000 versions per tenant; 409 `conflict` for the same list, model and start
  twice. A new version starts now or later and never changes.
- `getPriceListVersion` reads any version, replaced ones included; `deletePriceListVersion` removes one that
  has not started (409 `invalid_state` otherwise) and returns it as it was. An ID of another tenant is the
  404 of an ID that does not exist.
- `getActiveCurrency` and `setActiveCurrency` (`PUT {"currency": "EUR"}`; `""` is the default of the
  installation): `ActiveCurrency` has `currency` (as set), `in_force` (the code in force now) and
  `effective_from` (while a change is pending); the answer to a change adds `inactive_limits` (at most 100:
  `level` `tenant` or `platform`, `rule_id`, `metric`, `currency`): the cost rules and ceilings that stop
  applying when it takes effect. A currency other than the default needs a list (400 on `currency`: `no price
  list in this currency: add prices in it first`). The change takes effect at the start of the next month in
  the tenant's time zone; a second change before then replaces the pending currency and keeps the instant.
- **Audit.** Three actions in the tenant's log, written in the transaction of the change, with the tenant
  principal as actor: `tenant_price.create` and `tenant_price.delete` (resource `tenant_price_version`:
  provider, model, `model_id`, currency, kind, `valid_from`; a create adds the `rates` and
  `previous_version`) and `tenant.active_currency_update` (resource `tenant`: `from`, `to`, `in_force`,
  `effective_from`, `inactive_limits` as a number). The `note` of a version is stored with it and never
  copied into an audit record or a log line. The platform's log gets no row from these operations.
- The `Tenant` resource shows the platform `active_currency`, `active_currency_in_force` and
  `active_currency_effective_from`: a setting of the tenant, not usage and not a price, which the platform
  needs to know which of its ceilings apply. `createTenant` and `updateTenant` do not take it.
- `listTenantModels` also shows, per model, `tenant_price` (the version of the tenant's active list in force,
  when it is a price) and `price_source` (`tenant`, `catalogue`, or empty when no price applies in the active
  currency). A model that a tenant has priced cannot be deleted (409 `invalid_state`, field `tenant_prices`).
- CLI: `price-list lists|list|get|set|delete` and `price-list currency get|set`, each with `--tenant`;
  `set` takes `--currency`, `--model`, a repeatable `--rate category[@threshold]=amount`, `--end`,
  `--valid-from` and `--note`; `currency set` takes a code or `--default` and prints the rules that become
  inactive.

### The catalogue

The operations are described in the [catalogue guide](../operations/catalogue.md). Provider and endpoint
path parameters are names; models, price versions and imports are IDs; the CLI accepts a model
reference `provider/name` wherever it takes a model.

| Operation | Method and path | Permission |
|---|---|---|
| `listProviders`, `createProvider` | `GET`, `POST /v1/providers` | `catalog:read`, `catalog:write` |
| `getProvider`, `updateProvider`, `deleteProvider` | `GET`, `PATCH`, `DELETE /v1/providers/{provider}` | `catalog:read`, `catalog:write`, `catalog:write` |
| `listEndpoints`, `createEndpoint` | `GET`, `POST /v1/providers/{provider}/endpoints` | `catalog:read`, `catalog:write` |
| `getEndpoint`, `updateEndpoint`, `deleteEndpoint` | `GET`, `PATCH`, `DELETE .../endpoints/{endpoint}` | `catalog:read`, `catalog:write`, `catalog:write` |
| `listModels`, `createModel` | `GET`, `POST /v1/models` | `catalog:read`, `catalog:write` |
| `getModel`, `updateModel`, `deleteModel` | `GET`, `PATCH`, `DELETE /v1/models/{model}` | `catalog:read`, `catalog:write`, `catalog:write` |
| `listModelGrants` | `GET /v1/models/{model}/grants` | `catalog:read` |
| `grantModel`, `revokeModelGrant` | `PUT`, `DELETE /v1/models/{model}/grants/{tenant}` | `catalog:write` |
| `listPriceVersions`, `createPriceOverride` | `GET`, `POST /v1/models/{model}/prices` | `catalog:read`, `catalog:price` |
| `deletePriceVersion` | `DELETE /v1/models/{model}/prices/{price_version}` | `catalog:price` |
| `clearPriceOverride` | `POST /v1/models/{model}/clear-price-override` | `catalog:price` |
| `listPriceImports`, `getPriceImport` | `GET /v1/price-imports`, `GET /v1/price-imports/{price_import}` | `catalog:read` |
| `runPriceImport` | `POST /v1/price-imports` | `catalog:price` |
| `listTenantModels` | `GET /v1/tenants/{tenant}/models` | `model:read` |
| `getModelRestriction`, `setModelRestriction`, `clearModelRestriction` | `GET`, `PUT`, `DELETE /v1/tenants/{tenant}/model-restriction` | `model:read`, `model:restrict`, `model:restrict` |
| `getCapability` | `GET /v1/tenants/{tenant}/capability` | `model:read` |
| `listUsageRecords` | `GET /v1/tenants/{tenant}/usage-records` | `usage:read` |

- `getCapability` answers whether a tenant can call a provider and model now (`provider` and `model` are required): `callable`, the failing `reasons` in a fixed order (`provider_unavailable`, `model_not_allowed`, `no_credential`, `credential_disabled`, `credential_revoked`), the `checks` and `budget`, which takes four values from the tenant's enforced quotas and budgets of a day or month that match the provider and model, read from the usage ledger with the worker database role, inside the tenant's session under row-level security: `not_enforced` (none matches; budgets in another currency than the active one do not count), `unpriced` (an active budget (one in the tenant's active currency) matches and the model has no price in that currency while `limits.on_unknown_price` is `refuse`; reason `no_price`), `exhausted` (the total of the current period is at or above a limit; reason `budget_exhausted`) and `available`. The two reasons follow the others in that order. Only the tenant's own rules for the whole tenant count (the check describes a key without pin or list, so rules of service accounts and keys, soft rules and rate limits are not evaluated), requests in flight are not in the total, and `available` means "not exhausted": a call whose estimate exceeds what is left is still refused. It reads committed catalogue state and credential metadata and never opens a credential, so it can differ from a call made at the same moment by at most the cache lifetimes; a key's pin and model list are not evaluated. `checks.budget` uses the calendar of the tenant (its time zone, and a change pending) and matches rules by catalogue identifier. A suspended tenant is answered 403 `tenant_suspended`. CLI: `capability check --tenant T --provider P --model M`. [ADR 0043](../adr/0043-tenant-capability-check-and-usage-list.md).
- `listUsageRecords` lists a tenant's usage records newest first, with `from`, `to`, `provider`, `limit_state` (`limited`, `unlimited`, `unverified` or `not_evaluated`), `limit` and `cursor` (rules and representation in the [usage guide](../operations/usage.md), section 9); a record shows `limit_state`, empty for one written before sprint 5.3. CLI: `usage list --tenant T [--from --to --provider --limit-state]`.
- The provider member `stream_usage_option` is `add` or `omit`; `omit` for a provider of family `anthropic` is `invalid_request` ("must be add for a provider of family anthropic").
- Collections: `listModels` filters by `provider`, `name`, `status` and `availability`. `createModel`
  takes `availability` `selected` unless told otherwise. `listPriceVersions` takes `at` (RFC 3339) and
  returns the version in force then, at most one.
- `grantModel` and `revokeModelGrant` are idempotent; an unknown tenant is `not_found`. Setting a
  tenant's restriction needs every reference to name a model visible to the tenant; clearing a
  restriction twice, or one that does not exist, returns 200.
- `runPriceImport` takes `{source: embedded | remote, dry_run}`, runs synchronously and returns 201 with
  the import and its report. `remote` while `pricing.refresh.enabled` is false is `invalid_request` on
  `source`. The URL is operator configuration; no operation takes one. A failed remote download is
  `price_source_unavailable` (502), an unreadable dataset `price_dataset_invalid` (422); both have a
  fixed text and never quote the response. A failed run is audited (`price_import.run`, outcome
  `failure`).
- `deleteProvider` and `deleteEndpoint` are `invalid_state` while something references them (the
  default endpoint while others exist; for a provider or a model, also a limit rule of any tenant or of the
  platform, whose `errors` entry says to disable the provider, or to retire the model or remove its
  grants); `deleteModel` is `invalid_state` once a price version exists:
  retire the model instead. `clearPriceOverride` is `invalid_state` while a version of the model has not
  started; the override version stays in force until the next import creates one.
- `createVirtualKey` accepts `models` (1 to 64 unique references); `VirtualKey` returns it.
- Every write of the catalogue and of a grant, restriction or price is audited in the same transaction.
  Catalogue, grant, price and import events (`provider.*`, `endpoint.*`, `model.*`, `model_grant.*`,
  `price.*`, `price_import.run`) are in the platform log; `model_restriction.set` and `.clear` are in the
  tenant's log, and `virtual_key.create` records the `models` of a key.

Flags of the catalogue commands: `provider create NAME --family F [--display-name --description
--dataset-provider --stream-usage-option add|omit]`; `endpoint create PROVIDER NAME --base-url U [--region --private --default]`;
`model create REF [--upstream-id --availability --status --capability ...  --context-window
--max-output-tokens --default-output-tokens]`; `model grant|revoke-grant MODEL --tenant T`; `price override MODEL --currency C
--rate category[@threshold]=amount ... [--valid-from --note]`; `price clear-override MODEL --reason R`;
`price-import run [--source embedded|remote] [--dry-run]`; `model-restriction set --tenant T --model REF
...` or `--none`. Update commands clear an optional text member with `--flag ""`; booleans are set with
`--enabled=false` and `--private=false`. In the API every rate states its `threshold` (0 for the base rate);
the command sends `threshold: 0` when no `@threshold` is given.

`pandokos import-prices` (a host command, not an API client) prints one `name: value` line for each of
`models_seen`, `created`, `unchanged`, `overridden`, `unmatched` and `skipped`, then one line per item
(`provider/model: outcome (dataset model X); issue detail`), and exits 0 on success, 1 when the import
fails and 2 for wrong usage. It reads and checks the file before it opens the database.
