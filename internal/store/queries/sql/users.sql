-- Users, local accounts, federated identities, platform roles, memberships, sign-in throttling and
-- OIDC sign-in transactions (sprint 4.2). Statements say which scope they run in; in a tenant's
-- session none names the tenant in a WHERE clause on purpose: the policy does (TEN-04).

-- name: GetAccountForSignIn :one
-- Platform scope.
SELECT u.id::text, u.state, COALESCE(u.tenant_id::text, '')::text AS managing_tenant_id, u.display_name,
       a.username, a.password_hash, a.must_change, a.password_expires_at
  FROM pandokos.local_account a JOIN pandokos.app_user u ON u.id = a.user_id
 WHERE a.username = $1;

-- name: GetThrottle :many
SELECT key, kind, failures, window_started_at, last_failure_at, locked_until, now()::timestamptz AS now
  FROM pandokos.signin_throttle WHERE key = ANY (sqlc.arg(keys)::bytea[]);

-- name: CountFailure :one
-- One failure for one key; the window restarts when it has passed; the lock is set when the
-- count reaches the limit. Returns the count and whether this call locked the key.
INSERT INTO pandokos.signin_throttle AS t (key, kind, failures, window_started_at, last_failure_at)
VALUES (sqlc.arg(key), sqlc.arg(kind), 1, now(), now())
ON CONFLICT (key) DO UPDATE SET
    failures          = CASE WHEN t.window_started_at <= now() - sqlc.arg(failure_window)::interval THEN 1 ELSE t.failures + 1 END,
    window_started_at = CASE WHEN t.window_started_at <= now() - sqlc.arg(failure_window)::interval THEN now() ELSE t.window_started_at END,
    last_failure_at   = now(),
    locked_until      = CASE WHEN t.window_started_at > now() - sqlc.arg(failure_window)::interval
                                  AND t.failures + 1 >= sqlc.arg(max_failures)::integer
                                  AND (t.locked_until IS NULL OR t.locked_until <= now())
                             THEN now() + sqlc.arg(lock_duration)::interval ELSE t.locked_until END
RETURNING failures, (locked_until IS NOT NULL AND locked_until = now() + sqlc.arg(lock_duration)::interval) AS locked_now;

-- name: PurgeThrottle :execrows
DELETE FROM pandokos.signin_throttle
 WHERE last_failure_at <= now() - sqlc.arg(keep)::interval AND (locked_until IS NULL OR locked_until <= now());

-- name: DeleteThrottle :execrows
DELETE FROM pandokos.signin_throttle WHERE key = sqlc.arg(key)::bytea;

-- name: UserTenants :many
SELECT pandokos.user_tenants($1)::text AS tenant_id;

-- name: LookupPlatformUser :one
SELECT COALESCE(pandokos.platform_user_lookup(sqlc.narg(user_id), sqlc.narg(username))::text, '')::text AS user_id;

-- name: TakeOidcLogin :execrows
DELETE FROM pandokos.oidc_login WHERE state_hash = $1 AND expires_at > now();

-- name: CreateOidcLogin :exec
INSERT INTO pandokos.oidc_login (state_hash, expires_at)
VALUES (sqlc.arg(state_hash)::bytea, sqlc.arg(expires_at)::timestamptz);

-- name: CountOidcLogins :one
SELECT count(*)::integer AS live FROM pandokos.oidc_login WHERE expires_at > now();

-- name: PurgeOidcLogins :execrows
DELETE FROM pandokos.oidc_login WHERE expires_at <= now();

-- name: TenantIDsByName :many
-- Platform scope: the group rules of the identity provider name tenants.
SELECT t.name, t.id FROM pandokos.tenant t WHERE t.name = ANY (sqlc.arg(names)::text[]);

-- name: CountTenants :one
-- Platform scope: whether any tenant exists, for the group rules of the identity provider.
SELECT count(*)::integer AS tenants FROM pandokos.tenant;

-- name: CreateUser :one
INSERT INTO pandokos.app_user (sign_in, tenant_id, display_name, email)
VALUES (sqlc.arg(sign_in)::text, sqlc.narg(tenant_id)::uuid, sqlc.arg(display_name)::text, sqlc.arg(email)::text)
RETURNING id, sign_in, tenant_id, display_name, email, state, state_changed_at, created_at, updated_at;

