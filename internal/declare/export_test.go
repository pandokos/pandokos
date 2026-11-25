package declare_test

import (
	"bytes"
	"context"
	"flag"
	"os"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/pandokos/pandokos/internal/declare"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden.yaml from the expected documents of the tests")

func ctx(t testing.TB) context.Context {
	t.Helper()
	c, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return c
}

// marshal writes a document as the command does: two spaces of indentation.
func marshal(t testing.TB, d *declare.Document) []byte {
	t.Helper()
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(d); err != nil {
		t.Fatal(err)
	}
	_ = enc.Close()
	return b.Bytes()
}

// wantTenant is the document that tenant acme must export to.
func wantTenant() *declare.Document {
	return &declare.Document{
		Pandokos: declare.Version, Kind: declare.KindTenant,
		Settings:         &declare.Settings{TimeZone: "Europe/Rome", ActiveCurrency: "EUR", MetadataKeys: &[]string{"project", "team"}},
		ModelRestriction: &declare.Restriction{Models: []string{"openai/gpt-4o"}},
		Roles:            []declare.Role{{Name: "billing-readers", Description: "Reads usage and limits for billing", Permissions: []string{"limit:read", "usage:read"}}},
		ServiceAccounts: []declare.ServiceAccount{
			{Name: "billing", Description: "Billing exports", Roles: []string{"usage-viewer"}},
			{Name: "reports", Roles: []string{"auditor", "usage-viewer"}},
		},
		Limits: []declare.Limit{
			{Subject: "key: " + keyID, Provider: "openai", Model: "gpt-4o", Metric: "requests_per_minute", Limit: 30, Enforce: true},
			{Subject: "service_account: billing", Metric: "tokens_per_day", Limit: 50000},
			{Subject: "tenant", Metric: "requests_per_day", Limit: 1000, Enforce: true, Thresholds: []int{80, 100}},
		},
		PriceLists: []declare.PriceList{{Currency: "EUR", Versions: []declare.PriceVersion{
			{Provider: "openai", Model: "gpt-4o", ValidFrom: "2026-01-01T00:00:00Z", Note: "list price 2026", Rates: []declare.Rate{{Category: "input", Threshold: 0, Rate: "2.50"}, {Category: "output", Threshold: 0, Rate: "10.00"}}},
			{Provider: "openai", Model: "gpt-4o", ValidFrom: "2026-06-01T00:00:00Z", Rates: []declare.Rate{{Category: "input", Threshold: 0, Rate: "2.80"}, {Category: "output", Threshold: 0, Rate: "11.00"}}},
		}}},
	}
}

// wantPlatform is the document that the platform must export to.
func wantPlatform() *declare.Document {
	return &declare.Document{
		Pandokos: declare.Version, Kind: declare.KindPlatform,
		Providers: []declare.Provider{{Name: "openai", APIFamily: "openai", DisplayName: "OpenAI", DatasetProvider: "openai", StreamUsageOption: "add",
			Endpoints: []declare.Endpoint{{Name: "default", BaseURL: "https://api.openai.com", IsDefault: true}}}},
		Models: []declare.Model{
			{Provider: "openai", Name: "gpt-4o", UpstreamID: "gpt-4o-2024-11-20", DisplayName: "GPT-4o", Status: "active", Availability: "selected", Capabilities: []string{"chat", "vision"}, ContextWindow: 128000, MaxOutputTokens: 16384},
			{Provider: "openai", Name: "gpt-4o-mini", UpstreamID: "gpt-4o-mini", Status: "active", Availability: "all", Capabilities: []string{"chat"}, ContextWindow: 128000, MaxOutputTokens: 16384},
		},
		ModelGrants: []declare.ModelGrant{{Provider: "openai", Model: "gpt-4o", Tenant: "acme"}},
		PriceOverrides: []declare.PriceVersion{{Provider: "openai", Model: "gpt-4o", ValidFrom: "2026-03-01T00:00:00Z", Note: "negotiated", Currency: "USD",
			Rates: []declare.Rate{{Category: "input", Threshold: 0, Rate: "2.00"}, {Category: "output", Threshold: 0, Rate: "8.00"}}}},
		PlatformLimits: []declare.Limit{
			{Tenant: "acme", Provider: "openai", Metric: "tokens_per_day", Limit: 5000000, Enforce: true},
			{Tenant: "default", Metric: "requests_per_minute", Limit: 600, Enforce: true},
		},
	}
}

