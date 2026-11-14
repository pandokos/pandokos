-- 0006_usage_ledger: the usage ledger (one row per authenticated inference request, no content),
-- partitioned by month on the request start, append-only, and the function that creates its
-- partitions. Runs in one transaction as pandokos_owner. Every name is schema-qualified.

-- Usage ledger (tenant-scoped table, append-only, partitioned). The gateway generates id and
-- started_at; a batch that is sent twice inserts each row once (the primary key).
CREATE TABLE pandokos.usage_record (
    tenant_id               uuid        NOT NULL,
    id                      uuid        NOT NULL,
    started_at              timestamptz NOT NULL,
    recorded_at             timestamptz NOT NULL DEFAULT now(),
    request_id              text        NOT NULL,
    trace_id                text        NOT NULL DEFAULT '',
    principal_kind          text        NOT NULL,
    principal_id            text        NOT NULL,
    key_id                  text        NOT NULL DEFAULT '',
    credential_id           uuid,
    funding                 text        NOT NULL,
    provider                text        NOT NULL,
    api_family              text        NOT NULL,
    operation               text        NOT NULL,
    endpoint                text        NOT NULL DEFAULT '',
    model                   text        NOT NULL DEFAULT '',
    model_id                uuid,
    upstream_model          text        NOT NULL DEFAULT '',
    stream                  boolean,
    outcome                 text        NOT NULL,
    error_code              text        NOT NULL DEFAULT '',
    upstream_status         integer,
    ttfb_ms                 bigint,
    duration_ms             bigint      NOT NULL,
    request_bytes           bigint,
    response_bytes          bigint,
    input_source            text        NOT NULL,
    output_source           text        NOT NULL,
    input_tokens            bigint,
    output_tokens           bigint,
    cache_read_tokens       bigint,
    cache_write_tokens      bigint,
    reasoning_tokens        bigint,
    other_units             jsonb       NOT NULL DEFAULT '{}'::jsonb,
    provider_usage          jsonb       NOT NULL DEFAULT '{}'::jsonb,
    estimated_input_tokens  bigint,
    estimated_output_tokens bigint,
    cost_status             text        NOT NULL,
    cost_reason             text        NOT NULL DEFAULT '',
    cost                    numeric(38,18),
    currency                text,
    price_version_id        uuid,
    cost_fallbacks          text[]      NOT NULL DEFAULT '{}'::text[],
    metadata                jsonb       NOT NULL DEFAULT '{}'::jsonb,
    metadata_dropped        integer     NOT NULL DEFAULT 0,
    CONSTRAINT usage_record_pkey PRIMARY KEY (tenant_id, started_at, id),
    CONSTRAINT usage_record_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT usage_record_request_id_format CHECK (request_id ~ '^[0-9a-f]{32}$'),
    CONSTRAINT usage_record_trace_id_format CHECK (trace_id ~ '^([0-9a-f]{32})?$'),
    CONSTRAINT usage_record_principal_kind_valid CHECK (principal_kind IN ('service_account')),
    CONSTRAINT usage_record_principal_id_format CHECK (principal_id ~ '^[0-9a-f-]{36}$'),
    CONSTRAINT usage_record_key_id_format CHECK (key_id ~ '^[A-Za-z0-9_-]{0,64}$'),
    CONSTRAINT usage_record_funding_valid CHECK (funding IN ('byok')),
    CONSTRAINT usage_record_provider_format CHECK (provider ~ '^[a-z0-9-]{1,63}$'),
    CONSTRAINT usage_record_api_family_format CHECK (api_family ~ '^[a-z0-9-]{1,63}$'),
    CONSTRAINT usage_record_operation_format CHECK (operation ~ '^[a-z0-9._-]{1,63}$'),
    CONSTRAINT usage_record_endpoint_length CHECK (char_length(endpoint) <= 63),
    CONSTRAINT usage_record_model_length CHECK (char_length(model) <= 200 AND char_length(upstream_model) <= 200),
    CONSTRAINT usage_record_outcome_valid CHECK (outcome IN ('ok', 'upstream_error', 'cancelled', 'timeout',
                                                             'rejected_limit', 'rejected_policy', 'internal_error')),
    CONSTRAINT usage_record_error_code_format CHECK (error_code ~ '^[a-z_]{0,40}$'),
    CONSTRAINT usage_record_upstream_status_range CHECK (upstream_status BETWEEN 100 AND 599),
    CONSTRAINT usage_record_times_not_negative CHECK (duration_ms >= 0 AND COALESCE(ttfb_ms, 0) >= 0
                                                      AND COALESCE(request_bytes, 0) >= 0 AND COALESCE(response_bytes, 0) >= 0),
    CONSTRAINT usage_record_source_valid CHECK (input_source IN ('provider', 'estimated', 'none')
                                                AND output_source IN ('provider', 'estimated', 'none')),
    -- Values reported by the provider and values estimated by Pandokos never share a column (USE-04).
    CONSTRAINT usage_record_input_by_source CHECK (
        (input_source = 'provider') = (input_tokens IS NOT NULL)
        AND (input_source = 'estimated') = (estimated_input_tokens IS NOT NULL)),
    CONSTRAINT usage_record_output_by_source CHECK (
        (output_source = 'provider') = (output_tokens IS NOT NULL)
        AND (output_source = 'estimated') = (estimated_output_tokens IS NOT NULL)),
    CONSTRAINT usage_record_detail_needs_provider CHECK (
        (cache_read_tokens IS NULL AND cache_write_tokens IS NULL) OR input_source = 'provider'),
    CONSTRAINT usage_record_reasoning_needs_provider CHECK (reasoning_tokens IS NULL OR output_source = 'provider'),
    CONSTRAINT usage_record_tokens_not_negative CHECK (
        COALESCE(input_tokens, 0) >= 0 AND COALESCE(output_tokens, 0) >= 0 AND COALESCE(cache_read_tokens, 0) >= 0
        AND COALESCE(cache_write_tokens, 0) >= 0 AND COALESCE(reasoning_tokens, 0) >= 0
        AND COALESCE(estimated_input_tokens, 0) >= 0 AND COALESCE(estimated_output_tokens, 0) >= 0),
    CONSTRAINT usage_record_other_units_valid CHECK (
        jsonb_typeof(other_units) = 'object' AND pg_column_size(other_units) <= 2048
        AND NOT jsonb_path_exists(other_units, '$.* ? (@.type() != "number")')),
    CONSTRAINT usage_record_provider_usage_valid CHECK (
        jsonb_typeof(provider_usage) = 'object' AND pg_column_size(provider_usage) <= 4096
        AND NOT jsonb_path_exists(provider_usage, '$.* ? (@.type() != "number")')),
    CONSTRAINT usage_record_cost_status_valid CHECK (cost_status IN ('known', 'unknown', 'none')),
    CONSTRAINT usage_record_cost_reason_valid CHECK (
        (cost_status = 'unknown') = (cost_reason IN ('no_price', 'category_unpriced', 'inconsistent_usage'))
        AND (cost_status <> 'unknown') = (cost_reason = '')),
    CONSTRAINT usage_record_cost_by_status CHECK (
        (cost_status = 'known') = (cost IS NOT NULL)
        AND (cost IS NULL OR (cost >= 0 AND currency IS NOT NULL AND price_version_id IS NOT NULL))),
    CONSTRAINT usage_record_currency_format CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT usage_record_cost_fallbacks_bound CHECK (cardinality(cost_fallbacks) <= 8),
    CONSTRAINT usage_record_metadata_valid CHECK (
        jsonb_typeof(metadata) = 'object' AND pg_column_size(metadata) <= 4096
        AND NOT jsonb_path_exists(metadata, '$.* ? (@.type() != "string")')),
    CONSTRAINT usage_record_metadata_dropped_range CHECK (metadata_dropped BETWEEN 0 AND 1000)
) PARTITION BY RANGE (started_at);

