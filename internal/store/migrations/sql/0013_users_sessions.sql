-- 0013_users_sessions: people (users, local accounts, federated identities, sessions), memberships
-- and their roles, platform roles of users, sign-in throttling, OIDC sign-in transactions and
-- idempotency keys; tokens of trusted issuers for the admin role. Runs in one transaction as
-- pandokos_owner. Every name is schema-qualified. The gateway and worker roles receive no
-- privilege on any table of this migration (IAM-22).

-- Timestamps of rows whose key is user_id; the key never changes.
CREATE FUNCTION pandokos.user_row_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.created_at := now();
        NEW.updated_at := now();
        RETURN NEW;
    END IF;
    IF NEW.user_id <> OLD.user_id THEN
        RAISE EXCEPTION 'user_id is immutable' USING ERRCODE = 'check_violation';
    END IF;
    NEW.created_at := OLD.created_at;
    NEW.updated_at := now();
    RETURN NEW;
END
$$;

-- Users (global table with explicit policies). tenant_id names the tenant that manages the
-- account; NULL: the platform does. A federated user is always managed by the platform.
CREATE TABLE pandokos.app_user (
    id               uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    sign_in          text        NOT NULL,
    tenant_id        uuid,
    display_name     text        NOT NULL DEFAULT '',
    email            text        NOT NULL DEFAULT '',
    state            text        NOT NULL DEFAULT 'active',
    state_changed_at timestamptz NOT NULL DEFAULT now(),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT app_user_pkey PRIMARY KEY (id),
    CONSTRAINT app_user_id_sign_in_key UNIQUE (id, sign_in),
    CONSTRAINT app_user_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT app_user_sign_in_valid CHECK (sign_in IN ('local', 'oidc')),
    CONSTRAINT app_user_federated_is_platform CHECK (sign_in = 'local' OR tenant_id IS NULL),
    CONSTRAINT app_user_display_name_format CHECK (char_length(display_name) <= 200 AND display_name !~ '[[:cntrl:]]'),
    CONSTRAINT app_user_email_format CHECK (char_length(email) <= 254 AND email !~ '[[:cntrl:][:space:]]'),
    CONSTRAINT app_user_state_valid CHECK (state IN ('active', 'disabled'))
);
CREATE INDEX app_user_tenant_idx ON pandokos.app_user (tenant_id) WHERE tenant_id IS NOT NULL;

CREATE FUNCTION pandokos.app_user_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NEW.sign_in <> OLD.sign_in OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id THEN
        RAISE EXCEPTION 'sign_in and tenant_id of a user are immutable' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.state <> OLD.state THEN
        NEW.state_changed_at := now();
    ELSE
        NEW.state_changed_at := OLD.state_changed_at;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER app_user_row_guard BEFORE INSERT OR UPDATE ON pandokos.app_user
    FOR EACH ROW EXECUTE FUNCTION pandokos.global_row_guard();
CREATE TRIGGER app_user_guard BEFORE UPDATE ON pandokos.app_user
    FOR EACH ROW EXECUTE FUNCTION pandokos.app_user_guard();

ALTER TABLE pandokos.app_user ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.app_user FORCE ROW LEVEL SECURITY;

GRANT SELECT ON pandokos.app_user TO pandokos_admin;
GRANT INSERT (sign_in, tenant_id, display_name, email) ON pandokos.app_user TO pandokos_admin;
GRANT UPDATE (display_name, email, state) ON pandokos.app_user TO pandokos_admin;

-- Memberships (tenant-scoped table): a user is a member of a tenant. source says who keeps the
-- row: an administrator ('manual') or the group mapping at sign-in ('idp').
CREATE TABLE pandokos.membership (
    tenant_id  uuid        NOT NULL,
    id         uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    user_id    uuid        NOT NULL,
    source     text        NOT NULL DEFAULT 'manual',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT membership_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT membership_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT membership_user_fkey FOREIGN KEY (user_id) REFERENCES pandokos.app_user (id),
    CONSTRAINT membership_user_key UNIQUE (tenant_id, user_id),
    CONSTRAINT membership_source_valid CHECK (source IN ('manual', 'idp'))
);
CREATE INDEX membership_user_idx ON pandokos.membership (user_id);

