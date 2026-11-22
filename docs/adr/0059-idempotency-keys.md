# 0059. Idempotency keys

- Status: proposed
- Date: 2026-11-22

Requirement API-05. Refines [ADR 0017](0017-admin-api-spec-first.md) where it names the create operations.

## Context

A client that loses the response of a `POST` that creates something cannot tell whether the resource
exists, and a retry creates a second one. API-05 asks that create operations accept an idempotency key.
The key must also be safe with one-time secrets, and a crash between the commit and the stored response
must not create a duplicate on retry.

## Decision

**D-16.** Every operation whose success status is 201 accepts the header `Idempotency-Key` (1 to 128
printable ASCII characters). Without it nothing changes.

- **Scope of a key**: the tenant of the path (or the platform), the principal (kind and ID) and the key.
  The row stores the operation and SHA-256(salt, operation, path, body) with a random salt, never the
  body.
- **State machine.** *Claim*, in the hook after authorization, in a transaction of the operation's
  scope: a new row `pending`, locked for 60 s. An existing row: another operation or request hash is
  `422 idempotency_key_reused`; a live lock is `409 idempotency_in_progress` with `Retry-After`; a stale
  lock without a commit mark is taken over (`attempt` + 1). *Commit mark*: `audit.RecordTenant` and
  `audit.RecordPlatform`, which every create operation calls inside its transaction, also set
  `committed_at`, the resource type and ID on the claim carried by the context, `WHERE attempt = $n`; no
  row updated is an error, so the transaction of a superseded attempt rolls back. The creation and its
  memory are one atomic fact, without a change to any handler. *Completion*, after the handler: on 2xx
  the status and body are stored **without the members that are shown once** (`key`, `secret`,
  `password`, `initial_password`); otherwise the row is deleted unless it carries the commit mark.
  *Replay*: a completed row answers with the stored status and body and `Idempotency-Replayed: true`;
  the handler does not run and nothing is audited. A row with the commit mark and no stored response
  (the process died between commit and completion) answers `409 idempotency_response_lost`: the
  resource exists and its ID is in the audit log.
- **Retention**: `idempotency.retention` (24 hours, 1 hour to 7 days), purged by the admin role.
- The create schemas make the one-time member optional, because a replay does not carry it.

## Consequences

- One more statement in the audit helpers for every create operation (the commit mark); every 201
  operation must write its success audit row inside its transaction, which a test proves.
- A retry after a lost response learns the resource's ID and can revoke what it cannot read, where an
  error would have hidden the ID.
- Idempotency rows are global with policies and are deleted with a tenant.

## Alternatives considered

**Storing the response after the handler only.** A crash between the commit and the store creates a
duplicate on retry.

**Storing full responses.** Secrets at rest.

**Natural keys only.** Names are not unique for keys and mappings.

## History

- 2026-11-22: proposed with sprint 4.2. Maintainer decision of 2026-11-21: a replayed create answers
  without the one-time secret (O-11).
