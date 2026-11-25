package declare_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/declare"
)

// validate is Parse followed by Validate for a document of the kind of target.
func validate(doc string, target declare.Target) (*declare.Document, error) {
	d, err := declare.Parse([]byte(doc))
	if err != nil {
		return nil, err
	}
	return d, d.Validate(target)
}

const platformHead = "pandokos: v1\nkind: platform\n"
const tenantHead = "pandokos: v1\nkind: tenant\n"

// TestParseNewSections is T-1119: the five sections of sprint 12.3 and the mapping subject of a
// limit parse and validate; an unknown member, a forbidden member (secret, signing_secret, id), a
// duplicate natural key and a scope other than the two are invalid documents (exit code 2); and the
// text of an error quotes no value of the document.
func TestParseNewSections(t *testing.T) {
	t.Run("the documents of the export parse back to themselves", func(t *testing.T) {
		for _, c := range []struct {
			name   string
			want   *declare.Document
			target declare.Target
		}{
			{"tenant", wantTenantFull(), declare.Target{Tenant: tidAcme}},
			{"platform", wantPlatformFull(), declare.Target{Platform: true}},
		} {
			got, err := validate(string(marshal(t, c.want)), c.target)
			if err != nil {
				t.Errorf("%s: a document with the five sections is refused: %v", c.name, err)
				continue
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("%s: the document parsed differs from the one written:\n%s\nwant:\n%s", c.name, marshal(t, got), marshal(t, c.want))
			}
		}
	})

	t.Run("the mapping subject of a limit", func(t *testing.T) {
		d, err := validate(tenantHead+`limits:
  - subject:
      mapping:
        scope: platform
        issuer: shared-idp
        subject: svc-reports
    metric: requests_per_day
    limit: 5
    enforce: true
  - subject: tenant
    metric: requests_per_day
    limit: 9
`, declare.Target{Tenant: tidAcme})
		if err != nil {
			t.Fatalf("a rule on a subject mapping is refused: %v", err)
		}
		if len(d.Limits) != 2 || d.Limits[0].Mapping == nil || *d.Limits[0].Mapping != (declare.MappingSubject{Scope: "platform", Issuer: "shared-idp", Subject: "svc-reports"}) || d.Limits[0].Subject != "" {
			t.Errorf("the mapping form is decoded into %+v", d.Limits[0])
		}
		if d.Limits[1].Mapping != nil || d.Limits[1].Subject != "tenant" {
			t.Errorf("the string form is decoded into %+v", d.Limits[1])
		}
	})

	// Each invalid document holds the canary as the value of the member that is wrong, so that the
	// text of its error can be searched for it.
	for _, c := range []struct {
		name, doc string
		target    declare.Target
		mention   string // what the error must say (a member or a rule, never a value)
	}{
		{"an unknown member of a token issuer", tenantHead + "token_issuers:\n  - name: a\n    issuer: https://i.example.test\n    audience: x\n    enabled: true\n    frobnicate: " + canaryMember + "\n",
			declare.Target{Tenant: tidAcme}, "frobnicate"},
		{"an unknown member of a platform issuer", platformHead + "platform_issuers:\n  - name: a\n    issuer: https://i.example.test\n    audience: x\n    tenant_claim: org\n    enabled: true\n    nonsense: " + canaryMember + "\n",
			declare.Target{Platform: true}, "nonsense"},
		{"an unknown member of a destination", tenantHead + "event_destinations:\n  - name: a\n    url: https://h.example.test\n    enabled: true\n    cursor: " + canaryMember + "\n",
			declare.Target{Tenant: tidAcme}, "cursor"},
		{"an unknown member of a tenant of the platform", platformHead + "tenants:\n  - name: acme\n    state: " + canaryMember + "\n", declare.Target{Platform: true}, "state"},
		{"a secret in a destination", tenantHead + "event_destinations:\n  - name: a\n    url: https://h.example.test\n    enabled: true\n    secret: " + canaryMember + "\n",
			declare.Target{Tenant: tidAcme}, "secret"},
		{"a signing secret in a destination", tenantHead + "event_destinations:\n  - name: a\n    url: https://h.example.test\n    enabled: true\n    signing_secret: " + canaryMember + "\n",
			declare.Target{Tenant: tidAcme}, "signing_secret"},
		{"an id in a token issuer", tenantHead + "token_issuers:\n  - name: a\n    id: " + canaryMember + "\n    issuer: https://i.example.test\n    audience: x\n    enabled: true\n",
			declare.Target{Tenant: tidAcme}, "id"},
		{"an id in a platform issuer", platformHead + "platform_issuers:\n  - id: " + canaryMember + "\n    name: a\n    issuer: https://i.example.test\n    audience: x\n    tenant_claim: org\n    enabled: true\n",
			declare.Target{Platform: true}, "id"},
		{"a scope that is neither tenant nor platform", tenantHead + "subject_mappings:\n  - issuer:\n      scope: " + canaryMember + "\n      name: corp-idp\n    subject: ci\n    service_account: billing\n",
			declare.Target{Tenant: tidAcme}, "scope"},
		{"a scope that is neither tenant nor platform in a limit", tenantHead + "limits:\n  - subject:\n      mapping:\n        scope: " + canaryMember + "\n        issuer: corp-idp\n        subject: ci\n    metric: requests_per_day\n    limit: 5\n",
			declare.Target{Tenant: tidAcme}, "scope"},
		{"an unknown member of the mapping subject", tenantHead + "limits:\n  - subject:\n      mapping:\n        scope: tenant\n        issuer: corp-idp\n        subject: ci\n        extra: " + canaryMember + "\n    metric: requests_per_day\n    limit: 5\n",
			declare.Target{Tenant: tidAcme}, "extra"},
		{"a mapping subject with another key than mapping", tenantHead + "limits:\n  - subject:\n      other:\n        scope: tenant\n    metric: requests_per_day\n    limit: 5\n",
			declare.Target{Tenant: tidAcme}, "subject"},
		{"a token issuer that occurs twice", tenantHead + "token_issuers:\n  - name: a\n    issuer: https://i.example.test\n    audience: x\n    description: " + canaryMember + "\n    enabled: true\n  - name: a\n    issuer: https://j.example.test\n    audience: y\n    enabled: true\n",
			declare.Target{Tenant: tidAcme}, "twice"},
		{"a platform issuer that occurs twice", platformHead + "platform_issuers:\n  - name: a\n    issuer: https://i.example.test\n    audience: x\n    tenant_claim: org\n    description: " + canaryMember + "\n    enabled: true\n  - name: a\n    issuer: https://j.example.test\n    audience: y\n    tenant_claim: org\n    enabled: true\n",
			declare.Target{Platform: true}, "twice"},
		{"a tenant that occurs twice on an issuer", platformHead + "platform_issuers:\n  - name: a\n    issuer: https://i.example.test\n    audience: x\n    tenant_claim: org\n    enabled: true\n    tenants:\n      - tenant: acme\n        claim_value: " + canaryMember + "\n      - tenant: acme\n",
			declare.Target{Platform: true}, "twice"},
		{"a tenant of the platform that occurs twice", platformHead + "tenants:\n  - name: acme\n    external_reference: " + canaryMember + "\n  - name: acme\n    external_reference: other\n", declare.Target{Platform: true}, "twice"},
		{"a mapping that occurs twice", tenantHead + "subject_mappings:\n  - issuer: {scope: tenant, name: corp-idp}\n    subject: ci\n    service_account: billing\n    description: " + canaryMember + "\n  - issuer: {scope: tenant, name: corp-idp}\n    subject: ci\n    service_account: reports\n",
			declare.Target{Tenant: tidAcme}, "twice"},
		{"a destination that occurs twice", tenantHead + "event_destinations:\n  - name: a\n    url: https://h.example.test\n    description: " + canaryMember + "\n    enabled: true\n  - name: a\n    url: https://k.example.test\n    enabled: true\n",
			declare.Target{Tenant: tidAcme}, "twice"},
		{"a rule on a mapping that occurs twice", tenantHead + "limits:\n  - subject: {mapping: {scope: tenant, issuer: corp-idp, subject: ci}}\n    metric: requests_per_day\n    limit: 5\n  - subject: {mapping: {scope: tenant, issuer: corp-idp, subject: ci}}\n    metric: requests_per_day\n    limit: 6\n",
			declare.Target{Tenant: tidAcme}, "twice"},
		{"a section of the other kind (platform issuers in a tenant document)", tenantHead + "platform_issuers:\n  - name: a\n    description: " + canaryMember + "\n", declare.Target{Tenant: tidAcme}, "platform_issuers"},
		{"a section of the other kind (destinations in a platform document)", platformHead + "event_destinations:\n  - name: a\n    description: " + canaryMember + "\n", declare.Target{Platform: true}, "event_destinations"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := validate(c.doc, c.target)
			if err == nil {
				t.Fatal("the document is accepted")
			}
			var inv *declare.InvalidError
			if !errors.As(err, &inv) {
				t.Fatalf("the error is %T (%v), want an *InvalidError: the command answers it with exit code 2", err, err)
			}
			if !strings.Contains(err.Error(), c.mention) {
				t.Errorf("the error %q does not say what is wrong (%q)", err, c.mention)
			}
			if strings.Contains(err.Error(), canaryMember) {
				t.Errorf("the error quotes a value of the document: %q", err)
			}
		})
	}
}
