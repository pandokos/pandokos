package admin_test

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
)

func oneOf(t testing.TB, what string, r *apitest.Response, statuses ...int) {
	t.Helper()
	for _, s := range statuses {
		if r.Status == s {
			return
		}
	}
	t.Errorf("%s: status %d, want one of %v\n%s", what, r.Status, statuses, r.Body)
}

// TestUsers is T-802, the platform's half: every operation on users.
func TestUsers(t *testing.T) {
	t.Parallel()
	p := newPeople(t, apitest.PeopleConfig{})
	d := p.d

	t.Run("create a local user", func(t *testing.T) {
		out := decode[adminapi.UserCreated](t, p.platform(t, "POST", "/v1/users", map[string]any{
			"sign_in": "local", "username": "ada.lovelace", "display_name": "Ada Lovelace", "email": "ada@example.test", "platform_roles": []string{"platform-admin"},
		}), 201)
		u := out.User
		if u.SignIn != "local" || u.Username != "ada.lovelace" || u.DisplayName != "Ada Lovelace" || u.Email != "ada@example.test" || u.State != "active" ||
			!u.MustChangePassword || strings.Join(u.PlatformRoles, ",") != "platform-admin" || u.ID == "" || u.LockedUntil != nil {
			t.Errorf("user %+v", u)
		}
		if !reGenerated.MatchString(out.InitialPassword) {
			t.Errorf("initial password %q is not 24 characters of a-z2-7", out.InitialPassword)
		}
		b := p.api.NewBrowser()
		if s := decode[adminapi.Session](t, b.SignIn(t, "ada.lovelace", out.InitialPassword), 200); !s.MustChangePassword {
			t.Error("the first sign-in does not require a password change")
		}
		// A user without roles.
		plain := decode[adminapi.UserCreated](t, p.platform(t, "POST", "/v1/users", map[string]any{"sign_in": "local", "username": "plain.user"}), 201)
		if len(plain.User.PlatformRoles) != 0 || plain.User.DisplayName != "" {
			t.Errorf("user %+v", plain.User)
		}
	})

	t.Run("the rules of createUser", func(t *testing.T) {
		bad := map[string]map[string]any{
			"no username":            {"sign_in": "local"},
			"an upper-case username": {"sign_in": "local", "username": "Upper.Case"},
			"a short username":       {"sign_in": "local", "username": "a"},
			"a subject on a local":   {"sign_in": "local", "username": "with.subject", "subject": "x"},
			"an unknown sign-in":     {"sign_in": "saml", "username": "unknown.method"},
			"no sign-in":             {"username": "no.method"},
			"an unknown role":        {"sign_in": "local", "username": "role.unknown", "platform_roles": []string{"ghost"}},
			"the inference role":     {"sign_in": "local", "username": "role.inference", "platform_roles": []string{"inference"}},
			"a tenant role":          {"sign_in": "local", "username": "role.tenant", "platform_roles": []string{"tenant-admin"}},
			"a repeated role":        {"sign_in": "local", "username": "role.twice", "platform_roles": []string{"platform-admin", "platform-admin"}},
			"a long display name":    {"sign_in": "local", "username": "long.display", "display_name": strings.Repeat("a", 201)},
			"an e-mail with a space": {"sign_in": "local", "username": "bad.email", "email": "a b@example.test"},
			"an unknown member":      {"sign_in": "local", "username": "extra.member", "admin": true},
		}
		for name, body := range bad {
			if r := p.platform(t, "POST", "/v1/users", body); r.Status != 400 || problem(t, r).Code != "invalid_request" {
				t.Errorf("%s: %d %s", name, r.Status, r.Body)
			}
		}
		oneOf(t, "an oidc user without a subject", p.platform(t, "POST", "/v1/users", map[string]any{"sign_in": "oidc"}), 400)
		oneOf(t, "an oidc user while the provider is off", p.platform(t, "POST", "/v1/users", map[string]any{"sign_in": "oidc", "subject": "someone"}), 400, 409)

		decode[adminapi.UserCreated](t, p.platform(t, "POST", "/v1/users", map[string]any{"sign_in": "local", "username": "taken.name"}), 201)
		if c := code(t, p.platform(t, "POST", "/v1/users", map[string]any{"sign_in": "local", "username": "taken.name"}), 409); c != "conflict" {
			t.Errorf("a username that exists: %s", c)
		}
		d.TenantUser(t, p.tid, "tenant.taken", []string{"tenant-admin"})
		if c := code(t, p.platform(t, "POST", "/v1/users", map[string]any{"sign_in": "local", "username": "tenant.taken"}), 409); c != "conflict" {
			t.Errorf("a username that a tenant's user has: %s", c)
		}
	})

	t.Run("local accounts switched off", func(t *testing.T) {
		off := newPeople(t, apitest.PeopleConfig{Admin: func(o *admin.Options) { o.Auth.LocalEnabled = false }})
		if c := code(t, off.platform(t, "POST", "/v1/users", map[string]any{"sign_in": "local", "username": "no.local"}), 409); c != "invalid_state" {
			t.Errorf("code %s", c)
		}
	})

	t.Run("get, list, sort, filter, page", func(t *testing.T) {
		q := newPeople(t, apitest.PeopleConfig{})
		var ids []string
		for i := 0; i < 6; i++ {
			u := q.d.PlatformUser(t, fmt.Sprintf("list.user%d", i), nil)
			ids = append(ids, u.ID)
		}
		q.d.TenantUser(t, q.tid, "list.tenant", []string{"tenant-admin"})
		q.d.PlatformUser(t, "list.off", nil, dbtest.Disabled())

		all := decode[adminapi.UserList](t, q.platform(t, "GET", "/v1/users?limit=200", nil), 200)
		names := map[string]bool{}
		for _, u := range all.Items {
			names[u.Username] = true
			if u.Username == "list.tenant" {
				t.Error("a user that a tenant manages is listed by the platform")
			}
		}
		if len(all.Items) != 8 || !names["root.admin"] || !names["list.off"] { // root, six, one disabled
			t.Errorf("%d users: %v", len(all.Items), names)
		}
		// Pages of three, in each order, cover everything once.
		for _, sortBy := range []string{"created_at", "-created_at", "username", "-username"} {
			var got []string
			cursor := ""
			for page := 0; page < 10; page++ {
				path := "/v1/users?limit=3&sort=" + sortBy
				if cursor != "" {
					path += "&cursor=" + url.QueryEscape(cursor)
				}
				l := decode[adminapi.UserList](t, q.platform(t, "GET", path, nil), 200)
				for _, u := range l.Items {
					got = append(got, u.Username)
				}
				if cursor = l.NextCursor; cursor == "" {
					break
				}
			}
			if len(got) != 8 {
				t.Errorf("sort %s: %d users over the pages, want 8: %v", sortBy, len(got), got)
			}
			sorted := append([]string(nil), got...)
			switch sortBy {
			case "username":
				if !sort.StringsAreSorted(got) {
					t.Errorf("sort %s: %v", sortBy, got)
				}
			case "-username":
				sort.Sort(sort.Reverse(sort.StringSlice(sorted)))
				if strings.Join(sorted, ",") != strings.Join(got, ",") {
					t.Errorf("sort %s: %v", sortBy, got)
				}
			}
		}
		if l := decode[adminapi.UserList](t, q.platform(t, "GET", "/v1/users?state=disabled", nil), 200); len(l.Items) != 1 || l.Items[0].Username != "list.off" {
			t.Errorf("state=disabled: %+v", l.Items)
		}
		if l := decode[adminapi.UserList](t, q.platform(t, "GET", "/v1/users?username=list.user3", nil), 200); len(l.Items) != 1 || l.Items[0].ID != ids[3] {
			t.Errorf("username filter: %+v", l.Items)
		}
		if l := decode[adminapi.UserList](t, q.platform(t, "GET", "/v1/users?username=list.use", nil), 200); len(l.Items) != 0 {
			t.Errorf("a prefix is not a username: %+v", l.Items)
		}
		for _, bad := range []string{"sort=nope", "limit=0", "limit=201", "state=gone", "cursor=junk", "username=UPPER", "extra=1"} {
			if r := q.platform(t, "GET", "/v1/users?"+bad, nil); r.Status != 400 {
				t.Errorf("?%s: %d", bad, r.Status)
			}
		}
		one := decode[adminapi.User](t, q.platform(t, "GET", "/v1/users/"+ids[0], nil), 200)
		if one.ID != ids[0] || one.Username != "list.user0" {
			t.Errorf("getUser %+v", one)
		}
		if c := code(t, q.platform(t, "GET", "/v1/users/"+apitest.NoUser, nil), 404); c != "not_found" {
			t.Errorf("code %s", c)
		}
		tu := q.d.TenantUser(t, q.tid, "get.tenant", []string{"tenant-admin"})
		if c := code(t, q.platform(t, "GET", "/v1/users/"+tu.ID, nil), 404); c != "not_found" {
			t.Errorf("a user that a tenant manages through the platform operation: %s", c)
		}
	})

	t.Run("update, disable, enable, unlock", func(t *testing.T) {
		q := newPeople(t, apitest.PeopleConfig{})
		u := q.d.PlatformUser(t, "edit.user", nil)
		up := decode[adminapi.User](t, q.platform(t, "PATCH", "/v1/users/"+u.ID, map[string]any{"display_name": "Edited", "email": "e@example.test", "platform_roles": []string{"platform-admin"}}), 200)
		if up.DisplayName != "Edited" || up.Email != "e@example.test" || strings.Join(up.PlatformRoles, ",") != "platform-admin" {
			t.Errorf("updated %+v", up)
		}
		// A member that is absent is not changed; roles can be emptied.
		up = decode[adminapi.User](t, q.platform(t, "PATCH", "/v1/users/"+u.ID, map[string]any{"display_name": "Again"}), 200)
		if up.DisplayName != "Again" || up.Email != "e@example.test" || len(up.PlatformRoles) != 1 {
			t.Errorf("updated %+v", up)
		}
		up = decode[adminapi.User](t, q.platform(t, "PATCH", "/v1/users/"+u.ID, map[string]any{"platform_roles": []string{}}), 200)
		if len(up.PlatformRoles) != 0 {
			t.Errorf("roles %v", up.PlatformRoles)
		}
		for name, body := range map[string]map[string]any{"unknown role": {"platform_roles": []string{"ghost"}}, "inference": {"platform_roles": []string{"inference"}}, "username": {"username": "new.name"}, "state": {"state": "disabled"}} {
			if r := q.platform(t, "PATCH", "/v1/users/"+u.ID, body); r.Status != 400 {
				t.Errorf("%s: %d", name, r.Status)
			}
		}
		if c := code(t, q.platform(t, "PATCH", "/v1/users/"+apitest.NoUser, map[string]any{"display_name": "x"}), 404); c != "not_found" {
			t.Errorf("code %s", c)
		}

		// Disable and enable; repeating either changes nothing.
		for i := 0; i < 2; i++ {
			if s := decode[adminapi.User](t, q.platform(t, "POST", "/v1/users/"+u.ID+"/disable", map[string]any{}), 200); s.State != "disabled" {
				t.Errorf("disabled: %+v", s)
			}
		}
		for i := 0; i < 2; i++ {
			if s := decode[adminapi.User](t, q.platform(t, "POST", "/v1/users/"+u.ID+"/enable", map[string]any{}), 200); s.State != "active" {
				t.Errorf("enabled: %+v", s)
			}
		}

		// Unlock: ten wrong passwords lock the account.
		for i := 0; i < 10; i++ {
			q.d.Age(t, "signin_throttle", "", time.Minute, "last_failure_at")
			q.api.NewBrowser().SignIn(t, u.Username, "wrong password here")
		}
		locked := decode[adminapi.User](t, q.platform(t, "GET", "/v1/users/"+u.ID, nil), 200)
		if locked.LockedUntil == nil || !locked.LockedUntil.After(time.Now().Add(14*time.Minute)) {
			t.Fatalf("locked_until %v after ten failures", locked.LockedUntil)
		}
		if r := q.api.NewBrowser().SignIn(t, u.Username, u.Password); r.Status != 429 {
			t.Errorf("the right password while locked: %d", r.Status)
		}
		if free := decode[adminapi.User](t, q.platform(t, "POST", "/v1/users/"+u.ID+"/unlock", map[string]any{}), 200); free.LockedUntil != nil {
			t.Errorf("unlocked %+v", free)
		}
		if r := q.api.NewBrowser().SignIn(t, u.Username, u.Password); r.Status != 200 {
			t.Errorf("sign-in after the unlock: %d", r.Status)
		}
	})

	t.Run("a user cannot lock itself out", func(t *testing.T) {
		q := newPeople(t, apitest.PeopleConfig{})
		b := q.signIn(t, q.root)
		if c := code(t, b.Do(t, "POST", "/v1/users/"+q.root.ID+"/disable", map[string]any{}, nil), 409); c != "invalid_state" {
			t.Errorf("disabling itself: %s", c)
		}
		if c := code(t, b.Do(t, "PATCH", "/v1/users/"+q.root.ID, map[string]any{"platform_roles": []string{}}, nil), 409); c != "invalid_state" {
			t.Errorf("removing its own last platform role: %s", c)
		}
		// The platform key is not a user: it may.
		if r := q.platform(t, "PATCH", "/v1/users/"+q.root.ID, map[string]any{"display_name": "changed"}); r.Status != 200 {
			t.Errorf("another change to itself: %d", r.Status)
		}
		// Another administrator may disable it.
		other := q.d.PlatformUser(t, "other.admin", []string{"platform-admin"})
		ob := q.signIn(t, other)
		if r := ob.Do(t, "POST", "/v1/users/"+q.root.ID+"/disable", map[string]any{}, nil); r.Status != 200 {
			t.Errorf("disabling another administrator: %d %s", r.Status, r.Body)
		}
	})

	t.Run("permissions", func(t *testing.T) {
		if r := p.tenantKey(t, "GET", "/v1/users", nil); r.Status != 403 {
			t.Errorf("a tenant key lists users: %d", r.Status)
		}
		bare := d.PlatformUser(t, "bare.user", nil)
		if r := p.sessionOf(t, bare.ID).Do(t, "GET", "/v1/users", nil, nil); r.Status != 403 {
			t.Errorf("a user without the role lists users: %d", r.Status)
		}
		if r := p.api.Do(t, "GET", "/v1/users", "", nil, nil); r.Status != 401 {
			t.Errorf("no credential: %d", r.Status)
		}
	})
}

