package repo

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// logicalLines joins the lines of a pip requirements file that end in a backslash.
func logicalLines(text string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasSuffix(trimmed, `\`) {
			cur.WriteString(strings.TrimSuffix(trimmed, `\`) + " ")
			continue
		}
		cur.WriteString(trimmed)
		out = append(out, cur.String())
		cur.Reset()
	}
	return out
}

// TestSDKLockfiles is T-R33: the SDK suites install exactly what was locked.
func TestSDKLockfiles(t *testing.T) {
	t.Run("requirements.in pins with ==", func(t *testing.T) {
		n := 0
		for _, l := range logicalLines(read(t, "test/sdk/python/requirements.in")) {
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			n++
			if !regexp.MustCompile(`^[A-Za-z0-9_.-]+==[0-9][A-Za-z0-9.!+_-]*$`).MatchString(l) {
				t.Errorf("requirements.in: %q is not an exact pin", l)
			}
		}
		if n != 3 {
			t.Errorf("%d requirements, want openai, anthropic and pytest", n)
		}
	})
	t.Run("requirements.txt: every requirement has == and a hash", func(t *testing.T) {
		n := 0
		for _, l := range logicalLines(read(t, "test/sdk/python/requirements.txt")) {
			if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "--") {
				continue
			}
			n++
			if !regexp.MustCompile(`^[A-Za-z0-9_.-]+==[0-9][^\s]*`).MatchString(l) {
				t.Errorf("requirements.txt: %q has no exact version", l)
			}
			if !strings.Contains(l, "--hash=sha256:") {
				t.Errorf("requirements.txt: %q has no hash", l)
			}
		}
		if n < 10 {
			t.Errorf("%d locked requirements: the lock looks incomplete", n)
		}
	})
	t.Run("package.json versions are exact and package-lock.json agrees", func(t *testing.T) {
		var pkg struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if err := json.Unmarshal([]byte(read(t, "test/sdk/ts/package.json")), &pkg); err != nil {
			t.Fatal(err)
		}
		direct := map[string]string{}
		for k, v := range pkg.Dependencies {
			direct[k] = v
		}
		for k, v := range pkg.DevDependencies {
			direct[k] = v
		}
		if len(direct) != 2 || direct["openai"] == "" || direct["@anthropic-ai/sdk"] == "" {
			t.Fatalf("direct dependencies %v, want openai and @anthropic-ai/sdk", direct)
		}
		for name, v := range direct {
			if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(v) {
				t.Errorf("package.json: %s %q is not an exact version", name, v)
			}
		}
		var lock struct {
			LockfileVersion int `json:"lockfileVersion"`
			Packages        map[string]struct {
				Version   string `json:"version"`
				Integrity string `json:"integrity"`
				Link      bool   `json:"link"`
			} `json:"packages"`
		}
		if err := json.Unmarshal([]byte(read(t, "test/sdk/ts/package-lock.json")), &lock); err != nil {
			t.Fatal(err)
		}
		if lock.LockfileVersion != 3 {
			t.Errorf("lockfileVersion %d, want 3", lock.LockfileVersion)
		}
		if len(lock.Packages) < 5 {
			t.Errorf("%d packages in the lock", len(lock.Packages))
		}
		for path, p := range lock.Packages {
			if path == "" || p.Link {
				continue
			}
			if p.Integrity == "" {
				t.Errorf("package-lock.json: %s has no integrity", path)
			}
		}
		for name, v := range direct {
			if got := lock.Packages["node_modules/"+name].Version; got != v {
				t.Errorf("the lock has %s at %q, package.json says %q", name, got, v)
			}
		}
	})
	t.Run("CI and the make targets install with hashes and without scripts", func(t *testing.T) {
		ci := read(t, ".github/workflows/ci.yml")
		makefile := read(t, "Makefile")
		for what, text := range map[string]string{"ci.yml": ci, "Makefile": makefile} {
			if !strings.Contains(text, "--require-hashes") {
				t.Errorf("%s does not install the Python packages with --require-hashes", what)
			}
			if !strings.Contains(text, "--only-binary :all:") || !strings.Contains(text, "--no-deps") {
				t.Errorf("%s does not install with --only-binary :all: --no-deps", what)
			}
		}
		if !strings.Contains(makefile, "npm ci --ignore-scripts") {
			t.Error("the Makefile does not install the Node packages with npm ci --ignore-scripts")
		}
		for _, job := range []string{"sdk-python:", "sdk-ts:"} {
			if !strings.Contains(ci, job) {
				t.Errorf("ci.yml has no job %s", job)
			}
		}
		if !strings.Contains(ci, "make test-sdk-python") || !strings.Contains(ci, "make test-sdk-ts") {
			t.Error("ci.yml does not run the SDK suites through the make targets")
		}
	})
	t.Run("every action is pinned to a commit", func(t *testing.T) {
		pin := regexp.MustCompile(`^\s*-?\s*uses:\s*(\S+)`)
		full := regexp.MustCompile(`^(\./\S+|[\w.-]+/[\w./-]+@[0-9a-f]{40})$`)
		for name, text := range workflows(t) {
			for _, line := range strings.Split(text, "\n") {
				if m := pin.FindStringSubmatch(line); m != nil && !full.MatchString(m[1]) {
					t.Errorf("%s: %s is not pinned to 40 hexadecimal characters", name, m[1])
				}
			}
		}
	})
}
