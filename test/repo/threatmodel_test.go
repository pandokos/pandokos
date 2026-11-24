package repo

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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

const (
	modelFile      = "docs/architecture/security-model.md"
	reviewedCommit = "96cb793c0359b339c4c98785c29a0776e2dada0f" // the commit the review of sprint 12.2 read
)

// newestReview returns the path of the newest review record (the lexically greatest file name of
// docs/architecture/threat-model-review-*.md), relative to the root.
func newestReview(t *testing.T) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root(t), "docs", "architecture", "threat-model-review-*.md"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no review record docs/architecture/threat-model-review-*.md (%v)", err)
	}
	sort.Strings(files)
	rel, err := filepath.Rel(root(t), files[len(files)-1])
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(rel)
}

// tableOf returns the rows of the first table of text whose header starts with the cells given
// (further columns are allowed), the header included in header.
func tableOf(text string, start ...string) (header []string, rows [][]string) {
	lines := strings.Split(text, "\n")
	for i := 0; i+1 < len(lines); i++ {
		cells := mdCells(lines[i])
		if len(cells) < len(start) || !isSeparator(lines[i+1]) || !equalCells(cells[:len(start)], start) {
			continue
		}
		for _, l := range lines[i+2:] {
			if !strings.HasPrefix(strings.TrimSpace(l), "|") {
				break
			}
			rows = append(rows, mdCells(l))
		}
		return cells, rows
	}
	return nil, nil
}

// consecutive checks that the identifiers of the first column are prefix-1 to prefix-n with no
// hole (zero-padded to width digits), at least min of them, and returns them.
func consecutive(t *testing.T, what, prefix string, width, min int, rows [][]string) []string {
	t.Helper()
	var ids []string
	for i, r := range rows {
		want := prefix + fmt.Sprintf("%0*d", width, i+1)
		if len(r) == 0 || r[0] != want {
			got := ""
			if len(r) > 0 {
				got = r[0]
			}
			t.Errorf("%s: row %d is %q, want %q (identifiers are unique and consecutive)", what, i+1, got, want)
			continue
		}
		ids = append(ids, want)
	}
	if len(ids) < min {
		t.Errorf("%s: %d rows, at least %d (a row of the model is never deleted)", what, len(ids), min)
	}
	return ids
}

// modelIdentifiers reads the three identifier tables of the security model: boundaries, statements
// and threats, each with the columns ID, a name, Controls, Code and Proof.
func modelIdentifiers(t *testing.T) (boundaries, statements, threats [][]string, text string) {
	t.Helper()
	text = read(t, modelFile)
	for _, c := range []struct {
		name  string
		rows  *[][]string
		first string
	}{
		{"Boundary", &boundaries, "TB-"}, {"Statement", &statements, "TS-"}, {"Threat", &threats, "TM-"},
	} {
		header := []string{"ID", c.name, "Controls", "Code", "Proof"}
		if *c.rows = mdTable(text, header...); *c.rows == nil {
			t.Errorf("%s has no table with the header | %s |", modelFile, strings.Join(header, " | "))
		}
	}
	return
}

var proofRE = regexp.MustCompile(`^[a-z][a-z0-9]*\.(Test|Fuzz)[A-Za-z0-9_]*$`)

