# 0011. Prices: versioned in the database, seeded from an open dataset

- Status: accepted
- Date: 2026-11-11

## Context

Every call must be priced (principle 11) with the price in force when it was made (CAT-08).
Provider prices change often, differ by token category and sometimes by context size or time of
day. Operators need to correct or override them. Maintaining a price list by hand in this project
would be a permanent chore.

## Decision

- Prices live in the database as **price versions**: model, validity start, currency, and a price
  per million tokens for each token category, plus optional tiers. A cost calculation picks the
  version valid at the request time and records its ID in the usage record.
- Pandokos **imports** prices from the open `genai-prices` dataset, which is published as a single
  JSON document under a permissive licence. A release embeds a snapshot; an operator can import a
  newer file with the CLI or enable a periodic refresh by the worker. Refresh is off by default,
  because it is an outbound call the operator has not asked for.
- An import never edits existing versions. A changed price creates a new version starting at the
  import time, with an audit record.
- **Operator overrides** are price versions marked as such; imports do not replace them. Private
  and self-hosted models get their prices this way, including zero.
- A model without a price is usable under BYOK, with cost recorded as unknown, and blocked for
  platform-funded use unless the operator allows it.
- All amounts use decimal arithmetic. Costs are stored with enough precision for per-token prices
  and are rounded only for display.

## Consequences

- Costs are estimates. Under BYOK they are informational; under platform-funded use they are the
  operator's stated basis for charging, not the provider's invoice. The documentation is explicit
  about the difference.
- Pandokos depends on the format of an external dataset. The importer is small and versioned; a
  format change breaks imports, not running installations.
- Reconstructing a past cost is always possible, because versions are immutable.

## Alternatives considered

**A price list maintained in this repository.** Full control, constant upkeep.

**Calling provider billing APIs.** Not available uniformly, not per request, and requires
privileged provider credentials.

**Leaving cost to downstream systems.** Budgets in money (LIM-03) need the cost at request time.

## History


- 2026-11: sprint 2.2 implements it with the v2 format of the dataset, which replaced the frozen v1 files ([ADR 0036](0036-prices-costs-and-importer.md)).
