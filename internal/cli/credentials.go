package cli

import (
	"flag"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/secret"
)

func init() { commands = append(commands, credentialCommands...) }

// toAny turns the items of a page into the shape that paged wants.
func toAny[T any](in []T) []any {
	items := make([]any, len(in))
	for i, v := range in {
		items[i] = v
	}
	return items
}

// readSecretFile reads the value of a credential from the file named by --secret-file. It is the
// only way a command receives a provider secret: no flag, variable or standard input carries one.
// The messages quote nothing of the file.
func readSecretFile(path string) (secret.Secret, error) {
	if path == "" {
		return secret.Secret{}, usagef("--secret-file is required: the secret is read from a file and from nothing else")
	}
	s, err := secret.ReadFile(path)
	if err != nil {
		return secret.Secret{}, err
	}
	return s, nil
}

// credentialTarget parses the flags and the credential ID of a command on one credential, and
// resolves the tenant.
func (a *app) credentialTarget(fs *flag.FlagSet, args []string, tenantFlag *string) (tenantID, id string, err error) {
	pos, err := parse(fs, args, 1)
	if err != nil {
		return "", "", err
	}
	if err := a.connect(); err != nil {
		return "", "", err
	}
	tid, err := a.resolveTenant(*tenantFlag)
	if err != nil {
		return "", "", err
	}
	return tid, pos[0], nil
}

