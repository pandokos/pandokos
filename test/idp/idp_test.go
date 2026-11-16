//go:build idp

package idp

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/jwks"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// Audiences of the providers' clients (test/idp/up.sh and keycloak-realm.json.tmpl).
const (
	keycloakAudience = "https://pandokos.test/llm"
	otherAudience    = "https://other.test"
	hydraAudience    = "https://pandokos.test/t1"
	// authentikAudience is the client ID of the provider (blueprint authentik-blueprint.yaml): Authentik
	// puts it in aud as a string and offers no other value.
	authentikAudience = "pandokos-tests"
	composeProject    = "pandokos-testidp"
	chatPath          = "/openai/v1/chat/completions"
)

var (
	binary string
	caFile string
	env    = map[string]string{}
	reID   = regexp.MustCompile(`(?m)^id: ([0-9a-f-]{36})$`)
)

// variables that local/idp/env (written by make idp-up) must provide.
var required = []string{
	"PANDOKOS_TEST_IDP_CA_FILE",
	"PANDOKOS_TEST_IDP_KEYCLOAK_ISSUER", "PANDOKOS_TEST_IDP_KEYCLOAK_TOKEN_URL",
	"PANDOKOS_TEST_IDP_HYDRA_ISSUER", "PANDOKOS_TEST_IDP_HYDRA_TOKEN_URL",
	"PANDOKOS_TEST_IDP_KEYCLOAK_CLIENT_A", "PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_A",
	"PANDOKOS_TEST_IDP_KEYCLOAK_CLIENT_B", "PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_B",
	"PANDOKOS_TEST_IDP_KEYCLOAK_CLIENT_C", "PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_C",
	"PANDOKOS_TEST_IDP_KEYCLOAK_CLIENT_OTHER", "PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_OTHER",
	"PANDOKOS_TEST_IDP_HYDRA_CLIENT_TENANT", "PANDOKOS_TEST_IDP_HYDRA_SECRET_TENANT",
	"PANDOKOS_TEST_IDP_HYDRA_CLIENT_UNMAPPED", "PANDOKOS_TEST_IDP_HYDRA_SECRET_UNMAPPED",
	"PANDOKOS_TEST_IDP_AUTHENTIK_ISSUER", "PANDOKOS_TEST_IDP_AUTHENTIK_TOKEN_URL",
	"PANDOKOS_TEST_IDP_AUTHENTIK_CLIENT_ID", "PANDOKOS_TEST_IDP_AUTHENTIK_CLIENT_SECRET",
	"PANDOKOS_TEST_IDP_AUTHENTIK_USER_A", "PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_A",
	"PANDOKOS_TEST_IDP_AUTHENTIK_USER_B", "PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_B",
	"PANDOKOS_TEST_IDP_AUTHENTIK_USER_C", "PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_C",
	"PANDOKOS_TEST_IDP_AUTHENTIK_USER_UNMAPPED", "PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_UNMAPPED",
}

