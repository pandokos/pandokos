# 0010. Usage extraction from responses and streams

- Status: accepted
- Date: 2026-11-11

## Context

Limits, the ledger and billing all depend on token counts that only the provider knows exactly.
Each API family reports them differently: in different fields, with cached tokens counted inside
or outside the input total, and, when streaming, in different events. One family reports usage in
a stream only when the request asks for it. The forwarder must not delay or alter the stream
(GW-03) and must not keep content (principle 8).

## Decision

Each API family provides a **usage reader** with two entry points: one for a complete JSON
response, one for a stream of server-sent events.

- The forwarder tees the response body: bytes go to the caller as they arrive and, in parallel, to
  the reader. The reader works incrementally and keeps only counters, never content. A slow or
  failing reader cannot hold back the stream; if it fails, the request is recorded with estimated
  usage.
- Readers normalize into one internal form: input tokens, output tokens, cache-read tokens,
  cache-write tokens, reasoning tokens, plus a map for provider-specific categories. The
  normalization rule for each family is documented, in particular whether cached tokens are part
  of the input count. The original provider values are stored alongside.
- **Streams in the OpenAI chat protocol**: when the request does not ask for usage, Pandokos adds
  the option that makes the provider send it, and removes the resulting usage-only event from the
  stream so the caller sees exactly what it would have seen. This is the only body modification
  the proxy makes, and it is documented (GW-14).
- When the provider reports nothing (an error before generation, a cancelled stream, a server that
  does not implement usage), Pandokos **estimates**: input from the request size, output from the
  bytes streamed so far. The record marks each value as `provider` or `estimated`.
- The same request-side estimate is what the limiter reserves before forwarding
  ([ADR 0012](0012-limits-and-budget-reservation.md)): input estimated from the body size, output
  from the request's output-token limit or a per-model default.

Readers are tested against recorded responses from each provider, covering every supported
endpoint in streaming and non-streaming form, and are fuzzed, since they parse data from the
network.

## Consequences

- Knowledge of provider response formats lives in this project and must follow provider changes.
  It is a few fields per family and is isolated behind one interface.
- Estimates are approximate by nature. They are flagged, and platform-funded billing can be
  configured to treat estimated records separately.
- No tokenizer is embedded: exact pre-counting would add large dependencies per model family for
  a number the provider returns anyway.

## Alternatives considered

**Using an existing extraction library.** The best-maintained one is a Python package; its Go
counterpart documents pricing, not extraction, and neither addresses streams.

**Buffering the whole response to parse it.** Breaks streaming.

**Counting tokens locally with tokenizers.** Heavy, model-specific and still not what the provider
bills.

**Trusting usage declared by the caller.** The caller is the party being limited.

## History

- 2026-11: sprint 3.2 estimates only for a 2xx answer without readable usage, stores the provider's counters as a flat map of integers, and makes the forwarder independent of the reader's output when nothing is removed ([ADR 0039](0039-usage-record-admission-and-ledger-writer.md)).
