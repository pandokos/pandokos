---
id: SPRINT-6.2
title: Reports and audit reading
epic: EPIC-6
release: v0.2
status: in-progress
design: draft 2026-11-22
---

# Sprint 6.2 — Reports and audit reading

- Epic: [6 — Observability, reports and lifecycle](../epic.md)
- Implementation plan: [plan.md](plan.md)
- Previous sprint: [6.1 — Metrics and conventions](../6.1-metrics-and-conventions/sprint.md)
- Scope: the epic file, section 6.2, with the items carried forward to 6.2 by the closed epics
  ([epic 3](../../../../docs/history/epic-03-gateway/README.md),
  [epic 4](../../../../docs/history/epic-04-identity-and-rbac/README.md),
  [epic 5](../../../../docs/history/epic-05-limits-and-budgets/README.md)) and by section 11 of
  sprint 6.1.
- Risk class: **high** (a migration that rewrites every existing audit row once and changes how
  every audit row is written; a new trigger on the path of every administrative change and of three
  functions of the gateway; the reader that the limit counters and the capability check depend on
  replaced by a faster one that must give the same figures; the first functions that remove rows of
  append-only tables; new read paths over usage and audit, which ADR 0054 and ADR 0061 constrain)
- Decisions implemented: [ADR 0013](../../../../docs/adr/0013-ledger-and-audit.md) (reading,
  rollups, hash chain, retention);
  [0019](../../../../docs/adr/0019-configuration.md) (export and apply; three keys);
  [0032](../../../../docs/adr/0032-audit-log-first-version.md) (the chain, the read grant);
  [0054](../../../../docs/adr/0054-usage-data-is-the-tenants.md) (reports are the tenant's);
  [0053](../../../../docs/adr/0053-tenant-price-lists-and-active-currency.md) (costs per currency and
  price source, never converted);
  [0061](../../../../docs/adr/0061-support-actions-and-reasons-how-the-platform-acts-inside-a-tenant.md)
  (`audit:read` and the attribute `Support`);
  [0051](../../../../docs/adr/0051-period-counters-and-the-ledger.md) (the counters read the ledger);
  [0043](../../../../docs/adr/0043-tenant-capability-check-and-usage-list.md) (the capability check);
  [0028](../../../../docs/adr/0028-admin-api-generator.md) (streamed answers);
  [0062](../../../../docs/adr/0062-metrics-instruments-attributes-cardinality-and-scrape.md) (five
  instruments); [0063](../../../../docs/adr/0063-the-log-redaction-and-the-log-reference.md) (six
  log lines); [0020](../../../../docs/adr/0020-error-contract.md) (no new code).
- Decision records changed by this sprint: new **ADR 0064** (usage rollup, reports and exports),
  **ADR 0065** (the audit hash chain, reading and retention; amends ADR 0013 on audit retention) and
  **ADR 0066** (declarative export and apply; refines ADR 0019); history lines on 0013, 0019, 0028,
  0032, 0039, 0043, 0051, 0054, 0061 and 0062. Text in [plan.md](plan.md), part E.

This document is the design. The implementer follows [plan.md](plan.md); a gap in either document is
raised as a question, not filled silently ([planning/README.md](../../../README.md)).

| Given by the epic and the closed sprints | Designed in |
|---|---|
| Usage rollups maintained by the worker in each tenant's session | §5.1, D-1, D-2 |
| The rollup serves the limit counters and the capability check (5.2 §11), with the same figures and the credential rule (`limits.Sum`) | §5.2, D-3 |
| Report API by period, provider, model and caller; the tenant's day and month (`limits.TenantCalendar`); costs per currency and price source, never converted or added; `rejected_limit` apart | §5.3, D-4 |
| No platform aggregate per tenant; the platform sees operational indicators only (ADR 0054) | §6, D-5 |
| The "used against limit" endpoint (5.2 O-15, deferred to here) | §5.4, D-6 |
| Usage export as NDJSON and CSV | §5.5, D-7, D-8 |
| Audit reading API with filters, `support_action_id` filter, `auth.failure` rows of both listeners (`detail.listener`) | §5.7, D-13 |
| `audit:read` for `owner`, `tenant-admin`, `auditor`, not `read-only`; `Support` or not; a platform permission to read the platform's log (6.1 §11) | §5.8, D-14, D-15 |
| Per-tenant hash chain; `pandokos audit verify`; audit export | §5.6, §5.9, D-9 to D-12 |
| Retention for usage and audit; partition dropping through the audited function; the decision on rows in the default partition | §5.10, D-16 to D-18 |
| Restriction of accepted metadata keys per tenant (PRV-03) | §5.11, D-19 |
| `pandokos export` and `pandokos apply` (CAT-10, ADR 0019), with rules and price lists; `apply` adds versions, never rewrites history | §5.12, D-20, D-21 |
| Refused grants and refused support attempts as events | D-23 (stay log lines; 6.4) |
| The volume of `rejected_limit` rows (5.1 O-11), with the figures of 6.1 | D-24 |
| Exit: a tenant's usage viewer obtains last month's cost by model and cannot see another tenant's | T-955 |
| Exit: altering an audit row by hand makes verification fail | T-938 |
| Exit: `apply` of an exported document is a no-op | T-971 (and T-969) |

## 1. Outcome

1. The worker keeps a **usage rollup** per tenant, in the tenant's session, advanced by the time of
   recording so that no late record is missed; one **exact reader** answers every aggregate from it
   and from the ledger, so the limit counters, the rebase and the capability check get the figures
   they get today, faster.
2. A tenant role with `usage:read` gets a **report** by the tenant's day, month or window, by
   provider, model, caller and key, per currency and price source, never converted; reads **how much
   of a limit rule is used**; and **exports** its records as NDJSON or CSV.
3. Every audit row is **sealed by the database** in a hash chain per tenant and for the platform,
   whoever writes it; the rows written before are sealed once by the migration.
4. `audit:read` (`owner`, `tenant-admin`, `auditor`) and `platform_audit:read` (both platform roles)
   **read and export** the logs with filters; `pandokos audit verify` fails, naming the row, when a row
   was altered, removed, inserted or reordered by hand; an auditor verifies the chain without reading
   the prices recorded in it.
5. **Retention** of usage (whole monthly partitions) and audit (the oldest rows of each chain, with
   its anchor moved), off by default, every removal audited.
6. A tenant **restricts the metadata keys** it accepts.
7. `pandokos export` and `pandokos apply` manage the catalogue and a tenant declaratively; `apply` of
   an exported document is a no-op.

## 2. Scope

### In scope

- Migration `0015_reports_audit` (plan A1, run on PostgreSQL 16.15 while designing) and the
  statements of plan A2.
- New packages `internal/auditchain` (canonical encoding, hash, verifier), `internal/reports` (rollup
  and retention jobs), `internal/declare` (export and apply).
- `internal/admin` (twelve operations, §5.13), `internal/apigen` (streamed answers, raw JSON members),
  `internal/authz` (two permissions), `internal/limits` (period bounds of a report),
  `internal/catalog`, `internal/gateway` and `internal/usage` (metadata keys), `internal/cli`,
  `internal/config` (three keys), `internal/telemetry` (five instruments), `cmd/pandokos` (wiring).
- Tests at every level, documentation, ADR 0064 to 0066, the draft release notes.

### Not in scope

| Item | Where |
|---|---|
| Retention configured per tenant (partitions hold every tenant; a per-tenant setting needs row deletion) | not planned; O-3 |
| Retention of `limit_event` and `price_import` | 6.3 (with tenant deletion), on the mechanism of D-17 |
| Pseudonymization of a deleted tenant's records, and what it means for its chain | 6.3 (§11) |
| Refused grants and refused support attempts as recorded events | 6.4 as events if wanted (D-23, O-12) |
| Forwarding audit rows to an external system as they happen | 6.4 (the export and the head meet AUD-06 until then) |
| Joining platform rows about a tenant (model grants) into the tenant's log (2.2 O-7) | not planned (D-13) |
| Platform-level issuers, tenant token issuers and subject mappings in `export`/`apply`; members, credentials, keys | issuers and mappings: 12.3 ("an instance defined by configuration alone"); members, credentials and keys: never |
| A `--prune` of `apply` | not planned (D-21, O-7) |
| Reports for platform roles; any platform aggregate per tenant | never (ADR 0054) |
| Grouping a report by metadata value, operation or endpoint; a web page for reports | after 1.0, on request |

## 3. Requirements claimed

The meaning is quoted from [docs/requirements](../../../../docs/requirements/README.md).

| ID | Meaning | Delivered here | Extent |
|---|---|---|---|
| USE-06 | "Aggregated reports are available to the tenant, by period, provider, model and caller" | `getUsageReport` (§5.3) on the rollup (§5.1, §5.2) | full |
| USE-07 | "Tenant roles read their tenant's usage. Platform roles read no usage of a tenant: they see operational indicators of the installation, with no tenant and no cost" | Reports, limit usage and exports are tenant operations under `usage:read`; no platform route or definer function returns usage (§6); the instruments of §5.15 carry no tenant | full (R2 part) |
| USE-08 | "Retention is configurable and usage can be exported in an open format" | `retention.usage_months` (§5.10); `exportUsageRecords` as NDJSON and CSV (§5.5) | full |
| AUD-04 | "Audit records cannot be changed or deleted by runtime identities, and tampering is detectable" | The chain sealed by the database (§5.6), `pandokos audit verify` (§5.9); append-only kept for runtime roles (D-17) | full (the second half, R2) |
| AUD-05 | "Tenant roles read their tenant's audit log. Platform auditors read platform events" | `listAuditEvents`, `exportAuditEvents`, `getAuditHead` under `audit:read`; the platform's three under `platform_audit:read` (§5.7, §5.8) | full |
| AUD-06 | "Audit retention is configurable, and the log can be exported or forwarded to an external system" | `retention.audit_months` (§5.10); the NDJSON export and the head for external anchoring (§5.7) | full (forwarding as it happens: 6.4) |
| CAT-10 | "The catalogue can be exported and imported declaratively" | `pandokos export` and `pandokos apply` (§5.12): the catalogue (providers, endpoints, models, grants, price overrides, ceilings) and a tenant's configuration | full |
| PRV-03 | "Caller metadata is bounded in number and size, and a tenant can restrict the accepted keys" | The bound exists (3.2); `metadata_key_policy` and its three operations (§5.11) | full |
| AUD-01 | "Pandokos audits authentication failures and every creation, change, disabling, revocation and deletion of …" | Touched: four new actions (`usage.retention`, `audit.retention`, `tenant.metadata_keys_set`, `tenant.metadata_keys_clear`) | touched |
| AZ-10 | "Managing credentials, managing identities and viewing costs can be granted separately" | Touched: `audit:read` does not reveal prices to a holder without `price_list:read` (D-12) | touched |

## 4. Repository layout

New: `internal/store/migrations/sql/0015_reports_audit.sql`, `internal/store/queries/sql/reports.sql`,
`internal/auditchain/`, `internal/reports/`, `internal/declare/`,
`internal/admin/{audit_read,reports,exports,limit_usage,metadata_keys}.go`,
`internal/cli/{audit,reports,declare}.go`, `internal/store/migrations/upgrade_0015_db_test.go`,
`test/e2e/{reports,audit,declare}_test.go`, `test/repo/reports_test.go`, `docs/operations/{audit,declarative}.md`,
`docs/adr/0064-usage-rollup-reports-and-exports.md`, `docs/adr/0065-the-audit-hash-chain-reading-and-retention.md`,
`docs/adr/0066-declarative-export-and-apply.md`.

Changed: `internal/store/queries/sql/{audit,limits}.sql` and the generated package,
`internal/store/migrations/migrations.sum`, `internal/limits/zones.go`, `internal/apigen/main.go`,
`api/openapi/admin.v1.yaml`, `internal/adminapi` (generated), `internal/authz/{authz,roles}.go` and
`testdata/roles.golden`, `internal/catalog/{access,load}.go`, `internal/gateway/handler.go`,
`internal/usage/metadata.go`, `internal/config/{config,keys}.go`, `internal/telemetry/{instruments,sources}.go`,
`cmd/pandokos/{main,serve,metrics,usage}.go`, `internal/cli/{cli,commands}.go`, `test/isolation/spec.go`,
the tests of §9 "Changed tests", the documents of plan part E.

## 5. Architecture

### 5.1 The rollup (D-1, D-2)

`usage_rollup` (tenant-scoped, forced row-level security, canonical policy) has one row per
`(hour, principal_kind, principal_id, key_id, provider, model, model_id, currency, price_source,
outcome, credential)`, where `hour` is `date_bin('1 hour', started_at, '2000-01-01 00:00:00+00')`
(the UTC hour, independent of the session's time zone), `model_id`, `currency` and `price_source` are
`''` when the record has none (`price_source` mapped as `listUsageRecords` maps it: a record with a
price version and no source is `catalogue`), and `credential` is `credential_id IS NOT NULL`. Its
measures: `requests`, the seven token columns summed as recorded (provider and estimated apart,
USE-04), `cost` (the sum of known costs), `cost_known` and `cost_unknown` (counts).

`usage_rollup_state` holds one `watermark` per tenant: every record whose `recorded_at` is before it
is in the rollup, none at or after it. `recorded_at` is the database's `now()` at insert (the gateway
has no grant on the column).

**The job** (`reports.Rollup`, worker role, every `reports.rollup_interval`, default 1 min): lists
the tenant IDs in platform scope (`ListTenantsForWorker`: identifiers of tenants that are `active`,
`suspended` or `deleting`, nothing else of them), then for each tenant, in one transaction of the
tenant's session:

1. `LockUsageRollup` creates the state at the oldest `recorded_at` of the tenant (or one minute ago)
   or locks it as it is. Two workers on one tenant serialize here (epic 8 safe).
2. `RollUpUsage` adds every record recorded in `[watermark, upto)` to the rollup by an additive
   upsert and moves the watermark to `upto`, where `upto` = min(now − 1 min, watermark + 7 days),
   never below the watermark.
3. Repeat (each a transaction) until the watermark is within two minutes of now or the pass has used
   its budget of 30 s for this tenant; the next tick continues.

**Why no record is lost.** A record is counted when its `recorded_at` lies in a processed window.
`recorded_at` is the start time of the inserting transaction; the ledger writer's insert transaction
ends within `writeTimeout` (5 s, `internal/ledger/writer.go`), so a transaction that started before
`now − 1 min` has committed or failed when the window is read (T-950 pins the relation). A record
with an old `started_at` but a recent `recorded_at` (a backlog after an outage) falls in a later
window and is added to its old hour (T-949).

**Cost.** One upsert per tenant and minute over the records of that minute, by the new index
`usage_record (tenant_id, recorded_at)`. A rollup that stops (worker down, database errors) costs
speed, never correctness: the exact reader reads the ledger for what the rollup does not hold.

### 5.2 The exact reader and the limit counters (D-3)

Every aggregate is a sum over the records whose `started_at` lies in a window, grouped by periods.
The reader splits the window:

- **Clean hours**: whole UTC hours inside the window that no period boundary cuts. For them it takes
  the rollup rows **plus** the ledger records of those hours recorded at or after the watermark.
- **Everything else** (the partial hours at the edges, hours cut by a boundary inside the window):
  the ledger records, all of them.

The two parts are disjoint and cover the window, and both are read in one statement (one snapshot),
so the watermark, the rollup and the ledger agree. The sum is the sum over the records (T-947, a
property test over random records, random recording times, random rollup runs and random windows,
zones with offsets of 30 and 45 minutes included).

**Limits.** `AggregateUsageForLimits` keeps its name, parameters and result columns; its body becomes
the exact reader with periods `[from_time, live_from)` (minute 0) and per-minute groups after
`live_from` (always from the ledger: `live_from` is the reconciler's cut, about 12 min ago). The old
body is kept as `AggregateUsageForLimitsFromLedger` for the property test only (T-R74 forbids it
elsewhere). `limits.Ledger.Usage`, `limits.Sum`, the reconciler, the hourly rebase and the capability
check change nothing: they get the same rows, faster (T-951). The note of 5.2 §11 ("the capability
check reads the ledger on the worker pool, one aggregate per call, and ignores requests in flight")
stays true except for the cost.

### 5.3 The report (D-4, D-5)

`GET /v1/tenants/{tenant}/usage-report` (`getUsageReport`, `usage:read`):

- `from`, `to`: a civil date `YYYY-MM-DD` (the start of that day in the tenant's calendar) or an
  RFC 3339 instant. Defaults: `from` = the start of the current month of the tenant, `to` = now.
  `from < to`, at most 400 days.
- `period`: `day`, `month` (default) or `total`. Period boundaries are those of `limits.TenantCalendar`
  (the zone in force at each instant, a pending change of zone included); the first and last groups
  are clipped to `[from, to)`.
- `group_by`: a comma-separated subset of `provider`, `model`, `caller`, `key` (default `model`).
  `caller` is the principal (`principal_kind`, `principal_id`); `key` is `key_id`.
- `provider`, `model`: filters on the names as recorded.
- Rows are always split by `currency` and `price_source`; `cost` is a decimal string, `null` when no
  record of the row has a known cost. No field adds two currencies or two price sources. Counts:
  `requests` (all but `rejected_limit`), `requests_ok`, `rejected_limit` (apart, 5.2 §11),
  `cost_unknown` and the seven token sums.
- More than 10 000 rows: `400 invalid_request` with `errors[0] = {field: "group_by", message: "the
  report has more than 10000 rows: narrow the window, the period or the grouping"}`.

The handler computes the boundaries in Go (`limits.TenantCalendar.Bounds`), runs `UsageReport` in the
tenant's session on the **admin** pool and maps periods back to `[period_start, period_end)`.

### 5.4 Usage against a limit rule (D-6)

`GET /v1/tenants/{tenant}/limits/{rule}/usage` (`getLimitUsage`, `usage:read`): for a rule of the
tenant with a day or month metric, the period at now in the tenant's calendar and what the ledger
holds for it, through `limits.Ledger.Usage` and `limits.Sum` (one definition, 5.2): `used` (requests or
tokens) or `used_amount` (cost, in the rule's currency), `period_start`, `period_end`, `as_of`.
Requests in flight are not counted (the counters' reservations are per instance, ADR 0051); the
description says so. A per-minute rule: `409 invalid_state`.

### 5.5 Usage export (D-7, D-8)

`GET /v1/tenants/{tenant}/usage-records/export` (`exportUsageRecords`, `usage:read`): `from`, `to`
(RFC 3339; default and bound as `listUsageRecords`: at most 31 days), `provider`, `format` (`ndjson`,
default, or `csv`). The answer is streamed: `Content-Type: application/x-ndjson` (one `UsageRecord`
JSON object per line, the schema of `listUsageRecords`, oldest first by `(started_at, id)`) or
`text/csv; charset=utf-8` (a header line, then one line per record; the columns are the members of
`UsageRecord` in schema order, arrays and objects as their JSON text in one cell, costs as decimal
strings). `Content-Disposition: attachment; filename="usage-<tenant>-<from>-<to>.<ext>"`.

The handler reads pages of 1000 records by keyset, each in its own tenant transaction, and writes
them as it reads. A record committed during an export with a key the export has passed is not in it;
records never change, so none appears twice. Every page extends the write deadline of the
connection by 60 s (the admin listener's `WriteTimeout` is 60 s) and the export is bounded to 15 min.

**CSV injection.** A text cell whose first character is `=`, `+`, `-`, `@`, a tab or a carriage
return is written with a leading `'` (OWASP's advice); numbers are not. NDJSON is never altered.

**Failure mid-stream.** Before the first byte the answer is the usual problem. After it, the handler
aborts the response (`http.ErrAbortHandler`): the chunked stream ends without its terminator, so every
client sees an incomplete transfer, never a truncated file that looks complete; the line `export
aborted` is logged.

### 5.6 The audit hash chain (D-9 to D-11)

Each tenant has one chain (`audit_event` rows of that tenant) and the platform one
(`platform_audit_event`). Every row gets, in a `BEFORE INSERT` trigger that runs as the owner
(`audit_seal_tenant`, `audit_seal_platform`, replacing `audit_stamp`):

- `id` (UUIDv7) and `occurred_at` (`now()`), as before;
- `seq`: the head's `seq` + 1, unique per chain;
- `prev_hash`: the head's `hash` (32 zero bytes for the first row);
- `hash` = `audit_row_hash(...)`; then the head (`audit_chain` per tenant, `platform_audit_chain`)
  takes `seq` and `hash`.

The head row is locked `FOR UPDATE` until the transaction ends, so rows of one chain get consecutive
numbers in commit order, and a row that is not inserted (a failed statement, a rolled-back
transaction or subtransaction) takes its change of the head with it. The tenant trigger reads and
writes the head as the session of the row's tenant (it sets `app.tenant_id` to `NEW.tenant_id` for
its own statements and restores it), so a migration or the owner can still insert; a runtime role
that names another tenant is refused by the policy of `audit_event`, which is checked after the
trigger.

**Canonical encoding v1** (ADR 0065; SQL in plan A1, Go in `internal/auditchain`):

```
F(s)  = uint32 big-endian byte length of UTF-8(s) ‖ UTF-8(s)
C(v)  = 'o' ‖ uint32(n) ‖ for each member, keys sorted by their UTF-8 bytes: F(key) ‖ C(value)
      | 'a' ‖ uint32(n) ‖ C(element)…            | 's' ‖ F(string)
      | 'n' ‖ F(number as PostgreSQL writes it)  | 't' | 'f' | 'z' (null)
hash  = SHA-256( F("pandokos-audit-v1") ‖ prev_hash ‖ F(scope) ‖ F(seq) ‖ F(id) ‖ F(occurred_at)
                 ‖ F(actor_type) ‖ F(actor_id) ‖ F(actor_name) ‖ F(key_id) ‖ F(action)
                 ‖ F(resource_type) ‖ F(resource_id) ‖ F(outcome) ‖ F(request_id) ‖ F(reason)
                 ‖ F(support_action_id) ‖ SHA-256(C(detail)) )
```

`scope` is `tenant:<tenant id>` or `platform`; `seq` in decimal; `id` and `support_action_id` as
lower-case UUID text (`''` when absent, always for the platform); `occurred_at` as
`YYYY-MM-DDTHH:MM:SS.ffffffZ` in UTC. A number's text is the one `jsonb` stores (`1.50` stays
`1.50`, `1e3` is `1000`); the API passes `detail` through as the database's text and the verifier
decodes it with `json.Number`, so no float ever enters. The tag bytes are ASCII.

The chain commits to the **digest of the detail**, not to its text: that is what lets a reader verify
a row whose detail it may not read (§5.8).

**The rows written before 0015** are sealed once by the migration, chain by chain, in the order
`(occurred_at, id)` (T-937). **`record_unlimited_traffic`** used `INSERT … ON CONFLICT DO NOTHING`,
whose `BEFORE` trigger would advance the head for a row that is then not inserted; it now inserts in
a subtransaction and treats `unique_violation` as "already written" (same signature and answer,
T-935).

**Lock order.** A transaction that writes both logs writes the tenant's first
(`audit.RecordIntervention` already does; T-934 runs interventions and tenant writes concurrently).

### 5.7 Reading the audit log (D-13)

| Operation | Path | Permission | Answer |
|---|---|---|---|
| `listAuditEvents` | `GET /v1/tenants/{tenant}/audit-events` | `audit:read` | a page, newest first by `seq` |
| `getAuditHead` | `GET /v1/tenants/{tenant}/audit-head` | `audit:read` | `seq`, `hash`, `anchor_seq`, `anchor_hash` |
| `exportAuditEvents` | `GET /v1/tenants/{tenant}/audit-events/export` | `audit:read` | NDJSON, oldest first, from `from_seq` (default the anchor) to `to_seq` (default the head read at the start) |
| `listPlatformAuditEvents` | `GET /v1/platform-audit-events` | `platform_audit:read` | as above |
| `getPlatformAuditHead` | `GET /v1/platform-audit-head` | `platform_audit:read` | as above |
| `exportPlatformAuditEvents` | `GET /v1/platform-audit-events/export` | `platform_audit:read` | as above |

Filters of the lists: `from`, `to` (RFC 3339, on `occurred_at`), `action` (exact, or a prefix ending
in `.` such as `member.`), `resource_type`, `resource_id`, `actor_type`, `actor_id`, `outcome`,
`request_id`, and for a tenant `support_action_id`. A cursor is bound to the filters. An event carries
every column, `seq`, `prev_hash` and `hash` in lower-case hex, `detail` as a JSON object and
`detail_withheld` (§5.8). `auth.failure` rows of both listeners are returned as written, with
`detail.listener` `inference` or `admin` (T-940).

The tenant's log does not show platform rows about the tenant (model grants, 2.2 O-7 stands):
interventions are already written to both logs (ADR 0061), and nothing reads the platform log in a
tenant's session.

### 5.8 Who reads what (D-12, D-14, D-15)

- **`audit:read`** (tenant scope, not `Support`): `owner` (all tenant permissions), `tenant-admin`
  (all but owner-only), `auditor` (added). `read-only` is built from the tenant `:read` permissions
  minus an explicit exclusion list that holds `audit:read`. Not `Support`: the log records price
  rates (`tenant_price.create`), budget amounts and other configuration, and ADR 0061 point 3 keeps
  every permission that reads prices out of a support action; a platform administrator reads what it
  did in a tenant in its own log (interventions are written to both).
- **`platform_audit:read`** (platform scope, no attribute): `platform-admin` (all platform
  permissions) and `platform-auditor` (added), like `metrics:read` (6.1 §11).
- **Withheld details.** The detail of an event whose action is in `admin.PriceDetailActions`
  (`tenant_price.create`, `tenant_price.delete`, `tenant.active_currency_update`: every action whose
  detail holds a rate, an amount of a price list or the active currency; the list is checked against
  the actions of the code by T-941) is returned to a principal **without** `price_list:read` as
  `detail: {}`, `detail_withheld: true` and `detail_digest` = hex of SHA-256(C(detail)). The verifier
  uses the digest in place of hashing the detail, so the chain verifies end to end (T-941, T-939).
  `auditor` and `tenant-admin`/`owner` thus differ: the auditor sees that a price changed, when and by
  whom, not the rate (AZ-10).

### 5.9 `pandokos audit verify` (D-10)

```
pandokos audit verify (--tenant ID | --platform) [--file FILE] [--expect-head HEX] [--from-seq N]
```

Without `--file` it reads the head, then the export up to that head, through the API. With
`--file` it verifies an NDJSON export offline (no server needed); the file may start at any row, and
the link to the rows before it is reported as not verified. It checks, row by row: `seq` contiguous
from the first row (the anchor, or `--from-seq`); `prev_hash` equal to the previous row's `hash` (the
first row's to the head's `anchor_hash` when it starts at the anchor); `hash` equal to the
recomputation (with `detail_digest` for a withheld row); and at the end the last row equal to the head
(`seq` and `hash`) and to `--expect-head` when given.

Output: `verified <n> rows, seq <a>..<b>, head <hash>` and exit 0; or one line per break,
`break at seq <n>: <reason>` with reason `hash_mismatch`, `prev_mismatch`, `seq_gap`, `anchor_mismatch`,
`head_mismatch` (the log ends before the head, or past it), `expected_head_mismatch`, and exit 1.
A malformed or truncated input is exit 1 with `invalid export: <what>`.

What it proves and what not, stated in the audit guide: a row changed, removed, inserted or moved by
someone who did not recompute the rest of the chain is detected; someone who can write the tables can
rewrite the whole chain and its head, which only an anchor kept elsewhere (`--expect-head` with a head
taken earlier) detects (ADR 0013).

### 5.10 Retention (D-16 to D-18)

Two keys, read at start, both `0` (keep everything) by default:

- `retention.usage_months`: `0`, or 2 to 120. Keeps the current UTC month and the N full months
  before it.
- `retention.audit_months`: `0`, or 1 to 1200. Keeps the audit rows written since the start of the
  current UTC month minus N months.

The job (`reports.Retention`, worker role) runs at start, after the partitions are ensured, and then
every hour (it does nothing when there is nothing older than the cut):

1. For each tenant, in its session, `tenant_retention(usage, audit)`: removes the tenant's rollup rows
   and its rows of `usage_record_default` before the usage cut, and the rows of its chain before the
   audit cut **from the start of the chain** (every row up to the last `seq` written before the cut);
   moves the anchor (`anchor_seq`, `anchor_hash` = the hash of the last removed row); writes
   `usage.retention` and `audit.retention` in the tenant's log when it removed something.
2. In platform scope, `platform_retention(usage, audit)`: drops each monthly partition that ends at or
   before the usage cut (`DROP TABLE`, under the advisory lock of `ensure_usage_partitions` and a 5 s
   lock timeout), removes the platform log's rows before the audit cut the same way, and writes
   `usage.retention` (the partitions dropped) and `audit.retention` in the platform log.

Removal goes through `append_only`, which now lets a `DELETE` through when the setting
`pandokos.retention` is `on` in the transaction; only the two functions set it, around their own
`DELETE`. No runtime role holds `DELETE` on an append-only table, so for them the setting changes
nothing (L-17 unchanged, T-961). `TRUNCATE` and `UPDATE` stay refused for everybody.

**The default partition** (the decision carried from 3.2): rows that landed there stay readable,
counted by the rollup and the reports like any other, and are removed by `tenant_retention` with their
month; `ensure_usage_partitions` and its warning are unchanged.

**Audit retention by chain prefix, not by partition** (ADR 0065 amends ADR 0013): the audit tables
are not partitioned, and a partitioned table cannot keep the unique keys of `limit.unlimited_traffic`
(they do not contain the partition key); the volume of the logs is small; a chain needs an anchor
whatever removes its first rows.

### 5.11 Metadata keys a tenant accepts (D-19)

`metadata_key_policy` (tenant-scoped, as `tenant_model_restriction` is for models): no row, every key
with the format of 3.2; a row, only the keys it lists (0 to 64, each of the key format). Operations
`getMetadataKeys` (`usage:read`), `setMetadataKeys` and `clearMetadataKeys` (`tenant_setting:write`,
whose description gains "and the metadata keys it accepts"), each change audited
(`tenant.metadata_keys_set`, `tenant.metadata_keys_clear`, detail `keys`).

The gateway reads the row with the tenant's catalogue view (`catalog.TenantAccess.MetadataKeys`, nil
for no row; invalidated by `catalog:<tenant>`). Parsing stays at step 3b; at step 7, when the view is
in hand, keys outside the list move from the kept set to the dropped count, and the
`X-Pandokos-Metadata-Dropped` header is updated before anything is forwarded. A request that ends
before step 7 records no metadata and counts every entry it kept as dropped (the tenant's list is
not known yet, and storing a key the tenant refused is what PRV-03 forbids).

### 5.12 Declarative export and apply (D-20, D-21)

A **document** (YAML, `pandokos: v1`, `kind: platform` or `kind: tenant`) lists resources by their
natural keys, with the fields of their create bodies minus what the server sets (IDs, times, computed
states). Full schema in `docs/operations/declarative.md` (plan E1), Go types in `internal/declare`.

| Kind | Sections and natural keys |
|---|---|
| `platform` | `providers` (name; with `endpoints` by name), `models` (provider, name), `model_grants` (provider, model, tenant **name**), `price_overrides` (provider, model, `valid_from`), `platform_limits` (tenant name or `default`, provider, model, metric, currency) |
| `tenant` | `settings` (`time_zone`, `active_currency`, `metadata_keys`), `model_restriction`, `roles` (custom, by name), `service_accounts` (name, with their `roles`), `limits` (subject, provider, model, metric, currency; subject `tenant`, `service_account: <name>` or `key: <key ID>`), `price_lists` (currency; `versions` by provider, model, `valid_from`) |

`pandokos export (--platform | --tenant ID) [--out FILE]` reads through the API and writes a
deterministic document (sections and items sorted by natural key). It never contains a secret, a key,
a credential, a person, a usage figure or an ID that is not portable (a rule on a subject mapping is
left out and listed in a comment line at the end; a key-scoped rule keeps the public key ID).

`pandokos apply --file FILE (--platform | --tenant ID) [--dry-run]`:

- Reads the current state through the API, compares by natural key, and plans: **create** what is
  absent, **update** a mutable field that differs (with the update operation of that resource),
  **report** an immutable field that differs (`conflict`, not changed), **leave alone** and report what
  exists but is not in the document. Nothing is ever deleted.
- **Versions are only added**: a price version (tenant or override) whose `(provider, model,
  valid_from)` exists is unchanged when its rates are equal and a `conflict` otherwise; an absent one
  is created when `valid_from` is now or later and reported `skipped: in the past` otherwise.
- Settings that take effect later (time zone, active currency) compare the value set, pending
  included, so the document just exported compares equal.
- Order: platform — providers, endpoints, models, grants, overrides, ceilings; tenant — settings
  (zone, metadata keys), custom roles, service accounts and their roles, model restriction, limits,
  price lists, then the active currency (a budget may need its list first).
- Output: one line per change (`create service_account billing`, `update limit …`, `conflict …`,
  `skipped …`, `not in document …`) and `<n> changes, <m> conflicts, <k> left alone`. Exit 0 when
  every planned change succeeded (conflicts and "left alone" are reported, not failures), 1 when a call
  failed (the rest is not attempted; re-running converges), 2 for a usage error or an invalid document.
- On the document just exported the plan is empty: no write call, `0 changes` (T-969, T-971).

### 5.13 Operations added to the OpenAPI document

Twelve operations (full text in plan A3): `getUsageReport`, `exportUsageRecords`, `getLimitUsage`,
`listAuditEvents`, `getAuditHead`, `exportAuditEvents`, `listPlatformAuditEvents`,
`getPlatformAuditHead`, `exportPlatformAuditEvents`, `getMetadataKeys`, `setMetadataKeys`,
`clearMetadataKeys`. The three exports are **streamed answers**, a construct the generator gains
(D-8, ADR 0064 and a history line on 0028): a `200` whose content is `application/x-ndjson`, optionally
also `text/csv`, each with the schema of one line for the document; the server method returns
`*adminapi.Stream{ContentType, Filename, Write func(ctx, w io.Writer) error}`, the client method
`*adminapi.StreamBody{ContentType string; Body io.ReadCloser}`; a `format` query parameter picks the
content when there are two. The scrape endpoint of 6.1 stays outside the document.

### 5.14 Command line

| Command | Operation |
|---|---|
| `usage report --tenant ID [--from D] [--to D] [--month YYYY-MM] [--period day\|month\|total] [--group-by …] [--provider P] [--model M]` | `getUsageReport` (`--month` sets `from` and `to` to that month's first days) |
| `usage export --tenant ID [--from T] [--to T] [--provider P] [--format ndjson\|csv] [--out FILE]` | `exportUsageRecords`; a window over 31 days is split into consecutive requests; `--out` writes a temporary file renamed at the end |
| `limit usage --tenant ID RULE` | `getLimitUsage` |
| `metadata-keys get\|set\|clear --tenant ID [KEY…]` | the three operations |
| `audit list\|head\|export --tenant ID …` | the three tenant operations |
| `platform-audit list\|head\|export …` | the three platform operations |
| `audit verify`, `platform-audit verify` | composite (§5.9) |
| `export`, `apply` | composite (§5.12) |

Composite commands live in a second table (`internal/cli.composites`) so that T-219 ("every operation
has exactly one command") is unchanged.

### 5.15 Metrics and logs (D-22)

Five instruments, no tenant attribute (ADR 0062), added to `telemetry.Catalogue()` and the metrics
reference (T-R65), read at collection from `reports.Rollup.Stats()`, `reports.Retention.Stats()` and
`admin` export counters:

| # | Name | Kind | Unit | Attributes | Source |
|---|---|---|---|---|---|
| M42 | `pandokos.reports.rollups` | OC | `{run}` | `pandokos.result` (`ok`, `failed`) | one per tenant pass |
| M43 | `pandokos.reports.rollup.lag` | G | `s` | — | the largest now − watermark among the tenants of the last pass; 0 before the first |
| M44 | `pandokos.retention.removed` | OC | `{item}` | `pandokos.retention.kind` (`usage_partition`, `usage_record`, `usage_rollup`, `audit_event`, `platform_audit_event`) | the results of the two functions, summed over tenants |
| M45 | `pandokos.retention.runs` | OC | `{run}` | `pandokos.result` (`ok`, `failed`) | one per job run |
| M46 | `pandokos.exports` | OC | `{export}` | `pandokos.export.kind` (`usage`, `audit`, `platform_audit`), `pandokos.result` (`complete`, `aborted`) | the export handlers |

Log lines (rows of `docs/reference/logs.md`, T-930 and T-R66 enforce them):

| Message | Level | Fields | When |
|---|---|---|---|
| `retention ready` | info | `usage_months`, `audit_months` | at start, with the worker role |
| `retention applied` | info | `partitions`, `usage_records`, `audit_events`, `platform_audit_events` | a run removed something (totals over the installation) |
| `retention failing` | warn | `error_class`, `sqlstate`, `failures` | a run failed; at most once an hour |
| `usage rollup failing` | warn | `error_class`, `sqlstate`, `failures` | a tenant pass failed; at most once a minute |
| `usage rollup recovered` | info | `failures` | the first pass that succeeds after failures |
| `export aborted` | warn | `request_id`, `error_class`, `duration_ms` | an export failed after its first byte |

No line names a tenant, a partition month of a tenant, a count of one tenant or a row (ADR 0055).

### 5.16 Configuration (D-16, D-2)

| Key | Environment | Type | Default | Rule |
|---|---|---|---|---|
| `reports.rollup_interval` | `PANDOKOS_REPORTS_ROLLUP_INTERVAL` | duration | `1m` | 10s to 1h; `serve` only |
| `retention.usage_months` | `PANDOKOS_RETENTION_USAGE_MONTHS` | integer | `0` | 0, or 2 to 120; `serve` only |
| `retention.audit_months` | `PANDOKOS_RETENTION_AUDIT_MONTHS` | integer | `0` | 0, or 1 to 1200; `serve` only; a value below 12 is a start-up warning (`configuration warning`, existing line) |

### 5.17 Requirements, records and plans that change

- Requirements: no wording changes; USE-06, USE-07, USE-08, AUD-04 (second half), AUD-05, AUD-06,
  CAT-10 and PRV-03 are marked delivered by the epic record when the epic closes.
- `docs/architecture/data-model.md` (rollup, chain, retention, metadata policy),
  `security-model.md` (the row "The audit log is altered", what the chain proves),
  `docs/reference/data-flows.md` (exports, the declarative document), `docs/operations/usage.md`
  (sections 7 and the new 9 to 11), `database.md` (grants of 0015, the index procedure).
- The epic file: the note of 6.2 about events (D-23) and the deferrals of §2.

## 6. Isolation and permissions

| Table / function | Scope | Gateway | Admin | Worker |
|---|---|---|---|---|
| `audit_event` | tenant | — (functions) | `INSERT` (as before), **`SELECT`** | — |
| `platform_audit_event` | global, policies | — | `INSERT`, **`SELECT`** (platform scope) | `INSERT` |
| `audit_chain` (new) | tenant | — | `SELECT` | — |
| `platform_audit_chain` (new) | global, policies | — | `SELECT` (platform scope) | — |
| `usage_rollup` (new) | tenant | — | `SELECT` | `SELECT`, `INSERT`, `UPDATE`, `DELETE` |
| `usage_rollup_state` (new) | tenant | — | `SELECT` | `SELECT`, `INSERT (tenant_id, watermark)`, `UPDATE (watermark, updated_at)` |
| `metadata_key_policy` (new) | tenant | `SELECT` | `SELECT`, `INSERT (tenant_id, keys)`, `UPDATE (keys)`, `DELETE` | — |
| `audit_seal_tenant()`, `audit_seal_platform()` | definer, triggers only | — | — | — |
| `platform_retention(int, int)` | definer, platform scope | — | — | `EXECUTE` |
| `tenant_retention(int, int)` | definer, tenant session | — | — | `EXECUTE` |
| `audit_field`, `audit_canonical`, `audit_row_hash` | invoker, owner only | — | — | — |

- Every new tenant-scoped table has the canonical policy, forced, a fixture in `Spec.Fixtures` and
  its rows in the grants matrix (T-952, L-12, L-16); `platform_audit_chain` is a restricted global
  table (two policies: the owner for the seal and retention functions; the admin role in platform
  scope).
- **The platform reads no usage of a tenant** (ADR 0054): the report, limit usage, export and rollup
  statements run only in tenant sessions; `ListTenantsForWorker` returns identifiers; the two
  retention functions return counts, never a row, and `platform_retention` touches the ledger only to
  drop whole partitions (T-747 changed, T-R71).
- **Every new route declares its permission** and is covered by the cross-tenant harness (T-270
  classifies the three platform operations; T-944 for each built-in role; T-955 end to end).
- `getUsageReport`, `getLimitUsage`, `exportUsageRecords` are refused to a platform principal even
  under a support action (`usage:read` is not `Support`); the audit operations of a tenant likewise
  (`audit:read` is not `Support`) (T-944).

## 7. Test infrastructure and CI

- `dbtest` gains `SealedRow` (insert an audit row with a chosen `occurred_at` and the chain kept,
  for retention tests: the owner disables `append_only`, writes the row and recomputes the chain with
  `audit_row_hash`) and `Tamper` (owner: disable the trigger, change or delete a row, enable it).
- `test/e2e`: `startBinary` gains `rollupInterval` and `retention` options; an e2e helper inserts
  ledger rows as the gateway role (`InsertUsageRecords`) with a chosen `started_at`.
- Nothing else changes in CI; the new tests run in the existing jobs.

## 8. Decisions

| # | Decision |
|---|---|
| D-1 | A usage rollup per tenant by UTC hour of `started_at` and by every dimension a report or a limit names, with token and cost sums kept as recorded (provider and estimated apart, costs per currency and price source) |
| D-2 | The worker advances it by `recorded_at` with a watermark per tenant, in the tenant's session: additive upserts over `[watermark, min(now − 1 min, watermark + 7 days))`, one transaction each, every `reports.rollup_interval` (1 min) |
| D-3 | One exact reader: rollup for clean hours (plus their records recorded since the watermark), ledger for the rest, in one statement; `AggregateUsageForLimits` keeps its contract and becomes this reader, so counters, rebase and capability check get the same rows |
| D-4 | `getUsageReport`: periods day, month or total in the tenant's calendar, grouping by provider, model, caller, key; always by currency and price source, never converted; `rejected_limit` counted apart; ≤ 400 days, ≤ 10 000 rows |
| D-5 | Reports, limit usage and exports are tenant operations under `usage:read`; no platform operation reads them, not even under a support action |
| D-6 | `getLimitUsage` for day and month rules: the ledger's figure through `limits.Ledger` and `limits.Sum`, in flight excluded; per-minute rules refused |
| D-7 | `exportUsageRecords`: NDJSON (the `UsageRecord` schema) or CSV, oldest first, 31-day windows, pages of 1000 by keyset in separate transactions, deadline extended per page, 15-min bound, CSV formula guard, abort on failure after the first byte |
| D-8 | The generator gains streamed answers (`application/x-ndjson`, `text/csv`): `Stream` for the server, `StreamBody` for the client, `format` to choose |
| D-9 | A hash chain per tenant and for the platform, sealed by a definer `BEFORE INSERT` trigger for every writer, serialized by a locked head row; canonical encoding v1 of §5.6, committing to the digest of the detail |
| D-10 | Verification is client-side, over the export (API or file): `pandokos audit verify` with head, anchor and `--expect-head` |
| D-11 | Existing rows are sealed once by the migration in `(occurred_at, id)` order; `record_unlimited_traffic` inserts in a subtransaction instead of `ON CONFLICT` |
| D-12 | The details of price actions are withheld from readers without `price_list:read`, with their digest, so the chain still verifies (AZ-10) |
| D-13 | Audit read API: list with filters (window, action or prefix, resource, actor, outcome, request ID, support action), head, export to the head; platform rows about a tenant are not joined into its log |
| D-14 | `audit:read` (tenant): `owner`, `tenant-admin`, `auditor`; not `read-only` (exclusion list); not `Support` |
| D-15 | `platform_audit:read` (platform): `platform-admin`, `platform-auditor` |
| D-16 | Retention by two static keys, off by default: `retention.usage_months` (whole UTC months, 2–120) and `retention.audit_months` (1–1200); hourly job of the worker |
| D-17 | `append_only` admits a `DELETE` only under `pandokos.retention = on`, which only the two retention functions set; the mechanism 6.3 reuses |
| D-18 | Ledger: whole partitions dropped by `platform_retention`; a tenant's default-partition and rollup rows removed by `tenant_retention`. Audit: the oldest rows of a chain removed as a prefix with the anchor moved (amends ADR 0013). Every removal audited |
| D-19 | `metadata_key_policy` per tenant, applied by the gateway at step 7 with the catalogue view; a request that ends before step 7 records no metadata |
| D-20 | `pandokos export`/`apply`: YAML v1 documents of kind platform or tenant, by natural key, through the API, deterministic order, no secret or person |
| D-21 | `apply` creates and updates, reports conflicts and what it leaves alone, never deletes, only adds price versions (never in the past); a no-op on its own export |
| D-22 | Five instruments (M42–M46) and six log lines, no tenant in any |
| D-23 | Refused grants and refused support attempts stay log lines and M26 (4.3 O-12); whether they become events is 6.4's |
| D-24 | `rejected_limit` rows stay one per refused request (USE-01); reports count them apart; retention bounds them; their volume under load is measured in 10.1 |
| D-25 | No new error code; the only new permissions are the two of D-14, D-15; three configuration keys; one migration; no new Go module |

## 9. Tests

Levels: U unit, I integration (in-process, may use the test database), D database (the isolation
harness and the schema), E end to end (the binary), R repository.

### New tests

| ID | Test | Level | Asserts | Requirement |
|---|---|---|---|---|
| T-933 | `isolation.TestAuditChainSeal` | D | Every writer of each log gives a row with `seq` = previous + 1 from 1, `prev_hash` = the previous `hash` (zero bytes first), `hash` = `auditchain.Hash` of the row as read, `id` and `occurred_at` the database's: `InsertAuditEvent` (admin, tenant session; under `ActInTenant`; inside `Provision`), `InsertPlatformAuditEvent` (admin and worker), `record_auth_failure`, `record_token_auth_failure`, `record_unlimited_traffic` (both logs), the owner without a session; the head equals the last row; a runtime role cannot write `seq`, `prev_hash`, `hash` (42501) nor touch a head | AUD-04 |
| T-934 | `isolation.TestAuditChainConcurrent` | D | 8 writers × 50 rows in one tenant, with 20 interventions (tenant then platform) and 20 platform rows interleaved: every transaction commits, no gap, no duplicate, heads equal the last rows | AUD-04 |
| T-935 | `isolation.TestAuditChainNotInserted` | D | A row in a rolled-back transaction, a row refused by a check (detail over 8192 bytes), a row refused by the policy (another tenant's ID) and a second `record_unlimited_traffic` of the same day each leave the head unchanged; the second call answers false; two concurrent first calls of a day give one row per log and an intact chain | AUD-04 |
| T-936 | `auditchain.TestCanonicalEncoding` | U+D | Golden byte strings for `C` over: nested objects and arrays, keys that sort differently by bytes and by code point (`"B"`, `"a"`, `"é"`, `"ab"`), escapes, an empty object and array, `1`, `1.50`, `-0.5`, `1e3` stored by `jsonb`, booleans, null; `Hash` of fixed rows equals `pandokos.audit_row_hash` run by the owner on the same values (one tenant row with a support action, one platform row) | AUD-04 |
| T-937 | `migrations.TestUpgrade0015` | D | A database at 0014 with rows in two tenants and the platform (some written by migration 0011 style, some with equal `occurred_at`): after 0015 each chain is numbered by `(occurred_at, id)`, verifies with `auditchain.Verify`, the heads equal the last rows, every other column of every row is unchanged, row-level security is forced again and `append_only` is enabled | AUD-04 |
| T-938 | `e2e.TestAuditVerifyDetectsTampering` | E | **Exit.** A tenant with 30 events from API calls (and `auth.failure` rows from both listeners); `pandokos audit verify --tenant` exits 0; then, each on a fresh copy of the log state: the owner changes `detail` of row 10, changes `actor_name` of row 3, deletes row 15, deletes the last row, swaps `seq` of rows 5 and 6, inserts a row with a forged `seq` and `hash` computed with a wrong key: each run exits 1 and names the first broken `seq` and the reason of §5.9; the same for the platform log with `platform-audit verify` | **exit**, AUD-04 |
| T-939 | `auditchain.TestVerify` | U | Synthetic exports: a valid chain; each break reason of §5.9 alone; a withheld row verified by its digest, and a withheld row whose digest is altered; a chain starting at the anchor after retention, and starting elsewhere (reported unverified link); `--expect-head` equal and different; truncated last line; a line that is not JSON | AUD-04 |
| T-940 | `admin.TestListAuditEvents` | I | Newest first by `seq`; each filter alone and combined; `action` exact and prefix (`member.`); the window; a cursor refused with other filters; `support_action_id` returns exactly the rows written under that action; `auth.failure` rows of the inference listener (key and token) and of the admin listener are listed with their `detail.listener`; every member of the schema present | AUD-05 |
| T-941 | `admin.TestAuditDetailWithheld` | I | An `auditor` gets the rows of `tenant_price.create`, `tenant_price.delete` and `tenant.active_currency_update` with `detail: {}`, `detail_withheld: true` and the digest that `auditchain` computes from the detail; an `owner` gets the detail and `detail_withheld: false`; the same in the export; the list `admin.PriceDetailActions` equals the set of actions whose detail the code writes with a rate, an amount of a price list or the active currency (a table of the audit calls, checked against `grep` of `internal/admin` in T-R75) | AZ-10, AUD-05 |
| T-942 | `isolation.TestAuditReadIsolation` | D | In tenant A's session the admin role sees A's rows and head only; no session sees `audit_event` rows without a tenant; `platform_audit_event` and its head only in platform scope; the gateway and worker roles read neither log (42501); the canonical functions are not executable by a runtime role | AUD-05, TEN-07 |
| T-943 | `authz.TestAuditPermissions` | U | `audit:read`: tenant scope, not `Support`, held by `owner`, `tenant-admin`, `auditor`, not by `read-only`, `usage-viewer`, `developer`, `credential-manager`, `inference`; `platform_audit:read`: platform scope, held by `platform-admin` and `platform-auditor`; `SupportPermissions()` unchanged (eleven); opening a support action with `audit:read` is `400 invalid_request` | AUD-05 |
| T-944 | `admin.TestReportAndAuditRoutes` | I | For each new operation and each built-in tenant role: allowed exactly when the role holds the permission; a platform key (admin and auditor) gets 403 on every tenant operation, also with an open support action carrying all eleven `Support` permissions; a tenant key gets 403 on the three platform operations; a platform key scoped to `catalog:read` gets 403 on the platform audit operations | USE-07, AUD-05 |
| T-945 | `admin.TestAuditExport` | I | NDJSON content type and disposition; oldest first; `from_seq`/`to_seq`; default from the anchor to the head read at the start (a row written during the export is not in it); an injected database failure after the first page aborts the connection (the client gets an unexpected EOF) and logs `export aborted`; before the first byte it is a problem | AUD-06 |
| T-946 | `admin.TestAuditHead` | I | The head equals the last row; after retention the anchor is the first kept row and the hash of the last removed one | AUD-04 |
| T-947 | `reports.TestExactReaderProperty` | I | 200 random runs: random records (outcomes, sources, known/unknown/none costs, two currencies, both price sources and none, credential or not, provider renamed, model ID absent), `recorded_at` spread over the run with late records of old `started_at`, rollup runs at random moments; after each: `AggregateUsageForLimits` equals `AggregateUsageForLimitsFromLedger` row for row for random `[from, live_from, to)` (aligned, minute-aligned, inside one hour); `UsageReport` equals a Go sum over the raw records for random groupings, periods and zones (`UTC`, `Europe/Rome` across a DST change, `Asia/Kolkata`, `Asia/Kathmandu`, a pending zone change) | USE-06 |
| T-948 | `reports.TestRollupAtomicAndConcurrent` | I | A pass cancelled between statements leaves rollup and watermark as before; two workers on one tenant never count a record twice; a tenant with no record gets a state and no row | USE-06 |
| T-949 | `reports.TestRollupLateRecords` | I | A record started three hours ago and recorded now is in the rollup's old hour after the next pass and in the report; a record recorded less than one minute ago is not yet in the rollup but is in the report (from the ledger) | USE-06 |
| T-950 | `reports.TestRollupDelayExceedsWriterTimeout` | U | The rollup's delay (1 min, the constant in `reports.sql` read by the test) is more than `ledger` `writeTimeout` + `store` commit bound; the statement text of `RollUpUsage` holds `now() - interval '1 minute'` | USE-06 |
| T-951 | `limits.TestLedgerReaderUnchanged` | I | The existing reconciler, rebase and capability tests run against a database whose rollup is current, half current and empty, with identical results; `Ledger.Usage` returns the same groups in the three cases | — (D-3) |
| T-952 | `isolation` fixtures and matrix for `usage_rollup`, `usage_rollup_state`, `audit_chain`, `metadata_key_policy` | D | The negative tests of 1.2 over the four tables (cross-tenant read, write, update, delete refused for every runtime role); the worker in A's session cannot read B's rollup; the gateway has no privilege on the rollup | TEN-07 |
| T-953 | `reports.TestRollupTenantLoop` | I | Tenants are listed in platform scope by ID only; each tenant's statements run in its session (a statement recorder on the transaction); a tenant whose pass fails does not stop the others; M42, M43 and the two log lines | USE-07 |
| T-954 | `admin.TestUsageReport` | I | Each period and grouping; `from`/`to` as dates and instants; default window; clipping of the first and last period; a month in `Europe/Rome` and a day in `Asia/Kathmandu`; a pending change of zone; two currencies and two price sources stay apart; `cost` null when none is known; `rejected_limit` apart; filters; 400 days accepted and 401 refused; the 10 000-row bound; a tenant with no records gets `rows: []` | USE-06 |
| T-955 | `e2e.TestReportLastMonthByModel` | E | **Exit.** Two tenants; ledger rows of last month (inserted as the gateway role) and calls of this month to the fake provider on two models; a `usage-viewer` key of tenant A reads `usage report --month <last>` and gets, per model, the cost per currency equal to the sum of A's rows and nothing of B's; with A's key on B's path the answer equals the one for an unknown tenant (404, same body but the request ID); B's viewer sees only B's; a platform key gets 403; the answer is the same before and after the rollup caught up | **exit**, USE-06, USE-07 |
| T-956 | `admin.TestLimitUsage` | I | A day and a month rule of each metric kind, a key-scoped and a model-scoped rule: `used`/`used_amount` equal `limits.Sum` over the ledger for the period at now in the tenant's zone; a per-minute rule 409; another tenant's rule 404 | — (D-6) |
| T-957 | `admin.TestUsageExport` | I | NDJSON lines equal the items of `listUsageRecords` for the same window (oldest first instead of newest); CSV header and rows, arrays as JSON cells, costs as strings; a metadata value `=1+1` is `'=1+1` in CSV and unchanged in NDJSON; 31 days accepted, more refused; with a 1 s write timeout and a slow page the export still completes (deadline extended); failure after the first byte aborts; `Content-Disposition` | USE-08 |
| T-958 | `apigen.TestStreamedAnswers` | U | A document with an NDJSON-only and an NDJSON-plus-CSV `200` generates `Stream`/`StreamBody` methods and a `format` parameter; a stream response with `application/json` too, a `201` stream, or a stream with a request body is refused; the generated client returns the body unread and a problem as an error | — (D-8) |
| T-959 | `e2e.TestExportsHoldNoForeignData` | E | Tenant A's usage export, report, audit export and `pandokos export` document contain none of tenant B's IDs, names, key IDs, credential IDs, metadata canaries, nor any of `fixtures.Canaries()` or a key secret; the platform document contains no tenant secret and no usage figure | USE-07, OBS-04 |
| T-960 | `isolation.TestUsageRetention` | D | `platform_retention(2, 0)` in platform scope drops exactly the partitions that end on or before the cut, writes one `usage.retention` row naming them (chain intact), is idempotent; refuses 1 and 121, a tenant session and the admin role; `tenant_retention(2, 0)` removes the tenant's default-partition rows and rollup rows before the cut, not another tenant's, and writes `usage.retention` in that tenant's log when it removed a ledger row | USE-08 |
| T-961 | `isolation.TestAppendOnlyUnderRetention` | D | With `pandokos.retention = on` set by each runtime role, `DELETE`, `UPDATE` and `TRUNCATE` on the five append-only tables are still refused (no privilege); the owner without the setting is refused; L-17 holds | AUD-04, USE-05 |
| T-962 | `isolation.TestAuditRetention` | D | Rows sealed with old times (`dbtest.SealedRow`): `tenant_retention(0, 12)` removes the prefix up to the last row before the cut (a later row with an earlier time inside the prefix goes with it), moves the anchor, appends `audit.retention` with the counts and the last hash; the chain verifies from the anchor; a second run removes nothing and writes nothing; the platform log the same through `platform_retention` | AUD-06 |
| T-963 | `reports.TestRetentionJob` | I | With both keys 0 no function is called; with values the tenant pass then the platform pass run at start and then once an hour; a failing tenant does not stop the others; `retention ready`, `retention applied`, `retention failing`; M44 and M45 | USE-08, AUD-06 |
| T-964 | `gateway.TestMetadataKeyPolicy` | I | No policy: as before; a policy `[team]`: `team` kept, `project` dropped, the dropped header counts it; an empty policy keeps nothing; a request refused before step 7 records no metadata and counts its entries as dropped; changing the policy is seen by the next request after the notification (cache dropped by `catalog:<tenant>`) | PRV-03 |
| T-965 | `admin.TestMetadataKeys` | I | Get, set and clear with their audit rows; 65 keys, a duplicate, a key with an upper-case letter or 33 characters refused (400); only `tenant_setting:write` writes and `usage:read` reads; a platform principal 403 | PRV-03 |
| T-966 | `declare.TestExportDocument` | U | From fixed API answers (fake client): the tenant and platform documents equal golden files; order by natural key; no field holding an ID that is not portable; a subject-mapping rule is listed as left out; no secret, key secret, credential or member | CAT-10 |
| T-967 | `declare.TestApplyPlan` | U | Against a fake server: create, update, conflict on an immutable field, a price version equal (nothing), different (conflict), absent in the future (create) and in the past (skipped); resources absent from the document reported, not deleted; `--dry-run` makes no write call; the order of §5.12; exit codes 0, 1, 2 | CAT-10 |
| T-968 | `declare.TestApplyRejectsInvalidDocuments` | U | Unknown member, wrong `pandokos` version, `kind` against the flag, duplicate natural keys, a document over 4 MiB, YAML anchors and aliases (refused), a secret-looking member (`secret`, `password`, `token`) refused | CAT-10 |
| T-969 | `declare.TestApplyOfExportIsNoOp` | U | **Exit (unit).** For the tenant and the platform documents of T-966: export, then apply against the same fake state: zero write calls, output `0 changes`, exit 0 | **exit**, CAT-10 |
| T-970 | `cli.TestReportAuditCommands` | U | Every new command sends the flags it is given and none it is not given; `--month` computes the dates; `usage export` splits 70 days into three requests and writes the file through a temporary name; `audit verify` reads head then export and prints the line of §5.9; composites are outside `commands` | — (§5.14) |
| T-971 | `e2e.TestExportApplyRoundTrip` | E | **Exit.** A tenant configured through the CLI (zone, metadata keys, model restriction, a custom role, two service accounts with roles, rules on the tenant, a service account and a key, a price list with two versions, the active currency) and a platform (provider with endpoint, two models, grants, an override, a ceiling): `pandokos export` then `pandokos apply` of the file: output `0 changes`, no new audit row in either log (heads unchanged), the API state byte-equal; `apply` of the tenant document to a second empty tenant creates it; `export` of the second equals the first's | **exit**, CAT-10 |
| T-972 | `config.TestReportsRetentionKeys` | U | The three keys: types, defaults, bounds (0 accepted; 1 refused for usage; 1201 refused), environment names, `serve` only, the warning below 12 months | USE-08, AUD-06 |
| T-973 | `telemetry.TestReportsInstruments` | U | M42 to M46 exist with their kinds, units and closed attribute values; T-900 extended to them | — (D-22) |
| T-974 | `e2e.TestAuditReadEndToEnd` | E | A support action opened and used in a tenant: the tenant's `audit list --support-action-id` shows exactly its rows; the platform's `platform-audit list` shows the open, close and interventions; `auth.failure` of both listeners are listed; `audit export` then `audit verify --file` exits 0 offline | AUD-05 |
| T-975 | `isolation.TestMetadataKeyPolicyTable` | D | The negative tests of 1.2 on the table; the check refuses 65 keys and a key of the wrong format; a change notifies `catalog:<tenant>` | PRV-03 |
| T-976 | `e2e.TestRetentionEndToEnd` | E | `retention.usage_months: 2` and `retention.audit_months: 1` with partitions and sealed rows made old by the test: after the first run the old partitions are gone, the logs have their `usage.retention` and `audit.retention` rows, `audit verify` passes for both, a report of the dropped months returns no rows, and the log has `retention applied` | USE-08, AUD-06 |

Repository tests:

| ID | Test | Asserts |
|---|---|---|
| T-R71 | `repo.TestPlatformReadsNoTenantReports` | No handler of a platform-scoped operation, and no code in `db.Platform`/`inPlatform`, calls `UsageReport`, `ExportUsageRecords`, `RollUpUsage`, `LockUsageRollup`, `TenantRetention`, `ListAuditEvents`, `ExportAuditEvents`, `GetAuditHead` or the metadata statements; `internal/reports` opens platform scope only for `ListTenantsForWorker` and `PlatformRetention` |
| T-R72 | `repo.TestAuditChainOneDefinition` | Only `internal/auditchain` computes the canonical encoding or the hash in Go; the SQL functions `audit_field`, `audit_canonical`, `audit_row_hash` exist in migration 0015 only; `internal/auditchain` imports no product package |
| T-R73 | `repo.TestReportsRecords` | ADR 0064, 0065, 0066 exist and are indexed; history lines naming them on 0013, 0019, 0028, 0032, 0039, 0043, 0051, 0054, 0061, 0062 |
| T-R74 | `repo.TestLedgerReaderFromLedgerOnlyInTests` | `AggregateUsageForLimitsFromLedger` is called only from `_test.go` files |
| T-R75 | `repo.TestPriceDetailActions` | Every `audit.Record*` call in `internal/admin` whose detail map has a key `rates`, `amount`, `currency` with a price-list resource type, or the active currency, names an action of `admin.PriceDetailActions` |
| T-R76 | `repo.TestReportsDocuments` | The usage guide names the report, the export, the rollup and retention keys; the audit guide names the permissions, the chain, `audit verify`, `--expect-head` and retention; the declarative guide lists every section of §5.12; the configuration reference has the three keys; the draft release notes announce the permissions, the keys, the migration's index note and the commands |

### Changed tests of earlier sprints

| Test | Change |
|---|---|
| T-280 `isolation.TestAuditIsAppendOnly` | The admin role now reads `audit_event` in a tenant session and `platform_audit_event` in platform scope; `UPDATE`, `DELETE` and `TRUNCATE` stay refused for every role |
| `isolation.TestSchemaLint`, `TestSchemaLintDetects` and the tests that run the lint (`TestRecordAuthFailure`, `TestIssuerTables`, `TestUsagePartitions`, `TestMembershipTables`, `TestTenantRoleTable`) | `Spec` gains the four tenant tables (fixtures, grants), `platform_audit_chain` (global, restricted), the four definer functions, the two `SELECT` grants (plan A4) |
| T-747 `isolation.TestPlatformSeesNoTenantUsage` ("the definer functions … with the result types pinned") | Lists `audit_seal_tenant`, `audit_seal_platform` (trigger), `platform_retention` (`integer, bigint`), `tenant_retention` (`bigint, bigint, bigint`); the two retention functions are allowed to name `usage_record` because they return counts only |
| `migrations.TestUpgrade0013`, `TestUpgrade0014`, `TestTenantPriceListsUpgrade` | Migrate to their own version, not to the latest (0015 adds the chain columns to the audit tables); the count of migrations is their own |
| `queries.TestEveryStatement` | Exercises the new statements; the count of statements becomes the new total |
| Tests of `record_unlimited_traffic` (5.3) | Unchanged assertions against the new body |
| T-846, T-847 (golden of roles and attributes) | `audit:read` (tenant, no attribute) on `owner`, `tenant-admin`, `auditor`; `platform_audit:read` on both platform roles |
| T-219 `cli.TestEveryOperationHasACommand` | Unchanged; the composites are in their own table |
| T-270 `isolation.TestCrossTenantIndistinguishable` | `platformAndSelf` classifies the three platform audit operations |
| `repo.TestConfigurationReference`, `TestLogReference`, `TestMetricsReference` | The three keys, the six lines, the five instruments |
| `limits` reconciler and capability tests | Unchanged; run again under T-951 |

### Requirement coverage

| Requirement | Tests |
|---|---|
| USE-06 | T-947, T-948, T-949, T-954, T-955 |
| USE-07 | T-944, T-953, T-955, T-959, T-R71 |
| USE-08 | T-957, T-960, T-963, T-972, T-976 |
| AUD-04 | T-933 to T-939, T-946, T-961 |
| AUD-05 | T-940 to T-944, T-974 |
| AUD-06 | T-945, T-962, T-963, T-976 |
| CAT-10 | T-966 to T-969, T-971 |
| PRV-03 | T-964, T-965, T-975 |
| Exit | T-955, T-938, T-971 (T-969 in process) |

## 10. Definition of done for this sprint

- [ ] Every test of §9 exists under its name, failed before its implementation, and passes; the
      changed tests are changed as listed.
- [ ] USE-06, USE-07, USE-08, AUD-04, AUD-05, AUD-06, CAT-10 and PRV-03 are met and covered (§9).
- [ ] Every new tenant-scoped table has its policy, fixture and grants, and every new route declares a
      permission and has its cross-tenant negative test (T-944, T-952, T-270).
- [ ] No secret, content or other tenant's data appears in a report, an export, a document or a log
      line (T-959, T-930, the canary tests of earlier sprints).
- [ ] The three exit tests pass (T-955, T-938, T-971).
- [ ] Migration 0015 is applied by `pandokos migrate` on a copy of a database of v0.1.x upgraded to
      0014 with data, and `audit verify` passes on both logs afterwards (T-937; part F records it).
- [ ] `make check` passes (lint, vet, generated code, vulnerability check, secret scan, the full suite
      with the race detector).
- [ ] The usage, audit, declarative, database and configuration guides, the data model, the security
      model, the data flows, the log and metrics references, the permissions reference (generated),
      ADR 0064 to 0066 with their history lines, and the draft release notes of v0.2.0 are current.

## 11. Consequences for later sprints

- **6.3 (tenant deletion).** The deletion job removes a tenant's rows of the append-only tables
  (`usage_record`, `audit_event`, `limit_event`) through definer functions that use the same switch
  (`pandokos.retention`, D-17), and removes its `usage_rollup`, `usage_rollup_state`, `audit_chain`
  and `metadata_key_policy` rows (plain deletes with grants it adds). **Pseudonymizing** a kept audit
  row changes its hash: either the deleted tenant's chain is closed with a final `tenant.delete`
  event and its rows are pseudonymized and **re-sealed** by one audited function that records the old
  and new heads (the verifier then reports "resealed at seq N"), or the actor fields are kept as they
  are (they are identifiers, not personal data). 6.3 decides; the platform's chain is never re-sealed.
  The tenant data export of 6.3 can reuse `exportUsageRecords`, `exportAuditEvents` and the tenant
  document of `pandokos export`. The completion record goes to the platform chain.
- **6.4 (events).** The outbox relay may publish audit rows as events with their `seq` and `hash`, so a
  receiver can verify and anchor the chain as it happens (the "forwarded" of AUD-06). Refused grants and
  refused support attempts can become events (D-23). The relay's instruments join the catalogue (6.1
  §11). A usage event does not need the rollup.
- **Epic 8.** Several workers can run the rollup and retention at once: the state row and the
  advisory lock serialize them. The exact reader works across instances unchanged (it reads the
  database). Sprint 8.2: the worker process runs `reports.Rollup` and `reports.Retention`; the admin
  process serves reports and exports; streamed answers need the ingress not to buffer them (Helm
  values).
- **Sprint 10.1.** Load tests measure the seal trigger under concurrent administration (the head lock),
  the rollup at the target traffic, report latency on a year of data, and the volume of `rejected_limit`
  rows under a retry storm (D-24).
- **Sprint 12.3.** `apply` gains platform-level issuers, tenant token issuers and subject mappings, for
  "an instance defined by configuration alone"; the restore drill verifies both chains after restore
  and compares the head with one anchored before the backup.
- **Release v0.2.0.** The notes announce the two permissions, the three keys, the twelve operations and
  the commands, the sealing of existing audit rows by migration 0015, and the index it builds over the
  ledger (with the procedure for large ledgers).
- **A move of PostgreSQL's major version** keeps the chain verifiable as long as `jsonb` writes numbers
  as before; the restore drill of 12.3 verifies it on the target version.

## 12. Risks and open points

### What was verified while designing

| Fact | How |
|---|---|
| Migration 0015 (plan A1) applies after 0001–0014 on PostgreSQL 16.15, on an empty database and on one with audit rows in two tenants and the platform; the backfilled chains link; a new row through the admin role, the owner outside a session, `record_unlimited_traffic` twice (true, then false and the head unchanged), a rolled-back insert (head unchanged) and a cross-tenant insert (refused, other head unchanged) behave as §5.6 says | a scratch database on the test server: `psql` with the files, as `pandokos_owner` |
| `platform_retention(2, 0)` drops exactly the partitions before the cut and writes its row; `tenant_retention(0, 12)` on rows made old removes the prefix, moves the anchor, appends `audit.retention`; every row still matches `audit_row_hash`; the admin role cannot execute the function | the same database |
| The rollup statements (A2) run as the worker; after three passes the rollup equals the ledger below the watermark (requests and cost); `AggregateUsageForLimits` (new) equals the old statement row for row on seven windows (aligned, inside an hour, a 05:45 boundary, `live_from` after `to`); `UsageReport` with day boundaries of `Asia/Kolkata` equals a direct grouping of the records | the same database, 3000 synthetic records |
| `sqlc` v1.31.1 compiles the migration and the statements of A2 and A2b; the module builds and vets with them; the limits, ledger, audit and store tests pass with the new reader; the failing tests are exactly those listed under "Changed tests" | a scratch copy of the repository, `go run …/sqlc@v1.31.1 generate`, `go test` on the test server |
| The schema lint reports only the missing `Spec` entries of plan A4 for the new objects (no finding of L-08, L-17 or L-20) | the same copy, `TestSchemaLint` |
| Adding a column to `tenant` would change the generated `PandokosTenant` (and make it non-comparable): hence the separate table of §5.11 | the same copy |
| apigen accepts only `application/json` successes today | `internal/apigen/main.go`, `successBody` |

Not verified: apigen's streamed answers (designed in plan A3, tested by T-958), the Go code of every
part, the timings of the rollup on a large ledger (10.1), and the plan of each statement on a large
ledger (the property and integration tests check results, not plans; part F runs `EXPLAIN` on a
generated ledger of 10⁶ rows and records it).

### Risks

| # | Risk | Mitigation |
|---|---|---|
| R-1 | The head lock serializes the audit writes of one tenant, and every platform-log write of the installation; a transaction that writes both logs in the other order can deadlock | Writes are short and rare; tenant-then-platform is the rule (RecordIntervention); T-934; PostgreSQL resolves a deadlock by failing one transaction, which the API answers 503 and the client retries |
| R-2 | Migration 0015 builds an index over the whole ledger and rewrites every audit row, blocking ledger writes meanwhile | The release notes and the database guide give the size rule and the procedure: build `usage_record_*_tenant_id_recorded_at_idx` per partition `CONCURRENTLY` beforehand (the migration then attaches them); the audit tables are small |
| R-3 | The rollup misses a record whose insert transaction outlives the one-minute delay | The writer's transaction is bounded by 5 s (T-950); manual inserts by an operator are not supported (the database guide says so) |
| R-4 | The exact reader is slower than expected on a large ledger (a plan that scans the window) | Part F records `EXPLAIN` on 10⁶ rows; the statement uses the primary key range and the new index; 10.1 measures |
| R-5 | `DROP TABLE` of a partition waits for the ledger writer's lock and delays inserts | 5 s lock timeout; once a month at most; a failure is retried the next hour |
| R-6 | The canonical encoding depends on the text `jsonb` writes for numbers | That text is stable across PostgreSQL versions (numeric output); 12.3 verifies after restore on the target version; the encoding is versioned (`pandokos-audit-v1`) |
| R-7 | A partial `apply` leaves part of a document applied | Every step is one API call with its audit row; re-running converges; the output says where it stopped |
| R-8 | Auditors read limit values, role definitions and other configuration in the log | Accepted: `auditor` already reads limits and roles; prices and the active currency are withheld (D-12) |
| R-9 | Someone with write access to the tables rewrites a whole chain and its head | Documented (ADR 0013); `--expect-head` against a head kept elsewhere; 6.4 forwarding |
| R-10 | A large CSV or NDJSON export holds an admin connection for minutes | Pages in separate transactions; 15-minute bound; 31-day windows |

### Open points (raised to the maintainer; none blocks the sprint)

| # | Question | Recommendation |
|---|---|---|
| O-1 | `audit:read` without the attribute `Support`: an operator under a support action cannot read the tenant's log. Confirm, or give it `Support`? | Confirm: the log records price rates and budget amounts, which ADR 0061 point 3 keeps out of support actions; the platform reads its interventions in its own log |
| O-2 | The details of price actions are withheld (with their digest) from readers without `price_list:read`, so `auditor` does not see rates. Confirm, or give `auditor` `price_list:read`, or show everything? | Confirm the withholding: it keeps AZ-10 and the 4.3 description of `auditor`, and the chain still verifies |
| O-3 | Retention is one installation-wide setting per kind, in the static configuration. Per-tenant retention? | Installation-wide now (partitions hold every tenant); per tenant only on request, as a later design with row deletion |
| O-4 | Audit retention removes a prefix of each chain and moves its anchor, instead of dropping partitions as ADR 0013 says. Confirm the amendment? | Confirm (§5.10: unique keys, small volume, the anchor is needed anyway) |
| O-5 | Both retention keys default to 0 (keep everything). Or a default, such as 13 months of usage? | 0: nothing is ever removed without the operator deciding it |
| O-6 | The chain uses Pandokos's own canonical encoding (§5.6) rather than RFC 8785 (JSON canonicalization). Confirm? | Confirm: JCS is not available in PostgreSQL and would need number formatting rules the database does not follow; ours is short, tested on both sides and versioned |
| O-7 | `apply` never deletes; no `--prune`. Confirm? | Confirm (ADR 0019: "by default it leaves them alone and reports them"); a prune can be designed when asked, with a dry-run requirement |
| O-8 | Platform rows about a tenant (model grants) are not shown in the tenant's log (2.2 O-7). Keep? | Keep: the tenant sees the effect in `listTenantModels`; a cross-log read would need a definer function over the platform log in a tenant session |
| O-9 | A request refused before model resolution records no metadata (§5.11). Confirm? | Confirm: those requests did no work, and storing keys the tenant may refuse is what PRV-03 forbids |
| O-10 | CSV cells that start with `= + - @`, a tab or a carriage return get a leading `'`. Confirm? | Confirm (OWASP); NDJSON is the exact format |
| O-11 | Exports are limited to 31 days per request (the CLI splits longer windows). Confirm, or allow a year? | Confirm: bounded requests, the same window as the list |
| O-12 | Refused grants and refused support attempts stay log lines and a counter, not audit rows (4.3 O-12); events in 6.4 if wanted. Confirm? | Confirm |
| O-13 | Migration 0015 builds an index over the ledger in its transaction (blocking writes on a large ledger), with a documented pre-build procedure. Confirm, or split the index into a separate migration step run concurrently by an operator command? | Confirm with the documented procedure: migrations stay one transaction each (ADR 0026) |

### Maintainer decisions on the open points (2026-11-22)

| Open point | Decision |
|---|---|
| O-1 | Confirmed: `audit:read` is not available under a support action (no `Support` attribute); the log holds price rates and budget amounts |
| O-2 | Confirmed: price details are withheld from readers without `price_list:read`, with their digest so the chain still verifies |
| O-3 | Confirmed: retention is installation-wide, not per tenant |
| O-4 | Confirmed: ADR 0013 is amended; audit retention removes the oldest part of each chain and moves its anchor |
| O-5 | Confirmed: both retention keys default to 0; nothing is removed unless the operator sets them |
| O-6 | Confirmed: Pandokos's own canonical encoding, not RFC 8785 |
| O-7 | Confirmed: no `--prune` for `apply` |
| O-8 | Confirmed: model grants stay out of the tenant's log |
| O-9 | Confirmed: requests refused before model resolution record no metadata |
| O-10 | Confirmed: CSV cells are guarded against formulas |
| O-11 | Confirmed: 31-day export window; the CLI splits longer windows |
| O-12 | Confirmed: refused grants stay unaudited (log lines); 6.4 decides about events |
| O-13 | Confirmed: the migration builds the index in its transaction; the documented procedure lets operators build it beforehand without blocking, and the v0.2.0 release notes point to it |
