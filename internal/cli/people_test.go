package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
)

const (
	uid  = "0192f0f0-0000-7000-8000-0000000000a1"
	uid2 = "0192f0f0-0000-7000-8000-0000000000a2"
)

func (f *fakeAPI) ListUsers(_ context.Context, in adminapi.ListUsersInput) (*adminapi.UserList, error) {
	return pick[adminapi.UserList](f, "listUsers"), f.rec("listUsers", in)
}
func (f *fakeAPI) CreateUser(_ context.Context, in adminapi.CreateUserInput) (*adminapi.UserCreated, error) {
	return pick[adminapi.UserCreated](f, "createUser"), f.rec("createUser", in)
}
func (f *fakeAPI) GetUser(_ context.Context, in adminapi.GetUserInput) (*adminapi.User, error) {
	return pick[adminapi.User](f, "getUser"), f.rec("getUser", in)
}
func (f *fakeAPI) UpdateUser(_ context.Context, in adminapi.UpdateUserInput) (*adminapi.User, error) {
	return pick[adminapi.User](f, "updateUser"), f.rec("updateUser", in)
}
func (f *fakeAPI) DisableUser(_ context.Context, in adminapi.DisableUserInput) (*adminapi.User, error) {
	return pick[adminapi.User](f, "disableUser"), f.rec("disableUser", in)
}
func (f *fakeAPI) EnableUser(_ context.Context, in adminapi.EnableUserInput) (*adminapi.User, error) {
	return pick[adminapi.User](f, "enableUser"), f.rec("enableUser", in)
}
func (f *fakeAPI) ResetUserPassword(_ context.Context, in adminapi.ResetUserPasswordInput) (*adminapi.PasswordReset, error) {
	return pick[adminapi.PasswordReset](f, "resetUserPassword"), f.rec("resetUserPassword", in)
}
func (f *fakeAPI) UnlockUser(_ context.Context, in adminapi.UnlockUserInput) (*adminapi.User, error) {
	return pick[adminapi.User](f, "unlockUser"), f.rec("unlockUser", in)
}
func (f *fakeAPI) RevokeUserSessions(_ context.Context, in adminapi.RevokeUserSessionsInput) (*adminapi.SessionsRevoked, error) {
	return pick[adminapi.SessionsRevoked](f, "revokeUserSessions"), f.rec("revokeUserSessions", in)
}
func (f *fakeAPI) ListMembers(_ context.Context, in adminapi.ListMembersInput) (*adminapi.MemberList, error) {
	return pick[adminapi.MemberList](f, "listMembers"), f.rec("listMembers", in)
}
func (f *fakeAPI) CreateMember(_ context.Context, in adminapi.CreateMemberInput) (*adminapi.MemberCreated, error) {
	return pick[adminapi.MemberCreated](f, "createMember"), f.rec("createMember", in)
}
func (f *fakeAPI) GetMember(_ context.Context, in adminapi.GetMemberInput) (*adminapi.Member, error) {
	return pick[adminapi.Member](f, "getMember"), f.rec("getMember", in)
}
func (f *fakeAPI) UpdateMember(_ context.Context, in adminapi.UpdateMemberInput) (*adminapi.Member, error) {
	return pick[adminapi.Member](f, "updateMember"), f.rec("updateMember", in)
}
func (f *fakeAPI) DeleteMember(_ context.Context, in adminapi.DeleteMemberInput) (*adminapi.Member, error) {
	return pick[adminapi.Member](f, "deleteMember"), f.rec("deleteMember", in)
}
func (f *fakeAPI) DisableMember(_ context.Context, in adminapi.DisableMemberInput) (*adminapi.Member, error) {
	return pick[adminapi.Member](f, "disableMember"), f.rec("disableMember", in)
}
func (f *fakeAPI) EnableMember(_ context.Context, in adminapi.EnableMemberInput) (*adminapi.Member, error) {
	return pick[adminapi.Member](f, "enableMember"), f.rec("enableMember", in)
}
func (f *fakeAPI) ResetMemberPassword(_ context.Context, in adminapi.ResetMemberPasswordInput) (*adminapi.PasswordReset, error) {
	return pick[adminapi.PasswordReset](f, "resetMemberPassword"), f.rec("resetMemberPassword", in)
}
func (f *fakeAPI) UnlockMember(_ context.Context, in adminapi.UnlockMemberInput) (*adminapi.Member, error) {
	return pick[adminapi.Member](f, "unlockMember"), f.rec("unlockMember", in)
}
func (f *fakeAPI) RevokeMemberSessions(_ context.Context, in adminapi.RevokeMemberSessionsInput) (*adminapi.SessionsRevoked, error) {
	return pick[adminapi.SessionsRevoked](f, "revokeMemberSessions"), f.rec("revokeMemberSessions", in)
}

