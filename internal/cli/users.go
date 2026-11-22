package cli

import (
	"flag"
	"fmt"

	"github.com/pandokos/pandokos/internal/adminapi"
)

func init() { commands = append(commands, peopleCommands...) }

// resolveUser turns the ID or the username of a platform user into an ID.
func (a *app) resolveUser(v string) (string, error) {
	if reUUID.MatchString(v) {
		return v, nil
	}
	l, err := a.client.ListUsers(a.ctx, adminapi.ListUsersInput{Username: v})
	if err != nil {
		return "", err
	}
	for _, u := range l.Items {
		if u.Username == v {
			return u.ID, nil
		}
	}
	return "", fmt.Errorf("no platform user named %q", v)
}

// resolveMember turns the ID or the username of a member into an ID.
func (a *app) resolveMember(tenantID, v string) (string, error) {
	if reUUID.MatchString(v) {
		return v, nil
	}
	l, err := a.client.ListMembers(a.ctx, adminapi.ListMembersInput{Tenant: tenantID, Username: v})
	if err != nil {
		return "", err
	}
	for _, m := range l.Items {
		if m.Username == v {
			return m.UserID, nil
		}
	}
	return "", fmt.Errorf("no member named %q", v)
}

// printPassword prints a generated password once: on standard output, or into the out-file when one
// was given, in which case the output only says where it is.
func (a *app) printPassword(reset *adminapi.PasswordReset, out *keyOut) error {
	return a.printSecretCreated("password", func() error {
		fmt.Fprintf(a.stdout, "user_id: %s\nexpires_at: %s\n", reset.UserID, reset.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"))
		return nil
	}, reset, reset.Password, out, func() { reset.Password = "" })
}

// memberFlags are the flags of the commands on one member.
type memberFlags struct{ tenant string }

func (m *memberFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&m.tenant, "tenant", "", "tenant ID or name")
}

