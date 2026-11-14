package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/pricing/genaiprices"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/migrations"
)

// maxPriceFileBytes is the largest dataset file import-prices reads.
const maxPriceFileBytes = 16 << 20

// readPriceFile reads a dataset file of at most maxPriceFileBytes.
func readPriceFile(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // the path is a flag of the operator
	if err != nil {
		return nil, fmt.Errorf("cannot read the file: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxPriceFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read the file: %w", err)
	}
	if len(b) > maxPriceFileBytes {
		return nil, fmt.Errorf("the file is larger than %d bytes", maxPriceFileBytes)
	}
	return b, nil
}

// runImportPrices is the host command import-prices (sprint 2.2, section 5.10): it imports a
// price dataset from a file or the embedded snapshot with the admin database user.
func runImportPrices(ctx context.Context, args []string, stdout, stderr io.Writer, environ []string) int {
	fs := flag.NewFlagSet("import-prices", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configFile := fs.String("config", "", "configuration file")
	file := fs.String("file", "", "price dataset file (genai-prices v2 format)")
	embedded := fs.Bool("embedded", false, "import the snapshot embedded in the program")
	dryRun := fs.Bool("dry-run", false, "report what would change and write nothing")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	fileGiven := false
	fs.Visit(func(f *flag.Flag) { fileGiven = fileGiven || f.Name == "file" })
	usageErr := func(msg string) int {
		fmt.Fprintf(stderr, "pandokos: import-prices: %s\n", msg)
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch {
	case fs.NArg() > 0:
		return usageErr("takes no argument")
	case fileGiven && *file == "":
		return usageErr("--file needs a file name")
	case fileGiven == *embedded:
		return usageErr("give exactly one of --file and --embedded")
	}

	path := *configFile
	if path == "" {
		path = config.FileFromEnviron(environ)
	}
	cfg, warnings, err := config.Load(config.Source{File: path, Environ: environ, Purpose: config.PurposeInit})
	if err != nil {
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			for _, p := range ve.Problems {
				fmt.Fprintf(stderr, "error: %s: %s\n", p.Key, p.Message)
			}
		} else {
			fmt.Fprintf(stderr, "error: %v\n", err)
		}
		return 1
	}
	for _, w := range warnings {
		// Only what concerns the database connection: the listeners and the provider URL are
		// not used by this command, and their warnings would be noise.
		if strings.HasPrefix(w.Key, "database.") {
			fmt.Fprintf(stderr, "warning: %s: %s\n", w.Key, w.Message)
		}
	}

	src := catalog.Source{Kind: catalog.SourceEmbedded, Data: genaiprices.Snapshot()}
	if fileGiven {
		data, err := readPriceFile(*file)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		src = catalog.Source{Kind: catalog.SourceFile, Ref: *file, Data: data}
	}

	ms, err := migrations.Load()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := store.Open(store.Options{Conn: connOptions(cfg, cfg.Database.Admin.User, cfg.Database.Admin.Password), Role: store.RoleAdmin, MaxConns: 2, Migrations: ms})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	cctx, cancel := context.WithTimeout(sigCtx, 30*time.Second)
	err = db.Check(cctx)
	cancel()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	rep, err := catalog.Import(sigCtx, db, src, catalog.ImportOptions{
		Actor:  audit.Actor{Type: audit.ActorHost, Name: catalog.ActorImportPrices},
		DryRun: *dryRun,
	})
	if err != nil {
		if errors.Is(err, catalog.ErrDatasetInvalid) {
			fmt.Fprintln(stderr, "error: the file is not a price dataset this version can read")
		} else {
			fmt.Fprintf(stderr, "error: the import failed (%s)\n", catalog.ErrorClass(err))
		}
		return 1
	}
	fmt.Fprintf(stdout, "models_seen: %d\ncreated: %d\nunchanged: %d\noverridden: %d\nunmatched: %d\nskipped: %d\n",
		rep.ModelsSeen, rep.Created, rep.Unchanged, rep.Overridden, rep.Unmatched, rep.Skipped)
	for _, it := range rep.Items {
		line := fmt.Sprintf("%s/%s: %s", it.Provider, it.Model, it.Outcome)
		if it.DatasetModel != "" {
			line += " (dataset model " + it.DatasetModel + ")"
		}
		for _, is := range it.Issues {
			line += "; " + is.Reason
			if is.Detail != "" {
				line += " " + is.Detail
			}
		}
		fmt.Fprintln(stdout, line)
	}
	for _, is := range rep.DatasetIssues {
		fmt.Fprintf(stdout, "dataset: %s %s\n", is.Reason, is.Detail)
	}
	return 0
}
