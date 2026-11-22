package admin_test

import (
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/password"
	"github.com/pandokos/pandokos/internal/session"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
)

var reGenerated = regexp.MustCompile(`^[a-z2-7]{24}$`)

// TestMustChangeGate is T-788: a session that must change its password may call three operations.
func TestMustChangeGate(t *testing.T) {
	w := &world{api: apitest.NewPeople(t, apitest.PeopleConfig{})}
	w.fill(t, "gate")
	tid, _ := tenant.ParseID(w.tenant.ID)
	u := w.api.DB.PlatformUser(t, "gate.user", []string{"platform-admin"}, dbtest.MustChange(time.Now().Add(72*time.Hour)))
	w.api.DB.Member(t, tid, u.ID, []string{"tenant-admin"}) // so that no operation fails earlier than the gate

	b := w.api.NewBrowser()
	in := decode[adminapi.Session](t, b.SignIn(t, u.Username, u.Password), 200)
	if !in.MustChangePassword {
		t.Fatal("the session of a generated password does not say that the password must be changed")
	}
	allowed := map[string]bool{"getMe": true, "changePassword": true, "signOut": true}
	n := 0
	for _, op := range adminapi.Operations {
		if op.Public || allowed[op.ID] { // a public operation reads no session
			continue
		}
		c := apitest.CallFor(t, op, w.params())
		r := b.Do(t, c.Method, c.Path, c.Body, nil)
		if r.Status != 403 || problem(t, r).Code != "password_change_required" {
			t.Errorf("%s: %d %s, want 403 password_change_required", op.ID, r.Status, r.Body)
		}
		n++
	}
	if n < 100 {
		t.Errorf("only %d operations were tried", n)
	}
	me := decode[adminapi.Me](t, b.Do(t, "GET", "/v1/me", nil, nil), 200)
	if me.PrincipalType != "user" || !me.MustChangePassword {
		t.Errorf("getMe: %+v", me)
	}

	// After the change every operation works.
	next := dbtest.NewPassword(t)
	out := decode[adminapi.Session](t, b.Do(t, "POST", "/v1/auth/password", map[string]any{"current_password": u.Password, "new_password": next}, nil), 200)
	if out.MustChangePassword {
		t.Error("the new session still says that the password must be changed")
	}
	for _, path := range []string{"/v1/tenants", "/v1/tenants/" + w.tenant.ID + "/service-accounts", "/v1/users"} {
		if r := b.Do(t, "GET", path, nil, nil); r.Status != 200 {
			t.Errorf("GET %s after the change: %d %s", path, r.Status, r.Body)
		}
	}
	if r := b.Do(t, "POST", "/v1/auth/sign-out", map[string]any{}, nil); r.Status != 200 {
		t.Errorf("signOut: %d", r.Status)
	}
}

