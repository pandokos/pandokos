package repo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
)

func parseFile(t *testing.T, fset *token.FileSet, path string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func relPath(t *testing.T, path string) string {
	t.Helper()
	rel, _ := filepath.Rel(root(t), path)
	return filepath.ToSlash(rel)
}

// calledName returns the name a call expression calls: X.Name(...) or Name(...).
func calledName(call *ast.CallExpr) string {
	switch f := call.Fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.Ident:
		return f.Name
	}
	return ""
}

// tenantReportStatements are the statements that read a tenant's usage, its audit log or its metadata
// policy (sprint 6.2): none runs in platform scope.
var tenantReportStatements = []string{"UsageReport", "ExportUsageRecords", "RollUpUsage", "LockUsageRollup", "TenantRetention", "ListAuditEvents", "ExportAuditEvents",
	"GetAuditHead", "GetMetadataKeyPolicy", "SetMetadataKeyPolicy", "ClearMetadataKeyPolicy"}

// TestPlatformReadsNoTenantReports is T-R71 (ADR 0054): no function that runs in platform scope calls
// a statement that reads a tenant's usage, audit log or metadata policy; a handler of a platform-scoped
// operation calls none either; internal/reports opens platform scope for ListTenantsForWorker and
// PlatformRetention only.
func TestPlatformReadsNoTenantReports(t *testing.T) {
	banned := map[string]bool{}
	for _, s := range tenantReportStatements {
		banned[s] = true
	}
	fset := token.NewFileSet()
	platformHandlers := map[string]bool{}
	for _, op := range adminapi.Operations {
		if p, ok := authz.Lookup(op.Permission); ok && p.Scope == authz.ScopePlatform {
			platformHandlers[strings.ToUpper(op.ID[:1])+op.ID[1:]] = true
		}
	}
	for _, file := range goFiles(t, "cmd", "internal") {
		rel := relPath(t, file)
		if strings.HasPrefix(rel, "internal/store/queries/") {
			continue // the generated statements
		}
		f := parseFile(t, fset, file)
		inReports := strings.HasPrefix(rel, "internal/reports/")
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := calledName(call)
			if name != "Platform" && name != "inPlatform" {
				return true
			}
			for _, arg := range call.Args {
				lit, ok := arg.(*ast.FuncLit)
				if !ok {
					continue
				}
				ast.Inspect(lit, func(m ast.Node) bool {
					c, ok := m.(*ast.CallExpr)
					if !ok {
						return true
					}
					cn := calledName(c)
					if banned[cn] {
						t.Errorf("%s: %s is called in platform scope", fset.Position(c.Pos()), cn)
					}
					if inReports && (cn == "ListTenantsForWorker" || cn == "PlatformRetention") {
						return true
					}
					if inReports && isQueryCall(c) && cn != "New" { // queries.New(tx) builds the statements, it runs none
						t.Errorf("%s: internal/reports runs %s in platform scope: only ListTenantsForWorker and PlatformRetention", fset.Position(c.Pos()), cn)
					}
					return true
				})
			}
			return true
		})
		// A handler of a platform-scoped operation never reads a tenant's reports.
		if strings.HasPrefix(rel, "internal/admin/") {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv == nil || !platformHandlers[fn.Name.Name] {
					continue
				}
				ast.Inspect(fn, func(m ast.Node) bool {
					if c, ok := m.(*ast.CallExpr); ok && banned[calledName(c)] {
						// Sprint 6.3 (T-R71 changed): the two operations of the kept log of a deleted tenant read its
						// head and export it, in the tenant's session, for a tenant that is deleted; nothing else.
						keptLog := (fn.Name.Name == "GetDeletedTenantAuditHead" && calledName(c) == "GetAuditHead") ||
							(fn.Name.Name == "ExportDeletedTenantAuditEvents" && calledName(c) == "ExportAuditEvents")
						if !keptLog {
							t.Errorf("%s: the platform-scoped handler %s calls %s", fset.Position(c.Pos()), fn.Name.Name, calledName(c))
						}
					}
					return true
				})
			}
		}
	}
	if len(platformHandlers) == 0 {
		t.Error("the operations of the document list no platform-scoped operation")
	}
}

// isQueryCall is true for a call of a method of the generated queries: q.Something(ctx, ...).
func isQueryCall(c *ast.CallExpr) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok || len(c.Args) == 0 {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && (id.Name == "q" || id.Name == "queries")
}

