package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeidp"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/otlptest"
	"github.com/pandokos/pandokos/test/testutil"
)

var rePasswordLine = regexp.MustCompile(`(?m)^password: ([a-z2-7]{24})$`)

// browser returns a browser of the administration listener of the process.
func (p *proc) browser() *apitest.Browser {
	return apitest.NewBrowserAt(p.adminURL(""), p.client.Transport)
}

// initAdmin runs init --admin-username and returns the generated password.
func initAdmin(t *testing.T, env []string, username string, more ...string) string {
	t.Helper()
	code, out, errs := runBinary(t, env, append([]string{"init", "--admin-username", username}, more...)...)
	if code != 0 {
		t.Fatalf("init --admin-username %s: exit %d\nstdout: %s\nstderr: %s", username, code, out, errs)
	}
	m := rePasswordLine.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no password in the output of init:\n%s", out)
	}
	return m[1]
}

func get[T any](t *testing.T, r *apitest.Response, status int) T {
	t.Helper()
	var v T
	if r.Status != status {
		t.Fatalf("status %d, want %d\n%s", r.Status, status, r.Body)
	}
	r.JSON(t, &v)
	return v
}

// freeAddr returns a loopback address that no one listens on.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// oidcProc is the binary with the identity provider on, against a fake issuer on a listed private host.
type oidcProc struct {
	*proc
	idp *fakeidp.Server
	o   *apitest.OIDC
}

// startOIDC starts the process with the relying party configured for the fake issuer; the
// administration listener has a fixed address, which auth.public_origin names.
func startOIDC(t *testing.T, d *dbtest.Database, idp *fakeidp.Server, o opts, env ...string) *oidcProc {
	t.Helper()
	addr := freeAddr(t)
	idp.RegisterRedirectURI("http://" + addr + "/v1/auth/oidc/callback")
	o.db, o.noMigrate = d, true
	if o.level == "" {
		o.level = "info"
	}
	o.env = append(append([]string{
		"PANDOKOS_LISTENERS_ADMIN_ADDRESS=" + addr, "PANDOKOS_AUTH_PUBLIC_ORIGIN=http://" + addr,
		"PANDOKOS_AUTH_OIDC_ENABLED=true", "PANDOKOS_AUTH_OIDC_ISSUER=" + idp.Issuer(), "PANDOKOS_AUTH_OIDC_CLIENT_ID=" + idp.ClientID(),
		"PANDOKOS_AUTH_OIDC_CLIENT_SECRET_FILE=" + testutil.WriteSecret(t, "oidc-client", idp.ClientSecret()),
		"PANDOKOS_AUTH_OIDC_PRIVATE_HOSTS=localhost", "PANDOKOS_UPSTREAM_TLS_CA_FILE=" + idp.CAFile(),
	}, env...), o.env...)
	p := startBinary(t, o)
	return &oidcProc{proc: p, idp: idp, o: &apitest.OIDC{IDP: idp, Post: "/"}}
}

