package declare_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
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

// TestEmptyMetadataPolicy is part of T-966 and T-967: a tenant restricted to no key (accepts no
// caller metadata) is not the same as a tenant with no restriction. The export says `metadata_keys: []`,
// a document that says `[]` sets the empty policy, and a document that does not mention the member
// leaves the policy alone.
func TestEmptyMetadataPolicy(t *testing.T) {
	tenant := declare.Target{Tenant: tidAcme}
	// what the plan would write: what the state holds beyond the document is only reported.
	writes := func(p declare.Plan) []string {
		var out []string
		for _, l := range actions(p) {
			if !strings.HasPrefix(l, "not in document") {
				out = append(out, l)
			}
		}
		return out
	}
	emptyPolicy := func(t *testing.T) *fakeAPI {
		st := tenantState()
		st["/v1/tenants/"+tidAcme+"/metadata-keys"] = adminapi.MetadataKeys{Restricted: true, Keys: []string{}}
		return newFake(t, st)
	}
	withSettings := func(t *testing.T, settings string) *declare.Document {
		doc, err := declare.Parse([]byte("pandokos: " + declare.Version + "\nkind: tenant\n" + settings))
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}

	t.Run("a restriction to no key is exported", func(t *testing.T) {
		doc, err := declare.Export(ctx(t), emptyPolicy(t).client(), tenant)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(marshal(t, doc)); !strings.Contains(got, "metadata_keys: []") {
			t.Errorf("the export has no `metadata_keys: []`:\n%s", got)
		}
	})

	t.Run("a document that says [] sets the empty policy", func(t *testing.T) {
		f := newFake(t, tenantState()) // restricted to project and team
		p, err := declare.Diff(ctx(t), f.client(), tenant, withSettings(t, "settings:\n  metadata_keys: []\n"))
		if err != nil {
			t.Fatal(err)
		}
		if got := writes(p); len(got) != 1 || !has(got, "setting metadata_keys") {
			t.Fatalf("the plan: %v, want one setting of metadata_keys", got)
		}
		if _, err := p.Apply(ctx(t), f.client()); err != nil {
			t.Fatal(err)
		}
		if w := f.writeCalls(); len(w) != 1 || w[0].Method != "PUT" || !strings.Contains(w[0].Path, "/metadata-keys") || strings.Contains(w[0].Body, "project") {
			t.Errorf("writes: %+v, want one PUT of the empty list", w)
		}
	})

	t.Run("[] against an empty policy changes nothing", func(t *testing.T) {
		f := emptyPolicy(t)
		p, err := declare.Diff(ctx(t), f.client(), tenant, withSettings(t, "settings:\n  metadata_keys: []\n"))
		if err != nil {
			t.Fatal(err)
		}
		if got := writes(p); len(got) != 0 {
			t.Errorf("the plan: %v, want nothing", got)
		}
	})

	t.Run("a member that is absent leaves the policy alone", func(t *testing.T) {
		f := emptyPolicy(t)
		p, err := declare.Diff(ctx(t), f.client(), tenant, withSettings(t, "settings:\n  time_zone: Europe/Rome\n"))
		if err != nil {
			t.Fatal(err)
		}
		if got := writes(p); len(got) != 0 {
			t.Errorf("the plan: %v, want nothing", got)
		}
	})
}

// ---- sprint 12.3: issuers, mappings, destinations and the external reference

func diff(t *testing.T, f *fakeAPI, target declare.Target, doc *declare.Document, opts ...declare.Options) declare.Plan {
	t.Helper()
	p, err := declare.Diff(ctx(t), f.client(), target, doc, opts...)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	return p
}

