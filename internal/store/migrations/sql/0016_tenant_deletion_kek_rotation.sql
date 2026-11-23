-- 0016_tenant_deletion_kek_rotation: the states of key-encryption key versions and their
-- activation and retirement, the re-wrap grant of the worker, the guard that closes a tenant to
-- new rows once its deletion has begun, the deletion request and its progress, the erasure steps
-- run by the worker, and retention of the records kept for a deleted tenant (ADR 0067, ADR 0068).
-- Runs in one transaction as pandokos_owner. Every name is schema-qualified.

-- ---------------------------------------------------------------------------------------------
-- 1. Append-only, except for retention (DELETE, migration 0015) and erasure (UPDATE).
-- ---------------------------------------------------------------------------------------------

-- A row of an append-only table is removed only under pandokos.retention and rewritten only
-- under pandokos.erasure. Only functions that run as the owner set them around their own
-- statement: no runtime role holds DELETE or UPDATE on these tables, so the settings give a
-- runtime role nothing. TRUNCATE stays refused for everybody.
CREATE OR REPLACE FUNCTION pandokos.append_only() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'DELETE' AND pg_catalog.current_setting('pandokos.retention', true) = 'on' THEN
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE' AND pg_catalog.current_setting('pandokos.erasure', true) = 'on' THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
END
$$;

-- A started price version of a tenant is removed only with its tenant, under the same setting as
-- the rows of the append-only tables (one mechanism, 5.4 section 11).
CREATE OR REPLACE FUNCTION pandokos.tenant_price_version_guard() RETURNS trigger
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
    IF OLD.valid_from <= now() AND pg_catalog.current_setting('pandokos.retention', true) IS DISTINCT FROM 'on' THEN
        RAISE EXCEPTION 'a price version that has started cannot be removed' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN OLD;
END
$$;

-- A definer function runs as the owner, for whom the guards of section 3, the policies of section 5
-- and append_only admit a row under the two settings, which any role may set. Every definer
-- function therefore starts with both off (definer_settings_off, the first declaration of its
-- body) and, unless it sets one itself, puts back its caller's values before it returns
-- (definer_settings_restore): what a SET clause on the function would do, which the owner cannot
-- attach for a parameter that no module defines. Section 7 gives the functions of the earlier
-- migrations the same start; lint rule L-22 asks it of every definer function.
CREATE FUNCTION pandokos.definer_settings_off() RETURNS text[]
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v text[] := ARRAY[COALESCE(pg_catalog.current_setting('pandokos.erasure', true), ''),
                      COALESCE(pg_catalog.current_setting('pandokos.retention', true), '')];
BEGIN
    PERFORM pg_catalog.set_config('pandokos.erasure', 'off', true);
    PERFORM pg_catalog.set_config('pandokos.retention', 'off', true);
    RETURN v;
END
$$;

CREATE FUNCTION pandokos.definer_settings_restore(p_saved text[]) RETURNS void
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    PERFORM pg_catalog.set_config('pandokos.erasure', p_saved[1], true);
    PERFORM pg_catalog.set_config('pandokos.retention', p_saved[2], true);
END
$$;

REVOKE ALL ON FUNCTION pandokos.definer_settings_off() FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.definer_settings_restore(text[]) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------
-- 2. Key-encryption key versions: one is active, a replaced one is retiring until no data key is
--    wrapped by it, then retired (VLT-10). The key itself is never here.
-- ---------------------------------------------------------------------------------------------

ALTER TABLE pandokos.kek_version
    ADD COLUMN state        text        NOT NULL DEFAULT 'retiring',
    ADD COLUMN activated_at timestamptz,
    ADD COLUMN retired_at   timestamptz;

-- The version registered last was the one in use; any other row is a version that is retired as
-- soon as nothing is wrapped by it.
UPDATE pandokos.kek_version v
   SET state = 'active', activated_at = v.created_at
 WHERE v.id = (SELECT w.id FROM pandokos.kek_version w ORDER BY w.created_at DESC, w.id DESC LIMIT 1);
UPDATE pandokos.kek_version SET activated_at = created_at WHERE activated_at IS NULL;

ALTER TABLE pandokos.kek_version
    ALTER COLUMN activated_at SET NOT NULL,
    ALTER COLUMN activated_at SET DEFAULT now(),
    ADD CONSTRAINT kek_version_state_valid CHECK (state IN ('active', 'retiring', 'retired')),
    ADD CONSTRAINT kek_version_retired_at CHECK ((state = 'retired') = (retired_at IS NOT NULL));
CREATE UNIQUE INDEX kek_version_active_key ON pandokos.kek_version ((true)) WHERE state = 'active';

-- Versions are registered by kek_activate only.
REVOKE INSERT (id, provider) ON pandokos.kek_version FROM pandokos_admin;

-- The owner writes the lifecycle actions of this migration in the platform log, inside its
-- functions.
CREATE POLICY platform_audit_event_lifecycle_insert ON pandokos.platform_audit_event
    FOR INSERT TO pandokos_owner
    WITH CHECK (action IN ('kek.activate', 'kek.retire', 'tenant.delete_start', 'tenant.delete_complete',
                           'tenant.records_retention'));

-- In platform scope, at the start of serve: makes p_id the active version. An unknown version is
-- registered and replaces the active one, which becomes retiring; the active version is left as
-- it is; a retiring or retired version is refused (a replaced key never comes back). Returns
-- 'activated' or 'unchanged'.
CREATE FUNCTION pandokos.kek_activate(p_id text, p_provider text) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    v_state    text;
    v_provider text;
    v_previous text;
BEGIN
    IF NOT pandokos.platform_scope() THEN
        RAISE EXCEPTION 'kek_activate needs the platform scope' USING ERRCODE = 'insufficient_privilege';
    END IF;
    LOCK TABLE pandokos.kek_version IN SHARE ROW EXCLUSIVE MODE;
    SELECT v.state, v.provider INTO v_state, v_provider FROM pandokos.kek_version v WHERE v.id = p_id;
    IF FOUND THEN
        IF v_provider <> p_provider THEN
            RAISE EXCEPTION 'key-encryption key version % belongs to provider %', p_id, v_provider
                USING ERRCODE = 'object_not_in_prerequisite_state', CONSTRAINT = 'kek_version_provider';
        END IF;
        IF v_state <> 'active' THEN
            RAISE EXCEPTION 'key-encryption key version % is % and cannot become active again', p_id, v_state
                USING ERRCODE = 'object_not_in_prerequisite_state', CONSTRAINT = 'kek_version_replaced';
        END IF;
        PERFORM pandokos.definer_settings_restore(v_settings);
        RETURN 'unchanged';
    END IF;
    UPDATE pandokos.kek_version SET state = 'retiring' WHERE state = 'active' RETURNING id INTO v_previous;
    INSERT INTO pandokos.kek_version (id, provider, state) VALUES (p_id, p_provider, 'active');
    INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, resource_id, outcome, detail)
    VALUES ('host', 'serve', 'kek.activate', 'kek_version', p_id, 'success',
            pg_catalog.jsonb_build_object('provider', p_provider, 'previous', COALESCE(v_previous, '')));
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN 'activated';
END
$$;

-- In platform scope, by the worker: retires every retiring version that wraps no data key. The
-- test is the foreign key of tenant_data_key, which sees the rows of every tenant and waits for
-- an insert in flight: the row is deleted and, when nothing refers to it, written again as
-- retired. Returns the number of versions retired.
CREATE FUNCTION pandokos.kek_retire() RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    r          pandokos.kek_version%ROWTYPE;
    n          integer := 0;
BEGIN
    IF NOT pandokos.platform_scope() THEN
        RAISE EXCEPTION 'kek_retire needs the platform scope' USING ERRCODE = 'insufficient_privilege';
    END IF;
    FOR r IN SELECT * FROM pandokos.kek_version v WHERE v.state = 'retiring' ORDER BY v.id FOR UPDATE LOOP
        BEGIN
            DELETE FROM pandokos.kek_version v WHERE v.id = r.id;
            INSERT INTO pandokos.kek_version (id, provider, created_at, state, activated_at, retired_at)
            VALUES (r.id, r.provider, r.created_at, 'retired', r.activated_at, pg_catalog.now());
        EXCEPTION WHEN foreign_key_violation THEN
            CONTINUE;
        END;
        INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, resource_id, outcome, detail)
        VALUES ('platform', 'rewrap', 'kek.retire', 'kek_version', r.id, 'success',
                pg_catalog.jsonb_build_object('provider', r.provider));
        n := n + 1;
    END LOOP;
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN n;
END
$$;

