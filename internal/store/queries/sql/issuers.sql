-- Trusted token issuers, platform-level token issuers and subject mappings.
--
-- The authentication statements below run as follows. LookupIssuers, GetPlatformIssuerForAuth and
-- LookupPlatformIssuerTenant run without a tenant session (db.Unscoped): the tenant is not known
-- yet. GetTrustedIssuerForAuth, GetTokenPrincipal and RecordTokenAuthFailure run in the session of
-- the tenant that the lookup returned. In GetTokenPrincipal the policy of platform_issuer_tenant
-- shows only the session tenant's rows, so a tenant that the lookup function did not return finds
-- no mapping even if the gateway asked in the wrong session.
--
-- The administration statements after them run in a tenant session, except those marked platform,
-- which run in platform scope. None of the tenant statements names the tenant in a WHERE clause on
-- purpose: the policy does (TEN-04). tenant_id appears only where a row is inserted.

-- name: LookupIssuers :many
SELECT l.scope::text AS scope, COALESCE(l.tenant_id::text, '')::text AS tenant_id, l.issuer_id::text AS issuer_id
  FROM pandokos.issuer_lookup(sqlc.arg(issuer)::text, sqlc.arg(audiences)::text[]) l;

-- name: GetTrustedIssuerForAuth :one
SELECT i.id, i.tenant_id, i.issuer, i.audience, i.jwks_uri, i.algorithms, i.subject_claim, i.required_claims, i.enabled
  FROM pandokos.trusted_issuer i
 WHERE i.id = sqlc.arg(id)::uuid;

-- name: GetPlatformIssuerForAuth :one
SELECT i.id, i.issuer, i.audience, i.jwks_uri, i.algorithms, i.subject_claim, i.tenant_claim, i.required_claims, i.enabled
  FROM pandokos.platform_issuer i
 WHERE i.id = sqlc.arg(id)::uuid;

-- name: LookupPlatformIssuerTenant :one
SELECT COALESCE(pandokos.platform_issuer_tenant_lookup(sqlc.arg(issuer_id)::uuid, sqlc.arg(claim_value)::text)::text, '')::text AS tenant_id;

-- name: CountPlatformIssuerMappings :one
SELECT pandokos.platform_issuer_mapping_count(sqlc.arg(issuer_id)::uuid,
           (SELECT COALESCE(array_agg(t.id), ARRAY[]::uuid[]) FROM pandokos.tenant t))::bigint AS mappings;

-- name: GetTokenPrincipal :one
SELECT m.id AS mapping_id,
       s.id AS service_account_id, s.name AS service_account_name, s.state AS service_account_state,
       s.valid_from, s.valid_until,
       t.id AS tenant_id, t.name AS tenant_name, t.state AS tenant_state,
       COALESCE((SELECT array_agg(r.role ORDER BY r.role) FROM pandokos.role_assignment r
                  WHERE r.service_account_id = m.service_account_id), '{}')::text[] AS roles,
       COALESCE((SELECT array_agg(DISTINCT p ORDER BY p)
                   FROM pandokos.role_assignment r
                   JOIN pandokos.tenant_role c
                     ON c.tenant_id = r.tenant_id AND c.name = r.role AND c.state = 'active'
                  CROSS JOIN LATERAL unnest(c.permissions) AS p
                  WHERE r.service_account_id = m.service_account_id), '{}')::text[] AS granted
  FROM pandokos.subject_mapping m
  JOIN pandokos.service_account s ON s.tenant_id = m.tenant_id AND s.id = m.service_account_id
  JOIN pandokos.tenant t ON t.id = m.tenant_id
 WHERE m.subject = sqlc.arg(subject)::text
   AND CASE WHEN sqlc.arg(scope)::text = 'tenant'
            THEN m.trusted_issuer_id = sqlc.arg(issuer_id)::uuid
                 AND EXISTS (SELECT 1 FROM pandokos.trusted_issuer i
                              WHERE i.id = m.trusted_issuer_id AND i.enabled)
            WHEN sqlc.arg(scope)::text = 'platform'
            THEN m.platform_issuer_id = sqlc.arg(issuer_id)::uuid
                 AND EXISTS (SELECT 1 FROM pandokos.platform_issuer_tenant l
                              WHERE l.issuer_id = m.platform_issuer_id AND l.tenant_id = m.tenant_id
                                AND l.claim_value = sqlc.arg(claim_value)::text)
                 AND EXISTS (SELECT 1 FROM pandokos.platform_issuer i
                              WHERE i.id = m.platform_issuer_id AND i.enabled)
            ELSE false END;

