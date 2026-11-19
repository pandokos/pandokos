# Sprint 5.3 — Implementation plan

- Design: [sprint.md](sprint.md). Section numbers (§) refer to it.
- Each part below is self-contained: an agent needs sprint.md and that part only. Parts run in
  order A, B, C1 … C8, D, E, F.
- Suggested grouping for implementers: **G1** = C1 then C2 (`internal/limits`: identifiers and
  the tenant calendar; no gateway, no API); **G2** = C4 then C5 (rules, their subjects and the
  catalogue in the administration API); **G3** = C6 then C7 (time zone, capability, usage, CLI).
  After part B the three do not depend on each other. C3 and C8 are one agent, in order, after
  G1 to G3. A, B, D, E and F are one agent each. Because the risk class is high, C5 and the
  time-zone part of C2 and C6 are reviewed by an agent that did not write them, with §5.3 and
  §5.7 as the checklist.

## Rules for the implementer

1. Work on the current branch (`claude/epic-5-implementation-cu4vxo`); do not create a branch.
   Sign off every commit (`git commit -s`), without any AI attribution. One commit for A, one
   for B (tests, failing), one per step of C (message given with the step), one for D, one for
   E, one for F if something is found.
2. **No new Go module**, no new configuration key, permission or error code.
3. Appendices A1 (migration 0011) and A2 (`limits.sql`) are copied **byte for byte**. A1 was run
   on PostgreSQL 16.15 and A2 compiled with `sqlc` v1.31.1 while designing; `apigen` was not run
   on A3. If one does not work as written, stop and ask. A merged migration is immutable (ADR
   0026): until the pull request merges, a correction changes the file and its line in
   `migrations.sum` together.
4. **The database is the guard, the handler is the path.** Every handler that removes a subject
   deletes its rules first, under `LockLimitRules`, and audits each (§5.3). Do not weaken a
   foreign key, a trigger or a check to make a test pass; a `restrict_violation` in a test means
   a path forgot its rules.
5. **Tenant scope.** Every statement runs as `queries.New(tx).<Method>` inside a `store`
   transaction helper (T-R10, T-R12). No statement names the tenant in a `WHERE` clause, except
   `LockTenantTimeZone` and `SetTenantTimeZone`, which address the `tenant` row by ID under its
   policies. The only crossings of the tenant boundary are the two definer functions of A1.
6. **The limiter sees numbers and identifiers only.** The new audit rows, log fields and usage
   column hold identifiers, catalogue names, dates and zone names. No body, header value,
   credential or model output; no real key in a test, fixture or document.
7. **One definition each**: `limits.Sum` for what the ledger counts; `limits.TenantCalendar` for
   which period an instant belongs to; `budgetCurrencies` for the currencies of a scope;
   `ruleDetail` for the audit detail of a rule (the SQL of the definer function mirrors it and
   T-714 compares them).
8. The gateway does not import `internal/limits` (T-R40); `internal/limits` does not import
   `internal/ledger` or `internal/admin` (T-R43).
9. Generated files are never edited by hand (`make generate`).
10. Every new route declares its permission in the specification and gets its cross-tenant test
    in the same commit as its handler. Every handler that changes something writes its audit
    record in the transaction of the change.
11. Tests hold for root and non-root; nothing sleeps longer than 2 s; waiting is
    `testutil.Eventually`; time-dependent logic takes a clock. A bound is never made to pass by
    lengthening it and a database test never by skipping it. A property test that fails is fixed
    in the code or reported; its seed is never pinned to a passing one.
12. A gap in sprint.md or in this plan is a question to the orchestrator, not a decision.

## Environment

```sh
set -a; . local/testdb/env; set +a     # the test database runs in Docker; do not run make db-up or db-down
make generate                           # apigen and sqlc
go test -race -count=1 ./...            # everything but the SDK suites
make check                              # what a pull request must pass
```

If the test database cannot be reached, stop and report; do not skip database tests.

## Part A — Scaffolding (commit: `chore: sprint 5.3 scaffolding: migration 0011, queries, API specification, types`)

Goal: the schema, the queries, the specification and the generated code of the sprint exist and
the tree builds; behaviour is that of 5.2 wherever a later part adds something.

Files:

1. `internal/store/migrations/sql/0011_limit_integrity.sql` — appendix A1, byte for byte; its
   line in `internal/store/migrations/migrations.sum` (name and SHA-256, as the other lines).
2. `internal/store/queries/sql/limits.sql` — appendix A2, the whole file.
3. `internal/store/queries/sql/usage.sql`, `tenants.sql` — the changes of appendix A2b.
4. `api/openapi/admin.v1.yaml` — appendix A3.
5. `test/isolation/spec.go` — appendix A4.
6. `make generate`.
7. Mechanical adaptation so that `go build ./... && go vet ./...` pass, and nothing more:
   - `internal/limits`: `Rule`, `Limit`, `Query` and `UsageGroup` gain the identifier fields of
     §5.2; the loader fills identifiers and names from the new row shape; the ledger reader
     reads `ProviderID` and `ModelID`. Matching still compares what it compared (C1 changes it).
   - `internal/admin`: the rule handlers call `ResolveLimitSubject` and `ResolveLimitScope` and
     pass identifiers to `CreateLimitRule` and `CreatePlatformLimitRule` (empty string to nil);
     row conversions add `provider_id` and `model_id`; `LimitSubjectExists` and
     `LimitScopeExists` are gone. Tenant handlers read the three new columns into the resource
     with `time_zone_in_force` equal to `time_zone` for now.
   - `internal/ledger/rows.go` and `internal/usage`: the field `LimitState` (JSON `limit_state`,
     null when empty); `internal/admin/usage.go`: the field and the filter.
   - `test/dbtest/limits.go`: `SeedLimitRule` and `SeedPlatformLimit` resolve `subjectID`,
     `Provider` and `Model` to identifiers in the tenant's (or platform) session and fail the
     test when one does not exist; `SetLimitRule` and `DeleteLimitRule` address the rule the same
     way. `dbtest.MigrateTo(tb, n)` applies the first `n` embedded migrations.
   - New operations `getTenantTimeZone` and `setTenantTimeZone` are registered with their
     permission and return `errNotImplemented`-style 500 until C6 (the route and permission
     tests must already see them).

Verification:

```sh
make generate && go build ./... && go vet ./...
make generate-check                     # once the generated code is staged
go test -count=1 ./internal/store/... ./test/isolation -run 'TestMigrat|TestSchemaLint|TestDefaultSpec'
```

`TestSchemaLint` must report nothing. Tests of 5.1 and 5.2 that seed rules for subjects or
scopes that do not exist fail from here on: part B fixes their fixtures.

### Appendix A1 — `internal/store/migrations/sql/0011_limit_integrity.sql`

```sql
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
    FOREACH t IN ARRAY COALESCE(p_tenant_ids, ARRAY[]::uuid[]) LOOP
        PERFORM pg_catalog.set_config('app.tenant_id', t::text, true);
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
```

### Appendix A2 — `internal/store/queries/sql/limits.sql`

