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

// TestPlanesDoNotImportEachOther is T-R13.
func TestPlanesDoNotImportEachOther(t *testing.T) {
	const mod = "github.com/pandokos/pandokos/internal/"
	rules := map[string][]string{
		"gateway":  {"admin", "adminapi", "authn", "cli", "store"},
		"admin":    {"gateway", "family", "server", "config"},
		"cli":      {"store", "admin", "config"},
		"authz":    {"store", "admin", "gateway", "config", "authn"},
		"adminapi": {"store", "admin", "gateway", "authn", "authz", "config", "cli"},
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

// TestOpenAPIDocument is T-R15.
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
	if strings.Join(withSecret, ",") != "PlatformKeyCreated,VirtualKeyCreated" {
		t.Errorf("secret occurs in %v, want only the two ...Created schemas", withSecret)
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