-- name: RecordTokenAuthFailure :exec
SELECT pandokos.record_token_auth_failure(sqlc.arg(scope)::text, sqlc.arg(issuer_id)::uuid, sqlc.arg(reason)::text,
                                          sqlc.arg(subject)::text, sqlc.arg(request_id)::text);

-- Tenant-bound registrations. Tenant session.

-- name: CreateTrustedIssuer :one
INSERT INTO pandokos.trusted_issuer (tenant_id, name, description, issuer, audience, jwks_uri, algorithms,
                                     subject_claim, required_claims, enabled)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(name)::text, sqlc.arg(description)::text, sqlc.arg(issuer)::text,
        sqlc.arg(audience)::text, sqlc.arg(jwks_uri)::text, sqlc.arg(algorithms)::text[],
        sqlc.arg(subject_claim)::text, sqlc.arg(required_claims)::jsonb, sqlc.arg(enabled)::boolean)
RETURNING id, tenant_id, name, description, issuer, audience, jwks_uri, algorithms, subject_claim, required_claims,
          enabled, created_at, updated_at;

-- name: GetTrustedIssuer :one
SELECT i.id, i.tenant_id, i.name, i.description, i.issuer, i.audience, i.jwks_uri, i.algorithms, i.subject_claim,
       i.required_claims, i.enabled, i.created_at, i.updated_at
  FROM pandokos.trusted_issuer i
 WHERE i.id = sqlc.arg(id)::uuid;

-- name: LockTrustedIssuer :one
SELECT i.id
  FROM pandokos.trusted_issuer i
 WHERE i.id = sqlc.arg(id)::uuid
   FOR UPDATE;

-- name: ListTrustedIssuers :many
SELECT i.id, i.tenant_id, i.name, i.description, i.issuer, i.audience, i.jwks_uri, i.algorithms, i.subject_claim,
       i.required_claims, i.enabled, i.created_at, i.updated_at
  FROM pandokos.trusted_issuer i
 WHERE (sqlc.arg(name)::text = '' OR i.name = sqlc.arg(name)::text)
   AND (sqlc.arg(enabled)::text = '' OR i.enabled = (sqlc.arg(enabled)::text = 'true'))
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN i.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN i.id < sqlc.arg(after_id)::uuid
            WHEN 'name'        THEN (i.name, i.id) > (sqlc.arg(after_name)::text, sqlc.arg(after_id)::uuid)
            WHEN '-name'       THEN (i.name, i.id) < (sqlc.arg(after_name)::text, sqlc.arg(after_id)::uuid)
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN i.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN i.id END DESC,
          CASE WHEN sqlc.arg(sort)::text = 'name'        THEN i.name END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-name'       THEN i.name END DESC,
          CASE WHEN sqlc.arg(sort)::text = 'name'        THEN i.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-name'       THEN i.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: UpdateTrustedIssuer :one
UPDATE pandokos.trusted_issuer
   SET name            = COALESCE(sqlc.narg(name)::text, name),
       description     = COALESCE(sqlc.narg(description)::text, description),
       issuer          = COALESCE(sqlc.narg(issuer)::text, issuer),
       audience        = COALESCE(sqlc.narg(audience)::text, audience),
       jwks_uri        = COALESCE(sqlc.narg(jwks_uri)::text, jwks_uri),
       algorithms      = COALESCE(sqlc.narg(algorithms)::text[], algorithms),
       subject_claim   = COALESCE(sqlc.narg(subject_claim)::text, subject_claim),
       required_claims = COALESCE(sqlc.narg(required_claims)::text::jsonb, required_claims),
       enabled         = COALESCE(sqlc.narg(enabled)::boolean, enabled)
 WHERE id = sqlc.arg(id)::uuid