```sql
-- Limit rules, the events of quotas and budgets, and the ledger aggregates their counters are
-- rebuilt from. Every statement runs in a store transaction helper; none names the tenant in a
-- WHERE clause: the policies do (TEN-04). A rule row names its subject and its scope by
-- identifier (migration 0011); every statement that returns a rule also returns subject_id (the
-- service account ID, the key ID of the virtual key, or the ID of the subject mapping) and the
-- names of the provider and the model, read through the foreign keys.

-- Gateway pool, session of the tenant whose rules are loaded. platform_limit_rule shows the
-- defaults and the tenant's own rows in that session.

-- name: ListLimitRules :many
SELECT r.id, r.subject_kind,
       COALESCE(r.service_account_id::text, k.key_id, r.subject_mapping_id::text, '')::text AS subject_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds
  FROM pandokos.limit_rule r
  LEFT JOIN pandokos.virtual_key k ON k.tenant_id = r.tenant_id AND k.id = r.virtual_key_id
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id
 ORDER BY r.id
 LIMIT 5001;

-- name: ListPlatformLimitRules :many
SELECT r.id, (r.tenant_id IS NULL)::boolean AS is_default,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds
  FROM pandokos.platform_limit_rule r
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id
 ORDER BY r.tenant_id NULLS FIRST, r.id;

-- The time zone of the tenant of the session: the zone that is or will be in force, the one in
-- force until effective_from when a change is pending ('' is the default of the installation).
-- Gateway pool (with the rules), admin pool (capability check, time zone operations).

-- name: GetTenantTimeZone :one
SELECT t.time_zone, t.time_zone_previous, t.time_zone_effective_from
  FROM pandokos.tenant t
 WHERE t.id = pandokos.current_tenant_id();

-- Admin pool. The row is locked: two changes of the zone do not interleave. In a tenant session
-- the row is the tenant's own; in the platform scope the tenant is named. db_now is the time the
-- trigger of the table compares with: the instant of a change is computed from it.

-- name: LockTenantTimeZone :one
SELECT t.id, t.time_zone, t.time_zone_previous, t.time_zone_effective_from, now()::timestamptz AS db_now
  FROM pandokos.tenant t
 WHERE t.id = sqlc.arg(id)::uuid
   FOR UPDATE;

-- name: SetTenantTimeZone :one
UPDATE pandokos.tenant
   SET time_zone                = sqlc.arg(time_zone)::text,
       time_zone_previous       = sqlc.arg(time_zone_previous)::text,
       time_zone_effective_from = sqlc.narg(time_zone_effective_from)::timestamptz
 WHERE id = sqlc.arg(id)::uuid
RETURNING id, time_zone, time_zone_previous, time_zone_effective_from;

-- Gateway pool, session of the tenant. A row that exists already is left alone: recording is
-- idempotent per rule, period, kind and percentage.

-- name: InsertLimitEvent :execrows
INSERT INTO pandokos.limit_event (tenant_id, kind, rule_id, rule_level, subject_kind, subject_id, provider, model,
                                  metric, currency, period_start, period_end, percent, limit_value, observed)
VALUES (pandokos.current_tenant_id(), sqlc.arg(kind)::text, sqlc.arg(rule_id)::uuid, sqlc.arg(rule_level)::text,
        sqlc.arg(subject_kind)::text, sqlc.arg(subject_id)::text, sqlc.arg(provider)::text, sqlc.arg(model)::text,
        sqlc.arg(metric)::text, sqlc.arg(currency)::text, sqlc.arg(period_start)::timestamptz,
        sqlc.arg(period_end)::timestamptz, sqlc.arg(percent)::smallint, sqlc.arg(limit_value)::bigint,
        sqlc.arg(observed)::numeric)
ON CONFLICT DO NOTHING;

-- Gateway pool, session of the tenant: the audit rows of the tenant's first unlimited request of
-- a day, through the one function the gateway role may execute for it. recorded is false when the
-- day had its row already.

-- name: RecordUnlimitedTraffic :one
SELECT pandokos.record_unlimited_traffic(sqlc.arg(day)::date, sqlc.arg(time_zone)::text, sqlc.arg(provider)::text,
                                         sqlc.arg(model)::text, sqlc.arg(request_id)::text)::boolean AS recorded;

-- Worker pool (counters and capability check), session of the tenant. What the ledger holds for
-- the requests that started in [from_time, to_time), by the dimensions a rule can name: the
-- service account, the key, the provider and the model, the last two by identifier. The provider
-- of a record is the provider that has its name now; the model is the one the record names by ID.
-- Rows that started before live_from are summed with minute 0; later rows by the minute they
-- started in (Unix time / 60). A request counts when a credential was opened for it; its tokens
-- are those the cost was computed on; its cost counts when it is known.

-- name: AggregateUsageForLimits :many
SELECT (CASE WHEN u.started_at >= sqlc.arg(live_from)::timestamptz
             THEN floor(extract(epoch FROM u.started_at) / 60)::bigint ELSE 0 END)::bigint AS minute,
       u.principal_id, u.key_id, COALESCE(p.id::text, '')::text AS provider_id,
       COALESCE(u.model_id::text, '')::text AS model_id, COALESCE(u.currency, '')::text AS currency,
       count(*) FILTER (WHERE u.credential_id IS NOT NULL)::bigint AS requests,
       COALESCE(sum(COALESCE(u.input_tokens, u.estimated_input_tokens, 0)
                    + COALESCE(u.output_tokens, u.estimated_output_tokens, 0))
                FILTER (WHERE u.credential_id IS NOT NULL), 0)::numeric AS tokens,
       COALESCE(sum(u.cost) FILTER (WHERE u.credential_id IS NOT NULL), 0)::numeric AS cost
  FROM pandokos.usage_record u
  LEFT JOIN pandokos.provider p ON p.name = u.provider
 WHERE u.started_at >= sqlc.arg(from_time)::timestamptz
   AND u.started_at < sqlc.arg(to_time)::timestamptz
 GROUP BY 1, u.principal_id, u.key_id, p.id, u.model_id, COALESCE(u.currency, '')
HAVING count(*) FILTER (WHERE u.credential_id IS NOT NULL) > 0;

-- Administration API, tenant session (admin pool).

-- The lock of every statement that creates rules of a tenant or deletes them with their subject:
-- a rule is not created for a subject while that subject is being removed.

-- name: LockLimitRules :exec
SELECT pg_catalog.pg_advisory_xact_lock(
           pg_catalog.hashtextextended('pandokos.limit_rule:' || pandokos.current_tenant_id()::text, 0));

-- name: CountLimitRules :one
SELECT count(*)::bigint FROM pandokos.limit_rule;

-- The subject a creation names, as the identifier of its row: a service account by ID; for 'key'
-- a virtual key that is not revoked, by key ID, or a subject mapping by ID. All three are empty
-- for the tenant and for a subject that is not in this tenant.

-- name: ResolveLimitSubject :one
SELECT COALESCE((SELECT s.id::text FROM pandokos.service_account s
                  WHERE sqlc.arg(subject_kind)::text = 'service_account'
                    AND s.id::text = sqlc.arg(subject_id)::text), '')::text AS service_account_id,
       COALESCE((SELECT v.id::text FROM pandokos.virtual_key v
                  WHERE sqlc.arg(subject_kind)::text = 'key' AND v.key_id = sqlc.arg(subject_id)::text
                    AND v.revoked_at IS NULL), '')::text AS virtual_key_id,
       COALESCE((SELECT sm.id::text FROM pandokos.subject_mapping sm
                  WHERE sqlc.arg(subject_kind)::text = 'key'
                    AND sm.id::text = sqlc.arg(subject_id)::text), '')::text AS subject_mapping_id;

-- The scope a creation names, as identifiers. No row: no such provider. model_id is empty when
-- model is empty or names no model of the provider. Tenant session or platform scope.

-- name: ResolveLimitScope :one
SELECT pr.id::text AS provider_id,
       COALESCE((SELECT mo.id::text FROM pandokos.model mo
                  WHERE mo.provider_id = pr.id AND mo.name = sqlc.arg(model)::text), '')::text AS model_id
  FROM pandokos.provider pr
 WHERE pr.name = sqlc.arg(provider)::text;

-- The currencies of the catalogue prices that can be in force for a scope: for each model of the
-- scope, the version in force now and every version that starts later. In a tenant session with
-- visible_only, the models are those the tenant can be given (available to all, or granted: the
-- policy of model_tenant_grant shows the tenant's grants only); otherwise every model of the
-- scope. Sprint 5.4 adds the currencies of the tenant's own price list to this set.

-- name: ListPriceCurrenciesForScope :many
SELECT DISTINCT v.currency
  FROM pandokos.price_version v
  JOIN pandokos.model mo ON mo.id = v.model_id
 WHERE (sqlc.narg(provider_id)::uuid IS NULL OR mo.provider_id = sqlc.narg(provider_id)::uuid)
   AND (sqlc.narg(model_id)::uuid IS NULL OR mo.id = sqlc.narg(model_id)::uuid)
   AND (NOT sqlc.arg(visible_only)::boolean OR mo.availability = 'all'
        OR EXISTS (SELECT 1 FROM pandokos.model_tenant_grant g WHERE g.model_id = mo.id))
   AND (v.valid_from > now()
        OR v.valid_from = (SELECT max(c.valid_from) FROM pandokos.price_version c
                            WHERE c.model_id = v.model_id AND c.valid_from <= now()))
 ORDER BY v.currency;

-- name: CreateLimitRule :one
WITH r AS (
    INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, service_account_id, virtual_key_id, subject_mapping_id,
                                     provider_id, model_id, metric, limit_value, currency, enforce, thresholds)
    VALUES (pandokos.current_tenant_id(), sqlc.arg(subject_kind)::text, sqlc.narg(service_account_id)::uuid,
            sqlc.narg(virtual_key_id)::uuid, sqlc.narg(subject_mapping_id)::uuid, sqlc.narg(provider_id)::uuid,
            sqlc.narg(model_id)::uuid, sqlc.arg(metric)::text, sqlc.arg(limit_value)::bigint,
            sqlc.arg(currency)::text, sqlc.arg(enforce)::boolean, sqlc.arg(thresholds)::smallint[])
    RETURNING *
)
SELECT r.id, r.subject_kind,
       COALESCE(r.service_account_id::text, k.key_id, r.subject_mapping_id::text, '')::text AS subject_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM r
  LEFT JOIN pandokos.virtual_key k ON k.tenant_id = r.tenant_id AND k.id = r.virtual_key_id
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id;

-- name: GetLimitRule :one
SELECT r.id, r.subject_kind,
       COALESCE(r.service_account_id::text, k.key_id, r.subject_mapping_id::text, '')::text AS subject_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM pandokos.limit_rule r
  LEFT JOIN pandokos.virtual_key k ON k.tenant_id = r.tenant_id AND k.id = r.virtual_key_id
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id
 WHERE r.id = sqlc.arg(id)::uuid;

-- name: LockLimitRule :one
SELECT r.id, r.subject_kind,
       COALESCE(r.service_account_id::text, k.key_id, r.subject_mapping_id::text, '')::text AS subject_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM pandokos.limit_rule r
  LEFT JOIN pandokos.virtual_key k ON k.tenant_id = r.tenant_id AND k.id = r.virtual_key_id
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id
 WHERE r.id = sqlc.arg(id)::uuid
   FOR UPDATE OF r;

-- subject_id, provider and model filter by what the API shows: the subject as above, the names
-- of the scope.

-- name: ListLimitRulesPage :many
SELECT r.id, r.subject_kind,
       COALESCE(r.service_account_id::text, k.key_id, r.subject_mapping_id::text, '')::text AS subject_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM pandokos.limit_rule r
  LEFT JOIN pandokos.virtual_key k ON k.tenant_id = r.tenant_id AND k.id = r.virtual_key_id
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id
 WHERE (sqlc.arg(subject_kind)::text = '' OR r.subject_kind = sqlc.arg(subject_kind)::text)
   AND (sqlc.arg(subject_id)::text = ''
        OR COALESCE(r.service_account_id::text, k.key_id, r.subject_mapping_id::text, '') = sqlc.arg(subject_id)::text)
   AND (sqlc.arg(metric)::text = '' OR r.metric = sqlc.arg(metric)::text)
   AND (sqlc.arg(provider)::text = '' OR p.name = sqlc.arg(provider)::text)
   AND (sqlc.arg(model)::text = '' OR m.name = sqlc.arg(model)::text)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN r.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN r.id < sqlc.arg(after_id)::uuid
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN r.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN r.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: UpdateLimitRule :one
WITH r AS (
    UPDATE pandokos.limit_rule
       SET limit_value = sqlc.arg(limit_value)::bigint,
           enforce     = sqlc.arg(enforce)::boolean,
           thresholds  = sqlc.arg(thresholds)::smallint[]
     WHERE id = sqlc.arg(id)::uuid
    RETURNING *
)
SELECT r.id, r.subject_kind,
       COALESCE(r.service_account_id::text, k.key_id, r.subject_mapping_id::text, '')::text AS subject_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM r
  LEFT JOIN pandokos.virtual_key k ON k.tenant_id = r.tenant_id AND k.id = r.virtual_key_id
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id;

-- name: DeleteLimitRule :one
WITH r AS (
    DELETE FROM pandokos.limit_rule
     WHERE id = sqlc.arg(id)::uuid
    RETURNING *
)
SELECT r.id, r.subject_kind,
       COALESCE(r.service_account_id::text, k.key_id, r.subject_mapping_id::text, '')::text AS subject_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM r
  LEFT JOIN pandokos.virtual_key k ON k.tenant_id = r.tenant_id AND k.id = r.virtual_key_id
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id;

-- The rules of a subject that is going away, deleted before it in the same transaction: those of
-- a virtual key (by key ID; '' for none), of a subject mapping, or of every subject mapping of a
-- tenant-bound issuer. The caller holds LockLimitRules and writes one audit row per row returned.

-- name: DeleteLimitRulesOfSubject :many
WITH r AS (
    DELETE FROM pandokos.limit_rule d
     WHERE (sqlc.arg(key_id)::text <> ''
            AND d.virtual_key_id = (SELECT v.id FROM pandokos.virtual_key v WHERE v.key_id = sqlc.arg(key_id)::text))
        OR d.subject_mapping_id = sqlc.narg(subject_mapping_id)::uuid
        OR d.subject_mapping_id IN (SELECT sm.id FROM pandokos.subject_mapping sm
                                     WHERE sm.trusted_issuer_id = sqlc.narg(trusted_issuer_id)::uuid)
    RETURNING *
)
SELECT r.id, r.subject_kind,
       COALESCE(r.service_account_id::text, k.key_id, r.subject_mapping_id::text, '')::text AS subject_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM r
  LEFT JOIN pandokos.virtual_key k ON k.tenant_id = r.tenant_id AND k.id = r.virtual_key_id
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id
 ORDER BY r.id;

-- The ceilings that apply to the tenant of the session: the defaults and its own rows.

-- name: ListTenantPlatformLimitRules :many
SELECT r.id, (r.tenant_id IS NULL)::boolean AS is_default,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM pandokos.platform_limit_rule r
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id
 WHERE (NOT sqlc.arg(after)::boolean OR r.id > sqlc.arg(after_id)::uuid)
 ORDER BY r.id
 LIMIT sqlc.arg(page_size)::integer;

-- name: ListLimitEvents :many
SELECT e.id, e.kind, e.rule_id, e.rule_level, e.subject_kind, e.subject_id, e.provider, e.model, e.metric,
       e.currency, e.period_start, e.period_end, e.percent, e.limit_value, e.observed, e.occurred_at
  FROM pandokos.limit_event e
 WHERE (sqlc.arg(kind)::text = '' OR e.kind = sqlc.arg(kind)::text)
   AND (sqlc.arg(rule_id)::text = '' OR e.rule_id::text = sqlc.arg(rule_id)::text)
   AND (NOT sqlc.arg(after)::boolean OR e.id < sqlc.arg(after_id)::uuid)
 ORDER BY e.id DESC
 LIMIT sqlc.arg(page_size)::integer;

-- Administration API, platform session (admin pool).

-- name: CreatePlatformLimitRule :one
WITH r AS (
    INSERT INTO pandokos.platform_limit_rule (tenant_id, provider_id, model_id, metric, limit_value, currency, enforce,
                                              thresholds)
    VALUES (sqlc.narg(tenant_id)::uuid, sqlc.narg(provider_id)::uuid, sqlc.narg(model_id)::uuid,
            sqlc.arg(metric)::text, sqlc.arg(limit_value)::bigint, sqlc.arg(currency)::text,
            sqlc.arg(enforce)::boolean, sqlc.arg(thresholds)::smallint[])
    RETURNING *
)
SELECT r.id, r.tenant_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM r
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id;

-- name: GetPlatformLimitRule :one
SELECT r.id, r.tenant_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM pandokos.platform_limit_rule r
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id
 WHERE r.id = sqlc.arg(id)::uuid;

-- name: LockPlatformLimitRule :one
SELECT r.id, r.tenant_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM pandokos.platform_limit_rule r
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id
 WHERE r.id = sqlc.arg(id)::uuid
   FOR UPDATE OF r;

-- applies_to: '' every row, 'default' the rows without tenant, otherwise a tenant ID.

-- name: ListPlatformLimitRulesPage :many
SELECT r.id, r.tenant_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM pandokos.platform_limit_rule r
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id
 WHERE (sqlc.arg(applies_to)::text = ''
        OR (sqlc.arg(applies_to)::text = 'default' AND r.tenant_id IS NULL)
        OR r.tenant_id::text = sqlc.arg(applies_to)::text)
   AND (sqlc.arg(metric)::text = '' OR r.metric = sqlc.arg(metric)::text)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN r.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN r.id < sqlc.arg(after_id)::uuid
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN r.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN r.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: UpdatePlatformLimitRule :one
WITH r AS (
    UPDATE pandokos.platform_limit_rule
       SET limit_value = sqlc.arg(limit_value)::bigint,
           enforce     = sqlc.arg(enforce)::boolean,
           thresholds  = sqlc.arg(thresholds)::smallint[]
     WHERE id = sqlc.arg(id)::uuid
    RETURNING *
)
SELECT r.id, r.tenant_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM r
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id;

-- name: DeletePlatformLimitRule :one
WITH r AS (
    DELETE FROM pandokos.platform_limit_rule
     WHERE id = sqlc.arg(id)::uuid
    RETURNING *
)
SELECT r.id, r.tenant_id,
       COALESCE(r.provider_id::text, '')::text AS provider_id, COALESCE(r.model_id::text, '')::text AS model_id,
       COALESCE(p.name, '')::text AS provider, COALESCE(m.name, '')::text AS model,
       r.metric, r.limit_value, r.currency, r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM r
  LEFT JOIN pandokos.provider p ON p.id = r.provider_id
  LEFT JOIN pandokos.model m ON m.id = r.model_id;

-- The rules of the subject mappings of a platform-level issuer, in every tenant, deleted with one
-- audit row each in the log of its tenant, before the issuer goes (platform session; the function
-- is a definer function, migration 0011). The actor is the platform principal of the request.

-- name: DeletePlatformIssuerLimitRules :one
SELECT pandokos.delete_platform_issuer_limit_rules(sqlc.arg(issuer_id)::uuid,
           (SELECT COALESCE(array_agg(t.id), ARRAY[]::uuid[]) FROM pandokos.tenant t),
           sqlc.arg(actor_id)::text, sqlc.arg(actor_name)::text, sqlc.arg(key_id)::text,
           sqlc.arg(request_id)::text)::bigint AS rules;
```