// TestLocalAccountExit is T-831, the exit of the sprint: the binary and PostgreSQL only.
func TestLocalAccountExit(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	env := baseEnv(t, d.Env(t))
	password := initAdmin(t, env, "root.admin")
	p := startBinary(t, opts{level: "info", db: d, noMigrate: true})

	b := p.browser()
	first := get[adminapi.Session](t, b.SignIn(t, "root.admin", password), 200)
	if !first.MustChangePassword || first.User.Username != "root.admin" {
		t.Fatalf("the first sign-in: %+v", first)
	}
	// The forced change: nothing but the three operations until the password is changed.
	if r := b.Do(t, "GET", "/v1/tenants", nil, nil); r.Status != 403 || r.Problem() != "password_change_required" {
		t.Errorf("before the change: %d %s", r.Status, r.Body)
	}
	newPassword := dbtest.NewPassword(t)
	changed := get[adminapi.Session](t, b.Do(t, "POST", "/v1/auth/password", map[string]any{"current_password": password, "new_password": newPassword}, nil), 200)
	if changed.MustChangePassword {
		t.Fatal("the session still has to change its password")
	}

	// The administrator creates a tenant and a platform user.
	created := get[adminapi.TenantCreated](t, b.Do(t, "POST", "/v1/tenants", map[string]any{"name": "acme"}, nil), 201)
	if created.AdminKey == nil || created.AdminKey.Secret == "" {
		t.Fatalf("the tenant was created without a first key: %+v", created)
	}
	second := get[adminapi.UserCreated](t, b.Do(t, "POST", "/v1/users", map[string]any{"sign_in": "local", "username": "second.admin", "platform_roles": []string{"platform-admin"}}, nil), 201)
	if second.InitialPassword == "" {
		t.Fatal("no initial password for the platform user")
	}

	// As tenant administrator (the key of the first service account) it creates a member.
	hdr := http.Header{"Authorization": {"Bearer " + created.AdminKey.Secret}}
	member := get[adminapi.MemberCreated](t, p.browser().Do(t, "POST", "/v1/tenants/"+created.Tenant.ID+"/members",
		map[string]any{"account": "new", "username": "acme.member", "roles": []string{"tenant-admin"}}, hdr), 201)
	if member.Member.ManagedBy != "tenant" || member.InitialPassword == "" {
		t.Fatalf("member %+v", member)
	}

	// The member must change the generated password and then administers its tenant.
	mb := p.browser()
	in := get[adminapi.Session](t, mb.SignIn(t, "acme.member", member.InitialPassword), 200)
	if !in.MustChangePassword {
		t.Error("the generated password does not have to be changed")
	}
	path := "/v1/tenants/" + created.Tenant.ID + "/service-accounts"
	if r := mb.Do(t, "POST", path, map[string]any{"name": "before-change", "roles": []string{"inference"}}, nil); r.Status != 403 || r.Problem() != "password_change_required" {
		t.Errorf("before the change: %d %s", r.Status, r.Body)
	}
	memberPassword := dbtest.NewPassword(t)
	get[adminapi.Session](t, mb.Do(t, "POST", "/v1/auth/password", map[string]any{"current_password": member.InitialPassword, "new_password": memberPassword}, nil), 200)
	if r := mb.Do(t, "POST", path, map[string]any{"name": "after-change", "roles": []string{"inference"}}, nil); r.Status != 201 {
		t.Errorf("the member administers its tenant: %d %s", r.Status, r.Body)
	}
	me := get[adminapi.Me](t, mb.Do(t, "GET", "/v1/me", nil, nil), 200)
	if me.PrincipalType != "user" || len(me.Memberships) != 1 || me.Memberships[0].Tenant.ID != created.Tenant.ID {
		t.Errorf("me: %+v", me)
	}
	// The platform user does the same on the platform.
	sb := p.browser()
	if s := get[adminapi.Session](t, sb.SignIn(t, "second.admin", second.InitialPassword), 200); !s.MustChangePassword {
		t.Error("the platform user does not have to change its password")
	}
	if code := p.stop(t); code != 0 {
		t.Errorf("exit code %d", code)
	}
}

// TestLockExit is T-832: ten wrong passwords lock the account, whatever the eleventh says.
func TestLockExit(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	env := baseEnv(t, d.Env(t))
	password := initAdmin(t, env, "lock.admin")
	p := startBinary(t, opts{level: "info", db: d, noMigrate: true})
	b := p.browser()
	for i := 0; i < 10; i++ {
		d.Age(t, "signin_throttle", "", time.Minute, "last_failure_at") // the delays are not the point
		if r := b.SignIn(t, "lock.admin", "wrong password number "+fmt.Sprint(i)); r.Status != 401 {
			t.Fatalf("wrong password %d: %d %s", i, r.Status, r.Body)
		}
	}
	r := b.SignIn(t, "lock.admin", password)
	if r.Status != 429 || r.Problem() != "too_many_attempts" || r.Header.Get("Retry-After") == "" {
		t.Fatalf("the eleventh attempt: %d %s", r.Status, r.Body)
	}
	fails, locks := 0, 0
	for _, row := range d.PlatformAuditRows(t) {
		switch row.Action {
		case "auth.failure":
			fails++
		case "user.lock":
			locks++
		}
	}
	if fails != 10 || locks != 1 {
		t.Errorf("the audit log holds %d failures and %d locks, want 10 and 1", fails, locks)
	}
	// The host command unlocks it.
	if code, out, errs := runBinary(t, env, "user", "unlock", "lock.admin"); code != 0 {
		t.Fatalf("user unlock: %d %s %s", code, out, errs)
	}
	if r := b.SignIn(t, "lock.admin", password); r.Status != 200 {
		t.Errorf("sign-in after the unlock: %d %s", r.Status, r.Body)
	}
	unlocked := false
	for _, row := range d.PlatformAuditRows(t) {
		unlocked = unlocked || (row.Action == "user.unlock" && row.ActorType == "host")
	}
	if !unlocked {
		t.Error("the unlock of the host is not audited")
	}
}

