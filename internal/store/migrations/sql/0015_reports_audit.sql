-- 0015_reports_audit: the hash chain of the two audit logs and its heads, the audit read grants,
-- retention of usage and audit through two audited functions, the usage rollup and its state,
-- the index of the ledger by time of recording, and the metadata keys a tenant accepts
-- (ADR 0064, ADR 0065). Runs in one transaction as pandokos_owner. Every name is
-- schema-qualified. Existing audit rows are given their place in the chain once, here.

-- ---------------------------------------------------------------------------------------------
-- 1. Append-only, except for the retention functions.
-- ---------------------------------------------------------------------------------------------

-- A row of an append-only table is never changed. It is removed only by a function of this
-- migration that runs as the owner and sets pandokos.retention for the statement that deletes: no
-- runtime role holds DELETE on these tables, so the setting gives a runtime role nothing.
CREATE OR REPLACE FUNCTION pandokos.append_only() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'DELETE' AND pg_catalog.current_setting('pandokos.retention', true) = 'on' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
END
$$;

-- ---------------------------------------------------------------------------------------------
-- 2. The canonical encoding of an audit row and its hash (ADR 0065).
-- ---------------------------------------------------------------------------------------------

-- A text as its UTF-8 bytes after their length as four bytes, big-endian.
CREATE FUNCTION pandokos.audit_field(p text) RETURNS bytea
    LANGUAGE sql IMMUTABLE STRICT
    SET search_path = pg_catalog, pg_temp
    RETURN pg_catalog.int4send(pg_catalog.octet_length(pg_catalog.convert_to(p, 'UTF8')))
           || pg_catalog.convert_to(p, 'UTF8');

-- A JSON value as bytes: a tag, then for an object its member count and the members sorted by the
-- bytes of their keys (key, value), for an array its length and its elements in order, for a
-- string or a number its text as a field (a number as PostgreSQL writes it), and nothing more for
-- true, false and null.
CREATE FUNCTION pandokos.audit_canonical(j jsonb) RETURNS bytea
    LANGUAGE plpgsql IMMUTABLE STRICT
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    out bytea;
    k   text;
    v   jsonb;