### Appendix A2b — changed statements of other query files

`usage.sql`, `InsertUsageRecords`: add `limit_state` as the last column of the `INSERT` column
list, `r.limit_state` as the last expression of the `SELECT` list and `limit_state text` as the
last member of the `jsonb_to_recordset` column definition (after `metadata_dropped`).

`usage.sql`, `ListUsageRecords`: add `COALESCE(u.limit_state, '')::text AS limit_state` as the
last output column, and after the `provider` filter the line

```sql
   AND (sqlc.arg(limit_state)::text = '' OR u.limit_state = sqlc.arg(limit_state)::text)
```

`tenants.sql`: every statement that returns a tenant (`CreateTenant`, `GetTenant`,
`GetTenantForUpdate`, `ListTenants`, `UpdateTenant`, `SetTenantState`) appends `time_zone,
time_zone_previous, time_zone_effective_from` to its column list. `CreateTenant` inserts `time_zone`:

```sql
-- name: CreateTenant :one
INSERT INTO pandokos.tenant (name, display_name, time_zone)
VALUES (sqlc.arg(name)::text, sqlc.arg(display_name)::text, sqlc.arg(time_zone)::text)
RETURNING id, name, display_name, state, created_at, updated_at, state_changed_at,
          time_zone, time_zone_previous, time_zone_effective_from;
```

