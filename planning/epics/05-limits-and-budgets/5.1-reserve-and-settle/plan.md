# Sprint 5.1 — Implementation plan

- Design: [sprint.md](sprint.md). Section numbers (§) refer to it.
- Each part below is self-contained: an agent needs sprint.md and that part only. Parts run in
  order A, B, C1 … C6, D, E, F.
- Suggested grouping for implementers: **G1** = C1 (counters and the in-process store: no
  database, no gateway); **G2** = C2 (rules, loader, cache); **G3** = C3 (the estimate). After
  part B the three do not depend on each other. C4, C5 and C6 are one agent, in order, after G1 to
  G3 are merged. A, B, D, E and F are one agent each. Because the risk class is high, C1 and C4
  are reviewed by an agent that did not write them, with §5.4, §5.6 and §5.7 as the checklist.

## Rules for the implementer

1. Branch `feat/5.1-reserve-and-settle` from `main`; sign off every commit. One commit for A, one
   for B (tests, failing), one per step of C (message given with the step), one for D, one for E,
   one for F.
2. **No new Go module**, in either `go.mod`. Property tests use `math/rand/v2`.
3. **The limiter sees numbers and identifiers only**: tenant ID, service account ID, key ID or
   mapping ID, metric names, token counts, a cost, a request ID. No body, no header value, no
   credential and no model output is passed to `internal/limits`, logged by it or put in a
   header. No test, fixture or document holds a real key.
4. **§5.4 and §5.7 are the contract.** Exactly one of settle and release per reservation, from
   `finish` only. Reserve changes every counter or none. Nothing in `internal/limits` reads a
   clock except through `Now`, starts a goroutine except `Memory.Run`, or performs I/O in
   `Reserve`, `Settle` or `Release`.
5. **The tenant of a counter and of a rule comes from the principal**, never from a header, a
   path or a body. `LoadRules` runs inside `WithTenant` of that tenant; no statement names the
   tenant in a `WHERE` clause (TEN-04).
6. **The gateway does not import `internal/limits`**; it sees limits through `gateway.Limits`
   (§5.10). `internal/admin`, `internal/cli`, `internal/authz` and `api/openapi/admin.v1.yaml` are
   not touched: no route, permission, command or audit action is added (D-13).
7. Appendices A1 (migration 0009) and A2 (queries) are copied byte for byte. A1 was run on
   PostgreSQL 16 while designing; A2, the schema lint and `sqlc` were not run on them. If one does
   not work as written, stop and ask.
8. Generated files are never edited by hand (`make generate`). SQL runs only as
   `queries.New(tx).<Method>` inside a `store` transaction helper (T-R10, T-R12).
9. Tests hold for root and non-root; nothing sleeps longer than 2 s; waiting is
   `testutil.Eventually`; time-dependent logic takes a clock. A bound is never made to pass by
   lengthening it and a database test never by skipping it. A property test that fails is fixed
   in the code or reported; its seed is never pinned to a passing one.
10. A hash, digest or version is never typed from memory: `make migrations-sum` writes checksums,
    `make generate` writes generated code.
11. English everywhere; every exported identifier has a doc comment.

## Environment

- `make db-up` starts PostgreSQL 16 with TLS and PgBouncer (Docker) and writes `local/testdb/env`.
  Docker pulls may be rate-limited: if the images are missing and cannot be pulled, stop and say
  so; do not skip database tests.
- `make test` (`go test -race -count=1 ./...`), `make test-sdk`, `make image` then `make
  test-container` and `make test-compose`, `make fuzz`, `make bench`, `make generate`, `make
  generate-check`, `make migrations-sum`, `make lint`, `make vuln`, `make secrets`, `make check`
  (the full gate).
- Before starting: `make db-up && make check` must pass on `main`.

---

## Part A — Scaffolding (commit: `chore: sprint 5.1 scaffolding: migration 0009, limits package, ports, configuration`)

After this part everything compiles, every existing test passes, and nothing is enforced.

**Files**

