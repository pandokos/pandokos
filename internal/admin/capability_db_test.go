package admin_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// capCall asks for the capability of a tenant with a key.
func capCall(t testing.TB, c *cat, key, tenantID, provider, model string) *apitest.Response {
	t.Helper()
	q := url.Values{}
	if provider != "" {
		q.Set("provider", provider)
	}
	if model != "" {
		q.Set("model", model)
	}
	return c.do(t, "GET", "/v1/tenants/"+tenantID+"/capability?"+q.Encode(), key, nil)
}

func capabilityOf(t testing.TB, c *cat, tenantID, key, provider, model string) adminapi.Capability {
	t.Helper()
	var out adminapi.Capability
	ok(t, capCall(t, c, key, tenantID, provider, model), 200, &out)
	return out
}

// credentialFor registers a credential of tenant a for the provider, as its default.
func credentialFor(t testing.TB, c *cat, provider, label string) adminapi.Credential {
	t.Helper()
	cred := c.api.Credential(t, c.a.ID, c.ka, provider, label)
	if !cred.IsDefault {
		r := c.do(t, "POST", "/v1/tenants/"+c.a.ID+"/credentials/"+cred.ID+"/set-default", c.ka, nil)
		if r.Status/100 != 2 {
			t.Fatalf("set-default: %d %s", r.Status, r.Body)
		}
	}
	return cred
}

