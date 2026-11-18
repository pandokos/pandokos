-- 0011_limit_integrity: limit rules name their subject and their scope by identifier, with foreign
-- keys; the rules that named nothing are deleted with an audit row; a time zone per tenant; how
-- limits treated a request, in the usage ledger; the audit row of a tenant's first unlimited
-- request of a day; the function that deletes the rules of a platform-level issuer's mappings
-- (ADR 0049, 0050, 0052 as amended by sprint 5.3). Runs in one transaction as pandokos_owner.
-- Every name is schema-qualified. Not compatible with the binary of sprint 5.2: stop, migrate,
-- start (ADR 0026).

-- ---------------------------------------------------------------------------------------------
-- 1. The time zone of a tenant.
-- ---------------------------------------------------------------------------------------------

-- time_zone is the IANA zone in which the tenant's days and months start; '' is the default of the
-- installation (limits.time_zone). A change does not take effect at once: until
-- time_zone_effective_from the zone in force is time_zone_previous ('' again the default). The
-- instant is computed by the administration API (the start of the next month in the zone in
-- force); the trigger below checks the shape of every change.
ALTER TABLE pandokos.tenant
    ADD COLUMN time_zone                text NOT NULL DEFAULT '',
    ADD COLUMN time_zone_previous       text NOT NULL DEFAULT '',
    ADD COLUMN time_zone_effective_from timestamptz,
    ADD CONSTRAINT tenant_time_zone_format CHECK (
        time_zone ~ '^([A-Za-z][A-Za-z0-9_+-]{0,31}(/[A-Za-z0-9_+-]{1,31}){0,2})?$'
        AND time_zone_previous ~ '^([A-Za-z][A-Za-z0-9_+-]{0,31}(/[A-Za-z0-9_+-]{1,31}){0,2})?$'),
    ADD CONSTRAINT tenant_time_zone_pending CHECK (
        (time_zone_effective_from IS NULL AND time_zone_previous = '')
        OR (time_zone_effective_from IS NOT NULL AND time_zone_previous <> time_zone));

-- A tenant's session (a tenant setting without the platform scope) changes nothing of its row but
-- the time zone. A zone is in force from the creation of the tenant; afterwards the zone in force
-- changes only through a pending change whose instant lies in the future (at most 32 days), and a
-- pending change that is replaced keeps its instant: no statement moves a period boundary that
-- has not been reached into the past.
CREATE FUNCTION pandokos.tenant_time_zone_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    pending  boolean;
    in_force text;
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.time_zone_previous <> '' OR NEW.time_zone_effective_from IS NOT NULL THEN
            RAISE EXCEPTION 'a tenant is created without a pending time zone' USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;
    IF pandokos.current_tenant_id() IS NOT NULL AND NOT pandokos.platform_scope()
       AND (NEW.name <> OLD.name OR NEW.display_name <> OLD.display_name OR NEW.state <> OLD.state) THEN
        RAISE EXCEPTION 'a tenant session changes only the time zone of its tenant' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF NEW.time_zone = OLD.time_zone AND NEW.time_zone_previous = OLD.time_zone_previous
       AND NEW.time_zone_effective_from IS NOT DISTINCT FROM OLD.time_zone_effective_from THEN
        RETURN NEW;
    END IF;
    pending := OLD.time_zone_effective_from IS NOT NULL AND now() < OLD.time_zone_effective_from;
    in_force := CASE WHEN pending THEN OLD.time_zone_previous ELSE OLD.time_zone END;
    IF NEW.time_zone_effective_from IS NULL THEN
        -- No pending change: the zone must be the one in force (a pending change withdrawn, or one
        -- that took effect and is tidied up).
        IF NEW.time_zone <> in_force THEN
            RAISE EXCEPTION 'the time zone in force changes only through a pending change' USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.time_zone_previous <> in_force THEN
        RAISE EXCEPTION 'time_zone_previous must be the time zone in force' USING ERRCODE = 'check_violation';
    END IF;
    IF pending THEN
        IF NEW.time_zone_effective_from <> OLD.time_zone_effective_from THEN
            RAISE EXCEPTION 'a pending time zone change keeps its instant' USING ERRCODE = 'check_violation';
        END IF;
    ELSIF NEW.time_zone_effective_from <= now() OR NEW.time_zone_effective_from > now() + interval '32 days' THEN
        RAISE EXCEPTION 'a time zone change takes effect in the future, within 32 days' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER tenant_time_zone_guard BEFORE INSERT OR UPDATE ON pandokos.tenant
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_time_zone_guard();

