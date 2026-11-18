# Sprint 5.2 — Implementation plan

- Design: [sprint.md](sprint.md). Section numbers (§) refer to it.
- Each part below is self-contained: an agent needs sprint.md and that part only. Parts run in
  order A, B, C1 … C9, D, E, F.
- Suggested grouping for implementers: **G1** = C1 then C3 (calendar and period counters: no
  database, no gateway); **G2** = C2 (rules); **G3** = C4 (ledger reader, reconciler, event
  writer); **G4** = C6 then C7 (administration API and CLI). After part B the four do not depend
  on each other. C5, C8 and C9 are one agent, in order, after G1 to G4 are merged. A, B, D, E and
  F are one agent each. Because the risk class is high, C3 and C4 are reviewed by an agent that
  did not write them, with §5.4 and §5.6 as the checklist.

## Rules for the implementer

1. Work on the current branch (`claude/epic-5-implementation-cu4vxo`); do not create a branch.
   Sign off every commit (`git commit -s`). One commit for A, one for B (tests, failing), one per
   step of C (message given with the step), one for D, one for E, one for F if something is found.
2. **No new Go module**, in either `go.mod`. `time/tzdata` is standard library.
3. **The limiter sees numbers and identifiers only**: tenant, service account, key or mapping
   IDs, provider and model names, metric names, counts, a cost, a currency, times, a request ID.
   No body, header value, credential or model output reaches `internal/limits`, a log line, a
   header or `limit_event`. No test, fixture or document holds a real key.
4. **§5.4 and §5.6 are the contract.** `Reserve` changes every counter or none. Nothing in
   `Reserve`, `Settle` or `Release` performs I/O or reads a clock except through `Now`. Load, fold
   and rebase compute outside the shard lock and apply inside it, one tenant at a time.
5. **One definition of what the ledger counts**: `limits.Sum`. The store, the reconciler and the
   capability check call it; none re-implements the matching.
6. **Tenant scope.** Every ledger read runs inside `WithTenant` of that tenant, on the worker
   pool (counters) or the admin pool (capability check). No statement names the tenant in a
   `WHERE` clause (TEN-04). The gateway pool reads no usage record.
7. **The gateway does not import `internal/limits`** (T-R40); `internal/limits` does not import
   `internal/ledger` or `internal/admin` (T-R43).
8. Appendices A1 (migration 0010) and A2 (queries) are copied byte for byte. A1 was run on
   PostgreSQL 16 and A2 compiled with `sqlc` while designing; the schema lint was not run on A1
   and `apigen` not on A3. If one does not work as written, stop and ask.
9. Generated files are never edited by hand (`make generate`). SQL runs only as
   `queries.New(tx).<Method>` inside a `store` transaction helper (T-R10, T-R12).
10. Every new route declares its permission in the specification and gets its cross-tenant test
    in the same commit as its handler. Every handler that changes a rule writes its audit record
    in the transaction of the change.
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

## Part A — Scaffolding (commit: `chore: sprint 5.2 scaffolding: migration 0010, queries, API specification, types`)

Everything compiles and every existing test passes after this part; nothing new is enforced yet.

1. `internal/store/migrations/sql/0010_quotas_budgets.sql`: appendix A1, byte for byte. Update
   `internal/store/migrations/migrations.sum` the way the existing entries were produced
   (`sha256sum` of the file; the migration test prints the expected line).
2. `internal/store/queries/sql/limits.sql`: replace the file by appendix A2.
   `internal/store/queries/sql/catalog.sql`: add `m.default_output_tokens` to the select lists of
   `ListModels`, `GetModel`, `LockModel`, `ListVisibleModels` and `LoadModels`; add the column and
   `sqlc.narg(default_output_tokens)::bigint` to `CreateModel`; add to `UpdateModel`
   `default_output_tokens = CASE WHEN sqlc.arg(set_default_output_tokens)::boolean THEN
   sqlc.narg(default_output_tokens)::bigint ELSE default_output_tokens END`.
3. `api/openapi/admin.v1.yaml`: appendix A3. `make generate`.
4. `internal/authz/authz.go`: six constants and catalogue entries (§5.12), added to the two
   roles. Descriptions: `limit:read` "Reading and listing the tenant's limit rules, the platform
   ceilings that apply to it and its limit events"; `limit:write` "Creating and changing a limit
   rule of the tenant"; `limit:delete` "Deleting a limit rule of the tenant";
   `platform_limit:read|write|delete` likewise for "the ceilings the platform sets for tenants".
5. `internal/limits`: the exported types and signatures of §5.2 to §5.6 and §5.9 with bodies that
   return "not implemented" errors or zero values, so that part B compiles:
   - `limits.go`: the six metrics; `Metric.Period() (PeriodKind, bool)`, `Metric.Cost() bool`;
     `Metrics()` returns the nine in the order of §5.3; `Limit` and `Mark` as §5.3;
     `Request.Start`; `ErrNotLoaded`; `CostScale = 1_000_000`.
   - `period.go`: `PeriodKind`, `Period`, `Calendar`, `NewCalendar`, `Of`, `Zone`.
   - `rules.go`: `Rule` gains `ID`, `Provider`, `Model`, `Currency`, `Enforce`, `Thresholds
     []int`; `Query`; `For(q Query, cal *Calendar)`. Keep the 5.1 behaviour for the three
     metrics so that existing tests pass with `For(Query{ServiceAccountID: …, KeyID: …}, nil)`.
   - `usage.go`: `UsageGroup`, `Sum(l Limit, rows []UsageGroup) Amount`, `Ledger`,
     `NewLedger(db *store.DB) Ledger`. `Amount` is `struct{ N int64; Cost pricing.Decimal }`:
     `N` for requests and tokens, `Cost` for cost metrics.
   - `reconcile.go`: `ReconcilerOptions{Store *Memory; Ledger Ledger; Calendar *Calendar;
     Pending func() (time.Time, bool); Horizon, Interval time.Duration; Logger *slog.Logger; Now
     func() time.Time}`, `NewReconciler`, `Ensure(ctx, tenant.ID, []Limit) error`, `Run(ctx)`,
     `Tick(ctx)` (one pass of `Run`, for tests).
   - `events.go`: `Event`, `EventSink interface{ Offer(Event) bool }`, `EventWriterOptions{DB
     *store.DB; Logger; Now}`, `NewEventWriter`, `Offer`, `Run(ctx)`, `Close(ctx) (unwritten int)`.
   - `memory.go`: `MemoryOptions.Events EventSink`; the methods of C3 as stubs.
6. `internal/gateway/ports.go`: `LimitEstimate` gains `Provider, Model string` and `Start
   time.Time`; `LimitRefusal` gains `Code`, `Scope`, `Currency string`; `Target` gains
   `DefaultOutputTokens int64`. `internal/family`: `CodeQuotaExceeded`, `CodeNoPrice` with status,
   message (§5.8, §5.5) and Anthropic type; `HeaderLimitCurrency`, `HeaderLimitScope`.
7. `internal/ledger/writer.go`: `func (w *Writer) OldestPendingStart() (time.Time, bool)` — the
   smallest `StartedAt` among the records handed over and neither committed nor rejected (the
   queue and the batch being written), under the writer's mutex; false when there is none.
8. `internal/config`: the four keys of §5.14 in `Limits` (`TimeZone`, `OnUnknownPrice`,
   `OnStoreFailure`, `ReconcileInterval`), defaults, validation, the warning, the parsed
   `*time.Location` filled by `Load`; `keys.go` and the reference page entries.
9. `test/isolation/spec.go`: appendix A4. `test/dbtest/limits.go`: `SeedLimitRule` and
   `SeedPlatformLimit` take an options struct for the new columns (existing callers keep their
   meaning); `SeedUsage(tb, tenant, rows…)` inserts usage rows on the gateway role.
10. `cmd/pandokos/main.go`: `import _ "time/tzdata"`.

**Verify:** `make generate && go build ./... && go test -count=1 ./...` passes;
`go test -count=1 ./test/isolation/ -run 'TestSchemaLint|TestDefaultSpecMatchesDesign|TestCrossTenantNegative'`
passes; `git diff --stat` shows generated code only under `internal/adminapi` and
`internal/store/queries`.

### Appendix A1 — `internal/store/migrations/sql/0010_quotas_budgets.sql`

