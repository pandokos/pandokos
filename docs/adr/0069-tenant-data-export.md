# 0069. The tenant data export and its schema

- Status: proposed
- Date: 2026-11-23

Refines [ADR 0054](0054-usage-data-is-the-tenants.md), [ADR 0064](0064-usage-rollup-reports-and-exports.md) and
[ADR 0066](0066-declarative-export-and-apply.md). Requirement TEN-12; sprint 6.3.

## Context

A tenant's data can be exported in a documented format before its deletion (TEN-12). The platform reads no usage
(ADR 0054) and a suspended tenant reads nothing, so the export is the tenant's own act, and it must be taken before
the request that suspends it. The exports of sprint 6.2 cover usage and audit, the declarative document of ADR 0066
covers configuration only; neither is the whole of what a tenant may read.

## Decision

1. **The export is a composite command run by the tenant through the administration API**:
   `pandokos tenant-export --tenant ID --out DIR [--usage-from T]`, with a key or session that holds every read permission
   of the tenant (`owner` does). It reads the API only; each line is the object the API returns (compacted), so the export
   holds what the tenant may read and nothing else: no secret of a credential or a key, no password hash, nothing of another
   tenant. A `403` stops it with the missing permission named and no manifest. (D-18)
2. **The format is a directory** written through `DIR.partial` and renamed at the end: `manifest.json` (format
   `pandokos-tenant-export`, version, tenant, times, a record count and a SHA-256 per file, the audit head, the usage
   window) and one newline-delimited JSON file per kind: settings, service accounts, virtual keys, credentials (metadata
   only), token issuers, subject mappings, platform issuers, limits, platform limits, limit events, price list versions,
   members, roles, support actions, usage records (windows of 31 days, oldest first) and the audit log (from its anchor to
   the head in the manifest). (D-18)
3. **The schema is JSON Schema 2020-12 and is generated from the OpenAPI document** (3.1 schemas are 2020-12) by
   `internal/apigen -export-schema`: the manifest at the root and one `$defs` entry per kind, each the schema of the API
   object. It is committed as `docs/reference/tenant-export.v1.schema.json`, checked for drift by `make generate-check`,
   embedded in the binary and printed by `pandokos tenant-export schema`, which needs no server. (D-19)
4. **The version rule.** The format `version` changes when a member is removed or changes meaning; an added member does not
   change it.
5. **Tests validate a real export with a third-party validator** in the nested module `test/sdk`
   (`github.com/santhosh-tekuri/jsonschema/v6`, [ADR 0023](0023-test-dependencies-in-nested-modules.md)); its CI job gets a
   PostgreSQL database. No module is added to the product. (D-19)

## Consequences

- The export is taken before the deletion is requested; the deletion guide puts it in the procedure. After the request
  the tenant cannot read anything, and after the deletion nobody can read the ledger.
- The schema cannot drift from the API, and describes the shape, not every rule the API enforces; T-1003 validates real
  output.
- An export holds usage metadata and audit details, which are not protected once saved; the guide says so.
- It is not importable into another installation; `pandokos apply` covers configuration.

## Alternatives considered

**A server-side archive.** It needs a platform path to a tenant's usage (ADR 0054) or a long request.

**One file.** The kinds differ in size by orders of magnitude and are read by different permissions.

**The declarative document.** Configuration only: no records, no people.

**A hand-written schema.** It drifts from the API; a generated one cannot.

## History

- 2026-11-23: proposed with sprint 6.3.
