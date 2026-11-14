-- 0007_provider_stream_usage: whether Pandokos adds the stream usage option to chat completion
-- streams sent to a provider of the openai family (ADR 0042). Runs in one transaction as
-- pandokos_owner. Every name is schema-qualified.

ALTER TABLE pandokos.provider
    ADD COLUMN stream_usage_option text NOT NULL DEFAULT 'add',
    ADD CONSTRAINT provider_stream_usage_option_valid CHECK (stream_usage_option IN ('add', 'omit'));

GRANT INSERT (stream_usage_option), UPDATE (stream_usage_option) ON pandokos.provider TO pandokos_admin;
