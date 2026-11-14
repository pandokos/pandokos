package cli

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// multi is a repeatable string flag.
type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func parseTime(flagName, v string) (*time.Time, error) {
	if v == "" {
		return nil, nil //nolint:nilnil // the flag was not given
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil, usagef("%s must be an RFC 3339 time", flagName)
	}
	t = t.UTC()
	return &t, nil
}

// listFlags are the flags of every list command.
type listFlags struct {
	state, sort, cursor string
	limit               int
	all                 bool
}

func (l *listFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&l.state, "state", "", "only items in this state")
	fs.StringVar(&l.sort, "sort", "", "order of the items")
	fs.StringVar(&l.cursor, "cursor", "", "the next_cursor of the previous page")
	fs.IntVar(&l.limit, "limit", 0, "page size")
	fs.BoolVar(&l.all, "all", false, "follow the cursors to the end")
}

// paged runs fetch for one page, or for every page with --all, and prints the items.
func (a *app) paged(l *listFlags, fetch func(cursor string) ([]any, string, error), raw func(items []any) any) error {
	cursor := l.cursor
	var all []any
	for {
		items, next, err := fetch(cursor)
		if err != nil {
			return err
		}
		all = append(all, items...)
		if next == "" || !l.all {
			if next != "" {
				defer fmt.Fprintf(a.stderr, "more: --cursor %s\n", next)
			}
			break
		}
		cursor = next
	}
	if a.output == "json" {
		return a.printJSON(raw(all))
	}
	a.printTable(all)
	return nil
}

func (a *app) begin(fs *flag.FlagSet, args []string, positional int) ([]string, error) {
	pos, err := parse(fs, args, positional)
	if err != nil {
		return nil, err
	}
	return pos, a.connect()
}

