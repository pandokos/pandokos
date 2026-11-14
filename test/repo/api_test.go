package repo

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/config"
)

// sqlMethods are the methods whose second argument is SQL text.
var sqlMethods = map[string]bool{"Exec": true, "Query": true, "QueryRow": true, "SendBatch": true, "CopyFrom": true, "Prepare": true}

// sqlException says why one call may take SQL that is not a literal. Each is a file and the
// source text of the argument.
type sqlException struct{ file, arg, reason string }

var sqlExceptions = []sqlException{
	{"internal/store/tx.go", "sql", "Tx passes the statement of its caller through; the call sites are checked, and only the store holds a Tx"},
	{"internal/store/migrate/apply.go", "m.SQL", "the migrator executes the content of a migration file: a build input, not a built string"},
	{"internal/store/migrate/apply.go", `"SET LOCAL ROLE " + pgx.Identifier{OwnerRole}.Sanitize()`, "a fixed role name, quoted as an identifier by the driver"},
}

// sqlViolations returns one message per call in src whose SQL argument is not a string literal
// or a constant made of literals. consts are the package-level constants of the package.
func sqlViolations(fset *token.FileSet, file *ast.File, consts map[string]bool) []string {
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !sqlMethods[sel.Sel.Name] || len(call.Args) < 2 {
			return true
		}
		if !literalSQL(call.Args[1], consts) {
			out = append(out, fset.Position(call.Pos()).String()+": "+sel.Sel.Name+" takes "+exprText(call.Args[1]))
		}
		return true
	})
	return out
}

func literalSQL(e ast.Expr, consts map[string]bool) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Kind == token.STRING
	case *ast.Ident:
		return consts[v.Name]
	case *ast.ParenExpr:
		return literalSQL(v.X, consts)
	case *ast.BinaryExpr:
		return v.Op == token.ADD && literalSQL(v.X, consts) && literalSQL(v.Y, consts)
	}
	return false
}

// exprText prints the simple shapes of an argument, for the exception list and the messages.
func exprText(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Value
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprText(v.X) + "." + v.Sel.Name
	case *ast.BinaryExpr:
		return exprText(v.X) + " " + v.Op.String() + " " + exprText(v.Y)
	case *ast.CallExpr:
		args := make([]string, len(v.Args))
		for i, a := range v.Args {
			args[i] = exprText(a)
		}
		return exprText(v.Fun) + "(" + strings.Join(args, ", ") + ")"
	case *ast.CompositeLit:
		elts := make([]string, len(v.Elts))
		for i, a := range v.Elts {
			elts[i] = exprText(a)
		}
		return exprText(v.Type) + "{" + strings.Join(elts, ", ") + "}"
	case *ast.ParenExpr:
		return "(" + exprText(v.X) + ")"
	}
	return "<expression>"
}

// stringConsts returns the names of the constants (package-level or local, of every file of the
// directory) whose value is a string literal or a + of string literals.
func stringConsts(files []*ast.File) map[string]bool {
	consts := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, f := range files {
			ast.Inspect(f, func(n ast.Node) bool {
				g, ok := n.(*ast.GenDecl)
				if !ok || g.Tok != token.CONST {
					return true
				}
				for _, spec := range g.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if consts[name.Name] || i >= len(vs.Values) {
							continue
						}
						if literalSQL(vs.Values[i], consts) {
							consts[name.Name] = true
							changed = true
						}
					}
				}
				return true
			})
		}
	}
	return consts
}

