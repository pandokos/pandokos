package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/fakeidp"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/testutil"
)

// stateOf returns what the API says about a tenant, as one text: the output of the commands that
// read every resource of a document, in JSON.
func (p *proc) stateOf(t *testing.T, admin, tenantID string) string {
	t.Helper()
	var sb strings.Builder
	for _, args := range [][]string{
		{"limit", "time-zone", "get", "--tenant", tenantID}, {"metadata-keys", "get", "--tenant", tenantID}, {"model-restriction", "get", "--tenant", tenantID},
		{"role", "list", "--tenant", tenantID}, {"service-account", "list", "--tenant", tenantID}, {"limit", "list", "--tenant", tenantID},
		{"price-list", "list", "--tenant", tenantID}, {"price-list", "currency", "get", "--tenant", tenantID}, {"subject-mapping", "list", "--tenant", tenantID},
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
			strings.HasPrefix(k, `"subject_id"`) || strings.HasPrefix(k, `"model_id"`) || strings.HasPrefix(k, `"provider_id"`) || strings.HasPrefix(k, `"as_of"`) ||
			strings.HasPrefix(k, `"service_account_id"`) {
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
	t.Parallel()
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
	// Sprint 12.3 (the test is changed): the tenant has a platform-level issuer that lists both tenants, a mapping on
	// it, and a rule on the mapping. A rule on a key is not part of this scenario: the key ID is the key's own, and a
	// second tenant cannot hold it. A token issuer of the tenant is not either: its issuer and audience are unique in
	// the installation (T-1126 round-trips it between two installations).
	shared := reID.FindStringSubmatch(p.mustCLI(t, ops, "platform-issuer", "create", "--name", "shared", "--issuer", "https://shared.example.test", "--audience", "pandokos-declared", "--tenant-claim", "org"))[1]
	p.mustCLI(t, ops, "platform-issuer", "add-tenant", shared, idA)
	p.mustCLI(t, ops, "platform-issuer", "add-tenant", shared, idB)
	mapping := reID.FindStringSubmatch(p.mustCLI(t, adminA, "subject-mapping", "create", "--tenant", idA, "--issuer", shared, "--platform", "--subject", "ci", "--service-account", "billing"))[1]
	// The rules are created in the order of the document's keys (a rule on a mapping, a service account, then the
	// tenant), which is the order apply creates them in: the list of a tenant is in the order of creation.
	p.mustCLI(t, adminA, "limit", "create", "--tenant", idA, "--subject", "key", "--key", mapping, "--metric", "requests_per_day", "--limit", "7")
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

	// A tenant restricted to no key: the export says so, and the apply to another tenant restricts it too.
	p.mustCLI(t, adminA, "metadata-keys", "set", "--tenant", idA)
	emptyDoc := filepath.Join(dir, "empty.yaml")
	p.mustCLI(t, adminA, "export", "--tenant", idA, "--out", emptyDoc)
	if b, _ := os.ReadFile(emptyDoc); !strings.Contains(string(b), "metadata_keys: []") {
		t.Errorf("the export of a tenant restricted to no key:\n%s", b)
	}
	if out := p.mustCLI(t, adminA, "apply", "--tenant", idA, "--file", emptyDoc); !strings.Contains(out, "0 changes") {
		t.Errorf("apply of the export of a tenant restricted to no key:\n%s\nwant 0 changes", out)
	}
	if out := p.mustCLI(t, adminB, "apply", "--tenant", idB, "--file", emptyDoc); !strings.Contains(out, "setting metadata_keys") {
		t.Errorf("apply to a tenant that accepts the keys:\n%s\nwant the empty policy set", out)
	}
	if a, b := stateWithoutIDs(p.stateOf(t, adminA, idA)), stateWithoutIDs(p.stateOf(t, adminB, idB)); a != b {
		t.Errorf("the empty policy was not carried over:\n%s\n---\n%s", a, b)
	}
	_ = json.Valid
	_ = fmt.Sprint
}

// TestExportApplyIssuersRoundTrip is T-1126: an installation that has what distinguishes it (a
// tenant issuer, a platform-level issuer that lists the tenant, a mapping of each, a rule on a
// mapping, an event destination, an external reference) is exported to documents and the documents
// are applied to a fresh installation that has the tenant under the same name. The exports of the two
// are equal; a token of the fake identity provider authenticates on the second as the mapped
// account; the destination on the second delivers an event that verifies with the secret that
// apply wrote to its file; a second apply plans nothing; and that secret is in no output or log.
func TestExportApplyIssuersRoundTrip(t *testing.T) {
	t.Parallel()
	idp := fakeidp.Start(t, fakeidp.Options{})
	up := fakeprovider.New(t)
	recv := newFaultReceiver(t, "/hook")
	o := opts{level: "info", up: up, events: fastEvents}
	one := startTokenProc(t, idp, o)
	two := startTokenProc(t, idp, o)

	// The first installation.
	a := one.tenant(t, "roundtrip")
	one.run(t, one.boot, "tenant", "update", "--external-reference", "crm-4711", a.id)
	tenantIssuer := one.registration(t, a, "corp", "pandokos-roundtrip")
	platformIssuer := one.platformRegistration(t, "shared", "pandokos-roundtrip-shared", "org")
	one.run(t, one.boot, "platform-issuer", "add-tenant", "--claim-value", "org-roundtrip", platformIssuer, a.id)
	mapCI := one.mapping(t, a, tenantIssuer, "ci", false)
	one.mapping(t, a, platformIssuer, "svc", true)
	one.run(t, a.admin, "limit", "create", "--tenant", a.id, "--subject", "key", "--key", mapCI, "--metric", "requests_per_day", "--limit", "100")
	newDestination(t, one.proc, a.admin, a.id, "hooks", recv.url("/hook"))

	dir := t.TempDir()
	platformDoc, tenantDoc := filepath.Join(dir, "platform.yaml"), filepath.Join(dir, "tenant.yaml")
	one.run(t, one.boot, "export", "--platform", "--out", platformDoc)
	one.run(t, a.admin, "export", "--tenant", a.id, "--out", tenantDoc)
	for _, want := range []string{"platform_issuers:", "name: shared", "claim_value: org-roundtrip", "tenants:", "external_reference: crm-4711"} {
		if b, _ := os.ReadFile(platformDoc); !strings.Contains(string(b), want) {
			t.Errorf("the platform document lacks %q:\n%s", want, b)
		}
	}
	for _, want := range []string{"token_issuers:", "name: corp", "subject_mappings:", "subject: ci", "event_destinations:", "name: hooks", "mapping:"} {
		if b, _ := os.ReadFile(tenantDoc); !strings.Contains(string(b), want) {
			t.Errorf("the tenant document lacks %q:\n%s", want, b)
		}
	}

	// The second installation: the tenant of the same name, then the documents.
	b := two.tenant(t, "roundtrip")
	secrets := filepath.Join(t.TempDir(), "secrets")
	if err := os.Mkdir(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	out := two.run(t, two.boot, "apply", "--platform", "--file", platformDoc)
	out += two.run(t, b.admin, "apply", "--tenant", b.id, "--file", tenantDoc, "--secrets-out", secrets)
	if strings.Contains(out, "0 changes") && !strings.Contains(out, "create ") {
		t.Errorf("the documents changed nothing on the second installation:\n%s", out)
	}

	// The exports are equal.
	for name, doc := range map[string]string{"platform": platformDoc, "tenant": tenantDoc} {
		second := filepath.Join(dir, "second-"+name+".yaml")
		if name == "platform" {
			two.run(t, two.boot, "export", "--platform", "--out", second)
		} else {
			two.run(t, b.admin, "export", "--tenant", b.id, "--out", second)
		}
		first, _ := os.ReadFile(doc)
		got, _ := os.ReadFile(second)
		if string(first) != string(got) {
			t.Errorf("the %s export of the second installation differs from the first's:\n%s\n---\n%s", name, got, first)
		}
	}

	// A token of the fake identity provider authenticates as the mapped account, by both registrations.
	tenantToken := two.sign("ES256", "ci", "pandokos-roundtrip")
	platformToken := two.sign("ES256", "svc", "pandokos-roundtrip-shared", func(cl fakeidp.Claims) fakeidp.Claims { return cl.With("org", "org-roundtrip") })
	for name, token := range map[string]string{"the tenant's registration": tenantToken, "the platform-level one": platformToken} {
		if status, text := two.openai(t, token, false); status != 200 {
			t.Errorf("a token of %s on the second installation: %d\n%s", name, status, text)
		}
	}
	for _, row := range waitRows(t, two.db, b.id, 2) {
		if row["principal_kind"] != "token" || row["principal_id"] != b.sa {
			t.Errorf("usage row of the second installation: %v", row)
		}
	}

	// The destination delivers, and the event verifies with the secret that apply wrote to the file.
	file := filepath.Join(secrets, "event-destination-hooks")
	secretBytes, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("apply wrote no secret file: %v", err)
	}
	secret := strings.TrimSpace(string(secretBytes))
	recv.setSecret(secret)
	testutil.Eventually(t, 60*time.Second, "an event to arrive and verify with the secret of the file", func() bool { return recv.store.size() > 0 })

	// A second apply plans nothing, and the secret is in no output or log.
	for _, args := range [][]string{{"apply", "--platform", "--file", platformDoc}, {"apply", "--tenant", b.id, "--file", tenantDoc, "--secrets-out", secrets}} {
		key := two.boot
		if args[1] == "--tenant" {
			key = b.admin
		}
		if again := two.run(t, key, args...); !strings.Contains(again, "0 changes") {
			t.Errorf("a second apply:\n%s\nwant 0 changes", again)
		}
	}
	for _, p := range []*tokenProc{one, two} {
		_ = p.stop(t)
	}
	for _, text := range append(append([]string{}, one.cliOut...), two.cliOut...) {
		testutil.AssertAbsent(t, "an output of the command line", text, secret)
	}
	for name, text := range map[string]string{"log of the first installation": one.stdout.String() + one.stderr.String(), "log of the second": two.stdout.String() + two.stderr.String()} {
		testutil.AssertAbsent(t, name, text, secret)
	}
}

// TestExamplePlatformDocument is T-1128: examples/declarative/platform.yaml applies to a fresh
// installation; a tenant with no rule of its own is then limited to the default ceiling it
// carries; a second apply plans nothing.
func TestExamplePlatformDocument(t *testing.T) {
	t.Parallel()
	p := startBinary(t, opts{level: "info"})
	boot := p.bootstrap(t)
	example := filepath.Join(testutil.RepoRoot(t), "examples", "declarative", "platform.yaml")

	out := p.mustCLI(t, boot, "apply", "--platform", "--file", example)
	if !strings.Contains(out, "create ") {
		t.Fatalf("the example document created nothing on a fresh installation:\n%s", out)
	}
	id, admin := p.newTenant(t, boot, "ceiling")
	list := cliJSON(t, p, admin, "limit", "platform", "--tenant", id)
	var found bool
	for _, it := range list["items"].([]any) {
		m, _ := it.(map[string]any)
		if m["metric"] == "concurrent_requests" && m["limit"] == float64(256) && m["enforce"] == true {
			found = true
		}
	}
	if !found {
		t.Errorf("a tenant with no rule of its own does not see the default ceiling of 256 concurrent requests: %v", list)
	}
	if again := p.mustCLI(t, boot, "apply", "--platform", "--file", example); !strings.Contains(again, "0 changes") {
		t.Errorf("a second apply of the example:\n%s\nwant 0 changes", again)
	}
}
