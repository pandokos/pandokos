package admin_test

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/oidc"
	"github.com/pandokos/pandokos/internal/session"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeidp"
)

var idpPeople = map[string]fakeidp.Claims{
	"ada":  {"name": "Ada Lovelace", "email": "ada@example.test", "groups": []string{"admins", "eng", "other"}},
	"bob":  {"name": "Bob Builder", "email": "bob@example.test", "groups": []string{"admins", "eng"}},
	"carl": {"name": "Carl", "email": "carl@example.test"},
	"dan":  {"name": "Dan", "email": "dan@example.test", "groups": []string{"unmapped"}},
}

var reSessionCookie = regexp.MustCompile(`^__Host-pandokos_session=pds_[A-Za-z0-9_-]{43}; Path=/; HttpOnly; Secure; SameSite=Strict$`)

func last(h []apitest.Hop) apitest.Hop { return h[len(h)-1] }

// failedRedirect reports whether the hop sends the browser to the failure target.
func failedRedirect(h apitest.Hop, post, reason string) bool {
	u, err := url.Parse(h.Location)
	return err == nil && h.Status == 303 && u.Path == post && u.Query().Get("sign_in_error") == reason
}

// TestOidcSignIn is T-814.
func TestOidcSignIn(t *testing.T) {
	api, o := apitest.NewPeopleOIDC(t, apitest.PeopleConfig{}, apitest.OIDCConfig{Users: idpPeople})
	p := peopleOn(t, api)
	d := p.d
	issuer := o.IDP.Issuer()

	methods := decode[adminapi.AuthMethods](t, api.NewBrowser().Do(t, "GET", "/v1/auth/methods", nil, nil), 200)
	if !methods.Local.Enabled || !methods.Oidc.Enabled || methods.Oidc.DisplayName != "Acme single sign-on" || methods.Oidc.StartPath != "/v1/auth/oidc/start" {
		t.Errorf("methods %+v", methods)
	}

	t.Run("a linked subject signs in", func(t *testing.T) {
		ada := d.FederatedUser(t, issuer, "ada", []string{"platform-admin"}, dbtest.Display("Old Name", "old@example.test"))
		o.IDP.SignInAs("ada")
		b := api.NewBrowser()
		hops := o.SignIn(t, b)
		if len(hops) < 2 || hops[0].Status != 303 || !strings.HasPrefix(hops[0].Location, o.IDP.AuthorizeURL()) {
			t.Fatalf("hops %+v", hops)
		}
		if c := strings.Join(hops[0].Header.Values("Set-Cookie"), "|"); !strings.HasPrefix(c, "__Host-pandokos_oidc=") || !strings.Contains(c, "SameSite=Lax") || !strings.Contains(c, "Max-Age=600") || !strings.Contains(c, "Secure") || !strings.Contains(c, "HttpOnly") {
			t.Errorf("the start sets %q", c)
		}
		end := last(hops)
		if end.Status != 303 || end.Location != "/console" {
			t.Fatalf("the callback ends with %d %q, want 303 /console (%s)", end.Status, end.Location, end.Body)
		}
		var sess, cleared string
		for _, c := range end.Header.Values("Set-Cookie") {
			switch {
			case strings.HasPrefix(c, session.CookieName+"="):
				sess = c
			case strings.HasPrefix(c, "__Host-pandokos_oidc=;"):
				cleared = c
			}
		}
		if !reSessionCookie.MatchString(sess) || cleared == "" {
			t.Errorf("the callback sets %q", end.Header.Values("Set-Cookie"))
		}
		me := decode[adminapi.Me](t, b.Do(t, "GET", "/v1/me", nil, nil), 200)
		if me.PrincipalType != "user" || me.ID != ada.ID || me.Session == nil || me.Session.Method != "oidc" || strings.Join(me.Roles, ",") != "platform-admin" {
			t.Errorf("me %+v", me)
		}
		list := decode[adminapi.SessionList](t, b.Do(t, "GET", "/v1/auth/sessions", nil, nil), 200)
		if len(list.Items) != 1 || list.Items[0].IdleTimeoutSeconds != 1800 || list.Items[0].Method != "oidc" || !list.Items[0].Current {
			t.Errorf("sessions %+v", list.Items)
		}
		// Display attributes come from the claims at each sign-in.
		u := decode[adminapi.User](t, p.platform(t, "GET", "/v1/users/"+ada.ID, nil), 200)
		if u.DisplayName != "Ada Lovelace" || u.Email != "ada@example.test" || u.Subject != "ada" || u.SignIn != "oidc" {
			t.Errorf("user %+v", u)
		}
		// The session works for state changes with the headers a page sends.
		if r := b.Do(t, "POST", "/v1/auth/sign-out", map[string]any{}, nil); r.Status != 200 {
			t.Errorf("sign-out: %d", r.Status)
		}
	})

	t.Run("a disabled user is refused", func(t *testing.T) {
		bob := d.FederatedUser(t, issuer, "bob", nil, dbtest.Disabled())
		o.IDP.SignInAs("bob")
		b := api.NewBrowser()
		end := last(o.SignIn(t, b))
		if !failedRedirect(end, "/console", "sign_in_failed") || b.Token() != "" {
			t.Errorf("end %d %q, session cookie %q", end.Status, end.Location, b.Token())
		}
		var reasons []any
		for _, r := range d.FailureRows(t, tenant.ID{}) {
			if r.ResourceID == bob.ID {
				reasons = append(reasons, r.Detail["reason"])
				if r.Detail["method"] != "oidc" || r.Detail["listener"] != "admin" {
					t.Errorf("failure row %+v", r)
				}
			}
		}
		if len(reasons) != 1 || reasons[0] != "user_disabled" {
			t.Errorf("reasons %v, want [user_disabled]", reasons)
		}
		if n := d.Count(t, "SELECT count(*) FROM pandokos.user_session WHERE user_id = $1::uuid", bob.ID); n != 0 {
			t.Errorf("%d sessions of a disabled user", n)
		}
	})

	t.Run("an unlinked subject without a rule is refused", func(t *testing.T) {
		users := d.Count(t, "SELECT count(*) FROM pandokos.app_user")
		o.IDP.SignInAs("carl")
		b := api.NewBrowser()
		end := last(o.SignIn(t, b))
		if !failedRedirect(end, "/console", "sign_in_failed") || b.Token() != "" {
			t.Errorf("end %d %q", end.Status, end.Location)
		}
		if n := d.Count(t, "SELECT count(*) FROM pandokos.app_user"); n != users {
			t.Error("a user was created for an unlinked subject")
		}
		found := false
		for _, r := range d.FailureRows(t, tenant.ID{}) {
			if r.Detail["reason"] == "unmapped_subject" {
				found = true
				if r.ResourceID != "" || strings.Contains(r.Raw, "carl") {
					t.Errorf("the row of an unmapped subject names it: %+v", r)
				}
			}
		}
		if !found {
			t.Error("no auth.failure row with the reason unmapped_subject")
		}
	})
}

