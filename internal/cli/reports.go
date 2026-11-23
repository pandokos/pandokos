package cli

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
)

func init() { commands = append(commands, reportCommands...) }

var (
	reMonth   = regexp.MustCompile(`^(\d{4})-(0[1-9]|1[0-2])$`)
	reGroupBy = regexp.MustCompile(`^(provider|model|caller|key)(,(provider|model|caller|key)){0,3}$`)
)

// maxExportWindow is the longest window the server answers in one export.
const maxExportWindow = 31 * 24 * time.Hour

// reportCommands are the commands of usage reports, usage export, the usage of a limit rule and the
// metadata keys.
var reportCommands = []command{
	{[]string{"usage", "report"}, "getUsageReport", func(a *app, args []string) error {
		fs := a.flags("usage report")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		from := fs.String("from", "", "start: YYYY-MM-DD or an RFC 3339 instant")
		to := fs.String("to", "", "end, excluded: YYYY-MM-DD or an RFC 3339 instant")
		month := fs.String("month", "", "the month YYYY-MM (instead of --from and --to)")
		period := fs.String("period", "", "day, month or total")
		groupBy := fs.String("group-by", "", "comma-separated subset of provider, model, caller, key")
		provider := fs.String("provider", "", "only records of this provider")
		model := fs.String("model", "", "only records of this model name")
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		if *month != "" {
			m := reMonth.FindStringSubmatch(*month)
			if m == nil {
				return usagef("--month must be YYYY-MM")
			}
			if *from != "" || *to != "" {
				return usagef("--month cannot be combined with --from or --to")
			}
			first, err := time.Parse("2006-01", *month)
			if err != nil {
				return usagef("--month must be YYYY-MM")
			}
			*from = first.Format("2006-01-02")
			*to = first.AddDate(0, 1, 0).Format("2006-01-02")
		}
		if *period != "" && !adminapi.ReportPeriod(*period).Valid() {
			return usagef("--period must be day, month or total")
		}
		if *groupBy != "" && !reGroupBy.MatchString(*groupBy) {
			return usagef("--group-by must be a comma-separated subset of provider, model, caller and key")
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		rep, err := a.client.GetUsageReport(a.ctx, adminapi.GetUsageReportInput{Tenant: tid, From: *from, To: *to,
			Period: adminapi.ReportPeriod(*period), GroupBy: *groupBy, Provider: *provider, Model: *model})
		if err != nil {
			return err
		}
		if a.output == "json" {
			return a.printJSON(rep)
		}
		fmt.Fprintf(a.stdout, "from: %s\nto: %s\nperiod: %s\ntime_zone: %s\n", rep.From.Format(time.RFC3339), rep.To.Format(time.RFC3339), rep.Period, rep.TimeZone)
		a.printTable(toAny(rep.Rows))
		return nil
	}},

	{[]string{"usage", "export"}, "exportUsageRecords", func(a *app, args []string) error {
		fs := a.flags("usage export")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		from := fs.String("from", "", "start of the window (RFC 3339)")
		to := fs.String("to", "", "end of the window, excluded (RFC 3339)")
		provider := fs.String("provider", "", "only records of this provider")
		format := fs.String("format", "", "ndjson or csv")
		out := fs.String("out", "", "write to this file (through FILE.partial) instead of standard output")
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		if *format != "" && !adminapi.ExportFormat(*format).Valid() {
			return usagef("--format must be ndjson or csv")
		}
		fromT, err := parseTime("--from", *from)
		if err != nil {
			return err
		}
		toT, err := parseTime("--to", *to)
		if err != nil {
			return err
		}
		if fromT != nil && toT != nil && !toT.After(*fromT) {
			return usagef("--from must be before --to")
		}
		if err := a.connect(); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		// A window over 31 days is split into consecutive requests, each a window the server answers.
		type window struct{ from, to string }
		windows := []window{{*from, *to}}
		if fromT != nil && toT != nil && toT.Sub(*fromT) > maxExportWindow {
			windows = nil
			for s := *fromT; s.Before(*toT); {
				e := s.Add(maxExportWindow)
				if e.After(*toT) {
					e = *toT
				}
				windows = append(windows, window{s.Format(time.RFC3339Nano), e.Format(time.RFC3339Nano)})
				s = e
			}
		}
		csv := adminapi.ExportFormat(*format) == adminapi.ExportFormatCsv
		return a.writeExport(*out, func(w io.Writer) error {
			for i, win := range windows {
				body, err := a.client.ExportUsageRecords(a.ctx, adminapi.ExportUsageRecordsInput{Tenant: tid, From: win.from, To: win.to,
					Provider: *provider, Format: adminapi.ExportFormat(*format)})
				if err != nil {
					return err
				}
				var src io.Reader = body.Body
				if csv && i > 0 {
					// The header of the file is written once.
					br := bufio.NewReader(body.Body)
					if _, err := br.ReadBytes('\n'); err != nil && !errors.Is(err, io.EOF) {
						_ = body.Body.Close()
						return err
					}
					src = br
				}
				_, err = io.Copy(w, src)
				_ = body.Body.Close()
				if err != nil {
					return err
				}
			}
			return nil
		})
	}},

	{[]string{"limit", "usage"}, "getLimitUsage", func(a *app, args []string) error {
		fs := a.flags("limit usage")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		pos, err := a.begin(fs, args, 1)
		if err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		u, err := a.client.GetLimitUsage(a.ctx, adminapi.GetLimitUsageInput{Tenant: tid, Rule: pos[0]})
		if err != nil {
			return err
		}
		return a.printResource(u)
	}},

	{[]string{"metadata-keys", "get"}, "getMetadataKeys", func(a *app, args []string) error {
		fs := a.flags("metadata-keys get")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		k, err := a.client.GetMetadataKeys(a.ctx, adminapi.GetMetadataKeysInput{Tenant: tid})
		if err != nil {
			return err
		}
		return a.printResource(k)
	}},

	{[]string{"metadata-keys", "set"}, "setMetadataKeys", func(a *app, args []string) error {
		fs := a.flags("metadata-keys set")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		pos, err := parseAny(fs, args)
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
		keys := append([]string{}, pos...)
		k, err := a.client.SetMetadataKeys(a.ctx, adminapi.SetMetadataKeysInput{Tenant: tid, Body: adminapi.MetadataKeysSet{Keys: keys}})
		if err != nil {
			return err
		}
		return a.printResource(k)
	}},

	{[]string{"metadata-keys", "clear"}, "clearMetadataKeys", func(a *app, args []string) error {
		fs := a.flags("metadata-keys clear")
		tenantFlag := fs.String("tenant", "", "tenant ID or name")
		if _, err := a.begin(fs, args, 0); err != nil {
			return err
		}
		tid, err := a.resolveTenant(*tenantFlag)
		if err != nil {
			return err
		}
		k, err := a.client.ClearMetadataKeys(a.ctx, adminapi.ClearMetadataKeysInput{Tenant: tid})
		if err != nil {
			return err
		}
		return a.printResource(k)
	}},
}

// parseAny is parse for a command that takes any number of positional arguments.
func parseAny(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, &usageError{err.Error()}
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// writeExport runs write against standard output, or against FILE.partial that is renamed to FILE
// after the last byte; a failure removes the partial file.
func (a *app) writeExport(out string, write func(w io.Writer) error) error {
	if out == "" {
		return write(a.stdout)
	}
	tmp := out + ".partial"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // the path is a flag of the operator
	if err != nil {
		return fmt.Errorf("cannot create the output file: %w", err)
	}
	err = write(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Clean(out)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// readLines calls each with the lines of r, without their newline; a last line without one is a
// truncated export.
func readLines(r io.Reader, each func(line []byte) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 && (err != nil || line[len(line)-1] != '\n') {
			return errTruncated
		}
		if len(line) > 0 {
			if err := each(bytes.TrimRight(line, "\r\n")); err != nil {
				return err
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

var errTruncated = errors.New("truncated last line")