-- A user that a tenant manages is a member of that tenant only (sprint 4.2, D-1): otherwise the
-- administrators of one tenant could reset the password of a member of another.
CREATE FUNCTION pandokos.membership_guard() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    home uuid;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.user_id <> OLD.user_id THEN
            RAISE EXCEPTION 'user_id of a membership is immutable' USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;
    SELECT u.tenant_id INTO home FROM pandokos.app_user u WHERE u.id = NEW.user_id;
    IF home IS NOT NULL AND home <> NEW.tenant_id THEN
        RAISE EXCEPTION 'a user managed by a tenant is a member of that tenant only' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER membership_row_guard BEFORE INSERT OR UPDATE ON pandokos.membership
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();
CREATE TRIGGER membership_guard BEFORE INSERT OR UPDATE ON pandokos.membership
    FOR EACH ROW EXECUTE FUNCTION pandokos.membership_guard();

ALTER TABLE pandokos.membership ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.membership FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.membership
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.membership TO pandokos_admin;
GRANT INSERT (tenant_id, user_id, source) ON pandokos.membership TO pandokos_admin;
GRANT UPDATE (source) ON pandokos.membership TO pandokos_admin;
GRANT DELETE ON pandokos.membership TO pandokos_admin;

-- Roles of a member (tenant-scoped table). The role is the name of a built-in role; role_assignment
-- keeps the roles of service accounts, and the gateway role reads that table only.
CREATE TABLE pandokos.membership_role (
    tenant_id  uuid        NOT NULL,
    id         uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    user_id    uuid        NOT NULL,
    role       text        NOT NULL,
    source     text        NOT NULL DEFAULT 'manual',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT membership_role_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT membership_role_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT membership_role_membership_fkey FOREIGN KEY (tenant_id, user_id)
        REFERENCES pandokos.membership (tenant_id, user_id) ON DELETE CASCADE,
    CONSTRAINT membership_role_key UNIQUE (tenant_id, user_id, role),
    CONSTRAINT membership_role_role_format CHECK (role ~ '^[a-z][a-z0-9-]{0,62}$'),
    CONSTRAINT membership_role_source_valid CHECK (source IN ('manual', 'idp'))
);

CREATE TRIGGER membership_role_row_guard BEFORE INSERT OR UPDATE ON pandokos.membership_role
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();

ALTER TABLE pandokos.membership_role ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.membership_role FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.membership_role
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.membership_role TO pandokos_admin;
GRANT INSERT (tenant_id, user_id, role, source) ON pandokos.membership_role TO pandokos_admin;
GRANT DELETE ON pandokos.membership_role TO pandokos_admin;

-- Which tenants a user is a member of (global table, no grant to any runtime role), kept by a
-- trigger and read only through user_tenants.
CREATE TABLE pandokos.membership_directory (
    user_id   uuid NOT NULL,
    tenant_id uuid NOT NULL,
    CONSTRAINT membership_directory_pkey PRIMARY KEY (user_id, tenant_id),
    CONSTRAINT membership_directory_user_fkey FOREIGN KEY (user_id) REFERENCES pandokos.app_user (id),
    CONSTRAINT membership_directory_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id)
);

CREATE FUNCTION pandokos.membership_directory_sync() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM pandokos.membership_directory d WHERE d.user_id = OLD.user_id AND d.tenant_id = OLD.tenant_id;
    ELSE
        INSERT INTO pandokos.membership_directory (user_id, tenant_id) VALUES (NEW.user_id, NEW.tenant_id);
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER membership_directory_sync AFTER INSERT OR DELETE ON pandokos.membership
    FOR EACH ROW EXECUTE FUNCTION pandokos.membership_directory_sync();

