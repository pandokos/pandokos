package repo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/usage"
)

const productModule = "github.com/pandokos/pandokos/"

// importsOf returns, for every package of ./cmd/... and ./internal/..., its non-test imports.
func importsOf(t *testing.T) map[string][]string {
	t.Helper()
	cmd := exec.Command("go", "list", "-f", `{{.ImportPath}} {{join .Imports " "}}`, "./cmd/...", "./internal/...")
	cmd.Dir = root(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	m := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			m[strings.TrimPrefix(fields[0], productModule)] = fields[1:]
		}
	}
	return m
}

// TestTelemetryImports is T-R28 (sprint 3.2, section 4): the OpenTelemetry SDK, the exporter, the
// protobuf modules and logr are imported by internal/telemetry only; the gateway imports of
// OpenTelemetry the API packages trace, attribute, codes and propagation; internal/usage imports
// the standard library, tenant, family and pricing; internal/ledger imports usage, store,
// store/queries and tenant, and cmd/pandokos alone imports it; internal/family is a leaf.
func TestTelemetryImports(t *testing.T) {
	imports := importsOf(t)
	forbiddenOutsideTelemetry := []string{
		"go.opentelemetry.io/otel/sdk", "go.opentelemetry.io/otel/exporters", "github.com/go-logr/", "go.opentelemetry.io/proto", "google.golang.org/protobuf",
	}

	t.Run("the SDK, the exporter, logr and protobuf stay in internal/telemetry", func(t *testing.T) {
		for pkg, imps := range imports {
			for _, imp := range imps {
				for _, f := range forbiddenOutsideTelemetry {
					if strings.HasPrefix(imp, f) && pkg != "internal/telemetry" {
						t.Errorf("%s imports %s: only internal/telemetry may", pkg, imp)
					}
				}
			}
		}
		var sdk, exporter, bridge bool
		for _, imp := range imports["internal/telemetry"] {
			sdk = sdk || strings.HasPrefix(imp, "go.opentelemetry.io/otel/sdk")
			exporter = exporter || strings.HasPrefix(imp, "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp")
			bridge = bridge || strings.HasPrefix(imp, "github.com/go-logr/logr")
		}
		if !sdk || !exporter || !bridge {
			t.Errorf("internal/telemetry imports the SDK %v, the exporter %v, logr %v: the test would prove nothing", sdk, exporter, bridge)
		}
	})

	t.Run("the gateway imports the API packages of OpenTelemetry only", func(t *testing.T) {
		allowed := map[string]bool{}
		for _, a := range []string{"trace", "attribute", "codes", "propagation"} {
			allowed["go.opentelemetry.io/otel/"+a] = true
		}
		used := 0
		for _, imp := range imports["internal/gateway"] {
			if strings.HasPrefix(imp, "go.opentelemetry.io/") {
				used++
				if !allowed[imp] {
					t.Errorf("internal/gateway imports %s", imp)
				}
			}
		}
		if used == 0 {
			t.Error("internal/gateway imports no OpenTelemetry package: the test would prove nothing")
		}
		for _, d := range internalDeps(t, "gateway") {
			if strings.HasPrefix(d, "go.opentelemetry.io/otel/sdk") || strings.HasPrefix(d, "go.opentelemetry.io/otel/exporters") {
				t.Errorf("internal/gateway reaches %s", d)
			}
		}
	})

	only := func(t *testing.T, pkg string, allowed ...string) {
		t.Helper()
		ok := map[string]bool{}
		for _, a := range allowed {
			ok[productModule+a] = true
		}
		n := 0
		for _, imp := range imports[pkg] {
			first, _, _ := strings.Cut(imp, "/")
			switch {
			case strings.HasPrefix(imp, productModule):
				n++
				if !ok[imp] {
					t.Errorf("%s imports %s", pkg, imp)
				}
			case strings.Contains(first, "."):
				t.Errorf("%s imports %s, which is not the standard library or the product", pkg, imp)
			}
		}
		if n == 0 {
			t.Errorf("%s imports nothing of the product: the test would prove nothing", pkg)
		}
	}
	t.Run("what usage imports", func(t *testing.T) {
		only(t, "internal/usage", "internal/tenant", "internal/family", "internal/pricing")
	})
	t.Run("what ledger imports", func(t *testing.T) {
		only(t, "internal/ledger", "internal/usage", "internal/store", "internal/store/queries", "internal/tenant")
	})
	t.Run("who imports the ledger", func(t *testing.T) {
		importers := 0
		for pkg, imps := range imports {
			for _, imp := range imps {
				if imp == productModule+"internal/ledger" {
					importers++
					if pkg != "cmd/pandokos" {
						t.Errorf("%s imports internal/ledger: only cmd/pandokos may", pkg)
					}
				}
			}
		}
		if importers != 1 {
			t.Errorf("%d packages import internal/ledger, want exactly cmd/pandokos", importers)
		}
	})
	t.Run("the gateway does not reach the ledger or the telemetry package", func(t *testing.T) {
		for _, d := range internalDeps(t, "gateway") {
			if d == productModule+"internal/ledger" || d == productModule+"internal/telemetry" {
				t.Errorf("internal/gateway reaches %s", d)
			}
		}
	})
	t.Run("internal/family stays a leaf", func(t *testing.T) {
		for _, imp := range imports["internal/family"] {
			if strings.HasPrefix(imp, productModule) {
				t.Errorf("internal/family imports %s", imp)
			}
		}
	})
}

