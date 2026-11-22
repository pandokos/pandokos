-- 0014_roles_support: custom roles of a tenant, support actions of the platform inside a tenant,
-- the support action on audit rows, and the role owner for the identities that held every tenant
-- permission before it existed. Runs in one transaction as pandokos_owner. Every name is
-- schema-qualified. No function of this migration is SECURITY DEFINER.

-- ---------------------------------------------------------------------------------------------
-- Custom roles (tenant-scoped table). A row is a named set of catalogue permissions; the
-- catalogue and the built-in roles are in code (ADR 0007). role_assignment.role and
-- membership_role.role hold the name of a built-in role or of a row of this table.
CREATE TABLE pandokos.tenant_role (
    tenant_id   uuid        NOT NULL,
    id          uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    name        text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    permissions text[]      NOT NULL,
    state       text        NOT NULL DEFAULT 'active',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tenant_role_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT tenant_role_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT tenant_role_name_key UNIQUE (tenant_id, name),
    CONSTRAINT tenant_role_name_format CHECK (name ~ '^[a-z][a-z0-9-]{0,62}$'),
    -- The names of the built-in roles, and two prefixes kept for roles that the product adds later.
    CONSTRAINT tenant_role_name_not_builtin CHECK (
        name NOT IN ('owner', 'tenant-admin', 'credential-manager', 'usage-viewer', 'auditor', 'developer',
                     'inference', 'read-only', 'platform-admin', 'platform-auditor')
        AND name !~ '^(platform|pandokos)-'),
    CONSTRAINT tenant_role_description_format CHECK (char_length(description) <= 500 AND description !~ '[[:cntrl:]]'),
    CONSTRAINT tenant_role_permissions_format CHECK (
        cardinality(permissions) BETWEEN 1 AND 64
        AND array_position(permissions, NULL) IS NULL
        AND array_to_string(permissions, ' ') ~ '^[a-z_]+:[a-z_]+( [a-z_]+:[a-z_]+)*$'),
    CONSTRAINT tenant_role_state_valid CHECK (state IN ('active', 'inactive'))
);

-- The name of a role never changes: assignments hold it. A role that is assigned to a member or
-- to a service account is not deleted (AZ-05). The handler checks first, with the row locked;
-- this trigger is the backstop.
CREATE FUNCTION pandokos.tenant_role_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.name <> OLD.name THEN
            RAISE EXCEPTION 'the name of a role is immutable' USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;
    IF EXISTS (SELECT 1 FROM pandokos.membership_role r WHERE r.tenant_id = OLD.tenant_id AND r.role = OLD.name)
       OR EXISTS (SELECT 1 FROM pandokos.role_assignment r WHERE r.tenant_id = OLD.tenant_id AND r.role = OLD.name) THEN
        RAISE EXCEPTION 'the role is assigned' USING ERRCODE = 'foreign_key_violation', CONSTRAINT = 'tenant_role_in_use';
    END IF;
    RETURN OLD;
END
$$;

CREATE TRIGGER tenant_role_row_guard BEFORE INSERT OR UPDATE ON pandokos.tenant_role
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();
CREATE TRIGGER tenant_role_guard BEFORE UPDATE OR DELETE ON pandokos.tenant_role
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_role_guard();
-- The gateways cache principals with the permissions of their roles (ADR 0030).
CREATE TRIGGER tenant_role_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.tenant_role
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_cache('scoped');

CREATE INDEX role_assignment_role_idx ON pandokos.role_assignment (tenant_id, role);
CREATE INDEX membership_role_role_idx ON pandokos.membership_role (tenant_id, role);

ALTER TABLE pandokos.tenant_role ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.tenant_role FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.tenant_role
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

-- The gateway reads the permissions of the roles of a service account; it writes nothing.
GRANT SELECT ON pandokos.tenant_role TO pandokos_gateway, pandokos_admin;
GRANT INSERT (tenant_id, name, description, permissions) ON pandokos.tenant_role TO pandokos_admin;
GRANT UPDATE (description, permissions, state) ON pandokos.tenant_role TO pandokos_admin;
GRANT DELETE ON pandokos.tenant_role TO pandokos_admin;

