-- Tenant deletion and its erasure (ADR 0067). The header of each statement says which scope it needs.

-- Scope: admin, platform scope
-- name: InsertTenantDeletion :one
INSERT INTO pandokos.tenant_deletion (tenant_id, reason, requested_by_type, requested_by_id, requested_by_name, request_id, not_before)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(reason)::text, sqlc.arg(requested_by_type)::text, sqlc.arg(requested_by_id)::text,
        sqlc.arg(requested_by_name)::text, sqlc.arg(request_id)::text, now() + sqlc.arg(grace)::interval)
RETURNING tenant_id, state, step, reason, requested_by_type, requested_by_id, requested_by_name, request_id, requested_at,
          not_before, started_at, completed_at, passes, kek_versions, removed, audit, completion_seq, external_reference;

-- Scope: admin, platform scope
-- name: GetTenantDeletion :one
SELECT d.tenant_id, d.state, d.step, d.reason, d.requested_by_type, d.requested_by_id, d.requested_by_name, d.request_id,
       d.requested_at, d.not_before, d.started_at, d.completed_at, d.passes, d.kek_versions, d.removed, d.audit, d.completion_seq, d.external_reference,
       ARRAY(SELECT v.id FROM pandokos.kek_version v WHERE v.id = ANY (d.kek_versions) AND v.state <> 'retired' ORDER BY v.id)::text[] AS pending_kek_versions
  FROM pandokos.tenant_deletion d
 WHERE d.tenant_id = sqlc.arg(tenant_id)::uuid;

-- Scope: admin, platform scope; newest request first
-- name: ListTenantDeletions :many
SELECT d.tenant_id, d.state, d.step, d.reason, d.requested_by_type, d.requested_by_id, d.requested_by_name, d.request_id,
       d.requested_at, d.not_before, d.started_at, d.completed_at, d.passes, d.kek_versions, d.removed, d.audit, d.completion_seq, d.external_reference,
       ARRAY(SELECT v.id FROM pandokos.kek_version v WHERE v.id = ANY (d.kek_versions) AND v.state <> 'retired' ORDER BY v.id)::text[] AS pending_kek_versions
  FROM pandokos.tenant_deletion d
 WHERE (sqlc.arg(state)::text = '' OR d.state = sqlc.arg(state)::text)
   AND (NOT sqlc.arg(after)::boolean OR (d.requested_at, d.tenant_id) < (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::uuid))
 ORDER BY d.requested_at DESC, d.tenant_id DESC LIMIT sqlc.arg(page_size)::integer;

-- Scope: admin, platform scope
-- name: DeleteScheduledTenantDeletion :execrows
DELETE FROM pandokos.tenant_deletion WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND state = 'scheduled';

-- Scope: worker, platform scope
-- name: ListOpenTenantDeletions :many
SELECT d.tenant_id, d.request_id, d.started_at FROM pandokos.tenant_deletion d
 WHERE d.state = 'running' OR (d.state = 'scheduled' AND d.not_before <= now())
 ORDER BY d.not_before, d.tenant_id;

-- Scope: worker, platform scope
-- name: CountTenantDeletions :many
SELECT d.state, count(*)::bigint AS n FROM pandokos.tenant_deletion d WHERE d.state <> 'completed' GROUP BY d.state;

-- Scope: worker, platform scope
-- name: CountPendingErasures :one
SELECT count(*)::bigint FROM pandokos.tenant_deletion d
 WHERE d.state = 'completed'
   AND EXISTS (SELECT 1 FROM pandokos.kek_version v WHERE v.id = ANY (d.kek_versions) AND v.state <> 'retired');

-- Scope: worker, session of the tenant
-- name: TenantErasureStep :one
SELECT pandokos.tenant_erasure_step(sqlc.arg(batch)::integer)::text AS next;

-- Scope: worker, platform scope
-- name: ListDeletedTenantsForWorker :many
SELECT t.id FROM pandokos.tenant t WHERE t.state = 'deleted' ORDER BY t.id;

-- Scope: worker, session of a deleted tenant
-- name: DeletedTenantRetention :one
SELECT r.usage_rows, r.audit_rows
  FROM pandokos.deleted_tenant_retention(sqlc.arg(usage_months)::integer, sqlc.arg(audit_months)::integer) r;