// TestThreatModelTraceability is T-R89 (sprint 12.2, section 5.4): the security model has its
// tables with their headers; identifiers are unique and consecutive; every path of Code exists;
// every name of Proof resolves to exactly one Test or Fuzz function declared in a _test.go file of
// a directory whose last element is the package name; every row of the parser table names a
// function that exists and a fuzz target that exists; the status line links the newest record.
func TestThreatModelTraceability(t *testing.T) {
	boundaries, statements, threats, text := modelIdentifiers(t)
	consecutive(t, "boundaries", "TB-", 1, 8, boundaries)
	consecutive(t, "statements", "TS-", 1, 3, statements)
	consecutive(t, "threats", "TM-", 2, 60, threats)

	tests := testFunctions(t)
	check := func(kind string, rows [][]string) {
		for _, r := range rows {
			if len(r) != 5 {
				t.Errorf("%s row %v: %d cells, want 5", kind, r, len(r))
				continue
			}
			id := r[0]
			code := backticked(r[3])
			if len(code) < 1 || len(code) > 3 {
				t.Errorf("%s: %d paths in Code, want 1 to 3", id, len(code))
			}
			for _, p := range code {
				if _, err := os.Stat(filepath.Join(root(t), filepath.FromSlash(p))); err != nil {
					t.Errorf("%s: Code names %s, which does not exist", id, p)
				}
			}
			proof := backticked(r[4])
			if len(proof) < 1 || len(proof) > 6 {
				t.Errorf("%s: %d tests in Proof, want 1 to 6", id, len(proof))
			}
			for _, p := range proof {
				switch files := tests[p]; {
				case !proofRE.MatchString(p):
					t.Errorf("%s: Proof names %q, which is not package.TestName or package.FuzzName", id, p)
				case len(files) == 0:
					t.Errorf("%s: Proof names %s, which no _test.go file declares", id, p)
				case len(files) > 1:
					t.Errorf("%s: Proof names %s, which %d files declare: %v", id, p, len(files), files)
				}
			}
		}
	}
	check("boundary", boundaries)
	check("statement", statements)
	check("threat", threats)

	t.Run("the status line links the newest record", func(t *testing.T) {
		var status string
		for _, l := range strings.Split(text, "\n") {
			if strings.HasPrefix(l, "- Status: ") {
				status = l
				break
			}
		}
		newest := filepath.Base(newestReview(t))
		re := regexp.MustCompile(`^- Status: reviewed against the code on \d{4}-\d{2}-\d{2} \(sprint 12\.2\)`)
		if !re.MatchString(status) {
			t.Errorf("the status line is %q: want \"- Status: reviewed against the code on <date> (sprint 12.2): [review record](...)\"", status)
		}
		if !strings.Contains(status, "]("+newest+")") {
			t.Errorf("the status line does not link the newest record %s: %q", newest, status)
		}
	})

	t.Run("the parsers that read from the network", func(t *testing.T) {
		if !strings.Contains(text, "\n## Parsers that read from the network") {
			t.Fatal("the model has no section 'Parsers that read from the network'")
		}
		if !strings.Contains(text, "\n## Reviews") {
			t.Error("the model has no last section 'Reviews'")
		}
	})
}

