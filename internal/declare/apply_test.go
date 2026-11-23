package declare_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/cli"
	"github.com/pandokos/pandokos/internal/declare"
	"github.com/pandokos/pandokos/test/testutil"
)

// runCLI runs a command of the command line against the fake and returns its exit code and output.
func runCLI(t *testing.T, f *fakeAPI, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	keyFile := testutil.WriteSecret(t, "key", testutil.NewKey(t))
	environ := []string{"PANDOKOS_CLI_SERVER=" + f.srv.URL, "PANDOKOS_CLI_KEY_FILE=" + keyFile}
	var out, errb bytes.Buffer
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code = cli.Run(c, args, &out, &errb, environ)
	return code, out.String(), errb.String()
}

func writeDoc(t *testing.T, doc string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "doc.yaml")
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func actions(p declare.Plan) []string {
	var out []string
	for _, c := range p.Changes {
		out = append(out, c.Action+" "+c.Resource)
	}
	return out
}

func has(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

// TestApplyPlan is T-967: against a fake server a document is compared with the state by natural key;
// what is absent is created, a mutable field that differs is updated, an immutable one is a
// conflict, versions are only added, what is not in the document is reported and never deleted, a
// dry run makes no write, and the writes come in the order of section 5.12.
func TestApplyPlan(t *testing.T) {
	tenant := declare.Target{Tenant: tidAcme}
	platform := declare.Target{Platform: true}
	plan := func(t *testing.T, f *fakeAPI, target declare.Target, doc *declare.Document) declare.Plan {
		t.Helper()
		p, err := declare.Diff(ctx(t), f.client(), target, doc)
		if err != nil {
			t.Fatalf("Diff: %v", err)
		}
		return p
	}

	t.Run("create, update and not in document", func(t *testing.T) {
		f := newFake(t, tenantState())
		doc := wantTenant()
		doc.ServiceAccounts = append(doc.ServiceAccounts, declare.ServiceAccount{Name: "new-sa", Roles: []string{"inference"}})
		doc.Roles[0].Description = "Reads usage, limits and prices"
		doc.Roles = append(doc.Roles, declare.Role{Name: "auditors-plus", Permissions: []string{"audit:read"}})
		doc.ServiceAccounts = doc.ServiceAccounts[:2:2] // reports and billing stay; new-sa below
		doc.ServiceAccounts = append(doc.ServiceAccounts, declare.ServiceAccount{Name: "new-sa", Roles: []string{"inference"}})
		// "reports" is dropped from the document: it exists but is not in it.
		doc.ServiceAccounts = []declare.ServiceAccount{doc.ServiceAccounts[0], doc.ServiceAccounts[2]}
		p := plan(t, f, tenant, doc)
		got := actions(p)
		for _, want := range []string{"create service_account new-sa", "create role auditors-plus", "update role billing-readers", "not in document service_account reports"} {
			if !has(got, want) {
				t.Errorf("the plan has no %q: %v", want, got)
			}
		}
		if len(p.Changes) != 4 {
			t.Errorf("the plan has %d changes, want 4: %v", len(p.Changes), got)
		}
		if n := len(f.writeCalls()); n != 0 {
			t.Fatalf("planning made %d write calls (a dry run makes none)", n)
		}
		res, err := p.Apply(ctx(t), f.client())
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		writes := f.writeCalls()
		var paths []string
		for _, w := range writes {
			paths = append(paths, w.Method+" "+w.Path)
		}
		for _, want := range []string{"POST /v1/tenants/" + tidAcme + "/service-accounts", "POST /v1/tenants/" + tidAcme + "/roles", "PATCH /v1/tenants/" + tidAcme + "/roles/billing-readers"} {
			if !has(paths, want) {
				t.Errorf("no write %q in %v", want, paths)
			}
		}
		for _, w := range writes {
			if w.Method == "DELETE" || strings.Contains(w.Path, "/disable") {
				t.Errorf("the application deleted or disabled something: %s %s", w.Method, w.Path)
			}
		}
		if len(writes) != 3 || res.Changes != 3 || res.LeftAlone != 1 || res.Conflicts != 0 {
			t.Errorf("%d writes, result %+v, want 3 changes and 1 left alone", len(writes), res)
		}
		if !has(res.Lines, "create service_account new-sa") || !has(res.Lines, "not in document service_account reports") {
			t.Errorf("the lines: %v", res.Lines)
		}
	})

	t.Run("an immutable field that differs is a conflict", func(t *testing.T) {
		f := newFake(t, platformState())
		doc := wantPlatform()
		doc.Providers[0].APIFamily = "anthropic" // the family of a provider is not changed
		p := plan(t, f, platform, doc)
		if got := actions(p); len(got) != 1 || !strings.HasPrefix(got[0], "conflict provider openai") {
			t.Fatalf("the plan: %v, want one conflict on provider openai", got)
		}
		res, err := p.Apply(ctx(t), f.client())
		if err != nil || len(f.writeCalls()) != 0 || res.Conflicts != 1 || res.Changes != 0 {
			t.Errorf("Apply: %+v %v, %d writes: a conflict is reported, not changed", res, err, len(f.writeCalls()))
		}
	})

	t.Run("versions are only added", func(t *testing.T) {
		f := newFake(t, tenantState())
		doc := wantTenant()
		future := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
		versions := doc.PriceLists[0].Versions
		changed := versions[1]
		changed.Rates = []declare.Rate{{Category: "input", Threshold: 0, Rate: "9.99"}, {Category: "output", Threshold: 0, Rate: "11.00"}}
		doc.PriceLists[0].Versions = []declare.PriceVersion{
			versions[0], // equal: nothing
			changed,     // exists with other rates: a conflict
			{Provider: "openai", Model: "gpt-4o", ValidFrom: future, Rates: []declare.Rate{{Category: "input", Threshold: 0, Rate: "3.00"}}},                 // absent, in the future: created
			{Provider: "openai", Model: "gpt-4o", ValidFrom: "2025-01-01T00:00:00Z", Rates: []declare.Rate{{Category: "input", Threshold: 0, Rate: "1.00"}}}, // absent, in the past: skipped
		}
		p := plan(t, f, tenant, doc)
		got := actions(p)
		if len(got) != 3 || !has(got, "create price") || !has(got, "conflict price") || !has(got, "skipped price") {
			t.Fatalf("the plan: %v, want a create, a conflict and a skipped, and nothing for the equal version", got)
		}
		for _, c := range p.Changes {
			if c.Action == "skipped" && !strings.Contains(c.Detail+c.Resource, "in the past") {
				t.Errorf("the skipped version does not say why: %+v", c)
			}
		}
		if _, err := p.Apply(ctx(t), f.client()); err != nil {
			t.Fatal(err)
		}
		writes := f.writeCalls()
		if len(writes) != 1 || writes[0].Method != "POST" || !strings.HasSuffix(writes[0].Path, "/prices") || !strings.Contains(writes[0].Body, future) {
			t.Errorf("writes: %+v, want one POST of the future version", writes)
		}
	})

	t.Run("the order of section 5.12", func(t *testing.T) {
		// A tenant with nothing in it: the whole document is created.
		f := newFake(t, map[string]any{})
		doc := wantTenant()
		doc.PriceLists[0].Versions = doc.PriceLists[0].Versions[:0]
		doc.PriceLists[0].Versions = []declare.PriceVersion{{Provider: "openai", Model: "gpt-4o", ValidFrom: time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second).Format(time.RFC3339),
			Rates: []declare.Rate{{Category: "input", Threshold: 0, Rate: "3.00"}}}}
		p := plan(t, f, tenant, doc)
		if _, err := p.Apply(ctx(t), f.client()); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		position := func(sub string) int {
			for i, w := range f.writeCalls() {
				if strings.Contains(w.Path, sub) {
					return i
				}
			}
			return -1
		}
		order := []struct{ name, sub string }{
			{"settings: the time zone", "/time-zone"}, {"settings: the metadata keys", "/metadata-keys"}, {"custom roles", "/roles"},
			{"service accounts", "/service-accounts"}, {"the model restriction", "/model-restriction"}, {"limits", "/limits"}, {"price lists", "/prices"}, {"the active currency", "/active-currency"},
		}
		prev := -1
		for _, o := range order {
			at := position(o.sub)
			if at < 0 {
				t.Errorf("no write for %s (%s): %v", o.name, o.sub, f.writeCalls())
				continue
			}
			if at <= prev {
				t.Errorf("%s is written at %d, after the step before it (%d): the order of section 5.12 is broken", o.name, at, prev)
			}
			prev = at
		}
		// The platform: providers, endpoints, models, grants, overrides, ceilings.
		g := newFake(t, map[string]any{"/v1/tenants": platformState()["/v1/tenants"]})
		pd := wantPlatform()
		pd.PriceOverrides[0].ValidFrom = time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second).Format(time.RFC3339) // an override is only added from now on
		pp := plan(t, g, platform, pd)
		if _, err := pp.Apply(ctx(t), g.client()); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		pos := func(sub string) int {
			for i, w := range g.writeCalls() {
				if strings.HasSuffix(w.Path, sub) || strings.Contains(w.Path, sub) {
					return i
				}
			}
			return -1
		}
		prev = -1
		for _, o := range []struct{ name, sub string }{{"providers", "/v1/providers"}, {"endpoints", "/endpoints"}, {"models", "/v1/models"}, {"grants", "/grants"}, {"overrides", "/prices"}, {"ceilings", "/v1/platform-limits"}} {
			at := pos(o.sub)
			if o.sub == "/v1/providers" { // the first write is the provider itself
				at = 0
				if len(g.writeCalls()) == 0 || g.writeCalls()[0].Path != "/v1/providers" {
					at = -1
				}
			}
			if o.sub == "/v1/models" { // a model is created at /v1/models, not under /grants or /prices
				at = -1
				for i, w := range g.writeCalls() {
					if w.Path == "/v1/models" {
						at = i
						break
					}
				}
			}
			if at < 0 {
				t.Errorf("no write for %s: %v", o.name, g.writeCalls())
				continue
			}
			if at <= prev {
				t.Errorf("%s is written at %d, after the step before it (%d)", o.name, at, prev)
			}
			prev = at
		}
	})

	t.Run("a failed call stops the rest", func(t *testing.T) {
		f := newFake(t, map[string]any{})
		f.failWrite = 2
		p := plan(t, f, tenant, wantTenant())
		res, err := p.Apply(ctx(t), f.client())
		if err == nil {
			t.Fatal("Apply succeeded although a call failed")
		}
		if n := len(f.writeCalls()); n != 2 {
			t.Errorf("%d writes after the failure at the second, want 2: the rest is not attempted", n)
		}
		_ = res
	})
}