// TestSessionNotOnInference is T-833: a session is not a credential of the inference listener.
func TestSessionNotOnInference(t *testing.T) {
	t.Parallel()
	up := fakeprovider.New(t)
	d := dbtest.NewMigrated(t)
	env := baseEnv(t, d.Env(t))
	// init issues the bootstrap key too: the process uses it for the tenant of the control request,
	// since a second init without --recover is refused.
	platformFile := filepath.Join(t.TempDir(), "bootstrap.key")
	password := initAdmin(t, env, "infer.admin", "--key-out", platformFile)
	p := startBinary(t, opts{level: "info", up: up, db: d, noMigrate: true})
	p.platformFile = platformFile
	b := p.browser()
	get[adminapi.Session](t, b.SignIn(t, "infer.admin", password), 200)
	token := b.Token()
	if token == "" {
		t.Fatal("no session cookie")
	}
	good := p.inferenceKey(t) // a valid virtual key, for the control

	cookie := http.Header{"Cookie": {"__Host-pandokos_session=" + token}, "Content-Type": {"application/json"}}
	wrongKey, _, _, err := authn.Generate(authn.KindTenant)
	if err != nil {
		t.Fatal(err)
	}
	for name, hdr := range map[string]http.Header{
		"a session cookie alone":              cookie,
		"a session cookie beside a wrong key": {"Cookie": {"__Host-pandokos_session=" + token}, "Authorization": {"Bearer " + wrongKey.Reveal()}, "Content-Type": {"application/json"}},
		"a session token as the bearer":       authHeader(token),
		"a session token as X-Api-Key":        {"X-Api-Key": {token}, "Content-Type": {"application/json"}},
	} {
		for _, path := range []string{"/openai/v1/chat/completions", "/anthropic/v1/messages"} {
			body := chatBody("fake-basic", false, fixtures.CanaryPrompt)
			if strings.HasPrefix(path, "/anthropic") {
				body = messagesBody(fakeprovider.ModelBasic, false)
			}
			status, text := exchangeWith(t, p.client, "POST", p.url(path), hdr.Clone(), body)
			if status != 401 {
				t.Errorf("%s on %s: %d\n%s", name, path, status, text)
			}
			if strings.Contains(text, token) {
				t.Errorf("%s on %s: the response holds the token", name, path)
			}
		}
	}
	// The administration listener: the token in the headers of a key is no credential either.
	for name, hdr := range map[string]http.Header{"bearer": {"Authorization": {"Bearer " + token}}, "X-Api-Key": {"X-Api-Key": {token}}} {
		if status, _ := exchange(t, "GET", p.adminURL("/v1/me"), hdr, nil); status != 401 {
			t.Errorf("a session token as %s on the administration listener: %d", name, status)
		}
	}
	if n := len(up.Requests()); n != 0 {
		t.Errorf("%d requests were forwarded to the provider", n)
	}
	// The control: a real key still calls the provider.
	if status, text := exchange(t, "POST", p.url("/openai/v1/chat/completions"), authHeader(good), chatBody("fake-basic", false, fixtures.CanaryPrompt)); status != 200 {
		t.Errorf("a virtual key: %d\n%s", status, text)
	}
}

