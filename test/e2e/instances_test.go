package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/vault/transittest"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeidp"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/otlptest"
	"github.com/pandokos/pandokos/test/testcert"
	"github.com/pandokos/pandokos/test/testutil"
	"github.com/pandokos/pandokos/test/vaulttest"
)

// ambient is the environment other software defines, with values that would break either instance
// if the process read them: a database host that nobody answers on, a wrong password, a collector
// and a proxy that do not exist, and a time zone far from the two instances' (sprint 12.3, 5.10).
var ambient = []string{
	"PGHOST=192.0.2.1", "PGPORT=1", "PGPASSWORD=synthetic-ambient-wrong-password", "PGUSER=ambient",
	"OTEL_EXPORTER_OTLP_ENDPOINT=http://192.0.2.1:4318", "OTEL_SERVICE_NAME=synthetic-ambient-service",
	"HTTPS_PROXY=http://192.0.2.1:3128", "HTTP_PROXY=http://192.0.2.1:3128",
	"TZ=Pacific/Kiritimati",
}

// configFile writes the file of a process that is defined by a file only: the settings given as
// PANDOKOS_… variables are written as the keys they are, typed as the key table says.
func configFile(t *testing.T, env []string) string {
	t.Helper()
	byEnv := map[string]config.Key{}
	for _, k := range config.Keys() {
		byEnv[k.Env] = k
	}
	root := map[string]any{}
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		key, ok := byEnv[name]
		if !ok {
			t.Fatalf("%s is not a configuration key", name)
		}
		var v any = value
		switch key.Type {
		case "integer":
			n, err := strconv.Atoi(value)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			v = n
		case "boolean":
			v = value == "true"
		case "list":
			if key.Name != "telemetry.resource.attributes" { // the one list that the file holds as text
				v = strings.Split(value, ",")
			}
		}
		node := root
		parts := strings.Split(key.Name, ".")
		for _, seg := range parts[:len(parts)-1] {
			next, _ := node[seg].(map[string]any)
			if next == nil {
				next = map[string]any{}
				node[seg] = next
			}
			node = next
		}
		node[parts[len(parts)-1]] = v
	}
	b, err := yaml.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "instance.yaml")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// without drops the variables whose names start with one of the prefixes.
func without(env []string, prefixes ...string) []string {
	var out []string
outer:
	for _, kv := range env {
		for _, p := range prefixes {
			if strings.HasPrefix(kv, p) {
				continue outer
			}
		}
		out = append(out, kv)
	}
	return out
}

// startProcess starts "pandokos serve" with exactly the environment and arguments of a process of
// the test's own making, which the helpers of startBinary do not allow (they always give both a
// file and variables), and waits for it to listen and to be ready.
func startProcess(t *testing.T, p *proc, args ...string) {
	t.Helper()
	p.stdout, p.stderr, p.exited = &testutil.LogBuffer{}, &testutil.LogBuffer{}, make(chan struct{})
	p.cmd = exec.Command(binary, append([]string{"serve"}, args...)...)
	p.cmd.Env = p.env
	p.cmd.Stdout, p.cmd.Stderr = p.stdout, p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		err := p.cmd.Wait()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			p.code = ee.ExitCode()
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
	p.waitReady(t)
}