// bodyKeys is the sorted member names of the JSON body of a write.
func bodyKeys(t *testing.T, w call) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(w.Body), &m); err != nil {
		t.Fatalf("the body of %s %s is not a JSON object: %q", w.Method, w.Path, w.Body)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// writePaths is "METHOD path" of every write.
func writePaths(f *fakeAPI) []string {
	var out []string
	for _, w := range f.writeCalls() {
		out = append(out, w.Method+" "+w.Path)
	}
	return out
}

func noDeletes(t *testing.T, f *fakeAPI) {
	t.Helper()
	for _, w := range f.writeCalls() {
		if w.Method == "DELETE" {
			t.Errorf("a plan of sprint 12.3 deleted something: %s %s", w.Method, w.Path)
		}
	}
}

// TestPlanIssuers is T-1121: the platform-level and the tenant token issuers are created, updated
// member by member, and never deleted; the tenants of a platform issuer are added, their claim value
// changed, a missing tenant is a conflict, and a tenant that is listed in the installation and not
// in the document is reported as not in the document.
func TestPlanIssuers(t *testing.T) {
	platform, tenant := declare.Target{Platform: true}, declare.Target{Tenant: tidAcme}

	t.Run("the export of the state plans nothing", func(t *testing.T) {
		for _, c := range []struct {
			target declare.Target
			state  map[string]any
			doc    *declare.Document
		}{{platform, platformStateFull(), wantPlatformFull()}, {tenant, tenantStateFull(), wantTenantFull()}} {
			f := newFake(t, c.state)
			if got := actions(diff(t, f, c.target, c.doc)); len(got) != 0 {
				t.Errorf("the plan of the document that was just exported is %v, want empty", got)
			}
		}
	})

	t.Run("a platform issuer is created with its tenants", func(t *testing.T) {
		f := newFake(t, platformStateFull())
		doc := wantPlatformFull()
		doc.PlatformIssuers = append(doc.PlatformIssuers, declare.PlatformIssuer{Name: "new-idp", Issuer: "https://new.example.test", Audience: "pandokos", TenantClaim: "org", Enabled: ptr(true),
			Tenants: []declare.IssuerTenant{{Tenant: "acme"}, {Tenant: "globex", ClaimValue: "gx"}}})
		p := diff(t, f, platform, doc)
		if got := actions(p); len(got) < 1 || !has(got, "create platform_issuer new-idp") {
			t.Fatalf("the plan: %v, want a create of platform_issuer new-idp", got)
		}
		if _, err := p.Apply(ctx(t), f.client()); err != nil {
			t.Fatal(err)
		}
		writes := f.writeCalls()
		if len(writes) != 3 || writes[0].Method != "POST" || writes[0].Path != "/v1/platform-issuers" {
			t.Fatalf("writes: %v, want a POST of the issuer and one PUT for each of its tenants", writePaths(f))
		}
		var made struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(writes[0].Resp), &made)
		for i, tid := range []string{tidAcme, tidGlobex} {
			w := writes[1+i]
			if w.Method != "PUT" || w.Path != "/v1/platform-issuers/"+made.ID+"/tenants/"+tid {
				t.Errorf("write %d is %s %s, want the PUT of tenant %s on the issuer just created", 1+i, w.Method, w.Path, tid)
			}
		}
		if !strings.Contains(writes[2].Body, `"claim_value":"gx"`) {
			t.Errorf("the claim value is not sent: %s", writes[2].Body)
		}
		noDeletes(t, f)
	})

	for _, c := range []struct {
		member string
		change func(i *declare.PlatformIssuer)
	}{
		{"description", func(i *declare.PlatformIssuer) { i.Description = "changed" }},
		{"issuer", func(i *declare.PlatformIssuer) { i.Issuer = "https://other.example.test" }},
		{"audience", func(i *declare.PlatformIssuer) { i.Audience = "other" }},
		{"jwks_uri", func(i *declare.PlatformIssuer) { i.JWKSURI = ptr("https://shared.example.test/other-keys") }},
		{"algorithms", func(i *declare.PlatformIssuer) { i.Algorithms = []string{"ES256"} }},
		{"subject_claim", func(i *declare.PlatformIssuer) { i.SubjectClaim = "email" }},
		{"tenant_claim", func(i *declare.PlatformIssuer) { i.TenantClaim = "tid" }},
		{"required_claims", func(i *declare.PlatformIssuer) { i.RequiredClaims = ptr(map[string]string{"azp": "other"}) }},
		{"enabled", func(i *declare.PlatformIssuer) { i.Enabled = ptr(false) }},
	} {
		t.Run("a platform issuer is updated: "+c.member, func(t *testing.T) {
			f := newFake(t, platformStateFull())
			doc := wantPlatformFull()
			c.change(&doc.PlatformIssuers[0])
			p := diff(t, f, platform, doc)
			if got := actions(p); len(got) != 1 || !strings.HasPrefix(got[0], "update platform_issuer shared-idp") {
				t.Fatalf("the plan: %v, want one update of platform_issuer shared-idp", got)
			}
			if _, err := p.Apply(ctx(t), f.client()); err != nil {
				t.Fatal(err)
			}
			w := f.writeCalls()
			if len(w) != 1 || w[0].Method != "PATCH" || w[0].Path != "/v1/platform-issuers/"+issuerPlatformID || !reflect.DeepEqual(bodyKeys(t, w[0]), []string{c.member}) {
				t.Errorf("writes: %+v, want one PATCH that carries %s and nothing else", w, c.member)
			}
		})
	}

	t.Run("a token issuer is created and updated", func(t *testing.T) {
		f := newFake(t, tenantStateFull())
		doc := wantTenantFull()
		doc.TokenIssuers[0].Audience = "pandokos-acme-2"
		doc.TokenIssuers = append(doc.TokenIssuers, declare.TokenIssuer{Name: "new-idp", Issuer: "https://new.example.test", Audience: "x", Enabled: ptr(true)})
		p := diff(t, f, tenant, doc)
		got := actions(p)
		if len(got) != 2 || !has(got, "create token_issuer new-idp") || !has(got, "update token_issuer corp-idp") {
			t.Fatalf("the plan: %v, want a create of new-idp and an update of corp-idp", got)
		}
		if _, err := p.Apply(ctx(t), f.client()); err != nil {
			t.Fatal(err)
		}
		paths := writePaths(f)
		if !has(paths, "POST /v1/tenants/"+tidAcme+"/token-issuers") || !has(paths, "PATCH /v1/tenants/"+tidAcme+"/token-issuers/"+issuerTenantID) {
			t.Errorf("writes: %v", paths)
		}
	})

	t.Run("tenants of an issuer: added, claim value changed, absent value is the default", func(t *testing.T) {
		st := platformStateFull()
		st["/v1/tenants"] = append(st["/v1/tenants"].([]any), adminapi.Tenant{ID: tidInitech, Name: "initech", State: adminapi.TenantStateActive, CreatedAt: t0, UpdatedAt: t0, StateChangedAt: t0})
		f := newFake(t, st)
		doc := wantPlatformFull()
		doc.PlatformIssuers[0].Tenants = []declare.IssuerTenant{
			{Tenant: "acme", ClaimValue: "acme-co"}, // the value changes from the tenant's ID
			{Tenant: "globex"},                      // absent: the default, the tenant's ID in the target: differs from "globex-co"
			{Tenant: "initech"},                     // added
		}
		p := diff(t, f, platform, doc)
		if got := actions(p); len(got) != 3 {
			t.Fatalf("the plan: %v, want three changes (acme, globex, initech)", got)
		}
		if _, err := p.Apply(ctx(t), f.client()); err != nil {
			t.Fatal(err)
		}
		prefix := "PUT /v1/platform-issuers/" + issuerPlatformID + "/tenants/"
		for _, tid := range []string{tidAcme, tidGlobex, tidInitech} {
			if !has(writePaths(f), prefix+tid) {
				t.Errorf("no write %q in %v", prefix+tid, writePaths(f))
			}
		}
		for _, w := range f.writeCalls() {
			switch {
			case strings.HasSuffix(w.Path, tidAcme) && !strings.Contains(w.Body, `"claim_value":"acme-co"`):
				t.Errorf("acme's claim value is not sent: %s", w.Body)
			case (strings.HasSuffix(w.Path, tidGlobex) || strings.HasSuffix(w.Path, tidInitech)) && strings.Contains(w.Body, "claim_value"):
				t.Errorf("an absent claim value is sent as a value: %s", w.Body)
			}
		}
		noDeletes(t, f)
	})

	t.Run("a tenant that does not exist is a conflict", func(t *testing.T) {
		f := newFake(t, platformStateFull())
		doc := wantPlatformFull()
		doc.PlatformIssuers[0].Tenants = append(doc.PlatformIssuers[0].Tenants, declare.IssuerTenant{Tenant: "nowhere"})
		p := diff(t, f, platform, doc)
		if got := actions(p); len(got) != 1 || !strings.HasPrefix(got[0], "conflict ") || !strings.Contains(got[0], "nowhere") {
			t.Fatalf("the plan: %v, want one conflict naming the tenant", got)
		}
		if res, err := p.Apply(ctx(t), f.client()); err != nil || res.Conflicts != 1 || len(f.writeCalls()) != 0 {
			t.Errorf("Apply: %+v %v with %d writes: a conflict is reported and nothing is written", res, err, len(f.writeCalls()))
		}
	})

	t.Run("a listed tenant that is not in the document, and an issuer that is not, are reported and kept", func(t *testing.T) {
		f := newFake(t, platformStateFull())
		doc := wantPlatformFull()
		doc.PlatformIssuers[0].Tenants = []declare.IssuerTenant{{Tenant: "acme"}} // globex stays listed in the installation
		p := diff(t, f, platform, doc)
		if got := actions(p); len(got) != 1 || !strings.HasPrefix(got[0], "not in document") || !strings.Contains(got[0], "globex") {
			t.Fatalf("the plan: %v, want one not in document naming globex", got)
		}
		doc.PlatformIssuers = nil
		tenantsGone := diff(t, newFake(t, platformStateFull()), platform, doc)
		if got := actions(tenantsGone); !has(got, "not in document platform_issuer shared-idp") {
			t.Errorf("the plan: %v, want the issuer reported as not in the document", got)
		}
		if _, err := p.Apply(ctx(t), f.client()); err != nil {
			t.Fatal(err)
		}
		if n := len(f.writeCalls()); n != 0 {
			t.Errorf("%d writes: what is not in the document is reported, never deleted", n)
		}
	})

	t.Run("a token issuer that is not in the document is kept", func(t *testing.T) {
		f := newFake(t, tenantStateFull())
		doc := wantTenantFull()
		doc.TokenIssuers = nil
		doc.SubjectMappings = doc.SubjectMappings[:1] // the mapping on corp-idp would be a conflict: leave it out
		doc.Limits = []declare.Limit{doc.Limits[0], doc.Limits[2], doc.Limits[3]}
		p := diff(t, f, tenant, doc)
		if got := actions(p); !has(got, "not in document token_issuer corp-idp") {
			t.Errorf("the plan: %v, want not in document token_issuer corp-idp", got)
		}
		if _, err := p.Apply(ctx(t), f.client()); err != nil {
			t.Fatal(err)
		}
		noDeletes(t, f)
	})
}

