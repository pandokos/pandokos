package admin_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
)

type auditStep struct {
	name       string
	do         func() *apitest.Response
	platform   []string // the actions written to the platform log, sorted
	tenant     []string // the actions written to the tenant's log
	actor      string   // actor_type of every row
	resourceID string   // the user the user.* and member.* rows are about; "" to skip the check
}

// TestPeopleAudit is T-803: each action of section 5.14 writes its rows in the right log.
func TestPeopleAudit(t *testing.T) {
	p := newPeople(t, apitest.PeopleConfig{})
	d := p.d
	var secrets []string // what no row may hold

	plat := decode[adminapi.UserCreated](t, p.platform(t, "POST", "/v1/users", map[string]any{"sign_in": "local", "username": "audited.user"}), 201)
	secrets = append(secrets, plat.InitialPassword)
	member := decode[adminapi.MemberCreated](t, p.tenantKey(t, "POST", p.membersPath(""), map[string]any{"account": "new", "username": "audited.member", "roles": []string{"tenant-admin"}}), 201)
	secrets = append(secrets, member.InitialPassword)
	shared := d.PlatformUser(t, "audited.shared", nil)
	pb := p.signIn(t, d.PlatformUser(t, "audit.actor", []string{"platform-admin"})) // a user acts: actor type user

	steps := []auditStep{
		{"createUser", func() *apitest.Response {
			r := p.platform(t, "POST", "/v1/users", map[string]any{"sign_in": "local", "username": "audited.created", "display_name": "Created", "platform_roles": []string{"platform-admin"}})
			if r.Status == 201 {
				secrets = append(secrets, decode[adminapi.UserCreated](t, r, 201).InitialPassword)
			}
			return r
		}, []string{"platform_role.assign", "user.create"}, nil, "platform", ""},
		{"updateUser", func() *apitest.Response {
			return p.platform(t, "PATCH", "/v1/users/"+plat.User.ID, map[string]any{"display_name": "Renamed", "email": "r@example.test"})
		}, []string{"user.update"}, nil, "platform", plat.User.ID},
		{"updateUser roles added", func() *apitest.Response {
			return p.platform(t, "PATCH", "/v1/users/"+plat.User.ID, map[string]any{"platform_roles": []string{"platform-admin"}})
		}, []string{"platform_role.assign"}, nil, "platform", ""},
		{"updateUser roles removed", func() *apitest.Response {
			return p.platform(t, "PATCH", "/v1/users/"+plat.User.ID, map[string]any{"platform_roles": []string{}})
		}, []string{"platform_role.remove"}, nil, "platform", ""},
		{"disableUser", func() *apitest.Response {
			return p.platform(t, "POST", "/v1/users/"+plat.User.ID+"/disable", map[string]any{})
		}, []string{"user.disable"}, nil, "platform", plat.User.ID},
		{"enableUser", func() *apitest.Response {
			return p.platform(t, "POST", "/v1/users/"+plat.User.ID+"/enable", map[string]any{})
		}, []string{"user.enable"}, nil, "platform", plat.User.ID},
		{"resetUserPassword", func() *apitest.Response {
			r := p.platform(t, "POST", "/v1/users/"+plat.User.ID+"/reset-password", map[string]any{})
			if r.Status == 200 {
				secrets = append(secrets, decode[adminapi.PasswordReset](t, r, 200).Password)
			}
			return r
		}, []string{"user.password_reset"}, nil, "platform", plat.User.ID},
		{"unlockUser", func() *apitest.Response {
			for i := 0; i < 10; i++ {
				d.Age(t, "signin_throttle", "", time.Minute, "last_failure_at")
				p.api.NewBrowser().SignIn(t, "audited.user", "wrong password here")
			}
			before := len(d.PlatformAuditRows(t))
			_ = before
			return p.platform(t, "POST", "/v1/users/"+plat.User.ID+"/unlock", map[string]any{})
		}, []string{"user.unlock"}, nil, "platform", plat.User.ID},
		{"revokeUserSessions", func() *apitest.Response {
			p.signIn(t, d.PlatformUser(t, "revoked.user", nil))
			u := d.PlatformUser(t, "revoked.user2", nil)
			p.signIn(t, u)
			return p.platform(t, "POST", "/v1/users/"+u.ID+"/revoke-sessions", map[string]any{})
		}, []string{"user.sessions_revoke"}, nil, "platform", ""},
		{"createMember new", func() *apitest.Response {
			r := p.tenantKey(t, "POST", p.membersPath(""), map[string]any{"account": "new", "username": "audited.second", "roles": []string{"tenant-admin"}})
			if r.Status == 201 {
				secrets = append(secrets, decode[adminapi.MemberCreated](t, r, 201).InitialPassword)
			}
			return r
		}, nil, []string{"member.add", "user.create"}, "service_account", ""},
		{"createMember existing", func() *apitest.Response {
			return p.tenantKey(t, "POST", p.membersPath(""), map[string]any{"account": "existing", "user_id": shared.ID, "roles": []string{"tenant-admin"}})
		}, nil, []string{"member.add"}, "service_account", shared.ID},
		{"updateMember", func() *apitest.Response {
			return p.tenantKey(t, "PATCH", p.membersPath("/"+member.Member.UserID), map[string]any{"display_name": "Audited"})
		}, nil, []string{"user.update"}, "service_account", member.Member.UserID},
		{"updateMember roles", func() *apitest.Response {
			return p.tenantKey(t, "PATCH", p.membersPath("/"+shared.ID), map[string]any{"roles": []string{"tenant-admin"}})
		}, nil, []string{}, "service_account", ""},
		{"disableMember", func() *apitest.Response {
			return p.tenantKey(t, "POST", p.membersPath("/"+member.Member.UserID+"/disable"), map[string]any{})
		}, nil, []string{"user.disable"}, "service_account", member.Member.UserID},
		{"enableMember", func() *apitest.Response {
			return p.tenantKey(t, "POST", p.membersPath("/"+member.Member.UserID+"/enable"), map[string]any{})
		}, nil, []string{"user.enable"}, "service_account", member.Member.UserID},
		{"resetMemberPassword", func() *apitest.Response {
			r := p.tenantKey(t, "POST", p.membersPath("/"+member.Member.UserID+"/reset-password"), map[string]any{})
			if r.Status == 200 {
				secrets = append(secrets, decode[adminapi.PasswordReset](t, r, 200).Password)
			}
			return r
		}, nil, []string{"user.password_reset"}, "service_account", member.Member.UserID},
		{"unlockMember", func() *apitest.Response {
			return p.tenantKey(t, "POST", p.membersPath("/"+member.Member.UserID+"/unlock"), map[string]any{})
		}, nil, []string{"user.unlock"}, "service_account", member.Member.UserID},
		{"revokeMemberSessions", func() *apitest.Response {
			return p.tenantKey(t, "POST", p.membersPath("/"+member.Member.UserID+"/revoke-sessions"), map[string]any{})
		}, nil, []string{"user.sessions_revoke"}, "service_account", member.Member.UserID},
		{"deleteMember", func() *apitest.Response {
			return p.tenantKey(t, "DELETE", p.membersPath("/"+shared.ID), nil)
		}, nil, []string{"member.remove"}, "service_account", shared.ID},
		{"deleteMember of a managed user", func() *apitest.Response {
			return p.tenantKey(t, "DELETE", p.membersPath("/"+member.Member.UserID), nil)
		}, nil, []string{"member.remove", "user.disable"}, "service_account", ""},
		{"a user acts: updateUser", func() *apitest.Response {
			return pb.Do(t, "PATCH", "/v1/users/"+plat.User.ID, map[string]any{"display_name": "By a user"}, nil)
		}, []string{"user.update"}, nil, "user", plat.User.ID},
	}
	for _, s := range steps {
		beforeP, beforeT := len(d.PlatformAuditRows(t)), len(d.AuditRows(t, p.tid))
		r := s.do()
		if r.Status != 200 && r.Status != 201 {
			t.Errorf("%s: %d %s", s.name, r.Status, r.Body)
			continue
		}
		gotP, gotT := d.PlatformAuditRows(t)[beforeP:], d.AuditRows(t, p.tid)[beforeT:]
		gotP, gotT = filterOut(gotP, "auth.sign_in"), filterOut(gotT, "auth.sign_in") // a step may sign people in first
		if s.name == "unlockUser" {                                                   // the ten failures before it are rows too
			gotP = filterOut(gotP, "auth.failure", "user.lock")
		}
		check := func(log string, got []dbtest.AuditRow, want []string) {
			t.Helper()
			var actions []string
			for _, row := range got {
				actions = append(actions, row.Action)
				if row.Outcome != "success" || row.ActorType != s.actor || row.KeyID != "" && s.actor == "user" {
					t.Errorf("%s: row %+v of the %s log: outcome/actor/key", s.name, row, log)
				}
				if s.actor == "user" && (row.ActorID == "" || row.ActorName == "") {
					t.Errorf("%s: a row of a user's act without the actor ID and name: %+v", s.name, row)
				}
				if s.resourceID != "" && (strings.HasPrefix(row.Action, "user.") || strings.HasPrefix(row.Action, "member.")) && row.ResourceID != s.resourceID {
					t.Errorf("%s: row %s about %q, want %q", s.name, row.Action, row.ResourceID, s.resourceID)
				}
			}
			sort.Strings(actions)
			if strings.Join(actions, ",") != strings.Join(want, ",") {
				t.Errorf("%s: %s log got %v, want %v", s.name, log, actions, want)
			}
		}
		check("platform", gotP, s.platform)
		check("tenant", gotT, s.tenant)
	}

	// Nothing secret is in any row: no password, no hash, no token, no typed username of a failure.
	all := d.AllAuditText(t)
	for _, v := range secrets {
		if v != "" && strings.Contains(all, v) {
			t.Errorf("a generated password is in the audit log")
		}
	}
	for _, v := range []string{"$argon2id", "password_hash", "pds_"} {
		if strings.Contains(all, v) {
			t.Errorf("the audit log holds %q", v)
		}
	}

	// A user's own acts: the actor is the user, the key ID is empty.
	nb := p.api.NewBrowser()
	u := d.PlatformUser(t, "self.changer", nil)
	nb.MustSignIn(t, u.Username, u.Password)
	next := dbtest.NewPassword(t)
	before := len(d.PlatformAuditRows(t))
	if r := nb.Do(t, "POST", "/v1/auth/password", map[string]any{"current_password": u.Password, "new_password": next}, nil); r.Status != 200 {
		t.Fatalf("changePassword: %d %s", r.Status, r.Body)
	}
	rows := d.PlatformAuditRows(t)[before:]
	if len(rows) != 1 || rows[0].Action != "user.password_change" || rows[0].ActorType != "user" || rows[0].ActorID != u.ID || rows[0].ActorName != u.Username || rows[0].KeyID != "" || rows[0].ResourceID != u.ID {
		t.Errorf("changePassword rows: %+v", rows)
	}
	for _, row := range rows {
		if strings.Contains(row.Raw, next) || strings.Contains(row.Raw, u.Password) {
			t.Error("a password is in the row of a password change")
		}
	}
	var signIns []dbtest.AuditRow
	for _, row := range d.PlatformAuditRows(t) {
		if row.Action == "auth.sign_in" && row.ActorID == u.ID {
			signIns = append(signIns, row)
		}
	}
	if len(signIns) != 1 || signIns[0].Detail["method"] != "local" || signIns[0].ActorType != "user" {
		t.Errorf("sign-in rows: %+v", signIns)
	}
}