// TestCapability is T-543: each row of the table of section 5.6.
func TestCapability(t *testing.T) {
	fake := fakeprovider.New(t)
	fake.Accept(fixtures.CanaryCredential)
	c := newCat(t, withValidator(t, time.Second))

	// A provider with an endpoint, an "all" model and a "selected" model, and a credential.
	newProvider := func(t *testing.T) (name string, all, selected adminapi.Model) {
		t.Helper()
		p := c.createProvider(t, nil)
		c.createEndpoint(t, p.Name, nil)
		all = c.createModel(t, p.Name, map[string]any{"availability": "all"})
		selected = c.createModel(t, p.Name, map[string]any{"availability": "selected"})
		return p.Name, all, selected
	}
	reasons := func(got adminapi.Capability) string { return strings.Join(got.Reasons, ",") }

	t.Run("callable", func(t *testing.T) {
		p, all, _ := newProvider(t)
		cred := credentialFor(t, c, p, "main")
		start := time.Now().Add(-5 * time.Second)
		got := capabilityOf(t, c, c.a.ID, c.ka, p, all.Name)
		if !got.Callable || len(got.Reasons) != 0 || got.Reasons == nil || got.Provider != p || got.Model != all.Name {
			t.Fatalf("%+v", got)
		}
		want := adminapi.CapabilityChecks{ProviderAvailable: true, ModelAllowed: true, Credential: adminapi.CredentialAvailabilityActive,
			CredentialID: cred.ID, CredentialValidated: false, Budget: adminapi.BudgetAvailabilityNotEnforced}
		if got.Checks != want {
			t.Errorf("checks %+v, want %+v", got.Checks, want)
		}
		if got.CheckedAt.Before(start) || got.CheckedAt.After(time.Now().Add(5*time.Second)) {
			t.Errorf("checked_at %v", got.CheckedAt)
		}
	})

	t.Run("provider unknown, disabled, without a default endpoint: one reason, the model is not evaluated", func(t *testing.T) {
		p, all, _ := newProvider(t)
		credentialFor(t, c, p, "main")
		if r := c.platform(t, "PATCH", "/v1/providers/"+p, map[string]any{"enabled": false}); r.Status != 200 {
			t.Fatalf("%d %s", r.Status, r.Body)
		}
		got := capabilityOf(t, c, c.a.ID, c.ka, p, all.Name)
		if got.Callable || reasons(got) != "provider_unavailable" || got.Checks.ProviderAvailable || got.Checks.ModelAllowed {
			t.Errorf("disabled: %+v", got)
		}

		bare := c.createProvider(t, nil) // no endpoint
		bareModel := c.createModel(t, bare.Name, map[string]any{"availability": "all"})
		credentialFor(t, c, bare.Name, "main")
		got = capabilityOf(t, c, c.a.ID, c.ka, bare.Name, bareModel.Name)
		if got.Callable || reasons(got) != "provider_unavailable" || got.Checks.ProviderAvailable || got.Checks.ModelAllowed {
			t.Errorf("without an endpoint: %+v", got)
		}

		got = capabilityOf(t, c, c.a.ID, c.ka, "synthetic-no-such-provider", "synthetic-model")
		if got.Callable || len(got.Reasons) == 0 || got.Reasons[0] != "provider_unavailable" || got.Checks.ProviderAvailable || got.Checks.ModelAllowed {
			t.Errorf("unknown: %+v", got)
		}
		for _, r := range got.Reasons {
			if r == "model_not_allowed" {
				t.Errorf("the model was evaluated for an unavailable provider: %v", got.Reasons)
			}
		}
	})

	t.Run("model unknown, retired, not granted, outside the restriction: one reason", func(t *testing.T) {
		p, all, selected := newProvider(t)
		credentialFor(t, c, p, "main")
		check := func(what, model string) {
			t.Helper()
			got := capabilityOf(t, c, c.a.ID, c.ka, p, model)
			if got.Callable || reasons(got) != "model_not_allowed" || got.Checks.ModelAllowed || !got.Checks.ProviderAvailable {
				t.Errorf("%s: %+v", what, got)
			}
		}
		check("unknown", "synthetic-no-such-model")
		check("not granted", selected.Name)
		c.grant(t, selected.ID, c.a.ID)
		if got := capabilityOf(t, c, c.a.ID, c.ka, p, selected.Name); !got.Callable {
			t.Errorf("granted: %+v", got)
		}
		retired := c.createModel(t, p, map[string]any{"availability": "all"})
		if r := c.platform(t, "PATCH", "/v1/models/"+retired.ID, map[string]any{"status": "retired"}); r.Status != 200 {
			t.Fatalf("retire: %d %s", r.Status, r.Body)
		}
		check("retired", retired.Name)
		if r := c.do(t, "PUT", "/v1/tenants/"+c.a.ID+"/model-restriction", c.ka, map[string]any{"models": []string{p + "/" + selected.Name}}); r.Status/100 != 2 {
			t.Fatalf("restrict: %d %s", r.Status, r.Body)
		}
		check("outside the restriction", all.Name)
		if got := capabilityOf(t, c, c.a.ID, c.ka, p, selected.Name); !got.Callable {
			t.Errorf("inside the restriction: %+v", got)
		}
		// Another tenant is not restricted.
		if got := capabilityOf(t, c, c.b.ID, c.kb, p, all.Name); got.Callable || reasons(got) != "no_credential" {
			t.Errorf("tenant b: %+v", got)
		}
		if r := c.do(t, "DELETE", "/v1/tenants/"+c.a.ID+"/model-restriction", c.ka, nil); r.Status/100 != 2 {
			t.Fatalf("clear restriction: %d %s", r.Status, r.Body)
		}
	})

	t.Run("no default credential, disabled, revoked", func(t *testing.T) {
		p, all, _ := newProvider(t)
		got := capabilityOf(t, c, c.a.ID, c.ka, p, all.Name)
		if got.Callable || reasons(got) != "no_credential" || got.Checks.Credential != adminapi.CredentialAvailabilityMissing || got.Checks.CredentialID != "" {
			t.Errorf("none: %+v", got)
		}
		cred := credentialFor(t, c, p, "main")
		other := c.api.Credential(t, c.a.ID, c.ka, p, "second") // not the default: it plays no part
		if r := c.do(t, "POST", "/v1/tenants/"+c.a.ID+"/credentials/"+cred.ID+"/disable", c.ka, nil); r.Status/100 != 2 {
			t.Fatalf("disable: %d %s", r.Status, r.Body)
		}
		got = capabilityOf(t, c, c.a.ID, c.ka, p, all.Name)
		if got.Callable || reasons(got) != "credential_disabled" || got.Checks.Credential != adminapi.CredentialAvailabilityDisabled || got.Checks.CredentialID != cred.ID {
			t.Errorf("disabled: %+v (the other credential %s must not count)", got, other.ID)
		}
		if r := c.do(t, "POST", "/v1/tenants/"+c.a.ID+"/credentials/"+cred.ID+"/enable", c.ka, nil); r.Status/100 != 2 {
			t.Fatalf("enable: %d %s", r.Status, r.Body)
		}
		if got = capabilityOf(t, c, c.a.ID, c.ka, p, all.Name); !got.Callable {
			t.Errorf("enabled again: %+v", got)
		}
		if r := c.do(t, "POST", "/v1/tenants/"+c.a.ID+"/credentials/"+cred.ID+"/revoke", c.ka, nil); r.Status/100 != 2 {
			t.Fatalf("revoke: %d %s", r.Status, r.Body)
		}
		got = capabilityOf(t, c, c.a.ID, c.ka, p, all.Name)
		if got.Callable || reasons(got) != "credential_revoked" || got.Checks.Credential != adminapi.CredentialAvailabilityRevoked || got.Checks.CredentialID != cred.ID {
			t.Errorf("revoked: %+v", got)
		}
	})

	t.Run("reasons are listed in the order of the table", func(t *testing.T) {
		p, _, selected := newProvider(t)
		got := capabilityOf(t, c, c.a.ID, c.ka, p, selected.Name) // not granted, and no credential
		if got.Callable || reasons(got) != "model_not_allowed,no_credential" {
			t.Errorf("%+v", got)
		}
	})

	t.Run("credential_validated reports validated_at and never blocks", func(t *testing.T) {
		p := c.createProvider(t, nil)
		c.createEndpoint(t, p.Name, map[string]any{"base_url": fake.URL(), "private": true})
		m := c.createModel(t, p.Name, map[string]any{"availability": "all"})
		r := c.do(t, "POST", "/v1/tenants/"+c.a.ID+"/credentials", c.ka, map[string]any{"provider": p.Name, "label": "validated", "secret": fixtures.CanaryCredential, "validate": true})
		var cred adminapi.Credential
		ok(t, r, 201, &cred)
		if cred.ValidatedAt == nil {
			t.Fatalf("the credential was not validated: %+v", cred)
		}
		got := capabilityOf(t, c, c.a.ID, c.ka, p.Name, m.Name)
		if !got.Callable || !got.Checks.CredentialValidated {
			t.Errorf("validated: %+v", got)
		}
		if r := c.do(t, "PUT", "/v1/tenants/"+c.a.ID+"/credentials/"+cred.ID+"/secret", c.ka, map[string]any{"secret": fixtures.CanaryCredential + "-2"}); r.Status/100 != 2 {
			t.Fatalf("replace: %d %s", r.Status, r.Body)
		}
		got = capabilityOf(t, c, c.a.ID, c.ka, p.Name, m.Name)
		if !got.Callable || got.Checks.CredentialValidated {
			t.Errorf("replaced without validation: %+v (callable, and validated_at is cleared)", got)
		}
	})

	t.Run("budget", func(t *testing.T) {
		p, all, _ := newProvider(t)
		if got := capabilityOf(t, c, c.a.ID, c.ka, p, all.Name); got.Checks.Budget != adminapi.BudgetAvailabilityNotEnforced {
			t.Errorf("budget %q", got.Checks.Budget)
		}
	})

	t.Run("provider and model are required and checked", func(t *testing.T) {
		for _, tc := range []struct {
			name, provider, model string
			fields                []string
		}{
			{"no provider", "", "m", []string{"provider"}},
			{"no model", "p", "", []string{"model"}},
			{"neither", "", "", []string{"model", "provider"}},
			{"a malformed provider", "Bad_Provider", "m", []string{"provider"}},
			{"a malformed model", "p", "bad model!", []string{"model"}},
			{"both malformed", "Bad_Provider", "bad model!", []string{"model", "provider"}},
		} {
			r := capCall(t, c, c.ka, c.a.ID, tc.provider, tc.model)
			p := expect(t, r, 400, adminapi.ProblemCodeInvalidRequest)
			var fields []string
			for _, e := range p.Errors {
				fields = append(fields, e.Field)
			}
			sort.Strings(fields)
			if strings.Join(fields, ",") != strings.Join(tc.fields, ",") {
				t.Errorf("%s: errors name %v, want %v", tc.name, fields, tc.fields)
			}
			testutil.AssertAbsent(t, tc.name, string(r.Body), "Bad_Provider", "bad model!")
		}
	})

	t.Run("nothing is audited and the answer holds no secret", func(t *testing.T) {
		p, all, _ := newProvider(t)
		credentialFor(t, c, p, "audit-check")
		count := func() (n int) {
			if err := c.api.Super(t).QueryRow(ctx(t), `SELECT (SELECT count(*) FROM pandokos.audit_event) + (SELECT count(*) FROM pandokos.platform_audit_event)`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		before := count()
		r := capCall(t, c, c.ka, c.a.ID, p, all.Name)
		ok(t, r, 200, nil)
		if after := count(); after != before {
			t.Errorf("a read of the capability wrote %d audit rows", after-before)
		}
		testutil.AssertNoCanary(t, "the answer", string(r.Body))
		for _, s := range testutil.SecretForms(fixtures.CanaryCredential) {
			if strings.Contains(string(r.Body), s) {
				t.Error("the answer holds the secret")
			}
		}
	})

	t.Run("the handler calls no credential query but ListCredentials", func(t *testing.T) {
		root := testutil.RepoRoot(t)
		credQueries := map[string]bool{}
		sqlText, err := os.ReadFile(filepath.Join(root, "internal/store/queries/sql/credentials.sql"))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range regexp.MustCompile(`(?m)^-- name: (\w+)`).FindAllStringSubmatch(string(sqlText), -1) {
			credQueries[m[1]] = true
		}
		if !credQueries["ListCredentials"] || len(credQueries) < 5 {
			t.Fatalf("credentials.sql names %v", credQueries)
		}
		file := filepath.Join(root, "internal/admin/capability.go")
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		used := map[string]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "queries" {
					used[sel.Sel.Name] = true
				}
				if credQueries[sel.Sel.Name] && sel.Sel.Name != "ListCredentials" {
					t.Errorf("capability.go refers to %s, a query of credentials.sql other than ListCredentials", sel.Sel.Name)
				}
			}
			return true
		})
		if !used["ListCredentials"] {
			t.Error("capability.go does not read the tenant's credentials through queries.ListCredentials")
		}
		for _, im := range f.Imports {
			path := strings.Trim(im.Path.Value, `"`)
			if strings.HasSuffix(path, "/internal/vault") || strings.HasSuffix(path, "/internal/gateway") || strings.HasSuffix(path, "/internal/resolver") {
				t.Errorf("capability.go imports %s", path)
			}
		}
	})
}

