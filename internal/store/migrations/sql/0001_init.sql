-- 0001_init: schema, migration ledger, helper functions, tenant, service_account.
-- Runs in one transaction as pandokos_owner. Every name is schema-qualified: nothing in
-- Pandokos depends on search_path.

CREATE SCHEMA IF NOT EXISTS pandokos;
REVOKE ALL ON SCHEMA pandokos FROM PUBLIC;
GRANT USAGE ON SCHEMA pandokos TO pandokos_gateway, pandokos_admin, pandokos_worker;

-- Functions created by the owner are not executable by PUBLIC; each grant is explicit.
ALTER DEFAULT PRIVILEGES FOR ROLE pandokos_owner REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;

-- Migration ledger (global table).
CREATE TABLE pandokos.schema_migration (
    version          integer     NOT NULL,
    name             text        NOT NULL,
    checksum         text        NOT NULL,
    applied_at       timestamptz NOT NULL DEFAULT now(),
    applied_by       text        NOT NULL DEFAULT session_user,
    pandokos_version text        NOT NULL,
    CONSTRAINT schema_migration_pkey PRIMARY KEY (version),
    CONSTRAINT schema_migration_version_positive CHECK (version > 0),
    CONSTRAINT schema_migration_checksum_format CHECK (checksum ~ '^[0-9a-f]{64}$')
);
GRANT SELECT ON pandokos.schema_migration TO pandokos_gateway, pandokos_admin, pandokos_worker;

-- UUID version 7: 48 bits of Unix time in milliseconds, then random bits.
CREATE FUNCTION pandokos.uuid_v7() RETURNS uuid
    LANGUAGE sql VOLATILE PARALLEL SAFE
    RETURN encode(
        set_bit(
            set_bit(
                overlay(uuid_send(gen_random_uuid())
                        PLACING substring(int8send(floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint) FROM 3)
                        FROM 1 FOR 6),
                52, 1),
            53, 1),
        'hex')::uuid;

-- The tenant of the current transaction, or NULL. An empty setting is NULL: after a
-- transaction that set it, PostgreSQL reports '' instead of "absent" on the same connection.
-- A value that is not a UUID raises an error.
CREATE FUNCTION pandokos.current_tenant_id() RETURNS uuid
    LANGUAGE sql STABLE PARALLEL SAFE
    RETURN NULLIF(current_setting('app.tenant_id', true), '')::uuid;

-- True only in a transaction that declared the platform scope.
CREATE FUNCTION pandokos.platform_scope() RETURNS boolean
    LANGUAGE sql STABLE PARALLEL SAFE
    RETURN COALESCE(current_setting('app.scope', true) = 'platform', false);

GRANT EXECUTE ON FUNCTION pandokos.uuid_v7(), pandokos.current_tenant_id(), pandokos.platform_scope()
    TO pandokos_gateway, pandokos_admin, pandokos_worker;

-- Lifecycle and immutability of tenant rows.
CREATE FUNCTION pandokos.tenant_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'tenant rows are never deleted' USING ERRCODE = 'restrict_violation';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'provisioning' THEN
            RAISE EXCEPTION 'a tenant is created in state provisioning' USING ERRCODE = 'check_violation';
        END IF;
        NEW.created_at := now();
        NEW.updated_at := now();
        NEW.state_changed_at := now();
        RETURN NEW;
    END IF;
    IF NEW.id <> OLD.id THEN
        RAISE EXCEPTION 'the tenant id is immutable' USING ERRCODE = 'check_violation';
    END IF;
    IF OLD.state = 'deleted' THEN
        RAISE EXCEPTION 'a deleted tenant cannot be changed' USING ERRCODE = 'check_violation';
    END IF;
    NEW.created_at := OLD.created_at;
    NEW.updated_at := now();
    NEW.state_changed_at := OLD.state_changed_at;
    IF NEW.state <> OLD.state THEN
        IF (OLD.state, NEW.state) NOT IN (
            ('provisioning', 'active'),
            ('provisioning', 'deleting'),
            ('active', 'suspended'),
            ('active', 'deleting'),
            ('suspended', 'active'),
            ('suspended', 'deleting'),
            ('deleting', 'deleted')
        ) THEN
            RAISE EXCEPTION 'invalid tenant state transition from % to %', OLD.state, NEW.state
                USING ERRCODE = 'check_violation';
        END IF;
        NEW.state_changed_at := now();
    END IF;
    RETURN NEW;
