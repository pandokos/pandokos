//go:build compose

package compose

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// readmeCall returns the command of the third step of the README quick start, as written.
func readmeCall(t *testing.T, repo string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	i := strings.Index(readme, "**3. Call it.**")
	if i < 0 {
		t.Fatal("README.md has no step \"**3. Call it.**\"")
	}
	rest := readme[i:]
	j := strings.Index(rest, "```bash\n")
	if j < 0 {
		t.Fatal("the third step of the README has no bash block")
	}
	rest = rest[j+len("```bash\n"):]
	k := strings.Index(rest, "\n```")
	if k < 0 {
		t.Fatal("the bash block of the third step is not closed")
	}
	return rest[:k]
}

// TestQuickstartScript is T-609: deploy/compose/quickstart.sh with --yes in a copy of the reference,
// then the call of the README's third step, read from README.md and run as written; then the
// reruns. The fake provider stands in for OpenAI: after the script, the endpoint of openai points
// to it and gpt-4o-mini is sent upstream as fake-basic, the one deviation from the README.
func TestQuickstartScript(t *testing.T) {
	image := os.Getenv("PANDOKOS_TEST_IMAGE")
	if image == "" {
		t.Fatal("PANDOKOS_TEST_IMAGE is not set: build the image (make image) and name it; this test fails, it does not skip")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Fatal("curl is required: the README's call is run as written")
	}
	repo := testutil.RepoRoot(t)
	call := readmeCall(t, repo)

	tmp := t.TempDir()
	copyTree(t, filepath.Join(repo, "deploy", "compose"), filepath.Join(tmp, "deploy", "compose"))
	copyTree(t, filepath.Join(repo, "deploy", "postgres"), filepath.Join(tmp, "deploy", "postgres"))
	dir := filepath.Join(tmp, "deploy", "compose")
	override, err := os.ReadFile(filepath.Join(repo, "test", "compose", "override.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "override.yaml"), override, 0o644); err != nil {
		t.Fatal(err)
	}
	var nonce [4]byte
	_, _ = rand.Read(nonce[:])
	s := &stack{t: t, dir: dir, project: "pdk-" + hex.EncodeToString(nonce[:])}
	t.Setenv("PANDOKOS_TEST_IMAGE", image)

	// The script and the README call run plain docker compose: the project and the override come
	// from the environment Compose reads. A key of the developer's environment never reaches them.
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "OPENAI_API_KEY=") && !strings.HasPrefix(kv, "COMPOSE_") {
			env = append(env, kv)
		}
	}
	env = append(env, "COMPOSE_PROJECT_NAME="+s.project, "COMPOSE_FILE=compose.yaml:override.yaml")
	sh := func(bound time.Duration, extra []string, name string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), bound)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		cmd.Env = append(append([]string(nil), env...), extra...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	quickstart := func(t *testing.T, extra ...string) string {
		t.Helper()
		out, err := sh(10*time.Minute, extra, "./quickstart.sh", "--yes")
		if err != nil {
			t.Fatalf("quickstart.sh: %v\n%s", err, out)
		}
		testutil.AssertAbsent(t, "the output of quickstart.sh", out, testutil.SecretForms(fixtures.CanaryCredential)...)
		return out
	}
	keyFile := filepath.Join(dir, "quickstart.key")
	readKey := func(t *testing.T) string {
		t.Helper()
		info, err := os.Stat(keyFile)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("quickstart.key has mode %o, want 0600", info.Mode().Perm())
		}
		b, _ := os.ReadFile(keyFile)
		key := strings.TrimSpace(string(b))
		if !strings.HasPrefix(key, "pdk_") || strings.ContainsAny(key, " \n") {
			t.Fatalf("quickstart.key does not hold one key: %q", b)
		}
		return key
	}

	t.Cleanup(func() {
		if t.Failed() {
			out, _, _ := s.compose("", 60*time.Second, "logs", "--no-color")
			t.Logf("docker compose logs:\n%s", out)
		}
		_, _, _ = s.compose("", 120*time.Second, "down", "-v", "--remove-orphans")
	})

	if out, err := sh(time.Minute, nil, "./quickstart.sh", "--no-such-flag"); err == nil {
		t.Errorf("an unknown argument was accepted:\n%s", out)
	}

	var key, first string
	t.Run("a first run does every step and writes the key, without printing it", func(t *testing.T) {
		first = quickstart(t, "OPENAI_API_KEY="+fixtures.CanaryCredential)
		for _, want := range []string{"[1/5]", "[5/5]", "Pandokos is ready"} {
			if !strings.Contains(first, want) {
				t.Errorf("the output lacks %q:\n%s", want, first)
			}
		}
		if n := strings.Count(first, "already done, skipped"); n != 0 {
			t.Errorf("a first run skipped %d steps:\n%s", n, first)
		}
		key = readKey(t)
		testutil.AssertAbsent(t, "the output of quickstart.sh", first, testutil.KeyForms(key)...)
	})
	if key == "" {
		t.FailNow()
	}

	t.Run("the script prints the README's call", func(t *testing.T) {
		for _, line := range strings.Split(call, "\n") {
			if !strings.Contains(first, strings.TrimSpace(line)) {
				t.Errorf("the output of quickstart.sh lacks the README line %q", strings.TrimSpace(line))
			}
		}
	})

	const boot, demo = "/home/nonroot/bootstrap.key", "/home/nonroot/demo-admin.key"
	fake := fakeprovider.NewOn(t, "0.0.0.0:0")
	fake.Accept(fixtures.CanaryCredential)
	s.pdk(boot, "", "endpoint", "update", "--base-url", fmt.Sprintf("http://host.docker.internal:%d", fake.Port()), "--private", "openai", "default")
	s.pdk(boot, "", "model", "update", "--upstream-id", fakeprovider.ModelBasic, "openai/gpt-4o-mini")

	readmeWorks := func(t *testing.T) {
		t.Helper()
		var out string
		testutil.Eventually(t, 60*time.Second, "the README's call answers", func() bool {
			out, _ = sh(time.Minute, nil, "sh", "-c", call)
			return strings.Contains(out, `"choices"`)
		})
		if strings.Contains(out, `"error"`) {
			t.Errorf("the README's call: %s", out)
		}
	}

	t.Run("the README's call answers with the key and leaves a usage record", func(t *testing.T) {
		readmeWorks(t)
		out := s.pdk(demo, "", "usage", "list", "--all", "-o", "json")
		var doc struct {
			Items []adminapi.UsageRecord `json:"items"`
		}
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		found := false
		for _, it := range doc.Items {
			if it.Provider == "openai" && it.Model == "gpt-4o-mini" && it.Outcome == "ok" {
				found = true
			}
		}
		if !found {
			t.Errorf("no successful gpt-4o-mini record: %s", out)
		}
	})

	t.Run("a rerun without the OpenAI key skips every step and keeps the key", func(t *testing.T) {
		out := quickstart(t)
		if n := strings.Count(out, "already done, skipped"); n != 3 {
			t.Errorf("%d steps skipped, want 3:\n%s", n, out)
		}
		if got := readKey(t); got != key {
			t.Error("a rerun replaced quickstart.key")
		}
	})

	t.Run("a rerun after the key file is lost issues a new key that works", func(t *testing.T) {
		if err := os.Remove(keyFile); err != nil {
			t.Fatal(err)
		}
		out := quickstart(t)
		if n := strings.Count(out, "already done, skipped"); n != 2 {
			t.Errorf("%d steps skipped, want 2:\n%s", n, out)
		}
		if readKey(t) == key {
			t.Error("the same key again")
		}
		readmeWorks(t)
	})

	t.Run("no secret in the logs", func(t *testing.T) {
		logs := s.mustCompose("", 60*time.Second, "logs", "--no-color")
		secrets := append(testutil.KeyForms(key), testutil.SecretForms(fixtures.CanaryCredential)...)
		testutil.AssertAbsent(t, "docker compose logs", logs, secrets...)
	})
}
