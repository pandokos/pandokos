package e2e

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testcert"
	"github.com/pandokos/pandokos/test/testutil"
)

const chatPath = "/openai/v1/chat/completions"

// callerTenant is a tenant with an administrator, an inference key and a credential for openai
// (fixtures.CanaryCredential) made with the process's catalogue set up.
type callerTenant struct {
	id, adminFile, keyID, key string
}

func (p *proc) newCaller(t *testing.T, name string) callerTenant {
	t.Helper()
	platform := p.bootstrap(t)
	id, admin := p.newTenant(t, platform, name)
	keyID, key := p.newInferenceKey(t, admin, id)
	p.prepareTenant(t, admin)
	return callerTenant{id: id, adminFile: admin, keyID: keyID, key: key}
}

// chatCall sends a chat completion request with the key and returns the status and the whole text
// of the response.
func (p *proc) chatCall(t *testing.T, key, model string, stream bool) (int, string) {
	t.Helper()
	return exchangeWith(t, p.client, "POST", p.url(chatPath), authHeader(key), chatBody(model, stream, fixtures.CanaryPrompt))
}

// assertPandokosError checks a response raised by Pandokos: the status, both headers and the
// OpenAI shape.
func assertPandokosError(t *testing.T, what string, status int, text string, wantStatus int, code string) {
	t.Helper()
	if status != wantStatus {
		t.Errorf("%s: status %d, want %d\n%s", what, status, wantStatus, text)
	}
	for _, want := range []string{"X-Pandokos-Error: " + code, "X-Pandokos-Request-Id: ", `"type":"pandokos_error"`, `"code":"` + code + `"`} {
		if !strings.Contains(text, want) {
			t.Errorf("%s: the response lacks %q\n%s", what, want, text)
		}
	}
}

// front is a TLS server on loopback whose certificate, from a test authority, names localhost.
// It forwards to a fake provider and counts the connections it accepts.
type front struct {
	*httptest.Server
	caFile   string
	port     string
	accepted atomic.Int64
}

func startFront(t *testing.T, up *fakeprovider.Server) *front {
	t.Helper()
	ca, certFile, keyFile := testcert.Files(t, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(up.URL())
	if err != nil {
		t.Fatal(err)
	}
	f := &front{caFile: ca}
	f.Server = httptest.NewUnstartedServer(httputil.NewSingleHostReverseProxy(target))
	f.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	f.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			f.accepted.Add(1)
		}
	}
	f.StartTLS()
	t.Cleanup(f.Close)
	_, f.port, _ = net.SplitHostPort(f.Listener.Addr().String())
	return f
}

// endpointIs waits until a call for the model fake-basic gives the wanted status: the gateway
// learns of an endpoint change through a notification.
func (p *proc) waitStatus(t *testing.T, key string, want int) {
	t.Helper()
	testutil.Eventually(t, 10*time.Second, fmt.Sprintf("the gateway to answer %d", want), func() bool {
		s, _ := p.chatCall(t, key, fakeprovider.ModelBasic, false)
		return s == want
	})
}

