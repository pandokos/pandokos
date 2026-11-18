# 0042. OpenAI-compatible providers and the per-provider stream usage option

- Status: accepted
- Date: 2026-11-14

Amends [ADR 0024](0024-openai-stream-usage-option.md); refines [ADR 0009](0009-native-passthrough-by-api-family.md).

## Context

ADR 0009 says a provider that speaks an existing family is configuration, not code. Compatible servers
differ in small ways: where usage appears in a stream, extra usage members, and some refuse unknown
request members such as `stream_options`, which ADR 0024 adds to every chat stream that does not ask
for usage. CI can reach no provider and holds no key.

## Decision

- A compatible provider is a provider row of family `openai`, its endpoints (a base URL may carry a
  path; a self-hosted server is a private endpoint), models and the tenant's credential. No code is
  per vendor.
- Each provider has `stream_usage_option`: `add` (default; ADR 0024 unchanged) or `omit` (Pandokos never
  adds the option; streams whose caller did not ask for usage are read in observe mode and estimated
  when the server reports nothing). `omit` is refused for a provider of another family.
- CI proves the family against named compatibility profiles replayed by the fake provider. Real servers
  are checked by hand, and the result, with date and server version, is kept in the providers guide.

## Consequences

- An operator can serve a server that refuses the option without losing streaming.
- With `omit`, usage of such streams is estimated unless the caller asks for it; the record says so.
- The verification of real servers is documentation, not a test; it is repeated when an SDK or a
  provider's API changes.

## Alternatives considered

**One global switch.** Costs every other provider its exact stream usage.
**Per endpoint.** Splits one server's behaviour across its addresses for no gain.
**Retrying without the option after a refusal.** A retry (GW-11) and a second request the caller did not make.

## History

- 2026-11-18: accepted by the maintainer after review.