var commands = []command{
	{[]string{"whoami"}, "getMe", func(a *app, args []string) error {
		if _, err := a.begin(a.flags("whoami"), args, 0); err != nil {
			return err
		}
		me, err := a.client.GetMe(a.ctx, adminapi.GetMeInput{})
		if err != nil {
			return err
		}
		return a.printResource(me)
	}},

	{[]string{"tenant", "list"}, "listTenants", func(a *app, args []string) error {
		fs := a.flags("tenant list")
		var l listFlags
		l.register(fs)
		name := fs.String("name", "", "only the tenant with this name")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListTenants(a.ctx, adminapi.ListTenantsInput{State: adminapi.TenantState(l.state), Name: *name, Sort: adminapi.NamedSort(l.sort), Limit: l.limit, Cursor: cursor})
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

	{[]string{"tenant", "create"}, "createTenant", func(a *app, args []string) error {
		fs := a.flags("tenant create")
		display := fs.String("display-name", "", "display name")
		adminName := fs.String("admin-name", "", "name of the first service account")
		expires := fs.String("admin-key-expires-at", "", "expiry of the first key (RFC 3339)")
		keyFile := fs.String("admin-key-out", "", "write the first key to this new file")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		exp, err := parseTime("--admin-key-expires-at", *expires)
		if err != nil {
			return err
		}
		if err := a.connect(); err != nil {
			return err
		}
		out, err := openKeyOut(*keyFile)
		if err != nil {
			return err
		}
		res, err := a.client.CreateTenant(a.ctx, adminapi.CreateTenantInput{Body: adminapi.TenantCreate{
			Name: pos[0], DisplayName: *display, AdminServiceAccountName: *adminName, AdminKeyExpiresAt: exp}})
		if err != nil {
			out.abandon()
			return err
		}
		return a.printCreated(func() error {
			if err := a.printResource(res.Tenant); err != nil {
				return err
			}
			fmt.Fprintf(a.stdout, "admin_service_account_id: %s\nadmin_key_id: %s\n", res.AdminServiceAccount.ID, res.AdminKey.Key.ID)
			return nil
		}, res, res.AdminKey.Secret, out, func() { res.AdminKey.Secret = "" })
	}},

	{[]string{"tenant", "get"}, "getTenant", func(a *app, args []string) error {
		pos, err := a.begin(a.flags("tenant get"), args, 1)
		if err != nil {
			return err
		}
		id, err := a.resolveTenant(pos[0])
		if err != nil {
			return err
		}
		t, err := a.client.GetTenant(a.ctx, adminapi.GetTenantInput{Tenant: id})
		if err != nil {
			return err
		}
		return a.printResource(t)
	}},

	{[]string{"tenant", "update"}, "updateTenant", func(a *app, args []string) error {
		fs := a.flags("tenant update")
		name := fs.String("name", "", "new name")
		display := fs.String("display-name", "", "new display name")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		if *name == "" && *display == "" {
			return usagef("tenant update needs --name or --display-name")
		}
		id, err := a.resolveTenant(pos[0])
		if err != nil {
			return err
		}
		t, err := a.client.UpdateTenant(a.ctx, adminapi.UpdateTenantInput{Tenant: id, Body: adminapi.TenantUpdate{Name: nonEmpty(*name), DisplayName: nonEmpty(*display)}})
		if err != nil {
			return err
		}
		return a.printResource(t)
	}},

	{[]string{"tenant", "suspend"}, "suspendTenant", func(a *app, args []string) error {
		fs := a.flags("tenant suspend")
		reason := fs.String("reason", "", "recorded in the audit log")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		id, err := a.resolveTenant(pos[0])
		if err != nil {
			return err
		}
		t, err := a.client.SuspendTenant(a.ctx, adminapi.SuspendTenantInput{Tenant: id, Body: adminapi.Reason{Reason: *reason}})
		if err != nil {
			return err
		}
		return a.printResource(t)
	}},

	{[]string{"tenant", "reactivate"}, "reactivateTenant", func(a *app, args []string) error {
		fs := a.flags("tenant reactivate")
		reason := fs.String("reason", "", "recorded in the audit log")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		id, err := a.resolveTenant(pos[0])
		if err != nil {
			return err
		}
		t, err := a.client.ReactivateTenant(a.ctx, adminapi.ReactivateTenantInput{Tenant: id, Body: adminapi.Reason{Reason: *reason}})
		if err != nil {
			return err
		}
		return a.printResource(t)
	}},

	{[]string{"service-account", "list"}, "listServiceAccounts", func(a *app, args []string) error {
		fs := a.flags("service-account list")
		var l listFlags
		l.register(fs)
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		name := fs.String("name", "", "only the service account with this name")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListServiceAccounts(a.ctx, adminapi.ListServiceAccountsInput{Tenant: tid, State: adminapi.ServiceAccountState(l.state), Name: *name, Sort: adminapi.NamedSort(l.sort), Limit: l.limit, Cursor: cursor})
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

	{[]string{"service-account", "create"}, "createServiceAccount", func(a *app, args []string) error {
		fs := a.flags("service-account create")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		desc := fs.String("description", "", "description")
		var roles multi
		fs.Var(&roles, "role", "role (repeatable)")
		from := fs.String("valid-from", "", "start of validity (RFC 3339)")
		until := fs.String("valid-until", "", "end of validity (RFC 3339)")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		vf, err := parseTime("--valid-from", *from)
		if err != nil {
			return err
		}
		vu, err := parseTime("--valid-until", *until)
		if err != nil {
			return err
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		sa, err := a.client.CreateServiceAccount(a.ctx, adminapi.CreateServiceAccountInput{Tenant: tid, Body: adminapi.ServiceAccountCreate{
			Name: pos[0], Description: *desc, ValidFrom: vf, ValidUntil: vu, Roles: roles}})
		if err != nil {
			return err
		}
		return a.printResource(sa)
	}},

	{[]string{"service-account", "get"}, "getServiceAccount", func(a *app, args []string) error {
		fs := a.flags("service-account get")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		id, err := a.resolveServiceAccount(tid, pos[0])
		if err != nil {
			return err
		}
		sa, err := a.client.GetServiceAccount(a.ctx, adminapi.GetServiceAccountInput{Tenant: tid, ServiceAccount: id})
		if err != nil {
			return err
		}
		return a.printResource(sa)
	}},

	{[]string{"service-account", "update"}, "updateServiceAccount", func(a *app, args []string) error {
		fs := a.flags("service-account update")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		name := fs.String("name", "", "new name")
		desc := fs.String("description", "", "new description")
		var roles multi
		fs.Var(&roles, "role", "role (repeatable; replaces the set)")
		noRoles := fs.Bool("no-roles", false, "remove every role")
		from := fs.String("valid-from", "", "start of validity (RFC 3339)")
		until := fs.String("valid-until", "", "end of validity (RFC 3339)")
		noFrom := fs.Bool("no-valid-from", false, "remove the start of validity")
		noUntil := fs.Bool("no-valid-until", false, "remove the end of validity")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		body := adminapi.ServiceAccountUpdate{Name: nonEmpty(*name)}
		if given(fs, "description") {
			body.Description = adminapi.Some(*desc) // an empty description clears it
		}
		switch {
		case *noRoles:
			body.Roles = adminapi.Some([]string{})
		case len(roles) > 0:
			body.Roles = adminapi.Some([]string(roles))
		}
		switch {
		case *noFrom:
			body.ValidFrom = adminapi.Null[time.Time]()
		case *from != "":
			t, err := parseTime("--valid-from", *from)
			if err != nil {
				return err
			}
			body.ValidFrom = adminapi.Value(*t)
		}
		switch {
		case *noUntil:
			body.ValidUntil = adminapi.Null[time.Time]()
		case *until != "":
			t, err := parseTime("--valid-until", *until)
			if err != nil {
				return err
			}
			body.ValidUntil = adminapi.Value(*t)
		}
		switch {
		case *noRoles && len(roles) > 0:
			return usagef("--no-roles cannot be combined with --role")
		case *noFrom && *from != "":
			return usagef("--no-valid-from cannot be combined with --valid-from")
		case *noUntil && *until != "":
			return usagef("--no-valid-until cannot be combined with --valid-until")
		}
		if !body.Name.Set && !body.Description.Set && !body.Roles.Set && !body.ValidFrom.Set && !body.ValidUntil.Set {
			return usagef("service-account update needs a member to change")
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		id, err := a.resolveServiceAccount(tid, pos[0])
		if err != nil {
			return err
		}
		sa, err := a.client.UpdateServiceAccount(a.ctx, adminapi.UpdateServiceAccountInput{Tenant: tid, ServiceAccount: id, Body: body})
		if err != nil {
			return err
		}
		return a.printResource(sa)
	}},

	{[]string{"service-account", "disable"}, "disableServiceAccount", func(a *app, args []string) error {
		fs := a.flags("service-account disable")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		reason := fs.String("reason", "", "recorded in the audit log")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		id, err := a.resolveServiceAccount(tid, pos[0])
		if err != nil {
			return err
		}
		sa, err := a.client.DisableServiceAccount(a.ctx, adminapi.DisableServiceAccountInput{Tenant: tid, ServiceAccount: id, Body: adminapi.Reason{Reason: *reason}})
		if err != nil {
			return err
		}
		return a.printResource(sa)
	}},

	{[]string{"service-account", "enable"}, "enableServiceAccount", func(a *app, args []string) error {
		fs := a.flags("service-account enable")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		reason := fs.String("reason", "", "recorded in the audit log")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		id, err := a.resolveServiceAccount(tid, pos[0])
		if err != nil {
			return err
		}
		sa, err := a.client.EnableServiceAccount(a.ctx, adminapi.EnableServiceAccountInput{Tenant: tid, ServiceAccount: id, Body: adminapi.Reason{Reason: *reason}})
		if err != nil {
			return err
		}
		return a.printResource(sa)
	}},

	{[]string{"key", "list"}, "listVirtualKeys", func(a *app, args []string) error {
		fs := a.flags("key list")
		var l listFlags
		l.register(fs)
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		saFlag := fs.String("service-account", "", "only the keys of this service account (ID or name)")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		sa := ""
		if *saFlag != "" {
			if sa, err = a.resolveServiceAccount(tid, *saFlag); err != nil {
				return err
			}
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListVirtualKeys(a.ctx, adminapi.ListVirtualKeysInput{Tenant: tid, ServiceAccountID: sa, State: adminapi.KeyState(l.state), Sort: adminapi.Sort(l.sort), Limit: l.limit, Cursor: cursor})
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

	{[]string{"key", "create"}, "createVirtualKey", func(a *app, args []string) error {
		fs := a.flags("key create")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		saFlag := fs.String("service-account", "", "service account ID or name (required)")
		desc := fs.String("description", "", "description")
		var scopes multi
		fs.Var(&scopes, "scope", "permission the key is limited to (repeatable)")
		expires := fs.String("expires-at", "", "expiry (RFC 3339)")
		keyFile := fs.String("key-out", "", "write the key to this new file")
		credFlag := fs.String("credential", "", "pin the key to this credential (ID)")
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		if *saFlag == "" {
			return usagef("key create needs --service-account")
		}
		exp, err := parseTime("--expires-at", *expires)
		if err != nil {
			return err
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		sa, err := a.resolveServiceAccount(tid, *saFlag)
		if err != nil {
			return err
		}
		out, err := openKeyOut(*keyFile)
		if err != nil {
			return err
		}
		res, err := a.client.CreateVirtualKey(a.ctx, adminapi.CreateVirtualKeyInput{Tenant: tid, Body: adminapi.VirtualKeyCreate{
			ServiceAccountID: sa, Description: *desc, CredentialID: *credFlag, Scopes: scopes, ExpiresAt: exp}})
		if err != nil {
			out.abandon()
			return err
		}
		return a.printCreated(func() error { return a.printResource(res.Key) }, res, res.Secret, out, func() { res.Secret = "" })
	}},

	{[]string{"key", "get"}, "getVirtualKey", func(a *app, args []string) error {
		fs := a.flags("key get")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		k, err := a.client.GetVirtualKey(a.ctx, adminapi.GetVirtualKeyInput{Tenant: tid, Key: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(k)
	}},

	{[]string{"key", "revoke"}, "revokeVirtualKey", func(a *app, args []string) error {
		fs := a.flags("key revoke")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		reason := fs.String("reason", "", "recorded in the audit log")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		k, err := a.client.RevokeVirtualKey(a.ctx, adminapi.RevokeVirtualKeyInput{Tenant: tid, Key: pos[0], Body: adminapi.Reason{Reason: *reason}})
		if err != nil {
			return err
		}
		return a.printResource(k)
	}},

	{[]string{"platform-key", "list"}, "listPlatformKeys", func(a *app, args []string) error {
		fs := a.flags("platform-key list")
		var l listFlags
		l.register(fs)
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListPlatformKeys(a.ctx, adminapi.ListPlatformKeysInput{State: adminapi.KeyState(l.state), Sort: adminapi.Sort(l.sort), Limit: l.limit, Cursor: cursor})
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

	{[]string{"platform-key", "create"}, "createPlatformKey", func(a *app, args []string) error {
		fs := a.flags("platform-key create")
		desc := fs.String("description", "", "description")
		expires := fs.String("expires-at", "", "expiry (RFC 3339)")
		keyFile := fs.String("key-out", "", "write the key to this new file")
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		exp, err := parseTime("--expires-at", *expires)
		if err != nil {
			return err
		}
		if err := a.connect(); err != nil {
			return err
		}
		out, err := openKeyOut(*keyFile)
		if err != nil {
			return err
		}
		res, err := a.client.CreatePlatformKey(a.ctx, adminapi.CreatePlatformKeyInput{Body: adminapi.PlatformKeyCreate{Description: *desc, ExpiresAt: exp}})
		if err != nil {
			out.abandon()
			return err
		}
		return a.printCreated(func() error { return a.printResource(res.Key) }, res, res.Secret, out, func() { res.Secret = "" })
	}},

	{[]string{"platform-key", "get"}, "getPlatformKey", func(a *app, args []string) error {
		pos, err := a.begin(a.flags("platform-key get"), args, 1)
		if err != nil {
			return err
		}
		k, err := a.client.GetPlatformKey(a.ctx, adminapi.GetPlatformKeyInput{Key: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(k)
	}},

	{[]string{"platform-key", "revoke"}, "revokePlatformKey", func(a *app, args []string) error {
		fs := a.flags("platform-key revoke")
		reason := fs.String("reason", "", "recorded in the audit log")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		k, err := a.client.RevokePlatformKey(a.ctx, adminapi.RevokePlatformKeyInput{Key: pos[0], Body: adminapi.Reason{Reason: *reason}})
		if err != nil {
			return err
		}
		return a.printResource(k)
	}},
}

// nonEmpty is the optional member of an update for a flag: absent when the flag is empty.
func nonEmpty(s string) adminapi.Optional[string] {
	if s == "" {
		return adminapi.Optional[string]{}
	}
	return adminapi.Some(s)
}

// given reports whether the flag was set on the command line, even to the empty string.
func given(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) { found = found || f.Name == name })
	return found
}