// TestGatewayExitScenario is T-451: the exit scenario of the epic on the built binary.
func TestGatewayExitScenario(t *testing.T) {
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", up: up})
	boot := p.bootstrap(t)
	p.useFakeProvider(t, boot, "openai", up)
	p.addFakeModels(t, boot)
	p.upReady = true
	p.mustCLI(t, boot, "model", "create", "--availability", "selected", "openai/fake-selected")

	const credA, credB, credE = "synthetic-credential-tenant-a-0001", "synthetic-credential-tenant-b-0002", "synthetic-credential-tenant-e-0003"
	up.Accept(credA, credB, credE)
	newTenant := func(name, credential string, keyFlags ...string) callerTenant {
		id, admin := p.newTenant(t, boot, name)
		keyID, key := p.newInferenceKey(t, admin, id, keyFlags...)
		if credential != "" {
			p.addCredential(t, admin, "openai", credential)
		}
		return callerTenant{id: id, adminFile: admin, keyID: keyID, key: key}
	}
	a, b := newTenant("alpha", credA), newTenant("beta", credB)
	c := newTenant("gamma", "")
	d := newTenant("delta", credA, "--scope", "model:invoke")
	e := newTenant("epsilon", credE)

	authorizations := func(from int) []string {
		var out []string
		for _, r := range up.Requests()[from:] {
			if r.Path == "/v1/chat/completions" {
				out = append(out, r.Header.Get("Authorization"))
			}
		}
		return out
	}

	t.Run("tenant A completes a non-streaming and a streaming call with its own credential", func(t *testing.T) {
		before := len(up.Requests())
		status, text := p.chatCall(t, a.key, fakeprovider.ModelBasic, false)
		if status != 200 || !strings.HasSuffix(text, string(fixtures.Read("chat_completion.json"))) {
			t.Fatalf("non-streaming: %d\n%s", status, text)
		}
		status, text = p.chatCall(t, a.key, fakeprovider.ModelBasic, true)
		if status != 200 || !strings.HasSuffix(text, "data: [DONE]\n\n") || strings.Contains(text, `"choices":[]`) {
			t.Fatalf("streaming: %d\n%s", status, text)
		}
		got := authorizations(before)
		if len(got) != 2 || got[0] != "Bearer "+credA || got[1] != "Bearer "+credA {
			t.Errorf("the provider saw %v, want A's credential twice", got)
		}
		for _, r := range up.Requests()[before:] {
			testutil.AssertAbsent(t, "a header the provider received", fmt.Sprint(r.Header), testutil.KeyForms(a.key)...)
		}
	})

	t.Run("tenant B's calls carry B's credential and never A's", func(t *testing.T) {
		before := len(up.Requests())
		for i := 0; i < 2; i++ {
			if status, text := p.chatCall(t, b.key, fakeprovider.ModelBasic, i == 1); status != 200 {
				t.Fatalf("B: %d\n%s", status, text)
			}
		}
		got := authorizations(before)
		if len(got) != 2 {
			t.Fatalf("the provider saw %v", got)
		}
		for _, h := range got {
			if h != "Bearer "+credB || strings.Contains(h, credA) {
				t.Errorf("the provider saw %q for a call of B", h)
			}
		}
	})

	refused := func(what string, key string, status int, code string, do func() (int, string)) {
		t.Helper()
		before := len(up.Requests())
		s, text := do()
		assertPandokosError(t, what, s, text, status, code)
		if n := len(up.Requests()) - before; n != 0 {
			t.Errorf("%s: the provider received %d requests", what, n)
		}
		testutil.AssertAbsent(t, what, text, append(testutil.KeyForms(key), testutil.SecretForms(credA)...)...)
	}
	refused("a model that is selected and not granted", a.key, 403, "model_not_allowed", func() (int, string) { return p.chatCall(t, a.key, "fake-selected", false) })
	refused("a model that does not exist", a.key, 403, "model_not_allowed", func() (int, string) { return p.chatCall(t, a.key, "no-such-model", false) })
	refused("an operation that is not supported", a.key, 404, "endpoint_not_supported", func() (int, string) {
		return exchangeWith(t, p.client, "POST", p.url("/openai/v1/embeddings"), authHeader(a.key), []byte(`{"model":"x","input":"y"}`))
	})
	refused("a provider that is not served", a.key, 404, "endpoint_not_supported", func() (int, string) {
		return exchangeWith(t, p.client, "POST", p.url("/nope/v1/chat/completions"), authHeader(a.key), chatBody("fake-basic", false, "x"))
	})
	refused("a key scoped to model:invoke alone", d.key, 403, "forbidden", func() (int, string) { return p.chatCall(t, d.key, fakeprovider.ModelBasic, false) })
	refused("a tenant without a credential", c.key, 409, "no_credential", func() (int, string) { return p.chatCall(t, c.key, fakeprovider.ModelBasic, false) })
	refused("a body that is not a request", a.key, 400, "invalid_request", func() (int, string) {
		return exchangeWith(t, p.client, "POST", p.url(chatPath), authHeader(a.key), []byte(`{"messages":[]}`))
	})
	refused("no credential in the request", a.key, 401, "unauthenticated", func() (int, string) {
		return exchangeWith(t, p.client, "POST", p.url(chatPath), http.Header{"Content-Type": {"application/json"}}, chatBody("fake-basic", false, "x"))
	})

	t.Run("a suspended tenant is refused within the bound", func(t *testing.T) {
		if status, _ := p.chatCall(t, e.key, fakeprovider.ModelBasic, false); status != 200 {
			t.Fatalf("before the suspension: %d", status)
		}
		p.mustCLI(t, boot, "tenant", "suspend", e.id)
		testutil.Eventually(t, 3*time.Second, "the suspended tenant to be refused", func() bool {
			s, text := p.chatCall(t, e.key, fakeprovider.ModelBasic, false)
			if s == 403 && strings.Contains(text, "tenant_suspended") {
				assertPandokosError(t, "a suspended tenant", s, text, 403, "tenant_suspended")
				return true
			}
			return false
		})
	})
}