// TestResetPassword is T-789.
func TestResetPassword(t *testing.T) {
	p := newPeople(t, apitest.PeopleConfig{})
	d := p.d
	// A person the platform manages, a member the tenant manages, a federated user.
	plat := d.PlatformUser(t, "plat.person", nil)
	member := d.TenantUser(t, p.tid, "tenant.person", []string{"tenant-admin"})
	shared := d.PlatformUser(t, "shared.person", nil)
	d.Member(t, p.tid, shared.ID, []string{"tenant-admin"})
	fed := d.FederatedUser(t, "https://idp.example.test", "subject-1", nil)
	otherTenant, otherKey := p.api.Tenant(t, "globex")
	_ = otherTenant

	t.Run("a platform-managed user, by the platform", func(t *testing.T) {
		b1, b2 := p.signIn(t, plat), p.signIn(t, plat)
		_ = b2
		// A failed attempt leaves a throttle row that the reset removes.
		if r := p.api.NewBrowser().SignIn(t, plat.Username, "wrong password here"); r.Status != 401 {
			t.Fatalf("wrong password: %d", r.Status)
		}
		before := len(d.PlatformAuditRows(t))
		out := decode[adminapi.PasswordReset](t, p.platform(t, "POST", "/v1/users/"+plat.ID+"/reset-password", map[string]any{}), 200)
		if out.UserID != plat.ID || !reGenerated.MatchString(out.Password) || out.ExpiresAt.Before(time.Now().Add(71*time.Hour)) || out.ExpiresAt.After(time.Now().Add(73*time.Hour)) {
			t.Fatalf("response %+v", out)
		}
		if r := b1.Do(t, "GET", "/v1/me", nil, nil); r.Status != 401 {
			t.Errorf("a session survived the reset: %d", r.Status)
		}
		if n := d.Count(t, "SELECT count(*) FROM pandokos.user_session WHERE user_id = $1::uuid", plat.ID); n != 0 {
			t.Errorf("%d sessions after the reset", n)
		}
		for _, r := range d.ThrottleRows(t) {
			if r.Kind == "account" {
				t.Errorf("a throttle row of the account survived the reset: %+v", r)
			}
		}
		if r := p.api.NewBrowser().SignIn(t, plat.Username, plat.Password); r.Status != 401 {
			t.Errorf("the old password after the reset: %d", r.Status)
		}
		nb := p.api.NewBrowser()
		in := decode[adminapi.Session](t, nb.SignIn(t, plat.Username, out.Password), 200)
		if !in.MustChangePassword {
			t.Error("the generated password does not have to be changed")
		}
		rows := d.PlatformAuditRows(t)[before:]
		found := 0
		for _, r := range rows {
			if r.Action == "user.password_reset" && r.ResourceID == plat.ID && r.Outcome == "success" && r.ActorType == "platform" {
				found++
			}
			if strings.Contains(r.Raw, out.Password) {
				t.Error("the generated password is in an audit row")
			}
		}
		if found != 1 {
			t.Errorf("%d user.password_reset rows in the platform log, want 1: %+v", found, rows)
		}
	})

	t.Run("a member the tenant manages, by the tenant", func(t *testing.T) {
		b := p.signIn(t, member)
		out := decode[adminapi.PasswordReset](t, p.tenantKey(t, "POST", p.membersPath("/"+member.ID+"/reset-password"), map[string]any{}), 200)
		if !reGenerated.MatchString(out.Password) || out.UserID != member.ID {
			t.Fatalf("response %+v", out)
		}
		if r := b.Do(t, "GET", "/v1/me", nil, nil); r.Status != 401 {
			t.Errorf("a session survived the reset: %d", r.Status)
		}
		var logged bool
		for _, r := range d.AuditRows(t, p.tid) {
			logged = logged || (r.Action == "user.password_reset" && r.ResourceID == member.ID)
		}
		if !logged {
			t.Error("no user.password_reset row in the tenant's log")
		}
		if r := p.api.NewBrowser().SignIn(t, member.Username, out.Password); r.Status != 200 {
			t.Errorf("sign-in with the generated password: %d", r.Status)
		}
	})

	t.Run("refused across the managing rule", func(t *testing.T) {
		// A platform-managed member: the tenant cannot reset it.
		if c := code(t, p.tenantKey(t, "POST", p.membersPath("/"+shared.ID+"/reset-password"), map[string]any{}), 409); c != "invalid_state" {
			t.Errorf("code %s", c)
		}
		// The administrator of another tenant does not see this tenant's member.
		r := p.api.Do(t, "POST", p.membersPath("/"+member.ID+"/reset-password"), otherKey, map[string]any{}, nil)
		if c := code(t, r, 404); c != "not_found" {
			t.Errorf("code %s", c)
		}
		// Platform operations see platform-managed users only.
		if c := code(t, p.platform(t, "POST", "/v1/users/"+member.ID+"/reset-password", map[string]any{}), 404); c != "not_found" {
			t.Errorf("code %s", c)
		}
		// A federated user has no password.
		if c := code(t, p.platform(t, "POST", "/v1/users/"+fed.ID+"/reset-password", map[string]any{}), 409); c != "invalid_state" {
			t.Errorf("code %s", c)
		}
		if c := code(t, p.platform(t, "POST", "/v1/users/"+apitest.NoUser+"/reset-password", map[string]any{}), 404); c != "not_found" {
			t.Errorf("code %s", c)
		}
	})
}

