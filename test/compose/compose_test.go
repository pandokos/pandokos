//go:build compose

// Package compose tests the Compose reference deployment of deploy/compose against a built image
// (PANDOKOS_TEST_IMAGE) and the fake provider standing in for the providers.
package compose

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// secretFiles are the files setup.sh creates (grafana_admin is for the observability overlay, sprint 6.1 D2).
var secretFiles = []string{"db_superuser", "db_owner", "db_gateway", "db_admin", "db_worker", "pandokos_kek", "grafana_admin", "tls_cert", "tls_key"}

// stack is one running Compose project in a temporary copy of the reference.
type stack struct {
	t       *testing.T
	dir     string // deploy/compose of the copy
	project string
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.Walk(from, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		dest := filepath.Join(to, rel)
		// A secrets directory left by a manual quick-start is not part of the reference: the test
		// must start from nothing, as setup.sh does on a fresh clone.
		if info.IsDir() && info.Name() == "secrets" {
			return filepath.SkipDir
		}
		if info.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, b, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}

// run runs a command with a bound and returns its stdout, stderr and error.
func run(t *testing.T, dir string, stdin string, bound time.Duration, name string, args ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// compose runs docker compose for the project.
func (s *stack) compose(stdin string, bound time.Duration, args ...string) (string, string, error) {
	s.t.Helper()
	full := append([]string{"compose", "-p", s.project, "-f", "compose.yaml", "-f", "override.yaml"}, args...)
	return run(s.t, s.dir, stdin, bound, "docker", full...)
}

func (s *stack) mustCompose(stdin string, bound time.Duration, args ...string) string {
	s.t.Helper()
	out, errs, err := s.compose(stdin, bound, args...)
	if err != nil {
		s.t.Fatalf("docker compose %s: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), err, out, errs)
	}
	return out
}

// pdk runs the Pandokos CLI in the container with a key file, as the guide's pdk function does.
func (s *stack) pdk(keyFile, stdin string, args ...string) string {
	s.t.Helper()
	full := append([]string{"exec", "-T", "-e", "PANDOKOS_CLI_KEY_FILE=" + keyFile, "pandokos", "/pandokos"}, args...)
	return s.mustCompose(stdin, 60*time.Second, full...)
}

// TestQuickStart is T-567: the quick-start of docs/operations/compose.md, step by step.
func TestQuickStart(t *testing.T) {
	image := os.Getenv("PANDOKOS_TEST_IMAGE")
	if image == "" {
		t.Fatal("PANDOKOS_TEST_IMAGE is not set: build the image (make image) and name it; this test fails, it does not skip")
	}
	if _, errs, err := run(t, ".", "", 30*time.Second, "docker", "compose", "version"); err != nil {
		t.Fatalf("docker compose v2 is required: %v %s", err, errs)
	}
	repo := testutil.RepoRoot(t)

	// A copy of the reference with the same relative layout, and the override of the test.
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
	secrets := filepath.Join(dir, "secrets")

	t.Run("setup.sh creates the files with their modes and changes nothing on a rerun", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			if out, errs, err := run(t, dir, "", 60*time.Second, "./setup.sh"); err != nil {
				t.Fatalf("run %d: %v\n%s\n%s", i+1, err, out, errs)
			}
			if i == 0 {
				continue
			}
		}
		before := map[string][]byte{}
		for _, name := range secretFiles {
			path := filepath.Join(secrets, name)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if info.Mode().Perm() != 0o444 {
				t.Errorf("%s has mode %o, want 0444", name, info.Mode().Perm())
			}
			b, _ := os.ReadFile(path)
			if len(bytes.TrimSpace(b)) == 0 {
				t.Errorf("%s is empty", name)
			}
			before[name] = b
		}
		if info, err := os.Stat(secrets); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("the secrets directory: %v %v", info, err)
		}
		if out, errs, err := run(t, dir, "", 60*time.Second, "./setup.sh"); err != nil {
			t.Fatalf("a third run: %v %s %s", err, out, errs)
		}
		for _, name := range secretFiles {
			if b, _ := os.ReadFile(filepath.Join(secrets, name)); !bytes.Equal(b, before[name]) {
				t.Errorf("setup.sh changed %s on a rerun", name)
			}
		}
		entries, _ := os.ReadDir(secrets)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		want := append([]string(nil), secretFiles...)
		sort.Strings(want)
		if strings.Join(names, ",") != strings.Join(want, ",") {
			t.Errorf("secrets holds %v, want %v", names, want)
		}
	})

	t.Cleanup(func() {
		if t.Failed() {
			out, _, _ := s.compose("", 60*time.Second, "logs", "--no-color")
			t.Logf("docker compose logs:\n%s", out)
		}
		_, _, _ = s.compose("", 120*time.Second, "down", "-v", "--remove-orphans")
	})

	ca := x509.NewCertPool()
	pem, err := os.ReadFile(filepath.Join(secrets, "tls_cert"))
	if err != nil {
		t.Fatal(err)
	}
	if !ca.AppendCertsFromPEM(pem) {
		t.Fatal("secrets/tls_cert holds no certificate")
	}
	trusting := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: ca, ServerName: "localhost"}}}

	s.mustCompose("", 10*time.Minute, "up", "-d", "--wait")

	t.Run("ports are on the host loopback and PostgreSQL is not published", func(t *testing.T) {
		out := s.mustCompose("", 60*time.Second, "ps", "-a", "--format", "json")
		published := 0
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if line == "" {
				continue
			}
			var c struct {
				Service    string
				State      string
				ExitCode   int
				Publishers []struct {
					URL           string
					PublishedPort int
				}
			}
			if err := json.Unmarshal([]byte(line), &c); err != nil {
				t.Fatalf("%v: %s", err, line)
			}
			for _, p := range c.Publishers {
				if p.PublishedPort == 0 {
					continue
				}
				published++
				if c.Service == "postgres" {
					t.Errorf("PostgreSQL is published on port %d", p.PublishedPort)
				}
				if p.URL != "127.0.0.1" {
					t.Errorf("%s publishes port %d on %q, want 127.0.0.1", c.Service, p.PublishedPort, p.URL)
				}
			}
			switch c.Service {
			case "migrate":
				if c.State != "exited" || c.ExitCode != 0 {
					t.Errorf("migrate: %s exit %d", c.State, c.ExitCode)
				}
			case "pandokos", "postgres":
				if c.State != "running" {
					t.Errorf("%s is %s", c.Service, c.State)
				}
			}
		}
		if published < 2 {
			t.Errorf("%d published ports, want the two listeners", published)
		}
	})

	t.Run("plain HTTP fails and TLS verifies with the generated certificate", func(t *testing.T) {
		for _, port := range []string{"8080", "8081"} {
			resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://127.0.0.1:" + port + "/readyz")
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode/100 == 2 {
					t.Errorf("plain HTTP to %s answered %d", port, resp.StatusCode)
				}
			}
		}
		testutil.Eventually(t, 90*time.Second, "readiness over TLS", func() bool {
			resp, err := trusting.Get("https://localhost:8080/readyz")
			if err != nil {
				return false
			}
			defer resp.Body.Close()
			return resp.StatusCode == 200
		})
		resp, err := trusting.Get("https://localhost:8081/v1/me")
		if err != nil {
			t.Fatalf("the admin listener over TLS: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("the admin listener answered %d without a key", resp.StatusCode)
		}
		if resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("https://localhost:8080/readyz"); err == nil {
			resp.Body.Close()
			t.Error("the certificate verified without the generated authority")
		}
	})

	t.Run("the server's database sessions use TLS", func(t *testing.T) {
		out := s.mustCompose("", 60*time.Second, "exec", "-T", "postgres", "psql", "-U", "postgres", "-d", "pandokos", "-tA", "-c",
			`SELECT a.usename || ' ' || s.ssl FROM pg_stat_ssl s JOIN pg_stat_activity a ON a.pid = s.pid WHERE a.usename LIKE 'pandokos_%' ORDER BY 1`)
		if !strings.Contains(out, "pandokos_gateway t") {
			t.Errorf("no TLS session of the gateway role:\n%s", out)
		}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if strings.HasSuffix(line, " f") {
				t.Errorf("a database session without TLS: %s", line)
			}
		}
	})

	// The quick-start. The fake provider stands in for both providers: the endpoints of openai
	// and anthropic are updated to it as private endpoints, the one deviation from the guide.
	fake := fakeprovider.NewOn(t, "0.0.0.0:0")
	fake.Accept(fixtures.CanaryCredential)
	const boot, demo = "/home/nonroot/bootstrap.key", "/home/nonroot/demo-admin.key"
	s.mustCompose("", 60*time.Second, "exec", "-T", "pandokos", "/pandokos", "init", "--key-out", boot)
	s.mustCompose("", 60*time.Second, "exec", "-T", "-e", "PANDOKOS_CLI_KEY_FILE="+boot, "pandokos", "/pandokos", "tenant", "create", "demo", "--admin-key-out", demo)
	for _, provider := range []string{"openai", "anthropic"} {
		s.pdk(boot, "", "endpoint", "update", "--base-url", fmt.Sprintf("http://host.docker.internal:%d", fake.Port()), "--private", provider, "default")
		s.pdk(boot, "", "model", "create", "--availability", "all", provider+"/fake-basic")
	}
	s.pdk(demo, fixtures.CanaryCredential, "credential", "create", "--provider", "openai", "--secret-file", "/dev/stdin", "openai-main")
	s.pdk(demo, fixtures.CanaryCredential, "credential", "create", "--provider", "anthropic", "--secret-file", "/dev/stdin", "anthropic-main")
	s.pdk(demo, "", "service-account", "create", "--role", "inference", "app")
	s.pdk(demo, "", "key", "create", "--service-account", "app", "--key-out", "/home/nonroot/app.key")
	keyCopy := filepath.Join(tmp, "app.key")
	s.mustCompose("", 60*time.Second, "cp", "pandokos:/home/nonroot/app.key", keyCopy)
	keyBytes, err := os.ReadFile(keyCopy)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSpace(string(keyBytes))

	t.Run("capability check says callable", func(t *testing.T) {
		out := s.pdk(demo, "", "capability", "check", "--provider", "anthropic", "--model", "claude-sonnet-4-5", "-o", "json")
		var got adminapi.Capability
		if err := json.Unmarshal([]byte(out), &got); err != nil || !got.Callable {
			t.Errorf("%v: %s", err, out)
		}
	})

	call := func(path string, hdr http.Header, body string) (int, string) {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://localhost:8080"+path, strings.NewReader(body))
		req.Header = hdr
		resp, err := trusting.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	t.Run("one streamed Anthropic call and one streamed OpenAI call with BYOK", func(t *testing.T) {
		status, body := call("/anthropic/v1/messages", http.Header{"Authorization": {"Bearer " + key}, "Anthropic-Version": {"2026-10-21"}, "Content-Type": {"application/json"}},
			`{"model":"fake-basic","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"`+fixtures.CanaryPrompt+`"}]}`)
		if status != 200 || body != string(fixtures.ReadAnthropic("message_stream.sse")) {
			t.Fatalf("anthropic: %d %s", status, body)
		}
		status, body = call("/openai/v1/chat/completions", http.Header{"Authorization": {"Bearer " + key}, "Content-Type": {"application/json"}},
			`{"model":"fake-basic","stream":true,"messages":[{"role":"user","content":"`+fixtures.CanaryPrompt+`"}]}`)
		if status != 200 || !strings.Contains(body, "data: [DONE]") {
			t.Fatalf("openai: %d %s", status, body)
		}
		want := map[string]string{"/v1/messages": "X-Api-Key", "/v1/chat/completions": "Authorization"}
		seen := map[string]bool{}
		for _, r := range fake.Requests() {
			if header, ok := want[r.Path]; ok {
				seen[r.Path] = true
				if r.Header.Get(header) == "" || strings.Contains(fmt.Sprint(r.Header), key) {
					t.Errorf("%s: the credential header %s %q", r.Path, header, r.Header.Get(header))
				}
			}
		}
		if len(seen) != 2 {
			t.Errorf("the fake saw %v", seen)
		}
	})

	// The usage records read while the stack runs, for the persistence check after the stop.
	usageBefore := map[string]bool{}
	t.Run("usage list shows both records with provider sources", func(t *testing.T) {
		var items []adminapi.UsageRecord
		testutil.Eventually(t, 30*time.Second, "both records", func() bool {
			out := s.pdk(demo, "", "usage", "list", "--all", "-o", "json")
			var doc struct {
				Items []adminapi.UsageRecord `json:"items"`
			}
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			items = doc.Items
			return len(items) >= 2
		})
		providers := map[string]bool{}
		for _, it := range items {
			usageBefore[it.ID] = true
			providers[it.Provider] = true
			if it.InputSource != "provider" || it.OutputSource != "provider" || it.InputTokens == 0 || !it.Stream {
				t.Errorf("record %+v", it)
			}
		}
		if !providers["openai"] || !providers["anthropic"] {
			t.Errorf("providers %v", providers)
		}
	})

	t.Run("docker compose stop exits 0 within the grace period", func(t *testing.T) {
		began := time.Now()
		s.mustCompose("", 90*time.Second, "stop")
		if took := time.Since(began); took > 45*time.Second {
			t.Errorf("stop took %s, the grace period is 45s", took)
		}
		out := s.mustCompose("", 60*time.Second, "ps", "-a", "--format", "json")
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			var c struct {
				Service  string
				ExitCode int
			}
			if line != "" && json.Unmarshal([]byte(line), &c) == nil && c.Service == "pandokos" && c.ExitCode != 0 {
				t.Errorf("pandokos exited with %d", c.ExitCode)
			}
		}
	})

	t.Run("no canary and no secret in the logs", func(t *testing.T) {
		logs := s.mustCompose("", 60*time.Second, "logs", "--no-color")
		var secretValues []string
		for _, name := range secretFiles {
			if name == "tls_cert" {
				continue
			}
			if b, err := os.ReadFile(filepath.Join(secrets, name)); err == nil {
				secretValues = append(secretValues, strings.TrimSpace(string(b)))
			}
		}
		secretValues = append(secretValues, testutil.KeyForms(key)...)
		secretValues = append(secretValues, testutil.SecretForms(fixtures.CanaryCredential)...)
		testutil.AssertNoCanary(t, "docker compose logs", logs)
		testutil.AssertAbsent(t, "docker compose logs", logs, secretValues...)
	})

	// R1 criterion 1: the data lives in the named volumes, so it survives docker compose down
	// (without -v) and up, after the stop above; the usage records were read while the stack ran. The bootstrap and demo keys are in the cli volume and stay valid.
	t.Run("data survives docker compose down and up", func(t *testing.T) {
		usageIDs := func() map[string]bool {
			out := s.pdk(demo, "", "usage", "list", "--all", "-o", "json")
			var doc struct {
				Items []adminapi.UsageRecord `json:"items"`
			}
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			ids := map[string]bool{}
			for _, it := range doc.Items {
				ids[it.ID] = true
			}
			return ids
		}
		before := usageBefore
		if len(before) < 2 {
			t.Fatalf("%d usage records before down, want at least 2", len(before))
		}
		s.mustCompose("", 3*time.Minute, "down", "--remove-orphans")
		s.mustCompose("", 10*time.Minute, "up", "-d", "--wait")
		testutil.Eventually(t, 90*time.Second, "readiness after up", func() bool {
			resp, err := trusting.Get("https://localhost:8080/readyz")
			if err != nil {
				return false
			}
			defer resp.Body.Close()
			return resp.StatusCode == 200
		})
		if out := s.pdk(boot, "", "tenant", "list", "-o", "json"); !strings.Contains(out, `"demo"`) {
			t.Errorf("the tenant demo is gone after down and up:\n%s", out)
		}
		after := usageIDs()
		for id := range before {
			if !after[id] {
				t.Errorf("usage record %s is gone after down and up", id)
			}
		}
	})
}
