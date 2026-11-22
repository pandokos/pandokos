-- Support actions of the platform inside a tenant (sprint 4.3). The session each statement runs in
-- is named in its comment: ActInTenant is store.DB.ActInTenant (platform scope that is also the
-- session of one tenant), "platform" is the platform scope, "tenant" a tenant session.

-- name: OpenSupportAction :one
-- ActInTenant.
INSERT INTO pandokos.support_action (tenant_id, actor_type, actor_id, actor_name, key_id, reason, permissions, request_id, expires_at)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(actor_type)::text, sqlc.arg(actor_id)::text, sqlc.arg(actor_name)::text,
        sqlc.arg(key_id)::text, sqlc.arg(reason)::text, sqlc.arg(permissions)::text[], sqlc.arg(request_id)::text,
        now() + make_interval(secs => sqlc.arg(seconds)::integer))
RETURNING id, tenant_id, actor_type, actor_id, actor_name, key_id, reason, permissions, request_id,
          opened_at, expires_at, closed_at, closed_by, closed_by_id, close_reason;

-- name: CloseOpenSupportActions :many
-- ActInTenant. Run before OpenSupportAction: one open action per actor and tenant.
UPDATE pandokos.support_action
   SET closed_at = now(),
       closed_by = CASE WHEN expires_at <= now() THEN 'expiry' ELSE 'platform' END,
       closed_by_id = CASE WHEN expires_at <= now() THEN '' ELSE sqlc.arg(closed_by_id)::text END
 WHERE tenant_id = sqlc.arg(tenant_id)::uuid
   AND actor_type = sqlc.arg(actor_type)::text
   AND actor_id = sqlc.arg(actor_id)::text
   AND closed_at IS NULL
RETURNING id, closed_by;

-- name: GetOpenSupportAction :one
-- Platform. The look-up of the hook: the open action of an actor in a tenant.
SELECT a.id::text AS id, a.permissions, a.expires_at, t.name AS tenant_name, t.state AS tenant_state
  FROM pandokos.support_action a
  JOIN pandokos.tenant t ON t.id = a.tenant_id
 WHERE a.tenant_id = sqlc.arg(tenant_id)::uuid
   AND a.actor_type = sqlc.arg(actor_type)::text
   AND a.actor_id = sqlc.arg(actor_id)::text
   AND a.closed_at IS NULL
   AND a.expires_at > now();

-- name: ListOpenSupportActionsOfActor :many
-- Platform. For GET /v1/me: every open action of the actor, by tenant name.
SELECT a.id::text AS id, a.tenant_id, a.permissions, a.expires_at, t.name AS tenant_name, t.state AS tenant_state
  FROM pandokos.support_action a
  JOIN pandokos.tenant t ON t.id = a.tenant_id
 WHERE a.actor_type = sqlc.arg(actor_type)::text
   AND a.actor_id = sqlc.arg(actor_id)::text
   AND a.closed_at IS NULL
   AND a.expires_at > now()
 ORDER BY t.name, a.id;

-- name: GetSupportAction :one
-- Platform or tenant.
SELECT id, tenant_id, actor_type, actor_id, actor_name, key_id, reason, permissions, request_id,
       opened_at, expires_at, closed_at, closed_by, closed_by_id, close_reason
  FROM pandokos.support_action
 WHERE id = sqlc.arg(id)::uuid AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ListSupportActions :many
-- Platform or tenant. state: open, closed or empty for both.
SELECT id, tenant_id, actor_type, actor_id, actor_name, key_id, reason, permissions, request_id,
       opened_at, expires_at, closed_at, closed_by, closed_by_id, close_reason
  FROM pandokos.support_action
 WHERE tenant_id = sqlc.arg(tenant_id)::uuid
   AND (sqlc.arg(state)::text = ''
        OR (sqlc.arg(state)::text = 'open' AND closed_at IS NULL AND expires_at > now())
        OR (sqlc.arg(state)::text = 'closed' AND NOT (closed_at IS NULL AND expires_at > now())))
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'opened_at'  THEN (opened_at, id) > (sqlc.arg(after_opened_at)::timestamptz, sqlc.arg(after_id)::uuid)
            WHEN '-opened_at' THEN (opened_at, id) < (sqlc.arg(after_opened_at)::timestamptz, sqlc.arg(after_id)::uuid)
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'opened_at'  THEN opened_at END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-opened_at' THEN opened_at END DESC,
          CASE WHEN sqlc.arg(sort)::text = 'opened_at'  THEN id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-opened_at' THEN id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: LockSupportAction :one
-- ActInTenant or tenant.
SELECT id, tenant_id, actor_type, actor_id, actor_name, key_id, reason, permissions, request_id,
       opened_at, expires_at, closed_at, closed_by, closed_by_id, close_reason
  FROM pandokos.support_action
 WHERE id = sqlc.arg(id)::uuid AND tenant_id = sqlc.arg(tenant_id)::uuid
   FOR UPDATE;

-- name: CloseSupportAction :execrows
-- ActInTenant or tenant.
UPDATE pandokos.support_action
   SET closed_at = now(), closed_by = sqlc.arg(closed_by)::text, closed_by_id = sqlc.arg(closed_by_id)::text,
       close_reason = sqlc.arg(close_reason)::text
 WHERE id = sqlc.arg(id)::uuid AND closed_at IS NULL;

-- name: TenantExists :one
-- Platform.
SELECT EXISTS (SELECT 1 FROM pandokos.tenant WHERE id = sqlc.arg(id)::uuid) AS exists;