// TestSignInEndpoint is T-790.
func TestSignInEndpoint(t *testing.T) {
	p := newPeople(t, apitest.PeopleConfig{})
	d := p.d
	good := d.PlatformUser(t, "good.person", []string{"platform-admin"}, dbtest.Display("Good Person", "good@example.test"))
	disabled := d.PlatformUser(t, "off.person", nil, dbtest.Disabled())
	expired := d.PlatformUser(t, "old.person", nil, dbtest.MustChange(time.Now().Add(-time.Hour)))

	t.Run("success", func(t *testing.T) {
		b := p.api.NewBrowser()
		r := b.SignIn(t, "Good.Person", good.Password)
		s := decode[adminapi.Session](t, r, 200)
		if s.User.ID != good.ID || s.User.Username != "good.person" || s.User.DisplayName != "Good Person" || s.Method != "local" || s.MustChangePassword || !s.Current ||
			s.IdleTimeoutSeconds != 1800 || !s.ExpiresAt.After(time.Now().Add(11*time.Hour)) || s.ID == "" {
			t.Errorf("session %+v", s)
		}
		cookies := setCookies(r)
		if len(cookies) != 1 || !regexp.MustCompile(`^__Host-pandokos_session=pds_[A-Za-z0-9_-]{43}; Path=/; HttpOnly; Secure; SameSite=Strict$`).MatchString(cookies[0]) {
			t.Errorf("Set-Cookie %q", cookies)
		}
		if r.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("Cache-Control %q", r.Header.Get("Cache-Control"))
		}
		if strings.Contains(string(r.Body), b.Token()) || strings.Contains(string(r.Body), good.Password) {
			t.Error("the body of the response holds the token or the password")
		}
		if r := b.Do(t, "GET", "/v1/me", nil, nil); r.Status != 200 {
			t.Errorf("getMe with the new session: %d", r.Status)
		}
	})

	t.Run("the four refusals look alike", func(t *testing.T) {
		var want *apitest.Response
		for name, c := range map[string][2]string{
			"unknown username": {"nobody.here", "some password 12345"},
			"wrong password":   {"good.person", "not the password 12345"},
			"disabled user":    {"off.person", disabled.Password},
			"expired password": {"old.person", expired.Password},
		} {
			// No delay of the source row may be the difference between the answers.
			d.Age(t, "signin_throttle", "", time.Minute, "last_failure_at")
			r := p.api.NewBrowser().SignIn(t, c[0], c[1])
			if r.Status != 401 || problem(t, r).Code != "unauthenticated" {
				t.Errorf("%s: %d %s", name, r.Status, r.Body)
				continue
			}
			if len(setCookies(r)) != 0 {
				t.Errorf("%s: Set-Cookie %q", name, setCookies(r))
			}
			if want == nil {
				want = r
				continue
			}
			if normalize(r) != normalize(want) {
				t.Errorf("%s: the body differs:\n%s\nwant\n%s", name, normalize(r), normalize(want))
			}
			for _, h := range []string{"Content-Type", "Cache-Control", "Www-Authenticate", "Retry-After"} {
				if r.Header.Get(h) != want.Header.Get(h) {
					t.Errorf("%s: header %s is %q, want %q", name, h, r.Header.Get(h), want.Header.Get(h))
				}
			}
		}
	})

	t.Run("429 with Retry-After", func(t *testing.T) {
		b := p.api.NewBrowser()
		for i := 0; i < 3; i++ {
			b.SignIn(t, "throttled.person", "wrong password 12345")
		}
		r := b.SignIn(t, "throttled.person", "wrong password 12345")
		if code(t, r, 429) != "too_many_attempts" {
			t.Errorf("body %s", r.Body)
		}
		if ra := r.Header.Get("Retry-After"); !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(ra) {
			t.Errorf("Retry-After %q", ra)
		}
	})

	t.Run("local sign-in switched off", func(t *testing.T) {
		off := newPeople(t, apitest.PeopleConfig{Admin: func(o *admin.Options) { o.Auth.LocalEnabled = false }})
		b := off.api.NewBrowser()
		if c := code(t, b.SignIn(t, "good.person", "whatever password 123"), 403); c != "sign_in_disabled" {
			t.Errorf("code %s", c)
		}
		failures := 0
		for _, r := range off.d.FailureRows(t, tenant.ID{}) {
			if r.Detail["reason"] == "local_disabled" {
				failures++
				if r.Detail["method"] != "local" || r.ResourceID != "" || strings.Contains(r.Raw, "good.person") {
					t.Errorf("failure row %+v", r)
				}
			}
		}
		if failures != 1 {
			t.Errorf("%d auth.failure rows with the reason local_disabled, want 1", failures)
		}
		m := decode[adminapi.AuthMethods](t, b.Do(t, "GET", "/v1/auth/methods", nil, nil), 200)
		if m.Local.Enabled || m.Oidc.Enabled {
			t.Errorf("methods %+v", m)
		}
	})

	t.Run("unknown members are reported without echoing them", func(t *testing.T) {
		secretValue, name := apitest.Canary("value"), apitest.Canary("member")
		typed := apitest.Canary("password")
		r := p.api.NewBrowser().Do(t, "POST", "/v1/auth/sign-in", map[string]any{"username": "good.person", "password": typed, name: secretValue}, nil)
		if code(t, r, 400) != "invalid_request" {
			t.Errorf("body %s", r.Body)
		}
		for _, v := range []string{secretValue, typed} {
			if strings.Contains(string(r.Body), v) {
				t.Errorf("the problem echoes %q", v)
			}
		}
		if !strings.Contains(string(r.Body), name) {
			t.Errorf("the problem does not name the unknown member: %s", r.Body)
		}
		for _, body := range []any{map[string]any{"username": "x"}, map[string]any{"password": "x"}, map[string]any{"username": "", "password": "x"}, `{"username":`, []any{}} {
			if r := p.api.NewBrowser().Do(t, "POST", "/v1/auth/sign-in", body, nil); r.Status != 400 {
				t.Errorf("body %v: %d", body, r.Status)
			}
		}
	})

	t.Run("503 when the hasher is busy", func(t *testing.T) {
		slow := password.Params{MemoryKiB: 262144, Iterations: 4, Parallelism: 1}
		busy := newPeople(t, apitest.PeopleConfig{Params: &slow, MaxHashes: 1})
		var wg sync.WaitGroup
		statuses := make(chan *apitest.Response, 10)
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				b := busy.api.NewBrowser()
				statuses <- b.SignIn(t, "nobody.at.all", "some password 12345")
			}()
		}
		wg.Wait()
		close(statuses)
		saw503 := false
		for r := range statuses {
			if r.Status == 503 {
				saw503 = true
				if code(t, r, 503) != "unavailable" || r.Header.Get("Retry-After") != "1" {
					t.Errorf("503 without code unavailable and Retry-After: 1: %s %v", r.Body, r.Header)
				}
			} else if r.Status != 401 && r.Status != 429 {
				t.Errorf("status %d", r.Status)
			}
		}
		if !saw503 {
			t.Error("no request was answered 503 with ten sign-ins at once on a hasher that runs one computation")
		}
	})
}