// dbText is every row of every table of the schema as text, for the search of an identifier.
func dbText(t *testing.T, d *dbtest.Database) string {
	t.Helper()
	conn := d.Super(t)
	ctx := context.Background()
	rows, err := conn.Query(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema = 'pandokos' AND table_type = 'BASE TABLE' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	var sb strings.Builder
	for _, name := range tables {
		r, err := conn.Query(ctx, fmt.Sprintf(`SELECT row_to_json(x)::text FROM pandokos.%q x`, name))
		if err != nil {
			continue // a partition parent or a table the superuser cannot describe as text
		}
		for r.Next() {
			var s string
			if err := r.Scan(&s); err == nil {
				sb.WriteString(s)
				sb.WriteByte('\n')
			}
		}
		r.Close()
	}
	return sb.String()
}

// TestTwoInstancesSideBySide is T-1127: one binary, two processes, one host, the same ambient
// environment. A is defined by a file only (no PANDOKOS_ variable), B by the environment only (no
// file). Their databases, key-encryption keys (a file; a key service, after the refusal of an
// exportable key without the option), trusted issuers, listeners (plain; TLS with a public origin),
// telemetry (OTLP; Prometheus), support settings and time zones differ. Each serves a call with its
// own credential; what belongs to one (a key, a token, a session, a platform key) is refused by the
// other; each collector receives only its instance's telemetry with its attributes; a support
// action is refused on A and limited to 30 minutes on B; a tenant's day starts in each instance's
// zone; config check of each is clean but for the expected warnings; and nothing of A is in B's log
// or database, nor the reverse. The test is parallel: the ambient variables are in the processes'
// environments, never set with t.Setenv.
func TestTwoInstancesSideBySide(t *testing.T) {
	t.Parallel()

	idpA, idpB := fakeidp.Start(t, fakeidp.Options{}), fakeidp.Start(t, fakeidp.Options{})
	colA, colB := otlptest.New(t), otlptest.New(t)
	upA, upB := fakeprovider.New(t), fakeprovider.New(t)
	dbA, dbB := dbtest.NewMigrated(t), freshDB(t) // B's installation has no key version yet: its key is in the service
	const credA, credB = "synthetic-instance-a-credential-2f81c0aa", "synthetic-instance-b-credential-9d47e613"

	// Instance A: a file only. A key file, plain loopback listeners, collector 1 with its attributes and
	// metrics by OTLP, a platform-level issuer on IdP 1, support off, the zone Europe/Rome.
	envA := append(dbA.Env(t),
		"PANDOKOS_LOG_LEVEL=info",
		"PANDOKOS_LISTENERS_INFERENCE_ADDRESS=127.0.0.1:0", "PANDOKOS_LISTENERS_ADMIN_ADDRESS=127.0.0.1:0",
		"PANDOKOS_TELEMETRY_OTLP_ENDPOINT="+colA.URL().String(), "PANDOKOS_TELEMETRY_RESOURCE_ATTRIBUTES=deployment.environment=instance-a-canary,service.instance.id=a-1",
		"PANDOKOS_TELEMETRY_OTLP_TIMEOUT=2s", "PANDOKOS_TELEMETRY_METRICS_OTLP=true", "PANDOKOS_TELEMETRY_METRICS_EXPORT_INTERVAL=5s",
		"PANDOKOS_TOKENS_PRIVATE_HOSTS=localhost", "PANDOKOS_UPSTREAM_TLS_CA_FILE="+idpA.CAFile(),
		"PANDOKOS_SUPPORT_ENABLED=false", "PANDOKOS_LIMITS_TIME_ZONE=Europe/Rome")
	cfgA := configFile(t, envA)
	a := &proc{scheme: "http", client: &http.Client{Timeout: 10 * time.Second}, db: dbA, up: upA, upURL: upA.URL(), col: colA}
	a.cfg = cfgA
	a.env = baseEnv(t, ambient) // no PANDOKOS_ variable: the file is the whole configuration

	// Instance B: the environment only. The key service with an exportable key (refused without the
	// option, first), TLS on both listeners with its own certificate and a public origin, collector 2
	// and the Prometheus scrape, a tenant issuer on IdP 2, support for 30 minutes, the zone UTC.
	const keyName = "synthetic-instance-b-key"
	service := newTransitWorld(t, vaulttest.TransitSetup{Key: keyName, KeyOptions: transittest.KeyOptions{Exportable: true}})
	ca, cert, key := testcert.Files(t, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	envB := append(without(dbB.Env(t), "PANDOKOS_VAULT_KEK_LOCAL_KEY_FILE="),
		"PANDOKOS_LOG_LEVEL=info",
		"PANDOKOS_LISTENERS_INFERENCE_ADDRESS=127.0.0.1:0", "PANDOKOS_LISTENERS_ADMIN_ADDRESS=127.0.0.1:0",
		"PANDOKOS_LISTENERS_INFERENCE_TLS_CERT_FILE="+cert, "PANDOKOS_LISTENERS_INFERENCE_TLS_KEY_FILE="+key,
		"PANDOKOS_LISTENERS_ADMIN_TLS_CERT_FILE="+cert, "PANDOKOS_LISTENERS_ADMIN_TLS_KEY_FILE="+key,
		"PANDOKOS_AUTH_PUBLIC_ORIGIN=https://localhost:8443",
		"PANDOKOS_VAULT_KEK_PROVIDER=transit",
		"PANDOKOS_TELEMETRY_OTLP_ENDPOINT="+colB.URL().String(), "PANDOKOS_TELEMETRY_RESOURCE_ATTRIBUTES=deployment.environment=instance-b-canary,service.instance.id=b-1",
		"PANDOKOS_TELEMETRY_METRICS_PROMETHEUS=true",
		"PANDOKOS_TOKENS_PRIVATE_HOSTS=localhost", "PANDOKOS_UPSTREAM_TLS_CA_FILE="+idpB.CAFile(),
		"PANDOKOS_SUPPORT_ENABLED=true", "PANDOKOS_SUPPORT_MAX_DURATION=30m", "PANDOKOS_LIMITS_TIME_ZONE=UTC")
	envB = append(envB, service.conf().env()...)
	pool := x509.NewCertPool()
	pem, err := os.ReadFile(ca)
	if err != nil {
		t.Fatal(err)
	}
	pool.AppendCertsFromPEM(pem)
	b := &proc{scheme: "https", caFile: ca, client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}},
		db: dbB, up: upB, upURL: upB.URL(), col: colB}
	b.env = baseEnv(t, append(append([]string{}, ambient...), envB...))

	// The refusal first: the key is exportable and the option is not set.
	code, out, errs := runBinary(t, b.env, "serve")
	if code != 1 || !strings.Contains(out+errs, "allow_exportable_key") {
		t.Fatalf("serve on an exportable key without the option: exit %d\n%s%s", code, out, errs)
	}
	b.env = append(b.env, "PANDOKOS_VAULT_KEK_TRANSIT_ALLOW_EXPORTABLE_KEY=true")

	// The configuration of each is checked as it will run.
	for name, check := range map[string]struct {
		env  []string
		args []string
	}{"A": {a.env, []string{"config", "check", "--config", cfgA}}, "B": {b.env, []string{"config", "check"}}} {
		code, out, errs := runBinary(t, check.env, check.args...)
		if code != 0 {
			t.Errorf("config check of %s: exit %d\n%s%s", name, code, out, errs)
		}
		testutil.AssertAbsent(t, "config check of "+name, out+errs, credA, credB)
	}

	// Both start from nothing: init writes the bootstrap key and an administrator, serve starts.
	keyFileA, keyFileB := filepath.Join(t.TempDir(), "a.key"), filepath.Join(t.TempDir(), "b.key")
	passwordA := initAdmin(t, a.env, "a.admin", "--config", cfgA, "--key-out", keyFileA)
	passwordB := initAdmin(t, b.env, "b.admin", "--key-out", keyFileB)
	a.platformFile, b.platformFile = keyFileA, keyFileB
	startProcess(t, a, "--config", cfgA)
	startProcess(t, b)
	for _, p := range []*proc{a, b} {
		if strings.Contains(p.stdout.String(), "192.0.2.1") {
			t.Errorf("the log of a process names an address of the ambient environment:\n%s", p.stdout.String())
		}
	}

	// Each has a tenant, a credential of its own and an inference key.
	upA.Accept(credA)
	upB.Accept(credB)
	type site struct {
		p                   *proc
		up                  *fakeprovider.Server
		cred                string
		id, admin, infKey   string
		keyID, name, zoneIn string
	}
	sites := []*site{{p: a, up: upA, cred: credA, name: "instance-a-tenant", zoneIn: "Europe/Rome"}, {p: b, up: upB, cred: credB, name: "instance-b-tenant", zoneIn: "UTC"}}
	for _, s := range sites {
		s.p.prepareUpstream(t)
		s.up.Accept(s.cred)
		s.id, s.admin = s.p.newTenant(t, s.p.platformFile, s.name)
		s.p.addCredential(t, s.admin, "openai", s.cred)
		s.keyID, s.infKey = s.p.newInferenceKey(t, s.admin, s.id)
	}

	t.Run("each serves a call with its own credential", func(t *testing.T) {
		for _, s := range sites {
			if status, text := exchangeWith(t, s.p.client, "POST", s.p.url("/openai/v1/chat/completions"), authHeader(s.infKey), chatBody(fakeprovider.ModelBasic, false, "side by side")); status != 200 {
				t.Fatalf("%s: %d\n%s", s.name, status, text)
			}
			var got []string
			for _, r := range s.up.Requests() {
				if r.Method == http.MethodPost {
					got = append(got, r.Header.Get("Authorization"))
				}
			}
			if len(got) != 1 || got[0] != "Bearer "+s.cred {
				t.Errorf("%s: the provider received %d calls, with the credential of this instance: %v", s.name, len(got), len(got) == 1 && got[0] == "Bearer "+s.cred)
			}
		}
	})

	t.Run("what belongs to one is refused by the other", func(t *testing.T) {
		// A key of one, a platform key of one, and a session of A are no credential of the other.
		for _, c := range []struct {
			name         string
			from, to     *site
			path, header string
		}{
			{"the inference key of A on B", sites[0], sites[1], "/openai/v1/chat/completions", "key"},
			{"the inference key of B on A", sites[1], sites[0], "/openai/v1/chat/completions", "key"},
		} {
			if status, text := exchangeWith(t, c.to.p.client, "POST", c.to.p.url(c.path), authHeader(c.from.infKey), chatBody(fakeprovider.ModelBasic, false, "x")); status != 401 {
				t.Errorf("%s: %d\n%s", c.name, status, text)
			}
		}
		for name, pair := range map[string][2]*proc{"the platform key of A on B": {a, b}, "the platform key of B on A": {b, a}} {
			from, to := pair[0], pair[1]
			if status, _ := exchangeWith(t, to.client, "GET", to.adminURL("/v1/me"), authHeader(readKeyFile(t, from.platformFile)), nil); status != 401 {
				t.Errorf("%s: %d, want 401", name, status)
			}
			if status, _ := exchangeWith(t, from.client, "GET", from.adminURL("/v1/me"), authHeader(readKeyFile(t, from.platformFile)), nil); status != 200 {
				t.Errorf("%s: its own installation answers %d, want 200", name, status)
			}
		}
		br := a.browser()
		get[adminapi.Session](t, br.SignIn(t, "a.admin", passwordA), 200)
		token := br.Token()
		if token == "" {
			t.Fatal("no session cookie on A")
		}
		cookie := http.Header{"Cookie": {"__Host-pandokos_session=" + token}}
		if status, _ := exchangeWith(t, b.client, "GET", b.adminURL("/v1/me"), cookie, nil); status != 401 {
			t.Errorf("a session of A on B: %d, want 401", status)
		}
		_ = passwordB
	})

	// The support settings.
	t.Run("support: refused on A, limited to 30 minutes on B", func(t *testing.T) {
		code, out, errs := a.cli(t, keyFileA, "tenant-support", "open", "--tenant", sites[0].id, "--reason", "side by side", "--permission", "service_account:read")
		if code == 0 || !strings.Contains(out+errs, "support_disabled") {
			t.Errorf("a support action on A (support.enabled false): exit %d\n%s%s", code, out, errs)
		}
		open := cliJSON(t, b, keyFileB, "tenant-support", "open", "--tenant", sites[1].id, "--reason", "side by side", "--permission", "service_account:read")
		opened, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(open["opened_at"]))
		expires, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(open["expires_at"]))
		if got := expires.Sub(opened); got != 30*time.Minute {
			t.Errorf("the default support action on B lasts %v, want 30m (support.max_duration): %v", got, open)
		}
		if code, _, _ := b.cli(t, keyFileB, "tenant-support", "open", "--tenant", sites[1].id, "--reason", "too long", "--permission", "service_account:read", "--duration", "31m"); code == 0 {
			t.Error("a support action of 31 minutes is accepted on B")
		}
	})

	t.Run("a tenant's day starts in the zone of its instance", func(t *testing.T) {
		for _, s := range sites {
			got := cliJSON(t, s.p, s.admin, "limit", "time-zone", "get", "--tenant", s.id)
			if got["in_force"] != s.zoneIn {
				t.Errorf("%s: the time zone in force is %v, want %s", s.name, got["in_force"], s.zoneIn)
			}
		}
	})

	t.Run("each collector receives only its instance's telemetry", func(t *testing.T) {
		testutil.Eventually(t, 30*time.Second, "spans at both collectors and the metrics of A", func() bool {
			return len(colA.Spans()) > 0 && len(colB.Spans()) > 0 && len(colA.Metrics()) > 0
		})
		attrs := func(c *otlptest.Collector) string {
			var sb strings.Builder
			for _, rs := range c.ResourceSpans() {
				for _, kv := range rs.GetResource().GetAttributes() {
					sb.WriteString(kv.GetKey() + "=" + kv.GetValue().GetStringValue() + "\n")
				}
			}
			return sb.String()
		}
		if got := attrs(colA); !strings.Contains(got, "deployment.environment=instance-a-canary") || strings.Contains(got, "instance-b-canary") {
			t.Errorf("the resource attributes at collector 1:\n%s", got)
		}
		if got := attrs(colB); !strings.Contains(got, "deployment.environment=instance-b-canary") || strings.Contains(got, "instance-a-canary") {
			t.Errorf("the resource attributes at collector 2:\n%s", got)
		}
		if n := len(colB.Metrics()); n != 0 {
			t.Errorf("collector 2 received %d metric exports: B scrapes (Prometheus) and does not export metrics by OTLP", n)
		}
		status, text := exchangeWith(t, b.client, "GET", b.adminURL("/metrics"), authHeader(readKeyFile(t, keyFileB)), nil)
		if status != 200 || !strings.Contains(text, "pandokos_") {
			t.Errorf("the scrape of B: %d\n%.300s", status, text)
		}
		if status, _ := exchangeWith(t, a.client, "GET", a.adminURL("/metrics"), authHeader(readKeyFile(t, keyFileA)), nil); status != 404 {
			t.Errorf("the scrape of A: %d, want 404 (telemetry.metrics.prometheus is not set on A)", status)
		}
	})

	// Trusted issuers, applied from documents: a platform-level issuer on IdP 1 for A, a tenant issuer on
	// IdP 2 for B, each with the mapping of a subject to the tenant's service account.
	t.Run("trusted issuers applied from documents; a token of one is refused by the other", func(t *testing.T) {
		dir := t.TempDir()
		write := func(name, doc string) string {
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}
		for _, s := range sites {
			s.p.mustCLI(t, s.admin, "service-account", "create", "--tenant", s.id, "--role", "inference", "workload")
		}
		platformDocA := write("platform-a.yaml", fmt.Sprintf("pandokos: v1\nkind: platform\nplatform_issuers:\n  - name: idp-a\n    issuer: %s\n    audience: pandokos-instance-a\n    tenant_claim: org\n    enabled: true\n    tenants:\n      - tenant: %s\n        claim_value: org-a\n",
			idpA.Issuer(), sites[0].name))
		tenantDocA := write("tenant-a.yaml", "pandokos: v1\nkind: tenant\nservice_accounts:\n  - name: workload\n    roles:\n      - inference\nsubject_mappings:\n  - issuer:\n      scope: platform\n      name: idp-a\n    subject: svc\n    service_account: workload\n")
		tenantDocB := write("tenant-b.yaml", fmt.Sprintf("pandokos: v1\nkind: tenant\nservice_accounts:\n  - name: workload\n    roles:\n      - inference\ntoken_issuers:\n  - name: idp-b\n    issuer: %s\n    audience: pandokos-instance-b\n    enabled: true\nsubject_mappings:\n  - issuer:\n      scope: tenant\n      name: idp-b\n    subject: svc\n    service_account: workload\n", idpB.Issuer()))
		a.mustCLI(t, keyFileA, "apply", "--platform", "--file", platformDocA)
		a.mustCLI(t, sites[0].admin, "apply", "--tenant", sites[0].id, "--file", tenantDocA)
		b.mustCLI(t, sites[1].admin, "apply", "--tenant", sites[1].id, "--file", tenantDocB)

		tokenA := idpA.Sign("ES256", idpA.Standard("svc", "pandokos-instance-a", 10*time.Minute).With("org", "org-a"))
		tokenB := idpB.Sign("ES256", idpB.Standard("svc", "pandokos-instance-b", 10*time.Minute))
		call := func(p *proc, token string) int {
			status, _ := exchangeWith(t, p.client, "POST", p.url("/openai/v1/chat/completions"), authHeader(token), chatBody(fakeprovider.ModelBasic, false, "x"))
			return status
		}
		if s := call(a, tokenA); s != 200 {
			t.Errorf("a token of IdP 1 on A: %d, want 200", s)
		}
		if s := call(b, tokenB); s != 200 {
			t.Errorf("a token of IdP 2 on B: %d, want 200", s)
		}
		if s := call(b, tokenA); s != 401 {
			t.Errorf("a token of IdP 1 on B: %d, want 401", s)
		}
		if s := call(a, tokenB); s != 401 {
			t.Errorf("a token of IdP 2 on A: %d, want 401", s)
		}
	})

	t.Run("nothing of one is in the log or the database of the other", func(t *testing.T) {
		logs := map[*proc]string{}
		for _, p := range []*proc{a, b} {
			logs[p] = p.stdout.String() + p.stderr.String()
		}
		texts := map[*proc]string{a: dbText(t, dbA), b: dbText(t, dbB)}
		for _, c := range []struct {
			name         string
			mine, theirs *site
			cred         string
			idp          *fakeidp.Server
		}{{"A in B", sites[0], sites[1], credA, idpA}, {"B in A", sites[1], sites[0], credB, idpB}} {
			needles := []string{c.mine.id, c.mine.name, c.mine.keyID, c.mine.infKey, c.cred, c.idp.Issuer()}
			testutil.AssertAbsent(t, "the log of the other instance ("+c.name+")", logs[c.theirs.p], needles...)
			testutil.AssertAbsent(t, "the database of the other instance ("+c.name+")", texts[c.theirs.p], needles...)
		}
		for _, p := range []*proc{a, b} {
			testutil.AssertAbsent(t, "a log", logs[p], "synthetic-ambient-wrong-password", "192.0.2.1", "synthetic-ambient-service")
		}
		for _, p := range []*proc{a, b} {
			if code := p.stop(t); code != 0 {
				t.Errorf("exit code %d", code)
			}
		}
	})
}
