---
id: SPRINT-5.1
title: Reserve and settle
epic: EPIC-5
release: v0.2
status: in-progress
design: draft 2026-11-17
---

# Sprint 5.1 — Reserve and settle

- Epic: [5 — Limits and budgets](../epic.md)
- Implementation plan: [plan.md](plan.md)
- Previous sprint: [4.1 — Trusted token issuers](../../04-identity-and-rbac/4.1-trusted-token-issuers/sprint.md)
- Risk class: **high** (the semantics of limits under concurrency; a new refusal on the hot path
  of every inference request; state that must be returned on every way a request can end; the
  first sprint after the tag `v0.1.0`)
- Decisions implemented: ADR [0012](../../../../docs/adr/0012-limits-and-budget-reservation.md)
  (reserve and settle, sliding windows, concurrency, the in-process store),
  [0020](../../../../docs/adr/0020-error-contract.md) (the code `rate_limited` is put into use),
  [0037](../../../../docs/adr/0037-request-pipeline-and-resolution.md) (a step between the model
  check and the credential), [0039](../../../../docs/adr/0039-usage-record-admission-and-ledger-writer.md)
  (outcome `rejected_limit`; settlement equals the record),
  [0019](../../../../docs/adr/0019-configuration.md) (two keys; rules are state, not
  configuration), [0026](../../../../docs/adr/0026-schema-migrations.md) and
  [0027](../../../../docs/adr/0027-database-roles-and-session-context.md) (migration 0009, two
  tables), [0029](../../../../docs/adr/0029-static-sql-sqlc.md) (two queries),
  [0045](../../../../docs/adr/0045-trusted-token-issuers.md) (a token principal is limited like a
  key principal)
- New decision records written by this sprint: **0048** (reserve and settle in the pipeline: the
  estimate, the counters, the documented bound and the 429 contract; refines 0012), **0049**
  (limit rules: levels, subjects, storage and resolution; refines 0012); history lines on 0012,
  0020, 0037, 0039. Text in [plan.md](plan.md), part E.

This document is the design. The implementer follows [plan.md](plan.md); a gap in either document
is raised as a question, not filled silently ([planning/README.md](../../../README.md)).

Items carried into this sprint, and where each is decided:

| Item | From | Decided in |
|---|---|---|
| *Reserve* around steps 7 and 8, *settle* in the handler's finish | 3.1 §11, 3.2 §11, epic 3 record | §5.1, §5.7, D-1, D-2 |
| The 429 codes | 3.1 §11, ADR 0020 | §5.8, D-11 (`rate_limited` now; `quota_exceeded` with 5.2) |
| Per-tenant concurrency, the rest of GW-16 | 3.1 §2, epic 3 record | §5.4, D-1 |
| Outcome `rejected_limit` | 3.2 §2, §11 | §5.9, D-12 |
| The request-side estimate, from `Adapter.EstimateUsage` | 3.2 §11 | §5.3, D-3 |
| The request-side estimate for Anthropic from `max_tokens`; `count_tokens` reserves no tokens | 3.3 §11 | §5.3, D-3, D-17 |
| The output estimate of a stream is a count of content events (3.2 O-5): revisit | 3.2 §12 | D-15: confirmed again, with the reason |
| An incremental scanner for the `usage` member of JSON answers over 4 MiB (3.2 O-14): decide | 3.2 §12 | D-16: not built, not scheduled |
| The `budget` field of the capability check becomes `available` or `exhausted` | 3.3 §11, epic 3 record | D-14: moved to 5.2, where budgets exist |
| Limits keyed by service account apply to keys and tokens; a limit per key has the mapping ID as its key for tokens | 4.1 §11 | §5.5, D-8 |
| Limits on a virtual key, the rest of IAM-04 | 1.3 §3, 2.2 §3 | §5.5 (enforced now; written through the API of 5.2, D-13) |
| Counters are rebuilt from `usage_record` by the worker role | 3.2 §11 | 5.2 (period counters); rate windows are not rebuilt, as ADR 0012 says |

## 1. Outcome

A tenant, a service account or a key that has a limit cannot exceed it, however many requests
arrive at once:

1. Before a request is forwarded the gateway estimates what it will consume and **reserves** that
   against every limit that applies to the caller, in one atomic step: one request and one
   concurrency slot, and the estimated tokens. If one limit has no room, nothing is reserved and
   the caller gets a 429 in the shape of the API it called, with `Retry-After`, the code
   `rate_limited` and the limit that refused.
2. When the request ends, however it ends, the reservation is **settled** with what the usage
   record holds, or **released** in full when nothing was sent to the provider. Nothing stays
   reserved: not after a failure, a cancellation, a panic, a client that disconnects in the middle
   of a stream or a forced shutdown.
3. Three limits exist: requests per minute, tokens per minute, concurrent requests. Each can be
   set by the platform for a tenant, and by a tenant for itself, a service account or a key; a
   lower level can only restrict.
4. What is strict and what is bounded is stated, proven by property tests and shown by a parallel
   load test: admitted requests never exceed a request-rate or a concurrency limit; tokens exceed
   a token limit by at most the amount by which the requests in flight exceed their own
   reservations (§5.6).

Counters live in the memory of the instance. Limits for several instances, quotas, budgets, the
administration API and the CLI for rules are later sprints (§2).

## 2. Scope

### In scope

- `internal/limits`: the store interface (reserve, settle, release, expiry of abandoned
  reservations), the in-process store, rule resolution, the loader and the per-tenant cache of
  rules.
- The request-side estimate of tokens and cost: `family.Request.OutputBound`, the estimator of the
  gateway, two catalogue values on the target.
- Migration 0009: `limit_rule` (tenant-scoped) and `platform_limit_rule` (global, restricted), the
  notification `limits`; two queries; the isolation harness.
- The pipeline: step 7b *reserve*, settle or release in `finish`, the code `rate_limited`, the
  response headers, the outcome `rejected_limit`, log fields.
- Two configuration keys.
- Property tests, race tests, a parallel load test, end-to-end tests with the built binary, one
  SDK test.
- `docs/operations/limits.md`, the changed pages, ADR 0048 and 0049.

### Not in scope

| Item | Where |
|---|---|
| Administration API, CLI, permissions and audit records for limit rules (AUD-01 for limits) | 5.2 (D-13, O-1) |
| Quotas per day and month, time zone; monetary budgets; unknown prices; `quota_exceeded` | 5.2 |
| Rules per provider or per model (the optional part of LIM-04) | 5.2 |
| Soft thresholds and their event; delivery of events | 5.2; 6.4 |
| Counters rebuilt from the ledger, reconciliation by the worker | 5.2 |
| Behaviour when the counter store fails (LIM-11) | 5.2 (the in-process store cannot be unreachable) |
| `checks.budget` of the capability check | 5.2 (D-14) |
| A default output reservation per model | 5.2 (O-5) |
| Limits consistent across instances; the Valkey store (LIM-10) | epic 8 |
| Metrics of limit rejections (OBS-06) | 6.1 |
| Allowance headers on admitted responses | not scheduled (O-7) |
| A tokenizer; the incremental `usage` scanner | not planned (D-3); not scheduled (D-16) |
| Python and TypeScript SDK tests of the 429, and an Anthropic SDK test of it | 5.2: those suites drive the built binary, and rules can be written through the CLI then. Here the OpenAI Go SDK is tested (T-646) and the Anthropic shape by T-631 |

## 3. Requirements claimed

The meaning is quoted from [docs/requirements](../../../../docs/requirements/README.md).