// TestAuditChainOneDefinition is T-R72: the canonical encoding and the hash are written in Go only in
// internal/auditchain and in SQL only in migration 0015; internal/auditchain imports no product
// package.
func TestAuditChainOneDefinition(t *testing.T) {
	fset := token.NewFileSet()
	for _, file := range goFiles(t, "cmd", "internal", "test") {
		rel := relPath(t, file)
		if strings.HasPrefix(rel, "internal/auditchain/") {
			continue
		}
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "pandokos-audit-v1") {
			t.Errorf("%s holds the version tag of the canonical encoding: only internal/auditchain computes it", rel)
		}
		_ = fset
	}
	migrations, err := filepath.Glob(filepath.Join(root(t), "internal/store/migrations/sql/*.sql"))
	if err != nil || len(migrations) == 0 {
		t.Fatalf("no migrations: %v", err)
	}
	for _, fn := range []string{"audit_field", "audit_canonical", "audit_row_hash"} {
		re := regexp.MustCompile(`(?i)CREATE\s+(OR\s+REPLACE\s+)?FUNCTION\s+pandokos\.` + fn + `\(`)
		var in []string
		for _, m := range migrations {
			b, err := os.ReadFile(m)
			if err != nil {
				t.Fatal(err)
			}
			if re.Match(b) {
				in = append(in, filepath.Base(m))
			}
		}
		if len(in) != 1 || !strings.HasPrefix(in[0], "0015_") {
			t.Errorf("the SQL function %s is defined in %v, want migration 0015 only", fn, in)
		}
	}
	for _, file := range goFiles(t, "internal/auditchain") {
		f := parseFile(t, fset, file)
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(p, productModule) {
				t.Errorf("%s imports %s: internal/auditchain imports no product package", relPath(t, file), p)
			}
		}
	}
}

// TestReportsRecords is T-R73: ADR 0064, 0065 and 0066 exist, are indexed and name their sprint; the
// ten records that sprint 6.2 touches have a history line that says so.
func TestReportsRecords(t *testing.T) {
	adrs := map[string]string{
		"0064": "0064-usage-rollup-reports-and-exports.md",
		"0065": "0065-the-audit-hash-chain-reading-and-retention.md",
		"0066": "0066-declarative-export-and-apply.md",
	}
	index := read(t, "docs/adr/README.md")
	for num, file := range adrs {
		doc, err := os.ReadFile(filepath.Join(root(t), "docs/adr", file))
		if err != nil {
			t.Errorf("ADR %s: %v", num, err)
			continue
		}
		if !strings.Contains(string(doc), "# "+num+".") {
			t.Errorf("ADR %s does not start with its number", num)
		}
		if !strings.Contains(index, "["+num+"]("+file+")") {
			t.Errorf("docs/adr/README.md does not index ADR %s", num)
		}
		if !strings.Contains(string(doc), "## History") {
			t.Errorf("ADR %s has no History section", num)
		}
	}
	want := map[string][]string{
		"0013-ledger-and-audit":                                                  {"0064", "0065"},
		"0019-configuration":                                                     {"0066"},
		"0028-admin-api-generator":                                               {"0064"},
		"0032-audit-log-first-version":                                           {"0065"},
		"0039-usage-record-admission-and-ledger-writer":                          {"0064"},
		"0043-tenant-capability-check-and-usage-list":                            {"0064"},
		"0051-period-counters-and-the-ledger":                                    {"0064"},
		"0054-usage-data-is-the-tenants":                                         {"0064"},
		"0061-support-actions-and-reasons-how-the-platform-acts-inside-a-tenant": {"0065"},
		"0062-metrics-instruments-attributes-cardinality-and-scrape":             {},
	}
	for adr, refs := range want {
		doc := read(t, "docs/adr/"+adr+".md")
		h := regexp.MustCompile(`(?m)^## History\s*$`).FindStringIndex(doc)
		if h == nil {
			t.Errorf("%s has no History section", adr)
			continue
		}
		var line string
		for _, l := range strings.Split(doc[h[0]:], "\n") {
			if strings.Contains(l, "sprint 6.2") {
				line = l
			}
		}
		if line == "" {
			t.Errorf("%s has no history line that names sprint 6.2", adr)
			continue
		}
		for _, r := range refs {
			if !strings.Contains(line, "ADR "+r) {
				t.Errorf("%s: the history line %q does not name ADR %s", adr, line, r)
			}
		}
	}
}

// TestLedgerReaderFromLedgerOnlyInTests is T-R74: the old body of the aggregate of the limits is kept
// for the property test, and called from nowhere else.
func TestLedgerReaderFromLedgerOnlyInTests(t *testing.T) {
	fset := token.NewFileSet()
	found := false
	for _, file := range goFiles(t, "cmd", "internal", "test") {
		rel := relPath(t, file)
		if strings.HasPrefix(rel, "internal/store/queries/") {
			continue // the generated definition
		}
		f := parseFile(t, fset, file)
		ast.Inspect(f, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok && calledName(c) == "AggregateUsageForLimitsFromLedger" {
				t.Errorf("%s: AggregateUsageForLimitsFromLedger is called outside a test file", fset.Position(c.Pos()))
			}
			return true
		})
	}
	// It is called by a test, so that it is not dead.
	tests, _ := filepath.Glob(filepath.Join(root(t), "internal/reports/*_test.go"))
	for _, f := range tests {
		if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), "AggregateUsageForLimitsFromLedger") {
			found = true
		}
	}
	if !found {
		t.Error("no test in internal/reports calls AggregateUsageForLimitsFromLedger")
	}
}