-- The tenants of one user. It crosses the tenant policy for one fact, for one user, and only in
-- the platform scope (sign-in and GET /v1/me); roles are then read in each tenant's session.
CREATE FUNCTION pandokos.user_tenants(p_user_id uuid) RETURNS SETOF uuid
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NOT pandokos.platform_scope() THEN
        RAISE EXCEPTION 'user_tenants needs the platform scope' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN QUERY SELECT d.tenant_id FROM pandokos.membership_directory d WHERE d.user_id = p_user_id ORDER BY d.tenant_id;
END
$$;

REVOKE ALL ON FUNCTION pandokos.user_tenants(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.user_tenants(uuid) TO pandokos_admin;

-- Policies of app_user. The platform scope sees and writes every user (sign-in happens there); a
-- tenant's session sees and writes the users it manages and sees its members.
CREATE POLICY app_user_platform ON pandokos.app_user
    FOR ALL TO pandokos_admin
    USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY app_user_managed ON pandokos.app_user
    FOR ALL TO pandokos_admin
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));
CREATE POLICY app_user_member ON pandokos.app_user
    FOR SELECT TO pandokos_admin
    USING (EXISTS (SELECT 1 FROM pandokos.membership m
                    WHERE m.tenant_id = (SELECT pandokos.current_tenant_id()) AND m.user_id = app_user.id));
-- The definer functions of this migration run as the owner, which the forced policies would
-- otherwise leave without any row.
CREATE POLICY app_user_owner ON pandokos.app_user FOR SELECT TO pandokos_owner USING (true);

-- Local accounts (global table with explicit policies): username and password hash of a user.
-- The hash is an Argon2id PHC string that carries its parameters and salt.
CREATE TABLE pandokos.local_account (
    user_id             uuid        NOT NULL,
    sign_in             text        NOT NULL DEFAULT 'local',
    username            text        NOT NULL,
    password_hash       text        NOT NULL,
    must_change         boolean     NOT NULL DEFAULT true,
    password_changed_at timestamptz NOT NULL DEFAULT now(),
    password_expires_at timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT local_account_pkey PRIMARY KEY (user_id),
    CONSTRAINT local_account_user_fkey FOREIGN KEY (user_id, sign_in) REFERENCES pandokos.app_user (id, sign_in),
    CONSTRAINT local_account_sign_in_valid CHECK (sign_in = 'local'),
    CONSTRAINT local_account_username_key UNIQUE (username),
    CONSTRAINT local_account_username_format CHECK (username ~ '^[a-z0-9][a-z0-9._@+-]{1,62}[a-z0-9]$'),
    CONSTRAINT local_account_password_hash_format CHECK (
        password_hash ~ '^\$argon2id\$v=19\$m=[0-9]{1,8},t=[0-9]{1,3},p=[0-9]{1,3}\$[A-Za-z0-9+/]{22,86}\$[A-Za-z0-9+/]{43,86}$'),
    CONSTRAINT local_account_expiry_only_generated CHECK (must_change OR password_expires_at IS NULL)
);

CREATE TRIGGER local_account_guard BEFORE INSERT OR UPDATE ON pandokos.local_account
    FOR EACH ROW EXECUTE FUNCTION pandokos.user_row_guard();

ALTER TABLE pandokos.local_account ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.local_account FORCE ROW LEVEL SECURITY;

CREATE POLICY local_account_platform ON pandokos.local_account
    FOR ALL TO pandokos_admin
    USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY local_account_managed ON pandokos.local_account
    FOR ALL TO pandokos_admin
    USING (EXISTS (SELECT 1 FROM pandokos.app_user u
                    WHERE u.id = local_account.user_id AND u.tenant_id = (SELECT pandokos.current_tenant_id())))
    WITH CHECK (EXISTS (SELECT 1 FROM pandokos.app_user u
                         WHERE u.id = local_account.user_id AND u.tenant_id = (SELECT pandokos.current_tenant_id())));