| ID | Meaning | Delivered here | Extent |
|---|---|---|---|
| LIM-01 | "Rate limits in requests per minute and tokens per minute" | Metrics `requests_per_minute` and `tokens_per_minute` on sliding windows (§5.4) | full, single instance |
| LIM-04 | "Limits apply at platform, tenant and virtual-key level, optionally per provider or model. A lower level can only restrict" | Platform ceilings, tenant rules, service-account and key rules; every applicable counter must admit (§5.5) | levels: full; per provider or model: 5.2; writing rules: 5.2 |
| LIM-06 | "Limits are checked before forwarding and settled with the actual usage afterwards" | Step 7b and `finish` (§5.1, §5.7) | full for the three metrics |
| LIM-07 | "Concurrent requests cannot overshoot a budget without bound: capacity is reserved from an estimate before forwarding and settled afterwards. The maximum overshoot is documented" | Reservation of estimated tokens; the bound of §5.6, in `limits.md`, proven by T-617 and T-640 | tokens; money with 5.2, on the same mechanism |
| LIM-08 | "The treatment of failed, cancelled, refused and usage-less requests is defined and documented" | The table of §5.7 | full for the three metrics |
| LIM-09 | "A limit rejection returns HTTP 429 with a stable code, `Retry-After` and remaining-allowance headers, and is distinguishable from a provider's own 429" | §5.8 | full (`quota_exceeded` with 5.2) |
| GW-16 | "Request size, header size, duration and per-tenant concurrency are limited" | Metric `concurrent_requests` | the concurrency part |
| GW-04 | "When the caller disconnects the upstream request is cancelled. Timeouts are configurable. Nothing stays reserved or locked afterwards" | Settle or release on every end (§5.7, T-635) | the reservation part 3.2 left open |
| IAM-04 | "A virtual key always belongs to exactly one tenant and can be restricted by scope, model allow-list, expiry and limits" | Rules of subject `key` are enforced | enforcement; writing: 5.2 |
| TEN-04, TEN-06, TEN-07 | Row-level security on tenant data; isolation "uniformly for … limit counters …"; negative tests for every tenant-scoped table | `limit_rule` has the policy and a fixture; `platform_limit_rule` its own policies; counters are keyed by tenant (T-621, T-626, T-644) | full for what this sprint adds |

Not claimed: AUD-01 for limits (no operation changes a rule in this sprint), LIM-02, LIM-03,
LIM-05, LIM-10 to LIM-13, API-07's budget part, OBS-06.

## 4. Repository layout

New:

```
internal/limits/limits.go          Metric, Subject, Limit, Amounts, Request, Reservation, Refusal, Store
internal/limits/window.go          the sliding window of one counter
internal/limits/memory.go          the in-process store: shards, reservations, sweep
internal/limits/rules.go           Rule, RuleSet, RuleSet.For (resolution)
internal/limits/load.go            LoadRules: one tenant transaction, two queries
internal/limits/cache.go           RuleCache: per tenant, lifetime, notifications
internal/gateway/estimate.go       the request-side estimate
internal/gateway/limits.go         step 7b, the 429, settle and release
internal/resolver/limits.go        gateway.Limits over RuleCache and Store
internal/store/migrations/sql/0009_limit_rules.sql
internal/store/queries/sql/limits.sql
docs/operations/limits.md
docs/adr/0048-…, 0049-…
```

Changed: `internal/family/{family,adapter}.go` (one code, five header names, `OutputBound`),
`internal/family/openai/request.go`, `internal/family/anthropic/{request,errors}.go`,
`internal/gateway/{handler,ports,record}.go`, `internal/resolver/catalogue.go` (two target values),
`internal/authn/cache.go`, `internal/vault/opener.go`, `internal/catalog/cache.go` (two ignored
payloads), `internal/config/*`, `internal/server/server.go`, `cmd/pandokos/serve.go`,
`test/isolation/spec.go`, `test/dbtest` and `test/testutil` (helpers), `.golangci.yml`, `Makefile`
(`bench`), generated code, `migrations.sum`, documentation.

No new Go module. No change to `api/openapi/admin.v1.yaml`, to `internal/authz` or to the CLI.

## 5. Architecture

### 5.1 The pipeline, as changed

Steps 1 to 7 and 8 to 10 are those of 3.1 §5.1 with the changes of 3.2, 3.3 and 4.1. One step is
added and `finish` gains one action:

| # | Step | Refusal |
|---|---|---|
| 7 | **Model** (unchanged). The target now also carries the model's `max_output_tokens` and `context_window` (0 when the catalogue has none) | unchanged |
| 7b | **Reserve**: the estimate of §5.3 is reserved against every limit that applies to the principal (§5.5), atomically (§5.4) | 429 `rate_limited` with the headers of §5.8; 503 `unavailable` with `Retry-After: 5` when the rules cannot be loaded; connection aborted when the caller went away |
| 8 | **Credential** (unchanged) | unchanged; the reservation is released |
| 9 | **Forward** (unchanged). `forwarded` is set immediately before the proxy is called | unchanged |
| finish | After the usage record is built and handed over: **settle** when `forwarded`, **release** otherwise (§5.7) | — |

Rules that hold across steps, added to those of 3.1:

- A request refused in steps 2 to 7 reserves nothing and never reaches the limiter. A request
  refused in step 7b opens no credential and reaches no provider (T-630).
- Every limit of a request, the concurrency slot included, is taken in the one call of step 7b.
  The body is read before it: what an authenticated caller can cost before step 7b stays bounded
  by the body size and read time and by ledger admission, as since 3.2 (O-8).
- Exactly one of settle and release is called for every reservation, exactly once, from `finish`,
  which runs by `defer` and after a recovered panic. A failure inside settle or release is
  recovered and logged; it changes neither the answer nor the record.
- With no rule for the caller, step 7b does nothing: no lock is taken and no counter exists.

### 5.2 `internal/limits`: the contract

```go
type Metric string // "requests_per_minute", "tokens_per_minute", "concurrent_requests"
type SubjectKind string // "tenant", "service_account", "key"

// Limit is one effective limit a request is reserved against.
type Limit struct {
    Tenant  tenant.ID
    Subject SubjectKind
    ID      string // "" for the tenant; a service account ID; a key ID or a subject mapping ID
    Metric  Metric
    Max     int64  // 1 to 1e12
    Level   string // "platform" or "tenant": who set the value in force (diagnostic only)
}

// Amounts is what a request reserves, or what it consumed. One request is implied.
type Amounts struct {
    Tokens int64
    Cost   pricing.Cost // carried for 5.2; no metric of this sprint reads it
}

type Request struct {
    ID       string        // the request ID: diagnostics only
    Limits   []Limit       // all of one tenant; may be empty
    Estimate Amounts
    TTL      time.Duration // after it an unsettled reservation is released by the store
}

// Reservation is a value: a store over the network needs nothing else to settle it.
type Reservation struct {
    ID      string
    Limits  []Limit
    Amounts Amounts
    At      time.Time // admission time: the second the amounts are attributed to
    Expires time.Time
}

type Refusal struct {
    Limit      Limit         // the first limit, in the order of §5.5, that had no room
    Remaining  int64         // Max minus what is counted, never negative
    RetryAfter time.Duration // 1 s to 61 s, the longest over every limit that had no room
    Reset      time.Duration // until the refused counter is empty if nothing is admitted; 0 for concurrency
    OverLimit  bool          // the estimate alone exceeds Limit.Max: waiting does not help
}

// Store holds the counters. All three methods are safe for concurrent use and never block on I/O
// in the in-process implementation.
type Store interface {
    // Reserve takes Estimate (and one request, and one slot) against every limit, or nothing.
    // Exactly one of the Reservation and the Refusal is meaningful; an error means the decision
    // could not be taken and nothing was reserved.
    Reserve(ctx context.Context, r Request) (Reservation, *Refusal, error)
    // Settle replaces the reservation by actual and frees the slot. ErrNotOutstanding when the
    // reservation was already settled, released or expired; then nothing changes.
    Settle(ctx context.Context, res Reservation, actual Amounts) error
    // Release returns everything the reservation took. ErrNotOutstanding as for Settle.
    Release(ctx context.Context, res Reservation) error
}
```

`Memory` is the in-process `Store`:

```go
type MemoryOptions struct {
    Logger *slog.Logger     // required
    Now    func() time.Time // default time.Now
}
func NewMemory(o MemoryOptions) (*Memory, error)
func (m *Memory) Run(ctx context.Context)   // calls Sweep every second until ctx is done
func (m *Memory) Sweep(now time.Time) int   // releases expired reservations, drops empty counters; returns the number expired
func (m *Memory) Outstanding() int          // reservations not yet settled, released or expired
func (m *Memory) Counters() int             // counters held
```

### 5.3 The request-side estimate (D-3)

There is no tokenizer, as in ADR 0039. The estimate is computed in step 7b from the body that was
read, the request that was parsed and the target.