// TestPlanSubjectMappings is T-1122: a mapping is created; it has no update operation, so a
// different service account or description is a conflict; an issuer or a service account that does
// not exist is a conflict; and the mappings are planned after the issuers and the service accounts
// and before the limits, a rule on a mapping being resolved to the mapping's ID when it is applied.
func TestPlanSubjectMappings(t *testing.T) {
	tenant := declare.Target{Tenant: tidAcme}

	t.Run("a mapping is created", func(t *testing.T) {
		f := newFake(t, tenantStateFull())
		doc := wantTenantFull()
		doc.SubjectMappings = append(doc.SubjectMappings,
			declare.SubjectMapping{Issuer: declare.MappingIssuer{Scope: "tenant", Name: "corp-idp"}, Subject: "new-subject", ServiceAccount: "billing", Description: "new"},
			declare.SubjectMapping{Issuer: declare.MappingIssuer{Scope: "platform", Name: "shared-idp"}, Subject: "other-subject", ServiceAccount: "reports"})
		p := diff(t, f, tenant, doc)
		if got := actions(p); len(got) != 2 || !has(got, "create subject_mapping") {
			t.Fatalf("the plan: %v, want two creates of subject_mapping", got)
		}
		if _, err := p.Apply(ctx(t), f.client()); err != nil {
			t.Fatal(err)
		}
		w := f.writeCalls()
		if len(w) != 2 {
			t.Fatalf("writes: %v", writePaths(f))
		}
		for _, x := range w {
			if x.Method != "POST" || x.Path != "/v1/tenants/"+tidAcme+"/subject-mappings" {
				t.Errorf("write %s %s", x.Method, x.Path)
			}
		}
		for _, want := range []string{`"scope":"tenant"`, `"id":"` + issuerTenantID + `"`, `"service_account_id":"` + saBilling + `"`, `"subject":"new-subject"`} {
			if !strings.Contains(w[0].Body, want) && !strings.Contains(w[1].Body, want) {
				t.Errorf("no body carries %s: %s %s", want, w[0].Body, w[1].Body)
			}
		}
		if !strings.Contains(w[0].Body+w[1].Body, `"scope":"platform"`) || !strings.Contains(w[0].Body+w[1].Body, `"id":"`+issuerPlatformID+`"`) {
			t.Errorf("the mapping on the platform issuer does not name it by its ID: %s %s", w[0].Body, w[1].Body)
		}
	})

	for _, c := range []struct {
		name   string
		change func(m *declare.SubjectMapping)
	}{
		{"a different service account", func(m *declare.SubjectMapping) { m.ServiceAccount = "reports" }},
		{"a different description", func(m *declare.SubjectMapping) { m.Description = "something else" }},
	} {
		t.Run(c.name+" is a conflict", func(t *testing.T) {
			f := newFake(t, tenantStateFull())
			doc := wantTenantFull()
			c.change(&doc.SubjectMappings[1])
			p := diff(t, f, tenant, doc)
			if got := actions(p); len(got) != 1 || !strings.HasPrefix(got[0], "conflict subject_mapping") {
				t.Fatalf("the plan: %v, want one conflict on the mapping (it has no update)", got)
			}
			if res, err := p.Apply(ctx(t), f.client()); err != nil || res.Conflicts != 1 || len(f.writeCalls()) != 0 {
				t.Errorf("Apply: %+v %v, %d writes", res, err, len(f.writeCalls()))
			}
		})
	}

	for _, c := range []struct {
		name   string
		change func(m *declare.SubjectMapping)
	}{
		{"an issuer that does not exist", func(m *declare.SubjectMapping) { m.Issuer.Name = "ghost-idp" }},
		{"an issuer of the platform that does not list the tenant", func(m *declare.SubjectMapping) {
			m.Issuer = declare.MappingIssuer{Scope: "platform", Name: "other-idp"}
		}},
		{"a service account that does not exist", func(m *declare.SubjectMapping) { m.ServiceAccount = "ghost" }},
	} {
		t.Run(c.name+" is a conflict", func(t *testing.T) {
			f := newFake(t, tenantStateFull())
			doc := wantTenantFull()
			doc.SubjectMappings = append(doc.SubjectMappings, declare.SubjectMapping{Issuer: declare.MappingIssuer{Scope: "tenant", Name: "corp-idp"}, Subject: "another", ServiceAccount: "billing"})
			c.change(&doc.SubjectMappings[2])
			p := diff(t, f, tenant, doc)
			if got := actions(p); len(got) != 1 || !strings.HasPrefix(got[0], "conflict subject_mapping") {
				t.Fatalf("the plan: %v, want one conflict", got)
			}
			if _, err := p.Apply(ctx(t), f.client()); err != nil || len(f.writeCalls()) != 0 {
				t.Errorf("Apply: %v with %d writes", err, len(f.writeCalls()))
			}
		})
	}

	t.Run("the order: service accounts, issuers, mappings, limits, destinations", func(t *testing.T) {
		// A tenant with nothing in it but a platform issuer that lists it: the whole document is created.
		st := map[string]any{"/v1/tenants/" + tidAcme + "/platform-issuers": tenantStateFull()["/v1/tenants/"+tidAcme+"/platform-issuers"]}
		f := newFake(t, st)
		doc := wantTenantFull()
		doc.PriceLists = nil
		doc.Limits = []declare.Limit{doc.Limits[1], doc.Limits[3]} // the rule on the mapping, and the rule on the tenant
		doc.SubjectMappings = doc.SubjectMappings[1:]              // the tenant issuer's mapping only: the platform one is in the state's list
		p := diff(t, f, tenant, doc, declare.Options{SecretSink: func(string, []byte) error { return nil }})
		if _, err := p.Apply(ctx(t), f.client()); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		position := func(sub string) int {
			for i, w := range f.writeCalls() {
				if w.Method == "POST" && strings.HasSuffix(w.Path, sub) {
					return i
				}
			}
			return -1
		}
		prev := -1
		for _, sub := range []string{"/service-accounts", "/token-issuers", "/subject-mappings", "/limits", "/event-destinations"} {
			at := position(sub)
			if at < 0 {
				t.Errorf("no write for %s: %v", sub, writePaths(f))
				continue
			}
			if at <= prev {
				t.Errorf("%s is written at %d, not after the step before it (%d): %v", sub, at, prev, writePaths(f))
			}
			prev = at
		}
		// The rule on the mapping names the mapping the plan created, by the ID the server gave it.
		var mapping, rule call
		for _, w := range f.writeCalls() {
			switch {
			case strings.HasSuffix(w.Path, "/subject-mappings"):
				mapping = w
			case strings.HasSuffix(w.Path, "/limits") && strings.Contains(w.Body, `"subject_kind":"key"`):
				rule = w
			}
		}
		var made struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(mapping.Resp), &made)
		if made.ID == "" || !strings.Contains(rule.Body, `"subject_id":"`+made.ID+`"`) {
			t.Errorf("the rule on the mapping does not name the mapping just created (%q): %s", made.ID, rule.Body)
		}
	})
}

