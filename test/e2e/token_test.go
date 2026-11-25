package e2e

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeidp"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/otlptest"
	"github.com/pandokos/pandokos/test/testutil"
)

const (
	openaiPath    = "/openai/v1/chat/completions"
	anthropicCred = "synthetic-credential-anthropic-token-0001"
)

// tokenProc is the binary with tokens configured against a fake issuer: the host of the issuer is a
// private host and the authority of its certificate is trusted. Everything the CLI printed is kept,
// for the canary test.
type tokenProc struct {
	*proc
	idp    *fakeidp.Server
	up     *fakeprovider.Server
	boot   string
	cliOut []string
}

// tokenEnv is the configuration that lets the process fetch the keys of the fake issuer.
func tokenEnv(idp *fakeidp.Server, extra ...string) []string {
	return append([]string{"PANDOKOS_TOKENS_PRIVATE_HOSTS=localhost", "PANDOKOS_UPSTREAM_TLS_CA_FILE=" + idp.CAFile()}, extra...)
}

func startTokenProc(t *testing.T, idp *fakeidp.Server, o opts) *tokenProc {
	t.Helper()
	if o.up == nil {
		o.up = fakeprovider.New(t)
	}
	if o.level == "" {
		o.level = "info"
	}
	o.env = append(tokenEnv(idp), o.env...)
	p := startBinary(t, o)
	w := &tokenProc{proc: p, idp: idp, up: o.up}
	w.boot = p.bootstrap(t)
	return w
}