**What the family reads.** `family.Request` gains one method:

```go
// OutputBound is what a request says about the output it may produce.
type OutputBound struct {
    None    bool  // the operation produces no output tokens
    Tokens  int64 // the caller's limit per choice; 0 when the caller set none
    Choices int64 // the number of choices asked for; at least 1
}
OutputBound() OutputBound
```

| Family, operation | `Tokens` | `Choices` |
|---|---|---|
| `openai` `chat.completions` | `max_completion_tokens`, else `max_tokens` | `n` |
| `openai` `responses` | `max_output_tokens` | 1 |
| `openai` `embeddings` | `None` | — |
| `anthropic` `messages` | `max_tokens` | 1 |
| `anthropic` `messages.count_tokens` | `None` | — |

A member counts only when it is a top-level member whose value is a JSON number written with
decimal digits alone (no sign, fraction or exponent), of at most 12 digits and at least 1; for
`n`, at most 128. The last occurrence of a repeated member decides, as `encoding/json` does.
Anything else is "not set" (`Tokens` 0, `Choices` 1): if the provider accepts a spelling Pandokos
does not read, the request is reserved as one without a limit, which the bound of §5.6 covers.
Nothing else of the body is read and the body is not changed.

**The estimate.**

```
input  = adapter.EstimateUsage(op, Evidence{RequestBytes: len(body)}).InputTokens   // bytes / 4, rounded up; 0 for count_tokens
input  = min(input, target.ContextWindow)                 when the catalogue has one
per    = bound.Tokens                                      when the caller set a limit
       = limits.default_output_tokens                      otherwise
per    = min(per, target.MaxOutputTokens)                  when the catalogue has one
output = 0 when bound.None, else per × bound.Choices       (saturating)
tokens = input + output                                    (saturating at 1e12)
cost   = pricing.CostOf(Usage{input, output}, the price version in force at the start of the request)
```

- The input rule is the one an estimated usage record uses, so an estimated record settles its
  input at the reserved value. The cap by the context window keeps a body that is large in bytes
  and small in tokens (an inline image) from reserving more than the model could ever read.
- `cost` is unknown when the model has no price in force; tokens are reserved all the same. No
  limit of this sprint reads the cost: it is computed, tested and carried in the reservation so
  that 5.2 adds rules, not plumbing.
- `count_tokens` reserves one request and one slot and no tokens (D-17).

### 5.4 Counters of the in-process store (D-4, D-6, D-7)

A counter exists per (tenant, subject kind, subject ID, metric), created on first use.

**Sliding window** (`requests_per_minute`, `tokens_per_minute`). A ring of 61 buckets of one
second. An amount admitted at time *t* is added to the bucket of second ⌊*t*⌋ and is counted while
`now < ⌊t⌋ + 61 s`: every amount is counted for at least 60 s and less than 61 s. Consequence:
whatever is admitted within any interval of 60 s is counted together at the last admission of
that interval, so the limit holds in every such interval, not only in aligned minutes; the price
is a window up to one second longer than a minute, never shorter.

**Gauge** (`concurrent_requests`). The number of reservations outstanding.

**Reserve.** Under the lock of the tenant's shard: advance every window to now; for every limit,
in the order given, test `counted + amount ≤ Max` (amount: 1 for requests and concurrency, the
estimated tokens for tokens). If every test passes, add every amount, record the reservation
with its expiry and return it. Otherwise change nothing and return the refusal of §5.2.

**Settle.** Under the same lock: the slot is freed; the request stays counted; the tokens of the
admission second are changed by `actual − reserved` if that second is still in the window, and
not at all if it has left (the reservation left with it). The reservation is forgotten.

**Release.** The slot is freed; the request and the reserved tokens are subtracted from the
admission second if it is still in the window. The reservation is forgotten.

**Expiry.** `Sweep` releases every reservation whose `Expires` has passed, logs `limit
reservation expired` at level error with the request ID and the tenant, once each, and drops
counters whose window is empty and whose gauge is zero. The gateway sets `TTL =
limits.max_request_duration + 1 m`, which no request outlives: an expiry means a defect (a settle
that panicked), not a slow request. A settle that arrives after the expiry is `ErrNotOutstanding`
and changes nothing. A crashed instance loses its counters with its memory; expiry is what a
store over the network (epic 8) needs for that case, and the reason `Reservation` carries
`Expires`.

**`Retry-After` and `Reset`.** For a window: the time until enough of the oldest counted seconds
leave for this request to fit, rounded up to a second, at least 1 and at most 61; when
`OverLimit`, 60. For concurrency: 1. `Reset` is the time until the newest counted second leaves.
The refusal carries the longest `Retry-After` over all limits without room, so that a retry at
that time is not refused by a limit the answer did not name.

**Locks and memory.** 64 shards chosen by tenant ID, one mutex each; all limits of a request
belong to one tenant (a request that names two is an error and reserves nothing), so one lock
makes the reservation atomic. A counter is 61 integers. Counters exist only for subjects with a
rule and traffic in the last 61 s; reservations are bounded by `ledger.buffer_size`, since only
admitted requests reserve. The store reads no clock but `Now` and starts no goroutine but `Run`.

### 5.5 Limit rules: levels, subjects, resolution (D-8, D-9, D-10)

**Storage** (migration 0009, full text in plan.md, appendix A1).

- `limit_rule` (tenant-scoped, policy `tenant_isolation`): `subject_kind` (`tenant`,
  `service_account`, `key`), `subject_id` (empty for `tenant`; the service account's ID; the key
  ID of a virtual key or, for callers with a token, the ID of the subject mapping), `metric`,
  `limit_value` (1 to 10¹²); unique per (tenant, subject, metric). The admin role inserts, updates
  `limit_value` only and deletes; the gateway role reads; the worker has no privilege.
- `platform_limit_rule` (global, restricted): `tenant_id` (null: the default for every tenant),
  `metric`, `limit_value`; unique per (tenant, metric), nulls not distinct. Written by the admin
  role in platform scope only. A tenant's session reads the defaults and its own rows.
- Both tables notify `pandokos_cache`: `limits:<tenant>` for a row of one tenant, `limits` for a
  default.

No row means no limit: an installation without rules behaves as before this sprint (O-4).

**Resolution.** `RuleSet.For(serviceAccountID, keyID) []Limit`, a pure function, returns at most
nine limits in a fixed order: subject `tenant`, then `service_account`, then `key`; within a
subject `concurrent_requests`, `requests_per_minute`, `tokens_per_minute`.

| Subject | Counter key | `Max` |
|---|---|---|
| `tenant` | the tenant | the smaller of the platform ceiling and the tenant's own rule, whichever exist. The platform ceiling is the platform row of that tenant if there is one, else the default row |
| `service_account` | `Principal.ID` | the tenant's rule for that service account |
| `key` | `Principal.KeyID`: the key ID, or the mapping ID for a token | the tenant's rule for that key |

"A lower level can only restrict" holds by construction: every limit returned must admit the
request, so a rule can remove room and never add it. A key rule above the tenant's limit is
valid and has no effect. Only the platform can raise a tenant's ceiling, by a row for that tenant
that replaces the default; that is one level deciding for itself, not a lower level loosening a
higher one. Limits keyed by service account apply to keys and tokens alike (4.1 §11).

**Loading.** `LoadRules(ctx, db, tenant)` runs one tenant transaction on the gateway pool with two
queries (appendix A2). More than 5,000 rows of `limit_rule` for one tenant is an error (the
API of 5.2 enforces the count on write). `RuleCache` holds one `RuleSet` per tenant with the
mechanics of the catalogue's tenant entries: lifetime `cache.limits_ttl` from the start of the
load, single flight, a failed load is not cached, a load outdated by an invalidation is
discarded. Payloads: `limits:<tenant>` and `tenant:<tenant>` drop that tenant; `limits` drops
every tenant; the payloads other caches know are ignored; an unknown payload and a listener reset
flush. The other three caches of the gateway learn to ignore the two new payloads.

When the rules of a tenant cannot be loaded and no entry is cached, the request is answered 503
`unavailable`: a request whose limits cannot be verified is not forwarded (overview §9). A changed
rule applies to the counts already held: counters are not reset.

### 5.6 Guarantees and the bound (D-5)

