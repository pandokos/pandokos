// Package e2e runs the built pandokos binary against the fake provider.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

var binary string

func TestMain(m *testing.M) {
	os.Exit(func() int {
		dir, err := os.MkdirTemp("", "pandokos-e2e-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer os.RemoveAll(dir)
		rootOut, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
		if err != nil {
			fmt.Fprintln(os.Stderr, "go list:", err)
			return 1
		}
		binary = filepath.Join(dir, "pandokos")
		build := exec.Command("go", "build", "-o", binary, "github.com/pandokos/pandokos/cmd/pandokos")
		build.Dir = strings.TrimSpace(string(rootOut))
		build.Stderr = os.Stderr
		if err := build.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "go build:", err)
			return 1
		}
		return m.Run()
	}())
}

type opts struct {
	level     string
	baseURL   string
	env       []string
	db        *dbtest.Database // nil: a new database of the test
	noMigrate bool             // true: the database is used as it is
}

type proc struct {
	cmd    *exec.Cmd
	stdout *testutil.LogBuffer
	stderr *testutil.LogBuffer
	exited chan struct{}
	code   int
	addr   string
	cfg    string
	env    []string
	db     *dbtest.Database
}

func baseEnv(t *testing.T, baseURL string, extra []string) []string {
	t.Helper()
	return append([]string{
		"PATH=" + os.Getenv("PATH"),
		"PANDOKOS_SKELETON_OPENAI_BASE_URL=" + baseURL,
		"PANDOKOS_SKELETON_OPENAI_API_KEY_FILE=" + testutil.WriteSecret(t, "openai_api_key", fixtures.CanaryProviderKey),
	}, extra...)
}

