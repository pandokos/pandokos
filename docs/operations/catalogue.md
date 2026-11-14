# The catalogue and prices

The catalogue says which providers, endpoints and models an installation knows, which tenants may use
which models, and what each model costs. This guide is for the platform administrator. Decisions:
[ADR 0035](../adr/0035-catalogue-and-model-access.md) (catalogue and access) and
[ADR 0036](../adr/0036-prices-costs-and-importer.md) (prices). The inference route does not read the
catalogue yet; it starts to in sprint 3.1, and costs are computed and recorded from sprint 3.2. Until
then the catalogue is administered, imported and tested, not used for calls.

Every command below is a client of the administration API ([reference](../reference/admin-api.md)) and
needs a platform key; model access for a tenant needs a key of that tenant.

## 1. Concepts

| Concept | What it is | Notes |
|---|---|---|
| Provider | A company or service that sells models, such as `openai` | `name` is the first path segment of the inference route, fixed at creation, lower case letters, digits and hyphens; `healthz`, `readyz` and `v1` are reserved. Has an API family and can be disabled |
| API family | The wire format the provider speaks: `openai` or `anthropic` | Fixed per provider at creation; the list is in the program |
| Endpoint | A base URL of a provider, such as `https://api.openai.com` | A provider has one default endpoint, which the gateway uses; `private` marks one that may use `http` or a private address |
| Model | What callers name: `provider/name` | `name` (public, fixed), `upstream_id` (what is sent to the provider, defaults to the name), status `active`, `deprecated` or `retired`, availability `all` or `selected` |
| Credential | A tenant's secret for a provider, kept in the [vault](vault.md) | The credential names the provider; the provider must be in the catalogue |

A model is always referred to as `provider/name`, split at the first `/` (a model name may contain more
slashes, such as `meta-llama/Llama-3-70b`; a provider name never does).

## 2. The seed and how to add a provider

The migration seeds the providers `openai` and `anthropic`, each with an enabled default endpoint named
`default` (`https://api.openai.com`, `https://api.anthropic.com`) and these models, all available to
every tenant: `gpt-4o`, `gpt-4o-mini`, `gpt-4.1`, `gpt-4.1-mini`, `gpt-4.1-nano`, `gpt-5`, `gpt-5-mini`,
`gpt-5-nano`, `o3`, `o4-mini`, `text-embedding-3-small`, `text-embedding-3-large`, `claude-haiku-4-5`,
`claude-sonnet-4-5`, `claude-opus-4-5`, `claude-sonnet-4-6`, `claude-opus-4-6`. No price is seeded: the
first start imports the embedded price list (section 5).

To add an OpenAI-compatible provider, for example a self-hosted server that is not reachable from the
internet:

```
pandokos provider create vllm --family openai --display-name "Our vLLM"
pandokos endpoint create vllm main --base-url http://models.internal:8000 --private
pandokos model create vllm/llama-3-70b --upstream-id meta-llama/Llama-3-70b-instruct \
    --availability selected
pandokos credential create --tenant ACME --provider vllm --secret-file ./vllm.key "vllm for ACME"
pandokos model grant vllm/llama-3-70b --tenant ACME
```

- The first endpoint of a provider becomes its default; `endpoint update --default` moves the default
  in one step. The base URL has a scheme and a host, no user information, query or fragment, and no
  trailing slash (a trailing `/` is removed; the scheme and the host are lower-cased). The native path
  of a request is appended to it. Only a `private` endpoint may use `http`, and a public one may not
  name a loopback, private, link-local or unspecified address. Host names are not resolved when the
  endpoint is written; the guard that checks the address actually dialled belongs to the gateway
  sprint (3.1). Declare an endpoint `private` only for a destination you control.
- A model is created `selected` unless `--availability all` is given: nobody may use it until a tenant
  is granted it. Names and upstream identifiers are unique per provider.
- A provider is deleted only when no endpoint, model or credential references it. A model with price
  versions cannot be deleted: set its status to `retired` instead.
- A disabled provider still accepts credentials, so a tenant can prepare one before the provider is
  enabled.
- `--dataset-provider ID` on a provider tells the price importer which provider of the price dataset
  holds its prices. Without it the provider's models are never priced by an import (you can set an
  override, section 6).

## 3. Who may use a model

A principal may use a model only when every one of these holds (the rule is applied in this order):

1. the provider exists, is enabled and has an enabled default endpoint;
2. a model of that provider has exactly that public name (the upstream identifier is never accepted in
   its place) and is not `retired`;
3. the model is available to the tenant: availability `all`, or `selected` and the tenant holds a
   grant;
4. the tenant has no restriction, or its restriction lists `provider/name`;
5. the key has no model list, or its list names `provider/name`.

Every failure after the first is the same refusal, so a caller cannot tell a model that does not exist
from one that was granted only to other tenants. A platform principal has no tenant and cannot call
models.