// TestNoSQLFromStrings is T-R12: no SQL is built from strings, and none is run outside the store.
func TestNoSQLFromStrings(t *testing.T) {
	t.Run("negative cases are detected", func(t *testing.T) {
		for name, src := range map[string]string{
			"concatenation with a variable": `package x
func f(c C, id string) { c.Exec(ctx, "SELECT * FROM t WHERE id = " + id) }`,
			"Sprintf": `package x
func f(c C, id string) { c.Query(ctx, fmt.Sprintf("SELECT %s", id)) }`,
			"a var": `package x
var q = "SELECT 1"
func f(c C) { c.QueryRow(ctx, q) }`,
			"a parameter": `package x
func f(c C, q string) { c.Exec(ctx, q) }`,
		} {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "x.go", src, 0)
			if err != nil {
				t.Fatal(name, err)
			}
			if v := sqlViolations(fset, f, stringConsts([]*ast.File{f})); len(v) != 1 {
				t.Errorf("%s: %d violations reported, want 1: %v", name, len(v), v)
			}
		}
		for name, src := range map[string]string{
			"a literal": `package x
func f(c C) { c.Exec(ctx, "SELECT 1") }`,
			"a const of literals": `package x
const q = "SELECT " + "1"
func f(c C) { c.Exec(ctx, q) }`,
			"a const of consts": `package x
const a = "SELECT "
const q = a + "1"
func f(c C) { c.Exec(ctx, q) }`,
			"another method": `package x
func f(c C, id string) { c.Run(ctx, "a" + id) }`,
		} {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "x.go", src, 0)
			if err != nil {
				t.Fatal(name, err)
			}
			if v := sqlViolations(fset, f, stringConsts([]*ast.File{f})); len(v) != 0 {
				t.Errorf("%s: reported %v", name, v)
			}
		}
	})

	t.Run("the product", func(t *testing.T) {
		used := map[int]bool{}
		for _, dir := range []string{"cmd", "internal"} {
			byDir := map[string][]*ast.File{}
			names := map[*ast.File]string{}
			fset := token.NewFileSet()
			err := filepath.WalkDir(filepath.Join(root(t), dir), func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
					return err
				}
				f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
				if err != nil {
					return err
				}
				if len(f.Comments) > 0 && strings.Contains(f.Comments[0].List[0].Text, "Code generated") {
					return nil
				}
				byDir[filepath.Dir(path)] = append(byDir[filepath.Dir(path)], f)
				rel, _ := filepath.Rel(root(t), path)
				names[f] = filepath.ToSlash(rel)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, files := range byDir {
				consts := stringConsts(files)
				for _, f := range files {
					rel := names[f]
					ast.Inspect(f, func(n ast.Node) bool {
						call, ok := n.(*ast.CallExpr)
						if !ok {
							return true
						}
						sel, ok := call.Fun.(*ast.SelectorExpr)
						if !ok || !sqlMethods[sel.Sel.Name] || len(call.Args) < 2 {
							return true
						}
						if !strings.HasPrefix(rel, "internal/store/") {
							t.Errorf("%s: %s is called outside internal/store", rel, sel.Sel.Name)
							return true
						}
						if literalSQL(call.Args[1], consts) {
							return true
						}
						text := exprText(call.Args[1])
						for i, ex := range sqlExceptions {
							if ex.file == rel && ex.arg == text {
								used[i] = true
								return true
							}
						}
						t.Errorf("%s: %s builds its SQL: %s", fset.Position(call.Pos()), sel.Sel.Name, text)
						return true
					})
				}
			}
		}
		for i, ex := range sqlExceptions {
			if !used[i] {
				t.Errorf("the exception for %s (%s) is not needed any more", ex.file, ex.arg)
			}
		}
	})
}

// internalDeps returns the import paths of the dependencies of ./internal/<pkg>.
func internalDeps(t *testing.T, pkg string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "./internal/"+pkg)
	cmd.Dir = root(t)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps ./internal/%s: %v\n%s", pkg, err, stderr.String())
	}
	return strings.Fields(string(out))
}

