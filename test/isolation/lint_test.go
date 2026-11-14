package isolation

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/dbtest"
)

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return c
}

// TestSchemaLint is T-140: the real migrations produce a schema with zero findings. This test
// is the build gate of exit criterion 1: a table without a policy fails the build.
func TestSchemaLint(t *testing.T) {
	d := dbtest.NewMigrated(t)
	findings, err := Lint(ctx(t), d.Super(t), DefaultSpec())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Errorf("%s %s: %s", f.Rule, f.Object, f.Detail)
	}
}

// TestDefaultSpecMatchesDesign is T-142 (changed in sprint 2.1 and 2.2, T-390): the spec is the one of sprint 1.3,
// sections 5.10 and 6.1, with the vault tables, the metadata view and the pin of sprint 2.1,
// sections 5.3 and 6.
func TestDefaultSpecMatchesDesign(t *testing.T) {
	want := Spec{
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
			"usage_record": {
				"pandokos_gateway": {"INSERT(api_family,cache_read_tokens,cache_write_tokens,cost,cost_fallbacks,cost_reason,cost_status,credential_id,currency,duration_ms,endpoint,error_code,estimated_input_tokens,estimated_output_tokens,funding,id,input_source,input_tokens,key_id,metadata,metadata_dropped,model,model_id,operation,other_units,outcome,output_source,output_tokens,price_version_id,principal_id,principal_kind,provider,provider_usage,reasoning_tokens,request_bytes,request_id,response_bytes,started_at,stream,tenant_id,trace_id,ttfb_ms,upstream_model,upstream_status)"},
				"pandokos_admin":   {"SELECT"},
				"pandokos_worker":  {"SELECT"},
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
			"usage_record": {
				Insert: `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, funding,
                                   provider, api_family, operation, outcome, duration_ms, input_source,
                                   output_source, cost_status)
VALUES ($1, pandokos.uuid_v7(), now(), md5(random()::text), 'service_account', gen_random_uuid()::text, 'byok',
        'openai', 'openai', 'chat.completions', 'ok', 1, 'none', 'none', 'none') RETURNING id::text`,
				Update: `model = 'changed'`,
			},
			"tenant_model_restriction": {
				Insert: `INSERT INTO pandokos.tenant_model_restriction (tenant_id, models) VALUES ($1::uuid, ARRAY['openai/gpt-4o']) RETURNING id::text`,
				Update: `models = ARRAY['openai/gpt-4o-mini']`,
			},
		},
		SecurityDefiner: []string{"virtual_key_lookup", "virtual_key_directory_add", "record_auth_failure", "ensure_usage_partitions"},
		Views:           []string{"provider_credential_metadata"},
		AppendOnly:      []string{"audit_event", "platform_audit_event", "price_import", "usage_record"},
	}
	got := DefaultSpec()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DefaultSpec() differs from the design:\n got %+v\nwant %+v", got, want)
	}
	if !reflect.DeepEqual(got.Views, []string{"provider_credential_metadata"}) {
		t.Errorf("Views = %v, want only provider_credential_metadata", got.Views)
	}
}

const (
	canonicalTable = `CREATE TABLE pandokos.zz (
    tenant_id uuid NOT NULL REFERENCES pandokos.tenant (id),
    id uuid NOT NULL DEFAULT pandokos.uuid_v7(),
    PRIMARY KEY (tenant_id, id)
);`
	enable = `ALTER TABLE pandokos.zz ENABLE ROW LEVEL SECURITY;`
	force  = `ALTER TABLE pandokos.zz FORCE ROW LEVEL SECURITY;`
	policy = `CREATE POLICY tenant_isolation ON pandokos.zz
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));`
	ok = enable + force + policy
)

