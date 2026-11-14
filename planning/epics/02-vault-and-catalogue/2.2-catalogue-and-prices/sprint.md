---
id: SPRINT-2.2
title: Catalogue and prices
epic: EPIC-2
release: v0.1
status: in-progress
design: draft 2026-11-14
---

# Sprint 2.2 — Catalogue and prices

- Epic: [2 — Vault and catalogue](../epic.md)
- Implementation plan: [plan.md](plan.md)
- Previous sprint: [2.1 — Vault](../2.1-vault/sprint.md)
- Decisions implemented: ADR [0011](../../../../docs/adr/0011-pricing-source.md)
  (primary), the catalogue part of [0009](../../../../docs/adr/0009-native-passthrough-by-api-family.md),
  [0004](../../../../docs/adr/0004-postgresql-and-rls.md) and
  [0027](../../../../docs/adr/0027-database-roles-and-session-context.md) (tables, roles, session),
  [0031](../../../../docs/adr/0031-authorization-core-and-provisioning.md) (permissions),
  [0030](../../../../docs/adr/0030-keys-format-storage-lookup.md) (listener, cache pattern),
  [0032](../../../../docs/adr/0032-audit-log-first-version.md), [0017](../../../../docs/adr/0017-admin-api-spec-first.md)
  and [0028](../../../../docs/adr/0028-admin-api-generator.md) (generator),
  [0029](../../../../docs/adr/0029-static-sql-sqlc.md), [0019](../../../../docs/adr/0019-configuration.md),
  [0020](../../../../docs/adr/0020-error-contract.md), [0018](../../../../docs/adr/0018-supply-chain.md)
  (no new Go dependency; one embedded third-party data file)
- New decision records written by this sprint: **0035** (catalogue and model access) and **0036**
  (prices, costs and the price importer); history lines on 0009, 0011, 0027, 0028, 0030. Content
  in [plan.md](plan.md), part E.

This document is the design. The implementer follows [plan.md](plan.md); a gap in either document
is raised as a question, not filled silently ([planning/README.md](../../../README.md)).

Maintainer decisions of epics 1 and 2 taken as given: database TLS is `verify-full` or `disable`;
one installation per PostgreSQL cluster; provider traffic does not honour `HTTPS_PROXY` until 3.1;
the in-repository generator stays; platform operations on tenant data run in the named tenant's
session (2.1 O-1); `credential:use` stays separate from `model:invoke` (2.1 O-4).

## 1. Outcome

The platform administrator describes, through the administration API and the CLI, which
providers exist (each with one API family and one or more endpoints, public or explicitly
private), which models each offers (public name, upstream identifier, capabilities, status,
limits), and which tenants may use each model: all of them, or the ones it is granted to. A
tenant administrator narrows that for the whole tenant and, when issuing a key, for that key. A
database lookup and a gateway-side cache answer "may this principal use this model of this
provider" with one function, which sprint 3.1 calls in the inference path.

Every model can carry versioned prices in a currency. Prices come from an embedded snapshot of
the open `genai-prices` dataset, imported when `serve` starts, from a file imported with
`pandokos import-prices`, or, when the operator enables it, from a periodic download; operator
overrides take precedence and imports never touch them. A change creates a new immutable version
and an audit record. `pricing.Cost` computes the cost of a usage at a time with exact decimal
arithmetic and says when the cost is unknown; sprint 3.2 records it.

At the end of the sprint the three exit conditions of the epic hold: a model is visible to one
tenant and not to another (T-398, T-401); a cost computed for a past timestamp uses the version
valid then (T-395); an import that changes a price adds a version and an audit record (T-394,
T-410). The inference route still uses `skeleton.openai.*` (section 2).

## 2. Scope

### In scope

- Migration `0004_catalogue`: `provider`, `provider_endpoint`, `model`, `model_tenant_grant`,
  `tenant_model_restriction`, `virtual_key.models`, `price_import`, `price_version`,
  `price_rate`, catalogue notifications, worker grants for the importer, the foreign key from
  `provider_credential.provider`, seed rows for `openai` and `anthropic`.
- `internal/pricing` (decimal, price versions, cost), `internal/pricing/genaiprices` (dataset
  parser, matcher, extractor, embedded snapshot), `internal/catalog` (validation, access rule,
  gateway cache, database access for the gateway, importer, remote fetch, refresh job).
- Twenty-nine operations of the administration API, their CLI commands, five permissions, audit
  actions; `VirtualKeyCreate.models`; the host command `pandokos import-prices`.
- Generator: presence-aware optional members of update bodies and validation of nested request
  objects; `adminapi.EmptyDescription` and its hand-written marshalling are removed.
- Configuration keys of section 5.10; the principal cache and the `Opener` ignore catalogue
  payloads; `vault.Providers` is replaced by the catalogue.
- Isolation spec, fixtures, documentation (`docs/operations/catalogue.md`), ADR 0035 and 0036.

### Not in scope

| Item | Sprint |
|---|---|
| Calling the model check in the inference path, refusing with `model_not_allowed`, routing by provider name, taking the endpoint from the catalogue; removal of `skeleton.openai.*` | 3.1 |
| The egress guard (private-address refusal, dialled-address check), `HTTPS_PROXY` for provider traffic and for the price download | 3.1 |
| Validators of credentials for other providers and against catalogue endpoints | 3.1 |
| Recording cost and price version on the usage record; reading non-token units | 3.2 |
| The unified `/v1/…` endpoint choosing a provider from the model name (and its ambiguity rule) | 3.1 / 3.3 |
| The tenant capability endpoint ("can this tenant call this model now") on the inference side | 3.3 |
| Blocking platform-funded requests without a price, and its override (the second half of CAT-08) | 7.1 |
| Request-side cost estimation for limits; monetary budgets with unknown prices | 5.x |
| Tenant-supplied endpoints with allowed host patterns (CAT-03, S) | R4 |
| Aliases (CAT-07), declarative export and import of the catalogue (CAT-10) | R4, R2 |
| Tier 2 and tier 3 families (`gemini`, `azure-openai`, Bedrock) | epic 9 |
| Removing a tenant's grant rows when it is deleted | 6.3 |
| `serve --role`: the refresh job in its own worker process | 8.2 |
| Console pages for catalogue, model access and prices | 11.2, 11.3 |

## 3. Requirements claimed

Wording quoted from [docs/requirements](../../../../docs/requirements/README.md).

| ID | Requirement (quoted) | What this sprint delivers | Claim |
|---|---|---|---|
| CAT-01 | "The platform admin maintains a catalogue of providers. Provider, API family, endpoint, model and credential are distinct concepts. A provider can be enabled or disabled" | Tables `provider` (with `api_family` and `enabled`), `provider_endpoint`, `model`; credentials stay in the vault and now reference `provider.name`; fifteen platform operations | full |
| CAT-02 | "Provider support is tiered. Tier 1: OpenAI, Anthropic, and any endpoint that speaks the OpenAI API (…). Tier 2: Google Gemini, Azure OpenAI. Tier 3: Amazon Bedrock, Vertex AI, Cohere" | The data of tier 1: families `openai` and `anthropic`; seeded providers `openai` and `anthropic` with their public endpoints and seventeen models; any further OpenAI-compatible provider is a `createProvider` with family `openai` (private and self-hosted endpoints included) | tier 1 data (the traffic is 3.1 and 3.3) |
| CAT-03 | "Endpoints, including regional and private ones, are registered by the platform admin. A tenant can supply its own endpoint only for providers whose catalogue entry declares an allowed host pattern" | Endpoints with `region`, `private`, `enabled`, one default per provider; `http` only for private endpoints; a public endpoint cannot name a private IP literal | platform endpoints (M, R1); tenant endpoints R4 |
| CAT-04 | "The model catalogue records, when known: public name, upstream identifier, provider, API family, capabilities, status, context window and limits" | `model.name`, `upstream_id`, provider, family (of the provider), `capabilities` from a fixed vocabulary, `status` (`active`, `deprecated`, `retired`), `context_window`, `max_output_tokens` | full |
| CAT-05 | "The platform admin makes a model available to all tenants or to selected ones. A tenant admin can restrict it further for the tenant or for individual keys" | `model.availability` (`all`, `selected`) and `model_tenant_grant`; `tenant_model_restriction`; `virtual_key.models` | full |
| CAT-06 | "A request for a model that is not allowed is rejected before it is forwarded, whatever identifier the caller uses" | The decision: `catalog.Snapshot.Resolve` with one refusal for unknown, retired, not granted, outside the tenant restriction and outside the key's list (section 5.6); the gateway cache that serves it; tested on the database and in the cache | the decision and its data; enforcement in the inference path 3.1 |
| CAT-08 | "Prices are versioned in time, with a currency. The cost of a request uses the price in force when it was made. When no price is known the usage is recorded with an unknown cost, and platform-funded requests are blocked unless configured otherwise" | Immutable price versions with `valid_from` and `currency`; `PriceAt` and `pricing.VersionAt`; `pricing.Cost` with exact decimals and an explicit unknown result | versions, currency, cost at a time, unknown cost; recording 3.2; the platform-funded block 7.1 |
| CAT-09 | "The price list can be loaded and refreshed from an open dataset and overridden by the operator. Every change creates a new version and an audit record" | Importer of the `genai-prices` v2 format: embedded snapshot imported at start, `import-prices --file`, `runPriceImport`, optional periodic refresh; overrides; one platform audit row per new version and one per run that changed something | full |
| IAM-04 | "A virtual key always belongs to exactly one tenant and can be restricted by scope, model allow-list, expiry and limits" | The model allow-list: `VirtualKeyCreate.models`, immutable, on the principal | model allow-list (limits 5.1) |

