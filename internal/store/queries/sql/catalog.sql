-- The catalogue: providers, endpoints, models, grants of models to tenants, the model restriction
-- of a tenant. Statements that write providers, endpoints, models and grants run in platform
-- scope (store.DB.Platform); the Load statements and the reads of providers and models run in
-- any session; ListTenantGrants, ListVisibleModels and the restriction statements run in a
-- tenant session.

-- name: ListProviders :many
SELECT p.id, p.name, p.display_name, p.description, p.api_family, p.enabled, p.dataset_provider, p.created_at, p.updated_at,
       p.stream_usage_option
  FROM pandokos.provider p
 WHERE (NOT sqlc.arg(after)::boolean OR p.name > sqlc.arg(after_name)::text)
 ORDER BY p.name
 LIMIT sqlc.arg(page_size)::integer;

-- name: GetProvider :one
SELECT p.id, p.name, p.display_name, p.description, p.api_family, p.enabled, p.dataset_provider, p.created_at, p.updated_at,
       p.stream_usage_option
  FROM pandokos.provider p
 WHERE p.name = sqlc.arg(name)::text;

-- Writes of the endpoints of a provider lock its row: they move the default endpoint and check
-- what is left, and two of them at once would collide on provider_endpoint_default_key.
-- name: GetProviderForUpdate :one
SELECT p.id, p.name, p.display_name, p.description, p.api_family, p.enabled, p.dataset_provider, p.created_at, p.updated_at,
       p.stream_usage_option
  FROM pandokos.provider p
 WHERE p.name = sqlc.arg(name)::text
   FOR UPDATE;

-- name: CreateProvider :one
INSERT INTO pandokos.provider (name, display_name, description, api_family, enabled, dataset_provider, stream_usage_option)
VALUES (sqlc.arg(name)::text, sqlc.arg(display_name)::text, sqlc.arg(description)::text, sqlc.arg(api_family)::text,
        sqlc.arg(enabled)::boolean, sqlc.arg(dataset_provider)::text, sqlc.arg(stream_usage_option)::text)
RETURNING id, name, display_name, description, api_family, enabled, dataset_provider, created_at, updated_at, stream_usage_option;

-- name: UpdateProvider :one
UPDATE pandokos.provider
   SET display_name     = CASE WHEN sqlc.arg(set_display_name)::boolean THEN sqlc.arg(display_name)::text ELSE display_name END,
       description      = CASE WHEN sqlc.arg(set_description)::boolean THEN sqlc.arg(description)::text ELSE description END,
       enabled          = CASE WHEN sqlc.arg(set_enabled)::boolean THEN sqlc.arg(enabled)::boolean ELSE enabled END,
       dataset_provider = CASE WHEN sqlc.arg(set_dataset_provider)::boolean THEN sqlc.arg(dataset_provider)::text ELSE dataset_provider END,
       stream_usage_option = CASE WHEN sqlc.arg(set_stream_usage_option)::boolean THEN sqlc.arg(stream_usage_option)::text ELSE stream_usage_option END
 WHERE name = sqlc.arg(name)::text
RETURNING id, name, display_name, description, api_family, enabled, dataset_provider, created_at, updated_at, stream_usage_option;

-- name: DeleteProvider :one
DELETE FROM pandokos.provider
 WHERE name = sqlc.arg(name)::text
RETURNING id, name, display_name, description, api_family, enabled, dataset_provider, created_at, updated_at, stream_usage_option;

-- name: ListEndpoints :many
SELECT e.id, e.provider_id, e.name, e.base_url, e.region, e.private, e.enabled, e.is_default, e.description, e.created_at, e.updated_at
  FROM pandokos.provider_endpoint e
 WHERE e.provider_id = sqlc.arg(provider_id)::uuid
   AND (NOT sqlc.arg(after)::boolean OR e.name > sqlc.arg(after_name)::text)
 ORDER BY e.name
 LIMIT sqlc.arg(page_size)::integer;

-- name: GetEndpoint :one
SELECT e.id, e.provider_id, e.name, e.base_url, e.region, e.private, e.enabled, e.is_default, e.description, e.created_at, e.updated_at
  FROM pandokos.provider_endpoint e
 WHERE e.provider_id = sqlc.arg(provider_id)::uuid AND e.name = sqlc.arg(name)::text;

-- name: CreateEndpoint :one
INSERT INTO pandokos.provider_endpoint (provider_id, name, base_url, region, private, enabled, is_default, description)
VALUES (sqlc.arg(provider_id)::uuid, sqlc.arg(name)::text, sqlc.arg(base_url)::text, sqlc.arg(region)::text,
        sqlc.arg(private)::boolean, sqlc.arg(enabled)::boolean,
        sqlc.arg(is_default)::boolean OR NOT EXISTS (SELECT 1 FROM pandokos.provider_endpoint d
                                                     WHERE d.provider_id = sqlc.arg(provider_id)::uuid AND d.is_default),
        sqlc.arg(description)::text)
