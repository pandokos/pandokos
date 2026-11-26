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

	"go.yaml.in/yaml/v3"

	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/declare"
)

// The repository tests of sprint 12.3 (operability): what the process reads, the scopes of the
// configuration, the guide and the drill, the record, the upgrade policy and the declarative
// documents.

// TestProcessReadsOnlyItsConfiguration is T-R98 (sprint.md 5.10 point 2): in cmd/ and internal/,
// outside test files, no call reads the environment, the host name, or the home, configuration,
// cache or working directory, except in two places; and every PANDOKOS_… name in the code is a key of
// the configuration table, PANDOKOS_CONFIG, a variable of the API commands (PANDOKOS_CLI_…) or the one
// test hook.
func TestProcessReadsOnlyItsConfiguration(t *testing.T) {
	forbidden := map[string]bool{"Getenv": true, "LookupEnv": true, "Environ": true, "ExpandEnv": true, "Hostname": true,
		"UserHomeDir": true, "UserConfigDir": true, "UserCacheDir": true, "Getwd": true}
	// The two places, each by file and function, with its reason.
	allowed := map[string]string{
		"cmd/pandokos/main.go:main":                     "the one place the environment enters the process: main hands os.Environ() to run, which passes it to config.Load",
		"internal/store/conn.go:refuseServiceVariables": "the one guard: it reads PGSERVICE and PGSERVICEFILE only to refuse them, because a libpq service file cannot be neutralised",
	}
	known := map[string]bool{"PANDOKOS_CONFIG": true, "PANDOKOS_TEST_REFRESH_DELAY": true}
	for _, k := range config.Keys() {
		known[k.Env] = true
	}
	reName := regexp.MustCompile(`^PANDOKOS_[A-Z0-9_]+$`)

	used := map[string]bool{}
	// A literal that is only tested as a prefix (strings.HasPrefix(name, "PANDOKOS_SERVICE_")) names a
	// family of variables that the process ignores (the service links of Kubernetes), not a variable
	// it reads: it is not held to the rule of the names.
	prefixOnly := map[*ast.BasicLit]bool{}
	fset := token.NewFileSet()
	for _, file := range goFiles(t, "cmd", "internal") {
		rel := relPath(t, file)
		f := parseFile(t, fset, file)
		var enclosing string
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			enclosing = fn.Name.Name
			ast.Inspect(fn, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					sel, ok := x.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "strings" && (sel.Sel.Name == "HasPrefix" || sel.Sel.Name == "CutPrefix" || sel.Sel.Name == "TrimPrefix") && len(x.Args) == 2 {
						if lit, ok := x.Args[1].(*ast.BasicLit); ok {
							prefixOnly[lit] = true
						}
					}
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "os" && forbidden[sel.Sel.Name] {
						key := rel + ":" + enclosing
						if _, ok := allowed[key]; !ok {
							t.Errorf("%s: %s calls os.%s: the process reads its configuration from the file and the environment it was given, in two places only", fset.Position(x.Pos()), key, sel.Sel.Name)
						}
						used[key] = true
					}
				case *ast.BasicLit:
					if x.Kind != token.STRING || prefixOnly[x] {
						return true
					}
					if s, err := strconv.Unquote(x.Value); err == nil && reName.MatchString(s) && !known[s] && !strings.HasPrefix(s, "PANDOKOS_CLI_") {
						t.Errorf("%s: %q is not a key of the configuration table, PANDOKOS_CONFIG, a variable of the API commands or the test hook", fset.Position(x.Pos()), s)
					}
				}
				return true
			})
		}
	}
	// Package-level initialisers are not inside a function: a call there is found by the same walk.
	for _, file := range goFiles(t, "cmd", "internal") {
		f := parseFile(t, fset, file)
		for _, decl := range f.Decls {
			if gd, ok := decl.(*ast.GenDecl); ok {
				ast.Inspect(gd, func(n ast.Node) bool {
					if call, ok := n.(*ast.CallExpr); ok {
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
							if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "os" && forbidden[sel.Sel.Name] {
								t.Errorf("%s: a package-level call to os.%s", fset.Position(call.Pos()), sel.Sel.Name)
							}
						}
					}
					return true
				})
			}
		}
	}
	for key := range allowed {
		if !used[key] {
			t.Errorf("the allow-list names %s, which no longer reads the environment: remove it", key)
		}
	}
}

