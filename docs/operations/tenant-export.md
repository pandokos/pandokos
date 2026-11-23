# Tenant data export

A tenant can export its own data in a documented format: a directory of newline-delimited JSON files
with a manifest, described by a published JSON Schema (TEN-12). The export is the tenant's evidence and
its way out before a deletion ([deletion guide](tenant-deletion.md)). Decision:
[ADR 0069](../adr/0069-tenant-data-export.md).

## 1. Who runs it

The tenant runs it, with a key or a session that holds **every read permission** of the tenant (the
`owner` role does). It reads the administration API only, so it holds what the tenant may read and nothing
else: no secret of a credential (VLT-04), no secret of a key, no password hash, no session token, nothing of
another tenant. The platform cannot make the export for a tenant, because the platform reads no usage
([ADR 0054](../adr/0054-usage-data-is-the-tenants.md)), and a suspended tenant reads nothing (TEN-10): **take
the export before a deletion is requested**. A tenant that was suspended for another reason is reactivated
for the export.

```
pandokos tenant-export --tenant 8a1f0c2e-… --out ./acme-export [--usage-from 2026-01-01T00:00:00Z]
```

The command is a client of the API like the others: `PANDOKOS_CLI_SERVER`, `PANDOKOS_CLI_KEY_FILE` and
`PANDOKOS_CLI_CA_FILE` ([configuration](configuration.md#variables-of-the-api-commands)); `--tenant` is the
tenant's ID. `--out` is a directory that **does not exist**: the files are written into `DIR.partial` and the
directory is renamed at the end, so `DIR` exists only when the export is complete. A `403` on any read stops
the export with the missing permission named and writes no manifest; an interrupted export leaves
`DIR.partial` without a manifest, which you remove before trying again. On success it prints
`export of ID written to DIR: N files, M records, audit head seq S` (or the manifest with `-o json`).

## 2. The files

Each line is the JSON object that the API returns, compacted, in the API's order. Files are created with
mode 0600.

| File | Content | Read with |
|---|---|---|
| `manifest.json` | one object: `format` (`pandokos-tenant-export`), `version` (`1`), `tenant_id`, `created_at`, `pandokos_version`, `files` (name, kind, `records`, `sha256`), `audit_head` (`seq`, `hash`, `anchor_seq`), `usage_from`, `usage_to`. Written last | none |
| `settings.json` | one object: time zone (in force and pending), active currency (in force and pending), metadata keys, model restriction | `getTenantTimeZone`, `getActiveCurrency`, `getMetadataKeys`, `getModelRestriction` |
| `service_accounts.ndjson` | service accounts with their roles | `listServiceAccounts` |
| `virtual_keys.ndjson` | keys, metadata only | `listVirtualKeys` |
| `credentials.ndjson` | provider credentials, metadata only (fingerprint, hint; never the secret) | `listCredentials` |
| `token_issuers.ndjson`, `subject_mappings.ndjson`, `platform_issuers.ndjson` | registrations, mappings, and the platform-level issuers that list the tenant | `listTokenIssuers`, `listSubjectMappings`, `listTenantPlatformIssuers` |
| `limits.ndjson`, `platform_limits.ndjson`, `limit_events.ndjson` | rules, the ceilings that apply, limit events | `listLimits`, `listTenantPlatformLimits`, `listLimitEvents` |
| `price_list_versions.ndjson` | every version of every price list with its rates | `listPriceListVersions` |
| `members.ndjson`, `roles.ndjson`, `support_actions.ndjson` | members with their roles, custom roles, support actions | `listMembers`, `listRoles`, `listSupportActions` |
| `usage_records.ndjson` | the ledger, oldest first | `exportUsageRecords`, in windows of 31 days from `--usage-from` (default 60 months ago) to the time of the export |
| `audit_events.ndjson` | the audit log from its anchor to the head named in the manifest | `getAuditHead`, `exportAuditEvents` |

The manifest's `files[].sha256` is the SHA-256 of the file as written and `records` its number of lines,
so a copy can be checked with `sha256sum`. The usage file holds the caller metadata of the records and
their costs, and the audit file the actors and details of the log: **neither is protected once saved**.
Keep the export where you keep the tenant's other records. The head in the manifest lets you verify the
log later: `pandokos audit verify --file audit_events.ndjson --expect-head <hash>` ([audit guide](audit.md)).

## 3. The schema

The format is described by `docs/reference/tenant-export.v1.schema.json`, a JSON Schema (draft 2020-12,
`$id` `urn:pandokos:schema:tenant-export:v1`). The manifest is the root of the schema, and there is one
entry of `$defs` per kind of file (`settings`, `service_account`, `virtual_key`, and so on), each the schema
of the corresponding API object. The binary embeds it:

```
pandokos tenant-export schema > tenant-export.v1.schema.json
```

(it needs no server and no key). A line of a file of kind `k` validates against `#/$defs/k`, and the
manifest's `files[].kind` names the kind. Any validator of JSON Schema 2020-12 can do it; the test suite
uses a third-party validator on a real export (`github.com/santhosh-tekuri/jsonschema/v6`, in the nested
test module of ADR 0023).

The schema is **generated** from `api/openapi/admin.v1.yaml` (OpenAPI 3.1 schemas are JSON Schema 2020-12)
by `internal/apigen -export-schema`, committed, and checked for drift by `make generate-check`. It
describes the shape, not every rule the API enforces; the format is versioned for that reason.

**The version rule.** `version` in the manifest changes when a member is removed from the format or changes
meaning; a member that is added does not change it. A reader of version 1 ignores members it does not know.

## 4. Limits

- The export is **made by the tenant**, through the API, and needs every read permission it contains. A key
  without one fails with the permission named, with no manifest.
- It does not cover what the platform keeps about the tenant in its own log, nor the credentials' secrets.
- It is not importable: `pandokos apply` covers configuration ([declarative guide](declarative.md)); there
  is no import of a tenant export into another installation.
- A tenant exports **before** the request of its deletion: after it the tenant is suspended and reads nothing,
  and after the deletion nobody can read its ledger.
