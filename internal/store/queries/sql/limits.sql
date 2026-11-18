-- Limit rules, the events of quotas and budgets, and the ledger aggregates their counters are
-- rebuilt from. Every statement runs in a store transaction helper; none names the tenant in a
-- WHERE clause: the policies do (TEN-04).

-- Gateway pool, session of the tenant whose rules are loaded. platform_limit_rule shows the
-- defaults and the tenant's own rows in that session.

-- name: ListLimitRules :many
SELECT r.id, r.subject_kind, r.subject_id, r.provider, r.model, r.metric, r.limit_value, r.currency, r.enforce,
       r.thresholds
  FROM pandokos.limit_rule r
 ORDER BY r.subject_kind, r.subject_id, r.provider, r.model, r.metric
 LIMIT 5001;

-- name: ListPlatformLimitRules :many
SELECT r.id, (r.tenant_id IS NULL)::boolean AS is_default, r.provider, r.model, r.metric, r.limit_value, r.currency,
       r.enforce, r.thresholds
  FROM pandokos.platform_limit_rule r
 ORDER BY r.tenant_id NULLS FIRST, r.provider, r.model, r.metric;

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

-- Worker pool (counters) or admin pool (capability check), session of the tenant. What the
-- ledger holds for the requests that started in [from_time, to_time), by the dimensions a rule
-- can name. Rows that started before live_from are summed with minute 0; later rows by the minute
-- they started in (Unix time / 60). A request counts when a credential was opened for it; its
-- tokens are those the cost was computed on; its cost counts when it is known.

-- name: AggregateUsageForLimits :many
SELECT (CASE WHEN u.started_at >= sqlc.arg(live_from)::timestamptz
             THEN floor(extract(epoch FROM u.started_at) / 60)::bigint ELSE 0 END)::bigint AS minute,
       u.principal_id, u.key_id, u.provider, u.model, COALESCE(u.currency, '')::text AS currency,
       count(*) FILTER (WHERE u.credential_id IS NOT NULL)::bigint AS requests,
       COALESCE(sum(COALESCE(u.input_tokens, u.estimated_input_tokens, 0)
                    + COALESCE(u.output_tokens, u.estimated_output_tokens, 0)), 0)::numeric AS tokens,
       COALESCE(sum(u.cost), 0)::numeric AS cost
  FROM pandokos.usage_record u
 WHERE u.started_at >= sqlc.arg(from_time)::timestamptz
   AND u.started_at < sqlc.arg(to_time)::timestamptz
 GROUP BY 1, u.principal_id, u.key_id, u.provider, u.model, COALESCE(u.currency, '')
HAVING count(*) FILTER (WHERE u.credential_id IS NOT NULL) > 0;

-- Administration API, tenant session (admin pool).

-- name: LockLimitRules :exec
SELECT pg_catalog.pg_advisory_xact_lock(
           pg_catalog.hashtextextended('pandokos.limit_rule:' || pandokos.current_tenant_id()::text, 0));

-- name: CountLimitRules :one
SELECT count(*)::bigint FROM pandokos.limit_rule;

-- name: LimitSubjectExists :one
SELECT (CASE sqlc.arg(subject_kind)::text
            WHEN 'tenant' THEN true
            WHEN 'service_account' THEN EXISTS (SELECT 1 FROM pandokos.service_account s
                                                 WHERE s.id::text = sqlc.arg(subject_id)::text)
            WHEN 'key' THEN EXISTS (SELECT 1 FROM pandokos.virtual_key k WHERE k.key_id = sqlc.arg(subject_id)::text)
                            OR EXISTS (SELECT 1 FROM pandokos.subject_mapping m
                                        WHERE m.id::text = sqlc.arg(subject_id)::text)
            ELSE false END)::boolean AS found;

-- In a tenant session a model is found when the tenant can see it; in the platform scope when
-- it exists.

-- name: LimitScopeExists :one
SELECT EXISTS (SELECT 1 FROM pandokos.provider p
                WHERE p.name = sqlc.arg(provider)::text
                  AND (sqlc.arg(model)::text = ''
                       OR EXISTS (SELECT 1 FROM pandokos.model m
                                   WHERE m.provider_id = p.id AND m.name = sqlc.arg(model)::text)))::boolean AS found;

-- name: CreateLimitRule :one
INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, subject_id, provider, model, metric, limit_value, currency,
                                 enforce, thresholds)
VALUES (pandokos.current_tenant_id(), sqlc.arg(subject_kind)::text, sqlc.arg(subject_id)::text,
        sqlc.arg(provider)::text, sqlc.arg(model)::text, sqlc.arg(metric)::text, sqlc.arg(limit_value)::bigint,
        sqlc.arg(currency)::text, sqlc.arg(enforce)::boolean, sqlc.arg(thresholds)::smallint[])
RETURNING id, subject_kind, subject_id, provider, model, metric, limit_value, currency, enforce, thresholds,
          created_at, updated_at;