// TestPlanesDoNotImportEachOther is T-R13, extended by T-R20 (sprint 2.1, section 4) and by sprint
// 2.2 (section 4): the catalogue is below the planes and knows neither of them; the gateway cannot
// reach it (it cannot reach the store); the administration plane may.
func TestPlanesDoNotImportEachOther(t *testing.T) {
	const mod = "github.com/pandokos/pandokos/internal/"
	rules := map[string][]string{
		"gateway": {"admin", "adminapi", "authn", "cli", "store", "vault", "catalog"},
		// "family" is not listed: the catalogue prices usage (pricing.CostOf takes family.Usage), and
		// the administration plane reaches the catalogue (sprint 2.2, C8), so it reaches family
		// transitively through internal/pricing.
		"admin":    {"gateway", "server", "config"},
		"cli":      {"store", "admin", "config"},
		"authz":    {"store", "admin", "gateway", "config", "authn"},
		"adminapi": {"store", "admin", "gateway", "authn", "authz", "config", "cli"},
		// T-R20: the vault is below the planes and knows neither of them, nor the server, the CLI
		// or the configuration.
		"vault": {"admin", "adminapi", "gateway", "family", "server", "cli", "config"},
		// Sprint 2.2: internal/catalog may not reach admin, adminapi, gateway, server, cli, config
		// or the vault; internal/gateway does not reach it until sprint 3.1 gives it an interface.
		"catalog": {"admin", "adminapi", "gateway", "server", "cli", "config", "vault"},
	}
	for pkg, banned := range rules {
		deps := internalDeps(t, pkg)
		for _, d := range deps {
			for _, b := range banned {
				if d == mod+b || strings.HasPrefix(d, mod+b+"/") {
					t.Errorf("internal/%s reaches internal/%s (through %s)", pkg, b, d)
				}
			}
		}
	}
	// The generated client and router use the standard library only.
	for _, d := range internalDeps(t, "adminapi") {
		first, _, _ := strings.Cut(d, "/")
		if strings.Contains(first, ".") && d != mod+"adminapi" {
			t.Errorf("internal/adminapi reaches %s, which is not the standard library", d)
		}
	}
	// The vault imports authz, secret, store, store/queries, tenant and the standard library, and
	// nothing else of the product; internal/admin may import the vault.
	t.Run("what the vault imports", func(t *testing.T) {
		cmd := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, "./internal/vault")
		cmd.Dir = root(t)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list ./internal/vault: %v", err)
		}
		allowed := map[string]bool{}
		for _, a := range []string{"authz", "secret", "store", "store/queries", "tenant"} {
			allowed[mod+a] = true
		}
		n := 0
		for _, imp := range strings.Fields(string(out)) {
			first, _, _ := strings.Cut(imp, "/")
			switch {
			case strings.HasPrefix(imp, mod):
				n++
				if !allowed[imp] {
					t.Errorf("internal/vault imports %s", imp)
				}
			case strings.Contains(first, "."):
				t.Errorf("internal/vault imports %s, which is not the standard library or the product", imp)
			}
		}
		if n == 0 {
			t.Error("internal/vault imports nothing of the product: the test would prove nothing")
		}
		found := false
		for _, d := range internalDeps(t, "admin") {
			found = found || d == mod+"vault"
		}
		if !found {
			t.Error("internal/admin does not reach internal/vault")
		}
	})
	// Sprint 2.2: what internal/catalog imports of the product, and that the administration plane uses it.
	t.Run("what the catalogue imports", func(t *testing.T) {
		cmd := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, "./internal/catalog")
		cmd.Dir = root(t)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list ./internal/catalog: %v", err)
		}
		allowed := map[string]bool{}
		for _, a := range []string{"authz", "audit", "pricing", "pricing/genaiprices", "store", "store/queries", "tenant", "telemetry", "buildinfo"} {
			allowed[mod+a] = true
		}
		n := 0
		for _, imp := range strings.Fields(string(out)) {
			first, _, _ := strings.Cut(imp, "/")
			switch {
			case strings.HasPrefix(imp, mod):
				n++
				if !allowed[imp] {
					t.Errorf("internal/catalog imports %s", imp)
				}
			case strings.Contains(first, "."):
				t.Errorf("internal/catalog imports %s, which is not the standard library or the product", imp)
			}
		}
		if n == 0 {
			t.Error("internal/catalog imports nothing of the product: the test would prove nothing")
		}
		for _, want := range []string{"catalog", "pricing"} {
			found := false
			for _, d := range internalDeps(t, "admin") {
				found = found || d == mod+want
			}
			if !found {
				t.Errorf("internal/admin does not reach internal/%s", want)
			}
		}
		for _, d := range internalDeps(t, "gateway") {
			if d == mod+"catalog" || d == mod+"pricing/genaiprices" {
				t.Errorf("internal/gateway reaches %s", d)
			}
		}
	})
	// internal/authz and internal/requestid know only the tenant package.
	for _, pkg := range []string{"authz", "requestid"} {
		for _, d := range internalDeps(t, pkg) {
			first, _, _ := strings.Cut(d, "/")
			if strings.Contains(first, ".") && d != mod+pkg && d != mod+"tenant" {
				t.Errorf("internal/%s reaches %s", pkg, d)
			}
		}
	}
}