RETURNING id, tenant_id, name, description, issuer, audience, jwks_uri, algorithms, subject_claim, required_claims,
          enabled, created_at, updated_at;

-- name: DeleteTrustedIssuer :one
DELETE FROM pandokos.trusted_issuer
 WHERE id = sqlc.arg(id)::uuid
RETURNING id, tenant_id, name, description, issuer, audience, jwks_uri, algorithms, subject_claim, required_claims,
          enabled, created_at, updated_at;

-- name: CountSubjectMappingsOfIssuer :one
SELECT count(*)::bigint AS mappings
  FROM pandokos.subject_mapping m
 WHERE m.trusted_issuer_id = sqlc.arg(issuer_id)::uuid
    OR m.platform_issuer_id = sqlc.arg(issuer_id)::uuid;

-- name: ListMappedServiceAccountGrants :many
-- The service accounts that subjects of a trusted issuer act as, each with its roles and the
-- permissions of its active custom roles, as GetServiceAccountGrant reads them. Run after
-- LockTrustedIssuer, which keeps a mapping from being added meanwhile.
SELECT s.id,
       COALESCE((SELECT array_agg(r.role ORDER BY r.role) FROM pandokos.role_assignment r
                  WHERE r.service_account_id = s.id), '{}')::text[] AS roles,
       COALESCE((SELECT array_agg(DISTINCT p ORDER BY p)
                   FROM pandokos.role_assignment r
                   JOIN pandokos.tenant_role c
                     ON c.tenant_id = r.tenant_id AND c.name = r.role AND c.state = 'active'
                  CROSS JOIN LATERAL unnest(c.permissions) AS p
                  WHERE r.service_account_id = s.id), '{}')::text[] AS granted
  FROM pandokos.service_account s
 WHERE s.id IN (SELECT m.service_account_id FROM pandokos.subject_mapping m
                 WHERE m.trusted_issuer_id = sqlc.arg(issuer_id)::uuid)
 ORDER BY s.id;

-- Subject mappings. Tenant session. Every row carries the scope, ID and name of its registration
-- and the name of its service account.

-- name: CreateSubjectMapping :one
WITH m AS (
    INSERT INTO pandokos.subject_mapping (tenant_id, trusted_issuer_id, platform_issuer_id, subject,
                                          service_account_id, description)
    VALUES (sqlc.arg(tenant_id)::uuid, sqlc.narg(trusted_issuer_id)::uuid, sqlc.narg(platform_issuer_id)::uuid,
            sqlc.arg(subject)::text, sqlc.arg(service_account_id)::uuid, sqlc.arg(description)::text)
    RETURNING id, tenant_id, trusted_issuer_id, platform_issuer_id, subject, service_account_id, description, created_at
)
SELECT m.id, m.tenant_id,
       CASE WHEN m.trusted_issuer_id IS NOT NULL THEN 'tenant' ELSE 'platform' END::text AS issuer_scope,
       COALESCE(m.trusted_issuer_id, m.platform_issuer_id)::text AS issuer_id,
       COALESCE(ti.name, pi.name, '')::text AS issuer_name,
       m.subject, m.service_account_id, s.name AS service_account_name, m.description, m.created_at
  FROM m
  JOIN pandokos.service_account s ON s.tenant_id = m.tenant_id AND s.id = m.service_account_id
  LEFT JOIN pandokos.trusted_issuer ti ON ti.tenant_id = m.tenant_id AND ti.id = m.trusted_issuer_id
  LEFT JOIN pandokos.platform_issuer pi ON pi.id = m.platform_issuer_id;