// TestPriceDetailActions is T-R75: every audit event of internal/admin whose detail holds a rate, an
// amount of a price list, or the active currency is named in admin.PriceDetailActions, which is what
// is withheld from a reader without price_list:read.
func TestPriceDetailActions(t *testing.T) {
	fset := token.NewFileSet()
	seen := map[string]bool{}
	for _, file := range goFiles(t, "internal/admin") {
		f := parseFile(t, fset, file)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			keysOf := detailKeys(fn)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || calledName(call) != "tenantEvent" || len(call.Args) < 5 {
					return true
				}
				action, resource := stringArg(call.Args[1]), stringArg(call.Args[2])
				if action == "" {
					return true
				}
				seen[action] = true
				keys := map[string]bool{}
				switch d := call.Args[4].(type) {
				case *ast.CompositeLit:
					for _, k := range literalKeys(d) {
						keys[k] = true
					}
				case *ast.Ident:
					for k := range keysOf[d.Name] {
						keys[k] = true
					}
				}
				holdsPrice := keys["rates"] || (strings.HasPrefix(resource, "tenant_price") && (keys["amount"] || keys["currency"])) || action == "tenant.active_currency_update"
				if holdsPrice && !admin.PriceDetailActions[action] {
					t.Errorf("%s: the detail of %s holds a price, a rate or the active currency but the action is not in admin.PriceDetailActions", fset.Position(call.Pos()), action)
				}
				return true
			})
		}
	}
	var listed []string
	for a := range admin.PriceDetailActions {
		listed = append(listed, a)
		if !seen[a] {
			t.Errorf("admin.PriceDetailActions names %s, which no tenantEvent call of internal/admin writes", a)
		}
	}
	sort.Strings(listed)
	if len(listed) == 0 {
		t.Error("admin.PriceDetailActions is empty")
	}
}

func stringArg(e ast.Expr) string {
	if b, ok := e.(*ast.BasicLit); ok && b.Kind == token.STRING {
		s, _ := strconv.Unquote(b.Value)
		return s
	}
	return ""
}

func literalKeys(c *ast.CompositeLit) []string {
	var out []string
	for _, el := range c.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			if s := stringArg(kv.Key); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// detailKeys returns, for each variable of the function that holds a map, the keys it is given by a
// literal or by an assignment of an index.
func detailKeys(fn *ast.FuncDecl) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	add := func(name, key string) {
		if out[name] == nil {
			out[name] = map[string]bool{}
		}
		out[name][key] = true
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range s.Lhs {
				switch l := lhs.(type) {
				case *ast.Ident:
					if i < len(s.Rhs) {
						if c, ok := s.Rhs[i].(*ast.CompositeLit); ok {
							for _, k := range literalKeys(c) {
								add(l.Name, k)
							}
						}
					}
				case *ast.IndexExpr:
					if id, ok := l.X.(*ast.Ident); ok {
						if k := stringArg(l.Index); k != "" {
							add(id.Name, k)
						}
					}
				}
			}
		}
		return true
	})
	return out
}

// TestReportsDocuments is T-R76: the guides of the sprint name what the design says they name.
func TestReportsDocuments(t *testing.T) {
	check := func(file string, words ...string) {
		t.Helper()
		doc, err := os.ReadFile(filepath.Join(root(t), file))
		if err != nil {
			t.Errorf("%s: %v", file, err)
			return
		}
		for _, w := range words {
			if !strings.Contains(string(doc), w) {
				t.Errorf("%s does not mention %q", file, w)
			}
		}
	}
	check("docs/operations/usage.md", "usage report", "usage export", "rollup", "retention.usage_months", "reports.rollup_interval", "rejected_limit", "metadata-keys")
	check("docs/operations/audit.md", "audit:read", "platform_audit:read", "hash chain", "audit verify", "--expect-head", "retention.audit_months", "anchor")
	check("docs/operations/declarative.md", "providers", "models", "model_grants", "price_overrides", "platform_limits", "settings", "model_restriction", "roles", "service_accounts",
		"limits", "price_lists", "pandokos export", "pandokos apply", "--dry-run")
	check("docs/operations/configuration.md", "reports.rollup_interval", "retention.usage_months", "retention.audit_months",
		"PANDOKOS_REPORTS_ROLLUP_INTERVAL", "PANDOKOS_RETENTION_USAGE_MONTHS", "PANDOKOS_RETENTION_AUDIT_MONTHS")
	check("docs/releases/v0.2.0-draft.md", "audit:read", "platform_audit:read", "reports.rollup_interval", "retention.usage_months", "retention.audit_months", "0015",
		"pandokos audit verify", "pandokos export", "pandokos apply", "index")
}