// TestApplyRejectsInvalidDocuments is T-968: a document that is not valid is refused with a usage
// error (exit 2) that names what is wrong, before any call that changes something.
func TestApplyRejectsInvalidDocuments(t *testing.T) {
	valid := "pandokos: v1\nkind: tenant\nsettings:\n  time_zone: Europe/Rome\n"
	for name, c := range map[string]struct {
		doc  string
		want string // what the message names
		args []string
	}{
		"an unknown member":            {valid + "colour: blue\n", "colour", nil},
		"an unknown member in an item": {"pandokos: v1\nkind: tenant\nroles:\n  - name: r\n    permissions: [usage:read]\n    shade: x\n", "shade", nil},
		"another version":              {"pandokos: v2\nkind: tenant\n", "v2", nil},
		"no version":                   {"kind: tenant\n", "pandokos", nil},
		"a kind against the flag":      {"pandokos: v1\nkind: platform\n", "platform", nil},
		"no kind":                      {"pandokos: v1\n", "kind", nil},
		"a duplicate natural key":      {"pandokos: v1\nkind: tenant\nroles:\n  - name: dup\n    permissions: [usage:read]\n  - name: dup\n    permissions: [limit:read]\n", "dup", nil},
		"a duplicate service account":  {"pandokos: v1\nkind: tenant\nservice_accounts:\n  - name: twice\n  - name: twice\n", "twice", nil},
		"an anchor and an alias":       {"pandokos: v1\nkind: tenant\nroles:\n  - &r\n    name: one\n    permissions: [usage:read]\n  - *r\n", "alias", nil},
		"an anchor alone":              {"pandokos: v1\nkind: tenant\nsettings: &s\n  time_zone: Europe/Rome\n", "anchor", nil},
		"a secret member":              {"pandokos: v1\nkind: tenant\nservice_accounts:\n  - name: x\n    secret: hunter2hunter2\n", "secret", nil},
		"a password member":            {"pandokos: v1\nkind: tenant\nroles:\n  - name: x\n    password: hunter2hunter2\n    permissions: []\n", "password", nil},
		"a token member":               {"pandokos: v1\nkind: tenant\nsettings:\n  token: abc\n", "token", nil},
		"not YAML":                     {"pandokos: [v1\n", "", nil},
		"not a mapping":                {"- a\n- b\n", "", nil},
		"an empty document":            {"", "", nil},
		"a document over 4 MiB":        {valid + "# " + strings.Repeat("x", 4<<20) + "\n", "4 MiB", nil},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, tenantState())
			code, out, errs := runCLI(t, f, "apply", "--file", writeDoc(t, c.doc), "--tenant", tidAcme)
			if strings.Contains(errs, "sub-command") {
				t.Fatalf("apply is not a command of the binary: %q", errs)
			}
			if code != 2 {
				t.Fatalf("exit %d (stdout %q, stderr %q), want 2", code, out, errs)
			}
			if !strings.Contains(strings.ToLower(errs), strings.ToLower(c.want)) {
				t.Errorf("the message %q does not name %q", errs, c.want)
			}
			if n := len(f.writeCalls()); n != 0 {
				t.Errorf("%d write calls for an invalid document", n)
			}
			if strings.Contains(errs, "hunter2hunter2") || strings.Contains(out, "hunter2hunter2") {
				t.Error("the message repeats the value of a secret-looking member")
			}
		})
	}
	t.Run("a kind against the flag, the other way", func(t *testing.T) {
		f := newFake(t, platformState())
		code, _, errs := runCLI(t, f, "apply", "--file", writeDoc(t, "pandokos: v1\nkind: tenant\n"), "--platform")
		if code != 2 || !strings.Contains(errs, "tenant") || strings.Contains(errs, "sub-command") {
			t.Errorf("exit %d, stderr %q", code, errs)
		}
	})
	t.Run("neither --platform nor --tenant, or both", func(t *testing.T) {
		f := newFake(t, tenantState())
		doc := writeDoc(t, valid)
		for _, args := range [][]string{{"apply", "--file", doc}, {"apply", "--file", doc, "--platform", "--tenant", tidAcme}, {"apply", "--tenant", tidAcme}} {
			if code, _, errs := runCLI(t, f, args...); code != 2 || errs == "" || strings.Contains(errs, "sub-command") {
				t.Errorf("%v: exit %d (%s), want a usage error", args, code, errs)
			}
		}
	})
}

