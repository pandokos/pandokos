package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/fakeprovider"
)

// stateOf returns what the API says about a tenant, as one text: the output of the commands that
// read every resource of a document, in JSON.
func (p *proc) stateOf(t *testing.T, admin, tenantID string) string {
	t.Helper()
	var sb strings.Builder
	for _, args := range [][]string{
		{"limit", "time-zone", "get", "--tenant", tenantID}, {"metadata-keys", "get", "--tenant", tenantID}, {"model-restriction", "get", "--tenant", tenantID},
		{"role", "list", "--tenant", tenantID}, {"service-account", "list", "--tenant", tenantID}, {"limit", "list", "--tenant", tenantID},
		{"price-list", "list", "--tenant", tenantID}, {"price-list", "currency", "get", "--tenant", tenantID},
	} {
		out := p.mustCLI(t, admin, append(args, "-o", "json")...)
		sb.WriteString(strings.ReplaceAll(strings.Join(args, " "), tenantID, "TENANT") + "\n" + out + "\n")
	}
	return sb.String()
}

// stateWithoutIDs removes what differs between two tenants that hold the same configuration: the
// identifiers and the times the server made.
func stateWithoutIDs(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		k := strings.TrimSpace(l)
		if strings.HasPrefix(k, `"id"`) || strings.HasPrefix(k, `"tenant_id"`) || strings.HasPrefix(k, `"created_at"`) || strings.HasPrefix(k, `"updated_at"`) ||
			strings.HasPrefix(k, `"subject_id"`) || strings.HasPrefix(k, `"model_id"`) || strings.HasPrefix(k, `"provider_id"`) || strings.HasPrefix(k, `"as_of"`) {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func heads(t *testing.T, p *proc) string {
	t.Helper()
	rows, err := p.db.Super(t).Query(ctxT(t), `SELECT 'tenant:' || tenant_id::text, seq::text, encode(hash, 'hex') FROM pandokos.audit_chain UNION ALL
		SELECT 'platform', seq::text, encode(hash, 'hex') FROM pandokos.platform_audit_chain ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a, b, c string
		if err := rows.Scan(&a, &b, &c); err != nil {
			t.Fatal(err)
		}
		out = append(out, a+" "+b+" "+c)
	}
	return strings.Join(out, "\n")
}

// TestExportApplyRoundTrip is T-971, the exit criterion of declarative configuration: a tenant
// configured through the CLI and a platform with a catalogue; pandokos export and then pandokos apply
// of the file say 0 changes and write no audit row; the state of the API is byte for byte what it was;
// the document of the tenant applied to a second, empty tenant creates it, and the export of the
// second equals the first.
func TestExportApplyRoundTrip(t *testing.T) {
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", up: up})
	boot := p.bootstrap(t)
	p.prepareUpstream(t)
	ops := filepath.Join(t.TempDir(), "ops.key")
	p.mustCLI(t, boot, "platform-key", "create", "--key-out", ops)
	idA, adminA := p.newTenant(t, ops, "declared-a")
	idB, adminB := p.newTenant(t, ops, "declared-b")
	model := "openai/" + fakeprovider.ModelBasic

	// The platform: a provider with an endpoint, two models, a grant, an override, a ceiling.
	p.mustCLI(t, ops, "provider", "create", "--family", "openai", "--display-name", "Declared", "declared")
	p.mustCLI(t, ops, "endpoint", "create", "--base-url", "https://declared.example.test", "declared", "main")
	p.mustCLI(t, ops, "model", "create", "--availability", "selected", "declared/model-one")
	p.mustCLI(t, ops, "model", "create", "--availability", "all", "declared/model-two")
	p.mustCLI(t, ops, "model", "grant", "--tenant", idA, "declared/model-one")
	p.mustCLI(t, ops, "price", "override", "--currency", "USD", "--rate", "input=3", "--rate", "output=6", model)
	p.mustCLI(t, ops, "platform-limit", "create", "--for-tenant", idA, "--metric", "tokens_per_day", "--limit", "9000000")

	// The tenant: zone, metadata keys, a restriction, a custom role, two service accounts with roles, rules on the tenant, a
	// service account and a key, a price list of two versions, the active currency.
	p.mustCLI(t, adminA, "limit", "time-zone", "set", "--tenant", idA, "Europe/Rome")
	p.mustCLI(t, adminA, "metadata-keys", "set", "--tenant", idA, "team", "project")
	p.mustCLI(t, adminA, "model-restriction", "set", "--tenant", idA, "--model", model, "--model", "openai/"+fakeprovider.ModelDetails)
	p.mustCLI(t, adminA, "role", "create", "--tenant", idA, "--permission", "usage:read", "--permission", "limit:read", "--description", "reads", "readers")
	p.mustCLI(t, adminA, "service-account", "create", "--tenant", idA, "--role", "usage-viewer", "--role", "readers", "billing")
	p.mustCLI(t, adminA, "service-account", "create", "--tenant", idA, "--role", "auditor", "reports")
	// A rule on a key is not part of this scenario: the key ID is the key's own, and a second tenant cannot hold it.
	// The rules are created in the order of the document's keys (a service account before the tenant), which is the
	// order apply creates them in: the list of a tenant is in the order of creation.
	p.mustCLI(t, adminA, "limit", "create", "--tenant", idA, "--subject", "service-account", "--service-account", "billing", "--metric", "tokens_per_day", "--limit", "50000", "--soft", "--threshold", "80")
	p.mustCLI(t, adminA, "limit", "create", "--tenant", idA, "--metric", "requests_per_day", "--limit", "1000")
	// Both versions start later: apply never adds a version in the past, so a version that started when it
	// was set could not be created in the second tenant.
	soon := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	p.mustCLI(t, adminA, "price-list", "set", "--tenant", idA, "--currency", "EUR", "--model", model, "--rate", "input=4", "--rate", "output=8", "--valid-from", soon)
	future := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	p.mustCLI(t, adminA, "price-list", "set", "--tenant", idA, "--currency", "EUR", "--model", model, "--rate", "input=5", "--rate", "output=9", "--valid-from", future)
	p.mustCLI(t, adminA, "price-list", "currency", "set", "--tenant", idA, "EUR")

	dir := t.TempDir()
	tenantDoc, platformDoc := filepath.Join(dir, "tenant.yaml"), filepath.Join(dir, "platform.yaml")
	p.mustCLI(t, adminA, "export", "--tenant", idA, "--out", tenantDoc)
	p.mustCLI(t, ops, "export", "--platform", "--out", platformDoc)
	stateBefore := p.stateOf(t, adminA, idA)
	platformBefore := p.mustCLI(t, ops, "model", "list", "-o", "json") + p.mustCLI(t, ops, "platform-limit", "list", "-o", "json")
	headsBefore := heads(t, p)

	out := p.mustCLI(t, adminA, "apply", "--tenant", idA, "--file", tenantDoc)
	if !strings.Contains(out, "0 changes") {
		t.Errorf("apply of the tenant's export:\n%s\nwant 0 changes", out)
	}
	out = p.mustCLI(t, ops, "apply", "--platform", "--file", platformDoc)
	if !strings.Contains(out, "0 changes") {
		t.Errorf("apply of the platform's export:\n%s\nwant 0 changes", out)
	}
	if after := heads(t, p); after != headsBefore {
		t.Errorf("an apply that changes nothing wrote audit rows:\n%s\n%s", headsBefore, after)
	}
	if after := p.stateOf(t, adminA, idA); after != stateBefore {
		t.Errorf("the state of the tenant changed:\n%s\n%s", stateBefore, after)
	}
	if after := p.mustCLI(t, ops, "model", "list", "-o", "json") + p.mustCLI(t, ops, "platform-limit", "list", "-o", "json"); after != platformBefore {
		t.Error("the state of the platform changed")
	}

	// The document of the tenant creates the second, empty tenant.
	out = p.mustCLI(t, adminB, "apply", "--tenant", idB, "--file", tenantDoc)
	if strings.Contains(out, "0 changes") || !strings.Contains(out, "create ") {
		t.Errorf("apply to an empty tenant:\n%s\nwant it to create the resources", out)
	}
	secondDoc := filepath.Join(dir, "second.yaml")
	p.mustCLI(t, adminB, "export", "--tenant", idB, "--out", secondDoc)
	first, _ := os.ReadFile(tenantDoc)
	second, _ := os.ReadFile(secondDoc)
	if string(first) != string(second) {
		t.Errorf("the export of the second tenant differs from the first's:\n%s\n---\n%s", second, first)
	}
	if again := p.mustCLI(t, adminB, "apply", "--tenant", idB, "--file", tenantDoc); !strings.Contains(again, "0 changes") {
		t.Errorf("a second apply to the second tenant:\n%s", again)
	}
	if a, b := stateWithoutIDs(p.stateOf(t, adminA, idA)), stateWithoutIDs(p.stateOf(t, adminB, idB)); a != b {
		t.Errorf("the two tenants differ although they were configured by the same document:\n%s\n---\n%s", a, b)
	}
	_ = json.Valid
	_ = fmt.Sprint
}
