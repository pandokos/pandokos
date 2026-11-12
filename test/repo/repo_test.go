// Package repo holds conformance tests that read the files of the repository.
package repo

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/test/fixtures"
)

var (
	rootOnce sync.Once
	rootDir  string
)

// root finds the repository root: the directory with the go.mod of the product module.
func root(t *testing.T) string {
	t.Helper()
	rootOnce.Do(func() {
		dir, err := os.Getwd()
		if err != nil {
			return
		}
		for {
			b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
			if err == nil && strings.Contains(string(b), "module github.com/pandokos/pandokos\n") {
				rootDir = dir
				return
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				return
			}
			dir = parent
		}
	})
	if rootDir == "" {
		t.Fatal("repository root not found")
	}
	return rootDir
}

func read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root(t), rel))
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return string(b)
}

func workflows(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root(t), ".github", "workflows", "*.yml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no workflows found: %v", err)
	}
	out := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(f)] = string(b)
	}
	for _, need := range []string{"ci.yml", "release.yml"} {
		if _, ok := out[need]; !ok {
			t.Fatalf("workflow %s is missing", need)
		}
	}
	return out
}

func TestActionsPinned(t *testing.T) {
	uses := regexp.MustCompile(`^\s*-?\s*uses:\s*(.+?)\s*$`)
	pinned := regexp.MustCompile(`^(\./\S+|[\w.-]+/[\w./-]+@[0-9a-f]{40}\s+# v\d+\.\d+\.\d+)$`)
	perms := regexp.MustCompile(`(?m)^permissions:\s*\n\s+contents:\s*read\s*$`)
	for name, text := range workflows(t) {
		n := 0
		for _, line := range strings.Split(text, "\n") {
			if m := uses.FindStringSubmatch(line); m != nil {
				n++
				if !pinned.MatchString(m[1]) {
					t.Errorf("%s: action not pinned to a commit with a version comment: %s", name, m[1])
				}
			}
		}
		if n == 0 {
			t.Errorf("%s: no uses: lines found", name)
		}
		if !perms.MatchString(text) {
			t.Errorf("%s: no top-level permissions with contents: read", name)
		}
		if strings.Contains(text, "pull_request_target") {
			t.Errorf("%s: pull_request_target is forbidden", name)
		}
	}
}