```sql
-- 0010_quotas_budgets: quotas and budgets per day and month, the scope of a rule (provider and
-- model), soft thresholds, the events they record, and the default output reservation of a model
-- (ADR 0050, 0052). Runs in one transaction as pandokos_owner. Every name is schema-qualified.

-- Rules of a tenant: six metrics per period, a scope, a currency for budgets, and thresholds.
-- provider and model are names of the catalogue ('' means every provider, every model of the
-- provider); there is no foreign key: a rule whose scope names nothing never matches a request.
-- limit_value of a cost metric is in millionths of the currency.
ALTER TABLE pandokos.limit_rule
    ADD COLUMN provider   text       NOT NULL DEFAULT '',
    ADD COLUMN model      text       NOT NULL DEFAULT '',
    ADD COLUMN currency   text       NOT NULL DEFAULT '',
    ADD COLUMN enforce    boolean    NOT NULL DEFAULT true,
    ADD COLUMN thresholds smallint[] NOT NULL DEFAULT '{}'::smallint[],
    DROP CONSTRAINT limit_rule_key,
    DROP CONSTRAINT limit_rule_metric_valid,
    DROP CONSTRAINT limit_rule_limit_value_range,
    ADD CONSTRAINT limit_rule_key UNIQUE (tenant_id, subject_kind, subject_id, provider, model, metric),
    ADD CONSTRAINT limit_rule_metric_valid CHECK (metric IN (
        'requests_per_minute', 'tokens_per_minute', 'concurrent_requests',
        'requests_per_day', 'requests_per_month', 'tokens_per_day', 'tokens_per_month',
        'cost_per_day', 'cost_per_month')),
    ADD CONSTRAINT limit_rule_limit_value_range CHECK (
        limit_value >= 1
        AND limit_value <= CASE WHEN metric IN ('cost_per_day', 'cost_per_month')
                                THEN 1000000000000000 ELSE 1000000000000 END),
    ADD CONSTRAINT limit_rule_scope_format CHECK (
        provider ~ '^([a-z][a-z0-9-]{0,62})?$'
        AND model ~ '^([A-Za-z0-9][A-Za-z0-9._:@/+-]{0,127})?$'
        AND (model = '' OR provider <> '')),
    ADD CONSTRAINT limit_rule_currency_by_metric CHECK (
        (metric IN ('cost_per_day', 'cost_per_month') AND currency ~ '^[A-Z]{3}$')
        OR (metric NOT IN ('cost_per_day', 'cost_per_month') AND currency = '')),
    ADD CONSTRAINT limit_rule_thresholds_valid CHECK (
        cardinality(thresholds) <= 4
        AND array_position(thresholds, NULL) IS NULL
        AND 1 <= ALL (thresholds) AND 100 >= ALL (thresholds)),
    ADD CONSTRAINT limit_rule_soft_by_metric CHECK (
        (enforce OR cardinality(thresholds) >= 1)
        AND (metric NOT IN ('requests_per_minute', 'tokens_per_minute', 'concurrent_requests')
             OR (enforce AND cardinality(thresholds) = 0)));

GRANT INSERT (provider, model, currency, enforce, thresholds) ON pandokos.limit_rule TO pandokos_admin;
GRANT UPDATE (enforce, thresholds) ON pandokos.limit_rule TO pandokos_admin;

-- Ceilings of the platform: the same columns and rules.
ALTER TABLE pandokos.platform_limit_rule
    ADD COLUMN provider   text       NOT NULL DEFAULT '',
    ADD COLUMN model      text       NOT NULL DEFAULT '',
    ADD COLUMN currency   text       NOT NULL DEFAULT '',
    ADD COLUMN enforce    boolean    NOT NULL DEFAULT true,
    ADD COLUMN thresholds smallint[] NOT NULL DEFAULT '{}'::smallint[],
    DROP CONSTRAINT platform_limit_rule_key,
    DROP CONSTRAINT platform_limit_rule_metric_valid,
    DROP CONSTRAINT platform_limit_rule_limit_value_range,
    ADD CONSTRAINT platform_limit_rule_key UNIQUE NULLS NOT DISTINCT (tenant_id, provider, model, metric),
    ADD CONSTRAINT platform_limit_rule_metric_valid CHECK (metric IN (
        'requests_per_minute', 'tokens_per_minute', 'concurrent_requests',
        'requests_per_day', 'requests_per_month', 'tokens_per_day', 'tokens_per_month',
        'cost_per_day', 'cost_per_month')),
    ADD CONSTRAINT platform_limit_rule_limit_value_range CHECK (
        limit_value >= 1
        AND limit_value <= CASE WHEN metric IN ('cost_per_day', 'cost_per_month')
                                THEN 1000000000000000 ELSE 1000000000000 END),
    ADD CONSTRAINT platform_limit_rule_scope_format CHECK (
        provider ~ '^([a-z][a-z0-9-]{0,62})?$'
        AND model ~ '^([A-Za-z0-9][A-Za-z0-9._:@/+-]{0,127})?$'
        AND (model = '' OR provider <> '')),
    ADD CONSTRAINT platform_limit_rule_currency_by_metric CHECK (
        (metric IN ('cost_per_day', 'cost_per_month') AND currency ~ '^[A-Z]{3}$')
        OR (metric NOT IN ('cost_per_day', 'cost_per_month') AND currency = '')),
    ADD CONSTRAINT platform_limit_rule_thresholds_valid CHECK (
        cardinality(thresholds) <= 4
        AND array_position(thresholds, NULL) IS NULL
        AND 1 <= ALL (thresholds) AND 100 >= ALL (thresholds)),
    ADD CONSTRAINT platform_limit_rule_soft_by_metric CHECK (
        (enforce OR cardinality(thresholds) >= 1)
        AND (metric NOT IN ('requests_per_minute', 'tokens_per_minute', 'concurrent_requests')
             OR (enforce AND cardinality(thresholds) = 0)));

GRANT INSERT (provider, model, currency, enforce, thresholds) ON pandokos.platform_limit_rule TO pandokos_admin;
GRANT UPDATE (enforce, thresholds) ON pandokos.platform_limit_rule TO pandokos_admin;

-- What a quota or a budget recorded: a threshold that was reached, or a limit that refused a
-- request for the first time in a period (tenant-scoped table, append-only). One row per rule,
-- period, kind and percentage, whoever records it and however often: the unique key is what makes
-- recording idempotent. rule_id names a row of limit_rule (rule_level 'tenant') or of
-- platform_limit_rule ('platform'), without a foreign key: the event outlives the rule. percent is
-- 0 for 'exhausted'. limit_value and observed are in the unit of the metric (requests, tokens, or
-- the currency; limit_value of a cost metric in millionths). No request, content or caller is
-- named. Sprint 6.4 writes the outbox row in the transaction that inserts here.
CREATE TABLE pandokos.limit_event (
    tenant_id    uuid           NOT NULL,
    id           uuid           NOT NULL DEFAULT pandokos.uuid_v7(),
    kind         text           NOT NULL,
    rule_id      uuid           NOT NULL,
    rule_level   text           NOT NULL,
    subject_kind text           NOT NULL,
    subject_id   text           NOT NULL DEFAULT '',
    provider     text           NOT NULL DEFAULT '',
    model        text           NOT NULL DEFAULT '',
    metric       text           NOT NULL,
    currency     text           NOT NULL DEFAULT '',
    period_start timestamptz    NOT NULL,
    period_end   timestamptz    NOT NULL,
    percent      smallint       NOT NULL DEFAULT 0,
    limit_value  bigint         NOT NULL,
    observed     numeric(38,18) NOT NULL,
    occurred_at  timestamptz    NOT NULL DEFAULT now(),
    CONSTRAINT limit_event_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT limit_event_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT limit_event_once UNIQUE (tenant_id, rule_id, period_start, kind, percent),
    CONSTRAINT limit_event_kind_valid CHECK (kind IN ('threshold_reached', 'exhausted')),
    CONSTRAINT limit_event_percent_by_kind CHECK (
        (kind = 'threshold_reached' AND percent BETWEEN 1 AND 100) OR (kind = 'exhausted' AND percent = 0)),
    CONSTRAINT limit_event_rule_level_valid CHECK (rule_level IN ('tenant', 'platform')),
    CONSTRAINT limit_event_subject_kind_valid CHECK (subject_kind IN ('tenant', 'service_account', 'key')),
    CONSTRAINT limit_event_subject_id_format CHECK (subject_id ~ '^[A-Za-z0-9_-]{0,64}$'
                                                    AND (subject_kind = 'tenant') = (subject_id = '')),
    CONSTRAINT limit_event_scope_format CHECK (
        provider ~ '^([a-z][a-z0-9-]{0,62})?$'
        AND model ~ '^([A-Za-z0-9][A-Za-z0-9._:@/+-]{0,127})?$'
        AND (model = '' OR provider <> '')),
    CONSTRAINT limit_event_metric_valid CHECK (metric IN (
        'requests_per_day', 'requests_per_month', 'tokens_per_day', 'tokens_per_month',
        'cost_per_day', 'cost_per_month')),
    CONSTRAINT limit_event_currency_by_metric CHECK (
        (metric IN ('cost_per_day', 'cost_per_month') AND currency ~ '^[A-Z]{3}$')
        OR (metric NOT IN ('cost_per_day', 'cost_per_month') AND currency = '')),
    CONSTRAINT limit_event_period_valid CHECK (period_end > period_start),
    CONSTRAINT limit_event_values_valid CHECK (limit_value >= 1 AND observed >= 0)
);

CREATE TRIGGER limit_event_append_only BEFORE UPDATE OR DELETE ON pandokos.limit_event
    FOR EACH ROW EXECUTE FUNCTION pandokos.append_only();
CREATE TRIGGER limit_event_no_truncate BEFORE TRUNCATE ON pandokos.limit_event
    FOR EACH STATEMENT EXECUTE FUNCTION pandokos.append_only();

ALTER TABLE pandokos.limit_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.limit_event FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.limit_event
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

-- The gateway appends and reads nothing; the admin role reads; nobody changes a row.
GRANT INSERT (tenant_id, kind, rule_id, rule_level, subject_kind, subject_id, provider, model, metric, currency,
              period_start, period_end, percent, limit_value, observed)
    ON pandokos.limit_event TO pandokos_gateway;
GRANT SELECT ON pandokos.limit_event TO pandokos_admin;

-- The output tokens reserved per choice for a request to this model that sets no output limit;
-- NULL: the configured default (limits.default_output_tokens).
ALTER TABLE pandokos.model
    ADD COLUMN default_output_tokens bigint,
    ADD CONSTRAINT model_default_output_tokens_range CHECK (
        default_output_tokens IS NULL OR default_output_tokens BETWEEN 1 AND 1000000);

GRANT INSERT (default_output_tokens) ON pandokos.model TO pandokos_admin;
GRANT UPDATE (default_output_tokens) ON pandokos.model TO pandokos_admin;
```

