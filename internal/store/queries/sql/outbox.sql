-- Events for external systems (sprint 6.4, ADR 0070 to 0072). Statements marked (W) belong to the relay and the janitor,
-- (A) to the administration; the scope each runs in is in its header. No statement here writes outbox_event,
-- outbox_stream or platform_outbox_event: the database writes events.

-- (W, platform scope)
-- name: ClaimOutboxDestinations :many
SELECT c.tenant_id::uuid AS tenant_id, c.destination_id::uuid AS destination_id
  FROM pandokos.outbox_relay_claim(sqlc.arg(token)::uuid, sqlc.arg(lease_seconds)::integer, sqlc.arg(max)::integer) c;

-- (W, platform scope)
-- name: GetOutboxRelayState :one
SELECT s.destinations::bigint, s.failing::bigint, s.disabled::bigint, s.dead_letters::bigint,
       s.oldest_pending_seconds::double precision
  FROM pandokos.outbox_relay_state() s;

-- (W) the first statement of a round: it reads the head
-- name: GetEventDestinationForRelay :one
SELECT d.id, d.name, d.url, d.event_types, d.enabled, d.secret, d.secret_key_version, d.previous_secret,
       d.previous_secret_key_version, d.previous_secret_expires_at, d.cursor_seq, d.failures, d.next_attempt_at, d.last_error,
       d.test_event_id, (d.test_requested_at IS NOT NULL
                         AND (d.test_completed_at IS NULL OR d.test_completed_at < d.test_requested_at))::boolean AS test_pending,
       (SELECT t.external_reference FROM pandokos.tenant t WHERE t.id = d.tenant_id) AS external_reference,
       COALESCE((SELECT s.last_seq FROM pandokos.outbox_stream s WHERE s.tenant_id = d.tenant_id), 0)::bigint AS head_seq
  FROM pandokos.event_destination d
 WHERE d.id = sqlc.arg(id)::uuid AND d.lease_token = sqlc.arg(token)::uuid;

-- (W, A) after GetEventDestinationForRelay, never before
-- name: ListOutboxEventsAfter :many
SELECT o.seq, o.id, o.type, o.subject, o.occurred_at, o.recorded_at, o.data
  FROM pandokos.outbox_event o
 WHERE o.seq > sqlc.arg(after)::bigint
   AND (cardinality(sqlc.arg(types)::text[]) = 0 OR o.type = ANY (sqlc.arg(types)::text[]))
 ORDER BY o.seq
 LIMIT sqlc.arg(max)::integer;

-- (W)
-- name: ListReplayDeadLetters :many
SELECT l.seq, l.attempts, o.id, o.type, o.subject, o.occurred_at, o.data
  FROM pandokos.outbox_dead_letter l
  JOIN pandokos.outbox_event o ON o.tenant_id = l.tenant_id AND o.seq = l.seq
 WHERE l.destination_id = sqlc.arg(destination_id)::uuid AND l.state = 'replay'
 ORDER BY l.seq
 LIMIT sqlc.arg(max)::integer;

-- (W) 0 rows: the lease is not ours any more
-- name: SettleEventDestination :execrows
UPDATE pandokos.event_destination
   SET cursor_seq = GREATEST(cursor_seq, sqlc.arg(cursor_seq)::bigint),
       failures = sqlc.arg(failures)::integer,
       failing_since = CASE WHEN sqlc.arg(failures)::integer = 0 THEN NULL ELSE COALESCE(failing_since, now()) END,
       next_attempt_at = sqlc.arg(next_attempt_at)::timestamptz,
       last_success_at = COALESCE(sqlc.narg(last_success_at)::timestamptz, last_success_at),
       last_failure_at = COALESCE(sqlc.narg(last_failure_at)::timestamptz, last_failure_at),
       last_error = sqlc.arg(last_error)::text,
       last_status = sqlc.narg(last_status)::integer,
       test_completed_at = CASE WHEN sqlc.arg(test_outcome)::text <> '' THEN now() ELSE test_completed_at END,
       test_outcome = CASE WHEN sqlc.arg(test_outcome)::text <> '' THEN sqlc.arg(test_outcome)::text ELSE test_outcome END,
       test_status = CASE WHEN sqlc.arg(test_outcome)::text <> '' THEN sqlc.narg(test_status)::integer ELSE test_status END,
       lease_token = NULL, lease_until = NULL
 WHERE id = sqlc.arg(id)::uuid AND lease_token = sqlc.arg(token)::uuid;