-- ---------------------------------------------------------------------------------------------
-- Support actions (global table with explicit policies). A row says that a platform principal
-- may act inside one tenant with the listed permissions until it expires or is closed, and why.
-- The platform scope sees every row; a tenant's session sees and ends the rows of its tenant.
-- A row is never deleted here (tenant deletion removes it, sprint 6.3).
CREATE TABLE pandokos.support_action (
    id           uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    tenant_id    uuid        NOT NULL,
    actor_type   text        NOT NULL,
    actor_id     text        NOT NULL,
    actor_name   text        NOT NULL DEFAULT '',
    key_id       text        NOT NULL DEFAULT '',
    reason       text        NOT NULL,
    permissions  text[]      NOT NULL,
    request_id   text        NOT NULL DEFAULT '',
    opened_at    timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    closed_at    timestamptz,
    closed_by    text        NOT NULL DEFAULT '',
    closed_by_id text        NOT NULL DEFAULT '',
    close_reason text        NOT NULL DEFAULT '',
    CONSTRAINT support_action_pkey PRIMARY KEY (id),
    CONSTRAINT support_action_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT support_action_actor_type_valid CHECK (actor_type IN ('platform', 'user')),
    CONSTRAINT support_action_actor_id_format CHECK (actor_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    CONSTRAINT support_action_actor_name_format CHECK (char_length(actor_name) <= 200 AND actor_name !~ '[[:cntrl:]]'),
    CONSTRAINT support_action_key_id_format CHECK (key_id ~ '^[A-Za-z0-9_-]{0,64}$'),
    CONSTRAINT support_action_reason_format CHECK (char_length(reason) BETWEEN 1 AND 500 AND reason ~ '[^[:space:]]'),
    CONSTRAINT support_action_permissions_format CHECK (
        cardinality(permissions) BETWEEN 1 AND 64
        AND array_position(permissions, NULL) IS NULL
        AND array_to_string(permissions, ' ') ~ '^[a-z_]+:[a-z_]+( [a-z_]+:[a-z_]+)*$'),
    CONSTRAINT support_action_request_id_format CHECK (request_id ~ '^([0-9a-f]{32})?$'),
    CONSTRAINT support_action_lifetime CHECK (expires_at > opened_at AND expires_at <= opened_at + interval '24 hours'),
    CONSTRAINT support_action_closed_by_valid CHECK (closed_by IN ('', 'platform', 'tenant', 'expiry')),
    CONSTRAINT support_action_close_complete CHECK ((closed_at IS NULL) = (closed_by = '')),
    CONSTRAINT support_action_closed_by_id_format CHECK (closed_by_id ~ '^[A-Za-z0-9_-]{0,64}$'),
    CONSTRAINT support_action_close_reason_length CHECK (char_length(close_reason) <= 500)
);
-- One open action for an actor in a tenant: opening another closes the first.
CREATE UNIQUE INDEX support_action_open_key ON pandokos.support_action (tenant_id, actor_type, actor_id)
    WHERE closed_at IS NULL;
CREATE INDEX support_action_tenant_idx ON pandokos.support_action (tenant_id, opened_at DESC);
CREATE INDEX support_action_actor_idx ON pandokos.support_action (actor_type, actor_id) WHERE closed_at IS NULL;

-- The time of opening is the database's (the handler writes expires_at as now() plus the duration,
-- in the same transaction). A row changes once:
-- when it is closed, at the time of the database, or at its expiry when it is closed as expired.
CREATE FUNCTION pandokos.support_action_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.opened_at := now();
        IF NEW.closed_at IS NOT NULL THEN
            RAISE EXCEPTION 'a support action is opened open' USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.closed_at IS NOT NULL THEN
        RAISE EXCEPTION 'a closed support action cannot be changed' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.id <> OLD.id OR NEW.tenant_id <> OLD.tenant_id OR NEW.actor_type <> OLD.actor_type OR NEW.actor_id <> OLD.actor_id
       OR NEW.actor_name <> OLD.actor_name OR NEW.key_id <> OLD.key_id OR NEW.reason <> OLD.reason
       OR NEW.permissions <> OLD.permissions OR NEW.request_id <> OLD.request_id
       OR NEW.opened_at <> OLD.opened_at OR NEW.expires_at <> OLD.expires_at THEN
        RAISE EXCEPTION 'a support action is immutable except for its closing' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.closed_by = 'expiry' AND now() < OLD.expires_at THEN
        RAISE EXCEPTION 'a support action that has not expired is not closed as expired' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.closed_at IS NOT NULL THEN
        NEW.closed_at := CASE WHEN NEW.closed_by = 'expiry' THEN OLD.expires_at ELSE LEAST(now(), OLD.expires_at) END;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER support_action_guard BEFORE INSERT OR UPDATE ON pandokos.support_action
    FOR EACH ROW EXECUTE FUNCTION pandokos.support_action_guard();

