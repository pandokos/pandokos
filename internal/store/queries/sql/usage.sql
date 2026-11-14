-- Usage ledger. Insert only for the gateway; the admin role lists a tenant's records in its
-- session (ListUsageRecords). No statement changes or removes a row. records is a JSON array
-- passed as text and cast (ADR 0027).

-- name: InsertUsageRecords :execrows
INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, trace_id, principal_kind, principal_id, key_id,
                                   credential_id, funding, provider, api_family, operation, endpoint, model, model_id,
                                   upstream_model, stream, outcome, error_code, upstream_status, ttfb_ms, duration_ms,
                                   request_bytes, response_bytes, input_source, output_source, input_tokens,
                                   output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, other_units,
                                   provider_usage, estimated_input_tokens, estimated_output_tokens, cost_status,
                                   cost_reason, cost, currency, price_version_id, cost_fallbacks, metadata,
                                   metadata_dropped)
SELECT pandokos.current_tenant_id(), r.id, r.started_at, r.request_id, r.trace_id, r.principal_kind, r.principal_id,
       r.key_id, r.credential_id, r.funding, r.provider, r.api_family, r.operation, r.endpoint, r.model, r.model_id,
       r.upstream_model, r.stream, r.outcome, r.error_code, r.upstream_status, r.ttfb_ms, r.duration_ms,
       r.request_bytes, r.response_bytes, r.input_source, r.output_source, r.input_tokens, r.output_tokens,
       r.cache_read_tokens, r.cache_write_tokens, r.reasoning_tokens, r.other_units, r.provider_usage,
       r.estimated_input_tokens, r.estimated_output_tokens, r.cost_status, r.cost_reason, r.cost, r.currency,
       r.price_version_id, COALESCE(r.cost_fallbacks, '{}'::text[]), r.metadata, r.metadata_dropped
  FROM jsonb_to_recordset(sqlc.arg(records)::text::jsonb) AS r(
       id uuid, started_at timestamptz, request_id text, trace_id text, principal_kind text, principal_id text,
       key_id text, credential_id uuid, funding text, provider text, api_family text, operation text, endpoint text,
       model text, model_id uuid, upstream_model text, stream boolean, outcome text, error_code text,
       upstream_status integer, ttfb_ms bigint, duration_ms bigint, request_bytes bigint, response_bytes bigint,
       input_source text, output_source text, input_tokens bigint, output_tokens bigint, cache_read_tokens bigint,
       cache_write_tokens bigint, reasoning_tokens bigint, other_units jsonb, provider_usage jsonb,
       estimated_input_tokens bigint, estimated_output_tokens bigint, cost_status text, cost_reason text,
       cost numeric, currency text, price_version_id uuid, cost_fallbacks text[], metadata jsonb,
       metadata_dropped integer)
ON CONFLICT DO NOTHING;

-- name: EnsureUsagePartitions :one
SELECT created::integer AS created, default_in_use::boolean AS default_in_use
  FROM pandokos.ensure_usage_partitions(sqlc.arg(ahead)::integer);

-- name: ListUsageRecords :many
SELECT u.id, u.started_at, u.request_id, u.trace_id, u.principal_kind, u.principal_id, u.key_id, u.credential_id,
       u.funding, u.provider, u.api_family, u.operation, u.endpoint, u.model, u.model_id, u.upstream_model, u.stream,
       u.outcome, u.error_code, u.upstream_status, u.ttfb_ms, u.duration_ms, u.request_bytes, u.response_bytes,
       u.input_source, u.output_source, u.input_tokens, u.output_tokens, u.cache_read_tokens, u.cache_write_tokens,
       u.reasoning_tokens, u.other_units, u.provider_usage, u.estimated_input_tokens, u.estimated_output_tokens,
       u.cost_status, u.cost_reason, u.cost, u.currency, u.price_version_id, u.cost_fallbacks, u.metadata,
       u.metadata_dropped
  FROM pandokos.usage_record u
 WHERE u.started_at >= sqlc.arg(from_at)::timestamptz
   AND u.started_at <  sqlc.arg(to_at)::timestamptz
   AND (sqlc.arg(provider)::text = '' OR u.provider = sqlc.arg(provider)::text)
   AND (NOT sqlc.arg(after)::boolean
        OR (u.started_at, u.id) < (sqlc.arg(after_started_at)::timestamptz, sqlc.arg(after_id)::uuid))
 ORDER BY u.started_at DESC, u.id DESC
 LIMIT sqlc.arg(page_size)::integer;