// TestEgressAttacks is T-452: a crafted endpoint and a redirect do not reach an internal address.
func TestEgressAttacks(t *testing.T) {
	up := fakeprovider.New(t)
	f := startFront(t, up)
	p := startBinary(t, opts{level: "info", up: up, env: []string{"PANDOKOS_UPSTREAM_TLS_CA_FILE=" + f.caFile}})
	boot := p.bootstrap(t)
	tn := p.newCaller(t, "attacked")
	p.waitStatus(t, tn.key, 200) // the endpoint is the fake, private

	blocked := func(reason string) {
		t.Helper()
		testutil.Eventually(t, 5*time.Second, "an egress blocked line for "+reason, func() bool {
			for _, l := range completeLines(p.stdout, "egress blocked") {
				if l["reason"] == reason {
					return true
				}
			}
			return false
		})
	}

	t.Run("(a) a public endpoint named localhost", func(t *testing.T) {
		p.mustCLI(t, boot, "endpoint", "update", "--base-url", "https://localhost:"+f.port, "--private=false", "openai", "default")
		p.waitStatus(t, tn.key, 502)
		status, text := p.chatCall(t, tn.key, fakeprovider.ModelBasic, false)
		assertPandokosError(t, "localhost", status, text, 502, "upstream_unreachable")
		testutil.AssertAbsent(t, "the response", text, "localhost", f.port, "127.0.0.1")
		if n := f.accepted.Load(); n != 0 {
			t.Errorf("the listener on that port accepted %d connections", n)
		}
		blocked("private_address")

		// (d) validate: true against the same endpoint.
		secretFile := testutil.WriteSecret(t, "validate-secret", fixtures.CanaryCredential)
		code, out, errs := p.cli(t, tn.adminFile, "credential", "create", "--provider", "openai", "--secret-file", secretFile, "--validate", "validated")
		if code != 1 || !strings.Contains(out+errs, "validation_unavailable") {
			t.Errorf("validate against a public endpoint named localhost: exit %d\n%s\n%s", code, out, errs)
		}
		testutil.AssertAbsent(t, "the CLI", out+errs, testutil.SecretForms(fixtures.CanaryCredential)...)
		if n := f.accepted.Load(); n != 0 {
			t.Errorf("the validation reached the listener: %d connections", n)
		}
	})

	t.Run("(b) a provider that redirects to an internal listener", func(t *testing.T) {
		var hits atomic.Int64
		internal := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
		t.Cleanup(internal.Close)
		up.RedirectTo(internal.URL + "/metadata")
		p.useFakeProvider(t, boot, "openai", up)
		p.waitStatus(t, tn.key, 200)
		status, text := p.chatCall(t, tn.key, fakeprovider.ModelRedirect, false)
		assertPandokosError(t, "redirect", status, text, 502, "upstream_unreachable")
		if strings.Contains(text, "Location:") || strings.Contains(text, internal.URL) {
			t.Errorf("the redirect reached the caller:\n%s", text)
		}
		if n := hits.Load(); n != 0 {
			t.Errorf("the internal listener received %d requests", n)
		}
	})

	t.Run("(c) an endpoint on the metadata address, marked private", func(t *testing.T) {
		p.mustCLI(t, boot, "endpoint", "update", "--base-url", "http://169.254.169.254", "--private", "openai", "default")
		p.waitStatus(t, tn.key, 502)
		status, text := p.chatCall(t, tn.key, fakeprovider.ModelBasic, false)
		assertPandokosError(t, "metadata", status, text, 502, "upstream_unreachable")
		testutil.AssertAbsent(t, "the response", text, "169.254")
		blocked("forbidden_address")
		found := false
		for _, l := range completeLines(p.stdout, "request") {
			found = found || l["egress_reason"] == "forbidden_address"
		}
		if !found {
			t.Error("no request line with egress_reason forbidden_address")
		}
	})
}

