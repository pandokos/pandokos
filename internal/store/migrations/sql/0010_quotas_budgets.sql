-- 0010_quotas_budgets: quotas and budgets per day and month, the scope of a rule (provider and
-- model), soft thresholds, the events they record, and the default output reservation of a model
-- (ADR 0050, 0052). Runs in one transaction as pandokos_owner. Every name is schema-qualified.

-- Rules of a tenant: six metrics per period, a scope, a currency for budgets, and thresholds.
-- provider and model are names of the catalogue ('' means every provider, every model of the
-- provider); there is no foreign key: a rule whose scope names nothing never matches a request.
-- limit_value of a cost metric is in millionths of the currency.
ALTER TABLE pandokos.limit_rule
    ADD COLUMN provider   text       NOT NULL DEFAULT '',
    ADD COLUMN model      text       NOT NULL DEFAULT '',
    ADD COLUMN currency   text       NOT NULL DEFAULT '',
    ADD COLUMN enforce    boolean    NOT NULL DEFAULT true,
    ADD COLUMN thresholds smallint[] NOT NULL DEFAULT '{}'::smallint[],
    DROP CONSTRAINT limit_rule_key,
    DROP CONSTRAINT limit_rule_metric_valid,
    DROP CONSTRAINT limit_rule_limit_value_range,
    ADD CONSTRAINT limit_rule_key UNIQUE (tenant_id, subject_kind, subject_id, provider, model, metric),
    ADD CONSTRAINT limit_rule_metric_valid CHECK (metric IN (
        'requests_per_minute', 'tokens_per_minute', 'concurrent_requests',
        'requests_per_day', 'requests_per_month', 'tokens_per_day', 'tokens_per_month',
        'cost_per_day', 'cost_per_month')),
    ADD CONSTRAINT limit_rule_limit_value_range CHECK (
        limit_value >= 1
        AND limit_value <= CASE WHEN metric IN ('cost_per_day', 'cost_per_month')
                                THEN 1000000000000000 ELSE 1000000000000 END),
    ADD CONSTRAINT limit_rule_scope_format CHECK (
        provider ~ '^([a-z][a-z0-9-]{0,62})?$'
        AND model ~ '^([A-Za-z0-9][A-Za-z0-9._:@/+-]{0,127})?$'
        AND (model = '' OR provider <> '')),
    ADD CONSTRAINT limit_rule_currency_by_metric CHECK (
        (metric IN ('cost_per_day', 'cost_per_month') AND currency ~ '^[A-Z]{3}$')
        OR (metric NOT IN ('cost_per_day', 'cost_per_month') AND currency = '')),
    ADD CONSTRAINT limit_rule_thresholds_valid CHECK (
        cardinality(thresholds) <= 4
        AND array_position(thresholds, NULL) IS NULL
        AND 1 <= ALL (thresholds) AND 100 >= ALL (thresholds)),
    ADD CONSTRAINT limit_rule_soft_by_metric CHECK (
        (enforce OR cardinality(thresholds) >= 1)
        AND (metric NOT IN ('requests_per_minute', 'tokens_per_minute', 'concurrent_requests')
             OR (enforce AND cardinality(thresholds) = 0)));

GRANT INSERT (provider, model, currency, enforce, thresholds) ON pandokos.limit_rule TO pandokos_admin;
GRANT UPDATE (enforce, thresholds) ON pandokos.limit_rule TO pandokos_admin;

-- Ceilings of the platform: the same columns and rules.
ALTER TABLE pandokos.platform_limit_rule
    ADD COLUMN provider   text       NOT NULL DEFAULT '',
    ADD COLUMN model      text       NOT NULL DEFAULT '',
    ADD COLUMN currency   text       NOT NULL DEFAULT '',
    ADD COLUMN enforce    boolean    NOT NULL DEFAULT true,
    ADD COLUMN thresholds smallint[] NOT NULL DEFAULT '{}'::smallint[],
    DROP CONSTRAINT platform_limit_rule_key,
    DROP CONSTRAINT platform_limit_rule_metric_valid,
    DROP CONSTRAINT platform_limit_rule_limit_value_range,
    ADD CONSTRAINT platform_limit_rule_key UNIQUE NULLS NOT DISTINCT (tenant_id, provider, model, metric),
    ADD CONSTRAINT platform_limit_rule_metric_valid CHECK (metric IN (
        'requests_per_minute', 'tokens_per_minute', 'concurrent_requests',
        'requests_per_day', 'requests_per_month', 'tokens_per_day', 'tokens_per_month',
        'cost_per_day', 'cost_per_month')),
    ADD CONSTRAINT platform_limit_rule_limit_value_range CHECK (
        limit_value >= 1
        AND limit_value <= CASE WHEN metric IN ('cost_per_day', 'cost_per_month')
                                THEN 1000000000000000 ELSE 1000000000000 END),
    ADD CONSTRAINT platform_limit_rule_scope_format CHECK (
        provider ~ '^([a-z][a-z0-9-]{0,62})?$'
        AND model ~ '^([A-Za-z0-9][A-Za-z0-9._:@/+-]{0,127})?$'
        AND (model = '' OR provider <> '')),
    ADD CONSTRAINT platform_limit_rule_currency_by_metric CHECK (
        (metric IN ('cost_per_day', 'cost_per_month') AND currency ~ '^[A-Z]{3}$')
        OR (metric NOT IN ('cost_per_day', 'cost_per_month') AND currency = '')),
    ADD CONSTRAINT platform_limit_rule_thresholds_valid CHECK (
        cardinality(thresholds) <= 4
        AND array_position(thresholds, NULL) IS NULL
        AND 1 <= ALL (thresholds) AND 100 >= ALL (thresholds)),
    ADD CONSTRAINT platform_limit_rule_soft_by_metric CHECK (
        (enforce OR cardinality(thresholds) >= 1)
        AND (metric NOT IN ('requests_per_minute', 'tokens_per_minute', 'concurrent_requests')
             OR (enforce AND cardinality(thresholds) = 0)));

