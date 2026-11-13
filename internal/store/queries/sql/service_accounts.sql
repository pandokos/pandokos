-- Service accounts and their roles. Every statement runs in a tenant session; none names the
-- tenant in a WHERE clause on purpose: the policy does (TEN-04). tenant_id appears only where
-- a row is inserted.

-- name: CreateServiceAccount :one
INSERT INTO pandokos.service_account (tenant_id, name, description, valid_from, valid_until)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(name)::text, sqlc.arg(description)::text,
        sqlc.narg(valid_from)::timestamptz, sqlc.narg(valid_until)::timestamptz)
RETURNING id, tenant_id, name, description, state, valid_from, valid_until, created_at, updated_at;

-- name: GetServiceAccount :one
SELECT s.id, s.tenant_id, s.name, s.description, s.state, s.valid_from, s.valid_until, s.created_at, s.updated_at,
       COALESCE((SELECT array_agg(r.role ORDER BY r.role) FROM pandokos.role_assignment r
                  WHERE r.service_account_id = s.id), '{}')::text[] AS roles
  FROM pandokos.service_account s
 WHERE s.id = sqlc.arg(id)::uuid;

-- name: LockServiceAccount :one
SELECT s.id, s.state
  FROM pandokos.service_account s
 WHERE s.id = sqlc.arg(id)::uuid
   FOR UPDATE;

-- name: ListServiceAccounts :many
SELECT s.id, s.tenant_id, s.name, s.description, s.state, s.valid_from, s.valid_until, s.created_at, s.updated_at,
       COALESCE((SELECT array_agg(r.role ORDER BY r.role) FROM pandokos.role_assignment r
                  WHERE r.service_account_id = s.id), '{}')::text[] AS roles
  FROM pandokos.service_account s
 WHERE (sqlc.arg(state)::text = '' OR s.state = sqlc.arg(state)::text)
   AND (sqlc.arg(name)::text = '' OR s.name = sqlc.arg(name)::text)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN s.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN s.id < sqlc.arg(after_id)::uuid
            WHEN 'name'        THEN (s.name, s.id) > (sqlc.arg(after_name)::text, sqlc.arg(after_id)::uuid)
            WHEN '-name'       THEN (s.name, s.id) < (sqlc.arg(after_name)::text, sqlc.arg(after_id)::uuid)
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN s.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN s.id END DESC,
          CASE WHEN sqlc.arg(sort)::text = 'name'        THEN s.name END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-name'       THEN s.name END DESC,
          CASE WHEN sqlc.arg(sort)::text = 'name'        THEN s.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-name'       THEN s.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: UpdateServiceAccount :execrows
UPDATE pandokos.service_account
   SET name        = CASE WHEN sqlc.arg(set_name)::boolean THEN sqlc.arg(name)::text ELSE name END,
       description = CASE WHEN sqlc.arg(set_description)::boolean THEN sqlc.arg(description)::text ELSE description END,
       valid_from  = CASE WHEN sqlc.arg(set_valid_from)::boolean THEN sqlc.narg(valid_from)::timestamptz ELSE valid_from END,
       valid_until = CASE WHEN sqlc.arg(set_valid_until)::boolean THEN sqlc.narg(valid_until)::timestamptz ELSE valid_until END
 WHERE id = sqlc.arg(id)::uuid;

-- name: SetServiceAccountState :execrows
UPDATE pandokos.service_account
   SET state = sqlc.arg(state)::text
 WHERE id = sqlc.arg(id)::uuid;

-- name: DeleteRoleAssignments :execrows
DELETE FROM pandokos.role_assignment
 WHERE service_account_id = sqlc.arg(service_account_id)::uuid;

-- name: CreateRoleAssignment :exec
INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(service_account_id)::uuid, sqlc.arg(role)::text);
