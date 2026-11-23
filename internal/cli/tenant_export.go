package cli

import (
	"fmt"

	"github.com/pandokos/pandokos/internal/buildinfo"
	"github.com/pandokos/pandokos/internal/tenantexport"
)

// exportSchema is pandokos tenant-export schema: the embedded JSON Schema, from no server.
func (a *app) exportSchema(args []string) error {
	fs := a.flags("tenant-export schema")
	if _, err := parse(fs, args, 0); err != nil {
		return err
	}
	_, err := a.stdout.Write(tenantexport.Schema)
	return err
}

// tenantExport is pandokos tenant-export (section 5.11).
func (a *app) tenantExport(args []string) error {
	fs := a.flags("tenant-export")
	tenant := fs.String("tenant", "", "ID of the tenant (required)")
	out := fs.String("out", "", "the new directory to write the export to (required)")
	usageFrom := fs.String("usage-from", "", "start of the usage ledger, RFC 3339 (default: 60 months ago)")
	if _, err := parse(fs, args, 0); err != nil {
		return err
	}
	if !reUUID.MatchString(*tenant) {
		return usagef("tenant-export needs --tenant with the tenant ID")
	}
	if *out == "" {
		return usagef("tenant-export needs --out, a directory that does not exist")
	}
	from, err := parseTime("--usage-from", *usageFrom)
	if err != nil {
		return err
	}
	if err := a.connect(); err != nil {
		return err
	}
	opts := tenantexport.Options{PandokosVersion: buildinfo.Version}
	if from != nil {
		opts.UsageFrom = *from
	}
	m, err := tenantexport.Write(a.ctx, a.client, *tenant, *out, opts)
	if err != nil {
		return err
	}
	if a.output == "json" {
		return a.printJSON(m)
	}
	var records int64
	for _, f := range m.Files {
		records += f.Records
	}
	fmt.Fprintf(a.stdout, "export of %s written to %s: %d files, %d records, audit head seq %d\n", m.TenantID, *out, len(m.Files), records, m.AuditHead.Seq)
	return nil
}
