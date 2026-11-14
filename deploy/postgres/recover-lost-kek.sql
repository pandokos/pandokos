-- Recovery after the loss of a key-encryption key (docs/operations/vault.md). Run as a
-- superuser, in the Pandokos database, with the lost version:
--   psql -v kek_version=local-0123456789abcdef -f recover-lost-kek.sql
-- Credentials sealed under data keys of that version can never be opened again: they are
-- revoked, the data keys and the version are removed, and tenants register credentials again.
BEGIN;
UPDATE pandokos.provider_credential c
   SET state = 'revoked', state_changed_by = 'platform', state_reason = 'key-encryption key lost'
 WHERE c.state <> 'revoked'
   AND EXISTS (SELECT 1 FROM pandokos.tenant_data_key d
                WHERE d.tenant_id = c.tenant_id AND d.version = c.key_version
                  AND d.kek_version = :'kek_version');
DELETE FROM pandokos.tenant_data_key WHERE kek_version = :'kek_version';
DELETE FROM pandokos.kek_version WHERE id = :'kek_version';
COMMIT;