// docTableCells splits a row of a Markdown table into its trimmed cells, honouring \| as a literal bar.
func docTableCells(line string) []string {
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '\\' && i+1 < len(line) && line[i+1] == '|':
			cur.WriteByte('|')
			i++
		case line[i] == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(line[i])
		}
	}
	cells = append(cells, strings.TrimSpace(cur.String()))
	if len(cells) > 0 && cells[0] == "" {
		cells = cells[1:]
	}
	if n := len(cells); n > 0 && cells[n-1] == "" {
		cells = cells[:n-1]
	}
	return cells
}

// TestConfigurationScopes is T-R99: the configuration reference shows each key's scope as the table of
// keys has it, has the section "What makes an installation" and the table of the ambient environment,
// and the limits guide links the reference for the time zone.
func TestConfigurationScopes(t *testing.T) {
	doc := read(t, "docs/operations/configuration.md")

	t.Run("a Scope column with the scope of every key", func(t *testing.T) {
		scopeCol := -1
		got := map[string]string{}
		for _, line := range strings.Split(doc, "\n") {
			if !strings.HasPrefix(line, "|") {
				continue
			}
			cells := docTableCells(line)
			if scopeCol < 0 {
				for i, c := range cells {
					if strings.EqualFold(c, "Scope") && len(cells) >= 5 {
						scopeCol = i
					}
				}
				continue
			}
			if len(cells) > scopeCol && strings.HasPrefix(cells[0], "`") {
				got[strings.Trim(cells[0], "`")] = strings.Trim(cells[scopeCol], "` ")
			}
		}
		if scopeCol < 0 {
			t.Fatal("the table of keys has no Scope column")
		}
		for _, k := range config.Keys() {
			if got[k.Name] != k.Scope {
				t.Errorf("%s: the reference says the scope is %q, the table of keys says %q", k.Name, got[k.Name], k.Scope)
			}
		}
	})

	t.Run("what makes an installation", func(t *testing.T) {
		if !regexp.MustCompile(`(?mi)^#+ .*what makes an installation`).MatchString(doc) {
			t.Error(`configuration.md has no section "What makes an installation"`)
		}
		low := strings.ToLower(doc)
		for _, want := range []string{"instance", "installation", "documents", "init", "migrate"} {
			if !strings.Contains(low, want) {
				t.Errorf("the section does not mention %q", want)
			}
		}
	})

	t.Run("the ambient environment", func(t *testing.T) {
		if !regexp.MustCompile(`(?mi)^#+ .*ambient`).MatchString(doc) {
			t.Error("configuration.md has no section about the ambient environment")
		}
		for _, v := range []string{"PGSERVICE", "PGSERVICEFILE", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "OTEL_", "SSL_CERT_FILE", "SSL_CERT_DIR", "`TZ`", "GODEBUG"} {
			if !strings.Contains(doc, v) {
				t.Errorf("the table of the ambient environment does not name %s", v)
			}
		}
	})

	t.Run("the limits guide links the reference for the time zone", func(t *testing.T) {
		limits := read(t, "docs/operations/limits.md")
		var found bool
		for _, para := range strings.Split(limits, "\n\n") {
			if strings.Contains(para, "same `limits.time_zone`") && regexp.MustCompile(`\]\(configuration\.md#[a-z0-9-]+\)`).MatchString(para) {
				found = true
			}
		}
		if !found {
			t.Error("the sentence of limits.md that says every instance must use the same limits.time_zone has no link to configuration.md#…")
		}
	})
}

// drillSteps returns, from test/drill/drill.go, the Step constants (identifier -> value) and the
// commands (Cmd<id without dots> -> text).
func drillSteps(t *testing.T) (steps map[string]string, commands map[string]string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root(t), "test", "drill", "drill.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var eval func(e ast.Expr) (string, bool)
	eval = func(e ast.Expr) (string, bool) {
		switch x := e.(type) {
		case *ast.BasicLit:
			s, err := strconv.Unquote(x.Value)
			return s, err == nil
		case *ast.BinaryExpr:
			a, ok1 := eval(x.X)
			b, ok2 := eval(x.Y)
			return a + b, ok1 && ok2 && x.Op == token.ADD
		case *ast.ParenExpr:
			return eval(x.X)
		}
		return "", false
	}
	steps, commands = map[string]string{}, map[string]string{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				v, ok := eval(vs.Values[i])
				if !ok {
					continue
				}
				if id, ok := vs.Type.(*ast.Ident); ok && id.Name == "Step" {
					steps[v] = name.Name
				}
				if strings.HasPrefix(name.Name, "Cmd") && vs.Type == nil {
					commands[strings.TrimPrefix(name.Name, "Cmd")] = v
				}
			}
		}
	}
	return steps, commands
}