// TestMain fails (never skips) when the providers' settings or the database are missing, then builds
// the binary the tests run.
func TestMain(m *testing.M) {
	os.Exit(func() int {
		for _, k := range required {
			if env[k] = os.Getenv(k); env[k] == "" {
				fmt.Fprintf(os.Stderr, "idp: %s is not set: run \"make idp-up\" and \"make test-idp\" (tests behind the tag idp never skip)\n", k)
				return 1
			}
		}
		if os.Getenv(dbtest.EnvDatabaseURL) == "" || os.Getenv(dbtest.EnvNoDatabase) != "" {
			fmt.Fprintf(os.Stderr, "idp: %s is not set or %s is set: run \"make db-up\" (tests behind the tag idp never skip)\n", dbtest.EnvDatabaseURL, dbtest.EnvNoDatabase)
			return 1
		}
		caFile = env["PANDOKOS_TEST_IDP_CA_FILE"]
		dir, err := os.MkdirTemp("", "pandokos-idp-")
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

// providerClient is an HTTP client that trusts the authority of the providers' certificate.
func providerClient(t *testing.T) *http.Client {
	t.Helper()
	pem, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatalf("%s holds no certificate", caFile)
	}
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
}

// issue obtains an access token by the client-credentials grant, with the client secret in the body.
// An empty audience sends none. It returns the status and the token ("" on a refusal).
func issue(t *testing.T, tokenURL, id, secret, audience string) (int, string) {
	t.Helper()
	status, tok, err := tryIssue(t, tokenURL, id, secret, audience)
	if err != nil {
		t.Fatalf("token endpoint %s: %v", tokenURL, err)
	}
	return status, tok
}

// tryIssue is issue that returns a transport error instead of failing the test.
func tryIssue(t *testing.T, tokenURL, id, secret, audience string) (int, string, error) {
	t.Helper()
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {id}, "client_secret": {secret}}
	if audience != "" {
		form.Set("audience", audience)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := providerClient(t).Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, "", err
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(body, &out)
	return resp.StatusCode, out.AccessToken, nil
}

// mustIssue is issue that fails the test unless a token is issued.
func mustIssue(t *testing.T, tokenURL, id, secret, audience string) string {
	t.Helper()
	status, tok := issue(t, tokenURL, id, secret, audience)
	if status != 200 || tok == "" {
		t.Fatalf("client %s did not get a token (status %d)", id, status)
	}
	return tok
}

func keycloakToken(t *testing.T, client, secret string) string {
	t.Helper()
	return mustIssue(t, env["PANDOKOS_TEST_IDP_KEYCLOAK_TOKEN_URL"], env[client], env[secret], "")
}

func hydraToken(t *testing.T, client, secret, audience string) string {
	t.Helper()
	return mustIssue(t, env["PANDOKOS_TEST_IDP_HYDRA_TOKEN_URL"], env[client], env[secret], audience)
}

// authentikToken obtains an access token of a service account of Authentik: the client-credentials
// grant with the client of the provider and the account's user name and app password. The scope
// pandokos asks for the claim pandokos_org (a scope mapping applies only to a requested scope).
func authentikToken(t *testing.T, user, password string) string {
	t.Helper()
	return authentikTokenScope(t, user, password, "pandokos")
}

// authentikTokenScope is authentikToken with the given scope ("" asks for none).
func authentikTokenScope(t *testing.T, user, password, scope string) string {
	t.Helper()
	form := url.Values{
		"grant_type": {"client_credentials"}, "client_id": {env["PANDOKOS_TEST_IDP_AUTHENTIK_CLIENT_ID"]},
		"client_secret": {env["PANDOKOS_TEST_IDP_AUTHENTIK_CLIENT_SECRET"]},
		"username":      {env[user]}, "password": {env[password]},
	}
	if scope != "" {
		form.Set("scope", scope)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, env["PANDOKOS_TEST_IDP_AUTHENTIK_TOKEN_URL"], strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := providerClient(t).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil || resp.StatusCode != 200 || out.AccessToken == "" {
		t.Fatalf("service account %s did not get a token (status %d)", env[user], resp.StatusCode)
	}
	return out.AccessToken
}

// segments decodes the header and the payload of a token, for assertions about what a provider
// issued. A token is never printed.
func segments(t *testing.T, token string) (header, payload map[string]any) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("a token with %d segments", len(parts))
	}
	out := make([]map[string]any, 2)
	for i := range out {
		b, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out[0], out[1]
}

// gw is the binary, serving on loopback, with tokens enabled for hosts named localhost.
type gw struct {
	t      *testing.T
	d      *dbtest.Database
	up     *fakeprovider.Server
	cmd    *exec.Cmd
	stdout *testutil.LogBuffer
	exited chan struct{}
	env    []string
	addr   string
	admin  string
	boot   string
}

// start runs a process on database d (a new migrated one when nil) and the fake provider up (a new
// one when nil). The process trusts the authority of the providers for the keys of the issuers.
func start(t *testing.T, d *dbtest.Database, up *fakeprovider.Server, extra ...string) *gw {
	t.Helper()
	if d == nil {
		d = dbtest.NewMigrated(t)
	}
	if up == nil {
		up = fakeprovider.New(t)
	}
	cfg := filepath.Join(t.TempDir(), "pandokos.yaml")
	content := "log:\n  level: info\nlisteners:\n  inference:\n    address: 127.0.0.1:0\n  admin:\n    address: 127.0.0.1:0\nshutdown:\n  drain_timeout: 5s\n"
	if err := os.WriteFile(cfg, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	e := append([]string{"PATH=" + os.Getenv("PATH")}, d.Env(t)...)
	e = append(e, "PANDOKOS_TOKENS_PRIVATE_HOSTS=localhost", "PANDOKOS_UPSTREAM_TLS_CA_FILE="+caFile)
	e = append(e, extra...)
	g := &gw{t: t, d: d, up: up, stdout: &testutil.LogBuffer{}, exited: make(chan struct{}), env: e}
	g.cmd = exec.Command(binary, "serve", "--config", cfg)
	g.cmd.Env = e
	g.cmd.Stdout, g.cmd.Stderr = g.stdout, &testutil.LogBuffer{}
	if err := g.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = g.cmd.Wait(); close(g.exited) }()
	t.Cleanup(func() {
		select {
		case <-g.exited:
		default:
			_ = g.cmd.Process.Kill()
			<-g.exited
		}
	})
	testutil.Eventually(t, 10*time.Second, "the listening log lines", func() bool {
		for _, l := range g.stdout.Find(t, "listening") {
			a, _ := l["address"].(string)
			switch l["listener"] {
			case "inference":
				g.addr = a
			case "admin":
				g.admin = a
			}
		}
		return g.addr != "" && g.admin != ""
	})
	for _, a := range []string{g.addr, g.admin} {
		testutil.Eventually(t, 10*time.Second, "readiness of "+a, func() bool {
			resp, err := http.Get("http://" + a + "/readyz") //nolint:noctx // a loopback probe of the process under test
			if err != nil {
				return false
			}
			resp.Body.Close()
			return resp.StatusCode == 200
		})
	}
	return g
}

// run runs the binary to completion with a 20 s deadline.
func run(t *testing.T, env []string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) || ctx.Err() != nil {
			t.Fatalf("pandokos %s: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), err, out.String(), errb.String())
		}
		t.Fatal(err)
	}
	return out.String()
}

// cli runs a command of the CLI against the admin listener with a key file.
func (g *gw) cli(keyFile string, args ...string) string {
	g.t.Helper()
	return run(g.t, []string{"PANDOKOS_CLI_SERVER=http://" + g.admin, "PANDOKOS_CLI_KEY_FILE=" + keyFile}, args...)
}

func (g *gw) id(out string) string {
	g.t.Helper()
	m := reID.FindStringSubmatch(out)
	if m == nil {
		g.t.Fatalf("no ID in the output:\n%s", out)
	}
	return m[1]
}

// bootstrap makes the platform key once.
func (g *gw) bootstrap() string {
	g.t.Helper()
	if g.boot == "" {
		file := filepath.Join(g.t.TempDir(), "bootstrap.key")
		run(g.t, g.env, "init", "--key-out", file)
		g.boot = file
	}
	return g.boot
}

// upstream points openai at the fake provider and creates the model, once.
func (g *gw) upstream() {
	g.t.Helper()
	b := g.bootstrap()
	g.cli(b, "endpoint", "update", "--base-url", g.up.URL(), "--private", "openai", "default")
	g.cli(b, "model", "create", "--availability", "all", "openai/"+fakeprovider.ModelBasic)
	g.up.Accept(fixtures.CanaryCredential)
}

// tenant is a tenant with a credential for openai and a service account with the role inference.
type tenant struct{ id, admin, sa string }

func (g *gw) tenant(name string) tenant {
	g.t.Helper()
	admin := filepath.Join(g.t.TempDir(), name+".key")
	id := g.id(g.cli(g.bootstrap(), "tenant", "create", "--admin-key-out", admin, name))
	secret := testutil.WriteSecret(g.t, "provider-secret", fixtures.CanaryCredential)
	g.cli(admin, "credential", "create", "--provider", "openai", "--secret-file", secret, "credential")
	sa := g.id(g.cli(admin, "service-account", "create", "--tenant", id, "--role", "inference", "workload"))
	return tenant{id, admin, sa}
}

// call sends a chat completion with a bearer token and returns the status.
func (g *gw) call(token string) int {
	g.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	body := `{"model":"` + fakeprovider.ModelBasic + `","messages":[{"role":"user","content":"hello"}]}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+g.addr+chatPath, strings.NewReader(body))
	if err != nil {
		g.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		g.t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// usage reads the usage records of a tenant, one JSON object per row.
func (g *gw) usage(tenantID string) []map[string]any {
	g.t.Helper()
	conn := g.d.Super(g.t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rows, err := conn.Query(ctx, `SELECT to_jsonb(u) FROM pandokos.usage_record u WHERE tenant_id = $1::uuid ORDER BY started_at, id`, tenantID)
	if err != nil {
		g.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			g.t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			g.t.Fatal(err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		g.t.Fatal(err)
	}
	return out
}

// waitUsage waits until the tenant has n usage records and returns them.
func (g *gw) waitUsage(tenantID string, n int) []map[string]any {
	g.t.Helper()
	var rows []map[string]any
	testutil.Eventually(g.t, 15*time.Second, fmt.Sprintf("%d usage rows of the tenant", n), func() bool {
		rows = g.usage(tenantID)
		return len(rows) >= n
	})
	if len(rows) != n {
		g.t.Fatalf("%d usage rows, want %d: %v", len(rows), n, rows)
	}
	return rows
}

// wantToken checks that a usage row records a token call of the tenant's service account.
func wantToken(t *testing.T, row map[string]any, tn tenant) {
	t.Helper()
	if row["principal_kind"] != "token" || row["principal_id"] != tn.sa || row["tenant_id"] != tn.id || row["outcome"] != "ok" {
		t.Errorf("usage row: %v", row)
	}
}

// keycloakPlatform sets up the platform-level registration of the Keycloak realm: the claim
// pandokos_org designates the tenant, and the subject is the client ID (azp), because the sub of a
// service account is an identifier Keycloak generates. Tenant A lists org-a, tenant B org-b; tenant C
// has a mapping for its client but is not listed (the mapping can only be written while it is).
func keycloakPlatform(g *gw) (a, b, c tenant) {
	t := g.t
	t.Helper()
	g.upstream()
	boot := g.bootstrap()
	a, b, c = g.tenant("kc-a"), g.tenant("kc-b"), g.tenant("kc-c")
	reg := g.id(g.cli(boot, "platform-issuer", "create", "--name", "keycloak", "--issuer", env["PANDOKOS_TEST_IDP_KEYCLOAK_ISSUER"],
		"--audience", keycloakAudience, "--tenant-claim", "pandokos_org", "--subject-claim", "azp"))
	for _, x := range []struct {
		tn      tenant
		claim   string
		subject string
	}{{a, "org-a", "workload-a"}, {b, "org-b", "workload-b"}, {c, "org-c-listed", "workload-c"}} {
		g.cli(boot, "platform-issuer", "add-tenant", "--claim-value", x.claim, reg, x.tn.id)
		g.cli(x.tn.admin, "subject-mapping", "create", "--tenant", x.tn.id, "--issuer", reg, "--platform", "--subject", x.subject, "--service-account", x.tn.sa)
	}
	g.cli(boot, "platform-issuer", "remove-tenant", reg, c.id)
	return a, b, c
}

// TestKeycloakPlatformIssuer is T-602: a Keycloak token whose claim designates tenant A calls a model
// and the usage row is A's; a token whose claim designates a tenant outside the list is refused; a token
// of the client with another audience is refused.
func TestKeycloakPlatformIssuer(t *testing.T) {
	g := start(t, nil, nil)
	a, b, c := keycloakPlatform(g)

	tokA := keycloakToken(t, "PANDOKOS_TEST_IDP_KEYCLOAK_CLIENT_A", "PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_A")
	tokB := keycloakToken(t, "PANDOKOS_TEST_IDP_KEYCLOAK_CLIENT_B", "PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_B")
	tokC := keycloakToken(t, "PANDOKOS_TEST_IDP_KEYCLOAK_CLIENT_C", "PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_C")
	tokOther := keycloakToken(t, "PANDOKOS_TEST_IDP_KEYCLOAK_CLIENT_OTHER", "PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_OTHER")

	// What the realm issued is what the design relies on.
	hdr, pl := segments(t, tokA)
	if hdr["alg"] != "RS256" || pl["azp"] != "workload-a" || pl["pandokos_org"] != "org-a" || pl["aud"] != keycloakAudience || pl["iss"] != env["PANDOKOS_TEST_IDP_KEYCLOAK_ISSUER"] {
		t.Fatalf("the token of workload-a is not what the realm was set up to issue: header %v, claims %v", hdr, pl)
	}

	if s := g.call(tokA); s != 200 {
		t.Fatalf("the token of A: %d", s)
	}
	wantToken(t, g.waitUsage(a.id, 1)[0], a)
	if s := g.call(tokB); s != 200 {
		t.Fatalf("the token of B: %d", s)
	}
	wantToken(t, g.waitUsage(b.id, 1)[0], b)

	// The claim of C designates a tenant outside the list: refused, although C maps the subject.
	if s := g.call(tokC); s != 401 {
		t.Errorf("a claim outside the list: %d, want 401", s)
	}
	// The same client with another audience: the audience is not registered.
	if s := g.call(tokOther); s != 401 {
		t.Errorf("another audience: %d, want 401", s)
	}
	// The token of A without the scope that carries the claim: no tenant, refused.
	noScope := authentikTokenScope(t, "PANDOKOS_TEST_IDP_AUTHENTIK_USER_A", "PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_A", "")
	if _, pl := segments(t, noScope); pl["pandokos_org"] != nil {
		t.Fatalf("a token without the scope carries the claim: %v", pl)
	}
	if s := g.call(noScope); s != 401 {
		t.Errorf("a token without the claim: %d, want 401", s)
	}
	if n := len(g.usage(c.id)); n != 0 {
		t.Errorf("tenant C has %d usage rows", n)
	}
	if n := len(g.usage(a.id)); n != 1 {
		t.Errorf("tenant A has %d usage rows after the refusals, want 1", n)
	}
}

// TestHydraTenantIssuer is T-603: a Hydra client-credentials token with the registered audience acts for
// the tenant through the mapping of its client ID; another audience and an unmapped client are refused.
func TestHydraTenantIssuer(t *testing.T) {
	g := start(t, nil, nil)
	g.upstream()
	tn := g.tenant("hydra")
	other := g.tenant("hydra-other") // a second tenant that must see nothing
	reg := g.id(g.cli(tn.admin, "token-issuer", "create", "--tenant", tn.id, "--name", "hydra", "--issuer", env["PANDOKOS_TEST_IDP_HYDRA_ISSUER"], "--audience", hydraAudience))
	g.cli(tn.admin, "subject-mapping", "create", "--tenant", tn.id, "--issuer", reg, "--subject", env["PANDOKOS_TEST_IDP_HYDRA_CLIENT_TENANT"], "--service-account", tn.sa)

	tok := hydraToken(t, "PANDOKOS_TEST_IDP_HYDRA_CLIENT_TENANT", "PANDOKOS_TEST_IDP_HYDRA_SECRET_TENANT", hydraAudience)
	hdr, pl := segments(t, tok)
	if hdr["alg"] != "RS256" || pl["sub"] != env["PANDOKOS_TEST_IDP_HYDRA_CLIENT_TENANT"] || pl["iss"] != env["PANDOKOS_TEST_IDP_HYDRA_ISSUER"] {
		t.Fatalf("the Hydra token is not what the provider was set up to issue: header %v, claims %v", hdr, pl)
	}
	if aud, _ := pl["aud"].([]any); len(aud) != 1 || aud[0] != hydraAudience {
		t.Fatalf("aud of the Hydra token: %v", pl["aud"])
	}
	if s := g.call(tok); s != 200 {
		t.Fatalf("the token with the registered audience: %d", s)
	}
	wantToken(t, g.waitUsage(tn.id, 1)[0], tn)

	// Another audience (one the client may request) and no audience at all (aud is an empty array).
	if s := g.call(hydraToken(t, "PANDOKOS_TEST_IDP_HYDRA_CLIENT_TENANT", "PANDOKOS_TEST_IDP_HYDRA_SECRET_TENANT", otherAudience)); s != 401 {
		t.Errorf("another audience: %d, want 401", s)
	}
	if s := g.call(hydraToken(t, "PANDOKOS_TEST_IDP_HYDRA_CLIENT_TENANT", "PANDOKOS_TEST_IDP_HYDRA_SECRET_TENANT", "")); s != 401 {
		t.Errorf("no audience: %d, want 401", s)
	}
	// An audience the client may not request is refused by the provider itself.
	if s, _ := issue(t, env["PANDOKOS_TEST_IDP_HYDRA_TOKEN_URL"], env["PANDOKOS_TEST_IDP_HYDRA_CLIENT_TENANT"], env["PANDOKOS_TEST_IDP_HYDRA_SECRET_TENANT"], "https://pandokos.test/t2"); s == 200 {
		t.Error("Hydra issued a token for an audience the client may not request")
	}
	// A client the registration does not map, with the right audience.
	if s := g.call(hydraToken(t, "PANDOKOS_TEST_IDP_HYDRA_CLIENT_UNMAPPED", "PANDOKOS_TEST_IDP_HYDRA_SECRET_UNMAPPED", hydraAudience)); s != 401 {
		t.Errorf("an unmapped client: %d, want 401", s)
	}
	g.waitUsage(tn.id, 1)
	if n := len(g.usage(other.id)); n != 0 {
		t.Errorf("the other tenant has %d usage rows", n)
	}
}

// authentikTenant sets up a tenant-bound registration of the Authentik provider: the audience is the
// client ID, and the subject (the user name of the service account) of workload A maps to the
// tenant's service account.
func authentikTenant(g *gw, name, audience string) (tenant, string) {
	g.t.Helper()
	g.upstream()
	tn := g.tenant(name)
	reg := g.id(g.cli(tn.admin, "token-issuer", "create", "--tenant", tn.id, "--name", "authentik", "--issuer", env["PANDOKOS_TEST_IDP_AUTHENTIK_ISSUER"], "--audience", audience))
	g.cli(tn.admin, "subject-mapping", "create", "--tenant", tn.id, "--issuer", reg, "--subject", env["PANDOKOS_TEST_IDP_AUTHENTIK_USER_A"], "--service-account", tn.sa)
	return tn, reg
}

// TestAuthentikTenantIssuer is T-606: an Authentik client-credentials token of a service account
// acts for the tenant through the mapping of its subject and the usage row is the service account's;
// an unmapped service account is refused; the same token is refused by a registration with another
// audience (Authentik issues no other audience than the client ID, so the other audience is the
// registration's).
func TestAuthentikTenantIssuer(t *testing.T) {
	g := start(t, nil, nil)
	tn, _ := authentikTenant(g, "authentik", authentikAudience)
	other := g.tenant("authentik-other") // a second tenant that must see nothing

	tok := authentikToken(t, "PANDOKOS_TEST_IDP_AUTHENTIK_USER_A", "PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_A")
	hdr, pl := segments(t, tok)
	if hdr["alg"] != "RS256" || pl["sub"] != env["PANDOKOS_TEST_IDP_AUTHENTIK_USER_A"] || pl["aud"] != authentikAudience || pl["iss"] != env["PANDOKOS_TEST_IDP_AUTHENTIK_ISSUER"] {
		t.Fatalf("the Authentik token is not what the provider was set up to issue: header %v, claims %v", hdr, pl)
	}
	if s := g.call(tok); s != 200 {
		t.Fatalf("the token of a mapped service account: %d", s)
	}
	wantToken(t, g.waitUsage(tn.id, 1)[0], tn)

	// A service account the registration does not map, with the right audience.
	if s := g.call(authentikToken(t, "PANDOKOS_TEST_IDP_AUTHENTIK_USER_UNMAPPED", "PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_UNMAPPED")); s != 401 {
		t.Errorf("an unmapped subject: %d, want 401", s)
	}
	g.waitUsage(tn.id, 1)
	if n := len(g.usage(other.id)); n != 0 {
		t.Errorf("the other tenant has %d usage rows", n)
	}

	// The same token against a registration of the same issuer with another audience.
	g2 := start(t, nil, nil)
	tn2, _ := authentikTenant(g2, "authentik-audience", otherAudience)
	if s := g2.call(tok); s != 401 {
		t.Errorf("another audience: %d, want 401", s)
	}
	if n := len(g2.usage(tn2.id)); n != 0 {
		t.Errorf("the tenant with the other audience has %d usage rows", n)
	}
}

// authentikPlatform sets up the platform-level registration of the Authentik provider: the claim
// pandokos_org (from the attribute of the service account, through a scope mapping) designates the
// tenant and the subject is sub, the user name. Tenant A lists org-a, B org-b; C has a mapping but
// its claim value is not listed (the mapping can only be written while it is).
func authentikPlatform(g *gw) (a, b, c tenant) {
	t := g.t
	t.Helper()
	g.upstream()
	boot := g.bootstrap()
	a, b, c = g.tenant("ak-a"), g.tenant("ak-b"), g.tenant("ak-c")
	reg := g.id(g.cli(boot, "platform-issuer", "create", "--name", "authentik", "--issuer", env["PANDOKOS_TEST_IDP_AUTHENTIK_ISSUER"],
		"--audience", authentikAudience, "--tenant-claim", "pandokos_org", "--subject-claim", "sub"))
	for _, x := range []struct {
		tn      tenant
		claim   string
		subject string
	}{{a, "org-a", "PANDOKOS_TEST_IDP_AUTHENTIK_USER_A"}, {b, "org-b", "PANDOKOS_TEST_IDP_AUTHENTIK_USER_B"}, {c, "org-c-listed", "PANDOKOS_TEST_IDP_AUTHENTIK_USER_C"}} {
		g.cli(boot, "platform-issuer", "add-tenant", "--claim-value", x.claim, reg, x.tn.id)
		g.cli(x.tn.admin, "subject-mapping", "create", "--tenant", x.tn.id, "--issuer", reg, "--platform", "--subject", env[x.subject], "--service-account", x.tn.sa)
	}
	g.cli(boot, "platform-issuer", "remove-tenant", reg, c.id)
	return a, b, c
}

// TestAuthentikPlatformIssuer is T-607: an Authentik token whose claim designates tenant A calls a
// model and the usage row is A's; the claim designates the tenant, not the subject; a token whose
// claim designates a tenant outside the list is refused; a token without the claim (the scope that
// carries it not requested) is refused.
func TestAuthentikPlatformIssuer(t *testing.T) {
	g := start(t, nil, nil)
	a, b, c := authentikPlatform(g)

	tokA := authentikToken(t, "PANDOKOS_TEST_IDP_AUTHENTIK_USER_A", "PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_A")
	tokB := authentikToken(t, "PANDOKOS_TEST_IDP_AUTHENTIK_USER_B", "PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_B")
	tokC := authentikToken(t, "PANDOKOS_TEST_IDP_AUTHENTIK_USER_C", "PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_C")
	if _, pl := segments(t, tokA); pl["pandokos_org"] != "org-a" || pl["sub"] != env["PANDOKOS_TEST_IDP_AUTHENTIK_USER_A"] {
		t.Fatalf("the token of workload-a is not what the provider was set up to issue: claims %v", pl)
	}

	if s := g.call(tokA); s != 200 {
		t.Fatalf("the token of A: %d", s)
	}
	wantToken(t, g.waitUsage(a.id, 1)[0], a)
	if s := g.call(tokB); s != 200 {
		t.Fatalf("the token of B: %d", s)
	}
	wantToken(t, g.waitUsage(b.id, 1)[0], b)

	// The claim of C designates a tenant outside the list: refused, although C maps the subject.
	if s := g.call(tokC); s != 401 {
		t.Errorf("a claim outside the list: %d, want 401", s)
	}
	// The unmapped account carries the claim of A, but no mapping: the claim alone acts for nobody.
	if s := g.call(authentikToken(t, "PANDOKOS_TEST_IDP_AUTHENTIK_USER_UNMAPPED", "PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_UNMAPPED")); s != 401 {
		t.Errorf("an unmapped subject with the claim of A: %d, want 401", s)
	}
	// The token of A without the scope that carries the claim: no tenant, refused.
	noScope := authentikTokenScope(t, "PANDOKOS_TEST_IDP_AUTHENTIK_USER_A", "PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_A", "")
	if _, pl := segments(t, noScope); pl["pandokos_org"] != nil {
		t.Fatalf("a token without the scope carries the claim: %v", pl)
	}
	if s := g.call(noScope); s != 401 {
		t.Errorf("a token without the claim: %d, want 401", s)
	}
	if n := len(g.usage(c.id)); n != 0 {
		t.Errorf("tenant C has %d usage rows", n)
	}
	if n := len(g.usage(a.id)); n != 1 {
		t.Errorf("tenant A has %d usage rows after the refusals, want 1", n)
	}
}

// TestAuthentikTokenShape is T-608: the token and the key set of Authentik at the pinned version
// are what ADR 0047 records (RS256, aud a string, sub the user name, 300 s, no nbf, only signing
// keys), because the registration defaults depend on it.
func TestAuthentikTokenShape(t *testing.T) {
	tok := authentikToken(t, "PANDOKOS_TEST_IDP_AUTHENTIK_USER_A", "PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_A")
	hdr, pl := segments(t, tok)
	if hdr["alg"] != "RS256" || hdr["typ"] != "JWT" || hdr["kid"] == "" {
		t.Errorf("header %v", hdr)
	}
	iat, _ := pl["iat"].(float64)
	exp, _ := pl["exp"].(float64)
	if _, ok := pl["nbf"]; ok || exp-iat != 300 || pl["azp"] != authentikAudience {
		t.Errorf("claims %v: want no nbf, a life of 300 s and azp the client ID", pl)
	}
	// The key set at the discovery's jwks_uri (Authentik's own path), every key for signing.
	keys := fetchJWKS(t, env["PANDOKOS_TEST_IDP_AUTHENTIK_ISSUER"]+"jwks/")
	found := false
	for _, k := range keys {
		if k["use"] != "sig" {
			t.Errorf("a key with use %v", k["use"])
		}
		found = found || k["kid"] == hdr["kid"]
	}
	if !found {
		t.Errorf("the key %v of the token is not in the key set of %d keys", hdr["kid"], len(keys))
	}
}

// compose runs docker compose on the project of up.sh, which needs no file to stop and start a service.
func compose(t *testing.T, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", append([]string{"compose", "-p", composeProject}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// TestProviderOutage is T-604: Keycloak is stopped; a token issued before keeps working; Keycloak
// restarts with the same realm keys (the container is stopped, not removed) and new tokens work.
func TestProviderOutage(t *testing.T) {
	g := start(t, nil, nil, "PANDOKOS_TOKENS_JWKS_MIN_REFETCH_INTERVAL=1s")
	a, _, _ := keycloakPlatform(g)

	before := keycloakToken(t, "PANDOKOS_TEST_IDP_KEYCLOAK_CLIENT_A", "PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_A")
	hdrBefore, _ := segments(t, before)
	if s := g.call(before); s != 200 {
		t.Fatalf("a warm call: %d", s)
	}

	compose(t, "stop", "keycloak")
	t.Cleanup(func() { // the other tests need it, even when this one fails
		compose(t, "start", "keycloak")
		waitKeycloak(t)
	})
	for i := 0; i < 5; i++ {
		if s := g.call(before); s != 200 {
			t.Fatalf("call %d with the provider stopped: %d", i, s)
		}
	}
	// A process that never had the keys cannot verify anything: 503, never 401, and nothing forwarded.
	second := start(t, g.d, g.up, "PANDOKOS_TOKENS_JWKS_MIN_REFETCH_INTERVAL=1s")
	forwarded := len(g.up.Requests())
	for i := 0; i < 3; i++ {
		if s := second.call(before); s != 503 {
			t.Errorf("a process without keys while the provider is down: %d, want 503", s)
		}
	}
	if n := len(g.up.Requests()) - forwarded; n != 0 {
		t.Errorf("%d requests were forwarded although the token could not be verified", n)
	}

	compose(t, "start", "keycloak")
	waitKeycloak(t)
	after := keycloakToken(t, "PANDOKOS_TEST_IDP_KEYCLOAK_CLIENT_A", "PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_A")
	if hdrAfter, _ := segments(t, after); hdrAfter["kid"] != hdrBefore["kid"] {
		t.Errorf("the realm signs with another key after the restart: %v then %v", hdrBefore["kid"], hdrAfter["kid"])
	}
	if s := g.call(after); s != 200 {
		t.Errorf("a new token, process with keys: %d", s)
	}
	testutil.Eventually(t, 10*time.Second, "the process without keys to recover", func() bool { return second.call(after) == 200 })
	if n := len(g.waitUsageAtLeast(a.id, 8)); n < 8 {
		t.Errorf("%d usage rows", n)
	}
}

// waitKeycloak waits until Keycloak issues tokens (it needs seconds to start).
func waitKeycloak(t *testing.T) {
	t.Helper()
	testutil.Eventually(t, 2*time.Minute, "Keycloak to issue tokens", func() bool {
		status, _, err := tryIssue(t, env["PANDOKOS_TEST_IDP_KEYCLOAK_TOKEN_URL"], env["PANDOKOS_TEST_IDP_KEYCLOAK_CLIENT_A"], env["PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_A"], "")
		return err == nil && status == 200
	})
}

// waitUsageAtLeast waits for n rows or more.
func (g *gw) waitUsageAtLeast(tenantID string, n int) []map[string]any {
	g.t.Helper()
	var rows []map[string]any
	testutil.Eventually(g.t, 15*time.Second, "the usage rows of the tenant", func() bool {
		rows = g.usage(tenantID)
		return len(rows) >= n
	})
	return rows
}

// TestProviderDocuments is T-605: the discovery document and the key set of each provider at the
// pinned version pass internal/jwks unchanged (algorithms, use, key sizes), and the default algorithms
// of a registration accept each provider's default tokens.
func TestProviderDocuments(t *testing.T) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	cache, err := jwks.New(jwks.Options{
		Transport: func(bool) http.RoundTripper {
			return &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
		},
		PrivateHosts: []string{"localhost"},
		FetchTimeout: 5 * time.Second, RefreshInterval: 15 * time.Minute, MinRefetchInterval: time.Second, MaxStale: 6 * time.Hour,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	// The default algorithms of a registration (RS256 and ES256, section 5.3).
	defaults := map[string]bool{"RS256": true, "ES256": true}

	cases := []struct {
		name, issuer string
		token        string
	}{
		{"keycloak", env["PANDOKOS_TEST_IDP_KEYCLOAK_ISSUER"], keycloakToken(t, "PANDOKOS_TEST_IDP_KEYCLOAK_CLIENT_A", "PANDOKOS_TEST_IDP_KEYCLOAK_SECRET_A")},
		{"hydra", env["PANDOKOS_TEST_IDP_HYDRA_ISSUER"], hydraToken(t, "PANDOKOS_TEST_IDP_HYDRA_CLIENT_TENANT", "PANDOKOS_TEST_IDP_HYDRA_SECRET_TENANT", hydraAudience)},
		{"authentik", env["PANDOKOS_TEST_IDP_AUTHENTIK_ISSUER"], authentikToken(t, "PANDOKOS_TEST_IDP_AUTHENTIK_USER_A", "PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_A")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hdr, pl := segments(t, c.token)
			alg, _ := hdr["alg"].(string)
			kid, _ := hdr["kid"].(string)
			if !defaults[alg] {
				t.Errorf("the default token is signed with %q: not accepted by the default algorithms", alg)
			}
			if kid == "" {
				t.Fatal("the token has no kid")
			}
			if pl["iss"] != c.issuer {
				t.Errorf("iss %v is not the issuer URL %s: the registration would never match", pl["iss"], c.issuer)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			// Discovery is used (no explicit jwks_uri): the document is read as it is.
			key, err := cache.Key(ctx, jwks.Source{Issuer: c.issuer}, kid, alg)
			if err != nil {
				t.Fatalf("the key of the token was not found through discovery: %v", err)
			}
			if rk, ok := key.(*rsa.PublicKey); !ok || rk.N.BitLen() < 2048 {
				t.Errorf("key %T of %v bits", key, bitLen(key))
			}
			// A key ID the set does not have is a refusal, not an outage.
			if _, err := cache.Key(ctx, jwks.Source{Issuer: c.issuer}, "no-such-key", alg); !errors.Is(err, jwks.ErrKeyNotFound) {
				t.Errorf("an unknown kid: %v", err)
			}
		})
	}

	t.Run("keycloak keys for encryption are dropped", func(t *testing.T) {
		keys := fetchJWKS(t, env["PANDOKOS_TEST_IDP_KEYCLOAK_ISSUER"]+"/protocol/openid-connect/certs")
		enc := 0
		for _, k := range keys {
			if k["use"] != "enc" {
				continue
			}
			enc++
			kid, _ := k["kid"].(string)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if _, err := cache.Key(ctx, jwks.Source{Issuer: env["PANDOKOS_TEST_IDP_KEYCLOAK_ISSUER"]}, kid, "RS256"); !errors.Is(err, jwks.ErrKeyNotFound) {
				t.Errorf("an encryption key (use enc) was usable for verification: %v", err)
			}
			cancel()
		}
		if enc == 0 {
			t.Error("the key set of Keycloak holds no key with use enc: the scenario no longer tests what it says")
		}
	})
}

// fetchJWKS reads a key set from a provider.
func fetchJWKS(t *testing.T, uri string) []map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := providerClient(t).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var doc struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	return doc.Keys
}

func bitLen(k any) any {
	if rk, ok := k.(*rsa.PublicKey); ok {
		return rk.N.BitLen()
	}
	return "?"
}