-- name: GetUser :one
-- Platform scope or the session of the tenant that manages the user.
SELECT u.id, u.sign_in, u.tenant_id, u.display_name, u.email, u.state, u.state_changed_at, u.created_at, u.updated_at,
       COALESCE(a.username, '') AS username, COALESCE(a.must_change, false) AS must_change,
       a.password_expires_at,
       COALESCE(i.subject, '') AS subject,
       COALESCE((SELECT array_agg(r.role ORDER BY r.role) FROM pandokos.platform_role_assignment r
                  WHERE r.user_id = u.id), '{}')::text[] AS platform_roles
  FROM pandokos.app_user u
  LEFT JOIN pandokos.local_account a ON a.user_id = u.id
  LEFT JOIN pandokos.user_identity i ON i.user_id = u.id
 WHERE u.id = sqlc.arg(id)::uuid;

-- name: LockUser :one
SELECT u.id, u.sign_in, u.tenant_id, u.state
  FROM pandokos.app_user u
 WHERE u.id = sqlc.arg(id)::uuid
   FOR UPDATE;

-- name: ListUsers :many
-- Platform scope; the users that the platform manages only.
SELECT u.id, u.sign_in, u.tenant_id, u.display_name, u.email, u.state, u.state_changed_at, u.created_at, u.updated_at,
       COALESCE(a.username, '') AS username, COALESCE(a.must_change, false) AS must_change,
       a.password_expires_at,
       COALESCE(i.subject, '') AS subject,
       COALESCE((SELECT array_agg(r.role ORDER BY r.role) FROM pandokos.platform_role_assignment r
                  WHERE r.user_id = u.id), '{}')::text[] AS platform_roles
  FROM pandokos.app_user u
  LEFT JOIN pandokos.local_account a ON a.user_id = u.id
  LEFT JOIN pandokos.user_identity i ON i.user_id = u.id
 WHERE u.tenant_id IS NULL
   AND (sqlc.arg(state)::text = '' OR u.state = sqlc.arg(state)::text)
   AND (sqlc.arg(username)::text = '' OR a.username = sqlc.arg(username)::text)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN u.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN u.id < sqlc.arg(after_id)::uuid
            WHEN 'username'    THEN (COALESCE(a.username, ''), u.id) > (sqlc.arg(after_username)::text, sqlc.arg(after_id)::uuid)
            WHEN '-username'   THEN (COALESCE(a.username, ''), u.id) < (sqlc.arg(after_username)::text, sqlc.arg(after_id)::uuid)
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN u.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN u.id END DESC,
          CASE WHEN sqlc.arg(sort)::text = 'username'    THEN COALESCE(a.username, '') END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-username'   THEN COALESCE(a.username, '') END DESC,
          CASE WHEN sqlc.arg(sort)::text = 'username'    THEN u.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-username'   THEN u.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: UpdateUser :execrows
UPDATE pandokos.app_user
   SET display_name = CASE WHEN sqlc.arg(set_display_name)::boolean THEN sqlc.arg(display_name)::text ELSE display_name END,
       email        = CASE WHEN sqlc.arg(set_email)::boolean THEN sqlc.arg(email)::text ELSE email END
 WHERE id = sqlc.arg(id)::uuid;

-- name: SetUserState :execrows
UPDATE pandokos.app_user SET state = sqlc.arg(state)::text WHERE id = sqlc.arg(id)::uuid;

-- name: CreateLocalAccount :exec
INSERT INTO pandokos.local_account (user_id, username, password_hash, must_change, password_expires_at)
VALUES (sqlc.arg(user_id)::uuid, sqlc.arg(username)::text, sqlc.arg(password_hash)::text,
        sqlc.arg(must_change)::boolean, sqlc.narg(password_expires_at)::timestamptz);

-- name: SetPassword :execrows
UPDATE pandokos.local_account
   SET password_hash = sqlc.arg(password_hash)::text, must_change = sqlc.arg(must_change)::boolean,
       password_expires_at = sqlc.narg(password_expires_at)::timestamptz, password_changed_at = now()
 WHERE user_id = sqlc.arg(user_id)::uuid;

-- name: UpgradePasswordHash :execrows
-- Rehash with the current parameters; a concurrent change of the password wins.
UPDATE pandokos.local_account SET password_hash = sqlc.arg(new_hash)::text
 WHERE user_id = sqlc.arg(user_id)::uuid AND password_hash = sqlc.arg(old_hash)::text;

