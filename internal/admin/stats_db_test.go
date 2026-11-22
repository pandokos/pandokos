package admin_test

import (
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
)

// TestSupportAndGrantStats is T-921: opening, closing (by the platform) and ending (by the tenant) a
// support action, and a refused grant, are counted once the work is done; a request that is refused
// or fails changes nothing; the counts agree with the log lines `grant refused`.
func TestSupportAndGrantStats(t *testing.T) {
	w := newSW(t)
	valid := []string{authz.MemberRead, authz.ServiceAccountRead}
	want := apitest.AdminCounts{SupportEnded: map[string]int64{}, OIDC: map[string]int64{}}
	check := func(what string) {
		t.Helper()
		got := w.api.AdminStats(t)
		if got.GrantsRefused != want.GrantsRefused || got.SupportOpened != want.SupportOpened ||
			got.SupportEnded["closed"] != want.SupportEnded["closed"] || got.SupportEnded["ended"] != want.SupportEnded["ended"] {
			t.Errorf("after %s: %+v, want grants refused %d opened %d closed %d ended %d", what, got,
				want.GrantsRefused, want.SupportOpened, want.SupportEnded["closed"], want.SupportEnded["ended"])
		}
	}
	check("nothing") // a handler that has done nothing has counted nothing

	// Refused requests do not open anything.
	for what, body := range map[string]map[string]any{
		"no reason":   {"permissions": valid},
		"a bad list":  {"reason": apitest.Reason("x"), "permissions": []string{"tenant:read"}},
		"no list":     {"reason": apitest.Reason("x")},
		"a long text": {"reason": strings.Repeat("r", 501), "permissions": valid},
	} {
		if r := w.key.Do(t, "POST", w.sp(""), body, nil); r.Status != 400 {
			t.Fatalf("%s: %d", what, r.Status)
		}
	}
	if r := w.key.Do(t, "POST", "/v1/tenant-support/00000000-0000-7000-8000-000000000099/actions", map[string]any{"reason": apitest.Reason("x"), "permissions": valid}, nil); r.Status < 400 {
		t.Fatalf("an action on a tenant that is not there: %d", r.Status)
	}
	check("requests that were refused")

	// Opened by a key, closed by the platform.
	a := w.open(t, w.key, apitest.Reason("open"), valid, nil)
	want.SupportOpened++
	check("an opening")
	if r := w.key.Do(t, "POST", w.sp("/"+a.ID+"/close"), map[string]any{"reason": apitest.Reason("close")}, nil); r.Status != 200 {
		t.Fatalf("close: %d %s", r.Status, r.Body)
	}
	want.SupportEnded["closed"]++
	check("a closing")
	// Closing it again fails: nothing is counted.
	if r := w.key.Do(t, "POST", w.sp("/"+a.ID+"/close"), map[string]any{"reason": apitest.Reason("close again")}, nil); r.Status < 400 {
		t.Fatalf("a second close: %d", r.Status)
	}
	check("a second closing that failed")

	// Opened by a user, ended by the tenant.
	b := w.open(t, w.user, apitest.Reason("open as a user"), []string{authz.MemberRead}, nil)
	want.SupportOpened++
	if r := w.owner.Do(t, "POST", w.tp("/support-actions/"+b.ID+"/end"), map[string]any{"reason": apitest.Reason("end")}, nil); r.Status != 200 {
		t.Fatalf("end: %d %s", r.Status, r.Body)
	}
	want.SupportEnded["ended"]++
	check("an ending by the tenant")
	if r := w.owner.Do(t, "POST", w.tp("/support-actions/"+b.ID+"/end"), map[string]any{"reason": apitest.Reason("end again")}, nil); r.Status < 400 {
		t.Fatalf("a second end: %d", r.Status)
	}
	check("a second ending that failed")

	// Refused grants: a tenant administrator may not give the owner role or a support permission.
	admin := w.api.AsRole(t, w.tenant.ID, authz.RoleTenantAdmin)
	admin.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": apitest.UniqueName("sa"), "roles": []string{"owner"}}, nil)
	want.GrantsRefused++
	check("a refused grant")
	admin.Do(t, "POST", w.tp("/roles"), map[string]any{"name": apitest.UniqueName("grab"), "permissions": []string{authz.SupportActionEnd}}, nil)
	want.GrantsRefused++
	check("a second refused grant")
	// A request that is invalid before the question of the grant arises is not a refused grant.
	admin.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"roles": []string{"inference"}}, nil)
	check("an invalid request")

	lines := strings.Count(w.api.Logs.String(), `"msg":"grant refused"`)
	if int64(lines) != want.GrantsRefused {
		t.Errorf("%d `grant refused` lines against %d counted", lines, want.GrantsRefused)
	}
}

// TestOIDCSignInStats is T-920 (OIDC part): a sign-in through the identity provider is counted as
// the method oidc, ok when it opened a session and refused when the state, the token or the mapping
// was refused.
func TestOIDCSignInStats(t *testing.T) {
	api, o := apitest.NewPeopleOIDC(t, apitest.PeopleConfig{}, apitest.OIDCConfig{Users: idpPeople})
	p := peopleOn(t, api)
	issuer := o.IDP.Issuer()

	if got := api.AdminStats(t).OIDC; len(got) != 0 {
		t.Fatalf("a new handler has counted %v", got)
	}
	p.d.FederatedUser(t, issuer, "ada", []string{"platform-admin"})
	o.IDP.SignInAs("ada")
	if end := last(o.SignIn(t, api.NewBrowser())); end.Status != 303 || end.Location != "/console" {
		t.Fatalf("the sign-in ended %d %q", end.Status, end.Location)
	}
	if got := api.AdminStats(t).OIDC; got["ok"] != 1 || len(got) != 1 {
		t.Errorf("after a good sign-in: %v", got)
	}

	// An unlinked subject without a rule is refused.
	o.IDP.SignInAs("carl")
	if end := last(o.SignIn(t, api.NewBrowser())); !failedRedirect(end, "/console", "sign_in_failed") {
		t.Fatalf("carl: %d %q", end.Status, end.Location)
	}
	// A disabled user is refused.
	p.d.FederatedUser(t, issuer, "bob", nil, dbtest.Disabled())
	o.IDP.SignInAs("bob")
	if end := last(o.SignIn(t, api.NewBrowser())); !failedRedirect(end, "/console", "sign_in_failed") {
		t.Fatalf("bob: %d %q", end.Status, end.Location)
	}
	got := api.AdminStats(t).OIDC
	if got["ok"] != 1 || got["refused"] != 2 {
		t.Errorf("after two refusals: %v, want ok 1 refused 2", got)
	}
	for k := range got {
		if k != "ok" && k != "refused" && k != "unavailable" {
			t.Errorf("the result %q is not in the closed list", k)
		}
	}
	for _, sub := range []string{"ada", "bob", "carl"} {
		if strings.Contains(api.Logs.String(), `"`+sub+`"`) {
			t.Errorf("the log holds the subject %q", sub)
		}
	}
}
