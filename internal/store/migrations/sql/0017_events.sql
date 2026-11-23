-- 0017_events: the external reference of a tenant; the event log of a tenant (outbox), written in
-- the transaction that records the fact; webhook destinations with the state of their delivery;
-- dead letters; the claim of the relay and its state; retention of events and of limit events;
-- the event log of the platform; and the erasure of all of it with the tenant (ADR 0070, ADR 0071,
-- ADR 0072).
-- Runs in one transaction as pandokos_owner. Every name is schema-qualified.

-- ---------------------------------------------------------------------------------------------
-- 1. The external reference of a tenant: what the operator's other systems call it. Set by the
--    platform, carried by every event, copied into the deletion request.
-- ---------------------------------------------------------------------------------------------

ALTER TABLE pandokos.tenant
    ADD COLUMN external_reference text NOT NULL DEFAULT '',
    ADD CONSTRAINT tenant_external_reference_format
        CHECK (char_length(external_reference) <= 255 AND external_reference !~ '[[:cntrl:]]');
GRANT INSERT (external_reference), UPDATE (external_reference) ON pandokos.tenant TO pandokos_admin;

-- The reference is the platform's: the administration role may update the row of the tenant of
-- its session (time zone, active currency), but not this column.
CREATE FUNCTION pandokos.tenant_external_reference_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NEW.external_reference IS DISTINCT FROM OLD.external_reference AND NOT pandokos.platform_scope() THEN
        RAISE EXCEPTION 'the external reference of a tenant changes in the platform scope' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END
$$;
REVOKE ALL ON FUNCTION pandokos.tenant_external_reference_guard() FROM PUBLIC;
CREATE TRIGGER tenant_external_reference_guard BEFORE UPDATE OF external_reference ON pandokos.tenant
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_external_reference_guard();

ALTER TABLE pandokos.tenant_deletion ADD COLUMN external_reference text NOT NULL DEFAULT '';
GRANT SELECT (external_reference) ON pandokos.tenant_deletion TO pandokos_admin;

-- The request keeps the reference the tenant has when it is made: the event "tenant deleted" is
-- written when the tenant's configuration is gone.
CREATE OR REPLACE FUNCTION pandokos.tenant_deletion_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_ref text;
BEGIN
    SELECT x.external_reference INTO v_ref FROM pandokos.tenant x WHERE x.id = NEW.tenant_id AND x.state = 'suspended';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'a deletion is requested for a suspended tenant' USING ERRCODE = 'restrict_violation', CONSTRAINT = 'tenant_deletion_suspended';
    END IF;
    NEW.state := 'scheduled';
    NEW.step := 'begin';
    NEW.requested_at := pg_catalog.now();
    NEW.external_reference := v_ref;
    RETURN NEW;
END
$$;

-- A timestamp as the events carry it: RFC 3339, UTC, microseconds.
CREATE FUNCTION pandokos.rfc3339(p timestamptz) RETURNS text
    LANGUAGE sql STABLE PARALLEL SAFE
    SET search_path = pg_catalog, pg_temp
    RETURN pg_catalog.to_char(pg_catalog.timezone('UTC', p), 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"');
REVOKE ALL ON FUNCTION pandokos.rfc3339(timestamptz) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------
-- 2. The event log of a tenant. A row is an event: appended in the transaction that records the
--    fact, by the triggers of section 3 and by nothing else; never changed; removed by retention
--    and with the tenant. seq grows in the order of the commits (the row of outbox_stream is
--    locked from the first event of a transaction to its commit), so a reader that asks for
--    "seq > n" never misses a row. It is not gapless.
-- ---------------------------------------------------------------------------------------------

CREATE TABLE pandokos.outbox_stream (
    tenant_id uuid   NOT NULL,
    last_seq  bigint NOT NULL DEFAULT 0,
    CONSTRAINT outbox_stream_pkey PRIMARY KEY (tenant_id),
    CONSTRAINT outbox_stream_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT outbox_stream_last_seq_valid CHECK (last_seq >= 0)
);

ALTER TABLE pandokos.outbox_stream ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.outbox_stream FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pandokos.outbox_stream
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));
CREATE TRIGGER outbox_stream_tenant_open BEFORE INSERT ON pandokos.outbox_stream
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();
GRANT SELECT ON pandokos.outbox_stream TO pandokos_admin, pandokos_worker;

CREATE TABLE pandokos.outbox_event (
    tenant_id   uuid        NOT NULL,
    seq         bigint      NOT NULL,
    id          uuid        NOT NULL,
    type        text        NOT NULL,
    subject     text        NOT NULL DEFAULT '',
    occurred_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now(),
    data        jsonb       NOT NULL,
    CONSTRAINT outbox_event_pkey PRIMARY KEY (tenant_id, seq),
    CONSTRAINT outbox_event_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT outbox_event_seq_valid CHECK (seq >= 1),
    CONSTRAINT outbox_event_type_format CHECK (type ~ '^io\.pandokos\.[a-z][a-z_]*(\.[a-z][a-z_]*)+\.v[1-9][0-9]?$'),
    CONSTRAINT outbox_event_subject_length CHECK (char_length(subject) <= 255),
    CONSTRAINT outbox_event_data_object CHECK (jsonb_typeof(data) = 'object')
);

CREATE TRIGGER outbox_event_append_only BEFORE UPDATE OR DELETE ON pandokos.outbox_event
    FOR EACH ROW EXECUTE FUNCTION pandokos.append_only();
CREATE TRIGGER outbox_event_no_truncate BEFORE TRUNCATE ON pandokos.outbox_event
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only();
CREATE TRIGGER outbox_event_tenant_open BEFORE INSERT ON pandokos.outbox_event
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();