### Appendix A2 — `internal/store/queries/sql/limits.sql`

```sql
-- Limit rules, the events of quotas and budgets, and the ledger aggregates their counters are
-- rebuilt from. Every statement runs in a store transaction helper; none names the tenant in a
-- WHERE clause: the policies do (TEN-04).

-- Gateway pool, session of the tenant whose rules are loaded. platform_limit_rule shows the
-- defaults and the tenant's own rows in that session.

-- name: ListLimitRules :many
SELECT r.id, r.subject_kind, r.subject_id, r.provider, r.model, r.metric, r.limit_value, r.currency, r.enforce,
       r.thresholds
  FROM pandokos.limit_rule r
 ORDER BY r.subject_kind, r.subject_id, r.provider, r.model, r.metric
 LIMIT 5001;

-- name: ListPlatformLimitRules :many
SELECT r.id, (r.tenant_id IS NULL)::boolean AS is_default, r.provider, r.model, r.metric, r.limit_value, r.currency,
       r.enforce, r.thresholds
  FROM pandokos.platform_limit_rule r
 ORDER BY r.tenant_id NULLS FIRST, r.provider, r.model, r.metric;

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

-- Worker pool (counters) or admin pool (capability check), session of the tenant. What the
-- ledger holds for the requests that started in [from_time, to_time), by the dimensions a rule
-- can name. Rows that started before live_from are summed with minute 0; later rows by the minute
-- they started in (Unix time / 60). A request counts when a credential was opened for it; its
-- tokens are those the cost was computed on; its cost counts when it is known.

-- name: AggregateUsageForLimits :many
SELECT (CASE WHEN u.started_at >= sqlc.arg(live_from)::timestamptz
             THEN floor(extract(epoch FROM u.started_at) / 60)::bigint ELSE 0 END)::bigint AS minute,
       u.principal_id, u.key_id, u.provider, u.model, COALESCE(u.currency, '')::text AS currency,
       count(*) FILTER (WHERE u.credential_id IS NOT NULL)::bigint AS requests,
       COALESCE(sum(COALESCE(u.input_tokens, u.estimated_input_tokens, 0)
                    + COALESCE(u.output_tokens, u.estimated_output_tokens, 0)), 0)::numeric AS tokens,
       COALESCE(sum(u.cost), 0)::numeric AS cost
  FROM pandokos.usage_record u
 WHERE u.started_at >= sqlc.arg(from_time)::timestamptz
   AND u.started_at < sqlc.arg(to_time)::timestamptz
 GROUP BY 1, u.principal_id, u.key_id, u.provider, u.model, COALESCE(u.currency, '')
HAVING count(*) FILTER (WHERE u.credential_id IS NOT NULL) > 0;

-- Administration API, tenant session (admin pool).

-- name: LockLimitRules :exec
SELECT pg_catalog.pg_advisory_xact_lock(
           pg_catalog.hashtextextended('pandokos.limit_rule:' || pandokos.current_tenant_id()::text, 0));

-- name: CountLimitRules :one
SELECT count(*)::bigint FROM pandokos.limit_rule;

-- name: LimitSubjectExists :one
SELECT (CASE sqlc.arg(subject_kind)::text
            WHEN 'tenant' THEN true
            WHEN 'service_account' THEN EXISTS (SELECT 1 FROM pandokos.service_account s
                                                 WHERE s.id::text = sqlc.arg(subject_id)::text)
            WHEN 'key' THEN EXISTS (SELECT 1 FROM pandokos.virtual_key k WHERE k.key_id = sqlc.arg(subject_id)::text)
                            OR EXISTS (SELECT 1 FROM pandokos.subject_mapping m
                                        WHERE m.id::text = sqlc.arg(subject_id)::text)
            ELSE false END)::boolean AS found;

-- In a tenant session a model is found when the tenant can see it; in the platform scope when
-- it exists.

-- name: LimitScopeExists :one
SELECT EXISTS (SELECT 1 FROM pandokos.provider p
                WHERE p.name = sqlc.arg(provider)::text
                  AND (sqlc.arg(model)::text = ''
                       OR EXISTS (SELECT 1 FROM pandokos.model m
                                   WHERE m.provider_id = p.id AND m.name = sqlc.arg(model)::text)))::boolean AS found;

-- name: CreateLimitRule :one
INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, subject_id, provider, model, metric, limit_value, currency,
                                 enforce, thresholds)
VALUES (pandokos.current_tenant_id(), sqlc.arg(subject_kind)::text, sqlc.arg(subject_id)::text,
        sqlc.arg(provider)::text, sqlc.arg(model)::text, sqlc.arg(metric)::text, sqlc.arg(limit_value)::bigint,
        sqlc.arg(currency)::text, sqlc.arg(enforce)::boolean, sqlc.arg(thresholds)::smallint[])
RETURNING id, subject_kind, subject_id, provider, model, metric, limit_value, currency, enforce, thresholds,
          created_at, updated_at;

-- name: GetLimitRule :one
SELECT r.id, r.subject_kind, r.subject_id, r.provider, r.model, r.metric, r.limit_value, r.currency, r.enforce,
       r.thresholds, r.created_at, r.updated_at
  FROM pandokos.limit_rule r
 WHERE r.id = sqlc.arg(id)::uuid;

-- name: LockLimitRule :one
SELECT r.id, r.subject_kind, r.subject_id, r.provider, r.model, r.metric, r.limit_value, r.currency, r.enforce,
       r.thresholds, r.created_at, r.updated_at
  FROM pandokos.limit_rule r
 WHERE r.id = sqlc.arg(id)::uuid
   FOR UPDATE;

-- name: ListLimitRulesPage :many
SELECT r.id, r.subject_kind, r.subject_id, r.provider, r.model, r.metric, r.limit_value, r.currency, r.enforce,
       r.thresholds, r.created_at, r.updated_at
  FROM pandokos.limit_rule r
 WHERE (sqlc.arg(subject_kind)::text = '' OR r.subject_kind = sqlc.arg(subject_kind)::text)
   AND (sqlc.arg(subject_id)::text = '' OR r.subject_id = sqlc.arg(subject_id)::text)
   AND (sqlc.arg(metric)::text = '' OR r.metric = sqlc.arg(metric)::text)
   AND (sqlc.arg(provider)::text = '' OR r.provider = sqlc.arg(provider)::text)
   AND (sqlc.arg(model)::text = '' OR r.model = sqlc.arg(model)::text)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN r.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN r.id < sqlc.arg(after_id)::uuid
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN r.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN r.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: UpdateLimitRule :one
UPDATE pandokos.limit_rule
   SET limit_value = sqlc.arg(limit_value)::bigint,
       enforce     = sqlc.arg(enforce)::boolean,
       thresholds  = sqlc.arg(thresholds)::smallint[]
 WHERE id = sqlc.arg(id)::uuid
RETURNING id, subject_kind, subject_id, provider, model, metric, limit_value, currency, enforce, thresholds,
          created_at, updated_at;

-- name: DeleteLimitRule :one
DELETE FROM pandokos.limit_rule
 WHERE id = sqlc.arg(id)::uuid
RETURNING id, subject_kind, subject_id, provider, model, metric, limit_value, currency, enforce, thresholds,
          created_at, updated_at;

-- The ceilings that apply to the tenant of the session: the defaults and its own rows.

-- name: ListTenantPlatformLimitRules :many
SELECT r.id, (r.tenant_id IS NULL)::boolean AS is_default, r.provider, r.model, r.metric, r.limit_value, r.currency,
       r.enforce, r.thresholds, r.created_at, r.updated_at
  FROM pandokos.platform_limit_rule r
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
INSERT INTO pandokos.platform_limit_rule (tenant_id, provider, model, metric, limit_value, currency, enforce,
                                          thresholds)
VALUES (sqlc.narg(tenant_id)::uuid, sqlc.arg(provider)::text, sqlc.arg(model)::text, sqlc.arg(metric)::text,
        sqlc.arg(limit_value)::bigint, sqlc.arg(currency)::text, sqlc.arg(enforce)::boolean,
        sqlc.arg(thresholds)::smallint[])
RETURNING id, tenant_id, provider, model, metric, limit_value, currency, enforce, thresholds, created_at, updated_at;

-- name: GetPlatformLimitRule :one
SELECT r.id, r.tenant_id, r.provider, r.model, r.metric, r.limit_value, r.currency, r.enforce, r.thresholds,
       r.created_at, r.updated_at
  FROM pandokos.platform_limit_rule r
 WHERE r.id = sqlc.arg(id)::uuid;

-- name: LockPlatformLimitRule :one
SELECT r.id, r.tenant_id, r.provider, r.model, r.metric, r.limit_value, r.currency, r.enforce, r.thresholds,
       r.created_at, r.updated_at
  FROM pandokos.platform_limit_rule r
 WHERE r.id = sqlc.arg(id)::uuid
   FOR UPDATE;

-- applies_to: '' every row, 'default' the rows without tenant, otherwise a tenant ID.

-- name: ListPlatformLimitRulesPage :many
SELECT r.id, r.tenant_id, r.provider, r.model, r.metric, r.limit_value, r.currency, r.enforce, r.thresholds,
       r.created_at, r.updated_at
  FROM pandokos.platform_limit_rule r
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
UPDATE pandokos.platform_limit_rule
   SET limit_value = sqlc.arg(limit_value)::bigint,
       enforce     = sqlc.arg(enforce)::boolean,
       thresholds  = sqlc.arg(thresholds)::smallint[]
 WHERE id = sqlc.arg(id)::uuid
RETURNING id, tenant_id, provider, model, metric, limit_value, currency, enforce, thresholds, created_at, updated_at;

-- name: DeletePlatformLimitRule :one
DELETE FROM pandokos.platform_limit_rule
 WHERE id = sqlc.arg(id)::uuid
RETURNING id, tenant_id, provider, model, metric, limit_value, currency, enforce, thresholds, created_at, updated_at;
```

