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
			"virtual_key": {
				"pandokos_gateway": {"SELECT"},
				"pandokos_admin": {
					"INSERT(description,expires_at,key_id,scopes,secret_hash,service_account_id,tenant_id)",
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
				"pandokos_admin": {"INSERT(action,actor_id,actor_name,actor_type,detail,key_id,outcome,reason,request_id,resource_id,resource_type)"},
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
			"audit_event": {
				Insert: `INSERT INTO pandokos.audit_event (tenant_id, actor_type, action, resource_type, outcome)
VALUES ($1::uuid, 'platform', 'test.fixture', 'test', 'success') RETURNING id::text`,
				Update: `reason = 'changed'`,
			},
		},
		SecurityDefiner: []string{"virtual_key_lookup", "virtual_key_directory_add"},
		AppendOnly:      []string{"audit_event", "platform_audit_event"},
	}
}