| File | Content |
|---|---|
| `internal/store/migrations/sql/0009_limit_rules.sql` | Appendix A1, byte for byte; then `make migrations-sum` |
| `internal/store/queries/sql/limits.sql` | Appendix A2, byte for byte; then `make generate` |
| `test/isolation/spec.go` | Appendix A3 |
| `internal/limits/limits.go` | The types, constants and `Store` of §5.2, complete, with doc comments; `var ErrNotOutstanding`; `func Metrics() []Metric` and `func SubjectKinds() []SubjectKind` in the order of §5.5 |
| `internal/limits/memory.go` | `MemoryOptions`, `NewMemory` (validates the logger), and the methods of §5.2 as stubs: `Reserve`, `Settle`, `Release` return `errors.New("limits: not implemented")`; `Run` returns when the context is done; `Sweep`, `Outstanding`, `Counters` return 0 |
| `internal/limits/rules.go` | `type Rule struct{ SubjectKind SubjectKind; SubjectID string; Metric Metric; Value int64 }`; `type RuleSet struct{ Tenant tenant.ID; Platform, Own map[Metric]int64; ServiceAccounts, Keys map[string]map[Metric]int64 }`; `func NewRuleSet(t tenant.ID, platformDefault, platformTenant map[Metric]int64, rules []Rule) RuleSet` and `func (s RuleSet) For(serviceAccountID, keyID string) []Limit`, both stubs returning zero values |
| `internal/limits/load.go` | `func LoadRules(ctx context.Context, db *store.DB, id tenant.ID) (RuleSet, error)` stub returning an error; `const MaxRulesPerTenant = 5000` |
| `internal/limits/cache.go` | `type RuleCacheOptions struct{ DB *store.DB; Logger *slog.Logger; TTL time.Duration; Now func() time.Time }`; `NewRuleCache`, and an unexported `newRuleCache(loader, options)` taking `func(context.Context, tenant.ID) (RuleSet, error)` for tests; `Rules(ctx, tenant.ID) (RuleSet, error)`, `Invalidate(payload string)`, `Flush()` as stubs |
| `internal/family/family.go` | `CodeRateLimited ErrorCode = "rate_limited"`; `Status()` 429; `Message()` as §5.8; constants `HeaderLimitSubject`, `HeaderLimitMetric`, `HeaderLimit`, `HeaderLimitRemaining`, `HeaderLimitReset` with the names of §5.8 |
| `internal/family/adapter.go` | `type OutputBound` and the method `OutputBound() OutputBound` on `Request` (§5.3) |
| `internal/family/openai/request.go`, `anthropic/request.go` | `OutputBound` as a stub returning `family.OutputBound{Choices: 1}` |
| `internal/family/anthropic/errors.go` | `CodeRateLimited` → `rate_limit_error` |
| `internal/gateway/ports.go` | The types and interfaces of §5.10; `Target.MaxOutputTokens`, `Target.ContextWindow` |
| `internal/gateway/handler.go` | `Options.Limits`, `Options.DefaultOutputTokens`, copied to the handler; no validation and no use yet |
| `internal/resolver/limits.go` | `type LimitsOptions struct{ Rules *limits.RuleCache; Store limits.Store; TTL time.Duration; Logger *slog.Logger }`; `func NewLimits(o LimitsOptions) (*Limits, error)`; `Reserve` as a stub that returns an error |
| `internal/config/{config,keys}.go`, `docs/operations/configuration.md` | The two keys of §5.11 with defaults and descriptions, defined like `cache.catalog_ttl`, without bounds yet; their two rows in the reference table, which a repository test compares with the code |
| `internal/server/server.go` | `Options.Limits gateway.Limits` (optional), passed on with `DefaultOutputTokens` from the configuration |
| every construction of `gateway.Options` (`grep -rn "gateway.Options{" internal test cmd`) | `DefaultOutputTokens: 4096` |
| every test type that implements `family.Request` | the new method |

`cmd/pandokos/serve.go` is not changed here: the binary enforces nothing until C5.

**Verification**

```
make migrations-sum && make generate && make generate-check
go build ./... && go vet ./...
make db-up && go test -race -count=1 ./...        # green: T-140 passes with the new spec
go test ./test/isolation/ -run 'TestSchemaLint|TestDefaultSpecMatchesDesign' -count=1 -v
make lint
```

If `TestSchemaLint` reports a finding for `platform_limit_rule` (a nullable `tenant_id` on a
global table is new), stop and ask: do not change the lint or the table.

### Appendix A1 — `internal/store/migrations/sql/0009_limit_rules.sql`

