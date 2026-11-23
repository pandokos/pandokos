# Audit log guide

Every administrative or security event is one row of an append-only log, in the same transaction as the
change ([ADR 0032](../adr/0032-audit-log-first-version.md)). Since migration 0015 every row is sealed
in a **hash chain** and both logs can be read, filtered, exported and verified
([ADR 0065](../adr/0065-the-audit-hash-chain-reading-and-retention.md)). This guide says who reads
which log, how the chain works, how to check it and what that proves.

## 1. Who reads which log

There are two logs: each tenant's, and the platform's.

| Log | Permission | Roles that hold it |
|---|---|---|
| a tenant's | `audit:read` (tenant scope) | `owner`, `tenant-admin`, `auditor`; not `read-only`; no custom role unless it is given the permission |
| the platform's | `platform_audit:read` (platform scope) | `platform-admin`, `platform-auditor` |

- **`audit:read` is not available under a support action.** The log records price rates and budget
  amounts, which ADR 0061 keeps out of support actions. A platform administrator who acted inside a
  tenant reads what it did in the platform's own log: interventions (`support_action.open` and the rest)
  are written to both logs.
- The tenant's log does not show platform rows about the tenant, such as model grants: the tenant sees
  their effect in its model list.
- Reading is not audited.

## 2. Reading and filtering

| Operation | Path | CLI |
|---|---|---|
| `listAuditEvents` | `GET /v1/tenants/{tenant}/audit-events` | `pandokos audit list --tenant T` |
| `getAuditHead` | `GET /v1/tenants/{tenant}/audit-head` | `pandokos audit head --tenant T` |
| `exportAuditEvents` | `GET /v1/tenants/{tenant}/audit-events/export` | `pandokos audit export --tenant T` |
| `listPlatformAuditEvents` | `GET /v1/platform-audit-events` | `pandokos platform-audit list` |
| `getPlatformAuditHead` | `GET /v1/platform-audit-head` | `pandokos platform-audit head` |
| `exportPlatformAuditEvents` | `GET /v1/platform-audit-events/export` | `pandokos platform-audit export` |

A list returns the newest rows first, by sequence number. Filters: `from` and `to` (RFC 3339, on
`occurred_at`, `to` excluded), `action` (exact, or a prefix that ends in a dot such as `member.`),
`resource_type`, `resource_id`, `actor_type`, `actor_id`, `outcome` (`success` or `failure`),
`request_id` and, for a tenant, `support_action_id` (the rows written under one support action). The
cursor of a page carries its filters: a following page must repeat exactly the filters of the first,
otherwise `400 invalid_request`. An event has every column of the row, `seq`, `prev_hash` and `hash` in
lower-case hexadecimal, `detail` as a JSON object and `detail_withheld` (section 5). Rows of failed
authentication (`auth.failure`) are returned as written, with `detail.listener` `inference` or `admin`.

## 3. The chain

Each tenant has one chain and the platform has one. A trigger of the database seals every row as it is
inserted, whoever inserts it (the administration and worker roles, the three functions of the gateway,
a migration), and takes the head of the chain under a lock until the transaction ends: rows of one
chain get consecutive numbers in commit order, and a row that is not inserted leaves the head as it was.

- **What is hashed.** Version 1 of the encoding (`pandokos-audit-v1`): the previous hash, the scope
  (`tenant:<tenant ID>` or `platform`), the sequence number, the row's ID, its time (UTC, microseconds),
  the actor type, ID and name, the key ID, the action, the resource type and ID, the outcome, the request
  ID, the reason, the support action ID, and the SHA-256 digest of the detail in a canonical form: every
  field is length-prefixed UTF-8, objects are sorted by key, numbers keep the text the database stores.
  The same definition is written in SQL (`audit_row_hash`) and in Go (`internal/auditchain`) and the two
  are tested against each other; nothing else computes it.
- **Hash of the detail, not its text.** The chain commits to the digest of the detail, which is what lets
  a reader verify a row whose detail it may not read (section 5).
- **The head and the anchor.** The head is the last sequence number and hash of the chain
  (`audit head` returns `seq` and `hash`). The anchor is the first row still kept (`anchor_seq`, 1 before
  any removal) and the hash of the row before it (`anchor_hash`, the hash of the last row retention
  removed, section 6; 32 zero bytes before any removal). The first row's previous hash is the anchor's.
- **Rows that existed before migration 0015** were sealed once by the migration, chain by chain, in the
  order of their time and ID. A chain therefore starts at the upgrade as far as proof goes: it shows
  nothing about whether those rows were intact before.

## 4. `audit verify`

```sh
pandokos audit verify --tenant T [--expect-head HEX] [--from-seq N] [--file FILE]
pandokos platform-audit verify [--expect-head HEX] [--from-seq N] [--file FILE]
```