-- The calendar of a tenant is cached with its limit rules.
CREATE FUNCTION pandokos.notify_tenant_limits() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    PERFORM pg_catalog.pg_notify('pandokos_cache', 'limits:' || NEW.id::text);
    RETURN NULL;
END
$$;

CREATE TRIGGER tenant_time_zone_notify AFTER UPDATE ON pandokos.tenant
    FOR EACH ROW WHEN (OLD.time_zone IS DISTINCT FROM NEW.time_zone
                       OR OLD.time_zone_previous IS DISTINCT FROM NEW.time_zone_previous
                       OR OLD.time_zone_effective_from IS DISTINCT FROM NEW.time_zone_effective_from)
    EXECUTE FUNCTION pandokos.notify_tenant_limits();

-- The tenant's own session may update its row; the guard above leaves it the time zone only.
CREATE POLICY tenant_self_update ON pandokos.tenant
    FOR UPDATE TO pandokos_admin
    USING (id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (id = (SELECT pandokos.current_tenant_id()));

GRANT INSERT (time_zone) ON pandokos.tenant TO pandokos_admin;
GRANT UPDATE (time_zone, time_zone_previous, time_zone_effective_from) ON pandokos.tenant TO pandokos_admin;

-- ---------------------------------------------------------------------------------------------
-- 2. How limits treated a request, in the usage ledger.
-- ---------------------------------------------------------------------------------------------

-- NULL: the row was written before this migration. The column has no default and the check is
-- NOT VALID, so no partition is rewritten or scanned: every existing row holds NULL, which the
-- check accepts, and every new row is checked.
ALTER TABLE pandokos.usage_record ADD COLUMN limit_state text;
ALTER TABLE pandokos.usage_record ADD CONSTRAINT usage_record_limit_state_valid
    CHECK (limit_state IS NULL OR limit_state IN ('limited', 'unlimited', 'unverified', 'not_evaluated')) NOT VALID;

GRANT INSERT (limit_state) ON pandokos.usage_record TO pandokos_gateway;

-- ---------------------------------------------------------------------------------------------
-- 3. Limit rules: typed subject columns, the scope by identifier.
-- ---------------------------------------------------------------------------------------------

-- A model is referenced together with its provider.
ALTER TABLE pandokos.model ADD CONSTRAINT model_provider_id_key UNIQUE (provider_id, id);

ALTER TABLE pandokos.limit_rule
    ADD COLUMN service_account_id uuid,
    ADD COLUMN virtual_key_id     uuid,
    ADD COLUMN subject_mapping_id uuid,
    ADD COLUMN provider_id        uuid,
    ADD COLUMN model_id           uuid;

ALTER TABLE pandokos.platform_limit_rule
    ADD COLUMN provider_id uuid,
    ADD COLUMN model_id    uuid;

-- The data migration reads and writes rows of every tenant. Forced row-level security hides them
-- from the owner too (ADR 0026), so it is lifted on the tables involved for the rest of these
-- statements and put back below, in this transaction; the row triggers of the two rule tables are
-- off meanwhile, so that updated_at keeps its value and nothing is notified per row.
ALTER TABLE pandokos.limit_rule           NO FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.platform_limit_rule  NO FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.service_account      NO FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.virtual_key          NO FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.subject_mapping      NO FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.provider             NO FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.model                NO FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.audit_event          NO FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.platform_audit_event NO FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.limit_rule          DISABLE TRIGGER USER;
ALTER TABLE pandokos.platform_limit_rule DISABLE TRIGGER USER;

-- Names become identifiers: what a name designates now is what the rule matched until now. A key
-- that is revoked is not resolved: its rules go, as they do from now on when a key is revoked.
UPDATE pandokos.limit_rule r SET service_account_id = s.id
  FROM pandokos.service_account s
 WHERE r.subject_kind = 'service_account' AND s.tenant_id = r.tenant_id AND s.id::text = r.subject_id;
UPDATE pandokos.limit_rule r SET virtual_key_id = k.id
  FROM pandokos.virtual_key k
 WHERE r.subject_kind = 'key' AND k.tenant_id = r.tenant_id AND k.key_id = r.subject_id AND k.revoked_at IS NULL;
UPDATE pandokos.limit_rule r SET subject_mapping_id = m.id
  FROM pandokos.subject_mapping m
 WHERE r.subject_kind = 'key' AND m.tenant_id = r.tenant_id AND m.id::text = r.subject_id;
UPDATE pandokos.limit_rule r SET provider_id = p.id
  FROM pandokos.provider p
 WHERE r.provider <> '' AND p.name = r.provider;
UPDATE pandokos.limit_rule r SET model_id = m.id
  FROM pandokos.model m
 WHERE r.model <> '' AND m.provider_id = r.provider_id AND m.name = r.model;
UPDATE pandokos.platform_limit_rule r SET provider_id = p.id
  FROM pandokos.provider p
 WHERE r.provider <> '' AND p.name = r.provider;
UPDATE pandokos.platform_limit_rule r SET model_id = m.id
  FROM pandokos.model m
 WHERE r.model <> '' AND m.provider_id = r.provider_id AND m.name = r.model;

-- A rule whose subject or scope designates nothing matched no request. It is deleted, and the
-- audit log of its tenant (of the platform, for a ceiling) keeps every one of its fields.
WITH gone AS (
    DELETE FROM pandokos.limit_rule r
     WHERE (r.subject_kind <> 'tenant' AND num_nonnulls(r.service_account_id, r.virtual_key_id, r.subject_mapping_id) = 0)
        OR (r.provider <> '' AND r.provider_id IS NULL)
        OR (r.model <> '' AND r.model_id IS NULL)
    RETURNING r.*
)
INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_name, action, resource_type, resource_id, outcome, detail)
SELECT g.tenant_id, 'platform', 'migration 0011', 'limit.delete', 'limit_rule', g.id::text, 'success',
       jsonb_strip_nulls(jsonb_build_object(
           'cause', CASE WHEN g.subject_kind <> 'tenant'
                              AND num_nonnulls(g.service_account_id, g.virtual_key_id, g.subject_mapping_id) = 0
                         THEN CASE WHEN EXISTS (SELECT 1 FROM pandokos.virtual_key k
                                                 WHERE k.tenant_id = g.tenant_id AND k.key_id = g.subject_id)
                                   THEN 'migration_key_revoked' ELSE 'migration_subject_missing' END
                         ELSE 'migration_scope_missing' END,
           'subject_kind', g.subject_kind, 'subject_id', g.subject_id, 'provider', g.provider, 'model', g.model,
           'metric', g.metric,
           'limit', CASE WHEN g.metric NOT IN ('cost_per_day', 'cost_per_month') THEN g.limit_value END,
           'amount', CASE WHEN g.metric IN ('cost_per_day', 'cost_per_month')
                          THEN trim_scale(g.limit_value::numeric / 1000000)::text END,
           'currency', NULLIF(g.currency, ''),
           'enforce', g.enforce, 'thresholds', to_jsonb(g.thresholds)))
  FROM gone g;