CREATE POLICY local_account_member ON pandokos.local_account
    FOR SELECT TO pandokos_admin
    USING (EXISTS (SELECT 1 FROM pandokos.membership m
                    WHERE m.tenant_id = (SELECT pandokos.current_tenant_id()) AND m.user_id = local_account.user_id));
CREATE POLICY local_account_owner ON pandokos.local_account FOR SELECT TO pandokos_owner USING (true);

GRANT SELECT ON pandokos.local_account TO pandokos_admin;
GRANT INSERT (user_id, username, password_hash, must_change, password_expires_at) ON pandokos.local_account TO pandokos_admin;
GRANT UPDATE (password_hash, must_change, password_changed_at, password_expires_at) ON pandokos.local_account TO pandokos_admin;

-- Federated identities (global table with explicit policies): the issuer and subject of the
-- identity provider that a user signs in with. Platform scope only.
CREATE TABLE pandokos.user_identity (
    user_id    uuid        NOT NULL,
    sign_in    text        NOT NULL DEFAULT 'oidc',
    issuer     text        NOT NULL,
    subject    text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT user_identity_pkey PRIMARY KEY (user_id),
    CONSTRAINT user_identity_user_fkey FOREIGN KEY (user_id, sign_in) REFERENCES pandokos.app_user (id, sign_in),
    CONSTRAINT user_identity_sign_in_valid CHECK (sign_in = 'oidc'),
    CONSTRAINT user_identity_subject_key UNIQUE (issuer, subject),
    CONSTRAINT user_identity_issuer_format CHECK (issuer ~ '^https://[^[:space:][:cntrl:]?#@]+$' AND octet_length(issuer) <= 255),
    CONSTRAINT user_identity_subject_format CHECK (char_length(subject) BETWEEN 1 AND 255 AND subject !~ '[[:cntrl:]]')
);

CREATE TRIGGER user_identity_guard BEFORE INSERT OR UPDATE ON pandokos.user_identity
    FOR EACH ROW EXECUTE FUNCTION pandokos.user_row_guard();

ALTER TABLE pandokos.user_identity ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.user_identity FORCE ROW LEVEL SECURITY;

CREATE POLICY user_identity_platform ON pandokos.user_identity
    FOR ALL TO pandokos_admin
    USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.user_identity TO pandokos_admin;
GRANT INSERT (user_id, issuer, subject) ON pandokos.user_identity TO pandokos_admin;

-- Sessions (global table with explicit policies). Only the SHA-256 of the token is stored. A row
-- changes in last_used_at only; a session ends by deletion.
CREATE TABLE pandokos.user_session (
    id           uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    token_hash   bytea       NOT NULL,
    user_id      uuid        NOT NULL,
    method       text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    CONSTRAINT user_session_pkey PRIMARY KEY (id),
    CONSTRAINT user_session_token_hash_key UNIQUE (token_hash),
    CONSTRAINT user_session_user_fkey FOREIGN KEY (user_id) REFERENCES pandokos.app_user (id),
    CONSTRAINT user_session_token_hash_length CHECK (octet_length(token_hash) = 32),
    CONSTRAINT user_session_method_valid CHECK (method IN ('local', 'oidc')),
    CONSTRAINT user_session_lifetime CHECK (expires_at > created_at AND expires_at <= created_at + interval '7 days')
);
CREATE INDEX user_session_user_idx ON pandokos.user_session (user_id);
CREATE INDEX user_session_expires_idx ON pandokos.user_session (expires_at);

CREATE FUNCTION pandokos.user_session_stamp() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    NEW.id := pandokos.uuid_v7();
    NEW.created_at := now();
    NEW.last_used_at := now();
    RETURN NEW;
END
$$;

CREATE TRIGGER user_session_stamp BEFORE INSERT ON pandokos.user_session
    FOR EACH ROW EXECUTE FUNCTION pandokos.user_session_stamp();

