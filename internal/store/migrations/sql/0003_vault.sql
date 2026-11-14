-- 0003_vault: key-encryption key versions, tenant data keys, provider credentials and their
-- metadata view, the credential a virtual key is pinned to, vault notifications. Runs in one
-- transaction as pandokos_owner. Every name is schema-qualified.

-- Versions of the key-encryption key that wrapped a data key of this database (global table).
-- The key itself is never in the database (VLT-09): a row holds an identifier only.
CREATE TABLE pandokos.kek_version (
    id         text        NOT NULL,
    provider   text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT kek_version_pkey PRIMARY KEY (id),
    CONSTRAINT kek_version_id_format CHECK (id ~ '^[a-z0-9][a-z0-9._:-]{0,126}$'),
    CONSTRAINT kek_version_provider_format CHECK (provider ~ '^[a-z][a-z0-9-]{0,30}$')
);

GRANT SELECT ON pandokos.kek_version TO pandokos_gateway, pandokos_admin, pandokos_worker;
GRANT INSERT (id, provider) ON pandokos.kek_version TO pandokos_admin;

-- Tells the instances that vault data of a tenant changed. The payload names a tenant, never a
-- credential or a key.
CREATE FUNCTION pandokos.notify_vault() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        PERFORM pg_catalog.pg_notify('pandokos_cache', 'vault:' || OLD.tenant_id::text);
    ELSE
        PERFORM pg_catalog.pg_notify('pandokos_cache', 'vault:' || NEW.tenant_id::text);
    END IF;
    RETURN NULL;
END
$$;

-- Data keys of tenants (tenant-scoped table). wrapped_key is the data key encrypted by the
-- key-encryption key version kek_version; it is useless without that key.
CREATE TABLE pandokos.tenant_data_key (
    tenant_id   uuid        NOT NULL,
    id          uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    version     integer     NOT NULL DEFAULT 1,
    kek_version text        NOT NULL,
    wrapped_key bytea       NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tenant_data_key_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT tenant_data_key_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT tenant_data_key_kek_version_fkey FOREIGN KEY (kek_version) REFERENCES pandokos.kek_version (id),
    CONSTRAINT tenant_data_key_version_key UNIQUE (tenant_id, version),
    CONSTRAINT tenant_data_key_version_positive CHECK (version >= 1),
    CONSTRAINT tenant_data_key_wrapped_key_length CHECK (octet_length(wrapped_key) BETWEEN 32 AND 1024)
);
CREATE INDEX tenant_data_key_kek_version_idx ON pandokos.tenant_data_key (kek_version);

CREATE FUNCTION pandokos.tenant_data_key_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NEW.version <> OLD.version THEN
        RAISE EXCEPTION 'the version of a data key is immutable' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER tenant_data_key_guard BEFORE INSERT OR UPDATE ON pandokos.tenant_data_key
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();
CREATE TRIGGER tenant_data_key_version_guard BEFORE UPDATE ON pandokos.tenant_data_key
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_data_key_guard();
CREATE TRIGGER tenant_data_key_notify AFTER UPDATE OR DELETE ON pandokos.tenant_data_key
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_vault();

ALTER TABLE pandokos.tenant_data_key ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.tenant_data_key FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.tenant_data_key
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.tenant_data_key TO pandokos_gateway, pandokos_admin;
GRANT INSERT (tenant_id, version, kek_version, wrapped_key) ON pandokos.tenant_data_key TO pandokos_admin;