For one counter with limit *L*, with the attribution rule of §5.4 (a request's amounts belong to
its admission second):

1. **Requests per minute: strict.** In every interval of 60 s the number of requests that were
   admitted and not released is at most *L*.
2. **Concurrent requests: strict.** At every instant the number of reservations outstanding is at
   most *L*.
3. **Tokens per minute: strict on reserved amounts, bounded on actual ones.** Let *I* be any
   interval of 60 s and *s* the last admission in it. Then

   `Σ actual(r), r admitted in I  ≤  L + Σ max(0, actual(r) − reserved(r)), r admitted in I and in flight at s`

   Proof: at *s* every request admitted in *I* is counted, the settled ones at their actual amount
   and the others at their reservation, and the admission at *s* required the sum to be at most
   *L*; afterwards the sum changes only by the settlements of the requests still in flight.
   Corollaries: if no request exceeds its reservation the limit is strict; the overshoot is at
   most *k* × *e*, where *k* is the number of requests in flight (at most the concurrency limit,
   when one is set) and *e* the most one request can exceed its reservation; and once the counter
   is over *L* nothing is admitted until the window has moved on, so an overshoot is not repeated
   within the window.
4. **How much one request can exceed its reservation.** Output: nothing when the caller set a
   limit the provider enforces; otherwise at most `max_output_tokens` of the model minus the
   reserved default, per choice. Input: the estimate is bytes / 4; text in languages or content
   whose tokens are shorter than four bytes, and inputs referenced rather than inlined (an image
   by URL) can exceed it, by at most the model's `context_window` minus the reserved input. When
   the catalogue has neither value for a model, Pandokos cannot state *e* for it; the provider's
   own maximum then applies. ADR 0012 named the output part only; ADR 0048 states both.
5. **Per instance.** Each instance counts alone: *N* gateway instances admit up to *N* times a
   limit until epic 8. Counters do not survive a restart; a window is at most 61 s old.

### 5.7 How a request ends (LIM-08)

Settlement uses the figures of the usage record, whatever their source: `tokens = input + output`
of the figures the cost was computed on, 0 when the record has no usage. The ledger and the
counters therefore never disagree, which the rebuild from the ledger of 5.2 depends on.

| End of the request | Limiter call | Requests | Tokens | Slot |
|---|---|---|---|---|
| Refused in steps 2 to 7 (policy, invalid body, model) | none | not counted | none | none |
| Refused in step 7b (`rate_limited`) | reserve fails | not counted | none | none |
| Refused or failed in step 8; caller gone before step 9 | release | returned | returned | freed |
| 2xx, usage from the provider | settle | counted | actual | freed |
| 2xx, no usable usage (estimated record, sources mixed) | settle | counted | the record's estimate | freed |
| Provider error (4xx, 5xx, its own 429) | settle | counted | 0 | freed |
| Provider unreachable, egress refusal, redirect, upstream timeout | settle | counted | 0 | freed |
| Caller disconnects mid-stream, upstream aborts, `max_request_duration`, forced shutdown | settle | counted | what the record holds: provider figures seen so far, else the estimate | freed |
| Panic after the reservation | settle if forwarded, else release | as above | as above | freed |
| `messages.count_tokens` | settle | counted | 0 | freed |
| Settle or release itself fails | recovered, logged at error | — | — | freed by expiry |

A request counts as a request once Pandokos tried to send it (O-10). A refused request consumes
nothing (O-9).

### 5.8 The 429 contract (D-11, LIM-09)

A refusal of step 7b:

| Part | Value |
|---|---|
| Status | 429 |
| Body | the error shape of the family, as every Pandokos error: `openai`: `type` `pandokos_error`, `code` `rate_limited`; `anthropic`: `error.type` `rate_limit_error`. Message: `Pandokos: a rate limit of Pandokos was reached; retry after the time given in Retry-After.` |
| `X-Pandokos-Error` | `rate_limited` |
| `X-Pandokos-Request-Id` | as on every response |
| `Retry-After` | whole seconds, 1 to 61 (§5.4) |
| `X-Pandokos-Limit-Subject` | `tenant`, `service_account` or `key` |
| `X-Pandokos-Limit-Metric` | `requests_per_minute`, `tokens_per_minute` or `concurrent_requests` |
| `X-Pandokos-Limit` | the limit in force for that subject and metric |
| `X-Pandokos-Limit-Remaining` | what is left of it now, 0 or more (for tokens it can be positive and smaller than the request) |
| `X-Pandokos-Limit-Reset` | whole seconds until that counter is empty; absent for `concurrent_requests` |

- One code serves the three metrics (O-6); the metric header tells them apart. `quota_exceeded`,
  which ADR 0020 already lists, is put into use by 5.2.
- A provider's own 429 passes unchanged, with the provider's `x-ratelimit-*` and `retry-after`
  headers and without `X-Pandokos-Error`: the presence of that header is the distinction, as for
  every other error (ADR 0020). The `X-Pandokos-Limit*` names collide with no provider header.
- The headers name a subject kind, never an ID, and values of the caller's own tenant only.
- The headers are set on refusals only (O-7).
- An SDK treats the answer as its rate-limit error and retries after `Retry-After` (T-646, for the
  OpenAI Go SDK; the other SDK suites follow in 5.2).

### 5.9 Record, log, span (D-12)

- **Usage record** of a refusal of step 7b: outcome `rejected_limit`, `error_code` `rate_limited`,
  model fields set (step 7 passed), no credential, no usage, cost status `none`. No column is
  added. `outcomeOf` maps the code; every other mapping is unchanged.
- **Request log line**, new fields: `reserved_tokens` when a reservation was made;
  on a refusal `limit_subject`, `limit_metric`, `limit_level`, `limit`, `retry_after_s`, and
  `estimate_over_limit: true` when waiting cannot help. No subject ID beyond the principal fields
  the line already has.
- **Other log lines**: `limit reservation expired` (error; request ID, tenant); `limit settle
  failed` (error; request ID, panic type); `limit rules unavailable` (warn; tenant; at most what
  the catalogue cache logs for its loads).
- **Spans**: the server span of a refusal ends as for every Pandokos error; no attribute is added
  (6.1 adds metrics).

### 5.10 Interfaces of the gateway (D-18)

```go
// LimitEstimate is what a call is expected to consume (§5.3).
type LimitEstimate struct {
    RequestID                 string
    InputTokens, OutputTokens int64
    Cost                      pricing.Cost
}

// LimitUsage is what a call consumed: the figures of its usage record.
type LimitUsage struct {
    Tokens int64
    Cost   pricing.Cost
}

// LimitRefusal is a refusal of step 7b, in plain values.
type LimitRefusal struct {
    Level, Subject, Metric string
    Limit, Remaining       int64
    RetryAfter, Reset      time.Duration
    OverLimit              bool
}

// Limits reserves capacity for a call. A nil Limits enforces nothing.
type Limits interface {
    // Reserve returns the reservation of an admitted call (never nil then), a refusal, or an
    // error when the limits of the tenant cannot be verified now.
    Reserve(ctx context.Context, p authz.Principal, e LimitEstimate) (Reservation, *LimitRefusal, error)
}

// Reservation is settled or released exactly once by the gateway. Neither method blocks or
// returns an error: the implementation logs what fails.
type Reservation interface {
    Settle(u LimitUsage)
    Release()
}
```

`Options` gains `Limits Limits` (optional) and `DefaultOutputTokens int64` (must be positive);
`Target` gains `MaxOutputTokens` and `ContextWindow` (`int64`, 0 unknown). The gateway does not
import `internal/limits` (depguard: "the gateway sees limits through gateway.Limits").
`resolver.Limits` implements the port: `RuleCache.Rules` → `RuleSet.For` → with no limit, a
reservation that does nothing; otherwise `Store.Reserve` with the TTL it was built with. Its
`Settle` and `Release` call the store with a context that is not the request's (the request's is
often cancelled by then) and log `ErrNotOutstanding` at error.

### 5.11 Configuration

