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
| `token-issuer list`, `create`, `get`, `update`, `delete`, `platform` | `listTokenIssuers`, `createTokenIssuer`, `getTokenIssuer`, `updateTokenIssuer`, `deleteTokenIssuer`, `listTenantPlatformIssuers` |
| `subject-mapping list`, `create`, `get`, `delete` | `listSubjectMappings`, `createSubjectMapping`, `getSubjectMapping`, `deleteSubjectMapping` |
| `platform-issuer list`, `create`, `get`, `update`, `delete`, `tenants`, `add-tenant`, `remove-tenant` | `listPlatformIssuers`, `createPlatformIssuer`, `getPlatformIssuer`, `updatePlatformIssuer`, `deletePlatformIssuer`, `listPlatformIssuerTenants`, `putPlatformIssuerTenant`, `deletePlatformIssuerTenant` |
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
  return the resource as it was. Deleting a registration deletes its subject mappings.
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
| `listPlatformLimits`, `createPlatformLimit` | `GET`, `POST /v1/platform-limits` | `platform_limit:read`, `platform_limit:write` |
| `getPlatformLimit`, `updatePlatformLimit`, `deletePlatformLimit` | `GET`, `PATCH`, `DELETE /v1/platform-limits/{rule}` | `platform_limit:read`, `platform_limit:write`, `platform_limit:delete` |

- **A rule** has `id`, `subject_kind` (`tenant`, `service_account`, `key`), `subject_id`, `provider`,
  `model`, `metric` (one of the nine), `limit` (an integer; 0 for a cost metric), `amount` (a decimal
  string with at most six fraction digits; empty unless a cost metric), `currency`, `enforce`,
  `thresholds` (up to four percentages, ascending), `created_at` and `updated_at`. A platform rule has
  `tenant` (an ID; empty for a default of every tenant) in place of the subject. `listLimits` filters
  by `subject_kind`, `subject_id`, `metric`, `provider` and `model`; `listPlatformLimits` by the tenant
  it applies to and `metric`.
- **Validation** (400 `invalid_request` naming the field; 409 `conflict` when a rule of the same
  subject, scope and metric exists). A count metric needs `limit` (1 to 1,000,000,000,000) and takes no
  `amount` or `currency`; a cost metric needs `amount` (greater than 0, at most 1,000,000,000, at most
  six fraction digits) and `currency` (three capital letters) and takes no `limit`. The three metrics
  `concurrent_requests`, `requests_per_minute` and `tokens_per_minute` take `enforce: true` and no
  thresholds; `enforce: false` needs at least one threshold; a repeated threshold is refused.
  `subject_kind` `tenant` takes no `subject_id`; the other kinds need one that exists **in the
  tenant**: a service account by ID, or for `key` a virtual key by key ID or a subject mapping by ID.
  `model` needs `provider`; the provider must exist and the model must be one the tenant can see (for
  a platform rule, one that exists). The message is the same whether an ID exists in another tenant or
  nowhere. A platform rule with a `tenant` needs an existing tenant.
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
- A change reaches the gateway at once through a notification and at the latest after
  `cache.limits_ttl`; counters are not reset.
- CLI: `limit list|get|create|update|delete`, `limit platform`, `limit events` and
  `platform-limit list|get|create|update|delete`, each with `--tenant` where the route has one.
  `limit create` takes `--metric`, `--limit` or `--amount` with `--currency`, `--subject
  tenant|service-account|key` with `--service-account ID_OR_NAME` or `--key KEY_ID`, `--provider`,
  `--model`, `--soft` and a repeatable `--threshold`; `platform-limit create` takes `--for-tenant`;
  `update` takes `--limit`, `--amount`, `--soft`, `--enforce`, `--threshold` and `--no-thresholds`.
  `limit events` takes `--kind` and `--rule`.

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

- `getCapability` answers whether a tenant can call a provider and model now (`provider` and `model` are required): `callable`, the failing `reasons` in a fixed order (`provider_unavailable`, `model_not_allowed`, `no_credential`, `credential_disabled`, `credential_revoked`), the `checks` and `budget`, which takes four values from the tenant's enforced quotas and budgets of a day or month that match the provider and model, read from the usage ledger with the worker database role, inside the tenant's session under row-level security: `not_enforced` (none matches), `unpriced` (a budget matches and the model has no price in the budget's currency while `limits.on_unknown_price` is `refuse`; reason `no_price`), `exhausted` (the total of the current period is at or above a limit; reason `budget_exhausted`) and `available`. The two reasons follow the others in that order. Only the tenant's own rules for the whole tenant count (the check describes a key without pin or list, so rules of service accounts and keys, soft rules and rate limits are not evaluated), requests in flight are not in the total, and `available` means "not exhausted": a call whose estimate exceeds what is left is still refused. It reads committed catalogue state and credential metadata and never opens a credential, so it can differ from a call made at the same moment by at most the cache lifetimes; a key's pin and model list are not evaluated. A suspended tenant is answered 403 `tenant_suspended`. CLI: `capability check --tenant T --provider P --model M`. [ADR 0043](../adr/0043-tenant-capability-check-and-usage-list.md).
- `listUsageRecords` lists a tenant's usage records newest first, with `from`, `to`, `provider`, `limit` and `cursor` (rules and representation in the [usage guide](../operations/usage.md), section 9). CLI: `usage list --tenant T [--from --to --provider]`.
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
  default endpoint while others exist); `deleteModel` is `invalid_state` once a price version exists:
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
