-- Key-encryption key versions and tenant data keys. The kek_version statements run in any
-- transaction; the data key statements in a tenant session.

-- name: RegisterKEKVersion :exec
INSERT INTO pandokos.kek_version (id, provider)
VALUES (sqlc.arg(id)::text, sqlc.arg(provider)::text)
ON CONFLICT (id) DO NOTHING;

-- name: ListKEKVersions :many
SELECT id, provider FROM pandokos.kek_version ORDER BY id;

-- name: CreateDataKey :execrows
INSERT INTO pandokos.tenant_data_key (tenant_id, version, kek_version, wrapped_key)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(version)::integer, sqlc.arg(kek_version)::text, sqlc.arg(wrapped_key)::bytea)
ON CONFLICT (tenant_id, version) DO NOTHING;

-- name: GetDataKey :one
SELECT version, kek_version, wrapped_key
  FROM pandokos.tenant_data_key
 WHERE version = sqlc.arg(version)::integer;