`UpdateTenant` does not touch the zone: the handler calls `SetTenantTimeZone` (appendix A2) in
the same transaction.

### Appendix A3 — `api/openapi/admin.v1.yaml`

New path, after `/v1/tenants/{tenant}/limit-events`:

```yaml
  /v1/tenants/{tenant}/time-zone:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: getTenantTimeZone
      tags: [limits]
      summary: Read the time zone in which the tenant's days and months start
      x-pandokos-permission: limit:read
      responses:
        "200": {$ref: '#/components/responses/TenantTimeZone'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
    put:
      operationId: setTenantTimeZone
      tags: [limits]
      summary: Set the time zone in which the tenant's days and months start
      description: >-
        The change takes effect at the start of the next month in the zone in force; until then
        effective_from says when. Setting the zone in force again withdraws a pending change. An
        empty time_zone is the default of the installation.
      x-pandokos-permission: limit:write
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/TenantTimeZoneSet'}
      responses:
        "200": {$ref: '#/components/responses/TenantTimeZone'}
        "400": {$ref: '#/components/responses/InvalidRequest'}
        "401": {$ref: '#/components/responses/Unauthenticated'}
        "403": {$ref: '#/components/responses/Forbidden'}
        "404": {$ref: '#/components/responses/NotFound'}
        "413": {$ref: '#/components/responses/RequestTooLarge'}
        "415": {$ref: '#/components/responses/UnsupportedMediaType'}
        "500": {$ref: '#/components/responses/Internal'}
        "503": {$ref: '#/components/responses/Unavailable'}
```

