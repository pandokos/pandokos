// Package isolation holds the tenant-isolation harness: a schema lint, the registry of fixtures
// that says how to test each tenant-scoped table, and the negative tests that run unfiltered SQL
// as every runtime role (sprint 1.2, section 6).
package isolation

// Finding is one violation reported by Lint.
type Finding struct {
	Rule   string // "L-07"
	Object string // "pandokos.service_account"
	Detail string
}

// Spec is what the schema is expected to be.
type Spec struct {
	Global          map[string]GlobalTable         // table name -> why it is global
	Grants          map[string]map[string][]string // table -> privilege role -> privileges
	Fixtures        map[string]Fixture             // tenant-scoped table -> how to test it
	SecurityDefiner []string                       // functions allowed to be SECURITY DEFINER
	Views           []string                       // views allowed to exist
	AppendOnly      []string                       // tables whose rows are never changed or removed
}

// GlobalTable explains why a table has no tenant_id.
type GlobalTable struct {
	Reason     string
	Restricted bool // true: must have forced row-level security and at least one policy
}

// Fixture says how the negative tests create and change a row of a tenant-scoped table.
type Fixture struct {
	Insert string // one INSERT with $1 = tenant id that creates one valid row and ends with RETURNING <Key>::text
	Key    string // column that identifies the row; "id" when empty
	Update string // a SET clause that changes one ordinary column, e.g. "description = 'changed'"
}

