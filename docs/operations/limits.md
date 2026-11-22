# Limits guide

Pandokos can refuse a request before it reaches a provider when the caller, the key or the tenant
would pass a limit: requests per minute, tokens per minute, requests in flight, and, over a calendar
day or month, requests, tokens or cost. This page says what is counted, how a request is estimated
before it is sent, what the limits guarantee and what they only bound, what the caller sees when one
is reached, how the counters relate to the usage ledger, how to write rules and how to choose
values. It applies from sprint 5.1 ([ADR 0048](../adr/0048-reserve-and-settle.md),
[ADR 0049](../adr/0049-limit-rules.md)) and sprint 5.2 ([ADR 0050](../adr/0050-quotas-and-budgets-periods-attribution-currencies.md),
[ADR 0051](../adr/0051-period-counters-and-the-ledger.md), [ADR 0052](../adr/0052-administration-of-limits.md)),
sprint 5.3 (the amendments of ADR 0049, 0050 and 0052): rules that reference their subject and
scope, the time zone of a tenant, the currency check of budgets, `limit_state`; and sprint 5.4
([ADR 0053](../adr/0053-tenant-price-lists-and-active-currency.md), [ADR 0054](../adr/0054-usage-data-is-the-tenants.md),
the amendments of 0049, 0050 and 0052 again): the active currency of a tenant, one rule per currency,
and the usage details that the log no longer carries.