New response `TenantTimeZone` ("The time zone of the tenant.", schema `TenantTimeZone`) and
schemas:

```yaml
    TimeZoneName:
      type: string
      maxLength: 64
      pattern: '^([A-Za-z][A-Za-z0-9_+-]{0,31}(/[A-Za-z0-9_+-]{1,31}){0,2})?$'
      description: An IANA time zone name; empty for the default of the installation.
    TenantTimeZone:
      type: object
      additionalProperties: false
      required: [time_zone, in_force]
      properties:
        time_zone: {$ref: '#/components/schemas/TimeZoneName'}
        in_force: {type: string, description: The name of the zone in force now; the default of the installation resolved.}
        effective_from: {type: string, format: date-time, description: Present while a change is pending; the instant from which time_zone is in force.}
    TenantTimeZoneSet:
      type: object
      additionalProperties: false
      required: [time_zone]
      properties:
        time_zone: {$ref: '#/components/schemas/TimeZoneName'}
    LimitState:
      type: string
      enum: [limited, unlimited, unverified, not_evaluated]
```

Changed schemas (members added; nothing removed):

| Schema | Change |
|---|---|
| `Tenant` | `time_zone` (`TimeZoneName`) and `time_zone_in_force` (string), both required; `time_zone_effective_from` (date-time, not required) |
| `TenantCreate`, `TenantUpdate` | `time_zone` (`TimeZoneName`), optional |
| `LimitRule`, `PlatformLimitRule` | `provider_id`, `model_id`: `{type: string, description: 'Empty when the rule is not scoped to a provider (a model).'}`, both required. The description of `subject_id` of `LimitRule` adds "a key that is revoked cannot be named" |
| `UsageRecord` | `limit_state`: `{type: string, description: 'limited, unlimited, unverified or not_evaluated; empty for a record written before the field existed.'}`, required |

Changed operations: `listUsageRecords` gains the query parameter `limit_state` (schema
`LimitState`; the cursor sentence names it); `updateTenant` — summary "Change the name, the
display name or the time zone of a tenant"; `deleteProvider` and `deleteModel` — the description
adds "or while a limit rule of any tenant or of the platform references it; errors then names
limit_rules"; `revokeVirtualKey`, `deleteSubjectMapping`, `deleteTokenIssuer`,
`deletePlatformIssuer` — the description adds "The limit rules of the key (of the mapping, of
its mappings) are deleted with it."; `createLimit`, `updateLimit`, `createPlatformLimit`,
`updatePlatformLimit` — the description adds the currency check of §5.8. The descriptions of
`limit:read` and `limit:write` in `internal/authz` add "and the tenant's time zone".

### Appendix A4 — `test/isolation/spec.go`

```go
// Grants (replace)
"limit_rule": {
    "pandokos_gateway": {"SELECT"},
    "pandokos_admin": {"DELETE", "INSERT(currency,enforce,limit_value,metric,model_id,provider_id,service_account_id,subject_kind,subject_mapping_id,tenant_id,thresholds,virtual_key_id)",
        "SELECT", "UPDATE(enforce,limit_value,thresholds)"},
},
"platform_limit_rule": {
    "pandokos_gateway": {"SELECT"},
    "pandokos_admin": {"DELETE", "INSERT(currency,enforce,limit_value,metric,model_id,provider_id,tenant_id,thresholds)",
        "SELECT", "UPDATE(enforce,limit_value,thresholds)"},
},
// "tenant": pandokos_admin becomes
//   {"INSERT(display_name,name,time_zone)", "SELECT", "UPDATE(display_name,name,state,time_zone,time_zone_effective_from,time_zone_previous)"}
// "usage_record": the INSERT column list of pandokos_gateway gains limit_state (between key_id and metadata)

// Fixture (replace): a rule of the tenant itself needs no subject row
"limit_rule": {
    Insert: `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, metric, limit_value)
VALUES ($1::uuid, 'tenant', 'requests_per_minute', 10) RETURNING id::text`,
    Update: `limit_value = 11`,
},

// SecurityDefiner: add "delete_platform_issuer_limit_rules", "record_unlimited_traffic".
```

`TestDefaultSpecMatchesDesign` gets the same values. Nothing is added to `Global`, `Views` or
`AppendOnly`.

## Part B — Tests first (commit: `test: sprint 5.3 tests, failing`)

Every test of §9 is written under its stated name, compiles, and fails for the reason it tests
(or is skipped by nothing). The pull request quotes the failing output of this commit.

### B1. Helpers and fixtures

- `dbtest`: `SeedServiceAccount`, `SeedVirtualKey` (returns the key ID and the row ID; an option
  revokes it), `SeedTrustedIssuer`, `SeedPlatformIssuer`, `SeedSubjectMapping`, `SeedModel`,
  `SeedPrice(model, currency, validFrom)` where they do not exist yet; `SetTenantZone(tenant,
  zone, previous, effectiveFrom)` on a superuser connection with the platform scope set.
- `dbtest.MigrateTo` (part A) and `SeedRulesAt0010(tb, d)`: plain SQL on a superuser connection
  against the schema of 0010, one rule per row of §5.10 in two tenants plus ceilings; it returns
  what T-692 expects for each.
- `limitstest.Real`: a `TenantCalendar` per tenant, `SetZone(tenant, zone, at)`, the reference
  model of 5.2 extended with the zone in force at each instant.
- Fixtures of earlier tests: every `SeedLimitRule` call with an invented service account, key or
  mapping ID, or a provider or model name that is not in the catalogue, creates the subject or
  the catalogue entry first (`grep -rn 'SeedLimitRule\|SeedPlatformLimit' --include=*_test.go`
  lists them; the seeded providers `openai` and `anthropic` and their models exist already).
  The cases "a rule whose subject does not exist matches nothing" are deleted: the schema now
  refuses the row (T-694).

