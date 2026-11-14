// Package e2e runs the built pandokos binary against the fake provider.
package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/otlptest"
	"github.com/pandokos/pandokos/test/testcert"
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
	up        *fakeprovider.Server // the provider of the tenants of the test (its URL is the endpoint of openai)
	endpoint  string               // another base URL for the endpoint of openai, when there is no fake (private)
	env       []string
	db        *dbtest.Database // nil: a new database of the test
	noMigrate bool             // true: the database is used as it is
	tls       bool             // both listeners serve TLS with a certificate of testcert

	// Sprint 3.2: the ledger and the traces.
	ledgerBuffer int                 // ledger.buffer_size and ledger.batch_size; 0 keeps the defaults
	flushTimeout string              // ledger.shutdown_flush_timeout, for example "2s"
	otlpEndpoint string              // telemetry.otlp.endpoint ("" exports nothing)
	otlpHeader   string              // the value of the header X-Collector-Key sent with every export
	otlpTimeout  string              // telemetry.otlp.timeout
	propagate    bool                // telemetry.traces.propagate
	collector    *otlptest.Collector // the collector at otlpEndpoint, when the test has one
}

type proc struct {
	cmd       *exec.Cmd
	stdout    *testutil.LogBuffer
	stderr    *testutil.LogBuffer
	exited    chan struct{}
	code      int
	addr      string // inference listener
	adminAddr string
	scheme    string // "http" or "https", the same for both listeners
	caFile    string // with TLS: the authority of the certificate
	client    *http.Client
	cfg       string
	env       []string
	db        *dbtest.Database

	platformFile string // the key file of the bootstrap key, once init ran
	tenantKey    string // an inference key of a tenant made by inferenceKey
	tenants      int

	col *otlptest.Collector // the collector the process exports to, if the test started one

	up      *fakeprovider.Server
	upURL   string // the base URL the endpoint of openai is pointed at
	upReady bool   // the endpoint and the models were set up
	credN   int
}

func baseEnv(t *testing.T, extra []string) []string {
	t.Helper()
	return append([]string{"PATH=" + os.Getenv("PATH")}, extra...)
}

// fakeModels are the models of the fake provider; each is created in the catalogue of the
// process as a model of provider openai available to every tenant.
var fakeModels = []string{
	fakeprovider.ModelBasic, fakeprovider.ModelDetails, fakeprovider.ModelGatedStream, fakeprovider.ModelHang,
	fakeprovider.ModelError429, fakeprovider.ModelError500Echo, fakeprovider.ModelUsageOnLastChunk,
	fakeprovider.ModelRedirect, fakeprovider.ModelEchoAuth, fakeprovider.ModelClose,
}

// useFakeProvider points the default endpoint of a provider at the fake provider, as a private
// endpoint (CLI endpoint update), with the key file of a platform key.
func (p *proc) useFakeProvider(t *testing.T, platformFile, provider string, up *fakeprovider.Server) {
	t.Helper()
	p.useEndpoint(t, platformFile, provider, up.URL())
}

// useEndpoint points the default endpoint of a provider at a base URL, as a private endpoint.
func (p *proc) useEndpoint(t *testing.T, platformFile, provider, baseURL string) {
	t.Helper()
	p.mustCLI(t, platformFile, "endpoint", "update", "--base-url", baseURL, "--private", provider, "default")
}

// addFakeModels creates the models of the fake in the catalogue (the seeded catalogue has none
// of these names).
func (p *proc) addFakeModels(t *testing.T, platformFile string) {
	t.Helper()
	for _, m := range fakeModels {
		p.mustCLI(t, platformFile, "model", "create", "--availability", "all", "openai/"+m)
	}
}

// addCredential registers a credential of a provider for the tenant of the administrator's key
// file, with the secret read from a file, and returns its ID.
func (p *proc) addCredential(t *testing.T, adminFile, provider, secretValue string) (id string) {
	t.Helper()
	p.credN++
	secretFile := testutil.WriteSecret(t, "provider-secret", secretValue)
	out := p.mustCLI(t, adminFile, "credential", "create", "--provider", provider, "--secret-file", secretFile, fmt.Sprintf("credential-%d", p.credN))
	m := reCredentialID.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no credential ID in the output of credential create:\n%s", out)
	}
	return m[1]
}

// prepareUpstream points openai at the fake of the process and creates its models, once.
func (p *proc) prepareUpstream(t *testing.T) {
	t.Helper()
	if p.upReady || p.upURL == "" {
		return
	}
	platform := p.bootstrap(t)
	p.useEndpoint(t, platform, "openai", p.upURL)
	p.addFakeModels(t, platform)
	if p.up != nil {
		p.up.Accept(fixtures.CanaryCredential)
	}
	p.upReady = true
}

// prepareTenant gives the tenant of an administrator a credential for openai equal to
// fixtures.CanaryCredential, after the endpoint and the models were set up.
func (p *proc) prepareTenant(t *testing.T, adminFile string) {
	t.Helper()
	p.prepareUpstream(t)
	p.addCredential(t, adminFile, "openai", fixtures.CanaryCredential)
}

