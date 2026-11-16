-- 0008_trusted_issuers: registrations of trusted token issuers (tenant-bound and platform-level),
-- the tenant list of platform-level registrations, subject mappings, the directory that selects a
-- registration by issuer and audience, the functions the gateway role may execute, and the usage
-- ledger's principal kind for tokens (ADR 0045). Runs in one transaction as pandokos_owner. Every
-- name is schema-qualified.

-- A request authenticated by a token is recorded with principal_kind 'token': principal_id is the
-- service account, key_id the subject mapping.
ALTER TABLE pandokos.usage_record
    DROP CONSTRAINT usage_record_principal_kind_valid,
    ADD CONSTRAINT usage_record_principal_kind_valid CHECK (principal_kind IN ('service_account', 'token'));

-- Tells the instances that a platform-level registration or its tenant list changed.
CREATE FUNCTION pandokos.notify_issuers() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    PERFORM pg_catalog.pg_notify('pandokos_cache', 'issuers');
    RETURN NULL;
END
$$;

-- Tenant-bound registrations (tenant-scoped table): every token they accept acts for the tenant.
CREATE TABLE pandokos.trusted_issuer (
    tenant_id       uuid        NOT NULL,
    id              uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    name            text        NOT NULL,
    description     text        NOT NULL DEFAULT '',
    issuer          text        NOT NULL,
    audience        text        NOT NULL,
    jwks_uri        text        NOT NULL DEFAULT '',
    algorithms      text[]      NOT NULL DEFAULT '{RS256,ES256}'::text[],
    subject_claim   text        NOT NULL DEFAULT 'sub',
    required_claims jsonb       NOT NULL DEFAULT '{}'::jsonb,
    enabled         boolean     NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT trusted_issuer_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT trusted_issuer_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT trusted_issuer_name_key UNIQUE (tenant_id, name),
    CONSTRAINT trusted_issuer_name_format CHECK (name ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    CONSTRAINT trusted_issuer_description_length CHECK (char_length(description) <= 200),
    CONSTRAINT trusted_issuer_issuer_format CHECK (issuer ~ '^https://[^[:space:][:cntrl:]?#@]+$' AND octet_length(issuer) <= 255),
    CONSTRAINT trusted_issuer_audience_format CHECK (char_length(audience) BETWEEN 1 AND 255 AND audience !~ '[[:cntrl:]]'),
    CONSTRAINT trusted_issuer_jwks_uri_format CHECK (jwks_uri = '' OR (jwks_uri ~ '^https://[^[:space:][:cntrl:]#@]+$'
                                                                       AND octet_length(jwks_uri) <= 2048)),
    CONSTRAINT trusted_issuer_algorithms_valid CHECK (cardinality(algorithms) BETWEEN 1 AND 10 AND algorithms <@
        ARRAY['RS256', 'RS384', 'RS512', 'PS256', 'PS384', 'PS512', 'ES256', 'ES384', 'ES512', 'EdDSA']::text[]),
    CONSTRAINT trusted_issuer_subject_claim_format CHECK (subject_claim ~ '^[A-Za-z0-9_.:/-]{1,64}$'),
    CONSTRAINT trusted_issuer_required_claims_object CHECK (jsonb_typeof(required_claims) = 'object'
                                                            AND pg_column_size(required_claims) <= 4096)
);

CREATE TRIGGER trusted_issuer_guard BEFORE INSERT OR UPDATE ON pandokos.trusted_issuer
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();
CREATE TRIGGER trusted_issuer_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.trusted_issuer
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_cache('scoped');

ALTER TABLE pandokos.trusted_issuer ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.trusted_issuer FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.trusted_issuer
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.trusted_issuer TO pandokos_gateway, pandokos_admin;
GRANT INSERT (tenant_id, name, description, issuer, audience, jwks_uri, algorithms, subject_claim, required_claims, enabled)
    ON pandokos.trusted_issuer TO pandokos_admin;
GRANT UPDATE (name, description, issuer, audience, jwks_uri, algorithms, subject_claim, required_claims, enabled)
    ON pandokos.trusted_issuer TO pandokos_admin;
GRANT DELETE ON pandokos.trusted_issuer TO pandokos_admin;

-- Platform-level registrations (global table with explicit policies): the tenant of a token is
-- read from the claim named by tenant_claim and must be in platform_issuer_tenant.
CREATE TABLE pandokos.platform_issuer (
    id              uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    name            text        NOT NULL,
    description     text        NOT NULL DEFAULT '',
    issuer          text        NOT NULL,
    audience        text        NOT NULL,
    jwks_uri        text        NOT NULL DEFAULT '',
    algorithms      text[]      NOT NULL DEFAULT '{RS256,ES256}'::text[],
    subject_claim   text        NOT NULL DEFAULT 'sub',
    tenant_claim    text        NOT NULL,
    required_claims jsonb       NOT NULL DEFAULT '{}'::jsonb,
    enabled         boolean     NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT platform_issuer_pkey PRIMARY KEY (id),
    CONSTRAINT platform_issuer_name_key UNIQUE (name),
    CONSTRAINT platform_issuer_name_format CHECK (name ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    CONSTRAINT platform_issuer_description_length CHECK (char_length(description) <= 200),
    CONSTRAINT platform_issuer_issuer_format CHECK (issuer ~ '^https://[^[:space:][:cntrl:]?#@]+$' AND octet_length(issuer) <= 255),
    CONSTRAINT platform_issuer_audience_format CHECK (char_length(audience) BETWEEN 1 AND 255 AND audience !~ '[[:cntrl:]]'),
    CONSTRAINT platform_issuer_jwks_uri_format CHECK (jwks_uri = '' OR (jwks_uri ~ '^https://[^[:space:][:cntrl:]#@]+$'
                                                                        AND octet_length(jwks_uri) <= 2048)),
    CONSTRAINT platform_issuer_algorithms_valid CHECK (cardinality(algorithms) BETWEEN 1 AND 10 AND algorithms <@
        ARRAY['RS256', 'RS384', 'RS512', 'PS256', 'PS384', 'PS512', 'ES256', 'ES384', 'ES512', 'EdDSA']::text[]),
    CONSTRAINT platform_issuer_subject_claim_format CHECK (subject_claim ~ '^[A-Za-z0-9_.:/-]{1,64}$'),
    CONSTRAINT platform_issuer_tenant_claim_format CHECK (tenant_claim ~ '^[A-Za-z0-9_.:/-]{1,64}$'),
    CONSTRAINT platform_issuer_required_claims_object CHECK (jsonb_typeof(required_claims) = 'object'
                                                             AND pg_column_size(required_claims) <= 4096)
);

CREATE TRIGGER platform_issuer_guard BEFORE INSERT OR UPDATE ON pandokos.platform_issuer
    FOR EACH ROW EXECUTE FUNCTION pandokos.global_row_guard();
CREATE TRIGGER platform_issuer_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.platform_issuer
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.notify_issuers();

ALTER TABLE pandokos.platform_issuer ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.platform_issuer FORCE ROW LEVEL SECURITY;

CREATE POLICY platform_issuer_select ON pandokos.platform_issuer
    FOR SELECT TO pandokos_gateway, pandokos_admin USING (true);
CREATE POLICY platform_issuer_insert ON pandokos.platform_issuer
    FOR INSERT TO pandokos_admin WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY platform_issuer_update ON pandokos.platform_issuer
    FOR UPDATE TO pandokos_admin USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY platform_issuer_delete ON pandokos.platform_issuer
    FOR DELETE TO pandokos_admin USING ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.platform_issuer TO pandokos_gateway, pandokos_admin;
GRANT INSERT (name, description, issuer, audience, jwks_uri, algorithms, subject_claim, tenant_claim, required_claims, enabled)
    ON pandokos.platform_issuer TO pandokos_admin;
GRANT UPDATE (name, description, issuer, audience, jwks_uri, algorithms, subject_claim, tenant_claim, required_claims, enabled)
    ON pandokos.platform_issuer TO pandokos_admin;
GRANT DELETE ON pandokos.platform_issuer TO pandokos_admin;

-- The tenants a platform-level registration may act for, each with the claim value that designates
-- it (global table with explicit policies: it describes tenants). A tenant's session sees its own
-- rows; the platform scope sees and changes all of them. There is no wildcard row.
CREATE TABLE pandokos.platform_issuer_tenant (
    issuer_id   uuid        NOT NULL,
    tenant_id   uuid        NOT NULL,
    claim_value text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT platform_issuer_tenant_pkey PRIMARY KEY (issuer_id, tenant_id),
    CONSTRAINT platform_issuer_tenant_claim_value_key UNIQUE (issuer_id, claim_value),
    CONSTRAINT platform_issuer_tenant_issuer_fkey FOREIGN KEY (issuer_id) REFERENCES pandokos.platform_issuer (id) ON DELETE CASCADE,
    CONSTRAINT platform_issuer_tenant_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT platform_issuer_tenant_claim_value_format CHECK (char_length(claim_value) BETWEEN 1 AND 255
                                                                AND claim_value !~ '[[:cntrl:]]')
);
CREATE INDEX platform_issuer_tenant_tenant_idx ON pandokos.platform_issuer_tenant (tenant_id);

CREATE TRIGGER platform_issuer_tenant_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.platform_issuer_tenant
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.notify_issuers();

ALTER TABLE pandokos.platform_issuer_tenant ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.platform_issuer_tenant FORCE ROW LEVEL SECURITY;

CREATE POLICY platform_issuer_tenant_self ON pandokos.platform_issuer_tenant
    FOR SELECT TO pandokos_gateway, pandokos_admin
    USING (tenant_id = (SELECT pandokos.current_tenant_id()));
CREATE POLICY platform_issuer_tenant_platform_select ON pandokos.platform_issuer_tenant
    FOR SELECT TO pandokos_admin USING ((SELECT pandokos.platform_scope()));
CREATE POLICY platform_issuer_tenant_platform_insert ON pandokos.platform_issuer_tenant
    FOR INSERT TO pandokos_admin WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY platform_issuer_tenant_platform_update ON pandokos.platform_issuer_tenant
    FOR UPDATE TO pandokos_admin USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY platform_issuer_tenant_platform_delete ON pandokos.platform_issuer_tenant
    FOR DELETE TO pandokos_admin USING ((SELECT pandokos.platform_scope()));
-- The lookup function below runs as the owner, which the forced policies would otherwise leave
-- without any row.
CREATE POLICY platform_issuer_tenant_owner ON pandokos.platform_issuer_tenant
    FOR SELECT TO pandokos_owner USING (true);

GRANT SELECT ON pandokos.platform_issuer_tenant TO pandokos_gateway, pandokos_admin;
GRANT INSERT (issuer_id, tenant_id, claim_value) ON pandokos.platform_issuer_tenant TO pandokos_admin;
GRANT UPDATE (claim_value) ON pandokos.platform_issuer_tenant TO pandokos_admin;
GRANT DELETE ON pandokos.platform_issuer_tenant TO pandokos_admin;

-- The tenant a claim value designates for a platform-level registration, or NULL. It takes one
-- registration and one value, returns one tenant ID, and cannot list.
CREATE FUNCTION pandokos.platform_issuer_tenant_lookup(p_issuer_id uuid, p_claim_value text) RETURNS uuid
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
    RETURN (SELECT t.tenant_id FROM pandokos.platform_issuer_tenant t
             WHERE t.issuer_id = p_issuer_id AND t.claim_value = p_claim_value);

REVOKE ALL ON FUNCTION pandokos.platform_issuer_tenant_lookup(uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.platform_issuer_tenant_lookup(uuid, text) TO pandokos_gateway;

-- Which registration an issuer and audience pair selects (global table, no grant to any runtime
-- role). The primary key makes the pair unique across the installation, over both kinds of
-- registration. Maintained by the trigger below; read only through issuer_lookup.
CREATE TABLE pandokos.issuer_directory (
    issuer    text NOT NULL,
    audience  text NOT NULL,
    scope     text NOT NULL,
    tenant_id uuid,
    issuer_id uuid NOT NULL,
    CONSTRAINT issuer_directory_pkey PRIMARY KEY (issuer, audience),
    CONSTRAINT issuer_directory_issuer_id_key UNIQUE (issuer_id),
    CONSTRAINT issuer_directory_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT issuer_directory_scope_valid CHECK (scope IN ('tenant', 'platform')),
    CONSTRAINT issuer_directory_scope_tenant CHECK ((scope = 'tenant') = (tenant_id IS NOT NULL))
);

-- TG_ARGV[0]: 'tenant' (row of trusted_issuer) or 'platform' (row of platform_issuer).
CREATE FUNCTION pandokos.issuer_directory_sync() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        DELETE FROM pandokos.issuer_directory d WHERE d.issuer_id = OLD.id;
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        INSERT INTO pandokos.issuer_directory (issuer, audience, scope, tenant_id, issuer_id)
        VALUES (NEW.issuer, NEW.audience, TG_ARGV[0],
                CASE WHEN TG_ARGV[0] = 'tenant' THEN (pg_catalog.to_jsonb(NEW) ->> 'tenant_id')::uuid END, NEW.id);
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER trusted_issuer_directory AFTER INSERT OR UPDATE OR DELETE ON pandokos.trusted_issuer
    FOR EACH ROW EXECUTE FUNCTION pandokos.issuer_directory_sync('tenant');
CREATE TRIGGER platform_issuer_directory AFTER INSERT OR UPDATE OR DELETE ON pandokos.platform_issuer
    FOR EACH ROW EXECUTE FUNCTION pandokos.issuer_directory_sync('platform');

-- The registrations an issuer selects with one of up to 16 audiences. The one path from a token
-- to a tenant-bound registration that crosses the tenant policy: exact values in, at most one row
-- per audience out, and nothing for NULL, an empty list or a longer one.
CREATE FUNCTION pandokos.issuer_lookup(p_issuer text, p_audiences text[])
    RETURNS TABLE (scope text, tenant_id uuid, issuer_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
    SELECT d.scope, d.tenant_id, d.issuer_id
      FROM pandokos.issuer_directory d
     WHERE d.issuer = p_issuer
       AND pg_catalog.cardinality(p_audiences) BETWEEN 1 AND 16
       AND d.audience = ANY (p_audiences)
$$;

REVOKE ALL ON FUNCTION pandokos.issuer_lookup(text, text[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.issuer_lookup(text, text[]) TO pandokos_gateway;

-- Subject mappings (tenant-scoped table): one subject of one registration acts as one service
-- account. A row never changes; it is deleted and created again. Exactly one of the two issuer
-- references is set.
CREATE TABLE pandokos.subject_mapping (
    tenant_id          uuid        NOT NULL,
    id                 uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    trusted_issuer_id  uuid,
    platform_issuer_id uuid,
    subject            text        NOT NULL,
    service_account_id uuid        NOT NULL,
    description        text        NOT NULL DEFAULT '',
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT subject_mapping_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT subject_mapping_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT subject_mapping_trusted_issuer_fkey FOREIGN KEY (tenant_id, trusted_issuer_id)
        REFERENCES pandokos.trusted_issuer (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT subject_mapping_platform_issuer_fkey FOREIGN KEY (platform_issuer_id)
        REFERENCES pandokos.platform_issuer (id) ON DELETE CASCADE,
    CONSTRAINT subject_mapping_service_account_fkey FOREIGN KEY (tenant_id, service_account_id)
        REFERENCES pandokos.service_account (tenant_id, id),
    CONSTRAINT subject_mapping_one_issuer CHECK (num_nonnulls(trusted_issuer_id, platform_issuer_id) = 1),
    CONSTRAINT subject_mapping_trusted_key UNIQUE (tenant_id, trusted_issuer_id, subject),
    CONSTRAINT subject_mapping_platform_key UNIQUE (tenant_id, platform_issuer_id, subject),
    CONSTRAINT subject_mapping_subject_format CHECK (char_length(subject) BETWEEN 1 AND 255 AND subject !~ '[[:cntrl:]]'),
    CONSTRAINT subject_mapping_description_length CHECK (char_length(description) <= 200)
);
CREATE INDEX subject_mapping_service_account_idx ON pandokos.subject_mapping (tenant_id, service_account_id);
CREATE INDEX subject_mapping_platform_issuer_idx ON pandokos.subject_mapping (platform_issuer_id) WHERE platform_issuer_id IS NOT NULL;

CREATE TRIGGER subject_mapping_guard BEFORE INSERT OR UPDATE ON pandokos.subject_mapping
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();
CREATE TRIGGER subject_mapping_notify AFTER INSERT OR DELETE ON pandokos.subject_mapping
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_cache('scoped');

ALTER TABLE pandokos.subject_mapping ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.subject_mapping FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.subject_mapping
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.subject_mapping TO pandokos_gateway, pandokos_admin;
GRANT INSERT (tenant_id, trusted_issuer_id, platform_issuer_id, subject, service_account_id, description)
    ON pandokos.subject_mapping TO pandokos_admin;
GRANT DELETE ON pandokos.subject_mapping TO pandokos_admin;

-- The gateway records a refused token in the audit log of the tenant of the session, as
-- record_auth_failure does for a key (0005): one kind of row, fixed columns, no privilege on
-- audit_event for the gateway role. p_subject is empty unless the token's signature was verified.
CREATE FUNCTION pandokos.record_token_auth_failure(p_scope text, p_issuer_id uuid, p_reason text, p_subject text,
                                                   p_request_id text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF p_scope IS NULL OR p_scope NOT IN ('tenant', 'platform') OR p_issuer_id IS NULL
       OR p_reason IS NULL OR p_reason !~ '^[a-z_]{1,40}$'
       OR p_request_id IS NULL OR p_request_id !~ '^[0-9a-f]{32}$'
       OR p_subject IS NULL OR pg_catalog.char_length(p_subject) > 255 OR p_subject ~ '[[:cntrl:]]' THEN
        RAISE EXCEPTION 'invalid authentication failure record' USING ERRCODE = 'check_violation';
    END IF;
    INSERT INTO pandokos.audit_event (tenant_id, actor_type, action, resource_type, resource_id,
                                      outcome, request_id, detail)
    VALUES ((SELECT pandokos.current_tenant_id()), 'anonymous', 'auth.failure', 'token_issuer', p_issuer_id::text,
            'failure', p_request_id,
            pg_catalog.jsonb_strip_nulls(pg_catalog.jsonb_build_object(
                'reason', p_reason, 'listener', 'inference', 'issuer_scope', p_scope,
                'subject', NULLIF(p_subject, ''))));
END
$$;

REVOKE ALL ON FUNCTION pandokos.record_token_auth_failure(text, uuid, text, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.record_token_auth_failure(text, uuid, text, text, text) TO pandokos_gateway;
