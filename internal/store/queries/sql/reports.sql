-- Usage rollup, reports and exports (ADR 0064). Every statement runs in the session of one tenant,
-- except ListTenantsForWorker (platform scope, identifiers only) and PlatformRetention. The rollup
-- is written by the worker role only; the admin role reads it.

-- name: ListTenantsForWorker :many
SELECT t.id FROM pandokos.tenant t WHERE t.state IN ('active', 'suspended', 'deleting') ORDER BY t.id;

-- name: LockUsageRollup :one
INSERT INTO pandokos.usage_rollup_state AS s (tenant_id, watermark)
VALUES (pandokos.current_tenant_id(),
        COALESCE((SELECT min(u.recorded_at) FROM pandokos.usage_record u), now() - interval '1 minute'))
ON CONFLICT (tenant_id) DO UPDATE SET watermark = s.watermark
RETURNING s.watermark;

-- The records recorded in [watermark, upto) are added to the rollup and the watermark moves to
-- upto: at most seven days of recording at a time, never past one minute before now (longer than
-- any transaction of the ledger writer, which ends within writeTimeout).

-- name: RollUpUsage :one
WITH bound AS (
    SELECT GREATEST(sqlc.arg(watermark)::timestamptz,
                    LEAST(now() - interval '1 minute', sqlc.arg(watermark)::timestamptz + interval '7 days')) AS upto
), added AS (
    INSERT INTO pandokos.usage_rollup AS r (tenant_id, hour, principal_kind, principal_id, key_id, provider, model, model_id,
                                            currency, price_source, outcome, credential, requests, input_tokens,
                                            output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
                                            estimated_input_tokens, estimated_output_tokens, cost, cost_known, cost_unknown)
    SELECT pandokos.current_tenant_id(), date_bin('1 hour', u.started_at, '2000-01-01 00:00:00+00'), u.principal_kind,
           u.principal_id, u.key_id, u.provider, u.model, COALESCE(u.model_id::text, ''), COALESCE(u.currency, ''),
           COALESCE(u.price_source, CASE WHEN u.price_version_id IS NOT NULL THEN 'catalogue' END, ''), u.outcome,
           u.credential_id IS NOT NULL, count(*), COALESCE(sum(u.input_tokens), 0), COALESCE(sum(u.output_tokens), 0),
           COALESCE(sum(u.cache_read_tokens), 0), COALESCE(sum(u.cache_write_tokens), 0),
           COALESCE(sum(u.reasoning_tokens), 0), COALESCE(sum(u.estimated_input_tokens), 0),
           COALESCE(sum(u.estimated_output_tokens), 0), COALESCE(sum(u.cost), 0), count(u.cost),
           count(*) FILTER (WHERE u.cost_status = 'unknown')
      FROM pandokos.usage_record u, bound b
     WHERE u.recorded_at >= sqlc.arg(watermark)::timestamptz AND u.recorded_at < b.upto
     GROUP BY 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12
    ON CONFLICT ON CONSTRAINT usage_rollup_pkey DO UPDATE SET
           requests = r.requests + EXCLUDED.requests, input_tokens = r.input_tokens + EXCLUDED.input_tokens,
           output_tokens = r.output_tokens + EXCLUDED.output_tokens,
           cache_read_tokens = r.cache_read_tokens + EXCLUDED.cache_read_tokens,
           cache_write_tokens = r.cache_write_tokens + EXCLUDED.cache_write_tokens,
           reasoning_tokens = r.reasoning_tokens + EXCLUDED.reasoning_tokens,
           estimated_input_tokens = r.estimated_input_tokens + EXCLUDED.estimated_input_tokens,
           estimated_output_tokens = r.estimated_output_tokens + EXCLUDED.estimated_output_tokens,
           cost = r.cost + EXCLUDED.cost, cost_known = r.cost_known + EXCLUDED.cost_known,
           cost_unknown = r.cost_unknown + EXCLUDED.cost_unknown
    RETURNING 1
)
UPDATE pandokos.usage_rollup_state s SET watermark = b.upto, updated_at = now()
  FROM bound b
 WHERE s.tenant_id = pandokos.current_tenant_id()
RETURNING s.watermark, (SELECT count(*) FROM added)::bigint AS groups;

-- The report: the records whose start lies in [bounds[1], bounds[n]), grouped by the period
-- [bounds[i], bounds[i+1]) they started in and by the dimensions asked for, always by currency and
-- price source. Whole hours that no boundary cuts come from the rollup for the records recorded
-- before the watermark; everything else from the ledger. See ADR 0064 for why the sum is exact.