// TestGroupReconciliation is T-815.
func TestGroupReconciliation(t *testing.T) {
	var captured admin.Options
	api, o := apitest.NewPeopleOIDC(t,
		apitest.PeopleConfig{Admin: func(a *admin.Options) { captured = *a }},
		apitest.OIDCConfig{Users: idpPeople, Mappings: []string{"platform:platform-admin=admins", "tenant:acme:tenant-admin=eng"}})
	p := peopleOn(t, api)
	d := p.d
	issuer := o.IDP.Issuer()
	globex, _ := api.Tenant(t, "globex")
	gtid, _ := tenant.ParseID(globex.ID)

	ada := d.FederatedUser(t, issuer, "ada", nil)
	bob := d.FederatedUser(t, issuer, "bob", []string{"platform-admin"})
	d.Member(t, p.tid, bob.ID, []string{"tenant-admin"}) // manual rows
	d.Member(t, gtid, bob.ID, []string{"tenant-admin"})  // a tenant that no rule names

	roles := func(table, id string) string {
		var s string
		if err := d.Super(t).QueryRow(ctx(t), "SELECT COALESCE(string_agg(role || ':' || source, ',' ORDER BY role), '') FROM pandokos.platform_role_assignment WHERE user_id = $1::uuid", id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	memberRoles := func(tid tenant.ID, id string) string {
		var s string
		if err := d.Super(t).QueryRow(ctx(t), "SELECT COALESCE(string_agg(r.role || ':' || r.source, ',' ORDER BY r.role), '') FROM pandokos.membership_role r WHERE r.tenant_id = $1::uuid AND r.user_id = $2::uuid", tid.String(), id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	signIn := func(sub string) {
		t.Helper()
		o.IDP.SignInAs(sub)
		if end := last(o.SignIn(t, api.NewBrowser())); end.Location != "/console" {
			t.Fatalf("%s: the sign-in ends with %d %q", sub, end.Status, end.Location)
		}
	}

	// First sign-in: the rules add what the groups justify.
	beforeP, beforeT := len(d.PlatformAuditRows(t)), len(d.AuditRows(t, p.tid))
	signIn("ada")
	if got := roles("platform", ada.ID); got != "platform-admin:idp" {
		t.Errorf("platform roles %q, want platform-admin:idp", got)
	}
	if got := memberRoles(p.tid, ada.ID); got != "tenant-admin:idp" {
		t.Errorf("roles in acme %q, want tenant-admin:idp", got)
	}
	var assigned, added int
	for _, r := range d.PlatformAuditRows(t)[beforeP:] {
		if r.Action == "platform_role.assign" && r.Detail["source"] == "idp" && strings.Contains(r.Raw, ada.ID) {
			assigned++
		}
	}
	for _, r := range d.AuditRows(t, p.tid)[beforeT:] {
		if r.Action == "member.add" && r.Detail["source"] == "idp" && r.ResourceID == ada.ID {
			added++
		}
	}
	if assigned != 1 || added != 1 {
		t.Errorf("%d platform_role.assign and %d member.add rows with source idp, want one of each", assigned, added)
	}
	// A second sign-in with the same groups changes nothing and audits nothing.
	beforeP, beforeT = len(d.PlatformAuditRows(t)), len(d.AuditRows(t, p.tid))
	signIn("ada")
	for _, r := range d.PlatformAuditRows(t)[beforeP:] {
		if strings.HasPrefix(r.Action, "platform_role.") {
			t.Errorf("a sign-in with the same groups wrote %s", r.Action)
		}
	}
	for _, r := range d.AuditRows(t, p.tid)[beforeT:] {
		if strings.HasPrefix(r.Action, "member.") {
			t.Errorf("a sign-in with the same groups wrote %s", r.Action)
		}
	}

	// Manual rows are never touched and a role that exists as manual is not duplicated.
	signIn("bob")
	if got := roles("platform", bob.ID); got != "platform-admin:manual" {
		t.Errorf("bob's platform roles %q, want platform-admin:manual", got)
	}
	if got := memberRoles(p.tid, bob.ID); got != "tenant-admin:manual" {
		t.Errorf("bob's roles in acme %q, want tenant-admin:manual", got)
	}

	// The provider withdraws the groups: exactly what the rules gave is removed.
	o.IDP.IDTokenClaims(func(c fakeidp.Claims) fakeidp.Claims { return c.With("groups", []string{}) })
	beforeP, beforeT = len(d.PlatformAuditRows(t)), len(d.AuditRows(t, p.tid))
	signIn("ada")
	if got := roles("platform", ada.ID); got != "" {
		t.Errorf("platform roles after the groups were withdrawn: %q", got)
	}
	if n := d.Count(t, "SELECT count(*) FROM pandokos.membership WHERE user_id = $1::uuid", ada.ID); n != 0 {
		t.Errorf("%d memberships after the groups were withdrawn", n)
	}
	var removedRole, removedMember bool
	for _, r := range d.PlatformAuditRows(t)[beforeP:] {
		removedRole = removedRole || (r.Action == "platform_role.remove" && strings.Contains(r.Raw, ada.ID))
	}
	for _, r := range d.AuditRows(t, p.tid)[beforeT:] {
		removedMember = removedMember || (r.Action == "member.remove" && r.ResourceID == ada.ID)
	}
	if !removedRole || !removedMember {
		t.Errorf("removals audited: platform role %v, member %v", removedRole, removedMember)
	}
	signIn("bob")
	if roles("platform", bob.ID) != "platform-admin:manual" || memberRoles(p.tid, bob.ID) != "tenant-admin:manual" || memberRoles(gtid, bob.ID) != "tenant-admin:manual" {
		t.Error("a manual row did not survive the withdrawal of the groups")
	}

	// A rule that names a tenant that does not exist stops the start-up.
	rules, err := oidc.ParseRules([]string{"tenant:ghost:tenant-admin=eng"})
	if err != nil {
		t.Fatal(err)
	}
	bad := captured
	bad.Auth.GroupRules = rules
	if _, err := admin.New(bad); err == nil {
		t.Error("admin.New accepted a group rule that names a tenant that does not exist")
	}
	// A rule that names a role that a user cannot hold likewise.
	rules, err = oidc.ParseRules([]string{"platform:inference=eng"})
	if err == nil {
		bad.Auth.GroupRules = rules
		if _, err := admin.New(bad); err == nil {
			t.Error("admin.New accepted a group rule that names a role a user cannot hold")
		}
	}
	good := captured
	good.Auth.GroupRules = o.Rules
	if _, err := admin.New(good); err != nil {
		t.Errorf("admin.New with the rules of the test: %v", err)
	}
}

// TestAutoProvision is T-816.
func TestAutoProvision(t *testing.T) {
	mappings := []string{"platform:platform-admin=admins"}
	t.Run("on", func(t *testing.T) {
		api, o := apitest.NewPeopleOIDC(t, apitest.PeopleConfig{}, apitest.OIDCConfig{Users: idpPeople, Mappings: mappings, AutoProvision: true})
		p := peopleOn(t, api)
		d := p.d
		users := d.Count(t, "SELECT count(*) FROM pandokos.app_user")

		o.IDP.SignInAs("ada") // groups: admins, eng, other: one is mapped
		b := api.NewBrowser()
		if end := last(o.SignIn(t, b)); end.Location != "/console" {
			t.Fatalf("an unknown subject with a mapped group: %d %q", end.Status, end.Location)
		}
		var id, signIn, display, email, state string
		var tenantID *string
		if err := d.Super(t).QueryRow(ctx(t), `SELECT u.id::text, u.sign_in, u.display_name, u.email, u.state, u.tenant_id::text
			FROM pandokos.app_user u JOIN pandokos.user_identity i ON i.user_id = u.id WHERE i.subject = 'ada' AND i.issuer = $1`, o.IDP.Issuer()).Scan(&id, &signIn, &display, &email, &state, &tenantID); err != nil {
			t.Fatalf("the provisioned user: %v", err)
		}
		if signIn != "oidc" || display != "Ada Lovelace" || email != "ada@example.test" || state != "active" || tenantID != nil {
			t.Errorf("user %s %s %s %s %v", signIn, display, email, state, tenantID)
		}
		var roles string
		_ = d.Super(t).QueryRow(ctx(t), "SELECT string_agg(role || ':' || source, ',') FROM pandokos.platform_role_assignment WHERE user_id = $1::uuid", id).Scan(&roles)
		if roles != "platform-admin:idp" {
			t.Errorf("roles %q", roles)
		}
		if me := decode[adminapi.Me](t, b.Do(t, "GET", "/v1/me", nil, nil), 200); me.ID != id {
			t.Errorf("me %+v", me)
		}

		// A mapped group is needed: without one the subject is refused and no row is created.
		for _, sub := range []string{"carl", "dan"} {
			before := d.Count(t, "SELECT count(*) FROM pandokos.app_user")
			o.IDP.SignInAs(sub)
			b := api.NewBrowser()
			end := last(o.SignIn(t, b))
			if !failedRedirect(end, "/console", "sign_in_failed") || b.Token() != "" {
				t.Errorf("%s: %d %q", sub, end.Status, end.Location)
			}
			if n := d.Count(t, "SELECT count(*) FROM pandokos.app_user"); n != before {
				t.Errorf("%s: a user was created", sub)
			}
		}
		if n := d.Count(t, "SELECT count(*) FROM pandokos.app_user"); n != users+1 {
			t.Errorf("%d users, want %d", n, users+1)
		}
	})

	t.Run("off", func(t *testing.T) {
		api, o := apitest.NewPeopleOIDC(t, apitest.PeopleConfig{}, apitest.OIDCConfig{Users: idpPeople, Mappings: mappings})
		p := peopleOn(t, api)
		before := p.d.Count(t, "SELECT count(*) FROM pandokos.app_user")
		o.IDP.SignInAs("ada")
		b := api.NewBrowser()
		end := last(o.SignIn(t, b))
		if !failedRedirect(end, "/console", "sign_in_failed") || b.Token() != "" {
			t.Errorf("%d %q", end.Status, end.Location)
		}
		if n := p.d.Count(t, "SELECT count(*) FROM pandokos.app_user"); n != before {
			t.Error("a user was created with auto_provision off")
		}
	})
}

// TestOidcRateBound is T-817: the table of sign-in transactions is bounded.
func TestOidcRateBound(t *testing.T) {
	api, o := apitest.NewPeopleOIDC(t, apitest.PeopleConfig{}, apitest.OIDCConfig{Users: idpPeople})
	d := api.DB
	if _, err := d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.oidc_login (state_hash, created_at, expires_at)
		SELECT sha256(('state-' || g)::bytea), now(), now() + interval '10 minutes' FROM generate_series(1, 10001) g`); err != nil {
		t.Fatal(err)
	}
	b := api.NewBrowser()
	r := b.Do(t, "GET", "/v1/auth/oidc/start", nil, nil)
	if code(t, r, 429) != "too_many_attempts" || r.Header.Get("Retry-After") == "" {
		t.Errorf("start with 10001 live transactions: %d %v", r.Status, r.Header)
	}
	// They expire and are purged: the start works again.
	d.Age(t, "oidc_login", "", 20*time.Minute, "created_at", "expires_at")
	if err := api.People.Accounts.Purge(ctx(t)); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if n := d.Count(t, "SELECT count(*) FROM pandokos.oidc_login"); n != 0 {
		t.Errorf("%d transactions after the purge", n)
	}
	if start := o.Start(t, b); start.Status != 303 {
		t.Errorf("start after the purge: %d", start.Status)
	}
}
