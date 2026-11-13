-- 0002_identity_audit: role assignments, virtual keys and their lookup, platform keys, audit
-- logs, cache notifications. Runs in one transaction as pandokos_owner. Every name is
-- schema-qualified.

-- Service accounts are disabled, never deleted, once audit records refer to them (sprint 1.2, O-5).
REVOKE DELETE ON pandokos.service_account FROM pandokos_admin;

-- Timestamps and immutable identity of rows of global tables.
CREATE FUNCTION pandokos.global_row_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.created_at := now();
        NEW.updated_at := now();
        RETURN NEW;
    END IF;
    IF NEW.id <> OLD.id THEN
        RAISE EXCEPTION 'id is immutable' USING ERRCODE = 'check_violation';
    END IF;
    NEW.created_at := OLD.created_at;
    NEW.updated_at := now();
    RETURN NEW;
END
$$;

-- A key is revoked once, at the time of the database, and never un-revoked. Nothing else of a
-- key changes after it is created.
CREATE FUNCTION pandokos.key_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NEW.key_id <> OLD.key_id OR NEW.secret_hash <> OLD.secret_hash
       OR NEW.expires_at IS DISTINCT FROM OLD.expires_at THEN
        RAISE EXCEPTION 'a key is immutable except for its revocation' USING ERRCODE = 'check_violation';
    END IF;
    IF OLD.revoked_at IS NOT NULL THEN
        IF NEW.revoked_at IS DISTINCT FROM OLD.revoked_at THEN
            RAISE EXCEPTION 'a revocation cannot be changed' USING ERRCODE = 'check_violation';
        END IF;
    ELSIF NEW.revoked_at IS NOT NULL THEN
        NEW.revoked_at := now();
    END IF;
    RETURN NEW;
END
$$;

-- Audit rows are never changed or removed, by any role.
CREATE FUNCTION pandokos.append_only() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
END
$$;

-- Identity and time of an audit row are set by the database.
CREATE FUNCTION pandokos.audit_stamp() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    NEW.id := pandokos.uuid_v7();
    NEW.occurred_at := now();
    RETURN NEW;
END
$$;

-- Tells the instances that cached identity data changed. The payload names a tenant or the
-- platform, never a key or a secret. TG_ARGV[0]: 'scoped' (row has tenant_id), 'tenant' (row
-- is a tenant), 'platform'.
CREATE FUNCTION pandokos.notify_cache() RETURNS trigger
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
    IF TG_ARGV[0] = 'platform' THEN
        PERFORM pg_catalog.pg_notify('pandokos_cache', 'platform');
    ELSIF TG_ARGV[0] = 'tenant' THEN
        PERFORM pg_catalog.pg_notify('pandokos_cache', 'tenant:' || (rec ->> 'id'));
    ELSE
        PERFORM pg_catalog.pg_notify('pandokos_cache', 'tenant:' || (rec ->> 'tenant_id'));
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER tenant_notify AFTER UPDATE ON pandokos.tenant
    FOR EACH ROW WHEN (OLD.state IS DISTINCT FROM NEW.state OR OLD.name IS DISTINCT FROM NEW.name)
    EXECUTE FUNCTION pandokos.notify_cache('tenant');
CREATE TRIGGER service_account_notify AFTER UPDATE ON pandokos.service_account
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_cache('scoped');

-- Role assignments (tenant-scoped table). The role is the name of a built-in role; the
-- catalogue of roles is in code (ADR 0007).
CREATE TABLE pandokos.role_assignment (
    tenant_id          uuid        NOT NULL,
    id                 uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    service_account_id uuid        NOT NULL,
    role               text        NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT role_assignment_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT role_assignment_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT role_assignment_service_account_fkey FOREIGN KEY (tenant_id, service_account_id)
        REFERENCES pandokos.service_account (tenant_id, id),
    CONSTRAINT role_assignment_key UNIQUE (tenant_id, service_account_id, role),
    CONSTRAINT role_assignment_role_format CHECK (role ~ '^[a-z][a-z0-9-]{0,62}$')
);