REVOKE ALL ON FUNCTION pandokos.kek_activate(text, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.kek_retire() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.kek_activate(text, text) TO pandokos_admin;
GRANT EXECUTE ON FUNCTION pandokos.kek_retire() TO pandokos_worker;

-- A data key is created, and re-wrapped, under the active version only: an instance whose
-- configuration still names a replaced key fails closed.
CREATE FUNCTION pandokos.tenant_data_key_kek_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pandokos.kek_version v WHERE v.id = NEW.kek_version AND v.state = 'active') THEN
        RAISE EXCEPTION 'a data key is wrapped by the active key-encryption key version only'
            USING ERRCODE = 'restrict_violation', CONSTRAINT = 'tenant_data_key_kek_active';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER tenant_data_key_kek_guard BEFORE INSERT OR UPDATE OF kek_version ON pandokos.tenant_data_key
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_data_key_kek_guard();

-- The worker re-wraps data keys in the session of their tenant (ADR 0008).
GRANT SELECT ON pandokos.tenant_data_key TO pandokos_worker;
GRANT UPDATE (kek_version, wrapped_key) ON pandokos.tenant_data_key TO pandokos_worker;

-- ---------------------------------------------------------------------------------------------
-- 3. A tenant that is being deleted, or is deleted, takes no new row. Only the erasure functions
--    of this migration, which run as the owner and set pandokos.erasure, write rows for it.
-- ---------------------------------------------------------------------------------------------

-- The tenants that take no new row (global table): a tenant enters it when its deletion begins
-- and never leaves. It holds identifiers only. A runtime role reads the row of the tenant of its
-- session, or every row in platform scope; only the erasure function writes it.
CREATE TABLE pandokos.tenant_closed (
    tenant_id uuid        NOT NULL,
    closed_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tenant_closed_pkey PRIMARY KEY (tenant_id),
    CONSTRAINT tenant_closed_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id)
);

ALTER TABLE pandokos.tenant_closed ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.tenant_closed FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_closed_owner ON pandokos.tenant_closed
    FOR ALL TO pandokos_owner USING (true) WITH CHECK (true);
