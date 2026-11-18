-- Limit rules, the events of quotas and budgets, and the ledger aggregates their counters are
-- rebuilt from. Every statement runs in a store transaction helper; none names the tenant in a
-- WHERE clause: the policies do (TEN-04). A rule row names its subject and its scope by
-- identifier (migration 0011); every statement that returns a rule also returns subject_id (the
-- service account ID, the key ID of the virtual key, or the ID of the subject mapping) and the
-- names of the provider and the model, read through the foreign keys.

-- Gateway pool, session of the tenant whose rules are loaded. platform_limit_rule shows the
-- defaults and the tenant's own rows in that session.

-- name: ListLimitRules :many
SELECT r.id, r.subject_kind,
       COALESCE(r.service_account_id::text, k.key_id, r.subject_mapping_id::text, '')::text AS subject_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds
  FROM pandokos.limit_rule r
  LEFT JOIN pandokos.virtual_key k ON k.tenant_id = r.tenant_id AND k.id = r.virtual_key_id
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id
 ORDER BY r.id
 LIMIT 5001;

-- name: ListPlatformLimitRules :many
SELECT r.id, (r.tenant_id IS NULL)::boolean AS is_default,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds
  FROM pandokos.platform_limit_rule r
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id
 ORDER BY r.tenant_id NULLS FIRST, r.id;

-- The time zone of the tenant of the session: the zone that is or will be in force, the one in
-- force until effective_from when a change is pending ('' is the default of the installation).
-- Gateway pool (with the rules), admin pool (capability check, time zone operations).

-- name: GetTenantTimeZone :one
SELECT t.time_zone, t.time_zone_previous, t.time_zone_effective_from
  FROM pandokos.tenant t
 WHERE t.id = pandokos.current_tenant_id();

-- Admin pool. The row is locked: two changes of the zone do not interleave. In a tenant session
-- the row is the tenant's own; in the platform scope the tenant is named. db_now is the time the
-- trigger of the table compares with: the instant of a change is computed from it.

-- name: LockTenantTimeZone :one
SELECT t.id, t.time_zone, t.time_zone_previous, t.time_zone_effective_from, now()::timestamptz AS db_now
  FROM pandokos.tenant t
 WHERE t.id = sqlc.arg(id)::uuid
   FOR UPDATE;

-- name: SetTenantTimeZone :one
UPDATE pandokos.tenant
   SET time_zone                = sqlc.arg(time_zone)::text,
       time_zone_previous       = sqlc.arg(time_zone_previous)::text,
       time_zone_effective_from = sqlc.narg(time_zone_effective_from)::timestamptz
 WHERE id = sqlc.arg(id)::uuid
RETURNING id, time_zone, time_zone_previous, time_zone_effective_from;

-- Gateway pool, session of the tenant. A row that exists already is left alone: recording is
-- idempotent per rule, period, kind and percentage.

-- name: InsertLimitEvent :execrows
INSERT INTO pandokos.limit_event (tenant_id, kind, rule_id, rule_level, subject_kind, subject_id, provider, model,
                                  metric, currency, period_start, period_end, percent, limit_value, observed)
VALUES (pandokos.current_tenant_id(), sqlc.arg(kind)::text, sqlc.arg(rule_id)::uuid, sqlc.arg(rule_level)::text,
        sqlc.arg(subject_kind)::text, sqlc.arg(subject_id)::text, sqlc.arg(provider)::text, sqlc.arg(model)::text,
        sqlc.arg(metric)::text, sqlc.arg(currency)::text, sqlc.arg(period_start)::timestamptz,
        sqlc.arg(period_end)::timestamptz, sqlc.arg(percent)::smallint, sqlc.arg(limit_value)::bigint,
        sqlc.arg(observed)::numeric)
ON CONFLICT DO NOTHING;

-- Gateway pool, session of the tenant: the audit rows of the tenant's first unlimited request of
-- a day, through the one function the gateway role may execute for it. recorded is false when the
-- day had its row already.

