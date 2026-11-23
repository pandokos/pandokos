package repo

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenantexport"
)

// settingRef matches the name of the two settings of the erasure as a whole string (not the attribute
// pandokos.retention.kind of the metrics).
var settingRef = regexp.MustCompile("['\"`]pandokos\\.(erasure|retention)['\"`]")

// TestErasureOnlyInTheDatabase is T-R77: the two settings that let a statement change an append-only row
// appear in migrations 0015 and 0016 and nowhere else; the deletion job holds no statement but the three of
// the design; and no handler of a platform-scoped operation runs a statement of the kept log but the two of
// section 5.4.
func TestErasureOnlyInTheDatabase(t *testing.T) {
	// The settings are in the two migrations, and in no Go file of the product or of its test helpers.
	for _, dir := range []string{"cmd", "internal", "test"} {
		for _, file := range goFiles(t, dir) {
			if m := settingRef.FindString(read(t, relPath(t, file))); m != "" {
				t.Errorf("%s names the setting %s: only functions of migrations 0015 and 0016 set it", relPath(t, file), m)
			}
		}
	}
	var sqlFiles []string
	for _, pattern := range []string{"internal/store/migrations/sql/*.sql", "internal/store/queries/sql/*.sql", "deploy/postgres/*.sql"} {
		m, err := filepath.Glob(filepath.Join(root(t), pattern))
		if err != nil {
			t.Fatal(err)
		}
		sqlFiles = append(sqlFiles, m...)
	}
	found := map[string]bool{}
	for _, f := range sqlFiles {
		rel := relPath(t, f)
		if !settingRef.MatchString(read(t, rel)) && !strings.Contains(read(t, rel), "pandokos.erasure") && !strings.Contains(read(t, rel), "pandokos.retention'") {
			continue
		}
		base := filepath.Base(rel)
		if !strings.HasPrefix(base, "0015_") && !strings.HasPrefix(base, "0016_") {
			t.Errorf("%s names a setting of the erasure: only migrations 0015 and 0016 do", rel)
		}
		found[base[:4]] = true
	}
	if !found["0015"] || !found["0016"] {
		t.Errorf("the settings are in %v: they are set by migrations 0015 and 0016", found)
	}

	// The deletion job: three statements, no SQL of its own.
	allowed := map[string]bool{"ListOpenTenantDeletions": true, "CountTenantDeletions": true, "TenantErasureStep": true, "New": true}
	fset := token.NewFileSet()
	var seen []string
	for _, file := range goFiles(t, "internal/lifecycle") {
		for _, lit := range regexp.MustCompile(`(?i)\b(insert into|update\s+pandokos|delete from|truncate|set_config|pandokos\.tenant_)`).FindAllString(read(t, relPath(t, file)), -1) {
			t.Errorf("%s holds SQL (%q): the job calls the step function and nothing else", relPath(t, file), lit)
		}
		f := parseFile(t, fset, file)
		ast.Inspect(f, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch name := calledName(c); {
			case isStatementCall(c):
				seen = append(seen, name)
				if !allowed[name] {
					t.Errorf("%s: internal/lifecycle runs %s: only ListOpenTenantDeletions, CountTenantDeletions and TenantErasureStep", fset.Position(c.Pos()), name)
				}
			case name == "Exec" || name == "Query" || name == "QueryRow" || name == "CopyFrom" || name == "SendBatch":
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name != "q" && id.Name != "queries" {
						t.Errorf("%s: internal/lifecycle calls %s.%s: a statement of its own", fset.Position(c.Pos()), id.Name, name)
					}
				}
			}
			return true
		})
	}
	sort.Strings(seen)
	for _, want := range []string{"ListOpenTenantDeletions", "CountTenantDeletions", "TenantErasureStep"} {
		if !contains(seen, want) {
			t.Errorf("internal/lifecycle does not call %s (calls %v)", want, seen)
		}
	}
	// The worker's statements of the deletion are not called anywhere else, and the statements of
	// the kept log are read by the two handlers of the design only (T-R71 holds the rest).
	for _, file := range goFiles(t, "cmd", "internal") {
		rel := relPath(t, file)
		if strings.HasPrefix(rel, "internal/store/queries/") || strings.HasPrefix(rel, "internal/lifecycle/") {
			continue
		}
		for _, stmt := range []string{"TenantErasureStep", "DeletedTenantRetention", "ListDeletedTenantsForWorker"} {
			if strings.Contains(read(t, rel), "."+stmt+"(") && !strings.HasPrefix(rel, "internal/reports/") {
				t.Errorf("%s calls %s: only the deletion job and the retention job do", rel, stmt)
			}
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TestKEKOneWriter is T-R78: only kek_activate and kek_retire write kek_version; only internal/vault
// wraps and unwraps with a key provider; RegisterKEKVersion is gone.
func TestKEKOneWriter(t *testing.T) {
	writes := regexp.MustCompile(`(?i)(insert\s+into|update|delete\s+from)\s+pandokos\.kek_version\b`)
	files, err := filepath.Glob(filepath.Join(root(t), "internal", "store", "queries", "sql", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("the statements: %v", err)
	}
	for _, f := range files {
		if m := writes.FindString(read(t, relPath(t, f))); m != "" {
			t.Errorf("%s writes kek_version (%q): only kek_activate and kek_retire do", relPath(t, f), m)
		}
	}
	for _, file := range goFiles(t, "cmd", "internal", "test") {
		rel := relPath(t, file)
		if strings.Contains(read(t, rel), "RegisterKEKVersion") {
			t.Errorf("%s names RegisterKEKVersion, which is gone", rel)
		}
	}
	if strings.Contains(read(t, "internal/store/queries/sql/vault.sql"), "RegisterKEKVersion") {
		t.Error("vault.sql still holds RegisterKEKVersion")
	}
	// Wrap(ctx, plain, aad) and Unwrap(ctx, version, wrapped, aad) are called by internal/vault only.
	fset := token.NewFileSet()
	for _, file := range goFiles(t, "cmd", "internal") {
		rel := relPath(t, file)
		// internal/vault is the one user; `kek generate` makes one test wrap on the *LocalProvider it has just
		// built from a generated key, to learn its version (it holds no data and no interface value).
		if strings.HasPrefix(rel, "internal/vault/") || rel == "cmd/pandokos/kek.go" {
			continue
		}
		f := parseFile(t, fset, file)
		ast.Inspect(f, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch name := calledName(c); {
			case name == "Wrap" && len(c.Args) == 3, name == "Unwrap" && len(c.Args) == 4:
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
					t.Errorf("%s: %s calls %s of a key provider: only internal/vault wraps and unwraps", fset.Position(c.Pos()), rel, sel.Sel.Name)
				}
			}
			return true
		})
	}
	// The admin and worker roles are the only ones that call the two functions: the statements exist once.
	vault := read(t, "internal/store/queries/sql/vault.sql")
	for _, name := range []string{"ActivateKEKVersion", "RetireKEKVersions"} {
		if strings.Count(vault, "name: "+name+" ") != 1 {
			t.Errorf("vault.sql does not hold %s exactly once", name)
		}
	}
}

// TestLifecycleRecords is T-R79: the three decisions of sprint 6.3 exist and are indexed, the records they
// refine have a history line that names them, and VLT-11 is worded as section 5.18 words it.
func TestLifecycleRecords(t *testing.T) {
	index := read(t, "docs/adr/README.md")
	adrs := map[string]string{
		"0067": "0067-tenant-deletion-stages-closed-tenants-and-kept-records",
		"0068": "0068-key-encryption-key-versions-rotation-and-erasure-in-backups",
		"0069": "0069-tenant-data-export",
	}
	for n, name := range adrs {
		if _, err := os.Stat(filepath.Join(root(t), "docs", "adr", name+".md")); err != nil {
			t.Errorf("ADR %s: %v", n, err)
			continue
		}
		doc := read(t, "docs/adr/"+name+".md")
		if !regexp.MustCompile(`(?m)^- Status: (proposed|accepted)\s*$`).MatchString(doc) {
			t.Errorf("ADR %s has no status", n)
		}
		for _, section := range []string{"## Decision", "## Consequences", "## Alternatives considered", "## History"} {
			if !strings.Contains(doc, section) {
				t.Errorf("ADR %s has no section %q", n, section)
			}
		}
		if !strings.Contains(index, name+".md") {
			t.Errorf("docs/adr/README.md does not index ADR %s", n)
		}
	}
	history := map[string]string{
		"0008": "0068", "0033": "0068", "0013": "0067", "0027": "0067", "0054": "0067", "0065": "0067",
		"0023": "validator", "0062": "M47", "0063": "eight lines",
	}
	for adr, want := range history {
		matches, _ := filepath.Glob(filepath.Join(root(t), "docs", "adr", adr+"-*.md"))
		if len(matches) != 1 {
			t.Errorf("ADR %s: %d files", adr, len(matches))
			continue
		}
		doc := read(t, relPath(t, matches[0]))
		i := strings.Index(doc, "## History")
		if i < 0 {
			t.Errorf("ADR %s has no History", adr)
			continue
		}
		hist := doc[i:]
		if !regexp.MustCompile(`(?m)^- .*(sprint 6\.3|ADR 006[789]|0067|0068|0069)`).MatchString(hist) || !strings.Contains(hist, want) {
			t.Errorf("ADR %s: no history line of sprint 6.3 that names %q", adr, want)
		}
	}
	vlt := regexp.MustCompile(`(?m)^\| VLT-11 \|[^\n]*`).FindString(read(t, "docs/requirements/README.md"))
	for _, phrase := range []string{"in the database at once", "in backups once the key-encryption key version that wrapped the data key is retired"} {
		if !strings.Contains(vlt, phrase) {
			t.Errorf("VLT-11 is %q, want it to say %q", vlt, phrase)
		}
	}
	if strings.Contains(vlt, "including in backups") {
		t.Error("VLT-11 still promises erasure in backups unconditionally")
	}
}

// TestLifecycleDocuments is T-R80: the guides and references of sprint 6.3 say what the design says they
// say, and the failure reference cites only tests, log lines, instruments and keys that exist.
func TestLifecycleDocuments(t *testing.T) {
	has := func(t *testing.T, name, doc string, wants ...string) {
		t.Helper()
		for _, w := range wants {
			if !strings.Contains(strings.ToLower(doc), strings.ToLower(w)) {
				t.Errorf("%s does not mention %q", name, w)
			}
		}
	}
	t.Run("the deletion guide", func(t *testing.T) {
		doc := read(t, "docs/operations/tenant-deletion.md")
		has(t, "tenant-deletion.md", doc, "tenant:delete", "tenant_deletion.grace_period", "access", "secrets", "configuration", "usage", "audit",
			"pseudonym", "tenant deletion verify", "key rotation complete", "destroy", "backup", "kek-version list", "tenant-export", "residue")
	})
	t.Run("the vault guide", func(t *testing.T) {
		doc := read(t, "docs/operations/vault.md")
		if !regexp.MustCompile(`(?m)^#+ .*Rotating the key`).MatchString(doc) {
			t.Error("vault.md has no section \"Rotating the key\"")
		}
		has(t, "vault.md", doc, "unwrap_key_files", "one instance", "several instances", "key rotation complete", "kek-version list", "erasures_pending", "old backups")
	})
	t.Run("the backup guide", func(t *testing.T) {
		doc := read(t, "docs/operations/backup-and-restore.md")
		has(t, "backup-and-restore.md", doc, "what to back up", "what is not backed up", "restore", "after a restore", "the four cases", "recover-lost-kek.sql",
			"audit head", "tenant deletion list", "unwrap_key_files", "pg_dump")
	})
	t.Run("the export guide", func(t *testing.T) {
		doc := read(t, "docs/operations/tenant-export.md")
		has(t, "tenant-export.md", doc, "manifest.json", "tenant-export.v1.schema.json", "tenant-export schema", "JSON Schema", "2020-12")
		for _, k := range tenantexport.Kinds {
			has(t, "tenant-export.md", doc, k.File)
		}
	})
	t.Run("the configuration reference", func(t *testing.T) {
		doc := read(t, "docs/operations/configuration.md")
		has(t, "configuration.md", doc, "tenant_deletion.grace_period", "PANDOKOS_TENANT_DELETION_GRACE_PERIOD", "tenant_deletion.interval", "PANDOKOS_TENANT_DELETION_INTERVAL",
			"vault.kek.local.unwrap_key_files", "PANDOKOS_VAULT_KEK_LOCAL_UNWRAP_KEY_FILES")
	})
	t.Run("the release notes", func(t *testing.T) {
		doc := read(t, "docs/releases/v0.2.0-draft.md")
		has(t, "v0.2.0-draft.md", doc, "tenant:delete", "kek:read", "tenant_deletion.grace_period", "tenant_deletion.interval", "vault.kek.local.unwrap_key_files",
			"seven operations", "0016_tenant_deletion_kek_rotation", "VLT-11", "kek_activation", "pandokos tenant delete", "pandokos tenant-export", "M47")
	})
	t.Run("the failure reference", func(t *testing.T) {
		doc := read(t, "docs/reference/failure-behaviour.md")
		type row struct{ cells []string }
		var header []string
		var rows []row
		for _, line := range strings.Split(doc, "\n") {
			if !strings.HasPrefix(line, "|") {
				header = nil
				continue
			}
			cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
			}
			if strings.HasPrefix(strings.ReplaceAll(cells[0], "-", ""), ":") || strings.Trim(cells[0], "-: ") == "" {
				continue
			}
			if header == nil {
				header = cells
				continue
			}
			rows = append(rows, row{cells})
		}
		if len(rows) < 15 {
			t.Fatalf("the failure reference has %d rows", len(rows))
		}
		firstCells := strings.ToLower(strings.Join(func() []string {
			var out []string
			for _, r := range rows {
				out = append(out, r.cells[0])
			}
			return out
		}(), "\n"))
		for _, subject := range []string{"postgresql", "key file", "identity provider", "collector", "provider", "price dataset", // the dependencies
			"ledger writer", "partition", "rollup", "retention", "limit reconciler", "event writer", "deletion", "re-wrap", "price refresh"} { // the jobs
			if !strings.Contains(firstCells, subject) {
				t.Errorf("the failure reference has no row for %q", subject)
			}
		}
		// What a row cites exists.
		tests := map[string]bool{}
		for _, f := range testFiles(t) {
			for _, m := range regexp.MustCompile(`(?m)^func (Test\w+)\(`).FindAllStringSubmatch(read(t, relPath(t, f)), -1) {
				tests[m[1]] = true
			}
		}
		logRows := read(t, "docs/reference/logs.md")
		keys := map[string]bool{}
		for _, k := range config.Keys() {
			keys[k.Name] = true
		}
		instruments := map[string]bool{}
		for _, s := range telemetry.Catalogue() {
			instruments[s.Name] = true
		}
		token := regexp.MustCompile("`([^`]+)`")
		for _, r := range rows {
			for _, cell := range r.cells {
				for _, m := range token.FindAllStringSubmatch(cell, -1) {
					tok := m[1]
					switch {
					case strings.HasPrefix(tok, "Test"):
						if !tests[tok] {
							t.Errorf("the failure reference cites the test %s, which does not exist (row %q)", tok, r.cells[0])
						}
					case strings.HasPrefix(tok, "pandokos."):
						if !instruments[tok] {
							t.Errorf("the failure reference cites the instrument %s, which does not exist (row %q)", tok, r.cells[0])
						}
					case regexp.MustCompile(`^(listeners|limits|upstream|shutdown|cache|database|vault|pricing|ledger|telemetry|tokens|auth|idempotency|support|tenant_deletion|reports|retention|log)\.[a-z_.]+$`).MatchString(tok):
						if !keys[tok] {
							t.Errorf("the failure reference cites the key %s, which does not exist (row %q)", tok, r.cells[0])
						}
					case strings.Contains(strings.ToLower(header[min(len(header)-1, indexOfCell(r.cells, cell))]), "log"):
						if !strings.Contains(logRows, "`"+tok+"`") {
							t.Errorf("the failure reference cites the log line %q, which is not in docs/reference/logs.md (row %q)", tok, r.cells[0])
						}
					}
				}
			}
		}
		// A row has the five things the design says: the caller's answer, what is kept or lost, the log line, the instrument and the test.
		joined := strings.ToLower(strings.Join(header, " "))
		for _, col := range []string{"log", "instrument", "test", "recover"} {
			if !strings.Contains(joined, col) {
				t.Errorf("the table of the failure reference has no column about %q: %v", col, header)
			}
		}
	})
	t.Run("the overview points to the failure reference", func(t *testing.T) {
		has(t, "docs/architecture/overview.md", read(t, "docs/architecture/overview.md"), "failure-behaviour.md")
	})
}

func indexOfCell(cells []string, cell string) int {
	for i, c := range cells {
		if c == cell {
			return i
		}
	}
	return 0
}

// testFiles lists the test files of the repository (the modules of the product and of the SDK tests).
func testFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, dir := range []string{"cmd", "internal", "test"} {
		err := filepath.WalkDir(filepath.Join(root(t), dir), func(path string, e os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !e.IsDir() && strings.HasSuffix(path, "_test.go") {
				out = append(out, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// isStatementCall reports whether c is a call of a generated statement: queries.New(tx).Name(...) or q.Name(...).
func isStatementCall(c *ast.CallExpr) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch x := sel.X.(type) {
	case *ast.Ident:
		return x.Name == "q" && sel.Sel.Name != "New"
	case *ast.CallExpr:
		inner, ok := x.Fun.(*ast.SelectorExpr)
		if !ok || inner.Sel.Name != "New" {
			return false
		}
		id, ok := inner.X.(*ast.Ident)
		return ok && id.Name == "queries"
	}
	return false
}