// TestApplyOfExportIsNoOp is T-969, the unit half of the exit criterion: export a state, apply the
// document to the same state, and nothing is written; the command prints 0 changes and exits 0.
func TestApplyOfExportIsNoOp(t *testing.T) {
	for _, c := range []struct {
		name   string
		target declare.Target
		state  func() map[string]any
		flags  []string
	}{
		{"tenant", declare.Target{Tenant: tidAcme}, tenantState, []string{"--tenant", tidAcme}},
		{"platform", declare.Target{Platform: true}, platformState, []string{"--platform"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t, c.state())
			doc, err := declare.Export(ctx(t), f.client(), c.target)
			if err != nil {
				t.Fatalf("Export: %v", err)
			}
			p, err := declare.Diff(ctx(t), f.client(), c.target, doc)
			if err != nil {
				t.Fatalf("Diff: %v", err)
			}
			if got := actions(p); len(got) != 0 {
				t.Errorf("the plan for the document just exported is %v, want empty", got)
			}
			if res, err := p.Apply(ctx(t), f.client()); err != nil || res.Changes != 0 {
				t.Errorf("Apply: %+v %v", res, err)
			}

			// The same through the commands.
			file := filepath.Join(t.TempDir(), "doc.yaml")
			if code, _, errs := runCLI(t, f, append([]string{"export", "--out", file}, c.flags...)...); code != 0 {
				t.Fatalf("export: exit %d: %s", code, errs)
			}
			b, err := os.ReadFile(file)
			if err != nil || string(b) != string(marshal(t, doc)) && !strings.HasPrefix(string(b), string(marshal(t, doc))) {
				t.Errorf("the file differs from the document (%v):\n%s", err, b)
			}
			code, out, errs := runCLI(t, f, append([]string{"apply", "--file", file}, c.flags...)...)
			if code != 0 || !strings.Contains(out, "0 changes") {
				t.Errorf("apply of the export: exit %d, stdout %q, stderr %q, want exit 0 and 0 changes", code, out, errs)
			}
			if n := len(f.writeCalls()); n != 0 {
				t.Errorf("%d write calls: %v", n, f.writeCalls())
			}
			code, out, _ = runCLI(t, f, append([]string{"apply", "--dry-run", "--file", file}, c.flags...)...)
			if code != 0 || !strings.Contains(out, "0 changes") {
				t.Errorf("--dry-run: exit %d, stdout %q", code, out)
			}
		})
	}
}

