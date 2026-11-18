-- 0012_tenant_price_lists: the price lists of a tenant (its price versions and their rates, one
-- list per currency), the active currency of a tenant, one rule per currency in the two rule
-- tables, and the source of the price in the usage ledger (ADR 0053). Runs in one transaction as
-- pandokos_owner. Every name is schema-qualified. No existing row is rewritten.

-- ---------------------------------------------------------------------------------------------
-- 1. The active currency of a tenant.
-- ---------------------------------------------------------------------------------------------

-- active_currency is the currency every cost of the tenant is computed in ('' is the default of
-- the installation, pricing.default_currency). While active_currency_effective_from lies in the
-- future a change is pending: the currency in force is active_currency_previous until that instant
-- and active_currency from it.
ALTER TABLE pandokos.tenant
    ADD COLUMN active_currency                text        NOT NULL DEFAULT '',
    ADD COLUMN active_currency_previous       text        NOT NULL DEFAULT '',
    ADD COLUMN active_currency_effective_from timestamptz,
    ADD CONSTRAINT tenant_active_currency_format CHECK (
        active_currency ~ '^([A-Z]{3})?$' AND active_currency_previous ~ '^([A-Z]{3})?$');

-- The shape of a change, for every role: only a session of the tenant itself changes the currency
-- (the platform does not choose it); the currency in force changes only through a pending change
-- whose instant lies in the future (at most 32 days), and a pending change that is replaced keeps
-- its instant. The administration API computes the instant: the start of the next month in the
-- time zone in force, the instant of a time zone change.
CREATE FUNCTION pandokos.tenant_active_currency_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    pending  boolean;
    in_force text;
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.active_currency <> '' OR NEW.active_currency_previous <> ''
           OR NEW.active_currency_effective_from IS NOT NULL THEN
            RAISE EXCEPTION 'a tenant is created with the default currency' USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.active_currency = OLD.active_currency AND NEW.active_currency_previous = OLD.active_currency_previous
       AND NEW.active_currency_effective_from IS NOT DISTINCT FROM OLD.active_currency_effective_from THEN
        RETURN NEW;
    END IF;
    IF pandokos.current_tenant_id() IS DISTINCT FROM OLD.id THEN
        RAISE EXCEPTION 'only a session of the tenant changes its active currency'
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    pending := OLD.active_currency_effective_from IS NOT NULL AND now() < OLD.active_currency_effective_from;
    in_force := CASE WHEN pending THEN OLD.active_currency_previous ELSE OLD.active_currency END;
    IF NEW.active_currency_effective_from IS NULL THEN
        IF NEW.active_currency <> in_force THEN
            RAISE EXCEPTION 'the active currency in force changes only through a pending change'
                USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.active_currency_previous <> in_force THEN
        RAISE EXCEPTION 'active_currency_previous must be the currency in force' USING ERRCODE = 'check_violation';
    END IF;
    IF pending THEN
        IF NEW.active_currency_effective_from <> OLD.active_currency_effective_from THEN
            RAISE EXCEPTION 'a pending change of the active currency keeps its instant'
                USING ERRCODE = 'check_violation';
        END IF;
    ELSIF NEW.active_currency_effective_from <= now()
          OR NEW.active_currency_effective_from > now() + interval '32 days' THEN
        RAISE EXCEPTION 'a change of the active currency takes effect in the future, within 32 days'
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER tenant_active_currency_guard BEFORE INSERT OR UPDATE ON pandokos.tenant
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_active_currency_guard();

-- The gateway caches the active currency with the tenant's prices and with its limit rules.
CREATE FUNCTION pandokos.notify_tenant_currency() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    PERFORM pg_catalog.pg_notify('pandokos_cache', 'catalog:' || NEW.id::text);
    PERFORM pg_catalog.pg_notify('pandokos_cache', 'limits:' || NEW.id::text);
    RETURN NULL;
END
$$;

CREATE TRIGGER tenant_active_currency_notify AFTER UPDATE ON pandokos.tenant
    FOR EACH ROW WHEN (OLD.active_currency IS DISTINCT FROM NEW.active_currency
                       OR OLD.active_currency_previous IS DISTINCT FROM NEW.active_currency_previous
                       OR OLD.active_currency_effective_from IS DISTINCT FROM NEW.active_currency_effective_from)
    EXECUTE FUNCTION pandokos.notify_tenant_currency();

-- The policy tenant_self_update of migration 0011 lets the tenant's session update its row.
GRANT UPDATE (active_currency, active_currency_previous, active_currency_effective_from)
    ON pandokos.tenant TO pandokos_admin;