WITH gone AS (
    DELETE FROM pandokos.platform_limit_rule r
     WHERE (r.provider <> '' AND r.provider_id IS NULL)
        OR (r.model <> '' AND r.model_id IS NULL)
    RETURNING r.*
)
INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, resource_id, outcome, detail)
SELECT 'platform', 'migration 0011', 'platform_limit.delete', 'platform_limit_rule', g.id::text, 'success',
       jsonb_strip_nulls(jsonb_build_object(
           'cause', 'migration_scope_missing',
           'tenant', COALESCE(g.tenant_id::text, ''), 'provider', g.provider, 'model', g.model, 'metric', g.metric,
           'limit', CASE WHEN g.metric NOT IN ('cost_per_day', 'cost_per_month') THEN g.limit_value END,
           'amount', CASE WHEN g.metric IN ('cost_per_day', 'cost_per_month')
                          THEN trim_scale(g.limit_value::numeric / 1000000)::text END,
           'currency', NULLIF(g.currency, ''),
           'enforce', g.enforce, 'thresholds', to_jsonb(g.thresholds)))
  FROM gone g;

ALTER TABLE pandokos.limit_rule          ENABLE TRIGGER USER;
ALTER TABLE pandokos.platform_limit_rule ENABLE TRIGGER USER;
ALTER TABLE pandokos.limit_rule           FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.platform_limit_rule  FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.service_account      FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.virtual_key          FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.subject_mapping      FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.provider             FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.model                FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.audit_event          FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.platform_audit_event FORCE ROW LEVEL SECURITY;

