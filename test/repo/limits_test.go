package repo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/gateway"
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
	// Sprint 5.2 added the currency and the scope to the headers of a refusal.
	for _, h := range []string{family.HeaderLimitSubject, family.HeaderLimitMetric, family.HeaderLimit, family.HeaderLimitRemaining, family.HeaderLimitReset,
		family.HeaderLimitCurrency, family.HeaderLimitScope} {
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

// guideTable returns the body rows of the table that follows the header row in doc, as cells.
func guideTable(doc, header string) [][]string {
	lines := strings.Split(doc, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != header {
			continue
		}
		var rows [][]string
		for _, row := range lines[i+2:] { // the line after the header is the separator
			row = strings.TrimSpace(row)
			if !strings.HasPrefix(row, "|") {
				break
			}
			cells := strings.Split(strings.Trim(row, "|"), "|")
			for j := range cells {
				cells[j] = strings.TrimSpace(cells[j])
			}
			rows = append(rows, cells)
		}
		return rows
	}
	return nil
}

// TestLimitsGuideQuotas is T-R42: the operations guide names every metric, the codes and the headers
// of the refusals, the four keys of sprint 5.2 and the horizon, and has the table of what is strict,
// bounded and best effort.
func TestLimitsGuideQuotas(t *testing.T) {
	guide := read(t, "docs/operations/limits.md")
	for _, m := range limits.Metrics() {
		if !strings.Contains(guide, string(m)) {
			t.Errorf("docs/operations/limits.md does not name the metric %s", m)
		}
	}
	for _, code := range []string{string(family.CodeRateLimited), string(family.CodeQuotaExceeded), string(family.CodeNoPrice)} {
		if !strings.Contains(guide, code) {
			t.Errorf("docs/operations/limits.md does not name the code %s", code)
		}
	}
	for _, h := range []string{family.HeaderLimitSubject, family.HeaderLimitMetric, family.HeaderLimit, family.HeaderLimitRemaining, family.HeaderLimitReset,
		family.HeaderLimitCurrency, family.HeaderLimitScope} {
		if !strings.Contains(guide, h) {
			t.Errorf("docs/operations/limits.md does not name the header %s", h)
		}
	}
	for _, key := range []string{"limits.time_zone", "limits.on_unknown_price", "limits.on_store_failure", "limits.reconcile_interval"} {
		if !strings.Contains(guide, key) {
			t.Errorf("docs/operations/limits.md does not name the configuration key %s", key)
		}
	}
	if !regexp.MustCompile(`12\s?m(in)?\s?30\s?s`).MatchString(guide) && !strings.Contains(guide, "12m30s") {
		t.Error("docs/operations/limits.md does not state the horizon (12 m 30 s with the defaults)")
	}
	for _, word := range []string{"limit_event", "Retry-After", "daylight", "currency", "threshold"} {
		if !strings.Contains(strings.ToLower(guide), strings.ToLower(word)) {
			t.Errorf("docs/operations/limits.md does not mention %q", word)
		}
	}
	rows := guideTable(guide, "| Limit | Guarantee |")
	if len(rows) != 6 {
		t.Fatalf("the table of guarantees has %d rows, want the six of the design (header `| Limit | Guarantee |`)", len(rows))
	}
	want := []struct{ limit, guarantee string }{
		{"concurrent", "strict"}, {"per day or month", "strict"}, {"cost per day or month", "bounded"},
		{"right after", "best effort"}, {"several instances", "per instance"}, {"thresholds", "once"},
	}
	for i, w := range want {
		if len(rows[i]) < 2 || !strings.Contains(strings.ToLower(rows[i][0]), w.limit) || !strings.Contains(strings.ToLower(rows[i][1]), w.guarantee) {
			t.Errorf("row %d of the table of guarantees: %v, want a limit naming %q and a guarantee naming %q", i+1, rows[i], w.limit, w.guarantee)
		}
	}
}

// TestLimitsBoundaries is T-R43 (sprint 5.2): internal/limits does not import internal/ledger or
// internal/admin; only cmd/pandokos (and tests) give the reader the worker pool; internal/admin names
// neither limits.Memory nor limits.Reconciler.
func TestLimitsBoundaries(t *testing.T) {
	t.Run("internal/limits does not import the ledger or the administration API", func(t *testing.T) {
		files := nonTestGoFiles(t, []string{"internal/limits"})
		if len(files) < 8 {
			t.Fatalf("%d files in internal/limits", len(files))
		}
		for _, rel := range files {
			paths, _, _ := importsAndNames(t, rel)
			for _, p := range paths {
				if p == modulePath+"/internal/ledger" || p == modulePath+"/internal/admin" || p == modulePath+"/internal/gateway" {
					t.Errorf("%s imports %s", rel, p)
				}
			}
		}
	})

	const limitsPath = modulePath + "/internal/limits"
	// calls returns the calls of limits.<name> in a Go file, with the source of their first argument.
	calls := func(t *testing.T, rel, name string) []string {
		t.Helper()
		src, err := os.ReadFile(filepath.Join(root(t), rel))
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(token.NewFileSet(), rel, src, 0)
		if err != nil {
			t.Fatal(err)
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
		var out []string
		if local == "" {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == local && sel.Sel.Name == name {
					arg := ""
					if len(call.Args) > 0 {
						arg = string(src[call.Args[0].Pos()-f.FileStart : call.Args[0].End()-f.FileStart])
					}
					out = append(out, arg)
				}
			}
			return true
		})
		return out
	}

	t.Run("only cmd/pandokos builds the ledger reader, on the worker pool", func(t *testing.T) {
		var inMain []string
		err := filepath.WalkDir(root(t), func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root(t), path)
			rel = filepath.ToSlash(rel)
			if e.IsDir() {
				switch {
				case rel == ".git", rel == "local", rel == "test", strings.HasSuffix(rel, "/node_modules"), rel == "node_modules":
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
				return nil
			}
			for _, arg := range calls(t, rel, "NewLedger") {
				if !strings.HasPrefix(rel, "cmd/pandokos/") {
					t.Errorf("%s calls limits.NewLedger: only cmd/pandokos builds the reader", rel)
				}
				inMain = append(inMain, rel+": "+arg)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(inMain) == 0 {
			t.Error("cmd/pandokos does not build the ledger reader (limits.NewLedger)")
		}
		for _, c := range inMain {
			if !strings.Contains(strings.ToLower(c), "worker") {
				t.Errorf("%s: the reader is not given the worker pool", c)
			}
		}
	})

	t.Run("internal/admin names neither limits.Memory nor limits.Reconciler", func(t *testing.T) {
		files := nonTestGoFiles(t, []string{"internal/admin"})
		if len(files) < 10 {
			t.Fatalf("%d files in internal/admin", len(files))
		}
		banned := map[string]bool{"Memory": true, "NewMemory": true, "MemoryOptions": true, "Reconciler": true, "NewReconciler": true, "ReconcilerOptions": true}
		for _, rel := range files {
			_, _, file := importsAndNames(t, rel)
			local := ""
			for _, imp := range file.Imports {
				if strings.Trim(imp.Path.Value, `"`) == limitsPath {
					local = "limits"
					if imp.Name != nil {
						local = imp.Name.Name
					}
				}
			}
			if local == "" {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == local && banned[sel.Sel.Name] {
						t.Errorf("%s names limits.%s: the administration API reads the ledger, it does not hold counters", rel, sel.Sel.Name)
					}
				}
				return true
			})
		}
	})
}

