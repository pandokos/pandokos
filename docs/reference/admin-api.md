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
| `provider list`, `create`, `get`, `update`, `delete` | `listProviders`, `createProvider`, `getProvider`, `updateProvider`, `deleteProvider` |
| `endpoint list`, `create`, `get`, `update`, `delete` (after `PROVIDER`) | `listEndpoints`, `createEndpoint`, `getEndpoint`, `updateEndpoint`, `deleteEndpoint` |
| `model list`, `create`, `get`, `update`, `delete`, `grants`, `grant`, `revoke-grant` | `listModels`, `createModel`, `getModel`, `updateModel`, `deleteModel`, `listModelGrants`, `grantModel`, `revokeModelGrant` |
| `price list`, `override`, `delete`, `clear-override` (after `MODEL`) | `listPriceVersions`, `createPriceOverride`, `deletePriceVersion`, `clearPriceOverride` |
| `price-import list`, `get`, `run` | `listPriceImports`, `getPriceImport`, `runPriceImport` |
| `tenant-model list` | `listTenantModels` |
| `model-restriction get`, `set`, `clear` | `getModelRestriction`, `setModelRestriction`, `clearModelRestriction` |
| `capability check` | `getCapability` |
| `usage list` | `listUsageRecords` |
| `import-prices (--file FILE \| --embedded) [--dry-run]` | none: a host command that reads a price dataset and writes through the database ([catalogue guide](../operations/catalogue.md)) |
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
  default endpoint while others exist); `deleteModel` is `invalid_state` once a price version exists:
  retire the model instead. `clearPriceOverride` is `invalid_state` while a version of the model has not
  started; the override version stays in force until the next import creates one.
- `createVirtualKey` accepts `models` (1 to 64 unique references); `VirtualKey` returns it.
- Every write of the catalogue and of a grant, restriction or price is audited in the same transaction.
  Catalogue, grant, price and import events (`provider.*`, `endpoint.*`, `model.*`, `model_grant.*`,
  `price.*`, `price_import.run`) are in the platform log; `model_restriction.set` and `.clear` are in the
  tenant's log, and `virtual_key.create` records the `models` of a key.

Flags of the catalogue commands: `provider create NAME --family F [--display-name --description
--dataset-provider]`; `endpoint create PROVIDER NAME --base-url U [--region --private --default]`;
`model create REF [--upstream-id --availability --status --capability ...  --context-window
--max-output-tokens]`; `model grant|revoke-grant MODEL --tenant T`; `price override MODEL --currency C
--rate category[@threshold]=amount ... [--valid-from --note]`; `price clear-override MODEL --reason R`;
`price-import run [--source embedded|remote] [--dry-run]`; `model-restriction set --tenant T --model REF
...` or `--none`. Update commands clear an optional text member with `--flag ""`; booleans are set with
`--enabled=false` and `--private=false`. In the API every rate states its `threshold` (0 for the base rate);
the command sends `threshold: 0` when no `@threshold` is given.

`pandokos import-prices` (a host command, not an API client) prints one `name: value` line for each of
`models_seen`, `created`, `unchanged`, `overridden`, `unmatched` and `skipped`, then one line per item
(`provider/model: outcome (dataset model X); issue detail`), and exits 0 on success, 1 when the import
fails and 2 for wrong usage. It reads and checks the file before it opens the database.