ALTER TABLE pandokos.user_session ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.user_session FORCE ROW LEVEL SECURITY;

CREATE POLICY user_session_platform ON pandokos.user_session
    FOR ALL TO pandokos_admin
    USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY user_session_managed_select ON pandokos.user_session
    FOR SELECT TO pandokos_admin
    USING (EXISTS (SELECT 1 FROM pandokos.app_user u
                    WHERE u.id = user_session.user_id AND u.tenant_id = (SELECT pandokos.current_tenant_id())));
CREATE POLICY user_session_managed_delete ON pandokos.user_session
    FOR DELETE TO pandokos_admin
    USING (EXISTS (SELECT 1 FROM pandokos.app_user u
                    WHERE u.id = user_session.user_id AND u.tenant_id = (SELECT pandokos.current_tenant_id())));

GRANT SELECT ON pandokos.user_session TO pandokos_admin;
GRANT INSERT (token_hash, user_id, method, expires_at) ON pandokos.user_session TO pandokos_admin;
GRANT UPDATE (last_used_at) ON pandokos.user_session TO pandokos_admin;
GRANT DELETE ON pandokos.user_session TO pandokos_admin;

-- Platform roles of users (global table with explicit policies). Only a user that the platform
-- manages can hold one.
CREATE TABLE pandokos.platform_role_assignment (
    id         uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    user_id    uuid        NOT NULL,
    role       text        NOT NULL,
    source     text        NOT NULL DEFAULT 'manual',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT platform_role_assignment_pkey PRIMARY KEY (id),
    CONSTRAINT platform_role_assignment_user_fkey FOREIGN KEY (user_id) REFERENCES pandokos.app_user (id),
    CONSTRAINT platform_role_assignment_key UNIQUE (user_id, role),
    CONSTRAINT platform_role_assignment_role_format CHECK (role ~ '^[a-z][a-z0-9-]{0,62}$'),
    CONSTRAINT platform_role_assignment_source_valid CHECK (source IN ('manual', 'idp'))
);

CREATE FUNCTION pandokos.platform_role_assignment_guard() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF (SELECT u.tenant_id FROM pandokos.app_user u WHERE u.id = NEW.user_id) IS NOT NULL THEN
        RAISE EXCEPTION 'a user managed by a tenant holds no platform role' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER platform_role_assignment_row_guard BEFORE INSERT OR UPDATE ON pandokos.platform_role_assignment
    FOR EACH ROW EXECUTE FUNCTION pandokos.global_row_guard();
CREATE TRIGGER platform_role_assignment_guard BEFORE INSERT ON pandokos.platform_role_assignment
    FOR EACH ROW EXECUTE FUNCTION pandokos.platform_role_assignment_guard();

ALTER TABLE pandokos.platform_role_assignment ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.platform_role_assignment FORCE ROW LEVEL SECURITY;

CREATE POLICY platform_role_assignment_platform ON pandokos.platform_role_assignment
    FOR ALL TO pandokos_admin
    USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.platform_role_assignment TO pandokos_admin;
GRANT INSERT (user_id, role, source) ON pandokos.platform_role_assignment TO pandokos_admin;
GRANT DELETE ON pandokos.platform_role_assignment TO pandokos_admin;

-- The ID of a user that the platform manages, by ID or by username, or NULL: what a tenant's
-- session needs to add such a user as a member, and cannot see. It takes one ID or one username,
-- returns one ID, cannot list, and answers in a tenant's session only.
CREATE FUNCTION pandokos.platform_user_lookup(p_user_id uuid, p_username text) RETURNS uuid
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
    RETURN (SELECT u.id
              FROM pandokos.app_user u
              LEFT JOIN pandokos.local_account a ON a.user_id = u.id
             WHERE u.tenant_id IS NULL
               AND pandokos.current_tenant_id() IS NOT NULL
               AND pg_catalog.num_nonnulls(p_user_id, p_username) = 1
               AND (u.id = p_user_id OR a.username = p_username));