// declaredFunctions maps "package.Name" (a function or a method) to the non-test files of a
// directory under internal, test or cmd whose last element is the package that declare it.
func declaredFunctions(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, top := range []string{"internal", "test", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root(t), top), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root(t), path)
			pkg := filepath.Base(filepath.Dir(rel))
			if filepath.ToSlash(filepath.Dir(rel)) == "cmd/pandokos" {
				pkg = "main"
			}
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok {
					out[pkg+"."+fn.Name.Name] = append(out[pkg+"."+fn.Name.Name], filepath.ToSlash(rel))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// makefileFuzzRuns returns the fuzz commands of the Makefile as "package.FuzzName" with the
// directory of each.
func makefileFuzzRuns(t *testing.T) map[string]string {
	t.Helper()
	makefile := read(t, "Makefile")
	i := strings.Index(makefile, "\nfuzz:")
	if i < 0 {
		t.Fatal("the Makefile has no fuzz target")
	}
	block := makefile[i+1:]
	if j := strings.Index(block, "\n\n"); j >= 0 {
		block = block[:j]
	}
	runs := map[string]string{}
	re := regexp.MustCompile(`go test (\./[^ ]+) .*-fuzz '\^(Fuzz[A-Za-z0-9_]+)\$\$'`)
	for _, m := range re.FindAllStringSubmatch(block, -1) {
		dir := strings.TrimSuffix(strings.TrimPrefix(m[1], "./"), "/")
		pkg := filepath.Base(dir)
		if dir == "cmd/pandokos" {
			pkg = "main"
		}
		runs[pkg+"."+m[2]] = dir
	}
	return runs
}

// TestNetworkParsersAreFuzzed is T-R91: every row of the parser table of the model names a
// function that exists in the package it names and a fuzz target that exists and runs in make fuzz;
// every fuzz target of the Makefile is in the table.
func TestNetworkParsersAreFuzzed(t *testing.T) {
	text := read(t, modelFile)
	rows := mdTable(text, "Input", "Parser", "Fuzz target")
	if len(rows) == 0 {
		t.Fatalf("%s has no table with the header | Input | Parser | Fuzz target |", modelFile)
	}
	tests := testFunctions(t)
	declared := declaredFunctions(t)
	runs := makefileFuzzRuns(t)
	listed := map[string]bool{}
	for _, r := range rows {
		if len(r) != 3 {
			t.Errorf("row %v: %d cells, want 3", r, len(r))
			continue
		}
		parsers := backticked(r[1])
		if len(parsers) == 0 {
			t.Errorf("%q names no parser in back quotes", r[0])
		}
		for _, p := range parsers {
			if !strings.Contains(p, ".") {
				continue // a part of speech, not a function: `package.Function` names are the ones checked
			}
			if len(declared[p]) == 0 {
				t.Errorf("%q: the parser %s is declared by no function of that package", r[0], p)
			}
		}
		targets := backticked(r[2])
		if len(targets) == 0 {
			t.Errorf("%q names no fuzz target", r[0])
		}
		for _, f := range targets {
			listed[f] = true
			files := tests[f]
			switch {
			case !strings.Contains(f, ".Fuzz"):
				t.Errorf("%q: %s is not a fuzz target (package.FuzzName)", r[0], f)
			case len(files) != 1:
				t.Errorf("%q: the fuzz target %s is declared by %d files", r[0], f, len(files))
			default:
				dir := runs[f]
				if dir == "" {
					t.Errorf("%q: the fuzz target %s is not run by make fuzz", r[0], f)
				} else if filepath.ToSlash(filepath.Dir(files[0])) != dir {
					t.Errorf("%q: %s is declared in %s and make fuzz runs it in %s", r[0], f, filepath.Dir(files[0]), dir)
				}
			}
		}
	}
	var missing []string
	for f := range runs {
		if !listed[f] {
			missing = append(missing, f)
		}
	}
	sort.Strings(missing)
	for _, f := range missing {
		t.Errorf("make fuzz runs %s, which the parser table of the model does not list", f)
	}
}

var (
	reviewStatusSet   = regexp.MustCompile(`^(proved|proved \(wording corrected\)|test added|fixed)(; residual accepted)?$`)
	reviewDecisionSet = map[string]bool{"fixed": true, "test added": true, "wording": true, "accepted": true, "deferred": true, "maintainer action": true}
	hex40             = regexp.MustCompile(`^[0-9a-f]{40}$`)
	isoDate           = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)
)

