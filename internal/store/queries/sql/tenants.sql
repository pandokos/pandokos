-- Tenants. Every statement runs in platform scope (store.DB.Platform or Provision), except
-- GetTenantSelf, which runs in the tenant's own session.

-- name: CreateTenant :one
INSERT INTO pandokos.tenant (name, display_name)
VALUES (sqlc.arg(name)::text, sqlc.arg(display_name)::text)
RETURNING id, name, display_name, state, created_at, updated_at, state_changed_at;

-- name: GetTenant :one
SELECT id, name, display_name, state, created_at, updated_at, state_changed_at
  FROM pandokos.tenant
 WHERE id = sqlc.arg(id)::uuid;

-- name: GetTenantForUpdate :one
SELECT id, name, display_name, state, created_at, updated_at, state_changed_at
  FROM pandokos.tenant
 WHERE id = sqlc.arg(id)::uuid
   FOR UPDATE;

-- name: ListTenants :many
SELECT t.id, t.name, t.display_name, t.state, t.created_at, t.updated_at, t.state_changed_at
  FROM pandokos.tenant t
 WHERE (sqlc.arg(state)::text = '' OR t.state = sqlc.arg(state)::text)
   AND (sqlc.arg(name)::text = '' OR t.name = sqlc.arg(name)::text)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN t.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN t.id < sqlc.arg(after_id)::uuid
            WHEN 'name'        THEN (t.name, t.id) > (sqlc.arg(after_name)::text, sqlc.arg(after_id)::uuid)
            WHEN '-name'       THEN (t.name, t.id) < (sqlc.arg(after_name)::text, sqlc.arg(after_id)::uuid)
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN t.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN t.id END DESC,
          CASE WHEN sqlc.arg(sort)::text = 'name'        THEN t.name END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-name'       THEN t.name END DESC,
          CASE WHEN sqlc.arg(sort)::text = 'name'        THEN t.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-name'       THEN t.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: UpdateTenant :one
UPDATE pandokos.tenant
   SET name         = CASE WHEN sqlc.arg(set_name)::boolean THEN sqlc.arg(name)::text ELSE name END,
       display_name = CASE WHEN sqlc.arg(set_display_name)::boolean THEN sqlc.arg(display_name)::text ELSE display_name END
 WHERE id = sqlc.arg(id)::uuid
RETURNING id, name, display_name, state, created_at, updated_at, state_changed_at;

-- name: SetTenantState :one
UPDATE pandokos.tenant
   SET state = sqlc.arg(state)::text
 WHERE id = sqlc.arg(id)::uuid
RETURNING id, name, display_name, state, created_at, updated_at, state_changed_at;
