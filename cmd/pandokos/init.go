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

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/migrations"
)

func runInit(ctx context.Context, args []string, stdout, stderr io.Writer, environ []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("config", "", "configuration file")
	ttl := fs.Duration("ttl", 24*time.Hour, "lifetime of the key, from 5m to 720h")
	description := fs.String("description", "bootstrap", "description of the key")
	keyOut := fs.String("key-out", "", "write the key to this new file (mode 0600) instead of printing it")
	recoverKey := fs.Bool("recover", false, "issue the key although a usable platform key exists (every platform key is lost)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "pandokos: init takes no argument")
		fmt.Fprint(stderr, usage)
		return 2
	}
	if *ttl < 5*time.Minute || *ttl > 720*time.Hour {
		fmt.Fprintln(stderr, "pandokos: --ttl must be between 5m and 720h")
		return 2
	}
	path := *file
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
		fmt.Fprintf(stderr, "warning: %s: %s\n", w.Key, w.Message)
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

	written := false
	res, err := admin.Bootstrap(sigCtx, db, admin.BootstrapOptions{
		TTL: *ttl, Description: *description, Recover: *recoverKey,
		BeforeCommit: func(key secret.Secret, _ string, _ time.Time) error {
			if *keyOut == "" {
				return nil
			}
			f, err := os.OpenFile(*keyOut, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the path is a flag of the operator
			if err != nil {
				return fmt.Errorf("cannot create the key file: %w", err)
			}
			written = true
			_, werr := f.WriteString(key.Reveal() + "\n")
			if cerr := f.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				return fmt.Errorf("cannot write the key file: %w", werr)
			}
			return nil
		},
	})
	if err != nil {
		if written {
			_ = os.Remove(*keyOut)
		}
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "bootstrap platform key created")
	fmt.Fprintf(stdout, "key id:  %s\n", res.KeyID)
	fmt.Fprintf(stdout, "expires: %s\n", res.ExpiresAt.UTC().Format(time.RFC3339))
	if *keyOut != "" {
		fmt.Fprintf(stdout, "key written to %s\n", *keyOut)
	} else {
		fmt.Fprintf(stdout, "key:     %s\n", res.Key.Reveal()) // the key is shown once, here
	}
	fmt.Fprintln(stderr, `warning: this is a bootstrap key: create a platform key with "pandokos platform-key create", then revoke this one`)
	return 0
}