-- (W) in the transaction of the settle, after it changed a row
-- name: ParkDeadLetter :exec
INSERT INTO pandokos.outbox_dead_letter (tenant_id, destination_id, seq, event_id, type, attempts, reason, status)
VALUES (pandokos.current_tenant_id(), sqlc.arg(destination_id)::uuid, sqlc.arg(seq)::bigint, sqlc.arg(event_id)::uuid,
        sqlc.arg(type)::text, sqlc.arg(attempts)::integer, sqlc.arg(reason)::text, sqlc.narg(status)::integer)
ON CONFLICT (tenant_id, destination_id, seq)
DO UPDATE SET state = 'dead', attempts = EXCLUDED.attempts, reason = EXCLUDED.reason, status = EXCLUDED.status, parked_at = now();

-- (W) a replay that failed
-- name: ReturnDeadLetter :exec
UPDATE pandokos.outbox_dead_letter
   SET state = 'dead', attempts = attempts + 1, reason = sqlc.arg(reason)::text, status = sqlc.narg(status)::integer, parked_at = now()
 WHERE destination_id = sqlc.arg(destination_id)::uuid AND seq = sqlc.arg(seq)::bigint AND state = 'replay';

-- (W) a replay that succeeded
-- name: DeleteDeadLetter :exec
DELETE FROM pandokos.outbox_dead_letter
 WHERE destination_id = sqlc.arg(destination_id)::uuid AND seq = sqlc.arg(seq)::bigint AND state = 'replay';

-- (W)
-- name: TenantOutboxRetention :one
SELECT r.events::bigint, r.expired::bigint, r.limit_events::bigint
  FROM pandokos.tenant_outbox_retention(sqlc.arg(event_hours)::integer, sqlc.arg(usage_months)::integer, sqlc.arg(batch)::integer) r;

-- (A)
-- name: CreateEventDestination :one
INSERT INTO pandokos.event_destination (tenant_id, id, name, description, url, event_types, enabled, secret, secret_key_version)
VALUES (pandokos.current_tenant_id(), sqlc.arg(id)::uuid, sqlc.arg(name), sqlc.arg(description), sqlc.arg(url), sqlc.arg(event_types)::text[],
        sqlc.arg(enabled), sqlc.arg(secret), sqlc.arg(secret_key_version))
RETURNING id;

-- (A) also ListEventDestinations :many with the keyset of the other lists
-- name: GetEventDestination :one
SELECT d.id, d.tenant_id, d.name, d.description, d.url, d.event_types, d.enabled, d.secret_created_at,
       d.previous_secret_expires_at, d.created_at, d.updated_at, d.cursor_seq, d.failures, d.failing_since,
       d.next_attempt_at, d.last_success_at, d.last_failure_at, d.last_error, d.last_status, d.expired_events,
       d.test_requested_at, d.test_event_id, d.test_completed_at, d.test_outcome, d.test_status,
       COALESCE((SELECT s.last_seq FROM pandokos.outbox_stream s WHERE s.tenant_id = d.tenant_id), 0)::bigint AS head_seq,
       (SELECT count(*) FROM pandokos.outbox_dead_letter l WHERE l.destination_id = d.id AND l.state = 'dead')::bigint AS dead_letters
  FROM pandokos.event_destination d
 WHERE d.id = sqlc.arg(id)::uuid;

