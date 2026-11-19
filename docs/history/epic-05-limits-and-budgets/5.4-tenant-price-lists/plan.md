# Sprint 5.4 — Implementation plan

- Design: [sprint.md](sprint.md). Section numbers (§) refer to it.
- Each part below is self-contained: an agent needs sprint.md and that part only. Parts run in
  order A, B, C1 … C8, D, E, F.
- Suggested grouping for implementers: **G1** = C1, C2, C3 (`internal/pricing`, the rule set,
  the catalogue cache, the resolver, the gateway's pricing); **G2** = C5, C6 (the
  administration API); **G3** = C4 (logs, spans, configuration); **G4** = C7 (CLI). After part B
  the four do not depend on each other. C8 is one agent after them. A, B, D, E and F are one
  agent each. Because the risk class is high, C2 (which budgets apply), C3 (what a request is
  priced with) and the change rule of C6 are reviewed by an agent that did not write them, with
  §5.2, §5.3 and §5.6 as the checklist.

## Rules for the implementer

1. Work on the current branch (`claude/epic-5-implementation-cu4vxo`); do not create a branch.
   Sign off every commit (`git commit -s`), without any AI attribution. One commit for A, one
   for B (tests, failing), one per step of C (message given with the step), one for D, one for
   E, one for F if something is found.
2. **No new Go module**, no new error code, no `SECURITY DEFINER` function, no permission of
   platform scope. The three permissions of §5.5 and the two configuration keys of §5.14 are
   the only new ones.
3. Appendices A1 (migration 0012) and A2 (`tenant_prices.sql`) are copied **byte for byte**. A1
   was run on PostgreSQL 16.15, A2 compiled with `sqlc` v1.31.1 and A3 was run through `apigen`
   while designing. The SHA-256 of A1 as a file is given with it; if it differs after copying,
   the copy is wrong. If one of them does not work as written, stop and ask. A merged migration
   is immutable (ADR 0026): until the pull request merges, a correction changes the file and its
   line in `migrations.sum` together.
4. **The platform reads and writes nothing of a tenant's prices, usage or limit events.** No
   handler of a platform-scoped operation, and no code inside `db.Platform` or `inPlatform`,
   calls a statement on `tenant_price_version`, `tenant_price_rate`, `usage_record` or
   `limit_event`, and none opens a tenant's session to do so (T-R52, T-747). The currency check
   of a platform ceiling reads the `tenant` row and nothing else of the tenant.
5. **One definition each**: `pricing.List.At` for the price in force (no other code compares a
   tenant's version with a catalogue version or a price's currency with the active one;
   `pricing.VersionAt` is not called outside `internal/pricing`, T-R51); `RuleSet.For` for which
   limits apply (the resolver and the handlers do not filter by currency themselves);
   `budgetCurrencies` for the currencies of a scope; `limits.NextChange` for the instant of a
   change.
6. **Never convert, never add two currencies** — in code, tests, fixtures and documentation.
7. **The database is the guard, the handler is the path.** The handler validates before it
   writes; do not weaken a trigger, a check or a foreign key to make a test pass. SQLSTATE 23000
   or 42501 from a price table or from the currency columns in a handler test means the handler
   skipped a validation or ran in the wrong session.
8. **Tenant scope.** Every statement runs as `queries.New(tx).<Method>` inside a `store`
   transaction helper (T-R10, T-R12). No statement on the two price tables names the tenant in a
   `WHERE` clause; `LockTenantActiveCurrency` and `SetTenantActiveCurrency` address the `tenant`
   row by ID under its policies, in the tenant's session.
9. In `internal/limits` only `rules.go` and `load.go` change. The gateway does not import
   `internal/limits` (T-R40); `internal/limits` does not import `internal/ledger` or
   `internal/admin` (T-R43). The importer and `internal/admin/prices.go` are not changed.
10. Logs and spans: the fields of §5.11 are written only behind
    `telemetry.tenant_usage_details`. No new log line carries an amount, a token count, a rate
    or a currency of a tenant. The `note` of a version goes to its row and nowhere else. No
    body, header value, credential or model output; no real key in a test, fixture or document.
11. Generated files are never edited by hand (`make generate`). Every new route declares its
    permission in the specification and gets its cross-tenant test in the same commit as its
    handler. Every handler that changes something writes its audit record in the transaction of
    the change.
12. Tests hold for root and non-root; nothing sleeps longer than 2 s; waiting is
    `testutil.Eventually`; time-dependent logic takes a clock. A bound is never made to pass by
    lengthening it and a database test never by skipping it. A property test that fails is fixed
    in the code or reported; its seed is never pinned to a passing one.
13. A gap in sprint.md or in this plan is a question to the orchestrator, not a decision.

## Environment

```sh
set -a; . local/testdb/env; set +a     # the test database runs in Docker; do not run make db-up or db-down
make generate                           # apigen and sqlc
go test -race -count=1 ./...            # everything but the SDK suites
make check                              # what a pull request must pass
```

If the test database cannot be reached, stop and report; do not skip database tests. It could
not be reached from the design session: appendix A1 was verified on another PostgreSQL 16.15
without a pooler, so the pooled halves of the isolation tests run for the first time in part A.

## Part A — Scaffolding (commit: `chore: sprint 5.4 scaffolding: migration 0012, queries, API specification, permissions, configuration keys, types`)

Goal: the schema, the queries, the specification, the permissions, the configuration keys and the
generated code of the sprint exist and the tree builds; behaviour is that of 5.3 wherever a
later part adds something.

Files:

1. `internal/store/migrations/sql/0012_tenant_price_lists.sql` — appendix A1, byte for byte; its
   line in `internal/store/migrations/migrations.sum` (`make migrations-sum`).
2. `internal/store/queries/sql/tenant_prices.sql` — appendix A2, the whole file.
3. `internal/store/queries/sql/usage.sql`, `tenants.sql`, `limits.sql` — appendix A2b.
4. `api/openapi/admin.v1.yaml` — appendix A3.
5. `test/isolation/spec.go` — appendix A4.
6. `internal/authz/authz.go` — three constants and catalogue entries after the limit
   permissions, all of tenant scope, added to `tenant-admin` only:

   | Constant | Name | Description |
   |---|---|---|
   | `PriceListRead` | `price_list:read` | Reading the tenant's price lists and their versions, and the tenant's active currency |
   | `PriceListWrite` | `price_list:write` | Adding a price version to one of the tenant's price lists; choosing the tenant's active currency |
   | `PriceListDelete` | `price_list:delete` | Removing a price version of the tenant's lists that has not started |

   The description of `model:read` becomes "Listing the models available to the tenant, with
   the prices that apply to it; reading the tenant's model restriction"; that of `tenant:read`
   adds ", with their time zone and active currency".