// TestPlanDestinations is T-1123: without a secret sink an absent destination is skipped with the
// stated text; with one it is created and the signing secret goes to the sink only; the four members
// are updated; no secret, cursor or delivery state is in an export; the plan never rotates a secret.
func TestPlanDestinations(t *testing.T) {
	tenant := declare.Target{Tenant: tidAcme}
	withNew := func() *declare.Document {
		doc := wantTenantFull()
		doc.EventDestinations = append(doc.EventDestinations, declare.EventDestination{Name: "new-hook", URL: "https://new.example.test/events", EventTypes: ptr([]string{"io.pandokos.usage.recorded.v1"}), Enabled: ptr(true)})
		return doc
	}

	t.Run("without a secret sink an absent destination is skipped", func(t *testing.T) {
		f := newFake(t, tenantStateFull())
		p := diff(t, f, tenant, withNew())
		var skipped []declare.Change
		for _, c := range p.Changes {
			if c.Action == "skipped" {
				skipped = append(skipped, c)
			}
		}
		if len(p.Changes) != 1 || len(skipped) != 1 {
			t.Fatalf("the plan: %v, want one skipped line", actions(p))
		}
		line := skipped[0].Line()
		if !strings.Contains(line, "event_destination new-hook") || !strings.Contains(line, "needs --secrets-out") {
			t.Errorf("the line is %q, want it to say that event_destination new-hook needs --secrets-out", line)
		}
		res, err := p.Apply(ctx(t), f.client())
		if err != nil || len(f.writeCalls()) != 0 || res.Changes != 0 || res.LeftAlone != 1 {
			t.Errorf("Apply: %+v %v with %d writes, want nothing created and the destination counted as left alone", res, err, len(f.writeCalls()))
		}
		if p := diff(t, newFake(t, tenantStateFull()), tenant, withNew()); len(p.Preview().Lines) != 1 {
			t.Errorf("a dry run prints %v", p.Preview().Lines)
		}
	})

	t.Run("with a secret sink it is created and the secret goes to the sink only", func(t *testing.T) {
		f := newFake(t, tenantStateFull())
		var names []string
		var secrets []string
		sink := func(name string, secret []byte) error {
			names, secrets = append(names, name), append(secrets, string(secret))
			return nil
		}
		p := diff(t, f, tenant, withNew(), declare.Options{SecretSink: sink})
		if got := actions(p); len(got) != 1 || !has(got, "create event_destination new-hook") {
			t.Fatalf("the plan: %v", got)
		}
		res, err := p.Apply(ctx(t), f.client())
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(names, []string{"event-destination-new-hook"}) || !reflect.DeepEqual(secrets, []string{canarySigningSecret}) {
			t.Errorf("the sink received %v and %d secrets, want the secret of new-hook once", names, len(secrets))
		}
		w := f.writeCalls()
		if len(w) != 1 || w[0].Method != "POST" || w[0].Path != "/v1/tenants/"+tidAcme+"/event-destinations" {
			t.Fatalf("writes: %v", writePaths(f))
		}
		text := strings.Join(res.Lines, "\n") + fmt.Sprintf("%v %+v", actions(p), p.Changes)
		if strings.Contains(text, canarySigningSecret) || strings.Contains(text, "whsec_") {
			t.Errorf("the signing secret is in what apply prints: %s", text)
		}
	})

	t.Run("a created destination with no secret in the answer is an error", func(t *testing.T) {
		f := newFake(t, tenantStateFull())
		f.noSecret = true
		called := false
		p := diff(t, f, tenant, withNew(), declare.Options{SecretSink: func(string, []byte) error { called = true; return nil }})
		_, err := p.Apply(ctx(t), f.client())
		if err == nil || !strings.Contains(err.Error(), "rotate-secret") {
			t.Errorf("Apply: %v, want an error that says to run event-destination rotate-secret", err)
		}
		if called {
			t.Error("the sink was called with no secret")
		}
	})

	t.Run("a sink that fails fails the apply without printing the secret", func(t *testing.T) {
		f := newFake(t, tenantStateFull())
		p := diff(t, f, tenant, withNew(), declare.Options{SecretSink: func(string, []byte) error { return errors.New("the directory is full") }})
		_, err := p.Apply(ctx(t), f.client())
		if err == nil || strings.Contains(err.Error(), canarySigningSecret) {
			t.Errorf("Apply: %v, want an error that holds no secret", err)
		}
	})

	for _, c := range []struct {
		member string
		change func(d *declare.EventDestination)
	}{
		{"description", func(d *declare.EventDestination) { d.Description = "changed" }},
		{"url", func(d *declare.EventDestination) { d.URL = "https://moved.example.test/pandokos" }},
		{"event_types", func(d *declare.EventDestination) { d.EventTypes = ptr([]string{}) }},
		{"enabled", func(d *declare.EventDestination) { d.Enabled = ptr(false) }},
	} {
		t.Run("a destination is updated: "+c.member, func(t *testing.T) {
			f := newFake(t, tenantStateFull())
			doc := wantTenantFull()
			c.change(&doc.EventDestinations[1])
			p := diff(t, f, tenant, doc)
			if got := actions(p); len(got) != 1 || !strings.HasPrefix(got[0], "update event_destination hooks") {
				t.Fatalf("the plan: %v, want one update of event_destination hooks", got)
			}
			if _, err := p.Apply(ctx(t), f.client()); err != nil {
				t.Fatal(err)
			}
			w := f.writeCalls()
			if len(w) != 1 || w[0].Method != "PATCH" || w[0].Path != "/v1/tenants/"+tidAcme+"/event-destinations/"+destinationID || !reflect.DeepEqual(bodyKeys(t, w[0]), []string{c.member}) {
				t.Errorf("writes: %+v, want one PATCH that carries %s and nothing else", w, c.member)
			}
		})
	}

	t.Run("nothing is rotated, deleted or rewound, whatever the document", func(t *testing.T) {
		f := newFake(t, tenantStateFull())
		doc := withNew()
		doc.EventDestinations = doc.EventDestinations[1:] // audit-sink is not in the document
		doc.EventDestinations[0].Enabled = ptr(false)
		p := diff(t, f, tenant, doc, declare.Options{SecretSink: func(string, []byte) error { return nil }})
		if _, err := p.Apply(ctx(t), f.client()); err != nil {
			t.Fatal(err)
		}
		for _, w := range f.writeCalls() {
			if w.Method == "DELETE" || strings.Contains(w.Path, "rotate-secret") || strings.Contains(w.Path, "rewind") || strings.Contains(w.Path, "replay") {
				t.Errorf("apply wrote %s %s", w.Method, w.Path)
			}
		}
		if got := actions(p); !has(got, "not in document event_destination audit-sink") {
			t.Errorf("the plan: %v, want audit-sink reported as not in the document", got)
		}
	})

	t.Run("an export holds no secret, cursor or delivery state", func(t *testing.T) {
		doc, err := declare.Export(ctx(t), newFake(t, tenantStateFull()).client(), tenant)
		if err != nil {
			t.Fatal(err)
		}
		text := string(marshal(t, doc))
		for _, banned := range []string{"secret", "whsec_", "cursor", "delivery", "failures", canaryDelivery, "31337"} {
			if strings.Contains(text, banned) {
				t.Errorf("the export holds %q:\n%s", banned, text)
			}
		}
	})
}