// run is mustCLI that keeps the output.
func (w *tokenProc) run(t *testing.T, keyFile string, args ...string) string {
	t.Helper()
	code, out, errs := w.cli(t, keyFile, args...)
	w.cliOut = append(w.cliOut, out, errs)
	if code != 0 {
		t.Fatalf("%s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, out, errs)
	}
	return out
}

// tenantWith creates a tenant with a credential of openai and of anthropic and a service account with the role inference.
type tokenTenant struct {
	id, admin, sa string
}

func (w *tokenProc) tenant(t *testing.T, name string) tokenTenant {
	t.Helper()
	id, admin := w.newTenant(t, w.boot, name)
	w.prepareTenant(t, admin)
	w.addCredential(t, admin, "anthropic", anthropicCred)
	out := w.run(t, admin, "service-account", "create", "--tenant", id, "--role", "inference", "workload")
	return tokenTenant{id, admin, reID.FindStringSubmatch(out)[1]}
}

func (w *tokenProc) useAnthropic(t *testing.T) {
	t.Helper()
	w.prepareUpstream(t)
	w.useAnthropicFake(t, w.boot, w.up, fakeprovider.ModelBasic)
	w.up.Accept(anthropicCred, fixtures.CanaryCredential) // Accept replaces the set: both families' credentials
}

// registration registers the fake issuer for the tenant with the audience and returns its ID.
func (w *tokenProc) registration(t *testing.T, tn tokenTenant, name, audience string, extra ...string) string {
	t.Helper()
	out := w.run(t, tn.admin, append([]string{"token-issuer", "create", "--tenant", tn.id, "--name", name, "--issuer", w.idp.Issuer(), "--audience", audience}, extra...)...)
	return reID.FindStringSubmatch(out)[1]
}

// platformRegistration registers the fake issuer at platform level.
func (w *tokenProc) platformRegistration(t *testing.T, name, audience, claim string) string {
	t.Helper()
	out := w.run(t, w.boot, "platform-issuer", "create", "--name", name, "--issuer", w.idp.Issuer(), "--audience", audience, "--tenant-claim", claim)
	return reID.FindStringSubmatch(out)[1]
}

// mapping maps the subject of a registration to the tenant's service account and returns the ID of the mapping.
func (w *tokenProc) mapping(t *testing.T, tn tokenTenant, issuerID, subject string, platform bool) string {
	t.Helper()
	args := []string{"subject-mapping", "create", "--tenant", tn.id, "--issuer", issuerID, "--subject", subject, "--service-account", tn.sa}
	if platform {
		args = append(args, "--platform")
	}
	return reID.FindStringSubmatch(w.run(t, tn.admin, args...))[1]
}

func (w *tokenProc) sign(alg, sub string, aud any, f ...func(fakeidp.Claims) fakeidp.Claims) string {
	c := w.idp.Standard(sub, aud, 10*time.Minute)
	for _, m := range f {
		c = m(c)
	}
	return w.idp.Sign(alg, c)
}

func (w *tokenProc) openai(t *testing.T, token string, stream bool) (int, string) {
	t.Helper()
	return exchange(t, "POST", w.url(openaiPath), authHeader(token), chatBody("fake-basic", stream, fixtures.CanaryPrompt))
}

func (w *tokenProc) anthropic(t *testing.T, token string, bearer bool) (int, string) {
	t.Helper()
	return exchangeWith(t, w.client, "POST", w.url(anthropicPath), anthropicHeader(token, bearer), messagesBody(fakeprovider.ModelBasic, false))
}

// TestTokenCall is T-596: the binary, a fake issuer on a listed private host; tenant-bound and
// platform-level calls, streaming and not, through both families; the usage row is in the right
// tenant and the usage list of the API shows it.
func TestTokenCall(t *testing.T) {
	t.Parallel()
	idp := fakeidp.Start(t, fakeidp.Options{})
	w := startTokenProc(t, idp, opts{level: "debug"})
	w.useAnthropic(t)
	a, b, c := w.tenant(t, "token-a"), w.tenant(t, "token-b"), w.tenant(t, "token-c")

	// Tenant A: a tenant-bound registration and a mapping.
	issA := w.registration(t, a, "idp", "pandokos-e2e-a")
	mapA := w.mapping(t, a, issA, "workload-a", false)
	// Tenants B and C: a platform-level registration that lists B only; both map the subject.
	platform := w.platformRegistration(t, "shared", "pandokos-e2e-shared", "org")
	w.run(t, w.boot, "platform-issuer", "add-tenant", "--claim-value", "org-b", platform, b.id)
	mapB := w.mapping(t, b, platform, "svc", true)
	// Tenant C maps the same subject, but is not in the list: a mapping can only be written while
	// the platform-level registration lists the tenant (it is invisible otherwise), so C is listed
	// for the time of the mapping and removed again; the mapping stays.
	w.run(t, w.boot, "platform-issuer", "add-tenant", "--claim-value", "org-c-listed", platform, c.id)
	w.mapping(t, c, platform, "svc", true)
	w.run(t, w.boot, "platform-issuer", "remove-tenant", platform, c.id)

	tokenA := w.sign("ES256", "workload-a", "pandokos-e2e-a")
	tokenB := w.sign("ES256", "svc", "pandokos-e2e-shared", func(cl fakeidp.Claims) fakeidp.Claims { return cl.With("org", "org-b") })

	t.Run("tenant-bound, both families, streaming and not", func(t *testing.T) {
		for name, call := range map[string]func() (int, string){
			"openai":              func() (int, string) { return w.openai(t, tokenA, false) },
			"openai streaming":    func() (int, string) { return w.openai(t, tokenA, true) },
			"anthropic bearer":    func() (int, string) { return w.anthropic(t, tokenA, true) },
			"anthropic X-Api-Key": func() (int, string) { return w.anthropic(t, tokenA, false) },
		} {
			if s, text := call(); s != 200 {
				t.Errorf("%s: %d\n%s", name, s, text)
			}
		}
		rows := waitRows(t, w.db, a.id, 4)
		for _, r := range rows {
			if r["principal_kind"] != "token" || r["principal_id"] != a.sa || r["key_id"] != mapA || r["outcome"] != "ok" || r["tenant_id"] != a.id {
				t.Errorf("usage row: %v", r)
			}
		}
	})

	t.Run("platform-level: the claim designates the tenant", func(t *testing.T) {
		for name, call := range map[string]func() (int, string){
			"openai":    func() (int, string) { return w.openai(t, tokenB, false) },
			"streaming": func() (int, string) { return w.openai(t, tokenB, true) },
			"anthropic": func() (int, string) { return w.anthropic(t, tokenB, true) },
		} {
			if s, text := call(); s != 200 {
				t.Errorf("%s: %d\n%s", name, s, text)
			}
		}
		rows := waitRows(t, w.db, b.id, 3)
		for _, r := range rows {
			if r["principal_kind"] != "token" || r["principal_id"] != b.sa || r["key_id"] != mapB || r["tenant_id"] != b.id {
				t.Errorf("usage row of B: %v", r)
			}
		}
		// Tenant C maps the same subject but is not in the list: nothing of the claim reaches it, and it has no usage.
		for _, claim := range []string{c.id, "org-c", "token-c"} {
			tok := w.sign("ES256", "svc", "pandokos-e2e-shared", func(cl fakeidp.Claims) fakeidp.Claims { return cl.With("org", claim) })
			if s, _ := w.openai(t, tok, false); s != 401 {
				t.Errorf("claim %q: %d, want 401", claim, s)
			}
		}
		if n := len(usageRows(t, w.db, c.id)); n != 0 {
			t.Errorf("tenant C has %d usage rows", n)
		}
		// A's rows are untouched by B's calls.
		if n := len(usageRows(t, w.db, a.id)); n != 4 {
			t.Errorf("tenant A has %d usage rows", n)
		}
	})

	t.Run("the usage list of the API shows the calls", func(t *testing.T) {
		items := w.usageList(t, a.admin, a.id)
		if len(items) != 4 {
			t.Fatalf("%d records listed, want 4", len(items))
		}
		for _, it := range items {
			if it.PrincipalKind != "token" || it.PrincipalID != a.sa || it.KeyID != mapA {
				t.Errorf("listed record: %+v", it)
			}
		}
		if got := w.usageList(t, b.admin, b.id); len(got) != 3 {
			t.Errorf("tenant B lists %d records", len(got))
		}
	})

	t.Run("a refused token never reaches the provider, and a key still works", func(t *testing.T) {
		before := len(w.up.Requests())
		for name, tok := range map[string]string{
			"an unmapped subject": w.sign("ES256", "stranger", "pandokos-e2e-a"),
			"another audience":    w.sign("ES256", "workload-a", "pandokos-e2e-other"),
			"an expired token": w.sign("ES256", "workload-a", "pandokos-e2e-a", func(cl fakeidp.Claims) fakeidp.Claims {
				return cl.With("iat", time.Now().Add(-2*time.Hour).Unix()).With("exp", time.Now().Add(-time.Hour).Unix())
			}),
			"none": w.idp.Raw(map[string]any{"alg": "none"}, w.idp.Standard("workload-a", "pandokos-e2e-a", time.Minute), nil) + "c2ln",
		} {
			if s, text := w.openai(t, tok, false); s != 401 || !strings.Contains(text, "X-Pandokos-Error: unauthenticated") {
				t.Errorf("%s: %d\n%s", name, s, text)
			}
		}
		if n := len(w.up.Requests()) - before; n != 0 {
			t.Errorf("%d requests reached the provider", n)
		}
		_, key := w.newInferenceKey(t, a.admin, a.id)
		if s, text := w.openai(t, key, false); s != 200 {
			t.Errorf("a virtual key: %d\n%s", s, text)
		}
	})

	t.Run("the token of a tenant that is suspended is refused by the decision", func(t *testing.T) {
		w.run(t, w.boot, "tenant", "suspend", "--reason", "test", c.id)
		w.run(t, w.boot, "platform-issuer", "add-tenant", "--claim-value", "org-c", platform, c.id)
		tok := w.sign("ES256", "svc", "pandokos-e2e-shared", func(cl fakeidp.Claims) fakeidp.Claims { return cl.With("org", "org-c") })
		testutil.Eventually(t, 5*time.Second, "the suspended tenant to be refused", func() bool {
			s, text := w.openai(t, tok, false)
			return s == 403 && strings.Contains(text, "tenant_suspended")
		})
	})
}

// TestTokenCanary is T-597 (the binary): at log level debug, no segment of any presented token and
// nothing of the key set of the issuer is in the output of the process, a response, an audit row, a
// usage row, an exported span, the forwarded request or the output of the CLI.
func TestTokenCanary(t *testing.T) {
	t.Parallel()
	idp := fakeidp.Start(t, fakeidp.Options{})
	col := otlptest.New(t)
	w := startTokenProc(t, idp, opts{level: "debug", collector: col, otlpEndpoint: col.URL().String(), propagate: true})
	w.useAnthropic(t)
	a := w.tenant(t, "canary-token")
	iss := w.registration(t, a, "idp", "pandokos-canary")
	w.mapping(t, a, iss, "workload", false)
	other := fakeidp.Start(t, fakeidp.Options{})

	good := []string{w.sign("ES256", "workload", "pandokos-canary"), w.sign("RS256", "workload", "pandokos-canary"), w.sign("ES256", "workload", "pandokos-canary")}
	bad := []string{
		w.sign("ES256", "stranger", "pandokos-canary"),
		other.Sign("ES256", other.Standard("workload", "pandokos-canary", time.Minute).With("iss", idp.Issuer())),
		w.idp.Raw(map[string]any{"alg": "ES256", "kid": idp.KeyID("ES256")}, idp.Standard("workload", "pandokos-canary", time.Minute), other.SignWith("ES256")),
		w.idp.Raw(map[string]any{"alg": "none"}, idp.Standard("workload", "pandokos-canary", time.Minute), nil) + "c2lnbmF0dXJl",
		w.sign("ES256", "workload", "pandokos-canary", func(c fakeidp.Claims) fakeidp.Claims {
			return c.With("iat", time.Now().Add(-2*time.Hour).Unix()).With("exp", time.Now().Add(-time.Hour).Unix())
		}),
		other.Sign("ES256", other.Standard("workload", "pandokos-unregistered", time.Minute)),
	}
	var responses []string
	for i, tok := range append(append([]string(nil), good...), bad...) {
		var s int
		var text string
		switch i % 3 {
		case 0:
			s, text = w.openai(t, tok, i%2 == 0)
		case 1:
			s, text = w.anthropic(t, tok, true)
		default:
			s, text = w.anthropic(t, tok, false)
		}
		if (i < len(good)) != (s == 200) {
			t.Errorf("token %d: status %d", i, s)
		}
		responses = append(responses, text)
	}
	rows := waitRows(t, w.db, a.id, len(good))
	if code := w.stop(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	var all strings.Builder
	for _, s := range []string{w.stdout.String(), w.stderr.String(), strings.Join(responses, "\n"), strings.Join(w.cliOut, "\n"), fmt.Sprint(rows)} {
		all.WriteString(s)
		all.WriteString("\n")
	}
	for _, r := range w.up.Requests() {
		all.WriteString(fmt.Sprintf("%v %s\n", r.Header, r.Body))
	}
	for _, r := range col.Requests() {
		all.WriteString(fmt.Sprintf("%v %s\n", r.Header, r.Body))
	}
	super := w.db.Super(t)
	rs, err := super.Query(ctxT(t), `SELECT to_jsonb(a)::text FROM pandokos.audit_event a UNION ALL SELECT to_jsonb(p)::text FROM pandokos.platform_audit_event p`)
	if err != nil {
		t.Fatal(err)
	}
	for rs.Next() {
		var s string
		if err := rs.Scan(&s); err != nil {
			t.Fatal(err)
		}
		all.WriteString(s + "\n")
	}
	rs.Close()
	text := all.String()

	checked := 0
	for _, tok := range append(append([]string(nil), good...), bad...) {
		for _, seg := range append(strings.Split(tok, "."), tok) {
			if len(seg) < 16 {
				continue
			}
			checked++
			if strings.Contains(text, seg) {
				t.Errorf("a segment of a presented token is in the output: %.30s", seg)
			}
		}
	}
	if checked < 30 {
		t.Errorf("only %d segments searched", checked)
	}
	if strings.Contains(text, string(idp.JWKS())) {
		t.Error("the key set of the issuer is in the output")
	}
	for _, piece := range strings.Split(string(idp.JWKS()), `"`) {
		if len(piece) >= 40 && strings.Contains(text, piece) {
			t.Errorf("key material of the issuer is in the output: %.30s", piece)
		}
	}
	for _, want := range []string{"authentication failed", "issuer keys fetched"} {
		if !strings.Contains(w.stdout.String(), want) {
			t.Errorf("the process log has no %q line: the test did not look at the output it should", want)
		}
	}
	testutil.AssertNoCanary(t, "process output", w.stdout.String()+w.stderr.String())
}

// TestTokenIssuerOutage is T-598: with a warm cache the issuer is stopped and calls keep succeeding;
// a new process without keys answers 503, never 401, and never forwards; when the issuer returns, calls
// succeed again within the refetch interval.
func TestTokenIssuerOutage(t *testing.T) {
	t.Parallel()
	idp := fakeidp.Start(t, fakeidp.Options{})
	env := []string{"PANDOKOS_TOKENS_JWKS_MIN_REFETCH_INTERVAL=1s"}
	w := startTokenProc(t, idp, opts{level: "info", env: env})
	a := w.tenant(t, "outage")
	iss := w.registration(t, a, "idp", "pandokos-outage")
	w.mapping(t, a, iss, "workload", false)
	tok := func() string { return w.sign("ES256", "workload", "pandokos-outage") }

	if s, text := w.openai(t, tok(), false); s != 200 {
		t.Fatalf("a warm call: %d\n%s", s, text)
	}
	idp.Stop()
	for i := 0; i < 5; i++ {
		if s, text := w.openai(t, tok(), i%2 == 0); s != 200 {
			t.Fatalf("call %d with the issuer stopped: %d\n%s", i, s, text)
		}
	}
	// A new token with a new subject still verifies: the keys are cached, not the tokens.
	d, j := idp.Requests()

	// A new process on the same database has no keys.
	second := startBinary(t, opts{level: "info", up: w.up, db: w.db, noMigrate: true, env: tokenEnv(idp, env...)})
	before := len(w.up.Requests())
	for i := 0; i < 3; i++ {
		s, text := exchange(t, "POST", second.url(openaiPath), authHeader(tok()), chatBody("fake-basic", false, "x"))
		if s != 503 || !strings.Contains(text, "X-Pandokos-Error: unavailable") {
			t.Errorf("a process without keys: %d, want 503 unavailable\n%s", s, text)
		}
	}
	if n := len(w.up.Requests()) - before; n != 0 {
		t.Errorf("%d requests were forwarded although the token could not be verified", n)
	}
	if d2, j2 := idp.Requests(); d2 != d || j2 != j {
		t.Errorf("the stopped issuer received requests: %d,%d then %d,%d", d, j, d2, j2)
	}
	testutil.Eventually(t, 5*time.Second, "the log of the failed fetch", func() bool {
		for _, l := range completeLines(second.stdout, "issuer keys not fetched") {
			if l["class"] == "network" {
				return true
			}
		}
		return false
	})
	// The issuer returns: calls succeed again within the refetch interval.
	idp.Resume()
	testutil.Eventually(t, 5*time.Second, "calls to succeed again", func() bool {
		s, _ := exchange(t, "POST", second.url(openaiPath), authHeader(tok()), chatBody("fake-basic", false, "x"))
		return s == 200
	})
	if s, text := w.openai(t, tok(), false); s != 200 {
		t.Errorf("the first process after the return: %d\n%s", s, text)
	}
}

// TestTokenRevocation is T-599: deleting the mapping, disabling the registration and removing the tenant
// from the list each refuse the next call within the bounds of section 5.6: with notifications, and through
// PgBouncer in transaction mode without the direct address, within cache.principal_ttl.
func TestTokenRevocation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		setup    func(t *testing.T) (*dbtest.Database, []string)
		ttl      string
		deadline time.Duration
	}{
		{"direct, with notifications", func(t *testing.T) (*dbtest.Database, []string) { return dbtest.NewMigrated(t), nil }, "60s", 2 * time.Second},
		{"through the pooler without the direct address", func(t *testing.T) (*dbtest.Database, []string) {
			d := dbtest.NewMigrated(t)
			pooled := d.Pooled(t)
			return pooled, nil
		}, "2s", 2500 * time.Millisecond},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, extra := c.setup(t)
			idp := fakeidp.Start(t, fakeidp.Options{})
			w := startTokenProc(t, idp, opts{level: "info", db: d, noMigrate: true, env: append([]string{"PANDOKOS_CACHE_PRINCIPAL_TTL=" + c.ttl}, extra...)})
			a := w.tenant(t, "revoked")
			issMap := w.registration(t, a, "by-mapping", "aud-mapping")
			mapID := w.mapping(t, a, issMap, "workload", false)
			issOff := w.registration(t, a, "by-disable", "aud-disable")
			w.mapping(t, a, issOff, "workload", false)
			platform := w.platformRegistration(t, "shared", "aud-shared", "org")
			w.run(t, w.boot, "platform-issuer", "add-tenant", "--claim-value", "org-a", platform, a.id)
			w.mapping(t, a, platform, "workload", true)
			tokens := map[string]func() string{
				"mapping": func() string { return w.sign("ES256", "workload", "aud-mapping") },
				"disable": func() string { return w.sign("ES256", "workload", "aud-disable") },
				"list": func() string {
					return w.sign("ES256", "workload", "aud-shared", func(cl fakeidp.Claims) fakeidp.Claims { return cl.With("org", "org-a") })
				},
			}
			status := func(name string) int { s, _ := w.openai(t, tokens[name](), false); return s }
			for name := range tokens {
				if s := status(name); s != 200 {
					t.Fatalf("a good token (%s): %d", name, s)
				}
			}
			refused := func(name, what string) {
				t.Helper()
				testutil.Eventually(t, c.deadline+500*time.Millisecond, what, func() bool { return status(name) == 401 })
			}
			stillWorks := func(names ...string) {
				t.Helper()
				for _, name := range names {
					if s := status(name); s != 200 {
						t.Errorf("the token %s was refused by a change that was not about it: %d", name, s)
					}
				}
			}
			w.run(t, a.admin, "subject-mapping", "delete", "--tenant", a.id, mapID)
			refused("mapping", "the token of the deleted mapping to be refused")
			stillWorks("disable", "list")

			w.run(t, a.admin, "token-issuer", "update", "--tenant", a.id, "--disable", issOff)
			refused("disable", "the token of the disabled registration to be refused")
			stillWorks("list")

			w.run(t, w.boot, "platform-issuer", "remove-tenant", platform, a.id)
			refused("list", "the token of the tenant that left the list to be refused")

			// Enabling again brings the registration back, within the same bound.
			w.run(t, a.admin, "token-issuer", "update", "--tenant", a.id, "--enable", issOff)
			testutil.Eventually(t, c.deadline+500*time.Millisecond, "the enabled registration to work", func() bool { return status("disable") == 200 })
		})
	}
}