7. `internal/config`: `pricing.default_currency` (string, default `USD`, validated as three
   capital letters) and `telemetry.tenant_usage_details` (boolean, default false; when true a
   warning in the list `serve` already logs as `configuration warning`: "tenant usage details
   (cost, currency, token counts) are written to logs and traces"), with their environment
   variables and the reference output of `pandokos config`.
8. `make generate`.
9. Mechanical adaptation so that `go build ./... && go vet ./...` pass, and nothing more:
   - `internal/pricing`: `Version.End bool`; `Cost.Source string`; `list.go` with the constants,
     `Active`, `Active.At`, `List`, `List.At` and `List.Cost` of §5.2 — for now `At` ignores
     `Currency` and `Tenant` and answers from `Catalogue` with `SourceCatalogue` (C1).
   - `internal/limits`: `RuleSet.Currency pricing.Active` (zero; C2 fills and uses it).
   - `internal/catalog`: `TenantAccess.Currency` and `.Prices` (zero and nil);
     `CacheOptions.DefaultCurrency`; `Cache.ResolveIn` and the `source` interface of the resolver
     return the `TenantAccess` between the snapshot and the model.
   - `internal/gateway`: `Target.Currency`, `Target.TenantPrices`, `Target.PriceList()`,
     `LimitRefusal.PriceSource`; an option `UsageDetails bool` of the handler (true for now, so
     that the log line and the spans are those of 5.3 until C4); `estimate.go` and `record.go`
     call `PriceList().Cost(…)` and keep `Source` where they replace a cost that does not fit.
   - `internal/ledger/rows.go`: `PriceSource *string` (JSON `price_source`, null when
     `Cost.VersionID` is empty).
   - `internal/admin`: the tenant conversions read the three new columns into `active_currency`,
     `active_currency_effective_from` and, for now, `active_currency_in_force` equal to
     `active_currency`; `usage.go` fills `price_source`; `budget_currency.go` takes the signature
     of C5 with the behaviour of 5.3; rule conversions set `active` true; `tenant_prices.go` and
     `active_currency.go` register the seven operations, each returning the 500 that the
     unimplemented operations of 5.3 part A returned; `Options` gains `DefaultCurrency`.
   - `cmd/pandokos/serve.go` passes the two configuration values where the options exist.
   - `test/dbtest/seeds.go`: `SeedTenantPrice(tb, tenant, currency, provider, model string,
     validFrom time.Time, rates ...Rate) string` (one transaction on a superuser connection:
     version, then rates; without rates `input` 1 and `output` 2, as `SeedPrice`; a zero
     `validFrom` is now), `SeedTenantPriceEnd(tb, tenant, currency, provider, model, validFrom)
     string`, `SetTenantCurrency(tb, tenant, currency, previous string, effectiveFrom
     time.Time)` written as `SetTenantZone` is (platform scope set, `session_replication_role =
     replica`, then a `pg_notify` of `catalog:<tenant>` and `limits:<tenant>`, because the
     triggers are off).

Verification:

```sh
make generate && go build ./... && go vet ./...
make generate-check                     # once the generated code is staged
sha256sum internal/store/migrations/sql/0012_tenant_price_lists.sql
go test -count=1 ./internal/store/... ./test/isolation -run 'TestMigrat|TestSchemaLint|TestCrossTenantNegative|TestTenantTimeZonePolicies|TestLimitRuleIntegrity'
```

`TestSchemaLint` must report nothing and the other four must pass in both modes.
`TestDefaultSpecMatchesDesign`, the permission tests and the tests over every operation fail from
here on: part B fixes them.

### Appendix A1 — `internal/store/migrations/sql/0012_tenant_price_lists.sql`

SHA-256 of the file (it ends with one newline after the last statement):
`b603f39aa7e324c200a0f04fae056d4227f0b81bb9d02199fb1b1ff1e48e4b00`.

```sql
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
```

### Appendix A2 — `internal/store/queries/sql/tenant_prices.sql`

```sql
-- Tenant price lists and the active currency of a tenant (migration 0012). The versions of a
-- tenant in one currency are one list. Every statement on the two price tables runs in the
-- session of the tenant and names no tenant in a WHERE clause: the policies do (TEN-04), and no
-- policy shows a row to the platform scope. The statements on the tenant row address it by
-- pandokos.current_tenant_id() or, to lock and update it, by ID under its policies. Rates come back
-- as three parallel arrays ordered by category and threshold, and travel as decimal text.

-- The active currency of the tenant of the session: the currency that is or will be in force, the
-- one in force until effective_from when a change is pending ('' is the default of the
-- installation). Gateway pool (with the tenant's prices, and with its limit rules), admin pool.

-- name: GetTenantActiveCurrency :one
SELECT t.active_currency, t.active_currency_previous, t.active_currency_effective_from
  FROM pandokos.tenant t
 WHERE t.id = pandokos.current_tenant_id();

-- Admin pool, session of the tenant. The row is locked: two changes do not interleave. db_now is
-- the time the trigger of the table compares with; the time zone columns give the calendar the
-- instant of a change is computed in.

-- name: LockTenantActiveCurrency :one
SELECT t.id, t.active_currency, t.active_currency_previous, t.active_currency_effective_from,
       t.time_zone, t.time_zone_previous, t.time_zone_effective_from, now()::timestamptz AS db_now
  FROM pandokos.tenant t
 WHERE t.id = sqlc.arg(id)::uuid
   FOR UPDATE;

-- name: SetTenantActiveCurrency :one
UPDATE pandokos.tenant
   SET active_currency                = sqlc.arg(active_currency)::text,
       active_currency_previous       = sqlc.arg(active_currency_previous)::text,
       active_currency_effective_from = sqlc.narg(active_currency_effective_from)::timestamptz
 WHERE id = sqlc.arg(id)::uuid
RETURNING id, active_currency, active_currency_previous, active_currency_effective_from;

-- Whether a currency has a list the tenant of the session could be priced with: a price of its
-- own in that currency, or a catalogue price in it for a model the tenant can be given; in both
-- cases a version in force now or starting later.

-- name: CurrencyHasList :one
SELECT (EXISTS (SELECT 1 FROM pandokos.tenant_price_version v
                 WHERE v.currency = sqlc.arg(currency)::text AND v.kind = 'price'
                   AND (v.valid_from > now()
                        OR v.valid_from = (SELECT max(c.valid_from) FROM pandokos.tenant_price_version c
                                            WHERE c.currency = v.currency AND c.model_id = v.model_id
                                              AND c.valid_from <= now())))
        OR EXISTS (SELECT 1 FROM pandokos.price_version v
                     JOIN pandokos.model mo ON mo.id = v.model_id
                    WHERE v.currency = sqlc.arg(currency)::text
                      AND (mo.availability = 'all'
                           OR EXISTS (SELECT 1 FROM pandokos.model_tenant_grant g WHERE g.model_id = mo.id))
                      AND (v.valid_from > now()
                           OR v.valid_from = (SELECT max(c.valid_from) FROM pandokos.price_version c
                                               WHERE c.model_id = v.model_id AND c.valid_from <= now()))))::boolean
       AS has_list;

-- The lock of every statement that adds to the lists of the tenant of the session, so that the
-- start of a new version is read after the versions another writer committed.

-- name: LockTenantPrices :exec
SELECT pg_catalog.pg_advisory_xact_lock(
           pg_catalog.hashtextextended('pandokos.tenant_price:' || pandokos.current_tenant_id()::text, 0));

-- name: CountTenantPriceVersions :one
SELECT count(*)::bigint FROM pandokos.tenant_price_version;

-- The lists of the tenant: one row per currency, with the number of versions and of the models
-- that have a price in force now.

-- name: ListTenantPriceLists :many
SELECT v.currency, count(*)::bigint AS versions,
       count(*) FILTER (WHERE v.kind = 'price'
                          AND v.valid_from = (SELECT max(c.valid_from) FROM pandokos.tenant_price_version c
                                               WHERE c.currency = v.currency AND c.model_id = v.model_id
                                                 AND c.valid_from <= now()))::bigint AS models_priced
  FROM pandokos.tenant_price_version v
 GROUP BY v.currency
 ORDER BY v.currency;

-- The model a version names, when the tenant of the session can see it: the provider is enabled,
-- the model is not retired, and it is available to all or granted to the tenant (the policy of
-- model_tenant_grant shows the tenant's grants only). No row for a model elsewhere or nowhere.

-- name: ResolveTenantPriceModel :one
SELECT m.id, p.id AS provider_id, p.name AS provider, m.name
  FROM pandokos.model m
  JOIN pandokos.provider p ON p.id = m.provider_id
 WHERE p.name = sqlc.arg(provider)::text
   AND m.name = sqlc.arg(model)::text
   AND p.enabled
   AND m.status <> 'retired'
   AND (m.availability = 'all' OR EXISTS (SELECT 1 FROM pandokos.model_tenant_grant g WHERE g.model_id = m.id));

-- name: CreateTenantPriceVersion :one
INSERT INTO pandokos.tenant_price_version (tenant_id, model_id, valid_from, kind, currency, note)
VALUES (pandokos.current_tenant_id(), sqlc.arg(model_id)::uuid, sqlc.arg(valid_from)::timestamptz,
        sqlc.arg(kind)::text, sqlc.arg(currency)::text, sqlc.arg(note)::text)
RETURNING id, valid_from, created_at;

-- name: CreateTenantPriceRates :execrows
INSERT INTO pandokos.tenant_price_rate (tenant_id, price_version_id, category, threshold, rate)
SELECT pandokos.current_tenant_id(), sqlc.arg(price_version_id)::uuid, (sqlc.arg(categories)::text[])[i.n],
       (sqlc.arg(thresholds)::bigint[])[i.n], ((sqlc.arg(rates)::text[])[i.n])::numeric
  FROM generate_series(1, cardinality(sqlc.arg(categories)::text[])) AS i (n);

-- in_force: the version is the one of its list and model in force now.

-- name: GetTenantPriceVersion :one
SELECT v.id, v.model_id, p.name AS provider, m.name AS model, v.valid_from, v.kind, v.currency, v.note, v.created_at,
       COALESCE(v.valid_from = (SELECT max(c.valid_from) FROM pandokos.tenant_price_version c
                                 WHERE c.currency = v.currency AND c.model_id = v.model_id
                                   AND c.valid_from <= now()), false)::boolean AS in_force,
       COALESCE(array_agg(r.category ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS categories,
       COALESCE(array_agg(r.threshold ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::bigint[] AS thresholds,
       COALESCE(array_agg(r.rate::text ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS rates
  FROM pandokos.tenant_price_version v
  JOIN pandokos.model m ON m.id = v.model_id
  JOIN pandokos.provider p ON p.id = m.provider_id
  LEFT JOIN pandokos.tenant_price_rate r ON r.tenant_id = v.tenant_id AND r.price_version_id = v.id
 WHERE v.id = sqlc.arg(id)::uuid
 GROUP BY v.tenant_id, v.id, p.name, m.name;

-- Newest first (the ID is a UUID of version 7). With at, only the versions in force at that
-- instant, one per list and model.

-- name: ListTenantPriceVersions :many
SELECT v.id, v.model_id, p.name AS provider, m.name AS model, v.valid_from, v.kind, v.currency, v.note, v.created_at,
       COALESCE(v.valid_from = (SELECT max(c.valid_from) FROM pandokos.tenant_price_version c
                                 WHERE c.currency = v.currency AND c.model_id = v.model_id
                                   AND c.valid_from <= now()), false)::boolean AS in_force,
       COALESCE(array_agg(r.category ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS categories,
       COALESCE(array_agg(r.threshold ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::bigint[] AS thresholds,
       COALESCE(array_agg(r.rate::text ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS rates
  FROM pandokos.tenant_price_version v
  JOIN pandokos.model m ON m.id = v.model_id
  JOIN pandokos.provider p ON p.id = m.provider_id
  LEFT JOIN pandokos.tenant_price_rate r ON r.tenant_id = v.tenant_id AND r.price_version_id = v.id
 WHERE (sqlc.arg(currency)::text = '' OR v.currency = sqlc.arg(currency)::text)
   AND (sqlc.arg(provider)::text = '' OR p.name = sqlc.arg(provider)::text)
   AND (sqlc.arg(model)::text = '' OR m.name = sqlc.arg(model)::text)
   AND (sqlc.narg(at)::timestamptz IS NULL
        OR v.valid_from = (SELECT max(c.valid_from) FROM pandokos.tenant_price_version c
                            WHERE c.currency = v.currency AND c.model_id = v.model_id
                              AND c.valid_from <= sqlc.narg(at)::timestamptz))
   AND (NOT sqlc.arg(after)::boolean OR v.id < sqlc.arg(after_id)::uuid)
 GROUP BY v.tenant_id, v.id, p.name, m.name
 ORDER BY v.id DESC
 LIMIT sqlc.arg(page_size)::integer;

-- The version of a list and a model in force at an instant, without its rates: what a new version
-- replaces.

-- name: TenantPriceAt :one
SELECT v.id, v.kind, v.valid_from
  FROM pandokos.tenant_price_version v
 WHERE v.currency = sqlc.arg(currency)::text
   AND v.model_id = sqlc.arg(model_id)::uuid
   AND v.valid_from <= sqlc.arg(at)::timestamptz
 ORDER BY v.valid_from DESC
 LIMIT 1;

-- The trigger of the table refuses a version that has started.

-- name: DeleteTenantPriceVersion :execrows
DELETE FROM pandokos.tenant_price_version
 WHERE id = sqlc.arg(id)::uuid;

-- Gateway pool (with the tenant's model access) and admin pool (capability check, the tenant's
-- models): the lists of the given currencies only (the currency in force and, while a change is
-- pending, the one that follows); for each list and model, the version that was in force an hour
-- ago and every later one. A request is priced at its start, which lies moments before this is
-- read; the hour is the margin.

-- name: LoadTenantPriceVersions :many
SELECT v.id, v.model_id, v.valid_from, v.kind, v.currency,
       COALESCE(array_agg(r.category ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS categories,
       COALESCE(array_agg(r.threshold ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::bigint[] AS thresholds,
       COALESCE(array_agg(r.rate::text ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS rates
  FROM pandokos.tenant_price_version v
  LEFT JOIN pandokos.tenant_price_rate r ON r.tenant_id = v.tenant_id AND r.price_version_id = v.id
 WHERE v.currency = ANY (sqlc.arg(currencies)::text[])
   AND v.valid_from >= COALESCE((SELECT max(c.valid_from) FROM pandokos.tenant_price_version c
                                  WHERE c.currency = v.currency AND c.model_id = v.model_id
                                    AND c.valid_from <= now() - interval '1 hour'),
                                '-infinity'::timestamptz)
 GROUP BY v.tenant_id, v.id
 ORDER BY v.currency, v.model_id, v.valid_from;

-- The currencies a cost rule of the tenant of the session can be written in, for a scope (sprint
-- 5.3, section 5.8, with the tenant's lists): for the models of the scope the tenant can be given,
-- the currencies of the tenant's own prices and of the catalogue's prices, in both cases of the
-- version in force now and of every version that starts later.

-- name: ListBudgetCurrenciesForTenantScope :many
WITH scope AS (
    SELECT mo.id
      FROM pandokos.model mo
     WHERE (sqlc.narg(provider_id)::uuid IS NULL OR mo.provider_id = sqlc.narg(provider_id)::uuid)
       AND (sqlc.narg(model_id)::uuid IS NULL OR mo.id = sqlc.narg(model_id)::uuid)
       AND (mo.availability = 'all' OR EXISTS (SELECT 1 FROM pandokos.model_tenant_grant g WHERE g.model_id = mo.id))
)
SELECT DISTINCT x.currency::text AS currency
  FROM (SELECT v.currency
          FROM pandokos.tenant_price_version v
          JOIN scope s ON s.id = v.model_id
         WHERE v.kind = 'price'
           AND (v.valid_from > now()
                OR v.valid_from = (SELECT max(c.valid_from) FROM pandokos.tenant_price_version c
                                    WHERE c.currency = v.currency AND c.model_id = v.model_id
                                      AND c.valid_from <= now()))
        UNION ALL
        SELECT v.currency
          FROM pandokos.price_version v
          JOIN scope s ON s.id = v.model_id
         WHERE v.valid_from > now()
            OR v.valid_from = (SELECT max(c.valid_from) FROM pandokos.price_version c
                                WHERE c.model_id = v.model_id AND c.valid_from <= now())) x
 ORDER BY 1;
```

### Appendix A2b — changed statements of other query files

`tenants.sql`: every statement that returns the columns of `tenant` (`CreateTenant`,
`GetTenant`, `GetTenantForUpdate`, `ListTenants`, `UpdateTenant`, `SetTenantState`) adds, after
`time_zone, time_zone_previous, time_zone_effective_from`, in the same form (with the `t.`
prefix where the statement has one):

```sql
       active_currency, active_currency_previous, active_currency_effective_from
```

Without it the generated row types stop being the table's model and `internal/admin/tenants.go`
does not build. No statement of this file writes the three columns.

`usage.sql`, `InsertUsageRecords`: the column list ends `…, metadata_dropped, limit_state,
price_source)`; the select list ends `…, r.limit_state, r.price_source`; the record definition
ends `…, metadata_dropped integer, limit_state text, price_source text)`.

`usage.sql`, `ListUsageRecords`: the select list ends

```sql
       u.metadata_dropped, COALESCE(u.limit_state, '')::text AS limit_state,
       COALESCE(u.price_source, CASE WHEN u.price_version_id IS NOT NULL THEN 'catalogue' END, '')::text AS price_source
```

`limits.sql`: no statement changes. The comment above `ListPriceCurrenciesForScope` ends "…
otherwise every model of the scope. Rules of a tenant use `ListBudgetCurrenciesForTenantScope`
(`tenant_prices.sql`), which adds the tenant's own lists; this statement serves the ceilings of
the platform." in place of its last sentence.

### Appendix A3 — `api/openapi/admin.v1.yaml`

New paths, before `/v1/tenants/{tenant}/capability`:

```yaml
  /v1/tenants/{tenant}/price-lists:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: listPriceLists
      tags: [tenant-prices]
      summary: List the price lists of the tenant, one per currency
      x-pandokos-permission: price_list:read
      responses:
        "200": {$ref: '#/components/responses/TenantPriceListList'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
  /v1/tenants/{tenant}/prices:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: listPriceListVersions
      tags: [tenant-prices]
      summary: List the price versions of the tenant's price lists, newest first
      description: >-
        A cursor is valid only with the same currency, provider, model and at.
      x-pandokos-permission: price_list:read
      parameters:
        - {$ref: '#/components/parameters/Limit'}
        - {$ref: '#/components/parameters/Cursor'}
        - {name: currency, in: query, description: Only versions of the list in this currency., schema: {$ref: '#/components/schemas/Currency'}}
        - {name: provider, in: query, description: Only versions for models of this provider., schema: {$ref: '#/components/schemas/ProviderName'}}
        - {name: model, in: query, description: Only versions for the model of this name; needs provider., schema: {$ref: '#/components/schemas/ModelName'}}
        - {name: at, in: query, description: 'An RFC 3339 time: only the versions in force then, one per list and model.', schema: {type: string, minLength: 1, maxLength: 64}}
      responses:
        "200": {$ref: '#/components/responses/TenantPriceVersionList'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
    post:
      operationId: createPriceListVersion
      tags: [tenant-prices]
      summary: Add a price version to one of the tenant's price lists
      description: >-
        The list is the one of the version's currency; the first version in a currency creates the
        list. The version starts at valid_from (now when absent; never in the past). While the
        currency is the tenant's active currency it is the price of the tenant's requests for the
        model, in place of the catalogue price in that currency; in another currency it is kept and
        unused. kind end takes no rates: from its start the list does not price the model. The
        model must be available to the tenant.
      x-pandokos-permission: price_list:write
      requestBody: {required: true, content: {application/json: {schema: {$ref: '#/components/schemas/TenantPriceCreate'}}}}
      responses:
        "201": {$ref: '#/components/responses/TenantPriceVersionCreated'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "409": {$ref: '#/components/responses/Conflict'}
        "413": {$ref: '#/components/responses/RequestTooLarge'}
        "415": {$ref: '#/components/responses/UnsupportedMediaType'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
  /v1/tenants/{tenant}/prices/{price}:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
      - {$ref: '#/components/parameters/TenantPrice'}
    get:
      operationId: getPriceListVersion
      tags: [tenant-prices]
      summary: Read a price version of the tenant's price lists
      description: >-
        Any version, past ones included: the one a usage record names with price_source tenant.
      x-pandokos-permission: price_list:read
      responses:
        "200": {$ref: '#/components/responses/TenantPriceVersion'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
    delete:
      operationId: deletePriceListVersion
      tags: [tenant-prices]
      summary: Remove a price version of the tenant's lists that has not started
      description: >-
        Refused with invalid_state once the version is in force or past: versions are immutable.
        The response is the version as it was.
      x-pandokos-permission: price_list:delete
      responses:
        "200": {$ref: '#/components/responses/TenantPriceVersion'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "409": {$ref: '#/components/responses/Conflict'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
  /v1/tenants/{tenant}/active-currency:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: getActiveCurrency
      tags: [tenant-prices]
      summary: Read the currency every cost of the tenant is computed in
      x-pandokos-permission: price_list:read
      responses:
        "200": {$ref: '#/components/responses/ActiveCurrency'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
    put:
      operationId: setActiveCurrency
      tags: [tenant-prices]
      summary: Choose the currency every cost of the tenant is computed in
      description: >-
        The currency must have a list: prices of the tenant in it, or catalogue prices in it. The
        change takes effect at the start of the next month in the tenant's time zone; until then
        effective_from says when. Setting the currency in force again withdraws a pending change.
        An empty currency is the default of the installation. From the change on, only budgets and
        ceilings in the new currency apply; inactive_limits names the others.
      x-pandokos-permission: price_list:write
      requestBody: {required: true, content: {application/json: {schema: {$ref: '#/components/schemas/ActiveCurrencySet'}}}}
      responses:
        "200": {$ref: '#/components/responses/ActiveCurrency'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "413": {$ref: '#/components/responses/RequestTooLarge'}
        "415": {$ref: '#/components/responses/UnsupportedMediaType'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
```

New parameter (after `PriceVersion`), responses (after `PriceVersionList`) and schemas (after
`PriceOverride`):

```yaml
    TenantPrice:
      name: price
      in: path
      required: true
      description: ID of a price version of the tenant's price lists.
      schema: {$ref: '#/components/schemas/ID'}
```

```yaml
    TenantPriceVersion:
      description: The price version.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/TenantPriceVersion'}
    TenantPriceVersionCreated:
      description: The new price version.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/TenantPriceVersion'}
    TenantPriceVersionList:
      description: One page of price versions of the tenant's price lists.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/TenantPriceVersionList'}
    TenantPriceListList:
      description: The price lists of the tenant.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/TenantPriceListList'}
    ActiveCurrency:
      description: The active currency of the tenant.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/ActiveCurrency'}
```

```yaml
    TenantPriceKind:
      type: string
      description: price carries rates; end carries none, and from its start the list does not price the model.
      enum: [price, end]
    CurrencyOrDefault:
      type: string
      pattern: '^([A-Z]{3})?$'
      description: An ISO 4217 code; empty for the default currency of the installation.
    TenantPriceVersion:
      type: object
      additionalProperties: false
      required: [id, model_id, provider, model, kind, valid_from, currency, rates, note, in_force, created_at]
      properties:
        id: {$ref: '#/components/schemas/ID'}
        model_id: {$ref: '#/components/schemas/ID'}
        provider: {$ref: '#/components/schemas/ProviderName'}
        model: {$ref: '#/components/schemas/ModelName'}
        kind: {$ref: '#/components/schemas/TenantPriceKind'}
        valid_from: {type: string, format: date-time}
        currency: {$ref: '#/components/schemas/Currency'}
        rates:
          type: array
          items: {$ref: '#/components/schemas/PriceRate'}
        note: {type: string}
        in_force: {type: boolean, description: true when this is the version of its list and model in force now. A list is used only while its currency is the active currency.}
        created_at: {type: string, format: date-time}
    TenantPriceVersionList:
      type: object
      additionalProperties: false
      required: [items]
      properties:
        items:
          type: array
          items: {$ref: '#/components/schemas/TenantPriceVersion'}
        next_cursor: {type: string, description: Absent on the last page.}
    TenantPriceCreate:
      type: object
      additionalProperties: false
      required: [provider, model, currency]
      properties:
        provider: {$ref: '#/components/schemas/ProviderName'}
        model: {$ref: '#/components/schemas/ModelName'}
        currency: {$ref: '#/components/schemas/Currency'}
        kind: {$ref: '#/components/schemas/TenantPriceKind'}
        valid_from: {type: string, format: date-time, description: Now when absent; never in the past.}
        note: {type: string, maxLength: 500}
        rates:
          type: array
          maxItems: 64
          items: {$ref: '#/components/schemas/PriceRate'}
    TenantPriceList:
      type: object
      additionalProperties: false
      required: [currency, versions, models_priced, active]
      properties:
        currency: {$ref: '#/components/schemas/Currency'}
        versions: {type: integer, description: Versions of the list, past ones included.}
        models_priced: {type: integer, description: Models the list prices now.}
        active: {type: boolean, description: true when the currency of the list is the active currency in force.}
    TenantPriceListList:
      type: object
      additionalProperties: false
      required: [items]
      properties:
        items:
          type: array
          items: {$ref: '#/components/schemas/TenantPriceList'}
    InactiveLimit:
      type: object
      additionalProperties: false
      required: [level, rule_id, metric, currency]
      properties:
        level: {type: string, description: 'platform (a ceiling) or tenant (a rule of the tenant).'}
        rule_id: {$ref: '#/components/schemas/ID'}
        metric: {$ref: '#/components/schemas/LimitMetric'}
        currency: {$ref: '#/components/schemas/Currency'}
    ActiveCurrency:
      type: object
      additionalProperties: false
      required: [currency, in_force]
      properties:
        currency: {$ref: '#/components/schemas/CurrencyOrDefault'}
        in_force: {$ref: '#/components/schemas/Currency'}
        effective_from: {type: string, format: date-time, description: Present while a change is pending; the instant from which currency is in force.}
        inactive_limits:
          type: array
          maxItems: 100
          description: >-
            Only in the answer to a change: the cost rules of the tenant and the ceilings of the
            platform that apply to it whose currency is not the new one. From effective_from they
            are inactive: they count nothing and refuse nothing.
          items: {$ref: '#/components/schemas/InactiveLimit'}
    ActiveCurrencySet:
      type: object
      additionalProperties: false
      required: [currency]
      properties:
        currency: {$ref: '#/components/schemas/CurrencyOrDefault'}
```

Changed schemas (members added; nothing removed):

| Schema | Change |
|---|---|
| `Tenant` | `active_currency` (`CurrencyOrDefault`) and `active_currency_in_force` (string), both required; `active_currency_effective_from` (date-time, not required). `TenantCreate` and `TenantUpdate` do **not** gain a currency member |
| `LimitRule` | `active`: `{type: boolean, description: 'false for a cost rule whose currency is not the active currency of the tenant in force now; such a rule counts and refuses nothing.'}`, required |
| `PlatformLimitRule` | `active` (boolean, not required): "Only in listTenantPlatformLimits: whether the ceiling applies to the tenant in its active currency" |
| `TenantModel` | `tenant_price` (`TenantPriceVersion`, not required) and `price_source`: `{type: string, description: 'tenant or catalogue: which of tenant_price and price applies to the tenant in its active currency; empty when neither does.'}` (not required). The description of `price` becomes "The catalogue's version in force" |
| `UsageRecord` | `price_source`: `{type: string, description: 'catalogue or tenant: the price list price_version_id belongs to; empty when the record has no price version.'}`, required, after `price_version_id` |
| `BudgetAvailability` | description: "not_enforced when no enforced quota or budget in force matches (a budget in another currency than the tenant's active currency is not in force); unpriced when a budget matches and the model has no price in the active currency; exhausted when a matching quota or budget is used up for the current period." |

Other changes: the tag `tenant-prices` in the list of tags, after `prices` ("Price lists of a
tenant and its active currency"); `createLimit`, `createPlatformLimit` — "One rule per subject,
scope, metric and currency"; the sentence on the currency check of the four rule operations
becomes "The currency of a cost rule must have a price list for the scope: the tenant's or the
catalogue's (for a ceiling of one tenant: the catalogue's, or the tenant's active currency)";
`listTenantModels` — the description adds "price is the catalogue's version in force,
tenant_price the version of the tenant's active list; price_source says which applies";
`deleteModel` — the description adds "or while a price version of a tenant references it;
errors then names tenant_prices"; `getCapability` — the description of `checks.budget` follows
`BudgetAvailability`.

### Appendix A4 — `test/isolation/spec.go`

```go
// Grants (add)
"tenant_price_version": {
    "pandokos_gateway": {"SELECT"},
    "pandokos_admin":   {"DELETE", "INSERT(currency,kind,model_id,note,tenant_id,valid_from)", "SELECT"},
},
"tenant_price_rate": {
    "pandokos_gateway": {"SELECT"},
    "pandokos_admin":   {"INSERT(category,price_version_id,rate,tenant_id,threshold)", "SELECT"},
},
// "tenant": the UPDATE of pandokos_admin becomes
//   "UPDATE(active_currency,active_currency_effective_from,active_currency_previous,display_name,name,state,time_zone,time_zone_effective_from,time_zone_previous)"
// "usage_record": the INSERT column list of pandokos_gateway gains price_source (between output_tokens and price_version_id)

// Fixtures (add). Both create a scheduled version with one rate: the shape is checked at commit,
// and a scheduled version can be deleted again.
"tenant_price_version": {
    Insert: `WITH v AS (INSERT INTO pandokos.tenant_price_version (tenant_id, model_id, valid_from, currency)
           SELECT $1::uuid, m.id, now() + interval '1 hour' + random() * interval '1 hour', 'EUR'
             FROM pandokos.model m ORDER BY m.id LIMIT 1 RETURNING tenant_id, id),
     r AS (INSERT INTO pandokos.tenant_price_rate (tenant_id, price_version_id, category, rate)
           SELECT v.tenant_id, v.id, 'input', 1 FROM v RETURNING price_version_id)
SELECT r.price_version_id::text FROM r`,
    Update: `note = 'changed'`,
},
"tenant_price_rate": {
    Insert: `WITH v AS (INSERT INTO pandokos.tenant_price_version (tenant_id, model_id, valid_from, currency)
           SELECT $1::uuid, m.id, now() + interval '1 hour' + random() * interval '1 hour', 'EUR'
             FROM pandokos.model m ORDER BY m.id LIMIT 1 RETURNING tenant_id, id),
     r AS (INSERT INTO pandokos.tenant_price_rate (tenant_id, price_version_id, category, rate)
           SELECT v.tenant_id, v.id, 'input', 1 FROM v RETURNING price_version_id)
SELECT r.price_version_id::text FROM r`,
    Key:    "price_version_id",
    Update: `rate = 2`,
},
```

`TestDefaultSpecMatchesDesign` gets the same values. Nothing is added to `Global`,
`SecurityDefiner`, `Views` or `AppendOnly` (the price tables are not append-only: a scheduled
version is deleted).

## Part B — Tests first (commit: `test: sprint 5.4 tests, failing`)

Every test of §9 is written under its stated name, compiles, and fails for the reason it tests
(or is skipped by nothing). The pull request quotes the failing output of this commit.

### B1. Helpers and fixtures

- `test/apitest/calls.go`: `CallFor` learns the seven operations. `Params` gains `TenantPrice`
  (the ID of a **scheduled** version of the tenant, so that it can be read and deleted). Bodies:
  `createPriceListVersion` — `{"provider": "openai", "model": "gpt-4o-mini", "currency": "EUR",
  "valid_from": <now + 1 h + a random offset>, "rates": [{"category": "input", "threshold": 0,
  "rate": "1"}]}` (a random start, or two calls collide on the unique key); `setActiveCurrency`
  — `{"currency": ""}` (always accepted, changes nothing). `fresh` creates a scheduled version
  per call for the operations that name `{price}`.
- **Fixtures of earlier tests that this sprint changes the meaning of** — find them, fix them in
  this commit, and list them in its message:
  - a cost rule in a currency other than `USD` with a tenant on the default currency
    (`grep -rnE '"(EUR|GBP|CHF|JPY)"' --include=*_test.go internal test`): the fixture sets the
    tenant's currency with `SetTenantCurrency` (or `limitstest.SetCurrency`), or uses USD;
  - an expectation of 409 `no_price` because "the price is in another currency than the budget"
    (`grep -rn 'no_price\|NoPrice' --include=*_test.go .`): that budget is now inactive. Keep the
    test's intent with a model that has no price in the active currency; the old case moves to
    T-737 with the opposite expectation;
  - a read of `cost`, `currency`, the `usage` group, `reserved_tokens`, `limit`,
    `limit_currency` or `price_currency` from a request log line, or of `pandokos.cost.amount` /
    `pandokos.cost.currency` from a span (`grep -rnE 'reserved_tokens|"usage"|limit_currency|price_currency|cost\.amount|"cost"' --include=*_test.go . test/compose test/container`):
    the test's gateway is built with usage details on, or the assertion goes. T-739 is the test
    of the default;
  - `RuleSet` literals and `NewScopedRuleSet` calls keep compiling and keep every cost limit
    (the zero `Active`).
- `limitstest.Real`: a currency per tenant (`pricing.Active`), `SetCurrency(tenant, currency,
  at)`, and the reference model of 5.2 extended with "a cost limit counts only while its
  currency is in force".
- T-727 and T-730 read their seed from `PANDOKOS_TEST_SEED` or the clock and log it (`seedOf` in
  `internal/limits/helpers_test.go`; a copy in `internal/pricing`).
- T-751 needs a seam to fail a transaction after the write: package variables in
  `internal/admin` (`afterTenantPriceInsert`, `afterActiveCurrencySet func() error`, nil in
  production), set through `export_test.go` as `afterRuleDeletion` is; the bound of 50,000 is a
  variable set the same way (`maxTenantPriceVersions`).
- T-736 to T-738: the in-process gateway of the 5.3 integration tests with the real catalogue
  cache and rule cache on the test database and an injected clock, the fake provider, prices
  written with the seeds, the currency moved with `SetTenantCurrency` (in force, or pending with
  the clock stepped over the instant). Amounts are chosen by hand and stated in the test: with a
  tenant price of 1 EUR per million input tokens and 2 per million output tokens, the estimate
  of the fixture request is a known amount, and a budget of three estimates is reached by the
  fourth reservation.
- T-746 and T-747 seed, for two tenants, usage records, limit events and prices whose amounts,
  token counts and rates are canaries (distinct, improbable numbers such as 7654321 tokens and a
  rate of `6.54321`); T-747 searches every response body and the platform's audit rows for their
  decimal texts.
- T-739 captures the handler's logger and the in-memory span exporter of the trace tests.

### B2. Where each test lives

| Tests | File |
|---|---|
| T-726, T-727 | `internal/pricing/{list,list_property}_test.go` |
| T-728 to T-730 | `internal/limits/{rules_test,load_db_test,currency_property_test}.go` |
| T-731, T-732 | `internal/catalog/{cache_test,tenant_prices_db_test}.go` |
| T-733 | `internal/resolver/catalogue_internal_test.go` |
| T-734 to T-739 | `internal/gateway/{estimate_test,price_source_test,active_currency_db_test,inactive_budget_db_test,tenant_price_effect_db_test,usage_details_test}.go` |
| T-740 | `internal/ledger/writer_db_test.go` |
| T-741 to T-748 | `test/isolation/{prices,tenant,limits,usage,platform,api}_test.go` (`prices_test.go` and `platform_test.go` are new) |
| T-749 to T-752, T-754 to T-761 | `internal/admin/{tenant_prices,active_currency,tenant_price_audit,tenant_price_permissions,budget_currency,limits,capability,catalogue_access,usage,catalogue_delete,prices,tenants}_db_test.go` (new files for the first four) |
| T-753 | `internal/authz/authz_test.go` |
| T-762 | `internal/config/config_test.go` |
| T-763 | `internal/cli/tenant_prices_test.go` |
| T-764 | `test/e2e/tenant_prices_test.go` |
| T-765 | `internal/store/migrations/upgrade_0012_db_test.go` |
| T-R48 to T-R52 | `test/repo/{docs,adr,catalogue,usage}_test.go` |

The changed tests of §9 are changed in this commit too.

Verification: `go vet ./...` passes; `go test -count=1 ./... 2>&1 | tee /tmp/b.log` fails in the
new tests only (list them in the commit message).

## Part C — Implementation

### C1. The price in force — `feat: the price in force for a tenant: its list in the active currency, else the catalogue's price in it`

Files: `internal/pricing/{list,price,cost}.go`.

- `Active.At(t)`: `Previous` when `From` is not zero and `t` is before it, else `Current`.
- `List.At(t)`: with the zero `Currency`, `VersionAt(l.Catalogue, t)` and `SourceCatalogue`.
  Otherwise `c := l.Currency.At(t)`; among `l.Tenant` the versions whose `Currency` is `c`:
  `VersionAt`; when found and not `End`, that version and `SourceTenant`. Otherwise
  `VersionAt(l.Catalogue, t)`; when found and its `Currency` is `c`, it and `SourceCatalogue`.
  Otherwise not found, with an empty source.
- `List.Cost(u, t)`: `CostOf(u, v, found)`; `Source` is set when `VersionID` is.
- `Version.Validate` is unchanged and is not called for an `End` version. `CostOf`, the
  fallbacks and `SameRates` are unchanged.

Verification: `go test -race -count=1 ./internal/pricing/` (T-726, T-727, T-370 to T-373).

### C2. Which budgets apply — `feat: cost limits apply in the tenant's active currency; one rule per currency`

Files: `internal/limits/{rules,load}.go`.

- `slotKey` gains the currency (empty for a metric that is not a cost), for platform rows and
  for the tenant's rules: a row of the tenant replaces the default of its scope, metric and
  currency.
- `RuleSet.For(q)`: after the resolution of 5.3, drop every limit with `Metric.Cost()` whose
  `Currency` differs from `set.Currency.At(q.Start)` — unless `set.Currency` is the zero value.
  The order of the result and "at most one limit per counter" hold.
- `loadRules` takes the default currency (`RuleCacheOptions.DefaultCurrency`, required like
  `Zones`; `LoadRules` gains the parameter) and reads `GetTenantActiveCurrency` in its
  transaction, after the time zone: `Current` and `Previous` with `''` replaced by the default,
  `From` from `active_currency_effective_from` when it is set.
- Nothing else of the package changes. `Limit`, the store, the counters, the reconciler and the
  events never see an inactive limit.

Verification: `go test -race -count=1 ./internal/limits/` (T-728 to T-730 and every test of 5.1
to 5.3 in the package).

### C3. Prices in the gateway — `feat: a tenant's currency and prices in the catalogue cache; price source in the usage record`

Files: `internal/catalog/{access,load,cache}.go`, `internal/resolver/{catalogue,limits}.go`,
`internal/gateway/{ports,estimate,record}.go`, `internal/ledger/rows.go`,
`cmd/pandokos/serve.go`.

- `LoadTenantAccess(ctx, db, id, defaultCurrency)`: after the grants and the restriction,
  `GetTenantActiveCurrency` (resolved as in C2), then `LoadTenantPriceVersions` with the
  currencies of the `Active` (one, or two while a change is pending), built into `Prices` by
  model ID with `ratesOf`; `End` is `kind == "end"`; `Source` of a tenant's `pricing.Version` is
  `"tenant"`. The administration API passes its own `DefaultCurrency`.
- `Cache`: no new state; `Invalidate` and `Flush` are unchanged. Update the comments that say
  what the entry holds.
- `resolver.Catalogue.Resolve`: `Currency: ta.Currency`, `TenantPrices: ta.Prices[m.ID]`. A
  principal without tenant has the zero access (the zero `Active`: catalogue prices as before).
- `gateway`: `estimateOf` and `recordOf` use `t.PriceList().Cost(usage, start)`; the three
  fields of `Target` are read nowhere else in the package.
- `resolver.Limits.reserve`: the `no_price` refusal sets `PriceSource: e.Cost.Source`. Nothing
  else: `cannotMeasure` stays as the last guard (§5.4).
- `ledger/rows.go`: `PriceSource` from `r.Cost.Source` when `VersionID` is not empty.
- `serve.go`: the default currency to the catalogue cache and the rule cache.

Verification: `go test -race -count=1 ./internal/catalog/ ./internal/resolver/ ./internal/gateway/ ./internal/ledger/`
(T-731 to T-737 and T-740 pass; T-738 passes with C6).

### C4. Logs and traces — `feat: request log and spans without tenant usage details unless configured`

Files: `internal/gateway/{handler,limits,spans}.go`, `internal/config`, `cmd/pandokos/serve.go`.

- The handler option `UsageDetails` comes from `telemetry.tenant_usage_details` and is false by
  default. Behind it, and only behind it: in the request line `cost`, `currency`, the `usage`
  group, `reserved_tokens`, and of a refusal `limit`, `limit_currency`, `price_currency`,
  `price_source`; on the client span `pandokos.cost.amount` and `pandokos.cost.currency`.
  `cost_status`, `usage_source`, `usage_output_source`, the byte counts, `limit_state`,
  `budget_unpriced`, `limits_unverified`, `limit_subject`, `limit_metric`, `limit_level`,
  `limit_scope`, `retry_after_s` and `estimate_over_limit` stay.
- `SpanAttributes` keeps listing every key the gateway can set.
- One function builds the gated fields of the line, so that T-R52 can find it.
- `serve` logs the configuration warning of part A when the key is on.

Verification: `go test -race -count=1 ./internal/gateway/ ./internal/config/` (T-739, T-762).

### C5. Rules in the administration API — `feat: budgets in any currency that has a list; rules per currency; the capability check in the active currency`

Files: `internal/admin/{budget_currency,limits,platform_limits,limit_fields,capability,
model_access,usage,tenants,convert_catalog}.go`.

- `budget_currency.go`:

  ```go
  // budgetScope is the scope of a cost rule and whose rule it is.
  type budgetScope struct {
      ProviderID, ModelID *string
      Tenant              bool     // a rule of the tenant whose session q runs in
      TenantCurrencies    []string // for a ceiling of one tenant: its active and pending currency, resolved
  }
  func budgetCurrencies(ctx context.Context, q *queries.Queries, s budgetScope) ([]string, error)
  ```

  `Tenant`: `ListBudgetCurrenciesForTenantScope`. Otherwise `ListPriceCurrenciesForScope` with
  `VisibleOnly` false, merged with `TenantCurrencies`, sorted, without duplicates.
  `checkBudgetCurrency` takes the scope; its two messages are unchanged.
- `platform_limits.go`: for a ceiling with a tenant, on create and on update, the currencies
  come from `GetTenant` in the platform transaction (the columns of appendix A2b), resolved with
  the default currency. **No tenant session is opened and no price table is read.**
- `limits.go`, `platform_limits.go`: the unique-key violation is still 409 `conflict` (the
  constraint names did not change); the audit details already name the currency.
- `active`: the tenant handlers read `GetTenantActiveCurrency` once per request, resolve the
  currency in force at the database's time of the read, and set `active` on each rule: true for
  a metric that is not a cost, else `currency == in force`. `listTenantPlatformLimits` sets it
  too; `listPlatformLimits` and the other platform operations leave it absent.
- `capability.go`: `LoadRules` and `LoadTenantAccess` with the default currency; the budget
  check asks `pricing.List{Currency: ta.Currency, Catalogue: snap.Versions(m.ID), Tenant:
  ta.Prices[m.ID]}.At(now)`; `unpriced` when a matching enforced cost limit exists and nothing
  is found (the limits it sees are active by C2).
- `model_access.go`: the access once per call; per model `List.At(now)` with the catalogue row
  of `PriceAt`; `tenant_price` when the source is `tenant` (`GetTenantPriceVersion`),
  `price_source` as §5.7.
- `tenants.go`: `active_currency_in_force` resolved (pending and default), as the zone is.
- `convert_catalog.go`, `mapCatalogError`: `tenant_price_version_model_fkey` →
  `invalidState("tenant_prices", "tenant price lists reference this model; retire the model instead")`.

Verification: `go test -count=1 ./internal/admin/` (T-754 to T-761, T-716, T-681, T-720, T-715).

### C6. Price lists and the active currency — `feat: tenant price lists and the tenant's active currency in the administration API`

Files: `internal/admin/{tenant_prices,active_currency}.go`, `export_test.go`.

- Every handler runs `inTenantPath` (the tenant's session; the permission check has already
  answered 404 for another tenant).
- **Create**, in the order of §5.5: `LockTenantPrices`; `ResolveTenantPriceModel` (no row: 400
  on `model`); validation (a `price`: rates through `pricing.Version.Validate` with the mapping
  of `createPriceOverride`; an `end`: 400 on `rates` `must be absent for an end`); the start
  (`valid_from` not before `s.now()`, else `ClockTimestamp` after the lock); for an `end`,
  `TenantPriceAt(currency, model, start)` must be a `price`; `CountTenantPriceVersions` against
  the bound; `CreateTenantPriceVersion`, `CreateTenantPriceRates` (rates sorted as `sortRates`
  does); `GetTenantPriceVersion` for the answer; the audit row (`audit.RecordTenant`).
- Errors: unique `tenant_price_version_valid_from_key` → `errConflict`; a check violation
  without a constraint name (the guard) → 400 on `valid_from` `must not be in the past`; a
  restrict violation on delete → `errInvalidState`. Nothing maps 23000 or 42501.
- **Delete**: `GetTenantPriceVersion` (no row: 404), `DeleteTenantPriceVersion`, the audit row.
- **Lists**: `listPriceLists` from `ListTenantPriceLists`, `active` from the currency in force;
  `listPriceListVersions`: `provider` required with `model`, `at` as RFC 3339, the cursor on the
  ID (`decodeCursor` with the key `tenant_prices`).
- **The active currency** (`active_currency.go`), modelled on `setTimeZone` of `time_zone.go`:
  `LockTenantActiveCurrency`; the currency in force and whether a change is pending, by
  `db_now`; then
  - the value already set: nothing is written (the answer shows a pending instant);
  - the currency in force: the pending change is withdrawn (`previous` empty, no instant);
  - a change pending: the new currency replaces it and keeps the instant;
  - otherwise: the instant is `limits.NextChange` of the calendar of **the time zone in force**
    at `db_now` (the zone columns of the same row, resolved as the zone handler does), with
    `previous` the currency in force.

  Before any of the three writes that set a currency other than `''`: `CurrencyHasList`, else
  400 on `currency`. Raw values are compared (`''` and the code it resolves to are different
  values: setting `USD` on a tenant with the default is a pending change that changes no
  price). `inactive_limits`: from `ListLimitRules` and `ListPlatformLimitRules` in the same
  transaction — cost metrics whose currency differs from the resolved new currency; a default
  ceiling left out when the tenant has its own ceiling of the same scope, metric and currency;
  ordered by level, then rule ID; the first 100. The audit row with the count.
- The conversion to `adminapi.TenantPriceVersion` reuses the rate conversion of `priceOut`
  (decimal text without trailing zeros).

Verification: `go test -count=1 ./internal/admin/ -run 'TestTenantPrice|TestActiveCurrency' ./test/isolation`
(T-741 to T-752 pass; T-738 passes now).

### C7. CLI — `feat: CLI for tenant price lists and the active currency`

Files: `internal/cli/{tenant_prices,commands,limits,capability}.go`.

The commands of §5.14, one per operation, in a table `priceListCommands` appended to the command
list. `--model` is split at the first `/`; `--rate` uses `parseRate`; `--valid-from` uses
`parseTime`; `--end` sends `kind: end` and refuses `--rate` with a usage error; `currency set`
takes a code or `--default`, and prints under the resource one line per inactive limit (`level`,
`rule_id`, `metric`, `currency`) under the heading `limits that will be inactive:`. `limit list`
and `limit platform` gain the column `active`; `tenant get` and `tenant list` show
`active_currency_in_force`.

Verification: `go test -count=1 ./internal/cli/` (T-763, T-219).

### C8. End to end — `test: tenant price lists, the active currency and usage confidentiality end to end`

Files: `test/e2e/tenant_prices_test.go`; `test/compose`, `test/container` where they read the
removed log fields (part B found them).

T-764 with the binary and the CLI, in the order of its row in §9; the currency is moved as §7
says. One run of the binary with `PANDOKOS_TELEMETRY_TENANT_USAGE_DETAILS=true` checks the
warning and the fields.

Verification: `go test -count=1 ./test/e2e/ -run 'TestTenantPriceLists'` then
`go test -race -count=1 ./...`.

## Part D — Repository tests and container (commit: `ci: sprint 5.4 in repository tests`)

- T-R51 in `test/repo/catalogue_test.go`: parse the Go files of the module; a call of
  `pricing.VersionAt` outside `internal/pricing` fails; in `internal/gateway` a selector
  `.Prices`, `.TenantPrices` or `.Currency` on a `Target` outside the method `PriceList` fails
  (test files excepted).
- T-R52 in `test/repo/usage_test.go`: (1) from the generated query files, the methods whose SQL
  names `pandokos.usage_record`, `pandokos.limit_event`, `pandokos.tenant_price_version` or
  `pandokos.tenant_price_rate`; (2) in the non-test Go files, every call of one of them lies
  inside a function literal passed to `WithTenant`, `inTenant` or `inTenantPath`, or in a
  function only called from such literals (keep an explicit, short list of those helpers in the
  test), and never inside `Platform` or `inPlatform`; (3) in `internal/admin`, the exported
  handler methods whose operation has a platform permission (from `adminapi.Operations` and
  `authz.Lookup`) reach none of them; (4) in `internal/gateway`, the log keys of §5.11 appear as
  string literals in the one function of C4 only.
- T-R48 to T-R50 exist from part B and pass after part E.
- No new CI job, image layer, compose service or bench target. Run `make test-container` and
  `make test-compose` once: the image must start against a database at 0012, and their
  assertions on log lines must hold with the default configuration.

Verification: `go test -count=1 ./test/repo/` (T-R51 and T-R52 pass; T-R48 to T-R50 fail until
part E).

## Part E — Documentation and decision records (commit: `docs: sprint 5.4 documentation, ADR 0053 and 0054, requirements`)

### E1. Guides and reference

- `docs/operations/catalogue.md`: a new section **"Tenant price lists and the active currency"**
  after "Overrides" (later sections renumbered, links followed): a list per currency and its
  rules (those of section 4); who reads and writes (the tenant, nobody else); the active
  currency and the rule of §5.2 with one worked example (catalogue USD; the tenant writes a EUR
  list; nothing changes; it sets the currency; from the next month EUR prices and EUR budgets;
  a model the EUR list does not price has an unknown cost); `end`; the change rule and why it
  waits; how fast a version takes effect; what imports and overrides do not touch; a catalogue
  price in another currency than a tenant's; a model with tenant versions is retired, not
  deleted.
- `docs/operations/limits.md`, section 5, rewritten: a budget has a currency; **only budgets and
  ceilings in the tenant's active currency apply**, the others are inactive (`active` in `limit
  list`); a budget can be written in any currency that has a list for its scope (the table of
  §5.7); preparing a change of currency step by step with the commands; what the platform's
  ceilings measure (the tenant's price, one cost per request), that a tenant leaves cost
  ceilings in another currency behind when it changes, how the platform follows (the `Tenant`
  resource, a ceiling for that tenant in the new currency) and that request and token ceilings
  cannot be stepped out of; `no_price` now means "no price in the active currency". Sections 8
  and 15: the log fields behind the key, and troubleshooting with the tenant's tools. Section
  16: tenant price lists leave the list of what is not there.
- `docs/operations/usage.md`: the column `price_source`; "Reproducing a cost"; a section **"Who
  reads usage"**: the tenant, through `usage:read`; no platform role; what the operator's logs
  and traces carry by default and with the key; the limit of issue #33.
- `docs/operations/gateway.md`, `container.md`, `telemetry.md`: the tables of log fields and
  span attributes mark the fields of §5.11 as present only with
  `telemetry.tenant_usage_details`; the warning at start.
- `docs/operations/configuration.md`: the two keys (table, validation table, example file);
  `cache.catalog_ttl` and `cache.limits_ttl` also bound a change of a tenant's prices and
  currency.
- `docs/operations/database.md`: section 17, "Grants of migration 0012" — the two tables, the
  columns of `tenant` and their guard, the keys of the rule tables, `price_source`; the upgrade
  note and the three changes of behaviour of §5.13; that the worker holds nothing on the price
  tables and that no policy shows them to the platform scope.
- `docs/reference/admin-api.md` (seven operations, three audit actions, the changed resources),
  `permissions.md` (three permissions; the sentence that no platform permission reads usage,
  limit events or prices of a tenant), `docs/architecture/data-model.md`.
- `docs/architecture/security-model.md`: under "What is protected", the tenant's usage records,
  limit events and price lists are read through the product by the tenant only (row-level
  security, tenant-scoped permissions, logs and traces without usage details by default); under
  "What Pandokos does not protect against", **confidentiality of a tenant's data against whoever
  administers the database, the hosts or the backups (issue #33)**: the owner role, a superuser,
  a backup or a host reads every table; row-level security separates tenants and the product's
  platform roles, not the operator of the installation.
- `docs/requirements/README.md`:
  - after CAT-10: `| CAT-11 | A tenant can hold price lists of its own, one per currency, for the models it can use. Its active currency selects the list: where that list prices a model it takes precedence over the catalogue price in that currency. Only the tenant reads and writes its lists | S | R2 | new (issue #48) |`
  - USE-06 becomes "Aggregated reports are available to the tenant, by period, provider, model
    and caller";
  - USE-07 becomes "Tenant roles read their tenant's usage. Platform roles read no usage of a
    tenant: they see operational indicators of the installation, with no tenant and no cost";
  - AZ-06 becomes "Platform roles are separate from tenant roles and imply no tenant
    membership: platform admin, platform auditor. No platform role reads the usage or the costs
    of a tenant" (priorities, releases and sources unchanged);
  - section 9 ("What changed from SOW 0.1") names CAT-11 among the additions and, among what was
    rescoped, that platform usage viewing was dropped: usage is the tenant's.
  Any other sentence of the requirements, the overview or the roadmap that promises platform
  aggregates per tenant or a platform usage viewer is brought in line
  (`grep -rn 'usage viewer\|platform aggregates' docs planning`).
- `planning/epics/06-observability-reports-lifecycle/epic.md`:
  - 6.1, the bullet on cardinality becomes "Cardinality policy: tenant label off by default,
    opt-in, and never on cost or token metrics: those exist per installation only (ADR 0054)";
    the bullet on the redacting log handler adds "; usage details stay behind
    `telemetry.tenant_usage_details`";
  - 6.2, the first bullet becomes "Usage rollups maintained by the worker in each tenant's
    session; report API for the tenant by period, provider, model and caller; no platform
    aggregate per tenant: the platform sees operational indicators of the installation, without
    tenant and cost (ADR 0054). The rollup serves the limit counters and the capability check
    (5.2 §11)"; a note under the sprint: "Costs are reported as recorded, per currency and
    price source, never converted or added across currencies (5.4 §11)".

### E2. Decision records

Two new records and their rows in `docs/adr/README.md` (`proposed`):

**`docs/adr/0053-tenant-price-lists-and-active-currency.md`**

```markdown
# 0053. Tenant price lists and the active currency

- Status: proposed
- Date: 2026-11-18

Refines [ADR 0036](0036-prices-costs-and-importer.md) and
[ADR 0050](0050-quotas-and-budgets-periods-attribution-currencies.md). Requirement CAT-11.

## Context

Catalogue prices are global: one price per model for every tenant, imported in USD or overridden
by the operator for everyone (0036). Budgets do not convert (0050). A tenant could neither budget
in its own currency nor record the cost of a contract price (issue #48). The maintainer's review
of 2026-11-18 decided who owns such prices and how a currency is chosen.

## Decision

1. A tenant holds price lists of its own, one per currency: versions for models it can see, with
   the rules of 0036 (immutable, starting now or later, rates per category with tiers and
   fallbacks, exact decimals). A version is a price or an end. They are stored in two
   tenant-scoped tables; only the tenant reads and writes them (`price_list:*`). The platform has
   no permission, route, policy or function that reads or writes them.
2. A tenant has one active currency, stored on the tenant; the default is the installation's
   (`pricing.default_currency`, USD). Every cost of the tenant is computed in it: with the
   tenant's list in that currency where it prices the model, otherwise with the catalogue's
   version in force when it is in that currency; otherwise the cost is unknown. Lists in other
   currencies are kept and unused. Nothing is converted and no two currencies are added. One
   function (`pricing.List`) decides.
3. Only the tenant changes its active currency, and only to a currency that has a list (its own
   or the catalogue's). A change takes effect at the start of the next month in the tenant's
   time zone, the instant of a zone change, so that no budget is freed.
4. Only cost limits in the active currency apply to a tenant: its budgets and the platform's
   ceilings alike. The others are inactive. A rule is one per subject, scope, metric and
   currency, so that a change can be prepared.
5. A request has one cost. A ceiling in the active currency counts the cost at the tenant's
   price. The platform reads a tenant's active currency, not its prices.
6. The usage record states the price version and the list it belongs to (`price_source`). The
   ledger has no foreign key to a version; a version that has started cannot be removed.
7. The gateway keeps a tenant's currency and prices in the tenant's entry of the catalogue
   cache, and the currency in its rule set.

## Consequences

- A tenant budgets and reads its costs in one currency of its choice, at its own prices; other
  tenants, imports and overrides are unaffected.
- A catalogue price in another currency than a tenant's active currency does not price that
  tenant's requests.
- A tenant that changes its currency leaves the platform's cost ceilings in the old currency
  behind; the platform sees the change coming and can set a ceiling in the new one. Ceilings in
  requests and tokens do not depend on currencies.
- Under BYOK a tenant list that is wrong makes the tenant's own figures wrong.
- A model that a tenant has priced is retired, not deleted.

## Alternatives considered

**A global override in the tenant's currency.** Changes the price for every tenant and stops
imports for the model.

**Lists written or read by the platform, or a grant from the platform.** Rejected in the review:
a tenant's prices are the tenant's.

**Precedence of the tenant's price whatever its currency.** Costs of one tenant in several
currencies within a period, budgets that silently stop measuring some models.

**A second cost per request at the catalogue price, for the platform's ceilings.** Two costs in
the estimate, the settlement, the ledger and the counters.

**A change of currency that applies at once.** Frees the budgets of the period in progress.

**A `valid_until` on a version.** Ending a list would update a version.

## History

- 2026-11-18: proposed with sprint 5.4, after the maintainer's review of the same day.
```

**`docs/adr/0054-usage-data-is-the-tenants.md`**

```markdown
# 0054. Usage data is the tenant's: what the platform does not see

- Status: proposed
- Date: 2026-11-18

Refines [ADR 0013](0013-ledger-and-audit.md), [ADR 0015](0015-observability.md) and
[ADR 0007](0007-authorization-rbac.md). Requirements USE-06, USE-07, AZ-06.

## Context

The usage ledger, the limit events and now the price lists describe what a tenant consumes and
what it pays. The first requirements gave platform roles aggregates per tenant and a usage
viewer, and the request log and the traces carried cost and token counts of every request. The
maintainer's review of 2026-11-18 decided that this data is the tenant's.

## Decision

1. Through the product, only the tenant reads its usage records, limit events and price lists.
   No platform permission, route, policy or `SECURITY DEFINER` function returns them, and no
   platform role is a usage viewer. Background jobs read usage only in the session of the tenant
   they work for.
2. The platform sees operational indicators of the installation: requests, errors, latency,
   saturation, without a tenant dimension on anything that is a cost or a token count, and
   without costs per tenant. It still sets ceilings, and knows the ceilings it set, not what was
   counted against them.
3. The request log and the traces carry no cost, currency, token count, reserved amount or
   budget figure of a request. `telemetry.tenant_usage_details`, off by default, brings them
   back, with a warning at start: for single-tenant installations and for debugging.
4. This is held by tests: negative tests on platform sessions in the database, on every platform
   operation of the API, on the result types of definer functions, and a repository test over
   the handlers.
5. Confidentiality against whoever administers the database, the hosts or the backups is out of
   scope (issue #33) and stated in the security model.

## Consequences

- Reports, rollups and exports (sprint 6.2) are built for the tenant; metrics (sprint 6.1) have
  no tenant label on cost and tokens.
- An operator troubleshoots a refusal with operational fields; the figures are in the tenant's
  own tools (capability check, usage list, rules).
- The tenant ID stays in logs and traces: that a tenant made a request is operational.

## Alternatives considered

**Platform aggregates per tenant, as first required.** The platform would read what tenants
spend; under BYOK it has no need to.

**Removing the fields from logs with no way back.** A single-tenant installation loses a useful
line for nothing.

**Encrypting usage per tenant.** Protects against the operator of the database, which is another
problem (issue #33) with other costs.

## History

- 2026-11-18: proposed with sprint 5.4, after the maintainer's review of the same day.
```

**Amendments** — a section `## Amendment (sprint 5.4)` before History, and the history line
`- 2026-11-18: amended by sprint 5.4 (see Amendment); [ADR 0053](0053-tenant-price-lists-and-active-currency.md).`

- **ADR 0050**: "Replaces decision 4 for a price in another currency, and extends decision 3 and
  the amendment of sprint 5.3. (1) A tenant has an active currency; a budget, and a ceiling of
  the platform, applies only while its currency is the tenant's active currency, and is
  inactive otherwise: it counts and refuses nothing. (2) A request a budget in force cannot
  measure — no price in the active currency — still meets `limits.on_unknown_price`. A price in
  another currency is no longer a case: prices outside the active currency are not used. (3)
  The currency check on write admits any currency that has a list for the scope: the tenant's
  own or the catalogue's; for a ceiling of one tenant, the catalogue's or that tenant's active
  or pending currency. (4) A change of the active currency takes effect at the start of the
  next month in the tenant's zone, with a pending zone change at the same instant."
- **ADR 0049**: "A cost limit outside the tenant's active currency is left out when the limits
  of a request are resolved. A tenant's ceiling replaces the default of the same scope, metric
  and currency."
- **ADR 0052**: "There is one rule per subject, scope, metric and currency (decision 1). The rule
  resources show whether a cost rule is active."

**History lines** (one each, dated 2026-11-18, starting "sprint 5.4:"):

| Record | Line |
|---|---|
| 0011 | tenants hold price lists of their own, used in their active currency ([ADR 0053](0053-tenant-price-lists-and-active-currency.md)) |
| 0015 | logs and traces carry no cost, currency or token count unless `telemetry.tenant_usage_details` is on ([ADR 0054](0054-usage-data-is-the-tenants.md)) |
| 0031 | three tenant permissions, `price_list:read|write|delete`; no platform permission reads a tenant's usage, limit events or prices |
| 0035 | the per-tenant entry of the catalogue cache also holds the tenant's active currency and price versions |
| 0036 | the rules of a price version also govern the versions of a tenant's price lists; imports and overrides do not touch them; a catalogue price prices a tenant's request only in the tenant's active currency ([ADR 0053](0053-tenant-price-lists-and-active-currency.md)) |
| 0039 | `usage_record.price_source` says which price list the price version belongs to; the column is added without rewriting a partition |
| 0040 | the cost attributes of the client span are set only with `telemetry.tenant_usage_details` |
| 0043 | `checks.budget` counts the budgets in the tenant's active currency and the price in it |

### E3. Planning files and traceability

- `planning/epics/05-limits-and-budgets/epic.md`: section 5.4 gains "Designed and delivered as
  [5.4-tenant-price-lists/sprint.md](5.4-tenant-price-lists/sprint.md)."; the list of decisions
  the epic implements names ADR 0053 and 0054.
- `planning/roadmap.md`: the row of 5.4 is `done` when the sprint closes.
- `sprint.md`: `status: done` at close, with the review's answers to §12.

Verification: `go test -count=1 ./test/repo/` passes entirely; `make check`.

## Part F — Verification (no commit unless something is found)

```sh
set -a; . local/testdb/env; set +a
make generate-check && make check
go test -race -count=1 ./...
go test -race -count=20 ./internal/pricing/ ./internal/catalog/ ./internal/limits/
for s in 1 2 3; do PANDOKOS_TEST_SEED=$s go test -count=1 ./internal/pricing/ ./internal/limits/ -run 'TestListProperty|TestCurrencyChangeNeverFrees'; done
make test-sdk-python && make test-sdk-ts && make test-container && make test-compose
make secrets
```

Then, by hand against the definition of done (§10):

1. The exit condition, read from the output of T-736, T-746, T-747 and T-764: the amounts, the
   429, the two currencies and sources in the records, the 403 and the empty reads of the
   platform.
2. `grep -rn 'VersionAt(' --include=*.go . | grep -v _test | grep -v internal/pricing` prints
   nothing.
3. `git diff main --stat -- internal/limits` lists `rules.go`, `load.go` and tests only;
   `git diff main --stat -- internal/catalog/importer.go internal/admin/prices.go` prints
   nothing.
4. `grep -n 'x-pandokos-permission: price_list' api/openapi/admin.v1.yaml` shows seven lines and
   `grep -c 'tenant_price:' internal/authz/authz.go` prints 0: no platform permission.
5. Start the binary with the default configuration, send one request with a known token count,
   and read the log: no cost, currency or token field. Start it with the key on: the warning and
   the fields.
6. In a database migrated by the suite: `\d pandokos.tenant_price_version` shows forced
   row-level security, one policy and the four triggers; no function of the schema is new and
   `SECURITY DEFINER`.
7. The pull request quotes: the failing output of part B, the property seeds, the coverage table
   of §9 with links to the CI run, the three changes of behaviour of §5.13, and the open points
   of §12 for the maintainer.

## What the implementer does not do

- Does not give the platform any way to read or write a tenant's prices, usage or limit events,
  and does not add `active_currency` to `createTenant` or `updateTenant`.
- Does not change the counters, the reconciler, the importer, the catalogue's price tables or
  `createPriceOverride`.
- Does not add a definer function, a cache, a notification payload or a third configuration key.
- Does not convert or add amounts of different currencies anywhere, tests included.
- Does not make a change of the active currency take effect before the next month (O-1 is the
  maintainer's to decide).
- Does not add tenant price lists to an export, a report or the tenant deletion: §11.
- Does not edit a merged migration, pin a property seed, lengthen a bound or skip a database
  test.