// section returns the text of a Markdown table row or heading block: the whole document, for the
// guides that are checked by names.
func tableRows(doc string) [][]string {
	var rows [][]string
	for _, l := range strings.Split(doc, "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "|") || strings.Trim(l, "|-: ") == "" {
			continue
		}
		var cells []string
		for _, c := range strings.Split(strings.Trim(l, "|"), "|") {
			cells = append(cells, strings.TrimSpace(c))
		}
		rows = append(rows, cells)
	}
	return rows
}

// TestUsageAndTelemetryGuides is T-R29: the guides of sprint 3.2 name what the product defines.
func TestUsageAndTelemetryGuides(t *testing.T) {
	mig := read(t, "internal/store/migrations/sql/0006_usage_ledger.sql")

	t.Run("usage.md", func(t *testing.T) {
		doc := read(t, "docs/operations/usage.md")
		// Every column of the table, read from the migration.
		create := mig[strings.Index(mig, "CREATE TABLE pandokos.usage_record ("):]
		create = create[:strings.Index(create, ") PARTITION BY")]
		cols := 0
		for _, m := range regexp.MustCompile(`(?m)^    ([a-z_]+)\s+(?:uuid|text|timestamptz|boolean|integer|bigint|jsonb|numeric\(\d+,\d+\)|text\[\])[\s,]`).FindAllStringSubmatch(create, -1) {
			cols++
			if !strings.Contains(doc, "`"+m[1]+"`") {
				t.Errorf("usage.md does not name the column %s", m[1])
			}
		}
		if cols != 44 {
			t.Errorf("read %d columns from the migration, want 44", cols)
		}
		for _, o := range []usage.Outcome{usage.OutcomeOK, usage.OutcomeUpstreamError, usage.OutcomeCancelled, usage.OutcomeTimeout, usage.OutcomeRejectedLimit, usage.OutcomeRejectedPolicy, usage.OutcomeInternalError} {
			if !strings.Contains(doc, "`"+string(o)+"`") {
				t.Errorf("usage.md does not name the outcome %s", o)
			}
		}
		for _, s := range []usage.Source{usage.SourceProvider, usage.SourceEstimated, usage.SourceNone} {
			if !strings.Contains(doc, "`"+string(s)+"`") {
				t.Errorf("usage.md does not name the source %s", s)
			}
		}
		for _, c := range []string{"known", "unknown", "none", "no_price", "category_unpriced", "inconsistent_usage"} {
			if !strings.Contains(doc, "`"+c+"`") {
				t.Errorf("usage.md does not name the cost status or reason %s", c)
			}
		}
		for _, w := range []string{"X-Pandokos-Metadata-", "X-Pandokos-Metadata-Dropped", "loss window", "informational", "default partition", "usage record not written", "usage record rejected", "ledger write failing"} {
			if !strings.Contains(strings.ToLower(doc), strings.ToLower(w)) {
				t.Errorf("usage.md lacks %q", w)
			}
		}
		for _, bound := range []string{"8", "32", "128"} { // entries, key length and value length of caller metadata
			if !regexp.MustCompile(`\b` + bound + `\b`).MatchString(doc) {
				t.Errorf("usage.md does not state the metadata bound %s", bound)
			}
		}
		for _, k := range config.Keys() {
			if strings.HasPrefix(k.Name, "ledger.") && !strings.Contains(doc, k.Name) {
				t.Errorf("usage.md does not name %s", k.Name)
			}
		}
	})

	t.Run("telemetry.md", func(t *testing.T) {
		doc := read(t, "docs/operations/telemetry.md")
		for _, k := range gateway.SpanAttributes {
			if !strings.Contains(doc, "`"+k+"`") {
				t.Errorf("telemetry.md does not name the span attribute %s", k)
			}
		}
		keys := 0
		for _, k := range config.Keys() {
			if strings.HasPrefix(k.Name, "telemetry.") {
				keys++
				if !strings.Contains(doc, k.Name) {
					t.Errorf("telemetry.md does not name %s", k.Name)
				}
			}
		}
		if keys != 8 {
			t.Errorf("%d telemetry keys, want 8", keys)
		}
		for _, w := range []string{"OTEL_RESOURCE_ATTRIBUTES", "OTEL_", "traceparent", "upstream.tls.ca_file", "sampl", "propagat"} {
			if !strings.Contains(doc, w) {
				t.Errorf("telemetry.md lacks %q", w)
			}
		}
	})

	t.Run("data-flows.md", func(t *testing.T) {
		doc := read(t, "docs/reference/data-flows.md")
		rows := tableRows(doc)
		if len(rows) < 5 {
			t.Fatalf("the data-flow table has %d rows", len(rows))
		}
		header := strings.ToLower(strings.Join(rows[0], "|"))
		if !strings.Contains(header, "receives") || !strings.Contains(header, "never") {
			t.Fatalf("the header of the table: %v", rows[0])
		}
		for _, dest := range []string{"provider", "collector", "database", "log"} {
			found := false
			for _, r := range rows[1:] {
				if len(r) >= 3 && strings.Contains(strings.ToLower(r[0]), dest) {
					found = true
					if r[1] == "" || r[2] == "" {
						t.Errorf("the row of %s has an empty cell", dest)
					}
				}
			}
			if !found {
				t.Errorf("data-flows.md has no row for the destination %s", dest)
			}
		}
	})

	t.Run("the gateway guide", func(t *testing.T) {
		doc := read(t, "docs/operations/gateway.md")
		for _, w := range []string{"chat.completions", "responses", "embeddings", "/v1/chat/completions", "/v1/responses", "/v1/embeddings", "background", "X-Pandokos-Metadata-", "X-Pandokos-Metadata-Dropped", "ttfb_ms"} {
			if !strings.Contains(doc, w) {
				t.Errorf("gateway.md lacks %q", w)
			}
		}
	})

	t.Run("the migration has what the guides describe", func(t *testing.T) {
		if !strings.Contains(mig, "ensure_usage_partitions") || !strings.Contains(mig, "usage_record_default") {
			t.Error("migration 0006 lacks the partition function or the default partition")
		}
	})
}