-- The names go; the identifiers get their foreign keys. subject_kind 'tenant' sets no subject
-- column, 'service_account' sets service_account_id, 'key' sets exactly one of virtual_key_id (a
-- virtual key) and subject_mapping_id (a caller with a token). ON DELETE RESTRICT: whoever deletes
-- a subject, a provider or a model deletes the rules first, and audits them, or is refused.
ALTER TABLE pandokos.limit_rule
    DROP CONSTRAINT limit_rule_key,
    DROP CONSTRAINT limit_rule_subject_id_format,
    DROP CONSTRAINT limit_rule_scope_format;
ALTER TABLE pandokos.limit_rule
    DROP COLUMN subject_id,
    DROP COLUMN provider,
    DROP COLUMN model;
ALTER TABLE pandokos.limit_rule
    ADD CONSTRAINT limit_rule_service_account_fkey FOREIGN KEY (tenant_id, service_account_id)
        REFERENCES pandokos.service_account (tenant_id, id) ON DELETE RESTRICT,
    ADD CONSTRAINT limit_rule_virtual_key_fkey FOREIGN KEY (tenant_id, virtual_key_id)
        REFERENCES pandokos.virtual_key (tenant_id, id) ON DELETE RESTRICT,
    ADD CONSTRAINT limit_rule_subject_mapping_fkey FOREIGN KEY (tenant_id, subject_mapping_id)
        REFERENCES pandokos.subject_mapping (tenant_id, id) ON DELETE RESTRICT,
    ADD CONSTRAINT limit_rule_provider_fkey FOREIGN KEY (provider_id)
        REFERENCES pandokos.provider (id) ON DELETE RESTRICT,
    ADD CONSTRAINT limit_rule_model_fkey FOREIGN KEY (provider_id, model_id)
        REFERENCES pandokos.model (provider_id, id) ON DELETE RESTRICT,
    ADD CONSTRAINT limit_rule_subject_by_kind CHECK (
        (subject_kind = 'tenant' AND num_nonnulls(service_account_id, virtual_key_id, subject_mapping_id) = 0)
        OR (subject_kind = 'service_account' AND service_account_id IS NOT NULL
            AND num_nonnulls(virtual_key_id, subject_mapping_id) = 0)
        OR (subject_kind = 'key' AND service_account_id IS NULL
            AND num_nonnulls(virtual_key_id, subject_mapping_id) = 1)),
    ADD CONSTRAINT limit_rule_scope_valid CHECK (model_id IS NULL OR provider_id IS NOT NULL),
    ADD CONSTRAINT limit_rule_key UNIQUE NULLS NOT DISTINCT
        (tenant_id, service_account_id, virtual_key_id, subject_mapping_id, provider_id, model_id, metric);

CREATE INDEX limit_rule_subject_mapping_idx ON pandokos.limit_rule (tenant_id, subject_mapping_id)
    WHERE subject_mapping_id IS NOT NULL;
CREATE INDEX limit_rule_virtual_key_idx ON pandokos.limit_rule (tenant_id, virtual_key_id)
    WHERE virtual_key_id IS NOT NULL;
CREATE INDEX limit_rule_provider_idx ON pandokos.limit_rule (provider_id) WHERE provider_id IS NOT NULL;
CREATE INDEX limit_rule_model_idx ON pandokos.limit_rule (model_id) WHERE model_id IS NOT NULL;

