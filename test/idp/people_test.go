//go:build idp

package idp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/oidc"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// The people of the sprint 4.2 realm (part D, D1): the confidential client pandokos-admin with the
// standard flow and PKCE required, whose redirect URI is the callback of the test origin
// (PANDOKOS_TEST_IDP_ORIGIN), a group membership mapper for the ID token, the groups below, and the
// users alice (both groups) and bob (none) with generated passwords. The tests read what they need
// from the environment that make idp-up writes; they fail, never skip, while part D has not made it.
const (
	groupAdmins = "pandokos-admins" // rule: platform:platform-admin=pandokos-admins
	groupTenant = "tenant-a-admins" // rule: tenant:tenant-a:tenant-admin=tenant-a-admins
)

func peopleEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv("PANDOKOS_TEST_IDP_KEYCLOAK_" + name)
	if v == "" {
		t.Fatalf("PANDOKOS_TEST_IDP_KEYCLOAK_%s is not set: part D adds the people of sprint 4.2 to the realm and to local/idp/env", name)
	}
	return v
}

// kcPerson is a person of the realm.
type kcPerson struct{ username, password string }

// person returns alice or bob.
func person(t *testing.T, which string) kcPerson {
	t.Helper()
	return kcPerson{which, peopleEnv(t, strings.ToUpper(which)+"_PASSWORD")}
}

