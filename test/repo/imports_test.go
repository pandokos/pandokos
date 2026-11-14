package repo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
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