CREATE POLICY tenant_closed_read ON pandokos.tenant_closed
    FOR SELECT TO pandokos_gateway, pandokos_admin, pandokos_worker
    USING (tenant_id = (SELECT pandokos.current_tenant_id()) OR (SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.tenant_closed TO pandokos_gateway, pandokos_admin, pandokos_worker;

-- The owner reads and changes the row of a tenant inside the erasure functions only.
CREATE POLICY tenant_erasure_select ON pandokos.tenant
    FOR SELECT TO pandokos_owner
    USING (pg_catalog.current_setting('pandokos.erasure', true) = 'on');
CREATE POLICY tenant_erasure_update ON pandokos.tenant
    FOR UPDATE TO pandokos_owner
    USING (pg_catalog.current_setting('pandokos.erasure', true) = 'on')
    WITH CHECK (pg_catalog.current_setting('pandokos.erasure', true) = 'on');

-- For the tables whose policy binds every row to the tenant of the session: one look at that
-- tenant per statement. The setting lets a row through only for the owner, that is, inside a
-- function of this migration: a runtime role that sets it gains nothing, and every definer function
-- starts with both settings off (section 7), so setting one before calling it gains nothing either.
CREATE FUNCTION pandokos.tenant_open() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t uuid := pandokos.current_tenant_id();
BEGIN
    IF t IS NULL THEN
        RETURN NULL;
    END IF;
    IF current_user = 'pandokos_owner' AND pg_catalog.current_setting('pandokos.erasure', true) = 'on' THEN
        RETURN NULL;
    END IF;
    IF EXISTS (SELECT 1 FROM pandokos.tenant_closed x WHERE x.tenant_id = t) THEN
        RAISE EXCEPTION 'the tenant is being deleted or is deleted' USING ERRCODE = 'restrict_violation', CONSTRAINT = 'tenant_open';
    END IF;
    RETURN NULL;
END
$$;

-- For the global tables that name a tenant in a row written in platform scope: one look per row.
CREATE FUNCTION pandokos.tenant_open_row() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t uuid := (pg_catalog.to_jsonb(NEW) ->> 'tenant_id')::uuid;
BEGIN
    IF t IS NULL THEN
        RETURN NEW;
    END IF;
    IF current_user = 'pandokos_owner' AND pg_catalog.current_setting('pandokos.erasure', true) = 'on' THEN
        RETURN NEW;
    END IF;
    IF EXISTS (SELECT 1 FROM pandokos.tenant_closed x WHERE x.tenant_id = t) THEN
        RAISE EXCEPTION 'the tenant is being deleted or is deleted' USING ERRCODE = 'restrict_violation', CONSTRAINT = 'tenant_open';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER audit_chain_tenant_open BEFORE INSERT ON pandokos.audit_chain
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER audit_event_tenant_open BEFORE INSERT ON pandokos.audit_event
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER idempotency_key_tenant_open BEFORE INSERT ON pandokos.idempotency_key
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER limit_event_tenant_open BEFORE INSERT ON pandokos.limit_event
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER limit_rule_tenant_open BEFORE INSERT ON pandokos.limit_rule
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER membership_tenant_open BEFORE INSERT ON pandokos.membership
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER membership_role_tenant_open BEFORE INSERT ON pandokos.membership_role
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER metadata_key_policy_tenant_open BEFORE INSERT ON pandokos.metadata_key_policy
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER provider_credential_tenant_open BEFORE INSERT ON pandokos.provider_credential
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER role_assignment_tenant_open BEFORE INSERT ON pandokos.role_assignment
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER service_account_tenant_open BEFORE INSERT ON pandokos.service_account
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER subject_mapping_tenant_open BEFORE INSERT ON pandokos.subject_mapping
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER tenant_data_key_tenant_open BEFORE INSERT ON pandokos.tenant_data_key
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER tenant_model_restriction_tenant_open BEFORE INSERT ON pandokos.tenant_model_restriction
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER tenant_price_rate_tenant_open BEFORE INSERT ON pandokos.tenant_price_rate
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER tenant_price_version_tenant_open BEFORE INSERT ON pandokos.tenant_price_version
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER tenant_role_tenant_open BEFORE INSERT ON pandokos.tenant_role
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER trusted_issuer_tenant_open BEFORE INSERT ON pandokos.trusted_issuer
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER usage_record_tenant_open BEFORE INSERT ON pandokos.usage_record
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER usage_rollup_tenant_open BEFORE INSERT ON pandokos.usage_rollup
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER usage_rollup_state_tenant_open BEFORE INSERT ON pandokos.usage_rollup_state
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
CREATE TRIGGER virtual_key_tenant_open BEFORE INSERT ON pandokos.virtual_key
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();

CREATE TRIGGER app_user_tenant_open BEFORE INSERT ON pandokos.app_user
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_open_row();
CREATE TRIGGER model_tenant_grant_tenant_open BEFORE INSERT ON pandokos.model_tenant_grant
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_open_row();
CREATE TRIGGER platform_issuer_tenant_tenant_open BEFORE INSERT ON pandokos.platform_issuer_tenant
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_open_row();
CREATE TRIGGER platform_limit_rule_tenant_open BEFORE INSERT ON pandokos.platform_limit_rule
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_open_row();
CREATE TRIGGER support_action_tenant_open BEFORE INSERT ON pandokos.support_action
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_open_row();

-- ---------------------------------------------------------------------------------------------
-- 4. The deletion of a tenant: the request and its progress (global table). A row is created by
--    the admin role in platform scope when a deletion is requested, removed by it while the
--    deletion is still scheduled (cancel), and from then on written by the erasure functions only.
-- ---------------------------------------------------------------------------------------------

CREATE TABLE pandokos.tenant_deletion (
    tenant_id         uuid        NOT NULL,
    state             text        NOT NULL DEFAULT 'scheduled',
    step              text        NOT NULL DEFAULT 'begin',
    reason            text        NOT NULL,
    requested_by_type text        NOT NULL,
    requested_by_id   text        NOT NULL DEFAULT '',
    requested_by_name text        NOT NULL DEFAULT '',
    request_id        text        NOT NULL DEFAULT '',
    requested_at      timestamptz NOT NULL DEFAULT now(),
    not_before        timestamptz NOT NULL,
    started_at        timestamptz,
    settle_at         timestamptz,
    completed_at      timestamptz,
    passes            integer     NOT NULL DEFAULT 0,
    salt              bytea,
    usage_started_at  timestamptz,
    usage_id          uuid,
    kek_versions      text[]      NOT NULL DEFAULT '{}',
    removed           jsonb       NOT NULL DEFAULT '{}',
    audit             jsonb       NOT NULL DEFAULT '{}',
    completion_seq    bigint,
    CONSTRAINT tenant_deletion_pkey PRIMARY KEY (tenant_id),
    CONSTRAINT tenant_deletion_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT tenant_deletion_state_valid CHECK (state IN ('scheduled', 'running', 'completed')),
    CONSTRAINT tenant_deletion_step_valid CHECK (step IN ('begin', 'access', 'secrets', 'configuration', 'usage', 'audit', 'complete', 'done')),
    CONSTRAINT tenant_deletion_state_step CHECK ((state = 'scheduled') = (step = 'begin') AND (state = 'completed') = (step = 'done')),
    CONSTRAINT tenant_deletion_reason_length CHECK (char_length(reason) BETWEEN 1 AND 500),
    CONSTRAINT tenant_deletion_requested_by_type_valid CHECK (requested_by_type IN ('platform', 'user')),
    CONSTRAINT tenant_deletion_times CHECK ((state = 'scheduled') = (started_at IS NULL) AND (state = 'completed') = (completed_at IS NOT NULL)),
    CONSTRAINT tenant_deletion_salt CHECK ((state = 'running') = (salt IS NOT NULL) AND (salt IS NULL OR octet_length(salt) = 32)),
    CONSTRAINT tenant_deletion_objects CHECK (jsonb_typeof(removed) = 'object' AND jsonb_typeof(audit) = 'object')
);
CREATE INDEX tenant_deletion_open_idx ON pandokos.tenant_deletion (not_before) WHERE state <> 'completed';

ALTER TABLE pandokos.tenant_deletion ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.tenant_deletion FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_deletion_owner ON pandokos.tenant_deletion
    FOR ALL TO pandokos_owner USING (true) WITH CHECK (true);
CREATE POLICY tenant_deletion_platform_select ON pandokos.tenant_deletion
    FOR SELECT TO pandokos_admin, pandokos_worker USING ((SELECT pandokos.platform_scope()));
CREATE POLICY tenant_deletion_platform_insert ON pandokos.tenant_deletion
    FOR INSERT TO pandokos_admin WITH CHECK ((SELECT pandokos.platform_scope()) AND state = 'scheduled');
CREATE POLICY tenant_deletion_platform_delete ON pandokos.tenant_deletion
    FOR DELETE TO pandokos_admin USING ((SELECT pandokos.platform_scope()) AND state = 'scheduled');

-- No runtime role reads the salt of the pseudonyms or the cursor of the ledger.
GRANT SELECT (tenant_id, state, step, reason, requested_by_type, requested_by_id, requested_by_name, request_id,
              requested_at, not_before, started_at, settle_at, completed_at, passes, kek_versions, removed, audit, completion_seq)
    ON pandokos.tenant_deletion TO pandokos_admin;
GRANT SELECT (tenant_id, state, step, request_id, not_before, started_at, kek_versions)
    ON pandokos.tenant_deletion TO pandokos_worker;
GRANT INSERT (tenant_id, reason, requested_by_type, requested_by_id, requested_by_name, request_id, not_before)
    ON pandokos.tenant_deletion TO pandokos_admin;
GRANT DELETE ON pandokos.tenant_deletion TO pandokos_admin;

-- A tenant with a deletion request is not reactivated: the request is cancelled first.
CREATE FUNCTION pandokos.tenant_deletion_holds() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
BEGIN
    IF NEW.state = 'active' AND OLD.state = 'suspended'
       AND EXISTS (SELECT 1 FROM pandokos.tenant_deletion d WHERE d.tenant_id = NEW.id) THEN
        RAISE EXCEPTION 'the deletion of the tenant is requested' USING ERRCODE = 'restrict_violation', CONSTRAINT = 'tenant_deletion_holds';
    END IF;
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN NEW;
END
$$;
REVOKE ALL ON FUNCTION pandokos.tenant_deletion_holds() FROM PUBLIC;
CREATE TRIGGER tenant_deletion_holds BEFORE UPDATE OF state ON pandokos.tenant
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_deletion_holds();

-- A deletion is requested for a suspended tenant only (the request suspends it in the same
-- transaction).
CREATE FUNCTION pandokos.tenant_deletion_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pandokos.tenant x WHERE x.id = NEW.tenant_id AND x.state = 'suspended') THEN
        RAISE EXCEPTION 'a deletion is requested for a suspended tenant' USING ERRCODE = 'restrict_violation', CONSTRAINT = 'tenant_deletion_suspended';
    END IF;
    NEW.state := 'scheduled';
    NEW.step := 'begin';
    NEW.requested_at := pg_catalog.now();
    RETURN NEW;
END
$$;
REVOKE ALL ON FUNCTION pandokos.tenant_deletion_guard() FROM PUBLIC;
CREATE TRIGGER tenant_deletion_guard BEFORE INSERT ON pandokos.tenant_deletion
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_deletion_guard();

-- ---------------------------------------------------------------------------------------------
-- 5. Policies that let the erasure functions, and nothing else, remove a tenant's rows of the
--    global tables: the owner, in the session of that tenant, under pandokos.erasure.
-- ---------------------------------------------------------------------------------------------

CREATE POLICY model_tenant_grant_erasure ON pandokos.model_tenant_grant FOR ALL TO pandokos_owner
    USING (tenant_id = (SELECT pandokos.current_tenant_id()) AND pg_catalog.current_setting('pandokos.erasure', true) = 'on');
CREATE POLICY platform_limit_rule_erasure ON pandokos.platform_limit_rule FOR ALL TO pandokos_owner
    USING (tenant_id = (SELECT pandokos.current_tenant_id()) AND pg_catalog.current_setting('pandokos.erasure', true) = 'on');
CREATE POLICY platform_issuer_tenant_erasure ON pandokos.platform_issuer_tenant FOR DELETE TO pandokos_owner
    USING (tenant_id = (SELECT pandokos.current_tenant_id()) AND pg_catalog.current_setting('pandokos.erasure', true) = 'on');
CREATE POLICY support_action_erasure ON pandokos.support_action FOR ALL TO pandokos_owner
    USING (tenant_id = (SELECT pandokos.current_tenant_id()) AND pg_catalog.current_setting('pandokos.erasure', true) = 'on');
CREATE POLICY idempotency_key_erasure ON pandokos.idempotency_key FOR ALL TO pandokos_owner
    USING (tenant_id = (SELECT pandokos.current_tenant_id()) AND pg_catalog.current_setting('pandokos.erasure', true) = 'on');
CREATE POLICY app_user_erasure ON pandokos.app_user FOR DELETE TO pandokos_owner
    USING (tenant_id = (SELECT pandokos.current_tenant_id()) AND pg_catalog.current_setting('pandokos.erasure', true) = 'on');
CREATE POLICY local_account_erasure ON pandokos.local_account FOR DELETE TO pandokos_owner
    USING (pg_catalog.current_setting('pandokos.erasure', true) = 'on'
           AND EXISTS (SELECT 1 FROM pandokos.app_user u WHERE u.id = user_id AND u.tenant_id = (SELECT pandokos.current_tenant_id())));
CREATE POLICY user_identity_erasure ON pandokos.user_identity FOR ALL TO pandokos_owner
    USING (pg_catalog.current_setting('pandokos.erasure', true) = 'on'
           AND EXISTS (SELECT 1 FROM pandokos.app_user u WHERE u.id = user_id AND u.tenant_id = (SELECT pandokos.current_tenant_id())));
CREATE POLICY user_session_erasure ON pandokos.user_session FOR ALL TO pandokos_owner
    USING (pg_catalog.current_setting('pandokos.erasure', true) = 'on'
           AND EXISTS (SELECT 1 FROM pandokos.app_user u WHERE u.id = user_id AND u.tenant_id = (SELECT pandokos.current_tenant_id())));

-- ---------------------------------------------------------------------------------------------
-- 6. The erasure functions. The worker calls tenant_erasure_step in the session of the tenant
--    until it answers 'done'; each call is one transaction and one step, or one batch of a step.
--    The functions below it are the owner's alone.
-- ---------------------------------------------------------------------------------------------

-- A pseudonym of an identifier: the text of a UUID made of the first 16 bytes of
-- SHA-256(salt, value). The salt is destroyed when the deletion completes, so a pseudonym cannot
-- be recomputed from a guessed value. The empty text stays empty.
CREATE FUNCTION pandokos.pseudonym(p_salt bytea, p_value text) RETURNS text
    LANGUAGE sql IMMUTABLE
    SET search_path = pg_catalog, pg_temp
    RETURN CASE WHEN p_value = '' THEN ''
                ELSE pg_catalog.encode(pg_catalog.substring(pg_catalog.sha256(p_salt || pg_catalog.convert_to(p_value, 'UTF8')), 1, 16), 'hex')::uuid::text END;

-- The sum, key by key, of two objects of counts.
CREATE FUNCTION pandokos.jsonb_counts_add(a jsonb, b jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path = pg_catalog, pg_temp
    RETURN (SELECT COALESCE(pg_catalog.jsonb_object_agg(s.key, s.n), '{}'::jsonb)
              FROM (SELECT e.key, pg_catalog.sum(e.value::bigint) AS n
                      FROM (SELECT * FROM pg_catalog.jsonb_each_text(a) UNION ALL SELECT * FROM pg_catalog.jsonb_each_text(b)) e
                     GROUP BY e.key) s);

-- How many rows each table still holds for the tenant of the session, for the tables that a
-- deletion empties. Reads the directories and the global tables as the owner.
CREATE FUNCTION pandokos.tenant_residue() RETURNS jsonb
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t   uuid := pandokos.current_tenant_id();
    out jsonb := '{}';
    n   bigint;
    tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['limit_rule', 'subject_mapping', 'trusted_issuer', 'issuer_directory', 'platform_issuer_tenant',
                               'virtual_key', 'virtual_key_directory', 'role_assignment', 'idempotency_key', 'support_action',
                               'membership_role', 'membership', 'membership_directory', 'app_user', 'service_account',
                               'tenant_role', 'tenant_data_key', 'provider_credential', 'platform_limit_rule',
                               'model_tenant_grant', 'tenant_model_restriction', 'metadata_key_policy', 'tenant_price_rate',
                               'tenant_price_version', 'limit_event', 'usage_rollup', 'usage_rollup_state'] LOOP
        EXECUTE pg_catalog.format('SELECT pg_catalog.count(*) FROM pandokos.%I x WHERE x.tenant_id = $1', tab) INTO n USING t;
        IF n > 0 THEN
            out := out || pg_catalog.jsonb_build_object(tab, n);
        END IF;
    END LOOP;
    RETURN out;
END
$$;

-- Removal of access: every identity, key, mapping, registration, membership, session and role of
-- the tenant, in the order the foreign keys impose. Returns the rows removed by table.
CREATE FUNCTION pandokos.tenant_erasure_access() RETURNS jsonb
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t   uuid := pandokos.current_tenant_id();
    out jsonb := '{}';
    n   bigint;
BEGIN
    DELETE FROM pandokos.limit_rule;                                        GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('limit_rule', n);
    DELETE FROM pandokos.subject_mapping;                                   GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('subject_mapping', n);
    DELETE FROM pandokos.trusted_issuer;                                    GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('trusted_issuer', n);
    DELETE FROM pandokos.issuer_directory d WHERE d.tenant_id = t;
    DELETE FROM pandokos.platform_issuer_tenant p WHERE p.tenant_id = t;    GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('platform_issuer_tenant', n);
    DELETE FROM pandokos.virtual_key;                                       GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('virtual_key', n);
    DELETE FROM pandokos.virtual_key_directory d WHERE d.tenant_id = t;
    DELETE FROM pandokos.role_assignment;                                   GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('role_assignment', n);
    DELETE FROM pandokos.idempotency_key k WHERE k.tenant_id = t;           GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('idempotency_key', n);
    DELETE FROM pandokos.support_action a WHERE a.tenant_id = t;            GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('support_action', n);
    DELETE FROM pandokos.membership_role;                                   GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('membership_role', n);
    DELETE FROM pandokos.membership;                                        GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('membership', n);
    DELETE FROM pandokos.membership_directory d WHERE d.tenant_id = t;
    DELETE FROM pandokos.user_session s USING pandokos.app_user u WHERE u.id = s.user_id AND u.tenant_id = t;
    DELETE FROM pandokos.local_account a USING pandokos.app_user u WHERE u.id = a.user_id AND u.tenant_id = t;
    DELETE FROM pandokos.user_identity i USING pandokos.app_user u WHERE u.id = i.user_id AND u.tenant_id = t;
    DELETE FROM pandokos.app_user u WHERE u.tenant_id = t;                  GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('app_user', n);
    DELETE FROM pandokos.service_account;                                   GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('service_account', n);
    DELETE FROM pandokos.tenant_role;                                       GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('tenant_role', n);
    RETURN out;
END
$$;

-- Cryptographic erasure: the data keys of the tenant, then the sealed secrets they opened.
-- Returns the key-encryption key versions that wrapped a data key, and the rows removed.
CREATE FUNCTION pandokos.tenant_erasure_secrets(OUT kek_versions text[], OUT removed jsonb)
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    n bigint;
BEGIN
    SELECT COALESCE(pg_catalog.array_agg(DISTINCT k.kek_version ORDER BY k.kek_version), '{}') INTO kek_versions
      FROM pandokos.tenant_data_key k;
    DELETE FROM pandokos.tenant_data_key;     GET DIAGNOSTICS n = ROW_COUNT; removed := pg_catalog.jsonb_build_object('tenant_data_key', n);
    DELETE FROM pandokos.provider_credential; GET DIAGNOSTICS n = ROW_COUNT; removed := removed || pg_catalog.jsonb_build_object('provider_credential', n);
END
$$;

-- Removal of configuration and of derived data. The counts of limit events and of rollup rows
-- are not returned: they are figures of the tenant's usage (ADR 0054).
CREATE FUNCTION pandokos.tenant_erasure_configuration() RETURNS jsonb
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t   uuid := pandokos.current_tenant_id();
    out jsonb := '{}';
    n   bigint;
BEGIN
    DELETE FROM pandokos.platform_limit_rule r WHERE r.tenant_id = t;       GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('platform_limit_rule', n);
    DELETE FROM pandokos.model_tenant_grant g WHERE g.tenant_id = t;        GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('model_tenant_grant', n);
    DELETE FROM pandokos.tenant_model_restriction;                          GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('tenant_model_restriction', n);
    DELETE FROM pandokos.metadata_key_policy;                               GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('metadata_key_policy', n);
    PERFORM pg_catalog.set_config('pandokos.retention', 'on', true);
    DELETE FROM pandokos.tenant_price_version;                              GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('tenant_price_version', n);
    DELETE FROM pandokos.limit_event;
    PERFORM pg_catalog.set_config('pandokos.retention', 'off', true);
    DELETE FROM pandokos.usage_rollup;
    DELETE FROM pandokos.usage_rollup_state;
    RETURN out;
END
$$;

-- One batch of the ledger: the principal and the key of each record become pseudonyms and the
-- caller's metadata is emptied. Returns the last record of the batch, or nothing when no record
-- is left after the cursor.
CREATE FUNCTION pandokos.tenant_erasure_usage(p_salt bytea, p_started_at timestamptz, p_id uuid, p_batch integer,
                                             OUT last_started_at timestamptz, OUT last_id uuid)
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    WITH b AS (
        SELECT u.started_at, u.id
          FROM pandokos.usage_record u
         WHERE p_started_at IS NULL OR (u.started_at, u.id) > (p_started_at, p_id)
         ORDER BY u.started_at, u.id
         LIMIT p_batch
    ), w AS (
        UPDATE pandokos.usage_record u
           SET principal_id = pandokos.pseudonym(p_salt, u.principal_id),
               key_id       = pandokos.pseudonym(p_salt, u.key_id),
               metadata     = '{}'
          FROM b
         WHERE u.started_at = b.started_at AND u.id = b.id
        RETURNING u.started_at, u.id
    )
    SELECT w.started_at, w.id INTO last_started_at, last_id FROM w ORDER BY w.started_at DESC, w.id DESC LIMIT 1;
END
$$;

-- The audit log of the tenant is closed with a last row, then every row kept is pseudonymized
-- (the actor, the key and the resource become pseudonyms; the actor's name, the reason and the
-- detail are emptied) and the chain is sealed again from its anchor with the encoding of ADR
-- 0065. Returns the anchor, the last sequence number, the rows, and the head before and after.
CREATE FUNCTION pandokos.tenant_erasure_audit(p_salt bytea) RETURNS jsonb
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t        uuid := pandokos.current_tenant_id();
    r        pandokos.audit_event%ROWTYPE;
    v_anchor bigint;
    v_prev   bytea;
    v_before bytea;
    v_last   bigint;
    v_hash   bytea;
    v_rows   bigint := 0;
    v_actor  text;
    v_key    text;
    v_res    text;
BEGIN
    INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_name, action, resource_type, resource_id, outcome)
    VALUES (t, 'platform', 'deletion', 'tenant.delete', 'tenant', t::text, 'success');
    SELECT c.anchor_seq, c.anchor_hash, c.seq, c.hash INTO v_anchor, v_prev, v_last, v_before
      FROM pandokos.audit_chain c WHERE c.tenant_id = t FOR UPDATE;
    FOR r IN SELECT * FROM pandokos.audit_event e WHERE e.seq >= v_anchor ORDER BY e.seq LOOP
        v_actor := pandokos.pseudonym(p_salt, r.actor_id);
        v_key := pandokos.pseudonym(p_salt, r.key_id);
        v_res := CASE WHEN r.resource_id = t::text THEN r.resource_id ELSE pandokos.pseudonym(p_salt, r.resource_id) END;
        v_hash := pandokos.audit_row_hash('tenant:' || t::text, r.seq, r.id, r.occurred_at, r.actor_type, v_actor, '', v_key,
                                          r.action, r.resource_type, v_res, r.outcome, r.request_id, '',
                                          r.support_action_id, '{}'::jsonb, v_prev);
        UPDATE pandokos.audit_event e
           SET actor_id = v_actor, actor_name = '', key_id = v_key, resource_id = v_res, reason = '', detail = '{}',
               prev_hash = v_prev, hash = v_hash
         WHERE e.seq = r.seq;
        v_prev := v_hash;
        v_rows := v_rows + 1;
    END LOOP;
    UPDATE pandokos.audit_chain SET hash = v_prev WHERE tenant_id = t;
    RETURN pg_catalog.jsonb_build_object('anchor_seq', v_anchor, 'last_seq', v_last, 'rows', v_rows,
                                         'original_head', pg_catalog.encode(v_before, 'hex'),
                                         'resealed_head', pg_catalog.encode(v_prev, 'hex'));
END
$$;

-- The state of a tenant changes in platform scope (migration 0011): the scope is declared for the
-- one statement and put back.
CREATE FUNCTION pandokos.tenant_erasure_state(p_tenant uuid, p_state text) RETURNS void
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    prev text := pg_catalog.current_setting('app.scope', true);
BEGIN
    PERFORM pg_catalog.set_config('app.scope', 'platform', true);
    UPDATE pandokos.tenant SET state = p_state WHERE id = p_tenant;
    PERFORM pg_catalog.set_config('app.scope', COALESCE(prev, ''), true);
END
$$;

REVOKE ALL ON FUNCTION pandokos.tenant_erasure_state(uuid, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.pseudonym(bytea, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.jsonb_counts_add(jsonb, jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.tenant_residue() FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.tenant_erasure_access() FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.tenant_erasure_secrets() FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.tenant_erasure_configuration() FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.tenant_erasure_usage(bytea, timestamptz, uuid, integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.tenant_erasure_audit(bytea) FROM PUBLIC;

-- In the session of a tenant whose deletion is requested: runs the step the deletion is at and
-- answers the step that comes next: 'scheduled' (not due yet: nothing was done), 'access',
-- 'secrets', 'configuration', 'usage' (again while records are left), 'audit', 'complete',
-- 'settling' (the completion waits until two minutes after the start, or after a second pass, for
-- transactions that began before it) and 'done'. Every call is idempotent: a step that ran is not run again.
-- p_batch is the number of ledger records of one call of the step 'usage' (100 to 50000).
CREATE FUNCTION pandokos.tenant_erasure_step(p_batch integer) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    t       uuid := pandokos.current_tenant_id();
    d       pandokos.tenant_deletion%ROWTYPE;
    v_name  text;
    v_state text;
    v_out   jsonb;
    v_keks  text[];
    v_at    timestamptz;
    v_id    uuid;
    v_seq   bigint;
BEGIN
    IF t IS NULL THEN
        RAISE EXCEPTION 'tenant_erasure_step needs a tenant session' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF p_batch IS NULL OR p_batch NOT BETWEEN 100 AND 50000 THEN
        RAISE EXCEPTION 'invalid batch' USING ERRCODE = 'check_violation';
    END IF;
    SELECT * INTO d FROM pandokos.tenant_deletion x WHERE x.tenant_id = t FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'no deletion is requested for the tenant' USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;
    IF d.step = 'done' THEN
        RETURN 'done';
    END IF;
    PERFORM pg_catalog.set_config('pandokos.erasure', 'on', true);
    SELECT x.name, x.state INTO v_name, v_state FROM pandokos.tenant x WHERE x.id = t FOR NO KEY UPDATE;
    CASE d.step
    WHEN 'begin' THEN
        IF d.not_before > pg_catalog.now() THEN
            PERFORM pg_catalog.set_config('pandokos.erasure', 'off', true);
            RETURN 'scheduled';
        END IF;
        IF v_state <> 'suspended' THEN
            RAISE EXCEPTION 'the tenant is not suspended' USING ERRCODE = 'object_not_in_prerequisite_state';
        END IF;
        PERFORM pandokos.tenant_erasure_state(t, 'deleting');
        INSERT INTO pandokos.tenant_closed (tenant_id) VALUES (t);
        UPDATE pandokos.tenant_deletion
           SET state = 'running', step = 'access', started_at = pg_catalog.now(), settle_at = pg_catalog.now() + interval '2 minutes',
               salt = pg_catalog.uuid_send(pg_catalog.gen_random_uuid()) || pg_catalog.uuid_send(pg_catalog.gen_random_uuid())
         WHERE tenant_id = t;
        INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, resource_id, outcome, request_id, detail)
        VALUES ('platform', 'deletion', 'tenant.delete_start', 'tenant', t::text, 'success', d.request_id,
                pg_catalog.jsonb_build_object('name', v_name, 'requested_at', d.requested_at, 'not_before', d.not_before));
    WHEN 'access' THEN
        v_out := pandokos.tenant_erasure_access();
        UPDATE pandokos.tenant_deletion SET step = 'secrets', removed = pandokos.jsonb_counts_add(removed, v_out), passes = passes + 1 WHERE tenant_id = t;
    WHEN 'secrets' THEN
        SELECT s.kek_versions, s.removed INTO v_keks, v_out FROM pandokos.tenant_erasure_secrets() s;
        UPDATE pandokos.tenant_deletion
           SET step = 'configuration', removed = pandokos.jsonb_counts_add(removed, v_out),
               kek_versions = ARRAY(SELECT DISTINCT k FROM pg_catalog.unnest(kek_versions || v_keks) k ORDER BY 1)
         WHERE tenant_id = t;
    WHEN 'configuration' THEN
        v_out := pandokos.tenant_erasure_configuration();
        -- A second pass (rows left by a transaction that began before the deletion) does not do
        -- the ledger and the log again: they took no row after the start.
        UPDATE pandokos.tenant_deletion
           SET step = CASE WHEN d.audit = '{}'::jsonb THEN 'usage' ELSE 'complete' END,
               removed = pandokos.jsonb_counts_add(removed, v_out)
         WHERE tenant_id = t;
    WHEN 'usage' THEN
        SELECT u.last_started_at, u.last_id INTO v_at, v_id
          FROM pandokos.tenant_erasure_usage(d.salt, d.usage_started_at, d.usage_id, p_batch) u;
        IF v_id IS NULL THEN
            UPDATE pandokos.tenant_deletion SET step = 'audit' WHERE tenant_id = t;
        ELSE
            UPDATE pandokos.tenant_deletion SET usage_started_at = v_at, usage_id = v_id WHERE tenant_id = t;
        END IF;
    WHEN 'audit' THEN
        v_out := pandokos.tenant_erasure_audit(d.salt);
        UPDATE pandokos.tenant_deletion SET step = 'complete', audit = v_out WHERE tenant_id = t;
    WHEN 'complete' THEN
        IF pg_catalog.now() < d.settle_at THEN
            PERFORM pg_catalog.set_config('pandokos.erasure', 'off', true);
            RETURN 'settling';
        END IF;
        v_out := pandokos.tenant_residue();
        IF v_out <> '{}'::jsonb THEN
            -- A transaction that began before the deletion left rows: the removal runs again.
            UPDATE pandokos.tenant_deletion SET step = 'access', settle_at = pg_catalog.now() + interval '2 minutes' WHERE tenant_id = t;
            PERFORM pg_catalog.set_config('pandokos.erasure', 'off', true);
            RETURN 'access';
        END IF;
        INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, resource_id, outcome, request_id, detail)
        VALUES ('platform', 'deletion', 'tenant.delete_complete', 'tenant', t::text, 'success', d.request_id,
                pg_catalog.jsonb_build_object('name', v_name, 'requested_at', d.requested_at, 'started_at', d.started_at,
                                              'passes', d.passes, 'removed', d.removed, 'kek_versions', pg_catalog.to_jsonb(d.kek_versions),
                                              'usage', 'pseudonymized', 'audit', d.audit, 'residue', 0));
        -- The head of the platform's chain is locked by this transaction: its number is this row's.
        SELECT c.seq INTO v_seq FROM pandokos.platform_audit_chain c WHERE c.id = 1;
        PERFORM pandokos.tenant_erasure_state(t, 'deleted');
        UPDATE pandokos.tenant_deletion
           SET state = 'completed', step = 'done', completed_at = pg_catalog.now(), salt = NULL, completion_seq = v_seq
         WHERE tenant_id = t;
    END CASE;
    PERFORM pg_catalog.set_config('pandokos.erasure', 'off', true);
    RETURN (SELECT x.step FROM pandokos.tenant_deletion x WHERE x.tenant_id = t);
END
$$;

-- In the session of a deleted tenant: removes the records kept for it that are older than the
-- retention of the installation — its rows of the default partition of the ledger (the monthly
-- partitions are dropped whole by platform_retention) and the oldest rows of its log, whose
-- anchor moves; the closed chain takes no new row, so the removal is recorded in the platform
-- log, without a figure of usage.
CREATE FUNCTION pandokos.deleted_tenant_retention(p_usage_months integer, p_audit_months integer,
                                                 OUT usage_rows bigint, OUT audit_rows bigint)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    t       uuid := pandokos.current_tenant_id();
    v_month date := pg_catalog.date_trunc('month', pg_catalog.timezone('UTC', pg_catalog.now()))::date;
    v_cut   date;
    v_last  bigint;
    v_first bigint;
    v_hash  bytea;
BEGIN
    IF t IS NULL OR NOT EXISTS (SELECT 1 FROM pandokos.tenant_deletion x WHERE x.tenant_id = t AND x.state = 'completed') THEN
        RAISE EXCEPTION 'deleted_tenant_retention needs the session of a deleted tenant' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF p_usage_months IS NULL OR (p_usage_months <> 0 AND p_usage_months NOT BETWEEN 2 AND 120)
       OR p_audit_months IS NULL OR (p_audit_months <> 0 AND p_audit_months NOT BETWEEN 1 AND 1200) THEN
        RAISE EXCEPTION 'invalid retention' USING ERRCODE = 'check_violation';
    END IF;
    usage_rows := 0;
    audit_rows := 0;
    PERFORM pg_catalog.set_config('pandokos.retention', 'on', true);
    IF p_usage_months > 0 THEN
        v_cut := (v_month - pg_catalog.make_interval(months => p_usage_months))::date;
        DELETE FROM pandokos.usage_record_default u WHERE u.started_at < (v_cut::timestamp AT TIME ZONE 'UTC');
        GET DIAGNOSTICS usage_rows = ROW_COUNT;
    END IF;
    IF p_audit_months > 0 THEN
        v_cut := (v_month - pg_catalog.make_interval(months => p_audit_months))::date;
        SELECT c.anchor_seq INTO v_first FROM pandokos.audit_chain c WHERE c.tenant_id = t FOR UPDATE;
        SELECT pg_catalog.max(e.seq) INTO v_last FROM pandokos.audit_event e
         WHERE e.occurred_at < (v_cut::timestamp AT TIME ZONE 'UTC');
        IF v_last IS NOT NULL AND v_last >= v_first THEN
            SELECT e.hash INTO v_hash FROM pandokos.audit_event e WHERE e.seq = v_last;
            DELETE FROM pandokos.audit_event e WHERE e.seq <= v_last;
            GET DIAGNOSTICS audit_rows = ROW_COUNT;
            UPDATE pandokos.audit_chain SET anchor_seq = v_last + 1, anchor_hash = v_hash WHERE tenant_id = t;
        END IF;
    END IF;
    PERFORM pg_catalog.set_config('pandokos.retention', 'off', true);
    IF audit_rows > 0 THEN
        INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, resource_id, outcome, detail)
        VALUES ('platform', 'retention', 'tenant.records_retention', 'tenant', t::text, 'success',
                pg_catalog.jsonb_build_object('before', pg_catalog.to_char(v_cut, 'YYYY-MM-DD'), 'months', p_audit_months,
                                              'audit_events', audit_rows, 'first_seq', v_first, 'last_seq', v_last,
                                              'last_hash', pg_catalog.encode(v_hash, 'hex')));
    END IF;
END
$$;

REVOKE ALL ON FUNCTION pandokos.tenant_erasure_step(integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.deleted_tenant_retention(integer, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.tenant_erasure_step(integer) TO pandokos_worker;
GRANT EXECUTE ON FUNCTION pandokos.deleted_tenant_retention(integer, integer) TO pandokos_worker;

-- ---------------------------------------------------------------------------------------------
-- 7. The definer functions of the earlier migrations get the start of section 1: both settings
--    off, the caller's values back before they return; the four that set one themselves
--    (tenant_retention, platform_retention, and tenant_erasure_step and deleted_tenant_retention
--    above) leave both off. Their bodies are otherwise unchanged, except that the lookups of
--    migrations 0002, 0008 and 0013 become PL/pgSQL to have a first statement. The one exception
--    is audit_seal_tenant, the trigger that seals a row of a tenant's log: tenant_erasure_step
--    writes the closing row of a closed tenant through it, under the setting. It is reached only
--    through an INSERT into audit_event, whose statement guard has judged the caller first (lint
--    rule L-22).
-- ---------------------------------------------------------------------------------------------

CREATE OR REPLACE FUNCTION pandokos.virtual_key_lookup(p_key_id text) RETURNS uuid
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    v_tenant   uuid;
BEGIN
    v_tenant := (SELECT d.tenant_id FROM pandokos.virtual_key_directory d WHERE d.key_id = p_key_id);
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN v_tenant;
END
$$;

CREATE OR REPLACE FUNCTION pandokos.virtual_key_directory_add() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
BEGIN
    INSERT INTO pandokos.virtual_key_directory (key_id, tenant_id) VALUES (NEW.key_id, NEW.tenant_id);
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN NULL;
END
$$;

CREATE OR REPLACE FUNCTION pandokos.record_auth_failure(p_key_id text, p_reason text, p_request_id text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
BEGIN
    IF p_key_id !~ '^t[a-z2-7]{16}$' OR p_reason !~ '^[a-z_]{1,40}$' OR p_request_id !~ '^[0-9a-f]{32}$' THEN
        RAISE EXCEPTION 'invalid authentication failure record' USING ERRCODE = 'check_violation';
    END IF;
    INSERT INTO pandokos.audit_event (tenant_id, actor_type, key_id, action, resource_type, resource_id,
                                      outcome, request_id, detail)
    VALUES ((SELECT pandokos.current_tenant_id()), 'anonymous', p_key_id, 'auth.failure', 'virtual_key', p_key_id,
            'failure', p_request_id, pg_catalog.jsonb_build_object('reason', p_reason, 'listener', 'inference'));
    PERFORM pandokos.definer_settings_restore(v_settings);
END
$$;

CREATE OR REPLACE FUNCTION pandokos.ensure_usage_partitions(p_ahead integer, OUT created integer, OUT default_in_use boolean)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    v_month    date;
    v_name     text;
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
    PERFORM pandokos.definer_settings_restore(v_settings);
END
$$;

CREATE OR REPLACE FUNCTION pandokos.issuer_directory_sync() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        DELETE FROM pandokos.issuer_directory d WHERE d.issuer_id = OLD.id;
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        INSERT INTO pandokos.issuer_directory (issuer, audience, scope, tenant_id, issuer_id)
        VALUES (NEW.issuer, NEW.audience, TG_ARGV[0],
                CASE WHEN TG_ARGV[0] = 'tenant' THEN (pg_catalog.to_jsonb(NEW) ->> 'tenant_id')::uuid END, NEW.id);
    END IF;
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN NULL;
END
$$;

CREATE OR REPLACE FUNCTION pandokos.issuer_lookup(p_issuer text, p_audiences text[])
    RETURNS TABLE (scope text, tenant_id uuid, issuer_id uuid)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
BEGIN
    RETURN QUERY
    SELECT d.scope, d.tenant_id, d.issuer_id
      FROM pandokos.issuer_directory d
     WHERE d.issuer = p_issuer
       AND pg_catalog.cardinality(p_audiences) BETWEEN 1 AND 16
       AND d.audience = ANY (p_audiences);
    PERFORM pandokos.definer_settings_restore(v_settings);
END
$$;

CREATE OR REPLACE FUNCTION pandokos.platform_issuer_tenant_lookup(p_issuer_id uuid, p_claim_value text) RETURNS uuid
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    v_tenant   uuid;
BEGIN
    v_tenant := (SELECT t.tenant_id FROM pandokos.platform_issuer_tenant t
                  WHERE t.issuer_id = p_issuer_id AND t.claim_value = p_claim_value);
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN v_tenant;
END
$$;

CREATE OR REPLACE FUNCTION pandokos.platform_issuer_mapping_count(p_issuer_id uuid, p_tenant_ids uuid[]) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    prev       text := pg_catalog.current_setting('app.tenant_id', true);
    t          uuid;
    total      bigint := 0;
BEGIN
    IF NOT pandokos.platform_scope() THEN
        RAISE EXCEPTION 'platform_issuer_mapping_count needs the platform scope' USING ERRCODE = 'insufficient_privilege';
    END IF;
    FOREACH t IN ARRAY COALESCE(p_tenant_ids, ARRAY[]::uuid[]) LOOP
        PERFORM pg_catalog.set_config('app.tenant_id', t::text, true);
        total := total + (SELECT pg_catalog.count(*) FROM pandokos.subject_mapping m WHERE m.platform_issuer_id = p_issuer_id);
    END LOOP;
    PERFORM pg_catalog.set_config('app.tenant_id', COALESCE(prev, ''), true);
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN total;
END
$$;

CREATE OR REPLACE FUNCTION pandokos.record_token_auth_failure(p_scope text, p_issuer_id uuid, p_reason text, p_subject text, p_request_id text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
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
    PERFORM pandokos.definer_settings_restore(v_settings);
END
$$;

CREATE OR REPLACE FUNCTION pandokos.delete_platform_issuer_limit_rules(p_issuer_id uuid, p_tenant_ids uuid[], p_actor_id text,
                                                                      p_actor_name text, p_key_id text, p_request_id text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    prev       text := pg_catalog.current_setting('app.tenant_id', true);
    t          uuid;
    n          bigint;
    total      bigint := 0;
BEGIN
    IF NOT pandokos.platform_scope() THEN
        RAISE EXCEPTION 'delete_platform_issuer_limit_rules needs the platform scope' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF p_issuer_id IS NULL
       OR p_actor_id IS NULL OR p_actor_id !~ '^[A-Za-z0-9_-]{0,64}$'
       OR p_actor_name IS NULL OR pg_catalog.char_length(p_actor_name) > 200 OR p_actor_name ~ '[[:cntrl:]]'
       OR p_key_id IS NULL OR p_key_id !~ '^[A-Za-z0-9_-]{0,64}$'
       OR p_request_id IS NULL OR p_request_id !~ '^([0-9a-f]{32})?$' THEN
        RAISE EXCEPTION 'invalid audit actor' USING ERRCODE = 'check_violation';
    END IF;
    FOREACH t IN ARRAY ARRAY(SELECT DISTINCT x FROM pg_catalog.unnest(COALESCE(p_tenant_ids, ARRAY[]::uuid[])) AS x ORDER BY x) LOOP
        PERFORM pg_catalog.set_config('app.tenant_id', t::text, true);
        -- A tenant with no mapping of this issuer has nothing to delete: it is neither locked nor
        -- touched (the caller may name every tenant of the installation). Safe without the lock: the
        -- handler holds the row of the issuer, so no mapping of it is created meanwhile.
        IF NOT EXISTS (SELECT 1 FROM pandokos.subject_mapping m WHERE m.tenant_id = t AND m.platform_issuer_id = p_issuer_id) THEN
            CONTINUE;
        END IF;
        -- The lock of createLimit: no rule is created for one of these mappings meanwhile.
        PERFORM pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('pandokos.limit_rule:' || t::text, 0));
        WITH gone AS (
            DELETE FROM pandokos.limit_rule r
             USING pandokos.subject_mapping m
             WHERE m.tenant_id = r.tenant_id AND m.id = r.subject_mapping_id AND m.platform_issuer_id = p_issuer_id
            RETURNING r.*
        )
        INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_id, actor_name, key_id, action, resource_type,
                                          resource_id, outcome, request_id, detail)
        SELECT g.tenant_id, 'platform', p_actor_id, p_actor_name, p_key_id, 'limit.delete', 'limit_rule', g.id::text,
               'success', p_request_id,
               pg_catalog.jsonb_strip_nulls(pg_catalog.jsonb_build_object(
                   'cause', 'platform_issuer_deleted', 'cause_id', p_issuer_id::text,
                   'subject_kind', g.subject_kind, 'subject_id', g.subject_mapping_id::text,
                   'provider', COALESCE(p.name, ''), 'model', COALESCE(mo.name, ''),
                   'provider_id', g.provider_id::text, 'model_id', g.model_id::text,
                   'metric', g.metric,
                   'limit', CASE WHEN g.metric NOT IN ('cost_per_day', 'cost_per_month') THEN g.limit_value END,
                   'amount', CASE WHEN g.metric IN ('cost_per_day', 'cost_per_month')
                                  THEN pg_catalog.trim_scale(g.limit_value::numeric / 1000000)::text END,
                   'currency', NULLIF(g.currency, ''),
                   'enforce', g.enforce, 'thresholds', pg_catalog.to_jsonb(g.thresholds)))
          FROM gone g
          LEFT JOIN pandokos.provider p ON p.id = g.provider_id
          LEFT JOIN pandokos.model mo ON mo.id = g.model_id;
        GET DIAGNOSTICS n = ROW_COUNT;
        total := total + n;
    END LOOP;
    PERFORM pg_catalog.set_config('app.tenant_id', COALESCE(prev, ''), true);
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN total;
END
$$;

CREATE OR REPLACE FUNCTION pandokos.membership_guard() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    home       uuid;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.user_id <> OLD.user_id THEN
            RAISE EXCEPTION 'user_id of a membership is immutable' USING ERRCODE = 'check_violation';
        END IF;
        PERFORM pandokos.definer_settings_restore(v_settings);
        RETURN NEW;
    END IF;
    SELECT u.tenant_id INTO home FROM pandokos.app_user u WHERE u.id = NEW.user_id;
    IF home IS NOT NULL AND home <> NEW.tenant_id THEN
        RAISE EXCEPTION 'a user managed by a tenant is a member of that tenant only' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION pandokos.membership_directory_sync() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM pandokos.membership_directory d WHERE d.user_id = OLD.user_id AND d.tenant_id = OLD.tenant_id;
    ELSE
        INSERT INTO pandokos.membership_directory (user_id, tenant_id) VALUES (NEW.user_id, NEW.tenant_id);
    END IF;
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN NULL;
END
$$;

CREATE OR REPLACE FUNCTION pandokos.user_tenants(p_user_id uuid) RETURNS SETOF uuid
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
BEGIN
    IF NOT pandokos.platform_scope() THEN
        RAISE EXCEPTION 'user_tenants needs the platform scope' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN QUERY SELECT d.tenant_id FROM pandokos.membership_directory d WHERE d.user_id = p_user_id ORDER BY d.tenant_id;
    PERFORM pandokos.definer_settings_restore(v_settings);
END
$$;

CREATE OR REPLACE FUNCTION pandokos.platform_role_assignment_guard() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
BEGIN
    IF (SELECT u.tenant_id FROM pandokos.app_user u WHERE u.id = NEW.user_id) IS NOT NULL THEN
        RAISE EXCEPTION 'a user managed by a tenant holds no platform role' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION pandokos.platform_user_lookup(p_user_id uuid, p_username text) RETURNS uuid
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    v_user     uuid;
BEGIN
    v_user := (SELECT u.id
                 FROM pandokos.app_user u LEFT JOIN pandokos.local_account a ON a.user_id = u.id
                WHERE u.tenant_id IS NULL AND pandokos.current_tenant_id() IS NOT NULL
                  AND pg_catalog.num_nonnulls(p_user_id, p_username) = 1
                  AND (u.id = p_user_id OR a.username = p_username));
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN v_user;
END
$$;

CREATE OR REPLACE FUNCTION pandokos.record_unlimited_traffic(p_day date, p_time_zone text, p_provider text, p_model text, p_request_id text) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    t          uuid := pandokos.current_tenant_id();
    d          jsonb;
    written    boolean := false;
BEGIN
    IF t IS NULL OR p_day IS NULL
       OR p_day NOT BETWEEN (pg_catalog.timezone('UTC', pg_catalog.now()))::date - 2
                        AND (pg_catalog.timezone('UTC', pg_catalog.now()))::date + 2
       OR p_time_zone IS NULL OR p_time_zone !~ '^[A-Za-z][A-Za-z0-9_+-]{0,31}(/[A-Za-z0-9_+-]{1,31}){0,2}$'
       OR p_provider IS NULL OR p_provider !~ '^[a-z][a-z0-9-]{0,62}$'
       OR p_model IS NULL OR p_model !~ '^([A-Za-z0-9][A-Za-z0-9._:@/+-]{0,127})?$'
       OR p_request_id IS NULL OR p_request_id !~ '^[0-9a-f]{32}$' THEN
        RAISE EXCEPTION 'invalid unlimited traffic record' USING ERRCODE = 'check_violation';
    END IF;
    d := pg_catalog.jsonb_build_object('day', pg_catalog.to_char(p_day, 'YYYY-MM-DD'), 'time_zone', p_time_zone,
                                       'provider', p_provider, 'model', p_model);
    BEGIN
        INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_name, action, resource_type, resource_id,
                                          outcome, request_id, detail)
        VALUES (t, 'platform', 'gateway', 'limit.unlimited_traffic', 'tenant', t::text, 'success', p_request_id, d);
        written := true;
    EXCEPTION WHEN unique_violation THEN
        NULL;
    END;
    BEGIN
        INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, resource_id,
                                                   outcome, request_id, detail)
        VALUES ('platform', 'gateway', 'limit.unlimited_traffic', 'tenant', t::text, 'success', p_request_id, d);
    EXCEPTION WHEN unique_violation THEN
        NULL;
    END;
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN written;
END
$$;

CREATE OR REPLACE FUNCTION pandokos.audit_seal_platform() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    h_seq      bigint;
    h_hash     bytea;
BEGIN
    NEW.id := pandokos.uuid_v7();
    NEW.occurred_at := pg_catalog.now();
    SELECT c.seq, c.hash INTO h_seq, h_hash FROM pandokos.platform_audit_chain c WHERE c.id = 1 FOR UPDATE;
    NEW.seq := h_seq + 1;
    NEW.prev_hash := h_hash;
    NEW.hash := pandokos.audit_row_hash('platform', NEW.seq, NEW.id, NEW.occurred_at, NEW.actor_type, NEW.actor_id,
                                        NEW.actor_name, NEW.key_id, NEW.action, NEW.resource_type, NEW.resource_id,
                                        NEW.outcome, NEW.request_id, NEW.reason, NULL, NEW.detail, NEW.prev_hash);
    UPDATE pandokos.platform_audit_chain SET seq = NEW.seq, hash = NEW.hash WHERE id = 1;
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION pandokos.platform_retention(p_usage_months integer, p_audit_months integer,
                                           OUT partitions_dropped integer, OUT audit_rows bigint)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    v_month date := pg_catalog.date_trunc('month', pg_catalog.timezone('UTC', pg_catalog.now()))::date;
    v_cut   date;
    v_names text[] := '{}';
    v_name  text;
    v_last  bigint;
    v_first bigint;
    v_hash  bytea;
BEGIN
    IF NOT pandokos.platform_scope() THEN
        RAISE EXCEPTION 'platform_retention needs the platform scope' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF p_usage_months IS NULL OR (p_usage_months <> 0 AND p_usage_months NOT BETWEEN 2 AND 120)
       OR p_audit_months IS NULL OR (p_audit_months <> 0 AND p_audit_months NOT BETWEEN 1 AND 1200) THEN
        RAISE EXCEPTION 'invalid retention' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM pg_catalog.set_config('lock_timeout', '5s', true);
    partitions_dropped := 0;
    audit_rows := 0;
    IF p_usage_months > 0 THEN
        v_cut := (v_month - pg_catalog.make_interval(months => p_usage_months))::date;
        PERFORM pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('pandokos.usage_record.partitions', 0));
        FOR v_name IN
            SELECT c.relname::text
              FROM pg_catalog.pg_inherits i
              JOIN pg_catalog.pg_class c ON c.oid = i.inhrelid
             WHERE i.inhparent = 'pandokos.usage_record'::pg_catalog.regclass
               AND c.relname ~ '^usage_record_y[0-9]{4}m[0-9]{2}$'
               AND (pg_catalog.to_date(pg_catalog.substr(c.relname, 14), '"y"YYYY"m"MM')
                    + pg_catalog.make_interval(months => 1))::date <= v_cut
             ORDER BY 1
        LOOP
            EXECUTE pg_catalog.format('DROP TABLE pandokos.%I', v_name);
            v_names := v_names || v_name;
        END LOOP;
        partitions_dropped := pg_catalog.cardinality(v_names);
        IF partitions_dropped > 0 THEN
            INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, outcome, detail)
            VALUES ('platform', 'retention', 'usage.retention', 'usage_record', 'success',
                    pg_catalog.jsonb_build_object('before', pg_catalog.to_char(v_cut, 'YYYY-MM-DD'),
                                                  'months', p_usage_months, 'partitions', pg_catalog.to_jsonb(v_names)));
        END IF;
    END IF;
    IF p_audit_months > 0 THEN
        v_cut := (v_month - pg_catalog.make_interval(months => p_audit_months))::date;
        PERFORM pg_catalog.set_config('pandokos.retention', 'on', true);
        SELECT c.anchor_seq INTO v_first FROM pandokos.platform_audit_chain c WHERE c.id = 1 FOR UPDATE;
        SELECT pg_catalog.max(e.seq) INTO v_last FROM pandokos.platform_audit_event e
         WHERE e.occurred_at < (v_cut::timestamp AT TIME ZONE 'UTC');
        IF v_last IS NOT NULL AND v_last >= v_first THEN
            SELECT e.hash INTO v_hash FROM pandokos.platform_audit_event e WHERE e.seq = v_last;
            DELETE FROM pandokos.platform_audit_event e WHERE e.seq <= v_last;
            GET DIAGNOSTICS audit_rows = ROW_COUNT;
            UPDATE pandokos.platform_audit_chain SET anchor_seq = v_last + 1, anchor_hash = v_hash WHERE id = 1;
        END IF;
        PERFORM pg_catalog.set_config('pandokos.retention', 'off', true);
        IF audit_rows > 0 THEN
            INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, outcome, detail)
            VALUES ('platform', 'retention', 'audit.retention', 'platform_audit_event', 'success',
                    pg_catalog.jsonb_build_object('before', pg_catalog.to_char(v_cut, 'YYYY-MM-DD'),
                                                  'months', p_audit_months, 'deleted', audit_rows,
                                                  'first_seq', v_first, 'last_seq', v_last,
                                                  'last_hash', pg_catalog.encode(v_hash, 'hex')));
        END IF;
    END IF;