-- name: RecordUnlimitedTraffic :one
SELECT pandokos.record_unlimited_traffic(sqlc.arg(day)::date, sqlc.arg(time_zone)::text, sqlc.arg(provider)::text,
                                         sqlc.arg(model)::text, sqlc.arg(request_id)::text)::boolean AS recorded;

-- Worker pool (counters and capability check), session of the tenant. What the ledger holds for
-- the requests that started in [from_time, to_time), by the dimensions a rule can name: the
-- service account, the key, the provider and the model, the last two by identifier. The provider
-- of a record is the provider that has its name now; the model is the one the record names by ID.
-- Rows that started before live_from are summed with minute 0; later rows by the minute they
-- started in (Unix time / 60). A request counts when a credential was opened for it; its tokens
-- are those the cost was computed on; its cost counts when it is known.

-- name: AggregateUsageForLimits :many
SELECT (CASE WHEN u.started_at >= sqlc.arg(live_from)::timestamptz
             THEN floor(extract(epoch FROM u.started_at) / 60)::bigint ELSE 0 END)::bigint AS minute,
       u.principal_id, u.key_id, COALESCE(p.id::text, '')::text AS provider_id,
       COALESCE(u.model_id::text, '')::text AS model_id, COALESCE(u.currency, '')::text AS currency,
       count(*) FILTER (WHERE u.credential_id IS NOT NULL)::bigint AS requests,
       COALESCE(sum(COALESCE(u.input_tokens, u.estimated_input_tokens, 0)
                    + COALESCE(u.output_tokens, u.estimated_output_tokens, 0))
                FILTER (WHERE u.credential_id IS NOT NULL), 0)::numeric AS tokens,
       COALESCE(sum(u.cost) FILTER (WHERE u.credential_id IS NOT NULL), 0)::numeric AS cost
  FROM pandokos.usage_record u
  LEFT JOIN pandokos.provider p ON p.name = u.provider
 WHERE u.started_at >= sqlc.arg(from_time)::timestamptz
   AND u.started_at < sqlc.arg(to_time)::timestamptz
 GROUP BY 1, u.principal_id, u.key_id, p.id, u.model_id, COALESCE(u.currency, '')
HAVING count(*) FILTER (WHERE u.credential_id IS NOT NULL) > 0;

-- Administration API, tenant session (admin pool).

-- The lock of every statement that creates rules of a tenant or deletes them with their subject:
-- a rule is not created for a subject while that subject is being removed.

-- name: LockLimitRules :exec
SELECT pg_catalog.pg_advisory_xact_lock(
           pg_catalog.hashtextextended('pandokos.limit_rule:' || pandokos.current_tenant_id()::text, 0));

-- name: CountLimitRules :one
SELECT count(*)::bigint FROM pandokos.limit_rule;

-- The subject a creation names, as the identifier of its row: a service account by ID; for 'key'
-- a virtual key that is not revoked, by key ID, or a subject mapping by ID. All three are empty
-- for the tenant and for a subject that is not in this tenant.

-- name: ResolveLimitSubject :one
SELECT COALESCE((SELECT s.id::text FROM pandokos.service_account s
                  WHERE sqlc.arg(subject_kind)::text = 'service_account'
                    AND s.id::text = sqlc.arg(subject_id)::text), '')::text AS service_account_id,
       COALESCE((SELECT v.id::text FROM pandokos.virtual_key v
                  WHERE sqlc.arg(subject_kind)::text = 'key' AND v.key_id = sqlc.arg(subject_id)::text
                    AND v.revoked_at IS NULL), '')::text AS virtual_key_id,
       COALESCE((SELECT sm.id::text FROM pandokos.subject_mapping sm
                  WHERE sqlc.arg(subject_kind)::text = 'key'
                    AND sm.id::text = sqlc.arg(subject_id)::text), '')::text AS subject_mapping_id;

-- The scope a creation names, as identifiers. No row: no such provider. model_id is empty when
-- model is empty or names no model of the provider. Tenant session or platform scope.