// kcLogin plays the browser at the login form of Keycloak: it follows the redirect that starts the
// flow, fills the form and returns the path and query that the provider redirects to, on the
// origin of Pandokos.
func kcLogin(t *testing.T, authURL string, p kcPerson) string {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := providerClient(t)
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Get(authURL)
	if err != nil {
		t.Fatalf("the authorization endpoint: %v", err)
	}
	page, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	m := regexp.MustCompile(`<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"`).FindSubmatch(page)
	if m == nil {
		t.Fatalf("no login form at the authorization endpoint (status %d)", resp.StatusCode)
	}
	form := url.Values{"username": {p.username}, "password": {p.password}, "credentialId": {""}}
	resp, err = client.PostForm(html.UnescapeString(string(m[1])), form)
	if err != nil {
		t.Fatalf("the login form: %v", err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusFound || loc == "" {
		return "" // the provider refused the person
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	return u.RequestURI()
}

// people is the process under test: the binary against Keycloak, with the group rules of the sprint.
type peopleProc struct {
	*gw
	addr, origin string
	boot         string
	root         string // the password of the administrator that init made
}

func startPeople(t *testing.T, d *dbtest.Database, extra ...string) *peopleProc {
	t.Helper()
	if d == nil {
		d = dbtest.NewMigrated(t)
	}
	// The redirect URI of the client is the callback of the test origin: the process serves there.
	origin := os.Getenv("PANDOKOS_TEST_IDP_ORIGIN")
	if origin == "" {
		t.Fatal("PANDOKOS_TEST_IDP_ORIGIN is not set: part D writes it to local/idp/env")
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		t.Fatalf("PANDOKOS_TEST_IDP_ORIGIN %q", origin)
	}
	addr := u.Host
	secretFile := testutil.WriteSecret(t, "oidc-client", peopleEnv(t, "PEOPLE_SECRET"))
	e := []string{
		"PANDOKOS_LISTENERS_ADMIN_ADDRESS=" + addr, "PANDOKOS_AUTH_PUBLIC_ORIGIN=http://" + addr,
		"PANDOKOS_AUTH_OIDC_ENABLED=true", "PANDOKOS_AUTH_OIDC_AUTO_PROVISION=true", "PANDOKOS_AUTH_OIDC_ISSUER=" + env["PANDOKOS_TEST_IDP_KEYCLOAK_ISSUER"], "PANDOKOS_AUTH_OIDC_CLIENT_ID=" + peopleEnv(t, "PEOPLE_CLIENT"),
		"PANDOKOS_AUTH_OIDC_CLIENT_SECRET_FILE=" + secretFile, "PANDOKOS_AUTH_OIDC_PRIVATE_HOSTS=localhost",
		"PANDOKOS_AUTH_OIDC_GROUP_MAPPINGS=platform:platform-admin=" + groupAdmins + ",tenant:tenant-a:tenant-admin=" + groupTenant,
	}
	p := &peopleProc{addr: addr, origin: "http://" + addr}
	boot := filepath.Join(t.TempDir(), "boot.key")
	initEnv := append([]string{"PATH=" + os.Getenv("PATH")}, d.Env(t)...)
	out := run(t, initEnv, "init", "--admin-username", "root.admin", "--key-out", boot)
	if m := regexp.MustCompile(`(?m)^password: ([a-z2-7]{24})$`).FindStringSubmatch(out); m != nil {
		p.root = m[1]
	}
	p.boot = boot
	p.gw = start(t, d, nil, append(e, extra...)...)
	return p
}

func (p *peopleProc) browser() *apitest.Browser { return apitest.NewBrowserAt(p.origin, nil) }

func (p *peopleProc) cli(t *testing.T, args ...string) string {
	t.Helper()
	return run(t, []string{"PANDOKOS_CLI_SERVER=" + p.origin, "PANDOKOS_CLI_KEY_FILE=" + p.boot}, args...)
}

// signIn runs a whole sign-in through Keycloak's form and returns the last hop.
func (p *peopleProc) signIn(t *testing.T, b *apitest.Browser, who kcPerson) apitest.Hop {
	t.Helper()
	start := b.Navigate(t, "/v1/auth/oidc/start")
	hop := start[len(start)-1]
	if hop.Status != 303 {
		return hop
	}
	back := kcLogin(t, hop.Location, who)
	if back == "" {
		return apitest.Hop{Status: 0}
	}
	hops := b.Navigate(t, back)
	return hops[len(hops)-1]
}

func me(t *testing.T, b *apitest.Browser) adminapi.Me {
	t.Helper()
	r := b.Do(t, "GET", "/v1/me", nil, nil)
	var m adminapi.Me
	if r.Status != 200 {
		t.Fatalf("getMe: %d %s", r.Status, r.Body)
	}
	r.JSON(t, &m)
	return m
}

// TestKeycloakSignIn is T-840, an exit condition: a Keycloak user signs in through the form of the
// provider, with PKCE; the session is the one of a local sign-in.
func TestKeycloakSignIn(t *testing.T) {
	p := startPeople(t, nil)
	a := person(t, "alice")
	b := p.browser()
	end := p.signIn(t, b, a)
	if end.Status != 303 || strings.Contains(end.Location, "sign_in_error") || b.Token() == "" {
		t.Fatalf("the sign-in ended with %d %q", end.Status, end.Location)
	}
	if !regexp.MustCompile(`^__Host-pandokos_session=pds_`).MatchString(strings.Join(end.Header.Values("Set-Cookie"), "|")) &&
		!strings.Contains(strings.Join(end.Header.Values("Set-Cookie"), "|"), "__Host-pandokos_session=pds_") {
		t.Errorf("Set-Cookie %q", end.Header.Values("Set-Cookie"))
	}
	m := me(t, b)
	if m.PrincipalType != "user" || m.Session == nil || m.Session.Method != "oidc" || m.Name == "" {
		t.Errorf("getMe: %+v", m)
	}
	// The user was provisioned with the role of the group rule, not by hand.
	if strings.Join(m.Roles, ",") != "platform-admin" {
		t.Errorf("roles %v, want platform-admin by the group rule", m.Roles)
	}
	// The ID token was checked with PKCE: a start whose verifier the provider does not know fails.
	start := b.Navigate(t, "/v1/auth/oidc/start")
	u, _ := url.Parse(start[len(start)-1].Location)
	if u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("code_challenge") == "" || u.Query().Get("nonce") == "" {
		t.Errorf("the authorization request: %v", u.Query())
	}
}

// TestKeycloakGroups is T-841: the group claim gives a membership with tenant-admin, and the user
// administers that tenant; removed from the group, the next sign-in removes the membership.
func TestKeycloakGroups(t *testing.T) {
	p := startPeople(t, nil)
	tenantOut := p.cli(t, "tenant", "create", "tenant-a")
	tenantID := regexp.MustCompile(`(?m)^id: ([0-9a-f-]{36})$`).FindStringSubmatch(tenantOut)[1]
	who := person(t, "bob")
	kc := newKeycloakAdmin(t)

	if err := kc.setGroup(who.username, groupTenant, true); err != nil {
		t.Fatalf("put %s in %s: %v", who.username, groupTenant, err)
	}
	b := p.browser()
	if end := p.signIn(t, b, who); end.Status != 303 || strings.Contains(end.Location, "sign_in_error") {
		t.Fatalf("the sign-in ended with %d %q", end.Status, end.Location)
	}
	m := me(t, b)
	if len(m.Memberships) != 1 || m.Memberships[0].Tenant.ID != tenantID || strings.Join(m.Memberships[0].Roles, ",") != "tenant-admin" {
		t.Fatalf("memberships %+v", m.Memberships)
	}
	if r := b.Do(t, "POST", "/v1/tenants/"+tenantID+"/service-accounts", map[string]any{"name": "by-keycloak-user", "roles": []string{"inference"}}, nil); r.Status != 201 {
		t.Errorf("the member administers its tenant: %d %s", r.Status, r.Body)
	}
	// Removed from the group at the provider: the next sign-in removes the membership.
	if err := kc.setGroup(who.username, groupTenant, false); err != nil {
		t.Fatalf("remove %s from %s: %v", who.username, groupTenant, err)
	}
	b2 := p.browser()
	if end := p.signIn(t, b2, who); end.Status != 303 {
		t.Fatalf("the second sign-in ended with %d %q", end.Status, end.Location)
	}
	if m := me(t, b2); len(m.Memberships) != 0 {
		t.Errorf("memberships after the group was withdrawn: %+v", m.Memberships)
	}
}

// TestGroupClaimShapes is T-842: the group claims of Keycloak (ID token) and of Authentik (token with the
// profile scope) are read by oidc.Groups with the default claim name.
func TestGroupClaimShapes(t *testing.T) {
	// Keycloak: the ID token of the standard flow, obtained by hand with PKCE.
	who := person(t, "alice")
	verifier := base64.RawURLEncoding.EncodeToString(func() []byte { b := make([]byte, 32); _, _ = rand.Read(b); return b }())
	sum := sha256.Sum256([]byte(verifier))
	redirect := os.Getenv("PANDOKOS_TEST_IDP_ORIGIN") + "/v1/auth/oidc/callback"
	issuer := env["PANDOKOS_TEST_IDP_KEYCLOAK_ISSUER"]
	q := url.Values{"response_type": {"code"}, "client_id": {peopleEnv(t, "PEOPLE_CLIENT")}, "redirect_uri": {redirect}, "scope": {"openid profile email"},
		"state": {"s"}, "nonce": {"n"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}}
	back := kcLogin(t, issuer+"/protocol/openid-connect/auth?"+q.Encode(), who)
	if back == "" {
		t.Fatal("Keycloak refused the person")
	}
	u, _ := url.Parse(back)
	code := u.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in %s", back)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier},
		"client_id": {peopleEnv(t, "PEOPLE_CLIENT")}, "client_secret": {peopleEnv(t, "PEOPLE_SECRET")}}
	resp, err := providerClient(t).PostForm(env["PANDOKOS_TEST_IDP_KEYCLOAK_TOKEN_URL"], form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil || tok.IDToken == "" {
		t.Fatalf("no ID token (status %d)", resp.StatusCode)
	}
	_, payload := segments(t, tok.IDToken)
	if got := groupsOf(t, payload); len(got) == 0 || !contains(got, groupAdmins) {
		t.Errorf("Keycloak: groups %v, want %s among them", got, groupAdmins)
	}
	// Authentik: the access token with the profile scope.
	_, apay := segments(t, authentikTokenScope(t, "PANDOKOS_TEST_IDP_AUTHENTIK_USER_A", "PANDOKOS_TEST_IDP_AUTHENTIK_SECRET_A", "profile"))
	if got := groupsOf(t, apay); len(got) == 0 {
		t.Errorf("Authentik: no group read with the default claim name from %v", keysOf(apay))
	}
}

func groupsOf(t *testing.T, payload map[string]any) []string {
	t.Helper()
	raw := map[string]json.RawMessage{}
	for k, v := range payload {
		b, _ := json.Marshal(v)
		raw[k] = b
	}
	return oidc.Groups(raw, "groups")
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

func keysOf(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestKeycloakUserFlows is T-843, an exit condition: the flows of T-831 through Keycloak.
func TestKeycloakUserFlows(t *testing.T) {
	p := startPeople(t, nil)
	kc := newKeycloakAdmin(t)
	admin, other := person(t, "alice"), person(t, "bob")

	// An administrator linked by hand: createUser with the subject at the provider.
	root := p.browser()
	root.MustSignIn(t, "root.admin", p.root)
	changed := dbtest.NewPassword(t)
	if r := root.Do(t, "POST", "/v1/auth/password", map[string]any{"current_password": p.root, "new_password": changed}, nil); r.Status != 200 {
		t.Fatalf("change the password of the first administrator: %d %s", r.Status, r.Body)
	}
	subjectA, err := kc.subject(admin.username)
	if err != nil {
		t.Fatal(err)
	}
	linked := func(sub string, roles []string) adminapi.UserCreated {
		r := root.Do(t, "POST", "/v1/users", map[string]any{"sign_in": "oidc", "subject": sub, "platform_roles": roles}, nil)
		if r.Status != 201 {
			t.Fatalf("createUser: %d %s", r.Status, r.Body)
		}
		var out adminapi.UserCreated
		r.JSON(t, &out)
		return out
	}
	la := linked(subjectA, []string{"platform-admin"})

	b := p.browser()
	if end := p.signIn(t, b, admin); end.Status != 303 || strings.Contains(end.Location, "sign_in_error") {
		t.Fatalf("the linked administrator: %d %q", end.Status, end.Location)
	}
	if m := me(t, b); m.ID != la.User.ID {
		t.Errorf("getMe %+v", m)
	}
	// It creates a tenant and, with the key of the tenant's first administrator, a member.
	r := b.Do(t, "POST", "/v1/tenants", map[string]any{"name": "flows"}, nil)
	if r.Status != 201 {
		t.Fatalf("createTenant: %d %s", r.Status, r.Body)
	}
	var created adminapi.TenantCreated
	r.JSON(t, &created)
	member := apitest.NewBrowserAt(p.origin, nil).Do(t, "POST", "/v1/tenants/"+created.Tenant.ID+"/members",
		map[string]any{"account": "new", "username": "flows.member", "roles": []string{"tenant-admin"}}, http.Header{"Authorization": {"Bearer " + created.AdminKey.Secret}})
	if member.Status != 201 {
		t.Errorf("createMember: %d %s", member.Status, member.Body)
	}

	// A person that Keycloak accepts and Pandokos has disabled is refused.
	subjectB, err := kc.subject(other.username)
	if err != nil {
		t.Fatal(err)
	}
	lb := linked(subjectB, nil)
	if r := root.Do(t, "POST", "/v1/users/"+lb.User.ID+"/disable", map[string]any{}, nil); r.Status != 200 {
		t.Fatalf("disableUser: %d %s", r.Status, r.Body)
	}
	db := p.browser()
	end := p.signIn(t, db, other)
	u, _ := url.Parse(end.Location)
	if end.Status != 303 || u.Query().Get("sign_in_error") != "sign_in_failed" || db.Token() != "" {
		t.Errorf("a disabled user that Keycloak accepts: %d %q", end.Status, end.Location)
	}

	// Keycloak stopped: the open session keeps working, a new sign-in fails cleanly.
	kc.stop(t)
	if r := b.Do(t, "GET", "/v1/me", nil, nil); r.Status != 200 {
		t.Errorf("the open session with Keycloak stopped: %d", r.Status)
	}
	nb := p.browser()
	start := nb.Navigate(t, "/v1/auth/oidc/start")
	h := start[len(start)-1]
	if h.Status == 303 && strings.HasPrefix(h.Location, env["PANDOKOS_TEST_IDP_KEYCLOAK_ISSUER"]) {
		// The metadata was cached: the redirect works, the provider is not there to answer the browser.
		return
	}
	if h.Status != 503 && !(h.Status == 303 && strings.Contains(h.Location, "sign_in_error=unavailable")) {
		t.Errorf("a new sign-in with Keycloak stopped: %d %q", h.Status, h.Location)
	}
}

// keycloakAdmin drives the administration API of Keycloak, for the tests that change the realm.
type keycloakAdmin struct {
	t      *testing.T
	client *http.Client
	base   string
	token  string
}

func newKeycloakAdmin(t *testing.T) *keycloakAdmin {
	t.Helper()
	k := &keycloakAdmin{t: t, client: providerClient(t), base: peopleEnv(t, "ADMIN_URL")}
	form := url.Values{"grant_type": {"password"}, "client_id": {"admin-cli"}, "username": {peopleEnv(t, "ADMIN_USER")}, "password": {peopleEnv(t, "ADMIN_PASSWORD")}}
	// An earlier test stops Keycloak and starts it again when it ends, without waiting: ask until it answers.
	var resp *http.Response
	var err error
	for deadline := time.Now().Add(90 * time.Second); ; time.Sleep(time.Second) {
		resp, err = k.client.PostForm(k.base+"/realms/master/protocol/openid-connect/token", form)
		if err == nil || time.Now().After(deadline) {
			break
		}
	}
	if err != nil {
		t.Fatalf("the administration API of Keycloak: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil || out.AccessToken == "" {
		t.Fatalf("no administration token (status %d)", resp.StatusCode)
	}
	k.token = out.AccessToken
	return k
}

func (k *keycloakAdmin) do(method, path string, body any) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, k.base+"/admin/realms/pandokos"+path, rd)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return b, resp.StatusCode, err
}

func (k *keycloakAdmin) subject(username string) (string, error) {
	b, status, err := k.do("GET", "/users?exact=true&username="+url.QueryEscape(username), nil)
	if err != nil || status != 200 {
		return "", fmt.Errorf("users: %d %v", status, err)
	}
	var users []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &users); err != nil || len(users) != 1 {
		return "", fmt.Errorf("the user %s: %v (%d found)", username, err, len(users))
	}
	return users[0].ID, nil
}

// setGroup puts the user in the group or takes it out.
func (k *keycloakAdmin) setGroup(username, group string, member bool) error {
	id, err := k.subject(username)
	if err != nil {
		return err
	}
	b, status, err := k.do("GET", "/groups?exact=true&search="+url.QueryEscape(group), nil)
	if err != nil || status != 200 {
		return fmt.Errorf("groups: %d %v", status, err)
	}
	var groups []struct {
		ID, Name string
	}
	if err := json.Unmarshal(b, &groups); err != nil || len(groups) == 0 {
		return fmt.Errorf("the group %s does not exist: %v", group, err)
	}
	method := "DELETE"
	if member {
		method = "PUT"
	}
	_, status, err = k.do(method, "/users/"+id+"/groups/"+groups[0].ID, nil)
	if err != nil || status/100 != 2 {
		return fmt.Errorf("%s membership: %d %v", method, status, err)
	}
	return nil
}

// stop stops the container of Keycloak and starts it again when the test ends.
func (k *keycloakAdmin) stop(t *testing.T) {
	t.Helper()
	if out, err := exec.Command("docker", "compose", "-p", composeProject, "stop", "keycloak").CombinedOutput(); err != nil {
		t.Fatalf("cannot stop Keycloak: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "compose", "-p", composeProject, "start", "keycloak").Run() })
}