// TestFuzzTargetsAreRun is T-R30: every Fuzz function under internal/ is in the fuzz target of the
// Makefile, with the directory of its package.
func TestFuzzTargetsAreRun(t *testing.T) {
	makefile := read(t, "Makefile")
	i := strings.Index(makefile, "\nfuzz:")
	if i < 0 {
		t.Fatal("the Makefile has no fuzz target")
	}
	block := makefile[i+1:]
	if j := strings.Index(block, "\n\n"); j >= 0 {
		block = block[:j]
	}
	runs := map[string]bool{} // "dir name"
	re := regexp.MustCompile(`go test (\./[^ ]+) .*-fuzz '\^(Fuzz[A-Za-z0-9_]+)\$\$'`)
	for _, m := range re.FindAllStringSubmatch(block, -1) {
		runs[strings.TrimSuffix(m[1], "/")+" "+m[2]] = true
	}
	if len(runs) < 5 {
		t.Fatalf("read %d fuzz commands from the Makefile", len(runs))
	}
	found := map[string]bool{}
	err := filepath.WalkDir(filepath.Join(root(t), "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		dir, _ := filepath.Rel(root(t), filepath.Dir(path))
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Fuzz") {
				found["./"+filepath.ToSlash(dir)+" "+fn.Name.Name] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for k := range found {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if !runs[k] {
			t.Errorf("the fuzz target of the Makefile does not run %s", k)
		}
	}
	for _, want := range []string{"FuzzResponsesStreamReader", "FuzzFlattenUsage", "FuzzParseMetadata"} {
		ok := false
		for k := range found {
			ok = ok || strings.HasSuffix(k, " "+want)
		}
		if !ok {
			t.Errorf("no Fuzz function named %s exists under internal/", want)
		}
	}
	for k := range runs {
		if !found[k] {
			t.Errorf("the Makefile runs %s, which does not exist", k)
		}
	}
}

// usageTables are the tables whose rows only the tenant reads (sprint 5.4, section 5.10).
var usageTables = []string{"pandokos.usage_record", "pandokos.limit_event", "pandokos.tenant_price_version", "pandokos.tenant_price_rate"}

// usageStatements returns, from the generated query files, the methods whose SQL names one of the tables.
func usageStatements(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	fset := token.NewFileSet()
	files, err := filepath.Glob(filepath.Join(root(t), "internal/store/queries/*.sql.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no generated query files: %v", err)
	}
	for _, file := range files {
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		sqlOf := map[string]string{}
		for _, d := range f.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.CONST {
				for _, sp := range g.Specs {
					vs := sp.(*ast.ValueSpec)
					for i, n := range vs.Names {
						if lit, ok := vs.Values[i].(*ast.BasicLit); ok {
							sqlOf[n.Name] = lit.Value
						}
					}
				}
			}
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok {
					for _, tbl := range usageTables {
						if strings.Contains(sqlOf[id.Name], tbl) {
							out[fn.Name.Name] = true
						}
					}
				}
				return true
			})
		}
	}
	if len(out) < 4 {
		t.Fatalf("only %d statements on the usage tables were found: the test proves nothing", len(out))
	}
	return out
}