| To | Command | Permission |
|---|---|---|
| Make a model available to all or to selected tenants | `model update REF --availability all\|selected` | `catalog:write` |
| Grant or take back a model | `model grant REF --tenant T`, `model revoke-grant REF --tenant T`, `model grants REF` | `catalog:write`, `catalog:read` |
| Retire or deprecate a model | `model update REF --status retired\|deprecated` | `catalog:write` |
| Narrow a tenant to a list | `model-restriction set --tenant T --model REF ...` (`--none`: no model), `model-restriction clear --tenant T`, `model-restriction get --tenant T` | `model:restrict`, `model:read` (tenant) |
| See what a tenant may use, with prices | `tenant-model list --tenant T` | `model:read` |
| Narrow one key | `key create --model REF ...` ([keys](keys.md)) | `virtual_key:write` |

Granting is idempotent, and so are revoking a grant and clearing a restriction. Switching a model
between `all` and `selected` keeps its grants; they matter again when it returns to `selected`. A
restriction and a key list can only narrow: they never give access to a model the tenant is not
otherwise allowed. A restriction may name only models visible to the tenant, and so may a key's list;
an unknown name and an invisible one give the same error.

## 4. Prices

A **price version** belongs to a model and holds a currency and, for each category, a rate per million
units. Versions never change. A new price is a new version that starts at the moment it is created or
later, never in the past; a version that has not started may be deleted. The version **in force at t**
is the one with the greatest start not after t. `price list MODEL` shows the versions and `--at TIME`
the one in force then.

Categories and the units they price, from the usage of a call (input tokens include cache reads and
writes; output tokens include reasoning, [ADR 0010](../adr/0010-usage-extraction.md)):

| Category | Units |
|---|---|
| `cache_read` | cache read tokens |
| `cache_write_1h` | tokens written to the one-hour cache |
| `cache_write` | cache write tokens, less those of the one-hour cache |
| `input_audio` | audio input tokens |
| `input` | input tokens, less cache reads, cache writes and audio input |
| `output_audio` | audio output tokens |
| `output` | output tokens, less audio output (reasoning is included) |

The cost of a call is the sum, over categories, of units times rate divided by a million. A rate may
have **tiers**: a threshold in input tokens of the request, and the rate with the greatest threshold not
above the request's input tokens applies to every unit of its category (a cliff, not a staircase). A
category with units and no rate is priced at a **fallback** rate and the cost says so: `cache_read`,
`cache_write` and `input_audio` at `input`; `cache_write_1h` at `cache_write`, then `input`;
`output_audio` at `output`. A category with no rate after the fallback makes the cost **unknown**
(`category_unpriced`); with no version in force it is unknown too (`no_price`); usage that does not add
up (a negative unit count) is unknown (`inconsistent_usage`). Zero units cost nothing. Unknown is not
zero: it is recorded as unknown.

Rates and costs are exact decimals, never floating point; the API and the CLI carry them as decimal
strings (`"2.5"`), with at most 12 fraction digits for a rate. A currency is three capital letters; an
import writes `USD`, an override may name any. Nothing converts or adds amounts of different
currencies; a cost carries its currency.

## 5. The dataset

Prices come from the open dataset **genai-prices** by Pydantic
(<https://github.com/pydantic/genai-prices>), published under the **MIT** licence (the licence and
the copyright line are kept beside the data). Pandokos reads the **v2 format**
(`prices/new_data/v2/data.json`); the older v1 files are frozen upstream.

- **Embedded.** Every release carries the v2 file of a pinned upstream commit, byte for byte, with its
  licence and a `SOURCE` file (URL, commit, retrieval date, SHA-256), in
  `internal/pricing/genaiprices/snapshot/`. This release embeds commit `5acfa1123141`, retrieved
  2026-11-14.
- **What is imported.** The importer never creates models. For every model of a provider that has a
  `dataset_provider`, it looks for the first model of that provider in the dataset whose match clauses
  accept the catalogue model's upstream identifier (exact, case-sensitive; regular expressions use Go
  syntax), and imports the price in force today. A changed price becomes a new version starting at the
  import, with an audit record; an unchanged one creates nothing.
- **What it cannot represent** is reported per model, never silently dropped (an issue in the report):

  | Issue | Meaning |
  |---|---|
  | `scheduled_price_not_imported` | a price that starts in the future; a later import picks it up |
  | `time_of_day_price_ignored` | a price that depends on the time of day |
  | `price_variants_ignored` | variants such as a service tier; the standard price is used |
  | `category_ignored` | a unit that is not one of the seven categories (searches, images, ...) |
  | `value_not_representable` | a negative rate, more than 12 fraction digits, above 1,000,000, or a tier start above 100,000,000: the model is skipped |
  | `malformed_prices` | the price of the model has an unexpected shape: the model is skipped and the import goes on |
  | `no_supported_price` | nothing left to price: the model is skipped |
  | `regex_unsupported` | a match pattern Go cannot compile: it matches nothing; the report names the model, not the pattern |

  Per model the outcome is `created`, `unchanged`, `overridden`, `unmatched` or `skipped`. Discounted
  tiers, variants and time-of-day prices are therefore priced at the standard rate.