ALTER TABLE pandokos.outbox_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.outbox_event FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pandokos.outbox_event
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

-- The admin role reads (the export); the worker reads (the relay). No runtime role writes.
GRANT SELECT ON pandokos.outbox_event TO pandokos_admin, pandokos_worker;

-- Reserves p_count sequence numbers of a tenant and returns the one before the first. The row
-- stays locked until the transaction ends: that is what orders the log.
CREATE FUNCTION pandokos.outbox_seq_reserve(p_tenant uuid, p_count bigint) RETURNS bigint
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_last bigint;
BEGIN
    INSERT INTO pandokos.outbox_stream AS s (tenant_id, last_seq)
    VALUES (p_tenant, p_count)
    ON CONFLICT (tenant_id) DO UPDATE SET last_seq = s.last_seq + EXCLUDED.last_seq
    RETURNING s.last_seq INTO v_last;
    RETURN v_last - p_count;
END
$$;
REVOKE ALL ON FUNCTION pandokos.outbox_seq_reserve(uuid, bigint) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------
-- 3. The facts that are events of a tenant: a usage record and a limit event. The event is
--    written by a trigger of the table that holds the fact, so no writer can record one without
--    the other, and a row that a conflict left out has no event.
-- ---------------------------------------------------------------------------------------------

-- One event per usage record, in the statement that inserts the records (the batch of the
-- ledger writer): one reservation and one insert per statement. The data of
-- io.pandokos.usage.recorded.v1 is the record without its provider-specific usage object; it
-- holds no content, because the ledger has none. The expression is written here and not in a
-- function of its own: a function with a search_path is called once per row.
CREATE FUNCTION pandokos.usage_record_outbox() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    v_prev     text := pg_catalog.current_setting('app.tenant_id', true);
    v_tenant   uuid;
    v_count    bigint;
    v_base     bigint;