// TestSessionPrincipal is T-791: the table of section 5.3.
func TestSessionPrincipal(t *testing.T) {
	p := newPeople(t, apitest.PeopleConfig{})
	d := p.d
	suspended, _ := p.api.Tenant(t, "frozen")
	if r := p.platform(t, "POST", "/v1/tenants/"+suspended.ID+"/suspend", map[string]any{}); r.Status != 200 {
		t.Fatalf("suspend: %d", r.Status)
	}
	stid, _ := tenant.ParseID(suspended.ID)
	other, _ := p.api.Tenant(t, "globex")

	admin := d.PlatformUser(t, "both.person", []string{"platform-admin"})
	d.Member(t, p.tid, admin.ID, []string{"tenant-admin"})
	d.Member(t, stid, admin.ID, []string{"tenant-admin"})
	bare := d.PlatformUser(t, "bare.person", nil)
	member := d.TenantUser(t, p.tid, "member.person", []string{"tenant-admin"})

	ba, bb, bm := p.sessionOf(t, admin.ID), p.sessionOf(t, bare.ID), p.sessionOf(t, member.ID)
	status := func(b *apitest.Browser, path string) int { return b.Do(t, "GET", path, nil, nil).Status }
	list := "/v1/tenants/" + p.tenant.ID + "/service-accounts"

	// A platform operation: with and without a platform role.
	if s := status(ba, "/v1/tenants"); s != 200 {
		t.Errorf("platform operation with a platform role: %d", s)
	}
	for name, b := range map[string]*apitest.Browser{"no role": bb, "tenant role only": bm} {
		if c := code(t, b.Do(t, "GET", "/v1/tenants", nil, nil), 403); c != "forbidden" {
			t.Errorf("%s: code %s", name, c)
		}
	}
	// A tenant operation: as a member, as a non-member (identical to an unknown tenant), in a suspended tenant.
	if s := status(bm, list); s != 200 {
		t.Errorf("tenant operation as a member: %d", s)
	}
	nonMember := bb.Do(t, "GET", list, nil, nil)
	unknown := bb.Do(t, "GET", "/v1/tenants/"+apitest.NoUser+"/service-accounts", nil, nil)
	if code(t, nonMember, 404) != "not_found" || normalize(nonMember) != normalize(unknown) {
		t.Errorf("a non-member is not told apart from an unknown tenant:\n%s\n%s", nonMember.Body, unknown.Body)
	}
	if c := code(t, bm.Do(t, "GET", "/v1/tenants/"+other.ID+"/service-accounts", nil, nil), 404); c != "not_found" {
		t.Errorf("another tenant's operation: %s", c)
	}
	if c := code(t, ba.Do(t, "GET", "/v1/tenants/"+suspended.ID+"/service-accounts", nil, nil), 403); c != "tenant_suspended" {
		t.Errorf("a suspended tenant: %s", c)
	}

	// getMe for a user with two memberships.
	me := decode[adminapi.Me](t, ba.Do(t, "GET", "/v1/me", nil, nil), 200)
	if me.PrincipalType != "user" || me.ID != admin.ID || me.Name != "both.person" || me.KeyID != "" || me.Bootstrap || me.Tenant != nil ||
		strings.Join(me.Roles, ",") != "platform-admin" || me.Session == nil || me.Session.Method != "local" || !me.Session.Current {
		t.Errorf("me: %+v", me)
	}
	if len(me.Memberships) != 2 {
		t.Fatalf("memberships %+v, want two", me.Memberships)
	}
	for _, m := range me.Memberships {
		if strings.Join(m.Roles, ",") != "tenant-admin" || (m.Tenant.ID != p.tenant.ID && m.Tenant.ID != suspended.ID) {
			t.Errorf("membership %+v", m)
		}
	}
	for _, perm := range []string{"identity:read", "session:self", "user:read"} {
		found := false
		for _, e := range me.Permissions {
			found = found || e == perm
		}
		if !found {
			t.Errorf("permissions %v lack %s", me.Permissions, perm)
		}
	}
	// A user without roles still reads itself and its sessions.
	if s := status(bb, "/v1/me"); s != 200 {
		t.Errorf("getMe without roles: %d", s)
	}
	if s := status(bb, "/v1/auth/sessions"); s != 200 {
		t.Errorf("listOwnSessions without roles: %d", s)
	}

	// session:self is refused for a key: it has no session.
	for name, key := range map[string]string{"platform key": p.api.PlatformKey(), "tenant key": p.adminKey} {
		r := p.api.Do(t, "GET", "/v1/auth/sessions", key, nil, nil)
		if c := code(t, r, 403); c != "forbidden" {
			t.Errorf("%s: %s", name, c)
		}
		if r := p.api.Do(t, "POST", "/v1/auth/sign-out", key, map[string]any{}, nil); r.Status != 403 {
			t.Errorf("%s signOut: %d", name, r.Status)
		}
	}
}

