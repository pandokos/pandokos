-- Platform keys. Every statement runs in platform scope with the admin role.

-- name: LockPlatformKeys :exec
-- Serializes pandokos init: the bytes of "pdk-init" as a big-endian integer.
SELECT pg_catalog.pg_advisory_xact_lock(8098715872705538420);

-- name: CountUsablePlatformKeys :one
SELECT count(*)::integer AS usable,
       (count(*) FILTER (WHERE bootstrap))::integer AS bootstrap
  FROM pandokos.platform_key
 WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now());

-- name: CreatePlatformKey :one
INSERT INTO pandokos.platform_key (key_id, description, role, secret_hash, bootstrap, expires_at)
VALUES (sqlc.arg(key_id)::text, sqlc.arg(description)::text, sqlc.arg(role)::text, sqlc.arg(secret_hash)::bytea,
        sqlc.arg(bootstrap)::boolean, sqlc.narg(expires_at)::timestamptz)
RETURNING id, key_id, description, role, bootstrap, expires_at, revoked_at, created_at,
          (CASE WHEN revoked_at IS NOT NULL THEN 'revoked' WHEN expires_at <= now() THEN 'expired' ELSE 'active' END)::text AS state;

-- name: GetPlatformKey :one
SELECT k.id, k.key_id, k.description, k.role, k.secret_hash, k.bootstrap, k.expires_at, k.revoked_at, k.created_at,
       (CASE WHEN k.revoked_at IS NOT NULL THEN 'revoked' WHEN k.expires_at <= now() THEN 'expired' ELSE 'active' END)::text AS state
  FROM pandokos.platform_key k
 WHERE k.key_id = sqlc.arg(key_id)::text;

-- name: ListPlatformKeys :many
SELECT k.id, k.key_id, k.description, k.role, k.bootstrap, k.expires_at, k.revoked_at, k.created_at,
       (CASE WHEN k.revoked_at IS NOT NULL THEN 'revoked' WHEN k.expires_at <= now() THEN 'expired' ELSE 'active' END)::text AS state
  FROM pandokos.platform_key k
 WHERE (sqlc.arg(state)::text = ''
        OR (CASE WHEN k.revoked_at IS NOT NULL THEN 'revoked' WHEN k.expires_at <= now() THEN 'expired' ELSE 'active' END) = sqlc.arg(state)::text)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN k.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN k.id < sqlc.arg(after_id)::uuid
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN k.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN k.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: RevokePlatformKey :execrows
UPDATE pandokos.platform_key
   SET revoked_at = now()
 WHERE key_id = sqlc.arg(key_id)::text
   AND revoked_at IS NULL;
