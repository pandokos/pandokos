# Limits guide

Pandokos can refuse a request before it reaches a provider when the caller, the key or the tenant
would pass a limit: requests per minute, tokens per minute, or requests in flight. This page says
what is counted, how a request is estimated before it is sent, what the limits guarantee and what
they only bound, what the caller sees when one is reached, and how to choose values. It applies from
sprint 5.1 ([ADR 0048](../adr/0048-reserve-and-settle.md), [ADR 0049](../adr/0049-limit-rules.md)).

> **Limits are per gateway instance until the Valkey store exists.** Each instance counts alone, in
> memory: with *N* instances a limit admits up to *N* times its value, and a restart empties the
> counters. **Rules are written through the administration API of sprint 5.2; until then no rule
> exists in an installation, and with no rule nothing is limited.** Sprint 5.1 builds the
> enforcement and the storage of the rules; it adds no route, command or permission to write them.

## 1. What is counted

| Metric | Counts | Window |
|---|---|---|
| `requests_per_minute` | requests that Pandokos tried to send | sliding, 60 to 61 s |
| `tokens_per_minute` | input plus output tokens, reserved at the start and corrected at the end | sliding, 60 to 61 s |
| `concurrent_requests` | requests in flight: admitted and not yet finished | none (a gauge) |

- The window of the two rates is never shorter than a minute and is up to one second longer: an
  amount is added to the second in which the request was admitted and is counted until 61 seconds
  after the start of that second. Because of that, a limit holds in **every** interval of 60 seconds,
  not only in the minutes of the clock.
- A request counts once Pandokos tries to send it. A request refused before that (an invalid body,
  a model the key may not use, a limit, no usable credential) is not counted and consumes no tokens. A request
  the provider answered with an error, or that could not reach the provider, stays counted as a
  request and counts no tokens.
- `messages.count_tokens` is a request and a slot, and no tokens.
- A limit value is an integer from 1 to 1,000,000,000,000.

## 2. Levels and subjects

A request is checked against **one limit per subject and metric that has a rule**. Every limit must
admit it: a rule can take room away and never add it.

| Subject (`subject_kind`) | What it names | Counter | Value in force |
|---|---|---|---|
| `tenant` | the calling tenant as a whole | the tenant | the smaller of the platform ceiling and the tenant's own rule, whichever exist. The platform ceiling is the platform row of that tenant if there is one, else the platform default |
| `service_account` | one service account of the tenant | the service account's ID | the tenant's rule for that service account |
| `key` | one virtual key, or, for a caller that presents a token, its subject mapping | the key ID, or the mapping ID for a token | the tenant's rule for that key |

Limits of a service account apply to its keys and to its tokens alike. A rule whose subject does
not exist, or a key rule above the tenant's limit, is valid and has no effect. The platform can
raise a tenant's ceiling with a row for that tenant that replaces the default; a tenant cannot
loosen what the platform set. A platform ceiling counts each tenant on its own: a limit on the sum
over all tenants cannot be expressed.

Rules live in two tables ([database guide](database.md)). Counters exist only for subjects that
have a rule and traffic in the last 61 seconds.

**Example 1: a platform default with one tenant raised.** The platform sets a default of
`requests_per_minute` 600 and, for tenant `acme`, a row of 1200. Tenant `globex` has no row of its
own: its ceiling is 600. `acme` has no rule of its own either: its ceiling is 1200. If `acme` adds
a tenant rule of 300, its limit in force is 300, the smaller value; if it adds 5000, the limit
stays 1200.

**Example 2: a tenant limit with a tighter key.** A tenant has `tokens_per_minute` 200000 for the
tenant and 30000 for the key `7f3a...` of its batch job. All other keys can use the whole 200000
between them; the batch key can never use more than 30000 in a minute, and what it uses also
counts against the tenant's 200000. A request of the batch key is refused when either counter has
no room, and the answer says which one (`X-Pandokos-Limit-Subject`).

## 3. Reserve and settle: the estimate