// TestDrillFollowsTheGuide is T-R100: the step identifiers of the backup guide equal the Step
// constants of the drill suite, and each command block of the guide under a step identifier is the
// command the suite runs for it. The guide carries an identifier as an anchor of the one form
// <a id="step-4-2"></a> (the dot of "4.2" becomes a hyphen, "6.1s" is "step-6-1s"), and the code
// block that follows it, up to the next anchor, is the command.
func TestDrillFollowsTheGuide(t *testing.T) {
	guide := read(t, "docs/operations/backup-and-restore.md")
	steps, commands := drillSteps(t)
	if len(steps) < 20 {
		t.Fatalf("the suite names %d steps: %v", len(steps), steps)
	}

	reAnchor := regexp.MustCompile(`<a id="step-([0-9]+)-([a-z0-9]+)"></a>`)
	anchors := reAnchor.FindAllStringSubmatchIndex(guide, -1)
	inGuide := map[string]string{} // identifier -> the text of its section up to the next anchor
	for i, m := range anchors {
		id := guide[m[2]:m[3]] + "." + guide[m[4]:m[5]]
		end := len(guide)
		if i+1 < len(anchors) {
			end = anchors[i+1][0]
		}
		if _, dup := inGuide[id]; dup {
			t.Errorf("the guide has the anchor of step %s twice", id)
		}
		inGuide[id] = guide[m[1]:end]
	}
	var onlyGuide, onlySuite []string
	for id := range inGuide {
		if _, ok := steps[id]; !ok {
			onlyGuide = append(onlyGuide, id)
		}
	}
	for id := range steps {
		if _, ok := inGuide[id]; !ok {
			onlySuite = append(onlySuite, id)
		}
	}
	sort.Strings(onlyGuide)
	sort.Strings(onlySuite)
	if len(onlyGuide) > 0 {
		t.Errorf("steps of the guide that the suite does not run: %v", onlyGuide)
	}
	if len(onlySuite) > 0 {
		t.Errorf("steps of the suite that the guide does not carry as an anchor: %v", onlySuite)
	}

	reBlock := regexp.MustCompile("(?s)```[a-z]*\n(.*?)```")
	for key, want := range commands {
		id := key[:1] + "." + key[1:]
		section, ok := inGuide[id]
		if !ok {
			continue // reported above
		}
		m := reBlock.FindStringSubmatch(section)
		if m == nil {
			t.Errorf("step %s: the guide has no command block under its anchor", id)
			continue
		}
		if strings.TrimSpace(m[1]) != strings.TrimSpace(want) {
			t.Errorf("step %s: the command block of the guide is\n%s\nand the suite runs\n%s", id, strings.TrimSpace(m[1]), strings.TrimSpace(want))
		}
	}
}