REVOKE ALL ON FUNCTION pandokos.platform_user_lookup(uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.platform_user_lookup(uuid, text) TO pandokos_admin;

-- Sign-in throttling (global table with explicit policies, platform scope only). The key is the
-- SHA-256 of the kind and of the lower-case username or of the source address prefix: a row
-- exists for a username whether or not an account has it.
CREATE TABLE pandokos.signin_throttle (
    key               bytea       NOT NULL,
    kind              text        NOT NULL,
    failures          integer     NOT NULL DEFAULT 0,
    window_started_at timestamptz NOT NULL DEFAULT now(),
    last_failure_at   timestamptz NOT NULL DEFAULT now(),
    locked_until      timestamptz,
    CONSTRAINT signin_throttle_pkey PRIMARY KEY (key),
    CONSTRAINT signin_throttle_key_length CHECK (octet_length(key) = 32),
    CONSTRAINT signin_throttle_kind_valid CHECK (kind IN ('account', 'source')),
    CONSTRAINT signin_throttle_failures_range CHECK (failures BETWEEN 0 AND 1000000)
);
CREATE INDEX signin_throttle_last_failure_idx ON pandokos.signin_throttle (last_failure_at);

ALTER TABLE pandokos.signin_throttle ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.signin_throttle FORCE ROW LEVEL SECURITY;

CREATE POLICY signin_throttle_platform ON pandokos.signin_throttle
    FOR ALL TO pandokos_admin
    USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));

GRANT SELECT, DELETE ON pandokos.signin_throttle TO pandokos_admin;
GRANT INSERT (key, kind, failures, window_started_at, last_failure_at, locked_until) ON pandokos.signin_throttle TO pandokos_admin;
GRANT UPDATE (failures, window_started_at, last_failure_at, locked_until) ON pandokos.signin_throttle TO pandokos_admin;

-- Sign-in transactions of the OIDC relying party (global table with explicit policies, platform
-- scope only). The row holds the SHA-256 of the state and nothing else: it makes a state usable
-- once and for a bounded time. Nonce and code verifier are never stored.
CREATE TABLE pandokos.oidc_login (
    state_hash bytea       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    CONSTRAINT oidc_login_pkey PRIMARY KEY (state_hash),
    CONSTRAINT oidc_login_state_hash_length CHECK (octet_length(state_hash) = 32),
    CONSTRAINT oidc_login_lifetime CHECK (expires_at > created_at AND expires_at <= created_at + interval '1 hour')
);
CREATE INDEX oidc_login_expires_idx ON pandokos.oidc_login (expires_at);

ALTER TABLE pandokos.oidc_login ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.oidc_login FORCE ROW LEVEL SECURITY;

CREATE POLICY oidc_login_platform ON pandokos.oidc_login
    FOR ALL TO pandokos_admin
    USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));

GRANT SELECT, DELETE ON pandokos.oidc_login TO pandokos_admin;
GRANT INSERT (state_hash, expires_at) ON pandokos.oidc_login TO pandokos_admin;