GRANT INSERT (service_account_id, virtual_key_id, subject_mapping_id, provider_id, model_id)
    ON pandokos.limit_rule TO pandokos_admin;

ALTER TABLE pandokos.platform_limit_rule
    DROP CONSTRAINT platform_limit_rule_key,
    DROP CONSTRAINT platform_limit_rule_scope_format;
ALTER TABLE pandokos.platform_limit_rule
    DROP COLUMN provider,
    DROP COLUMN model;
ALTER TABLE pandokos.platform_limit_rule
    ADD CONSTRAINT platform_limit_rule_provider_fkey FOREIGN KEY (provider_id)
        REFERENCES pandokos.provider (id) ON DELETE RESTRICT,
    ADD CONSTRAINT platform_limit_rule_model_fkey FOREIGN KEY (provider_id, model_id)
        REFERENCES pandokos.model (provider_id, id) ON DELETE RESTRICT,
    ADD CONSTRAINT platform_limit_rule_scope_valid CHECK (model_id IS NULL OR provider_id IS NOT NULL),
    ADD CONSTRAINT platform_limit_rule_key UNIQUE NULLS NOT DISTINCT (tenant_id, provider_id, model_id, metric);

CREATE INDEX platform_limit_rule_provider_idx ON pandokos.platform_limit_rule (provider_id) WHERE provider_id IS NOT NULL;
CREATE INDEX platform_limit_rule_model_idx ON pandokos.platform_limit_rule (model_id) WHERE model_id IS NOT NULL;

GRANT INSERT (provider_id, model_id) ON pandokos.platform_limit_rule TO pandokos_admin;

-- A virtual key is never deleted, so its foreign key never fires: two triggers hold "a revoked key
-- has no limit rule" instead. A rule is not created for a revoked key, and a transaction that
-- revokes a key and leaves a rule of it is refused when it commits.
CREATE FUNCTION pandokos.limit_rule_key_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NEW.virtual_key_id IS NOT NULL THEN
        -- The share lock makes the pair of triggers race-free for every role, whatever advisory lock a
        -- caller took: a transaction that revokes the key waits for this one to commit (and then finds
        -- the rule when it commits), one that has revoked it first is seen here once it commits.
        PERFORM 1 FROM pandokos.virtual_key k WHERE k.tenant_id = NEW.tenant_id AND k.id = NEW.virtual_key_id FOR SHARE;
    END IF;
    IF NEW.virtual_key_id IS NOT NULL
       AND EXISTS (SELECT 1 FROM pandokos.virtual_key k
                    WHERE k.tenant_id = NEW.tenant_id AND k.id = NEW.virtual_key_id AND k.revoked_at IS NOT NULL) THEN
        RAISE EXCEPTION 'a revoked key takes no limit rule' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER limit_rule_key_guard BEFORE INSERT ON pandokos.limit_rule
    FOR EACH ROW EXECUTE FUNCTION pandokos.limit_rule_key_guard();

CREATE FUNCTION pandokos.virtual_key_revoke_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM pandokos.limit_rule r WHERE r.tenant_id = NEW.tenant_id AND r.virtual_key_id = NEW.id) THEN
        RAISE EXCEPTION 'a revoked key still has limit rules' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER virtual_key_revoke_guard AFTER UPDATE OF revoked_at ON pandokos.virtual_key
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (NEW.revoked_at IS NOT NULL)
    EXECUTE FUNCTION pandokos.virtual_key_revoke_guard();

-- ---------------------------------------------------------------------------------------------
-- 4. The rules of the subject mappings of a platform-level issuer.
-- ---------------------------------------------------------------------------------------------