```sql
-- 0009_limit_rules: the limit rules a tenant sets for itself, its service accounts and its keys,
-- the ceilings the platform sets for tenants, and the notification that tells the instances that
-- rules changed (ADR 0049). Runs in one transaction as pandokos_owner. Every name is
-- schema-qualified.

-- Tells the instances that limit rules changed: 'limits:<tenant>' for rows of one tenant,
-- 'limits' for a platform default, which concerns every tenant.
CREATE FUNCTION pandokos.notify_limits() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    rec jsonb;
BEGIN
    IF TG_OP = 'DELETE' THEN
        rec := to_jsonb(OLD);
    ELSE
        rec := to_jsonb(NEW);
    END IF;
    IF rec ->> 'tenant_id' IS NULL THEN
        PERFORM pg_catalog.pg_notify('pandokos_cache', 'limits');
    ELSE
        PERFORM pg_catalog.pg_notify('pandokos_cache', 'limits:' || (rec ->> 'tenant_id'));
    END IF;
    RETURN NULL;
END
$$;

-- Rules of a tenant (tenant-scoped table). subject_kind 'tenant' is the tenant as a whole;
-- 'service_account' names a service account by its ID; 'key' names a virtual key by its key ID or,
-- for callers that present a token, a subject mapping by its ID. There is no foreign key to the
-- subject: a rule whose subject does not exist never matches a request. Only limit_value of a rule changes (column grants).
CREATE TABLE pandokos.limit_rule (
    tenant_id    uuid        NOT NULL,
    id           uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    subject_kind text        NOT NULL,
    subject_id   text        NOT NULL DEFAULT '',
    metric       text        NOT NULL,
    limit_value  bigint      NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT limit_rule_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT limit_rule_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT limit_rule_key UNIQUE (tenant_id, subject_kind, subject_id, metric),
    CONSTRAINT limit_rule_subject_kind_valid CHECK (subject_kind IN ('tenant', 'service_account', 'key')),
    CONSTRAINT limit_rule_subject_id_format CHECK (subject_id ~ '^[a-z0-9-]{0,64}$'
                                                   AND (subject_kind = 'tenant') = (subject_id = '')),
    CONSTRAINT limit_rule_metric_valid CHECK (metric IN ('requests_per_minute', 'tokens_per_minute', 'concurrent_requests')),
    CONSTRAINT limit_rule_limit_value_range CHECK (limit_value BETWEEN 1 AND 1000000000000)
);

CREATE TRIGGER limit_rule_guard BEFORE INSERT OR UPDATE ON pandokos.limit_rule
    FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();
CREATE TRIGGER limit_rule_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.limit_rule
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_limits();

ALTER TABLE pandokos.limit_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.limit_rule FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON pandokos.limit_rule
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));

GRANT SELECT ON pandokos.limit_rule TO pandokos_gateway, pandokos_admin;
GRANT INSERT (tenant_id, subject_kind, subject_id, metric, limit_value) ON pandokos.limit_rule TO pandokos_admin;
GRANT UPDATE (limit_value) ON pandokos.limit_rule TO pandokos_admin;
GRANT DELETE ON pandokos.limit_rule TO pandokos_admin;

-- Ceilings the platform sets for tenants (global table with explicit policies: it describes
-- tenants). A row without tenant is the default for every tenant; a row with a tenant replaces the
-- default of the same metric for that tenant. A tenant's session sees the defaults and its own
-- rows; the platform scope sees and changes all of them.
CREATE TABLE pandokos.platform_limit_rule (
    id          uuid        NOT NULL DEFAULT pandokos.uuid_v7(),
    tenant_id   uuid,
    metric      text        NOT NULL,
    limit_value bigint      NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT platform_limit_rule_pkey PRIMARY KEY (id),
    CONSTRAINT platform_limit_rule_tenant_fkey FOREIGN KEY (tenant_id) REFERENCES pandokos.tenant (id),
    CONSTRAINT platform_limit_rule_key UNIQUE NULLS NOT DISTINCT (tenant_id, metric),
    CONSTRAINT platform_limit_rule_metric_valid CHECK (metric IN ('requests_per_minute', 'tokens_per_minute', 'concurrent_requests')),
    CONSTRAINT platform_limit_rule_limit_value_range CHECK (limit_value BETWEEN 1 AND 1000000000000)
);

CREATE TRIGGER platform_limit_rule_guard BEFORE INSERT OR UPDATE ON pandokos.platform_limit_rule
    FOR EACH ROW EXECUTE FUNCTION pandokos.global_row_guard();
CREATE TRIGGER platform_limit_rule_notify AFTER INSERT OR UPDATE OR DELETE ON pandokos.platform_limit_rule
    FOR EACH ROW EXECUTE FUNCTION pandokos.notify_limits();

ALTER TABLE pandokos.platform_limit_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE pandokos.platform_limit_rule FORCE ROW LEVEL SECURITY;

CREATE POLICY platform_limit_rule_self ON pandokos.platform_limit_rule
    FOR SELECT TO pandokos_gateway, pandokos_admin
    USING (tenant_id IS NULL OR tenant_id = (SELECT pandokos.current_tenant_id()));
CREATE POLICY platform_limit_rule_platform_select ON pandokos.platform_limit_rule
    FOR SELECT TO pandokos_admin USING ((SELECT pandokos.platform_scope()));
CREATE POLICY platform_limit_rule_platform_insert ON pandokos.platform_limit_rule
    FOR INSERT TO pandokos_admin WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY platform_limit_rule_platform_update ON pandokos.platform_limit_rule
    FOR UPDATE TO pandokos_admin USING ((SELECT pandokos.platform_scope())) WITH CHECK ((SELECT pandokos.platform_scope()));
CREATE POLICY platform_limit_rule_platform_delete ON pandokos.platform_limit_rule
    FOR DELETE TO pandokos_admin USING ((SELECT pandokos.platform_scope()));

GRANT SELECT ON pandokos.platform_limit_rule TO pandokos_gateway, pandokos_admin;
GRANT INSERT (tenant_id, metric, limit_value) ON pandokos.platform_limit_rule TO pandokos_admin;
GRANT UPDATE (limit_value) ON pandokos.platform_limit_rule TO pandokos_admin;
GRANT DELETE ON pandokos.platform_limit_rule TO pandokos_admin;
```

### Appendix A2 — `internal/store/queries/sql/limits.sql`

```sql
-- Limit rules. Both statements run on the gateway pool in the session of the tenant whose rules
-- are loaded (store.DB.WithTenant). Neither names the tenant in a WHERE clause on purpose: the
-- policies do (TEN-04). In that session platform_limit_rule shows the defaults and the tenant's
-- own rows, nothing of another tenant.

-- name: ListLimitRules :many
SELECT r.subject_kind, r.subject_id, r.metric, r.limit_value
  FROM pandokos.limit_rule r
 ORDER BY r.subject_kind, r.subject_id, r.metric
 LIMIT 5001;

-- name: ListPlatformLimitRules :many
SELECT (r.tenant_id IS NULL)::boolean AS is_default, r.metric, r.limit_value
  FROM pandokos.platform_limit_rule r
 ORDER BY r.tenant_id NULLS FIRST, r.metric;
```

### Appendix A3 — `test/isolation/spec.go`

```go
// Global
"platform_limit_rule": {Reason: "ceilings the platform sets for tenants; a tenant's session sees the defaults and its own rows, the platform scope all of them; protected by its own policies", Restricted: true},

// Grants
"limit_rule": {
    "pandokos_gateway": {"SELECT"},
    "pandokos_admin":   {"DELETE", "INSERT(limit_value,metric,subject_id,subject_kind,tenant_id)", "SELECT", "UPDATE(limit_value)"},
},
"platform_limit_rule": {
    "pandokos_gateway": {"SELECT"},
    "pandokos_admin":   {"DELETE", "INSERT(limit_value,metric,tenant_id)", "SELECT", "UPDATE(limit_value)"},
},

// Fixtures
"limit_rule": {
    Insert: `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, subject_id, metric, limit_value)
VALUES ($1::uuid, 'key', 't' || substr(md5(random()::text), 1, 16), 'requests_per_minute', 10) RETURNING id::text`,
    Update: `limit_value = 11`,
},
```