### B2. Where each test lives

| Tests | File |
|---|---|
| T-692 | `internal/store/migrations/upgrade_0011_db_test.go` |
| T-693 to T-698, T-719 | `test/isolation/{limits,issuers,audit,tenant,usage,api}_test.go` |
| T-699, T-700, T-705 | `internal/limits/{zones,zone_property,reconcile}_test.go` |
| T-701, T-702 | `internal/limits/{rules,usage}_test.go` |
| T-703, T-704 | `internal/limits/{load,usage}_db_test.go` |
| T-706, T-707 | `internal/resolver/{limits,unlimited}_test.go` |
| T-708 to T-712 | `internal/gateway/{limit_state,unlimited,tenant_zone,scope_id,no_price}_test.go` (the integration files with the database carry the suffix the package already uses) |
| T-713 to T-718, T-720, T-721 | `internal/admin/{limits,limit_subjects,catalogue_delete,budget_currency,time_zone,capability,usage}_db_test.go` |
| T-722 | `internal/ledger/writer_db_test.go` |
| T-723 | `internal/cli/limits_test.go` |
| T-724, T-725 | `test/e2e/{integrity,upgrade}_test.go` |
| T-R45 to T-R47 | `test/repo/{docs,adr}_test.go` |

T-714 needs a seam to fail a transaction after the rules are deleted: a package variable in
`internal/admin` (`afterRuleDeletion func() error`, nil in production), set through
`export_test.go` as the rule bound of 5.2 is. T-725 builds the binary once and drives `pandokos
migrate` against a database prepared with `MigrateTo(10)` and a rule written by SQL.

Verification: `go vet ./...` passes; `go test -count=1 ./... 2>&1 | tee /tmp/b.log` fails in the
new tests only (list them in the commit message).

## Part C — Implementation

### C1. Identifiers in the limiter — `feat: limit rules matched by identifier`

Files: `internal/limits/{limits,rules,load,usage,periods,memory,events}.go`.

- `scopeKey` and `counterKey` hold provider ID and model ID. `RuleSet.For` builds its three
  scopes from `Query.ProviderID` and `Query.ModelID`. `groupMatches` compares identifiers; a
  group with an empty model ID matches the scopes *all* and *provider* only.
- `validRule`: a model ID needs a provider ID.
- `Event` and the refusal keep the names (`Limit.Provider`, `Limit.Model`); `without` in the
  resolver and every equality of limits compare identifiers.
- The loader keeps skipping what this version does not know.

Verification: `go test -race -count=1 ./internal/limits/ -run 'TestResolve|TestUsageSum|TestLoadRules|TestLedgerUsage|TestMemory|TestProperty'`
(T-701 to T-704 pass, the calendar parts of T-703 excepted).

### C2. The calendar of a tenant — `feat: a time zone per tenant for quota and budget periods`

Files: `internal/limits/zones.go` (new), `rules.go`, `load.go`, `cache.go`, `reconcile.go`,
`periods.go`.

- `Zones` and `TenantCalendar` as in §5.7. `Zones.Calendar` builds a `Calendar` once per name
  (`time.LoadLocation` on the embedded database, then `NewCalendar`) under a mutex; the map is
  bounded by the number of zone names.
- `LoadRules(ctx, db, id, zones)` reads `GetTenantTimeZone` in the same tenant transaction and
  sets `RuleSet.Calendar`: `Current` from `time_zone`; when `time_zone_effective_from` is set,
  `Previous` from `time_zone_previous` and `From`. `RuleCacheOptions` gains `Zones`.
- `RuleSet.For(q)` (one argument) uses `s.Calendar.Of(kind, q.Start)`; a zero calendar yields
  no period limit.
- `NextChange(inForce *Calendar, now time.Time) time.Time` returns `inForce.Of(PerMonth,
  now).End` — the one place that computes the instant of §5.7.
- `ReconcilerOptions` loses `Calendar`. In `reconcile`, the cut points of `[cut, next)` are the
  `Start` and `End` of every period of `Store.Periods(t)` that lie strictly inside it, sorted
  and deduplicated; one ledger query per piece, as before.
- Nothing else of §5.4 and §5.6 of 5.2 changes: a counter is still identified by its period
  start, loaded when its period started before the cut, created empty otherwise.

Verification: `go test -race -count=20 ./internal/limits/`; T-699, T-700, T-703, T-705 pass;
T-700 with three more values of `PANDOKOS_TEST_SEED` (quote them).

### C3. The pipeline — `feat: limit state in the usage record; unlimited traffic audited`

Files: `internal/gateway/{ports,limits,handler,record,estimate}.go`, `internal/resolver/{limits,
unlimited,catalogue}.go`, `internal/usage`, `internal/ledger/rows.go`.

- `gateway.LimitState` (`limited`, `unlimited`, `unverified`, `not_evaluated`).
  `Limits.Reserve` returns `(Reservation, *LimitRefusal, LimitState, error)`. The resolver
  returns the state of §5.5: `unlimited` when `For` is empty; `unverified` with every error and
  whenever a limit was left out for a failed load or a failed reserve; `limited` otherwise
  (refusals included). The handler starts each request at `not_evaluated`, stores what `Reserve`
  returns, and writes it to `usage.Record.LimitState` and the log field `limit_state`.
- `Target.ProviderID` from the snapshot; `LimitEstimate.ProviderID`, `.ModelID`.
- `LimitRefusal.PriceCurrency`: with code `no_price`, `Currency` is the currency of the first
  enforced budget that cannot measure the request and `PriceCurrency` the estimate's
  (`e.Cost.Currency`, empty without a price); the log line carries `limit_currency` and
  `price_currency`. Body and headers of the 409 do not change.
- `resolver.UnlimitedAuditor` (§5.6): `NewUnlimitedAuditor(db, logger, now)`, `Note(tenant,
  day, zone, provider, model, requestID)` (non-blocking), `Run(ctx)`, `Close(ctx)`. Memory: map
  tenant → last day recorded or offered, at most 10,000 entries (emptied when full); queue 256;
  write timeout 2 s through `RecordUnlimitedTraffic` in `WithTenant`; on error or a full queue
  the entry is removed and the tenant is not offered again for 10 s. One log line per row
  written (`unlimited traffic recorded`, info: tenant, day) and one debug line per failure.
  `LimitsOptions` gains `Unlimited` (an interface with `Note`); `Reserve` calls it when the
  state is `unlimited`, with `rules.Calendar.Day(e.Start)` and the name of the zone in force.

Verification: `go test -race -count=1 ./internal/gateway/ ./internal/resolver/ ./internal/ledger/`;
T-706 to T-712 and T-722 pass.

### C4. Rules in the administration API — `feat: limit rules reference their subject and scope; budget currency checked on write`

Files: `internal/admin/{limits,limit_fields,platform_limits,budget_currency,providers,models,
convert_catalog,problem,admin}.go`.

- `createLimit`: `LockLimitRules`, count, `ResolveLimitSubject` (all three empty for a
  non-tenant kind: 400 on `subject_id` with the message of 5.2), `ResolveLimitScope` (no row:
  400 on `provider`; a model named and not resolved, or not visible to the tenant: 400 on
  `model`), the currency check for a cost metric, `CreateLimitRule`. `mapLimitError` maps a
  violation of a subject or scope foreign key and of `limit_rule_key_guard` to the same 400s.