CREATE TRIGGER role_assignment_guard BEFORE INSERT OR UPDATE ON pandokos.role_assignment
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();
CREATE TRIGGER role_assignment_notify AFTER INSERT OR DELETE ON pandokos.role_assignment
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_cache('scoped');

ALTER TABLE pandokos.role_assignment ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.role_assignment FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.role_assignment
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.role_assignment TO pandokos_gateway, pandokos_admin;
GRANT INSERT (tenant_id, service_account_id, role) ON pandokos.role_assignment TO pandokos_admin;
GRANT DELETE ON pandokos.role_assignment TO pandokos_admin;

-- Virtual keys (tenant-scoped table). Only the SHA-256 of the secret is stored.
CREATE TABLE pandokos.virtual_key (
    tenant_id          uuid        NOT NULL,
    id                 uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    service_account_id uuid        NOT NULL,
    key_id             text        NOT NULL,
    description        text        NOT NULL DEFAULT '',
    secret_hash        bytea       NOT NULL,
    scopes             text[],
    expires_at         timestamptz,
    revoked_at         timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT virtual_key_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT virtual_key_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT virtual_key_service_account_fkey FOREIGN KEY (tenant_id, service_account_id)
        REFERENCES pandokos.service_account (tenant_id, id),
    CONSTRAINT virtual_key_key_id_key UNIQUE (key_id),
    CONSTRAINT virtual_key_key_id_format CHECK (key_id ~ '^t[a-z2-7]{16}$'),
    CONSTRAINT virtual_key_description_length CHECK (char_length(description) <= 200),
    CONSTRAINT virtual_key_secret_hash_length CHECK (octet_length(secret_hash) = 32),
    CONSTRAINT virtual_key_scopes_size CHECK (scopes IS NULL OR cardinality(scopes) BETWEEN 1 AND 32)
);
CREATE INDEX virtual_key_service_account_idx ON pandokos.virtual_key (tenant_id, service_account_id);

CREATE TRIGGER virtual_key_guard BEFORE INSERT OR UPDATE ON pandokos.virtual_key
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();
CREATE TRIGGER virtual_key_key_guard BEFORE UPDATE ON pandokos.virtual_key
    FOR EACH ROW EXECUTE FUNCTION pandokos.key_guard();
CREATE TRIGGER virtual_key_notify AFTER UPDATE ON pandokos.virtual_key
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_cache('scoped');

ALTER TABLE pandokos.virtual_key ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.virtual_key FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.virtual_key
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.virtual_key TO pandokos_gateway, pandokos_admin;
GRANT INSERT (tenant_id, service_account_id, key_id, description, secret_hash, scopes, expires_at)
    ON pandokos.virtual_key TO pandokos_admin;
GRANT UPDATE (revoked_at) ON pandokos.virtual_key TO pandokos_admin;

-- Which tenant a key ID belongs to (global table, no grant to any runtime role). It is the
-- only thing that can be learnt without a tenant session, and only through the function below.
CREATE TABLE pandokos.virtual_key_directory (
    key_id    text NOT NULL,
    tenant_id uuid NOT NULL,
    CONSTRAINT virtual_key_directory_pkey PRIMARY KEY (key_id),
    CONSTRAINT virtual_key_directory_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id)
);

CREATE FUNCTION pandokos.virtual_key_directory_add() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    INSERT INTO pandokos.virtual_key_directory (key_id, tenant_id) VALUES (NEW.key_id, NEW.tenant_id);
    RETURN NULL;
END
$$;

CREATE TRIGGER virtual_key_directory_add AFTER INSERT ON pandokos.virtual_key
    FOR EACH ROW EXECUTE FUNCTION pandokos.virtual_key_directory_add();

