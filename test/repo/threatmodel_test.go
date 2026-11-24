package repo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// mdTable returns the rows of the first Markdown table of text whose header row is exactly header
// (cells trimmed), without the header and the separator; nil when there is none.
func mdTable(text string, header ...string) [][]string {
	lines := strings.Split(text, "\n")
	for i := 0; i+1 < len(lines); i++ {
		if !equalCells(mdCells(lines[i]), header) || !isSeparator(lines[i+1]) {
			continue
		}
		rows := [][]string{}
		for _, l := range lines[i+2:] {
			if !strings.HasPrefix(strings.TrimSpace(l), "|") {
				break
			}
			rows = append(rows, mdCells(l))
		}
		return rows
	}
	return nil
}

// mdCells splits a table row into trimmed cells; an escaped bar (\|) stays in its cell.
func mdCells(line string) []string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") {
		return nil
	}
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	const placeholder = "\x00"
	parts := strings.Split(strings.ReplaceAll(line, `\|`, placeholder), "|")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(strings.ReplaceAll(p, placeholder, "|"))
	}
	return parts
}

func isSeparator(line string) bool {
	cells := mdCells(line)
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		if strings.Trim(c, "-: ") != "" || !strings.Contains(c, "-") {
			return false
		}
	}
	return true
}

func equalCells(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var backtickRE = regexp.MustCompile("`([^`]*)`")

// backticked returns the texts between back quotes in a cell.
func backticked(cell string) []string {
	var out []string
	for _, m := range backtickRE.FindAllStringSubmatch(cell, -1) {
		out = append(out, m[1])
	}
	return out
}

// testFunctions maps "package.Function" to the files that declare a top-level Test… or Fuzz…
// function of that name, for every _test.go file under internal, test and cmd; the package is the
// last element of the directory, "main" for cmd/pandokos. Build tags are ignored (go/parser).
func testFunctions(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, top := range []string{"internal", "test", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root(t), top), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root(t), path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			pkg := filepath.Base(filepath.Dir(rel))
			if rel == "cmd/pandokos/"+filepath.Base(rel) {
				pkg = "main"
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil {
					continue
				}
				if strings.HasPrefix(fn.Name.Name, "Test") || strings.HasPrefix(fn.Name.Name, "Fuzz") {
					out[pkg+"."+fn.Name.Name] = append(out[pkg+"."+fn.Name.Name], rel)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

// Helpers used by the tests of part B of sprint 12.2.
var _ = []any{mdTable, backticked, testFunctions, composeImages}