// TestSchemaLintDetects is T-141: for each rule L-01 to L-19 a deliberately defective object makes
// it fire.
func TestSchemaLintDetects(t *testing.T) {
	type row struct {
		name string
		ddl  string // run as pandokos_owner
		post string // run as the superuser afterwards
		spec func(*Spec)
		rule string // the rule that must fire; several rules separated by commas must all fire
		only bool   // the findings must be exactly one, with this rule, on pandokos.zz
	}
	rows := []row{
		{name: "owner", ddl: canonicalTable + ok, post: `ALTER TABLE pandokos.zz OWNER TO pandokos_admin`, rule: "L-01,L-15"},
		{name: "view", ddl: `CREATE VIEW pandokos.zz_v AS SELECT id FROM pandokos.service_account`, rule: "L-02"},
		{name: "view-without-security-invoker", ddl: `CREATE VIEW pandokos.zz_v AS SELECT 1 AS x`,
			spec: func(s *Spec) { s.Views = append(s.Views, "zz_v") }, rule: "L-19"},
		{name: "no-tenant-column", ddl: `CREATE TABLE pandokos.zz (id uuid PRIMARY KEY)`, rule: "L-03"},
		{name: "nullable-tenant", ddl: `CREATE TABLE pandokos.zz (
    tenant_id uuid REFERENCES pandokos.tenant (id),
    id uuid NOT NULL DEFAULT pandokos.uuid_v7(),
    PRIMARY KEY (id)
);` + ok, rule: "L-03"},
		{name: "no-tenant-fk", ddl: `CREATE TABLE pandokos.zz (
    tenant_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT pandokos.uuid_v7(),
    PRIMARY KEY (tenant_id, id)
);` + ok, rule: "L-04"},
		{name: "rls-not-enabled", ddl: canonicalTable, rule: "L-05"},
		{name: "rls-not-forced", ddl: canonicalTable + enable + policy, rule: "L-06"},
		{name: "no-policy", ddl: canonicalTable + enable + force, rule: "L-07"},
		{name: "policy-true", ddl: canonicalTable + enable + force + `CREATE POLICY tenant_isolation ON pandokos.zz USING (true) WITH CHECK (true);`, rule: "L-08"},
		{name: "policy-select-only", ddl: canonicalTable + enable + force + `CREATE POLICY tenant_isolation ON pandokos.zz FOR SELECT
    USING (tenant_id = (SELECT pandokos.current_tenant_id()));`, rule: "L-08"},
		{name: "policy-raw-setting", ddl: canonicalTable + enable + force + `CREATE POLICY tenant_isolation ON pandokos.zz
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);`, rule: "L-08"},
		{name: "policy-restricted-roles", ddl: canonicalTable + enable + force + `CREATE POLICY tenant_isolation ON pandokos.zz TO pandokos_admin
    USING (tenant_id = (SELECT pandokos.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT pandokos.current_tenant_id()));`, rule: "L-08"},
		{name: "extra-policy", ddl: canonicalTable + ok + `CREATE POLICY wide ON pandokos.zz FOR SELECT USING (true);`, rule: "L-09"},
		{name: "key-without-tenant", ddl: `CREATE TABLE pandokos.zz (
    tenant_id uuid NOT NULL REFERENCES pandokos.tenant (id),
    id uuid NOT NULL DEFAULT pandokos.uuid_v7(),
    PRIMARY KEY (id)
);` + ok, rule: "L-10"},
		{name: "fk-without-tenant", ddl: `ALTER TABLE pandokos.service_account ADD CONSTRAINT zz_u UNIQUE (id);
CREATE TABLE pandokos.zz (
    tenant_id uuid NOT NULL REFERENCES pandokos.tenant (id),
    id uuid NOT NULL DEFAULT pandokos.uuid_v7(),
    sa uuid REFERENCES pandokos.service_account (id),
    PRIMARY KEY (tenant_id, id)
);` + ok, rule: "L-11"},
		{name: "grant-truncate", ddl: `GRANT TRUNCATE ON pandokos.service_account TO pandokos_admin`, rule: "L-12"},
		{name: "grant-public", ddl: `GRANT SELECT ON pandokos.service_account TO PUBLIC`, rule: "L-12"},
		{name: "grant-gateway-write", ddl: `GRANT UPDATE ON pandokos.service_account TO pandokos_gateway`, rule: "L-12"},
		{name: "grant-column", ddl: `GRANT UPDATE (id) ON pandokos.service_account TO pandokos_admin`, rule: "L-12"},
		{name: "missing-grant", ddl: `REVOKE SELECT ON pandokos.tenant FROM pandokos_worker`, rule: "L-12"},
		{name: "tenant-not-forced", ddl: `ALTER TABLE pandokos.tenant NO FORCE ROW LEVEL SECURITY`, rule: "L-13"},
		{name: "security-definer", ddl: `CREATE FUNCTION pandokos.zz_f() RETURNS integer LANGUAGE sql SECURITY DEFINER RETURN 1`, rule: "L-14"},
		{name: "public-function", ddl: `CREATE FUNCTION pandokos.zz_g() RETURNS integer LANGUAGE sql RETURN 1;
GRANT EXECUTE ON FUNCTION pandokos.zz_g() TO PUBLIC;`, rule: "L-14"},
		{name: "runtime-create", post: `GRANT CREATE ON SCHEMA pandokos TO pandokos_worker`, rule: "L-15"},
		{name: "append-only-grant", ddl: `GRANT UPDATE ON pandokos.audit_event TO pandokos_admin`, rule: "L-17,L-12"},
		{name: "append-only-column-grant", ddl: `GRANT UPDATE (reason) ON pandokos.platform_audit_event TO pandokos_admin`, rule: "L-17,L-12"},
		{name: "append-only-missing-row-trigger", ddl: `DROP TRIGGER audit_event_append_only ON pandokos.audit_event`, rule: "L-17"},
		{name: "append-only-missing-truncate-trigger", ddl: `DROP TRIGGER platform_audit_event_no_truncate ON pandokos.platform_audit_event`, rule: "L-17"},
		{name: "append-only-disabled-trigger", ddl: `ALTER TABLE pandokos.audit_event DISABLE TRIGGER audit_event_append_only`, rule: "L-17"},
		{name: "append-only-wrong-function", ddl: `DROP TRIGGER audit_event_append_only ON pandokos.audit_event;
CREATE TRIGGER audit_event_append_only BEFORE UPDATE OR DELETE ON pandokos.audit_event FOR EACH ROW EXECUTE FUNCTION pandokos.row_guard();`, rule: "L-17"},
		{name: "definer-without-search-path", ddl: `ALTER FUNCTION pandokos.virtual_key_lookup(text) RESET search_path`, rule: "L-18"},
		{name: "no-fixture", ddl: canonicalTable + ok, rule: "L-16", only: true},
		{name: "stale-spec", spec: func(s *Spec) { s.Fixtures["gone"] = Fixture{Insert: "SELECT 1"} }, rule: "L-16"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			d := dbtest.NewMigrated(t)
			super := d.Super(t)
			if r.ddl != "" {
				if _, err := super.Exec(ctx(t), "SET ROLE pandokos_owner"); err != nil {
					t.Fatal(err)
				}
				if _, err := super.Exec(ctx(t), r.ddl); err != nil {
					t.Fatalf("ddl: %v", err)
				}
				if _, err := super.Exec(ctx(t), "RESET ROLE"); err != nil {
					t.Fatal(err)
				}
			}
			if r.post != "" {
				if _, err := super.Exec(ctx(t), r.post); err != nil {
					t.Fatalf("post: %v", err)
				}
			}
			spec := DefaultSpec()
			if r.spec != nil {
				r.spec(&spec)
			}
			findings, err := Lint(ctx(t), d.Super(t), spec)
			if err != nil {
				t.Fatal(err)
			}
			if r.only {
				if len(findings) != 1 || findings[0].Rule != r.rule || findings[0].Object != "pandokos.zz" {
					t.Fatalf("findings %+v, want exactly one %s on pandokos.zz", findings, r.rule)
				}
				return
			}
			for _, want := range strings.Split(r.rule, ",") {
				fired := false
				for _, f := range findings {
					if f.Rule == want {
						fired = true
					}
				}
				if !fired {
					t.Errorf("rule %s did not fire; findings: %+v", want, findings)
				}
			}
		})
	}
}