func TestDependencies(t *testing.T) {
	gomod := read(t, "go.mod")
	var direct []string
	inRequire := false
	for _, line := range strings.Split(gomod, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "require (":
			inRequire = true
		case line == ")":
			inRequire = false
		case strings.HasPrefix(line, "require "):
			if !strings.Contains(line, "// indirect") {
				direct = append(direct, strings.Fields(strings.TrimPrefix(line, "require "))[0])
			}
		case inRequire && line != "" && !strings.Contains(line, "// indirect"):
			direct = append(direct, strings.Fields(line)[0])
		}
	}
	if len(direct) != 1 || direct[0] != "go.yaml.in/yaml/v3" {
		t.Errorf("direct requirements = %v, want only go.yaml.in/yaml/v3", direct)
	}
	if !regexp.MustCompile(`(?m)^go 1\.24\.0$`).MatchString(gomod) {
		t.Error("go.mod must declare go 1.24.0")
	}
	if regexp.MustCompile(`(?m)^toolchain\b`).MatchString(gomod) {
		t.Error("go.mod must not have a toolchain line")
	}
	if _, err := os.Stat(filepath.Join(root(t), "go.sum")); err != nil {
		t.Errorf("go.sum: %v", err)
	}
	if !strings.Contains(read(t, "test/sdk/go.mod"), "github.com/openai/openai-go/v3") {
		t.Error("test/sdk/go.mod must require the OpenAI SDK")
	}
	err := filepath.WalkDir(root(t), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root(t), path)
		if d.IsDir() && (d.Name() == ".git" || rel == filepath.Join("test", "sdk") || rel == "planning" || rel == "docs") {
			return fs.SkipDir
		}
		isSource := d.Name() == "go.mod" || d.Name() == "go.sum" || strings.HasSuffix(d.Name(), ".go")
		if d.IsDir() || !isSource || rel == filepath.Join("test", "repo", "repo_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "openai/openai-go") {
			t.Errorf("%s names the SDK; only test/sdk may", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDockerfile(t *testing.T) {
	df := read(t, "Dockerfile")
	version := strings.TrimSpace(read(t, ".go-version"))
	var froms []string
	lastFrom := -1
	lines := strings.Split(df, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "FROM ") {
			froms = append(froms, line)
			lastFrom = i
		}
	}
	if len(froms) != 2 {
		t.Fatalf("want 2 FROM lines, got %d", len(froms))
	}
	digest := regexp.MustCompile(`@sha256:[0-9a-f]{64}`)
	for _, f := range froms {
		if !digest.MatchString(f) {
			t.Errorf("not pinned by digest: %s", f)
		}
	}
	if !strings.Contains(froms[0], "golang:"+version+"-") && !strings.Contains(froms[0], "golang:"+version+"@") {
		t.Errorf("builder %q does not match .go-version %s", froms[0], version)
	}
	if !strings.Contains(froms[1], "gcr.io/distroless/static-debian13:nonroot@sha256:") {
		t.Errorf("final base: %s", froms[1])
	}
	for _, want := range []string{"USER 65532:65532", "CGO_ENABLED=0", "-trimpath"} {
		if !strings.Contains(df, want) {
			t.Errorf("Dockerfile lacks %q", want)
		}
	}
	for _, line := range lines[lastFrom+1:] {
		if strings.HasPrefix(strings.TrimSpace(line), "RUN ") {
			t.Errorf("RUN in the final stage: %s", line)
		}
	}
}

func TestSecurityPolicy(t *testing.T) {
	text := read(t, "SECURITY.md")
	for _, h := range []string{"## Reporting a vulnerability", "## Response process", "## Supported versions"} {
		if !strings.Contains(text, h+"\n") {
			t.Errorf("SECURITY.md lacks the section %q", h)
		}
	}
	_, process, ok := strings.Cut(text, "## Response process\n")
	if !ok {
		return
	}
	process, _, _ = strings.Cut(process, "\n## ")
	for _, word := range []string{"acknowledg", "triage"} {
		re := regexp.MustCompile(`(?i)` + word + `[^\n]*?(\d+) (?:working )?days`)
		m := re.FindStringSubmatch(process)
		if m == nil {
			t.Errorf("the response process states no %s target in days", word)
			continue
		}
		if n, _ := strconv.Atoi(m[1]); n <= 0 {
			t.Errorf("%s target %d", word, n)
		}
	}
}

func TestConfigurationReference(t *testing.T) {
	doc := read(t, "docs/operations/configuration.md")
	for _, k := range config.Keys() {
		for _, want := range []string{k.Name, k.Env} {
			if !strings.Contains(doc, want) {
				t.Errorf("configuration.md does not mention %s", want)
			}
		}
		if k.Default != "" && !strings.Contains(doc, k.Default) {
			t.Errorf("configuration.md does not mention the default %q of %s", k.Default, k.Name)
		}
	}
}

func TestReleaseWorkflowShape(t *testing.T) {
	text := workflows(t)["release.yml"]
	if !regexp.MustCompile(`(?m)^\s*tags:\s*\[\s*"v\*"\s*\]`).MatchString(text) && !regexp.MustCompile(`(?m)^\s*-\s*['"]?v\*['"]?\s*$`).MatchString(text) {
		t.Error("release.yml must trigger on tags v*")
	}
	for _, want := range []string{
		"cosign sign", "cosign attest", "actions/attest-build-provenance",
		"linux/amd64", "linux/arm64", "\n  verify:", "cosign verify ", "cosign verify-attestation",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("release.yml lacks %q", want)
		}
	}
}

func TestNoForbiddenStrings(t *testing.T) {
	sk := regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`)
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root(t), dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root(t), path)
			for _, c := range fixtures.Canaries() {
				if strings.Contains(string(b), c) {
					t.Errorf("%s contains a canary value", rel)
				}
			}
			if sk.Match(b) {
				t.Errorf("%s contains a string that looks like a provider key", rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