// TestPlanExternalReference is T-1124: the external reference of an existing tenant is updated; a
// tenant that does not exist is a conflict and apply never creates one; an empty reference leaves
// the tenant's reference alone; the export lists only tenants that have one (TestExportExternalReference).
func TestPlanExternalReference(t *testing.T) {
	platform := declare.Target{Platform: true}

	t.Run("an existing tenant is updated", func(t *testing.T) {
		f := newFake(t, platformStateFull())
		doc := wantPlatformFull()
		doc.Tenants = []declare.PlatformTenant{{Name: "acme", ExternalReference: "crm-9999"}, {Name: "globex", ExternalReference: "crm-1"}}
		p := diff(t, f, platform, doc)
		if got := actions(p); len(got) != 2 || !has(got, "update tenant acme") || !has(got, "update tenant globex") {
			t.Fatalf("the plan: %v, want an update of acme and of globex", got)
		}
		if _, err := p.Apply(ctx(t), f.client()); err != nil {
			t.Fatal(err)
		}
		w := f.writeCalls()
		if len(w) != 2 {
			t.Fatalf("writes: %v", writePaths(f))
		}
		for _, x := range w {
			if x.Method != "PATCH" || !reflect.DeepEqual(bodyKeys(t, x), []string{"external_reference"}) {
				t.Errorf("write %s %s %s: want a PATCH of external_reference alone", x.Method, x.Path, x.Body)
			}
		}
		if w[0].Path != "/v1/tenants/"+tidAcme || !strings.Contains(w[0].Body, "crm-9999") {
			t.Errorf("the first write is %s %s", w[0].Path, w[0].Body)
		}
	})

	t.Run("the same reference changes nothing", func(t *testing.T) {
		f := newFake(t, platformStateFull())
		if got := actions(diff(t, f, platform, wantPlatformFull())); len(got) != 0 {
			t.Errorf("the plan: %v, want empty", got)
		}
	})

	t.Run("a tenant that does not exist is a conflict and is never created", func(t *testing.T) {
		f := newFake(t, platformStateFull())
		doc := wantPlatformFull()
		doc.Tenants = append(doc.Tenants, declare.PlatformTenant{Name: "nowhere", ExternalReference: "crm-0"})
		p := diff(t, f, platform, doc)
		if got := actions(p); len(got) != 1 || !strings.HasPrefix(got[0], "conflict tenant nowhere") {
			t.Fatalf("the plan: %v, want one conflict on tenant nowhere", got)
		}
		if res, err := p.Apply(ctx(t), f.client()); err != nil || res.Conflicts != 1 {
			t.Errorf("Apply: %+v %v", res, err)
		}
		for _, w := range f.writeCalls() {
			if w.Method == "POST" && w.Path == "/v1/tenants" {
				t.Error("apply created a tenant: its first key is a secret, apply never does")
			}
		}
	})

	t.Run("an empty reference leaves the tenant's alone", func(t *testing.T) {
		f := newFake(t, platformStateFull())
		doc := wantPlatformFull()
		doc.Tenants = []declare.PlatformTenant{{Name: "acme"}, {Name: "globex"}} // acme has crm-4711, globex none
		p := diff(t, f, platform, doc)
		if got := actions(p); len(got) != 0 {
			t.Fatalf("the plan: %v, want nothing", got)
		}
		if _, err := p.Apply(ctx(t), f.client()); err != nil || len(f.writeCalls()) != 0 {
			t.Errorf("Apply: %v with %d writes", err, len(f.writeCalls()))
		}
	})
}

