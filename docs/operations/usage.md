# Usage guide

Every authenticated request to the inference listener leaves one row in the usage ledger, the table
`usage_record`: who called, what was called, how it ended, how many tokens it used and what that
cost. A row holds no content: no prompt, no completion, no header value and no secret. This page
says when a row is written, what each column means, how tokens and cost are decided, how the writer
behaves and what to look for in the log. It applies from sprint 3.2; decisions:
[ADR 0010](../adr/0010-usage-extraction.md), [ADR 0013](../adr/0013-ledger-and-audit.md) and
[ADR 0039](../adr/0039-usage-record-admission-and-ledger-writer.md).

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

No API, command or page reads usage yet; see section 9 for inspecting rows with SQL.

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
| `principal_kind`, `principal_id` | who called: `service_account` and its ID |
| `key_id` | the ID of the virtual key |
| `credential_id` | the provider credential used; empty until a credential was chosen |
| `funding` | `byok` (the tenant's own credential) |
| `metadata` | the caller's metadata entries, section 5 |
| `metadata_dropped` | how many metadata entries were dropped |

**What was called**

| Column | Meaning |
|---|---|
| `provider`, `api_family` | the catalogue provider and its API family |
| `operation` | `chat.completions`, `responses` or `embeddings` |
| `endpoint` | the endpoint name; empty until it was chosen |
| `model` | the catalogue name of the model; empty until the model resolved |
| `model_id` | the catalogue ID of the model, when it resolved |
| `upstream_model` | the identifier the provider received |
| `stream` | whether a stream was asked for; empty until the body was read |
| `outcome` | how the request ended, section 3 |
| `error_code` | the Pandokos error code, when Pandokos raised one |
| `upstream_status` | the provider's status; empty when it did not answer |
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
| `other_units` | other billable units the family reports (a flat map of numbers) |
| `provider_usage` | the integer members of the provider's `usage` object keyed by their path (`prompt_tokens`, `prompt_tokens_details.cached_tokens`): at most 32 entries, nothing but non-negative integers, so the provider cannot put text into the ledger |

**Estimated by Pandokos** (empty unless the source is `estimated`)

| Column | Meaning |
|---|---|
| `estimated_input_tokens`, `estimated_output_tokens` | Pandokos's own estimate, section 4 |

Provider values and estimates never share a column; check constraints enforce it.

**Cost**

| Column | Meaning |
|---|---|
| `cost_status` | `known`, `unknown` or `none`, section 4 |
| `cost_reason` | when the status is `unknown`: `no_price`, `category_unpriced` or `inconsistent_usage` |
| `cost` | the amount, exact (`numeric(38,18)`), in the currency of the price |
| `currency` | the three-letter code of the price version |
| `price_version_id` | the price version in force when the request started |
| `cost_fallbacks` | the usage categories that were priced at a fallback rate |

No column except `tenant_id` is a foreign key: a record outlives the key, the credential, the model
and the price it names.

## 3. Outcomes

`outcome` takes the first value that applies:

| Outcome | When |
|---|---|
| `rejected_policy` | Pandokos refused the request: `tenant_suspended`, `forbidden`, `model_not_allowed`, `endpoint_not_supported`, `no_credential`, `invalid_request`, `request_too_large` |
| `timeout` | `request_timeout`, `upstream_timeout`, or `limits.max_request_duration` passed after the provider's headers |
| `upstream_error` | `upstream_unreachable`, the provider cut its answer, or the provider answered 4xx or 5xx (`upstream_status` says which) |
| `internal_error` | `unavailable` while resolving the model or credential, or `internal` |
| `cancelled` | the caller went away, or the server was forced down at shutdown |
| `ok` | everything else |

`rejected_limit` exists in the table for the limits of a later release and is not written yet. The
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

A status that is not 2xx is recorded without usage: a refused request generated nothing Pandokos
observed, and a provider does not bill it. Failed provider calls therefore show no consumption.

**The estimates are coarse by design** (no tokenizer; ADR 0010). For the `openai` family: input is
the request body size in bytes divided by four, rounded up; output is one token per content event
of a stream, or the response body size divided by four, rounded up, for a plain answer (0 for
`embeddings`). They are stored in their own columns and marked `estimated`. Use them to see that
something was used and roughly how much, never to bill.

**Cost** is computed when the request ends, from the price version in force when it started and the
provider's usage (or the estimate, when that is all there is):

| `cost_status` | When |
|---|---|
| `known` | the price covers the usage |
| `unknown` | usage exists and could not be priced; `cost_reason` is `no_price` (no price version for the model), `category_unpriced` or `inconsistent_usage` |
| `none` | both sources are `none` |

Amounts are exact, in the currency of the price version, and never converted. **Under BYOK the cost
is informational**: the tenant pays the provider with its own credential, and the figure is what the
catalogue's prices say the call would cost.

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
`metadata_dropped`. Metadata is never forwarded to a provider, never logged, never put on a span,
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
| `usage record rejected` | error | the database refused one record (fields: SQLSTATE, constraint name, `record_id`); the record is dropped and the others are not held up. It means the checks in Go and the table disagree: report it |
| `usage record not written` | error | a record still in memory when the writer's shutdown deadline ended: its identity fields (`record_id`, `request_id`, `tenant_id`, `outcome`, ...) and `row`, the whole row as JSON with the metadata values removed. **Rebuild the rows from these lines** with an `INSERT` as the owner; the record ID makes a repeated insert harmless. A last line, `ledger closed with records lost`, gives the count |
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
`ledger.shutdown_flush_timeout`. Records it cannot write are logged in full as
`usage record not written`, and one last line, `ledger closed with records lost`, gives their count. Give the container at least
`shutdown.drain_timeout + 5 s + ledger.shutdown_flush_timeout` plus a few seconds to stop; see the
[container guide](container.md).

## 9. Inspecting rows

Nothing reads the ledger through an API in this release. Row-level security is forced, so even the
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

To follow one request, search the log for its `request_id` or `record_id`, and the ledger by
`(tenant_id, request_id)`, which is indexed.