RETURNING id, provider_id, name, base_url, region, private, enabled, is_default, description, created_at, updated_at;

-- name: ClearDefaultEndpoint :execrows
UPDATE pandokos.provider_endpoint
   SET is_default = false
 WHERE provider_id = sqlc.arg(provider_id)::uuid AND is_default AND name <> sqlc.arg(keep)::text;

-- name: UpdateEndpoint :one
UPDATE pandokos.provider_endpoint
   SET base_url    = CASE WHEN sqlc.arg(set_base_url)::boolean THEN sqlc.arg(base_url)::text ELSE base_url END,
       region      = CASE WHEN sqlc.arg(set_region)::boolean THEN sqlc.arg(region)::text ELSE region END,
       private     = CASE WHEN sqlc.arg(set_private)::boolean THEN sqlc.arg(private)::boolean ELSE private END,
       enabled     = CASE WHEN sqlc.arg(set_enabled)::boolean THEN sqlc.arg(enabled)::boolean ELSE enabled END,
       is_default  = CASE WHEN sqlc.arg(set_is_default)::boolean THEN sqlc.arg(is_default)::boolean ELSE is_default END,
       description = CASE WHEN sqlc.arg(set_description)::boolean THEN sqlc.arg(description)::text ELSE description END
 WHERE provider_id = sqlc.arg(provider_id)::uuid AND name = sqlc.arg(name)::text
RETURNING id, provider_id, name, base_url, region, private, enabled, is_default, description, created_at, updated_at;

-- name: DeleteEndpoint :one
DELETE FROM pandokos.provider_endpoint
 WHERE provider_id = sqlc.arg(provider_id)::uuid AND name = sqlc.arg(name)::text
RETURNING id, provider_id, name, base_url, region, private, enabled, is_default, description, created_at, updated_at;

-- name: ListModels :many
SELECT m.id, p.name AS provider, p.api_family, m.name, m.upstream_id, m.display_name, m.description, m.status,
       m.availability, m.capabilities, m.context_window, m.max_output_tokens, m.price_override, m.created_at, m.updated_at
  FROM pandokos.model m
  JOIN pandokos.provider p ON p.id = m.provider_id
 WHERE (sqlc.arg(provider)::text = '' OR p.name = sqlc.arg(provider)::text)
   AND (sqlc.arg(name)::text = '' OR m.name = sqlc.arg(name)::text)
   AND (sqlc.arg(status)::text = '' OR m.status = sqlc.arg(status)::text)
   AND (sqlc.arg(availability)::text = '' OR m.availability = sqlc.arg(availability)::text)
   AND (NOT sqlc.arg(after)::boolean OR CASE sqlc.arg(sort)::text
            WHEN 'created_at'  THEN m.id > sqlc.arg(after_id)::uuid
            WHEN '-created_at' THEN m.id < sqlc.arg(after_id)::uuid
            ELSE false END)
 ORDER BY CASE WHEN sqlc.arg(sort)::text = 'created_at'  THEN m.id END ASC,
          CASE WHEN sqlc.arg(sort)::text = '-created_at' THEN m.id END DESC
 LIMIT sqlc.arg(page_size)::integer;

-- name: GetModel :one
SELECT m.id, p.name AS provider, p.api_family, m.name, m.upstream_id, m.display_name, m.description, m.status,
       m.availability, m.capabilities, m.context_window, m.max_output_tokens, m.price_override, m.created_at, m.updated_at
  FROM pandokos.model m
  JOIN pandokos.provider p ON p.id = m.provider_id
 WHERE m.id = sqlc.arg(id)::uuid;

-- name: LockModel :one
SELECT m.id, m.name, m.price_override
  FROM pandokos.model m
 WHERE m.id = sqlc.arg(id)::uuid
   FOR UPDATE;

-- name: CreateModel :one
INSERT INTO pandokos.model (provider_id, name, upstream_id, display_name, description, status, availability, capabilities,
                            context_window, max_output_tokens)
VALUES (sqlc.arg(provider_id)::uuid, sqlc.arg(name)::text, sqlc.arg(upstream_id)::text, sqlc.arg(display_name)::text,
        sqlc.arg(description)::text, sqlc.arg(status)::text, sqlc.arg(availability)::text, sqlc.arg(capabilities)::text[],
        sqlc.narg(context_window)::bigint, sqlc.narg(max_output_tokens)::bigint)
RETURNING id;