// TestThreatModelReview is T-R90 (sprint 12.2, section 5.4): the dated record has its header
// fields; one status row per identifier of the model and none more, from the closed set; one row
// per finding with a decision from the closed set, a test that exists for every `fixed` and `test
// added`, a sprint or an issue for every `deferred` and an acceptance with a date for every
// `accepted`. The placeholder `pending (plan F5)` counts as empty, so the test is red until the
// maintainer has decided.
func TestThreatModelReview(t *testing.T) {
	path := newestReview(t)
	text := read(t, path)
	tests := testFunctions(t)

	t.Run("the header", func(t *testing.T) {
		field := func(name string) string {
			for _, l := range strings.Split(text, "\n") {
				if v, ok := strings.CutPrefix(l, "- "+name+":"); ok {
					return strings.TrimSpace(v)
				}
			}
			t.Errorf("%s has no line \"- %s: ...\"", path, name)
			return ""
		}
		if v := field("Date"); !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(v) {
			t.Errorf("Date %q is not a date", v)
		}
		if v := strings.Trim(field("Reviewed commit"), "` "); v != reviewedCommit {
			t.Errorf("Reviewed commit %q, want %s", v, reviewedCommit)
		}
		if v := strings.Trim(field("Closing commit"), "` "); !hex40.MatchString(v) {
			t.Errorf("Closing commit %q is not a 40-digit commit hash", v)
		}
		for _, name := range []string{"Reviewed by", "Risk"} {
			if v := field(name); len(v) < 10 || strings.Contains(strings.ToLower(v), "pending") {
				t.Errorf("%s is %q", name, v)
			}
		}
		for _, h := range []string{"Method", "Findings", "Residual exposures", "Supply chain", "Not reviewed"} {
			if !regexp.MustCompile(`(?m)^#{2,3} .*` + h).MatchString(text) {
				t.Errorf("%s has no section about %q", path, h)
			}
		}
	})

	t.Run("one status per identifier of the model", func(t *testing.T) {
		boundaries, statements, threats, _ := modelIdentifiers(t)
		want := map[string]bool{}
		for _, rows := range [][][]string{boundaries, statements, threats} {
			for _, r := range rows {
				if len(r) > 0 {
					want[r[0]] = true
				}
			}
		}
		_, rows := tableOf(text, "ID", "Status")
		if len(rows) == 0 {
			t.Fatalf("%s has no table | ID | Status | ... |", path)
		}
		got := map[string]bool{}
		for _, r := range rows {
			if len(r) < 2 {
				continue
			}
			if got[r[0]] {
				t.Errorf("%s: two rows", r[0])
			}
			got[r[0]] = true
			if !want[r[0]] {
				t.Errorf("%s: a row for an identifier the model does not have", r[0])
			}
			if !reviewStatusSet.MatchString(strings.Trim(r[1], " ")) {
				t.Errorf("%s: the status %q is not one of proved, proved (wording corrected), test added, fixed, each optionally followed by \"; residual accepted\"", r[0], r[1])
			}
		}
		for id := range want {
			if !got[id] {
				t.Errorf("%s: no row in the record", id)
			}
		}
	})

	t.Run("the findings", func(t *testing.T) {
		header, rows := tableOf(text, "Finding", "Decision")
		if len(rows) == 0 {
			t.Fatalf("%s has no table | Finding | Decision | ... |", path)
		}
		col := map[string]int{}
		for i, h := range header {
			col[h] = i
		}
		for _, name := range []string{"Shown by", "Deferred to", "Accepted by"} {
			if _, ok := col[name]; !ok {
				t.Fatalf("the findings table has no column %q (header %v)", name, header)
			}
		}
		cell := func(r []string, name string) string {
			if col[name] < len(r) {
				return strings.TrimSpace(r[col[name]])
			}
			return ""
		}
		empty := func(s string) bool {
			s = strings.ToLower(strings.TrimSpace(s))
			return s == "" || s == "-" || s == "n/a" || strings.Contains(s, "pending")
		}
		seen := map[string]bool{}
		for _, r := range rows {
			id := r[0]
			if !regexp.MustCompile(`^F-\d+$`).MatchString(id) {
				t.Errorf("a finding row with the identifier %q", id)
				continue
			}
			seen[id] = true
			decision := cell(r, "Decision")
			if !reviewDecisionSet[decision] {
				t.Errorf("%s: the decision %q is not one of fixed, test added, wording, accepted, deferred, maintainer action", id, decision)
			}
			shown := cell(r, "Shown by")
			if empty(shown) {
				t.Errorf("%s: nothing shows the decision", id)
			}
			switch decision {
			case "fixed", "test added":
				found := false
				for _, b := range backticked(shown) {
					if proofRE.MatchString(b) && len(tests[b]) == 1 {
						found = true
					}
				}
				if !found {
					t.Errorf("%s: %q is shown by no test that exists (package.TestName in back quotes)", id, decision)
				}
			case "deferred":
				if to := cell(r, "Deferred to"); !regexp.MustCompile(`sprint \d+\.\d+|#\d+`).MatchString(to) {
					t.Errorf("%s: deferred to %q, which is neither a sprint nor an issue", id, to)
				}
			case "accepted":
				by := cell(r, "Accepted by")
				if empty(by) || !isoDate.MatchString(by) {
					t.Errorf("%s: accepted, and the acceptance %q has no maintainer and no date (the placeholder pending (plan F5) is empty)", id, by)
				}
			}
		}
		for n := 1; n <= 21; n++ {
			if id := "F-" + strconv.Itoa(n); !seen[id] {
				t.Errorf("the record has no row for %s", id)
			}
		}
	})
}