-- Idempotency keys of create operations (global table with explicit policies). A row belongs to
-- a tenant (operations under /v1/tenants/{tenant}) or to the platform (tenant_id NULL), and to one
-- principal. request_hash is SHA-256(salt || operation || path || body). The stored response never
-- holds a member that is shown once.
CREATE TABLE pandokos.idempotency_key (
    id              uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    tenant_id       uuid,
    principal_kind  text        NOT NULL,
    principal_id    text        NOT NULL,
    key             text        NOT NULL,
    operation       text        NOT NULL,
    salt            bytea       NOT NULL,
    request_hash    bytea       NOT NULL,
    attempt         integer     NOT NULL DEFAULT 1,
    locked_until    timestamptz NOT NULL,
    committed_at    timestamptz,
    resource_type   text        NOT NULL DEFAULT '',
    resource_id     text        NOT NULL DEFAULT '',
    response_status integer,
    response_body   jsonb,
    completed_at    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,
    CONSTRAINT idempotency_key_pkey PRIMARY KEY (id),
    CONSTRAINT idempotency_key_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT idempotency_key_principal_kind_valid CHECK (principal_kind IN ('platform', 'service_account', 'user')),
    CONSTRAINT idempotency_key_principal_id_format CHECK (principal_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    CONSTRAINT idempotency_key_key_format CHECK (key ~ '^[\x21-\x7e]{1,128}$'),
    CONSTRAINT idempotency_key_operation_format CHECK (operation ~ '^[A-Za-z][A-Za-z0-9]{0,63}$'),
    CONSTRAINT idempotency_key_salt_length CHECK (octet_length(salt) = 16),
    CONSTRAINT idempotency_key_request_hash_length CHECK (octet_length(request_hash) = 32),
    CONSTRAINT idempotency_key_attempt_positive CHECK (attempt >= 1),
    CONSTRAINT idempotency_key_response_complete CHECK (
        (completed_at IS NULL AND response_status IS NULL AND response_body IS NULL)
        OR (completed_at IS NOT NULL AND committed_at IS NOT NULL AND response_status BETWEEN 200 AND 299
            AND jsonb_typeof(response_body) = 'object' AND pg_column_size(response_body) <= 262144)),
    CONSTRAINT idempotency_key_resource_format CHECK (resource_type ~ '^[a-z_]{0,64}$' AND char_length(resource_id) <= 64)
);
CREATE UNIQUE INDEX idempotency_key_key ON pandokos.idempotency_key
    ((COALESCE(tenant_id, '00000000-0000-0000-0000-000000000000'::uuid)), principal_kind, principal_id, key);
CREATE INDEX idempotency_key_expires_idx ON pandokos.idempotency_key (expires_at);

ALTER TABLE pandokos.idempotency_key ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.idempotency_key FORCE ROW LEVEL SECURITY;

CREATE POLICY idempotency_key_tenant ON pandokos.idempotency_key
    FOR ALL TO pandokos_admin
    USING (tenant_id IS NOT NULL AND tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id IS NOT NULL AND tenant_id = (SELECT pandokos.current_tenant_id()));
CREATE POLICY idempotency_key_platform ON pandokos.idempotency_key
    FOR ALL TO pandokos_admin
    USING (tenant_id IS NULL AND (SELECT pandokos.platform_scope()))
    WITH CHECK (tenant_id IS NULL AND (SELECT pandokos.platform_scope()));

GRANT SELECT, DELETE ON pandokos.idempotency_key TO pandokos_admin;
GRANT INSERT (tenant_id, principal_kind, principal_id, key, operation, salt, request_hash, locked_until, expires_at)
    ON pandokos.idempotency_key TO pandokos_admin;
GRANT UPDATE (attempt, locked_until, committed_at, resource_type, resource_id, response_status, response_body, completed_at)
    ON pandokos.idempotency_key TO pandokos_admin;

-- The host commands (pandokos user reset-password, unlock) record what they did to a user that a
-- tenant manages in that tenant's log too.
ALTER TABLE pandokos.audit_event DROP CONSTRAINT audit_event_actor_type_valid;
ALTER TABLE pandokos.audit_event ADD CONSTRAINT audit_event_actor_type_valid
    CHECK (actor_type IN ('platform', 'service_account', 'user', 'anonymous', 'host'));

-- Tokens of trusted issuers on the administration listener (tokens.admin_listener): the admin role
-- selects a registration and the tenant of a platform-level token as the gateway role does.
GRANT EXECUTE ON FUNCTION pandokos.issuer_lookup(text, text[]) TO pandokos_admin;
GRANT EXECUTE ON FUNCTION pandokos.platform_issuer_tenant_lookup(uuid, text) TO pandokos_admin;