Nothing is added to `SecurityDefiner`, `Views` or `AppendOnly`. The fixture's subject ID is random
so that the harness can insert several rows per tenant.

---

## Part B — Tests first (commit: `test: sprint 5.1 tests, failing`)

Every test of §9, written against the stubs of part A. They must compile and fail for the right
reason (a stub's error, a missing header, a wrong count), and the pull request quotes the failing
output. Three tests pass from the start, because part A already holds what they check, and are
named as such in the pull request: T-R40 (the imports are already right), T-625 (the triggers
exist) and T-626 (the migration exists).

**Files:** `internal/limits/{window,memory,property,rules,cache}_test.go`,
`internal/limits/load_db_test.go`, `internal/limits/bench_test.go`;
`internal/store/limits_db_test.go`; `test/isolation/limits_test.go`;
`internal/family/openai/outputbound_test.go`, `internal/family/anthropic/outputbound_test.go`;
`internal/gateway/{estimate,limits,load}_test.go`; `internal/gateway/levels_db_test.go`;
`internal/resolver/{limits_test,catalogue_internal_test}.go`; `internal/config/limits_test.go`;
`internal/server/server_test.go`; `test/e2e/limits_test.go`; `test/sdk/limits_test.go`;
`test/repo/limits_test.go`; the changed tests of §9.

### B1. Helpers

- `test/dbtest`: `SeedLimitRule(tb, tenant, subjectKind, subjectID, metric string, value int64)`,
  `SetLimitRule` (update of `limit_value`), `DeleteLimitRule`, `SeedPlatformLimit(tb, tenant *tenant.ID,
  metric string, value int64)` (nil: the default). Each opens the **admin** role and runs in
  `WithTenant` or `Platform`, so that the grants and policies are exercised; none uses the
  superuser connection. Plain SQL in a test helper is allowed here as in the other `dbtest` seeds.
- `test/testutil`: `StaticLimits` — a `gateway.Limits` with a queue of answers (admit, refuse
  with a given `LimitRefusal`, error) and a log of calls: each `Reserve` with its principal and
  estimate, each `Settle` with its `LimitUsage`, each `Release`, in order, safe for concurrent use;
  options to make `Settle` or `Release` panic. `RealLimits(tb, rules ...limits.Rule)` — a
  `resolver.Limits` over a real `limits.Memory` and a rule cache fed from a fixed `RuleSet`, with
  `Advance(d)` moving the store's clock and accessors to `Outstanding` and `Counters`.
- `test/fakeprovider`: an option to report, per request, usage of `reserved + d` tokens for a
  given `d` (the test passes the reservation it expects), and the existing gates to hold a stream
  open. No new behaviour beyond that.
- `internal/limits` property tests share one file of helpers: a schedule generator (operations
  reserve, settle with an amount below, at or above the reservation, release, advance the clock by
  0 to 90 s in steps that include sub-second ones, sweep), a reference model that keeps every
  admitted event with its time and amounts, and a seed taken from `PANDOKOS_TEST_SEED` or the
  clock, logged with `t.Logf` at the start of every property test. Each property runs 2,000
  schedules of up to 200 operations by default and 200 schedules with `-short`.

### B2. Where each test lives

| Tests | File | Notes |
|---|---|---|
| T-610 | `internal/limits/window_test.go` | the window type is unexported: an internal test |
| T-611 to T-615, T-621 | `internal/limits/memory_test.go` | fixed clock; state compared through a test-only snapshot of counters |
| T-616 to T-618, T-620 | `internal/limits/property_test.go` | written against `limits.Store` plus the three accessors, so that epic 8 can run them on another store |
| T-619 | `internal/limits/rules_test.go` | table, then random rule sets |
| T-622 | `internal/limits/bench_test.go` | `BenchmarkReserveSettle/{sequential,parallel-one-tenant,parallel-many-tenants}` |
| T-623 | `internal/limits/cache_test.go` | fake loader, fake clock; follow `internal/catalog/cache_test.go` |
| T-624 | `internal/limits/load_db_test.go` | `dbtest.EachMode`: direct and through PgBouncer |
| T-625 | `internal/store/limits_db_test.go` | a listening connection, as the existing notification tests |
| T-626 | `test/isolation/limits_test.go` | follow `TestCatalogPolicies` |
| T-627 | the two `outputbound_test.go` | table plus `FuzzOutputBound`; add the targets to `make fuzz` in part D |
| T-628 | `internal/gateway/estimate_test.go` | pure function, no handler |
| T-629 | `internal/resolver/catalogue_internal_test.go` | |
| T-630 to T-636 | `internal/gateway/limits_test.go` | `StaticLimits`; both families through the existing table of providers |
| T-637 | `internal/resolver/limits_test.go` | fake store and fixed rules |
| T-638, T-640 | `internal/gateway/load_test.go` | `RealLimits`; T-640 is skipped by nothing, `-short` included |
| T-639 | `internal/gateway/levels_db_test.go` | real `RuleCache` on the test database, rules through `dbtest` |
| T-641 | `internal/config/limits_test.go` | |
| T-642 | `internal/server/server_test.go` | |
| T-643 to T-645 | `test/e2e/limits_test.go` | rules through `dbtest` on the binary's database; `testutil.Eventually` for the 2 s bound |
| T-646 | `test/sdk/limits_test.go` | `startServer` gains an optional `gateway.Limits`; retries on for the second half |
| T-R40, T-R41 | `test/repo/limits_test.go` | T-R41 reads the constants through `limits.Metrics()`, `limits.SubjectKinds()` and the header constants |

Notes for T-640: the three phases are subtests on fresh stores. Phase (a) asserts equality (137),
not only the upper bound: a store that refuses too much also fails. Phase (b) sets the fake
provider's excess `d` and the concurrency limit `k` and asserts the sum of the tokens of the
records with outcome `ok` against `L + k*d`; it also runs with `d = 0` and asserts `≤ L`. Phase (c)
checks `Outstanding() == 0` right after the last handler returns (wait for the handlers, not for
time) and `Counters() == 0` after `Advance(61 * time.Second)` and one `Sweep`.

**Verification**

```
go build ./... && go vet ./...
go test -count=1 ./internal/limits/ ./internal/gateway/ ./internal/resolver/ ./internal/family/... 2>&1 | tail -60   # fails: quote it
make lint
```

---

## Part C — Implementation

### C1. Counters and the in-process store — `feat: in-process limit counters: reserve, settle, release, expiry`

**Files:** `internal/limits/window.go`, `internal/limits/memory.go`.

1. `window`: 61 buckets, the second of the newest bucket, a running total. `advance(sec)` zeroes
   the buckets that left and subtracts them from the total; a jump of 61 s or more empties it; a
   second that is not after the newest one is a no-op (time never goes back in a window: an
   earlier `Now` is treated as the newest second). `add(sec, n)` and `live(sec) bool` (is that
   admission second still counted).
2. `Memory`: 64 shards (`[64]struct{ mu sync.Mutex; counters map[counterKey]*counter; held
   map[string]*held }`), shard = FNV-1a of the tenant ID. `counterKey{tenant, kind, id, metric}`.
   A `counter` is a window or a gauge by metric. `held` keeps the reservation and its admission
   second.
3. `Reserve`: validate (limits of one tenant, known metrics, `Max ≥ 1`, non-negative estimate,
   positive TTL); with no limit return a reservation without touching a shard; otherwise the
   algorithm of §5.4. The reservation ID is the request ID plus a per-store sequence number, so
   that two reservations of one request ID cannot collide.
4. `Settle`, `Release`: §5.4. Both delete the `held` entry first and return `ErrNotOutstanding`
   when it is absent. A settle with a negative actual is treated as 0.
5. Refusal: §5.4 for `Remaining`, `RetryAfter`, `Reset`, `OverLimit`; the first limit without room
   in input order; `RetryAfter` the longest over all limits without room.
6. `Sweep(now)`: per shard, release what expired (log once each, level error, message `limit
   reservation expired`, attributes `request_id`, `tenant_id`), then delete counters with an
   empty window and a zero gauge. `Run`: a one-second ticker calling `Sweep(Now())`.
7. No allocation in `Reserve` beyond the reservation and, on first use, the counter: T-622
   reports it.

**Verification**

```
go test -race -count=1 ./internal/limits/ -run 'TestWindow|TestReserveAtomic|TestSettle|TestRelease|TestExpiry|TestRetryAfter|TestProperty|TestMemoryRace|TestTenantSeparation' -v
go test -race -count=20 ./internal/limits/ -run 'TestProperty|TestMemoryRace'
for s in 1 2 3; do PANDOKOS_TEST_SEED=$s go test -count=1 ./internal/limits/ -run TestProperty; done
go test ./internal/limits/ -run '^$' -bench ReserveSettle -benchmem
```

### C2. Rules — `feat: limit rules: resolution, loader, cache and notifications`

**Files:** `internal/limits/{rules,load,cache}.go`; `internal/authn/cache.go` (both caches),
`internal/vault/opener.go`, `internal/catalog/cache.go`.

1. `NewRuleSet`: the platform ceiling per metric is the tenant's platform value when present,
   else the default; rules are indexed by subject. `For`: the table and the order of §5.5;
   `Level` is `platform` when the platform ceiling is the smaller or the only value, else
   `tenant`; service-account and key limits have level `tenant`. An empty ID yields no limit for
   that subject.
2. `LoadRules`: `db.WithTenant(ctx, id, …)`, `ListPlatformLimitRules` then `ListLimitRules`; more
   than `MaxRulesPerTenant` rows is an error that names the count, not the tenant's data; a metric
   or subject kind the code does not know is skipped (a newer schema during an upgrade) and
   counted in one debug line.
3. `RuleCache`: copy the mechanics of the tenant entries of `internal/catalog/cache.go` (flight
   with epoch, lifetime from the start of the load, 10,000 entries then flush, 15 s load timeout
   detached from the caller's context). `Invalidate`: `limits:<tenant>` and `tenant:<tenant>`
   drop one tenant; `limits` drops all; `platform`, `issuers`, `catalog`, `catalog:…`, `vault:…`,
   `probe:…` are ignored; anything else flushes. A failed load logs `limit rules unavailable`
   (warn, `tenant_id`).
4. The three other caches: add `payload == "limits"` and the prefix `limits:` to their ignored
   payloads.

**Verification**

```
go test -race -count=1 ./internal/limits/ -run 'TestResolve|TestRuleCache|TestLoadRules' -v
go test -race -count=1 ./internal/store/ -run TestLimitNotifications
go test -race -count=1 ./test/isolation/ ./internal/authn/ ./internal/vault/ ./internal/catalog/
```

### C3. The estimate — `feat: request-side estimate of tokens and cost`

**Files:** `internal/family/openai/request.go`, `internal/family/anthropic/request.go`,
`internal/gateway/estimate.go`, `internal/resolver/catalogue.go`.

1. `OutputBound` in both families, from the members already scanned by `ParseOperation` (no
   second scan of the body): the table and the number rule of §5.3. One shared helper in
   `internal/family` reads "a top-level member that is a plain positive integer of at most 12
   digits" from a member's byte range.
2. `gateway.estimate(op, adapter, bodyLen, bound, target, defaultOutput, start) LimitEstimate`:
   the formula of §5.3, with saturating arithmetic; the price version is
   `pricing.VersionAt(target.Prices, start)`; an amount that `usage.CostFits` rejects is an
   unknown cost.
3. `resolver.Catalogue.Resolve` fills `MaxOutputTokens` and `ContextWindow` from the model.

**Verification**

```
go test -race -count=1 ./internal/family/... ./internal/resolver/ -run 'OutputBound|CatalogueLimitsValues'
go test -race -count=1 ./internal/gateway/ -run TestEstimate
go test ./internal/family/openai/ -run '^$' -fuzz FuzzOutputBound -fuzztime 30s
go test ./internal/family/anthropic/ -run '^$' -fuzz FuzzOutputBound -fuzztime 30s
```

### C4. The pipeline — `feat: reserve before the credential, settle in finish, 429 rate_limited`

**Files:** `internal/gateway/{handler,limits,record}.go`.

1. `New`: `DefaultOutputTokens` must be positive. `reqState` gains `reservation Reservation`,
   `reservedTokens int64`, `refusal *LimitRefusal`, `forwarded bool`.
2. Step 7b, in `limits.go`, called from `ServeHTTP` right after `st.target, st.resolved = …` and
   before `credentials.Open`. With `h.limits == nil` it returns at once. It computes the estimate,
   calls `Reserve` with the request's context, and:
   - on an error: when the caller is gone, abort as `unavailable` does; otherwise set
     `Retry-After: 5` and answer `unavailable`;
   - on a refusal: keep it in the state, set the headers of §5.8 on the response
     (`Retry-After` and `Reset` as whole seconds rounded up; `Reset` omitted when it is 0), then
     `h.fail(st, family.CodeRateLimited)`;
   - otherwise keep the reservation and the reserved tokens.
3. `st.forwarded = true` is the statement immediately before `h.proxy.ServeHTTP`.
4. `finish`: after `finishRecord` (so the record exists) and before `logRequest`, when a
   reservation is held: `Settle(LimitUsage{…})` if `forwarded`, else `Release()`, inside a
   function that recovers and logs `limit settle failed` (error; `request_id`, `panic_type`). The
   tokens are `costUsage.InputTokens + costUsage.OutputTokens` when the record's input source is
   not `none`, else 0; the cost is the record's cost. When the request was not admitted or holds
   no reservation, nothing is called.
5. `outcomeOf`: `CodeRateLimited` → `usage.OutcomeRejectedLimit`. The comment on that constant in
   `internal/usage/record.go` loses "unused until a later sprint".
6. `logRequest`: the fields of §5.9, after `error_code`.

Do not move, merge or reorder any existing step. Do not read the clock for the estimate: use
`st.start`.

**Verification**

```
go test -race -count=1 ./internal/gateway/ -run 'TestReserveStep|TestRateLimited|TestRejectedLimitRecord|TestSettleEqualsRecord|TestReleaseWhenNotForwarded|TestEveryEndSettles|TestLimiterFailures|TestPipelineOrder|TestAnthropicPipelineOrder|TestNew' -v
go test -race -count=1 ./internal/gateway/ ./internal/family/... ./internal/usage/
```

### C5. Wiring — `feat: limits wired into serve: rule cache, store, sweeper, configuration`

**Files:** `internal/resolver/limits.go`, `internal/config/validate.go`, `internal/server/server.go`,
`cmd/pandokos/serve.go`.

1. `resolver.Limits.Reserve`: subjects from the principal (`TenantID`, `ID`, `KeyID`); only for
   `authz.KindServiceAccount` with a tenant, anything else is an error; `Rules` → `For`; with no
   limit, a shared reservation whose methods do nothing; else `Store.Reserve` with the request ID
   and the TTL. The reservation's `Settle` and `Release` call the store with
   `context.WithTimeout(context.Background(), 2*time.Second)` and log an error other than nil at
   level error (`limit settle failed`, `request_id`, `error`).
2. Configuration bounds of §5.11 in `validate`.
3. `serve.go`: build `limits.NewMemory`, `limits.NewRuleCache` (gateway pool, `cache.limits_ttl`),
   `resolver.NewLimits` with `TTL = limits.max_request_duration + time.Minute`; pass it in
   `server.Options`; add `ruleCache.Invalidate(p)` to `OnNotify` and `ruleCache.Flush()` to
   `OnReset`; run `memory.Run(background)` in the group of the background goroutines, so that it
   stops after the handlers.

**Verification**

```
go test -race -count=1 ./internal/resolver/ ./internal/config/ ./internal/server/ ./cmd/...
go test -race -count=1 ./internal/gateway/ -run 'TestConcurrencyLimit|TestLevels|TestParallelLoad' -v
```

### C6. Everything together — `test: end-to-end limits, parallel load, canaries, SDK`

**Files:** only what the tests of T-640 and T-643 to T-646 show to be missing; no new behaviour.

**Verification**

```
go test -race -count=1 ./test/e2e/ -run 'TestRateLimit|TestLimitsPerTenant|TestLimitCanary' -v
go test -race -count=5 ./internal/gateway/ -run TestParallelLoad
make test-sdk
make test
```

---

## Part D — Lint rules, fuzz and bench targets, container (commit: `ci: limits in lint rules, fuzz and bench targets`)

No new CI job, image or digest.

| File | Change |
|---|---|
| `.golangci.yml` | In the depguard list of the gateway (`gateway-takes-plain-values`): `internal/limits`, "the gateway sees limits through gateway.Limits". In the lists that keep `internal/admin` and `internal/cli` from the gateway's internals: `internal/limits` too |
| `Makefile` | `bench` also runs `go test ./internal/limits/ -run '^$$' -bench . -benchmem`; `fuzz` gains the two `FuzzOutputBound` targets, in the form of the existing ones |
| `.github/workflows/ci.yml` | unchanged; read it to confirm that `make fuzz`, if CI runs it, picks the new targets from the Makefile and not from a list in the workflow |
| `Dockerfile`, `test/compose`, `deploy/` | unchanged: no new required key, port or volume |

**Verification**

```
make lint && make fuzz && make bench
make image && make test-container && make test-compose      # unchanged behaviour with no rule
```

---

## Part E — Documentation and decision records (commit: `docs: sprint 5.1 documentation and ADRs`)

### E1. New page: `docs/operations/limits.md`

In this order: (1) the box at the top: limits are per gateway instance until the Valkey store
exists; rules are written through the administration API from sprint 5.2, and until then no rule
exists in an installation; (2) the three metrics, each with its exact meaning (§5.4: the window is
never shorter than a minute and up to a second longer; a request counts once Pandokos tries to
send it); (3) levels and subjects with the table of §5.5 and two worked examples (a platform
default with one tenant raised; a tenant limit with a tighter key); (4) reserve and settle: the
estimate of §5.3 as a table, with what to tell callers (set an output limit) and how
`limits.default_output_tokens` and the catalogue's `max_output_tokens` and `context_window` enter;
(5) **what is strict and what is bounded**, the table and the inequality of §5.6 in its words,
with one numeric example; (6) how a request ends, the table of §5.7; (7) the 429: §5.8, with a
`curl -i` transcript that shows placeholders, and how to tell it from a provider's 429; what the
OpenAI and Anthropic SDKs do with it; (8) sizing advice from R-2 to R-4 (a token limit not below
the largest request; a concurrency limit to bound the overshoot); (9) the log fields of §5.9 and
troubleshooting by field; (10) what is not there yet, with the sprint that brings it.

### E2. Changed pages

| Page | Change |
|---|---|
| `docs/operations/configuration.md` | the two keys with their bounds (the rows exist since part A; add the bounds table rows) |
| `docs/operations/gateway.md` | step 7b in the pipeline; the 429 in the table of errors; the new log fields |
| `docs/operations/usage.md` | outcome `rejected_limit` is written; such rows carry no usage and no cost |
| `docs/operations/database.md` | the two tables, who reads and writes them, the two notification payloads |
| `docs/operations/providers.md`, `keys.md`, `tokens.md` | one sentence each with a link: a key or a mapping can be the subject of a rule |
| `docs/reference/data-flows.md` | no new flow: say that limits add no outbound connection |
| `docs/architecture/overview.md` | §5 step 5 is built: remove "limits, step 5, are not built yet, so in the code this step follows the model check directly" and state the real order (model, reserve, credential); the dependency table's Valkey row and §9 unchanged; the cache table gains the rule cache; the package list's `limits/` line reads "rules, their cache, counters in memory (Valkey with epic 8)" |
| `docs/architecture/data-model.md` | `platform_limit_rule` in the platform group; a short section on the two tables and the absence of a foreign key to the subject |
| `docs/architecture/security-model.md` | the rows "A tenant overspends through concurrency" and "Resource exhaustion" name what is enforced now and per instance |
| `docs/requirements/README.md` | unchanged |
| `README.md` | the status sentence, if it lists what v0.2 has so far |

### E3. Decision records (status `proposed`, date of the commit; index in `docs/adr/README.md`)

**0048. Reserve and settle in the pipeline: estimate, counters, guarantees and the 429 contract**
(refines 0012; refines 0020, 0037, 0039).
*Context*: 0012 fixed the semantics (reserve, settle, expiry), the kinds of limit and the
guarantees in outline. It left open where the reservation sits in the pipeline, how a request is
estimated without a tokenizer, what a sliding window is exactly, what "settle" uses, and the
names of the 429 headers; its bound on the overshoot named the output only.
*Decision*: (1) The reservation is one atomic call between the model check and the credential,
for every limit of the request, the concurrency slot included; settle or release happens once, in
the handler's finish. (2) The estimate: input is the request's bytes over four through the
family's estimator, capped by the model's context window; output is the caller's limit per
choice times the choices, else a configured default, capped by the model's maximum output; a
limit counts only when written as a plain positive integer; the cost is that usage at the price
in force. (3) A rate window is 61 buckets of one second; an amount belongs to its admission
second and is counted for at least 60 s; settle corrects that second while it is counted.
(4) Settlement uses the figures of the usage record, whatever their source; a request that was
never sent is released in full; a request that was sent stays counted as a request; a refused
request consumes nothing; token counting is a request without tokens. (5) Guarantees: request
rate and concurrency are strict; for tokens, in every 60 s interval the tokens of the requests
admitted in it are at most the limit plus the sum, over those still in flight at the last
admission, of what each used above its reservation; one request can exceed its reservation by at
most the model's maximum output minus the reserved default when it set no limit, and by the
context window minus the estimated input. (6) A reservation is a value with an expiry; the
gateway sets it beyond the longest request; in process an expiry is a defect and is logged.
Counters are per instance and are not rebuilt at start. (7) A refusal is 429 `rate_limited` in
the family's shape with `Retry-After` and the headers `X-Pandokos-Limit-Subject`, `-Metric`,
`X-Pandokos-Limit`, `-Remaining` and `-Reset`; one code serves the three metrics; the usage
record has outcome `rejected_limit`.
*Consequences*: the ledger and the counters agree by construction, which rebuilding counters from
the ledger needs; a body that is large in bytes and small in tokens reserves more than it uses
until it settles; a token limit below the largest request refuses that request for good; limits
multiply by the number of instances until the shared store exists.
*Alternatives considered*: reserving the model's maximum output when the caller sets no limit
(one request in flight exhausts a token limit); a tokenizer per provider (a dependency per
provider and still an estimate); an exact log of events per counter (memory per request); the
two-bucket weighted window (not strict); attributing tokens to the time of settlement (a long
stream would be charged to a minute in which nothing was admitted, and the bound could not be
stated per interval); taking the concurrency slot before the body is read (two calls that must be
kept consistent); a code per metric.

**0049. Limit rules: levels, subjects and resolution** (refines 0012).
*Context*: LIM-04 asks for limits at platform, tenant and key level, where a lower level can only
restrict. 0012 said that every applicable limit is reserved; it did not say where rules live,
what the platform level means, or what a rule's subject is now that a caller can be a key or a
token.
*Decision*: (1) Two tables. `platform_limit_rule` is global and written in platform scope: a row
without tenant is the default ceiling of every tenant, a row with a tenant replaces the default
for it. `limit_rule` is tenant-scoped: a rule names a subject (the tenant, a service account, or
a key: a virtual key by key ID, a token caller by its subject mapping), a metric and a value;
there is no foreign key to the subject. (2) A request is reserved against one limit per subject
and metric that has a rule; for the tenant subject the value is the smaller of the platform
ceiling and the tenant's rule. Every one must admit: a rule can only remove room. (3) No rule
means no limit, and nothing is seeded. (4) The gateway reads a tenant's rules in that tenant's
session and caches them per tenant, with a lifetime and the notifications `limits` and
`limits:<tenant>`; when they cannot be read and none are cached, the request is refused as
unavailable. A changed rule applies to the counts already held. (5) Rules are written through the
administration API of sprint 5.2; this record fixes their storage and meaning only.
*Consequences*: no validation between levels exists to be wrong; a rule above its parent is
valid and idle; a rule for a subject that does not exist is idle; a platform ceiling counts each
tenant on its own, and an installation-wide sum is not expressible.
*Alternatives considered*: limits as columns of the key (does not cover tokens, tenants or the
platform); one table with a level column (a tenant-scoped row that only the platform may write
needs a policy on the actor, not on the scope); rejecting a rule that exceeds its parent (a later
change of the parent would invalidate rows); rules in the configuration file (ADR 0019 puts
limits with the state managed through the API).

**History lines**: 0012 ("2026-11: sprint 5.1 fixes the pipeline step, the estimate, the window
and the bound, which names input and output (ADR 0048), and the rules (ADR 0049); quotas, budgets
and the behaviour of a failing store follow in 5.2"); 0020 ("sprint 5.1 puts `rate_limited` into
use, for requests, tokens and concurrency, with the limit headers of ADR 0048; no new code");
0037 ("sprint 5.1 adds the reservation between the model check and the credential and the
settlement in the handler's finish"); 0039 ("sprint 5.1 writes outcome `rejected_limit`; limits
settle with the record's figures, so the stream estimate of this record is confirmed unchanged").

### E4. Planning files

`planning/roadmap.md`: sprint 5.1 `done`. `planning/epics/05-limits-and-budgets/epic.md`:
`status: in-progress`; under 5.2 add "the `budget` field of the capability check (carried from
epic 3)", "a default output reservation per model, if confirmed (5.1 O-5)", "Python and
TypeScript SDK tests of a Pandokos 429", and, if O-1 is confirmed, keep "Administration API and
CLI for limits" with "permissions and audit records". This sprint's `status: done` at close, with
the maintainer's decisions on §12 in a table as earlier sprints have.

**Verification:** `go test ./test/repo/...` (links, the ADR index, the configuration table
against the code, T-R41); `make lint`.

---

## Part F — Verification (no commit unless something is found)

Run on the branch with everything merged, and put the output in the pull request.

```
make db-up
make check
make image && make test-container && make test-compose
go test -race -count=20 ./internal/limits/
for s in 11 222 3333; do PANDOKOS_TEST_SEED=$s go test -race -count=1 ./internal/limits/ -run TestProperty; done
go test -race -count=10 ./internal/gateway/ -run 'TestParallelLoad|TestEveryEndSettles|TestConcurrencyLimit'
make bench
git diff --stat main -- api/openapi internal/authz internal/admin internal/cli      # must be empty
```

Checklist for the pull request, each with its evidence:

1. The three exit conditions of §10 with the names of the tests and the lines of their output
   (the counts of T-640 a, the sum against the bound of T-640 b, the zeroes of T-640 c).
2. The coverage table of §9.
3. The failing output of part B.
4. The benchmark figures of T-622, as information.
5. The definition of done of §10, ticked.
6. The open points of §12 with the maintainer's answers, or a note that they are pending.

A defect found here is fixed in a commit of its own with a test that fails without the fix.

---

## What the implementer does not do

- Add a route, a permission, a CLI command, an audit action or an OpenAPI change for limits.
- Add a metric besides the three, a period counter, a cost limit, or the code `quota_exceeded`.
- Change `checks.budget` of the capability check.
- Change how an estimated usage record is computed, or add a tokenizer.
- Seed a rule in the migration, or read rules from the configuration.
- Add a switch that turns enforcement off, or one that admits when the rules cannot be loaded.
- Persist counters, read the ledger for them, or add a Valkey client.
- Lengthen a bound, pin a seed or skip a test to make the suite pass.
- Change the texts of appendices A1 and A2 without asking.