Without `--file` the command reads the head, then the export up to that head, through the API; with
`--file` it verifies an NDJSON export offline, with no server (`--tenant` then names the tenant's ID,
because the scope is part of every hash). The checks, row by row:

- the first row is the one asked for (`--from-seq` when it is given, the anchor otherwise) and the
  sequence numbers are contiguous from it, so a deleted oldest row is a break (`seq_gap`);
- each row's `prev_hash` equals the previous row's `hash`, and the first row's equals the anchor's when
  the export starts at the anchor (a `--from-seq` above the anchor, or an offline file without a head,
  says the link to the rows before it is not verified);
- each `hash` equals the recomputation (with `detail_digest` in place of the detail for a withheld row);
- the last row equals the head, and `--expect-head` when it is given.

The export stops at the head that was read first. **The verifier also reads the rows past that head**
from the list (rows written since the head was read, or put there by hand) and verifies them as the
continuation of the chain, so a row appended after the head without sealing is reported.

Output: `verified <n> rows, seq <a>..<b>, head <hash>` and exit 0; or one line per break,
`break at seq <n>: <reason>`, and exit 1:

| Reason | Meaning |
|---|---|
| `hash_mismatch` | the row's hash is not that of its content: the row was changed |
| `prev_mismatch` | the row does not point at the hash of the one before |
| `seq_gap` | rows are missing, or a number repeats |
| `anchor_mismatch` | the first row does not continue from the anchor |
| `head_mismatch` | the log ends before the head, or goes past it with rows that do not continue it |
| `expected_head_mismatch` | the last row is not the head given with `--expect-head` |

A malformed or truncated input is exit 1 with `invalid export: <what>`; a usage error is exit 2.

## 5. Withheld details

The detail of a price action (`tenant_price.create`, `tenant_price.delete`,
`tenant.active_currency_update`: the actions whose detail holds a rate, an amount of a price list or the
active currency) is returned to a principal **without** `price_list:read` as `detail: {}`,
`detail_withheld: true` and `detail_digest`, the hex of the digest that the chain uses. An `owner` or
`tenant-admin` sees the detail; an `auditor` sees that a price changed, when and by whom, not the rate.
The verifier uses the digest, so a chain verifies from the export of a reader that cannot read the
detail. The export has the same rule.

## 6. Retention of the logs

`retention.audit_months` (default `0`: keep everything; 1 to 1200, a value below 12 is a start-up
warning) keeps the audit rows written since the start of the current UTC month minus that many months.
The worker runs the job at start and then every hour. For each tenant it removes the rows of the chain
**from its start** up to the last one before the cut, moves the anchor to the last removed row and writes
`audit.retention` in the same chain; the platform's log is treated the same way and writes
`audit.retention` too. The audit tables are not partitioned (a partitioned table could not keep the
unique keys of the unlimited-traffic rows, and the chain needs an anchor in any case), so retention
deletes rows through a function of the database that alone sets the switch `append_only` looks for
([database guide](database.md#20-grants-of-migration-0015)). Removal is for good: export and keep the
log first when it must be kept. The setting is installation-wide, not per tenant.

## 7. Anchoring the head elsewhere

The chain is held in the database it protects. Whoever can write the tables can recompute every hash and
the head. What they cannot do is change the head you wrote down earlier, so keep heads **off the
installation**:

```sh
# daily, from a host that is not the installation, appending to a store that only appends
pandokos audit head --tenant T -o json >> /archive/audit-heads-T.jsonl
pandokos platform-audit head -o json >> /archive/audit-heads-platform.jsonl

# at the audit: the chain from the start must pass through the head you kept
pandokos audit verify --tenant T --expect-head <hash of a kept head>
```

`--expect-head` takes the hash of the *last* row the verification reads; to check an older kept head
verify an export that ends at its `seq` (`audit export --to-seq N`, then `verify --file`). A head kept
before a retention run stays valid for the rows after its `seq`: continue from the anchor.

## 8. What the chain proves, and what not

- **Proves**: a row that was changed, removed, inserted or moved by someone who did not recompute the
  rest of the chain is detected (`hash_mismatch`, `prev_mismatch`, `seq_gap`), as is a log cut short of
  the head.
- **Does not prove**: that rows were intact before the upgrade of migration 0015; that a row is true (it
  records what the product wrote); that nothing was removed from the end by someone who also moved the
  head, unless a head kept elsewhere is given; and it does not stop anyone, it makes alteration visible.
- **The residual risk**: a person who can write the tables of the database (the owner role, a superuser)
  can rewrite a whole chain and its head. Only an anchor kept elsewhere detects it
  ([security model](../architecture/security-model.md)).
- Audit writes of one tenant, and every write of the platform's log, are serialized by the lock of the
  head; this is the cost of consecutive numbers.