// TestAbsentMembersOfIssuersAndDestinations holds the rule of the members that a plain value could not leave
// alone: on an update an absent enabled, required_claims, event_types or jwks_uri changes nothing; an explicit
// empty value clears; on a create an absent enabled makes the item disabled and the others empty.
func TestAbsentMembersOfIssuersAndDestinations(t *testing.T) {
	platform, tenant := declare.Target{Platform: true}, declare.Target{Tenant: tidAcme}

	t.Run("omitted on update means no change", func(t *testing.T) {
		pdoc := wantPlatformFull()
		pdoc.PlatformIssuers[0].Enabled, pdoc.PlatformIssuers[0].RequiredClaims, pdoc.PlatformIssuers[0].JWKSURI = nil, nil, nil
		if got := actions(diff(t, newFake(t, platformStateFull()), platform, pdoc)); len(got) != 0 {
			t.Errorf("a platform issuer without enabled, required_claims and jwks_uri: the plan is %v, want empty", got)
		}
		tdoc := wantTenantFull()
		tdoc.TokenIssuers[0].Enabled, tdoc.TokenIssuers[0].RequiredClaims, tdoc.TokenIssuers[0].JWKSURI = nil, nil, nil
		for i := range tdoc.EventDestinations {
			tdoc.EventDestinations[i].Enabled, tdoc.EventDestinations[i].EventTypes = nil, nil
		}
		if got := actions(diff(t, newFake(t, tenantStateFull()), tenant, tdoc)); len(got) != 0 {
			t.Errorf("a token issuer and destinations without those members: the plan is %v, want empty", got)
		}
	})

	t.Run("an explicit empty value on update clears", func(t *testing.T) {
		f := newFake(t, tenantStateFull())
		doc := wantTenantFull()
		doc.TokenIssuers[0].RequiredClaims = ptr(map[string]string{})
		doc.TokenIssuers[0].JWKSURI = ptr("")
		doc.EventDestinations[1].EventTypes = ptr([]string{})
		p := diff(t, f, tenant, doc)
		if got := actions(p); len(got) != 2 || !has(got, "update token_issuer corp-idp") || !has(got, "update event_destination hooks") {
			t.Fatalf("the plan: %v, want an update of the token issuer's required_claims and of the destination's event_types", got)
		}
		if _, err := p.Apply(ctx(t), f.client()); err != nil {
			t.Fatal(err)
		}
		for _, w := range f.writeCalls() {
			if strings.Contains(w.Path, "token-issuers") && !reflect.DeepEqual(bodyKeys(t, w), []string{"required_claims"}) {
				t.Errorf("the token issuer was patched with %v, want required_claims only (jwks_uri is already empty)", bodyKeys(t, w))
			}
			if strings.Contains(w.Path, "event-destinations") && !reflect.DeepEqual(bodyKeys(t, w), []string{"event_types"}) {
				t.Errorf("the destination was patched with %v, want event_types only", bodyKeys(t, w))
			}
		}
	})

	t.Run("omitted on create means disabled and empty", func(t *testing.T) {
		f := newFake(t, tenantStateFull())
		doc := wantTenantFull()
		doc.TokenIssuers = append(doc.TokenIssuers, declare.TokenIssuer{Name: "bare-idp", Issuer: "https://bare.example.test", Audience: "x"})
		doc.EventDestinations = append(doc.EventDestinations, declare.EventDestination{Name: "bare-hook", URL: "https://bare.example.test/events"})
		sink := func(string, []byte) error { return nil }
		p := diff(t, f, tenant, doc, declare.Options{SecretSink: sink})
		if _, err := p.Apply(ctx(t), f.client()); err != nil {
			t.Fatal(err)
		}
		var seen int
		for _, w := range f.writeCalls() {
			if w.Method != "POST" {
				continue
			}
			var body struct {
				Enabled bool `json:"enabled"`
			}
			if err := json.Unmarshal([]byte(w.Body), &body); err != nil {
				t.Fatal(err)
			}
			if body.Enabled {
				t.Errorf("%s created an item enabled although the document did not say so: %s", w.Path, w.Body)
			}
			seen++
		}
		if seen != 2 {
			t.Errorf("%d creates, want 2: %v", seen, writePaths(f))
		}
	})
}