// TestCredentialPrecedence is T-792.
func TestCredentialPrecedence(t *testing.T) {
	p := newPeople(t, apitest.PeopleConfig{})
	user := p.d.PlatformUser(t, "cookie.person", nil)
	b := p.sessionOf(t, user.ID)
	token := b.Token()
	get := func(key, cookie string) *apitest.Response {
		bb := p.api.NewBrowser()
		if cookie != "" {
			bb.SetCookie(session.CookieName, cookie)
		}
		var h http.Header
		if key != "" {
			h = http.Header{"Authorization": {"Bearer " + key}}
		}
		return bb.Do(t, "GET", "/v1/me", nil, h)
	}
	// The key and a cookie of someone else: the key, the cookie ignored.
	if me := decode[adminapi.Me](t, get(p.api.PlatformKey(), token), 200); me.PrincipalType != "platform" {
		t.Errorf("a key beside a cookie: %+v", me)
	}
	// A bad key and a good cookie: 401, the cookie is not a fallback.
	if r := get("pdk_not_a_key", token); r.Status != 401 {
		t.Errorf("a bad key beside a good cookie: %d", r.Status)
	}
	// A cookie alone is a session.
	if me := decode[adminapi.Me](t, get("", token), 200); me.PrincipalType != "user" || me.ID != user.ID {
		t.Errorf("a cookie alone: %+v", me)
	}
	// Neither.
	if r := get("", ""); r.Status != 401 {
		t.Errorf("no credential: %d", r.Status)
	}
	// A cookie that is not a session token, and a token in the Authorization header.
	if r := get("", "not-a-token"); r.Status != 401 {
		t.Errorf("a cookie that is not a token: %d", r.Status)
	}
	if r := get(token, ""); r.Status != 401 {
		t.Errorf("a session token in Authorization: %d", r.Status)
	}
	// The same on a state-changing request: a key needs no CSRF header, a cookie does.
	nb := p.api.NewBrowser()
	nb.NoCSRF, nb.NoOrigin = true, true
	if r := nb.Do(t, "POST", "/v1/platform-keys", map[string]any{"description": "by a key"}, http.Header{"Authorization": {"Bearer " + p.api.PlatformKey()}}); r.Status != 201 {
		t.Errorf("a key without CSRF headers: %d %s", r.Status, r.Body)
	}
}