// TestOidcUsers is the part of T-802 that needs the identity provider: a user linked by an administrator.
func TestOidcUsers(t *testing.T) {
	t.Parallel()
	api, _ := apitest.NewPeopleOIDC(t, apitest.PeopleConfig{}, apitest.OIDCConfig{})
	p := peopleOn(t, api)
	out := decode[adminapi.UserCreated](t, p.platform(t, "POST", "/v1/users", map[string]any{"sign_in": "oidc", "subject": "person-1", "display_name": "Linked", "platform_roles": []string{"platform-admin"}}), 201)
	if out.User.SignIn != "oidc" || out.User.Subject != "person-1" || out.User.Username != "" || out.InitialPassword != "" || out.User.MustChangePassword {
		t.Errorf("created %+v", out)
	}
	if c := code(t, p.platform(t, "POST", "/v1/users", map[string]any{"sign_in": "oidc", "subject": "person-1"}), 409); c != "conflict" {
		t.Errorf("the same subject twice: %s", c)
	}
	for name, body := range map[string]map[string]any{
		"a username on an oidc user":    {"sign_in": "oidc", "subject": "person-2", "username": "has.username"},
		"a subject with a control char": {"sign_in": "oidc", "subject": "bad\nsubject"},
		"a subject of 256 characters":   {"sign_in": "oidc", "subject": strings.Repeat("s", 256)},
	} {
		if r := p.platform(t, "POST", "/v1/users", body); r.Status != 400 {
			t.Errorf("%s: %d", name, r.Status)
		}
	}
}