// TestGatewayCanaries is T-453: nothing of a prompt, a completion, a key or a credential is
// written anywhere it must not be.
func TestGatewayCanaries(t *testing.T) {
	for _, level := range []string{"info", "debug"} {
		t.Run(level, func(t *testing.T) {
			up := fakeprovider.New(t)
			p := startBinary(t, opts{level: level, up: up, env: []string{"PANDOKOS_LIMITS_MAX_REQUEST_BODY_BYTES=2048"}})
			boot := p.bootstrap(t)
			p.useFakeProvider(t, boot, "openai", up)
			p.addFakeModels(t, boot)
			p.upReady = true
			p.mustCLI(t, boot, "model", "create", "--availability", "selected", "openai/fake-selected")
			up.Accept(fixtures.CanaryCredential)

			id, admin := p.newTenant(t, boot, "canary")
			_, key := p.newInferenceKey(t, admin, id)
			_, scoped := p.newInferenceKey(t, admin, id, "--scope", "model:invoke")
			p.addCredential(t, admin, "openai", fixtures.CanaryCredential)
			idNo, adminNo := p.newTenant(t, boot, "canary-no-credential")
			_, keyNo := p.newInferenceKey(t, adminNo, idNo)

			type response struct{ what, text string }
			var responses []response
			call := func(what, key, model string, stream bool) int {
				s, text := p.chatCall(t, key, model, stream)
				responses = append(responses, response{what, text})
				return s
			}
			raw := func(what, method, path string, hdr http.Header, body []byte) {
				_, text := exchangeWith(t, p.client, method, p.url(path), hdr, body)
				responses = append(responses, response{what, text})
			}
			// Successes.
			p.waitStatus(t, key, 200)
			call("success", key, fakeprovider.ModelBasic, false)
			call("stream", key, fakeprovider.ModelBasic, true)
			// Every refusal of T-451.
			call("model not granted", key, "fake-selected", false)
			call("model unknown", key, "no-such-model", false)
			call("scoped key", scoped, fakeprovider.ModelBasic, false)
			call("no credential", keyNo, fakeprovider.ModelBasic, false)
			raw("no authorization", "POST", chatPath, http.Header{"Content-Type": {"application/json"}}, chatBody("fake-basic", false, fixtures.CanaryPrompt))
			raw("embeddings", "POST", "/openai/v1/embeddings", authHeader(key), chatBody("fake-basic", false, fixtures.CanaryPrompt))
			raw("invalid body", "POST", chatPath, authHeader(key), []byte(`{"messages":[{"content":"`+fixtures.CanaryPrompt+`"}]}`))
			raw("too large", "POST", chatPath, authHeader(key), chatBody("fake-basic", false, strings.Repeat(fixtures.CanaryPrompt, 200)))
			raw("wrong key", "POST", chatPath, authHeader(testutil.NewKey(t)), chatBody("fake-basic", false, fixtures.CanaryPrompt))
			// A provider 401 whose headers echo the credential.
			up.Accept()
			call("provider 401", key, fakeprovider.ModelBasic, false)
			call("provider echo", key, fakeprovider.ModelEchoAuth, false)
			up.Accept(fixtures.CanaryCredential)
			// An egress block.
			p.mustCLI(t, boot, "endpoint", "update", "--base-url", "http://169.254.169.254", "--private", "openai", "default")
			testutil.Eventually(t, 10*time.Second, "the egress block", func() bool { return call("egress block", key, fakeprovider.ModelBasic, false) == 502 })
			p.useFakeProvider(t, boot, "openai", up)
			p.waitStatus(t, key, 200)
			// A validation.
			secretFile := testutil.WriteSecret(t, "validate-secret", fixtures.CanaryCredential)
			p.mustCLI(t, admin, "credential", "create", "--provider", "openai", "--secret-file", secretFile, "--validate", "validated")

			if code := p.stop(t); code != 0 {
				t.Fatalf("exit code %d", code)
			}
			var dumps []response
			for _, table := range []string{"audit_event", "platform_audit_event"} {
				var dump string
				if err := p.db.Super(t).QueryRow(t.Context(), "SELECT COALESCE(jsonb_agg(to_jsonb(t))::text, '') FROM pandokos."+table+" t").Scan(&dump); err != nil {
					t.Fatal(err)
				}
				dumps = append(dumps, response{table, dump})
			}
			outputs := []response{{"stdout", p.stdout.String()}, {"stderr", p.stderr.String()}}
			outputs = append(outputs, dumps...)

			secrets := append(append(testutil.KeyForms(key), testutil.KeyForms(scoped)...), testutil.KeyForms(keyNo)...)
			secrets = append(secrets, testutil.SecretForms(fixtures.CanaryCredential)...)
			secrets = append(secrets, testutil.SecretForms(fixtures.CanaryProviderKey)...)
			secrets = append(secrets, p.db.Passwords()...)
			for _, o := range outputs {
				testutil.AssertNoCanary(t, o.what, o.text)
				testutil.AssertAbsent(t, o.what, o.text, secrets...)
			}
			for _, r := range responses {
				testutil.AssertAbsent(t, "the response "+r.what, r.text, secrets...)
				if strings.Contains(r.text, "X-Pandokos-Error") {
					testutil.AssertNoCanary(t, "the Pandokos error "+r.what, r.text)
				}
			}
		})
	}
}