Pandokos has no tokenizer. Before the credential is opened it reserves an estimate against every
limit of the request in one atomic step: every limit is charged, or none is. When the request ends
the reservation is settled with the figures of the usage record, or released if the request was never
sent ([section 5](#5-how-a-request-ends)).

| Part | Rule |
|---|---|
| Input tokens | the bytes of the request body divided by four, rounded up (the same rule as an estimated usage record), at most the model's `context_window` when the catalogue has one. 0 for `messages.count_tokens` |
| Output tokens per choice | the caller's own output limit when it set one, else `limits.default_output_tokens` (default 4096); at most the model's `max_output_tokens` when the catalogue has one |
| Choices | `n` of a chat completion (at most 128); 1 for every other operation |
| Output tokens | per choice times choices; 0 for `embeddings` and `messages.count_tokens` |
| Reserved tokens | input plus output, at most 1,000,000,000,000 |
| Cost | the reserved tokens at the price in force when the request started; unknown when the model has no price. No limit of this release reads it |

The caller's output limit is `max_completion_tokens`, else `max_tokens`, for `chat.completions`;
`max_output_tokens` for `responses`; `max_tokens` for `messages`. A member counts only when it is a
top-level member written as a plain positive integer of at most 12 digits (no sign, fraction or
exponent); the last occurrence decides. Anything else counts as "not set", so a request whose limit
Pandokos cannot read is reserved at the default.

**What to tell callers:** set an output limit. A request with no limit reserves the default, which
is a guess; a request with a limit reserves exactly that (up to the model's maximum) and is
admitted more predictably. Callers that set a limit larger than needed hold more of the token limit
than they use until they settle.

Two consequences follow. A body that is large in bytes and small in tokens (an inline image)
reserves more than it uses until the request settles, and the context window caps that. A token
limit below what the largest request reserves refuses that request for good (see section 7).

## 4. What is strict and what is bounded

| Limit | Guarantee |
|---|---|
| `requests_per_minute` | **Strict.** In every interval of 60 seconds at most *L* requests were admitted and not released |
| `concurrent_requests` | **Strict.** At every instant at most *L* requests are in flight |
| `tokens_per_minute` | **Strict on reserved amounts, bounded on actual ones** |

For tokens, take any interval of 60 seconds and let *s* be the last admission in it. Then the
tokens that the requests admitted in the interval used are at most *L* plus, for each of those
requests that was still in flight at *s*, what it used above its reservation. The reason: at *s*
every request admitted in the interval is counted, the finished ones at what they used and the others
at their reservation, and the admission at *s* required that sum to be at most *L*; afterwards it
changes only when the requests still in flight settle. Three corollaries:

- If no request uses more than it reserved, the limit is strict.
- The overshoot is at most *k* times *e*, where *k* is the number of requests in flight (at most the
  concurrency limit, when one is set) and *e* the most one request can exceed its reservation.
- Once a counter is over *L* nothing is admitted until the window has moved on, so an overshoot is
  not repeated inside the window.

*e* is: nothing in output when the caller set a limit that the provider enforces; otherwise, per
choice, the model's `max_output_tokens` minus the reserved default. In input, text whose tokens are
shorter than four bytes and inputs referenced rather than inlined (an image by URL) can exceed the
estimate, by at most the model's `context_window` minus the reserved input. When the catalogue has
neither value for a model, Pandokos cannot state *e* for it and the provider's own maximum applies.

**Example.** A tenant has `tokens_per_minute` 100000 and `concurrent_requests` 4. Its callers set no
output limit; the default is 4096 and the model's maximum output is 16384, so *e* is 16384 minus 4096,
which is 12288 (taking the input as exact). In the worst minute, four requests in flight each
produce the maximum: the tokens used in that interval are at most 100000 + 4 x 12288 = 149152. With
no concurrency limit, *k* is not bounded by a rule and the same arithmetic has no ceiling: that is
why section 7 asks for one.

Counters are per instance (see the box at the top), and a changed rule applies to the counts
already held: counters are not reset.

## 5. How a request ends

| End of the request | Limiter call | Requests | Tokens | Slot |
|---|---|---|---|---|
| Refused before the limits (policy, invalid body, model) | none | not counted | none | none |
| Refused by a limit (`rate_limited`) | reserve fails | not counted | none | none |
| Refused or failed when the credential is opened; the caller went away before the request was sent | release | returned | returned | freed |
| 2xx, usage from the provider | settle | counted | actual | freed |
| 2xx, no usable usage (an estimated record) | settle | counted | the record's estimate | freed |
| Provider error (4xx, 5xx, its own 429) | settle | counted | 0 | freed |
| Provider unreachable, egress refusal, redirect, upstream timeout | settle | counted | 0 | freed |
| Caller disconnects mid-stream, provider aborts, `limits.max_request_duration`, forced shutdown | settle | counted | what the record holds: provider figures seen so far, else the estimate | freed |
| A panic after the reservation | settle if the request was sent, else release | as above | as above | freed |
| `messages.count_tokens` | settle | counted | 0 | freed |
| Settle or release itself fails | recovered, logged at error | | | freed by expiry |

Settlement uses the figures of the usage record, whatever their source, so the ledger and the
counters agree. Settle corrects the second the request was admitted in while that second is still
in the window; if it has left, nothing is corrected. Exactly one of settle and release happens per
reservation, when the request finishes.

A reservation also has an expiry, `limits.max_request_duration` plus one minute, which no request
outlives. If one expires the instance releases it and logs `limit reservation expired` at level
error with the request ID and the tenant: that is a defect (a settlement that failed), not a slow
request.

## 6. The 429

A request refused by a limit is answered 429 in the error shape of its API family:

```
$ curl -i https://pandokos.example.com/openai/v1/chat/completions \
    -H "Authorization: Bearer $PANDOKOS_KEY" -H "Content-Type: application/json" \
    -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}],"max_tokens":50}'
HTTP/1.1 429 Too Many Requests
Content-Type: application/json
Retry-After: 17
X-Pandokos-Error: rate_limited
X-Pandokos-Request-Id: <request id>
X-Pandokos-Limit-Subject: key
X-Pandokos-Limit-Metric: requests_per_minute
X-Pandokos-Limit: 60
X-Pandokos-Limit-Remaining: 0
X-Pandokos-Limit-Reset: 17

{"error":{"message":"Pandokos: a rate limit of Pandokos was reached; retry after the time given in Retry-After.","type":"pandokos_error","param":null,"code":"rate_limited"}}
```

| Part | Value |
|---|---|
| Body | `openai`: `type` `pandokos_error`, `code` `rate_limited`. `anthropic`: `error.type` `rate_limit_error`. The message is the constant above |
| `X-Pandokos-Error` | `rate_limited` |
| `Retry-After` | whole seconds, 1 to 61: when a retry would fit. If several limits had no room, the longest |
| `X-Pandokos-Limit-Subject` | `tenant`, `service_account` or `key`: never an ID |
| `X-Pandokos-Limit-Metric` | `requests_per_minute`, `tokens_per_minute` or `concurrent_requests` |
| `X-Pandokos-Limit` | the limit in force for that subject and metric |
| `X-Pandokos-Limit-Remaining` | what is left of it now, 0 or more (for tokens it can be positive and smaller than the request) |
| `X-Pandokos-Limit-Reset` | whole seconds until that counter is empty; absent for `concurrent_requests` and when it is already empty |

The headers are set on refusals only, never on a successful answer. One code serves the three
metrics; the metric header tells them apart. When the estimate of one request is by itself larger
than the limit, waiting cannot help: the log line says `estimate_over_limit` and `Retry-After` is 60.

**Telling it from a provider's 429.** A response with `X-Pandokos-Error` comes from Pandokos. A
provider's own 429 passes unchanged, with the provider's `x-ratelimit-*` and `retry-after` headers
and without `X-Pandokos-Error` and without the `X-Pandokos-Limit*` headers, and it counts as a
request that the provider answered with an error.

**What SDKs do.** The OpenAI and Anthropic SDKs treat the answer as their rate-limit error: they
retry a small number of times by default, waiting for `Retry-After` when they find it short enough
and backing off by themselves otherwise, and then raise the error. Sprint 5.1 tests this against the
OpenAI Go SDK only; the Python and TypeScript SDKs are tested in sprint 5.2.

When the rules of a tenant cannot be read and none are cached, the request is answered 503
`unavailable` with `Retry-After: 5`: a request whose limits cannot be verified is not forwarded.

## 7. Choosing values

- **A token limit must not be below the largest request** that a caller makes: that request
  reserves its input plus its output and is refused every time, because no waiting makes room. Look
  at the largest `reserved_tokens` in the log and at the output limits your callers set.
- **Set a concurrency limit next to a token limit.** It bounds *k* in the inequality of section 4,
  so it bounds the overshoot.
- **Ask callers to set an output limit**, so a request reserves what it is likely to use.
- A request limit and a concurrency limit are strict; size them by what the provider and your
  budget can take. Remember that limits multiply by the number of instances until the shared store
  exists: divide by the number of instances you run.
- A limit that is changed applies to the counts already held. Lowering it below the current count
  refuses new requests until the window has moved on.

## 8. Logs and troubleshooting

The `request` line ([gateway guide](gateway.md), section 10) gains these fields:

| Field | Present | Meaning |
|---|---|---|
| `reserved_tokens` | a reservation was made | the tokens reserved for the request: input plus output estimate |
| `limit_subject`, `limit_metric` | refused by a limit | the subject kind and the metric that had no room |
| `limit_level` | refused by a limit | `platform` or `tenant`: who set the value in force |
| `limit` | refused by a limit | the value in force |
| `retry_after_s` | refused by a limit | the `Retry-After` that was sent |
| `estimate_over_limit` | the estimate alone exceeds the limit | `true`: waiting will not help |

Other lines: `limit reservation expired` (error: `request_id`, `tenant_id`), `limit settle failed`
(error: `request_id`, and the panic type or the error), and `limit rules unavailable` (warning:
`tenant_id`). No line holds a body, a header value, a credential or a subject ID other than the
principal fields of the request line.

| You see | Look at |
|---|---|
| 429 with `limit_metric` `requests_per_minute` | the caller's rate against `limit`; `limit_subject` says whether it is the key, the service account or the tenant |
| 429 with `limit_metric` `tokens_per_minute` and `estimate_over_limit: true` | the largest `reserved_tokens`; raise the limit or ask the caller for a lower output limit or a smaller input |
| 429 with `limit_metric` `tokens_per_minute` and a small `Remaining` | the output limit the callers set: without one every request reserves `limits.default_output_tokens` |
| 429 with `limit_metric` `concurrent_requests` | slow or hanging requests; `limits.max_request_duration` ends them |
| 503 `unavailable` and `limit rules unavailable` | the database: the rules of that tenant could not be read and none were cached |
| `limit reservation expired` | a defect: report it with the request ID |
| A rule that seems ignored | the subject ID (a key ID, a service account ID, or for a token the mapping ID); a rule for a subject that does not exist never matches. A rule lasts at most `cache.limits_ttl` in the gateway cache before it applies |
| `rejected_limit` in the usage list | the same refusals, as records without usage and without cost ([usage guide](usage.md)) |

## 9. What is not there yet

| Missing | Sprint |
|---|---|
| Writing rules: administration API, CLI, permissions and audit records for limit rules | 5.2 |
| Budgets and quotas (a cost or token total per period), and the code `quota_exceeded` | 5.2 |
| The `budget` field of the capability check | 5.2 |
| A default output reservation per model | 5.2 |
| Python and TypeScript SDK tests of a Pandokos 429 | 5.2 |
| Counters shared by several instances, kept in Valkey and rebuilt from the ledger | epic 8 |
| Limit metrics for dashboards | 6.1 |
