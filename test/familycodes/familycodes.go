// Package familycodes lists the error codes of internal/family from its source, so that a test
// that sweeps "every code" ranges over the product's own list and fails on a code it does not
// know. It imports nothing of the product, so that every package's tests may use it.
package familycodes

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// root returns the directory of the product module's go.mod.
func root(tb testing.TB) string {
	tb.Helper()
	dir, err := os.Getwd()
	if err != nil {
		tb.Fatal(err)
	}
	for {
		b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.Contains(string(b), "module github.com/pandokos/pandokos\n") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			tb.Fatal("repository root not found")
		}
		dir = parent
	}
}

// All returns the values of the constants of type ErrorCode declared in internal/family/family.go
// (the constants whose name starts with Code), sorted.
func All(tb testing.TB) []string {
	tb.Helper()
	file := filepath.Join(root(tb), "internal", "family", "family.go")
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		tb.Fatal(err)
	}
	var out []string
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Code") || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					tb.Fatal(err)
				}
				out = append(out, v)
			}
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		tb.Fatal("no error code found in internal/family/family.go")
	}
	return out
}
