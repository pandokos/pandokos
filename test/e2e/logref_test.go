package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// logRow is one message of docs/reference/logs.md.
type logRow struct {
	levels     map[string]bool
	fields     map[string]bool
	identities map[string]bool // the platform identities the row may carry
}

var (
	reRowHeading = regexp.MustCompile("^### `(.+)`$")
	reCode       = regexp.MustCompile("`([^`]+)`")
)

// loadLogReference parses docs/reference/logs.md: each message is a level-3 heading
// ### `message` followed by a line `Level: …` (one or more of debug, info, warn, error), a line
// `Fields: ` with the fields in backticks (or `none`) and, for a line that may carry the identity of
// a platform principal, a line `Platform identities: ` with those fields; the glossary of fields is
// the table under `## Fields`.
func loadLogReference(t testing.TB) (rows map[string]*logRow, glossary map[string]bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "docs", "reference", "logs.md"))
	if err != nil {
		t.Errorf("docs/reference/logs.md: %v", err)
		return nil, nil
	}
	rows, glossary = map[string]*logRow{}, map[string]bool{}
	var cur *logRow
	inGlossary := false
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, " \r")
		switch {
		case strings.HasPrefix(line, "## "):
			inGlossary, cur = strings.TrimSpace(line) == "## Fields", nil
		case reRowHeading.MatchString(line):
			cur = &logRow{levels: map[string]bool{}, fields: map[string]bool{}, identities: map[string]bool{}}
			rows[reRowHeading.FindStringSubmatch(line)[1]] = cur
			inGlossary = false
		case inGlossary && strings.HasPrefix(line, "|"):
			if m := reCode.FindStringSubmatch(line); m != nil && strings.HasPrefix(line, "| `") {
				glossary[m[1]] = true
			}
		case cur != nil && strings.HasPrefix(line, "Level:"):
			for _, l := range strings.FieldsFunc(strings.TrimPrefix(line, "Level:"), func(r rune) bool { return r == ',' || r == ' ' }) {
				cur.levels[strings.ToLower(l)] = true
			}
		case cur != nil && strings.HasPrefix(line, "Fields:"):
			for _, m := range reCode.FindAllStringSubmatch(line, -1) {
				cur.fields[m[1]] = true
			}
		case cur != nil && strings.HasPrefix(line, "Platform identities:"):
			for _, m := range reCode.FindAllStringSubmatch(line, -1) {
				cur.identities[m[1]] = true
			}
		}
	}
	return rows, glossary
}

// checkLogReference is T-930: every line of the standard output of the process is a JSON object whose
// msg is a row of the log reference and whose fields are those of the row (besides time, level and
// msg); no line holds an identifier of the tenants, service accounts, keys or credentials of the
// test, or a canary. It runs once per process, when the process has exited.
func checkLogReference(t *testing.T, p *proc) {
	t.Helper()
	if p.logChecked {
		return
	}
	p.logChecked = true
	rows, glossary := loadLogReference(t)
	if rows == nil {
		return
	}
	text := p.stdout.String()
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		text = text[:i] // a trailing fragment is not a line
	} else {
		return
	}
	needles := append(append([]string(nil), fixtures.Canaries()...), p.ids...)
	reported := map[string]bool{}
	report := func(key, format string, args ...any) {
		if !reported[key] {
			reported[key] = true
			t.Errorf(format, args...)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			report("json:"+line, "a line of the log is not a JSON object: %.200s", line)
			continue
		}
		msg, _ := obj["msg"].(string)
		// The error log of the standard library's HTTP servers (http.Server.ErrorLog) writes its own
		// texts, which hold an address and a cause: they are the one row `http: *`.
		if strings.HasPrefix(msg, "http: ") || strings.HasPrefix(msg, "http2: ") {
			if lvl, _ := obj["level"].(string); lvl == "WARN" && len(obj) == 3 {
				msg = "http: *"
			}
		}
		row, ok := rows[msg]
		if !ok {
			report("msg:"+msg, "the message %q is not in docs/reference/logs.md: %.300s", msg, line)
			continue
		}
		if lvl, _ := obj["level"].(string); !row.levels[strings.ToLower(lvl)] {
			report("level:"+msg+lvl, "the message %q is written at level %s, which the reference does not give it", msg, lvl)
		}
		var extra []string
		for k := range obj {
			if k == "time" || k == "level" || k == "msg" {
				continue
			}
			if !row.fields[k] {
				extra = append(extra, k)
			}
			if !glossary[k] {
				report("glossary:"+k, "the field %q (of %q) is not in the glossary of docs/reference/logs.md", k, msg)
			}
		}
		if len(extra) > 0 {
			sort.Strings(extra)
			report("fields:"+msg+strings.Join(extra, ","), "the message %q has the fields %v, which docs/reference/logs.md does not list for it", msg, extra)
		}
		for _, n := range needles {
			if n != "" && strings.Contains(line, n) {
				report("id:"+msg+n, "a line of %q holds an identifier or a canary of the test: %.40q", msg, n)
			}
		}
	}
}

// TestLogReferenceParses checks the parser of the reference against the page committed with the
// tests (it fails with an empty or absent page).
func TestLogReferenceParses(t *testing.T) {
	rows, glossary := loadLogReference(t)
	if len(rows) < 50 || len(glossary) < 30 {
		t.Fatalf("docs/reference/logs.md holds %d messages and %d fields", len(rows), len(glossary))
	}
	for _, msg := range []string{"request", "listening", "starting", "trace export failing"} {
		row := rows[msg]
		if row == nil || len(row.levels) == 0 || len(row.fields) == 0 && msg != "none" {
			t.Errorf("the row of %q: %+v", msg, row)
		}
	}
	if r := rows["bootstrap key active"]; r == nil || !r.identities["key_id"] {
		t.Errorf("`bootstrap key active` is not marked as a platform identity: %+v", r)
	}
}