-- name: ResolveLimitScope :one
SELECT pr.id::text AS provider_id,
       COALESCE((SELECT mo.id::text FROM pandokos.model mo
                  WHERE mo.provider_id = pr.id AND mo.name = sqlc.arg(model)::text), '')::text AS model_id
  FROM pandokos.provider pr
 WHERE pr.name = sqlc.arg(provider)::text;

-- The currencies of the catalogue prices that can be in force for a scope: for each model of the
-- scope, the version in force now and every version that starts later. In a tenant session with
-- visible_only, the models are those the tenant can be given (available to all, or granted: the
-- policy of model_tenant_grant shows the tenant's grants only); otherwise every model of the
-- scope. Sprint 5.4 adds the currencies of the tenant's own price list to this set.

-- name: ListPriceCurrenciesForScope :many
SELECT DISTINCT v.currency
  FROM pandokos.price_version v
  JOIN pandokos.model mo ON mo.id = v.model_id
 WHERE (sqlc.narg(provider_id)::uuid IS NULL OR mo.provider_id = sqlc.narg(provider_id)::uuid)
   AND (sqlc.narg(model_id)::uuid IS NULL OR mo.id = sqlc.narg(model_id)::uuid)
   AND (NOT sqlc.arg(visible_only)::boolean OR mo.availability = 'all'
        OR EXISTS (SELECT 1 FROM pandokos.model_tenant_grant g WHERE g.model_id = mo.id))
   AND (v.valid_from > now()
        OR v.valid_from = (SELECT max(c.valid_from) FROM pandokos.price_version c
                            WHERE c.model_id = v.model_id AND c.valid_from <= now()))
 ORDER BY v.currency;

-- name: CreateLimitRule :one
WITH r AS (
    INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, service_account_id, virtual_key_id, subject_mapping_id,
                                     provider_id, model_id, metric, limit_value, currency, enforce, thresholds)
    VALUES (pandokos.current_tenant_id(), sqlc.arg(subject_kind)::text, sqlc.narg(service_account_id)::uuid,
            sqlc.narg(virtual_key_id)::uuid, sqlc.narg(subject_mapping_id)::uuid, sqlc.narg(provider_id)::uuid,
            sqlc.narg(model_id)::uuid, sqlc.arg(metric)::text, sqlc.arg(limit_value)::bigint,
            sqlc.arg(currency)::text, sqlc.arg(enforce)::boolean, sqlc.arg(thresholds)::smallint[])
    RETURNING *
)
SELECT r.id, r.subject_kind,
       COALESCE(r.service_account_id::text, k.key_id, r.subject_mapping_id::text, '')::text AS subject_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM r
  LEFT JOIN pandokos.virtual_key k ON k.tenant_id = r.tenant_id AND k.id = r.virtual_key_id
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id;

-- name: GetLimitRule :one
SELECT r.id, r.subject_kind,
       COALESCE(r.service_account_id::text, k.key_id, r.subject_mapping_id::text, '')::text AS subject_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM pandokos.limit_rule r
  LEFT JOIN pandokos.virtual_key k ON k.tenant_id = r.tenant_id AND k.id = r.virtual_key_id
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id
 WHERE r.id = sqlc.arg(id)::uuid;

-- name: LockLimitRule :one
SELECT r.id, r.subject_kind,
       COALESCE(r.service_account_id::text, k.key_id, r.subject_mapping_id::text, '')::text AS subject_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM pandokos.limit_rule r
  LEFT JOIN pandokos.virtual_key k ON k.tenant_id = r.tenant_id AND k.id = r.virtual_key_id
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id
 WHERE r.id = sqlc.arg(id)::uuid
   FOR UPDATE OF r;

-- subject_id, provider and model filter by what the API shows: the subject as above, the names
-- of the scope.