BEGIN
    -- The rows of a statement of a runtime role are all of the tenant of its session (the policy
    -- of usage_record). A superuser, whom no policy binds, may insert without a session or for
    -- several tenants: the log of each tenant is then written in a session of that tenant.
    FOR v_tenant, v_count IN SELECT r.tenant_id, pg_catalog.count(*) FROM inserted r GROUP BY r.tenant_id LOOP
        PERFORM pg_catalog.set_config('app.tenant_id', v_tenant::text, true);
        v_base := pandokos.outbox_seq_reserve(v_tenant, v_count);
        INSERT INTO pandokos.outbox_event (tenant_id, seq, id, type, subject, occurred_at, data)
        SELECT r.tenant_id, v_base + pg_catalog.row_number() OVER (ORDER BY r.started_at, r.id), r.id,
               'io.pandokos.usage.recorded.v1', r.request_id, r.recorded_at,
               pg_catalog.jsonb_strip_nulls(pg_catalog.jsonb_build_object(
                   'record_id', r.id,
                   'request_id', r.request_id,
                   'trace_id', NULLIF(r.trace_id, ''),
                   'started_at', pg_catalog.to_char(pg_catalog.timezone('UTC', r.started_at), 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
                   'duration_ms', r.duration_ms,
                   'principal', pg_catalog.jsonb_build_object('kind', r.principal_kind, 'id', r.principal_id),
                   'key_id', NULLIF(r.key_id, ''),
                   'provider', r.provider,
                   'api_family', r.api_family,
                   'operation', r.operation,
                   'model', NULLIF(r.model, ''),
                   'upstream_model', NULLIF(r.upstream_model, ''),
                   'stream', r.stream,
                   'outcome', r.outcome,
                   'error_code', NULLIF(r.error_code, ''),
                   'usage', pg_catalog.jsonb_build_object(
                       'input_tokens', r.input_tokens, 'output_tokens', r.output_tokens,
                       'cache_read_tokens', r.cache_read_tokens, 'cache_write_tokens', r.cache_write_tokens,
                       'reasoning_tokens', r.reasoning_tokens,
                       'input_source', r.input_source, 'output_source', r.output_source,
                       'other_units', NULLIF(r.other_units, '{}'::jsonb)),
                   'cost', pg_catalog.jsonb_build_object(
                       'status', r.cost_status, 'reason', NULLIF(r.cost_reason, ''),
                       'amount', pg_catalog.trim_scale(r.cost)::text, 'currency', r.currency,
                       'price_version_id', r.price_version_id, 'price_source', r.price_source,
                       'fallbacks', CASE WHEN r.cost_fallbacks <> '{}'::text[] THEN pg_catalog.to_jsonb(r.cost_fallbacks) END),
                   'limit_state', r.limit_state,
                   'metadata', NULLIF(r.metadata, '{}'::jsonb)))
          FROM inserted r
         WHERE r.tenant_id = v_tenant;
    END LOOP;
    PERFORM pg_catalog.set_config('app.tenant_id', COALESCE(v_prev, ''), true);
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN NULL;
END
$$;
REVOKE ALL ON FUNCTION pandokos.usage_record_outbox() FROM PUBLIC;
CREATE TRIGGER usage_record_outbox AFTER INSERT ON pandokos.usage_record
    REFERENCING NEW TABLE AS inserted
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.usage_record_outbox();

-- One event per limit event (5.2): io.pandokos.limit.threshold_reached.v1 or
-- io.pandokos.limit.exhausted.v1, with the ID of the limit_event row.
CREATE FUNCTION pandokos.limit_event_outbox() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    v_prev     text := pg_catalog.current_setting('app.tenant_id', true);
    v_cost     boolean := NEW.metric IN ('cost_per_day', 'cost_per_month');
BEGIN
    PERFORM pg_catalog.set_config('app.tenant_id', NEW.tenant_id::text, true);
    INSERT INTO pandokos.outbox_event (tenant_id, seq, id, type, subject, occurred_at, data)
    VALUES (NEW.tenant_id, pandokos.outbox_seq_reserve(NEW.tenant_id, 1) + 1, NEW.id,
            'io.pandokos.limit.' || NEW.kind || '.v1', NEW.rule_id::text, NEW.occurred_at,
            pg_catalog.jsonb_strip_nulls(pg_catalog.jsonb_build_object(
                'limit_event_id', NEW.id,
                'rule_id', NEW.rule_id,
                'rule_level', NEW.rule_level,
                'subject', pg_catalog.jsonb_build_object('kind', NEW.subject_kind, 'id', NULLIF(NEW.subject_id, '')),
                'provider', NULLIF(NEW.provider, ''),
                'model', NULLIF(NEW.model, ''),
                'metric', NEW.metric,
                'currency', NULLIF(NEW.currency, ''),
                'period', pg_catalog.jsonb_build_object('start', pandokos.rfc3339(NEW.period_start), 'end', pandokos.rfc3339(NEW.period_end)),
                'percent', CASE WHEN NEW.kind = 'threshold_reached' THEN NEW.percent END,
                'limit', CASE WHEN NOT v_cost THEN NEW.limit_value END,
                'amount', CASE WHEN v_cost THEN pg_catalog.trim_scale(NEW.limit_value::numeric / 1000000)::text END,
                'observed', pg_catalog.trim_scale(NEW.observed)::text)));
    PERFORM pg_catalog.set_config('app.tenant_id', COALESCE(v_prev, ''), true);
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN NULL;
END
$$;
REVOKE ALL ON FUNCTION pandokos.limit_event_outbox() FROM PUBLIC;
CREATE TRIGGER limit_event_outbox AFTER INSERT ON pandokos.limit_event
    FOR EACH ROW EXECUTE FUNCTION pandokos.limit_event_outbox();

-- ---------------------------------------------------------------------------------------------
-- 4. Webhook destinations of a tenant, with the state of their delivery. The administration
--    writes the configuration, the worker the state; column grants keep them apart, and the
--    admin role cannot read a sealed secret (as for provider credentials). cursor_seq is the
--    event up to which everything is settled for the destination: delivered, parked as a dead
--    letter, not of its types, or removed by retention. A test request is asked by the
--    administration (test_requested_at, test_event_id) and sent by the worker.
-- ---------------------------------------------------------------------------------------------

CREATE TABLE pandokos.event_destination (
    tenant_id                   uuid        NOT NULL,
    id                          uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    name                        text        NOT NULL,
    description                 text        NOT NULL DEFAULT '',
    url                         text        NOT NULL,
    event_types                 text[]      NOT NULL DEFAULT '{}',
    enabled                     boolean     NOT NULL DEFAULT true,
    secret                      bytea       NOT NULL,
    secret_key_version          integer     NOT NULL,
    secret_created_at           timestamptz NOT NULL DEFAULT now(),
    previous_secret             bytea,
    previous_secret_key_version integer,
    previous_secret_expires_at  timestamptz,
    created_at                  timestamptz NOT NULL DEFAULT now(),
    updated_at                  timestamptz NOT NULL DEFAULT now(),
    cursor_seq                  bigint      NOT NULL DEFAULT 0,
    failures                    integer     NOT NULL DEFAULT 0,
    failing_since               timestamptz,
    next_attempt_at             timestamptz NOT NULL DEFAULT now(),
    lease_token                 uuid,
    lease_until                 timestamptz,
    last_success_at             timestamptz,
    last_failure_at             timestamptz,
    last_error                  text        NOT NULL DEFAULT '',
    last_status                 integer,
    expired_events              bigint      NOT NULL DEFAULT 0,
    test_requested_at           timestamptz,
    test_event_id               uuid,
    test_completed_at           timestamptz,
    test_outcome                text        NOT NULL DEFAULT '',
    test_status                 integer,
    CONSTRAINT event_destination_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT event_destination_id_key UNIQUE (id),
    CONSTRAINT event_destination_name_key UNIQUE (tenant_id, name),
    CONSTRAINT event_destination_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT event_destination_name_format CHECK (name ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    CONSTRAINT event_destination_description_length CHECK (char_length(description) <= 500),
    CONSTRAINT event_destination_url_format CHECK (char_length(url) <= 2048 AND url ~ '^https?://[^[:space:][:cntrl:]]+$'),
    CONSTRAINT event_destination_event_types_valid CHECK (
        cardinality(event_types) <= 16
        AND event_types <@ ARRAY['io.pandokos.usage.recorded.v1', 'io.pandokos.limit.threshold_reached.v1',
                                 'io.pandokos.limit.exhausted.v1']::text[]),
    CONSTRAINT event_destination_secret_length CHECK (octet_length(secret) BETWEEN 30 AND 512),
    CONSTRAINT event_destination_previous_secret CHECK (
        (previous_secret IS NULL) = (previous_secret_key_version IS NULL)
        AND (previous_secret IS NULL) = (previous_secret_expires_at IS NULL)
        AND (previous_secret IS NULL OR octet_length(previous_secret) BETWEEN 30 AND 512)),
    CONSTRAINT event_destination_state_valid CHECK (
        cursor_seq >= 0 AND failures >= 0 AND expired_events >= 0
        AND (failures = 0) = (failing_since IS NULL)
        AND (lease_token IS NULL) = (lease_until IS NULL)
        AND last_error ~ '^[a-z_]{0,40}$'
        AND (last_status IS NULL OR last_status BETWEEN 100 AND 599)),
    CONSTRAINT event_destination_test_valid CHECK (
        (test_requested_at IS NULL) = (test_event_id IS NULL)
        AND test_outcome ~ '^[a-z_]{0,40}$'
        AND (test_status IS NULL OR test_status BETWEEN 100 AND 599))
);

ALTER TABLE pandokos.event_destination ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.event_destination FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pandokos.event_destination
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));
CREATE TRIGGER event_destination_tenant_open BEFORE INSERT ON pandokos.event_destination
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();