ALTER TABLE pandokos.support_action ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.support_action FORCE ROW LEVEL SECURITY;

CREATE POLICY support_action_platform ON pandokos.support_action
    FOR ALL TO pandokos_admin
    USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY support_action_tenant_read ON pandokos.support_action
    FOR SELECT TO pandokos_admin
    USING (tenant_id = (SELECT pandokos.current_tenant_id()));
CREATE POLICY support_action_tenant_end ON pandokos.support_action
    FOR UPDATE TO pandokos_admin
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.support_action TO pandokos_admin;
GRANT INSERT (tenant_id, actor_type, actor_id, actor_name, key_id, reason, permissions, request_id, expires_at)
    ON pandokos.support_action TO pandokos_admin;
GRANT UPDATE (closed_at, closed_by, closed_by_id, close_reason) ON pandokos.support_action TO pandokos_admin;

-- ---------------------------------------------------------------------------------------------
-- What a platform principal changes in a tenant under a support action names the action. Adding
-- a nullable column rewrites no row and fires no row trigger of the append-only table.
ALTER TABLE pandokos.audit_event ADD COLUMN support_action_id uuid;
GRANT INSERT (support_action_id) ON pandokos.audit_event TO pandokos_admin;
CREATE INDEX audit_event_support_action_idx ON pandokos.audit_event (tenant_id, support_action_id)
    WHERE support_action_id IS NOT NULL;

-- ---------------------------------------------------------------------------------------------
-- Until this migration tenant-admin held every tenant permission. The role owner now does, and
-- tenant-admin every one but those kept for owners. An identity that holds tenant-admin by an
-- administrator's hand becomes an owner as well, so that every existing tenant has one and nobody
-- holds less than before. Rows kept by the group rules (source idp) are left to the rules.
-- The owner of the tables is not subject to their policies while they are not forced (as in
-- migration 0011); both tables are forced again before the migration ends.
ALTER TABLE pandokos.role_assignment NO FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.membership_role NO FORCE ROW LEVEL SECURITY;

INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role)
SELECT r.tenant_id, r.service_account_id, 'owner'
  FROM pandokos.role_assignment r
 WHERE r.role = 'tenant-admin'
ON CONFLICT ON CONSTRAINT role_assignment_key DO NOTHING;

INSERT INTO pandokos.membership_role (tenant_id, user_id, role, source)
SELECT r.tenant_id, r.user_id, 'owner', 'manual'
  FROM pandokos.membership_role r
 WHERE r.role = 'tenant-admin' AND r.source = 'manual'
ON CONFLICT ON CONSTRAINT membership_role_key DO NOTHING;

ALTER TABLE pandokos.role_assignment FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.membership_role FORCE ROW LEVEL SECURITY;