CREATE INDEX usage_record_request_idx ON pandokos.usage_record (tenant_id, request_id);

CREATE TRIGGER usage_record_append_only BEFORE UPDATE OR DELETE ON pandokos.usage_record
    FOR EACH ROW EXECUTE FUNCTION pandokos.append_only();
CREATE TRIGGER usage_record_no_truncate BEFORE TRUNCATE ON pandokos.usage_record
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only();

ALTER TABLE pandokos.usage_record ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.usage_record FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.usage_record
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

-- The gateway appends and reads nothing. The admin and worker roles read; nobody changes a row.
GRANT INSERT (tenant_id, id, started_at, request_id, trace_id, principal_kind, principal_id, key_id, credential_id,
              funding, provider, api_family, operation, endpoint, model, model_id, upstream_model, stream, outcome,
              error_code, upstream_status, ttfb_ms, duration_ms, request_bytes, response_bytes, input_source,
              output_source, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
              other_units, provider_usage, estimated_input_tokens, estimated_output_tokens, cost_status, cost_reason,
              cost, currency, price_version_id, cost_fallbacks, metadata, metadata_dropped)
    ON pandokos.usage_record TO pandokos_gateway;
GRANT SELECT ON pandokos.usage_record TO pandokos_admin, pandokos_worker;

