-- Custom roles of a tenant (sprint 4.3). Every statement runs in a tenant session; none names the
-- tenant in a WHERE clause on purpose: the policy does (TEN-04). tenant_id appears only where a
-- row is inserted.

-- name: CreateTenantRole :one
INSERT INTO pandokos.tenant_role (tenant_id, name, description, permissions)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(name)::text, sqlc.arg(description)::text, sqlc.arg(permissions)::text[])
RETURNING id, tenant_id, name, description, permissions, state, created_at, updated_at;

-- name: GetTenantRole :one
SELECT t.id, t.tenant_id, t.name, t.description, t.permissions, t.state, t.created_at, t.updated_at,
       (SELECT count(*) FROM pandokos.membership_role r WHERE r.role = t.name)::bigint AS members,
       (SELECT count(*) FROM pandokos.role_assignment r WHERE r.role = t.name)::bigint AS service_accounts
  FROM pandokos.tenant_role t
 WHERE t.name = sqlc.arg(name)::text;

-- name: ListTenantRoles :many
SELECT t.id, t.tenant_id, t.name, t.description, t.permissions, t.state, t.created_at, t.updated_at
  FROM pandokos.tenant_role t
 WHERE (sqlc.arg(state)::text = '' OR t.state = sqlc.arg(state)::text)
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

-- name: CountTenantRoles :one
SELECT count(*)::bigint AS roles FROM pandokos.tenant_role;

-- name: LockTenantRole :one
SELECT id, name, description, permissions, state
  FROM pandokos.tenant_role
 WHERE name = sqlc.arg(name)::text
   FOR UPDATE;

-- name: LockTenantRolesForAssignment :many
SELECT name, permissions, state
  FROM pandokos.tenant_role
 WHERE name = ANY (sqlc.arg(names)::text[])
 ORDER BY name
   FOR SHARE;

-- name: UpdateTenantRole :execrows
UPDATE pandokos.tenant_role
   SET description = sqlc.arg(description)::text, permissions = sqlc.arg(permissions)::text[]
 WHERE id = sqlc.arg(id)::uuid;

-- name: SetTenantRoleState :execrows
UPDATE pandokos.tenant_role
   SET state = sqlc.arg(state)::text
 WHERE id = sqlc.arg(id)::uuid;

-- name: DeleteTenantRole :execrows
DELETE FROM pandokos.tenant_role WHERE id = sqlc.arg(id)::uuid;

-- name: CountRoleHolders :one
-- Run after LockTenantRole.
SELECT (SELECT count(*) FROM pandokos.membership_role r WHERE r.role = sqlc.arg(name)::text)::bigint AS members,
       (SELECT count(*) FROM pandokos.role_assignment r WHERE r.role = sqlc.arg(name)::text)::bigint AS service_accounts;

-- name: GetServiceAccountGrant :one
-- The roles and the permissions of the custom roles of a service account, as GetKeyPrincipal reads them.
SELECT COALESCE((SELECT array_agg(r.role ORDER BY r.role) FROM pandokos.role_assignment r
                  WHERE r.service_account_id = s.id), '{}')::text[] AS roles,
       COALESCE((SELECT array_agg(DISTINCT p ORDER BY p)
                   FROM pandokos.role_assignment r
                   JOIN pandokos.tenant_role c
                     ON c.tenant_id = r.tenant_id AND c.name = r.role AND c.state = 'active'
                  CROSS JOIN LATERAL unnest(c.permissions) AS p
                  WHERE r.service_account_id = s.id), '{}')::text[] AS granted
  FROM pandokos.service_account s
 WHERE s.id = sqlc.arg(id)::uuid;

-- name: GetMemberGrant :one
-- The roles and the permissions of the custom roles of a member, as GetMemberPrincipal reads them.
SELECT COALESCE((SELECT array_agg(r.role ORDER BY r.role) FROM pandokos.membership_role r
                  WHERE r.user_id = m.user_id), '{}')::text[] AS roles,
       COALESCE((SELECT array_agg(DISTINCT p ORDER BY p)
                   FROM pandokos.membership_role r
                   JOIN pandokos.tenant_role c
                     ON c.tenant_id = r.tenant_id AND c.name = r.role AND c.state = 'active'
                  CROSS JOIN LATERAL unnest(c.permissions) AS p
                  WHERE r.user_id = m.user_id), '{}')::text[] AS granted
  FROM pandokos.membership m
 WHERE m.user_id = sqlc.arg(user_id)::uuid;