-- name: CreateUserIdentity :exec
INSERT INTO pandokos.user_identity (user_id, issuer, subject)
VALUES (sqlc.arg(user_id)::uuid, sqlc.arg(issuer)::text, sqlc.arg(subject)::text);

-- name: GetUserByIdentity :one
-- Platform scope.
SELECT u.id, u.sign_in, u.tenant_id, u.display_name, u.email, u.state, u.state_changed_at, u.created_at, u.updated_at,
       i.issuer, i.subject
  FROM pandokos.user_identity i JOIN pandokos.app_user u ON u.id = i.user_id
 WHERE i.issuer = sqlc.arg(issuer)::text AND i.subject = sqlc.arg(subject)::text;

-- name: ListPlatformRoles :many
SELECT role, source FROM pandokos.platform_role_assignment
 WHERE user_id = sqlc.arg(user_id)::uuid ORDER BY role;

-- name: AddPlatformRole :exec
INSERT INTO pandokos.platform_role_assignment (user_id, role, source)
VALUES (sqlc.arg(user_id)::uuid, sqlc.arg(role)::text, sqlc.arg(source)::text);

-- name: RemovePlatformRole :execrows
DELETE FROM pandokos.platform_role_assignment
 WHERE user_id = sqlc.arg(user_id)::uuid AND role = sqlc.arg(role)::text;

-- name: CountPlatformRoleHolders :one
-- Active users that hold the role.
SELECT count(*)::integer AS holders
  FROM pandokos.platform_role_assignment r JOIN pandokos.app_user u ON u.id = r.user_id
 WHERE r.role = sqlc.arg(role)::text AND u.state = 'active';

-- name: CreateMembership :one
INSERT INTO pandokos.membership (tenant_id, user_id, source)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(user_id)::uuid, sqlc.arg(source)::text)
RETURNING id, tenant_id, user_id, source, created_at, updated_at;

-- name: GetMember :one
-- A tenant's session.
SELECT m.id, m.user_id, m.source, m.created_at, m.updated_at,
       u.sign_in, u.tenant_id AS managing_tenant_id, u.display_name, u.email, u.state,
       COALESCE(a.username, '') AS username, COALESCE(a.must_change, false) AS must_change,
       COALESCE((SELECT array_agg(r.role ORDER BY r.role) FROM pandokos.membership_role r
                  WHERE r.user_id = m.user_id), '{}')::text[] AS roles
  FROM pandokos.membership m
  JOIN pandokos.app_user u ON u.id = m.user_id
  LEFT JOIN pandokos.local_account a ON a.user_id = m.user_id
 WHERE m.user_id = sqlc.arg(user_id)::uuid;

-- name: ListMembers :many
-- A tenant's session.
SELECT m.id, m.user_id, m.source, m.created_at, m.updated_at,
       u.sign_in, u.tenant_id AS managing_tenant_id, u.display_name, u.email, u.state,
       COALESCE(a.username, '') AS username, COALESCE(a.must_change, false) AS must_change,
       COALESCE((SELECT array_agg(r.role ORDER BY r.role) FROM pandokos.membership_role r
                  WHERE r.user_id = m.user_id), '{}')::text[] AS roles
  FROM pandokos.membership m
  JOIN pandokos.app_user u ON u.id = m.user_id
  LEFT JOIN pandokos.local_account a ON a.user_id = m.user_id
 WHERE (sqlc.arg(username)::text = '' OR a.username = sqlc.arg(username)::text)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN m.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN m.id < sqlc.arg(after_id)::uuid
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN m.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN m.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: DeleteMembership :execrows
-- The roles go with the row (ON DELETE CASCADE).
DELETE FROM pandokos.membership WHERE user_id = sqlc.arg(user_id)::uuid;

-- name: SetMembershipSource :execrows
UPDATE pandokos.membership SET source = sqlc.arg(source)::text WHERE user_id = sqlc.arg(user_id)::uuid;

-- name: AddMembershipRole :exec
INSERT INTO pandokos.membership_role (tenant_id, user_id, role, source)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(user_id)::uuid, sqlc.arg(role)::text, sqlc.arg(source)::text);

-- name: RemoveMembershipRole :execrows
DELETE FROM pandokos.membership_role
 WHERE user_id = sqlc.arg(user_id)::uuid AND role = sqlc.arg(role)::text;

-- name: ListMembershipRoles :many
SELECT role, source FROM pandokos.membership_role
 WHERE user_id = sqlc.arg(user_id)::uuid ORDER BY role;