// TestSessionLifetimes is T-793. Rows are aged rather than the clock moved: SQL stamps them with now().
func TestSessionLifetimes(t *testing.T) {
	p := newPeople(t, apitest.PeopleConfig{})
	d := p.d
	u := d.PlatformUser(t, "life.person", nil)
	read := func(id string) (created, used, expires time.Time) {
		if err := d.Super(t).QueryRow(ctx(t), "SELECT created_at, last_used_at, expires_at FROM pandokos.user_session WHERE id = $1::uuid", id).Scan(&created, &used, &expires); err != nil {
			t.Fatal(err)
		}
		return
	}

	t.Run("last_used_at is written at most once a minute", func(t *testing.T) {
		b := p.api.NewBrowser()
		s := decode[adminapi.Session](t, b.SignIn(t, u.Username, u.Password), 200)
		_, first, _ := read(s.ID)
		b.Do(t, "GET", "/v1/me", nil, nil)
		b.Do(t, "GET", "/v1/me", nil, nil)
		if _, again, _ := read(s.ID); !again.Equal(first) {
			t.Errorf("last_used_at moved within a minute: %v -> %v", first, again)
		}
		d.Age(t, "user_session", "id = '"+s.ID+"'", 2*time.Minute, "last_used_at")
		_, aged, _ := read(s.ID)
		b.Do(t, "GET", "/v1/me", nil, nil)
		if _, used, _ := read(s.ID); !used.After(aged.Add(time.Minute)) || since(used) > 10*time.Second {
			t.Errorf("last_used_at %v was not written after two minutes without a write (it was %v)", used, aged)
		}
	})

	t.Run("idle expiry, and use keeps a session alive", func(t *testing.T) {
		b := p.api.NewBrowser()
		s := decode[adminapi.Session](t, b.SignIn(t, u.Username, u.Password), 200)
		for i := 0; i < 3; i++ { // each use is 29 minutes after the last one
			d.Age(t, "user_session", "id = '"+s.ID+"'", 29*time.Minute, "last_used_at")
			if r := b.Do(t, "GET", "/v1/me", nil, nil); r.Status != 200 {
				t.Fatalf("29 minutes after the last use, round %d: %d", i, r.Status)
			}
		}
		d.Age(t, "user_session", "id = '"+s.ID+"'", 31*time.Minute, "last_used_at")
		r := b.Do(t, "GET", "/v1/me", nil, nil)
		if code(t, r, 401) != "unauthenticated" || !clearsSession(r) {
			t.Errorf("31 minutes without use: %d, cookie cleared: %v", r.Status, clearsSession(r))
		}
		if r := b.Do(t, "GET", "/v1/me", nil, nil); r.Status != 401 {
			t.Errorf("the cookie was cleared, the next request: %d", r.Status)
		}
	})

	t.Run("absolute expiry although used", func(t *testing.T) {
		b := p.api.NewBrowser()
		s := decode[adminapi.Session](t, b.SignIn(t, u.Username, u.Password), 200)
		if r := b.Do(t, "GET", "/v1/me", nil, nil); r.Status != 200 {
			t.Fatal("not signed in")
		}
		d.Age(t, "user_session", "id = '"+s.ID+"'", 13*time.Hour, "created_at", "expires_at") // used a moment ago, born 13 hours ago
		r := b.Do(t, "GET", "/v1/me", nil, nil)
		if code(t, r, 401) != "unauthenticated" || !clearsSession(r) {
			t.Errorf("past the absolute lifetime: %d, cookie cleared: %v", r.Status, clearsSession(r))
		}
	})

	t.Run("a session that does not exist clears the cookie", func(t *testing.T) {
		b := p.api.NewBrowser()
		tok, _, _ := session.NewToken()
		b.SetCookie(session.CookieName, tok.Reveal())
		r := b.Do(t, "GET", "/v1/me", nil, nil)
		if r.Status != 401 || !clearsSession(r) {
			t.Errorf("%d, cookie cleared: %v", r.Status, clearsSession(r))
		}
	})
}

