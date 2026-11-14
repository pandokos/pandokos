# 0009. Native passthrough organized by API family; no translation

- Status: accepted
- Date: 2026-11-11

## Context

Gateways in this category usually expose one unified API and translate it to each provider. That
makes switching models easy and puts a second interpretation of every provider feature in the
proxy: new parameters, tool calling, caching controls and reasoning options arrive late or
altered. The requirements choose transparency (principle 12, GW-01).

Many providers publish an endpoint that speaks the OpenAI wire protocol.

## Decision

**Requests are forwarded in the provider's own protocol, byte for byte except where stated.**

The unit of implementation is the **API family**, a wire protocol, not the provider. A family
defines:

- the endpoints Pandokos supports, as an allow-list (for the OpenAI family: chat completions,
  responses, embeddings; for Anthropic: messages and token counting);
- where the model name and stream flag are in a request;
- how the provider credential is attached;
- how usage is read from a response and from a stream ([ADR 0010](0010-usage-extraction.md));
- the shape of an error ([ADR 0020](0020-error-contract.md)).

A **provider** in the catalogue is a name, an API family and one or more endpoints. Adding a
provider that speaks an existing family is configuration, not code.

Initial families: `openai` and `anthropic`. Then `gemini`, and `azure-openai` as a variant of
`openai` with its own addressing and authentication. Bedrock needs request signing and a binary
event stream and comes last.

**Routing.** The first path segment is the provider's name in the catalogue; the remainder is the
native path: `/openai/v1/chat/completions`, `/anthropic/v1/messages`, `/groq/v1/chat/completions`.

**Unified endpoint.** `/v1/…` accepts the OpenAI protocol and picks the provider from the model
name. It can reach any provider registered with the `openai` family, which includes the
OpenAI-compatible endpoints that other vendors operate. Pandokos translates nothing.

**What the proxy does change**, exhaustively: it removes the inbound credential and hop-by-hop
headers, forwards only allow-listed request headers, attaches the provider credential, replaces a
model alias with the upstream model name when aliases are used, and may ask the provider to
include usage in a stream ([ADR 0010](0010-usage-extraction.md)). Nothing else.

**What it never does**: retry, fall back to another model or provider, choose between
credentials, cache responses.

Endpoints outside the allow-list are refused, because a call Pandokos cannot meter would bypass
limits and the ledger. Asynchronous endpoints (batches, files) are excluded until metering for
them is designed.

## Consequences

- Provider features work on the day the provider ships them, as long as they use a supported
  endpoint.
- Callers keep provider-specific code. Switching provider means switching SDK or using the
  provider's OpenAI-compatible endpoint. That is the caller's concern, by design.
- Each family is small and testable against recorded provider responses.
- A new endpoint needs a deliberate addition to an allow-list.

## Alternatives considered

**A unified API with translation.** The dominant design, and exactly the "second translation" the
product exists to avoid. It also makes the proxy responsible for semantic differences it cannot
hide.

**Opaque passthrough of any path.** Maximally transparent, but unmetered endpoints defeat budgets
and the ledger, and the attack surface is whatever the provider exposes.

## History


- 2026-11: sprint 2.2 reserves `healthz`, `readyz` and `v1` as provider names and stores the family per provider ([ADR 0035](0035-catalogue-and-model-access.md)).
