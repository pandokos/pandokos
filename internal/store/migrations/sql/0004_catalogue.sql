-- 0004_catalogue: providers, endpoints, models, model grants to tenants, the model restriction of
-- a tenant and of a key, price imports, price versions and their rates, catalogue notifications,
-- the seed rows of the providers openai and anthropic. Runs in one transaction as pandokos_owner.
-- Every name is schema-qualified.

-- Tells the instances that the catalogue changed. TG_ARGV[0]: 'global' (payload 'catalog', for a
-- statement trigger) or 'scoped' (payload 'catalog:<tenant>', from the row's tenant_id). The
-- payload never names a model, a price or a key.
CREATE FUNCTION pandokos.notify_catalog() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    rec jsonb;
BEGIN
    IF TG_ARGV[0] = 'global' THEN
        PERFORM pg_catalog.pg_notify('pandokos_cache', 'catalog');
        RETURN NULL;
    END IF;
    IF TG_OP = 'DELETE' THEN
        rec := to_jsonb(OLD);
    ELSE
        rec := to_jsonb(NEW);
    END IF;
    PERFORM pg_catalog.pg_notify('pandokos_cache', 'catalog:' || (rec ->> 'tenant_id'));
    RETURN NULL;
END
$$;

-- Rows that nobody changes once written.
CREATE FUNCTION pandokos.immutable_row() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    RAISE EXCEPTION 'rows of % are immutable', TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
END
$$;

-- Providers (global table with explicit policies). The name is the first path segment of the
-- inference listener and what credentials refer to: it never changes, and the names the
-- listener uses itself are refused.
CREATE TABLE pandokos.provider (
    id               uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    name             text        NOT NULL,
    display_name     text        NOT NULL DEFAULT '',
    description      text        NOT NULL DEFAULT '',
    api_family       text        NOT NULL,
    enabled          boolean     NOT NULL DEFAULT true,
    dataset_provider text        NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT provider_pkey PRIMARY KEY (id),
    CONSTRAINT provider_name_key UNIQUE (name),
    CONSTRAINT provider_name_format CHECK (name ~ '^[a-z][a-z0-9-]{0,62}$'),
    CONSTRAINT provider_name_reserved CHECK (name NOT IN ('healthz', 'readyz', 'v1')),
    CONSTRAINT provider_display_name_length CHECK (char_length(display_name) <= 200),
    CONSTRAINT provider_description_length CHECK (char_length(description) <= 500),
    CONSTRAINT provider_api_family_valid CHECK (api_family IN ('openai', 'anthropic')),
    CONSTRAINT provider_dataset_provider_format CHECK (dataset_provider ~ '^([a-z0-9][a-z0-9_.-]{0,62})?$')
);

CREATE FUNCTION pandokos.provider_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NEW.name <> OLD.name OR NEW.api_family <> OLD.api_family THEN
        RAISE EXCEPTION 'the name and the API family of a provider are immutable' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

-- Endpoints of providers (global table with explicit policies). base_url is what the native path
-- is appended to. A private endpoint is one the platform administrator declares to be on a
-- private network: only such an endpoint may use http, and only such an endpoint will be allowed
-- to reach a private address by the egress guard of sprint 3.1.
CREATE TABLE pandokos.provider_endpoint (
    id          uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    provider_id uuid        NOT NULL,
    name        text        NOT NULL,
    base_url    text        NOT NULL,
    region      text        NOT NULL DEFAULT '',
    private     boolean     NOT NULL DEFAULT false,
    enabled     boolean     NOT NULL DEFAULT true,
    is_default  boolean     NOT NULL DEFAULT false,
    description text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT provider_endpoint_pkey PRIMARY KEY (id),
    CONSTRAINT provider_endpoint_provider_fkey FOREIGN KEY (provider_id) REFERENCES pandokos.provider (id),
    CONSTRAINT provider_endpoint_name_key UNIQUE (provider_id, name),
    CONSTRAINT provider_endpoint_name_format CHECK (name ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    CONSTRAINT provider_endpoint_base_url_format CHECK (char_length(base_url) <= 2048
        AND base_url ~ '^https?://[^/?#@[:space:]]+(/[^?#[:space:]]*)?$' AND base_url !~ '/$'),
    CONSTRAINT provider_endpoint_plain_http_private CHECK (base_url ~ '^https://' OR private),
    CONSTRAINT provider_endpoint_region_format CHECK (region ~ '^([a-z0-9][a-z0-9-]{0,62})?$'),
    CONSTRAINT provider_endpoint_description_length CHECK (char_length(description) <= 500)
);
-- At most one default endpoint per provider: the one the gateway uses.
CREATE UNIQUE INDEX provider_endpoint_default_key ON pandokos.provider_endpoint (provider_id) WHERE is_default;

CREATE FUNCTION pandokos.provider_endpoint_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NEW.provider_id <> OLD.provider_id OR NEW.name <> OLD.name THEN
        RAISE EXCEPTION 'the provider and the name of an endpoint are immutable' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

-- Models (global table with explicit policies). name is what callers send; upstream_id is what
-- the provider receives. A model is available to every tenant, or to the tenants granted it.
CREATE TABLE pandokos.model (
    id                uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    provider_id       uuid        NOT NULL,
    name              text        NOT NULL,
    upstream_id       text        NOT NULL,
    display_name      text        NOT NULL DEFAULT '',
    description       text        NOT NULL DEFAULT '',
    status            text        NOT NULL DEFAULT 'active',
    availability      text        NOT NULL DEFAULT 'selected',
    capabilities      text[]      NOT NULL DEFAULT '{}',
    context_window    bigint,
    max_output_tokens bigint,
    price_override    boolean     NOT NULL DEFAULT false,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT model_pkey PRIMARY KEY (id),
    CONSTRAINT model_provider_fkey FOREIGN KEY (provider_id) REFERENCES pandokos.provider (id),
    CONSTRAINT model_name_key UNIQUE (provider_id, name),
    CONSTRAINT model_upstream_id_key UNIQUE (provider_id, upstream_id),
    CONSTRAINT model_name_format CHECK (name ~ '^[A-Za-z0-9][A-Za-z0-9._:@/+-]{0,127}$'),
    CONSTRAINT model_upstream_id_format CHECK (char_length(upstream_id) <= 256 AND upstream_id ~ '^[!-~]+$'),
    CONSTRAINT model_display_name_length CHECK (char_length(display_name) <= 200),
    CONSTRAINT model_description_length CHECK (char_length(description) <= 1000),
    CONSTRAINT model_status_valid CHECK (status IN ('active', 'deprecated', 'retired')),
    CONSTRAINT model_availability_valid CHECK (availability IN ('all', 'selected')),
    CONSTRAINT model_capabilities_valid CHECK (capabilities <@ ARRAY['text', 'vision', 'audio', 'tools', 'reasoning',
        'embeddings', 'structured_output', 'streaming']::text[]),
    CONSTRAINT model_context_window_range CHECK (context_window IS NULL OR context_window BETWEEN 1 AND 100000000),
    CONSTRAINT model_max_output_tokens_range CHECK (max_output_tokens IS NULL OR max_output_tokens BETWEEN 1 AND 100000000)
);

CREATE FUNCTION pandokos.model_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NEW.provider_id <> OLD.provider_id OR NEW.name <> OLD.name THEN
        RAISE EXCEPTION 'the provider and the name of a model are immutable' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

-- Seed: the two providers of tier 1 that have their own API family, their public endpoints and
-- a first set of their models, available to every tenant. Inserted before row-level security is
-- enabled, because the owner has no policy.
INSERT INTO pandokos.provider (name, display_name, api_family, dataset_provider) VALUES
    ('openai', 'OpenAI', 'openai', 'openai'),
    ('anthropic', 'Anthropic', 'anthropic', 'anthropic');

INSERT INTO pandokos.provider_endpoint (provider_id, name, base_url, is_default)
SELECT p.id, 'default', v.base_url, true
  FROM pandokos.provider p
  JOIN (VALUES ('openai', 'https://api.openai.com'), ('anthropic', 'https://api.anthropic.com')) AS v (provider, base_url)
    ON v.provider = p.name;

INSERT INTO pandokos.model (provider_id, name, upstream_id, availability, capabilities, context_window)
SELECT p.id, v.name, v.name, 'all', v.capabilities::text[], v.context_window
  FROM pandokos.provider p
  JOIN (VALUES
        ('openai', 'gpt-4o', '{text}', 128000),
        ('openai', 'gpt-4o-mini', '{text}', 128000),
        ('openai', 'gpt-4.1', '{text}', 1000000),
        ('openai', 'gpt-4.1-mini', '{text}', 1000000),
        ('openai', 'gpt-4.1-nano', '{text}', 1000000),
        ('openai', 'gpt-5', '{text}', 400000),
        ('openai', 'gpt-5-mini', '{text}', 400000),
        ('openai', 'gpt-5-nano', '{text}', 400000),
        ('openai', 'o3', '{text}', 200000),
        ('openai', 'o4-mini', '{text}', 200000),
        ('openai', 'text-embedding-3-small', '{embeddings}', 8192),
        ('openai', 'text-embedding-3-large', '{embeddings}', 8192),
        ('anthropic', 'claude-haiku-4-5', '{text}', 200000),
        ('anthropic', 'claude-sonnet-4-5', '{text}', 200000),
        ('anthropic', 'claude-opus-4-5', '{text}', 200000),
        ('anthropic', 'claude-sonnet-4-6', '{text}', 1000000),
        ('anthropic', 'claude-opus-4-6', '{text}', 1000000)
       ) AS v (provider, name, capabilities, context_window)
    ON v.provider = p.name;

CREATE TRIGGER provider_row_guard BEFORE INSERT OR UPDATE ON pandokos.provider
    FOR EACH ROW EXECUTE FUNCTION pandokos.global_row_guard();
CREATE TRIGGER provider_guard BEFORE UPDATE ON pandokos.provider
    FOR EACH ROW EXECUTE FUNCTION pandokos.provider_guard();
CREATE TRIGGER provider_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.provider
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.notify_catalog('global');

CREATE TRIGGER provider_endpoint_row_guard BEFORE INSERT OR UPDATE ON pandokos.provider_endpoint
    FOR EACH ROW EXECUTE FUNCTION pandokos.global_row_guard();
CREATE TRIGGER provider_endpoint_guard BEFORE UPDATE ON pandokos.provider_endpoint
    FOR EACH ROW EXECUTE FUNCTION pandokos.provider_endpoint_guard();
CREATE TRIGGER provider_endpoint_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.provider_endpoint
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.notify_catalog('global');

CREATE TRIGGER model_row_guard BEFORE INSERT OR UPDATE ON pandokos.model
    FOR EACH ROW EXECUTE FUNCTION pandokos.global_row_guard();
CREATE TRIGGER model_guard BEFORE UPDATE ON pandokos.model
    FOR EACH ROW EXECUTE FUNCTION pandokos.model_guard();
CREATE TRIGGER model_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.model
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.notify_catalog('global');

-- Every runtime role reads the catalogue in any session; only the admin role in platform scope
-- writes it.
ALTER TABLE pandokos.provider ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.provider FORCE ROW LEVEL SECURITY;
CREATE POLICY provider_read ON pandokos.provider
    FOR SELECT TO pandokos_gateway, pandokos_admin, pandokos_worker USING (true);
CREATE POLICY provider_platform_insert ON pandokos.provider
    FOR INSERT TO pandokos_admin WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY provider_platform_update ON pandokos.provider
    FOR UPDATE TO pandokos_admin USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY provider_platform_delete ON pandokos.provider
    FOR DELETE TO pandokos_admin USING ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.provider TO pandokos_gateway, pandokos_admin, pandokos_worker;
GRANT INSERT (name, display_name, description, api_family, enabled, dataset_provider) ON pandokos.provider TO pandokos_admin;
GRANT UPDATE (display_name, description, enabled, dataset_provider) ON pandokos.provider TO pandokos_admin;
GRANT DELETE ON pandokos.provider TO pandokos_admin;

ALTER TABLE pandokos.provider_endpoint ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.provider_endpoint FORCE ROW LEVEL SECURITY;
CREATE POLICY provider_endpoint_read ON pandokos.provider_endpoint
    FOR SELECT TO pandokos_gateway, pandokos_admin USING (true);
CREATE POLICY provider_endpoint_platform_insert ON pandokos.provider_endpoint
    FOR INSERT TO pandokos_admin WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY provider_endpoint_platform_update ON pandokos.provider_endpoint
    FOR UPDATE TO pandokos_admin USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY provider_endpoint_platform_delete ON pandokos.provider_endpoint
    FOR DELETE TO pandokos_admin USING ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.provider_endpoint TO pandokos_gateway, pandokos_admin;
GRANT INSERT (provider_id, name, base_url, region, private, enabled, is_default, description)
    ON pandokos.provider_endpoint TO pandokos_admin;
GRANT UPDATE (base_url, region, private, enabled, is_default, description) ON pandokos.provider_endpoint TO pandokos_admin;
GRANT DELETE ON pandokos.provider_endpoint TO pandokos_admin;

ALTER TABLE pandokos.model ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.model FORCE ROW LEVEL SECURITY;
CREATE POLICY model_read ON pandokos.model
    FOR SELECT TO pandokos_gateway, pandokos_admin, pandokos_worker USING (true);
CREATE POLICY model_platform_insert ON pandokos.model
    FOR INSERT TO pandokos_admin WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY model_platform_update ON pandokos.model
    FOR UPDATE TO pandokos_admin USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY model_platform_delete ON pandokos.model
    FOR DELETE TO pandokos_admin USING ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.model TO pandokos_gateway, pandokos_admin, pandokos_worker;
GRANT INSERT (provider_id, name, upstream_id, display_name, description, status, availability, capabilities,
              context_window, max_output_tokens)
    ON pandokos.model TO pandokos_admin;
GRANT UPDATE (upstream_id, display_name, description, status, availability, capabilities, context_window,
              max_output_tokens, price_override)
    ON pandokos.model TO pandokos_admin;
GRANT DELETE ON pandokos.model TO pandokos_admin;

-- Grants of models to tenants (global table with explicit policies: it describes tenants). A
-- tenant's session sees its own rows; the platform scope sees and changes all of them.
CREATE TABLE pandokos.model_tenant_grant (
    model_id   uuid        NOT NULL,
    tenant_id  uuid        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT model_tenant_grant_pkey PRIMARY KEY (model_id, tenant_id),
    CONSTRAINT model_tenant_grant_model_fkey FOREIGN KEY (model_id) REFERENCES pandokos.model (id) ON DELETE CASCADE,
    CONSTRAINT model_tenant_grant_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id)
);
CREATE INDEX model_tenant_grant_tenant_idx ON pandokos.model_tenant_grant (tenant_id);

CREATE TRIGGER model_tenant_grant_notify AFTER INSERT OR DELETE ON pandokos.model_tenant_grant
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_catalog('scoped');

ALTER TABLE pandokos.model_tenant_grant ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.model_tenant_grant FORCE ROW LEVEL SECURITY;
CREATE POLICY model_tenant_grant_self ON pandokos.model_tenant_grant
    FOR SELECT TO pandokos_gateway, pandokos_admin
    USING (tenant_id = (SELECT pandokos.current_tenant_id()));
CREATE POLICY model_tenant_grant_platform_select ON pandokos.model_tenant_grant
    FOR SELECT TO pandokos_admin USING ((SELECT pandokos.platform_scope()));
CREATE POLICY model_tenant_grant_platform_insert ON pandokos.model_tenant_grant
    FOR INSERT TO pandokos_admin WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY model_tenant_grant_platform_delete ON pandokos.model_tenant_grant
    FOR DELETE TO pandokos_admin USING ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.model_tenant_grant TO pandokos_gateway, pandokos_admin;
GRANT INSERT (model_id, tenant_id) ON pandokos.model_tenant_grant TO pandokos_admin;
GRANT DELETE ON pandokos.model_tenant_grant TO pandokos_admin;

-- The models a tenant restricts itself to (tenant-scoped table). No row: no restriction. A row:
-- only the models it names, as "<provider>/<model>".
CREATE TABLE pandokos.tenant_model_restriction (
    tenant_id  uuid        NOT NULL,
    id         uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    models     text[]      NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tenant_model_restriction_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT tenant_model_restriction_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT tenant_model_restriction_tenant_key UNIQUE (tenant_id),
    CONSTRAINT tenant_model_restriction_models_valid CHECK (cardinality(models) <= 512 AND array_position(models, NULL) IS NULL)
);

CREATE TRIGGER tenant_model_restriction_guard BEFORE INSERT OR UPDATE ON pandokos.tenant_model_restriction
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();
CREATE TRIGGER tenant_model_restriction_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.tenant_model_restriction
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_catalog('scoped');

ALTER TABLE pandokos.tenant_model_restriction ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.tenant_model_restriction FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.tenant_model_restriction
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.tenant_model_restriction TO pandokos_gateway, pandokos_admin;
GRANT INSERT (tenant_id, models) ON pandokos.tenant_model_restriction TO pandokos_admin;
GRANT UPDATE (models) ON pandokos.tenant_model_restriction TO pandokos_admin;
GRANT DELETE ON pandokos.tenant_model_restriction TO pandokos_admin;

-- The model allow-list of a virtual key (IAM-04): NULL, no restriction. Set when the key is
-- created and never changed, like its scopes.
ALTER TABLE pandokos.virtual_key ADD COLUMN models text[];
ALTER TABLE pandokos.virtual_key ADD CONSTRAINT virtual_key_models_valid
    CHECK (models IS NULL OR (cardinality(models) <= 64 AND array_position(models, NULL) IS NULL));

CREATE FUNCTION pandokos.virtual_key_models_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NEW.models IS DISTINCT FROM OLD.models THEN
        RAISE EXCEPTION 'the models of a key are set when the key is created' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER virtual_key_models_guard BEFORE UPDATE ON pandokos.virtual_key
    FOR EACH ROW EXECUTE FUNCTION pandokos.virtual_key_models_guard();

GRANT INSERT (models) ON pandokos.virtual_key TO pandokos_admin;

-- Runs of the price importer (global table with explicit policies, append-only). report holds
-- what could not be represented, per catalogue model; it never holds dataset text beyond
-- identifiers.
CREATE TABLE pandokos.price_import (
    id               uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    started_at       timestamptz NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    source           text        NOT NULL,
    source_ref       text        NOT NULL DEFAULT '',
    dataset_format   text        NOT NULL,
    digest           text        NOT NULL DEFAULT '',
    outcome          text        NOT NULL,
    error_class      text        NOT NULL DEFAULT '',
    actor_type       text        NOT NULL,
    actor_id         text        NOT NULL DEFAULT '',
    models_seen      integer     NOT NULL DEFAULT 0,
    versions_created integer     NOT NULL DEFAULT 0,
    unchanged        integer     NOT NULL DEFAULT 0,
    overridden       integer     NOT NULL DEFAULT 0,
    unmatched        integer     NOT NULL DEFAULT 0,
    skipped          integer     NOT NULL DEFAULT 0,
    report           jsonb       NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT price_import_pkey PRIMARY KEY (id),
    CONSTRAINT price_import_source_valid CHECK (source IN ('embedded', 'file', 'remote')),
    CONSTRAINT price_import_source_ref_length CHECK (char_length(source_ref) <= 2048),
    CONSTRAINT price_import_dataset_format_valid CHECK (dataset_format IN ('genai-prices-v2')),
    CONSTRAINT price_import_digest_format CHECK (digest ~ '^([0-9a-f]{64})?$'),
    CONSTRAINT price_import_outcome_valid CHECK (outcome IN ('success', 'failure')),
    CONSTRAINT price_import_error_class_format CHECK (error_class ~ '^[a-z_]{0,64}$'),
    CONSTRAINT price_import_actor_type_valid CHECK (actor_type IN ('platform', 'host')),
    CONSTRAINT price_import_counts CHECK (LEAST(models_seen, versions_created, unchanged, overridden, unmatched, skipped) >= 0),
    CONSTRAINT price_import_report_object CHECK (jsonb_typeof(report) = 'object' AND pg_column_size(report) <= 65536)
);
CREATE INDEX price_import_digest_idx ON pandokos.price_import (digest);

CREATE TRIGGER price_import_append_only BEFORE UPDATE OR DELETE ON pandokos.price_import
    FOR EACH ROW EXECUTE FUNCTION pandokos.append_only();
CREATE TRIGGER price_import_no_truncate BEFORE TRUNCATE ON pandokos.price_import
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only();

ALTER TABLE pandokos.price_import ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.price_import FORCE ROW LEVEL SECURITY;
CREATE POLICY price_import_platform_select ON pandokos.price_import
    FOR SELECT TO pandokos_admin, pandokos_worker USING ((SELECT pandokos.platform_scope()));
CREATE POLICY price_import_platform_insert ON pandokos.price_import
    FOR INSERT TO pandokos_admin, pandokos_worker WITH CHECK ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.price_import TO pandokos_admin, pandokos_worker;
GRANT INSERT (started_at, source, source_ref, dataset_format, digest, outcome, error_class, actor_type, actor_id,
              models_seen, versions_created, unchanged, overridden, unmatched, skipped, report)
    ON pandokos.price_import TO pandokos_admin, pandokos_worker;

-- Price versions (global table with explicit policies). A version never changes; it starts now
-- or later, never in the past; one that has not started yet can be removed. The version in force
-- at a time t is the one of the model with the greatest valid_from <= t.
CREATE TABLE pandokos.price_version (
    id         uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    model_id   uuid        NOT NULL,
    valid_from timestamptz NOT NULL DEFAULT now(),
    currency   text        NOT NULL,
    source     text        NOT NULL,
    import_id  uuid,
    note       text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT price_version_pkey PRIMARY KEY (id),
    CONSTRAINT price_version_model_fkey FOREIGN KEY (model_id) REFERENCES pandokos.model (id),
    CONSTRAINT price_version_import_fkey FOREIGN KEY (import_id) REFERENCES pandokos.price_import (id),
    CONSTRAINT price_version_valid_from_key UNIQUE (model_id, valid_from),
    CONSTRAINT price_version_currency_format CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT price_version_source_valid CHECK (source IN ('import', 'override')),
    CONSTRAINT price_version_import_presence CHECK ((source = 'import') = (import_id IS NOT NULL)),
    CONSTRAINT price_version_note_length CHECK (char_length(note) <= 500)
);

CREATE FUNCTION pandokos.price_version_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.created_at := now();
        IF NEW.valid_from < now() THEN
            RAISE EXCEPTION 'a price version cannot start in the past' USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'price versions are immutable' USING ERRCODE = 'restrict_violation';
    END IF;
    IF OLD.valid_from <= now() THEN
        RAISE EXCEPTION 'a price version that has started cannot be removed' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN OLD;
END
$$;

CREATE TRIGGER price_version_guard BEFORE INSERT OR UPDATE OR DELETE ON pandokos.price_version
    FOR EACH ROW EXECUTE FUNCTION pandokos.price_version_guard();
CREATE TRIGGER price_version_no_truncate BEFORE TRUNCATE ON pandokos.price_version
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only();
CREATE TRIGGER price_version_notify AFTER INSERT OR DELETE ON pandokos.price_version
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.notify_catalog('global');

-- Rates of a price version, per million units of a category. threshold is the start of a tier:
-- when the input tokens of a request reach it, its rate applies to every unit of the category.
CREATE TABLE pandokos.price_rate (
    price_version_id uuid           NOT NULL,
    category         text           NOT NULL,
    threshold        bigint         NOT NULL DEFAULT 0,
    rate             numeric(24, 12) NOT NULL,
    CONSTRAINT price_rate_pkey PRIMARY KEY (price_version_id, category, threshold),
    CONSTRAINT price_rate_version_fkey FOREIGN KEY (price_version_id) REFERENCES pandokos.price_version (id) ON DELETE CASCADE,
    CONSTRAINT price_rate_category_valid CHECK (category IN ('input', 'output', 'cache_read', 'cache_write',
        'cache_write_1h', 'input_audio', 'output_audio')),
    CONSTRAINT price_rate_threshold_range CHECK (threshold BETWEEN 0 AND 100000000),
    CONSTRAINT price_rate_rate_range CHECK (rate >= 0 AND rate <= 1000000)
);

CREATE TRIGGER price_rate_immutable BEFORE UPDATE ON pandokos.price_rate
    FOR EACH ROW EXECUTE FUNCTION pandokos.immutable_row();
CREATE TRIGGER price_rate_no_truncate BEFORE TRUNCATE ON pandokos.price_rate
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only();

ALTER TABLE pandokos.price_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.price_version FORCE ROW LEVEL SECURITY;
CREATE POLICY price_version_read ON pandokos.price_version
    FOR SELECT TO pandokos_gateway, pandokos_admin, pandokos_worker USING (true);
CREATE POLICY price_version_platform_insert ON pandokos.price_version
    FOR INSERT TO pandokos_admin, pandokos_worker WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY price_version_platform_delete ON pandokos.price_version
    FOR DELETE TO pandokos_admin USING ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.price_version TO pandokos_gateway, pandokos_admin, pandokos_worker;
GRANT INSERT (model_id, valid_from, currency, source, import_id, note) ON pandokos.price_version TO pandokos_admin, pandokos_worker;
GRANT DELETE ON pandokos.price_version TO pandokos_admin;

ALTER TABLE pandokos.price_rate ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.price_rate FORCE ROW LEVEL SECURITY;
CREATE POLICY price_rate_read ON pandokos.price_rate
    FOR SELECT TO pandokos_gateway, pandokos_admin, pandokos_worker USING (true);
CREATE POLICY price_rate_platform_insert ON pandokos.price_rate
    FOR INSERT TO pandokos_admin, pandokos_worker WITH CHECK ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.price_rate TO pandokos_gateway, pandokos_admin, pandokos_worker;
GRANT INSERT (price_version_id, category, threshold, rate) ON pandokos.price_rate TO pandokos_admin, pandokos_worker;

-- The worker imports prices in platform scope and audits what it changed.
CREATE POLICY platform_audit_event_worker_insert ON pandokos.platform_audit_event
    FOR INSERT TO pandokos_worker
    WITH CHECK ((SELECT pandokos.platform_scope()));
GRANT INSERT (actor_type, actor_id, actor_name, key_id, action, resource_type, resource_id, outcome, request_id, reason, detail)
    ON pandokos.platform_audit_event TO pandokos_worker;

-- A credential names a provider of the catalogue (sprint 2.1, section 11).
ALTER TABLE pandokos.provider_credential ADD CONSTRAINT provider_credential_provider_fkey
    FOREIGN KEY (provider) REFERENCES pandokos.provider (name);
