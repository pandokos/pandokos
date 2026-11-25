package admin_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/apitest"
)

// TestCSRF is T-798: a request that changes state with a session, or signs in, is refused unless it
// carries the header, the Origin of the listener and no cross-site Sec-Fetch-Site.
func TestCSRF(t *testing.T) {
	t.Parallel()
	w := &world{api: apitest.NewPeople(t, apitest.PeopleConfig{})}
	w.fill(t, "csrf")
	tid, _ := tenant.ParseID(w.tenant.ID)
	d := w.api.DB
	u := d.PlatformUser(t, "csrf.person", []string{"platform-admin"})
	d.Member(t, tid, u.ID, []string{"tenant-admin"})
	b := w.api.NewBrowser()
	b.MustSignIn(t, u.Username, u.Password)

	variants := []struct {
		name  string
		apply func(*apitest.Browser)
	}{
		{"without the header", func(b *apitest.Browser) { b.NoCSRF = true }},
		{"without Origin", func(b *apitest.Browser) { b.NoOrigin = true }},
		{"with another origin", func(b *apitest.Browser) { b.Origin = "https://evil.example.test" }},
		{"with the same host on another port", func(b *apitest.Browser) { b.Origin = strings.Replace(w.api.URL, "127.0.0.1:", "127.0.0.1:1", 1) }},
		{"with the null origin", func(b *apitest.Browser) { b.Origin = "null" }},
		{"cross-site", func(b *apitest.Browser) { b.SecFetchSite = "cross-site" }},
		{"same-site", func(b *apitest.Browser) { b.SecFetchSite = "same-site" }},
		{"none", func(b *apitest.Browser) { b.SecFetchSite = "none" }},
	}
	before := w.api.Snapshot(t, "audit_event", "platform_audit_event")
	n := 0
	for _, op := range adminapi.Operations {
		if op.Method == http.MethodGet {
			continue
		}
		c := apitest.CallFor(t, op, w.params())
		for _, v := range variants {
			attacker := w.api.NewBrowser()
			attacker.SetCookie("__Host-pandokos_session", b.Token())
			v.apply(attacker)
			r := attacker.Do(t, c.Method, c.Path, c.Body, nil)
			if r.Status != 403 || problem(t, r).Code != "csrf_rejected" {
				t.Errorf("%s %s: %d %s, want 403 csrf_rejected", op.ID, v.name, r.Status, r.Body)
			}
			n++
		}
	}
	if n < 400 {
		t.Errorf("only %d requests were tried", n)
	}
	if after := w.api.Snapshot(t, "audit_event", "platform_audit_event"); after != before {
		t.Error("a request refused as forged changed a row")
	}

	// The same request with everything in order is not refused for that reason.
	ok := w.api.NewBrowser()
	ok.SetCookie("__Host-pandokos_session", b.Token())
	ok.SecFetchSite = "same-origin"
	if r := ok.Do(t, "POST", "/v1/tenants/"+w.tenant.ID+"/service-accounts", map[string]any{"name": "csrf-sa", "roles": []string{"inference"}}, nil); r.Status != 201 {
		t.Errorf("a well-formed request: %d %s", r.Status, r.Body)
	}

	// With a key none of this is asked: no browser attaches an Authorization header by itself.
	k := w.api.NewBrowser()
	k.NoCSRF, k.Origin, k.SecFetchSite = true, "https://evil.example.test", "cross-site"
	r := k.Do(t, "POST", "/v1/tenants/"+w.tenant.ID+"/service-accounts", map[string]any{"name": "csrf-key-sa", "roles": []string{"inference"}},
		http.Header{"Authorization": {"Bearer " + w.adminKey}})
	if r.Status != 201 {
		t.Errorf("a key with cross-site headers: %d %s", r.Status, r.Body)
	}

	// A GET with a session needs none of it.
	g := w.api.NewBrowser()
	g.SetCookie("__Host-pandokos_session", b.Token())
	g.NoCSRF, g.NoOrigin, g.SecFetchSite = true, true, "cross-site"
	if r := g.Do(t, "GET", "/v1/me", nil, nil); r.Status != 200 {
		t.Errorf("GET with a session and no CSRF header: %d %s", r.Status, r.Body)
	}
}

