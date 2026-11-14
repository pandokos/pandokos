-- Prices: imports, versions and their rates. Writes run in platform scope (admin or worker
-- role); the reads of versions run in any session; the reads of imports in platform scope. A
-- version's rates come back as three parallel arrays ordered by category and threshold; rates
-- travel as decimal text, never as floating point.

-- name: LockPriceImports :exec
SELECT pg_catalog.pg_advisory_xact_lock(7203391045122417921);

-- last_remote is the epoch when no remote import succeeded.
-- name: PriceImportHistory :one
SELECT EXISTS (SELECT 1 FROM pandokos.price_import i
                WHERE i.digest = sqlc.arg(digest)::text AND i.outcome = 'success')::boolean AS digest_seen,
       EXISTS (SELECT 1 FROM pandokos.price_import i
                WHERE i.source IN ('file', 'remote') AND i.outcome = 'success'
                  AND i.created_at > sqlc.arg(since)::timestamptz)::boolean AS newer_import,
       COALESCE((SELECT max(i.created_at) FROM pandokos.price_import i
                  WHERE i.source = 'remote' AND i.outcome = 'success'), 'epoch'::timestamptz)::timestamptz AS last_remote;

-- name: CreatePriceImport :one
INSERT INTO pandokos.price_import (started_at, source, source_ref, dataset_format, digest, outcome, error_class, actor_type,
                                   actor_id, models_seen, versions_created, unchanged, overridden, unmatched, skipped, report)
VALUES (sqlc.arg(started_at)::timestamptz, sqlc.arg(source)::text, sqlc.arg(source_ref)::text, sqlc.arg(dataset_format)::text,
        sqlc.arg(digest)::text, sqlc.arg(outcome)::text, sqlc.arg(error_class)::text, sqlc.arg(actor_type)::text,
        sqlc.arg(actor_id)::text, sqlc.arg(models_seen)::integer, sqlc.arg(versions_created)::integer,
        sqlc.arg(unchanged)::integer, sqlc.arg(overridden)::integer, sqlc.arg(unmatched)::integer,
        sqlc.arg(skipped)::integer, sqlc.arg(report)::jsonb)
RETURNING id, created_at;

-- name: ListPriceImports :many
SELECT i.id, i.started_at, i.created_at, i.source, i.source_ref, i.dataset_format, i.digest, i.outcome, i.error_class,
       i.actor_type, i.actor_id, i.models_seen, i.versions_created, i.unchanged, i.overridden, i.unmatched, i.skipped
  FROM pandokos.price_import i
 WHERE (NOT sqlc.arg(after)::boolean OR i.id < sqlc.arg(after_id)::uuid)
 ORDER BY i.id DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: GetPriceImport :one
SELECT i.id, i.started_at, i.created_at, i.source, i.source_ref, i.dataset_format, i.digest, i.outcome, i.error_class,
       i.actor_type, i.actor_id, i.models_seen, i.versions_created, i.unchanged, i.overridden, i.unmatched, i.skipped,
       i.report
  FROM pandokos.price_import i
 WHERE i.id = sqlc.arg(id)::uuid;

-- name: CreatePriceVersion :one
INSERT INTO pandokos.price_version (model_id, valid_from, currency, source, import_id, note)
VALUES (sqlc.arg(model_id)::uuid, COALESCE(sqlc.narg(valid_from)::timestamptz, now()), sqlc.arg(currency)::text,
        sqlc.arg(source)::text, sqlc.narg(import_id)::uuid, sqlc.arg(note)::text)
RETURNING id, valid_from, created_at;

-- name: CreatePriceRates :execrows
INSERT INTO pandokos.price_rate (price_version_id, category, threshold, rate)
SELECT sqlc.arg(price_version_id)::uuid, (sqlc.arg(categories)::text[])[i.n], (sqlc.arg(thresholds)::bigint[])[i.n],
       ((sqlc.arg(rates)::text[])[i.n])::numeric
  FROM generate_series(1, cardinality(sqlc.arg(categories)::text[])) AS i (n);

-- name: PriceAt :one
SELECT v.id, v.model_id, v.valid_from, v.currency, v.source, v.import_id, v.note, v.created_at,
       COALESCE(array_agg(r.category ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS categories,
       COALESCE(array_agg(r.threshold ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::bigint[] AS thresholds,
       COALESCE(array_agg(r.rate::text ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS rates
  FROM pandokos.price_version v
  LEFT JOIN pandokos.price_rate r ON r.price_version_id = v.id
 WHERE v.id = (SELECT w.id FROM pandokos.price_version w
                WHERE w.model_id = sqlc.arg(model_id)::uuid
                  AND w.valid_from <= COALESCE(sqlc.narg(at)::timestamptz, now())
                ORDER BY w.valid_from DESC
                LIMIT 1)
 GROUP BY v.id;

-- name: GetPriceVersion :one
SELECT v.id, v.model_id, v.valid_from, v.currency, v.source, v.import_id, v.note, v.created_at,
       COALESCE(array_agg(r.category ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS categories,
       COALESCE(array_agg(r.threshold ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::bigint[] AS thresholds,
       COALESCE(array_agg(r.rate::text ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS rates
  FROM pandokos.price_version v
  LEFT JOIN pandokos.price_rate r ON r.price_version_id = v.id
 WHERE v.id = sqlc.arg(id)::uuid AND v.model_id = sqlc.arg(model_id)::uuid
 GROUP BY v.id;

-- name: ListPriceVersions :many
SELECT v.id, v.model_id, v.valid_from, v.currency, v.source, v.import_id, v.note, v.created_at,
       COALESCE(array_agg(r.category ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS categories,
       COALESCE(array_agg(r.threshold ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::bigint[] AS thresholds,
       COALESCE(array_agg(r.rate::text ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS rates
  FROM pandokos.price_version v
  LEFT JOIN pandokos.price_rate r ON r.price_version_id = v.id
 WHERE v.model_id = sqlc.arg(model_id)::uuid
   AND (NOT sqlc.arg(after)::boolean OR v.valid_from < sqlc.arg(after_valid_from)::timestamptz)
 GROUP BY v.id
 ORDER BY v.valid_from DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: HasFuturePriceVersion :one
SELECT EXISTS (SELECT 1 FROM pandokos.price_version v
                WHERE v.model_id = sqlc.arg(model_id)::uuid AND v.valid_from > now())::boolean AS future;

-- name: DeletePriceVersion :execrows
DELETE FROM pandokos.price_version
 WHERE id = sqlc.arg(id)::uuid AND model_id = sqlc.arg(model_id)::uuid;

-- name: LoadPriceVersions :many
SELECT v.id, v.model_id, v.valid_from, v.currency, v.source,
       COALESCE(array_agg(r.category ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS categories,
       COALESCE(array_agg(r.threshold ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::bigint[] AS thresholds,
       COALESCE(array_agg(r.rate::text ORDER BY r.category, r.threshold) FILTER (WHERE r.category IS NOT NULL), '{}')::text[] AS rates
  FROM pandokos.price_version v
  LEFT JOIN pandokos.price_rate r ON r.price_version_id = v.id
 GROUP BY v.id
 ORDER BY v.model_id, v.valid_from;
