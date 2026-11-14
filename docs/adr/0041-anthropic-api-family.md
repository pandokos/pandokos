# 0041. The anthropic API family

- Status: proposed
- Date: 2026-11-14

Refines [ADR 0009](0009-native-passthrough-by-api-family.md), [ADR 0010](0010-usage-extraction.md)
and [ADR 0020](0020-error-contract.md).

## Context

ADR 0009 makes the API family the unit of implementation and names `anthropic` second, with messages
and token counting. Its SDKs send the key differently from OpenAI's (and changed how recently), its
streams report input usage in the first event and output in the last, its error body has its own shape,
and a request that reaches no provider must still get an error its SDK can parse.

## Decision

- Operations `messages` (`POST /v1/messages`) and `messages.count_tokens` (`POST
  /v1/messages/count_tokens`). Token counting consumes nothing and is recorded without usage or cost.
- The caller's key is read from `X-Api-Key` or `Authorization: Bearer`; when both are present they must
  be equal. The provider's key is sent as `X-Api-Key`. `Anthropic-Version` and `Anthropic-Beta` are
  forwarded as sent and never added.
- Forwarded request headers: `Accept`, `Content-Type`, `User-Agent`, `Anthropic-Version`,
  `Anthropic-Beta`. Passed response headers: `Content-Type`, `Content-Length`, `Retry-After`,
  `Request-Id`, `X-Should-Retry`, `Anthropic-*`. The provider's request ID is `Request-Id`.
- The body is forwarded as received except the model alias; nothing is spliced, because the provider
  reports usage in every stream.
- Usage: input includes both cache counts; the 1-hour cache write is `cache_write_1h_tokens`; server
  tool requests go to the other units. In a stream, `message_start` reports the input side and each
  `message_delta` replaces the members it carries. A stream that ends after `message_start` is recorded
  with the provider's input and an estimated output.
- A Pandokos error is `{"type":"error","error":{"type":<the provider's type for the status>,
  "message":"Pandokos: …"}}` with the headers of ADR 0020; no new code.
- A request that reaches no family gets the shape of the first family that recognizes its path or a
  header only that family's clients send (`Anthropic-Version`), else the `openai` shape. Status, code and
  message do not depend on the choice.

## Consequences

- The stock Anthropic SDKs work with a base URL and a key; records can mix provider and estimated
  sources, which the ledger already allows.
- A key change in the SDKs (header or scheme) is absorbed by accepting both headers; the SDK suite
  detects the next one.
- Every family adapter has two more small methods (`Recognizes`, `UpstreamRequestID`).

## Alternatives considered

**`X-Api-Key` only.** What the API documented first; refuses the current SDKs' default.
**Estimating the whole record when the stream is cut.** Simpler, and discards an exact input figure.
**Always the OpenAI shape for unrouted requests.** An Anthropic SDK would report a parse failure.