### Appendix A3 — `api/openapi/admin.v1.yaml`

Add the tags `limits` and `platform-limits`. Add the three values `available`, `exhausted` and
`unpriced` to `BudgetAvailability` (description: "not_enforced when no enforced quota or budget of
the tenant matches; unpriced when a budget matches and the model has no price in its currency;
exhausted when a matching quota or budget is used up for the current period."), and to the
description of `Capability.reasons` the two reasons `no_price`, `budget_exhausted` at the end of
the order. Add `default_output_tokens` to `Model` (required; "In tokens; 0 when not set."), to
`ModelCreate` and `ModelUpdate` (`{type: integer, minimum: 0, maximum: 1000000}`; "0 clears it."
for the update). No problem code is added.

Paths (responses of every operation: `401`, `403`, `404` (not for the platform collection),
`500`, `503` as in the neighbouring operations; `400` where there are parameters or a body;
`409`, `413`, `415` where there is a body):

```yaml
  /v1/tenants/{tenant}/limits:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: listLimits
      tags: [limits]
      summary: List the limit rules of a tenant
      x-pandokos-permission: limit:read
      parameters:
        - {$ref: '#/components/parameters/Limit'}
        - {$ref: '#/components/parameters/Cursor'}
        - {$ref: '#/components/parameters/Sort'}
        - {name: subject_kind, in: query, description: Only rules of this subject kind., schema: {$ref: '#/components/schemas/LimitSubjectKind'}}
        - {name: subject_id, in: query, description: Only rules of the subject with this ID., schema: {type: string, minLength: 1, maxLength: 64}}
        - {name: metric, in: query, description: Only rules of this metric., schema: {$ref: '#/components/schemas/LimitMetric'}}
        - {name: provider, in: query, description: Only rules scoped to this provider., schema: {$ref: '#/components/schemas/ProviderName'}}
        - {name: model, in: query, description: Only rules scoped to this model., schema: {$ref: '#/components/schemas/ModelName'}}
      responses:
        "200": {$ref: '#/components/responses/LimitRuleList'}
    post:
      operationId: createLimit
      tags: [limits]
      summary: Create a limit rule of a tenant
      description: >-
        One rule per subject, scope and metric. The subject and the scope must exist. A count metric
        takes limit; a cost metric takes amount and currency. A tenant has at most 5000 rules.
      x-pandokos-permission: limit:write
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/LimitRuleCreate'}
      responses:
        "201": {$ref: '#/components/responses/LimitRuleCreated'}
  /v1/tenants/{tenant}/limits/{rule}:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
      - {$ref: '#/components/parameters/LimitRule'}
    get:
      operationId: getLimit
      tags: [limits]
      summary: Read a limit rule
      x-pandokos-permission: limit:read
      responses:
        "200": {$ref: '#/components/responses/LimitRule'}
    patch:
      operationId: updateLimit
      tags: [limits]
      summary: Change the value, the enforcement or the thresholds of a limit rule
      description: >-
        At least one member. Subject, scope, metric and currency never change. Counters are not
        reset by a change.
      x-pandokos-permission: limit:write
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/LimitRuleUpdate'}
      responses:
        "200": {$ref: '#/components/responses/LimitRule'}
    delete:
      operationId: deleteLimit
      tags: [limits]
      summary: Delete a limit rule
      description: >-
        The response is the rule as it was.
      x-pandokos-permission: limit:delete
      responses:
        "200": {$ref: '#/components/responses/LimitRule'}
  /v1/tenants/{tenant}/platform-limits:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: listTenantPlatformLimits
      tags: [limits]
      summary: List the ceilings the platform sets for a tenant
      description: >-
        The defaults for every tenant and the rows of this tenant; a row of the tenant replaces the
        default of the same scope and metric. Read-only.
      x-pandokos-permission: limit:read
      parameters:
        - {$ref: '#/components/parameters/Limit'}
        - {$ref: '#/components/parameters/Cursor'}
      responses:
        "200": {$ref: '#/components/responses/PlatformLimitRuleList'}
  /v1/tenants/{tenant}/limit-events:
    parameters:
      - {$ref: '#/components/parameters/Tenant'}
    get:
      operationId: listLimitEvents
      tags: [limits]
      summary: List what the tenant's quotas and budgets recorded, newest first
      x-pandokos-permission: limit:read
      parameters:
        - {$ref: '#/components/parameters/Limit'}
        - {$ref: '#/components/parameters/Cursor'}
        - {name: kind, in: query, description: Only events of this kind., schema: {$ref: '#/components/schemas/LimitEventKind'}}
        - {name: rule, in: query, description: Only events of the rule with this ID., schema: {$ref: '#/components/schemas/ID'}}
      responses:
        "200": {$ref: '#/components/responses/LimitEventList'}
  /v1/platform-limits:
    get:
      operationId: listPlatformLimits
      tags: [platform-limits]
      summary: List the ceilings the platform sets for tenants
      x-pandokos-permission: platform_limit:read
      parameters:
        - {$ref: '#/components/parameters/Limit'}
        - {$ref: '#/components/parameters/Cursor'}
        - {$ref: '#/components/parameters/Sort'}
        - {name: applies_to, in: query, description: 'Only the defaults ("default") or the rows of the tenant with this ID.', schema: {type: string, minLength: 1, maxLength: 36}}
        - {name: metric, in: query, description: Only rules of this metric., schema: {$ref: '#/components/schemas/LimitMetric'}}
      responses:
        "200": {$ref: '#/components/responses/PlatformLimitRuleList'}
    post:
      operationId: createPlatformLimit
      tags: [platform-limits]
      summary: Create a ceiling for every tenant or for one
      description: >-
        Without tenant the rule is the default for every tenant; with a tenant it replaces the
        default of the same scope and metric for that tenant.
      x-pandokos-permission: platform_limit:write
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/PlatformLimitRuleCreate'}
      responses:
        "201": {$ref: '#/components/responses/PlatformLimitRuleCreated'}
  /v1/platform-limits/{rule}:
    parameters:
      - {$ref: '#/components/parameters/PlatformLimitRule'}
    get:
      operationId: getPlatformLimit
      tags: [platform-limits]
      summary: Read a platform ceiling
      x-pandokos-permission: platform_limit:read
      responses:
        "200": {$ref: '#/components/responses/PlatformLimitRule'}
    patch:
      operationId: updatePlatformLimit
      tags: [platform-limits]
      summary: Change the value, the enforcement or the thresholds of a platform ceiling
      x-pandokos-permission: platform_limit:write
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/LimitRuleUpdate'}
      responses:
        "200": {$ref: '#/components/responses/PlatformLimitRule'}
    delete:
      operationId: deletePlatformLimit
      tags: [platform-limits]
      summary: Delete a platform ceiling
      description: >-
        The response is the rule as it was.
      x-pandokos-permission: platform_limit:delete
      responses:
        "200": {$ref: '#/components/responses/PlatformLimitRule'}
```

Parameters `LimitRule` and `PlatformLimitRule`: `name: rule`, `in: path`, `required: true`,
schema `ID`. Responses `LimitRule`, `LimitRuleCreated`, `LimitRuleList`, `PlatformLimitRule`,
`PlatformLimitRuleCreated`, `PlatformLimitRuleList`, `LimitEventList`, in the form of their
neighbours. Schemas:

```yaml
    LimitMetric:
      type: string
      enum: [concurrent_requests, requests_per_minute, tokens_per_minute, requests_per_day, requests_per_month,
             tokens_per_day, tokens_per_month, cost_per_day, cost_per_month]
    LimitSubjectKind:
      type: string
      enum: [tenant, service_account, key]
    LimitEventKind:
      type: string
      enum: [threshold_reached, exhausted]
    LimitAmount:
      type: string
      description: A decimal amount in the currency of the rule, with at most six fraction digits.
      pattern: '^(0|[1-9][0-9]{0,9})(\.[0-9]{1,6})?$'
    Currency:
      type: string
      pattern: '^[A-Z]{3}$'
    LimitThresholds:
      type: array
      description: Percentages of the limit at which an event is recorded, ascending.
      maxItems: 4
      uniqueItems: true
      items: {type: integer, minimum: 1, maximum: 100}
    LimitRule:
      type: object
      additionalProperties: false
      required: [id, subject_kind, subject_id, provider, model, metric, limit, amount, currency, enforce, thresholds,
                 created_at, updated_at]
      properties:
        id: {$ref: '#/components/schemas/ID'}
        subject_kind: {$ref: '#/components/schemas/LimitSubjectKind'}
        subject_id: {type: string, description: 'Empty for the tenant; a service account ID; a key ID or a subject mapping ID.'}
        provider: {type: string, description: Empty when the rule is not scoped to a provider.}
        model: {type: string, description: Empty when the rule is not scoped to a model.}
        metric: {$ref: '#/components/schemas/LimitMetric'}
        limit: {type: integer, description: Requests or tokens; 0 for a cost metric.}
        amount: {type: string, description: The budget as a decimal; empty unless a cost metric.}
        currency: {type: string, description: Empty unless a cost metric.}
        enforce: {type: boolean, description: false means the rule never refuses and only records its thresholds.}
        thresholds: {$ref: '#/components/schemas/LimitThresholds'}
        created_at: {type: string, format: date-time}
        updated_at: {type: string, format: date-time}
    LimitRuleCreate:
      type: object
      additionalProperties: false
      required: [metric]
      properties:
        subject_kind: {$ref: '#/components/schemas/LimitSubjectKind'}
        subject_id: {type: string, minLength: 1, maxLength: 64}
        provider: {$ref: '#/components/schemas/ProviderName'}
        model: {$ref: '#/components/schemas/ModelName'}
        metric: {$ref: '#/components/schemas/LimitMetric'}
        limit: {type: integer, minimum: 1, maximum: 1000000000000}
        amount: {$ref: '#/components/schemas/LimitAmount'}
        currency: {$ref: '#/components/schemas/Currency'}
        enforce: {type: boolean, default: true}
        thresholds: {$ref: '#/components/schemas/LimitThresholds'}
    LimitRuleUpdate:
      type: object
      additionalProperties: false
      properties:
        limit: {type: integer, minimum: 1, maximum: 1000000000000}
        amount: {$ref: '#/components/schemas/LimitAmount'}
        enforce: {type: boolean}
        thresholds: {$ref: '#/components/schemas/LimitThresholds'}
    PlatformLimitRule:
      type: object
      additionalProperties: false
      required: [id, tenant, provider, model, metric, limit, amount, currency, enforce, thresholds, created_at, updated_at]
      properties:
        id: {$ref: '#/components/schemas/ID'}
        tenant: {type: string, description: The tenant the rule is for; empty for the default of every tenant.}
        # provider, model, metric, limit, amount, currency, enforce, thresholds, created_at, updated_at: as LimitRule
    PlatformLimitRuleCreate:
      type: object
      additionalProperties: false
      required: [metric]
      properties:
        tenant: {$ref: '#/components/schemas/ID'}
        # provider, model, metric, limit, amount, currency, enforce, thresholds: as LimitRuleCreate
    LimitEvent:
      type: object
      additionalProperties: false
      required: [id, kind, rule_id, rule_level, subject_kind, subject_id, provider, model, metric, currency,
                 period_start, period_end, percent, limit, amount, observed, occurred_at]
      properties:
        id: {$ref: '#/components/schemas/ID'}
        kind: {$ref: '#/components/schemas/LimitEventKind'}
        rule_id: {$ref: '#/components/schemas/ID'}
        rule_level: {type: string, enum: [tenant, platform]}
        subject_kind: {$ref: '#/components/schemas/LimitSubjectKind'}
        subject_id: {type: string}
        provider: {type: string}
        model: {type: string}
        metric: {$ref: '#/components/schemas/LimitMetric'}
        currency: {type: string}
        period_start: {type: string, format: date-time}
        period_end: {type: string, format: date-time}
        percent: {type: integer, description: The threshold reached; 0 for exhausted.}
        limit: {type: integer, description: The value of the rule in requests or tokens; 0 for a cost metric.}
        amount: {type: string, description: The value of the rule as a decimal; empty unless a cost metric.}
        observed: {type: string, description: 'What was used when the event was recorded: a decimal in requests, tokens or the currency.'}
        occurred_at: {type: string, format: date-time}
```

The two comment lines stand for the properties written out in full (the generator has no
inheritance). `LimitRuleList`, `PlatformLimitRuleList` and `LimitEventList` are `{items,
next_cursor}` objects like `SubjectMappingList`. If `apigen` refuses a keyword used here
(`pattern`, `uniqueItems`, `default`), stop and ask.

### Appendix A4 — `test/isolation/spec.go`

```go
// Grants (replace the two entries of 5.1, add the third)
"limit_rule": {
    "pandokos_gateway": {"SELECT"},
    "pandokos_admin": {"DELETE", "INSERT(currency,enforce,limit_value,metric,model,provider,subject_id,subject_kind,tenant_id,thresholds)",
        "SELECT", "UPDATE(enforce,limit_value,thresholds)"},
},
"platform_limit_rule": {
    "pandokos_gateway": {"SELECT"},
    "pandokos_admin": {"DELETE", "INSERT(currency,enforce,limit_value,metric,model,provider,tenant_id,thresholds)",
        "SELECT", "UPDATE(enforce,limit_value,thresholds)"},
},
"limit_event": {
    "pandokos_gateway": {"INSERT(currency,kind,limit_value,metric,model,observed,percent,period_end,period_start,provider,rule_id,rule_level,subject_id,subject_kind,tenant_id)"},
    "pandokos_admin":   {"SELECT"},
},

// Fixtures (limit_rule keeps its fixture of 5.1)
"limit_event": {
    Insert: `INSERT INTO pandokos.limit_event (tenant_id, kind, rule_id, rule_level, subject_kind, metric, period_start, period_end, percent, limit_value, observed)
VALUES ($1::uuid, 'threshold_reached', pandokos.uuid_v7(), 'tenant', 'tenant', 'tokens_per_day', date_trunc('day', now()), date_trunc('day', now()) + interval '1 day', 80, 10, 8) RETURNING id::text`,
    Update: `percent = 81`,
},

// AppendOnly: add "limit_event".
```

The fixture follows that of `usage_record`, the other append-only table written by the gateway
role. The grants of `model` gain `default_output_tokens` in both column lists. Nothing is added to
`SecurityDefiner` or `Views`.

## Part B — Tests first (commit: `test: sprint 5.2 tests, failing`)

Write every test of §9 under its stated name, and change the tests listed under "Changed tests".
The commit must compile; new tests fail (or skip nothing) until part C. Quote the failing output
in the commit body (`go test ./... 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' | sort | uniq -c`).

### B1. Helpers

- `internal/limits/helpers_test.go`: the **reference model** of §7. It records for every request
  its start, admission, limits, reserved amounts, end kind (settle with actual, release, expire),
  and commit time or loss; it answers `Ledger(asOf)` as `[]UsageGroup` through the same grouping
  as the query (by minute from a given instant), and `Bound(limit)` as the right-hand side of
  §5.5. It shares no code with `Memory` except `Sum`'s inputs.
- `fakeLedger` implementing `limits.Ledger` over the reference, with an injectable error and a
  call log (tenant, from, liveFrom, to).
- `recordingSink` implementing `EventSink`, with a switch that refuses offers.
- `test/limitstest`: `Real` gains `Reconciler`, `Ledger` (fake or the database reader), `Events`,
  `Calendar`; `New` keeps working for 5.1 tests.
- Zones: the helper file imports `_ "time/tzdata"`; the zone list of T-647 is `UTC`,
  `Europe/Rome`, `America/New_York`, `America/St_Johns`, `America/Santiago`, `America/Havana`,
  `America/Asuncion`, `Africa/Cairo`, `Asia/Beirut`, `Asia/Kolkata`, `Asia/Kathmandu`,
  `Australia/Lord_Howe`, `Pacific/Apia`, `Pacific/Kiritimati`, `Pacific/Chatham`.
- `test/apitest`: calls for the twelve operations. `test/dbtest`: `SeedUsage`.

### B2. Where each test lives

| Tests | File |
|---|---|
| T-647 | `internal/limits/period_test.go` |
| T-648, T-649, T-653, T-656 | `internal/limits/periods_test.go` |
| T-650, T-652 | `internal/limits/property_periods_test.go` |
| T-651, T-657 | `internal/limits/reconcile_test.go`, `usage_test.go` |
| T-654 | `internal/limits/rules_test.go` |
| T-655 | `internal/limits/memory_test.go` |
| T-658, T-659, T-660, T-662 | `internal/limits/usage_db_test.go`, `reconcile_db_test.go`, `events_db_test.go`, `load_db_test.go` |
| T-661, T-679 | `test/isolation/limits_test.go` |
| T-663, T-665 to T-674 | `internal/gateway/estimate_test.go`, `limits_test.go`, `quota_test.go`, `levels_db_test.go`, `load_test.go` |
| T-664 | `internal/resolver/limits_test.go` |
| T-675 to T-678, T-680 to T-682 | `internal/admin/limits_db_test.go`, `capability_db_test.go`, `catalogue_db_test.go` |
| T-683, T-684, T-685 | `internal/authz/authz_test.go`, `internal/cli/limits_test.go`, `internal/config/limits_test.go` |
| T-686 to T-688 | `test/e2e/limits_test.go` |
| T-689, T-690, T-691 | `test/sdk/python/test_limits.py`, `test/sdk/ts/limits.test.mjs`, `test/sdk/limits_test.go`; harness in `test/sdk/external_test.go` |
| T-R42 to T-R44 | `test/repo` |

Notes for single tests:

- **T-650, T-652** use `math/rand/v2` with the logged seed and `PANDOKOS_TEST_SEED` of 5.1. A
  schedule step is one of: start a request (random subject, model, amounts), admit it, end it
  (settle below, at or above the reservation; release; let it expire), commit or lose its record,
  advance the clock (seconds to days, across midnights and month ends), create a rule, run
  `Ensure`, `Tick`. T-652's two assertions are those of §9, checked after every step and at the
  end after advancing `H` + the interval and one more `Tick`.
- **T-669, T-670** build the gateway with the real `Memory`, `Reconciler`, database `Ledger`,
  `ledger.Writer` and `EventWriter` on a migrated test database, with the clock of `limitstest`.
  T-670 stops the writer (flush), builds a second set of the same components on the same database
  and compares, for every rule, `Sum(limit, ledger)` with what `Memory` reports through
  `Counted(limit)` (a read-only accessor for tests and for 6.1).
- **T-675** lowers the rule bound through an unexported variable set by `export_test.go`.
- **T-678, T-679** iterate over the operations of the generated route table whose tag is `limits`
  or `platform-limits`, so that a route added later without a test fails them.
- **T-686** restarts the binary with the same configuration and database and makes one call.
- **T-689, T-690**: with `max_retries=0` the first call succeeds and the second raises the SDK's
  rate-limit error; assert status 429, the header `x-pandokos-error` (`rate_limited` or
  `quota_exceeded`) and a numeric `retry-after`. One test per family and code.

**Verify:** `go vet ./... && go test -count=1 ./... 2>&1 | tail -40` shows the new tests failing
and no compilation error. Changed tests of earlier sprints that fail until part C are listed in
the commit body.

## Part C — Implementation

### C1. Calendar — `feat: periods of quotas and budgets in a configured time zone`

**Files:** `internal/limits/period.go`. §5.2.

- `NewCalendar` rejects a nil location and a zone that has, between 2020 and 2040, an offset that
  is not a whole number of minutes (walk `ZoneBounds` from 2020-01-01).
- `Of(kind, t)`: `L` = civil date (or first of the month) of `t` in the zone; `Start = S(L)`,
  `End = S(next L)`. `S(L)`: candidates are `time.Date(y, m, d, 0, 0, 0, 0, loc)`, every zone
  transition instant within 36 hours of it (walk `ZoneBounds`), and, for every offset in effect in
  that range, civil midnight of `L` minus that offset; `S` is the earliest candidate `c` whose
  label is `L` or later and whose label one nanosecond earlier is before `L`.
- Cache: the last day and month computed, behind an `atomic.Pointer`; a hit is a range check.
- `t` is truncated to the microsecond before anything else.

**Verify:** `go test -count=1 -run 'TestCalendar' ./internal/limits/` (T-647), three more seeds.

### C2. Rules — `feat: limit rules with scope, currency, enforcement and thresholds`

**Files:** `internal/limits/{rules,load,cache}.go`. §5.3.

- `LoadRules` reads the new columns of both queries; a row with an unknown metric, a cost metric
  without currency, or a threshold outside 1 to 100 is skipped and counted as in 5.1.
- `RuleSet` is indexed by subject, then scope (`provider`, `model`), then metric, so that `For`
  does a fixed number of map lookups (three subjects × three scopes), never a scan of the rules.
- `For(q, cal)`: the order and the merge of §5.3. `Mark.At = ceil(value × percent / 100)`,
  computed in integers; marks sorted by `At`, then level (`platform` first), then percent. With
  `cal == nil` period rules are skipped (5.1 tests). `Limit.Period` from `cal.Of(kind, q.Start)`.
- The platform ceiling of (scope, metric) is the tenant's row if there is one, else the default,
  as in 5.1, now per scope.

**Verify:** `go test -count=1 -run 'TestResolve|TestRuleCache|TestLoadRules' ./internal/limits/`
(T-619, T-623, T-624, T-654, T-662).

### C3. Period counters — `feat: period counters in the in-process store`

**Files:** `internal/limits/{memory,periods}.go`. §5.4, §5.6 (the store side), §5.9.

- `counterKey` gains `provider`, `model`, `currency`; a period counter is additionally keyed by
  `Period.Start`. `validate` accepts the nine metrics and detects a repeated counter with a map
  when a request has more than 16 limits. The fixed array of twelve counters in `Reserve` goes.
- Per shard: `cuts map[tenant.ID]time.Time`. Methods (all take the shard lock, none does I/O):

```go
func (m *Memory) Cut(t tenant.ID) (time.Time, bool)
func (m *Memory) SetCut(t tenant.ID, cut time.Time)            // only when the tenant has none
// Missing returns the period limits of ls that Reserve could not serve: no counter, and the
// tenant has no cut or the period started before it.
func (m *Memory) Missing(ls []Limit) []Limit
// Load creates the counters of ls for one period from rows read with liveFrom = the tenant's cut.
// A counter that exists is left alone.
func (m *Memory) Load(t tenant.ID, p Period, ls []Limit, rows []UsageGroup)
type Piece struct{ From, To time.Time; Rows []UsageGroup }
// Fold adds the pieces to the base of every counter of t whose period contains them, drops the
// live minutes before cut and sets the cut. cut must not be before the current one.
func (m *Memory) Fold(t tenant.ID, pieces []Piece, cut time.Time)
// Rebase replaces the base of every counter of t with period p by the sum of rows.
func (m *Memory) Rebase(t tenant.ID, p Period, rows []UsageGroup)
func (m *Memory) PeriodTenants() []tenant.ID                    // tenants with a cut
func (m *Memory) Periods(t tenant.ID) []Period                  // distinct periods of its counters
func (m *Memory) Counted(l Limit) (Amount, bool)                // tests and metrics
```

- A counter keeps the identity of the limit it was created for (subject, scope, metric,
  currency) so that `Fold` and `Rebase` can call `Sum` with it; marks and `Max` come with each
  request's `Limit` and are remembered from the last one for the tests after a fold.
- `Reserve`, `Settle`, `Release`, expiry: the table of §5.4. Cost arithmetic with
  `pricing.Decimal`; `Max` is compared as `Max / CostScale`. The settled cost is
  `actual.Cost.Amount` when `actual.Cost.Known` and its currency is the counter's.
- Marks: after a settle, `Load`, `Fold`, `Rebase`: for each mark with `used ≥ At` not yet
  recorded for (rule, percent) in this counter, `Events.Offer`; record it only when the offer was
  taken. `exhausted`: at the first refusal by this counter, same rule. A nil sink records
  nothing and marks nothing.
- A settle or release whose minute is before the cut (it cannot happen while the reservation
  lifetime is shorter than the horizon) changes `reserved` only. `Refusal.RetryAfter` and `Reset`
  are no longer bounded by 61 s for a period limit; fix the comment of the type.
- `Sweep`: the two drop rules of §5.4; a tenant without period counters loses its cut.
- `Sum` (`usage.go`): a group matches a limit when the subject matches (`tenant`: always;
  `service_account`: `PrincipalID == ID`; `key`: `KeyID == ID`), the provider is empty or equal,
  the model is empty or equal; it adds `Requests`, `Tokens`, or `Cost` when `Currency` is the
  limit's.

**Verify:** `go test -race -count=1 ./internal/limits/` without the database tests: T-648 to
T-657 pass; `go test -race -count=20 -run 'Race|Property' ./internal/limits/`.

### C4. Ledger reader, reconciler, event writer — `feat: period counters loaded from the ledger and reconciled; limit events`

**Files:** `internal/limits/{usage,reconcile,events}.go`, `internal/ledger/writer.go`. §5.6, §5.7,
§5.9.

- `NewLedger(db).Usage`: `db.WithTenant`, `AggregateUsageForLimits`; `tokens` parsed as an
  integer saturating at `math.MaxInt64`, `cost` with `pricing.ParseDecimal`; timeout 5 s.
- `Reconciler.cut(now)`: the formula of §5.6, floored to the minute.
- `Ensure(ctx, t, ls)`: `store.Missing(ls)`; empty → nil without taking any lock of its own.
  Otherwise, under the tenant's mutex (a `sync.Map` of mutexes; waiting honours `ctx` and a 5 s
  bound): re-check; set the cut when the tenant has none; for each distinct period among the
  missing limits whose period started before the cut, one `Usage(t, p.Start, cut, now)` and one
  `store.Load`; the others are left to `Reserve`. An error leaves the store unchanged and is
  returned.