// TestMembers is T-802, the tenant's half: every operation on members.
func TestMembers(t *testing.T) {
	t.Parallel()
	p := newPeople(t, apitest.PeopleConfig{})
	d := p.d
	shared := d.PlatformUser(t, "shared.person", nil, dbtest.Display("Shared", "shared@example.test"))

	t.Run("add a new account", func(t *testing.T) {
		out := decode[adminapi.MemberCreated](t, p.tenantKey(t, "POST", p.membersPath(""), map[string]any{
			"account": "new", "username": "new.member", "display_name": "New Member", "email": "new@example.test", "roles": []string{"tenant-admin"},
		}), 201)
		m := out.Member
		if m.ManagedBy != "tenant" || m.Source != "manual" || m.Username != "new.member" || m.SignIn != "local" || m.State != "active" || !m.MustChangePassword ||
			strings.Join(m.Roles, ",") != "tenant-admin" || m.UserID == "" || m.DisplayName != "New Member" {
			t.Errorf("member %+v", m)
		}
		if !reGenerated.MatchString(out.InitialPassword) {
			t.Errorf("initial password %q", out.InitialPassword)
		}
		b := p.api.NewBrowser()
		s := decode[adminapi.Session](t, b.SignIn(t, "new.member", out.InitialPassword), 200)
		if !s.MustChangePassword {
			t.Error("a new account has to change its password")
		}
		// The platform does not see a user that a tenant manages.
		if c := code(t, p.platform(t, "GET", "/v1/users/"+m.UserID, nil), 404); c != "not_found" {
			t.Errorf("code %s", c)
		}
	})

	t.Run("the rules of createMember", func(t *testing.T) {
		bad := map[string]map[string]any{
			"no roles":                   {"account": "new", "username": "no.roles", "roles": []string{}},
			"no roles member":            {"account": "new", "username": "no.roles2"},
			"the inference role":         {"account": "new", "username": "role.inference", "roles": []string{"inference"}},
			"a platform role":            {"account": "new", "username": "role.platform", "roles": []string{"platform-admin"}},
			"an unknown role":            {"account": "new", "username": "role.unknown", "roles": []string{"ghost"}},
			"a repeated role":            {"account": "new", "username": "role.twice", "roles": []string{"tenant-admin", "tenant-admin"}},
			"a new account without name": {"account": "new", "roles": []string{"tenant-admin"}},
			"a new account with a user":  {"account": "new", "username": "with.user", "user_id": apitest.NoUser, "roles": []string{"tenant-admin"}},
			"existing with neither":      {"account": "existing", "roles": []string{"tenant-admin"}},
			"existing with both":         {"account": "existing", "user_id": shared.ID, "username": shared.Username, "roles": []string{"tenant-admin"}},
			"an unknown kind of account": {"account": "other", "username": "other.kind", "roles": []string{"tenant-admin"}},
			"an upper-case username":     {"account": "new", "username": "Upper.Case", "roles": []string{"tenant-admin"}},
		}
		for name, body := range bad {
			if r := p.tenantKey(t, "POST", p.membersPath(""), body); r.Status != 400 || problem(t, r).Code != "invalid_request" {
				t.Errorf("%s: %d %s", name, r.Status, r.Body)
			}
		}
		decode[adminapi.MemberCreated](t, p.tenantKey(t, "POST", p.membersPath(""), map[string]any{"account": "new", "username": "taken.member", "roles": []string{"tenant-admin"}}), 201)
		if c := code(t, p.tenantKey(t, "POST", p.membersPath(""), map[string]any{"account": "new", "username": "taken.member", "roles": []string{"tenant-admin"}}), 409); c != "conflict" {
			t.Errorf("a username that exists: %s", c)
		}
		if c := code(t, p.tenantKey(t, "POST", p.membersPath(""), map[string]any{"account": "new", "username": shared.Username, "roles": []string{"tenant-admin"}}), 409); c != "conflict" {
			t.Errorf("the username of a platform user: %s", c)
		}
	})

	t.Run("add an existing account", func(t *testing.T) {
		out := decode[adminapi.MemberCreated](t, p.tenantKey(t, "POST", p.membersPath(""), map[string]any{"account": "existing", "user_id": shared.ID, "roles": []string{"tenant-admin"}}), 201)
		m := out.Member
		if out.InitialPassword != "" || m.ManagedBy != "platform" || m.UserID != shared.ID || m.Username != shared.Username || m.MustChangePassword {
			t.Errorf("member %+v password %q", m, out.InitialPassword)
		}
		if c := code(t, p.tenantKey(t, "POST", p.membersPath(""), map[string]any{"account": "existing", "username": shared.Username, "roles": []string{"tenant-admin"}}), 409); c != "conflict" {
			t.Errorf("a user that is a member already: %s", c)
		}
		again := d.PlatformUser(t, "by.name", nil)
		out = decode[adminapi.MemberCreated](t, p.tenantKey(t, "POST", p.membersPath(""), map[string]any{"account": "existing", "username": "by.name", "roles": []string{"tenant-admin"}}), 201)
		if out.Member.UserID != again.ID {
			t.Errorf("member %+v", out.Member)
		}
		for name, body := range map[string]map[string]any{
			"an ID that does not exist":      {"account": "existing", "user_id": apitest.NoUser, "roles": []string{"tenant-admin"}},
			"a username that does not exist": {"account": "existing", "username": "nobody.here", "roles": []string{"tenant-admin"}},
		} {
			if c := code(t, p.tenantKey(t, "POST", p.membersPath(""), body), 404); c != "not_found" {
				t.Errorf("%s: %s", name, c)
			}
		}
		// A user that another tenant manages is not found, whatever it is called.
		other, otherKey := p.api.Tenant(t, "globex")
		_ = other
		otherUser := decode[adminapi.MemberCreated](t, p.api.Do(t, "POST", "/v1/tenants/"+other.ID+"/members", otherKey, map[string]any{"account": "new", "username": "globex.person", "roles": []string{"tenant-admin"}}, nil), 201)
		if c := code(t, p.tenantKey(t, "POST", p.membersPath(""), map[string]any{"account": "existing", "user_id": otherUser.Member.UserID, "roles": []string{"tenant-admin"}}), 404); c != "not_found" {
			t.Errorf("a user that another tenant manages: %s", c)
		}
		if c := code(t, p.tenantKey(t, "POST", p.membersPath(""), map[string]any{"account": "existing", "username": "globex.person", "roles": []string{"tenant-admin"}}), 404); c != "not_found" {
			t.Errorf("a user that another tenant manages, by name: %s", c)
		}
	})

	t.Run("get, list, sort, filter, page", func(t *testing.T) {
		q := newPeople(t, apitest.PeopleConfig{})
		var users []dbtest.Person
		for i := 0; i < 5; i++ {
			users = append(users, q.d.TenantUser(t, q.tid, fmt.Sprintf("member.u%d", i), []string{"tenant-admin"}))
		}
		outsider := q.d.PlatformUser(t, "member.outsider", nil)
		all := decode[adminapi.MemberList](t, q.tenantKey(t, "GET", q.membersPath("?limit=200"), nil), 200)
		if len(all.Items) != 5 {
			t.Fatalf("%d members, want 5", len(all.Items))
		}
		for _, sortBy := range []string{"created_at", "-created_at"} {
			var got []string
			cursor := ""
			for page := 0; page < 10; page++ {
				path := q.membersPath("?limit=2&sort=" + sortBy)
				if cursor != "" {
					path += "&cursor=" + url.QueryEscape(cursor)
				}
				l := decode[adminapi.MemberList](t, q.tenantKey(t, "GET", path, nil), 200)
				for _, m := range l.Items {
					got = append(got, m.Username)
				}
				if cursor = l.NextCursor; cursor == "" {
					break
				}
			}
			if len(got) != 5 {
				t.Errorf("sort %s: %v", sortBy, got)
			}
			if (sortBy == "created_at") != (got[0] == "member.u0") {
				t.Errorf("sort %s starts with %s", sortBy, got[0])
			}
		}
		if l := decode[adminapi.MemberList](t, q.tenantKey(t, "GET", q.membersPath("?username=member.u2"), nil), 200); len(l.Items) != 1 || l.Items[0].UserID != users[2].ID {
			t.Errorf("username filter: %+v", l.Items)
		}
		for _, bad := range []string{"sort=username", "limit=0", "cursor=junk", "username=UPPER"} {
			if r := q.tenantKey(t, "GET", q.membersPath("?"+bad), nil); r.Status != 400 {
				t.Errorf("?%s: %d", bad, r.Status)
			}
		}
		m := decode[adminapi.Member](t, q.tenantKey(t, "GET", q.membersPath("/"+users[1].ID), nil), 200)
		if m.UserID != users[1].ID || m.ManagedBy != "tenant" {
			t.Errorf("getMember %+v", m)
		}
		if c := code(t, q.tenantKey(t, "GET", q.membersPath("/"+outsider.ID), nil), 404); c != "not_found" {
			t.Errorf("a user that is not a member: %s", c)
		}
		if c := code(t, q.tenantKey(t, "GET", q.membersPath("/"+apitest.NoUser), nil), 404); c != "not_found" {
			t.Errorf("an unknown user: %s", c)
		}
	})

	t.Run("update, delete, and the managing rule", func(t *testing.T) {
		q := newPeople(t, apitest.PeopleConfig{})
		mine := q.d.TenantUser(t, q.tid, "mine.member", []string{"tenant-admin"})
		sh := q.d.PlatformUser(t, "shared.member", nil)
		q.d.Member(t, q.tid, sh.ID, []string{"tenant-admin"})

		up := decode[adminapi.Member](t, q.tenantKey(t, "PATCH", q.membersPath("/"+mine.ID), map[string]any{"display_name": "Mine", "email": "mine@example.test", "roles": []string{"tenant-admin"}}), 200)
		if up.DisplayName != "Mine" || up.Email != "mine@example.test" || strings.Join(up.Roles, ",") != "tenant-admin" {
			t.Errorf("updated %+v", up)
		}
		if r := q.tenantKey(t, "PATCH", q.membersPath("/"+mine.ID), map[string]any{"roles": []string{}}); r.Status != 400 {
			t.Errorf("empty roles: %d", r.Status)
		}
		if r := q.tenantKey(t, "PATCH", q.membersPath("/"+mine.ID), map[string]any{"roles": []string{"inference"}}); r.Status != 400 {
			t.Errorf("inference: %d", r.Status)
		}
		// The roles of a platform-managed member are the tenant's to set; its attributes are not.
		if r := q.tenantKey(t, "PATCH", q.membersPath("/"+sh.ID), map[string]any{"roles": []string{"tenant-admin"}}); r.Status != 200 {
			t.Errorf("roles of a platform-managed member: %d %s", r.Status, r.Body)
		}
		if c := code(t, q.tenantKey(t, "PATCH", q.membersPath("/"+sh.ID), map[string]any{"display_name": "Mine now"}), 409); c != "invalid_state" {
			t.Errorf("attributes of a platform-managed member: %s", c)
		}
		// Operations on the account act on members that the tenant manages only.
		for _, path := range []string{"/disable", "/enable", "/unlock", "/reset-password", "/revoke-sessions"} {
			body := map[string]any{}
			if path == "/reset-password" {
				body = map[string]any{"reason": "test"} // required since sprint 4.3
			}
			if c := code(t, q.tenantKey(t, "POST", q.membersPath("/"+sh.ID+path), body), 409); c != "invalid_state" {
				t.Errorf("%s on a platform-managed member: %s", path, c)
			}
			if r := q.tenantKey(t, "POST", q.membersPath("/"+mine.ID+path), body); r.Status != 200 {
				t.Errorf("%s on a member the tenant manages: %d %s", path, r.Status, r.Body)
			}
			if c := code(t, q.tenantKey(t, "POST", q.membersPath("/"+apitest.NoUser+path), body), 404); c != "not_found" {
				t.Errorf("%s on an unknown user: %s", path, c)
			}
		}
		// Deleting: a platform-managed member loses the membership and keeps the account.
		decode[adminapi.Member](t, q.tenantKey(t, "DELETE", q.membersPath("/"+sh.ID), nil), 200)
		if c := code(t, q.tenantKey(t, "GET", q.membersPath("/"+sh.ID), nil), 404); c != "not_found" {
			t.Errorf("the membership after delete: %s", c)
		}
		if u := decode[adminapi.User](t, q.platform(t, "GET", "/v1/users/"+sh.ID, nil), 200); u.State != "active" {
			t.Errorf("the account of a removed platform-managed member: %+v", u)
		}
		// A member the tenant manages is disabled with it.
		decode[adminapi.Member](t, q.tenantKey(t, "DELETE", q.membersPath("/"+mine.ID), nil), 200)
		if c := code(t, q.tenantKey(t, "DELETE", q.membersPath("/"+mine.ID), nil), 404); c != "not_found" {
			t.Errorf("deleting twice: %s", c)
		}
		if n := q.d.Count(t, "SELECT count(*) FROM pandokos.app_user WHERE id = $1::uuid AND state = 'disabled'", mine.ID); n != 1 {
			t.Error("the account of a removed member that the tenant manages is not disabled")
		}
	})

	t.Run("a member cannot disable or remove itself", func(t *testing.T) {
		q := newPeople(t, apitest.PeopleConfig{})
		self := q.d.TenantUser(t, q.tid, "self.admin", []string{"tenant-admin"})
		b := q.signIn(t, self)
		if c := code(t, b.Do(t, "POST", q.membersPath("/"+self.ID+"/disable"), map[string]any{}, nil), 409); c != "invalid_state" {
			t.Errorf("disabling itself: %s", c)
		}
		if c := code(t, b.Do(t, "DELETE", q.membersPath("/"+self.ID), nil, nil), 409); c != "invalid_state" {
			t.Errorf("removing itself: %s", c)
		}
		if n := q.d.Count(t, "SELECT count(*) FROM pandokos.app_user u JOIN pandokos.membership m ON m.user_id = u.id WHERE u.id = $1::uuid AND u.state = 'active'", self.ID); n != 1 {
			t.Error("the member lost its account or its membership")
		}
		// An administrator key is not a user, and another administrator may.
		if r := q.tenantKey(t, "POST", q.membersPath("/"+self.ID+"/disable"), map[string]any{}); r.Status != 200 {
			t.Errorf("disabling it with a key: %d %s", r.Status, r.Body)
		}
	})

	t.Run("who may do what", func(t *testing.T) {
		q := newPeople(t, apitest.PeopleConfig{})
		member := q.d.TenantUser(t, q.tid, "plain.member", []string{"tenant-admin"})
		bare := q.d.PlatformUser(t, "bare.person", nil)
		q.d.Member(t, q.tid, bare.ID, []string{"tenant-admin"})
		if _, err := q.d.Super(t).Exec(ctx(t), "DELETE FROM pandokos.membership_role WHERE user_id = $1::uuid", bare.ID); err != nil {
			t.Fatal(err)
		}
		// A member without a role may not read the members; a tenant administrator may.
		if r := q.sessionOf(t, bare.ID).Do(t, "GET", q.membersPath(""), nil, nil); r.Status != 403 {
			t.Errorf("a member without roles lists the members: %d", r.Status)
		}
		if r := q.sessionOf(t, member.ID).Do(t, "GET", q.membersPath(""), nil, nil); r.Status != 200 {
			t.Errorf("a tenant administrator: %d", r.Status)
		}
		// A platform administrator is not a member: 404, as for an unknown tenant.
		if r := q.sessionOf(t, q.root.ID).Do(t, "GET", q.membersPath(""), nil, nil); r.Status != 404 {
			t.Errorf("a platform administrator who is not a member: %d", r.Status)
		}
		// A platform key does not hold a tenant permission.
		if r := q.platform(t, "GET", q.membersPath(""), nil); r.Status != 403 {
			t.Errorf("a platform key lists the members: %d", r.Status)
		}
	})
}
