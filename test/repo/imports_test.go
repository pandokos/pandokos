package repo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/pandokos/pandokos"

// importsAndNames returns the import paths of a Go file, and the name each is known by.
func importsAndNames(t *testing.T, rel string) (paths []string, names map[string]string, file *ast.File) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(root(t), rel))
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), rel, src, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	names = map[string]string{}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		paths = append(paths, p)
		name := filepath.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		names[name] = p
	}
	return paths, names, f
}

func isStd(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

// TestImportsOfThisSprint is T-R35: the layering of the new packages, mirrored by the linter.
func TestImportsOfThisSprint(t *testing.T) {
	t.Run("family/sse imports the standard library only", func(t *testing.T) {
		files := nonTestGoFiles(t, []string{"internal/family/sse"})
		if len(files) == 0 {
			t.Fatal("no file in internal/family/sse")
		}
		for _, rel := range files {
			paths, _, _ := importsAndNames(t, rel)
			for _, p := range paths {
				if !isStd(p) {
					t.Errorf("%s imports %s", rel, p)
				}
			}
		}
	})
	t.Run("family/anthropic imports the standard library, family and family/sse", func(t *testing.T) {
		files := nonTestGoFiles(t, []string{"internal/family/anthropic"})
		if len(files) < 7 {
			t.Fatalf("%d files in internal/family/anthropic", len(files))
		}
		for _, rel := range files {
			paths, _, _ := importsAndNames(t, rel)
			for _, p := range paths {
				if !isStd(p) && p != modulePath+"/internal/family" && p != modulePath+"/internal/family/sse" {
					t.Errorf("%s imports %s", rel, p)
				}
			}
		}
	})
	t.Run("the gateway imports family packages in families.go only", func(t *testing.T) {
		families := 0
		for _, rel := range nonTestGoFiles(t, []string{"internal/gateway"}) {
			paths, _, _ := importsAndNames(t, rel)
			for _, p := range paths {
				if strings.HasPrefix(p, modulePath+"/internal/family/") && filepath.Base(rel) != "families.go" {
					t.Errorf("%s imports %s: the adapters are named in families.go only", rel, p)
				}
				if strings.HasPrefix(p, modulePath+"/internal/family/") {
					families++
				}
			}
		}
		if families < 2 {
			t.Errorf("families.go imports %d family packages, want openai and anthropic", families)
		}
	})
	t.Run("internal/admin imports neither the gateway nor the resolver and never names vault.Opener", func(t *testing.T) {
		files := nonTestGoFiles(t, []string{"internal/admin"})
		if len(files) < 20 {
			t.Fatalf("%d files in internal/admin", len(files))
		}
		for _, rel := range files {
			paths, names, _ := importsAndNames(t, rel)
			for _, p := range paths {
				if p == modulePath+"/internal/gateway" || p == modulePath+"/internal/resolver" {
					t.Errorf("%s imports %s", rel, p)
				}
			}
			src, _ := os.ReadFile(filepath.Join(root(t), rel))
			full, err := parser.ParseFile(token.NewFileSet(), rel, src, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(full, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && names[id.Name] == modulePath+"/internal/vault" && sel.Sel.Name == "Opener" {
						t.Errorf("%s refers to vault.Opener: the admin plane never opens a credential", rel)
					}
				}
				return true
			})
		}
	})
}

// TestJOSEImports is T-R36: the JOSE library of sprint 4.1 is imported only by internal/authn,
// internal/jwks and test/fakeidp, only through its root package, and the root module requires it
// directly, with nothing else new.
func TestJOSEImports(t *testing.T) {
	const jose = "github.com/go-jose/go-jose/v4"
	allowed := []string{"internal/authn/", "internal/jwks/", "test/fakeidp/"}
	importers := map[string]bool{}
	err := filepath.WalkDir(root(t), func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root(t), path)
		rel = filepath.ToSlash(rel)
		if e.IsDir() {
			switch rel {
			case ".git", "local", "bin", "planning":
				return fs.SkipDir
			}
			if filepath.Base(rel) == "node_modules" {
				return fs.SkipDir
			}
			// The nested module of the SDK tests has its own go.mod and its own rules.
			if rel != "." {
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") {
			return nil
		}
		paths, _, _ := importsAndNames(t, rel)
		for _, p := range paths {
			if p == jose || strings.HasPrefix(p, jose+"/") || strings.HasPrefix(p, "github.com/go-jose/") {
				if p != jose {
					t.Errorf("%s imports %s: only the root package of go-jose is used", rel, p)
				}
				importers[rel] = true
				ok := false
				for _, a := range allowed {
					ok = ok || strings.HasPrefix(rel, a)
				}
				if !ok {
					t.Errorf("%s imports the JOSE library, which only %v may", rel, allowed)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Each of the three packages uses it (the module would be dead weight in a package that does not).
	for _, a := range allowed {
		found := false
		for rel := range importers {
			found = found || strings.HasPrefix(rel, a)
		}
		if !found {
			t.Errorf("%s does not import go-jose", a)
		}
	}
	gomod := read(t, "go.mod")
	if !regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(jose) + ` v4\.\d+\.\d+\s*$`).MatchString(gomod) {
		t.Errorf("go.mod does not require %s directly (no // indirect)", jose)
	}
	// The module that it brings requires nothing else: go.sum has its lines and no other new module of the sprint.
	sum := read(t, "go.sum")
	if strings.Count(sum, jose+" ") < 2 {
		t.Errorf("go.sum has %d lines of go-jose, want its go.mod and its zip", strings.Count(sum, jose+" "))
	}
	// The linter enforces the same layering.
	lint := read(t, ".golangci.yml")
	if !strings.Contains(lint, jose) {
		t.Errorf(".golangci.yml does not mention %s", jose)
	}
	for _, dir := range allowed[:2] { // the two packages of the product; the fake is test code
		if !strings.Contains(lint, strings.TrimSuffix(dir, "/")) {
			t.Errorf(".golangci.yml does not mention %s", dir)
		}
	}
}