-- Provider credentials of tenants (tenant-scoped table). secret is the sealed secret: only the
-- gateway role can read it, and it is destroyed when the credential is revoked.
CREATE TABLE pandokos.provider_credential (
    tenant_id        uuid        NOT NULL,
    id               uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    provider         text        NOT NULL,
    label            text        NOT NULL,
    description      text        NOT NULL DEFAULT '',
    state            text        NOT NULL DEFAULT 'active',
    is_default       boolean     NOT NULL DEFAULT false,
    key_version      integer     NOT NULL,
    secret           bytea,
    fingerprint      text        NOT NULL,
    hint             text        NOT NULL DEFAULT '',
    expires_at       timestamptz,
    validated_at     timestamptz,
    rotated_at       timestamptz,
    state_changed_at timestamptz NOT NULL DEFAULT now(),
    state_changed_by text        NOT NULL DEFAULT 'tenant',
    state_reason     text        NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT provider_credential_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT provider_credential_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT provider_credential_label_key UNIQUE (tenant_id, provider, label),
    CONSTRAINT provider_credential_provider_format CHECK (provider ~ '^[a-z][a-z0-9-]{0,62}$'),
    CONSTRAINT provider_credential_label_format CHECK (label ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    CONSTRAINT provider_credential_description_length CHECK (char_length(description) <= 500),
    CONSTRAINT provider_credential_state_valid CHECK (state IN ('active', 'disabled', 'revoked')),
    CONSTRAINT provider_credential_key_version_positive CHECK (key_version >= 1),
    CONSTRAINT provider_credential_secret_presence CHECK ((state = 'revoked') = (secret IS NULL)),
    CONSTRAINT provider_credential_secret_length CHECK (secret IS NULL OR octet_length(secret) BETWEEN 30 AND 8221),
    CONSTRAINT provider_credential_fingerprint_format CHECK (fingerprint ~ '^[0-9a-f]{32}$'),
    CONSTRAINT provider_credential_hint_length CHECK (char_length(hint) <= 4),
    CONSTRAINT provider_credential_state_changed_by_valid CHECK (state_changed_by IN ('tenant', 'platform')),
    CONSTRAINT provider_credential_state_reason_length CHECK (char_length(state_reason) <= 500)
);
-- At most one default per tenant and provider.
CREATE UNIQUE INDEX provider_credential_default_key ON pandokos.provider_credential (tenant_id, provider) WHERE is_default;

-- Lifecycle of a credential: created active; active and disabled alternate; revoked is final
-- and destroys the sealed secret; the provider never changes.
CREATE FUNCTION pandokos.provider_credential_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'active' THEN
            RAISE EXCEPTION 'a credential is created in state active' USING ERRCODE = 'check_violation';
        END IF;
        NEW.rotated_at := NULL;
        NEW.state_changed_at := now();
        RETURN NEW;
    END IF;
    IF NEW.provider <> OLD.provider THEN
        RAISE EXCEPTION 'the provider of a credential is immutable' USING ERRCODE = 'check_violation';
    END IF;
    NEW.rotated_at := OLD.rotated_at;
    NEW.state_changed_at := OLD.state_changed_at;
    IF OLD.state = 'revoked' THEN
        IF NEW.state <> 'revoked' OR NEW.secret IS NOT NULL THEN
            RAISE EXCEPTION 'a revoked credential stays revoked' USING ERRCODE = 'check_violation';
        END IF;
        IF NEW.is_default AND NOT OLD.is_default THEN
            RAISE EXCEPTION 'a revoked credential cannot become the default' USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.state <> OLD.state THEN
        NEW.state_changed_at := now();
    END IF;
    IF NEW.state = 'revoked' THEN
        NEW.secret := NULL;
        RETURN NEW;
    END IF;
    IF NEW.secret IS NULL THEN
        RAISE EXCEPTION 'only a revocation removes the secret' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.secret <> OLD.secret THEN
        NEW.rotated_at := now();
        IF NEW.validated_at IS NOT DISTINCT FROM OLD.validated_at THEN
            NEW.validated_at := NULL;
        END IF;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER provider_credential_guard BEFORE INSERT OR UPDATE ON pandokos.provider_credential
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();
CREATE TRIGGER provider_credential_lifecycle BEFORE INSERT OR UPDATE ON pandokos.provider_credential
    FOR EACH ROW EXECUTE FUNCTION pandokos.provider_credential_guard();
CREATE TRIGGER provider_credential_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.provider_credential
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_vault();

ALTER TABLE pandokos.provider_credential ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.provider_credential FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.provider_credential
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

-- The gateway reads everything, the sealed secret included, in the session of the tenant. The
-- admin role writes the sealed secret and can never read it back (AZ-08, VLT-04).
GRANT SELECT ON pandokos.provider_credential TO pandokos_gateway;
GRANT SELECT (tenant_id, id, provider, label, description, state, is_default, key_version, fingerprint, hint,
              expires_at, validated_at, rotated_at, state_changed_at, state_changed_by, state_reason, created_at, updated_at)
    ON pandokos.provider_credential TO pandokos_admin;
GRANT INSERT (tenant_id, id, provider, label, description, is_default, key_version, secret, fingerprint, hint,
              expires_at, validated_at)
    ON pandokos.provider_credential TO pandokos_admin;
GRANT UPDATE (label, description, state, is_default, key_version, secret, fingerprint, hint, expires_at, validated_at,
              state_changed_by, state_reason)
    ON pandokos.provider_credential TO pandokos_admin;
GRANT DELETE ON pandokos.provider_credential TO pandokos_admin;

-- What may be read about a credential by anybody but the gateway: every column except the
-- sealed secret. It runs with the privileges and the policies of the caller.
CREATE VIEW pandokos.provider_credential_metadata
    WITH (security_invoker = true, security_barrier = true) AS
SELECT c.tenant_id, c.id, c.provider, c.label, c.description, c.state, c.is_default, c.key_version, c.fingerprint,
       c.hint, c.expires_at, c.validated_at, c.rotated_at, c.state_changed_at, c.state_changed_by, c.state_reason,
       c.created_at, c.updated_at
  FROM pandokos.provider_credential c;

GRANT SELECT ON pandokos.provider_credential_metadata TO pandokos_admin;

-- The credential a virtual key is pinned to (VLT-01). It is set when the key is created and
-- never changes; deleting the credential clears it.
ALTER TABLE pandokos.virtual_key ADD COLUMN credential_id uuid;
ALTER TABLE pandokos.virtual_key ADD CONSTRAINT virtual_key_credential_fkey
    FOREIGN KEY (tenant_id, credential_id) REFERENCES pandokos.provider_credential (tenant_id, id)
    ON DELETE SET NULL (credential_id);
CREATE INDEX virtual_key_credential_idx ON pandokos.virtual_key (tenant_id, credential_id) WHERE credential_id IS NOT NULL;

CREATE FUNCTION pandokos.virtual_key_pin_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NEW.credential_id IS DISTINCT FROM OLD.credential_id AND NEW.credential_id IS NOT NULL THEN
        RAISE EXCEPTION 'the credential of a key is set when the key is created' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER virtual_key_pin_guard BEFORE UPDATE ON pandokos.virtual_key
    FOR EACH ROW EXECUTE FUNCTION pandokos.virtual_key_pin_guard();

GRANT INSERT (credential_id) ON pandokos.virtual_key TO pandokos_admin;