// TestSignOutAndOwnSessions is T-794.
func TestSignOutAndOwnSessions(t *testing.T) {
	p := newPeople(t, apitest.PeopleConfig{})
	d := p.d
	u := d.PlatformUser(t, "own.person", nil)
	v := d.PlatformUser(t, "other.person", nil)

	b1, b2, bv := p.signIn(t, u), p.signIn(t, u), p.signIn(t, v)
	list := decode[adminapi.SessionList](t, b1.Do(t, "GET", "/v1/auth/sessions", nil, nil), 200)
	if len(list.Items) != 2 {
		t.Fatalf("%d sessions, want 2", len(list.Items))
	}
	var current, other adminapi.Session
	for _, s := range list.Items {
		if s.User.ID != u.ID {
			t.Errorf("a session of another user: %+v", s)
		}
		if s.Current {
			current = s
		} else {
			other = s
		}
	}
	if current.ID == "" || other.ID == "" {
		t.Fatalf("exactly one session is the current one: %+v", list.Items)
	}
	// Ending another session of one's own.
	ended := decode[adminapi.Session](t, b1.Do(t, "DELETE", "/v1/auth/sessions/"+other.ID, nil, nil), 200)
	if ended.ID != other.ID {
		t.Errorf("deleted %+v", ended)
	}
	if r := b2.Do(t, "GET", "/v1/me", nil, nil); r.Status != 401 {
		t.Errorf("the session that was ended: %d", r.Status)
	}
	// Not another user's session, nor one that does not exist.
	vlist := decode[adminapi.SessionList](t, bv.Do(t, "GET", "/v1/auth/sessions", nil, nil), 200)
	if code(t, b1.Do(t, "DELETE", "/v1/auth/sessions/"+vlist.Items[0].ID, nil, nil), 404) != "not_found" {
		t.Error("a session of another user is not a 404")
	}
	if r := bv.Do(t, "GET", "/v1/me", nil, nil); r.Status != 200 {
		t.Errorf("the other user's session was ended: %d", r.Status)
	}
	if code(t, b1.Do(t, "DELETE", "/v1/auth/sessions/"+apitest.NoUser, nil, nil), 404) != "not_found" {
		t.Error("a session that does not exist is not a 404")
	}

	// Sign-out.
	token := b1.Token()
	r := b1.Do(t, "POST", "/v1/auth/sign-out", map[string]any{}, nil)
	if so := decode[adminapi.SignedOut](t, r, 200); !so.SignedOut || !clearsSession(r) {
		t.Errorf("sign-out: %+v cleared=%v", so, clearsSession(r))
	}
	replay := p.api.NewBrowser()
	replay.SetCookie(session.CookieName, token)
	if r := replay.Do(t, "GET", "/v1/me", nil, nil); r.Status != 401 {
		t.Errorf("the token after sign-out: %d", r.Status)
	}
	if n := d.Count(t, "SELECT count(*) FROM pandokos.user_session WHERE user_id = $1::uuid", u.ID); n != 0 {
		t.Errorf("%d sessions of the user remain", n)
	}
}

