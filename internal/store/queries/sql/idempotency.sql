-- Idempotency keys of create operations (sprint 4.2, §5.11). Every statement runs in the scope of
-- the operation: a tenant's session for the operations under /v1/tenants/{tenant}, platform scope
-- otherwise.

-- name: ClaimIdempotencyKey :one
-- In the scope of the operation. No row returned: the key exists (read it with GetIdempotencyKey).
INSERT INTO pandokos.idempotency_key (tenant_id, principal_kind, principal_id, key, operation, salt, request_hash, locked_until, expires_at)
VALUES (sqlc.narg(tenant_id), $1, $2, $3, $4, $5, $6, now() + interval '60 seconds', now() + sqlc.arg(retention)::interval)
ON CONFLICT DO NOTHING
RETURNING id::text, attempt;

-- name: GetIdempotencyKey :one
SELECT id::text, operation, salt, request_hash, attempt, locked_until > now() AS locked,
       (committed_at IS NOT NULL)::boolean AS committed, (completed_at IS NOT NULL)::boolean AS completed,
       response_status, COALESCE(response_body::text, '')::text AS response_body, expires_at <= now() AS expired
  FROM pandokos.idempotency_key
 WHERE COALESCE(tenant_id, '00000000-0000-0000-0000-000000000000'::uuid)
       = COALESCE(sqlc.narg(tenant_id)::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
   AND principal_kind = $1 AND principal_id = $2 AND key = $3
   FOR UPDATE;

-- name: TakeOverIdempotencyKey :one
UPDATE pandokos.idempotency_key SET attempt = attempt + 1, locked_until = now() + interval '60 seconds'
 WHERE id = $1 AND committed_at IS NULL AND locked_until <= now()
RETURNING attempt;

-- name: MarkIdempotencyKeyCommitted :execrows
-- In the transaction of the create. Zero rows: the attempt was superseded; the caller fails.
UPDATE pandokos.idempotency_key SET committed_at = now(), resource_type = $3, resource_id = $4
 WHERE id = $1 AND attempt = $2 AND committed_at IS NULL;

-- name: CompleteIdempotencyKey :exec
UPDATE pandokos.idempotency_key SET response_status = $2, response_body = sqlc.arg(response_body)::text::jsonb, completed_at = now()
 WHERE id = $1 AND committed_at IS NOT NULL AND completed_at IS NULL;

-- name: ReleaseIdempotencyKey :exec
DELETE FROM pandokos.idempotency_key WHERE id = $1 AND attempt = $2 AND committed_at IS NULL;

-- name: PurgeIdempotencyKeys :execrows
DELETE FROM pandokos.idempotency_key WHERE expires_at <= now();
