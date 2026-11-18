package repo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/limits"
)

// TestLimitsImports is T-R40: the layering of sprint 5.1.
func TestLimitsImports(t *testing.T) {
	const limitsPath = modulePath + "/internal/limits"

	t.Run("internal/gateway does not import internal/limits", func(t *testing.T) {
		files := nonTestGoFiles(t, []string{"internal/gateway"})
		if len(files) == 0 {
			t.Fatal("no file in internal/gateway")
		}
		for _, rel := range files {
			paths, _, _ := importsAndNames(t, rel)
			for _, p := range paths {
				if p == limitsPath {
					t.Errorf("%s imports %s: the gateway sees limits through gateway.Limits", rel, p)
				}
			}
		}
	})

	t.Run("internal/limits imports the standard library, tenant, pricing, store and the generated queries", func(t *testing.T) {
		files := nonTestGoFiles(t, []string{"internal/limits"})
		if len(files) < 5 {
			t.Fatalf("%d files in internal/limits", len(files))
		}
		allowed := map[string]bool{
			modulePath + "/internal/tenant":        true,
			modulePath + "/internal/pricing":       true,
			modulePath + "/internal/store":         true,
			modulePath + "/internal/store/queries": true,
		}
		for _, rel := range files {
			paths, _, _ := importsAndNames(t, rel)
			for _, p := range paths {
				if !isStd(p) && !allowed[p] {
					t.Errorf("%s imports %s", rel, p)
				}
			}
		}
	})

	t.Run("only cmd/pandokos, internal/limits and the tests under test/ name the in-process store", func(t *testing.T) {
		names := map[string]bool{"Memory": true, "NewMemory": true, "MemoryOptions": true}
		checked := 0
		err := filepath.WalkDir(root(t), func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root(t), path)
			rel = filepath.ToSlash(rel)
			if e.IsDir() {
				switch {
				case rel == ".git", rel == "local", strings.HasPrefix(rel, "node_modules"), strings.HasSuffix(rel, "/node_modules"),
					rel == "internal/limits", rel == "test", rel == "cmd/pandokos":
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(rel, ".go") {
				return nil
			}
			checked++
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			f, err := parser.ParseFile(token.NewFileSet(), rel, src, 0)
			if err != nil {
				return err
			}
			local := ""
			for _, imp := range f.Imports {
				if strings.Trim(imp.Path.Value, `"`) == limitsPath {
					local = "limits"
					if imp.Name != nil {
						local = imp.Name.Name
					}
				}
			}
			if local == "" {
				return nil
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == local && names[sel.Sel.Name] {
						t.Errorf("%s names limits.%s: only cmd/pandokos builds the store", rel, sel.Sel.Name)
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if checked < 100 {
			t.Fatalf("%d Go files checked: the walk is wrong", checked)
		}
	})
}

// TestLimitsGuide is T-R41: the operations guide names everything the code defines.
func TestLimitsGuide(t *testing.T) {
	guide := read(t, "docs/operations/limits.md")
	for _, m := range limits.Metrics() {
		if !strings.Contains(guide, string(m)) {
			t.Errorf("docs/operations/limits.md does not name the metric %s", m)
		}
	}
	for _, k := range limits.SubjectKinds() {
		if !strings.Contains(guide, string(k)) {
			t.Errorf("docs/operations/limits.md does not name the subject kind %s", k)
		}
	}
	for _, h := range []string{family.HeaderLimitSubject, family.HeaderLimitMetric, family.HeaderLimit, family.HeaderLimitRemaining, family.HeaderLimitReset} {
		if !strings.Contains(guide, h) {
			t.Errorf("docs/operations/limits.md does not name the header %s", h)
		}
	}
	for _, key := range []string{"limits.default_output_tokens", "cache.limits_ttl"} {
		if !strings.Contains(guide, key) {
			t.Errorf("docs/operations/limits.md does not name the configuration key %s", key)
		}
	}
	if !strings.Contains(guide, "rate_limited") {
		t.Error("docs/operations/limits.md does not name the code rate_limited")
	}
}