- `Tick(ctx)`: for each tenant of `PeriodTenants`, under its mutex: `C'` = new cut; when later
  than the cut: pieces of `[C, C')` split at every day start of the calendar in between, one
  `Usage(t, from, to, to)` each (no live part), then `Fold`. Then, when the tenant's last rebase
  is an hour old: for each of `Periods(t)`, `Usage(t, p.Start, C', C')` and `Rebase`. Errors are
  logged (`limit reconciliation failed`, warn, tenant, error class as `ledger.errorClass` does)
  and the tenant is retried at the next tick. `Run` calls `Tick` every `Interval`.
- `EventWriter`: a channel of 1,024; `Offer` never blocks; `Run` inserts one event per
  transaction of its tenant (`InsertLimitEvent`), logs `limit event recorded` when a row was
  inserted, retries with a backoff of 100 ms doubling to 5 s while `store.IsUnavailable`, drops
  on any other error with `limit event not recorded` (error; kind, metric, SQLSTATE). `observed`
  and `limit_value` are written in the unit of the metric (the cost as a decimal, the value of a
  budget in millionths).

**Verify:** `go test -count=1 -run 'TestLedgerUsage|TestReconciler|TestEventWriter|TestLoadFoldRebase|TestPropertyReconcile' ./internal/limits/`
(T-651, T-652, T-658 to T-660); `go test -count=1 ./internal/ledger/`.