func filterOut(rows []dbtest.AuditRow, actions ...string) []dbtest.AuditRow {
	var out []dbtest.AuditRow
next:
	for _, r := range rows {
		for _, a := range actions {
			if r.Action == a {
				continue next
			}
		}
		out = append(out, r)
	}
	return out
}

// TestPeopleAuditIsAtomic is the part of T-803 that says that the row is in the transaction of the
// change: when the audit log refuses the row, the user is not created.
func TestPeopleAuditIsAtomic(t *testing.T) {
	p := newPeople(t, apitest.PeopleConfig{})
	super := p.api.Super(t)
	for _, q := range []string{
		`CREATE FUNCTION pandokos.zz_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit is down'; END $$`,
		`CREATE TRIGGER a_fail BEFORE INSERT ON pandokos.audit_event FOR EACH ROW EXECUTE FUNCTION pandokos.zz_fail()`,
		`CREATE TRIGGER a_fail BEFORE INSERT ON pandokos.platform_audit_event FOR EACH ROW EXECUTE FUNCTION pandokos.zz_fail()`,
	} {
		if _, err := super.Exec(ctx(t), q); err != nil {
			t.Fatal(err)
		}
	}
	users := p.d.Count(t, "SELECT count(*) FROM pandokos.app_user")
	for name, r := range map[string]*apitest.Response{
		"createUser":   p.platform(t, "POST", "/v1/users", map[string]any{"sign_in": "local", "username": "atomic.user"}),
		"createMember": p.tenantKey(t, "POST", p.membersPath(""), map[string]any{"account": "new", "username": "atomic.member", "roles": []string{"tenant-admin"}}),
	} {
		if r.Status != 500 {
			t.Errorf("%s with the audit log down: %d, want 500", name, r.Status)
		}
	}
	if n := p.d.Count(t, "SELECT count(*) FROM pandokos.app_user"); n != users {
		t.Errorf("%d users were created although their audit rows were refused", n-users)
	}
	if n := p.d.Count(t, "SELECT count(*) FROM pandokos.local_account WHERE username LIKE 'atomic.%'"); n != 0 {
		t.Error("an account was created although the audit row was refused")
	}
}