> **Limits are per gateway instance until the Valkey store exists.** Each instance counts alone, in
> memory: with *N* instances a limit admits up to *N* times its value. A restart empties the rate
> windows and the concurrency gauge; the counters of days and months are rebuilt from the usage
> ledger ([section 11](#11-counters-and-the-ledger)). **With no rule nothing is limited**: nothing is
> seeded. Rules are written through the administration API or the CLI
> ([section 13](#13-managing-rules)).

## 1. What is counted

| Metric | Counts | Window |
|---|---|---|
| `concurrent_requests` | requests in flight: admitted and not yet finished | none (a gauge) |
| `requests_per_minute` | requests that Pandokos tried to send | sliding, 60 to 61 s |
| `tokens_per_minute` | input plus output tokens, reserved at the start and corrected at the end | sliding, 60 to 61 s |
| `requests_per_day`, `requests_per_month` | requests that started in the calendar day or month | the period, in the time zone of the tenant ([section 4](#4-periods-and-the-time-zone)) |
| `tokens_per_day`, `tokens_per_month` | input plus output tokens of the requests that started in the period | the period |
| `cost_per_day`, `cost_per_month` | the cost of the requests that started in the period, in one currency | the period |

- The window of the two rates is never shorter than a minute and is up to one second longer: an
  amount is added to the second in which the request was admitted and is counted until 61 seconds
  after the start of that second. Because of that, a limit holds in **every** interval of 60 seconds,
  not only in the minutes of the clock.
- A request counts once its credential has been opened, that is, once Pandokos is about to try to
  send it. The usage record of such a request carries a credential ID, and that is exactly the
  rule the ledger uses: the counters and the ledger count the same requests. A request refused
  before that (an invalid body, a model the key may not use, a limit, no usable credential) is not
  counted and consumes no tokens. A request the provider answered with an error, or that could not
  reach the provider, stays counted as a request and counts no tokens. A request refused by a limit
  (`rejected_limit`) counts nothing.
- `messages.count_tokens` is a request and a slot, and no tokens.
- A limit value is an integer from 1 to 1,000,000,000,000. A budget is an amount with at most six
  fraction digits, up to 1,000,000,000 units of its currency ([section 5](#5-budgets)).

## 2. Levels and subjects

A request is checked against **one limit per subject, scope and metric that has a rule**. Every
limit must admit it: a rule can take room away and never add it.

| Subject (`subject_kind`) | What it names | Counter | Value in force |
|---|---|---|---|
| `tenant` | the calling tenant as a whole | the tenant | the smaller of the platform ceiling and the tenant's own rule, whichever exist. The platform ceiling is the platform row of that tenant if there is one, else the platform default |
| `service_account` | one service account of the tenant | the service account's ID | the tenant's rule for that service account |
| `key` | one virtual key, or, for a caller that presents a token, its subject mapping | the key ID, or the mapping ID for a token | the tenant's rule for that key |

Limits of a service account apply to its keys and to its tokens alike. A subject mapping, which is what
a token's caller is identified by, can be the subject of a rule of kind `key` (its mapping ID is the
`--key`; [tokens guide](tokens.md)). A rule names a subject that exists: the database refuses one for a
subject that does not, for a subject of another tenant and for a revoked key, and it refuses to delete a
subject while a rule names it. Whoever removes a subject removes its rules first: revoking a key, deleting
a subject mapping, a registration or a platform-level registration deletes the rules of those subjects in
the same transaction, each with an audit row ([section 13](#13-managing-rules)). A disabled service
account keeps its rules. A key rule above the tenant's limit is valid and has no effect. The platform can
raise a tenant's ceiling with a row for that tenant that replaces the default; a tenant cannot
loosen what the platform set. A platform ceiling counts each tenant on its own: a limit on the sum
over all tenants cannot be expressed. The platform ceiling and the tenant's own rule share one
counter when they are in the same currency (for a budget); the value in force is the smaller of the
enforced values and the thresholds of both are kept.

Rules live in two tables ([database guide](database.md)). Counters exist only for subjects that
have a rule and traffic.

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

## 2a. Two quotas

Two different quotas stand between a caller and a model, and only one of them is Pandokos's.

| | The quota decided in Pandokos | The quota a provider grants to a credential |
|---|---|---|
| Who sets it | you, with the rules of this guide | the provider, for the account that owns the credential: rate tiers (requests and tokens per minute), spend limits, daily caps |
| What it counts | what passes through this Pandokos installation, by tenant, service account, key, provider and model | everything the credential is used for, by anyone, anywhere, including other Pandokos tenants or instances that hold the same account |
| Visible to Pandokos | yes: it is the rule | **no**: Pandokos does not know the tier of a credential, does not read the provider's headers to learn it and has no number to compare |
| What the caller sees | 429 `rate_limited` or `quota_exceeded`, 409 `no_price`, from Pandokos | the provider's own 429 (or 5xx), forwarded as the provider sent it; Pandokos only records it in the usage record |

A Pandokos rule never raises what a provider allows, and nothing in Pandokos reduces itself when the
provider refuses. What you can do is set Pandokos's rules so that the provider's quota is not reached,
or is shared the way you want. Two recipes, with the rule that a tenant has **one default credential
per provider** (a key can be pinned to another credential of the tenant, and only then does it stop
using the default one: [keys guide](keys.md)).

**Protect a provider tier.** The tenant's credential for `openai` has a tier of 300,000 tokens a minute
at the provider. Put a rule of the tenant scoped to the provider below it, so that the tenant's callers
together are refused by Pandokos (a 429 with `X-Pandokos-Limit-Scope: provider`) before the provider
refuses them:

```
pandokos limit create --tenant acme --metric tokens_per_minute --limit 250000 --provider openai
```

Leave a margin for what the tier also counts that Pandokos does not (other users of the same account,
other instances: limits are per instance, [section 10](#10-what-is-strict-bounded-and-best-effort)), and
for the estimate of a request being only an estimate. A tier on requests, or on a daily spend, takes
the same recipe with `requests_per_minute` or a budget (`cost_per_day`).

**Keep a batch key from starving an interactive one.** A tenant runs an interactive application and a
nightly batch, each with its own virtual key, and both use the default credential of `openai`, so both
draw on its tier. A rule of the tenant alone (250,000) does not help: the batch can use all of it. Give
the batch key a rule of subject `key`, scoped to the provider, that leaves the room the interactive keys
need:

```
pandokos limit create --tenant acme --subject key --key 7f3a... --metric tokens_per_minute \
    --limit 100000 --provider openai
pandokos limit create --tenant acme --metric tokens_per_minute --limit 250000 --provider openai
```

The batch key can never use more than 100,000 a minute of the tier, and what it uses also counts against
the tenant's 250,000: the interactive keys keep at least 150,000. A request of the batch key is refused
when either counter has no room, and `X-Pandokos-Limit-Subject` says which. The rule is on the provider
because the default credential is the provider's: a request of the same key to another provider is
another counter. A key **pinned** to another credential of the tenant draws on that credential's tier,
not on the default one: do not count its requests against the default credential's rule, and give it its
own rule if its credential has a tier to protect.

## 3. Scopes

A rule can be narrowed to what is called, with the two members `provider` and `model` (names of
the catalogue):

| Scope (`X-Pandokos-Limit-Scope`) | Members | Matches |
|---|---|---|
| `all` | neither | every request of the subject |
| `provider` | `provider` | requests to that provider |
| `model` | `provider` and `model` | requests to that model of that provider |

A model needs its provider. A rule holds the **identifiers** of its provider and model, not their names:
the API takes and shows names (and, in a rule, the identifiers `provider_id` and `model_id`, empty when
the rule is not scoped), the gateway and the counters match by identifier. A model created with the name
of a deleted one is another model, which no earlier rule matches. A request is checked against the rules of every scope that matches it,
each with its own counter, so a narrower rule can only restrict: a tenant with 5,000,000
`tokens_per_month` for all and 1,000,000 for `openai` / `gpt-4o` can spend the 5,000,000 across
everything, and no more than 1,000,000 of it on that model. There is one rule per subject, scope
and metric. When a rule is written the provider must exist and the model must be one
the tenant can see (the answer is the same whether it is elsewhere or nowhere), and the database holds
the reference from then on: a provider or a model that a rule of any tenant or of the platform names
cannot be deleted (409 `invalid_state`; [catalogue guide](catalogue.md)). Rate metrics can be scoped too.
A provider that is deleted and created again with the same name inside one period is the one exception:
the ledger stores the provider by name, so the rows of the old provider count for a rule on the new one,
which errs towards restricting.

## 4. Periods and the time zone

Every tenant has a time zone, in which its days and months start. `limits.time_zone` names an IANA zone
(`UTC` by default): it is the zone of **the tenants that have none of their own**, and the zone a tenant
gets when it is set to the default. The binary carries its own zone database, so the answer does not
depend on the container image. A day is a civil date in the zone and a month is a civil month; a day is
as long as the zone makes it, 23 or 25 hours on the days when the clocks change (daylight saving time),
and starts at the first instant of the civil date even if local midnight does not exist or exists twice.
A zone must have offsets that are whole minutes: `limits.time_zone` is refused at start-up with an error
naming it, and a tenant's zone with a 400 on `time_zone`. All instances of one installation must use the
same `limits.time_zone`. Changing `limits.time_zone` takes effect at the next restart, for the tenants
that have no zone of their own; it is an operator's decision about every such tenant, and the periods
of the new default count the whole usage of their range.

**A request belongs to the period it started in.** The period is that of the *start time* of the
request (`started_at` of its usage record), not of its admission and not of its end. A request that
starts at 23:59:50 and ends after midnight is reserved against, and settled into, the day it
started in. This is what makes a counter and a ledger total over the same period the same number.
(The two rates of per-minute limits keep counting by admission second.) Platform rules and the tenant's
own rules of one tenant share one calendar: the tenant's.

**Worked example: a 23-hour day.** With the zone `Europe/Rome`, the clocks move forward on
Sunday 28 March 2027. That day starts at 00:00 CET, which is 23:00 UTC on the 27th, and ends at
00:00 CEST, which is 22:00 UTC on the 28th: it lasts 23 hours. A `requests_per_day` rule of 1,000
refuses the 1,001st request that *started* between those two instants. A request that is refused at
12:00 local time (10:00 UTC) gets `Retry-After: 43200`, twelve hours, the time left to the end of
the day as the zone's clock sees it, not 14. A day of the other kind (25 hours, in October) works the
same way. A month starts where its first day starts.

**Setting the zone of a tenant.** A zone given when the tenant is created is in force at once.
Afterwards:

```
pandokos tenant create acme --time-zone Europe/Rome
pandokos limit time-zone get --tenant acme
pandokos limit time-zone set --tenant acme Asia/Tokyo         # or: --default
pandokos tenant update acme --time-zone ""                    # the platform: "" is the default
```

The platform sets it with `tenant:write` (`createTenant`, `updateTenant`), the tenant's administrator
with `tenant_setting:write` (`setTenantTimeZone`, `limit time-zone set`; before sprint 4.3 it was `limit:write`, and a key scoped to `limit:write` alone no longer sets it; `limit:read` reads it with
`getTenantTimeZone`). Every change is audited: `tenant.time_zone_update` in the tenant's log (detail
`from`, `to`, `in_force`, `effective_from`) and `tenant.update` in the platform's log. The resource has
`time_zone` (as set; empty for the default), `in_force` (the name of the zone in force now, the default
resolved) and `effective_from` (present while a change is pending); the tenant resource shows
`time_zone`, `time_zone_in_force` and `time_zone_effective_from`. Setting the value that is already set
changes and audits nothing.

**The change rule.** A zone does not change on the spot. A change made at time *t₀* takes effect at the
**start of the next month in the zone in force at *t₀***, for days and months alike. Until then nothing
changes. A second change before then replaces the pending zone and keeps the instant; setting the zone
in force again withdraws the pending change. A period is always a **whole** civil period of the zone in
force at the request's start, and its counter counts the whole range, including what was used in its
hours before the change took effect. So a change never frees a quota: it only moves where the next
boundaries fall. The database holds the shape of this (a change without a pending instant, an instant
in the past or more than 32 days away, or a replacement that moves the instant is refused).

The four consequences:

1. **A change waits** up to a month: until `effective_from` the old zone's days and months apply.
2. **The day in which the new zone takes over can refuse at once**, because its range began before the
   change and the usage of those hours counts.
3. **A `Retry-After` given just before the change** points at the instant of the change, where the new
   zone's period is tested afresh (the same holds for `X-Pandokos-Limit-Reset`).
4. **Threshold events** of both zones' periods can appear for the hours they share.

**Worked example: UTC to `Asia/Tokyo` on the 10th.** A tenant in UTC sets `Asia/Tokyo` on 10 November.
The change takes effect where the UTC month ends, at 1 December 00:00 UTC, which is 09:00 in Tokyo. Until
then days and months are UTC days and months and nothing changes. From that instant a `requests_per_day`
rule counts the Tokyo day of 1 December, which began at 15:00 UTC on 30 November, so the day **counts its
nine earlier hours** (15:00 to 24:00 UTC on 30 November, used under the old zone) and can be exhausted
by traffic of those hours; the next day, 2 December, begins at 15:00 UTC on 1 December and starts
empty. The monthly counter of December in Tokyo counts from the same 15:00 UTC.

## 5. Budgets

A budget is a `cost_per_day` or `cost_per_month` rule: an amount in one currency. Which budgets and
ceilings apply to a tenant is decided by its **active currency** ([ADR 0050](../adr/0050-quotas-and-budgets-periods-attribution-currencies.md),
amended in sprint 5.4; [ADR 0053](../adr/0053-tenant-price-lists-and-active-currency.md)).

- **The active currency.** Every tenant has one: `pricing.default_currency` (`USD` unless configured)
  until it chooses another ([catalogue guide](catalogue.md), section 7). Every cost of the tenant is
  computed in it, with the tenant's price list in that currency where it prices the model, otherwise
  with the catalogue's price when that is in that currency, otherwise unknown. Nothing is converted and
  amounts of different currencies are never added.
- **Only budgets and ceilings in the active currency apply.** A cost rule of the tenant, of a service
  account or of a key, and a ceiling of the platform, applies to a request only when its currency is the
  tenant's active currency at the request's start. A cost rule in another currency is **inactive**: it
  is not evaluated, holds no counter, records no event and never refuses. A request that only inactive
  cost rules match is `unlimited` ([section 15](#15-logs-and-troubleshooting)). Count and token rules
  do not depend on currencies.
- **Which rules are inactive.** Every rule resource has `active`: `false` for a cost rule whose currency
  is not the tenant's active currency now, `true` otherwise (on a platform ceiling the member is omitted
  instead of `false`, and the CLI prints it as `false`). `limit list --tenant T` shows the column,
  and so does `limit platform --tenant T` for the platform's ceilings that apply to the tenant. The
  platform's own list (`platform-limit list`) has no `active`: it compares the ceiling's currency with
  the tenant's `active_currency_in_force` on the `Tenant` resource.
- **One rule per currency.** A rule is one per subject, scope, metric **and currency**, so a budget in the
  active currency and one prepared in another coexist (409 `conflict` only for the same four). A
  ceiling of the platform for one tenant replaces the default ceiling of the same scope, metric and
  currency.
- **A budget can be written in any currency that has a list for its scope.** The currency is checked when
  the rule is written, on create and on update, with 400 on `currency`:

  | Rule | Currencies accepted |
  |---|---|
  | A rule of a tenant | those of the tenant's own prices and of the catalogue's prices, for the models of the scope the tenant can be given: a version in force now or starting later |
  | A ceiling of the platform for one tenant | the catalogue's for the models of the scope, plus the tenant's active currency and its pending one (read from the `Tenant` resource: the platform does not read the tenant's lists, and a currency the tenant made active has a list) |
  | A default ceiling | the catalogue's |

  The answer names the currencies, sorted (`must be a currency the prices of this scope use: EUR, USD`),
  or says `no model of this scope has a price`. Whether the currency is the active one is **not**
  checked: a budget in another currency is how a change of currency is prepared. Count metrics are not
  checked. The check is about the moment of the write: prices change afterwards.
- **Preparing a change of currency, step by step** (a tenant administrator; the example moves ACME from
  USD to EUR):

  1. Write the prices in the new currency for the models in use: `pandokos price-list set --tenant ACME
     --currency EUR --model openai/gpt-4o --rate input=2.3 --rate output=9.2`. Check with
     `pandokos price-list lists --tenant ACME` and `tenant-model list --tenant ACME` (`tenant_price` and
     `price_source` show what prices each model now).
  2. Write the budgets in the new currency next to the old ones: `pandokos limit create --tenant ACME
     --metric cost_per_month --amount 400 --currency EUR`. They are inactive (`limit list` says so) and
     count nothing until the change.
  3. Set the currency: `pandokos price-list currency set --tenant ACME EUR`. It takes effect at the start
     of the next month in the tenant's time zone, never at once; the answer gives `effective_from` and
     `inactive_limits`, the rules (of the tenant and of the platform) that will stop applying. Until then
     nothing changes. Setting the currency in force again withdraws the change.
  4. From that instant the EUR budgets start whole periods, counted from the ledger over their range like
     any counter: the costs recorded before are in the old currency and are not theirs. The USD rules stay
     and can be deleted afterwards.

  No budget stops applying in the middle of one of its periods and none starts with a period already
  running, so a change frees nothing of the period in progress.
- **What the platform's ceilings measure.** A ceiling in the tenant's active currency counts the cost
  at **the tenant's price**, one cost per request ([ADR 0048](../adr/0048-reserve-and-settle.md)): where
  the tenant's list prices the model, the tenant's price; otherwise the catalogue's. Under BYOK the tenant
  pays its provider; a tenant list that is wrong makes the tenant's own figures wrong.
- **A tenant can step out of the platform's cost ceilings by changing its currency.** From the next
  month the ceilings in the old currency are inactive for it. The platform sees the pending change on
  the `Tenant` resource (`active_currency`, `active_currency_in_force`, `active_currency_effective_from`;
  `pandokos tenant get`) and can write a ceiling for that tenant in the new currency before it takes effect
  (`platform-limit create --for-tenant T --currency EUR ...`, accepted for the tenant's active or pending
  currency). **Ceilings in requests and tokens do not depend on currencies and cannot be stepped out of**:
  a platform that needs a limit no tenant can leave sets it in tokens.
- **Catalogue prices are set by the platform, for every tenant** (imported or overridden); since sprint 5.4 a
  tenant can add prices of its own, which take precedence in its active currency, and cannot change the
  catalogue's. **A catalogue price in another currency.** An override in another currency
  ([catalogue guide](catalogue.md), section 6) prices only the tenants whose active currency it is in:
  for the others the model has no catalogue price, and a budget that applies meets
  `limits.on_unknown_price`. It marks the model so that imports stop updating it. It is right when the
  provider is really billed in that currency; for one tenant's currency, the tenant writes its own list.
- **Amounts.** An amount has at most six fraction digits and is at most 1,000,000,000. It is stored
  in millionths of the currency and counted with exact decimals. The `X-Pandokos-Limit` and
  `X-Pandokos-Limit-Remaining` headers of a budget are decimals with no trailing zeros (`250.5`, `0`).
- **Unknown prices at reservation.** The estimate of a request has a known cost only when the model has a
  price **in the tenant's active currency** that can price it: the tenant's list, or the catalogue's.
  `no_price` therefore means "no price in the active currency". If a budget or a ceiling that applies
  to the request cannot measure it, `limits.on_unknown_price` decides:

| Value | What happens |
|---|---|
| `refuse` (default) | The request is refused with 409 `no_price`: `Pandokos: a budget applies to this call and the model has no price the budget can be measured with.` There is no `Retry-After`, because waiting does not help. The usage record has outcome `rejected_limit` and `error_code` `no_price` |
| `allow` | The request is admitted and reserved against every other limit; the budgets that cannot measure it do not count it. The log line has `budget_unpriced: true` |

  A tenant with an active budget therefore cannot call a model without a price in its currency unless the
  operator says so. A model with no price in the active currency and no active budget or ceiling is
  served, and its cost is recorded as unknown. To see why a request met `no_price`, use the tenant's own
  tools: `tenant-model list` (`listTenantModels`: `price`, `tenant_price`, `price_source`: is there a price in the active
  currency?), `limit list` (`active`) and the capability check; the amounts are not in the operator's log
  by default (section 15).
- **Unknown price at settlement.** A request whose cost turns out unknown (a category of usage that
  has no rate) counts a cost of zero in a budget: a budget counts known costs only, exactly as the
  ledger total does.
- **The bound.** A budget is strict on what it reserved and bounded on what was spent
  ([section 10](#10-what-is-strict-bounded-and-best-effort)): the excess of one request over its
  reservation is at most the cost of a full context window of input and a full output at the highest
  rates of the price in force, minus what was reserved.
- **The capability check.** `checks.budget` of the tenant's capability check ([administration API
  reference](../reference/admin-api.md)) says whether a call would find a budget exhausted, computed
  from the ledger, which it reads with the worker database role inside the tenant's session, under
  row-level security (not with the admin role): `not_enforced` (no enforced quota or budget in force
  matches; inactive budgets do not count), `unpriced` (an active budget matches and the model has no
  price in the active currency), `exhausted` or `available`. `available` means "not
  exhausted"; a call whose estimate exceeds what is left is still refused. Requests in flight are
  not in that total.
- **How fast it applies.** A change of currency or of prices reaches a gateway within a second
  normally and within `cache.catalog_ttl` and `cache.limits_ttl` at most (30 s by default). If the two
  caches of one gateway disagree for that time, the request meets `limits.on_unknown_price`. A change
  of currency is known a month ahead unless it is made in the last minutes of a month.

## 6. Reserve and settle: the estimate

Pandokos has no tokenizer. Before the credential is opened it reserves an estimate against every
limit of the request in one atomic step: every limit is charged, or none is. When the request ends
the reservation is settled with the figures of the usage record, or released if the request was never
sent ([section 7](#7-how-a-request-ends)).

| Part | Rule |
|---|---|
| Input tokens | the bytes of the request body divided by four, rounded up (the same rule as an estimated usage record), at most the model's `context_window` when the catalogue has one. 0 for `messages.count_tokens` |
| Output tokens per choice | the caller's own output limit when it set one; else the model's `default_output_tokens` when it has one; else `limits.default_output_tokens` (default 4096); at most the model's `max_output_tokens` when the catalogue has one |
| Choices | `n` of a chat completion (at most 128); 1 for every other operation |
| Output tokens | per choice times choices; 0 for `embeddings` and `messages.count_tokens` |
| Reserved tokens | input plus output, at most 1,000,000,000,000 |
| Cost | the reserved tokens at the price in force when the request started; unknown when the model has no price. Budgets reserve it |

The caller's output limit is `max_completion_tokens`, else `max_tokens`, for `chat.completions`;
`max_output_tokens` for `responses`; `max_tokens` for `messages`. A member counts only when it is a
top-level member written as a plain positive integer of at most 12 digits (no sign, fraction or
exponent); the last occurrence decides. Anything else counts as "not set", so a request whose limit
Pandokos cannot read is reserved at the default.

**The default per model.** A model can carry `default_output_tokens` (1 to 1,000,000, written with
the model by a platform administrator, [catalogue guide](catalogue.md)): the output reserved for a
request that sets no limit of its own. It lets a model that usually answers briefly reserve less than
the installation-wide default, and one that answers long reserve more.

**What to tell callers:** set an output limit. A request with no limit reserves the default, which
is a guess; a request with a limit reserves exactly that (up to the model's maximum) and is
admitted more predictably. Callers that set a limit larger than needed hold more of the token limit
or budget than they use until they settle.

Two consequences follow. A body that is large in bytes and small in tokens (an inline image)
reserves more than it uses until the request settles, and the context window caps that. A token
limit below what the largest request reserves refuses that request for good ([section 14](#14-choosing-values)).

## 7. How a request ends

| End of the request | Limiter call | Requests | Tokens and cost | Slot |
|---|---|---|---|---|
| Refused before the limits (policy, invalid body, model) | none | not counted | none | none |
| Refused by a limit (`rate_limited`, `quota_exceeded`) or by `no_price` | reserve fails | not counted | none | none |
| Refused or failed when the credential is opened; the caller went away before the credential was opened | release | returned | returned | freed |
| 2xx, usage from the provider | settle | counted | actual | freed |
| 2xx, no usable usage (an estimated record) | settle | counted | the record's estimate | freed |
| Provider error (4xx, 5xx, its own 429) | settle | counted | 0 | freed |
| Provider unreachable, egress refusal, redirect, upstream timeout | settle | counted | 0 | freed |
| Caller disconnects mid-stream, provider aborts, `limits.max_request_duration`, forced shutdown | settle | counted | what the record holds: provider figures seen so far, else the estimate | freed |
| The caller goes away, or a panic happens, after the credential was opened but before the call | settle | counted | 0 | freed |
| `messages.count_tokens` | settle | counted | 0 | freed |
| Settle or release itself fails | recovered, logged at error | | | freed by expiry |

Settlement uses the figures of the usage record, whatever their source, so the ledger and the
counters agree. For the two rates, settle corrects the second the request was admitted in while that
second is still in the window; if it has left, nothing is corrected. For a period counter it
corrects the minute of the request's start. Exactly one of settle and release happens per
reservation, when the request finishes.

A reservation also has an expiry, `limits.max_request_duration` plus one minute, which no request
outlives. If one expires the instance releases it and logs `limit reservation expired` at level
error with the request ID and the tenant: that is a defect (a settlement that failed), not a slow
request.

## 8. The 429 and the 409

A request refused by a limit is answered 429 in the error shape of its API family. A rate or
concurrency limit answers `rate_limited`; a limit of a day or month answers `quota_exceeded`:

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
X-Pandokos-Limit-Scope: all
X-Pandokos-Limit: 60
X-Pandokos-Limit-Remaining: 0
X-Pandokos-Limit-Reset: 17

{"error":{"message":"Pandokos: a rate limit of Pandokos was reached; retry after the time given in Retry-After.","type":"pandokos_error","param":null,"code":"rate_limited"}}
```

A budget that is used up answers like this (the values are examples):

```
HTTP/1.1 429 Too Many Requests
Retry-After: 1036800
X-Pandokos-Error: quota_exceeded
X-Pandokos-Limit-Subject: tenant
X-Pandokos-Limit-Metric: cost_per_month
X-Pandokos-Limit-Scope: provider
X-Pandokos-Limit: 500
X-Pandokos-Limit-Remaining: 0.0125
X-Pandokos-Limit-Currency: USD
X-Pandokos-Limit-Reset: 1036800

{"error":{"message":"Pandokos: a quota or budget of Pandokos was reached; it resets at the time given in Retry-After.","type":"pandokos_error","param":null,"code":"quota_exceeded"}}
```

| Part | Value |
|---|---|
| Body | `openai`: `type` `pandokos_error`, `code` `rate_limited` or `quota_exceeded`. `anthropic`: `error.type` `rate_limit_error` for both. The messages are constants |
| `X-Pandokos-Error` | `rate_limited`, `quota_exceeded` or, for the 409, `no_price` |
| `Retry-After` | whole seconds. For `rate_limited`, 1 to 61: when a retry would fit; if several limits had no room, the longest. For `quota_exceeded`, the seconds to the end of the period, at least 1 |
| `X-Pandokos-Limit-Subject` | `tenant`, `service_account` or `key`: never an ID |
| `X-Pandokos-Limit-Metric` | one of the nine metrics |
| `X-Pandokos-Limit-Scope` | `all`, `provider` or `model`: how narrow the rule that refused is |
| `X-Pandokos-Limit` | the limit in force for that subject, scope and metric (a decimal in the currency for a budget) |
| `X-Pandokos-Limit-Remaining` | what is left of it now, 0 or more (for tokens it can be positive and smaller than the request) |
| `X-Pandokos-Limit-Currency` | the currency of a budget; absent otherwise |
| `X-Pandokos-Limit-Reset` | whole seconds until that counter is empty (rates) or the period ends (quotas and budgets); absent for `concurrent_requests` and when it is already empty |

When limits of both kinds have no room, the refusal is the first in the order subject `tenant`,
`service_account`, `key`, then scope `all`, `provider`, `model`, then the nine metrics, and its kind
decides the code. The headers are set on refusals only, never on a successful answer. When the
estimate of one request is by itself larger than the limit, waiting cannot help: the log line says
`estimate_over_limit`; `Retry-After` is 60 for a rate and the end of the period for a quota.

**The 409 `no_price`** (section 5) is answered with `X-Pandokos-Error: no_price` and no limit
header and no `Retry-After`; Anthropic callers see `invalid_request_error`, as for every 409.

**Telling it from a provider's 429.** A response with `X-Pandokos-Error` comes from Pandokos. A
provider's own 429 passes unchanged, with the provider's `x-ratelimit-*` and `retry-after` headers
and without `X-Pandokos-Error` and without the `X-Pandokos-Limit*` headers, and it counts as a
request that the provider answered with an error.

**What SDKs do.** The OpenAI and Anthropic SDKs treat both 429 codes as their rate-limit error: they
retry a small number of times by default, waiting for `Retry-After` when they find it short enough
and backing off by themselves otherwise, and then raise the error. A `Retry-After` of hours is not
waited for. The Go, Python and TypeScript SDK suites check this against a Pandokos 429.

When the rules of a tenant cannot be read and none are cached, the request is answered 503
`unavailable` with `Retry-After: 5`: a request whose limits cannot be verified is not forwarded.

## 9. Thresholds and events

A rule can carry up to four **thresholds**, percentages of its value (1 to 100). Two things are
recorded as **events**, once per rule and period, in the append-only table `limit_event`:

- `threshold_reached`: what was *used* passed a threshold. Thresholds follow what was consumed, not
  what is reserved, so an estimate that is returned at settlement does not raise an alarm.
- `exhausted`: an enforced period limit refused a request for the first time in the period. It is
  recorded for platform ceilings too, and the tenant sees it.

A rule that is **soft** (`enforce: false`) never refuses; it only records its thresholds, so it
needs at least one. The three metrics of sprint 5.1 are always enforced and take no thresholds.

An event holds the rule and its level (`tenant` or `platform`), the subject, scope, metric,
currency, the period, the rule's value and the value observed. It names no request, caller or
content. Recording is idempotent: the key is the tenant, the rule, the period start, the kind and
the percentage, so a restart, which finds the same threshold passed again, inserts nothing. The
gateway inserts events off the request path; if the database is unavailable it retries, and it
flushes what is left at shutdown within what remains of `ledger.shutdown_flush_timeout`. An event
can be lost only when the process dies between the crossing and the insert **and** the period ends
before the next start. An inserted row writes no log line: the event is the tenant's. A row the database refuses is logged as `limit event not recorded` with its kind, the SQLSTATE and the error class, and nothing of the tenant or the rule ([ADR 0055](../adr/0055-operational-telemetry-holds-no-tenant-data.md)). Read events with
`pandokos limit events` or `listLimitEvents` (newest first). Delivery to other systems (webhooks)
does not exist yet: sprint 6.4 adds it ([ADR 0014](../adr/0014-events-outbox.md)).

## 10. What is strict, bounded and best effort

| Limit | Guarantee |
|---|---|
| Requests per minute, concurrent requests | strict (5.1) |
| Requests per day or month | strict on what the counter sees |
| Tokens per minute, day or month; cost per day or month | strict on reserved amounts; bounded on actual ones (the bound below) |
| Any period limit, right after a rule is created, or with records that never reached the ledger | best effort for the cases of the table below |
| Any limit with several instances | per instance (until epic 8) |
| Thresholds and events | recorded once per rule and period; best effort across a crash at the end of a period |

What a counter holds, and so what it guarantees, depends on the situation
([section 11](#11-counters-and-the-ledger)):

| Situation | What the counter holds |
|---|---|
| Steady state, one instance | Exactly what was reserved and settled: the bound below |
| After a restart | The ledger, from the first request of each tenant. Requests of the previous process whose record was not written are not counted |
| A rule created in the middle of a period | The ledger at creation; requests in flight at that moment enter when the cut passes them, at most the horizon plus the interval later (17 m 30 s with the defaults) |
| Any discrepancy between memory and ledger | Gone one horizon and one interval after the request started; anything else within the hour (rebase) |
| Several instances | Each sees the others' usage through the ledger, the horizon plus the interval late. Not a supported configuration for limits until epic 8 |

**The bound.** For the rates, take any interval of 60 seconds; for a period, the period. Let *s* be
the last admission in it and *L* the limit. The amount that the requests admitted in the interval
used is at most *L* plus, for each of those requests still in flight at *s*, what it used above its
reservation. The reason: at *s* every request admitted in the interval is counted, the finished ones
at what they used and the others at their reservation, and the admission at *s* required that sum to
be at most *L*; afterwards it changes only when the requests still in flight settle. Three
corollaries:

| Metric | Guarantee |
|---|---|
| Requests (per minute, day or month), concurrent requests | **Strict.** At most *L* requests were admitted and not released in the interval; at every instant at most *L* are in flight |
| Tokens and cost | **Strict on reserved amounts, bounded on actual ones** |

- If no request uses more than it reserved, the limit is strict.
- The overshoot is at most *k* times *e*, where *k* is the number of requests in flight (at most the
  concurrency limit, when one is set) and *e* the most one request can exceed its reservation.
- Once a counter is over *L* nothing is admitted until the window has moved on or the period has
  ended, so an overshoot is not repeated inside it.

*e* for tokens is: nothing in output when the caller set a limit that the provider enforces;
otherwise, per choice, the model's `max_output_tokens` minus the reserved default. In input, text
whose tokens are shorter than four bytes and inputs referenced rather than inlined (an image by URL)
can exceed the estimate, by at most the model's `context_window` minus the reserved input. When the
catalogue has neither value for a model, Pandokos cannot state *e* for it and the provider's own
maximum applies. For **cost**, *e* is larger, because the estimate prices plain input and output at
the tier of the estimated input while the record can hold dearer categories (cache writes, audio)
and a higher tier: it is at most the cost of a full context window and a full output at the highest
rates of the price version in force, minus the reserved cost.

**Example.** A tenant has `tokens_per_minute` 100000 and `concurrent_requests` 4. Its callers set no
output limit; the default is 4096 and the model's maximum output is 16384, so *e* is 16384 minus 4096,
which is 12288 (taking the input as exact). In the worst minute, four requests in flight each
produce the maximum: the tokens used in that interval are at most 100000 + 4 x 12288 = 149152. With
no concurrency limit, *k* is not bounded by a rule and the same arithmetic has no ceiling: that is
why section 14 asks for one.

A changed rule applies to the counts already held: counters are not reset.

## 11. Counters and the ledger

The rate windows and the concurrency gauge live in memory only. The counter of a day or a month is
made of two parts: `base`, what the usage ledger holds for the requests of the period that started
before the tenant's **cut**, and `live`, what this instance reserved and settled since, by minute of
request start. PostgreSQL is the source of truth (ADR 0012); the counter follows the ledger.

- **When a counter is loaded.** On the first request of a tenant that needs a period counter, not
  at start-up. The request waits for the load, at most 5 seconds. A tenant gets its cut at that
  moment. A restart therefore costs one query per tenant and period when the tenant is next seen.
- **The cut and the horizon.** The cut is the minute before which every request has ended and its
  usage record is committed (or rejected by the database, or lost). It is `now` minus the **horizon**,
  held back by the oldest record this instance has handed to the ledger writer and not yet committed;
  it never moves back, and while the database is down it does not advance. The horizon is
  `limits.request_body_read_timeout` + `limits.max_request_duration` + 2 minutes: **12 m 30 s** with
  the defaults (30 s and 10 m), longer than any request lives from its start to the hand-over of its
  record. If you raise either timeout the horizon follows; there is no key for it.
- **The interval.** Every `limits.reconcile_interval` (default 5 minutes, 10 seconds to 1 hour) the
  worker folds the requests that the cut has passed into `base`, replacing what the instance counted
  by what the ledger holds, and once an hour it recomputes `base` from the ledger. Neither counts a
  request twice: a request lives in memory until the cut passes its minute, and then in the ledger.
- **What this means.** After a restart the counters equal the ledger. A discrepancy (a record the
  database rejected, lost at a crash, or written by another process) disappears one horizon plus one
  interval after the request started (17 m 30 s with the defaults), and anything else within the
  hour. Records that never reach the ledger are not counted. The counters read the ledger with the
  worker role in the tenant's session; the gateway role reads no usage record. A gateway process
  without the worker role cannot load period counters and needs the shared store of epic 8.
- A zone that is changed, or a rule created in the middle of a period, changes the boundaries or the
  set of counters; the table in section 10 says what the counter holds.

## 12. When counters cannot be verified

The loading of a period counter reads the database. `limits.on_store_failure` says what happens to a
request when that fails (and the counter is not there), or when the store of counters itself
returns an error:

| Value | What happens |
|---|---|
| `refuse` (default) | 503 `unavailable` with `Retry-After: 5`, usage record `internal_error`, nothing forwarded |
| `allow` | The request is admitted. After a failed load it is reserved against every limit whose counter exists and not against the others; after a failed reserve it holds no reservation. The log line has `limits_unverified: true`; `limit counters unavailable` is logged at warn, at most once per tenant every ten seconds. Start-up warns that the setting is in force |

- **Rules that cannot be loaded always refuse**, whatever the setting: a request whose limits are
  unknown is not forwarded.
- A failed load is not cached; the next request tries again. A fold or rebase that fails changes
  nothing and is retried at the next interval (`limit reconciliation failed`, warn).
- What `allow` admits is in the ledger, so the counters include it once they can be read again.
  `allow` trades the guarantee of a quota or budget during an outage for availability: a quota or
  budget can be exceeded while the counters are unavailable.

## 13. Managing rules

Rules are written through the administration API ([reference](../reference/admin-api.md)) and the
CLI; each command maps to one operation. A tenant administrator manages the tenant's rules with
`limit:read`, `limit:write` and `limit:delete`; a platform administrator manages ceilings with the
`platform_limit:*` permissions ([permissions](../reference/permissions.md)). A rule body may carry an optional
`reason` (`--reason`), recorded in the audit row of the change; a reason alone, without a change, is refused. Every change is audited
in the transaction of the change. A change reaches the gateway at once through a database
notification and at the latest after `cache.limits_ttl`; counters are not reset by it.

```
pandokos limit list|get|create|update|delete      --tenant T [RULE_ID]
pandokos limit platform --tenant T                 the platform ceilings that apply to the tenant
pandokos limit events   --tenant T [--kind K] [--rule ID]
pandokos platform-limit list|get|create|update|delete [RULE_ID]
```

`create` takes `--metric` (required), `--limit N` for a count or `--amount DECIMAL --currency CCC`
for a budget, `--subject tenant|service-account|key` with `--service-account ID_OR_NAME` or
`--key KEY_ID` (the tenant by default), `--provider`, `--model`, `--soft` (never refuse) and
`--threshold N` (repeatable); `platform-limit create` takes `--for-tenant T` (absent: the default for
every tenant). `update` takes `--limit`, `--amount`, `--soft`, `--enforce`, `--threshold` (replaces the
set) and `--no-thresholds`; the subject, scope, metric and currency never change: delete and create.

**A rate rule.** A key of a batch job may use 30,000 tokens a minute:

```
pandokos limit create --tenant acme --subject key --key 7f3a... --metric tokens_per_minute --limit 30000
```

**A monthly budget with thresholds 80 and 100.** 500 US dollars a month for the tenant, with events
when 80% and 100% have been used:

```
pandokos limit create --tenant acme --metric cost_per_month --amount 500 --currency USD \
    --threshold 80 --threshold 100
```

**A model-scoped daily quota.** Two million tokens a day on one model:

```
pandokos limit create --tenant acme --metric tokens_per_day --limit 2000000 \
    --provider openai --model gpt-4o
```

**A platform default concurrency.** Every tenant may have 50 requests in flight unless a row for the
tenant says otherwise:

```
pandokos platform-limit create --metric concurrent_requests --limit 50
pandokos platform-limit create --for-tenant acme --metric concurrent_requests --limit 200
```

**A soft rule.** Only record when a team passes half and nine tenths of 10,000 requests a day:

```
pandokos limit create --tenant acme --subject service-account --service-account batch \
    --metric requests_per_day --limit 10000 --soft --threshold 50 --threshold 90
pandokos limit events --tenant acme --kind threshold_reached
```

Rules are validated when written: a count metric takes `--limit` and no currency, a cost metric an
amount and a currency (and the currency must be one that has a price list for the scope, section 5); a subject must
exist in the tenant and must not be a revoked key (the answer is the same whether an ID is elsewhere or
nowhere); a tenant holds at most 5,000 rules (the 5,001st is refused with 409 `invalid_state`).
A rule is never left without its subject or its scope.

**Rules deleted with their subject.** The administration API deletes the rules of a subject before it
removes the subject, in one transaction, and writes one audit row `limit.delete` for each, in the
tenant's log, with the actor of that operation, the request ID, the `reason` when there is one, every
field of the rule and in `detail` the `cause` and the `cause_id`:

| Operation | `cause` | `cause_id` |
|---|---|---|
| revoke a virtual key | `key_revoked` | the key ID |
| delete a subject mapping | `subject_mapping_deleted` | the mapping ID |
| delete a trusted issuer (with its mappings) | `token_issuer_deleted` | the issuer ID |
| delete a platform-level issuer (every tenant) | `platform_issuer_deleted` | the issuer ID |

The row of the operation itself (`virtual_key.revoke`, `subject_mapping.delete`, `token_issuer.delete`,
`platform_issuer.delete`) gains `limit_rules_deleted`, the number. Rules deleted by the upgrade to
sprint 5.3 carry the actor name `migration 0011` and a `cause` that starts with `migration_`
([database guide](database.md), section 16). A service account that is disabled keeps its rules. The
deleted rules reach the gateway through the same notification as any deleted rule.

## 14. Choosing values

- **A token limit must not be below the largest request** that a caller makes: that request
  reserves its input plus its output and is refused every time, because no waiting makes room. Look
  at the largest requests in the tenant's usage records and at the output limits your callers set.
- **A budget must not be below the cost of the largest request**: such a request reserves its cost
  in full and is refused every time. The cost of a request reserved is its estimated tokens at the
  price in force; the bound of section 10 says how far one request can exceed it.
- **Set a concurrency limit next to a token limit, and next to a tight budget.** It bounds *k* in
  the inequality of section 10, so it bounds the overshoot, and with a tight budget a burst of
  requests in flight can overshoot by several full requests.
- **Ask callers to set an output limit**, so a request reserves what it is likely to use, or set
  the per-model default (`default_output_tokens`) for a model whose answers are usually short or long.
- A request limit and a concurrency limit are strict; size them by what the provider and your
  budget can take. Remember that limits multiply by the number of instances until the shared store
  exists: divide by the number of instances you run.
- A limit that is changed applies to the counts already held. Lowering it below the current count
  refuses new requests until the window has moved on, or the period has ended.
- Choose the active currency of a tenant once, as you do its zone: a change waits for the next month, and
  the budgets in the old currency stop applying then. Prepare the budgets in the new currency before it
  (section 5). A platform that sets cost ceilings should know that a tenant can leave them by changing its
  currency; set the ceiling that must hold in tokens.
- Choose the zone of a tenant once, where its days and months should turn; a change moves every
  boundary after it takes effect at the start of the next month (section 4). Leave `limits.time_zone` where the
  tenants without a zone of their own should turn. Keep `limits.on_unknown_price` at `refuse` unless unpriced models are expected to be
  called under a budget.

## 15. Logs and troubleshooting

The `request` line ([gateway guide](gateway.md), section 10) says how limits treated a request and
nothing of the tenant's rules ([ADR 0055](../adr/0055-operational-telemetry-holds-no-tenant-data.md)):

| Field | Present | Meaning |
|---|---|---|
| `limit_state` | every request line | how limits treated the request: `limited`, `unlimited`, `unverified` or `not_evaluated`, the same value as the usage record |
| `limits_unverified` | admitted with `limits.on_store_failure` `allow` | `true`: counters could not be verified |
| `error_code` | refused | `rate_limited`, `quota_exceeded` or `no_price` |

The rule that refused a request (subject, metric, scope, level, value, `Retry-After`), the tokens
reserved and the currencies are the tenant's: the caller reads them in the response headers
(`X-Pandokos-Limit-*`), the tenant in its usage records, limit events and rules. To troubleshoot a
refusal, ask the tenant for the `X-Pandokos-Request-Id` and the headers of the answer.

Other lines: `limit reservation expired` (error: `request_id`), `limit settle failed`
(error: `request_id`, and the panic type or the error), `limit rules unavailable` (warning),
`limit counters unavailable` (warning: `error_class`; once per tenant every ten seconds),
`limit reconciliation failed` (warning: `error_class`), `limit event not recorded` (error: kind,
SQLSTATE, error class) and `limit events not recorded` at shutdown (warning: the number left).
`unlimited traffic not recorded` (debug: `request_id`, `error_class`) tells that the recorder below
failed; a recorded row writes no line. At start-up `limits ready` states the default zone and the two
settings. No line names a tenant, a body, a header value, a credential or a subject.

| You see | Look at |
|---|---|
| 429 `rate_limited` on `requests_per_minute` (header `X-Pandokos-Limit-Metric`) | the caller's rate against the limit; the subject says whether it is the key, the service account or the tenant |
| 429 `rate_limited` on `tokens_per_minute` and a small `Remaining` | the output limit the callers set: without one every request reserves the default; an estimate larger than the limit cannot be cured by waiting |
| 429 `rate_limited` with `limit_metric` `concurrent_requests` | slow or hanging requests; `limits.max_request_duration` ends them |
| 429 `quota_exceeded` | the metric, scope and `Retry-After`: the quota or budget resets at the end of the period in the tenant's time zone; raise the rule or wait. `limit events --kind exhausted` shows when it was first reached |
| 409 `no_price` | the model has no price in the tenant's **active currency** while a budget or ceiling in it applies. With the tenant's tools: `tenant-model list` (`price`, `tenant_price`, `price_source`), `limit list` (`active`), `price-list currency get`. The tenant adds a price in its active currency, narrows the budget to a scope with prices, or the operator sets `limits.on_unknown_price` to `allow` |
| A budget that never refuses | `active` in `limit list`: its currency may not be the tenant's active currency (`price-list currency get`), or the change of currency has not taken effect yet (`effective_from`) |
| 503 `unavailable` and `limit rules unavailable` | the database: the rules of that tenant could not be read and none were cached |
| 503 `unavailable` and `limit counters unavailable` | the usage ledger could not be read to load a counter and `limits.on_store_failure` is `refuse` |
| `limit reservation expired` | a defect: report it with the request ID |
| A rule that seems ignored | the subject ID (a key ID, a service account ID, or for a token the mapping ID) and the scope, which the rule holds by identifier: a model recreated with the same name is another model. A rule lasts at most `cache.limits_ttl` in the gateway cache before it applies. A rule that has vanished: look for its `limit.delete` row and its `cause` |
| Quotas turn at the wrong hour | the tenant's time zone and a pending change: `pandokos limit time-zone get --tenant T` shows `in_force` and `effective_from` |
| `unlimited` in the usage list (`--limit-state unlimited`) | no rule matched that request: see the row of unlimited traffic below |
| A counter that seems too high or low after a restart | section 11: it equals the ledger; records that were never written are not counted |
| One `limit.unlimited_traffic` row | the first request of a tenant in a day that no rule covered: see below |
| `rejected_limit` in the usage list | the same refusals, as records without usage and without cost, with `error_code` `rate_limited`, `quota_exceeded` or `no_price` ([usage guide](usage.md)) |

**How a request was treated: `limit_state`.** Every usage record, and every `request` log line, says how
limits treated the request:

| `limit_state` | Meaning |
|---|---|
| `limited` | at least one rule matched and every matching limit was evaluated: the request was reserved against them or refused by one (429, or 409 `no_price`). A budget that `limits.on_unknown_price: allow` leaves out of the count was evaluated |
| `unlimited` | no rule of the platform, the tenant, the service account or the key matched the request for its provider and model |
| `unverified` | at least one matching limit could not be evaluated: the rules could not be read (503), or counters could not be loaded or reserved, so the request was refused (503) or, with `limits.on_store_failure: allow`, admitted without them |
| `not_evaluated` | the request ended before the limits were consulted (refused for its body, its model or a policy), or the gateway runs without a limiter |

Rows written before sprint 5.3 have none. `pandokos usage list --limit-state unlimited` lists the
requests nothing limited ([usage guide](usage.md)).

**Traffic nothing limited: `limit.unlimited_traffic`.** The first request of a tenant in a day of the
tenant (in its time zone) whose state is `unlimited` writes one audit row in the tenant's log and one in
the platform's, with `actor_type` `platform`, `actor_name` `gateway`, `resource_type` `tenant`, the tenant
as the resource and, in `detail`, `day` (`2026-11-18`), `time_zone`, and the `provider` and `model` of
that request. The row says that a tenant sent traffic that no rule limited, not how much: the usage
records say that. It is written once per tenant and day, whatever the number of instances, restarts or
repetitions (two unique indexes), by a database function the gateway role may execute, **off the request
path**: the request costs one map lookup, and a bounded queue and one writer do the rest. A write that
fails is tried again by a later request, at most once per tenant every ten seconds; the row of a day is
missing only if the database could not be written from the first unlimited request to the end of the
day. After a restart the first unlimited request of each tenant costs one call that inserts nothing.
Seeing the row for a tenant you meant to limit is the signal to write a rule.

## 16. What is not there yet

| Missing | Sprint |
|---|---|
| Counters shared by several instances, kept in Valkey and rebuilt from the ledger | epic 8 |
| Delivery of limit events to other systems (webhook, outbox) | 6.4 |
| Limit metrics for dashboards | 6.1 |
| A "used against limit" report, and a usage rollup that makes the ledger reads cheap | 6.2 |
| A cheaper path for budgets: a request with budget limits makes about 105 allocations per reserve and settle, against 4 for rate limits (measured by benchmark T-622; sprint 5.3 did not change the count); revisited with the 6.1 metrics and epic 8 | 6.1, epic 8 |

Tenant price lists, listed here before, were delivered in sprint 5.4 (section 5). Platform aggregates per tenant are not planned: usage is the tenant's ([usage guide](usage.md), section 10).