var credentialCommands = []command{
	{[]string{"credential", "list"}, "listCredentials", func(a *app, args []string) error {
		fs := a.flags("credential list")
		var l listFlags
		l.register(fs)
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		provider := fs.String("provider", "", "only credentials of this provider")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListCredentials(a.ctx, adminapi.ListCredentialsInput{Tenant: tid, State: adminapi.CredentialState(l.state), Provider: *provider, Sort: adminapi.Sort(l.sort), Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, func(items []any) any { return map[string]any{"items": items} })
	}},

	{[]string{"credential", "create"}, "createCredential", func(a *app, args []string) error {
		fs := a.flags("credential create")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		provider := fs.String("provider", "", "provider identifier (required)")
		secretFile := fs.String("secret-file", "", "file with the provider API key (required)")
		desc := fs.String("description", "", "description")
		expires := fs.String("expires-at", "", "expiry (RFC 3339)")
		validate := fs.Bool("validate", false, "present the secret to the provider before storing it")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		if *provider == "" {
			return usagef("credential create needs --provider")
		}
		exp, err := parseTime("--expires-at", *expires)
		if err != nil {
			return err
		}
		sec, err := readSecretFile(*secretFile)
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
		c, err := a.client.CreateCredential(a.ctx, adminapi.CreateCredentialInput{Tenant: tid, Body: adminapi.CredentialCreate{
			Provider: *provider, Label: pos[0], Secret: sec.Reveal(), Description: *desc, ExpiresAt: exp, ValidateAtWrite: *validate}}) // the request body is the one place where the secret is revealed
		if err != nil {
			return err
		}
		return a.printResource(c)
	}},

	{[]string{"credential", "get"}, "getCredential", func(a *app, args []string) error {
		fs := a.flags("credential get")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		tid, id, err := a.credentialTarget(fs, args, tenantFlag)
		if err != nil {
			return err
		}
		c, err := a.client.GetCredential(a.ctx, adminapi.GetCredentialInput{Tenant: tid, Credential: id})
		if err != nil {
			return err
		}
		return a.printResource(c)
	}},

	{[]string{"credential", "update"}, "updateCredential", func(a *app, args []string) error {
		fs := a.flags("credential update")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		label := fs.String("label", "", "new label")
		desc := fs.String("description", "", "new description")
		expires := fs.String("expires-at", "", "new expiry (RFC 3339)")
		noExpires := fs.Bool("no-expires-at", false, "remove the expiry")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		body := adminapi.CredentialUpdate{Label: nonEmpty(*label)}
		if given(fs, "description") {
			body.Description = adminapi.Some(*desc) // an empty description clears it
		}
		switch {
		case *noExpires && *expires != "":
			return usagef("--no-expires-at cannot be combined with --expires-at")
		case *noExpires:
			body.ExpiresAt = adminapi.Null[time.Time]()
		case *expires != "":
			t, err := parseTime("--expires-at", *expires)
			if err != nil {
				return err
			}
			body.ExpiresAt = adminapi.Value(*t)
		}
		if !body.Label.Set && !body.Description.Set && !body.ExpiresAt.Set {
			return usagef("credential update needs a member to change")
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		c, err := a.client.UpdateCredential(a.ctx, adminapi.UpdateCredentialInput{Tenant: tid, Credential: pos[0], Body: body})
		if err != nil {
			return err
		}
		return a.printResource(c)
	}},

	{[]string{"credential", "replace"}, "replaceCredentialSecret", func(a *app, args []string) error {
		fs := a.flags("credential replace")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		secretFile := fs.String("secret-file", "", "file with the new provider API key (required)")
		expires := fs.String("expires-at", "", "expiry (RFC 3339)")
		validate := fs.Bool("validate", false, "present the secret to the provider before storing it")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		exp, err := parseTime("--expires-at", *expires)
		if err != nil {
			return err
		}
		sec, err := readSecretFile(*secretFile)
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
		c, err := a.client.ReplaceCredentialSecret(a.ctx, adminapi.ReplaceCredentialSecretInput{Tenant: tid, Credential: pos[0], Body: adminapi.CredentialSecret{
			Secret: sec.Reveal(), ExpiresAt: exp, ValidateAtWrite: *validate}}) // the request body is the one place where the secret is revealed
		if err != nil {
			return err
		}
		return a.printResource(c)
	}},

	{[]string{"credential", "disable"}, "disableCredential", func(a *app, args []string) error {
		fs := a.flags("credential disable")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		reason := fs.String("reason", "", "recorded in the audit log")
		tid, id, err := a.credentialTarget(fs, args, tenantFlag)
		if err != nil {
			return err
		}
		c, err := a.client.DisableCredential(a.ctx, adminapi.DisableCredentialInput{Tenant: tid, Credential: id, Body: adminapi.Reason{Reason: *reason}})
		if err != nil {
			return err
		}
		return a.printResource(c)
	}},

	{[]string{"credential", "enable"}, "enableCredential", func(a *app, args []string) error {
		fs := a.flags("credential enable")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		reason := fs.String("reason", "", "recorded in the audit log")
		tid, id, err := a.credentialTarget(fs, args, tenantFlag)
		if err != nil {
			return err
		}
		c, err := a.client.EnableCredential(a.ctx, adminapi.EnableCredentialInput{Tenant: tid, Credential: id, Body: adminapi.Reason{Reason: *reason}})
		if err != nil {
			return err
		}
		return a.printResource(c)
	}},

	{[]string{"credential", "set-default"}, "setDefaultCredential", func(a *app, args []string) error {
		fs := a.flags("credential set-default")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		reason := fs.String("reason", "", "recorded in the audit log")
		tid, id, err := a.credentialTarget(fs, args, tenantFlag)
		if err != nil {
			return err
		}
		c, err := a.client.SetDefaultCredential(a.ctx, adminapi.SetDefaultCredentialInput{Tenant: tid, Credential: id, Body: adminapi.Reason{Reason: *reason}})
		if err != nil {
			return err
		}
		return a.printResource(c)
	}},

	{[]string{"credential", "revoke"}, "revokeCredential", func(a *app, args []string) error {
		fs := a.flags("credential revoke")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		reason := fs.String("reason", "", "recorded in the audit log")
		tid, id, err := a.credentialTarget(fs, args, tenantFlag)
		if err != nil {
			return err
		}
		c, err := a.client.RevokeCredential(a.ctx, adminapi.RevokeCredentialInput{Tenant: tid, Credential: id, Body: adminapi.Reason{Reason: *reason}})
		if err != nil {
			return err
		}
		return a.printResource(c)
	}},

	{[]string{"credential", "delete"}, "deleteCredential", func(a *app, args []string) error {
		fs := a.flags("credential delete")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		tid, id, err := a.credentialTarget(fs, args, tenantFlag)
		if err != nil {
			return err
		}
		c, err := a.client.DeleteCredential(a.ctx, adminapi.DeleteCredentialInput{Tenant: tid, Credential: id})
		if err != nil {
			return err
		}
		return a.printResource(c)
	}},

	{[]string{"tenant-credential", "list"}, "listTenantCredentials", func(a *app, args []string) error {
		fs := a.flags("tenant-credential list")
		var l listFlags
		l.register(fs)
		tenantFlag := fs.String("tenant", "", "tenant ID or name (required with a platform key)")
		provider := fs.String("provider", "", "only credentials of this provider")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListTenantCredentials(a.ctx, adminapi.ListTenantCredentialsInput{Tenant: tid, State: adminapi.CredentialState(l.state), Provider: *provider, Sort: adminapi.Sort(l.sort), Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, func(items []any) any { return map[string]any{"items": items} })
	}},

	{[]string{"tenant-credential", "get"}, "getTenantCredential", func(a *app, args []string) error {
		fs := a.flags("tenant-credential get")
		tenantFlag := fs.String("tenant", "", "tenant ID or name (required with a platform key)")
		tid, id, err := a.credentialTarget(fs, args, tenantFlag)
		if err != nil {
			return err
		}
		c, err := a.client.GetTenantCredential(a.ctx, adminapi.GetTenantCredentialInput{Tenant: tid, Credential: id})
		if err != nil {
			return err
		}
		return a.printResource(c)
	}},

	{[]string{"tenant-credential", "disable"}, "disableTenantCredential", func(a *app, args []string) error {
		fs := a.flags("tenant-credential disable")
		tenantFlag := fs.String("tenant", "", "tenant ID or name (required with a platform key)")
		reason := fs.String("reason", "", "recorded in the audit log of the tenant (required)")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		if *reason == "" {
			return usagef("tenant-credential disable needs --reason")
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		c, err := a.client.DisableTenantCredential(a.ctx, adminapi.DisableTenantCredentialInput{Tenant: tid, Credential: pos[0], Body: adminapi.RequiredReason{Reason: *reason}})
		if err != nil {
			return err
		}
		return a.printResource(c)
	}},

	{[]string{"tenant-credential", "revoke"}, "revokeTenantCredential", func(a *app, args []string) error {
		fs := a.flags("tenant-credential revoke")
		tenantFlag := fs.String("tenant", "", "tenant ID or name (required with a platform key)")
		reason := fs.String("reason", "", "recorded in the audit log of the tenant (required)")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		if *reason == "" {
			return usagef("tenant-credential revoke needs --reason")
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		c, err := a.client.RevokeTenantCredential(a.ctx, adminapi.RevokeTenantCredentialInput{Tenant: tid, Credential: pos[0], Body: adminapi.RequiredReason{Reason: *reason}})
		if err != nil {
			return err
		}
		return a.printResource(c)
	}},
}