| Key | Type | Default | Bounds | Meaning |
|---|---|---|---|---|
| `limits.default_output_tokens` | integer | `4096` | 1 to 1,000,000 | Output tokens reserved per choice for a request that sets no output limit; capped by the model's `max_output_tokens` when the catalogue has it |
| `cache.limits_ttl` | duration | `30s` | 1 s to 5 m | Lifetime of a tenant's cached limit rules in the gateway: the upper bound on the time a changed rule takes to apply when no notification arrives |

Both are read like `cache.catalog_ttl`; a value out of bounds is a start-up error with the key
named. The reservation lifetime is derived (`limits.max_request_duration` + 1 m) and is not a key
(O-13). Rules are not configuration: ADR 0019 puts limits with the state managed through the
API.

### 5.12 Wiring (`cmd/pandokos`, `internal/server`)

`serve` builds one `limits.Memory`, one `limits.RuleCache` on the gateway pool and
`resolver.NewLimits`; passes it through `server.Options.Limits`; adds `RuleCache.Invalidate` and
`RuleCache.Flush` to the listener's callbacks; runs `Memory.Run` with the background context of
the process, stopped after the handlers have ended. Nothing is persisted and nothing is read at
start.

## 6. Isolation harness

- `limit_rule`: a fixture in `DefaultSpec.Fixtures` (insert a tenant rule, update `limit_value`);
  the generic negative tests of 1.2 then cover it for every runtime role. Grants in `DefaultSpec`.
- `platform_limit_rule`: in `DefaultSpec.Global`, restricted, with the reason "ceilings the
  platform sets for tenants; a tenant's session sees the defaults and its own rows, the platform
  scope all of them; protected by its own policies"; its own test (T-626).
- `notify_limits` is not a definer function; nothing is added to `SecurityDefiner`.
- Counters are not in the database; their isolation is T-621 (unit) and T-644 (end to end).
- No route is added, so no permission and no cross-tenant route test is added; the inference
  routes keep theirs.

## 7. Test infrastructure and CI

- Time: the store, the cache and the estimator take a clock; no test sleeps to move a window. The
  load test (T-640) uses the real clock for its parallel phase and a clock offset for "61 s later".
- Property tests use `math/rand/v2` with a seed that is logged and can be set through
  `PANDOKOS_TEST_SEED`; a failure prints the seed and the shortest failing prefix of the schedule.
  No module is added.
- Rules in tests are written through the admin role in the right session
  (`dbtest.SeedLimitRule`, `dbtest.SeedPlatformLimit`), never as a superuser, so that every
  database test also exercises the grants.
- `testutil.StaticLimits` is a `gateway.Limits` with fixed answers and a call log, for the
  pipeline tests; tests of behaviour under load use the real `Memory`.
- CI: no new job. Everything runs in `go test -race -count=1 ./...`; `make bench` also runs the
  limiter benchmark.

## 8. Decisions

| # | Decision | Reason | Recorded in |
|---|---|---|---|
| D-1 | **Reserve is step 7b**, after the model check and before the credential; every limit of the request, the concurrency slot included, is taken in that one atomic call | The estimate needs the body, the model and its catalogue values; a refused caller must cost no decryption (3.1 §11); one call is what makes "all or nothing" simple to prove | ADR 0048 |
| D-2 | **Settlement equals the usage record; release only when nothing was sent** | One source of truth for the ledger and the counters (3.2 §11), required by the rebuild of 5.2; "forwarded" is one flag set in one place | ADR 0048 |
| D-3 | **The estimate**: input bytes / 4 through `Adapter.EstimateUsage`, capped by the context window; output from the caller's limit times the choices, else `limits.default_output_tokens`, capped by the model's maximum; cost from the price in force | Same input rule as an estimated record; ADR 0012 expects a default below the maximum ("actual usage can exceed the estimate when the caller sets no output limit"); reserving a model's full maximum would let one request in flight exhaust a token limit | ADR 0048 |
| D-4 | **Windows of 61 one-second buckets; amounts belong to the admission second** | Strict in every 60 s interval with 61 integers per counter; an exact log costs memory per request, the two-bucket approximation is not strict | ADR 0048 |
| D-5 | **The bound of §5.6 is the documented guarantee**; it names input and output | LIM-07 asks for a documented maximum; ADR 0012's wording covered the output only | ADR 0048 |
| D-6 | **`Store` has `Reserve`, `Settle`, `Release`; a reservation is a value with an expiry**; the gateway's TTL is `max_request_duration` + 1 m; a settle after expiry changes nothing | The same interface must fit Valkey (epic 8), where expiry covers a crashed instance; in process an expiry can only mean a defect and is logged as one | ADR 0048 |
| D-7 | **In-process store: 64 shards by tenant, one sweeper, no database** | A request's limits are of one tenant, so one lock is atomic; no I/O on the hot path (ADR 0012) | sprint decision |
| D-8 | **Rules: platform ceilings in a global table, tenant rules in a tenant table; subjects tenant, service account, key; every applicable counter must admit** | "A lower level can only restrict" by construction, with no validation between levels to get wrong; the service-account subject is what makes one rule cover keys and tokens (4.1 §11) | ADR 0049 |
| D-9 | **Rules reach the gateway through a per-tenant cache** with lifetime and notifications; rules that cannot be loaded refuse with 503 | Same mechanics and failure rule as the catalogue's tenant entries | ADR 0049 |
| D-10 | **No rule, no limit; the migration seeds nothing** | An upgrade must not start refusing traffic | ADR 0049, O-4 |
| D-11 | **One code, `rate_limited`, for the three metrics; five `X-Pandokos-Limit*` headers and `Retry-After` on refusals** | ADR 0020 already has the code; the metric is data, not a new contract; own header names cannot collide with a provider's | ADR 0048, history of 0020 |
| D-12 | **A refusal writes a usage record with outcome `rejected_limit`; no new column** | The epic asks for the record; which limit refused is in the log line and, from 6.1, in metrics | ADR 0048, history of 0039 |
| D-13 | **No administration route, permission, CLI command or audit action in this sprint** | The epic gives them to 5.2, which designs them once for rates, quotas and budgets; tests write rules through the admin role's grants | sprint decision, O-1 |
| D-14 | **`checks.budget` of the capability check stays `not_enforced`** | It reports budgets, which arrive in 5.2; a rate limit is not a budget and its state lasts seconds | sprint decision; epic file |
| D-15 | **3.2 O-5 confirmed again**: the output estimate of a stream stays its count of content events, and limits settle with it | The settlement must equal the record; the estimate only applies to answers without provider usage; changing the ledger's rule to suit limits would change records' meaning | history of 0039 |
| D-16 | **3.2 O-14 not built and not scheduled** | A JSON answer over 4 MiB is in practice an embeddings batch, whose output is zero and whose input settles at the reserved estimate: the scanner would change no counter | sprint decision |
| D-17 | **`count_tokens` is one request and no tokens** | It is a call to the provider with the tenant's credential; it consumes nothing (3.3 §11) | ADR 0048 |
| D-18 | **`Options.Limits` is optional; nil enforces nothing; `serve` always sets it** | Keeps the gateway's existing tests and ports style (ADR 0037 rule 8); T-643 proves the binary enforces | sprint decision |
| D-19 | **No new dependency**; property tests on `math/rand/v2` with a logged seed | ADR 0018 | sprint decision |
| D-20 | **Counters are per instance and not rebuilt at start** | ADR 0012: rate windows are short-lived and not reconciled; several instances are epic 8 | ADR 0048 |

## 9. Tests

Levels: **U** unit; **D** needs the test database; **I** in-process gateway with the fake
provider; **E** the built binary, end to end; **S** SDK suite (nested module); **R** repository
test. **P** marks a property test, **−race** a test whose value is the race detector. IDs
continue from T-609 and T-R39.

### New tests

