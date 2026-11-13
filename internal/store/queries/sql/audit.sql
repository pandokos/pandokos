-- Audit. Insert only: there is no statement that reads, changes or removes an audit row.
-- detail is passed as JSON text and cast: the driver sends text parameters (ADR 0027).

-- name: InsertAuditEvent :exec
INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_id, actor_name, key_id, action, resource_type,
                                  resource_id, outcome, request_id, reason, detail)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(actor_type)::text, sqlc.arg(actor_id)::text, sqlc.arg(actor_name)::text,
        sqlc.arg(key_id)::text, sqlc.arg(action)::text, sqlc.arg(resource_type)::text, sqlc.arg(resource_id)::text,
        sqlc.arg(outcome)::text, sqlc.arg(request_id)::text, sqlc.arg(reason)::text, sqlc.arg(detail)::text::jsonb);

-- name: InsertPlatformAuditEvent :exec
INSERT INTO pandokos.platform_audit_event (actor_type, actor_id, actor_name, key_id, action, resource_type,
                                           resource_id, outcome, request_id, reason, detail)
VALUES (sqlc.arg(actor_type)::text, sqlc.arg(actor_id)::text, sqlc.arg(actor_name)::text,
        sqlc.arg(key_id)::text, sqlc.arg(action)::text, sqlc.arg(resource_type)::text, sqlc.arg(resource_id)::text,
        sqlc.arg(outcome)::text, sqlc.arg(request_id)::text, sqlc.arg(reason)::text, sqlc.arg(detail)::text::jsonb);