-- name: UpdateModel :execrows
UPDATE pandokos.model
   SET upstream_id       = CASE WHEN sqlc.arg(set_upstream_id)::boolean THEN sqlc.arg(upstream_id)::text ELSE upstream_id END,
       display_name      = CASE WHEN sqlc.arg(set_display_name)::boolean THEN sqlc.arg(display_name)::text ELSE display_name END,
       description       = CASE WHEN sqlc.arg(set_description)::boolean THEN sqlc.arg(description)::text ELSE description END,
       status            = CASE WHEN sqlc.arg(set_status)::boolean THEN sqlc.arg(status)::text ELSE status END,
       availability      = CASE WHEN sqlc.arg(set_availability)::boolean THEN sqlc.arg(availability)::text ELSE availability END,
       capabilities      = CASE WHEN sqlc.arg(set_capabilities)::boolean THEN sqlc.arg(capabilities)::text[] ELSE capabilities END,
       context_window    = CASE WHEN sqlc.arg(set_context_window)::boolean THEN sqlc.narg(context_window)::bigint ELSE context_window END,
       max_output_tokens = CASE WHEN sqlc.arg(set_max_output_tokens)::boolean THEN sqlc.narg(max_output_tokens)::bigint ELSE max_output_tokens END
 WHERE id = sqlc.arg(id)::uuid;

-- name: SetModelPriceOverride :execrows
UPDATE pandokos.model
   SET price_override = sqlc.arg(price_override)::boolean
 WHERE id = sqlc.arg(id)::uuid;

-- name: DeleteModel :execrows
DELETE FROM pandokos.model
 WHERE id = sqlc.arg(id)::uuid;

-- name: ListModelGrants :many
SELECT g.model_id, g.tenant_id, t.name AS tenant_name, g.created_at
  FROM pandokos.model_tenant_grant g
  JOIN pandokos.tenant t ON t.id = g.tenant_id
 WHERE g.model_id = sqlc.arg(model_id)::uuid
   AND (NOT sqlc.arg(after)::boolean OR g.tenant_id > sqlc.arg(after_id)::uuid)
 ORDER BY g.tenant_id
 LIMIT sqlc.arg(page_size)::integer;

-- name: GrantModel :execrows
INSERT INTO pandokos.model_tenant_grant (model_id, tenant_id)
VALUES (sqlc.arg(model_id)::uuid, sqlc.arg(tenant_id)::uuid)
ON CONFLICT (model_id, tenant_id) DO NOTHING;

-- name: GetModelGrant :one
SELECT g.model_id, g.tenant_id, t.name AS tenant_name, g.created_at
  FROM pandokos.model_tenant_grant g
  JOIN pandokos.tenant t ON t.id = g.tenant_id
 WHERE g.model_id = sqlc.arg(model_id)::uuid AND g.tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: RevokeModelGrant :execrows
DELETE FROM pandokos.model_tenant_grant
 WHERE model_id = sqlc.arg(model_id)::uuid AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ListTenantGrants :many
SELECT g.model_id
  FROM pandokos.model_tenant_grant g
 ORDER BY g.model_id;

-- name: ListVisibleModels :many
SELECT m.id, p.name AS provider, p.api_family, m.name, m.upstream_id, m.display_name, m.description, m.status,
       m.availability, m.capabilities, m.context_window, m.max_output_tokens, m.price_override, m.created_at, m.updated_at
  FROM pandokos.model m
  JOIN pandokos.provider p ON p.id = m.provider_id
 WHERE p.enabled
   AND m.status <> 'retired'
   AND (m.availability = 'all' OR EXISTS (SELECT 1 FROM pandokos.model_tenant_grant g WHERE g.model_id = m.id))
   AND (sqlc.arg(provider)::text = '' OR p.name = sqlc.arg(provider)::text)
   AND (NOT sqlc.arg(after)::boolean OR m.id > sqlc.arg(after_id)::uuid)
 ORDER BY m.id
 LIMIT sqlc.arg(page_size)::integer;

-- name: GetModelRestriction :one
SELECT r.models, r.updated_at
  FROM pandokos.tenant_model_restriction r;

-- name: SetModelRestriction :one
INSERT INTO pandokos.tenant_model_restriction (tenant_id, models)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(models)::text[])
ON CONFLICT (tenant_id) DO UPDATE SET models = EXCLUDED.models
RETURNING models, updated_at;

-- name: ClearModelRestriction :execrows
DELETE FROM pandokos.tenant_model_restriction;

-- name: LoadProviders :many
SELECT p.id, p.name, p.api_family, p.enabled, p.dataset_provider, p.stream_usage_option
  FROM pandokos.provider p
 ORDER BY p.name;

-- name: LoadEndpoints :many
SELECT e.provider_id, e.name, e.base_url, e.region, e.private, e.enabled, e.is_default
  FROM pandokos.provider_endpoint e
 ORDER BY e.provider_id, e.name;

-- name: LoadModels :many
SELECT m.id, m.provider_id, m.name, m.upstream_id, m.status, m.availability, m.context_window, m.max_output_tokens,
       m.price_override
  FROM pandokos.model m
 ORDER BY m.id;
