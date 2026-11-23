-- Key-encryption key versions and tenant data keys. The kek_version statements run in any
-- transaction; the data key statements in a tenant session.

-- Scope: any transaction
-- name: ListKEKVersions :many
SELECT v.id, v.provider, v.state, v.created_at, v.activated_at, v.retired_at FROM pandokos.kek_version v ORDER BY v.activated_at, v.id;

-- Scope: admin, platform scope
-- name: ListKEKVersionsWithErasures :many
SELECT v.id, v.provider, v.state, v.created_at, v.activated_at, v.retired_at,
       (SELECT count(*) FROM pandokos.tenant_deletion d
         WHERE d.state = 'completed' AND v.state <> 'retired' AND v.id = ANY (d.kek_versions))::bigint AS erasures_pending
  FROM pandokos.kek_version v ORDER BY v.activated_at, v.id;

-- Scope: admin, platform scope
-- name: ActivateKEKVersion :one
SELECT pandokos.kek_activate(sqlc.arg(id)::text, sqlc.arg(provider)::text)::text AS result;

-- Scope: worker, platform scope
-- name: RetireKEKVersions :one
SELECT pandokos.kek_retire()::integer AS retired;

-- Scope: worker, platform scope
-- name: ListTenantsForRewrap :many
SELECT t.id FROM pandokos.tenant t WHERE t.state <> 'deleted' ORDER BY t.id;

-- Scope: worker, session of the tenant
-- name: LockDataKeysForRewrap :many
SELECT k.version, k.kek_version, k.wrapped_key FROM pandokos.tenant_data_key k
 WHERE k.kek_version <> sqlc.arg(active)::text ORDER BY k.version FOR UPDATE;

-- Scope: worker, session of the tenant
-- name: RewrapDataKey :execrows
UPDATE pandokos.tenant_data_key
   SET kek_version = sqlc.arg(kek_version)::text, wrapped_key = sqlc.arg(wrapped_key)::bytea
 WHERE version = sqlc.arg(version)::integer AND kek_version = sqlc.arg(old_kek_version)::text;

-- name: CreateDataKey :execrows
INSERT INTO pandokos.tenant_data_key (tenant_id, version, kek_version, wrapped_key)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(version)::integer, sqlc.arg(kek_version)::text, sqlc.arg(wrapped_key)::bytea)
ON CONFLICT (tenant_id, version) DO NOTHING;

-- name: GetDataKey :one
SELECT version, kek_version, wrapped_key
  FROM pandokos.tenant_data_key
 WHERE version = sqlc.arg(version)::integer;
