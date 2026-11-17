-- Limit rules. Both statements run on the gateway pool in the session of the tenant whose rules
-- are loaded (store.DB.WithTenant). Neither names the tenant in a WHERE clause on purpose: the
-- policies do (TEN-04). In that session platform_limit_rule shows the defaults and the tenant's
-- own rows, nothing of another tenant.

-- name: ListLimitRules :many
SELECT r.subject_kind, r.subject_id, r.metric, r.limit_value
  FROM pandokos.limit_rule r
 ORDER BY r.subject_kind, r.subject_id, r.metric
 LIMIT 5001;

-- name: ListPlatformLimitRules :many
SELECT (r.tenant_id IS NULL)::boolean AS is_default, r.metric, r.limit_value
  FROM pandokos.platform_limit_rule r
 ORDER BY r.tenant_id NULLS FIRST, r.metric;
