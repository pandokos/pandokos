# Usage guide

Every authenticated request to the inference listener leaves one row in the usage ledger, the table
`usage_record`: who called, what was called, how it ended, how many tokens it used and what that
cost. A row holds no content: no prompt, no completion, no header value and no secret. This page
says when a row is written, what each column means, how tokens and cost are decided, how the writer
behaves and what to look for in the log. It applies from sprint 3.2; decisions:
[ADR 0010](../adr/0010-usage-extraction.md), [ADR 0013](../adr/0013-ledger-and-audit.md) and
[ADR 0039](../adr/0039-usage-record-admission-and-ledger-writer.md); sprint 3.3 adds the `anthropic` family
([ADR 0041](../adr/0041-anthropic-api-family.md)) and the list of records ([ADR 0043](../adr/0043-tenant-capability-check-and-usage-list.md)).

## 1. When a record is written

A request is **admitted** right after it authenticates, by taking a slot of the ledger buffer.
Every admitted request produces exactly one record, whatever its outcome: a refusal by policy, a
provider error, a cancellation by the caller, a timeout, a forced shutdown, an internal failure.
Two cases have no record:

- The request did not authenticate (no key, a key that cannot be verified). Nothing about an
  anonymous caller is stored, parsed or traced; the failure is logged and audited as before
  ([gateway guide](gateway.md), section 8).
- The buffer is full. The request is refused with `503 unavailable` and the log field
  `ledger: full`, before it costs anything. This is the one authenticated request without a record.

A tenant administrator reads its records with `pandokos usage list` (section 9); inspecting rows with SQL is the operator's alternative.

## 2. The columns

All text columns are format-checked by the database, so none can hold a body. Columns are listed by
group; the table is `pandokos.usage_record`.

**Attribution**

| Column | Meaning |
|---|---|
| `tenant_id` | the tenant of the key; part of the primary key and the row-level security policy |
| `id` | the record ID, a UUID version 7 made by the gateway (so that a batch written twice stores each row once) |
| `started_at` | when the request started, UTC; the partition key |
| `recorded_at` | when the row was committed, by the database |
| `request_id` | the value of `X-Pandokos-Request-Id`, 32 hexadecimal characters |
| `trace_id` | the trace ID of the request, 32 hexadecimal characters, empty when it has none |
| `principal_kind`, `principal_id` | who called: `service_account` (a virtual key) or `token` (a token of a trusted issuer), and the ID of the service account in both cases, so a report that groups by `principal_id` sees one service account whichever mechanism it used |
| `key_id` | the ID of the virtual key; for `token`, the ID of the subject mapping that let the subject act as the service account ([tokens guide](tokens.md)) |
| `credential_id` | the provider credential used; empty until a credential was chosen |
| `metadata` | the caller's metadata entries, section 5 |
| `metadata_dropped` | how many metadata entries were dropped |

**What was called**

| Column | Meaning |
|---|---|
| `provider`, `api_family` | the catalogue provider and its API family |
| `operation` | `chat.completions`, `responses`, `embeddings`, `messages` or `messages.count_tokens` |
| `endpoint` | the endpoint name; empty until it was chosen |
| `model` | the catalogue name of the model; empty until the model resolved |
| `model_id` | the catalogue ID of the model, when it resolved |
| `upstream_model` | the identifier the provider received |
| `stream` | whether a stream was asked for; empty until the body was read |
| `outcome` | how the request ended, section 3 |
| `error_code` | the Pandokos error code, when Pandokos raised one |
| `upstream_status` | the provider's status (100 to 599); empty when it did not answer or answered a status outside that range (the answer is still forwarded) |
| `ttfb_ms` | milliseconds from the start of the request to the first body byte from the provider; empty when no body byte arrived |
| `duration_ms` | milliseconds from the start to the end of the request |
| `request_bytes`, `response_bytes` | body bytes received from the caller and sent to the caller |

The caller's own `model` string is stored only once it has resolved to a catalogue model.

**Measured by the provider** (empty unless the source is `provider`)

| Column | Meaning |
|---|---|
| `input_source`, `output_source` | where each total comes from, section 4 |
| `input_tokens`, `output_tokens` | the provider's totals; input includes cached tokens and output includes reasoning tokens |
| `cache_read_tokens`, `cache_write_tokens`, `reasoning_tokens` | the provider's detail counts, when reported |
| `other_units` | other units the provider bills, as the family reports them (a flat map of numbers) |
| `provider_usage` | the integer members of the provider's `usage` object keyed by their path (`prompt_tokens`, `prompt_tokens_details.cached_tokens`): at most 32 entries, nothing but non-negative integers, so the provider cannot put text into the ledger |