| ID | Test | Level | Proves | Requirement |
|---|---|---|---|---|
| T-610 | `limits.TestWindow` | U | An amount is counted for at least 60 s and less than 61 s at every sub-second offset; gaps longer than the window empty it; totals equal the sum of live buckets after any sequence of advances | LIM-01 |
| T-611 | `limits.TestReserveAtomic` | U | With nine limits of which any one has no room, no counter changes and no reservation exists; the refusal names the first limit without room in the order of §5.5; `RetryAfter` is the longest; a request with limits of two tenants is an error | **LIM-06**, LIM-04 |
| T-612 | `limits.TestSettle` | U | Tokens below the reservation return the remainder at once; above it the excess is counted; after the admission second left the window nothing changes; the request stays counted; the slot is freed; a second settle is `ErrNotOutstanding` and changes nothing | **LIM-06** |
| T-613 | `limits.TestRelease` | U | After release the store equals its state before the reserve, also across a window advance; a second release changes nothing | **LIM-08**, GW-04 |
| T-614 | `limits.TestExpiry` | U | A reservation is never expired before its TTL; `Sweep` after it frees slot, request and tokens, logs once at error with the request ID; a later settle is `ErrNotOutstanding`; empty counters are dropped; `Run` returns when its context ends | LIM-08, GW-04 |
| T-615 | `limits.TestRetryAfter` | U | The table of §5.4 for requests, tokens (several seconds must leave), `OverLimit` (60), concurrency (1); always 1 to 61; a retry at exactly `RetryAfter` is admitted when nothing else changed | **LIM-09** |
| T-616 | `limits.TestPropertyRequests` | U, P | Random schedules of reserve, settle, release and clock steps against a reference that keeps every event: in every 60 s interval admitted-and-not-released requests ≤ *L*; outstanding ≤ concurrency limit at every instant; the store never admits what the exact 60 s reference refuses and never refuses what the exact 61 s reference admits | **LIM-01**, **GW-16** |
| T-617 | `limits.TestPropertyTokens` | U, P | The inequality of §5.6 (3) for every 60 s interval of random schedules whose actual amounts fall below, at and above the reservations; with no request above its reservation the limit is strict | **LIM-07** |
| T-618 | `limits.TestPropertyConservation` | U, P | After any schedule in which every reservation is settled, released or expired: `Outstanding` is 0, every gauge is 0, and 61 s later `Counters` is 0 | **LIM-08**, GW-04 |
| T-619 | `limits.TestResolve` | U, P | The table of §5.5; for random rule sets: the `Max` of every returned limit is at most every rule that applies to its subject; adding or lowering a service-account or key rule never changes or removes a tenant limit; a platform row for the tenant replaces the default, in both directions; order and count of the result | **LIM-04**, IAM-04 |
| T-620 | `limits.TestMemoryRace` | U, −race | 64 goroutines reserve, settle, release and sweep on 8 tenants that share subject IDs: no race reported; the invariants of T-616 and T-618 hold at the end | LIM-06 |
| T-621 | `limits.TestTenantSeparation` | U | Two tenants with identical service-account and key IDs and identical rules: exhausting one changes no counter of the other | **TEN-06** |
| T-622 | `limits.BenchmarkReserveSettle` | U | Reported, no threshold: ns and allocations per reserve-and-settle with nine limits, sequential and parallel on one tenant and on many | — |
| T-623 | `limits.TestRuleCache` | U | Lifetime from the start of the load; single flight; a failed load is not cached; a load outdated by an invalidation is discarded; `limits:<tenant>` and `tenant:<tenant>` drop one tenant, `limits` all; known foreign payloads are ignored; an unknown payload and `Flush` empty it | LIM-04 |
| T-624 | `limits.TestLoadRules` | D | Rows of both tables become the `RuleSet` of §5.5; a tenant row replaces the default, another tenant's row is never seen; 5,001 rules are an error; the load runs as the gateway role in the tenant's session | LIM-04, TEN-04 |
| T-625 | `store.TestLimitNotifications` | D | Insert, update and delete on `limit_rule` notify `limits:<tenant>`; on `platform_limit_rule` `limits` for a default and `limits:<tenant>` for a tenant row; nothing else | LIM-04 |
| T-626 | `isolation.TestLimitRulePolicies` | D | `platform_limit_rule` for each runtime role and session (tenant A, tenant B, platform, none): reads as §5.5; writes only by the admin role in platform scope; the gateway and the worker write nothing, the worker reads nothing of either table; the checks (metric, range, subject format, tenant subject without ID, uniqueness with a null tenant); only `limit_value` can be updated | **TEN-04**, **TEN-07** |
| T-627 | `openai.TestOutputBound`, `anthropic.TestOutputBound`, `FuzzOutputBound` (both) | U | The table of §5.3 per operation; precedence of `max_completion_tokens`; repeated members; every spelling that is "not set" (negative, zero, fraction, exponent, string, null, 13 digits, nested member of the same name); `n` out of range; the body is unchanged; fuzz: no panic, `Choices` ≥ 1, `Tokens` ≥ 0 | LIM-06 |
| T-628 | `gateway.TestEstimate` | U | Each line of the formula of §5.3 at its boundary: context-window cap, model cap on a caller limit and on the default, choices with saturation, unknown catalogue values, unknown price (cost unknown, tokens reserved), `count_tokens` and embeddings | **LIM-06**, LIM-07 |
| T-629 | `resolver.TestCatalogueLimitsValues` | U | `Target.MaxOutputTokens` and `ContextWindow` come from the catalogue model; 0 when unset | LIM-07 |
| T-630 | `gateway.TestReserveStep` | I | Reserve is called once, after `Resolve` and before `Open`, with the estimate of §5.3 and the principal; refusals of steps 2 to 7 never call it; a refusal of 7b opens no credential and sends nothing; both families | **LIM-06** |
| T-631 | `gateway.TestRateLimited` | I | For both families and each metric: status, body shape, every header of §5.8 with its exact value, integer `Retry-After`; absent `Reset` for concurrency; a provider's own 429 passes unchanged with its headers and without `X-Pandokos-Error` | **LIM-09** |
| T-632 | `gateway.TestRejectedLimitRecord` | I | The record of §5.9 (outcome, code, model, no credential, no usage, cost `none`); the log fields; `estimate_over_limit`; the spans end as for other refusals | **LIM-09**, LIM-08 |
| T-633 | `gateway.TestSettleEqualsRecord` | I | For provider usage, estimated usage and mixed sources, stream and not, both families: the settled tokens equal the record's figures; provider errors and unreachable providers settle 0; `count_tokens` settles 0 | **LIM-06**, **LIM-08** |
| T-634 | `gateway.TestReleaseWhenNotForwarded` | I | `no_credential`, credential denied, credential unavailable, caller gone before step 9: release once, settle never | **LIM-08** |
| T-635 | `gateway.TestEveryEndSettles` | I | Client disconnect mid-stream, upstream abort mid-stream, `max_request_duration`, forced shutdown, a panic before and after the response header, a usage reader that panics: exactly one settle or release per reservation; the disconnect settles what the record holds | **GW-04**, **LIM-08** |
| T-636 | `gateway.TestLimiterFailures` | I | `Reserve` returning an error: 503, `Retry-After: 5`, record `internal_error`, nothing forwarded; a `Settle` or `Release` that panics: answer and record unchanged, one error log line | LIM-08 |
| T-637 | `resolver.TestLimitsAdapter` | U | No applicable rule: no store call and a reservation that does nothing; the mapping of principal to subjects for a key and for a token (mapping ID); refusal fields; `ErrNotOutstanding` is logged; the store is called with a context that outlives the request's | LIM-04, IAM-04 |
| T-638 | `gateway.TestConcurrencyLimit` | I | With the real store and limit 3: three streams held open, the fourth is refused with metric `concurrent_requests` and `Retry-After: 1`; when one ends, by completion, by disconnect or by upstream error, the next is admitted | **GW-16** |
| T-639 | `gateway.TestLevels` | I, D | Rules written to the database through the admin role: a platform ceiling below the tenant rule wins and the reverse; a key rule above the tenant limit has no effect; a key rule restricts that key only; a service-account rule spans two keys and a token of that account; a token is limited by a `key` rule on its mapping ID; a changed rule applies after its notification without a restart | **LIM-04**, **IAM-04** |
| T-640 | `gateway.TestParallelLoad` | I, −race | The exit condition, with the real store and 64 workers. (a) 2,000 requests, tenant limit 137 requests per minute: exactly 137 forwarded, 1,863 answered 429, each with a record. (b) token limit *L*, concurrency limit *k*, a provider that reports *d* tokens above every reservation: the tokens of forwarded requests are at most *L* + *k*·*d*, and at most *L* when *d* is 0. (c) a third of the requests cancelled by the caller, a third failing upstream, some refused for no credential: afterwards `Outstanding` is 0 and every gauge is 0, and 61 s later (clock offset) `Counters` is 0. At most 10 s | **LIM-01**, **LIM-07**, **LIM-08**, GW-04 |
| T-641 | `config.TestLimitsKeys` | U | Defaults and bounds of §5.11; the error names the key | SEC-04 |
| T-642 | `server.TestLimitsWiring` | U | `Options.Limits` and `limits.default_output_tokens` reach the gateway: a server built with a refusing `Limits` answers 429; one built without answers as before | LIM-06 |
| T-643 | `e2e.TestRateLimit` | E | The binary, both families: with a seeded limit of 5 requests per minute the sixth call is the 429 of §5.8; the ledger holds five `ok` rows and one `rejected_limit`; raising the limit in the database admits the next call within 2 s; with no rule nothing is refused | **LIM-01**, **LIM-09** |
| T-644 | `e2e.TestLimitsPerTenant` | E | Two tenants with the same rules: one exhausted, the other unaffected; a platform default applies to both; a platform row for one tenant changes that tenant only | **TEN-06**, LIM-04 |
| T-645 | `e2e.TestLimitCanary` | E | At log level `debug`, a refused and a settled request whose body, provider credential and virtual key carry canaries: no log line, record, header or error body holds them | OBS-04 |
| T-646 | `sdk.TestRateLimitError` | S | The OpenAI Go SDK raises its error type for the refusal, with status 429 and `x-pandokos-error` readable from it; with the SDK's default retries a call refused for concurrency succeeds on the SDK's own retry, after `Retry-After` | **LIM-09** |
| T-R40 | `repo.TestLimitsImports` | R | `internal/gateway` does not import `internal/limits`; `internal/limits` imports only the standard library, `tenant`, `pricing`, `store` and the generated queries; no file outside `internal/limits` and `test` names `limits.Memory` except `cmd/pandokos` | — |
| T-R41 | `repo.TestLimitsGuide` | R | `docs/operations/limits.md` names every metric, every subject kind and every header constant of the code, and the two configuration keys | LIM-08 |