- `budgetCurrencies(ctx, q, providerID, modelID *string, tenantSession bool) ([]string, error)`
  wraps `ListPriceCurrenciesForScope` (`visible_only` = `tenantSession`); `checkBudgetCurrency`
  returns the 400 of §5.8. Called by `createLimit`, `updateLimit` (cost metric),
  `createPlatformLimit`, `updatePlatformLimit`.
- Audit detail: `ruleDetail` adds `provider_id` and `model_id` when not empty.
- `stateError{fields}`: an `invalid_state` with `errors`; the `fail` hook writes them.
  `mapCatalogError` returns it for the four constraint names of §5.4 and keeps
  `errInvalidState` for every other foreign key.

Verification: T-713, T-715, T-716 pass; `go test -count=1 ./internal/admin/ -run 'Limit|Catalog|Provider|Model'`.

### C5. Subjects and their rules — `feat: the rules of a revoked key, a deleted mapping or issuer are deleted and audited`

Files: `internal/admin/{limit_subjects,virtual_keys,subject_mappings,token_issuers,
platform_issuers}.go`.

- `deleteSubjectRules(ctx, tx, q, tid, par, cause, causeID, reason) (int, error)`: calls
  `DeleteLimitRulesOfSubject`, writes one `limit.delete` per row with `tenantEvent` (so the
  actor and the request ID are those of the operation), `Reason` as given, and `ruleDetail` plus
  `cause`, `cause_id`; then calls the test seam.
