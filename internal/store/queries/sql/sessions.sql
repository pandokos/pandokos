-- Sessions of users on the administration listener (sprint 4.2). Platform scope, except
-- GetMemberPrincipal, which runs in the session of the tenant of the request.

-- name: GetSessionPrincipal :one
-- Platform scope. One statement per request with a session cookie (§5.6).
SELECT s.id::text, s.user_id::text, s.method, s.created_at, s.last_used_at, s.expires_at,
       u.sign_in, u.state, u.display_name, COALESCE(u.tenant_id::text, '')::text AS managing_tenant_id,
       COALESCE(a.username, '') AS username, COALESCE(a.must_change, false) AS must_change,
       COALESCE((SELECT array_agg(r.role ORDER BY r.role) FROM pandokos.platform_role_assignment r
                  WHERE r.user_id = u.id), '{}')::text[] AS platform_roles
  FROM pandokos.user_session s
  JOIN pandokos.app_user u ON u.id = s.user_id
  LEFT JOIN pandokos.local_account a ON a.user_id = u.id
 WHERE s.token_hash = $1;

-- name: TouchSession :exec
UPDATE pandokos.user_session SET last_used_at = now()
 WHERE id = $1 AND last_used_at < now() - interval '1 minute';

-- name: GetMemberPrincipal :one
-- The tenant's session. No row: the user is not a member (404).
SELECT t.name AS tenant_name, t.state AS tenant_state,
       COALESCE((SELECT array_agg(r.role ORDER BY r.role) FROM pandokos.membership_role r
                  WHERE r.user_id = m.user_id), '{}')::text[] AS roles,
       COALESCE((SELECT array_agg(DISTINCT p ORDER BY p)
                   FROM pandokos.membership_role r
                   JOIN pandokos.tenant_role c
                     ON c.tenant_id = r.tenant_id AND c.name = r.role AND c.state = 'active'
                  CROSS JOIN LATERAL unnest(c.permissions) AS p
                  WHERE r.user_id = m.user_id), '{}')::text[] AS granted
  FROM pandokos.membership m
  JOIN pandokos.tenant t ON t.id = m.tenant_id
 WHERE m.user_id = $1;

-- name: DeleteOldestSessions :exec
-- Keeps the $2 most recent sessions of the user.
DELETE FROM pandokos.user_session
 WHERE id IN (SELECT o.id FROM pandokos.user_session o WHERE o.user_id = $1
               ORDER BY o.created_at DESC, o.id DESC OFFSET $2);

-- name: PurgeSessions :execrows
DELETE FROM pandokos.user_session
 WHERE expires_at <= now() OR last_used_at <= now() - sqlc.arg(idle)::interval;

-- name: CreateSession :one
INSERT INTO pandokos.user_session (token_hash, user_id, method, expires_at)
VALUES (sqlc.arg(token_hash)::bytea, sqlc.arg(user_id)::uuid, sqlc.arg(method)::text, sqlc.arg(expires_at)::timestamptz)
RETURNING id, user_id, method, created_at, last_used_at, expires_at;

-- name: DeleteSession :execrows
DELETE FROM pandokos.user_session WHERE id = sqlc.arg(id)::uuid;

-- name: DeleteSessionOfUser :execrows
DELETE FROM pandokos.user_session WHERE id = sqlc.arg(id)::uuid AND user_id = sqlc.arg(user_id)::uuid;

-- name: DeleteSessionsOfUser :execrows
-- Every session of the user, except the one named by except_id when it is given.
DELETE FROM pandokos.user_session
 WHERE user_id = sqlc.arg(user_id)::uuid
   AND (sqlc.narg(except_id)::uuid IS NULL OR id <> sqlc.narg(except_id)::uuid);

-- name: ListSessionsOfUser :many
SELECT id, user_id, method, created_at, last_used_at, expires_at
  FROM pandokos.user_session
 WHERE user_id = sqlc.arg(user_id)::uuid
 ORDER BY created_at DESC, id DESC;
