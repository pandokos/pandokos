-- Usage ledger. Insert only for the gateway: no statement reads, changes or removes a usage row
-- in the request path. records is a JSON array passed as text and cast (ADR 0027).

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