-- name: GetSubjectMapping :one
SELECT m.id, m.tenant_id,
       CASE WHEN m.trusted_issuer_id IS NOT NULL THEN 'tenant' ELSE 'platform' END::text AS issuer_scope,
       COALESCE(m.trusted_issuer_id, m.platform_issuer_id)::text AS issuer_id,
       COALESCE(ti.name, pi.name, '')::text AS issuer_name,
       m.subject, m.service_account_id, s.name AS service_account_name, m.description, m.created_at
  FROM pandokos.subject_mapping m
  JOIN pandokos.service_account s ON s.tenant_id = m.tenant_id AND s.id = m.service_account_id
  LEFT JOIN pandokos.trusted_issuer ti ON ti.tenant_id = m.tenant_id AND ti.id = m.trusted_issuer_id
  LEFT JOIN pandokos.platform_issuer pi ON pi.id = m.platform_issuer_id
 WHERE m.id = sqlc.arg(id)::uuid;

-- name: ListSubjectMappings :many
SELECT m.id, m.tenant_id,
       CASE WHEN m.trusted_issuer_id IS NOT NULL THEN 'tenant' ELSE 'platform' END::text AS issuer_scope,
       COALESCE(m.trusted_issuer_id, m.platform_issuer_id)::text AS issuer_id,
       COALESCE(ti.name, pi.name, '')::text AS issuer_name,
       m.subject, m.service_account_id, s.name AS service_account_name, m.description, m.created_at
  FROM pandokos.subject_mapping m
  JOIN pandokos.service_account s ON s.tenant_id = m.tenant_id AND s.id = m.service_account_id
  LEFT JOIN pandokos.trusted_issuer ti ON ti.tenant_id = m.tenant_id AND ti.id = m.trusted_issuer_id
  LEFT JOIN pandokos.platform_issuer pi ON pi.id = m.platform_issuer_id
 WHERE (sqlc.arg(issuer_id)::text = ''
        OR COALESCE(m.trusted_issuer_id, m.platform_issuer_id)::text = sqlc.arg(issuer_id)::text)
   AND (sqlc.arg(service_account_id)::text = '' OR m.service_account_id::text = sqlc.arg(service_account_id)::text)
   AND (sqlc.arg(subject)::text = '' OR m.subject = sqlc.arg(subject)::text)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN m.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN m.id < sqlc.arg(after_id)::uuid
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN m.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN m.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: DeleteSubjectMapping :one
WITH m AS (
    DELETE FROM pandokos.subject_mapping
     WHERE id = sqlc.arg(id)::uuid
    RETURNING id, tenant_id, trusted_issuer_id, platform_issuer_id, subject, service_account_id, description, created_at
)
SELECT m.id, m.tenant_id,
       CASE WHEN m.trusted_issuer_id IS NOT NULL THEN 'tenant' ELSE 'platform' END::text AS issuer_scope,
       COALESCE(m.trusted_issuer_id, m.platform_issuer_id)::text AS issuer_id,
       COALESCE(ti.name, pi.name, '')::text AS issuer_name,
       m.subject, m.service_account_id, s.name AS service_account_name, m.description, m.created_at
  FROM m
  JOIN pandokos.service_account s ON s.tenant_id = m.tenant_id AND s.id = m.service_account_id
  LEFT JOIN pandokos.trusted_issuer ti ON ti.tenant_id = m.tenant_id AND ti.id = m.trusted_issuer_id
  LEFT JOIN pandokos.platform_issuer pi ON pi.id = m.platform_issuer_id;

-- Platform-level registrations as a tenant sees them. Tenant session: the policy of
-- platform_issuer_tenant narrows the join to the rows of the session tenant, which carry its
-- claim value.

