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

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
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

	t.Run("budget: not_enforced only while no enforced period limit of the tenant matches", func(t *testing.T) {
		p, all, _ := newProvider(t)
		if got := capabilityOf(t, c, c.a.ID, c.ka, p, all.Name); got.Checks.Budget != adminapi.BudgetAvailabilityNotEnforced {
			t.Errorf("budget %q", got.Checks.Budget)
		}
		// Sprint 5.2: a quota of the tenant that matches the call changes the answer (T-681 has the cases).
		r := c.do(t, "POST", "/v1/tenants/"+c.a.ID+"/limits", c.ka, map[string]any{"metric": "requests_per_month", "limit": 100, "provider": p})
		if r.Status != 201 {
			t.Fatalf("create: %d %s", r.Status, r.Body)
		}
		if got := capabilityOf(t, c, c.a.ID, c.ka, p, all.Name); got.Checks.Budget != adminapi.BudgetAvailabilityAvailable {
			t.Errorf("budget %q with a matching quota, want available", got.Checks.Budget)
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
				if credQueries[sel.Sel.Name] {
					used[sel.Sel.Name] = true // a method of *queries.Queries
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

// TestCapabilityBudget is T-681: the four values of checks.budget (sprint 5.2, section 5.13).
func TestCapabilityBudget(t *testing.T) {
	c := newCat(t)
	tn, err := tenant.ParseID(c.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	newModel := func(t *testing.T, priced bool) (provider string, m adminapi.Model) {
		t.Helper()
		p := c.createProvider(t, nil)
		c.createEndpoint(t, p.Name, nil)
		m = c.createModel(t, p.Name, map[string]any{"availability": "all"})
		credentialFor(t, c, p.Name, "main")
		if priced {
			r := c.platform(t, "POST", "/v1/models/"+m.ID+"/prices", map[string]any{"currency": "USD", "rates": []map[string]any{{"category": "input", "threshold": 0, "rate": "1"}, {"category": "output", "threshold": 0, "rate": "2"}}})
			if r.Status/100 != 2 {
				t.Fatalf("a price: %d %s", r.Status, r.Body)
			}
		}
		return p.Name, m
	}
	rule := func(t *testing.T, body map[string]any) adminapi.LimitRule {
		t.Helper()
		return decode[adminapi.LimitRule](t, c.do(t, "POST", "/v1/tenants/"+c.a.ID+"/limits", c.ka, body), 201)
	}
	budget := func(t *testing.T, provider string, m adminapi.Model) adminapi.Capability {
		t.Helper()
		return capabilityOf(t, c, c.a.ID, c.ka, provider, m.Name)
	}
	now := func() time.Time { return time.Now().UTC() }
	usageRows := func(provider, model string, n int, tokens int64, cost, currency string) []dbtest.UsageRow {
		rows := make([]dbtest.UsageRow, n)
		for i := range rows {
			rows[i] = dbtest.UsageRow{StartedAt: now().Add(-time.Duration(i+1) * time.Millisecond), Provider: provider, Model: model, InputTokens: tokens, Cost: cost, Currency: currency}
		}
		return rows
	}
	// seedBudget writes a budget the administration API now refuses (sprint 5.3): one in a currency
	// that no price of its scope uses. It is written as a row, the way a price that went away leaves it.
	seedBudget := func(t *testing.T, db *dbtest.Database, id tenant.ID, metric string, value int64, provider, currency string) {
		t.Helper()
		db.SeedLimitRule(t, id, "tenant", "", metric, value, dbtest.LimitOptions{Provider: provider, Currency: currency})
	}
	want := func(t *testing.T, got adminapi.Capability, budget adminapi.BudgetAvailability, callable bool, reasons ...string) {
		t.Helper()
		if got.Checks.Budget != budget || got.Callable != callable || strings.Join(got.Reasons, ",") != strings.Join(reasons, ",") {
			t.Errorf("budget %q, callable %v, reasons %v; want %q, %v, %v", got.Checks.Budget, got.Callable, got.Reasons, budget, callable, reasons)
		}
	}

	t.Run("requests: not enforced, available, exhausted", func(t *testing.T) {
		p, m := newModel(t, true)
		want(t, budget(t, p, m), adminapi.BudgetAvailabilityNotEnforced, true)
		r := rule(t, map[string]any{"metric": "requests_per_day", "limit": 3, "provider": p})
		want(t, budget(t, p, m), adminapi.BudgetAvailabilityAvailable, true)
		c.api.DB.SeedUsage(t, tn, usageRows(p, m.Name, 2, 10, "", "")...)
		want(t, budget(t, p, m), adminapi.BudgetAvailabilityAvailable, true)
		c.api.DB.SeedUsage(t, tn, usageRows(p, m.Name, 1, 10, "", "")...)
		want(t, budget(t, p, m), adminapi.BudgetAvailabilityExhausted, false, "budget_exhausted")
		// What is available is "not exhausted": raising the limit makes the call callable again.
		decode[adminapi.LimitRule](t, c.do(t, "PATCH", "/v1/tenants/"+c.a.ID+"/limits/"+r.ID, c.ka, map[string]any{"limit": 4}), 200)
		want(t, budget(t, p, m), adminapi.BudgetAvailabilityAvailable, true)
	})

	t.Run("tokens, with the estimated figure when the provider reported none", func(t *testing.T) {
		p, m := newModel(t, true)
		rule(t, map[string]any{"metric": "tokens_per_month", "limit": 100, "provider": p})
		c.api.DB.SeedUsage(t, tn,
			dbtest.UsageRow{StartedAt: now().Add(-time.Second), Provider: p, Model: m.Name, InputTokens: 50, OutputTokens: 30},
			dbtest.UsageRow{StartedAt: now().Add(-2 * time.Second), Provider: p, Model: m.Name, EstimatedInputTokens: 10, EstimatedOutputTokens: 9})
		want(t, budget(t, p, m), adminapi.BudgetAvailabilityAvailable, true) // 99
		c.api.DB.SeedUsage(t, tn, dbtest.UsageRow{StartedAt: now(), Provider: p, Model: m.Name, InputTokens: 1})
		want(t, budget(t, p, m), adminapi.BudgetAvailabilityExhausted, false, "budget_exhausted")
	})

	t.Run("cost in the currency of the budget", func(t *testing.T) {
		p, m := newModel(t, true)
		rule(t, map[string]any{"metric": "cost_per_day", "amount": "1", "currency": "USD", "provider": p})
		c.api.DB.SeedUsage(t, tn, usageRows(p, m.Name, 1, 5, "0.6", "USD")...)
		c.api.DB.SeedUsage(t, tn, usageRows(p, m.Name, 1, 5, "50", "EUR")...) // another currency: no cost for this budget
		want(t, budget(t, p, m), adminapi.BudgetAvailabilityAvailable, true)
		c.api.DB.SeedUsage(t, tn, usageRows(p, m.Name, 1, 5, "0.4", "USD")...)
		want(t, budget(t, p, m), adminapi.BudgetAvailabilityExhausted, false, "budget_exhausted")
	})

	t.Run("unpriced: no price in the currency of the budget", func(t *testing.T) {
		p, m := newModel(t, false)
		seedBudget(t, c.api.DB, tn, "cost_per_month", 10_000_000, p, "USD")
		want(t, budget(t, p, m), adminapi.BudgetAvailabilityUnpriced, false, "no_price")
		q, priced := newModel(t, true)
		seedBudget(t, c.api.DB, tn, "cost_per_month", 10_000_000, q, "EUR") // priced in USD only
		want(t, budget(t, q, priced), adminapi.BudgetAvailabilityUnpriced, false, "no_price")
	})

	t.Run("unpriced with on_unknown_price allow: the call can be made", func(t *testing.T) {
		ac := newCat(t, func(o *admin.Options) { o.Limits.OnUnknownPrice = "allow" })
		atn, _ := tenant.ParseID(ac.a.ID)
		p := ac.createProvider(t, nil)
		ac.createEndpoint(t, p.Name, nil)
		m := ac.createModel(t, p.Name, map[string]any{"availability": "all"})
		credentialFor(t, ac, p.Name, "main")
		seedBudget(t, ac.api.DB, atn, "cost_per_month", 10_000_000, p.Name, "USD") // the model has no price
		got := capabilityOf(t, ac, ac.a.ID, ac.ka, p.Name, m.Name)
		want(t, got, adminapi.BudgetAvailabilityAvailable, true)
		decode[adminapi.LimitRule](t, ac.do(t, "POST", "/v1/tenants/"+ac.a.ID+"/limits", ac.ka, map[string]any{"metric": "requests_per_month", "limit": 1, "provider": p.Name}), 201)
		ac.api.DB.SeedUsage(t, atn, usageRows(p.Name, m.Name, 1, 1, "", "")...)
		want(t, capabilityOf(t, ac, ac.a.ID, ac.ka, p.Name, m.Name), adminapi.BudgetAvailabilityExhausted, false, "budget_exhausted")
	})

	t.Run("scope: a rule applies to its provider and model only", func(t *testing.T) {
		p, m := newModel(t, true)
		other := c.createModel(t, p, map[string]any{"availability": "all"})
		rule(t, map[string]any{"metric": "requests_per_day", "limit": 1, "provider": p, "model": m.Name})
		c.api.DB.SeedUsage(t, tn, usageRows(p, m.Name, 1, 1, "", "")...)
		want(t, budget(t, p, m), adminapi.BudgetAvailabilityExhausted, false, "budget_exhausted")
		want(t, budget(t, p, other), adminapi.BudgetAvailabilityNotEnforced, true) // another model of the provider
		// The usage of another provider does not count.
		q, qm := newModel(t, true)
		rule(t, map[string]any{"metric": "requests_per_day", "limit": 1, "provider": q})
		c.api.DB.SeedUsage(t, tn, usageRows(p, m.Name, 3, 1, "", "")...)
		want(t, budget(t, q, qm), adminapi.BudgetAvailabilityAvailable, true)
	})

	t.Run("soft rules, rules of a service account or a key, and rate limits are not evaluated", func(t *testing.T) {
		p, m := newModel(t, true)
		sa := decode[adminapi.ServiceAccount](t, c.do(t, "POST", "/v1/tenants/"+c.a.ID+"/service-accounts", c.ka, map[string]any{"name": "capability-sa"}), 201)
		rule(t, map[string]any{"metric": "requests_per_day", "limit": 1, "provider": p, "enforce": false, "thresholds": []int{100}})
		rule(t, map[string]any{"metric": "tokens_per_day", "limit": 1, "provider": p, "subject_kind": "service_account", "subject_id": sa.ID})
		rule(t, map[string]any{"metric": "requests_per_minute", "limit": 1, "provider": p})
		rule(t, map[string]any{"metric": "concurrent_requests", "limit": 1, "provider": p})
		c.api.DB.SeedUsage(t, tn, usageRows(p, m.Name, 5, 100, "", "")...)
		want(t, budget(t, p, m), adminapi.BudgetAvailabilityNotEnforced, true)
	})

	t.Run("a platform ceiling counts, and another tenant's usage does not", func(t *testing.T) {
		p, m := newModel(t, true)
		pl := decode[adminapi.PlatformLimitRule](t, c.platform(t, "POST", "/v1/platform-limits", map[string]any{"tenant": c.a.ID, "metric": "requests_per_day", "limit": 2, "provider": p}), 201)
		btn, _ := tenant.ParseID(c.b.ID)
		c.api.DB.SeedUsage(t, btn, usageRows(p, m.Name, 5, 1, "", "")...)
		want(t, budget(t, p, m), adminapi.BudgetAvailabilityAvailable, true)
		c.api.DB.SeedUsage(t, tn, usageRows(p, m.Name, 2, 1, "", "")...)
		want(t, budget(t, p, m), adminapi.BudgetAvailabilityExhausted, false, "budget_exhausted")
		// A tenant rule above the ceiling does not lift it.
		rule(t, map[string]any{"metric": "requests_per_day", "limit": 50, "provider": p})
		want(t, budget(t, p, m), adminapi.BudgetAvailabilityExhausted, false, "budget_exhausted")
		decode[adminapi.PlatformLimitRule](t, c.platform(t, "DELETE", "/v1/platform-limits/"+pl.ID, nil), 200)
		want(t, budget(t, p, m), adminapi.BudgetAvailabilityAvailable, true)
	})

	t.Run("the reasons come after those of 3.3, in the order no_price, budget_exhausted", func(t *testing.T) {
		p, m := newModel(t, false)
		if r := c.do(t, "GET", "/v1/tenants/"+c.a.ID+"/credentials?provider="+p, c.ka, nil); r.Status != 200 {
			t.Fatal(r.Status)
		}
		seedBudget(t, c.api.DB, tn, "cost_per_month", 10_000_000, p, "USD")
		rule(t, map[string]any{"metric": "requests_per_month", "limit": 1, "provider": p})
		c.api.DB.SeedUsage(t, tn, usageRows(p, m.Name, 1, 1, "", "")...)
		want(t, budget(t, p, m), adminapi.BudgetAvailabilityUnpriced, false, "no_price", "budget_exhausted")
		// With a credential problem as well: the reason of 3.3 comes first.
		nc := c.createProvider(t, nil)
		c.createEndpoint(t, nc.Name, nil)
		nm := c.createModel(t, nc.Name, map[string]any{"availability": "all"}) // no credential
		rule(t, map[string]any{"metric": "requests_per_month", "limit": 1, "provider": nc.Name})
		c.api.DB.SeedUsage(t, tn, usageRows(nc.Name, nm.Name, 1, 1, "", "")...)
		want(t, budget(t, nc.Name, nm), adminapi.BudgetAvailabilityExhausted, false, "no_credential", "budget_exhausted")
	})

	t.Run("nothing is written and nothing of another tenant is read", func(t *testing.T) {
		p, m := newModel(t, true)
		rule(t, map[string]any{"metric": "requests_per_day", "limit": 5, "provider": p})
		count := func() (n int) {
			if err := c.api.Super(t).QueryRow(ctx(t), `SELECT (SELECT count(*) FROM pandokos.audit_event) + (SELECT count(*) FROM pandokos.platform_audit_event) + (SELECT count(*) FROM pandokos.limit_event)`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		before := count()
		budget(t, p, m)
		if after := count(); after != before {
			t.Errorf("the check wrote %d rows", after-before)
		}
		// B's key asking about A's tenant sees the same 404 as for a tenant that does not exist.
		expect(t, capCall(t, c, c.kb, c.a.ID, p, m.Name), 404, adminapi.ProblemCodeNotFound)
	})
}

// TestCapabilityTenantZone is T-720: the budget check of the capability endpoint counts in the
// periods of the tenant's zone and matches the rules by identifier: a daily quota exhausted in the
// tenant's day, a rule of a model, and a change that is pending.
func TestCapabilityTenantZone(t *testing.T) {
	// The clock of the check: 22:00 UTC on November 17th, which is 12:00 of the 18th on Kiritimati and 11:00 of the 17th in Pago Pago.
	now := time.Date(2026, 11, 17, 22, 0, 0, 0, time.UTC)
	c := newCat(t, func(o *admin.Options) { o.Now = func() time.Time { return now } })
	tn, err := tenant.ParseID(c.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	p := c.createProvider(t, nil)
	c.createEndpoint(t, p.Name, nil)
	m := c.createModel(t, p.Name, map[string]any{"availability": "all"})
	other := c.createModel(t, p.Name, map[string]any{"availability": "all"})
	credentialFor(t, c, p.Name, "main")
	check := func(t *testing.T, model string) adminapi.Capability {
		t.Helper()
		return capabilityOf(t, c, c.a.ID, c.ka, p.Name, model)
	}
	wantBudget := func(t *testing.T, what string, got adminapi.Capability, b adminapi.BudgetAvailability) {
		t.Helper()
		if got.Checks.Budget != b || got.Callable != (b != adminapi.BudgetAvailabilityExhausted) {
			t.Errorf("%s: budget %q, callable %v, reasons %v; want %q", what, got.Checks.Budget, got.Callable, got.Reasons, b)
		}
	}
	rule := func(body map[string]any) adminapi.LimitRule {
		t.Helper()
		return decode[adminapi.LimitRule](t, c.do(t, "POST", "/v1/tenants/"+c.a.ID+"/limits", c.ka, body), 201)
	}

	// Two requests of the 17th, at 08:00 UTC: in the UTC day, in neither the Kiritimati day (from 10:00 UTC) nor the Pago Pago day (from 11:00 UTC).
	rows := []dbtest.UsageRow{
		{StartedAt: time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC), Provider: p.Name, Model: m.Name, InputTokens: 1},
		{StartedAt: time.Date(2026, 11, 17, 8, 1, 0, 0, time.UTC), Provider: p.Name, Model: m.Name, InputTokens: 1},
	}
	c.api.DB.SeedUsage(t, tn, rows...)
	rule(map[string]any{"metric": "requests_per_day", "limit": 2, "provider": p.Name})

	t.Run("a daily quota exhausted in the day of the default zone", func(t *testing.T) {
		wantBudget(t, "UTC", check(t, m.Name), adminapi.BudgetAvailabilityExhausted)
	})
	t.Run("the same usage is another day in the tenant's zone", func(t *testing.T) {
		c.api.DB.SetTenantZone(t, tn, "Pacific/Kiritimati", "", time.Time{})
		wantBudget(t, "Kiritimati", check(t, m.Name), adminapi.BudgetAvailabilityAvailable)
		c.api.DB.SetTenantZone(t, tn, "Pacific/Pago_Pago", "", time.Time{})
		wantBudget(t, "Pago Pago", check(t, m.Name), adminapi.BudgetAvailabilityAvailable)
		c.api.DB.SetTenantZone(t, tn, "Pacific/Honolulu", "", time.Time{}) // -10: the day started at 10:00 UTC
		wantBudget(t, "Honolulu", check(t, m.Name), adminapi.BudgetAvailabilityAvailable)
		c.api.DB.SetTenantZone(t, tn, "", "", time.Time{})
		wantBudget(t, "the default again", check(t, m.Name), adminapi.BudgetAvailabilityExhausted)
	})
	t.Run("a pending change leaves the zone in force until its instant", func(t *testing.T) {
		later := now.Add(10 * 24 * time.Hour)
		c.api.DB.SetTenantZone(t, tn, "Pacific/Kiritimati", "", later)
		wantBudget(t, "pending", check(t, m.Name), adminapi.BudgetAvailabilityExhausted)
		c.api.DB.SetTenantZone(t, tn, "Pacific/Kiritimati", "", now.Add(-time.Hour)) // the instant has passed
		wantBudget(t, "taken effect", check(t, m.Name), adminapi.BudgetAvailabilityAvailable)
		c.api.DB.SetTenantZone(t, tn, "", "", time.Time{})
	})
	t.Run("a rule of a model limits that model by identifier", func(t *testing.T) {
		rule(map[string]any{"metric": "requests_per_month", "limit": 1, "provider": p.Name, "model": other.Name})
		c.api.DB.SeedUsage(t, tn, dbtest.UsageRow{StartedAt: now.Add(-time.Hour), Provider: p.Name, Model: other.Name, InputTokens: 1})
		wantBudget(t, "the model of the rule", check(t, other.Name), adminapi.BudgetAvailabilityExhausted)
		// The first model is not the rule's model: its usage does not count for it, and the provider's rule governs it.
		got := check(t, m.Name)
		for _, r := range got.Reasons {
			if r == "budget_exhausted" && got.Checks.Budget != adminapi.BudgetAvailabilityExhausted {
				t.Errorf("inconsistent: %+v", got)
			}
		}
		// The same name under another provider is another model: the rule of this one does not limit it.
		p2 := c.createProvider(t, nil)
		c.createEndpoint(t, p2.Name, nil)
		credentialFor(t, c, p2.Name, "main")
		twin := c.createModel(t, p2.Name, map[string]any{"name": other.Name, "availability": "all"})
		wantBudget(t, "a model of the same name of another provider", capabilityOf(t, c, c.a.ID, c.ka, p2.Name, twin.Name), adminapi.BudgetAvailabilityNotEnforced)
	})
}