// TestSignInTransport is T-800: a listener that is reachable in clear text outside loopback, and
// whose HTTPS origin the operator did not name, signs nobody in.
func TestSignInTransport(t *testing.T) {
	t.Parallel()
	plain := func(o *admin.Options) { o.Origin = admin.OriginOptions{} } // no TLS, not loopback, no auth.public_origin
	p := newPeople(t, apitest.PeopleConfig{Admin: plain})
	user := p.d.PlatformUser(t, "transport.person", []string{"platform-admin"})
	cookie := p.sessionOf(t, user.ID)

	anon := p.api.NewBrowser()
	for name, r := range map[string]*apitest.Response{
		"getAuthMethods":      anon.Do(t, "GET", "/v1/auth/methods", nil, nil),
		"signIn":              anon.SignIn(t, user.Username, user.Password),
		"startOidcSignIn":     anon.Do(t, "GET", "/v1/auth/oidc/start", nil, nil),
		"completeOidcSignIn":  anon.Do(t, "GET", "/v1/auth/oidc/callback?code=x&state=y", nil, nil),
		"a session request":   cookie.Do(t, "GET", "/v1/me", nil, nil),
		"a session operation": cookie.Do(t, "POST", "/v1/auth/sign-out", map[string]any{}, nil),
	} {
		if r.Status != 403 || problem(t, r).Code != "sign_in_disabled" {
			t.Errorf("%s: %d %s, want 403 sign_in_disabled", name, r.Status, r.Body)
		}
		if len(setCookies(r)) != 0 {
			t.Errorf("%s: Set-Cookie %q", name, setCookies(r))
		}
	}
	// A key does not depend on the origin.
	if r := p.api.Do(t, "GET", "/v1/me", p.api.PlatformKey(), nil, nil); r.Status != 200 {
		t.Errorf("a key on a plain listener: %d", r.Status)
	}

	// With the origin named, they work.
	named := newPeople(t, apitest.PeopleConfig{Admin: func(o *admin.Options) { o.Origin = admin.OriginOptions{Public: "https://admin.example.test"} }})
	u2 := named.d.PlatformUser(t, "named.person", []string{"platform-admin"})
	b := named.api.NewBrowser()
	b.Origin = "https://admin.example.test"
	if r := b.Do(t, "GET", "/v1/auth/methods", nil, nil); r.Status != 200 {
		t.Errorf("getAuthMethods with the origin named: %d", r.Status)
	}
	if r := b.SignIn(t, u2.Username, u2.Password); r.Status != 200 {
		t.Errorf("signIn with the origin named: %d %s", r.Status, r.Body)
	}
	if r := b.Do(t, "GET", "/v1/me", nil, nil); r.Status != 200 {
		t.Errorf("a session request with the origin named: %d", r.Status)
	}
	// An origin that is not the named one is still a forgery.
	b.Origin = "http://" + strings.TrimPrefix(named.api.URL, "http://")
	if r := b.Do(t, "POST", "/v1/auth/sign-out", map[string]any{}, nil); r.Status != 403 || problem(t, r).Code != "csrf_rejected" {
		t.Errorf("the listener's own address when another origin is named: %d %s", r.Status, r.Body)
	}
}

// TestSecurityHeaders is T-801: every response, problems included, carries the headers of section 5.6.
func TestSecurityHeaders(t *testing.T) {
	t.Parallel()
	w := &world{api: apitest.NewPeople(t, apitest.PeopleConfig{})}
	w.fill(t, "headers")
	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
		"Cache-Control":           "no-store",
	}
	check := func(what string, r *apitest.Response) {
		t.Helper()
		for h, v := range want {
			if got := r.Header.Get(h); got != v {
				t.Errorf("%s (%d): %s is %q, want %q", what, r.Status, h, got, v)
			}
		}
	}
	for _, op := range adminapi.Operations {
		c := apitest.CallFor(t, op, w.params())
		key := w.platform // whatever the answer is, it carries the headers
		check(op.ID+" with a key", w.api.Do(t, c.Method, c.Path, key, c.Body, nil))
		check(op.ID+" with no credential", w.api.Do(t, c.Method, c.Path, "", c.Body, nil))
	}
	b := w.api.NewBrowser()
	check("a 404", b.Do(t, "GET", "/v1/nope", nil, nil))
	check("a 405", b.Do(t, "PUT", "/v1/me", nil, nil))
	check("malformed JSON", b.Do(t, "POST", "/v1/auth/sign-in", `{"username":`, nil))
	check("a body that is too large", b.Do(t, "POST", "/v1/auth/sign-in", strings.Repeat("a", 2<<20), nil))
	check("a forged request", func() *apitest.Response {
		b.NoCSRF = true
		defer func() { b.NoCSRF = false }()
		return b.SignIn(t, "x", "y")
	}())
	check("an authentication failure", w.api.Do(t, "GET", "/v1/me", "pdk_wrong", nil, nil))
	check("a success with a cookie", func() *apitest.Response {
		u := w.api.DB.PlatformUser(t, "header.person", nil)
		return w.api.NewBrowser().SignIn(t, u.Username, u.Password)
	}())
}