-- name: ListTenantPlatformIssuers :many
SELECT i.id, i.name, i.description, i.issuer, i.audience, i.subject_claim, i.tenant_claim, l.claim_value, i.enabled
  FROM pandokos.platform_issuer i
  JOIN pandokos.platform_issuer_tenant l ON l.issuer_id = i.id
 WHERE (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN i.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN i.id < sqlc.arg(after_id)::uuid
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN i.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN i.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: GetTenantPlatformIssuer :one
SELECT i.id, i.name, i.description, i.issuer, i.audience, i.subject_claim, i.tenant_claim, l.claim_value, i.enabled
  FROM pandokos.platform_issuer i
  JOIN pandokos.platform_issuer_tenant l ON l.issuer_id = i.id
 WHERE i.id = sqlc.arg(id)::uuid;

-- Platform-level registrations. Platform session.

-- name: CreatePlatformIssuer :one
WITH i AS (
    INSERT INTO pandokos.platform_issuer (name, description, issuer, audience, jwks_uri, algorithms, subject_claim,
                                          tenant_claim, required_claims, enabled)
    VALUES (sqlc.arg(name)::text, sqlc.arg(description)::text, sqlc.arg(issuer)::text, sqlc.arg(audience)::text,
            sqlc.arg(jwks_uri)::text, sqlc.arg(algorithms)::text[], sqlc.arg(subject_claim)::text,
            sqlc.arg(tenant_claim)::text, sqlc.arg(required_claims)::jsonb, sqlc.arg(enabled)::boolean)
    RETURNING id, name, description, issuer, audience, jwks_uri, algorithms, subject_claim, tenant_claim,
              required_claims, enabled, created_at, updated_at
)
SELECT i.id, i.name, i.description, i.issuer, i.audience, i.jwks_uri, i.algorithms, i.subject_claim, i.tenant_claim,
       i.required_claims, i.enabled, i.created_at, i.updated_at,
       0::integer AS tenant_count
  FROM i;

-- name: GetPlatformIssuer :one
SELECT i.id, i.name, i.description, i.issuer, i.audience, i.jwks_uri, i.algorithms, i.subject_claim, i.tenant_claim,
       i.required_claims, i.enabled, i.created_at, i.updated_at,
       (SELECT count(*) FROM pandokos.platform_issuer_tenant l WHERE l.issuer_id = i.id)::integer AS tenant_count
  FROM pandokos.platform_issuer i
 WHERE i.id = sqlc.arg(id)::uuid;

-- name: LockPlatformIssuer :one
SELECT i.id
  FROM pandokos.platform_issuer i
 WHERE i.id = sqlc.arg(id)::uuid
   FOR UPDATE;

-- name: ListPlatformIssuers :many
SELECT i.id, i.name, i.description, i.issuer, i.audience, i.jwks_uri, i.algorithms, i.subject_claim, i.tenant_claim,
       i.required_claims, i.enabled, i.created_at, i.updated_at,
       (SELECT count(*) FROM pandokos.platform_issuer_tenant l WHERE l.issuer_id = i.id)::integer AS tenant_count
  FROM pandokos.platform_issuer i
 WHERE (sqlc.arg(name)::text = '' OR i.name = sqlc.arg(name)::text)
   AND (sqlc.arg(enabled)::text = '' OR i.enabled = (sqlc.arg(enabled)::text = 'true'))
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN i.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN i.id < sqlc.arg(after_id)::uuid
            WHEN 'name'        THEN (i.name, i.id) > (sqlc.arg(after_name)::text, sqlc.arg(after_id)::uuid)
            WHEN '-name'       THEN (i.name, i.id) < (sqlc.arg(after_name)::text, sqlc.arg(after_id)::uuid)
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN i.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN i.id END DESC,
          CASE WHEN sqlc.arg(sort)::text = 'name'        THEN i.name END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-name'       THEN i.name END DESC,
          CASE WHEN sqlc.arg(sort)::text = 'name'        THEN i.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-name'       THEN i.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: UpdatePlatformIssuer :one
WITH i AS (
    UPDATE pandokos.platform_issuer
       SET name            = COALESCE(sqlc.narg(name)::text, name),
           description     = COALESCE(sqlc.narg(description)::text, description),
           issuer          = COALESCE(sqlc.narg(issuer)::text, issuer),
           audience        = COALESCE(sqlc.narg(audience)::text, audience),
           jwks_uri        = COALESCE(sqlc.narg(jwks_uri)::text, jwks_uri),
           algorithms      = COALESCE(sqlc.narg(algorithms)::text[], algorithms),
           subject_claim   = COALESCE(sqlc.narg(subject_claim)::text, subject_claim),
           tenant_claim    = COALESCE(sqlc.narg(tenant_claim)::text, tenant_claim),
           required_claims = COALESCE(sqlc.narg(required_claims)::text::jsonb, required_claims),
           enabled         = COALESCE(sqlc.narg(enabled)::boolean, enabled)
     WHERE id = sqlc.arg(id)::uuid
    RETURNING id, name, description, issuer, audience, jwks_uri, algorithms, subject_claim, tenant_claim,
              required_claims, enabled, created_at, updated_at
)
SELECT i.id, i.name, i.description, i.issuer, i.audience, i.jwks_uri, i.algorithms, i.subject_claim, i.tenant_claim,
       i.required_claims, i.enabled, i.created_at, i.updated_at,
       (SELECT count(*) FROM pandokos.platform_issuer_tenant l WHERE l.issuer_id = i.id)::integer AS tenant_count
  FROM i;

-- name: DeletePlatformIssuer :one
WITH i AS (
    DELETE FROM pandokos.platform_issuer
     WHERE id = sqlc.arg(id)::uuid
    RETURNING id, name, description, issuer, audience, jwks_uri, algorithms, subject_claim, tenant_claim,
              required_claims, enabled, created_at, updated_at
)
SELECT i.id, i.name, i.description, i.issuer, i.audience, i.jwks_uri, i.algorithms, i.subject_claim, i.tenant_claim,
       i.required_claims, i.enabled, i.created_at, i.updated_at,
       (SELECT count(*) FROM pandokos.platform_issuer_tenant l WHERE l.issuer_id = i.id)::integer AS tenant_count
  FROM i;

-- The tenant list of a platform-level registration. Platform session.

-- name: ListPlatformIssuerTenants :many
SELECT l.issuer_id, l.tenant_id, t.name AS tenant_name, l.claim_value, l.created_at
  FROM pandokos.platform_issuer_tenant l
  JOIN pandokos.tenant t ON t.id = l.tenant_id
 WHERE l.issuer_id = sqlc.arg(issuer_id)::uuid
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN (l.created_at, l.tenant_id) > (sqlc.arg(after_created_at)::timestamptz, sqlc.arg(after_id)::uuid)
            WHEN '-created_at' THEN (l.created_at, l.tenant_id) < (sqlc.arg(after_created_at)::timestamptz, sqlc.arg(after_id)::uuid)
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN l.created_at END ASC,
          CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN l.tenant_id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN l.created_at END DESC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN l.tenant_id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: PutPlatformIssuerTenant :one
-- previous_claim_value is the value the tenant had before this statement, empty when it was not in
-- the list (a claim value is never empty); the caller holds the registration's row lock, so it is what the statement replaced.
WITH old AS (
    SELECT p.claim_value
      FROM pandokos.platform_issuer_tenant p
     WHERE p.issuer_id = sqlc.arg(issuer_id)::uuid AND p.tenant_id = sqlc.arg(tenant_id)::uuid
), l AS (
    INSERT INTO pandokos.platform_issuer_tenant (issuer_id, tenant_id, claim_value)
    VALUES (sqlc.arg(issuer_id)::uuid, sqlc.arg(tenant_id)::uuid, sqlc.arg(claim_value)::text)
    ON CONFLICT (issuer_id, tenant_id) DO UPDATE SET claim_value = EXCLUDED.claim_value
    RETURNING issuer_id, tenant_id, claim_value, created_at
)
SELECT l.issuer_id, l.tenant_id, t.name AS tenant_name, l.claim_value, l.created_at,
       COALESCE((SELECT o.claim_value FROM old o), '')::text AS previous_claim_value
  FROM l
  JOIN pandokos.tenant t ON t.id = l.tenant_id;

-- name: DeletePlatformIssuerTenant :one
WITH l AS (
    DELETE FROM pandokos.platform_issuer_tenant
     WHERE issuer_id = sqlc.arg(issuer_id)::uuid AND tenant_id = sqlc.arg(tenant_id)::uuid
    RETURNING issuer_id, tenant_id, claim_value, created_at
)
SELECT l.issuer_id, l.tenant_id, t.name AS tenant_name, l.claim_value, l.created_at
  FROM l
  JOIN pandokos.tenant t ON t.id = l.tenant_id;
