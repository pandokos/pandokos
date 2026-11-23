package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/pandokos/pandokos/internal/declare"
)

func init() { composites = append(composites, declareCommands...) }

// declareCommands are export and apply: declarative configuration (section 5.12).
var declareCommands = []command{
	{[]string{"export"}, "", func(a *app, args []string) error {
		fs := a.flags("export")
		platform := fs.Bool("platform", false, "export the platform's catalogue and ceilings")
		tenantFlag := fs.String("tenant", "", "export this tenant (ID or name)")
		out := fs.String("out", "", "write to this file (through FILE.partial) instead of standard output")
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		if *platform == (*tenantFlag != "") {
			return usagef("export needs exactly one of --platform and --tenant")
		}
		if err := a.connect(); err != nil {
			return err
		}
		target, err := a.declareTarget(*platform, *tenantFlag)
		if err != nil {
			return err
		}
		doc, err := declare.Export(a.ctx, a.client, target)
		if err != nil {
			return err
		}
		b, err := doc.YAML()
		if err != nil {
			return err
		}
		return a.writeExport(*out, func(w io.Writer) error {
			_, err := w.Write(b)
			return err
		})
	}},

	{[]string{"apply"}, "", func(a *app, args []string) error {
		fs := a.flags("apply")
		file := fs.String("file", "", "the document (required)")
		platform := fs.Bool("platform", false, "apply to the platform")
		tenantFlag := fs.String("tenant", "", "apply to this tenant (ID or name)")
		dry := fs.Bool("dry-run", false, "print the plan and change nothing")
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		if *file == "" {
			return usagef("apply needs --file")
		}
		if *platform == (*tenantFlag != "") {
			return usagef("apply needs exactly one of --platform and --tenant")
		}
		data, err := readDocument(*file)
		if err != nil {
			return err
		}
		doc, err := declare.Parse(data)
		if err == nil {
			err = doc.Validate(declare.Target{Platform: *platform, Tenant: *tenantFlag})
		}
		var inv *declare.InvalidError
		if errors.As(err, &inv) {
			return usagef("%s", inv.Error())
		}
		if err != nil {
			return err
		}
		if err := a.connect(); err != nil {
			return err
		}
		target, err := a.declareTarget(*platform, *tenantFlag)
		if err != nil {
			return err
		}
		plan, err := declare.Diff(a.ctx, a.client, target, doc)
		if errors.As(err, &inv) {
			return usagef("%s", inv.Error())
		}
		if err != nil {
			return err
		}
		var res declare.Result
		if *dry {
			res = plan.Preview()
		} else {
			res, err = plan.Apply(a.ctx, a.client)
		}
		for _, l := range res.Lines {
			fmt.Fprintln(a.stdout, l)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "%d changes, %d conflicts, %d left alone\n", res.Changes, res.Conflicts, res.LeftAlone)
		return nil
	}},
}

func (a *app) declareTarget(platform bool, tenant string) (declare.Target, error) {
	if platform {
		return declare.Target{Platform: true}, nil
	}
	id, err := a.resolveTenant(tenant)
	if err != nil {
		return declare.Target{}, err
	}
	return declare.Target{Tenant: id}, nil
}

// readDocument reads the file of a document, refusing one over the largest size.
func readDocument(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // the path is a flag of the operator
	if err != nil {
		return nil, fmt.Errorf("cannot read the document: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, declare.MaxDocument+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read the document: %w", err)
	}
	return data, nil
}