-- name: ListLimitRulesPage :many
SELECT r.id, r.subject_kind,
       COALESCE(r.service_account_id::text, k.key_id, r.subject_mapping_id::text, '')::text AS subject_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM pandokos.limit_rule r
  LEFT JOIN pandokos.virtual_key k ON k.tenant_id = r.tenant_id AND k.id = r.virtual_key_id
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id
 WHERE (sqlc.arg(subject_kind)::text = '' OR r.subject_kind = sqlc.arg(subject_kind)::text)
   AND (sqlc.arg(subject_id)::text = ''
        OR COALESCE(r.service_account_id::text, k.key_id, r.subject_mapping_id::text, '') = sqlc.arg(subject_id)::text)
   AND (sqlc.arg(metric)::text = '' OR r.metric = sqlc.arg(metric)::text)
   AND (sqlc.arg(provider)::text = '' OR p.name = sqlc.arg(provider)::text)
   AND (sqlc.arg(model)::text = '' OR m.name = sqlc.arg(model)::text)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN r.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN r.id < sqlc.arg(after_id)::uuid
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN r.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN r.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: UpdateLimitRule :one
WITH r AS (
    UPDATE pandokos.limit_rule
       SET limit_value = sqlc.arg(limit_value)::bigint,
           enforce     = sqlc.arg(enforce)::boolean,
           thresholds  = sqlc.arg(thresholds)::smallint[]
     WHERE id = sqlc.arg(id)::uuid
    RETURNING *
)
SELECT r.id, r.subject_kind,
       COALESCE(r.service_account_id::text, k.key_id, r.subject_mapping_id::text, '')::text AS subject_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM r
  LEFT JOIN pandokos.virtual_key k ON k.tenant_id = r.tenant_id AND k.id = r.virtual_key_id
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id;

-- name: DeleteLimitRule :one
WITH r AS (
    DELETE FROM pandokos.limit_rule
     WHERE id = sqlc.arg(id)::uuid
    RETURNING *
)
SELECT r.id, r.subject_kind,
       COALESCE(r.service_account_id::text, k.key_id, r.subject_mapping_id::text, '')::text AS subject_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM r
  LEFT JOIN pandokos.virtual_key k ON k.tenant_id = r.tenant_id AND k.id = r.virtual_key_id
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id;

-- The rules of a subject that is going away, deleted before it in the same transaction: those of
-- a virtual key (by key ID; '' for none), of a subject mapping, or of every subject mapping of a
-- tenant-bound issuer. The caller holds LockLimitRules and writes one audit row per row returned.

-- name: DeleteLimitRulesOfSubject :many
WITH r AS (
    DELETE FROM pandokos.limit_rule d
     WHERE (sqlc.arg(key_id)::text <> ''
            AND d.virtual_key_id = (SELECT v.id FROM pandokos.virtual_key v WHERE v.key_id = sqlc.arg(key_id)::text))
        OR d.subject_mapping_id = sqlc.narg(subject_mapping_id)::uuid
        OR d.subject_mapping_id IN (SELECT sm.id FROM pandokos.subject_mapping sm
                                     WHERE sm.trusted_issuer_id = sqlc.narg(trusted_issuer_id)::uuid)
    RETURNING *
)
SELECT r.id, r.subject_kind,
       COALESCE(r.service_account_id::text, k.key_id, r.subject_mapping_id::text, '')::text AS subject_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM r
  LEFT JOIN pandokos.virtual_key k ON k.tenant_id = r.tenant_id AND k.id = r.virtual_key_id
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id
 ORDER BY r.id;

-- The ceilings that apply to the tenant of the session: the defaults and its own rows.

-- name: ListTenantPlatformLimitRules :many
SELECT r.id, (r.tenant_id IS NULL)::boolean AS is_default,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM pandokos.platform_limit_rule r
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id
 WHERE (NOT sqlc.arg(after)::boolean OR r.id > sqlc.arg(after_id)::uuid)
 ORDER BY r.id
 LIMIT sqlc.arg(page_size)::integer;