-- ---------------------------------------------------------------------------------------------
-- 2. One rule per subject, scope, metric and currency.
-- ---------------------------------------------------------------------------------------------

-- A budget can exist in a currency that is not the active one, next to the budget in the active
-- currency, to prepare a change. currency is '' for every metric that is not a cost.
ALTER TABLE pandokos.limit_rule
    DROP CONSTRAINT limit_rule_key,
    ADD CONSTRAINT limit_rule_key UNIQUE NULLS NOT DISTINCT
        (tenant_id, service_account_id, virtual_key_id, subject_mapping_id, provider_id, model_id, metric, currency);
ALTER TABLE pandokos.platform_limit_rule
    DROP CONSTRAINT platform_limit_rule_key,
    ADD CONSTRAINT platform_limit_rule_key UNIQUE NULLS NOT DISTINCT
        (tenant_id, provider_id, model_id, metric, currency);

-- ---------------------------------------------------------------------------------------------
-- 3. Price versions of a tenant (tenant-scoped tables). Only the tenant reads and writes them.
-- ---------------------------------------------------------------------------------------------

-- The versions of one currency are one price list of the tenant. A version never changes; it starts
-- now or later, never in the past; one that has not started can be removed. kind 'price' carries
-- rates. kind 'end' carries none: from its start the list does not price the model. The version of
-- a tenant, a currency and a model in force at t is the one with the greatest valid_from <= t. A
-- list is used only while its currency is the active currency of the tenant.
CREATE TABLE pandokos.tenant_price_version (
    tenant_id  uuid        NOT NULL,
    id         uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    model_id   uuid        NOT NULL,
    valid_from timestamptz NOT NULL DEFAULT now(),
    kind       text        NOT NULL DEFAULT 'price',
    currency   text        NOT NULL,
    note       text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tenant_price_version_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT tenant_price_version_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT tenant_price_version_model_fkey FOREIGN KEY (model_id) REFERENCES pandokos.model (id)
        ON DELETE RESTRICT,
    CONSTRAINT tenant_price_version_valid_from_key UNIQUE (tenant_id, currency, model_id, valid_from),
    CONSTRAINT tenant_price_version_kind_valid CHECK (kind IN ('price', 'end')),
    CONSTRAINT tenant_price_version_currency_format CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT tenant_price_version_note_length CHECK (char_length(note) <= 500)
);
-- For the referential check when a model is deleted.
CREATE INDEX tenant_price_version_model_idx ON pandokos.tenant_price_version (model_id);

-- The rules of a version that hold for every role.
CREATE FUNCTION pandokos.tenant_price_version_guard() RETURNS trigger
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
    IF OLD.valid_from <= now() THEN
        RAISE EXCEPTION 'a price version that has started cannot be removed' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN OLD;
END
$$;

CREATE TRIGGER tenant_price_version_guard BEFORE INSERT OR UPDATE OR DELETE ON pandokos.tenant_price_version
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_price_version_guard();
CREATE TRIGGER tenant_price_version_no_truncate BEFORE TRUNCATE ON pandokos.tenant_price_version
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only();
-- The gateway caches a tenant's prices with its model access: the same payload drops both.
CREATE TRIGGER tenant_price_version_notify AFTER INSERT OR DELETE ON pandokos.tenant_price_version
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_catalog('scoped');

