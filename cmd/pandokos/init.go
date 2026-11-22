package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"github.com/pandokos/pandokos/internal/account"
	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/password"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/migrations"
)

// reUsername is the form of a username (the check of the table local_account).
var reUsername = regexp.MustCompile(`^[a-z0-9][a-z0-9._@+-]{1,62}[a-z0-9]$`)

// createSecretFile creates a new file of mode 0600 for a secret and writes it, or fails when the file
// exists; what names the secret in the messages.
func createSecretFile(path, what, value string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the path is a flag of the operator
	if err != nil {
		return fmt.Errorf("cannot create the %s file: %w", what, err)
	}
	_, werr := f.WriteString(value + "\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("cannot write the %s file: %w", what, werr)
	}
	return nil
}

func runInit(ctx context.Context, args []string, stdout, stderr io.Writer, environ []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("config", "", "configuration file")
	ttl := fs.Duration("ttl", 24*time.Hour, "lifetime of the key, from 5m to 720h")
	description := fs.String("description", "bootstrap", "description of the key")
	keyOut := fs.String("key-out", "", "write the key to this new file (mode 0600) instead of printing it")
	recoverKey := fs.Bool("recover", false, "issue the key although a usable platform key exists (every platform key is lost)")
	adminUsername := fs.String("admin-username", "", "also create the first administrator, a local user with this username and a generated password")
	passwordOut := fs.String("password-out", "", "write the administrator's password to this new file (mode 0600) instead of printing it")
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
	adminGiven := false
	fs.Visit(func(f *flag.Flag) { adminGiven = adminGiven || f.Name == "admin-username" })
	if adminGiven && !reUsername.MatchString(*adminUsername) {
		fmt.Fprintln(stderr, "pandokos: --admin-username must be 3 to 64 lower-case letters, digits and . _ @ + -, starting and ending with a letter or a digit")
		return 2
	}
	if *passwordOut != "" && !adminGiven {
		fmt.Fprintln(stderr, "pandokos: --password-out needs --admin-username")
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

	opts := admin.BootstrapOptions{TTL: *ttl, Description: *description, Recover: *recoverKey}
	if adminGiven {
		l := cfg.Auth.Local
		hasher, err := password.New(password.Params{
			MemoryKiB: uint32(l.Argon2.MemoryKiB), Iterations: uint32(l.Argon2.Iterations), Parallelism: uint8(l.Argon2.Parallelism), //nolint:gosec // bounded by the configuration
		}, l.MaxConcurrentHashes)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		opts.AdminUsername, opts.Hasher, opts.AdminPasswordTTL = *adminUsername, hasher, time.Duration(l.GeneratedPasswordTTL)
	}
	passwordWritten := false
	if *passwordOut != "" {
		opts.PasswordOut = func(pw secret.Secret) error {
			if err := createSecretFile(*passwordOut, "password", pw.Reveal()); err != nil {
				return err
			}
			passwordWritten = true
			return nil
		}
	}
	written := false
	opts.BeforeCommit = func(key secret.Secret, _ string, _ time.Time) error {
		if *keyOut == "" {
			return nil
		}
		if err := createSecretFile(*keyOut, "key", key.Reveal()); err != nil {
			return err // a file that was created and could not be written is removed by createSecretFile
		}
		written = true
		return nil
	}
	res, err := admin.Bootstrap(sigCtx, db, opts)
	if err != nil {
		if written {
			_ = os.Remove(*keyOut)
		}
		if passwordWritten {
			_ = os.Remove(*passwordOut)
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
	if adminGiven {
		fmt.Fprintf(stdout, "admin user: %s\n", *adminUsername)
		if *passwordOut != "" {
			fmt.Fprintf(stdout, "password written to %s\n", *passwordOut)
		} else {
			fmt.Fprintf(stdout, "password: %s\n", res.AdminPassword.Reveal()) // shown once, here
		}
		if !cfg.Auth.Local.Enabled {
			fmt.Fprintln(stderr, "warning: auth.local.enabled is false: the password signs in only when local accounts are switched on")
		}
	} else if has, err := account.HasAdministrator(sigCtx, db); err == nil && !has && cfg.Auth.Local.Enabled {
		fmt.Fprintln(stderr, `hint: no person can sign in yet: create the first administrator with "pandokos init --admin-username NAME"`)
	}
	fmt.Fprintln(stderr, `warning: this is a bootstrap key: create a platform key with "pandokos platform-key create", then revoke this one`)
	return 0
}