var peopleCommands = []command{
	{[]string{"platform-user", "list"}, "listUsers", func(a *app, args []string) error {
		fs := a.flags("platform-user list")
		var l listFlags
		l.register(fs)
		username := fs.String("username", "", "only the user with this username")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListUsers(a.ctx, adminapi.ListUsersInput{State: adminapi.UserState(l.state), Username: *username, Sort: adminapi.UserSort(l.sort), Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			items := make([]any, len(out.Items))
			for i, v := range out.Items {
				items[i] = v
			}
			return items, out.NextCursor, nil
		}, func(items []any) any { return map[string]any{"items": items} })
	}},

	{[]string{"platform-user", "create"}, "createUser", func(a *app, args []string) error {
		fs := a.flags("platform-user create")
		signIn := fs.String("sign-in", "", "the sign-in method: local or oidc (required)")
		username := fs.String("username", "", "username of a local user")
		subject := fs.String("subject", "", "subject at the identity provider of an oidc user")
		display := fs.String("display-name", "", "display name")
		email := fs.String("email", "", "e-mail address")
		var roles multi
		fs.Var(&roles, "role", "platform role (repeatable)")
		passwordOut := fs.String("password-out", "", "write the first password to this new file (mode 0600) instead of printing it")
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		if *signIn == "" {
			return usagef("platform-user create needs --sign-in")
		}
		if err := a.connect(); err != nil {
			return err
		}
		out, err := openSecretOut(*passwordOut, "password")
		if err != nil {
			return err
		}
		res, err := a.client.CreateUser(a.ctx, adminapi.CreateUserInput{Body: adminapi.UserCreate{
			SignIn: adminapi.SignInMethod(*signIn), Username: *username, Subject: *subject, DisplayName: *display, Email: *email, PlatformRoles: roles}})
		if err != nil {
			out.abandon()
			return err
		}
		return a.printCreatedAccount(res.InitialPassword, out, res, func() error { return a.printResource(res.User) }, func() { res.InitialPassword = "" })
	}},

	{[]string{"platform-user", "get"}, "getUser", func(a *app, args []string) error {
		pos, err := a.begin(a.flags("platform-user get"), args, 1)
		if err != nil {
			return err
		}
		id, err := a.resolveUser(pos[0])
		if err != nil {
			return err
		}
		u, err := a.client.GetUser(a.ctx, adminapi.GetUserInput{User: id})
		if err != nil {
			return err
		}
		return a.printResource(u)
	}},

	{[]string{"platform-user", "update"}, "updateUser", func(a *app, args []string) error {
		fs := a.flags("platform-user update")
		display := fs.String("display-name", "", "new display name")
		email := fs.String("email", "", "new e-mail address")
		var roles multi
		fs.Var(&roles, "role", "platform role (repeatable; replaces the set)")
		noRoles := fs.Bool("no-roles", false, "remove every platform role")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		body := adminapi.UserUpdate{}
		if given(fs, "display-name") {
			body.DisplayName = adminapi.Some(*display)
		}
		if given(fs, "email") {
			body.Email = adminapi.Some(*email)
		}
		switch {
		case *noRoles && len(roles) > 0:
			return usagef("--no-roles cannot be combined with --role")
		case *noRoles:
			body.PlatformRoles = adminapi.Some([]string{})
		case len(roles) > 0:
			body.PlatformRoles = adminapi.Some([]string(roles))
		}
		if !body.DisplayName.Set && !body.Email.Set && !body.PlatformRoles.Set {
			return usagef("platform-user update needs a member to change")
		}
		if err := a.connect(); err != nil {
			return err
		}
		id, err := a.resolveUser(pos[0])
		if err != nil {
			return err
		}
		u, err := a.client.UpdateUser(a.ctx, adminapi.UpdateUserInput{User: id, Body: body})
		if err != nil {
			return err
		}
		return a.printResource(u)
	}},

	{[]string{"platform-user", "disable"}, "disableUser", func(a *app, args []string) error {
		return userAction(a, "platform-user disable", args, func(id, reason string) (any, error) {
			return a.client.DisableUser(a.ctx, adminapi.DisableUserInput{User: id, Body: adminapi.Reason{Reason: reason}})
		})
	}},

	{[]string{"platform-user", "enable"}, "enableUser", func(a *app, args []string) error {
		return userAction(a, "platform-user enable", args, func(id, reason string) (any, error) {
			return a.client.EnableUser(a.ctx, adminapi.EnableUserInput{User: id, Body: adminapi.Reason{Reason: reason}})
		})
	}},

	{[]string{"platform-user", "unlock"}, "unlockUser", func(a *app, args []string) error {
		return userAction(a, "platform-user unlock", args, func(id, reason string) (any, error) {
			return a.client.UnlockUser(a.ctx, adminapi.UnlockUserInput{User: id, Body: adminapi.Reason{Reason: reason}})
		})
	}},

	{[]string{"platform-user", "revoke-sessions"}, "revokeUserSessions", func(a *app, args []string) error {
		return userAction(a, "platform-user revoke-sessions", args, func(id, reason string) (any, error) {
			return a.client.RevokeUserSessions(a.ctx, adminapi.RevokeUserSessionsInput{User: id, Body: adminapi.Reason{Reason: reason}})
		})
	}},

	{[]string{"platform-user", "reset-password"}, "resetUserPassword", func(a *app, args []string) error {
		fs := a.flags("platform-user reset-password")
		reason := fs.String("reason", "", "recorded in the audit log")
		passwordOut := fs.String("password-out", "", "write the new password to this new file (mode 0600) instead of printing it")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		if err := a.connect(); err != nil {
			return err
		}
		out, err := openSecretOut(*passwordOut, "password")
		if err != nil {
			return err
		}
		id, err := a.resolveUser(pos[0])
		if err != nil {
			out.abandon()
			return err
		}
		res, err := a.client.ResetUserPassword(a.ctx, adminapi.ResetUserPasswordInput{User: id, Body: adminapi.RequiredReason{Reason: *reason}})
		if err != nil {
			out.abandon()
			return err
		}
		return a.printPassword(res, out)
	}},

	{[]string{"member", "list"}, "listMembers", func(a *app, args []string) error {
		fs := a.flags("member list")
		var m memberFlags
		m.register(fs)
		var l listFlags
		fs.StringVar(&l.sort, "sort", "", "order of the items")
		fs.StringVar(&l.cursor, "cursor", "", "the next_cursor of the previous page")
		fs.IntVar(&l.limit, "limit", 0, "page size")
		fs.BoolVar(&l.all, "all", false, "follow the cursors to the end")
		username := fs.String("username", "", "only the member with this username")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(m.tenant)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListMembers(a.ctx, adminapi.ListMembersInput{Tenant: tid, Username: *username, Sort: adminapi.Sort(l.sort), Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			items := make([]any, len(out.Items))
			for i, v := range out.Items {
				items[i] = v
			}
			return items, out.NextCursor, nil
		}, func(items []any) any { return map[string]any{"items": items} })
	}},

	{[]string{"member", "add"}, "createMember", func(a *app, args []string) error {
		fs := a.flags("member add")
		var m memberFlags
		m.register(fs)
		isNew := fs.Bool("new", false, "create a new account that the tenant manages (needs --username)")
		username := fs.String("username", "", "username of the new account, or of the existing platform user to add")
		userID := fs.String("user-id", "", "ID of the existing platform user to add")
		display := fs.String("display-name", "", "display name of a new account")
		email := fs.String("email", "", "e-mail address of a new account")
		var roles multi
		fs.Var(&roles, "role", "role in the tenant (repeatable; at least one)")
		passwordOut := fs.String("password-out", "", "write the first password to this new file (mode 0600) instead of printing it")
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		body := adminapi.MemberCreate{Roles: roles}
		switch {
		case len(roles) == 0:
			return usagef("member add needs at least one --role")
		case *isNew && (*username == "" || *userID != ""):
			return usagef("--new needs --username and no --user-id")
		case !*isNew && (*username == "") == (*userID == ""):
			return usagef("member add needs --new with --username, or exactly one of --user-id and --username")
		case !*isNew && (*display != "" || *email != ""):
			return usagef("--display-name and --email are for a new account")
		case *isNew:
			body.Account, body.Username, body.DisplayName, body.Email = adminapi.MemberAccountKindNew, *username, *display, *email
		default:
			body.Account, body.Username, body.UserID = adminapi.MemberAccountKindExisting, *username, *userID
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(m.tenant)
		if err != nil {
			return err
		}
		out, err := openSecretOut(*passwordOut, "password")
		if err != nil {
			return err
		}
		res, err := a.client.CreateMember(a.ctx, adminapi.CreateMemberInput{Tenant: tid, Body: body})
		if err != nil {
			out.abandon()
			return err
		}
		return a.printCreatedAccount(res.InitialPassword, out, res, func() error { return a.printResource(res.Member) }, func() { res.InitialPassword = "" })
	}},

	{[]string{"member", "get"}, "getMember", func(a *app, args []string) error {
		return memberAction(a, "member get", args, func(tid, id, _ string) (any, error) {
			return a.client.GetMember(a.ctx, adminapi.GetMemberInput{Tenant: tid, User: id})
		})
	}},

	{[]string{"member", "update"}, "updateMember", func(a *app, args []string) error {
		fs := a.flags("member update")
		var m memberFlags
		m.register(fs)
		display := fs.String("display-name", "", "new display name (a user that the tenant manages)")
		email := fs.String("email", "", "new e-mail address (a user that the tenant manages)")
		var roles multi
		fs.Var(&roles, "role", "role in the tenant (repeatable; replaces the set)")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		body := adminapi.MemberUpdate{}
		if given(fs, "display-name") {
			body.DisplayName = adminapi.Some(*display)
		}
		if given(fs, "email") {
			body.Email = adminapi.Some(*email)
		}
		if len(roles) > 0 {
			body.Roles = adminapi.Some([]string(roles))
		}
		if !body.DisplayName.Set && !body.Email.Set && !body.Roles.Set {
			return usagef("member update needs a member to change")
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(m.tenant)
		if err != nil {
			return err
		}
		id, err := a.resolveMember(tid, pos[0])
		if err != nil {
			return err
		}
		res, err := a.client.UpdateMember(a.ctx, adminapi.UpdateMemberInput{Tenant: tid, User: id, Body: body})
		if err != nil {
			return err
		}
		return a.printResource(res)
	}},

	{[]string{"member", "remove"}, "deleteMember", func(a *app, args []string) error {
		return memberAction(a, "member remove", args, func(tid, id, _ string) (any, error) {
			return a.client.DeleteMember(a.ctx, adminapi.DeleteMemberInput{Tenant: tid, User: id})
		})
	}},

	{[]string{"member", "disable"}, "disableMember", func(a *app, args []string) error {
		return memberAction(a, "member disable", args, func(tid, id, reason string) (any, error) {
			return a.client.DisableMember(a.ctx, adminapi.DisableMemberInput{Tenant: tid, User: id, Body: adminapi.Reason{Reason: reason}})
		})
	}},

	{[]string{"member", "enable"}, "enableMember", func(a *app, args []string) error {
		return memberAction(a, "member enable", args, func(tid, id, reason string) (any, error) {
			return a.client.EnableMember(a.ctx, adminapi.EnableMemberInput{Tenant: tid, User: id, Body: adminapi.Reason{Reason: reason}})
		})
	}},

	{[]string{"member", "unlock"}, "unlockMember", func(a *app, args []string) error {
		return memberAction(a, "member unlock", args, func(tid, id, reason string) (any, error) {
			return a.client.UnlockMember(a.ctx, adminapi.UnlockMemberInput{Tenant: tid, User: id, Body: adminapi.Reason{Reason: reason}})
		})
	}},

	{[]string{"member", "revoke-sessions"}, "revokeMemberSessions", func(a *app, args []string) error {
		return memberAction(a, "member revoke-sessions", args, func(tid, id, reason string) (any, error) {
			return a.client.RevokeMemberSessions(a.ctx, adminapi.RevokeMemberSessionsInput{Tenant: tid, User: id, Body: adminapi.Reason{Reason: reason}})
		})
	}},

	{[]string{"member", "reset-password"}, "resetMemberPassword", func(a *app, args []string) error {
		fs := a.flags("member reset-password")
		var m memberFlags
		m.register(fs)
		reason := fs.String("reason", "", "recorded in the audit log")
		passwordOut := fs.String("password-out", "", "write the new password to this new file (mode 0600) instead of printing it")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(m.tenant)
		if err != nil {
			return err
		}
		out, err := openSecretOut(*passwordOut, "password")
		if err != nil {
			return err
		}
		id, err := a.resolveMember(tid, pos[0])
		if err != nil {
			out.abandon()
			return err
		}
		res, err := a.client.ResetMemberPassword(a.ctx, adminapi.ResetMemberPasswordInput{Tenant: tid, User: id, Body: adminapi.RequiredReason{Reason: *reason}})
		if err != nil {
			out.abandon()
			return err
		}
		return a.printPassword(res, out)
	}},
}

// printCreatedAccount prints a created user or member: the resource, then the first password once, or where it
// was written. An account without a password (an oidc user, a platform user added as a member) leaves
// an out-file nothing to hold, and the file is removed.
func (a *app) printCreatedAccount(pw string, out *keyOut, full any, text func() error, blank func()) error {
	if pw == "" {
		out.abandon()
		if a.output == "json" {
			return a.printJSON(full)
		}
		return text()
	}
	return a.printSecretCreated("password", text, full, pw, out, blank)
}

// userAction runs a command that takes a platform user and an optional --reason.
func userAction(a *app, name string, args []string, call func(id, reason string) (any, error)) error {
	fs := a.flags(name)
	reason := fs.String("reason", "", "recorded in the audit log")
	pos, err := a.begin(fs, args, 1)
	if err != nil {
		return err
	}
	id, err := a.resolveUser(pos[0])
	if err != nil {
		return err
	}
	res, err := call(id, *reason)
	if err != nil {
		return err
	}
	return a.printResource(res)
}

// memberAction runs a command that takes a tenant and a member, with an optional --reason.
func memberAction(a *app, name string, args []string, call func(tenantID, id, reason string) (any, error)) error {
	fs := a.flags(name)
	var m memberFlags
	m.register(fs)
	reason := fs.String("reason", "", "recorded in the audit log")
	pos, err := a.begin(fs, args, 1)
	if err != nil {
		return err
	}
	tid, err := a.resolveTenant(m.tenant)
	if err != nil {
		return err
	}
	id, err := a.resolveMember(tid, pos[0])
	if err != nil {
		return err
	}
	res, err := call(tid, id, *reason)
	if err != nil {
		return err
	}
	return a.printResource(res)
}
