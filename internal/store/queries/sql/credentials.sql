-- Provider credentials. Every statement runs in a tenant session. The statements of the admin
-- role read pandokos.provider_credential_metadata or name metadata columns only: that role
-- cannot read the sealed secret. The two ForUse statements are the gateway's.

-- name: NewCredentialID :one
SELECT pandokos.uuid_v7()::uuid AS id;

-- name: CreateCredential :one
INSERT INTO pandokos.provider_credential (tenant_id, id, provider, label, description, is_default, key_version,
                                          secret, fingerprint, hint, expires_at, validated_at)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(id)::uuid, sqlc.arg(provider)::text, sqlc.arg(label)::text,
        sqlc.arg(description)::text,
        NOT EXISTS (SELECT 1 FROM pandokos.provider_credential_metadata m
                     WHERE m.provider = sqlc.arg(provider)::text AND m.is_default),
        sqlc.arg(key_version)::integer, sqlc.arg(secret)::bytea, sqlc.arg(fingerprint)::text, sqlc.arg(hint)::text,
        sqlc.narg(expires_at)::timestamptz,
        CASE WHEN sqlc.arg(validated)::boolean THEN now() END)
RETURNING tenant_id, id, provider, label, description, state, is_default, key_version, fingerprint, hint, expires_at,
          validated_at, rotated_at, state_changed_at, state_changed_by, state_reason, created_at, updated_at;

-- name: GetCredential :one
SELECT m.tenant_id, m.id, m.provider, m.label, m.description, m.state, m.is_default, m.key_version, m.fingerprint,
       m.hint, m.expires_at, m.validated_at, m.rotated_at, m.state_changed_at, m.state_changed_by, m.state_reason,
       m.created_at, m.updated_at
  FROM pandokos.provider_credential_metadata m
 WHERE m.id = sqlc.arg(id)::uuid;

-- name: ListCredentials :many
SELECT m.tenant_id, m.id, m.provider, m.label, m.description, m.state, m.is_default, m.key_version, m.fingerprint,
       m.hint, m.expires_at, m.validated_at, m.rotated_at, m.state_changed_at, m.state_changed_by, m.state_reason,
       m.created_at, m.updated_at
  FROM pandokos.provider_credential_metadata m
 WHERE (sqlc.arg(state)::text = '' OR m.state = sqlc.arg(state)::text)
   AND (sqlc.arg(provider)::text = '' OR m.provider = sqlc.arg(provider)::text)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN m.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN m.id < sqlc.arg(after_id)::uuid
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN m.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN m.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: LockCredential :one
SELECT c.id, c.provider, c.label, c.state, c.is_default, c.fingerprint, c.hint
  FROM pandokos.provider_credential c
 WHERE c.id = sqlc.arg(id)::uuid
   FOR UPDATE;

-- name: UpdateCredential :one
UPDATE pandokos.provider_credential
   SET label       = CASE WHEN sqlc.arg(set_label)::boolean THEN sqlc.arg(label)::text ELSE label END,
       description = CASE WHEN sqlc.arg(set_description)::boolean THEN sqlc.arg(description)::text ELSE description END,
       expires_at  = CASE WHEN sqlc.arg(set_expires_at)::boolean THEN sqlc.narg(expires_at)::timestamptz ELSE expires_at END
 WHERE id = sqlc.arg(id)::uuid
RETURNING tenant_id, id, provider, label, description, state, is_default, key_version, fingerprint, hint, expires_at,
          validated_at, rotated_at, state_changed_at, state_changed_by, state_reason, created_at, updated_at;

-- name: ReplaceCredentialSecret :one
UPDATE pandokos.provider_credential
   SET key_version  = sqlc.arg(key_version)::integer,
       secret       = sqlc.arg(secret)::bytea,
       fingerprint  = sqlc.arg(fingerprint)::text,
       hint         = sqlc.arg(hint)::text,
       expires_at   = sqlc.narg(expires_at)::timestamptz,
       validated_at = CASE WHEN sqlc.arg(validated)::boolean THEN now() END
 WHERE id = sqlc.arg(id)::uuid
   AND state <> 'revoked'
RETURNING tenant_id, id, provider, label, description, state, is_default, key_version, fingerprint, hint, expires_at,
          validated_at, rotated_at, state_changed_at, state_changed_by, state_reason, created_at, updated_at;

-- name: SetCredentialState :one
UPDATE pandokos.provider_credential
   SET state            = sqlc.arg(state)::text,
       state_changed_by = sqlc.arg(changed_by)::text,
       state_reason     = sqlc.arg(reason)::text
 WHERE id = sqlc.arg(id)::uuid
RETURNING tenant_id, id, provider, label, description, state, is_default, key_version, fingerprint, hint, expires_at,
          validated_at, rotated_at, state_changed_at, state_changed_by, state_reason, created_at, updated_at;

-- name: ClearDefaultCredential :one
UPDATE pandokos.provider_credential
   SET is_default = false
 WHERE provider = sqlc.arg(provider)::text
   AND is_default
RETURNING id;

-- name: SetDefaultCredential :one
UPDATE pandokos.provider_credential
   SET is_default = true
 WHERE id = sqlc.arg(id)::uuid
RETURNING tenant_id, id, provider, label, description, state, is_default, key_version, fingerprint, hint, expires_at,
          validated_at, rotated_at, state_changed_at, state_changed_by, state_reason, created_at, updated_at;

-- name: CountOtherCredentials :one
SELECT count(*)::bigint AS n
  FROM pandokos.provider_credential_metadata m
 WHERE m.provider = sqlc.arg(provider)::text
   AND m.id <> sqlc.arg(id)::uuid
   AND m.state <> 'revoked';

-- name: CountUsablePins :one
SELECT count(*)::bigint AS n
  FROM pandokos.virtual_key k
 WHERE k.credential_id = sqlc.arg(credential_id)::uuid
   AND k.revoked_at IS NULL
   AND (k.expires_at IS NULL OR k.expires_at > now());

-- name: DeleteCredential :execrows
DELETE FROM pandokos.provider_credential
 WHERE id = sqlc.arg(id)::uuid;

-- name: GetCredentialForUse :one
SELECT c.id, c.provider, c.label, c.state, c.key_version, c.secret, c.fingerprint
  FROM pandokos.provider_credential c
 WHERE c.id = sqlc.arg(id)::uuid;

-- name: GetDefaultCredentialForUse :one
SELECT c.id, c.provider, c.label, c.state, c.key_version, c.secret, c.fingerprint
  FROM pandokos.provider_credential c
 WHERE c.provider = sqlc.arg(provider)::text
   AND c.is_default;
