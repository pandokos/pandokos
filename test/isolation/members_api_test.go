package isolation

import (
	"net/http"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
)

// TestMembersAPI is T-826: the member operations across tenants and across the managing rule.
func TestMembersAPI(t *testing.T) {
	api := apitest.NewPeople(t, apitest.PeopleConfig{})
	d := api.DB
	ta, keyA := api.Tenant(t, "tenant-a")
	tb, keyB := api.Tenant(t, "tenant-b")
	ida, _ := tenant.ParseID(ta.ID)
	idb, _ := tenant.ParseID(tb.ID)

	mine := d.TenantUser(t, ida, "a.managed", []string{"tenant-admin"})
	shared := d.PlatformUser(t, "shared.member", nil)
	d.Member(t, ida, shared.ID, []string{"tenant-admin"})
	theirs := d.TenantUser(t, idb, "b.managed", []string{"tenant-admin"})
	outsider := d.PlatformUser(t, "outsider", []string{"platform-admin"}) // no membership anywhere
	adminB := d.TenantUser(t, idb, "b.admin", []string{"tenant-admin"})

	session := func(who dbtest.Person) *apitest.Browser {
		b := api.NewBrowser()
		b.MustSignIn(t, who.Username, who.Password)
		return b
	}
	bOutsider, bAdminB := session(outsider), session(adminB)

	// Every member operation, with tenant B's credential (key or session) on tenant A's path: 404.
	n := 0
	before := api.Snapshot(t, "audit_event", "platform_audit_event", "user_session", "signin_throttle")
	for _, op := range adminapi.Operations {
		if !strings.Contains(op.Path, "/members") {
			continue
		}
		n++
		call := apitest.CallFor(t, op, apitest.Params{Tenant: ta.ID, User: mine.ID})
		for name, do := range map[string]func() *apitest.Response{
			"B's key":     func() *apitest.Response { return api.Do(t, call.Method, call.Path, keyB, call.Body, nil) },
			"B's session": func() *apitest.Response { return bAdminB.Do(t, call.Method, call.Path, call.Body, nil) },
			"a session of a user that is no member of A": func() *apitest.Response { return bOutsider.Do(t, call.Method, call.Path, call.Body, nil) },
		} {
			r := do()
			if r.Status != 404 || !strings.Contains(string(r.Body), `"code":"not_found"`) {
				t.Errorf("%s with %s on tenant A's path: %d %s, want 404 not_found", op.ID, name, r.Status, r.Body)
			}
		}
	}
	if n != 10 {
		t.Errorf("%d member operations, want 10", n)
	}
	if after := api.Snapshot(t, "audit_event", "platform_audit_event", "user_session", "signin_throttle"); after != before {
		t.Error("a request across tenants changed a row")
	}
	// The answer is the one for a tenant that does not exist.
	unknown := api.Do(t, "GET", "/v1/tenants/"+apitest.NoUser+"/members", keyB, nil, nil)
	cross := api.Do(t, "GET", "/v1/tenants/"+ta.ID+"/members", keyB, nil, nil)
	if unknown.Status != 404 || cross.Status != 404 {
		t.Errorf("unknown %d, cross %d", unknown.Status, cross.Status)
	}
	if normalizeBody(unknown) != normalizeBody(cross) {
		t.Errorf("a tenant that exists is told apart from one that does not:\n%s\n%s", unknown.Body, cross.Body)
	}

	// A tenant administrator cannot act on the account of a platform-managed member.
	adminA := d.TenantUser(t, ida, "a.admin", []string{"tenant-admin"})
	bAdminA := session(adminA)
	for _, path := range []string{"/reset-password", "/disable", "/unlock", "/revoke-sessions", "/enable"} {
		body := map[string]any{}
		if path == "/reset-password" {
			body = map[string]any{"reason": "test"} // required since sprint 4.3
		}
		for name, do := range map[string]func() *apitest.Response{
			"A's key": func() *apitest.Response {
				return api.Do(t, "POST", "/v1/tenants/"+ta.ID+"/members/"+shared.ID+path, keyA, body, nil)
			},
			"A's session": func() *apitest.Response {
				return bAdminA.Do(t, "POST", "/v1/tenants/"+ta.ID+"/members/"+shared.ID+path, body, nil)
			},
		} {
			if r := do(); r.Status != 409 || !strings.Contains(string(r.Body), `"code":"invalid_state"`) {
				t.Errorf("%s %s on a platform-managed member: %d %s, want 409 invalid_state", name, path, r.Status, r.Body)
			}
		}
	}
	// Nothing changed: the shared user still signs in with its password and is active.
	if r := api.NewBrowser().SignIn(t, shared.Username, shared.Password); r.Status != 200 {
		t.Errorf("the platform-managed member after the refused operations: %d", r.Status)
	}
	// A tenant administrator does not see a user of another tenant, however it asks.
	for name, r := range map[string]*apitest.Response{
		"get":      api.Do(t, "GET", "/v1/tenants/"+ta.ID+"/members/"+theirs.ID, keyA, nil, nil),
		"disable":  api.Do(t, "POST", "/v1/tenants/"+ta.ID+"/members/"+theirs.ID+"/disable", keyA, map[string]any{}, nil),
		"reset":    api.Do(t, "POST", "/v1/tenants/"+ta.ID+"/members/"+theirs.ID+"/reset-password", keyA, map[string]any{"reason": "test"}, nil),
		"existing": api.Do(t, "POST", "/v1/tenants/"+ta.ID+"/members", keyA, map[string]any{"account": "existing", "user_id": theirs.ID, "roles": []string{"tenant-admin"}}, nil),
		"by name":  api.Do(t, "POST", "/v1/tenants/"+ta.ID+"/members", keyA, map[string]any{"account": "existing", "username": theirs.Username, "roles": []string{"tenant-admin"}}, nil),
		"list":     api.Do(t, "GET", "/v1/tenants/"+ta.ID+"/members?username="+theirs.Username, keyA, nil, nil),
	} {
		if name == "list" {
			if r.Status != 200 || strings.Contains(string(r.Body), theirs.ID) {
				t.Errorf("list filtered by the username of B's user: %d %s", r.Status, r.Body)
			}
			continue
		}
		if r.Status != 404 {
			t.Errorf("%s: %d %s, want 404", name, r.Status, r.Body)
		}
	}
	if r := api.NewBrowser().SignIn(t, theirs.Username, theirs.Password); r.Status != 200 {
		t.Errorf("the user of tenant B after A's attempts: %d", r.Status)
	}
	// The platform's list shows neither tenant's users; the member lists show their own.
	var l adminapi.MemberList
	r := api.Do(t, "GET", "/v1/tenants/"+ta.ID+"/members?limit=200", keyA, nil, nil)
	if r.Status != http.StatusOK {
		t.Fatalf("list members: %d %s", r.Status, r.Body)
	}
	r.JSON(t, &l)
	seen := map[string]bool{}
	for _, m := range l.Items {
		seen[m.Username] = true
	}
	if !seen["a.managed"] || !seen["shared.member"] || !seen["a.admin"] || seen["b.managed"] || seen["b.admin"] || seen["outsider"] {
		t.Errorf("the members of A: %v", seen)
	}
}

func normalizeBody(r *apitest.Response) string {
	s := string(r.Body)
	for _, id := range []string{r.Header.Get("X-Pandokos-Request-Id")} {
		if id != "" {
			s = strings.ReplaceAll(s, id, "ID")
		}
	}
	return s
}