Touched, not claimed: **AZ-07** (the catalogue part of the platform admin's duties), **AZ-02**
(read, write and price are distinct catalogue permissions), **AUD-01** (catalogue changes are
audited), **TEN-06** (the catalogue cache is tenant-keyed for access; the refresh job is a
platform job with an identity), **VLT-01** (credentials name catalogue providers).

## 4. Repository layout

```
internal/pricing/                    new, pure (standard library and internal/family only)
  decimal.go     Decimal: parse, format, add, multiply, round
  price.go       Category, Rate, Version, Validate, Equal
  cost.go        Cost, Breakdown
  at.go          VersionAt
internal/pricing/genaiprices/        new, pure
  parse.go       Dataset types, Parse
  match.go       match clauses
  extract.go     Extract: dataset model -> pricing.Version candidate and issues
  snapshot.go    //go:embed snapshot/data.json; Snapshot, SnapshotInfo
  snapshot/data.json, snapshot/LICENSE, snapshot/SOURCE
internal/catalog/                    new
  names.go       name, URL and reference validation, reserved names
  access.go      Snapshot, Model, Resolve, errors
  cache.go       Cache (gateway): TTL, Invalidate, Flush
  load.go        LoadSnapshot, LoadTenantAccess, PriceAt (database)
  importer.go    Import, Report, ImportAtStart
  fetch.go       Fetch (remote dataset, bounded)
  refresh.go     Refresher (background job)
internal/admin/{providers,endpoints,models,prices,model_access}.go   new
internal/store/migrations/sql/0004_catalogue.sql
internal/store/queries/sql/catalog.sql, prices.sql (new); virtual_keys.sql (changed)
cmd/pandokos/importprices.go         new: `import-prices`
docs/operations/catalogue.md         new
```

Import rules (T-R13 extended, T-R23): `internal/pricing` and `internal/pricing/genaiprices`
import the standard library, `internal/family` and each other only — no `store`, no `net`,
`net/http`, `os` (the snapshot is `embed`), and no `float32` or `float64` outside tests.
`internal/catalog` may import `authz`, `audit`, `pricing`, `genaiprices`, `store`,
`store/queries`, `tenant`, `telemetry`, `buildinfo` and the standard library; it must not reach
`admin`, `adminapi`, `gateway`, `server`, `cli`, `config`, `vault`. `internal/admin` imports
`catalog` and `pricing`. `internal/gateway` still cannot reach `store`, hence not `catalog`:
3.1 defines the interface through which it receives the cache (as for the `Opener`).

## 5. Architecture

```mermaid
flowchart LR
    snap[embedded snapshot] --> imp[catalog.Import]
    file[import-prices --file] --> imp
    remote[refresh: HTTPS GET] --> imp
    imp -- admin or worker role, platform scope --> db[(PostgreSQL)]
    h[admin handlers] -- admin role --> db
    db -- NOTIFY catalog / catalog:tenant --> cache[catalog.Cache in the gateway]
    cache -- gateway role --> db
    cache --> resolve[Resolve: may this principal use provider/model]
    resolve -. 3.1 .-> gw[inference route]
    prices[pricing.Cost] -. 3.2 .-> gw
```

### 5.1 Schema

Full SQL in plan.md appendix A, applied while designing to PostgreSQL 16 after 0001 to 0003 and
exercised for every role and session (section 12).

| Table | Scope | Columns of note | Grants |
|---|---|---|---|
| `provider` | global, restricted | `name` (unique, immutable, not `healthz`, `readyz`, `v1`), `display_name`, `description`, `api_family` (`openai`, `anthropic`; immutable), `enabled`, `dataset_provider` (identifier in the price dataset; empty: not imported) | all three roles `SELECT`; admin `INSERT`, `UPDATE` (not name, family), `DELETE` |
| `provider_endpoint` | global, restricted | `provider_id`, `name` (unique per provider, immutable), `base_url`, `region`, `private`, `enabled`, `is_default` (at most one per provider) | gateway, admin `SELECT`; admin write |
| `model` | global, restricted | `provider_id`, `name` (unique per provider, immutable), `upstream_id` (unique per provider), `display_name`, `description`, `status`, `availability`, `capabilities`, `context_window`, `max_output_tokens`, `price_override` | all three `SELECT`; admin write |
| `model_tenant_grant` | global, restricted (describes tenants) | `model_id`, `tenant_id`; cascades with the model | gateway, admin `SELECT` (own tenant's rows in a tenant session; all in platform scope, admin only); admin `INSERT`, `DELETE` in platform scope |
| `tenant_model_restriction` | tenant, canonical policy | `models text[]` (references), one row per tenant at most | gateway `SELECT`; admin `SELECT`, `INSERT`, `UPDATE (models)`, `DELETE` |
| `virtual_key` | tenant | new `models text[]` (`NULL`: no restriction; immutable) | admin `INSERT (models)` added |
| `price_import` | global, restricted, append-only | source, digest, outcome, counts, `report` (≤ 64 KiB) | admin, worker `SELECT`, `INSERT` in platform scope |
| `price_version` | global, restricted | `model_id`, `valid_from`, `currency`, `source` (`import`, `override`), `import_id`, `note`; unique `(model_id, valid_from)` | all three `SELECT`; admin, worker `INSERT` in platform scope; admin `DELETE` (future versions only) |
| `price_rate` | global, restricted | `price_version_id`, `category`, `threshold`, `rate numeric(24,12)` | all three `SELECT`; admin, worker `INSERT` in platform scope; nobody updates; removed only with its version |
| `platform_audit_event` | (existing) | — | worker `INSERT` and a policy for it in platform scope (the refresh job audits) |
| `provider_credential` | (existing) | foreign key `provider_credential_provider_fkey (provider) REFERENCES provider (name)` | unchanged |

- **Restricted global tables** (D-1): forced row-level security; a `SELECT` policy `USING (true)`
  for the runtime roles that read the table, and `INSERT`, `UPDATE`, `DELETE` policies for the
  admin role (and the worker where listed) that require `platform_scope()`. An admin transaction
  in a tenant's session or with no scope cannot change the catalogue, whatever a handler does.
- **Guards** (triggers): provider name and family, endpoint provider and name, model provider and
  name are immutable; `global_row_guard` keeps `created_at` and `updated_at`. A price version
  cannot start before `now()`, never changes, and can be deleted only while `valid_from > now()`;
  its rates go with it (`ON DELETE CASCADE`) and never change; `price_import` is append-only
  (lint L-17). `virtual_key.models` cannot change after insert.
- **Notifications**: `notify_catalog()` sends `catalog` (statement triggers on `provider`,
  `provider_endpoint`, `model`, `price_version`) and `catalog:<tenant>` (row triggers on
  `model_tenant_grant`, `tenant_model_restriction`).
- **Seed** (D-19), inserted before row-level security is enabled: providers `openai` (family
  `openai`, dataset `openai`) and `anthropic`; endpoints `default` at `https://api.openai.com`
  and `https://api.anthropic.com`; models, all `availability = 'all'`, `upstream_id = name`,
  context windows as the dataset states: `gpt-4o`, `gpt-4o-mini`, `gpt-4.1`, `gpt-4.1-mini`,
  `gpt-4.1-nano`, `gpt-5`, `gpt-5-mini`, `gpt-5-nano`, `o3`, `o4-mini`,
  `text-embedding-3-small`, `text-embedding-3-large`, `claude-haiku-4-5`, `claude-sonnet-4-5`,
  `claude-opus-4-5`, `claude-sonnet-4-6`, `claude-opus-4-6`. No price is seeded by the migration:
  the embedded snapshot prices them at the first start.
- **Lint** (`test/isolation/spec.go`): `Global` gains the seven global tables, all `Restricted`,
  with reasons; `Fixtures` gains `tenant_model_restriction`; `AppendOnly` gains `price_import`;
  `Grants` pins every matrix above (plan.md B2). No new lint rule.

### 5.2 Names, endpoints, references

- **Provider name**: `^[a-z][a-z0-9-]{0,62}$` (the format credentials already use), not one of
  `healthz`, `readyz` (1.1 D-11) and `v1` (ADR 0009's unified endpoint takes `/v1/…`). The list
  lives in `catalog.ReservedProviderNames` and in the check constraint; T-379 and T-391 keep
  them equal.
- **Endpoint `base_url`**: absolute URL, scheme `https`, or `http` only when `private` is true;
  host required; no user information, query or fragment; no trailing slash; at most 2048
  characters. The native path of a request is appended to it (`https://api.openai.com` +
  `/v1/chat/completions`). When `private` is false and the host is an IP literal in a loopback,
  private, link-local, unique-local or unspecified range (`netip.Addr` predicates), the request
  is refused (`invalid_request` on `base_url`, "a private address needs private: true"). Host
  names are not resolved at write time: the egress guard of 3.1 checks the dialled address.
- **Model name** (what callers send): `^[A-Za-z0-9][A-Za-z0-9._:@/+-]{0,127}$` — slashes are
  allowed, because OpenAI-compatible hosts use names such as `meta-llama/Llama-3-70b`.
  **Upstream identifier**: printable ASCII without space, 1 to 256 characters; defaults to the
  name.
- **Model reference** in tenant restrictions, key allow-lists and the CLI:
  `<provider>/<model name>`, split at the first `/` (a provider name has none).
- **Capabilities**: subset of `text`, `vision`, `audio`, `tools`, `reasoning`, `embeddings`,
  `structured_output`, `streaming`; informational in v0.1.

### 5.3 Model access (CAT-05, CAT-06)

```go
package catalog

type Provider struct{ ID, Name, Family, DatasetProvider string; Enabled bool }
type Endpoint struct{ Name, BaseURL, Region string; Private, Enabled, Default bool }
type Model struct {
    ID, Provider, Name, UpstreamID, Status, Availability string
    ContextWindow, MaxOutputTokens int64 // 0: unknown
}
// Snapshot is the global catalogue at one moment: providers, endpoints, models, price versions.
type Snapshot struct{ /* unexported maps; immutable once built */ }
// TenantAccess is what one tenant was granted and restricted itself to.
type TenantAccess struct {
    Granted    map[string]bool // model IDs granted to the tenant
    Restricted bool            // the tenant has a restriction row
    Allowed    map[string]bool // references of the restriction
}
var (
    ErrProviderUnavailable = errors.New("catalog: provider unknown, disabled or without an enabled default endpoint")
    ErrModelNotAllowed     = errors.New("catalog: model not allowed")
)
// Resolve returns the model and the default endpoint that p may use for provider/name.
func (s *Snapshot) Resolve(p authz.Principal, ta TenantAccess, provider, name string) (Model, Endpoint, error)
func (s *Snapshot) Visible(ta TenantAccess, m Model) bool         // ignores the restriction and the key
func (s *Snapshot) Versions(modelID string) []pricing.Version    // sorted by ValidFrom
func ParseRef(ref string) (provider, name string, err error)
```

The rule, applied in this order (D-6):

1. The provider exists, is enabled and has an enabled default endpoint; else
   `ErrProviderUnavailable`.
2. A model of that provider has exactly that public name (case-sensitive; the upstream
   identifier is never accepted in its place); its status is not `retired`; its availability is
   `all` or the tenant holds a grant; the tenant has no restriction or the restriction names
   `provider/name`; the key has no list (`p.Models == nil`) or the list names `provider/name`.
   Any failure is `ErrModelNotAllowed`, the same error for each, so a caller cannot learn which
   models other tenants were granted.

`authz.Principal` gains `Models []string` (nil: no restriction), filled by `authn` from
`GetKeyPrincipal`. The principal cache already drops a tenant on `tenant:` payloads, and the list
of a key cannot change, so no new invalidation is needed for it.

Platform principals have no tenant and never pass `Resolve` (they cannot call models, ADR 0031).

### 5.4 Prices and costs (CAT-08)

**Decimal** (D-9). `pricing.Decimal` is an exact decimal built on `math/big` (an unscaled
`*big.Int` and a scale); there is no floating point anywhere in the package (T-R23).

```go
func ParseDecimal(s string) (Decimal, error) // -?digits(.digits)?; no exponent, sign "+", NaN, Inf, spaces; ≤ 30 fraction digits, ≤ 60 digits
func (d Decimal) String() string             // canonical: no exponent, no trailing zeros, "0"
func (d Decimal) Add(e Decimal) Decimal
func (d Decimal) MulInt(n int64) Decimal
func (d Decimal) DivPow10(n int) Decimal     // exact
func (d Decimal) Round(places int) Decimal   // half away from zero; for display only
func (d Decimal) Cmp(e Decimal) int
func (d Decimal) Sign() int
func (d Decimal) MarshalText() ([]byte, error); func (d *Decimal) UnmarshalText([]byte) error
```

A **rate** is a price per one million units of a category: `numeric(24,12)` in the database
(at most 12 fraction digits, 0 to 1 000 000), decimal text in SQL parameters, results and JSON
(the API never carries a rate as a JSON number). A cost is exact: tokens × rate / 10⁶ has at
most 18 fraction digits; nothing is rounded except by `Round` for display (3.2 stores
`numeric(38,18)`).

```go
type Category string // input, output, cache_read, cache_write, cache_write_1h, input_audio, output_audio
type Rate struct {
    Category  Category
    Threshold int64   // start of a tier, in input tokens of the request; 0 for the base rate
    PerMillion Decimal
}
type Version struct {
    ID, ModelID string
    ValidFrom   time.Time
    Currency    string // ISO 4217 letters
    Source      string // import, override
    Rates       []Rate // sorted by category, then threshold
}
func (v Version) Validate() error // currency; ≥1 rate; every category has a threshold-0 rate; thresholds unique and ≤ 1e8; 0 ≤ rate ≤ 1e6; ≤ 12 fraction digits
func (v Version) SameRates(w Version) bool
func VersionAt(versions []Version, t time.Time) (Version, bool) // greatest ValidFrom <= t

type Cost struct {
    Known     bool
    Reason    string        // when !Known: "no_price", "category_unpriced", "inconsistent_usage"
    Amount    Decimal       // in Currency; zero when !Known
    Currency  string
    VersionID string        // "" when no version
    Lines     []CostLine    // the breakdown 3.2 may store
    Fallbacks []Category    // categories priced at another category's rate
}
type CostLine struct {
    Category Category
    Units    int64
    Rate     Decimal // per million, after tier and fallback
    Amount   Decimal
}
func CostOf(u family.Usage, v Version, found bool) Cost
```

**Formula** (D-11), from `family.Usage` (ADR 0010: input includes cache reads and writes, output
includes reasoning):

| Category | Units |
|---|---|
| `cache_read` | `CacheReadTokens` |
| `cache_write_1h` | `Extra["cache_write_1h_tokens"]` |
| `cache_write` | `CacheWriteTokens − cache_write_1h` |
| `input_audio` | `Extra["input_audio_tokens"]` |
| `input` | `InputTokens − CacheReadTokens − CacheWriteTokens − input_audio` |
| `output_audio` | `Extra["output_audio_tokens"]` |
| `output` | `OutputTokens − output_audio` (reasoning included) |

A negative unit count is `inconsistent_usage`. The **tier** of every category is chosen with the
request's total `InputTokens`: the rate with the greatest `Threshold ≤ InputTokens` applies to
every unit of that category (the "cliff" of the dataset and of providers' long-context prices).
A category with units and no rate is priced at its **fallback** — `cache_read`, `cache_write`,
`input_audio` at `input`; `cache_write_1h` at `cache_write`, then `input`; `output_audio` at
`output` — and named in `Fallbacks`; with no rate after the fallback the cost is unknown
(`category_unpriced`). Zero units cost nothing whether priced or not. No version in force:
`no_price`.

**Lookup**: `catalog.PriceAt(ctx, tx, modelID, at)` (the database, statement `PriceAt`) and
`Snapshot.Versions` + `pricing.VersionAt` (the cache) return the same version for the same
instant (T-395).

**Versions** (D-8): immutable; `valid_from` defaults to the transaction time and cannot be in
the past; a version that has not started may be deleted; the database refuses everything else.
"In force at t" is the version with the greatest `valid_from ≤ t`; two versions of a model never
share an instant.

**Currency** (D-10): per version, three capital letters; imports write `USD` (the dataset's
currency); an override may name any. Nothing converts or adds amounts of different currencies;
a cost carries its currency.

### 5.5 Overrides (CAT-09)

- `createPriceOverride` inserts a version with `source = 'override'` (`valid_from` absent: now;
  present: now or later) and sets `model.price_override = true` in the same transaction, after
  `LockModel`.
- While `price_override` is true, imports do not create versions for the model and report it as
  `overridden`.
- `clearPriceOverride` sets it back to false; refused with `invalid_state` while a version of the
  model has not started. The override version stays in force until the next import creates a
  version; the response and the guide say so.
- `deletePriceVersion` removes a version that has not started (`invalid_state` otherwise, mapped
  from the trigger's `restrict_violation`). Deleting the last future override does not clear the
  flag.

### 5.6 The price dataset and the importer

**Source** (verified, section 12): `genai-prices` by Pydantic, MIT licence. Its README marks the
v1 files (`prices/data.json`) as frozen and the v2 files as current:
`prices/new_data/v2/data.json` with the JSON schema `data.schema.json`. The importer reads the
**v2** format (`dataset_format = 'genai-prices-v2'`): a JSON array of providers, each with `id`,
`models[]`; a model has `id`, `match` (clauses `equals`, `starts_with`, `ends_with`,
`contains`, `regex`, `or`, `and`), `prices` (a `ModelPrice`, or a list of `ConditionalPrice`
with an optional `constraint` `{start_date}` or `{start_time, end_time}`), optional
`price_variants` (`{when, prices, constraint}`), `context_window`, `deprecated`. A `ModelPrice`
maps a category name to a number or to `{base, tiers: [{start, price}]}`. Prices are USD per
million units.

**Embedded snapshot**: `internal/pricing/genaiprices/snapshot/data.json`, the v2 `data.json` at a
pinned upstream commit, byte for byte, with the upstream `LICENSE` beside it and a `SOURCE` file
(URL, commit, retrieval date, SHA-256). The pinned commit is the one observed while designing;
plan.md step A6 gives the commands that fetch and check it.

```go
package genaiprices

func Parse(b []byte) (*Dataset, error)                    // json.Decoder.UseNumber; ≤ 16 MiB; unknown members ignored; wrong types of used members are errors
func (d *Dataset) Find(datasetProvider, upstreamID string) (*Model, bool) // first model whose match accepts upstreamID
func Extract(m *Model, today time.Time /* UTC day */) (pricing.Version /* no ID, model, valid_from */, []Issue, error)
type Issue struct{ Reason, Detail string } // detail: identifiers only (a category name, a variant key)
func Snapshot() []byte
func SnapshotInfo() (commit string, retrieved time.Time, sha256 string)
```

**Matching** (D-14). The importer never creates models. For every model of every provider whose
`dataset_provider` is not empty, it looks for the first model of that dataset provider whose
`match` accepts the catalogue model's `upstream_id` (exact, case-sensitive string tests;
`regex` with Go's RE2 syntax, `regexp.MatchString` semantics; a pattern RE2 rejects matches
nothing and adds the issue `regex_unsupported`). No match: `unmatched`.

**Extraction** at import time with `today` in UTC:

| Dataset construct | Importer |
|---|---|
| `prices` object | the candidate |
| list of conditional prices | the last entry whose constraint is absent or a `start_date ≤ today` (the dataset's own rule) |
| entry with `start_date > today` | ignored; issue `scheduled_price_not_imported` (a later import picks it up) |
| entry with `start_time`/`end_time` | ignored; issue `time_of_day_price_ignored` |
| `price_variants` | ignored; issue `price_variants_ignored`, detail the `when` keys (`service_tier`, `processing`, …) |
| `input_mtok`, `output_mtok`, `cache_read_mtok`, `cache_write_mtok`, `cache_write_1h_mtok`, `input_audio_mtok`, `output_audio_mtok` | rates of `input`, `output`, `cache_read`, `cache_write`, `cache_write_1h`, `input_audio`, `output_audio` |
| `{base, tiers}` | threshold 0 at `base`, one rate per tier at `start` |
| any other category (`web_searches_kcount`, `*_image_*`, `cache_audio_read_mtok`, `requests_kcount`, …) | ignored; issue `category_ignored`, detail the name |
| negative number, more than 12 fraction digits, above 1 000 000, a tier start above 10⁸ | the model is skipped; issue `value_not_representable` |
| no supported category left | skipped; issue `no_supported_price` |

A model with issues but a valid candidate is imported; the issues go to the report. Numbers are
read from their JSON text: `0.195` is exactly 0.195.

**Run** (`catalog.Import`):

```go
type Source struct {
    Kind string // embedded, file, remote
    Ref  string // file path or URL as given by the operator; "" for embedded
    Data []byte
}
type ImportOptions struct {
    Actor  audit.Actor // host for the CLI, start and refresh; the platform principal for the API
    DryRun bool
    Now    func() time.Time
    RequestID string
}
type Report struct {
    ImportID string; Digest string
    ModelsSeen, Created, Unchanged, Overridden, Unmatched, Skipped int
    Items []ReportItem // provider, model, dataset_model, outcome, issues
}
func Import(ctx context.Context, db *store.DB, src Source, o ImportOptions) (Report, error)
```

1. Parse (failure: `ErrDatasetInvalid`, recorded as a failed run with `error_class`
   `dataset_invalid`; no version).
2. One platform transaction: `LockPriceImports` (an advisory transaction lock, so concurrent
   imports from several instances run one after the other), read providers and models, for each
   candidate compare with the version in force now (`PriceAt`, `SameRates` and currency):
   override → `overridden`; equal → `unchanged`; else `CreatePriceVersion` (source `import`,
   `valid_from` = transaction time) and `CreatePriceRates`, plus one audit row `price.create`.
3. `CreatePriceImport` with the counts and the report (items with an issue or a non-unchanged
   outcome; truncated to 64 KiB with a `truncated` count), and one audit row `price_import.run`
   when at least one version was created or the run came from the API or the CLI. The run row is
   written in the same transaction as the versions; a dry run writes nothing and returns the
   report.

**At start** (`catalog.ImportAtStart`, D-15), when `pricing.import_at_start` is true: after the
database checks, with the worker role, `serve` imports the embedded snapshot **unless** a
successful import with the same digest exists, or a successful `file` or `remote` import was
recorded after the snapshot's retrieval date (a newer dataset must not be replaced by an older
release snapshot). A failure is a warning, never a start failure. Several instances starting
together serialize on the lock and the second one skips.

**Remote refresh** (D-16), when `pricing.refresh.enabled` is true: a goroutine of `serve` on the
worker pool, first run one minute after start, then every `pricing.refresh.interval`; it skips a
run when the last successful remote import is younger than half the interval (another instance
did it). `catalog.Fetch`:

- `GET` the configured `https` URL (an `http` URL is a configuration error); headers `Accept:
  application/json` and `User-Agent: pandokos/<version>`, nothing else;
- its own `http.Transport`: `Proxy: nil` (no `HTTPS_PROXY` until 3.1 decides it), system roots,
  TLS 1.2 or later, no redirect followed (a 3xx is a failure), no connection reuse;
- 60 s in total; status 200 only; at most 16 MiB read (more is `too_large`);
- failures are classes (`unreachable`, `timeout`, `status`, `redirect`, `too_large`,
  `dataset_invalid`): logged, recorded as a failed run, never retried before the next interval.
  No byte of the response is logged.

The destination is operator configuration only; no API operation takes a URL.

### 5.7 Gateway cache

```go
type CacheOptions struct {
    DB     *store.DB      // the gateway pool
    Logger *slog.Logger
    TTL    time.Duration  // cache.catalog_ttl
    Now    func() time.Time
}
func NewCache(o CacheOptions) (*Cache, error)
// Snapshot returns the global catalogue, loading it when absent, stale or older than TTL.
func (c *Cache) Snapshot(ctx context.Context) (*Snapshot, error)
// Access returns the tenant's grants and restriction (one tenant transaction on a miss).
func (c *Cache) Access(ctx context.Context, id tenant.ID) (TenantAccess, error)
// Resolve is Snapshot, Access and Snapshot.Resolve.
func (c *Cache) Resolve(ctx context.Context, p authz.Principal, provider, name string) (Model, Endpoint, error)
func (c *Cache) Invalidate(payload string) // listener OnNotify
func (c *Cache) Flush()                    // listener OnReset
```

- One snapshot (providers, endpoints, models, every price version with its rates) and a map of
  tenant access entries, at most 10 000, emptied when full. Same construction as the principal
  cache: an entry lives for the lifetime from the start of its load; failures are not cached.
- Loading the snapshot is one transaction of the gateway role with no scope (`Unscoped`): the
  catalogue tables are readable in any session. Loading access is one tenant transaction.
- Payloads: `catalog` marks the snapshot stale; `catalog:<tenant>` and `tenant:<tenant>` drop that
  tenant's entry; `platform`, `probe:…`, `vault:…` nothing; anything else flushes.
- The principal cache (`authn`) and the `Opener` ignore `catalog` and `catalog:…` (D-18).
- **Bound**: a catalogue or price change reaches `Resolve` and the cost computation within
  `cache.catalog_ttl` (default 30 s, 1 s to 5 m), normally under a second. A price change
  computed by a stale snapshot uses the previous version; 3.2 records the version ID, so the
  record stays self-consistent.

`serve` builds the cache on the gateway pool and adds it to the listener's `OnNotify` and
`OnReset`; nothing calls `Resolve` in the running binary until 3.1.

### 5.8 Administration API

Full additions in plan.md appendix B. 29 operations (64 in total). Provider and endpoint path
parameters are names; models, price versions and imports are IDs.

| Operation | Method and path | Permission | Notes |
|---|---|---|---|
| `listProviders`, `createProvider` | `GET`, `POST /v1/providers` | `catalog:read`, `catalog:write` | ordered by name |
| `getProvider`, `updateProvider`, `deleteProvider` | `GET`, `PATCH`, `DELETE /v1/providers/{provider}` | `catalog:read`, `catalog:write`, `catalog:write` | delete: `invalid_state` while an endpoint, model or credential references it |
| `listEndpoints`, `createEndpoint` | `GET`, `POST /v1/providers/{provider}/endpoints` | read, write | the first endpoint becomes the default |
| `getEndpoint`, `updateEndpoint`, `deleteEndpoint` | `…/endpoints/{endpoint}` | read, write, write | `is_default: true` moves the default in one transaction; deleting the default: `invalid_state` while other endpoints exist |
| `listModels`, `createModel` | `GET`, `POST /v1/models` | read, write | filters `provider`, `name`, `status`, `availability`; `availability` defaults to `selected` |
| `getModel`, `updateModel`, `deleteModel` | `/v1/models/{model}` | read, write, write | delete: `invalid_state` once a price version exists (retire instead) |
| `listModelGrants` | `GET /v1/models/{model}/grants` | read | |
| `grantModel`, `revokeModelGrant` | `PUT`, `DELETE /v1/models/{model}/grants/{tenant}` | write | idempotent; unknown tenant `not_found`; response is the grant (revoke: as it was, or `not_found`) |
| `listPriceVersions`, `createPriceOverride` | `GET`, `POST /v1/models/{model}/prices` | `catalog:read`, `catalog:price` | list: query `at` (RFC 3339) returns the version in force then, at most one item |
| `deletePriceVersion` | `DELETE /v1/models/{model}/prices/{price_version}` | `catalog:price` | only before it starts |
| `clearPriceOverride` | `POST /v1/models/{model}/clear-price-override` | `catalog:price` | |
| `listPriceImports`, `getPriceImport` | `GET /v1/price-imports`, `…/{price_import}` | `catalog:read` | get includes the report |
| `runPriceImport` | `POST /v1/price-imports` | `catalog:price` | body `{source: embedded \| remote, dry_run}`; `remote` while refresh is disabled → `invalid_request` on `source`; synchronous, 201 with the import |
| `listTenantModels` | `GET /v1/tenants/{tenant}/models` | `model:read` | models visible to the tenant, each with `allowed` (passes the restriction) and the price in force now |
| `getModelRestriction`, `setModelRestriction`, `clearModelRestriction` | `GET`, `PUT`, `DELETE /v1/tenants/{tenant}/model-restriction` | `model:read`, `model:restrict`, `model:restrict` | set: every reference must name a model visible to the tenant |

`VirtualKey` gains `models`; `VirtualKeyCreate` gains `models` (1 to 64 unique references; each
must name a model visible to the tenant when the key is created). A reference that is unknown and
one that is not visible give the same `invalid_request` ("must name models available to the
tenant"), so a tenant cannot probe for other tenants' models.

**Rules of the handlers.**

- Catalogue writes run in `db.Platform`; tenant operations in `db.WithTenant`; `grantModel`
  checks the tenant with `GetTenant` in the same platform transaction.
- Unique violations: `provider_name_key`, `provider_endpoint_name_key`, `model_name_key`,
  `model_upstream_id_key`, `price_version_valid_from_key` → `conflict`. Foreign-key violations
  on delete → `invalid_state` ("still referenced"). Check violations of the reserved names →
  `invalid_request` on `name` (the handler checks first; the constraint is the backstop).
- `createPriceOverride`: rates are `[{category, threshold, rate}]` with `rate` a decimal string
  (`^(0|[1-9][0-9]{0,6})(\.[0-9]{1,12})?$`); `pricing.Version.Validate` must pass; `valid_from`
  in the past → `invalid_request`.
- `updateModel` with `availability: selected` keeps existing grants; `all` keeps them too (they
  matter again when it returns to `selected`).
- The credential handlers (`createCredential`) check the provider with `GetProvider` instead of
  `vault.KnownProvider`; a foreign-key violation on `provider_credential_provider_fkey` maps to the
  same `invalid_request` on `provider`. A disabled provider still accepts credentials.

**Problem codes added** (ADR 0020, administration side):

| `code` | Status | Title | Detail (fixed) | Raised when |
|---|---|---|---|---|
| `price_source_unavailable` | 502 | Price source unavailable | The price dataset could not be downloaded. Nothing was imported. | `runPriceImport` with `remote` and a fetch failure |
| `price_dataset_invalid` | 422 | Price dataset invalid | The price dataset could not be read. Nothing was imported. | parsing failed |

### 5.9 Permissions and roles

| Permission | Scope | Allows |
|---|---|---|
| `catalog:read` | platform | Reading providers, endpoints, models, model grants, price versions and price imports |
| `catalog:write` | platform | Creating, changing and deleting providers, endpoints and models; granting models to tenants and revoking grants |
| `catalog:price` | platform | Setting and removing price overrides; running a price import |
| `model:read` | tenant | Listing the models available to the tenant, with their prices; reading the tenant's model restriction |
| `model:restrict` | tenant | Setting and clearing the tenant's model restriction |

Appended in this order (25 permissions). `platform-admin` gains the three platform ones;
`tenant-admin` gains the two tenant ones; `inference` is unchanged. Issuing a key with `models`
needs only `virtual_key:write`.

### 5.10 Configuration, commands, CLI

| Key | Type | Default | Rule |
|---|---|---|---|
| `cache.catalog_ttl` | duration | `30s` | 1 s to 5 m |
| `pricing.import_at_start` | boolean | `true` | |
| `pricing.refresh.enabled` | boolean | `false` | |
| `pricing.refresh.url` | URL | `https://raw.githubusercontent.com/pydantic/genai-prices/main/prices/new_data/v2/data.json` | `https` only; no user information |
| `pricing.refresh.interval` | duration | `24h` | 1 h to 720 h |

**Host command** `pandokos import-prices [--config FILE] (--file FILE | --embedded) [--dry-run]`:
loads the configuration for `PurposeInit` (the admin database user only), runs `catalog.Import`
with actor `host` and name `import-prices`, prints the counts and one line per report item, and
exits 0 on success (issues included), 1 on a failed import, 2 on wrong usage. The file is read
with a 16 MiB limit.

**API commands** (each only calls the generated client): `provider list|create|get|update|
delete`, `endpoint list|create|get|update|delete PROVIDER …`, `model list|create|get|update|
delete|grants|grant|revoke-grant`, `price list|override|delete|clear-override MODEL`,
`price-import list|get|run`, `tenant-model list`, `model-restriction get|set|clear`. `MODEL` is
an ID or a reference `provider/name` (resolved with `listModels?provider=&name=`). `key create`
gains a repeatable `--model REF`.

### 5.11 Audit actions and log lines

Platform log (`audit.RecordPlatform`; actor `host` with name `import-prices`, `price-refresh` or
`price-import-at-start` for jobs):

| Action | Resource | Detail |
|---|---|---|
| `provider.create`, `provider.update`, `provider.delete` | `provider`, ID = name | fields; changed members as `{from, to}` |
| `endpoint.create`, `endpoint.update`, `endpoint.delete` | `endpoint`, ID = `provider/name` | fields; `{from, to}` |
| `model.create`, `model.update`, `model.delete` | `model` | `provider`, `name`; `{from, to}` |
| `model_grant.create`, `model_grant.delete` | `model` | `provider`, `name`, `tenant_id`, `tenant_name` (only when the grant changed) |
| `price.create` | `price_version` | `provider`, `model`, `source`, `currency`, `valid_from`, `import_id`, `rates` (category, threshold, rate text; the previous version's ID) |
| `price.delete`, `price.clear_override` | `price_version`, `model` | `provider`, `model`, `valid_from` |
| `price_import.run` | `price_import` | `source`, `source_ref` (file base name or URL host), `digest`, counts, `dry_run` |

Tenant log: `model_restriction.set` (`models`, `previous`), `model_restriction.clear`;
`virtual_key.create` gains `models` when set.

| Message | Level | Fields |
|---|---|---|
| `prices imported` | info | `source`, `import_id`, `digest`, `created`, `unchanged`, `overridden`, `unmatched`, `skipped`, `duration_ms` |
| `price import skipped` | info | `source`, `reason` (`digest_seen`, `newer_import`, `recent_remote`) |
| `price import failed` | warn | `source`, `error_class`, `duration_ms` |
| `catalog cache flushed` | debug | `reason` (`full`, `reset`, `unknown_payload`) |

### 5.12 Generator: update bodies and nested objects (D-20)

1.3 decided that a construct the generator lacks is added with a test in the sprint that needs
it; 2.1 left `adminapi.EmptyDescription` as a stop-gap. This sprint has six update bodies with
optional members that must be clearable (`description: ""`, `context_window: 0`,
`capabilities` replaced, `enabled: false`), and arrays of request objects (rates).

- **Update schema**: a component object used as the request body of a `PATCH` operation. It
  may not be used anywhere else (the generator refuses). Each of its properties that is not
  required and not nullable is generated as `Optional[T]` (`T` = `string`, `int`, `bool`, an enum
  type or `[]string`) with the tag `omitzero`:

  ```go
  // Optional is a member that is absent or present with a value; null is refused.
  type Optional[T any] struct{ Set bool; Value T }
  func Some[T any](v T) Optional[T]
  func (o Optional[T]) IsZero() bool
  func (o Optional[T]) MarshalJSON() ([]byte, error)
  func (o *Optional[T]) UnmarshalJSON(b []byte) error // "null" is an error: "must not be null"
  ```

  Validation runs only when `Set`. Nullable members stay `Nullable[T]`.
- **Nested request objects**: `Validate` of an object calls `Validate` of each member object and
  of each item of an array of objects, and prefixes the field names (`rates[2].rate`).
- **Retired**: `adminapi.EmptyDescription`, `CredentialUpdate.MarshalJSON`, the `peekUpdate` /
  `descriptionGiven` state of `internal/admin`. `TenantUpdate`, `ServiceAccountUpdate` and
  `CredentialUpdate` become update schemas; their handlers test `.Set`; the CLI sends
  `adminapi.Some("")` to clear a description.

## 6. Isolation harness

- `DefaultSpec` gains the seven global tables, the grants of section 5.1, the fixture of
  `tenant_model_restriction`, `price_import` in `AppendOnly`. T-143 runs its statements for the
  new tenant table, every role, both sessions, directly and through the pooler.
- T-391 covers what the lint cannot: the policies of restricted global tables per role and
  session (verified matrix in section 12).
- T-270 to T-274 cover the new operations: `apitest.CallFor`, `Params.Model`, `Params.Provider`,
  `Params.PriceVersion`, `Params.PriceImport`; `platformAndSelf` lists the 25 platform
  operations as `platform`; the four tenant operations are cross-tenant tested; the body-member
  check of T-270 extends to `models` of `createVirtualKey` and `setModelRestriction` (a model
  granted only to B named by A gives the same 400 as an unknown one).

## 7. Test infrastructure and CI

- `test/fixtures/prices/`: small synthetic v2 documents (`base.json`, `changed.json`,
  `shapes.json` with every construct of section 5.6, `invalid.json`) for providers `synthetic` and
  `openai`; model identifiers prefixed `synthetic-` where free; no real prices are asserted
  except in T-377, which reads the embedded snapshot.
- `testutil.PriceServer(tb, body, behaviour)` serves a document over TLS with the test CA
  (`test/testcert`) and the behaviours `ok`, `404`, `redirect`, `stall`, `huge`; e2e and the
  refresh test point `SSL_CERT_FILE` at the test CA so the product code uses system roots.
- `apitest.API.Catalog(tb)` creates a provider `synthetic-…` with an endpoint and models for a
  test; `apitest.New` passes `admin.Options.Snapshot` the embedded snapshot.
- Tests hold for root (local) and non-root (CI) users: no test relies on file permissions or on
  a file being unreadable; files are created in `t.TempDir()` with mode 0600 and read by the same
  user (lesson of 2.1).
- CI needs no new job or service; the price download is never exercised against the Internet in
  CI (T-411 uses the local server).

## 8. Decisions

| # | Decision | Reason |
|---|---|---|
| D-1 | **Catalogue tables are global and restricted: forced row-level security, read by the runtime roles in any session, written only by the admin role (and the worker for prices) in platform scope** | 1.2 §11 foresaw them "global and open". The data model's role table says the admin role writes platform tables "when acting as platform": a policy makes it a property of the database, as for `tenant`. Reads stay open to all three roles in any session, which is what the gateway needs (it has no platform scope) |
| D-2 | **Grants of models to tenants are a global table with tenant-describing policies**, not a tenant-scoped table | The platform decides them and must list "which tenants may use model X"; a canonical tenant policy would hide that from platform scope (2.1 D-7 accepted that loss for secrets, not for configuration). A tenant's session sees its own rows, so the gateway reads them in the tenant session. Data model rule 7 already covers global tables that describe tenants |
| D-3 | **Provider name unique and immutable, reserved `healthz`, `readyz`, `v1`; family immutable and one of the families in code** | The name is the first path segment (ADR 0009) and the target of the credential foreign key (2.1 §11); `v1` is the unified endpoint's prefix. A family needs code, so the check constraint and `family` change together |
| D-4 | **Endpoints: one default per provider, used by the gateway; `private` declared by the platform admin; `http` only for private; a public endpoint cannot name a private IP literal** | Deterministic choice without a routing policy. "Private endpoints marked explicitly" becomes a flag the egress guard of 3.1 can trust, and the obvious mistake is refused at write; names are checked at dial time |
| D-5 | **A model has a public name and an upstream identifier, both unique per provider; the name is immutable; references are `provider/name`; IDs in API paths** | Names are what callers, restrictions and allow-lists use; IDs in paths because model names contain slashes. Uniqueness of the upstream identifier keeps the importer's matching and the forwarded identifier unambiguous until aliases (CAT-07) |
| D-6 | **One decision function, `Resolve`, with one refusal for every reason; exact public name only** | CAT-06 "whatever identifier the caller uses": an upstream identifier, a dated variant or another tenant's model is simply not in the caller's set. One error prevents learning other tenants' grants |
| D-7 | **Tenant restriction: one row of references, absent = unrestricted; key list: an immutable column on the key; both by reference, checked against visibility when written** | Mirrors scopes (immutable, in the principal, invalidated by existing payloads). An explicit row distinguishes "no restriction" from "nothing allowed". References survive a model being deleted and recreated; a reference that later becomes invisible simply stops matching |
| D-8 | **Price versions are immutable, start now or later, never in the past; future versions can be deleted** | ADR 0011 "versions are immutable" and the exit condition: a past instant must keep resolving to the same version. Allowing deletion before the start fixes mistakes in scheduled overrides without rewriting history |
| D-9 | **Own `Decimal` on `math/big`; `numeric(24,12)` storage; decimal text across SQL and JSON; no floating point in the package; costs exact, rounded only for display (half away from zero)** | ADR 0011 and data model rule 6; no dependency (ADR 0018). The dataset's tier-1 values have at most 6 fraction digits (verified); 12 leave room for per-token prices. JSON numbers are never used for money because clients parse them as floats |
| D-10 | **Currency per version; no conversion; imports are USD** | ADR 0011 says "with a currency". Overrides for private models may be in the operator's currency; adding currencies is 5.x and 7.x's problem, made explicit by `Cost.Currency` |
| D-11 | **Cost formula over `family.Usage` with fixed categories, cliff tiers on total input, documented fallbacks, explicit unknown reasons** | The normalization of ADR 0010 counts cache tokens inside input; the dataset's tiers are cliffs on input tokens; falling back to the input rate for an unpriced cache category is what providers bill when they do not distinguish it, and is reported |
| D-12 | **Overrides are versions; a flag on the model stops imports until cleared** | ADR 0011: "imports do not replace them". A flag is simpler and more honest than ending an override with a synthetic version; clearing does not invent a price, the next import does |
| D-13 | **The importer reads the v2 format and embeds the full v2 file at a pinned commit, with its MIT licence** | v1 is frozen upstream (README, verified). MIT is compatible with Apache-2.0 if the notice travels with the file. Embedding the whole file (820 KB) keeps operators' OpenAI-compatible providers priceable without a new release; it was scanned clean by the repository's secret scanner |
| D-14 | **The importer prices catalogue models only, matching their upstream identifier with the dataset's own match clauses; it never creates models** | Dataset identifiers include prefixes and pseudo-models (`ft:gpt-4o`, `gpt-4o:extended`) that are not callable; the catalogue is the platform admin's statement of what exists. Matching through the dataset's clauses prices dated variants without extra work |
| D-15 | **The embedded snapshot is imported at start once per digest, and never over a newer file or remote import** | ADR 0011 "a release embeds a snapshot"; without the second rule a restart of an older release would revert prices to older values |
| D-16 | **Remote refresh off by default; operator-configured https URL; no proxy, no redirects, 16 MiB, 60 s; in serve on the worker role; skipped when another instance refreshed recently** | ADR 0011 "an outbound call the operator has not asked for". The egress guard and proxy support are 3.1 (maintainer decision 1.1 O-2); a fixed operator destination is not a request-forgery surface. Operators behind a proxy use `import-prices --file` |
| D-17 | **The worker role gets `INSERT` on price tables, `price_import` and the platform audit log, in platform scope** | The refresh job is the first job; TEN-06 wants jobs with an identity and least privilege. It keeps running correctly when 8.2 moves it to a worker process |
| D-18 | **Payloads `catalog` and `catalog:<tenant>`; the principal cache and the `Opener` ignore them; the catalogue cache drops a tenant on `tenant:` too** | 2.1 §11. A suspended tenant's access entry must not outlive the principal cache's |
| D-19 | **The migration seeds two providers, their public endpoints and seventeen current models available to all tenants; prices come from the snapshot** | The credential foreign key needs the providers inside the migration. A short list of models gives a working installation; operators add more with `createModel`, and imports price them automatically. Availability `all` because tier-1 models under BYOK cost the operator nothing; `createModel` defaults to `selected`. Open point O-1 |
| D-20 | **Generator: `Optional[T]` for every optional member of update bodies; nested validation; the sentinel retired** | 2.1 §11 and 1.3's rule: the first sprint that needs another clearable member builds the feature with a test |
| D-21 | **Enforcement of CAT-06 in the inference path stays in 3.1**; this sprint delivers the function, the data and the cache | The route still forwards with `skeleton.openai.*`; adding the check there now would be removed by 3.1's pipeline |

## 9. Tests

Levels: U unit, D database (`make db-up`), A in-process API (`test/apitest`), E end-to-end with
the built binary, C container, R repository. IDs continue after T-362 and T-R22.

### New tests

| ID | Test | Level | Proves | Requirement |
|---|---|---|---|---|
| T-370 | `pricing.TestDecimal` | U | Parse and canonical text of a table of inputs; refusals (exponent, `+`, `NaN`, `Inf`, spaces, empty, too many digits); `Add`, `MulInt`, `DivPow10` exact; `Round` half away from zero at every place; round trip text; `FuzzParseDecimal` (parse → String → parse is identity) | **CAT-08** |
| T-371 | `pricing.TestCost` | U | The formula of §5.4 row by row; cliff tiers below, at and above a threshold; every fallback and its report; zero units unpriced is free; `no_price`, `category_unpriced`, `inconsistent_usage`; exactness (1 token at 0.000000000001 per million is `0.000000000000000001`); the breakdown lines sum to the amount | **CAT-08** |
| T-372 | `pricing.TestVersionAt` | U | Greatest `ValidFrom ≤ t`; equality at the boundary; before the first; future versions ignored | **CAT-08** |
| T-373 | `pricing.TestVersionValidate` | U | Currency format, missing base rate, duplicate threshold, negative, above 1e6, 13 fraction digits, unknown category; `SameRates` ignores order and trailing zeros | CAT-08 |
| T-374 | `genaiprices.TestParse` | U | `shapes.json` parses into the expected structure; numbers kept as text; unknown members ignored; a wrong type of a used member, a non-array root and 16 MiB + 1 are errors that quote no content | CAT-09 |
| T-375 | `genaiprices.TestMatch` | U | Each clause, nesting, case sensitivity, first match wins, a Python-only regex construct (`(?<=…)`) matches nothing with `regex_unsupported` | CAT-09 |
| T-376 | `genaiprices.TestExtract` | U | Every row of the extraction table of §5.6 with its issue; the conditional rule with `today` before, on and after a `start_date`; tiers to thresholds | **CAT-09** |
| T-377 | `genaiprices.TestSnapshot` | U | The embedded file parses; its SHA-256 equals `SOURCE`; `LICENSE` is the MIT text; every model of the migration seed matches and yields a valid version with an `input` rate (T-394 checks it in the database) | CAT-02, CAT-09 |
| T-378 | `catalog.TestNames` | U | Provider names (format, reserved list equal to `ReservedProviderNames`), endpoint URLs (schemes, private flag, IP literals of each range, user information, query, fragment, trailing slash, length), model names with slashes, references | **CAT-03**, CAT-01 |
| T-379 | `catalog.TestResolve` | U | The rule of §5.3 over a synthetic snapshot: each refusal is `ErrModelNotAllowed` with no distinguishing text; provider disabled or without enabled default endpoint; retired model; upstream identifier refused as a name; key list and tenant restriction combined; platform principal refused | **CAT-05**, **CAT-06** |
| T-380 | `catalog.TestCache` | U | Injected clock and loader: lifetime from load start; `catalog` reloads the snapshot; `catalog:` and `tenant:` drop one tenant; `vault:`, `platform`, probes nothing; unknown payload and `Flush` everything; full map emptied; failures not cached | **CAT-06** |
| T-381 | `authn.TestCache`, `vault.TestOpenerCache` (extended) | U | `catalog` and `catalog:<tenant>` remove no principal and no credential | TEN-10 |
| T-382 | `authz.TestCatalogue`, `TestRoles` (changed) | U | 25 permissions in order; roles of §5.9 | AZ-02 |
| T-383 | `config` tests (extended) | U | The five keys: defaults, bounds, `http` URL refused, environment names, `Keys()` and the reference in step | CAT-09 |
| T-384 | `apigen` tests (extended) | U | `Optional` generated for update schemas only; null refused; validation only when set; a schema used as PATCH body and elsewhere is refused; nested object and array-of-object validation with prefixed fields; generated code compiles (client to router round trip of an absent, an empty and a set member) | — |
| T-385 | `cli` tests (extended) | U | Every new operation has a command (T-219); model reference resolution; `key create --model`; `credential update --description ""` clears through `Some("")`; `import-prices` flag rules and exit codes | API-03 |
| T-386 | `catalog.TestFetch` | U | Against a TLS test server: 200 accepted; 404, 302 (not followed), stall past the timeout, body over 16 MiB, refused connection → the error classes; `HTTPS_PROXY` in the environment unused; only the two headers sent; no response byte in errors | CAT-09 |
| T-390 | `isolation.TestSchemaLint` (T-140), `TestDefaultSpecMatchesDesign` | D | Zero findings with migration 0004; the spec is the one of §5.1 and §6 | TEN-05 |
| T-391 | `isolation.TestCatalogPolicies` | D | For each restricted global table, role and session (tenant A, tenant B, platform, none): reads as §5.1; writes only in platform scope by the listed roles (42501 or a row-level security violation otherwise); grants visible to their tenant only; the reserved names and family checks; notification payloads `catalog` and `catalog:<tenant>` and nothing else | **CAT-01**, **CAT-05**, TEN-07 |
| T-392 | `isolation.TestPriceGuards` | D | Past `valid_from` refused; update refused; delete of a started version refused, of a future one allowed with its rates; rates immutable; `price_import` append-only; duplicate instant refused; `virtual_key.models` immutable; credential with an unknown provider and deleting a provider with credentials refused | **CAT-08**, CAT-09 |
| T-393 | `queries.TestEveryStatement` (T-230, extended) | D | Every new `sqlc` method, directly and through the pooler | — |
| T-394 | `catalog.TestImportDB` | D | **Exit 3**: import `base.json` → versions and one `price.create` audit row per version, `price_import.run`; import again → nothing new; import `changed.json` → one new version for the changed model only, its audit row names the previous version, the old version unchanged; override → `overridden`; unmatched and skipped reported; dry run writes nothing; two concurrent imports serialize and the second creates nothing; the worker role does it in platform scope; the embedded snapshot prices every seeded model | **CAT-09**, CAT-08 |
| T-395 | `catalog.TestPriceAtDB` | D | **Exit 2**: versions v1, v2, v3 created in sequence; `PriceAt` and `CostOf` for an instant before v1 (unknown), inside each interval, on each boundary and now give the version valid then; the cache's `VersionAt` agrees for every instant | **CAT-08** |
| T-396 | `catalog.TestImportAtStart` | D | Imports once per digest; skipped after a newer `file` or `remote` import; a failure is reported, not returned as fatal; two concurrent starts import once | CAT-09 |
| T-397 | `catalog.TestCacheDB` | D | Real listener on a direct connection: a grant, a revocation, a restriction change and a price version reach `Resolve` and `Versions` within 2 s; through the pooler (no notifications) within the injected TTL | **CAT-06** |
| T-398 | `catalog.TestResolveDB` | D | **Exit 1** at the database: model `availability = selected`, granted to tenant A: the gateway role's cache resolves it for A's principal and refuses B's with the same error; `all` makes it visible to both; A's restriction and a key list narrow it | **CAT-05**, **CAT-06** |
| T-400 | `admin.TestCatalogLifecycle` | A | Providers, endpoints, models: create, get, list with filters and pagination, update (each member, clearing with empty values), delete and every `conflict`, `invalid_state`, `invalid_request` of §5.8; reserved names; default endpoint moves; seed rows present | **CAT-01**, **CAT-02**, **CAT-03**, **CAT-04** |
| T-401 | `admin.TestModelVisibility` | A | **Exit 1** through the API: `selected` model granted to A: A's `listTenantModels` shows it, B's does not; grant idempotent; revoke; `all`; `retired` and disabled provider disappear | **CAT-05** |
| T-402 | `admin.TestModelRestrictions` | A | Restriction set, read, cleared; `allowed` flags; key `models` stored, shown, on the principal; an unknown reference and a reference to a model granted only to B give identical 400s; key list immutable | **CAT-05**, **IAM-04** |
| T-403 | `admin.TestPriceOverrides` | A | Override now and in the future; list with `at`; import skips the model; clear refused while a future version exists, allowed after deleting it; delete of a started version refused; rates as decimal strings out and in, never JSON numbers | **CAT-08**, **CAT-09** |
| T-404 | `admin.TestPriceImportAPI` | A | `embedded` runs and returns the report; `remote` while disabled → 400; with refresh enabled against the test server: success, and 502 `price_source_unavailable` for each failure class; invalid document → 422; get shows the report | CAT-09 |
| T-405 | `admin.TestCatalogAudit` | A | Every mutating operation writes the row of §5.11 with the request ID of the response; an operation that changes nothing writes none; a failing audit insert rolls the change back | AUD-01, CAT-09 |
| T-406 | `admin.TestCatalogPermissions` | A | A platform key without `catalog:price` cannot override; a tenant key scoped to `model:read` cannot restrict; `inference` reads no model list; a tenant key reaches no platform catalogue operation | AZ-02 |
| T-407 | `admin.TestUpdateBodies` | A | For `updateTenant`, `updateServiceAccount`, `updateCredential`, `updateProvider`, `updateEndpoint`, `updateModel`: an absent member is unchanged, an empty string clears, `null` for a non-nullable member is 400; the sentinel no longer exists | — |
| T-410 | `e2e.TestCatalogue` | E | `serve` from an empty database logs `prices imported` (source `embedded`); `provider create`, `model create`, `model grant` with the CLI; `import-prices --file changed.json` against the same database → exit 0, one new version, `price.create` and `price_import.run` rows read as superuser; restart → `price import skipped` (`newer_import`) | **CAT-09**, exit 3 |
| T-411 | `e2e.TestPriceRefresh` | E | Refresh enabled against the local TLS server (interval 1 h, first run after the start delay shortened by the test-only environment `PANDOKOS_TEST_REFRESH_DELAY`): one remote import; a 404 server gives `price import failed` with `error_class=status` and the process stays ready | CAT-09 |
| T-412 | `container` test (extended) | C | The image starts, imports the embedded snapshot and logs `prices imported` | CAT-09 |
| T-R23 | `repo.TestPricingIsPure` | R | The import rules of §4 for `pricing` and `genaiprices`; no `float32`/`float64` token in their non-test files | **CAT-08** |
| T-R24 | `repo.TestPriceSnapshot` | R | `snapshot/LICENSE` and `SOURCE` exist; `SOURCE` names the URL of a commit, not of a branch; `docs/operations/catalogue.md` names the five configuration keys, the snapshot's licence, the bound of §5.7, and the phrase "imports never change an override"; `docs/reference` lists the new permissions, commands and codes (T-R14 extended) | CAT-09 |

### Changed tests of epic 1 and 2

T-140, T-142, T-143 (new tables through the spec); T-201, T-202 (problem codes); T-219;
T-230; T-270 to T-274; T-281, T-282; T-305, T-306; T-309; T-340 (provider check now from the
catalogue; message unchanged); the tests of `updateCredential` and the CLI that used
`EmptyDescription`; T-R13, T-R14, T-R15; every test that starts `serve` (`pricing.import_at_start`
is true by default: tests that count audit rows set it to false or account for the import).

### Requirement coverage

| Requirement | Tests |
|---|---|
| CAT-01 | T-378, T-391, T-400 |
| CAT-02 | T-377, T-394, T-400 |
| CAT-03 | T-378, T-400 |
| CAT-04 | T-400 |
| CAT-05 | T-379, T-391, T-398, T-401, T-402 |
| CAT-06 | T-379, T-380, T-397, T-398 |
| CAT-08 | T-370 to T-373, T-392, T-395, T-403, T-R23 |
| CAT-09 | T-374 to T-377, T-383, T-386, T-394, T-396, T-403, T-404, T-405, T-410 to T-412, T-R24 |
| IAM-04 | T-402 |

## 10. Definition of done for this sprint

**Requirements and tests**

- [ ] Every test of section 9 exists under the stated name and passes; the changed tests pass.
- [ ] The tests-first record exists: the commit of part B precedes the implementation commits and
      the pull request quotes its failing output.
- [ ] The coverage table is repeated in the pull request with links to the CI run.

**Isolation and permissions**

- [ ] `tenant_model_restriction` has the canonical policy, forced row-level security and a
      fixture; the seven global tables are listed with reasons and their policies are tested by
      T-391; T-140 reports zero findings.
- [ ] Each of the 29 operations names a permission of the catalogue and is exercised by T-270
      (cross-tenant or platform classification), T-272 and T-273.

**Exit conditions of the epic**

- [ ] A model is visible to one tenant and not to another (T-398, T-401).
- [ ] A cost for a past timestamp uses the version valid then (T-395).
- [ ] An import that changes a price adds a version and an audit record (T-394, T-410).

**Quality gates**

- [ ] `make check` passes locally and in CI; `make image` and `make test-container` pass.
- [ ] `go.mod` and `go.sum` are unchanged; the snapshot is byte-identical to the pinned upstream
      file (`sha256sum` equals `SOURCE`); `make secrets` passes with the snapshot committed.

**Documentation and decisions**

- [ ] `docs/operations/catalogue.md` exists; configuration, database, keys, container,
      permissions, admin API, data model, security model and overview pages are updated (plan.md
      part E); `NOTICE`-style attribution of the dataset is in the guide and next to the file.
- [ ] ADR 0035 and 0036 are added; ADR 0009, 0011, 0027, 0028 and 0030 carry history lines; the
      index lists the new records.
- [ ] `migrations.sum` has the line of `0004_catalogue.sql`.
- [ ] `status:` of this file is `done`; the review records the answers to section 12.

## 11. Consequences for later sprints

- **3.1 (gateway)** defines the interface through which `internal/gateway` receives
  `catalog.Cache` (it cannot import `store`); routes by provider name with the provider's
  family; calls `Resolve` after `Decide` and before reading the body further than the model
  name, mapping `ErrModelNotAllowed` to `model_not_allowed` and `ErrProviderUnavailable` to the
  family's not-found shape; forwards to the default endpoint's `base_url` + native path and
  decides whether it replaces the public name by `upstream_id` (ADR 0009 allows it); enforces
  `private` in the egress guard; decides `HTTPS_PROXY` for provider traffic **and for the price
  download** (`catalog.Fetch` has `Proxy: nil` until then); moves credential validation onto
  catalogue endpoints; removes `skeleton.openai.*`.
- **3.2** calls `pricing.CostOf` with the version from the cache at the request's start time,
  stores `price_version_id`, amount (`numeric(38,18)`), currency, `Known`, `Reason` and the
  fallbacks on the usage record; an unknown cost is recorded as such (CAT-08); adds non-token
  units if it wants the dataset's `*_kcount` prices (they are reported as ignored today).
- **3.3** adds the capability endpoint on top of `Resolve`; decides how the unified `/v1/…`
  endpoint picks a provider when two `openai`-family providers expose the same model name; adds
  the `anthropic` usage reader's `cache_write_1h_tokens` to `Extra` under that name.
- **5.x** estimates request cost with the same function and decides budgets in one currency.
- **6.3** removes a deleted tenant's `model_tenant_grant` rows (global table; the worker needs a
  grant) and its restriction row.
- **7.1** blocks platform-funded calls when `Known` is false unless configured; decides whether
  rating rules reference price versions.
- **8.2** runs the refresh job in the worker process; nothing else changes (D-17).
- **11.2, 11.3** build the pages on these operations; rates are decimal strings in the console.
- **R4 (CAT-07, CAT-03 S)**: aliases relax the uniqueness of `upstream_id`; tenant endpoints add
  an allowed host pattern to `provider`.

## 12. Risks and open points

### What was verified while designing

- On the test PostgreSQL 16 of `make db-up`, in a scratch database: migration `0004_catalogue`
  applies after 0001 to 0003 as `pandokos_owner`; seed rows; for each role: the gateway reads the
  catalogue in a tenant session and cannot write; the admin role cannot write without platform
  scope or in a tenant session (row-level security violation) and can in platform scope; reserved
  names, immutability of provider name and family, `http` only for private endpoints; grant rows
  visible to their own tenant only (A sees 1, B sees 0), not insertable from a tenant session; the
  worker inserts an import, a version, rates and a platform audit row in platform scope and
  nothing without it; past `valid_from`, updates, deletion of a started version refused; a future
  version deleted with its rates; `price_rate` immutable; `price_import` append-only;
  `virtual_key.models` immutable; the credential foreign key in both directions; a model
  deletion cascading its grants under forced row-level security; deleting a priced model
  refused; the payloads `catalog`, `catalog:<tenant>`; the rate statement with
  `generate_series` and the `PriceAt` aggregation.
- The grants matrix of plan.md B2 was read back from `information_schema` after applying the
  migration.
- `sqlc` v1.31.1 parses the migration and generates appendix C (43 new methods) with the
  `numeric` override; the generated package builds; no `pgtype` in any generated file.
  (A multi-argument `unnest` is not understood by `sqlc`; the statement uses `generate_series`.)
- The dataset: `https://github.com/pydantic/genai-prices`, MIT licence ("Copyright (c) Pydantic
  Services Inc. 2025 to present"); README: v1 frozen, v2 current. At upstream HEAD
  `5acfa1123141a4216ef77dc6b1c8a6aa948f9e81` (observed 2026-11-14 with `git ls-remote`),
  `prices/new_data/v2/data.json` is 820 690 bytes with SHA-256
  `1aa26e77c74c171fe66e4215ecd5ecc22a378e4568cb4522984b1c048c0b3970`: 47 providers, 2 334
  models, 310 conditional prices (302 `start_date`, 8 time-of-day), 111 models with
  `price_variants`, 560 tiered values; no negative value; tier-1 values have at most 6 fraction
  digits (only `openrouter` and `huggingface_together` exceed 12); no `sk-…` string and no
  Pandokos key pattern; `gitleaks` v8.30.1 with the repository's configuration finds nothing in
  it. Every model of the seed is matched by the dataset's clauses, with the context windows
  written into the migration.

**Not verified**: the Go code (not written); the generator changes; the behaviour of
`raw.githubusercontent.com` towards a non-browser client over time (redirects, rate limits).

### Risks

| # | Risk | Mitigation |
|---|---|---|
| R-1 | **The sprint is large**: three packages, a generator feature, a migration with nine objects, 29 operations, about fifty tests | Parts C1 to C5 have no HTTP and are reviewable alone; the generator change (C6) is isolated and tested first; the API parts are mechanical over the same helpers |
| R-2 | **The dataset format changes** (v2 is recent; v1 was frozen) | The format is named in every run (`dataset_format`); a change breaks imports, not running installations (ADR 0011); the embedded snapshot keeps working; T-374 pins the parser to fixtures |
| R-3 | **Imported prices are wrong** (upstream error or a compromised repository) | Bounds per value; one audit row per changed price with old and new rates; overrides win; refresh off by default; the URL can be pinned to a commit; scheduled future prices are not imported |
| R-4 | **Constructs not represented**: time-of-day prices (DeepSeek), service tiers and batch variants (OpenAI, Anthropic), non-token units (web searches) | Reported per model in every run; costs use the standard price, so they are an upper bound for discounted tiers and a lower bound when searches are used; operators override |
| R-5 | **A stale cache prices a request with the previous version for up to `cache.catalog_ttl`** | Bounded and documented; the version ID is recorded with the cost (3.2), so records stay explainable |
| R-6 | **The embedded file adds 0.8 MB to the binary and the repository** | Accepted for a self-contained price list; a slim file was considered (O-3) |
| R-7 | **The seed's model list ages** | It is a starting set: operators add models; the snapshot of each release prices new catalogue models; no migration edits it |
| R-8 | **The worker role writes prices and the platform audit log** | Least privilege: insert only, platform scope only, pinned by the grants matrix (L-12) |
| R-9 | **Mixed-version roll-out**: an older instance flushes its whole principal cache and Opener on every `catalog` payload | Safe by design (ADR 0030); no release exists before this sprint |

### Open points (raised to the maintainer; none blocks the sprint)

| # | Point | Recommendation |
|---|---|---|
| O-1 | Seed: seventeen models with `availability = all` (D-19). Alternatives: no models, or `selected` (default deny for the seed too) | Keep D-19: BYOK models cost the operator nothing and an empty catalogue makes 3.1 refuse every call; models created later default to `selected` |
| O-2 | Imports never create models (D-14). An alternative is a per-provider switch that creates missing models from the dataset (with `selected`) | Keep D-14 for v0.1; revisit with CAT-10 (declarative catalogue, R2) |
| O-3 | Embedded file: full v2 (820 KB) or `data_slim.json` (565 KB, long fields and free models removed) | Full file: free models matter for self-hosted endpoints, and descriptions are not imported anyway |
| O-4 | Scheduled prices (`start_date` in the future) are not imported as future versions; a later import picks them up, so a price change takes effect at the first import after its date | Keep: upstream withdrew such a scheduled increase once (the dataset says so for one Anthropic model); a future version would need retraction logic |
| O-5 | Remote refresh does not honour `HTTPS_PROXY` until 3.1 | Keep (consistent with 1.1 O-2); document `import-prices --file` for proxied installations; 3.1 decides both together |
| O-6 | Currency per version without conversion (D-10). Alternative: one installation currency (`pricing.currency`) that overrides must use | Keep per-version currency; 5.x and 7.1 decide how budgets and rating handle several |
| O-7 | Grants live in the platform log only; the tenant's audit log does not show that a model was granted or revoked | Keep; the tenant sees the effect in `listTenantModels`; the audit read API (6.2) can join both logs |
| O-8 | A tenant restriction and key lists name models by reference, so a model deleted and recreated under the same name is allowed again | Keep: models are retired, not deleted, once priced; deletion is for mistakes |

### Maintainer decisions on the open points (2026-11-14)

| # | Decision |
|---|---|
| O-1 | Confirmed: the 17 seeded models are available to all tenants; models created later default to selected tenants only |
| O-2 | Confirmed: the importer never creates models in v0.1; revisited with the declarative catalogue export and import (CAT-10) |
| O-3 | Confirmed: the full v2 file is embedded, with its licence |
| O-4 | Confirmed: future-dated prices are not imported; a later import picks them up once in force |
| O-5 | Confirmed: the download does not honour `HTTPS_PROXY` until 3.1; `import-prices --file` covers installations behind a proxy |
| O-6 | Confirmed: currency per version, no conversion; epics 5 and 7 decide multi-currency handling |
| O-7 | Confirmed: grants are audited in the platform log only |
| O-8 | Confirmed: priced models are retired, not deleted |