-- Deleting a platform-level registration cascades to the subject mappings of its tenants, and a
-- mapping that a limit rule names cannot go (limit_rule_subject_mapping_fkey). The rules and the
-- audit logs are the tenants' and the platform session reaches neither, so this function deletes
-- those rules tenant by tenant, as that tenant's session (the setting is local to the
-- transaction), writes one audit row limit.delete per rule in the log of its tenant, naming the
-- platform actor it is given, and restores the session's own tenant setting. It refuses a session
-- that is not in the platform scope. The caller names the tenants (the platform session lists them
-- all); the result is the number of rules deleted, never a row. Executable by the admin role only.
CREATE FUNCTION pandokos.delete_platform_issuer_limit_rules(p_issuer_id uuid, p_tenant_ids uuid[], p_actor_id text,
                                                           p_actor_name text, p_key_id text, p_request_id text)
    RETURNS bigint
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    prev  text := pg_catalog.current_setting('app.tenant_id', true);
    t     uuid;
    n     bigint;
    total bigint := 0;
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
    RETURN total;
END
$$;

REVOKE ALL ON FUNCTION pandokos.delete_platform_issuer_limit_rules(uuid, uuid[], text, text, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.delete_platform_issuer_limit_rules(uuid, uuid[], text, text, text, text) TO pandokos_admin;

-- The function above runs as the owner, which the catalogue policies do not name.
CREATE POLICY provider_owner_read ON pandokos.provider FOR SELECT TO pandokos_owner USING (true);
CREATE POLICY model_owner_read ON pandokos.model FOR SELECT TO pandokos_owner USING (true);

-- ---------------------------------------------------------------------------------------------
-- 5. The first request of a tenant in a day that no limit rule covers.
-- ---------------------------------------------------------------------------------------------

-- One audit row per tenant and day in each log, whoever records it and however often: the unique
-- keys are what makes recording idempotent across instances and restarts.
CREATE UNIQUE INDEX audit_event_unlimited_traffic_key ON pandokos.audit_event (tenant_id, (detail ->> 'day'))
    WHERE action = 'limit.unlimited_traffic';
CREATE UNIQUE INDEX platform_audit_event_unlimited_traffic_key ON pandokos.platform_audit_event (resource_id, (detail ->> 'day'))
    WHERE action = 'limit.unlimited_traffic';

-- The function below runs as the owner, for which the platform log has no policy: it may insert
-- this one action, and see it (INSERT ... ON CONFLICT needs to see the row it conflicts with).
CREATE POLICY platform_audit_event_unlimited_traffic_insert ON pandokos.platform_audit_event
    FOR INSERT TO pandokos_owner
    WITH CHECK (action = 'limit.unlimited_traffic');
CREATE POLICY platform_audit_event_unlimited_traffic_select ON pandokos.platform_audit_event
    FOR SELECT TO pandokos_owner
    USING (action = 'limit.unlimited_traffic');

-- The gateway records that a request of the tenant of the session was admitted with no limit rule
-- applying to it, once per day of the tenant: one row in the audit log of the tenant and one in
-- the platform log, as record_auth_failure does for a refused key (0005). One kind of row, fixed
-- columns, no privilege on either log for the gateway role. p_day is the civil date of the request
-- in the tenant's time zone, which the gateway computes; it must lie within two days of the
-- database's date. The result says whether the row of the tenant's log was written by this call.
CREATE FUNCTION pandokos.record_unlimited_traffic(p_day date, p_time_zone text, p_provider text, p_model text,
                                                 p_request_id text) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t      uuid := pandokos.current_tenant_id();
    d      jsonb;
    n      bigint;
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
    INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_name, action, resource_type, resource_id,
                                      outcome, request_id, detail)
    VALUES (t, 'platform', 'gateway', 'limit.unlimited_traffic', 'tenant', t::text, 'success', p_request_id, d)
    ON CONFLICT (tenant_id, (detail ->> 'day')) WHERE action = 'limit.unlimited_traffic' DO NOTHING;
    GET DIAGNOSTICS n = ROW_COUNT;
    INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, resource_id,
                                               outcome, request_id, detail)
    VALUES ('platform', 'gateway', 'limit.unlimited_traffic', 'tenant', t::text, 'success', p_request_id, d)
    ON CONFLICT (resource_id, (detail ->> 'day')) WHERE action = 'limit.unlimited_traffic' DO NOTHING;
    RETURN n > 0;
END
$$;

REVOKE ALL ON FUNCTION pandokos.record_unlimited_traffic(date, text, text, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.record_unlimited_traffic(date, text, text, text, text) TO pandokos_gateway;
