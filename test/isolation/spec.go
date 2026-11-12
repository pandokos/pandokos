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
			"schema_migration": {Reason: "describes the installation, holds no tenant information"},
			"tenant":           {Reason: "the list of tenants; protected by its own policies", Restricted: true},
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
					"DELETE",
					"INSERT(description,name,state,tenant_id,valid_from,valid_until)",
					"SELECT",
					"UPDATE(description,name,state,valid_from,valid_until)",
				},
			},
		},
		Fixtures: map[string]Fixture{
			"service_account": {
				Insert: `INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, 'sa-' || substr(md5(random()::text), 1, 12)) RETURNING id::text`,
				Update: `description = 'changed'`,
			},
		},
	}
}