// drillTests are the names of the tests of test/drill.
func drillTests(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, file := range []string{"dump", "restore", "after", "keys", "report"} {
		b, err := os.ReadFile(filepath.Join(root(t), "test", "drill", file+"_test.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range regexp.MustCompile(`(?m)^func (Test\w+)\(`).FindAllStringSubmatch(string(b), -1) {
			out[m[1]] = true
		}
	}
	return out
}

// TestDrillRecord is T-R101: the record of the drill exists, has the sections of sprint.md 5.7, a row
// per case with a test that exists, the table of what must be entered again (5.6), and is linked from
// the backup guide; the guide no longer says the restore is untested; the requirements page marks
// OPS-10 and VLT-16 tested; and no document says "sprint 12.3" of something still to come.
func TestDrillRecord(t *testing.T) {
	record := read(t, "docs/operations/restore-drill-2026-11.md")
	guide := read(t, "docs/operations/backup-and-restore.md")

	t.Run("the sections", func(t *testing.T) {
		for _, h := range []string{"what was drilled", "the cases", "findings", "what must be entered again", "figures", "not drilled", "who ran it"} {
			if !regexp.MustCompile(`(?mi)^#+ .*` + regexp.QuoteMeta(h)).MatchString(record) {
				t.Errorf("the record has no section about %q", h)
			}
		}
		for _, want := range []string{"make test-drill", "G-1", "F-1", "HashiCorp Vault", "indicative"} {
			if !strings.Contains(record, want) {
				t.Errorf("the record does not mention %q", want)
			}
		}
	})

	t.Run("a row per case with a test that exists", func(t *testing.T) {
		tests := drillTests(t)
		for _, c := range []struct{ id, test string }{
			{"L1", "TestRestoreLogical"}, {"P1", "TestRestorePhysical"}, {"P2", "TestRestorePhysical"},
			{"K1", "TestKeyCasesFile"}, {"K2", "TestKeyCasesFile"}, {"K3", "TestKeyCasesFile"}, {"K4", "TestKeyCasesFile"},
			{"S1", "TestKeyCasesService"}, {"S2", "TestKeyCasesService"}, {"S3", "TestKeyCasesService"}, {"S4", "TestKeyCasesService"},
			{"N1", "TestRestoreOnLaterMajor"},
		} {
			var row string
			for _, line := range strings.Split(record, "\n") {
				if strings.HasPrefix(line, "|") && regexp.MustCompile(`\b`+c.id+`\b`).MatchString(line) && strings.Contains(line, c.test) {
					row = line
				}
			}
			if row == "" {
				t.Errorf("the record has no row for case %s with its test %s", c.id, c.test)
			}
			if !tests[c.test] {
				t.Errorf("test/drill has no test %s", c.test)
			}
		}
	})

	t.Run("the table of what must be entered again", func(t *testing.T) {
		for _, row := range []string{"Nothing but time", "The key of an old backup's version", "The key entirely", "One tenant's data key row",
			"unseal shares", "The database with no backup", "The audit heads kept outside"} {
			if !strings.Contains(record, row) {
				t.Errorf("the table of 5.6 has no row %q", row)
			}
		}
		for _, col := range []string{"Lost", "Comes back", "Cannot be recovered", "The owner", "The operator"} {
			if !strings.Contains(record, col) {
				t.Errorf("the table of 5.6 has no column %q", col)
			}
		}
	})

	t.Run("the guide", func(t *testing.T) {
		if !strings.Contains(guide, "restore-drill-2026-11.md") {
			t.Error("the backup guide does not link the record")
		}
		for _, gone := range []string{"rehearsed and tested is sprint 12.3", "What sprint 12.3 adds", "Until then, rehearse"} {
			if strings.Contains(guide, gone) {
				t.Errorf("the backup guide still says %q", gone)
			}
		}
		if !regexp.MustCompile(`(?m)^## 7\. Drills`).MatchString(guide) {
			t.Error(`the backup guide has no section "7. Drills"`)
		}
	})

	t.Run("the requirements page", func(t *testing.T) {
		reqs := read(t, "docs/requirements/README.md")
		for _, id := range []string{"OPS-10", "VLT-16"} {
			var row string
			for _, line := range strings.Split(reqs, "\n") {
				if strings.HasPrefix(line, "| "+id+" |") {
					row = line
				}
			}
			if !strings.Contains(row, "tested (sprint 12.3)") {
				t.Errorf("the row of %s does not say tested (sprint 12.3): %s", id, row)
			}
		}
	})

	t.Run("nothing says sprint 12.3 of something to come", func(t *testing.T) {
		skip := map[string]bool{"docs/operations/restore-drill-2026-11.md": true, "docs/operations/upgrade.md": true}
		reHistory := regexp.MustCompile(`^\s*[-*] \d{4}-\d{2}-\d{2}:`)
		reDone := regexp.MustCompile(`(?i)\b(delivered|added|made|ran|executed|drilled|measured|wrote|written|carried out)\b`)
		var files []string
		for _, dir := range []string{"docs/operations", "docs/reference", "docs/architecture", "docs/adr"} {
			err := filepath.Walk(filepath.Join(root(t), dir), func(path string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() && strings.HasSuffix(path, ".md") {
					files = append(files, path)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, file := range files {
			rel := relPath(t, file)
			base := filepath.Base(rel)
			if skip[rel] || strings.HasPrefix(base, "0075-") || strings.HasPrefix(base, "0076-") || strings.HasPrefix(base, "0077-") {
				continue
			}
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for i, line := range strings.Split(string(b), "\n") {
				if !regexp.MustCompile(`12\.3`).MatchString(line) || reHistory.MatchString(line) || reDone.MatchString(line) {
					continue
				}
				if regexp.MustCompile(`(?i)(sprint 12\.3|\(12\.3\)|\b12\.3\))`).MatchString(line) {
					t.Errorf("%s:%d says sprint 12.3 of something still to come: %s", rel, i+1, strings.TrimSpace(line))
				}
			}
		}
	})
}

// TestUpgradePolicy is T-R102: the upgrade guide exists with the points of sprint.md 5.8; PREVIOUS
// has three well-formed lines and its version is the newest patch of the minor before the one in
// development (the draft's, or, between the page of a release and the draft of the next, the newest
// page's); the Makefile has previous-binary, test-upgrade and test-drill; ci.yml has the jobs
// upgrade and drill running them with no continue-on-error and no upload step; "Releasing" names the
// two jobs, the record and the move of PREVIOUS; and every release page from v0.3.0 on, the draft
// included, links a review record that exists.
func TestUpgradePolicy(t *testing.T) {
	t.Run("the guide", func(t *testing.T) {
		doc := read(t, "docs/operations/upgrade.md")
		for _, h := range []string{"supported upgrade", "patch release", "procedure", "if you forget to stop an instance", "going back", "what is checked at every release"} {
			if !regexp.MustCompile(`(?mi)^#+ .*` + regexp.QuoteMeta(h)).MatchString(doc) {
				t.Errorf("upgrade.md has no section about %q", h)
			}
		}
		for _, want := range []string{"v0.2.x", "pandokos migrate", "config check", "kek_activation", "migrations are forward-only", "test/upgrade/PREVIOUS", "semantic versioning", "ADR 0075"} {
			if !strings.Contains(doc, want) {
				t.Errorf("upgrade.md does not mention %q", want)
			}
		}
	})

	t.Run("PREVIOUS", func(t *testing.T) {
		lines := strings.Split(strings.TrimSpace(read(t, "test/upgrade/PREVIOUS")), "\n")
		if len(lines) != 3 {
			t.Fatalf("test/upgrade/PREVIOUS has %d lines, want 3", len(lines))
		}
		res := []*regexp.Regexp{regexp.MustCompile(`^version=v\d+\.\d+\.\d+$`), regexp.MustCompile(`^commit=[0-9a-f]{40}$`),
			regexp.MustCompile(`^image=ghcr\.io/pandokos/pandokos@sha256:[0-9a-f]{64}$`)}
		for i, re := range res {
			if !re.MatchString(lines[i]) {
				t.Errorf("line %d of PREVIOUS is %q, want it to match %s", i+1, lines[i], re)
			}
		}
		// The minor in development is the one of the draft page; with no draft, the release is being cut
		// and the minor "in development" is the one of the newest release page. PREVIOUS is the newest
		// patch of the minor before it. More than one draft is an error.
		draft, err := filepath.Glob(filepath.Join(root(t), "docs", "releases", "v*-draft.md"))
		if err != nil || len(draft) > 1 {
			t.Fatalf("docs/releases has %d draft pages (%v), want at most 1", len(draft), err)
		}
		var major string
		var minor int
		if len(draft) == 1 {
			m := regexp.MustCompile(`v(\d+)\.(\d+)\.\d+-draft\.md`).FindStringSubmatch(filepath.Base(draft[0]))
			major, minor = m[1], mustAtoi(t, m[2])
		} else {
			parts := strings.Split(latestRelease(t), ".")
			major, minor = parts[0], mustAtoi(t, parts[1])
		}
		pages, _ := filepath.Glob(filepath.Join(root(t), "docs", "releases", "v"+major+"."+strconv.Itoa(minor-1)+".*.md"))
		best := -1
		for _, p := range pages {
			if pm := regexp.MustCompile(`v\d+\.\d+\.(\d+)\.md$`).FindStringSubmatch(p); pm != nil && mustAtoi(t, pm[1]) > best {
				best = mustAtoi(t, pm[1])
			}
		}
		if want := "version=v" + major + "." + strconv.Itoa(minor-1) + "." + strconv.Itoa(best); lines[0] != want {
			t.Errorf("PREVIOUS says %q, the newest patch of the minor before the one in development is %q", lines[0], want)
		}
	})

	t.Run("the Makefile", func(t *testing.T) {
		mk := read(t, "Makefile")
		for _, target := range []string{"previous-binary", "test-upgrade", "test-drill"} {
			if !regexp.MustCompile(`(?m)^` + target + `:`).MatchString(mk) {
				t.Errorf("the Makefile has no target %s", target)
			}
		}
	})

	t.Run("the CI jobs", func(t *testing.T) {
		var ci struct {
			Jobs map[string]struct {
				ContinueOnError any `yaml:"continue-on-error"`
				Steps           []struct {
					Run             string `yaml:"run"`
					Uses            string `yaml:"uses"`
					ContinueOnError any    `yaml:"continue-on-error"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal([]byte(read(t, ".github/workflows/ci.yml")), &ci); err != nil {
			t.Fatal(err)
		}
		for job, targets := range map[string][]string{"upgrade": {"make previous-binary", "make test-upgrade"}, "drill": {"make test-drill"}} {
			j, ok := ci.Jobs[job]
			if !ok {
				t.Errorf("ci.yml has no job %s", job)
				continue
			}
			if j.ContinueOnError != nil {
				t.Errorf("job %s has continue-on-error", job)
			}
			var runs []string
			for _, s := range j.Steps {
				runs = append(runs, s.Run)
				if s.ContinueOnError != nil {
					t.Errorf("a step of job %s has continue-on-error", job)
				}
				if strings.Contains(strings.ToLower(s.Uses), "upload") {
					t.Errorf("job %s uploads something (%s): the jobs upload nothing, a dump or a snapshot never leaves the runner", job, s.Uses)
				}
			}
			all := strings.Join(runs, "\n")
			for _, target := range targets {
				if !strings.Contains(all, target) {
					t.Errorf("job %s does not run %q", job, target)
				}
			}
		}
	})

	t.Run("Releasing", func(t *testing.T) {
		doc := read(t, "CONTRIBUTING.md")
		i := strings.Index(doc, "### Releasing")
		if i < 0 {
			t.Fatal("CONTRIBUTING.md has no section Releasing")
		}
		section := doc[i:]
		if j := strings.Index(section[3:], "\n## "); j > 0 {
			section = section[:j+3]
		}
		for _, want := range []string{"`upgrade`", "`drill`", "restore-drill-", "test/upgrade/PREVIOUS", "previous-binary"} {
			if !strings.Contains(section, want) {
				t.Errorf("the Releasing section does not mention %s", want)
			}
		}
	})

	t.Run("every release page from v0.3.0 on links a review record and the drill record", func(t *testing.T) {
		pages, _ := filepath.Glob(filepath.Join(root(t), "docs", "releases", "v*.md"))
		var seen int
		for _, p := range pages {
			m := regexp.MustCompile(`^v(\d+)\.(\d+)\.`).FindStringSubmatch(filepath.Base(p))
			if m == nil || mustAtoi(t, m[1]) == 0 && mustAtoi(t, m[2]) < 3 {
				continue
			}
			seen++
			text, _ := os.ReadFile(p)
			for _, pattern := range []string{`\(\.\./architecture/(threat-model-review-\d{4}-\d{2}\.md)`, `\(\.\./operations/(restore-drill-\d{4}-\d{2}\.md)`} {
				links := regexp.MustCompile(pattern).FindAllStringSubmatch(string(text), -1)
				if len(links) == 0 {
					t.Errorf("%s links no record of %s", relPath(t, p), pattern)
				}
				for _, l := range links {
					dir := "architecture"
					if strings.HasPrefix(l[1], "restore") {
						dir = "operations"
					}
					if _, err := os.Stat(filepath.Join(root(t), "docs", dir, l[1])); err != nil {
						t.Errorf("%s links %s, which does not exist", relPath(t, p), l[1])
					}
				}
			}
		}
		if seen == 0 {
			t.Error("no release page of v0.3.0 or later was found, the draft included")
		}
	})
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestDeclarativeDocuments is T-R103: the declarative guide's table has the new sections and the flag
// --secrets-out; the events guide and ADR 0066 and 0072 no longer defer them; and the example
// platform document parses.
func TestDeclarativeDocuments(t *testing.T) {
	t.Run("the declarative guide", func(t *testing.T) {
		doc := read(t, "docs/operations/declarative.md")
		for _, want := range []string{"platform_issuers", "token_issuers", "subject_mappings", "event_destinations", "external_reference", "`tenants`",
			"mapping:", "--secrets-out", "event-destination-", "0600", "never rotates"} {
			if !strings.Contains(doc, want) {
				t.Errorf("declarative.md does not mention %q", want)
			}
		}
		if strings.Contains(doc, "are not in the document (sprint 12.3)") {
			t.Error("declarative.md still defers the issuers and the mappings")
		}
	})

	t.Run("the events guide and the ADRs no longer defer", func(t *testing.T) {
		for file, gone := range map[string][]string{
			"docs/operations/events.md":                                                            {"are not in `export`/`apply` (sprint 12.3)", "sprint 12.3."},
			"docs/adr/0066-declarative-export-and-apply.md":                                        {"join in sprint 12.3"},
			"docs/adr/0072-event-types-the-envelope-the-external-reference-and-platform-events.md": {"are not in `export` and `apply` (sprint 12.3)"},
		} {
			text := read(t, file)
			for _, g := range gone {
				for _, line := range strings.Split(text, "\n") {
					if strings.Contains(line, g) && !regexp.MustCompile(`^\s*[-*] \d{4}-\d{2}-\d{2}:`).MatchString(line) {
						t.Errorf("%s still says %q: %s", file, g, strings.TrimSpace(line))
					}
				}
			}
		}
		if !strings.Contains(read(t, "docs/operations/events.md"), "export") || !strings.Contains(strings.ToLower(read(t, "docs/operations/events.md")), "event_destinations") {
			t.Error("events.md does not say that the destinations are in export and apply")
		}
	})

	t.Run("the example platform document parses", func(t *testing.T) {
		b := []byte(read(t, "examples/declarative/platform.yaml"))
		doc, err := declare.Parse(b)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if err := doc.Validate(declare.Target{Platform: true}); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		var ceiling bool
		for _, l := range doc.PlatformLimits {
			ceiling = ceiling || (l.Tenant == "default" && l.Metric == "concurrent_requests" && l.Limit == 256 && l.Enforce)
		}
		if !ceiling {
			t.Errorf("the example has no default ceiling of 256 concurrent requests: %+v", doc.PlatformLimits)
		}
		if len(doc.Providers) == 0 || len(doc.Providers[0].Endpoints) == 0 || len(doc.Models) == 0 {
			t.Error("the example has no provider with an endpoint and a model")
		}
	})
}

// TestDrillComposePins is the change of sprint 12.3 to T-R93 and T-R94: the Compose file of the drill
// is covered by the pins and by Dependabot; its two PostgreSQL 16 servers share the digest of the
// test database, its later major has a digest of its own, its two OpenBao servers share the digest of
// the transit suite, and it publishes nothing but on 127.0.0.1.
func TestDrillComposePins(t *testing.T) {
	var compose struct {
		Services map[string]struct {
			Image string   `yaml:"image"`
			Ports []string `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(read(t, "test/drill/compose.yaml")), &compose); err != nil {
		t.Fatal(err)
	}
	image := func(file, service string) string {
		var c struct {
			Services map[string]struct {
				Image string `yaml:"image"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal([]byte(read(t, file)), &c); err != nil {
			t.Fatal(err)
		}
		return c.Services[service].Image
	}
	want := map[string]string{
		"source": image("test/db/compose.yaml", "postgres"), "target": image("test/db/compose.yaml", "postgres"),
		"openbao": image("test/transit/compose.yaml", "openbao"), "openbao-target": image("test/transit/compose.yaml", "openbao"),
	}
	for service, img := range want {
		if got := compose.Services[service].Image; got != img || !regexp.MustCompile(`@sha256:[0-9a-f]{64}$`).MatchString(got) {
			t.Errorf("service %s of the drill is %q, want the pinned image %q", service, got, img)
		}
	}
	next := compose.Services["target-next"].Image
	if !regexp.MustCompile(`^postgres:\d+-alpine@sha256:[0-9a-f]{64}$`).MatchString(next) || next == want["source"] {
		t.Errorf("target-next is %q, want a pinned image of a later major than the source's", next)
	}
	for name, s := range compose.Services {
		for _, p := range s.Ports {
			if !strings.HasPrefix(p, "127.0.0.1:") {
				t.Errorf("service %s publishes %s: the drill publishes on 127.0.0.1 only", name, p)
			}
		}
	}
	if !dependabotEntries(t)[[2]string{"docker-compose", "/test/drill"}] {
		t.Error("dependabot.yml has no entry for /test/drill")
	}
}

// TestTestDatabaseLaterMajor is the test of the job "test (pg18)": the later major of the test database
// is a pinned image that is the restore drill's, it keeps its data off the disk where the 18 image wants
// it, up.sh reads PANDOKOS_TEST_PG, and CI runs the release toolchain against it besides the floor.
func TestTestDatabaseLaterMajor(t *testing.T) {
	var next struct {
		Services map[string]struct {
			Image string   `yaml:"image"`
			Tmpfs []string `yaml:"tmpfs"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(read(t, "test/db/compose.pg18.yaml")), &next); err != nil {
		t.Fatal(err)
	}
	var drill struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(read(t, "test/drill/compose.yaml")), &drill); err != nil {
		t.Fatal(err)
	}
	got := next.Services["postgres"].Image
	if !regexp.MustCompile(`^postgres:18[\w.-]*@sha256:[0-9a-f]{64}$`).MatchString(got) {
		t.Errorf("test/db/compose.pg18.yaml image %q: want postgres 18 pinned by digest", got)
	}
	if want := drill.Services["target-next"].Image; got != want {
		t.Errorf("test/db/compose.pg18.yaml image %q differs from the later major of the drill %q: move both together", got, want)
	}
	if tmpfs := next.Services["postgres"].Tmpfs; len(tmpfs) != 1 || tmpfs[0] != "/var/lib/postgresql" {
		t.Errorf("tmpfs %v: the 18 image keeps its data under /var/lib/postgresql", tmpfs)
	}
	up := read(t, "test/db/up.sh")
	if !strings.Contains(up, "PANDOKOS_TEST_PG") || !strings.Contains(up, "test/db/compose.pg18.yaml") {
		t.Error("test/db/up.sh does not start the later major for PANDOKOS_TEST_PG=18")
	}
	var ci struct {
		Jobs map[string]struct {
			Name     string `yaml:"name"`
			Strategy struct {
				Matrix struct {
					Include []map[string]string `yaml:"include"`
				} `yaml:"matrix"`
			} `yaml:"strategy"`
			Steps []struct {
				Run string            `yaml:"run"`
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(read(t, ".github/workflows/ci.yml")), &ci); err != nil {
		t.Fatal(err)
	}
	job := ci.Jobs["test"]
	var floor, release, pg18 map[string]string
	for _, m := range job.Strategy.Matrix.Include {
		switch m["name"] {
		case "floor":
			floor = m
		case "release":
			release = m
		case "pg18":
			pg18 = m
		}
	}
	if floor == nil || release == nil || pg18 == nil {
		t.Fatal("the job test has no entries floor, release and pg18")
	}
	if pg18["postgres"] != "18" || floor["postgres"] != "" || release["postgres"] != "" {
		t.Error("only the entry pg18 of the job test runs PostgreSQL 18; floor and release run the floor")
	}
	if pg18["go-version-file"] != release["go-version-file"] || pg18["go-version"] != release["go-version"] {
		t.Error("the entry pg18 must use the release toolchain, like release")
	}
	started := false
	for _, s := range job.Steps {
		if strings.Contains(s.Run, "test/db/up.sh") && s.Env["PANDOKOS_TEST_PG"] == "${{ matrix.postgres }}" {
			started = true
		}
	}
	if !started {
		t.Error("the step of the job test that starts the database does not pass matrix.postgres as PANDOKOS_TEST_PG")
	}
}