### C5. Pipeline — `feat: quotas and budgets in the pipeline: quota_exceeded, no_price, counted with the credential`

**Files:** `internal/gateway/{estimate,limits,handler,record}.go`, `internal/family/*`,
`internal/resolver/{limits,catalogue}.go`. §5.1, §5.5, §5.7, §5.8, §5.10.

- `estimateOf`: the default of §5.10; the estimate carries `Provider`, `Model` (the target's
  names) and `Start` (`st.start`).
- Handler: `st.counted = true` in the statement that sets `st.opened`; `settleLimit` tests
  `counted`; the field `forwarded` is removed if nothing else reads it.
- `resolver.Limits.Reserve`, in this order: rules; `For(Query, calendar)`; no limit → the empty
  reservation; **unpriced**: the cost limits whose currency is not that of a known estimate —
  `refuse` returns a refusal with `Code: "no_price"`, `allow` removes them and marks the
  reservation; **ensure**: when a period limit remains, `Reconciler.Ensure`; on error `refuse`
  returns the error, `allow` removes `store.Missing(ls)` and marks the reservation; **reserve**;
  an error of the store under `allow` yields the empty reservation, marked. The refusal's `Code`
  is `quota_exceeded` for a period metric, else `rate_limited`; `Scope` from the limit; `Currency`
  for a budget.