// TestDisableEndsSessions is T-795.
func TestDisableEndsSessions(t *testing.T) {
	p := newPeople(t, apitest.PeopleConfig{})
	d := p.d
	twice := func(who dbtest.Person) (*apitest.Browser, *apitest.Browser) {
		return p.signIn(t, who), p.signIn(t, who)
	}
	dead := func(what string, bs ...*apitest.Browser) {
		t.Helper()
		for _, b := range bs {
			if r := b.Do(t, "GET", "/v1/me", nil, nil); r.Status != 401 {
				t.Errorf("%s: a session still works: %d", what, r.Status)
			}
		}
	}

	t.Run("disabling a user", func(t *testing.T) {
		u := d.PlatformUser(t, "disable.user", nil)
		a, b := twice(u)
		if r := p.platform(t, "POST", "/v1/users/"+u.ID+"/disable", map[string]any{}); r.Status != 200 {
			t.Fatalf("disableUser: %d %s", r.Status, r.Body)
		}
		dead("disableUser", a, b)
		if r := p.platform(t, "POST", "/v1/users/"+u.ID+"/enable", map[string]any{}); r.Status != 200 {
			t.Fatalf("enableUser: %d", r.Status)
		}
		dead("after enableUser", a, b) // enabling brings no session back
		if r := p.api.NewBrowser().SignIn(t, u.Username, u.Password); r.Status != 200 {
			t.Errorf("sign-in after enabling: %d", r.Status)
		}
	})

	t.Run("disabling a member", func(t *testing.T) {
		u := d.TenantUser(t, p.tid, "disable.member", []string{"tenant-admin"})
		a, b := twice(u)
		if r := p.tenantKey(t, "POST", p.membersPath("/"+u.ID+"/disable"), map[string]any{}); r.Status != 200 {
			t.Fatalf("disableMember: %d %s", r.Status, r.Body)
		}
		dead("disableMember", a, b)
		if r := p.tenantKey(t, "POST", p.membersPath("/"+u.ID+"/enable"), map[string]any{}); r.Status != 200 {
			t.Fatalf("enableMember: %d", r.Status)
		}
		dead("after enableMember", a, b)
	})

	t.Run("removing the membership of a user the tenant manages", func(t *testing.T) {
		u := d.TenantUser(t, p.tid, "removed.member", []string{"tenant-admin"})
		a, b := twice(u)
		if r := p.tenantKey(t, "DELETE", p.membersPath("/"+u.ID), nil); r.Status != 200 {
			t.Fatalf("deleteMember: %d %s", r.Status, r.Body)
		}
		dead("deleteMember", a, b)
		if r := p.api.NewBrowser().SignIn(t, u.Username, u.Password); r.Status != 401 {
			t.Errorf("a removed member signs in: %d", r.Status)
		}
	})
}

// TestRevokeSessions is T-796.
func TestRevokeSessions(t *testing.T) {
	p := newPeople(t, apitest.PeopleConfig{})
	d := p.d
	u := d.PlatformUser(t, "revoke.user", nil)
	bystander := d.PlatformUser(t, "bystander", nil)
	m := d.TenantUser(t, p.tid, "revoke.member", []string{"tenant-admin"})
	mb := d.TenantUser(t, p.tid, "bystander.member", []string{"tenant-admin"})

	for name, c := range map[string]struct {
		who, by dbtest.Person
		path    string
		call    func(string) *apitest.Response
	}{
		"revokeUserSessions":   {u, bystander, "/v1/users/" + u.ID + "/revoke-sessions", func(path string) *apitest.Response { return p.platform(t, "POST", path, map[string]any{}) }},
		"revokeMemberSessions": {m, mb, p.membersPath("/" + m.ID + "/revoke-sessions"), func(path string) *apitest.Response { return p.tenantKey(t, "POST", path, map[string]any{}) }},
	} {
		a, b, c3 := p.signIn(t, c.who), p.signIn(t, c.who), p.signIn(t, c.who)
		by := p.signIn(t, c.by)
		out := decode[adminapi.SessionsRevoked](t, c.call(c.path), 200)
		if out.Revoked != 3 {
			t.Errorf("%s: revoked %d, want 3", name, out.Revoked)
		}
		for _, x := range []*apitest.Browser{a, b, c3} {
			if r := x.Do(t, "GET", "/v1/me", nil, nil); r.Status != 401 {
				t.Errorf("%s: a session survived: %d", name, r.Status)
			}
		}
		if r := by.Do(t, "GET", "/v1/me", nil, nil); r.Status != 200 {
			t.Errorf("%s: the session of another user was ended: %d", name, r.Status)
		}
		if out := decode[adminapi.SessionsRevoked](t, c.call(c.path), 200); out.Revoked != 0 {
			t.Errorf("%s: a second revocation ended %d", name, out.Revoked)
		}
	}
	// A platform-managed member is not revoked by the tenant.
	shared := d.PlatformUser(t, "shared.user", nil)
	d.Member(t, p.tid, shared.ID, []string{"tenant-admin"})
	if c := code(t, p.tenantKey(t, "POST", p.membersPath("/"+shared.ID+"/revoke-sessions"), map[string]any{}), 409); c != "invalid_state" {
		t.Errorf("code %s", c)
	}
}