// TestTokenSSRF is T-600: a registration whose issuer is on the loopback, in a private range or at the
// metadata address is a 503 with the class blocked in the log, and the target received no request; listing
// the host admits the private ones only.
func TestTokenSSRF(t *testing.T) {
	t.Parallel()
	idp := fakeidp.Start(t, fakeidp.Options{}) // the loopback target: it is also a valid issuer, which makes the test sharp
	d := dbtest.NewMigrated(t)
	up := fakeprovider.New(t)
	// The first process lists no host.
	first := startBinary(t, opts{level: "info", up: up, db: d, noMigrate: true, env: []string{"PANDOKOS_UPSTREAM_TLS_CA_FILE=" + idp.CAFile()}})
	w := &tokenProc{proc: first, idp: idp, up: up}
	w.boot = first.bootstrap(t)
	a := w.tenant(t, "ssrf")

	_, port, _ := net.SplitHostPort(idp.Host())
	targets := map[string]string{
		"loopback":                "https://localhost:" + port + "/",
		"a private range":         "https://10.255.255.1/",
		"another private range":   "https://192.168.255.1/realms/x",
		"the metadata address":    "https://169.254.169.254/",
		"the metadata address v6": "https://[fd00:ec2::254]/",
	}
	tokens := map[string]string{}
	for name, issuer := range targets {
		aud := "aud-" + strings.ReplaceAll(name, " ", "-")
		out := w.run(t, a.admin, "token-issuer", "create", "--tenant", a.id, "--name", strings.ReplaceAll(name, " ", "-"), "--issuer", issuer, "--audience", aud)
		iss := reID.FindStringSubmatch(out)[1]
		w.mapping(t, a, iss, "workload", false)
		tokens[name] = idp.Sign("ES256", idp.Standard("workload", aud, 10*time.Minute).With("iss", issuer))
	}
	dBefore, jBefore := idp.Requests()
	for name, tok := range tokens {
		s, text := exchange(t, "POST", first.url(openaiPath), authHeader(tok), chatBody("fake-basic", false, "x"))
		if s != 503 || !strings.Contains(text, "X-Pandokos-Error: unavailable") {
			t.Errorf("%s: %d, want 503\n%s", name, s, text)
		}
	}
	if d2, j2 := idp.Requests(); d2 != dBefore || j2 != jBefore {
		t.Errorf("the loopback target received requests: %d,%d then %d,%d", dBefore, jBefore, d2, j2)
	}
	if n := len(up.Requests()); n != 0 {
		t.Errorf("%d requests reached the provider", n)
	}
	testutil.Eventually(t, 5*time.Second, "the blocked fetches in the log", func() bool {
		blocked := 0
		for _, l := range completeLines(first.stdout, "issuer keys not fetched") {
			if l["class"] == "blocked" {
				blocked++
			}
		}
		return blocked >= len(targets)
	})
	for _, l := range completeLines(first.stdout, "issuer keys not fetched") {
		if l["class"] != "blocked" {
			t.Errorf("a fetch failed with the class %v, want blocked: %v", l["class"], l)
		}
	}
	if strings.Contains(first.stdout.String(), "10.255.255.1") || strings.Contains(first.stdout.String(), "169.254.169.254") {
		t.Errorf("the log names the address of a blocked fetch\n%s", first.stdout.String())
	}

	// A second process on the same database lists the host: the loopback issuer works, the others stay blocked.
	second := startBinary(t, opts{level: "info", up: up, db: d, noMigrate: true, env: tokenEnv(idp)})
	for name, tok := range tokens {
		s, text := exchange(t, "POST", second.url(openaiPath), authHeader(tok), chatBody("fake-basic", false, "x"))
		want := 503
		if name == "loopback" {
			want = 200
		}
		if s != want {
			t.Errorf("%s with the host listed: %d, want %d\n%s", name, s, want, text)
		}
	}
	if d2, j2 := idp.Requests(); d2 != dBefore+1 || j2 != jBefore+1 {
		t.Errorf("the listed issuer received %d discovery and %d key set requests, want 1 and 1", d2-dBefore, j2-jBefore)
	}
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return c
}
