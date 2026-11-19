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