GRANT INSERT (provider, model, currency, enforce, thresholds) ON pandokos.platform_limit_rule TO pandokos_admin;
GRANT UPDATE (enforce, thresholds) ON pandokos.platform_limit_rule TO pandokos_admin;

-- What a quota or a budget recorded: a threshold that was reached, or a limit that refused a
-- request for the first time in a period (tenant-scoped table, append-only). One row per rule,
-- period, kind and percentage, whoever records it and however often: the unique key is what makes
-- recording idempotent. rule_id names a row of limit_rule (rule_level 'tenant') or of
-- platform_limit_rule ('platform'), without a foreign key: the event outlives the rule. percent is
-- 0 for 'exhausted'. limit_value and observed are in the unit of the metric (requests, tokens, or
-- the currency; limit_value of a cost metric in millionths). No request, content or caller is
-- named. Sprint 6.4 writes the outbox row in the transaction that inserts here.
CREATE TABLE pandokos.limit_event (
    tenant_id    uuid           NOT NULL,
    id           uuid           NOT NULL DEFAULT pandokos.uuid_v7(),
    kind         text           NOT NULL,
    rule_id      uuid           NOT NULL,
    rule_level   text           NOT NULL,
    subject_kind text           NOT NULL,
    subject_id   text           NOT NULL DEFAULT '',
    provider     text           NOT NULL DEFAULT '',
    model        text           NOT NULL DEFAULT '',
    metric       text           NOT NULL,
    currency     text           NOT NULL DEFAULT '',
    period_start timestamptz    NOT NULL,
    period_end   timestamptz    NOT NULL,
    percent      smallint       NOT NULL DEFAULT 0,
    limit_value  bigint         NOT NULL,
    observed     numeric(38,18) NOT NULL,
    occurred_at  timestamptz    NOT NULL DEFAULT now(),
    CONSTRAINT limit_event_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT limit_event_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT limit_event_once UNIQUE (tenant_id, rule_id, period_start, kind, percent),
    CONSTRAINT limit_event_kind_valid CHECK (kind IN ('threshold_reached', 'exhausted')),
    CONSTRAINT limit_event_percent_by_kind CHECK (
        (kind = 'threshold_reached' AND percent BETWEEN 1 AND 100) OR (kind = 'exhausted' AND percent = 0)),
    CONSTRAINT limit_event_rule_level_valid CHECK (rule_level IN ('tenant', 'platform')),
    CONSTRAINT limit_event_subject_kind_valid CHECK (subject_kind IN ('tenant', 'service_account', 'key')),
    CONSTRAINT limit_event_subject_id_format CHECK (subject_id ~ '^[A-Za-z0-9_-]{0,64}$'
                                                    AND (subject_kind = 'tenant') = (subject_id = '')),
    CONSTRAINT limit_event_scope_format CHECK (
        provider ~ '^([a-z][a-z0-9-]{0,62})?$'
        AND model ~ '^([A-Za-z0-9][A-Za-z0-9._:@/+-]{0,127})?$'
        AND (model = '' OR provider <> '')),
    CONSTRAINT limit_event_metric_valid CHECK (metric IN (
        'requests_per_day', 'requests_per_month', 'tokens_per_day', 'tokens_per_month',
        'cost_per_day', 'cost_per_month')),
    CONSTRAINT limit_event_currency_by_metric CHECK (
        (metric IN ('cost_per_day', 'cost_per_month') AND currency ~ '^[A-Z]{3}$')
        OR (metric NOT IN ('cost_per_day', 'cost_per_month') AND currency = '')),
    CONSTRAINT limit_event_period_valid CHECK (period_end > period_start),
    CONSTRAINT limit_event_values_valid CHECK (limit_value >= 1 AND observed >= 0)
);

CREATE TRIGGER limit_event_append_only BEFORE UPDATE OR DELETE ON pandokos.limit_event
    FOR EACH ROW EXECUTE FUNCTION pandokos.append_only();
CREATE TRIGGER limit_event_no_truncate BEFORE TRUNCATE ON pandokos.limit_event
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only();

ALTER TABLE pandokos.limit_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.limit_event FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.limit_event
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

-- The gateway appends and reads nothing; the admin role reads; nobody changes a row.
GRANT INSERT (tenant_id, kind, rule_id, rule_level, subject_kind, subject_id, provider, model, metric, currency,
              period_start, period_end, percent, limit_value, observed)
    ON pandokos.limit_event TO pandokos_gateway;
GRANT SELECT ON pandokos.limit_event TO pandokos_admin;

-- The output tokens reserved per choice for a request to this model that sets no output limit;
-- NULL: the configured default (limits.default_output_tokens).
ALTER TABLE pandokos.model
    ADD COLUMN default_output_tokens bigint,
    ADD CONSTRAINT model_default_output_tokens_range CHECK (
        default_output_tokens IS NULL OR default_output_tokens BETWEEN 1 AND 1000000);

GRANT INSERT (default_output_tokens) ON pandokos.model TO pandokos_admin;
GRANT UPDATE (default_output_tokens) ON pandokos.model TO pandokos_admin;
