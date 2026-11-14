# 0036. Prices, costs and the price importer

- Status: proposed
- Date: 2026-11-14

Refines [ADR 0011](0011-pricing-source.md).

## Context

ADR 0011 fixes versioned prices imported from `genai-prices`, operator overrides and decimal
arithmetic. Building it needs a storage format, a cost formula over the usage of ADR 0010, a
mapping from the dataset, whose v1 files are frozen upstream, and rules for imports at start and
by refresh.

## Decision

1. A price version belongs to a model, starts at `valid_from` (never in the past), has a
   currency and rates per million units of a category (`input`, `output`, `cache_read`,
   `cache_write`, `cache_write_1h`, `input_audio`, `output_audio`), with optional tiers whose
   threshold is compared with the request's input tokens and applies to every unit (cliff).
   Versions never change; one that has not started may be deleted. The version in force at t
   has the greatest `valid_from ≤ t`.
2. Amounts are exact decimals (`math/big`; `numeric(24,12)` for rates; decimal text in SQL and
   JSON); costs are not rounded except for display. A cost carries its currency and is unknown,
   with a reason, when no version is in force or a category with units has no rate after the
   documented fallbacks.
3. The importer reads the `genai-prices` v2 format, prices catalogue models only by applying
   the dataset's match clauses to their upstream identifiers, imports the price in force today,
   and reports per model what it cannot represent (scheduled and time-of-day prices, price
   variants, other units). It never creates models. A changed price is a new version starting at
   the import, with an audit record; a model with an operator override is left alone.
4. A release embeds the v2 file of a pinned commit with its licence; `serve` imports it once per
   digest and never over a newer file or download. The download is off by default, goes to an
   operator-configured `https` URL without proxy or redirects, bounded in size and time, and
   runs with the worker role.

## Consequences

Past costs are reproducible from immutable versions. Discounted tiers and time-of-day prices are
priced at the standard rate until represented. A format change upstream breaks imports, not
installations.

## Alternatives considered

Floating point or a decimal dependency; prices in a JSON column; backdated versions;
importing the v1 files (frozen); creating models from the dataset (its identifiers include
prefixes and pseudo-models); importing scheduled prices as future versions (upstream withdraws
them).