// TestSignInAudit is T-804.
func TestSignInAudit(t *testing.T) {
	p := newPeople(t, apitest.PeopleConfig{})
	d := p.d
	user := d.PlatformUser(t, "audited.person", nil)
	member := d.TenantUser(t, p.tid, "audited.tenant", []string{"tenant-admin"})
	fail := func(username string) *apitest.Response {
		d.Age(t, "signin_throttle", "", time.Minute, "last_failure_at") // no delay is the point
		return p.api.NewBrowser().SignIn(t, username, "wrong password here")
	}

	t.Run("ten wrong passwords: ten failures and one lock", func(t *testing.T) {
		for i := 0; i < 10; i++ {
			fail(user.Username)
		}
		fails, locks := 0, 0
		for _, r := range d.PlatformAuditRows(t) {
			switch r.Action {
			case "auth.failure":
				fails++
				if r.ResourceType != "user" || r.ResourceID != user.ID || r.Detail["reason"] != "wrong_password" || r.Detail["method"] != "local" || r.Detail["listener"] != "admin" || r.ActorType != "anonymous" {
					t.Errorf("failure row %+v", r)
				}
			case "user.lock":
				locks++
				if r.ResourceID != user.ID || r.ActorType != "anonymous" {
					t.Errorf("lock row %+v", r)
				}
			}
		}
		if fails != 10 || locks != 1 {
			t.Errorf("%d failures and %d locks in the platform log, want 10 and 1", fails, locks)
		}
	})

	t.Run("an unknown username writes rows without a resource ID, and never the name", func(t *testing.T) {
		typed := apitest.Canary("username")
		before := len(d.PlatformAuditRows(t))
		fail(typed)
		rows := d.PlatformAuditRows(t)[before:]
		if len(rows) != 1 || rows[0].Action != "auth.failure" || rows[0].ResourceID != "" || rows[0].Detail["reason"] != "unknown_user" {
			t.Fatalf("rows %+v", rows)
		}
		if strings.Contains(d.AllAuditText(t), typed) {
			t.Error("the typed username is in the audit log")
		}
	})

	t.Run("the rows of a tenant's user are in the tenant's log", func(t *testing.T) {
		before := len(d.AuditRows(t, p.tid))
		beforeP := len(d.PlatformAuditRows(t))
		for i := 0; i < 10; i++ {
			fail(member.Username)
		}
		var fails, locks int
		for _, r := range d.AuditRows(t, p.tid)[before:] {
			switch r.Action {
			case "auth.failure":
				fails++
				if r.ResourceID != member.ID {
					t.Errorf("failure row %+v", r)
				}
			case "user.lock":
				locks++
			}
		}
		if fails != 10 || locks != 1 {
			t.Errorf("%d failures and %d locks in the tenant's log, want 10 and 1", fails, locks)
		}
		for _, r := range d.PlatformAuditRows(t)[beforeP:] {
			if r.ResourceID == member.ID {
				t.Errorf("a row of a tenant's user in the platform log: %+v", r)
			}
		}
	})

	t.Run("beyond the cap failures are logged only, the lock is still audited", func(t *testing.T) {
		q := newPeople(t, apitest.PeopleConfig{})
		victim := q.d.PlatformUser(t, "capped.person", nil)
		for i := 0; i < 80; i++ { // the cap is 60 a minute per instance
			q.d.Age(t, "signin_throttle", "", time.Minute, "last_failure_at")
			q.api.NewBrowser().SignIn(t, fmt.Sprintf("nobody.%d", i), "wrong password here")
		}
		failures := 0
		for _, r := range q.d.PlatformAuditRows(t) {
			if r.Action == "auth.failure" {
				failures++
			}
		}
		if failures == 0 || failures > 60 {
			t.Errorf("%d failure rows after 80 refused sign-ins, want at most the cap of 60", failures)
		}
		for i := 0; i < 10; i++ {
			q.d.Age(t, "signin_throttle", "", time.Minute, "last_failure_at")
			q.api.NewBrowser().SignIn(t, victim.Username, "wrong password here")
		}
		locks := 0
		for _, r := range q.d.PlatformAuditRows(t) {
			if r.Action == "user.lock" && r.ResourceID == victim.ID {
				locks++
			}
		}
		if locks != 1 {
			t.Errorf("%d lock rows beyond the cap, want 1", locks)
		}
		if !strings.Contains(q.api.Logs.String(), "authentication failed") {
			t.Error("a failure beyond the cap is not logged")
		}
	})
}
