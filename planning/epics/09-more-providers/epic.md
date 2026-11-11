---
id: EPIC-9
title: More providers
status: todo
release: v0.4
depends_on: [EPIC-3]
---

# Epic 9 — More providers

## Outcome

Gemini and Azure OpenAI are supported natively, and a unified OpenAI-compatible endpoint routes by
model. This epic depends only on the gateway and can be brought forward if an adopter needs it.

## Decisions it implements

ADR 0009, 0010.

## Sprints

### 9.1 Gemini and the unified endpoint

- The `gemini` family: endpoints, `x-goog-api-key`, model in the path, usage in responses and
  streams, error shape.
- Model aliases.
- `/v1` unified endpoint: OpenAI protocol, provider chosen from the model, limited to providers
  registered with the `openai` family.
- Compatibility tests with the official Google SDK.

Requirements: CAT-02 (tier 2), CAT-07, GW-15.

### 9.2 Azure OpenAI

- The Azure variant of the OpenAI family: resource and deployment addressing, API versions, key
  header.
- Tenant-supplied endpoints for providers that declare an allowed host pattern, subject to the
  egress guard.
- Compatibility tests with the Azure mode of the OpenAI SDK.

Requirements: CAT-02 (tier 2), CAT-03 (tenant endpoints).

Exit for the epic: both providers pass the compatibility suite in streaming and non-streaming
form with correct usage records.

## Risks

- A tenant-supplied host is the one place where a tenant influences the destination. The host
  pattern, the egress guard and a review of this sprint by a second person are the controls.
