package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

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
		secretsOut := fs.String("secrets-out", "", "a private directory for the signing secrets of the event destinations apply creates (without it none is created)")
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
		var sink *secretSink
		var opts []declare.Options
		if *secretsOut != "" {
			if sink, err = newSecretSink(*secretsOut); err != nil {
				return err
			}
			opts = append(opts, declare.Options{SecretSink: sink.write})
		}
		if err := a.connect(); err != nil {
			return err
		}
		target, err := a.declareTarget(*platform, *tenantFlag)
		if err != nil {
			return err
		}
		plan, err := declare.Diff(a.ctx, a.client, target, doc, opts...)
		if errors.As(err, &inv) {
			return usagef("%s", inv.Error())
		}
		if err != nil {
			return err
		}
		if sink != nil {
			// Before anything is created: a secret that cannot be written must not be lost.
			if err := sink.free(plan.NewDestinations()); err != nil {
				return err
			}
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
		if sink != nil {
			fmt.Fprintf(a.stdout, "%d changes, %d conflicts, %d left alone; %d secret files written to %s\n", res.Changes, res.Conflicts, res.LeftAlone, sink.written, sink.dir)
			return nil
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

// secretSink writes the signing secrets of the event destinations that apply creates, one file
// each, in a private directory. A secret is shown once by the API, so a file is never overwritten
// and never left half written, and the secret goes to no other place.
type secretSink struct {
	dir     string
	written int
}

// newSecretSink checks the directory before any call: it exists, it is a directory (not a link to
// one) and no one but its owner can enter it.
func newSecretSink(dir string) (*secretSink, error) {
	info, err := os.Lstat(dir)
	switch {
	case err != nil:
		return nil, usagef("--secrets-out: the directory cannot be used: %s", errText(err))
	case !info.IsDir():
		return nil, usagef("--secrets-out: %s is not a directory (a link to one is not accepted either)", dir)
	case info.Mode().Perm()&0o077 != 0:
		return nil, usagef("--secrets-out: the directory %s must be accessible by its owner only (chmod 700)", dir)
	}
	return &secretSink{dir: dir}, nil
}

func errText(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

func (s *secretSink) path(name string) (string, error) {
	file := "event-destination-" + name
	if name == "" || file != filepath.Base(file) || strings.ContainsAny(name, "/\\\x00") {
		return "", fmt.Errorf("the name of the destination cannot be a file name")
	}
	return filepath.Join(s.dir, file), nil
}

// free fails, as a usage error, when a file for a destination that will be created exists.
func (s *secretSink) free(destinations []string) error {
	for _, d := range destinations {
		p, err := s.path(d)
		if err != nil {
			return usagef("--secrets-out: %s", err)
		}
		if _, err := os.Lstat(p); err == nil {
			return usagef("--secrets-out: %s exists: apply never overwrites a secret file; move it away", p)
		} else if !errors.Is(err, os.ErrNotExist) {
			return usagef("--secrets-out: %s cannot be checked: %s", p, errText(err))
		}
	}
	return nil
}

// write is the declare.Options sink: name is "event-destination-<destination>".
func (s *secretSink) write(name string, secret []byte) (err error) {
	p, err := s.path(strings.TrimPrefix(name, "event-destination-"))
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the directory is the operator's flag; the file name is checked above
	if err != nil {
		return fmt.Errorf("cannot create %s: %s", p, errText(err))
	}
	line := append(slices.Clone(secret), '\n')
	defer clear(line)
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("cannot write %s: %s", p, errText(cerr))
		}
		if err != nil {
			_ = os.Remove(p) // never leave a file that holds half a secret
		}
	}()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("cannot write %s: %s", p, errText(err))
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("cannot write %s: %s", p, errText(err))
	}
	s.written++
	return nil
}
