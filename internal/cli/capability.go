package cli

import (
	"github.com/pandokos/pandokos/internal/adminapi"
)

func init() { commands = append(commands, capabilityCommands...) }

// capabilityCommands are the commands that read what a tenant can call and what it has used.
var capabilityCommands = []command{
	{[]string{"capability", "check"}, "getCapability", func(a *app, args []string) error {
		fs := a.flags("capability check")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		provider := fs.String("provider", "", "the provider (required)")
		model := fs.String("model", "", "the public model name (required)")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		if *provider == "" || *model == "" {
			return usagef("capability check needs --provider and --model")
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		c, err := a.client.GetCapability(a.ctx, adminapi.GetCapabilityInput{Tenant: tid, Provider: *provider, Model: *model})
		if err != nil {
			return err
		}
		return a.printResource(c)
	}},

	{[]string{"usage", "list"}, "listUsageRecords", func(a *app, args []string) error {
		fs := a.flags("usage list")
		var l listFlags
		l.registerPaging(fs)
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		from := fs.String("from", "", "start of the window (RFC 3339; default 24 hours before --to)")
		to := fs.String("to", "", "end of the window, excluded (RFC 3339; default now)")
		provider := fs.String("provider", "", "only records of this provider")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		return a.paged(&l, func(cursor string) ([]any, string, error) {
			out, err := a.client.ListUsageRecords(a.ctx, adminapi.ListUsageRecordsInput{
				Tenant: tid, From: *from, To: *to, Provider: *provider, Limit: l.limit, Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			return toAny(out.Items), out.NextCursor, nil
		}, itemsOf)
	}},
}