func golden(t *testing.T, name string, want *declare.Document) string {
	t.Helper()
	path := "testdata/" + name + ".golden.yaml"
	if *update {
		if err := os.WriteFile(path, marshal(t, want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the golden file is missing (run with -update): %v", err)
	}
	return string(b)
}

// TestExportDocument is T-966: from fixed answers of the API the tenant and platform documents equal
// the golden files, in the order of the natural keys whatever the order of the answers, with no ID
// that is not portable and no secret, key, credential, person or usage figure. Since sprint 12.3 (the
// test is changed) the tenant has an issuer, a mapping and a rule on it, and nothing is left out: no
// comment line for the rules on a subject mapping.
func TestExportDocument(t *testing.T) {
	for _, c := range []struct {
		name    string
		target  declare.Target
		state   func() map[string]any
		want    *declare.Document
		leftOut int
		banned  []string
	}{
		{"tenant", declare.Target{Tenant: tidAcme}, tenantStateFull, wantTenantFull(), 0,
			[]string{tidAcme, tidGlobex, saBilling, saReports, roleID, modelGPT, mappingID, mappingTenantID, issuerTenantID, issuerPlatformID, destinationID, destination2ID,
				canarySecret, canaryMember, canaryDelivery, "31337", "424242", "gpt-4o-2024", "credential", "password", "secret", "cursor"}},
		{"platform", declare.Target{Platform: true}, platformStateFull, wantPlatformFull(), 0,
			[]string{tidAcme, tidGlobex, modelGPT, modelMini, issuerPlatformID, "0192f0f0-aaaa", "0192f0f0-bbbb", "0192f0f0-cccc", "424242", "secret"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			wantYAML := golden(t, c.name, c.want)
			f := newFake(t, c.state())
			doc, err := declare.Export(ctx(t), f.client(), c.target)
			if err != nil {
				t.Fatalf("Export: %v", err)
			}
			if got := string(marshal(t, doc)); got != wantYAML {
				t.Errorf("the document differs from testdata/%s.golden.yaml:\n%s\nwant:\n%s", c.name, got, wantYAML)
			}
			if doc.Pandokos != declare.Version || doc.Kind != c.want.Kind {
				t.Errorf("header: %q %q", doc.Pandokos, doc.Kind)
			}
			if len(doc.LeftOut) != c.leftOut {
				t.Errorf("left out: %v, want %d entries", doc.LeftOut, c.leftOut)
			}
			text := string(marshal(t, doc)) + strings.Join(doc.LeftOut, "\n")
			for _, b := range c.banned {
				if strings.Contains(text, b) {
					t.Errorf("the document holds %q:\n%s", b, text)
				}
			}
			if c.name == "tenant" && !strings.Contains(text, keyID) {
				t.Errorf("a key-scoped rule keeps the public key ID, and the document lacks %s", keyID)
			}
			if strings.Contains(text, "\n"+"id:") || strings.Contains(text, " id: ") {
				t.Errorf("the document has an id member:\n%s", text)
			}

			// Whatever the order of the answers, the order of the document is the order of the keys.
			shuffled := c.state()
			for path, v := range shuffled {
				if l, ok := v.([]any); ok {
					shuffled[path] = reverse(l)
				}
			}
			g := newFake(t, shuffled)
			again, err := declare.Export(ctx(t), g.client(), c.target)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(again, doc) {
				t.Errorf("the order of the answers changes the document:\n%s\n%s", marshal(t, again), marshal(t, doc))
			}
			if n := len(f.writeCalls()); n != 0 {
				t.Errorf("an export made %d write calls", n)
			}
		})
	}
}

// TestExportIssuersAndMappings is T-1120: deterministic output; platform issuers with their tenants
// by name, the claim value absent when it is the tenant's ID; token issuers; mappings by the scope
// and name of their issuer and the name of their service account; rules on a mapping exported in the
// mapping form, with no comment line left out.
func TestExportIssuersAndMappings(t *testing.T) {
	t.Run("platform issuers with their tenants by name", func(t *testing.T) {
		f := newFake(t, platformStateFull())
		doc, err := declare.Export(ctx(t), f.client(), declare.Target{Platform: true})
		if err != nil {
			t.Fatal(err)
		}
		want := wantPlatformFull()
		if !reflect.DeepEqual(doc.PlatformIssuers, want.PlatformIssuers) {
			t.Errorf("the platform issuers:\n%+v\nwant:\n%+v", doc.PlatformIssuers, want.PlatformIssuers)
		}
		text := string(marshal(t, doc))
		for _, banned := range []string{tidAcme, tidGlobex, issuerPlatformID} {
			if strings.Contains(text, banned) {
				t.Errorf("the document holds the identifier %s:\n%s", banned, text)
			}
		}
		if strings.Count(text, "claim_value:") != 1 || !strings.Contains(text, "claim_value: globex-co") {
			t.Errorf("claim_value is exported only for the tenant whose value is not its ID:\n%s", text)
		}
		if n := len(f.writeCalls()); n != 0 {
			t.Errorf("an export made %d write calls", n)
		}
	})

	t.Run("token issuers and mappings of a tenant", func(t *testing.T) {
		f := newFake(t, tenantStateFull())
		doc, err := declare.Export(ctx(t), f.client(), declare.Target{Tenant: tidAcme})
		if err != nil {
			t.Fatal(err)
		}
		want := wantTenantFull()
		if !reflect.DeepEqual(doc.TokenIssuers, want.TokenIssuers) {
			t.Errorf("the token issuers:\n%+v\nwant:\n%+v", doc.TokenIssuers, want.TokenIssuers)
		}
		if !reflect.DeepEqual(doc.SubjectMappings, want.SubjectMappings) {
			t.Errorf("the mappings:\n%+v\nwant:\n%+v", doc.SubjectMappings, want.SubjectMappings)
		}
		text := string(marshal(t, doc))
		for _, banned := range []string{mappingID, mappingTenantID, issuerTenantID, issuerPlatformID, saBilling, saReports} {
			if strings.Contains(text, banned) {
				t.Errorf("the document holds the identifier %s:\n%s", banned, text)
			}
		}
	})

	t.Run("a rule on a mapping is exported, with no comment line", func(t *testing.T) {
		f := newFake(t, tenantStateFull())
		doc, err := declare.Export(ctx(t), f.client(), declare.Target{Tenant: tidAcme})
		if err != nil {
			t.Fatal(err)
		}
		var found *declare.Limit
		for i := range doc.Limits {
			if doc.Limits[i].Mapping != nil {
				found = &doc.Limits[i]
			}
		}
		if found == nil || *found.Mapping != (declare.MappingSubject{Scope: "tenant", Issuer: "corp-idp", Subject: "ci-runner"}) || found.Subject != "" || found.Metric != "requests_per_day" || found.Limit != 5 {
			t.Fatalf("the rule on the mapping is %+v in %+v", found, doc.Limits)
		}
		if len(doc.LeftOut) != 0 {
			t.Errorf("left out: %v: a rule on a mapping is no longer left out", doc.LeftOut)
		}
		b, err := doc.YAML()
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "#") {
				t.Errorf("the export has a comment line: %q", line)
			}
		}
		if !strings.Contains(string(b), "mapping:") {
			t.Errorf("the export has no mapping subject:\n%s", b)
		}
	})

	t.Run("the output is deterministic whatever the order of the answers", func(t *testing.T) {
		for _, c := range []struct {
			name   string
			target declare.Target
			state  func() map[string]any
		}{{"tenant", declare.Target{Tenant: tidAcme}, tenantStateFull}, {"platform", declare.Target{Platform: true}, platformStateFull}} {
			first, err := declare.Export(ctx(t), newFake(t, c.state()).client(), c.target)
			if err != nil {
				t.Fatal(err)
			}
			shuffled := c.state()
			for path, v := range shuffled {
				if l, ok := v.([]any); ok {
					shuffled[path] = reverse(l)
				}
			}
			again, err := declare.Export(ctx(t), newFake(t, shuffled).client(), c.target)
			if err != nil {
				t.Fatal(err)
			}
			a, _ := first.YAML()
			b, _ := again.YAML()
			if string(a) != string(b) {
				t.Errorf("%s: the order of the answers changes the document:\n%s\n%s", c.name, a, b)
			}
		}
	})
}

// TestExportExternalReference is part of T-1124: the export of the platform lists only the tenants
// that have an external reference.
func TestExportExternalReference(t *testing.T) {
	f := newFake(t, platformStateFull())
	doc, err := declare.Export(ctx(t), f.client(), declare.Target{Platform: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := []declare.PlatformTenant{{Name: "acme", ExternalReference: "crm-4711"}}; !reflect.DeepEqual(doc.Tenants, want) {
		t.Errorf("the tenants of the document are %+v, want %+v (globex has no reference and is not listed)", doc.Tenants, want)
	}
}
