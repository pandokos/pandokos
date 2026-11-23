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
// that is not portable and no secret, key, credential, person or usage figure.
func TestExportDocument(t *testing.T) {
	for _, c := range []struct {
		name    string
		target  declare.Target
		state   func() map[string]any
		want    *declare.Document
		leftOut int
		banned  []string
	}{
		{"tenant", declare.Target{Tenant: tidAcme}, tenantState, wantTenant(), 1,
			[]string{tidAcme, tidGlobex, saBilling, saReports, roleID, modelGPT, mappingID, canarySecret, canaryMember, "424242", "gpt-4o-2024", "credential", "password", "secret"}},
		{"platform", declare.Target{Platform: true}, platformState, wantPlatform(), 0,
			[]string{tidAcme, tidGlobex, modelGPT, modelMini, "0192f0f0-aaaa", "0192f0f0-bbbb", "0192f0f0-cccc", "424242", "secret"}},
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
			if c.leftOut == 1 && !strings.Contains(doc.LeftOut[0], "subject mapping") {
				t.Errorf("the rule left out is not described as a subject mapping rule: %q", doc.LeftOut[0])
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