// TestUpstreamCAAndProxy is T-454.
func TestUpstreamCAAndProxy(t *testing.T) {
	t.Run("a private CA for a private endpoint", func(t *testing.T) {
		up := fakeprovider.NewTLS(t)
		d := dbtest.New(t)
		first := startBinary(t, opts{level: "info", up: up, db: d})
		key := first.inferenceKey(t)
		if status, text := first.chatCall(t, key, fakeprovider.ModelBasic, false); status != 502 {
			t.Fatalf("without upstream.tls.ca_file: %d\n%s", status, text)
		} else {
			assertPandokosError(t, "an unknown authority", status, text, 502, "upstream_unreachable")
		}
		if code := first.stop(t); code != 0 {
			t.Fatalf("exit code %d", code)
		}
		caFile := testutil.WriteSecret(t, "upstream-ca.pem", strings.TrimSpace(string(up.CertPEM())))
		second := startBinary(t, opts{level: "info", up: up, db: d, noMigrate: true, env: []string{"PANDOKOS_UPSTREAM_TLS_CA_FILE=" + caFile}})
		second.platformFile, second.tenantKey, second.upReady = first.platformFile, first.tenantKey, true
		second.waitStatus(t, key, 200)
	})

	t.Run("a proxy for a public endpoint", func(t *testing.T) {
		up := fakeprovider.New(t)
		f := startFront(t, up)
		proxy, requests := testutil.ConnectProxy(t)
		priceURL := strings.Replace(testutil.PriceServer(t, priceFixture(t, "changed.json"), testutil.PriceOK), "127.0.0.1", "localhost", 1)
		p := startBinary(t, opts{level: "info", up: up, env: []string{
			"PANDOKOS_UPSTREAM_TLS_CA_FILE=" + f.caFile,
			"PANDOKOS_UPSTREAM_PROXY_URL=" + proxy.String(),
			"HTTPS_PROXY=http://127.0.0.1:1", // the environment's proxy is not used
			"PANDOKOS_PRICING_REFRESH_ENABLED=true",
			"PANDOKOS_PRICING_REFRESH_URL=" + priceURL,
			"PANDOKOS_PRICING_REFRESH_INTERVAL=1h",
			"PANDOKOS_TEST_REFRESH_DELAY=1s",
			"SSL_CERT_FILE=" + testutil.SSLCertFile(t),
		}})
		tn := p.newCaller(t, "proxied")
		boot := p.bootstrap(t)
		p.mustCLI(t, boot, "endpoint", "update", "--base-url", "https://localhost:"+f.port, "--private=false", "openai", "default")
		p.waitStatus(t, tn.key, 200)
		var connects []string
		for _, r := range requests() {
			if r.Method == http.MethodConnect {
				connects = append(connects, r.Host)
			}
			for name, v := range r.Header {
				testutil.AssertAbsent(t, "the CONNECT request, header "+name, strings.Join(v, ","), testutil.SecretForms(fixtures.CanaryCredential)...)
				testutil.AssertAbsent(t, "the CONNECT request, header "+name, strings.Join(v, ","), testutil.KeyForms(tn.key)...)
			}
		}
		want := "localhost:" + f.port
		seen := 0
		for _, c := range connects {
			if c == want {
				seen++
			}
		}
		if seen < 1 {
			t.Errorf("the proxy saw the CONNECT requests %v, want one for %s", connects, want)
		}
		// The call that succeeded is the documented delegation to the proxy.
		testutil.Eventually(t, 15*time.Second, "the price refresh through the proxy", func() bool { return len(completeLines(p.stdout, "prices imported")) > 0 })
		priceHost := ""
		if u, err := url.Parse(priceURL); err == nil {
			priceHost = u.Host
		}
		found := false
		for _, r := range requests() {
			found = found || (r.Method == http.MethodConnect && r.Host == priceHost)
		}
		if !found {
			t.Errorf("the price refresh did not go through the proxy: no CONNECT to %s", priceHost)
		}
		// The key is set: HTTPS_PROXY gives no warning, and is not used.
		for _, l := range logLines(t, p.stdout.String(), "configuration warning") {
			if l["key"] == "upstream.proxy.url" {
				t.Errorf("a warning although upstream.proxy.url is set: %v", l)
			}
		}
	})

	t.Run("HTTPS_PROXY in the environment is not used and gives the warning", func(t *testing.T) {
		up := fakeprovider.New(t)
		var contacted atomic.Int64
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				contacted.Add(1)
				c.Close()
			}
		}()
		t.Cleanup(func() { l.Close() })
		value := "http://" + l.Addr().String()
		p := startBinary(t, opts{level: "info", up: up, env: []string{"HTTPS_PROXY=" + value}})
		key := p.inferenceKey(t)
		if status, text := p.chatCall(t, key, fakeprovider.ModelBasic, false); status != 200 {
			t.Fatalf("a call with HTTPS_PROXY in the environment: %d\n%s", status, text)
		}
		var warning string
		for _, w := range logLines(t, p.stdout.String(), "configuration warning") {
			if w["key"] == "upstream.proxy.url" {
				warning = fmt.Sprint(w["warning"])
			}
		}
		if !strings.Contains(warning, "HTTPS_PROXY") {
			t.Errorf("no warning that names HTTPS_PROXY in the log: %q\n%s", warning, p.stdout.String())
		}
		testutil.AssertAbsent(t, "the process output", p.stdout.String()+p.stderr.String(), value)
		if n := contacted.Load(); n != 0 {
			t.Errorf("the proxy of the environment was contacted %d times", n)
		}
	})
}