- `revokeVirtualKey`: `LockLimitRules`; `RevokeVirtualKey`; when a row was revoked,
  `deleteSubjectRules` by key ID (`key_revoked`, the request's reason) and `limit_rules_deleted`
  in the detail of `virtual_key.revoke`.
- `deleteSubjectMapping`: `LockLimitRules`; `deleteSubjectRules` by mapping
  (`subject_mapping_deleted`); `DeleteSubjectMapping`; the detail.
- `deleteTokenIssuer`: `LockLimitRules`; the issuer lock and count as now; `deleteSubjectRules`
  by issuer (`token_issuer_deleted`); `DeleteTrustedIssuer`; the detail.
- `deletePlatformIssuer`: after the issuer lock and the mapping count,
  `DeletePlatformIssuerLimitRules` with the platform principal (`audit.ActorOf(p)`: ID, name,
  key ID) and the request ID; then `DeletePlatformIssuer`; the detail.
- A mapping that does not exist still answers 404 without touching rules.

Verification: T-714 passes, with the seam (rollback) and the race case; T-695 passes.

### C6. Time zone, capability, usage — `feat: administration of a tenant's time zone; capability check in the tenant's zone`

Files: `internal/admin/{time_zone,tenants,capability,usage,admin}.go`.

- `setTimeZone(ctx, q, tenantID, zone, zones) (row, changed, error)`, used by both routes:

  ```
  cur := LockTenantTimeZone(id)                       // cur.db_now is the clock
  pending := cur.effective_from != nil && cur.db_now < *cur.effective_from
  inForce := cur.time_zone;  if pending { inForce = cur.time_zone_previous }
  zone == cur.time_zone → nothing changes
  zone == inForce       → Set(zone, "", nil)                              // the pending change is withdrawn
  pending               → Set(zone, cur.time_zone_previous, cur.effective_from)   // the instant stays
  otherwise             → Set(zone, inForce, NextChange(zones.Calendar(inForce), cur.db_now))
  ```

  `zone` is validated first with `zones.Calendar(zone)` (400 on `time_zone`). A violation of
  the trigger is an internal error: it means this function is wrong.
- `setTenantTimeZone` runs it in the tenant's session (`inTenantPath`) and audits
  `tenant.time_zone_update`; `updateTenant` runs it in the platform session and adds the change
  to the detail of `tenant.update`; `createTenant` validates and inserts `time_zone`.
- The resources: `in_force` is the name of `zones.Calendar(inForce)` (the default resolved);
  `effective_from` only while pending.
- `budgetCheck`: `limits.LoadRules(ctx, s.db, tid, s.limits.Zones)`, `For(Query{ProviderID,
  ModelID, Start: now})` with the provider's ID from the snapshot and the model's ID when it
  resolved; `Options.Limits.Calendar` is replaced by `Zones`.
- `listUsageRecords`: the field and the filter (400 for a value outside the enum is the
  generated validation).

Verification: T-717, T-718, T-719, T-720, T-721 pass; `TestEveryRouteDeclaresAPermission`
passes.

### C7. CLI — `feat: CLI for the tenant's time zone and the limit state`

Files: `internal/cli/{limits,commands,capability}.go`. The commands of §5.11, each mapped to one
operation; `limit time-zone set` takes exactly one of a zone argument and `--default`; `tenant
update` accepts `--time-zone` alone; tables show `time_zone`, `in_force`, `effective_from` and,
for usage, a `LIMITS` column.

Verification: T-723 and the CLI command coverage test pass.

### C8. Wiring and end to end — `feat: tenant calendars and the unlimited-traffic recorder wired into serve; end-to-end tests`

Files: `cmd/pandokos/serve.go`, `test/e2e`.

- `serve` builds `limits.NewZones(cfg.Limits.Location)` once and gives it to the rule cache and
  to `admin.New`; the reconciler gets no calendar; `resolver.NewUnlimitedAuditor` on the gateway
  pool is passed to `resolver.NewLimits`, run with the background context and closed with the
  event writer. The line `limits ready` names the default zone.
- T-724 and T-725.

Verification: `go test -count=1 ./test/e2e/ -run 'TestRuleIntegrity|TestUpgradeWithRules|TestQuotaCanary|TestMonthlyBudget'`.

## Part D — Lint rules, bench, container (commit: `ci: sprint 5.3 in repository tests and bench targets`)

- T-R40, T-R41, T-R43: the import lists and constants of this sprint (`internal/resolver` may
  import `internal/audit` as it does for failures; `internal/limits` still imports neither the
  ledger nor the administration).
- T-622 (bench): the resolution case uses identifiers; quote allocations per request before and
  after (the lookup of the unlimited recorder must add none on the limited path).
- `make test-container` and `make test-compose` run unchanged; the compose example needs no new
  setting. No workflow file changes.

Verification: `make check`.

## Part E — Documentation and decision records (commit: `docs: sprint 5.3 documentation and ADR amendments`)

### E1. Guides and reference

The pages and sections of §5.11. For `limits.md`: the new section "Two quotas" goes after
"Levels and subjects"; "Periods and the time zone" is rewritten for the tenant's zone with the
change rule, the four consequences of §5.7 and one worked example (UTC to `Asia/Tokyo` on the
10th: nothing until the 1st at 00:00 UTC, then the Tokyo day that began at 15:00 UTC counts its
nine earlier hours); "Budgets" gains the currency check, "prices are set by the platform for
every tenant" and the paragraph on overrides in another currency; "What is not there yet" loses
the zone per tenant and names tenant price lists (5.4). `database.md` section 16 follows
sections 14 and 15: what 0011 changes, the grants, the two functions and why each crosses the
boundary, the locks, **stop, migrate, start**, what the previous binary does, and the query
that lists what the migration deleted:

```sql
SELECT occurred_at, resource_id, detail FROM pandokos.audit_event
 WHERE action = 'limit.delete' AND actor_name = 'migration 0011';   -- in a tenant session
```

### E2. Decision records

Status stays `accepted`; the index is not changed. Each record gets a section **after
"Alternatives considered" and before "History"**, and one history line. The amendments are
accepted with the merge of this sprint, the scope having been decided in the review of
2026-11-18.

**ADR 0049** — section `## Amendment (sprint 5.3)`:

> Decided in the review of 2026-11-18; replaces "there is no foreign key to the subject" of
> decision 1 and the consequence "a rule for a subject that does not exist is idle".
>
> 1. A rule names its subject by typed columns (`service_account_id`, `virtual_key_id`,
>    `subject_mapping_id`) with composite foreign keys `(tenant_id, id)`, and its scope by
>    `provider_id` and `model_id`, in both tables. All are `ON DELETE RESTRICT`: deletions are
>    audited by the application, so nothing cascades. `limit_event` has no foreign key.
> 2. The API takes and shows names; the gateway and the counters match by identifier. A model
>    created with the name of a deleted one is another model.
> 3. Revoking a virtual key, deleting a subject mapping and deleting an issuer with its mappings
>    delete the rules of those subjects in the same transaction, each with an audit row
>    `limit.delete` that names the actor and the cause. A disabled service account keeps its
>    rules. A revoked key has no rule (two triggers, since keys are never deleted).
> 4. A provider or model that a rule of any tenant or of the platform references cannot be
>    deleted (409 `invalid_state`); the foreign key decides, without reading across tenants.
> 5. No job looks for orphan rules; a schema test checks the foreign keys.
> 6. Migration 0011 resolved existing names to what they designated and deleted, with an audit
>    row each, the rules that designated nothing or a revoked key.
> 7. The usage record states how limits treated the request (`limited`, `unlimited`,
>    `unverified`, `not_evaluated`), and a tenant's first unlimited request of a day leaves one
>    audit row `limit.unlimited_traffic` in the tenant's log and in the platform's.

History line: `- <date>: amended by sprint 5.3 (see Amendment).`

**ADR 0050** — section `## Amendment (sprint 5.3)`:

> Decided in the review of 2026-11-18; replaces "one time zone per installation" of decision 1,
> the alternative "a zone per tenant or per rule" and the consequence "changing the zone …
> takes effect at restart" for tenants that have a zone of their own.
>
> 1. A tenant has a time zone (`tenant.time_zone`); `limits.time_zone` is the zone of the
>    tenants that have none. The platform sets it (`tenant:write`), and so does the tenant
>    (`limit:write`); both are audited.
> 2. A change takes effect at the start of the next month in the zone in force, for days and
>    months alike. A period is always a whole civil period of the zone in force at the request's
>    start, and its counter counts the whole range, so the first periods of a new zone include
>    what was used in their hours before the change took effect: moving a boundary never frees a
>    quota. Platform and tenant rules of one tenant still share one calendar.
> 3. A budget is refused on write (400 on `currency`, naming the currencies in use) when no
>    catalogue price that can be in force for its scope is in its currency. Nothing is
>    converted. Sprint 5.4 adds the tenant's own prices to that set.

History line: `- <date>: amended by sprint 5.3 (see Amendment); tenant price lists remain planned for sprint 5.4.`

**ADR 0052** — section `## Amendment (sprint 5.3)`:

> Decided in the review of 2026-11-18; replaces the consequence "a rule whose subject or scope
> later disappears stays and matches nothing".
>
> 1. The existence of a rule's subject and scope is held by foreign keys; the checks on write
>    remain, to answer 400 with one message for "elsewhere" and "nowhere", and a revoked key is
>    not a subject.
> 2. Rules deleted with their subject are audited as `limit.delete` with the actor of that
>    operation and, in the detail, `cause` (`key_revoked`, `subject_mapping_deleted`,
>    `token_issuer_deleted`, `platform_issuer_deleted`) and `cause_id`. For a platform-level
>    issuer a definer function deletes and audits them in each tenant.
> 3. Two audit actions are added: `limit.unlimited_traffic` (tenant and platform log, written by
>    a definer function the gateway role may execute, once per tenant and day) and
>    `tenant.time_zone_update`.
> 4. Two operations are added, `getTenantTimeZone` and `setTenantTimeZone`, under `limit:read`
>    and `limit:write`.

History line: `- <date>: amended by sprint 5.3 (see Amendment).`

History lines only (`- <date>: sprint 5.3 …`): **0013** and **0032** (`limit.delete` carries a
cause; `limit.unlimited_traffic`, `tenant.time_zone_update`; two partial unique indexes on the
audit logs); **0026** (a data migration lifts forced row-level security on the tables it
touches, inside its transaction); **0027** (two definer functions; a tenant session may update
the time zone of its tenant row); **0039** (`usage_record.limit_state`, added without rewriting
a partition); **0043** (`checks.budget` uses the tenant's time zone and matches by identifier);
**0045** (deleting a mapping or an issuer deletes the limit rules of the mappings); **0051**
(counters keyed by provider and model identifier; fold pieces cut at the boundaries of the
periods held, for two zones).

### E3. Planning files and traceability

- `planning/epics/05-limits-and-budgets/epic.md`: 5.3 links to this design; `planning/roadmap.md`
  if it lists sprints. `docs/requirements` traceability (where the repository keeps the map of
  requirement to test): LIM-02, LIM-03, LIM-04, AUD-01 with the tests of §9.
- `docs/history` is written when the epic closes, not here.
- `sprint.md`: `status: done` and the maintainer's answers to §12, when the review has them.

Verification: `go test -count=1 ./test/repo/` (T-R45 to T-R47, T-R42, T-R44).

## Part F — Verification (no commit unless something is found)

```sh
make check
go test -race -count=20 ./internal/limits/
PANDOKOS_TEST_SEED=<n> go test -count=1 ./internal/limits/ -run 'TestTenantCalendar|TestZoneChangeNeverFrees'   # three values of n
make test-sdk-python && make test-sdk-ts && make test-container && make test-compose
make secrets
```

Then, by hand against the built binary and the test database: migrate a database at 0010 that
holds a rule on a revoked key and read its audit row; revoke a key that has a rule and read the
two audit rows; try to delete a model that a rule names; set a tenant's zone and read
`effective_from`; send one request to a tenant without rules and read both audit logs. Tick the
definition of done of §10 item by item in the pull request, with the coverage table.

## What the implementer does not do

- Add a cascade, a job that looks for orphans, or a function that reads rules across tenants.
- Add a foreign key to `limit_event`, a provider ID to `usage_record`, or a new actor type.
- Convert currencies, or let a zone change take effect before the instant of §5.7.
- Validate or rewrite the `NOT VALID` check of `usage_record`, or touch an existing partition.
- Change a response body or header of the inference listener.