-- The tenant of a key ID, or NULL. The one path that crosses the tenant policy: it takes one
-- key ID, returns one tenant ID, and cannot list.
CREATE FUNCTION pandokos.virtual_key_lookup(p_key_id text) RETURNS uuid
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
    RETURN (SELECT d.tenant_id FROM pandokos.virtual_key_directory d WHERE d.key_id = p_key_id);

GRANT EXECUTE ON FUNCTION pandokos.virtual_key_lookup(text) TO pandokos_gateway, pandokos_admin;

-- Platform keys (global table with explicit policies): credentials of platform administrators.
-- The gateway role has no grant on it.
CREATE TABLE pandokos.platform_key (
    id          uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    key_id      text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    role        text        NOT NULL DEFAULT 'platform-admin',
    secret_hash bytea       NOT NULL,
    bootstrap   boolean     NOT NULL DEFAULT false,
    expires_at  timestamptz,
    revoked_at  timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT platform_key_pkey PRIMARY KEY (id),
    CONSTRAINT platform_key_key_id_key UNIQUE (key_id),
    CONSTRAINT platform_key_key_id_format CHECK (key_id ~ '^p[a-z2-7]{16}$'),
    CONSTRAINT platform_key_description_length CHECK (char_length(description) <= 200),
    CONSTRAINT platform_key_role_format CHECK (role ~ '^[a-z][a-z0-9-]{0,62}$'),
    CONSTRAINT platform_key_secret_hash_length CHECK (octet_length(secret_hash) = 32),
    CONSTRAINT platform_key_bootstrap_expires CHECK (NOT bootstrap OR expires_at IS NOT NULL)
);

CREATE TRIGGER platform_key_guard BEFORE INSERT OR UPDATE ON pandokos.platform_key
    FOR EACH ROW EXECUTE FUNCTION pandokos.global_row_guard();
CREATE TRIGGER platform_key_key_guard BEFORE UPDATE ON pandokos.platform_key
    FOR EACH ROW EXECUTE FUNCTION pandokos.key_guard();
CREATE TRIGGER platform_key_notify AFTER UPDATE ON pandokos.platform_key
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_cache('platform');

ALTER TABLE pandokos.platform_key ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.platform_key FORCE ROW LEVEL SECURITY;

CREATE POLICY platform_key_select ON pandokos.platform_key
    FOR SELECT TO pandokos_admin
    USING ((SELECT pandokos.platform_scope()));
CREATE POLICY platform_key_insert ON pandokos.platform_key
    FOR INSERT TO pandokos_admin
    WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY platform_key_update ON pandokos.platform_key
    FOR UPDATE TO pandokos_admin
    USING ((SELECT pandokos.platform_scope()))
    WITH CHECK ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.platform_key TO pandokos_admin;
GRANT INSERT (key_id, description, role, secret_hash, bootstrap, expires_at) ON pandokos.platform_key TO pandokos_admin;
GRANT UPDATE (revoked_at) ON pandokos.platform_key TO pandokos_admin;

-- Audit log of a tenant (tenant-scoped table, append-only).
CREATE TABLE pandokos.audit_event (
    tenant_id     uuid        NOT NULL,
    id            uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    occurred_at   timestamptz NOT NULL DEFAULT now(),
    actor_type    text        NOT NULL,
    actor_id      text        NOT NULL DEFAULT '',
    actor_name    text        NOT NULL DEFAULT '',
    key_id        text        NOT NULL DEFAULT '',
    action        text        NOT NULL,
    resource_type text        NOT NULL,
    resource_id   text        NOT NULL DEFAULT '',
    outcome       text        NOT NULL,
    request_id    text        NOT NULL DEFAULT '',
    reason        text        NOT NULL DEFAULT '',
    detail        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT audit_event_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT audit_event_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT audit_event_actor_type_valid CHECK (actor_type IN ('platform', 'service_account', 'user', 'anonymous')),
    CONSTRAINT audit_event_action_format CHECK (action ~ '^[a-z_]+\.[a-z_]+$'),
    CONSTRAINT audit_event_resource_type_format CHECK (resource_type ~ '^[a-z_]+$'),
    CONSTRAINT audit_event_outcome_valid CHECK (outcome IN ('success', 'failure')),
    CONSTRAINT audit_event_reason_length CHECK (char_length(reason) <= 500),
    CONSTRAINT audit_event_detail_object CHECK (jsonb_typeof(detail) = 'object' AND pg_column_size(detail) <= 8192)
);