-- name: ListLimitEvents :many
SELECT e.id, e.kind, e.rule_id, e.rule_level, e.subject_kind, e.subject_id, e.provider, e.model, e.metric,
       e.currency, e.period_start, e.period_end, e.percent, e.limit_value, e.observed, e.occurred_at
  FROM pandokos.limit_event e
 WHERE (sqlc.arg(kind)::text = '' OR e.kind = sqlc.arg(kind)::text)
   AND (sqlc.arg(rule_id)::text = '' OR e.rule_id::text = sqlc.arg(rule_id)::text)
   AND (NOT sqlc.arg(after)::boolean OR e.id < sqlc.arg(after_id)::uuid)
 ORDER BY e.id DESC
 LIMIT sqlc.arg(page_size)::integer;

-- Administration API, platform session (admin pool).

-- name: CreatePlatformLimitRule :one
WITH r AS (
    INSERT INTO pandokos.platform_limit_rule (tenant_id, provider_id, model_id, metric, limit_value, currency, enforce,
                                              thresholds)
    VALUES (sqlc.narg(tenant_id)::uuid, sqlc.narg(provider_id)::uuid, sqlc.narg(model_id)::uuid,
            sqlc.arg(metric)::text, sqlc.arg(limit_value)::bigint, sqlc.arg(currency)::text,
            sqlc.arg(enforce)::boolean, sqlc.arg(thresholds)::smallint[])
    RETURNING *
)
SELECT r.id, r.tenant_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM r
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id;

-- name: GetPlatformLimitRule :one
SELECT r.id, r.tenant_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM pandokos.platform_limit_rule r
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id
 WHERE r.id = sqlc.arg(id)::uuid;

-- name: LockPlatformLimitRule :one
SELECT r.id, r.tenant_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM pandokos.platform_limit_rule r
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id
 WHERE r.id = sqlc.arg(id)::uuid
   FOR UPDATE OF r;

-- applies_to: '' every row, 'default' the rows without tenant, otherwise a tenant ID.

-- name: ListPlatformLimitRulesPage :many
SELECT r.id, r.tenant_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM pandokos.platform_limit_rule r
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id
 WHERE (sqlc.arg(applies_to)::text = ''
        OR (sqlc.arg(applies_to)::text = 'default' AND r.tenant_id IS NULL)
        OR r.tenant_id::text = sqlc.arg(applies_to)::text)
   AND (sqlc.arg(metric)::text = '' OR r.metric = sqlc.arg(metric)::text)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN r.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN r.id < sqlc.arg(after_id)::uuid
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN r.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN r.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: UpdatePlatformLimitRule :one
WITH r AS (
    UPDATE pandokos.platform_limit_rule
       SET limit_value = sqlc.arg(limit_value)::bigint,
           enforce     = sqlc.arg(enforce)::boolean,
           thresholds  = sqlc.arg(thresholds)::smallint[]
     WHERE id = sqlc.arg(id)::uuid
    RETURNING *
)
SELECT r.id, r.tenant_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM r
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id;

-- name: DeletePlatformLimitRule :one
WITH r AS (
    DELETE FROM pandokos.platform_limit_rule
     WHERE id = sqlc.arg(id)::uuid
    RETURNING *
)
SELECT r.id, r.tenant_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM r
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id;

-- The rules of the subject mappings of a platform-level issuer, in every tenant, deleted with one
-- audit row each in the log of its tenant, before the issuer goes (platform session; the function
-- is a definer function, migration 0011). The actor is the platform principal of the request.

-- name: DeletePlatformIssuerLimitRules :one
SELECT pandokos.delete_platform_issuer_limit_rules(sqlc.arg(issuer_id)::uuid,
           (SELECT COALESCE(array_agg(t.id ORDER BY t.id), ARRAY[]::uuid[]) FROM pandokos.tenant t),
           sqlc.arg(actor_id)::text, sqlc.arg(actor_name)::text, sqlc.arg(key_id)::text,
           sqlc.arg(request_id)::text)::bigint AS rules;