// TestDisableExit is T-834: a user that is disabled is out at its next request, also through PgBouncer
// in transaction mode.
func TestDisableExit(t *testing.T) {
	t.Parallel()
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		env := baseEnv(t, d.Env(t))
		password := initAdmin(t, env, "disable.admin")
		p := startBinary(t, opts{level: "info", db: d, noMigrate: true})
		admin := p.browser()
		get[adminapi.Session](t, admin.SignIn(t, "disable.admin", password), 200)
		get[adminapi.Session](t, admin.Do(t, "POST", "/v1/auth/password", map[string]any{"current_password": password, "new_password": dbtest.NewPassword(t)}, nil), 200)

		created := get[adminapi.UserCreated](t, admin.Do(t, "POST", "/v1/users", map[string]any{"sign_in": "local", "username": "to.disable", "platform_roles": []string{"platform-admin"}}, nil), 201)
		victim := p.browser()
		get[adminapi.Session](t, victim.SignIn(t, "to.disable", created.InitialPassword), 200)
		if r := victim.Do(t, "GET", "/v1/me", nil, nil); r.Status != 200 {
			t.Fatalf("the user before it is disabled: %d", r.Status)
		}
		if r := admin.Do(t, "POST", "/v1/users/"+created.User.ID+"/disable", map[string]any{}, nil); r.Status != 200 {
			t.Fatalf("disable: %d %s", r.Status, r.Body)
		}
		if r := victim.Do(t, "GET", "/v1/me", nil, nil); r.Status != 401 {
			t.Errorf("the request after the user was disabled: %d %s", r.Status, r.Body)
		}
		if r := p.browser().SignIn(t, "to.disable", created.InitialPassword); r.Status != 401 {
			t.Errorf("a disabled user signs in: %d", r.Status)
		}
	})
}

// TestLocalDisabled is T-835: with auth.local.enabled false and the identity provider, only the provider signs in.
func TestLocalDisabled(t *testing.T) {
	t.Parallel()
	idp := fakeidp.Start(t, fakeidp.Options{Users: map[string]fakeidp.Claims{"ada": {"name": "Ada", "email": "ada@example.test"}}})
	idp.SignInAs("ada")
	d := dbtest.NewMigrated(t)
	ada := d.FederatedUser(t, idp.Issuer(), "ada", []string{"platform-admin"})
	d.PlatformUser(t, "local.person", []string{"platform-admin"})
	p := startOIDC(t, d, idp, opts{}, "PANDOKOS_AUTH_LOCAL_ENABLED=false")

	b := p.browser()
	m := get[adminapi.AuthMethods](t, b.Do(t, "GET", "/v1/auth/methods", nil, nil), 200)
	if m.Local.Enabled || !m.Oidc.Enabled || m.Oidc.StartPath != "/v1/auth/oidc/start" {
		t.Errorf("methods %+v", m)
	}
	if r := b.SignIn(t, "local.person", "whatever password 1234"); r.Status != 403 || r.Problem() != "sign_in_disabled" {
		t.Errorf("password sign-in: %d %s", r.Status, r.Body)
	}
	hops := p.o.SignIn(t, b)
	if end := hops[len(hops)-1]; end.Status != 303 || end.Location != "/" || b.Token() == "" {
		t.Fatalf("the sign-in through the provider ended with %d %q", end.Status, end.Location)
	}
	if me := get[adminapi.Me](t, b.Do(t, "GET", "/v1/me", nil, nil), 200); me.ID != ada.ID || me.Session == nil || me.Session.Method != "oidc" {
		t.Errorf("me %+v", me)
	}
	if r := b.Do(t, "POST", "/v1/users", map[string]any{"sign_in": "local", "username": "no.local.user"}, nil); r.Status != 409 || r.Problem() != "invalid_state" {
		t.Errorf("a local user while local accounts are off: %d %s", r.Status, r.Body)
	}
	if r := b.Do(t, "POST", "/v1/users", map[string]any{"sign_in": "oidc", "subject": "grace"}, nil); r.Status != 201 {
		t.Errorf("a federated user: %d %s", r.Status, r.Body)
	}
}