END
$$;

CREATE OR REPLACE FUNCTION pandokos.tenant_retention(p_usage_months integer, p_audit_months integer,
                                         OUT usage_rows bigint, OUT rollup_rows bigint, OUT audit_rows bigint)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    t       uuid := pandokos.current_tenant_id();
    v_month date := pg_catalog.date_trunc('month', pg_catalog.timezone('UTC', pg_catalog.now()))::date;
    v_cut   date;
    v_at    timestamptz;
    v_last  bigint;
    v_first bigint;
    v_hash  bytea;
BEGIN
    IF t IS NULL THEN
        RAISE EXCEPTION 'tenant_retention needs a tenant session' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF p_usage_months IS NULL OR (p_usage_months <> 0 AND p_usage_months NOT BETWEEN 2 AND 120)
       OR p_audit_months IS NULL OR (p_audit_months <> 0 AND p_audit_months NOT BETWEEN 1 AND 1200) THEN
        RAISE EXCEPTION 'invalid retention' USING ERRCODE = 'check_violation';
    END IF;
    usage_rows := 0;
    rollup_rows := 0;
    audit_rows := 0;
    IF p_usage_months > 0 THEN
        v_cut := (v_month - pg_catalog.make_interval(months => p_usage_months))::date;
        v_at := v_cut::timestamp AT TIME ZONE 'UTC';
        DELETE FROM pandokos.usage_rollup r WHERE r.hour < v_at;
        GET DIAGNOSTICS rollup_rows = ROW_COUNT;
        PERFORM pg_catalog.set_config('pandokos.retention', 'on', true);
        DELETE FROM pandokos.usage_record_default u WHERE u.started_at < v_at;
        GET DIAGNOSTICS usage_rows = ROW_COUNT;
        PERFORM pg_catalog.set_config('pandokos.retention', 'off', true);
        IF usage_rows > 0 THEN
            INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_name, action, resource_type, outcome, detail)
            VALUES (t, 'platform', 'retention', 'usage.retention', 'usage_record', 'success',
                    pg_catalog.jsonb_build_object('before', pg_catalog.to_char(v_cut, 'YYYY-MM-DD'),
                                                  'months', p_usage_months, 'deleted', usage_rows));
        END IF;
    END IF;
    IF p_audit_months > 0 THEN
        v_cut := (v_month - pg_catalog.make_interval(months => p_audit_months))::date;
        INSERT INTO pandokos.audit_chain (tenant_id) VALUES (t) ON CONFLICT (tenant_id) DO NOTHING;
        SELECT c.anchor_seq INTO v_first FROM pandokos.audit_chain c WHERE c.tenant_id = t FOR UPDATE;
        SELECT pg_catalog.max(e.seq) INTO v_last FROM pandokos.audit_event e
         WHERE e.occurred_at < (v_cut::timestamp AT TIME ZONE 'UTC');
        IF v_last IS NOT NULL AND v_last >= v_first THEN
            SELECT e.hash INTO v_hash FROM pandokos.audit_event e WHERE e.seq = v_last;
            PERFORM pg_catalog.set_config('pandokos.retention', 'on', true);
            DELETE FROM pandokos.audit_event e WHERE e.seq <= v_last;
            GET DIAGNOSTICS audit_rows = ROW_COUNT;
            PERFORM pg_catalog.set_config('pandokos.retention', 'off', true);
            UPDATE pandokos.audit_chain SET anchor_seq = v_last + 1, anchor_hash = v_hash WHERE tenant_id = t;
            INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_name, action, resource_type, outcome, detail)
            VALUES (t, 'platform', 'retention', 'audit.retention', 'audit_event', 'success',
                    pg_catalog.jsonb_build_object('before', pg_catalog.to_char(v_cut, 'YYYY-MM-DD'),
                                                  'months', p_audit_months, 'deleted', audit_rows,
                                                  'first_seq', v_first, 'last_seq', v_last,
                                                  'last_hash', pg_catalog.encode(v_hash, 'hex')));
        END IF;
    END IF;
END
$$;