- `LimitsOptions` gains `Calendar`, `Reconciler` (an interface with `Ensure`), `OnUnknownPrice`,
  `OnStoreFailure`. The reservation exposes `Unverified() bool` and `Unpriced() bool` through an
  optional interface the gateway reads for the two log fields.
- `gateway/limits.go`: the code and headers of §5.8 by `ref.Code`; budget values formatted from
  millionths (`250500000` → `250.5`); `Retry-After` omitted for `no_price`; log fields
  `limit_scope`, `limit_currency`. `outcomeOf` maps both codes to `rejected_limit`.
- The line `limit counters unavailable` (warn) is logged by the resolver, at most once per tenant
  and 10 s.

**Verify:** `go test -race -count=1 ./internal/gateway/ ./internal/resolver/ ./internal/family/... ./test/familycodes/`
(T-663 to T-674 and the changed tests of 5.1).

### C6. Administration API — `feat: administration API for limits; capability budget; model default output`

**Files:** `internal/admin/{limits,platform_limits,capability,models,convert_catalog,admin}.go`.
§5.11 to §5.13.

- Tenant operations run in `inTenantPath`, platform ones in `inPlatform`. Creation: validate the
  body (§5.11) → `LockLimitRules` and `CountLimitRules` (tenant rules only) → `LimitSubjectExists`
  and `LimitScopeExists` → insert → audit. A unique violation of `limit_rule_key` or
  `platform_limit_rule_key` is `conflict`; a foreign-key violation of the platform rule's tenant
  is 400 on `tenant`; a check violation that validation should have caught is `internal`.
- Update: `LockLimitRule`, merge, validate the result, update, audit with `change(from, to)` per
  changed field; an update that changes nothing writes no audit row and returns the rule.
- `amount` ↔ `limit_value`: parse with `pricing.ParseDecimal`, multiply by 10⁶, require an
  integer between 1 and 10¹⁵; format back without trailing zeros.
- Field messages: `subject_id` "must name a service account, a virtual key or a subject mapping
  of this tenant"; `provider` "must name a provider"; `model` "must name a model available to
  this tenant" (platform: "must name a model of the provider"); `tenant` "must name a tenant".
- `GetCapability`: `limits.LoadRules` on the admin pool, `For(Query{Provider, Model, Start: now},
  calendar)` with empty IDs, keep period limits with `Max > 0`; the price version in force from
  the resolved target for the unpriced rule; one `AggregateUsageForLimits` per distinct period on
  the admin pool (`liveFrom = to = now`), `limits.Sum`; the values and reasons of §5.13.
  `admin.Options` gains `Limits struct{ Calendar *limits.Calendar; OnUnknownPrice string }`; a
  nil calendar means UTC.
- Models: the field in create, update (0 clears), the resource, the audit detail; the catalogue
  snapshot and `resolver.Catalogue` carry it to `Target.DefaultOutputTokens`.

**Verify:** `go test -count=1 ./internal/admin/ ./internal/authz/ ./internal/catalog/ ./test/isolation/`
(T-661, T-675 to T-683 and the changed tests).

### C7. CLI — `feat: CLI commands for limits`

**Files:** `internal/cli/limits.go`, `internal/cli/catalogue.go`. §5.15.

Commands and flags as §5.15, in the style of `issuers.go`. `--service-account` resolves a name
through `resolveServiceAccount`; `--limit` and `--amount` are mutually exclusive; `--soft` and
`--enforce` are mutually exclusive; `--threshold` and `--no-thresholds` likewise. Tables show
`id`, subject, scope, metric, value (the limit or `amount currency`), `enforce`, thresholds.

**Verify:** `go test -count=1 ./internal/cli/` (T-684 and the command coverage test).

### C8. Wiring — `feat: quotas and budgets wired into serve`

**Files:** `cmd/pandokos/serve.go`, `internal/server/server.go`, `internal/config/*`. §5.14, §5.15.

The order of construction of §5.15. `Reconciler.Run` and `EventWriter.Run` start with
`Memory.Run`; at shutdown, after `writer.Close`, `EventWriter.Close` with what is left of the
flush deadline (at least 1 s); a non-zero count is logged (`limit events not recorded`, warn).
The start-up log line of the limits names the zone and the two settings.

**Verify:** `go test -count=1 ./cmd/... ./internal/server/ ./internal/config/` (T-685).

### C9. Everything together — `test: end-to-end quotas and budgets, restart, canaries, SDK suites`

Make T-686 to T-691 pass; extend the SDK harness (§7). Fix what they find in the code of C1 to
C8, in separate commits named after the part.

**Verify:** `go test -count=1 ./test/e2e/ -run 'TestMonthlyBudget|TestLimitAdministration|TestQuotaCanary'`;
`make test-sdk test-sdk-python test-sdk-ts`.

## Part D — Lint rules, fuzz and bench targets, container (commit: `ci: quotas and budgets in lint rules and bench targets`)

- `.golangci.yml`: the depguard rules for T-R43 (`internal/limits` may not import
  `internal/ledger`, `internal/admin`, `internal/gateway`).
- `Makefile`: nothing new unless `bench` lists packages explicitly; the benchmark of T-622 has a
  case with nine limits of which six are period limits.
- Container and Compose: no change of image contents. Run `make test-container` and `make
  test-compose`; the binary size before and after (the embedded zone database) goes into the pull
  request (`ls -l` of the built binary on both commits; do not quote a number from memory).

**Verify:** `make lint && make bench && make test-container && make test-compose`.

## Part E — Documentation and decision records (commit: `docs: sprint 5.2 documentation and ADRs`)

