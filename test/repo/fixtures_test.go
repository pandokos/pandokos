package repo

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/test/fixtures"
)

// TestFixturesHoldNoKeys is T-R34: no fixture holds a key, and the string values of the Anthropic
// and compatible fixtures are canaries or synthetic.
func TestFixturesHoldNoKeys(t *testing.T) {
	dir := filepath.Join(root(t), "test", "fixtures")
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`sk-ant-`), regexp.MustCompile(`sk-proj-`), regexp.MustCompile(`gsk_[A-Za-z0-9]`),
		regexp.MustCompile(`AKIA[0-9A-Z]{16}`), regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36}`), regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),
		regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	}
	// The custom rules of .gitleaks.toml.
	for _, m := range regexp.MustCompile(`(?m)^regex = '''(.+)'''\s*$`).FindAllStringSubmatch(read(t, ".gitleaks.toml"), -1) {
		re, err := regexp.Compile(m[1])
		if err != nil {
			t.Fatalf(".gitleaks.toml: %q: %v", m[1], err)
		}
		patterns = append(patterns, re)
	}
	if len(patterns) < 8 {
		t.Fatalf("%d patterns", len(patterns))
	}
	scanned := 0
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		rel, _ := filepath.Rel(root(t), path)
		for _, re := range patterns {
			if re.Match(b) {
				t.Errorf("%s matches the key pattern %s", rel, re)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 30 {
		t.Errorf("only %d fixture files were scanned", scanned)
	}

	canaries := fixtures.Canaries()
	completion := fixtures.CanaryCompletion
	structural := map[string]bool{ // wire vocabulary: enums, not content
		"type": true, "role": true, "stop_reason": true, "stop_sequence": true, "model": true, "object": true,
		"finish_reason": true, "service_tier": true, "code": true,
	}
	content := map[string]bool{"text": true, "content": true, "partial_json": true}
	enum := regexp.MustCompile(`^[a-z0-9_.-]+$`)
	var walk func(t *testing.T, where, key string, v any)
	walk = func(t *testing.T, where, key string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				walk(t, where, k, e)
			}
		case []any:
			for _, e := range x {
				walk(t, where, key, e)
			}
		case string:
			switch {
			case strings.Contains(x, "synthetic"):
			case structural[key] && enum.MatchString(x):
			case content[key] && (x == " end" || x == "" || strings.Contains(completion, x)):
			default:
				for _, c := range canaries {
					if strings.Contains(x, c) {
						return
					}
				}
				t.Errorf("%s: the string value %q of %q is neither a canary nor synthetic", where, x, key)
			}
		}
	}
	for _, d := range []string{"anthropic", "compat"} {
		for _, name := range fixtures.Dir(d) {
			where := d + "/" + name
			var raw []byte
			if d == "compat" {
				raw = fixtures.ReadCompat(name)
			} else {
				raw = fixtures.ReadAnthropic(name)
			}
			switch {
			case strings.HasSuffix(name, ".json"):
				var doc any
				if err := json.Unmarshal(raw, &doc); err != nil {
					t.Fatalf("%s: %v", where, err)
				}
				walk(t, where, "", doc)
			case strings.HasSuffix(name, ".sse"):
				for _, line := range bytes.Split(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")), []byte("\n")) {
					payload, ok := bytes.CutPrefix(line, []byte("data: "))
					if !ok || string(payload) == "[DONE]" {
						continue
					}
					var doc any
					if err := json.Unmarshal(payload, &doc); err != nil {
						t.Fatalf("%s: %v", where, err)
					}
					walk(t, where, "", doc)
				}
			}
		}
	}
}