var (
	person = adminapi.User{ID: uid, SignIn: adminapi.SignInMethod("local"), Username: "ada.lovelace", DisplayName: "Ada", Email: "ada@example.test",
		State: adminapi.UserState("active"), StateChangedAt: t0, PlatformRoles: []string{"platform-admin"}, CreatedAt: t0, UpdatedAt: t0}
	member = adminapi.Member{UserID: uid2, SignIn: adminapi.SignInMethod("local"), Username: "grace.hopper", DisplayName: "Grace", Email: "grace@example.test",
		State: adminapi.UserState("active"), ManagedBy: adminapi.ManagedBy("tenant"), Roles: []string{"tenant-admin"}, Source: adminapi.RoleSource("manual"), CreatedAt: t0, UpdatedAt: t0}
)

// TestEveryPeopleOperationHasACommand is the part of T-827 that reads the table: the two groups of
// operations are the only ones that gained commands, under the words of section 5.12.
func TestEveryPeopleOperationHasACommand(t *testing.T) {
	want := map[string]string{
		"listUsers": "platform-user list", "createUser": "platform-user create", "getUser": "platform-user get", "updateUser": "platform-user update",
		"disableUser": "platform-user disable", "enableUser": "platform-user enable", "resetUserPassword": "platform-user reset-password",
		"unlockUser": "platform-user unlock", "revokeUserSessions": "platform-user revoke-sessions",
		"listMembers": "member list", "createMember": "member add", "getMember": "member get", "updateMember": "member update",
		"deleteMember": "member remove", "disableMember": "member disable", "enableMember": "member enable",
		"resetMemberPassword": "member reset-password", "unlockMember": "member unlock", "revokeMemberSessions": "member revoke-sessions",
	}
	exempt := 0
	for _, op := range adminapi.Operations {
		if op.Permission == "" || op.Permission == authz.SessionSelf {
			exempt++
		}
	}
	if exempt != 8 {
		t.Errorf("%d operations are exempt from having a command, want the eight sign-in and session operations", exempt)
	}
	// Every operation answers --help under its words, which is how the table is seen from here.
	r := newRig(t)
	for id, words := range want {
		code, out, errs := r.run(t, append(strings.Fields(words), "--help")...)
		if code != 0 && code != 2 || !strings.Contains(out+errs, "Usage of "+words) {
			t.Errorf("%s: %q has no command (exit %d): %s%s", id, words, code, out, errs)
		}
	}
}

