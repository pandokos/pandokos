-- Recovery after the loss of a key-encryption key (docs/operations/vault.md). Run as a
-- superuser, in the Pandokos database, with the lost version:
--   psql -v kek_version=local-0123456789abcdef -f recover-lost-kek.sql
-- Credentials sealed under data keys of that version can never be opened again: they are
-- revoked, the data keys and the version are removed, and tenants register credentials again.
-- The signing secret of an event destination is sealed under the same data keys. A destination
-- whose current secret was sealed under a data key of that version is disabled (its URL, its types
-- and its cursor stay, so the events the log still holds are delivered when it is enabled again),
-- and a previous secret sealed under one is dropped; the owner of the destination runs
-- `pandokos event-destination rotate-secret`, gives the receiver the new secret and enables it.
-- It ends by printing the number of credentials revoked, of destinations disabled and of
-- previous secrets dropped. Run for a version that does not exist, or run again, it changes nothing.
-- After it, no version may be active, and the next start of `serve` activates the new key.
BEGIN;
CREATE TEMPORARY TABLE IF NOT EXISTS pg_temp.lost_kek_report (what text PRIMARY KEY, n bigint NOT NULL);
DELETE FROM pg_temp.lost_kek_report;
WITH revoked AS (
    UPDATE pandokos.provider_credential c
       SET state = 'revoked', state_changed_by = 'platform', state_reason = 'key-encryption key lost'
     WHERE c.state <> 'revoked'
       AND EXISTS (SELECT 1 FROM pandokos.tenant_data_key d
                    WHERE d.tenant_id = c.tenant_id AND d.version = c.key_version
                      AND d.kek_version = :'kek_version')
 RETURNING 1)
INSERT INTO pg_temp.lost_kek_report SELECT 'credentials revoked', count(*) FROM revoked;
-- Destinations whose current secret was sealed under a data key of the lost version: disabled.
WITH disabled AS (
    UPDATE pandokos.event_destination e SET enabled = false
     WHERE e.enabled
       AND EXISTS (SELECT 1 FROM pandokos.tenant_data_key d
                    WHERE d.tenant_id = e.tenant_id AND d.version = e.secret_key_version
                      AND d.kek_version = :'kek_version')
 RETURNING 1)
INSERT INTO pg_temp.lost_kek_report SELECT 'destinations disabled', count(*) FROM disabled;
-- A previous secret sealed under such a data key: dropped (the trigger of the table clears the
-- secret when its expiry is null).
WITH dropped AS (
    UPDATE pandokos.event_destination e SET previous_secret_expires_at = NULL
     WHERE e.previous_secret_key_version IS NOT NULL
       AND EXISTS (SELECT 1 FROM pandokos.tenant_data_key d
                    WHERE d.tenant_id = e.tenant_id AND d.version = e.previous_secret_key_version
                      AND d.kek_version = :'kek_version')
 RETURNING 1)
INSERT INTO pg_temp.lost_kek_report SELECT 'previous secrets dropped', count(*) FROM dropped;
DELETE FROM pandokos.tenant_data_key WHERE kek_version = :'kek_version';
DELETE FROM pandokos.kek_version WHERE id = :'kek_version';
COMMIT;
SELECT what, n FROM pg_temp.lost_kek_report ORDER BY what;
