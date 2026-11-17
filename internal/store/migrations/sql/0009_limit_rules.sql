-- 0009_limit_rules: the limit rules a tenant sets for itself, its service accounts and its keys,
-- the ceilings the platform sets for tenants, and the notification that tells the instances that
-- rules changed (ADR 0049). Runs in one transaction as pandokos_owner. Every name is
-- schema-qualified.

-- Tells the instances that limit rules changed: 'limits:<tenant>' for rows of one tenant,
-- 'limits' for a platform default, which concerns every tenant.
CREATE FUNCTION pandokos.notify_limits() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    rec jsonb;
BEGIN
    IF TG_OP = 'DELETE' THEN
        rec := to_jsonb(OLD);
    ELSE
        rec := to_jsonb(NEW);
    END IF;
    IF rec ->> 'tenant_id' IS NULL THEN
        PERFORM pg_catalog.pg_notify('pandokos_cache', 'limits');
    ELSE
        PERFORM pg_catalog.pg_notify('pandokos_cache', 'limits:' || (rec ->> 'tenant_id'));
    END IF;
    RETURN NULL;
END
$$;

-- Rules of a tenant (tenant-scoped table). subject_kind 'tenant' is the tenant as a whole;
-- 'service_account' names a service account by its ID; 'key' names a virtual key by its key ID or,
-- for callers that present a token, a subject mapping by its ID. There is no foreign key to the
-- subject: a rule whose subject does not exist never matches a request. Only limit_value of a rule changes (column grants).
CREATE TABLE pandokos.limit_rule (
    tenant_id    uuid        NOT NULL,
    id           uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    subject_kind text        NOT NULL,
    subject_id   text        NOT NULL DEFAULT '',
    metric       text        NOT NULL,
    limit_value  bigint      NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT limit_rule_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT limit_rule_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT limit_rule_key UNIQUE (tenant_id, subject_kind, subject_id, metric),
    CONSTRAINT limit_rule_subject_kind_valid CHECK (subject_kind IN ('tenant', 'service_account', 'key')),
    CONSTRAINT limit_rule_subject_id_format CHECK (subject_id ~ '^[a-z0-9-]{0,64}$'
                                                   AND (subject_kind = 'tenant') = (subject_id = '')),
    CONSTRAINT limit_rule_metric_valid CHECK (metric IN ('requests_per_minute', 'tokens_per_minute', 'concurrent_requests')),
    CONSTRAINT limit_rule_limit_value_range CHECK (limit_value BETWEEN 1 AND 1000000000000)
);

CREATE TRIGGER limit_rule_guard BEFORE INSERT OR UPDATE ON pandokos.limit_rule
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();
CREATE TRIGGER limit_rule_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.limit_rule
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_limits();

ALTER TABLE pandokos.limit_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.limit_rule FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.limit_rule
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.limit_rule TO pandokos_gateway, pandokos_admin;
GRANT INSERT (tenant_id, subject_kind, subject_id, metric, limit_value) ON pandokos.limit_rule TO pandokos_admin;
GRANT UPDATE (limit_value) ON pandokos.limit_rule TO pandokos_admin;
GRANT DELETE ON pandokos.limit_rule TO pandokos_admin;

-- Ceilings the platform sets for tenants (global table with explicit policies: it describes
-- tenants). A row without tenant is the default for every tenant; a row with a tenant replaces the
-- default of the same metric for that tenant. A tenant's session sees the defaults and its own
-- rows; the platform scope sees and changes all of them.
CREATE TABLE pandokos.platform_limit_rule (
    id          uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    tenant_id   uuid,
    metric      text        NOT NULL,
    limit_value bigint      NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT platform_limit_rule_pkey PRIMARY KEY (id),
    CONSTRAINT platform_limit_rule_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT platform_limit_rule_key UNIQUE NULLS NOT DISTINCT (tenant_id, metric),
    CONSTRAINT platform_limit_rule_metric_valid CHECK (metric IN ('requests_per_minute', 'tokens_per_minute', 'concurrent_requests')),
    CONSTRAINT platform_limit_rule_limit_value_range CHECK (limit_value BETWEEN 1 AND 1000000000000)
);

CREATE TRIGGER platform_limit_rule_guard BEFORE INSERT OR UPDATE ON pandokos.platform_limit_rule
    FOR EACH ROW EXECUTE FUNCTION pandokos.global_row_guard();
CREATE TRIGGER platform_limit_rule_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.platform_limit_rule
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_limits();

ALTER TABLE pandokos.platform_limit_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.platform_limit_rule FORCE ROW LEVEL SECURITY;

CREATE POLICY platform_limit_rule_self ON pandokos.platform_limit_rule
    FOR SELECT TO pandokos_gateway, pandokos_admin
    USING (tenant_id IS NULL OR tenant_id = (SELECT pandokos.current_tenant_id()));
CREATE POLICY platform_limit_rule_platform_select ON pandokos.platform_limit_rule
    FOR SELECT TO pandokos_admin USING ((SELECT pandokos.platform_scope()));
CREATE POLICY platform_limit_rule_platform_insert ON pandokos.platform_limit_rule
    FOR INSERT TO pandokos_admin WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY platform_limit_rule_platform_update ON pandokos.platform_limit_rule
    FOR UPDATE TO pandokos_admin USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY platform_limit_rule_platform_delete ON pandokos.platform_limit_rule
    FOR DELETE TO pandokos_admin USING ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.platform_limit_rule TO pandokos_gateway, pandokos_admin;
GRANT INSERT (tenant_id, metric, limit_value) ON pandokos.platform_limit_rule TO pandokos_admin;
GRANT UPDATE (limit_value) ON pandokos.platform_limit_rule TO pandokos_admin;
GRANT DELETE ON pandokos.platform_limit_rule TO pandokos_admin;