// DefaultSpec returns the expected schema after the migrations of this repository.
func DefaultSpec() Spec {
	return Spec{
		Global: map[string]GlobalTable{
			"schema_migration":       {Reason: "describes the installation, holds no tenant information"},
			"tenant":                 {Reason: "the list of tenants; protected by its own policies", Restricted: true},
			"virtual_key_directory":  {Reason: "Key ID to tenant, for the lookup function; no runtime role has any privilege on it"},
			"platform_key":           {Reason: "Credentials of the platform; protected by its own policies", Restricted: true},
			"platform_audit_event":   {Reason: "Audit log of the platform; protected by its own policies", Restricted: true},
			"kek_version":            {Reason: "identifiers of key-encryption key versions; describes the installation, holds no key and no tenant information"},
			"provider":               {Reason: "providers of the catalogue; read by every runtime role, written in platform scope", Restricted: true},
			"provider_endpoint":      {Reason: "endpoints of providers; read by the gateway and the admin role, written in platform scope", Restricted: true},
			"model":                  {Reason: "models of the catalogue; read by every runtime role, written in platform scope", Restricted: true},
			"model_tenant_grant":     {Reason: "which tenants a model is granted to; a tenant's session sees its own rows; protected by its own policies", Restricted: true},
			"price_import":           {Reason: "runs of the price importer; platform scope only", Restricted: true},
			"price_version":          {Reason: "price versions; read by every runtime role, inserted in platform scope", Restricted: true},
			"price_rate":             {Reason: "rates of price versions; read by every runtime role, inserted in platform scope", Restricted: true},
			"platform_issuer":        {Reason: "platform-level token issuers; read by the gateway and the admin role, written in platform scope", Restricted: true},
			"platform_issuer_tenant": {Reason: "the tenants a platform-level token issuer may act for; a tenant's session sees its own rows, the platform scope all of them; protected by its own policies", Restricted: true},
			"issuer_directory":       {Reason: "which registration an issuer and audience select, for the lookup function; no runtime role has any privilege on it"},
			"platform_limit_rule":    {Reason: "ceilings the platform sets for tenants; a tenant's session sees the defaults and its own rows, the platform scope all of them; protected by its own policies", Restricted: true},
		},
		Grants: map[string]map[string][]string{
			"schema_migration": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"SELECT"},
				"pandokos_worker":  {"SELECT"},
			},
			"tenant": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"INSERT(display_name,name,time_zone)", "SELECT", "UPDATE(active_currency,active_currency_effective_from,active_currency_previous,display_name,name,state,time_zone,time_zone_effective_from,time_zone_previous)"},
				"pandokos_worker":  {"SELECT"},
			},
			"service_account": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {
					"INSERT(description,name,state,tenant_id,valid_from,valid_until)",
					"SELECT",
					"UPDATE(description,name,state,valid_from,valid_until)",
				},
			},
			"role_assignment": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"DELETE", "INSERT(role,service_account_id,tenant_id)", "SELECT"},
			},
			"kek_version": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"INSERT(id,provider)", "SELECT"},
				"pandokos_worker":  {"SELECT"},
			},
			"tenant_data_key": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"INSERT(kek_version,tenant_id,version,wrapped_key)", "SELECT"},
			},
			"provider_credential": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {
					"DELETE",
					"INSERT(description,expires_at,fingerprint,hint,id,is_default,key_version,label,provider,secret,tenant_id,validated_at)",
					"SELECT(created_at,description,expires_at,fingerprint,hint,id,is_default,key_version,label,provider,rotated_at,state,state_changed_at,state_changed_by,state_reason,tenant_id,updated_at,validated_at)",
					"UPDATE(description,expires_at,fingerprint,hint,is_default,key_version,label,secret,state,state_changed_by,state_reason,validated_at)",
				},
			},
			"provider_credential_metadata": {
				"pandokos_admin": {"SELECT"},
			},
			"virtual_key": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {
					"INSERT(credential_id,description,expires_at,key_id,models,scopes,secret_hash,service_account_id,tenant_id)",
					"SELECT",
					"UPDATE(revoked_at)",
				},
			},
			"platform_key": {
				"pandokos_admin": {
					"INSERT(bootstrap,description,expires_at,key_id,role,secret_hash)",
					"SELECT",
					"UPDATE(revoked_at)",
				},
			},
			"audit_event": {
				"pandokos_admin": {"INSERT(action,actor_id,actor_name,actor_type,detail,key_id,outcome,reason,request_id,resource_id,resource_type,tenant_id)"},
			},
			"platform_audit_event": {
				"pandokos_admin":  {"INSERT(action,actor_id,actor_name,actor_type,detail,key_id,outcome,reason,request_id,resource_id,resource_type)"},
				"pandokos_worker": {"INSERT(action,actor_id,actor_name,actor_type,detail,key_id,outcome,reason,request_id,resource_id,resource_type)"},
			},
			"provider": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {
					"DELETE",
					"INSERT(api_family,dataset_provider,description,display_name,enabled,name,stream_usage_option)",
					"SELECT",
					"UPDATE(dataset_provider,description,display_name,enabled,stream_usage_option)",
				},
				"pandokos_worker": {"SELECT"},
			},
			"provider_endpoint": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {
					"DELETE",
					"INSERT(base_url,description,enabled,is_default,name,private,provider_id,region)",
					"SELECT",
					"UPDATE(base_url,description,enabled,is_default,private,region)",
				},
			},
			"model": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {
					"DELETE",
					"INSERT(availability,capabilities,context_window,default_output_tokens,description,display_name,max_output_tokens,name,provider_id,status,upstream_id)",
					"SELECT",
					"UPDATE(availability,capabilities,context_window,default_output_tokens,description,display_name,max_output_tokens,price_override,status,upstream_id)",
				},
				"pandokos_worker": {"SELECT"},
			},
			"model_tenant_grant": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"DELETE", "INSERT(model_id,tenant_id)", "SELECT"},
			},
			"tenant_model_restriction": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"DELETE", "INSERT(models,tenant_id)", "SELECT", "UPDATE(models)"},
			},
			"price_import": {
				"pandokos_admin":  {"INSERT(actor_id,actor_type,dataset_format,digest,error_class,models_seen,outcome,overridden,report,skipped,source,source_ref,started_at,unchanged,unmatched,versions_created)", "SELECT"},
				"pandokos_worker": {"INSERT(actor_id,actor_type,dataset_format,digest,error_class,models_seen,outcome,overridden,report,skipped,source,source_ref,started_at,unchanged,unmatched,versions_created)", "SELECT"},
			},
			"price_version": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"DELETE", "INSERT(currency,import_id,model_id,note,source,valid_from)", "SELECT"},
				"pandokos_worker":  {"INSERT(currency,import_id,model_id,note,source,valid_from)", "SELECT"},
			},
			"tenant_price_version": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"DELETE", "INSERT(currency,kind,model_id,note,tenant_id,valid_from)", "SELECT"},
			},
			"tenant_price_rate": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"INSERT(category,price_version_id,rate,tenant_id,threshold)", "SELECT"},
			},
			"usage_record": {
				"pandokos_gateway": {"INSERT(api_family,cache_read_tokens,cache_write_tokens,cost,cost_fallbacks,cost_reason,cost_status,credential_id,currency,duration_ms,endpoint,error_code,estimated_input_tokens,estimated_output_tokens,id,input_source,input_tokens,key_id,limit_state,metadata,metadata_dropped,model,model_id,operation,other_units,outcome,output_source,output_tokens,price_source,price_version_id,principal_id,principal_kind,provider,provider_usage,reasoning_tokens,request_bytes,request_id,response_bytes,started_at,stream,tenant_id,trace_id,ttfb_ms,upstream_model,upstream_status)"},
				"pandokos_admin":   {"SELECT"},
				"pandokos_worker":  {"SELECT"},
			},
			"price_rate": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"INSERT(category,price_version_id,rate,threshold)", "SELECT"},
				"pandokos_worker":  {"INSERT(category,price_version_id,rate,threshold)", "SELECT"},
			},
			"trusted_issuer": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {
					"DELETE",
					"INSERT(algorithms,audience,description,enabled,issuer,jwks_uri,name,required_claims,subject_claim,tenant_id)",
					"SELECT",
					"UPDATE(algorithms,audience,description,enabled,issuer,jwks_uri,name,required_claims,subject_claim)",
				},
			},
			"platform_issuer": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {
					"DELETE",
					"INSERT(algorithms,audience,description,enabled,issuer,jwks_uri,name,required_claims,subject_claim,tenant_claim)",
					"SELECT",
					"UPDATE(algorithms,audience,description,enabled,issuer,jwks_uri,name,required_claims,subject_claim,tenant_claim)",
				},
			},
			"platform_issuer_tenant": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"DELETE", "INSERT(claim_value,issuer_id,tenant_id)", "SELECT", "UPDATE(claim_value)"},
			},
			"subject_mapping": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"DELETE", "INSERT(description,platform_issuer_id,service_account_id,subject,tenant_id,trusted_issuer_id)", "SELECT"},
			},
			"limit_rule": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {"DELETE", "INSERT(currency,enforce,limit_value,metric,model_id,provider_id,service_account_id,subject_kind,subject_mapping_id,tenant_id,thresholds,virtual_key_id)",
					"SELECT", "UPDATE(enforce,limit_value,thresholds)"},
			},
			"platform_limit_rule": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {"DELETE", "INSERT(currency,enforce,limit_value,metric,model_id,provider_id,tenant_id,thresholds)",
					"SELECT", "UPDATE(enforce,limit_value,thresholds)"},
			},
			"limit_event": {
				"pandokos_gateway": {"INSERT(currency,kind,limit_value,metric,model,observed,percent,period_end,period_start,provider,rule_id,rule_level,subject_id,subject_kind,tenant_id)"},
				"pandokos_admin":   {"SELECT"},
			},
		},
		Fixtures: map[string]Fixture{
			"service_account": {
				Insert: `INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, 'sa-' || substr(md5(random()::text), 1, 12)) RETURNING id::text`,
				Update: `description = 'changed'`,
			},
			"role_assignment": {
				Insert: `WITH sa AS (INSERT INTO pandokos.service_account (tenant_id, name)
            VALUES ($1::uuid, 'sa-' || substr(md5(random()::text), 1, 12)) RETURNING id)
INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role)
SELECT $1::uuid, sa.id, 'inference' FROM sa RETURNING id::text`,
				Update: `role = 'changed'`,
			},
			"virtual_key": {
				Insert: `WITH sa AS (INSERT INTO pandokos.service_account (tenant_id, name)
            VALUES ($1::uuid, 'sa-' || substr(md5(random()::text), 1, 12)) RETURNING id)
INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash)
SELECT $1::uuid, sa.id, 't' || translate(substr(md5(random()::text), 1, 16), '0189', 'ghij'),
       sha256(random()::text::bytea) FROM sa RETURNING id::text`,
				Update: `description = 'changed'`,
			},
			"tenant_data_key": {
				Insert: `WITH k AS (INSERT INTO pandokos.kek_version (id, provider) VALUES ('test-fixture', 'test') ON CONFLICT (id) DO NOTHING)
INSERT INTO pandokos.tenant_data_key (tenant_id, kek_version, wrapped_key)
VALUES ($1::uuid, 'test-fixture', decode(repeat('00', 61), 'hex')) RETURNING id::text`,
				Update: `wrapped_key = decode(repeat('11', 61), 'hex')`,
			},
			"provider_credential": {
				Insert: `INSERT INTO pandokos.provider_credential (tenant_id, provider, label, key_version, secret, fingerprint)
VALUES ($1::uuid, 'openai', 'c-' || substr(md5(random()::text), 1, 12), 1, decode(repeat('00', 61), 'hex'), md5(random()::text)) RETURNING id::text`,
				Update: `description = 'changed'`,
			},
			"audit_event": {
				Insert: `INSERT INTO pandokos.audit_event (tenant_id, actor_type, action, resource_type, outcome)
VALUES ($1::uuid, 'platform', 'test.fixture', 'test', 'success') RETURNING id::text`,
				Update: `reason = 'changed'`,
			},
			"usage_record": {
				Insert: `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id,
                                   provider, api_family, operation, outcome, duration_ms, input_source,
                                   output_source, cost_status)
VALUES ($1, pandokos.uuid_v7(), now(), md5(random()::text), 'service_account', gen_random_uuid()::text,
        'openai', 'openai', 'chat.completions', 'ok', 1, 'none', 'none', 'none') RETURNING id::text`,
				Update: `model = 'changed'`,
			},
			"trusted_issuer": {
				Insert: `INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience)
VALUES ($1::uuid, 'ti-' || substr(md5(random()::text), 1, 12),
        'https://' || substr(md5(random()::text), 1, 16) || '.example.test', 'aud-' || $1::uuid::text) RETURNING id::text`,
				Update: `description = 'changed'`,
			},
			"subject_mapping": {
				Insert: `WITH sa AS (INSERT INTO pandokos.service_account (tenant_id, name)
            VALUES ($1::uuid, 'sa-' || substr(md5(random()::text), 1, 12)) RETURNING id),
     ti AS (INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience)
            VALUES ($1::uuid, 'ti-' || substr(md5(random()::text), 1, 12),
                    'https://' || substr(md5(random()::text), 1, 16) || '.example.test', 'aud-' || $1::uuid::text) RETURNING id)
INSERT INTO pandokos.subject_mapping (tenant_id, trusted_issuer_id, subject, service_account_id)
SELECT $1::uuid, ti.id, 'sub-' || substr(md5(random()::text), 1, 12), sa.id FROM sa, ti RETURNING id::text`,
				Update: `subject = 'changed'`,
			},
			"tenant_model_restriction": {
				Insert: `INSERT INTO pandokos.tenant_model_restriction (tenant_id, models) VALUES ($1::uuid, ARRAY['openai/gpt-4o']) RETURNING id::text`,
				Update: `models = ARRAY['openai/gpt-4o-mini']`,
			},
			"limit_rule": {
				Insert: `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, metric, limit_value)
VALUES ($1::uuid, 'tenant', 'requests_per_minute', 10) RETURNING id::text`,
				Update: `limit_value = 11`,
			},
			"limit_event": {
				Insert: `INSERT INTO pandokos.limit_event (tenant_id, kind, rule_id, rule_level, subject_kind, metric, period_start, period_end, percent, limit_value, observed)
VALUES ($1::uuid, 'threshold_reached', pandokos.uuid_v7(), 'tenant', 'tenant', 'tokens_per_day', date_trunc('day', now()), date_trunc('day', now()) + interval '1 day', 80, 10, 8) RETURNING id::text`,
				Update: `percent = 81`,
			},
			"tenant_price_version": {
				Insert: `WITH v AS (INSERT INTO pandokos.tenant_price_version (tenant_id, model_id, valid_from, currency)
           SELECT $1::uuid, m.id, now() + interval '1 hour' + random() * interval '1 hour', 'EUR'
             FROM pandokos.model m ORDER BY m.id LIMIT 1 RETURNING tenant_id, id),
     r AS (INSERT INTO pandokos.tenant_price_rate (tenant_id, price_version_id, category, rate)
           SELECT v.tenant_id, v.id, 'input', 1 FROM v RETURNING price_version_id)
SELECT r.price_version_id::text FROM r`,
				Update: `note = 'changed'`,
			},
			"tenant_price_rate": {
				Insert: `WITH v AS (INSERT INTO pandokos.tenant_price_version (tenant_id, model_id, valid_from, currency)
           SELECT $1::uuid, m.id, now() + interval '1 hour' + random() * interval '1 hour', 'EUR'
             FROM pandokos.model m ORDER BY m.id LIMIT 1 RETURNING tenant_id, id),
     r AS (INSERT INTO pandokos.tenant_price_rate (tenant_id, price_version_id, category, rate)
           SELECT v.tenant_id, v.id, 'input', 1 FROM v RETURNING price_version_id)
SELECT r.price_version_id::text FROM r`,
				Key:    "price_version_id",
				Update: `rate = 2`,
			},
		},
		SecurityDefiner: []string{"virtual_key_lookup", "virtual_key_directory_add", "record_auth_failure", "ensure_usage_partitions",
			"issuer_lookup", "platform_issuer_tenant_lookup", "record_token_auth_failure", "issuer_directory_sync", "platform_issuer_mapping_count",
			"delete_platform_issuer_limit_rules", "record_unlimited_traffic"},
		Views:      []string{"provider_credential_metadata"},
		AppendOnly: []string{"audit_event", "platform_audit_event", "price_import", "usage_record", "limit_event"},
	}
}
