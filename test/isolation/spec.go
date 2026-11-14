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
			"schema_migration":      {Reason: "describes the installation, holds no tenant information"},
			"tenant":                {Reason: "the list of tenants; protected by its own policies", Restricted: true},
			"virtual_key_directory": {Reason: "Key ID to tenant, for the lookup function; no runtime role has any privilege on it"},
			"platform_key":          {Reason: "Credentials of the platform; protected by its own policies", Restricted: true},
			"platform_audit_event":  {Reason: "Audit log of the platform; protected by its own policies", Restricted: true},
			"kek_version":           {Reason: "identifiers of key-encryption key versions; describes the installation, holds no key and no tenant information"},
			"provider":              {Reason: "providers of the catalogue; read by every runtime role, written in platform scope", Restricted: true},
			"provider_endpoint":     {Reason: "endpoints of providers; read by the gateway and the admin role, written in platform scope", Restricted: true},
			"model":                 {Reason: "models of the catalogue; read by every runtime role, written in platform scope", Restricted: true},
			"model_tenant_grant":    {Reason: "which tenants a model is granted to; a tenant's session sees its own rows; protected by its own policies", Restricted: true},
			"price_import":          {Reason: "runs of the price importer; platform scope only", Restricted: true},
			"price_version":         {Reason: "price versions; read by every runtime role, inserted in platform scope", Restricted: true},
			"price_rate":            {Reason: "rates of price versions; read by every runtime role, inserted in platform scope", Restricted: true},
		},
		Grants: map[string]map[string][]string{
			"schema_migration": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"SELECT"},
				"pandokos_worker":  {"SELECT"},
			},
			"tenant": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"INSERT(display_name,name)", "SELECT", "UPDATE(display_name,name,state)"},
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
					"INSERT(api_family,dataset_provider,description,display_name,enabled,name)",
					"SELECT",
					"UPDATE(dataset_provider,description,display_name,enabled)",
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
					"INSERT(availability,capabilities,context_window,description,display_name,max_output_tokens,name,provider_id,status,upstream_id)",
					"SELECT",
					"UPDATE(availability,capabilities,context_window,description,display_name,max_output_tokens,price_override,status,upstream_id)",
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
			"price_rate": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin":   {"INSERT(category,price_version_id,rate,threshold)", "SELECT"},
				"pandokos_worker":  {"INSERT(category,price_version_id,rate,threshold)", "SELECT"},
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
			"tenant_model_restriction": {
				Insert: `INSERT INTO pandokos.tenant_model_restriction (tenant_id, models) VALUES ($1::uuid, ARRAY['openai/gpt-4o']) RETURNING id::text`,
				Update: `models = ARRAY['openai/gpt-4o-mini']`,
			},
		},
		SecurityDefiner: []string{"virtual_key_lookup", "virtual_key_directory_add", "record_auth_failure"},
		Views:           []string{"provider_credential_metadata"},
		AppendOnly:      []string{"audit_event", "platform_audit_event", "price_import"},
	}
}