// TestPeopleCommands is T-827: flags map to the bodies, and a password is printed once and only on
// standard output.
func TestPeopleCommands(t *testing.T) {
	t.Run("platform-user list and get", func(t *testing.T) {
		r := newRig(t)
		r.f.out["listUsers"] = &adminapi.UserList{Items: []adminapi.User{person}}
		r.f.out["getUser"] = &person
		out, _ := mustRun(t, r, "platform-user", "list", "--state", "active", "--username", "ada.lovelace", "--sort", "-username", "--limit", "5")
		if got := r.f.only(t, "listUsers"); !reflect.DeepEqual(got, adminapi.ListUsersInput{Limit: 5, Sort: adminapi.UserSort("-username"), State: adminapi.UserState("active"), Username: "ada.lovelace"}) {
			t.Errorf("list: %+v", got)
		}
		if !strings.Contains(out, "ada.lovelace") || !strings.Contains(out, uid) {
			t.Errorf("list output:\n%s", out)
		}
		out, _ = mustRun(t, r, "platform-user", "get", uid)
		if got := r.f.only(t, "getUser"); !reflect.DeepEqual(got, adminapi.GetUserInput{User: uid}) {
			t.Errorf("get: %+v", got)
		}
		for _, want := range []string{"id: " + uid, "username: ada.lovelace", "state: active"} {
			if !lineWith(out, want) {
				t.Errorf("get output lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("platform-user create prints the password once, on standard output", func(t *testing.T) {
		r := newRig(t)
		const pw = "abcdefghijklmnopqrstuvwx"
		r.f.out["createUser"] = &adminapi.UserCreated{User: person, InitialPassword: pw}
		out, errs := mustRun(t, r, "platform-user", "create", "--sign-in", "local", "--username", "ada.lovelace", "--display-name", "Ada", "--email", "ada@example.test", "--role", "platform-admin")
		want := adminapi.CreateUserInput{Body: adminapi.UserCreate{SignIn: adminapi.SignInMethod("local"), Username: "ada.lovelace", DisplayName: "Ada", Email: "ada@example.test", PlatformRoles: []string{"platform-admin"}}}
		if got := r.f.only(t, "createUser"); !reflect.DeepEqual(got, want) {
			t.Errorf("create: %+v", got)
		}
		if strings.Count(out, pw) != 1 || strings.Contains(errs, pw) {
			t.Errorf("the password must be on stdout once and nowhere else:\nstdout %q\nstderr %q", out, errs)
		}
		// -o json prints it once as well.
		out, errs = mustRun(t, r, "platform-user", "create", "--sign-in", "local", "--username", "ada.lovelace", "-o", "json")
		if strings.Count(out, pw) != 1 || strings.Contains(errs, pw) {
			t.Errorf("json: stdout %q stderr %q", out, errs)
		}
		// With an out-file it is written there (mode 0600) and never printed.
		file := filepath.Join(t.TempDir(), "initial.password")
		out, errs = mustRun(t, r, "platform-user", "create", "--sign-in", "local", "--username", "ada.lovelace", "--password-out", file)
		if strings.Contains(out+errs, pw) || !lineWith(out, "password written to "+file) {
			t.Errorf("stdout %q stderr %q", out, errs)
		}
		st, err := os.Stat(file)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("out-file: %v %v", st, err)
		}
		if b, _ := os.ReadFile(file); string(b) != pw+"\n" {
			t.Errorf("the out-file holds %q", b)
		}
		// An existing out-file is an error and nothing is created.
		r.f.got = map[string][]any{}
		code, out, errs := r.run(t, "platform-user", "create", "--sign-in", "local", "--username", "x.user", "--password-out", file)
		if code == 0 || strings.Contains(out+errs, pw) || len(r.f.calls("createUser")) != 0 {
			t.Errorf("exit %d, stdout %q stderr %q, %d calls", code, out, errs, len(r.f.calls("createUser")))
		}
		// An oidc user has no password: the subject is the body.
		r.f.out["createUser"] = &adminapi.UserCreated{User: adminapi.User{ID: uid, SignIn: adminapi.SignInMethod("oidc"), Subject: "sub-1", State: adminapi.UserState("active")}}
		r.f.got = map[string][]any{}
		mustRun(t, r, "platform-user", "create", "--sign-in", "oidc", "--subject", "sub-1")
		if got := r.f.only(t, "createUser"); !reflect.DeepEqual(got, adminapi.CreateUserInput{Body: adminapi.UserCreate{SignIn: adminapi.SignInMethod("oidc"), Subject: "sub-1"}}) {
			t.Errorf("create oidc: %+v", got)
		}
	})

	t.Run("platform-user update, disable, enable, unlock, revoke-sessions", func(t *testing.T) {
		r := newRig(t)
		for _, id := range []string{"updateUser", "disableUser", "enableUser", "unlockUser"} {
			r.f.out[id] = &person
		}
		r.f.out["revokeUserSessions"] = &adminapi.SessionsRevoked{Revoked: 3}
		mustRun(t, r, "platform-user", "update", "--display-name", "Ada L", "--role", "platform-admin", uid)
		if got := r.f.only(t, "updateUser"); !reflect.DeepEqual(got, adminapi.UpdateUserInput{User: uid, Body: adminapi.UserUpdate{DisplayName: adminapi.Some("Ada L"), PlatformRoles: adminapi.Some([]string{"platform-admin"})}}) {
			t.Errorf("update: %+v", got)
		}
		if !strings.Contains(r.f.patchBody, `"display_name":"Ada L"`) || strings.Contains(r.f.patchBody, `"email"`) {
			t.Errorf("an absent flag must be an absent member on the wire: %s", r.f.patchBody)
		}
		for _, c := range []struct{ verb, id string }{{"disable", "disableUser"}, {"enable", "enableUser"}, {"unlock", "unlockUser"}} {
			mustRun(t, r, "platform-user", c.verb, "--reason", "because", uid)
			if n := len(r.f.calls(c.id)); n != 1 {
				t.Errorf("%s: %d calls of %s", c.verb, n, c.id)
			}
		}
		if got := r.f.only(t, "disableUser"); !reflect.DeepEqual(got, adminapi.DisableUserInput{User: uid, Body: adminapi.Reason{Reason: "because"}}) {
			t.Errorf("disable: %+v", got)
		}
		out, _ := mustRun(t, r, "platform-user", "revoke-sessions", uid)
		if got := r.f.only(t, "revokeUserSessions"); !reflect.DeepEqual(got, adminapi.RevokeUserSessionsInput{User: uid}) {
			t.Errorf("revoke-sessions: %+v", got)
		}
		if !lineWith(out, "revoked: 3") {
			t.Errorf("revoke-sessions output:\n%s", out)
		}
	})

	t.Run("platform-user reset-password", func(t *testing.T) {
		r := newRig(t)
		const pw = "zyxwvutsrqponmlkjihgfedc"
		r.f.out["resetUserPassword"] = &adminapi.PasswordReset{UserID: uid, Password: pw, ExpiresAt: t1}
		out, errs := mustRun(t, r, "platform-user", "reset-password", uid)
		if got := r.f.only(t, "resetUserPassword"); !reflect.DeepEqual(got, adminapi.ResetUserPasswordInput{User: uid}) {
			t.Errorf("reset-password: %+v", got)
		}
		if strings.Count(out, pw) != 1 || strings.Contains(errs, pw) {
			t.Errorf("stdout %q stderr %q", out, errs)
		}
		file := filepath.Join(t.TempDir(), "reset.password")
		out, errs = mustRun(t, r, "platform-user", "reset-password", "--password-out", file, uid)
		if strings.Contains(out+errs, pw) {
			t.Errorf("the password is printed although an out-file was given: %q %q", out, errs)
		}
		if b, _ := os.ReadFile(file); string(b) != pw+"\n" {
			t.Errorf("the out-file holds %q", b)
		}
	})

	t.Run("member commands", func(t *testing.T) {
		r := newRig(t)
		const pw = "mnopqrstuvwxyzabcdefghij"
		r.f.out["listMembers"] = &adminapi.MemberList{Items: []adminapi.Member{member}}
		for _, id := range []string{"getMember", "updateMember", "deleteMember", "disableMember", "enableMember", "unlockMember"} {
			r.f.out[id] = &member
		}
		r.f.out["createMember"] = &adminapi.MemberCreated{Member: member, InitialPassword: pw}
		r.f.out["resetMemberPassword"] = &adminapi.PasswordReset{UserID: uid2, Password: pw, ExpiresAt: t1}
		r.f.out["revokeMemberSessions"] = &adminapi.SessionsRevoked{Revoked: 1}

		mustRun(t, r, "member", "list", "--tenant", tid, "--username", "grace.hopper", "--sort", "-created_at", "--limit", "7")
		if got := r.f.only(t, "listMembers"); !reflect.DeepEqual(got, adminapi.ListMembersInput{Tenant: tid, Limit: 7, Sort: adminapi.Sort("-created_at"), Username: "grace.hopper"}) {
			t.Errorf("list: %+v", got)
		}
		mustRun(t, r, "member", "get", "--tenant", tid, uid2)
		if got := r.f.only(t, "getMember"); !reflect.DeepEqual(got, adminapi.GetMemberInput{Tenant: tid, User: uid2}) {
			t.Errorf("get: %+v", got)
		}

		// A new account: the password is printed once on stdout.
		out, errs := mustRun(t, r, "member", "add", "--tenant", tid, "--new", "--username", "grace.hopper", "--display-name", "Grace", "--role", "tenant-admin")
		want := adminapi.CreateMemberInput{Tenant: tid, Body: adminapi.MemberCreate{Account: adminapi.MemberAccountKind("new"), Username: "grace.hopper", DisplayName: "Grace", Roles: []string{"tenant-admin"}}}
		if got := r.f.only(t, "createMember"); !reflect.DeepEqual(got, want) {
			t.Errorf("add a new account: %+v", got)
		}
		if strings.Count(out, pw) != 1 || strings.Contains(errs, pw) {
			t.Errorf("add: stdout %q stderr %q", out, errs)
		}
		// An existing account, by ID and by username.
		r.f.got = map[string][]any{}
		r.f.out["createMember"] = &adminapi.MemberCreated{Member: member}
		mustRun(t, r, "member", "add", "--tenant", tid, "--user-id", uid, "--role", "tenant-admin")
		if got := r.f.only(t, "createMember"); !reflect.DeepEqual(got, adminapi.CreateMemberInput{Tenant: tid, Body: adminapi.MemberCreate{Account: adminapi.MemberAccountKind("existing"), UserID: uid, Roles: []string{"tenant-admin"}}}) {
			t.Errorf("add an existing account by ID: %+v", got)
		}
		r.f.got = map[string][]any{}
		mustRun(t, r, "member", "add", "--tenant", tid, "--username", "ada.lovelace", "--role", "tenant-admin")
		if got := r.f.only(t, "createMember"); !reflect.DeepEqual(got, adminapi.CreateMemberInput{Tenant: tid, Body: adminapi.MemberCreate{Account: adminapi.MemberAccountKind("existing"), Username: "ada.lovelace", Roles: []string{"tenant-admin"}}}) {
			t.Errorf("add an existing account by username: %+v", got)
		}
		if code, _, _ := r.run(t, "member", "add", "--tenant", tid, "--role", "tenant-admin"); code == 0 {
			t.Error("member add without who to add")
		}
		if code, _, _ := r.run(t, "member", "add", "--tenant", tid, "--user-id", uid, "--username", "x.y"); code == 0 {
			t.Error("member add with both an ID and a username")
		}

		mustRun(t, r, "member", "update", "--tenant", tid, "--role", "tenant-admin", "--email", "g@example.test", uid2)
		if got := r.f.only(t, "updateMember"); !reflect.DeepEqual(got, adminapi.UpdateMemberInput{Tenant: tid, User: uid2, Body: adminapi.MemberUpdate{Roles: adminapi.Some([]string{"tenant-admin"}), Email: adminapi.Some("g@example.test")}}) {
			t.Errorf("update: %+v", got)
		}
		mustRun(t, r, "member", "remove", "--tenant", tid, uid2)
		if got := r.f.only(t, "deleteMember"); !reflect.DeepEqual(got, adminapi.DeleteMemberInput{Tenant: tid, User: uid2}) {
			t.Errorf("remove: %+v", got)
		}
		for _, c := range []struct{ verb, id string }{{"disable", "disableMember"}, {"enable", "enableMember"}, {"unlock", "unlockMember"}, {"revoke-sessions", "revokeMemberSessions"}, {"reset-password", "resetMemberPassword"}} {
			r.f.got = map[string][]any{}
			out, errs := mustRun(t, r, "member", c.verb, "--tenant", tid, uid2)
			if n := len(r.f.calls(c.id)); n != 1 {
				t.Errorf("%s: %d calls", c.verb, n)
			}
			if c.verb == "reset-password" && (strings.Count(out, pw) != 1 || strings.Contains(errs, pw)) {
				t.Errorf("reset-password: stdout %q stderr %q", out, errs)
			}
		}
		// A tenant is named by name or ID, as everywhere else.
		code, _, errs2 := r.run(t, "member", "list")
		if code == 0 || !strings.Contains(errs2, "--tenant") {
			t.Errorf("member list without a tenant: exit %d, stderr %q", code, errs2)
		}
	})
}