// TestReferenceDocuments is T-R14.
func TestReferenceDocuments(t *testing.T) {
	t.Run("permissions", func(t *testing.T) {
		doc := read(t, "docs/reference/permissions.md")
		lines := strings.Split(doc, "\n")
		has := func(parts ...string) bool {
			for _, l := range lines {
				ok := true
				for _, p := range parts {
					ok = ok && strings.Contains(l, p)
				}
				if ok {
					return true
				}
			}
			return false
		}
		for _, p := range authz.Catalogue() {
			if !has("`"+p.Name+"`", string(p.Scope)) {
				t.Errorf("permissions.md has no line with %s and its scope %s", p.Name, p.Scope)
			}
		}
		for _, r := range authz.Roles() {
			parts := []string{"`" + r.Name + "`"}
			for _, p := range r.Permissions {
				parts = append(parts, "`"+p+"`")
			}
			if !has(parts...) {
				t.Errorf("permissions.md has no line with the role %s and all its permissions", r.Name)
			}
		}
		for _, w := range []string{"default is deny", "tenant_suspended", "not_found"} {
			if !strings.Contains(doc, w) {
				t.Errorf("permissions.md does not explain %q", w)
			}
		}
	})
	t.Run("keys", func(t *testing.T) {
		doc := read(t, "docs/operations/keys.md")
		for _, w := range []string{authn.Pattern, "pandokos init", "cache.principal_ttl", "database.direct_address", "30 s", "60 s", "--recover", "--key-out"} {
			if !strings.Contains(doc, w) {
				t.Errorf("keys.md lacks %q", w)
			}
		}
	})
	t.Run("admin API (sprint 2.1)", func(t *testing.T) {
		doc := read(t, "docs/reference/admin-api.md")
		for _, op := range adminapi.Operations {
			if !strings.Contains(doc, "`"+op.ID+"`") {
				t.Errorf("admin-api.md does not name the operation %s", op.ID)
			}
		}
		for _, w := range []string{
			"`credential_rejected`", "`validation_unavailable`", "422", "502",
			"credential list", "tenant-credential", "kek generate",
			"write-only", "never returned",
		} {
			if !strings.Contains(doc, w) {
				t.Errorf("admin-api.md lacks %q", w)
			}
		}
	})
	t.Run("keys (sprint 2.1)", func(t *testing.T) {
		doc := read(t, "docs/operations/keys.md")
		for _, w := range []string{"--credential", "credential:use", "model:invoke"} {
			if !strings.Contains(doc, w) {
				t.Errorf("keys.md lacks %q", w)
			}
		}
	})
	t.Run("catalogue (sprint 2.2)", func(t *testing.T) {
		// T-R14 extended: the codes, the commands and the permissions of the sprint in the reference pages.
		api := read(t, "docs/reference/admin-api.md")
		for _, w := range []string{
			"`price_source_unavailable`", "`price_dataset_invalid`", "502", "422",
			"import-prices", "price-import", "tenant-model", "model-restriction", "revoke-grant", "clear-override",
		} {
			if !strings.Contains(api, w) {
				t.Errorf("admin-api.md lacks %q", w)
			}
		}
		perms := read(t, "docs/reference/permissions.md")
		for _, p := range []string{"catalog:read", "catalog:write", "catalog:price", "model:read", "model:restrict"} {
			if !strings.Contains(perms, "`"+p+"`") {
				t.Errorf("permissions.md does not list %s", p)
			}
		}
		keys := read(t, "docs/operations/keys.md")
		for _, w := range []string{"--model"} {
			if !strings.Contains(keys, w) {
				t.Errorf("keys.md lacks %q", w)
			}
		}
		for _, k := range config.Keys() {
			switch k.Name {
			case "cache.catalog_ttl", "pricing.import_at_start", "pricing.refresh.enabled", "pricing.refresh.url", "pricing.refresh.interval":
				if !strings.Contains(read(t, "docs/operations/configuration.md"), k.Env) {
					t.Errorf("configuration.md does not name the variable %s", k.Env)
				}
			}
		}
	})
	t.Run("tls", func(t *testing.T) {
		doc := read(t, "docs/operations/tls.md")
		for _, k := range config.Keys() {
			if strings.Contains(k.Name, ".tls.") && strings.HasPrefix(k.Name, "listeners.") || k.Name == "listeners.admin.allow_plaintext" {
				if !strings.Contains(doc, k.Name) {
					t.Errorf("tls.md does not name %s", k.Name)
				}
			}
		}
		for _, w := range []string{"verify-full", "database.tls.ca_file"} {
			if !strings.Contains(doc, w) {
				t.Errorf("tls.md lacks %q", w)
			}
		}
	})
}