-- A destination starts at the head of the log: it receives the events recorded after it was
-- created. A tenant has at most 8. When the secret is replaced, the trigger keeps the replaced one
-- as the previous secret if the statement gives it an expiry, and drops it otherwise: the
-- administration, which cannot read the column, never copies a sealed secret itself.
CREATE FUNCTION pandokos.event_destination_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        PERFORM pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('pandokos.event_destination:' || NEW.tenant_id::text, 0));
        IF (SELECT pg_catalog.count(*) FROM pandokos.event_destination d WHERE d.tenant_id = NEW.tenant_id) >= 8 THEN
            RAISE EXCEPTION 'a tenant has at most 8 event destinations' USING ERRCODE = 'check_violation', CONSTRAINT = 'event_destination_limit';
        END IF;
        NEW.created_at := pg_catalog.now();
        NEW.updated_at := NEW.created_at;
        NEW.secret_created_at := NEW.created_at;
        NEW.cursor_seq := COALESCE((SELECT s.last_seq FROM pandokos.outbox_stream s WHERE s.tenant_id = NEW.tenant_id), 0);
        NEW.failures := 0;
        NEW.failing_since := NULL;
        NEW.next_attempt_at := NEW.created_at;
        NEW.lease_token := NULL;
        NEW.lease_until := NULL;
        NEW.expired_events := 0;
        RETURN NEW;
    END IF;
    IF NEW.secret IS DISTINCT FROM OLD.secret THEN
        NEW.secret_created_at := pg_catalog.now();
        IF NEW.previous_secret_expires_at IS NOT NULL THEN
            NEW.previous_secret := OLD.secret;
            NEW.previous_secret_key_version := OLD.secret_key_version;
        END IF;
    END IF;
    IF NEW.previous_secret_expires_at IS NULL THEN
        NEW.previous_secret := NULL;
        NEW.previous_secret_key_version := NULL;
    END IF;
    RETURN NEW;
END
$$;
REVOKE ALL ON FUNCTION pandokos.event_destination_guard() FROM PUBLIC;
CREATE TRIGGER event_destination_guard BEFORE INSERT OR UPDATE ON pandokos.event_destination
    FOR EACH ROW EXECUTE FUNCTION pandokos.event_destination_guard();

-- The worker reads everything, the secrets included: it signs. The administration reads
-- everything but the two sealed secrets.
GRANT SELECT ON pandokos.event_destination TO pandokos_worker;
GRANT SELECT (tenant_id, id, name, description, url, event_types, enabled, secret_key_version, secret_created_at,
              previous_secret_key_version, previous_secret_expires_at, created_at, updated_at, cursor_seq, failures,
              failing_since, next_attempt_at, lease_token, lease_until, last_success_at, last_failure_at, last_error,
              last_status, expired_events, test_requested_at, test_event_id, test_completed_at, test_outcome, test_status)
    ON pandokos.event_destination TO pandokos_admin;
GRANT INSERT (tenant_id, id, name, description, url, event_types, enabled, secret, secret_key_version)
    ON pandokos.event_destination TO pandokos_admin;
-- The administration changes the configuration, replaces the secret, asks for a test, moves the
-- cursor back (replay) and, with a replay or an enabling, releases the lease and the back-off.
GRANT UPDATE (name, description, url, event_types, enabled, secret, secret_key_version, previous_secret_expires_at,
              updated_at, cursor_seq, failures, failing_since, next_attempt_at, lease_token, lease_until,
              test_requested_at, test_event_id)
    ON pandokos.event_destination TO pandokos_admin;
GRANT DELETE ON pandokos.event_destination TO pandokos_admin;
-- The worker settles a round of delivery and a test, and nothing else.
GRANT UPDATE (cursor_seq, failures, failing_since, next_attempt_at, lease_token, lease_until,
              last_success_at, last_failure_at, last_error, last_status, test_completed_at, test_outcome, test_status)
    ON pandokos.event_destination TO pandokos_worker;

-- Events that a destination refused max_attempts times while it was at the head of its queue:
-- parked, shown, and delivered again when a replay is asked ('replay'). The event itself stays
-- in the log until retention removes it, and the dead letter with it.
CREATE TABLE pandokos.outbox_dead_letter (
    tenant_id           uuid        NOT NULL,
    destination_id      uuid        NOT NULL,
    seq                 bigint      NOT NULL,
    event_id            uuid        NOT NULL,
    type                text        NOT NULL,
    state               text        NOT NULL DEFAULT 'dead',
    attempts            integer     NOT NULL,
    reason              text        NOT NULL,
    status              integer,
    parked_at           timestamptz NOT NULL DEFAULT now(),
    replay_requested_at timestamptz,
    CONSTRAINT outbox_dead_letter_pkey PRIMARY KEY (tenant_id, destination_id, seq),
    CONSTRAINT outbox_dead_letter_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT outbox_dead_letter_destination_fkey FOREIGN KEY (tenant_id, destination_id)
        REFERENCES pandokos.event_destination (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT outbox_dead_letter_state_valid CHECK (state IN ('dead', 'replay')),
    CONSTRAINT outbox_dead_letter_values_valid CHECK (
        seq >= 1 AND attempts >= 1 AND reason ~ '^[a-z_]{1,40}$' AND (status IS NULL OR status BETWEEN 100 AND 599))
);
CREATE INDEX outbox_dead_letter_replay_idx ON pandokos.outbox_dead_letter (tenant_id, destination_id) WHERE state = 'replay';

ALTER TABLE pandokos.outbox_dead_letter ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.outbox_dead_letter FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pandokos.outbox_dead_letter
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));
CREATE TRIGGER outbox_dead_letter_tenant_open BEFORE INSERT ON pandokos.outbox_dead_letter
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.tenant_open();