// TestOidcExit is T-836: the binary and the fake issuer; group rules; the provider stopped.
func TestOidcExit(t *testing.T) {
	t.Parallel()
	idp := fakeidp.Start(t, fakeidp.Options{Users: map[string]fakeidp.Claims{
		"ada":   {"name": "Ada", "email": "ada@example.test", "groups": []string{"eng"}},
		"grace": {"name": "Grace", "email": "grace@example.test", "groups": []string{"unmapped"}},
	}})
	d := dbtest.NewMigrated(t)
	env := baseEnv(t, d.Env(t))
	boot := t.TempDir() + "/boot.key"
	if code, out, errs := runBinary(t, env, "init", "--key-out", boot); code != 0 {
		t.Fatalf("init: %d %s %s", code, out, errs)
	}
	// The tenant that the rule names exists before the process starts.
	tenantID := ""
	{
		tmp := startBinary(t, opts{level: "info", db: d, noMigrate: true})
		out := tmp.mustCLI(t, boot, "tenant", "create", "acme")
		tenantID = reID.FindStringSubmatch(out)[1]
		if code := tmp.stop(t); code != 0 {
			t.Fatalf("exit %d", code)
		}
	}
	p := startOIDC(t, d, idp, opts{}, "PANDOKOS_AUTH_OIDC_GROUP_MAPPINGS=tenant:acme:tenant-admin=eng", "PANDOKOS_AUTH_OIDC_AUTO_PROVISION=true")

	idp.SignInAs("ada")
	b := p.browser()
	hops := p.o.SignIn(t, b)
	if end := hops[len(hops)-1]; end.Location != "/" || b.Token() == "" {
		t.Fatalf("the sign-in ended with %d %q", end.Status, end.Location)
	}
	me := get[adminapi.Me](t, b.Do(t, "GET", "/v1/me", nil, nil), 200)
	if len(me.Memberships) != 1 || me.Memberships[0].Tenant.ID != tenantID || strings.Join(me.Memberships[0].Roles, ",") != "tenant-admin" {
		t.Fatalf("the member of the group rule: %+v", me)
	}
	// The member administers its tenant.
	if r := b.Do(t, "POST", "/v1/tenants/"+tenantID+"/service-accounts", map[string]any{"name": "by-ada", "roles": []string{"inference"}}, nil); r.Status != 201 {
		t.Errorf("the member administers its tenant: %d %s", r.Status, r.Body)
	}
	// A person with no mapped group is not made a user.
	idp.SignInAs("grace")
	gb := p.browser()
	if end := last(p.o.SignIn(t, gb)); end.Location == "/" || gb.Token() != "" {
		t.Errorf("an unmapped person signed in: %+v", end)
	}

	// The provider stops: a sign-in under way ends in the failure redirect with unavailable, and the
	// session that exists keeps working.
	idp.SignInAs("ada")
	pending := p.browser()
	start := p.o.Start(t, pending)
	q := p.o.Authorize(t, start.Location)
	idp.Stop()
	end := last(p.o.Callback(t, pending, q))
	if end.Status != 303 || !strings.Contains(end.Location, "sign_in_error=unavailable") || pending.Token() != "" {
		t.Errorf("the callback with the provider stopped: %d %q", end.Status, end.Location)
	}
	if r := b.Do(t, "GET", "/v1/me", nil, nil); r.Status != 200 {
		t.Errorf("the session that existed before: %d", r.Status)
	}
	idp.Resume()
	if end := last(p.o.SignIn(t, p.browser())); end.Location != "/" {
		t.Errorf("sign-in after the provider is back: %d %q", end.Status, end.Location)
	}
}

func last(h []apitest.Hop) apitest.Hop { return h[len(h)-1] }