### Changed tests of earlier sprints

| Test | Change |
|---|---|
| T-140 (schema lint), `TestDefaultSpecMatchesDesign` | two tables, one function, their grants and the fixture |
| the migration tests (count, checksums) | migration 0009 |
| the cache tests of `authn`, the vault opener and the catalogue | payloads `limits` and `limits:<tenant>` are ignored |
| the sweeps over every error code (`familycodes`, the shape tests of both families) | `rate_limited`: status 429, message, Anthropic type `rate_limit_error` |
| `gateway` outcome table (3.2 §5.5) | `rate_limited` → `rejected_limit` |
| T-426 (pipeline order), both families | step 7b sits between the model and the credential |
| `gateway.TestNew` | `DefaultOutputTokens` must be positive; `Limits` may be nil |
| the configuration reference test | two keys |
| `family.Request` fakes in tests | `OutputBound` |

### Requirement coverage

| Requirement | Tests |
|---|---|
| LIM-01 | T-610, T-616, T-640, T-643 |
| LIM-04 | T-611, T-619, T-623 to T-625, T-637, T-639, T-644 |
| LIM-06 | T-611, T-612, T-627, T-628, T-630, T-633 |
| LIM-07 | T-617, T-628, T-629, T-640 |
| LIM-08 | T-613, T-614, T-618, T-632 to T-636, T-640, T-R41 |
| LIM-09 | T-615, T-631, T-632, T-643, T-646 |
| GW-16 (concurrency) | T-616, T-638 |
| GW-04 (reservations) | T-613, T-614, T-618, T-635, T-640 |
| IAM-04 (limits) | T-619, T-637, T-639 |
| TEN-04, TEN-06, TEN-07 | T-621, T-624, T-626, T-644, T-140 |

## 10. Definition of done for this sprint

**Requirements and tests**

- [ ] Every test of §9 exists under the stated name and passes; the changed tests pass; `make
      check`, `make test-container` and `make test-compose` pass.
- [ ] The tests-first record exists: the commit of part B precedes the implementation commits and
      the pull request quotes its failing output.
- [ ] The property tests ran with at least three different seeds besides the default, and
      `go test -race -count=20 ./internal/limits/` passes; both are quoted in the pull request.
- [ ] The coverage table is repeated in the pull request with links to the CI run.

**Exit conditions of the epic's sprint**

- [ ] Under the parallel load test admitted requests never exceed a request-rate limit (T-640 a,
      T-616).
- [ ] Token overshoot stays within the documented bound (T-640 b, T-617), and the bound is in
      `docs/operations/limits.md` in the words of §5.6.
- [ ] A failed or cancelled request leaves nothing reserved (T-640 c, T-618, T-635).

**Isolation and permissions**

- [ ] `limit_rule` has the policy `tenant_isolation` and a fixture; `platform_limit_rule` is in
      `DefaultSpec` with its reason; T-140 reports nothing; T-626 passes.
- [ ] The gateway role and the worker role can write neither table.
- [ ] No route was added; `api/openapi/admin.v1.yaml` and `internal/authz` are unchanged.

**Secrets and content**

- [ ] T-645 passes at log level `debug`; `make secrets` passes.
- [ ] The limiter receives numbers and identifiers only: no body, no header value, no credential.

**Documentation and decisions**

- [ ] `docs/operations/limits.md`; `configuration.md`; `gateway.md`; `usage.md`; `database.md`;
      overview (step 5 exists; the sentence that says it is not built is removed), data model and
      security model.
- [ ] ADR 0048 and 0049 with status `proposed`; history lines on 0012, 0020, 0037, 0039; the
      index of `docs/adr/README.md`.
- [ ] The epic file says that `checks.budget` and the per-model default belong to 5.2.
- [ ] `status:` of this file is `done`; the review records the answers to §12.

## 11. Consequences for later sprints

- **5.2** (quotas and budgets):
  - *Rules.* Adds metrics to the two check constraints (requests, tokens and cost per day and
    month) and scope columns for provider and model (`NOT NULL DEFAULT ''`, added to the unique
    keys); `RuleSet.For` gains the target and returns more limits; nothing of the three existing
    metrics changes. The rule count per tenant (5,000) is enforced on write.
  - *Administration API and CLI.* Operations on both tables with permissions (a tenant
    permission for `limit_rule`, a platform permission for `platform_limit_rule`), cross-tenant
    negative tests, audit actions for create, change and delete (AUD-01), existence checks of the
    subject on write. The grants already exist; the handlers need no migration for them.
  - *Period counters.* A second counter kind behind the same `Store`: `Reserve` adds the
    estimate to the period's counter, `Settle` replaces it, `Release` returns it; here a
    reservation that outlives its admission second matters, and `Expires` is what bounds it.
    `Amounts.Cost` is already reserved and settled; budgets add the metric that reads it, the
    currency rule (2.2 O-6) and the behaviour with unknown prices.
  - *Rebuild and reconciliation.* Period counters are rebuilt from `usage_record` at start and
    reconciled by the worker; this works because settlement equals the record (D-2). Requests
    with outcome `rejected_limit` are not usage. Rate windows and the gauge are not rebuilt.
  - *The code `quota_exceeded`* and its headers follow §5.8; `LimitRefusal` gains the code.
  - *SDK suites.* The Python and TypeScript suites gain a Pandokos 429 for both families once
    the CLI can write a rule.
  - *Capability check.* `checks.budget` becomes `available` or `exhausted` with the reason
    `budget_exhausted`; it needs a read of a counter, which `Store` does not have yet.
  - *LIM-11* applies to stores that can fail; `Store.Reserve` already returns an error.
  - *LIM-12.* `limits.md` is the page to extend; its table of what is strict exists.
  - *Per-model default* for the output reservation (O-5), if confirmed: a catalogue column read
    in the formula of §5.3 before the configuration key.
- **6.1** (metrics): limit rejections by metric and subject kind; reservations outstanding;
  expired reservations (which must stay at zero); reserved against settled tokens, the measure of
  the estimate's quality; rule-cache loads.
- **6.2** (reports, retention): `rejected_limit` rows can be many under a retry storm (R-6);
  reports count them apart from usage.