### E1. `docs/operations/limits.md`

Rewrite section 1 ("What is counted") with the nine metrics; add, in this order: *Periods and the
time zone* (§5.2, with one worked example of a 23-hour day and the rule "a request belongs to the
period it started in"); *Budgets* (§5.5: currency, unknown prices, both settings, the bound and
its cost form); *Scopes* (§5.3); *Thresholds and events* (§5.9); *What is strict, bounded and best
effort* (the table of §5.15, word for word, then the table of §5.6); *Counters and the ledger*
(§5.6 in operator's terms: restart, the horizon and how it is derived, the interval); *When
counters cannot be verified* (§5.7); *Managing rules* (the CLI of §5.15 with one example per
kind: a rate rule, a monthly budget with thresholds 80 and 100, a model-scoped daily quota, a
platform default concurrency, a soft rule); *Choosing values* (extend: a budget not below the
cost of the largest request; a concurrency limit next to a tight budget; the per-model default).
Replace section 9 ("What is not there yet") by what remains: several instances, delivery of
events, metrics.

### E2. Changed pages

- `docs/operations/configuration.md`: the four keys. `gateway.md`: the codes `quota_exceeded`
  and `no_price`, the two headers, D-2 in the table of how a request ends. `usage.md`: the three
  `error_code` values of `rejected_limit`; which records count. `catalogue.md`:
  `default_output_tokens`. `database.md`: migration 0010 (adds columns with defaults; no rewrite
  of `usage_record`; safe to apply before the new binary starts, and an old binary keeps working
  on the new schema because it ignores metrics it does not know).
- `docs/reference/admin-api.md`: the twelve operations, validation, the six audit actions, the
  capability values. `permissions.md`: six permissions and the two roles.
- `docs/architecture/overview.md` (limits step, dependency table: the ledger as the source of
  period counters; the worker's reconciliation exists), `data-model.md` (`limit_event`, the new
  columns), `security-model.md` (spend: what a budget guarantees and what it does not).
- `README.md` feature list if it mentions limits.

### E3. Decision records (status `proposed`, date of the commit; index in `docs/adr/README.md`)

**0050 — Quotas and budgets: periods, attribution, currencies and unknown prices.** Refines 0012
and 0048. *Context:* 0012 named a counter per period "in the configured time zone" and left open
which requests a period holds, what a budget's unit is, several currencies and unknown prices.
*Decision:* (1) six metrics per day and month; one zone per installation, embedded zone database;
a period is the set of requests whose start has that civil date, so days follow the zone's clock
changes. (2) A request counts exactly when its usage record carries a credential ID (replaces
"forwarded" of 0048 for one narrow case). (3) A budget is an amount in one currency, stored in
millionths, counted with exact decimals, and counts known costs in that currency only; nothing is
converted. (4) A request a budget cannot measure is refused with 409 `no_price` by default, or
admitted uncounted (`limits.on_unknown_price`); an unknown cost at settlement counts zero. (5)
Period limits refuse with 429 `quota_exceeded`, `Retry-After` to the end of the period. (6) The
bound of 0048 holds per period; for cost the excess of one request is bounded by the cost of a
full context window and output at the version's highest rates. (7) A model can carry its default
output reservation. *Consequences:* counter and ledger aggregate are the same sum; a tenant with
a budget cannot call an unpriced model unless the operator says so; mixed currencies need one
budget per currency on narrower scopes. *Alternatives:* attribution by admission time (differs
from the ledger at every boundary); a zone per tenant or rule (two rules on one counter could
disagree on the period); converting currencies; counting unpriced requests at zero silently.

**0051 — Period counters and the ledger: load, cut, reconciliation, the failing store.** Refines
0012. *Context:* 0012 says counters are rebuilt at start and reconciled by the worker; it does not
say how a running instance replaces what it counted by what the ledger holds without counting a
request twice. *Decision:* (1) a period counter is `base` (ledger, requests started before the
tenant's cut) plus `live` (this instance's reservations and settlements by start minute). (2) The
cut is the minute before which every request has ended and its record is committed or lost: now
minus the horizon, held back by the oldest unwritten record. (3) Load on first use per tenant,
fold every `limits.reconcile_interval`, rebase hourly; all read the ledger on the worker role in
the tenant's session, through one query and one matching function. (4) Rate windows and the
concurrency gauge are not reconciled. (5) `limits.on_store_failure`: refuse with 503 by default;
`allow` admits without the limits that could not be verified. Rules that cannot be loaded always
refuse. (6) The capability check computes `checks.budget` from the ledger. *Consequences:* after a
restart counters equal the ledger; any discrepancy disappears one horizon and one interval after
the request started; records that never reach the ledger are not counted; a gateway process
without the worker role needs the shared store of epic 8. *Alternatives:* loading every tenant at
start; tracking each request until its record is committed; reconciling by full recomputation
only (needs quiescence or double counts); counters in PostgreSQL (0012).

**0052 — Administration of limits: scopes, thresholds, events, API and permissions.** Refines
0049; implements the first half of 0014 for limits. *Context:* 0049 fixed storage and meaning of
rules and left their writing to this sprint; LIM-04 asks for provider and model scopes, LIM-05 for
soft thresholds. *Decision:* (1) both rule tables carry scope, currency, enforcement and
thresholds for all metrics; one rule per subject, scope and metric; value, enforcement and
thresholds are the only mutable fields. (2) Two API resources, tenant rules and platform ceilings,
with read, write and delete permissions per scope; a tenant reads the ceilings that apply to it.
(3) Writes check that subject and scope exist, with one answer for "elsewhere" and "nowhere", and
bound a tenant to 5,000 rules under a lock. (4) Every change is audited in its transaction. (5) A
threshold is reached when what was used passes it; an enforced limit is exhausted at its first
refusal in a period; both are rows of the append-only table `limit_event`, unique per rule,
period, kind and percentage, inserted by the gateway role off the request path. 6.4 adds the
outbox row in the same transaction. *Consequences:* events are durable and idempotent before any
delivery exists; an event can be lost only across a crash at the end of a period. *Alternatives:*
thresholds as separate soft rules; events detected by the worker from ledger aggregates (minutes
late); writing the outbox now (its schema belongs to 6.4).

History lines (one line each, dated): 0012 (5.2 implements periods, budgets, rebuild and the
failing store: 0050, 0051), 0014 (limit facts recorded in `limit_event`; outbox in 6.4), 0020
(`quota_exceeded` in use; `no_price` 409 added), 0032 (six actions), 0035 (`default_output_tokens`),
0043 (`checks.budget` values and two reasons), 0048 (counted with the credential; per-model
default), 0049 (columns, API: 0052).

### E4. Planning files and traceability

- `planning/epics/05-limits-and-budgets/epic.md`: under 5.2, a line "Designed and delivered as
  [5.2-quotas-and-budgets/sprint.md](5.2-quotas-and-budgets/sprint.md)"; the risk note gains
  "delivered as `model.default_output_tokens`".
- `planning/roadmap.md`: nothing until the sprint closes (the orchestrator sets the status).
- `planning/epics/06-observability-reports-lifecycle/epic.md`: 6.2 gains "the usage rollup serves
  the limit counters and the capability check (5.2 §11)"; 6.4 gains "the outbox row of a limit
  event is written with its `limit_event` row (5.2 §11)".
- `docs/requirements/README.md` is not edited (it holds no status column); the coverage table of
  §9 is the traceability record.

**Verify:** `go test -count=1 ./test/repo/` (T-R42 to T-R44 and the documentation tests of
earlier sprints); every relative link of the changed pages resolves (the repository's link test).

## Part F — Verification (no commit unless something is found)

1. `make check`; `make test-sdk-python test-sdk-ts`; `make test-container test-compose`.
2. `go test -race -count=20 ./internal/limits/`; the property tests with three more seeds
   (`PANDOKOS_TEST_SEED=<n> go test -count=1 -run 'Property|TestCalendar|TestResolveScopes' ./internal/limits/`),
   seeds and results quoted.
3. Walk §10 of sprint.md line by line and tick each box with the test or file that shows it.
4. Review against the two checklists: §5.4 (every row of the table; all-or-nothing with mixed
   limits; no I/O under the lock) and §5.6 (the invariant after load, fold, rebase; the cut never
   moves back; the five bullets of "why nothing is counted twice").
5. Isolation: `go test -count=1 ./test/isolation/`; confirm by reading that no new statement names
   a tenant in a `WHERE` clause and that the reader is only ever given the worker pool (T-R43).
6. Secrets: `make secrets`; T-688 at log level `debug`.
7. The pull request body: the tests-first output of part B, the coverage table of §9 with links,
   the seeds, the binary size before and after, and the answers the review needs for §12.

## What the implementer does not do

- No outbox table, relay or webhook (6.4). No metric (6.1). No rollup table (6.2).
- No change to `usage_record` or to what the ledger writer writes.
- No time zone per tenant or rule, no currency conversion, no "used against limit" endpoint.
- No new error code beyond `no_price`, no new problem code on the administration API.
- No status change of the sprint, the epic or the roadmap: the orchestrator closes the sprint.