GRANT SELECT ON pandokos.outbox_dead_letter TO pandokos_admin, pandokos_worker;
GRANT UPDATE (state, replay_requested_at) ON pandokos.outbox_dead_letter TO pandokos_admin;
GRANT INSERT (tenant_id, destination_id, seq, event_id, type, attempts, reason, status) ON pandokos.outbox_dead_letter TO pandokos_worker;
GRANT UPDATE (state, attempts, reason, status, parked_at) ON pandokos.outbox_dead_letter TO pandokos_worker;
GRANT DELETE ON pandokos.outbox_dead_letter TO pandokos_worker;

-- Which tenants have destinations (global table, identifiers only, like the other directories):
-- what the claim of the relay walks. Written by the trigger below and by the erasure; no runtime
-- role holds a privilege on it.
CREATE TABLE pandokos.event_destination_directory (
    destination_id uuid NOT NULL,
    tenant_id      uuid NOT NULL,
    CONSTRAINT event_destination_directory_pkey PRIMARY KEY (destination_id),
    CONSTRAINT event_destination_directory_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id)
);
CREATE INDEX event_destination_directory_tenant_idx ON pandokos.event_destination_directory (tenant_id);


CREATE FUNCTION pandokos.event_destination_directory_sync() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM pandokos.event_destination_directory d WHERE d.destination_id = OLD.id;
    ELSE
        INSERT INTO pandokos.event_destination_directory (destination_id, tenant_id) VALUES (NEW.id, NEW.tenant_id);
    END IF;
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN NULL;
END
$$;
REVOKE ALL ON FUNCTION pandokos.event_destination_directory_sync() FROM PUBLIC;
CREATE TRIGGER event_destination_directory_sync AFTER INSERT OR DELETE ON pandokos.event_destination
    FOR EACH ROW EXECUTE FUNCTION pandokos.event_destination_directory_sync();

-- In platform scope: gives the caller, for p_lease_seconds, up to p_limit destinations that are
-- not leased and either have a test request waiting, or are enabled, due, and have an event
-- after their cursor or a dead letter to replay.
-- It answers identifiers only. A lease that is not settled expires by itself: another worker
-- then delivers the same events again (at least once). Workers never wait for each other
-- (SKIP LOCKED).
CREATE FUNCTION pandokos.outbox_relay_claim(p_token uuid, p_lease_seconds integer, p_limit integer)
    RETURNS TABLE (tenant_id uuid, destination_id uuid)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    v_prev     text := pg_catalog.current_setting('app.tenant_id', true);
    v_left     integer := p_limit;
    v_count    integer;
    t          uuid;
BEGIN
    IF NOT pandokos.platform_scope() THEN
        RAISE EXCEPTION 'outbox_relay_claim needs the platform scope' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF p_token IS NULL OR p_lease_seconds IS NULL OR p_lease_seconds NOT BETWEEN 10 AND 600
       OR p_limit IS NULL OR p_limit NOT BETWEEN 1 AND 1000 THEN
        RAISE EXCEPTION 'invalid claim' USING ERRCODE = 'check_violation';
    END IF;
    FOR t IN SELECT x.tenant_id FROM (SELECT DISTINCT d.tenant_id FROM pandokos.event_destination_directory d) x
              ORDER BY pg_catalog.random() LOOP
        PERFORM pg_catalog.set_config('app.tenant_id', t::text, true);
        -- The rows are chosen once (MATERIALIZED): a subquery with LIMIT and FOR UPDATE inside an IN
        -- is planned as a semi-join that may run it again, and the claim would then take more than
        -- the limit.
        RETURN QUERY
        WITH due AS MATERIALIZED (
            SELECT y.id
              FROM pandokos.event_destination y
             WHERE y.tenant_id = t
               AND (y.lease_until IS NULL OR y.lease_until <= pg_catalog.now())
               AND ((y.test_requested_at IS NOT NULL
                     AND (y.test_completed_at IS NULL OR y.test_completed_at < y.test_requested_at))
                    OR (y.enabled AND y.next_attempt_at <= pg_catalog.now()
                        AND (y.cursor_seq < COALESCE((SELECT s.last_seq FROM pandokos.outbox_stream s WHERE s.tenant_id = t), 0)
                             OR EXISTS (SELECT 1 FROM pandokos.outbox_dead_letter l
                                         WHERE l.tenant_id = t AND l.destination_id = y.id AND l.state = 'replay'))))
             ORDER BY y.next_attempt_at
             LIMIT v_left
               FOR UPDATE SKIP LOCKED)
        UPDATE pandokos.event_destination u
           SET lease_token = p_token, lease_until = pg_catalog.now() + pg_catalog.make_interval(secs => p_lease_seconds)
          FROM due
         WHERE u.tenant_id = t AND u.id = due.id
        RETURNING u.tenant_id, u.id;
        GET DIAGNOSTICS v_count = ROW_COUNT;
        v_left := v_left - v_count;
        EXIT WHEN v_left <= 0;
    END LOOP;
    PERFORM pg_catalog.set_config('app.tenant_id', COALESCE(v_prev, ''), true);
    PERFORM pandokos.definer_settings_restore(v_settings);