BEGIN
    CASE pg_catalog.jsonb_typeof(j)
    WHEN 'object' THEN
        out := '\x6f'::bytea || pg_catalog.int4send((SELECT pg_catalog.count(*)::integer FROM pg_catalog.jsonb_object_keys(j)));
        FOR k, v IN SELECT e.key, e.value FROM pg_catalog.jsonb_each(j) AS e ORDER BY e.key COLLATE "C" LOOP
            out := out || pandokos.audit_field(k) || pandokos.audit_canonical(v);
        END LOOP;
    WHEN 'array' THEN
        out := '\x61'::bytea || pg_catalog.int4send(pg_catalog.jsonb_array_length(j));
        FOR v IN SELECT a.e FROM pg_catalog.jsonb_array_elements(j) WITH ORDINALITY AS a(e, n) ORDER BY a.n LOOP
            out := out || pandokos.audit_canonical(v);
        END LOOP;
    WHEN 'string' THEN
        out := '\x73'::bytea || pandokos.audit_field(j #>> '{}');
    WHEN 'number' THEN
        out := '\x6e'::bytea || pandokos.audit_field(j #>> '{}');
    WHEN 'boolean' THEN
        out := CASE WHEN j = 'true'::jsonb THEN '\x74'::bytea ELSE '\x66'::bytea END;
    ELSE
        out := '\x7a'::bytea;
    END CASE;
    RETURN out;
END
$$;

-- The hash of a row: SHA-256 over the version, the hash of the previous row, the fields in this
-- order and the SHA-256 of the canonical detail. scope is 'tenant:<id>' or 'platform'; the time
-- is UTC with six fraction digits; an absent support action is the empty text.
CREATE FUNCTION pandokos.audit_row_hash(p_scope text, p_seq bigint, p_id uuid, p_occurred_at timestamptz,
                                        p_actor_type text, p_actor_id text, p_actor_name text, p_key_id text,
                                        p_action text, p_resource_type text, p_resource_id text, p_outcome text,
                                        p_request_id text, p_reason text, p_support_action_id uuid, p_detail jsonb,
                                        p_prev_hash bytea) RETURNS bytea
    LANGUAGE sql STABLE
    SET search_path = pg_catalog, pg_temp
    RETURN pg_catalog.sha256(
        pandokos.audit_field('pandokos-audit-v1') || p_prev_hash
        || pandokos.audit_field(p_scope) || pandokos.audit_field(p_seq::text) || pandokos.audit_field(p_id::text)
        || pandokos.audit_field(pg_catalog.to_char(p_occurred_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'))
        || pandokos.audit_field(p_actor_type) || pandokos.audit_field(p_actor_id) || pandokos.audit_field(p_actor_name)
        || pandokos.audit_field(p_key_id) || pandokos.audit_field(p_action) || pandokos.audit_field(p_resource_type)
        || pandokos.audit_field(p_resource_id) || pandokos.audit_field(p_outcome) || pandokos.audit_field(p_request_id)
        || pandokos.audit_field(p_reason) || pandokos.audit_field(COALESCE(p_support_action_id::text, ''))
        || pg_catalog.sha256(pandokos.audit_canonical(p_detail)));

-- ---------------------------------------------------------------------------------------------
-- 3. The heads of the chains.
-- ---------------------------------------------------------------------------------------------

-- The head of a tenant's chain (tenant-scoped table): the last sequence number and its hash, and
-- the anchor, from which the chain is verified: the first row kept by retention and the hash of the
-- row before it. Only the seal and retention functions write it; the admin role reads it.
CREATE TABLE pandokos.audit_chain (
    tenant_id   uuid   NOT NULL,
    seq         bigint NOT NULL DEFAULT 0,
    hash        bytea  NOT NULL DEFAULT pg_catalog.decode(pg_catalog.repeat('00', 32), 'hex'),
    anchor_seq  bigint NOT NULL DEFAULT 1,
    anchor_hash bytea  NOT NULL DEFAULT pg_catalog.decode(pg_catalog.repeat('00', 32), 'hex'),
    CONSTRAINT audit_chain_pkey PRIMARY KEY (tenant_id),
    CONSTRAINT audit_chain_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT audit_chain_valid CHECK (seq >= 0 AND anchor_seq BETWEEN 1 AND seq + 1
                                        AND octet_length(hash) = 32 AND octet_length(anchor_hash) = 32)
);

ALTER TABLE pandokos.audit_chain ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.audit_chain FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.audit_chain
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.audit_chain TO pandokos_admin;

-- The head of the platform's chain (global table, one row).
CREATE TABLE pandokos.platform_audit_chain (
    id          smallint NOT NULL DEFAULT 1,
    seq         bigint   NOT NULL DEFAULT 0,
    hash        bytea    NOT NULL DEFAULT pg_catalog.decode(pg_catalog.repeat('00', 32), 'hex'),
    anchor_seq  bigint   NOT NULL DEFAULT 1,
    anchor_hash bytea    NOT NULL DEFAULT pg_catalog.decode(pg_catalog.repeat('00', 32), 'hex'),
    CONSTRAINT platform_audit_chain_pkey PRIMARY KEY (id),
    CONSTRAINT platform_audit_chain_one CHECK (id = 1),
    CONSTRAINT platform_audit_chain_valid CHECK (seq >= 0 AND anchor_seq BETWEEN 1 AND seq + 1
                                                 AND octet_length(hash) = 32 AND octet_length(anchor_hash) = 32)
);

ALTER TABLE pandokos.platform_audit_chain ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.platform_audit_chain FORCE ROW LEVEL SECURITY;

CREATE POLICY platform_audit_chain_owner ON pandokos.platform_audit_chain
    FOR ALL TO pandokos_owner USING (true) WITH CHECK (true);
CREATE POLICY platform_audit_chain_select ON pandokos.platform_audit_chain
    FOR SELECT TO pandokos_admin USING ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.platform_audit_chain TO pandokos_admin;

-- ---------------------------------------------------------------------------------------------
-- 4. The chain columns, and the rows written before this migration.
-- ---------------------------------------------------------------------------------------------

ALTER TABLE pandokos.audit_event          ADD COLUMN seq bigint, ADD COLUMN prev_hash bytea, ADD COLUMN hash bytea;
ALTER TABLE pandokos.platform_audit_event ADD COLUMN seq bigint, ADD COLUMN prev_hash bytea, ADD COLUMN hash bytea;

-- Forced row-level security hides every tenant's rows from the owner (ADR 0026), and the rows are
-- append-only: both are lifted for the statements below and put back before the migration ends.
ALTER TABLE pandokos.audit_event          NO FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.platform_audit_event NO FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.audit_chain          NO FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.audit_event          DISABLE TRIGGER audit_event_append_only;
ALTER TABLE pandokos.platform_audit_event DISABLE TRIGGER platform_audit_event_append_only;

-- Every existing row in the order it was written (time, then the time-ordered ID), chain by chain.
DO $$
DECLARE
    r     record;
    cur   uuid;
    n     bigint;
    prev  bytea;
    zero  bytea := pg_catalog.decode(pg_catalog.repeat('00', 32), 'hex');
BEGIN
    cur := NULL;
    FOR r IN SELECT * FROM pandokos.audit_event ORDER BY tenant_id, occurred_at, id LOOP
        IF cur IS DISTINCT FROM r.tenant_id THEN
            IF cur IS NOT NULL THEN
                INSERT INTO pandokos.audit_chain (tenant_id, seq, hash) VALUES (cur, n, prev);
            END IF;
            cur := r.tenant_id; n := 0; prev := zero;
        END IF;
        n := n + 1;
        UPDATE pandokos.audit_event e
           SET seq = n, prev_hash = prev,
               hash = pandokos.audit_row_hash('tenant:' || r.tenant_id::text, n, r.id, r.occurred_at, r.actor_type,
                                              r.actor_id, r.actor_name, r.key_id, r.action, r.resource_type,
                                              r.resource_id, r.outcome, r.request_id, r.reason, r.support_action_id,
                                              r.detail, prev)
         WHERE e.tenant_id = r.tenant_id AND e.id = r.id
        RETURNING e.hash INTO prev;
    END LOOP;
    IF cur IS NOT NULL THEN
        INSERT INTO pandokos.audit_chain (tenant_id, seq, hash) VALUES (cur, n, prev);
    END IF;

    n := 0; prev := zero;
    FOR r IN SELECT * FROM pandokos.platform_audit_event ORDER BY occurred_at, id LOOP
        n := n + 1;
        UPDATE pandokos.platform_audit_event e
           SET seq = n, prev_hash = prev,
               hash = pandokos.audit_row_hash('platform', n, r.id, r.occurred_at, r.actor_type, r.actor_id,
                                              r.actor_name, r.key_id, r.action, r.resource_type, r.resource_id,
                                              r.outcome, r.request_id, r.reason, NULL, r.detail, prev)
         WHERE e.id = r.id
        RETURNING e.hash INTO prev;
    END LOOP;
    INSERT INTO pandokos.platform_audit_chain (id, seq, hash) VALUES (1, n, prev);
END
$$;

ALTER TABLE pandokos.audit_event          ENABLE TRIGGER audit_event_append_only;
ALTER TABLE pandokos.platform_audit_event ENABLE TRIGGER platform_audit_event_append_only;
ALTER TABLE pandokos.audit_event          FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.platform_audit_event FORCE ROW LEVEL SECURITY;
ALTER TABLE pandokos.audit_chain          FORCE ROW LEVEL SECURITY;

ALTER TABLE pandokos.audit_event
    ALTER COLUMN seq SET NOT NULL, ALTER COLUMN prev_hash SET NOT NULL, ALTER COLUMN hash SET NOT NULL,
    ADD CONSTRAINT audit_event_seq_key UNIQUE (tenant_id, seq),
    ADD CONSTRAINT audit_event_chain_valid CHECK (seq >= 1 AND octet_length(prev_hash) = 32 AND octet_length(hash) = 32);
ALTER TABLE pandokos.platform_audit_event
    ALTER COLUMN seq SET NOT NULL, ALTER COLUMN prev_hash SET NOT NULL, ALTER COLUMN hash SET NOT NULL,
    ADD CONSTRAINT platform_audit_event_seq_key UNIQUE (seq),
    ADD CONSTRAINT platform_audit_event_chain_valid CHECK (seq >= 1 AND octet_length(prev_hash) = 32 AND octet_length(hash) = 32);

-- ---------------------------------------------------------------------------------------------
-- 5. Every new row is sealed: its ID, its time, its place in the chain and its hash are the
--    database's, whoever writes it (the admin and worker roles, the functions of the gateway, a
--    migration). The head row is locked until the transaction ends, so the rows of one chain are
--    numbered in the order they commit. A row that is not inserted (a failed statement, a rolled
--    back transaction or subtransaction) takes its change of the head with it.
-- ---------------------------------------------------------------------------------------------

CREATE FUNCTION pandokos.audit_seal_tenant() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    prev   text := pg_catalog.current_setting('app.tenant_id', true);
    h_seq  bigint;
    h_hash bytea;
BEGIN
    NEW.id := pandokos.uuid_v7();
    NEW.occurred_at := pg_catalog.now();
    -- The head is read and written as the session of the row's tenant, whoever inserts (a
    -- migration or the owner may insert outside a tenant session). A row for another tenant than
    -- the session's is still refused by the policy of audit_event, after this trigger: the
    -- statement fails and its change of that head is rolled back with it.
    PERFORM pg_catalog.set_config('app.tenant_id', NEW.tenant_id::text, true);
    INSERT INTO pandokos.audit_chain (tenant_id) VALUES (NEW.tenant_id) ON CONFLICT (tenant_id) DO NOTHING;
    SELECT c.seq, c.hash INTO h_seq, h_hash FROM pandokos.audit_chain c WHERE c.tenant_id = NEW.tenant_id FOR UPDATE;
    NEW.seq := h_seq + 1;
    NEW.prev_hash := h_hash;
    NEW.hash := pandokos.audit_row_hash('tenant:' || NEW.tenant_id::text, NEW.seq, NEW.id, NEW.occurred_at,
                                        NEW.actor_type, NEW.actor_id, NEW.actor_name, NEW.key_id, NEW.action,
                                        NEW.resource_type, NEW.resource_id, NEW.outcome, NEW.request_id, NEW.reason,
                                        NEW.support_action_id, NEW.detail, NEW.prev_hash);
    UPDATE pandokos.audit_chain SET seq = NEW.seq, hash = NEW.hash WHERE tenant_id = NEW.tenant_id;
    PERFORM pg_catalog.set_config('app.tenant_id', COALESCE(prev, ''), true);
    RETURN NEW;
END
$$;

CREATE FUNCTION pandokos.audit_seal_platform() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    h_seq  bigint;
    h_hash bytea;
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
    RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION pandokos.audit_seal_tenant() FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.audit_seal_platform() FROM PUBLIC;

DROP TRIGGER audit_event_stamp ON pandokos.audit_event;
DROP TRIGGER platform_audit_event_stamp ON pandokos.platform_audit_event;
DROP FUNCTION pandokos.audit_stamp();

CREATE TRIGGER audit_event_seal BEFORE INSERT ON pandokos.audit_event
    FOR EACH ROW EXECUTE FUNCTION pandokos.audit_seal_tenant();
CREATE TRIGGER platform_audit_event_seal BEFORE INSERT ON pandokos.platform_audit_event
    FOR EACH ROW EXECUTE FUNCTION pandokos.audit_seal_platform();

-- A sealed row's BEFORE INSERT trigger runs before ON CONFLICT is decided, so the one function
-- that relied on ON CONFLICT ... DO NOTHING (migration 0011) now inserts in a subtransaction and
-- treats a duplicate as the row already written. Same signature, same answer.
CREATE OR REPLACE FUNCTION pandokos.record_unlimited_traffic(p_day date, p_time_zone text, p_provider text, p_model text,
                                                            p_request_id text) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    t       uuid := pandokos.current_tenant_id();
    d       jsonb;
    written boolean := false;
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
    RETURN written;
END
$$;

-- ---------------------------------------------------------------------------------------------
-- 6. Reading the logs (AUD-05). A tenant's session reads its own log; the platform scope of the
--    admin role reads the platform's.
-- ---------------------------------------------------------------------------------------------

GRANT SELECT ON pandokos.audit_event TO pandokos_admin;

CREATE POLICY platform_audit_event_select ON pandokos.platform_audit_event
    FOR SELECT TO pandokos_admin
    USING ((SELECT pandokos.platform_scope()));
GRANT SELECT ON pandokos.platform_audit_event TO pandokos_admin;

-- ---------------------------------------------------------------------------------------------
-- 7. The usage rollup (ADR 0064): what the ledger holds, by hour (UTC) of the request's start and
--    by every dimension a report or a limit can name, for the records recorded before the
--    watermark of the tenant. Derived and rebuilt from the ledger, so not append-only: the worker
--    adds to it and removes what retention removed from the ledger.
-- ---------------------------------------------------------------------------------------------

CREATE TABLE pandokos.usage_rollup (
    tenant_id               uuid           NOT NULL,
    hour                    timestamptz    NOT NULL,
    principal_kind          text           NOT NULL,
    principal_id            text           NOT NULL,
    key_id                  text           NOT NULL,
    provider                text           NOT NULL,
    model                   text           NOT NULL,
    model_id                text           NOT NULL,
    currency                text           NOT NULL,
    price_source            text           NOT NULL,
    outcome                 text           NOT NULL,
    credential              boolean        NOT NULL,
    requests                bigint         NOT NULL,
    input_tokens            bigint         NOT NULL,
    output_tokens           bigint         NOT NULL,
    cache_read_tokens       bigint         NOT NULL,
    cache_write_tokens      bigint         NOT NULL,
    reasoning_tokens        bigint         NOT NULL,
    estimated_input_tokens  bigint         NOT NULL,
    estimated_output_tokens bigint         NOT NULL,
    cost                    numeric(38,18) NOT NULL,
    cost_known              bigint         NOT NULL,
    cost_unknown            bigint         NOT NULL,
    CONSTRAINT usage_rollup_pkey PRIMARY KEY (tenant_id, hour, principal_kind, principal_id, key_id, provider, model,
                                              model_id, currency, price_source, outcome, credential),
    CONSTRAINT usage_rollup_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT usage_rollup_hour_valid CHECK (hour = pg_catalog.date_bin('1 hour', hour, '2000-01-01 00:00:00+00')),
    CONSTRAINT usage_rollup_currency_format CHECK (currency ~ '^([A-Z]{3})?$'),
    CONSTRAINT usage_rollup_price_source_valid CHECK (price_source IN ('', 'catalogue', 'tenant')),
    CONSTRAINT usage_rollup_values_valid CHECK (
        requests >= 1 AND input_tokens >= 0 AND output_tokens >= 0 AND cache_read_tokens >= 0
        AND cache_write_tokens >= 0 AND reasoning_tokens >= 0 AND estimated_input_tokens >= 0
        AND estimated_output_tokens >= 0 AND cost >= 0 AND cost_known BETWEEN 0 AND requests
        AND cost_unknown BETWEEN 0 AND requests)
);

ALTER TABLE pandokos.usage_rollup ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.usage_rollup FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.usage_rollup
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.usage_rollup TO pandokos_admin, pandokos_worker;
GRANT INSERT, UPDATE, DELETE ON pandokos.usage_rollup TO pandokos_worker;

-- How far the rollup of a tenant has gone: every record recorded before watermark is in it, none
-- recorded at or after it.
CREATE TABLE pandokos.usage_rollup_state (
    tenant_id  uuid        NOT NULL,
    watermark  timestamptz NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT usage_rollup_state_pkey PRIMARY KEY (tenant_id),
    CONSTRAINT usage_rollup_state_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id)
);

ALTER TABLE pandokos.usage_rollup_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.usage_rollup_state FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.usage_rollup_state
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.usage_rollup_state TO pandokos_admin, pandokos_worker;
GRANT INSERT (tenant_id, watermark), UPDATE (watermark, updated_at) ON pandokos.usage_rollup_state TO pandokos_worker;

-- The rollup reads the records of a tenant by the time they were recorded. On a large ledger this
-- statement builds one index per partition while the table takes no writes; the usage guide shows
-- how to build them beforehand without blocking (they are then attached, not rebuilt).
CREATE INDEX usage_record_recorded_idx ON pandokos.usage_record (tenant_id, recorded_at);

-- ---------------------------------------------------------------------------------------------
-- 8. Retention (USE-08, AUD-06). Two functions run by the worker role, both audited.
-- ---------------------------------------------------------------------------------------------

-- The owner removes rows of the platform log only inside platform_retention, and writes the two
-- retention actions there.
CREATE POLICY platform_audit_event_retention_select ON pandokos.platform_audit_event
    FOR SELECT TO pandokos_owner
    USING (pg_catalog.current_setting('pandokos.retention', true) = 'on');
CREATE POLICY platform_audit_event_retention_delete ON pandokos.platform_audit_event
    FOR DELETE TO pandokos_owner
    USING (pg_catalog.current_setting('pandokos.retention', true) = 'on');
CREATE POLICY platform_audit_event_retention_insert ON pandokos.platform_audit_event
    FOR INSERT TO pandokos_owner
    WITH CHECK (action IN ('usage.retention', 'audit.retention'));

-- In platform scope: drops the monthly partitions of the ledger that end before the start of the
-- current UTC month minus p_usage_months months, and removes the rows of the platform log written
-- before the start of the current UTC month minus p_audit_months months, from the start of the
-- chain: the anchor moves past them. 0 keeps everything of that kind. Each removal is recorded in
-- the platform log. Takes no name and no date from its caller.
CREATE FUNCTION pandokos.platform_retention(p_usage_months integer, p_audit_months integer,
                                           OUT partitions_dropped integer, OUT audit_rows bigint)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
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

-- In the session of a tenant: removes the tenant's rollup rows and the tenant's rows of the default
-- partition of the ledger whose hour or start is before the cut of p_usage_months, and the rows of
-- the tenant's log before the cut of p_audit_months, from the start of the chain. The removal of
-- ledger or log rows is recorded in the tenant's log.
CREATE FUNCTION pandokos.tenant_retention(p_usage_months integer, p_audit_months integer,
                                         OUT usage_rows bigint, OUT rollup_rows bigint, OUT audit_rows bigint)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
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

REVOKE ALL ON FUNCTION pandokos.platform_retention(integer, integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION pandokos.tenant_retention(integer, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.platform_retention(integer, integer) TO pandokos_worker;
GRANT EXECUTE ON FUNCTION pandokos.tenant_retention(integer, integer) TO pandokos_worker;

-- ---------------------------------------------------------------------------------------------
-- 9. The metadata keys a tenant accepts (PRV-03), as tenant_model_restriction does for models
--    (tenant-scoped table). No row: every key that has the format; a row: only the keys it lists.
--    The gateway reads it with the tenant's catalogue view and is told when it changes.
-- ---------------------------------------------------------------------------------------------

CREATE TABLE pandokos.metadata_key_policy (
    tenant_id  uuid        NOT NULL,
    id         uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    keys       text[]      NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT metadata_key_policy_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT metadata_key_policy_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT metadata_key_policy_tenant_key UNIQUE (tenant_id),
    CONSTRAINT metadata_key_policy_keys_valid CHECK (
        cardinality(keys) <= 64 AND array_position(keys, NULL) IS NULL
        AND (cardinality(keys) = 0 OR array_to_string(keys, ',') <> '')
        AND array_to_string(keys, ',') ~ '^([a-z0-9][a-z0-9_-]{0,31}(,[a-z0-9][a-z0-9_-]{0,31})*)?$')
);

CREATE TRIGGER metadata_key_policy_guard BEFORE INSERT OR UPDATE ON pandokos.metadata_key_policy
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();
CREATE TRIGGER metadata_key_policy_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.metadata_key_policy
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_catalog('scoped');

ALTER TABLE pandokos.metadata_key_policy ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.metadata_key_policy FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.metadata_key_policy
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.metadata_key_policy TO pandokos_gateway, pandokos_admin;
GRANT INSERT (tenant_id, keys) ON pandokos.metadata_key_policy TO pandokos_admin;
GRANT UPDATE (keys) ON pandokos.metadata_key_policy TO pandokos_admin;
GRANT DELETE ON pandokos.metadata_key_policy TO pandokos_admin;
