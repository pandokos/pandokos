package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pandokos/pandokos/internal/buildinfo"
	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/migrations"
)

func runMigrate(ctx context.Context, args []string, stdout, stderr io.Writer, environ []string) int {
	status := len(args) > 0 && args[0] == "status"
	if status {
		args = args[1:]
	}
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("config", "", "configuration file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "pandokos: migrate takes no argument other than status")
		fmt.Fprint(stderr, usage)
		return 2
	}
	path := *file
	if path == "" {
		path = config.FileFromEnviron(environ)
	}

	cfg, warnings, err := config.Load(config.Source{File: path, Environ: environ, Purpose: config.PurposeMigrate})
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
		fmt.Fprintf(stderr, "warning: %s: %s\n", w.Key, w.Message)
	}
	ms, err := migrations.Load()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	opts := connOptions(cfg, cfg.Database.Owner.User, cfg.Database.Owner.Password)

	if status {
		st, err := store.MigrationStatus(sigCtx, opts, ms)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		applied := map[int]time.Time{}
		for _, a := range st.Applied {
			applied[a.Version] = a.AppliedAt
		}
		for _, m := range ms {
			if t, ok := applied[m.Version]; ok {
				fmt.Fprintf(stdout, "%s  applied %s\n", m.ID(), t.UTC().Format(time.RFC3339))
			} else {
				fmt.Fprintf(stdout, "%s  pending\n", m.ID())
			}
		}
		fmt.Fprintf(stdout, "schema is at version %d; this binary needs version %d\n", st.Version, st.Want)
		if len(st.Pending) > 0 {
			return 1
		}
		return 0
	}

	res, err := store.Migrate(sigCtx, opts, ms, buildinfo.Version)
	for _, m := range res.Applied {
		fmt.Fprintf(stdout, "applied %s\n", m.ID())
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "schema is at version %d\n", res.Version)
	return 0
}