- **6.3** (tenant deletion): the tenant's rows of both tables are removed with it.
- **6.4** (events): threshold and exhaustion events (LIM-13) are produced where 5.2 records
  them; the refusal of step 7b itself produces none.
- **Epic 8** (shared counters): a Valkey `Store` implements the three methods as atomic scripts
  over the same keys (tenant, subject, metric), with `Reservation.Expires` as the key lifetime
  of an abandoned reservation; the property tests of this sprint are written against `Store` and
  run against it unchanged. The gauge needs the expiry for crashed instances. `RuleCache` is per
  instance and already driven by notifications. Until then each instance enforces alone (§5.6).
- **4.2 and 4.3**: users do not call the inference API; nothing changes. A custom role does not
  affect limits.
- **R4** (unified endpoint): the estimate runs per resolved provider; `OutputBound` is per family.

## 12. Risks and open points

### What was verified while designing

- Migration 0009 as written in the plan was applied after 0001 to 0008 on the test PostgreSQL 16
  (2026-11-17) and exercised as each runtime role: tenant rows visible to their tenant only;
  defaults visible to every session of the gateway and admin roles; a tenant row of the platform
  table visible to that tenant and the platform scope; writes of the platform table refused
  outside the platform scope (row-level security) and for the gateway and worker roles
  (privilege); a second default for a metric refused by the unique key; an update of `metric`
  refused by the column grant; the three notification payloads received. The schema lint and
  `sqlc` were **not** run on it: part A does.
- The highest test IDs in the repository are T-609 and T-R39; the highest ADR is 0047; the last
  migration is 0008.
- `model.max_output_tokens` and `model.context_window` exist (migration 0004) and are in the
  catalogue snapshot; `gateway.Target` does not carry them yet.
- `usage.OutcomeRejectedLimit` and the table's check constraint already accept `rejected_limit`;
  `error_code` accepts `rate_limited`. No migration touches `usage_record`.
- The three caches that listen on `pandokos_cache` flush on a payload they do not know: an
  instance of the previous version stays correct during an upgrade, at the price of flushes.
- `finish` runs by `defer` and recovers panics, including `http.ErrAbortHandler`; it is the only
  place that needs the settle.
- The nested SDK module holds the OpenAI Go SDK only; the Anthropic SDKs are in the Python and
  TypeScript suites, which drive the built binary through the CLI.
- Not verified: the behaviour of the official SDKs on a 429 with `Retry-After` beyond what their
  documentation states; T-646 does for one of them.

### Risks

| # | Risk | Mitigation |
|---|---|---|
| R-1 | A path on which a request ends without settle or release leaks a concurrency slot until expiry | One place (`finish`); T-635 enumerates the ends; T-618 and T-640 (c) check conservation; expiry frees it and logs at error; 6.1 alerts on expiries |
| R-2 | The input estimate is far below the actual input (short tokens, referenced inputs) and a tenant overshoots a token limit | Bounded and documented (§5.6); the settlement counts the actual amount, so the next requests are refused until the window moves; operators can add a concurrency limit to bound *k* |
| R-3 | The estimate is far above the actual usage and requests are refused that would have fitted (large bodies, callers that set a high output limit by habit) | Context-window and model caps; the remainder returns at settlement; `reserved_tokens` in the log and `estimate_over_limit` make it visible; `limits.md` says how to size a token limit (not below the largest request) |
| R-4 | A request whose estimate exceeds the limit is refused forever and SDKs retry it | `Retry-After: 60` bounds the retry rate; the log field names the case; documented |
| R-5 | Lock contention on a very busy tenant | The critical section is a few integer operations on at most nine counters; T-622 reports it; shards keep tenants apart |
| R-6 | A caller that ignores `Retry-After` writes a ledger row per refusal | The refusal is cheap (no decryption, no provider call) and the ledger's admission already bounds memory; 6.1 measures it, 6.2 decides retention; O-11 |
| R-7 | Operators assume limits hold across instances | Stated at the top of `limits.md`, in the overview's table and in the release notes; epic 8 |
| R-8 | The window is up to a second longer than a minute and a test or a customer expects exactly 60 s | Documented as "never shorter"; `Retry-After` is computed from the real expiry, so retries succeed |
| R-9 | A spelling of the output limit that a provider accepts and Pandokos reads as "not set" | Falls to the default, inside the documented bound; fuzzed (T-627); the rule is in ADR 0048 |

### Open points (raised to the maintainer; none blocks the sprint)

| # | Point | Recommendation |
|---|---|---|
| O-1 | Rules can be enforced but not written through the product until 5.2 (D-13). Alternative: a minimal `PUT/GET/DELETE` on both tables now, redesigned when quotas and budgets arrive | Keep D-13: both sprints ship in v0.2; one API design for all kinds of limit |
| O-2 | "Platform level" is a ceiling the platform sets per tenant (a default and per-tenant rows). Alternative or addition: a limit on the sum over all tenants of an installation | As designed; an installation-wide sum is a new subject kind when someone asks |
| O-3 | Three subjects below the platform: tenant, service account, key. Alternative: tenant and key only, as LIM-04's wording | Keep the service account: it is the only way one rule covers a workload's keys and tokens (4.1 §11), at the cost of one value in a check constraint |
| O-4 | No rule is seeded: per-tenant concurrency is unlimited until the platform sets a default (GW-16 is met as "can be limited"). Alternative: seed a default of 256 concurrent requests per tenant | No seed in the migration; 5.2's guide tells operators to set a platform default; revisit a shipped default with the Helm chart |
| O-5 | The reservation for a request without output limit is one configuration value, capped per model by `max_output_tokens`. The epic's risk note says "per model and configurable": alternative, a catalogue column `default_output_tokens` | Defer the column to 5.2, where budgets make it matter; the cap already makes the value per model when the catalogue is filled |
| O-6 | One code for the three metrics. Alternative: `concurrency_limited` for the gauge | One code; the metric header and the log field distinguish; a new stable code is a one-way door |
| O-7 | Allowance headers only on refusals. Alternative: on every admitted response, for the tightest limit | Not now: which limit is "the tightest" across metrics needs a rule of its own; decide with 6.1 or on request |
| O-8 | The concurrency slot is taken in step 7b, after the body was read. Alternative: a second, earlier call right after authentication | As designed (D-1): one atomic call; body reading is bounded by size, time and ledger admission |
| O-9 | A refused request does not count against the request rate. Alternative: count it, to penalize callers that hammer | As designed: a refusal that consumed allowance would make `Retry-After` wrong |
| O-10 | A request that was sent and failed upstream stays counted as a request; its tokens settle at 0 | As designed: "strict" then has one meaning, requests Pandokos tried to send |
| O-11 | Every refusal writes a usage record (D-12). Alternative: sample or aggregate refusals | Keep one record per refusal, as the epic says; revisit with the measurements of 6.1 |
| O-12 | No tokenizer: input is bytes / 4 capped by the context window | Keep (ADR 0039's rule); a tokenizer per provider is a dependency and still an estimate |
| O-13 | The reservation lifetime is derived, not configurable | Keep: the only safe value is "longer than any request" |

### Maintainer decisions on the open points (2026-11-17)

| Point | Decision |
|---|---|
| O-1 | Accepted: no API, CLI or permission to write rules in 5.1; sprint 5.2 designs one administration API for every limit kind. Both sprints ship in v0.2 |
| O-2 | Accepted: the platform level is a per-tenant ceiling and a default, not a sum over tenants |
| O-3 | Accepted: the service-account subject is kept beside tenant and key |
| O-4 | Accepted: no seeded default concurrency; revisited with the Helm chart |
| O-5 | Deferred to 5.2: a per-model default output reservation |
| O-6 | Accepted: one code `rate_limited`, the metric named in a header |
| O-7 | Accepted: no allowance headers on admitted responses for now |
| O-8 | Accepted: the concurrency slot is taken after the body is read |
| O-9 | Accepted: a refused request does not count against the request rate |
| O-10 | Accepted: a request sent and failed upstream counts as a request, its tokens settle at 0 |
| O-11 | Accepted: one usage record per refusal; revisited with the measurements of 6.1 |
| O-12 | Accepted: no tokenizer; input is bytes / 4 capped by the context window |
| O-13 | Accepted: the reservation lifetime is derived, not configurable |