-- name: UsageReport :many
WITH w0 AS (
    SELECT sqlc.arg(bounds)::timestamptz[] AS bounds,
           COALESCE((SELECT s.watermark FROM pandokos.usage_rollup_state s), '-infinity'::timestamptz) AS wm
), w1 AS (
    SELECT w0.*, w0.bounds[1] AS f, w0.bounds[cardinality(w0.bounds)] AS t,
           CASE WHEN date_bin('1 hour', w0.bounds[1], '2000-01-01 00:00:00+00') = w0.bounds[1] THEN w0.bounds[1]
                ELSE date_bin('1 hour', w0.bounds[1], '2000-01-01 00:00:00+00') + interval '1 hour' END AS lo0,
           date_bin('1 hour', w0.bounds[cardinality(w0.bounds)], '2000-01-01 00:00:00+00') AS hi0
      FROM w0
), w AS (
    SELECT w1.bounds, w1.wm, w1.f, w1.t,
           CASE WHEN w1.hi0 > w1.lo0 THEN w1.lo0 ELSE w1.f END AS lo,
           CASE WHEN w1.hi0 > w1.lo0 THEN w1.hi0 ELSE w1.f END AS hi
      FROM w1
), d AS (
    SELECT COALESCE(array_agg(DISTINCT date_bin('1 hour', b, '2000-01-01 00:00:00+00')), '{}')::timestamptz[] AS dirty
      FROM w, unnest(w.bounds) AS b
     WHERE date_bin('1 hour', b, '2000-01-01 00:00:00+00') <> b
       AND date_bin('1 hour', b, '2000-01-01 00:00:00+00') >= w.lo
       AND date_bin('1 hour', b, '2000-01-01 00:00:00+00') < w.hi
), parts AS (
    SELECT r.hour AS at, r.provider, r.model, r.model_id, r.principal_kind, r.principal_id, r.key_id, r.currency,
           r.price_source, r.outcome, r.requests, r.input_tokens, r.output_tokens, r.cache_read_tokens,
           r.cache_write_tokens, r.reasoning_tokens, r.estimated_input_tokens, r.estimated_output_tokens, r.cost,
           r.cost_known, r.cost_unknown
      FROM pandokos.usage_rollup r, w, d
     WHERE r.hour >= w.lo AND r.hour < w.hi AND r.hour <> ALL (d.dirty)
    UNION ALL
    SELECT u.started_at, u.provider, u.model, COALESCE(u.model_id::text, ''), u.principal_kind, u.principal_id,
           u.key_id, COALESCE(u.currency, ''),
           COALESCE(u.price_source, CASE WHEN u.price_version_id IS NOT NULL THEN 'catalogue' END, ''), u.outcome, 1,
           COALESCE(u.input_tokens, 0), COALESCE(u.output_tokens, 0), COALESCE(u.cache_read_tokens, 0),
           COALESCE(u.cache_write_tokens, 0), COALESCE(u.reasoning_tokens, 0), COALESCE(u.estimated_input_tokens, 0),
           COALESCE(u.estimated_output_tokens, 0), COALESCE(u.cost, 0), (u.cost IS NOT NULL)::integer,
           (u.cost_status = 'unknown')::integer
      FROM pandokos.usage_record u, w, d
     WHERE (u.started_at >= w.f AND u.started_at < w.lo)
        OR (u.started_at >= w.hi AND u.started_at < w.t)
        OR (u.started_at >= w.lo AND u.started_at < w.hi
            AND (u.recorded_at >= w.wm OR date_bin('1 hour', u.started_at, '2000-01-01 00:00:00+00') = ANY (d.dirty)))
)
SELECT width_bucket(p.at, w.bounds)::integer AS period,
       (CASE WHEN sqlc.arg(by_provider)::boolean THEN p.provider ELSE '' END)::text AS provider,
       (CASE WHEN sqlc.arg(by_model)::boolean THEN p.model ELSE '' END)::text AS model,
       (CASE WHEN sqlc.arg(by_model)::boolean THEN p.model_id ELSE '' END)::text AS model_id,
       (CASE WHEN sqlc.arg(by_caller)::boolean THEN p.principal_kind ELSE '' END)::text AS principal_kind,
       (CASE WHEN sqlc.arg(by_caller)::boolean THEN p.principal_id ELSE '' END)::text AS principal_id,
       (CASE WHEN sqlc.arg(by_key)::boolean THEN p.key_id ELSE '' END)::text AS key_id,
       p.currency, p.price_source,
       COALESCE(sum(p.requests) FILTER (WHERE p.outcome <> 'rejected_limit'), 0)::bigint AS requests,
       COALESCE(sum(p.requests) FILTER (WHERE p.outcome = 'ok'), 0)::bigint AS requests_ok,
       COALESCE(sum(p.requests) FILTER (WHERE p.outcome = 'rejected_limit'), 0)::bigint AS rejected_limit,
       sum(p.input_tokens)::bigint AS input_tokens, sum(p.output_tokens)::bigint AS output_tokens,
       sum(p.cache_read_tokens)::bigint AS cache_read_tokens, sum(p.cache_write_tokens)::bigint AS cache_write_tokens,
       sum(p.reasoning_tokens)::bigint AS reasoning_tokens,
       sum(p.estimated_input_tokens)::bigint AS estimated_input_tokens,
       sum(p.estimated_output_tokens)::bigint AS estimated_output_tokens,
       sum(p.cost)::numeric AS cost, sum(p.cost_known)::bigint AS cost_known, sum(p.cost_unknown)::bigint AS cost_unknown
  FROM parts p, w
 WHERE (sqlc.arg(provider)::text = '' OR p.provider = sqlc.arg(provider)::text)
   AND (sqlc.arg(model)::text = '' OR p.model = sqlc.arg(model)::text)
 GROUP BY 1, 2, 3, 4, 5, 6, 7, 8, 9
 ORDER BY 1, 2, 3, 4, 5, 6, 7, 8, 9
 LIMIT sqlc.arg(max_rows)::integer + 1;

