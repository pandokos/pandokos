-- Audit. Inserts (InsertAuditEvent, InsertPlatformAuditEvent, RecordAuthFailure) and, since 0015,
-- reads: a tenant's session reads its log and the head of its chain, the platform scope of the admin
-- role the platform's. detail is read as text: its canonical encoding is computed from it (ADR 0065).
-- detail is passed to the inserts as JSON text and cast: the driver sends text parameters (ADR 0027).

-- name: InsertAuditEvent :exec
INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_id, actor_name, key_id, action, resource_type,
                                  resource_id, outcome, request_id, reason, detail, support_action_id)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(actor_type)::text, sqlc.arg(actor_id)::text, sqlc.arg(actor_name)::text,
        sqlc.arg(key_id)::text, sqlc.arg(action)::text, sqlc.arg(resource_type)::text, sqlc.arg(resource_id)::text,
        sqlc.arg(outcome)::text, sqlc.arg(request_id)::text, sqlc.arg(reason)::text, sqlc.arg(detail)::text::jsonb,
        sqlc.narg(support_action_id)::uuid);

-- name: InsertPlatformAuditEvent :exec
INSERT INTO pandokos.platform_audit_event (actor_type, actor_id, actor_name, key_id, action, resource_type,
                                           resource_id, outcome, request_id, reason, detail)
VALUES (sqlc.arg(actor_type)::text, sqlc.arg(actor_id)::text, sqlc.arg(actor_name)::text,
        sqlc.arg(key_id)::text, sqlc.arg(action)::text, sqlc.arg(resource_type)::text, sqlc.arg(resource_id)::text,
        sqlc.arg(outcome)::text, sqlc.arg(request_id)::text, sqlc.arg(reason)::text, sqlc.arg(detail)::text::jsonb);

-- name: RecordAuthFailure :exec
SELECT pandokos.record_auth_failure(sqlc.arg(key_id)::text, sqlc.arg(reason)::text, sqlc.arg(request_id)::text);

-- name: ListAuditEvents :many
SELECT e.seq, e.id, e.occurred_at, e.actor_type, e.actor_id, e.actor_name, e.key_id, e.action, e.resource_type,
       e.resource_id, e.outcome, e.request_id, e.reason, COALESCE(e.support_action_id::text, '')::text AS support_action_id,
       e.detail::text AS detail, e.prev_hash, e.hash
  FROM pandokos.audit_event e
 WHERE e.seq < sqlc.arg(before_seq)::bigint
   AND (sqlc.narg(from_at)::timestamptz IS NULL OR e.occurred_at >= sqlc.narg(from_at)::timestamptz)
   AND (sqlc.narg(to_at)::timestamptz IS NULL OR e.occurred_at < sqlc.narg(to_at)::timestamptz)
   AND (sqlc.arg(action)::text = '' OR e.action = sqlc.arg(action)::text
        OR (right(sqlc.arg(action)::text, 1) = '.' AND starts_with(e.action, sqlc.arg(action)::text)))
   AND (sqlc.arg(resource_type)::text = '' OR e.resource_type = sqlc.arg(resource_type)::text)
   AND (sqlc.arg(resource_id)::text = '' OR e.resource_id = sqlc.arg(resource_id)::text)
   AND (sqlc.arg(actor_type)::text = '' OR e.actor_type = sqlc.arg(actor_type)::text)
   AND (sqlc.arg(actor_id)::text = '' OR e.actor_id = sqlc.arg(actor_id)::text)
   AND (sqlc.arg(outcome)::text = '' OR e.outcome = sqlc.arg(outcome)::text)
   AND (sqlc.arg(request_id)::text = '' OR e.request_id = sqlc.arg(request_id)::text)
   AND (sqlc.arg(support_action_id)::text = '' OR e.support_action_id = sqlc.arg(support_action_id)::text::uuid)
 ORDER BY e.seq DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: ExportAuditEvents :many
SELECT e.seq, e.id, e.occurred_at, e.actor_type, e.actor_id, e.actor_name, e.key_id, e.action, e.resource_type,
       e.resource_id, e.outcome, e.request_id, e.reason, COALESCE(e.support_action_id::text, '')::text AS support_action_id,
       e.detail::text AS detail, e.prev_hash, e.hash
  FROM pandokos.audit_event e
 WHERE e.seq > sqlc.arg(after_seq)::bigint AND e.seq <= sqlc.arg(to_seq)::bigint
 ORDER BY e.seq
 LIMIT sqlc.arg(page_size)::integer;

-- name: GetAuditHead :one
SELECT c.seq, c.hash, c.anchor_seq, c.anchor_hash FROM pandokos.audit_chain c
 WHERE c.tenant_id = pandokos.current_tenant_id();

-- name: ListPlatformAuditEvents :many
SELECT e.seq, e.id, e.occurred_at, e.actor_type, e.actor_id, e.actor_name, e.key_id, e.action, e.resource_type,
       e.resource_id, e.outcome, e.request_id, e.reason, e.detail::text AS detail, e.prev_hash, e.hash
  FROM pandokos.platform_audit_event e
 WHERE e.seq < sqlc.arg(before_seq)::bigint
   AND (sqlc.narg(from_at)::timestamptz IS NULL OR e.occurred_at >= sqlc.narg(from_at)::timestamptz)
   AND (sqlc.narg(to_at)::timestamptz IS NULL OR e.occurred_at < sqlc.narg(to_at)::timestamptz)
   AND (sqlc.arg(action)::text = '' OR e.action = sqlc.arg(action)::text
        OR (right(sqlc.arg(action)::text, 1) = '.' AND starts_with(e.action, sqlc.arg(action)::text)))
   AND (sqlc.arg(resource_type)::text = '' OR e.resource_type = sqlc.arg(resource_type)::text)
   AND (sqlc.arg(resource_id)::text = '' OR e.resource_id = sqlc.arg(resource_id)::text)
   AND (sqlc.arg(actor_type)::text = '' OR e.actor_type = sqlc.arg(actor_type)::text)
   AND (sqlc.arg(actor_id)::text = '' OR e.actor_id = sqlc.arg(actor_id)::text)
   AND (sqlc.arg(outcome)::text = '' OR e.outcome = sqlc.arg(outcome)::text)
   AND (sqlc.arg(request_id)::text = '' OR e.request_id = sqlc.arg(request_id)::text)
 ORDER BY e.seq DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: ExportPlatformAuditEvents :many
SELECT e.seq, e.id, e.occurred_at, e.actor_type, e.actor_id, e.actor_name, e.key_id, e.action, e.resource_type,
       e.resource_id, e.outcome, e.request_id, e.reason, e.detail::text AS detail, e.prev_hash, e.hash
  FROM pandokos.platform_audit_event e
 WHERE e.seq > sqlc.arg(after_seq)::bigint AND e.seq <= sqlc.arg(to_seq)::bigint
 ORDER BY e.seq
 LIMIT sqlc.arg(page_size)::integer;

-- name: GetPlatformAuditHead :one
SELECT c.seq, c.hash, c.anchor_seq, c.anchor_hash FROM pandokos.platform_audit_chain c WHERE c.id = 1;