// TestPeopleCanary is T-837 for the binary: planted values in accepted and refused sign-ins of both
// methods at log level debug appear in no output of the process.
func TestPeopleCanary(t *testing.T) {
	t.Parallel()
	idp := fakeidp.Start(t, fakeidp.Options{Users: map[string]fakeidp.Claims{"ada": {"name": "Ada", "email": "ada@example.test", "groups": []string{"admins"}}}})
	idp.SignInAs("ada")
	col := otlptest.New(t)
	d := dbtest.NewMigrated(t)
	env := baseEnv(t, d.Env(t))

	var cliOut []string
	password := initAdmin(t, env, "canary.admin")
	typedUser, typedPassword := apitest.Canary("username"), apitest.Canary("password")
	code, out, errs := runBinary(t, env, "user", "reset-password", "canary.admin")
	cliOut = append(cliOut, out, errs)
	if code != 0 {
		t.Fatalf("reset-password: %d %s", code, errs)
	}
	reset := rePasswordLine.FindStringSubmatch(out)[1]
	p := startOIDC(t, d, idp, opts{level: "debug", collector: col, otlpEndpoint: col.URL().String(), propagate: true},
		"PANDOKOS_AUTH_OIDC_GROUP_MAPPINGS=platform:platform-admin=admins", "PANDOKOS_AUTH_OIDC_AUTO_PROVISION=true")

	var responses []string
	rec := func(r *apitest.Response) *apitest.Response {
		responses = append(responses, fmt.Sprint(r.Header)+string(r.Body))
		return r
	}
	b := p.browser()
	rec(b.SignIn(t, typedUser, typedPassword))
	rec(b.SignIn(t, "canary.admin", typedPassword))
	rec(b.SignIn(t, "canary.admin", password)) // the first password: replaced by the reset
	good := rec(b.SignIn(t, "canary.admin", reset))
	if good.Status != 200 {
		t.Fatalf("sign-in with the reset password: %d %s", good.Status, good.Body)
	}
	token := b.Token()
	next := dbtest.NewPassword(t)
	rec(b.Do(t, "POST", "/v1/auth/password", map[string]any{"current_password": reset, "new_password": next}, nil))
	created := get[adminapi.UserCreated](t, rec(b.Do(t, "POST", "/v1/users", map[string]any{"sign_in": "local", "username": "canary.created"}, nil)), 201)
	hops := p.o.SignIn(t, p.browser())
	if end := last(hops); end.Location != "/" {
		t.Fatalf("sign-in through the provider: %d %q", end.Status, end.Location)
	}
	idp.SignInAs("nobody")
	for _, h := range p.o.SignIn(t, p.browser()) {
		responses = append(responses, h.Location+fmt.Sprint(h.Header)+string(h.Body))
	}

	code = p.stop(t)
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
	var hash string
	if err := d.Super(t).QueryRow(context.Background(), "SELECT password_hash FROM pandokos.local_account WHERE username = 'canary.admin'").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	var spans strings.Builder
	for _, s := range col.Spans() {
		b, _ := json.Marshal(s)
		spans.Write(b)
	}
	var tables strings.Builder
	for _, tbl := range []string{"idempotency_key", "signin_throttle", "oidc_login"} {
		var text string
		_ = d.Super(t).QueryRow(context.Background(), "SELECT COALESCE(string_agg(to_jsonb(x)::text, ' '), '') FROM pandokos."+tbl+" x").Scan(&text)
		tables.WriteString(text)
	}
	outputs := map[string]string{
		"the process output": p.stdout.String() + p.stderr.String(), "an audit row": d.AllAuditText(t), "an exported span": spans.String(),
		"the tables of throttling, transactions and keys": tables.String(), "the output of the CLI": strings.Join(cliOut, "\n"),
	}
	for _, v := range []struct {
		name, value string
		shownOnce   bool // a response may show it: the one that creates it
	}{
		{"a typed username", typedUser, false}, {"a typed password", typedPassword, false}, {"the first password", password, false},
		{"the new password", next, false}, {"the hash", hash, false}, {"the session token", token, true},
		{"the generated password of a user", created.InitialPassword, true},
		{"the client secret", idp.ClientSecret(), false}, {"the access token", idp.AccessToken(), false}, {"the refresh token", idp.RefreshToken(), false},
		{"the ID token", idp.LastIDToken(), false}, {"the code", idp.LastCode(), false},
	} {
		if v.value == "" {
			t.Fatalf("%s was not planted", v.name)
		}
		for name, text := range outputs {
			if strings.Contains(text, v.value) {
				t.Errorf("%s is in %s", v.name, name)
			}
		}
		if v.shownOnce {
			continue
		}
		for _, r := range responses {
			if strings.Contains(r, v.value) {
				t.Errorf("%s is in a response", v.name)
			}
		}
	}
}