-- Gives a partition the protection of its parent for anyone who names it directly: forced
-- row-level security with the canonical policy, and no TRUNCATE. No runtime role holds a
-- privilege on a partition; rows are reached through pandokos.usage_record only.
CREATE FUNCTION pandokos.protect_usage_partition(p_name text) RETURNS void
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    EXECUTE pg_catalog.format('ALTER TABLE pandokos.%I ENABLE ROW LEVEL SECURITY', p_name);
    EXECUTE pg_catalog.format('ALTER TABLE pandokos.%I FORCE ROW LEVEL SECURITY', p_name);
    EXECUTE pg_catalog.format(
        'CREATE POLICY tenant_isolation ON pandokos.%I USING (tenant_id = (SELECT pandokos.current_tenant_id())) '
        'WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()))', p_name);
    EXECUTE pg_catalog.format(
        'CREATE TRIGGER %I BEFORE TRUNCATE ON pandokos.%I FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only()',
        p_name || '_no_truncate', p_name);
    EXECUTE pg_catalog.format('REVOKE ALL ON pandokos.%I FROM PUBLIC', p_name);
END
$$;

-- Rows whose month has no partition land here instead of being refused. It is expected to stay
-- empty: ensure_usage_partitions reports when it is not.
CREATE TABLE pandokos.usage_record_default PARTITION OF pandokos.usage_record DEFAULT;
SELECT pandokos.protect_usage_partition('usage_record_default');

-- Creates the monthly partitions (UTC) from the month before the current one to p_ahead months
-- after it, and reports how many it created and whether the default partition has ever held a
-- row. A month whose rows are already in the default partition is skipped. Safe to call
-- in parallel and repeatedly. It runs with the rights of the owner, because runtime roles own
-- nothing and cannot create tables; the worker role is its only caller.
CREATE FUNCTION pandokos.ensure_usage_partitions(p_ahead integer, OUT created integer, OUT default_in_use boolean)
    LANGUAGE plpgsql
    SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_month date;
    v_name  text;
BEGIN
    IF p_ahead IS NULL OR p_ahead < 1 OR p_ahead > 12 THEN
        RAISE EXCEPTION 'p_ahead must be between 1 and 12' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM pg_catalog.set_config('lock_timeout', '5s', true);
    PERFORM pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('pandokos.usage_record.partitions', 0));
    created := 0;
    FOR i IN -1 .. p_ahead LOOP
        v_month := (pg_catalog.date_trunc('month', pg_catalog.timezone('UTC', pg_catalog.now()))
                    + pg_catalog.make_interval(months => i))::date;
        v_name := 'usage_record_' || pg_catalog.to_char(v_month, '"y"YYYY"m"MM');
        IF pg_catalog.to_regclass('pandokos.' || pg_catalog.quote_ident(v_name)) IS NULL THEN
            BEGIN
                EXECUTE pg_catalog.format(
                    'CREATE TABLE pandokos.%I PARTITION OF pandokos.usage_record FOR VALUES FROM (%L) TO (%L)',
                    v_name,
                    pg_catalog.to_char(v_month, 'YYYY-MM-DD') || ' 00:00:00+00',
                    pg_catalog.to_char((v_month + pg_catalog.make_interval(months => 1))::date, 'YYYY-MM-DD')
                        || ' 00:00:00+00');
                PERFORM pandokos.protect_usage_partition(v_name);
                created := created + 1;
            EXCEPTION WHEN check_violation THEN
                -- The default partition already holds rows of that month: they stay there, and
                -- the month keeps going to the default partition.
                NULL;
            END;
        END IF;
    END LOOP;
    default_in_use := pg_catalog.pg_relation_size('pandokos.usage_record_default'::pg_catalog.regclass) > 0;
END
$$;

GRANT EXECUTE ON FUNCTION pandokos.ensure_usage_partitions(integer) TO pandokos_worker;

-- The partitions of a new installation: last month to three months ahead.
SELECT pandokos.ensure_usage_partitions(3);