END
$$;
REVOKE ALL ON FUNCTION pandokos.outbox_relay_claim(uuid, integer, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.outbox_relay_claim(uuid, integer, integer) TO pandokos_worker;

-- In platform scope: the state of delivery over the installation, for the metrics. It answers
-- counts and one age, never an identifier and nothing of one tenant.
CREATE FUNCTION pandokos.outbox_relay_state(OUT destinations bigint, OUT failing bigint, OUT disabled bigint,
                                           OUT dead_letters bigint, OUT oldest_pending_seconds double precision)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    v_prev     text := pg_catalog.current_setting('app.tenant_id', true);
    t          uuid;
    r          record;
BEGIN
    IF NOT pandokos.platform_scope() THEN
        RAISE EXCEPTION 'outbox_relay_state needs the platform scope' USING ERRCODE = 'insufficient_privilege';
    END IF;
    destinations := 0;
    failing := 0;
    disabled := 0;
    dead_letters := 0;
    oldest_pending_seconds := 0;
    FOR t IN SELECT DISTINCT d.tenant_id FROM pandokos.event_destination_directory d LOOP
        PERFORM pg_catalog.set_config('app.tenant_id', t::text, true);
        SELECT pg_catalog.count(*) FILTER (WHERE d.enabled) AS enabled,
               pg_catalog.count(*) FILTER (WHERE d.enabled AND d.failures > 0) AS failing,
               pg_catalog.count(*) FILTER (WHERE NOT d.enabled) AS disabled,
               COALESCE(pg_catalog.max(EXTRACT(EPOCH FROM pg_catalog.now() - (
                   SELECT o.recorded_at FROM pandokos.outbox_event o
                    WHERE o.tenant_id = t AND o.seq > d.cursor_seq ORDER BY o.seq LIMIT 1))) FILTER (WHERE d.enabled), 0) AS oldest
          INTO r
          FROM pandokos.event_destination d
         WHERE d.tenant_id = t;
        destinations := destinations + r.enabled;
        failing := failing + r.failing;
        disabled := disabled + r.disabled;
        oldest_pending_seconds := GREATEST(oldest_pending_seconds, r.oldest::double precision);
        dead_letters := dead_letters + (SELECT pg_catalog.count(*) FROM pandokos.outbox_dead_letter l WHERE l.tenant_id = t AND l.state = 'dead');
    END LOOP;
    PERFORM pg_catalog.set_config('app.tenant_id', COALESCE(v_prev, ''), true);
    PERFORM pandokos.definer_settings_restore(v_settings);
END
$$;
REVOKE ALL ON FUNCTION pandokos.outbox_relay_state() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.outbox_relay_state() TO pandokos_worker;

-- ---------------------------------------------------------------------------------------------
-- 5. Retention. In the session of a tenant: removes one batch of the events recorded more than
--    p_event_hours ago, from the oldest; moves past them the cursor of every destination that had
--    not passed them, counting them as expired for the enabled ones; removes the dead letters of
--    removed events; and, with p_usage_months > 0, the limit events older than the retention of
--    usage (their rule and period are then long closed). The worker calls it until events < p_batch.
-- ---------------------------------------------------------------------------------------------

CREATE FUNCTION pandokos.tenant_outbox_retention(p_event_hours integer, p_usage_months integer, p_batch integer,
                                               OUT events bigint, OUT expired bigint, OUT limit_events bigint)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
    t      uuid := pandokos.current_tenant_id();
    v_cut  timestamptz := pg_catalog.now() - pg_catalog.make_interval(hours => p_event_hours);
    v_seqs bigint[];
    v_max  bigint;
    v_day  date;
BEGIN
    IF t IS NULL THEN
        RAISE EXCEPTION 'tenant_outbox_retention needs a tenant session' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF p_event_hours IS NULL OR p_event_hours NOT BETWEEN 1 AND 2160
       OR p_usage_months IS NULL OR (p_usage_months <> 0 AND p_usage_months NOT BETWEEN 2 AND 120)
       OR p_batch IS NULL OR p_batch NOT BETWEEN 100 AND 50000 THEN
        RAISE EXCEPTION 'invalid retention' USING ERRCODE = 'check_violation';
    END IF;
    expired := 0;
    limit_events := 0;
    PERFORM pg_catalog.set_config('pandokos.retention', 'on', true);
    WITH oldest AS (SELECT o.seq, o.recorded_at FROM pandokos.outbox_event o WHERE o.tenant_id = t ORDER BY o.seq LIMIT p_batch),
         gone AS (DELETE FROM pandokos.outbox_event e USING oldest x
                   WHERE e.tenant_id = t AND e.seq = x.seq AND x.recorded_at < v_cut
                  RETURNING e.seq)
    SELECT COALESCE(pg_catalog.array_agg(g.seq), '{}'), pg_catalog.max(g.seq) INTO v_seqs, v_max FROM gone g;
    events := pg_catalog.cardinality(v_seqs);
    IF events > 0 THEN
        WITH behind AS (
            SELECT d.id, d.enabled, (SELECT pg_catalog.count(*) FROM pg_catalog.unnest(v_seqs) s WHERE s > d.cursor_seq) AS n
              FROM pandokos.event_destination d
             WHERE d.tenant_id = t AND d.cursor_seq < v_max
               FOR UPDATE OF d),
             moved AS (
            UPDATE pandokos.event_destination d
               SET cursor_seq = v_max,
                   expired_events = d.expired_events + CASE WHEN b.enabled THEN b.n ELSE 0 END
              FROM behind b
             WHERE d.tenant_id = t AND d.id = b.id
            RETURNING CASE WHEN b.enabled THEN b.n ELSE 0 END AS n)
        SELECT COALESCE(pg_catalog.sum(m.n), 0) INTO expired FROM moved m;
        DELETE FROM pandokos.outbox_dead_letter l WHERE l.tenant_id = t AND l.seq = ANY (v_seqs);
    END IF;
    IF p_usage_months > 0 THEN
        v_day := (pg_catalog.date_trunc('month', pg_catalog.timezone('UTC', pg_catalog.now()))::date
                  - pg_catalog.make_interval(months => p_usage_months))::date;
        DELETE FROM pandokos.limit_event e WHERE e.tenant_id = t AND e.occurred_at < (v_day::timestamp AT TIME ZONE 'UTC');
        GET DIAGNOSTICS limit_events = ROW_COUNT;
    END IF;
    PERFORM pg_catalog.set_config('pandokos.retention', 'off', true);
    IF limit_events > 0 THEN
        INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_name, action, resource_type, outcome, detail)
        VALUES (t, 'platform', 'retention', 'limit_event.retention', 'limit_event', 'success',
                pg_catalog.jsonb_build_object('before', pg_catalog.to_char(v_day, 'YYYY-MM-DD'),
                                              'months', p_usage_months, 'deleted', limit_events));
    END IF;