// TestOpenAPIDocument is T-R15, changed in sprint 2.1: the secret of a credential is written by
// two request bodies and returned by none.
func TestOpenAPIDocument(t *testing.T) {
	var doc struct {
		OpenAPI string                          `yaml:"openapi"`
		Paths   map[string]map[string]yaml.Node `yaml:"paths"`
	}
	raw := read(t, "api/openapi/admin.v1.yaml")
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Errorf("openapi: %q", doc.OpenAPI)
	}
	type op struct {
		ID         string                       `yaml:"operationId"`
		Permission string                       `yaml:"x-pandokos-permission"`
		Responses  map[string]map[string]string `yaml:"responses"`
	}
	generated := map[string]adminapi.Operation{}
	for _, o := range adminapi.Operations {
		generated[o.ID] = o
	}
	catalogue := map[string]bool{}
	for _, p := range authz.Catalogue() {
		catalogue[p.Name] = true
	}
	n := 0
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/") {
			t.Errorf("path %s does not start with /v1/", path)
		}
		for method, node := range item {
			if method == "parameters" {
				continue
			}
			var o op
			if err := node.Decode(&o); err != nil {
				t.Fatal(err)
			}
			n++
			if !catalogue[o.Permission] {
				t.Errorf("%s %s: the permission %q is not in the catalogue", method, path, o.Permission)
			}
			if g, ok := generated[o.ID]; !ok || g.Permission != o.Permission {
				t.Errorf("%s: the document says %q, Operations says %+v", o.ID, o.Permission, g)
			}
			for _, code := range []string{"401", "403", "500", "503"} {
				if _, ok := o.Responses[code]; !ok {
					t.Errorf("%s does not list %s", o.ID, code)
				}
			}
		}
	}
	if n != len(adminapi.Operations) {
		t.Errorf("the document has %d operations, the generated code %d", n, len(adminapi.Operations))
	}
	// Sprint 2.2: 64 operations, and money is never a JSON number: no schema of the document has a
	// number type, and a rate is a string with a pattern.
	if n != 64 {
		t.Errorf("the document has %d operations, want 64 (35 and the 29 of sprint 2.2)", n)
	}
	if regexp.MustCompile(`(?m)type:\s*(number|\[[^\]]*number)`).MatchString(raw) || regexp.MustCompile(`format:\s*(float|double)`).MatchString(raw) {
		t.Error("the document uses a floating-point type: prices travel as decimal strings")
	}
	var rate struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Type    any    `yaml:"type"` // a string, or a list ([string, "null"]) for a nullable member
					Pattern string `yaml:"pattern"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal([]byte(raw), &rate); err != nil {
		t.Fatal(err)
	}
	if r := rate.Components.Schemas["PriceRate"].Properties["rate"]; r.Type != "string" || r.Pattern == "" {
		t.Errorf("PriceRate.rate is %+v, want a string with a pattern", r)
	}

	var schemas struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]yaml.Node `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal([]byte(raw), &schemas); err != nil {
		t.Fatal(err)
	}
	var withSecret []string
	for name, s := range schemas.Components.Schemas {
		for member := range s.Properties {
			if strings.Contains(strings.ToLower(member), "hash") {
				t.Errorf("schema %s has the member %s", name, member)
			}
			if member == "secret" {
				withSecret = append(withSecret, name)
			}
		}
	}
	sort.Strings(withSecret)
	// A secret is written by two request bodies (a provider credential is created and replaced)
	// and returned by the two ...Created schemas, and by no schema of a response about credentials.
	if strings.Join(withSecret, ",") != "CredentialCreate,CredentialSecret,PlatformKeyCreated,VirtualKeyCreated" {
		t.Errorf("secret occurs in %v, want only CredentialCreate, CredentialSecret and the two ...Created schemas", withSecret)
	}
}