-- Rates of a tenant's price version (tenant-scoped table), per million units of a category, with
-- the tiers of the catalogue's rates. Rows are written in the transaction of their version and
-- never afterwards: a version that exists does not gain a rate.
CREATE TABLE pandokos.tenant_price_rate (
    tenant_id        uuid            NOT NULL,
    price_version_id uuid            NOT NULL,
    category         text            NOT NULL,
    threshold        bigint          NOT NULL DEFAULT 0,
    rate             numeric(24, 12) NOT NULL,
    CONSTRAINT tenant_price_rate_pkey PRIMARY KEY (tenant_id, price_version_id, category, threshold),
    CONSTRAINT tenant_price_rate_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT tenant_price_rate_version_fkey FOREIGN KEY (tenant_id, price_version_id)
        REFERENCES pandokos.tenant_price_version (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT tenant_price_rate_category_valid CHECK (category IN ('input', 'output', 'cache_read', 'cache_write',
        'cache_write_1h', 'input_audio', 'output_audio')),
    CONSTRAINT tenant_price_rate_threshold_range CHECK (threshold BETWEEN 0 AND 100000000),
    CONSTRAINT tenant_price_rate_rate_range CHECK (rate >= 0 AND rate <= 1000000)
);

CREATE TRIGGER tenant_price_rate_immutable BEFORE UPDATE ON pandokos.tenant_price_rate
    FOR EACH ROW EXECUTE FUNCTION pandokos.immutable_row();
CREATE TRIGGER tenant_price_rate_no_truncate BEFORE TRUNCATE ON pandokos.tenant_price_rate
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only();

-- Checked at commit, for the version a row belongs to (TG_ARGV[0]: 'version' or 'rate'): a price
-- has at least one rate and a base rate (threshold 0) for every category it prices; an end has no
-- rate; and the rates were written in the transaction that created the version (created_at is the
-- start of that transaction, set by the guard above). The administration API validates all of it
-- first; a violation here is a defect, and its SQLSTATE (23000) maps to no answer of the API.
CREATE FUNCTION pandokos.tenant_price_shape() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_tenant  uuid := NEW.tenant_id;
    v_version uuid;
    v         record;
    n         bigint;
BEGIN
    IF TG_ARGV[0] = 'version' THEN
        v_version := NEW.id;
    ELSE
        v_version := NEW.price_version_id;
    END IF;
    SELECT t.kind, t.created_at INTO v
      FROM pandokos.tenant_price_version t
     WHERE t.tenant_id = v_tenant AND t.id = v_version;
    IF NOT FOUND THEN
        -- Created and removed in one transaction: the rates went with it.
        RETURN NULL;
    END IF;
    IF TG_ARGV[0] = 'rate' AND v.created_at <> now() THEN
        RAISE EXCEPTION 'rates are written with their price version' USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    SELECT count(*) INTO n FROM pandokos.tenant_price_rate r
     WHERE r.tenant_id = v_tenant AND r.price_version_id = v_version;
    IF v.kind = 'end' THEN
        IF n > 0 THEN
            RAISE EXCEPTION 'the end of a price list has no rates' USING ERRCODE = 'integrity_constraint_violation';
        END IF;
        RETURN NULL;
    END IF;
    IF n = 0 THEN
        RAISE EXCEPTION 'a price version needs at least one rate' USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF EXISTS (SELECT 1 FROM pandokos.tenant_price_rate r
                WHERE r.tenant_id = v_tenant AND r.price_version_id = v_version
                GROUP BY r.category
               HAVING min(r.threshold) > 0) THEN
        RAISE EXCEPTION 'every category with a rate needs a rate at threshold 0'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER tenant_price_version_shape AFTER INSERT ON pandokos.tenant_price_version
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_price_shape('version');
CREATE CONSTRAINT TRIGGER tenant_price_rate_shape AFTER INSERT ON pandokos.tenant_price_rate
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION pandokos.tenant_price_shape('rate');

ALTER TABLE pandokos.tenant_price_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.tenant_price_version FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pandokos.tenant_price_version
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

ALTER TABLE pandokos.tenant_price_rate ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.tenant_price_rate FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pandokos.tenant_price_rate
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

-- The gateway and the administration API read and the administration API writes, always in the
-- session of the tenant: no policy shows a row to the platform scope. The worker holds nothing:
-- counters are rebuilt from recorded costs, not from prices.
GRANT SELECT ON pandokos.tenant_price_version TO pandokos_gateway, pandokos_admin;
GRANT INSERT (tenant_id, model_id, valid_from, kind, currency, note)
    ON pandokos.tenant_price_version TO pandokos_admin;
GRANT DELETE ON pandokos.tenant_price_version TO pandokos_admin;

GRANT SELECT ON pandokos.tenant_price_rate TO pandokos_gateway, pandokos_admin;
GRANT INSERT (tenant_id, price_version_id, category, threshold, rate) ON pandokos.tenant_price_rate TO pandokos_admin;

-- ---------------------------------------------------------------------------------------------
-- 4. The source of the price in the usage ledger.
-- ---------------------------------------------------------------------------------------------

-- Which table price_version_id names: 'catalogue' (price_version) or 'tenant'
-- (tenant_price_version). NULL: the record has no price version, or it was written before this
-- migration, when every version was the catalogue's. The column has no default and the check is NOT
-- VALID, so no partition is rewritten or scanned; every new row is checked.
ALTER TABLE pandokos.usage_record ADD COLUMN price_source text;
ALTER TABLE pandokos.usage_record ADD CONSTRAINT usage_record_price_source_valid
    CHECK (price_source IS NULL
           OR (price_source IN ('catalogue', 'tenant') AND price_version_id IS NOT NULL)) NOT VALID;

GRANT INSERT (price_source) ON pandokos.usage_record TO pandokos_gateway;