END
$$;
REVOKE ALL ON FUNCTION pandokos.tenant_outbox_retention(integer, integer, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pandokos.tenant_outbox_retention(integer, integer, integer) TO pandokos_worker;

-- ---------------------------------------------------------------------------------------------
-- 6. The event log of the platform (global table): what happened to tenants and to the
--    key-encryption key, without a figure of usage. Written by the functions and triggers below,
--    read by the administration in platform scope, never changed and not removed.
-- ---------------------------------------------------------------------------------------------

CREATE TABLE pandokos.platform_outbox_stream (
    id       smallint NOT NULL DEFAULT 1,
    last_seq bigint   NOT NULL DEFAULT 0,
    CONSTRAINT platform_outbox_stream_pkey PRIMARY KEY (id),
    CONSTRAINT platform_outbox_stream_one CHECK (id = 1 AND last_seq >= 0)
);
INSERT INTO pandokos.platform_outbox_stream (id) VALUES (1);

CREATE TABLE pandokos.platform_outbox_event (
    seq         bigint      NOT NULL,
    id          uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    type        text        NOT NULL,
    subject     text        NOT NULL DEFAULT '',
    occurred_at timestamptz NOT NULL DEFAULT now(),
    tenant_id   uuid,
    data        jsonb       NOT NULL,
    CONSTRAINT platform_outbox_event_pkey PRIMARY KEY (seq),
    CONSTRAINT platform_outbox_event_id_key UNIQUE (id),
    CONSTRAINT platform_outbox_event_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT platform_outbox_event_seq_valid CHECK (seq >= 1),
    CONSTRAINT platform_outbox_event_type_format CHECK (type ~ '^io\.pandokos\.[a-z][a-z_]*(\.[a-z][a-z_]*)+\.v[1-9][0-9]?$'),
    CONSTRAINT platform_outbox_event_subject_length CHECK (char_length(subject) <= 255),
    CONSTRAINT platform_outbox_event_data_object CHECK (jsonb_typeof(data) = 'object')
);
CREATE TRIGGER platform_outbox_event_append_only BEFORE UPDATE OR DELETE ON pandokos.platform_outbox_event
    FOR EACH ROW EXECUTE FUNCTION pandokos.append_only();
CREATE TRIGGER platform_outbox_event_no_truncate BEFORE TRUNCATE ON pandokos.platform_outbox_event
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only();

ALTER TABLE pandokos.platform_outbox_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.platform_outbox_event FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_outbox_event_owner ON pandokos.platform_outbox_event
    FOR ALL TO pandokos_owner USING (true) WITH CHECK (true);
CREATE POLICY platform_outbox_event_select ON pandokos.platform_outbox_event
    FOR SELECT TO pandokos_admin USING ((SELECT pandokos.platform_scope()));
GRANT SELECT ON pandokos.platform_outbox_event TO pandokos_admin;

-- Appends an event of the platform. The head of the platform's audit chain is locked first: a
-- transaction that writes both always takes the two locks in this order.
CREATE FUNCTION pandokos.platform_outbox_append(p_type text, p_tenant uuid, p_subject text, p_data jsonb) RETURNS void
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_seq bigint;
BEGIN
    PERFORM 1 FROM pandokos.platform_audit_chain c WHERE c.id = 1 FOR UPDATE;
    UPDATE pandokos.platform_outbox_stream SET last_seq = last_seq + 1 WHERE id = 1 RETURNING last_seq INTO v_seq;
    INSERT INTO pandokos.platform_outbox_event (seq, type, subject, tenant_id, data)
    VALUES (v_seq, p_type, p_subject, p_tenant, pg_catalog.jsonb_strip_nulls(p_data));
END
$$;
REVOKE ALL ON FUNCTION pandokos.platform_outbox_append(text, uuid, text, jsonb) FROM PUBLIC;

-- A deletion requested, cancelled (the scheduled row is removed) and completed (the statement of
-- tenant_erasure_step that writes the completion): each is an event in the same transaction.
CREATE FUNCTION pandokos.tenant_deletion_outbox() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
BEGIN
    IF TG_OP = 'INSERT' THEN
        PERFORM pandokos.platform_outbox_append('io.pandokos.tenant.deletion_requested.v1', NEW.tenant_id, NEW.tenant_id::text,
            pg_catalog.jsonb_build_object('tenant_id', NEW.tenant_id, 'external_reference', NULLIF(NEW.external_reference, ''),
                                          'requested_at', pandokos.rfc3339(NEW.requested_at), 'not_before', pandokos.rfc3339(NEW.not_before)));
    ELSIF TG_OP = 'DELETE' THEN
        PERFORM pandokos.platform_outbox_append('io.pandokos.tenant.deletion_cancelled.v1', OLD.tenant_id, OLD.tenant_id::text,
            pg_catalog.jsonb_build_object('tenant_id', OLD.tenant_id, 'external_reference', NULLIF(OLD.external_reference, ''),
                                          'requested_at', pandokos.rfc3339(OLD.requested_at)));
    ELSIF NEW.state = 'completed' AND OLD.state <> 'completed' THEN
        PERFORM pandokos.platform_outbox_append('io.pandokos.tenant.deleted.v1', NEW.tenant_id, NEW.tenant_id::text,
            pg_catalog.jsonb_build_object('tenant_id', NEW.tenant_id, 'external_reference', NULLIF(NEW.external_reference, ''),
                                          'requested_at', pandokos.rfc3339(NEW.requested_at), 'started_at', pandokos.rfc3339(NEW.started_at),
                                          'completed_at', pandokos.rfc3339(NEW.completed_at),
                                          'kek_versions', pg_catalog.to_jsonb(NEW.kek_versions),
                                          'completion_seq', NEW.completion_seq));
    END IF;
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN NULL;
END
$$;
REVOKE ALL ON FUNCTION pandokos.tenant_deletion_outbox() FROM PUBLIC;
CREATE TRIGGER tenant_deletion_outbox AFTER INSERT OR DELETE OR UPDATE OF state ON pandokos.tenant_deletion
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_deletion_outbox();

-- A key-encryption key version that becomes active, and one that is retired (from then on the
-- backups taken under it cannot yield a deleted tenant's credentials once its file is destroyed).
-- kek_retire writes a retired version as a new row after removing the retiring one: that insert is
-- the retirement.
CREATE FUNCTION pandokos.kek_version_outbox() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_settings text[] := pandokos.definer_settings_off();
BEGIN
    IF NEW.state = 'active' AND (TG_OP = 'INSERT' OR OLD.state <> 'active') THEN
        PERFORM pandokos.platform_outbox_append('io.pandokos.kek.version_activated.v1', NULL, NEW.id,
            pg_catalog.jsonb_build_object('version', NEW.id, 'provider', NEW.provider));
    ELSIF NEW.state = 'retired' AND (TG_OP = 'INSERT' OR OLD.state <> 'retired') THEN
        PERFORM pandokos.platform_outbox_append('io.pandokos.kek.version_retired.v1', NULL, NEW.id,
            pg_catalog.jsonb_build_object('version', NEW.id, 'provider', NEW.provider));
    END IF;
    PERFORM pandokos.definer_settings_restore(v_settings);
    RETURN NULL;
END
$$;
REVOKE ALL ON FUNCTION pandokos.kek_version_outbox() FROM PUBLIC;
CREATE TRIGGER kek_version_outbox AFTER INSERT OR UPDATE OF state ON pandokos.kek_version
    FOR EACH ROW EXECUTE FUNCTION pandokos.kek_version_outbox();

-- ---------------------------------------------------------------------------------------------
-- 7. The erasure of a tenant removes its destinations with the access (their secrets are sealed
--    under the data key, and a webhook is a way out for the tenant's data) and its event log
--    with the configuration. Events not delivered when the deletion begins are removed with it.
--    The bodies are those of migration 0016 with the new tables.
-- ---------------------------------------------------------------------------------------------

CREATE OR REPLACE FUNCTION pandokos.tenant_residue() RETURNS jsonb
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
                               'tenant_role', 'outbox_dead_letter', 'event_destination', 'event_destination_directory',
                               'tenant_data_key', 'provider_credential', 'platform_limit_rule',
                               'model_tenant_grant', 'tenant_model_restriction', 'metadata_key_policy', 'tenant_price_rate',
                               'tenant_price_version', 'limit_event', 'usage_rollup', 'usage_rollup_state',
                               'outbox_event', 'outbox_stream'] LOOP
        EXECUTE pg_catalog.format('SELECT pg_catalog.count(*) FROM pandokos.%I x WHERE x.tenant_id = $1', tab) INTO n USING t;
        IF n > 0 THEN
            out := out || pg_catalog.jsonb_build_object(tab, n);
        END IF;
    END LOOP;
    RETURN out;
END
$$;

CREATE OR REPLACE FUNCTION pandokos.tenant_erasure_access() RETURNS jsonb
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
    DELETE FROM pandokos.outbox_dead_letter;
    DELETE FROM pandokos.event_destination;                                 GET DIAGNOSTICS n = ROW_COUNT; out := out || pg_catalog.jsonb_build_object('event_destination', n);
    DELETE FROM pandokos.event_destination_directory d WHERE d.tenant_id = t;
    RETURN out;
END
$$;

CREATE OR REPLACE FUNCTION pandokos.tenant_erasure_configuration() RETURNS jsonb
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
    DELETE FROM pandokos.outbox_event;
    PERFORM pg_catalog.set_config('pandokos.retention', 'off', true);
    -- A transaction that began before the deletion and has not ended holds the row of the stream:
    -- the step does not wait for it; the row and the events of that transaction are found by
    -- tenant_residue, and the second pass removes them.
    DELETE FROM pandokos.outbox_stream s
     WHERE s.tenant_id IN (SELECT x.tenant_id FROM pandokos.outbox_stream x WHERE x.tenant_id = t FOR UPDATE SKIP LOCKED);
    DELETE FROM pandokos.usage_rollup;
    DELETE FROM pandokos.usage_rollup_state;
    RETURN out;
END
$$;