// tenantOnly are the helpers that run a function in the session of a tenant.
var tenantOnly = map[string]bool{"WithTenant": true, "inTenant": true, "inTenantPath": true}

// TestUsageStaysWithTheTenant is T-R52: the statements that read usage_record, limit_event or a price
// table of a tenant (found by their SQL) are called only inside WithTenant, inTenant or inTenantPath
// (or in a function only called from those), never inside Platform or inPlatform; no handler of an
// operation with a platform permission in internal/admin reaches one; and the gateway's request line
// builds the fields of section 5.11 in one function only.
func TestUsageStaysWithTheTenant(t *testing.T) {
	stmts := usageStatements(t)
	fset := token.NewFileSet()

	t.Run("calls are inside a tenant session, never a platform one", func(t *testing.T) {
		type site struct {
			pos      token.Position
			fn       string // the function declaration that holds the call
			tenant   bool   // inside a function literal passed to a tenant helper
			platform bool   // inside a function literal passed to Platform or inPlatform
		}
		var sites []site
		calledFrom := map[string][]bool{} // function name -> for each call of it, whether the call is inside a tenant literal
		for _, file := range goFiles(t, "cmd", "internal") {
			if p := filepath.ToSlash(file); strings.Contains(p, "internal/store/queries/") || strings.Contains(p, "internal/adminapi/") { // generated; the names of its methods are those of operations
				continue
			}
			f, err := parser.ParseFile(fset, file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				var stack []ast.Node
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if n == nil {
						stack = stack[:len(stack)-1]
						return true
					}
					stack = append(stack, n)
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					name := ""
					switch c := call.Fun.(type) {
					case *ast.SelectorExpr:
						name = c.Sel.Name
					case *ast.Ident:
						name = c.Name
					}
					tenantCtx, platformCtx := false, false
					for i := len(stack) - 2; i >= 0; i-- {
						lit, isLit := stack[i].(*ast.FuncLit)
						if !isLit || i == 0 {
							continue
						}
						if outer, ok := stack[i-1].(*ast.CallExpr); ok {
							helper := ""
							switch c := outer.Fun.(type) {
							case *ast.SelectorExpr:
								helper = c.Sel.Name
							case *ast.Ident:
								helper = c.Name
							}
							_ = lit
							tenantCtx = tenantCtx || tenantOnly[helper]
							platformCtx = platformCtx || helper == "Platform" || helper == "inPlatform"
						}
					}
					if stmts[name] {
						sites = append(sites, site{fset.Position(call.Pos()), fn.Name.Name, tenantCtx, platformCtx})
					}
					calledFrom[name] = append(calledFrom[name], tenantCtx)
					return true
				})
			}
		}
		if len(sites) == 0 {
			t.Fatal("no call of a statement on the usage tables was found: the test proves nothing")
		}
		onlyFromTenant := func(fn string) bool {
			calls := calledFrom[fn]
			if len(calls) == 0 {
				return false
			}
			for _, c := range calls {
				if !c {
					return false
				}
			}
			return true
		}
		for _, s := range sites {
			if s.platform {
				t.Errorf("%s: a statement on a tenant's usage or prices is called inside a platform session", s.pos)
			}
			if !s.tenant && !onlyFromTenant(s.fn) {
				t.Errorf("%s: %s calls a statement on a tenant's usage or prices outside WithTenant, inTenant and inTenantPath, and is not only called from them", s.pos, s.fn)
			}
		}
	})

	t.Run("no handler of a platform operation reaches one", func(t *testing.T) {
		body := map[string]*ast.FuncDecl{}
		for _, file := range goFiles(t, "internal/admin") {
			f, err := parser.ParseFile(fset, file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range f.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && fn.Body != nil {
					body[fn.Name.Name] = fn
				}
			}
		}
		reaches := func(start string) bool {
			seen := map[string]bool{}
			var walk func(name string) bool
			walk = func(name string) bool {
				fn := body[name]
				if fn == nil || seen[name] {
					return false
				}
				seen[name] = true
				found := false
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok || found {
						return !found
					}
					callee := ""
					switch c := call.Fun.(type) {
					case *ast.SelectorExpr:
						callee = c.Sel.Name
					case *ast.Ident:
						callee = c.Name
					}
					if stmts[callee] || (callee != name && walk(callee)) {
						found = true
					}
					return !found
				})
				return found
			}
			return walk(start)
		}
		checked := 0
		for _, op := range adminapi.Operations {
			perm, ok := authz.Lookup(op.Permission)
			if !ok || perm.Scope != authz.ScopePlatform {
				continue
			}
			handler := strings.ToUpper(op.ID[:1]) + op.ID[1:]
			if body[handler] == nil {
				t.Errorf("no handler %s for %s", handler, op.ID)
				continue
			}
			checked++
			if reaches(handler) {
				t.Errorf("%s (a platform operation) reaches a statement on a tenant's usage, limit events or prices", handler)
			}
		}
		if checked < 40 {
			t.Errorf("%d platform handlers checked", checked)
		}
	})

	t.Run("the request line builds the fields of 5.11 in one function", func(t *testing.T) {
		keys := map[string]bool{`"cost"`: true, `"currency"`: true, `"reserved_tokens"`: true, `"limit"`: true, `"limit_currency"`: true, `"price_currency"`: true, `"price_source"`: true}
		holders := map[string][]string{}
		for _, file := range goFiles(t, "internal/gateway") {
			f, err := parser.ParseFile(fset, file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if lit, ok := n.(*ast.BasicLit); ok && keys[lit.Value] {
						holders[fn.Name.Name] = append(holders[fn.Name.Name], lit.Value)
					}
					return true
				})
			}
		}
		if len(holders) != 1 {
			t.Errorf("the log keys of section 5.11 are string literals in %d functions (%v), want the one function that builds the gated fields", len(holders), holders)
		}
	})
}