// TestGenerationIsCheckedInCI is T-R16.
func TestGenerationIsCheckedInCI(t *testing.T) {
	ci := workflows(t)["ci.yml"]
	job := regexp.MustCompile(`(?m)^  generate:\n(?:    .*\n|\n)+`).FindString(ci)
	if job == "" || !strings.Contains(job, "make generate-check") {
		t.Error("ci.yml has no job that runs make generate-check")
	}
	if !regexp.MustCompile(`(?m)^SQLC := github\.com/sqlc-dev/sqlc/cmd/sqlc@v\d+\.\d+\.\d+$`).MatchString(read(t, "Makefile")) {
		t.Error("the Makefile does not pin sqlc by version")
	}
	rel := workflows(t)["release.yml"]
	if !strings.Contains(rel, "api/openapi/admin.v1.yaml") || strings.Index(rel, "api/openapi/admin.v1.yaml") < strings.Index(rel, "sbom.spdx.json") {
		t.Error("release.yml does not attach api/openapi/admin.v1.yaml next to the SBOM")
	}
}

// TestNoKeyMaterialInTheRepository is T-R19.
func TestNoKeyMaterialInTheRepository(t *testing.T) {
	cmd := exec.Command("git", "ls-files", "-co", "--exclude-standard")
	cmd.Dir = root(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	key := regexp.MustCompile(authn.Pattern)
	armour := regexp.MustCompile(strings.Repeat("-", 5) + `BEGIN [A-Z ]*(PRIVATE KEY|CERTIFICATE)`)
	n := 0
	for _, rel := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if rel == "" || strings.HasPrefix(rel, "planning/") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root(t), rel))
		if err != nil {
			continue // a file removed in the working tree
		}
		n++
		if key.Match(b) {
			t.Errorf("%s contains a string in the format of a key", rel)
		}
		if armour.Match(b) {
			t.Errorf("%s contains PEM key material or a certificate", rel)
		}
	}
	if n < 100 {
		t.Errorf("only %d files were scanned", n)
	}
	if !strings.Contains(read(t, "test/db/compose.yaml"), "../../local/testdb/tls:/tls:ro") {
		t.Error("the compose file does not mount the TLS directory from local/")
	}
}

// TestSecretScanning checks that the history of the repository is scanned for keys in CI, with
// the same expression the product documents for secret scanners.
func TestSecretScanning(t *testing.T) {
	cfg := read(t, ".gitleaks.toml")
	if !strings.Contains(cfg, "regex = '''"+authn.Pattern+"'''") {
		t.Errorf(".gitleaks.toml has no rule with the regular expression %s", authn.Pattern)
	}
	if !strings.Contains(cfg, "useDefault = true") {
		t.Error(".gitleaks.toml does not extend the default rules")
	}
	if !regexp.MustCompile(`(?m)^secrets:`).MatchString(read(t, "Makefile")) {
		t.Error("Makefile has no secrets target")
	}
	ci := workflows(t)["ci.yml"]
	if !strings.Contains(ci, "make secrets") {
		t.Error("ci.yml has no job that runs make secrets")
	}
	if !strings.Contains(ci, "fetch-depth: 0") {
		t.Error("ci.yml does not fetch the full history for the secret scan")
	}
	if !strings.Contains(read(t, "CONTRIBUTING.md"), "make secrets") {
		t.Error("CONTRIBUTING.md does not mention make secrets")
	}
}