-- name: GetLimitRule :one
SELECT r.id, r.subject_kind, r.subject_id, r.provider, r.model, r.metric, r.limit_value, r.currency, r.enforce,
       r.thresholds, r.created_at, r.updated_at
  FROM pandokos.limit_rule r
 WHERE r.id = sqlc.arg(id)::uuid;

-- name: LockLimitRule :one
SELECT r.id, r.subject_kind, r.subject_id, r.provider, r.model, r.metric, r.limit_value, r.currency, r.enforce,
       r.thresholds, r.created_at, r.updated_at
  FROM pandokos.limit_rule r
 WHERE r.id = sqlc.arg(id)::uuid
   FOR UPDATE;

-- name: ListLimitRulesPage :many
SELECT r.id, r.subject_kind, r.subject_id, r.provider, r.model, r.metric, r.limit_value, r.currency, r.enforce,
       r.thresholds, r.created_at, r.updated_at
  FROM pandokos.limit_rule r
 WHERE (sqlc.arg(subject_kind)::text = '' OR r.subject_kind = sqlc.arg(subject_kind)::text)
   AND (sqlc.arg(subject_id)::text = '' OR r.subject_id = sqlc.arg(subject_id)::text)
   AND (sqlc.arg(metric)::text = '' OR r.metric = sqlc.arg(metric)::text)
   AND (sqlc.arg(provider)::text = '' OR r.provider = sqlc.arg(provider)::text)
   AND (sqlc.arg(model)::text = '' OR r.model = sqlc.arg(model)::text)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN r.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN r.id < sqlc.arg(after_id)::uuid
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN r.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN r.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: UpdateLimitRule :one
UPDATE pandokos.limit_rule
   SET limit_value = sqlc.arg(limit_value)::bigint,
       enforce     = sqlc.arg(enforce)::boolean,
       thresholds  = sqlc.arg(thresholds)::smallint[]
 WHERE id = sqlc.arg(id)::uuid
RETURNING id, subject_kind, subject_id, provider, model, metric, limit_value, currency, enforce, thresholds,
          created_at, updated_at;

-- name: DeleteLimitRule :one
DELETE FROM pandokos.limit_rule
 WHERE id = sqlc.arg(id)::uuid
RETURNING id, subject_kind, subject_id, provider, model, metric, limit_value, currency, enforce, thresholds,
          created_at, updated_at;

-- The ceilings that apply to the tenant of the session: the defaults and its own rows.

-- name: ListTenantPlatformLimitRules :many
SELECT r.id, (r.tenant_id IS NULL)::boolean AS is_default, r.provider, r.model, r.metric, r.limit_value, r.currency,
       r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM pandokos.platform_limit_rule r
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
INSERT INTO pandokos.platform_limit_rule (tenant_id, provider, model, metric, limit_value, currency, enforce,
                                          thresholds)
VALUES (sqlc.narg(tenant_id)::uuid, sqlc.arg(provider)::text, sqlc.arg(model)::text, sqlc.arg(metric)::text,
        sqlc.arg(limit_value)::bigint, sqlc.arg(currency)::text, sqlc.arg(enforce)::boolean,
        sqlc.arg(thresholds)::smallint[])
RETURNING id, tenant_id, provider, model, metric, limit_value, currency, enforce, thresholds, created_at, updated_at;

-- name: GetPlatformLimitRule :one
SELECT r.id, r.tenant_id, r.provider, r.model, r.metric, r.limit_value, r.currency, r.enforce, r.thresholds,
       r.created_at, r.updated_at
  FROM pandokos.platform_limit_rule r
 WHERE r.id = sqlc.arg(id)::uuid;

-- name: LockPlatformLimitRule :one
SELECT r.id, r.tenant_id, r.provider, r.model, r.metric, r.limit_value, r.currency, r.enforce, r.thresholds,
       r.created_at, r.updated_at
  FROM pandokos.platform_limit_rule r
 WHERE r.id = sqlc.arg(id)::uuid
   FOR UPDATE;

-- applies_to: '' every row, 'default' the rows without tenant, otherwise a tenant ID.

-- name: ListPlatformLimitRulesPage :many
SELECT r.id, r.tenant_id, r.provider, r.model, r.metric, r.limit_value, r.currency, r.enforce, r.thresholds,
       r.created_at, r.updated_at
  FROM pandokos.platform_limit_rule r
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
UPDATE pandokos.platform_limit_rule
   SET limit_value = sqlc.arg(limit_value)::bigint,
       enforce     = sqlc.arg(enforce)::boolean,
       thresholds  = sqlc.arg(thresholds)::smallint[]
 WHERE id = sqlc.arg(id)::uuid
RETURNING id, tenant_id, provider, model, metric, limit_value, currency, enforce, thresholds, created_at, updated_at;

-- name: DeletePlatformLimitRule :one
DELETE FROM pandokos.platform_limit_rule
 WHERE id = sqlc.arg(id)::uuid
RETURNING id, tenant_id, provider, model, metric, limit_value, currency, enforce, thresholds, created_at, updated_at;