**How limits treated the request**

| Column | Meaning |
|---|---|
| `limit_state` | `limited`: at least one rule matched and every matching limit was evaluated, whether the request was reserved against them or refused by one (429, or 409 `no_price`). `unlimited`: no rule of the platform, the tenant, the service account or the key matched the request for its provider and model. `unverified`: a matching limit could not be evaluated: the rules could not be read (503), or counters could not be loaded or reserved, so the request was refused (503) or, with `limits.on_store_failure: allow`, admitted without them. `not_evaluated`: the request ended before the limits were consulted (refused for its body, its model or a policy), or the gateway runs without a limiter. Empty (NULL): the row was written before sprint 5.3 ([limits guide](limits.md), section 15) |

**Estimated by Pandokos** (empty unless the source is `estimated`)

| Column | Meaning |
|---|---|
| `estimated_input_tokens`, `estimated_output_tokens` | Pandokos's own estimate, section 4 |

Provider values and estimates never share a column; check constraints enforce it.

**Cost**

| Column | Meaning |
|---|---|
| `cost_status` | `known`, `unknown` or `none`, section 4 |
| `cost_reason` | when the status is `unknown`: `no_price`, `category_unpriced` or `inconsistent_usage` (also when the amount would not fit the column) |
| `cost` | the amount, exact (`numeric(38,18)`), in the currency of the price |
| `currency` | the three-letter code of the price version: the tenant's active currency when the request started (section 4) |
| `price_version_id` | the price version in force when the request started |
| `price_source` | which list `price_version_id` belongs to: `tenant` (a version of the tenant's own price list, table `tenant_price_version`) or `catalogue` (`price_version`); empty (NULL) when no price was used and in records written before sprint 5.4 |
| `cost_fallbacks` | the usage categories that were priced at a fallback rate |

No column except `tenant_id` is a foreign key: a record outlives the key, the credential, the model
and the price it names.

## 3. Outcomes

`outcome` takes the first value that applies:

| Outcome | When |
|---|---|
| `rejected_limit` | Pandokos refused the request because a limit was reached or a budget could not be applied: `error_code` `rate_limited`, `quota_exceeded` or `no_price`. Such a row carries no usage and no cost (cost status `none`), no credential, and counts as no consumption ([limits guide](limits.md)) |
| `rejected_policy` | Pandokos refused the request: `tenant_suspended`, `forbidden`, `model_not_allowed`, `endpoint_not_supported`, `no_credential`, `invalid_request`, `request_too_large` |
| `timeout` | `request_timeout`, `upstream_timeout`, or `limits.max_request_duration` passed after the provider's headers |
| `upstream_error` | `upstream_unreachable`, the provider cut its answer, or the provider answered 4xx or 5xx (`upstream_status` says which) |
| `internal_error` | `unavailable` while resolving the model, the limit rules or the credential, or `internal` |
| `cancelled` | the caller went away, or the server was forced down at shutdown |
| `ok` | everything else |

A `rejected_limit` row is written for every request refused in step 7b, since sprint 5.1; it holds the
model fields (the model check had passed) and no usage and no cost. Its `error_code` is `rate_limited`
(requests, tokens, concurrency), `quota_exceeded` (a quota or budget of a day or month) or `no_price`
(sprint 5.2).

**Which records the limits count.** A quota or budget counts the records that carry a credential ID, that
is the requests whose credential was opened, with the input plus output tokens of the record
(provider figures, else the estimate) and its cost when known. A `rejected_limit` or `rejected_policy`
row has no credential ID and counts nothing; a request that the provider answered with an error counts
as one request without tokens. The limiter and the ledger use the same definition, so a total over
`started_at` in a period equals the counter ([limits guide](limits.md), section 11). The
request log line keeps its own, finer vocabulary (`client_cancelled`, `shutdown_aborted`, ...); the
ledger's `cancelled` covers both.

## 4. Sources, estimates and cost

Each of the two totals has a source:

| Source | Meaning |
|---|---|
| `provider` | the provider's own figure, read from its answer |
| `estimated` | Pandokos's estimate |
| `none` | no figure |

What was recorded depends on what happened:

| What happened | Input | Output |
|---|---|---|
| Not forwarded (refused before the provider was called, or cancelled before it) | `none` | `none` |
| Forwarded and no answer (connection error, refusal by the egress guard, timeout before the headers), a redirect, or a status that is not 2xx | `none` | `none` |
| 2xx and usage was read | `provider` | `provider` |
| 2xx and no usage was read: the provider sent none, the stream was cut, cancelled or timed out, or the reader gave up | `estimated` | `estimated` |

The two sides can have different sources. When an `anthropic` stream ends after `message_start` (cut, cancelled or failed) the record keeps the provider's input figures (`input_source` is `provider`) and an estimated output (`output_source` is `estimated`); the cost is then computed from the provider's input side and the estimated output. The log line has `usage_output_source` only when the two sources differ, and the provider span has `pandokos.usage.output_source` always. An operation without usage, `messages.count_tokens`, is recorded with both sources `none` and `cost_status` `none`: counting tokens consumes nothing.

A status that is not 2xx is recorded without usage: a refused request generated nothing Pandokos
observed, and a provider does not bill it. Failed provider calls therefore show no consumption.

**Anthropic normalization.** Totals are inclusive: `input_tokens` is the provider's `input_tokens` plus `cache_creation_input_tokens` plus `cache_read_input_tokens`; the cache read and write figures are kept apart; `output_tokens` includes thinking, as the provider counts it; the 1-hour cache write (`cache_creation.ephemeral_1h_input_tokens`) is priced as `cache_write_1h`; `server_tool_use.web_search_requests` is kept in `other_units` and is not priced. In a stream `message_start` reports the input side and each `message_delta` replaces the members it carries (cumulative counts). A negative or non-integer count makes the usage unusable and the request is estimated.

**The estimates are coarse by design** (no tokenizer; ADR 0010). For the `openai` and `anthropic` families: input is
the request body size in bytes divided by four, rounded up; output is one token per content event
of a stream, or the response body size divided by four, rounded up, for a plain answer (0 for
`embeddings`). They are stored in their own columns and marked `estimated`. Use them to see that
something was used and roughly how much, never to bill.

**Cost** is computed when the request ends, from the price version in force when it started and the
provider's usage (or the estimate, when that is all there is):

| `cost_status` | When |
|---|---|
| `known` | the price covers the usage |
| `unknown` | usage exists and could not be priced; `cost_reason` is `no_price` (no price for the model in the tenant's active currency), `category_unpriced` or `inconsistent_usage` |
| `none` | both sources are `none` |

Amounts are exact, in the currency of the price version, and never converted. The price is chosen by the tenant's active currency: the model's version in force in the tenant's own list in that currency, else the catalogue's version in force when it is in that currency, else none ([catalogue guide](catalogue.md), section 7). **Under BYOK the cost
is informational**: the tenant pays the provider with its own credential, and the figure is what the
catalogue's prices say the call would cost.

**Reproducing a cost.** A record names the price it used: `price_version_id` and `price_source`. For `catalogue`, `pandokos price list MODEL` shows the version and its rates (a platform administrator); for `tenant`, `pandokos price-list get --tenant T ID` shows it (a tenant administrator), replaced versions included. The cost is the sum, over the usage categories of the record, of units times rate divided by a million, with the tiers and fallbacks of the [catalogue guide](catalogue.md), section 4. A request is priced with one version and one currency from its estimate to its settlement.

## 5. Caller metadata

A caller attaches labels to a request with headers named `X-Pandokos-Metadata-<key>`, read after
authentication and stored in `metadata`:

| Rule | Value |
|---|---|
| Key | the rest of the header name, lower-cased; `[a-z0-9][a-z0-9_-]{0,31}`, up to 32 characters |
| Value | one value per key; 1 to 128 bytes of printable ASCII, surrounding spaces trimmed |
| Count | at most 8 entries, taken in ascending key order |

An entry that breaks a rule (a bad key, an empty or bad value, two values for one key, a ninth key)
is dropped and counted; the request proceeds. The count is stored in `metadata_dropped` and, when
not zero, returned in the response header `X-Pandokos-Metadata-Dropped` and logged as
`metadata_dropped`; the count stops at 1000. Metadata is never forwarded to a provider, never logged, never put on a span,
and no decision reads it.

**Metadata is stored exactly as sent.** It is the caller's to keep free of personal data and of
content. Use opaque identifiers (an order number, a team code), not names, addresses or text.

## 6. The writer

One goroutine batches records and writes them to the database with the gateway role, which holds
`INSERT` on the table and nothing else.

- **Batching.** A record waits at most `ledger.flush_interval` (default 50 ms) for company, or until
  `ledger.batch_size` (500) records wait. Records are grouped by tenant and each group is inserted
  in one transaction of that tenant. The insert ignores conflicts: a batch that is sent twice (a
  commit whose answer was lost) stores each row once.
- **The buffer.** `ledger.buffer_size` (default 10 000) counts slots. A slot is taken at admission
  and freed when the row is committed, so the records of admitted requests always fit and memory
  is bounded. **It is also the number of requests an instance admits at once**: size it above the
  concurrency you expect (streams last as long as the generation) and above the writes that can
  pile up while the database is briefly away. At the default each instance admits 10 000
  simultaneous requests.
- **The four keys.** `ledger.flush_interval`, `ledger.batch_size`, `ledger.buffer_size` and
  `ledger.shutdown_flush_timeout` (default 10 s); see the [configuration reference](configuration.md).
- **The loss window.** Records are in memory until written. A process that is killed (SIGKILL, power
  loss, a second signal at shutdown) loses the records in memory: at most `ledger.buffer_size`,
  normally those of the last `ledger.flush_interval`. A clean stop writes them first (section 8).
- **Database away.** Writes that fail because the database is unavailable stay queued in order and
  are retried with a growing wait (100 ms up to 5 s). Requests continue while slots are free; when
  they are all taken, step 3a refuses new requests and readiness fails.

**Readiness.** The health check `ledger` fails with `ledger: buffer full` from the moment a request
is refused for lack of a slot until the slots in use fall to half of the buffer. While it fails,
`/readyz` answers 503: the instance has stopped serving calls it cannot record, and a load balancer
should send traffic elsewhere.

**Log lines of the writer**

| Message | Level | Meaning and what to do |
|---|---|---|
| `ledger write failing` | warn | the database refused or could not be reached; logged when the state begins and once a minute; records stay queued. Look at the `database-*` readiness checks and the database |
| `ledger write recovered` | info | writing works again |
| `usage record rejected` | error | one record was refused and dropped; the others are not held up. Either the table refused it (fields: `request_id`, SQLSTATE and constraint name), or the check in Go (`Validate`) refused it before it was sent (fields: `error` naming the column, and `request_id` unless that is the column that failed). The line holds nothing else of the record ([ADR 0055](../adr/0055-operational-telemetry-holds-no-tenant-data.md)). The gateway saturates what the caller or the provider can make too large, so this should not happen: it means a bug, or the checks in Go and the table disagree. Report it with the line. States of a busy database (serialization failure, deadlock, lock not available, read-only transaction) are not rejections: the records stay queued and the writer retries |
| `usage partitions not ensured` | warn | creating the monthly partitions failed (at start, which carries on, or in the hourly job); fields `error_class` and `sqlstate`. Rows still go to the default partition; look at the worker role and the database |
| `usage record not written` | error | a record that arrived after the writer had closed; the line holds nothing of it. It is lost, like the records counted by `ledger closed with records lost` |
| `ledger closed with records lost` | error | the writer's shutdown deadline ended with records still in memory: `unwritten` gives their number, `missing` the requests admitted that never recorded. Those records are lost: Pandokos does not rebuild them ([ADR 0055](../adr/0055-operational-telemetry-holds-no-tenant-data.md)). Give the container the stop grace period of section 8 |
| `usage record without admission` | error | a handler recorded without having been admitted; once per start; report it |

## 7. Partitions

`usage_record` is partitioned by calendar month (UTC) on `started_at`. Partitions are named
`usage_record_yYYYYmMM` (for example `usage_record_y2026m11`), plus `usage_record_default`.

- `serve` creates the partitions of the month before the current one and the three months after it
  when it starts, and again every hour, with the worker role through the function
  `ensure_usage_partitions`; the log line is `usage partitions created` with the count, when not
  zero. A failure at start is a warning, not fatal.
- A row whose month has no partition lands in the **default partition** instead of being refused. It
  is expected to stay empty. When it is not, `serve` warns once per start with `usage records in
  the default partition`: the hourly job is not running or fell behind. Rows in the default
  partition stay there; a later release decides their retention.
- No partition is ever dropped by Pandokos in this release. Retention is a later sprint.
- Partitions carry forced row-level security and no grants of their own: reach rows through
  `pandokos.usage_record`.

## 8. Shutdown

After the drain (`shutdown.drain_timeout`), requests still running are cancelled and `serve` waits
up to 5 s for them to record; the writer then stops admitting, writes every queued record
and the late records of the requests just cancelled, under its own deadline
`ledger.shutdown_flush_timeout`. Records it cannot write by then are lost, and one last line,
`ledger closed with records lost`, gives their count. Give the container at least
`shutdown.drain_timeout + 5 s + ledger.shutdown_flush_timeout` plus a few seconds to stop; see the
[container guide](container.md).

## 9. Inspecting rows

A tenant administrator lists the tenant's records, newest first, through the administration API
(`listUsageRecords`, permission `usage:read`, held by `tenant-admin`) or the CLI:

```sh
pandokos usage list --tenant demo [--from 2026-11-19T00:00:00Z] [--to 2026-11-20T00:00:00Z] [--provider anthropic] \
    [--limit-state limited|unlimited|unverified|not_evaluated]
```

`to` defaults to now and `from` to 24 hours before `to`; the window is at most 31 days and `from`
must be before `to` (otherwise `invalid_request` on `from`). The records are those with
`from` <= `started_at` < `to`. Pages hold 1 to 200 records (default 50); the cursor carries the
resolved window and the filter (so a list without `to` pages over the window of its first page),
and a cursor used with an explicit `from`, `to` or `provider` that differs from them is
`invalid_request` on `cursor`. `limit_state` filters on one of the four values (the cursor carries it too); a
record written before sprint 5.3 has none and shows an empty string. The
list is read as the admin role under the tenant's row-level security policy and is not audited. In
the answer `other_units` and `provider_usage` are arrays of `{name, value}` sorted by name, `metadata` is an
array of `{key, value}` (the caller's own labels), a token figure is 0 when its source is not
`provider`, an estimate is 0 when its source is not `estimated`, `cost` is `null` unless
`cost_status` is `known`, and a text column that is absent is the empty string. A record never holds
content, so the list returns none.

The operator can also read the table with SQL. Row-level security is forced, so even the
owner sees no row without a tenant. As the database owner, set the tenant in a transaction:

```sql
BEGIN;
SELECT set_config('app.tenant_id', '<tenant uuid>', true);
SELECT started_at, operation, model, outcome, input_source, input_tokens, estimated_input_tokens,
       output_tokens, estimated_output_tokens, cost_status, cost, currency
  FROM pandokos.usage_record
 ORDER BY started_at DESC
 LIMIT 20;
COMMIT;
```

A superuser sees every row and bypasses the policy; that access belongs to the infrastructure
operator, outside the boundary the product enforces. The admin and worker roles hold `SELECT` under
the same policy; the gateway role cannot read a row it wrote.

To follow one request, search the log for its `request_id` (the tenant has it: it is the response
header `X-Pandokos-Request-Id` and a column of its usage records), and the ledger by
`(tenant_id, request_id)`, which is indexed.

## 10. Who reads usage

The usage records, the limit events and the price lists of a tenant are **the tenant's**
([ADR 0054](../adr/0054-usage-data-is-the-tenants.md)):

- Through the product only the tenant reads them: `usage:read` (`pandokos usage list`), `limit:read`
  (`limit events`) and `price_list:read`, all tenant-scoped permissions of `tenant-admin`. No platform
  role reads usage, limit events or prices of a tenant, no platform operation returns them and no
  platform role is a usage viewer ([permissions](../reference/permissions.md)). In the database the
  platform scope and a session without a scope see no row of these tables.
- What the platform sees are operational indicators of the installation (requests, errors, latency,
  saturation) with no tenant and no cost, and the ceilings it set, not what was counted against them.
- **What the operator's logs and traces carry.** Nothing of the tenant
  ([ADR 0055](../adr/0055-operational-telemetry-holds-no-tenant-data.md)): no tenant, caller, key,
  model, credential, size, usage source, token count, cost, currency, metadata count or limit rule.
  The log is not a copy of the ledger: no line of the ledger writer holds a row, and a record that
  cannot be written is counted, not logged. The request line and the spans hold what the operator
  needs to run the installation (route, status, outcome, error code, provider, endpoint, durations,
  trace ID); the operator reaches one request by the `request_id` the tenant gives. No setting brings
  tenant data back into logs or traces.
- **The limit.** Row-level security separates tenants from each other and from the platform roles of
  the product. It does not protect a tenant's data from whoever administers the database, the hosts or
  the backups: the owner role, a superuser or a backup reads every table (issue #33, [security
  model](../architecture/security-model.md)).
