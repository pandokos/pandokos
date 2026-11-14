package repo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/config"
)

// TestPricingIsPure is T-R23: internal/pricing and internal/pricing/genaiprices import the
// standard library, internal/family and each other only (no store, no network, no files: the
// snapshot is embedded), and no file of theirs but a test names float32 or float64.
func TestPricingIsPure(t *testing.T) {
	const mod = "github.com/pandokos/pandokos/internal/"
	// The standard-library packages that reach outside the process.
	banned := map[string]bool{"net": true, "net/http": true, "net/url": true, "os": true, "os/exec": true, "io/ioutil": true, "syscall": true, "database/sql": true, "plugin": true}
	for pkg, allowed := range map[string][]string{
		"pricing":             {"family"},
		"pricing/genaiprices": {"family", "pricing"},
	} {
		cmd := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, "./internal/"+pkg)
		cmd.Dir = root(t)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list ./internal/%s: %v", pkg, err)
		}
		imports := strings.Fields(string(out))
		if len(imports) == 0 {
			t.Errorf("internal/%s has no imports: the test would prove nothing", pkg)
		}
		ok := map[string]bool{}
		for _, a := range allowed {
			ok[mod+a] = true
		}
		for _, imp := range imports {
			first, _, _ := strings.Cut(imp, "/")
			switch {
			case strings.HasPrefix(imp, mod):
				if !ok[imp] {
					t.Errorf("internal/%s imports %s", pkg, imp)
				}
			case strings.Contains(first, "."):
				t.Errorf("internal/%s imports %s, which is not the standard library or the product", pkg, imp)
			case banned[imp]:
				t.Errorf("internal/%s imports %s", pkg, imp)
			}
		}

		// No floating-point type in a file that is not a test: not even a conversion.
		dir := filepath.Join(root(t), "internal", filepath.FromSlash(pkg))
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		files := 0
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			files++
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && (id.Name == "float32" || id.Name == "float64") {
					t.Errorf("internal/%s/%s:%d names %s: money is exact (CAT-08)", pkg, e.Name(), fset.Position(id.Pos()).Line, id.Name)
				}
				return true
			})
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			// A float can come in through a library function too.
			for _, w := range []string{"ParseFloat", "big.NewFloat", "math.Float"} {
				if strings.Contains(string(b), w) {
					t.Errorf("internal/%s/%s uses %s", pkg, e.Name(), w)
				}
			}
		}
		if files == 0 {
			t.Errorf("internal/%s has no source file", pkg)
		}
	}
}

// TestPriceSnapshot is T-R24: the embedded price list carries its licence and its provenance, and
// the operations guide of the catalogue states what an operator must know about it.
func TestPriceSnapshot(t *testing.T) {
	dir := "internal/pricing/genaiprices/snapshot/"
	licence := read(t, dir+"LICENSE")
	for _, w := range []string{"MIT License", "Pydantic", "Permission is hereby granted"} {
		if !strings.Contains(licence, w) {
			t.Errorf("the snapshot LICENSE lacks %q", w)
		}
	}
	source := read(t, dir+"SOURCE")
	if !regexp.MustCompile(`(?m)^source: https://raw\.githubusercontent\.com/pydantic/genai-prices/[0-9a-f]{40}/prices/new_data/v2/data\.json$`).MatchString(source) {
		t.Errorf("SOURCE does not name the URL of a commit of the v2 file (a branch is not a pin):\n%s", source)
	}
	for _, field := range []string{"commit", "retrieved", "sha256", "license"} {
		if !regexp.MustCompile(`(?m)^` + field + `: \S`).MatchString(source) {
			t.Errorf("SOURCE has no %s line", field)
		}
	}
	if _, err := os.Stat(filepath.Join(root(t), dir, "data.json")); err != nil {
		t.Error(err)
	}

	doc := read(t, "docs/operations/catalogue.md")
	for _, k := range config.Keys() {
		switch k.Name {
		case "cache.catalog_ttl", "pricing.import_at_start", "pricing.refresh.enabled", "pricing.refresh.url", "pricing.refresh.interval":
			for _, want := range []string{k.Name, k.Env} {
				if !strings.Contains(doc, want) {
					t.Errorf("catalogue.md does not name %s", want)
				}
			}
		}
	}
	for _, w := range []string{
		"MIT",         // the licence of the snapshot
		"30 s", "5 m", // the default and the maximum of cache.catalog_ttl (section 5.7)
		"imports never change an override", // the rule of section 5.5, in these words
		"import-prices --file",             // the command of an operator behind a proxy
	} {
		if !strings.Contains(doc, w) {
			t.Errorf("catalogue.md lacks %q", w)
		}
	}
}