// TestApplyExitCodes is part of T-967: exit 0 when every planned change succeeded (conflicts and
// what is left alone are reported, not failures), 1 when a call failed, 2 for a usage error.
func TestApplyExitCodes(t *testing.T) {
	tenantFlags := []string{"--tenant", tidAcme}
	doc := wantTenant()
	doc.ServiceAccounts = append(doc.ServiceAccounts, declare.ServiceAccount{Name: "new-sa", Roles: []string{"inference"}})
	file := writeDoc(t, string(marshal(t, doc)))

	f := newFake(t, tenantState())
	code, out, errs := runCLI(t, f, append([]string{"apply", "--file", file}, tenantFlags...)...)
	if code != 0 || !strings.Contains(out, "create service_account new-sa") || !strings.Contains(out, "1 changes, 0 conflicts") && !strings.Contains(out, "1 change") {
		t.Errorf("a change that works: exit %d, stdout %q, stderr %q", code, out, errs)
	}
	if n := len(f.writeCalls()); n != 1 {
		t.Errorf("%d writes, want 1", n)
	}

	// Conflicts and left alone: reported, exit 0.
	p := wantPlatform()
	p.Providers[0].APIFamily = "anthropic"
	g := newFake(t, platformState())
	code, out, _ = runCLI(t, g, "apply", "--platform", "--file", writeDoc(t, string(marshal(t, p))))
	if code != 0 || !strings.Contains(out, "conflict provider openai") || !strings.Contains(out, "1 conflicts") && !strings.Contains(out, "1 conflict") {
		t.Errorf("a conflict: exit %d, stdout %q, want it reported with exit 0", code, out)
	}

	// A call that fails: exit 1, the rest is not attempted.
	h := newFake(t, tenantState())
	h.failWrite = 1
	if code, _, errs := runCLI(t, h, append([]string{"apply", "--file", file}, tenantFlags...)...); code != 1 || errs == "" {
		t.Errorf("a failed call: exit %d (%s), want 1", code, errs)
	}
	// A usage error: exit 2, no call.
	if code, _, _ := runCLI(t, newFake(t, tenantState()), "apply", "--file", file); code != 2 {
		t.Errorf("no target: exit %d, want 2", code)
	}
}