// TestAuthFailureAudit is T-455: attributable failures of the inference listener are audited, once.
func TestAuthFailureAudit(t *testing.T) {
	up := fakeprovider.New(t)
	p := startBinary(t, opts{level: "info", up: up})
	tn := p.newCaller(t, "audited")
	if status, _ := p.chatCall(t, tn.key, fakeprovider.ModelBasic, false); status != 200 {
		t.Fatalf("the key does not work before it is revoked: %d", status)
	}
	p.mustCLI(t, tn.adminFile, "key", "revoke", "--tenant", tn.id, tn.keyID)

	rows := func() (n int, details []string) {
		t.Helper()
		r, err := p.db.Super(t).Query(t.Context(), `SELECT detail::text FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'auth.failure'`, tn.id)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		for r.Next() {
			var d string
			if err := r.Scan(&d); err != nil {
				t.Fatal(err)
			}
			details = append(details, d)
		}
		return len(details), details
	}
	testutil.Eventually(t, 10*time.Second, "the revoked key to be refused", func() bool {
		s, _ := p.chatCall(t, tn.key, fakeprovider.ModelBasic, false)
		return s == 401
	})
	for i := 0; i < 2; i++ {
		if s, text := p.chatCall(t, tn.key, fakeprovider.ModelBasic, false); s != 401 {
			t.Fatalf("a revoked key: %d\n%s", s, text)
		}
	}
	n, details := rows()
	if n != 1 {
		t.Fatalf("%d auth.failure rows for a revoked key presented several times, want one: %v", n, details)
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(details[0]), &d); err != nil || d["listener"] != "inference" || d["reason"] != "revoked" {
		t.Errorf("the detail: %s (%v)", details[0], err)
	}

	// An unknown key names no tenant: logged, not audited.
	unknown, _, _, err := authn.Generate(authn.KindTenant)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if s, _ := p.chatCall(t, unknown.Reveal(), fakeprovider.ModelBasic, false); s != 401 {
			t.Fatalf("an unknown key: %d", s)
		}
	}
	if n, _ := rows(); n != 1 {
		t.Errorf("an unknown key changed the log of the tenant: %d rows", n)
	}
	var total int
	if err := p.db.Super(t).QueryRow(t.Context(), `SELECT count(*) FROM pandokos.audit_event WHERE action = 'auth.failure'`).Scan(&total); err != nil || total != 1 {
		t.Errorf("%d auth.failure rows in all tenants, want 1 (%v)", total, err)
	}

	// The log lines say what happened to each failure.
	testutil.Eventually(t, 5*time.Second, "the authentication failed lines", func() bool { return len(completeLines(p.stdout, "authentication failed")) >= 5 })
	var audited []any
	var unknownAudited []any
	for _, l := range completeLines(p.stdout, "authentication failed") {
		switch l["reason"] {
		case "revoked":
			audited = append(audited, l["audited"])
		case "unknown_key":
			if v, ok := l["audited"]; ok {
				unknownAudited = append(unknownAudited, v)
			}
		}
	}
	if len(audited) < 3 || audited[0] != true || audited[1] != false || audited[2] != false {
		t.Errorf("audited on the lines of the revoked key: %v, want [true false false ...]", audited)
	}
	if len(unknownAudited) != 0 {
		t.Errorf("audited on the lines of an unknown key: %v", unknownAudited)
	}
	// Nothing of the key is in the audit log.
	var dump string
	if err := p.db.Super(t).QueryRow(t.Context(), `SELECT COALESCE(jsonb_agg(to_jsonb(t))::text, '') FROM pandokos.audit_event t`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	testutil.AssertAbsent(t, "audit_event", dump, testutil.KeyForms(tn.key)...)
}