-- name: ExportUsageRecords :many
SELECT u.id, u.started_at, u.request_id, u.trace_id, u.principal_kind, u.principal_id, u.key_id, u.credential_id,
       u.provider, u.api_family, u.operation, u.endpoint, u.model, u.model_id, u.upstream_model, u.stream,
       u.outcome, u.error_code, u.upstream_status, u.ttfb_ms, u.duration_ms, u.request_bytes, u.response_bytes,
       u.input_source, u.output_source, u.input_tokens, u.output_tokens, u.cache_read_tokens, u.cache_write_tokens,
       u.reasoning_tokens, u.other_units, u.provider_usage, u.estimated_input_tokens, u.estimated_output_tokens,
       u.cost_status, u.cost_reason, u.cost, u.currency, u.price_version_id, u.cost_fallbacks, u.metadata,
       u.metadata_dropped, COALESCE(u.limit_state, '')::text AS limit_state,
       COALESCE(u.price_source, CASE WHEN u.price_version_id IS NOT NULL THEN 'catalogue' END, '')::text AS price_source
  FROM pandokos.usage_record u
 WHERE u.started_at >= sqlc.arg(from_at)::timestamptz
   AND u.started_at <  sqlc.arg(to_at)::timestamptz
   AND (sqlc.arg(provider)::text = '' OR u.provider = sqlc.arg(provider)::text)
   AND (NOT sqlc.arg(after)::boolean
        OR (u.started_at, u.id) > (sqlc.arg(after_started_at)::timestamptz, sqlc.arg(after_id)::uuid))
 ORDER BY u.started_at, u.id
 LIMIT sqlc.arg(page_size)::integer;

-- name: TenantRetention :one
SELECT usage_rows::bigint AS usage_rows, rollup_rows::bigint AS rollup_rows, audit_rows::bigint AS audit_rows
  FROM pandokos.tenant_retention(sqlc.arg(usage_months)::integer, sqlc.arg(audit_months)::integer);

-- name: PlatformRetention :one
SELECT partitions_dropped::integer AS partitions_dropped, audit_rows::bigint AS audit_rows
  FROM pandokos.platform_retention(sqlc.arg(usage_months)::integer, sqlc.arg(audit_months)::integer);

-- name: GetMetadataKeyPolicy :one
SELECT p.keys, p.updated_at FROM pandokos.metadata_key_policy p;

-- name: SetMetadataKeyPolicy :one
INSERT INTO pandokos.metadata_key_policy AS p (tenant_id, keys)
VALUES (pandokos.current_tenant_id(), sqlc.arg(keys)::text[])
ON CONFLICT (tenant_id) DO UPDATE SET keys = EXCLUDED.keys
RETURNING p.keys, p.updated_at;

-- name: ClearMetadataKeyPolicy :execrows
DELETE FROM pandokos.metadata_key_policy;