CREATE TRIGGER audit_event_stamp BEFORE INSERT ON pandokos.audit_event
    FOR EACH ROW EXECUTE FUNCTION pandokos.audit_stamp();
CREATE TRIGGER audit_event_append_only BEFORE UPDATE OR DELETE ON pandokos.audit_event
    FOR EACH ROW EXECUTE FUNCTION pandokos.append_only();
CREATE TRIGGER audit_event_no_truncate BEFORE TRUNCATE ON pandokos.audit_event
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only();

ALTER TABLE pandokos.audit_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.audit_event FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.audit_event
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT INSERT (tenant_id, actor_type, actor_id, actor_name, key_id, action, resource_type, resource_id, outcome, request_id, reason, detail)
    ON pandokos.audit_event TO pandokos_admin;

-- Audit log of the platform (global table with explicit policies, append-only).
CREATE TABLE pandokos.platform_audit_event (
    id            uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    occurred_at   timestamptz NOT NULL DEFAULT now(),
    actor_type    text        NOT NULL,
    actor_id      text        NOT NULL DEFAULT '',
    actor_name    text        NOT NULL DEFAULT '',
    key_id        text        NOT NULL DEFAULT '',
    action        text        NOT NULL,
    resource_type text        NOT NULL,
    resource_id   text        NOT NULL DEFAULT '',
    outcome       text        NOT NULL,
    request_id    text        NOT NULL DEFAULT '',
    reason        text        NOT NULL DEFAULT '',
    detail        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT platform_audit_event_pkey PRIMARY KEY (id),
    CONSTRAINT platform_audit_event_actor_type_valid CHECK (actor_type IN ('platform', 'service_account', 'user', 'anonymous', 'host')),
    CONSTRAINT platform_audit_event_action_format CHECK (action ~ '^[a-z_]+\.[a-z_]+$'),
    CONSTRAINT platform_audit_event_resource_type_format CHECK (resource_type ~ '^[a-z_]+$'),
    CONSTRAINT platform_audit_event_outcome_valid CHECK (outcome IN ('success', 'failure')),
    CONSTRAINT platform_audit_event_reason_length CHECK (char_length(reason) <= 500),
    CONSTRAINT platform_audit_event_detail_object CHECK (jsonb_typeof(detail) = 'object' AND pg_column_size(detail) <= 8192)
);

CREATE TRIGGER platform_audit_event_stamp BEFORE INSERT ON pandokos.platform_audit_event
    FOR EACH ROW EXECUTE FUNCTION pandokos.audit_stamp();
CREATE TRIGGER platform_audit_event_append_only BEFORE UPDATE OR DELETE ON pandokos.platform_audit_event
    FOR EACH ROW EXECUTE FUNCTION pandokos.append_only();
CREATE TRIGGER platform_audit_event_no_truncate BEFORE TRUNCATE ON pandokos.platform_audit_event
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only();

ALTER TABLE pandokos.platform_audit_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.platform_audit_event FORCE ROW LEVEL SECURITY;

CREATE POLICY platform_audit_event_insert ON pandokos.platform_audit_event
    FOR INSERT TO pandokos_admin
    WITH CHECK ((SELECT pandokos.platform_scope()));

GRANT INSERT (actor_type, actor_id, actor_name, key_id, action, resource_type, resource_id, outcome, request_id, reason, detail)
    ON pandokos.platform_audit_event TO pandokos_admin;
