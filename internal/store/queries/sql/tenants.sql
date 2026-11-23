-- Tenants. Every statement runs in platform scope (store.DB.Platform or Provision), except
-- GetTenantSelf, which runs in the tenant's own session.

-- name: CreateTenant :one
INSERT INTO pandokos.tenant (name, display_name, time_zone, external_reference)
VALUES (sqlc.arg(name)::text, sqlc.arg(display_name)::text, sqlc.arg(time_zone)::text, sqlc.arg(external_reference)::text)
RETURNING id, name, display_name, state, created_at, updated_at, state_changed_at,
          time_zone, time_zone_previous, time_zone_effective_from,
          active_currency, active_currency_previous, active_currency_effective_from, external_reference;

-- name: GetTenant :one
SELECT id, name, display_name, state, created_at, updated_at, state_changed_at,
       time_zone, time_zone_previous, time_zone_effective_from,
       active_currency, active_currency_previous, active_currency_effective_from, external_reference
  FROM pandokos.tenant
 WHERE id = sqlc.arg(id)::uuid;

-- name: GetTenantForUpdate :one
SELECT id, name, display_name, state, created_at, updated_at, state_changed_at,
       time_zone, time_zone_previous, time_zone_effective_from,
       active_currency, active_currency_previous, active_currency_effective_from, external_reference
  FROM pandokos.tenant
 WHERE id = sqlc.arg(id)::uuid
   FOR UPDATE;

-- name: ListTenants :many
SELECT t.id, t.name, t.display_name, t.state, t.created_at, t.updated_at, t.state_changed_at,
       t.time_zone, t.time_zone_previous, t.time_zone_effective_from,
       t.active_currency, t.active_currency_previous, t.active_currency_effective_from, external_reference
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
       display_name = CASE WHEN sqlc.arg(set_display_name)::boolean THEN sqlc.arg(display_name)::text ELSE display_name END,
       external_reference = COALESCE(sqlc.narg(external_reference)::text, external_reference)
 WHERE id = sqlc.arg(id)::uuid
RETURNING id, name, display_name, state, created_at, updated_at, state_changed_at,
          time_zone, time_zone_previous, time_zone_effective_from,
          active_currency, active_currency_previous, active_currency_effective_from, external_reference;

-- name: SetTenantState :one
UPDATE pandokos.tenant
   SET state = sqlc.arg(state)::text
 WHERE id = sqlc.arg(id)::uuid
RETURNING id, name, display_name, state, created_at, updated_at, state_changed_at,
          time_zone, time_zone_previous, time_zone_effective_from,
          active_currency, active_currency_previous, active_currency_effective_from, external_reference;