// TestTokenAdministers is T-838: with tokens.admin_listener a token of the fake issuer creates a virtual
// key through the administration API, with an idempotency key, twice, and one key exists.
func TestTokenAdministers(t *testing.T) {
	t.Parallel()
	idp := fakeidp.Start(t, fakeidp.Options{})
	w := startTokenProc(t, idp, opts{level: "info", env: []string{"PANDOKOS_TOKENS_ADMIN_LISTENER=true"}})
	a := w.tenant(t, "token-admin")
	iss := w.registration(t, a, "idp", "pandokos-admin-listener")
	// The service account of a mapping needs the role that administers the tenant.
	adminSA := reID.FindStringSubmatch(w.run(t, a.admin, "service-account", "create", "--tenant", a.id, "--role", "tenant-admin", "token-admins"))[1]
	w.run(t, a.admin, "subject-mapping", "create", "--tenant", a.id, "--issuer", iss, "--subject", "ci", "--service-account", adminSA)
	token := w.sign("ES256", "ci", "pandokos-admin-listener")

	hdr := func(key string) http.Header {
		return http.Header{"Authorization": {"Bearer " + token}, "Content-Type": {"application/json"}, "Idempotency-Key": {key}}
	}
	body := []byte(`{"service_account_id":"` + a.sa + `","description":"made by a token"}`)
	path := "/v1/tenants/" + a.id + "/virtual-keys"
	before := len(strings.Split(strings.TrimSpace(w.run(t, a.admin, "key", "list", "--tenant", a.id, "-o", "json")), `"id"`))
	s1, first := exchange(t, "POST", w.adminURL(path), hdr("token-key-1"), body)
	if s1 != 201 {
		t.Fatalf("the first request: %d\n%s", s1, first)
	}
	s2, second := exchange(t, "POST", w.adminURL(path), hdr("token-key-1"), body)
	if s2 != 201 || !strings.Contains(second, "Idempotency-Replayed: true") {
		t.Fatalf("the second request: %d\n%s", s2, second)
	}
	after := len(strings.Split(strings.TrimSpace(w.run(t, a.admin, "key", "list", "--tenant", a.id, "-o", "json")), `"id"`))
	if after-before != 1 {
		t.Errorf("%d virtual keys were created by two requests with one idempotency key, want 1", after-before)
	}
	if strings.Contains(second, "pdk_") {
		t.Error("the replay shows the key again")
	}
	// A token that is refused is a 401.
	other := fakeidp.Start(t, fakeidp.Options{})
	bad := other.Sign("ES256", other.Standard("ci", "pandokos-admin-listener", time.Minute).With("iss", idp.Issuer()))
	h := hdr("token-key-2")
	h.Set("Authorization", "Bearer "+bad)
	if s, _ := exchange(t, "POST", w.adminURL(path), h, body); s != 401 {
		t.Errorf("a token that is not valid: %d", s)
	}
}

