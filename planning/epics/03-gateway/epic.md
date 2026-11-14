---
id: EPIC-3
title: Gateway
status: in-progress
release: v0.1
depends_on: [EPIC-2]
---

# Epic 3 — Gateway

## Outcome

Unmodified OpenAI and Anthropic SDKs call models through Pandokos with a virtual key, streaming
included; the tenant's own credential is used; each call leaves one usage record with tokens and
estimated cost and one span. This completes v0.1 together with sprint 4.1.

## Decisions it implements

ADR 0009, 0010, 0013 (ledger), 0015 (traces), 0020.

## Sprints

### 3.1 Authenticated passthrough

- The request pipeline: route by provider name, authenticate from the family's native header,
  parse model and stream flag within a body limit, authorize the model, resolve endpoint and
  credential, forward.
- The `openai` family: endpoint allow-list, credential injection, error shape.
- Header allow-list; removal of inbound credentials and hop-by-hop headers.
- Egress guard: catalogue hosts only, no redirects, check of the dialled address, upstream TLS
  verification with optional private CA.
- The error contract with stable codes and the `x-pandokos-error` header.
- Request IDs; limits on body size and duration.
- Replaces the hard-wired route of sprint 1.1.
- Only chat completions on the allow-list; the shutdown sequence and its bounds (OPS-12).

Requirements: GW-01, GW-02, GW-05 to GW-12, GW-16 (size, duration), FND-01 and FND-02 (BYOK),
SEC-01, SEC-02, OPS-12.

Exit: a non-streaming call succeeds with a tenant's key and credential; a call for a model not
granted, to an unsupported path, or from a suspended tenant is refused with the documented code;
attempts to reach an internal address through a crafted endpoint or redirect fail.

### 3.2 Streaming, usage and ledger

- Streaming forwarder with the tee to the usage reader; time to first byte measured.
- Usage option injected into OpenAI chat streams when absent, and the extra event removed.
- Estimation when the provider reports nothing; value sources recorded.
- The usage ledger: partitioned table, insert-only role, batching writer, bounded buffer, flush on
  shutdown, readiness tied to the buffer.
- One record per request for every outcome, including refusals after authentication.
- Caller metadata headers recorded, bounded, never forwarded.
- Cancellation on client disconnect; draining on shutdown.
- OTLP traces: server span and provider span joined to the caller's trace.
- Fuzz tests for the stream reader.
- The `responses` and `embeddings` endpoints of the `openai` family, with their usage readers
  (moved from 3.1, whose allow-list holds chat completions only).

Requirements: GW-03, GW-04, GW-13, GW-14, USE-01 to USE-05 (append-only), OBS-01 (traces),
OBS-02, OBS-04, OBS-05, PRV-02; OPS-12 is completed in 3.1 and touched here for the ledger flush.

Exit: a streamed call delivers events without added buffering and produces one record with
provider-reported tokens and cost; a cancelled stream produces a record marked cancelled; with the
database stopped, records are buffered and then the instance reports not ready; no content appears
in the ledger, the logs or the spans.

### 3.3 Anthropic and OpenAI-compatible providers

- The `anthropic` family: endpoints, `x-api-key`, usage across stream events, error shape.
- OpenAI-compatible providers by configuration alone, verified with two public providers and one
  self-hosted server.
- The tenant capability endpoint: can this tenant call this provider and model now.
- SDK compatibility suite in CI: official OpenAI and Anthropic SDKs (Python and TypeScript)
  against a mock upstream that replays recorded responses.
- Docker Compose reference: one container plus PostgreSQL, with a quick-start guide.
- Documentation: base URLs per SDK, supported endpoints, what the proxy changes.

Requirements: CAT-02 (tier 1), API-07, OPS-02, and GW-01 to GW-14 for the second family.

Exit: the quick-start takes a new user from `docker compose up` to a streamed Anthropic and
OpenAI call with BYOK and a usage record; the compatibility suite is green.

## Risks

- Provider stream formats have edge cases (errors in mid-stream, keep-alive events, tool-call
  deltas). Recorded fixtures must cover them; the reader must fail soft.
- Removing the injected usage event must not disturb clients. If it proves fragile, the fallback
  is to leave the stream untouched and estimate, recorded as a decision.
