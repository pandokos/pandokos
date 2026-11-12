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

// TestDefaultSpecMatchesDesign is T-142: the spec is the one of sprint.md sections 5.6, 6.1, 6.2.
func TestDefaultSpecMatchesDesign(t *testing.T) {
	want := Spec{
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
	got := DefaultSpec()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DefaultSpec() differs from the design:\n got %+v\nwant %+v", got, want)
	}
	if len(got.SecurityDefiner) != 0 || len(got.Views) != 0 {
		t.Error("no SECURITY DEFINER function and no view is allowed yet")
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

// TestSchemaLintDetects is T-141: for each rule a deliberately defective object makes it fire.
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