- **Reports.** Every run is recorded (`price-import list`, `price-import get ID` shows the report, cut
  at 60,000 bytes with a count of what was left out). A dataset that cannot be read records a failed
  run and creates nothing.

**`import-prices --file`.** `pandokos import-prices --file FILE` (or `--embedded`; add `--dry-run` to
see the report and write nothing) imports a dataset from a file, with the admin database user and no
network: use it behind a proxy or in an air-gapped network after downloading the file yourself. It
reads and checks the file (16 MiB at most) before it opens the database, prints `models_seen`,
`created`, `unchanged`, `overridden`, `unmatched` and `skipped`, one `name: value` line each, then one
line per model, and exits 0 on success even with issues, 1 when the import fails, 2 for wrong usage. The
same import is available through the API: `price-import run --source embedded [--dry-run]`.

**The import at start.** With `pricing.import_at_start` (`PANDOKOS_PRICING_IMPORT_AT_START`, default
`true`), `serve` imports the embedded list after its database checks and logs `prices imported`. It
skips the import in two cases, both logged as `price import skipped`: a successful import of the same
digest exists (`digest_seen`), or a successful file or download import was recorded after the retrieval
date of the embedded list (`newer_import`: an older release must not replace a newer dataset). A failure
logs `price import failed` and never stops the server. Several instances starting together take turns
and the second one skips.

**The refresh.** With `pricing.refresh.enabled` (`PANDOKOS_PRICING_REFRESH_ENABLED`, default `false`) a
job of `serve` downloads the dataset from `pricing.refresh.url` (`PANDOKOS_PRICING_REFRESH_URL`) one
minute after start and then every `pricing.refresh.interval` (`PANDOKOS_PRICING_REFRESH_INTERVAL`, 24
hours by default, 1 to 720 hours); it skips a run when another instance imported less than half an
interval ago. The download is a single `GET` over `https` with a total limit of 60 seconds and 16 MiB,
without a proxy (`HTTPS_PROXY` is ignored until sprint 3.1 decides it) and without following redirects;
a failure (`unreachable`, `timeout`, `status`, `redirect`, `too_large`, `dataset_invalid`) is logged, kept
as a failed run and not retried before the next interval. No byte of the response is logged. The URL is
operator configuration only; no request can change it. The default URL names the `main` branch, which
moves: **pin it to a commit** (`.../genai-prices/<commit>/prices/new_data/v2/data.json`) so that what you
import is what you reviewed. The dataset is data, not code; the worst a bad one does is wrong cost
estimates, which the report shows and an override corrects.

## 6. Overrides

An operator can fix a price, or price a model the dataset does not know:

```
pandokos price override openai/gpt-4o --currency USD --rate input=2.4 --rate output=9.6 \
    --rate input@200000=4.8 --note "contract price"
```

- `--rate category[@threshold]=amount` is repeatable; every category needs a rate without threshold
  (the base rate); thresholds are unique and at most 100,000,000.
- The override is a version with source `override`, starting now or at `--valid-from` (not in the past),
  and it marks the model as overridden. The rule is that imports never change an override: while a model is marked,
  a run reports it as `overridden` and creates no version for it.
- `price delete MODEL VERSION` removes a version that has not started (a scheduled override). Deleting
  the last scheduled override does not clear the mark.
- `price clear-override MODEL --reason R` lifts the mark, so the next import may create versions. It is
  refused while a version of the model has not started. The override stays in force until the next
  import creates a version.

## 7. How fast a change takes effect

The gateway keeps the catalogue (providers, endpoints, models, price versions) and each tenant's access
(grants and restriction) in memory for `cache.catalog_ttl` (`PANDOKOS_CACHE_CATALOG_TTL`): **30 s** by
default, from 1 s to **5 m** at most. PostgreSQL notifications (`catalog` and `catalog:<tenant>`) empty
the cache at once, normally in under a second; the lifetime is the guarantee when notifications are not
delivered, for instance behind a pooler in transaction mode without `database.direct_address`
([keys](keys.md)). The administration API does not cache. A call that is already running is not
interrupted. A price changed during a call's lifetime may be priced with the previous version; the
version is recorded with the cost, so the record is self-consistent.

## 8. Limits

- Costs are **estimates, not invoices** ([ADR 0011](../adr/0011-pricing-source.md)): the provider's
  invoice is the authority. Use an override when you have a contract price.
- Price variants (service tiers, batch discounts, regions) and time-of-day prices are not represented;
  they are priced at the standard rate and reported.
- Currencies are not converted and amounts in different currencies are never added.
- The dataset is third-party data under the MIT licence; its accuracy is not guaranteed.
- A model the dataset does not match has no price until you set an override; its cost is unknown, not
  zero.
- The inference route does not yet check access or compute costs (sprints 3.1 and 3.2).