END
$$;

-- Timestamps and immutable identity of rows of tenant-scoped tables.
CREATE FUNCTION pandokos.row_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.created_at := now();
        NEW.updated_at := now();
        RETURN NEW;
    END IF;
    IF NEW.id <> OLD.id OR NEW.tenant_id <> OLD.tenant_id THEN
        RAISE EXCEPTION 'id and tenant_id are immutable' USING ERRCODE = 'check_violation';
    END IF;
    NEW.created_at := OLD.created_at;
    NEW.updated_at := now();
    RETURN NEW;
END
$$;

-- Tenants (global table with explicit policies).
CREATE TABLE pandokos.tenant (
    id               uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    name             text        NOT NULL,
    display_name     text        NOT NULL DEFAULT '',
    state            text        NOT NULL DEFAULT 'provisioning',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    state_changed_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tenant_pkey PRIMARY KEY (id),
    CONSTRAINT tenant_name_format CHECK (name ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    CONSTRAINT tenant_display_name_length CHECK (char_length(display_name) <= 200),
    CONSTRAINT tenant_state_valid CHECK (state IN ('provisioning', 'active', 'suspended', 'deleting', 'deleted'))
);
CREATE UNIQUE INDEX tenant_name_key ON pandokos.tenant (name) WHERE state <> 'deleted';

CREATE TRIGGER tenant_guard BEFORE INSERT OR UPDATE OR DELETE ON pandokos.tenant
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_guard();

ALTER TABLE pandokos.tenant ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.tenant FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_self ON pandokos.tenant
    FOR SELECT TO pandokos_gateway, pandokos_admin, pandokos_worker
    USING (id = (SELECT pandokos.current_tenant_id()));
CREATE POLICY tenant_platform_select ON pandokos.tenant
    FOR SELECT TO pandokos_admin, pandokos_worker
    USING ((SELECT pandokos.platform_scope()));
CREATE POLICY tenant_platform_insert ON pandokos.tenant
    FOR INSERT TO pandokos_admin
    WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY tenant_platform_update ON pandokos.tenant
    FOR UPDATE TO pandokos_admin
    USING ((SELECT pandokos.platform_scope()))
    WITH CHECK ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.tenant TO pandokos_gateway, pandokos_admin, pandokos_worker;
GRANT INSERT (name, display_name) ON pandokos.tenant TO pandokos_admin;
GRANT UPDATE (name, display_name, state) ON pandokos.tenant TO pandokos_admin;

-- Service accounts (tenant-scoped table).
CREATE TABLE pandokos.service_account (
    tenant_id   uuid        NOT NULL,
    id          uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    name        text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    state       text        NOT NULL DEFAULT 'active',
    valid_from  timestamptz,
    valid_until timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT service_account_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT service_account_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT service_account_name_key UNIQUE (tenant_id, name),
    CONSTRAINT service_account_name_format CHECK (name ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    CONSTRAINT service_account_description_length CHECK (char_length(description) <= 500),
    CONSTRAINT service_account_state_valid CHECK (state IN ('active', 'disabled')),
    CONSTRAINT service_account_validity_order CHECK (valid_from IS NULL OR valid_until IS NULL OR valid_from < valid_until)
);

CREATE TRIGGER service_account_guard BEFORE INSERT OR UPDATE ON pandokos.service_account
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();

ALTER TABLE pandokos.service_account ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.service_account FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.service_account
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.service_account TO pandokos_gateway, pandokos_admin;
GRANT INSERT (tenant_id, name, description, state, valid_from, valid_until) ON pandokos.service_account TO pandokos_admin;
GRANT UPDATE (name, description, state, valid_from, valid_until) ON pandokos.service_account TO pandokos_admin;
GRANT DELETE ON pandokos.service_account TO pandokos_admin;