// TestCryptographyInOnePlace is T-R21: only internal/vault imports crypto/aes, crypto/cipher and
// crypto/hkdf, and no package imports a cryptography module that is not the standard library.
func TestCryptographyInOnePlace(t *testing.T) {
	only := map[string]bool{"crypto/aes": true, "crypto/cipher": true, "crypto/hkdf": true}
	seen := map[string]bool{}
	files := 0
	err := filepath.WalkDir(root(t), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root(t), path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch rel {
			case ".git", "planning", "docs", "local", "test/sdk":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		files++
		inVault := strings.HasPrefix(rel, "internal/vault/")
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if only[p] {
				if inVault {
					seen[p] = !strings.HasSuffix(rel, "_test.go") || seen[p] // the product, not its tests, must use it
				} else {
					t.Errorf("%s imports %s: sealing, unsealing and key derivation live in internal/vault only", rel, p)
				}
			}
			first, _, _ := strings.Cut(p, "/")
			if strings.Contains(first, ".") && strings.Contains(p, "crypto") {
				t.Errorf("%s imports %s: a cryptography module outside the standard library", rel, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 100 {
		t.Errorf("only %d Go files were read", files)
	}
	for p := range only {
		if !seen[p] {
			t.Errorf("internal/vault does not import %s: the test would prove nothing", p)
		}
	}
	// The module requires no cryptography module either.
	for _, line := range strings.Split(read(t, "go.mod"), "\n") {
		if strings.Contains(line, "crypto") {
			t.Errorf("go.mod requires %s", strings.TrimSpace(line))
		}
	}
	// The linter says the same, so that the rule is checked when code is written.
	lint := read(t, ".golangci.yml")
	for p := range only {
		if !strings.Contains(lint, "pkg: "+p) {
			t.Errorf(".golangci.yml does not forbid %s outside internal/vault", p)
		}
	}
}

// TestVaultGuide is T-R22: the operations guide of the vault carries the facts an operator needs
// to survive the loss of the key, and the decision records exist.
func TestVaultGuide(t *testing.T) {
	doc := read(t, "docs/operations/vault.md")
	for _, k := range []string{"vault.kek.provider", "vault.kek.local.key_file", "cache.credential_ttl", "cache.data_key_ttl"} {
		if !strings.Contains(doc, k) {
			t.Errorf("vault.md does not name %s", k)
		}
	}
	for _, k := range config.Keys() {
		switch k.Name {
		case "vault.kek.provider", "vault.kek.local.key_file", "cache.credential_ttl", "cache.data_key_ttl":
			if !strings.Contains(doc, k.Env) {
				t.Errorf("vault.md does not name the variable %s", k.Env)
			}
		}
	}
	for _, w := range []string{
		"60 s", "15 m", "30 s", "5 m", // the two bounds and the two defaults
		"separately",           // the backup rule (VLT-13)
		"recover-lost-kek.sql", // the recovery script
		"every tenant must re-enter its provider credentials", // what the loss of the key means
		"kek generate", "vault ready",
		"0400", "0440",
	} {
		if !strings.Contains(doc, w) {
			t.Errorf("vault.md lacks %q", w)
		}
	}
	script := read(t, "deploy/postgres/recover-lost-kek.sql")
	for _, w := range []string{":'kek_version'", "pandokos.provider_credential", "pandokos.tenant_data_key", "pandokos.kek_version", "'revoked'"} {
		if !strings.Contains(script, w) {
			t.Errorf("recover-lost-kek.sql lacks %q", w)
		}
	}
	// The permissions, the commands and the codes of the reference pages.
	perms := read(t, "docs/reference/permissions.md")
	for _, p := range []string{"credential:read", "credential:write", "credential:rotate", "credential:delete", "credential:use", "tenant_credential:read", "tenant_credential:revoke"} {
		if !strings.Contains(perms, "`"+p+"`") {
			t.Errorf("permissions.md does not list %s", p)
		}
	}
	api := read(t, "docs/reference/admin-api.md")
	for _, w := range []string{"credential_rejected", "validation_unavailable", "tenant-credential", "kek generate"} {
		if !strings.Contains(api, w) {
			t.Errorf("admin-api.md lacks %q", w)
		}
	}
	// The decision records.
	index := read(t, "docs/adr/README.md")
	for file, title := range map[string]string{
		"docs/adr/0033-vault-formats-and-key-identifiers.md":       "# 0033. Vault: formats, derived keys, key identifiers, caches",
		"docs/adr/0034-credential-model-and-platform-oversight.md": "# 0034. Credential model and platform oversight",
	} {
		if !strings.HasPrefix(read(t, file), title+"\n") {
			t.Errorf("%s does not start with %q", file, title)
		}
		if !strings.Contains(index, strings.TrimSuffix(filepath.Base(file), ".md")) {
			t.Errorf("docs/adr/README.md does not list %s", filepath.Base(file))
		}
	}
}