// TestCapabilityIsolation is T-544: the tenant boundary and the permission of the operation.
func TestCapabilityIsolation(t *testing.T) {
	c := newCat(t)
	p := c.createProvider(t, nil)
	c.createEndpoint(t, p.Name, nil)
	m := c.createModel(t, p.Name, map[string]any{"availability": "all"})
	c.api.Credential(t, c.a.ID, c.ka, p.Name, "main")

	t.Run("another tenant's administrator: 404", func(t *testing.T) {
		expect(t, capCall(t, c, c.kb, c.a.ID, p.Name, m.Name), 404, adminapi.ProblemCodeNotFound)
	})
	t.Run("a platform key: 403", func(t *testing.T) {
		expect(t, capCall(t, c, c.plat, c.a.ID, p.Name, m.Name), 403, adminapi.ProblemCodeForbidden)
	})
	t.Run("the inference role: 403", func(t *testing.T) {
		client := c.api.Client(c.ka)
		sa, err := client.CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: c.a.ID, Body: adminapi.ServiceAccountCreate{Name: "worker", Roles: []string{"inference"}}})
		if err != nil {
			t.Fatal(err)
		}
		k, err := client.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: c.a.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: sa.ID}})
		if err != nil {
			t.Fatal(err)
		}
		expect(t, capCall(t, c, k.Secret, c.a.ID, p.Name, m.Name), 403, adminapi.ProblemCodeForbidden)
	})
	t.Run("no key and a wrong key: 401", func(t *testing.T) {
		expect(t, capCall(t, c, "", c.a.ID, p.Name, m.Name), 401, adminapi.ProblemCodeUnauthenticated)
		expect(t, capCall(t, c, "pdk_wrong", c.a.ID, p.Name, m.Name), 401, adminapi.ProblemCodeUnauthenticated)
	})
	t.Run("another tenant's data never shows", func(t *testing.T) {
		// Tenant b asks for its own capability of a provider only tenant a has a credential for.
		got := capabilityOf(t, c, c.b.ID, c.kb, p.Name, m.Name)
		if got.Callable || got.Checks.CredentialID != "" || strings.Join(got.Reasons, ",") != "no_credential" {
			t.Errorf("%+v", got)
		}
	})
	t.Run("a suspended tenant: 403 tenant_suspended", func(t *testing.T) {
		if r := c.platform(t, "POST", "/v1/tenants/"+c.a.ID+"/suspend", nil); r.Status/100 != 2 {
			t.Fatalf("suspend: %d %s", r.Status, r.Body)
		}
		expect(t, capCall(t, c, c.ka, c.a.ID, p.Name, m.Name), 403, adminapi.ProblemCodeTenantSuspended)
	})
}
