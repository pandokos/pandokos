---
id: EPIC-7
title: Platform-funded access and billing
status: todo
release: v0.3
depends_on: [EPIC-5, EPIC-6]
---

# Epic 7 — Platform-funded access and billing

## Outcome

An operator offers models paid with its own credentials, capped by budgets, and receives reliable
billable events in its billing system.

## Decisions it implements

ADR 0014, and the platform side of 0008 and 0011. It also settles the open question on a durable
intent record for platform-funded calls.

## Sprints

### 7.1 Platform credentials and funding modes

- Platform-owned credentials in the vault under a platform data key.
- Funding mode per tenant and provider; deterministic resolution; no fallback between modes.
- Enabling platform-funded access per provider, model and tenant.
- A budget is required for platform-funded requests.
- Rating rule: percentage markup or per-model price, versioned; upstream cost, rule version and
  chargeable amount stored apart.
- Decision and implementation of the durable intent record.

Requirements: VLT-02 (platform), FND-01, FND-03 to FND-05.

Exit: a tenant calls a platform-funded model and no API or error reveals the credential; without
a budget the call is refused; a tenant set to BYOK without a credential is refused and never
served with the platform's key.

### 7.2 Outbox and delivery

- Outbox table written with the usage record; relay in the worker with claiming, backoff,
  dead-letter state and replay.
- CloudEvents format and versioned event types; external customer reference per tenant.
- Signed webhook destination; pull export of events.
- Receiver example that shows idempotent handling.
- Failure tests: worker crash in mid-delivery, receiver outage, duplicate delivery.

Requirements: BIL-01 to BIL-04, BIL-07.

Exit: across injected crashes and outages, an idempotent receiver ends with exactly one entry per
platform-funded call and none for BYOK calls.

### 7.3 Corrections and notifications

- Usage corrections and the corresponding correction and void events.
- Events for limit thresholds and exhaustion.
- Tenant data export in a documented format.

Requirements: USE-05 (corrections), BIL-06, LIM-13, TEN-12.

Exit: a correction changes reports and reaches the receiver as a separate event that references
the original; a tenant export can be read with the published schema.

## Risks

- Charging on estimated usage is contentious. Records with estimated values are flagged in events
  so the operator's billing can treat them by its own policy.