// TestUpgradeWithPeople is T-839: a database of the previous release, migrated: keys and tokens work as
// before and init adds the first user.
func TestUpgradeWithPeople(t *testing.T) {
	t.Parallel()
	idp := fakeidp.Start(t, fakeidp.Options{})
	d := dbtest.New(t)
	d.MigrateTo(t, 12)
	tn := d.SeedTenant(t, "upgrade-people")
	sa := d.SeedServiceAccount(t, tn, "workload")
	super := d.Super(t)
	ctx := context.Background()
	vkey, vkeyID, vhash, _ := authn.Generate(authn.KindTenant)
	pkey, pkeyID, phash, _ := authn.Generate(authn.KindPlatform)
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role) VALUES ($1::uuid, $2::uuid, 'tenant-admin')`, []any{tn.String(), sa}},
		{`INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash) VALUES ($1::uuid, $2::uuid, $3, $4)`, []any{tn.String(), sa, vkeyID, vhash[:]}},
		{`INSERT INTO pandokos.platform_key (key_id, secret_hash, description) VALUES ($1, $2, 'ops')`, []any{pkeyID, phash[:]}},
		{`INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience) VALUES ($1::uuid, 'idp', $2, 'pandokos-upgrade')`, []any{tn.String(), idp.Issuer()}},
	} {
		if _, err := super.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("%s: %v", stmt.sql, err)
		}
	}
	var mapping string
	if err := super.QueryRow(ctx, `INSERT INTO pandokos.subject_mapping (tenant_id, trusted_issuer_id, subject, service_account_id)
		SELECT $1::uuid, id, 'workload', $2::uuid FROM pandokos.trusted_issuer WHERE tenant_id = $1::uuid RETURNING id::text`, tn.String(), sa).Scan(&mapping); err != nil {
		t.Fatal(err)
	}

	if code, out, errs := runBinary(t, d.Env(t), "migrate"); code != 0 || !strings.Contains(out, "applied 0013_users_sessions") {
		t.Fatalf("migrate: exit %d\nstdout: %s\nstderr: %s", code, out, errs)
	}
	p := startBinary(t, opts{level: "info", db: d, noMigrate: true, up: fakeprovider.New(t), env: tokenEnv(idp)})
	tok := idp.Sign("ES256", idp.Standard("workload", "pandokos-upgrade", 10*time.Minute))

	// Keys and tokens work as before on both listeners.
	for name, c := range map[string]struct {
		url  string
		hdr  http.Header
		want int
	}{
		"a platform key on the administration listener": {p.adminURL("/v1/me"), authHeader(pkey.Reveal()), 200},
		"a virtual key on the administration listener":  {p.adminURL("/v1/me"), authHeader(vkey.Reveal()), 200},
		"a wrong key on the administration listener":    {p.adminURL("/v1/me"), authHeader("pdk_not_a_key"), 401},
		"a token on the administration listener (off)":  {p.adminURL("/v1/me"), authHeader(tok), 401},
	} {
		if status, text := exchange(t, "GET", c.url, c.hdr, nil); status != c.want {
			t.Errorf("%s: %d, want %d\n%s", name, status, c.want, text)
		}
	}
	for name, key := range map[string]string{"a virtual key": vkey.Reveal(), "a token": tok} {
		if status, text := exchange(t, "POST", p.url("/openai/v1/chat/completions"), authHeader(key), chatBody("fake-basic", false, fixtures.CanaryPrompt)); status == 401 {
			t.Errorf("%s on the inference listener: %d\n%s", name, status, text)
		}
	}
	if status, _ := exchange(t, "POST", p.url("/openai/v1/chat/completions"), authHeader("pdk_not_a_key"), chatBody("fake-basic", false, "x")); status != 401 {
		t.Errorf("a wrong key on the inference listener: %d", status)
	}
	_ = mapping

	// init adds the first user to an installation that has keys already.
	password := initAdmin(t, p.env, "first.admin", "--recover")
	if n := d.Count(t, "SELECT count(*) FROM pandokos.app_user"); n != 1 {
		t.Errorf("%d users after init", n)
	}
	b := p.browser()
	if s := get[adminapi.Session](t, b.SignIn(t, "first.admin", password), 200); !s.MustChangePassword {
		t.Error("the first password does not have to be changed")
	}
	if status, _ := exchange(t, "GET", p.adminURL("/v1/me"), authHeader(pkey.Reveal()), nil); status != 200 {
		t.Errorf("the old platform key after init: %d", status)
	}
	_ = testutil.RepoRoot
}
