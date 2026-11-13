-- Virtual keys. LookupVirtualKeyTenant runs with no tenant and no scope (store.DB.Unscoped);
-- every other statement runs in a tenant session.

-- name: LookupVirtualKeyTenant :one
SELECT COALESCE(pandokos.virtual_key_lookup(sqlc.arg(key_id)::text)::text, '')::text AS tenant_id;

-- name: GetKeyPrincipal :one
SELECT k.key_id, k.secret_hash, COALESCE(k.scopes, '{}')::text[] AS scopes, (k.scopes IS NOT NULL)::boolean AS scoped,
       k.expires_at, k.revoked_at,
       s.id AS service_account_id, s.name AS service_account_name, s.state AS service_account_state,
       s.valid_from, s.valid_until,
       t.id AS tenant_id, t.name AS tenant_name, t.state AS tenant_state,
       COALESCE((SELECT array_agg(r.role ORDER BY r.role) FROM pandokos.role_assignment r
                  WHERE r.service_account_id = k.service_account_id), '{}')::text[] AS roles
  FROM pandokos.virtual_key k
  JOIN pandokos.service_account s ON s.tenant_id = k.tenant_id AND s.id = k.service_account_id
  JOIN pandokos.tenant t ON t.id = k.tenant_id
 WHERE k.key_id = sqlc.arg(key_id)::text;

-- name: CreateVirtualKey :one
INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, description, secret_hash, scopes, expires_at)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(service_account_id)::uuid, sqlc.arg(key_id)::text,
        sqlc.arg(description)::text, sqlc.arg(secret_hash)::bytea,
        CASE WHEN sqlc.arg(scoped)::boolean THEN sqlc.arg(scopes)::text[] END,
        sqlc.narg(expires_at)::timestamptz)
RETURNING key_id, tenant_id, service_account_id, description, COALESCE(scopes, '{}')::text[] AS scopes,
          (scopes IS NOT NULL)::boolean AS scoped, expires_at, revoked_at, created_at,
          (CASE WHEN revoked_at IS NOT NULL THEN 'revoked' WHEN expires_at <= now() THEN 'expired' ELSE 'active' END)::text AS state;

-- name: GetVirtualKey :one
SELECT k.key_id, k.tenant_id, k.service_account_id, k.description, COALESCE(k.scopes, '{}')::text[] AS scopes,
       (k.scopes IS NOT NULL)::boolean AS scoped, k.expires_at, k.revoked_at, k.created_at,
       (CASE WHEN k.revoked_at IS NOT NULL THEN 'revoked' WHEN k.expires_at <= now() THEN 'expired' ELSE 'active' END)::text AS state
  FROM pandokos.virtual_key k
 WHERE k.key_id = sqlc.arg(key_id)::text;

-- name: ListVirtualKeys :many
SELECT k.id, k.key_id, k.tenant_id, k.service_account_id, k.description, COALESCE(k.scopes, '{}')::text[] AS scopes,
       (k.scopes IS NOT NULL)::boolean AS scoped, k.expires_at, k.revoked_at, k.created_at,
       (CASE WHEN k.revoked_at IS NOT NULL THEN 'revoked' WHEN k.expires_at <= now() THEN 'expired' ELSE 'active' END)::text AS state
  FROM pandokos.virtual_key k
 WHERE (sqlc.arg(state)::text = ''
        OR (CASE WHEN k.revoked_at IS NOT NULL THEN 'revoked' WHEN k.expires_at <= now() THEN 'expired' ELSE 'active' END) = sqlc.arg(state)::text)
   AND (NOT sqlc.arg(by_service_account)::boolean OR k.service_account_id = sqlc.arg(service_account_id)::uuid)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN k.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN k.id < sqlc.arg(after_id)::uuid
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN k.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN k.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: RevokeVirtualKey :execrows
UPDATE pandokos.virtual_key
   SET revoked_at = now()
 WHERE key_id = sqlc.arg(key_id)::text
   AND revoked_at IS NULL;
