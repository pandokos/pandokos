package repo

import (
	"bytes"
	"encoding/base64"
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

// TestNoKeyMaterial is T-R37: no file of the repository holds a PEM block, a JWK with private
// members or a string of JWT shape, except the files that have to name the patterns. Keys and
// tokens of the tests are made when the tests run, and what is made lives under local/ or in a
// temporary directory, neither of which is scanned (local/ is not versioned).
func TestNoKeyMaterial(t *testing.T) {
	pem := regexp.MustCompile(strings.Repeat("-", 5) + "BEGIN") // not written out: this file must not match itself
	jwt := regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]*`)
	kty := regexp.MustCompile(`"kty"`)
	private := regexp.MustCompile(`"d"\s*:`)

	// The files that name a pattern without holding the thing, and why. A prefix ending in / is a directory.
	named := map[string]string{
		"test/repo/fixtures_test.go":       "T-R34, in this file, names the PEM armour it looks for in the fixtures",
		"planning/":                        "the design documents describe these patterns and their tests",
		"docs/history/":                    "the record of the design documents",
		"internal/config/upstream_test.go": "a synthetic block of another type than a key (a CRL around marker bytes), made to test the refusal of a file that holds no certificate",
	}
	exempt := func(rel string) bool {
		for p := range named {
			if rel == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(rel, p)) {
				return true
			}
		}
		return false
	}
	scanned := 0
	err := filepath.WalkDir(root(t), func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root(t), path)
		rel = filepath.ToSlash(rel)
		if e.IsDir() {
			switch rel {
			case ".git", "local", "bin":
				return fs.SkipDir
			}
			switch filepath.Base(rel) {
			case "node_modules", "vendor", ".venv", "__pycache__":
				return fs.SkipDir
			}
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 8<<20 {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.IndexByte(b, 0) >= 0 { // a binary file
			return nil
		}
		scanned++
		if exempt(rel) {
			return nil
		}
		if pem.Match(b) {
			t.Errorf("%s holds a PEM block", rel)
		}
		if jwt.Match(b) {
			t.Errorf("%s holds a string of JWT shape: %q", rel, jwt.Find(b))
		}
		if kty.Match(b) && private.Match(b) {
			t.Errorf("%s holds a JWK with a private member", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 300 {
		t.Errorf("only %d files were scanned", scanned)
	}
	// What is exempt exists: a stale entry hides a file that moved.
	for p := range named {
		if _, err := os.Stat(filepath.Join(root(t), filepath.FromSlash(p))); err != nil {
			t.Errorf("the exemption %s does not name a file or a directory: %v", p, err)
		}
	}
	// The patterns catch what they are for; the samples are made here, not written out, so that this
	// file holds none of them.
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	q := func(name string) string { return `"` + name + `"` }
	for name, c := range map[string]struct {
		re   *regexp.Regexp
		text string
	}{
		"a PEM block":   {pem, strings.Repeat("-", 5) + "BEGIN PRIVATE KEY" + strings.Repeat("-", 5)},
		"a JWT":         {jwt, enc(`{"alg":"ES256"}`) + "." + enc(`{"sub":"workload"}`) + "." + enc("signature-bytes")},
		"a JWT, no sig": {jwt, enc(`{"alg":"none"}`) + "." + enc(`{"sub":"workload"}`) + "."},
		"a private JWK": {private, "{" + q("kty") + `:"EC",` + q("d") + `:"c"}`},
		"a JWK, spaced": {private, "{" + q("kty") + `: "EC", ` + q("d") + ` : "c"}`},
		"a key type":    {kty, "{" + q("kty") + `:"oct"}`},
	} {
		if !c.re.MatchString(c.text) {
			t.Errorf("the pattern for %s does not match %q", name, c.text)
		}
	}
}