// TestLimitsReference is T-R44: the permissions reference lists the six permissions and the
// administration API reference the twelve operations and the six audit actions of sprint 5.2.
func TestLimitsReference(t *testing.T) {
	permissions := read(t, "docs/reference/permissions.md")
	for _, p := range []string{"limit:read", "limit:write", "limit:delete", "platform_limit:read", "platform_limit:write", "platform_limit:delete"} {
		if !strings.Contains(permissions, "`"+p+"`") {
			t.Errorf("docs/reference/permissions.md does not list %s", p)
		}
	}
	api := read(t, "docs/reference/admin-api.md")
	for _, op := range []string{"listLimits", "createLimit", "getLimit", "updateLimit", "deleteLimit", "listTenantPlatformLimits", "listLimitEvents",
		"listPlatformLimits", "createPlatformLimit", "getPlatformLimit", "updatePlatformLimit", "deletePlatformLimit"} {
		if !strings.Contains(api, "`"+op+"`") {
			t.Errorf("docs/reference/admin-api.md does not list the operation %s", op)
		}
	}
	// An action is listed alone (`limit.create`) or in the compact form of the other pages
	// (`limit.create|update|delete`).
	for _, action := range []string{"limit.create", "limit.update", "limit.delete", "platform_limit.create", "platform_limit.update", "platform_limit.delete"} {
		prefix, verb, _ := strings.Cut(action, ".")
		re := regexp.MustCompile("`" + regexp.QuoteMeta(prefix) + `\.[a-z|]*\b` + verb + `\b[a-z|]*` + "`")
		if !re.MatchString(api) {
			t.Errorf("docs/reference/admin-api.md does not list the audit action %s", action)
		}
	}
}

// TestLimitStateConstants is part of T-R40 (sprint 5.3): the four states of the usage record are the
// same in the gateway, the schema check of migration 0011 and the specification.
func TestLimitStateConstants(t *testing.T) {
	states := []string{string(gateway.LimitStateNotEvaluated), string(gateway.LimitStateUnlimited), string(gateway.LimitStateLimited), string(gateway.LimitStateUnverified)}
	migration := read(t, "internal/store/migrations/sql/0011_limit_integrity.sql")
	spec := read(t, "api/openapi/admin.v1.yaml")
	check := regexp.MustCompile(`(?s)usage_record_limit_state_valid\s+CHECK \(limit_state IS NULL OR limit_state IN \(([^)]*)\)`).FindStringSubmatch(migration)
	if check == nil {
		t.Fatal("migration 0011 has no check usage_record_limit_state_valid")
	}
	enum := regexp.MustCompile(`(?s)\n    LimitState:.*?\n    \w`).FindString(spec)
	if enum == "" {
		t.Fatal("the specification has no schema LimitState")
	}
	for _, s := range states {
		if !strings.Contains(check[1], "'"+s+"'") {
			t.Errorf("the check of migration 0011 does not list %s", s)
		}
		if !strings.Contains(enum, s) {
			t.Errorf("the schema LimitState does not list %s", s)
		}
	}
	if n := strings.Count(check[1], "'") / 2; n != len(states) {
		t.Errorf("the check lists %d states, the gateway defines %d", n, len(states))
	}
}

// TestResolverImports is part of T-R43 (sprint 5.3): the resolver may use the audit package, as it
// does for failures; it never imports the administration API.
func TestResolverImports(t *testing.T) {
	files := nonTestGoFiles(t, []string{"internal/resolver"})
	if len(files) < 4 {
		t.Fatalf("%d files in internal/resolver", len(files))
	}
	for _, rel := range files {
		paths, _, _ := importsAndNames(t, rel)
		for _, p := range paths {
			if p == modulePath+"/internal/admin" || p == modulePath+"/internal/adminapi" {
				t.Errorf("%s imports %s", rel, p)
			}
		}
	}
}