func writeConfig(t *testing.T, level string) string {
	t.Helper()
	content := "log:\n  level: " + level + "\nlisteners:\n  inference:\n    address: 127.0.0.1:0\n  admin:\n    address: 127.0.0.1:0\nshutdown:\n  drain_timeout: 5s\n"
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

// sprint32Env is the configuration of the ledger and of the traces that the options ask for.
func (o opts) sprint32Env(t *testing.T) []string {
	t.Helper()
	var env []string
	if o.ledgerBuffer > 0 {
		env = append(env, fmt.Sprintf("PANDOKOS_LEDGER_BUFFER_SIZE=%d", o.ledgerBuffer), fmt.Sprintf("PANDOKOS_LEDGER_BATCH_SIZE=%d", o.ledgerBuffer))
	}
	if o.flushTimeout != "" {
		env = append(env, "PANDOKOS_LEDGER_SHUTDOWN_FLUSH_TIMEOUT="+o.flushTimeout)
	}
	if o.otlpEndpoint != "" {
		env = append(env, "PANDOKOS_TELEMETRY_OTLP_ENDPOINT="+o.otlpEndpoint)
	}
	if o.otlpHeader != "" {
		env = append(env, "PANDOKOS_TELEMETRY_OTLP_HEADERS_FILE="+testutil.WriteSecret(t, "otlp-headers", "X-Collector-Key: "+o.otlpHeader))
	}
	if o.otlpTimeout != "" {
		env = append(env, "PANDOKOS_TELEMETRY_OTLP_TIMEOUT="+o.otlpTimeout)
	}
	if o.propagate {
		env = append(env, "PANDOKOS_TELEMETRY_TRACES_PROPAGATE=true")
	}
	return env
}

// collector returns the collector the process exports to; the test started it and gave its URL
// to the process through opts.
func (p *proc) collector(t *testing.T) *otlptest.Collector {
	t.Helper()
	if p.col == nil {
		t.Fatal("the process has no collector")
	}
	return p.col
}

// usageRows reads the usage records of a tenant as the superuser of the test database, one JSON
// object per row (to_jsonb), in the order of the start.
func usageRows(t *testing.T, d *dbtest.Database, tenantID string) []map[string]any {
	t.Helper()
	conn := d.Super(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rows, err := conn.Query(ctx, `SELECT to_jsonb(u) FROM pandokos.usage_record u WHERE tenant_id = $1::uuid ORDER BY started_at, id`, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func startBinary(t *testing.T, o opts) *proc {
	t.Helper()
	p := &proc{stdout: &testutil.LogBuffer{}, stderr: &testutil.LogBuffer{}, exited: make(chan struct{}), scheme: "http", client: http.DefaultClient}
	if o.db == nil {
		o.db = dbtest.New(t)
	}
	if !o.noMigrate {
		o.db.Migrate(t)
	}
	p.db = o.db
	p.cfg = writeConfig(t, o.level)
	extra := append(o.db.Env(t), o.sprint32Env(t)...)
	extra = append(extra, o.env...) // the options of the test come last and win
	p.col = o.collector
	if o.tls {
		ca, cert, key := testcert.Files(t, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
		for _, l := range []string{"INFERENCE", "ADMIN"} {
			extra = append(extra, "PANDOKOS_LISTENERS_"+l+"_TLS_CERT_FILE="+cert, "PANDOKOS_LISTENERS_"+l+"_TLS_KEY_FILE="+key)
		}
		pem, err := os.ReadFile(ca)
		if err != nil {
			t.Fatal(err)
		}
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(pem)
		p.caFile, p.scheme = ca, "https"
		p.client = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}, ForceAttemptHTTP2: true}}
	}
	p.up = o.up
	switch {
	case o.endpoint != "":
		p.upURL = o.endpoint
	case o.up != nil:
		p.upURL = o.up.URL()
	}
	p.env = baseEnv(t, extra)
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
	testutil.Eventually(t, 10*time.Second, "the listening log lines", func() bool {
		for _, l := range completeLines(p.stdout, "listening") {
			addr, _ := l["address"].(string)
			switch l["listener"] {
			case "inference":
				p.addr = addr
			case "admin":
				p.adminAddr = addr
			}
		}
		return p.addr != "" && p.adminAddr != ""
	})
	for _, addr := range []string{p.addr, p.adminAddr} {
		testutil.Eventually(t, 10*time.Second, "readiness of "+addr, func() bool {
			resp, err := p.client.Get(p.scheme + "://" + addr + "/readyz")
			if err != nil {
				return false
			}
			resp.Body.Close()
			return resp.StatusCode == 200
		})
	}
	return p
}

// bootstrap runs "init --key-out" with the environment of the process and returns the key file.
// It does so once per process.
func (p *proc) bootstrap(t *testing.T) string {
	t.Helper()
	if p.platformFile != "" {
		return p.platformFile
	}
	file := filepath.Join(t.TempDir(), "bootstrap.key")
	code, out, errs := runBinary(t, p.env, "init", "--key-out", file)
	if code != 0 {
		t.Fatalf("init: exit %d\nstdout: %s\nstderr: %s", code, out, errs)
	}
	p.platformFile = file
	return file
}

// cli runs an API command of the binary against the admin listener of the process.
func (p *proc) cli(t *testing.T, keyFile string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	env := []string{"PANDOKOS_CLI_SERVER=" + p.scheme + "://" + p.adminAddr, "PANDOKOS_CLI_KEY_FILE=" + keyFile}
	if p.caFile != "" {
		env = append(env, "PANDOKOS_CLI_CA_FILE="+p.caFile)
	}
	return runBinary(t, env, args...)
}

func (p *proc) mustCLI(t *testing.T, keyFile string, args ...string) string {
	t.Helper()
	code, out, errs := p.cli(t, keyFile, args...)
	if code != 0 {
		t.Fatalf("%s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, out, errs)
	}
	return out
}

var reID = regexp.MustCompile(`(?m)^id: ([0-9a-f-]{36})$`)

// newTenant creates a tenant with the key file of a platform key and returns its ID and the key
// file of its administrator.
func (p *proc) newTenant(t *testing.T, platformFile, name string) (id, adminFile string) {
	t.Helper()
	adminFile = filepath.Join(t.TempDir(), name+".key")
	out := p.mustCLI(t, platformFile, "tenant", "create", "--admin-key-out", adminFile, name)
	m := reID.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no tenant ID in the output of tenant create:\n%s", out)
	}
	return m[1], adminFile
}

// newInferenceKey creates a service account with the role inference and a key for it, with the
// administrator of the tenant, and returns the key.
func (p *proc) newInferenceKey(t *testing.T, adminFile, tenantID string, keyFlags ...string) (keyID, key string) {
	t.Helper()
	p.mustCLI(t, adminFile, "service-account", "create", "--tenant", tenantID, "--role", "inference", fmt.Sprintf("inference-%d", time.Now().UnixNano()))
	list := p.mustCLI(t, adminFile, "service-account", "list", "--tenant", tenantID, "-o", "json")
	var sas struct {
		Items []struct {
			ID    string   `json:"id"`
			Roles []string `json:"roles"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(list), &sas); err != nil {
		t.Fatal(err)
	}
	var sa string
	for _, i := range sas.Items {
		for _, r := range i.Roles {
			if r == "inference" {
				sa = i.ID
			}
		}
	}
	if sa == "" {
		t.Fatalf("no inference service account in %s", list)
	}
	keyFile := filepath.Join(t.TempDir(), "inference.key")
	out := p.mustCLI(t, adminFile, append([]string{"key", "create", "--tenant", tenantID, "--service-account", sa, "--key-out", keyFile}, keyFlags...)...)
	b, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^key id: (t[a-z2-7]{16})$`).FindStringSubmatch(out)
	if m == nil {
		m = regexp.MustCompile(`(?m)^id: (t[a-z2-7]{16})$`).FindStringSubmatch(out)
	}
	if m != nil {
		keyID = m[1]
	}
	return keyID, strings.TrimSpace(string(b))
}

// inferenceKey returns a key that the inference listener accepts, made through the CLI.
func (p *proc) inferenceKey(t *testing.T) string {
	t.Helper()
	if p.tenantKey != "" {
		return p.tenantKey
	}
	platform := p.bootstrap(t)
	p.tenants++
	id, admin := p.newTenant(t, platform, fmt.Sprintf("tenant-%d", p.tenants))
	_, p.tenantKey = p.newInferenceKey(t, admin, id)
	p.prepareTenant(t, admin)
	return p.tenantKey
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
	return append(baseEnv(t, d.Env(t)), extra...)
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

func (p *proc) url(path string) string { return p.scheme + "://" + p.addr + path }

func (p *proc) adminURL(path string) string { return p.scheme + "://" + p.adminAddr + path }

// exchange sends a request and returns the full text of the response (status line, headers, body).
func exchange(t *testing.T, method, url string, hdr http.Header, body []byte) (status int, text string) {
	t.Helper()
	return exchangeWith(t, http.DefaultClient, method, url, hdr, body)
}

// exchangeWith is exchange with a client of the caller (for TLS).
func exchangeWith(t *testing.T, client *http.Client, method, url string, hdr http.Header, body []byte) (status int, text string) {
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
	resp, err := client.Do(req)
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

func authHeader(key string) http.Header {
	return http.Header{"Authorization": {"Bearer " + key}, "Content-Type": {"application/json"}}
}

func TestServeStreamAndSigterm(t *testing.T) {
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", up: up})
	key := p.inferenceKey(t)

	status, text := exchange(t, "POST", p.url("/openai/v1/chat/completions"), authHeader(key), chatBody("fake-basic", false, fixtures.CanaryPrompt))
	if status != 200 || !strings.HasSuffix(text, string(fixtures.Read("chat_completion.json"))) {
		t.Fatalf("non-streaming: %d\n%s", status, text)
	}
	status, text = exchange(t, "POST", p.url("/openai/v1/chat/completions"), authHeader(key), chatBody("fake-basic", true, fixtures.CanaryPrompt))
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

			p := startBinary(t, opts{level: level, up: up, env: []string{"PANDOKOS_LIMITS_MAX_REQUEST_BODY_BYTES=1024"}})
			key := p.inferenceKey(t)
			call := func(label, method, path string, hdr http.Header, body []byte) {
				_, responses[label] = exchange(t, method, p.url(path), hdr, body)
			}
			call("1 non-streaming", "POST", "/openai/v1/chat/completions", authHeader(key), chatBody("fake-basic", false, fixtures.CanaryPrompt))
			call("2 streaming", "POST", "/openai/v1/chat/completions", authHeader(key), chatBody("fake-basic", true, fixtures.CanaryPrompt))
			call("3 no authorization", "POST", "/openai/v1/chat/completions", http.Header{"Content-Type": {"application/json"}}, chatBody("fake-basic", false, fixtures.CanaryPrompt))
			call("4 not found", "GET", "/nope?token="+key, authHeader(key), nil)
			call("5 too large", "POST", "/openai/v1/chat/completions", authHeader(key), chatBody("fake-basic", false, strings.Repeat(fixtures.CanaryPrompt, 100)))
			call("6 provider error", "POST", "/openai/v1/chat/completions", authHeader(key), chatBody("fake-error-500-echo", false, fixtures.CanaryPrompt))
			if code := p.stop(t); code != 0 {
				t.Fatalf("exit code %d", code)
			}
			outputs = append(outputs, p.stdout, p.stderr)

			unreachable := startBinary(t, opts{level: level, endpoint: "http://127.0.0.1:1"})
			ukey := unreachable.inferenceKey(t)
			_, responses["7 unreachable"] = exchange(t, "POST", unreachable.url("/openai/v1/chat/completions"), authHeader(ukey), chatBody("fake-basic", false, fixtures.CanaryPrompt))
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

			secrets := append(append(p.db.Passwords(), unreachable.db.Passwords()...), testutil.KeyForms(key)...)
			secrets = append(secrets, testutil.KeyForms(ukey)...)
			for i, b := range outputs {
				testutil.AssertNoCanary(t, fmt.Sprintf("process output %d", i), b.String())
				testutil.AssertAbsent(t, fmt.Sprintf("process output %d", i), b.String(), secrets...)
			}
			for label, text := range responses {
				if strings.Contains(text, fixtures.CanaryProviderKey) {
					t.Errorf("response %q contains the provider key", label)
				}
				testutil.AssertAbsent(t, "response "+label, text, testutil.KeyForms(key)...)
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
	applied := func(name string) *regexp.Regexp {
		return regexp.MustCompile(`^` + name + `  applied \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)
	}

	code, out, errs := runBinary(t, env, "migrate", "status")
	if code != 1 || errs != "" || out != "0001_init  pending\n0002_identity_audit  pending\n0003_vault  pending\n0004_catalogue  pending\n0005_gateway_auth_failures  pending\n0006_usage_ledger  pending\nschema is at version 0; this binary needs version 6\n" {
		t.Fatalf("status before: code %d, stdout %q, stderr %q", code, out, errs)
	}
	code, out, errs = runBinary(t, env, "migrate")
	if code != 0 || errs != "" || out != "applied 0001_init\napplied 0002_identity_audit\napplied 0003_vault\napplied 0004_catalogue\napplied 0005_gateway_auth_failures\napplied 0006_usage_ledger\nschema is at version 6\n" {
		t.Fatalf("migrate: code %d, stdout %q, stderr %q", code, out, errs)
	}
	code, out, errs = runBinary(t, env, "migrate")
	if code != 0 || errs != "" || out != "schema is at version 6\n" {
		t.Fatalf("second migrate: code %d, stdout %q, stderr %q", code, out, errs)
	}
	code, out, errs = runBinary(t, env, "migrate", "status")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || errs != "" || len(lines) != 7 || !applied("0001_init").MatchString(lines[0]) || !applied("0002_identity_audit").MatchString(lines[1]) || !applied("0003_vault").MatchString(lines[2]) || !applied("0004_catalogue").MatchString(lines[3]) || !applied("0005_gateway_auth_failures").MatchString(lines[4]) || !applied("0006_usage_ledger").MatchString(lines[5]) || lines[6] != "schema is at version 6; this binary needs version 6" {
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
	if _, err := d.Super(t).Exec(context.Background(), "INSERT INTO pandokos.schema_migration (version, name, checksum, pandokos_version) VALUES (7, 'future', repeat('0', 64), 'x')"); err != nil {
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
	p := startBinary(t, opts{level: "info", up: up, db: d})
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
	if code != 0 || !strings.Contains(out, "schema is at version 6") {
		t.Fatalf("migrate through the pooler: code %d, stdout %q, stderr %q", code, out, errs)
	}
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", up: up, db: pooled, noMigrate: true})
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
	p := startBinary(t, opts{level: "debug", up: up, db: d, noMigrate: true})
	exchange(t, "POST", p.url("/openai/v1/chat/completions"), authHeader(p.inferenceKey(t)), chatBody("fake-basic", false, fixtures.CanaryPrompt))
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	collect("serve", p.stdout.String(), p.stderr.String())

	for _, o := range outputs {
		testutil.AssertAbsent(t, o.where, o.text, secrets...)
		if !strings.HasPrefix(o.where, "response") { // a response to a model call carries the provider's text
			testutil.AssertNoCanary(t, o.where, o.text)
		}
	}
}

const legacyAccessToken = "pandokos-canary-access-token-4e8b2d7c61f0a935" // the static token of sprint 1.1

var reKeyLine = regexp.MustCompile(`(?m)^key: +(pdk_[tp][a-z2-7]{16}_[a-z2-7]{59})$`)

func tableCount(t *testing.T, d *dbtest.Database, table string) int {
	t.Helper()
	var n int
	if err := d.Super(t).QueryRow(context.Background(), "SELECT count(*) FROM pandokos."+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestServeWithTLS is T-244.
func TestServeWithTLS(t *testing.T) {
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "debug", up: up, tls: true})
	platform := p.bootstrap(t)
	if code, out, errs := p.cli(t, platform, "whoami"); code != 0 || !strings.Contains(out, "principal_type: platform") {
		t.Fatalf("whoami over TLS: %d %s %s", code, out, errs)
	}
	// Without the authority the CLI refuses.
	env := []string{"PANDOKOS_CLI_SERVER=https://" + p.adminAddr, "PANDOKOS_CLI_KEY_FILE=" + platform}
	if code, _, _ := runBinary(t, env, "whoami"); code != 1 {
		t.Errorf("the CLI without the authority: exit %d", code)
	}
	key := p.inferenceKey(t)
	status, text := exchangeWith(t, p.client, "POST", p.url("/openai/v1/chat/completions"), authHeader(key), chatBody("fake-basic", false, fixtures.CanaryPrompt))
	if status != 200 {
		t.Fatalf("inference over TLS: %d\n%s", status, text)
	}
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	b, _ := os.ReadFile(platform)
	for i, out := range []*testutil.LogBuffer{p.stdout, p.stderr} {
		testutil.AssertAbsent(t, fmt.Sprintf("process output %d", i), out.String(), testutil.KeyForms(strings.TrimSpace(string(b)))...)
		testutil.AssertAbsent(t, "process output", out.String(), testutil.KeyForms(key)...)
	}
	for _, l := range completeLines(p.stdout, "listening") {
		if l["tls"] != true {
			t.Errorf("listening line: %v", l)
		}
	}
}

// TestDatabaseTLS is T-245.
func TestDatabaseTLS(t *testing.T) {
	ca := dbtest.CAFile(t)
	d := dbtest.New(t)
	if d.Host != "127.0.0.1" || d.Port != 55432 {
		t.Skipf("the TLS test needs the direct test PostgreSQL at 127.0.0.1:55432, not %s:%d", d.Host, d.Port)
	}
	tlsEnv := func(extra ...string) []string {
		return append(append(d.Env(t), "PANDOKOS_DATABASE_TLS_MODE=verify-full", "PANDOKOS_DATABASE_TLS_CA_FILE="+ca), extra...)
	}
	if code, out, errs := runBinary(t, tlsEnv(), "migrate"); code != 0 {
		t.Fatalf("migrate: %d %s %s", code, out, errs)
	}
	keyFile := filepath.Join(t.TempDir(), "k")
	if code, out, errs := runBinary(t, tlsEnv(), "init", "--key-out", keyFile); code != 0 {
		t.Fatalf("init: %d %s %s", code, out, errs)
	}
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", up: up, db: d, noMigrate: true, env: tlsEnv()[len(d.Env(t)):]})
	var ssl int
	if err := d.Super(t).QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_ssl s JOIN pg_stat_activity a ON a.pid = s.pid WHERE a.datname = $1 AND s.ssl AND a.application_name LIKE 'pandokos-%'`, d.Name).Scan(&ssl); err != nil || ssl < 3 {
		t.Errorf("encrypted Pandokos connections: %d %v", ssl, err)
	}
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit %d", code)
	}
	for name, env := range map[string][]string{
		"another authority": append(d.Env(t), "PANDOKOS_DATABASE_TLS_MODE=verify-full", "PANDOKOS_DATABASE_TLS_CA_FILE="+testutil.WriteCA(t)),
		"host name":         tlsEnv("PANDOKOS_DATABASE_ADDRESS=localhost:55432"),
	} {
		code, out, errs := runBinary(t, serveEnv(t, d, env[len(d.Env(t)):]...), "serve", "--config", writeConfig(t, "info"))
		failed := logLines(t, out, "startup failed")
		if code != 1 || len(failed) != 1 || !strings.Contains(fmt.Sprint(failed[0]["error"]), "cannot connect") || len(logLines(t, out, "listening")) != 0 {
			t.Errorf("%s: exit %d\n%s\n%s", name, code, out, errs)
		}
	}
}

// TestNoDefaultCredentials is T-250.
func TestNoDefaultCredentials(t *testing.T) {
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", up: up})
	for _, table := range []string{"virtual_key", "platform_key", "virtual_key_directory"} {
		if n := tableCount(t, p.db, table); n != 0 {
			t.Errorf("%s has %d rows after migrate", table, n)
		}
	}
	headers := []http.Header{
		{}, {"Authorization": {"Bearer "}}, {"Authorization": {"Bearer " + legacyAccessToken}},
		{"Authorization": {"Bearer " + fixtures.CanaryProviderKey}}, {"Authorization": {"Bearer " + fixtures.CanaryPrompt}},
	}
	for _, h := range headers {
		h.Set("Content-Type", "application/json")
		if status, text := exchange(t, "POST", p.url("/openai/v1/chat/completions"), h, chatBody("fake-basic", false, "x")); status != 401 {
			t.Errorf("inference with %v: %d\n%s", h, status, text)
		}
		for _, op := range adminapi.Operations {
			c := apitest.CallFor(t, op, apitest.Params{Tenant: "0192f0f0-0000-7000-8000-000000000001", ServiceAccount: "0192f0f0-0000-7000-8000-000000000002", VirtualKey: "tabcdefghijklmnop", PlatformKey: "pabcdefghijklmnop", Credential: "0192f0f0-0000-7000-8000-000000000003",
				Provider: "synthetic-missing", Endpoint: "ep-missing", Model: "0192f0f0-0000-7000-8000-000000000004", PriceVersion: "0192f0f0-0000-7000-8000-000000000005", PriceImport: "0192f0f0-0000-7000-8000-000000000006"})
			var body []byte
			if c.Body != nil {
				body, _ = json.Marshal(c.Body)
			}
			if status, text := exchange(t, c.Method, p.adminURL(c.Path), h.Clone(), body); status != 401 {
				t.Errorf("%s with %v: %d\n%s", op.ID, h, status, text)
			}
		}
	}
	if n := len(up.Requests()); n != 0 {
		t.Errorf("the provider saw %d requests", n)
	}
}

// TestInit is T-251.
func TestInit(t *testing.T) {
	d := dbtest.NewMigrated(t)
	env := baseEnv(t, d.Env(t))
	code, out, errs := runBinary(t, env, "init")
	if code != 0 {
		t.Fatalf("init: %d %s %s", code, out, errs)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 || lines[0] != "bootstrap platform key created" || !regexp.MustCompile(`^key id:  p[a-z2-7]{16}$`).MatchString(lines[1]) ||
		!regexp.MustCompile(`^expires: \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`).MatchString(lines[2]) || !reKeyLine.MatchString(lines[3]) {
		t.Fatalf("init output:\n%s", out)
	}
	if !strings.Contains(errs, `warning: this is a bootstrap key: create a platform key with "pandokos platform-key create", then revoke this one`) {
		t.Errorf("stderr: %q", errs)
	}
	key := reKeyLine.FindStringSubmatch(out)[1]
	exp, _ := time.Parse(time.RFC3339, strings.TrimPrefix(lines[2], "expires: "))
	if d := time.Until(exp); d < 23*time.Hour || d > 24*time.Hour+time.Minute {
		t.Errorf("expires in %v, want about 24h", d)
	}
	// The key authenticates as platform-admin with bootstrap: true.
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", up: up, db: d, noMigrate: true})
	_, text := exchange(t, "GET", p.adminURL("/v1/me"), authHeader(key), nil)
	if !strings.Contains(text, `"bootstrap":true`) || !strings.Contains(text, "platform-admin") {
		t.Errorf("me: %s", text)
	}
	var action, actorType, actorName, detail string
	if err := d.Super(t).QueryRow(context.Background(), "SELECT action, actor_type, actor_name, detail::text FROM pandokos.platform_audit_event").Scan(&action, &actorType, &actorName, &detail); err != nil ||
		action != "platform_key.bootstrap" || actorType != "host" || actorName != "pandokos init" || !strings.Contains(detail, `"recover": false`) {
		t.Errorf("audit: %v %s %s %s %s", err, action, actorType, actorName, detail)
	}
	// A second init is refused and creates nothing; --recover creates one.
	code, out, errs = runBinary(t, env, "init")
	if code != 1 || out != "" || !strings.Contains(errs, `error: a usable platform key exists; create further keys with it, or run "pandokos init --recover" if every platform key is lost`) || tableCount(t, d, "platform_key") != 1 {
		t.Errorf("second init: %d %q %q", code, out, errs)
	}
	if code, _, errs := runBinary(t, env, "init", "--recover"); code != 0 || tableCount(t, d, "platform_key") != 2 {
		t.Errorf("--recover: %d %s", code, errs)
	}
	var recovered int
	if err := d.Super(t).QueryRow(context.Background(), `SELECT count(*) FROM pandokos.platform_audit_event WHERE detail @> '{"recover": true}'`).Scan(&recovered); err != nil || recovered != 1 {
		t.Errorf("recover audit rows: %d %v", recovered, err)
	}
	// --key-out writes a 0600 file and prints no key.
	file := filepath.Join(t.TempDir(), "out.key")
	code, out, _ = runBinary(t, env, "init", "--recover", "--key-out", file)
	st, err := os.Stat(file)
	b, _ := os.ReadFile(file)
	if code != 0 || err != nil || st.Mode().Perm() != 0o600 || strings.Contains(out, strings.TrimSpace(string(b))) || !strings.Contains(out, "key written to "+file) {
		t.Errorf("--key-out: %d %v %q", code, err, out)
	}
	// An existing out-file is an error that creates nothing.
	before := tableCount(t, d, "platform_key")
	if code, _, _ := runBinary(t, env, "init", "--recover", "--key-out", file); code != 1 || tableCount(t, d, "platform_key") != before {
		t.Errorf("an existing out-file: %d", code)
	}
	// A key past its expiry is refused.
	expired, keyID, hash, _ := authn.Generate(authn.KindPlatform)
	if _, err := d.Super(t).Exec(context.Background(), "INSERT INTO pandokos.platform_key (key_id, secret_hash, expires_at) VALUES ($1, $2, now() - interval '1 second')", keyID, hash[:]); err != nil {
		t.Fatal(err)
	}
	if status, _ := exchange(t, "GET", p.adminURL("/v1/me"), authHeader(expired.Reveal()), nil); status != 401 {
		t.Errorf("an expired key: %d", status)
	}
	// Four concurrent runs on a fresh database create one key.
	d2 := dbtest.NewMigrated(t)
	env2 := baseEnv(t, d2.Env(t))
	codes := make(chan int, 4)
	for i := 0; i < 4; i++ {
		go func() { c, _, _ := runBinary(t, env2, "init"); codes <- c }()
	}
	ok, refused := 0, 0
	for i := 0; i < 4; i++ {
		if <-codes == 0 {
			ok++
		} else {
			refused++
		}
	}
	if ok != 1 || refused != 3 || tableCount(t, d2, "platform_key") != 1 {
		t.Errorf("concurrent init: %d succeeded, %d refused, %d keys", ok, refused, tableCount(t, d2, "platform_key"))
	}
}

// TestBootstrapWarnings is T-252.
func TestBootstrapWarnings(t *testing.T) {
	d := dbtest.NewMigrated(t)
	up := fakeprovider.New(t)
	env := baseEnv(t, d.Env(t))
	file := filepath.Join(t.TempDir(), "boot.key")
	if code, out, errs := runBinary(t, env, "init", "--key-out", file); code != 0 {
		t.Fatalf("init: %d %s %s", code, out, errs)
	}
	p := startBinary(t, opts{level: "info", up: up, db: d, noMigrate: true})
	p.platformFile = file
	testutil.Eventually(t, 5*time.Second, "the bootstrap warning", func() bool { return len(completeLines(p.stdout, "bootstrap key active")) >= 1 })
	l := completeLines(p.stdout, "bootstrap key active")[0]
	if l["level"] != "WARN" || l["key_id"] == nil || l["expires_at"] == nil {
		t.Errorf("warning: %v", l)
	}
	b, _ := os.ReadFile(file)
	req, _ := http.NewRequest("GET", p.adminURL("/v1/me"), nil)
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(b)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-Pandokos-Warning") != "bootstrap-key-active" {
		t.Errorf("headers: %v", resp.Header)
	}
	// Create a platform key, revoke the bootstrap key; a restart does not warn.
	second := filepath.Join(t.TempDir(), "second.key")
	p.mustCLI(t, file, "platform-key", "create", "--key-out", second)
	out := p.mustCLI(t, second, "platform-key", "list", "-o", "json")
	var list struct {
		Items []struct {
			ID        string `json:"id"`
			Bootstrap bool   `json:"bootstrap"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatal(err)
	}
	for _, k := range list.Items {
		if k.Bootstrap {
			p.mustCLI(t, second, "platform-key", "revoke", k.ID)
		}
	}
	if code := p.stop(t); code != 0 {
		t.Fatalf("exit %d", code)
	}
	p2 := startBinary(t, opts{level: "info", up: up, db: d, noMigrate: true})
	time.Sleep(500 * time.Millisecond)
	if n := len(completeLines(p2.stdout, "bootstrap key active")); n != 0 {
		t.Errorf("the restart warned %d times", n)
	}
}

func auditActions(t *testing.T, d *dbtest.Database, query string, args ...any) []string {
	t.Helper()
	rows, err := d.Super(t).Query(context.Background(), query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

// TestExitScenario is T-290.
func TestExitScenario(t *testing.T) {
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", up: up})
	boot := p.bootstrap(t)
	opsKey := filepath.Join(t.TempDir(), "ops.key")
	p.mustCLI(t, boot, "platform-key", "create", "--key-out", opsKey)
	type tn struct{ id, admin, key string }
	var ts []tn
	for _, name := range []string{"alpha", "beta"} {
		id, admin := p.newTenant(t, opsKey, name)
		_, key := p.newInferenceKey(t, admin, id)
		p.prepareTenant(t, admin)
		ts = append(ts, tn{id, admin, key})
		status, text := exchange(t, "POST", p.url("/openai/v1/chat/completions"), authHeader(key), chatBody("fake-basic", false, fixtures.CanaryPrompt))
		if status != 200 {
			t.Fatalf("inference for %s: %d\n%s", name, status, text)
		}
	}
	// Tenant alpha cannot address beta, through the CLI and through the API.
	code, _, errs := p.cli(t, ts[0].admin, "service-account", "list", "--tenant", ts[1].id)
	if code != 1 || !strings.Contains(errs, "not_found") {
		t.Errorf("CLI across tenants: %d %q", code, errs)
	}
	b, _ := os.ReadFile(ts[0].admin)
	status, text := exchange(t, "GET", p.adminURL("/v1/tenants/"+ts[1].id+"/virtual-keys"), authHeader(strings.TrimSpace(string(b))), nil)
	if status != 404 || !strings.Contains(text, "not_found") {
		t.Errorf("API across tenants: %d %s", status, text)
	}
	// Every step is in the audit logs, in order.
	got := auditActions(t, p.db, "SELECT action FROM pandokos.platform_audit_event ORDER BY occurred_at, action")
	want := "platform_key.bootstrap,platform_key.create,tenant.create,endpoint.update," + strings.Repeat("model.create,", len(fakeModels)) + "tenant.create"
	if strings.Join(got, ",") != want {
		t.Errorf("platform log: %v, want %s", got, want)
	}
	for _, x := range ts {
		got := auditActions(t, p.db, "SELECT action FROM pandokos.audit_event WHERE tenant_id = $1::uuid ORDER BY occurred_at, action", x.id)
		if want := "data_key.create,service_account.create,virtual_key.create,service_account.create,virtual_key.create,credential.create"; strings.Join(got, ",") != want {
			t.Errorf("tenant log: %v, want %s", got, want)
		}
	}
}

// TestRevocationAndSuspensionEndToEnd is T-291.
func TestRevocationAndSuspensionEndToEnd(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(t *testing.T) (*dbtest.Database, []string)
		ttl      string
		deadline time.Duration
		warns    bool
	}{
		{"direct", func(t *testing.T) (*dbtest.Database, []string) { return dbtest.NewMigrated(t), nil }, "60s", 2 * time.Second, false},
		{"pooler with direct address", func(t *testing.T) (*dbtest.Database, []string) {
			d := dbtest.New(t)
			pooled := d.Pooled(t)
			d.Migrate(t)
			return pooled, []string{"PANDOKOS_DATABASE_DIRECT_ADDRESS=" + net.JoinHostPort(d.Host, fmt.Sprint(d.Port))}
		}, "60s", 2 * time.Second, false},
		{"pooler without direct address", func(t *testing.T) (*dbtest.Database, []string) {
			d := dbtest.New(t)
			pooled := d.Pooled(t)
			d.Migrate(t)
			return pooled, nil
		}, "2s", 2500 * time.Millisecond, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, extra := c.setup(t)
			up := fakeprovider.New(t)
			p := startBinary(t, opts{level: "info", up: up, db: d, noMigrate: true, env: append([]string{"PANDOKOS_CACHE_PRINCIPAL_TTL=" + c.ttl}, extra...)})
			platform := p.bootstrap(t)
			id, admin := p.newTenant(t, platform, "cached")
			keyID1, key1 := p.newInferenceKey(t, admin, id)
			_, key2 := p.newInferenceKey(t, admin, id)
			p.prepareTenant(t, admin)
			call := func(key string) (int, string) {
				return exchange(t, "POST", p.url("/openai/v1/chat/completions"), authHeader(key), chatBody("fake-basic", false, "x"))
			}
			for _, k := range []string{key1, key2} {
				if s, _ := call(k); s != 200 {
					t.Fatalf("a good key: %d", s)
				}
			}
			p.mustCLI(t, admin, "key", "revoke", "--tenant", id, keyID1)
			testutil.Eventually(t, c.deadline+500*time.Millisecond, "the revoked key to be refused", func() bool { s, _ := call(key1); return s == 401 })
			p.mustCLI(t, platform, "tenant", "suspend", id)
			testutil.Eventually(t, c.deadline+500*time.Millisecond, "the suspended tenant to be refused", func() bool {
				s, text := call(key2)
				return s == 403 && strings.Contains(text, "tenant_suspended")
			})
			p.mustCLI(t, platform, "tenant", "reactivate", id)
			testutil.Eventually(t, c.deadline+500*time.Millisecond, "the reactivated tenant to work", func() bool { s, _ := call(key2); return s == 200 })
			if c.warns {
				testutil.Eventually(t, 20*time.Second, "the warning that notifications do not arrive", func() bool {
					return len(completeLines(p.stdout, "cache invalidation unavailable")) >= 1
				})
			} else if n := len(completeLines(p.stdout, "cache invalidation unavailable")); n != 0 {
				t.Errorf("an unexpected warning: %d", n)
			}
		})
	}
}

// TestKeyCanaries is T-292.
func TestKeyCanaries(t *testing.T) {
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "debug", up: up})
	var keys []string
	type output struct{ where, text, creates string }
	var outs []output
	run := func(keyFile string, args ...string) (int, string) {
		code, out, errs := p.cli(t, keyFile, args...)
		creates := ""
		if m := reKeyLine.FindStringSubmatch(out); m != nil {
			creates = m[1]
			if strings.Count(out, creates) != 1 {
				t.Errorf("%v: the key appears %d times in its own stdout", args, strings.Count(out, creates))
			}
			keys = append(keys, creates)
		}
		outs = append(outs, output{strings.Join(args, " ") + " stdout", out, creates}, output{strings.Join(args, " ") + " stderr", errs, ""})
		return code, out
	}
	bootOut := func() string {
		code, out, errs := runBinary(t, p.env, "init")
		if code != 0 {
			t.Fatalf("init: %s", errs)
		}
		m := reKeyLine.FindStringSubmatch(out)
		keys = append(keys, m[1])
		outs = append(outs, output{"init stdout", out, m[1]}, output{"init stderr", errs, ""})
		return m[1]
	}()
	boot := filepath.Join(t.TempDir(), "boot.key")
	if err := os.WriteFile(boot, []byte(bootOut+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.platformFile = boot
	type tn struct{ id, adminFile, key string }
	var ts []tn
	for _, name := range []string{"one", "two"} {
		_, out := run(boot, "tenant", "create", name)
		id := reID.FindStringSubmatch(out)[1]
		adminKey := reKeyLine.FindStringSubmatch(out)[1]
		af := filepath.Join(t.TempDir(), name+".key")
		if err := os.WriteFile(af, []byte(adminKey+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		run(af, "service-account", "create", "--tenant", id, "--role", "inference", "caller")
		_, kout := run(af, "key", "create", "--tenant", id, "--service-account", "caller")
		p.prepareTenant(t, af)
		ts = append(ts, tn{id, af, reKeyLine.FindStringSubmatch(kout)[1]})
	}
	var responses []string
	record := func(_ int, text string) { responses = append(responses, text) }
	for _, x := range ts {
		s, text := exchange(t, "POST", p.url("/openai/v1/chat/completions"), authHeader(x.key), chatBody("fake-basic", false, "x"))
		if s != 200 {
			t.Fatalf("inference: %d", s)
		}
		record(s, text)
	}
	// Wrong, malformed and revoked keys.
	wrong, _, _, _ := authn.Generate(authn.KindTenant)
	for _, k := range []string{wrong.Reveal(), "pdk_malformed", ""} {
		record(exchange(t, "POST", p.url("/openai/v1/chat/completions"), authHeader(k), chatBody("fake-basic", false, "x")))
		record(exchange(t, "GET", p.adminURL("/v1/me"), authHeader(k), nil))
	}
	keys = append(keys, wrong.Reveal())
	run(ts[0].adminFile, "key", "list", "--tenant", ts[0].id, "-o", "json")
	// A forced 500.
	if _, err := p.db.Super(t).Exec(context.Background(), "REVOKE SELECT ON pandokos.tenant FROM pandokos_admin"); err != nil {
		t.Fatal(err)
	}
	record(exchange(t, "GET", p.adminURL("/v1/tenants"), authHeader(bootOut), nil))
	if _, err := p.db.Super(t).Exec(context.Background(), "GRANT SELECT ON pandokos.tenant TO pandokos_admin"); err != nil {
		t.Fatal(err)
	}
	// A CLI failure.
	if code, _ := run(ts[0].adminFile, "tenant", "get", "nobody"); code != 1 {
		t.Errorf("tenant get nobody: %d", code)
	}
	// Revoke a key and use it.
	run(ts[1].adminFile, "key", "list", "--tenant", ts[1].id)
	if code, _ := run(ts[1].adminFile, "key", "revoke", "--tenant", ts[1].id, ts[1].key[4:21]); code != 0 {
		t.Errorf("revoke: %d", code)
	}
	record(exchange(t, "GET", p.adminURL("/v1/me"), authHeader(ts[1].key), nil))

	if code := p.stop(t); code != 0 {
		t.Fatalf("exit %d", code)
	}
	outs = append(outs, output{"server stdout", p.stdout.String(), ""}, output{"server stderr", p.stderr.String(), ""})
	for _, table := range []string{"audit_event", "platform_audit_event"} {
		var dump string
		if err := p.db.Super(t).QueryRow(context.Background(), "SELECT COALESCE(jsonb_agg(to_jsonb(t))::text, '') FROM pandokos."+table+" t").Scan(&dump); err != nil {
			t.Fatal(err)
		}
		outs = append(outs, output{table, dump, ""})
	}
	for i, r := range responses {
		outs = append(outs, output{fmt.Sprintf("response %d", i), r, ""})
	}
	for _, k := range keys {
		for _, o := range outs {
			if o.creates == k {
				continue
			}
			testutil.AssertAbsent(t, o.where, o.text, testutil.KeyForms(k)...)
		}
	}
	for _, o := range outs {
		testutil.AssertAbsent(t, o.where, o.text, p.db.Passwords()...)
		if !strings.HasPrefix(o.where, "response") { // a response to a model call carries the provider's text
			testutil.AssertNoCanary(t, o.where, o.text)
		}
	}
}