func writeConfig(t *testing.T, level string) string {
	t.Helper()
	tokenFile := testutil.WriteSecret(t, "access_token", fixtures.CanaryAccessToken)
	content := "log:\n  level: " + level + "\nlisteners:\n  inference:\n    address: 127.0.0.1:0\nshutdown:\n  drain_timeout: 5s\nskeleton:\n  access_token_file: " + tokenFile + "\n"
	p := filepath.Join(t.TempDir(), "pandokos.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// completeLines returns the decoded complete lines (a trailing fragment is ignored) whose msg matches.
func completeLines(b *testutil.LogBuffer, msg string) []map[string]any {
	s := b.String()
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	} else {
		return nil
	}
	var out []map[string]any
	for _, line := range strings.Split(s, "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

func startBinary(t *testing.T, o opts) *proc {
	t.Helper()
	p := &proc{stdout: &testutil.LogBuffer{}, stderr: &testutil.LogBuffer{}, exited: make(chan struct{})}
	if o.db == nil {
		o.db = dbtest.New(t)
	}
	if !o.noMigrate {
		o.db.Migrate(t)
	}
	p.db = o.db
	p.cfg = writeConfig(t, o.level)
	p.env = baseEnv(t, o.baseURL, append(append([]string{}, o.env...), o.db.Env(t)...))
	p.cmd = exec.Command(binary, "serve", "--config", p.cfg)
	p.cmd.Env = p.env
	p.cmd.Stdout, p.cmd.Stderr = p.stdout, p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		err := p.cmd.Wait()
		p.code = 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			p.code = ee.ExitCode()
		} else if err != nil {
			p.code = -1
		}
		close(p.exited)
	}()
	t.Cleanup(func() {
		select {
		case <-p.exited:
		default:
			_ = p.cmd.Process.Kill()
			<-p.exited
		}
	})
	testutil.Eventually(t, 10*time.Second, "the listening log line", func() bool {
		l := completeLines(p.stdout, "listening")
		if len(l) == 0 {
			return false
		}
		p.addr, _ = l[0]["address"].(string)
		return p.addr != ""
	})
	testutil.Eventually(t, 10*time.Second, "readiness", func() bool {
		resp, err := http.Get("http://" + p.addr + "/readyz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	})
	return p
}

// runBinary runs the binary with the given environment and arguments to completion, with a 20 s
// deadline, for commands that exit (migrate, migrate status, a serve that must fail).
func runBinary(t *testing.T, env []string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		t.Fatalf("pandokos %s did not exit within 20 s\nstdout: %s", strings.Join(args, " "), out.String())
	case errors.As(err, &ee):
		code = ee.ExitCode()
	case err != nil:
		t.Fatal(err)
	}
	return code, out.String(), errb.String()
}

// serveEnv is the environment of a serve process on database d, without a fake provider.
func serveEnv(t *testing.T, d *dbtest.Database, extra ...string) []string {
	t.Helper()
	return append(baseEnv(t, "http://127.0.0.1:1/v1", d.Env(t)), extra...)
}

// logLines decodes the JSON log lines of stdout whose msg matches.
func logLines(t *testing.T, stdout, msg string) []map[string]any {
	t.Helper()
	b := &testutil.LogBuffer{}
	_, _ = b.Write([]byte(stdout))
	return b.Find(t, msg)
}

func (p *proc) stop(t *testing.T) int {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.exited:
	case <-time.After(10 * time.Second):
		t.Fatal("the process did not exit within 10 s of SIGTERM")
	}
	return p.code
}

func (p *proc) url(path string) string { return "http://" + p.addr + path }

// exchange sends a request and returns the full text of the response (status line, headers, body).
func exchange(t *testing.T, method, url string, hdr http.Header, body []byte) (status int, text string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = hdr
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	var sb strings.Builder
	sb.WriteString(resp.Status + "\n")
	for k, v := range resp.Header {
		sb.WriteString(k + ": " + strings.Join(v, ",") + "\n")
	}
	sb.WriteString("\n" + string(b))
	return resp.StatusCode, sb.String()
}

func chatBody(model string, stream bool, prompt string) []byte {
	s := `{"model":"` + model + `","messages":[{"role":"user","content":"` + prompt + `"}]`
	if stream {
		s += `,"stream":true`
	}
	return []byte(s + "}")
}

func authHeader() http.Header {
	return http.Header{"Authorization": {"Bearer " + fixtures.CanaryAccessToken}, "Content-Type": {"application/json"}}
}

func TestServeStreamAndSigterm(t *testing.T) {
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", baseURL: up.BaseURL()})

	status, text := exchange(t, "POST", p.url("/openai/v1/chat/completions"), authHeader(), chatBody("fake-basic", false, fixtures.CanaryPrompt))
	if status != 200 || !strings.HasSuffix(text, string(fixtures.Read("chat_completion.json"))) {
		t.Fatalf("non-streaming: %d\n%s", status, text)
	}
	status, text = exchange(t, "POST", p.url("/openai/v1/chat/completions"), authHeader(), chatBody("fake-basic", true, fixtures.CanaryPrompt))
	if status != 200 || !strings.HasSuffix(text, "data: [DONE]\n\n") || strings.Contains(text, `"choices":[]`) {
		t.Fatalf("streaming: %d\n%s", status, text)
	}

	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	for _, line := range strings.Split(strings.TrimSpace(p.stdout.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("stdout line is not a JSON object: %q", line)
		}
	}
	if s := p.stderr.String(); s != "" {
		t.Fatalf("stderr is not empty: %q", s)
	}
	if len(completeLines(p.stdout, "shutdown complete")) != 1 {
		t.Error("shutdown complete not logged")
	}
}

func TestCanaries(t *testing.T) {
	for _, level := range []string{"info", "debug"} {
		t.Run(level, func(t *testing.T) {
			up := fakeprovider.New(t)
			responses := map[string]string{}
			var outputs []*testutil.LogBuffer

			p := startBinary(t, opts{level: level, baseURL: up.BaseURL(), env: []string{"PANDOKOS_LIMITS_MAX_REQUEST_BODY_BYTES=1024"}})
			call := func(label, method, path string, hdr http.Header, body []byte) {
				_, responses[label] = exchange(t, method, p.url(path), hdr, body)
			}
			call("1 non-streaming", "POST", "/openai/v1/chat/completions", authHeader(), chatBody("fake-basic", false, fixtures.CanaryPrompt))
			call("2 streaming", "POST", "/openai/v1/chat/completions", authHeader(), chatBody("fake-basic", true, fixtures.CanaryPrompt))
			call("3 no authorization", "POST", "/openai/v1/chat/completions", http.Header{"Content-Type": {"application/json"}}, chatBody("fake-basic", false, fixtures.CanaryPrompt))
			call("4 not found", "GET", "/nope?token="+fixtures.CanaryAccessToken, authHeader(), nil)
			call("5 too large", "POST", "/openai/v1/chat/completions", authHeader(), chatBody("fake-basic", false, strings.Repeat(fixtures.CanaryPrompt, 100)))
			call("6 provider error", "POST", "/openai/v1/chat/completions", authHeader(), chatBody("fake-error-500-echo", false, fixtures.CanaryPrompt))
			if code := p.stop(t); code != 0 {
				t.Fatalf("exit code %d", code)
			}
			outputs = append(outputs, p.stdout, p.stderr)

			unreachable := startBinary(t, opts{level: level, baseURL: "http://127.0.0.1:1/v1"})
			_, responses["7 unreachable"] = exchange(t, "POST", unreachable.url("/openai/v1/chat/completions"), authHeader(), chatBody("fake-basic", false, fixtures.CanaryPrompt))
			if code := unreachable.stop(t); code != 0 {
				t.Fatalf("exit code %d", code)
			}
			outputs = append(outputs, unreachable.stdout, unreachable.stderr)

			check := exec.Command(binary, "config", "check", "--config", p.cfg)
			check.Env = p.env
			var co, ce testutil.LogBuffer
			check.Stdout, check.Stderr = &co, &ce
			if err := check.Run(); err != nil {
				t.Fatalf("config check: %v\n%s", err, ce.String())
			}
			outputs = append(outputs, &co, &ce)

			for i, b := range outputs {
				testutil.AssertNoCanary(t, fmt.Sprintf("process output %d", i), b.String())
				testutil.AssertAbsent(t, fmt.Sprintf("process output %d", i), b.String(), append(p.db.Passwords(), unreachable.db.Passwords()...)...)
			}
			for label, text := range responses {
				if strings.Contains(text, fixtures.CanaryProviderKey) {
					t.Errorf("response %q contains the provider key", label)
				}
			}
			for _, label := range []string{"3 no authorization", "4 not found", "5 too large", "7 unreachable"} {
				testutil.AssertNoCanary(t, "response "+label, responses[label])
			}
			r6 := responses["6 provider error"]
			if !strings.Contains(r6, fixtures.CanaryPrompt) || !strings.Contains(r6, fixtures.CanaryCompletion) {
				t.Errorf("response 6 must carry the provider's body, which echoes the canaries:\n%s", r6)
			}
			for _, label := range []string{"3 no authorization", "5 too large", "7 unreachable"} {
				if !strings.Contains(responses[label], "X-Pandokos-Error") {
					t.Errorf("response %q lacks the error header", label)
				}
			}
		})
	}
}

func TestMigrateCommand(t *testing.T) { // T-160
	d := dbtest.New(t)
	// The default listener address warns that it serves plain HTTP; this test wants an empty stderr.
	env := append(d.Env(t), "PANDOKOS_LISTENERS_INFERENCE_ADDRESS=127.0.0.1:0")
	appliedLine := regexp.MustCompile(`^0001_init  applied \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)

	code, out, errs := runBinary(t, env, "migrate", "status")
	if code != 1 || errs != "" || out != "0001_init  pending\nschema is at version 0; this binary needs version 1\n" {
		t.Fatalf("status before: code %d, stdout %q, stderr %q", code, out, errs)
	}
	code, out, errs = runBinary(t, env, "migrate")
	if code != 0 || errs != "" || out != "applied 0001_init\nschema is at version 1\n" {
		t.Fatalf("migrate: code %d, stdout %q, stderr %q", code, out, errs)
	}
	code, out, errs = runBinary(t, env, "migrate")
	if code != 0 || errs != "" || out != "schema is at version 1\n" {
		t.Fatalf("second migrate: code %d, stdout %q, stderr %q", code, out, errs)
	}
	code, out, errs = runBinary(t, env, "migrate", "status")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || errs != "" || len(lines) != 2 || !appliedLine.MatchString(lines[0]) || lines[1] != "schema is at version 1; this binary needs version 1" {
		t.Fatalf("status after: code %d, stdout %q, stderr %q", code, out, errs)
	}
	testutil.AssertAbsent(t, "migrate output", out+errs, d.Passwords()...)
}

func TestServeRefusesUnmigratedDatabase(t *testing.T) { // T-161
	d := dbtest.New(t)
	start := time.Now()
	code, out, errs := runBinary(t, serveEnv(t, d), "serve", "--config", writeConfig(t, "info"))
	if code != 1 || time.Since(start) > 10*time.Second {
		t.Fatalf("code %d after %v\nstdout: %s\nstderr: %s", code, time.Since(start), out, errs)
	}
	failed := logLines(t, out, "startup failed")
	if len(failed) != 1 || !strings.Contains(fmt.Sprint(failed[0]["error"]), `run "pandokos migrate"`) {
		t.Fatalf("startup failed lines: %v\nstdout: %s", failed, out)
	}
	if len(logLines(t, out, "listening")) != 0 {
		t.Error("the process listened")
	}

	d.Migrate(t)
	if _, err := d.Super(t).Exec(context.Background(), "INSERT INTO pandokos.schema_migration (version, name, checksum, pandokos_version) VALUES (2, 'future', repeat('0', 64), 'x')"); err != nil {
		t.Fatal(err)
	}
	code, out, errs = runBinary(t, serveEnv(t, d), "serve", "--config", writeConfig(t, "info"))
	failed = logLines(t, out, "startup failed")
	if code != 1 || len(failed) != 1 || !strings.Contains(fmt.Sprint(failed[0]["error"]), "upgrade Pandokos") {
		t.Fatalf("with a newer schema: code %d, stdout %s, stderr %s", code, out, errs)
	}
	if len(logLines(t, out, "listening")) != 0 {
		t.Error("the process listened")
	}
}

func TestServeReadinessFollowsDatabase(t *testing.T) { // T-162
	d := dbtest.New(t)
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", baseURL: up.BaseURL(), db: d})
	status := func(path string) int {
		resp, err := http.Get(p.url(path))
		if err != nil {
			return -1
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if status("/readyz") != 200 {
		t.Fatal("not ready at the start")
	}
	d.SetAcceptingConnections(t, false)
	testutil.Eventually(t, 10*time.Second, "readiness to fail", func() bool { return status("/readyz") == 503 })
	if status("/healthz") != 200 {
		t.Error("liveness must not depend on the database")
	}
	found := false
	for _, l := range completeLines(p.stdout, "readiness check failed") {
		if c, _ := l["check"].(string); strings.HasPrefix(c, "database-") {
			found = true
		}
	}
	if !found {
		t.Errorf("no readiness check failed line for a database check: %s", p.stdout.String())
	}
	d.SetAcceptingConnections(t, true)
	testutil.Eventually(t, 15*time.Second, "readiness to recover", func() bool { return status("/readyz") == 200 })
	if status("/healthz") != 200 {
		t.Error("liveness failed")
	}
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

func TestServeBehindPooler(t *testing.T) { // T-163
	d := dbtest.New(t)
	pooled := d.Pooled(t)
	code, out, errs := runBinary(t, pooled.Env(t), "migrate")
	if code != 0 || !strings.Contains(out, "schema is at version 1") {
		t.Fatalf("migrate through the pooler: code %d, stdout %q, stderr %q", code, out, errs)
	}
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", baseURL: up.BaseURL(), db: pooled, noMigrate: true})
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

func TestServeRefusesPrivilegedUser(t *testing.T) { // T-164
	d := dbtest.NewMigrated(t)
	su := d.CreateLogin(t, "su", "SUPERUSER")
	env := serveEnv(t, d,
		"PANDOKOS_DATABASE_GATEWAY_USER="+su.User,
		"PANDOKOS_DATABASE_GATEWAY_PASSWORD_FILE="+testutil.WriteSecret(t, "su_password", su.Password.Reveal()),
	)
	code, out, errs := runBinary(t, env, "serve", "--config", writeConfig(t, "info"))
	failed := logLines(t, out, "startup failed")
	if code != 1 || len(failed) != 1 || !strings.Contains(fmt.Sprint(failed[0]["error"]), "is a superuser; a runtime role must not be") {
		t.Fatalf("code %d, stdout %s, stderr %s", code, out, errs)
	}
	if len(logLines(t, out, "listening")) != 0 {
		t.Error("the process listened")
	}
	testutil.AssertAbsent(t, "output", out+errs, su.Password.Reveal())
}

func TestDatabaseCanaries(t *testing.T) { // T-165
	d := dbtest.New(t)
	wrong := fmt.Sprintf("wrong-%d", time.Now().UnixNano()) // a string that is not a real password
	secrets := append(d.Passwords(), wrong)
	type output struct{ where, text string }
	var outputs []output
	collect := func(where, out, errs string) {
		outputs = append(outputs, output{where + " stdout", out}, output{where + " stderr", errs})
	}

	// (a) a wrong password.
	envWrong := serveEnv(t, d, "PANDOKOS_DATABASE_GATEWAY_PASSWORD_FILE="+testutil.WriteSecret(t, "wrong_password", wrong))
	d.Migrate(t)
	code, out, errs := runBinary(t, envWrong, "serve", "--config", writeConfig(t, "debug"))
	if code != 1 {
		t.Errorf("serve with a wrong password: code %d", code)
	}
	collect("serve wrong password", out, errs)

	// (b) an unreachable address.
	envDown := serveEnv(t, d, "PANDOKOS_DATABASE_ADDRESS=127.0.0.1:1")
	code, out, errs = runBinary(t, envDown, "serve", "--config", writeConfig(t, "debug"))
	if code != 1 {
		t.Errorf("serve with an unreachable database: code %d", code)
	}
	collect("serve unreachable", out, errs)
	code, out, errs = runBinary(t, append(d.Env(t), "PANDOKOS_DATABASE_ADDRESS=127.0.0.1:1"), "migrate")
	if code != 1 {
		t.Errorf("migrate with an unreachable database: code %d", code)
	}
	collect("migrate unreachable", out, errs)

	// (c) a normal run.
	code, out, errs = runBinary(t, d.Env(t), "migrate")
	if code != 0 {
		t.Errorf("migrate: code %d: %s", code, errs)
	}
	collect("migrate", out, errs)
	code, out, errs = runBinary(t, d.Env(t), "migrate", "status")
	if code != 0 {
		t.Errorf("migrate status: code %d: %s", code, errs)
	}
	collect("migrate status", out, errs)
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "debug", baseURL: up.BaseURL(), db: d, noMigrate: true})
	exchange(t, "POST", p.url("/openai/v1/chat/completions"), authHeader(), chatBody("fake-basic", false, fixtures.CanaryPrompt))
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	collect("serve", p.stdout.String(), p.stderr.String())

	for _, o := range outputs {
		testutil.AssertAbsent(t, o.where, o.text, secrets...)
		testutil.AssertNoCanary(t, o.where, o.text)
	}
}