-- (A) enabling (false to true) also clears the failure state
-- name: UpdateEventDestination :execrows
UPDATE pandokos.event_destination d
   SET name = COALESCE(sqlc.narg(name), d.name), description = COALESCE(sqlc.narg(description), d.description),
       url = COALESCE(sqlc.narg(url), d.url), event_types = COALESCE(sqlc.narg(event_types)::text[], d.event_types),
       enabled = COALESCE(sqlc.narg(enabled), d.enabled), updated_at = now(),
       failures = CASE WHEN sqlc.narg(enabled)::boolean AND NOT d.enabled THEN 0 ELSE d.failures END,
       failing_since = CASE WHEN sqlc.narg(enabled)::boolean AND NOT d.enabled THEN NULL ELSE d.failing_since END,
       next_attempt_at = CASE WHEN sqlc.narg(enabled)::boolean AND NOT d.enabled THEN now() ELSE d.next_attempt_at END
 WHERE d.id = sqlc.arg(id)::uuid;

-- (A) the trigger keeps the replaced secret when an expiry is given
-- name: RotateEventDestinationSecret :execrows
UPDATE pandokos.event_destination
   SET secret = sqlc.arg(secret), secret_key_version = sqlc.arg(secret_key_version), updated_at = now(),
       previous_secret_expires_at = CASE WHEN sqlc.arg(previous_seconds)::integer > 0
                                         THEN now() + make_interval(secs => sqlc.arg(previous_seconds)::integer) END
 WHERE id = sqlc.arg(id)::uuid;

-- (A)
-- name: RequestEventDestinationTest :execrows
UPDATE pandokos.event_destination
   SET test_requested_at = now(), test_event_id = sqlc.arg(event_id)::uuid
 WHERE id = sqlc.arg(id)::uuid;

-- (A) since is NULL or after is NULL, never both
-- name: RewindEventDestination :one
UPDATE pandokos.event_destination d
   SET cursor_seq = LEAST(d.cursor_seq, COALESCE(sqlc.narg(after)::bigint,
           (SELECT o.seq FROM pandokos.outbox_event o WHERE o.recorded_at < sqlc.narg(since)::timestamptz
             ORDER BY o.seq DESC LIMIT 1), 0)),
       failures = 0, failing_since = NULL, next_attempt_at = now(), lease_token = NULL, lease_until = NULL
 WHERE d.id = sqlc.arg(id)::uuid
RETURNING d.cursor_seq;

-- (A)
-- name: DeleteEventDestination :execrows
DELETE FROM pandokos.event_destination WHERE id = sqlc.arg(id)::uuid;

-- (A) keyset on seq
-- name: ListEventDeadLetters :many
SELECT l.seq, l.event_id, l.type, l.state, l.attempts, l.reason, l.status, l.parked_at, l.replay_requested_at
  FROM pandokos.outbox_dead_letter l
 WHERE l.destination_id = sqlc.arg(destination_id)::uuid AND l.seq > sqlc.arg(after)::bigint
 ORDER BY l.seq LIMIT sqlc.arg(max)::integer;

-- (A) an empty list: all of the destination
-- name: ReplayEventDeadLetters :execrows
UPDATE pandokos.outbox_dead_letter
   SET state = 'replay', replay_requested_at = now()
 WHERE destination_id = sqlc.arg(destination_id)::uuid AND state = 'dead'
   AND (cardinality(sqlc.arg(seqs)::bigint[]) = 0 OR seq = ANY (sqlc.arg(seqs)::bigint[]));

-- (A, platform scope)
-- name: ListPlatformOutboxEventsAfter :many
SELECT e.seq, e.id, e.type, e.subject, e.occurred_at, e.tenant_id, e.data,
       COALESCE((SELECT t.external_reference FROM pandokos.tenant t WHERE t.id = e.tenant_id), '') AS external_reference
  FROM pandokos.platform_outbox_event e
 WHERE e.seq > sqlc.arg(after)::bigint
   AND (cardinality(sqlc.arg(types)::text[]) = 0 OR e.type = ANY (sqlc.arg(types)::text[]))
 ORDER BY e.seq LIMIT sqlc.arg(max)::integer;
